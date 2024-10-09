package clients

import (
	"context"

	snapshotclient "github.com/kubernetes-csi/external-snapshotter/client/v6/clientset/versioned"
	cfgclientset "github.com/openshift/client-go/config/clientset/versioned"
	cfginformers "github.com/openshift/client-go/config/informers/externalversions"
	cfgv1informers "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	opclient "github.com/openshift/client-go/operator/clientset/versioned"
	opinformers "github.com/openshift/client-go/operator/informers/externalversions"
	hypextclient "github.com/openshift/hypershift/client/clientset/clientset"
	hypextinformers "github.com/openshift/hypershift/client/informers/externalversions"
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
	ManagedConfigNamespace = "openshift-config-managed" // For kube-cloud-config config map. TODO: should be namespace list configurable?
)

// Clients is a collection of clients for a CSI driver operator.
type Clients struct {
	// Namespace where to install CSI driver control plane. On standalone cluster, it's the same as the guest namespace,
	// i.e. openshift-cluster-csi-drivers.
	ControlPlaneNamespace string

	// Namespace where to install CSI driver guest.
	GuestNamespace string

	// Client for operator's ClusterCSIDriver CR. Always in the guest or standalone cluster.
	OperatorClient v1helpers.OperatorClientWithFinalizers
	// Informer for the ClusterCSIDriver CR. Always in the guest or standalone cluster.
	// Explicitly private, because OperatorClient.Informer() should be used to add event handlers.
	operatorDynamicInformers dynamicinformer.DynamicSharedInformerFactory

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

	// HyperShift Client for HyperShift control plane
	ControlPlaneHypeClient hypextclient.Interface
	// HyperShift Informer in HyperShift control plane
	ControlPlaneHypeInformer hypextinformers.SharedInformerFactory

	// Kubernetes API client for guest or standalone.
	KubeClient kubernetes.Interface
	// Kubernetes API client for guest or standalone. Per namespace.
	KubeInformers v1helpers.KubeInformersForNamespaces

	// CRD API client for guest or standalone. Used to check if volume snapshot CRDs are installed.
	APIExtClient apiextclient.Interface
	// CRD API informers for guest or standalone. Used to check if volume snapshot CRDs are installed.
	APIExtInformer apiextinformers.SharedInformerFactory

	// Dynamic client for the guest cluster. E.g. for VolumeSnapshotClass.
	DynamicClient dynamic.Interface
	// Dynamic informer for the guest cluster. E.g. for VolumeSnapshotClass.
	DynamicInformer dynamicinformer.DynamicSharedInformerFactory

	// operator.openshift.io client, e.g. for ClusterCSIDriver. Always in the guest or standalone cluster.
	OperatorClientSet opclient.Interface
	// operator.openshift.io informers.  Always in the guest or standalone cluster.
	OperatorInformers opinformers.SharedInformerFactory

	// config.openshift.io client, e.g. for Infrastructure. Always in the guest or standalone cluster.
	ConfigClientSet cfgclientset.Interface
	// config.openshift.io informers. Always in the guest or standalone cluster.
	ConfigInformers cfginformers.SharedInformerFactory

	// VolumeSnapshotClass client
	SnapshotClient snapshotclient.Interface
}

// GetControlPlaneSecretInformer returns a Secret informer for given control plane namespace.
func (c *Clients) GetControlPlaneSecretInformer(namespace string) coreinformers.SecretInformer {
	return c.ControlPlaneKubeInformers.InformersFor(namespace).Core().V1().Secrets()
}

func (c *Clients) GetNodeSecretInformer(namespace string) coreinformers.SecretInformer {
	return c.KubeInformers.InformersFor(namespace).Core().V1().Secrets()
}

// GetControlPlaneConfigMapInformer returns a ConfigMap informer for given control plane namespace.
func (c *Clients) GetControlPlaneConfigMapInformer(namespace string) coreinformers.ConfigMapInformer {
	return c.ControlPlaneKubeInformers.InformersFor(namespace).Core().V1().ConfigMaps()
}

// GetConfigMapInformer returns a ConfigMap informer for given guest namespace.
func (c *Clients) GetConfigMapInformer(namespace string) coreinformers.ConfigMapInformer {
	return c.KubeInformers.InformersFor(namespace).Core().V1().ConfigMaps()
}

// GetNodeInformer returns a Node informer.
func (c *Clients) GetNodeInformer() coreinformers.NodeInformer {
	return c.KubeInformers.InformersFor("").Core().V1().Nodes()
}

// GetInfraInformer returns an Infrastructure informer.
func (c *Clients) GetInfraInformer() cfgv1informers.InfrastructureInformer {
	return c.ConfigInformers.Config().V1().Infrastructures()
}

// Start starts all informers.
func (c *Clients) Start(ctx context.Context) {
	if c.operatorDynamicInformers != nil {
		c.operatorDynamicInformers.Start(ctx.Done())
	}
	c.ControlPlaneKubeInformers.Start(ctx.Done())
	c.ControlPlaneDynamicInformer.Start(ctx.Done())
	if c.ControlPlaneHypeInformer != nil {
		c.ControlPlaneHypeInformer.Start(ctx.Done())
	}
	c.KubeInformers.Start(ctx.Done())
	c.APIExtInformer.Start(ctx.Done())
	c.DynamicInformer.Start(ctx.Done())
	c.OperatorInformers.Start(ctx.Done())
	c.ConfigInformers.Start(ctx.Done())
}

// WaitForCacheSync waits for all caches to sync.
func (c *Clients) WaitForCacheSync(ctx context.Context) {
	if c.operatorDynamicInformers != nil {
		c.operatorDynamicInformers.WaitForCacheSync(ctx.Done())
	}
	c.ControlPlaneKubeInformers.InformersFor(c.ControlPlaneNamespace).WaitForCacheSync(ctx.Done())
	for ns := range c.ControlPlaneKubeInformers.Namespaces() {
		c.ControlPlaneKubeInformers.InformersFor(ns).WaitForCacheSync(ctx.Done())
	}
	c.ControlPlaneDynamicInformer.WaitForCacheSync(ctx.Done())
	if c.ControlPlaneHypeInformer != nil {
		c.ControlPlaneHypeInformer.WaitForCacheSync(ctx.Done())
	}
	for ns := range c.KubeInformers.Namespaces() {
		c.KubeInformers.InformersFor(ns).WaitForCacheSync(ctx.Done())
	}
	c.APIExtInformer.WaitForCacheSync(ctx.Done())
	c.DynamicInformer.WaitForCacheSync(ctx.Done())
	c.OperatorInformers.WaitForCacheSync(ctx.Done())
	c.ConfigInformers.WaitForCacheSync(ctx.Done())
}
