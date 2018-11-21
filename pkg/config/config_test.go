package config

import (
	"io/ioutil"
	"os"
	"reflect"
	"testing"

	csi "github.com/openshift/csi-operator/pkg/apis/csidriver/v1alpha1"
)

func str2ptr(s string) *string {
	return &s
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	expectedCfg := &Config{
		DefaultImages: csi.CSIDeploymentContainerImages{
			AttacherImage:        str2ptr("quay.io/k8scsi/csi-attacher:v0.3.0"),
			ProvisionerImage:     str2ptr("quay.io/k8scsi/csi-provisioner:v0.3.1"),
			DriverRegistrarImage: str2ptr("quay.io/k8scsi/driver-registrar:v0.3.0"),
			LivenessProbeImage:   str2ptr("quay.io/k8scsi/livenessprobe:v0.4.1"),
		},
		InfrastructureNodeSelector:    nil,
		DeploymentReplicas:            1,
		ClusterRoleName:               "system:openshift:csi-driver",
		LeaderElectionClusterRoleName: "system:openshift:csi-driver-controller-leader-election",
		KubeletRootDir:                "/var/lib/kubelet",
	}

	if !reflect.DeepEqual(cfg, expectedCfg) {
		t.Errorf("Unexpected default config: expected:\n%+v \ngot:\n%+v", expectedCfg, cfg)
	}
}

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name           string
		configFile     string
		expectedConfig *Config
		expectError    bool
	}{
		{
			name:           "empty config file",
			configFile:     "",
			expectedConfig: DefaultConfig(),
		},
		{
			name: "full config",
			configFile: `
defaultImages:
  attacherImage: "my-attacher"
  provisionerImage: "my-provisioner"
  driverRegistrarImage: "my-registrar"
  livenessProbeImage: "my-probe"
infrastructureNodeSelector:
  foo: bar
deploymentReplicas: 999
clusterRoleName: my-role
leaderElectionClusterRoleName: my-leader-election-role
kubeletRootDir: /var/lib/my-kubelet
`,
			expectedConfig: &Config{
				DefaultImages: csi.CSIDeploymentContainerImages{
					AttacherImage:        str2ptr("my-attacher"),
					ProvisionerImage:     str2ptr("my-provisioner"),
					DriverRegistrarImage: str2ptr("my-registrar"),
					LivenessProbeImage:   str2ptr("my-probe"),
				},
				InfrastructureNodeSelector:    map[string]string{"foo": "bar"},
				DeploymentReplicas:            999,
				ClusterRoleName:               "my-role",
				LeaderElectionClusterRoleName: "my-leader-election-role",
				KubeletRootDir:                "/var/lib/my-kubelet",
			},
		},
		{
			name: "merge config",
			configFile: `
defaultImages:
  attacherImage: "my-attacher"
  livenessProbeImage: "my-probe"
infrastructureNodeSelector:
    
deploymentReplicas: 999
kubeletRootDir: /var/lib/my-kubelet
`,
			expectedConfig: &Config{
				DefaultImages: csi.CSIDeploymentContainerImages{
					AttacherImage:        str2ptr("my-attacher"),
					ProvisionerImage:     str2ptr("quay.io/k8scsi/csi-provisioner:v0.3.1"),
					DriverRegistrarImage: str2ptr("quay.io/k8scsi/driver-registrar:v0.3.0"),
					LivenessProbeImage:   str2ptr("my-probe"),
				},
				InfrastructureNodeSelector:    nil,
				DeploymentReplicas:            999,
				ClusterRoleName:               "system:openshift:csi-driver",
				LeaderElectionClusterRoleName: "system:openshift:csi-driver-controller-leader-election",
				KubeletRootDir:                "/var/lib/my-kubelet",
			},
		},
		{
			name: "unknown field",
			configFile: `
XYZ: ABC
kubeletRootDir: /var/lib/my-kubelet
`,
			expectError: true,
		},
		{
			name: "invalid number",
			configFile: `
deploymentReplicas: 999xyz
`,
			expectError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f, err := ioutil.TempFile("", "load-config-XXXXXXX")
			if err != nil {
				t.Fatal(err)
			}
			path := f.Name()
			defer os.Remove(path)
			defer f.Close()

			_, err = f.WriteString(test.configFile)
			if err != nil {
				t.Errorf("Error writing %s: %s", f.Name(), err)
			}
			f.Close()

			cfg, err := LoadConfig(path)
			if err != nil && !test.expectError {
				t.Errorf("Unexpected error: %s", err)
			}
			if err == nil && test.expectError {
				t.Errorf("Expected error, got none")
			}
			if !reflect.DeepEqual(cfg, test.expectedConfig) {
				t.Errorf("Unexpected config: expected:\n%+v \ngot:\n%+v", test.expectedConfig, cfg)
			}
		})
	}
}
