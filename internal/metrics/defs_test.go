package metrics

import "testing"

func TestDefinitionsContainPhaseOneMetrics(t *testing.T) {
	t.Parallel()

	required := map[string]Type{
		DiskIOReadTotal:               TypeCounter,
		DiskIOWriteTotal:              TypeCounter,
		DiskThroughputReadBytes:       TypeGauge,
		DiskThroughputWriteBytes:      TypeGauge,
		DiskLatencyMS:                 TypeGauge,
		DiskQueueDepth:                TypeGauge,
		NetworkReceiveBytesPerSecond:  TypeGauge,
		NetworkTransmitBytesPerSecond: TypeGauge,
	}

	for name, wantType := range required {
		def, ok := Definitions[name]
		if !ok {
			t.Fatalf("Definitions missing %q", name)
		}
		if def.Type != wantType {
			t.Fatalf("Definitions[%q].Type = %q, want %q", name, def.Type, wantType)
		}
		if def.Help == "" {
			t.Fatalf("Definitions[%q].Help must not be empty", name)
		}
	}
}

func TestRawName(t *testing.T) {
	t.Parallel()

	got := RawName("aliyun", "network", "net_vpc_rx_rate")
	if got != "cloud_raw_aliyun_network_net_vpc_rx_rate" {
		t.Fatalf("RawName() = %q", got)
	}
}
