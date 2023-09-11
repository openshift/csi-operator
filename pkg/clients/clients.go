package clients

import (
	"context"

	cfgclientset "github.com/openshift/client-go/config/clientset/versioned"
	cfginformers "github.com/openshift/client-go/config/informers/externalversions"
	cfgv1informers "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	opclient "github.com/openshift/client-go/operator/clientset/versioned"
	opinformers "github.com/openshift/client-go/operator/informers/externalversions"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	apiextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	CSIDriverNamespace     = "openshift-cluster-csi-drivers"
	CloudConfigNamespace   = "openshift-config"
	ManagedConfigNamespace = "openshift-config-managed"
)

// Clients is a collection of clients for a CSI driver operator.
type Clients struct {
	// Namespace where to install CSI driver control plane. On standalone cluster, it's the same as the guest namespace,
	// i.e. openshift-cluster-csi-drivers.
	ControlPlaneNamespace string

	// Client for operator's ClusterCSIDriver CR. Always in the guest or standalone cluster.
	OperatorClient v1helpers.OperatorClientWithFinalizers
	// Informer for the ClusterCSIDriver CR. Always in the guest or standalone cluster.
	OperatorDynamicInformers dynamicinformer.DynamicSharedInformerFactory

	// Recorder for the operator events. Always in the guest cluster.
	EventRecorder events.Recorder

	// Kubernetes API client for HyperShift or standalone control plane.
	ControlPlaneKubeClient kubernetes.Interface
	// Kubernetes API client for HyperShift or standalone control plane. Per namespace.
	ControlPlaneKubeInformers v1helpers.KubeInformersForNamespaces

	// Dynamic client in HyperShift or standalone control plane. E.g. for HyperShift's HostedControlPlane and Prometheus CRs.
	ControlPlaneDynamicClient dynamic.Interface
	// Informer in HyperShift or standalone control plane. E.g. for HyperShift's HostedControlPlane and Prometheus CRs.
	ControlPlaneDynamicInformer dynamicinformer.DynamicSharedInformerFactory

	// Kubernetes API client for guest or standalone.
	GuestKubeClient kubernetes.Interface
	// Kubernetes API client for guest or standalone. Per namespace.
	GuestKubeInformers v1helpers.KubeInformersForNamespaces

	// CRD API client for guest or standalone. Used to check if volume snapshot CRDs are installed.
	GuestAPIExtClient apiextclient.Interface
	// CRD API informers for guest or standalone. Used to check if volume snapshot CRDs are installed.
	GuestAPIExtInformer apiextinformers.SharedInformerFactory

	// Dynamic client for the guest cluster. E.g. for VolumeSnapshotClass.
	GuestDynamicClient dynamic.Interface
	// Dynamic informer for the guest cluster. E.g. for VolumeSnapshotClass.
	GuestDynamicInformer dynamicinformer.DynamicSharedInformerFactory

	// operator.openshift.io client, e.g. for ClusterCSIDriver. Always in the guest or standalone cluster.
	GuestOperatorClientSet opclient.Interface
	// operator.openshift.io informers.  Always in the guest or standalone cluster.
	GuestOperatorInformers opinformers.SharedInformerFactory

	// config.openshift.io client, e.g. for Infrastructure. Always in the guest or standalone cluster.
	GuestConfigClientSet cfgclientset.Interface
	// config.openshift.io informers. Always in the guest or standalone cluster.
	GuestConfigInformers cfginformers.SharedInformerFactory
}

// GetControlPlaneSecretInformer returns a Secret informer for given control plane namespace.
func (c *Clients) GetControlPlaneSecretInformer(namespace string) coreinformers.SecretInformer {
	return c.ControlPlaneKubeInformers.InformersFor(namespace).Core().V1().Secrets()
}

// GetControlPlaneConfigMapInformer returns a ConfigMap informer for given control plane namespace.
func (c *Clients) GetControlPlaneConfigMapInformer(namespace string) coreinformers.ConfigMapInformer {
	return c.ControlPlaneKubeInformers.InformersFor(namespace).Core().V1().ConfigMaps()
}

// GetGuestConfigMapInformer returns a ConfigMap informer for given guest namespace.
func (c *Clients) GetGuestConfigMapInformer(namespace string) coreinformers.ConfigMapInformer {
	return c.GuestKubeInformers.InformersFor(namespace).Core().V1().ConfigMaps()
}

// GetGuestNodeInformer returns a Node informer.
func (c *Clients) GetGuestNodeInformer() coreinformers.NodeInformer {
	return c.GuestKubeInformers.InformersFor("").Core().V1().Nodes()
}

// GetGuestInfraInformer returns an Infrastructure informer.
func (c *Clients) GetGuestInfraInformer() cfgv1informers.InfrastructureInformer {
	return c.GuestConfigInformers.Config().V1().Infrastructures()
}

// Start starts all informers.
func (c *Clients) Start(ctx context.Context) {
	c.OperatorDynamicInformers.Start(ctx.Done())
	c.ControlPlaneKubeInformers.Start(ctx.Done())
	c.ControlPlaneDynamicInformer.Start(ctx.Done())
	c.GuestKubeInformers.Start(ctx.Done())
	c.GuestAPIExtInformer.Start(ctx.Done())
	c.GuestDynamicInformer.Start(ctx.Done())
	c.GuestOperatorInformers.Start(ctx.Done())
	c.GuestConfigInformers.Start(ctx.Done())
}

// WaitForCacheSync waits for all caches to sync.
func (c *Clients) WaitForCacheSync(ctx context.Context) {
	c.OperatorDynamicInformers.WaitForCacheSync(ctx.Done())
	c.ControlPlaneKubeInformers.InformersFor(c.ControlPlaneNamespace).WaitForCacheSync(ctx.Done())
	c.ControlPlaneDynamicInformer.WaitForCacheSync(ctx.Done())
	c.GuestKubeInformers.InformersFor(CSIDriverNamespace).WaitForCacheSync(ctx.Done())
	c.GuestAPIExtInformer.WaitForCacheSync(ctx.Done())
	c.GuestDynamicInformer.WaitForCacheSync(ctx.Done())
	c.GuestOperatorInformers.WaitForCacheSync(ctx.Done())
	c.GuestConfigInformers.WaitForCacheSync(ctx.Done())
}
