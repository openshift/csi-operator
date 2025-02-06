package aws_ebs

import (
	"context"
	"errors"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"

	"github.com/aws/aws-sdk-go/aws/awserr"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
)

// startTagsUpdateQueueWorker runs a worker that will update the volumes tags independently.
func (c *EBSVolumeTagsController) startTagsUpdateQueueWorker(ctx context.Context, syncContext factory.SyncContext) error {
	for {
		select {
		case <-ctx.Done():
			klog.Infof("context canceled, stopping tags queue worker for EBS Volume Tags")
			return errors.New("context canceled, stopping tags queue worker for EBS Volume Tags")
		default:
			item, quit := c.queue.Get()
			if quit {
				klog.Infof("tags queue worker is shutting down")
				return errors.New("tags queue worker is shutting down")
			}
			c.processVolumes(ctx, item)
		}
	}
}

// processFailedVolume processes a single failed volume from the queue
func (c *EBSVolumeTagsController) processVolumes(ctx context.Context, item *pvUpdateQueueItem) {
	defer c.queue.Done(item)

	if item == nil {
		c.queue.Forget(item)
		return
	}

	infra, err := c.getInfrastructure()
	if err != nil {
		klog.Errorf("failed to get infrastructure object: %v", err)
		c.queue.AddRateLimited(item)
		return
	}

	if infra.Status.PlatformStatus == nil || infra.Status.PlatformStatus.AWS == nil || len(infra.Status.PlatformStatus.AWS.Region) == 0 {
		klog.Infof("skipping volume tags update because AWS region or tags are not defined")
		c.removeVolumesFromQueueSet(item.pvNames...)
		return
	}

	ec2Client, err := c.getEC2Client(ctx, infra.Status.PlatformStatus.AWS.Region)
	if err != nil {
		klog.Errorf("failed to get EC2 client: %v", err)
		c.queue.AddRateLimited(item)
		return
	}
	if item.updateType == updateTypeBatch {
		pvList := make([]*v1.PersistentVolume, 0)
		for _, pvName := range item.pvNames {
			pv, err := c.getPersistentVolumeByName(pvName)
			if err != nil {
				if apierrors.IsNotFound(err) {
					c.removeVolumesFromQueueSet(pvName)
					continue
				}
				// requeue the volume individually, will retry with backoff time.
				c.queue.AddRateLimited(&pvUpdateQueueItem{
					updateType: updateTypeIndividual,
					pvNames:    []string{pvName},
				})
				klog.Errorf("Failed to retrieve PersistentVolume %s: %v", pvName, err)
				continue
			}
			pvList = append(pvList, pv)
		}
		if len(pvList) == 0 {
			c.queue.Forget(item)
			return
		}
		// update the tags for the volume list.
		err = c.updateEBSTags(ec2Client, infra.Status.PlatformStatus.AWS.ResourceTags, pvList...)
		if err != nil {
			klog.Errorf("failed to update EBS tags: %v", err)
			c.handleBatchTagUpdateFailure(pvList, err)
			c.queue.Forget(item)
			return
		}
		newTagsHash := computeTagsHash(infra.Status.PlatformStatus.AWS.ResourceTags)
		// Update PV annotations after successfully updating the tags in AWS
		for _, volume := range pvList {
			// Set the new tag hash annotation in the PV object
			updatedVolume := setPVTagHash(volume, newTagsHash)
			// Update the PV with the new annotations
			err = c.updateVolume(ctx, updatedVolume)
			if err != nil {
				klog.Errorf("Error updating PV annotations for volume %s: %v", volume.Name, err)
				// requeue the volume individually, will retry with backoff time.
				c.queue.AddRateLimited(&pvUpdateQueueItem{
					updateType: updateTypeIndividual,
					pvNames:    convertPVsListToStringArray(volume),
				})
				continue
			}
			c.removeVolumesFromQueueSet(volume.Name)
			klog.Infof("Successfully updated PV annotations and tags for volume %s", volume.Name)
		}
	} else {
		if len(item.pvNames) == 0 {
			c.queue.Forget(item)
			return
		}
		pv, err := c.getPersistentVolumeByName(item.pvNames[0])
		if err != nil {
			if apierrors.IsNotFound(err) {
				klog.Infof("skipping volume tags update because PV %v does not exist", item.pvNames[0])
				c.removeVolumesFromQueueSet(pv.Name)
				c.queue.Forget(item)
				return
			}
			klog.Errorf("Error retrieving PV: %v", err)
			c.queue.AddRateLimited(item)
			return
		}
		// check if volume still need to update the tags.
		if !c.needsTagUpdate(infra, pv) {
			c.queue.Forget(item)
			return
		}
		err = c.updateEBSTags(ec2Client, infra.Status.PlatformStatus.AWS.ResourceTags, pv)
		if err != nil {
			if awsErr, ok := err.(awserr.Error); ok {
				switch awsErr.Code() {
				case awsErrorVolumeNotFound:
					klog.Errorf("Volume %s not found: %v , Removing the volume from the queue", pv.Spec.CSI.VolumeHandle, awsErr.Message())
					c.queue.Forget(item)
					c.removeVolumesFromQueueSet(pv.Name)
					return
				}
			}
			c.handleIndividualTagUpdateFailure(pv, err)
			c.queue.AddRateLimited(item)
			return
		}
		newTagsHash := computeTagsHash(infra.Status.PlatformStatus.AWS.ResourceTags)
		updatedVolume := setPVTagHash(pv, newTagsHash)
		// Update the PV with the new annotations
		err = c.updateVolume(ctx, updatedVolume)
		if err != nil {
			klog.Errorf("Error updating PV annotations for volume %s: %v", pv.Name, err)
			c.queue.AddRateLimited(item)
			return
		}
		c.removeVolumesFromQueueSet(pv.Name)
		klog.Infof("Successfully updated PV annotations and tags for volume %s", pv.Name)
	}
	c.queue.Forget(item)
}

// needsTagUpdate checks if the PersistentVolume tags need to be updated
func (c *EBSVolumeTagsController) needsTagUpdate(infra *configv1.Infrastructure, pv *v1.PersistentVolume) bool {
	existingHash := getPVTagHash(pv)
	newTagsHash := computeTagsHash(infra.Status.PlatformStatus.AWS.ResourceTags)
	return existingHash == "" || existingHash != newTagsHash
}
