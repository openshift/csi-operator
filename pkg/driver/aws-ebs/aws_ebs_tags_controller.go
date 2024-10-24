package aws_ebs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	configv1 "github.com/openshift/api/config/v1"
	operatorapi "github.com/openshift/api/operator/v1"
	"github.com/openshift/csi-operator/pkg/clients"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
)

const (
	awsSecretNamespace     = "openshift-cluster-csi-drivers"
	awsSecretName          = "ebs-cloud-credentials"
	infrastructureResource = "cluster"
	driverName             = "ebs.csi.aws.com"
	tagHashAnnotationKey   = "ebs.csi.aws.com/volume-tags-hash"
	batchSize              = 100
)

type EBSVolumeTagsController struct {
	name          string
	commonClient  *clients.Clients
	eventRecorder events.Recorder
	failedQueue   workqueue.RateLimitingInterface
}

func NewEBSVolumeTagsController(
	ctx context.Context,
	name string,
	commonClient *clients.Clients,
	eventRecorder events.Recorder) factory.Controller {

	c := &EBSVolumeTagsController{
		name:          name,
		commonClient:  commonClient,
		eventRecorder: eventRecorder,
		failedQueue:   workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter()),
	}

	go c.startFailedQueueWorker(ctx)

	return factory.New().WithSync(
		c.Sync,
	).ResyncEvery(
		20*time.Minute,
	).WithInformers(
		c.commonClient.ConfigInformers.Config().V1().Infrastructures().Informer(),
	).ToController(
		"EBSVolumeTagsController",
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
	var infraRegion = ""
	if infra.Status.PlatformStatus != nil && infra.Status.PlatformStatus.AWS != nil {
		infraRegion = infra.Status.PlatformStatus.AWS.Region
	}
	ec2Client, err := c.getEC2Client(ctx, infraRegion)
	if err != nil {
		return err
	}
	err = c.processInfrastructure(infra, ec2Client)
	if err != nil {
		return err
	}

	return nil
}

// startFailedQueueWorker runs a worker that processes failed batches independently
func (c *EBSVolumeTagsController) startFailedQueueWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			klog.Infof("Context canceled, stopping failed queue worker for ebs Volume Tags")
			return // Stop the goroutine when the context is canceled
		default:
			// Get the next failed batch from the queue
			item, quit := c.failedQueue.Get()
			if quit {
				klog.Infof("Failed queue worker is shutting down")
				return
			}

			func(obj interface{}) {
				defer c.failedQueue.Done(obj)

				batch, ok := obj.(string)
				if !ok {
					klog.Errorf("Invalid batch type in failed queue, skipping")
					c.failedQueue.Forget(obj) // Remove invalid object from the queue
					return
				}

				klog.Infof("Retrying failed batch: %v", batch)

				// Get the infrastructure and EC2 client
				infra, err := c.getInfrastructure()
				if err != nil {
					klog.Errorf("Failed to get infrastructure for retry: %v", err)
					c.failedQueue.AddRateLimited(batch) // Retry the failed batch again with backoff
					return
				}
				var infraRegion = ""
				if infra.Status.PlatformStatus != nil && infra.Status.PlatformStatus.AWS != nil {
					infraRegion = infra.Status.PlatformStatus.AWS.Region
				}
				ec2Client, err := c.getEC2Client(context.TODO(), infraRegion)
				if err != nil {
					klog.Errorf("Failed to get EC2 client for retry: %v", err)
					c.failedQueue.AddRateLimited(batch) // Retry the failed batch again with backoff
					return
				}

				// Retry updating the tags for the failed batch
				if infra.Status.PlatformStatus != nil && infra.Status.PlatformStatus.AWS != nil {
					err = c.updateEBSTags(strings.Split(batch, ","), ec2Client, infra.Status.PlatformStatus.AWS.ResourceTags)
					if err != nil {
						errorMessage := fmt.Sprintf("Error creating tags for volume %s: %v", batch, err)
						c.handleFailure(strings.Split(batch, ","), errorMessage)
					} else {
						c.handleSuccess(strings.Split(batch, ","))
						c.failedQueue.Forget(batch)
					}
				}
			}(item)
		}
	}
}

// getEC2Client retrieves AWS credentials from the secret and creates an AWS EC2 client using session.Options
func (c *EBSVolumeTagsController) getEC2Client(ctx context.Context, awsRegion string) (*ec2.EC2, error) {
	secret, err := c.commonClient.KubeClient.CoreV1().Secrets(awsSecretNamespace).Get(ctx, awsSecretName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("error retrieving AWS credentials secret: %v", err)
	}

	// Check for aws_access_key_id and aws_secret_access_key fields
	awsAccessKeyID, accessKeyFound := secret.Data["aws_access_key_id"]
	awsSecretAccessKey, secretKeyFound := secret.Data["aws_secret_access_key"]

	if accessKeyFound && secretKeyFound {
		return createEC2ClientWithStaticKeys(awsRegion, string(awsAccessKeyID), string(awsSecretAccessKey))
	}

	// Otherwise, check for credentials field and create session using that
	credentialsData, credentialsFound := secret.Data["credentials"]
	if credentialsFound {
		tempFile, err := writeCredentialsToTempFile(credentialsData)
		if err != nil {
			return nil, fmt.Errorf("error writing credentials to temporary file: %v", err)
		}

		return createEC2ClientWithCredentialsFile(awsRegion, tempFile)
	}

	return nil, fmt.Errorf("no valid AWS credentials found in secret")
}

// createEC2ClientWithStaticKeys creates an EC2 client using static credentials (access key and secret key)
func createEC2ClientWithStaticKeys(awsRegion, awsAccessKeyID, awsSecretAccessKey string) (*ec2.EC2, error) {
	awsSession, err := session.NewSession(&aws.Config{
		Region:      aws.String(awsRegion),
		Credentials: credentials.NewStaticCredentials(awsAccessKeyID, awsSecretAccessKey, ""),
	})
	if err != nil {
		return nil, fmt.Errorf("error creating AWS session with static credentials: %v", err)
	}
	return ec2.New(awsSession), nil
}

// createEC2ClientWithCredentialsFile creates an EC2 client using a temporary credentials file
func createEC2ClientWithCredentialsFile(awsRegion, credentialsFilename string) (*ec2.EC2, error) {
	klog.Infof("Creating AWS session using credentials file: %s", credentialsFilename)

	defer func() {
		err := os.Remove(credentialsFilename)
		if err != nil {
			klog.Warningf("Failed to remove temporary credentials file: %v", err)
		} else {
			klog.Infof("Temporary credentials file %s removed successfully.", credentialsFilename)
		}
	}()

	awsOptions := session.Options{
		Config: aws.Config{
			Region: aws.String(awsRegion),
		},
		SharedConfigState: session.SharedConfigEnable,
		SharedConfigFiles: []string{credentialsFilename},
	}

	awsSession, err := session.NewSessionWithOptions(awsOptions)
	if err != nil {
		return nil, fmt.Errorf("error creating AWS session using credentials file: %v", err)
	}

	return ec2.New(awsSession), nil
}

// writeCredentialsToTempFile writes credentials data to a temporary file and returns the filename
func writeCredentialsToTempFile(data []byte) (string, error) {
	f, err := os.CreateTemp("", "aws-shared-credentials")
	if err != nil {
		return "", fmt.Errorf("failed to create file for shared credentials: %v", err)
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		defer os.Remove(f.Name())
		return "", fmt.Errorf("failed to write credentials to %s: %v", f.Name(), err)
	}
	return f.Name(), nil
}

// getAWSRegionFromInfrastructure retrieves the AWS region from the Infrastructure resource in OpenShift
func (c *EBSVolumeTagsController) getInfrastructure() (*configv1.Infrastructure, error) {
	infra, err := c.commonClient.ConfigClientSet.ConfigV1().Infrastructures().Get(context.TODO(),
		infrastructureResource, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve Infrastructure resource: %v", err)
	}

	return infra, nil
}

// processInfrastructure processes the Infrastructure resource and updates EBS tags
func (c *EBSVolumeTagsController) processInfrastructure(infra *configv1.Infrastructure, ec2Client *ec2.EC2) error {
	if infra.Status.PlatformStatus != nil && infra.Status.PlatformStatus.AWS != nil &&
		infra.Status.PlatformStatus.AWS.ResourceTags != nil {
		awsInfra := infra.Status.PlatformStatus.AWS
		err := c.fetchPVsAndUpdateTags(awsInfra.ResourceTags, ec2Client)
		if err != nil {
			klog.Errorf("Error processing PVs for infrastructure update: %v", err)
			return err
		}
	}
	return nil
}

// fetchPVsAndUpdateTags retrieves all PVs and updates the AWS EBS tags in batches of 100
func (c *EBSVolumeTagsController) fetchPVsAndUpdateTags(resourceTags []configv1.AWSResourceTag, ec2Client *ec2.EC2) error {
	pvs, err := c.commonClient.KubeClient.CoreV1().PersistentVolumes().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("error fetching PVs: %v", err)
	}

	// Compute the hash for the new set of tags
	newTagsHash := computeTagsHash(resourceTags)

	// Collect the volume IDs and map them to the corresponding PV objects
	var volumeIDs []string
	pvMap := make(map[string]*v1.PersistentVolume)

	for _, pv := range pvs.Items {
		if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == driverName {
			volumeID := pv.Spec.CSI.VolumeHandle
			existingHash := getPVTagHash(&pv)

			if existingHash == "" || existingHash != newTagsHash {
				volumeIDs = append(volumeIDs, volumeID)
				pvMap[volumeID] = &pv
			}
		}
	}

	// If there are no volumes to update, return early
	if len(volumeIDs) == 0 {
		klog.Infof("No volume tags to update as hashes are unchanged")
		return nil
	}

	// Process the volume IDs in batches of 100 (as before)
	for i := 0; i < len(volumeIDs); i += batchSize {
		end := i + batchSize
		if end > len(volumeIDs) {
			end = len(volumeIDs)
		}
		batch := volumeIDs[i:end]

		// Update tags on AWS EBS volumes
		err = c.updateEBSTags(batch, ec2Client, resourceTags)
		if err != nil {
			errorMessage := fmt.Sprintf("Error creating tags for volume %s: %v", volumeIDs, err)
			c.handleFailure(volumeIDs, errorMessage)
			continue
		}

		// Update PV annotations after successfully updating the tags in AWS
		for _, volumeID := range batch {
			pv := pvMap[volumeID]

			// Set the new tag hash annotation in the PV object
			setPVTagHash(pv, newTagsHash)

			// Update the PV with the new annotations
			_, err = c.commonClient.KubeClient.CoreV1().PersistentVolumes().Update(context.TODO(), pv, metav1.UpdateOptions{})
			if err != nil {
				klog.Errorf("Error updating PV annotations for volume %s: %v", volumeID, err)
				continue
			}
			klog.Infof("Successfully updated PV annotations for volume %s", volumeID)
		}

		c.handleSuccess(batch)
	}

	return nil
}

// updateEBSTags updates the tags of an AWS EBS volume
func (c *EBSVolumeTagsController) updateEBSTags(volumeIDs []string, ec2Client *ec2.EC2, resourceTags []configv1.AWSResourceTag) error {

	// Merge the existing tags with new resource tags
	mergedTags := newAndUpdatedTags(resourceTags)

	klog.Infof("Updating EBS tags for volume IDs %s with tags: %v", volumeIDs, mergedTags)

	// Create or update the tags
	_, err := ec2Client.CreateTags(&ec2.CreateTagsInput{
		Resources: volumesIDsToResourceIDs(volumeIDs),
		Tags:      mergedTags,
	})

	if err != nil {
		return fmt.Errorf("error creating tags for volume %s: %v", volumeIDs, err)
	}

	klog.Infof("Successfully updated tags for volumes %s", volumeIDs)
	return nil
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

func volumesIDsToResourceIDs(volumeIDs []string) []*string {
	var resourceIDs []*string
	for _, volumeID := range volumeIDs {
		resourceIDs = append(resourceIDs, aws.String(volumeID))
	}
	return resourceIDs
}

func (c *EBSVolumeTagsController) setCSIDriverDegradedCondition(degraded bool, reason, message string) error {
	return c.retryOperatorStatusUpdate(func(status *operatorapi.OperatorStatus) error {
		conditionStatus := operatorapi.ConditionFalse
		if degraded {
			conditionStatus = operatorapi.ConditionTrue
		}

		v1helpers.SetOperatorCondition(&status.Conditions, operatorapi.OperatorCondition{
			Type:    operatorapi.OperatorStatusTypeDegraded,
			Status:  conditionStatus,
			Reason:  reason,
			Message: message,
		})

		return nil
	})
}

func (c *EBSVolumeTagsController) handleSuccess(batch []string) {
	klog.Infof("Successfully processed tags for batch: %v", batch)

	err := c.setCSIDriverDegradedCondition(false, "EBSVolumeTagsControllerDegraded", "Volumes Tagging operation succeeded")
	if err != nil {
		klog.Errorf("Error setting Degraded condition to False: %v", err)
	}
}

func (c *EBSVolumeTagsController) handleFailure(batch []string, errorMessage string) {
	klog.Errorf("Error in batch %v: %v", batch, errorMessage)

	// Emit a warning event for the failure
	c.eventRecorder.Warning("EBSVolumeTagsUpdateFailed", fmt.Sprintf("Failed to update tags for batch %v: %v", batch, errorMessage))

	err := c.setCSIDriverDegradedCondition(true, "EBSVolumeTagsControllerDegraded", errorMessage)
	if err != nil {
		klog.Errorf("Error setting Degraded condition to True: %v", err)
	}
	c.failedQueue.AddRateLimited(sliceToString(batch))
}

// retryOperatorStatusUpdate retries the status update for the ClusterCSIDriver in case of conflicts.
func (c *EBSVolumeTagsController) retryOperatorStatusUpdate(updateFunc func(status *operatorapi.OperatorStatus) error) error {
	return wait.ExponentialBackoff(wait.Backoff{
		Duration: 100 * time.Millisecond, // Initial retry delay
		Factor:   2.0,                    // Exponential factor
		Jitter:   0.1,                    // Jitter to randomize delays slightly
		Steps:    5,                      // Number of retries before giving up
	}, func() (bool, error) {
		_, status, resourceVersion, err := c.commonClient.OperatorClient.GetOperatorState()
		if err != nil {
			klog.Errorf("Failed to get operator state: %v", err)
			return false, err
		}

		err = updateFunc(status)
		if err != nil {
			if apierrors.IsConflict(err) {
				klog.Warningf("Conflict while updating ClusterCSIDriver, retrying: %v", err)
				return false, nil
			}
			return false, err
		}

		_, err = c.commonClient.OperatorClient.UpdateOperatorStatus(context.TODO(), resourceVersion, status)
		if err != nil {
			if apierrors.IsConflict(err) {
				klog.Warningf("Conflict while updating status, retrying: %v", err)
				return false, nil
			}
			return false, err
		}

		return true, nil
	})
}

// Convert the slice of volume IDs to a single string
func sliceToString(slice []string) string {
	return strings.Join(slice, ",")
}

// setPVTagHash stores the hash in the PV annotations.
func setPVTagHash(pv *v1.PersistentVolume, hash string) {
	// Ensure the PV has an annotations map
	if pv.Annotations == nil {
		pv.Annotations = make(map[string]string)
	}

	// Set or update the tag hash annotation
	pv.Annotations[tagHashAnnotationKey] = hash
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
