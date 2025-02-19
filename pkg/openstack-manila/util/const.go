package util

const (
	ManilaSecretName          = "csi-manila-secrets"

	CloudConfigNamespace = "openshift-config"
	CloudConfigName      = "cloud-provider-config"

	StorageClassNamePrefix = "csi-manila-"

	// OpenStack config files
	// Note that these are for the operator, not the driver itself. The paths
	// are defined in cluster-storage-operator
	CloudConfigFilename = "/etc/openstack/clouds.yaml"
	CertFile            = "/etc/openstack/ca.crt"

	// Name of cloud in secret provided by cloud-credentials-operator
	CloudName = "openstack"
)
