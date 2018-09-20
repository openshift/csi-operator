/*

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"path"
	"regexp"

	csidriverv1alpha1 "github.com/openshift/csi-operator/pkg/apis/csidriver/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	r.addOwner(&crb.ObjectMeta, cr)
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
		"csidriver.storage.okd.io/daemonset": dsName,
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
	registrarSocketDirectory := "/csi"
	registrarSocketPath := path.Join(registrarSocketDirectory, csiDriverSocketFileName)

	// Path to the CSI driver socket from kubelet point of view
	kubeletSocketDirectory := path.Join(r.config.KubeletRootDir, "plugins", sanitizeDriverName(cr.Spec.DriverName))
	kubeletSocketPath := path.Join(registrarSocketDirectory, csiDriverSocketFileName)

	// Path to the kubelet dynamic registration directory
	kubeletRegistrationDirectory := path.Join(r.config.KubeletRootDir, "plugins")

	bTrue := true
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
		SecurityContext: &v1.SecurityContext{
			Privileged: &bTrue,
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
				Name:      "csi-driver",
				MountPath: registrarSocketDirectory,
			},
			{
				Name:      "registration-dir",
				MountPath: "/registration",
			},
		},
	}
	podSpec.Spec.Containers = append(podSpec.Spec.Containers, registrar)

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
			Name: "csi-driver",
			VolumeSource: v1.VolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: kubeletSocketDirectory,
					Type: &typeDirOrCreate,
				},
			},
		},
	}
	podSpec.Spec.Volumes = append(podSpec.Spec.Volumes, volumes...)

	// Patch the driver container with the volume for CSI driver socket
	volumeMount := v1.VolumeMount{
		Name:      "csi-driver",
		MountPath: csiDriverSocketDirectory,
	}
	driverContainer := &podSpec.Spec.Containers[0]
	driverContainer.VolumeMounts = append(driverContainer.VolumeMounts, volumeMount)

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
		"csidriver.storage.okd.io/deployment": dName,
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
	sidecarSocketDirectory := "/csi"
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
				Name:      "csi-driver",
				MountPath: "/csi",
			},
		},
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
				Name:      "csi-driver",
				MountPath: "/csi",
			},
		},
	}
	podSpec.Spec.Containers = append(podSpec.Spec.Containers, attacher)

	// Add volumes
	volumes := []v1.Volume{
		{
			Name: "csi-driver",
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
		Name:      "csi-driver",
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
	r.addOwner(&expectedSC.ObjectMeta, cr)
	if template.Default != nil && *template.Default == true {
		expectedSC.Annotations = map[string]string{
			"storageclass.kubernetes.io/is-default-class": "true",
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
