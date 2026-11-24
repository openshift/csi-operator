// Package volumecreate creates AWS CSI test volumes and infrastructure.
package volumecreate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/s3files"
	s3filestypes "github.com/aws/aws-sdk-go-v2/service/s3files/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

const (
	operatorName            = "volume-create"
	operatorNamespace       = "openshift-cluster-csi-drivers"
	storageClassName        = "s3files-sc"
	storageClassLocation    = "STORAGECLASS_LOCATION"
	manifestLocation        = "MANIFEST_LOCATION"
	efsCSIDriverName        = "efs.csi.aws.com"
	directReadPolicySuffix  = "-s3files-direct-read"
	serviceBucketPolicyName = "S3FilesBucketAccess"
	clusterTagFormat        = "kubernetes.io/cluster/%s"
	resourceWaitInterval    = 10 * time.Second
	resourceWaitTimeout     = 10 * time.Minute
)

var managedPolicies = map[string][]string{
	// mount.s3files must read /var/run/aws/auth/credentials so pod mounts and
	// controller cleanup mounts use the respective CSI identity, not the EC2
	// instance role. Client permissions therefore belong on the CSI users only.
	// Retain the other instance-role permissions until separately validated;
	// this change only removes the client-policy attachment tested on-cluster.
	"instance": {
		"arn:aws:iam::aws:policy/service-role/AmazonS3FilesCSIDriverPolicy",
	},
	"controller": {
		"arn:aws:iam::aws:policy/AmazonS3FilesClientFullAccess",
		"arn:aws:iam::aws:policy/service-role/AmazonS3FilesCSIDriverPolicy",
		"arn:aws:iam::aws:policy/AmazonS3FilesFullAccess",
	},
	"node": {
		"arn:aws:iam::aws:policy/AmazonS3FilesClientFullAccess",
		"arn:aws:iam::aws:policy/service-role/AmazonS3FilesCSIDriverPolicy",
	},
}

type s3FilesCreator struct {
	*genericCreator
	iam     *iam.Client
	s3      *s3.Client
	s3files *s3files.Client
	sts     *sts.Client
	bucket  string
	role    string
	group   string
	policy  string
}

func NewS3FilesCreator() Creator {
	return &s3FilesCreator{}
}

func (c *s3FilesCreator) Init(ctx context.Context, kubeConfig *rest.Config, localCredentials bool) error {
	base, err := newGenericCreator(ctx, kubeConfig, localCredentials)
	if err != nil {
		return err
	}
	c.genericCreator = base
	c.iam = iam.NewFromConfig(base.awsConfig)
	c.s3 = s3.NewFromConfig(base.awsConfig)
	c.s3files = s3files.NewFromConfig(base.awsConfig)
	c.sts = sts.NewFromConfig(base.awsConfig)
	c.bucket = boundedName(c.infraName+"-s3files", 63)
	c.role = boundedName(c.infraName+"-s3files-role", 64)
	c.group = boundedName(c.infraName+"-s3files-sg", 255)
	c.policy = boundedName(c.infraName+directReadPolicySuffix, 128)
	return nil
}

func (c *s3FilesCreator) Create(ctx context.Context, singleZone string) error {
	account, err := c.accountID(ctx)
	if err != nil {
		return err
	}
	nodes, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list cluster nodes: %w", err)
	}
	vpc, cidr, subnets, instanceRoles, err := c.network(ctx, nodes, singleZone)
	if err != nil {
		return err
	}
	if err := c.createBucket(ctx); err != nil {
		return err
	}
	// The S3 Files service must be able to assume its bucket-access role before
	// the file system can be created. Network endpoints are created only after
	// the file system is available.
	roleARN, err := c.createRole(ctx, account)
	if err != nil {
		return err
	}
	fsID, err := c.createFileSystem(ctx, roleARN)
	if err != nil {
		return err
	}
	groupID, err := c.createSecurityGroup(ctx, vpc, cidr)
	if err != nil {
		return err
	}
	if err := c.createMountTargets(ctx, fsID, groupID, subnets); err != nil {
		return err
	}
	// These secrets belong to the installed EFS CSI driver, not this helper.
	// Map their access keys back to IAM users so both driver components receive
	// the policies that AWS requires for S3 Files provisioning and mounting.
	controllerUser, err := c.userForSecret(ctx, "aws-efs-cloud-credentials")
	if err != nil {
		return err
	}
	nodeUser, err := c.userForSecret(ctx, "node-aws-efs-cloud-credentials")
	if err != nil {
		return err
	}
	if err := c.grantPermissions(ctx, instanceRoles, controllerUser, nodeUser); err != nil {
		return err
	}
	if err := c.writeTestInputs(fsID); err != nil {
		return err
	}
	klog.Infof("created S3 Files file system %s backed by bucket %s", fsID, c.bucket)
	return nil
}

// cleanup removes the test infrastructure after checking for workloads unless forced.
func (c *s3FilesCreator) Delete(ctx context.Context, force bool) error {
	fsIDs, err := c.fileSystems(ctx)
	if err != nil {
		return err
	}
	if !force {
		if err := c.ensureNoWorkloads(ctx, fsIDs); err != nil {
			return err
		}
	}
	// Access points and mount targets must be removed before AWS accepts a
	// file-system deletion. Delete the file system before its security group,
	// because its managed network interfaces retain the group while deleting.
	for _, fsID := range fsIDs {
		if err := c.deleteFileSystem(ctx, fsID); err != nil {
			return err
		}
	}
	if err := c.deleteSecurityGroup(ctx); err != nil {
		return err
	}
	if err := c.revokePermissions(ctx); err != nil {
		return err
	}
	if err := c.deleteRole(ctx); err != nil {
		return err
	}
	return c.deleteBucket(ctx)
}

// status reports only resources whose deterministic name and ownership tag
// identify them as belonging to this cluster's test environment.
func (c *s3FilesCreator) Status(ctx context.Context) error {
	klog.Infof("S3 Files status for cluster infrastructure %q", c.infraName)

	if err := c.statusBucket(ctx); err != nil {
		return err
	}
	if err := c.statusRole(ctx); err != nil {
		return err
	}
	fileSystems, err := c.ownedFileSystems(ctx)
	if err != nil {
		return err
	}
	if len(fileSystems) == 0 {
		klog.Infof("S3 Files file systems for bucket %s: absent", c.bucket)
	}
	fsIDs := make([]string, 0, len(fileSystems))
	for _, fileSystem := range fileSystems {
		id := aws.ToString(fileSystem.FileSystemId)
		fsIDs = append(fsIDs, id)
		klog.Infof("S3 Files file system %s: status=%s", id, fileSystem.Status)
		if err := c.statusFileSystemEndpoints(ctx, id); err != nil {
			return err
		}
	}
	if err := c.statusSecurityGroups(ctx); err != nil {
		return err
	}
	return c.statusWorkloads(ctx, fsIDs)
}

// statusBucket reports bucket presence and versioning without modifying it.
func (c *s3FilesCreator) statusBucket(ctx context.Context) error {
	if _, err := c.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(c.bucket)}); err != nil {
		if isNotFound(err) {
			klog.Infof("S3 bucket %s: absent", c.bucket)
			return nil
		}
		return fmt.Errorf("check S3 bucket %s: %w", c.bucket, err)
	}
	versioning, err := c.s3.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(c.bucket)})
	if err != nil {
		return fmt.Errorf("get S3 bucket versioning for %s: %w", c.bucket, err)
	}
	status := string(versioning.Status)
	if status == "" {
		status = "unconfigured"
	}
	klog.Infof("S3 bucket %s: present, versioning=%s", c.bucket, status)
	return nil
}

// statusRole reports the helper role and its inline bucket policy.
func (c *s3FilesCreator) statusRole(ctx context.Context) error {
	role, err := c.iam.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(c.role)})
	if isNotFound(err) {
		klog.Infof("S3 Files helper IAM role %s: absent; inline policy %s: absent", c.role, serviceBucketPolicyName)
		return nil
	}
	if err != nil {
		return fmt.Errorf("get S3 Files helper IAM role %s: %w", c.role, err)
	}
	klog.Infof("S3 Files helper IAM role %s: present (%s)", c.role, aws.ToString(role.Role.Arn))
	if _, err := c.iam.GetRolePolicy(ctx, &iam.GetRolePolicyInput{RoleName: aws.String(c.role), PolicyName: aws.String(serviceBucketPolicyName)}); err != nil {
		if isNotFound(err) {
			klog.Infof("S3 Files helper IAM role inline policy %s: absent", serviceBucketPolicyName)
			return nil
		}
		return fmt.Errorf("get S3 Files helper IAM role inline policy %s: %w", serviceBucketPolicyName, err)
	}
	klog.Infof("S3 Files helper IAM role inline policy %s: present", serviceBucketPolicyName)
	return nil
}

// statusFileSystemEndpoints reports mount targets and access points for a file system.
func (c *s3FilesCreator) statusFileSystemEndpoints(ctx context.Context, fsID string) error {
	targets, err := c.mountTargets(ctx, fsID)
	if err != nil {
		return fmt.Errorf("list mount targets for S3 Files file system %s: %w", fsID, err)
	}
	if len(targets) == 0 {
		klog.Infof("S3 Files file system %s mount targets: absent", fsID)
	}
	for _, target := range targets {
		klog.Infof("S3 Files mount target %s: file-system=%s subnet=%s vpc=%s status=%s", aws.ToString(target.MountTargetId), fsID, aws.ToString(target.SubnetId), aws.ToString(target.VpcId), target.Status)
	}
	points, err := c.accessPoints(ctx, fsID)
	if err != nil {
		return fmt.Errorf("list access points for S3 Files file system %s: %w", fsID, err)
	}
	if len(points) == 0 {
		klog.Infof("S3 Files file system %s access points: absent", fsID)
	}
	for _, point := range points {
		klog.Infof("S3 Files access point %s: file-system=%s name=%s status=%s", aws.ToString(point.AccessPointId), fsID, aws.ToString(point.Name), point.Status)
	}
	return nil
}

// statusSecurityGroups reports security groups carrying this cluster's ownership tag.
func (c *s3FilesCreator) statusSecurityGroups(ctx context.Context) error {
	clusterTag := fmt.Sprintf(clusterTagFormat, c.infraName)
	groups, err := c.ec2.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{Filters: []ec2types.Filter{
		{Name: aws.String("group-name"), Values: []string{c.group}},
		{Name: aws.String("tag:" + clusterTag), Values: []string{"owned"}},
	}})
	if err != nil {
		return fmt.Errorf("describe S3 Files security group %s: %w", c.group, err)
	}
	if len(groups.SecurityGroups) == 0 {
		klog.Infof("S3 Files security group %s: absent", c.group)
	}
	for _, group := range groups.SecurityGroups {
		klog.Infof("S3 Files security group %s: present (id=%s vpc=%s)", c.group, aws.ToString(group.GroupId), aws.ToString(group.VpcId))
	}
	return nil
}

// statusWorkloads reports PVs and PVCs that would prevent cleanup.
func (c *s3FilesCreator) statusWorkloads(ctx context.Context, fsIDs []string) error {
	pvs, err := c.kube.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list persistent volumes for status: %w", err)
	}
	pvcs, err := c.kube.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list persistent volume claims for status: %w", err)
	}
	workloads := cleanupWorkloads(pvs.Items, pvcs.Items, fsIDs)
	if len(workloads) == 0 {
		klog.Infof("S3 Files Kubernetes PV/PVC workloads: absent")
		return nil
	}
	for _, workload := range workloads {
		klog.Infof("S3 Files Kubernetes workload: %s", workload)
	}
	return nil
}

// accountID returns the account of the configured AWS credentials.
func (c *s3FilesCreator) accountID(ctx context.Context) (string, error) {
	result, err := c.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", fmt.Errorf("get AWS caller identity: %w", err)
	}
	return aws.ToString(result.Account), nil
}

// network returns the cluster VPC, its CIDR, the node subnets that need mount
// targets and the distinct IAM roles of all node instances. Control-plane and
// worker nodes use different instance profiles, and both mount S3 Files.
func (c *s3FilesCreator) network(ctx context.Context, nodes *corev1.NodeList, singleZone string) (string, string, []string, []string, error) {
	network, err := discoverNetwork(ctx, c.ec2, nodes, singleZone)
	if err != nil {
		return "", "", nil, nil, err
	}
	profileNames, err := instanceProfileNames(network.reservations)
	if err != nil {
		return "", "", nil, nil, err
	}
	roleSet := sets.New[string]()
	for _, profileName := range profileNames {
		profile, err := c.iam.GetInstanceProfile(ctx, &iam.GetInstanceProfileInput{InstanceProfileName: aws.String(profileName)})
		if err != nil {
			return "", "", nil, nil, fmt.Errorf("get instance profile %s: %w", profileName, err)
		}
		if len(profile.InstanceProfile.Roles) != 1 {
			return "", "", nil, nil, fmt.Errorf("instance profile %s has no role", profileName)
		}
		roleSet.Insert(aws.ToString(profile.InstanceProfile.Roles[0].RoleName))
	}
	return network.vpcID, network.cidrBlock, network.subnetIDs, sets.List(roleSet), nil
}

// instanceProfileNames returns the sorted, distinct instance profile names of
// the given instances. Every instance must have an instance profile so that
// its role can be granted S3 Files mount permissions.
func instanceProfileNames(reservations []ec2types.Reservation) ([]string, error) {
	profiles := sets.New[string]()
	for _, reservation := range reservations {
		for _, instance := range reservation.Instances {
			if instance.IamInstanceProfile == nil {
				return nil, fmt.Errorf("instance %s has no IAM instance profile", aws.ToString(instance.InstanceId))
			}
			profiles.Insert(lastPathElement(aws.ToString(instance.IamInstanceProfile.Arn)))
		}
	}
	return sets.List(profiles), nil
}

// createBucket ensures the test bucket exists with versioning enabled.
func (c *s3FilesCreator) createBucket(ctx context.Context) error {
	// Versioning is a mandatory S3 Files prerequisite, including when a prior
	// interrupted run left the deterministic test bucket behind.
	if _, err := c.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(c.bucket)}); err != nil {
		input := &s3.CreateBucketInput{Bucket: aws.String(c.bucket)}
		if c.region != "us-east-1" {
			input.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{LocationConstraint: s3types.BucketLocationConstraint(c.region)}
		}
		if _, err := c.s3.CreateBucket(ctx, input); err != nil {
			return fmt.Errorf("create bucket %s: %w", c.bucket, err)
		}
	}
	_, err := c.s3.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{Bucket: aws.String(c.bucket), VersioningConfiguration: &s3types.VersioningConfiguration{Status: s3types.BucketVersioningStatusEnabled}})
	return err
}

// createRole ensures the service role and its bucket policy exist, returning its ARN.
func (c *s3FilesCreator) createRole(ctx context.Context, account string) (string, error) {
	trust, err := json.Marshal(serviceTrustPolicy(account, c.region))
	if err != nil {
		return "", err
	}
	roleResult, err := c.iam.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(c.role)})
	var roleARN string
	if isNotFound(err) {
		createdRole, err := c.iam.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String(c.role), AssumeRolePolicyDocument: aws.String(string(trust))})
		if err != nil {
			return "", fmt.Errorf("create role %s: %w", c.role, err)
		}
		roleARN = aws.ToString(createdRole.Role.Arn)
	} else if err != nil {
		return "", fmt.Errorf("get role %s: %w", c.role, err)
	} else {
		roleARN = aws.ToString(roleResult.Role.Arn)
	}
	policy, err := json.Marshal(serviceBucketPolicy(c.bucket, account, c.region))
	if err != nil {
		return "", err
	}
	if _, err := c.iam.PutRolePolicy(ctx, &iam.PutRolePolicyInput{RoleName: aws.String(c.role), PolicyName: aws.String(serviceBucketPolicyName), PolicyDocument: aws.String(string(policy))}); err != nil {
		return "", err
	}
	return roleARN, nil
}

// createFileSystem reuses an owned file system or creates one with bounded IAM propagation retries.
func (c *s3FilesCreator) createFileSystem(ctx context.Context, roleARN string) (string, error) {
	ids, err := c.fileSystems(ctx)
	if err != nil {
		return "", err
	}
	if len(ids) > 0 {
		return ids[0], c.waitForFileSystem(ctx, ids[0], "available")
	}
	// The bucket is unique to this helper invocation. Reusing an existing file
	// system makes retries safe while preserving cleanup's scoped lookup.
	input := &s3files.CreateFileSystemInput{
		Bucket: aws.String("arn:aws:s3:::" + c.bucket), RoleArn: aws.String(roleARN), ClientToken: aws.String(c.infraName + "-s3files"), AcceptBucketWarning: aws.Bool(true),
		Tags: []s3filestypes.Tag{{Key: aws.String("Name"), Value: aws.String(boundedName(c.infraName+"-s3files", 256))}, {Key: aws.String(fmt.Sprintf(clusterTagFormat, c.infraName)), Value: aws.String("owned")}},
	}
	var result *s3files.CreateFileSystemOutput
	for attempt, delay := 0, 2*time.Second; ; attempt++ {
		result, err = c.s3files.CreateFileSystem(ctx, input)
		if err == nil {
			break
		}
		if attempt == 5 || !isIAMPropagationError(err) {
			return "", fmt.Errorf("create S3 Files file system: %w", err)
		}
		if err := waitForContext(ctx, delay); err != nil {
			return "", err
		}
		delay = min(delay*2, 30*time.Second)
	}
	return aws.ToString(result.FileSystemId), c.waitForFileSystem(ctx, aws.ToString(result.FileSystemId), s3filestypes.LifeCycleStateAvailable)
}

// fileSystems returns the IDs of file systems owned by this cluster.
func (c *s3FilesCreator) fileSystems(ctx context.Context) ([]string, error) {
	fileSystems, err := c.ownedFileSystems(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(fileSystems))
	for _, fileSystem := range fileSystems {
		ids = append(ids, aws.ToString(fileSystem.FileSystemId))
	}
	return ids, nil
}

// ownedFileSystems lists all file systems in the test bucket carrying this cluster's ownership tag.
func (c *s3FilesCreator) ownedFileSystems(ctx context.Context) ([]s3filestypes.ListFileSystemsDescription, error) {
	var fileSystems []s3filestypes.ListFileSystemsDescription
	var nextToken *string
	for {
		result, err := c.s3files.ListFileSystems(ctx, &s3files.ListFileSystemsInput{Bucket: aws.String("arn:aws:s3:::" + c.bucket), NextToken: nextToken})
		if err != nil {
			return nil, fmt.Errorf("list S3 Files file systems: %w", err)
		}
		for _, fs := range result.FileSystems {
			owned, err := c.isOwnedFileSystem(ctx, aws.ToString(fs.FileSystemArn))
			if err != nil {
				return nil, err
			}
			if owned {
				fileSystems = append(fileSystems, fs)
			}
		}
		if result.NextToken == nil {
			return fileSystems, nil
		}
		nextToken = result.NextToken
	}
}

// isOwnedFileSystem checks all tag pages for this cluster's ownership tag.
func (c *s3FilesCreator) isOwnedFileSystem(ctx context.Context, arn string) (bool, error) {
	var nextToken *string
	var tags []s3filestypes.Tag
	for {
		result, err := c.s3files.ListTagsForResource(ctx, &s3files.ListTagsForResourceInput{ResourceId: aws.String(arn), NextToken: nextToken})
		if err != nil {
			return false, fmt.Errorf("list tags for S3 Files file system %s: %w", arn, err)
		}
		tags = append(tags, result.Tags...)
		if result.NextToken == nil {
			return hasTag(tags, fmt.Sprintf(clusterTagFormat, c.infraName), "owned"), nil
		}
		nextToken = result.NextToken
	}
}

// ensureNoWorkloads refuses cleanup while matching PVs or PVCs exist.
func (c *s3FilesCreator) ensureNoWorkloads(ctx context.Context, fsIDs []string) error {
	pvs, err := c.kube.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list persistent volumes before cleanup: %w", err)
	}
	pvcs, err := c.kube.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list persistent volume claims before cleanup: %w", err)
	}
	workloads := cleanupWorkloads(pvs.Items, pvcs.Items, fsIDs)
	if len(workloads) == 0 {
		return nil
	}
	return fmt.Errorf("refusing cleanup while S3 Files workloads exist: %s; delete them first or pass --force", strings.Join(workloads, ", "))
}

// hasTag reports whether a tag list contains the exact key and value.
func hasTag(tags []s3filestypes.Tag, key, value string) bool {
	for _, tag := range tags {
		if aws.ToString(tag.Key) == key && aws.ToString(tag.Value) == value {
			return true
		}
	}
	return false
}

// cleanupWorkloads returns sorted references to PVs and PVCs using the test infrastructure.
func cleanupWorkloads(pvs []corev1.PersistentVolume, pvcs []corev1.PersistentVolumeClaim, fsIDs []string) []string {
	fsIDSet := sets.New(fsIDs...)
	var workloads []string
	for _, pv := range pvs {
		if persistentVolumeUsesFileSystem(&pv, fsIDSet) {
			workloads = append(workloads, "PersistentVolume/"+pv.Name)
		}
	}
	for _, pvc := range pvcs {
		if pvc.Spec.StorageClassName != nil && *pvc.Spec.StorageClassName == storageClassName {
			workloads = append(workloads, "PersistentVolumeClaim/"+pvc.Namespace+"/"+pvc.Name)
		}
	}
	sort.Strings(workloads)
	return workloads
}

// persistentVolumeUsesFileSystem matches EFS CSI volume handles against the owned file systems.
func persistentVolumeUsesFileSystem(pv *corev1.PersistentVolume, fsIDs sets.Set[string]) bool {
	if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != efsCSIDriverName {
		return false
	}
	for fsID := range fsIDs {
		if strings.Contains(pv.Spec.CSI.VolumeHandle, "s3files:"+fsID) {
			return true
		}
	}
	return false
}

// waitForFileSystem waits for the requested state, stopping on failure, cancellation or timeout.
func (c *s3FilesCreator) waitForFileSystem(ctx context.Context, id string, want s3filestypes.LifeCycleState) error {
	deadline := time.Now().Add(resourceWaitTimeout)
	for time.Now().Before(deadline) {
		result, err := c.s3files.GetFileSystem(ctx, &s3files.GetFileSystemInput{FileSystemId: aws.String(id)})
		if err != nil {
			return fmt.Errorf("get S3 Files file system %s: %w", id, err)
		}
		if result.Status == want {
			return nil
		}
		if result.Status == s3filestypes.LifeCycleStateError {
			return fmt.Errorf("S3 Files file system %s failed: %s", id, aws.ToString(result.StatusMessage))
		}
		if err := waitForContext(ctx, resourceWaitInterval); err != nil {
			return err
		}
	}
	return fmt.Errorf("timed out waiting for S3 Files file system %s to become %s", id, want)
}

// createSecurityGroup ensures a tagged security group exists and repairs missing NFS ingress.
func (c *s3FilesCreator) createSecurityGroup(ctx context.Context, vpc, cidr string) (string, error) {
	groups, err := c.ec2.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{Filters: []ec2types.Filter{{Name: aws.String("group-name"), Values: []string{c.group}}, {Name: aws.String("vpc-id"), Values: []string{vpc}}}})
	if err != nil {
		return "", err
	}
	var groupID string
	if len(groups.SecurityGroups) > 0 {
		groupID = aws.ToString(groups.SecurityGroups[0].GroupId)
	} else {
		result, err := createTaggedSecurityGroup(ctx, c.ec2, c.group, "S3 Files NFS access for "+c.infraName, vpc, clusterTag(c.infraName))
		if err != nil {
			return "", err
		}
		groupID = result
	}
	_, err = c.ec2.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{GroupId: aws.String(groupID), IpPermissions: []ec2types.IpPermission{{IpProtocol: aws.String("tcp"), FromPort: aws.Int32(2049), ToPort: aws.Int32(2049), IpRanges: []ec2types.IpRange{{CidrIp: aws.String(cidr)}}}}})
	var apiErr smithy.APIError
	if err != nil && !(errors.As(err, &apiErr) && apiErr.ErrorCode() == "InvalidPermission.Duplicate") {
		return "", err
	}
	return groupID, nil
}

// createMountTargets creates missing subnet endpoints and waits until all targets are available.
func (c *s3FilesCreator) createMountTargets(ctx context.Context, fsID, groupID string, subnets []string) error {
	existing, err := c.mountTargets(ctx, fsID)
	if err != nil {
		return err
	}
	seen := sets.New[string]()
	for _, target := range existing {
		seen.Insert(aws.ToString(target.SubnetId))
	}
	for _, subnet := range subnets {
		if seen.Has(subnet) {
			continue
		}
		if _, err := c.s3files.CreateMountTarget(ctx, &s3files.CreateMountTargetInput{FileSystemId: aws.String(fsID), SubnetId: aws.String(subnet), SecurityGroups: []string{groupID}}); err != nil {
			return err
		}
	}
	// CSI tests can start as soon as this command exits, so wait for every
	// endpoint instead of exposing a StorageClass with still-creating targets.
	return c.waitForMountTargets(ctx, fsID, len(subnets))
}

// waitForMountTargets waits for the expected number of available endpoints.
func (c *s3FilesCreator) waitForMountTargets(ctx context.Context, fsID string, count int) error {
	deadline := time.Now().Add(resourceWaitTimeout)
	for time.Now().Before(deadline) {
		result, err := c.mountTargets(ctx, fsID)
		if err != nil {
			return err
		}
		if len(result) < count {
			if err := waitForContext(ctx, resourceWaitInterval); err != nil {
				return err
			}
			continue
		}
		available := true
		for _, target := range result {
			if target.Status == s3filestypes.LifeCycleStateError {
				return fmt.Errorf("S3 Files mount target failed: %s", aws.ToString(target.StatusMessage))
			}
			if target.Status != s3filestypes.LifeCycleStateAvailable {
				available = false
			}
		}
		if available {
			return nil
		}
		if err := waitForContext(ctx, resourceWaitInterval); err != nil {
			return err
		}
	}
	return fmt.Errorf("timed out waiting for mount targets of S3 Files file system %s", fsID)
}

// userForSecret resolves the IAM user owning the access key in a driver credential secret.
func (c *s3FilesCreator) userForSecret(ctx context.Context, name string) (string, error) {
	secret, err := c.kube.CoreV1().Secrets(operatorNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get %s: %w", name, err)
	}
	key, ok := secret.Data["aws_access_key_id"]
	if !ok {
		return "", fmt.Errorf("secret %s has no aws_access_key_id", name)
	}
	// GetAccessKeyLastUsed returns the owning user directly. Scanning every IAM
	// user and access key is prohibitively slow in shared CI accounts.
	result, err := c.iam.GetAccessKeyLastUsed(ctx, &iam.GetAccessKeyLastUsedInput{AccessKeyId: aws.String(string(key))})
	if err != nil {
		return "", fmt.Errorf("get IAM user for access key in secret %s: %w", name, err)
	}
	if result.UserName == nil {
		return "", fmt.Errorf("no IAM user owns the access key from secret %s", name)
	}
	return aws.ToString(result.UserName), nil
}

// grantPermissions grants the node roles and driver users the required mount and bucket permissions.
func (c *s3FilesCreator) grantPermissions(ctx context.Context, instanceRoles []string, controller, node string) error {
	// The managed S3 Files policies authorize the mount protocol and CSI access
	// point operations. Direct S3 reads are a separate AWS requirement for
	// efficient large-object reads, so scope an inline policy to this test bucket.
	policy, err := json.Marshal(directReadPolicy(c.bucket))
	if err != nil {
		return err
	}
	for _, role := range instanceRoles {
		for _, arn := range managedPolicies["instance"] {
			if _, err := c.iam.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{RoleName: aws.String(role), PolicyArn: aws.String(arn)}); err != nil {
				return fmt.Errorf("attach %s to role %s: %w", arn, role, err)
			}
		}
		if _, err := c.iam.PutRolePolicy(ctx, &iam.PutRolePolicyInput{RoleName: aws.String(role), PolicyName: aws.String(c.policy), PolicyDocument: aws.String(string(policy))}); err != nil {
			return fmt.Errorf("put policy %s on role %s: %w", c.policy, role, err)
		}
	}
	for kind, user := range map[string]string{"controller": controller, "node": node} {
		for _, arn := range managedPolicies[kind] {
			if _, err := c.iam.AttachUserPolicy(ctx, &iam.AttachUserPolicyInput{UserName: aws.String(user), PolicyArn: aws.String(arn)}); err != nil {
				return fmt.Errorf("attach %s to user %s: %w", arn, user, err)
			}
		}
		if _, err := c.iam.PutUserPolicy(ctx, &iam.PutUserPolicyInput{UserName: aws.String(user), PolicyName: aws.String(c.policy), PolicyDocument: aws.String(string(policy))}); err != nil {
			return fmt.Errorf("put policy %s on user %s: %w", c.policy, user, err)
		}
	}
	return nil
}

// writeTestInputs writes the generated StorageClass and CSI test manifest.
func (c *s3FilesCreator) writeTestInputs(fsID string) error {
	if err := writeAsset("overlays/aws-s3files/testing/sc.yaml", storageClassLocation, map[string]string{"${storageclassname}": storageClassName, "${filesystemid}": fsID}); err != nil {
		return err
	}
	return writeAsset("overlays/aws-s3files/testing/manifest.yaml", manifestLocation, map[string]string{"${storageclassname}": storageClassName})
}

// deleteFileSystem removes access points and endpoints before deleting and waiting for the file system.
func (c *s3FilesCreator) deleteFileSystem(ctx context.Context, fsID string) error {
	accessPoints, err := c.accessPoints(ctx, fsID)
	if err != nil {
		return err
	}
	for _, point := range accessPoints {
		if _, err := c.s3files.DeleteAccessPoint(ctx, &s3files.DeleteAccessPointInput{AccessPointId: point.AccessPointId}); err != nil {
			return err
		}
	}
	targets, err := c.mountTargets(ctx, fsID)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if _, err := c.s3files.DeleteMountTarget(ctx, &s3files.DeleteMountTargetInput{MountTargetId: target.MountTargetId}); err != nil {
			return err
		}
	}
	// Mount-target network interfaces are asynchronously deleted. Waiting here
	// prevents a transient dependency violation from failing file-system cleanup.
	if err := c.waitForMountTargetsDeleted(ctx, fsID); err != nil {
		return err
	}
	if _, err := c.s3files.DeleteFileSystem(ctx, &s3files.DeleteFileSystemInput{FileSystemId: aws.String(fsID), ForceDelete: aws.Bool(true)}); err != nil {
		return err
	}
	deadline := time.Now().Add(resourceWaitTimeout)
	for time.Now().Before(deadline) {
		result, err := c.s3files.GetFileSystem(ctx, &s3files.GetFileSystemInput{FileSystemId: aws.String(fsID)})
		if isNotFound(err) || result != nil && result.Status == s3filestypes.LifeCycleStateDeleted {
			return nil
		}
		if err != nil {
			return fmt.Errorf("get S3 Files file system %s during deletion: %w", fsID, err)
		}
		if err := waitForContext(ctx, resourceWaitInterval); err != nil {
			return err
		}
	}
	return fmt.Errorf("timed out waiting for S3 Files file system %s to be deleted", fsID)
}

// waitForMountTargetsDeleted waits until no endpoints remain for the file system.
func (c *s3FilesCreator) waitForMountTargetsDeleted(ctx context.Context, fsID string) error {
	deadline := time.Now().Add(resourceWaitTimeout)
	for time.Now().Before(deadline) {
		targets, err := c.mountTargets(ctx, fsID)
		if err != nil {
			return err
		}
		if len(targets) == 0 {
			return nil
		}
		if err := waitForContext(ctx, resourceWaitInterval); err != nil {
			return err
		}
	}
	return fmt.Errorf("timed out waiting for mount targets of S3 Files file system %s to be deleted", fsID)
}

// deleteSecurityGroup removes the helper security group if it exists.
func (c *s3FilesCreator) deleteSecurityGroup(ctx context.Context) error {
	groups, err := c.ec2.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{Filters: []ec2types.Filter{{Name: aws.String("group-name"), Values: []string{c.group}}}})
	if err != nil {
		return err
	}
	if len(groups.SecurityGroups) == 0 {
		return nil
	}
	_, err = c.ec2.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{GroupId: groups.SecurityGroups[0].GroupId})
	if isNotFound(err) {
		return nil
	}
	return err
}

// revokePermissions removes the helper's managed and inline policy grants from node roles and driver users.
func (c *s3FilesCreator) revokePermissions(ctx context.Context) error {
	nodes, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	_, _, _, instanceRoles, err := c.network(ctx, nodes, "")
	if err != nil {
		return err
	}
	controller, err := c.userForSecret(ctx, "aws-efs-cloud-credentials")
	if err != nil {
		return err
	}
	node, err := c.userForSecret(ctx, "node-aws-efs-cloud-credentials")
	if err != nil {
		return err
	}
	// Cleanup must be idempotent: a role or user may never have received a
	// policy, for example when a previous create run failed part way through.
	for _, role := range instanceRoles {
		for _, arn := range managedPolicies["instance"] {
			if _, err := c.iam.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{RoleName: aws.String(role), PolicyArn: aws.String(arn)}); err != nil && !isNotFound(err) {
				return fmt.Errorf("detach %s from role %s: %w", arn, role, err)
			}
		}
		if _, err := c.iam.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(role), PolicyName: aws.String(c.policy)}); err != nil && !isNotFound(err) {
			return fmt.Errorf("delete policy %s from role %s: %w", c.policy, role, err)
		}
	}
	for kind, user := range map[string]string{"controller": controller, "node": node} {
		for _, arn := range managedPolicies[kind] {
			if _, err := c.iam.DetachUserPolicy(ctx, &iam.DetachUserPolicyInput{UserName: aws.String(user), PolicyArn: aws.String(arn)}); err != nil && !isNotFound(err) {
				return fmt.Errorf("detach %s from user %s: %w", arn, user, err)
			}
		}
		if _, err := c.iam.DeleteUserPolicy(ctx, &iam.DeleteUserPolicyInput{UserName: aws.String(user), PolicyName: aws.String(c.policy)}); err != nil && !isNotFound(err) {
			return fmt.Errorf("delete policy %s from user %s: %w", c.policy, user, err)
		}
	}
	return nil
}

// deleteRole removes the service role and its inline policy if the role exists.
func (c *s3FilesCreator) deleteRole(ctx context.Context) error {
	if _, err := c.iam.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(c.role)}); isNotFound(err) {
		return nil
	}
	if _, err := c.iam.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(c.role), PolicyName: aws.String(serviceBucketPolicyName)}); err != nil && !isNotFound(err) {
		return err
	}
	_, err := c.iam.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(c.role)})
	return err
}

// deleteBucket removes all object versions and delete markers before deleting the test bucket.
func (c *s3FilesCreator) deleteBucket(ctx context.Context) error {
	// A versioned bucket cannot be removed until both object versions and delete
	// markers are removed. S3 limits DeleteObjects requests to 1,000 entries.
	var objects []s3types.ObjectIdentifier
	var keyMarker, versionMarker *string
	for {
		versions, err := c.s3.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{Bucket: aws.String(c.bucket), KeyMarker: keyMarker, VersionIdMarker: versionMarker})
		if isNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, version := range versions.Versions {
			objects = append(objects, s3types.ObjectIdentifier{Key: version.Key, VersionId: version.VersionId})
		}
		for _, marker := range versions.DeleteMarkers {
			objects = append(objects, s3types.ObjectIdentifier{Key: marker.Key, VersionId: marker.VersionId})
		}
		if !aws.ToBool(versions.IsTruncated) {
			break
		}
		keyMarker, versionMarker = versions.NextKeyMarker, versions.NextVersionIdMarker
	}
	for len(objects) > 0 {
		count := len(objects)
		if count > 1000 {
			count = 1000
		}
		out, err := c.s3.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: aws.String(c.bucket), Delete: &s3types.Delete{Objects: objects[:count], Quiet: aws.Bool(true)}})
		if err != nil {
			return fmt.Errorf("delete objects in bucket %s: %w", c.bucket, err)
		}
		if len(out.Errors) > 0 {
			failure := out.Errors[0]
			return fmt.Errorf("delete %d objects in bucket %s failed; first failure: key=%q version=%q code=%s message=%s", len(out.Errors), c.bucket, aws.ToString(failure.Key), aws.ToString(failure.VersionId), aws.ToString(failure.Code), aws.ToString(failure.Message))
		}
		objects = objects[count:]
	}
	_, err := c.s3.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(c.bucket)})
	return err
}

// mountTargets lists every page of mount targets for a file system.
func (c *s3FilesCreator) mountTargets(ctx context.Context, fsID string) ([]s3filestypes.ListMountTargetsDescription, error) {
	var targets []s3filestypes.ListMountTargetsDescription
	var nextToken *string
	for {
		result, err := c.s3files.ListMountTargets(ctx, &s3files.ListMountTargetsInput{FileSystemId: aws.String(fsID), NextToken: nextToken})
		if err != nil {
			return nil, err
		}
		targets = append(targets, result.MountTargets...)
		if result.NextToken == nil {
			return targets, nil
		}
		nextToken = result.NextToken
	}
}

// accessPoints lists every page of access points for a file system.
func (c *s3FilesCreator) accessPoints(ctx context.Context, fsID string) ([]s3filestypes.ListAccessPointsDescription, error) {
	var points []s3filestypes.ListAccessPointsDescription
	var nextToken *string
	for {
		result, err := c.s3files.ListAccessPoints(ctx, &s3files.ListAccessPointsInput{FileSystemId: aws.String(fsID), NextToken: nextToken})
		if err != nil {
			return nil, err
		}
		points = append(points, result.AccessPoints...)
		if result.NextToken == nil {
			return points, nil
		}
		nextToken = result.NextToken
	}
}

// lastPathElement returns the resource name after the final slash in an ARN.
func lastPathElement(arn string) string { parts := strings.Split(arn, "/"); return parts[len(parts)-1] }

// serviceTrustPolicy allows S3 Files to assume the role only for file systems in this account and region.
func serviceTrustPolicy(account, region string) map[string]any {
	return map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{
		"Sid": "AllowS3FilesAssumeRole", "Effect": "Allow", "Principal": map[string]string{"Service": "elasticfilesystem.amazonaws.com"}, "Action": "sts:AssumeRole",
		"Condition": map[string]any{"StringEquals": map[string]string{"aws:SourceAccount": account}, "ArnLike": map[string]string{"aws:SourceArn": fmt.Sprintf("arn:aws:s3files:%s:%s:file-system/*", region, account)}},
	}}}
}

// serviceBucketPolicy grants the service access to the test bucket and required KMS and EventBridge operations.
func serviceBucketPolicy(bucket, account, region string) map[string]any {
	return map[string]any{"Version": "2012-10-17", "Statement": []any{
		map[string]any{"Sid": "S3BucketPermissions", "Effect": "Allow", "Action": []string{"s3:ListBucket", "s3:ListBucketVersions"}, "Resource": "arn:aws:s3:::" + bucket, "Condition": map[string]any{"StringEquals": map[string]string{"aws:ResourceAccount": account}}},
		map[string]any{"Sid": "S3ObjectPermissions", "Effect": "Allow", "Action": []string{"s3:AbortMultipartUpload", "s3:DeleteObject*", "s3:GetObject*", "s3:List*", "s3:PutObject*"}, "Resource": "arn:aws:s3:::" + bucket + "/*", "Condition": map[string]any{"StringEquals": map[string]string{"aws:ResourceAccount": account}}},
		map[string]any{"Sid": "UseKmsKeyWithS3Files", "Effect": "Allow", "Action": []string{"kms:GenerateDataKey", "kms:Encrypt", "kms:Decrypt", "kms:ReEncryptFrom", "kms:ReEncryptTo"}, "Resource": fmt.Sprintf("arn:aws:kms:%s:%s:*", region, account), "Condition": map[string]any{"StringLike": map[string]any{"kms:ViaService": "s3." + region + ".amazonaws.com", "kms:EncryptionContext:aws:s3:arn": []string{"arn:aws:s3:::" + bucket, "arn:aws:s3:::" + bucket + "/*"}}}},
		map[string]any{"Sid": "EventBridgeManage", "Effect": "Allow", "Action": []string{"events:DeleteRule", "events:DisableRule", "events:EnableRule", "events:PutRule", "events:PutTargets", "events:RemoveTargets"}, "Resource": "arn:aws:events:*:*:rule/DO-NOT-DELETE-S3-Files*", "Condition": map[string]any{"StringEquals": map[string]string{"events:ManagedBy": "elasticfilesystem.amazonaws.com"}}},
		map[string]any{"Sid": "EventBridgeRead", "Effect": "Allow", "Action": []string{"events:DescribeRule", "events:ListRuleNamesByTarget", "events:ListRules", "events:ListTargetsByRule"}, "Resource": "arn:aws:events:*:*:rule/*"},
	}}
}

// directReadPolicy grants bucket listing and object reads scoped to the test bucket.
func directReadPolicy(bucket string) map[string]any {
	return map[string]any{"Version": "2012-10-17", "Statement": []any{
		map[string]any{"Sid": "S3ObjectReadAccess", "Effect": "Allow", "Action": []string{"s3:GetObject", "s3:GetObjectVersion"}, "Resource": "arn:aws:s3:::" + bucket + "/*"},
		map[string]any{"Sid": "S3BucketListAccess", "Effect": "Allow", "Action": "s3:ListBucket", "Resource": "arn:aws:s3:::" + bucket},
	}}
}
