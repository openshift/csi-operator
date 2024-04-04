package samba

import (
	"context"
	"fmt"

	"github.com/openshift/csi-operator/assets"
	"github.com/openshift/csi-operator/pkg/clients"
	"github.com/openshift/csi-operator/pkg/driver/common/operator"
	"github.com/openshift/csi-operator/pkg/generator"
	"github.com/openshift/csi-operator/pkg/operator/config"

	opv1 "github.com/openshift/api/operator/v1"
	"k8s.io/klog/v2"
)

const (
	generatedAssetBase = "overlays/samba/generated"
)

// GetSambaOperatorConfig returns runtime configuration of the CSI driver operator.
func GetSambaOperatorConfig() *config.OperatorConfig {
	return &config.OperatorConfig{
		CSIDriverName:                   opv1.SambaCSIDriver,
		UserAgent:                       "smb-csi-driver-operator",
		AssetReader:                     assets.ReadFile,
		AssetDir:                        generatedAssetBase,
		OperatorControllerConfigBuilder: GetSambaOperatorControllerConfig,
	}
}

// GetSambaOperatorControllerConfig returns second half of runtime configuration of the CSI driver operator,
// after a client connection + cluster flavour are established.
func GetSambaOperatorControllerConfig(ctx context.Context, flavour generator.ClusterFlavour, c *clients.Clients) (*config.OperatorControllerConfig, error) {
	if flavour != generator.FlavourStandalone {
		klog.Error(nil, "Flavour HyperShift is not supported!")
		return nil, fmt.Errorf("Flavour HyperShift is not supported!")
	}

	cfg := operator.NewDefaultOperatorControllerConfig(flavour, c, "Samba")

	go c.ConfigInformers.Start(ctx.Done())

	return cfg, nil
}
