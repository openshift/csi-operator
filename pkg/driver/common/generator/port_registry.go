package generator

const (
	AWSEBSLoopbackMetricsPortStart = 8201
	AWSEBSExposedMetricsPortStart  = 9201

	// it should be safe to reuse port for Azure because we do not
	// expect to deploy azure-disk and EBS driver on same clusters
	AzureDiskLoopbackMetricsPortStart = 8201
	AzureDiskExposedMetricsPortStart  = 9201

	AzureFileLoopbackMetricsPortStart = 8211
	AzureFileExposedMetricsPortStart  = 9211

	SambaLoopbackMetricsPortStart = 8221
	SambaExposedMetricsPortStart  = 9221
)
