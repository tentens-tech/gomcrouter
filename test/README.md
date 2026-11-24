# Tests

```sh
make test-race                     # unit tests
make cover                         # unit tests with coverage
make e2e                           # all e2e suites (~4 min, docker)
make e2e SUITES="routes failover"  # some suites
test/e2e/run.sh -l                 # list suites
```

e2e env: `E2E_KEEP=1` keeps the containers, `E2E_NO_BUILD=1` reuses the images,
`E2E_LOGDIR` changes the logs directory (default `test/e2e/.logs`, every container log is saved there).

## Unit tests

Every package has tests next to the code. The upstream packages share a fake memcached
([`internal/upstream/internal/upstreamtest`](../internal/upstream/internal/upstreamtest/upstreamtest.go): delays,
silent and malformed modes, dropped connections, restarts on the same port). `internal/server` and `cmd` run the whole
request path: the gnet server, the router, the upstream engine and a fake memcached.

## How e2e works

[`e2e/run.sh`](e2e/run.sh) builds the image from the [`Dockerfile`](../Dockerfile) plus a tools layer
([`e2e/tools.Dockerfile`](e2e/tools.Dockerfile): `curl` and [`e2e-client`](e2e/client/main.go), the user stays
10001) and runs every router like in production: read-only root FS, non-root, `--cap-drop ALL`, `no-new-privileges`.

All containers share the `gmr-e2e` network. Two memcached 1.6 nodes are reachable as `memcache1` and `memcache2`;
the `late` and `dns` suites start their own nodes. Requests are sent from the `gmr-tools` container with
`e2e-client`:

- `e2e-client raw 'set k 0 0 1\r\nv\r\n' 'get k\r\n'` sends raw requests and prints the replies, to compare them
  byte by byte (with `-closed` it also requires the server to close the connection);
- `e2e-client load -conns 32 -ops 500 -size 200 -pipeline 16` writes unique keys from many connections, reads them
  back in pipelined batches and fails on any value that doesn't belong to its request.

Values are checked directly on the memcached nodes too, to see where a route sent them. Routers run with
`--healthcheck-interval 1s --dns-cache-ttl 2s` so failures are detected quickly.

Configs: [`examples/docker/gomcrouter.yaml`](../examples/docker/gomcrouter.yaml) and
[`e2e/configs/`](e2e/configs) for the cases the example doesn't cover.
[`examples/docker`](../examples/docker) stays a runnable docker-compose example with its own smoke test (`make example`).

## Coverage

Where every option is tested. *unit* — `go test`, otherwise the e2e suite.

### Flags

| Flag | Tests |
| --- | --- |
| `-f`, `--router-config` | every suite; missing file, invalid YAML: `flags`, `image`; unit (`internal/config`) |
| `-l`, `--listen` | `flags` (custom port, the default port isn't used) |
| `-L`, `--metrics-listen-addr` | `flags` |
| `--log-level` | `flags` (`error`, invalid value), `late` (`debug`); unit |
| `-H`, `--healthcheck-interval` | `failover`, `late`; unit (`internal/upstream`) |
| `-t`, `--dns-cache-ttl` | `dns`, `late`; unit |
| `-C`, `--upstream-connections` | `flags`, `load`; unit (`internal/server`: replies keep their order across sockets) |
| `-n`, `--server-event-loops` | `flags`, `load`; unit |
| `-r`, `--timeout-ms` | `flags`; unit (`internal/upstream/io`: timeouts without other traffic) |
| `-b`, `--buffer-size` | `flags`; unit (requests and replies larger than the buffer) |
| `-v`, `--version`, `-h` | `image`; unit (`cmd`) |

### Routes

| Route | Tests |
| --- | --- |
| `DefaultRoute` | `routes`, `failover` (the next healthy host takes over); unit |
| `AllFastestRoute` | `routes`, `example` (set, incr, delete fan out), `failover`; unit |
| `MissFailoverRoute` | `routes`, `example`; unit (replies keep their order when a miss goes to the next host) |
| `LocalRoute` | `routes`, `example` (version); unit |
| `OperationSelectorRoute` | `example`, `routes` (without policies, unknown route type); unit |
| empty pool, no healthy host | `routes`, `failover`, `late` |

### Protocol

| Case | Tests |
| --- | --- |
| get, gets, multi-key get, set, add, replace, append, prepend, cas, incr, decr, touch, delete | `protocol`; unit (`internal/proto/ascii`) |
| stats, version, verbosity, flush_all | `protocol` |
| quit (answered by the router, not sent to memcached) | `protocol`, `routes`; unit (`internal/server`) |
| pipelined requests, a request split across packets | `protocol`, `load`; unit |
| empty, binary, 200 KB and 900 KB values | `protocol` |
| malformed and unknown commands | `protocol`; unit |

### Upstreams

| Case | Tests |
| --- | --- |
| a host goes down and comes back, all hosts down | `failover`; unit (`internal/upstream`) |
| a host that doesn't resolve at start | `late` |
| the address of a host changes | `dns`; unit |

### Metrics

| Metric | Tests |
| --- | --- |
| `gomcrouter_requests_total`, `gomcrouter_request_duration_milliseconds` | `metrics`; unit (`internal/observability/metric`) |
| `gomcrouter_upstream_requests_total`, `gomcrouter_upstream_request_duration_milliseconds` | `metrics`; unit |
| `gomcrouter_upstream_get_hits_total`, `gomcrouter_upstream_get_misses_total` | `metrics`; unit |
| `gomcrouter_upstream_healthy_hosts_total` | every suite (readiness), `metrics`, `failover` |
| `gomcrouter_upstream_connections_total`, `gomcrouter_version` | `metrics`; unit |
| `gomcrouter_upstream_errors_total`, `gomcrouter_metrics_events_dropped_total` | unit |

### Image and signals

| Case | Tests |
| --- | --- |
| uid 10001, root-owned read-only binary, OCI version label | `image` |
| SIGINT, SIGTERM, `docker stop`: graceful shutdown, exit code 0 | `signals`; unit (`cmd`) |
| no panics, fatal errors or data races in any container log | after every run |
