package gcp_pd

import (
	"context"
	"fmt"

	"github.com/openshift/csi-operator/assets"
	"github.com/openshift/csi-operator/pkg/clients"
	"github.com/openshift/csi-operator/pkg/driver/common/operator"
	"github.com/openshift/csi-operator/pkg/generator"
	"github.com/openshift/csi-operator/pkg/operator/config"

	opv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"github.com/openshift/library-go/pkg/operator/staticresourcecontroller"
	"k8s.io/klog/v2"
)

const (
	customAssetBase    = "overlays/gcp-pd/custom"
	generatedAssetBase = "overlays/gcp-pd/generated"
)

// GetGCPPDOperatorConfig returns runtime configuration of the CSI driver operator.
func GetGCPPDOperatorConfig() *config.OperatorConfig {
	return &config.OperatorConfig{
		CSIDriverName:                   opv1.GCPPDCSIDriver,
		UserAgent:                       "gcp-pd-csi-driver-operator",
		AssetReader:                     assets.ReadFile,
		AssetDir:                        generatedAssetBase,
		OperatorControllerConfigBuilder: GetGCPPDOperatorControllerConfig,
		Removable:                       false,
	}
}

// GetGCPPDOperatorControllerConfig returns second half of runtime configuration of the CSI driver operator,
// after a client connection + cluster flavour are established.
func GetGCPPDOperatorControllerConfig(ctx context.Context, flavour generator.ClusterFlavour, c *clients.Clients) (*config.OperatorControllerConfig, error) {
	if flavour != generator.FlavourStandalone {
		klog.Error(nil, "Flavour HyperShift is not supported")
		return nil, fmt.Errorf("Flavour HyperShift is not supported")
	}

	cfg := operator.NewDefaultOperatorControllerConfig(flavour, c, "GCPPD")

	oldPrivilegedBindingController := staticresourcecontroller.NewStaticResourceController(
		cfg.GetControllerName("OldControllerPrivilegedBindingRemoval"),
		assets.ReadFile,
		nil,
		resourceapply.NewKubeClientHolder(c.KubeClient).WithDynamicClient(c.DynamicClient),
		c.OperatorClient,
		c.EventRecorder,
	).WithConditionalResources(
		assets.ReadFile,
		[]string{customAssetBase + "/old_controller_privileged_binding.yaml"},
		func() bool { return false },
		func() bool { return true },
	)
	cfg.ExtraControlPlaneControllers = append(cfg.ExtraControlPlaneControllers, oldPrivilegedBindingController)

	go c.ConfigInformers.Start(ctx.Done())

	return cfg, nil
}
