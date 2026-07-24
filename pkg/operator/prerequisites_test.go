package operator

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/openshift/library-go/pkg/operator/events"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakecore "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/clock"
)

func TestApplyPrerequisites(t *testing.T) {
	assetDir := "overlays/gcp-pd/generated/standalone"

	cases := []struct {
		name         string
		assetDir     string
		assetNames   []string
		setupReactor func(client *fakecore.Clientset)
		expectErr    bool
		errContains  string
	}{
		{
			name:       "should apply all prerequisite assets successfully",
			assetDir:   assetDir,
			assetNames: []string{"controller_sa.yaml", "node_sa.yaml"},
			expectErr:  false,
		},
		{
			name:       "should succeed with empty asset list",
			assetDir:   assetDir,
			assetNames: []string{},
			expectErr:  false,
		},
		{
			name:     "should apply all GCP PD prerequisite assets",
			assetDir: assetDir,
			assetNames: []string{
				"controller_sa.yaml",
				"node_sa.yaml",
				"hostnetwork_role.yaml",
				"controller_hostnetwork_binding.yaml",
				"privileged_role.yaml",
				"node_privileged_binding.yaml",
			},
			expectErr: false,
		},
		{
			name:        "should return error for non-existent asset file",
			assetDir:    assetDir,
			assetNames:  []string{"nonexistent.yaml"},
			expectErr:   true,
			errContains: "nonexistent.yaml",
		},
		{
			name:        "should return error when some assets do not exist",
			assetDir:    assetDir,
			assetNames:  []string{"controller_sa.yaml", "nonexistent.yaml"},
			expectErr:   true,
			errContains: "nonexistent.yaml",
		},
		{
			name:     "should retry failed assets and eventually succeed",
			assetDir: assetDir,
			assetNames: []string{
				"controller_sa.yaml",
				"node_sa.yaml",
			},
			setupReactor: func(client *fakecore.Clientset) {
				callCount := 0
				client.PrependReactor("create", "serviceaccounts", func(action k8stesting.Action) (bool, runtime.Object, error) {
					createAction := action.(k8stesting.CreateAction)
					sa := createAction.GetObject().(*corev1.ServiceAccount)
					if sa.Name == "gcp-pd-csi-driver-node-sa" {
						callCount++
						if callCount <= 1 {
							return true, nil, fmt.Errorf("transient error")
						}
					}
					return false, nil, nil
				})
			},
			expectErr: false,
		},
		{
			name:       "should return error for non-existent asset directory",
			assetDir:   "no/such/dir",
			assetNames: []string{"controller_sa.yaml"},
			expectErr:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origDelay := delayIteration
			origIterations := numIterations
			delayIteration = 0
			numIterations = 3
			defer func() {
				delayIteration = origDelay
				numIterations = origIterations
			}()

			kubeClient := fakecore.NewClientset()
			if tc.setupReactor != nil {
				tc.setupReactor(kubeClient)
			}
			recorder := events.NewInMemoryRecorder("test", &clock.RealClock{})
			ctx := context.Background()

			err := applyPrerequisites(ctx, kubeClient, recorder, tc.assetDir, tc.assetNames)
			if tc.expectErr && err == nil {
				t.Fatalf("expected error but got nil")
			}
			if !tc.expectErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.errContains != "" && err != nil && !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected error to contain %q, got %q", tc.errContains, err.Error())
			}

			if !tc.expectErr {
				verifyAppliedAssets(t, ctx, kubeClient, tc.assetDir, tc.assetNames)
			}
		})
	}
}

func verifyAppliedAssets(t *testing.T, ctx context.Context, kubeClient *fakecore.Clientset, assetDir string, assetNames []string) {
	t.Helper()
	for _, name := range assetNames {
		switch {
		case strings.Contains(name, "_sa.yaml"):
			namespace := "openshift-cluster-csi-drivers"
			saList, err := kubeClient.CoreV1().ServiceAccounts(namespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				t.Fatalf("failed to list service accounts: %v", err)
			}
			if len(saList.Items) == 0 {
				t.Fatalf("expected service accounts to be created, but found none")
			}
		}
	}
}
