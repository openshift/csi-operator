package generator

import (
	"strings"
	"testing"

	"github.com/openshift/csi-operator/assets"
	generated_assets "github.com/openshift/csi-operator/pkg/generated-assets"
)

// newMonitoringTestGenerator builds an AssetGenerator wired to the real embedded
// assets with a minimal config that only exercises the metrics ServiceMonitor
// generation path (driver metrics port set, no sidecars).
func newMonitoringTestGenerator(flavour ClusterFlavour) *AssetGenerator {
	cfg := &CSIDriverGeneratorConfig{
		AssetPrefix:      "test-csi-driver",
		AssetShortPrefix: "test",
		DriverName:       "test.csi.example.com",
		ControllerConfig: &ControlPlaneConfig{
			LocalMetricsPort:   8211,
			ExposedMetricsPort: 9211,
		},
		GuestConfig: &GuestConfig{
			LocalMetricsPort:   8206,
			ExposedMetricsPort: 9206,
		},
	}
	gen := NewAssetGenerator(flavour, cfg, assets.ReadFile)
	gen.controllerAssets = make(map[string]*YAMLWithHistory)
	gen.guestAssets = make(map[string]*YAMLWithHistory)
	return gen
}

// renderedServerName extracts the tlsConfig.serverName from a rendered
// ServiceMonitor asset.
func renderedServerName(t *testing.T, rendered []byte) string {
	t.Helper()
	const key = "serverName:"
	for _, line := range strings.Split(string(rendered), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key) {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, key))
		}
	}
	t.Fatalf("no serverName found in rendered ServiceMonitor:\n%s", rendered)
	return ""
}

// TestNodeServiceMonitorServerNameUsesNodeNamespace verifies that the node
// metrics ServiceMonitor's TLS serverName resolves to the guest namespace
// (${NODE_NAMESPACE}) so that it matches the serving certificate's SANs on
// HyperShift, where the guest and control-plane namespaces differ.
//
// Regression test for OCPBUGS-112272.
func TestNodeServiceMonitorServerNameUsesNodeNamespace(t *testing.T) {
	gen := newMonitoringTestGenerator(FlavourHyperShift)

	if err := gen.generateGuestMonitoringService(); err != nil {
		t.Fatalf("generateGuestMonitoringService failed: %v", err)
	}

	sm, ok := gen.guestAssets[generated_assets.NodeMetricServiceMonitorAssetName]
	if !ok {
		t.Fatalf("node ServiceMonitor asset %q was not generated", generated_assets.NodeMetricServiceMonitorAssetName)
	}

	serverName := renderedServerName(t, sm.Render())
	want := "test-csi-driver-node-metrics.${NODE_NAMESPACE}.svc"
	if serverName != want {
		t.Errorf("node ServiceMonitor serverName = %q, want %q", serverName, want)
	}
}

// TestControllerServiceMonitorServerNameUsesControlPlaneNamespace verifies that
// the controller metrics ServiceMonitor keeps resolving its serverName to the
// control-plane namespace (${NAMESPACE}), where the controller and its metrics
// Service run.
func TestControllerServiceMonitorServerNameUsesControlPlaneNamespace(t *testing.T) {
	gen := newMonitoringTestGenerator(FlavourStandalone)

	if err := gen.generateControllerMonitoringService(); err != nil {
		t.Fatalf("generateControllerMonitoringService failed: %v", err)
	}

	sm, ok := gen.controllerAssets[generated_assets.ControllerMetricServiceMonitorAssetName]
	if !ok {
		t.Fatalf("controller ServiceMonitor asset %q was not generated", generated_assets.ControllerMetricServiceMonitorAssetName)
	}

	serverName := renderedServerName(t, sm.Render())
	want := "test-csi-driver-controller-metrics.${NAMESPACE}.svc"
	if serverName != want {
		t.Errorf("controller ServiceMonitor serverName = %q, want %q", serverName, want)
	}
}
