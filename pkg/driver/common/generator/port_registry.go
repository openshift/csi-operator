package generator

const (
	AWSEBSLoopbackMetricsPortStart = 8201
	AWSEBSExposedMetricsPortStart  = 9201

	AWSEFSLoopbackMetricsPortStart = 8211
	AWSEFSExposedMetricsPortStart  = 9211

	// it should be safe to reuse port for Azure because we do not
	// expect to deploy azure-disk and EBS driver on same clusters
	AzureDiskControllerLoopbackMetricsPortStart = 8201
	AzureDiskControllerExposedMetricsPortStart  = 9201
	AzureDiskNodeLoopbackMetricsPortStart       = 8206
	AzureDiskNodeExposedMetricsPortStart        = 9206

	AzureFileLoopbackMetricsPortStart = 8211
	AzureFileExposedMetricsPortStart  = 9211

	SambaLoopbackMetricsPortStart = 8221
	SambaExposedMetricsPortStart  = 9221
)
