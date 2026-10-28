package operator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	"github.com/openshift/library-go/pkg/crypto"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"github.com/openshift/library-go/pkg/operator/events"
)

// WriteOperatorTLSConfig reads the cluster TLS profile from the APIServer CR
// and writes a GenericOperatorConfig file with TLS settings for the operator's
// own HTTPS endpoint (metrics, healthz). The returned path is passed to
// controllercmd via --config.
//
// Behavior by TLS adherence mode:
//   - StrictAllComponents: use the cluster-configured TLS profile
//   - Legacy/unset: use the default Intermediate profile
//
// In both modes a known profile is always applied — the operator never falls
// back to raw Go crypto/tls defaults. If the APIServer CR cannot be read,
// the function returns an error and the operator must not start.
//
// Returns ("", nil) only when running outside a cluster (local development).
func WriteOperatorTLSConfig(ctx context.Context, operatorName string) (string, error) {
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		klog.V(4).Infof("Not running in-cluster, skipping TLS profile injection: %v", err)
		return "", nil
	}

	client, err := configclient.NewForConfig(restConfig)
	if err != nil {
		return "", fmt.Errorf("failed to create config client: %w", err)
	}

	apiServer, err := client.ConfigV1().APIServers().Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to read APIServer config: %w", err)
	}

	useGroups, err := getFeatureGateTLSGroupPreferences(ctx, client)
	if err != nil {
		return "", err
	}

	var profile *configv1.TLSSecurityProfile
	if crypto.ShouldHonorClusterTLSProfile(apiServer.Spec.TLSAdherence) {
		profile = apiServer.Spec.TLSSecurityProfile
		klog.Infof("TLS adherence policy is %q, using cluster TLS profile", apiServer.Spec.TLSAdherence)
	} else {
		klog.Infof("TLS adherence policy is %q, using default Intermediate profile", apiServer.Spec.TLSAdherence)
	}

	minVersion, ciphers, groups := resolveTLSProfile(profile, useGroups)
	return writeConfig(operatorName, minVersion, ciphers, groups)
}

func writeConfig(operatorName string, minTLSVersion string, cipherSuites []string, groups []int32) (string, error) {
	configDir := filepath.Join("/tmp", operatorName+"-tls")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create TLS config dir: %w", err)
	}

	configPath := filepath.Join(configDir, "config.yaml")
	content := buildOperatorConfig(minTLSVersion, cipherSuites, groups)

	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		return "", fmt.Errorf("failed to write operator TLS config: %w", err)
	}

	klog.Infof("Wrote operator TLS config to %s (minTLSVersion=%s, %d cipher suites, %d TLS groups)", configPath, minTLSVersion, len(cipherSuites), len(groups))
	return configPath, nil
}

// resolveTLSProfile resolves a TLSSecurityProfile to concrete minTLSVersion and
// IANA cipher suite names. When profile is nil, defaults to Intermediate.
func resolveTLSProfile(profile *configv1.TLSSecurityProfile, useGroups bool) (string, []string, []int32) {
	var spec *configv1.TLSProfileSpec
	if profile == nil {
		spec = configv1.TLSProfiles[crypto.DefaultTLSProfileType]
	} else {
		switch profile.Type {
		case configv1.TLSProfileCustomType:
			if profile.Custom != nil {
				spec = &profile.Custom.TLSProfileSpec
			}
		case configv1.TLSProfileOldType, configv1.TLSProfileIntermediateType, configv1.TLSProfileModernType:
			spec = configv1.TLSProfiles[profile.Type]
		}
	}

	if spec == nil {
		spec = configv1.TLSProfiles[crypto.DefaultTLSProfileType]
	}

	var groups []int32
	if useGroups {
		// Filter out non-FIPS-approved groups
		groupStrings := crypto.GetSecurityProfileGroups(spec)
		// Convert to []int32
		var unknownGroups []string
		groups, unknownGroups = crypto.TLSGroupsToCurvePreferences(groupStrings)
		if len(unknownGroups) > 0 {
			klog.Warningf("Unknown TLS groups: %v", unknownGroups)
		}
	}
	return string(spec.MinTLSVersion), crypto.OpenSSLToIANACipherSuites(spec.Ciphers), groups
}

func buildOperatorConfig(minTLSVersion string, cipherSuites []string, groups []int32) string {
	var b strings.Builder
	fmt.Fprintf(&b, `apiVersion: operator.openshift.io/v1alpha1
kind: GenericOperatorConfig
servingInfo:
  minTLSVersion: %s
`, minTLSVersion)

	if len(cipherSuites) > 0 {
		b.WriteString("  cipherSuites:\n")
		for _, cs := range cipherSuites {
			fmt.Fprintf(&b, "  - %s\n", cs)
		}
	}
	if len(groups) > 0 {
		b.WriteString("  curvePreferences:\n")
		for _, group := range groups {
			fmt.Fprintf(&b, "  - %d\n", group)
		}
	}
	return b.String()
}

// WireTLSPreRunHook chains a PersistentPreRunE on the given cobra command that
// reads the cluster TLS profile and passes the resulting GenericOperatorConfig
// to controllercmd via --config. If TLS configuration cannot be obtained, the
// operator fails to start — it must never run with Go's default TLS settings.
// The pod will be restarted by the container runtime and retry.
func WireTLSPreRunHook(ctrlCmd *cobra.Command, operatorName string) {
	originalPreRunE := ctrlCmd.PersistentPreRunE
	ctrlCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if originalPreRunE != nil {
			if err := originalPreRunE(cmd, args); err != nil {
				return err
			}
		}

		// WriteOperatorTLSConfig can wait up to 60 seconds for the feature gates
		// to be observed.
		ctx, cancel := context.WithTimeout(cmd.Context(), 90*time.Second)
		defer cancel()

		configPath, err := WriteOperatorTLSConfig(ctx, operatorName)
		if err != nil {
			return fmt.Errorf("failed to obtain TLS configuration: %w", err)
		}
		if configPath != "" {
			if err := cmd.Flags().Set("config", configPath); err != nil {
				return fmt.Errorf("failed to set --config flag: %w", err)
			}
		}
		return nil
	}
}

// getFeatureGateTLSGroupPreferences determines whether the TLSGroupPreferences feature gate is
// enabled on the cluster. It spins up its own short-lived FeatureGate/ClusterVersion informers
// (this runs before the operator's shared informer factories are started) and waits for the
// initial set of feature gates to be observed, returning an error if that does not happen
// within one minute.
func getFeatureGateTLSGroupPreferences(ctx context.Context, client *configclient.Clientset) (bool, error) {
	informerFactory := configinformers.NewSharedInformerFactory(client, 10*time.Minute)
	eventRecorder := events.NewLoggingEventRecorder("csi-operator-tlsconfig", clock.RealClock{})

	// OLM: the operator version is not known, this tells the feature gate
	// accessor to fall back to the ClusterVersion CR.
	const missingVersionMarker = "0.0.1-snapshot"

	featureGateAccessor := featuregates.NewFeatureGateAccess(
		missingVersionMarker,
		missingVersionMarker,
		informerFactory.Config().V1().ClusterVersions(),
		informerFactory.Config().V1().FeatureGates(),
		eventRecorder,
	)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go featureGateAccessor.Run(runCtx)
	informerFactory.Start(runCtx.Done())

	select {
	case <-featureGateAccessor.InitialFeatureGatesObserved():
	case <-time.After(1 * time.Minute):
		return false, fmt.Errorf("timed out waiting for FeatureGate detection")
	case <-ctx.Done():
		return false, fmt.Errorf("context cancelled while waiting for FeatureGate detection: %w", ctx.Err())
	}

	currentFeatureGates, err := featureGateAccessor.CurrentFeatureGates()
	if err != nil {
		return false, fmt.Errorf("failed to determine feature gates: %w", err)
	}

	return currentFeatureGates.Enabled(features.FeatureGateTLSGroupPreferences), nil
}
