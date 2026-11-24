# 🧭 gomcrouter

[![CI](https://github.com/tentens-tech/gomcrouter/actions/workflows/ci.yml/badge.svg)](https://github.com/tentens-tech/gomcrouter/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/tentens-tech/gomcrouter/branch/main/graph/badge.svg)](https://codecov.io/gh/tentens-tech/gomcrouter)
[![Release](https://img.shields.io/github/v/release/tentens-tech/gomcrouter?sort=semver)](https://github.com/tentens-tech/gomcrouter/releases)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/tentens-tech/gomcrouter)](go.mod)
[![Go Report Card](https://goreportcard.com/badge/github.com/tentens-tech/gomcrouter)](https://goreportcard.com/report/github.com/tentens-tech/gomcrouter)

gomcrouter is a high performance memcache request router written in Go.
It accepts memcache protocol traffic, routes operations using configurable policies, and forwards requests to upstream memcached nodes using an event driven upstream engine.

Designed for low latency, high concurrency, and predictable routing behavior.

Supports memcache text protocol.

---

## Features

- Flexible routing policies per operation
- Multiple backend servers via ordered pools
- Active health checks
- Prometheus metrics endpoint
- Configurable server event loops
- Multiple upstream connections per host
- Configurable timeouts and buffers
- Small static binary, multi-arch image (linux/amd64, linux/arm64) running as non-root

---

## Installation

Every [release](https://github.com/tentens-tech/gomcrouter/releases) publishes a multi-arch image
and static binaries for linux/amd64 and linux/arm64 with their `checksums.txt`.

```bash
# Docker image
docker pull ghcr.io/tentens-tech/gomcrouter:latest

# Binary
VERSION=v1.3.4
curl -fsSL https://github.com/tentens-tech/gomcrouter/releases/download/$VERSION/gomcrouter-$VERSION-linux-amd64.tar.gz | tar xz gomcrouter
./gomcrouter --version
```

---

## Quick Start

### Build from source

```bash
make build        # or: go build -o gomcrouter .
```

Run with config file:

```bash
./gomcrouter -f router.yaml
```

### Run with Docker

```bash
docker run --rm \
  -p 8080:8080 \
  -p 9090:9090 \
  -v $(pwd)/router.yaml:/router.yaml:ro \
  ghcr.io/tentens-tech/gomcrouter:latest \
  /bin/gomcrouter -f /router.yaml
```

To build the image locally, run `make docker-build` (or `docker build -t gomcrouter .`).
The image runs as an unprivileged user (UID/GID `10001`) and works with a read-only root filesystem.

Ports:

- 8080 router listen
- 9090 metrics endpoint

See [examples/docker](examples/docker) for a docker-compose setup with two memcached nodes,
a writer, a metrics watcher and an end-to-end test.

---

## Command Line Options

```text
gomcrouter [flags]
```

### General

```text
-f, --router-config string
    path to router config file
    default router.yaml

-l, --listen string
    listen address
    default tcp://:8080

--log-level string
    log level
    default info

-v, --version
    print the version and exit

-h, --help
    print the help and exit
```

### Runtime

```text
-n, --server-event-loops int
    number of server event loops
    default 1

-C, --upstream-connections int
    upstream connections per host
    default 2

-r, --timeout-ms int
    upstream timeout in milliseconds
    default 1000

-b, --buffer-size int
    socket read / write buffer sizes in bytes
    default 1048576
```

### Health and DNS

```text
-H, --healthcheck-interval duration
    upstream healthcheck interval
    default 10s

-t, --dns-cache-ttl duration
    dns cache ttl
    default 30s
```

### Metrics

```text
-L, --metrics-listen-addr string
    metrics listen address
    default :9090
```

---

## Example Router Configuration

`router.yaml`:

```yaml
orderedPool:
  - memcache1:11211
  - memcache2:11211

route:
  type: OperationSelectorRoute
  operationPolicies:
    add: AllFastestRoute
    append: AllFastestRoute
    decr: AllFastestRoute
    delete: AllFastestRoute
    get: MissFailoverRoute
    gets: MissFailoverRoute
    incr: AllFastestRoute
    prepend: AllFastestRoute
    replace: AllFastestRoute
    set: AllFastestRoute
    version: LocalRoute
    quit: LocalRoute
```

---

## Routing

The `route.type` field defines which routing strategy is used.
Any available route implementation can be configured here.

Example:

```yaml
route:
  type: OperationSelectorRoute
```

### Route Types

| Route | Description |
| ------- | ------------- |
| OperationSelectorRoute | Select routing policy based on memcache command |
| AllFastestRoute | Send request to all hosts and return fastest response |
| MissFailoverRoute | Try next host on miss or failure |
| DefaultRoute | Route to the first host in pool order |
| LocalRoute | Handle request locally in router |

Route specific options may require additional configuration fields depending on the selected type.

---

## Protocol Support

| Protocol | Status |
| -------- | ------ |
| Memcached ASCII | ✅ Supported |
| Memcached Binary | ❌ Not supported yet |
| Memcached Meta | ❌ Not supported yet |

---

## Metrics

Prometheus metrics are served on `http://<metrics-listen-addr>/metrics`:

| Metric | Labels | Description |
| -------- | -------- | ------------- |
| `gomcrouter_requests_total` | `cmd` | Requests made by clients |
| `gomcrouter_request_duration_milliseconds` | `cmd` | Request latency histogram |
| `gomcrouter_upstream_requests_total` | `cmd`, `host` | Requests made to upstream hosts |
| `gomcrouter_upstream_errors_total` | `cmd`, `host` | Errors returned from upstream hosts |
| `gomcrouter_upstream_request_duration_milliseconds` | `cmd`, `host` | Upstream request latency histogram |
| `gomcrouter_upstream_get_hits_total` | `host` | `get`/`gets` responses with at least one `VALUE` |
| `gomcrouter_upstream_get_misses_total` | `host` | `get`/`gets` responses without a `VALUE` |
| `gomcrouter_upstream_healthy_hosts_total` | | Healthy upstream hosts |
| `gomcrouter_upstream_connections_total` | `host` | Upstream connections |
| `gomcrouter_metrics_events_dropped_total` | `event_type` | Metric events dropped because the async event ring was full |
| `gomcrouter_version` | `version` | Running version |

---

## Architecture

High-level request flow inside **gomcrouter**:

```text
Clients (Memcached ASCII)
          │
          ▼
┌────────────────────────┐
│ gnet frontend          │
│ - multicore loops      │
│ - TCP accept/read/write│
└───────────┬────────────┘
            ▼
┌────────────────────────┐
│ parser + router        │
│ - decode cmd           │
│ - route/hash/dispatch  │
└───────────┬────────────┘
            ▼
┌────────────────────────┐
│ pools                  │
│ - hosts + health state │
│ - selection/failover   │
└───────────┬────────────┘
            ▼
┌────────────────────────┐
│ io.Engine              │
│ - pollGroups (shards)  │
│ - netpoll + socket I/O │
│ - rings/batching       │
└───────────┬────────────┘
            ▼
Memcached backends
```

📊 Full architecture diagram (SVG): [docs/architecture.svg](docs/architecture.svg)

---

## Performance

See [BENCHMARKS.md](BENCHMARKS.md) for memtier_benchmark results.

---

## Contributing

Contributions are welcome, see [CONTRIBUTING.md](CONTRIBUTING.md). Report security issues privately,
see [SECURITY.md](SECURITY.md).

```bash
make help       # list the targets
make test-race  # unit tests with the race detector
make lint       # golangci-lint
make e2e        # docker-compose example and its end-to-end test
```

---

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.
