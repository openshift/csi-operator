package config

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/availabilityzones"
	"github.com/gophercloud/utils/v2/openstack/clientconfig"
	azutils "github.com/gophercloud/utils/v2/openstack/compute/v2/availabilityzones"
	"github.com/openshift/csi-operator/pkg/version"
)

// CloudInfo caches data fetched from the user's openstack cloud
type CloudInfo struct {
	ComputeZones []string
	VolumeZones  []string

	clients *openstackClients
}

type openstackClients struct {
	computeClient *gophercloud.ServiceClient
	volumeClient  *gophercloud.ServiceClient
}

var ci *CloudInfo

// enableTopologyFeature determines whether the topology feature flag for the CSI external
// provisioner sidecar container should be set to enabled or disabled, based on whether we have a
// mapping of compute AZs to block storage AZs or not.
func enableTopologyFeature() (bool, error) {
	var err error

	if ci == nil {
		ci, err = getCloudInfo()
		if err != nil {
			return false, fmt.Errorf("couldn't collect info about cloud availability zones: %w", err)
		}
	}

	// for us to enable the topology feature we should have a corresponding
	// compute AZ for each volume AZ: if we have more compute AZs than volume
	// AZs then this clearly isn't the case
	if len(ci.ComputeZones) > len(ci.VolumeZones) {
		return false, nil
	}

	// likewise if the names of the various AZs don't match, that clearly isn't
	// true
	for _, computeZone := range ci.ComputeZones {
		var found bool
		for _, volumeZone := range ci.VolumeZones {
			if computeZone == volumeZone {
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
	}

	return true, nil
}

// getCloudInfo fetches and caches metadata from openstack
func getCloudInfo() (*CloudInfo, error) {
	var ci *CloudInfo
	var err error

	ci = &CloudInfo{
		clients: &openstackClients{},
	}

	opts := new(clientconfig.ClientOpts)
	opts.Cloud = "openstack"

	// we represent version using commits since we don't tag releases
	ua := gophercloud.UserAgent{}
	ua.Prepend(fmt.Sprintf("csi-operator/%s", version.Get().GitCommit))

	// our assets mount the CA file at a known path and we don't want to rely on other things
	// setting the 'cafile' value in our clouds.yaml file to the same path, so we explicitly
	// override this if a CA file is present
	// NOTE(stephenfin): gophercloud (or rather, the clientconfig package) doesn't currently
	// provide the way to override configuration other than via environment variables
	if _, err := os.Stat(caFile); err == nil {
		os.Setenv("OS_CACERT", caFile)
	} else if _, err := os.Stat(legacyCAFile); err == nil { // legacy path
		os.Setenv("OS_CACERT", legacyCAFile)
	}

	ci.clients.computeClient, err = clientconfig.NewServiceClient(context.TODO(), "compute", opts)
	if err != nil {
		return nil, fmt.Errorf("failed to create a compute client: %w", err)
	}
	ci.clients.computeClient.UserAgent = ua

	ci.clients.volumeClient, err = clientconfig.NewServiceClient(context.TODO(), "volume", opts)
	if err != nil {
		return nil, fmt.Errorf("failed to create a volume client: %w", err)
	}
	ci.clients.volumeClient.UserAgent = ua

	err = ci.collectInfo()
	if err != nil {
		return nil, fmt.Errorf("failed to generate OpenStack cloud info: %w", err)
	}

	return ci, nil
}

// collectInfo fetches AZ information from the compute and block storage services
func (ci *CloudInfo) collectInfo() error {
	var err error

	ci.ComputeZones, err = ci.getComputeZones()
	if err != nil {
		return err
	}

	ci.VolumeZones, err = ci.getVolumeZones()
	if err != nil {
		return err
	}

	return nil
}

// getComputeZones fetches AZ information from the compute service
func (ci *CloudInfo) getComputeZones() ([]string, error) {
	zones, err := azutils.ListAvailableAvailabilityZones(context.TODO(), ci.clients.computeClient)
	if err != nil {
		return nil, fmt.Errorf("failed to list compute availability zones: %w", err)
	}

	if len(zones) == 0 {
		return nil, fmt.Errorf("could not find an available compute availability zone")
	}

	sort.Strings(zones)

	return zones, nil
}

// getVolumeZones fetches AZ information from the block storage service
func (ci *CloudInfo) getVolumeZones() ([]string, error) {
	allPages, err := availabilityzones.List(ci.clients.volumeClient).AllPages(context.TODO())
	if err != nil {
		return nil, fmt.Errorf("failed to list volume availability zones: %w", err)
	}

	availabilityZoneInfo, err := availabilityzones.ExtractAvailabilityZones(allPages)
	if err != nil {
		return nil, fmt.Errorf("failed to parse response with volume availability zone list: %w", err)
	}

	if len(availabilityZoneInfo) == 0 {
		return nil, fmt.Errorf("could not find an available volume availability zone")
	}

	var zones []string
	for _, zone := range availabilityZoneInfo {
		if zone.ZoneState.Available {
			zones = append(zones, zone.ZoneName)
		}
	}

	sort.Strings(zones)

	return zones, nil
}
