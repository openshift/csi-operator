package aws_ebs

import (
	"reflect"
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	opv1 "github.com/openshift/api/operator/v1"
	fakeconfig "github.com/openshift/client-go/config/clientset/versioned/fake"
	fakeoperator "github.com/openshift/client-go/operator/clientset/versioned/fake"
	"github.com/openshift/csi-operator/assets"
	"github.com/openshift/csi-operator/pkg/clients"
	"github.com/openshift/library-go/pkg/operator/resource/resourceread"
	appsv1 "k8s.io/api/apps/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func getInfrastructure() *configv1.Infrastructure {
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

func getTestDeployment() *appsv1.Deployment {
	// Re-using generated Deployment, not sure if it's good or bad.
	// It may break unit tests when we update the Deployment template.
	data, err := assets.ReadFile("overlays/aws-ebs/generated/standalone/controller.yaml")
	if err != nil {
		panic(err)
	}
	return resourceread.ReadDeploymentV1OrDie(data)
}

func getTestStorageClass() *storagev1.StorageClass {
	// Re-using generated StorageClass, not sure if it's good or bad.
	// It may break unit tests when we update the StorageClass template.
	data, err := assets.ReadFile("overlays/aws-ebs/generated/standalone/storageclass_gp3.yaml")
	if err != nil {
		panic(err)
	}
	return resourceread.ReadStorageClassV1OrDie(data)
}

func Test_WithCustomEndPoint(t *testing.T) {
	infraNoEndpoint := getInfrastructure()
	infraWithEC2Endpoint := getInfrastructure()
	infraWithEC2Endpoint.Status.PlatformStatus.AWS.ServiceEndpoints = []configv1.AWSServiceEndpoint{
		{
			Name: "ec2",
			URL:  "https://my-endpoint.org",
		},
		{
			Name: "foo",
			URL:  "https://foo.my-endpoint.org",
		},
	}
	infraWithoutEC2Endpoint := getInfrastructure()
	infraWithoutEC2Endpoint.Status.PlatformStatus.AWS.ServiceEndpoints = []configv1.AWSServiceEndpoint{
		{
			Name: "efs",
			URL:  "https://efs.my-endpoint.org",
		},
		{
			Name: "eks",
			URL:  "https://eks.my-endpoint.org",
		},
		{
			Name: "foo",
			URL:  "https://foo.my-endpoint.org",
		},
	}

	tests := []struct {
		name            string
		infrastructure  *configv1.Infrastructure
		expectedEnvVars map[string]string
	}{
		{
			name:           "no endpoint",
			infrastructure: infraNoEndpoint,
			expectedEnvVars: map[string]string{
				"AWS_ACCESS_KEY_ID":     "",
				"AWS_CONFIG_FILE":       "/var/run/secrets/aws/credentials",
				"AWS_SDK_LOAD_CONFIG":   "1",
				"AWS_SECRET_ACCESS_KEY": "",
				"CSI_ENDPOINT":          "unix:///var/lib/csi/sockets/pluginproxy/csi.sock",
			},
		},
		{
			name:           "EC2 endpoint set",
			infrastructure: infraWithEC2Endpoint,
			expectedEnvVars: map[string]string{
				"AWS_ACCESS_KEY_ID":     "",
				"AWS_CONFIG_FILE":       "/var/run/secrets/aws/credentials",
				"AWS_SDK_LOAD_CONFIG":   "1",
				"AWS_SECRET_ACCESS_KEY": "",
				"CSI_ENDPOINT":          "unix:///var/lib/csi/sockets/pluginproxy/csi.sock",
				"AWS_EC2_ENDPOINT":      "https://my-endpoint.org",
			},
		},
		{
			name:           "non-EC2 endpoints set",
			infrastructure: infraWithoutEC2Endpoint,
			expectedEnvVars: map[string]string{
				"AWS_ACCESS_KEY_ID":     "",
				"AWS_CONFIG_FILE":       "/var/run/secrets/aws/credentials",
				"AWS_SDK_LOAD_CONFIG":   "1",
				"AWS_SECRET_ACCESS_KEY": "",
				"CSI_ENDPOINT":          "unix:///var/lib/csi/sockets/pluginproxy/csi.sock",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := clients.GetFakeOperatorCR()
			c := clients.NewFakeClients("clusters-test", cr)
			hook, _ := withCustomEndPoint(c)
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
					// Collect env vars from struct EnvVar to map[string]string
					containerEnvs := map[string]string{}
					for _, env := range container.Env {
						containerEnvs[env.Name] = env.Value
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

func Test_WithCustomTags(t *testing.T) {
	infraNoTags := getInfrastructure()
	infraWithTags := getInfrastructure()
	infraWithTags.Status.PlatformStatus.AWS.ResourceTags = []configv1.AWSResourceTag{
		{
			Key:   "foo",
			Value: "bar",
		},
		{
			Key:   "baz",
			Value: "",
		},
	}
	infraWithEmptyTags := getInfrastructure()
	infraWithEmptyTags.Status.PlatformStatus.AWS.ResourceTags = []configv1.AWSResourceTag{}

	tests := []struct {
		name              string
		infrastructure    *configv1.Infrastructure
		expectedExtraTags string
	}{
		{
			name:              "no tags",
			infrastructure:    infraNoTags,
			expectedExtraTags: "",
		},
		{
			name:              "tags set",
			infrastructure:    infraWithTags,
			expectedExtraTags: "--extra-tags=foo=bar,baz=",
		},
		{
			name:              "empty tags",
			infrastructure:    infraWithEmptyTags,
			expectedExtraTags: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := clients.GetFakeOperatorCR()
			c := clients.NewFakeClients("clusters-test", cr)
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
					// Collect env vars from struct EnvVar to map[string]string
					extraTagsArgument := ""
					for _, arg := range container.Args {
						if strings.HasPrefix(arg, "--extra-tags") {
							extraTagsArgument = arg
						}
					}
					if extraTagsArgument != tt.expectedExtraTags {
						t.Errorf("expected csi-driver extra-tags argument %s, got %s", tt.expectedExtraTags, extraTagsArgument)
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

func Test_WithCustomRegion(t *testing.T) {
	infraNoRegion := getInfrastructure()
	infraWithRegion := getInfrastructure()
	infraWithRegion.Status.PlatformStatus.AWS.Region = "us-east-1"

	tests := []struct {
		name            string
		infrastructure  *configv1.Infrastructure
		expectedEnvVars map[string]string
	}{
		{
			name:           "no region",
			infrastructure: infraNoRegion,
			expectedEnvVars: map[string]string{
				"AWS_ACCESS_KEY_ID":     "",
				"AWS_CONFIG_FILE":       "/var/run/secrets/aws/credentials",
				"AWS_SDK_LOAD_CONFIG":   "1",
				"AWS_SECRET_ACCESS_KEY": "",
				"CSI_ENDPOINT":          "unix:///var/lib/csi/sockets/pluginproxy/csi.sock",
			},
		},
		{
			name:           "region set",
			infrastructure: infraWithRegion,
			expectedEnvVars: map[string]string{
				"AWS_ACCESS_KEY_ID":     "",
				"AWS_CONFIG_FILE":       "/var/run/secrets/aws/credentials",
				"AWS_SDK_LOAD_CONFIG":   "1",
				"AWS_SECRET_ACCESS_KEY": "",
				"CSI_ENDPOINT":          "unix:///var/lib/csi/sockets/pluginproxy/csi.sock",
				"AWS_REGION":            "us-east-1",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := clients.GetFakeOperatorCR()
			c := clients.NewFakeClients("clusters-test", cr)
			hook, _ := withAWSRegion(c)
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
					// Collect env vars from struct EnvVar to map[string]string
					containerEnvs := map[string]string{}
					for _, env := range container.Env {
						containerEnvs[env.Name] = env.Value
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

func Test_WithKMSKeyHook(t *testing.T) {
	driverNoKeys := clients.GetFakeOperatorCR()
	driverNoKeys.Name = string(opv1.AWSEBSCSIDriver)

	driverWithKeys := driverNoKeys.DeepCopy()
	driverWithKeys.Spec.DriverConfig = opv1.CSIDriverConfigSpec{
		DriverType: opv1.AWSDriverType,
		AWS: &opv1.AWSCSIDriverConfigSpec{
			KMSKeyARN: "my-key",
		},
	}

	tests := []struct {
		name                 string
		clusterCSIDriver     *opv1.ClusterCSIDriver
		expectedSCParameters map[string]string
	}{
		{
			name:             "no key",
			clusterCSIDriver: driverNoKeys,
			expectedSCParameters: map[string]string{
				"encrypted": "true",
				"type":      "gp3",
			},
		},
		{
			name:             "key set",
			clusterCSIDriver: driverWithKeys,
			expectedSCParameters: map[string]string{
				"encrypted": "true",
				"type":      "gp3",
				"kmsKeyId":  "my-key",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := clients.GetFakeOperatorCR()
			c := clients.NewFakeClients("clusters-test", cr)
			hook := withKMSKeyHook(c)
			sc := getTestStorageClass()
			// Arrange - inject custom infrastructure
			c.OperatorClientSet.(*fakeoperator.Clientset).Tracker().Add(tt.clusterCSIDriver)
			clients.SyncFakeInformers(t, c)

			// Act
			err := hook(&cr.Spec.OperatorSpec, sc)
			if err != nil {
				t.Fatalf("unexpected hook error: %v", err)
			}

			// Assert
			params := sc.Parameters
			if len(params) == 0 {
				// treat {} as nil
				params = nil
			}
			if !reflect.DeepEqual(params, tt.expectedSCParameters) {
				t.Errorf("expected storage class parameters %+v, got %+v", tt.expectedSCParameters, params)
			}
		})
	}
}
