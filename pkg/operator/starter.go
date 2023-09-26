package operator

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	kubeclient "k8s.io/client-go/kubernetes"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	opv1 "github.com/openshift/api/operator/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	v1 "github.com/openshift/client-go/config/listers/config/v1"
	opclient "github.com/openshift/client-go/operator/clientset/versioned"
	opinformers "github.com/openshift/client-go/operator/informers/externalversions"
	"github.com/openshift/library-go/pkg/config/client"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/csi/csicontrollerset"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivercontrollerservicecontroller"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivernodeservicecontroller"
	dc "github.com/openshift/library-go/pkg/operator/deploymentcontroller"
	"github.com/openshift/library-go/pkg/operator/events"
	goc "github.com/openshift/library-go/pkg/operator/genericoperatorclient"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"github.com/openshift/library-go/pkg/operator/resourcesynccontroller"
	"github.com/openshift/library-go/pkg/operator/staticresourcecontroller"
	"github.com/openshift/library-go/pkg/operator/v1helpers"

	"github.com/openshift/aws-ebs-csi-driver-operator/assets"
)

const (
	defaultNamespace   = "openshift-cluster-csi-drivers"
	operatorName       = "aws-ebs-csi-driver-operator"
	operandName        = "aws-ebs-csi-driver"
	infraConfigName    = "cluster"
	trustedCAConfigMap = "aws-ebs-csi-driver-trusted-ca-bundle"

	cloudCredSecretName   = "ebs-cloud-credentials"
	metricsCertSecretName = "aws-ebs-csi-driver-controller-metrics-serving-cert"

	hypershiftImageEnvName = "HYPERSHIFT_IMAGE"

	cloudConfigNamespace = "openshift-config-managed"
	cloudConfigName      = "kube-cloud-config"
	caBundleKey          = "ca-bundle.pem"

	infrastructureName = "cluster"
	kmsKeyID           = "kmsKeyId"

	hypershiftPriorityClass = "hypershift-control-plane"

	resync = 20 * time.Minute
)

var (
	hostedControlPlaneGVR = schema.GroupVersionResource{
		Group:    "hypershift.openshift.io",
		Version:  "v1beta1",
		Resource: "hostedcontrolplanes",
	}
)

func RunOperator(ctx context.Context, controllerConfig *controllercmd.ControllerContext, guestKubeConfigString string) error {
	// Create core clientset and informer for the MANAGEMENT cluster.
	eventRecorder := controllerConfig.EventRecorder
	controlPlaneNamespace := controllerConfig.OperatorNamespace
	controlPlaneKubeClient := kubeclient.NewForConfigOrDie(rest.AddUserAgent(controllerConfig.KubeConfig, operatorName))
	controlPlaneKubeInformersForNamespaces := v1helpers.NewKubeInformersForNamespaces(controlPlaneKubeClient, controlPlaneNamespace)
	controlPlaneSecretInformer := controlPlaneKubeInformersForNamespaces.InformersFor(controlPlaneNamespace).Core().V1().Secrets()
	controlPlaneConfigMapInformer := controlPlaneKubeInformersForNamespaces.InformersFor(controlPlaneNamespace).Core().V1().ConfigMaps()

	// Create informer for the ConfigMaps in the operator namespace.
	// This is used to get the custom CA bundle to use when accessing the AWS API.
	// This is only synced on standalone OCP clusters.
	controlPlaneCloudConfigInformers := v1helpers.NewKubeInformersForNamespaces(controlPlaneKubeClient, controlPlaneNamespace, cloudConfigNamespace)
	controlPlaneCloudConfigInformer := controlPlaneCloudConfigInformers.InformersFor(controlPlaneNamespace).Core().V1().ConfigMaps()
	controlPlaneCloudConfigLister := controlPlaneCloudConfigInformer.Lister().ConfigMaps(controlPlaneNamespace)

	controlPlaneDynamicClient, err := dynamic.NewForConfig(controllerConfig.KubeConfig)
	if err != nil {
		return err
	}
	controlPlaneDynamicInformers := dynamicinformer.NewFilteredDynamicSharedInformerFactory(controlPlaneDynamicClient, resync, controlPlaneNamespace, nil)

	// Create core clientset for the GUEST cluster.
	guestNamespace := defaultNamespace
	guestKubeConfig := controllerConfig.KubeConfig
	guestKubeClient := controlPlaneKubeClient
	isHypershift := guestKubeConfigString != ""
	if isHypershift {
		guestKubeConfig, err = client.GetKubeConfigOrInClusterConfig(guestKubeConfigString, nil)
		if err != nil {
			return err
		}
		guestKubeClient = kubeclient.NewForConfigOrDie(rest.AddUserAgent(guestKubeConfig, operatorName))

		// Create all events in the GUEST cluster.
		// Use name of the operator Deployment in the management cluster + namespace
		// in the guest cluster as the closest approximation of the real involvedObject.
		controllerRef, err := events.GetControllerReferenceForCurrentPod(ctx, controlPlaneKubeClient, controlPlaneNamespace, nil)
		controllerRef.Namespace = guestNamespace
		if err != nil {
			klog.Warningf("unable to get owner reference (falling back to namespace): %v", err)
		}
		eventRecorder = events.NewKubeRecorder(guestKubeClient.CoreV1().Events(guestNamespace), operandName, controllerRef)
	}

	guestAPIExtClient, err := apiextclient.NewForConfig(rest.AddUserAgent(guestKubeConfig, operatorName))
	if err != nil {
		return err
	}

	guestDynamicClient, err := dynamic.NewForConfig(guestKubeConfig)
	if err != nil {
		return err
	}

	// Client informers for the GUEST cluster.
	guestKubeInformersForNamespaces := v1helpers.NewKubeInformersForNamespaces(guestKubeClient, guestNamespace, "")
	guestConfigMapInformer := guestKubeInformersForNamespaces.InformersFor(guestNamespace).Core().V1().ConfigMaps()
	guestNodeInformer := guestKubeInformersForNamespaces.InformersFor("").Core().V1().Nodes()

	guestConfigClient := configclient.NewForConfigOrDie(rest.AddUserAgent(guestKubeConfig, operatorName))
	guestConfigInformers := configinformers.NewSharedInformerFactory(guestConfigClient, resync)
	guestInfraInformer := guestConfigInformers.Config().V1().Infrastructures()

	// operator.openshift.io client, used for ClusterCSIDriver
	guestCCDClient := opclient.NewForConfigOrDie(rest.AddUserAgent(guestKubeConfig, operatorName))
	guestCCDInformers := opinformers.NewSharedInformerFactory(guestCCDClient, resync)

	// Create client and informers for our ClusterCSIDriver CR.
	gvr := opv1.SchemeGroupVersion.WithResource("clustercsidrivers")
	guestOperatorClient, guestDynamicInformers, err := goc.NewClusterScopedOperatorClientWithConfigName(guestKubeConfig, gvr, string(opv1.AWSEBSCSIDriver))
	if err != nil {
		return err
	}
	staticResourceFiles := []string{
		"controller_pdb.yaml",
		"cabundle_cm.yaml",
	}
	if isHypershift {
		staticResourceFiles = append(staticResourceFiles, "hypershift/controller_sa.yaml")
	} else {
		staticResourceFiles = append(staticResourceFiles, "controller_sa.yaml")
	}

	var hostedControlPlaneLister cache.GenericLister
	var hostedControlPlaneInformer cache.SharedInformer
	if isHypershift {
		hostedControlPlaneInformer = controlPlaneDynamicInformers.ForResource(hostedControlPlaneGVR).Informer()
		hostedControlPlaneLister = controlPlaneDynamicInformers.ForResource(hostedControlPlaneGVR).Lister()
	}

	controlPlaneInformersForEvents := []factory.Informer{
		controlPlaneSecretInformer.Informer(),
		controlPlaneConfigMapInformer.Informer(),
		guestNodeInformer.Informer(),
		guestInfraInformer.Informer(),
	}
	if isHypershift {
		controlPlaneInformersForEvents = append(controlPlaneInformersForEvents, hostedControlPlaneInformer)
	} else {
		controlPlaneInformersForEvents = append(controlPlaneInformersForEvents, controlPlaneCloudConfigInformer.Informer())
	}

	// Start controllers that manage resources in the MANAGEMENT cluster.
	controlPlaneCSIControllerSet := csicontrollerset.NewCSIControllerSet(
		guestOperatorClient,
		eventRecorder,
	).WithLogLevelController().WithManagementStateController(
		operandName,
		false,
	).WithStaticResourcesController(
		"AWSEBSDriverControlPlaneStaticResourcesController",
		controlPlaneKubeClient,
		controlPlaneDynamicClient,
		controlPlaneKubeInformersForNamespaces,
		assetWithNamespaceFunc(controlPlaneNamespace),
		staticResourceFiles,
	).WithCSIConfigObserverController(
		"AWSEBSDriverCSIConfigObserverController",
		guestConfigInformers,
	).WithCSIDriverControllerService(
		"AWSEBSDriverControllerServiceController",
		assets.ReadFile,
		"controller.yaml",
		controlPlaneKubeClient,
		controlPlaneKubeInformersForNamespaces.InformersFor(controlPlaneNamespace),
		guestConfigInformers,
		controlPlaneInformersForEvents,
		withHypershiftDeploymentHook(isHypershift, os.Getenv(hypershiftImageEnvName), controlPlaneNamespace, hostedControlPlaneLister),
		withHypershiftReplicasHook(isHypershift, guestNodeInformer.Lister()),
		withNamespaceDeploymentHook(controlPlaneNamespace),
		csidrivercontrollerservicecontroller.WithSecretHashAnnotationHook(controlPlaneNamespace, cloudCredSecretName, controlPlaneSecretInformer),
		csidrivercontrollerservicecontroller.WithSecretHashAnnotationHook(controlPlaneNamespace, metricsCertSecretName, controlPlaneSecretInformer),
		csidrivercontrollerservicecontroller.WithObservedProxyDeploymentHook(),
		withCustomAWSCABundle(isHypershift, controlPlaneCloudConfigLister),
		withAWSRegion(guestInfraInformer.Lister()),
		withCustomTags(guestInfraInformer.Lister()),
		withCustomEndPoint(guestInfraInformer.Lister()),
		csidrivercontrollerservicecontroller.WithCABundleDeploymentHook(
			controlPlaneNamespace,
			trustedCAConfigMap,
			controlPlaneConfigMapInformer,
		),
		withHypershiftControlPlaneImages(isHypershift, os.Getenv("DRIVER_CONTROL_PLANE_IMAGE"), os.Getenv("LIVENESS_PROBE_CONTROL_PLANE_IMAGE")),
	)
	if err != nil {
		return err
	}

	// Start controllers that manage resources in GUEST clusters.
	guestCSIControllerSet := csicontrollerset.NewCSIControllerSet(
		guestOperatorClient,
		eventRecorder,
	).WithStaticResourcesController(
		"AWSEBSDriverGuestStaticResourcesController",
		guestKubeClient,
		guestDynamicClient,
		guestKubeInformersForNamespaces,
		assets.ReadFile,
		[]string{
			"csidriver.yaml",
			"node_sa.yaml",
			"rbac/privileged_role.yaml",
			"rbac/node_privileged_binding.yaml",
		},
	).WithConditionalStaticResourcesController(
		"AWSEBSDriverConditionalStaticResourcesController",
		guestKubeClient,
		guestDynamicClient,
		guestKubeInformersForNamespaces,
		assets.ReadFile,
		[]string{
			"volumesnapshotclass.yaml",
		},
		// Only install when CRD exists.
		func() bool {
			name := "volumesnapshotclasses.snapshot.storage.k8s.io"
			_, err := guestAPIExtClient.ApiextensionsV1().CustomResourceDefinitions().Get(context.TODO(), name, metav1.GetOptions{})
			return err == nil
		},
		// Don't ever remove.
		func() bool {
			return false
		},
	).WithCSIDriverNodeService(
		"AWSEBSDriverNodeServiceController",
		assets.ReadFile,
		"node.yaml",
		guestKubeClient,
		guestKubeInformersForNamespaces.InformersFor(guestNamespace),
		[]factory.Informer{guestConfigMapInformer.Informer()},
		csidrivernodeservicecontroller.WithObservedProxyDaemonSetHook(),
		csidrivernodeservicecontroller.WithCABundleDaemonSetHook(
			guestNamespace,
			trustedCAConfigMap,
			guestConfigMapInformer,
		),
	).WithStorageClassController(
		"AWSEBSDriverStorageClassController",
		assets.ReadFile,
		[]string{
			"storageclass_gp3.yaml",
			"storageclass_gp2.yaml",
		},
		guestKubeClient,
		guestKubeInformersForNamespaces.InformersFor(""),
		guestCCDInformers,
		getKMSKeyHook(guestCCDInformers.Operator().V1().ClusterCSIDrivers().Lister()),
	)

	if !isHypershift {
		caSyncController, err := newCustomAWSBundleSyncer(
			guestOperatorClient,
			controlPlaneCloudConfigInformers,
			controlPlaneKubeClient,
			controlPlaneNamespace,
			eventRecorder,
		)
		if err != nil {
			return fmt.Errorf("could not create the custom CA bundle syncer: %w", err)
		}

		klog.Info("Starting custom CA bundle informers")
		go controlPlaneCloudConfigInformers.Start(ctx.Done())

		klog.Info("Starting custom CA bundle sync controller")
		go caSyncController.Run(ctx, 1)

		staticResourcesController := staticresourcecontroller.NewStaticResourceController(
			"AWSEBSDriverStaticResourcesController",
			assets.ReadFile,
			[]string{
				"rbac/main_attacher_binding.yaml",
				"rbac/main_provisioner_binding.yaml",
				"rbac/volumesnapshot_reader_provisioner_binding.yaml",
				"rbac/main_resizer_binding.yaml",
				"rbac/storageclass_reader_resizer_binding.yaml",
				"rbac/main_snapshotter_binding.yaml",
				"service.yaml",
				"rbac/prometheus_role.yaml",
				"rbac/prometheus_rolebinding.yaml",
				"rbac/kube_rbac_proxy_role.yaml",
				"rbac/kube_rbac_proxy_binding.yaml",
				"rbac/lease_leader_election_role.yaml",
				"rbac/lease_leader_election_rolebinding.yaml",
			},
			(&resourceapply.ClientHolder{}).WithKubernetes(controlPlaneKubeClient).WithDynamicClient(controlPlaneDynamicClient),
			guestOperatorClient,
			eventRecorder,
		).AddKubeInformers(controlPlaneKubeInformersForNamespaces)

		klog.Info("Starting static resources controller")
		go staticResourcesController.Run(ctx, 1)

		serviceMonitorController := staticresourcecontroller.NewStaticResourceController(
			"AWSEBSDriverServiceMonitorController",
			assets.ReadFile,
			[]string{"servicemonitor.yaml"},
			(&resourceapply.ClientHolder{}).WithDynamicClient(controlPlaneDynamicClient),
			guestOperatorClient,
			eventRecorder,
		).WithIgnoreNotFoundOnCreate()

		klog.Info("Starting ServiceMonitor controller")
		go serviceMonitorController.Run(ctx, 1)
	}

	klog.Info("Starting the control plane informers")
	go controlPlaneKubeInformersForNamespaces.Start(ctx.Done())
	go controlPlaneDynamicInformers.Start(ctx.Done())

	klog.Info("Starting control plane controllerset")
	go controlPlaneCSIControllerSet.Run(ctx, 1)

	klog.Info("Starting the guest cluster informers")
	go guestKubeInformersForNamespaces.Start(ctx.Done())
	go guestDynamicInformers.Start(ctx.Done())
	go guestConfigInformers.Start(ctx.Done())
	go guestCCDInformers.Start(ctx.Done())

	klog.Info("Starting guest cluster controllerset")
	go guestCSIControllerSet.Run(ctx, 1)

	<-ctx.Done()

	return fmt.Errorf("stopped")
}

// withCustomAWSCABundle executes the asset as a template to fill out the parts required when using a custom CA bundle.
// The `caBundleConfigMap` parameter specifies the name of the ConfigMap containing the custom CA bundle. If the
// argument supplied is empty, then no custom CA bundle will be used.
func withCustomAWSCABundle(isHypershift bool, cloudConfigLister corev1listers.ConfigMapNamespaceLister) dc.DeploymentHookFunc {
	return func(_ *opv1.OperatorSpec, deployment *appsv1.Deployment) error {
		configName, err := customAWSCABundle(isHypershift, cloudConfigLister)
		if err != nil {
			return fmt.Errorf("could not determine if a custom CA bundle is in use: %w", err)
		}
		if configName == "" {
			return nil
		}

		deployment.Spec.Template.Spec.Volumes = append(deployment.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: "ca-bundle",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: configName},
				},
			},
		})
		for i := range deployment.Spec.Template.Spec.Containers {
			container := &deployment.Spec.Template.Spec.Containers[i]
			if container.Name != "csi-driver" {
				continue
			}
			container.Env = append(container.Env, corev1.EnvVar{
				Name:  "AWS_CA_BUNDLE",
				Value: "/etc/ca/ca-bundle.pem",
			})
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
				Name:      "ca-bundle",
				MountPath: "/etc/ca",
				ReadOnly:  true,
			})
			return nil
		}
		return fmt.Errorf("could not use custom CA bundle because the csi-driver container is missing from the deployment")
	}
}

func withCustomEndPoint(infraLister v1.InfrastructureLister) dc.DeploymentHookFunc {
	return func(_ *opv1.OperatorSpec, deployment *appsv1.Deployment) error {
		infra, err := infraLister.Get(infrastructureName)
		if err != nil {
			return err
		}
		if infra.Status.PlatformStatus == nil || infra.Status.PlatformStatus.AWS == nil {
			return nil
		}
		serviceEndPoints := infra.Status.PlatformStatus.AWS.ServiceEndpoints
		ec2EndPoint := ""
		for _, serviceEndPoint := range serviceEndPoints {
			if serviceEndPoint.Name == "ec2" {
				ec2EndPoint = serviceEndPoint.URL
			}
		}
		if ec2EndPoint == "" {
			return nil
		}

		for i := range deployment.Spec.Template.Spec.Containers {
			container := &deployment.Spec.Template.Spec.Containers[i]
			if container.Name != "csi-driver" {
				continue
			}
			container.Env = append(container.Env, corev1.EnvVar{
				Name:  "AWS_EC2_ENDPOINT",
				Value: ec2EndPoint,
			})
			return nil
		}
		return nil
	}
}

func newCustomAWSBundleSyncer(
	operatorClient v1helpers.OperatorClient,
	kubeInformers v1helpers.KubeInformersForNamespaces,
	kubeClient kubeclient.Interface,
	destinationNamespace string,
	eventRecorder events.Recorder,
) (factory.Controller, error) {
	// sync config map with additional trust bundle to the operator namespace,
	// so the operator can get it as a ConfigMap volume.
	srcConfigMap := resourcesynccontroller.ResourceLocation{
		Namespace: cloudConfigNamespace,
		Name:      cloudConfigName,
	}
	dstConfigMap := resourcesynccontroller.ResourceLocation{
		Namespace: destinationNamespace,
		Name:      cloudConfigName,
	}
	certController := resourcesynccontroller.NewResourceSyncController(
		operatorClient,
		kubeInformers,
		kubeClient.CoreV1(),
		kubeClient.CoreV1(),
		eventRecorder)
	err := certController.SyncConfigMap(dstConfigMap, srcConfigMap)
	if err != nil {
		return nil, err
	}
	return certController, nil
}

// customAWSCABundle returns true if the cloud config ConfigMap exists and contains a custom CA bundle.
func customAWSCABundle(isHypershift bool, cloudConfigLister corev1listers.ConfigMapNamespaceLister) (string, error) {
	configName := cloudConfigName
	if isHypershift {
		configName = "user-ca-bundle"
	}

	cloudConfigCM, err := cloudConfigLister.Get(configName)
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get the %s ConfigMap: %w", configName, err)
	}

	if _, ok := cloudConfigCM.Data[caBundleKey]; !ok {
		return "", nil
	}
	return configName, nil
}

// withCustomTags add tags from Infrastructure.Status.PlatformStatus.AWS.ResourceTags to the driver command line as
// --extra-tags=<key1>=<value1>,<key2>=<value2>,...
func withCustomTags(infraLister v1.InfrastructureLister) dc.DeploymentHookFunc {
	return func(spec *opv1.OperatorSpec, deployment *appsv1.Deployment) error {
		infra, err := infraLister.Get(infrastructureName)
		if err != nil {
			return err
		}
		if infra.Status.PlatformStatus == nil || infra.Status.PlatformStatus.AWS == nil {
			return nil
		}

		userTags := infra.Status.PlatformStatus.AWS.ResourceTags
		if len(userTags) == 0 {
			return nil
		}

		tagPairs := make([]string, 0, len(userTags))
		for _, userTag := range userTags {
			pair := fmt.Sprintf("%s=%s", userTag.Key, userTag.Value)
			tagPairs = append(tagPairs, pair)
		}
		tags := strings.Join(tagPairs, ",")
		tagsArgument := fmt.Sprintf("--extra-tags=%s", tags)

		for i := range deployment.Spec.Template.Spec.Containers {
			container := &deployment.Spec.Template.Spec.Containers[i]
			if container.Name != "csi-driver" {
				continue
			}
			container.Args = append(container.Args, tagsArgument)
		}
		return nil
	}
}

func assetWithNamespaceFunc(namespace string) resourceapply.AssetFunc {
	return func(name string) ([]byte, error) {
		content, err := assets.ReadFile(name)
		if err != nil {
			panic(err)
		}
		return bytes.ReplaceAll(content, []byte("${NAMESPACE}"), []byte(namespace)), nil
	}
}

func withNamespaceDeploymentHook(namespace string) dc.DeploymentHookFunc {
	return func(_ *opv1.OperatorSpec, deployment *appsv1.Deployment) error {
		deployment.Namespace = namespace
		return nil
	}
}

func withHypershiftReplicasHook(isHypershift bool, guestNodeLister corev1listers.NodeLister) dc.DeploymentHookFunc {
	if !isHypershift {
		return csidrivercontrollerservicecontroller.WithReplicasHook(guestNodeLister)
	}
	return func(_ *opv1.OperatorSpec, deployment *appsv1.Deployment) error {
		// TODO: get this information from HostedControlPlane.Spec.AvailabilityPolicy
		replicas := int32(1)
		deployment.Spec.Replicas = &replicas
		return nil
	}

}

func withHypershiftDeploymentHook(isHypershift bool, hypershiftImage string, namespace string, hostedControlPlaneLister cache.GenericLister) dc.DeploymentHookFunc {
	return func(_ *opv1.OperatorSpec, deployment *appsv1.Deployment) error {
		if !isHypershift {
			return nil
		}

		// Remove inject-proxy annotations
		delete(deployment.Annotations, "config.openshift.io/inject-proxy")
		delete(deployment.Annotations, "config.openshift.io/inject-proxy-cabundle")

		deployment.Spec.Template.Spec.PriorityClassName = hypershiftPriorityClass

		// Inject into the pod the volumes used by CSI and token minter sidecars.
		podSpec := &deployment.Spec.Template.Spec
		podSpec.Volumes = append(podSpec.Volumes,
			corev1.Volume{
				Name: "hosted-kubeconfig",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						// FIXME: use a ServiceAccount from the guest cluster
						SecretName: "service-network-admin-kubeconfig",
					},
				},
			},
		)

		// The bound-sa-token volume must be a empty disk in Hypershift.
		for i := range podSpec.Volumes {
			if podSpec.Volumes[i].Name != "bound-sa-token" {
				continue
			}
			podSpec.Volumes[i].VolumeSource = corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium: corev1.StorageMediumMemory,
				},
			}
		}

		// The metrics-serving-cert volume is not used in Hypershift.
		for i := range podSpec.Volumes {
			if podSpec.Volumes[i].Name == "metrics-serving-cert" {
				podSpec.Volumes = append(podSpec.Volumes[:i], podSpec.Volumes[i+1:]...)
				break
			}
		}

		filtered := []corev1.Container{}
		for i := range podSpec.Containers {
			switch podSpec.Containers[i].Name {
			case "driver-kube-rbac-proxy":
			case "provisioner-kube-rbac-proxy":
			case "attacher-kube-rbac-proxy":
			case "resizer-kube-rbac-proxy":
			case "snapshotter-kube-rbac-proxy":
			default:
				filtered = append(filtered, podSpec.Containers[i])
			}
		}
		podSpec.Containers = filtered

		// Inject into the CSI sidecars the hosted Kubeconfig.
		for i := range podSpec.Containers {
			container := &podSpec.Containers[i]
			switch container.Name {
			case "csi-provisioner":
			case "csi-attacher":
			case "csi-snapshotter":
			case "csi-resizer":
			default:
				continue
			}
			container.Args = append(container.Args, "--kubeconfig=$(KUBECONFIG)")
			container.Env = append(container.Env, corev1.EnvVar{
				Name:  "KUBECONFIG",
				Value: "/etc/hosted-kubernetes/kubeconfig",
			})
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
				Name:      "hosted-kubeconfig",
				MountPath: "/etc/hosted-kubernetes",
				ReadOnly:  true,
			})
		}

		// Add the token minter sidecar into the pod.
		podSpec.Containers = append(podSpec.Containers, corev1.Container{
			Name:            "token-minter",
			Image:           hypershiftImage,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Command:         []string{"/usr/bin/control-plane-operator", "token-minter"},
			Args: []string{
				"--service-account-namespace=openshift-cluster-csi-drivers",
				"--service-account-name=aws-ebs-csi-driver-controller-sa",
				"--token-audience=openshift",
				"--token-file=/var/run/secrets/openshift/serviceaccount/token",
				"--kubeconfig=/etc/hosted-kubernetes/kubeconfig",
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("10m"),
					corev1.ResourceMemory: resource.MustParse("10Mi"),
				},
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      "bound-sa-token",
					MountPath: "/var/run/secrets/openshift/serviceaccount",
				},
				{
					Name:      "hosted-kubeconfig",
					MountPath: "/etc/hosted-kubernetes",
					ReadOnly:  true,
				},
			},
		})

		// Add nodeSelector
		nodeSelector, err := getHostedControlPlaneNodeSelector(hostedControlPlaneLister, namespace)
		if err != nil {
			return err
		}
		podSpec.NodeSelector = nodeSelector

		// Add labels
		if deployment.Spec.Template.Labels == nil {
			deployment.Spec.Template.Labels = map[string]string{}
		}
		deployment.Spec.Template.Labels["hypershift.openshift.io/hosted-control-plane"] = namespace

		// Add Affinity
		if podSpec.Affinity == nil {
			podSpec.Affinity = &corev1.Affinity{}
		}
		if podSpec.Affinity.PodAffinity == nil {
			podSpec.Affinity.PodAffinity = &corev1.PodAffinity{}
		}
		podSpec.Affinity.PodAffinity.PreferredDuringSchedulingIgnoredDuringExecution = []corev1.WeightedPodAffinityTerm{
			{
				Weight: 100,
				PodAffinityTerm: corev1.PodAffinityTerm{
					LabelSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"hypershift.openshift.io/hosted-control-plane": namespace,
						},
					},
					TopologyKey: corev1.LabelHostname,
				},
			},
		}

		if podSpec.Affinity.NodeAffinity == nil {
			podSpec.Affinity.NodeAffinity = &corev1.NodeAffinity{}
		}
		podSpec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution = []corev1.PreferredSchedulingTerm{
			{
				Weight: 50,
				Preference: corev1.NodeSelectorTerm{
					MatchExpressions: []corev1.NodeSelectorRequirement{
						{
							Key:      "hypershift.openshift.io/control-plane",
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{"true"},
						},
					},
				},
			},
			{
				Weight: 100,
				Preference: corev1.NodeSelectorTerm{
					MatchExpressions: []corev1.NodeSelectorRequirement{
						{
							Key:      "hypershift.openshift.io/cluster",
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{namespace},
						},
					},
				},
			},
		}

		// Add tolerations
		podSpec.Tolerations = []corev1.Toleration{
			{
				Key:      "hypershift.openshift.io/control-plane",
				Operator: corev1.TolerationOpEqual,
				Value:    "true",
				Effect:   corev1.TaintEffectNoSchedule,
			},
			{
				Key:      "hypershift.openshift.io/cluster",
				Operator: corev1.TolerationOpEqual,
				Value:    namespace,
				Effect:   corev1.TaintEffectNoSchedule,
			},
		}

		return nil
	}
}

func getHostedControlPlaneNodeSelector(hostedControlPlaneLister cache.GenericLister, namespace string) (map[string]string, error) {
	hcp, err := getHostedControlPlane(hostedControlPlaneLister, namespace)
	if err != nil {
		return nil, err
	}
	nodeSelector, exists, err := unstructured.NestedStringMap(hcp.UnstructuredContent(), "spec", "nodeSelector")
	if !exists {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	klog.V(4).Infof("Using node selector %v", nodeSelector)
	return nodeSelector, nil
}

func getHostedControlPlane(hostedControlPlaneLister cache.GenericLister, namespace string) (*unstructured.Unstructured, error) {
	list, err := hostedControlPlaneLister.ByNamespace(namespace).List(labels.Everything())
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("no HostedControlPlane found in namespace %s", namespace)
	}
	if len(list) > 1 {
		return nil, fmt.Errorf("more than one HostedControlPlane found in namespace %s", namespace)
	}

	hcp := list[0].(*unstructured.Unstructured)
	if hcp == nil {
		return nil, fmt.Errorf("unknown type of HostedControlPlane found in namespace %s", namespace)
	}
	return hcp, nil
}

func withAWSRegion(infraLister v1.InfrastructureLister) dc.DeploymentHookFunc {
	return func(_ *opv1.OperatorSpec, deployment *appsv1.Deployment) error {
		infra, err := infraLister.Get(infrastructureName)
		if err != nil {
			return err
		}

		if infra.Status.PlatformStatus == nil || infra.Status.PlatformStatus.AWS == nil {
			return nil
		}

		region := infra.Status.PlatformStatus.AWS.Region
		if region == "" {
			return nil
		}

		for i := range deployment.Spec.Template.Spec.Containers {
			container := &deployment.Spec.Template.Spec.Containers[i]
			if container.Name != "csi-driver" {
				continue
			}
			container.Env = append(container.Env, corev1.EnvVar{
				Name:  "AWS_REGION",
				Value: region,
			})
		}
		return nil
	}
}

func withHypershiftControlPlaneImages(isHypershift bool, driverControlPlaneImage, livenessProbeControlPlaneImage string) dc.DeploymentHookFunc {
	return func(_ *opv1.OperatorSpec, deployment *appsv1.Deployment) error {
		if !isHypershift {
			return nil
		}
		for i := range deployment.Spec.Template.Spec.Containers {
			container := &deployment.Spec.Template.Spec.Containers[i]
			if container.Name == "csi-driver" && driverControlPlaneImage != "" {
				container.Image = driverControlPlaneImage
			}
			if container.Name == "csi-liveness-probe" && livenessProbeControlPlaneImage != "" {
				container.Image = livenessProbeControlPlaneImage
			}
		}
		return nil
	}
}
