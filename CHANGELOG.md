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

### Fixed
- Build instructions in `CONTRIBUTING.md` now use the correct entry point (`go build .`)
- `machinery/pool.bufferPool.Put` no longer mutates the slice header of buffers whose capacity does not match a pooled size class. Previously the static error responses in `proto/ascii` (e.g. `VersionBuf`, `ErrProxyErrorResponseBuf`) were being silently extended to their underlying allocation capacity on first use, causing trailing uninitialized bytes to be appended to subsequent client responses

---

## v1.3.3

### Added
- Per-host `gomcrouter_upstream_get_hits_total` and `gomcrouter_upstream_get_misses_total` counters for `get`/`gets` responses
- `ascii.ClassifyGetResponse` classifier (hit / miss / unknown) and `UpstreamRequestEvent.HitStatus` field to carry the result through the async metrics ring

---

## v1.3.2

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
