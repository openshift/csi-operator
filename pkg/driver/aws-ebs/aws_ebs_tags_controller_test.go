package aws_ebs

import (
	"context"
	"reflect"
	"sort"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"

	configv1 "github.com/openshift/api/config/v1"
	fakeconfig "github.com/openshift/client-go/config/clientset/versioned/fake"
)

func TestGetAWSRegionFromInfrastructure(t *testing.T) {
	infra := &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{
				AWS: &configv1.AWSPlatformStatus{
					Region: "us-east-1",
				},
			},
		},
	}

	// Use the OpenShift fake clientset, not Kubernetes fake client
	fakeConfigClient := fakeconfig.NewSimpleClientset()

	// Add the infrastructure resource to the fake OpenShift client
	_, err := fakeConfigClient.ConfigV1().Infrastructures().Create(context.TODO(), infra, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create infrastructure resource: %v", err)
	}

	// Call the function to test if the region is correctly retrieved
	region, err := getAWSRegionFromInfrastructure(fakeConfigClient)
	if err != nil {
		t.Fatalf("unexpected error retrieving AWS region: %v", err)
	}

	expectedRegion := "us-east-1"
	if region != expectedRegion {
		t.Errorf("expected AWS region %s, got %s", expectedRegion, region)
	}
}

func TestGetAWSRegionFromInfrastructure_ErrorHandling(t *testing.T) {
	// Case where AWS region is missing in Infrastructure resource
	infra := &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{
				AWS: nil, // AWS platform status is missing
			},
		},
	}

	fakeConfigClient := fakeconfig.NewSimpleClientset()

	_, err := fakeConfigClient.ConfigV1().Infrastructures().Create(context.TODO(), infra, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create infrastructure resource: %v", err)
	}

	// Call the function to test if it returns an error due to missing AWS information
	_, err = getAWSRegionFromInfrastructure(fakeConfigClient)
	if err == nil {
		t.Errorf("expected error retrieving AWS region, but got none")
	}
}

// TestGetEC2Client tests if the EC2 client is correctly initialized using credentials from a Kubernetes secret.
func TestGetEC2Client(t *testing.T) {
	// Create a fake Kubernetes core client with a secret containing AWS credentials
	fakeCoreClient := fake.NewSimpleClientset().CoreV1()

	// Add a secret containing AWS credentials
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      awsSecretName,
			Namespace: awsSecretNamespace,
		},
		Data: map[string][]byte{
			"aws_access_key_id":     []byte("fake-access-key-id"),
			"aws_secret_access_key": []byte("fake-secret-access-key"),
		},
	}
	_, err := fakeCoreClient.Secrets(awsSecretNamespace).Create(context.TODO(), secret, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create secret: %v", err)
	}

	// Call getEC2Client and verify that the AWS session is created
	awsRegion := "us-east-1"
	client, err := getEC2Client(context.TODO(), fakeCoreClient, awsRegion)
	if err != nil {
		t.Fatalf("unexpected error creating EC2 client: %v", err)
	}

	// Check if the EC2 client is not nil (indicating successful creation)
	if client == nil {
		t.Errorf("expected non-nil EC2 client, got nil")
	}
}

func TestGetEC2Client_MissingCredentials(t *testing.T) {
	// Create a fake Kubernetes core client with a secret missing AWS credentials
	fakeCoreClient := fake.NewSimpleClientset().CoreV1()

	// Add a secret without AWS credentials
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      awsSecretName,
			Namespace: awsSecretNamespace,
		},
		Data: map[string][]byte{
			// No aws_access_key_id or aws_secret_access_key
			"some_other_field": []byte("some-value"),
		},
	}
	_, err := fakeCoreClient.Secrets(awsSecretNamespace).Create(context.TODO(), secret, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create secret: %v", err)
	}

	// Call getEC2Client and expect an error due to missing credentials
	awsRegion := "us-east-1"
	_, err = getEC2Client(context.TODO(), fakeCoreClient, awsRegion)
	if err == nil {
		t.Errorf("expected error due to missing credentials, but got none")
	}
}

func TestGetEC2Client_WithEncodedCredentialsHandledByAWS(t *testing.T) {
	// Create a fake Kubernetes core client
	fakeCoreClient := fake.NewSimpleClientset().CoreV1()

	encodedCredentials := "W2RlZmF1bHRdCmF3c19hY2NCaGtMei8ydnd4a3Zzc0dFSUtLQQ==" // base64 credential format

	// Create the secret with the base64 encoded 'credentials' field
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      awsSecretName,
			Namespace: awsSecretNamespace,
		},
		Data: map[string][]byte{
			"credentials": []byte(encodedCredentials),
		},
	}
	_, err := fakeCoreClient.Secrets(awsSecretNamespace).Create(context.TODO(), secret, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create secret: %v", err)
	}

	// Call getEC2Client and verify that the AWS session is created using the credentials file
	awsRegion := "us-east-1"
	client, err := getEC2Client(context.TODO(), fakeCoreClient, awsRegion)
	if err != nil {
		t.Fatalf("unexpected error creating EC2 client with credentials field: %v", err)
	}

	// Check if the EC2 client is not nil (indicating successful creation)
	if client == nil {
		t.Errorf("expected non-nil EC2 client, got nil")
	}
}

// TestUpdateEBSTags tests the logic of merging and updating AWS tags without making real AWS calls.
func TestUpdateEBSTags(t *testing.T) {
	// Define existing tags returned by AWS EC2
	existingTagsOutput := &ec2.DescribeTagsOutput{
		Tags: []*ec2.TagDescription{
			{
				Key:   aws.String("existing-key"),
				Value: aws.String("existing-value"),
			},
			{
				Key:   aws.String("unchanged-key"),
				Value: aws.String("unchanged-value"),
			},
		},
	}

	// Define new resource tags from the infrastructure resource
	resourceTags := []configv1.AWSResourceTag{
		{
			Key:   "new-key",
			Value: "new-value",
		},
		{
			Key:   "existing-key", // This will override the existing tag
			Value: "updated-value",
		},
	}

	// Call the internal function that merges and updates the tags
	mergedTags := mergeTags(existingTagsOutput.Tags, resourceTags)

	// Define the expected merged tags
	expectedTags := []*ec2.Tag{
		{
			Key:   aws.String("existing-key"),
			Value: aws.String("updated-value"), // Should be updated
		},
		{
			Key:   aws.String("unchanged-key"),
			Value: aws.String("unchanged-value"),
		},
		{
			Key:   aws.String("new-key"),
			Value: aws.String("new-value"),
		},
	}
	sortTags(expectedTags)
	sortTags(mergedTags)
	// Compare the merged tags with the expected tags
	if !reflect.DeepEqual(mergedTags, expectedTags) {
		t.Errorf("expected tags %v, got %v", expectedTags, mergedTags)
	}
}

// Helper function to sort tags by Key
func sortTags(tags []*ec2.Tag) {
	sort.Slice(tags, func(i, j int) bool {
		return *tags[i].Key < *tags[j].Key
	})
}
