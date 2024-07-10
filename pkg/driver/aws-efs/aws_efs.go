package aws_efs

import (
	"context"
	opv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/csi-operator/assets"
	"github.com/openshift/csi-operator/pkg/clients"
	commongenerator "github.com/openshift/csi-operator/pkg/driver/common/generator"
	"github.com/openshift/csi-operator/pkg/driver/common/operator"
	"github.com/openshift/csi-operator/pkg/generator"
	"github.com/openshift/csi-operator/pkg/operator/config"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivercontrollerservicecontroller"
	dc "github.com/openshift/library-go/pkg/operator/deploymentcontroller"
)

const (
	cloudCredSecretName   = "aws-efs-cloud-credentials"
	metricsCertSecretName = "aws-efs-csi-driver-controller-metrics-serving-cert"
	trustedCAConfigMap    = "aws-efs-csi-driver-trusted-ca-bundle"

	generatedAssetBase = "overlays/aws-efs/generated"
)

// GetAWSEFSGeneratorConfig returns configuration for generating assets of AWS EFS CSI driver operator.
func GetAWSEFSGeneratorConfig() *generator.CSIDriverGeneratorConfig {
	return &generator.CSIDriverGeneratorConfig{
		AssetPrefix:      "aws-efs-csi-driver",
		AssetShortPrefix: "efs",
		DriverName:       "efs.csi.aws.com",
		StandaloneOnly:   true,
		OutputDir:        generatedAssetBase,

		ControllerConfig: &generator.ControlPlaneConfig{
			DeploymentTemplateAssetName: "overlays/aws-efs/patches/controller_add_driver.yaml",
			LivenessProbePort:           10302,
			MetricsPorts: []generator.MetricsPort{
				{
					LocalPort:           commongenerator.AWSEFSLoopbackMetricsPortStart,
					InjectKubeRBACProxy: true,
					ExposedPort:         commongenerator.AWSEFSExposedMetricsPortStart,
					Name:                "driver-m",
				},
			},
			SidecarLocalMetricsPortStart:   commongenerator.AWSEFSLoopbackMetricsPortStart + 1,
			SidecarExposedMetricsPortStart: commongenerator.AWSEFSExposedMetricsPortStart + 1,
			Sidecars: []generator.SidecarConfig{
				commongenerator.DefaultProvisioner.WithExtraArguments(
					"--feature-gates=Topology=true",
					"--extra-create-metadata=true",
					"--timeout=5m",
					"--worker-threads=1",
				),
				commongenerator.DefaultLivenessProbe.WithExtraArguments(
					"--probe-timeout=3s",
				),
			},
			Assets: commongenerator.DefaultControllerAssets.WithAssets(generator.StandaloneOnly,
				"overlays/aws-efs/base/privileged_role.yaml",
				"overlays/aws-efs/base/controller_privileged_binding.yaml",
				"overlays/aws-efs/base/credentials.yaml",
			),
		},

		GuestConfig: &generator.GuestConfig{
			DaemonSetTemplateAssetName:   "overlays/aws-efs/patches/node_add_driver.yaml",
			LivenessProbePort:            10303,
			NodeRegistrarHealthCheckPort: 10305,
			Sidecars: []generator.SidecarConfig{
				commongenerator.DefaultNodeDriverRegistrar,
				commongenerator.DefaultLivenessProbe.WithExtraArguments(
					"--probe-timeout=3s",
				),
			},
			Assets: commongenerator.DefaultNodeAssets.WithAssets(generator.StandaloneOnly,
				"overlays/aws-efs/base/csidriver.yaml",
			),
		},
	}
}

// GetAWSEFSOperatorConfig returns runtime configuration of the CSI driver operator.
func GetAWSEFSOperatorConfig() *config.OperatorConfig {
	return &config.OperatorConfig{
		CSIDriverName:                   opv1.AWSEFSCSIDriver,
		UserAgent:                       "aws-efs-csi-driver-operator",
		AssetReader:                     assets.ReadFile,
		AssetDir:                        generatedAssetBase,
		OperatorControllerConfigBuilder: GetAWSEFSOperatorControllerConfig,
		Removable:                       false,
	}
}

// GetAWSEFSOperatorControllerConfig returns second half of runtime configuration of the CSI driver operator,
// after a client connection + cluster flavour are established.
func GetAWSEFSOperatorControllerConfig(ctx context.Context, flavour generator.ClusterFlavour, c *clients.Clients) (*config.OperatorControllerConfig, error) {
	cfg := operator.NewDefaultOperatorControllerConfig(flavour, c, "AWSEFS")
	cfg.AddDeploymentHookBuilders(c, withCABundleDeploymentHook)
	cfg.DeploymentWatchedSecretNames = append(cfg.DeploymentWatchedSecretNames, cloudCredSecretName, metricsCertSecretName)

	return cfg, nil
}

// withCABundleDeploymentHook projects custom CA bundle ConfigMap into the CSI driver container
func withCABundleDeploymentHook(c *clients.Clients) (dc.DeploymentHookFunc, []factory.Informer) {
	hook := csidrivercontrollerservicecontroller.WithCABundleDeploymentHook(
		c.ControlPlaneNamespace,
		trustedCAConfigMap,
		c.GetControlPlaneConfigMapInformer(c.ControlPlaneNamespace),
	)
	informers := []factory.Informer{
		c.GetControlPlaneConfigMapInformer(c.ControlPlaneNamespace).Informer(),
	}
	return hook, informers
}
