package deploymentworkloadcontroller

import (
	"context"
	"fmt"
	configv1 "github.com/openshift/api/config/v1"
	opv1 "github.com/openshift/api/operator/v1"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	"github.com/openshift/library-go/pkg/config/leaderelection"
	"github.com/openshift/library-go/pkg/controller/factory"
	dcsc "github.com/openshift/library-go/pkg/operator/csi/csidrivercontrollerservicecontroller"
	dc "github.com/openshift/library-go/pkg/operator/deploymentcontroller"
	"github.com/openshift/library-go/pkg/operator/management"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"github.com/openshift/library-go/pkg/operator/resource/resourcemerge"
	"github.com/openshift/library-go/pkg/operator/resource/resourceread"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// DeploymentControllerWorkload DeploymentController is a generic controller that manages a deployment.
//
// This controller supports removable operands, as configured in pkg/operator/management.
//
// This controller produces the following conditions:
// <name>Available: indicates that the deployment controller  was successfully deployed and at least one Deployment replica is available.
// <name>Progressing: indicates that the Deployment is in progress.
// <name>Degraded: produced when the sync() method returns an error.
type DeploymentControllerWorkload struct {
	name              string
	manifest          []byte
	kubeClient        kubernetes.Interface
	operatorClient    v1helpers.OperatorClientWithFinalizers
	optionalInformers []factory.Informer

	// Optional hook functions to modify the deployment manifest.
	// This helps in modifying the manifests before it deployment
	// is created from the manifest.
	// If one of these functions returns an error, the sync
	// fails indicating the ordinal position of the failed function.
	// Also, in that scenario the Degraded status is set to True.
	// TODO: Collapse this into optional deployment hook.
	optionalManifestHooks []dc.ManifestHookFunc
	// Optional hook functions to modify the Deployment.
	// If one of these functions returns an error, the sync
	// fails indicating the ordinal position of the failed function.
	// Also, in that scenario the Degraded status is set to True.
	optionalDeploymentHooks []dc.DeploymentHookFunc
}

func NewDeploymentControllerWorkload(
	name string,
	assetFunc resourceapply.AssetFunc,
	file string,
	kubeClient kubernetes.Interface,
	operatorClient v1helpers.OperatorClientWithFinalizers,
	configInformer configinformers.SharedInformerFactory,
	optionalDeploymentHooks ...dc.DeploymentHookFunc,
) *DeploymentControllerWorkload {

	manifestFile, err := assetFunc(file)
	if err != nil {
		panic(fmt.Sprintf("asset: Asset(%v): %v", file, err))
	}

	var optionalManifestHooks []dc.ManifestHookFunc
	optionalManifestHooks = append(optionalManifestHooks, dcsc.WithPlaceholdersHook(configInformer))
	optionalManifestHooks = append(optionalManifestHooks, dcsc.WithServingInfo())
	leConfig := leaderelection.LeaderElectionDefaulting(configv1.LeaderElection{}, "default", "default")
	optionalManifestHooks = append(optionalManifestHooks, dcsc.WithLeaderElectionReplacerHook(leConfig))

	var deploymentHooks []dc.DeploymentHookFunc
	deploymentHooks = append(deploymentHooks, dcsc.WithControlPlaneTopologyHook(configInformer))
	deploymentHooks = append(deploymentHooks, optionalDeploymentHooks...)

	return &DeploymentControllerWorkload{
		name:                    name,
		manifest:                manifestFile,
		kubeClient:              kubeClient,
		operatorClient:          operatorClient,
		optionalManifestHooks:   optionalManifestHooks,
		optionalDeploymentHooks: optionalDeploymentHooks,
	}
}

func (c *DeploymentControllerWorkload) Name() string {
	return c.name
}

// PreconditionFulfilled is a function that indicates whether all prerequisites are met and we can Sync.
func (c *DeploymentControllerWorkload) PreconditionFulfilled(ctx context.Context) (bool, error) {
	return c.preconditionFulfilledInternal()
}

func (c *DeploymentControllerWorkload) preconditionFulfilledInternal() (bool, error) {
	//TODO: implement
	return true, nil
}

// Sync reconciles the deployment. Its return values are used by workloads controller to update the status
// of the operator. Those are:
//   - deployment (*appsv1.Deployment) -> the deployment object that we want to reconcile, if nil is returned
//     the workloads controller will set degraded=True, progressing=True, available=False
//   - operatorConfigAtHighestGeneration (bool): true is required to set operand version
//   - errors ([]error): aggregated list of errors, workload controller appends its own errors to it, returned during sync
func (c *DeploymentControllerWorkload) Sync(ctx context.Context, syncContext factory.SyncContext) (*appsv1.Deployment, bool, []error) {
	errors := []error{}

	opSpec, opStatus, _, err := c.operatorClient.GetOperatorState()
	if apierrors.IsNotFound(err) && management.IsOperatorRemovable() {
		return nil, true, errors
	}
	if err != nil {
		errors = append(errors, err)
	}

	meta, err := c.operatorClient.GetObjectMeta()
	if err != nil {
		errors = append(errors, err)
	}

	required, err := c.getDeployment(opSpec)
	if err != nil {
		errors = append(errors, err)
		return nil, true, errors
	}

	var deployment *appsv1.Deployment
	if management.IsOperatorRemovable() && meta.DeletionTimestamp != nil {
		deployment, err = c.syncDeleting(ctx, required, opSpec)
		if err != nil {
			errors = append(errors, err)
		}
	}
	deployment, err = c.syncManaged(ctx, required, opStatus, syncContext)
	if err != nil {
		errors = append(errors, err)
	}

	//TODO: returning operatorConfigAtHighestGeneration=true always, we don't have status.observedGeneration in operator
	return deployment, true, errors
}

func (c *DeploymentControllerWorkload) syncManaged(ctx context.Context, required *appsv1.Deployment, opStatus *opv1.OperatorStatus, syncContext factory.SyncContext) (*appsv1.Deployment, error) {
	klog.V(4).Infof("syncManaged")

	if management.IsOperatorRemovable() {
		if err := v1helpers.EnsureFinalizer(ctx, c.operatorClient, c.name); err != nil {
			return nil, err
		}
	}

	deployment, _, err := resourceapply.ApplyDeployment(
		ctx,
		c.kubeClient.AppsV1(),
		syncContext.Recorder(),
		required,
		resourcemerge.ExpectedDeploymentGeneration(required, opStatus.Generations),
	)
	if err != nil {
		return nil, err
	}

	return deployment, err
}

func (c *DeploymentControllerWorkload) syncDeleting(ctx context.Context, required *appsv1.Deployment, opSpec *opv1.OperatorSpec) (*appsv1.Deployment, error) {
	klog.V(4).Infof("syncDeleting")
	required, err := c.getDeployment(opSpec)
	if err != nil {
		return nil, err
	}

	err = c.kubeClient.AppsV1().Deployments(required.Namespace).Delete(ctx, required.Name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, err
	} else {
		klog.V(2).Infof("Deleted Deployment %s/%s", required.Namespace, required.Name)
	}

	// All removed, remove the finalizer as the last step
	if v1helpers.RemoveFinalizer(ctx, c.operatorClient, c.name); err != nil {
		return nil, err
	}

	return required, nil
}

func (c *DeploymentControllerWorkload) getDeployment(opSpec *opv1.OperatorSpec) (*appsv1.Deployment, error) {
	manifest := c.manifest
	for i := range c.optionalManifestHooks {
		var err error
		manifest, err = c.optionalManifestHooks[i](opSpec, manifest)
		if err != nil {
			return nil, fmt.Errorf("error running hook function (index=%d): %w", i, err)
		}
	}

	required := resourceread.ReadDeploymentV1OrDie(manifest)

	for i := range c.optionalDeploymentHooks {
		err := c.optionalDeploymentHooks[i](opSpec, required)
		if err != nil {
			return nil, fmt.Errorf("error running hook function (index=%d): %w", i, err)
		}
	}
	return required, nil
}
