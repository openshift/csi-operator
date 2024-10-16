package azure_file

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/openshift/csi-operator/assets"
	"github.com/openshift/csi-operator/pkg/clients"
	"github.com/openshift/csi-operator/pkg/driver/common/operator"
	"github.com/openshift/csi-operator/pkg/generator"
	"github.com/openshift/csi-operator/pkg/operator/config"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivercontrollerservicecontroller"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivernodeservicecontroller"
	"github.com/openshift/library-go/pkg/operator/resourcesynccontroller"

	dc "github.com/openshift/library-go/pkg/operator/deploymentcontroller"

	opv1 "github.com/openshift/api/operator/v1"
	commongenerator "github.com/openshift/csi-operator/pkg/driver/common/generator"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"k8s.io/klog/v2"
)

const (
	cloudCredSecretName   = "azure-file-credentials"
	metricsCertSecretName = "azure-file-csi-driver-controller-metrics-serving-cert"
	infrastructureName    = "cluster"
	cloudConfigName       = "kube-cloud-config"
	caBundleKey           = "ca-bundle.pem"
	trustedCAConfigMap    = "azure-file-csi-driver-trusted-ca-bundle"

	configMapName                        = "cloud-provider-config"
	openshiftDefaultCloudConfigNamespace = "openshift-config"

	generatedAssetBase = "overlays/azure-file/generated"

	operatorImageVersionEnvVarName = "OPERATOR_IMAGE_VERSION"
	ccmOperatorImageEnvName        = "CLUSTER_CLOUD_CONTROLLER_MANAGER_OPERATOR_IMAGE"

	// name of local cloud-config copied to openshift-cluster-csi-driver namespace.
	localCloudConfigName = "azure-cloud-config"
)

func GetAzureFileGeneratorConfig() *generator.CSIDriverGeneratorConfig {
	return &generator.CSIDriverGeneratorConfig{
		AssetPrefix:      "azure-file-csi-driver",
		AssetShortPrefix: "azure-file",
		DriverName:       "file.csi.azure.com",
		OutputDir:        generatedAssetBase,

		ControllerConfig: &generator.ControlPlaneConfig{
			DeploymentTemplateAssetName: "overlays/azure-file/patches/controller_add_driver.yaml",
			LivenessProbePort:           10303,
			MetricsPorts: []generator.MetricsPort{
				{
					LocalPort:           commongenerator.AzureFileLoopbackMetricsPortStart,
					InjectKubeRBACProxy: true,
					ExposedPort:         commongenerator.AzureFileExposedMetricsPortStart,
					Name:                "driver-m",
				},
			},
			SidecarLocalMetricsPortStart:   commongenerator.AzureFileLoopbackMetricsPortStart + 1,
			SidecarExposedMetricsPortStart: commongenerator.AzureFileExposedMetricsPortStart + 1,
			Sidecars: []generator.SidecarConfig{
				commongenerator.DefaultProvisionerWithSnapshots.WithExtraArguments(
					"--extra-create-metadata=true",
					"--timeout=300s",
					"--kube-api-qps=50",
					"--kube-api-burst=100",
				),
				commongenerator.DefaultAttacher.WithExtraArguments(
					"--timeout=120s",
				),
				commongenerator.DefaultResizer.WithExtraArguments(
					"--timeout=120s",
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
				"controller.yaml", "overlays/azure-file/patches/controller_add_hypershift_controller.yaml",
				"controller.yaml", "overlays/azure-file/patches/controller_add_driver_kubeconfig_hypershift.yaml.patch",
			).WithPatches(generator.StandaloneOnly,
				"controller.yaml", "overlays/azure-file/patches/controller_add_standalone_injector.yaml",
			),
		},

		GuestConfig: &generator.GuestConfig{
			DaemonSetTemplateAssetName: "overlays/azure-file/patches/node_add_driver.yaml",
			LivenessProbePort:          10302,
			// 10304 port is taken by azure disk
			NodeRegistrarHealthCheckPort: 10305,
			Sidecars: []generator.SidecarConfig{
				commongenerator.DefaultNodeDriverRegistrar,
				commongenerator.DefaultLivenessProbe.WithExtraArguments(
					"--probe-timeout=3s",
				),
			},
			Assets: commongenerator.DefaultNodeAssets.WithAssets(generator.AllFlavours,
				"overlays/azure-file/base/csidriver.yaml",
				"overlays/azure-file/base/storageclass.yaml",
				"overlays/azure-file/base/csi-driver-cluster-role.yaml",
				"overlays/azure-file/base/csi-driver-cluster-role-binding.yaml",
				"overlays/azure-file/base/volumesnapshotclass.yaml",
			),
		},
	}
}

// GetAzureFileOperatorConfig returns runtime configuration of the CSI driver operator.
func GetAzureFileOperatorConfig() *config.OperatorConfig {
	return &config.OperatorConfig{
		CSIDriverName:                   opv1.AzureFileCSIDriver,
		UserAgent:                       "azure-file-csi-driver-operator",
		AssetReader:                     assets.ReadFile,
		AssetDir:                        generatedAssetBase,
		CloudConfigNamespace:            openshiftDefaultCloudConfigNamespace,
		OperatorControllerConfigBuilder: GetAzureFileOperatorControllerConfig,
		Removable:                       false,
	}
}

// GetAzureFileOperatorControllerConfig returns second half of runtime configuration of the CSI driver operator,
// after a client connection + cluster flavour are established.
func GetAzureFileOperatorControllerConfig(ctx context.Context, flavour generator.ClusterFlavour, c *clients.Clients) (*config.OperatorControllerConfig, error) {
	cfg := operator.NewDefaultOperatorControllerConfig(flavour, c, "AzureFile")

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
	cfg.AddDeploymentHookBuilders(c, withCABundleDeploymentHook)

	cfg.DeploymentWatchedSecretNames = append(cfg.DeploymentWatchedSecretNames, cloudCredSecretName, metricsCertSecretName)

	// Hooks for daemonset or on the node
	cfg.AddDaemonSetHookBuilders(c, withClusterWideProxyDaemonSetHook, withCABundleDaemonSetHook)
	cfg.DaemonSetWatchedSecretNames = append(cfg.DaemonSetWatchedSecretNames, cloudCredSecretName)

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
		string(opv1.AzureFileCSIDriver),
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
		string(opv1.AzureFileCSIDriver),
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
