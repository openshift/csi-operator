package volume_snapshot_class

import (
	"context"
	"fmt"
	"time"

	snapshotapi "github.com/kubernetes-csi/external-snapshotter/client/v6/apis/volumesnapshot/v1"
	snapshotclient "github.com/kubernetes-csi/external-snapshotter/client/v6/clientset/versioned"
	snapshotscheme "github.com/kubernetes-csi/external-snapshotter/client/v6/clientset/versioned/scheme"
	operatorapi "github.com/openshift/api/operator/v1"
	"github.com/openshift/csi-operator/pkg/clients"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"github.com/openshift/library-go/pkg/operator/resource/resourcemerge"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/klog/v2"
)

var (
	genericScheme = runtime.NewScheme()
	genericCodecs = serializer.NewCodecFactory(genericScheme)
	genericCodec  = genericCodecs.UniversalDeserializer()
)

func init() {
	if err := snapshotscheme.AddToScheme(genericScheme); err != nil {
		panic(err)
	}
}

type VolumeSnapshotClassHookFunc func(*operatorapi.OperatorSpec, *snapshotapi.VolumeSnapshotClass) error

type VolumeSnapshotClassController struct {
	name                  string
	assetFunc             resourceapply.AssetFunc
	files                 []string
	builder               *clients.Builder
	commonClient          *clients.Clients
	foundSnapshotClassCRD bool

	snapshotClient              snapshotclient.Interface
	operatorClient              v1helpers.OperatorClient
	optionalVolumeSnapshotHooks []VolumeSnapshotClassHookFunc
	eventRecorder               events.Recorder
}

func NewVolumeSnapshotClassController(
	name string,
	assetFunc resourceapply.AssetFunc,
	files []string,
	builder *clients.Builder,
	eventRecorder events.Recorder,
	optionalVolumeSnapshotHooks ...VolumeSnapshotClassHookFunc) factory.Controller {
	commonClient := builder.GetClient()

	c := &VolumeSnapshotClassController{
		name:                        name,
		assetFunc:                   assetFunc,
		files:                       files,
		commonClient:                commonClient,
		operatorClient:              commonClient.OperatorClient,
		optionalVolumeSnapshotHooks: optionalVolumeSnapshotHooks,
		// we will assume SnapshotCRDClass is not installed
		foundSnapshotClassCRD: false,
		builder:               builder,
		eventRecorder:         eventRecorder,
	}

	return factory.New().WithSync(
		c.Sync,
	).ResyncEvery(
		time.Minute,
	).WithSyncDegradedOnError(
		c.operatorClient,
	).WithInformers(
		c.operatorClient.Informer(),
	).ToController(
		"VolumeSnapshotClassController",
		eventRecorder,
	)
}

func (c *VolumeSnapshotClassController) checkForVolumeSnapshotClassCRD(ctx context.Context) bool {
	name := "volumesnapshotclasses.snapshot.storage.k8s.io"
	_, err := c.commonClient.APIExtClient.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, name, metav1.GetOptions{})
	return err == nil
}

func (c *VolumeSnapshotClassController) WithHooks(optionalVolumeSnapshotHooks ...VolumeSnapshotClassHookFunc) *VolumeSnapshotClassController {
	c.optionalVolumeSnapshotHooks = append(c.optionalVolumeSnapshotHooks, optionalVolumeSnapshotHooks...)
	return c
}

func (c *VolumeSnapshotClassController) Sync(ctx context.Context, syncCtx factory.SyncContext) error {
	klog.V(4).Infof("StorageClassController sync started")
	defer klog.V(4).Infof("StorageClassController sync finished")

	opSpec, _, _, err := c.operatorClient.GetOperatorState()
	if err != nil {
		return err
	}
	if opSpec.ManagementState != operatorapi.Managed {
		return nil
	}

	if !c.foundSnapshotClassCRD && c.checkForVolumeSnapshotClassCRD(ctx) {
		c.snapshotClient, err = c.builder.AddSnapshotClient(ctx)
		if err != nil {
			return err
		}
		c.foundSnapshotClassCRD = true
	}

	if !c.foundSnapshotClassCRD {
		klog.V(4).Infof("SnapshotClass CRDs are not installed, skipping creating volumeSnapshotClass")
		return nil
	}

	for _, file := range c.files {
		if err := c.syncVolumeSnapshotClass(ctx, opSpec, file); err != nil {
			return err
		}
	}
	return nil
}

func (c *VolumeSnapshotClassController) syncVolumeSnapshotClass(ctx context.Context, opSpec *operatorapi.OperatorSpec, assetFile string) error {
	expectedVSCBytes, err := c.assetFunc(assetFile)
	if err != nil {
		return err
	}

	vsc, err := c.parseVolumeSnapshotClass(expectedVSCBytes)
	if err != nil {
		return err
	}

	for i := range c.optionalVolumeSnapshotHooks {
		err := c.optionalVolumeSnapshotHooks[i](opSpec, vsc)
		if err != nil {
			return fmt.Errorf("error running hook function (index=%d): %w", i, err)
		}
	}

	existingVSC, err := c.snapshotClient.SnapshotV1().VolumeSnapshotClasses().Get(ctx, vsc.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = c.snapshotClient.SnapshotV1().VolumeSnapshotClasses().Create(ctx, vsc, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("failed to created volumesnapshot class: %v", err)
		}
		return nil
	}

	existingVSCCopy := existingVSC.DeepCopy()
	existingParams := existingVSCCopy.Parameters

	modified := resourcemerge.BoolPtr(false)

	resourcemerge.EnsureObjectMeta(modified, &existingVSCCopy.ObjectMeta, vsc.DeepCopy().ObjectMeta)
	paramSame := equality.Semantic.DeepEqual(existingParams, vsc.Parameters)

	if paramSame && !*modified {
		return nil
	}

	vsc.ObjectMeta = *existingVSC.ObjectMeta.DeepCopy()
	vsc.TypeMeta = existingVSC.TypeMeta

	klog.V(4).Infof("volume snapshot classs %s modified outside of openshift - updating", assetFile)
	_, err = c.snapshotClient.SnapshotV1().VolumeSnapshotClasses().Update(ctx, vsc, metav1.UpdateOptions{})
	return err
}

func (c *VolumeSnapshotClassController) parseVolumeSnapshotClass(vscBytes []byte) (*snapshotapi.VolumeSnapshotClass, error) {
	requiredObj, _, err := genericCodec.Decode(vscBytes, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("can not decode %q: %v", vscBytes, err)
	}

	vsc, ok := requiredObj.(*snapshotapi.VolumeSnapshotClass)
	if !ok {
		return nil, fmt.Errorf("invalid volumesnapshot class: %+v", requiredObj)
	}
	return vsc, nil
}
