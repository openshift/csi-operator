package aws_efs

import (
	"context"
	"errors"
	"fmt"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
)

// startTagsQueueWorker runs a worker that processes volumes independently
func (c *EFSAccessPointTagsController) startTagsQueueWorker(ctx context.Context, syncContext factory.SyncContext) error {
	for {
		select {
		case <-ctx.Done():
			klog.Infof("Context canceled, stopping queue worker for EFS Volume Access Points Tags")
			return errors.New("context canceled, stopping queue worker for EFS Volume Access Points Tags")
		default:
			item, quit := c.queue.Get()
			if quit {
				klog.Infof("queue worker is shutting down")
				return errors.New("queue worker is shutting down")
			}
			c.processQueueVolume(ctx, item)
		}
	}
}

// processQueueVolume processes a single volume from the queue
func (c *EFSAccessPointTagsController) processQueueVolume(ctx context.Context, pvName string) {
	defer c.queue.Done(pvName)
	klog.Infof("Attempting to update tags for volume: %v", pvName)

	infra, err := c.getInfrastructure()
	if err != nil {
		klog.Errorf("Failed to get infrastructure object: %v", err)
		c.queue.AddRateLimited(pvName)
		return
	}
	if infra.Status.PlatformStatus == nil || infra.Status.PlatformStatus.AWS == nil || len(infra.Status.PlatformStatus.AWS.Region) == 0 {
		klog.Infof("Skipping failed volume %v because no AWS region defined", pvName)
		c.queue.AddRateLimited(pvName)
		return
	}

	pv, err := c.getPersistentVolume(pvName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.Infof("Skipping failed volume %v because it does not exist", pvName)
			c.removeFromQueue(pvName)
			return
		}
		klog.Errorf("Failed to get persistent volume %v: %v", pvName, err)
		c.queue.AddRateLimited(pvName)
		return
	}

	if c.needsTagUpdate(infra, pv) {
		c.updateTags(ctx, pv, infra.Status.PlatformStatus.AWS.Region, infra.Status.PlatformStatus.AWS.ResourceTags)
	} else {
		klog.Infof("No update needed for volume %s as hashes match", pvName)
		c.removeFromQueue(pvName)
	}
}

// retrievePersistentVolume retrieves the PersistentVolume object by its name
func (c *EFSAccessPointTagsController) getPersistentVolume(pvName string) (*v1.PersistentVolume, error) {
	pv, err := c.commonClient.KubeInformers.InformersFor("").Core().V1().PersistentVolumes().Lister().Get(pvName)
	if err != nil {
		klog.Errorf("Failed to retrieve PV for volume %s: %v", pvName, err)
		return nil, err
	}
	return pv, nil
}

// needsTagUpdate checks if the PersistentVolume tags need to be updated
func (c *EFSAccessPointTagsController) needsTagUpdate(infra *configv1.Infrastructure, pv *v1.PersistentVolume) bool {
	existingHash := getPVTagHash(pv)
	newTagsHash := computeTagsHash(infra.Status.PlatformStatus.AWS.ResourceTags)
	return existingHash == "" || existingHash != newTagsHash
}

// updateTags updates the EFS tags on AWS and the PersistentVolume annotations
func (c *EFSAccessPointTagsController) updateTags(ctx context.Context, pv *v1.PersistentVolume, region string, resourceTags []configv1.AWSResourceTag) {
	efsClient, err := c.getEFSClient(region)
	if err != nil {
		klog.Errorf("Failed to get EFS client for retry: %v", err)
		c.queue.AddRateLimited(pv.Name)
		return
	}

	err = c.updateEFSAccessPointTags(ctx, pv, efsClient, resourceTags)
	if err != nil {
		klog.Errorf("Failed to update tags for volume %s: %v", pv.Name, err)
		c.eventRecorder.Warning("EFSAccessPointTagsUpdateFailed", fmt.Sprintf("Failed to update tags for volume %v: %v", pv.Name, err.Error()))
		c.queue.AddRateLimited(pv.Name)
		return
	}

	newTagsHash := computeTagsHash(resourceTags)
	updatedVolume := setPVTagHash(pv, newTagsHash)

	err = c.updateVolume(ctx, updatedVolume)
	if err != nil {
		klog.Errorf("Error updating PV annotations for volume %s: %v", pv.Name, err)
		c.queue.AddRateLimited(pv.Name)
		return
	}

	klog.Infof("Successfully updated PV annotations for volume %s", pv.Name)
	c.removeFromQueue(pv.Name)
}
