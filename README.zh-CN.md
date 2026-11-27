# crproxy

[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

[English](README.md) | **简体中文**

crproxy 是一个轻量级、自托管的 OCI 镜像仓库，并提供多上游 pull-through 缓存。它以单个 Go 二进制交付，使用本地数据目录和 SQLite，不依赖外部数据库、OIDC 或 LDAP。

> **项目状态：** 尚未发布 1.0。OCI Distribution 核心已通过一致性测试，但 crproxy 明确是单实例产品，并存在有边界的安全与规模限制。生产使用前请阅读[安全与生产注意事项](#安全与生产注意事项)和[能力边界](#能力边界)。
>
> **许可证状态：** Apache License 2.0。许可证允许商业使用、修改和分发；除非另行约定，不附带任何担保、商业支持或 SLA。

## 能力概览

| 领域 | crproxy 提供的能力 |
| --- | --- |
| 本地镜像仓 | OCI Distribution Spec 1.1：manifest、index、artifact、blob、普通/分块/流式上传、跨仓库 mount、catalog、tags、referrers、delete、Range 与条件请求。 |
| 代理缓存 | 面向 Docker Hub、registry.k8s.io、Quay、GHCR 及其他 OCI Distribution 上游的只读 pull-through 缓存；支持路径路由和 Host 路由。 |
| 缓存行为 | manifest/tag TTL、ETag/304 回源、blob/token 请求合并、Range 透传，以及跨仓库、跨上游共享的 CAS。 |
| 鉴权 | Registry Basic、Bearer PAT、Docker Registry Token Flow（HS256 JWT）及可选匿名 action；管理 API 使用 HttpOnly Cookie Session 和 CSRF 防护。 |
| 管理台 | 内嵌 React 19 管理台，覆盖仓库、缓存、上游、用户、存储、审计、指标和运行时配置。 |
| 传输安全 | 原生 TLS 与证书原子热加载、可信反向代理、HSTS、安全响应头，以及 HTTP/HTTPS 出站代理。 |
| 运维 | 逻辑配额、缓存水位、LRU、GC/fsck、上传恢复、审计、Prometheus 指标、健康/就绪探测、离线备份恢复和主密钥轮换。 |

crproxy **不是** Harbor 的平替。它不提供多租户项目、仓库级 ACL、HA、对象存储、镜像复制、漏洞扫描、签名策略或外部身份源。详见[能力边界](#能力边界)。

## 已验证基线

- OCI Distribution Spec 1.1 一致性：`850 passed / 4 optional skips / 0 failed / 0 error`。
- Go 单元/集成测试、race detector 和 `go vet` 通过。
- 镜像客户端验收使用 Podman；其他客户端可能可用，但不属于当前验收基线。
- 参考部署已覆盖本地 push/pull、PAT pull、多上游代理拉取、缓存命中、TLS 证书热加载、HTTP 出站代理和只读容器运行。

详见 [docs/conformance-report.md](docs/conformance-report.md)、[docs/acceptance-baseline.md](docs/acceptance-baseline.md) 和 [docs/podman-smoke.md](docs/podman-smoke.md)。

## 快速启动

### 前置条件

- Go 1.24 或更新版本。
- Node.js 24 和 npm，用于构建内嵌管理台。
- 文档中的客户端冒烟测试使用 Podman。
- systemd 部署需要 Linux；容器部署需要容器运行时。

### 构建与运行

```bash
git clone https://codeberg.org/aido/crproxy.git
cd crproxy

cp crproxy.example.yaml crproxy.yaml
# 启动前修改 crproxy.yaml：
#   auth.bootstrap_admin.password：至少 12 个字符
#   storage.root：服务用户可写目录

make build
./bin/crproxy serve -config crproxy.yaml
```

默认监听 `:5000`，Registry 与管理台共用该端口。浏览器访问 `http://127.0.0.1:5000/` 进入管理台。首次启动创建的 `admin` 账号必须先完成改密，之后才能使用管理 API。

默认数据目录为 `./data`。数据库位于 `data/meta/crproxy.db`，内容寻址存储位于 `data/cas`。`data/meta/jwt.secret` 和 `data/meta/master.key` 是长期密钥，必须与数据库一起备份。

### Podman 冒烟测试

```bash
# 仅限本地 HTTP 开发环境。
podman login --tls-verify=false -u admin 127.0.0.1:5000

# 代理拉取示例。
podman pull 127.0.0.1:5000/r/dockerhub/busybox:latest
podman pull 127.0.0.1:5000/r/ghcr/actions/actions-runner:latest
podman pull 127.0.0.1:5000/r/quay/prometheus/busybox:latest

# 本地推送和拉取。
podman tag 127.0.0.1:5000/r/dockerhub/busybox:latest 127.0.0.1:5000/local/team/busybox:latest
podman push 127.0.0.1:5000/local/team/busybox:latest
podman pull 127.0.0.1:5000/local/team/busybox:latest
```

生产环境禁止使用 `--tls-verify=false`。应在 crproxy 前使用可信 TLS 终止器，或配置 `server.tls.mode: file`。

## 配置

`crproxy.example.yaml` 是完整配置参考。YAML 和环境变量构成启动基线；部分设置可在管理台运行时修改，并持久化到 SQLite，直到在页面中恢复默认值。

关键字段：

- `server.public_host` / `server.public_scheme`：客户端可见的 Registry 地址，用于 Bearer realm、Secure Cookie 和生成 URL，必须与客户端实际访问地址一致。
- `server.trusted_proxies`：仅信任这些 CIDR/IP 的 `Forwarded` 和 `X-Forwarded-*`。只有 crproxy 位于受控反向代理之后时才配置。
- `server.tls.mode=file`：启用原生 TLS；证书原子热加载，失败时继续使用旧证书。
- `storage.root`：`meta/`、`cas/`、`uploads/`、`tmp/`、`trash/` 必须位于同一文件系统。
- `storage.max_bytes` / `storage.cache_max_bytes`：总逻辑配额和代理缓存配额；本地内容不会被缓存 LRU 淘汰。
- `auth.registry.allow_anonymous` / `anonymous_actions`：默认关闭匿名访问；只授予明确需要的 action。
- `proxy.path_prefix`：代理路由前缀，默认 `r`。

监听地址、公开地址、可信代理、TLS 模式、存储根目录、启动管理员和密钥路径需要重启。管理台中的运行时分区支持热更新。

### 出站 HTTP/HTTPS 代理

```yaml
outbound_proxy:
  http_proxy: http://proxy.example.com:3128
  https_proxy: http://proxy.example.com:3128
  no_proxy: localhost,127.0.0.1,::1,.internal.example.com
  username: ""
  password: ""
```

HTTPS 上游通过 HTTP `CONNECT` 隧道访问。若 crproxy 运行在虚拟机或容器、代理运行在宿主机，不能使用 `127.0.0.1`，必须填写 crproxy 进程可访问的宿主地址。容器部署仍兼容标准 `HTTP_PROXY`、`HTTPS_PROXY` 和 `NO_PROXY` 环境变量。

完整配置矩阵和部署方式见 [docs/deployment.md](docs/deployment.md)。

## 安全与生产注意事项

- 在暴露到不可信网络前先终止 TLS。设置 `server.public_scheme: https`，并把 `server.public_host` 配置为真实客户端域名。
- 首次登录立即修改 bootstrap 管理员密码。账号疑似泄露时，必须同时更换密码并撤销 API Token；撤销 Token 是独立操作。
- 管理面（包括 `/api/v1` 和 `/metrics`）应视为受信网络端点。若可能从不可信网络访问，必须增加反向代理鉴权、网络策略或额外认证层。
- 仅对受控反向代理配置 `trusted_proxies`。边缘代理必须覆盖客户端伪造的转发头，而不是盲目追加。
- 按安全模型需要，将 `master.key`、`jwt.secret`、`crproxy.db` 和 CAS 放在加密存储上。必须一起备份；丢失主密钥将导致上游凭证不可恢复。
- 一个数据目录只能运行一个 crproxy 进程。进程会获取数据目录锁，不支持多实例共享存储。
- 当前授权模型为全局 `admin`/`operator`/`viewer` 加 PAT action scope，**未实现仓库级 ACL**。当前版本只应部署在运维人员处于同一信任边界的场景，或在团队之间增加网络隔离。
- 备份是离线操作：执行 `crproxy backup` 或 `crproxy restore` 前必须停止服务。依赖备份前应在独立环境验证恢复流程。

漏洞请按 [SECURITY.md](SECURITY.md) 的流程报告。修复发布前不要在公开 issue 中披露安全细节。

## 运维

```bash
bin/crproxy backup -config /etc/crproxy/crproxy.yaml -output /backup/crproxy.tar.gz
bin/crproxy restore -config /etc/crproxy/crproxy.yaml -input /backup/crproxy.tar.gz --force
bin/crproxy rotate-master-key -config /etc/crproxy/crproxy.yaml
bin/crproxy healthcheck -url http://127.0.0.1:5000/readyz
```

端点：

| 端点 | 用途 |
| --- | --- |
| `GET /healthz` | 进程存活探测。 |
| `GET /readyz` | SQLite 与存储就绪探测。 |
| `GET /api/v1/healthz` | 管理面旧版存活探测。 |
| `GET /api/v1/readyz` | 管理面旧版就绪探测。 |
| `GET /metrics` | Prometheus 指标，仅面向可信监控网络。 |

当 `server.admin.listen` 与 `server.listen` 不同时，Registry 端口只提供 OCI Distribution API，管理端口提供控制台、`/api/v1`、`/metrics` 和探测端点；未配置独立管理地址时，两者共用 Registry 端口。

## 能力边界

- 单实例、本地块存储；不支持对象存储、多实例共享存储、分布式锁和跨地域复制。
- 不支持 OIDC、LDAP、SAML；账号为本地账号。
- 不支持 Docker schema1。
- 不支持仓库级 ACL 或项目/租户模型。
- 不做漏洞扫描、签名验证策略、镜像复制或准入策略。
- 代理路由严格只读；写请求返回 `405`，不会转发到上游。
- 仅支持离线备份，不支持在线快照或时间点恢复。
- 不内置高可用、自动故障转移或商业支持 SLA。

## 文档

- [总体设计](DESIGN.md)
- [Registry 协议设计](docs/registry-protocol-design.md)
- [鉴权与 TLS 设计](docs/auth-design.md)
- [存储与缓存设计](docs/storage-design.md)
- [前端设计](docs/frontend-design.md)
- [部署与运维](docs/deployment.md)
- [发布验收基线](docs/acceptance-baseline.md)
- [商业发布检查清单](docs/release-checklist.md)
- [OCI 一致性报告](docs/conformance-report.md)
- [Podman 冒烟测试](docs/podman-smoke.md)
- [OpenAPI 文档](docs/openapi.yaml)

英文详细设计文档将在后续补齐。英文 README 与中文 README 作为等价项目入口维护；若出现差异，构建与许可证信息以英文 README 为准。

## 贡献

欢迎在 Apache License 2.0 条款下贡献代码。提交 Pull Request 前请阅读 [CONTRIBUTING.md](CONTRIBUTING.md)。所有提交必须包含 Developer Certificate of Origin 签名。

## 许可证

Copyright 2026 The crproxy Authors.

本项目采用 Apache License 2.0，详见 [LICENSE](LICENSE) 和 [NOTICE](NOTICE)。

许可证允许商业使用。再分发时必须保留许可证和适用声明。第三方组件仍受其各自许可证约束。软件按“原样”提供，不附带任何明示或默示担保。

发布的容器镜像会在 `/licenses` 目录中包含 `LICENSE` 和 `NOTICE`。
