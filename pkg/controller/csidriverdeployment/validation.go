package csidriverdeployment

import (
	"fmt"
	"regexp"
	"strings"

	openshiftapi "github.com/openshift/api/operator/v1alpha1"
	"github.com/openshift/csi-operator/pkg/apis/csidriver/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

const (
	csiDriverNameRexpFmt string = `^[a-zA-Z0-9][-a-zA-Z0-9_.]{0,61}[a-zA-Z-0-9]$`
	maxCSIDriverName     int    = 63
)

var (
	csiDriverNameRexp = regexp.MustCompile(csiDriverNameRexpFmt)
)

func (r *ReconcileCSIDriverDeployment) validateCSIDriverDeployment(instance *v1alpha1.CSIDriverDeployment) field.ErrorList {
	var errs field.ErrorList

	fldPath := field.NewPath("spec")

	errs = append(errs, r.validateDriverName(instance.Spec.DriverName, fldPath.Child("driverName"))...)
	errs = append(errs, r.validateDriverPerNodeTemplate(&instance.Spec.DriverPerNodeTemplate, fldPath.Child("driverPerNodeTemplate"))...)
	errs = append(errs, r.validateDriverControllerTemplate(instance.Spec.DriverControllerTemplate, fldPath.Child("driverControllerTemplate"))...)
	errs = append(errs, r.validateDriverSocket(instance.Spec.DriverSocket, fldPath.Child("driverSocket"))...)
	errs = append(errs, r.validateStorageClassTemplates(instance.Spec.StorageClassTemplates, fldPath.Child("storageClassTemplates"))...)
	errs = append(errs, r.validateNodeUpdateStrategy(instance.Spec.NodeUpdateStrategy, fldPath.Child("nodeUpdateStrategy"))...)
	errs = append(errs, r.validateContainerImages(instance.Spec.ContainerImages, fldPath.Child("containerImages"))...)
	errs = append(errs, r.validateManagementState(instance.Spec.ManagementState, fldPath.Child("managementState"))...)
	errs = append(errs, r.validatePositiveInteger(instance.Spec.ProbePeriodSeconds, fldPath.Child("probePeriodSeconds"))...)
	errs = append(errs, r.validatePositiveInteger(instance.Spec.ProbeTimeoutSeconds, fldPath.Child("probeTimeoutSeconds"))...)
	return errs
}

func (r *ReconcileCSIDriverDeployment) validateDriverName(driverName string, fldPath *field.Path) field.ErrorList {
	errs := field.ErrorList{}

	if len(driverName) > maxCSIDriverName {
		errs = append(errs, field.TooLong(fldPath, driverName, maxCSIDriverName))
	}

	if !csiDriverNameRexp.MatchString(driverName) {
		errs = append(errs, field.Invalid(
			fldPath,
			driverName,
			validation.RegexError(
				"must consist of alphanumeric characters, '-', '_' or '.', and must start and end with an alphanumeric character",
				csiDriverNameRexpFmt,
				"org.acme.csi-hostpath")))
	}
	return errs
}

func (r *ReconcileCSIDriverDeployment) validateDriverPerNodeTemplate(template *corev1.PodTemplateSpec, fldPath *field.Path) field.ErrorList {
	errs := field.ErrorList{}
	// We require at least one container. We can't really validate the rest, because podSpec is too big.
	if len(template.Spec.Containers) == 0 {
		errs = append(errs, field.Invalid(
			fldPath,
			template.Spec.Containers,
			validation.EmptyError()))
	}
	return errs
}

func (r *ReconcileCSIDriverDeployment) validateDriverControllerTemplate(template *corev1.PodTemplateSpec, fldPath *field.Path) field.ErrorList {
	errs := field.ErrorList{}
	if template == nil {
		// ControllerTemplate is optional.
		return errs
	}
	// We require at least one container. We can't really validate the rest, because podSpec is too big.
	if len(template.Spec.Containers) == 0 {
		errs = append(errs, field.Invalid(
			fldPath,
			template.Spec.Containers,
			validation.EmptyError()))
	}
	return errs
}

func (r *ReconcileCSIDriverDeployment) validateDriverSocket(driverSocket string, fldPath *field.Path) field.ErrorList {
	errs := field.ErrorList{}
	if driverSocket == "" {
		errs = append(errs, field.Invalid(fldPath, driverSocket, validation.EmptyError()))
	}

	return errs
}

func (r *ReconcileCSIDriverDeployment) validateStorageClassTemplates(templates []v1alpha1.StorageClassTemplate, fldPath *field.Path) field.ErrorList {
	var defaults []string
	errs := field.ErrorList{}

	for i, template := range templates {
		errs = append(errs, r.validateStorageClassTemplate(template, fldPath.Index(i))...)
		if template.Default != nil && *template.Default {
			defaults = append(defaults, template.Name)
		}
	}

	if len(defaults) > 1 {
		errs = append(errs, field.Invalid(fldPath, "true", fmt.Sprintf("multiple default storage classes are not supported: %s", strings.Join(defaults, ", "))))
	}
	return errs
}

func (r *ReconcileCSIDriverDeployment) validateStorageClassTemplate(template v1alpha1.StorageClassTemplate, fldPath *field.Path) field.ErrorList {
	errs := field.ErrorList{}
	// TODO: proper validation. Copy from kubernetes?
	return errs
}

func (r *ReconcileCSIDriverDeployment) validateNodeUpdateStrategy(strategy v1alpha1.CSIDeploymentUpdateStrategy, fldPath *field.Path) field.ErrorList {
	errs := field.ErrorList{}

	allowedStrategies := sets.NewString(
		string(v1alpha1.CSIDeploymentUpdateStrategyOnDelete),
		string(v1alpha1.CSIDeploymentUpdateStrategyRolling))

	if !allowedStrategies.Has(string(strategy)) {
		errs = append(errs, field.NotSupported(fldPath, strategy, allowedStrategies.List()))
	}
	return errs
}

func (r *ReconcileCSIDriverDeployment) validateContainerImages(images *v1alpha1.CSIDeploymentContainerImages, fldPath *field.Path) field.ErrorList {
	errs := field.ErrorList{}
	return errs
}

func (r *ReconcileCSIDriverDeployment) validateManagementState(state openshiftapi.ManagementState, fldPath *field.Path) field.ErrorList {
	errs := field.ErrorList{}

	allowedStates := sets.NewString(
		string(openshiftapi.Managed),
		string(openshiftapi.Unmanaged))

	if !allowedStates.Has(string(state)) {
		errs = append(errs, field.NotSupported(fldPath, state, allowedStates.List()))
	}
	return errs
}

func (r *ReconcileCSIDriverDeployment) validatePositiveInteger(value *int32, fldPath *field.Path) field.ErrorList {
	errs := field.ErrorList{}
	if value == nil {
		return errs
	}
	if *value <= 0 {
		errs = append(errs, field.Invalid(fldPath, *value, "must be positive integer number"))
	}
	return errs
}
