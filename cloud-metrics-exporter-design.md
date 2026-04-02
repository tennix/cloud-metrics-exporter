# Cloud Metrics Exporter Design Doc

## 1. Overview

### 1.1 Background

Existing Kubernetes monitoring stacks rely on node-exporter for local node metrics such as CPU, memory, and filesystem usage. Cloud-specific metrics such as disk throughput, disk latency, IOPS, and cloud network traffic must be collected from the cloud provider APIs.

Official cloud exporters usually work at account scope, which makes it hard to align cloud metrics with a specific Kubernetes cluster. This project narrows collection to the nodes that actually belong to the current cluster.

### 1.2 Goals

- **Automatic discovery**: discover the current cluster nodes from the Kubernetes API
- **Disk monitoring**: collect cloud disk throughput, latency, and provider-specific IOPS
- **Network monitoring**: collect instance-level cloud network traffic
- **PVC enrichment**: map data-disk metrics to PV / PVC / Pod / workload context
- **Prometheus compatibility**: expose a standard `/metrics` endpoint
- **Future multi-cloud support**: keep a provider abstraction for AWS / Aliyun / Azure / GCP

### 1.3 Current implementation status (2026-04)

The repository currently implements and validates:

- Aliyun-only MVP
- Kubernetes node discovery from `node.spec.providerID`
- ACK-compatible providerID parsing, including `region.instanceId` format
- Aliyun ECS monitor APIs for instance network metrics and disk metrics
- System-disk metrics at node scope
- PVC-backed data-disk metrics enriched with PV / PVC / Pod / workload labels
- Incremental cached disk collection to keep scrape latency bounded
- Fixed `/metrics` endpoint without explicit sample timestamps

---

## 2. Architecture

### 2.1 High-level architecture

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

### 2.2 Node discovery

Node discovery watches Kubernetes nodes and extracts:

- node name
- instance ID
- region
- zone
- internal / external IPs
- provider name

Current Aliyun parsing supports both:

| Format | Example |
|---|---|
| `aliyun:///{region}/{instanceId}` | `aliyun:///cn-hangzhou/i-bp123456` |
| ACK-style `{region}.{instanceId}` | `ap-southeast-1.i-t4n8li1ek5abarylythw` |

The implementation also prefers the stable Kubernetes zone label and falls back to the deprecated beta zone label when needed.

### 2.3 Provider interface

The current implementation uses a reduced provider surface:

```go
type Provider interface {
    Name() string
    Ping(ctx context.Context) error
    CollectDiskMetrics(ctx context.Context, targets []NodeTarget) ([]Sample, error)
    CollectNetworkMetrics(ctx context.Context, targets []NodeTarget) ([]Sample, error)
}
```

VM metrics, LB metrics, and other cloud providers remain future work.

---

## 3. Aliyun implementation

### 3.1 APIs used

The shipped implementation uses Aliyun ECS APIs, not CloudMonitor metric-name lookups:

- `ecs:DescribeDisks`
- `ecs:DescribeDiskMonitorData`
- `ecs:DescribeInstanceMonitorData`

### 3.2 Metric mapping

| Source field | Exposed metric |
|---|---|
| `BPSRead` | `cloud_disk_throughput_read_bytes` |
| `BPSWrite` | `cloud_disk_throughput_write_bytes` |
| `LatencyRead` | `cloud_disk_latency_ms` |
| `LatencyWrite` | `cloud_raw_aliyun_disk_LatencyWrite` |
| `IOPSRead` | `cloud_raw_aliyun_disk_IOPSRead` |
| `IOPSWrite` | `cloud_raw_aliyun_disk_IOPSWrite` |
| `IntranetRX` | `cloud_network_receive_bytes_per_second` |
| `IntranetTX` | `cloud_network_transmit_bytes_per_second` |

### 3.3 Authentication

The current exporter supports:

- **RRSA** (OIDC-based service account role)
- **ACK node role / RAM role** via the default Aliyun credential chain

It does **not** rely on static AccessKey / Secret pairs.

### 3.4 Endpoint handling

ECS clients must be created with explicit regional endpoints, for example:

- `ecs.ap-southeast-1.aliyuncs.com`

### 3.5 Runtime strategy for disk metrics

Disk APIs are more expensive and more likely to be throttled than instance-level network APIs. The current implementation therefore:

- fetches monitor data once per disk refresh
- fans out multiple disk metrics from the same response
- refreshes only a bounded subset of disks per scrape
- returns cached disk samples for previously refreshed disks
- degrades gracefully when throttling or timeout happens

This keeps network metrics responsive even when disk collection is incomplete.

---

## 4. Metric contract

### 4.1 Standard metrics currently emitted

#### Disk

- `cloud_disk_throughput_read_bytes`
- `cloud_disk_throughput_write_bytes`
- `cloud_disk_latency_ms`

#### Network

- `cloud_network_receive_bytes_per_second`
- `cloud_network_transmit_bytes_per_second`

### 4.2 Raw provider-specific metrics

- `cloud_raw_aliyun_disk_IOPSRead`
- `cloud_raw_aliyun_disk_IOPSWrite`
- `cloud_raw_aliyun_disk_LatencyWrite`

### 4.3 Metric semantics

- Counters are only used for truly cumulative values
- Rate / instantaneous values are emitted as gauges
- Queue depth is not mislabeled as latency
- When a provider field does not cleanly match a standard metric, it stays provider-specific

### 4.4 Labels

Common labels:

- `provider`
- `region`
- `instance_id`
- `node`
- `resource_type`

Disk-specific labels:

- `disk_id`
- `disk_role` (`system` or `data`)
- `pv`
- `pvc`
- `namespace`
- `pod`
- `workload`
- `workload_kind`

Raw metrics also include:

- `metric_name`

---

## 5. Disk enrichment model

### 5.1 Disk role split

The implementation follows this rule:

- **system disks** are exported at node scope
- **PVC-backed data disks** are exported at storage / workload scope

### 5.2 Join path

The enrichment flow is:

1. `DescribeDisks` returns `DiskId` and disk `Type`
2. Kubernetes PV lookup matches `PV.spec.csi.volumeHandle == DiskId`
3. PVC lookup matches `PVC.spec.volumeName == PV.name`
4. Pod lookup matches `spec.volumes[].persistentVolumeClaim.claimName == PVC.name`
5. ReplicaSet ownership is used to resolve higher-level workloads where possible

### 5.3 Output rule

- system disk → keep node-level labels only
- matched data disk → emit standard disk metrics with PV/PVC/workload labels
- unmatched data disk → do not emit misleading standard PVC-level metrics

---

## 6. Configuration

Sample runtime config:

```yaml
providers:
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
```

---

## 7. Deployment

### 7.1 Kubernetes deployment

The exporter runs as a single-replica Deployment in `monitoring`.

The current manifest uses:

- `serviceAccountName: cloud-metrics-exporter`
- `imagePullSecrets: [ghcr-pull-secret]`
- image: `ghcr.io/tennix/cloud-metrics-exporter:<short-git-hash>`
- fixed metrics port `9100`

### 7.2 RBAC

Current RBAC covers:

- `nodes`
- `persistentvolumes`
- `persistentvolumeclaims`
- `pods`
- `replicasets`

with `list/watch` permissions.

### 7.3 Prometheus scrape contract

- fixed `/metrics` path
- fixed Service / endpoints target
- no pod annotation overrides required
- no explicit sample timestamps

---

## 8. Phase 1 MVP scope

### Implemented

- Aliyun-only
- Kubernetes node discovery
- system disk + PVC data disk monitoring
- instance-level network monitoring
- Prometheus endpoint
- PVC / workload enrichment
- RRSA or node-role auth

### Not implemented yet

- VM CPU / memory metrics
- LB metrics
- multi-cloud providers
- HA deployment

---

## 9. Operational risks and mitigations

| Risk | Mitigation |
|---|---|
| Aliyun API throttling | one disk API call per refresh, incremental refresh, local cache, bounded disk scrape budget |
| Missing cloud permissions | fail visibly in logs with concrete denied actions |
| Missing monitor datapoints | skip empty disks instead of emitting misleading metrics |
| Scrape timeout | isolate disk collection budget so network metrics remain available |
| ProviderID format drift | support both canonical and ACK-specific Aliyun formats |

---

## 10. Validation status

The implementation has been validated with:

- local Go tests
- clean Go diagnostics
- image build and deployment to ACK
- live verification that network metrics are emitted
- live verification that system-disk metrics are emitted
- live verification that PVC-backed data-disk metrics include PV/PVC/pod/workload labels

---

## 11. References

- Alibaba Cloud ECS API docs
- Kubernetes PV / PVC / Pod APIs
- Prometheus exposition format

---

*Author: tennix*  
*Date: 2026-04-02*
