package controller

import (
	"context"
	"fmt"
	"sync"

	"github.com/golang/glog"
	openshiftapi "github.com/openshift/api/operator/v1alpha1"
	"github.com/openshift/csi-operator2/pkg/apis/csidriver/v1alpha1"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"github.com/operator-framework/operator-sdk/pkg/k8sclient"
	"github.com/operator-framework/operator-sdk/pkg/sdk"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
	csiclientset "k8s.io/csi-api/pkg/client/clientset/versioned"
)

const (
	ownerLabelNamespace = "csidriver.storage.okd.io/owner-namespace"
	ownerLabelName      = "csidriver.storage.okd.io/owner-name"

	finalizerName = "csidriver.storage.okd.io"
)

func NewHandler(cfg Config) (sdk.Handler, error) {
	kubeClient := k8sclient.GetKubeClient()
	csiClient, err := csiclientset.NewForConfig(k8sclient.GetKubeConfig())
	if err != nil {
		return nil, err
	}

	broadcaster := record.NewBroadcaster()
	broadcaster.StartLogging(glog.Infof)
	broadcaster.StartRecordingToSink(&v1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})
	eventRecorder := broadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "csi-operator"})

	handler := &Handler{
		kubeClient: kubeClient,
		csiClient:  csiClient,
		recorder:   eventRecorder,
		config:     cfg,
	}
	return handler, nil
}

type Handler struct {
	kubeClient kubernetes.Interface
	csiClient  csiclientset.Interface
	recorder   record.EventRecorder
	config     Config

	lock sync.Mutex
}

func (h *Handler) Handle(ctx context.Context, event sdk.Event) error {
	var instance *v1alpha1.CSIDriverDeployment
	var err error

	switch o := event.Object.(type) {
	case *v1alpha1.CSIDriverDeployment:
		instance = o
		err = nil
	case *corev1.ServiceAccount, *rbacv1.RoleBinding, *appsv1.Deployment, *appsv1.DaemonSet:
		// namespaced objects, use owner
		instance, err = h.getCSIDriverDeploymentFromOwner(o)

	case *rbacv1.ClusterRoleBinding, *storagev1.StorageClass:
		// non-namespaced objects, use labels
		instance, err = h.getCSIDriverDeploymentFromLabels(o)

	default:
		err = fmt.Errorf("unexpected object received: %+v", o)
	}
	if err != nil {
		if errors.IsNotFound(err) {
			// CSIDriverDeployment may have been deleted, but we still get events from deleted children.
			// They will get garbage collected soon.
			err = nil
		} else {
			glog.Error(err.Error())
			return err
		}
	}

	if instance == nil {
		return nil
	}

	err = h.handleCSIDriverDeployment(instance)
	if err != nil && !errors.IsAlreadyExists(err) {
		glog.Error(err.Error())
		return err
	}

	return nil
}

func (h *Handler) getCSIDriverDeploymentFromOwner(obj runtime.Object) (*v1alpha1.CSIDriverDeployment, error) {
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return nil, fmt.Errorf("cannot get accessor for object: %s", err)
	}
	owners := accessor.GetOwnerReferences()
	for _, owner := range owners {
		if owner.Kind == "CSIDriverDeployment" {
			return h.getCSIDriverDeployment(accessor.GetNamespace(), owner.Name)
		}
	}
	return nil, nil
}

func (h *Handler) getCSIDriverDeploymentFromLabels(obj runtime.Object) (*v1alpha1.CSIDriverDeployment, error) {
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return nil, fmt.Errorf("cannot get accessor for object: %s", err)
	}
	labels := accessor.GetLabels()
	ownerNamespace, ok := labels[ownerLabelNamespace]
	if !ok {
		return nil, nil
	}
	ownerName, ok := labels[ownerLabelName]
	if !ok {
		return nil, nil
	}

	return h.getCSIDriverDeployment(ownerNamespace, ownerName)
}

func (h *Handler) getCSIDriverDeployment(namespace, name string) (*v1alpha1.CSIDriverDeployment, error) {
	instance := &v1alpha1.CSIDriverDeployment{
		TypeMeta: metav1.TypeMeta{
			Kind:       "CSIDriverDeployment",
			APIVersion: v1alpha1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	err := sdk.Get(instance)
	if err != nil {
		return nil, err
	}
	return instance, nil

}
func (h *Handler) handleCSIDriverDeployment(instance *v1alpha1.CSIDriverDeployment) error {
	// Prevent multiple calls from different informers
	h.lock.Lock()
	defer h.lock.Unlock()

	var errs []error
	if instance.DeletionTimestamp != nil {
		// The deployment is being deleted, clean up
		errs, instance = h.cleanupCSIDriverDeployment(instance)
	} else {
		// The deployment was created / updated
		errs, instance = h.syncCSIDriverDeployment(instance)
	}
	if errs != nil {
		// Send errors as events
		for _, e := range errs {
			glog.V(2).Info(e.Error())
			h.recorder.Event(instance, corev1.EventTypeWarning, "SyncError", e.Error())
		}
		return utilerrors.NewAggregate(errs)

	}
	return nil
}

// syncCSIDriverDeployment checks one CSIDriverDeployment and ensures that all "children" objects are either
// created or updated.
func (h *Handler) syncCSIDriverDeployment(cr *v1alpha1.CSIDriverDeployment) ([]error, *v1alpha1.CSIDriverDeployment) {
	glog.V(4).Infof("=== Syncing CSIDriverDeployment %s/%s", cr.Namespace, cr.Name)
	var errs []error

	err, cr := h.syncFinalizer(cr)
	if err != nil {
		// Return now, we can't create subsequent objects without the finalizer because could miss event
		// with CSIDriverDeployment deleletion and we could not delete non-namespaced objects.
		return []error{err}, cr
	}

	serviceAccount, err := h.syncServiceAccount(cr)
	if err != nil {
		err := fmt.Errorf("error syncing ServiceAccount for CSIDriverDeployment %s/%s: %s", cr.Namespace, cr.Name, err)
		errs = append(errs, err)
	}
	err = h.syncClusterRoleBinding(cr, serviceAccount)
	if err != nil {
		err := fmt.Errorf("error syncing ClusterRoleBinding for CSIDriverDeployment %s/%s: %s", cr.Namespace, cr.Name, err)
		errs = append(errs, err)
	}

	err = h.syncLeaderElectionRoleBinding(cr, serviceAccount)
	if err != nil {
		err := fmt.Errorf("error syncing RoleBinding for CSIDriverDeployment %s/%s: %s", cr.Namespace, cr.Name, err)
		errs = append(errs, err)
	}

	expectedStorageClassNames := sets.NewString()
	for i := range cr.Spec.StorageClassTemplates {
		className := cr.Spec.StorageClassTemplates[i].Name
		expectedStorageClassNames.Insert(className)
		err = h.syncStorageClass(cr, &cr.Spec.StorageClassTemplates[i])
		if err != nil {
			err := fmt.Errorf("error syncing StorageClass %s for CSIDriverDeployment %s/%s: %s", className, cr.Namespace, cr.Name, err)
			errs = append(errs, err)
		}
	}
	removeErrs := h.removeUnexpectedStorageClasses(cr, expectedStorageClassNames)
	errs = append(errs, removeErrs...)

	var children []openshiftapi.GenerationHistory

	ds, err := h.syncDaemonSet(cr, serviceAccount)
	if err != nil {
		err := fmt.Errorf("error syncing DaemonSet for CSIDriverDeployment %s/%s: %s", cr.Namespace, cr.Name, err)
		errs = append(errs, err)
	}
	if ds != nil {
		// Store generation of the DaemonSet so we can check for DaemonSet.Spec changes.
		children = append(children, openshiftapi.GenerationHistory{
			Group:          appsv1.GroupName,
			Resource:       "DaemonSet",
			Namespace:      ds.Namespace,
			Name:           ds.Name,
			LastGeneration: ds.Generation,
		})
	}

	deployment, err := h.syncDeployment(cr, serviceAccount)
	if err != nil {
		err := fmt.Errorf("error syncing Deployment for CSIDriverDeployment %s/%s: %s", cr.Namespace, cr.Name, err)
		errs = append(errs, err)
	}
	if deployment != nil {
		// Store generation of the Deployment so we can check for DaemonSet.Spec changes.
		children = append(children, openshiftapi.GenerationHistory{
			Group:          appsv1.GroupName,
			Resource:       "Deployment",
			Namespace:      deployment.Namespace,
			Name:           deployment.Name,
			LastGeneration: deployment.Generation,
		})
	}

	err = h.syncStatus(cr, children)
	if err != nil {
		err := fmt.Errorf("error syncing CSIDriverDeployment.Status for %s/%s: %s", cr.Namespace, cr.Name, err)
		errs = append(errs, err)
	}

	return errs, cr
}

func (h *Handler) syncFinalizer(cr *v1alpha1.CSIDriverDeployment) (error, *v1alpha1.CSIDriverDeployment) {
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
	if err := sdk.Update(newCR); err != nil {
		if errors.IsConflict(err) {
			err = nil
		}
		return err, cr
	}

	return nil, newCR
}

type mergeFunc func(existingObject runtime.Object) (error, bool, runtime.Object)

/*
func (h *Handler) syncObject(expectedObject runtime.Object, merge mergeFunc) (error, runtime.Object) {
	existingObject := expectedObject.DeepCopyObject()
	accessor, err := meta.Accessor(expectedObject)
	if err != nil {
		return err, existingObject
	}

	key := types.NamespacedName{Namespace: accessor.GetNamespace(), Name: accessor.GetName()}
	err = h.Get(context.TODO(), key, existingObject)
	if err != nil {
		// Object does not exist, create it
		glog.V(2).Infof("Creating %T %s/%s", expectedObject, key.Namespace, key.Name)
		err := h.Create(context.TODO(), existingObject)
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
		err := h.Update(context.TODO(), newObject)
		if err != nil && errors.IsConflict(err) {
			err = nil
		}
		return err, newObject
	}

	return err, existingObject
}
*/

func (h *Handler) syncServiceAccount(cr *v1alpha1.CSIDriverDeployment) (*corev1.ServiceAccount, error) {
	glog.V(4).Infof("Syncing ServiceAccount")

	sc := h.generateServiceAccount(cr)

	sc, _, err := resourceapply.ApplyServiceAccount(k8sclient.GetKubeClient().CoreV1(), sc)
	if err != nil {
		return nil, err
	}
	return sc, nil
}

func (h *Handler) syncClusterRoleBinding(cr *v1alpha1.CSIDriverDeployment, serviceAccount *corev1.ServiceAccount) error {
	glog.V(4).Infof("Syncing ClusterRoleBinding")

	crb := h.generateClusterRoleBinding(cr, serviceAccount)

	_, _, err := resourceapply.ApplyClusterRoleBinding(k8sclient.GetKubeClient().RbacV1(), crb)
	return err
}

func (h *Handler) syncLeaderElectionRoleBinding(cr *v1alpha1.CSIDriverDeployment, serviceAccount *corev1.ServiceAccount) error {
	glog.V(4).Infof("Syncing leader election RoleBinding")

	rb := h.generateLeaderElectionRoleBinding(cr, serviceAccount)

	_, _, err := resourceapply.ApplyRoleBinding(k8sclient.GetKubeClient().RbacV1(), rb)
	return err
}

func (h *Handler) syncDaemonSet(cr *v1alpha1.CSIDriverDeployment, sa *corev1.ServiceAccount) (*appsv1.DaemonSet, error) {
	glog.V(4).Infof("Syncing DaemonSet")
	ds := h.generateDaemonSet(cr, sa)
	generation := h.getExpectedGeneration(cr, ds)

	ds, _, err := resourceapply.ApplyDaemonSet(k8sclient.GetKubeClient().AppsV1(), ds, generation, false)
	if err != nil {
		return nil, err
	}
	return ds, nil
}

func (h *Handler) syncDeployment(cr *v1alpha1.CSIDriverDeployment, sa *corev1.ServiceAccount) (*appsv1.Deployment, error) {
	glog.V(4).Infof("Syncing Deployment")
	if cr.Spec.DriverControllerTemplate == nil {
		// TODO: delete existing deployment!
		return nil, nil
	}

	deployment := h.generateDeployment(cr, sa)
	generation := h.getExpectedGeneration(cr, deployment)

	deployment, _, err := resourceapply.ApplyDeployment(k8sclient.GetKubeClient().AppsV1(), deployment, generation, false)
	if err != nil {
		return nil, err
	}
	return deployment, nil
}

func (h *Handler) syncStorageClass(cr *v1alpha1.CSIDriverDeployment, template *v1alpha1.StorageClassTemplate) error {
	glog.V(4).Infof("Syncing StorageClass %s", template.Name)

	sc := h.generateStorageClass(cr, template)
	_, _, err := applyStorageClass(h.kubeClient.StorageV1(), sc)

	return err
}

func (h *Handler) removeUnexpectedStorageClasses(cr *v1alpha1.CSIDriverDeployment, expectedClasses sets.String) []error {
	list, err := h.kubeClient.StorageV1().StorageClasses().List(metav1.ListOptions{LabelSelector: h.getOwnerLabelSelector(cr).String()})
	if err != nil {
		err := fmt.Errorf("cannot list StorageClasses for CSIDriverDeployment %s/%s: %s", cr.Namespace, cr.Name, err)
		return []error{err}
	}

	var errs []error
	for _, sc := range list.Items {
		if !expectedClasses.Has(sc.Name) {
			glog.V(4).Infof("Deleting StorageClass %s", sc.Name)
			if err := h.kubeClient.StorageV1().StorageClasses().Delete(sc.Name, nil); err != nil {
				if !errors.IsNotFound(err) {
					err := fmt.Errorf("cannot delete StorageClasses %s for CSIDriverDeployment %s/%s: %s", sc.Name, cr.Namespace, cr.Name, err)
					errs = append(errs, err)
				}
			}
		}
	}
	return errs
}

func (h *Handler) syncStatus(cr *v1alpha1.CSIDriverDeployment, children []openshiftapi.GenerationHistory) error {
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
		err := sdk.Update(newInstance)
		if err != nil && errors.IsConflict(err) {
			err = nil
		}
		return err
	}
	return nil
}

// cleanupCSIDriverDeployment removes non-namespaced objects owned by the CSIDriverDeployment.
// ObjectMeta.OwnerReference does not work for them.
func (h *Handler) cleanupCSIDriverDeployment(cr *v1alpha1.CSIDriverDeployment) ([]error, *v1alpha1.CSIDriverDeployment) {
	glog.V(4).Infof("=== Cleaning up CSIDriverDeployment %s/%s", cr.Namespace, cr.Name)

	errs := h.cleanupStorageClasses(cr)
	if err := h.cleanupClusterRoleBinding(cr); err != nil {
		errs = append(errs, err)
	}

	if len(errs) != 0 {
		// Don't remove the finalizer yet, there is still stuff to clean up
		return errs, cr
	}

	// Remove the finalizer as the last step
	err, newCR := h.cleanupFinalizer(cr)
	if err != nil {
		return []error{err}, cr
	}
	return nil, newCR
}

func (h *Handler) cleanupFinalizer(cr *v1alpha1.CSIDriverDeployment) (error, *v1alpha1.CSIDriverDeployment) {
	newCR := cr.DeepCopy()
	newCR.Finalizers = []string{}
	for _, f := range cr.Finalizers {
		if f == finalizerName {
			continue
		}
		newCR.Finalizers = append(newCR.Finalizers, f)
	}

	glog.V(4).Infof("Removing CSIDriverDeployment.Finalizers")
	err := sdk.Update(newCR)
	if err != nil {
		if errors.IsConflict(err) {
			err = nil
		}
		return err, cr
	}
	return nil, newCR
}

func (h *Handler) cleanupClusterRoleBinding(cr *v1alpha1.CSIDriverDeployment) error {
	sa := h.generateServiceAccount(cr)
	crb := h.generateClusterRoleBinding(cr, sa)
	err := h.kubeClient.RbacV1().ClusterRoleBindings().Delete(crb.Name, nil)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	glog.V(4).Infof("Deleted ClusterRoleBinding %s", crb.Name)
	return nil
}

func (h *Handler) cleanupStorageClasses(cr *v1alpha1.CSIDriverDeployment) []error {
	return h.removeUnexpectedStorageClasses(cr, sets.NewString())
}

func hasFinalizer(finalizers []string, finalizerName string) bool {
	for _, f := range finalizers {
		if f == finalizerName {
			return true
		}
	}
	return false
}

func (h *Handler) getExpectedGeneration(cr *v1alpha1.CSIDriverDeployment, obj runtime.Object) int64 {
	gvk := obj.GetObjectKind().GroupVersionKind()
	for _, child := range cr.Status.Children {
		if child.Group != gvk.Group || child.Resource != gvk.Kind {
			continue
		}
		accessor, err := meta.Accessor(obj)
		if err != nil {
			return -1
		}
		if child.Name != accessor.GetName() || child.Namespace != accessor.GetNamespace() {
			continue
		}
		return child.LastGeneration
	}
	return -1
}

func (h *Handler) getOwnerLabelSelector(i *v1alpha1.CSIDriverDeployment) labels.Selector {
	ls := labels.Set{
		ownerLabelNamespace: i.Namespace,
		ownerLabelName:      i.Name,
	}
	return labels.SelectorFromSet(ls)
}
