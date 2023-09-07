package aws_ebs

import (
	"github.com/openshift/csi-operator/pkg/config/common"
	"github.com/openshift/csi-operator/pkg/generator"
)

// GetAWSEBSGeneratorConfig returns configuration for generating assets of  AWS EBS CSI driver operator.
func GetAWSEBSGeneratorConfig() *generator.CSIDriverGeneratorConfig {
	return &generator.CSIDriverGeneratorConfig{
		AssetPrefix:      "aws-ebs-csi-driver",
		AssetShortPrefix: "ebs",
		DriverName:       "ebs.csi.aws.com",

		ControllerConfig: &generator.ControlPlaneConfig{
			DeploymentTemplateAssetName: "overlays/aws-ebs/patches/controller_add_driver.yaml",
			LivenessProbePort:           10301,
			MetricsPorts: []generator.MetricsPort{
				{
					LocalPort:           common.AWSEBSLoopbackMetricsPortStart,
					InjectKubeRBACProxy: true,
					ExposedPort:         common.AWSEBSExposedMetricsPortStart,
					Name:                "driver-m",
				},
			},
			SidecarLocalMetricsPortStart:   common.AWSEBSLoopbackMetricsPortStart + 1,
			SidecarExposedMetricsPortStart: common.AWSEBSExposedMetricsPortStart + 1,
			Sidecars: []generator.SidecarConfig{
				common.DefaultProvisionerWithSnapshots.WithExtraArguments(
					"--default-fstype=ext4",
					"--feature-gates=Topology=true",
					"--extra-create-metadata=true",
					"--timeout=60s",
				),
				common.DefaultAttacher.WithExtraArguments(
					"--timeout=60s",
				),
				common.DefaultResizer.WithExtraArguments(
					"--timeout=300s",
				),
				common.DefaultSnapshotter.WithExtraArguments(
					"--timeout=300s",
					"--extra-create-metadata",
				),
				common.DefaultLivenessProbe.WithExtraArguments(
					"--probe-timeout=3s",
				),
			},
			Assets: common.DefaultControllerAssets,
			AssetPatches: common.DefaultAssetPatches.WithPatches(generator.HyperShiftOnly,
				"controller.yaml", "overlays/aws-ebs/patches/controller_add_hypershift_controller_minter.yaml",
			),
		},

		GuestConfig: &generator.GuestConfig{
			DaemonSetTemplateAssetName: "overlays/aws-ebs/patches/node_add_driver.yaml",
			LivenessProbePort:          10300,
			Sidecars: []generator.SidecarConfig{
				common.DefaultNodeDriverRegistrar,
				common.DefaultLivenessProbe.WithExtraArguments(
					"--probe-timeout=3s",
				),
			},
			Assets: common.DefaultNodeAssets.WithAssets(generator.AllFlavours,
				"overlays/aws-ebs/base/csidriver.yaml",
				"overlays/aws-ebs/base/storageclass_gp2.yaml",
				"overlays/aws-ebs/base/storageclass_gp3.yaml",
				"overlays/aws-ebs/base/volumesnapshotclass.yaml",
			),
		},
	}
}
