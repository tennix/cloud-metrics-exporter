package metrics

type Type string

const (
	TypeCounter Type = "counter"
	TypeGauge   Type = "gauge"
)

type Definition struct {
	Name string
	Help string
	Type Type
}

const (
	DiskIOReadTotal               = "cloud_disk_io_read_total"
	DiskIOWriteTotal              = "cloud_disk_io_write_total"
	DiskThroughputReadBytes       = "cloud_disk_throughput_read_bytes"
	DiskThroughputWriteBytes      = "cloud_disk_throughput_write_bytes"
	DiskLatencyMS                 = "cloud_disk_latency_ms"
	DiskQueueDepth                = "cloud_disk_queue_depth"
	NetworkReceiveBytesPerSecond  = "cloud_network_receive_bytes_per_second"
	NetworkTransmitBytesPerSecond = "cloud_network_transmit_bytes_per_second"
)

var Definitions = map[string]Definition{
	DiskIOReadTotal: {
		Name: DiskIOReadTotal,
		Help: "Disk read I/O operations total.",
		Type: TypeCounter,
	},
	DiskIOWriteTotal: {
		Name: DiskIOWriteTotal,
		Help: "Disk write I/O operations total.",
		Type: TypeCounter,
	},
	DiskThroughputReadBytes: {
		Name: DiskThroughputReadBytes,
		Help: "Disk read throughput in bytes per second.",
		Type: TypeGauge,
	},
	DiskThroughputWriteBytes: {
		Name: DiskThroughputWriteBytes,
		Help: "Disk write throughput in bytes per second.",
		Type: TypeGauge,
	},
	DiskLatencyMS: {
		Name: DiskLatencyMS,
		Help: "Disk latency in milliseconds.",
		Type: TypeGauge,
	},
	DiskQueueDepth: {
		Name: DiskQueueDepth,
		Help: "Disk queue depth.",
		Type: TypeGauge,
	},
	NetworkReceiveBytesPerSecond: {
		Name: NetworkReceiveBytesPerSecond,
		Help: "Network receive rate in bytes per second.",
		Type: TypeGauge,
	},
	NetworkTransmitBytesPerSecond: {
		Name: NetworkTransmitBytesPerSecond,
		Help: "Network transmit rate in bytes per second.",
		Type: TypeGauge,
	},
}

func RawName(provider, resource, metric string) string {
	return "cloud_raw_" + provider + "_" + resource + "_" + metric
}

func Lookup(name string) (Definition, bool) {
	def, ok := Definitions[name]
	return def, ok
}
