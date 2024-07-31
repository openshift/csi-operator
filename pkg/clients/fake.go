package clients

import (
	"context"
	"testing"

	opv1 "github.com/openshift/api/operator/v1"
	fakeconfig "github.com/openshift/client-go/config/clientset/versioned/fake"
	cfginformers "github.com/openshift/client-go/config/informers/externalversions"
	fakeop "github.com/openshift/client-go/operator/clientset/versioned/fake"
	opinformers "github.com/openshift/client-go/operator/informers/externalversions"
	fakehype "github.com/openshift/hypershift/client/clientset/clientset/fake"
	hypextinformers "github.com/openshift/hypershift/client/informers/externalversions"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	corev1 "k8s.io/api/core/v1"
	fakeextapi "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	apiextinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/dynamic/fake"
	fakecore "k8s.io/client-go/kubernetes/fake"
)

// NewFakeClients creates a new Clients full of fake interfaces for testing.
// To add existing objects to the fake clients, use this pattern: c.controlPlaneKubeClient.(*fake.Clientset).Tracer().Add(obj1, obj2).
// Each fake client interface has its own Tracer.
func NewFakeClients(controllerNamespace string, cr *opv1.ClusterCSIDriver) *Clients {
	controlPlaneKubeClient := fakecore.NewSimpleClientset()
	controlPlaneKubeInformers := v1helpers.NewKubeInformersForNamespaces(controlPlaneKubeClient, controllerNamespace, "", CSIDriverNamespace)

	// Manually register HyperShift's CRDs used by the operator. We cannot import their schema,
	// https://issues.redhat.com/browse/HOSTEDCP-336, This is necessary to enable List() support in the fake dynamic
	// client. See comments in NewSimpleDynamicClientWithCustomListKinds for details.
	gvrToListKind := map[schema.GroupVersionResource]string{
		{Group: "hypershift.openshift.io", Version: "v1beta1", Resource: "hostedcontrolplanes"}: "HostedControlPlaneList",
	}
	scheme := runtime.NewScheme()
	controlPlaneDynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind)
	controlPlaneDynamicInformer := dynamicinformer.NewDynamicSharedInformerFactory(controlPlaneDynamicClient, 0)

	controlPlaneHypeClient := fakehype.NewSimpleClientset()
	controlPlaneHypeInformers := hypextinformers.NewSharedInformerFactory(controlPlaneHypeClient, 0)

	guestKubeClient := fakecore.NewSimpleClientset()
	guestKubeInformers := v1helpers.NewKubeInformersForNamespaces(guestKubeClient, controllerNamespace, "", CSIDriverNamespace)

	guestAPIExtClient := fakeextapi.NewSimpleClientset()
	guestAPIExtInformerFactory := apiextinformers.NewSharedInformerFactory(guestAPIExtClient, 0 /*no resync */)

	guestDynamicClient := fake.NewSimpleDynamicClient(scheme)
	guestDynamicInformer := dynamicinformer.NewDynamicSharedInformerFactory(guestDynamicClient, 0)

	guestOperatorClient := fakeop.NewSimpleClientset()
	guestOperatorInformerFactory := opinformers.NewSharedInformerFactory(guestOperatorClient, 0)

	guestConfigClient := fakeconfig.NewSimpleClientset()
	guestConfigInformerFactory := cfginformers.NewSharedInformerFactory(guestConfigClient, 0)

	dynamicOperatorClient := v1helpers.NewFakeOperatorClient(&cr.Spec.OperatorSpec, &cr.Status.OperatorStatus, nil)

	fakeObjectRef := corev1.ObjectReference{
		Kind:       "Pod",
		Namespace:  CSIDriverNamespace,
		Name:       "fake",
		APIVersion: "v1",
	}
	c := &Clients{
		ControlPlaneNamespace: controllerNamespace,

		OperatorClient: dynamicOperatorClient,
		// NewFakeOperatorClient does not provide informer factory. All event handlers should be added using
		// OperatorClient.Informer().
		operatorDynamicInformers: nil,

		EventRecorder: events.NewKubeRecorder(guestKubeClient.CoreV1().Events(CSIDriverNamespace), "fake", &fakeObjectRef),

		ControlPlaneKubeClient:      controlPlaneKubeClient,
		ControlPlaneKubeInformers:   controlPlaneKubeInformers,
		ControlPlaneDynamicClient:   controlPlaneDynamicClient,
		ControlPlaneDynamicInformer: controlPlaneDynamicInformer,
		ControlPlaneHypeClient:      controlPlaneHypeClient,
		ControlPlaneHypeInformer:    controlPlaneHypeInformers,

		KubeClient:        guestKubeClient,
		KubeInformers:     guestKubeInformers,
		APIExtClient:      guestAPIExtClient,
		APIExtInformer:    guestAPIExtInformerFactory,
		DynamicClient:     guestDynamicClient,
		DynamicInformer:   guestDynamicInformer,
		OperatorClientSet: guestOperatorClient,
		OperatorInformers: guestOperatorInformerFactory,
		ConfigClientSet:   guestConfigClient,
		ConfigInformers:   guestConfigInformerFactory,
	}
	return c
}

func GetFakeOperatorCR() *opv1.ClusterCSIDriver {
	return &opv1.ClusterCSIDriver{
		Spec: opv1.ClusterCSIDriverSpec{
			OperatorSpec: opv1.OperatorSpec{
				ManagementState:  opv1.Managed,
				LogLevel:         opv1.Normal,
				OperatorLogLevel: opv1.Normal,
			},
			StorageClassState: opv1.ManagedStorageClass,
			DriverConfig:      opv1.CSIDriverConfigSpec{},
		},
	}
}

func SyncFakeInformers(t *testing.T, c *Clients) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c.Start(ctx)
	c.WaitForCacheSync(ctx)
}
