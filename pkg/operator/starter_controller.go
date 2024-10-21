package operator

import (
	"context"
	"fmt"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/csi-operator/pkg/operator/config"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/csi/csicontrollerset"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/klog/v2"
)

type Controller struct {
	operatorClient           v1helpers.OperatorClient
	csiControllers           []*csicontrollerset.CSIControllerSet
	factoryControllers       []factory.Controller
	controllersRunning       bool
	eventRecorder            events.Recorder
	operatorControllerConfig *config.OperatorControllerConfig
}

type Runnable interface {
	Run(ctx context.Context, workers int)
}

const (
	// Minimal interval between controller resyncs.
	resyncInterval = 20 * time.Minute

	operatorConditionPrefix = "CSIController"
)

// Creates a controller that checks if a pre-condition, when defined,
// is met every 20 minutes. If the pre-condition is met and the controllers
// are not running yet, trigger the controllers run, if it's not, set the operator
// condition to disabled.
func StarterController(operatorClient v1helpers.OperatorClient,
	csiControllers []*csicontrollerset.CSIControllerSet,
	factoryControllers []factory.Controller,
	eventRecorder events.Recorder, operatorControllerConfig *config.OperatorControllerConfig) factory.Controller {

	c := &Controller{
		operatorClient:           operatorClient,
		csiControllers:           csiControllers,
		factoryControllers:       factoryControllers,
		eventRecorder:            eventRecorder.WithComponentSuffix(operatorConditionPrefix),
		operatorControllerConfig: operatorControllerConfig,
	}
	informers := []factory.Informer{operatorClient.Informer()}
	informers = append(informers, operatorControllerConfig.PreconditionInformers...)
	return factory.New().WithSync(c.sync).WithSyncDegradedOnError(operatorClient).ResyncEvery(resyncInterval).WithInformers(
		informers...,
	).ToController("StarterController", eventRecorder)
}

func (c *Controller) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	opSpec, _, _, err := c.operatorClient.GetOperatorState()
	if err != nil {
		return err
	}
	if opSpec.ManagementState != operatorv1.Managed {
		return nil
	}

	_, err = c.operatorControllerConfig.Precondition()
	if err != nil {
		return c.setDisabledCondition(ctx, fmt.Sprintf("Pre-condition not satisfied: %v", err))
	}
	if !c.controllersRunning {
		// Start controllers
		for _, controller := range c.factoryControllers {
			klog.Infof("Starting controller %s", controller.Name())
			go controller.Run(ctx, 1)
		}

		klog.V(4).Infof("Starting CSI driver controllers")
		for _, ctrl := range c.csiControllers {
			go func(ctrl Runnable) {
				defer utilruntime.HandleCrash()
				ctrl.Run(ctx, 1)
			}(ctrl)
		}
		c.controllersRunning = true
	}

	return c.setEnabledCondition(ctx)
}

func (c *Controller) setEnabledCondition(ctx context.Context) error {
	_, _, err := v1helpers.UpdateStatus(
		ctx,
		c.operatorClient,
		removeConditionFn(operatorConditionPrefix+"Disabled"),
	)
	return err
}

func (c *Controller) setDisabledCondition(ctx context.Context, msg string) error {
	disabledCnd := operatorv1.OperatorCondition{
		Type:    operatorConditionPrefix + "Disabled",
		Status:  operatorv1.ConditionTrue,
		Reason:  "PreConditionNotSatisfied",
		Message: msg,
	}
	_, _, err := v1helpers.UpdateStatus(
		ctx,
		c.operatorClient,
		v1helpers.UpdateConditionFn(disabledCnd),
	)
	return err
}

func removeConditionFn(cnd string) v1helpers.UpdateStatusFunc {
	return func(oldStatus *operatorv1.OperatorStatus) error {
		v1helpers.RemoveOperatorCondition(&oldStatus.Conditions, cnd)
		return nil
	}
}
