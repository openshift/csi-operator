package config

import (
	"context"

	opv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/csi-operator/pkg/clients"
	"github.com/openshift/csi-operator/pkg/generator"
	"github.com/openshift/csi-operator/pkg/operator/volume_snapshot_class"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivernodeservicecontroller"
	"github.com/openshift/library-go/pkg/operator/csi/csistorageclasscontroller"
	"github.com/openshift/library-go/pkg/operator/deploymentcontroller"
)

// OperatorConfig is configuration of a CSI driver operator.
type OperatorConfig struct {
	// CSI driver name. ClusterCSIDriver with this name will be used as CR of this operator.
	CSIDriverName opv1.CSIDriverName
	// HTTP User-agent for connection to the API server
	UserAgent string
	// Reader for generated assets.
	AssetReader generator.AssetReader
	// Base directory with operator's generated assets. It should have "standalone" and "hypershift" directories.
	AssetDir string
	// Function that returns OperatorControllerConfig based on the cluster flavour and Clients.
	// Clients are fully established when this builder is called, so it can create informers.
	OperatorControllerConfigBuilder func(context.Context, generator.ClusterFlavour, *clients.Clients) (*OperatorControllerConfig, error)
	// CloudConfigNamespace defines namespace in which cloud-configuration exists
	CloudConfigNamespace string
	// Removable should be true if the operator and its operand can be removed
	Removable bool
}

// OperatorControllerConfig is configuration of controllers that are used to deploy CSI drivers.
// OperatorConfig and OperatorControllerConfig are separate structures, because OperatorConfig is used to populate the
// `Clients` instance and OperatorControllerConfig already needs `Client` instance populated to create informers.
type OperatorControllerConfig struct {
	// Prefix of all library-go style controllers.
	ControllerNamePrefix string

	// List of hooks to add to CSI driver controller Deployment.
	DeploymentHooks []deploymentcontroller.DeploymentHookFunc
	// List of informers that should be added to the Deployment controller.
	DeploymentInformers []factory.Informer
	// List of secrets that should be watched by the Deployment controller. Change of content of these secrets
	// will cause redeployment of the controller.
	DeploymentWatchedSecretNames []string

	// List of secrets that should be watched by daemonset controller
	DaemonSetWatchedSecretNames []string

	// List of library-go style controllers to run in the control-plane namespace.
	ExtraControlPlaneControllers []factory.Controller

	// List of hooks to add to CSI driver guest DaemonSet.
	GuestDaemonSetHooks []csidrivernodeservicecontroller.DaemonSetHookFunc
	// List of informers that should be added to the guest DaemonSet controller.
	GuestDaemonSetInformers []factory.Informer
	// List of hooks should be run on the storage classes.
	// No informers here, because StorageClassController does not accept any.
	StorageClassHooks []csistorageclasscontroller.StorageClassHookFunc

	VolumeSnapshotClassHooks []volume_snapshot_class.VolumeSnapshotClassHookFunc

	// ExtraReplacements defines additional replacements that should be made to assets
	ExtraReplacementsFunc func() []string
}

func (o *OperatorControllerConfig) AddDeploymentHook(hook deploymentcontroller.DeploymentHookFunc, informers ...factory.Informer) {
	o.DeploymentHooks = append(o.DeploymentHooks, hook)
	if len(informers) > 0 {
		o.DeploymentInformers = append(o.DeploymentInformers, informers...)
	}
}

func (o *OperatorControllerConfig) AddDaemonSetHook(hook csidrivernodeservicecontroller.DaemonSetHookFunc, informers ...factory.Informer) {
	o.GuestDaemonSetHooks = append(o.GuestDaemonSetHooks, hook)
	if len(informers) > 0 {
		o.GuestDaemonSetInformers = append(o.GuestDaemonSetInformers, informers...)
	}
}

func (o *OperatorControllerConfig) AddStorageClassHook(hook csistorageclasscontroller.StorageClassHookFunc) {
	o.StorageClassHooks = append(o.StorageClassHooks, hook)
}

// AddDeploymentHookBuilders is a helper function to add multiple Deployment hooks to the OperatorControllerConfig.
func (o *OperatorControllerConfig) AddDeploymentHookBuilders(c *clients.Clients, builders ...DeploymentHookBuilder) {
	for _, builder := range builders {
		hook, informers := builder(c)
		o.AddDeploymentHook(hook, informers...)
	}
}

// AddDaemonSetHookBuilders is a helper function to add multiple DaemonSet hooks to the OperatorControllerConfig.
func (o *OperatorControllerConfig) AddDaemonSetHookBuilders(c *clients.Clients, builders ...DaemonSetHookBuilder) {
	for _, builder := range builders {
		hook, informers := builder(c)
		o.AddDaemonSetHook(hook, informers...)
	}
}

// AddStorageClassHookBuilders is a helper function to add multiple StorageClass hooks to the OperatorControllerConfig.
func (o *OperatorControllerConfig) AddStorageClassHookBuilders(c *clients.Clients, builders ...StorageClassHookBuilder) {
	for _, builder := range builders {
		hook := builder(c)
		o.AddStorageClassHook(hook)
	}
}

type DeploymentHookBuilder func(c *clients.Clients) (deploymentcontroller.DeploymentHookFunc, []factory.Informer)
type DaemonSetHookBuilder func(c *clients.Clients) (csidrivernodeservicecontroller.DaemonSetHookFunc, []factory.Informer)
type StorageClassHookBuilder func(c *clients.Clients) csistorageclasscontroller.StorageClassHookFunc

func (o *OperatorControllerConfig) GetControllerName(suffix string) string {
	return o.ControllerNamePrefix + suffix
}
