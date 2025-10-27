package operator

import (
	"os"

	"github.com/openshift/library-go/pkg/operator/status"
)

const (
	driverImageEnvName        = "DRIVER_IMAGE"
	provisionerImageEnvName   = "PROVISIONER_IMAGE"
	attacherImageEnvName      = "ATTACHER_IMAGE"
	resizerImageEnvName       = "RESIZER_IMAGE"
	snapshotterImageEnvName   = "SNAPSHOTTER_IMAGE"
	livenessProbeImageEnvName = "LIVENESS_PROBE_IMAGE"
	kubeRBACProxyImageEnvName = "KUBE_RBAC_PROXY_IMAGE"
	toolsImageEnvName         = "TOOLS_IMAGE"
	hyperShiftImageEnvName    = "HYPERSHIFT_IMAGE"
)

func DefaultReplacements(controlPlaneNamespace, guestNamespace string) []string {
	pairs := []string{}

	// Replace container images by env vars if they are set
	csiDriver := os.Getenv(driverImageEnvName)
	if csiDriver != "" {
		pairs = append(pairs, []string{"${DRIVER_IMAGE}", csiDriver}...)
	}

	provisioner := os.Getenv(provisionerImageEnvName)
	if provisioner != "" {
		pairs = append(pairs, []string{"${PROVISIONER_IMAGE}", provisioner}...)
	}

	attacher := os.Getenv(attacherImageEnvName)
	if attacher != "" {
		pairs = append(pairs, []string{"${ATTACHER_IMAGE}", attacher}...)
	}

	resizer := os.Getenv(resizerImageEnvName)
	if resizer != "" {
		pairs = append(pairs, []string{"${RESIZER_IMAGE}", resizer}...)
	}

	snapshotter := os.Getenv(snapshotterImageEnvName)
	if snapshotter != "" {
		pairs = append(pairs, []string{"${SNAPSHOTTER_IMAGE}", snapshotter}...)
	}

	livenessProbe := os.Getenv(livenessProbeImageEnvName)
	if livenessProbe != "" {
		pairs = append(pairs, []string{"${LIVENESS_PROBE_IMAGE}", livenessProbe}...)
	}

	kubeRBACProxy := os.Getenv(kubeRBACProxyImageEnvName)
	if kubeRBACProxy != "" {
		pairs = append(pairs, []string{"${KUBE_RBAC_PROXY_IMAGE}", kubeRBACProxy}...)
	}

	tools := os.Getenv(toolsImageEnvName)
	if tools != "" {
		pairs = append(pairs, []string{"${TOOLS_IMAGE}", tools}...)
	}

	hyperShiftImage := os.Getenv(hyperShiftImageEnvName)
	if csiDriver != "" {
		pairs = append(pairs, []string{"${HYPERSHIFT_IMAGE}", hyperShiftImage}...)
	}

	pairs = append(pairs, []string{"${NAMESPACE}", controlPlaneNamespace}...)
	pairs = append(pairs, []string{"${NODE_NAMESPACE}", guestNamespace}...)
	pairs = append(pairs, []string{"${RELEASE_VERSION}", status.VersionForOperatorFromEnv()}...)
	return pairs
}
