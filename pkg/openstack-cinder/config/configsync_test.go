package config

import (
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
	"k8s.io/utils/ptr"
)

func TestGenerateConfigMap(t *testing.T) {
	format.MaxDepth = 100
	format.TruncatedDiff = false

	tc := []struct {
		name     string
		source   []byte
		caCert   *string
		expected string
		errMsg   string
	}{
		{
			name:   "Unset config",
			source: nil,
			expected: `[Global]
use-clouds  = true
clouds-file = /etc/kubernetes/secret/clouds.yaml
cloud       = openstack`,
		}, {
			name:   "Empty config",
			source: []byte(""),
			expected: `[Global]
use-clouds  = true
clouds-file = /etc/kubernetes/secret/clouds.yaml
cloud       = openstack`,
		}, {
			name: "Minimal config",
			source: []byte(`[BlockStorage]
ignore-volume-az = True`),
			expected: `[BlockStorage]
ignore-volume-az = True

[Global]
use-clouds  = true
clouds-file = /etc/kubernetes/secret/clouds.yaml
cloud       = openstack`,
		}, {
			name:   "With CA cert",
			source: nil,
			caCert: ptr.To("not-so-secret CA data goes here"),
			expected: `[Global]
use-clouds  = true
clouds-file = /etc/kubernetes/secret/clouds.yaml
cloud       = openstack
ca-file     = /etc/kubernetes/static-pod-resources/configmaps/cloud-config/ca-bundle.pem`,
		}, {
			name: "Legacy BlockStorage options are ignored",
			source: []byte(`
[BlockStorage]
trust-device-path = /dev/sdb1`),
			expected: `[Global]
use-clouds  = true
clouds-file = /etc/kubernetes/secret/clouds.yaml
cloud       = openstack`,
		},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			actual, err := generateConfig(
				[]byte(tc.source),
				tc.caCert,
			)
			if tc.errMsg != "" {
				g.Expect(err).Should(MatchError(tc.errMsg))
				return
			} else {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(strings.TrimSpace(tc.expected)).Should(Equal(strings.TrimSpace(actual)))
			}
		})
	}
}
