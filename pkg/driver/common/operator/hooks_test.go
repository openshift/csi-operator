package operator

import (
	"reflect"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	fakeconfig "github.com/openshift/client-go/config/clientset/versioned/fake"
	"github.com/openshift/csi-operator/pkg/clients"
	"github.com/openshift/csi-operator/pkg/driver/common/operator/test_manifests"
	hypev1beta1api "github.com/openshift/hypershift/api/hypershift/v1beta1"
	fakehype "github.com/openshift/hypershift/client/clientset/clientset/fake"
	hypescheme "github.com/openshift/hypershift/client/clientset/clientset/scheme"
	"github.com/openshift/library-go/pkg/operator/resource/resourceread"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func getTestDeployment() *appsv1.Deployment {
	return resourceread.ReadDeploymentV1OrDie(test_manifests.ReadFileOrDie("aws_ebs_controller_hypershift.yaml"))
}

func makeInfrastructure() *configv1.Infrastructure {
	return &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{
				AWS: &configv1.AWSPlatformStatus{},
			},
		},
	}
}

func makeInfraWithCPTopology(mode configv1.TopologyMode) *configv1.Infrastructure {
	infra := makeInfrastructure()
	infra.Status.ControlPlaneTopology = mode
	return infra
}

func Test_WithStandaloneReplicas(t *testing.T) {
	tests := []struct {
		name             string
		infra            *configv1.Infrastructure
		expectedReplicas int32
	}{
		{
			name:             "HighlyAvailableTopologyMode",
			infra:            makeInfraWithCPTopology(configv1.HighlyAvailableTopologyMode),
			expectedReplicas: 2,
		},
		{
			name:             "SingleReplicaTopologyMode",
			infra:            makeInfraWithCPTopology(configv1.SingleReplicaTopologyMode),
			expectedReplicas: 1,
		},
		{
			name:             "ExternalTopologyMode",
			infra:            makeInfraWithCPTopology(configv1.ExternalTopologyMode),
			expectedReplicas: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := clients.GetFakeOperatorCR()
			c := clients.NewFakeClients("test", cr)
			// Inject custom infrastructure
			c.ConfigClientSet.(*fakeconfig.Clientset).Tracker().Add(tt.infra)
			hook, _ := withStandaloneReplicas(c)
			deployment := getTestDeployment()
			// Sync the informers with the client as the last step, withStandaloneReplicas()
			// must create necessary informers before.
			clients.SyncFakeInformers(t, c)

			// Act
			err := hook(&cr.Spec.OperatorSpec, deployment)
			if err != nil {
				t.Fatalf("unexpected hook error: %v", err)
			}
			// Assert
			if *deployment.Spec.Replicas != tt.expectedReplicas {
				t.Errorf("expected %d replicas, got %d", tt.expectedReplicas, *deployment.Spec.Replicas)
			}

		})
	}
}

func readHcpOrDie(objBytes []byte) *hypev1beta1api.HostedControlPlane {
	requiredObj, err := runtime.Decode(hypescheme.Codecs.UniversalDecoder(hypev1beta1api.SchemeGroupVersion), objBytes)
	if err != nil {
		panic(err)
	}
	return requiredObj.(*hypev1beta1api.HostedControlPlane)
}

func getTestHostedControlPlane(assetName string) *hypev1beta1api.HostedControlPlane {
	return readHcpOrDie(test_manifests.ReadFileOrDie(assetName))
}

func Test_WithHyperShiftNodeSelector(t *testing.T) {
	tests := []struct {
		name                 string
		hcp                  *hypev1beta1api.HostedControlPlane
		expectedNodeSelector map[string]string
	}{
		{
			name:                 "no selector",
			hcp:                  getTestHostedControlPlane("hcp_no_selector.yaml"),
			expectedNodeSelector: nil,
		},
		{
			name: "selector",
			hcp:  getTestHostedControlPlane("hcp_selector.yaml"),
			expectedNodeSelector: map[string]string{
				"foo": "bar",
				"baz": "",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := clients.GetFakeOperatorCR()
			c := clients.NewFakeClients("clusters-test", cr)
			// Arrange: inject HostedControlPlane to the clients
			c.ControlPlaneHypeClient.(*fakehype.Clientset).Tracker().Add(tt.hcp)

			hook, _ := withHyperShiftNodeSelector(c)
			deployment := getTestDeployment()
			// Sync the informers with the client as the last step, withHyperShiftNodeSelector()
			// must create necessary informers before.
			clients.SyncFakeInformers(t, c)

			// Act
			err := hook(&cr.Spec.OperatorSpec, deployment)
			if err != nil {
				t.Fatalf("unexpected hook error: %v", err)
			}
			// Assert
			if !reflect.DeepEqual(deployment.Spec.Template.Spec.NodeSelector, tt.expectedNodeSelector) {
				t.Errorf("expected node selector %+v , got %+v", tt.expectedNodeSelector, deployment.Spec.Template.Spec.NodeSelector)
			}
		})
	}
}

func Test_WithHyperShiftLabels(t *testing.T) {
	tests := []struct {
		name           string
		hcp            *hypev1beta1api.HostedControlPlane
		expectedLabels map[string]string
	}{
		{
			name:           "no labels",
			hcp:            getTestHostedControlPlane("hcp_no_labels.yaml"),
			expectedLabels: nil,
		},
		{
			name: "labels",
			hcp:  getTestHostedControlPlane("hcp_labels.yaml"),
			expectedLabels: map[string]string{
				"foo": "bar",
				"baz": "",
			},
		},
		{
			name: "existing labels should not be replaced",
			hcp:  getTestHostedControlPlane("hcp_no_labels.yaml"),
			expectedLabels: map[string]string{
				"app": "aws-ebs-csi-driver-controller",
				"hypershift.openshift.io/hosted-control-plane": "clusters-test",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := clients.GetFakeOperatorCR()
			c := clients.NewFakeClients("clusters-test", cr)
			// Arrange: inject HostedControlPlane to the clients
			c.ControlPlaneHypeClient.(*fakehype.Clientset).Tracker().Add(tt.hcp)

			hook, _ := withHyperShiftLabels(c)
			deployment := getTestDeployment()
			// Sync the informers with the client as the last step, withHyperShiftLabels()
			// must create necessary informers before.
			clients.SyncFakeInformers(t, c)

			// Act
			err := hook(&cr.Spec.OperatorSpec, deployment)
			if err != nil {
				t.Fatalf("unexpected hook error: %v", err)
			}
			// Assert
			for key, expectedValue := range tt.expectedLabels {
				value, exist := deployment.Spec.Template.Labels[key]
				if !exist || value != expectedValue {
					t.Errorf("expected labels %s to exist with value %s", key, expectedValue)
				}
			}
		})
	}
}

func Test_WithHyperShiftControlPlaneImages(t *testing.T) {
	tests := []struct {
		name string
		// env. variable name -> value
		env map[string]string
		// Deployment container name -> image name
		expectedContainerImages map[string]string
	}{
		{
			name: "no env",
			env:  nil,
			expectedContainerImages: map[string]string{
				"csi-driver":         "${DRIVER_IMAGE}",
				"csi-liveness-probe": "${LIVENESS_PROBE_IMAGE}",
			},
		},
		{
			name: "env set",
			env: map[string]string{
				"DRIVER_CONTROL_PLANE_IMAGE":          "control_plane_driver_image:1",
				"LIVENESS_PROBE_CONTROL_PLANE_IMAGE":  "control_plane_livenessprobe_image:1",
				"KUBE_RBAC_PROXY_CONTROL_PLANE_IMAGE": "control_plane_kube_rbac_proxy_image:1",
			},
			expectedContainerImages: map[string]string{
				"csi-driver":                  "control_plane_driver_image:1",
				"csi-liveness-probe":          "control_plane_livenessprobe_image:1",
				"kube-rbac-proxy-8201":        "control_plane_kube_rbac_proxy_image:1",
				"provisioner-kube-rbac-proxy": "control_plane_kube_rbac_proxy_image:1",
				"attacher-kube-rbac-proxy":    "control_plane_kube_rbac_proxy_image:1",
				"resizer-kube-rbac-proxy":     "control_plane_kube_rbac_proxy_image:1",
				"snapshotter-kube-rbac-proxy": "control_plane_kube_rbac_proxy_image:1",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := clients.GetFakeOperatorCR()
			c := clients.NewFakeClients("clusters-test", cr)
			hook, _ := withHyperShiftControlPlaneImages(c)
			deployment := getTestDeployment()
			// Arrange: set env vars
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			clients.SyncFakeInformers(t, c)

			// Act
			err := hook(&cr.Spec.OperatorSpec, deployment)
			if err != nil {
				t.Fatalf("unexpected hook error: %v", err)
			}
			// Assert
			for containerName, expectedImage := range tt.expectedContainerImages {
				found := false
				for _, container := range deployment.Spec.Template.Spec.Containers {
					if container.Name == containerName {
						if container.Image != expectedImage {
							t.Errorf("expected image %s for container %s, got %s", expectedImage, containerName, container.Image)
						}
						found = true
						break
					}
				}
				if !found {
					t.Errorf("container %s not found", containerName)
				}
			}
		})
	}
}
