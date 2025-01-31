package aws_ebs

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"golang.org/x/time/rate"
	"gopkg.in/ini.v1"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/credentials/stscreds"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/sts"

	configv1 "github.com/openshift/api/config/v1"
	operatorapi "github.com/openshift/api/operator/v1"
	"github.com/openshift/csi-operator/pkg/clients"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
)

const (
	awsEBSSecretNamespace = "openshift-cluster-csi-drivers"
	awsEBSSecretName      = "ebs-cloud-credentials"
	driverName            = "ebs.csi.aws.com"
	tagHashAnnotationKey  = "ebs.openshift.io/volume-tags-hash"
	batchSize             = 50

	awsErrorVolumeNotFound = "InvalidVolume.NotFound"

	defaultReSyncPeriod = 30 * time.Minute
)

type EBSVolumeTagsController struct {
	name           string
	commonClient   *clients.Clients
	eventRecorder  events.Recorder
	failedQueue    workqueue.TypedRateLimitingInterface[string]
	failedSet      map[string]struct{}
	mutex          sync.Mutex
	awsSession     *session.Session
	sessionExpTime int64
	rateLimiter    *rate.Limiter
}

// TokenClaims represents the JWT claims
type TokenClaims struct {
	Exp int64 `json:"exp"` // Expiry timestamp
}

func NewEBSVolumeTagsController(
	name string,
	commonClient *clients.Clients,
	eventRecorder events.Recorder) factory.Controller {

	// 10 qps, 100 bucket size.
	rateLimiter := rate.NewLimiter(rate.Limit(10), 100)

	c := &EBSVolumeTagsController{
		name:          name,
		commonClient:  commonClient,
		eventRecorder: eventRecorder,
		failedQueue:   workqueue.NewTypedRateLimitingQueue[string](workqueue.NewTypedItemExponentialFailureRateLimiter[string](10*time.Second, 100*time.Hour)),
		rateLimiter:   rateLimiter,
		mutex:         sync.Mutex{},
		failedSet:     make(map[string]struct{}),
	}
	return factory.New().WithSync(
		c.Sync,
	).WithInformers(
		c.commonClient.ConfigInformers.Config().V1().Infrastructures().Informer(),
	).ResyncEvery(
		defaultReSyncPeriod,
	).WithPostStartHooks(
		c.startFailedQueueWorker,
	).ToController(
		name,
		eventRecorder,
	)
}

func (c *EBSVolumeTagsController) Sync(ctx context.Context, syncCtx factory.SyncContext) error {
	klog.Infof("EBSVolumeTagsController sync started")
	defer klog.Infof("EBSVolumeTagsController sync finished")

	opSpec, _, _, err := c.commonClient.OperatorClient.GetOperatorState()
	if err != nil {
		return err
	}
	if opSpec.ManagementState != operatorapi.Managed {
		return nil
	}

	infra, err := c.getInfrastructure()
	if err != nil {
		return err
	}
	if infra == nil {
		return nil
	}
	err = c.processInfrastructure(ctx, infra)
	if err != nil {
		return err
	}

	return nil
}

// getEC2Client retrieves AWS credentials from the secret and creates an AWS EC2 client using session.Options
func (c *EBSVolumeTagsController) getEC2Client(awsRegion string) (*ec2.EC2, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.awsSession == nil || c.isSessionExpired() {
		sess, err := c.createAWSSession(awsRegion)
		if err != nil {
			klog.Errorf("Failed to create AWS session: %v", err)
			return nil, err
		}
		c.awsSession = sess
		return ec2.New(c.awsSession), nil
	}
	return ec2.New(c.awsSession), nil
}

func (c *EBSVolumeTagsController) createAWSSession(awsRegion string) (*session.Session, error) {
	secret, err := c.getEBSCloudCredSecret()
	if err != nil {
		klog.Errorf("error getting secret: %v", err)
		return nil, fmt.Errorf("error retrieving AWS credentials secret: %v", err)
	}

	credentialsData, credentialsFound := secret.Data["credentials"]
	if credentialsFound {
		sess, err := c.createSessionWithCredentials(credentialsData, awsRegion)
		if err != nil {
			klog.Errorf("error creating session: %v", err)
			return nil, fmt.Errorf("error creating session: %v", err)
		}
		return sess, nil
	}
	return nil, fmt.Errorf("no valid AWS credentials found in secret")
}

func (c *EBSVolumeTagsController) createSessionWithCredentials(credentialsData []byte, region string) (*session.Session, error) {
	// Load INI file from credentialsData
	cfg, err := ini.Load(credentialsData)
	if err != nil {
		klog.Errorf("Error parsing INI credentials: %v", err)
		return nil, fmt.Errorf("error parsing credentials data: %v", err)
	}

	section := cfg.Section("default")
	roleARN := section.Key("role_arn").String()
	tokenFile := section.Key("web_identity_token_file").String()

	// Validate required fields
	if roleARN == "" || tokenFile == "" {
		return nil, fmt.Errorf("missing required AWS credentials: role_arn or web_identity_token_file is empty")
	}

	tokenExpirationTime, err := c.awsSessionExpirationTime(tokenFile)
	if err != nil {
		klog.Errorf("Error getting expiration time : %v", err)
		return nil, err
	}

	// Create base AWS session
	sess, err := session.NewSession(&aws.Config{
		Region: aws.String(region),
	})
	if err != nil {
		klog.Errorf("Error creating base AWS session: %v", err)
		return nil, fmt.Errorf("error creating AWS session: %v", err)
	}

	// Configure WebIdentityRoleProvider
	provider := stscreds.NewWebIdentityRoleProviderWithOptions(
		sts.New(sess),
		roleARN,
		"aws-ebs-csi-driver-operator", // Role session name
		stscreds.FetchTokenPath(tokenFile),
	)

	// Create new session with WebIdentity credentials
	sess, err = session.NewSession(&aws.Config{
		Region:      aws.String(region),
		Credentials: credentials.NewCredentials(provider),
	})
	if err != nil {
		klog.Errorf("Error creating AWS session with Web Identity: %v", err)
		return nil, fmt.Errorf("error creating AWS session with Web Identity: %v", err)
	}
	c.sessionExpTime = tokenExpirationTime
	return sess, nil
}

// awsSessionExpirationTime gives the token expiry time for session.
func (c *EBSVolumeTagsController) awsSessionExpirationTime(tokenFile string) (int64, error) {
	if tokenFile == "" {
		return 0, fmt.Errorf("token file not specified")
	}
	data, err := os.ReadFile(tokenFile)
	if err != nil {
		return 0, fmt.Errorf("failed to read token file: %v", err)
	}

	parts := strings.Split(string(data), ".")
	if len(parts) < 2 {
		return 0, fmt.Errorf("invalid JWT token format")
	}

	// Decode the payload (second part of the JWT)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, fmt.Errorf("failed to decode token payload: %v", err)
	}

	var claims TokenClaims
	if err = json.Unmarshal(payload, &claims); err != nil {
		return 0, fmt.Errorf("failed to unmarshal token claims: %v", err)
	}
	return claims.Exp, nil
}

// isSessionExpired check if token expiry time is exceeded.
func (c *EBSVolumeTagsController) isSessionExpired() bool {
	return c.sessionExpTime < time.Now().Unix()
}

// getInfrastructure retrieves the Infrastructure resource in OpenShift
func (c *EBSVolumeTagsController) getInfrastructure() (*configv1.Infrastructure, error) {
	infra, err := c.commonClient.ConfigInformers.Config().V1().Infrastructures().Lister().Get(infrastructureName)
	if err != nil {
		klog.Errorf("error listing infrastructures objects: %v", err)
		return nil, err
	}
	return infra, nil
}

func (c *EBSVolumeTagsController) getEBSCloudCredSecret() (*v1.Secret, error) {
	awsCreds, err := c.commonClient.KubeInformers.InformersFor(awsEBSSecretNamespace).Core().V1().Secrets().Lister().Secrets(awsEBSSecretNamespace).Get(awsEBSSecretName)
	if err != nil {
		klog.Errorf("error getting secret object: %v", err)
		return nil, err
	}
	return awsCreds, nil
}

// processInfrastructure processes the Infrastructure resource and updates EBS tags
func (c *EBSVolumeTagsController) processInfrastructure(ctx context.Context, infra *configv1.Infrastructure) error {
	if infra.Status.PlatformStatus != nil && infra.Status.PlatformStatus.AWS != nil &&
		infra.Status.PlatformStatus.AWS.ResourceTags != nil {
		err := c.fetchPVsAndUpdateTags(ctx, infra)
		if err != nil {
			klog.Errorf("error processing PVs for infrastructure update: %v", err)
			return err
		}
	}
	return nil
}

// fetchPVsAndUpdateTags retrieves all PVs and updates the AWS EBS tags in batches of 100
func (c *EBSVolumeTagsController) fetchPVsAndUpdateTags(ctx context.Context, infra *configv1.Infrastructure) error {
	pvs, err := c.listPersistentVolumes()
	if err != nil {
		return fmt.Errorf("error fetching PVs: %v", err)
	}
	// Compute the hash for the new set of tags
	newTagsHash := computeTagsHash(infra.Status.PlatformStatus.AWS.ResourceTags)
	pvsToBeUpdated := c.filterUpdatableVolumes(pvs, newTagsHash)

	// If there are no volumes to update, return early
	if len(pvsToBeUpdated) == 0 {
		klog.Infof("No volume tags to update as hashes are unchanged")
		return nil
	}

	var infraRegion = ""
	if infra.Status.PlatformStatus != nil && infra.Status.PlatformStatus.AWS != nil {
		infraRegion = infra.Status.PlatformStatus.AWS.Region
	}
	ec2Client, err := c.getEC2Client(infraRegion)
	if err != nil {
		return err
	}

	// Process the volumes in batches
	for i := 0; i < len(pvsToBeUpdated); i += batchSize {
		end := i + batchSize
		if end > len(pvsToBeUpdated) {
			end = len(pvsToBeUpdated)
		}
		batch := pvsToBeUpdated[i:end]

		// Update tags on AWS EBS volumes
		err = c.updateEBSTags(ctx, batch, ec2Client, infra.Status.PlatformStatus.AWS.ResourceTags)
		if err != nil {
			c.handleTagUpdateFailure(batch, err)
			continue
		}

		// Update PV annotations after successfully updating the tags in AWS
		for _, volume := range batch {
			// Set the new tag hash annotation in the PV object
			updatedVolume := setPVTagHash(volume, newTagsHash)

			// Update the PV with the new annotations
			err = c.updateVolume(ctx, updatedVolume)
			if err != nil {
				klog.Errorf("Error updating PV annotations for volume %s: %v", volume.Name, err)
				c.addToFailedQueue(volume.Name) // Retry updating annotation if update fails
				continue
			}
			klog.Infof("Successfully updated PV annotations and tags for volume %s", volume.Name)
		}
	}
	return nil
}

// updateEBSTags updates the tags of an AWS EBS volume with rate limiting
func (c *EBSVolumeTagsController) updateEBSTags(ctx context.Context, pvBatch []*v1.PersistentVolume, ec2Client *ec2.EC2,
	resourceTags []configv1.AWSResourceTag) error {
	err := c.rateLimiter.Wait(ctx)
	// If context is cancelled or rate limit cannot be acquired, return error
	if err != nil {
		klog.Errorf("rate limiter error: %v", err)
		return err
	}

	// Prepare tags
	tags := newAndUpdatedTags(resourceTags)
	// Create or update the tags
	_, err = ec2Client.CreateTags(&ec2.CreateTagsInput{
		Resources: pvsToResourceIDs(pvBatch),
		Tags:      tags,
	})
	if err != nil {
		return err
	}
	return nil
}

func (c *EBSVolumeTagsController) listPersistentVolumes() ([]*v1.PersistentVolume, error) {
	pvList, err := c.commonClient.KubeInformers.InformersFor("").Core().V1().PersistentVolumes().Lister().List(labels.Everything())
	if err != nil {
		klog.Errorf("error listing volumes objects: %v", err)
		return nil, err
	}
	return pvList, nil
}

func (c *EBSVolumeTagsController) updateVolume(ctx context.Context, pv *v1.PersistentVolume) error {
	_, err := c.commonClient.KubeClient.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{})
	if err != nil {
		klog.Errorf("error updating volume object %s: %v", pv.Name, err)
		return err
	}
	return nil
}

func (c *EBSVolumeTagsController) handleTagUpdateFailure(batch []*v1.PersistentVolume, updateErr error) {
	for _, pv := range batch {
		klog.Errorf("error updating volume %v tags: %v", pv.Name, updateErr)
		c.addToFailedQueue(pv.Name)
	}
	var pvNames []string
	for _, pv := range batch {
		pvNames = append(pvNames, pv.Name)
	}
	errorMessage := fmt.Sprintf("error updating tags for volume %v: %v", pvNames, updateErr)
	// Emit a warning event for the failure
	c.eventRecorder.Warning("EBSVolumeTagsUpdateFailed", fmt.Sprintf("failed to update tags for batch %v: %v", pvNames, errorMessage))
}

// newAndUpdatedTags adds and update existing AWS tags with new resource tags from OpenShift infrastructure
func newAndUpdatedTags(resourceTags []configv1.AWSResourceTag) []*ec2.Tag {
	// Convert map back to slice of ec2.Tag
	var tags []*ec2.Tag
	for _, tag := range resourceTags {
		tags = append(tags, &ec2.Tag{
			Key:   aws.String(tag.Key),
			Value: aws.String(tag.Value),
		})
	}
	return tags
}

func (c *EBSVolumeTagsController) filterUpdatableVolumes(volumes []*v1.PersistentVolume, newTagsHash string) []*v1.PersistentVolume {
	var updatablePVs []*v1.PersistentVolume

	for _, volume := range volumes {
		// Check if the volume is a CSI volume with the correct driver
		if volume.Spec.CSI != nil && volume.Spec.CSI.Driver == driverName &&
			// Ensure the volume is not already in the failed queue and include volumes whose tag hash is missing or different from the new hash
			!c.isVolumeInFailedQueue(volume.Name) && getPVTagHash(volume) != newTagsHash {

			// Add the volume to the list of updatable volumes
			updatablePVs = append(updatablePVs, volume)
		}
	}
	return updatablePVs
}

// isVolumeInFailedQueue checks if a volume name is currently in the failed queue
func (c *EBSVolumeTagsController) isVolumeInFailedQueue(volumeName string) bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	// Check if the volume name is in the set
	_, exists := c.failedSet[volumeName]
	return exists
}

// addToFailedQueue adds a volume name to the failed queue and tracks it in the set
func (c *EBSVolumeTagsController) addToFailedQueue(volumeName string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	// Add volume name to the queue and set
	c.failedQueue.AddRateLimited(volumeName)
	c.failedSet[volumeName] = struct{}{}
}

// removeFromFailedQueue removes a volume name from the queue and the set
func (c *EBSVolumeTagsController) removeFromFailedQueue(volumeName string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	// Remove volume name from the queue and set
	c.failedQueue.Forget(volumeName)
	delete(c.failedSet, volumeName)
}

func pvsToResourceIDs(volumes []*v1.PersistentVolume) []*string {
	var resourceIDs []*string
	for _, volume := range volumes {
		resourceIDs = append(resourceIDs, aws.String(volume.Spec.CSI.VolumeHandle))
	}
	return resourceIDs
}

// setPVTagHash stores the hash in the PV annotations.
func setPVTagHash(pv *v1.PersistentVolume, hash string) *v1.PersistentVolume {
	// Create a deep copy of the PersistentVolume to avoid modifying the cached object
	pvCopy := pv.DeepCopy()

	// Ensure the PV has an annotations map
	if pvCopy.Annotations == nil {
		pvCopy.Annotations = make(map[string]string)
	}

	// Set or update the tag hash annotation
	pvCopy.Annotations[tagHashAnnotationKey] = hash

	return pvCopy
}

// getPVTagHash gets the hash stored in the PV annotations.
// If no annotation is found, it returns an empty string, indicating no tags have been applied yet.
func getPVTagHash(pv *v1.PersistentVolume) string {
	// Check if the annotation exists
	if hash, found := pv.Annotations[tagHashAnnotationKey]; found {
		return hash
	}
	// If no annotation is found, return an empty string
	return ""
}

// computeTagsHash computes a hash for the sorted resource tags.
func computeTagsHash(resourceTags []configv1.AWSResourceTag) string {
	// Sort tags by key for consistency
	sort.Slice(resourceTags, func(i, j int) bool {
		return resourceTags[i].Key < resourceTags[j].Key
	})

	// Create a string representation of the sorted tags
	var tagsString string
	for _, tag := range resourceTags {
		tagsString += tag.Key + "=" + tag.Value + ";"
	}

	// Compute SHA256 hash of the tags string
	hash := sha256.Sum256([]byte(tagsString))
	return hex.EncodeToString(hash[:])
}
