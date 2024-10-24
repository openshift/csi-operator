package openstack_manila

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"time"

	opv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/csi-operator/assets"
	"github.com/openshift/csi-operator/pkg/clients"
	commongenerator "github.com/openshift/csi-operator/pkg/driver/common/generator"
	"github.com/openshift/csi-operator/pkg/driver/common/operator"
	"github.com/openshift/csi-operator/pkg/generator"
	"github.com/openshift/csi-operator/pkg/openstack-manila/secret"
	"github.com/openshift/csi-operator/pkg/openstack-manila/util"
	"github.com/openshift/csi-operator/pkg/operator/config"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivercontrollerservicecontroller"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivernodeservicecontroller"
	dc "github.com/openshift/library-go/pkg/operator/deploymentcontroller"
	"github.com/openshift/library-go/pkg/operator/resourcesynccontroller"
	"k8s.io/klog/v2"
)

const (
	trustedCAConfigMap = "manila-csi-driver-trusted-ca-bundle"

	generatedAssetBase                   = "overlays/openstack-manila/generated"
	resyncInterval                       = 20 * time.Minute
	openshiftDefaultCloudConfigNamespace = "openshift-config"
	metricsCertSecretName                = "manila-csi-driver-controller-metrics-serving-cert"
	nfsImageEnvName                      = "NFS_DRIVER_IMAGE"
)

// GetOpenStackCinderGeneratorConfig returns configuration for generating assets of Manila CSI driver operator.
func GetOpenStackManilaGeneratorConfig() *generator.CSIDriverGeneratorConfig {
	return &generator.CSIDriverGeneratorConfig{
		AssetPrefix:      "manila-csi-driver",
		AssetShortPrefix: "openstack-manila",
		DriverName:       "manila.csi.openstack.org",
		OutputDir:        generatedAssetBase,

		ControllerConfig: &generator.ControlPlaneConfig{
			DeploymentTemplateAssetName: "overlays/openstack-manila/patches/controller_add_driver.yaml",
			LivenessProbePort:           10306,
			MetricsPorts: []generator.MetricsPort{
				{
					LocalPort:           commongenerator.OpenStackManilaLoopbackMetricsPortStart,
					InjectKubeRBACProxy: true,
					ExposedPort:         commongenerator.OpenStackManilaExposedMetricsPortStart,
					Name:                "driver-m",
				},
			},
			SidecarLocalMetricsPortStart:   commongenerator.OpenStackManilaLoopbackMetricsPortStart + 1,
			SidecarExposedMetricsPortStart: commongenerator.OpenStackManilaExposedMetricsPortStart + 1,
			Sidecars: []generator.SidecarConfig{
				commongenerator.DefaultProvisionerWithSnapshots.WithExtraArguments(
					"--timeout=120s",
					"--feature-gates=Topology=true",
				),
				commongenerator.DefaultResizer.WithExtraArguments(),
				commongenerator.DefaultSnapshotter.WithExtraArguments(),
				commongenerator.DefaultLivenessProbe.WithExtraArguments(
					"--probe-timeout=10s",
				),
			},
			Assets:       commongenerator.DefaultControllerAssets,
			AssetPatches: commongenerator.DefaultAssetPatches,
		},

		GuestConfig: &generator.GuestConfig{
			DaemonSetTemplateAssetName:   "overlays/openstack-manila/patches/node_add_driver.yaml",
			LivenessProbePort:            10305,
			NodeRegistrarHealthCheckPort: 10307,
			Sidecars: []generator.SidecarConfig{
				commongenerator.DefaultLivenessProbe.WithExtraArguments(
					"--probe-timeout=10s",
				),
				commongenerator.DefaultNodeDriverRegistrar,
			},
			Assets: commongenerator.DefaultNodeAssets.WithAssets(generator.AllFlavours,
				"overlays/openstack-manila/base/csidriver.yaml",
				"overlays/openstack-manila/base/volumesnapshotclass.yaml",
				"overlays/openstack-manila/base/node_nfs.yaml",
			),
		},
		StandaloneOnly: true,
	}
}

// GetOpenStackManilaOperatorConfig returns runtime configuration of the CSI driver operator.
func GetOpenStackManilaOperatorConfig() *config.OperatorConfig {
	return &config.OperatorConfig{
		CSIDriverName:                   opv1.ManilaCSIDriver,
		UserAgent:                       "openstack-manila-csi-driver-operator",
		AssetReader:                     assets.ReadFile,
		AssetDir:                        generatedAssetBase,
		CloudConfigNamespace:            openshiftDefaultCloudConfigNamespace,
		OperatorControllerConfigBuilder: GetOpenStackManilaOperatorControllerConfig,
		Removable:                       false,
	}
}

// GetOpenStackManilaOperatorControllerConfig returns second half of runtime configuration of the CSI driver operator,
// after a client connection + cluster flavour are established.
func GetOpenStackManilaOperatorControllerConfig(ctx context.Context, flavour generator.ClusterFlavour, c *clients.Clients) (*config.OperatorControllerConfig, error) {
	if flavour != generator.FlavourStandalone {
		klog.Error(nil, "Flavour HyperShift is not supported!")
		return nil, fmt.Errorf("flavour HyperShift is not supported")
	}
	cfg := operator.NewDefaultOperatorControllerConfig(flavour, c, "OpenStackManila")

	go c.ConfigInformers.Start(ctx.Done())

	// Hooks to run on all clusters
	cfg.AddDeploymentHookBuilders(c, withCABundleDeploymentHook)
	cfg.AddDaemonSetHookBuilders(c, withCABundleDaemonSetHook, withClusterWideProxyDaemonSetHook)

	cfg.DeploymentWatchedSecretNames = append(cfg.DeploymentWatchedSecretNames, metricsCertSecretName)

	// Generate NFS driver asset with image and namespace populated
	dsBytes, err := assetWithNFSDriver("overlays/openstack-manila/generated/standalone/node_nfs.yaml")
	if err != nil {
		return nil, err
	}
	nfsCSIDriverController := csidrivernodeservicecontroller.NewCSIDriverNodeServiceController(
		"NFSDriverNodeServiceController",
		dsBytes,
		c.EventRecorder,
		c.OperatorClient,
		c.KubeClient,
		c.ControlPlaneKubeInformers.InformersFor(c.ControlPlaneNamespace).Apps().V1().DaemonSets(),
		[]factory.Informer{c.GetControlPlaneConfigMapInformer(c.ControlPlaneNamespace).Informer()},
	)

	configMapSyncer, err := createConfigMapSyncer(c)
	if err != nil {
		return nil, err
	}

	secretSyncer, err := createSecretSyncer(c)
	if err != nil {
		return nil, err
	}
	cfg.ExtraControlPlaneControllers = append(cfg.ExtraControlPlaneControllers, configMapSyncer, secretSyncer, nfsCSIDriverController)
	return cfg, nil
}

// withClusterWideProxyHook adds the cluster-wide proxy config to the DaemonSet.
func withClusterWideProxyDaemonSetHook(_ *clients.Clients) (csidrivernodeservicecontroller.DaemonSetHookFunc, []factory.Informer) {
	hook := csidrivernodeservicecontroller.WithObservedProxyDaemonSetHook()
	return hook, nil
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

func createSecretSyncer(c *clients.Clients) (factory.Controller, error) {
	secretSyncController := secret.NewSecretSyncController(
		c.OperatorClient,
		c.KubeClient,
		c.KubeInformers,
		resyncInterval,
		c.EventRecorder)

	return secretSyncController, nil
}

func createConfigMapSyncer(c *clients.Clients) (factory.Controller, error) {
	// sync config map with OpenStack CA certificate to the operand namespace,
	// so the driver can get it as a ConfigMap volume.
	srcConfigMap := resourcesynccontroller.ResourceLocation{
		Namespace: util.CloudConfigNamespace,
		Name:      util.CloudConfigName,
	}
	dstConfigMap := resourcesynccontroller.ResourceLocation{
		Namespace: util.OperatorNamespace,
		Name:      util.CloudConfigName,
	}
	certController := resourcesynccontroller.NewResourceSyncController(
		c.OperatorClient,
		c.KubeInformers,
		c.KubeClient.CoreV1(),
		c.KubeClient.CoreV1(),
		c.EventRecorder)
	if err := certController.SyncConfigMap(dstConfigMap, srcConfigMap); err != nil {
		return nil, err
	}
	return certController, nil
}

// CSIDriverController can replace only a single driver in driver manifests.
// Manila needs to replace two of them: Manila driver and NFS driver image.
// Let the Manila image be replaced by CSIDriverController and NFS in this
// custom asset loading func.
func assetWithNFSDriver(file string) ([]byte, error) {
	asset, err := assets.ReadFile(file)
	if err != nil {
		return nil, err
	}
	nfsImage := os.Getenv(nfsImageEnvName)
	if nfsImage == "" {
		return asset, nil
	}

	asset = bytes.ReplaceAll(asset, []byte("${NFS_DRIVER_IMAGE}"), []byte(nfsImage))
	return bytes.ReplaceAll(asset, []byte("${NAMESPACE}"), []byte(util.OperatorNamespace)), nil
}
