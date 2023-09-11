package defaults

import (
	"fmt"
	"os"

	opv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/csi-operator/pkg/clients"
	"github.com/openshift/csi-operator/pkg/generator"
	"github.com/openshift/csi-operator/pkg/operator/config"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivercontrollerservicecontroller"
	dc "github.com/openshift/library-go/pkg/operator/deploymentcontroller"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
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
		cfg.AddDeploymentHookBuilders(c, withHyperShiftReplicas, withHyperShiftNodeSelector, getHyperShiftControlPlaneImages)
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
	hook := csidrivercontrollerservicecontroller.WithReplicasHook(c.GetGuestNodeInformer().Lister())
	informers := []factory.Informer{
		c.GetGuestNodeInformer().Informer(),
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

// getHostedControlPlaneNodeSelector returns the node selector from the HostedControlPlane CR.
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

// getHostedControlPlane returns the HostedControlPlane CR.
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

// getHyperShiftControlPlaneImages returns a Deployment hook that sets control-plane images on a HyperShift hosted
func getHyperShiftControlPlaneImages(c *clients.Clients) (dc.DeploymentHookFunc, []factory.Informer) {
	hook := func(_ *opv1.OperatorSpec, deployment *appsv1.Deployment) error {
		driverControlPlaneImage := os.Getenv("DRIVER_CONTROL_PLANE_IMAGE")
		livenessProbeControlPlaneImage := os.Getenv("LIVENESS_PROBE_CONTROL_PLANE_IMAGE")
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
	return hook, nil
}
