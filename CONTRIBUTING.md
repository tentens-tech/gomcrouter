# Contributing to gomcrouter

Thanks for your interest in contributing to **gomcrouter** 🚀  
This project is a high-performance Memcached protocol router focused on low latency, high throughput, and minimal allocations. Please read this guide before submitting changes.

---

# Philosophy

gomcrouter is performance-first and correctness-first.

Key principles:

- Low latency and high RPS are primary goals
- Avoid allocations on hot paths
- No goroutine-per-request designs
- Prefer explicit, simple, measurable logic over abstraction layers
- Benchmarks and measurements matter more than assumptions

If a change affects routing, parsing, IO, pools, or upstream — it must include benchmarks.

---

# Where to Start

We especially welcome contributors in these areas:

## ✅ Contribution Tracks

### 1. Protocol Track — Meta + Binary Protocol Support

Goal: Add support for Memcached Meta and Binary protocols.

Start here:

- `proto/` package
- Existing ASCII protocol implementation as reference
- Add new packages:
    - `proto/meta`
    - `proto/binary`

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

### 2. Routing Track — New Route Implementations

Goal: Add new routing strategies.

Start here:

- `router/` and `router/handlers/` package

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

# Development Setup

Requirements:

- Go (see go.mod for version)
- Docker (for integration tests)
- memtier_benchmark (for load testing)

Build:

```bash
go build -o gomcrouter .
```

Run tests:

```bash
go test ./...
```

Run benchmarks:

```bash
go test -bench . -benchmem ./...
```

---

# Code Style

- Always check your code with linter
- Keep functions small and explicit
- Prefer concrete types in critical loops
- Avoid hidden allocations
- Reuse buffers and structs via pools where appropriate

---

# Performance Rules (Hot Path)

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

# Benchmarks & Profiling

For performance-sensitive PRs include:

- before/after benchmark numbers
- allocs/op
- ns/op
- short description of benchmark scenario

**pprof** or **flamegraph** screenshots are welcome for major changes.

---

# Pull Request Process

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

---

# Architecture Changes

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

# Metrics & Observability

If adding metrics:

- Follow existing naming style
- Avoid high-cardinality labels
- Do not rename existing metrics without discussion
- Histograms must justify bucket choice
- Metric event structs must remain allocation-free
- Only use primitive numeric fields (uint, int, bool)
- Do not add maps, slices, strings, pointers, or interfaces to metric event structs

---

# Testing Requirements

All PRs must include:

- Unit tests for new logic
- Edge-case tests
- Parser changes → fuzz tests encouraged
- Protocol changes → golden tests required

---

# Bug Reports

Please include:

- config
- logs
- gomcrouter version/commit
- reproduction steps
- traffic pattern if relevant

---

# Feature Requests

Open an issue describing:

- use case
- expected behavior
- workload pattern
- routing or protocol needs

---

# What Will Likely Be Rejected

- Performance regressions without measurement
- Large PRs without prior discussion
- Allocation-heavy abstractions in hot path
- Breaking config changes without migration notes
- Protocol changes without tests

---

Thank you for helping make gomcrouter faster and better 💚
