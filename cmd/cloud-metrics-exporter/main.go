package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/cloud-metrics-exporter/cloud-metrics-exporter/internal/config"
	"github.com/cloud-metrics-exporter/cloud-metrics-exporter/internal/discovery"
	"github.com/cloud-metrics-exporter/cloud-metrics-exporter/internal/exporter"
	aliyunprovider "github.com/cloud-metrics-exporter/cloud-metrics-exporter/internal/provider/aliyun"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	defaultConfigPath = "/config/config.yaml"
	metricsPort       = 9100
)

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", defaultConfigPath, "Path to configuration file")
	flag.Parse()

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	providerCfg, err := cfg.EnabledProvider()
	if err != nil {
		log.Fatalf("resolve provider config: %v", err)
	}

	provider, err := aliyunprovider.New(providerCfg.Regions)
	if err != nil {
		log.Fatalf("create aliyun provider: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := provider.Ping(ctx); err != nil {
		log.Fatalf("provider ping failed: %v", err)
	}

	clientset, err := newClientset()
	if err != nil {
		log.Fatalf("create kubernetes clientset: %v", err)
	}

	nodeDiscovery := discovery.NewNodeDiscovery(clientset, cfg.Discovery.NodeRefreshInterval)
	if err := nodeDiscovery.Start(ctx); err != nil {
		log.Fatalf("start node discovery: %v", err)
	}

	volumeEnricher := discovery.NewVolumeEnricher(clientset, cfg.Discovery.NodeRefreshInterval)
	if err := volumeEnricher.Start(ctx); err != nil {
		log.Fatalf("start volume enricher: %v", err)
	}
	provider.SetDiskEnricher(volumeEnricher)

	registry := prometheus.NewRegistry()
	collector := exporter.New(provider, nodeDiscovery, cfg.Scrape.Timeout)
	if err := registry.Register(collector); err != nil {
		log.Fatalf("register exporter collector: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	server := &http.Server{Addr: exporter.ListenAddress(metricsPort), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("cloud metrics exporter listening on %s", server.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve metrics: %v", err)
	}
}

func newClientset() (kubernetes.Interface, error) {
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(restConfig)
}
