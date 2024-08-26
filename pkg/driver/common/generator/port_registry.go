package generator

// We can typically reuse ports as we don't expect to deploy CSI drivers for
// different clouds on the same cluster.
const (
	AWSEBSLoopbackMetricsPortStart = 8201
	AWSEBSExposedMetricsPortStart  = 9201

	AWSEFSLoopbackMetricsPortStart = 8211
	AWSEFSExposedMetricsPortStart  = 9211

	AzureDiskControllerLoopbackMetricsPortStart = 8201
	AzureDiskControllerExposedMetricsPortStart  = 9201
	AzureDiskNodeLoopbackMetricsPortStart       = 8206
	AzureDiskNodeExposedMetricsPortStart        = 9206

	AzureFileLoopbackMetricsPortStart = 8211
	AzureFileExposedMetricsPortStart  = 9211

	SambaLoopbackMetricsPortStart = 8221
	SambaExposedMetricsPortStart  = 9221

	OpenStackCinderLoopbackMetricsPortStart = 8202
	OpenStackCinderExposedMetricsPortStart  = 9202
)
