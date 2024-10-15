package operator

import (
	"context"
	"fmt"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/csi-operator/pkg/clients"
	"github.com/openshift/csi-operator/pkg/operator/config"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/csi/csicontrollerset"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/klog/v2"
)

type Controller struct {
	cli                *clients.Clients
	csiControllers     []*csicontrollerset.CSIControllerSet
	factoryControllers []factory.Controller
	controllersRunning bool
	eventRecorder      events.Recorder
	operator           *config.OperatorControllerConfig
}

type Runnable interface {
	Run(ctx context.Context, workers int)
}

const (
	// Minimal interval between controller resyncs.
	resyncInterval = 20 * time.Minute

	operatorConditionPrefix = "StarterController"
)

func StarterController(cli *clients.Clients,
	//informers v1helpers.KubeInformersForNamespaces,
	csiControllers []*csicontrollerset.CSIControllerSet,
	factoryControllers []factory.Controller,
	eventRecorder events.Recorder, operator *config.OperatorControllerConfig) factory.Controller {

	c := &Controller{
		cli:                cli,
		csiControllers:     csiControllers,
		factoryControllers: factoryControllers,
		eventRecorder:      eventRecorder.WithComponentSuffix("StarterController"),
		operator:           operator,
	}
	infor := []factory.Informer{cli.OperatorClient.Informer()}
	infor = append(infor, operator.PreconditionInformers...)
	return factory.New().WithSync(c.sync).WithSyncDegradedOnError(cli.OperatorClient).ResyncEvery(resyncInterval).WithInformers(
		infor...,
	).ToController("StarterController", eventRecorder)
}

func (c *Controller) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	klog.Infof("StarterController sync started")
	defer klog.Infof("StarterController sync finished")

	if c.operator.Precondition != nil {
		_, err := c.operator.Precondition()
		if err != nil {
			return c.setDisabledCondition(ctx, fmt.Sprintf("Pre condition not satisfied: %v", err))
		}
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
		c.cli.OperatorClient,
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
		c.cli.OperatorClient,
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
