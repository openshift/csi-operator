package controller

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

func (h *Handler) validateCSIDriverDeployment(instance *v1alpha1.CSIDriverDeployment) field.ErrorList {
	var errs field.ErrorList

	fldPath := field.NewPath("spec")

	errs = append(errs, h.validateDriverName(instance.Spec.DriverName, fldPath.Child("driverName"))...)
	errs = append(errs, h.validateDriverPerNodeTemplate(&instance.Spec.DriverPerNodeTemplate, fldPath.Child("driverPerNodeTemplate"))...)
	errs = append(errs, h.validateDriverControllerTemplate(instance.Spec.DriverControllerTemplate, fldPath.Child("driverControllerTemplate"))...)
	errs = append(errs, h.validateDriverSocket(instance.Spec.DriverSocket, fldPath.Child("driverSocket"))...)
	errs = append(errs, h.validateStorageClassTemplates(instance.Spec.StorageClassTemplates, fldPath.Child("storageClassTemplates"))...)
	errs = append(errs, h.validateNodeUpdateStrategy(instance.Spec.NodeUpdateStrategy, fldPath.Child("nodeUpdateStrategy"))...)
	errs = append(errs, h.validateContainerImages(instance.Spec.ContainerImages, fldPath.Child("containerImages"))...)
	errs = append(errs, h.validateManagementState(instance.Spec.ManagementState, fldPath.Child("managementState"))...)

	return errs
}

func (h *Handler) validateDriverName(driverName string, fldPath *field.Path) field.ErrorList {
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
				"csi-hostpath")))
	}
	return errs
}

func (h *Handler) validateDriverPerNodeTemplate(template *corev1.PodTemplateSpec, fldPath *field.Path) field.ErrorList {
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

func (h *Handler) validateDriverControllerTemplate(template *corev1.PodTemplateSpec, fldPath *field.Path) field.ErrorList {
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

func (h *Handler) validateDriverSocket(driverSocket string, fldPath *field.Path) field.ErrorList {
	errs := field.ErrorList{}
	if driverSocket == "" {
		errs = append(errs, field.Invalid(fldPath, driverSocket, validation.EmptyError()))
	}

	return errs
}

func (h *Handler) validateStorageClassTemplates(templates []v1alpha1.StorageClassTemplate, fldPath *field.Path) field.ErrorList {
	var defaults []string
	errs := field.ErrorList{}

	for i, template := range templates {
		errs = append(errs, h.validateStorageClassTemplate(template, fldPath.Index(i))...)
	}

	if len(defaults) > 1 {
		errs = append(errs, field.Invalid(fldPath, "true", fmt.Sprintf("multiple default storage classes are not supported: %s", strings.Join(defaults, ", "))))
	}
	return errs
}

func (h *Handler) validateStorageClassTemplate(template v1alpha1.StorageClassTemplate, fldPath *field.Path) field.ErrorList {
	errs := field.ErrorList{}
	// TODO: proper validation. Copy from kubernetes?
	return errs
}

func (h *Handler) validateNodeUpdateStrategy(strategy v1alpha1.CSIDeploymentUpdateStrategy, fldPath *field.Path) field.ErrorList {
	errs := field.ErrorList{}

	allowedStrategies := sets.NewString(
		string(v1alpha1.CSIDeploymentUpdateStrategyOnDelete),
		string(v1alpha1.CSIDeploymentUpdateStrategyRolling))

	if !allowedStrategies.Has(string(strategy)) {
		errs = append(errs, field.NotSupported(fldPath, strategy, allowedStrategies.List()))
	}
	return errs
}

func (h *Handler) validateContainerImages(images *v1alpha1.CSIDeploymentContainerImages, fldPath *field.Path) field.ErrorList {
	errs := field.ErrorList{}
	return errs
}

func (h *Handler) validateManagementState(state openshiftapi.ManagementState, fldPath *field.Path) field.ErrorList {
	errs := field.ErrorList{}

	allowedStates := sets.NewString(
		string(openshiftapi.Managed),
		string(openshiftapi.Unmanaged))

	if !allowedStates.Has(string(state)) {
		errs = append(errs, field.NotSupported(fldPath, state, allowedStates.List()))
	}
	return errs
}
