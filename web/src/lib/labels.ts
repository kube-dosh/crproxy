const mediaTypes: Record<string, string> = {
  "application/vnd.oci.image.index.v1+json": "多架构镜像清单",
  "application/vnd.docker.distribution.manifest.list.v2+json": "多架构镜像清单",
  "application/vnd.oci.image.manifest.v1+json": "镜像清单",
  "application/vnd.docker.distribution.manifest.v2+json": "镜像清单",
  "application/vnd.oci.image.config.v1+json": "镜像配置",
  "application/vnd.docker.container.image.v1+json": "镜像配置",
  "application/vnd.oci.image.layer.v1.tar+gzip": "镜像层",
  "application/vnd.oci.image.layer.nondistributable.v1.tar+gzip": "镜像层",
  "application/vnd.docker.image.rootfs.diff.tar.gzip": "镜像层",
  "application/vnd.oci.empty.v1+json": "空白内容",
};

const jobTypes: Record<string, string> = {
  "storage.gc": "空间回收",
  "storage.fsck": "存储检查",
  "cache.eviction": "缓存清理",
};

const scopes: Record<string, string> = { pull: "拉取", push: "推送", delete: "删除", manage: "管理", admin: "管理员" };
const authTypes: Record<string, string> = { anonymous: "匿名", challenge: "自动获取令牌", basic: "用户名密码", bearer: "固定访问令牌" };
const actorTypes: Record<string, string> = { user: "用户", anonymous: "匿名访客", token: "访问令牌", system: "系统" };
const targetTypes: Record<string, string> = { user: "用户", token: "访问令牌", upstream: "上游仓库", repository: "镜像仓库", upload: "上传会话", cache: "代理缓存", config: "系统设置", storage: "存储设置" };
const eventTypes: Record<string, string> = { "manifest.pull": "拉取镜像清单", "blob.pull": "拉取镜像层", "manifest.cache": "缓存镜像清单", "blob.cache": "缓存镜像层" };
const uploadStates: Record<string, string> = { created: "等待数据", uploading: "上传中", completed: "已完成", cancelled: "已取消" };

const auditActions: Record<string, string> = {
  "config.runtime.update": "更新系统设置",
  "config.runtime.reset": "恢复系统默认",
  "config.runtime.proxy_test": "测试出站代理",
  "storage.settings.update": "更新存储设置",
  "storage.gc": "运行空间回收",
  "storage.fsck": "运行存储检查",
  "upload.cancel": "取消上传",
  "cache.evict": "清理代理缓存",
  "auth.token.create": "创建访问令牌",
  "auth.token.revoke": "撤销访问令牌",
  "repository.delete": "删除镜像仓库",
  "manifest.delete": "删除镜像清单",
  "tag.delete": "删除镜像标签",
  "user.create": "创建用户",
  "user.update": "更新用户",
  "user.delete": "删除用户",
  "user.password.reset": "重置用户密码",
  "upstream.create": "创建上游仓库",
  "upstream.update": "更新上游仓库",
  "upstream.delete": "删除上游仓库",
  "upstream.test": "测试上游连接",
  "auth.login.success": "登录成功",
  "auth.login.failure": "登录失败",
  "auth.logout": "退出登录",
  "auth.password.change": "修改密码",
};

const auditMessages: Record<string, string> = {
  "invalid credentials": "用户名或密码错误",
  "login rate limited": "登录尝试过于频繁",
  "current password is incorrect": "当前密码不正确",
  "range request streamed without caching": "分段请求未写入缓存",
  "cache bypass": "已直接回源",
  "process restarted": "服务重启，任务已中断",
};

const jobProgressMessages: Record<string, string> = {
  "collecting orphan content and cache eviction candidates": "正在整理可回收数据",
  "garbage collection completed": "空间回收已完成",
  "checking CAS and metadata integrity": "正在检查存储数据完整性",
  "fsck completed": "存储检查已完成",
  "evicting cache repository": "正在清理缓存仓库",
};

const configTargetIds: Record<string, string> = {
  "registry-auth": "仓库访问控制",
  "web-session": "管理台登录会话",
  storage: "存储策略",
  uploads: "上传有效期",
  audit: "审计保留期",
  proxy: "代理服务",
  "outbound-proxy": "出站网络代理",
  security: "HTTPS 强制访问",
};

const storageTargetIds: Record<string, string> = {
  settings: "存储设置",
  gc: "空间回收",
  quick: "快速检查",
  full: "完整检查",
};

export const auditActionOptions = Object.entries(auditActions).map(([value, label]) => ({ value, label }));

export function contentKindLabel(value?: string) {
  return value === "manifest" ? "镜像清单" : value === "blob" ? "镜像层" : "镜像内容";
}

export function mediaTypeLabel(value?: string) {
  if (!value) return "—";
  return mediaTypes[value] ?? "其他镜像内容";
}

export function jobTypeLabel(value?: string) {
  return jobTypes[value || ""] ?? "运维任务";
}

export function scopeLabel(value?: string) {
  return scopes[value || ""] ?? "其他权限";
}

export function authTypeLabel(value?: string) {
  return authTypes[value || ""] ?? "未配置";
}

export function actorTypeLabel(value?: string) {
  return actorTypes[value || ""] ?? "其他来源";
}

export function targetTypeLabel(value?: string) {
  return targetTypes[value || ""] ?? "其他对象";
}

export function registryEventLabel(value?: string) {
  return eventTypes[value || ""] ?? "镜像访问";
}

export function uploadStateLabel(value?: string) {
  return uploadStates[value || ""] ?? "处理中";
}

export function auditActionLabel(value?: string) {
  return auditActions[value || ""] ?? "其他操作";
}

export function auditMessageLabel(value?: string) {
  if (!value) return "";
  if (/^status=\d+$/.test(value)) return "代理连接测试完成";
  if (/upstream|token endpoint|dial tcp|proxyconnect|x509|deadline exceeded|no such host|connection refused/i.test(value)) return upstreamHealthMessageLabel(value);
  return auditMessages[value] ?? value;
}

export function upstreamHealthMessageLabel(value?: string) {
  if (!value) return "";
  if (/[\u4e00-\u9fff]/.test(value)) return value;
  let match = value.match(/^upstream token endpoint returned (\d+)$/i);
  if (match) return `上游身份验证失败（HTTP ${match[1]}）`;
  match = value.match(/^upstream returned (?:HTTP )?(\d+)$/i);
  if (match) return `上游返回异常状态（HTTP ${match[1]}）`;
  if (/token realm must use HTTPS/i.test(value)) return "上游令牌服务地址必须使用 HTTPS";
  if (/invalid upstream token realm|token refresh produced no token|token response has no token/i.test(value)) return "上游身份验证配置异常";
  if (/timeout|deadline exceeded/i.test(value)) return "连接上游超时";
  if (/x509|certificate/i.test(value)) return "上游 HTTPS 证书校验失败";
  if (/proxyconnect|dial tcp|connection refused|no such host|network is unreachable/i.test(value)) return "无法连接上游，请检查地址和网络代理";
  return "上游连接检查失败，请检查地址、网络代理与登录凭证";
}

export function jobProgressLabel(value?: string) {
  if (!value) return "";
  const fixed = jobProgressMessages[value];
  if (fixed) return fixed;
  const eviction = value.match(/^evicting digest (\d+)\/(\d+)$/);
  if (eviction) return `正在清理缓存内容 ${eviction[1]}/${eviction[2]}`;
  return /[\u4e00-\u9fff]/.test(value) ? value : "正在处理任务";
}

export function jobErrorLabel(value?: string) {
  if (!value) return "";
  return auditMessages[value] ?? value;
}

export function auditTargetIdLabel(targetType?: string, value?: string) {
  if (!value) return "—";
  if (targetType === "config") return configTargetIds[value] ?? "系统设置";
  if (targetType === "storage") return storageTargetIds[value] ?? "存储设置";
  return value;
}
