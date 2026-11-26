# crproxy 前端设计方案

本文定义 crproxy 管理台的页面结构、视觉规范、组件选型、数据交互和测试策略。视觉与交互参考 `dosh/web` 和 `virtmgr/web`，但只复用设计模式，不直接搬业务代码。

## 1. 技术栈

```text
React 19 + TypeScript + Vite
Tailwind CSS 3.4 + shadcn/ui new-york
Radix UI primitives
lucide-react
@tanstack/react-query
sonner
自定义轻量 History Router（`src/lib/router.tsx`）
原生语义化表格 + CSS 柱状趋势图
```

建议与参考工程保持一致：

- `components.json`：`style=new-york`、`baseColor=slate`、CSS variables、lucide 图标。
- `tailwind.config.ts`：深色模式使用 `class`，字体使用 Inter / Noto Sans SC / JetBrains Mono。
- 前端构建产物通过 Go `embed` 打进单二进制。
- 管理 API 契约维护在 `docs/openapi.yaml`；当前前端类型在 `src/api/types.ts` 手工保持同步。

## 2. 视觉规范

### 2.1 设计风格

- 管理台风格，信息密度中等，不做营销页式大留白。
- 浅色背景 + 白色卡片 + 细边框；深色模式使用深蓝灰背景。
- 主色为蓝色，成功/警告/错误使用独立语义色。
- 卡片圆角 `0.5rem`，阴影使用轻量 `shadow-panel`。
- 数据页采用“页面标题 + 指标卡 + 表格/卡片列表 + 右侧/对话框操作”。
- 所有时间、大小、digest 使用等宽字体或 `tabular-nums`。

### 2.2 主题 token

直接采用参考工程的主色和语义色体系：

```css
:root {
  --background: 216 33% 97%;
  --foreground: 220 27% 14%;
  --card: 0 0% 100%;
  --primary: 214 90% 43%;
  --primary-foreground: 0 0% 100%;
  --muted: 215 24% 95%;
  --muted-foreground: 218 11% 42%;
  --border: 216 20% 89%;
  --success: 152 65% 25%;
  --warning: 35 92% 30%;
  --destructive: 0 72% 51%;
  --radius: 0.5rem;
}
```

深色模式沿用参考工程的深蓝灰色系，不在业务组件里硬编码颜色，必须使用 token。

### 2.3 布局

```text
┌──────────────────────────────────────────────────────────────┐
│ 顶栏：品牌 / 当前上下文 / 搜索 / 主题 / 用户 / 退出          │
├──────────────┬───────────────────────────────────────────────┤
│ 左侧导航     │ 内容区                                        │
│ 240px        │ PageHeader                                    │
│ 可折叠       │ Metric cards                                  │
│              │ 卡片 / 表格 / 图表                            │
└──────────────┴───────────────────────────────────────────────┘
```

- 顶栏高度 56px。
- 桌面端侧栏 240px，可折叠为 76px；移动端为抽屉。
- 内容区最大宽度 1440px，左右 padding 24px。
- 页面标题区使用 `PageHeader`，支持 breadcrumb、描述和右侧操作按钮。
- 空状态、加载状态、错误状态统一使用 `components/feedback/states.tsx` 模式。

## 3. 信息架构

主导航：

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

权限规则：

- `admin`：全部页面。
- `operator`：总览、本地镜像、代理缓存、上游、缓存条目、存储、审计、上传会话、只读系统设置；可本地 push/delete，可管理上游和存储阈值，不能管理用户或其他用户 Token。
- `viewer`：总览、本地镜像浏览、代理缓存浏览、上游只读；只读 pull，不显示运维页和系统设置页。
- 无权限页面直接隐藏菜单，并在直接访问时显示 403 状态。

“系统设置”只读展示运行时、端点和存储摘要，不提供配置写接口；可修改的存储阈值属于“存储与磁盘”的运维能力。

## 4. 页面设计

### 4.1 登录页

参考 `virtmgr` 登录页的双栏布局：

- 左侧品牌区：CRProxy 名称、说明“OCI Registry + 多上游 Pull-Through Cache”、TLS/本地仓库/缓存特性标签。
- 右侧登录卡：用户名、密码、登录按钮。
- 首次登录强制修改初始密码。
- 错误提示使用卡片内联错误，不使用弹窗。

### 4.2 总览页

顶部四张指标卡：本地仓库数、代理缓存仓库数、上游健康数、最近 24 小时缓存命中率。pull 请求数、活跃下载、字节数和磁盘水位在主卡片中展示。

主体布局：

- 左 2/3：24 小时请求趋势图，包含 hit / miss / upstream error。
- 右 1/3：磁盘水位和 GC 状态，展示 `normal/high/critical` 状态。
- 下方左侧：上游状态表，字段为名称、endpoint、路由、认证方式、健康状态。
- 下方右侧：最近活动，展示代理 manifest/blob 拉取、缓存命中和上游失败。
- 底部快捷卡片：按当前用户可用的 pull/push 命令示例，带复制按钮。

### 4.3 上游仓库页

使用响应式卡片网格，移动端单列。

字段：

- 名称
- ID
- 上游地址
- 路由域名 / 路径
- 认证方式：anonymous / challenge / basic / 静态 bearer
- 健康状态
- 最近检查时间
- 操作：测试、编辑、删除；启用/停用通过编辑对话框切换

顶部操作：

- 添加上游
- 刷新

新增/编辑对话框：

- 基本信息：名称、endpoint、路由 Host、启用状态。
- 认证：anonymous / challenge / basic / 静态 bearer；已保存凭证不回显，留空保留，可显式清除。
- TLS：自定义 CA PEM、是否跳过证书校验，跳过时显示警告。
- 拉取策略：repository 前缀（仅对单段名称生效）、tag TTL、请求超时、最大 blob。
- 底部固定操作：取消、创建/保存；连接测试在卡片操作中单独执行。
- 删除上游必须二次确认，提示会级联删除缓存仓库元数据且不影响本地仓库。

### 4.4 本地镜像页

用于管理通过 crproxy 本地 Registry 推送的镜像。

列表字段：

- repository
- tag 数量
- manifest 数量
- blob 数量
- 本地占用大小
- 最近访问时间
- 操作：查看详情、删除仓库

本地仓库不提供“新建”按钮，首次 push 时自动创建。

仓库详情页：

- 仓库基本信息。
- Pull 和 Push 命令。
- Tags 表：tag、digest、media type、大小、更新时间。
- Manifests 表：digest、media type、平台、大小、引用数量。
- Referrers 表：subject、artifactType、manifest digest。
- Blobs 表：digest、类型、大小、引用数、最近使用、状态。
- 删除 manifest、删除 tag、删除仓库。

删除操作必须明确说明会影响本地镜像，代理缓存删除是另一套流程。删除 tag 只移除别名，manifest 仍可按 digest 访问；删除 manifest 会移除所有指向它的 tag。删除 blob 若仍被 manifest 引用，后端返回 `409`，前端展示“仍被引用”的对象列表而不是吞掉错误。

### 4.5 代理缓存页

用于浏览“已经过代理访问过的仓库和镜像”，不是上游全量镜像列表。

列表字段：

- repository
- upstream
- tag 数量
- manifest / blob 数量
- 逻辑大小
- 最近访问时间
- 操作：查看详情、清理缓存

筛选：

- 上游
- 关键词

仓库详情页：

- 仓库基本信息。
- Pull 命令。
- Tags 表：tag、digest、media type、大小、最后检查、TTL 状态。
- Manifests / blobs 表：digest、kind、大小、引用数、最近访问、状态。
- 清理仓库缓存操作。

代理缓存页和详情页不显示 push、manifest push、Registry delete 入口；缓存清理统一走 `POST /api/v1/cache/evictions`，请求体可携带 `repository_id` 或 digest 列表。页面文案必须说明“只清理本地缓存，不删除上游镜像”。

### 4.6 缓存条目页

Blob / manifest 表格字段：

- Digest，等宽字体，支持复制。
- Kind（manifest / blob）。
- 大小。
- 引用数以及 local / cache 引用拆分。
- 最近访问时间。
- 访问次数。
- 状态：active / deleting / missing。
- 操作：选择后批量淘汰缓存。

操作：

- 批量淘汰所选 digest。
- 按 upstream、kind、digest/media type 筛选。

批量操作需要确认弹窗，明确说明只删除本地缓存，不删除上游镜像。

### 4.7 存储与磁盘页

顶部指标卡：

- 文件系统容量。
- 本地镜像用量。
- 代理缓存用量。
- 剩余空间。
- 临时文件占用。

主体：

- 容量进度条和磁盘状态徽标（normal / high / critical / emergency）。
- 按上游和仓库的缓存占用排行。
- 存储策略表单：max_bytes、cache_max_bytes、min_free_bytes、高/低水位、on_full。
- GC 任务列表：状态、开始时间、耗时、释放字节。
- quick/full fsck 入口。
- 活跃 upload 在独立“上传会话”页查看。

危险操作使用确认对话框：手动 GC、全量 fsck。

### 4.8 用户与 Token 页

管理员页面只管理本地用户；API Token 位于用户自助的“账户与 Token”页，避免管理员读取或代建其他用户凭证。

用户表：

- 用户 ID、用户名、角色、状态、密码状态、最近登录、创建时间。
- 新建用户、重置密码、禁用、删除。

账户与 Token 页：

- 名称、前缀、scopes、创建时间、过期时间、状态。
- scopes 包含 `pull`、`push`、`delete`、`manage`、`admin`。
- 新建时只展示一次完整 token，之后只显示前缀。
- 支持复制、撤销。

### 4.9 审计日志页

字段：

- 时间
- 操作者
- 来源 IP
- 动作
- 目标
- 结果
- request ID

筛选：action 精确匹配和结果；列表使用基于 `before` 游标的服务端分页，不一次性加载全量日志。challenge 类型、匿名开关和 Token TTL 由 YAML 配置，只读展示在系统设置页。

### 4.10 系统设置页

只读展示以下运行时信息，不提供配置写接口：

- 版本、启动时间、SQLite journal、OCI spec、是否允许匿名拉取。
- Registry、健康检查、Prometheus 和代理路径端点。
- 当前存储阈值、用量和磁盘状态。
- 安全基线：本地账号、Basic/PAT/token flow、Session/CSRF、上游凭证加密、代理只读。
- 配置和备份命令示例；可调存储阈值跳转“存储与磁盘”页。

### 4.11 关于页

- 版本、OCI spec、启动时间。
- 能力概览与固定非目标。
- 当前公开入口和管理台信息。

## 5. 组件规范

当前实现的 shadcn 风格基础组件：

```text
alert badge button card checkbox dialog input label progress select table textarea
```

业务复用组件：

- `AppShell`：侧栏、顶栏、响应式抽屉、主题切换。
- `PageHeader`：标题、描述、breadcrumb、操作区。
- `MetricCard`：带图标和语义色。
- `StatusBadge`：上游健康、磁盘状态、任务状态。
- 数据表：使用语义化 `<table>`、共享 toolbar/pagination/empty/error 状态，不引入额外表格运行时。
- `CopyButton`：digest、pull 命令。
- `ConfirmDialog`：危险操作确认。
- `Toolbar` / `Pagination`：列表筛选与服务端分页。
- `formatBytes` / `formatTime` / `shortDigest`：共享格式化函数。

所有按钮和表格操作必须有可读的 `aria-label`，键盘焦点样式使用 `ring` token。

## 6. 数据交互

### 6.1 状态管理

- 服务端状态使用 TanStack Query。
- 路由使用 `history.pushState` + `popstate` 的轻量 SPA Router；Go 静态服务回退 `index.html`，刷新不 404。
- 列表筛选与分页由页面状态维护；仓库详情使用 `/repositories/:id` 动态路由。
- 不使用 Redux；局部表单状态保持在页面或对话框内。
- mutation 成功后按 query key 精确失效，避免整页刷新。
- 总览与运维页面按 30–60 秒轮询；不引入 SSE/WebSocket 运行时。

建议 query key：

```text
["session"]
["overview"]
["registry-summary"]
["upstreams"]
["upstream", id]
["repositories", filters]
["repository", id]
["repository-tags", id]
["repository-manifests", id]
["repository-blobs", id]
["repository-referrers", id]
["cache-entries", filters]
["uploads-active"]
["uploads", filters]
["storage-summary"]
["storage-jobs"]
["users"]
["tokens"]
["audit", filters]
```

### 6.2 API 客户端

- 统一封装 `api` 对象，处理 JSON、错误、401、CSRF。
- 管理 API 使用 session cookie 和 `X-CSRF-Token`。
- 命令行和自动化可使用 Basic 或 Bearer PAT。
- 401 时清理 Query cache 并跳转登录页。
- 错误结构统一为：

```json
{
  "code": "upstream_auth_failed",
  "message": "上游鉴权失败",
  "details": {}
}
```

### 6.3 管理 API 范围

```text
POST   /api/v1/auth/login
POST   /api/v1/auth/logout
GET    /api/v1/auth/session
POST   /api/v1/auth/password

GET    /api/v1/overview
GET    /api/v1/registry/summary

GET    /api/v1/upstreams
POST   /api/v1/upstreams
GET    /api/v1/upstreams/{id}
PUT    /api/v1/upstreams/{id}
DELETE /api/v1/upstreams/{id}
POST   /api/v1/upstreams/{id}/test

GET    /api/v1/repositories?kind=local|cache
GET    /api/v1/repositories/{id}
GET    /api/v1/repositories/{id}/tags
GET    /api/v1/repositories/{id}/manifests
GET    /api/v1/repositories/{id}/blobs
GET    /api/v1/repositories/{id}/referrers
DELETE /api/v1/repositories/{id}
DELETE /api/v1/repositories/{id}/manifests/{digest}
DELETE /api/v1/repositories/{id}/tags/{tag}

GET    /api/v1/cache/entries
POST   /api/v1/cache/evictions
GET    /api/v1/cache/jobs/{id}

GET    /api/v1/storage/summary
GET    /api/v1/storage/breakdown
GET    /api/v1/storage/settings
PUT    /api/v1/storage/settings
POST   /api/v1/storage/gc
POST   /api/v1/storage/fsck
GET    /api/v1/storage/jobs
GET    /api/v1/storage/jobs/{id}

GET    /api/v1/uploads/active
GET    /api/v1/uploads
DELETE /api/v1/uploads/{id}

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

`DELETE /api/v1/repositories/{id}`、manifest delete 和 tag delete 只用于 local repository；cache repository 的清理由 `POST /api/v1/cache/evictions` 处理。所有管理写操作需要 `manage` 或 `admin` 权限并写入审计。

## 7. 目录结构

```text
web/
  components.json
  package.json
  tailwind.config.ts
  vite.config.ts
  src/
    main.tsx
    lib/router.tsx
    index.css
    api/
      client.ts
      types.ts
    components/
      layout/app-shell.tsx
      layout/page-header.tsx
      common/confirm-dialog.tsx
      common/copy-button.tsx
      common/pagination.tsx
      common/status-badge.tsx
      common/toolbar.tsx
      data/metric-card.tsx
      feedback/error-boundary.tsx
      feedback/forbidden-state.tsx
      feedback/states.tsx
      ui/                      # shadcn 组件
    features/
      about/
      account/
      audit/
      auth/
      cache/
      overview/
      repositories/
      upstreams/
      storage/
      settings/
      uploads/
      users/
    lib/
```

管理 API 契约位于 `docs/openapi.yaml`。`src/api/types.ts` 与 `docs/openapi.yaml` 均为手工维护，接口变更必须在同一提交中同步修改并由 TypeScript、Vitest 和 Go 集成测试校验。

## 8. 嵌入和构建

生产构建：

```bash
cd web && npm ci && npm run build
cd .. && go build -o bin/crproxy ./cmd/crproxy
```

Go 中使用 `embed.FS` 嵌入 `web/dist`：

- 静态资源直接返回。
- 非 `/api`、非 `/v2`、非静态文件的 GET 请求回退到 `index.html`。
- 开发模式由 Vite 代理 `/api` 到本地 Go 服务。
- 增加缓存头：带 hash 的静态资源长缓存，`index.html` 不缓存。

## 9. 测试

- `npm run typecheck`：全量 TypeScript 类型检查。
- `npm test`：Vitest 校验格式化、百分比和 digest 缩略等纯函数。
- `npm run build`：Vite 生产构建，确保 Go `embed` 所需产物完整。
- 协议、权限、鉴权、存储和代理边界由 Go 单元/集成测试及 OCI conformance 覆盖。
- 真实客户端验证只能使用 Podman，不使用 Docker、crane、oras、containerd 或 nerdctl。

## 10. 交互细节

- 时间默认显示本地时区，悬浮显示 ISO 时间。
- 大小统一使用 IEC 单位：KiB / MiB / GiB / TiB。
- 认证失败显示“上游鉴权失败”，不要暴露上游 token。
- 删除缓存必须明确“只删除本地缓存，不删除远端镜像”。
- 删除本地镜像必须明确“会影响本地仓库和拉取”，与代理缓存清理区分。
- 本地 push 命令必须包含正确的 Registry Host、repository 和 tag。
- 禁用上游时应提示已有缓存仍可读。
- 新增上游后立即展示 pull 命令和健康检查结果。
- 长任务返回 job id，前端轮询并在 5 秒内更新进度。
- 所有写操作记录审计；审计日志不记录密码、token 和 Authorization 头。
