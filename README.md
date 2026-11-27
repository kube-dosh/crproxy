# crproxy

[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

**English** | [简体中文](README.zh-CN.md)

crproxy is a lightweight, self-hosted OCI registry with multi-upstream pull-through caching. It ships as a single Go binary, uses a local data directory backed by SQLite, and does not require an external database, OIDC, or LDAP.

> **Project status:** pre-1.0. The OCI Distribution core is conformance-tested, but crproxy is intentionally a single-node product with documented security and scale boundaries. Review [Security and production notes](#security-and-production-notes) and [Limitations](#limitations) before production use.
>
> **License status:** Apache License 2.0. Commercial use, modification, and distribution are permitted under the license. No warranty, commercial support, or SLA is included unless separately agreed.

## Overview

| Area | What crproxy provides |
| --- | --- |
| Local registry | OCI Distribution Spec 1.1: manifests, indexes, artifacts, blobs, chunked and streamed uploads, cross-repository mount, catalog, tags, referrers, delete, Range, and conditional requests. |
| Proxy cache | Read-only pull-through cache for Docker Hub, registry.k8s.io, Quay, GHCR, and other OCI Distribution upstreams. Upstreams can be routed by path or Host. |
| Cache behavior | Manifest/tag TTL, ETag/304 revalidation, blob and token request coalescing, Range passthrough, and a shared content-addressed store across repositories and upstreams. |
| Authentication | Registry Basic auth, Bearer PAT, Docker Registry token flow (HS256 JWT), and optional anonymous actions. The management API uses HttpOnly cookie sessions with CSRF protection. |
| Administration | Embedded React 19 management console for repositories, cache, upstreams, users, storage, audit, metrics, and runtime settings. |
| Transport | Native TLS with atomic certificate reload, reverse-proxy awareness, HSTS, secure headers, and HTTP/HTTPS outbound proxy support. |
| Operations | Logical quotas, cache watermarks, LRU eviction, GC/fsck, upload recovery, audit events, Prometheus metrics, health/readiness probes, offline backup/restore, and master-key rotation. |

crproxy is **not** a drop-in Harbor replacement. It does not provide multi-tenant projects, repository-level ACLs, HA, object storage, replication, image scanning, signature policy, or external identity providers. See [Limitations](#limitations).

## Verified baseline

- OCI Distribution Spec 1.1 conformance: `850 passed / 4 optional skips / 0 failed / 0 error`.
- Go unit and integration tests, race detector, and `go vet` pass.
- The image-client acceptance suite uses Podman. Other clients may work, but are outside the current acceptance baseline.
- The reference deployment has been exercised with local push/pull, PAT pull, multi-upstream proxy pulls, cache hits, TLS certificate reload, outbound HTTP proxy, and a read-only container runtime.

See [docs/conformance-report.md](docs/conformance-report.md), [docs/acceptance-baseline.md](docs/acceptance-baseline.md), and [docs/podman-smoke.md](docs/podman-smoke.md).

## Quick start

### Prerequisites

- Go 1.24 or newer.
- Node.js 24 and npm for the embedded management console.
- Podman for the documented client smoke tests.
- Linux for the provided systemd unit, or a container runtime for the container images.

### Build and run

```bash
git clone https://github.com/kube-dosh/crproxy.git
cd crproxy

cp crproxy.example.yaml crproxy.yaml
# Edit crproxy.yaml before starting:
#   auth.bootstrap_admin.password: at least 12 characters
#   storage.root: a directory owned by the service user

make build
./bin/crproxy serve -config crproxy.yaml
```

The default listeners are `:5000` for the shared registry/management plane. Open `http://127.0.0.1:5000/` for the console. The bootstrap administrator is required to change the initial password before using the management API.

The default data directory is `./data`. The database is stored under `data/meta/crproxy.db`; the content-addressed store is under `data/cas`. `data/meta/jwt.secret` and `data/meta/master.key` are long-lived secrets and must be backed up with the database.

### Podman smoke test

```bash
# Local development only: the default configuration uses HTTP.
podman login --tls-verify=false -u admin 127.0.0.1:5000

# Proxy pull examples.
podman pull 127.0.0.1:5000/r/dockerhub/busybox:latest
podman pull 127.0.0.1:5000/r/ghcr/actions/actions-runner:latest
podman pull 127.0.0.1:5000/r/quay/prometheus/busybox:latest

# Local push and pull.
podman tag 127.0.0.1:5000/r/dockerhub/busybox:latest 127.0.0.1:5000/local/team/busybox:latest
podman push 127.0.0.1:5000/local/team/busybox:latest
podman pull 127.0.0.1:5000/local/team/busybox:latest
```

Do not use `--tls-verify=false` in production. Put crproxy behind a trusted TLS terminator or configure `server.tls.mode: file`.

## Configuration

`crproxy.example.yaml` is the canonical configuration reference. YAML and environment variables form the startup baseline. Selected settings can be changed at runtime from the management console and are persisted in SQLite until reset.

Key settings:

- `server.public_host` and `server.public_scheme`: the externally visible registry origin. They are used for Bearer realms, secure cookies, and generated URLs. They must match the client-facing address.
- `server.trusted_proxies`: CIDR/IP ranges whose `Forwarded` and `X-Forwarded-*` headers are trusted. Leave empty unless crproxy is behind a controlled reverse proxy.
- `server.tls.mode=file`: enables native TLS. Certificates are reloaded atomically; a failed reload keeps the previous certificate active.
- `storage.root`: `meta/`, `cas/`, `uploads/`, `tmp/`, and `trash/` must reside on the same filesystem.
- `storage.max_bytes` and `storage.cache_max_bytes`: total logical quota and proxy-cache quota. Local content is never evicted by cache LRU.
- `auth.registry.allow_anonymous` and `auth.registry.anonymous_actions`: anonymous access is disabled by default; grant only the actions that are explicitly required.
- `proxy.path_prefix`: the path prefix used for proxy routing. The default is `r`.

Settings such as listen addresses, public URL, trusted proxies, TLS mode, storage root, bootstrap administrator, and key file locations require a restart. Runtime sections exposed in the console support hot updates.

### Outbound HTTP/HTTPS proxy

```yaml
outbound_proxy:
  http_proxy: http://proxy.example.com:3128
  https_proxy: http://proxy.example.com:3128
  no_proxy: localhost,127.0.0.1,::1,.internal.example.com
  username: ""
  password: ""
```

HTTPS upstreams are reached through an HTTP `CONNECT` tunnel. If crproxy runs in a VM or container and the proxy runs on the host, do not use `127.0.0.1`; use an address reachable from the crproxy process. Standard `HTTP_PROXY`, `HTTPS_PROXY`, and `NO_PROXY` environment variables remain supported for container deployments.

For the complete configuration and deployment matrix, see [docs/deployment.md](docs/deployment.md).

## Security and production notes

- Terminate TLS before exposing the service to untrusted networks. Set `server.public_scheme: https` and configure `server.public_host` to the real client-facing hostname.
- Change the bootstrap administrator password immediately. On account compromise, rotate passwords **and revoke API tokens**; token revocation is a separate operation.
- Treat the management plane, including `/api/v1` and `/metrics`, as a trusted-network endpoint. Protect it with a reverse proxy, network policy, or an additional authentication layer if it is reachable from an untrusted network.
- Configure `trusted_proxies` only for proxies you control. Ensure the edge proxy overwrites client-supplied forwarding headers instead of blindly appending them.
- Keep `master.key`, `jwt.secret`, `crproxy.db`, and the CAS on encrypted storage where required by your security model. Back them up together; losing the master key makes stored upstream credentials unrecoverable.
- Run exactly one crproxy process per data directory. The process acquires a data-directory lock and does not support shared storage between instances.
- The current authorization model is global `admin`/`operator`/`viewer` plus PAT action scopes. **Repository-level ACLs are not implemented.** Deploy the current version only where operators share a trust boundary, or add network-level isolation between teams.
- Backups are offline operations: stop the service before running `crproxy backup` or `crproxy restore`. Test restore procedures in a clean environment before relying on them.

Report vulnerabilities through the process in [SECURITY.md](SECURITY.md). Do not disclose security details in a public issue before a fix is available.

## Operations

```bash
bin/crproxy backup -config /etc/crproxy/crproxy.yaml -output /backup/crproxy.tar.gz
bin/crproxy restore -config /etc/crproxy/crproxy.yaml -input /backup/crproxy.tar.gz --force
bin/crproxy rotate-master-key -config /etc/crproxy/crproxy.yaml
bin/crproxy healthcheck -url http://127.0.0.1:5000/readyz
```

Endpoints:

| Endpoint | Purpose |
| --- | --- |
| `GET /healthz` | Process liveness. |
| `GET /readyz` | SQLite and storage readiness. |
| `GET /api/v1/healthz` | Legacy management-plane liveness path. |
| `GET /api/v1/readyz` | Legacy management-plane readiness path. |
| `GET /metrics` | Prometheus metrics. Intended for a trusted monitoring network. |

When `server.admin.listen` is different from `server.listen`, the registry listener serves only the OCI Distribution API, while the management listener serves the console, `/api/v1`, `/metrics`, and probe endpoints. When no separate management address is configured, both planes share the registry listener.

## Limitations

- Single instance with local block storage. Object storage, multi-instance shared storage, distributed locking, and cross-region replication are not supported.
- No OIDC, LDAP, or SAML integration; accounts are local.
- No Docker schema1 support.
- No repository-level ACL or project/tenant model.
- No vulnerability scanning, signature verification policy, image replication, or admission policy.
- Proxy routes are strictly read-only. Write requests to proxy routes return `405` and are never forwarded upstream.
- Offline backup only; no online snapshot or point-in-time recovery.
- No built-in high availability, automated failover, or commercial support SLA.

## Documentation

- [Design and architecture](DESIGN.md) (Chinese)
- [Registry protocol design](docs/registry-protocol-design.md) (Chinese)
- [Authentication and TLS design](docs/auth-design.md) (Chinese)
- [Storage and cache design](docs/storage-design.md) (Chinese)
- [Frontend design](docs/frontend-design.md) (Chinese)
- [Deployment and operations](docs/deployment.md) (Chinese)
- [Release acceptance baseline](docs/acceptance-baseline.md) (Chinese)
- [Commercial release checklist](docs/release-checklist.md)
- [OCI conformance report](docs/conformance-report.md)
- [Podman smoke test](docs/podman-smoke.md)
- [OpenAPI document](docs/openapi.yaml)

English translations of the detailed design documents are planned. The English README and the Chinese README are maintained as equivalent project entry points; if they diverge, the English README is authoritative for build and licensing information.

## Contributing

Contributions are welcome under the terms of the Apache License 2.0. Read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request. All commits must include a Developer Certificate of Origin sign-off.

## License

Copyright 2026 The crproxy Authors.

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

Commercial use is permitted by the license. Redistributions must preserve the license and applicable notices. Third-party components remain subject to their own licenses. The software is provided on an “AS IS” basis without warranties or conditions of any kind.

Published container images include `LICENSE` and `NOTICE` under `/licenses`.
