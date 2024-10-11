package aws_ebs

import (
	"context"
	"errors"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	"github.com/openshift/library-go/pkg/controller/factory"
)

// startFailedQueueWorker runs a worker that processes failed volumes independently
func (c *EBSVolumeTagsController) startFailedQueueWorker(ctx context.Context, syncContext factory.SyncContext) error {
	for {
		select {
		case <-ctx.Done():
			klog.Infof("Context canceled, stopping failed queue worker for EBS Volume Tags")
			return errors.New("context canceled, stopping failed queue worker for EBS Volume Tags") // Stop the goroutine when the context is canceled
		default:
			// Get the next failed volume from the queue
			item, quit := c.failedQueue.Get()
			if quit {
				klog.Infof("Failed queue worker is shutting down")
				return errors.New("failed queue worker is shutting down")
			}

			func(obj interface{}) {
				defer c.failedQueue.Done(obj)

				pvName, ok := obj.(string)
				if !ok {
					klog.Errorf("Invalid volume name type in failed queue, skipping")
					c.failedQueue.Forget(obj) // Remove invalid object from the queue
					return
				}

				klog.Infof("Retrying failed volume: %v", pvName)

				// Get the infrastructure and EC2 client
				infra, err := c.getInfrastructure(ctx)
				if err != nil {
					klog.Errorf("Failed to get infrastructure for retry: %v", err)
					c.failedQueue.AddRateLimited(pvName) // Retry the failed volume again with backoff
					return
				}
				var infraRegion = ""
				if infra.Status.PlatformStatus != nil && infra.Status.PlatformStatus.AWS != nil {
					infraRegion = infra.Status.PlatformStatus.AWS.Region
				}
				ec2Client, err := c.getEC2Client(ctx, infraRegion)
				if err != nil {
					klog.Errorf("Failed to get EC2 client for retry: %v", err)
					c.failedQueue.AddRateLimited(pvName) // Retry the failed volume again with backoff
					return
				}

				// Compute the new tag hash for comparison
				newTagsHash := computeTagsHash(infra.Status.PlatformStatus.AWS.ResourceTags)

				// Retrieve the PV associated with the volume name
				pv, err := c.commonClient.KubeInformers.InformersFor("").Core().V1().PersistentVolumes().Lister().Get(pvName)
				if err != nil {
					klog.Errorf("Failed to retrieve PV for volume %s: %v", pvName, err)
					c.failedQueue.AddRateLimited(pvName)
					return
				}

				// Get existing tag hash from PV annotations
				existingHash := getPVTagHash(pv)

				// Check if tags need to be updated by comparing hashes
				if existingHash == "" || existingHash != newTagsHash {
					// Update EBS tags on AWS for this volume
					err = c.updateEBSTags([]*v1.PersistentVolume{pv}, ec2Client, infra.Status.PlatformStatus.AWS.ResourceTags)
					if err != nil {
						c.handleTagUpdateFailure([]*v1.PersistentVolume{pv}, err)
						return
					}

					// After successful update, store the new hash in the PV annotations
					setPVTagHash(pv, newTagsHash)

					// Update the PV with the new annotation
					err = c.updateVolumeWithRetry(ctx, pv)
					if err != nil {
						klog.Errorf("Error updating PV annotations for volume %s: %v", pvName, err)
						c.failedQueue.AddRateLimited(pvName) // Retry updating annotation if update fails
						return
					}

					klog.Infof("Successfully updated PV annotations for volume %s", pvName)
				} else {
					klog.Infof("No update needed for volume %s as hashes match", pvName)
				}
				c.failedQueue.Forget(pvName)

			}(item)
		}
	}
}
