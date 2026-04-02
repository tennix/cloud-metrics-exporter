# Cloud Metrics Exporter

Phase 1 MVP exports Aliyun cloud disk and network metrics for Kubernetes nodes discovered from `node.spec.providerID`.

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

## Phase 1 validation

- verify the exporter can resolve RRSA or node-role credentials in ACK
- verify the pod can read Kubernetes `nodes`
- verify `/metrics` is reachable on port `9100`
- verify the exporter exposes only semantically correct standard metrics plus raw fallback metrics when needed
