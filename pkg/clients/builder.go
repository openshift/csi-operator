package clients

import (
	"context"
	"time"

	opv1 "github.com/openshift/api/operator/v1"
	cfgclientset "github.com/openshift/client-go/config/clientset/versioned"
	cfginformers "github.com/openshift/client-go/config/informers/externalversions"
	opclient "github.com/openshift/client-go/operator/clientset/versioned"
	opinformers "github.com/openshift/client-go/operator/informers/externalversions"
	"github.com/openshift/library-go/pkg/config/client"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/openshift/library-go/pkg/operator/events"
	goc "github.com/openshift/library-go/pkg/operator/genericoperatorclient"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	apiextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	kubeclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

// Builder is a helper to create Clients.
type Builder struct {
	clientName             string
	csiDriverName          string
	resync                 time.Duration
	controllerConfig       *controllercmd.ControllerContext
	guestKubeConfigFile    string
	controlPlaneNamespaces []string
	guestNamespaces        []string
}

// NewBuilder creates a new Builder.
func NewBuilder(clientName string, csiDriverName string, controllerConfig *controllercmd.ControllerContext, resync time.Duration) *Builder {
	return &Builder{
		clientName:       clientName,
		csiDriverName:    csiDriverName,
		resync:           resync,
		controllerConfig: controllerConfig,
		controlPlaneNamespaces: []string{
			controllerConfig.OperatorNamespace,
		},
		guestNamespaces: []string{
			"",
			CloudConfigNamespace,
			CSIDriverNamespace,
			ManagedConfigNamespace, // TODO: is it needed?
		},
	}
}

// WithHyperShiftGuest sets the kubeconfig file for the guest cluster. Usable only on HyperShift.
func (b *Builder) WithHyperShiftGuest(kubeConfigFile string) {
	b.guestKubeConfigFile = kubeConfigFile
}

// BuildOrDie creates new Kubernetes clients.
func (b *Builder) BuildOrDie(ctx context.Context) *Clients {
	controlPlaneRestConfig := rest.AddUserAgent(b.controllerConfig.KubeConfig, b.clientName)
	controlPlaneKubeClient := kubeclient.NewForConfigOrDie(controlPlaneRestConfig)
	controlPlaneKubeInformers := v1helpers.NewKubeInformersForNamespaces(controlPlaneKubeClient, b.controlPlaneNamespaces...)

	controlPlaneDynamicClient := dynamic.NewForConfigOrDie(controlPlaneRestConfig)
	controlPlaneDynamicInformers := dynamicinformer.NewFilteredDynamicSharedInformerFactory(controlPlaneDynamicClient, b.resync, b.controllerConfig.OperatorNamespace, nil)

	isHyperShift := b.guestKubeConfigFile != ""
	clients := &Clients{
		ControlPlaneNamespace:     b.controllerConfig.OperatorNamespace,
		EventRecorder:             b.controllerConfig.EventRecorder,
		ControlPlaneKubeClient:    controlPlaneKubeClient,
		ControlPlaneKubeInformers: controlPlaneKubeInformers,

		ControlPlaneDynamicClient:   controlPlaneDynamicClient,
		ControlPlaneDynamicInformer: controlPlaneDynamicInformers,
	}

	guestKubeClient := controlPlaneKubeClient
	guestKubeConfig := controlPlaneRestConfig
	if isHyperShift {
		var err error
		guestKubeConfig, err = client.GetKubeConfigOrInClusterConfig(b.guestKubeConfigFile, nil)
		if err != nil {
			panic(err)
		}
		guestKubeConfig = rest.AddUserAgent(guestKubeConfig, b.clientName)
		guestKubeClient = kubeclient.NewForConfigOrDie(guestKubeConfig)

		// Create all events in the GUEST cluster.
		// Use name of the operator Deployment in the management cluster + namespace
		// in the guest cluster as the closest approximation of the real involvedObject.
		controllerRef, err := events.GetControllerReferenceForCurrentPod(ctx, controlPlaneKubeClient, b.controllerConfig.OperatorNamespace, nil)
		controllerRef.Namespace = CSIDriverNamespace
		if err != nil {
			klog.Warningf("unable to get owner reference (falling back to namespace): %v", err)
		}
		clients.EventRecorder = events.NewKubeRecorder(guestKubeClient.CoreV1().Events(CSIDriverNamespace), b.clientName, controllerRef)
	}
	clients.GuestKubeClient = guestKubeClient
	clients.GuestKubeInformers = v1helpers.NewKubeInformersForNamespaces(guestKubeClient, b.guestNamespaces...)

	gvr := opv1.SchemeGroupVersion.WithResource("clustercsidrivers")
	guestOperatorClient, guestOperatorDynamicInformers, err := goc.NewClusterScopedOperatorClientWithConfigName(guestKubeConfig, gvr, b.csiDriverName)
	if err != nil {
		panic(err)
	}
	clients.OperatorClient = guestOperatorClient
	clients.OperatorDynamicInformers = guestOperatorDynamicInformers

	guestAPIExtClient, err := apiextclient.NewForConfig(guestKubeConfig)
	if err != nil {
		panic(err)
	}
	clients.GuestAPIExtClient = guestAPIExtClient
	clients.GuestAPIExtInformer = apiextinformers.NewSharedInformerFactory(guestAPIExtClient, b.resync)

	guestDynamicClient, err := dynamic.NewForConfig(guestKubeConfig)
	if err != nil {
		panic(err)
	}
	clients.GuestDynamicClient = guestDynamicClient

	// TODO: non-filtered one for VolumeSnapshots?
	clients.GuestDynamicInformer = dynamicinformer.NewFilteredDynamicSharedInformerFactory(guestDynamicClient, b.resync, CSIDriverNamespace, nil)

	clients.GuestOperatorClientSet = opclient.NewForConfigOrDie(guestKubeConfig)
	clients.GuestOperatorInformers = opinformers.NewSharedInformerFactory(clients.GuestOperatorClientSet, b.resync)

	clients.GuestConfigClientSet = cfgclientset.NewForConfigOrDie(guestKubeConfig)
	clients.GuestConfigInformers = cfginformers.NewSharedInformerFactory(clients.GuestConfigClientSet, b.resync)

	return clients
}
