package csidriverdeployment

import (
	"testing"

	openshiftapi "github.com/openshift/api/operator/v1alpha1"
	"github.com/openshift/csi-operator/pkg/apis/csidriver/v1alpha1"
	"k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func TestValidateCSIDriverDeployment(t *testing.T) {
	validCR := &v1alpha1.CSIDriverDeployment{
		Spec: v1alpha1.CSIDriverDeploymentSpec{
			ManagementState:    openshiftapi.Managed,
			DriverName:         "mock",
			DriverSocket:       "/csi/csi.sock",
			NodeUpdateStrategy: "Rolling",
			DriverPerNodeTemplate: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "foo",
						},
					},
				},
			},
			DriverControllerTemplate: &v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name: "foo",
						},
					},
				},
			},
		},
	}

	validStorageClass := v1alpha1.StorageClassTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name: "class",
		},
	}
	policyDelete := v1.PersistentVolumeReclaimDelete
	bindingImmediate := storagev1.VolumeBindingImmediate

	validDefaultStorageClass := v1alpha1.StorageClassTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name: "class1",
		},
		Default:              &bTrue,
		Parameters:           map[string]string{"foo": "bar"},
		ReclaimPolicy:        &policyDelete,
		MountOptions:         []string{"-o", "myopt=foo"},
		AllowVolumeExpansion: &bTrue,
		VolumeBindingMode:    &bindingImmediate,
	}
	validDefaultStorageClass2 := v1alpha1.StorageClassTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name: "class2",
		},
		Default: &bTrue,
	}

	validCRWithClasses := validCR.DeepCopy()
	validCRWithClasses.Spec.StorageClassTemplates = []v1alpha1.StorageClassTemplate{
		validStorageClass,
		validDefaultStorageClass,
	}
	multipleDefaultClasses := validCR.DeepCopy()
	multipleDefaultClasses.Spec.StorageClassTemplates = []v1alpha1.StorageClassTemplate{
		validDefaultStorageClass,
		validDefaultStorageClass2,
	}

	noControllerTemplate := validCR.DeepCopy()
	noControllerTemplate.Spec.DriverControllerTemplate = nil

	invalidDriverNameCR := validCR.DeepCopy()
	invalidDriverNameCR.Spec.DriverName = "$%^&"

	longDriverNameCR := validCR.DeepCopy()
	longDriverNameCR.Spec.DriverName = "too.long.driver.name.012345678901234567890123456789012345678901234567890123456789"

	missingNodeContainer := validCR.DeepCopy()
	missingNodeContainer.Spec.DriverPerNodeTemplate.Spec.Containers = []v1.Container{}

	missingControllerContainer := validCR.DeepCopy()
	missingControllerContainer.Spec.DriverControllerTemplate.Spec.Containers = []v1.Container{}

	missingDriverSocket := validCR.DeepCopy()
	missingDriverSocket.Spec.DriverSocket = ""

	invalidNodeUpdateStrategy := validCR.DeepCopy()
	invalidNodeUpdateStrategy.Spec.NodeUpdateStrategy = v1alpha1.CSIDeploymentUpdateStrategy("unknown")

	missingNodeUpdateStrategy := validCR.DeepCopy()
	missingNodeUpdateStrategy.Spec.NodeUpdateStrategy = v1alpha1.CSIDeploymentUpdateStrategy("")

	invalidManagementState := validCR.DeepCopy()
	invalidManagementState.Spec.ManagementState = openshiftapi.ManagementState("unknown")

	missingManagementState := validCR.DeepCopy()
	missingManagementState.Spec.ManagementState = openshiftapi.ManagementState("")

	minus1 := int32(-1)
	invalidProbePeriod := validCR.DeepCopy()
	invalidProbePeriod.Spec.ProbePeriodSeconds = &minus1

	invalidProbeTimeout := validCR.DeepCopy()
	invalidProbeTimeout.Spec.ProbeTimeoutSeconds = &minus1

	tests := []struct {
		name           string
		cr             *v1alpha1.CSIDriverDeployment
		expectedErrors field.ErrorList
	}{
		{
			name:           "valid deployment",
			cr:             validCR,
			expectedErrors: field.ErrorList{},
		},
		{
			name:           "missing controller template",
			cr:             noControllerTemplate,
			expectedErrors: field.ErrorList{},
		},
		{
			name:           "valid storage classes",
			cr:             validCRWithClasses,
			expectedErrors: field.ErrorList{},
		},
		{
			name:           "multiple default storage classes",
			cr:             multipleDefaultClasses,
			expectedErrors: field.ErrorList{field.Invalid(field.NewPath("spec.storageClassTemplates"), nil, "")},
		},
		{
			name:           "invalid driver name",
			cr:             invalidDriverNameCR,
			expectedErrors: field.ErrorList{field.Invalid(field.NewPath("spec.driverName"), nil, "")},
		},
		{
			name: "too long driver name",
			cr:   longDriverNameCR,
			expectedErrors: field.ErrorList{
				field.TooLong(field.NewPath("spec.driverName"), nil, 63),
				// Length is also checked in the format regexp.
				field.Invalid(field.NewPath("spec.driverName"), nil, ""),
			},
		},
		{
			name:           "missing daemon set template container",
			cr:             missingNodeContainer,
			expectedErrors: field.ErrorList{field.Invalid(field.NewPath("spec.driverPerNodeTemplate"), nil, "")},
		},
		{
			name:           "missing controller template container",
			cr:             missingControllerContainer,
			expectedErrors: field.ErrorList{field.Invalid(field.NewPath("spec.driverControllerTemplate"), nil, "")},
		},
		{
			name:           "missing driverSocket",
			cr:             missingDriverSocket,
			expectedErrors: field.ErrorList{field.Invalid(field.NewPath("spec.driverSocket"), nil, "")},
		},
		{
			name:           "invalid update strategy",
			cr:             invalidNodeUpdateStrategy,
			expectedErrors: field.ErrorList{field.NotSupported(field.NewPath("spec.nodeUpdateStrategy"), nil, nil)},
		},
		{
			name:           "missing update strategy",
			cr:             missingNodeUpdateStrategy,
			expectedErrors: field.ErrorList{field.NotSupported(field.NewPath("spec.nodeUpdateStrategy"), nil, nil)},
		},
		{
			name:           "invalid management state",
			cr:             invalidManagementState,
			expectedErrors: field.ErrorList{field.NotSupported(field.NewPath("spec.managementState"), nil, nil)},
		},
		{
			name:           "missing management state",
			cr:             missingManagementState,
			expectedErrors: field.ErrorList{field.NotSupported(field.NewPath("spec.managementState"), nil, nil)},
		},
		{
			name:           "invalid probe period",
			cr:             invalidProbePeriod,
			expectedErrors: field.ErrorList{field.Invalid(field.NewPath("spec.probePeriodSeconds"), nil, "")},
		},
		{
			name:           "invalid probe timeout",
			cr:             invalidProbeTimeout,
			expectedErrors: field.ErrorList{field.Invalid(field.NewPath("spec.probeTimeoutSeconds"), nil, "")},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := &ReconcileCSIDriverDeployment{
				config: testConfig,
			}
			errs := handler.validateCSIDriverDeployment(test.cr)

			ok := errorMatch(errs, test.expectedErrors, t)
			if !ok {
				t.Error("Errors do not match")
				t.Log("Expected errors:")
				for _, err := range test.expectedErrors {
					t.Logf("%s", err)
				}
				t.Log("Received errors:")
				for _, err := range errs {
					t.Logf("%s", err)
				}
			}
		})
	}
}

func errorMatch(actual, expected field.ErrorList, t *testing.T) bool {
	if len(actual) != len(expected) {
		return false
	}

	ok := true

	for i := range actual {
		actualError := actual[i]
		expectedError := expected[i]
		if actualError.Type != expectedError.Type {
			t.Errorf("Error %d: expected type %q, got %q", i, expectedError.Type, actualError.Type)
			ok = false
		}
		if actualError.Field != expectedError.Field {
			t.Errorf("Error %d: expected field %q, got %q", i, expectedError.Field, actualError.Field)
			ok = false
		}
	}
	return ok
}
