package csidriverdeployment

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/golang/glog"
	openshiftapi "github.com/openshift/api/operator/v1alpha1"
	csidriverv1alpha1 "github.com/openshift/csi-operator/pkg/apis/csidriver/v1alpha1"
	"github.com/openshift/csi-operator/pkg/config"
	"github.com/openshift/csi-operator/pkg/resourceapply"
	"github.com/openshift/library-go/pkg/operator/v1alpha1helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
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
	finalizerName = "csidriver.storage.openshift.io"
	apiTimeout    = time.Minute
)

// Add creates a new CSIDriverDeployment Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager, config *config.Config) error {
	return add(mgr, newReconciler(mgr, config))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, config *config.Config) reconcile.Reconciler {
	return &ReconcileCSIDriverDeployment{
		client:   mgr.GetClient(),
		recorder: mgr.GetRecorder("csi-operator"),
		config:   config,
	}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("csidriverdeployment-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource CSIDriverDeployment
	err = c.Watch(&source.Kind{Type: &csidriverv1alpha1.CSIDriverDeployment{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &appsv1.Deployment{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &csidriverv1alpha1.CSIDriverDeployment{},
	})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &appsv1.DaemonSet{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &csidriverv1alpha1.CSIDriverDeployment{},
	})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &corev1.ServiceAccount{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &csidriverv1alpha1.CSIDriverDeployment{},
	})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &rbacv1.RoleBinding{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &csidriverv1alpha1.CSIDriverDeployment{},
	})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &rbacv1.ClusterRoleBinding{}}, &EnqueueRequestForLabels{})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &storagev1.StorageClass{}}, &EnqueueRequestForLabels{})
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileCSIDriverDeployment{}

// ReconcileCSIDriverDeployment reconciles a CSIDriverDeployment object
type ReconcileCSIDriverDeployment struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client client.Client
	scheme *runtime.Scheme

	config   *config.Config
	recorder record.EventRecorder
}

// Reconcile reads that state of the cluster for a CSIDriverDeployment object and makes changes based on the state read
// and what is in the CSIDriverDeployment.Spec
func (r *ReconcileCSIDriverDeployment) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	glog.V(3).Infof("Reconciling CSIDriverDeployment %s/%s\n", request.Namespace, request.Name)

	// Fetch the CSIDriverDeployment instance
	instance := &csidriverv1alpha1.CSIDriverDeployment{}
	ctx, cancel := r.apiContext()
	defer cancel()
	err := r.client.Get(ctx, request.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		glog.Warningf("failed to get %v: %v", request.NamespacedName, err)
		r.recorder.Event(instance, corev1.EventTypeWarning, "SyncError", err.Error())
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, r.handleCSIDriverDeployment(instance)
}

func (r *ReconcileCSIDriverDeployment) handleCSIDriverDeployment(instance *csidriverv1alpha1.CSIDriverDeployment) error {
	var errs []error
	newInstance := instance.DeepCopy()
	r.applyDefaults(newInstance)

	if instance.Spec.ManagementState == openshiftapi.Unmanaged {
		glog.V(2).Infof("CSIDriverDeployment %s/%s is Unmanaged, skipping", instance.Namespace, instance.Name)
		newInstance.Status.State = instance.Spec.ManagementState
	} else {

		// Instance is Managed, do something about it

		if newInstance.DeletionTimestamp != nil {
			// The deployment is being deleted, clean up.
			// Allow deletion without validation.
			newInstance.Status.State = openshiftapi.Removed
			newInstance, errs = r.cleanupCSIDriverDeployment(newInstance)

		} else {
			// The deployment was created / updated
			newInstance.Status.State = openshiftapi.Managed
			validationErrs := r.validateCSIDriverDeployment(newInstance)
			if len(validationErrs) > 0 {
				for _, err := range validationErrs {
					errs = append(errs, err)
				}
				// Store errors in status.conditions.
				r.syncConditions(newInstance, nil, nil, errs)
			} else {
				// Sync the CSIDriverDeployment only when validation passed.
				newInstance, errs = r.syncCSIDriverDeployment(newInstance)
			}
		}
		if errs != nil {
			// Send errors as events
			for _, e := range errs {
				glog.Warning(e.Error())
				if !errors.IsConflict(e) {
					r.recorder.Event(newInstance, corev1.EventTypeWarning, "SyncError", e.Error())
				}
			}
		}
	}

	err := r.syncStatus(instance, newInstance)
	if err != nil {
		// This error has not been logged above
		glog.Warning(err.Error())
		if !errors.IsConflict(err) {
			r.recorder.Event(newInstance, corev1.EventTypeWarning, "SyncError", err.Error())
		}
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return utilerrors.NewAggregate(errs)
	}

	return nil
}

// syncCSIDriverDeployment checks one CSIDriverDeployment and ensures that all "children" objects are either
// created or updated.
func (r *ReconcileCSIDriverDeployment) syncCSIDriverDeployment(cr *csidriverv1alpha1.CSIDriverDeployment) (*csidriverv1alpha1.CSIDriverDeployment, []error) {
	glog.V(2).Infof("=== Syncing CSIDriverDeployment %s/%s", cr.Namespace, cr.Name)
	var errs []error

	cr, err := r.syncFinalizer(cr)
	if err != nil {
		// Return now, we can't create subsequent objects without the finalizer because could miss event
		// with CSIDriverDeployment deletion and we could not delete non-namespaced objects.
		return cr, []error{err}
	}

	serviceAccount, err := r.syncServiceAccount(cr)
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

	var children []openshiftapi.GenerationHistory

	// There is no easy way how to detect change of DriverControllerTemplate or DriverPerNodeTemplate.
	// Assume that every change of CR generation changed the templates.
	generationChanged := cr.Status.ObservedGeneration == nil || cr.Generation != *cr.Status.ObservedGeneration

	ds, err := r.syncDaemonSet(cr, serviceAccount, generationChanged)
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

	deployment, err := r.syncDeployment(cr, serviceAccount, generationChanged)
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
	r.syncConditions(cr, deployment, ds, errs)

	return cr, errs
}

func (r *ReconcileCSIDriverDeployment) syncFinalizer(cr *csidriverv1alpha1.CSIDriverDeployment) (*csidriverv1alpha1.CSIDriverDeployment, error) {
	glog.V(4).Infof("Syncing CSIDriverDeployment.Finalizers")

	if hasFinalizer(cr.Finalizers, finalizerName) {
		return cr, nil
	}

	newCR := cr.DeepCopy()
	if newCR.Finalizers == nil {
		newCR.Finalizers = []string{}
	}
	newCR.Finalizers = append(newCR.Finalizers, finalizerName)

	ctx, cancel := r.apiContext()
	defer cancel()
	glog.V(4).Infof("Updating CSIDriverDeployment.Finalizers")
	if err := r.client.Update(ctx, newCR); err != nil {
		return cr, err
	}

	return newCR, nil
}

func (r *ReconcileCSIDriverDeployment) syncServiceAccount(cr *csidriverv1alpha1.CSIDriverDeployment) (*corev1.ServiceAccount, error) {
	glog.V(4).Infof("Syncing ServiceAccount")

	sc := r.generateServiceAccount(cr)

	ctx, cancel := r.apiContext()
	defer cancel()
	newSC, _, err := resourceapply.ApplyServiceAccount(ctx, r.client, sc)
	if err != nil {
		// Return the SC even on error, lot of subsequent children depend on it.
		return sc, err
	}
	return newSC, nil
}

func (r *ReconcileCSIDriverDeployment) syncClusterRoleBinding(cr *csidriverv1alpha1.CSIDriverDeployment, serviceAccount *corev1.ServiceAccount) error {
	glog.V(4).Infof("Syncing ClusterRoleBinding")

	crb := r.generateClusterRoleBinding(cr, serviceAccount)

	ctx, cancel := r.apiContext()
	defer cancel()
	_, _, err := resourceapply.ApplyClusterRoleBinding(ctx, r.client, crb)
	return err
}

func (r *ReconcileCSIDriverDeployment) syncLeaderElectionRoleBinding(cr *csidriverv1alpha1.CSIDriverDeployment, serviceAccount *corev1.ServiceAccount) error {
	glog.V(4).Infof("Syncing leader election RoleBinding")

	rb := r.generateLeaderElectionRoleBinding(cr, serviceAccount)

	ctx, cancel := r.apiContext()
	defer cancel()
	_, _, err := resourceapply.ApplyRoleBinding(ctx, r.client, rb)
	return err
}

func (r *ReconcileCSIDriverDeployment) syncDaemonSet(cr *csidriverv1alpha1.CSIDriverDeployment, sa *corev1.ServiceAccount, templateChanged bool) (*appsv1.DaemonSet, error) {
	glog.V(4).Infof("Syncing DaemonSet")
	requiredDS := r.generateDaemonSet(cr, sa)
	gvk := appsv1.SchemeGroupVersion.WithKind("DaemonSet")
	generation := r.getExpectedGeneration(cr, requiredDS, gvk)

	ctx, cancel := r.apiContext()
	defer cancel()
	ds, _, err := resourceapply.ApplyDaemonSet(ctx, r.client, requiredDS, generation, templateChanged)
	if err != nil {
		return requiredDS, err
	}
	return ds, nil
}

func (r *ReconcileCSIDriverDeployment) syncDeployment(cr *csidriverv1alpha1.CSIDriverDeployment, sa *corev1.ServiceAccount, templateChanged bool) (*appsv1.Deployment, error) {
	glog.V(4).Infof("Syncing Deployment")
	if cr.Spec.DriverControllerTemplate == nil {
		// TODO: delete existing deployment!
		return nil, nil
	}

	requiredDeployment := r.generateDeployment(cr, sa)
	gvk := appsv1.SchemeGroupVersion.WithKind("Deployment")
	generation := r.getExpectedGeneration(cr, requiredDeployment, gvk)

	ctx, cancel := r.apiContext()
	defer cancel()
	deployment, _, err := resourceapply.ApplyDeployment(ctx, r.client, requiredDeployment, generation, templateChanged)
	if err != nil {
		return requiredDeployment, err
	}
	return deployment, nil
}

func (r *ReconcileCSIDriverDeployment) syncStorageClass(cr *csidriverv1alpha1.CSIDriverDeployment, template *csidriverv1alpha1.StorageClassTemplate) error {
	glog.V(4).Infof("Syncing StorageClass %s", template.Name)

	sc := r.generateStorageClass(cr, template)
	ctx, cancel := r.apiContext()
	defer cancel()
	_, _, err := resourceapply.ApplyStorageClass(ctx, r.client, sc)

	return err
}

func (r *ReconcileCSIDriverDeployment) removeUnexpectedStorageClasses(cr *csidriverv1alpha1.CSIDriverDeployment, expectedClasses sets.String) []error {
	list := &storagev1.StorageClassList{}
	opts := client.ListOptions{
		LabelSelector: r.getOwnerLabelSelector(cr),
	}
	ctx, cancel := r.apiContext()
	defer cancel()
	err := r.client.List(ctx, &opts, list)
	if err != nil {
		err := fmt.Errorf("cannot list StorageClasses for CSIDriverDeployment %s/%s: %s", cr.Namespace, cr.Name, err)
		return []error{err}
	}

	var errs []error
	for _, sc := range list.Items {
		if !expectedClasses.Has(sc.Name) {
			glog.V(4).Infof("Deleting StorageClass %s", sc.Name)
			ctx, cancel := r.apiContext()
			defer cancel()
			if err := r.client.Delete(ctx, &sc); err != nil {
				if !errors.IsNotFound(err) {
					err := fmt.Errorf("cannot delete StorageClasses %s for CSIDriverDeployment %s/%s: %s", sc.Name, cr.Namespace, cr.Name, err)
					errs = append(errs, err)
				}
			}
		}
	}
	return errs
}

func (r *ReconcileCSIDriverDeployment) syncConditions(instance *csidriverv1alpha1.CSIDriverDeployment, deployment *appsv1.Deployment, ds *appsv1.DaemonSet, errs []error) {
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
			msgs = append(msgs, fmt.Sprintf("Deployment %q with CSI driver has %d not ready pod(s).", deployment.Name, deployment.Status.UnavailableReplicas))
		}
	} else {
		unknown = true
	}
	if ds != nil {
		if ds.Status.NumberUnavailable > 0 {
			available = false
			msgs = append(msgs, fmt.Sprintf("DaemonSet %q with CSI driver has %d not ready pod(s).", ds.Name, ds.Status.NumberUnavailable))
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

func (r *ReconcileCSIDriverDeployment) syncStatus(oldInstance, newInstance *csidriverv1alpha1.CSIDriverDeployment) error {
	glog.V(4).Info("Syncing CSIDriverDeployment.Status")

	if !equality.Semantic.DeepEqual(oldInstance.Status, newInstance.Status) {
		glog.V(4).Info("Updating CSIDriverDeployment.Status")
		ctx, cancel := r.apiContext()
		defer cancel()
		err := r.client.Status().Update(ctx, newInstance)
		return err
	}
	return nil
}

// cleanupCSIDriverDeployment removes non-namespaced objects owned by the CSIDriverDeployment.
// ObjectMeta.OwnerReference does not work for them.
func (r *ReconcileCSIDriverDeployment) cleanupCSIDriverDeployment(cr *csidriverv1alpha1.CSIDriverDeployment) (*csidriverv1alpha1.CSIDriverDeployment, []error) {
	glog.V(2).Infof("=== Cleaning up CSIDriverDeployment %s/%s", cr.Namespace, cr.Name)

	errs := r.cleanupStorageClasses(cr)
	if err := r.cleanupClusterRoleBinding(cr); err != nil {
		errs = append(errs, err)
	}

	if len(errs) != 0 {
		// Don't remove the finalizer yet, there is still stuff to clean up
		return cr, errs
	}

	// Remove the finalizer as the last step
	newCR, err := r.cleanupFinalizer(cr)
	if err != nil {
		return cr, []error{err}
	}
	return newCR, nil
}

func (r *ReconcileCSIDriverDeployment) cleanupFinalizer(cr *csidriverv1alpha1.CSIDriverDeployment) (*csidriverv1alpha1.CSIDriverDeployment, error) {
	newCR := cr.DeepCopy()
	newCR.Finalizers = []string{}
	for _, f := range cr.Finalizers {
		if f == finalizerName {
			continue
		}
		newCR.Finalizers = append(newCR.Finalizers, f)
	}

	glog.V(4).Infof("Removing CSIDriverDeployment.Finalizers")
	ctx, cancel := r.apiContext()
	defer cancel()
	err := r.client.Update(ctx, newCR)
	if err != nil {
		return cr, err
	}
	return newCR, nil
}

func (r *ReconcileCSIDriverDeployment) cleanupClusterRoleBinding(cr *csidriverv1alpha1.CSIDriverDeployment) error {
	sa := r.generateServiceAccount(cr)
	crb := r.generateClusterRoleBinding(cr, sa)
	ctx, cancel := r.apiContext()
	defer cancel()
	err := r.client.Delete(ctx, crb)
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

func hasFinalizer(finalizers []string, finalizerName string) bool {
	for _, f := range finalizers {
		if f == finalizerName {
			return true
		}
	}
	return false
}

func (r *ReconcileCSIDriverDeployment) getExpectedGeneration(cr *csidriverv1alpha1.CSIDriverDeployment, obj runtime.Object, gvk schema.GroupVersionKind) int64 {
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

func (r *ReconcileCSIDriverDeployment) getOwnerLabelSelector(i *csidriverv1alpha1.CSIDriverDeployment) labels.Selector {
	ls := labels.Set{
		OwnerLabelNamespace: i.Namespace,
		OwnerLabelName:      i.Name,
	}
	return labels.SelectorFromSet(ls)
}

func (r *ReconcileCSIDriverDeployment) apiContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), apiTimeout)
}
