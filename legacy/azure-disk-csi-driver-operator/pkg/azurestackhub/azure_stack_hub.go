package azurestackhub

import (
	"context"
	opCfgV1 "github.com/openshift/api/config/v1"
	opv1 "github.com/openshift/api/operator/v1"
	cfgV1 "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/csi/csidrivernodeservicecontroller"
	"github.com/openshift/library-go/pkg/operator/deploymentcontroller"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resourcesynccontroller"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	appsV1 "k8s.io/api/apps/v1"
	coreV1 "k8s.io/api/core/v1"
	metaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeclient "k8s.io/client-go/kubernetes"
)

const (
	configMapName         = "cloud-provider-config"
	infraConfigName       = "cluster"
	sslName               = "ssl"
	sslHostPath           = "/etc/ssl/certs"
	azureCfgName          = "azure-cfg"
	configEnvName         = "AZURE_ENVIRONMENT_FILEPATH"
	azureStackCloudConfig = "/etc/azure/azurestackcloud.json"
)

func WithAzureStackHubDaemonSetHook(runningOnAzureStackHub bool) csidrivernodeservicecontroller.DaemonSetHookFunc {
	return func(_ *opv1.OperatorSpec, ds *appsV1.DaemonSet) error {
		if runningOnAzureStackHub {
			injectEnvAndMounts(&ds.Spec.Template.Spec)
		}
		return nil
	}
}

func WithAzureStackHubDeploymentHook(runningOnAzureStackHub bool) deploymentcontroller.DeploymentHookFunc {
	return func(_ *opv1.OperatorSpec, deployment *appsV1.Deployment) error {
		if runningOnAzureStackHub {
			injectEnvAndMounts(&deployment.Spec.Template.Spec)
		}
		return nil
	}
}

func injectEnvAndMounts(spec *coreV1.PodSpec) {
	containers := spec.Containers
	for i := range containers {
		c := &spec.Containers[i]
		if c.Name == "csi-driver" {
			c.Env = append(c.Env, coreV1.EnvVar{
				Name:  configEnvName,
				Value: azureStackCloudConfig,
			})
			c.VolumeMounts = append(c.VolumeMounts, coreV1.VolumeMount{
				Name:      azureCfgName,
				MountPath: azureStackCloudConfig,
				SubPath:   "endpoints",
			})
			break
		}
	}

	spec.Volumes = append(spec.Volumes, coreV1.Volume{
		Name: azureCfgName,
		VolumeSource: coreV1.VolumeSource{
			ConfigMap: &coreV1.ConfigMapVolumeSource{
				LocalObjectReference: coreV1.LocalObjectReference{
					Name: configMapName,
				},
			},
		},
	})
}

func RunningOnAzureStackHub(ctx context.Context, configClient cfgV1.ConfigV1Interface) (bool, error) {
	infrastructure, err := configClient.Infrastructures().Get(ctx, infraConfigName, metaV1.GetOptions{})
	if err != nil {
		return false, err
	}

	if infrastructure.Status.PlatformStatus != nil &&
		infrastructure.Status.PlatformStatus.Azure != nil &&
		infrastructure.Status.PlatformStatus.Azure.CloudName == opCfgV1.AzureStackCloud {
		return true, nil
	}

	return false, nil
}

func NewAzureStackHubConfigSyncer(
	defaultNamespace string,
	openShiftConfigNamespace string,
	operatorClient v1helpers.OperatorClient,
	kubeInformers v1helpers.KubeInformersForNamespaces,
	kubeClient kubeclient.Interface,
	eventRecorder events.Recorder,
) (factory.Controller, error) {
	// sync config map with additional trust bundle to the operator namespace,
	// so the operator can get it as a ConfigMap volume.
	srcConfigMap := resourcesynccontroller.ResourceLocation{
		Namespace: openShiftConfigNamespace,
		Name:      configMapName,
	}
	dstConfigMap := resourcesynccontroller.ResourceLocation{
		Namespace: defaultNamespace,
		Name:      configMapName,
	}
	certController := resourcesynccontroller.NewResourceSyncController(
		operatorClient,
		kubeInformers,
		kubeClient.CoreV1(),
		kubeClient.CoreV1(),
		eventRecorder)
	err := certController.SyncConfigMap(dstConfigMap, srcConfigMap)
	if err != nil {
		return nil, err
	}
	return certController, nil
}
