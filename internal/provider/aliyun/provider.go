package aliyun

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	openapiv2 "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	ecs "github.com/alibabacloud-go/ecs-20140526/v4/client"
	"github.com/alibabacloud-go/tea/tea"
	"github.com/aliyun/credentials-go/credentials"
	"github.com/cloud-metrics-exporter/cloud-metrics-exporter/internal/discovery"
	projectmetrics "github.com/cloud-metrics-exporter/cloud-metrics-exporter/internal/metrics"
	"github.com/cloud-metrics-exporter/cloud-metrics-exporter/internal/provider"
)

const defaultPeriod = 60
const defaultMaxDiskRefreshPerScrape = 6

var (
	ErrNoTargets                 = errors.New("no aliyun targets to collect")
	ErrUnsupportedCredentialType = errors.New("unsupported aliyun credential type")
	metricSanitizer              = regexp.MustCompile(`[^a-zA-Z0-9_]+`)
)

type metricQuery struct {
	MetricName string
	Resource   string
	Standard   string
	MetricType projectmetrics.Type
	UnitScale  float64
	Optional   bool
	RawOnly    bool
}

type ecsAPI interface {
	DescribeDisks(request *ecs.DescribeDisksRequest) (*ecs.DescribeDisksResponse, error)
	DescribeDiskMonitorData(request *ecs.DescribeDiskMonitorDataRequest) (*ecs.DescribeDiskMonitorDataResponse, error)
	DescribeInstanceMonitorData(request *ecs.DescribeInstanceMonitorDataRequest) (*ecs.DescribeInstanceMonitorDataResponse, error)
}

type diskEnricher interface {
	LookupByDiskID(diskID string) discovery.DiskEnrichment
}

type Provider struct {
	credential         credentials.Credential
	ecsClientFactory   func(region string) (ecsAPI, error)
	now                func() time.Time
	period             time.Duration
	regions            []string
	metricsByType      map[string][]metricQuery
	clientMu           sync.Mutex
	ecsClientsByRegion map[string]ecsAPI
	enricher           diskEnricher
	diskStateMu        sync.Mutex
	diskSampleCache    map[string][]provider.Sample
	diskRefreshCursor  int
	maxDiskRefresh     int
}

type diskTarget struct {
	key    string
	diskID string
	role   string
	target discovery.NodeTarget
}

func New(regions []string) (*Provider, error) {
	cred, err := credentials.NewCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("create aliyun credential provider: %w", err)
	}

	p := &Provider{
		credential:         cred,
		period:             time.Duration(defaultPeriod) * time.Second,
		regions:            regions,
		now:                time.Now,
		ecsClientsByRegion: make(map[string]ecsAPI),
		diskSampleCache:    make(map[string][]provider.Sample),
		maxDiskRefresh:     defaultMaxDiskRefreshPerScrape,
	}
	p.ecsClientFactory = p.defaultECSClientFactory
	p.metricsByType = map[string][]metricQuery{
		"disk": {
			{MetricName: "BPSRead", Resource: "disk", Standard: projectmetrics.DiskThroughputReadBytes, MetricType: projectmetrics.TypeGauge, UnitScale: 1},
			{MetricName: "BPSWrite", Resource: "disk", Standard: projectmetrics.DiskThroughputWriteBytes, MetricType: projectmetrics.TypeGauge, UnitScale: 1},
			{MetricName: "LatencyRead", Resource: "disk", Standard: projectmetrics.DiskLatencyMS, MetricType: projectmetrics.TypeGauge, UnitScale: 1.0 / 1000.0},
			{MetricName: "LatencyWrite", Resource: "disk", MetricType: projectmetrics.TypeGauge, UnitScale: 1.0 / 1000.0, Optional: true, RawOnly: true},
			{MetricName: "IOPSRead", Resource: "disk", MetricType: projectmetrics.TypeGauge, UnitScale: 1, RawOnly: true},
			{MetricName: "IOPSWrite", Resource: "disk", MetricType: projectmetrics.TypeGauge, UnitScale: 1, RawOnly: true},
		},
		"network": {
			{MetricName: "IntranetRX", Resource: "network", Standard: projectmetrics.NetworkReceiveBytesPerSecond, MetricType: projectmetrics.TypeGauge, UnitScale: 1000.0 / 8.0 / float64(defaultPeriod)},
			{MetricName: "IntranetTX", Resource: "network", Standard: projectmetrics.NetworkTransmitBytesPerSecond, MetricType: projectmetrics.TypeGauge, UnitScale: 1000.0 / 8.0 / float64(defaultPeriod)},
		},
	}

	return p, nil
}

func (p *Provider) SetDiskEnricher(enricher diskEnricher) {
	p.enricher = enricher
}

func (p *Provider) Name() string { return "aliyun" }

func (p *Provider) Ping(ctx context.Context) error {
	_ = ctx
	credential, err := p.credential.GetCredential()
	if err != nil {
		return fmt.Errorf("resolve aliyun credential: %w", err)
	}
	if credential == nil || credential.AccessKeyId == nil || credential.AccessKeySecret == nil {
		return fmt.Errorf("aliyun credential chain returned incomplete credentials")
	}
	if err := validateCredentialType(tea.StringValue(credential.Type)); err != nil {
		return err
	}
	return nil
}

func validateCredentialType(credentialType string) error {
	switch credentialType {
	case "oidc_role_arn", "ecs_ram_role", "default":
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrUnsupportedCredentialType, credentialType)
	}
}

func (p *Provider) CollectDiskMetrics(ctx context.Context, targets []discovery.NodeTarget) ([]provider.Sample, error) {
	aliyunTargets := filterAliyunTargets(targets)
	if len(aliyunTargets) == 0 {
		log.Printf("aliyun: no disk targets after filtering %d discovered nodes", len(targets))
		return nil, ErrNoTargets
	}
	log.Printf("aliyun: collecting disk metrics for %d targets", len(aliyunTargets))

	var diskTargets []diskTarget
	for _, target := range aliyunTargets {
		log.Printf("aliyun: disk collection target node=%s region=%s instance=%s", target.NodeName, target.Region, target.InstanceID)
		diskRoles, err := p.describeDiskRoles(target)
		if err != nil {
			return p.cachedDiskSamples(nil), err
		}
		log.Printf("aliyun: describe disks returned %d roles for instance=%s", len(diskRoles), target.InstanceID)

		for diskID, role := range diskRoles {
			if role != "system" && role != "data" {
				continue
			}
			diskTargets = append(diskTargets, diskTarget{
				key:    diskCacheKey(target, diskID),
				diskID: diskID,
				role:   role,
				target: target,
			})
		}
	}

	selected, discoveredKeys := p.selectDiskTargets(diskTargets)
	for _, disk := range selected {
		select {
		case <-ctx.Done():
			return p.cachedDiskSamples(discoveredKeys), ctx.Err()
		default:
		}

		client, err := p.ecsClientForRegion(disk.target.Region)
		if err != nil {
			return p.cachedDiskSamples(discoveredKeys), err
		}

		diskSamples, err := p.collectDiskSamplesForDisk(client, disk.target, disk.diskID, map[string]string{disk.diskID: disk.role})
		if err != nil {
			if isThrottleError(err) {
				log.Printf("aliyun: throttled collecting disk metrics, returning cached/partial disk samples: %v", err)
				return p.cachedDiskSamples(discoveredKeys), nil
			}
			return p.cachedDiskSamples(discoveredKeys), err
		}
		p.setDiskCacheEntry(disk.key, diskSamples)
	}

	samples := p.cachedDiskSamples(discoveredKeys)
	log.Printf("aliyun: disk collection produced %d cached/current samples", len(samples))
	return samples, nil
}

func (p *Provider) CollectNetworkMetrics(ctx context.Context, targets []discovery.NodeTarget) ([]provider.Sample, error) {
	aliyunTargets := filterAliyunTargets(targets)
	if len(aliyunTargets) == 0 {
		log.Printf("aliyun: no network targets after filtering %d discovered nodes", len(targets))
		return nil, ErrNoTargets
	}
	log.Printf("aliyun: collecting %d network metrics across %d targets", len(p.metricsByType["network"]), len(aliyunTargets))

	var samples []provider.Sample
	for _, target := range aliyunTargets {
		client, err := p.ecsClientForRegion(target.Region)
		if err != nil {
			return nil, err
		}

		for _, query := range p.metricsByType["network"] {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}

			sampleBatch, err := p.queryNetworkMetric(client, target, query)
			if err != nil {
				if query.Optional {
					continue
				}
				return nil, err
			}
			samples = append(samples, sampleBatch...)
		}
	}

	log.Printf("aliyun: network collection produced %d samples", len(samples))
	return samples, nil
}

func filterAliyunTargets(targets []discovery.NodeTarget) []discovery.NodeTarget {
	filtered := make([]discovery.NodeTarget, 0, len(targets))
	for _, target := range targets {
		if target.Provider == "aliyun" && target.InstanceID != "" && target.Region != "" {
			filtered = append(filtered, target)
		}
	}
	return filtered
}

func (p *Provider) defaultECSClientFactory(region string) (ecsAPI, error) {
	config := &openapiv2.Config{
		RegionId:   tea.String(region),
		Endpoint:   tea.String(fmt.Sprintf("ecs.%s.aliyuncs.com", region)),
		Credential: p.credential,
	}
	client, err := ecs.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("create ecs client for region %s: %w", region, err)
	}
	return client, nil
}

func (p *Provider) ecsClientForRegion(region string) (ecsAPI, error) {
	p.clientMu.Lock()
	defer p.clientMu.Unlock()

	if client, ok := p.ecsClientsByRegion[region]; ok {
		return client, nil
	}

	client, err := p.ecsClientFactory(region)
	if err != nil {
		return nil, err
	}
	p.ecsClientsByRegion[region] = client
	return client, nil
}

func (p *Provider) describeDiskRoles(target discovery.NodeTarget) (map[string]string, error) {
	client, err := p.ecsClientForRegion(target.Region)
	if err != nil {
		return nil, err
	}

	request := &ecs.DescribeDisksRequest{}
	request.RegionId = tea.String(target.Region)
	request.InstanceId = tea.String(target.InstanceID)
	request.PageSize = tea.Int32(100)
	request.PageNumber = tea.Int32(1)

	response, err := client.DescribeDisks(request)
	if err != nil {
		return nil, fmt.Errorf("describe ecs disks for instance %s: %w", target.InstanceID, err)
	}

	roles := map[string]string{}
	if response == nil || response.Body == nil || response.Body.Disks == nil {
		log.Printf("aliyun: describe disks returned empty body for instance=%s region=%s", target.InstanceID, target.Region)
		return roles, nil
	}
	for _, disk := range response.Body.Disks.Disk {
		if disk == nil || disk.DiskId == nil || disk.Type == nil {
			continue
		}
		roles[tea.StringValue(disk.DiskId)] = tea.StringValue(disk.Type)
	}
	return roles, nil
}

func (p *Provider) queryNetworkMetric(client ecsAPI, target discovery.NodeTarget, query metricQuery) ([]provider.Sample, error) {
	point, err := p.fetchInstanceMonitorData(client, target)
	if err != nil {
		return nil, err
	}
	if point == nil {
		return nil, nil
	}

	value, ok := instanceMonitorValue(point, query.MetricName)
	if !ok {
		return nil, nil
	}
	value *= query.UnitScale

	labels := baseLabels(target, query.Resource)
	name := query.Standard
	if query.RawOnly || name == "" {
		name = projectmetrics.RawName("aliyun", query.Resource, sanitizeMetricName(query.MetricName))
		labels["metric_name"] = query.MetricName
	}

	return []provider.Sample{{Name: name, Labels: labels, Value: value}}, nil
}

func (p *Provider) queryDiskMetric(client ecsAPI, target discovery.NodeTarget, diskID string, query metricQuery, diskRoles map[string]string) ([]provider.Sample, error) {
	point, err := p.fetchDiskMonitorData(client, target, diskID)
	if err != nil {
		return nil, err
	}
	if point == nil {
		return nil, nil
	}

	value, ok := diskMonitorValue(point, query.MetricName)
	if !ok {
		return nil, nil
	}
	value *= query.UnitScale

	labels := baseLabels(target, query.Resource)
	labels["disk_id"] = diskID

	role := diskRoles[diskID]
	if role != "system" && role != "data" {
		return nil, nil
	}
	labels["disk_role"] = role

	if role == "data" {
		enrichment := p.lookupDiskEnrichment(diskID)
		if !enrichment.Matched && !query.RawOnly && query.Standard != "" {
			return nil, nil
		}
		labels["pv"] = enrichment.PV
		labels["pvc"] = enrichment.PVC
		labels["namespace"] = enrichment.Namespace
		labels["pod"] = enrichment.Pod
		labels["workload"] = enrichment.Workload
		labels["workload_kind"] = enrichment.WorkloadKind
	}

	name := query.Standard
	if query.RawOnly || name == "" {
		name = projectmetrics.RawName("aliyun", query.Resource, sanitizeMetricName(query.MetricName))
		labels["metric_name"] = query.MetricName
	}

	return []provider.Sample{{Name: name, Labels: labels, Value: value}}, nil
}

func (p *Provider) collectDiskSamplesForDisk(client ecsAPI, target discovery.NodeTarget, diskID string, diskRoles map[string]string) ([]provider.Sample, error) {
	point, err := p.fetchDiskMonitorData(client, target, diskID)
	if err != nil {
		return nil, err
	}
	if point == nil {
		return nil, nil
	}

	labels := baseLabels(target, "disk")
	labels["disk_id"] = diskID

	role := diskRoles[diskID]
	if role != "system" && role != "data" {
		return nil, nil
	}
	labels["disk_role"] = role

	if role == "data" {
		enrichment := p.lookupDiskEnrichment(diskID)
		labels["pv"] = enrichment.PV
		labels["pvc"] = enrichment.PVC
		labels["namespace"] = enrichment.Namespace
		labels["pod"] = enrichment.Pod
		labels["workload"] = enrichment.Workload
		labels["workload_kind"] = enrichment.WorkloadKind

		if !enrichment.Matched {
			allRaw := true
			for _, query := range p.metricsByType["disk"] {
				if !query.RawOnly && query.Standard != "" {
					allRaw = false
					break
				}
			}
			if allRaw {
				return nil, nil
			}
		}
	}

	var samples []provider.Sample
	for _, query := range p.metricsByType["disk"] {
		sampleBatch, err := p.buildDiskSampleFromPoint(target, diskID, labels, query, point, role)
		if err != nil {
			if query.Optional {
				continue
			}
			return samples, err
		}
		samples = append(samples, sampleBatch...)
	}

	return samples, nil
}

func diskCacheKey(target discovery.NodeTarget, diskID string) string {
	return target.Region + "/" + target.InstanceID + "/" + diskID
}

func (p *Provider) selectDiskTargets(targets []diskTarget) ([]diskTarget, map[string]struct{}) {
	sort.Slice(targets, func(i, j int) bool { return targets[i].key < targets[j].key })
	discovered := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		discovered[target.key] = struct{}{}
	}
	p.pruneDiskCache(discovered)
	if len(targets) == 0 {
		return nil, discovered
	}

	p.diskStateMu.Lock()
	defer p.diskStateMu.Unlock()
	if p.diskSampleCache == nil {
		p.diskSampleCache = make(map[string][]provider.Sample)
	}
	maxRefresh := p.maxDiskRefresh
	if maxRefresh <= 0 || maxRefresh > len(targets) {
		maxRefresh = len(targets)
	}
	start := 0
	if len(targets) > 0 {
		start = p.diskRefreshCursor % len(targets)
	}
	selected := make([]diskTarget, 0, maxRefresh)
	for i := 0; i < maxRefresh; i++ {
		idx := (start + i) % len(targets)
		selected = append(selected, targets[idx])
	}
	p.diskRefreshCursor = (start + maxRefresh) % len(targets)
	return selected, discovered
}

func (p *Provider) pruneDiskCache(discovered map[string]struct{}) {
	p.diskStateMu.Lock()
	defer p.diskStateMu.Unlock()
	if p.diskSampleCache == nil {
		p.diskSampleCache = make(map[string][]provider.Sample)
	}
	for key := range p.diskSampleCache {
		if _, ok := discovered[key]; !ok {
			delete(p.diskSampleCache, key)
		}
	}
}

func (p *Provider) setDiskCacheEntry(key string, samples []provider.Sample) {
	p.diskStateMu.Lock()
	defer p.diskStateMu.Unlock()
	if p.diskSampleCache == nil {
		p.diskSampleCache = make(map[string][]provider.Sample)
	}
	if len(samples) == 0 {
		delete(p.diskSampleCache, key)
		return
	}
	cloned := make([]provider.Sample, len(samples))
	copy(cloned, samples)
	p.diskSampleCache[key] = cloned
}

func (p *Provider) cachedDiskSamples(discovered map[string]struct{}) []provider.Sample {
	p.diskStateMu.Lock()
	defer p.diskStateMu.Unlock()
	if p.diskSampleCache == nil {
		return nil
	}
	var samples []provider.Sample
	for key, cached := range p.diskSampleCache {
		if discovered != nil {
			if _, ok := discovered[key]; !ok {
				continue
			}
		}
		samples = append(samples, cached...)
	}
	return samples
}

func (p *Provider) buildDiskSampleFromPoint(target discovery.NodeTarget, diskID string, base map[string]string, query metricQuery, point *ecs.DescribeDiskMonitorDataResponseBodyMonitorDataDiskMonitorData, role string) ([]provider.Sample, error) {
	value, ok := diskMonitorValue(point, query.MetricName)
	if !ok {
		return nil, nil
	}
	value *= query.UnitScale

	labels := cloneLabels(base)
	if role == "data" {
		if labels["pv"] == "" && labels["pvc"] == "" && !query.RawOnly && query.Standard != "" {
			return nil, nil
		}
	}

	name := query.Standard
	if query.RawOnly || name == "" {
		name = projectmetrics.RawName("aliyun", query.Resource, sanitizeMetricName(query.MetricName))
		labels["metric_name"] = query.MetricName
	}

	return []provider.Sample{{Name: name, Labels: labels, Value: value}}, nil
}

func (p *Provider) lookupDiskEnrichment(diskID string) discovery.DiskEnrichment {
	if p.enricher == nil {
		return discovery.DiskEnrichment{}
	}
	return p.enricher.LookupByDiskID(diskID)
}

func (p *Provider) fetchDiskMonitorData(client ecsAPI, target discovery.NodeTarget, diskID string) (*ecs.DescribeDiskMonitorDataResponseBodyMonitorDataDiskMonitorData, error) {
	now := p.now().UTC()
	request := &ecs.DescribeDiskMonitorDataRequest{
		DiskId:    tea.String(diskID),
		StartTime: tea.String(now.Add(-p.period).Format(time.RFC3339)),
		EndTime:   tea.String(now.Format(time.RFC3339)),
		Period:    tea.Int32(int32(p.period.Seconds())),
	}

	response, err := client.DescribeDiskMonitorData(request)
	if err != nil {
		return nil, fmt.Errorf("describe disk monitor data for disk %s: %w", diskID, err)
	}
	if response == nil || response.Body == nil || response.Body.MonitorData == nil || len(response.Body.MonitorData.DiskMonitorData) == 0 {
		log.Printf("aliyun: no disk monitor datapoints for disk=%s region=%s instance=%s", diskID, target.Region, target.InstanceID)
		return nil, nil
	}
	point := response.Body.MonitorData.DiskMonitorData[len(response.Body.MonitorData.DiskMonitorData)-1]
	log.Printf("aliyun: fetched disk monitor datapoint for disk=%s region=%s instance=%s", diskID, target.Region, target.InstanceID)
	return point, nil
}

func (p *Provider) fetchInstanceMonitorData(client ecsAPI, target discovery.NodeTarget) (*ecs.DescribeInstanceMonitorDataResponseBodyMonitorDataInstanceMonitorData, error) {
	now := p.now().UTC()
	request := &ecs.DescribeInstanceMonitorDataRequest{
		InstanceId: tea.String(target.InstanceID),
		StartTime:  tea.String(now.Add(-p.period).Format(time.RFC3339)),
		EndTime:    tea.String(now.Format(time.RFC3339)),
		Period:     tea.Int32(int32(p.period.Seconds())),
	}

	response, err := client.DescribeInstanceMonitorData(request)
	if err != nil {
		return nil, fmt.Errorf("describe instance monitor data for instance %s: %w", target.InstanceID, err)
	}
	if response == nil || response.Body == nil || response.Body.MonitorData == nil || len(response.Body.MonitorData.InstanceMonitorData) == 0 {
		log.Printf("aliyun: no instance monitor datapoints for region=%s instance=%s", target.Region, target.InstanceID)
		return nil, nil
	}
	point := response.Body.MonitorData.InstanceMonitorData[len(response.Body.MonitorData.InstanceMonitorData)-1]
	log.Printf("aliyun: fetched instance monitor datapoint for region=%s instance=%s", target.Region, target.InstanceID)
	return point, nil
}

func diskMonitorValue(point *ecs.DescribeDiskMonitorDataResponseBodyMonitorDataDiskMonitorData, metric string) (float64, bool) {
	switch metric {
	case "BPSRead":
		return int32Value(point.BPSRead)
	case "BPSWrite":
		return int32Value(point.BPSWrite)
	case "LatencyRead":
		return int32Value(point.LatencyRead)
	case "LatencyWrite":
		return int32Value(point.LatencyWrite)
	case "IOPSRead":
		return int32Value(point.IOPSRead)
	case "IOPSWrite":
		return int32Value(point.IOPSWrite)
	default:
		return 0, false
	}
}

func instanceMonitorValue(point *ecs.DescribeInstanceMonitorDataResponseBodyMonitorDataInstanceMonitorData, metric string) (float64, bool) {
	switch metric {
	case "IntranetRX":
		return int32Value(point.IntranetRX)
	case "IntranetTX":
		return int32Value(point.IntranetTX)
	default:
		return 0, false
	}
}

func int32Value(v *int32) (float64, bool) {
	if v == nil {
		return 0, false
	}
	return float64(*v), true
}

func cloneLabels(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func isThrottleError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Code: Throttling") || strings.Contains(msg, "Request was denied due to request throttling")
}

func baseLabels(target discovery.NodeTarget, resource string) map[string]string {
	return map[string]string{
		"provider":      "aliyun",
		"region":        target.Region,
		"instance_id":   target.InstanceID,
		"node":          target.NodeName,
		"resource_type": resource,
		"device":        "",
		"interface":     "",
		"metric_name":   "",
		"disk_id":       "",
		"disk_role":     "",
		"pv":            "",
		"pvc":           "",
		"namespace":     "",
		"pod":           "",
		"workload":      "",
		"workload_kind": "",
	}
}

func sanitizeMetricName(metricName string) string {
	sanitized := metricSanitizer.ReplaceAllString(metricName, "_")
	return strings.Trim(sanitized, "_")
}
