package gcp_pd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/openshift/csi-operator/assets"
	"github.com/openshift/csi-operator/pkg/clients"
	"github.com/openshift/csi-operator/pkg/driver/common/operator"
	"github.com/openshift/csi-operator/pkg/generator"
	"github.com/openshift/csi-operator/pkg/operator/config"

	opv1 "github.com/openshift/api/operator/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"github.com/openshift/library-go/pkg/operator/staticresourcecontroller"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

const (
	customAssetBase    = "overlays/gcp-pd/custom"
	generatedAssetBase = "overlays/gcp-pd/generated"

	// globalInfrastructureName is the default name of the Infrastructure object
	globalInfrastructureName = "cluster"

	// gcpDedicatedRegionPrefix is the prefix for GCP Dedicated regions.
	// GCP Dedicated regions start with "u-" (e.g. "u-germany-northeast1").
	gcpDedicatedRegionPrefix = "u-"
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

	storageClassFiles, err := getStorageClassFiles(ctx, c.ConfigClientSet)
	if err != nil {
		return nil, err
	}
	storageClassSet := sets.New[string](storageClassFiles...)

	cfg.StorageClassSelector = func(name string) bool {
		if storageClassSet.Has(name) {
			return true
		}
		return false
	}

	go c.ConfigInformers.Start(ctx.Done())

	return cfg, nil
}

// getStorageClassFiles returns the list of StorageClass asset files to use,
// based on whether the cluster runs on GCP Dedicated.
// It retries for up to 1 minute to fetch the Infrastructure CR, because during
// early cluster installation the CR may not exist yet.
// On GCP Dedicated, only hyperdisk-balanced is supported.
// On regular GCP, standard-csi and ssd-csi are used.
func getStorageClassFiles(ctx context.Context, configClient configclient.Interface) ([]string, error) {
	regularFiles := []string{
		"storageclass.yaml",
		"storageclass_ssd.yaml",
	}
	gcpDedicatedFiles := []string{
		"storageclass_hyperdisk_balanced.yaml",
	}

	var region string
	var lastErr error
	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, 1*time.Minute, true, func(ctx context.Context) (bool, error) {
		infra, err := configClient.ConfigV1().Infrastructures().Get(ctx, globalInfrastructureName, metav1.GetOptions{})
		if err != nil {
			lastErr = err
			klog.V(4).Infof("Failed to get Infrastructure CR, will retry: %v", err)
			return false, nil
		}
		if infra.Status.PlatformStatus == nil || infra.Status.PlatformStatus.GCP == nil {
			klog.V(4).Infof("Infrastructure CR has no GCP PlatformStatus, assuming regular GCP")
			return true, nil
		}
		region = infra.Status.PlatformStatus.GCP.Region
		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get Infrastructure CR: %w", lastErr)
	}

	if strings.HasPrefix(region, gcpDedicatedRegionPrefix) {
		klog.Infof("GCP Dedicated detected (region %q), using hyperdisk-balanced StorageClass", region)
		return gcpDedicatedFiles, nil
	}
	klog.Infof("Regular GCP detected (region %q), using standard StorageClasses", region)
	return regularFiles, nil
}
