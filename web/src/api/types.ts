export type Role = "admin" | "operator" | "viewer";

export type User = {
  id: string;
  username: string;
  role: Role;
  enabled: boolean;
  mustChangePassword: boolean;
  createdAt: string;
  lastLoginAt?: string;
};

export type Session = { user: User; csrfToken?: string; authMode: "session" };

export type Upstream = {
  id: string;
  name: string;
  endpoint: string;
  routeHost?: string;
  routePrefix: string;
  repositoryPrefix?: string;
  authType: "anonymous" | "basic" | "bearer" | "challenge";
  hasCredentials: boolean;
  caPem?: string;
  insecureSkipVerify: boolean;
  tagTtlSeconds: number;
  requestTimeoutSeconds: number;
  maxBlobBytes?: number;
  enabled: boolean;
  healthState: "unknown" | "healthy" | "unhealthy";
  healthMessage?: string;
  healthCheckedAt?: string;
};

export type UpstreamInput = {
  id?: string;
  name: string;
  endpoint: string;
  routeHost?: string;
  routePrefix?: string;
  repositoryPrefix?: string;
  authType: Upstream["authType"];
  caPem?: string;
  insecureSkipVerify: boolean;
  tagTtlSeconds: number;
  requestTimeoutSeconds: number;
  maxBlobBytes?: number;
  clearMaxBlobBytes?: boolean;
  enabled: boolean;
  replaceCredentials?: boolean;
  credentials?: { username?: string; password?: string; bearerToken?: string };
};

export type RepositorySummary = {
  id: number;
  namespace: string;
  name: string;
  kind: "local" | "cache";
  upstreamId?: string;
  tagCount: number;
  manifestCount: number;
  blobCount: number;
  size: number;
  createdAt: string;
  lastSeenAt?: string;
};

export type TagSummary = { tag: string; digest: string; mediaType: string; size: number; etag?: string; checkedAt: string; lastUsedAt: string; stale: boolean };
export type ContentEntry = { digest: string; algorithm: string; size: number; mediaType?: string; kind: "blob" | "manifest"; state: string; createdAt: string; lastUsedAt: string; accessCount: number; lastVerifiedAt?: string; refCount: number; localRefs: number; cacheRefs: number };
export type RepositoryReferrer = { subjectDigest: string; digest: string; mediaType: string; size: number; artifactType?: string; createdAt: string };
export type ListResponse<T> = { total: number; limit?: number; offset?: number } & T;

export type StorageSettings = { maxBytes: number; cacheMaxBytes: number; minFreeBytes: number; highWatermark: number; lowWatermark: number; onFull: "bypass" | "reject" };
export type RuntimeSection = "registry-auth" | "web-session" | "storage" | "uploads" | "audit" | "proxy" | "outbound-proxy" | "security";
export type RuntimeSectionMeta = { version: number; updatedAt?: string; updatedBy?: string; source: "startup" | "runtime" };
export type AnonymousRegistryAction = "pull" | "push" | "delete" | "catalog";
export type RegistryAuthPolicy = { challenge: "bearer" | "basic"; service: string; realm: string; allowAnonymous: boolean; anonymousActions: AnonymousRegistryAction[]; tokenTtl: string };
export type WebSessionPolicy = { sessionTtl: string; idleTimeout: string };
export type RuntimeStoragePolicy = { maxBytes: number; cacheMaxBytes: number; minFreeBytes: number; highWatermark: number; lowWatermark: number; onFull: "bypass" | "reject"; maxBlobBytes: number; maxManifestBytes: number; downloadConcurrency: number; tmpTtl: string; trashTtl: string };
export type UploadsPolicy = { uploadsTtl: string };
export type AuditPolicy = { auditRetention: string };
export type ProxyPolicy = { disabled: boolean };
export type SecurityPolicy = { hsts: boolean };
export type OutboundProxyPolicy = { httpProxy: string; httpsProxy: string; noProxy: string; username: string; passwordConfigured: boolean; passwordSource: "none" | "startup" | "runtime" };
export type StartupConfig = { listen: string; publicHost: string; publicScheme: string; registryURL: string; adminListen: string; adminPublicHost: string; adminPublicScheme: string; adminURL: string; planeMode: "shared" | "split"; trustedProxies: string[]; tlsMode: string; tlsMinVersion: string; tlsReloadSeconds: number; tlsCertificateConfigured: boolean; tlsKeyConfigured: boolean; storageRoot: string; proxyPathPrefix: string; bootstrapAdminUsername: string; jwtSecretConfigured: boolean; masterKeyConfigured: boolean };
export type RuntimeConfig = {
  registryAuth: RegistryAuthPolicy;
  webSession: WebSessionPolicy;
  storage: RuntimeStoragePolicy;
  uploads: UploadsPolicy;
  audit: AuditPolicy;
  proxy: ProxyPolicy;
  outboundProxy: OutboundProxyPolicy;
  security: SecurityPolicy;
  startup: StartupConfig;
  sections: Record<RuntimeSection, RuntimeSectionMeta>;
};
export type StorageSummary = { settings: StorageSettings; usage: { physicalBytes: number; localBytes: number; cacheOnlyBytes: number; sharedBytes: number; contentCount: number; localContentCount: number; cacheContentCount: number }; disk: { totalBytes: number; freeBytes: number; availableBytes: number; usedPercent: number }; state: "normal" | "high" | "critical" | "emergency"; physicalRatio: number; cacheRatio: number; tempBytes: number; activeDownloads: number };
export type GCStatus = { running: boolean; lastStartedAt?: string; lastFinishedAt?: string; lastError?: string; lastResult?: { orphanContents: number; orphanBytes: number; evictedRepositories: number; evictedBytes: number; trashRemoved: number; tmpRemoved: number; durationMs: number } };
export type StorageSummaryFull = StorageSummary & { gc: GCStatus };
export type Breakdown = { key: string; name: string; kind?: string; bytes: number; repositoryCount: number; contentCount: number };
export type Job = { id: string; type: string; state: "queued" | "running" | "succeeded" | "failed"; progressJson?: string; resultJson?: string; requestedBy?: string; startedAt?: string; finishedAt?: string; error?: string };
export type RegistryEvent = { id: number; at: string; type: string; repositoryId?: number; upstreamId?: string; digest?: string; size: number; cacheHit?: boolean; result: string; message?: string };
export type Overview = {
  upstreams: { total: number; healthy: number; items: Upstream[] };
  repositories: { local: number; cache: number };
  storage: StorageSummaryFull;
  requests24h: { pullRequests: number; cacheHits: number; cacheMisses: number; upstreamErrors: number; bytesServed: number; bytesCached: number };
  cacheHitRate: number;
  activity: RegistryEvent[];
  generatedAt: string;
};

export type EventBucket = { at: string; pullRequests: number; cacheHits: number; cacheMisses: number; upstreamErrors: number; bytesServed: number; bytesCached: number };
export type EventTimeline = { hours: number; since: string; buckets: EventBucket[] };

export type Upload = { id: string; repositoryId: number; repository: string; userId: string; username: string; size: number; state: string; createdAt: string; updatedAt: string; expiresAt: string };
export type AuditEvent = { id: number; at: string; actorType: string; actorId?: string; action: string; targetType?: string; targetId?: string; ip?: string; userAgent?: string; result: string; message?: string; requestId?: string };
export type APIToken = { id: string; userId: string; name: string; prefix: string; scopes: string[]; createdAt: string; expiresAt?: string; revokedAt?: string };

export type SystemInfo = { version: string; startedAt: string; database: string; journal: string; publicURL: string; managementURL: string; registryURL: string; planeMode: "shared" | "split"; registryListen: string; managementListen: string; proxyPathPrefix: string; allowAnonymous: boolean; anonymousActions: AnonymousRegistryAction[]; proxyDisabled: boolean; metricsPath: string; ociSpec: string };
export type APIErrorBody = { code: string; message: string; details?: unknown };
