package aws_efs

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	configv1 "github.com/openshift/api/config/v1"
	fakeconfig "github.com/openshift/client-go/config/clientset/versioned/fake"
	"github.com/openshift/csi-operator/assets"
	"github.com/openshift/csi-operator/pkg/clients"
	"github.com/openshift/library-go/pkg/operator/resource/resourceread"
)

func getTestDeployment() *appsv1.Deployment {
	data, err := assets.ReadFile("overlays/aws-efs/generated/standalone/controller.yaml")
	if err != nil {
		panic(err)
	}
	return resourceread.ReadDeploymentV1OrDie(data)
}

func getTestInfrastructure() *configv1.Infrastructure {
	return &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{
				AWS: &configv1.AWSPlatformStatus{},
			},
		},
	}
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

func Test_WithCustomTags(t *testing.T) {
	clusterID := "clusters-test"

	infraNoTags := getTestInfrastructure()
	infraNoTags.Status.InfrastructureName = clusterID

	infraWithTags := getTestInfrastructure()
	infraWithTags.Status.InfrastructureName = clusterID
	infraWithTags.Status.PlatformStatus.AWS.ResourceTags = []configv1.AWSResourceTag{
		{
			Key:   "foo",
			Value: "bar",
		},
		{
			Key:   "baz",
			Value: "abc",
		},
	}

	infraWithEmptyTags := getTestInfrastructure()
	infraWithEmptyTags.Status.InfrastructureName = clusterID
	infraWithEmptyTags.Status.PlatformStatus.AWS.ResourceTags = []configv1.AWSResourceTag{}

	infraWithPartialEmptyTags := getTestInfrastructure()
	infraWithPartialEmptyTags.Status.InfrastructureName = clusterID
	infraWithPartialEmptyTags.Status.PlatformStatus.AWS.ResourceTags = []configv1.AWSResourceTag{
		{
			Key:   "foo",
			Value: "bar",
		},
		{
			Key:   "",
			Value: "",
		},
	}

	tests := []struct {
		name            string
		infrastructure  *configv1.Infrastructure
		expectedTagArgs string
	}{
		{
			name:            "no tags",
			infrastructure:  infraNoTags,
			expectedTagArgs: fmt.Sprintf("--tags=kubernetes.io/cluster/${CLUSTER_ID}:owned"),
		},
		{
			name:            "tags set",
			infrastructure:  infraWithTags,
			expectedTagArgs: fmt.Sprintf("--tags=foo:bar baz:abc kubernetes.io/cluster/%s:owned", infraWithTags.Status.InfrastructureName),
		},
		{
			name:            "empty tags",
			infrastructure:  infraWithEmptyTags,
			expectedTagArgs: fmt.Sprintf("--tags=kubernetes.io/cluster/${CLUSTER_ID}:owned"),
		},
		{
			name:            "partial empty value tags",
			infrastructure:  infraWithPartialEmptyTags,
			expectedTagArgs: fmt.Sprintf("--tags=foo:bar kubernetes.io/cluster/%s:owned", infraWithTags.Status.InfrastructureName),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := clients.GetFakeOperatorCR()
			c := clients.NewFakeClients(clusterID, cr)
			hook, _ := withCustomTags(c)
			deployment := getTestDeployment()

			// Arrange - inject custom infrastructure
			c.ConfigClientSet.(*fakeconfig.Clientset).Tracker().Add(tt.infrastructure)
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
					// Collect all `--tags` arguments
					var foundTagArgs string
					for _, arg := range container.Args {
						if strings.HasPrefix(arg, "--tags=") {
							foundTagArgs = arg
							break
						}
					}

					// Ensure both expected `--tags` arguments exist
					if foundTagArgs != tt.expectedTagArgs {
						t.Errorf("expected %s --tags args, got: %s", tt.expectedTagArgs, foundTagArgs)
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

// contains checks if a slice contains a given string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
