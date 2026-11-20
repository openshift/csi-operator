package volumecreate

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	awsefs "github.com/aws/aws-sdk-go-v2/service/efs"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

const (
	efsStorageClassName  = "efs-sc"
	efsVolumeNameFormat  = "%s-efs"
	efsSecurityGroupName = "%s-sg"
	efsWaitInitialDelay  = 5 * time.Second
	efsWaitBackoffFactor = 1.2
	efsWaitSteps         = 10
)

type efsCreator struct {
	ctx       context.Context
	infraName string
	ec2       *ec2.Client
	efs       *awsefs.Client
	vpcID     string
	cidrBlock string
	subnetIDs []string
	groupID   string
	fsID      string
}

// RunEFS creates or describes EFS test infrastructure. It intentionally has no delete operation.
func RunEFS(ctx context.Context, kubeConfig *rest.Config, localCredentials bool, singleZone, operation string) error {
	if operation != "create" && operation != "describe" {
		return fmt.Errorf("unsupported EFS operation %q: use create or describe", operation)
	}
	if operation == "describe" && singleZone != "" {
		return fmt.Errorf("--single-zone is only valid with create")
	}
	return withAWSBootstrap(ctx, kubeConfig, localCredentials, func(base bootstrap) error {
		c := &efsCreator{ctx: ctx, infraName: base.infra.Status.InfrastructureName, ec2: ec2.NewFromConfig(base.aws), efs: awsefs.NewFromConfig(base.aws)}
		if operation == "describe" {
			return c.describe()
		}
		if err := c.create(base.kube, singleZone); err != nil {
			return err
		}
		return c.writeTestInputs(singleZone)
	})
}

func (c *efsCreator) create(kube *kubeclient.Clientset, singleZone string) error {
	nodes, err := kube.CoreV1().Nodes().List(c.ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list cluster nodes: %w", err)
	}
	network, err := discoverNetwork(c.ctx, c.ec2, nodes, singleZone)
	if err != nil {
		return err
	}
	c.vpcID, c.cidrBlock, c.subnetIDs = network.vpcID, network.cidrBlock, network.subnetIDs
	if c.groupID, err = c.createSecurityGroup(); err != nil {
		return err
	}
	if err := c.addFirewallRule(); err != nil {
		return err
	}
	if c.fsID, err = c.createFileSystem(singleZone); err != nil {
		return err
	}
	if err := c.createMountTargets(); err != nil {
		return err
	}
	if err := c.waitForMountTargets(); err != nil {
		return fmt.Errorf("waiting for mount targets to be available failed: %w", err)
	}
	klog.Infof("successfully created file system %s", c.fsID)
	return nil
}

func (c *efsCreator) createSecurityGroup() (string, error) {
	name := fmt.Sprintf(efsSecurityGroupName, c.infraName)
	groupID, err := createTaggedSecurityGroup(c.ctx, c.ec2, name, "for testing efs driver", c.vpcID, c.clusterTag())
	if err != nil {
		return "", fmt.Errorf("error creating security group: %w", err)
	}
	return groupID, nil
}

func (c *efsCreator) addFirewallRule() error {
	result, err := c.ec2.AuthorizeSecurityGroupIngress(c.ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		CidrIp: aws.String(c.cidrBlock), GroupId: aws.String(c.groupID), IpProtocol: aws.String("tcp"), FromPort: aws.Int32(2049), ToPort: aws.Int32(2049),
	})
	if err != nil {
		return fmt.Errorf("error creating firewall rule: %w", err)
	}
	if !aws.ToBool(result.Return) {
		return fmt.Errorf("error adding firewall rule")
	}
	return nil
}

func (c *efsCreator) createFileSystem(singleZone string) (string, error) {
	input := &awsefs.CreateFileSystemInput{Encrypted: aws.Bool(true), PerformanceMode: efstypes.PerformanceModeGeneralPurpose, Tags: []efstypes.Tag{
		{Key: aws.String("Name"), Value: aws.String(fmt.Sprintf(efsVolumeNameFormat, c.infraName))},
		{Key: aws.String(c.clusterTag()), Value: aws.String("owned")},
	}}
	if singleZone != "" {
		input.AvailabilityZoneName = aws.String(singleZone)
	}
	result, err := c.efs.CreateFileSystem(c.ctx, input)
	if err != nil {
		return "", fmt.Errorf("error creating filesystem: %w", err)
	}
	id := aws.ToString(result.FileSystemId)
	if err := c.waitForFileSystem(id); err != nil {
		return id, fmt.Errorf("waiting for EFS filesystem to become available failed: %w", err)
	}
	return id, nil
}

func (c *efsCreator) createMountTargets() error {
	for _, subnet := range c.subnetIDs {
		if _, err := c.efs.CreateMountTarget(c.ctx, &awsefs.CreateMountTargetInput{FileSystemId: aws.String(c.fsID), SecurityGroups: []string{c.groupID}, SubnetId: aws.String(subnet)}); err != nil {
			return fmt.Errorf("error creating mount target: %w", err)
		}
	}
	return nil
}

func (c *efsCreator) waitForFileSystem(id string) error {
	return wait.ExponentialBackoffWithContext(c.ctx, efsBackoff(), func(ctx context.Context) (bool, error) {
		result, err := c.efs.DescribeFileSystems(ctx, &awsefs.DescribeFileSystemsInput{FileSystemId: aws.String(id)})
		if err != nil {
			return false, err
		}
		return len(result.FileSystems) > 0 && result.FileSystems[0].LifeCycleState == efstypes.LifeCycleStateAvailable, nil
	})
}

func (c *efsCreator) waitForMountTargets() error {
	return wait.ExponentialBackoffWithContext(c.ctx, efsBackoff(), func(ctx context.Context) (bool, error) {
		result, err := c.efs.DescribeMountTargets(ctx, &awsefs.DescribeMountTargetsInput{FileSystemId: aws.String(c.fsID)})
		if err != nil {
			return false, err
		}
		if len(result.MountTargets) == 0 {
			return false, fmt.Errorf("no mount targets found associated with %s filesystem", c.fsID)
		}
		for _, target := range result.MountTargets {
			if target.LifeCycleState != efstypes.LifeCycleStateAvailable {
				return false, nil
			}
		}
		return true, nil
	})
}

func efsBackoff() wait.Backoff {
	return wait.Backoff{Duration: efsWaitInitialDelay, Factor: efsWaitBackoffFactor, Steps: efsWaitSteps}
}

func (c *efsCreator) describe() error {
	fileSystems, err := c.ownedFileSystems()
	if err != nil {
		return err
	}
	if len(fileSystems) == 0 {
		klog.Infof("EFS file systems for cluster infrastructure %q: absent", c.infraName)
	}
	for _, fs := range fileSystems {
		id := aws.ToString(fs.FileSystemId)
		klog.Infof("EFS file system %s: status=%s", id, fs.LifeCycleState)
		targets, err := c.efs.DescribeMountTargets(c.ctx, &awsefs.DescribeMountTargetsInput{FileSystemId: fs.FileSystemId})
		if err != nil {
			return fmt.Errorf("describe mount targets for EFS file system %s: %w", id, err)
		}
		if len(targets.MountTargets) == 0 {
			klog.Infof("EFS file system %s mount targets: absent", id)
		}
		for _, target := range targets.MountTargets {
			klog.Infof("EFS mount target %s: file-system=%s subnet=%s vpc=%s status=%s", aws.ToString(target.MountTargetId), id, aws.ToString(target.SubnetId), aws.ToString(target.VpcId), target.LifeCycleState)
		}
	}
	name := fmt.Sprintf(efsSecurityGroupName, c.infraName)
	groups, err := c.ec2.DescribeSecurityGroups(c.ctx, &ec2.DescribeSecurityGroupsInput{Filters: []ec2types.Filter{{Name: aws.String("group-name"), Values: []string{name}}, {Name: aws.String("tag:" + c.clusterTag()), Values: []string{"owned"}}}})
	if err != nil {
		return fmt.Errorf("describe EFS security group %s: %w", name, err)
	}
	if len(groups.SecurityGroups) == 0 {
		klog.Infof("EFS security group %s: absent", name)
	}
	for _, group := range groups.SecurityGroups {
		klog.Infof("EFS security group %s: present (id=%s vpc=%s)", name, aws.ToString(group.GroupId), aws.ToString(group.VpcId))
	}
	return nil
}

func (c *efsCreator) ownedFileSystems() ([]efstypes.FileSystemDescription, error) {
	var systems []efstypes.FileSystemDescription
	var marker *string
	for {
		result, err := c.efs.DescribeFileSystems(c.ctx, &awsefs.DescribeFileSystemsInput{Marker: marker})
		if err != nil {
			return nil, fmt.Errorf("describe EFS file systems: %w", err)
		}
		for _, fs := range result.FileSystems {
			if aws.ToString(fs.Name) == fmt.Sprintf(efsVolumeNameFormat, c.infraName) && hasEFSTag(fs.Tags, c.clusterTag(), "owned") {
				systems = append(systems, fs)
			}
		}
		if result.NextMarker == nil || *result.NextMarker == "" {
			return systems, nil
		}
		marker = result.NextMarker
	}
}

func hasEFSTag(tags []efstypes.Tag, key, value string) bool {
	for _, tag := range tags {
		if aws.ToString(tag.Key) == key && aws.ToString(tag.Value) == value {
			return true
		}
	}
	return false
}

func (c *efsCreator) clusterTag() string {
	return clusterTag(c.infraName)
}

func (c *efsCreator) writeTestInputs(singleZone string) error {
	storageClassAsset := "overlays/aws-efs/testing/sc.yaml"
	if singleZone != "" {
		storageClassAsset = "overlays/aws-efs/testing/sc-single-zone.yaml"
	}
	if err := writeAsset(storageClassAsset, storageClassLocation, map[string]string{"${storageclassname}": efsStorageClassName, "${filesystemid}": c.fsID, "${zone}": singleZone}); err != nil {
		return err
	}
	return writeAsset("overlays/aws-efs/testing/manifest.yaml", manifestLocation, map[string]string{"${storageclassname}": efsStorageClassName})
}
