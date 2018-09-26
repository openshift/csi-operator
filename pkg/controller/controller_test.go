/*

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var c client.Client

var expectedRequest = reconcile.Request{NamespacedName: types.NamespacedName{Name: "foo", Namespace: "default"}}
var depKey = types.NamespacedName{Name: "foo-deployment", Namespace: "default"}

const timeout = time.Second * 5

// TODO: fix the test, allows API server to run privileged pods
/*
func TestReconcile(t *testing.T) {
	g := gomega.NewGomegaWithT(t)
	instance := &csidriverv1alpha1.CSIDriverDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
		},
		Spec: csidriverv1alpha1.CSIDriverDeploymentSpec{
			DriverName: "foo",
			DriverPerNodeTemplate: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "foo",
							Image: "foo",
						},
					},
				},
			},
			DriverSocket: "/csi/csi.sock",
		},
	}

	// Setup the Manager and Controller.  Wrap the Controller Reconcile function so it writes each request to a
	// channel when it is finished.
	mgr, err := manager.New(cfg, manager.Options{})
	g.Expect(err).NotTo(gomega.HaveOccurred())
	c = mgr.GetClient()

	recFn, requests := SetupTestReconcile(newReconciler(mgr, getConfig()))
	g.Expect(add(mgr, recFn)).NotTo(gomega.HaveOccurred())
	defer close(StartTestManager(mgr, g))

	// Create the CSIDriverDeployment object and expect the Reconcile and Deployment to be created
	err = c.Create(context.TODO(), instance)
	// The instance object may not be a valid object because it might be missing some required fields.
	// Please modify the instance object by adding required fields and then remove the following if statement.
	if apierrors.IsInvalid(err) {
		t.Logf("failed to create object, got an invalid object error: %v", err)
		return
	}
	g.Expect(err).NotTo(gomega.HaveOccurred())
	defer c.Delete(context.TODO(), instance)
	g.Eventually(requests, timeout).Should(gomega.Receive(gomega.Equal(expectedRequest)))

	ds := &appsv1.DaemonSet{}
	g.Eventually(func() error { return c.Get(context.TODO(), depKey, ds) }, timeout).
		Should(gomega.Succeed())

	// Delete the Deployment and expect Reconcile to be called for Deployment deletion
	g.Expect(c.Delete(context.TODO(), ds)).NotTo(gomega.HaveOccurred())
	g.Eventually(requests, timeout).Should(gomega.Receive(gomega.Equal(expectedRequest)))
	g.Eventually(func() error { return c.Get(context.TODO(), depKey, ds) }, timeout).
		Should(gomega.Succeed())

	// Manually delete Deployment since GC isn't enabled in the test control plane
	g.Expect(c.Delete(context.TODO(), ds)).To(gomega.Succeed())
}

func getConfig() Config {
	str2ptr := func(str string) *string {
		return &str
	}

	return Config{
		DefaultImages: csidriverv1alpha1.CSIDeploymentContainerImages{
			AttacherImage:        str2ptr("csi-attacher"),
			ProvisionerImage:     str2ptr("csi-provisioner"),
			DriverRegistrarImage: str2ptr("driver-registrar"),
			LivenessProbeImage:   str2ptr("livenessprobe"),
		},
		InfrastructureNodeSelector:    nil,
		DeploymentReplicas:            1,
		ClusterRoleName:               "myrole",
		LeaderElectionClusterRoleName: "leader-role",
		KubeletRootDir:                "/var/lib/kubelet",
	}
}
*/
