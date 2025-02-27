package aws_efs

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"crypto/sha256"
	"encoding/hex"
	"gopkg.in/ini.v1"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/credentials/stscreds"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/efs"
	"github.com/aws/aws-sdk-go/service/sts"

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
	name          string
	commonClient  *clients.Clients
	eventRecorder events.Recorder
	queue         workqueue.TypedRateLimitingInterface[string]
	queueSet      map[string]struct{} // A set to track added volume names
	mutex         sync.Mutex
}

func NewEFSAccessPointTagsController(
	name string,
	commonClient *clients.Clients,
	eventRecorder events.Recorder) factory.Controller {

	c := &EFSAccessPointTagsController{
		name:          name,
		commonClient:  commonClient,
		eventRecorder: eventRecorder,
		queue:         workqueue.NewTypedRateLimitingQueue[string](workqueue.NewTypedItemExponentialFailureRateLimiter[string](10*time.Second, 100*time.Hour)),
		queueSet:      make(map[string]struct{}),
		mutex:         sync.Mutex{},
	}
	return factory.New().WithSync(
		c.Sync,
	).WithInformers(
		c.commonClient.ConfigInformers.Config().V1().Infrastructures().Informer(),
	).ResyncEvery(
		defaultReSyncPeriod,
	).WithPostStartHooks(
		c.startTagsQueueWorker,
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
	if infra == nil || infra.Status.PlatformStatus == nil || infra.Status.PlatformStatus.AWS == nil || !isHypershiftCluster(infra) {
		return nil
	}
	err = c.processInfrastructure(ctx, infra)
	if err != nil {
		return err
	}

	return nil
}

// isHypershiftCluster validates whether the cluster is a HyperShift cluster based on the label.
func isHypershiftCluster(infra *configv1.Infrastructure) bool {
	if infra.Labels != nil {
		if value, exists := infra.Labels["hypershift.openshift.io/managed"]; exists && value == "true" {
			return true
		}
	}
	return false
}

// getEFSClient retrieves AWS credentials from the secret and creates an AWS EFS client.
func (c *EFSAccessPointTagsController) getEFSClient(awsRegion string) (*efs.EFS, error) {
	secret, err := c.getEFSCloudCredSecret()
	if err != nil {
		klog.Errorf("error getting secret: %v", err)
		return nil, fmt.Errorf("error retrieving AWS credentials secret: %v", err)
	}

	credentialsData, credentialsFound := secret.Data["credentials"]
	if credentialsFound {
		sess, err := c.createClientWithCredentials(credentialsData, awsRegion)
		if err != nil {
			klog.Errorf("error creating session: %v", err)
			return nil, fmt.Errorf("error creating session: %v", err)
		}
		return sess, nil
	}
	return nil, fmt.Errorf("no valid AWS credentials found in secret")
}

// createClientWithCredentials creates the client using the credential.
func (c *EFSAccessPointTagsController) createClientWithCredentials(credentialsData []byte, awsRegion string) (*efs.EFS, error) {
	// Load INI file from credentialsData
	cfg, err := ini.Load(credentialsData)
	if err != nil {
		klog.Errorf("Error parsing INI credentials: %v", err)
		return nil, fmt.Errorf("error parsing credentials data: %v", err)
	}

	section := cfg.Section("default")
	roleARN := section.Key("role_arn").String()
	tokenFile := os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE")

	// Validate required fields
	if roleARN == "" || tokenFile == "" {
		return nil, fmt.Errorf("missing required AWS credentials: role_arn or web_identity_token_file is empty")
	}

	sess, err := session.NewSession(&aws.Config{
		Region: aws.String(awsRegion),
	})
	if err != nil {
		klog.Errorf("Error creating base AWS session: %v", err)
		return nil, fmt.Errorf("error creating AWS session: %v", err)
	}

	provider := stscreds.NewWebIdentityRoleProviderWithOptions(
		sts.New(sess),
		roleARN,
		"aws-ebs-csi-driver-operator", // Role session name
		stscreds.FetchTokenPath(tokenFile),
	)

	// Create new session with WebIdentity credentials
	sess, err = session.NewSession(&aws.Config{
		Region:      aws.String(awsRegion),
		Credentials: credentials.NewCredentials(provider),
	})
	if err != nil {
		klog.Errorf("Error creating AWS session with Web Identity: %v", err)
		return nil, fmt.Errorf("error creating AWS session with Web Identity: %v", err)
	}

	return efs.New(sess), nil
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

// getEFSCloudCredSecret returns the aws secret stored in awsEFSSecretName secret.
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
		err := c.fetchAndPushPvsToQueue(ctx, infra)
		if err != nil {
			klog.Errorf("Error processing PVs for infrastructure update: %v", err)
			return err
		}
	}
	return nil
}

// fetchAndPushPvsToQueue lists the PVs, filter the updatable PVs and push them to the queue to get the tags updated via worker.
func (c *EFSAccessPointTagsController) fetchAndPushPvsToQueue(ctx context.Context, infra *configv1.Infrastructure) error {
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
	// add the volumes to the queue for the queue worker.
	for _, volume := range pvsToBeUpdated {
		c.addToQueue(volume.Name)
	}
	return nil
}

// updateEFSAccessPointTags updates the tags of an AWS EFS Access Points
func (c *EFSAccessPointTagsController) updateEFSAccessPointTags(ctx context.Context, pv *v1.PersistentVolume, efsClient *efs.EFS, resourceTags []configv1.AWSResourceTag) error {
	tags := newAndUpdatedTags(resourceTags)

	// Create or update the tags
	_, err := efsClient.TagResource(&efs.TagResourceInput{
		ResourceId: aws.String(parseAccessPointID(pv.Spec.CSI.VolumeHandle)),
		Tags:       tags,
	})
	if err != nil {
		klog.Errorf("Error updating tags for PV %s: %v", pv.Spec.CSI.VolumeHandle, err)
		return err
	}
	return nil
}

// updateVolume updates the volume using kube client.
func (c *EFSAccessPointTagsController) updateVolume(ctx context.Context, pv *v1.PersistentVolume) error {
	_, err := c.commonClient.KubeClient.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{})
	if err != nil {
		klog.Errorf("Error updating PV %s: %v", pv.Name, err)
		return err
	}
	return nil
}

// listPersistentVolumes lists the pvs from the cache
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

// filterUpdatableVolumes filters the updatable volumes from all the volumes listed.
func (c *EFSAccessPointTagsController) filterUpdatableVolumes(volumes []*v1.PersistentVolume, newTagsHash string) []*v1.PersistentVolume {
	var pvsToBeUpdated = make([]*v1.PersistentVolume, 0)
	for _, volume := range volumes {
		if volume.Spec.CSI != nil && volume.Spec.CSI.Driver == efsDriverName &&
			parseAccessPointID(volume.Spec.CSI.VolumeHandle) != "" && !c.isVolumeInQueue(volume.Name) {
			existingHash := getPVTagHash(volume)
			if existingHash == "" || existingHash != newTagsHash {
				pvsToBeUpdated = append(pvsToBeUpdated, volume)
			}
		}
	}
	return pvsToBeUpdated
}

// isVolumeInQueue checks if a volume name is currently in the queue
func (c *EFSAccessPointTagsController) isVolumeInQueue(volumeName string) bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	// Check if the volume name is in the set
	_, exists := c.queueSet[volumeName]
	return exists
}

// addToQueue adds a volume name to the queue and tracks it in the set
func (c *EFSAccessPointTagsController) addToQueue(volumeName string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	// Add volume name to the queue and set
	c.queue.AddRateLimited(volumeName)
	c.queueSet[volumeName] = struct{}{}
}

// removeFromQueue removes a volume name from the queue and the set
func (c *EFSAccessPointTagsController) removeFromQueue(volumeName string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	// Remove volume name from the queue and set
	c.queue.Forget(volumeName)
	delete(c.queueSet, volumeName)
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
