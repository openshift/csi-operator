package operator

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apiextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	kubeclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"

	opv1 "github.com/openshift/api/operator/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	configlisters "github.com/openshift/client-go/config/listers/config/v1"
	applyopv1 "github.com/openshift/client-go/operator/applyconfigurations/operator/v1"
	opclient "github.com/openshift/client-go/operator/clientset/versioned"
	opinformers "github.com/openshift/client-go/operator/informers/externalversions"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"github.com/openshift/library-go/pkg/operator/csi/csicontrollerset"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivercontrollerservicecontroller"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivernodeservicecontroller"
	dc "github.com/openshift/library-go/pkg/operator/deploymentcontroller"
	goc "github.com/openshift/library-go/pkg/operator/genericoperatorclient"
	"github.com/openshift/library-go/pkg/operator/v1helpers"

	"github.com/openshift/gcp-pd-csi-driver-operator/assets"
)

const (
	// Operand and operator run in the same namespace
	defaultNamespace   = "openshift-cluster-csi-drivers"
	operatorName       = "gcp-pd-csi-driver-operator"
	operandName        = "gcp-pd-csi-driver"
	trustedCAConfigMap = "gcp-pd-csi-driver-trusted-ca-bundle"
	resync             = 20 * time.Minute

	cloudCredSecretName   = "gcp-pd-cloud-credentials"
	metricsCertSecretName = "gcp-pd-csi-driver-controller-metrics-serving-cert"

	diskEncryptionKMSKey  = "disk-encryption-kms-key"
	defaultKMSKeyLocation = "global"

	// globalInfrastructureName is the default name of the Infrastructure object
	globalInfrastructureName = "cluster"

	// ocpDefaultLabelFmt is the format string for the default label
	// added to the OpenShift created GCP resources.
	ocpDefaultLabelFmt = "kubernetes-io-cluster-%s=owned"

	operatorImageVersionEnvVarName = "OPERATOR_IMAGE_VERSION"
)

func RunOperator(ctx context.Context, controllerConfig *controllercmd.ControllerContext) error {
	// Create core clientset and informers
	kubeClient := kubeclient.NewForConfigOrDie(rest.AddUserAgent(controllerConfig.KubeConfig, operatorName))
	kubeInformersForNamespaces := v1helpers.NewKubeInformersForNamespaces(kubeClient, defaultNamespace, "")
	secretInformer := kubeInformersForNamespaces.InformersFor(defaultNamespace).Core().V1().Secrets()
	configMapInformer := kubeInformersForNamespaces.InformersFor(defaultNamespace).Core().V1().ConfigMaps()
	nodeInformer := kubeInformersForNamespaces.InformersFor("").Core().V1().Nodes()

	// Create config clientset and informer. This is used to get the cluster ID
	configClient := configclient.NewForConfigOrDie(rest.AddUserAgent(controllerConfig.KubeConfig, operatorName))
	configInformers := configinformers.NewSharedInformerFactory(configClient, resync)
	infraInformer := configInformers.Config().V1().Infrastructures()

	apiExtClient, err := apiextclient.NewForConfig(rest.AddUserAgent(controllerConfig.KubeConfig, operatorName))
	if err != nil {
		return err
	}

	// We need featuregate accessor made available to the operator pods
	desiredVersion := os.Getenv(operatorImageVersionEnvVarName)
	missingVersion := "0.0.1-snapshot"

	featureGateAccessor := featuregates.NewFeatureGateAccess(
		desiredVersion,
		missingVersion,
		configInformers.Config().V1().ClusterVersions(),
		configInformers.Config().V1().FeatureGates(),
		controllerConfig.EventRecorder,
	)
	go featureGateAccessor.Run(ctx)
	go configInformers.Start(ctx.Done())

	var featureGates featuregates.FeatureGate

	select {
	case <-featureGateAccessor.InitialFeatureGatesObserved():
		featureGates, _ = featureGateAccessor.CurrentFeatureGates()
		klog.Info("FeatureGates initialized", "knownFeatures", featureGates.KnownFeatures())
	case <-time.After(1 * time.Minute):
		klog.Error(nil, "timed out waiting for FeatureGate detection")
		return fmt.Errorf("timed out waiting for FeatureGate detection")
	}

	// Create GenericOperatorclient. This is used by the library-go controllers created down below
	gvr := opv1.SchemeGroupVersion.WithResource("clustercsidrivers")
	gvk := opv1.SchemeGroupVersion.WithKind("ClusterCSIDriver")
	operatorClient, dynamicInformers, err := goc.NewClusterScopedOperatorClientWithConfigName(
		clock.RealClock{},
		controllerConfig.KubeConfig,
		gvr,
		gvk,
		string(opv1.GCPPDCSIDriver),
		extractOperatorSpec,
		extractOperatorStatus,
	)
	if err != nil {
		return err
	}

	dynamicClient, err := dynamic.NewForConfig(controllerConfig.KubeConfig)
	if err != nil {
		return err
	}

	// operator.openshift.io client, used for ClusterCSIDriver
	operatorClientSet, err := opclient.NewForConfig(controllerConfig.KubeConfig)
	if err != nil {
		return err
	}
	operatorInformers := opinformers.NewSharedInformerFactory(operatorClientSet, resync)

	csiControllerSet := csicontrollerset.NewCSIControllerSet(
		operatorClient,
		controllerConfig.EventRecorder,
	).WithLogLevelController().WithManagementStateController(
		operandName,
		false,
	).WithStaticResourcesController(
		"GCPPDDriverStaticResourcesController",
		kubeClient,
		dynamicClient,
		kubeInformersForNamespaces,
		assets.ReadFile,
		[]string{
			"csidriver.yaml",
			"controller_sa.yaml",
			"controller_pdb.yaml",
			"node_sa.yaml",
			"service.yaml",
			"cabundle_cm.yaml",
			"rbac/main_attacher_binding.yaml",
			"rbac/privileged_role.yaml",
			"rbac/controller_privileged_binding.yaml",
			"rbac/node_privileged_binding.yaml",
			"rbac/main_provisioner_binding.yaml",
			"rbac/volumesnapshot_reader_provisioner_binding.yaml",
			"rbac/volumeattributesclass_reader_provisioner_binding.yaml",
			"rbac/volumeattributesclass_reader_resizer_binding.yaml",
			"rbac/main_resizer_binding.yaml",
			"rbac/storageclass_reader_resizer_binding.yaml",
			"rbac/main_snapshotter_binding.yaml",
			"rbac/kube_rbac_proxy_role.yaml",
			"rbac/kube_rbac_proxy_binding.yaml",
			"rbac/prometheus_role.yaml",
			"rbac/prometheus_rolebinding.yaml",
			"rbac/lease_leader_election_role.yaml",
			"rbac/lease_leader_election_rolebinding.yaml",
		},
	).WithConditionalStaticResourcesController(
		"GCPPDDriverConditionalStaticResourcesController",
		kubeClient,
		dynamicClient,
		kubeInformersForNamespaces,
		assets.ReadFile,
		[]string{
			"volumesnapshotclass.yaml",
			"volumesnapshotclass_images.yaml",
		},
		// Only install when CRD exists.
		func() bool {
			name := "volumesnapshotclasses.snapshot.storage.k8s.io"
			_, err := apiExtClient.ApiextensionsV1().CustomResourceDefinitions().Get(context.TODO(), name, metav1.GetOptions{})
			return err == nil
		},
		// Don't ever remove.
		func() bool {
			return false
		},
	).WithCSIConfigObserverController(
		"GCPPDDriverCSIConfigObserverController",
		configInformers,
	).WithCSIDriverControllerService(
		"GCPPDDriverControllerServiceController",
		assets.ReadFile,
		"controller.yaml",
		kubeClient,
		kubeInformersForNamespaces.InformersFor(defaultNamespace),
		configInformers,
		[]factory.Informer{
			nodeInformer.Informer(),
			infraInformer.Informer(),
			secretInformer.Informer(),
			configMapInformer.Informer(),
		},
		csidrivercontrollerservicecontroller.WithObservedProxyDeploymentHook(),
		csidrivercontrollerservicecontroller.WithCABundleDeploymentHook(
			defaultNamespace,
			trustedCAConfigMap,
			configMapInformer,
		),
		csidrivercontrollerservicecontroller.WithSecretHashAnnotationHook(
			defaultNamespace,
			cloudCredSecretName,
			secretInformer,
		),
		csidrivercontrollerservicecontroller.WithSecretHashAnnotationHook(
			defaultNamespace,
			metricsCertSecretName,
			secretInformer,
		),
		csidrivercontrollerservicecontroller.WithReplicasHook(configInformers),
		withCustomLabels(infraInformer.Lister()),
		withCustomResourceTags(infraInformer.Lister()),
	).WithCSIDriverNodeService(
		"GCPPDDriverNodeServiceController",
		assets.ReadFile,
		"node.yaml",
		kubeClient,
		kubeInformersForNamespaces.InformersFor(defaultNamespace),
		[]factory.Informer{configMapInformer.Informer()},
		csidrivernodeservicecontroller.WithObservedProxyDaemonSetHook(),
		csidrivernodeservicecontroller.WithCABundleDaemonSetHook(
			defaultNamespace,
			trustedCAConfigMap,
			configMapInformer,
		),
	).WithServiceMonitorController(
		"GCPPDDriverServiceMonitorController",
		dynamicClient,
		assets.ReadFile,
		"servicemonitor.yaml",
	).WithStorageClassController(
		"GCPPDDriverStorageClassController",
		assets.ReadFile,
		[]string{
			"storageclass.yaml",
			"storageclass_ssd.yaml",
		},
		kubeClient,
		kubeInformersForNamespaces.InformersFor(""),
		operatorInformers,
		getKMSKeyHook(operatorInformers.Operator().V1().ClusterCSIDrivers().Lister()),
	)

	if err != nil {
		return err
	}

	klog.Info("Starting the informers")
	go kubeInformersForNamespaces.Start(ctx.Done())
	go dynamicInformers.Start(ctx.Done())
	go configInformers.Start(ctx.Done())
	go operatorInformers.Start(ctx.Done())

	klog.Info("Starting controllerset")
	go csiControllerSet.Run(ctx, 1)

	<-ctx.Done()

	return nil
}

// withCustomLabels adds labels from Infrastructure.Status.PlatformStatus.GCP.ResourceLabels to the
// driver command line as --extra-labels=<key1>=<value1>,<key2>=<value2>,...
func withCustomLabels(infraLister configlisters.InfrastructureLister) dc.DeploymentHookFunc {
	return func(spec *opv1.OperatorSpec, deployment *appsv1.Deployment) error {
		infra, err := infraLister.Get(globalInfrastructureName)
		if err != nil {
			return fmt.Errorf("custom labels: failed to fetch global Infrastructure object: %w", err)
		}

		var labels []string
		if infra.Status.PlatformStatus != nil &&
			infra.Status.PlatformStatus.GCP != nil &&
			infra.Status.PlatformStatus.GCP.ResourceLabels != nil {
			labels = make([]string, len(infra.Status.PlatformStatus.GCP.ResourceLabels))
			for i, label := range infra.Status.PlatformStatus.GCP.ResourceLabels {
				labels[i] = fmt.Sprintf("%s=%s", label.Key, label.Value)
			}
		}

		labels = append(labels, fmt.Sprintf(ocpDefaultLabelFmt, infra.Status.InfrastructureName))
		labelsStr := strings.Join(labels, ",")
		labelsArg := fmt.Sprintf("--extra-labels=%s", labelsStr)
		klog.V(5).Infof("withCustomLabels: adding extra-labels arg to driver with value %s", labelsStr)

		for i := range deployment.Spec.Template.Spec.Containers {
			container := &deployment.Spec.Template.Spec.Containers[i]
			if container.Name != "csi-driver" {
				continue
			}
			container.Args = append(container.Args, labelsArg)
		}
		return nil
	}
}

// withCustomResourceTags adds resource tags from infrastructure.status.platformStatus.gcp.resourceTags to the
// driver command line as --resource-tags=<parent_id>/<tagKey_shortname>/<tagValue_shortname>,...
func withCustomResourceTags(infraLister configlisters.InfrastructureLister) dc.DeploymentHookFunc {
	return func(spec *opv1.OperatorSpec, deployment *appsv1.Deployment) error {
		infra, err := infraLister.Get(globalInfrastructureName)
		if err != nil {
			return fmt.Errorf("withCustomResourceTags: failed to fetch global Infrastructure object: %w", err)
		}

		var tags []string
		if infra.Status.PlatformStatus != nil &&
			infra.Status.PlatformStatus.GCP != nil &&
			infra.Status.PlatformStatus.GCP.ResourceTags != nil {
			tags = make([]string, len(infra.Status.PlatformStatus.GCP.ResourceTags))
			for i, tag := range infra.Status.PlatformStatus.GCP.ResourceTags {
				tags[i] = fmt.Sprintf("%s/%s/%s", tag.ParentID, tag.Key, tag.Value)
			}
		}

		if len(tags) <= 0 {
			klog.V(5).Infof("withCustomResourceTags: user tags not configured, no changes made to driver args")
			return nil
		}

		tagsStr := strings.Join(tags, ",")
		tagsArg := fmt.Sprintf("--extra-tags=%s", tagsStr)
		klog.V(5).Infof("withCustomResourceTags: adding extra-tags arg to driver with value %s", tagsStr)

		for i := range deployment.Spec.Template.Spec.Containers {
			container := &deployment.Spec.Template.Spec.Containers[i]
			if container.Name != "csi-driver" {
				continue
			}
			container.Args = append(container.Args, tagsArg)
		}
		return nil
	}
}

func extractOperatorSpec(obj *unstructured.Unstructured, fieldManager string) (*applyopv1.OperatorSpecApplyConfiguration, error) {
	castObj := &opv1.ClusterCSIDriver{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, castObj); err != nil {
		return nil, fmt.Errorf("unable to convert to ClusterCSIDriver: %w", err)
	}
	ret, err := applyopv1.ExtractClusterCSIDriver(castObj, fieldManager)
	if err != nil {
		return nil, fmt.Errorf("unable to extract fields for %q: %w", fieldManager, err)
	}
	if ret.Spec == nil {
		return nil, nil
	}
	return &ret.Spec.OperatorSpecApplyConfiguration, nil
}

func extractOperatorStatus(obj *unstructured.Unstructured, fieldManager string) (*applyopv1.OperatorStatusApplyConfiguration, error) {
	castObj := &opv1.ClusterCSIDriver{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, castObj); err != nil {
		return nil, fmt.Errorf("unable to convert to ClusterCSIDriver: %w", err)
	}
	ret, err := applyopv1.ExtractClusterCSIDriverStatus(castObj, fieldManager)
	if err != nil {
		return nil, fmt.Errorf("unable to extract fields for %q: %w", fieldManager, err)
	}

	if ret.Status == nil {
		return nil, nil
	}
	return &ret.Status.OperatorStatusApplyConfiguration, nil
}
