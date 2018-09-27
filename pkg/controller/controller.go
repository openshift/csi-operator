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
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"

	"github.com/golang/glog"

	csidriverv1alpha1 "github.com/openshift/csi-operator/pkg/apis/csidriver/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	finalizerName = "csidriver.storage.okd.io"
)

// Add creates a new CSIDriverDeployment Controller and adds it to the Manager with default RBAC. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager, cfg Config) error {
	return add(mgr, newReconciler(mgr, cfg))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, cfg Config) reconcile.Reconciler {
	return &ReconcileCSIDriverDeployment{
		Client:   mgr.GetClient(),
		scheme:   mgr.GetScheme(),
		config:   cfg,
		recorder: mgr.GetRecorder("CSIDriverDeployment"),
	}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler. It starts informers for all watched
// resources.
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	glog.V(4).Infof("Adding CSIDriver reconciler")
	// Create a new controller
	c, err := controller.New("csidriverdeployment-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to CSIDriverDeployment
	err = c.Watch(&source.Kind{Type: &csidriverv1alpha1.CSIDriverDeployment{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	// Watch ServiceAccounts created by CSIDriverDeployment
	err = c.Watch(&source.Kind{Type: &v1.ServiceAccount{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &csidriverv1alpha1.CSIDriverDeployment{},
	})
	if err != nil {
		return err
	}

	// Watch ClusterRoleBindings created by CSIDriverDeployment
	err = c.Watch(&source.Kind{Type: &rbacv1.ClusterRoleBinding{}}, &EnqueueRequestForLabels{})
	if err != nil {
		return err
	}

	// Watch StorageClasses created by CSIDriverDeployment
	err = c.Watch(&source.Kind{Type: &storagev1.StorageClass{}}, &EnqueueRequestForLabels{})
	if err != nil {
		return err
	}

	// Watch RoleBindings created by CSIDriverDeployment
	err = c.Watch(&source.Kind{Type: &rbacv1.RoleBinding{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &csidriverv1alpha1.CSIDriverDeployment{},
	})
	if err != nil {
		return err
	}

	// Watch DaemonSet created by CSIDriverDeployment
	err = c.Watch(&source.Kind{Type: &appsv1.DaemonSet{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &csidriverv1alpha1.CSIDriverDeployment{},
	})
	if err != nil {
		return err
	}

	// Watch Deployment created by CSIDriverDeployment
	err = c.Watch(&source.Kind{Type: &appsv1.Deployment{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &csidriverv1alpha1.CSIDriverDeployment{},
	})
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileCSIDriverDeployment{}

// ReconcileCSIDriverDeployment reconciles a CSIDriverDeployment object
type ReconcileCSIDriverDeployment struct {
	client.Client
	scheme   *runtime.Scheme
	config   Config
	recorder record.EventRecorder
}

// Reconcile reads that state of the cluster for a CSIDriverDeployment object and makes changes based on the state read
// and what is in the CSIDriverDeployment.Spec
// Automatically generate RBAC rules to allow the Controller to read and write Deployments
// +kubebuilder:rbac:groups=apps,resources=deployments;daemonsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings;rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=,resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=csidriver.storage.okd.io,resources=csidriverdeployments,verbs=get;list;watch;update;patch
func (r *ReconcileCSIDriverDeployment) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	// Fetch the CSIDriverDeployment instance
	instance := &csidriverv1alpha1.CSIDriverDeployment{}
	err := r.Get(context.TODO(), request.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			// Object not found, return.  Created objects are automatically garbage collected.
			// For additional cleanup logic use finalizers.
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	var errs []error
	if instance.DeletionTimestamp != nil {
		// The deployment is being deleted, clean up
		errs, instance = r.cleanupCSIDriverDeployment(instance)
	} else {
		// The deployment was created / updated
		errs, instance = r.syncCSIDriverDeployment(instance)
	}
	if errs != nil {
		// Send errors as events
		for _, err := range errs {
			glog.V(2).Info(err.Error())
			r.recorder.Event(instance, v1.EventTypeWarning, "SyncError", err.Error())
		}
	}
	return reconcile.Result{}, err
}

// syncCSIDriverDeployment checks one CSIDriverDeployment and ensures that all "children" objects are either
// created or updated.
func (r *ReconcileCSIDriverDeployment) syncCSIDriverDeployment(cr *csidriverv1alpha1.CSIDriverDeployment) ([]error, *csidriverv1alpha1.CSIDriverDeployment) {
	glog.V(4).Infof("=== Syncing CSIDriverDeployment %s/%s", cr.Namespace, cr.Name)
	var errs []error

	err, cr := r.syncFinalizer(cr)
	if err != nil {
		// Return now, we can't create subsequent objects without the finalizer because could miss event
		// with CSIDriverDeployment deleletion and we could not delete non-namespaced objects.
		return []error{err}, cr
	}

	err, serviceAccount := r.syncServiceAccount(cr)
	if err != nil {
		err := fmt.Errorf("error syncing ServiceAccount for CSIDriverDeployment %s/%s: %s", cr.Namespace, cr.Name, err)
		errs = append(errs, err)
	}
	err = r.syncClusterRoleBinding(cr, serviceAccount)
	if err != nil {
		err := fmt.Errorf("error syncing ClusterRoleBinding for CSIDriverDeployment %s/%s: %s", cr.Namespace, cr.Name, err)
		errs = append(errs, err)
	}

	err = r.syncLeaderElectionRoleBinding(cr, serviceAccount)
	if err != nil {
		err := fmt.Errorf("error syncing RoleBinding for CSIDriverDeployment %s/%s: %s", cr.Namespace, cr.Name, err)
		errs = append(errs, err)
	}

	expectedStorageClassNames := sets.NewString()
	for i := range cr.Spec.StorageClassTemplates {
		className := cr.Spec.StorageClassTemplates[i].Name
		expectedStorageClassNames.Insert(className)
		err = r.syncStorageClass(cr, &cr.Spec.StorageClassTemplates[i])
		if err != nil {
			err := fmt.Errorf("error syncing StorageClass %s for CSIDriverDeployment %s/%s: %s", className, cr.Namespace, cr.Name, err)
			errs = append(errs, err)
		}
	}
	removeErrs := r.removeUnexpectedStorageClasses(cr, expectedStorageClassNames)
	errs = append(errs, removeErrs...)

	var children []csidriverv1alpha1.ChildGeneration

	ds, err := r.syncDaemonSet(cr, serviceAccount)
	if err != nil {
		err := fmt.Errorf("error syncing DaemonSet for CSIDriverDeployment %s/%s: %s", cr.Namespace, cr.Name, err)
		errs = append(errs, err)
	}
	if ds != nil {
		// Store generation of the DaemonSet so we can check for DaemonSet.Spec changes.
		children = append(children, csidriverv1alpha1.ChildGeneration{
			Group:          appsv1.GroupName,
			Kind:           "DaemonSet",
			Namespace:      ds.Namespace,
			Name:           ds.Name,
			LastGeneration: ds.Generation,
		})
	}

	deployment, err := r.syncDeployment(cr, serviceAccount)
	if err != nil {
		err := fmt.Errorf("error syncing Deployment for CSIDriverDeployment %s/%s: %s", cr.Namespace, cr.Name, err)
		errs = append(errs, err)
	}
	if deployment != nil {
		// Store generation of the Deployment so we can check for DaemonSet.Spec changes.
		children = append(children, csidriverv1alpha1.ChildGeneration{
			Group:          appsv1.GroupName,
			Kind:           "Deployment",
			Namespace:      deployment.Namespace,
			Name:           deployment.Name,
			LastGeneration: deployment.Generation,
		})
	}

	err = r.syncStatus(cr, children)
	if err != nil {
		err := fmt.Errorf("error syncing CSIDriverDeployment.Status for %s/%s: %s", cr.Namespace, cr.Name, err)
		errs = append(errs, err)
	}

	return errs, cr
}

func (r *ReconcileCSIDriverDeployment) syncFinalizer(cr *csidriverv1alpha1.CSIDriverDeployment) (error, *csidriverv1alpha1.CSIDriverDeployment) {
	glog.V(4).Infof("Syncing CSIDriverDeployment.Finalizers")

	if hasFinalizer(cr.Finalizers, finalizerName) {
		return nil, cr
	}

	newCR := cr.DeepCopy()
	if newCR.Finalizers == nil {
		newCR.Finalizers = []string{}
	}
	newCR.Finalizers = append(newCR.Finalizers, finalizerName)

	glog.V(4).Infof("Updating CSIDriverDeployment.Finalizers")
	if err := r.Update(context.TODO(), newCR); err != nil {
		if errors.IsConflict(err) {
			err = nil
		}
		return err, cr
	}

	return nil, newCR
}

type mergeFunc func(existingObject runtime.Object) (error, bool, runtime.Object)

func (r *ReconcileCSIDriverDeployment) syncObject(expectedObject runtime.Object, merge mergeFunc) (error, runtime.Object) {
	existingObject := expectedObject.DeepCopyObject()
	accessor, err := meta.Accessor(expectedObject)
	if err != nil {
		return err, existingObject
	}

	key := types.NamespacedName{Namespace: accessor.GetNamespace(), Name: accessor.GetName()}
	err = r.Get(context.TODO(), key, existingObject)
	if err != nil {
		// Object does not exist, create it
		glog.V(2).Infof("Creating %T %s/%s", expectedObject, key.Namespace, key.Name)
		err := r.Create(context.TODO(), existingObject)
		if err != nil && errors.IsAlreadyExists(err) {
			err = nil
		}
		return err, expectedObject
	}

	err, changed, newObject := merge(existingObject)
	if err != nil {
		return err, existingObject
	}
	if changed {
		glog.V(2).Infof("Updating %T %s/%s", expectedObject, key.Namespace, key.Name)
		err := r.Update(context.TODO(), newObject)
		if err != nil && errors.IsConflict(err) {
			err = nil
		}
		return err, newObject
	}

	return err, existingObject
}

func (r *ReconcileCSIDriverDeployment) syncServiceAccount(cr *csidriverv1alpha1.CSIDriverDeployment) (error, *v1.ServiceAccount) {
	glog.V(4).Infof("Syncing ServiceAccount")

	expectedSC := r.generateServiceAccount(cr)

	err, existingSC := r.syncObject(expectedSC, func(existingObject runtime.Object) (error, bool, runtime.Object) {
		existingSC, ok := existingObject.(*v1.ServiceAccount)
		if !ok {
			return fmt.Errorf("Cannot convert %T to ServiceAccount", existingObject), false, existingObject
		}
		changed := r.addOwnerLabels(&existingSC.ObjectMeta, cr)
		return nil, changed, existingSC
	})
	return err, existingSC.(*v1.ServiceAccount)
}

func (r *ReconcileCSIDriverDeployment) syncClusterRoleBinding(cr *csidriverv1alpha1.CSIDriverDeployment, serviceAccount *v1.ServiceAccount) error {
	glog.V(4).Infof("Syncing ClusterRoleBinding")

	expectedCRB := r.generateClusterRoleBinding(cr, serviceAccount)

	err, _ := r.syncObject(expectedCRB, func(existingObject runtime.Object) (error, bool, runtime.Object) {
		existingCRB, ok := existingObject.(*rbacv1.ClusterRoleBinding)
		if !ok {
			return fmt.Errorf("Cannot convert %T to ClusterRoleBinding", existingObject), false, existingObject
		}
		changed := r.addOwnerLabels(&existingCRB.ObjectMeta, cr)

		if !equality.Semantic.DeepEqual(expectedCRB.Subjects, existingCRB.Subjects) {
			changed = true
			existingCRB.Subjects = expectedCRB.Subjects
		}
		if !equality.Semantic.DeepEqual(expectedCRB.RoleRef, existingCRB.RoleRef) {
			changed = true
			existingCRB.RoleRef = expectedCRB.RoleRef
		}

		return nil, changed, existingCRB
	})
	return err
}

func (r *ReconcileCSIDriverDeployment) syncLeaderElectionRoleBinding(cr *csidriverv1alpha1.CSIDriverDeployment, serviceAccount *v1.ServiceAccount) error {
	glog.V(4).Infof("Syncing leader election RoleBinding")

	expectedRB := r.generateLeaderElectionRoleBinding(cr, serviceAccount)

	err, _ := r.syncObject(expectedRB, func(existingObject runtime.Object) (error, bool, runtime.Object) {
		existingRB, ok := existingObject.(*rbacv1.RoleBinding)
		if !ok {
			return fmt.Errorf("Cannot convert %T to RoleBinding", existingObject), false, existingObject
		}
		changed := r.addOwnerLabels(&existingRB.ObjectMeta, cr)

		if !equality.Semantic.DeepEqual(expectedRB.Subjects, existingRB.Subjects) {
			changed = true
			existingRB.Subjects = expectedRB.Subjects
		}
		if !equality.Semantic.DeepEqual(expectedRB.RoleRef, existingRB.RoleRef) {
			changed = true
			existingRB.RoleRef = expectedRB.RoleRef
		}

		return nil, changed, existingRB
	})
	return err
}

func (r *ReconcileCSIDriverDeployment) syncDaemonSet(cr *csidriverv1alpha1.CSIDriverDeployment, sa *v1.ServiceAccount) (*appsv1.DaemonSet, error) {
	glog.V(4).Infof("Syncing DaemonSet")
	expectedDS := r.generateDaemonSet(cr, sa)

	err, existingDS := r.syncObject(expectedDS, func(existingObject runtime.Object) (error, bool, runtime.Object) {
		existingDS, ok := existingObject.(*appsv1.DaemonSet)
		if !ok {
			return fmt.Errorf("Cannot convert %T to DaemonSet", existingObject), false, existingObject
		}
		changed := r.addOwnerLabels(&existingDS.ObjectMeta, cr)

		if r.childModified(cr, appsv1.GroupName, "DaemonSet", existingDS.ObjectMeta) {
			// DaemonSet.Spec has changed from the value set by the controller
			existingDS.Spec = expectedDS.Spec
			changed = true
		}

		if specHasChanged(cr) {
			existingDS.Spec = expectedDS.Spec
			changed = true
		}

		return nil, changed, existingDS
	})
	return existingDS.(*appsv1.DaemonSet), err
}

func (r *ReconcileCSIDriverDeployment) syncDeployment(cr *csidriverv1alpha1.CSIDriverDeployment, sa *v1.ServiceAccount) (*appsv1.Deployment, error) {
	glog.V(4).Infof("Syncing Deployment")
	if cr.Spec.DriverControllerTemplate == nil {
		return nil, nil
	}

	expectedDeployment := r.generateDeployment(cr, sa)

	err, existingDeployment := r.syncObject(expectedDeployment, func(existingObject runtime.Object) (error, bool, runtime.Object) {
		existingDeployment, ok := existingObject.(*appsv1.Deployment)
		if !ok {
			return fmt.Errorf("Cannot convert %T to Deployment", existingObject), false, existingObject

		}
		changed := r.addOwnerLabels(&existingDeployment.ObjectMeta, cr)

		if r.childModified(cr, appsv1.GroupName, "Deployment", existingDeployment.ObjectMeta) {
			// Deployment.Spec has changed from the value set by the controller
			existingDeployment.Spec = expectedDeployment.Spec
			changed = true
		}

		if specHasChanged(cr) {
			existingDeployment.Spec = expectedDeployment.Spec
			changed = true

		}

		return nil, changed, existingDeployment
	})

	return existingDeployment.(*appsv1.Deployment), err
}

func (r *ReconcileCSIDriverDeployment) syncStorageClass(cr *csidriverv1alpha1.CSIDriverDeployment, template *csidriverv1alpha1.StorageClassTemplate) error {
	glog.V(4).Infof("Syncing StorageClass %s", template.Name)

	expectedSC := r.generateStorageClass(cr, template)

	err, _ := r.syncObject(expectedSC, func(existingObject runtime.Object) (error, bool, runtime.Object) {
		existingSC, ok := existingObject.(*storagev1.StorageClass)
		if !ok {
			return fmt.Errorf("Cannot convert %T to StorageClass", existingObject), false, existingObject

		}
		changed := r.addOwnerLabels(&existingSC.ObjectMeta, cr)

		if !equality.Semantic.DeepEqual(existingSC.MountOptions, expectedSC.MountOptions) {
			changed = true
			existingSC.MountOptions = expectedSC.MountOptions
		}

		allowedExpansionEqual := true
		if existingSC.AllowVolumeExpansion == nil && expectedSC.AllowVolumeExpansion != nil {
			allowedExpansionEqual = false
		}
		if existingSC.AllowVolumeExpansion != nil && expectedSC.AllowVolumeExpansion == nil {
			allowedExpansionEqual = false
		}
		if existingSC.AllowVolumeExpansion != nil && expectedSC.AllowVolumeExpansion != nil && *existingSC.AllowVolumeExpansion != *expectedSC.AllowVolumeExpansion {
			allowedExpansionEqual = false
		}
		if !allowedExpansionEqual {
			changed = true
			existingSC.AllowVolumeExpansion = expectedSC.AllowVolumeExpansion
		}

		if !equality.Semantic.DeepEqual(existingSC.AllowedTopologies, expectedSC.AllowedTopologies) {
			changed = true
			existingSC.AllowedTopologies = expectedSC.AllowedTopologies
		}

		if template.Default != nil && *template.Default == true {
			if value, found := existingSC.Annotations["storageclass.kubernetes.io/is-default-class"]; !found || value != "true" {
				if existingSC.Annotations == nil {
					existingSC.Annotations = map[string]string{}
				}
				existingSC.Annotations["storageclass.kubernetes.io/is-default-class"] = "true"
				changed = true
			}
		} else {
			if value, found := existingSC.Annotations["storageclass.kubernetes.io/is-default-class"]; found && value == "true" {
				delete(existingSC.Annotations, "storageclass.kubernetes.io/is-default-class")
				changed = true
			}
		}

		return nil, changed, existingSC
	})

	return err
}

func (r *ReconcileCSIDriverDeployment) removeUnexpectedStorageClasses(cr *csidriverv1alpha1.CSIDriverDeployment, expectedClasses sets.String) []error {
	list := &storagev1.StorageClassList{}
	listOpts := &client.ListOptions{LabelSelector: r.getOwnerLabelSelector(cr)}
	err := r.Client.List(context.TODO(), listOpts, list)
	if err != nil {
		err := fmt.Errorf("cannot list StorageClasses for CSIDriverDeployment %s/%s: %s", cr.Namespace, cr.Name, err)
		return []error{err}
	}

	var errs []error
	for _, sc := range list.Items {
		if !expectedClasses.Has(sc.Name) {
			glog.V(4).Infof("Deleting StorageClass %s", sc.Name)
			if err := r.Client.Delete(context.TODO(), &sc); err != nil {
				if !errors.IsNotFound(err) {
					err := fmt.Errorf("cannot delete StorageClasses %s for CSIDriverDeployment %s/%s: %s", sc.Name, cr.Namespace, cr.Name, err)
					errs = append(errs, err)
				}
			}
		}
	}
	return errs
}

func (r *ReconcileCSIDriverDeployment) syncStatus(cr *csidriverv1alpha1.CSIDriverDeployment, children []csidriverv1alpha1.ChildGeneration) error {
	newInstance := cr.DeepCopy()

	glog.V(4).Info("Syncing CSIDriverDeployment.Status")
	changed := false

	if !equality.Semantic.DeepEqual(cr.Status.Children, children) {
		newInstance.Status.Children = children
		changed = true
	}
	if cr.Status.ObservedGeneration == nil || *cr.Status.ObservedGeneration != cr.Generation {
		changed = true
		newInstance.Status.ObservedGeneration = &cr.Generation
	}
	if changed {
		err := r.Status().Update(context.TODO(), newInstance)
		if err != nil && errors.IsConflict(err) {
			err = nil
		}
		return err
	}
	return nil
}

// cleanupCSIDriverDeployment removes non-namespaced objects owned by the CSIDriverDeployment.
// ObjectMeta.OwnerReference does not work for them.
func (r *ReconcileCSIDriverDeployment) cleanupCSIDriverDeployment(cr *csidriverv1alpha1.CSIDriverDeployment) ([]error, *csidriverv1alpha1.CSIDriverDeployment) {
	glog.V(4).Infof("=== Cleaning up CSIDriverDeployment %s/%s", cr.Namespace, cr.Name)

	errs := r.cleanupStorageClasses(cr)
	if err := r.cleanupClusterRoleBinding(cr); err != nil {
		errs = append(errs, err)
	}

	if len(errs) != 0 {
		// Don't remove the finalizer yet, there is still stuff to clean up
		return errs, cr
	}

	// Remove the finalizer as the last step
	err, newCR := r.cleanupFinalizer(cr)
	if err != nil {
		return []error{err}, cr
	}
	return nil, newCR
}

func (r *ReconcileCSIDriverDeployment) cleanupFinalizer(cr *csidriverv1alpha1.CSIDriverDeployment) (error, *csidriverv1alpha1.CSIDriverDeployment) {
	// The informer may be stale and the real CSIDriverDeployment may have been already removed.
	currentCR := cr.DeepCopy()
	err := r.Get(context.TODO(), types.NamespacedName{Namespace: cr.Namespace, Name: cr.Name}, currentCR)
	if err != nil {
		if errors.IsNotFound(err) {
			glog.V(4).Infof("CSIDriverDeployment is already deleted")
			return nil, cr
		}
		return err, cr
	}

	currentCR.Finalizers = []string{}
	for _, f := range cr.Finalizers {
		if f == finalizerName {
			continue
		}
		currentCR.Finalizers = append(currentCR.Finalizers, f)
	}

	glog.V(4).Infof("Removing CSIDriverDeployment.Finalizers")
	err = r.Update(context.TODO(), currentCR)
	if err != nil {
		if errors.IsConflict(err) {
			err = nil
		}
		return err, cr
	}
	return nil, currentCR
}

func (r *ReconcileCSIDriverDeployment) cleanupClusterRoleBinding(cr *csidriverv1alpha1.CSIDriverDeployment) error {
	sa := r.generateServiceAccount(cr)
	crb := r.generateClusterRoleBinding(cr, sa)
	err := r.Client.Delete(context.TODO(), crb)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	glog.V(4).Infof("Deleted ClusterRoleBinding %s", crb.Name)
	return nil
}

func (r *ReconcileCSIDriverDeployment) cleanupStorageClasses(cr *csidriverv1alpha1.CSIDriverDeployment) []error {
	return r.removeUnexpectedStorageClasses(cr, sets.NewString())
}

// Adds labels to created objects. These labels will be used to find non-namespaced objects owned by
// a CSIDriverDeployment (as OwnerReference does not work there) and may be used to limit Watch() in future.
func (r *ReconcileCSIDriverDeployment) addOwnerLabels(meta *metav1.ObjectMeta, cr *csidriverv1alpha1.CSIDriverDeployment) bool {
	changed := false
	if meta.Labels == nil {
		meta.Labels = map[string]string{}
		changed = true
	}
	if v, exists := meta.Labels["csidriver.storage.okd.io/owner-namespace"]; !exists || v != cr.Namespace {
		meta.Labels["csidriver.storage.okd.io/owner-namespace"] = cr.Namespace
		changed = true
	}
	if v, exists := meta.Labels["csidriver.storage.okd.io/owner-name"]; !exists || v != cr.Name {
		meta.Labels["csidriver.storage.okd.io/owner-name"] = cr.Name
		changed = true
	}

	return changed
}

func (r *ReconcileCSIDriverDeployment) addOwner(meta *metav1.ObjectMeta, cr *csidriverv1alpha1.CSIDriverDeployment) {
	bTrue := true
	meta.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion: "csidriver.storage.okd.io/v1alpha1",
			Kind:       "CSIDriverDeployment",
			Name:       cr.Name,
			UID:        cr.UID,
			Controller: &bTrue,
		},
	}
}

func (r *ReconcileCSIDriverDeployment) uniqueGlobalName(i *csidriverv1alpha1.CSIDriverDeployment) string {
	return "csidriverdeployment-" + string(i.UID)
}

func (r *ReconcileCSIDriverDeployment) getOwnerLabelSelector(i *csidriverv1alpha1.CSIDriverDeployment) labels.Selector {
	ls := labels.Set{
		ownerLabelNamespace: i.Namespace,
		ownerLabelName:      i.Name,
	}
	return labels.SelectorFromSet(ls)
}

func (r *ReconcileCSIDriverDeployment) childModified(i *csidriverv1alpha1.CSIDriverDeployment, group, kind string, childMeta metav1.ObjectMeta) bool {
	for _, child := range i.Status.Children {
		if child.Group != group {
			continue
		}
		if child.Kind != kind {
			continue
		}
		if child.Namespace != childMeta.Namespace {
			continue
		}
		if child.Name != childMeta.Name {
			continue
		}
		if child.LastGeneration == childMeta.Generation {
			glog.V(4).Infof("No child modification: %s/%s %s/%s: generation %d, last known %d", child.Group, child.Kind, child.Namespace, child.Name, childMeta.Generation, child.LastGeneration)
			return false
		}
		glog.V(4).Infof("Detected child modification: %s/%s %s/%s: generation %d, last known %d", child.Group, child.Kind, child.Namespace, child.Name, childMeta.Generation, child.LastGeneration)
		return true
	}
	return true
}

func hasFinalizer(finalizers []string, finalizerName string) bool {
	for _, f := range finalizers {
		if f == finalizerName {
			return true
		}
	}
	return false
}

func specHasChanged(cr *csidriverv1alpha1.CSIDriverDeployment) bool {
	if cr.Status.ObservedGeneration == nil {
		// CSIDriverDeployment.Spec has not been reported synced yet
		glog.V(4).Infof("Detected CSIDriverDeployment generation change: last observed: nil, current: %d", cr.Generation)
		return true
	} else if *cr.Status.ObservedGeneration != cr.Generation {
		// CSIDriverDeployment.Spec has changed
		glog.V(4).Infof("Detected CSIDriverDeployment generation change: last observed: %d, current: %d", *cr.Status.ObservedGeneration, cr.Generation)
		return true
	}
	return false
}
