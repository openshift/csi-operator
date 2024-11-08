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

// startFailedQueueWorker runs a worker that processes failed volumes independently
func (c *EBSVolumeTagsController) startFailedQueueWorker(ctx context.Context, syncContext factory.SyncContext) error {
	for {
		select {
		case <-ctx.Done():
			klog.Infof("Context canceled, stopping failed queue worker for EBS Volume Tags")
			return errors.New("context canceled, stopping failed queue worker for EBS Volume Tags")
		default:
			item, quit := c.failedQueue.Get()
			if quit {
				klog.Infof("Failed queue worker is shutting down")
				return errors.New("failed queue worker is shutting down")
			}
			c.processFailedVolume(ctx, item)
		}
	}
}

// processFailedVolume processes a single failed volume from the queue
func (c *EBSVolumeTagsController) processFailedVolume(ctx context.Context, pvName string) {
	defer c.failedQueue.Done(pvName)

	klog.Infof("Retrying failed volume: %v", pvName)

	infra, err := c.getInfrastructure()
	if err != nil {
		klog.Errorf("Failed to get infrastructure object: %v", err)
		c.failedQueue.AddRateLimited(pvName)
		return
	}
	if infra.Status.PlatformStatus == nil || infra.Status.PlatformStatus.AWS == nil || len(infra.Status.PlatformStatus.AWS.Region) == 0 {
		klog.Infof("Skipping failed volume %v because no AWS region defined", pvName)
		c.failedQueue.AddRateLimited(pvName)
		return
	}

	pv, err := c.getPersistentVolume(pvName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.Infof("Skipping failed volume %v because it does not exist", pvName)
			c.failedQueue.Forget(pvName)
			return
		}
		klog.Errorf("Failed to get persistent volume %v: %v", pvName, err)
		c.failedQueue.AddRateLimited(pvName)
		return
	}

	if c.needsTagUpdate(infra, pv) {
		c.updateTags(ctx, pv, infra.Status.PlatformStatus.AWS.Region, infra.Status.PlatformStatus.AWS.ResourceTags)
	} else {
		klog.Infof("No update needed for volume %s as hashes match", pvName)
		c.failedQueue.Forget(pvName)
	}
}

// retrievePersistentVolume retrieves the PersistentVolume object by its name
func (c *EBSVolumeTagsController) getPersistentVolume(pvName string) (*v1.PersistentVolume, error) {
	pv, err := c.commonClient.KubeInformers.InformersFor("").Core().V1().PersistentVolumes().Lister().Get(pvName)
	if err != nil {
		klog.Errorf("Failed to retrieve PV for volume %s: %v", pvName, err)
		return nil, err
	}
	return pv, nil
}

// needsTagUpdate checks if the PersistentVolume tags need to be updated
func (c *EBSVolumeTagsController) needsTagUpdate(infra *configv1.Infrastructure, pv *v1.PersistentVolume) bool {
	existingHash := getPVTagHash(pv)
	newTagsHash := computeTagsHash(infra.Status.PlatformStatus.AWS.ResourceTags)
	return existingHash == "" || existingHash != newTagsHash
}

// updateTags updates the EBS tags on AWS and the PersistentVolume annotations
func (c *EBSVolumeTagsController) updateTags(ctx context.Context, pv *v1.PersistentVolume, region string, resourceTags []configv1.AWSResourceTag) {
	ec2Client, err := c.getEC2Client(ctx, region)
	if err != nil {
		klog.Errorf("Failed to get EC2 client for retry: %v", err)
		c.failedQueue.AddRateLimited(pv.Name)
		return
	}

	err = c.updateEBSTags(ctx, []*v1.PersistentVolume{pv}, ec2Client, resourceTags)
	if err != nil {
		if awsErr, ok := err.(awserr.Error); ok {
			switch awsErr.Code() {
			case awsErrorVolumeNotFound:
				klog.Errorf("Volume %s not found: %v , Removing the volume from the queue", pv.Spec.CSI.VolumeHandle, awsErr.Message())
				c.failedQueue.Forget(pv.Name)
				return
			}
		}
		c.handleTagUpdateFailure([]*v1.PersistentVolume{pv}, err)
		return
	}

	newTagsHash := computeTagsHash(resourceTags)
	updatedVolume := setPVTagHash(pv, newTagsHash)

	err = c.updateVolume(ctx, updatedVolume)
	if err != nil {
		klog.Errorf("Error updating PV annotations for volume %s: %v", pv.Name, err)
		c.failedQueue.AddRateLimited(pv.Name)
		return
	}

	klog.Infof("Successfully updated PV annotations for volume %s", pv.Name)
	c.failedQueue.Forget(pv.Name)
}
