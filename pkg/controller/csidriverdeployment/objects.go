package csidriverdeployment

import (
	"path"
	"regexp"

	csidriverv1alpha1 "github.com/openshift/csi-operator/pkg/apis/csidriver/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	daemonSetLabel  = "csidriver.storage.openshift.io/daemonset"
	deploymentLabel = "csidriver.storage.openshift.io/deployment"

	defaultStorageClassAnnotation = "storageclass.kubernetes.io/is-default-class"

	// Port where livenessprobe listens
	livenessprobePort = 9808

	// How many probe failures lead to driver restart
	livenessprobeFailureThreshold = 3

	// Default timeout of the probe (in seconds)
	livenessprobeDefaultTimeout = int32(30)

	// Name of volume with CSI driver socket
	driverSocketVolume = "csi-driver"

	// Path where driverSocketVolume is mounted into all sidecar container
	driverSocketVolumeMountPath = "/csi"

	// Name of volume with /var/lib/kubelet
	kubeletRootVolumeName = "kubelet-root"
)

// generateServiceAccount prepares a ServiceAccount that will be used by all pods (controller + daemon set) with
// CSI drivers and its sidecar containers.
func (r *ReconcileCSIDriverDeployment) generateServiceAccount(cr *csidriverv1alpha1.CSIDriverDeployment) *v1.ServiceAccount {
	scName := cr.Name

	sc := &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: cr.Namespace,
			Name:      scName,
		},
	}
	r.addOwnerLabels(&sc.ObjectMeta, cr)
	r.addOwner(&sc.ObjectMeta, cr)

	return sc
}

// generateClusterRoleBinding prepares a ClusterRoleBinding that gives a ServiceAccount privileges needed by
// sidecar containers.
func (r *ReconcileCSIDriverDeployment) generateClusterRoleBinding(cr *csidriverv1alpha1.CSIDriverDeployment, serviceAccount *v1.ServiceAccount) *rbacv1.ClusterRoleBinding {
	crbName := r.uniqueGlobalName(cr)
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: crbName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      serviceAccount.Name,
				Namespace: serviceAccount.Namespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     r.config.ClusterRoleName,
		},
	}
	r.addOwnerLabels(&crb.ObjectMeta, cr)
	return crb
}

// generateLeaderElectionRoleBinding prepares a RoleBinding that gives a ServiceAccount privileges needed by
// attacher and provisioner leader election.
func (r *ReconcileCSIDriverDeployment) generateLeaderElectionRoleBinding(cr *csidriverv1alpha1.CSIDriverDeployment, serviceAccount *v1.ServiceAccount) *rbacv1.RoleBinding {
	rbName := "leader-election-" + cr.Name
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: cr.Namespace,
			Name:      rbName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      serviceAccount.Name,
				Namespace: serviceAccount.Namespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     r.config.LeaderElectionClusterRoleName,
		},
	}
	r.addOwnerLabels(&rb.ObjectMeta, cr)
	r.addOwner(&rb.ObjectMeta, cr)
	return rb
}

// generateDaemonSet prepares a DaemonSet with CSI driver and driver registrar sidecar containers.
func (r *ReconcileCSIDriverDeployment) generateDaemonSet(cr *csidriverv1alpha1.CSIDriverDeployment, serviceAccount *v1.ServiceAccount) *appsv1.DaemonSet {
	dsName := cr.Name + "-node"

	labels := map[string]string{
		daemonSetLabel: dsName,
	}

	// Prepare DS.Spec.PodSpec
	podSpec := cr.Spec.DriverPerNodeTemplate.DeepCopy()
	if podSpec.Labels == nil {
		podSpec.Labels = labels
	} else {
		for k, v := range labels {
			podSpec.Labels[k] = v
		}
	}

	// Don't overwrite user's ServiceAccount
	if podSpec.Spec.ServiceAccountName == "" {
		podSpec.Spec.ServiceAccountName = serviceAccount.Name
	}

	// Path to the CSI driver socket in the driver container
	csiDriverSocketPath := cr.Spec.DriverSocket
	csiDriverSocketFileName := path.Base(csiDriverSocketPath)
	csiDriverSocketDirectory := path.Dir(csiDriverSocketPath)

	// Path to the CSI driver socket in the driver registrar container
	registrarSocketDirectory := driverSocketVolumeMountPath
	registrarSocketPath := path.Join(registrarSocketDirectory, csiDriverSocketFileName)

	// Path to the CSI driver socket from kubelet point of view
	kubeletSocketDirectory := path.Join(r.config.KubeletRootDir, "plugins", sanitizeDriverName(cr.Spec.DriverName))
	kubeletSocketPath := path.Join(kubeletSocketDirectory, csiDriverSocketFileName)

	// Path to the kubelet dynamic registration directory
	kubeletRegistrationDirectory := path.Join(r.config.KubeletRootDir, "plugins")

	// Add CSI Registrar sidecar
	registrarImage := *r.config.DefaultImages.DriverRegistrarImage
	if cr.Spec.ContainerImages != nil && cr.Spec.ContainerImages.DriverRegistrarImage != nil {
		registrarImage = *cr.Spec.ContainerImages.DriverRegistrarImage
	}
	registrar := v1.Container{
		Name:  "csi-driver-registrar",
		Image: registrarImage,
		Args: []string{
			"--v=5",
			"--csi-address=$(ADDRESS)",
			"--kubelet-registration-path=$(DRIVER_REG_SOCK_PATH)",
		},
		Env: []v1.EnvVar{
			{
				Name:  "ADDRESS",
				Value: registrarSocketPath,
			},
			{
				Name:  "DRIVER_REG_SOCK_PATH",
				Value: kubeletSocketPath,
			},
			{
				Name: "KUBE_NODE_NAME",
				ValueFrom: &v1.EnvVarSource{
					FieldRef: &v1.ObjectFieldSelector{
						FieldPath: "spec.nodeName",
					},
				},
			},
		},
		VolumeMounts: []v1.VolumeMount{
			{
				Name:      driverSocketVolume,
				MountPath: registrarSocketDirectory,
			},
			{
				Name:      "registration-dir",
				MountPath: "/registration",
			},
		},
	}
	if podSpec.Spec.Containers[0].SecurityContext != nil {
		registrar.SecurityContext = podSpec.Spec.Containers[0].SecurityContext.DeepCopy()
	}
	podSpec.Spec.Containers = append(podSpec.Spec.Containers, registrar)

	probeSocketPath := path.Join(driverSocketVolumeMountPath, csiDriverSocketFileName)
	r.addLivenessProbe(cr, podSpec, probeSocketPath)

	// Add volumes
	typeDir := v1.HostPathDirectory
	typeDirOrCreate := v1.HostPathDirectoryOrCreate
	volumes := []v1.Volume{
		{
			Name: "registration-dir",
			VolumeSource: v1.VolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: kubeletRegistrationDirectory,
					Type: &typeDir,
				},
			},
		},
		{
			Name: driverSocketVolume,
			VolumeSource: v1.VolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: kubeletSocketDirectory,
					Type: &typeDirOrCreate,
				},
			},
		},
		{
			Name: kubeletRootVolumeName,
			VolumeSource: v1.VolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: r.config.KubeletRootDir,
					Type: &typeDir,
				},
			},
		},
	}
	podSpec.Spec.Volumes = append(podSpec.Spec.Volumes, volumes...)

	// Patch the driver container with the volume for CSI driver socket
	bidirectional := v1.MountPropagationBidirectional
	volumeMounts := []v1.VolumeMount{
		{
			Name:      driverSocketVolume,
			MountPath: csiDriverSocketDirectory,
		},
		{
			Name:             kubeletRootVolumeName,
			MountPath:        r.config.KubeletRootDir,
			MountPropagation: &bidirectional,
		},
	}
	driverContainer := &podSpec.Spec.Containers[0]
	driverContainer.VolumeMounts = append(driverContainer.VolumeMounts, volumeMounts...)

	// Create the DaemonSet
	updateStrategy := appsv1.OnDeleteDaemonSetStrategyType
	if cr.Spec.NodeUpdateStrategy == csidriverv1alpha1.CSIDeploymentUpdateStrategyRolling {
		updateStrategy = appsv1.RollingUpdateDaemonSetStrategyType
	}
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: cr.Namespace,
			Name:      dsName,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: *podSpec,
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type: updateStrategy,
			},
		},
	}
	r.addOwnerLabels(&ds.ObjectMeta, cr)
	r.addOwner(&ds.ObjectMeta, cr)

	return ds
}

// generateDeployment prepares a Deployment with CSI driver and attacher and provisioner sidecar containers.
func (r *ReconcileCSIDriverDeployment) generateDeployment(cr *csidriverv1alpha1.CSIDriverDeployment, serviceAccount *v1.ServiceAccount) *appsv1.Deployment {
	dName := cr.Name + "-controller"

	labels := map[string]string{
		deploymentLabel: dName,
	}

	// Prepare the pod template
	podSpec := cr.Spec.DriverControllerTemplate.DeepCopy()
	if podSpec.Labels == nil {
		podSpec.Labels = labels
	} else {
		for k, v := range labels {
			podSpec.Labels[k] = v
		}
	}

	if podSpec.Spec.ServiceAccountName == "" {
		podSpec.Spec.ServiceAccountName = serviceAccount.Name
	}

	// Add sidecars

	// Path to the CSI driver socket in the driver container
	csiDriverSocketPath := cr.Spec.DriverSocket
	csiDriverSocketFileName := path.Base(csiDriverSocketPath)
	csiDriverSocketDirectory := path.Dir(csiDriverSocketPath)

	// Path to the CSI driver socket in the sidecar containers
	sidecarSocketDirectory := driverSocketVolumeMountPath
	sidecarSocketPath := path.Join(sidecarSocketDirectory, csiDriverSocketFileName)

	provisionerImage := *r.config.DefaultImages.ProvisionerImage
	if cr.Spec.ContainerImages != nil && cr.Spec.ContainerImages.ProvisionerImage != nil {
		provisionerImage = *cr.Spec.ContainerImages.ProvisionerImage
	}
	provisioner := v1.Container{
		Name:  "csi-provisioner",
		Image: provisionerImage,
		Args: []string{
			"--v=5",
			"--csi-address=$(ADDRESS)",
			"--provisioner=" + cr.Spec.DriverName,
			// TODO: add leader election parameters
		},
		Env: []v1.EnvVar{
			{
				Name:  "ADDRESS",
				Value: sidecarSocketPath,
			},
		},
		VolumeMounts: []v1.VolumeMount{
			{
				Name:      driverSocketVolume,
				MountPath: driverSocketVolumeMountPath,
			},
		},
	}
	if podSpec.Spec.Containers[0].SecurityContext != nil {
		provisioner.SecurityContext = podSpec.Spec.Containers[0].SecurityContext.DeepCopy()
	}
	podSpec.Spec.Containers = append(podSpec.Spec.Containers, provisioner)

	attacherImage := *r.config.DefaultImages.AttacherImage
	if cr.Spec.ContainerImages != nil && cr.Spec.ContainerImages.AttacherImage != nil {
		attacherImage = *cr.Spec.ContainerImages.AttacherImage
	}
	attacher := v1.Container{
		Name:  "csi-attacher",
		Image: attacherImage,
		Args: []string{
			"--v=5",
			"--csi-address=$(ADDRESS)",
			// TODO: add leader election parameters
		},
		Env: []v1.EnvVar{
			{
				Name:  "ADDRESS",
				Value: sidecarSocketPath,
			},
		},
		VolumeMounts: []v1.VolumeMount{
			{
				Name:      driverSocketVolume,
				MountPath: driverSocketVolumeMountPath,
			},
		},
	}
	if podSpec.Spec.Containers[0].SecurityContext != nil {
		attacher.SecurityContext = podSpec.Spec.Containers[0].SecurityContext.DeepCopy()
	}
	podSpec.Spec.Containers = append(podSpec.Spec.Containers, attacher)

	r.addLivenessProbe(cr, podSpec, sidecarSocketPath)

	// Add volumes
	volumes := []v1.Volume{
		{
			Name: driverSocketVolume,
			VolumeSource: v1.VolumeSource{
				EmptyDir: &v1.EmptyDirVolumeSource{},
			},
		},
	}
	podSpec.Spec.Volumes = append(podSpec.Spec.Volumes, volumes...)

	// Set selector to infra nodes only
	if podSpec.Spec.NodeSelector == nil {
		podSpec.Spec.NodeSelector = r.config.InfrastructureNodeSelector
	}

	// Patch the driver container with the volume for CSI driver socket
	volumeMount := v1.VolumeMount{
		Name:      driverSocketVolume,
		MountPath: csiDriverSocketDirectory,
	}
	driverContainer := &podSpec.Spec.Containers[0]
	driverContainer.VolumeMounts = append(driverContainer.VolumeMounts, volumeMount)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: cr.Namespace,
			Name:      dName,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: *podSpec,
			Replicas: &r.config.DeploymentReplicas,
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
			},
		},
	}
	r.addOwnerLabels(&deployment.ObjectMeta, cr)
	r.addOwner(&deployment.ObjectMeta, cr)

	return deployment
}

// generateStorageClass prepares a StorageClass from given template
func (r *ReconcileCSIDriverDeployment) generateStorageClass(cr *csidriverv1alpha1.CSIDriverDeployment, template *csidriverv1alpha1.StorageClassTemplate) *storagev1.StorageClass {
	expectedSC := &storagev1.StorageClass{
		// ObjectMeta will be filled below
		Provisioner:          cr.Spec.DriverName,
		Parameters:           template.Parameters,
		ReclaimPolicy:        template.ReclaimPolicy,
		MountOptions:         template.MountOptions,
		AllowVolumeExpansion: template.AllowVolumeExpansion,
		VolumeBindingMode:    template.VolumeBindingMode,
		AllowedTopologies:    template.AllowedTopologies,
	}
	template.ObjectMeta.DeepCopyInto(&expectedSC.ObjectMeta)
	r.addOwnerLabels(&expectedSC.ObjectMeta, cr)
	if template.Default != nil && *template.Default == true {
		expectedSC.Annotations = map[string]string{
			defaultStorageClassAnnotation: "true",
		}
	} else {
		expectedSC.Annotations = map[string]string{
			defaultStorageClassAnnotation: "false",
		}
	}
	return expectedSC
}

// sanitizeDriverName sanitizes CSI driver name to be usable as a directory name. All dangerous characters are replaced
// by '-'.
func sanitizeDriverName(driver string) string {
	re := regexp.MustCompile("[^a-zA-Z0-9-.]")
	name := re.ReplaceAllString(driver, "-")
	return name
}

// a CSIDriverDeployment (as OwnerReference does not work there) and may be used to limit Watch() in future.
func (r *ReconcileCSIDriverDeployment) addOwnerLabels(meta *metav1.ObjectMeta, cr *csidriverv1alpha1.CSIDriverDeployment) bool {
	changed := false
	if meta.Labels == nil {
		meta.Labels = map[string]string{}
		changed = true
	}
	if v, exists := meta.Labels[OwnerLabelNamespace]; !exists || v != cr.Namespace {
		meta.Labels[OwnerLabelNamespace] = cr.Namespace
		changed = true
	}
	if v, exists := meta.Labels[OwnerLabelName]; !exists || v != cr.Name {
		meta.Labels[OwnerLabelName] = cr.Name
		changed = true
	}

	return changed
}

func (r *ReconcileCSIDriverDeployment) addOwner(meta *metav1.ObjectMeta, cr *csidriverv1alpha1.CSIDriverDeployment) {
	bTrue := true
	meta.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion: csidriverv1alpha1.SchemeGroupVersion.String(),
			Kind:       "CSIDriverDeployment",
			Name:       cr.Name,
			UID:        cr.UID,
			Controller: &bTrue,
		},
	}
}

func (r *ReconcileCSIDriverDeployment) uniqueGlobalName(i *csidriverv1alpha1.CSIDriverDeployment) string {
	return "csidriverdeployment-" + string(i.UID)
}

func (r *ReconcileCSIDriverDeployment) addLivenessProbe(cr *csidriverv1alpha1.CSIDriverDeployment, podSpec *v1.PodTemplateSpec, sidecarSocketVolumePath string) {
	if cr.Spec.ProbePeriodSeconds == nil {
		return
	}

	probeTimeout := livenessprobeDefaultTimeout
	if cr.Spec.ProbeTimeoutSeconds != nil {
		probeTimeout = *cr.Spec.ProbeTimeoutSeconds
	}

	// Add the probe to driverContainer, so the *driver* is restarted when the probe fails.
	driverContainer := &podSpec.Spec.Containers[0]
	if driverContainer.Ports == nil {
		driverContainer.Ports = []v1.ContainerPort{}
	}
	driverContainer.Ports = append(driverContainer.Ports, v1.ContainerPort{
		Name:          "csi-probe",
		Protocol:      v1.ProtocolTCP,
		ContainerPort: livenessprobePort,
	})
	driverContainer.LivenessProbe = &v1.Probe{
		FailureThreshold:    livenessprobeFailureThreshold,
		InitialDelaySeconds: *cr.Spec.ProbePeriodSeconds,
		TimeoutSeconds:      probeTimeout,
		PeriodSeconds:       *cr.Spec.ProbePeriodSeconds,
		Handler: v1.Handler{
			HTTPGet: &v1.HTTPGetAction{
				Path: "/healthz",
				Port: intstr.FromString("csi-probe"),
			},
		},
	}

	// Add liveness probe container that provides /healthz endpoint for the liveness probe.
	livenessprobeImage := *r.config.DefaultImages.LivenessProbeImage
	if cr.Spec.ContainerImages != nil && cr.Spec.ContainerImages.LivenessProbeImage != nil {
		livenessprobeImage = *cr.Spec.ContainerImages.LivenessProbeImage
	}
	probeContainer := v1.Container{
		Name:  "csi-probe",
		Image: livenessprobeImage,
		Args: []string{
			"--v=5",
			"--csi-address=$(ADDRESS)",
		},
		ImagePullPolicy: v1.PullIfNotPresent,
		Env: []v1.EnvVar{
			{
				Name:  "ADDRESS",
				Value: sidecarSocketVolumePath,
			},
		},
		VolumeMounts: []v1.VolumeMount{
			{
				Name:      driverSocketVolume,
				MountPath: driverSocketVolumeMountPath,
			},
		},
	}
	if podSpec.Spec.Containers[0].SecurityContext != nil {
		probeContainer.SecurityContext = podSpec.Spec.Containers[0].SecurityContext.DeepCopy()
	}

	podSpec.Spec.Containers = append(podSpec.Spec.Containers, probeContainer)
}
