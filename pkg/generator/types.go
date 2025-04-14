package generator

import (
	"k8s.io/apimachinery/pkg/util/sets"
)

// ClusterFlavour is a flavour of OpenShift cluster for which we generate assets.
type ClusterFlavour string

const (
	// FlavourStandalone represents standalone cluster, including single-node one.
	FlavourStandalone ClusterFlavour = "standalone"
	// FlavourHyperShift represents HyperShift cluster.
	FlavourHyperShift ClusterFlavour = "hypershift"
)

var (
	// AllFlavours is a set of all supported flavours.
	AllFlavours = sets.New[ClusterFlavour](FlavourStandalone, FlavourHyperShift)
	// StandaloneOnly is standalone flavour only.
	StandaloneOnly = sets.New[ClusterFlavour](FlavourStandalone)
	// HyperShiftOnly is HyperShift flavour only.
	HyperShiftOnly = sets.New[ClusterFlavour](FlavourHyperShift)
)

// CSIDriverGeneratorConfig is a configuration for the generator for a single CSI driver operator.
type CSIDriverGeneratorConfig struct {
	// Prefix of names of Kubernetes API objects, for example `aws-ebs-csi-driver`.
	// The generator will replace all occurrences of `${ASSET_PREFIX}` in the assets with this value.
	AssetPrefix string
	// Short prefix of names of Kubernetes API objects, for example `ebs`.
	// The generator will replace all occurrences of `${ASSET_SHORT_PREFIX}` in the assets with this value.
	AssetShortPrefix string
	// Name of the CSI driver, for example `ebs.csi.aws.com`
	DriverName string
	// Configuration of control-plane components. They will be installed in control-plane namespace on HyperShift
	// or to openshift-cluster-csi-drivers namespace on standalone cluster.
	ControllerConfig *ControlPlaneConfig
	// Configuration of guest components. They will always be installed in openshift-cluster-csi-drivers namespace.
	GuestConfig *GuestConfig
	// Set this flag if only "standalone" subdirectory to be populated
	StandaloneOnly bool

	// Directory where to save generated assets. "standalone" and "hypershift" subdirectories will be created there.
	OutputDir string
}

// Configuration of control-plane components. They will be installed in control-plane namespace on HyperShift
// or to openshift-cluster-csi-drivers namespace on standalone cluster.
type ControlPlaneConfig struct {
	// Name of the template asset that contains Deployment of the controller.
	// This Deployment is applied as a patch against base/controller.yaml.
	// It may contain several containers, however, it should not contain any CSI sidecars - they will be added
	// by the generator based on Sidecars field.
	// The Deployment will be available as "controller.yaml" in the generated assets and can be patched
	// by
	DeploymentTemplateAssetName string
	// Metric port exposed by the driver itself.
	// Sidecar metrics ports are not included here, they will be added dynamically from sidecar config below.
	MetricsPort *MetricsPort
	// Liveness probe TCP port number exposed by the driver itself, i.e. by DeploymentTemplateAssetName.
	// It will be injected to liveness probe sidecar automatically.
	LivenessProbePort uint16

	// Start of TCP port number range to be used by sidecar to expose metrics on loopback interface.
	// The generator will allocate a port for each sidecar and add kube-rbac-proxy in front of it.
	SidecarLocalMetricsPortStart uint16
	// Start of TCP port number range to be used by sidecar to expose metrics via kube-rbac-proxy on the container
	// interface. This port will be allocated on the host, if the CSI driver uses host networking.
	SidecarExposedMetricsPortStart uint16
	// Configuration of all sidecars to inject into the controller Deployment.
	Sidecars []SidecarConfig

	// Assets to be created in the control plane namespace, incl. RBAC, ServiceAccounts, etc.
	// They will be managed by StaticResourcesController.
	Assets Assets
	// Patches to apply to any assets in the control plane namespace, not necessarily to the ones defined in
	// Assets. The controller Deployment can be patched too.
	AssetPatches AssetPatches
}

// Configuration of metric ports exposed by the CSI driver itself.
type MetricsPort struct {
	// TCP port number exposed by the driver itself on loopback interface.
	LocalPort uint16
	// TCP port number that should be exposed by kube-rbac-proxy on the container interface.
	ExposedPort uint16
	// Name of the port. It is used in metrics Service and ServiceMonitor.
	Name string
	// If true, kube-rbac-proxy will be injected in front of the LocalPort by the generator, exposing ExposedPort.
	InjectKubeRBACProxy bool
}

// Configuration of a single CSI sidecar. It usually contains also kube-rbac-proxy sidecar.
type SidecarConfig struct {
	// Name of the template asset that contains strategic merge patch with Deployment of the sidecar.
	// It already contains kube-rbac-proxy, if the sidecar exposes any metrics.
	// The template should be flavour-agnostic, i.e. work on both standalone and HyperShift.
	TemplateAssetName string
	// Extra CSI driver specific arguments to add to the first container of TemplateAssetName, such as timeouts.
	ExtraArguments []string
	// If true, the MetricPortName will be added to metrics Service and ServiceMonitor.
	HasMetricsPort bool
	// Name of the port to include in metrics Service and ServiceMonitor.
	MetricPortName string
	// Assets to add to the *guest* part of the CSI driver. Typically, RBAC objects needed by the sidecar
	// - the sidecar will operate in the guest cluster only and therefore should have its RBAC rules there.
	GuestAssetNames []string
	// List of patches to apply to the sidecar. Typically, patches that adds hypershift-specific fields to the sidecar.
	AssetPatches AssetPatches
}

// Configuration of guest components. They will always be installed in openshift-cluster-csi-drivers namespace.
type GuestConfig struct {
	// Name of the template asset that contains DaemonSet of the guest components.
	DaemonSetTemplateAssetName string
	// Metric ports exposed by the driver itself.
	MetricsPort *MetricsPort
	// Liveness probe TCP port number exposed by the driver itself, i.e. by DaemonSetTemplateAssetName.
	LivenessProbePort uint16
	// port where node-registrar will expose health check endpoint
	NodeRegistrarHealthCheckPort uint16
	// Configuration of all sidecars to inject into the guest DaemonSet.
	Sidecars []SidecarConfig

	// Other assets to be created in the guest cluster, including RBAC, ServiceAccounts, StorageClasses,
	// VolumeSnapshotClasses, etc.
	// All StorageClasses will be managed by StorageClassController.
	// All VolumeSnapshotClasses will be managed by VolumeSnapshotClassController.
	// All other assets will be managed by StaticResourcesController.
	Assets Assets
	// Patches to apply to any assets in the guest namespace, not necessarily to the ones defined in Assets.
	// The driver DaemonSet, StorageClasses, or VolumeSnapshotClasses can be patched too.
	AssetPatches AssetPatches
}

// Assets is array of assets. Using a special type to add methods that add more assets to existing ones.
type Assets []Asset

// Asset is a single conditional asset, i.e. a YAML file. It contains set of cluster favours that should use this asset.
type Asset struct {
	ClusterFlavours sets.Set[ClusterFlavour]
	// AssetName is name of a YAML file that is part of the operator assets (i.e. path to the file from assets/ directory).
	// The generated asset (after all patching etc.) will be available as basename(AssetName) in the final generated
	// asset directory.
	AssetName string
}

// AssetPatches is array of patches. Using a special type to add methods that add more patches to existing ones.
type AssetPatches []AssetPatch

// AssetPatch is a conditional patch to apply to an asset. It contains set of cluster favours that should use this patch.
type AssetPatch struct {
	ClusterFlavours sets.Set[ClusterFlavour]
	// The generated asset to patch. Note: it is relative to the generated asset directory, not to assets/ directory!
	GeneratedAssetName string
	// PatchAssetName is name of a file with the patch that is part of the operator assets (i.e. path to the file from
	// assets/ directory).
	// If the file name has suffix ".patch", it will be applied as JSON patch encoded as YAML. Otherwise, it will be
	// applied as strategic merge patch.
	PatchAssetName string
}

// WithExtraArguments appends the provides arguments to the 'args' array of asset used for the sidecar container.
func (cfg SidecarConfig) WithExtraArguments(extraArguments ...string) SidecarConfig {
	newCfg := cfg
	newCfg.ExtraArguments = extraArguments
	return newCfg
}

// WithAdditionalAssets adds one or more additional assets that will be treated as a strategic merge patches and merged
// into the base asset. This is the preferred way to extend a base asset.
func (cfg SidecarConfig) WithAdditionalAssets(assets ...string) SidecarConfig {
	newCfg := cfg
	newCfg.GuestAssetNames = append(newCfg.GuestAssetNames, assets...)
	return newCfg
}

// WithPatches modifies assets using provided JSON Patch files (in YAML format). It allows values to be modified rather
// than overridden (for example, to add an item to an array). In most cases, strategic merging should be preferred as it
// is significantly more readable.
func (cfg SidecarConfig) WithPatches(flavours sets.Set[ClusterFlavour], namePatchPairs ...string) SidecarConfig {
	newCfg := cfg
	newCfg.AssetPatches = newCfg.AssetPatches.WithPatches(flavours, namePatchPairs...)
	return newCfg
}

// WithAssets adds one or more additional assets that will be treated as a strategic merge patches and merged into the
// base asset. This is the preferred way to extend a base asset.
func (a Assets) WithAssets(flavours sets.Set[ClusterFlavour], assets ...string) Assets {
	newAssets := make([]Asset, 0, len(a)+len(assets))
	newAssets = append(newAssets, a...)
	for _, assetName := range assets {
		newAssets = append(newAssets, Asset{
			ClusterFlavours: flavours,
			AssetName:       assetName,
		})
	}
	return newAssets
}

// WithPatches modifies assets using provided JSON Patch files (in YAML format). It allows values to be modified rather
// than overridden for example to add an item to an array. In most cases, strategic merging should be preferred as it is
// significantly more readable.
func (p AssetPatches) WithPatches(flavours sets.Set[ClusterFlavour], namePatchPairs ...string) AssetPatches {
	if len(namePatchPairs)%2 != 0 {
		panic("namePatchPairs must be even")
	}
	newPatches := make([]AssetPatch, 0, len(p)+len(namePatchPairs)/2)
	newPatches = append(newPatches, p...)
	for i := 0; i < len(namePatchPairs); i += 2 {
		newPatches = append(newPatches, AssetPatch{
			ClusterFlavours:    flavours,
			GeneratedAssetName: namePatchPairs[i],
			PatchAssetName:     namePatchPairs[i+1],
		})
	}
	return newPatches
}

func NewAssets(flavours sets.Set[ClusterFlavour], assets ...string) Assets {
	return Assets{}.WithAssets(flavours, assets...)
}

func NewAssetPatches(flavours sets.Set[ClusterFlavour], namePatchPairs ...string) AssetPatches {
	return AssetPatches{}.WithPatches(flavours, namePatchPairs...)
}
