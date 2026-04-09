# Cloud Metrics Exporter Design

## Overview

Cloud Metrics Exporter fills the gap between Kubernetes-native node metrics and cloud-provider metrics.
It discovers the nodes that belong to the current cluster, collects cloud metrics for those nodes from provider APIs, and exposes the results on `/metrics` for Prometheus.

The current implementation is an Aliyun-focused MVP. It is designed around cluster-scoped discovery, Prometheus-compatible output, and label enrichment that makes cloud disk metrics easier to map back to Kubernetes workloads.

## Goals

- Discover cluster nodes from the Kubernetes API.
- Collect Aliyun disk and instance network metrics for those nodes.
- Enrich PVC-backed data-disk metrics with PV, PVC, Pod, and workload labels.
- Expose a standard Prometheus scrape endpoint.
- Keep the provider boundary small enough to support future clouds later.

## Current Scope

Implemented today:

- Aliyun provider only.
- Node discovery from `node.spec.providerID`.
- ACK-compatible provider ID parsing, including `{region}.{instanceId}`.
- Aliyun ECS disk and instance network collection.
- System-disk metrics at node scope.
- PVC-backed data-disk metrics with Kubernetes context labels.
- Incremental cached disk refresh to keep scrape latency bounded.

Not implemented yet:

- CPU and memory cloud metrics.
- Load balancer metrics.
- Additional cloud providers.
- High-availability deployment.

## Architecture

```text
Kubernetes API (nodes / PV / PVC / pods / ReplicaSets)
        |
        v
Node Discovery + Volume Enricher
        |
        v
Aliyun Provider
  - DescribeDisks
  - DescribeDiskMonitorData
  - DescribeInstanceMonitorData
        |
        v
Prometheus Collector -> /metrics
```

The exporter is organized into a few small pieces:

- `internal/discovery`: discovers nodes and resolves disk-to-Kubernetes relationships.
- `internal/provider/aliyun`: talks to Aliyun ECS APIs.
- `internal/exporter`: implements the Prometheus collector.
- `internal/metrics`: defines metric names and raw fallback behavior.

## Discovery Model

Node discovery reads Kubernetes nodes and extracts:

- node name
- instance ID
- region
- zone
- internal and external IPs
- provider name

Aliyun parsing currently supports both of these provider ID formats:

| Format | Example |
|---|---|
| `aliyun:///{region}/{instanceId}` | `aliyun:///cn-hangzhou/i-bp123456` |
| `{region}.{instanceId}` | `ap-southeast-1.i-t4n8li1ek5abarylythw` |

Zone resolution prefers the stable Kubernetes topology label and falls back to the deprecated beta label when needed.

## Provider Boundary

The current provider surface is intentionally narrow:

```go
type Provider interface {
    Name() string
    Ping(ctx context.Context) error
    CollectDiskMetrics(ctx context.Context, targets []NodeTarget) ([]Sample, error)
    CollectNetworkMetrics(ctx context.Context, targets []NodeTarget) ([]Sample, error)
}
```

This keeps the shipped implementation focused on the metrics the exporter actually emits today.

## Aliyun Collection Strategy

The current implementation uses these ECS APIs:

- `DescribeDisks`
- `DescribeDiskMonitorData`
- `DescribeInstanceMonitorData`

Authentication is based on RRSA or the default ACK node-role credential chain. The exporter does not depend on static AccessKey and Secret pairs.

Regional ECS endpoints must be used explicitly, for example `ecs.ap-southeast-1.aliyuncs.com`.

Disk collection is the most expensive part of a scrape, so the exporter uses an incremental refresh strategy:

- fetch monitor data once per disk refresh
- derive multiple disk metrics from the same response
- refresh only a bounded subset of disks per scrape
- return cached disk samples for disks not refreshed in the current scrape
- degrade gracefully on timeout or throttling

This keeps network metrics available even when disk collection is partial.

## Metric Model

Standard metrics currently emitted:

- `cloud_disk_throughput_read_bytes`
- `cloud_disk_throughput_write_bytes`
- `cloud_disk_latency_ms`
- `cloud_network_receive_bytes_per_second`
- `cloud_network_transmit_bytes_per_second`

Raw provider-specific metrics are emitted when a source field does not map cleanly to the standard set, including:

- `cloud_raw_aliyun_disk_IOPSRead`
- `cloud_raw_aliyun_disk_IOPSWrite`
- `cloud_raw_aliyun_disk_LatencyWrite`

Common labels:

- `provider`
- `region`
- `instance_id`
- `node`
- `resource_type`

Disk metrics may also include:

- `disk_id`
- `disk_role`
- `pv`
- `pvc`
- `namespace`
- `pod`
- `workload`
- `workload_kind`

Raw metrics also include `metric_name`.

## Disk Enrichment Rules

The exporter treats disks in two groups:

- system disks stay at node scope
- PVC-backed data disks are enriched with storage and workload labels

The join path is:

1. `DescribeDisks` returns `DiskId` and disk type.
2. PV lookup matches `PV.spec.csi.volumeHandle == DiskId`.
3. PVC lookup matches `PVC.spec.volumeName == PV.name`.
4. Pod lookup matches `spec.volumes[].persistentVolumeClaim.claimName == PVC.name`.
5. ReplicaSet ownership is used to resolve a higher-level workload when possible.

If a data disk cannot be matched back to Kubernetes objects, the exporter avoids emitting misleading PVC-scoped standard metrics.

## Runtime and Deployment Assumptions

Sample config is kept in `configs/config.yaml` and includes provider selection, enabled metric types, and refresh intervals.

The current deployment model is a single-replica Deployment exposing port `9100` with a fixed `/metrics` endpoint.

RBAC currently covers read access for:

- `nodes`
- `persistentvolumes`
- `persistentvolumeclaims`
- `pods`
- `replicasets`

## Operational Risks

| Risk | Mitigation |
|---|---|
| Aliyun API throttling | bounded disk refresh, local cache, one monitor fetch per disk refresh |
| Missing cloud permissions | fail visibly in logs with the denied action context |
| Missing monitor datapoints | skip incomplete disk samples rather than emit misleading metrics |
| Scrape timeout | isolate disk work so network metrics remain available |
| Provider ID format drift | support both canonical and ACK-specific Aliyun formats |
