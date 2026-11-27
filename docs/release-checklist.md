# Release Checklist

This checklist defines the minimum evidence for publishing a crproxy source or binary release. A release should not be published while any applicable gate is unresolved.

## Source and version

- [ ] The working tree is clean and the release commit is recorded.
- [ ] The release is created from a signed annotated tag using Semantic Versioning.
- [ ] The tagged tree builds without uncommitted generated files.
- [ ] `CHANGELOG.md` contains the release version, date, and user-visible changes.
- [ ] The version injected into the binary matches the tag.

## Legal and licensing

- [ ] `LICENSE` is present and unmodified Apache License 2.0 text.
- [ ] `NOTICE` identifies the project copyright holder and trademark boundary.
- [ ] Third-party license texts required by the dependency graph are included in the release artifact or an accompanying legal bundle.
- [ ] Container images and binary packages include `LICENSE` and `NOTICE` in a stable, discoverable path.
- [ ] The exact dependency graph is recorded from `go.sum` and `web/package-lock.json`.
- [ ] Redistributions do not imply trademark rights beyond customary use.

## Quality and protocol conformance

- [ ] `make verify` passes: Go tests, race detector, `go vet`, frontend typecheck, frontend tests, and frontend build.
- [ ] OCI Distribution Spec 1.1 conformance has zero failures and zero errors.
- [ ] Podman acceptance covers login, pull, push, delete behavior, PAT pull, proxy pull, cache hit, and proxy write rejection.
- [ ] The backup/restore round trip and master-key rotation have been exercised on the release binary.
- [ ] Any database migration has an upgrade test and a documented rollback boundary.

## Security

- [ ] Dependency and container vulnerability scans have no unmitigated critical or high findings.
- [ ] Security-relevant changes are listed in the release notes without publishing exploit details before a fix is available.
- [ ] TLS configuration, cookie security, CSRF behavior, trusted-proxy handling, and token scopes have been reviewed for regressions.
- [ ] No secrets, production hostnames, tokens, private keys, or customer data are present in the source, build context, or release artifact.
- [ ] Known security boundaries and limitations are reflected in `README.md`, `README.zh-CN.md`, and `SECURITY.md`.

## Supply chain and artifacts

- [ ] A software bill of materials (SBOM) is generated for the source or binary artifact.
- [ ] Release checksums are generated and published.
- [ ] Checksums, container images, and release metadata are signed.
- [ ] The build process and required toolchain versions are documented.
- [ ] The container image is pinned by digest in deployment examples where appropriate.
- [ ] Multi-architecture artifacts, if published, are tested on the advertised platforms.

## Deployment and rollback

- [ ] The systemd unit, container image, and Compose example are tested with the release artifact.
- [ ] Upgrade from the previous supported release is tested on a copy of production-shaped data.
- [ ] Rollback instructions are tested, including the database compatibility boundary.
- [ ] `README.md` and `README.zh-CN.md` are aligned and contain the correct version-specific instructions.
- [ ] The release notes identify known limitations, support scope, and whether any support or SLA commitment applies.
