package azurestackhub

import (
	"context"
	"testing"

	cfgV1 "github.com/openshift/api/config/v1"
	"github.com/openshift/azure-disk-csi-driver-operator/assets"
	"github.com/openshift/client-go/config/clientset/versioned/fake"
	"github.com/stretchr/testify/assert"
	appsV1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func TestAzureStackHubDetectionHappyPath(t *testing.T) {
	ctx := context.Background()
	infrastructure := mockInfra(cfgV1.AzureStackCloud)
	cfg := fake.NewSimpleClientset(infrastructure).ConfigV1()

	hub, err := RunningOnAzureStackHub(ctx, cfg)
	assert.Nil(t, err)
	assert.True(t, hub)
}

func TestAzureStackHubDetectionOnAzurePublicCloud(t *testing.T) {
	ctx := context.Background()
	infrastructure := mockInfra(cfgV1.AzurePublicCloud)
	cfg := fake.NewSimpleClientset(infrastructure).ConfigV1()

	hub, err := RunningOnAzureStackHub(ctx, cfg)
	assert.Nil(t, err)
	assert.False(t, hub)
}

func TestInjectPodSpecHappyPath(t *testing.T) {
	file, err := assets.ReadFile("controller.yaml")
	assert.Nil(t, err)
	dep := &appsV1.Deployment{}
	assert.Nil(t, yaml.Unmarshal(file, dep))

	injectEnvAndMounts(&dep.Spec.Template.Spec)
	assert.Len(t, dep.Spec.Template.Spec.Volumes, 7)
	foundCfgVolume := false
	for _, v := range dep.Spec.Template.Spec.Volumes {
		if v.Name == azureCfgName {
			assert.Equal(t, configMapName, v.VolumeSource.ConfigMap.Name)
			foundCfgVolume = true
		}
	}
	assert.Truef(t, foundCfgVolume, "did not find volume with name %s", azureCfgName)

	var csiDriver v1.Container
	for _, c := range dep.Spec.Template.Spec.Containers {
		if c.Name == "csi-driver" {
			csiDriver = c
			break
		}
	}
	assert.NotNil(t, csiDriver, "no csi-driver container found")
	assert.Len(t, csiDriver.VolumeMounts, 5)
	foundCfgVolumeMount := false
	for _, v := range csiDriver.VolumeMounts {
		if v.Name == azureCfgName {
			assert.Equal(t, azureStackCloudConfig, v.MountPath)
			foundCfgVolumeMount = true
		}
	}
	assert.Truef(t, foundCfgVolumeMount, "did not find volume mount with name %s", azureCfgName)

	foundStackEnv := false
	for _, env := range csiDriver.Env {
		if env.Name == configEnvName {
			foundStackEnv = true
			assert.Equal(t, azureStackCloudConfig, env.Value)
			break
		}
	}
	assert.Truef(t, foundStackEnv, "did not find env with name %s", configEnvName)
}

func mockInfra(cloudName cfgV1.AzureCloudEnvironment) *cfgV1.Infrastructure {
	infrastructure := &cfgV1.Infrastructure{
		ObjectMeta: metaV1.ObjectMeta{
			Name: infraConfigName,
		},
		Status: cfgV1.InfrastructureStatus{
			PlatformStatus: &cfgV1.PlatformStatus{
				Azure: &cfgV1.AzurePlatformStatus{
					CloudName: cloudName,
				},
			},
		},
	}
	return infrastructure
}
