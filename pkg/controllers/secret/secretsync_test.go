package secret

import (
	"bytes"
	"testing"

	"github.com/gophercloud/utils/v2/openstack/clientconfig"
	yaml "gopkg.in/yaml.v2"
)

func TestCloudToConf(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cloudsYAML string
		cloudCONF  map[string][]byte
	}{
		{
			name: "password_login",
			cloudsYAML: `
clouds:
    openstack:
        auth:
            auth_url: https://10.0.0.41:13000
            password: 'some password'
            project_domain_name: Default
            project_name: openshift
            user_domain_name: Default
            username: the_user_name
        cacert: /whatever/path/and/name.crt
        identity_api_version: '3'
        region_name: regionOne`,
			cloudCONF: map[string][]byte{
				"os-authURL":           []byte("https://10.0.0.41:13000"),
				"os-region":            []byte("regionOne"),
				"os-userName":          []byte("the_user_name"),
				"os-password":          []byte("some password"),
				"os-projectName":       []byte("openshift"),
				"os-projectDomainName": []byte("Default"),
				"os-userDomainName":    []byte("Default"),
				"os-domainName":        []byte("Default"),
				"os-certAuthorityPath": []byte("/etc/kubernetes/static-pod-resources/configmaps/cloud-config/ca-bundle.pem"),
			},
		},
		{
			name: "application_credentials_login",
			cloudsYAML: `
clouds:
    openstack:
        auth:
            auth_url: https://10.0.0.41:13000
            application_credential_id: '9c412bababababababababababababa9'
            application_credential_secret: 'Mbe8buGdQbMIvrQQp0F3qd0000000000000000000000000000000000000000000003UGp1Jo6n7iynXyGjkg'
        identity_api_version: '3'
        region_name: regionOne
        volume_api_version: '3'
        auth_type: "v3applicationcredential"`,
			cloudCONF: map[string][]byte{
				"os-authURL":                     []byte("https://10.0.0.41:13000"),
				"os-region":                      []byte("regionOne"),
				"os-applicationCredentialID":     []byte("9c412bababababababababababababa9"),
				"os-applicationCredentialSecret": []byte("Mbe8buGdQbMIvrQQp0F3qd0000000000000000000000000000000000000000000003UGp1Jo6n7iynXyGjkg"),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var clouds clientconfig.Clouds
			if err := yaml.Unmarshal([]byte(tc.cloudsYAML), &clouds); err != nil {
				// The YAML literal is hardcoded in this test
				// battery and it is assumed to be correct.
				panic(err)
			}
			have := cloudToConf(clouds.Clouds["openstack"])
			for haveK, haveV := range have {
				if wantV, ok := tc.cloudCONF[haveK]; ok {
					if !bytes.Equal(wantV, haveV) {
						t.Errorf("expected key %q to have value %q, found %q", haveK, wantV, haveV)
					}
				} else {
					t.Errorf("unexpected key %q", haveK)
				}
			}

			for wantK := range tc.cloudCONF {
				if _, ok := have[wantK]; !ok {
					t.Errorf("expected key %q, not found", wantK)
				}
			}
		})
	}
}
