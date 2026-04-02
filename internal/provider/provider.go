package provider

import (
	"context"

	"github.com/cloud-metrics-exporter/cloud-metrics-exporter/internal/discovery"
)

type Sample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

type Provider interface {
	Name() string
	Ping(ctx context.Context) error
	CollectDiskMetrics(ctx context.Context, targets []discovery.NodeTarget) ([]Sample, error)
	CollectNetworkMetrics(ctx context.Context, targets []discovery.NodeTarget) ([]Sample, error)
}
