# OCI Distribution / Registry V2 协议设计

本文定义 crproxy 的完整 Registry 协议实现。基线不是“只读 pull-through cache”，而是：

- 本地完整 OCI Distribution Registry，支持 pull、push、delete、catalog、tags、referrers。
- 多上游 pull-through cache，代理侧保持只读。
- 本地仓库与代理缓存共用同一套 CAS 存储，但生命周期和权限分开。

“完整”指协议行为完整，不指复制 Distribution 的全部运行组件。实现以 OCI Distribution Spec 1.1 为准，Distribution 只作为行为对照和测试 oracle；不 fork、不嵌入、不作为 sidecar 或运行时依赖。

## 1. 协议目标

以 OCI Distribution Spec 1.1 为主，同时兼容 Docker Registry HTTP API V2 的线协议行为。任何符合规范的 OCI 客户端都可以接入；当前发布验收的客户端证据只使用 Podman，并由 OCI Distribution Spec conformance tests 验证协议面。

实现策略：

- **本地 Registry 路由**：实现完整 pull + push + delete + catalog + tags + referrers。
- **代理上游路由**：只实现 pull、tags/list、referrers 透传；所有写操作返回 `405`。
- 代理路由不是上游的透明写代理，也不把缓存仓库暴露为本地可写仓库。
- 不支持 Docker schema1。
- 不支持跨实例复制、镜像扫描和策略引擎。

## 2. 路由与命名空间

### 2.1 Host 模式

```text
registry.example.com              -> 本地 Registry（可 push / pull / delete）
dockerhub.registry.example.com    -> Docker Hub 代理（只读）
ghcr.registry.example.com         -> GHCR 代理（只读）
quay.registry.example.com         -> Quay 代理（只读）
k8s.registry.example.com          -> registry.k8s.io 代理（只读）
```

本地 Registry 和代理缓存使用不同的 Host，避免客户端误以为可以 push 到代理上游。

### 2.2 路径模式

```text
registry.example.com/<repo>          -> 本地 Registry
registry.example.com/r/<slug>/<repo> -> 代理上游（只读）
```

规则：

- `r` 是保留的第一段路径，不允许作为本地 repository 的第一段。
- 路径模式下仍优先按 Host 匹配；只有未匹配到代理 Host 时才进入本地 Registry。
- 代理路由的写方法统一返回 `405 Method Not Allowed`。

### 2.2.1 上游 repository 名称映射

代理路由需要区分“客户端看到的 repository”和“上游实际路径”：

- `repositories.name` 保存客户端请求的 repository；cache repository 的 `namespace` 等于 upstream ID。
- `repository_prefix` 只对单段名称生效。例如 Docker Hub 配置 `repository_prefix=library` 时，`nginx` 映射为 `library/nginx`，`bitnami/nginx` 保持不变。
- 同一客户端名称在 tag TTL 内映射稳定；不根据上游返回的 `name` 反向改写本地 cache key。
- tags/list 响应中的 `name` 返回客户端请求的 repository 名，避免 Docker 客户端看到 `library/` 前缀后行为不一致。
- 上游 endpoint 只能来自预置或管理员配置；不能把请求 URL 当作上游地址。
- 路径模式下客户端可见 repository 是 `r/<slug>/<repo>`，token scope 和 JWT `access.name` 都使用这个完整路径；路由层再解析 slug 和 upstream。

这允许 `nginx` 和 `library/nginx` 在 cache 中成为两个独立 repository，同时共享同一份 CAS 内容。

### 2.3 Repository 类型

```text
local  -> 本地推送、可删除、不参与缓存 LRU 淘汰
cache  -> 从上游拉取后缓存、只读、可被 LRU 淘汰
```

同一个 digest 可以同时被 local 和 cache 仓库引用；物理 CAS 文件只有一个。

代理 cache 不出现在 `/v2/_catalog` 中。代理路由的 `GET /v2/_catalog` 固定返回 `404 Not Found`，不向上游转发；缓存仓库通过管理 API 查看。

## 3. 接口覆盖矩阵

| 接口 | 本地 Registry | 代理上游 | 说明 |
| --- | --- | --- | --- |
| `GET/HEAD /v2/` | 是 | 是 | API 版本检查 |
| `GET/HEAD /v2/<name>/manifests/<ref>` | 是 | 是 | 本地读取或回源缓存 |
| `GET/HEAD /v2/<name>/blobs/<digest>` | 是 | 是 | 本地读取或回源缓存 |
| `GET /v2/<name>/tags/list` | 是 | 直接透传 | 分页和 Link |
| `GET /v2/<name>/referrers/<digest>` | 是 | 透传 | OCI 1.1 |
| `GET /v2/_catalog` | 是 | 否（404） | 本地仓库目录 |
| `POST /v2/<name>/blobs/uploads/` | 是 | 否 | 开始上传 |
| `PATCH /v2/<name>/blobs/uploads/<id>` | 是 | 否 | 分块上传 |
| `PUT /v2/<name>/blobs/uploads/<id>` | 是 | 否 | 完成上传 |
| `GET /v2/<name>/blobs/uploads/<id>` | 是 | 否 | 查询上传状态 |
| `DELETE /v2/<name>/blobs/uploads/<id>` | 是 | 否 | 取消上传 |
| `PUT /v2/<name>/manifests/<ref>` | 是 | 否 | 推送 manifest |
| `DELETE /v2/<name>/manifests/<ref>` | 是 | 否 | tag 删除或 digest 删除 manifest |
| `DELETE /v2/<name>/blobs/<digest>` | 是 | 否 | 删除 blob 引用 |
| Blob mount | 是 | 否 | 跨仓库挂载 |
| Docker token flow | 是 | 是 | `/v2/token` |

代理 Host 的写请求：

- 返回 `405 Method Not Allowed`。
- 不创建本地 repository，不缓存上传内容。
- 不把写请求转发给上游。
- 这个 `405` 在认证 scope 授权之前判定，避免把“代理不可写”误报成 401/403。

代理路由的只读限制：

- `GET /v2/_catalog` 返回 `404`，不转发上游，也不列 cache repository。
- `POST`、`PATCH`、`PUT`、`DELETE` 均返回 `405`；包括 blob upload、manifest push 和 delete。
- `GET /v2/<name>/tags/list` 与 `GET /v2/<name>/referrers/<digest>` 每次只读透传上游，不使用 tag/manifest 缓存复用响应。

## 4. 通用响应和错误

### 4.1 响应头

```text
Docker-Distribution-Api-Version: registry/2.0
Docker-Content-Digest: <digest>
Location: /v2/<name>/blobs/<digest>
Content-Type: <manifest media type>
Content-Length: <size>
Range: 0-<offset>
Docker-Upload-UUID: <upload-id>
OCI-Subject: <subject-digest>
OCI-Filters-Applied: artifactType
Link: </v2/_catalog?last=...&n=...>; rel="next"
```

### 4.2 错误格式

```json
{
  "errors": [
    {
      "code": "BLOB_UNKNOWN",
      "message": "blob unknown to registry",
      "detail": { "digest": "sha256:..." }
    }
  ]
}
```

常用错误码：

```text
BLOB_UNKNOWN
BLOB_UPLOAD_INVALID
BLOB_UPLOAD_UNKNOWN
DIGEST_INVALID
MANIFEST_BLOB_UNKNOWN
MANIFEST_INVALID
MANIFEST_UNKNOWN
NAME_INVALID
NAME_UNKNOWN
SIZE_INVALID
TAG_INVALID
UNAUTHORIZED
DENIED
UNSUPPORTED
TOOMANYREQUESTS
```

状态码：

- `400`：请求格式错误、非法 digest、非法 Content-Range。
- `401`：未认证或凭据无效。
- `403`：已认证但无 pull/push/delete 权限。
- `404`：repository、manifest、blob、upload 不存在。
- `405`：代理路由上的写操作，或不支持的方法。
- `409`：上传状态冲突、 blob 仍被 manifest 引用。
- `413`：manifest 或请求体超过配置上限。
- `416`：上传 chunk 起点不连续或范围不满足。
- `429`：限流。
- `500`：内部错误。
- `507`：磁盘空间不足且配置为 reject。

## 5. 拉取流程

### 5.1 `/v2/`

```http
GET /v2/
200 OK
Docker-Distribution-Api-Version: registry/2.0
```

需要认证时返回 `401` 和 challenge。

### 5.2 Manifest pull

```http
GET /v2/<name>/manifests/<tag-or-digest>
Accept: application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, ...
```

本地 Registry：

1. 校验 pull 权限。
2. digest 引用直接查 repository 下的 manifest 引用。
3. tag 引用查询本地 tag。
4. 返回原始 manifest 字节，不重写。
5. 设置 `Content-Type`、`Docker-Content-Digest`、`Content-Length`。

代理 Registry：

1. 校验 pull 权限。
2. digest 命中缓存直接返回。
3. tag 按 TTL 校验上游，必要时条件请求或重新获取。
4. 写入 CAS 并记录 cache ref。

### 5.3 Blob pull

```http
GET /v2/<name>/blobs/<digest>
Range: bytes=0-1023
```

- 本地和缓存都从 CAS 读取。
- 支持 `Range`，返回 `206 Partial Content`。
- 代理缓存未命中且带 Range 时透传上游，不写入不完整缓存。
- `HEAD` 返回元数据，不读取 body。

### 5.4 tags list

```http
GET /v2/<name>/tags/list?n=100&last=v1.2.3
```

- 返回 `{"name":"library/nginx","tags":[...]}`。
- 按字典序排序。
- 支持 `n` 和 `last`。
- 超过一页返回 `Link` 头。
- 本地使用本地 tags。
- 代理每次请求直接透传上游；分页 `Link` 改写到客户端可见 repository，不复用 manifest tag 的 TTL 缓存。

### 5.5 catalog

```http
GET /v2/_catalog?n=100&last=library/nginx
```

- 只列本地 repository，不列代理缓存。
- 支持 `n` 和 `last`。
- 返回 `{"repositories":[...]}` 和 `Link`。
- 需要 `admin` 或 `manage` 权限；普通 pull 用户不返回 catalog。
- 代理 Host 和代理路径返回 `404`，不向上游请求 catalog。
- 管理台使用 `/api/v1/repositories?kind=cache` 查看缓存仓库，不复用 Registry catalog。

### 5.6 referrers

```http
GET /v2/<name>/referrers/<digest>?artifactType=application/vnd.example...&n=100&last=<digest>
```

- 本地根据 `subject` 关系生成 OCI image index。
- `Content-Type: application/vnd.oci.image.index.v1+json`。
- 支持 `artifactType` 过滤。
- 应用过滤时返回 `OCI-Filters-Applied: artifactType`。
- 支持 `n`/`last` 分页；超过一页时返回 `Link`。
- descriptor 包含对应 manifest 的 `artifactType` 和 annotations。
- image manifest 缺少 `artifactType` 时，descriptor 的 `artifactType` 取 config descriptor 的 `mediaType`；image index 缺少时省略。
- 本地没有 referrer，或 repository 尚不存在时，都返回空 image index，而不是 `404`；仍需通过 pull 鉴权。
- 代理路由原样透传上游；上游返回 `404` 时保持 `404`，不能伪造空 index，否则客户端会误判上游支持 referrers。
- 代理层不解析、不重建 referrers 索引；只有本地 manifest push 时写入本地 `referrers` 关系。

## 6. Blob 上传状态机

```text
POST /blobs/uploads/
       │
       ├── ?digest=...  -> 单请求上传 -> 201
       ├── ?mount=...   -> 源仓库存在 -> 201
       │                   否则 -> 202 + upload session
       └── 无 digest     -> 202 + upload session
                              │
                              ├── PATCH -> 202，追加 chunk
                              ├── GET   -> 204，返回 Range
                              ├── DELETE-> 204，取消
                              └── PUT ?digest=... -> 201，完成
```

### 6.1 开始上传

```http
POST /v2/<name>/blobs/uploads/
```

- 校验 push 权限。
- 生成高熵、URL-safe 的 upload ID；ID 只允许 `[A-Za-z0-9_-]`，临时文件路径固定由 `uploads/<id>.part` 派生，绝不读取或拼接用户提供的路径。
- 接受可选的 `digest-algorithm` 参数；如果提供，必须和最终 `digest` 的算法一致。
- 创建临时文件和 SQLite upload 记录。
- 返回：

```http
202 Accepted
Location: /v2/<name>/blobs/uploads/<id>
Range: 0-0
Docker-Upload-UUID: <id>
```

如果同时携带 `digest` 和 `mount`，视为非法请求并返回 `400`；两者只能选择一个。

### 6.2 分块上传

```http
PATCH /v2/<name>/blobs/uploads/<id>
Content-Type: application/octet-stream
Content-Range: 0-1048575
```

- 校验 upload 属于当前用户和 repository。
- upload 状态查询接受 `pull` 或 `push` scope；取消上传接受 `delete` 或 `push` scope。这样标准 token-flow 客户端按 HTTP 方法申请 scope 时可以复用会话。
- 校验 chunk 起点等于当前临时文件大小。
- 校验 `Content-Range` 的起止值和 `Content-Length` 一致；不接受带 `bytes=` 前缀的 HTTP Range 写法作为上传偏移。
- chunk 必须连续；起点跳跃或重复返回 `416 Requested Range Not Satisfiable`，不要追加写入。
- 追加写入临时文件。
- 超过 `max_blob_bytes` 或磁盘配额则拒绝。
- 返回 `202 Accepted`、新的 `Location` 和 `Range: 0-<last-byte>`。

不强制固定 chunk 对齐；不发送 `OCI-Chunk-Min-Length`。

### 6.3 完成上传

```http
PUT /v2/<name>/blobs/uploads/<id>?digest=sha256:...
Content-Type: application/octet-stream
```

- `PUT` 可以携带最后一段 body。
- 校验最终长度和 digest。
- 将临时文件原子 rename 到 CAS。
- 建立 repository blob 引用。
- 删除 upload 记录。
- 返回 `201 Created`、`Location: /v2/<name>/blobs/<digest>`、`Docker-Content-Digest`。

digest 不匹配：

- 保留 upload 临时文件和会话，允许客户端修正后重新 `PUT`。
- 返回 `400 DIGEST_INVALID`，并继续返回当前 `Range`。

### 6.4 单请求上传

```http
POST /v2/<name>/blobs/uploads/?digest=sha256:...
Content-Type: application/octet-stream
```

- 直接校验 body digest 并提交。
- 成功返回 `201 Created`、`Location` 和 `Docker-Content-Digest`。
- 适用于小 blob 和简单客户端。

### 6.5 查询和取消

```http
GET /v2/<name>/blobs/uploads/<id>
204 No Content
Location: /v2/<name>/blobs/uploads/<id>
Range: 0-<last-byte>
Docker-Upload-UUID: <id>
```

```http
DELETE /v2/<name>/blobs/uploads/<id>
204 No Content
```

- 查询和取消都要求 upload 属于当前用户和 repository。
- 服务重启后 upload 仍可继续，因为元数据和临时文件持久化；超过 TTL 后清理。
- `GET` 返回的 `Range` 表示已接收的最后一个字节；空 upload 使用 `Range: 0-0` 兼容 Docker 客户端。

### 6.6 跨仓库 mount

```http
POST /v2/<name>/blobs/uploads/?mount=<digest>&from=<source-repo>
```

规则：

- 当前用户必须对 source repository 有 pull 权限。
- 当前用户必须对目标 repository 有 push 权限。
- source 和 target 都必须是本地 repository；不从代理 cache 或上游直接 mount。
- source 中存在完整 blob 时，建立目标引用，返回 `201 Created`、`Location` 和 `Docker-Content-Digest`。
- 不存在或无权访问时，返回 `202 Accepted` 并开始普通 upload session。
- 不复制物理文件，只增加引用。

## 7. Manifest 推送

```http
PUT /v2/<name>/manifests/<tag-or-digest>
Content-Type: application/vnd.oci.image.manifest.v1+json
```

流程：

1. 校验 push 权限。
2. 限制 manifest 大小。
3. 校验 `Content-Type` 和 JSON 结构。
4. 计算 digest。
5. 如果 reference 是 digest，必须与计算值一致。
6. 校验媒体类型：
   - OCI image manifest
   - OCI image index
   - Docker schema2 manifest
   - Docker manifest list
7. 校验 config/layers 引用：
   - 普通 blob 必须在目标 repository 中存在。
   - 具有 `urls` 的 foreign layer 可以不存在。
   - image index 的子 manifest 必须在目标 repository 中存在；conformance 模式和默认模式都严格校验。
   - 如果 manifest 含 `subject`，subject 引用不强制要求已存在，但必须校验 digest 格式；此时写入 referrers 关系。
8. 写入 manifest CAS。
9. 建立 repository manifest 引用。
10. 如果 reference 是 tag，更新 tag 指向 digest。
11. 如果 manifest 有 `subject`，写入 referrers 关系。
12. 返回 `201 Created`。

响应：

```http
201 Created
Location: /v2/<name>/manifests/<digest>
Docker-Content-Digest: <digest>
OCI-Subject: <subject-digest>       # 仅 manifest 含 subject 时返回
```

tag 覆盖：

- 更新 tag 指向新 digest。
- 旧 manifest 仍保留并可按 digest 访问；自动清理 untagged manifest 不是当前能力，只能通过显式 delete 处理。
- 记录审计事件 `registry.manifest.push`。

## 8. 删除

### 8.1 Manifest/tag delete by reference

```http
DELETE /v2/<name>/manifests/<tag-or-digest>
```

- 校验 delete 权限。
- 根据 reference 类型分支处理：
  - reference 是 **tag**：只删除该 tag 指向关系；manifest 仍可按 digest 访问，`GET /v2/<name>/manifests/<tag>` 返回 404。
  - reference 是 **digest**：删除该 repository 的 manifest 引用，并删除所有指向该 digest 的 tag（包括不同 Accept 变体）。
- 删除 manifest 时只删除 `manifest_digest` 等于该 digest 的 referrer 记录；如果该 manifest 只是别的 referrer 的 subject，不删除那些 referrer，因为 OCI 允许 subject 不存在。
- 删除 manifest 时不全局删除 `content_edges`，因为同一 manifest digest 可能仍被另一个 repository 引用；blob 删除校验只统计仍有 manifest 引用的边。孤立 edge 由 GC/fsck 清理。
- 不删除该 manifest 引用的 config/layer blob；这些 blob 的回收由显式 delete 或 GC 决定。
- 返回 `202 Accepted`。
- CAS 内容是否删除由 GC 决定。

### 8.2 Blob delete

```http
DELETE /v2/<name>/blobs/<digest>
```

- 校验 delete 权限。
- 如果 blob 仍被同 repository 的任意**仍有 manifest 引用**通过 `content_edges` 引用，返回 `409 Conflict`，避免破坏镜像。这是 crproxy 的引用保护策略；conformance 流程先删除 manifest 再删除 blob，不会触发该冲突。
- 删除 repository blob 引用。
- 返回 `202 Accepted`。
- 物理内容由 GC 在无任何 local/cache 引用后删除。

### 8.3 Tag delete 与管理 API

Registry V2 没有 `DELETE /v2/<name>/tags/<tag>` 这样的独立路径；tag delete 的协议入口是 `DELETE /v2/<name>/manifests/<tag>`。

- 协议入口只删除 tag，不删除 manifest。
- 管理 API `DELETE /api/v1/repositories/{id}/tags/{tag}` 等价于只删除 tag 指向关系。
- tag 删除要覆盖该 tag 的所有 `accept_hash` 行，不能只删除当前请求 Accept 对应的一行。
- manifest 仍可按 digest 访问；如果希望彻底回收，需要再调用 manifest digest delete 或等待 GC 策略处理。

### 8.4 Repository delete 与管理 API

管理 API `DELETE /api/v1/repositories/{id}` 只对本地 repository 开放：

- 删除该 repository 的 tags、content_refs、referrers 和 uploads。
- 物理 CAS 内容由 GC 回收；如果同一 digest 仍被其他 local/cache repository 引用，不删除。
- 代理 cache repository 只能通过 `POST /api/v1/cache/evictions`（可带 `repository_id` 或 digest 列表）清除，不提供 Registry 写接口。

### 8.5 代理路由

代理 Host 和 `/r/<slug>/` 路径上的 DELETE 一律返回 `405`，不删除上游镜像，也不删除本地缓存；代理缓存清理走管理 API。

## 9. 鉴权与 scope

本地 Registry：

- pull 需要 `pull`。
- push 需要 `push`。
- delete 需要 `delete`。
- catalog 需要 `manage` 或 `admin`。

代理 Registry：

- 只允许 pull、tags/list、referrers。
- 写方法返回 `405`，不进入 auth challenge 的 scope 授权。

Docker token flow：

```text
scope=repository:library/nginx:pull
scope=repository:library/nginx:pull,push
scope=repository:library/nginx:push,delete
```

JWT `access.actions` 必须包含当前操作需要的动作。详细规则见 [`auth-design.md`](auth-design.md)。

`/v2/_catalog` 不使用 repository scope；通过 Distribution 的 Basic/PAT 凭据并按 `manage` 或 `admin` 授权。管理台 Session 不属于 Distribution 凭据。Docker token flow 不签发 catalog 权限，避免把仓库枚举能力下放给普通 pull token。

## 10. 并发、幂等和事务

- 同一 upload ID 的 PATCH/PUT 串行化，避免并发追加。
- 同一 blob digest 的最终提交使用 singleflight；已存在时直接建立引用。
- manifest PUT 以 digest 为幂等键。
- tag 更新使用 SQLite 事务。
- 内容文件先提交 CAS，再写数据库引用；崩溃时由 fsck/GC 处理孤儿。
- manifest 删除和 blob 引用删除使用短事务。
- 删除操作不阻塞正在读取的文件；Unix 下打开的文件描述符可继续读取。
- `Docker-Content-Digest` 必须与实际存储 digest 一致。

## 11. 校验规则

Repository 名称：

- 符合 Distribution spec 的路径组件规则。
- 禁止 `..`、空组件、控制字符和 URL 解码后穿越。
- 路径模式下第一段 `r` 保留给代理。

Digest：

- 支持 `sha256` 和 `sha512`。
- 格式严格校验，算法和长度匹配。

Manifest：

- `schemaVersion: 2`。
- 必需字段完整。
- config/layer descriptor 的 digest 和 size 合法。
- config/layer 引用必须是目标 repository 中已存在的 blob；foreign layer 的 `urls` 例外。
- image index 的子 manifest 必须存在于目标 repository。
- 不重写用户 manifest 字节。

Upload：

- 不允许 chunk 跳跃或重复起点。
- 总大小不超过 `max_blob_bytes`。
- Content-Range 必须合法。
- 完成后必须校验 digest。
- upload 临时路径只能由 upload ID 派生，且 open 时使用 `O_NOFOLLOW`；不接受请求中的路径参数。

## 12. 代理与本地共存规则

- 本地 repository 和代理 repository 不共享 namespace。
- Host 模式天然隔离；路径模式用 `r/` 保留前缀隔离。
- 代理缓存是只读的，不能通过 Registry API 删除。
- 本地内容不参与代理缓存 LRU 淘汰。
- 同一 digest 被本地引用时，物理内容受保护。
- 本地 manifest 与缓存 manifest 共享 CAS，但分别记录引用关系。
- 代理 tag 可以 TTL 过期；本地 tag 不自动过期，只有覆盖或删除。
- cache repository 的 `namespace` 必须等于 upstream ID；local repository 的 `namespace` 固定为 `local`。
- 代理 API 不暴露 cache repository 的写能力；所有缓存清理都走管理 API 并记录审计。

## 13. 协议兼容性测试

以下是发布门槛：

- OCI Distribution Spec conformance：
  - Pull tests。
  - Push tests。
  - Content discovery tests，包括 tags、catalog、referrers。
- Podman（唯一客户端冒烟工具）：
  - `podman login`
  - `podman pull`
  - `podman tag`
  - `podman push`
  - 删除 manifest 后重新 pull 的行为。
- 分块上传、单请求上传、mount、恢复上传。
- Range blob 和 HEAD。
- 多架构 image index。
- OCI artifact 和 referrers。
- 权限失败：无 push、无 delete、代理写操作。
- 并发 push 同一 blob 和同一 tag。
- 断电/重启后的 upload 恢复和 CAS 清理。

验收时至少要证明三点：

1. 本地 Registry 能通过 Podman push/pull，并通过 OCI conformance 的 pull、push、content discovery。
2. 代理路由对同一组客户端表现为只读 Registry；写请求返回 `405`，不产生 cache mutation。
3. 本地内容在缓存 LRU、缓存清理和上游失效场景下仍可读取；缺文件必须报损坏而不是回源。

## 14. 实施顺序

1. 本地 Registry 基础读取：`/v2/`、manifest/blob pull、HEAD、Range。
2. Blob upload 状态机、manifest PUT、tags、上传恢复。
3. 本地 catalog、delete、referrers、mount、管理 API 删除语义。
4. 协议 conformance 全量测试，作为 M1/M2 的门槛。
5. 多上游代理和缓存复用 CAS。
6. 鉴权 scope、管理台、审计和磁盘策略。
7. 完整端到端测试。

## 15. 规范覆盖边界

### 15.1 必做基线

- OCI Distribution Spec 1.1 的 pull、push、content discovery。
- manifest、blob、HEAD、Range、tags/list、catalog、referrers。
- chunked upload、monolithic upload、upload status、mount、delete。
- `OCI-Subject`、`OCI-Filters-Applied`、`Link`、`Docker-Content-Digest`。
- Docker schema2 和 OCI image manifest/index；不支持 schema1。

### 15.2 明确非目标

以下能力属于规范允许的可选项，当前交付明确不提供：

- `PUT /v2/<name>/manifests/<digest>?tag=<tag>` 一次推送多 tag，以及 `OCI-Tag` 响应头。
- 代理的 `ns` 查询参数和多段 source host 解析；crproxy 用 Host/路径路由替代。
- 从 proxy cache repository mount blob 到 local repository；mount 只允许 local source，避免缓存内容被隐式提升为本地内容。
- `Warning` 响应头。
- 服务端主动通知、跨实例复制、镜像扫描和策略引擎。

### 15.3 crproxy 扩展

- blob 被同 repository 的有效 manifest 引用时，`DELETE blob` 返回 `409`，而不是直接返回 `202`；这是引用保护策略。删除 manifest 后，conformance 的 blob delete 流程仍按 `202` 通过。
- 代理写请求返回 `405`，代理 catalog 返回 `404`。
- cache 内容只能通过管理 API 淘汰，Registry API 没有缓存删除入口。
