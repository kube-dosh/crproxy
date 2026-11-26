# 部署与运行手册

本文给出 crproxy 的生产部署闭环。项目只支持单实例本地存储；不要把同一数据目录挂载给多个进程。

## 1. 构建产物

### 本机构建

```bash
make verify
make build
./bin/crproxy version
```

`make build` 先执行 `npm ci` 和前端生产构建，再把 `web/dist` 内嵌到 Go 二进制。发布时可用 `VERSION=vX.Y.Z make build` 注入版本号。

### 容器镜像

```bash
podman build -t crproxy:v1 -f Dockerfile .
```

Dockerfile 使用三个基镜：Node 构建前端、Go 构建静态二进制、distroless nonroot 运行。受限网络可在构建参数中替换基镜：

```bash
podman build \
  --build-arg NODE_IMAGE=mirror.example.com/node:24-alpine \
  --build-arg GO_IMAGE=mirror.example.com/golang:1.24-alpine \
  --build-arg RUNTIME_IMAGE=mirror.example.com/distroless/static-debian12:nonroot \
  -t crproxy:v1 .
```

若构建主机完全无法访问基础镜像仓库，可先在本机构建前端和 Go 二进制，再使用已验证的离线路径：

```bash
make container-offline
```

该目标生成 `scratch` 运行时镜像，包含静态二进制、配置、CA bundle 与 `time/tzdata`，不执行容器内包管理或下载。

镜像内配置为 `/etc/crproxy/crproxy.yaml`，数据目录为 `/var/lib/crproxy`，进程 UID/GID 为 `65532:65532`。运行：

```bash
podman volume create crproxy-data
podman run -d --name crproxy \
  -p 5000:5000 \
  -e CRPROXY_ADMIN_PASSWORD='replace-with-a-long-password' \
  -e CRPROXY_PUBLIC_HOST='registry.example.com' \
  -e CRPROXY_PUBLIC_SCHEME='http' \
  -v crproxy-data:/var/lib/crproxy \
  --read-only --tmpfs /tmp:size=64m,mode=1777 \
  --cap-drop=ALL --security-opt no-new-privileges \
  --health-cmd '/crproxy healthcheck -url http://127.0.0.1:5000/api/v1/readyz' \
  --health-interval 30s --health-timeout 5s --health-retries 3 \
  crproxy:v1
```

使用 Compose：

```bash
export CRPROXY_ADMIN_PASSWORD='replace-with-a-long-password'
export CRPROXY_PUBLIC_HOST='registry.example.com'
export CRPROXY_PUBLIC_SCHEME='http'
podman compose up -d
```

`compose.yaml` 已限制只读根文件系统、移除 capabilities，并使用镜像内置 `healthcheck` 子命令。生产必须在外层配置 HTTPS。

## 2. systemd

```bash
useradd --system --home /var/lib/crproxy --shell /usr/sbin/nologin crproxy
install -d -o crproxy -g crproxy -m 0750 /var/lib/crproxy
install -d -o root -g crproxy -m 0750 /etc/crproxy
install -m 0755 bin/crproxy /usr/local/bin/crproxy
install -m 0640 -o root -g crproxy crproxy.example.yaml /etc/crproxy/crproxy.yaml
install -m 0640 -o root -g crproxy deploy/systemd/crproxy.service /etc/systemd/system/crproxy.service
printf 'CRPROXY_ADMIN_PASSWORD=%s\n' 'replace-with-a-long-password' > /etc/crproxy/crproxy.env
chmod 0600 /etc/crproxy/crproxy.env
systemctl daemon-reload
systemctl enable --now crproxy
systemctl status crproxy
```

修改 `storage.root` 为 `/var/lib/crproxy`，并按实际域名设置 `server.public_host` 与 `server.public_scheme`。

## 3. 出站代理、TLS 与反向代理

### 出站 HTTP/HTTPS 代理

crproxy 的上游 manifest、blob、token 和重定向请求统一使用 Go HTTP Transport，并遵循 `HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY`。HTTPS 上游通过代理的 HTTP CONNECT 隧道访问。变量必须在进程启动前设置；修改后重启服务生效。

systemd 部署可写入 `/etc/crproxy/crproxy.env`：

```bash
HTTP_PROXY=http://proxy.example.com:3128
HTTPS_PROXY=http://proxy.example.com:3128
NO_PROXY=127.0.0.1,localhost,.internal.example.com
```

容器部署使用同名环境变量：

```bash
podman run -d --name crproxy \
  -e HTTPS_PROXY=http://proxy.example.com:3128 \
  -e NO_PROXY=127.0.0.1,localhost \
  crproxy:v1
```

Compose 已透传 `HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY`。如果企业代理对 TLS 做中间人检查，需要把代理 CA 加入运行环境信任库，或通过上游 `ca_pem` 单独配置；不要用 `insecure_skip_verify` 代替可信 CA。不要把代理密码写入仓库，使用 systemd `EnvironmentFile`、容器 Secret 或部署平台的环境变量注入。

### 原生 TLS

```yaml
server:
  listen: "0.0.0.0:5443"
  public_host: "registry.example.com:5443"
  public_scheme: "https"
  tls:
    mode: file
    cert_file: /etc/crproxy/tls/tls.crt
    key_file: /etc/crproxy/tls/tls.key
    min_version: "1.2"
    reload_seconds: 30
    hsts: true
```

证书文件按周期检查并原子替换 TLS 配置；新证书解析失败时保留旧证书并记录错误，不会终止服务。私钥必须限制为服务用户可读。

### 反向代理

```yaml
server:
  listen: "127.0.0.1:5000"
  public_host: "registry.example.com"
  public_scheme: "https"
  trusted_proxies:
    - "127.0.0.1/32"
    - "10.0.0.0/8"
  tls:
    hsts: true
```

只有 `trusted_proxies` 中的直连地址才能提供 `Forwarded` 或 `X-Forwarded-For/Host/Proto`。反代必须覆盖而非追加客户端提供的这些头。`public_scheme=https` 时 Session Cookie 带 `Secure`，Bearer realm 也使用 HTTPS。

## 4. 数据目录与权限

```text
<storage.root>/
  LOCK
  meta/
    crproxy.db
    crproxy.db-journal        # rollback journal，仅写入事务期间存在
    jwt.secret
    master.key
  cas/sha256/...
  uploads/
  tmp/
  trash/
```

- 使用 SQLite rollback journal，不启用 WAL；备份和恢复流程会排除 journal/SHM/WAL。
- `cas/`、`uploads/`、`tmp/`、`trash/` 必须位于同一文件系统，跨文件系统 rename 不安全。
- 数据目录仅允许一个 crproxy 进程写入，`LOCK` 使用进程级 flock。
- 容器 bind mount 时必须把宿主目录 chown 给 `65532:65532`；命名卷会从镜像目录继承权限。
- `jwt.secret`、`master.key`、`crproxy.db` 均属于敏感数据；丢失 `master.key` 会导致上游凭证无法解密。

## 5. 备份与恢复

备份要求在维护窗口内停止服务，随后执行：

```bash
systemctl stop crproxy
crproxy backup -config /etc/crproxy/crproxy.yaml -output /backup/crproxy-$(date +%Y%m%dT%H%M%SZ).tar.gz
systemctl start crproxy
crproxy healthcheck -url https://registry.example.com/api/v1/readyz
```

归档包含 `meta/`、`cas/`、`uploads/`，排除临时文件、trash 和 SQLite journal，附带 SHA-256 校验。恢复：

```bash
systemctl stop crproxy
crproxy restore -config /etc/crproxy/crproxy.yaml -input /backup/crproxy-20260101T000000Z.tar.gz --force
systemctl start crproxy
```

`--force` 会保留现有数据目录为 `.pre-restore-*`，不会静默丢弃。恢复后应先看 `/api/v1/readyz`，再执行一次 quick fsck 或完整审计。

## 6. 密钥轮换

```bash
systemctl stop crproxy
crproxy rotate-master-key -config /etc/crproxy/crproxy.yaml
systemctl start crproxy
```

轮换会生成新版本密钥、重加密所有上游凭证并保留旧版本 key 以支持中断重跑。不要手工编辑 keyring；完成验证后再按运维策略归档旧密钥。

## 7. 健康检查、指标和日志

```bash
crproxy healthcheck -url http://127.0.0.1:5000/api/v1/readyz
curl -fsS http://127.0.0.1:5000/api/v1/healthz
curl -fsS http://127.0.0.1:5000/metrics
```

- `healthz` 只证明进程在运行；容器和负载均衡探针应使用 `readyz`。
- `/metrics` 标签只使用路由类别，不使用 repository 名或 digest，基数有界。
- 日志为 JSON 输出到 stderr，交由 journald、容器运行时或日志采集器管理。
- 审计日志保存在 SQLite，通过管理 API 或控制台查询；保留时长由 `storage.audit_retention` 控制。

## 8. 升级与回滚

1. 阅读新版本配置变更，先备份配置和数据。
2. 停止服务，替换二进制或镜像。
3. 启动后检查 `readyz`、`/metrics`、管理台和一次 Podman 登录/拉取。
4. 若升级失败，停止服务并使用旧二进制；不要把已升级的数据库直接交给旧版本，除非发布说明明确保证向后兼容。

## 9. 故障排查

- **`data directory is already in use`**：同一数据目录已有进程；确认没有残留容器或进程。
- **`readyz` 返回 503**：先看 `component` 字段；数据库损坏走备份恢复，存储不可用检查挂载和权限。
- **代理拉取超时**：检查上游网络、自定义 CA、上游凭证和 `/metrics` 的 `crproxy_upstream_health`。
- **登录成功后客户端仍 401**：核对 `public_host/public_scheme` 与客户端访问地址一致。
- **反代后 Cookie 不生效**：`public_scheme` 必须为 `https`，且反代地址在 `trusted_proxies` 中。
- **磁盘持续上涨**：检查 GC 任务、cache LRU、upload/trash/tmp TTL 和 `storage.max_bytes`。

## 10. 发布检查单

- `make verify` 全部通过。
- OCI Distribution conformance 结果无失败。
- Podman 完成 login、pull、tag、push、digest pull 和代理写拒绝。
- HTTPS + Basic/Bearer challenge 均在目标反代或原生 TLS 路径验证。
- 备份可在独立数据目录恢复并通过 readyz。
- Prometheus 能抓取 `/metrics`，告警覆盖磁盘 state 和上游健康。
- 管理台首个管理员已完成强制改密，默认密码未进入配置仓库。
