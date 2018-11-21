package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	openshiftapi "github.com/openshift/api/operator/v1alpha1"
	"github.com/openshift/csi-operator/pkg/apis"
	"github.com/openshift/csi-operator/pkg/apis/csidriver/v1alpha1"
	framework "github.com/operator-framework/operator-sdk/pkg/test"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/scheme"
)

const (
	// How long to wait for object to be really deleted.
	deletionTimeout = time.Minute

	// How long to wait for CSIDriverDeployment to get ready. It may wait for some image pulls.
	csiDeploymentTimeout = time.Minute * 5
)

func prepareTest(t *testing.T) (ctx *framework.TestCtx, client framework.FrameworkClient, namespace string) {
	t.Parallel()

	csiList := &v1alpha1.CSIDriverDeploymentList{
		TypeMeta: metav1.TypeMeta{
			Kind:       "CSIDriverDeployment",
			APIVersion: "csidriver.storage.openshift.io/v1alpha1",
		},
	}
	err := framework.AddToFrameworkScheme(apis.AddToScheme, csiList)
	if err != nil {
		t.Fatalf("failed to add custom resource scheme to framework: %v", err)
	}
	ctx = framework.NewTestCtx(t)
	ns, err := ctx.GetNamespace()
	if err != nil {
		t.Fatalf("failed to initialize namespace: %v", err)
	}
	return ctx, framework.Global.Client, ns

}

func waitForCSIDriverDeploymentReady(client framework.FrameworkClient, cr *v1alpha1.CSIDriverDeployment, timeout time.Duration) error {
	return wait.PollImmediate(time.Second, timeout, func() (done bool, err error) {
		newCR := &v1alpha1.CSIDriverDeployment{}
		err = client.Get(context.TODO(), types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}, newCR)
		if err != nil {
			return false, err
		}

		available := false
		syncSuccessful := false

		for _, c := range newCR.Status.Conditions {
			switch c.Type {
			case openshiftapi.OperatorStatusTypeAvailable:
				available = c.Status == openshiftapi.ConditionTrue
			case openshiftapi.OperatorStatusTypeSyncSuccessful:
				syncSuccessful = c.Status == openshiftapi.ConditionTrue
			}
		}
		if available && syncSuccessful {
			return true, nil
		}
		return false, nil
	})
}

func waitForObjectExists(client framework.FrameworkClient, namespace, name string, obj runtime.Object, timeout time.Duration) error {
	return wait.PollImmediate(time.Second, timeout, func() (done bool, err error) {
		err = client.Get(context.TODO(), types.NamespacedName{Name: name, Namespace: namespace}, obj)
		if err != nil {
			if errors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
}

func waitForObjectDeleted(client framework.FrameworkClient, namespace, name string, obj runtime.Object, timeout time.Duration) error {
	return wait.PollImmediate(time.Second, timeout, func() (done bool, err error) {
		err = client.Get(context.TODO(), types.NamespacedName{Name: name, Namespace: namespace}, obj)
		if err != nil {
			if errors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}
		return false, nil
	})
}

func createCSIDriverDeployment(client framework.FrameworkClient, namespace string, filename string) (*v1alpha1.CSIDriverDeployment, error) {
	csi := &v1alpha1.CSIDriverDeployment{}
	csiData := MustAsset(filename)
	if err := runtime.DecodeInto(scheme.Codecs.UniversalDeserializer(), csiData, csi); err != nil {
		return nil, fmt.Errorf("cannot decode %s: %s", filename, err)
	}
	csi.Namespace = namespace
	err := client.Create(context.TODO(), csi, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create CSIDriverDeployment: %s", err)
	}
	return csi, nil
}

func checkChildrenExists(t *testing.T, client framework.FrameworkClient, namespace string, cr *v1alpha1.CSIDriverDeployment, timeout time.Duration) {
	sa := &corev1.ServiceAccount{}
	if err := waitForObjectExists(client, namespace, cr.Name, sa, timeout); err != nil {
		t.Errorf("failed to get ServiceAccount: %s", err)
	}
	t.Log("ServiceAccount is ready")

	crb := &rbacv1.ClusterRoleBinding{}
	crbName := "csidriverdeployment-" + string(cr.UID)
	if err := waitForObjectExists(client, "", crbName, crb, timeout); err != nil {
		t.Errorf("failed to get ClusterRoleBinding: %s", err)
	} else {
		// TODO: check values
	}
	t.Log("ClusterRoleBinding is ready")

	rb := &rbacv1.RoleBinding{}
	rbName := "leader-election-" + cr.Name
	if err := waitForObjectExists(client, namespace, rbName, rb, timeout); err != nil {
		t.Errorf("failed to get RoleBinding: %s", err)
	} else {
		// TODO: check values
	}
	t.Log("RoleBinding is ready")

	ds := &appsv1.DaemonSet{}
	dsName := cr.Name + "-node"
	if err := waitForObjectExists(client, namespace, dsName, ds, timeout); err != nil {
		t.Errorf("failed to get DaemonSet: %s", err)
	} else {
		// TODO: check values
	}
	t.Log("DaemonSet is ready")

	deployment := &appsv1.Deployment{}
	deploymentName := cr.Name + "-controller"
	if err := waitForObjectExists(client, namespace, deploymentName, deployment, timeout); err != nil {
		t.Errorf("failed to get Deployment: %s", err)
	} else {
		// TODO: check values
	}
	t.Log("Deployment is ready")

	sc1 := &storagev1.StorageClass{}
	sc1Name := "sc1" // from hostpath.yaml
	if err := waitForObjectExists(client, "", sc1Name, sc1, timeout); err != nil {
		t.Errorf("failed to get StorageClass1: %s", err)
	} else {
		// TODO: check values
	}
	t.Log("StorageClass1 is ready")

	sc2 := &storagev1.StorageClass{}
	sc2Name := "sc2" // from hostpath.yaml
	if err := waitForObjectExists(client, "", sc2Name, sc2, timeout); err != nil {
		t.Errorf("failed to get StorageClass2: %s", err)
	} else {
		// TODO: check values
	}
	t.Log("StorageClass2 is ready")
}

func checkChildrenDeleted(t *testing.T, client framework.FrameworkClient, namespace string, cr *v1alpha1.CSIDriverDeployment, timeout time.Duration) {
	sa := &corev1.ServiceAccount{}
	if err := waitForObjectDeleted(client, namespace, cr.Name, sa, timeout); err != nil {
		t.Errorf("error waiting for ServiceAccount to be deleted: %s", err)
	}
	t.Log("ServiceAccount deleted")

	crb := &rbacv1.ClusterRoleBinding{}
	crbName := "csidriverdeployment-" + string(cr.UID)
	if err := waitForObjectDeleted(client, "", crbName, crb, timeout); err != nil {
		t.Errorf("error waiting for ClusterRoleBinding to be deleted: %s", err)
	}
	t.Log("ClusterRoleBinding deleted")

	rb := &rbacv1.RoleBinding{}
	rbName := "leader-election-" + cr.Name
	if err := waitForObjectDeleted(client, namespace, rbName, rb, timeout); err != nil {
		t.Errorf("error waiting for RoleBinding to be deleted: %s", err)
	}
	t.Log("RoleBinding deleted")

	ds := &appsv1.DaemonSet{}
	dsName := cr.Name + "-node"
	if err := waitForObjectDeleted(client, namespace, dsName, ds, timeout); err != nil {
		t.Errorf("error waiting for DaemonSet to be deleted: %s", err)
	}
	t.Log("DaemonSet deleted")

	deployment := &appsv1.Deployment{}
	deploymentName := cr.Name + "-controller"
	if err := waitForObjectDeleted(client, namespace, deploymentName, deployment, timeout); err != nil {
		t.Errorf("error waiting for Deployment to be deleted: %s", err)
	}
	t.Log("Deployment deleted")

	sc1 := &storagev1.StorageClass{}
	sc1Name := "sc1" // from hostpath.yaml
	if err := waitForObjectDeleted(client, "", sc1Name, sc1, timeout); err != nil {
		t.Errorf("error waiting for StorageClass1 to be deleted: %s", err)
	}
	t.Log("StorageClass1 deleted")

	sc2 := &storagev1.StorageClass{}
	sc2Name := "sc2" // from hostpath.yaml
	if err := waitForObjectDeleted(client, "", sc2Name, sc2, timeout); err != nil {
		t.Errorf("error waiting for StorageClass2 to be deleted: %s", err)
	}
	t.Log("StorageClass2 deleted")
}

func deleteChildren(t *testing.T, client framework.FrameworkClient, namespace string, cr *v1alpha1.CSIDriverDeployment) {
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: cr.Name, Namespace: namespace}}
	if err := client.Delete(context.TODO(), sa); err != nil {
		t.Errorf("error deleting ServiceAccount: %s", err)
	}
	t.Log("ServiceAccount deleted")

	crbName := "csidriverdeployment-" + string(cr.UID)
	crb := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: crbName, Namespace: ""}}
	if err := client.Delete(context.TODO(), crb); err != nil {
		t.Errorf("error deleting ClusterRoleBinding: %s", err)
	}
	t.Log("ClusterRoleBinding deleted")

	rbName := "leader-election-" + cr.Name
	rb := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: rbName, Namespace: namespace}}
	if err := client.Delete(context.TODO(), rb); err != nil {
		t.Errorf("error deleting RoleBinding: %s", err)
	}
	t.Log("RoleBinding deleted")

	dsName := cr.Name + "-node"
	ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: dsName, Namespace: namespace}}
	if err := client.Delete(context.TODO(), ds); err != nil {
		t.Errorf("error deleting DaemonSet: %s", err)
	}
	t.Log("DaemonSet deleted")

	deploymentName := cr.Name + "-controller"
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: deploymentName, Namespace: namespace}}
	if err := client.Delete(context.TODO(), deployment); err != nil {
		t.Errorf("error deleting Deployment: %s", err)
	}
	t.Log("Deployment deleted")

	sc1Name := "sc1" // from hostpath.yaml
	sc1 := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: sc1Name, Namespace: namespace}}
	if err := client.Delete(context.TODO(), sc1); err != nil {
		t.Errorf("error deleting StorageClass1: %s", err)
	}
	t.Log("StorageClass1 deleted")

	sc2Name := "sc2" // from hostpath.yaml
	sc2 := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: sc2Name, Namespace: namespace}}
	if err := client.Delete(context.TODO(), sc2); err != nil {
		t.Errorf("error deleting StorageClass2: %s", err)
	}
	t.Log("StorageClass2 deleted")
}

func modifyObject(client framework.FrameworkClient, obj runtime.Object, modifyFunc func(object runtime.Object)) error {
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return err
	}
	key := types.NamespacedName{Name: accessor.GetName(), Namespace: accessor.GetNamespace()}

	for {
		if err := client.Get(context.TODO(), key, obj); err != nil {
			return err
		}
		modifyFunc(obj)
		if err := client.Update(context.TODO(), obj); err != nil {
			if errors.IsConflict(err) {
				continue
			}
			return err
		}
		return nil
	}
}

// TestCSIOperator creates a CR for HostPath driver and waits until it is ready (both Conditions are true).
// Then it deletes the CR and checks that all objects are deleted too.
func TestCSIOperatorCreateDelete(t *testing.T) {
	ctx, client, ns := prepareTest(t)
	defer ctx.Cleanup()

	t.Log("=== Create CSIDriverDeployment")
	csi, err := createCSIDriverDeployment(client, ns, "hostpath.yaml")
	if err != nil {
		t.Fatalf("error creating CSIDriverDeployment: %s", err)
	}

	t.Log("=== Wait for CSIDriverDeployment to be ready")
	if err := waitForCSIDriverDeploymentReady(client, csi, csiDeploymentTimeout); err != nil {
		t.Errorf("failed to wait for CSIDriverDeployment to get ready: %s", err)
	}
	t.Log("CSIDriverDeployment is ready")

	t.Log("=== Check children")
	checkChildrenExists(t, client, ns, csi, time.Second)

	t.Log("=== Make CSIDriverDeployment unmanaged")
	// Make the CR unmanaged, so we can check deletion of objects
	err = modifyObject(client, csi, func(obj runtime.Object) {
		csi := obj.(*v1alpha1.CSIDriverDeployment)
		csi.Spec.ManagementState = openshiftapi.Unmanaged
	})
	if err != nil {
		t.Errorf("error updating CSIDriverDeployment: %s", err)
	}

	t.Log("=== Delete children")
	deleteChildren(t, client, ns, csi)
	checkChildrenDeleted(t, client, ns, csi, deletionTimeout)

	// Make the CR managed to test the controller re-creates the objects
	t.Log("=== Make CSIDriverDeployment managed")
	err = modifyObject(client, csi, func(obj runtime.Object) {
		csi := obj.(*v1alpha1.CSIDriverDeployment)
		csi.Spec.ManagementState = openshiftapi.Managed
	})
	if err != nil {
		t.Errorf("error updating CSIDriverDeployment: %s", err)
	}

	t.Log("=== Check children")
	checkChildrenExists(t, client, ns, csi, deletionTimeout)

	// Delete the CR
	t.Log("=== Delete CSIDriverDeployment")
	if err := client.Delete(context.TODO(), csi); err != nil {
		t.Errorf("failed to delete CSIDriverDeployment: %s", err)
	}
	t.Log("CSIDriverDeployment deleted")

	t.Log("=== Check children are deleted")
	checkChildrenDeleted(t, client, ns, csi, deletionTimeout)
}
