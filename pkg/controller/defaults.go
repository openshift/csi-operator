package controller

import (
	"github.com/openshift/csi-operator/pkg/apis/csidriver/v1alpha1"
)

func (h *Handler) applyDefaults(instance *v1alpha1.CSIDriverDeployment) {
	if instance.Spec.ManagementState == "" {
		instance.Spec.ManagementState = "Managed"
	}
}
