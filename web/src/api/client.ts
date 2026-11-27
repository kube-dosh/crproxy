import type {
  APIToken, APIErrorBody, AuditEvent, Breakdown, ContentEntry, EventTimeline, Job, ListResponse, Overview,
  RepositoryReferrer, RepositorySummary, RuntimeConfig, RuntimeSection, Session, StorageSettings, StorageSummaryFull, SystemInfo,
  TagSummary, Upstream, UpstreamInput, Upload, User,
} from "@/api/types";

export const unauthorizedEvent = "crproxy:unauthorized";
let csrfToken = "";

export class ApiError extends Error {
  status: number;
  code: string;
  details?: unknown;
  constructor(status: number, code: string, message: string, details?: unknown) {
    super(message);
    this.status = status;
    this.code = code;
    this.details = details;
  }
}

export function setCSRFToken(value?: string) {
  csrfToken = value ?? "";
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  if (init.body && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  if (csrfToken && init.method && !["GET", "HEAD", "OPTIONS"].includes(init.method)) headers.set("X-CSRF-Token", csrfToken);
  const response = await fetch(path, { ...init, headers, credentials: "same-origin" });
  if (response.status === 401) {
    if (!path.endsWith("/auth/login")) window.dispatchEvent(new Event(unauthorizedEvent));
    throw new ApiError(401, "unauthenticated", "登录已失效，请重新登录");
  }
  const contentType = response.headers.get("content-type") ?? "";
  if (!response.ok) {
    let body: APIErrorBody | undefined;
    if (contentType.includes("application/json")) body = await response.json().catch(() => undefined);
    throw new ApiError(response.status, body?.code ?? "request_failed", body?.message ?? `请求失败（HTTP ${response.status}）`, body?.details);
  }
  if (response.status === 204 || response.headers.get("content-length") === "0") return undefined as T;
  if (contentType.includes("application/json")) return response.json() as Promise<T>;
  return (await response.text()) as T;
}

export const api = {
  session: () => request<Session>("/api/v1/auth/session"),
  login: (username: string, password: string) => request<Session>("/api/v1/auth/login", { method: "POST", body: JSON.stringify({ username, password }) }),
  logout: () => request<void>("/api/v1/auth/logout", { method: "POST" }),
  changePassword: (currentPassword: string, newPassword: string) => request<void>("/api/v1/auth/password", { method: "POST", body: JSON.stringify({ currentPassword, newPassword }) }),
  systemInfo: () => request<SystemInfo>("/api/v1/system/info"),
  runtimeSettings: () => request<RuntimeConfig>("/api/v1/settings/runtime"),
  updateRuntimeSection: <T extends object>(section: RuntimeSection, expectedVersion: number, values: T) => request<RuntimeConfig>(`/api/v1/settings/runtime/${section}`, { method: "PUT", body: JSON.stringify({ expectedVersion, ...values }) }),
  resetRuntimeSection: (section: RuntimeSection, expectedVersion: number) => request<RuntimeConfig>(`/api/v1/settings/runtime/${section}/reset`, { method: "POST", body: JSON.stringify({ expectedVersion }) }),
  testOutboundProxy: (url?: string) => request<{ url: string; status: number; durationMs: number }>("/api/v1/settings/runtime/outbound-proxy/test", { method: "POST", body: JSON.stringify(url ? { url } : {}) }),
  overview: () => request<Overview>("/api/v1/overview"),
  eventTimeline: (hours = 24) => request<EventTimeline>(`/api/v1/events/timeline?hours=${hours}`),

  repositories: (params: { kind?: string; upstreamId?: string; q?: string; limit?: number; offset?: number } = {}) => {
    const query = new URLSearchParams();
    if (params.kind) query.set("kind", params.kind);
    if (params.upstreamId) query.set("upstream_id", params.upstreamId);
    if (params.q) query.set("q", params.q);
    if (params.limit) query.set("limit", String(params.limit));
    if (params.offset) query.set("offset", String(params.offset));
    return request<ListResponse<{ repositories: RepositorySummary[] }>>(`/api/v1/repositories?${query}`);
  },
  repository: (id: number) => request<RepositorySummary>(`/api/v1/repositories/${id}`),
  repositoryTags: (id: number) => request<ListResponse<{ tags: TagSummary[] }>>(`/api/v1/repositories/${id}/tags`),
  repositoryManifests: (id: number, params = "") => request<ListResponse<{ entries: ContentEntry[] }>>(`/api/v1/repositories/${id}/manifests${params}`),
  repositoryBlobs: (id: number, params = "") => request<ListResponse<{ entries: ContentEntry[] }>>(`/api/v1/repositories/${id}/blobs${params}`),
  repositoryReferrers: (id: number) => request<ListResponse<{ referrers: RepositoryReferrer[] }>>(`/api/v1/repositories/${id}/referrers`),
  deleteRepository: (id: number) => request<{ deleted: boolean }>(`/api/v1/repositories/${id}`, { method: "DELETE" }),
  deleteManifest: (id: number, digest: string) => request<{ deleted: boolean }>(`/api/v1/repositories/${id}/manifests/${encodeURIComponent(digest)}`, { method: "DELETE" }),
  deleteTag: (id: number, tag: string) => request<void>(`/api/v1/repositories/${id}/tags/${encodeURIComponent(tag)}`, { method: "DELETE" }),

  upstreams: () => request<{ upstreams: Upstream[] }>("/api/v1/upstreams"),
  upstream: (id: string) => request<Upstream>(`/api/v1/upstreams/${encodeURIComponent(id)}`),
  createUpstream: (input: UpstreamInput) => request<Upstream>("/api/v1/upstreams", { method: "POST", body: JSON.stringify(input) }),
  updateUpstream: (id: string, input: UpstreamInput) => request<Upstream>(`/api/v1/upstreams/${encodeURIComponent(id)}`, { method: "PUT", body: JSON.stringify(input) }),
  deleteUpstream: (id: string) => request<void>(`/api/v1/upstreams/${encodeURIComponent(id)}`, { method: "DELETE" }),
  testUpstream: (id: string) => request<{ status: string }>(`/api/v1/upstreams/${encodeURIComponent(id)}/test`, { method: "POST" }),

  cacheEntries: (params: { upstreamId?: string; kind?: string; q?: string; limit?: number; offset?: number } = {}) => {
    const query = new URLSearchParams();
    if (params.upstreamId) query.set("upstream_id", params.upstreamId);
    if (params.kind) query.set("kind", params.kind);
    if (params.q) query.set("q", params.q);
    if (params.limit) query.set("limit", String(params.limit));
    if (params.offset) query.set("offset", String(params.offset));
    return request<ListResponse<{ entries: ContentEntry[] }>>(`/api/v1/cache/entries?${query}`);
  },
  evictRepository: (repositoryId: number) => request<Job>("/api/v1/cache/evictions", { method: "POST", body: JSON.stringify({ repositoryId }) }),
  evictDigests: (digests: string[]) => request<Job>("/api/v1/cache/evictions", { method: "POST", body: JSON.stringify({ digests }) }),
  job: (id: string) => request<Job>(`/api/v1/storage/jobs/${encodeURIComponent(id)}`),

  uploads: (activeOnly = false) => request<{ uploads: Upload[]; total: number }>(`/api/v1/uploads${activeOnly ? "/active" : ""}`),
  deleteUpload: (id: string) => request<{ deleted: boolean }>(`/api/v1/uploads/${encodeURIComponent(id)}`, { method: "DELETE" }),

  storageSummary: () => request<StorageSummaryFull>("/api/v1/storage/summary"),
  storageBreakdown: (groupBy: "upstream" | "repository" = "upstream") => request<{ groupBy: string; items: Breakdown[] }>(`/api/v1/storage/breakdown?group_by=${groupBy}`),
  storageSettings: () => request<StorageSettings>("/api/v1/storage/settings"),
  updateStorageSettings: (settings: StorageSettings) => request<StorageSettings>("/api/v1/storage/settings", { method: "PUT", body: JSON.stringify(settings) }),
  runGC: () => request<Job>("/api/v1/storage/gc", { method: "POST" }),
  runFsck: (mode: "quick" | "full") => request<Job>("/api/v1/storage/fsck", { method: "POST", body: JSON.stringify({ mode }) }),
  jobs: () => request<{ jobs: Job[] }>("/api/v1/storage/jobs"),

  users: () => request<{ users: User[] }>("/api/v1/users"),
  createUser: (input: { username: string; password: string; role: "admin" | "operator" | "viewer" }) => request<User>("/api/v1/users", { method: "POST", body: JSON.stringify(input) }),
  updateUser: (id: string, input: { role?: string; enabled?: boolean }) => request<User>(`/api/v1/users/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify(input) }),
  deleteUser: (id: string) => request<void>(`/api/v1/users/${encodeURIComponent(id)}`, { method: "DELETE" }),
  resetUserPassword: (id: string, password: string) => request<void>(`/api/v1/users/${encodeURIComponent(id)}/password`, { method: "POST", body: JSON.stringify({ password }) }),
  tokens: () => request<{ tokens: APIToken[] }>("/api/v1/tokens"),
  createToken: (input: { name: string; scopes: string[]; expiresInDays: number }) => request<{ token: string; metadata: APIToken }>("/api/v1/tokens", { method: "POST", body: JSON.stringify(input) }),
  revokeToken: (id: string) => request<void>(`/api/v1/tokens/${encodeURIComponent(id)}`, { method: "DELETE" }),
  audit: (params: { before?: number; action?: string; result?: string } = {}) => {
    const query = new URLSearchParams();
    if (params.before) query.set("before", String(params.before));
    if (params.action) query.set("action", params.action);
    if (params.result) query.set("result", params.result);
    return request<{ events: AuditEvent[]; nextBefore?: number }>(`/api/v1/audit?${query}`);
  },
};

export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "未知错误";
}
