# Security Policy

## Supported Versions

We release patches for security vulnerabilities for the latest minor version:

| Version | Supported          |
| ------- | ------------------ |
| 1.4.x   | :white_check_mark: |
| < 1.4   | :x:                |

## Reporting a Vulnerability

We take security bugs seriously. We appreciate your efforts to responsibly disclose your findings, and will make every effort to acknowledge your contributions.

### How to Report

**Please do not report security vulnerabilities through public GitHub issues.**

Instead, report them privately through GitHub:
[**Report a vulnerability**](https://github.com/tentens-tech/gomcrouter/security/advisories/new)
(the "Security" tab of the repository → "Report a vulnerability").

Please include the following information in your report:

- **Type of issue** (e.g., request smuggling, memory corruption, denial of service, information disclosure)
- **Full paths of source file(s)** related to the manifestation of the issue
- **Location of the affected source code** (tag/branch/commit or direct URL)
- **Any special configuration required** to reproduce the issue (router config, flags)
- **Step-by-step instructions** to reproduce the issue
- **Proof-of-concept or exploit code** (if possible)
- **Impact of the issue**, including how an attacker might exploit the issue

### What to Expect

- **Acknowledgment**: We will acknowledge receipt of your vulnerability report within 48 hours.
- **Communication**: We will keep you informed about our progress throughout the process.
- **Resolution**: We aim to resolve critical issues within 90 days of the initial report.
- **Credit**: If you would like, we will credit you in any public disclosure of the vulnerability.

## Security Considerations

### Network Exposure

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

## Security Best Practices

### For Operators

1. **Keep Updated**: Regularly update to the latest version
2. **Network Policies**: Restrict access to the router and metrics ports
3. **Least Privilege**: Run as non-root with `readOnlyRootFilesystem`, `allowPrivilegeEscalation: false` and dropped capabilities
4. **Private Network**: Keep the router and the memcached upstreams off the public internet

### For Contributors

1. **Dependency Scanning**: Dependabot keeps Go modules, GitHub Actions (pinned to commit SHAs) and Docker base images up to date
2. **Code Review**: All changes require code review before merging
3. **Security Testing**: Include security-focused tests for new features
4. **Static Analysis**: golangci-lint, gosec, govulncheck and Trivy run as part of CI; govulncheck fails the build when reachable code is affected by a known vulnerability

## Security Updates

Security updates will be released as patch versions and announced via:

- GitHub Security Advisories
- Release notes

## Attribution

We would like to thank the following individuals for responsibly disclosing security issues:

*None yet - be the first!*
