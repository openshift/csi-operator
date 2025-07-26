package openstack_manila

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2/openstack/sharedfilesystems/v2/sharetypes"
	opv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/csi-operator/assets"
	"github.com/openshift/csi-operator/pkg/clients"
	commongenerator "github.com/openshift/csi-operator/pkg/driver/common/generator"
	"github.com/openshift/csi-operator/pkg/driver/common/operator"
	"github.com/openshift/csi-operator/pkg/generator"
	"github.com/openshift/csi-operator/pkg/openstack-manila/client"
	"github.com/openshift/csi-operator/pkg/openstack-manila/secret"
	"github.com/openshift/csi-operator/pkg/openstack-manila/util"
	"github.com/openshift/csi-operator/pkg/operator/config"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivercontrollerservicecontroller"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivernodeservicecontroller"
	dc "github.com/openshift/library-go/pkg/operator/deploymentcontroller"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"github.com/openshift/library-go/pkg/operator/resource/resourceread"
	"github.com/openshift/library-go/pkg/operator/resourcesynccontroller"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8serrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// DUMMY CHANGE

const (
	trustedCAConfigMap = "manila-csi-driver-trusted-ca-bundle"

	generatedAssetBase                   = "overlays/openstack-manila/generated"
	resyncInterval                       = 20 * time.Minute
	openshiftDefaultCloudConfigNamespace = "openshift-config"
	metricsCertSecretName                = "manila-csi-driver-controller-metrics-serving-cert"
	nfsImageEnvName                      = "NFS_DRIVER_IMAGE"
)

// GetOpenStackManilaGeneratorConfig returns configuration for generating assets of Manila CSI driver operator.
func GetOpenStackManilaGeneratorConfig() *generator.CSIDriverGeneratorConfig {
	return &generator.CSIDriverGeneratorConfig{
		AssetPrefix:      "manila-csi-driver",
		AssetShortPrefix: "openstack-manila",
		DriverName:       "manila.csi.openstack.org",
		OutputDir:        generatedAssetBase,

		ControllerConfig: &generator.ControlPlaneConfig{
			DeploymentTemplateAssetName: "overlays/openstack-manila/patches/controller_add_driver.yaml",
			LivenessProbePort:           10306,
			// TODO(stephenfin): Expose metrics port once the driver supports it
			SidecarLocalMetricsPortStart:   commongenerator.OpenStackManilaLoopbackMetricsPortStart + 1,
			SidecarExposedMetricsPortStart: commongenerator.OpenStackManilaExposedMetricsPortStart + 1,
			Sidecars: []generator.SidecarConfig{
				commongenerator.DefaultProvisionerWithSnapshots.WithExtraArguments(
					"--timeout=120s",
					"--feature-gates=Topology=true",
				),
				commongenerator.DefaultResizer.WithExtraArguments(
					"--timeout=240s",
					"--handle-volume-inuse-error=false",
				),
				commongenerator.DefaultSnapshotter.WithExtraArguments(),
				commongenerator.DefaultPodNetworkLivenessProbe.WithExtraArguments(
					"--probe-timeout=10s",
				),
			},
			Assets: commongenerator.DefaultControllerAssets,
			AssetPatches: commongenerator.DefaultAssetPatches.WithPatches(generator.HyperShiftOnly,
				"controller.yaml", "overlays/openstack-manila/patches/controller_add_hypershift_volumes.yaml",
				"controller.yaml", "overlays/openstack-manila/patches/controller_rename_config_map.yaml",
			).WithPatches(generator.AllFlavours,
				"service.yaml", "overlays/openstack-manila/patches/modify_service_selector.yaml",
				"controller_pdb.yaml", "overlays/openstack-manila/patches/modify_pdb.yaml",
				"controller.yaml", "overlays/openstack-manila/patches/modify_anti_affinity_selector.yaml",
			),
		},

		GuestConfig: &generator.GuestConfig{
			DaemonSetTemplateAssetName:   "overlays/openstack-manila/patches/node_add_driver.yaml",
			LivenessProbePort:            10305,
			NodeRegistrarHealthCheckPort: 10307,
			Sidecars: []generator.SidecarConfig{
				commongenerator.DefaultHostNetworkLivenessProbe.WithExtraArguments(
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
	}
}

// GetOpenStackManilaOperatorConfig returns runtime configuration of the CSI driver operator.
func GetOpenStackManilaOperatorConfig() *config.OperatorConfig {
	// TODO: To ensure upgrades work, let's keep using the same guest namespace,
	// named openshift-manila-csi-driver, manila has been using in past releases. Later, we
	// can migrate manila components to the namespace all other operators use and stop relying on
	// openshift-manila-csi-driver.
	return &config.OperatorConfig{
		CSIDriverName:                   opv1.ManilaCSIDriver,
		GuestNamespace:                  "openshift-manila-csi-driver",
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
	cfg := operator.NewDefaultOperatorControllerConfig(flavour, c, "OpenStackManila")

	go c.ConfigInformers.Start(ctx.Done())

	// Hooks to run on all clusters
	cfg.AddDeploymentHookBuilders(c, withCABundleDeploymentHook)
	cfg.AddDaemonSetHookBuilders(c, withCABundleDaemonSetHook, withClusterWideProxyDaemonSetHook)

	cfg.DeploymentWatchedSecretNames = append(cfg.DeploymentWatchedSecretNames, metricsCertSecretName)

	// Generate NFS driver asset with image and namespace populated
	dsBytes, err := assetWithNFSDriver("overlays/openstack-manila/generated/standalone/node_nfs.yaml", c)
	if err != nil {
		return nil, err
	}
	nfsCSIDriverController := csidrivernodeservicecontroller.NewCSIDriverNodeServiceController(
		"NFSDriverNodeServiceController",
		dsBytes,
		c.EventRecorder,
		c.OperatorClient,
		c.KubeClient,
		c.KubeInformers.InformersFor(c.GuestNamespace).Apps().V1().DaemonSets(),
		[]factory.Informer{c.GetConfigMapInformer(c.GuestNamespace).Informer()},
	)

	configMapSyncer, err := createConfigMapSyncer(c)
	if err != nil {
		return nil, err
	}

	cfg.Precondition = func() (bool, error) {
		openstackClient, err := client.NewOpenStackClient(util.CloudConfigFilename)
		if err != nil {
			return false, err
		}
		shareTypes, err := openstackClient.GetShareTypes()
		if err != nil {
			return false, err
		}

		if len(shareTypes) == 0 {
			klog.V(4).Infof("Manila does not provide any share types")
			return false, errors.New("manila does not provide any share types")
		}

		err = syncCSIDriver(ctx, c.KubeClient, c.EventRecorder)
		if err != nil {
			return true, err
		}

		err = syncStorageClasses(ctx, shareTypes, c.KubeClient, c.EventRecorder, c.GuestNamespace)
		if err != nil {
			return true, err
		}

		return true, nil
	}

	cfg.PreconditionInformers = []factory.Informer{c.GetCSIDriverInformer().Informer(), c.GetStorageClassInformer().Informer()}
	secretSyncer, err := createSecretSyncer(c)
	if err != nil {
		return nil, err
	}
	cfg.ExtraControlPlaneControllers = append(cfg.ExtraControlPlaneControllers, configMapSyncer, secretSyncer, nfsCSIDriverController)

	cfg.ExtraReplacementsFunc = func() []string {
		pairs := []string{}
		nfsImage := os.Getenv(nfsImageEnvName)
		if nfsImage != "" {
			pairs = append(pairs, []string{"${NFS_DRIVER_IMAGE}", nfsImage}...)
		}
		return pairs
	}
	return cfg, nil
}

// withClusterWideProxyHook adds the cluster-wide proxy config to the DaemonSet.
func withClusterWideProxyDaemonSetHook(_ *clients.Clients) (csidrivernodeservicecontroller.DaemonSetHookFunc, []factory.Informer) {
	hook := csidrivernodeservicecontroller.WithObservedProxyDaemonSetHook()
	return hook, nil
}

func getFsGroupPolicy() storagev1.FSGroupPolicy {

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

func syncCSIDriver(ctx context.Context, kubeClient kubernetes.Interface, recorder events.Recorder) error {
	klog.V(4).Infof("Starting CSI driver config refresh")
	defer klog.V(4).Infof("CSI driver config refresh finished")

	var errs []error

	stream, e := assets.ReadFile("overlays/openstack-manila/generated/standalone/csidriver.yaml")
	if e != nil {
		panic("Error loading the CSIDriver resource")
	}

	cr := resourceread.ReadCSIDriverV1OrDie(stream)
	f := getFsGroupPolicy()
	cr.Spec.FSGroupPolicy = &f

	_, _, err := resourceapply.ApplyCSIDriver(ctx, kubeClient.StorageV1(), recorder, cr)

	if err != nil {
		errs = append(errs, err)
	}

	return k8serrors.NewAggregate(errs)
}

func syncStorageClasses(ctx context.Context, shareTypes []sharetypes.ShareType, kubeClient kubernetes.Interface, recorder events.Recorder, guestNamespace string) error {
	var errs []error
	for _, shareType := range shareTypes {
		klog.V(4).Infof("Syncing storage class for shareType type %s", shareType.Name)
		sc := generateStorageClass(shareType, guestNamespace)
		_, _, err := resourceapply.ApplyStorageClass(ctx, kubeClient.StorageV1(), recorder, sc)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return k8serrors.NewAggregate(errs)
}

func generateStorageClass(shareType sharetypes.ShareType, guestNamespace string) *storagev1.StorageClass {
	/* As per RFC 1123 the storage class name must consist of lower case alphanumeric character,  '-' or '.'
	   and must start and end with an alphanumeric character.
	*/
	storageClassName := util.StorageClassNamePrefix + strings.ToLower(strings.Replace(shareType.Name, "_", "-", -1))
	delete := corev1.PersistentVolumeReclaimDelete
	immediate := storagev1.VolumeBindingImmediate
	allowVolumeExpansion := true
	sc := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: storageClassName,
		},
		Provisioner: "manila.csi.openstack.org",
		Parameters: map[string]string{
			"type": shareType.Name,
			"csi.storage.k8s.io/provisioner-secret-name":            util.ManilaSecretName,
			"csi.storage.k8s.io/provisioner-secret-namespace":       guestNamespace,
			"csi.storage.k8s.io/node-stage-secret-name":             util.ManilaSecretName,
			"csi.storage.k8s.io/node-stage-secret-namespace":        guestNamespace,
			"csi.storage.k8s.io/node-publish-secret-name":           util.ManilaSecretName,
			"csi.storage.k8s.io/node-publish-secret-namespace":      guestNamespace,
			"csi.storage.k8s.io/controller-expand-secret-name":      util.ManilaSecretName,
			"csi.storage.k8s.io/controller-expand-secret-namespace": guestNamespace,
		},
		ReclaimPolicy:        &delete,
		VolumeBindingMode:    &immediate,
		AllowVolumeExpansion: &allowVolumeExpansion,
	}
	return sc
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
		c.GuestNamespace,
		trustedCAConfigMap,
		c.GetConfigMapInformer(c.GuestNamespace),
	)
	informers := []factory.Informer{
		c.GetConfigMapInformer(c.GuestNamespace).Informer(),
	}

	return hook, informers
}

func createSecretSyncer(c *clients.Clients) (factory.Controller, error) {
	secretSyncController := secret.NewSecretSyncController(
		c.OperatorClient,
		c.KubeClient,
		c.ControlPlaneKubeInformers,
		c.ControlPlaneNamespace,
		c.GuestNamespace,
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
		Namespace: c.GuestNamespace,
		Name:      util.CloudConfigName,
	}
	certController := resourcesynccontroller.NewResourceSyncController(
		string(opv1.ManilaCSIDriver),
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
func assetWithNFSDriver(file string, c *clients.Clients) ([]byte, error) {
	asset, err := assets.ReadFile(file)
	if err != nil {
		return nil, err
	}
	nfsImage := os.Getenv(nfsImageEnvName)
	if nfsImage == "" {
		return asset, nil
	}

	asset = bytes.ReplaceAll(asset, []byte("${NFS_DRIVER_IMAGE}"), []byte(nfsImage))
	return bytes.ReplaceAll(asset, []byte("${NODE_NAMESPACE}"), []byte(c.GuestNamespace)), nil
}
