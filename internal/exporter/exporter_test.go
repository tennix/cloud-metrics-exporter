package exporter

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cloud-metrics-exporter/cloud-metrics-exporter/internal/discovery"
	"github.com/cloud-metrics-exporter/cloud-metrics-exporter/internal/provider"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/common/expfmt"
)

type stubDiscovery struct {
	targets []discovery.NodeTarget
}

func (s stubDiscovery) List() []discovery.NodeTarget { return s.targets }

type stubProvider struct {
	disk    []provider.Sample
	network []provider.Sample
}

func (s stubProvider) Name() string                 { return "stub" }
func (s stubProvider) Ping(_ context.Context) error { return nil }
func (s stubProvider) CollectDiskMetrics(_ context.Context, _ []discovery.NodeTarget) ([]provider.Sample, error) {
	return s.disk, nil
}
func (s stubProvider) CollectNetworkMetrics(_ context.Context, _ []discovery.NodeTarget) ([]provider.Sample, error) {
	return s.network, nil
}

func TestCollectorExportsMetricsWithoutTimestamps(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	collector := New(stubProvider{
		disk: []provider.Sample{{
			Name:   "cloud_disk_latency_ms",
			Value:  12.5,
			Labels: map[string]string{"provider": "aliyun", "region": "cn-hangzhou", "instance_id": "i-bp123", "node": "worker-1", "resource_type": "disk"},
		}},
		network: []provider.Sample{{
			Name:   "cloud_network_receive_bytes_per_second",
			Value:  256,
			Labels: map[string]string{"provider": "aliyun", "region": "cn-hangzhou", "instance_id": "i-bp123", "node": "worker-1", "resource_type": "network"},
		}},
	}, stubDiscovery{}, time.Second)

	if err := registry.Register(collector); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	output, err := testutil.CollectAndFormat(collector, expfmt.TypeTextPlain, "cloud_disk_latency_ms", "cloud_network_receive_bytes_per_second")
	if err != nil {
		t.Fatalf("CollectAndFormat() error = %v", err)
	}
	text := string(output)
	if !strings.Contains(text, "# HELP cloud_disk_latency_ms") || !strings.Contains(text, "# TYPE cloud_network_receive_bytes_per_second gauge") {
		t.Fatalf("unexpected exposition:\n%s", text)
	}
	if !strings.Contains(text, "cloud_disk_latency_ms{device=\"\",disk_id=\"\",disk_role=\"\"") {
		t.Fatalf("disk metrics must include disk enrichment labels:\n%s", text)
	}
	if strings.Contains(text, "cloud_network_receive_bytes_per_second{device=\"\",disk_id=\"\",disk_role=") {
		t.Fatalf("network metrics must not include disk enrichment labels:\n%s", text)
	}
	if strings.Contains(text, " 171") {
		t.Fatalf("exposition must not contain explicit timestamps:\n%s", text)
	}
}
