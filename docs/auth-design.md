# crproxy 鉴权与 TLS 详细设计

本文定义 crproxy 的客户端鉴权、管理台鉴权、上游鉴权、凭证保护、HTTPS、限流和审计。目标是同时满足 Docker/OCI 客户端和 Web 管理台，同时保持本地用户、单实例、SQLite 的轻量实现。

## 1. 范围与假设

### 1.1 范围

- Registry `/v2/` 客户端鉴权：Basic、Bearer PAT、Docker Registry token flow，覆盖 pull/push/delete。
- 管理台与 `/api/v1/`：Web Session、Basic、Bearer PAT、CSRF。
- 上游 Registry 鉴权：匿名、Basic、静态 Bearer、Bearer challenge token。
- 密码、PAT、Session、上游凭证的存储与加密。
- TLS、证书热加载、反向代理信任边界。
- 登录限流、失败处理、审计。
- 用户、角色、scope 和权限矩阵。

### 1.2 当前假设与边界

- 只使用 crproxy 本地用户和本地 PAT，明确不接入 OIDC、LDAP、SAML 或任何外部身份提供商。
- 不实现账号同步、用户组映射、SSO、单点登出和外部目录查询。
- 单实例部署，Session 和限流状态可以放在本地。
- 管理台和 Registry 使用同一个 HTTPS 入口。
- 密码和 PAT 支持作为 Docker Basic 密码使用。
- 默认关闭匿名拉取。
- 上游凭证按 upstream 粒度配置，不做仓库级凭证。

## 2. 身份与权限模型

### 2.1 用户角色

| 角色 | 查看管理台 | Pull | 本地 Push | 本地 Delete | 管理上游/缓存/存储 | 管理用户/Token | 查看系统设置快照 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| admin | 是 | 是 | 是 | 是 | 是 | 是 | 是 |
| operator | 是 | 是 | 是 | 是 | 是 | 否 | 是 |
| viewer | 是 | 是 | 否 | 否 | 否 | 否 | 否 |

系统设置页只读，仅展示运行时、端点和存储摘要，不提供写接口。存储阈值属于“管理上游/缓存/存储”的运维能力，由 admin 和 operator 在“存储与磁盘”页修改并写入审计。权限模型固定按角色控制，不实现仓库级 ACL。代理上游对所有允许 pull 的用户只读；本地仓库的 push/delete 仅 admin 和 operator 可用。

### 2.2 Scope

PAT 使用以下 scope：

```text
pull      # 访问 /v2 拉取
push      # 推送本地 blob 和 manifest
delete    # 删除本地 manifest/blob/tag
manage    # 管理上游、缓存、存储和任务
admin     # 管理用户、其他用户 Token 和审计
```

规则：

- Session 和 Basic 使用用户角色推导权限。
- PAT 的有效权限 = 用户角色权限 ∩ PAT scopes。
- 创建 PAT 时只能授予当前用户自身拥有的权限。
- viewer 只能创建 `pull` token。
- operator 可以创建 `pull`、`push`、`delete`、`manage` token，不能创建 `admin` token。
- admin 可以创建全部 scope。
- Docker JWT 只包含 repository 的 pull/push/delete scope，不包含管理权限。
- 代理上游路由即使 JWT 包含 push/delete，也只允许 pull；写方法返回 `405`。
- `/v2/_catalog` 不通过 repository scope 授权；要求 Basic、PAT 或管理台 Session，并且用户具备 `manage` 或 `admin`。
- 本地 Registry 的 push/delete 必须同时校验角色权限和 PAT/JWT scope；代理路由不因为没有 push 权限而返回 403，而是直接返回 405。

## 3. 客户端鉴权总览

```text
                         ┌──────────────────────────────┐
                         │ /v2/*                        │
Docker/OCI Client ──────▶│  Basic?                      │
                         │  Bearer PAT?                 │
                         │  Bearer Docker JWT?          │
                         │  anonymous?                  │
                         └──────────────┬───────────────┘
                                        │
                                        ▼
                         ┌──────────────────────────────┐
                         │ /v2/token                    │
                         │  Basic username/password     │
                         │  Basic username/PAT          │
                         │  Bearer PAT                  │
                         └──────────────────────────────┘
```

### 3.1 配置

推荐配置：

```yaml
auth:
  registry:
    challenge: bearer       # bearer | basic
    allow_basic: true
    allow_bearer: true
    allow_anonymous: false
    service: crproxy
    token_ttl: 5m
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
  bootstrap_admin:
    username: admin
    password_env: CRPROXY_ADMIN_PASSWORD
  master_key_file: /etc/crproxy/master.key
```

`challenge` 只决定未认证时返回哪种 `WWW-Authenticate`。无论选择哪种 challenge，服务端都接受 Basic 和 Bearer：

- `challenge: bearer`：推荐，兼容 Docker token flow。
- `challenge: basic`：适合只支持 Basic 的简单客户端。
- 不接受同时发送多个 challenge，避免不同客户端的选择行为不一致。
- `allow_basic` / `allow_bearer` 可以显式关闭某种凭据。

## 4. Basic 鉴权

### 4.1 适用范围

- `/v2/*`：Docker 客户端直接发送 Basic。
- `/v2/token`：Docker 用 Basic 凭据换取短期 JWT。
- `/api/v1/*`：CI、脚本和自动化直接调用管理 API。
- Web 登录页不使用 Basic challenge，而是 JSON 登录接口。

### 4.2 校验流程

```text
Authorization: Basic base64(username:password)
  -> 解析并限制长度
  -> 查询用户（大小写敏感）
  -> 用户不存在时执行一次 dummy Argon2 校验，避免时序枚举
  -> 用户被禁用则拒绝
  -> 如果 password 以 "crp_" 开头且允许 PAT-as-password：
       校验 PAT 哈希、scope、过期和撤销状态
     否则：
       校验 Argon2id 密码哈希
  -> 成功：绑定用户身份、角色和 PAT scope（如有）
```

要点：

- Argon2id 参数默认 `m=64MiB, t=3, p=2`，格式使用 PHC 字符串。
- 密码最大长度限制，避免超大输入导致 Argon2 资源消耗。
- 所有哈希比较使用 `subtle.ConstantTimeCompare` 或库提供的安全比较。
- Basic 失败统一返回 `401 invalid credentials`，不区分用户不存在、密码错误或用户禁用。
- `WWW-Authenticate` 中不暴露账号是否存在。

### 4.3 PAT 作为 Basic 密码

Podman 用户常见写法：

```bash
podman login registry.example.com -u alice -p crp_xxx
```

当前默认支持：

- Basic 密码如果是 `crp_...`，按 PAT 校验。
- 认证主体仍然是 PAT 所属用户。
- 权限取 PAT scope，而不是用户角色全集。
- 审计记录认证来源 `basic_pat`。

如果关闭 `pat.allow_as_basic_password`，PAT 只能通过 `Authorization: Bearer` 使用。

## 5. Bearer 与 Docker Token Flow

### 5.1 两类 Bearer

| 类型 | 格式 | 用途 | 校验方式 |
| --- | --- | --- | --- |
| PAT | `crp_<id>_<secret>` | CLI、自动化、管理 API、Registry | SQLite 哈希查询 |
| Registry JWT | 三段式 JWT | `/v2/*` 短期 pull/push/delete | HMAC 签名、claims、scope |

管理 API 不接受 Registry JWT，避免把短期 Registry token 当作管理凭据。管理 API 的 Bearer 只接受 PAT。

### 5.2 Token flow

未认证访问 `/v2/` 时，如果 `challenge=bearer`：

```http
HTTP/1.1 401 Unauthorized
Docker-Distribution-Api-Version: registry/2.0
WWW-Authenticate: Bearer realm="https://registry.example.com/v2/token",service="crproxy",scope="repository:library/nginx:pull,push"
```

Docker 随后请求：

```http
GET /v2/token?service=crproxy&scope=repository:library/nginx:pull,push
Authorization: Basic base64(alice:password)
```

服务端返回：

```json
{
  "token": "<jwt>",
  "access_token": "<jwt>",
  "expires_in": 300,
  "issued_at": "2026-11-26T00:00:00Z"
}
```

之后客户端携带：

```http
Authorization: Bearer <jwt>
```

### 5.3 Token endpoint 规则

```text
GET /v2/token
```

查询参数：

- `service`：必须等于配置的 `auth.registry.service`，默认 `crproxy`。
- `scope`：可重复；当前只支持 `repository:<repo>:pull`、`pull,push`、`push,delete`。`registry:catalog:*` 不签发，catalog 使用 Basic/PAT/Session 的 `manage` 或 `admin` 权限。
- `account`：可选，用于兼容；不作为身份依据。
- `offline_token`：忽略。

认证方式：

- Basic 用户名密码。
- Basic 用户名 + PAT。
- Bearer PAT。

行为：

1. 校验调用者身份和所请求的 pull/push/delete 权限。
2. 校验每个 scope 的 repository 格式。
3. 授权要求：用户角色允许对应 action，且 PAT scope 包含对应 action。
4. 全部 scope 都授予，或整体返回 `403`；不做部分授权，避免客户端误判。
5. 生成 JWT，TTL 默认 5 分钟，最大不超过 15 分钟。
6. 响应不得包含刷新 token。
7. 失败返回 `401` 或 `403`，并按 IP / 用户限流。

Realm 使用当前请求的 Registry Host（本地 Host 或代理 Host），保证 Docker 在代理路由上也能完成 token flow；`/v2/token` 在本地和代理 Host 都可用。代理 token 即使请求了 `push`，后续写请求仍会在鉴权前被路由层拒绝为 `405`。

路径代理模式下，客户端看到的 repository 是 `r/<slug>/<repo>`，Docker 计算出的 scope 也是这个完整路径。JWT `access.name` 必须保存客户端可见路径，路由层再解析 upstream；不能只签上游 repo 名，否则会和客户端请求的 scope 不匹配。

### 5.4 JWT claims

```json
{
  "iss": "crproxy",
  "aud": "crproxy-registry",
  "sub": "user-id",
  "iat": 1764115200,
  "nbf": 1764115190,
  "exp": 1764115500,
  "jti": "random-id",
  "auth_source": "password",
  "access": [
    {
      "type": "repository",
      "name": "library/nginx",
      "actions": ["pull", "push"]
    }
  ]
}
```

校验规则：

- 只接受 `HS256`，拒绝 `none` 和其他算法。
- 校验 `iss`、`aud`、`exp`、`nbf`、`iat`，允许 30 秒时钟偏差。
- `access` 中必须包含当前请求 repository 和操作对应的 action。
- 代理上游路由只认可 `pull`；即使 token 含 push，写请求仍返回 `405`。
- JWT 不写入数据库；撤销依赖短 TTL。
- Registry JWT 使用独立的 `auth.jwt_secret_file`，默认 `<storage.root>/meta/jwt.secret`，至少 32 字节。
- JWT 签名密钥不写入 SQLite，也不由上游凭证 master key 派生；替换该文件并重启会使现有短期 JWT 全部失效。

### 5.5 Bearer PAT 校验

```text
Authorization: Bearer crp_<id>_<secret>
  -> 解析 id
  -> 按 id 查询 api_tokens
  -> 检查 revoked_at / expires_at / 用户 enabled
  -> 计算 secret_hash，常量时间比较
  -> 计算 PAT 有效 scope
  -> 更新 last_used_at（批量）
```

PAT 结构建议：

```text
crp_<12 字符 id>_<32 字节随机 secret 的 base64url>
```

- 完整 token 只在创建时返回一次。
- 数据库保存 SHA-256(secret)，不保存明文。
- 令牌高熵，SHA-256 足够；不引入 Argon2，避免每次请求高成本。
- UI 只显示前缀，例如 `crp_ab12cd34_...`。
- 支持设置过期时间；默认不过期但 UI 建议 90 天。
- 撤销立即生效；已签发的 Registry JWT 最多存活 token TTL。

### 5.6 Registry authorization

`/v2/*` 路由统一经过 authorization：

- 无凭据且允许匿名：按匿名身份放行 pull。
- 无凭据且不允许匿名：返回 `401` + challenge。
- 代理路由的写方法：直接返回 `405`，不进入 auth scope 授权。
- Basic 或 Bearer PAT：按方法检查角色或 PAT scope 包含 pull/push/delete。
- Bearer JWT：检查 `access` 中包含当前 repository 和操作对应的 action。
- 本地 push：需要 `push`；本地 delete：需要 `delete`；catalog：需要 `manage` 或 `admin`。
- catalog 不使用 repository scope；Basic/PAT 按角色和 PAT scope 判断，Session 按角色判断。
- 已认证但无权限：返回 `403 DENIED`，不再次 challenge。
- 代理仓库不存在或无权访问时透传上游 `404`，不额外暴露上游私有仓库是否存在。
- blob 仍被同 repository manifest 引用时删除返回 `409`；磁盘空间不足且不允许 bypass 时返回 `507`。

Registry 错误格式：

```json
{
  "errors": [
    {
      "code": "UNAUTHORIZED",
      "message": "authentication required",
      "detail": [
        { "Type": "repository", "Name": "library/nginx", "Action": "pull" }
      ]
    }
  ]
}
```

响应头：

```text
Docker-Distribution-Api-Version: registry/2.0
WWW-Authenticate: Bearer realm="...",service="crproxy",scope="repository:<repo>:pull,push"
```

## 6. 管理台与 Web Session

### 6.1 登录

```http
POST /api/v1/auth/login
Content-Type: application/json

{
  "username": "alice",
  "password": "secret"
}
```

成功：

```http
HTTP/1.1 200 OK
Set-Cookie: __Host-crp_session=<random>; Path=/; Secure; HttpOnly; SameSite=Lax; Max-Age=43200

{
  "user": { "id": "...", "username": "alice", "role": "admin" },
  "csrfToken": "<random>"
}
```

失败：

```json
{
  "code": "invalid_credentials",
  "message": "用户名或密码错误"
}
```

规则：

- 用户不存在和密码错误返回同一错误。
- 登录失败按 IP + 用户名限流。
- 登录成功后生成全新 session ID，防止 session fixation。
- 记录登录审计，不记录密码。
- 首次登录或被管理员重置密码后要求修改密码。

### 6.2 Session 存储

```sql
CREATE TABLE sessions (
  id_hash TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  csrf_hash TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  idle_expires_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  ip TEXT,
  user_agent TEXT
);
```

- Cookie 中保存随机 session ID，数据库只保存 SHA-256 哈希。
- CSRF token 也保存哈希。
- 默认绝对有效期 12 小时，空闲超时 30 分钟。
- 每次请求更新 `last_seen_at` 和 `idle_expires_at`，可节流为每 60 秒写一次。
- 后台定时清理过期 session。
- 退出登录、修改密码、管理员禁用用户时删除对应 session。

### 6.3 Cookie 属性

生产环境：

```text
__Host-crp_session=<random>; Path=/; Secure; HttpOnly; SameSite=Lax
```

- `__Host-` 前缀要求 Secure、Path=/、无 Domain，降低子域覆盖风险。
- HTTP 开发环境可使用 `crp_session`。
- 不使用 localStorage 保存 session 或 PAT。

### 6.4 CSRF

管理 API 中：

- `GET`、`HEAD`、`OPTIONS` 不要求 CSRF。
- `POST`、`PUT`、`PATCH`、`DELETE` 使用 Cookie 认证时，必须携带 `X-CSRF-Token`。
- 服务端比较 `X-CSRF-Token` 与会话中的 `csrf_hash`。
- 同时校验 `Origin` / `Referer` 是否在允许列表中。
- Basic 和 Bearer 认证不依赖 Cookie，不需要 CSRF，但必须禁止浏览器自动携带这些凭据。
- 登录接口校验 Origin，登录成功后轮换 CSRF token。

### 6.5 Session API

```text
POST /api/v1/auth/login
POST /api/v1/auth/logout
GET  /api/v1/auth/session
POST /api/v1/auth/password
```

`GET /api/v1/auth/session` 返回当前用户、角色和 CSRF token，供前端刷新页面后恢复。

`POST /api/v1/auth/password` 要求当前密码和 CSRF；成功后：

- 删除其他 session。
- 按配置撤销所有 PAT，或保留现有 PAT 但记录审计。
- 清除 `must_change_password`。
- 记录审计。

## 7. 用户、密码与 PAT 管理

### 7.1 用户表

```sql
CREATE TABLE users (
  id TEXT PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  role TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  must_change_password INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  password_changed_at INTEGER,
  last_login_at INTEGER
);
```

用户规则：

- 用户名长度 1-64，只允许字母、数字、点、下划线、连字符。
- 密码最少 12 字符，建议 16；禁止只由用户名组成。
- 用户创建、禁用、角色变更、密码重置都写审计。
- 禁用用户立即失效所有 session；PAT 校验时检查用户状态。
- 不允许删除最后一个 admin。

### 7.2 PAT 表

```sql
CREATE TABLE api_tokens (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  prefix TEXT NOT NULL,
  secret_hash TEXT NOT NULL,
  scopes TEXT NOT NULL,
  expires_at INTEGER,
  created_at INTEGER NOT NULL,
  last_used_at INTEGER,
  last_used_ip TEXT,
  revoked_at INTEGER
);
```

- 一个用户最多配置 20 个活跃 PAT（可调整）。
- 同一用户下 token 名称唯一。
- 创建时校验 scope 不超过用户权限。
- 撤销后立即拒绝；已签发 JWT 无法撤销，等待过期。
- 管理台仅显示前缀、scope、过期时间和最近使用。
- PAT 不得写入 URL 查询参数，避免进入访问日志。

### 7.3 初始管理员

启动时：

1. 如果 `users` 表为空，读取 `bootstrap_admin.username` 和 `password_env`。
2. 创建 admin，设置 `must_change_password=1`。
3. 环境变量不存在或密码过短则启动失败，不生成默认密码。
4. 初始化完成后不再使用该环境变量。
5. 记录初始化审计。

不建议自动生成密码打印到日志，避免日志泄露。

## 8. 上游鉴权

### 8.1 类型

```text
anonymous   无凭证；遇到 Bearer challenge 时请求匿名 token
basic       配置用户名/密码；支持直接 Basic 和 token service
bearer      配置静态 Bearer token；直接发送给配置的 endpoint
```

上游凭证表单字段：

- username
- password
- bearer_token
- 可选 account

凭证更新后：

- 清空该 upstream 的 token cache。
- 递增 `credentials_version`。
- 立刻使用新凭证请求；失败不自动回退旧凭证。

### 8.2 上游 challenge 流程

```text
1. 按配置发送请求（无凭证 / Basic / 静态 Bearer）
2. 如果 200/304/404：结束
3. 如果 401：
   a. 解析 WWW-Authenticate，可能有多个 challenge
   b. 优先处理 Bearer challenge
   c. realm 必须是 https（insecure 模式除外）
   d. 请求 token：GET realm?service=...&scope=repository:<repo>:pull&account=<username>
   e. 使用 Basic 凭证或无凭证请求 token
   f. 解析 token / access_token / expires_in / issued_at
   g. 缓存 token
   h. 用 Bearer token 重试原请求一次
4. 重试仍 401/403：返回 502 upstream_auth_failed
```

解析 `WWW-Authenticate` 时要支持：

- 多个 challenge 头。
- 引号内的逗号。
- `realm`、`service`、`scope`、`error` 参数。
- 无 scope 时按当前 repository 构造 `repository:<repo>:pull`。
- token 服务返回 `token` 或 `access_token`。
- 缺失 `expires_in` 时使用保守 TTL，例如 60 秒。

### 8.3 Token cache

Key：

```text
upstream_id + credentials_version + realm + service + scope + username
```

保存：

- token 只存在于进程内存，不落盘。
- expires_at。
- issued_at。
- 最后使用时间。

规则：

- 提前 30 秒刷新。
- 同一个 key 使用 singleflight，避免并发 token 请求。
- 收到 401 且错误为 `invalid_token` 时，删除缓存并重试一次。
- 凭证更新、upstream 禁用、进程重启时清空。
- 不落盘、不写日志。

### 8.4 重定向与凭证泄漏

- 上游 blob 可能 307 到 CDN。
- 跨 Host 重定向必须删除 `Authorization`。
- 不把 CDN 地址返回给客户端。
- token realm 如果与上游不同 Host，仅发送 token 请求所需凭据，不发送任意额外 header。
- 禁止从 HTTPS 重定向到 HTTP（除非 upstream 显式 insecure）。

### 8.5 上游 TLS

每个 upstream 支持：

```yaml
tls:
  ca_pem: |
    -----BEGIN CERTIFICATE-----
  insecure_skip_verify: false
```

- 默认使用系统 CA。
- 自定义 CA 与系统 CA 合并。
- `insecure_skip_verify` 仅在受控环境使用，UI 必须显示警告。
- 上游连接最低 TLS 1.2。
- token realm 使用同一套 TLS 配置。

## 9. 凭证加密与主密钥

### 9.1 主密钥

- 上游凭证 master key 是带版本的 keyring，路径由 `auth.master_key_file` 配置，默认 `<storage.root>/meta/master.key`。
- 首次启动原子创建 0600 权限文件；每个 key version 为 32 字节随机值，文件只保存 base64 编码，不写入 SQLite，不写日志。
- keyring 只用于上游凭证 AES-256-GCM 加密。Registry JWT 和 CSRF 使用独立 `jwt.secret`，两者互不派生。
- 丢失 keyring 中所有相关 key version 将无法解密上游凭证，需要重新录入；备份必须同时包含 keyring 和数据库。

### 9.2 加密格式

```text
nonce(12) || ciphertext || tag(16)
AAD = "crproxy:upstream:<id>:credentials:<version>"
```

- 每次保存生成新的 nonce，禁止复用。
- 密文本身不内嵌 key version；`upstreams.credentials_key_version` 选择解密 key，AAD 中的 credential version 防止密文被重放到同一上游的其他版本。
- 解密失败按 `upstream_auth_unavailable` 处理，不返回密文细节。
- 内存中的明文缓冲区在使用后尽量清零。

### 9.3 轮换

提供 CLI：

```bash
crproxy rotate-master-key -config /etc/crproxy/crproxy.yaml
```

流程：

1. 停止服务并获取数据目录 `LOCK`；按运维要求先备份数据目录。
2. 如果所有上游凭证已使用当前 key version，则生成下一个 key version；否则继续上次中断的轮换。
3. 用原 key version 解密所有 upstream 凭证，再用目标 key version 重新加密并增加 credential version。
4. 旧 key version 保留在 keyring 中，避免中断后无法解密；轮换成功后由运维策略归档。
5. 写入 `master_key.rotate` 审计并重启服务。

master key 轮换不影响独立的 JWT 签名密钥；只有替换 `jwt.secret` 才会让现有短期 Registry JWT 失效。

## 10. HTTPS 与代理信任

### 10.1 原生 TLS

```yaml
server:
  tls:
    mode: file
    cert_file: /etc/crproxy/tls.crt
    key_file: /etc/crproxy/tls.key
    min_version: "1.2"
    redirect_http: true
    hsts: true
```

- 默认最低 TLS 1.2，推荐 1.3。
- 证书加载失败时启动失败，不静默降级到 HTTP。
- 支持 SIGHUP 热加载证书。
- 证书过期前 30 天产生告警指标。
- HSTS 仅在确认所有入口都支持 HTTPS 后开启。
- HTTP 入口只允许重定向和健康检查，不允许登录或 Registry 明文访问。

### 10.2 反向代理模式

```yaml
server:
  tls:
    mode: proxy
  trusted_proxies:
    - 10.0.0.0/8
    - 192.168.0.0/16
```

- 只信任配置 CIDR 发来的 `X-Forwarded-Proto`、`X-Forwarded-Host`、`X-Forwarded-For`。
- 非可信来源的转发头直接忽略。
- 使用真实客户端 IP 做限流和审计。
- 反向代理必须传递 Host，crproxy 仍按 Host 路由。
- 生产环境要求 `X-Forwarded-Proto=https`，否则 Secure Cookie 不生效。

### 10.3 Secure Cookie 判断

- 原生 TLS：始终 Secure。
- 反向代理：仅当可信代理标记 HTTPS 时 Secure。
- 非 HTTPS 环境：开发模式允许非 Secure Cookie，并在启动日志中警告。
- 生产配置检测到 HTTP 时拒绝启动，除非显式 `allow_insecure_http: true`。

## 11. 限流、失败处理和审计

### 11.1 限流

建议默认：

| 场景 | 限制 |
| --- | --- |
| 登录失败 | 每 IP + 用户名 5 次 / 15 分钟 |
| Token endpoint | 每 IP 60 次 / 分钟 |
| Registry 匿名 | 每 IP 600 次 / 分钟 |
| 管理 API | 每用户 300 次 / 分钟 |

- 失败登录采用指数退避。
- 触发限流返回 `429 Too Many Requests` 和 `Retry-After`。
- 限流状态保存在进程内存；重启后清空。
- 不要因为单账号失败锁定所有 IP，避免 DoS。
- 同时按 IP 和账号维度统计。

### 11.2 审计事件

```text
auth.login.success
auth.login.failure
auth.logout
auth.password.change
auth.token.create
auth.token.revoke
user.create
user.update
user.delete
user.password.reset
user.bootstrap
upstream.create
upstream.update
upstream.delete
upstream.test
manifest.delete
tag.delete
repository.delete
upload.cancel
cache.evict
storage.gc
storage.fsck
storage.settings.update
master_key.rotate
```

审计字段：

- 时间
- actor type / actor id
- action
- target type / target id
- IP
- user agent
- result
- request id
- message

禁止记录：

- 密码
- PAT 完整值
- Session ID
- CSRF token
- Authorization header
- 上游 Bearer token
- 上游 token service 响应

### 11.3 错误响应

管理 API：

```json
{
  "code": "invalid_credentials",
  "message": "用户名或密码错误"
}
```

Registry：

```json
{
  "errors": [
    { "code": "UNAUTHORIZED", "message": "authentication required" }
  ]
}
```

映射：

| 情况 | 状态码 |
| --- | --- |
| 无凭据 / 凭据错误 | 401 |
| 已认证但无 scope | 403 |
| 代理路由写操作 | 405（路由层直接判定） |
| blob 仍被同 repository manifest 引用 | 409 |
| manifest/请求体超过上限 | 413 |
| upload chunk 不连续 | 416 |
| 登录/Token 限流 | 429 |
| CSRF 校验失败 | 403 |
| 上游鉴权失败 | 502 |
| 上游仓库不存在 | 404 |
| 磁盘空间不足且配置为 reject | 507 |
| 服务端内部错误 | 500 |

## 12. 安全威胁与对策

| 威胁 | 对策 |
| --- | --- |
| 开放代理被滥用 | 默认关闭匿名；角色/PAT scope；仓库 allowlist 可扩展 |
| 密码爆破 | Argon2id、登录限流、指数退避、统一错误 |
| 时序枚举用户 | 用户不存在时执行 dummy Argon2 |
| Token 泄漏 | PAT 只显示一次、SHA-256 存储、可撤销、短 TTL JWT |
| Session 劫持 | HttpOnly + Secure + SameSite；Session ID 哈希存储；登录轮换 |
| XSS 窃取 Token | 不存 localStorage；管理台使用 HttpOnly Cookie；CSP |
| CSRF | SameSite + X-CSRF-Token + Origin 校验 |
| JWT 算法混淆 | 只允许 HS256，严格校验 iss/aud/exp/nbf |
| SSRF | upstream endpoint 固定配置；token realm 只接受上游挑战返回的 HTTPS 地址 |
| 凭证经重定向泄漏 | 跨 Host 删除 Authorization；禁止 HTTPS 降级 |
| 反向代理伪造 IP | 只信任配置 CIDR 的转发头 |
| 上游凭证落盘泄露 | AES-GCM 加密；master key 独立挂载 |
| 日志泄密 | 统一脱敏 Authorization、Cookie、token、password |
| 拒绝服务 | 请求体限制、密码长度限制、登录限流、下载并发限制 |

## 13. 数据库模式归属与迁移

`docs/storage-design.md` 是数据库 schema 的唯一权威来源；鉴权文档不再维护第二套表定义。

- 全新部署的 v4 schema 已包含 `users.must_change_password`、`users.password_changed_at`、`api_tokens.last_used_ip`、`upstreams.credentials_version` 和 `upstreams.credentials_key_version`，不需要执行 `ALTER TABLE`。
- 这些字段只用于从早期草案升级的迁移步骤，不应在启动时无条件执行。
- 使用 `schema_meta.version` 做顺序迁移；每个迁移在事务中执行，迁移失败则拒绝启动，避免半迁移状态。
- 鉴权模块只依赖 schema 版本和存储层接口，不在请求路径上做隐式 DDL。

如果从 v2 草案升级，迁移顺序为：先创建/确认基础表，再补充上述列，最后写入新的 `schema_meta.version`；迁移脚本由存储层集中管理，不放在鉴权初始化逻辑里。

## 14. 代码结构建议

```text
internal/auth/
  password/
    argon2.go          # PHC 编码、校验、dummy hash
  pat/
    token.go           # 生成、解析、哈希、scope
  session/
    session.go         # 登录、Cookie、CSRF、清理
  jwt/
    jwt.go             # Docker token 签发和校验
  middleware/
    registry.go        # Basic/Bearer/PAT/JWT/anonymous
    admin.go           # Session/Basic/PAT + CSRF
    permissions.go     # 角色与 scope 矩阵
internal/upstreamauth/
  challenge.go         # WWW-Authenticate 解析
  token.go             # token endpoint 请求和缓存
  cache.go
  crypto.go            # 上游凭证 AES-GCM
internal/tlsconfig/
  server.go
  client.go
```

依赖建议：

- `golang.org/x/crypto/argon2`
- `github.com/golang-jwt/jwt/v5`
- 标准库 `crypto/aead`、`crypto/hmac`、`crypto/rand`、`crypto/sha256`
- 标准库 `net/http`，不引入认证框架

## 15. 测试矩阵

### 15.1 单元测试

- Argon2 PHC 编码、校验、dummy hash。
- PAT 生成、解析、过期、撤销、scope 交集。
- JWT 签发、算法限制、aud/iss/exp/nbf 校验。
- Basic 中 PAT-as-password。
- Session Cookie、CSRF、Origin 校验、过期和轮换。
- `WWW-Authenticate` 多 challenge 解析。
- 上游 token cache、401 重试和 singleflight。
- AES-GCM 加解密、nonce 唯一性、AAD 错误。
- 限流器和 Retry-After。

### 15.2 集成测试

- `podman login` + `podman pull`：
  - Bearer challenge。
  - Basic challenge。
  - Basic + PAT 密码。
  - Bearer PAT。
- 本地 `podman push`：
  - Basic 密码。
  - Basic + PAT 密码。
  - Bearer token flow，scope 包含 push。
  - 无 push 权限返回 403。
- 本地 manifest/blob/tag delete 和 delete scope。
- catalog 仅允许 `manage`/`admin`；viewer 和普通 pull token 返回 403。
- blob 仍被 manifest 引用时 delete 返回 409。
- 磁盘空间不足且 `on_full=reject` 时 upload/cache 返回 507。
- 代理 Host 的 push/delete 返回 405。
- `/v2/token` 的合法、无权限、过期、错误 service/scope。
- 管理台登录、退出、刷新 session、CSRF 失败。
- 管理员禁用用户后 session 和 PAT 立即失效。
- 修改密码后其他 session 失效。
- 上游匿名、Basic、静态 Bearer、Bearer challenge。
- 上游 token 过期后自动刷新。
- 上游 401 后返回 502。
- HTTPS 证书热加载、Secure Cookie、HSTS。
- 反向代理转发头只信任白名单。
- 登录限流和审计内容脱敏。

### 15.3 兼容性测试

- Podman（唯一客户端冒烟工具）。
- 至少一个标准 Registry token service 实现。
- GitHub Container Registry、Docker Hub、Quay 的 token 流程冒烟测试。

## 16. 实施顺序

1. 用户表、Argon2 密码、初始管理员。
2. Basic 认证中间件和角色权限矩阵。
3. PAT 生成、校验、撤销。
4. Web Session、CSRF 和登录页 API。
5. Docker token endpoint 和 JWT。
6. 上游 challenge 解析、token cache、Basic/Bearer 静态凭证。
7. AES-GCM 凭证加密和 master key。
8. 原生 TLS、证书热加载、反向代理信任。
9. 登录限流、审计、错误脱敏。
10. 集成测试和 Podman 冒烟测试。
