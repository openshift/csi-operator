package util

import (
	"os"

	opv1 "github.com/openshift/api/operator/v1"
	dc "github.com/openshift/library-go/pkg/operator/deploymentcontroller"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// WithAROHCPSidecarContainers injects the Microsoft Managed Identity sidecars needed for ARO HCP deployments on the
// hosted control plane.
func WithAROHCPSidecarContainers() dc.DeploymentHookFunc {
	hook := func(_ *opv1.OperatorSpec, deployment *appsv1.Deployment) error {
		// Add the adapter-init container
		if deployment.Spec.Template.Spec.InitContainers == nil {
			deployment.Spec.Template.Spec.InitContainers = []corev1.Container{}
		}
		deployment.Spec.Template.Spec.InitContainers = append(deployment.Spec.Template.Spec.InitContainers, adapterInitContainer(os.Getenv("AZURE_ADAPTER_INIT_IMAGE")))

		// Add adapter-server sidecar container
		deployment.Spec.Template.Spec.Containers = append(deployment.Spec.Template.Spec.Containers,
			adapterServerContainer(
				os.Getenv("AZURE_ADAPTER_SERVER_IMAGE"),
				os.Getenv("ARO_HCP_DISK_MI_CLIENT_ID"),
				os.Getenv("CLIENT_ID_SECRET"),
				os.Getenv("TENANT_ID")))

		return nil
	}
	return hook
}

// adapterInitContainer returns the Microsoft adapter-init init container. This container needs the NET_ADMIN permission
// so the adapter-server sidecar container can intercept the Managed Identity Azure API authentication calls.
func adapterInitContainer(adapterImage string) corev1.Container {
	return corev1.Container{
		Name:            "adapter-init",
		Image:           adapterImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		SecurityContext: &corev1.SecurityContext{
			Capabilities: &corev1.Capabilities{
				Add: []corev1.Capability{
					"NET_ADMIN",
				},
			},
		}}
}

// adapterServerContainer returns the Microsoft adapter-server sidecar container. Currently, this container mimics Azure
// Managed Identity approval and returns an authentication token. The container currently needs a Service Principal to
// do this. Future versions of this container will be able to take a Managed Identity instead.
func adapterServerContainer(adapterImage, clientID, clientSecret, tenantID string) corev1.Container {
	return corev1.Container{Name: "adapter-server",
		Image:           adapterImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Args:            []string{"sp"},
		Env: []corev1.EnvVar{
			{
				Name:  "AZURE_CLIENT_ID",
				Value: clientID,
			},
			{
				Name:  "AZURE_CLIENT_SECRET",
				Value: clientSecret,
			},
			{
				Name:  "AZURE_TENANT_ID",
				Value: tenantID,
			},
		}}
}
