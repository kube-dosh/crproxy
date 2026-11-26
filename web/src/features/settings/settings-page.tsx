import { useQuery } from "@tanstack/react-query";
import { Activity, Database, LockKeyhole, RefreshCw, ServerCog, ShieldCheck, TerminalSquare } from "lucide-react";
import { api } from "@/api/client";
import { CopyButton } from "@/components/common/copy-button";
import { ErrorState, LoadingState } from "@/components/feedback/states";
import { PageHeader } from "@/components/layout/page-header";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { formatTime } from "@/lib/utils";

export function SettingsPage() {
  const system = useQuery({ queryKey: ["system", "info"], queryFn: api.systemInfo });
  const storage = useQuery({ queryKey: ["storage", "summary"], queryFn: api.storageSummary });
  const upstreams = useQuery({ queryKey: ["upstreams"], queryFn: api.upstreams });
  if (system.isLoading) return <LoadingState />;
  if (system.error || !system.data) return <ErrorState error={system.error} onRetry={() => void system.refetch()} />;
  const info = system.data;
  const secure = info.publicURL.startsWith("https://");
  return <>
    <PageHeader title="系统设置" description="只读运行时信息与安全基线。TLS、可信代理、认证策略和静态配置由 YAML 管理；可调存储阈值请在“存储与磁盘”页修改。" actions={<Button variant="outline" size="sm" onClick={() => void system.refetch()}><RefreshCw className="h-4 w-4" />刷新</Button>} />
    {!secure ? <Alert className="mb-4 border-warning/30 bg-warning/10"><ShieldCheck className="h-4 w-4 text-warning" /><AlertTitle>当前公开地址不是 HTTPS</AlertTitle><AlertDescription>仅允许本机或隔离开发环境。生产部署必须启用 TLS 或由可信反向代理终止 TLS，并设置 server.public_scheme=https。</AlertDescription></Alert> : <Alert className="mb-4 border-success/30 bg-success/10"><ShieldCheck className="h-4 w-4 text-success" /><AlertTitle>HTTPS 已启用</AlertTitle><AlertDescription>管理会话将使用 Secure + HttpOnly + SameSite=Lax Cookie；建议同时启用 HSTS 和可信代理列表。</AlertDescription></Alert>}
    <div className="grid gap-4 xl:grid-cols-2">
      <Card><CardHeader><div className="flex items-center gap-2"><ServerCog className="h-4 w-4 text-primary" /><CardTitle>运行时</CardTitle></div><CardDescription>进程与数据库状态。</CardDescription></CardHeader><CardContent><dl className="grid grid-cols-2 gap-3 text-sm"><Field label="版本" value={info.version} mono /><Field label="启动时间" value={formatTime(info.startedAt)} /><Field label="数据库" value={info.database} /><Field label="Journal" value={info.journal} mono /><Field label="OCI Spec" value={info.ociSpec} /><Field label="代理路径" value={`/${info.proxyPathPrefix}`} mono /></dl><div className="mt-4 flex flex-wrap gap-2"><Badge variant={info.journal === "delete" ? "success" : "warning"}>{info.journal === "delete" ? "无 WAL" : `journal=${info.journal}`}</Badge><Badge variant={info.allowAnonymous ? "warning" : "success"}>{info.allowAnonymous ? "Registry 允许匿名拉取" : "Registry 强制认证"}</Badge><Badge variant="outline">上游 {upstreams.data?.upstreams.length ?? 0}</Badge></div></CardContent></Card>
      <Card><CardHeader><div className="flex items-center gap-2"><Activity className="h-4 w-4 text-primary" /><CardTitle>端点</CardTitle></div><CardDescription>可直接用于探针、监控和 Podman。</CardDescription></CardHeader><CardContent className="space-y-3"><Endpoint label="Registry API" value={`${info.publicURL}/v2/`} /><Endpoint label="健康检查" value={`${info.publicURL}/api/v1/healthz`} /><Endpoint label="Prometheus 指标" value={`${info.publicURL}${info.metricsPath}`} /><Endpoint label="管理台" value={info.publicURL} /></CardContent></Card>
      <Card><CardHeader><div className="flex items-center gap-2"><Database className="h-4 w-4 text-primary" /><CardTitle>存储</CardTitle></div><CardDescription>线上阈值可在“存储与磁盘”页面修改。</CardDescription></CardHeader><CardContent>{storage.data ? <dl className="grid grid-cols-2 gap-3 text-sm"><Field label="物理上限" value={formatBytesPlain(storage.data.settings.maxBytes)} /><Field label="缓存上限" value={formatBytesPlain(storage.data.settings.cacheMaxBytes)} /><Field label="最低剩余磁盘" value={formatBytesPlain(storage.data.settings.minFreeBytes)} /><Field label="满盘策略" value={storage.data.settings.onFull} mono /><Field label="高 / 低水位" value={`${storage.data.settings.highWatermark} / ${storage.data.settings.lowWatermark}`} /><Field label="当前状态" value={storage.data.state} /></dl> : <p className="text-sm text-muted-foreground">正在读取存储摘要…</p>}</CardContent></Card>
      <Card><CardHeader><div className="flex items-center gap-2"><LockKeyhole className="h-4 w-4 text-primary" /><CardTitle>安全基线</CardTitle></div><CardDescription>本版本不接入 OIDC、LDAP 或 SAML。</CardDescription></CardHeader><CardContent className="space-y-2 text-sm text-muted-foreground"><p>• 本地账号 + Basic / PAT + Docker Registry Bearer Token。</p><p>• 管理台 Session 与 CSRF、Origin 校验、登录限流、审计日志。</p><p>• 上游凭证 AES-256-GCM 加密，主密钥与数据库分离。</p><p>• 代理路由永久只读，写方法返回 405。</p><p>• SQLite 使用 rollback journal，不产生 WAL/SHM。</p></CardContent></Card>
    </div>
    <Card className="mt-4"><CardHeader><div className="flex items-center gap-2"><TerminalSquare className="h-4 w-4 text-primary" /><CardTitle>配置与备份</CardTitle></div><CardDescription>配置变更通过受控的 YAML 与进程重启完成；数据目录备份应包含 meta、cas、uploads。</CardDescription></CardHeader><CardContent><div className="rounded-md border bg-muted/20 p-3 font-mono text-xs leading-6"><p>crproxy -config /etc/crproxy/crproxy.yaml</p><p>curl -fsS {info.publicURL}/api/v1/healthz</p><p>curl -fsS {info.publicURL}{info.metricsPath}</p></div></CardContent></Card>
  </>;
}

function Field({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return <div><dt className="text-xs text-muted-foreground">{label}</dt><dd className={`mt-0.5 break-all font-medium ${mono ? "font-mono text-xs" : ""}`}>{value}</dd></div>;
}

function Endpoint({ label, value }: { label: string; value: string }) {
  return <div className="rounded-md border bg-muted/20 p-2.5"><div className="flex items-center justify-between gap-2"><p className="text-xs text-muted-foreground">{label}</p><CopyButton value={value} label="" className="h-7 px-1.5" /></div><code className="mt-1 block break-all font-mono text-xs">{value}</code></div>;
}

function formatBytesPlain(value: number) { return new Intl.NumberFormat("zh-CN").format(value) + " B"; }
