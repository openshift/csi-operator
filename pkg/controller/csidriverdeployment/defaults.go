package csidriverdeployment

import (
	"github.com/openshift/csi-operator/pkg/apis/csidriver/v1alpha1"
)

func (r *ReconcileCSIDriverDeployment) applyDefaults(instance *v1alpha1.CSIDriverDeployment) {
	if instance.Spec.ManagementState == "" {
		instance.Spec.ManagementState = "Managed"
	}
}
