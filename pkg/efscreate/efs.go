package efscreate

import (
	"fmt"
	"strings"
	"time"

	"k8s.io/klog/v2"

	v1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	awsefs "github.com/aws/aws-sdk-go/service/efs"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	volumeCreateInitialDelay  = 5 * time.Second
	volumeCreateBackoffFactor = 1.2
	volumeCreateBackoffSteps  = 10

	operationDelay          = 2 * time.Second
	operationBackoffFactor  = 1.2
	operationRetryCount     = 5
	tagFormat               = "kubernetes.io/cluster/%s"
	efsVolumeNameFormat     = "%s-efs"
	securityGroupNameFormat = "%s-sg"
)

type EFS struct {
	infra     *v1.Infrastructure
	client    *ec2.EC2
	efsClient *awsefs.EFS
	vpcID     string
	cidrBlock string
	subnetIDs []string
	resources *ResourceInfo
}

// store resources that the code created
type ResourceInfo struct {
	securityGroupID string
	efsID           string
	mountTargets    []string
}

func NewEFSSession(infra *v1.Infrastructure, sess *session.Session) *EFS {
	service := ec2.New(sess)
	efsClient := awsefs.New(sess)
	return &EFS{
		client:    service,
		efsClient: efsClient,
		infra:     infra,
		subnetIDs: []string{},
		resources: &ResourceInfo{},
	}
}

func (efs *EFS) CreateEFSVolume(nodes *corev1.NodeList) (string, error) {
	instances := efs.getInstanceIDs(nodes)

	klog.V(4).Info("Loading AWS VPC")
	err := efs.getSecurityInfo(instances)
	if err != nil {
		return "", err
	}

	klog.V(4).Info("Creating SecurityGroup")
	sgid, err := efs.createSecurityGroup()
	if err != nil {
		return "", err
	}
	efs.resources.securityGroupID = sgid

	klog.V(4).Info("Adding firewall rule for NFS")
	ok, err := efs.addFireWallRule()
	if err != nil || !ok {
		return "", fmt.Errorf("error adding firewall rule: %v", err)
	}

	klog.V(4).Info("Creating EFS volume")
	fileSystemID, err := efs.createEFSFileSystem()
	if err != nil {
		return "", err
	}
	efs.resources.efsID = fileSystemID

	klog.V(4).Info("Creating MountTargets")
	mts, err := efs.createMountTargets()
	if err != nil {
		return "", err
	}
	efs.resources.mountTargets = mts

	klog.V(4).Info("Waiting for MountTargets to get available")
	err = efs.waitForAvailableMountTarget()
	if err != nil {
		return fileSystemID, fmt.Errorf("waiting for mount targets to be available failed: %v", err)
	}
	log("successfully created file system %s", fileSystemID)
	return fileSystemID, nil
}

func (efs *EFS) getInstanceIDs(nodes *corev1.NodeList) []string {
	nodeIDs := sets.NewString()
	for _, node := range nodes.Items {
		//get providerID of the form aws:///us-west-2a/i-0304804a704fefb7d
		instanceString := node.Spec.ProviderID
		instanceStringArray := strings.Split(instanceString, "/")
		if len(instanceStringArray) > 0 {
			nodeIDs.Insert(instanceStringArray[len(instanceStringArray)-1])
		}
	}
	return nodeIDs.List()
}

func (efs *EFS) createSecurityGroup() (string, error) {
	infraID := efs.infra.Status.InfrastructureName
	groupName := fmt.Sprintf(securityGroupNameFormat, infraID)
	securityGroupInput := ec2.CreateSecurityGroupInput{
		Description:       aws.String("for testing efs driver"),
		GroupName:         aws.String(groupName),
		VpcId:             &efs.vpcID,
		TagSpecifications: efs.getTags(ec2.ResourceTypeSecurityGroup, groupName),
	}
	response, err := efs.client.CreateSecurityGroup(&securityGroupInput)
	if err != nil {
		return "", fmt.Errorf("error creating security group")
	}
	return *response.GroupId, nil
}

func (efs *EFS) getTags(resourceType string, resourceName string) []*ec2.TagSpecification {
	var tagList []*ec2.Tag
	tags := map[string]string{
		"Name":                 resourceName,
		efs.getClusterTagKey(): "owned",
	}
	for k, v := range tags {
		tagList = append(tagList, &ec2.Tag{
			Key: aws.String(k), Value: aws.String(v),
		})
	}
	return []*ec2.TagSpecification{
		{
			Tags:         tagList,
			ResourceType: aws.String(resourceType),
		},
	}
}

func (efs *EFS) getClusterTagKey() string {
	return fmt.Sprintf(tagFormat, efs.infra.Status.InfrastructureName)
}

func (efs *EFS) addFireWallRule() (bool, error) {
	ruleInput := ec2.AuthorizeSecurityGroupIngressInput{
		CidrIp:     aws.String(efs.cidrBlock),
		GroupId:    aws.String(efs.resources.securityGroupID),
		IpProtocol: aws.String("tcp"),
		ToPort:     aws.Int64(2049),
		FromPort:   aws.Int64(2049),
	}
	response, err := efs.client.AuthorizeSecurityGroupIngress(&ruleInput)
	if err != nil {
		return false, fmt.Errorf("error creating firewall rule: %v", err)
	}
	return *response.Return, nil
}

func log(msg string, args ...interface{}) {
	klog.Infof(msg, args...)
}

func (efs *EFS) createEFSFileSystem() (string, error) {
	volumeName := fmt.Sprintf(efsVolumeNameFormat, efs.infra.Status.InfrastructureName)
	input := &awsefs.CreateFileSystemInput{
		Encrypted:       aws.Bool(true),
		PerformanceMode: aws.String(awsefs.PerformanceModeGeneralPurpose),
		Tags: []*awsefs.Tag{
			{
				Key:   aws.String("Name"),
				Value: aws.String(volumeName),
			},
			{
				Key:   aws.String(efs.getClusterTagKey()),
				Value: aws.String("owned"),
			},
		},
	}
	response, err := efs.efsClient.CreateFileSystem(input)
	if err != nil {
		log("error creating filesystem: %v", err)
		return "", fmt.Errorf("error creating filesystem: %v", err)
	}
	err = efs.waitForEFSToBeAvailable(*response.FileSystemId)
	if err != nil {
		log("error waiting for filesystem to become available: %v", err)
		return *response.FileSystemId, fmt.Errorf("waiting for EFS filesystem to become available failed: %v", err)
	}
	return *response.FileSystemId, nil
}

func (efs *EFS) createMountTargets() ([]string, error) {
	var mountTargets []string
	for i := range efs.subnetIDs {
		subnet := efs.subnetIDs[i]
		mountTargetInput := &awsefs.CreateMountTargetInput{
			FileSystemId:   aws.String(efs.resources.efsID),
			SecurityGroups: []*string{aws.String(efs.resources.securityGroupID)},
			SubnetId:       aws.String(subnet),
		}
		mt, err := efs.efsClient.CreateMountTarget(mountTargetInput)
		if err != nil {
			return mountTargets, fmt.Errorf("error creating mount target: %v", err)
		}
		mountTargets = append(mountTargets, *mt.MountTargetId)
	}
	return mountTargets, nil
}

func (efs *EFS) waitForAvailableMountTarget() error {
	efsID := efs.resources.efsID
	describeInput := &awsefs.DescribeMountTargetsInput{FileSystemId: aws.String(efsID)}
	backoff := wait.Backoff{
		Duration: volumeCreateInitialDelay,
		Factor:   operationBackoffFactor,
		Steps:    volumeCreateBackoffSteps,
	}
	err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		response, describeErr := efs.efsClient.DescribeMountTargets(describeInput)
		if describeErr != nil {
			return false, describeErr
		}
		mountTargets := response.MountTargets
		if len(mountTargets) == 0 {
			return false, fmt.Errorf("no mount targets found associated with %s filesystem", efsID)
		}
		allReady := true
		for _, mt := range mountTargets {
			if *mt.LifeCycleState != awsefs.LifeCycleStateAvailable {
				allReady = false
			}
		}
		if allReady {
			return true, nil
		}
		return false, nil
	})
	return err
}

func (efs *EFS) waitForEFSToBeAvailable(efsID string) error {
	describeInput := &awsefs.DescribeFileSystemsInput{FileSystemId: aws.String(efsID)}
	backoff := wait.Backoff{
		Duration: volumeCreateInitialDelay,
		Factor:   volumeCreateBackoffFactor,
		Steps:    volumeCreateBackoffSteps,
	}
	err := wait.ExponentialBackoff(backoff, func() (done bool, err error) {
		response, err := efs.efsClient.DescribeFileSystems(describeInput)
		if err != nil {
			return false, err
		}
		filesystems := response.FileSystems
		if len(filesystems) < 1 {
			return false, nil
		}
		fs := filesystems[0]
		if *fs.LifeCycleState != awsefs.LifeCycleStateAvailable {
			return false, nil
		}
		return true, nil
	})
	return err
}

func (efs *EFS) getSecurityInfo(instances []string) error {
	var instancePointers []*string
	for i := range instances {
		instancePointers = append(instancePointers, &instances[i])
	}
	request := &ec2.DescribeInstancesInput{
		InstanceIds: instancePointers,
	}
	var results []*ec2.Instance
	var nextToken *string

	for {
		response, err := efs.client.DescribeInstances(request)
		if err != nil {
			return fmt.Errorf("error listing AWS instances: %v", err)
		}

		for _, reservation := range response.Reservations {
			results = append(results, reservation.Instances...)
		}

		nextToken = response.NextToken
		if nextToken == nil || len(*nextToken) == 0 {
			break
		}
		request.NextToken = nextToken
	}
	if len(results) < 1 {
		return fmt.Errorf("no matching instances found")
	}
	instance := results[0]
	efs.vpcID = *instance.VpcId

	vpcRequest := &ec2.DescribeVpcsInput{VpcIds: []*string{instance.VpcId}}
	response, err := efs.client.DescribeVpcs(vpcRequest)
	if err != nil {
		return fmt.Errorf("error listing vpc: %v", err)
	}
	clusterVPCs := response.Vpcs
	if len(clusterVPCs) < 1 {
		return fmt.Errorf("no matching vpc found for %s", efs.vpcID)
	}
	clusterVPC := clusterVPCs[0]
	efs.cidrBlock = *clusterVPC.CidrBlock

	subNetSet := sets.NewString()
	for i := range results {
		subNetSet.Insert(*results[i].SubnetId)
	}
	efs.subnetIDs = subNetSet.List()
	return nil
}
