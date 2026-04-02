FROM gcr.io/distroless/static-debian12:nonroot
COPY bin/cloud-metrics-exporter /cloud-metrics-exporter
ENTRYPOINT ["/cloud-metrics-exporter"]
