# Changelog

All notable changes to this project will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project intends to follow [Semantic Versioning](https://semver.org/) once tagged releases begin. There is currently no tagged public release; version numbers remain pre-1.0.

## [Unreleased]

### Added

- Complete local OCI Distribution Spec 1.1 registry surface, including manifests, indexes, artifacts, blobs, uploads, mounts, tags, catalog, referrers, deletes, Range requests, and conditional requests.
- Read-only multi-upstream pull-through cache with path and Host routing, shared CAS, TTL revalidation, and request coalescing.
- Basic, Bearer PAT, Docker Registry token flow, and configurable anonymous registry actions.
- Cookie-session management API with CSRF protection and a separate OCI Registry plane.
- Embedded React management console for repositories, cache, upstreams, users, audit, storage, and runtime settings.
- Native TLS with atomic certificate reload, trusted reverse-proxy handling, HSTS, and outbound HTTP/HTTPS proxy support.
- Logical storage quotas, cache watermarks, LRU eviction, GC, fsck, upload recovery, offline backup/restore, and master-key rotation.
- Prometheus metrics, health/readiness probes, OCI conformance evidence, and Podman smoke-test documentation.

### Security

- Apache License 2.0, NOTICE, security-reporting policy, contribution policy, and code of conduct documentation added.
- Documented single-node, authorization, management-plane, backup, and high-availability boundaries for production operators.

### Known limitations

- No repository-level ACL or multi-tenant project model.
- No HA, object storage, online backup, image replication, vulnerability scanning, or external identity provider integration.
- The management plane, including `/metrics`, is intended for a trusted network.
