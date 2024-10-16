package azure_disk

import (
	"context"
	"fmt"
	"os"
	"time"

	snapshotapi "github.com/kubernetes-csi/external-snapshotter/client/v6/apis/volumesnapshot/v1"
	"github.com/openshift/csi-operator/assets"
	"github.com/openshift/csi-operator/pkg/clients"
	"github.com/openshift/csi-operator/pkg/driver/common/operator"
	"github.com/openshift/csi-operator/pkg/generator"
	"github.com/openshift/csi-operator/pkg/operator/config"
	"github.com/openshift/csi-operator/pkg/operator/volume_snapshot_class"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivercontrollerservicecontroller"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivernodeservicecontroller"
	"github.com/openshift/library-go/pkg/operator/csi/csistorageclasscontroller"
	"github.com/openshift/library-go/pkg/operator/resourcesynccontroller"

	dc "github.com/openshift/library-go/pkg/operator/deploymentcontroller"

	opCfgV1 "github.com/openshift/api/config/v1"
	opv1 "github.com/openshift/api/operator/v1"
	commongenerator "github.com/openshift/csi-operator/pkg/driver/common/generator"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	appsV1 "k8s.io/api/apps/v1"
	coreV1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

const (
	cloudCredSecretName   = "azure-disk-credentials"
	metricsCertSecretName = "azure-disk-csi-driver-controller-metrics-serving-cert"
	infrastructureName    = "cluster"
	cloudConfigName       = "kube-cloud-config"
	caBundleKey           = "ca-bundle.pem"
	trustedCAConfigMap    = "azure-disk-csi-driver-trusted-ca-bundle"

	configMapName                        = "cloud-provider-config"
	openshiftDefaultCloudConfigNamespace = "openshift-config"

	generatedAssetBase = "overlays/azure-disk/generated"

	operatorImageVersionEnvVarName = "OPERATOR_IMAGE_VERSION"
	ccmOperatorImageEnvName        = "CLUSTER_CLOUD_CONTROLLER_MANAGER_OPERATOR_IMAGE"

	// for azure stack hub
	configEnvName         = "AZURE_ENVIRONMENT_FILEPATH"
	azureStackCloudConfig = "/etc/azure/azurestackcloud.json"
	// name of volume that contains cloud-config in actual deployment or daemonset
	podAzureCfgVolumeName = "cloud-config"

	diskEncryptionSetID = "diskEncryptionSetID"

	// name of local cloud-config copied to openshift-cluster-csi-driver namespace.
	localCloudConfigName = "azure-cloud-config"

	incremetalSnapshotKey = "incremental"
)

func GetAzureDiskGeneratorConfig() *generator.CSIDriverGeneratorConfig {
	return &generator.CSIDriverGeneratorConfig{
		AssetPrefix:      "azure-disk-csi-driver",
		AssetShortPrefix: "azure-disk",
		DriverName:       "disk.csi.azure.com",
		OutputDir:        generatedAssetBase,

		ControllerConfig: &generator.ControlPlaneConfig{
			DeploymentTemplateAssetName: "overlays/azure-disk/patches/controller_add_driver.yaml",
			LivenessProbePort:           10301,
			MetricsPorts: []generator.MetricsPort{
				{
					LocalPort:           commongenerator.AzureDiskControllerLoopbackMetricsPortStart,
					InjectKubeRBACProxy: true,
					ExposedPort:         commongenerator.AzureDiskControllerExposedMetricsPortStart,
					Name:                "driver-m",
				},
			},
			SidecarLocalMetricsPortStart:   commongenerator.AzureDiskControllerLoopbackMetricsPortStart + 1,
			SidecarExposedMetricsPortStart: commongenerator.AzureDiskControllerExposedMetricsPortStart + 1,
			Sidecars: []generator.SidecarConfig{
				commongenerator.DefaultProvisionerWithSnapshots.WithExtraArguments(
					"--default-fstype=ext4",
					"--feature-gates=Topology=true",
					"--extra-create-metadata=true",
					"--strict-topology=true",
					"--timeout=30s",
					"--worker-threads=100",
					"--kube-api-qps=50",
					"--kube-api-burst=100",
				),
				commongenerator.DefaultAttacher.WithExtraArguments(
					"--timeout=1200s",
					"--worker-threads=1000",
					"--kube-api-qps=200",
					"--kube-api-burst=400",
				),
				commongenerator.DefaultResizer.WithExtraArguments(
					"--timeout=240s",
					"-handle-volume-inuse-error=false",
				),
				commongenerator.DefaultSnapshotter.WithExtraArguments(
					"--timeout=600s",
				),
				commongenerator.DefaultLivenessProbe.WithExtraArguments(
					"--probe-timeout=3s",
				),
			},
			Assets: commongenerator.DefaultControllerAssets,
			AssetPatches: commongenerator.DefaultAssetPatches.WithPatches(generator.HyperShiftOnly,
				"controller.yaml", "overlays/azure-disk/patches/controller_add_hypershift_controller.yaml",
			).WithPatches(generator.StandaloneOnly,
				"controller.yaml", "overlays/azure-disk/patches/controller_add_standalone_injector.yaml",
			),
		},

		GuestConfig: &generator.GuestConfig{
			DaemonSetTemplateAssetName: "overlays/azure-disk/patches/node_add_driver.yaml",
			MetricsPorts: []generator.MetricsPort{
				{
					LocalPort:           commongenerator.AzureDiskNodeLoopbackMetricsPortStart,
					ExposedPort:         commongenerator.AzureDiskNodeExposedMetricsPortStart,
					Name:                "driver-m",
					InjectKubeRBACProxy: true,
				},
			},
			LivenessProbePort: 10300,
			// 10303 port is taken by azurefile stuff and hence we must use 10304 here
			NodeRegistrarHealthCheckPort: 10304,
			Sidecars: []generator.SidecarConfig{
				commongenerator.DefaultNodeDriverRegistrar,
				commongenerator.DefaultLivenessProbe.WithExtraArguments(
					"--probe-timeout=3s",
				),
			},
			Assets: commongenerator.DefaultNodeAssets.WithAssets(generator.AllFlavours,
				"overlays/azure-disk/base/csidriver.yaml",
				"overlays/azure-disk/base/storageclass.yaml",
				"overlays/azure-disk/base/volumesnapshotclass.yaml",
			),
		},
	}
}

// GetAzureDiskOperatorConfig returns runtime configuration of the CSI driver operator.
func GetAzureDiskOperatorConfig() *config.OperatorConfig {
	return &config.OperatorConfig{
		CSIDriverName:                   opv1.AzureDiskCSIDriver,
		UserAgent:                       "azure-disk-csi-driver-operator",
		AssetReader:                     assets.ReadFile,
		AssetDir:                        generatedAssetBase,
		CloudConfigNamespace:            openshiftDefaultCloudConfigNamespace,
		OperatorControllerConfigBuilder: GetAzureDiskOperatorControllerConfig,
		Removable:                       false,
	}
}

// GetAzureDiskOperatorControllerConfig returns second half of runtime configuration of the CSI driver operator,
// after a client connection + cluster flavour are established.
func GetAzureDiskOperatorControllerConfig(ctx context.Context, flavour generator.ClusterFlavour, c *clients.Clients) (*config.OperatorControllerConfig, error) {
	cfg := operator.NewDefaultOperatorControllerConfig(flavour, c, "AzureDisk")

	infra, err := c.ConfigClientSet.ConfigV1().Infrastructures().Get(ctx, infrastructureName, metaV1.GetOptions{})
	if err != nil {
		klog.Errorf("unable to fetch infra object from guest cluster with: %v", err)
		return nil, fmt.Errorf("unable to fetch infra object: %v", err)
	}

	insideStackHub := runningInAzureStackHub(infra)

	// We need featuregate accessor made available to the operator pods
	desiredVersion := os.Getenv(operatorImageVersionEnvVarName)
	missingVersion := "0.0.1-snapshot"

	featureGateAccessor := featuregates.NewFeatureGateAccess(
		desiredVersion,
		missingVersion,
		c.ConfigInformers.Config().V1().ClusterVersions(),
		c.ConfigInformers.Config().V1().FeatureGates(),
		c.EventRecorder,
	)
	go featureGateAccessor.Run(ctx)
	go c.ConfigInformers.Start(ctx.Done())

	select {
	case <-featureGateAccessor.InitialFeatureGatesObserved():
		featureGates, _ := featureGateAccessor.CurrentFeatureGates()
		klog.Info("FeatureGates initialized", "knownFeatures", featureGates.KnownFeatures())
	case <-time.After(1 * time.Minute):
		klog.Error(nil, "timed out waiting for FeatureGate detection")
		return nil, fmt.Errorf("timed out waiting for FeatureGate detection")
	}

	// Hooks for control plane start
	cfg.AddDeploymentHookBuilders(c,
		withCABundleDeploymentHook,
	)

	cfg.AddDeploymentHook(withAzureStackHubDeploymentHook(insideStackHub))
	cfg.AddStorageClassHookBuilders(c, getKMSKeyHook)
	cfg.DeploymentWatchedSecretNames = append(cfg.DeploymentWatchedSecretNames, cloudCredSecretName, metricsCertSecretName)

	// Hooks for daemonset or on the node
	cfg.AddDaemonSetHookBuilders(c, withClusterWideProxyDaemonSetHook, withCABundleDaemonSetHook)
	cfg.DaemonSetWatchedSecretNames = append(cfg.DaemonSetWatchedSecretNames, cloudCredSecretName)
	cfg.AddDaemonSetHook(withAzureStackHubDaemonSetHook(insideStackHub))

	if flavour == generator.FlavourHyperShift {
		configMapSyncer, err := syncCloudConfigGuest(c)
		if err != nil {
			return nil, err
		}
		cfg.ExtraControlPlaneControllers = append(cfg.ExtraControlPlaneControllers, configMapSyncer)
	} else {
		standAloneConfigSyncer, err := syncCloudConfigStandAlone(c)
		if err != nil {
			return nil, err
		}
		cfg.ExtraControlPlaneControllers = append(cfg.ExtraControlPlaneControllers, standAloneConfigSyncer)
	}

	// add extra replacement for stuff
	cfg.ExtraReplacementsFunc = func() []string {
		pairs := []string{}
		pairs = append(pairs, []string{"${CLUSTER_CLOUD_CONTROLLER_MANAGER_OPERATOR_IMAGE}", os.Getenv(ccmOperatorImageEnvName)}...)
		pairs = append(pairs, []string{"${ENABLE_AZURE_WORKLOAD_IDENTITY}", "true"}...)
		return pairs
	}

	if insideStackHub {
		cfg.StorageClassHooks = append(cfg.StorageClassHooks, getStackHubStorageClassHook())
		cfg.VolumeSnapshotClassHooks = append(cfg.VolumeSnapshotClassHooks, getVolumeSnapshotHook())
	}
	return cfg, nil
}

// withCABundleDeploymentHook projects custom CA bundle ConfigMap into the CSI driver container
func withCABundleDeploymentHook(c *clients.Clients) (dc.DeploymentHookFunc, []factory.Informer) {
	hook := csidrivercontrollerservicecontroller.WithCABundleDeploymentHook(
		c.ControlPlaneNamespace,
		trustedCAConfigMap,
		c.GetControlPlaneConfigMapInformer(c.ControlPlaneNamespace),
	)
	informers := []factory.Informer{
		c.GetControlPlaneConfigMapInformer(c.ControlPlaneNamespace).Informer(),
	}
	return hook, informers
}

func withAzureStackHubDeploymentHook(runningOnAzureStackHub bool) dc.DeploymentHookFunc {
	hook := func(_ *opv1.OperatorSpec, deployment *appsV1.Deployment) error {
		if runningOnAzureStackHub {
			injectEnvAndMounts(&deployment.Spec.Template.Spec)
		}
		return nil
	}
	return hook
}

// TODO: fix references in the file
func injectEnvAndMounts(spec *coreV1.PodSpec) {
	containers := spec.Containers
	for i := range containers {
		c := &spec.Containers[i]
		if c.Name == "csi-driver" {
			c.Env = append(c.Env, coreV1.EnvVar{
				Name:  configEnvName,
				Value: azureStackCloudConfig,
			})
			c.VolumeMounts = append(c.VolumeMounts, coreV1.VolumeMount{
				Name:      podAzureCfgVolumeName,
				MountPath: azureStackCloudConfig,
				SubPath:   "endpoints",
			})
			break
		}
	}
}

func syncCloudConfigGuest(c *clients.Clients) (factory.Controller, error) {
	// syncs cloud-config from openshif-config namespace to openshift-cluster-csi-drivers namespace
	srcConfigMap := resourcesynccontroller.ResourceLocation{
		Namespace: openshiftDefaultCloudConfigNamespace,
		Name:      configMapName,
	}
	dstConfigMap := resourcesynccontroller.ResourceLocation{
		Namespace: clients.CSIDriverNamespace,
		Name:      localCloudConfigName,
	}
	cloudConfigSyncController := resourcesynccontroller.NewResourceSyncController(
		string(opv1.AzureDiskCSIDriver),
		c.OperatorClient,
		c.KubeInformers,
		c.KubeClient.CoreV1(),
		c.KubeClient.CoreV1(),
		c.EventRecorder)

	err := cloudConfigSyncController.SyncConfigMap(dstConfigMap, srcConfigMap)
	if err != nil {
		return nil, err
	}
	return cloudConfigSyncController, nil
}

// useful in standalone clusters for syncing cloud-provider config from openshift-config
// namespace to openshift-cluster-csi-drivers namespace
// Please note although I am using c.ControlPlaneNamespace, strictly speaking this is not needed
// in hypershift clusters because cloud-config is already synced there for external CCM and stuff.
func syncCloudConfigStandAlone(c *clients.Clients) (factory.Controller, error) {
	// sync config map with additional trust bundle to the operator namespace,
	// so the operator can get it as a ConfigMap volume.
	srcConfigMap := resourcesynccontroller.ResourceLocation{
		Namespace: openshiftDefaultCloudConfigNamespace,
		Name:      configMapName,
	}
	dstConfigMap := resourcesynccontroller.ResourceLocation{
		Namespace: c.ControlPlaneNamespace,
		Name:      localCloudConfigName,
	}
	cloudConfigSyncController := resourcesynccontroller.NewResourceSyncController(
		string(opv1.AzureDiskCSIDriver),
		c.OperatorClient,
		c.KubeInformers,
		c.KubeClient.CoreV1(),
		c.KubeClient.CoreV1(),
		c.EventRecorder)

	err := cloudConfigSyncController.SyncConfigMap(dstConfigMap, srcConfigMap)
	if err != nil {
		return nil, err
	}
	return cloudConfigSyncController, nil
}

func getStackHubStorageClassHook() csistorageclasscontroller.StorageClassHookFunc {
	return func(_ *opv1.OperatorSpec, sc *storagev1.StorageClass) error {
		sc.Parameters["skuname"] = "Premium_LRS"
		return nil
	}
}

func runningInAzureStackHub(infra *opCfgV1.Infrastructure) bool {
	if infra.Status.PlatformStatus != nil &&
		infra.Status.PlatformStatus.Azure != nil &&
		infra.Status.PlatformStatus.Azure.CloudName == opCfgV1.AzureStackCloud {
		return true
	}
	return false
}

// getKMSKeyHook checks for AzureCSIDriverConfigSpec in the ClusterCSIDriver object.
// If it contains DiskEncryptionSet, it sets the corresponding parameter in the SC.
// This allows the admin to specify a customer managed key to be used by default.
func getKMSKeyHook(c *clients.Clients) csistorageclasscontroller.StorageClassHookFunc {
	hook := func(_ *opv1.OperatorSpec, class *storagev1.StorageClass) error {
		ccdLister := c.OperatorInformers.Operator().V1().ClusterCSIDrivers().Lister()
		ccd, err := ccdLister.Get(class.Provisioner)
		if err != nil {
			return err
		}

		driverConfig := ccd.Spec.DriverConfig
		if driverConfig.DriverType != opv1.AzureDriverType || driverConfig.Azure == nil {
			klog.V(4).Infof("No AzureCSIDriverConfigSpec defined for %s", class.Provisioner)
			return nil
		}

		encset := driverConfig.Azure.DiskEncryptionSet
		if encset == nil {
			klog.V(4).Infof("Not setting empty %s parameter in StorageClass %s", diskEncryptionSetID, class.Name)
			return nil
		}

		if class.Parameters == nil {
			class.Parameters = map[string]string{}
		}
		value := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/diskEncryptionSets/%s", encset.SubscriptionID, encset.ResourceGroup, encset.Name)
		klog.V(4).Infof("Setting %s = %s in StorageClass %s", diskEncryptionSetID, value, class.Name)
		class.Parameters[diskEncryptionSetID] = value
		return nil
	}

	return hook
}

func getVolumeSnapshotHook() volume_snapshot_class.VolumeSnapshotClassHookFunc {
	hook := func(_ *opv1.OperatorSpec, vsc *snapshotapi.VolumeSnapshotClass) error {
		if vsc.Parameters == nil {
			vsc.Parameters = map[string]string{}
		}
		vsc.Parameters[incremetalSnapshotKey] = "false"
		return nil
	}
	return hook
}

// withCABundleDaemonSetHook projects custom CA bundle ConfigMap into the CSI driver container
func withCABundleDaemonSetHook(c *clients.Clients) (csidrivernodeservicecontroller.DaemonSetHookFunc, []factory.Informer) {
	hook := csidrivernodeservicecontroller.WithCABundleDaemonSetHook(
		clients.CSIDriverNamespace,
		trustedCAConfigMap,
		c.GetConfigMapInformer(clients.CSIDriverNamespace),
	)
	informers := []factory.Informer{
		c.GetConfigMapInformer(clients.CSIDriverNamespace).Informer(),
	}
	return hook, informers
}

// withClusterWideProxyHook adds the cluster-wide proxy config to the DaemonSet.
func withClusterWideProxyDaemonSetHook(_ *clients.Clients) (csidrivernodeservicecontroller.DaemonSetHookFunc, []factory.Informer) {
	hook := csidrivernodeservicecontroller.WithObservedProxyDaemonSetHook()
	return hook, nil
}

func withAzureStackHubDaemonSetHook(runningOnAzureStackHub bool) csidrivernodeservicecontroller.DaemonSetHookFunc {
	return func(_ *opv1.OperatorSpec, ds *appsV1.DaemonSet) error {
		if runningOnAzureStackHub {
			injectEnvAndMounts(&ds.Spec.Template.Spec)
		}
		return nil
	}
}
