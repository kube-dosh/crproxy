# Security Policy

## Supported versions

crproxy has not yet published a tagged 1.0 release. Security fixes are applied to the `main` branch. Once versioned releases begin, this section will identify the supported release lines and the expected backport window.

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability. Public disclosure before a fix is available can put users at risk.

Use a private reporting channel:

1. If the repository hosting platform provides private security advisories, open one for this repository.
2. Otherwise, contact the maintainers through the private contact method listed on their repository profile and request a private channel.
3. If no private channel is available, open a minimal public issue asking the maintainers to establish one. Do not include reproduction steps, credentials, affected hosts, or exploit details in that issue.

Please include:

- Affected commit, version, or container image digest.
- Deployment mode: shared port, split ports, native TLS, or reverse proxy.
- A concise description of the impact and the required preconditions.
- Reproduction steps or a minimal proof of concept.
- Relevant logs with secrets, tokens, cookies, and customer data removed.

Maintainers will acknowledge a complete report as soon as practical, assess severity, and coordinate a fix and disclosure timeline. Response times are best-effort and are not an SLA unless a separate support agreement exists.

## Research guidelines

Good-faith research should avoid:

- Accessing, modifying, or deleting data that does not belong to you.
- Disrupting production services or other users.
- Social engineering, phishing, or physical attacks.
- Testing against systems without explicit authorization.

Do not include live credentials, personal data, or customer content in reports. If a proof of concept requires sensitive data, use synthetic data and describe the test environment.

## Security boundaries

Operators should understand the following current boundaries before deployment:

- The management plane, including `/metrics`, is intended for a trusted network and does not provide application-level authentication for Prometheus scraping.
- Authorization is global by role plus PAT action scope. Repository-level ACLs and tenant isolation are not implemented.
- A data directory must be used by exactly one crproxy process.
- Backups are offline operations and require stopping the service.
- The project does not provide HA or automatic failover.

See [README.md](README.md) and [docs/deployment.md](docs/deployment.md) for deployment hardening guidance.
