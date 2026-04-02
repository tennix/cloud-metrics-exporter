package exporter

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/cloud-metrics-exporter/cloud-metrics-exporter/internal/discovery"
	projectmetrics "github.com/cloud-metrics-exporter/cloud-metrics-exporter/internal/metrics"
	"github.com/cloud-metrics-exporter/cloud-metrics-exporter/internal/provider"
	"github.com/prometheus/client_golang/prometheus"
)

var baseLabelNames = []string{"provider", "region", "instance_id", "node", "resource_type", "device", "interface", "metric_name", "disk_id"}

var diskLabelNames = []string{"provider", "region", "instance_id", "node", "resource_type", "device", "interface", "metric_name", "disk_id", "disk_role", "pv", "pvc", "namespace", "pod", "workload", "workload_kind"}

type nodeLister interface {
	List() []discovery.NodeTarget
}

type Collector struct {
	provider      provider.Provider
	nodeDiscovery nodeLister
	timeout       time.Duration
	staticDescs   []*prometheus.Desc
}

func New(provider provider.Provider, nodeDiscovery nodeLister, timeout time.Duration) *Collector {
	collector := &Collector{
		provider:      provider,
		nodeDiscovery: nodeDiscovery,
		timeout:       timeout,
	}

	for _, def := range projectmetrics.Definitions {
		collector.staticDescs = append(collector.staticDescs, prometheus.NewDesc(def.Name, def.Help, labelNamesForMetric(def.Name), nil))
	}

	sort.Slice(collector.staticDescs, func(i, j int) bool {
		return collector.staticDescs[i].String() < collector.staticDescs[j].String()
	})

	return collector
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range c.staticDescs {
		ch <- desc
	}
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	targets := c.nodeDiscovery.List()
	log.Printf("collector: discovered %d node targets", len(targets))
	diskCtx, diskCancel := context.WithTimeout(context.Background(), diskCollectionTimeout(c.timeout))
	defer diskCancel()

	diskSamples, err := c.provider.CollectDiskMetrics(diskCtx, targets)
	if err == nil {
		log.Printf("collector: collected %d disk samples", len(diskSamples))
		c.emitSamples(ch, diskSamples)
	} else {
		log.Printf("collector: disk collection error: %v", err)
	}

	networkCtx, networkCancel := context.WithTimeout(context.Background(), networkCollectionTimeout(c.timeout))
	defer networkCancel()

	networkSamples, err := c.provider.CollectNetworkMetrics(networkCtx, targets)
	if err == nil {
		log.Printf("collector: collected %d network samples", len(networkSamples))
		c.emitSamples(ch, networkSamples)
	} else {
		log.Printf("collector: network collection error: %v", err)
	}
}

func (c *Collector) emitSamples(ch chan<- prometheus.Metric, samples []provider.Sample) {
	for _, sample := range samples {
		def, ok := projectmetrics.Lookup(sample.Name)
		help := "Raw provider metric value."
		metricType := prometheus.GaugeValue
		if ok {
			help = def.Help
			if def.Type == projectmetrics.TypeCounter {
				metricType = prometheus.CounterValue
			}
		}

		labelNames := labelNamesForSample(sample)
		desc := prometheus.NewDesc(sample.Name, help, labelNames, nil)
		metric, err := prometheus.NewConstMetric(desc, metricType, sample.Value, labelValues(sample.Labels, labelNames)...)
		if err != nil {
			continue
		}
		ch <- metric
	}
}

func labelValues(labels map[string]string, names []string) []string {
	values := make([]string, 0, len(names))
	for _, name := range names {
		values = append(values, labels[name])
	}
	return values
}

func labelNamesForMetric(metricName string) []string {
	if strings.HasPrefix(metricName, "cloud_disk_") {
		return diskLabelNames
	}
	return baseLabelNames
}

func labelNamesForSample(sample provider.Sample) []string {
	if sample.Labels["resource_type"] == "disk" || strings.HasPrefix(sample.Name, "cloud_raw_aliyun_disk_") {
		return diskLabelNames
	}
	return baseLabelNames
}

func ListenAddress(port int) string {
	return fmt.Sprintf(":%d", port)
}

func diskCollectionTimeout(total time.Duration) time.Duration {
	if total <= 0 {
		return 5 * time.Second
	}
	if total <= 6*time.Second {
		return total / 2
	}
	if total/2 > 5*time.Second {
		return 5 * time.Second
	}
	return total / 2
}

func networkCollectionTimeout(total time.Duration) time.Duration {
	if total <= 0 {
		return 10 * time.Second
	}
	if total <= 6*time.Second {
		return total
	}
	remaining := total - diskCollectionTimeout(total)
	if remaining <= 0 {
		return total
	}
	return remaining
}
