package operator

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/openshift/api/config/v1"
	fakeconfig "github.com/openshift/client-go/config/clientset/versioned/fake"
)

func TestGetStorageClassFiles(t *testing.T) {
	tests := []struct {
		name          string
		infra         *v1.Infrastructure
		expectedFiles []string
	}{
		{
			name: "regular GCP region",
			infra: &v1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Status: v1.InfrastructureStatus{
					PlatformStatus: &v1.PlatformStatus{
						GCP: &v1.GCPPlatformStatus{
							Region: "us-central1",
						},
					},
				},
			},
			expectedFiles: []string{"storageclass.yaml", "storageclass_ssd.yaml"},
		},
		{
			name: "regular GCP region europe",
			infra: &v1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Status: v1.InfrastructureStatus{
					PlatformStatus: &v1.PlatformStatus{
						GCP: &v1.GCPPlatformStatus{
							Region: "europe-west1",
						},
					},
				},
			},
			expectedFiles: []string{"storageclass.yaml", "storageclass_ssd.yaml"},
		},
		{
			name: "GCP Dedicated region",
			infra: &v1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Status: v1.InfrastructureStatus{
					PlatformStatus: &v1.PlatformStatus{
						GCP: &v1.GCPPlatformStatus{
							Region: "u-germany-northeast1",
						},
					},
				},
			},
			expectedFiles: []string{"storageclass_hyperdisk_balanced.yaml"},
		},
		{
			name: "empty region defaults to regular GCP",
			infra: &v1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Status: v1.InfrastructureStatus{
					PlatformStatus: &v1.PlatformStatus{
						GCP: &v1.GCPPlatformStatus{
							Region: "",
						},
					},
				},
			},
			expectedFiles: []string{"storageclass.yaml", "storageclass_ssd.yaml"},
		},
		{
			name: "nil GCP status defaults to regular GCP",
			infra: &v1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Status: v1.InfrastructureStatus{
					PlatformStatus: &v1.PlatformStatus{},
				},
			},
			expectedFiles: []string{"storageclass.yaml", "storageclass_ssd.yaml"},
		},
		{
			name: "nil PlatformStatus defaults to regular GCP",
			infra: &v1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Status:     v1.InfrastructureStatus{},
			},
			expectedFiles: []string{"storageclass.yaml", "storageclass_ssd.yaml"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configClient := fakeconfig.NewSimpleClientset(test.infra)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			files, err := getStorageClassFiles(ctx, configClient)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(files) != len(test.expectedFiles) {
				t.Fatalf("expected %d files, got %d: %v", len(test.expectedFiles), len(files), files)
			}
			for i, f := range files {
				if f != test.expectedFiles[i] {
					t.Errorf("file[%d]: expected %q, got %q", i, test.expectedFiles[i], f)
				}
			}
		})
	}
}

func TestGetStorageClassFilesNoInfrastructure(t *testing.T) {
	configClient := fakeconfig.NewSimpleClientset()
	// Use a short timeout so the retry loop finishes quickly in tests.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := getStorageClassFiles(ctx, configClient)
	if err == nil {
		t.Fatal("expected error when Infrastructure CR does not exist, got nil")
	}
}
