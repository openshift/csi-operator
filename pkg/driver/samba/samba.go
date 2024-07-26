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
	commongenerator "github.com/openshift/csi-operator/pkg/driver/common/generator"
	"k8s.io/klog/v2"
)

const (
	generatedAssetBase = "overlays/samba/generated"
)

func GetSambaGeneratorConfig() *generator.CSIDriverGeneratorConfig {
	return &generator.CSIDriverGeneratorConfig{
		AssetPrefix:      "smb-csi-driver",
		AssetShortPrefix: "smb",
		DriverName:       "smb.csi.k8s.io",
		StandaloneOnly:   true,
		OutputDir:        generatedAssetBase,

		ControllerConfig: &generator.ControlPlaneConfig{
			DeploymentTemplateAssetName: "overlays/samba/patches/controller_add_driver.yaml",
			LivenessProbePort:           10307,
			MetricsPorts: []generator.MetricsPort{
				{
					LocalPort:           commongenerator.SambaLoopbackMetricsPortStart,
					InjectKubeRBACProxy: true,
					ExposedPort:         commongenerator.SambaExposedMetricsPortStart,
					Name:                "driver-m",
				},
			},
			SidecarLocalMetricsPortStart:   commongenerator.SambaLoopbackMetricsPortStart + 1,
			SidecarExposedMetricsPortStart: commongenerator.SambaExposedMetricsPortStart + 1,
			Sidecars: []generator.SidecarConfig{
				commongenerator.DefaultProvisioner.WithExtraArguments(
					"--extra-create-metadata=true",
				),
				commongenerator.DefaultLivenessProbe.WithExtraArguments(
					"--probe-timeout=3s",
				),
			},
			Assets: generator.NewAssets(generator.StandaloneOnly,
				"base/controller_sa.yaml",
				"base/controller_pdb.yaml",
			).WithAssets(generator.StandaloneOnly,
				"base/rbac/kube_rbac_proxy_role.yaml",
				"base/rbac/kube_rbac_proxy_binding.yaml",
				"base/rbac/prometheus_role.yaml",
				"base/rbac/prometheus_binding.yaml",
			),
			AssetPatches: generator.NewAssetPatches(generator.StandaloneOnly,
				"controller.yaml", "common/standalone/controller_add_affinity.yaml",
			),
		},

		GuestConfig: &generator.GuestConfig{
			DaemonSetTemplateAssetName:   "overlays/samba/patches/node_add_driver.yaml",
			LivenessProbePort:            10306,
			NodeRegistrarHealthCheckPort: 10308,
			Sidecars: []generator.SidecarConfig{
				commongenerator.DefaultNodeDriverRegistrar,
				commongenerator.DefaultLivenessProbe.WithExtraArguments(
					"--probe-timeout=3s",
				),
			},
			Assets: commongenerator.DefaultNodeAssets.WithAssets(generator.AllFlavours,
				"overlays/samba/base/configmap_and_secret_reader_provisioner_binding.yaml",
				"overlays/samba/base/controller_privileged_binding.yaml",
				"overlays/samba/base/csidriver.yaml",
			),
			AssetPatches: generator.NewAssetPatches(generator.StandaloneOnly,
				// Any role or cluster role bindings should not hardcode service account namespace because this operator is OLM based and can be installed into a custom namespace.
				"main_provisioner_binding.yaml", "overlays/samba/patches/binding_with_namespace_placeholder_controller.yaml",
				"lease_leader_election_binding.yaml", "overlays/samba/patches/binding_with_namespace_placeholder_controller.yaml",
				"configmap_and_secret_reader_provisioner_binding.yaml", "overlays/samba/patches/binding_with_namespace_placeholder_controller.yaml",
				"controller_privileged_binding.yaml", "overlays/samba/patches/binding_with_namespace_placeholder_controller.yaml",
				"node_privileged_binding.yaml", "overlays/samba/patches/binding_with_namespace_placeholder_node.yaml",
			),
		},
	}
}

// GetSambaOperatorConfig returns runtime configuration of the CSI driver operator.
func GetSambaOperatorConfig() *config.OperatorConfig {
	return &config.OperatorConfig{
		CSIDriverName:                   opv1.SambaCSIDriver,
		UserAgent:                       "smb-csi-driver-operator",
		AssetReader:                     assets.ReadFile,
		AssetDir:                        generatedAssetBase,
		OperatorControllerConfigBuilder: GetSambaOperatorControllerConfig,
		Removable:                       true,
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
