package controller

import (
	"testing"

	"github.com/openshift/csi-operator/pkg/config"

	"k8s.io/apimachinery/pkg/util/diff"

	openshiftapi "github.com/openshift/api/operator/v1alpha1"
	"github.com/openshift/csi-operator/pkg/apis/csidriver/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	defaultDriverContainer corev1.Container = corev1.Container{
		Name:  "defaultDriverContainer",
		Image: "defaultDriverImage",
	}
	defaultCR v1alpha1.CSIDriverDeployment = v1alpha1.CSIDriverDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: "default",
			UID:       "defaultuid",
		},
		Spec: v1alpha1.CSIDriverDeploymentSpec{
			DriverName:      "default",
			ManagementState: openshiftapi.Managed,
			DriverPerNodeTemplate: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"defaultLabel": "defaultValue",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{defaultDriverContainer},
				},
			},
			DriverControllerTemplate: &corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"defaultLabel": "defaultValue",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{defaultDriverContainer},
				},
			},
			DriverSocket:       "/my/csi/path/csi.sock",
			NodeUpdateStrategy: v1alpha1.CSIDeploymentUpdateStrategyRolling,
		},
	}

	defaultLabels = map[string]string{
		"csidriver.storage.openshift.io/owner-namespace": "default",
		"csidriver.storage.openshift.io/owner-name":      "default",
	}

	bTrue = true

	defaultOwner = metav1.OwnerReference{
		APIVersion: "csidriver.storage.openshift.io/v1alpha1",
		Kind:       "CSIDriverDeployment",
		Name:       "default",
		UID:        "defaultuid",
		Controller: &bTrue,
	}

	str2ptr = func(str string) *string {
		return &str
	}

	testConfig = &config.Config{
		DefaultImages: v1alpha1.CSIDeploymentContainerImages{
			AttacherImage:        str2ptr("quay.io/k8scsi/csi-attacher:v0.3.0"),
			ProvisionerImage:     str2ptr("quay.io/k8scsi/csi-provisioner:v0.3.1"),
			DriverRegistrarImage: str2ptr("quay.io/k8scsi/driver-registrar:v0.3.0"),
			LivenessProbeImage:   str2ptr("quay.io/k8scsi/livenessprobe:latest"),
		},
		InfrastructureNodeSelector: nil,
		// Not configurable at all
		DeploymentReplicas:            1,
		ClusterRoleName:               "csidriver",
		LeaderElectionClusterRoleName: "csidriver-controller-leader-election",
		KubeletRootDir:                "/var/lib/kubelet",
	}

	defaultSA = &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       "default",
			Name:            "default",
			Labels:          defaultLabels,
			OwnerReferences: []metav1.OwnerReference{defaultOwner},
		},
	}

	defaultClusterRoleBinding = &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       "",
			Name:            "csidriverdeployment-defaultuid",
			Labels:          defaultLabels,
			OwnerReferences: []metav1.OwnerReference{defaultOwner},
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "default",
				Namespace: "default",
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "csidriver",
		},
	}

	defaultLeaderElectionRoleBinding = &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       "default",
			Name:            "leader-election-default",
			Labels:          defaultLabels,
			OwnerReferences: []metav1.OwnerReference{defaultOwner},
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "default",
				Namespace: "default",
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "csidriver-controller-leader-election",
		},
	}

	int1              int32 = 1
	defaultDeployment       = &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       "default",
			Name:            "default-controller",
			Labels:          defaultLabels,
			OwnerReferences: []metav1.OwnerReference{defaultOwner},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &int1,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"csidriver.storage.openshift.io/deployment": "default-controller",
				},
			},
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"defaultLabel":                              "defaultValue",
						"csidriver.storage.openshift.io/deployment": "default-controller",
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "default",
					Containers: []corev1.Container{
						{
							Name:  "defaultDriverContainer",
							Image: "defaultDriverImage",
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "csi-driver",
									MountPath: "/my/csi/path",
								},
							},
						},
						{
							Name:  "csi-provisioner",
							Image: *testConfig.DefaultImages.ProvisionerImage,
							Args: []string{
								"--v=5",
								"--csi-address=$(ADDRESS)",
								"--provisioner=default",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "ADDRESS",
									Value: "/csi/csi.sock",
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "csi-driver",
									MountPath: "/csi",
								},
							},
						},
						{
							Name:  "csi-attacher",
							Image: *testConfig.DefaultImages.AttacherImage,
							Args: []string{
								"--v=5",
								"--csi-address=$(ADDRESS)",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "ADDRESS",
									Value: "/csi/csi.sock",
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "csi-driver",
									MountPath: "/csi",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "csi-driver",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
	}

	typeDir         = corev1.HostPathDirectory
	typeDirOrCreate = corev1.HostPathDirectoryOrCreate

	defaultDaemonSet = &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       "default",
			Name:            "default-node",
			Labels:          defaultLabels,
			OwnerReferences: []metav1.OwnerReference{defaultOwner},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"csidriver.storage.openshift.io/daemonset": "default-node",
				},
			},
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type: appsv1.RollingUpdateDaemonSetStrategyType,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"defaultLabel":                             "defaultValue",
						"csidriver.storage.openshift.io/daemonset": "default-node",
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "default",
					Containers: []corev1.Container{
						{
							Name:  "defaultDriverContainer",
							Image: "defaultDriverImage",
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "csi-driver",
									MountPath: "/my/csi/path",
								},
							},
						},
						{
							Name:  "csi-driver-registrar",
							Image: *testConfig.DefaultImages.DriverRegistrarImage,
							Args: []string{
								"--v=5",
								"--csi-address=$(ADDRESS)",
							},
							SecurityContext: &corev1.SecurityContext{
								Privileged: &bTrue,
							},
							Env: []corev1.EnvVar{
								{
									Name:  "ADDRESS",
									Value: "/csi/csi.sock",
								},
								{
									Name:  "DRIVER_REG_SOCK_PATH",
									Value: "/var/lib/kubelet/plugins/default/csi.sock",
								},
								{
									Name: "KUBE_NODE_NAME",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "spec.nodeName",
										},
									},
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "csi-driver",
									MountPath: "/csi",
								},
								{
									Name:      "registration-dir",
									MountPath: "/registration",
								},
							},
						},
					},
					Volumes: []corev1.Volume{

						{
							Name: "registration-dir",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/var/lib/kubelet/plugins",
									Type: &typeDir,
								},
							},
						},
						{
							Name: "csi-driver",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/var/lib/kubelet/plugins/default",
									Type: &typeDirOrCreate,
								},
							},
						},
					},
				},
			},
		},
	}
)

func TestGenerateServiceAccount(t *testing.T) {
	tests := []struct {
		name       string
		cdd        *v1alpha1.CSIDriverDeployment
		expectedSA *corev1.ServiceAccount
	}{
		{
			"pass",
			&defaultCR,
			defaultSA,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := &Handler{
				config: testConfig,
			}
			sa := handler.generateServiceAccount(test.cdd)
			if !equality.Semantic.DeepEqual(sa, test.expectedSA) {
				t.Errorf("expected sa \n%+v, got \n%+v", test.expectedSA, sa)
			}
		})
	}
}

func TestGenerateClusterRoleBinding(t *testing.T) {
	tests := []struct {
		name        string
		cdd         *v1alpha1.CSIDriverDeployment
		expectedCRB *rbacv1.ClusterRoleBinding
	}{
		{
			"pass",
			&defaultCR,
			defaultClusterRoleBinding,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := &Handler{
				config: testConfig,
			}
			sa := handler.generateServiceAccount(test.cdd)
			crb := handler.generateClusterRoleBinding(test.cdd, sa)
			if !equality.Semantic.DeepEqual(crb, test.expectedCRB) {
				t.Errorf("expected ClusterRoleBinding \n%+v, got \n%+v", test.expectedCRB, crb)
			}
		})
	}
}

func TestGenerateLeaderElectionRoleBinding(t *testing.T) {
	tests := []struct {
		name       string
		cdd        *v1alpha1.CSIDriverDeployment
		expectedRB *rbacv1.RoleBinding
	}{
		{
			"pass",
			&defaultCR,
			defaultLeaderElectionRoleBinding,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := &Handler{
				config: testConfig,
			}
			sa := handler.generateServiceAccount(test.cdd)
			rb := handler.generateLeaderElectionRoleBinding(test.cdd, sa)
			if !equality.Semantic.DeepEqual(rb, test.expectedRB) {
				t.Errorf("expected RoleBinding \n%+v, got \n%+v", test.expectedRB, rb)
			}
		})
	}
}

func TestGenerateDeployment(t *testing.T) {
	tests := []struct {
		name               string
		cdd                *v1alpha1.CSIDriverDeployment
		expectedDeployment *appsv1.Deployment
	}{
		{
			"pass",
			&defaultCR,
			defaultDeployment,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := &Handler{
				config: testConfig,
			}
			sa := handler.generateServiceAccount(test.cdd)
			deployment := handler.generateDeployment(test.cdd, sa)
			if !equality.Semantic.DeepEqual(deployment, test.expectedDeployment) {
				t.Errorf("expected Deployment \n%+v, got \n%+v", test.expectedDeployment, deployment)
				t.Logf("Diff: %s", diff.ObjectDiff(test.expectedDeployment, deployment))
			}
		})
	}
}

func TestGenerateDaemonSet(t *testing.T) {
	tests := []struct {
		name              string
		cdd               *v1alpha1.CSIDriverDeployment
		expectedDaemonSet *appsv1.DaemonSet
	}{
		{
			"pass",
			&defaultCR,
			defaultDaemonSet,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := &Handler{
				config: testConfig,
			}
			sa := handler.generateServiceAccount(test.cdd)
			ds := handler.generateDaemonSet(test.cdd, sa)
			if !equality.Semantic.DeepEqual(ds, test.expectedDaemonSet) {
				t.Errorf("expected DaeemonSet \n%+v, got \n%+v", test.expectedDaemonSet, ds)
				t.Logf("Diff: %s", diff.ObjectDiff(test.expectedDaemonSet, ds))
			}
		})
	}
}
