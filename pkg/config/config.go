package config

import (
	"io/ioutil"

	"github.com/golang/glog"

	csi "github.com/openshift/csi-operator/pkg/apis/csidriver/v1alpha1"
	"github.com/openshift/csi-operator/pkg/generated"
	"gopkg.in/yaml.v2"
)

// Config is configuration of the CSI Driver operator.
type Config struct {
	// Default sidecar container images used when CR does not specify anything special.
	DefaultImages csi.CSIDeploymentContainerImages `yaml:"defaultImages,omitempty"`

	// Selector of nodes where Deployment with controller components (provisioner, attacher) can run.
	// When nil, no selector will be set in the Deployment.
	InfrastructureNodeSelector map[string]string `yaml:"infrastructureNodeSelector,omitempty"`

	// Number of replicas of Deployment with controller components.
	DeploymentReplicas int32 `yaml:"deploymentReplicas,omitempty"`

	// Name of cluster role to bind to ServiceAccount that runs all pods with drivers. This role allows to run
	// provisioner, attacher and driver registrar, i.e. read/modify PV, PVC, Node, VolumeAttachment and whatnot in
	// *any* namespace.
	// TODO: should there be multiple ClusterRoles, separate one for provisioner, attacher and driver registrar?
	// In addition, some of them may require variants (e.g. provisioner without access to all secrets and / or attacher
	// without access to all secrets)
	ClusterRoleName string `yaml:"clusterRoleName,omitempty"`

	// Name of cluster role to bind to ServiceAccount that runs all pods with drivers. This role allows attacher and
	// provisioner to run leader election. It will be bound to the ServiceAccount using RoleBind, i.e. leader election
	// will be possible only in the namespace where the drivers run.
	LeaderElectionClusterRoleName string `yaml:"leaderElectionClusterRoleName,omitempty"`

	// Path to /var/lib/kubelet.
	KubeletRootDir string `yaml:"kubeletRootDir,omitempty"`
}

// DefaultConfig returns the default configuration of the operator.
func DefaultConfig() *Config {
	cfgBytes := generated.MustAsset("default-config.yaml")
	cfg := &Config{}
	err := yaml.Unmarshal(cfgBytes, cfg)
	if err != nil {
		panic(err)
	}
	return cfg
}

// LoadConfig loads operator config from a file. It fills all omitted fields with default values.
func LoadConfig(path string) (*Config, error) {
	glog.V(2).Infof("Loading config file %s", path)

	cfgBytes, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	err = yaml.UnmarshalStrict(cfgBytes, cfg)
	if err != nil {
		return nil, err
	}

	// Supply missing values from the default config
	defaultCfg := DefaultConfig()
	if cfg.DefaultImages.DriverRegistrarImage == nil {
		cfg.DefaultImages.DriverRegistrarImage = defaultCfg.DefaultImages.DriverRegistrarImage
	}
	if cfg.DefaultImages.AttacherImage == nil {
		cfg.DefaultImages.AttacherImage = defaultCfg.DefaultImages.AttacherImage
	}
	if cfg.DefaultImages.ProvisionerImage == nil {
		cfg.DefaultImages.ProvisionerImage = defaultCfg.DefaultImages.ProvisionerImage
	}
	if cfg.DefaultImages.LivenessProbeImage == nil {
		cfg.DefaultImages.LivenessProbeImage = defaultCfg.DefaultImages.LivenessProbeImage
	}
	if cfg.ClusterRoleName == "" {
		cfg.ClusterRoleName = defaultCfg.ClusterRoleName
	}
	if cfg.LeaderElectionClusterRoleName == "" {
		cfg.LeaderElectionClusterRoleName = defaultCfg.LeaderElectionClusterRoleName
	}
	if cfg.InfrastructureNodeSelector == nil {
		cfg.InfrastructureNodeSelector = defaultCfg.InfrastructureNodeSelector
	}
	if cfg.DeploymentReplicas == 0 {
		cfg.DeploymentReplicas = defaultCfg.DeploymentReplicas
	}
	if cfg.KubeletRootDir == "" {
		cfg.KubeletRootDir = defaultCfg.KubeletRootDir
	}
	return cfg, nil
}
