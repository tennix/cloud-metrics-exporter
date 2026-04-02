package aliyun

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	ecs "github.com/alibabacloud-go/ecs-20140526/v4/client"
	"github.com/alibabacloud-go/tea/tea"
	"github.com/aliyun/credentials-go/credentials"
	"github.com/cloud-metrics-exporter/cloud-metrics-exporter/internal/discovery"
	projectmetrics "github.com/cloud-metrics-exporter/cloud-metrics-exporter/internal/metrics"
)

type fakeCredential struct{ err error }

func (f fakeCredential) GetCredential() (*credentials.CredentialModel, error) {
	if f.err != nil {
		return nil, f.err
	}
	accessKeyID := "ak"
	accessKeySecret := "secret"
	typeName := "oidc_role_arn"
	return &credentials.CredentialModel{AccessKeyId: &accessKeyID, AccessKeySecret: &accessKeySecret, Type: &typeName}, nil
}
func (f fakeCredential) GetAccessKeyId() (*string, error)     { v := "ak"; return &v, nil }
func (f fakeCredential) GetAccessKeySecret() (*string, error) { v := "secret"; return &v, nil }
func (f fakeCredential) GetSecurityToken() (*string, error)   { v := "token"; return &v, nil }
func (f fakeCredential) GetBearerToken() *string              { v := ""; return &v }
func (f fakeCredential) GetType() *string                     { v := "fake"; return &v }

type unsupportedCredential struct{}

func (unsupportedCredential) GetCredential() (*credentials.CredentialModel, error) {
	accessKeyID := "ak"
	accessKeySecret := "secret"
	typeName := "access_key"
	return &credentials.CredentialModel{AccessKeyId: &accessKeyID, AccessKeySecret: &accessKeySecret, Type: &typeName}, nil
}
func (unsupportedCredential) GetAccessKeyId() (*string, error)     { v := "ak"; return &v, nil }
func (unsupportedCredential) GetAccessKeySecret() (*string, error) { v := "secret"; return &v, nil }
func (unsupportedCredential) GetSecurityToken() (*string, error)   { v := ""; return &v, nil }
func (unsupportedCredential) GetBearerToken() *string              { v := ""; return &v }
func (unsupportedCredential) GetType() *string                     { v := "access_key"; return &v }

type fakeECSClient struct {
	disks         []*ecs.DescribeDisksResponseBodyDisksDisk
	diskMonitor   map[string]*ecs.DescribeDiskMonitorDataResponseBodyMonitorDataDiskMonitorData
	instancePoint *ecs.DescribeInstanceMonitorDataResponseBodyMonitorDataInstanceMonitorData
	err           error
	diskCalls     map[string]int
}

func (f fakeECSClient) DescribeDisks(_ *ecs.DescribeDisksRequest) (*ecs.DescribeDisksResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &ecs.DescribeDisksResponse{Body: &ecs.DescribeDisksResponseBody{Disks: &ecs.DescribeDisksResponseBodyDisks{Disk: f.disks}}}, nil
}

func (f fakeECSClient) DescribeDiskMonitorData(request *ecs.DescribeDiskMonitorDataRequest) (*ecs.DescribeDiskMonitorDataResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.diskCalls != nil {
		f.diskCalls[tea.StringValue(request.DiskId)]++
	}
	point := f.diskMonitor[tea.StringValue(request.DiskId)]
	items := []*ecs.DescribeDiskMonitorDataResponseBodyMonitorDataDiskMonitorData{}
	if point != nil {
		items = append(items, point)
	}
	return &ecs.DescribeDiskMonitorDataResponse{Body: &ecs.DescribeDiskMonitorDataResponseBody{MonitorData: &ecs.DescribeDiskMonitorDataResponseBodyMonitorData{DiskMonitorData: items}}}, nil
}

func (f fakeECSClient) DescribeInstanceMonitorData(_ *ecs.DescribeInstanceMonitorDataRequest) (*ecs.DescribeInstanceMonitorDataResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	items := []*ecs.DescribeInstanceMonitorDataResponseBodyMonitorDataInstanceMonitorData{}
	if f.instancePoint != nil {
		items = append(items, f.instancePoint)
	}
	return &ecs.DescribeInstanceMonitorDataResponse{Body: &ecs.DescribeInstanceMonitorDataResponseBody{MonitorData: &ecs.DescribeInstanceMonitorDataResponseBodyMonitorData{InstanceMonitorData: items}}}, nil
}

type fakeDiskEnricher struct {
	items map[string]discovery.DiskEnrichment
}

func (f fakeDiskEnricher) LookupByDiskID(diskID string) discovery.DiskEnrichment {
	if item, ok := f.items[diskID]; ok {
		return item
	}
	return discovery.DiskEnrichment{}
}

func TestPing(t *testing.T) {
	p := &Provider{credential: fakeCredential{}}
	if err := p.Ping(context.Background()); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
}

func TestPingCredentialFailure(t *testing.T) {
	p := &Provider{credential: fakeCredential{err: errors.New("boom")}}
	if err := p.Ping(context.Background()); err == nil {
		t.Fatal("Ping() error = nil, want error")
	}
}

func TestPingRejectsUnsupportedCredentialType(t *testing.T) {
	p := &Provider{credential: unsupportedCredential{}}
	if err := p.Ping(context.Background()); !errors.Is(err, ErrUnsupportedCredentialType) {
		t.Fatalf("Ping() error = %v, want %v", err, ErrUnsupportedCredentialType)
	}
}

func TestValidateCredentialTypeAllowsNodeRole(t *testing.T) {
	t.Parallel()
	if err := validateCredentialType("ecs_ram_role"); err != nil {
		t.Fatalf("validateCredentialType() error = %v", err)
	}
}

func TestValidateCredentialTypeAllowsDefaultChain(t *testing.T) {
	t.Parallel()
	if err := validateCredentialType("default"); err != nil {
		t.Fatalf("validateCredentialType() error = %v", err)
	}
}

func TestCollectNetworkMetrics(t *testing.T) {
	t.Parallel()

	p := &Provider{
		credential:         fakeCredential{},
		period:             time.Minute,
		now:                func() time.Time { return time.Unix(1710000000, 0).UTC() },
		metricsByType:      map[string][]metricQuery{"network": {{MetricName: "IntranetRX", Resource: "network", Standard: projectmetrics.NetworkReceiveBytesPerSecond, UnitScale: 1000.0 / 8.0 / 60.0}}},
		ecsClientsByRegion: map[string]ecsAPI{},
	}
	p.ecsClientFactory = func(region string) (ecsAPI, error) {
		return fakeECSClient{instancePoint: &ecs.DescribeInstanceMonitorDataResponseBodyMonitorDataInstanceMonitorData{IntranetRX: tea.Int32(480)}}, nil
	}

	samples, err := p.CollectNetworkMetrics(context.Background(), []discovery.NodeTarget{{NodeName: "node-a", InstanceID: "i-bp123", Region: "cn-hangzhou", Provider: "aliyun"}})
	if err != nil {
		t.Fatalf("CollectNetworkMetrics() error = %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("CollectNetworkMetrics() len = %d, want 1", len(samples))
	}
	if samples[0].Name != projectmetrics.NetworkReceiveBytesPerSecond {
		t.Fatalf("sample.Name = %q", samples[0].Name)
	}
	if math.Abs(samples[0].Value-1000) > 0.0001 {
		t.Fatalf("sample.Value = %v, want 1000", samples[0].Value)
	}
}

func TestCollectDiskMetricsFallsBackToRawForIOPS(t *testing.T) {
	t.Parallel()

	p := &Provider{
		credential:         fakeCredential{},
		period:             time.Minute,
		now:                func() time.Time { return time.Unix(1710000000, 0).UTC() },
		metricsByType:      map[string][]metricQuery{"disk": {{MetricName: "IOPSRead", Resource: "disk", RawOnly: true, UnitScale: 1}}},
		ecsClientsByRegion: map[string]ecsAPI{},
	}
	p.ecsClientFactory = func(region string) (ecsAPI, error) {
		return fakeECSClient{
			disks:       []*ecs.DescribeDisksResponseBodyDisksDisk{{DiskId: tea.String("d-system"), Type: tea.String("system")}},
			diskMonitor: map[string]*ecs.DescribeDiskMonitorDataResponseBodyMonitorDataDiskMonitorData{"d-system": {IOPSRead: tea.Int32(2400)}},
		}, nil
	}

	samples, err := p.CollectDiskMetrics(context.Background(), []discovery.NodeTarget{{NodeName: "node-a", InstanceID: "i-bp123", Region: "cn-hangzhou", Provider: "aliyun"}})
	if err != nil {
		t.Fatalf("CollectDiskMetrics() error = %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("CollectDiskMetrics() len = %d, want 1", len(samples))
	}
	if samples[0].Name != "cloud_raw_aliyun_disk_IOPSRead" {
		t.Fatalf("sample.Name = %q", samples[0].Name)
	}
}

func TestCollectDiskMetricsWritesLatencyAsRaw(t *testing.T) {
	t.Parallel()

	p := &Provider{
		credential:         fakeCredential{},
		period:             time.Minute,
		now:                func() time.Time { return time.Unix(1710000000, 0).UTC() },
		metricsByType:      map[string][]metricQuery{"disk": {{MetricName: "LatencyWrite", Resource: "disk", RawOnly: true, UnitScale: 1.0 / 1000.0}}},
		ecsClientsByRegion: map[string]ecsAPI{},
	}
	p.ecsClientFactory = func(region string) (ecsAPI, error) {
		return fakeECSClient{
			disks:       []*ecs.DescribeDisksResponseBodyDisksDisk{{DiskId: tea.String("d-system"), Type: tea.String("system")}},
			diskMonitor: map[string]*ecs.DescribeDiskMonitorDataResponseBodyMonitorDataDiskMonitorData{"d-system": {LatencyWrite: tea.Int32(12000)}},
		}, nil
	}

	samples, err := p.CollectDiskMetrics(context.Background(), []discovery.NodeTarget{{NodeName: "node-a", InstanceID: "i-bp123", Region: "cn-hangzhou", Provider: "aliyun"}})
	if err != nil {
		t.Fatalf("CollectDiskMetrics() error = %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("CollectDiskMetrics() len = %d, want 1", len(samples))
	}
	if samples[0].Name != "cloud_raw_aliyun_disk_LatencyWrite" {
		t.Fatalf("sample.Name = %q", samples[0].Name)
	}
	if samples[0].Value != 12 {
		t.Fatalf("sample.Value = %v, want 12", samples[0].Value)
	}
}

func TestCollectDiskMetricsSkipsUnmatchedDataForStandardMetrics(t *testing.T) {
	t.Parallel()

	p := &Provider{
		credential:         fakeCredential{},
		period:             time.Minute,
		now:                func() time.Time { return time.Unix(1710000000, 0).UTC() },
		metricsByType:      map[string][]metricQuery{"disk": {{MetricName: "LatencyRead", Resource: "disk", Standard: projectmetrics.DiskLatencyMS, UnitScale: 1.0 / 1000.0}}},
		ecsClientsByRegion: map[string]ecsAPI{},
	}
	p.ecsClientFactory = func(region string) (ecsAPI, error) {
		return fakeECSClient{
			disks:       []*ecs.DescribeDisksResponseBodyDisksDisk{{DiskId: tea.String("d-data"), Type: tea.String("data")}},
			diskMonitor: map[string]*ecs.DescribeDiskMonitorDataResponseBodyMonitorDataDiskMonitorData{"d-data": {LatencyRead: tea.Int32(8000)}},
		}, nil
	}

	samples, err := p.CollectDiskMetrics(context.Background(), []discovery.NodeTarget{{NodeName: "node-a", InstanceID: "i-bp123", Region: "cn-hangzhou", Provider: "aliyun"}})
	if err != nil {
		t.Fatalf("CollectDiskMetrics() error = %v", err)
	}
	if len(samples) != 0 {
		t.Fatalf("CollectDiskMetrics() len = %d, want 0", len(samples))
	}
}

func TestCollectDiskMetricsEnrichesPVCBackedDataDisk(t *testing.T) {
	t.Parallel()

	p := &Provider{
		credential:         fakeCredential{},
		period:             time.Minute,
		now:                func() time.Time { return time.Unix(1710000000, 0).UTC() },
		metricsByType:      map[string][]metricQuery{"disk": {{MetricName: "LatencyRead", Resource: "disk", Standard: projectmetrics.DiskLatencyMS, UnitScale: 1.0 / 1000.0}}},
		ecsClientsByRegion: map[string]ecsAPI{},
		enricher:           fakeDiskEnricher{items: map[string]discovery.DiskEnrichment{"d-data": {Matched: true, PV: "pv-a", PVC: "pvc-a", Namespace: "app", Pod: "app-0", Workload: "web", WorkloadKind: "Deployment"}}},
	}
	p.ecsClientFactory = func(region string) (ecsAPI, error) {
		return fakeECSClient{
			disks:       []*ecs.DescribeDisksResponseBodyDisksDisk{{DiskId: tea.String("d-data"), Type: tea.String("data")}},
			diskMonitor: map[string]*ecs.DescribeDiskMonitorDataResponseBodyMonitorDataDiskMonitorData{"d-data": {LatencyRead: tea.Int32(8000)}},
		}, nil
	}

	samples, err := p.CollectDiskMetrics(context.Background(), []discovery.NodeTarget{{NodeName: "node-a", InstanceID: "i-bp123", Region: "cn-hangzhou", Provider: "aliyun"}})
	if err != nil {
		t.Fatalf("CollectDiskMetrics() error = %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("CollectDiskMetrics() len = %d, want 1", len(samples))
	}
	if samples[0].Labels["disk_role"] != "data" || samples[0].Labels["pv"] != "pv-a" || samples[0].Labels["workload_kind"] != "Deployment" {
		t.Fatalf("unexpected enrichment labels = %+v", samples[0].Labels)
	}
	if samples[0].Value != 8 {
		t.Fatalf("sample.Value = %v, want 8", samples[0].Value)
	}
}

func TestCollectDiskMetricsCallsMonitorOncePerDisk(t *testing.T) {
	t.Parallel()

	counts := map[string]int{}
	client := fakeECSClient{
		disks: []*ecs.DescribeDisksResponseBodyDisksDisk{{DiskId: tea.String("d-system"), Type: tea.String("system")}},
		diskMonitor: map[string]*ecs.DescribeDiskMonitorDataResponseBodyMonitorDataDiskMonitorData{
			"d-system": {BPSRead: tea.Int32(100), BPSWrite: tea.Int32(200), LatencyRead: tea.Int32(3000), LatencyWrite: tea.Int32(4000), IOPSRead: tea.Int32(10), IOPSWrite: tea.Int32(20)},
		},
		diskCalls: counts,
	}

	p := &Provider{
		credential: fakeCredential{},
		period:     time.Minute,
		now:        func() time.Time { return time.Unix(1710000000, 0).UTC() },
		metricsByType: map[string][]metricQuery{
			"disk": {
				{MetricName: "BPSRead", Resource: "disk", Standard: projectmetrics.DiskThroughputReadBytes, UnitScale: 1},
				{MetricName: "BPSWrite", Resource: "disk", Standard: projectmetrics.DiskThroughputWriteBytes, UnitScale: 1},
				{MetricName: "LatencyRead", Resource: "disk", Standard: projectmetrics.DiskLatencyMS, UnitScale: 1.0 / 1000.0},
				{MetricName: "LatencyWrite", Resource: "disk", RawOnly: true, UnitScale: 1.0 / 1000.0},
				{MetricName: "IOPSRead", Resource: "disk", RawOnly: true, UnitScale: 1},
				{MetricName: "IOPSWrite", Resource: "disk", RawOnly: true, UnitScale: 1},
			},
		},
		ecsClientsByRegion: map[string]ecsAPI{},
	}
	p.ecsClientFactory = func(region string) (ecsAPI, error) { return client, nil }

	samples, err := p.CollectDiskMetrics(context.Background(), []discovery.NodeTarget{{NodeName: "node-a", InstanceID: "i-bp123", Region: "cn-hangzhou", Provider: "aliyun"}})
	if err != nil {
		t.Fatalf("CollectDiskMetrics() error = %v", err)
	}
	if len(samples) != 6 {
		t.Fatalf("CollectDiskMetrics() len = %d, want 6", len(samples))
	}
	if counts["d-system"] != 1 {
		t.Fatalf("DescribeDiskMonitorData() calls = %d, want 1", counts["d-system"])
	}
}

func TestCollectDiskMetricsReturnsPartialOnThrottle(t *testing.T) {
	t.Parallel()

	client := fakeECSClient{
		disks: []*ecs.DescribeDisksResponseBodyDisksDisk{
			{DiskId: tea.String("d-ok"), Type: tea.String("system")},
			{DiskId: tea.String("d-throttle"), Type: tea.String("system")},
		},
		diskMonitor: map[string]*ecs.DescribeDiskMonitorDataResponseBodyMonitorDataDiskMonitorData{
			"d-ok": {BPSRead: tea.Int32(100)},
		},
		diskCalls: map[string]int{},
	}

	p := &Provider{
		credential:         fakeCredential{},
		period:             time.Minute,
		now:                func() time.Time { return time.Unix(1710000000, 0).UTC() },
		metricsByType:      map[string][]metricQuery{"disk": {{MetricName: "BPSRead", Resource: "disk", Standard: projectmetrics.DiskThroughputReadBytes, UnitScale: 1}}},
		ecsClientsByRegion: map[string]ecsAPI{},
	}
	p.ecsClientFactory = func(region string) (ecsAPI, error) {
		return throttleDiskClient{base: client}, nil
	}

	samples, err := p.CollectDiskMetrics(context.Background(), []discovery.NodeTarget{{NodeName: "node-a", InstanceID: "i-bp123", Region: "cn-hangzhou", Provider: "aliyun"}})
	if err != nil {
		t.Fatalf("CollectDiskMetrics() error = %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("CollectDiskMetrics() len = %d, want 1", len(samples))
	}
}

type throttleDiskClient struct{ base fakeECSClient }

func (t throttleDiskClient) DescribeDisks(r *ecs.DescribeDisksRequest) (*ecs.DescribeDisksResponse, error) {
	return t.base.DescribeDisks(r)
}
func (t throttleDiskClient) DescribeInstanceMonitorData(r *ecs.DescribeInstanceMonitorDataRequest) (*ecs.DescribeInstanceMonitorDataResponse, error) {
	return t.base.DescribeInstanceMonitorData(r)
}
func (t throttleDiskClient) DescribeDiskMonitorData(r *ecs.DescribeDiskMonitorDataRequest) (*ecs.DescribeDiskMonitorDataResponse, error) {
	if tea.StringValue(r.DiskId) == "d-throttle" {
		return nil, errors.New("Code: Throttling: Request was denied due to request throttling")
	}
	return t.base.DescribeDiskMonitorData(r)
}
