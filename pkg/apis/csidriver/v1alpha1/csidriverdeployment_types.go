package v1alpha1

import (
	openshiftapi "github.com/openshift/api/operator/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// CSIDriverDeploymentList is list of CSIDriverDeployments.
type CSIDriverDeploymentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []CSIDriverDeployment `json:"items"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// CSIDriverDeployment is the Schema for the csidriverdeployments API
type CSIDriverDeployment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CSIDriverDeploymentSpec   `json:"spec,omitempty"`
	Status CSIDriverDeploymentStatus `json:"status,omitempty"`
}

// CSIDriverDeploymentSpec defines the desired state of a CSI driver deployment.
type CSIDriverDeploymentSpec struct {
	// managementState indicates whether and how the operator should manage the component
	ManagementState openshiftapi.ManagementState `json:"managementState"`

	// Name of the CSI driver.
	// Required.
	DriverName string `json:"driverName"`

	// Template of pods that will run on every node. It must contain a
	// container with the driver and all volumes it needs (Secrets,
	// ConfigMaps, ...) Sidecars with driver registrar and liveness probe
	// will be added by the operator.
	// The first container in the pod template is assumed to be the
	// one with the CSI driver. An EmptyDir will be injected into this
	// container with a directory for the CSI driver socket. See
	// DriverSocket.
	// If the CSI driver should run only on specific nodes, this
	// template must have the right node selector.
	// Required.
	DriverPerNodeTemplate corev1.PodTemplateSpec `json:"driverPerNodeTemplate"`

	// Template of pods that will run the controller parts (attacher, provisioner). Nil when
	// the driver does not require any attacher or provisioner. Sidecar container
	// with provisioner and attacher will be added by the operator.
	// The first container in the pod template is assumed to be the
	// one with the CSI driver. An EmptyDir will be injected into this
	// container with a directory for the CSI driver socket. See
	// DriverSocket.
	// Optional.
	DriverControllerTemplate *corev1.PodTemplateSpec `json:"driverControllerTemplate,omitempty"`

	// Path to CSI socket in the containers with CSI driver. In case
	// perNodeTemplate or controllerTemplate have more containers, this
	// is the *first* container. The operator will inject an EmptyDir
	// or HostPath volume into these containers to be able to share the socket to
	// other containers in the pod.
	// Required.
	DriverSocket string `json:"driverSocket"`

	// Period of CSI driver liveness probe. The probe will issue Probe() call every
	// probePeriodSeconds and it will wait for probeTimeoutSeconds for successful response.
	// If the probe fails 3x in a row, the first container in driverPerNodeTemplate or
	// driverControllerTemplate is restarted.
	// When set, the probe is installed both to Deployment with the controller parts and
	// DaemonSet that runs the driver on every node.
	// Note that restarting a CSI driver container may be dangerous if the container
	// runs fuse daemons!
	// No probe is started when the field is not set.
	ProbePeriodSeconds *int32 `json:"probePeriodSeconds,omitempty"`

	// Timeout of CSI driver liveness probe. 30 seconds is used when it's not set and
	// probePeriodSeconds is set.
	ProbeTimeoutSeconds *int32 `json:"probeTimeoutSeconds,omitempty"`

	// Template of storage classes to create. "Provisioner" field will
	// be overwritten with DriverName by the operator.
	// Optional
	StorageClassTemplates []StorageClassTemplate `json:"storageClassTemplates,omitempty"`

	// Strategy of update of the DaemonSet that runs CSI driver on every node.
	// Required
	NodeUpdateStrategy CSIDeploymentUpdateStrategy `json:"nodeUpdateStrategy"`

	// Name of images to use for this CSI driver. Default OpenShift
	// image will be used for empty fields.
	// Optional.
	ContainerImages *CSIDeploymentContainerImages `json:"containerImages,omitempty"`
}

// StorageClassTemplate is a template of a storage class.
type StorageClassTemplate struct {
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Whether the class should be made default. Note that marking a storage class as default will not
	// modify any other default storage class as non-default.
	// Optional, default=false.
	Default *bool `json:"default,omitempty"`

	// Parameters for the provisioner. This is the same as StorageClass.parameters.
	Parameters map[string]string `json:"parameters,omitempty"`

	// reclaimPolicy is the reclaim policy that dynamically provisioned
	// PersistentVolumes of this storage class are created with.
	// Optional.
	// +kubebuilder:validation:Enum=Recycle,Delete,Retain
	ReclaimPolicy *corev1.PersistentVolumeReclaimPolicy `json:"reclaimPolicy,omitempty"`

	// mountOptions are the mount options that dynamically provisioned
	// PersistentVolumes of this storage class are created with
	// Optional.
	MountOptions []string `json:"mountOptions,omitempty"`

	// AllowVolumeExpansion shows whether the storage class allow volume expand
	// If the field is nil or not set, it would amount to expansion disabled
	// for all PVs created from this storageclass.
	// Optional.
	AllowVolumeExpansion *bool `json:"allowVolumeExpansion,omitempty"`

	// VolumeBindingMode indicates how PersistentVolumeClaims should be
	// provisioned and bound.  When unset, VolumeBindingImmediate is used.
	// This field is only honored by servers that enable the VolumeScheduling feature.
	// Optional.
	// +kubebuilder:validation:Enum=Immediate,WaitForFirstConsumer
	VolumeBindingMode *storagev1.VolumeBindingMode `json:"volumeBindingMode,omitempty"`

	// Restrict the node topologies where volumes can be dynamically provisioned.
	// Each volume plugin defines its own supported topology specifications.
	// An empty TopologySelectorTerm list means there is no topology restriction.
	// This field is only honored by servers that enable the VolumeScheduling feature.
	// Optional
	AllowedTopologies []corev1.TopologySelectorTerm `json:"allowedTopologies,omitempty"`
}

// CSIDeploymentUpdateStrategy is deployment strategy applied to DaemonSet with CSI drivers on nodes when CSIDriverDeployment changes.
type CSIDeploymentUpdateStrategy string

const (
	// CSIDeploymentUpdateStrategyRolling indicates that pods with CSI drivers running on nodes will be stopped and new version will be started.
	// This is equivalent to "Rolling" DaemonSet update strategy.
	// BEWARE: This strategy should not be used for CSI drivers that use fuse, as any fuse daemons
	// will be killed during the update!
	CSIDeploymentUpdateStrategyRolling CSIDeploymentUpdateStrategy = "Rolling"

	// CSIDeploymentUpdateStrategyOnDelete indicates that pods with CSI drivers will be updated only when something stops the pod
	// (e.g. node restart or external process). This is equivalent to "OnDelete" DaemonSet update strategy.
	// This strategy should be used for CSI drivers that need to run any long-running processes in their pods,
	// such as fuse daemons.
	CSIDeploymentUpdateStrategyOnDelete CSIDeploymentUpdateStrategy = "OnDelete"

	// TODO: add RollingDrain that drains nodes before performing update of a driver?
)

// CSIDeploymentContainerImages specifies custom sidecar container image names. This should be used only to override the default operator image names.
type CSIDeploymentContainerImages struct {
	// Name of CSI Attacher sidecar container image.
	// Optional.
	AttacherImage *string `json:"attacherImage,omitempty" yaml:"attacherImage"` // Use yaml tags for reading config from a file.

	// Name of CSI Provisioner sidecar container image.
	// Optional.
	ProvisionerImage *string `json:"provisionerImage,omitempty" yaml:"provisionerImage"`

	// Name of CSI Driver Registrar sidecar container image.
	// Optional.
	DriverRegistrarImage *string `json:"driverRegistrarImage,omitempty" yaml:"driverRegistrarImage"`

	// Name of CSI Liveness Probe sidecar container image.
	// Optional.
	LivenessProbeImage *string `json:"livenessProbeImage,omitempty" yaml:"livenessProbeImage"`
}

// CSIDriverDeploymentStatus defines the observed state of a CSI driver deployment.
type CSIDriverDeploymentStatus struct {
	// ObservedGeneration is the last generation of this object that
	// the operator has acted on.
	ObservedGeneration *int64 `json:"observedGeneration,omitempty"`

	// Generation of API objects that the operator has created / updated.
	// For internal operator bookkeeping purposes.
	Children []openshiftapi.GenerationHistory `json:"children,omitempty"`

	// state indicates what the operator has observed to be its current operational status.
	State openshiftapi.ManagementState `json:"state,omitempty"`

	// Conditions is a list of conditions and their status.
	Conditions []openshiftapi.OperatorCondition `json:"conditions,omitempty"`
}

func init() {
	SchemeBuilder.Register(&CSIDriverDeployment{}, &CSIDriverDeploymentList{})
}
