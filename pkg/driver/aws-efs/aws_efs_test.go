package aws_efs

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"os"
	"reflect"
	"testing"

	"github.com/openshift/csi-operator/assets"
	"github.com/openshift/csi-operator/pkg/clients"
	"github.com/openshift/library-go/pkg/operator/resource/resourceread"
	appsv1 "k8s.io/api/apps/v1"
)

func getTestDeployment() *appsv1.Deployment {
	data, err := assets.ReadFile("overlays/aws-efs/generated/standalone/controller.yaml")
	if err != nil {
		panic(err)
	}
	return resourceread.ReadDeploymentV1OrDie(data)
}

func getTestDaemonSet() *appsv1.DaemonSet {
	data, err := assets.ReadFile("overlays/aws-efs/generated/standalone/node.yaml")
	if err != nil {
		panic(err)
	}
	return resourceread.ReadDaemonSetV1OrDie(data)
}

func getTestCredentialsRequest() *unstructured.Unstructured {
	data, err := assets.ReadFile("overlays/aws-efs/generated/standalone/credentials.yaml")
	if err != nil {
		panic(err)
	}
	return resourceread.ReadUnstructuredOrDie(data)
}

func Test_WithFIPSDeploymentHook(t *testing.T) {
	tests := []struct {
		name            string
		fipsEnabled     string
		expectedEnvVars map[string]string
	}{
		{
			name:        "FIPS enabled",
			fipsEnabled: "true",
			expectedEnvVars: map[string]string{
				"FIPS_ENABLED": "true",
			},
		},
		{
			name:        "FIPS disabled",
			fipsEnabled: "false",
			expectedEnvVars: map[string]string{
				"FIPS_ENABLED": "false",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := clients.GetFakeOperatorCR()
			c := clients.NewFakeClients("clusters-test", cr)
			hook, _ := withFIPSDeploymentHookInternal(tt.fipsEnabled)
			deployment := getTestDeployment()
			clients.SyncFakeInformers(t, c)

			// Act
			err := hook(&cr.Spec.OperatorSpec, deployment)
			if err != nil {
				t.Fatalf("unexpected hook error: %v", err)
			}

			// Assert
			found := false
			for _, container := range deployment.Spec.Template.Spec.Containers {
				if container.Name == "csi-driver" {
					// Collect FIPS_ENABLED from struct EnvVar to map[string]string
					containerEnvs := map[string]string{}
					for _, env := range container.Env {
						if env.Name == "FIPS_ENABLED" {
							containerEnvs[env.Name] = env.Value
						}
					}
					if !reflect.DeepEqual(containerEnvs, tt.expectedEnvVars) {
						t.Errorf("expected csi-driver env var %+v, got %+v", tt.expectedEnvVars, containerEnvs)
					}
					found = true
					break
				}
			}
			if !found {
				t.Errorf("container csi-driver not found")
			}
		})
	}
}

func Test_WithFIPSDaemonSetHook(t *testing.T) {
	tests := []struct {
		name            string
		fipsEnabled     string
		expectedEnvVars map[string]string
	}{
		{
			name:        "FIPS enabled",
			fipsEnabled: "true",
			expectedEnvVars: map[string]string{
				"FIPS_ENABLED": "true",
			},
		},
		{
			name:        "FIPS disabled",
			fipsEnabled: "false",
			expectedEnvVars: map[string]string{
				"FIPS_ENABLED": "false",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := clients.GetFakeOperatorCR()
			c := clients.NewFakeClients("clusters-test", cr)
			hook, _ := withFIPSDaemonSetHookInternal(tt.fipsEnabled)
			daemonSet := getTestDaemonSet()
			clients.SyncFakeInformers(t, c)

			// Act
			err := hook(&cr.Spec.OperatorSpec, daemonSet)
			if err != nil {
				t.Fatalf("unexpected hook error: %v", err)
			}

			// Assert
			found := false
			for _, container := range daemonSet.Spec.Template.Spec.Containers {
				if container.Name == "csi-driver" {
					// Collect FIPS_ENABLED from struct EnvVar to map[string]string
					containerEnvs := map[string]string{}
					for _, env := range container.Env {
						if env.Name == "FIPS_ENABLED" {
							containerEnvs[env.Name] = env.Value
						}
					}
					if !reflect.DeepEqual(containerEnvs, tt.expectedEnvVars) {
						t.Errorf("expected csi-driver env var %+v, got %+v", tt.expectedEnvVars, containerEnvs)
					}
					found = true
					break
				}
			}
			if !found {
				t.Errorf("container csi-driver not found")
			}
		})
	}
}

func Test_StsCredentialsRequestHook(t *testing.T) {
	testARN := "arn:aws:iam::301721915996:role/user-123x-4567--efs-csi-driver-role"

	tests := []struct {
		name              string
		envVariables      map[string]string
		expectedARN       string
		expectedTokenPath string
	}{
		{
			name:              "STS is enabled",
			envVariables:      map[string]string{stsIAMRoleARNEnvVar: testARN},
			expectedARN:       testARN,
			expectedTokenPath: cloudTokenPath,
		},
		{
			name:              "STS is disabled - no ARN set",
			envVariables:      map[string]string{},
			expectedARN:       "",
			expectedTokenPath: "",
		},
		{
			name:              "STS is disabled - ARN is empty",
			envVariables:      map[string]string{stsIAMRoleARNEnvVar: ""},
			expectedARN:       "",
			expectedTokenPath: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := clients.GetFakeOperatorCR()

			// Set the environment variables
			for key, value := range tt.envVariables {
				err := os.Setenv(key, value)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}

			// Act
			testCr := getTestCredentialsRequest()
			err := stsCredentialsRequestHook(&cr.Spec.OperatorSpec, testCr)
			if err != nil {
				t.Fatalf("unexpected hook error: %v", err)
			}

			// Assert
			providerSpec, _, err := unstructured.NestedString(testCr.Object, "spec", "providerSpec", "stsIAMRoleARN")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if providerSpec != tt.expectedARN {
				t.Fatalf("expected stsIAMRoleARN %#v, got %#v", tt.expectedARN, providerSpec)
			}

			tokenPath, _, err := unstructured.NestedString(testCr.Object, "spec", "cloudTokenPath")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tokenPath != tt.expectedTokenPath {
				t.Fatalf("expected cloudTokenPath %#v, got %#v", tt.expectedTokenPath, tokenPath)
			}

			// Cleanup
			for key := range tt.envVariables {
				err := os.Unsetenv(key)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}
