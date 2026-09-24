// Package s3filescreate creates AWS S3 Files resources used by CSI tests.
package s3filescreate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/s3files"
	s3filestypes "github.com/aws/aws-sdk-go-v2/service/s3files/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	configv1 "github.com/openshift/api/config/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	"github.com/openshift/csi-operator/assets"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

const (
	operatorName            = "create-s3files-volume"
	credentialsNamespace    = "kube-system"
	credentialsName         = "aws-creds"
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
	// The worker mounts S3 Files, while the controller creates access points.
	// The node user receives the same mount and CSI permissions because the
	// EFS driver can use distinct controller and node credentials.
	"worker": {
		"arn:aws:iam::aws:policy/AmazonS3FilesClientFullAccess",
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

type creator struct {
	ctx       context.Context
	kube      *kubeclient.Clientset
	infra     *configv1.Infrastructure
	region    string
	ec2       *ec2.Client
	iam       *iam.Client
	s3        *s3.Client
	s3files   *s3files.Client
	sts       *sts.Client
	infraName string
	bucket    string
	role      string
	group     string
	policy    string
}

// Run performs the requested operation on the S3 Files test infrastructure.
func Run(ctx context.Context, controllerConfig *controllercmd.ControllerContext, localCredentials bool, singleZone string, force bool, operation string) error {
	if err := ValidateOperation(operation, singleZone, force); err != nil {
		return err
	}
	kube := kubeclient.NewForConfigOrDie(rest.AddUserAgent(controllerConfig.KubeConfig, operatorName))
	configClient := configclient.NewForConfigOrDie(rest.AddUserAgent(controllerConfig.KubeConfig, operatorName))
	infra, err := configClient.ConfigV1().Infrastructures().Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get cluster infrastructure: %w", err)
	}
	if infra.Status.PlatformStatus == nil || infra.Status.PlatformStatus.AWS == nil {
		return fmt.Errorf("cluster is not running on AWS")
	}

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(infra.Status.PlatformStatus.AWS.Region))
	if err != nil {
		return fmt.Errorf("load AWS configuration: %w", err)
	}
	if !localCredentials {
		// CI credentials are stored in the cluster and used by every AWS client.
		secret, err := kube.CoreV1().Secrets(credentialsNamespace).Get(ctx, credentialsName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get cluster AWS credentials: %w", err)
		}
		id, ok := secret.Data["aws_access_key_id"]
		if !ok {
			return fmt.Errorf("cluster AWS credential does not contain aws_access_key_id")
		}
		key, ok := secret.Data["aws_secret_access_key"]
		if !ok {
			return fmt.Errorf("cluster AWS credential does not contain aws_secret_access_key")
		}
		cfg.Credentials = aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(string(id), string(key), ""))
	}
	if _, err := cfg.Credentials.Retrieve(ctx); err != nil {
		return fmt.Errorf("retrieve AWS credentials: %w", err)
	}

	name := strings.ToLower(infra.Status.InfrastructureName)
	// Names are derived from the infrastructure ID so cleanup can identify
	// only resources belonging to this cluster's S3 Files test environment.
	c := &creator{
		ctx: ctx, kube: kube, infra: infra, region: infra.Status.PlatformStatus.AWS.Region,
		ec2: ec2.NewFromConfig(cfg), iam: iam.NewFromConfig(cfg), s3: s3.NewFromConfig(cfg), s3files: s3files.NewFromConfig(cfg), sts: sts.NewFromConfig(cfg),
		infraName: name, bucket: boundedName(name+"-s3files", 63), role: boundedName(name+"-s3files-role", 64),
		group: boundedName(name+"-s3files-sg", 255), policy: boundedName(name+directReadPolicySuffix, 128),
	}
	switch operation {
	case "delete":
		return c.cleanup(force)
	case "status":
		return c.status()
	case "create":
		return c.create(singleZone)
	}

	return fmt.Errorf("unsupported operation %q", operation)
}

// ValidateOperation validates the requested operation and its operation-specific flags.
func ValidateOperation(operation, singleZone string, force bool) error {
	switch operation {
	case "create":
		if force {
			return fmt.Errorf("--force is only valid with delete")
		}
	case "status":
		if singleZone != "" {
			return fmt.Errorf("--single-zone is only valid with create")
		}
		if force {
			return fmt.Errorf("--force is only valid with delete")
		}
	case "delete":
		if singleZone != "" {
			return fmt.Errorf("--single-zone is only valid with create")
		}
	default:
		return fmt.Errorf("unsupported operation %q: use create, status, or delete", operation)
	}
	return nil
}

func (c *creator) create(singleZone string) error {
	account, err := c.accountID()
	if err != nil {
		return err
	}
	nodes, err := c.kube.CoreV1().Nodes().List(c.ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list cluster nodes: %w", err)
	}
	vpc, cidr, subnets, workerRole, err := c.network(nodes, singleZone)
	if err != nil {
		return err
	}
	if err := c.createBucket(); err != nil {
		return err
	}
	// The S3 Files service must be able to assume its bucket-access role before
	// the file system can be created. Network endpoints are created only after
	// the file system is available.
	roleARN, err := c.createRole(account)
	if err != nil {
		return err
	}
	fsID, err := c.createFileSystem(roleARN)
	if err != nil {
		return err
	}
	groupID, err := c.createSecurityGroup(vpc, cidr)
	if err != nil {
		return err
	}
	if err := c.createMountTargets(fsID, groupID, subnets); err != nil {
		return err
	}
	// These secrets belong to the installed EFS CSI driver, not this helper.
	// Map their access keys back to IAM users so both driver components receive
	// the policies that AWS requires for S3 Files provisioning and mounting.
	controllerUser, err := c.userForSecret("aws-efs-cloud-credentials")
	if err != nil {
		return err
	}
	nodeUser, err := c.userForSecret("node-aws-efs-cloud-credentials")
	if err != nil {
		return err
	}
	if err := c.grantPermissions(workerRole, controllerUser, nodeUser); err != nil {
		return err
	}
	if err := c.writeTestInputs(fsID); err != nil {
		return err
	}
	klog.Infof("created S3 Files file system %s backed by bucket %s", fsID, c.bucket)
	return nil
}

func (c *creator) cleanup(force bool) error {
	fsIDs, err := c.fileSystems()
	if err != nil {
		return err
	}
	if !force {
		if err := c.ensureNoWorkloads(fsIDs); err != nil {
			return err
		}
	}
	// Access points and mount targets must be removed before AWS accepts a
	// file-system deletion. Delete the file system before its security group,
	// because its managed network interfaces retain the group while deleting.
	for _, fsID := range fsIDs {
		if err := c.deleteFileSystem(fsID); err != nil {
			return err
		}
	}
	if err := c.deleteSecurityGroup(); err != nil {
		return err
	}
	if err := c.revokePermissions(); err != nil {
		return err
	}
	if err := c.deleteRole(); err != nil {
		return err
	}
	return c.deleteBucket()
}

// status reports only resources whose deterministic name and ownership tag
// identify them as belonging to this cluster's test environment.
func (c *creator) status() error {
	klog.Infof("S3 Files status for cluster infrastructure %q", c.infraName)

	if err := c.statusBucket(); err != nil {
		return err
	}
	if err := c.statusRole(); err != nil {
		return err
	}
	fileSystems, err := c.ownedFileSystems()
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
		if err := c.statusFileSystemEndpoints(id); err != nil {
			return err
		}
	}
	if err := c.statusSecurityGroups(); err != nil {
		return err
	}
	return c.statusWorkloads(fsIDs)
}

func (c *creator) statusBucket() error {
	if _, err := c.s3.HeadBucket(c.ctx, &s3.HeadBucketInput{Bucket: aws.String(c.bucket)}); err != nil {
		if isNotFound(err) {
			klog.Infof("S3 bucket %s: absent", c.bucket)
			return nil
		}
		return fmt.Errorf("check S3 bucket %s: %w", c.bucket, err)
	}
	versioning, err := c.s3.GetBucketVersioning(c.ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(c.bucket)})
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

func (c *creator) statusRole() error {
	role, err := c.iam.GetRole(c.ctx, &iam.GetRoleInput{RoleName: aws.String(c.role)})
	if isNotFound(err) {
		klog.Infof("S3 Files helper IAM role %s: absent; inline policy %s: absent", c.role, serviceBucketPolicyName)
		return nil
	}
	if err != nil {
		return fmt.Errorf("get S3 Files helper IAM role %s: %w", c.role, err)
	}
	klog.Infof("S3 Files helper IAM role %s: present (%s)", c.role, aws.ToString(role.Role.Arn))
	if _, err := c.iam.GetRolePolicy(c.ctx, &iam.GetRolePolicyInput{RoleName: aws.String(c.role), PolicyName: aws.String(serviceBucketPolicyName)}); err != nil {
		if isNotFound(err) {
			klog.Infof("S3 Files helper IAM role inline policy %s: absent", serviceBucketPolicyName)
			return nil
		}
		return fmt.Errorf("get S3 Files helper IAM role inline policy %s: %w", serviceBucketPolicyName, err)
	}
	klog.Infof("S3 Files helper IAM role inline policy %s: present", serviceBucketPolicyName)
	return nil
}

func (c *creator) statusFileSystemEndpoints(fsID string) error {
	targets, err := c.mountTargets(fsID)
	if err != nil {
		return fmt.Errorf("list mount targets for S3 Files file system %s: %w", fsID, err)
	}
	if len(targets) == 0 {
		klog.Infof("S3 Files file system %s mount targets: absent", fsID)
	}
	for _, target := range targets {
		klog.Infof("S3 Files mount target %s: file-system=%s subnet=%s vpc=%s status=%s", aws.ToString(target.MountTargetId), fsID, aws.ToString(target.SubnetId), aws.ToString(target.VpcId), target.Status)
	}
	points, err := c.accessPoints(fsID)
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

func (c *creator) statusSecurityGroups() error {
	clusterTag := fmt.Sprintf(clusterTagFormat, c.infraName)
	groups, err := c.ec2.DescribeSecurityGroups(c.ctx, &ec2.DescribeSecurityGroupsInput{Filters: []ec2types.Filter{
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

func (c *creator) statusWorkloads(fsIDs []string) error {
	pvs, err := c.kube.CoreV1().PersistentVolumes().List(c.ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list persistent volumes for status: %w", err)
	}
	pvcs, err := c.kube.CoreV1().PersistentVolumeClaims("").List(c.ctx, metav1.ListOptions{})
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

func (c *creator) accountID() (string, error) {
	result, err := c.sts.GetCallerIdentity(c.ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", fmt.Errorf("get AWS caller identity: %w", err)
	}
	return aws.ToString(result.Account), nil
}

func (c *creator) network(nodes *corev1.NodeList, singleZone string) (string, string, []string, string, error) {
	instances := make([]string, 0, len(nodes.Items))
	for _, node := range nodes.Items {
		parts := strings.Split(node.Spec.ProviderID, "/")
		if len(parts) == 0 || parts[len(parts)-1] == "" {
			return "", "", nil, "", fmt.Errorf("node %q has no AWS provider ID", node.Name)
		}
		instances = append(instances, parts[len(parts)-1])
	}
	result, err := c.ec2.DescribeInstances(c.ctx, &ec2.DescribeInstancesInput{InstanceIds: instances})
	if err != nil {
		return "", "", nil, "", fmt.Errorf("describe worker instances: %w", err)
	}
	if len(result.Reservations) == 0 || len(result.Reservations[0].Instances) == 0 {
		return "", "", nil, "", fmt.Errorf("no instances found for cluster nodes")
	}
	first := result.Reservations[0].Instances[0]
	if first.IamInstanceProfile == nil {
		return "", "", nil, "", fmt.Errorf("instance %s has no IAM instance profile", aws.ToString(first.InstanceId))
	}
	subnetSet := map[string]struct{}{}
	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			if singleZone == "" || aws.ToString(instance.Placement.AvailabilityZone) == singleZone {
				subnetSet[aws.ToString(instance.SubnetId)] = struct{}{}
			}
		}
	}
	if len(subnetSet) == 0 {
		return "", "", nil, "", fmt.Errorf("no worker subnets found in requested zone %q", singleZone)
	}
	vpcs, err := c.ec2.DescribeVpcs(c.ctx, &ec2.DescribeVpcsInput{VpcIds: []string{aws.ToString(first.VpcId)}})
	if err != nil {
		return "", "", nil, "", fmt.Errorf("describe VPC %s: %w", aws.ToString(first.VpcId), err)
	}
	if len(vpcs.Vpcs) != 1 {
		return "", "", nil, "", fmt.Errorf("VPC %s not found", aws.ToString(first.VpcId))
	}
	profileName := lastPathElement(aws.ToString(first.IamInstanceProfile.Arn))
	profile, err := c.iam.GetInstanceProfile(c.ctx, &iam.GetInstanceProfileInput{InstanceProfileName: aws.String(profileName)})
	if err != nil {
		return "", "", nil, "", fmt.Errorf("get instance profile %s: %w", profileName, err)
	}
	if len(profile.InstanceProfile.Roles) != 1 {
		return "", "", nil, "", fmt.Errorf("instance profile %s has no role", profileName)
	}
	subnets := make([]string, 0, len(subnetSet))
	for subnet := range subnetSet {
		subnets = append(subnets, subnet)
	}
	sort.Strings(subnets)
	return aws.ToString(first.VpcId), aws.ToString(vpcs.Vpcs[0].CidrBlock), subnets, aws.ToString(profile.InstanceProfile.Roles[0].RoleName), nil
}

func (c *creator) createBucket() error {
	// Versioning is a mandatory S3 Files prerequisite, including when a prior
	// interrupted run left the deterministic test bucket behind.
	if _, err := c.s3.HeadBucket(c.ctx, &s3.HeadBucketInput{Bucket: aws.String(c.bucket)}); err != nil {
		input := &s3.CreateBucketInput{Bucket: aws.String(c.bucket)}
		if c.region != "us-east-1" {
			input.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{LocationConstraint: s3types.BucketLocationConstraint(c.region)}
		}
		if _, err := c.s3.CreateBucket(c.ctx, input); err != nil {
			return fmt.Errorf("create bucket %s: %w", c.bucket, err)
		}
	}
	_, err := c.s3.PutBucketVersioning(c.ctx, &s3.PutBucketVersioningInput{Bucket: aws.String(c.bucket), VersioningConfiguration: &s3types.VersioningConfiguration{Status: s3types.BucketVersioningStatusEnabled}})
	return err
}

func (c *creator) createRole(account string) (string, error) {
	trust, err := json.Marshal(serviceTrustPolicy(account, c.region))
	if err != nil {
		return "", err
	}
	roleResult, err := c.iam.GetRole(c.ctx, &iam.GetRoleInput{RoleName: aws.String(c.role)})
	var roleARN string
	if err != nil {
		createdRole, err := c.iam.CreateRole(c.ctx, &iam.CreateRoleInput{RoleName: aws.String(c.role), AssumeRolePolicyDocument: aws.String(string(trust))})
		if err != nil {
			return "", fmt.Errorf("create role %s: %w", c.role, err)
		}
		roleARN = aws.ToString(createdRole.Role.Arn)
	} else {
		roleARN = aws.ToString(roleResult.Role.Arn)
	}
	policy, err := json.Marshal(serviceBucketPolicy(c.bucket, account, c.region))
	if err != nil {
		return "", err
	}
	if _, err := c.iam.PutRolePolicy(c.ctx, &iam.PutRolePolicyInput{RoleName: aws.String(c.role), PolicyName: aws.String(serviceBucketPolicyName), PolicyDocument: aws.String(string(policy))}); err != nil {
		return "", err
	}
	return roleARN, nil
}

func (c *creator) createFileSystem(roleARN string) (string, error) {
	ids, err := c.fileSystems()
	if err != nil {
		return "", err
	}
	if len(ids) > 0 {
		return ids[0], c.waitForFileSystem(ids[0], "available")
	}
	// The bucket is unique to this helper invocation. Reusing an existing file
	// system makes retries safe while preserving cleanup's scoped lookup.
	result, err := c.s3files.CreateFileSystem(c.ctx, &s3files.CreateFileSystemInput{
		Bucket: aws.String("arn:aws:s3:::" + c.bucket), RoleArn: aws.String(roleARN), ClientToken: aws.String(c.infraName + "-s3files"), AcceptBucketWarning: aws.Bool(true),
		Tags: []s3filestypes.Tag{{Key: aws.String("Name"), Value: aws.String(boundedName(c.infraName+"-s3files", 256))}, {Key: aws.String(fmt.Sprintf(clusterTagFormat, c.infraName)), Value: aws.String("owned")}},
	})
	if err != nil {
		return "", fmt.Errorf("create S3 Files file system: %w", err)
	}
	return aws.ToString(result.FileSystemId), c.waitForFileSystem(aws.ToString(result.FileSystemId), s3filestypes.LifeCycleStateAvailable)
}

func (c *creator) fileSystems() ([]string, error) {
	fileSystems, err := c.ownedFileSystems()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(fileSystems))
	for _, fileSystem := range fileSystems {
		ids = append(ids, aws.ToString(fileSystem.FileSystemId))
	}
	return ids, nil
}

func (c *creator) ownedFileSystems() ([]s3filestypes.ListFileSystemsDescription, error) {
	var fileSystems []s3filestypes.ListFileSystemsDescription
	var nextToken *string
	for {
		result, err := c.s3files.ListFileSystems(c.ctx, &s3files.ListFileSystemsInput{Bucket: aws.String("arn:aws:s3:::" + c.bucket), NextToken: nextToken})
		if err != nil {
			return nil, fmt.Errorf("list S3 Files file systems: %w", err)
		}
		for _, fs := range result.FileSystems {
			owned, err := c.isOwnedFileSystem(aws.ToString(fs.FileSystemArn))
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

func (c *creator) isOwnedFileSystem(arn string) (bool, error) {
	var nextToken *string
	var tags []s3filestypes.Tag
	for {
		result, err := c.s3files.ListTagsForResource(c.ctx, &s3files.ListTagsForResourceInput{ResourceId: aws.String(arn), NextToken: nextToken})
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

func (c *creator) ensureNoWorkloads(fsIDs []string) error {
	pvs, err := c.kube.CoreV1().PersistentVolumes().List(c.ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list persistent volumes before cleanup: %w", err)
	}
	pvcs, err := c.kube.CoreV1().PersistentVolumeClaims("").List(c.ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list persistent volume claims before cleanup: %w", err)
	}
	workloads := cleanupWorkloads(pvs.Items, pvcs.Items, fsIDs)
	if len(workloads) == 0 {
		return nil
	}
	return fmt.Errorf("refusing cleanup while S3 Files workloads exist: %s; delete them first or pass --force", strings.Join(workloads, ", "))
}

func hasTag(tags []s3filestypes.Tag, key, value string) bool {
	for _, tag := range tags {
		if aws.ToString(tag.Key) == key && aws.ToString(tag.Value) == value {
			return true
		}
	}
	return false
}

func cleanupWorkloads(pvs []corev1.PersistentVolume, pvcs []corev1.PersistentVolumeClaim, fsIDs []string) []string {
	fsIDSet := make(map[string]struct{}, len(fsIDs))
	for _, fsID := range fsIDs {
		fsIDSet[fsID] = struct{}{}
	}
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

func persistentVolumeUsesFileSystem(pv *corev1.PersistentVolume, fsIDs map[string]struct{}) bool {
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

func (c *creator) waitForFileSystem(id string, want s3filestypes.LifeCycleState) error {
	deadline := time.Now().Add(resourceWaitTimeout)
	for time.Now().Before(deadline) {
		result, err := c.s3files.GetFileSystem(c.ctx, &s3files.GetFileSystemInput{FileSystemId: aws.String(id)})
		if err != nil {
			return fmt.Errorf("get S3 Files file system %s: %w", id, err)
		}
		if result.Status == want {
			return nil
		}
		if result.Status == s3filestypes.LifeCycleStateError {
			return fmt.Errorf("S3 Files file system %s failed: %s", id, aws.ToString(result.StatusMessage))
		}
		time.Sleep(resourceWaitInterval)
	}
	return fmt.Errorf("timed out waiting for S3 Files file system %s to become %s", id, want)
}

func (c *creator) createSecurityGroup(vpc, cidr string) (string, error) {
	groups, err := c.ec2.DescribeSecurityGroups(c.ctx, &ec2.DescribeSecurityGroupsInput{Filters: []ec2types.Filter{{Name: aws.String("group-name"), Values: []string{c.group}}, {Name: aws.String("vpc-id"), Values: []string{vpc}}}})
	if err != nil {
		return "", err
	}
	if len(groups.SecurityGroups) > 0 {
		return aws.ToString(groups.SecurityGroups[0].GroupId), nil
	}
	result, err := c.ec2.CreateSecurityGroup(c.ctx, &ec2.CreateSecurityGroupInput{GroupName: aws.String(c.group), Description: aws.String("S3 Files NFS access for " + c.infraName), VpcId: aws.String(vpc)})
	if err != nil {
		return "", err
	}
	if _, err := c.ec2.CreateTags(c.ctx, &ec2.CreateTagsInput{Resources: []string{aws.ToString(result.GroupId)}, Tags: []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String(c.group)}, {Key: aws.String(fmt.Sprintf(clusterTagFormat, c.infraName)), Value: aws.String("owned")}}}); err != nil {
		return "", err
	}
	if _, err := c.ec2.AuthorizeSecurityGroupIngress(c.ctx, &ec2.AuthorizeSecurityGroupIngressInput{GroupId: result.GroupId, IpPermissions: []ec2types.IpPermission{{IpProtocol: aws.String("tcp"), FromPort: aws.Int32(2049), ToPort: aws.Int32(2049), IpRanges: []ec2types.IpRange{{CidrIp: aws.String(cidr)}}}}}); err != nil {
		return "", err
	}
	return aws.ToString(result.GroupId), nil
}

func (c *creator) createMountTargets(fsID, groupID string, subnets []string) error {
	existing, err := c.mountTargets(fsID)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, target := range existing {
		seen[aws.ToString(target.SubnetId)] = true
	}
	for _, subnet := range subnets {
		if seen[subnet] {
			continue
		}
		if _, err := c.s3files.CreateMountTarget(c.ctx, &s3files.CreateMountTargetInput{FileSystemId: aws.String(fsID), SubnetId: aws.String(subnet), SecurityGroups: []string{groupID}}); err != nil {
			return err
		}
	}
	// CSI tests can start as soon as this command exits, so wait for every
	// endpoint instead of exposing a StorageClass with still-creating targets.
	return c.waitForMountTargets(fsID, len(subnets))
}

func (c *creator) waitForMountTargets(fsID string, count int) error {
	deadline := time.Now().Add(resourceWaitTimeout)
	for time.Now().Before(deadline) {
		result, err := c.mountTargets(fsID)
		if err != nil {
			return err
		}
		if len(result) < count {
			time.Sleep(resourceWaitInterval)
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
		time.Sleep(resourceWaitInterval)
	}
	return fmt.Errorf("timed out waiting for mount targets of S3 Files file system %s", fsID)
}

func (c *creator) userForSecret(name string) (string, error) {
	secret, err := c.kube.CoreV1().Secrets(operatorNamespace).Get(c.ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get %s: %w", name, err)
	}
	key, ok := secret.Data["aws_access_key_id"]
	if !ok {
		return "", fmt.Errorf("secret %s has no aws_access_key_id", name)
	}
	// GetAccessKeyLastUsed returns the owning user directly. Scanning every IAM
	// user and access key is prohibitively slow in shared CI accounts.
	result, err := c.iam.GetAccessKeyLastUsed(c.ctx, &iam.GetAccessKeyLastUsedInput{AccessKeyId: aws.String(string(key))})
	if err != nil {
		return "", fmt.Errorf("get IAM user for access key in secret %s: %w", name, err)
	}
	if result.UserName == nil {
		return "", fmt.Errorf("no IAM user owns the access key from secret %s", name)
	}
	return aws.ToString(result.UserName), nil
}

func (c *creator) grantPermissions(worker, controller, node string) error {
	identities := map[string]string{"worker": worker, "controller": controller, "node": node}
	// The managed S3 Files policies authorize the mount protocol and CSI access
	// point operations. Direct S3 reads are a separate AWS requirement for
	// efficient large-object reads, so scope an inline policy to this test bucket.
	policy, err := json.Marshal(directReadPolicy(c.bucket))
	if err != nil {
		return err
	}
	for kind, name := range identities {
		for _, arn := range managedPolicies[kind] {
			if kind == "worker" {
				_, err = c.iam.AttachRolePolicy(c.ctx, &iam.AttachRolePolicyInput{RoleName: aws.String(name), PolicyArn: aws.String(arn)})
			} else {
				_, err = c.iam.AttachUserPolicy(c.ctx, &iam.AttachUserPolicyInput{UserName: aws.String(name), PolicyArn: aws.String(arn)})
			}
			if err != nil {
				return err
			}
		}
		if kind == "worker" {
			_, err = c.iam.PutRolePolicy(c.ctx, &iam.PutRolePolicyInput{RoleName: aws.String(name), PolicyName: aws.String(c.policy), PolicyDocument: aws.String(string(policy))})
		} else {
			_, err = c.iam.PutUserPolicy(c.ctx, &iam.PutUserPolicyInput{UserName: aws.String(name), PolicyName: aws.String(c.policy), PolicyDocument: aws.String(string(policy))})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *creator) writeTestInputs(fsID string) error {
	if err := writeAsset("overlays/aws-s3files/testing/sc.yaml", storageClassLocation, map[string]string{"${storageclassname}": storageClassName, "${filesystemid}": fsID}); err != nil {
		return err
	}
	return writeAsset("overlays/aws-s3files/testing/manifest.yaml", manifestLocation, map[string]string{"${storageclassname}": storageClassName})
}

func (c *creator) deleteFileSystem(fsID string) error {
	accessPoints, err := c.accessPoints(fsID)
	if err != nil {
		return err
	}
	for _, point := range accessPoints {
		if _, err := c.s3files.DeleteAccessPoint(c.ctx, &s3files.DeleteAccessPointInput{AccessPointId: point.AccessPointId}); err != nil {
			return err
		}
	}
	targets, err := c.mountTargets(fsID)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if _, err := c.s3files.DeleteMountTarget(c.ctx, &s3files.DeleteMountTargetInput{MountTargetId: target.MountTargetId}); err != nil {
			return err
		}
	}
	// Mount-target network interfaces are asynchronously deleted. Waiting here
	// prevents a transient dependency violation from failing file-system cleanup.
	if err := c.waitForMountTargetsDeleted(fsID); err != nil {
		return err
	}
	if _, err := c.s3files.DeleteFileSystem(c.ctx, &s3files.DeleteFileSystemInput{FileSystemId: aws.String(fsID), ForceDelete: aws.Bool(true)}); err != nil {
		return err
	}
	deadline := time.Now().Add(resourceWaitTimeout)
	for time.Now().Before(deadline) {
		result, err := c.s3files.GetFileSystem(c.ctx, &s3files.GetFileSystemInput{FileSystemId: aws.String(fsID)})
		if isNotFound(err) || result != nil && result.Status == s3filestypes.LifeCycleStateDeleted {
			return nil
		}
		time.Sleep(resourceWaitInterval)
	}
	return fmt.Errorf("timed out waiting for S3 Files file system %s to be deleted", fsID)
}

func (c *creator) waitForMountTargetsDeleted(fsID string) error {
	deadline := time.Now().Add(resourceWaitTimeout)
	for time.Now().Before(deadline) {
		targets, err := c.mountTargets(fsID)
		if err != nil {
			return err
		}
		if len(targets) == 0 {
			return nil
		}
		time.Sleep(resourceWaitInterval)
	}
	return fmt.Errorf("timed out waiting for mount targets of S3 Files file system %s to be deleted", fsID)
}

func (c *creator) deleteSecurityGroup() error {
	groups, err := c.ec2.DescribeSecurityGroups(c.ctx, &ec2.DescribeSecurityGroupsInput{Filters: []ec2types.Filter{{Name: aws.String("group-name"), Values: []string{c.group}}}})
	if err != nil {
		return err
	}
	if len(groups.SecurityGroups) == 0 {
		return nil
	}
	_, err = c.ec2.DeleteSecurityGroup(c.ctx, &ec2.DeleteSecurityGroupInput{GroupId: groups.SecurityGroups[0].GroupId})
	if isNotFound(err) {
		return nil
	}
	return err
}

func (c *creator) revokePermissions() error {
	nodes, err := c.kube.CoreV1().Nodes().List(c.ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	_, _, _, worker, err := c.network(nodes, "")
	if err != nil {
		return err
	}
	controller, err := c.userForSecret("aws-efs-cloud-credentials")
	if err != nil {
		return err
	}
	node, err := c.userForSecret("node-aws-efs-cloud-credentials")
	if err != nil {
		return err
	}
	for kind, name := range map[string]string{"worker": worker, "controller": controller, "node": node} {
		for _, arn := range managedPolicies[kind] {
			if kind == "worker" {
				_, err = c.iam.DetachRolePolicy(c.ctx, &iam.DetachRolePolicyInput{RoleName: aws.String(name), PolicyArn: aws.String(arn)})
			} else {
				_, err = c.iam.DetachUserPolicy(c.ctx, &iam.DetachUserPolicyInput{UserName: aws.String(name), PolicyArn: aws.String(arn)})
			}
			if err != nil {
				return err
			}
		}
		if kind == "worker" {
			_, err = c.iam.DeleteRolePolicy(c.ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(name), PolicyName: aws.String(c.policy)})
		} else {
			_, err = c.iam.DeleteUserPolicy(c.ctx, &iam.DeleteUserPolicyInput{UserName: aws.String(name), PolicyName: aws.String(c.policy)})
		}
		if err != nil && !isNotFound(err) {
			return err
		}
	}
	return nil
}

func (c *creator) deleteRole() error {
	if _, err := c.iam.GetRole(c.ctx, &iam.GetRoleInput{RoleName: aws.String(c.role)}); isNotFound(err) {
		return nil
	}
	if _, err := c.iam.DeleteRolePolicy(c.ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(c.role), PolicyName: aws.String(serviceBucketPolicyName)}); err != nil && !isNotFound(err) {
		return err
	}
	_, err := c.iam.DeleteRole(c.ctx, &iam.DeleteRoleInput{RoleName: aws.String(c.role)})
	return err
}

func (c *creator) deleteBucket() error {
	// A versioned bucket cannot be removed until both object versions and delete
	// markers are removed. S3 limits DeleteObjects requests to 1,000 entries.
	var objects []s3types.ObjectIdentifier
	var keyMarker, versionMarker *string
	for {
		versions, err := c.s3.ListObjectVersions(c.ctx, &s3.ListObjectVersionsInput{Bucket: aws.String(c.bucket), KeyMarker: keyMarker, VersionIdMarker: versionMarker})
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
		if _, err := c.s3.DeleteObjects(c.ctx, &s3.DeleteObjectsInput{Bucket: aws.String(c.bucket), Delete: &s3types.Delete{Objects: objects[:count], Quiet: aws.Bool(true)}}); err != nil {
			return err
		}
		objects = objects[count:]
	}
	_, err := c.s3.DeleteBucket(c.ctx, &s3.DeleteBucketInput{Bucket: aws.String(c.bucket)})
	return err
}

func (c *creator) mountTargets(fsID string) ([]s3filestypes.ListMountTargetsDescription, error) {
	var targets []s3filestypes.ListMountTargetsDescription
	var nextToken *string
	for {
		result, err := c.s3files.ListMountTargets(c.ctx, &s3files.ListMountTargetsInput{FileSystemId: aws.String(fsID), NextToken: nextToken})
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

func (c *creator) accessPoints(fsID string) ([]s3filestypes.ListAccessPointsDescription, error) {
	var points []s3filestypes.ListAccessPointsDescription
	var nextToken *string
	for {
		result, err := c.s3files.ListAccessPoints(c.ctx, &s3files.ListAccessPointsInput{FileSystemId: aws.String(fsID), NextToken: nextToken})
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

func boundedName(name string, limit int) string {
	if len(name) > limit {
		return name[:limit]
	}
	return name
}

func isNotFound(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NoSuchBucket" || apiErr.ErrorCode() == "NoSuchEntity" || apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "ResourceNotFoundException" || apiErr.ErrorCode() == "InvalidGroup.NotFound")
}

func lastPathElement(arn string) string { parts := strings.Split(arn, "/"); return parts[len(parts)-1] }

func serviceTrustPolicy(account, region string) map[string]any {
	return map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{
		"Sid": "AllowS3FilesAssumeRole", "Effect": "Allow", "Principal": map[string]string{"Service": "elasticfilesystem.amazonaws.com"}, "Action": "sts:AssumeRole",
		"Condition": map[string]any{"StringEquals": map[string]string{"aws:SourceAccount": account}, "ArnLike": map[string]string{"aws:SourceArn": fmt.Sprintf("arn:aws:s3files:%s:%s:file-system/*", region, account)}},
	}}}
}

func serviceBucketPolicy(bucket, account, region string) map[string]any {
	return map[string]any{"Version": "2012-10-17", "Statement": []any{
		map[string]any{"Sid": "S3BucketPermissions", "Effect": "Allow", "Action": []string{"s3:ListBucket", "s3:ListBucketVersions"}, "Resource": "arn:aws:s3:::" + bucket, "Condition": map[string]any{"StringEquals": map[string]string{"aws:ResourceAccount": account}}},
		map[string]any{"Sid": "S3ObjectPermissions", "Effect": "Allow", "Action": []string{"s3:AbortMultipartUpload", "s3:DeleteObject*", "s3:GetObject*", "s3:List*", "s3:PutObject*"}, "Resource": "arn:aws:s3:::" + bucket + "/*", "Condition": map[string]any{"StringEquals": map[string]string{"aws:ResourceAccount": account}}},
		map[string]any{"Sid": "UseKmsKeyWithS3Files", "Effect": "Allow", "Action": []string{"kms:GenerateDataKey", "kms:Encrypt", "kms:Decrypt", "kms:ReEncryptFrom", "kms:ReEncryptTo"}, "Resource": fmt.Sprintf("arn:aws:kms:%s:%s:*", region, account), "Condition": map[string]any{"StringLike": map[string]any{"kms:ViaService": "s3." + region + ".amazonaws.com", "kms:EncryptionContext:aws:s3:arn": []string{"arn:aws:s3:::" + bucket, "arn:aws:s3:::" + bucket + "/*"}}}},
		map[string]any{"Sid": "EventBridgeManage", "Effect": "Allow", "Action": []string{"events:DeleteRule", "events:DisableRule", "events:EnableRule", "events:PutRule", "events:PutTargets", "events:RemoveTargets"}, "Resource": "arn:aws:events:*:*:rule/DO-NOT-DELETE-S3-Files*", "Condition": map[string]any{"StringEquals": map[string]string{"events:ManagedBy": "elasticfilesystem.amazonaws.com"}}},
		map[string]any{"Sid": "EventBridgeRead", "Effect": "Allow", "Action": []string{"events:DescribeRule", "events:ListRuleNamesByTarget", "events:ListRules", "events:ListTargetsByRule"}, "Resource": "arn:aws:events:*:*:rule/*"},
	}}
}

func directReadPolicy(bucket string) map[string]any {
	return map[string]any{"Version": "2012-10-17", "Statement": []any{
		map[string]any{"Sid": "S3ObjectReadAccess", "Effect": "Allow", "Action": []string{"s3:GetObject", "s3:GetObjectVersion"}, "Resource": "arn:aws:s3:::" + bucket + "/*"},
		map[string]any{"Sid": "S3BucketListAccess", "Effect": "Allow", "Action": "s3:ListBucket", "Resource": "arn:aws:s3:::" + bucket},
	}}
}
