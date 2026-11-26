# crproxy 设计方案 v4（完整 Registry 基线）

一个用 Go 实现的 OCI Registry：本地支持完整 OCI Distribution / Docker Registry V2 能力，同时提供多上游 pull-through cache，并内置 React 管理台。

> 本版基线冻结以下边界：本地 Registry 完整实现 OCI Distribution Spec 1.1 的 pull、push、delete 和 content discovery；代理路由只读；Distribution 只作为协议参考和测试 oracle，不进入运行时依赖；元数据使用纯 Go SQLite，默认 rollback journal，不启用 WAL。

详细设计：

- 协议实现：[`docs/registry-protocol-design.md`](docs/registry-protocol-design.md)
- 前端：[`docs/frontend-design.md`](docs/frontend-design.md)
- 磁盘与缓存：[`docs/storage-design.md`](docs/storage-design.md)
- 鉴权与 TLS：[`docs/auth-design.md`](docs/auth-design.md)
- 发布验收基线：[`docs/acceptance-baseline.md`](docs/acceptance-baseline.md)
- 部署运行手册：[`docs/deployment.md`](docs/deployment.md)
- 管理 API：[`docs/openapi.yaml`](docs/openapi.yaml)

## 0. 基线结论

crproxy 不是“只读 Registry 代理”，而是一个单进程的混合 Registry。第一条基线必须同时成立：

1. **本地 Registry 是完整实现**：不能把本地能力做成“只能 pull 的简化 Registry”。manifest、blob upload、manifest push、tag、catalog、referrers、delete、cross-repository mount 都属于本地核心能力。
2. **代理 Registry 是只读缓存**：代理路由可以 pull manifest/blob、查询 tags 和 referrers，但不接受 push、delete、upload 或其他写操作；代理写请求固定返回 `405`，不转发上游。
3. **本地和代理共用一套基础设施**：同一进程、同一 SQLite、同一 CAS、同一鉴权、同一管理台；通过 repository kind 和 namespace 分离生命周期，而不是拆成多套服务。
4. **协议完整但运行轻量**：减少对象存储驱动、HA、插件链、通知、扫描、复制和外部 IdP，不是减少 OCI 协议状态码、错误码、上传状态机或并发语义。
5. **元数据存储保持简单**：`modernc.org/sqlite`、rollback journal、短事务；不使用 WAL，也不引入外部数据库。

首个可发布版本的最低门槛是：OCI Distribution Spec pull/push/content discovery conformance 通过，Podman `login`/`pull`/`tag`/`push` 通过，代理多上游拉取和本地/缓存隔离通过，GC 不误删本地内容。客户端冒烟唯一使用 Podman。验收细节见 [`docs/acceptance-baseline.md`](docs/acceptance-baseline.md)。

## 1. 产品定位

crproxy 不再是“只读代理”，而是一个混合型 Registry：

```text
本地 Registry + 多上游 Pull-Through Cache + Web 管理台
```

两类流量严格分离：

- **本地 Registry**：完整 pull、push、delete、catalog、tags、referrers、blob mount。
- **代理 Registry**：只读 pull、tags/list、referrers 透传；写操作固定返回 `405`。

这样可以同时满足：

- 团队内部镜像仓库。
- Docker Hub、GHCR、Quay、registry.k8s.io 等外部上游加速。
- 本地镜像和缓存内容统一管理、统一鉴权、统一磁盘统计。

## 2. 需求覆盖

1. React + shadcn/ui 管理台，视觉和页面对齐 `dosh` / `virtmgr`。
2. 本地完整 OCI Distribution Spec 接口。
3. 多上游 pull-through cache。
4. 多用户、本地用户、角色、PAT 和 Docker token flow。
5. Basic、Bearer、HTTPS。
6. 本地镜像与代理缓存统一 CAS 存储。
7. 上游凭证加密、磁盘配额、GC、崩溃恢复和审计。
8. 上游回源支持标准 HTTP/HTTPS 出站代理和 `NO_PROXY`。
9. 不接入 OIDC、LDAP、SAML 或任何外部身份提供商。

## 3. 目标与非目标

### 3.1 目标

- 本地 Registry 支持完整 pull/push/delete/catalog/tags/referrers。
- 通过 OCI Distribution Spec conformance 的 pull 和 push 测试。
- 按 OCI/Docker Registry V2 协议兼容常见客户端；当前发布验收固定使用 Podman。
- 多上游统一接入，按 Host 或保留路径前缀路由。
- 上游请求遵循 `HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY`，HTTPS 上游支持 HTTP CONNECT 隧道。
- manifest 和 blob 内容寻址，跨仓库、跨上游去重。
- 单个 Go 二进制 + 一个本地数据目录运行。
- 内嵌 React 管理台，不单独部署前端。
- Basic、Bearer PAT、Docker token flow、Web Session。
- 容量限制、本地内容保护、缓存 LRU、Range、健康检查和 Prometheus 指标。
- 单实例、本地块存储；不提供对象存储或多实例后端。

### 3.2 非目标

- 不实现镜像复制、漏洞扫描、策略引擎和 Harbor 式多项目管理。
- 不支持 Docker schema1；只支持 OCI manifest/index、Docker schema2 manifest/manifest list。
- 不支持多实例共享存储、分布式锁和对象存储后端。
- 权限模型固定为本地用户 + 角色 + PAT scope，不实现多租户或仓库级 ACL。
- 不接入 OIDC、LDAP、SAML 或任何外部身份提供商。
- 不把代理路由变成可写 Registry；代理上游始终只读。

## 4. 核心设计决策

| 问题 | 决策 |
| --- | --- |
| 服务端 | Go 单二进制，内嵌 React 静态资源 |
| 前端 | React 19 + Vite + TypeScript + Tailwind + shadcn new-york |
| Registry 模式 | 本地完整 Registry + 代理只读 cache |
| 路由 | Host 优先，路径前缀 `r/` 作为 fallback |
| 本地协议 | 完整 OCI Distribution Spec 1.1 pull/push/delete/catalog/referrers |
| 代理协议 | pull、tags/list、referrers 透传；写操作为 `405` |
| manifest | digest 内容寻址；本地 tag 持久，代理 tag TTL |
| blob | digest 内容寻址，全局去重；本地引用保护，缓存 LRU |
| 上传 | 持久化 upload session + 临时文件，支持 chunk、mount、恢复 |
| 元数据 | 嵌入式 SQLite（纯 Go 驱动）+ 默认 rollback journal |
| 内容存储 | 本地文件系统 CAS，两级目录分片 |
| 客户端鉴权 | Basic + Bearer PAT + Docker token flow + Web session |
| 权限 | 本地 pull/push/delete；代理只允许 pull |
| 上游鉴权 | 匿名 / Basic / Bearer 静态 token / 自动 challenge token |
| 出站代理 | 标准 `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY`，HTTPS 使用 CONNECT |
| HTTPS | 原生 TLS 或反向代理终止 |
| 凭证保护 | 上游凭证 AES-GCM 加密，master key 独立提供 |
| 缓存淘汰 | 只淘汰未被本地引用的 cache-only 内容 |
| 部署 | 单实例、单数据卷；不支持多实例共享数据目录 |

## 5. 为什么仍然自研而不是直接用 Distribution

Distribution 是成熟实现，也能做 pull-through cache。但 crproxy 需要的是“本地完整 Registry + 多上游统一代理 + 自定义用户/UI/磁盘管理”的组合：

| 需求 | Distribution | crproxy |
| --- | --- | --- |
| 本地完整 Registry | 支持 | 自研实现并做 conformance |
| 多上游统一入口 | 一个实例通常一个 `remoteurl` | 单实例多 upstream，Host/路径路由 |
| 多用户 | registry 级 htpasswd/token | 本地用户、角色、PAT、Session、审计 |
| 本地 + 代理共存 | 需要多实例和外部路由 | 同一服务、同一 CAS、统一引用模型 |
| 统一缓存运维 | 弱 | 缓存浏览、配额、GC、磁盘水位、下载任务 |
| 管理台 | 无 | React 管理台 |
| 存储去重 | 多实例各存各的 | 本地与代理共享全局 CAS |

因此：

- 自研本地 Registry 协议和代理缓存，但以 Distribution 作为协议参考和测试 oracle。
- Harbor 只作为功能上限参考，不照搬组件复杂度。
- 如果只是“单上游 + 无 UI + 无多用户”，直接使用 Distribution 更省事；本项目不适用。

### 5.1 Distribution 的参考边界

需要明确区分两件事：

- **协议行为参考 Distribution**：状态码、错误码、响应头、上传状态机、分页、manifest 校验、删除语义都应严格对齐 OCI Distribution Spec，并用 Distribution 和 conformance tests 校验。
- **运行时代码不复用 Distribution**：不 fork、不嵌入、不把 Distribution 作为 sidecar 或库依赖。

原因是 Distribution 的服务器架构与我们的核心模型不同：

- 它的存储布局和 GC 面向“单 Registry”，不区分 local 和 proxy cache。
- 它没有多上游统一路由，也没有跨 upstream 的共享 CAS 和统一缓存配额。
- 它的配置、存储驱动和 middleware 是围绕完整 Registry 服务设计的，嵌入成本高。
- 它的用户、token 服务、Web 管理台和审计需要额外系统，无法满足本地多用户管理。
- 直接 fork 会长期维护一套完整的 Distribution 分支。

### 5.2 “轻量级”不等于“简化协议”

crproxy 的轻量化应来自减少运行面和可选能力，而不是省略协议细节：

- 只实现本地文件系统存储，不做多种对象存储驱动。
- 单一本地用户体系，不接 OIDC/LDAP。
- 不做通知系统、插件链、复制、扫描和多实例协调。
- 本地与代理共享一套 CAS、一套 SQLite、一套鉴权和一套 UI。
- 协议实现按 OCI conformance 做完整，而不是只实现 happy path。

因此“完整协议 + 轻量运行”是成立的；“完整协议但少写代码”不成立。Upload、manifest 校验、delete、referrers 和 GC 必须认真实现。

### 5.3 什么时候应该直接用 Distribution

- 只需要一个本地可写 Registry。
- 不需要多上游 pull-through cache。
- 不需要自定义用户、PAT、Session、审计和管理台。
- 能接受 Distribution 的配置、存储驱动和部署方式。

如果以上条件成立，Distribution 是更成熟、风险更低的选择。crproxy 选择自研，是因为它的核心价值在 Distribution 之外的代理、统一存储、统一鉴权和运维控制面。

## 6. 总体架构

```text
                         ┌──────────────────────────────────────────────┐
 Docker / OCI Client ───▶│ HTTP(S) Listener                            │
                         │  ├─ /v2/... Registry API                    │
                         │  ├─ /api/v1/... Management API              │
                         │  └─ /... embedded React SPA                 │
                         │                                              │
                         │  Auth / Scope Middleware                    │
                         │   ├─ Basic / Bearer PAT / Docker JWT        │
                         │   └─ Web session + CSRF                     │
                         │                                              │
                         │  Route Resolver                             │
                         │   ├─ local host -> Local Registry           │
                         │   └─ proxy host/path -> Proxy Registry      │
                         │                                              │
                         │  Local Registry Handler                     │
                         │   ├─ pull                                   │
                         │   ├─ blob upload sessions                   │
                         │   ├─ manifest push/delete                   │
                         │   ├─ tags / catalog / referrers             │
                         │   └─ cross-repo mount                       │
                         │                                              │
                         │  Proxy Handler                              │
                         │   ├─ manifest/blob pull-through             │
                         │   ├─ tag TTL / conditional revalidation     │
                         │   └─ upstream token cache                   │
                         │                                              │
                         │  Unified Storage                            │
                         │   ├─ CAS filesystem                         │
                         │   ├─ SQLite metadata                        │
                         │   ├─ upload manager                         │
                         │   └─ GC / quota / fsck                      │
                         └──────────────────────────────────────────────┘
```

## 7. 路由与 Repository 模型

### 7.1 Host 模式

```text
registry.example.com              -> 本地 Registry
dockerhub.registry.example.com    -> Docker Hub 代理
ghcr.registry.example.com         -> GHCR 代理
quay.registry.example.com         -> Quay 代理
k8s.registry.example.com          -> registry.k8s.io 代理
```

### 7.2 路径模式

```text
registry.example.com/<repo>          -> 本地 Registry
registry.example.com/r/<slug>/<repo> -> 代理上游
```

`r` 是保留第一段路径，不能用于本地仓库名。

### 7.2.1 上游 repository 名称映射

代理请求中的 repository 名先按客户端可见名称建立缓存仓库，再映射成上游 repository：

- cache 仓库的 `namespace` 固定为 upstream ID，`name` 保存客户端实际请求的名称。
- `repository_prefix` 只对**单段名称**生效，避免把 `library/library/nginx` 这类双前缀写错。
- Docker Hub 为例：`nginx` -> 上游 `library/nginx`；`bitnami/nginx` 保持不变。
- 同一客户端的 `nginx` 与 `library/nginx` 在本地可以是两个 cache repository；它们共享 CAS 内容，但分别维护 tag 和 TTL。
- 上游 endpoint 必须来自预置或管理员配置，不能由请求 URL 任意指定，避免把服务变成开放 SSRF 代理。

不支持任意正则重写；内网仓库路径映射只能使用当前受约束的 `repository_prefix`，不会根据客户端输入改写上游地址。

### 7.3 Repository 类型

```text
local  -> 本地推送，可 pull/push/delete，不参与缓存 LRU
cache  -> 上游缓存，只读 pull，可被 LRU 淘汰
```

同一个 digest 可以同时被 local 和 cache 引用，物理 CAS 文件只有一份。

代理 cache 不出现在 `/v2/_catalog` 中；代理缓存通过管理 API 和 React 管理台查看。代理路由的 `GET /v2/_catalog` 返回 `404`，不向上游转发。

### 7.4 上游预设

| 预设 | endpoint | 认证 | 备注 |
| --- | --- | --- | --- |
| Docker Hub | `https://registry-1.docker.io` | 匿名 token / Basic | 官方镜像补 `library/` |
| GHCR | `https://ghcr.io` | Bearer PAT / challenge | GitHub Container Registry |
| Quay | `https://quay.io` | Basic robot token / challenge | Red Hat Quay |
| Kubernetes | `https://registry.k8s.io` | 匿名 | 可能重定向到 CDN |
| Generic | 用户输入 | 匿名 / Basic / Bearer | Harbor、Distribution 等 |

### 7.5 上游数据模型

```go
type Upstream struct {
    ID                 string
    Name               string
    Endpoint           string
    RouteHost          string
    RoutePrefix        string
    RepositoryPrefix   string // 仅对单段 repository 名生效
    AuthType           string
    Credentials        EncryptedSecret
    CAPEM              string
    InsecureSkipVerify bool
    TagTTL             time.Duration
    RequestTimeout     time.Duration
    MaxBlobBytes       int64
    Enabled            bool
}
```

凭证只保存密文，管理 API 只返回 `has_credentials`。

### 7.6 出站 HTTP/HTTPS 代理

所有上游 manifest、blob、token 和重定向请求共用同一 `http.Transport`，并通过 `http.ProxyFromEnvironment` 读取进程环境中的 `HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY`。HTTPS 上游由代理建立 CONNECT 隧道。代理在启动前配置，运行中不热更新；`NO_PROXY` 应只包含需要直连的地址。若代理对上游 TLS 做中间人检查，必须配置可信 CA，不能依赖关闭证书校验。代理地址、凭证和上游 token 不写入审计或普通日志。

## 8. 协议实现范围

详细设计见 [`docs/registry-protocol-design.md`](docs/registry-protocol-design.md)。

### 8.1 本地 Registry

完整支持：

```text
GET  /v2/
HEAD /v2/
GET  /v2/_catalog
GET  /v2/<name>/tags/list
GET  /v2/<name>/referrers/<digest>
GET  /v2/<name>/manifests/<reference>
HEAD /v2/<name>/manifests/<reference>
PUT  /v2/<name>/manifests/<reference>
DELETE /v2/<name>/manifests/<reference>
GET  /v2/<name>/blobs/<digest>
HEAD /v2/<name>/blobs/<digest>
DELETE /v2/<name>/blobs/<digest>
POST /v2/<name>/blobs/uploads/
PATCH /v2/<name>/blobs/uploads/<id>
PUT  /v2/<name>/blobs/uploads/<id>
GET  /v2/<name>/blobs/uploads/<id>
DELETE /v2/<name>/blobs/uploads/<id>
POST /v2/<name>/blobs/uploads/?mount=<digest>&from=<repo>
GET  /v2/token
```

`GET /v2/token` 是认证端点，不属于 repository 读写 API；本地和代理 Host 都可用。

关键语义：

- Blob upload 支持普通、chunked、monolithic 和 mount。
- Manifest PUT 校验媒体类型、digest、config/layers 引用。
- Tag 覆盖只更新指向；旧 manifest 仍可按 digest 访问，直到显式删除。自动清理 untagged manifest 不在 v4 基线内。
- Delete 删除 repository 引用，物理内容由 GC 决定。
- Catalog 只列本地 repository。
- 本地 Referrers 根据 `subject` 关系生成 OCI image index；没有 referrer 时返回空 index。
- `DELETE /v2/<name>/manifests/<tag>` 只删除 tag；`DELETE /v2/<name>/manifests/<digest>` 删除 manifest 及所有指向它的 tag。Registry V2 没有 `/tags/<tag>` 独立路径，管理 API 也提供 tag 删除。
- 返回正确的 `Location`、`Range`、`Docker-Upload-UUID`、`Docker-Content-Digest`。
- 通过 OCI Distribution Spec pull/push conformance。

### 8.2 代理 Registry

支持：

- manifest pull / blob pull / tags list / referrers 透传。
- 多上游回源。
- tag TTL、ETag 条件请求。
- blob singleflight、边回源边缓存。
- 上游匿名、Basic、Bearer token challenge。
- 写操作固定返回 `405`。
- 上游 referrers 返回 `404` 时原样透传，不伪造空 index；只有本地 Registry 在无 referrer 时返回空 index。

代理 cache 不提供 push、delete、catalog；`GET /v2/_catalog` 返回 `404`，缓存列表只从管理 API 获取。

### 8.3 并发和一致性

- 同一 blob digest 下载 singleflight。
- 同一 upload ID 的 PATCH/PUT 串行。
- manifest PUT 以 digest 为幂等键。
- tag 更新在 SQLite 事务中完成。
- 内容文件先提交 CAS，再写引用；孤儿由 fsck/GC 清理。
- 删除与读取并发时，Unix 已打开的文件描述符可继续读。

## 9. 鉴权与 HTTPS

详细设计见 [`docs/auth-design.md`](docs/auth-design.md)。

### 9.1 客户端鉴权

- Basic 用户名 + 密码。
- Bearer PAT。
- Docker token flow，短期 JWT。
- Web Session + CSRF。

角色和 scope：

```text
admin    -> pull, push, delete, manage, admin
operator -> pull, push, delete, manage
viewer   -> pull
```

operator 可以查看只读的系统设置快照，并管理上游、缓存和存储阈值；不能管理用户或其他用户的 Token。系统设置页没有写接口，配置变更仍通过 YAML 和受控重启完成。

本地 Registry：

- pull -> `pull`
- push -> `push`
- delete -> `delete`
- catalog -> `manage` 或 `admin`

代理 Registry：

- 只允许 pull。
- 写方法返回 `405`，不进入上游写转发。

`/v2/_catalog` 不通过 Docker JWT 的 repository scope 授权；使用 Basic、PAT 或管理台 Session，并要求用户具备 `manage` 或 `admin`。Docker token flow 不签发 catalog scope，catalog 能力不会下放给普通 pull token。

Docker token endpoint：

```text
GET /v2/token?service=crproxy&scope=repository:library/nginx:pull,push
```

JWT 的 `access.actions` 必须包含当前操作所需动作。

### 9.2 管理 API

- Web Session Cookie + CSRF。
- Basic 或 Bearer PAT。
- 本地用户，不接 OIDC/LDAP。
- Argon2id 密码哈希，PAT 只存 SHA-256 哈希。
- 登录限流、审计、Origin 校验。

### 9.3 HTTPS

- 原生 TLS，证书热加载，最低 TLS 1.2。
- 或反向代理终止 TLS，只信任配置 CIDR 的转发头。
- Secure Cookie、HSTS、上游自定义 CA、`insecure_skip_verify` 警告。
- 上游凭证 AES-256-GCM 加密，master key 独立提供。

## 10. 磁盘与内容存储

详细设计见 [`docs/storage-design.md`](docs/storage-design.md)。

```text
/var/lib/crproxy/
  VERSION
  LOCK
  meta/crproxy.db
  cas/sha256/<aa>/<bb>/<hex>
  uploads/<upload-id>.part
  tmp/<job>.part
  trash/<uuid>.deleted
```

核心原则：

- CAS 同时保存本地和代理内容，物理去重。
- 本地引用保护内容，不参与缓存 LRU；只有显式删除后才可能回收。
- 代理 cache-only 内容参与 LRU 淘汰。
- `uploads/` 与 `cas/` 同文件系统，完成后原子 rename。
- `uploads` 元数据持久化，进程重启后可继续上传。
- SQLite 默认 rollback journal，不启用 WAL。
- 磁盘高/低水位、`min_free_bytes`、`on_full=bypass|reject`。
- 本地内容受保护，缓存满时优先淘汰 cache-only 内容。
- 不支持 NFS/SMB/CIFS；单实例 `LOCK` 文件。

## 11. 前端管理台

详细设计见 [`docs/frontend-design.md`](docs/frontend-design.md)。

页面：

```text
总览
本地镜像
代理缓存
上游仓库
缓存条目
存储与磁盘
用户与 Token
审计日志
系统设置
关于
```

重点页面：

- 本地镜像：repository、tag、manifest、referrers、push 命令、删除。
- 代理缓存：缓存仓库、tag TTL、回源状态、清理缓存。
- 存储：本地/缓存用量、上传任务、GC、fsck、磁盘水位。
- 用户：角色、PAT scope、push/delete 权限、登录记录。

技术栈：

- React 19 + Vite + TypeScript。
- Tailwind 3.4 + shadcn new-york。
- TanStack Query；轻量 History Router；原生语义化表格；CSS 趋势图。
- lucide-react、sonner。
- 单二进制 embed `web/dist`。

## 12. 管理 API

```text
POST   /api/v1/auth/login
POST   /api/v1/auth/logout
GET    /api/v1/auth/session
POST   /api/v1/auth/password

GET    /api/v1/overview
GET    /api/v1/registry/summary

GET    /api/v1/repositories?kind=local|cache
GET    /api/v1/repositories/{id}
GET    /api/v1/repositories/{id}/tags
GET    /api/v1/repositories/{id}/manifests
GET    /api/v1/repositories/{id}/blobs
GET    /api/v1/repositories/{id}/referrers
DELETE /api/v1/repositories/{id}
DELETE /api/v1/repositories/{id}/manifests/{digest}
DELETE /api/v1/repositories/{id}/tags/{tag}

GET    /api/v1/upstreams
POST   /api/v1/upstreams
GET    /api/v1/upstreams/{id}
PUT    /api/v1/upstreams/{id}
DELETE /api/v1/upstreams/{id}
POST   /api/v1/upstreams/{id}/test

GET    /api/v1/cache/entries
POST   /api/v1/cache/evictions
GET    /api/v1/cache/jobs/{id}
GET    /api/v1/uploads/active
GET    /api/v1/uploads
DELETE /api/v1/uploads/{id}

GET    /api/v1/storage/summary
GET    /api/v1/storage/breakdown
GET    /api/v1/storage/settings
PUT    /api/v1/storage/settings
POST   /api/v1/storage/gc
POST   /api/v1/storage/fsck
GET    /api/v1/storage/jobs
GET    /api/v1/storage/jobs/{id}

GET    /api/v1/users
POST   /api/v1/users
PATCH  /api/v1/users/{id}
DELETE /api/v1/users/{id}

GET    /api/v1/tokens
POST   /api/v1/tokens
DELETE /api/v1/tokens/{id}

GET    /api/v1/audit
GET    /api/v1/system/info
```

`DELETE /api/v1/repositories/{id}`、manifest delete 和 tag delete 只作用于 local repository；cache repository 的清理统一使用 `POST /api/v1/cache/evictions`。管理 API 的写操作需要 `manage` 或 `admin` 权限，并写入审计。

统一错误结构：

```json
{
  "code": "manifest_invalid",
  "message": "manifest 校验失败",
  "details": {}
}
```

## 13. 代码结构

```text
cmd/crproxy/
  main.go
api/
  openapi.yaml
internal/
  bootstrap/          # 配置、SQLite、master key、初始管理员
  config/             # YAML、环境变量、校验
  httpapi/            # /v2 和 /api/v1 路由
  auth/
    password/         # Argon2id
    pat/              # PAT 生成、校验、scope
    session/          # Session、Cookie、CSRF
    jwt/              # Docker Registry JWT
    middleware/       # Registry/Admin 鉴权与权限矩阵
  registry/
    local/            # 本地 pull/push/delete/catalog/referrers
    proxy/            # 多上游 pull-through cache
    upload/           # blob upload session
    manifest/         # manifest 解析、校验、引用关系
    referrers/        # OCI referrers 索引
  upstreamauth/       # challenge、token cache、凭证加密
  routing/            # host/path -> local/upstream
  store/              # CAS、SQLite、迁移
  cache/              # LRU、quota、GC、fsck
  jobs/               # 后台任务
  metrics/            # Prometheus
  logging/            # 结构化日志、request id
web/
  package.json
  src/
  dist/
```

## 14. 配置示例

```yaml
server:
  public_host: registry.example.com
  listen: ":8443"
  tls:
    mode: file
    cert_file: /etc/crproxy/tls.crt
    key_file: /etc/crproxy/tls.key
    min_version: "1.2"
    redirect_http: true
    hsts: false
  trusted_proxies: []

routing:
  mode: host                 # host | path | both
  local_host: registry.example.com
  wildcard_domain: registry.example.com
  path_prefix: r

registry:
  local:
    enabled: true
    allow_delete: true
    allow_catalog: true
    allow_referrers: true
    strict_manifest_validation: true
  proxy:
    enabled: true
    tag_ttl: 60s
  uploads:
    ttl: 24h
    max_blob_bytes: 21474836480
    max_manifest_bytes: 4194304

auth:
  registry:
    challenge: bearer
    allow_basic: true
    allow_bearer: true
    service: crproxy
    token_ttl: 5m
    allow_anonymous: false
  web:
    session_ttl: 12h
    idle_timeout: 30m
    cookie_name: "__Host-crp_session"
    csrf_header: "X-CSRF-Token"
  pat:
    allow_as_basic_password: true
  passwords:
    argon2_memory_mib: 64
    argon2_iterations: 3
    argon2_parallelism: 2
  master_key_file: /etc/crproxy/master.key
  bootstrap_admin:
    username: admin
    password_env: CRPROXY_ADMIN_PASSWORD

storage:
  root: /var/lib/crproxy
  max_bytes: 214748364800
  cache_max_bytes: 107374182400
  min_free_bytes: 16106127360
  high_watermark: 0.85
  low_watermark: 0.70
  on_full: bypass
  download_concurrency: 8

upstreams_seed:
  - name: dockerhub
    endpoint: https://registry-1.docker.io
    route_host: dockerhub.registry.example.com
    repository_prefix: library
    auth_type: challenge
  - name: ghcr
    endpoint: https://ghcr.io
    route_host: ghcr.registry.example.com
    auth_type: challenge
  - name: quay
    endpoint: https://quay.io
    route_host: quay.registry.example.com
    auth_type: challenge
  - name: k8s
    endpoint: https://registry.k8s.io
    route_host: k8s.registry.example.com
    auth_type: anonymous
```

## 15. 可观测性

指标：

- `crproxy_build_info{version}`
- `crproxy_uptime_seconds`
- `crproxy_http_requests_total{method,route,status}`
- `crproxy_http_request_duration_seconds{method,route}`
- `crproxy_storage_physical_bytes`
- `crproxy_storage_local_bytes`
- `crproxy_storage_cache_bytes`
- `crproxy_storage_content_total`
- `crproxy_disk_available_bytes`
- `crproxy_storage_state`
- `crproxy_active_downloads`
- `crproxy_gc_running`
- `crproxy_upstream_health{id,name}`
- `crproxy_registry_events_24h{kind}`
- `crproxy_scrape_error`

审计：

- 登录、登出、失败登录
- 管理 API 的本地 manifest/repository/tag 删除
- blob upload 取消
- 上游 CRUD、测试、凭证更新
- GC、fsck、缓存淘汰
- 用户、PAT 和存储阈值变更

日志和审计不记录密码、PAT、Authorization、上游 token。

## 16. 测试策略

### 16.1 协议

- OCI Distribution Spec conformance pull。
- OCI Distribution Spec conformance push 和 content discovery。
- Distribution 作为行为对照。
- Podman `login`/`pull`/`tag`/`push`、按 digest 拉取和删除后行为。
- chunked upload、monolithic upload、resume、mount。
- catalog/tags 分页和 Link。
- referrers 和 OCI artifact。
- delete 与 GC 后行为。

### 16.2 本地 Registry

- manifest 校验、tag 覆盖、digest 不一致。
- blob 引用完整性。
- 并发 push 同一 blob/tag。
- upload 重启恢复和过期清理。
- 本地内容不被缓存 GC 淘汰。
- 删除引用后物理内容回收时机。

### 16.3 代理

- 多上游 routes。
- tag TTL 和条件请求。
- 并发 blob singleflight。
- 上游 token challenge、401 刷新。
- `HTTP_PROXY` 上游接管、HTTPS CONNECT 真实回源与 `NO_PROXY` 直连边界。
- 代理写操作 405。
- 缓存淘汰不影响本地内容。

### 16.4 前端

- Vitest 纯函数测试、全量 TypeScript 类型检查、Vite 生产构建。
- 页面权限由后端角色矩阵和 Go 集成测试兜底；不允许只靠隐藏按钮做授权。
- Podman 作为唯一镜像客户端完成登录、拉取、推送和代理只读冒烟。

### 16.5 发布验收门槛

以下门槛全部满足才算完成“完整 Registry 基线”，不能用前端页面完成或代理拉取成功替代：

- OCI Distribution Spec conformance：pull、push、content discovery 三组必须通过；catalog、tags、referrers 的 `Link`、`Content-Type` 和错误码符合规范。
- Podman：`podman login`、`pull`、`tag`、`push`、按 digest 拉取和删除后行为验证通过；不使用其他镜像客户端做冒烟。
- 上传：普通 upload、chunked upload、monolithic upload、mount、重启恢复、取消和过期清理全部覆盖。
- 代理：至少 Docker Hub、GHCR、Quay、registry.k8s.io 四个上游完成 manifest/blob/tags 冒烟；代理写请求全部返回 `405`。
- 隔离：缓存 LRU、手动清理和上游删除都不会影响本地内容；本地内容缺失时必须报告数据损坏而不是当作 cache miss。
- 运维：SQLite 无 WAL 启动、备份恢复、quick/full fsck、GC 中途崩溃恢复和磁盘 `507` 路径有测试。

## 17. 实施状态

以下里程碑均已落地；任何变更必须保持已通过的协议与安全行为不回退。

### M1：本地 Registry 核心（已完成）

- `/v2/`、manifest/blob pull、HEAD、Range。
- blob upload（普通、chunked、monolithic）。
- manifest PUT、tag、HEAD/GET。
- SQLite 迁移、CAS、上传会话。
- Basic/Bearer PAT/Docker JWT。
- pull/push conformance 基础用例，作为持续回归而非临时脚本。

### M2：完整协议能力（已完成）

- delete manifest/blob、catalog、tags 分页、referrers、mount。
- manifest 引用校验、GC、fsck。
- 本地/缓存引用分离，本地内容保护。
- OCI conformance 全量测试。

### M3：代理缓存（已完成）

- 多上游 Host/路径路由。
- Docker Hub、GHCR、Quay、registry.k8s.io。
- `HTTP_PROXY`/`HTTPS_PROXY` 出站代理、HTTPS CONNECT 和 `NO_PROXY`。
- tag TTL、条件请求、blob singleflight。
- 缓存配额、LRU、回源指标。

### M4：管理台和运维（已完成）

- React 管理台全部页面。
- 本地镜像、代理缓存、存储、用户、审计。
- 上游凭证加密、HTTPS、限流。
- 单二进制 embed、多阶段容器镜像、Compose、systemd、部署文档。
- Prometheus、健康/就绪探测、备份恢复、主密钥轮换、TLS 热加载。

## 18. 主要风险

- **协议复杂度**：push、upload、delete、referrers 的行为面远大于只读代理，必须以 OCI conformance 为验收标准。
- **上传状态**：chunk 边界、Range、重启恢复、并发和过期清理容易出错。
- **引用完整性**：本地 manifest、tag、blob、referrers 和 CAS 回收必须一致。
- **本地与缓存共存**：本地内容不能被缓存 LRU 淘汰；同一 digest 的引用要正确计费。
- **鉴权 scope**：push/delete 必须严格授权，代理路由必须只读。
- **SQLite 单写**：适合单实例；高并发 push 需要短事务和重试。
- **多实例限制**：本地 CAS 和 SQLite 不支持多实例共享。
- **开放代理风险**：默认关闭匿名拉取，必须配置用户、PAT 或仓库白名单。

## 19. 部署基线

1. 路径模式是默认方式；Host 模式需要通配 DNS/证书或逐主机配置。
2. 原生 TLS 和可信反向代理两种模式都受支持；反代必须配置 `public_scheme` 与 `trusted_proxies`。
3. 当前权限模型固定为 admin/operator/viewer + PAT scope；仓库级 ACL 不在本版本范围。
4. 当前版本固定为本地块存储、单实例；对象存储和多实例高可用不属于本版本交付，不得通过共享同一数据目录规避。
