# crproxy

轻量级、自托管的 OCI Registry：本地支持完整 OCI Distribution Spec 1.1，另提供多上游只读 pull-through cache 和内置 React 管理台。一个 Go 二进制、一个本地数据目录、SQLite rollback journal，不依赖外部数据库、OIDC 或 LDAP。

## 能力基线

- 完整本地 Registry：ping、manifest/index/artifact、blob、普通/分块/流式上传、mount、catalog、tags、referrers、delete、Range 与条件请求。
- 只读多上游代理：Docker Hub、registry.k8s.io、Quay、GHCR 及任意 OCI Distribution 上游；支持路径路由和 Host 路由。
- 出站代理：上游请求遵循标准 `HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY`，HTTPS 上游通过 CONNECT 隧道访问。
- 代理始终只读：所有写方法固定返回 `405`，不会向上游转发写请求；代理仓库不会进入本地 catalog。
- 上游鉴权：anonymous、Basic、静态 Bearer、Bearer challenge；HTTPS、自定义 CA、TLS 校验开关。
- 缓存：manifest/tag TTL、ETag/304 回源、blob/token singleflight、Range 透传、同 digest 跨仓库/上游共享 CAS。
- 用户鉴权：Basic、Bearer PAT、Docker Registry token flow（HS256 JWT）、Web Session + CSRF。
- 安全：原生 TLS、证书原子热加载、可信反代、公开 scheme/Host、Secure Cookie、HSTS、安全响应头。
- 运维：配额与缓存水位、LRU、GC/fsck、上传恢复、审计、Prometheus 指标、健康/就绪探测、备份恢复、主密钥轮换。
- 交付：React 19 + shadcn 风格管理台内嵌进单二进制；Makefile、多阶段 Dockerfile、Compose、systemd、OpenAPI 与部署文档。

当前验证结果：

- 最终二进制 OCI Distribution Spec 1.1 conformance：`850 passed / 4 optional skips / 0 failed / 0 error`。
- Podman 5.8.9 实测：登录、Quay/GHCR 代理拉取、缓存命中、本地 push、按 digest pull、PAT pull、代理 push `405`、HTTPS + Basic 拉取与证书热加载。
- 出站代理实测：服务端配置 `HTTPS_PROXY=http://127.0.0.1:1080` 后，Podman 成功拉取 Docker Hub `busybox:latest` 与 registry.k8s.io `pause:3.10`，缓存命中分别约 `0.53s`、`0.56s`。
- 只读容器实测：UID `65532`、`cap-drop=ALL`、`no-new-privileges`，健康检查、内嵌管理台、登录、代理拉取和缓存命中通过。
- 自动化：`go test ./...`、`go test -race ./...`、`go vet ./...`、前端 unit test/typecheck/build。

## 快速启动

```bash
export CRPROXY_ADMIN_PASSWORD='replace-with-a-long-password'
go run ./cmd/crproxy -config crproxy.example.yaml
```

默认监听 `:5000`，数据目录 `./data`。首次启动创建 `admin`，密码不足 12 个字符会拒绝启动。`data/meta/jwt.secret` 和 `data/meta/master.key` 是长期密钥，必须与数据库一起备份。

前端源码已内嵌构建。完整构建：

```bash
make build        # 前端 npm ci/build + Go 单二进制 -> bin/crproxy
make verify       # test + race + vet + typecheck + web build
make container-offline  # Podman 构建 scratch 运行时镜像，适合离线/受控网络
```

## 使用 Podman

本项目的镜像客户端验收只使用 Podman。

```bash
podman login --tls-verify=false -u admin 127.0.0.1:5000

# 本地镜像
podman pull 127.0.0.1:5000/local/team/app:latest
podman push 127.0.0.1:5000/local/team/app:latest

# 代理拉取
podman pull 127.0.0.1:5000/r/quay/prometheus/busybox:latest
podman pull 127.0.0.1:5000/r/ghcr/actions/actions-runner:latest
```

生产环境应配置 TLS 或由可信反向代理终止 TLS，不要使用 `--tls-verify=false`。代理路由中的写请求会被明确拒绝，见 `docs/podman-smoke.md`。

### 出站代理

crproxy 进程遵循标准代理环境变量。需要在受限网络中回源时，在启动前设置：

```bash
export HTTP_PROXY=http://127.0.0.1:1080
export HTTPS_PROXY=http://127.0.0.1:1080
export NO_PROXY=127.0.0.1,localhost
./bin/crproxy -config /etc/crproxy/crproxy.yaml
```

HTTPS 上游通过 HTTP CONNECT 访问代理；`NO_PROXY` 只应包含确实需要直连的地址。Compose 已透传这三个变量，systemd 可通过 `EnvironmentFile=/etc/crproxy/crproxy.env` 配置。

## 配置重点

- `server.public_host` / `server.public_scheme`：生成 Bearer realm、Secure Cookie、绝对 URL 的依据；反代部署必须设置成客户端实际访问的地址。
- `server.trusted_proxies`：仅这些 CIDR/IP 的 `Forwarded`、`X-Forwarded-*` 会被信任。
- `server.tls.mode=file`：启用原生 HTTPS；证书按 `reload_seconds` 原子热加载，失败时继续使用旧证书。
- `server.tls.hsts=true`：只在 HTTPS 永久启用后打开。
- `storage.root`：`meta/`、`cas/`、`uploads/`、`tmp/`、`trash/` 必须位于同一文件系统。
- `storage.max_bytes` / `cache_max_bytes`：总逻辑配额与代理缓存配额；本地内容不会被缓存 LRU 删除。
- `proxy.path_prefix`：路径模式前缀，默认 `r`。

完整字段与注释见 `crproxy.example.yaml`。部署方式见 `docs/deployment.md`。

## 运维

```bash
bin/crproxy backup -config /etc/crproxy/crproxy.yaml -output /backup/crproxy.tar.gz
bin/crproxy restore -config /etc/crproxy/crproxy.yaml -input /backup/crproxy.tar.gz --force
bin/crproxy rotate-master-key -config /etc/crproxy/crproxy.yaml
bin/crproxy healthcheck -url http://127.0.0.1:5000/api/v1/readyz
```

端点：

- `GET /api/v1/healthz`：进程存活。
- `GET /api/v1/readyz`：SQLite 与存储可用。
- `GET /metrics`：Prometheus。
- 管理 API 与 OpenAPI：`docs/openapi.yaml`。

## 明确边界

- 单实例、本地块存储；不支持对象存储、多实例共享、跨地域复制。
- 不支持 OIDC、LDAP、SAML。
- 不支持 Docker schema1。
- 不做漏洞扫描、签名策略、镜像复制和仓库级 ACL；权限模型为 admin/operator/viewer + PAT scope。
- 代理上游不可写，这不是配置项。

## 文档

- 总体设计：`DESIGN.md`
- 协议实现：`docs/registry-protocol-design.md`
- 鉴权与 TLS：`docs/auth-design.md`
- 存储与缓存：`docs/storage-design.md`
- 前端：`docs/frontend-design.md`
- 部署：`docs/deployment.md`
- 验收：`docs/acceptance-baseline.md`
- Conformance：`docs/conformance-report.md`
- Podman 冒烟：`docs/podman-smoke.md`
