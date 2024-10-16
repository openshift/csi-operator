package operator

import (
	"fmt"
	"os"
	"strings"

	opv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/csi-operator/pkg/clients"
	"github.com/openshift/csi-operator/pkg/generator"
	"github.com/openshift/csi-operator/pkg/operator/config"
	hypev1beta1api "github.com/openshift/hypershift/api/hypershift/v1beta1"
	hypev1beta1listers "github.com/openshift/hypershift/client/listers/hypershift/v1beta1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivercontrollerservicecontroller"
	dc "github.com/openshift/library-go/pkg/operator/deploymentcontroller"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
		cfg.AddDeploymentHookBuilders(c, withHyperShiftReplicas, withHyperShiftNodeSelector, withHyperShiftControlPlaneImages)
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
		c.GetNodeInformer().Informer(),
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
			c.ControlPlaneHypeInformer.Hypershift().V1beta1().HostedControlPlanes().Lister(),
			c.ControlPlaneNamespace)
		if err != nil {
			return err
		}
		podSpec.NodeSelector = nodeSelector

		return nil
	}
	informers := []factory.Informer{
		c.ControlPlaneHypeInformer.Hypershift().V1beta1().HostedControlPlanes().Informer(),
	}
	return hook, informers
}

// getHostedControlPlaneNodeSelector returns the node selector from the HostedControlPlane CR.
func getHostedControlPlaneNodeSelector(hostedControlPlaneLister hypev1beta1listers.HostedControlPlaneLister, namespace string) (map[string]string, error) {
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

// getHostedControlPlane returns the HostedControlPlane CR.
func getHostedControlPlane(hostedControlPlaneLister hypev1beta1listers.HostedControlPlaneLister, namespace string) (*hypev1beta1api.HostedControlPlane, error) {
	list, err := hostedControlPlaneLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("no HostedControlPlane found in namespace %s", namespace)
	}
	if len(list) > 1 {
		return nil, fmt.Errorf("more than one HostedControlPlane found in namespace %s", namespace)
	}

	hcp := list[0]
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
