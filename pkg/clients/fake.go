package clients

import (
	fakeconfig "github.com/openshift/client-go/config/clientset/versioned/fake"
	cfginformers "github.com/openshift/client-go/config/informers/externalversions"
	fakeop "github.com/openshift/client-go/operator/clientset/versioned/fake"
	opinformers "github.com/openshift/client-go/operator/informers/externalversions"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	corev1 "k8s.io/api/core/v1"
	fakeextapi "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	apiextinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/dynamic/fake"
	fakecore "k8s.io/client-go/kubernetes/fake"
)

// NewFakeClients creates a new Clients for testing.
// TODO: inject existing objects to the fake clients.
func NewFakeClients(controllerNamespace string, isHyperShift bool) *Clients {
	controlPlaneKubeClient := fakecore.NewSimpleClientset()
	controlPlaneKubeInformers := v1helpers.NewKubeInformersForNamespaces(controlPlaneKubeClient, controllerNamespace, "", CSIDriverNamespace)
	scheme := runtime.NewScheme()
	controlPlaneDynamicClient := fake.NewSimpleDynamicClient(scheme)
	controlPlaneDynamicInformer := dynamicinformer.NewDynamicSharedInformerFactory(controlPlaneDynamicClient, 0)

	guestKubeClient := fakecore.NewSimpleClientset()
	guestKubeInformers := v1helpers.NewKubeInformersForNamespaces(controlPlaneKubeClient, controllerNamespace, "", CSIDriverNamespace)

	guestAPIExtClient := fakeextapi.NewSimpleClientset()
	guestAPIExtInformerFactory := apiextinformers.NewSharedInformerFactory(guestAPIExtClient, 0 /*no resync */)

	guestDynamicClient := fake.NewSimpleDynamicClient(scheme)
	guestDynamicInformer := dynamicinformer.NewDynamicSharedInformerFactory(guestDynamicClient, 0)

	guestOperatorClient := fakeop.NewSimpleClientset()
	guestOperatorInformerFactory := opinformers.NewSharedInformerFactory(guestOperatorClient, 0)

	guestConfigClient := fakeconfig.NewSimpleClientset()
	guestConfigInformerFactory := cfginformers.NewSharedInformerFactory(guestConfigClient, 0)

	fakeObjectRef := corev1.ObjectReference{
		Kind:       "Pod",
		Namespace:  CSIDriverNamespace,
		Name:       "fake",
		APIVersion: "v1",
	}
	return &Clients{
		ControlPlaneNamespace: controllerNamespace,

		// TODO: OperatorClient

		EventRecorder: events.NewKubeRecorder(guestKubeClient.CoreV1().Events(CSIDriverNamespace), "fake", &fakeObjectRef),

		ControlPlaneKubeClient:      controlPlaneKubeClient,
		ControlPlaneKubeInformers:   controlPlaneKubeInformers,
		ControlPlaneDynamicClient:   controlPlaneDynamicClient,
		ControlPlaneDynamicInformer: controlPlaneDynamicInformer,

		GuestKubeClient:        guestKubeClient,
		GuestKubeInformers:     guestKubeInformers,
		GuestAPIExtClient:      guestAPIExtClient,
		GuestAPIExtInformer:    guestAPIExtInformerFactory,
		GuestDynamicClient:     guestDynamicClient,
		GuestDynamicInformer:   guestDynamicInformer,
		GuestOperatorClientSet: guestOperatorClient,
		GuestOperatorInformers: guestOperatorInformerFactory,
		GuestConfigClientSet:   guestConfigClient,
		GuestConfigInformers:   guestConfigInformerFactory,
	}
}
