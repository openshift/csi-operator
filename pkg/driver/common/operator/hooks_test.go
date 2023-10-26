package operator

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/openshift/csi-operator/pkg/clients"
	"github.com/openshift/csi-operator/pkg/driver/common/operator/test_manifests"
	"github.com/openshift/library-go/pkg/operator/resource/resourceread"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

const (
	masterLabel = "node-role.kubernetes.io/master"
	workerLabel = "node-role.kubernetes.io/worker"
)

func getTestDeployment() *appsv1.Deployment {
	return resourceread.ReadDeploymentV1OrDie(test_manifests.ReadFileOrDie("aws_ebs_controller_hypershift.yaml"))
}

func Test_WithStandaloneReplicas(t *testing.T) {
	tests := []struct {
		name string
		// map "node label" -> nr. of nodes with this label
		nodeCounts       map[string]int
		expectedReplicas int32
	}{
		{
			name: "3 masters 3 workers",
			nodeCounts: map[string]int{
				masterLabel: 3,
				workerLabel: 3,
			},
			expectedReplicas: 2,
		},
		{
			name: "single node openshift",
			nodeCounts: map[string]int{
				masterLabel: 1,
			},
			expectedReplicas: 1,
		},
		{
			// Error case: this should not crash.
			name:             "no nodes",
			nodeCounts:       map[string]int{},
			expectedReplicas: 1, // default fallback
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := clients.GetFakeOperatorCR()
			c := clients.NewFakeClients("test", cr)
			// Arrange: inject desired nr. of nodes to the client
			kubeTracker := c.KubeClient.(*fake.Clientset).Tracker()
			nodeID := 0
			for label, count := range tt.nodeCounts {
				for i := 0; i < count; i++ {
					nodeID++
					node := &v1.Node{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								label: "",
							},
							Name: fmt.Sprintf("node%d", nodeID),
						},
					}
					kubeTracker.Add(node)
				}
			}
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

func getTestHostedControlPlane(assetName string) *unstructured.Unstructured {
	return resourceread.ReadUnstructuredOrDie(test_manifests.ReadFileOrDie(assetName))
}

func Test_WithHyperShiftNodeSelector(t *testing.T) {
	tests := []struct {
		name                 string
		hcp                  *unstructured.Unstructured
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
			c.ControlPlaneDynamicClient.(*fakedynamic.FakeDynamicClient).Tracker().Add(tt.hcp)

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
				"DRIVER_CONTROL_PLANE_IMAGE":         "control_plane_driver_image:1",
				"LIVENESS_PROBE_CONTROL_PLANE_IMAGE": "control_plane_livenessprobe_image:1",
			},
			expectedContainerImages: map[string]string{
				"csi-driver":         "control_plane_driver_image:1",
				"csi-liveness-probe": "control_plane_livenessprobe_image:1",
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
