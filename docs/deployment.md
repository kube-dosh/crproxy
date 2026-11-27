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
install -m 0644 deploy/systemd/crproxy.sysusers /etc/sysusers.d/crproxy.conf
systemd-sysusers /etc/sysusers.d/crproxy.conf
install -d -o root -g crproxy -m 0750 /etc/crproxy /var/lib/crproxy
install -m 0755 bin/crproxy /usr/local/bin/crproxy
install -D -m 0644 LICENSE /usr/share/licenses/crproxy/LICENSE
install -D -m 0644 NOTICE /usr/share/licenses/crproxy/NOTICE
install -m 0640 -o root -g crproxy crproxy.example.yaml /etc/crproxy/crproxy.yaml
install -m 0640 -o root -g crproxy deploy/systemd/crproxy.service /etc/systemd/system/crproxy.service
systemctl daemon-reload
systemctl enable --now crproxy
systemctl status crproxy
```

systemd 部署只维护一个配置文件 `/etc/crproxy/crproxy.yaml`。启动前必须在其中设置 `auth.bootstrap_admin.password`、把 `storage.root` 改为 `/var/lib/crproxy`，并按实际域名设置 `server.public_host` 与 `server.public_scheme`。该文件包含管理员密码和代理凭证，保持 `0640 root:crproxy`，不要提交真实值。

unit 使用 `ConfigurationDirectory=crproxy` 和 `StateDirectory=crproxy` 管理 `/etc/crproxy` 与 `/var/lib/crproxy`，默认只允许写数据目录。若把 `storage.root` 改到其他路径，必须同步扩展 unit 的 `ReadWritePaths`，否则 `ProtectSystem=strict` 会阻止写入。配置文件或 YAML 启动基线修改后执行 `systemctl restart crproxy`；管理台中支持热更新的设置不需要重启。

常用运维命令：

```bash
systemctl restart crproxy
systemctl reload-or-restart crproxy
journalctl -u crproxy -f
/usr/local/bin/crproxy healthcheck -url http://127.0.0.1:5000/api/v1/readyz
```

unit 不配置 `ExecReload`，因为 crproxy 没有以 SIGHUP 重新加载 YAML 的语义。运行时配置由管理台原子更新；启动基线、TLS 证书路径和数据目录属于重启项。

## 3. 出站代理、TLS 与反向代理

### 出站 HTTP/HTTPS 代理

crproxy 的上游 manifest、blob、token 和重定向请求统一使用 Go HTTP Transport。出站代理可以写入 YAML：

```yaml
outbound_proxy:
  http_proxy: http://proxy.example.com:3128
  https_proxy: http://proxy.example.com:3128
  no_proxy: 127.0.0.1,localhost,.internal.example.com
  username: ""
  password: ""
```

systemd 部署的 HTTP/HTTPS 代理全部写在 `outbound_proxy`，不需要额外的 env 文件。该块支持 HTTP 绝对 URI 转发和 HTTPS CONNECT 隧道。YAML 中的值是启动基线；部署后可在“系统设置 → 出站代理”中覆盖，保存后新请求立即使用新代理，无需重启。

代理监听在宿主机时，`127.0.0.1` 对虚拟机或容器内的 crproxy 指向的是 crproxy 自身。应使用从 crproxy 网络命名空间可达的宿主地址；本项目当前虚拟机环境使用宿主桥接网关：

```yaml
outbound_proxy:
  http_proxy: http://192.168.122.1:1080
  https_proxy: http://192.168.122.1:1080
  no_proxy: localhost,127.0.0.1,::1,192.168.0.0/16,10.0.0.0/8,172.16.0.0/12
```

容器部署使用同名环境变量：

```bash
podman run -d --name crproxy \
  -e HTTPS_PROXY=http://proxy.example.com:3128 \
  -e NO_PROXY=127.0.0.1,localhost \
  crproxy:v1
```

Compose 已透传 `HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY`。如果企业代理对 TLS 做中间人检查，需要把代理 CA 加入运行环境信任库，或通过上游 `ca_pem` 单独配置；不要用 `insecure_skip_verify` 代替可信 CA。不要把真实代理密码写入仓库模板，只写进部署环境的 `0640 root:crproxy` YAML。

### 运行时可热更新配置

`/etc/crproxy/crproxy.yaml` 定义启动基线。管理台把运行时覆盖写入 SQLite 的 `settings` 表（键为 `runtime.<section>.v1`），进程启动时先加载基线，再叠加运行时覆盖。删除某个分区覆盖即可恢复对应启动配置。

| 分区 | 配置项 | 管理入口 | 生效方式 | 权限 |
| --- | --- | --- | --- | --- |
| `registry-auth` | 访问模式、匿名 actions、认证协议、服务标识、令牌服务基地址、令牌有效期 | 系统设置 | 下一次 `/v2/` 请求 | admin |
| `web-session` | Session TTL、空闲超时 | 系统设置 | 新登录；已有会话下一次续期 | admin |
| `storage` | 容量、水位、满盘策略、Blob/Manifest 上限、下载并发、tmp/trash TTL | 存储与磁盘 | 新请求/后台任务立即使用 | admin、operator |
| `uploads` | 上传会话 TTL | 上传会话 | 新建上传立即使用；清理任务下一轮使用 | admin、operator |
| `audit` | 审计保留期 | 审计日志 | 后台保留任务下一轮使用 | admin |
| `proxy` | 代理入口启停 | 系统设置 | 下一个请求 | admin |
| `outbound-proxy` | 上游 HTTP/HTTPS 代理、NO_PROXY、认证 | 系统设置 | 下一个上游请求；关闭旧空闲连接 | admin |
| `security` | HSTS | 系统设置 | 下一个 HTTPS 响应 | admin |

### Kubernetes containerd 镜像加速

crproxy 位于 `192.168.122.10:5000`、宿主出站代理为 `192.168.122.1:1080` 时，containerd 节点可把 Docker Hub 与 `registry.k8s.io` 的拉取直接转到 crproxy 缓存。containerd 2.3.x 的 `hosts.toml` 不支持 `rewrite` 字段，使用 `override_path` 把带 `/v2/r/...` 的 API 根路径作为镜像端点。

先创建只读拉取账号，不要让节点共享管理员密码。凭证只放在节点本地的 `0600 hosts.toml` 中：

```toml
# /etc/containerd/certs.d/docker.io/hosts.toml
server = "https://192.168.122.10:5000/v2/r/dockerhub"
capabilities = ["pull", "resolve"]
override_path = true
ca = "/etc/containerd/certs.d/192.168.122.10:5000/ca.crt"

[header]
  Authorization = "Basic <base64(k8s-mirror:password)>"
```

```toml
# /etc/containerd/certs.d/registry.k8s.io/hosts.toml
server = "https://192.168.122.10:5000/v2/r/k8s"
capabilities = ["pull", "resolve"]
override_path = true
ca = "/etc/containerd/certs.d/192.168.122.10:5000/ca.crt"

[header]
  Authorization = "Basic <base64(k8s-mirror:password)>"
```

`config_path = "/etc/containerd/certs.d"` 已配置时无需重启 containerd。修改前备份已有的 `hosts.toml`，修改后设置 `0600 root:root`。验证：

```bash
crictl pull docker.io/library/alpine:3.21
crictl pull registry.k8s.io/pause:3.10
```

首次拉取回源并写入缓存；删除本地镜像后再次拉取应只从 crproxy 读取。Docker Hub 的短名称会解析为 `docker.io/library/...`，crproxy 会映射到 `r/dockerhub/library/...`。

仍必须修改 YAML 并重启的启动项包括：监听地址、公网 Host/Scheme、可信代理、TLS 模式/证书/最低版本/重载周期、存储根目录、代理路径前缀、启动管理员、JWT 与主密钥文件。管理台“系统设置”页会只读展示这些字段。

运行时配置采用分区乐观锁：客户端提交 `expectedVersion`，若其他管理员已修改同一分区则返回 `409 config_version_conflict`。出站代理密码使用主密钥 AES-GCM 加密保存，API 只返回 `passwordConfigured`，不会回显明文。SQLite 继续使用 rollback journal，不使用 WAL。备份必须同时包含 `meta/crproxy.db`、`meta/master.key`、`meta/jwt.secret`、`cas/`、`uploads/`。

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

### 管理平面独立端口

默认只在 `server.listen` 上提供服务，Registry 与管理台共用端口。如需隔离网络暴露面，可增加 `server.admin`：

```yaml
server:
  listen: "0.0.0.0:5000"
  public_host: "registry.example.com:5000"
  public_scheme: "https"
  tls:
    mode: file
    cert_file: /etc/crproxy/tls/registry.crt
    key_file: /etc/crproxy/tls/registry.key
  admin:
    listen: "0.0.0.0:5443"
    public_host: "admin.example.com:5443"
    public_scheme: "https"
    tls:
      mode: inherit
```

拆分后，Registry 端口仅提供 `/v2/`、`/v2/token`、`/healthz`、`/readyz`；管理端口提供控制台、`/api/v1/*`、`/metrics` 与健康检查。Bearer realm 始终使用 Registry 的公开地址，Session Cookie 的 `Secure` 属性依据管理端公开协议。`admin.listen` 为空或与 `server.listen` 相同时自动回退为共用端口，不会重复绑定。`admin.tls.mode` 支持 `inherit`、`file`、`none`。

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
crproxy healthcheck -url https://registry.example.com/readyz
```

归档包含 `meta/`、`cas/`、`uploads/`，排除临时文件、trash 和 SQLite journal，附带 SHA-256 校验。恢复：

```bash
systemctl stop crproxy
crproxy restore -config /etc/crproxy/crproxy.yaml -input /backup/crproxy-20260101T000000Z.tar.gz --force
systemctl start crproxy
```

`--force` 会保留现有数据目录为 `.pre-restore-*`，不会静默丢弃。恢复后应先看 `/readyz`，再执行一次 quick fsck 或完整审计。

## 6. 密钥轮换

```bash
systemctl stop crproxy
crproxy rotate-master-key -config /etc/crproxy/crproxy.yaml
systemctl start crproxy
```

轮换会生成新版本密钥、重加密所有上游凭证并保留旧版本 key 以支持中断重跑。不要手工编辑 keyring；完成验证后再按运维策略归档旧密钥。

## 7. 健康检查、指标和日志

```bash
crproxy healthcheck -url http://127.0.0.1:5000/readyz
curl -fsS http://127.0.0.1:5000/healthz
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
