package gcp_pd

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/openshift/api/config/v1"
	fakeconfig "github.com/openshift/client-go/config/clientset/versioned/fake"
	"github.com/openshift/csi-operator/pkg/clients"
)

func TestWithCustomResourceTags(t *testing.T) {

	infraObj := &v1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Status: v1.InfrastructureStatus{
			InfrastructureName: "test-sgdh7",
			PlatformStatus: &v1.PlatformStatus{
				GCP: &v1.GCPPlatformStatus{
					ProjectID: "test",
					Region:    "test",
				},
			},
		},
	}

	tmplDeployObj := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "csi-driver",
							Image: "example.io/example-csi-driver",
							Args: []string{
								"--endpoint=$(CSI_ENDPOINT)",
								"--logtostderr",
								"--v=2",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "GOOGLE_APPLICATION_CREDENTIALS",
									Value: "/etc/cloud-sa/service_account.json",
								},
								{
									Name:  "CSI_ENDPOINT",
									Value: "unix:///var/lib/csi/sockets/pluginproxy/csi.sock",
								},
							},
						},
						{
							Name:  "test-driver",
							Image: "example.io/example-test-driver",
						},
					},
				},
			},
		},
	}

	tests := []struct {
		name          string
		tags          []v1.GCPResourceTag
		expArgList    string
		createInfraCR bool
		wantErr       bool
	}{
		{
			name:          "user tags not configured",
			tags:          []v1.GCPResourceTag{},
			expArgList:    "",
			createInfraCR: true,
			wantErr:       false,
		},
		{
			name: "user tags configured",
			tags: []v1.GCPResourceTag{
				{
					ParentID: "openshift",
					Key:      "key1",
					Value:    "value1",
				},
				{
					ParentID: "openshift",
					Key:      "key2",
					Value:    "value2",
				},
				{
					ParentID: "openshift",
					Key:      "key3",
					Value:    "value3",
				},
			},
			expArgList:    "--extra-tags=openshift/key1/value1,openshift/key2/value2,openshift/key3/value3",
			createInfraCR: true,
			wantErr:       false,
		},
		{
			name:          "Infrastructure CR does not exist",
			tags:          []v1.GCPResourceTag{},
			expArgList:    "",
			createInfraCR: false,
			wantErr:       true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cr := clients.GetFakeOperatorCR()
			c := clients.NewFakeClients("clusters-test", cr)
			hook, _ := withCustomResourceTags(c)

			if test.createInfraCR {
				infraObj.Status.PlatformStatus.GCP.ResourceTags = test.tags
				c.ConfigClientSet.(*fakeconfig.Clientset).Tracker().Add(infraObj)
			}
			clients.SyncFakeInformers(t, c)

			deployment := tmplDeployObj.DeepCopy()
			updDeployment := tmplDeployObj.DeepCopy()
			if test.expArgList != "" {
				updDeployment.Spec.Template.Spec.Containers[0].Args = append(
					updDeployment.Spec.Template.Spec.Containers[0].Args,
					test.expArgList,
				)
			}

			err := hook(&cr.Spec.OperatorSpec, deployment)
			if (err != nil) != test.wantErr {
				t.Fatalf("unexpected hook error: %v", err)
			}
			if !equality.Semantic.DeepEqual(deployment, updDeployment) {
				t.Errorf("unexpected deployment want: %+v got: %+v", updDeployment, deployment)
			}
		})
	}
}
