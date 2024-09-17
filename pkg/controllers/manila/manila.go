package manila

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/openstack/sharedfilesystems/v2/sharetypes"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/csi-driver-manila-operator/assets"
	"github.com/openshift/csi-driver-manila-operator/pkg/util"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"github.com/openshift/library-go/pkg/operator/resource/resourceread"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8serrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	storagelisters "k8s.io/client-go/listers/storage/v1"
	"k8s.io/klog/v2"
)

// This ManilaController watches OpenStack and:
//  1. Installs Manila CSI drivers (Manila itself, NFS) once
//     it detects that there is Manila present (by running provided
//     manilaOperatorSet).
//  2. Creates StorageClass for each share type provided by Manila.
//  3. If there is no Manila in the OpenStack where the cluster runs,
//     it marks the operator with condition Disabled=true.
//
// Note that the CSI driver(s) are not un-installed when Manila becomes
// missing or it stops providing shares of given type - Manila bight be
// under (short?) maintenance / reconfiguration.
// Similarly, StorageClasses are not deleted when a share type disappears
// from Manila.
type ManilaController struct {
	operatorClient     v1helpers.OperatorClient
	kubeClient         kubernetes.Interface
	storageClassLister storagelisters.StorageClassLister
	csiDriverLister    storagelisters.CSIDriverLister
	// Controllers to start when Manila is detected
	csiControllers     []Runnable
	controllersRunning bool
	eventRecorder      events.Recorder
}

type Runnable interface {
	Run(ctx context.Context, workers int)
}

const (
	// Minimal interval between controller resyncs. The controller will detect
	// new share types in Manila and create StorageClasses for them at least
	// once per this interval.
	resyncInterval = 20 * time.Minute

	operatorConditionPrefix = "ManilaController"
)

func NewManilaController(
	operatorClient v1helpers.OperatorClient,
	kubeClient kubernetes.Interface,
	informers v1helpers.KubeInformersForNamespaces,
	csiControllers []Runnable,
	eventRecorder events.Recorder) factory.Controller {

	scInformer := informers.InformersFor("").Storage().V1().StorageClasses()
	csiInformer := informers.InformersFor("").Storage().V1().CSIDrivers()
	c := &ManilaController{
		operatorClient:     operatorClient,
		kubeClient:         kubeClient,
		storageClassLister: scInformer.Lister(),
		csiDriverLister:    csiInformer.Lister(),
		csiControllers:     csiControllers,
		eventRecorder:      eventRecorder.WithComponentSuffix("ManilaController"),
	}
	return factory.New().WithSync(c.sync).WithSyncDegradedOnError(operatorClient).ResyncEvery(resyncInterval).WithInformers(
		operatorClient.Informer(),
		scInformer.Informer(),
		csiInformer.Informer(),
	).ToController("ManilaController", eventRecorder)
}

func (c *ManilaController) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	klog.V(4).Infof("Manila sync started")
	defer klog.V(4).Infof("Manila sync finished")

	opSpec, _, _, err := c.operatorClient.GetOperatorState()
	if err != nil {
		return err
	}
	if opSpec.ManagementState != operatorv1.Managed {
		return nil
	}

	openstackClient, err := NewOpenStackClient(util.CloudConfigFilename)
	if err != nil {
		return c.setDisabledCondition(ctx, fmt.Sprintf("Unable to connect to OpenStack: %v", err))
	}
	shareTypes, err := openstackClient.GetShareTypes()
	if err != nil {
		return c.setDisabledCondition(ctx, fmt.Sprintf("Unable to retrieve Manila share types: %v", err))
	}

	if len(shareTypes) == 0 {
		klog.V(4).Infof("Manila does not provide any share types")
		return c.setDisabledCondition(ctx, "Manila does not provide any share types")
	}
	// Manila has some shares: start the actual CSI driver controller sets
	if !c.controllersRunning {
		klog.V(4).Infof("Starting CSI driver controllers")
		for _, ctrl := range c.csiControllers {
			go func(ctrl Runnable) {
				defer utilruntime.HandleCrash()
				ctrl.Run(ctx, 1)
			}(ctrl)
		}
		c.controllersRunning = true
	}

	err = c.syncCSIDriver(ctx)
	if err != nil {
		return err
	}

	err = c.syncStorageClasses(ctx, shareTypes)
	if err != nil {
		return err
	}

	return c.setEnabledCondition(ctx)
}

func (c *ManilaController) getFsGroupPolicy(ctx context.Context) storagev1.FSGroupPolicy {

	// NOTE: Set the fsGroupPolicy param to None as default value
	fsGroupPolicy := storagev1.NoneFSGroupPolicy

	fsGroupPolicyFromEnv := storagev1.FSGroupPolicy(os.Getenv("CSI_FSGROUP_POLICY"))
	switch fsGroupPolicyFromEnv {
	case storagev1.NoneFSGroupPolicy, storagev1.FileFSGroupPolicy, storagev1.ReadWriteOnceWithFSTypeFSGroupPolicy:
		fsGroupPolicy = fsGroupPolicyFromEnv
	default:
		if fsGroupPolicyFromEnv != "" {
			klog.V(4).Infof("Invalid CSI_FSGROUP_POLICY %q. Ignoring.", fsGroupPolicyFromEnv)
		}
	}

	return fsGroupPolicy
}

func (c *ManilaController) syncCSIDriver(ctx context.Context) error {
	klog.V(4).Infof("Starting CSI driver config refresh")
	defer klog.V(4).Infof("CSI driver config refresh finished")

	var errs []error

	stream, e := assets.ReadFile("csidriver.yaml")
	if e != nil {
		panic("Error loading the CSIDriver resource")
	}

	cr := resourceread.ReadCSIDriverV1OrDie(stream)
	f := c.getFsGroupPolicy(ctx)
	cr.Spec.FSGroupPolicy = &f

	_, _, err := resourceapply.ApplyCSIDriver(ctx, c.kubeClient.StorageV1(), c.eventRecorder, cr)

	if err != nil {
		errs = append(errs, err)
	}

	return k8serrors.NewAggregate(errs)
}

func (c *ManilaController) syncStorageClasses(ctx context.Context, shareTypes []sharetypes.ShareType) error {
	var errs []error
	for _, shareType := range shareTypes {
		klog.V(4).Infof("Syncing storage class for shareType type %s", shareType.Name)
		sc := c.generateStorageClass(shareType)
		_, _, err := resourceapply.ApplyStorageClass(ctx, c.kubeClient.StorageV1(), c.eventRecorder, sc)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return k8serrors.NewAggregate(errs)
}

func (c *ManilaController) applyStorageClass(ctx context.Context, expected *storagev1.StorageClass) error {
	_, _, err := resourceapply.ApplyStorageClass(ctx, c.kubeClient.StorageV1(), c.eventRecorder, expected)
	return err
}

func (c *ManilaController) generateStorageClass(shareType sharetypes.ShareType) *storagev1.StorageClass {
	/* As per RFC 1123 the storage class name must consist of lower case alphanumeric character,  '-' or '.'
	   and must start and end with an alphanumeric character.
	*/
	storageClassName := util.StorageClassNamePrefix + strings.ToLower(strings.Replace(shareType.Name, "_", "-", -1))
	delete := corev1.PersistentVolumeReclaimDelete
	immediate := storagev1.VolumeBindingImmediate
	sc := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: storageClassName,
		},
		Provisioner: "manila.csi.openstack.org",
		Parameters: map[string]string{
			"type": shareType.Name,
			"csi.storage.k8s.io/provisioner-secret-name":       util.ManilaSecretName,
			"csi.storage.k8s.io/provisioner-secret-namespace":  util.OperandNamespace,
			"csi.storage.k8s.io/node-stage-secret-name":        util.ManilaSecretName,
			"csi.storage.k8s.io/node-stage-secret-namespace":   util.OperandNamespace,
			"csi.storage.k8s.io/node-publish-secret-name":      util.ManilaSecretName,
			"csi.storage.k8s.io/node-publish-secret-namespace": util.OperandNamespace,
		},
		ReclaimPolicy:     &delete,
		VolumeBindingMode: &immediate,
	}
	return sc
}

func (c *ManilaController) setEnabledCondition(ctx context.Context) error {
	_, _, err := v1helpers.UpdateStatus(
		ctx,
		c.operatorClient,
		removeConditionFn(operatorConditionPrefix+"Disabled"),
	)
	return err
}

func (c *ManilaController) setDisabledCondition(ctx context.Context, msg string) error {
	disabledCnd := operatorv1.OperatorCondition{
		Type:    operatorConditionPrefix + "Disabled",
		Status:  operatorv1.ConditionTrue,
		Reason:  "NoManila",
		Message: msg,
	}
	_, _, err := v1helpers.UpdateStatus(
		ctx,
		c.operatorClient,
		v1helpers.UpdateConditionFn(disabledCnd),
	)
	return err
}

func removeConditionFn(cnd string) v1helpers.UpdateStatusFunc {
	return func(oldStatus *operatorv1.OperatorStatus) error {
		v1helpers.RemoveOperatorCondition(&oldStatus.Conditions, cnd)
		return nil
	}
}
