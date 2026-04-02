package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	valid := Config{
		Providers: []ProviderConfig{{
			Name:        "aliyun",
			Enabled:     true,
			Regions:     []string{"cn-hangzhou"},
			MetricTypes: []string{"disk", "network"},
		}},
		Discovery: DiscoveryConfig{NodeRefreshInterval: time.Minute},
		Scrape:    ScrapeConfig{Interval: time.Minute, Timeout: 30 * time.Second},
	}

	tests := []struct {
		name    string
		cfg     Config
		wantErr error
	}{
		{name: "valid", cfg: valid},
		{name: "no enabled provider", cfg: Config{}, wantErr: ErrNoEnabledProvider},
		{name: "multiple enabled providers", cfg: Config{
			Providers: []ProviderConfig{{Name: "aliyun", Enabled: true, MetricTypes: []string{"disk"}}, {Name: "aliyun", Enabled: true, MetricTypes: []string{"network"}}},
			Discovery: DiscoveryConfig{NodeRefreshInterval: time.Second},
			Scrape:    ScrapeConfig{Interval: time.Second, Timeout: time.Second},
		}, wantErr: ErrMultipleProviders},
		{name: "unsupported provider", cfg: Config{
			Providers: []ProviderConfig{{Name: "aws", Enabled: true, MetricTypes: []string{"disk"}}},
			Discovery: DiscoveryConfig{NodeRefreshInterval: time.Second},
			Scrape:    ScrapeConfig{Interval: time.Second, Timeout: time.Second},
		}, wantErr: ErrUnsupportedProvider},
		{name: "unsupported metric type", cfg: Config{
			Providers: []ProviderConfig{{Name: "aliyun", Enabled: true, MetricTypes: []string{"disk", "lb"}}},
			Discovery: DiscoveryConfig{NodeRefreshInterval: time.Second},
			Scrape:    ScrapeConfig{Interval: time.Second, Timeout: time.Second},
		}, wantErr: ErrUnsupportedMetricType},
		{name: "missing metric types", cfg: Config{
			Providers: []ProviderConfig{{Name: "aliyun", Enabled: true}},
			Discovery: DiscoveryConfig{NodeRefreshInterval: time.Second},
			Scrape:    ScrapeConfig{Interval: time.Second, Timeout: time.Second},
		}, wantErr: ErrNoMetricTypes},
		{name: "invalid discovery interval", cfg: Config{
			Providers: []ProviderConfig{{Name: "aliyun", Enabled: true, MetricTypes: []string{"disk"}}},
			Scrape:    ScrapeConfig{Interval: time.Second, Timeout: time.Second},
		}, wantErr: ErrInvalidDiscoveryConfig},
		{name: "invalid scrape interval", cfg: Config{
			Providers: []ProviderConfig{{Name: "aliyun", Enabled: true, MetricTypes: []string{"disk"}}},
			Discovery: DiscoveryConfig{NodeRefreshInterval: time.Second},
		}, wantErr: ErrInvalidScrapeConfig},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.cfg.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() unexpected error = %v", err)
				}
				return
			}

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte(`providers:
  - name: aliyun
    enabled: true
    regions:
      - cn-hangzhou
    metricTypes:
      - disk
      - network
discovery:
  nodeRefreshInterval: 60s
scrape:
  interval: 60s
  timeout: 30s
`)

	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	provider, err := cfg.EnabledProvider()
	if err != nil {
		t.Fatalf("EnabledProvider() error = %v", err)
	}

	if provider.Name != "aliyun" {
		t.Fatalf("provider.Name = %q, want aliyun", provider.Name)
	}

	if cfg.Discovery.NodeRefreshInterval != time.Minute {
		t.Fatalf("nodeRefreshInterval = %v, want %v", cfg.Discovery.NodeRefreshInterval, time.Minute)
	}
}
