# crproxy 发布验收基线

本文定义 crproxy v4 的发布门槛。它回答一个问题：**什么情况下可以宣称“完整本地 Registry + 多上游 Proxy Cache”基线完成？**

结论先行：前端页面完成、代理能拉取一个 Docker Hub 镜像、或手工 `podman push` 成功，都不足以证明基线完成。必须同时通过协议、客户端、鉴权、存储和代理隔离五组验收。

## 1. 不可妥协的基线

| 编号 | 基线 | 验收方式 |
| --- | --- | --- |
| B1 | 本地 Registry 完整实现 OCI Distribution Spec 1.1 | 官方 conformance pull/push/content discovery 通过 |
| B2 | 代理路由严格只读 | 所有写方法返回 `405`，上游无写请求 |
| B3 | 本地和代理共用 CAS 但不共享生命周期 | local 内容不受 cache LRU/清理影响 |
| B4 | 多上游按 Host 或路径解析 | 多上游 mock 协议测试通过；完成 GHCR/Quay 真实 Podman 冒烟；Docker Hub/registry.k8s.io 通过标准 HTTP(S) 代理真实回源 |
| B5 | 本地用户鉴权完整 | Basic、Bearer PAT、Docker token flow、Session/CSRF 测试通过 |
| B6 | SQLite 纯 Go、无 WAL | 启动参数和故障恢复测试通过 |
| B7 | 单实例、本地块存储 | 第二个进程不能同时写同一数据目录 |

## 2. 协议验收

### 2.1 OCI Distribution Spec conformance

以下三组必须全部通过，不能只跑 pull：

1. **Pull**：`/v2/`、manifest、blob、HEAD、Range、错误码。
2. **Push**：`POST` upload、`PATCH` chunk、`PUT` finalize、monolithic upload、manifest PUT。
3. **Content discovery**：tags/list、catalog、referrers、分页 `Link`、`artifactType` 过滤。

另外单独验证：

- Docker schema2 manifest 和 manifest list。
- OCI image manifest、image index、OCI artifact。
- foreign layer 的 `urls` 例外。
- image index 子 manifest 缺失时拒绝 push。
- manifest digest 与 reference 不一致时返回 `DIGEST_INVALID`。
- catalog 仅 `manage`/`admin` 可见。
- local referrers 无结果时返回 200 + 空 image index。

### 2.2 状态码基线

| 场景 | 期望 |
| --- | --- |
| 未认证且禁止匿名 | `401` + challenge |
| 已认证但缺 push/delete scope | `403 DENIED` |
| blob 不存在 | `404 BLOB_UNKNOWN` |
| manifest 不存在 | `404 MANIFEST_UNKNOWN` |
| 代理路由写操作 | `405`，且不进入 auth scope 授权 |
| 删除仍被 manifest 引用的 blob | `409` |
| manifest 超过配置上限 | `413` |
| chunk 起点不连续 | `416` |
| 磁盘不足且 `on_full=reject` | `507` |
| digest 不匹配 | `400 DIGEST_INVALID` |

## 3. 客户端验收

### 3.1 Podman

客户端冒烟只使用 Podman，不使用 Docker、containerd/nerdctl、crane、oras 或其他镜像客户端。

在本地 Registry Host 上逐项执行：

```bash
podman login registry.example.com
podman pull registry.example.com/library/nginx:latest
podman tag registry.example.com/library/nginx:latest registry.example.com/demo/nginx:test
podman push registry.example.com/demo/nginx:test
podman pull registry.example.com/demo/nginx@sha256:<digest>
```

还必须验证：

- Basic 密码登录。
- PAT 作为 Basic 密码登录。
- Bearer PAT 直接访问。
- Docker token flow 返回的 JWT 只包含本次 repository/action。
- 无 push 权限的用户 push 返回 403。
- tag 覆盖后新 digest 生效；旧 manifest 仍可按 digest 访问，显式删除后进入 GC 候选。

## 4. 上传与删除验收

- 普通 upload：POST → PATCH → PUT。
- 分块 upload：多个 PATCH，`Range` 连续，不允许跳块。
- monolithic upload：POST 携带 digest 和 body。
- upload 查询：GET 返回正确 `Range` 和 `Docker-Upload-UUID`。
- upload 取消：DELETE 后临时文件、SQLite 记录和审计都正确。
- 进程重启后 upload 可继续；超过 TTL 后清理。
- cross-repository mount 只增加引用，不复制物理文件。
- mount 源仓库不存在或无权访问时，返回 202 并开始普通 upload。
- `DELETE /manifests/<tag>` 返回 202，只删除 tag；manifest 仍可按 digest 拉取。
- `DELETE /manifests/<digest>` 返回 202，删除 manifest 引用和所有指向它的 tag，不误删共享 blob。
- `DELETE blob` 在仍被 manifest 引用时返回 409。
- repository delete 只移除本地引用，不立即 unlink 可能被别处共享的 CAS 文件。

## 5. 鉴权验收

- 角色矩阵：admin、operator、viewer 的页面、API 和 Registry 动作符合设计。
- PAT 有效权限 = 用户角色权限 ∩ PAT scopes。
- Docker JWT 只接受 HS256；拒绝 `none` 和其他算法。
- `/v2/_catalog` 仅 Basic/PAT/Session 的 manage/admin 可用。
- 代理 token 即使包含 push/delete，代理写请求仍返回 405。
- 登录限流、CSRF、Origin 校验、Session 轮换和密码修改后 session 失效。
- 上游匿名、Basic、静态 Bearer、challenge token 四种方式都有测试。
- 跨 Host 重定向不携带上游 Authorization；HTTPS 不降级。
- 审计不记录密码、PAT、Authorization、Session ID、CSRF 和上游 token。

## 6. 代理 Cache 验收

### 6.1 多上游

至少对以下上游完成 manifest、blob、tags/list 冒烟：

- Docker Hub：单段名称补 `library/`，`bitnami/nginx` 不补。
- GHCR：Bearer challenge / PAT。
- Quay：challenge / robot token。
- registry.k8s.io：匿名和重定向。
- Generic Registry：Harbor 或 Distribution，用于自定义 endpoint 验证。

### 6.2 只读与隔离

- 代理 Host 的 POST/PATCH/PUT/DELETE 全部返回 405。
- 代理写请求没有到达上游。
- 代理 `GET /v2/_catalog` 返回 404。
- 上游 referrers 404 保持 404；不伪造空 index。
- 同一个 blob 被 local 和 cache 引用时，物理 CAS 只有一份。
- cache LRU、手动 eviction、上游删除和 tag TTL 失效都不能删除 local 内容。
- cache 文件缺失时按 miss 回源；local 文件缺失时返回 500 并标记数据损坏。
- 同一 tag 的缓存 TTL、ETag/条件请求和并发 singleflight 有测试。
- 上游请求遵循 `HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY`；HTTP 绝对 URI 转发和 HTTPS CONNECT 隧道均有独立子进程自动化回归测试。

## 7. 存储与运维验收

- 数据目录包含 `meta/`、`cas/`、`uploads/`、`tmp/`、`trash/`，且 `uploads/`、`tmp/`、`cas/` 在同一文件系统。
- SQLite 使用 `modernc.org/sqlite`，`journal_mode=DELETE`，启动后不产生 `-wal`/`-shm`。
- `PRAGMA foreign_keys=ON`；cache repository 的 `namespace=upstream_id`，local repository 的 `namespace=local`。
- 删除 upstream 会级联清理其 cache repository 元数据，但 local repository 不受影响。
- upload ID 只允许安全字符，part 路径由 ID 派生，使用 `O_NOFOLLOW`，不能被路径穿越。
- quick fsck、full fsck、过期 upload 清理和 trash 恢复都有测试。
- GC 中途崩溃后重启能继续或清理 `deleting`/`trash` 状态。
- 磁盘 high/critical/emergency 状态、`on_full=bypass|reject` 和 `min_free_bytes` 都有测试。
- 第二个进程用同一数据目录启动会被 `LOCK` 拒绝。

## 8. 管理 API 与前端验收

- `POST /api/v1/auth/password` 可修改密码并使其他 session 失效。
- 本地镜像详情展示 tags、manifests、blobs、referrers 和 push 命令。
- 本地 manifest/tag/repository 删除与 409 引用冲突有明确错误提示。
- 代理缓存页不出现 push/Registry delete 入口；清理文案明确“不删除上游镜像”。
- 上游 CRUD、测试连接、凭证替换、自定义 CA 和跳过校验警告可用。
- 存储页展示本地/缓存用量、活跃 upload、GC/fsck 任务和磁盘水位。
- viewer 看不到写操作；系统设置页只读并对 operator 可见；operator 可管理上游、缓存和存储阈值，但不能管理用户或其他用户 Token。
- 前端以 `npm test`、`npm run typecheck`、`npm run build` 验证；客户端端到端冒烟由 Podman 完成。

## 9. 可观测性与发布证据

发布时至少保留以下证据：

- OCI conformance 报告和 commit/version。
- Podman 冒烟日志；不使用其他镜像客户端。
- 多上游代理冒烟记录，包含 405 写请求和 404 catalog 结果。
- 本地内容在缓存清理后仍可 pull 的证据。
- SQLite 无 WAL 的启动日志和 `PRAGMA` 检查结果。
- GC、fsck、磁盘不足和重启恢复测试记录。

## 10. 明确不在当前基线内

- OIDC、LDAP、SAML、外部用户目录。
- 对象存储、多实例共享缓存、分布式锁、HA。
- 镜像扫描、镜像复制、策略引擎、Harbor 式多项目管理。
- 仓库级 ACL、复杂路径重写、Docker schema1。

上述能力均为固定非目标，不属于当前交付或验收范围；它们不得被用来降低本地 Registry 协议、代理只读边界或安全验收标准。

## 11. 本轮验收证据

- 最终交付二进制 `bin/crproxy` 的 OCI Distribution Spec 1.1：`850 passed / 4 optional skips / 0 failed / 0 error`，详见 `docs/conformance-report.md`。
- Podman 5.8.9：登录、Quay 首拉与缓存命中、GHCR 拉取、本地 push、按 digest pull、PAT pull、代理 push `405`、原生 HTTPS + Basic 拉取与证书热加载、只读容器实例拉取与缓存命中，详见 `docs/podman-smoke.md`。
- 出站代理：服务端设置 `HTTP_PROXY/HTTPS_PROXY=http://127.0.0.1:1080` 后，Podman 真实拉取 Docker Hub `busybox:latest` 与 registry.k8s.io `pause:3.10` 成功，缓存命中分别约 `0.53s`、`0.56s`。
- `go test ./...`、`go test -race ./...`、`go vet ./...` 通过。
- `npm test`、`npm run typecheck`、`npm run build` 通过。
- 备份恢复、主密钥轮换、TLS 热加载、可信反代、存储配额、缓存淘汰与本地内容保护均有专项测试。
- 容器交付物以 UID `65532`、只读根文件系统、`cap-drop=ALL`、`no-new-privileges` 运行，健康检查、管理台、登录、代理拉取和缓存命中均通过。
- 当前环境直连 Docker Hub 会被重置、registry.k8s.io manifest 重定向目标会超时；通过标准 HTTP CONNECT 代理已完成两者真实 Podman 回源。部署环境的代理地址按 `docs/deployment.md` 配置，不需要修改上游代码。
