package operator

import (
	"reflect"
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/crypto"
)

func TestResolveTLSProfile(t *testing.T) {
	intermediateSpec := configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
	expectedIntermediateVersion := string(intermediateSpec.MinTLSVersion)
	expectedIntermediateCiphers := crypto.OpenSSLToIANACipherSuites(intermediateSpec.Ciphers)

	oldSpec := configv1.TLSProfiles[configv1.TLSProfileOldType]
	expectedOldVersion := string(oldSpec.MinTLSVersion)
	expectedOldCiphers := crypto.OpenSSLToIANACipherSuites(oldSpec.Ciphers)

	modernSpec := configv1.TLSProfiles[configv1.TLSProfileModernType]
	expectedModernVersion := string(modernSpec.MinTLSVersion)
	expectedModernCiphers := crypto.OpenSSLToIANACipherSuites(modernSpec.Ciphers)

	expectedIntermediateGroups, _ := crypto.TLSGroupsToCurvePreferences(intermediateSpec.Groups)

	customGroups := []configv1.TLSGroup{configv1.TLSGroupX25519, configv1.TLSGroupSecP256r1}
	expectedCustomGroups, _ := crypto.TLSGroupsToCurvePreferences(customGroups)
	// crypto.TLSGroupsToCurvePreferences([]) returns a non-nil, empty slice when there are no
	// groups to translate, so compute the "no custom groups" expectation the same way instead
	// of hardcoding nil/empty.
	expectedNoCustomGroups, _ := crypto.TLSGroupsToCurvePreferences([]configv1.TLSGroup(nil))

	tests := []struct {
		name              string
		profile           *configv1.TLSSecurityProfile
		useGroups         bool
		expectedVersion   string
		expectedCipherLen int
		expectedCiphers   []string
		expectedGroups    []int32
		checkExactVersion bool
		exactVersion      string
	}{
		{
			name:              "nil profile defaults to Intermediate",
			profile:           nil,
			expectedVersion:   expectedIntermediateVersion,
			expectedCipherLen: len(expectedIntermediateCiphers),
			expectedCiphers:   expectedIntermediateCiphers,
		},
		{
			name: "Old profile",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileOldType,
			},
			expectedVersion:   expectedOldVersion,
			expectedCipherLen: len(expectedOldCiphers),
			expectedCiphers:   expectedOldCiphers,
		},
		{
			name: "Intermediate profile",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileIntermediateType,
			},
			expectedVersion:   expectedIntermediateVersion,
			expectedCipherLen: len(expectedIntermediateCiphers),
			expectedCiphers:   expectedIntermediateCiphers,
		},
		{
			name: "Modern profile",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileModernType,
			},
			expectedVersion:   expectedModernVersion,
			expectedCipherLen: len(expectedModernCiphers),
			expectedCiphers:   expectedModernCiphers,
		},
		{
			name: "Custom profile with valid spec",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{
					TLSProfileSpec: configv1.TLSProfileSpec{
						MinTLSVersion: configv1.VersionTLS12,
						Ciphers:       []string{"ECDHE-RSA-AES128-GCM-SHA256"},
					},
				},
			},
			checkExactVersion: true,
			exactVersion:      string(configv1.VersionTLS12),
			expectedCipherLen: 1,
			expectedCiphers:   crypto.OpenSSLToIANACipherSuites([]string{"ECDHE-RSA-AES128-GCM-SHA256"}),
		},
		{
			name: "Custom profile with nil Custom falls back to Intermediate",
			profile: &configv1.TLSSecurityProfile{
				Type:   configv1.TLSProfileCustomType,
				Custom: nil,
			},
			expectedVersion:   expectedIntermediateVersion,
			expectedCipherLen: len(expectedIntermediateCiphers),
			expectedCiphers:   expectedIntermediateCiphers,
		},
		{
			name: "Unknown profile type falls back to Intermediate",
			profile: &configv1.TLSSecurityProfile{
				Type: "UnknownType",
			},
			expectedVersion:   expectedIntermediateVersion,
			expectedCipherLen: len(expectedIntermediateCiphers),
			expectedCiphers:   expectedIntermediateCiphers,
		},
		{
			name: "Intermediate profile with useGroups returns curve preferences",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileIntermediateType,
			},
			useGroups:         true,
			expectedVersion:   expectedIntermediateVersion,
			expectedCipherLen: len(expectedIntermediateCiphers),
			expectedCiphers:   expectedIntermediateCiphers,
			expectedGroups:    expectedIntermediateGroups,
		},
		{
			name: "Intermediate profile without useGroups returns no curve preferences",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileIntermediateType,
			},
			useGroups:         false,
			expectedVersion:   expectedIntermediateVersion,
			expectedCipherLen: len(expectedIntermediateCiphers),
			expectedCiphers:   expectedIntermediateCiphers,
			expectedGroups:    nil,
		},
		{
			name: "Custom profile with custom groups and feature gate on returns curve preferences",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{
					TLSProfileSpec: configv1.TLSProfileSpec{
						MinTLSVersion: configv1.VersionTLS12,
						Ciphers:       []string{"ECDHE-RSA-AES128-GCM-SHA256"},
						Groups:        []configv1.TLSGroup{configv1.TLSGroupX25519, configv1.TLSGroupSecP256r1},
					},
				},
			},
			useGroups:         true,
			checkExactVersion: true,
			exactVersion:      string(configv1.VersionTLS12),
			expectedCipherLen: 1,
			expectedCiphers:   crypto.OpenSSLToIANACipherSuites([]string{"ECDHE-RSA-AES128-GCM-SHA256"}),
			expectedGroups:    expectedCustomGroups,
		},
		{
			name: "Custom profile with custom groups and feature gate off returns no curve preferences",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{
					TLSProfileSpec: configv1.TLSProfileSpec{
						MinTLSVersion: configv1.VersionTLS12,
						Ciphers:       []string{"ECDHE-RSA-AES128-GCM-SHA256"},
						Groups:        []configv1.TLSGroup{configv1.TLSGroupX25519, configv1.TLSGroupSecP256r1},
					},
				},
			},
			useGroups:         false,
			checkExactVersion: true,
			exactVersion:      string(configv1.VersionTLS12),
			expectedCipherLen: 1,
			expectedCiphers:   crypto.OpenSSLToIANACipherSuites([]string{"ECDHE-RSA-AES128-GCM-SHA256"}),
			expectedGroups:    nil,
		},
		{
			name: "Custom profile without custom groups and feature gate on returns no curve preferences",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{
					TLSProfileSpec: configv1.TLSProfileSpec{
						MinTLSVersion: configv1.VersionTLS12,
						Ciphers:       []string{"ECDHE-RSA-AES128-GCM-SHA256"},
						Groups:        nil,
					},
				},
			},
			useGroups:         true,
			checkExactVersion: true,
			exactVersion:      string(configv1.VersionTLS12),
			expectedCipherLen: 1,
			expectedCiphers:   crypto.OpenSSLToIANACipherSuites([]string{"ECDHE-RSA-AES128-GCM-SHA256"}),
			expectedGroups:    expectedNoCustomGroups,
		},
		{
			name: "Custom profile without custom groups and feature gate off returns no curve preferences",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{
					TLSProfileSpec: configv1.TLSProfileSpec{
						MinTLSVersion: configv1.VersionTLS12,
						Ciphers:       []string{"ECDHE-RSA-AES128-GCM-SHA256"},
						Groups:        nil,
					},
				},
			},
			useGroups:         false,
			checkExactVersion: true,
			exactVersion:      string(configv1.VersionTLS12),
			expectedCipherLen: 1,
			expectedCiphers:   crypto.OpenSSLToIANACipherSuites([]string{"ECDHE-RSA-AES128-GCM-SHA256"}),
			expectedGroups:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version, ciphers, groups := resolveTLSProfile(tt.profile, tt.useGroups)

			if !reflect.DeepEqual(groups, tt.expectedGroups) {
				t.Errorf("expected groups %v, got %v", tt.expectedGroups, groups)
			}

			if tt.checkExactVersion {
				if version != tt.exactVersion {
					t.Errorf("expected version %q, got %q", tt.exactVersion, version)
				}
			} else if version != tt.expectedVersion {
				t.Errorf("expected version %q, got %q", tt.expectedVersion, version)
			}

			if len(ciphers) != tt.expectedCipherLen {
				t.Errorf("expected %d ciphers, got %d", tt.expectedCipherLen, len(ciphers))
			}

			if tt.expectedCiphers != nil {
				for i, expected := range tt.expectedCiphers {
					if i >= len(ciphers) {
						break
					}
					if ciphers[i] != expected {
						t.Errorf("cipher[%d]: expected %q, got %q", i, expected, ciphers[i])
					}
				}
			}
		})
	}
}

func TestBuildOperatorConfig(t *testing.T) {
	tests := []struct {
		name           string
		minTLSVersion  string
		cipherSuites   []string
		groups         []int32
		expectContains []string
		expectMissing  []string
	}{
		{
			name:          "with cipher suites",
			minTLSVersion: "VersionTLS12",
			cipherSuites:  []string{"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256", "TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384"},
			expectContains: []string{
				"apiVersion: operator.openshift.io/v1alpha1",
				"kind: GenericOperatorConfig",
				"minTLSVersion: VersionTLS12",
				"cipherSuites:",
				"- TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
				"- TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384",
			},
			expectMissing: []string{
				"curvePreferences:",
			},
		},
		{
			name:          "without cipher suites",
			minTLSVersion: "VersionTLS13",
			cipherSuites:  nil,
			expectContains: []string{
				"minTLSVersion: VersionTLS13",
			},
			expectMissing: []string{
				"cipherSuites:",
				"curvePreferences:",
			},
		},
		{
			name:          "empty cipher suites slice",
			minTLSVersion: "VersionTLS13",
			cipherSuites:  []string{},
			expectContains: []string{
				"minTLSVersion: VersionTLS13",
			},
			expectMissing: []string{
				"cipherSuites:",
				"curvePreferences:",
			},
		},
		{
			name:          "with curve preferences",
			minTLSVersion: "VersionTLS12",
			cipherSuites:  nil,
			groups:        []int32{29, 23},
			expectContains: []string{
				"minTLSVersion: VersionTLS12",
				"curvePreferences:",
				"- 29",
				"- 23",
			},
		},
		{
			name:          "empty groups slice produces no curve preferences",
			minTLSVersion: "VersionTLS13",
			groups:        []int32{},
			expectMissing: []string{
				"curvePreferences:",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildOperatorConfig(tt.minTLSVersion, tt.cipherSuites, tt.groups)

			for _, expected := range tt.expectContains {
				if !strings.Contains(result, expected) {
					t.Errorf("expected config to contain %q, got:\n%s", expected, result)
				}
			}

			for _, missing := range tt.expectMissing {
				if strings.Contains(result, missing) {
					t.Errorf("expected config NOT to contain %q, got:\n%s", missing, result)
				}
			}
		})
	}
}
