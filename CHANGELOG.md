# Changelog

All notable changes to **gomcrouter** are documented in this file.  
Format follows Keep a Changelog and Semantic Versioning.

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
