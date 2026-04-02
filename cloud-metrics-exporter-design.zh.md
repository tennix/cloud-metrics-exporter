# Cloud Metrics Exporter 设计文档

## 1. 概述

### 1.1 项目背景

现有 K8s 集群的监控方案依赖 node-exporter 采集节点本地指标（CPU、内存、磁盘空间），但云盘 IOPS/吞吐/延迟、网卡流量、负载均衡器监控等指标需要调用云厂商 API 获取。

现有云厂商官方 exporter（如阿里云 Cloud Monitor Exporter）默认采集范围为账号下所有节点，无法与特定 K8s 集群的节点一一对应，导致数据关联困难。

### 1.2 项目目标

- **自动发现**：从 K8s API 获取当前集群节点，与 node-exporter 监控目标一致
- **云盘监控**：采集 ESSD/Cloud Disk 的 IOPS、吞吐量、读写延迟
- **网卡监控**：采集 ENI 的入方向/出方向流量、带宽
- **PVC 关联**：将数据盘监控指标关联到 PV / PVC / Pod / Workload 维度
- **LB 监控**：作为后续阶段扩展，采集 K8s Service (type=LoadBalancer) 对应的云上 LB 监控指标
- **多云支持**：抽象统一接口，支持 AWS / 阿里云 / Azure / GCP

### 1.4 当前实现状态（2026-04）

当前仓库已实现并验证：

- 阿里云单云 MVP
- 从 K8s `node.spec.providerID` 发现 ACK 节点，并兼容 ACK 实际 providerID 形式 `region.instanceId`
- 使用 ECS API 采集实例网络指标：`DescribeInstanceMonitorData`
- 使用 ECS API 采集云盘信息与监控数据：`DescribeDisks` + `DescribeDiskMonitorData`
- 系统盘按节点维度输出，PVC 数据盘按 `pv/pvc/pod/workload` 维度输出
- 为避免 scrape 超时，对云盘指标采用**增量刷新 + 本地缓存**策略
- Prometheus 通过固定 `/metrics` 抓取，样本不携带显式时间戳

### 1.3 核心设计原则

- **Deployment 部署**：少量副本即可覆盖全量节点，不需 DaemonSet
- **Prometheus 兼容**：输出标准 `/metrics` 端点，直接对接 Prometheus/Grafana
- **松耦合架构**：云厂商 SDK 抽象为插件式 provider，便于扩展
- **节点过滤**：仅采集当前 K8s 集群的节点，避免数据噪音

---

## 2. 架构设计

### 2.1 整体架构

```
┌─────────────────────────────────────────────────────────────────┐
│                        Cloud Metrics Exporter                    │
│                                                                  │
│  ┌──────────────┐    ┌─────────────────────┐    ┌─────────────┐ │
│  │ K8s Node     │    │  Provider Registry   │    │ Prometheus │ │
│  │ Discovery    │───▶│  (multi-cloud)       │───▶│ /metrics   │ │
│  └──────────────┘    └──────────┬──────────┘    └─────────────┘ │
│                                │                               │
│         ┌──────────────────────┼──────────────────────┐        │
│         ▼                      ▼                      ▼        │
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────┐          │
│  │ AWS Provider │   │ Aliyun       │    │ GCP/Azure   │          │
│  │ (CloudWatch)│   │ Provider     │    │ Provider    │          │
│  └─────────────┘    └─────────────┘    └─────────────┘          │
│                                                                  │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │ 监控类别：                                                   │ │
│  │  - VM: CPU/内存/网络 (可选，与 node-exporter 互补)           │ │
│  │  - Disk: 云盘 IOPS/吞吐/延迟                                │ │
│  │  - ENI: 网卡流量/带宽                                       │ │
│  │  - LB: 负载均衡器 QPS/流量/连接数/健康检查                    │ │
│  └────────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────┘
```

### 2.2 组件设计

#### 2.2.1 Node Discovery

**职责**：从 K8s API 获取当前集群节点列表，解析 `.status.addresses` 获取内网 IP 和外部 IP，映射到云厂商 InstanceId。

**输入**：无（监听集群内所有节点变化）
**输出**：节点列表 `[]NodeTarget`

```go
type NodeTarget struct {
    NodeName    string            // k8s node name
    InternalIP  string            // pod CIDR 内网 IP
    ExternalIP  string            // 外部 IP (若有)
    InstanceID  string            // 云厂商实例 ID (从 node.spec.providerID 解析)
    Region      string
    Zone        string
    Provider    string            // "aws" | "aliyun" | "azure" | "gcp"
}
```

**实现方式**：
- List/Watch `nodes` 资源，缓存本地
- 从 `node.spec.providerID` 直接解析出 provider/region/instanceId
- 支持 Reload 信号（配置变更时刷新）

**providerID 格式解析**：

| 云厂商 | 格式 | 示例 |
|-------|------|------|
| AWS | `aws:///{region}/{instanceId}` | `aws:///us-west-2/i-0abc123` |
| 阿里云 | `aliyun:///{region}/{instanceId}` | `aliyun:///cn-hangzhou/i-bp123456` |
| 阿里云 (ACK) | `{region}.{instanceId}` | `ap-southeast-1.i-t4n8li1ek5abarylythw` |
| Azure | `azure:///subscriptions/{sub}/resourceGroups/{rg}/providers/...` | `azure:///subscriptions/xxx/...` |
| GCP | `gce:///{project}/{zone}/{instanceName}` | `gce:///my-project/us-central1-a/gke-node` |

**解析代码示例**：

```go
func parseProviderID(providerID string) (provider, region, instanceID string) {
    if providerID == "" {
        return "", "", ""
    }
    parts := strings.SplitN(providerID, "://", 2)
    if len(parts) != 2 {
        return "", "", ""
    }
    provider = parts[0]
    path := strings.TrimPrefix(parts[1], "/")
    segs := strings.Split(path, "/")

    switch provider {
    case "aws":
        // path: region/instanceId
        if len(segs) >= 2 {
            region = segs[0]
            instanceID = segs[1]
        }
    case "aliyun":
        // path: region/instanceId
        if len(segs) >= 2 {
            region = segs[0]
            instanceID = segs[1]
        }
    case "azure":
        // path: subscriptions/xxx/resourceGroups/yyy/...
        for i, seg := range segs {
            if seg == "resourceGroups" && i+1 < len(segs) {
                // 可提取 resourceGroup
            }
        }
        instanceID = providerID // Azure 用完整 ARN
    case "gce":
        // path: project/zone/instanceName
        if len(segs) >= 3 {
            region = segs[1] // zone 作为 region
            instanceID = segs[2]
        }
    }
    return
}
```

**优势**：
- ✅ 无需遍历节点做 API 查询
- ✅ 直接从 node 对象获取，零额外 API 调用
- ✅ 天然解决跨 VPC IP 冲突问题
- ✅ 支持跨可用区迁移（providerID 不变）

#### 2.2.2 Service Discovery (LB)

**职责**：遍历 K8s Services，识别 `type=LoadBalancer` 的 Service，获取关联的云上 LB ARN/ID。

```go
type LoadBalancerTarget struct {
    ServiceName      string  // k8s service name
    ServiceNamespace string  // k8s namespace
    LoadBalancerID   string  // 云厂商 LB ID/ARN
    LBType           string  // "classic" | "application" | "network"
    Provider         string
    Region           string
}
```

**映射关系**：
```
K8s Service (type=LoadBalancer)
  └── annotation: cloud.google.com/load-balancer-type (GCP)
  └── annotation: service.beta.kubernetes.io/aws-load-balancer-type (AWS)
  └── .status.loadBalancer.ingress[].hostname / ip
```

#### 2.2.3 Provider Interface

所有云厂商 SDK 统一实现 `Provider` 接口：

```go
type Provider interface {
    // Meta
    Name() string  // "aws" | "aliyun" | "azure" | "gcp"

    // VM / Node metrics
    CollectVMMetrics(ctx context.Context, targets []NodeTarget, ch chan<- *metrics.Metric) error

    // Disk metrics (云盘)
    CollectDiskMetrics(ctx context.Context, targets []NodeTarget, ch chan<- *metrics.Metric) error

    // ENI / Network metrics
    CollectNetworkMetrics(ctx context.Context, targets []NodeTarget, ch chan<- *metrics.Metric) error

    // LoadBalancer metrics
    CollectLBMetrics(ctx context.Context, targets []LoadBalancerTarget, ch chan<- *metrics.Metric) error

    // Health check
    Ping(ctx context.Context) error
}
```

#### 2.2.4 Metric Registry

指标注册表，定义标准化的指标名称和维度：

> **MVP 决策**：v0.1 对外暴露的标准指标名称以本节定义为准，不采用第 6 节中另一套候选命名。

```go
// 指标前缀: cloud_<category>_
type MetricName string

const (
    // Disk
    MetricDiskIOReadTotal   = "cloud_disk_io_read_total"
    MetricDiskIOWriteTotal  = "cloud_disk_io_write_total"
    MetricDiskThroughputRead  = "cloud_disk_throughput_read_bytes"
    MetricDiskThroughputWrite = "cloud_disk_throughput_write_bytes"
    MetricDiskLatency         = "cloud_disk_latency_ms"
    MetricDiskQueueDepth      = "cloud_disk_queue_depth"

    // Network
    MetricNetRxBytes       = "cloud_network_receive_bytes_total"
    MetricNetTxBytes       = "cloud_network_transmit_bytes_total"
    MetricNetRxBytesRate   = "cloud_network_receive_bytes_per_second"
    MetricNetTxBytesRate   = "cloud_network_transmit_bytes_per_second"
    MetricNetRxPackets     = "cloud_network_receive_packets_total"
    MetricNetTxPackets     = "cloud_network_transmit_packets_total"

    // LoadBalancer
    MetricLBRequestCount       = "cloud_lb_requests_total"
    MetricLBActiveConnections = "cloud_lb_active_connections"
    MetricLBTrafficRx         = "cloud_lb_network_rx_bytes"
    MetricLBTrafficTx         = "cloud_lb_network_tx_bytes"
    MetricLBHealthyHosts      = "cloud_lb_healthy_hosts"
    MetricLBUnhealthyHosts    = "cloud_lb_unhealthy_hosts"
)
```

**标准 Labels**：
- `node` / `instance_id` / `instance_name`
- `device` / `interface` (disk device 或网卡名)
- `region` / `zone`
- `service` / `namespace` (LB 专属)
- `lb_id`

---

## 3. 云厂商实现详情

### 3.1 阿里云 (Aliyun)

**当前实现 API**：

- `ecs:DescribeDisks`
- `ecs:DescribeDiskMonitorData`
- `ecs:DescribeInstanceMonitorData`

**对应指标**：

| 类别 | ECS Monitor 字段 | 映射到 |
|-----|------------------|--------|
| 云盘读吞吐 | `BPSRead` | `cloud_disk_throughput_read_bytes` |
| 云盘写吞吐 | `BPSWrite` | `cloud_disk_throughput_write_bytes` |
| 云盘读延迟 | `LatencyRead` | `cloud_disk_latency_ms` |
| 云盘写延迟 | `LatencyWrite` | `cloud_raw_aliyun_disk_LatencyWrite` |
| 云盘读 IOPS | `IOPSRead` | `cloud_raw_aliyun_disk_IOPSRead` |
| 云盘写 IOPS | `IOPSWrite` | `cloud_raw_aliyun_disk_IOPSWrite` |
| 实例内网流入 | `IntranetRX` | `cloud_network_receive_bytes_per_second` |
| 实例内网流出 | `IntranetTX` | `cloud_network_transmit_bytes_per_second` |

**认证**：支持 RRSA 或 ACK 节点 RAM Role（node-role/default credential chain），MVP 不使用静态 AK/SK。

**实现说明**：

- ECS 客户端需要显式指定区域 endpoint，例如 `ecs.ap-southeast-1.aliyuncs.com`
- 云盘采集使用单盘单次查询；同一次返回中扇出多个 disk 指标，避免 `磁盘数 × 指标数` 的请求膨胀
- 对于 ACK 中返回无监控点的磁盘，不输出标准 disk 指标

### 3.2 AWS

**API**：`CloudWatch.GetMetricData` / `GetMetricStatistics`

**资源**：
- EBS: `AWS/EBS` (VolumeId)
- ENI: `AWS/EC2` (InstanceId, DeviceId)
- ELB: `AWS/ELB` / `AWS/ApplicationELB` / `AWS/NetworkELB`

**指标示例**：

| 类别 | CloudWatch Metric | 映射到 |
|-----|------------------|--------|
| EBS ReadOps | VolumeReadOps | `cloud_disk_io_read_total` |
| EBS WriteOps | VolumeWriteOps | `cloud_disk_io_write_total` |
| EBS ReadBytes | VolumeReadBytes | `cloud_disk_throughput_read_bytes` |
| EBS WriteBytes | VolumeWriteBytes | `cloud_disk_throughput_write_bytes` |
| EBS QueueLength | VolumeQueueLength | `cloud_disk_queue_depth` |
| ELB RequestCount | RequestCount | `cloud_lb_requests_total` |
| ELB ActiveFlowCount | ActiveFlowCount | `cloud_lb_active_connections` |

**认证**：IRSA (IAM Role for Service Account)

### 3.3 Azure

**API**：Azure Monitor REST API (`/subscriptions/{}/providers/microsoft.insights/metricDefinitions`)

**资源**：
- Managed Disk: `Microsoft.Compute/disks`
- Network Interface: `Microsoft.Network/networkInterfaces`
- Load Balancer: `Microsoft.Network/loadBalancers`

**认证**：Workload Identity (Pod 场景)

### 3.4 GCP

**API**：Cloud Monitoring API (`monitoring.v3.MetricService.ListTimeSeries`)

**资源**：
- Disk: `compute.googleapis.com/disk/` (per instance)
- Network: `compute.googleapis.com/interface/` (per instance)
- LB: `loadbalancing.googleapis.com/` (per LB forwarding rule)

**认证**：Workload Identity Federation

---

## 4. 云厂商标准指标对比

### 4.1 通用指标（所有云厂商支持）

以下指标在所有云厂商中都有对应，可作为标准化基础：

| 类别 | 标准化指标名 | AWS | Azure | GCP | 阿里云 |
|-----|------------|------|-------|-----|--------|
| CPU | `cloud_cpu_utilization` | CPUUtilization | Percentage CPU | instance/cpu/utilization | CPUUtilization |
| 内存 | `cloud_memory_utilization` | MemoryUtilization | Percentage Memory | instance/memory/utilization | MemoryUtilization |
| 网卡流入 | `cloud_network_receive_bytes_total` | NetworkIn | Network In Bytes/sec | interface/rx_bytes | InternetInboundRate |
| 网卡流出 | `cloud_network_transmit_bytes_total` | NetworkOut | Network Out Bytes/sec | interface/tx_bytes | InternetOutboundRate |
| 磁盘读吞吐 | `cloud_disk_throughput_read_bytes` | VolumeReadBytes | Disk Read Bytes/sec | disk/read_bytes_count | DiskThroughputRead |
| 磁盘写吞吐 | `cloud_disk_throughput_write_bytes` | VolumeWriteBytes | Disk Write Bytes/sec | disk/write_bytes_count | DiskThroughputWrite |
| LB 请求数 | `cloud_lb_requests_total` | RequestCount | RequestCount | https/request_count | QPS |
| LB 活跃连接 | `cloud_lb_active_connections` | ActiveFlowCount | Current Connections | l4.flows.connections | ActiveConnection |

### 4.2 差异指标（云厂商特有）

| 云厂商 | 特有指标 | 说明 |
|--------|---------|------|
| AWS | `VolumeQueueLength` | EBS 队列长度 |
| AWS | `TargetResponseTime` | ALB 目标响应时间 |
| AWS | `NLBActiveFlowCount`, `NLBNewFlowCount` | NLB 新建/活跃连接数 |
| Azure | `DiskIOPSConsumedPercentage` | 已消耗 IOPS 百分比 |
| Azure | `VMAvailabilitySetMetric` | 可用性集指标 |
| GCP | `instance/guest/disk_space_used_percent` | 来宾磁盘使用率 |
| GCP | `gpu_utilization` | GPU 使用率（GCE） |
| GCP | `loadbalancing.googleapis.com/backend_latency` | LB 后端延迟 |
| 阿里云 | `ECSIntranetInRate`, `ECSIntranetOutRate` | ECS 内网流入/流出速率 |
| 阿里云 | `DiskReadIOPS`, `DiskWriteIOPS` | 云盘 IOPS |
| 阿里云 | `DiskLatency` | 云盘读写延迟 |

### 4.3 指标映射示例

```promql
# 统一标准化指标名（输出到 Prometheus）
cloud_cpu_utilization{instance_id="i-xxx",provider="aws"} 75.5
cloud_cpu_utilization{instance_id="i-xxx",provider="aliyun"} 72.3

# 原始云厂商指标（可通过 label 区分）
cloud_cpu_utilization_raw{instance_id="i-xxx",provider="aws",metric_name="CPUUtilization"} 75.5
cloud_cpu_utilization_raw{instance_id="i-xxx",provider="aliyun",metric_name="CPUUtilization"} 72.3
```

---

## 5. Provider Capability Matrix

各云厂商对不同监控类别的支持情况，标注数据来源（Cloud API / K8s API / Agent）。

| 类别 | 子项 | AWS | 阿里云 | Azure | GCP | 标准化 |
|------|-----|-----|--------|-------|-----|--------|
| **Node Discovery** | | | | | | |
| | 从 `spec.providerID` 解析 InstanceId | ✅ | ✅ | ✅ | ✅ | ✅ |
| | 从 K8s API 获取节点列表 | ✅ | ✅ | ✅ | ✅ | ✅ |
| | 从 `node.spec.externalID` 获取实例名 | ✅ | - | - | - | ❌ |
| | Watch 节点变化 | ✅ | ✅ | ✅ | ✅ | ✅ |
| **VM 指标** | | | | | | |
| | CPU 使用率 | ✅ | ✅ | ✅ | ✅ | ✅ |
| | 内存使用率 | ✅ | ✅ | ✅ | ✅ | ✅ |
| | 磁盘空间（系统盘） | ✅ | ✅ | ✅ | ✅ | ✅ |
| | 数据来源 | CloudWatch | 云监控 API | Azure Monitor | Cloud Monitoring | |
| **云盘（数据盘）** | | | | | | |
| | IOPS（读/写） | ✅ | ✅ | ✅ | ✅ | ✅ |
| | 吞吐量（读/写） | ✅ | ✅ | ✅ | ✅ | ✅ |
| | 延迟 / 队列长度 | ✅ | ✅ | ✅ | ✅ | ✅ |
| | 按 PVC/PV 关联 | ✅ | ✅ | ✅ | ✅ | ✅ |
| | 数据来源 | CloudWatch (EBS) | 云监控 (ESSD) | Azure Monitor (Managed Disk) | Cloud Monitoring | |
| **ENI / 网卡** | | | | | | |
| | 流入/流出带宽 | ✅ | ✅ | ✅ | ✅ | ✅ |
| | 包速率 | ✅ | ✅ | ✅ | ✅ | ✅ |
| | 按 interface 区分 | ✅ | ✅ | ✅ | ✅ | ✅ |
| | 数据来源 | CloudWatch (EC2) | 云监控 | Azure Monitor | Cloud Monitoring | |
| **LoadBalancer** | | | | | | |
| | 请求数 / QPS | ✅ | ✅ | ✅ | ✅ | ✅ |
| | 活跃连接数 | ✅ | ✅ | ✅ | ✅ | ✅ |
| | 健康主机数 | ✅ | ✅ | ✅ | ✅ | ✅ |
| | 后端延迟 | ✅ | - | ✅ | ✅ | 部分 |
| | 从 K8s Service 映射 LB ID | ✅ | ✅ | ✅ | ✅ | ✅ |
| | 数据来源 | CloudWatch (ELB) | 云监控 (SLB) | Azure Monitor (Load Balancer) | Cloud Monitoring | |
| **GPU 指标** | | | | | | |
| | GPU 使用率 | ✅ | ✅ | ✅ | ✅ | ❌ |
| | GPU 显存 | ✅ | - | ✅ | ✅ | ❌ |
| | 数据来源 | CloudWatch (EBS) | 云监控 | Azure Monitor | Cloud Monitoring | Provider-specific |
| **跨集群** | | | | | | |
| | 节点仅采集当前集群 | ✅ | ✅ | ✅ | ✅ | ✅ (通过 providerID 隔离) |
| | 多集群多账号聚合 | ⚠️ | ⚠️ | ⚠️ | ⚠️ | 需 federation 层 |

### 5.1 关键结论

1. **VM / 云盘 / 网卡 / LB** 这四类指标在四大云厂商都有对应，可通过标准化 `cloud_*` 前缀输出
2. **GPU 指标** 各家差异较大，建议作为 provider-specific 指标输出（保留原始 metric name）
3. **LB 后端延迟** 阿里云目前无对应 API，只能通过 CloudMonitor Exporter 补充或暂时跳过
4. **PVC 关联** 依赖 K8s PV/PVC API，与云厂商无关，四家均可实现

### 5.2 数据来源说明

| 数据来源 | 说明 | 示例 |
|---------|------|------|
| **Cloud API** | 云厂商原生监控 / 资源 API | CloudWatch, ECS DescribeDiskMonitorData |
| **K8s API** | K8s 原生资源 API | nodes, pods, persistentvolumes |
| **CSI Driver** | 容器存储接口获取 disk ID | PV.spec.csi.volumeHandle |
| **Metadata Service** | 云厂商实例元数据服务 | EC2 metadata, ECS metadata |

---

## 6. 统一 Metrics Schema

### 6.1 设计目标

统一 schema 的目标不是抹平所有云厂商差异，而是做到：

1. **通用指标统一命名**，方便跨云 Dashboard / Alert 复用
2. **保留 provider-specific 指标**，避免丢失云厂商特有能力
3. **label 语义稳定**，避免同一指标在不同 provider 下 label 含义不同
4. **优先 Prometheus 习惯**，遵循 `_total` / `_bytes` / `_seconds` 等命名规范

> **MVP 决策**：为了兼容当前设计与后续 dashboard 落地，v0.1 的公共指标名仍沿用第 2.2.4 节中的既有命名（例如 `cloud_disk_io_read_total`、`cloud_disk_latency_ms`）。第 6 节的职责改为补充语义约束、单位归一化策略与 provider-specific 指标策略，而不是重新定义另一套公共 metric 名称。

### 6.2 命名约定

MVP 统一采用：

```text
cloud_<resource>_<metric>[_<unit>]
```

示例：
- `cloud_cpu_utilization`
- `cloud_memory_utilization`
- `cloud_disk_io_read_total`
- `cloud_disk_io_write_total`
- `cloud_disk_throughput_read_bytes`
- `cloud_disk_throughput_write_bytes`
- `cloud_disk_latency_ms`
- `cloud_network_receive_bytes_total`
- `cloud_network_transmit_bytes_total`
- `cloud_lb_requests_total`
- `cloud_lb_active_connections`
- `cloud_lb_healthy_hosts`

### 6.3 指标类型约定

| 类型 | 规则 | 示例 |
|------|------|------|
| Counter | 单调递增累计值，必须以 `_total` 结尾 | `cloud_lb_requests_total` |
| Gauge | 当前状态值，不带 `_total` | `cloud_lb_active_connections` |
| Utilization | CPU/内存利用率可直接保留云厂商原始百分比语义，MVP 不强制重命名为 `_ratio` | `cloud_cpu_utilization` |
| Bytes | 字节累计值优先 `_bytes_total` | `cloud_network_receive_bytes_total` |
| Latency | MVP 公共指标沿用 `*_ms` 命名；若底层 API 返回其他单位，仅做单位换算，不做语义改写 | `cloud_disk_latency_ms` |
| Histogram | 暂不作为 MVP 必选；有需要时从 provider-specific 扩展 | `cloud_lb_request_duration_seconds_bucket` |

补充约束：

- 只有当云厂商 API 返回 **累计值 / 单调递增值** 时，才映射为 `*_total` 计数器。
- 如果云厂商 API 仅提供 **速率、瞬时值、队列深度** 等 gauge 语义，则 exporter 不应强行映射为 `*_total`。
- 对于无法与现有标准指标名严格对齐的情况，优先输出 provider-specific raw 指标；必要时可增加语义明确的 gauge 指标，例如 `*_bytes_per_second`。
- **只做单位归一化，不做语义转换**。例如队列长度不应映射成延迟，速率不应伪装成累计值。

### 6.4 标准 Labels

#### 必填 labels

所有标准化指标都应尽量包含以下 labels：

- `provider`: `aws` / `aliyun` / `azure` / `gcp`
- `account_id`: 云账号 / subscription / project
- `region`: 资源所属 region
- `resource_type`: `vm` / `disk` / `eni` / `lb`
- `instance_id`: 节点/实例唯一标识（如果适用）

#### 常用可选 labels

| Label | 含义 | 适用资源 |
|-------|------|---------|
| `node` | K8s node name | vm / disk / eni |
| `zone` | 可用区 | vm / disk / lb |
| `device` | 磁盘设备名，如 `/dev/vdb` | disk |
| `interface` | 网卡名，如 `eth0` / eni id | eni |
| `lb_id` | 云 LB 唯一标识 | lb |
| `service` | K8s Service 名称 | lb |
| `namespace` | K8s namespace | lb / pvc |
| `pv` | PV 名称 | disk |
| `pvc` | PVC 名称 | disk |
| `workload` | 使用 PVC 的 workload/pod | disk |
| `metric_name` | 原始云厂商 metric name | raw metric only |

#### 约束

- label key 一律使用 snake_case
- label value 不做 provider-specific 拼接语义
- 不把 unit 放进 label
- 不在标准指标里塞 `__name__` 级别的 provider 细节

### 6.5 标准化指标集合（MVP）

#### VM

- `cloud_cpu_utilization`（可选，MVP 默认不实现）
- `cloud_memory_utilization`（可选，MVP 默认不实现）

#### Disk

- `cloud_disk_io_read_total`
- `cloud_disk_io_write_total`
- `cloud_disk_throughput_read_bytes`
- `cloud_disk_throughput_write_bytes`
- `cloud_disk_latency_ms`
- `cloud_disk_queue_depth`（无标准映射时可退回 raw/provider-specific）

#### Network / ENI

- `cloud_network_receive_bytes_total`
- `cloud_network_transmit_bytes_total`
- `cloud_network_receive_bytes_per_second`（仅底层 API 提供速率时使用）
- `cloud_network_transmit_bytes_per_second`（仅底层 API 提供速率时使用）
- `cloud_network_receive_packets_total`
- `cloud_network_transmit_packets_total`
- `cloud_network_receive_dropped_total`
- `cloud_network_transmit_dropped_total`

#### LoadBalancer

- `cloud_lb_requests_total`
- `cloud_lb_active_connections`
- `cloud_lb_new_connections_total`
- `cloud_lb_healthy_hosts`
- `cloud_lb_unhealthy_hosts`
- `cloud_lb_network_rx_bytes`
- `cloud_lb_network_tx_bytes`
- `cloud_lb_backend_latency_ms`

### 6.6 Provider-specific 指标策略

原则：

- **有标准映射** → 输出标准指标
- **无标准映射但有价值** → 同时输出 raw/provider-specific 指标
- **语义差异太大** → 不强行映射，只保留 raw 指标

建议命名：

```text
cloud_raw_<provider>_<resource>_<metric>
```

示例：
- `cloud_raw_aws_ebs_volume_queue_length`
- `cloud_raw_aliyun_slb_qps`
- `cloud_raw_gcp_lb_backend_latencies`
- `cloud_raw_azure_disk_iops_consumed_percentage`

raw 指标至少带：
- `provider`
- `metric_name`
- `resource_type`
- `instance_id` / `lb_id` / `disk_id`

### 6.7 单位归一化规则

| 语义 | 统一单位 | 说明 |
|------|----------|------|
| CPU / Memory utilization | percent 或 provider 原始语义 | MVP 不强制转换为 ratio |
| Latency | ms | 公共指标沿用 `*_ms`；底层若非毫秒则换算为毫秒 |
| Throughput | bytes_total 或 bytes_per_second | 优先累计值；若云 API 只给速率则保留 gauge，不伪装成 counter |
| IOPS | ops_total 或 ops_per_second | 优先累计值；若只有瞬时 IOPS 则输出 gauge 或 raw |
| Bandwidth | bytes_per_second | 若是瞬时速率则不加 `_total` |

### 6.8 示例

#### 标准化输出

```promql
cloud_disk_throughput_read_bytes{
  provider="aliyun",
  account_id="123456789",
  region="cn-hangzhou",
  resource_type="disk",
  instance_id="i-bp123",
  node="ack-node-1",
  pv="pv-001",
  pvc="data-mysql",
  namespace="database"
} 1.234e+12
```

#### raw 输出

```promql
cloud_raw_aliyun_disk_disk_read_iops{
  provider="aliyun",
  metric_name="DiskReadIOPS",
  resource_type="disk",
  instance_id="i-bp123",
  disk_id="d-bp123"
} 2400
```

### 6.9 推荐实践

1. **Dashboard 默认看标准指标**
2. **排障时下钻到 raw 指标**
3. **Alert 优先基于标准指标**，减少跨云迁移成本
4. **provider-specific dashboard 单独维护**，不要污染通用面板

---

## 7. 数据模型

### 7.1 关联 PVC 的数据盘监控

对于数据盘（PVC），需要将云盘 ID 与 K8s PV/PVC 关联，才能在 Dashboard 中按应用维度展示，而不是只看到 disk id。

**关联关系**：

| 来源 | 信息 | 映射到 Label |
|-----|------|-------------|
| Cloud API | DiskId, AttachedNode | `instance_id`, `node` |
| K8s PV | `volumeHandle` = DiskId | `pv` |
| K8s PVC | `claimRef.name` + `claimRef.namespace` | `pvc`, `namespace` |
| K8s Pod | `.spec.volumes[].persistentVolumeClaim.claimName` | `pod`, `workload`, `workload_kind` |

**数据结构**：

```go
type DiskTarget struct {
    DiskID       string  // 云盘 ID (volumeHandle)
    InstanceID   string  // 当前挂载的云主机 InstanceId
    NodeName     string  // 当前挂载的 K8s 节点名
    PVName       string  // 对应 PV 名称
    PVCName      string  // 对应 PVC 名称
    PVCNamespace string  // PVC 所属 Namespace
    Workload     string  // 使用该 PVC 的 Workload 名
}
```

**采集流程**：

```text
1. 从 Cloud API 获取云盘列表 + 当前挂载的 InstanceId
2. 遍历 K8s PV，匹配 PV.spec.csi.volumeHandle == DiskId
3. 遍历 PVC，匹配 PVC.spec.volumeName == PV.name
4. 遍历 Pod，匹配 spec.volumes[].persistentVolumeClaim.claimName == PVC.name
5. 输出指标时追加 {pvc, namespace, pod, workload, workload_kind, node, ...}
```

**最终 Label 示例**：

```promql
cloud_disk_throughput_write_bytes{
  instance_id="i-xxx",
  disk_id="d-xxx",
  disk_role="data",
  node="worker-node-1",
  pv="pv-cloud-disk-essd-001",
  pvc="data-mysql",
  namespace="database",
  pod="mysql-statefulset-0",
  workload="mysql-statefulset",
  workload_kind="StatefulSet",
  provider="aliyun"
}
```

### 7.2 Prometheus Metric 结构

```go
type Metric struct {
    Name      string
    Labels    map[string]string
    Value     float64
}
```

> **Prometheus 合约（MVP）**：exporter 在 `/metrics` 中不显式输出样本时间戳，统一由 Prometheus 在抓取时赋值。这样可以避免云监控时间戳与抓取时间混用导致的陈旧样本、告警抖动和调试复杂度上升。

### 7.3 输出示例

```text
# HELP cloud_disk_latency_ms Disk latency in milliseconds.
# TYPE cloud_disk_latency_ms gauge
cloud_disk_latency_ms{instance_id="i-t4n1qcztiqh93yy2m9cd",disk_id="d-t4n1qcztiqh93yy54z9i",disk_role="system",node="ack-node-1",provider="aliyun",region="ap-southeast-1",resource_type="disk"} 0.293

# HELP cloud_disk_throughput_write_bytes Disk write throughput in bytes per second.
# TYPE cloud_disk_throughput_write_bytes gauge
cloud_disk_throughput_write_bytes{instance_id="i-t4n280pbxa0017yu3qos",disk_id="d-t4n1ufmdhby2clfxff5a",disk_role="data",pv="disk-xxx",pvc="persistent-queue-data-vmagent-o11y-vmagent-0",namespace="tidb2039555911017566208",pod="vmagent-o11y-vmagent-0",workload="vmagent-o11y-vmagent",workload_kind="StatefulSet",provider="aliyun",region="ap-southeast-1",resource_type="disk"} 0

# HELP cloud_network_receive_bytes_per_second Network receive rate in bytes per second.
# TYPE cloud_network_receive_bytes_per_second gauge
cloud_network_receive_bytes_per_second{instance_id="i-t4n280pbxa0017yu3qos",node="ack-node-2",provider="aliyun",region="ap-southeast-1",resource_type="network"} 249270.83333333334
```

---

## 8. 配置设计

### 8.1 ConfigMap

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: cloud-metrics-exporter
  namespace: monitoring
data:
  config.yaml: |
    providers:
      - name: aliyun
        enabled: true
        regions:
          - ap-southeast-1
        metricTypes:
          - disk
          - network

    discovery:
      nodeRefreshInterval: 60s

    scrape:
      interval: 60s
      timeout: 30s
```

### 8.2 认证配置

MVP 认证方式支持 **阿里云 RRSA** 或 **ACK 节点 RAM Role**。exporter 通过阿里云 SDK 默认凭证链获取临时凭证；MVP 不依赖静态 AK/SK。

```yaml
serviceAccountName: cloud-metrics-exporter
# 如使用 RRSA，需要按 ACK 要求绑定 OIDC / RAM Role
# 如使用节点 RAM Role，则无需额外 annotation
# exporter 容器本身不需要显式注入 AK/SK
```

---

## 9. 部署方式

### 9.1 Kubernetes Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cloud-metrics-exporter
  namespace: monitoring
spec:
  replicas: 1
  selector:
    matchLabels:
      app: cloud-metrics-exporter
  template:
    metadata:
      labels:
        app: cloud-metrics-exporter
    spec:
      serviceAccountName: cloud-metrics-exporter
      imagePullSecrets:
        - name: ghcr-pull-secret
      containers:
        - name: exporter
          image: ghcr.io/tennix/cloud-metrics-exporter:ack-node-role
          ports:
            - name: metrics
              containerPort: 9100
          resources:
            requests:
              cpu: 100m
              memory: 256Mi
            limits:
              cpu: 500m
              memory: 512Mi
          env:
            - name: NODE_NAME
              valueFrom:
                fieldRef:
                  fieldPath: spec.nodeName
          volumeMounts:
            - name: config
              mountPath: /config
              readOnly: true
      volumes:
        - name: config
          configMap:
            name: cloud-metrics-exporter
```

### 9.2 RBAC

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: cloud-metrics-exporter
rules:
  - apiGroups: [""]
    resources: ["nodes", "persistentvolumes", "persistentvolumeclaims", "pods"]
    verbs: ["list", "watch"]
  - apiGroups: ["apps"]
    resources: ["replicasets"]
    verbs: ["list", "watch"]
```

### 9.3 Prometheus Scrape Config

MVP 使用固定 `/metrics` 路径和固定端口，由 Service 暴露后给 Prometheus 抓取；不依赖 Pod Annotation 动态覆盖抓取参数。

```yaml
- job_name: 'cloud-metrics-exporter'
  kubernetes_sd_configs:
    - role: endpoints
      namespaces:
        names:
          - monitoring
  relabel_configs:
    - source_labels: [__meta_kubernetes_service_name]
      action: keep
      regex: cloud-metrics-exporter
    - source_labels: [__meta_kubernetes_endpoint_port_name]
      action: keep
      regex: metrics
    - target_label: __metrics_path__
      action: replace
      replacement: /metrics
```

---

## 10. 实施计划

### Phase 1: MVP (v0.1)
- 单一云厂商：阿里云
- 支持：K8s Node Discovery、云盘（系统盘 + PVC 数据盘）、网卡监控
- 从 K8s API 获取节点并解析 `node.spec.providerID`（兼容 ACK providerID 格式）
- 指标命名以第 2.2.4 节为准
- 认证方式：阿里云 RRSA 或 ACK 节点 RAM Role
- 单副本 Deployment

#### Phase 1 Checklist

- [x] **Node Discovery**
  - [x] List/Watch K8s `nodes`
  - [x] 解析 `node.spec.providerID`，提取 `provider` / `region` / `instance_id`
  - [x] 兼容 ACK `region.instanceId` providerID 格式
  - [x] 构建并缓存 `NodeTarget` 列表
  - [x] 跳过缺失或无法解析 `providerID` 的节点

- [x] **Aliyun Provider**
  - [x] 使用阿里云 SDK 默认凭证链（支持 RRSA / node-role）
  - [x] 校验 Pod 内凭证自动发现链路可用
  - [x] 增加 provider 健康检查 / 启动期校验

- [x] **Metrics Scope**
  - [x] 实现云盘指标采集
  - [x] 实现网卡指标采集
  - [x] MVP 默认不实现 VM CPU / 内存指标
  - [x] MVP 默认不实现 LB 指标

- [x] **Metric Contract**
  - [x] 使用第 2.2.4 节定义的公共指标名
  - [x] 输出 `cloud_disk_throughput_read_bytes`
  - [x] 输出 `cloud_disk_throughput_write_bytes`
  - [x] 输出 `cloud_disk_latency_ms`
  - [x] 输出 `cloud_network_receive_bytes_per_second`
  - [x] 输出 `cloud_network_transmit_bytes_per_second`

- [x] **Metric Semantics**
  - [x] 仅在源数据为累计值时输出 `*_total`
  - [x] 对速率类指标输出语义明确的 gauge（如 `*_bytes_per_second`）
  - [x] 不将队列深度映射为延迟
  - [x] 无法准确标准化的指标退回 raw/provider-specific 输出

- [x] **Prometheus Exporter**
  - [x] 暴露固定 `/metrics` 端点
  - [x] 使用固定 metrics 端口
  - [x] 不显式输出样本时间戳
  - [x] 确保 HELP / TYPE 元数据正确

- [x] **Deployment**
  - [x] 以单副本 Deployment 运行
  - [x] 绑定 ServiceAccount
  - [x] 通过 ConfigMap 挂载配置
  - [x] 通过 Service 暴露 metrics 端口
  - [x] 使用 `imagePullSecrets` 拉取 GHCR 镜像

- [x] **RBAC**
  - [x] 授予 `nodes` 的 `list/watch`
  - [x] 授予 `persistentvolumes` / `persistentvolumeclaims` / `pods` / `replicasets` 的 `list/watch`
  - [x] 为 PVC/PV enrichment 提供最小所需权限集

- [x] **Prometheus Scrape**
  - [x] 通过 Service / endpoints 抓取 exporter
  - [x] 使用固定 `/metrics` 路径
  - [x] MVP 不依赖 Pod Annotation 改写抓取参数

- [x] **Validation**
  - [x] 使用真实 `providerID` 样例验证节点解析逻辑
  - [x] 在集群内验证 RRSA / node-role 凭证链
  - [x] 校验导出的 metric name / type 与设计一致
  - [x] 校验 exporter 不输出误导性的 counter 语义
  - [x] 校验异常云数据不会生成错误或歧义指标
  - [x] 校验增量磁盘缓存策略可在 scrape 预算内逐步产出 disk 指标

### Phase 2: K8s 集成 (v0.2)
- 完善节点缓存 / Reload / 异常恢复机制
- 补充配置校验、错误可观测性和单元测试

### Phase 3: LB 监控 (v0.3)
- 实现 Service Discovery（LoadBalancer 类型）
- 采集 LB QPS/连接数/流量

### Phase 4: 多云支持 (v0.4)
- AWS Provider (CloudWatch)
- Azure Provider
- GCP Provider

### Phase 5: 生产化 (v1.0)
- 高可用部署（多副本 + 故障转移）
- 性能优化（批量查询、缓存）
- 完整测试覆盖

---

## 11. 风险与备选方案

| 风险 | 缓解措施 |
|-----|---------|
| 云 API 限流 | 单盘单次查询；增量刷新；本地缓存；disk scrape 预算隔离 |
| K8s API 不可用 | 本地缓存节点列表；exporter 内置 fallback |
| 指标漂移（云厂商变更 API） | 抽象层隔离；单元测试 mock 云 API 响应 |
| 多云支持复杂度 | Provider 接口隔离；每个云独立测试 |
| 数据量大（大规模集群） | 增量采集（只采集变化节点）；Prometheus remote_write 降频 |

---

## 12. 未来扩展

- **日志关联**：将云监控指标与容器日志关联（通过 trace_id）
- **告警集成**：原生支持云厂商 Alerting API
- **成本优化**：采集云盘/流量数据用于成本分析 Dashboard
- **Traces**：支持采集云上 LB 的延迟分布（Prometheus Histogram）

---

## 13. 参考资料

- [阿里云 Cloud Monitor API](https://help.aliyun.com/zh/cms/)
- [AWS CloudWatch Metrics](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/aws-services-cloudwatch-metrics.html)
- [Azure Monitor Metrics](https://learn.microsoft.com/zh-cn/azure/azure-monitor/essentials/data-platform-metrics)
- [GCP Cloud Monitoring](https://cloud.google.com/monitoring/api/metrics)
- [Prometheus Exposition Format](https://prometheus.io/docs/instrumenting/exposition_formats/)

---

*Author: tennix*  
*Date: 2026-03-30*
