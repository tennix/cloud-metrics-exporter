package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

var (
	ErrNoEnabledProvider      = errors.New("no enabled provider configured")
	ErrMultipleProviders      = errors.New("exactly one enabled provider is supported in phase 1")
	ErrUnsupportedProvider    = errors.New("phase 1 supports only aliyun provider")
	ErrUnsupportedMetricType  = errors.New("phase 1 supports only disk and network metric types")
	ErrNoMetricTypes          = errors.New("at least one metric type must be configured")
	ErrInvalidDiscoveryConfig = errors.New("discovery.nodeRefreshInterval must be greater than zero")
	ErrInvalidScrapeConfig    = errors.New("scrape interval and timeout must be greater than zero")
)

type Config struct {
	Providers []ProviderConfig `yaml:"providers"`
	Discovery DiscoveryConfig  `yaml:"discovery"`
	Scrape    ScrapeConfig     `yaml:"scrape"`
}

type ProviderConfig struct {
	Name        string   `yaml:"name"`
	Enabled     bool     `yaml:"enabled"`
	Regions     []string `yaml:"regions"`
	MetricTypes []string `yaml:"metricTypes"`
}

type DiscoveryConfig struct {
	NodeRefreshInterval time.Duration `yaml:"nodeRefreshInterval"`
}

type ScrapeConfig struct {
	Interval time.Duration `yaml:"interval"`
	Timeout  time.Duration `yaml:"timeout"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func (c Config) Validate() error {
	provider, err := c.EnabledProvider()
	if err != nil {
		return err
	}

	if provider.Name != "aliyun" {
		return fmt.Errorf("%w: %s", ErrUnsupportedProvider, provider.Name)
	}

	if len(provider.MetricTypes) == 0 {
		return ErrNoMetricTypes
	}

	for _, metricType := range provider.MetricTypes {
		if metricType != "disk" && metricType != "network" {
			return fmt.Errorf("%w: %s", ErrUnsupportedMetricType, metricType)
		}
	}

	if c.Discovery.NodeRefreshInterval <= 0 {
		return ErrInvalidDiscoveryConfig
	}

	if c.Scrape.Interval <= 0 || c.Scrape.Timeout <= 0 {
		return ErrInvalidScrapeConfig
	}

	if c.Scrape.Timeout > c.Scrape.Interval {
		return fmt.Errorf("scrape timeout must be less than or equal to interval")
	}

	return nil
}

func (c Config) EnabledProvider() (ProviderConfig, error) {
	var enabled []ProviderConfig
	for _, provider := range c.Providers {
		if provider.Enabled {
			enabled = append(enabled, provider)
		}
	}

	switch len(enabled) {
	case 0:
		return ProviderConfig{}, ErrNoEnabledProvider
	case 1:
		return enabled[0], nil
	default:
		return ProviderConfig{}, ErrMultipleProviders
	}
}
