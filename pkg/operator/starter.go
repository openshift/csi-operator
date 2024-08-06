package operator

import (
	"context"
	"path/filepath"
	"time"

	opv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/csi-operator/assets"
	"github.com/openshift/csi-operator/pkg/clients"
	"github.com/openshift/csi-operator/pkg/driver/common/operator"
	generated_assets "github.com/openshift/csi-operator/pkg/generated-assets"
	"github.com/openshift/csi-operator/pkg/generator"
	"github.com/openshift/csi-operator/pkg/operator/config"
	"github.com/openshift/csi-operator/pkg/operator/volume_snapshot_class"
	"k8s.io/klog/v2"

	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/openshift/library-go/pkg/operator/csi/csicontrollerset"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivercontrollerservicecontroller"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivernodeservicecontroller"
	"github.com/openshift/library-go/pkg/operator/management"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
)

type ConfigProvider func(flavour generator.ClusterFlavour, c *clients.Clients) *config.OperatorConfig

const (
	resync = 20 * time.Minute
)

func RunOperator(ctx context.Context, controllerConfig *controllercmd.ControllerContext, guestKubeConfigString string, opConfig *config.OperatorConfig) error {
	klog.V(2).Infof("Running openshift/csi-operator for %s", opConfig.CSIDriverName)
	isHypershift := guestKubeConfigString != ""
	controlPlaneNamespace := controllerConfig.OperatorNamespace

	flavour := generator.FlavourStandalone
	if isHypershift {
		flavour = generator.FlavourHyperShift
	}

	// Create Clients
	builder := clients.NewBuilder(opConfig.UserAgent, string(opConfig.CSIDriverName), controllerConfig, resync).
		WithHyperShiftGuest(guestKubeConfigString, opConfig.CloudConfigNamespace)

	c := builder.BuildOrDie(ctx)

	klog.Infof("Building clients is done")

	// Build ControllerConfig
	csiOperatorControllerConfig, err := opConfig.OperatorControllerConfigBuilder(ctx, flavour, c)
	if err != nil {
		klog.Errorf("error building operator config: %v", err)
		return err
	}

	// Load generated assets.
	assetDir := filepath.Join(opConfig.AssetDir, string(flavour))
	a, err := generated_assets.NewFromAssets(assets.ReadFile, assetDir)
	if err != nil {
		return err
	}
	defaultReplacements := operator.DefaultReplacements(controlPlaneNamespace)
	if csiOperatorControllerConfig.ExtraReplacementsFunc != nil {
		defaultReplacements = append(defaultReplacements, csiOperatorControllerConfig.ExtraReplacementsFunc()...)
	}

	a.SetReplacements(defaultReplacements)

	// Start controllers that manage resources in the MANAGEMENT cluster.
	controlPlaneControllerInformers := csiOperatorControllerConfig.DeploymentInformers
	controllerHooks := csiOperatorControllerConfig.DeploymentHooks
	credentialsRequestHooks := csiOperatorControllerConfig.CredentialsRequestHooks

	if len(csiOperatorControllerConfig.DeploymentWatchedSecretNames) > 0 {
		controlPlaneSecretInformer := c.GetControlPlaneSecretInformer(controlPlaneNamespace)
		for _, secretName := range csiOperatorControllerConfig.DeploymentWatchedSecretNames {
			controllerHooks = append(controllerHooks, csidrivercontrollerservicecontroller.WithSecretHashAnnotationHook(controlPlaneNamespace, secretName, controlPlaneSecretInformer))
		}
		controlPlaneControllerInformers = append(controlPlaneControllerInformers, controlPlaneSecretInformer.Informer())
	}

	// Only removable operators use conditional static resources.
	// If the operator is not removable, leave these functions nil
	// to unconditionally sync static resources.
	var shouldCreateFn resourceapply.ConditionalFunction = nil
	var shouldDeleteFn resourceapply.ConditionalFunction = nil
	if opConfig.Removable {
		shouldCreateFn = func() bool {
			return getOperatorSyncState(c.OperatorClient) == opv1.Managed
		}
		shouldDeleteFn = func() bool {
			return getOperatorSyncState(c.OperatorClient) == opv1.Removed
		}
	}

	controlPlaneCSIControllerSet := csicontrollerset.NewCSIControllerSet(
		c.OperatorClient,
		c.EventRecorder,
	).WithLogLevelController().WithManagementStateController(
		csiOperatorControllerConfig.GetControllerName("CSIDriver"),
		opConfig.Removable, // true if the operator is removable
	).WithConditionalStaticResourcesController(
		csiOperatorControllerConfig.GetControllerName("DriverControlPlaneStaticResourcesController"),
		c.ControlPlaneKubeClient,
		c.ControlPlaneDynamicClient,
		c.ControlPlaneKubeInformers,
		a.GetAsset,
		a.GetControllerStaticAssetNames(),
		shouldCreateFn,
		shouldDeleteFn,
	).WithCSIConfigObserverController(
		csiOperatorControllerConfig.GetControllerName("DriverCSIConfigObserverController"),
		c.ConfigInformers,
	).WithCSIDriverControllerService(
		csiOperatorControllerConfig.GetControllerName("DriverControllerServiceController"),
		a.GetAsset,
		generated_assets.ControllerDeploymentAssetName,
		c.ControlPlaneKubeClient,
		c.ControlPlaneKubeInformers.InformersFor(controlPlaneNamespace),
		c.ConfigInformers,
		controlPlaneControllerInformers,
		controllerHooks...,
	)
	if err != nil {
		return err
	}

	guestDaemonSetHooks := csiOperatorControllerConfig.GuestDaemonSetHooks
	guestDaemonInformers := csiOperatorControllerConfig.GuestDaemonSetInformers

	if len(csiOperatorControllerConfig.DaemonSetWatchedSecretNames) > 0 {
		nodeSecretInformer := c.GetNodeSecretInformer(clients.CSIDriverNamespace)
		for _, secretName := range csiOperatorControllerConfig.DaemonSetWatchedSecretNames {
			guestDaemonSetHooks = append(guestDaemonSetHooks, csidrivernodeservicecontroller.WithSecretHashAnnotationHook(clients.CSIDriverNamespace, secretName, nodeSecretInformer))
			guestDaemonInformers = append(guestDaemonInformers, nodeSecretInformer.Informer())
		}
	}

	// Prepare credentials request controller when needed
	if credentialsRequestAssetNames := a.GetCredentialsRequestAssetNames(); len(credentialsRequestAssetNames) > 0 {
		controlPlaneCSIControllerSet.WithCredentialsRequestController(
			csiOperatorControllerConfig.GetControllerName("CredentialsRequestController"),
			clients.CSIDriverNamespace,
			a.GetAsset,
			generated_assets.CredentialRequestControllerAssetName,
			c.ControlPlaneDynamicClient,
			c.OperatorInformers,
			credentialsRequestHooks...,
		)
	}

	// Prepare controllers that manage resources in the GUEST cluster.
	guestCSIControllerSet := csicontrollerset.NewCSIControllerSet(
		c.OperatorClient,
		c.EventRecorder,
	).WithConditionalStaticResourcesController(
		csiOperatorControllerConfig.GetControllerName("DriverGuestStaticResourcesController"),
		c.KubeClient,
		c.DynamicClient,
		c.KubeInformers,
		a.GetAsset,
		a.GetGuestStaticAssetNames(),
		shouldCreateFn,
		shouldDeleteFn,
	).WithCSIDriverNodeService(
		csiOperatorControllerConfig.GetControllerName("DriverNodeServiceController"),
		a.GetAsset,
		generated_assets.NodeDaemonSetAssetName,
		c.KubeClient,
		c.KubeInformers.InformersFor(clients.CSIDriverNamespace),
		guestDaemonInformers,
		guestDaemonSetHooks...,
	)

	// Prepare StorageClassController when needed
	if scNames := a.GetStorageClassAssetNames(); len(scNames) > 0 {
		guestCSIControllerSet = guestCSIControllerSet.WithStorageClassController(
			csiOperatorControllerConfig.GetControllerName("DriverStorageClassController"),
			a.GetAsset,
			scNames,
			c.KubeClient,
			c.KubeInformers.InformersFor(""),
			c.OperatorInformers,
			// TODO: add extra informers
			csiOperatorControllerConfig.StorageClassHooks...,
		)
	}

	snapshotAssetNames := a.GetVolumeSnapshotClassAssetNames()

	// Prepare static resource controller for VolumeSnapshotClasses when needed
	if len(snapshotAssetNames) > 0 {
		snapshotClassController := volume_snapshot_class.NewVolumeSnapshotClassController(
			csiOperatorControllerConfig.GetControllerName("VolumeSnapshotController"),
			a.GetAsset,
			snapshotAssetNames,
			builder,
			c.EventRecorder,
			csiOperatorControllerConfig.VolumeSnapshotClassHooks...,
		)
		csiOperatorControllerConfig.ExtraControlPlaneControllers = append(csiOperatorControllerConfig.ExtraControlPlaneControllers, snapshotClassController)
	}

	// Start all informers
	c.Start(ctx)
	klog.V(2).Infof("Waiting for informers to sync")
	c.WaitForCacheSync(ctx)
	klog.V(2).Infof("Informers synced")

	// Start controllers
	for _, controller := range csiOperatorControllerConfig.ExtraControlPlaneControllers {
		klog.Infof("Starting controller %s", controller.Name())
		go controller.Run(ctx, 1)
	}
	klog.Info("Starting control plane controllerset")
	go controlPlaneCSIControllerSet.Run(ctx, 1)
	klog.Info("Starting guest controllerset")
	go guestCSIControllerSet.Run(ctx, 1)

	<-ctx.Done()

	return nil
}

// getOperatorSyncState returns the management state of the operator to determine
// how to sync conditional resources. It returns one of the following states:
//
//	Managed: resources should be synced
//	Unmanaged: resources should NOT be synced
//	Removed: resources should be deleted
//
// Errors fetching the operator state will log an error and return Unmanaged
// to avoid syncing resources when the actual state is unknown.
func getOperatorSyncState(operatorClient v1helpers.OperatorClientWithFinalizers) opv1.ManagementState {
	opSpec, _, _, err := operatorClient.GetOperatorState()
	if err != nil {
		klog.Errorf("Failed to get operator state: %v", err)
		return opv1.Unmanaged
	}
	if opSpec.ManagementState != opv1.Managed {
		klog.Infof("Operator is not managed, skipping conditional resource sync")
		return opv1.Unmanaged
	}
	meta, err := operatorClient.GetObjectMeta()
	if err != nil {
		klog.Errorf("Failed to get operator object meta: %v", err)
		return opv1.Unmanaged
	}
	if management.IsOperatorRemovable() && meta.DeletionTimestamp != nil {
		klog.Infof("Operator deletion timestamp is set, removing conditional resources")
		return opv1.Removed
	}
	return opv1.Managed
}
