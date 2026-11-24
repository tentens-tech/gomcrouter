# Security Policy

## Supported Versions

Security fixes are applied to the most recent minor release line. Please make
sure you are running a current version before reporting an issue.

| Version | Supported |
|---------|-----------|
| 1.3.x   | ✅        |
| < 1.3   | ❌        |

## Reporting a Vulnerability

**Please do not open public GitHub issues for security vulnerabilities.**

We accept private reports through two channels:

1. **GitHub Security Advisories (preferred)** — open a private report at
   <https://github.com/tentens-tech/gomcrouter/security/advisories/new>.
2. **Email** — `slava.lucheninov@tentens.tech`.

Please include:

- A description of the issue and its impact
- Steps to reproduce or a minimal proof of concept
- Affected version, commit hash, or release tag
- Any suggested mitigation, if known

### What to expect

- We aim to acknowledge reports within **5 business days**.
- We work with reporters on a coordinated disclosure timeline, typically up to
  90 days from the initial report.
- Fixes are released as patch versions and credited in `CHANGELOG.md` if the
  reporter wishes to be named.

## Security Considerations

### Network exposure

- The memcached text protocol has no authentication or encryption. Anyone who can reach the router listen address
  (`--listen`, default `tcp://:8080`) can read, write and delete keys on every upstream. Expose it only inside a
  trusted network and restrict access with firewall rules or Kubernetes network policies.
- The metrics endpoint (`--metrics-listen-addr`, default `:9090`) is served without authentication and exposes
  upstream host names and traffic counters. Bind it to an internal interface when it isn't scraped from outside.
- Traffic to the upstream memcached nodes isn't encrypted either; keep them on a private network.

### Runtime

- The image runs as an unprivileged user (UID/GID `10001`), the binary is owned by root and read-only, and
  gomcrouter doesn't write to disk, so it works with `readOnlyRootFilesystem: true`,
  `allowPrivilegeEscalation: false` and all capabilities dropped.
- The router configuration file is trusted input: keep it read-only for the process.

### For contributors

- Dependabot keeps Go modules, GitHub Actions (pinned to commit SHAs) and Docker base images up to date.
- CI runs golangci-lint, gosec, govulncheck and Trivy on every code change; govulncheck fails the build when
  reachable code is affected by a known vulnerability.

Thank you for helping keep `gomcrouter` and its users safe.
