package operator

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	opv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/csi-operator/pkg/clients"
	"github.com/openshift/csi-operator/pkg/generator"
	"github.com/openshift/csi-operator/pkg/operator/config"
	hypev1beta1api "github.com/openshift/hypershift/api/hypershift/v1beta1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivercontrollerservicecontroller"
	dc "github.com/openshift/library-go/pkg/operator/deploymentcontroller"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

var (
	hostedControlPlaneGVR = schema.GroupVersionResource{
		Group:    "hypershift.openshift.io",
		Version:  "v1beta1",
		Resource: "hostedcontrolplanes",
	}
)

// NewDefaultOperatorConfig returns an OperatorConfig with default standalone / HyperShift hooks.
func NewDefaultOperatorControllerConfig(flavour generator.ClusterFlavour, c *clients.Clients, controllerNamePrefix string) *config.OperatorControllerConfig {
	cfg := &config.OperatorControllerConfig{
		ControllerNamePrefix: controllerNamePrefix,
	}
	// Default controller hooks
	if flavour == generator.FlavourStandalone {
		cfg.AddDeploymentHookBuilders(c, withClusterWideProxy, withStandaloneReplicas)
	} else {
		// HyperShift
		cfg.AddDeploymentHookBuilders(c, withHyperShiftReplicas, withHyperShiftNodeSelector, withHyperShiftLabels, withHyperShiftControlPlaneImages, withHyperShiftCustomTolerations, withHyperShiftRunAsUser)
	}

	return cfg
}

// withClusterWideProxy adds the cluster-wide proxy config to the Deployment.
func withClusterWideProxy(c *clients.Clients) (dc.DeploymentHookFunc, []factory.Informer) {
	hook := csidrivercontrollerservicecontroller.WithObservedProxyDeploymentHook()
	return hook, nil
}

// withStandaloneReplicas sets control-plane replica count to on a standalone cluster.
func withStandaloneReplicas(c *clients.Clients) (dc.DeploymentHookFunc, []factory.Informer) {
	hook := csidrivercontrollerservicecontroller.WithReplicasHook(c.ConfigInformers)
	informers := []factory.Informer{
		c.ConfigInformers.Config().V1().Infrastructures().Informer(),
	}
	return hook, informers
}

// withHyperShiftReplicas sets control-plane replica count on HyperShift.
func withHyperShiftReplicas(c *clients.Clients) (dc.DeploymentHookFunc, []factory.Informer) {
	hook := func(_ *opv1.OperatorSpec, deployment *appsv1.Deployment) error {
		// TODO: get this information from HostedControlPlane.Spec.AvailabilityPolicy
		replicas := int32(1)
		deployment.Spec.Replicas = &replicas
		return nil
	}

	return hook, nil
}

// withHyperShiftNodeSelector sets Deployment node selector on a HyperShift hosted control-plane.
func withHyperShiftNodeSelector(c *clients.Clients) (dc.DeploymentHookFunc, []factory.Informer) {
	hook := func(_ *opv1.OperatorSpec, deployment *appsv1.Deployment) error {
		podSpec := &deployment.Spec.Template.Spec
		// Add nodeSelector
		nodeSelector, err := getHostedControlPlaneNodeSelector(
			c.ControlPlaneDynamicInformer.ForResource(hostedControlPlaneGVR).Lister(),
			c.ControlPlaneNamespace)
		if err != nil {
			return err
		}
		podSpec.NodeSelector = nodeSelector

		return nil
	}
	informers := []factory.Informer{
		c.ControlPlaneDynamicInformer.ForResource(hostedControlPlaneGVR).Informer(),
	}
	return hook, informers
}

// withHyperShiftNodeSelector sets Deployment node selector on a HyperShift hosted control-plane.
func withHyperShiftCustomTolerations(c *clients.Clients) (dc.DeploymentHookFunc, []factory.Informer) {
	hook := func(_ *opv1.OperatorSpec, deployment *appsv1.Deployment) error {
		podSpec := &deployment.Spec.Template.Spec
		// Add Custom Tolerations
		customTolerations, err := getHostedControlPlaneTolerations(
			c.ControlPlaneDynamicInformer.ForResource(hostedControlPlaneGVR).Lister(),
			c.ControlPlaneNamespace)
		if err != nil {
			return err
		}
		podSpec.Tolerations = append(podSpec.Tolerations, customTolerations...)

		return nil
	}
	informers := []factory.Informer{
		c.ControlPlaneDynamicInformer.ForResource(hostedControlPlaneGVR).Informer(),
	}
	return hook, informers
}

// getHostedControlPlaneNodeSelector returns the node selector from the HostedControlPlane CR.
func getHostedControlPlaneNodeSelector(hostedControlPlaneLister cache.GenericLister, namespace string) (map[string]string, error) {
	hcp, err := getHostedControlPlane(hostedControlPlaneLister, namespace)
	if err != nil {
		return nil, err
	}
	nodeSelector := hcp.Spec.NodeSelector
	if len(nodeSelector) == 0 {
		return nil, nil
	}
	klog.V(4).Infof("Using node selector %v", nodeSelector)
	return nodeSelector, nil
}

func getHostedControlPlaneTolerations(hostedControlPlaneLister cache.GenericLister, namespace string) ([]corev1.Toleration, error) {
	hcp, err := getHostedControlPlane(hostedControlPlaneLister, namespace)
	if err != nil {
		return nil, err
	}
	tolerations := hcp.Spec.Tolerations
	if len(hcp.Spec.Tolerations) == 0 {
		return nil, nil
	}
	klog.V(4).Infof("Using tolerations %v", tolerations)
	return tolerations, nil
}

// withHyperShiftLabels sets Deployment labels on a HyperShift hosted control-plane.
func withHyperShiftLabels(c *clients.Clients) (dc.DeploymentHookFunc, []factory.Informer) {
	hook := func(_ *opv1.OperatorSpec, deployment *appsv1.Deployment) error {
		labels, err := getHostedControlLabels(
			c.ControlPlaneDynamicInformer.ForResource(hostedControlPlaneGVR).Lister(),
			c.ControlPlaneNamespace)
		if err != nil {
			return err
		}

		if deployment.Spec.Template.Labels == nil {
			deployment.Spec.Template.Labels = map[string]string{}
		}

		for key, value := range labels {
			// don't replace existing labels as they are used in the deployment's labelSelector.
			if _, exist := deployment.Spec.Template.Labels[key]; !exist {
				deployment.Spec.Template.Labels[key] = value
			}
		}
		return nil
	}
	informers := []factory.Informer{
		c.ControlPlaneDynamicInformer.ForResource(hostedControlPlaneGVR).Informer(),
	}
	return hook, informers
}

// getHostedControlLabels returns the labels from the HostedControlPlane CR.
func getHostedControlLabels(hostedControlPlaneLister cache.GenericLister, namespace string) (map[string]string, error) {
	hcp, err := getHostedControlPlane(hostedControlPlaneLister, namespace)
	if err != nil {
		return nil, err
	}
	labels := hcp.Spec.Labels
	if len(labels) == 0 {
		return nil, nil
	}
	klog.V(4).Infof("Using labels %v", labels)
	return labels, nil

}

// getHostedControlPlane returns the HostedControlPlane CR.
func getHostedControlPlane(hostedControlPlaneLister cache.GenericLister, namespace string) (*hypev1beta1api.HostedControlPlane, error) {
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

	unstructuredObj := list[0].(*unstructured.Unstructured)
	if unstructuredObj == nil {
		return nil, fmt.Errorf("unknown type of HostedControlPlane found in namespace %s", namespace)
	}
	hcp := &hypev1beta1api.HostedControlPlane{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredObj.Object, hcp); err != nil {
		return nil, fmt.Errorf("unable to convert to HostedControlPlane: %w", err)
	}
	return hcp, nil
}

// withHyperShiftControlPlaneImages returns a Deployment hook that sets control-plane images on a HyperShift hosted
func withHyperShiftControlPlaneImages(c *clients.Clients) (dc.DeploymentHookFunc, []factory.Informer) {
	hook := func(_ *opv1.OperatorSpec, deployment *appsv1.Deployment) error {
		driverControlPlaneImage := os.Getenv("DRIVER_CONTROL_PLANE_IMAGE")
		livenessProbeControlPlaneImage := os.Getenv("LIVENESS_PROBE_CONTROL_PLANE_IMAGE")
		kubeRBACProxyControlPlaneImage := os.Getenv("KUBE_RBAC_PROXY_CONTROL_PLANE_IMAGE")
		for i := range deployment.Spec.Template.Spec.Containers {
			container := &deployment.Spec.Template.Spec.Containers[i]
			if container.Name == "csi-driver" && driverControlPlaneImage != "" {
				container.Image = driverControlPlaneImage
			}
			if container.Name == "csi-liveness-probe" && livenessProbeControlPlaneImage != "" {
				container.Image = livenessProbeControlPlaneImage
			}
			if strings.Contains(container.Name, "kube-rbac-proxy") && kubeRBACProxyControlPlaneImage != "" {
				container.Image = kubeRBACProxyControlPlaneImage
			}
		}
		return nil
	}
	return hook, nil
}

// withHyperShiftRunAsUser handles the RUN_AS_USER environment variable for HyperShift deployments.
// This is required for deploying control planes on clusters that do not have Security Context Constraints (SCCs), for example AKS.
// If RUN_AS_USER is set, this hook adds runAsUser to security context of CSI driver controller pod.
func withHyperShiftRunAsUser(c *clients.Clients) (dc.DeploymentHookFunc, []factory.Informer) {
	hook := func(_ *opv1.OperatorSpec, deployment *appsv1.Deployment) error {
		uid := os.Getenv("RUN_AS_USER")
		if uid == "" {
			return nil
		}

		runAsUserValue, err := strconv.ParseInt(uid, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid RUN_AS_USER value %q: must be a valid integer: %w", uid, err)
		}
		if runAsUserValue < 0 {
			return fmt.Errorf("invalid RUN_AS_USER value %q: must be non-negative", uid)
		}

		if deployment.Spec.Template.Spec.SecurityContext == nil {
			deployment.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{}
		}
		deployment.Spec.Template.Spec.SecurityContext.RunAsUser = &runAsUserValue

		return nil
	}
	return hook, nil
}

// WithTokenMinter returns a Deployment hook that adds a token-minter sidecar container for HyperShift.
// The token-minter creates guest cluster service account tokens for use in the management cluster.
// Note: The bound-sa-token and hosted-kubeconfig volumes are added by the HyperShift patch files,
// so this hook only adds the container that uses them.
func WithTokenMinter(serviceAccountName string) dc.DeploymentHookFunc {
	return func(_ *opv1.OperatorSpec, deployment *appsv1.Deployment) error {
		tokenMinter := corev1.Container{
			Name:  "token-minter",
			Image: os.Getenv("HYPERSHIFT_IMAGE"),
			Command: []string{
				"/usr/bin/control-plane-operator",
				"token-minter",
			},
			Args: []string{
				"--service-account-namespace=openshift-cluster-csi-drivers",
				"--service-account-name=" + serviceAccountName,
				"--token-audience=openshift",
				"--token-file=/var/run/secrets/openshift/serviceaccount/token",
				"--kubeconfig=/etc/hosted-kubernetes/kubeconfig",
			},
			ImagePullPolicy: corev1.PullIfNotPresent,
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("10m"),
					corev1.ResourceMemory: resource.MustParse("10Mi"),
				},
			},
			TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
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
		}
		deployment.Spec.Template.Spec.Containers = append(deployment.Spec.Template.Spec.Containers, tokenMinter)

		return nil
	}
}
