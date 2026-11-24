package volumecreate

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	awsefs "github.com/aws/aws-sdk-go-v2/service/efs"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

const (
	efsStorageClassName  = "efs-sc"
	efsVolumeNameFormat  = "%s-efs"
	efsSecurityGroupName = "%s-sg"
)

type efsCreator struct {
	*genericCreator
	efs       *awsefs.Client
	vpcID     string
	cidrBlock string
	subnetIDs []string
	groupID   string
	fsID      string
}

func NewEFSCreator() Creator {
	return &efsCreator{}
}

func (c *efsCreator) Init(ctx context.Context, kubeConfig *rest.Config, localCredentials bool) error {
	var err error
	c.genericCreator, err = newGenericCreator(ctx, kubeConfig, localCredentials)
	if err != nil {
		return err
	}
	c.efs = awsefs.NewFromConfig(c.genericCreator.awsConfig)
	return nil
}

func (c *efsCreator) Create(ctx context.Context, singleZone string) error {
	nodes, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list cluster nodes: %w", err)
	}
	network, err := discoverNetwork(ctx, c.ec2, nodes, singleZone)
	if err != nil {
		return err
	}
	c.vpcID, c.cidrBlock, c.subnetIDs = network.vpcID, network.cidrBlock, network.subnetIDs
	if c.groupID, err = c.createSecurityGroup(ctx); err != nil {
		return err
	}
	if err := c.addFirewallRule(ctx); err != nil {
		return err
	}
	if c.fsID, err = c.createFileSystem(ctx, singleZone); err != nil {
		return err
	}
	if err := c.createMountTargets(ctx); err != nil {
		return err
	}
	if err := c.waitForMountTargets(ctx); err != nil {
		return fmt.Errorf("waiting for mount targets to be available failed: %w", err)
	}
	klog.Infof("successfully created file system %s", c.fsID)

	if err := c.writeTestInputs(singleZone); err != nil {
		return err
	}
	return nil
}

func (c *efsCreator) createSecurityGroup(ctx context.Context) (string, error) {
	name := fmt.Sprintf(efsSecurityGroupName, c.infraName)
	groupID, err := createTaggedSecurityGroup(ctx, c.ec2, name, "for testing efs driver", c.vpcID, c.clusterTag())
	if err != nil {
		return "", fmt.Errorf("error creating security group: %w", err)
	}
	return groupID, nil
}

func (c *efsCreator) addFirewallRule(ctx context.Context) error {
	result, err := c.ec2.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
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

func (c *efsCreator) createFileSystem(ctx context.Context, singleZone string) (string, error) {
	input := &awsefs.CreateFileSystemInput{Encrypted: aws.Bool(true), PerformanceMode: efstypes.PerformanceModeGeneralPurpose, Tags: []efstypes.Tag{
		{Key: aws.String("Name"), Value: aws.String(fmt.Sprintf(efsVolumeNameFormat, c.infraName))},
		{Key: aws.String(c.clusterTag()), Value: aws.String("owned")},
	}}
	if singleZone != "" {
		input.AvailabilityZoneName = aws.String(singleZone)
	}
	result, err := c.efs.CreateFileSystem(ctx, input)
	if err != nil {
		return "", fmt.Errorf("error creating filesystem: %w", err)
	}
	id := aws.ToString(result.FileSystemId)
	if err := c.waitForFileSystem(ctx, id); err != nil {
		return id, fmt.Errorf("waiting for EFS filesystem to become available failed: %w", err)
	}
	return id, nil
}

func (c *efsCreator) createMountTargets(ctx context.Context) error {
	for _, subnet := range c.subnetIDs {
		if _, err := c.efs.CreateMountTarget(ctx, &awsefs.CreateMountTargetInput{FileSystemId: aws.String(c.fsID), SecurityGroups: []string{c.groupID}, SubnetId: aws.String(subnet)}); err != nil {
			return fmt.Errorf("error creating mount target: %w", err)
		}
	}
	return nil
}

func (c *efsCreator) waitForFileSystem(ctx context.Context, id string) error {
	return wait.ExponentialBackoffWithContext(ctx, defaultBackoff(), func(ctx context.Context) (bool, error) {
		result, err := c.efs.DescribeFileSystems(ctx, &awsefs.DescribeFileSystemsInput{FileSystemId: aws.String(id)})
		if err != nil {
			return false, err
		}
		return len(result.FileSystems) > 0 && result.FileSystems[0].LifeCycleState == efstypes.LifeCycleStateAvailable, nil
	})
}

func (c *efsCreator) waitForMountTargets(ctx context.Context) error {
	return wait.ExponentialBackoffWithContext(ctx, defaultBackoff(), func(ctx context.Context) (bool, error) {
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

func (c *efsCreator) Status(ctx context.Context) error {
	fileSystems, err := c.ownedFileSystems(ctx)
	if err != nil {
		return err
	}
	if len(fileSystems) == 0 {
		klog.Infof("EFS file systems for cluster infrastructure %q: absent", c.infraName)
	}
	for _, fs := range fileSystems {
		id := aws.ToString(fs.FileSystemId)
		klog.Infof("EFS file system %s: status=%s", id, fs.LifeCycleState)
		targets, err := c.efs.DescribeMountTargets(ctx, &awsefs.DescribeMountTargetsInput{FileSystemId: fs.FileSystemId})
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
	groups, err := c.ec2.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{Filters: []ec2types.Filter{{Name: aws.String("group-name"), Values: []string{name}}, {Name: aws.String("tag:" + c.clusterTag()), Values: []string{"owned"}}}})
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

func (c *efsCreator) ownedFileSystems(ctx context.Context) ([]efstypes.FileSystemDescription, error) {
	var systems []efstypes.FileSystemDescription
	var marker *string
	for {
		result, err := c.efs.DescribeFileSystems(ctx, &awsefs.DescribeFileSystemsInput{Marker: marker})
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

func (c *efsCreator) Delete(ctx context.Context, force bool) error {
	return errors.New("delete operation not implemented for EFS")
}
