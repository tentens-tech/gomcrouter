# Changelog

All notable changes to **gomcrouter** are documented in this file.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [Semantic Versioning](https://semver.org/).

---

## Unreleased

---

## v1.4.2 — 2026-11-27

### Fixed

- `LICENSE` had a word that differs from the Apache License 2.0 text, so pkg.go.dev didn't recognize the license and
  hid the README and the documentation

### Added

- Package documentation for the `gomcrouter` command (shown on pkg.go.dev)

---

## v1.4.1 — 2026-11-25

### Fixed

- Replies to pipelined requests could come back out of order, so a client could get the value of another key: requests
  of one connection are spread over the upstream sockets and hosts. Replies are now written in the order of the requests
- `quit` was proxied to memcached, which closed the upstream connection shared by other clients and failed their
  in-flight requests. The router now closes the client connection itself, after the replies to the earlier requests
- A malformed request closed the connection before the replies to the earlier pipelined requests were sent
- Replies to a slow client were cut off when the connection was closed after `quit` or a malformed request; the
  connection is now closed only once its replies are sent, or when the client reads nothing for 30 seconds
- SIGTERM (`docker stop`, Kubernetes) killed the router without a graceful shutdown; it's handled like SIGINT
- `--log-level debug` had no effect
- A storage command or a VALUE reply declaring a length close to the maximum integer crashed the router
- A VALUE reply without a length was accepted; a `stats` reply followed by another reply or split across reads broke
  the upstream connection
- Requests to an upstream that doesn't answer never timed out while the router had no other traffic
- On macOS a connection closed by memcached failed the in-flight requests with a nil error (crash in the route handler)
- A request or a reply larger than `--buffer-size` blocked the upstream connection forever, it now fails; a run of
  such requests no longer delays the requests queued after it
- A socket replaced while handling a read could get a write callback meant for the old one
- Removing an upstream that shares its address with another one could remove the wrong one; one in three hosts was
  skipped when the upstream poller had to be recreated
- Metric events from several event loops were lost and raced; the request and upstream error metrics could get a
  wrong `cmd` label
- Data races on shutdown of the metrics server and the gnet server, and a stop before the start left them running
- The router config is checked before the metrics server starts, so a config error doesn't leave it listening

### Added

- Unit tests for every package (coverage 94-100%), with fake memcached servers for the upstream and server tests
- End-to-end suites in docker (`test/e2e/run.sh`, `make e2e`): image, flags, protocol, every route type, the example
  config, failover, a late upstream, DNS changes, metrics, load with pipelining and signals. See
  [test/README.md](test/README.md)

### Changed

- `make e2e` runs the e2e suites, the docker-compose example moved to `make example`; CI runs both

---

## v1.4.0 — 2026-11-24

### Added

- `--version` / `-v` flag
- `Makefile` with `build`, `test-race`, `cover`, `lint`, `vuln`, `sec`, `e2e`, `docker-build` and `release-tag` targets
- CI: `Detect Changes` job (documentation-only PRs skip the code jobs), a single `CI Result` check for the branch ruleset,
  linux/amd64 + linux/arm64 builds, image verification, the docker-compose end-to-end test, `govulncheck`,
  gofmt / `go mod tidy` / shellcheck checks
- `Documentation` workflow (markdownlint and link check) and `Stale Issues and PRs` workflow
- `codecov.yml`, `.markdownlint.json`, `.lycheeignore`, `.dockerignore`, `.pre-commit-config.yaml`
- OCI labels on the Docker image

### Changed

- Go 1.27 for CI, releases and the image; `go.mod` requires Go 1.26
- Updated all Go dependencies (gnet v2.10.0, prometheus/client_golang v1.25.0, cobra v1.10.2, viper v1.21.0,
  zap v1.28.0, testify v1.12.1, golang.org/x/sys v0.50.0, golang.org/x/text v0.44.0 and others)
- Docker image: `golang:1.27-alpine` builder, `alpine:3.24` runtime pinned instead of `alpine:latest`,
  `-trimpath -s -w` build, `go mod verify`, root-owned read-only binary
- Release workflow: least-privilege permissions per job, the tag is validated and passed through env vars,
  `force` defaults to `false` and replaces a release only after the build succeeded, prereleases don't move `:latest`
- All GitHub Actions are pinned to commit SHAs (updated by Dependabot), checkouts don't persist credentials
- `version.Version` defaults to `dev`; releases set it from the tag
- CODEOWNERS: every pattern lists all its owners (a later `*` line used to override the first one)
- `SECURITY.md`: vulnerabilities are reported only through GitHub Security Advisories;
  `CODE_OF_CONDUCT.md` contact is `slucheninov@gmail.com`

### Fixed

- `README.md`: the Docker example passed flags without the binary, so the container failed to start
- Trivy action was referenced by the mutable `master` branch
- Security: updated `golang.org/x/text` (GO-2026-5970, GO-2026-6629) and `golang.org/x/sys` (GO-2026-5024);
  govulncheck found no vulnerable code paths, the modules were flagged by dependency scanners
- `errors.Is` for `EINPROGRESS`/`EALREADY` checks in the socket dialer; typo in the `upstream_errors_total` help text

---

## v1.3.4 — 2026-04-27

### Added

- `SECURITY.md` with private vulnerability disclosure policy
- `CODE_OF_CONDUCT.md` adopting Contributor Covenant 2.1
- GitHub issue templates (bug report, feature request) and pull request template
- `dependabot.yml` for weekly Go module, GitHub Actions, and Docker base image updates
- Status badges in `README.md` (CI, release, license, Go version, Go report card)
- `upstream.AsyncDoer` interface and `handlers.Pool` interface so router handlers can be unit-tested in isolation from the gnet/netpoll-backed `*Host`. The cached `OrderedPool.healthyHostsView` is published via `atomic.Pointer[[]AsyncDoer]` so request handlers can read it lock-free on the hot path while writers (health-check transitions) update it under the existing mutex — no slice-header data race
- Unit tests covering all routing handlers (`DefRouteHandler`, `AllFastestRouteHandler`, `MissFailoverRouteHandler`, `LocalRouteHandler`, `OperationSelectorRouteHandler`) plus the `NewByPolicy` factory — 100% statement coverage of `internal/router/handlers`
- `examples/docker/test.sh` — host-side end-to-end test for the docker-compose example. Verifies that SET fans out to both memcaches, that stopping/restarting one memcache preserves writes and reads through the surviving node, and that the router converges back to two healthy hosts after recovery
- `gomcrouter_metrics_events_dropped_total{event_type=...}` counter — increments when the async metrics ring is full and an event would otherwise overwrite an unconsumed slot. Lets operators detect when the metrics consumer cannot keep up

### Fixed

- Build instructions in `CONTRIBUTING.md` now use the correct entry point (`go build .`)
- `machinery/pool.bufferPool.Put` no longer mutates the slice header of buffers whose capacity does not match a pooled size class. Previously the static error responses in `proto/ascii` (e.g. `VersionBuf`, `ErrProxyErrorResponseBuf`) were being silently extended to their underlying allocation capacity on first use, causing trailing uninitialized bytes to be appended to subsequent client responses
- `observability/metric.Collector` no longer overruns the async event ring when the consumer falls behind. `HandleRequestAsync` / `HandleUpstreamRequestAsync` check `SPSC.CanPush` before writing and increment the new dropped-events counter on overflow instead of silently overwriting an unread slot
- Metrics HTTP server now sets `ReadHeaderTimeout` (5s), `WriteTimeout` (30s) and `IdleTimeout` (60s). Closes gosec [G112](https://github.com/securego/gosec#available-rules) (Slowloris) — a slow client could previously keep a metrics connection open indefinitely by sending request headers byte-by-byte. Also switches the shutdown sentinel check to `errors.Is(err, http.ErrServerClosed)`
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
