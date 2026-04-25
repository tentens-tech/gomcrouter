# Changelog

All notable changes to **gomcrouter** are documented in this file.  
Format follows Keep a Changelog and Semantic Versioning.

---

## Unreleased

### Added
- `SECURITY.md` with private vulnerability disclosure policy
- `CODE_OF_CONDUCT.md` adopting Contributor Covenant 2.1
- GitHub issue templates (bug report, feature request) and pull request template
- `dependabot.yml` for weekly Go module, GitHub Actions, and Docker base image updates
- Status badges in `README.md` (CI, release, license, Go version, Go report card)
- `upstream.AsyncDoer` interface and `handlers.Pool` interface so router handlers can be unit-tested in isolation from the gnet/netpoll-backed `*Host`
- Unit tests covering all routing handlers (`DefRouteHandler`, `AllFastestRouteHandler`, `MissFailoverRouteHandler`, `LocalRouteHandler`, `OperationSelectorRouteHandler`) plus the `NewByPolicy` factory — 100% statement coverage of `internal/router/handlers`
- `examples/docker/test.sh` — host-side end-to-end test for the docker-compose example. Verifies that SET fans out to both memcaches, that stopping/restarting one memcache preserves writes and reads through the surviving node, and that the router converges back to two healthy hosts after recovery
- `gomcrouter_metrics_events_dropped_total{event_type=...}` counter — increments when the async metrics ring is full and an event would otherwise overwrite an unconsumed slot. Lets operators detect when the metrics consumer cannot keep up

### Fixed
- Build instructions in `CONTRIBUTING.md` now use the correct entry point (`go build .`)
- `machinery/pool.bufferPool.Put` no longer mutates the slice header of buffers whose capacity does not match a pooled size class. Previously the static error responses in `proto/ascii` (e.g. `VersionBuf`, `ErrProxyErrorResponseBuf`) were being silently extended to their underlying allocation capacity on first use, causing trailing uninitialized bytes to be appended to subsequent client responses
- `observability/metric.Collector` no longer overruns the async event ring when the consumer falls behind. `HandleRequestAsync` / `HandleUpstreamRequestAsync` check `SPSC.CanPush` before writing and increment the new dropped-events counter on overflow instead of silently overwriting an unread slot
- Metrics HTTP server now sets `ReadHeaderTimeout` (5s), `WriteTimeout` (30s) and `IdleTimeout` (60s). Closes gosec [G112](https://github.com/securego/gosec/blob/master/issues/slowloris.go) (Slowloris) — a slow client could previously keep a metrics connection open indefinitely by sending request headers byte-by-byte. Also switches the shutdown sentinel check to `errors.Is(err, http.ErrServerClosed)`
- Annotated the four FD-to-int32 conversions in `internal/upstream/netpoll/poller_poll.go` with `// #nosec G115` and a justification: file descriptors are bounded by `RLIMIT_NOFILE` and always fit in `int32` per the kernel `pollfd` ABI. Closes gosec G115 alerts on these lines without changing runtime behaviour

### Changed
- Removed the obsolete `version: "3.8"` attribute from `examples/docker/docker-compose.yml` (Compose v2+ ignores it and warns on each invocation)

---

## v1.3.3 — 2026-04-20

### Added
- Per-host `gomcrouter_upstream_get_hits_total` and `gomcrouter_upstream_get_misses_total` counters for `get`/`gets` responses
- `ascii.ClassifyGetResponse` classifier (hit / miss / unknown) and `UpstreamRequestEvent.HitStatus` field to carry the result through the async metrics ring

---

## v1.3.2 — 2026-02-20

### Added
- Event-loop based proxy core
- Pluggable routing engine and DefaultRoute
- Ordered upstream pool with health checks
- YAML router config and CLI tuning flags
- Prometheus metrics (requests, upstream, latency)
- Benchmark scenarios

### Changed
- Routing pipeline optimized for zero/minimal allocations
- Metric collection moved to allocation-free event structs
- Connection reuse and timeout handling improved

### Fixed
- Upstream failover edge cases
- Nil pointer crashes in upstream paths

### Performance
- Reduced allocations on hot path
- Buffer and metric event reuse

---

## Contributor Guide

Add entries under **Unreleased** using:
- Added / Changed / Fixed / Performance / Breaking
