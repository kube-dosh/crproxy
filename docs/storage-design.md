# crproxy 磁盘与缓存存储设计

本文详细说明 crproxy 的本地磁盘布局、内容寻址、元数据模型、容量控制、GC、故障恢复和运维方式。存储基线同时服务两类内容：

- 本地 Registry 推送的 manifest/blob：受本地引用保护，不能因为缓存水位被删除。
- 代理 pull-through cache 内容：只读、可 LRU 淘汰，缺失时按 cache miss 回源。

两者共享 CAS 和 SQLite，但通过 repository kind 与 namespace 隔离生命周期。

## 1. 设计目标

- 单个本地数据目录即可运行，不依赖外部数据库或对象存储。
- manifest 和 blob 使用内容寻址存储（CAS），跨仓库、跨上游去重。
- 写入必须原子，进程崩溃后不能留下被当作完整内容读取的半文件。
- 缓存可随时淘汰，不能因为缓存丢失影响上游镜像本身的可用性。
- 支持容量上限、文件系统剩余空间保护、LRU 淘汰和手动清理。
- 支持 Web 管理台展示磁盘使用、缓存明细、GC 状态和下载任务。
- 固定为单实例本地块存储；多实例共享磁盘明确不支持。

## 2. 文件系统总体布局

```text
/var/lib/crproxy/
  VERSION                 # 数据布局版本号
  LOCK                    # 单实例文件锁
  meta/
    crproxy.db            # SQLite 元数据
    crproxy.db-journal    # rollback journal，事务期间临时存在
  cas/
    sha256/
      0a/
        1b/
          1b2c3d...       # 完整 digest 十六进制，不带前缀
    sha512/
      .../
  uploads/
    <upload-id>.part      # blob upload session 临时文件
  tmp/
    blob-<job-id>.part
    manifest-<job-id>.part
  trash/
    <uuid>.deleted
```

约束：

- `uploads/`、`tmp/` 和 `cas/` 必须位于同一个文件系统，保证 `rename(2)` 原子提交。
- `meta/` 也建议放在同一个本地卷；至少不能放在文件锁和 `fsync` 语义不可靠的网络文件系统上。
- `trash/` 与 `cas/` 同文件系统，避免异步删除跨挂载点。
- 日志默认输出到 stdout，不写数据盘。
- TLS 证书、master key 等敏感配置放在 `/etc/crproxy` 或 Secret 挂载，不放在数据目录。

## 3. CAS 内容布局

### 3.1 路径规则

```text
cas/<algorithm>/<hex[0:2]>/<hex[2:4]>/<hex>
```

例如：

```text
cas/sha256/9f/86/9f86d081884c7d659a2feaa0c55ad015...e5b9
```

使用两级 256 桶目录，最多 65536 个目录，避免单目录文件数过大。每个内容一个文件，文件名为完整 digest hex。

### 3.2 存储对象

同一个 CAS 同时保存：

- image config blob
- layer blob
- OCI manifest
- OCI image index
- Docker schema2 manifest
- Docker manifest list

manifest 和 blob 都按内容 digest 存储。manifest 与普通 blob 的区别只记录在元数据的 `kind` 和 `media_type` 字段中。

### 3.3 为什么不按仓库目录存储

镜像层通常被多个仓库、多个 tag、多个上游共享。按仓库存储会重复占用磁盘，并且难以做全局 LRU。CAS 以 digest 为主键，天然去重：

```text
repo A ─┐
repo B ─┼──> sha256:abc...  (only one physical file)
repo C ─┘
```

仓库和上游关系通过 `repositories` / `content_refs` 元数据维护，用于管理台展示和本地内容保护，不影响物理去重。

## 4. 元数据存储：嵌入式 SQLite

### 4.1 为什么选 SQLite

管理台需要：

- 按上游、仓库、kind、访问时间筛选缓存条目；
- 分页和排序；
- 聚合磁盘用量；
- 维护用户、token、审计和任务。

bbolt 可以实现，但需要大量手写索引和扫描逻辑。嵌入式 SQLite 更适合这类管理型查询，同时仍然是单文件、无外部服务。建议使用纯 Go 驱动 `modernc.org/sqlite`，保持无 CGO、单二进制部署。

### 4.2 SQLite 参数

```sql
PRAGMA journal_mode = DELETE;   -- 默认 rollback journal，不启用 WAL
PRAGMA synchronous = FULL;
PRAGMA busy_timeout = 5000;
PRAGMA foreign_keys = ON;
PRAGMA temp_store = MEMORY;
```

固定不使用 WAL。这个服务的元数据写入量很小，写操作主要是用户、token、上游配置、tag 状态和批量访问时间刷新；SQLite 默认的 rollback journal 已经能保证事务原子性和崩溃恢复，同时少维护 `-wal`/`-shm` 文件和 checkpoint 逻辑。

`synchronous=FULL` 配合短事务优先保证元数据可靠性。写入性能不是本项目瓶颈，因此 WAL 不属于交付选项；部署时不得通过修改 journal mode 绕过无 WAL 的验收要求。

迁移以 `schema_meta.version` 为准，启动时按版本顺序在事务中执行。全新 v4 安装直接创建下文完整 schema；从早期草案升级时才执行增量迁移。鉴权、存储和 Registry 模块不各自维护 DDL，避免字段定义漂移。

### 4.3 核心表

```sql
CREATE TABLE schema_meta (
  version INTEGER NOT NULL
);

CREATE TABLE upstreams (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  endpoint TEXT NOT NULL,
  route_host TEXT UNIQUE,
  route_prefix TEXT UNIQUE,
  repository_prefix TEXT NOT NULL DEFAULT '',
  auth_type TEXT NOT NULL,              -- anonymous | basic | bearer
  credentials_ciphertext BLOB,          -- AES-GCM 加密后的凭证
  credentials_version INTEGER NOT NULL DEFAULT 1,
  credentials_key_version INTEGER NOT NULL DEFAULT 1,
  ca_pem TEXT,
  insecure_skip_verify INTEGER NOT NULL DEFAULT 0,
  tag_ttl_seconds INTEGER NOT NULL DEFAULT 60,
  request_timeout_seconds INTEGER NOT NULL DEFAULT 30,
  max_blob_bytes INTEGER,
  enabled INTEGER NOT NULL DEFAULT 1,
  health_state TEXT NOT NULL DEFAULT 'unknown',
  health_checked_at INTEGER,
  health_message TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE repositories (
  id INTEGER PRIMARY KEY,
  namespace TEXT NOT NULL CHECK (length(namespace) > 0),
  name TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('local', 'cache')),
  upstream_id TEXT REFERENCES upstreams(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  last_seen_at INTEGER,
  UNIQUE(namespace, name),
  CHECK (
    (kind = 'local' AND namespace = 'local' AND upstream_id IS NULL)
    OR
    (kind = 'cache' AND upstream_id IS NOT NULL AND namespace = upstream_id)
  )
);

-- 不变量：local 的 namespace 固定为 'local'；cache 的 namespace 必须等于 upstream_id。
-- 这样同一 repository 名在不同上游之间不会冲突，也不会把代理仓库误认为本地仓库。
-- 删除 upstream 会级联删除其 cache repository 元数据；本地 repository 不受影响。

CREATE TABLE contents (
  digest TEXT PRIMARY KEY,              -- 例如 sha256:abc...
  algorithm TEXT NOT NULL,
  size INTEGER NOT NULL,
  media_type TEXT,
  kind TEXT NOT NULL,                   -- blob | manifest | index | config
  state TEXT NOT NULL DEFAULT 'active', -- active | deleting | missing
  created_at INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL,
  access_count INTEGER NOT NULL DEFAULT 0,
  last_verified_at INTEGER
);

CREATE TABLE content_refs (
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  digest TEXT NOT NULL REFERENCES contents(digest) ON DELETE CASCADE,
  kind TEXT NOT NULL,                   -- blob | manifest
  first_seen_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  PRIMARY KEY(repo_id, digest, kind)
);

CREATE TABLE tags (
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  tag TEXT NOT NULL,
  accept_hash TEXT NOT NULL,
  digest TEXT NOT NULL REFERENCES contents(digest) ON DELETE CASCADE,
  media_type TEXT,
  size INTEGER NOT NULL,
  etag TEXT,
  checked_at INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL,
  PRIMARY KEY(repo_id, tag, accept_hash)
);

-- 本地 tag 不自动过期；cache tag 使用 checked_at + upstream.tag_ttl 判断新鲜度。

CREATE TABLE content_edges (
  parent_digest TEXT NOT NULL,
  child_digest TEXT NOT NULL,
  child_kind TEXT NOT NULL,
  PRIMARY KEY(parent_digest, child_digest)
);

CREATE TABLE referrers (
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  subject_digest TEXT NOT NULL,
  manifest_digest TEXT NOT NULL REFERENCES contents(digest) ON DELETE CASCADE,
  artifact_type TEXT,
  created_at INTEGER NOT NULL,
  PRIMARY KEY(repo_id, subject_digest, manifest_digest)
);

CREATE TABLE uploads (
  id TEXT PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  size INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL,                  -- created | uploading | finalizing
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);

-- upload part 的路径固定为 uploads/<id>.part，由 upload ID 派生，不持久化用户输入路径。
```

建议索引：

```sql
CREATE INDEX idx_contents_lru ON contents(state, last_used_at);
CREATE INDEX idx_contents_size ON contents(size);
CREATE INDEX idx_refs_repo ON content_refs(repo_id, digest);
CREATE INDEX idx_tags_digest ON tags(digest);
CREATE INDEX idx_repositories_kind ON repositories(kind, last_seen_at);
CREATE INDEX idx_repositories_upstream ON repositories(upstream_id, last_seen_at);
CREATE INDEX idx_referrers_subject ON referrers(repo_id, subject_digest);
CREATE INDEX idx_uploads_expiry ON uploads(expires_at);
```

`content_edges` 来自对 manifest 的解析，用于：

- 管理台展示 manifest 引用的 config 和 layers；
- 分析本地 manifest 的 blob 完整性；
- 防止删除仍被本地 manifest 引用的 blob；
- 分析代理缓存的完整度。

本地仓库要求引用完整性；代理缓存允许部分淘汰，因此 `content_edges` 主要用于本地删除校验和展示。删除本地 blob 时必须检查同 repository 的 manifest 引用。

### 4.3.1 引用语义与删除校验

`content_refs` 表示“某个 repository 认识这个内容”，不是全局 GC root；`content_edges` 表示 manifest 对 config/layer/child manifest 的依赖。

- local manifest：push 成功后在 `content_refs(kind=manifest)` 建立引用；tag 只是 manifest 的别名，不额外保护内容。
- local blob：upload 完成或 cross-repo mount 成功后在 `content_refs(kind=blob)` 建立引用。
- cache content：回源成功后在对应 cache repository 建立 `kind=blob|manifest` 引用；local/cache 属性由 `repositories.kind` 决定，tag TTL 和 LRU 只影响 cache repository 的引用。
- 删除 local tag：删除该 tag 的所有 `accept_hash` 行；manifest ref 保留，manifest 仍可按 digest 访问。
- 删除 local manifest：删除 manifest ref、指向它的 tag，以及 `manifest_digest` 等于该 digest 的 referrer 记录；不级联删除 child blob，也不删除以它为 subject 的其他 referrer。
- tag 覆盖不会删除旧 manifest ref；v4 不做自动 untagged manifest 清理，回收需要显式 manifest delete。
- 删除 local blob：查询 `content_edges` 时 JOIN 同一 repo 仍有 `content_refs(kind=manifest)` 的 parent；只要存在这样的引用就返回 `409`，否则删除该 repository 的 blob ref。这样同一 manifest digest 被其他 repo 引用时不会误判。
- 删除 local repository：在同一短事务中删除该 repo 的 tags、content_refs、referrers 和 uploads，然后交给 GC。
- cache 内容删除只能通过缓存淘汰或缓存清理 API 完成，不能通过 Registry delete 接口删除。

同一个 digest 同时有 local 和 cache 引用时，物理文件受 local 引用保护。GC 只有在不存在任何 local ref，且 cache 引用允许淘汰时才可回收。

### 4.4 用户、Token、审计和任务

```sql
CREATE TABLE users (
  id TEXT PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  role TEXT NOT NULL,                   -- admin | operator | viewer
  enabled INTEGER NOT NULL DEFAULT 1,
  must_change_password INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  password_changed_at INTEGER,
  last_login_at INTEGER
);

CREATE TABLE api_tokens (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  prefix TEXT NOT NULL,
  secret_hash TEXT NOT NULL,
  scopes TEXT NOT NULL,                 -- pull, push, delete, manage, admin
  expires_at INTEGER,
  last_used_at INTEGER,
  last_used_ip TEXT,
  revoked_at INTEGER,
  created_at INTEGER NOT NULL
);

CREATE TABLE sessions (
  id_hash TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  csrf_hash TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  idle_expires_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  ip TEXT,
  user_agent TEXT
);

CREATE TABLE audit_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  at INTEGER NOT NULL,
  actor_type TEXT NOT NULL,
  actor_id TEXT,
  action TEXT NOT NULL,
  target_type TEXT,
  target_id TEXT,
  ip TEXT,
  result TEXT NOT NULL,
  message TEXT,
  request_id TEXT
);

CREATE TABLE jobs (
  id TEXT PRIMARY KEY,
  type TEXT NOT NULL,                   -- gc | fsck | upstream_test
  state TEXT NOT NULL,                  -- queued | running | succeeded | failed | canceled
  progress_json TEXT,
  started_at INTEGER,
  finished_at INTEGER,
  error TEXT
);

CREATE TABLE settings (
  key TEXT PRIMARY KEY,
  value_json TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);
```

审计保留期建议默认 90 天，可通过定时任务按 `at` 删除旧记录。

## 5. 文件生命周期

### 5.1 写入流程

```text
1. 计算 digest、校验格式
2. 检查 quota / 文件系统剩余空间 / 是否已有内容
3. 在 tmp/ 创建临时文件 O_CREATE | O_EXCL | O_WRONLY，权限 0600
4. 从上游读取 body，同时写临时文件并计算 digest
5. 校验 Content-Length 和 digest
6. fsync 临时文件
7. rename 到 cas/<algo>/<aa>/<bb>/<hex>
8. fsync 目标目录（可配置，建议开启）
9. SQLite 事务 upsert contents / content_refs / 更新统计
10. 返回成功
```

异常处理：

- digest 不匹配、长度不符：删除临时文件，返回上游内容错误。
- 磁盘写满：删除临时文件，进入磁盘保护状态。
- 文件 rename 成功但 SQLite 失败：文件成为孤儿，由 `fsck` 或 GC 清理。
- 进程崩溃：启动时清理超过 `tmp_ttl` 的 `.part` 文件。

### 5.2 Blob upload 生命周期

本地 Registry 的 blob 上传使用持久化 upload session：

```text
POST /blobs/uploads/
  -> 创建 uploads 记录
  -> 创建 uploads/<upload-id>.part
  -> 202 + Location + Range

PATCH /blobs/uploads/<id>
  -> 校验 upload 所属用户、repo 和当前 size
  -> 顺序追加临时文件
  -> 更新 uploads.size / updated_at
  -> 202 + Range

PUT /blobs/uploads/<id>?digest=...
  -> 可选写入最后一段 body
  -> 校验总大小和 digest
  -> fsync
  -> rename 到 CAS
  -> SQLite 事务：upsert contents + content_refs
  -> 删除 uploads 记录
  -> 201 + Location + Docker-Content-Digest
```

规则：

- upload ID 只允许 `[A-Za-z0-9_-]`；part 路径由 `root/uploads/<id>.part` 派生，不接受请求参数、数据库中的绝对路径或客户端提供的文件名。
- 打开 part 文件使用 `O_NOFOLLOW`，并在创建时使用 `O_CREATE | O_EXCL`，防止符号链接和覆盖攻击。
- upload ID 绑定用户和 repository，不能被其他用户或仓库复用。
- PATCH 起点必须等于当前临时文件大小。
- upload 超过 `uploads.ttl` 后由后台任务删除临时文件和数据库记录。
- 进程重启后 upload 可以继续，因为临时文件和元数据都持久化。
- 单请求上传直接走同样的 CAS 提交逻辑。
- 跨仓库 mount 不复制文件，只新增 `content_refs`。

### 5.3 读取流程

```text
1. 查询 repository 和 contents
2. 如果记录不存在：local -> 404；cache -> 缓存 miss
3. 打开 cas 文件 O_RDONLY | O_NOFOLLOW
4. stat 校验 size
5. 通过 http.ServeContent 响应，支持 Range
6. 异步累计 last_used_at / access_count
```

如果数据库有记录但文件不存在：

- cache 内容：标记为 `missing` 或删除记录，当前请求按 miss 处理并重新回源。
- local 内容：不能按 miss 处理，必须返回 `500` 并记录数据损坏告警，等待备份恢复或 fsck。

不要在每个读取请求上做完整 SHA 校验，校验发生在写入时。可选的后台 scrub 任务做抽样或全量校验。

### 5.4 删除流程

Registry delete 和管理 API 删除的是引用关系，不是直接删除 CAS 文件。物理删除只由 GC 在确认没有 local ref 后执行：

```text
1. 协议/管理 API 在短事务中删除 manifest/tag/blob/repository 引用
2. GC 选出没有任何 local ref 且允许淘汰的内容
3. 将 contents.state 设为 deleting
4. rename cas 文件到 trash/<uuid>.deleted
5. 删除剩余 cache refs、content_edges 和 contents 记录
6. unlink trash 文件
7. 更新容量统计
```

如果删除 blob 时同 repository 的 manifest 仍通过 `content_edges` 引用它，协议层返回 `409`，不能把引用删除交给 GC 异步处理。删除 manifest 时保留全局 edge，但移除该 repo 的 manifest ref；孤立 edge 由 GC/fsck 清理。

进程崩溃后启动时：

- 扫描 `trash/` 并删除残留；
- 扫描 `state=deleting` 的记录并继续完成删除；
- 清理 `tmp/` 和过期 `uploads/`。

## 6. 容量、配额和磁盘保护

### 6.1 四层限制

1. `max_bytes`：crproxy 整体逻辑容量上限。
2. `cache_max_bytes`：代理缓存软上限；本地内容不计入这个上限。
3. `min_free_bytes`：文件系统必须保留的最小剩余空间。
4. `high_watermark` / `low_watermark`：GC 触发和停止水位。

建议：

```yaml
storage:
  max_bytes: 100GiB
  cache_max_bytes: 60GiB
  min_free_bytes: 15GiB
  high_watermark: 0.85
  low_watermark: 0.70
```

### 6.2 磁盘状态机

```text
normal     -> 正常缓存和下载
high       -> 触发后台 GC，仍可下载
critical   -> min_free_bytes 被突破，暂停缓存写入
emergency  -> 空间不足，所有新的缓存写入被拒绝
```

### 6.3 缓存满时的行为

代理缓存超过 `cache_max_bytes` 或水位时，只淘汰 cache-only 内容，不淘汰本地内容。

配置 `on_full`：

- `bypass`（推荐默认）：继续从上游流式响应，但不再写缓存，保证拉取不中断。
- `reject`：返回 `507 Insufficient Storage`，严格保护磁盘。

`min_free_bytes` 是硬保护，即使 `on_full=bypass`，也不能继续写入 CAS。

本地 push 和 upload 在磁盘不足时不能 bypass，必须返回 `507 Insufficient Storage`，避免写入不完整内容。

### 6.4 空间预留

下载前根据上游 `Content-Length` 做字节预留：

- 有长度：预留完整长度。
- 无长度：预留 `max_blob_bytes` 或默认阈值，完成后按实际大小结算。
- 预留失败先触发一次同步 GC；仍失败则按 `on_full` 策略处理。

预留只保存在内存中，进程重启自动释放。

### 6.5 每上游软配额

物理 CAS 是全局去重的，按上游计算“物理独占大小”不准确。当前实现：

- 全局配额强制执行。
- 每上游只显示逻辑使用量（通过 `content_refs` 去重后统计），作为软配额。
- 不提供每上游硬配额；共享内容的物理归属不具备稳定语义，淘汰以全局缓存仓库 LRU 为单位。

本地内容规则：

- `repositories.kind=local` 的引用保护内容，不参与缓存 LRU。
- 只有本地 manifest/tag/blob 引用被显式删除后，内容才可能进入 GC 候选。
- 同一 digest 同时存在 local 和 cache 引用时，物理文件受本地引用保护。

## 7. GC 算法

GC 分两类：

- 本地内容：只回收已经没有 local 引用的内容，绝不因缓存水位自动删除。
- 缓存内容：使用近似 LRU，未命中时可重新回源。

```text
1. 获取 GC 单飞锁，避免并发 GC
2. 计算当前物理大小、逻辑大小、文件系统剩余空间
3. 如果低于高水位且 min_free 安全，结束
4. 查询缓存候选：
   SELECT digest FROM contents
   WHERE state = 'active'
     AND NOT EXISTS (
       SELECT 1
       FROM content_refs cr
       JOIN repositories r ON r.id = cr.repo_id
       WHERE cr.digest = contents.digest AND r.kind = 'local'
     )
   ORDER BY last_used_at ASC
   LIMIT ...
5. 排除活跃下载、活跃读取和受保护 manifest
6. 批量标记 deleting
7. rename 到 trash，删除元数据，unlink
8. 重复直到低于低水位或没有候选
9. 更新统计并记录审计
```

保护规则：

- 尚未提交到 CAS 的下载临时文件不参与缓存淘汰。
- 正在响应读取的已打开文件由 Unix 文件描述符语义保护，淘汰后当前读取仍可完成。
- 本地引用始终保护同一物理内容，缓存 LRU 不会删除它。
- GC 与 fsck 都串行化物理清理；不根据 tag TTL 猜测 manifest 生命周期。

`last_used_at` 以内存批量更新为主，例如每 30 秒 flush 一次。这样避免每个请求都写 SQLite。

## 8. 文件系统选择与容量规划

### 8.1 推荐

- 本地 SSD/NVMe：最适合元数据和热点层。
- ext4：通用、稳定。
- XFS：大文件、高并发和较大缓存场景更合适。
- 挂载参数建议 `noatime`。

### 8.2 不支持

- NFS / SMB / CIFS：`rename`、`fsync` 和文件锁行为不可靠，容易出现锁冲突和缓存一致性问题。
- 网络块设备且无可靠语义的 FUSE 文件系统。
- 容器 overlay 的可写上层作为数据目录。

Kubernetes 部署应使用本地块存储 PVC 或 hostPath，并保证单实例挂载；不要多个 Pod 同时挂载可写数据目录。

### 8.3 容量估算

建议按“本地仓库 + 保留窗口内唯一缓存层大小”估算：

```text
所需容量 ≈ 本地镜像唯一层总大小
         + 最近 N 天实际拉取的唯一缓存层总大小 × 1.2 预留
         + SQLite / 审计 / 临时文件开销
```

影响因素：

- 镜像层压缩后大小；
- 多架构镜像重复度；
- tag 更新频率；
- 基础镜像共享程度；
- 回源带宽和可接受的首拉延迟。

不建议按“仓库数量 × 平均镜像大小”估算，因为层去重可能带来很大差异。

### 8.4 Inode 和目录

- 每个内容约一个 inode，另加临时文件、目录和 SQLite 文件。
- 文件系统 inode 数至少为预计内容数的 2 倍。
- 两级 256 桶目录避免单目录过大。
- 不建议做内容文件硬链接副本，CAS 已去重。

## 9. 权限与安全

- 服务以非 root 用户运行，例如 UID 65532。
- 数据根目录权限 `0750`。
- CAS 目录 `0750`，内容文件 `0640`。
- `meta/crproxy.db` 和 master key `0600` 或更严格。
- 打开 CAS 文件使用 `O_NOFOLLOW`，路径完全由校验后的 digest 构造。
- 禁止把用户输入的 repository 名直接拼进文件路径。
- 缓存内容不做应用层加密；如需静态加密，使用磁盘/LUKS 或云盘加密。
- 上游凭证使用 AES-GCM 加密后存 SQLite，master key 来自环境变量或权限受限的文件。

## 10. 备份、恢复和 fsck

### 10.1 备份

- 必须备份：`meta/crproxy.db`、配置、master key、TLS 证书。
- 本地镜像内容同样必须备份；CAS 中同时包含本地镜像和代理缓存，物理上无法简单区分。
- 最简单方案是定期对完整数据目录做卷快照；恢复后由 GC 清理缓存。
- 当前交付完整离线备份 `crproxy backup`，归档 `meta/`、`cas/`、`uploads/`；不做仅导出本地引用的子集备份。
- 不必备份：`tmp/`、`trash/` 和纯代理缓存内容，缓存可重新回源。
- SQLite 备份使用在线 backup API 或 `VACUUM INTO`，不要在事务写入期间直接复制数据库文件。

### 10.2 恢复

1. 恢复数据库、配置、JWT secret 和 master key。
2. 启动 crproxy。
3. 执行 quick fsck，核对 DB 和 CAS 文件。
4. 本地内容缺失时不能静默当作缓存 miss；必须报告数据损坏。
5. 无元数据记录的 CAS 文件由 fsck 识别并隔离到 trash，不提供从 CAS 反推仓库/tag 归属的 rebuild-index。

### 10.3 fsck 模式

- `quick`：检查记录对应文件是否存在、大小是否一致；清理 tmp/trash 和过期 uploads。
- `full`：重新计算所有内容 digest；慢，但可以发现磁盘损坏。

fsck 结果在管理台以 job 形式展示。

## 11. 监控指标与管理 API

### 11.1 指标

- `crproxy_storage_physical_bytes`
- `crproxy_storage_local_bytes`
- `crproxy_storage_cache_bytes`
- `crproxy_storage_content_total`
- `crproxy_storage_max_bytes`
- `crproxy_cache_max_bytes`
- `crproxy_disk_available_bytes`
- `crproxy_storage_temp_bytes`
- `crproxy_storage_state`
- `crproxy_active_downloads`
- `crproxy_gc_running`
- `crproxy_registry_events_24h{kind}`

### 11.2 管理 API

```text
GET  /api/v1/storage/summary
GET  /api/v1/storage/breakdown?group_by=upstream|repository
GET  /api/v1/storage/jobs
GET  /api/v1/storage/jobs/{id}
POST /api/v1/storage/gc
POST /api/v1/storage/fsck
GET  /api/v1/storage/settings
PUT  /api/v1/storage/settings
GET  /api/v1/cache/entries?kind=&upstream_id=&q=&limit=&offset=
POST /api/v1/cache/evictions
GET  /api/v1/uploads/active
```

写操作要求 `manage` 或 `admin` 权限，并记录审计日志。

## 12. 主要故障场景

| 场景 | 行为 |
| --- | --- |
| 下载中断 | 删除临时文件；已完成部分不进入 CAS |
| digest 不匹配 | 删除临时文件，返回上游内容错误 |
| 写入时磁盘满 | 终止缓存写入，按 `on_full` 策略响应 |
| rename 成功但 DB 写入失败 | 留下孤儿文件，由 fsck/GC 处理 |
| DB 有记录但文件缺失 | cache 视为 miss 回源；local 报告数据损坏 |
| 本地上传中断 | upload session 和 part 文件保留，重启后可继续；过期后清理 |
| upload 过程中服务重启 | 从 SQLite 和 part 文件恢复 Range |
| 本地内容被误删 | 不能按 cache miss 处理，fsck 报告数据损坏并从备份恢复 |
| GC 中途崩溃 | 启动时清理 deleting 状态和 trash |
| SQLite 损坏 | 从备份恢复；缓存目录可重建 |
| 数据目录被两个进程挂载 | `LOCK` 文件阻止启动，避免元数据损坏 |

## 13. 测试重点

- 并发下载同一个 digest 只写一个 CAS 文件。
- 客户端在下载中断开。
- Content-Length 未知和超大 blob。
- digest 校验失败。
- 写入成功但 SQLite 失败。
- 分块上传、单请求上传、跨仓库 mount、重启恢复和过期清理。
- 本地内容不被缓存 GC 淘汰。
- 删除仍被 manifest 引用的 blob 被拒绝。
- 删除 manifest 不误删仍被其他 repository 引用的 blob。
- local/cache namespace 唯一约束：同名 repository 在不同 upstream 不冲突。
- upload ID、part 路径和 `O_NOFOLLOW` 防止路径穿越/符号链接。
- GC 与活跃读取并发。
- 磁盘满、min_free 触发和 `on_full` 两种策略。
- SQLite 备份恢复和 fsck。
- 两级目录下大数量文件的扫描性能。
- 文件权限和符号链接攻击。
