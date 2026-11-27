# Contributing to crproxy

Thank you for contributing. crproxy is licensed under the Apache License 2.0. By submitting a contribution, you agree that your contribution is licensed under the same terms.

## Developer Certificate of Origin

All commits must be signed off under the [Developer Certificate of Origin 1.1](https://developercertificate.org/). Add a sign-off line to each commit:

```text
Signed-off-by: Your Name <you@example.com>
```

Use `git commit -s` to add it automatically. The sign-off certifies that you have the right to submit the contribution under the project license. Do not add a sign-off for another person.

## Before opening a pull request

- Search existing issues and pull requests first.
- Keep changes focused. Do not mix refactoring, formatting, and behavior changes without a clear reason.
- Do not include secrets, production hostnames, credentials, tokens, customer data, or private keys.
- For security issues, follow [SECURITY.md](SECURITY.md) instead of opening a public pull request.
- For user-visible behavior, update the relevant documentation. Keep `README.md` and `README.zh-CN.md` aligned.
- For API changes, update `docs/openapi.yaml` and the management console together.

## Development checks

Run the full verification suite before requesting review:

```bash
make verify
```

At minimum, run the checks relevant to your change:

```bash
go test ./...
go vet ./...
cd web && npm run typecheck && npm test && npm run build
```

The repository's image-client acceptance tests use Podman. If your change affects registry behavior, include a Podman reproduction or smoke-test result. Other clients are welcome for compatibility reports but do not replace the required Podman acceptance path.

## Code and review expectations

- Preserve the OCI Distribution semantics and error codes covered by the conformance and acceptance baselines.
- Keep the proxy plane strictly read-only. Do not add upstream write forwarding.
- Preserve the separation between the management API and the Registry API.
- Prefer small, well-tested changes over broad rewrites.
- Add tests for behavior changes. Bug fixes should include a regression test when practical.
- Do not weaken TLS verification, CSRF protection, session handling, or token scope checks without an explicit security review.

## Commit messages

Use clear, imperative commit subjects. A scope prefix is welcome but not required:

```text
registry: reject incompatible manifest references
docs: clarify backup and restore requirements
web: keep repository filters after navigation
```

## Documentation and translation

English is the default repository language. `README.md` is the authoritative entry point; `README.zh-CN.md` must remain equivalent. Detailed design documents are currently maintained in Chinese, and English translations are welcome.

## Release policy

The project does not yet have a tagged 1.0 release. Release notes must identify:

- Source commit and build version.
- Supported upgrade path and any database migration.
- Security-relevant changes.
- Conformance and Podman acceptance evidence.
- Known limitations and rollback instructions.

## Questions

Use the repository issue tracker for non-security questions about development. For deployment support, include the crproxy version, configuration shape with secrets removed, and relevant logs.
