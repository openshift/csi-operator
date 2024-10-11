package aws_ebs

import (
	"context"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"
	configv1 "github.com/openshift/api/config/v1"
	opv1 "github.com/openshift/api/operator/v1"
	fakeconfig "github.com/openshift/client-go/config/clientset/versioned/fake"
	"github.com/openshift/client-go/config/informers/externalversions"
	"github.com/openshift/csi-operator/pkg/clients"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
)

type FakeOperator struct {
	metav1.ObjectMeta
	Spec   opv1.OperatorSpec
	Status opv1.OperatorStatus
}

func TestEBSVolumeTagsController_Sync(t *testing.T) {
	ctx := context.TODO()

	fakeConfigClient := fakeconfig.NewSimpleClientset()
	informerFactory := externalversions.NewSharedInformerFactory(fakeConfigClient, 0)
	informerFactory.Config().V1().Infrastructures().Informer()

	// Test getEC2Client with valid and invalid AWS credentials
	t.Run("TestGetEC2Client", func(t *testing.T) {
		fakeCoreClient := fake.NewSimpleClientset()

		// Case 1: Valid credentials
		validSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      awsEBSSecretName,
				Namespace: awsEBSSecretNamespace,
			},
			Data: map[string][]byte{
				"aws_access_key_id":     []byte("test-access-key"),
				"aws_secret_access_key": []byte("test-secret-key"),
			},
		}
		_, err := fakeCoreClient.CoreV1().Secrets(awsEBSSecretNamespace).Create(ctx, validSecret, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("Failed to create secret for valid credentials: %v", err)
		}

		controller := &EBSVolumeTagsController{
			commonClient: &clients.Clients{KubeClient: fakeCoreClient},
		}

		awsRegion := "us-east-1"
		ec2Client, err := controller.getEC2Client(ctx, awsRegion)
		if err != nil {
			t.Fatalf("Expected EC2 client to be created without errors for valid credentials, but got: %v", err)
		}
		if ec2Client == nil {
			t.Fatalf("Expected non-nil EC2 client, but got nil")
		}

		// Case 2: Missing credentials
		invalidSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      awsEBSSecretName,
				Namespace: awsEBSSecretNamespace,
			},
			Data: map[string][]byte{
				"some_other_field": []byte("some-value"),
			},
		}
		_, err = fakeCoreClient.CoreV1().Secrets(awsEBSSecretNamespace).Update(ctx, invalidSecret, metav1.UpdateOptions{})
		if err != nil {
			t.Fatalf("Failed to create secret for invalid credentials: %v", err)
		}

		_, err = controller.getEC2Client(ctx, awsRegion)
		if err == nil {
			t.Fatalf("Expected error for missing AWS credentials, but got none")
		}
	})
}

// TestNewAndUpdatedTags checks that newAndUpdatedTags converts OpenShift AWS resource tags to AWS ec2.Tags correctly
func TestNewAndUpdatedTags(t *testing.T) {
	tests := []struct {
		name         string
		inputTags    []configv1.AWSResourceTag
		expectedTags []*ec2.Tag
	}{
		{
			name: "Single tag",
			inputTags: []configv1.AWSResourceTag{
				{Key: "key1", Value: "value1"},
			},
			expectedTags: []*ec2.Tag{
				{Key: aws.String("key1"), Value: aws.String("value1")},
			},
		},
		{
			name: "Multiple tags",
			inputTags: []configv1.AWSResourceTag{
				{Key: "key1", Value: "value1"},
				{Key: "key2", Value: "value2"},
			},
			expectedTags: []*ec2.Tag{
				{Key: aws.String("key1"), Value: aws.String("value1")},
				{Key: aws.String("key2"), Value: aws.String("value2")},
			},
		},
		{
			name:         "Empty tags",
			inputTags:    []configv1.AWSResourceTag{},
			expectedTags: []*ec2.Tag{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := newAndUpdatedTags(tt.inputTags)
			if len(result) != len(tt.expectedTags) {
				t.Fatalf("expected %d tags, got %d", len(tt.expectedTags), len(result))
			}

			for i, tag := range result {
				if *tag.Key != *tt.expectedTags[i].Key || *tag.Value != *tt.expectedTags[i].Value {
					t.Errorf("expected tag %v, got %v", tt.expectedTags[i], tag)
				}
			}
		})
	}
}

// TestVolumesIDsToResourceIDs checks that volumesIDsToResourceIDs converts a list of volume IDs to AWS resource IDs correctly
func TestVolumesIDsToResourceIDs(t *testing.T) {
	tests := []struct {
		name           string
		inputVolumeIDs []*v1.PersistentVolume
		expectedResult []*string
	}{
		{
			name: "pv-name",
			inputVolumeIDs: []*v1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "PV1",
					},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								VolumeHandle: "vol-1234",
							},
						},
					},
				},
			},
			expectedResult: []*string{aws.String("vol-1234")},
		},
		{
			name: "Multiple volume IDs",
			inputVolumeIDs: []*v1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "PV1",
					},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								VolumeHandle: "vol-1234",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "PV2",
					},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								VolumeHandle: "vol-5678",
							},
						},
					},
				},
			},
			expectedResult: []*string{aws.String("vol-1234"), aws.String("vol-5678")},
		},
		{
			name:           "No volume IDs",
			inputVolumeIDs: []*v1.PersistentVolume{},
			expectedResult: []*string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := pvsToResourceIDs(tt.inputVolumeIDs)
			if len(result) != len(tt.expectedResult) {
				t.Fatalf("expected %d resource IDs, got %d", len(tt.expectedResult), len(result))
			}

			for i, resourceID := range result {
				if *resourceID != *tt.expectedResult[i] {
					t.Errorf("expected resource ID %s, got %s", *tt.expectedResult[i], *resourceID)
				}
			}
		})
	}
}
