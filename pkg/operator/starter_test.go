package operator

import (
	"testing"
	"time"

	v1 "github.com/openshift/api/config/v1"
	fakeconfig "github.com/openshift/client-go/config/clientset/versioned/fake"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/fake"
)

func TestWithCustomCABundle(t *testing.T) {
	cases := []struct {
		name         string
		cm           *corev1.ConfigMap
		inDeployment *appsv1.Deployment
		expected     *appsv1.Deployment
	}{
		{
			name: "no configmap",
			inDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name: "csi-driver",
							}},
						},
					},
				},
			},
			expected: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name: "csi-driver",
							}},
						},
					},
				},
			},
		},
		{
			name: "no CA bundle in configmap",
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "openshift-config-managed",
					Name:      "kube-cloud-config",
				},
				Data: map[string]string{
					"other-key": "other-data",
				},
			},
			inDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name: "csi-driver",
							}},
						},
					},
				},
			},
			expected: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name: "csi-driver",
							}},
						},
					},
				},
			},
		},
		{
			name: "custom CA bundle",
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "openshift-config-managed",
					Name:      "kube-cloud-config",
				},
				Data: map[string]string{
					"ca-bundle.pem": "a custom bundle",
				},
			},
			inDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name: "csi-driver",
							}},
						},
					},
				},
			},
			expected: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name: "csi-driver",
								Env: []corev1.EnvVar{{
									Name:  "AWS_CA_BUNDLE",
									Value: "/etc/ca/ca-bundle.pem",
								}},
								VolumeMounts: []corev1.VolumeMount{{
									Name:      "ca-bundle",
									MountPath: "/etc/ca",
									ReadOnly:  true,
								}},
							}},
							Volumes: []corev1.Volume{{
								Name: "ca-bundle",
								VolumeSource: corev1.VolumeSource{
									ConfigMap: &corev1.ConfigMapVolumeSource{
										LocalObjectReference: corev1.LocalObjectReference{Name: cloudConfigName},
									},
								},
							}},
						},
					},
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resources := []runtime.Object{}
			if tc.cm != nil {
				resources = append(resources, tc.cm)
			}
			kubeClient := fake.NewSimpleClientset(resources...)
			kubeInformersForNamespaces := v1helpers.NewKubeInformersForNamespaces(kubeClient, cloudConfigNamespace)
			cloudConfigInformer := kubeInformersForNamespaces.InformersFor(cloudConfigNamespace).Core().V1().ConfigMaps()
			cloudConfigLister := cloudConfigInformer.Lister().ConfigMaps(cloudConfigNamespace)
			stopCh := make(chan struct{})
			go kubeInformersForNamespaces.Start(stopCh)
			defer close(stopCh)
			wait.Poll(100*time.Millisecond, 30*time.Second, func() (bool, error) {
				return cloudConfigInformer.Informer().HasSynced(), nil
			})
			deployment := tc.inDeployment.DeepCopy()
			err := withCustomAWSCABundle(false, cloudConfigLister)(nil, deployment)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if e, a := tc.expected, deployment; !equality.Semantic.DeepEqual(e, a) {
				t.Errorf("unexpected deployment\nwant=%#v\ngot= %#v", e, a)
			}
		})
	}
}

func TestWithCustomTags(t *testing.T) {
	tests := []struct {
		name         string
		userTags     []v1.AWSResourceTag
		inDeployment *appsv1.Deployment
		expected     *appsv1.Deployment
	}{
		{
			name:     "no tags",
			userTags: []v1.AWSResourceTag{},
			inDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name: "csi-driver",
							}},
						},
					},
				},
			},
			expected: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name: "csi-driver",
							}},
						},
					},
				},
			},
		},
		{
			name: "tags",
			userTags: []v1.AWSResourceTag{
				{
					Key:   "key1",
					Value: "value1",
				},
				{
					Key:   "key2",
					Value: "value2",
				},
				{
					Key:   "key3",
					Value: "value3",
				},
			},
			inDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name: "csi-driver",
								Args: []string{"--existing-options", "foo"},
							}},
						},
					},
				},
			},
			expected: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name: "csi-driver",
								Args: []string{
									"--existing-options", "foo",
									"--extra-tags=key1=value1,key2=value2,key3=value3",
								},
							}},
						},
					},
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			infra := &v1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
				},
				Status: v1.InfrastructureStatus{
					PlatformStatus: &v1.PlatformStatus{
						AWS: &v1.AWSPlatformStatus{
							ResourceTags: test.userTags,
						},
					},
				},
			}
			configClient := fakeconfig.NewSimpleClientset(infra)
			configInformerFactory := configinformers.NewSharedInformerFactory(configClient, 0)
			configInformerFactory.Config().V1().Infrastructures().Informer().GetIndexer().Add(infra)
			stopCh := make(chan struct{})
			go configInformerFactory.Start(stopCh)
			defer close(stopCh)
			wait.Poll(100*time.Millisecond, 30*time.Second, func() (bool, error) {
				return configInformerFactory.Config().V1().Infrastructures().Informer().HasSynced(), nil
			})
			deployment := test.inDeployment.DeepCopy()
			err := withCustomTags(configInformerFactory.Config().V1().Infrastructures().Lister())(nil, deployment)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if e, a := test.expected, deployment; !equality.Semantic.DeepEqual(e, a) {
				t.Errorf("unexpected deployment\nwant=%#v\ngot= %#v", e, a)
			}
		})
	}
}

func TestWithCustomEndPoint(t *testing.T) {
	tests := []struct {
		name            string
		customEndPoints []v1.AWSServiceEndpoint
		inDeployment    *appsv1.Deployment
		expected        *appsv1.Deployment
	}{
		{
			name:            "when no service end point is set",
			customEndPoints: []v1.AWSServiceEndpoint{},
			inDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name: "csi-driver",
								Env: []corev1.EnvVar{
									{
										Name:  "AWS_SECRET",
										Value: "SECRET",
									},
								},
							}},
						},
					},
				},
			},
			expected: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name: "csi-driver",
								Env: []corev1.EnvVar{
									{
										Name:  "AWS_SECRET",
										Value: "SECRET",
									},
								},
							}},
						},
					},
				},
			},
		},
		{
			name: "when a custom ec2 end point is specified",
			customEndPoints: []v1.AWSServiceEndpoint{
				{
					Name: "ec2",
					URL:  "https://example.com",
				},
			},
			inDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name: "csi-driver",
								Env: []corev1.EnvVar{
									{
										Name:  "AWS_SECRET",
										Value: "SECRET",
									},
								},
							}},
						},
					},
				},
			},
			expected: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name: "csi-driver",
								Env: []corev1.EnvVar{
									{
										Name:  "AWS_SECRET",
										Value: "SECRET",
									},
									{
										Name:  "AWS_EC2_ENDPOINT",
										Value: "https://example.com",
									},
								},
							}},
						},
					},
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			infra := &v1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
				},
				Status: v1.InfrastructureStatus{
					PlatformStatus: &v1.PlatformStatus{
						AWS: &v1.AWSPlatformStatus{
							ServiceEndpoints: test.customEndPoints,
						},
					},
				},
			}
			configClient := fakeconfig.NewSimpleClientset(infra)
			configInformerFactory := configinformers.NewSharedInformerFactory(configClient, 0)
			configInformerFactory.Config().V1().Infrastructures().Informer().GetIndexer().Add(infra)
			stopCh := make(chan struct{})
			go configInformerFactory.Start(stopCh)
			defer close(stopCh)
			wait.Poll(100*time.Millisecond, 30*time.Second, func() (bool, error) {
				return configInformerFactory.Config().V1().Infrastructures().Informer().HasSynced(), nil
			})
			deployment := test.inDeployment.DeepCopy()
			err := withCustomEndPoint(configInformerFactory.Config().V1().Infrastructures().Lister())(nil, deployment)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if e, a := test.expected, deployment; !equality.Semantic.DeepEqual(e, a) {
				t.Errorf("unexpected deployment\nwant=%#v\ngot= %#v", e, a)
			}
		})
	}

}

func TestWithHypershiftControlPlaneImages(t *testing.T) {
	tests := []struct {
		name                           string
		isHypershift                   bool
		driverControlPlaneImage        string
		livenessProbeControlPlaneImage string
		inDeployment                   *appsv1.Deployment
		expected                       *appsv1.Deployment
	}{
		{
			name:                           "not hypershift",
			isHypershift:                   false,
			driverControlPlaneImage:        "with-this",
			livenessProbeControlPlaneImage: "with-this",
			inDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "csi-driver",
									Image: "no-replace-this",
								}, {
									Name:  "csi-liveness-probe",
									Image: "no-replace-this",
								},
							},
						},
					},
				},
			},
			expected: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "csi-driver",
									Image: "no-replace-this",
								}, {
									Name:  "csi-liveness-probe",
									Image: "no-replace-this",
								},
							},
						},
					},
				},
			},
		},
		{
			name:                           "hypershift but CONTROL_PLANE_IMAGE envvars not set",
			isHypershift:                   true,
			driverControlPlaneImage:        "",
			livenessProbeControlPlaneImage: "",
			inDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "csi-driver",
									Image: "no-replace-this",
								}, {
									Name:  "csi-liveness-probe",
									Image: "no-replace-this",
								},
							},
						},
					},
				},
			},
			expected: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "csi-driver",
									Image: "no-replace-this",
								}, {
									Name:  "csi-liveness-probe",
									Image: "no-replace-this",
								},
							},
						},
					},
				},
			},
		},
		{
			name:                           "hypershift wiht CONTROL_PLANE_IMAGE envvars set",
			isHypershift:                   true,
			driverControlPlaneImage:        "with-this",
			livenessProbeControlPlaneImage: "with-this",
			inDeployment: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "csi-driver",
									Image: "replace-this",
								}, {
									Name:  "csi-liveness-probe",
									Image: "replace-this",
								},
							},
						},
					},
				},
			},
			expected: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "csi-driver",
									Image: "with-this",
								}, {
									Name:  "csi-liveness-probe",
									Image: "with-this",
								},
							},
						},
					},
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deployment := test.inDeployment.DeepCopy()
			err := withHypershiftControlPlaneImages(test.isHypershift, test.driverControlPlaneImage, test.livenessProbeControlPlaneImage)(nil, deployment)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if e, a := test.expected, deployment; !equality.Semantic.DeepEqual(e, a) {
				t.Errorf("unexpected deployment\nwant=%#v\ngot= %#v", e, a)
			}
		})
	}

}
