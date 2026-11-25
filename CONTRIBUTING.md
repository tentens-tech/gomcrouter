# Contributing to gomcrouter

Thanks for your interest in contributing to **gomcrouter** 🚀
This project is a high-performance Memcached protocol router focused on low latency, high throughput, and minimal allocations. Please read this guide before submitting changes.

This project and everyone participating in it is governed by our [Code of Conduct](CODE_OF_CONDUCT.md).

---

## Philosophy

gomcrouter is performance-first and correctness-first.

Key principles:

- Low latency and high RPS are primary goals
- Avoid allocations on hot paths
- No goroutine-per-request designs
- Prefer explicit, simple, measurable logic over abstraction layers
- Benchmarks and measurements matter more than assumptions

If a change affects routing, parsing, IO, pools, or upstream — it must include benchmarks.

---

## Where to Start

We especially welcome contributors in these areas:

### ✅ Contribution Tracks

#### 1. Protocol Track — Meta + Binary Protocol Support

Goal: Add support for Memcached Meta and Binary protocols.

Start here:

- `internal/proto/` package
- Existing ASCII protocol implementation (`internal/proto/ascii`) as reference
- Add new packages:
  - `internal/proto/meta`
  - `internal/proto/binary`

Guidelines:

- Keep parser isolated from networking
- Follow streaming decode model (handle partial frames)
- Match existing internal request representation
- Add golden tests from real protocol examples

PR must include:

- Protocol conformance tests
- Partial frame handling tests
- Benchmarks (ns/op, allocs/op)

Do not modify event loop code for first protocol PR — parser first, integration later.

#### 2. Routing Track — New Route Implementations

Goal: Add new routing strategies.

Start here:

- `internal/router/` and `internal/router/handlers/` packages

Examples:

- New failover strategies
- Latency-aware routing
- Op-aware routing
- Replica selection policies

Guidelines:

- Routing must be deterministic
- Must be concurrency-safe
- Avoid allocations in per-request route selection
- Include metrics hooks

PR must include:

- Unit tests
- Route behavior documentation
- Benchmarks

Avoid modifying core router engine for first PR — add new route type first.

---

## Development Setup

Requirements:

- Go (the version from [go.mod](go.mod); CI builds with the latest Go 1.27 release)
- Docker (for the image and the end-to-end test)
- [golangci-lint](https://golangci-lint.run/) v2
- memtier_benchmark (for load testing)
- Optional: [pre-commit](https://pre-commit.com/) — `pre-commit install`

`make help` lists all targets:

```bash
make build        # build ./gomcrouter with the version from git describe
make test-race    # unit tests with the race detector
make cover        # unit tests with coverage, writes coverage.out
make lint         # golangci-lint
make vuln         # govulncheck
make sec          # gosec
make e2e          # end-to-end suites in docker (make e2e SUITES="routes failover")
make example      # docker-compose example + examples/docker/test.sh
make docker-build # build the image
```

How the tests are organised and what each e2e suite covers: [test/README.md](test/README.md).

Run benchmarks:

```bash
go test -run '^$' -bench . -benchmem ./...
```

---

## Code Style

- Always check your code with linter
- Keep functions small and explicit
- Prefer concrete types in critical loops
- Avoid hidden allocations
- Reuse buffers and structs via pools where appropriate

---

## Performance Rules (Hot Path)

Hot path includes:

- protocol decode
- routing selection
- upstream write/read
- event loop callbacks
- buffer handling

Rules:

- No unnecessary allocations
- No logging in hot path
- No goroutine spawning per request
- No maps / interfaces / type assertions / reflection in hot paths to minimise runtime usage
- Changes must be benchmarked

If unsure whether your change touches hot path — assume it does and benchmark.

---

## Benchmarks & Profiling

For performance-sensitive PRs include:

- before/after benchmark numbers
- allocs/op
- ns/op
- short description of benchmark scenario

**pprof** or **flamegraph** screenshots are welcome for major changes.

---

## Pull Request Process

- Fork and create a feature branch
- Keep PRs small and focused
- One logical change per PR
- Include motivation and impact description
- Link related issues

PR description should include:

- What changed
- Why it changed
- Performance impact
- Config impact (if any)

Large refactors require opening an issue first.

Before you open a PR, make sure that `make lint test-race` pass, the code is `gofmt`-ed and `go mod tidy` doesn't
change `go.mod`/`go.sum` — CI checks all of them. Add an entry to `CHANGELOG.md` under **Unreleased**.

Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/): `type(scope): short description`,
where type is `feat`, `fix`, `perf`, `docs`, `refactor`, `test`, `ci` or `chore`.

PRs that only change documentation skip the code jobs of CI; the `CI Result` check is the one required for merging.

---

## Architecture Changes

For changes affecting:

- event loop
- poller
- socket layer
- upstream IO
- core router flow

You must:

- Open a design issue first
- Describe approach
- Explain performance impact
- Provide diagrams if possible

---

## Metrics & Observability

If adding metrics:

- Follow existing naming style
- Avoid high-cardinality labels
- Do not rename existing metrics without discussion
- Histograms must justify bucket choice
- Metric event structs must remain allocation-free
- Only use primitive numeric fields (uint, int, bool)
- Do not add maps, slices, strings, pointers, or interfaces to metric event structs

---

## Testing Requirements

All PRs must include:

- Unit tests for new logic
- Edge-case tests
- Parser changes → fuzz tests encouraged
- Protocol changes → golden tests required

---

## Bug Reports

Please include:

- config
- logs
- gomcrouter version/commit
- reproduction steps
- traffic pattern if relevant

---

## Feature Requests

Open an issue describing:

- use case
- expected behavior
- workload pattern
- routing or protocol needs

---

## What Will Likely Be Rejected

- Performance regressions without measurement
- Large PRs without prior discussion
- Allocation-heavy abstractions in hot path
- Breaking config changes without migration notes
- Protocol changes without tests

---

## Releases

Releases are made by maintainers. Pushing a `vX.Y.Z` tag runs the [release workflow](.github/workflows/release.yml), which:

- runs the unit tests;
- builds linux/amd64 and linux/arm64 binaries and attaches them with `checksums.txt` to a GitHub release;
- pushes a multi-arch image to `ghcr.io/tentens-tech/gomcrouter:vX.Y.Z` (and `:latest` for non-prerelease tags).

```bash
make release-tag TAG=vX.Y.Z
```

Tags with a suffix, such as `v1.4.0-rc.1`, are published as prereleases. The workflow can also be started manually
from the Actions tab for an existing tag; `force` replaces an existing release.

Move the **Unreleased** section of `CHANGELOG.md` under the new version before tagging.

---

Thank you for helping make gomcrouter faster and better 💚
