package manila

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/sharedfilesystems/v2/sharetypes"
	"github.com/gophercloud/utils/v2/openstack/clientconfig"
	"github.com/openshift/csi-driver-manila-operator/pkg/util"
	"github.com/openshift/csi-driver-manila-operator/pkg/version"
	"sigs.k8s.io/yaml"
)

type openStackClient struct {
	cloud *clientconfig.Cloud
}

func NewOpenStackClient(cloudConfigFilename string) (*openStackClient, error) {
	cloud, err := getCloudFromFile(cloudConfigFilename)
	if err != nil {
		return nil, err
	}
	return &openStackClient{
		cloud: cloud,
	}, nil
}

func (o *openStackClient) GetShareTypes() ([]sharetypes.ShareType, error) {
	clientOpts := new(clientconfig.ClientOpts)

	if o.cloud.AuthInfo != nil {
		clientOpts.AuthInfo = o.cloud.AuthInfo
		clientOpts.AuthType = o.cloud.AuthType
		clientOpts.Cloud = o.cloud.Cloud
		clientOpts.RegionName = o.cloud.RegionName
	}

	opts, err := clientconfig.AuthOptions(clientOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to generate auth options: %w", err)
	}

	provider, err := openstack.NewClient(opts.IdentityEndpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to create a provider client: %w", err)
	}

	// we represent version using commits since we don't tag releases
	ua := gophercloud.UserAgent{}
	ua.Prepend(fmt.Sprintf("csi-driver-manila-operator/%s", version.Get().GitCommit))
	provider.UserAgent = ua

	cert, err := getCloudProviderCert()
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to get cloud provider CA certificate: %w", err)
	}

	if len(cert) > 0 {
		certPool, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("create system cert pool failed: %w", err)
		}
		certPool.AppendCertsFromPEM(cert)
		client := http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				TLSClientConfig: &tls.Config{
					RootCAs: certPool,
				},
			},
		}
		provider.HTTPClient = client
	}

	provider.HTTPClient.Timeout = 120 * time.Second

	err = openstack.Authenticate(context.TODO(), provider, *opts)
	if err != nil {
		return nil, fmt.Errorf("cannot authenticate with given credentials: %w", err)
	}

	client, err := openstack.NewSharedFileSystemV2(provider, gophercloud.EndpointOpts{
		Region: clientOpts.RegionName,
	})
	if err != nil {
		return nil, fmt.Errorf("cannot find an endpoint for Shared File Systems API v2: %w", err)
	}

	allPages, err := sharetypes.List(client, &sharetypes.ListOpts{}).AllPages(context.TODO())
	if err != nil {
		return nil, fmt.Errorf("cannot list available share types: %w", err)
	}

	return sharetypes.ExtractShareTypes(allPages)
}

func getCloudFromFile(filename string) (*clientconfig.Cloud, error) {
	cloudConfig, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	var clouds clientconfig.Clouds
	err = yaml.Unmarshal(cloudConfig, &clouds)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal clouds credentials from %s: %w", filename, err)
	}

	cfg, ok := clouds.Clouds[util.CloudName]
	if !ok {
		return nil, fmt.Errorf("could not find cloud named %q in credential file %s", util.CloudName, filename)
	}
	return &cfg, nil
}

func getCloudProviderCert() ([]byte, error) {
	return ioutil.ReadFile(util.CertFile)
}
