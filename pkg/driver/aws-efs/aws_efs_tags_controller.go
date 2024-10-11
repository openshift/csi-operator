package aws_efs

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/aws/aws-sdk-go/aws/credentials/stscreds"
	"github.com/aws/aws-sdk-go/service/sts"
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
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/efs"

	configv1 "github.com/openshift/api/config/v1"
	operatorapi "github.com/openshift/api/operator/v1"
	"github.com/openshift/csi-operator/pkg/clients"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
)

const (
	awsEFSSecretNamespace = "openshift-cluster-csi-drivers"
	awsEFSSecretName      = "aws-efs-cloud-credentials"
	efsDriverName         = "efs.csi.aws.com"
	tagHashAnnotationKey  = "efs.openshift.io/access-point-tags-hash"
	infrastructureName    = "cluster"

	defaultReSyncPeriod = 30 * time.Minute
)

type EFSAccessPointTagsController struct {
	name           string
	commonClient   *clients.Clients
	eventRecorder  events.Recorder
	failedQueue    workqueue.TypedRateLimitingInterface[string]
	failedSet      map[string]struct{} // A set to track added volume names
	mutex          sync.Mutex
	awsSession     *session.Session
	sessionExpTime int64
	rateLimiter    *rate.Limiter
}

// TokenClaims represents the JWT claims
type TokenClaims struct {
	Exp int64 `json:"exp"` // Expiry timestamp
}

func NewEFSAccessPointTagsController(
	name string,
	commonClient *clients.Clients,
	eventRecorder events.Recorder) factory.Controller {

	c := &EFSAccessPointTagsController{
		name:          name,
		commonClient:  commonClient,
		eventRecorder: eventRecorder,
		failedQueue:   workqueue.NewTypedRateLimitingQueue[string](workqueue.NewTypedItemExponentialFailureRateLimiter[string](10*time.Second, 100*time.Hour)),
		rateLimiter:   rate.NewLimiter(rate.Limit(10), 100),
		failedSet:     make(map[string]struct{}),
		mutex:         sync.Mutex{},
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

func (c *EFSAccessPointTagsController) Sync(ctx context.Context, syncCtx factory.SyncContext) error {
	klog.Infof("EFSAccessPointTagsController sync started")
	defer klog.Infof("EFSAccessPointTagsController sync finished")

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
	if infra == nil || infra.Status.PlatformStatus == nil || infra.Status.PlatformStatus.AWS == nil {
		return nil
	}
	err = c.processInfrastructure(ctx, infra)
	if err != nil {
		return err
	}

	return nil
}

// getEFSClient retrieves AWS credentials from the secret and creates an AWS EFS client using session.Options
func (c *EFSAccessPointTagsController) getEFSClient(awsRegion string) (*efs.EFS, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.awsSession == nil || c.isSessionExpired() {
		sess, err := c.createAWSSession(awsRegion)
		if err != nil {
			klog.Errorf("Failed to create AWS session: %v", err)
			return nil, err
		}
		c.awsSession = sess
		return efs.New(c.awsSession), nil
	}
	return efs.New(c.awsSession), nil
}

func (c *EFSAccessPointTagsController) createAWSSession(awsRegion string) (*session.Session, error) {
	secret, err := c.getEFSCloudCredSecret()
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

func (c *EFSAccessPointTagsController) createSessionWithCredentials(credentialsData []byte, region string) (*session.Session, error) {
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
func (c *EFSAccessPointTagsController) awsSessionExpirationTime(tokenFile string) (int64, error) {
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
func (c *EFSAccessPointTagsController) isSessionExpired() bool {
	return c.sessionExpTime < time.Now().Unix()
}

// getInfrastructure retrieves the Infrastructure resource in OpenShift
func (c *EFSAccessPointTagsController) getInfrastructure() (*configv1.Infrastructure, error) {
	infra, err := c.commonClient.ConfigInformers.Config().V1().Infrastructures().Lister().Get(infrastructureName)
	if err != nil {
		klog.Errorf("error listing infrastructures objects: %v", err)
		return nil, err
	}
	return infra, nil
}

func (c *EFSAccessPointTagsController) getEFSCloudCredSecret() (*v1.Secret, error) {
	awsCreds, err := c.commonClient.KubeInformers.InformersFor(awsEFSSecretNamespace).Core().V1().Secrets().Lister().Secrets(awsEFSSecretNamespace).Get(awsEFSSecretName)
	if err != nil {
		klog.Errorf("error getting secret object: %v", err)
		return nil, err
	}
	return awsCreds, nil
}

// processInfrastructure processes the Infrastructure resource and updates EFS tags
func (c *EFSAccessPointTagsController) processInfrastructure(ctx context.Context, infra *configv1.Infrastructure) error {
	if infra.Status.PlatformStatus != nil && infra.Status.PlatformStatus.AWS != nil &&
		infra.Status.PlatformStatus.AWS.ResourceTags != nil {
		err := c.fetchPVsAndUpdateTags(ctx, infra)
		if err != nil {
			klog.Errorf("Error processing PVs for infrastructure update: %v", err)
			return err
		}
	}
	return nil
}

// fetchPVsAndUpdateTags retrieves all PVs and updates the AWS EFS Access Points tags in batches of 100
func (c *EFSAccessPointTagsController) fetchPVsAndUpdateTags(ctx context.Context, infra *configv1.Infrastructure) error {
	pvs, err := c.listPersistentVolumes()
	if err != nil {
		return fmt.Errorf("error fetching PVs: %v", err)
	}
	// Compute the hash for the new set of tags
	newTagsHash := computeTagsHash(infra.Status.PlatformStatus.AWS.ResourceTags)
	pvsToBeUpdated := c.filterUpdatableVolumes(pvs, newTagsHash)

	// If there are no volumes to update, return early
	if len(pvsToBeUpdated) == 0 {
		klog.Infof("No volumes to update as tag hashes are unchanged")
		return nil
	}
	var infraRegion = ""
	if infra.Status.PlatformStatus != nil && infra.Status.PlatformStatus.AWS != nil {
		infraRegion = infra.Status.PlatformStatus.AWS.Region
	}
	efsClient, err := c.getEFSClient(infraRegion)
	if err != nil {
		return err
	}

	for _, volume := range pvsToBeUpdated {
		err = c.updateEFSAccessPointTags(ctx, volume, efsClient, infra.Status.PlatformStatus.AWS.ResourceTags)
		if err != nil {
			klog.Errorf("Error updating volume's AccessPoint %s tags: %v", volume.Name, err)
			c.eventRecorder.Warning("EFSAccessPointTagsUpdateFailed", fmt.Sprintf("Failed to update tags for batch %v: %v", volume.Name, err.Error()))
			c.addToFailedQueue(volume.Name)
			continue
		}
		// Set the new tag hash annotation in the PV object
		updatedPv := setPVTagHash(volume, newTagsHash)

		// Update the PV with the new annotations
		err = c.updateVolume(ctx, updatedPv)
		if err != nil {
			klog.Errorf("Error updating PV annotations for volume %s: %v", volume.Name, err)
			c.addToFailedQueue(volume.Name)
			continue
		}
		klog.Infof("Successfully updated PV annotations and access points tags for volume %s", volume.Name)
	}
	return nil
}

// updateEFSAccessPointTags updates the tags of an AWS EFS Access Points
func (c *EFSAccessPointTagsController) updateEFSAccessPointTags(ctx context.Context, pv *v1.PersistentVolume, efsClient *efs.EFS, resourceTags []configv1.AWSResourceTag) error {

	err := c.rateLimiter.Wait(ctx)
	if err != nil {
		klog.Errorf("Error waiting for rate limiter: %v", err)
		return err
	}

	tags := newAndUpdatedTags(resourceTags)

	// Create or update the tags
	_, err = efsClient.TagResource(&efs.TagResourceInput{
		ResourceId: aws.String(parseAccessPointID(pv.Spec.CSI.VolumeHandle)),
		Tags:       tags,
	})
	if err != nil {
		klog.Errorf("Error updating tags for PV %s: %v", pv.Spec.CSI.VolumeHandle, err)
		return err
	}
	return nil
}

func (c *EFSAccessPointTagsController) updateVolume(ctx context.Context, pv *v1.PersistentVolume) error {
	_, err := c.commonClient.KubeClient.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{})
	if err != nil {
		klog.Errorf("Error updating PV %s: %v", pv.Name, err)
		return err
	}
	return nil
}

func (c *EFSAccessPointTagsController) listPersistentVolumes() ([]*v1.PersistentVolume, error) {
	pvList, err := c.commonClient.KubeInformers.InformersFor("").Core().V1().PersistentVolumes().Lister().List(labels.Everything())
	if err != nil {
		klog.Errorf("error listing volumes objects: %v", err)
		return nil, err
	}
	return pvList, nil
}

// newAndUpdatedTags adds and update existing AWS tags with new resource tags from OpenShift infrastructure
func newAndUpdatedTags(resourceTags []configv1.AWSResourceTag) []*efs.Tag {
	// Convert map back to slice of EFS.Tag
	var tags []*efs.Tag
	for _, tag := range resourceTags {
		tags = append(tags, &efs.Tag{
			Key:   aws.String(tag.Key),
			Value: aws.String(tag.Value),
		})
	}
	return tags
}

func (c *EFSAccessPointTagsController) filterUpdatableVolumes(volumes []*v1.PersistentVolume, newTagsHash string) []*v1.PersistentVolume {
	var pvsToBeUpdated = make([]*v1.PersistentVolume, 0)
	for _, volume := range volumes {
		if volume.Spec.CSI != nil && volume.Spec.CSI.Driver == efsDriverName &&
			parseAccessPointID(volume.Spec.CSI.VolumeHandle) != "" && !c.isVolumeInFailedQueue(volume.Name) {
			existingHash := getPVTagHash(volume)
			if existingHash == "" || existingHash != newTagsHash {
				pvsToBeUpdated = append(pvsToBeUpdated, volume)
			}
		}
	}
	return pvsToBeUpdated
}

// isVolumeInFailedQueue checks if a volume name is currently in the failed queue
func (c *EFSAccessPointTagsController) isVolumeInFailedQueue(volumeName string) bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	// Check if the volume name is in the set
	_, exists := c.failedSet[volumeName]
	return exists
}

// addToFailedQueue adds a volume name to the failed queue and tracks it in the set
func (c *EFSAccessPointTagsController) addToFailedQueue(volumeName string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	// Add volume name to the queue and set
	c.failedQueue.AddRateLimited(volumeName)
	c.failedSet[volumeName] = struct{}{}
}

// removeFromFailedQueue removes a volume name from the queue and the set
func (c *EFSAccessPointTagsController) removeFromFailedQueue(volumeName string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	// Remove volume name from the queue and set
	c.failedQueue.Forget(volumeName)
	delete(c.failedSet, volumeName)
}

// setPVTagHash stores the hash in the PV annotations.
func setPVTagHash(pv *v1.PersistentVolume, hash string) *v1.PersistentVolume {

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

// parseAccessPointID checks if an Access Point ID is present in the input string.
// It returns the Access Point ID if present, or an empty string if not.
func parseAccessPointID(input string) string {
	// Split the input string by "::" delimiter
	parts := strings.Split(input, "::")

	// Check if there's an Access Point ID after "::"
	if len(parts) == 2 && strings.HasPrefix(parts[1], "fsap-") {
		return parts[1]
	}
	// Return an empty string if no Access Point ID is found
	return ""
}
