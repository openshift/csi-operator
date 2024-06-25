package staticresource

import (
	"context"
	"time"

	opv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/management"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	operatorv1helpers "github.com/openshift/library-go/pkg/operator/v1helpers"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

type SyncObjects struct {
	CSIDriver      *storagev1.CSIDriver
	PrivilegedRole *rbacv1.ClusterRole
	CAConfigMap    *corev1.ConfigMap

	NodeServiceAccount *corev1.ServiceAccount
	NodeRoleBinding    *rbacv1.ClusterRoleBinding

	ControllerServiceAccount *corev1.ServiceAccount
	ControllerRoleBinding    *rbacv1.ClusterRoleBinding
	ProvisionerRoleBinding   *rbacv1.ClusterRoleBinding

	PrometheusRole        *rbacv1.Role
	PrometheusRoleBinding *rbacv1.RoleBinding
	MetricsService        *corev1.Service
	RBACProxyRole         *rbacv1.ClusterRole
	RBACProxyRoleBinding  *rbacv1.ClusterRoleBinding

	LeaseLeaderElectionRole        *rbacv1.Role
	LeaseLeaderElectionRoleBinding *rbacv1.RoleBinding
}

// CSIStaticResourceController creates, manages and deletes static resources of a CSI driver, such as RBAC rules.
// It's more hardcoded variant of library-go's StaticResourceController, which does not implement removal
// of objects yet.
type CSIStaticResourceController struct {
	operatorName      string
	operatorNamespace string
	operatorClient    operatorv1helpers.OperatorClientWithFinalizers
	kubeClient        kubernetes.Interface
	eventRecorder     events.Recorder
	objs              SyncObjects
}

func NewCSIStaticResourceController(
	name string,
	operatorNamespace string,
	operatorClient operatorv1helpers.OperatorClientWithFinalizers,
	kubeClient kubernetes.Interface,
	informers operatorv1helpers.KubeInformersForNamespaces,
	recorder events.Recorder,
	objs SyncObjects,
) factory.Controller {
	c := &CSIStaticResourceController{
		operatorName:      name,
		operatorNamespace: operatorNamespace,
		operatorClient:    operatorClient,
		kubeClient:        kubeClient,
		eventRecorder:     recorder,
		objs:              objs,
	}

	operatorInformers := []factory.Informer{
		operatorClient.Informer(),
		informers.InformersFor(operatorNamespace).Core().V1().ServiceAccounts().Informer(),
		informers.InformersFor(operatorNamespace).Storage().V1().CSIDrivers().Informer(),
		informers.InformersFor(operatorNamespace).Rbac().V1().ClusterRoles().Informer(),
		informers.InformersFor(operatorNamespace).Rbac().V1().ClusterRoleBindings().Informer(),
		informers.InformersFor(operatorNamespace).Rbac().V1().Roles().Informer(),
		informers.InformersFor(operatorNamespace).Rbac().V1().RoleBindings().Informer(),
		informers.InformersFor(operatorNamespace).Core().V1().Services().Informer(),
		informers.InformersFor(operatorNamespace).Core().V1().ConfigMaps().Informer(),
	}
	return factory.New().
		WithSyncDegradedOnError(operatorClient).
		WithInformers(operatorInformers...).
		WithSync(c.sync).
		ResyncEvery(time.Minute).
		ToController(name, recorder.WithComponentSuffix("csi-static-resource-controller"))
}

func (c *CSIStaticResourceController) sync(ctx context.Context, controllerContext factory.SyncContext) error {
	opSpec, opStatus, _, err := c.operatorClient.GetOperatorState()
	if apierrors.IsNotFound(err) {
		// TODO: proceed with removal?
		return nil
	}
	if err != nil {
		return err
	}

	if opSpec.ManagementState != opv1.Managed {
		return nil
	}

	meta, err := c.operatorClient.GetObjectMeta()
	if err != nil {
		return err
	}
	if management.IsOperatorRemovable() && meta.DeletionTimestamp != nil {
		return c.syncDeleting(ctx, opSpec, opStatus, controllerContext)
	}
	return c.syncManaged(ctx, opSpec, opStatus, controllerContext)
}

func (c *CSIStaticResourceController) syncManaged(ctx context.Context, opSpec *opv1.OperatorSpec, opStatus *opv1.OperatorStatus, controllerContext factory.SyncContext) error {
	err := operatorv1helpers.EnsureFinalizer(ctx, c.operatorClient, c.operatorName)
	if err != nil {
		return err
	}

	var errs []error
	// Common
	_, _, err = resourceapply.ApplyCSIDriver(ctx, c.kubeClient.StorageV1(), c.eventRecorder, c.objs.CSIDriver)
	if err != nil {
		errs = append(errs, err)
	}
	_, _, err = resourceapply.ApplyClusterRole(ctx, c.kubeClient.RbacV1(), c.eventRecorder, c.objs.PrivilegedRole)
	if err != nil {
		errs = append(errs, err)
	}
	_, _, err = resourceapply.ApplyConfigMap(ctx, c.kubeClient.CoreV1(), c.eventRecorder, c.objs.CAConfigMap)
	if err != nil {
		errs = append(errs, err)
	}

	// Node
	_, _, err = resourceapply.ApplyServiceAccount(ctx, c.kubeClient.CoreV1(), c.eventRecorder, c.objs.NodeServiceAccount)
	if err != nil {
		errs = append(errs, err)
	}
	_, _, err = resourceapply.ApplyClusterRoleBinding(ctx, c.kubeClient.RbacV1(), c.eventRecorder, c.objs.NodeRoleBinding)
	if err != nil {
		errs = append(errs, err)
	}

	// Controller
	_, _, err = resourceapply.ApplyServiceAccount(ctx, c.kubeClient.CoreV1(), c.eventRecorder, c.objs.ControllerServiceAccount)
	if err != nil {
		errs = append(errs, err)
	}
	_, _, err = resourceapply.ApplyClusterRoleBinding(ctx, c.kubeClient.RbacV1(), c.eventRecorder, c.objs.ControllerRoleBinding)
	if err != nil {
		errs = append(errs, err)
	}
	_, _, err = resourceapply.ApplyClusterRoleBinding(ctx, c.kubeClient.RbacV1(), c.eventRecorder, c.objs.ProvisionerRoleBinding)
	if err != nil {
		errs = append(errs, err)
	}
	_, _, err = resourceapply.ApplyRole(ctx, c.kubeClient.RbacV1(), c.eventRecorder, c.objs.LeaseLeaderElectionRole)
	if err != nil {
		errs = append(errs, err)
	}
	_, _, err = resourceapply.ApplyRoleBinding(ctx, c.kubeClient.RbacV1(), c.eventRecorder, c.objs.LeaseLeaderElectionRoleBinding)
	if err != nil {
		errs = append(errs, err)
	}

	// Metrics
	_, _, err = resourceapply.ApplyRole(ctx, c.kubeClient.RbacV1(), c.eventRecorder, c.objs.PrometheusRole)
	if err != nil {
		errs = append(errs, err)
	}
	_, _, err = resourceapply.ApplyRoleBinding(ctx, c.kubeClient.RbacV1(), c.eventRecorder, c.objs.PrometheusRoleBinding)
	if err != nil {
		errs = append(errs, err)
	}
	_, _, err = resourceapply.ApplyService(ctx, c.kubeClient.CoreV1(), c.eventRecorder, c.objs.MetricsService)
	if err != nil {
		errs = append(errs, err)
	}
	_, _, err = resourceapply.ApplyClusterRole(ctx, c.kubeClient.RbacV1(), c.eventRecorder, c.objs.RBACProxyRole)
	if err != nil {
		errs = append(errs, err)
	}
	_, _, err = resourceapply.ApplyClusterRoleBinding(ctx, c.kubeClient.RbacV1(), c.eventRecorder, c.objs.RBACProxyRoleBinding)
	if err != nil {
		errs = append(errs, err)
	}

	return errors.NewAggregate(errs)
}

func (c *CSIStaticResourceController) syncDeleting(ctx context.Context, opSpec *opv1.OperatorSpec, opStatus *opv1.OperatorStatus, controllerContext factory.SyncContext) error {
	var errs []error

	// Common
	if err := c.kubeClient.StorageV1().CSIDrivers().Delete(ctx, c.objs.CSIDriver.Name, metav1.DeleteOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			errs = append(errs, err)
		} else {
			klog.V(4).Infof("CSIDriver %s already removed", c.objs.CSIDriver.Name)
		}
	}

	if err := c.kubeClient.RbacV1().ClusterRoles().Delete(ctx, c.objs.PrivilegedRole.Name, metav1.DeleteOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			errs = append(errs, err)
		} else {
			klog.V(4).Infof("ClusterRole %s already removed", c.objs.PrivilegedRole.Name)
		}
	}

	if err := c.kubeClient.CoreV1().ConfigMaps(c.operatorNamespace).Delete(ctx, c.objs.CAConfigMap.Name, metav1.DeleteOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			errs = append(errs, err)
		} else {
			klog.V(4).Infof("ConfigMap %s already removed", c.objs.CAConfigMap.Name)
		}
	}

	// Node
	if err := c.kubeClient.CoreV1().ServiceAccounts(c.operatorNamespace).Delete(ctx, c.objs.NodeServiceAccount.Name, metav1.DeleteOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			errs = append(errs, err)
		} else {
			klog.V(4).Infof("ServiceAccount %s already removed", c.objs.NodeServiceAccount.Name)
		}
	}

	if err := c.kubeClient.RbacV1().ClusterRoleBindings().Delete(ctx, c.objs.NodeRoleBinding.Name, metav1.DeleteOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			errs = append(errs, err)
		} else {
			klog.V(4).Infof("ClusterRoleBinding %s already removed", c.objs.NodeRoleBinding.Name)
		}
	}

	// Controller
	if err := c.kubeClient.CoreV1().ServiceAccounts(c.operatorNamespace).Delete(ctx, c.objs.ControllerServiceAccount.Name, metav1.DeleteOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			errs = append(errs, err)
		} else {
			klog.V(4).Infof("ServiceAccount %s already removed", c.objs.ControllerServiceAccount.Name)
		}
	}

	if err := c.kubeClient.RbacV1().ClusterRoleBindings().Delete(ctx, c.objs.ControllerRoleBinding.Name, metav1.DeleteOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			errs = append(errs, err)
		} else {
			klog.V(4).Infof("ClusterRoleBinding %s already removed", c.objs.ControllerRoleBinding.Name)
		}
	}

	if err := c.kubeClient.RbacV1().ClusterRoleBindings().Delete(ctx, c.objs.ProvisionerRoleBinding.Name, metav1.DeleteOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			errs = append(errs, err)
		} else {
			klog.V(4).Infof("ClusterRoleBinding %s already removed", c.objs.ProvisionerRoleBinding.Name)
		}
	}

	if err := c.kubeClient.RbacV1().Roles(c.operatorNamespace).Delete(ctx, c.objs.LeaseLeaderElectionRole.Name, metav1.DeleteOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			errs = append(errs, err)
		} else {
			klog.V(4).Infof("Role %s already removed", c.objs.LeaseLeaderElectionRole.Name)
		}
	}

	if err := c.kubeClient.RbacV1().RoleBindings(c.operatorNamespace).Delete(ctx, c.objs.LeaseLeaderElectionRoleBinding.Name, metav1.DeleteOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			errs = append(errs, err)
		} else {
			klog.V(4).Infof("RoleBinding %s already removed", c.objs.LeaseLeaderElectionRoleBinding.Name)
		}
	}

	// Metrics
	if err := c.kubeClient.RbacV1().Roles(c.operatorNamespace).Delete(ctx, c.objs.PrometheusRole.Name, metav1.DeleteOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			errs = append(errs, err)
		} else {
			klog.V(4).Infof("Role %s already removed", c.objs.PrometheusRole.Name)
		}
	}

	if err := c.kubeClient.RbacV1().RoleBindings(c.operatorNamespace).Delete(ctx, c.objs.PrometheusRoleBinding.Name, metav1.DeleteOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			errs = append(errs, err)
		} else {
			klog.V(4).Infof("RoleBinding %s already removed", c.objs.PrometheusRoleBinding.Name)
		}
	}

	if err := c.kubeClient.CoreV1().Services(c.operatorNamespace).Delete(ctx, c.objs.MetricsService.Name, metav1.DeleteOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			errs = append(errs, err)
		} else {
			klog.V(4).Infof("Service %s already removed", c.objs.MetricsService.Name)
		}
	}

	if err := c.kubeClient.RbacV1().ClusterRoles().Delete(ctx, c.objs.RBACProxyRole.Name, metav1.DeleteOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			errs = append(errs, err)
		} else {
			klog.V(4).Infof("ClusterRole %s already removed", c.objs.RBACProxyRole.Name)
		}
	}

	if err := c.kubeClient.RbacV1().ClusterRoleBindings().Delete(ctx, c.objs.RBACProxyRoleBinding.Name, metav1.DeleteOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			errs = append(errs, err)
		} else {
			klog.V(4).Infof("ClusterRoleBinding %s already removed", c.objs.RBACProxyRoleBinding.Name)
		}
	}

	if err := errors.NewAggregate(errs); err != nil {
		return err
	}

	// All removed, remove the finalizer as the last step
	return operatorv1helpers.RemoveFinalizer(ctx, c.operatorClient, c.operatorName)
}
