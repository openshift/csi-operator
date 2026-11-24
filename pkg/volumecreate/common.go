package volumecreate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	"github.com/openshift/csi-operator/assets"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

const (
	awsCredentialsNamespace = "kube-system"
	awsCredentialsName      = "aws-creds"
	// ~10 minutes in total, longest gap 1m51s
	backoffWaitInitialDelay  = 5 * time.Second
	backoffWaitBackoffFactor = 1.2
	backoffWaitSteps         = 19
)

func defaultBackoff() wait.Backoff {
	return wait.Backoff{Duration: backoffWaitInitialDelay, Factor: backoffWaitBackoffFactor, Steps: backoffWaitSteps}
}

type networkInfo struct {
	vpcID        string
	cidrBlock    string
	subnetIDs    []string
	reservations []ec2types.Reservation
}

type genericCreator struct {
	kube      *kubeclient.Clientset
	awsConfig aws.Config
	ec2       *ec2.Client
	infraName string
	region    string
}

type Creator interface {
	Init(ctx context.Context, kubeConfig *rest.Config, localCredentials bool) error
	Create(ctx context.Context, singleZone string) error
	Delete(ctx context.Context, force bool) error
	Status(ctx context.Context) error
}

// newGenericCreator collects the cluster information and AWS clients shared by
// the service-specific creators. Network discovery is deferred until needed.
func newGenericCreator(ctx context.Context, kubeConfig *rest.Config, localCredentials bool) (*genericCreator, error) {
	kubeConfig = rest.AddUserAgent(kubeConfig, operatorName)
	kube, err := kubeclient.NewForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	configClient, err := configclient.NewForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("create infrastructure client: %w", err)
	}
	infra, err := configClient.ConfigV1().Infrastructures().Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get cluster infrastructure: %w", err)
	}
	if infra.Status.PlatformStatus == nil || infra.Status.PlatformStatus.AWS == nil {
		return nil, fmt.Errorf("cluster is not running on AWS")
	}
	awsConfig, err := loadAWSConfig(ctx, kube, infra.Status.PlatformStatus.AWS.Region, localCredentials)
	if err != nil {
		return nil, err
	}

	infraName := strings.ToLower(infra.Status.InfrastructureName)
	return &genericCreator{
		kube: kube, awsConfig: awsConfig, ec2: ec2.NewFromConfig(awsConfig),
		infraName: infraName, region: infra.Status.PlatformStatus.AWS.Region,
	}, nil
}

// loadAWSConfig uses the cluster's CI credentials unless local credentials were requested.
func loadAWSConfig(ctx context.Context, kube *kubeclient.Clientset, region string, localCredentials bool) (aws.Config, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return aws.Config{}, fmt.Errorf("load AWS configuration: %w", err)
	}
	if !localCredentials {
		secret, err := kube.CoreV1().Secrets(awsCredentialsNamespace).Get(ctx, awsCredentialsName, metav1.GetOptions{})
		if err != nil {
			return aws.Config{}, fmt.Errorf("get cluster AWS credentials: %w", err)
		}
		id, ok := secret.Data["aws_access_key_id"]
		if !ok {
			return aws.Config{}, fmt.Errorf("cluster AWS credential does not contain aws_access_key_id")
		}
		key, ok := secret.Data["aws_secret_access_key"]
		if !ok {
			return aws.Config{}, fmt.Errorf("cluster AWS credential does not contain aws_secret_access_key")
		}
		cfg.Credentials = aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(string(id), string(key), ""))
	}
	if _, err := cfg.Credentials.Retrieve(ctx); err != nil {
		return aws.Config{}, fmt.Errorf("retrieve AWS credentials: %w", err)
	}
	return cfg, nil
}

// discoverNetwork finds the cluster VPC, CIDR, and node subnets selected by --single-zone.
func discoverNetwork(ctx context.Context, client *ec2.Client, nodes *corev1.NodeList, singleZone string) (*networkInfo, error) {
	instances := make([]string, 0, len(nodes.Items))
	for _, node := range nodes.Items {
		parts := strings.Split(node.Spec.ProviderID, "/")
		if len(parts) == 0 || parts[len(parts)-1] == "" {
			return nil, fmt.Errorf("node %q has no AWS provider ID", node.Name)
		}
		instances = append(instances, parts[len(parts)-1])
	}
	request := &ec2.DescribeInstancesInput{InstanceIds: instances}
	var reservations []ec2types.Reservation
	for {
		result, err := client.DescribeInstances(ctx, request)
		if err != nil {
			return nil, fmt.Errorf("describe node instances: %w", err)
		}
		reservations = append(reservations, result.Reservations...)
		if result.NextToken == nil || *result.NextToken == "" {
			break
		}
		request.NextToken = result.NextToken
	}
	if len(reservations) == 0 || len(reservations[0].Instances) == 0 {
		return nil, fmt.Errorf("no instances found for cluster nodes")
	}
	first := reservations[0].Instances[0]
	subnetSet := sets.New[string]()
	for _, reservation := range reservations {
		for _, instance := range reservation.Instances {
			if singleZone == "" || aws.ToString(instance.Placement.AvailabilityZone) == singleZone {
				subnetSet.Insert(aws.ToString(instance.SubnetId))
			}
		}
	}
	if subnetSet.Len() == 0 {
		return nil, fmt.Errorf("no node subnets found in requested zone %q", singleZone)
	}
	vpcs, err := client.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{VpcIds: []string{aws.ToString(first.VpcId)}})
	if err != nil {
		return nil, fmt.Errorf("describe VPC %s: %w", aws.ToString(first.VpcId), err)
	}
	if len(vpcs.Vpcs) != 1 {
		return nil, fmt.Errorf("VPC %s not found", aws.ToString(first.VpcId))
	}
	return &networkInfo{vpcID: aws.ToString(first.VpcId), cidrBlock: aws.ToString(vpcs.Vpcs[0].CidrBlock), subnetIDs: sets.List(subnetSet), reservations: reservations}, nil
}

// clusterTag returns the ownership tag key for cluster-created resources.
func clusterTag(infraName string) string {
	return fmt.Sprintf(clusterTagFormat, infraName)
}

// createTaggedSecurityGroup creates an owned security group. Callers retain
// service-specific reuse and ingress behavior.
func createTaggedSecurityGroup(ctx context.Context, client *ec2.Client, name, description, vpcID, ownerTag string) (string, error) {
	result, err := client.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName: aws.String(name), Description: aws.String(description), VpcId: aws.String(vpcID),
		TagSpecifications: []ec2types.TagSpecification{{ResourceType: ec2types.ResourceTypeSecurityGroup, Tags: []ec2types.Tag{
			{Key: aws.String("Name"), Value: aws.String(name)},
			{Key: aws.String(ownerTag), Value: aws.String("owned")},
		}}},
	})
	if err != nil {
		return "", err
	}
	return aws.ToString(result.GroupId), nil
}

// writeAsset substitutes template values and writes to the configured path, or logs the result.
func writeAsset(asset, location string, replacements map[string]string) error {
	contents, err := assets.ReadFile(asset)
	if err != nil {
		return err
	}
	value := string(contents)
	for from, to := range replacements {
		value = strings.ReplaceAll(value, from, to)
	}
	if path := os.Getenv(location); path != "" {
		return os.WriteFile(path, []byte(value), 0640)
	}
	klog.Infof("No %s specified, generated:\n%s", location, value)
	return nil
}

// boundedName truncates an AWS resource name to the specified length limit.
func boundedName(name string, limit int) string {
	if len(name) > limit {
		return name[:limit]
	}
	return name
}

// isIAMPropagationError retries only explicit role visibility or propagation failures.
// Generic authorization, trust policy and validation failures may be permanent.
func isIAMPropagationError(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) || (apiErr.ErrorCode() != "ValidationException" && apiErr.ErrorCode() != "ResourceNotFoundException") {
		return false
	}
	message := strings.ToLower(apiErr.ErrorMessage())
	return strings.Contains(message, "role") && (strings.Contains(message, "propagat") || strings.Contains(message, "not yet") || strings.Contains(message, "role does not exist") || strings.Contains(message, "role not found"))
}

// isNotFound recognizes the AWS absence errors used by the helper's services.
func isNotFound(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NoSuchBucket" || apiErr.ErrorCode() == "NoSuchEntity" || apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "ResourceNotFoundException" || apiErr.ErrorCode() == "InvalidGroup.NotFound")
}
