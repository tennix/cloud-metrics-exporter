# Cloud Metrics Exporter

Cloud Metrics Exporter is a Go-based Prometheus exporter for Kubernetes clusters that need cloud-provider metrics with Kubernetes context.

It discovers cluster nodes from the Kubernetes API, collects cloud metrics directly from provider APIs, and exposes them on a standard `/metrics` endpoint for Prometheus.

## What problem this project solves

Typical Kubernetes monitoring stacks already have good local node metrics from tools such as `node-exporter`, but they do not expose cloud-specific signals such as cloud disk latency, IOPS, throughput, or cloud instance network traffic.

Cloud-provider metrics are often exposed outside direct Kubernetes workload context, which makes it difficult to answer cluster-scoped questions such as:

- which cloud disks belong to the nodes in this cluster,
- which PVC-backed data disk is slow,
- and which workload is affected by a cloud-side storage or network problem.

This project narrows collection to the nodes that actually belong to the current Kubernetes cluster and enriches disk metrics with Kubernetes labels such as `pv`, `pvc`, `pod`, and workload metadata.

## Design

The implementation design and current architecture are documented in `docs/design.md`.

## Local build

```bash
go test ./...
go build ./cmd/cloud-metrics-exporter
```

## Runtime config

The sample config lives at `configs/config.yaml` and is mounted to `/config/config.yaml` in the Kubernetes Deployment.

The Prometheus scrape snippet lives at `configs/prometheus-scrape.yaml`.

## Deployment order

```bash
kubectl apply -f deploy/serviceaccount.yaml
kubectl apply -f deploy/clusterrole.yaml
kubectl apply -f deploy/clusterrolebinding.yaml
kubectl apply -f deploy/configmap.yaml
kubectl apply -f deploy/deployment.yaml
kubectl apply -f deploy/service.yaml
```

## Helm chart

An equivalent Helm chart lives at `deploy/helm/cloud-metrics-exporter/`.

```bash
helm upgrade --install cloud-metrics-exporter deploy/helm/cloud-metrics-exporter --namespace monitoring --create-namespace
```

## TODO

- Add support for additional cloud providers behind the existing provider boundary.
- Expand metric coverage beyond disk and instance network metrics.
- Add load balancer metrics with Kubernetes service context where practical.
- Improve deployment options beyond the current single-replica baseline.
