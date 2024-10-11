package aws_ebs

import (
	"context"
	"fmt"
	"reflect"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"

	configv1 "github.com/openshift/api/config/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
)

const (
	awsSecretNamespace     = "openshift-cluster-csi-drivers"
	awsSecretName          = "ebs-cloud-credentials"
	infrastructureResource = "cluster"
	driverName             = "ebs.csi.aws.com"
)

// EBSVolumeTagController is the custom controller
type EBSVolumeTagController struct {
	configClient configclient.Interface
	coreClient   corev1.CoreV1Interface
	queue        workqueue.RateLimitingInterface
	informer     cache.SharedIndexInformer
	awsEC2Client *ec2.EC2
}

// NewEBSVolumeTagController initializes the controller and sets up the AWS session using credentials from a Kubernetes secret
func NewEBSVolumeTagController(configClient configclient.Interface, coreClient corev1.CoreV1Interface) (*EBSVolumeTagController, error) {
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())

	awsRegion, err := getAWSRegionFromInfrastructure(configClient)
	if err != nil {
		return nil, fmt.Errorf("error retrieving AWS region from infrastructure: %v", err)
	}

	// Initialize AWS EC2 client using the credentials from the secret
	awsEC2Client, err := getEC2Client(context.TODO(), coreClient, awsRegion)
	if err != nil {
		return nil, fmt.Errorf("error creating AWS EC2 client: %v", err)
	}

	// Create a listerWatcher for the Infrastructure resource
	listerWatcher := &cache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			return configClient.ConfigV1().Infrastructures().List(context.TODO(), options)
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			options.FieldSelector = fields.OneTermEqualSelector("metadata.name", infrastructureResource).String()
			return configClient.ConfigV1().Infrastructures().Watch(context.TODO(), options)
		},
	}

	// Set up a shared informer
	informer := cache.NewSharedIndexInformer(
		listerWatcher,
		&configv1.Infrastructure{},
		time.Minute*10,
		cache.Indexers{},
	)

	controller := &EBSVolumeTagController{
		configClient: configClient,
		coreClient:   coreClient,
		queue:        queue,
		informer:     informer,
		awsEC2Client: awsEC2Client,
	}

	// Add event handlers to the informer
	_, err = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			controller.handleAdd(obj)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			controller.handleUpdate(oldObj, newObj)
		},
		DeleteFunc: func(obj interface{}) {
			controller.handleDelete(obj)
		},
	})
	if err != nil {
		return nil, err
	}

	return controller, nil
}

// getEC2Client retrieves AWS credentials from the secret and creates an AWS EC2 client
func getEC2Client(ctx context.Context, coreClient corev1.CoreV1Interface, awsRegion string) (*ec2.EC2, error) {
	// Fetch the secret containing AWS credentials
	secret, err := coreClient.Secrets(awsSecretNamespace).Get(ctx, awsSecretName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("error retrieving AWS credentials secret: %v", err)
	}

	awsAccessKeyID := secret.Data["aws_access_key_id"]
	awsSecretAccessKey := secret.Data["aws_secret_access_key"]

	// Create a new AWS session using the credentials
	awsSession, err := session.NewSession(&aws.Config{
		Region:      aws.String(awsRegion),
		Credentials: credentials.NewStaticCredentials(string(awsAccessKeyID), string(awsSecretAccessKey), ""),
	})
	if err != nil {
		return nil, fmt.Errorf("error creating AWS session: %v", err)
	}

	// Return an EC2 client
	return ec2.New(awsSession), nil
}

// getAWSRegionFromInfrastructure retrieves the AWS region from the Infrastructure resource in OpenShift
func getAWSRegionFromInfrastructure(configClient configclient.Interface) (string, error) {
	infra, err := configClient.ConfigV1().Infrastructures().Get(context.TODO(), infrastructureResource, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to retrieve Infrastructure resource: %v", err)
	}

	if infra.Status.PlatformStatus == nil || infra.Status.PlatformStatus.AWS == nil {
		return "", fmt.Errorf("AWS platform status not found in Infrastructure resource")
	}

	return infra.Status.PlatformStatus.AWS.Region, nil
}

// handleAdd is called when an Infrastructure resource is added
func (c *EBSVolumeTagController) handleAdd(obj interface{}) {
	infra := obj.(*configv1.Infrastructure)
	klog.Infof("Infrastructure resource added: %s", infra.Name)
	c.processInfrastructure(infra)
}

// handleUpdate is called when an Infrastructure resource is updated
func (c *EBSVolumeTagController) handleUpdate(oldObj, newObj interface{}) {
	oldInfra := oldObj.(*configv1.Infrastructure)
	newInfra := newObj.(*configv1.Infrastructure)

	klog.Infof("Infrastructure resource updated: %s", newInfra.Name)

	if !reflect.DeepEqual(oldInfra.Status.PlatformStatus.AWS.ResourceTags, newInfra.Status.PlatformStatus.AWS.ResourceTags) {
		klog.Infof("AWS ResourceTags changed: triggering processing")
		c.processInfrastructure(newInfra)
	}
}

// handleDelete is called when an Infrastructure resource is deleted
func (c *EBSVolumeTagController) handleDelete(obj interface{}) {
	infra := obj.(*configv1.Infrastructure)
	klog.Infof("Infrastructure resource deleted: %s", infra.Name)
}

// processInfrastructure processes the Infrastructure resource and updates EBS tags
func (c *EBSVolumeTagController) processInfrastructure(infra *configv1.Infrastructure) {
	if infra.Status.PlatformStatus != nil && infra.Status.PlatformStatus.AWS != nil {
		awsInfra := infra.Status.PlatformStatus.AWS
		err := c.fetchPVsAndUpdateTags(awsInfra.ResourceTags)
		if err != nil {
			klog.Errorf("Error processing PVs for infrastructure update: %v", err)
		}
	}
}

// fetchPVsAndUpdateTags retrieves all PVs and updates the AWS EBS tags
func (c *EBSVolumeTagController) fetchPVsAndUpdateTags(resourceTags []configv1.AWSResourceTag) error {
	pvs, err := c.coreClient.PersistentVolumes().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("error fetching PVs: %v", err)
	}

	for _, pv := range pvs.Items {
		if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == driverName {
			volumeID := pv.Spec.CSI.VolumeHandle
			err = c.updateEBSTags(volumeID, resourceTags)
			if err != nil {
				klog.Errorf("Error updating tags for volume %s: %v", volumeID, err)
			} else {
				klog.Infof("Successfully updated tags for volume %s", volumeID)
			}
		}
	}

	return nil
}

// updateEBSTags updates the tags of an AWS EBS volume
func (c *EBSVolumeTagController) updateEBSTags(volumeID string, resourceTags []configv1.AWSResourceTag) error {
	existingTagsOutput, err := c.awsEC2Client.DescribeTags(&ec2.DescribeTagsInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("resource-id"),
				Values: []*string{aws.String(volumeID)},
			},
		},
	})
	if err != nil {
		return err
	}

	mergedTags := mergeTags(existingTagsOutput.Tags, resourceTags)

	klog.Infof("Updating EBS tags for volume ID %s with tags: %v", volumeID, mergedTags)

	_, err = c.awsEC2Client.CreateTags(&ec2.CreateTagsInput{
		Resources: []*string{aws.String(volumeID)},
		Tags:      mergedTags,
	})

	return err
}

// mergeTags merges existing AWS tags with new resource tags from OpenShift infrastructure
func mergeTags(existingTags []*ec2.TagDescription, resourceTags []configv1.AWSResourceTag) []*ec2.Tag {
	tagMap := make(map[string]string)

	// Add existing tags to the map
	for _, tagDesc := range existingTags {
		tagMap[*tagDesc.Key] = *tagDesc.Value
	}

	// Override with new resource tags
	for _, tag := range resourceTags {
		tagMap[tag.Key] = tag.Value
	}

	// Convert map back to slice of ec2.Tag
	var mergedTags []*ec2.Tag
	for key, value := range tagMap {
		mergedTags = append(mergedTags, &ec2.Tag{
			Key:   aws.String(key),
			Value: aws.String(value),
		})
	}

	return mergedTags
}

// Run starts the controller and processes events from the informer
func (c *EBSVolumeTagController) Run(ctx context.Context) {
	defer c.queue.ShutDown()

	klog.Infof("Starting EBSVolumeTagController")
	go c.informer.Run(ctx.Done())

	if !cache.WaitForCacheSync(ctx.Done(), c.informer.HasSynced) {
		klog.Fatal("Failed to sync caches")
		return
	}

	<-ctx.Done()

	klog.Infof("Shutting down EBSVolumeTagController")
}
