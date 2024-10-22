package aws_ebs

import (
	"context"
	"fmt"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	configv1 "github.com/openshift/api/config/v1"
	operatorapi "github.com/openshift/api/operator/v1"
	"github.com/openshift/csi-operator/pkg/clients"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
)

const (
	awsSecretNamespace     = "openshift-cluster-csi-drivers"
	awsSecretName          = "ebs-cloud-credentials"
	infrastructureResource = "cluster"
	driverName             = "ebs.csi.aws.com"
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
	// Define the informer for watching Infrastructure resource changes
	infraInformer := c.commonClient.ConfigInformers.Config().V1().Infrastructures().Informer()

	// Add event handler to process updates only when ResourceTags change
	infraInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldInfra := oldObj.(*configv1.Infrastructure)
			newInfra := newObj.(*configv1.Infrastructure)

			// Compare old and new AWS ResourceTags
			if !reflect.DeepEqual(oldInfra.Status.PlatformStatus.AWS.ResourceTags, newInfra.Status.PlatformStatus.AWS.ResourceTags) {
				klog.Infof("AWS ResourceTags changed: triggering reconciliation")
				syncCtx := factory.NewSyncContext(c.name, eventRecorder)
				err := c.Sync(context.TODO(), syncCtx)
				if err != nil {
					klog.Errorf("Error syncing AWS Infrastructure: %v", err)
					return
				}
			}
		},
	})

	go c.startFailedQueueWorker(ctx)

	return factory.New().WithSync(
		c.Sync,
	).ResyncEvery(
		20*time.Minute,
	).WithInformers(
		infraInformer,
	).ToController(
		"EBSVolumeTagsController",
		eventRecorder,
	)
}

func (c *EBSVolumeTagsController) Sync(ctx context.Context, syncCtx factory.SyncContext) error {
	klog.V(4).Infof("EBSVolumeTagsController sync started")
	defer klog.V(4).Infof("EBSVolumeTagsController sync finished")

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

	// Collect the volume IDs
	var volumeIDs []string
	for _, pv := range pvs.Items {
		if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == driverName {
			volumeID := pv.Spec.CSI.VolumeHandle
			volumeIDs = append(volumeIDs, volumeID)
		}
	}

	// Process the volume IDs in batches of 100
	const batchSize = 100
	for i := 0; i < len(volumeIDs); i += batchSize {
		end := i + batchSize
		if end > len(volumeIDs) {
			end = len(volumeIDs)
		}
		batch := volumeIDs[i:end]

		err = c.updateEBSTags(batch, ec2Client, resourceTags)
		if err != nil {
			errorMessage := fmt.Sprintf("Error creating tags for volume %s: %v", volumeIDs, err)
			c.handleFailure(volumeIDs, errorMessage)
		}
		c.handleSuccess(volumeIDs)
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

func (c *EBSVolumeTagsController) setCSIDriverAvailableCondition(available bool, reason, message string) error {
	return c.retryOperatorStatusUpdate(func(status *operatorapi.OperatorStatus) error {
		conditionStatus := operatorapi.ConditionTrue
		if !available {
			conditionStatus = operatorapi.ConditionFalse
		}

		v1helpers.SetOperatorCondition(&status.Conditions, operatorapi.OperatorCondition{
			Type:    operatorapi.OperatorStatusTypeAvailable,
			Status:  conditionStatus,
			Reason:  reason,
			Message: message,
		})

		return nil
	})
}

func (c *EBSVolumeTagsController) handleSuccess(batch []string) {
	klog.Infof("Successfully processed tags for batch: %v", batch)

	err := c.setCSIDriverDegradedCondition(false, "EBSVolumeTagsUpdateSucceeded", "Tagging operation succeeded")
	if err != nil {
		klog.Errorf("Error setting Degraded condition to False: %v", err)
	}

	err = c.setCSIDriverAvailableCondition(true, "EBSVolumeTagsUpdateSucceeded", "Driver available and healthy")
	if err != nil {
		klog.Errorf("Error setting Available condition to True: %v", err)
	}
}

func (c *EBSVolumeTagsController) handleFailure(batch []string, errorMessage string) {
	klog.Errorf("Error in batch %v: %v", batch, errorMessage)

	// Emit a warning event for the failure
	c.eventRecorder.Warning("EBSVolumeTagsUpdateFailed", fmt.Sprintf("Failed to update tags for batch %v: %v", batch, errorMessage))

	err := c.setCSIDriverDegradedCondition(true, "EBSVolumeTagsUpdateFailed", errorMessage)
	if err != nil {
		klog.Errorf("Error setting Degraded condition to True: %v", err)
	}

	err = c.setCSIDriverAvailableCondition(false, "EBSVolumeTagsUpdateFailed", "Driver unavailable due to errors")
	if err != nil {
		klog.Errorf("Error setting Available condition to False: %v", err)
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
