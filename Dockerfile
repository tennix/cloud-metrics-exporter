FROM --platform=$BUILDPLATFORM golang:1.25.7 AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -o /out/cloud-metrics-exporter ./cmd/cloud-metrics-exporter

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/cloud-metrics-exporter /cloud-metrics-exporter
ENTRYPOINT ["/cloud-metrics-exporter"]
