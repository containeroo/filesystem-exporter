# filesystem-exporter

`filesystem-exporter` scans an already-mounted filesystem path, reports usage for the root path by default, and exposes Prometheus metrics for capacity, availability, and used bytes.

The exporter is designed for Kubernetes and Prometheus-style operation:

- it keeps scrape handlers cheap by refreshing data on a background interval instead of doing a full scan on every scrape
- it exposes `/metrics`, `/-/healthy`, and `/-/ready`
- it starts serving HTTP probes immediately and performs the initial filesystem scan in the background
- it publishes process and Go runtime metrics in addition to exporter-specific metrics
- it relies on the container runtime or orchestrator to mount storage before the exporter starts

## What it measures

For a configured filesystem path such as `/data`, the exporter always reports metrics for:

- `/data`

If `-filesystem.report-child-dirs=true` is set, it also reports each immediate child directory under `/data`, for example `/data/archive` and `/data/uploads`.

It does not descend deeper for label cardinality. When child-directory reporting is enabled, the `used` metric for a child directory includes the full recursive size of that child subtree. Capacity and available bytes are derived from `statfs(2)` for the path being reported, which means sibling directories on the same filesystem typically share the same capacity and available values.

## Requirements

- a mounted filesystem path available to the exporter process
- read access to the configured path and its contents

The exporter does not mount or unmount filesystems itself. In Kubernetes, use a PVC, CSI volume, NFS volume, hostPath, or any other volume type that presents the target filesystem inside the container.

## Usage

```bash
go run ./cmd/filesystem-exporter \
  -filesystem.path=/data
```

Enable immediate child directory metrics explicitly if you need them:

```bash
go run ./cmd/filesystem-exporter \
  -filesystem.path=/data \
  -filesystem.report-child-dirs=true
```

For filesystems with many small files, especially high-latency backends like NFS, you can increase scan parallelism:

```bash
go run ./cmd/filesystem-exporter \
  -filesystem.path=/data \
  -filesystem.scan-concurrency=8
```

The exporter uses Go's standard `flag` package. It accepts both `-flag=value` and `--flag=value`, but the built-in help output uses the single-dash form, so the documentation does as well.

Then scrape:

- metrics: `http://127.0.0.1:9799/metrics`
- health: `http://127.0.0.1:9799/-/healthy`
- readiness: `http://127.0.0.1:9799/-/ready`

## Docker

Build the image with:

```bash
docker build -t filesystem-exporter:latest .
```

Run it against a bind-mounted path:

```bash
docker run --rm \
  -p 9799:9799 \
  -v /srv/data:/data:ro \
  filesystem-exporter:latest \
  -filesystem.path=/data
```

The provided [Dockerfile](Dockerfile) builds the exporter binary and packages it in a small Debian runtime image.

Published release images are expected at `ghcr.io/containeroo/filesystem-exporter:<tag>` and `ghcr.io/containeroo/filesystem-exporter:latest`.

## Kubernetes

Example manifests are available in [deploy/kubernetes/deployment.yaml](deploy/kubernetes/deployment.yaml), [deploy/kubernetes/servicemonitor.yaml](deploy/kubernetes/servicemonitor.yaml), and [deploy/kubernetes/prometheusrule.yaml](deploy/kubernetes/prometheusrule.yaml).

The deployment example mounts an NFS export directly into `/data`:

- a standard, non-privileged container
- an NFS export mounted at `/data`
- HTTP readiness and liveness probes that come up before the first full scan completes
- an optional startup probe
- a Service exposing the metrics port

Update the image reference, namespace, and the example `nfs.server` and `nfs.path` values before applying it.

The PrometheusRule example includes alerts for repeated collection failures, stale collection timestamps, and low free space on the root filesystem path. Adjust the hard-coded `/data` label matchers if you deploy the exporter with a different `-filesystem.path`.

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `-web.listen-address` | `:9799` | Address to listen on for HTTP traffic. |
| `-web.metrics-path` | `/metrics` | HTTP path used to expose Prometheus metrics. |
| `-filesystem.path` | `/data` | Absolute mounted filesystem path to scan. |
| `-filesystem.report-child-dirs` | `false` | Also report metrics for immediate child directories under `-filesystem.path`. |
| `-filesystem.scan-concurrency` | `1` | Maximum number of concurrent directory and file metadata operations during a scan. Higher values can help on NFS or small-file workloads. |
| `-collector.interval` | `5m` | Background refresh interval for usage collection. |
| `-collector.timeout` | `2m` | Timeout for an individual collection run. |

## Metrics

### Path metrics

These metrics include the constant label `root_path`, plus a `path` label for the root path. If `-filesystem.report-child-dirs=true` is enabled, they also include one series per depth-1 child directory.

- `filesystem_exporter_path_capacity_bytes`
- `filesystem_exporter_path_available_bytes`
- `filesystem_exporter_path_used_bytes`

Example:

```text
filesystem_exporter_path_used_bytes{root_path="/data",path="/data"} 1.506738176e+09
filesystem_exporter_path_used_bytes{root_path="/data",path="/data/archive"} 7.86706432e+08
```

### Exporter health metrics

- `filesystem_exporter_collect_success`
- `filesystem_exporter_collect_duration_seconds`
- `filesystem_exporter_collect_timestamp_seconds`

These help alert on failed collections while still allowing Prometheus to scrape the last successful snapshot.

## Development

Run the tests with:

```bash
go test ./...
```
