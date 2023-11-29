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
	userAgwent             string
	csiDriverName          string
	resync                 time.Duration
	controllerConfig       *controllercmd.ControllerContext
	guestKubeConfigFile    string
	controlPlaneNamespaces []string
	guestNamespaces        []string
}

// NewBuilder creates a new Builder.
func NewBuilder(userAgent string, csiDriverName string, controllerConfig *controllercmd.ControllerContext, resync time.Duration) *Builder {
	return &Builder{
		userAgwent:       userAgent,
		csiDriverName:    csiDriverName,
		resync:           resync,
		controllerConfig: controllerConfig,
		controlPlaneNamespaces: []string{
			controllerConfig.OperatorNamespace,
		},
		guestNamespaces: []string{
			"",
			CSIDriverNamespace,
		},
	}
}

// WithHyperShiftGuest sets the kubeconfig file for the guest cluster. Usable only on HyperShift.
func (b *Builder) WithHyperShiftGuest(kubeConfigFile string, cloudConfigNamespace string) *Builder {
	b.guestKubeConfigFile = kubeConfigFile
	if cloudConfigNamespace != "" {
		b.guestNamespaces = append(b.guestNamespaces, cloudConfigNamespace)
	} else {
		b.guestNamespaces = append(b.guestNamespaces, ManagedConfigNamespace)
	}
	return b
}

// BuildOrDie creates new Kubernetes clients.
func (b *Builder) BuildOrDie(ctx context.Context) *Clients {
	controlPlaneRestConfig := rest.AddUserAgent(b.controllerConfig.KubeConfig, b.userAgwent)
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
			klog.Fatalf("error reading guesKubeConfig from %s: %v", b.guestKubeConfigFile, err)
		}
		guestKubeConfig = rest.AddUserAgent(guestKubeConfig, b.userAgwent)
		guestKubeClient = kubeclient.NewForConfigOrDie(guestKubeConfig)

		// Create all events in the GUEST cluster.
		// Use name of the operator Deployment in the management cluster + namespace
		// in the guest cluster as the closest approximation of the real involvedObject.
		controllerRef, err := events.GetControllerReferenceForCurrentPod(ctx, controlPlaneKubeClient, b.controllerConfig.OperatorNamespace, nil)
		controllerRef.Namespace = CSIDriverNamespace
		if err != nil {
			klog.Warningf("unable to get owner reference (falling back to namespace): %v", err)
		}
		clients.EventRecorder = events.NewKubeRecorder(guestKubeClient.CoreV1().Events(CSIDriverNamespace), b.userAgwent, controllerRef)
	}
	clients.KubeClient = guestKubeClient
	clients.KubeInformers = v1helpers.NewKubeInformersForNamespaces(guestKubeClient, b.guestNamespaces...)

	gvr := opv1.SchemeGroupVersion.WithResource("clustercsidrivers")
	guestOperatorClient, guestOperatorDynamicInformers, err := goc.NewClusterScopedOperatorClientWithConfigName(guestKubeConfig, gvr, b.csiDriverName)
	if err != nil {
		klog.Fatalf("error building clustercsidriver informers: %v", err)
	}
	clients.OperatorClient = guestOperatorClient
	clients.operatorDynamicInformers = guestOperatorDynamicInformers

	guestAPIExtClient, err := apiextclient.NewForConfig(guestKubeConfig)
	if err != nil {
		klog.Fatalf("error building api extension client in target cluster: %v", err)
	}
	clients.APIExtClient = guestAPIExtClient
	clients.APIExtInformer = apiextinformers.NewSharedInformerFactory(guestAPIExtClient, b.resync)

	guestDynamicClient, err := dynamic.NewForConfig(guestKubeConfig)
	if err != nil {
		klog.Fatalf("error building dynamic api client in target cluster: %v", err)
	}
	clients.DynamicClient = guestDynamicClient

	// TODO: non-filtered one for VolumeSnapshots?
	clients.DynamicInformer = dynamicinformer.NewFilteredDynamicSharedInformerFactory(guestDynamicClient, b.resync, CSIDriverNamespace, nil)

	clients.OperatorClientSet = opclient.NewForConfigOrDie(guestKubeConfig)
	clients.OperatorInformers = opinformers.NewSharedInformerFactory(clients.OperatorClientSet, b.resync)

	clients.ConfigClientSet = cfgclientset.NewForConfigOrDie(guestKubeConfig)
	clients.ConfigInformers = cfginformers.NewSharedInformerFactory(clients.ConfigClientSet, b.resync)

	return clients
}
