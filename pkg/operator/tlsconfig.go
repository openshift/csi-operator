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

	configv1 "github.com/openshift/api/config/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	"github.com/openshift/library-go/pkg/crypto"
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

	var profile *configv1.TLSSecurityProfile
	if crypto.ShouldHonorClusterTLSProfile(apiServer.Spec.TLSAdherence) {
		profile = apiServer.Spec.TLSSecurityProfile
		klog.Infof("TLS adherence policy is %q, using cluster TLS profile", apiServer.Spec.TLSAdherence)
	} else {
		klog.Infof("TLS adherence policy is %q, using default Intermediate profile", apiServer.Spec.TLSAdherence)
	}

	minVersion, ciphers := resolveTLSProfile(profile)
	return writeConfig(operatorName, minVersion, ciphers)
}

func writeConfig(operatorName string, minTLSVersion string, cipherSuites []string) (string, error) {
	configDir := filepath.Join("/tmp", operatorName+"-tls")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create TLS config dir: %w", err)
	}

	configPath := filepath.Join(configDir, "config.yaml")
	content := buildOperatorConfig(minTLSVersion, cipherSuites)

	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		return "", fmt.Errorf("failed to write operator TLS config: %w", err)
	}

	klog.Infof("Wrote operator TLS config to %s (minTLSVersion=%s, %d cipher suites)", configPath, minTLSVersion, len(cipherSuites))
	return configPath, nil
}

// resolveTLSProfile resolves a TLSSecurityProfile to concrete minTLSVersion and
// IANA cipher suite names. When profile is nil, defaults to Intermediate.
func resolveTLSProfile(profile *configv1.TLSSecurityProfile) (string, []string) {
	if profile == nil {
		spec := configv1.TLSProfiles[crypto.DefaultTLSProfileType]
		return string(spec.MinTLSVersion), crypto.OpenSSLToIANACipherSuites(spec.Ciphers)
	}

	var spec *configv1.TLSProfileSpec
	switch profile.Type {
	case configv1.TLSProfileCustomType:
		if profile.Custom != nil {
			spec = &profile.Custom.TLSProfileSpec
		}
	case configv1.TLSProfileOldType, configv1.TLSProfileIntermediateType, configv1.TLSProfileModernType:
		spec = configv1.TLSProfiles[profile.Type]
	}

	if spec == nil {
		spec = configv1.TLSProfiles[crypto.DefaultTLSProfileType]
	}

	return string(spec.MinTLSVersion), crypto.OpenSSLToIANACipherSuites(spec.Ciphers)
}

func buildOperatorConfig(minTLSVersion string, cipherSuites []string) string {
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

		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
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
