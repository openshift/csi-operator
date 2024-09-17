package config

import (
	"fmt"
	"sort"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack/blockstorage/extensions/availabilityzones"
	"github.com/gophercloud/utils/openstack/clientconfig"
	azutils "github.com/gophercloud/utils/openstack/compute/v2/availabilityzones"
	"github.com/openshift/openstack-cinder-csi-driver-operator/pkg/version"
)

// CloudInfo caches data fetched from the user's openstack cloud
type CloudInfo struct {
	ComputeZones []string
	VolumeZones  []string

	clients *clients
}

type clients struct {
	computeClient *gophercloud.ServiceClient
	volumeClient  *gophercloud.ServiceClient
}

var ci *CloudInfo

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
		clients: &clients{},
	}

	opts := new(clientconfig.ClientOpts)
	opts.Cloud = "openstack"

	// we represent version using commits since we don't tag releases
	ua := gophercloud.UserAgent{}
	ua.Prepend(fmt.Sprintf("openstack-cinder-csi-driver-operator/%s", version.Get().GitCommit))

	ci.clients.computeClient, err = clientconfig.NewServiceClient("compute", opts)
	if err != nil {
		return nil, fmt.Errorf("failed to create a compute client: %w", err)
	}
	ci.clients.computeClient.UserAgent = ua

	ci.clients.volumeClient, err = clientconfig.NewServiceClient("volume", opts)
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

func (ci *CloudInfo) getComputeZones() ([]string, error) {
	zones, err := azutils.ListAvailableAvailabilityZones(ci.clients.computeClient)
	if err != nil {
		return nil, fmt.Errorf("failed to list compute availability zones: %w", err)
	}

	if len(zones) == 0 {
		return nil, fmt.Errorf("could not find an available compute availability zone")
	}

	sort.Strings(zones)

	return zones, nil
}

func (ci *CloudInfo) getVolumeZones() ([]string, error) {
	allPages, err := availabilityzones.List(ci.clients.volumeClient).AllPages()
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
