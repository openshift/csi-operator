package gcp_pd

import (
	"context"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	fakeconfig "github.com/openshift/client-go/config/clientset/versioned/fake"
	"github.com/openshift/csi-operator/pkg/clients"
	"github.com/openshift/csi-operator/pkg/generator"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// newTestClients returns fake Clients with an Infrastructure object already
// registered, so getStorageClassFiles() resolves immediately instead of
// polling.
func newTestClients(t *testing.T) *clients.Clients {
	t.Helper()
	cr := clients.GetFakeOperatorCR()
	c := clients.NewFakeClients("clusters-test", cr)
	infra := &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{Name: globalInfrastructureName},
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{
				GCP: &configv1.GCPPlatformStatus{Region: "us-central1"},
			},
		},
	}
	if err := c.ConfigClientSet.(*fakeconfig.Clientset).Tracker().Add(infra); err != nil {
		t.Fatalf("failed to seed fake Infrastructure: %v", err)
	}
	return c
}

func TestGetGCPPDOperatorControllerConfig(t *testing.T) {
	t.Run("When flavour is HyperShift it should no longer return an error", func(t *testing.T) {
		c := newTestClients(t)
		cfg, err := GetGCPPDOperatorControllerConfig(context.Background(), generator.FlavourHyperShift, c)
		if err != nil {
			t.Fatalf("expected no error for HyperShift flavour, got: %v", err)
		}
		if cfg == nil {
			t.Fatalf("expected non-nil config for HyperShift flavour")
		}
	})

	t.Run("When flavour is Standalone it should still register the old privileged binding cleanup controller", func(t *testing.T) {
		c := newTestClients(t)
		cfg, err := GetGCPPDOperatorControllerConfig(context.Background(), generator.FlavourStandalone, c)
		if err != nil {
			t.Fatalf("unexpected error for Standalone flavour: %v", err)
		}
		if len(cfg.ExtraControlPlaneControllers) != 1 {
			t.Errorf("expected 1 extra control plane controller (old binding cleanup) for Standalone, got %d", len(cfg.ExtraControlPlaneControllers))
		}
	})

	t.Run("When flavour is HyperShift it should not register the standalone-only old privileged binding cleanup controller", func(t *testing.T) {
		c := newTestClients(t)
		cfg, err := GetGCPPDOperatorControllerConfig(context.Background(), generator.FlavourHyperShift, c)
		if err != nil {
			t.Fatalf("unexpected error for HyperShift flavour: %v", err)
		}
		if len(cfg.ExtraControlPlaneControllers) != 0 {
			t.Errorf("expected 0 extra control plane controllers for HyperShift, got %d", len(cfg.ExtraControlPlaneControllers))
		}
	})
}
