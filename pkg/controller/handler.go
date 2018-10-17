package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/golang/glog"
	openshiftapi "github.com/openshift/api/operator/v1alpha1"
	"github.com/openshift/csi-operator/pkg/apis/csidriver/v1alpha1"
	"github.com/openshift/csi-operator/pkg/config"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"github.com/openshift/library-go/pkg/operator/v1alpha1helpers"
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
	// OwnerLabelNamespace is name of label with namespace of owner CSIDriverDeployment.
	OwnerLabelNamespace = "csidriver.storage.openshift.io/owner-namespace"
	// OwnerLabelName is name of label with name of owner CSIDriverDeployment.
	OwnerLabelName = "csidriver.storage.openshift.io/owner-name"

	finalizerName = "csidriver.storage.openshift.io"
)

// NewHandler constructs a new CSIDriverDeployment handler.
func NewHandler(cfg *config.Config) (sdk.Handler, error) {
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

// Handler is SDK handler for CSIDriverDeployment objects.
type Handler struct {
	kubeClient kubernetes.Interface
	csiClient  csiclientset.Interface
	recorder   record.EventRecorder
	config     *config.Config

	lock sync.Mutex
}

// Handle processes an event on an API object. It translates every event to CSIDriverDeployment event.
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
		if owner.Kind == v1alpha1.CSIDriverDeploymentKind {
			return h.getCSIDriverDeployment(accessor.GetNamespace(), owner.Name)
		}
	}
	glog.V(4).Infof("Ignoring event on %s %s/%s: not owned by any CSIDriverDeployment", obj.GetObjectKind().GroupVersionKind().Kind, accessor.GetNamespace(), accessor.GetName())
	return nil, nil
}

func (h *Handler) getCSIDriverDeploymentFromLabels(obj runtime.Object) (*v1alpha1.CSIDriverDeployment, error) {
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return nil, fmt.Errorf("cannot get accessor for object: %s", err)
	}
	labels := accessor.GetLabels()
	ownerNamespace, ok := labels[OwnerLabelNamespace]
	if !ok {
		return nil, nil
	}
	ownerName, ok := labels[OwnerLabelName]
	if !ok {
		glog.V(4).Infof("Ignoring event on %s %s/%s: not labelled by any CSIDriverDeployment", obj.GetObjectKind().GroupVersionKind().Kind, accessor.GetNamespace(), accessor.GetName())
		return nil, nil
	}

	return h.getCSIDriverDeployment(ownerNamespace, ownerName)
}

func (h *Handler) getCSIDriverDeployment(namespace, name string) (*v1alpha1.CSIDriverDeployment, error) {
	instance := &v1alpha1.CSIDriverDeployment{
		TypeMeta: metav1.TypeMeta{
			Kind:       v1alpha1.CSIDriverDeploymentKind,
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
	newInstance := instance.DeepCopy()
	h.applyDefaults(newInstance)

	if instance.Spec.ManagementState == openshiftapi.Unmanaged {
		glog.V(2).Infof("CSIDriverDeployment %s/%s is Unmanaged, skipping", instance.Namespace, instance.Name)
		newInstance.Status.State = instance.Spec.ManagementState
	} else {

		// Instance is Managed, do something about it

		if newInstance.DeletionTimestamp != nil {
			// The deployment is being deleted, clean up.
			// Allow deletion without validation.
			newInstance.Status.State = openshiftapi.Removed
			newInstance, errs = h.cleanupCSIDriverDeployment(newInstance)

		} else {
			// The deployment was created / updated
			newInstance.Status.State = openshiftapi.Managed
			validationErrs := h.validateCSIDriverDeployment(newInstance)
			if len(validationErrs) > 0 {
				for _, err := range validationErrs {
					errs = append(errs, err)
				}
				// Store errors in status.conditions.
				h.syncConditions(newInstance, nil, nil, errs)
			} else {
				// Sync the CSIDriverDeployment only when validation passed.
				newInstance, errs = h.syncCSIDriverDeployment(newInstance)
			}
		}
		if errs != nil {
			// Send errors as events
			for _, e := range errs {
				glog.V(2).Info(e.Error())
				h.recorder.Event(newInstance, corev1.EventTypeWarning, "SyncError", e.Error())
			}
		}
	}

	err := h.syncStatus(instance, newInstance)
	if err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return utilerrors.NewAggregate(errs)
	}

	return nil
}

// syncCSIDriverDeployment checks one CSIDriverDeployment and ensures that all "children" objects are either
// created or updated.
func (h *Handler) syncCSIDriverDeployment(cr *v1alpha1.CSIDriverDeployment) (*v1alpha1.CSIDriverDeployment, []error) {
	glog.V(4).Infof("=== Syncing CSIDriverDeployment %s/%s", cr.Namespace, cr.Name)
	var errs []error

	cr, err := h.syncFinalizer(cr)
	if err != nil {
		// Return now, we can't create subsequent objects without the finalizer because could miss event
		// with CSIDriverDeployment deletion and we could not delete non-namespaced objects.
		return cr, []error{err}
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

	cr.Status.Children = children
	if len(errs) == 0 {
		cr.Status.ObservedGeneration = &cr.Generation
	}
	h.syncConditions(cr, deployment, ds, errs)

	return cr, errs
}

func (h *Handler) syncFinalizer(cr *v1alpha1.CSIDriverDeployment) (*v1alpha1.CSIDriverDeployment, error) {
	glog.V(4).Infof("Syncing CSIDriverDeployment.Finalizers")

	if hasFinalizer(cr.Finalizers, finalizerName) {
		return cr, nil
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
		return cr, err
	}

	return newCR, nil
}

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
	requiredDS := h.generateDaemonSet(cr, sa)
	generation := h.getExpectedGeneration(cr, requiredDS)

	ds, _, err := resourceapply.ApplyDaemonSet(k8sclient.GetKubeClient().AppsV1(), requiredDS, generation, false)
	if err != nil {
		return requiredDS, err
	}
	return ds, nil
}

func (h *Handler) syncDeployment(cr *v1alpha1.CSIDriverDeployment, sa *corev1.ServiceAccount) (*appsv1.Deployment, error) {
	glog.V(4).Infof("Syncing Deployment")
	if cr.Spec.DriverControllerTemplate == nil {
		// TODO: delete existing deployment!
		return nil, nil
	}

	requiredDeployment := h.generateDeployment(cr, sa)
	generation := h.getExpectedGeneration(cr, requiredDeployment)

	deployment, _, err := resourceapply.ApplyDeployment(k8sclient.GetKubeClient().AppsV1(), requiredDeployment, generation, false)
	if err != nil {
		return requiredDeployment, err
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

func (h *Handler) syncConditions(instance *v1alpha1.CSIDriverDeployment, deployment *appsv1.Deployment, ds *appsv1.DaemonSet, errs []error) {
	// OperatorStatusTypeAvailable condition: true if both Deployment and DaemonSet are fully deployed.
	availableCondition := openshiftapi.OperatorCondition{
		Type: openshiftapi.OperatorStatusTypeAvailable,
	}
	available := true
	unknown := false
	msgs := []string{}
	if deployment != nil {
		if deployment.Status.UnavailableReplicas > 0 {
			available = false
			msgs = append(msgs, fmt.Sprintf("Deployment %q with CSI driver has still %d not ready pod(s).", deployment.Name, deployment.Status.UnavailableReplicas))
		}
	} else {
		unknown = true
	}
	if ds != nil {
		if ds.Status.NumberUnavailable > 0 {
			available = false
		}
	} else {
		unknown = true
	}

	switch {
	case unknown:
		availableCondition.Status = openshiftapi.ConditionUnknown
	case available:
		availableCondition.Status = openshiftapi.ConditionTrue
	default:
		availableCondition.Status = openshiftapi.ConditionFalse
	}
	availableCondition.Message = strings.Join(msgs, "\n")
	v1alpha1helpers.SetOperatorCondition(&instance.Status.Conditions, availableCondition)

	// OperatorStatusTypeSyncSuccessful condition: true if no error happened during sync.
	syncSuccessfulCondition := openshiftapi.OperatorCondition{
		Type:    openshiftapi.OperatorStatusTypeSyncSuccessful,
		Status:  openshiftapi.ConditionTrue,
		Message: "",
	}
	if len(errs) > 0 {
		syncSuccessfulCondition.Status = openshiftapi.ConditionFalse
		errStrings := make([]string, len(errs))
		for i := range errs {
			errStrings[i] = errs[i].Error()
		}
		syncSuccessfulCondition.Message = strings.Join(errStrings, "\n")
	}
	v1alpha1helpers.SetOperatorCondition(&instance.Status.Conditions, syncSuccessfulCondition)
}

func (h *Handler) syncStatus(oldInstance, newInstance *v1alpha1.CSIDriverDeployment) error {
	glog.V(4).Info("Syncing CSIDriverDeployment.Status")

	if !equality.Semantic.DeepEqual(oldInstance.Status, newInstance.Status) {
		glog.V(4).Info("Updating CSIDriverDeployment.Status")
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
func (h *Handler) cleanupCSIDriverDeployment(cr *v1alpha1.CSIDriverDeployment) (*v1alpha1.CSIDriverDeployment, []error) {
	glog.V(4).Infof("=== Cleaning up CSIDriverDeployment %s/%s", cr.Namespace, cr.Name)

	errs := h.cleanupStorageClasses(cr)
	if err := h.cleanupClusterRoleBinding(cr); err != nil {
		errs = append(errs, err)
	}

	if len(errs) != 0 {
		// Don't remove the finalizer yet, there is still stuff to clean up
		return cr, errs
	}

	// Remove the finalizer as the last step
	newCR, err := h.cleanupFinalizer(cr)
	if err != nil {
		return cr, []error{err}
	}
	return newCR, nil
}

func (h *Handler) cleanupFinalizer(cr *v1alpha1.CSIDriverDeployment) (*v1alpha1.CSIDriverDeployment, error) {
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
		return cr, err
	}
	return newCR, nil
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
		OwnerLabelNamespace: i.Namespace,
		OwnerLabelName:      i.Name,
	}
	return labels.SelectorFromSet(ls)
}
