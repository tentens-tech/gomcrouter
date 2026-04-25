<!--
Thanks for contributing to gomcrouter! Please read CONTRIBUTING.md before
submitting. Keep PRs small and focused — one logical change per PR.
-->

## What changed

<!-- Brief description of the change. -->

## Why

<!-- Motivation. Link related issue(s): closes #123 -->

## Type of change

- [ ] Bug fix
- [ ] New feature
- [ ] Performance improvement
- [ ] Refactor (no behavior change)
- [ ] Documentation
- [ ] Build / CI / tooling
- [ ] Breaking change

## Affected area

- [ ] Protocol (ASCII / Binary / Meta)
- [ ] Routing strategy / handlers
- [ ] Upstream pool / health checks
- [ ] IO engine / netpoll / socket
- [ ] Configuration
- [ ] Metrics / observability
- [ ] CLI / runtime flags
- [ ] Documentation only

## Performance impact

<!--
For changes touching protocol decode, routing, upstream IO, event loop
callbacks, or buffer handling, include before/after benchmark numbers
(ns/op, allocs/op) and a short description of the scenario.
Write "N/A" only if the change does not affect a hot path.
-->

## Config impact

<!-- Are existing router.yaml configs still valid? Any new flags? -->

## Checklist

- [ ] Tests added or updated
- [ ] Edge cases covered
- [ ] Linter passes (`golangci-lint run`)
- [ ] Tests pass with race detector (`go test -race ./...`)
- [ ] Benchmarks included for hot-path changes
- [ ] Docs updated (`README.md`, `CHANGELOG.md` under `Unreleased`)
- [ ] No new allocations on hot paths (or justified)
