package generator

const (
	AWSEBSLoopbackMetricsPortStart = 8201
	AWSEBSExposedMetricsPortStart  = 9201

	// it should be safe to reuse port for Azure because we do not
	// expect to deploy azure-disk and EBS driver on same clusters
	AzureDiskLoopbackMetricsPortStart = 8201
	AzureDiskExposedMetricsPortStart  = 9201
)
