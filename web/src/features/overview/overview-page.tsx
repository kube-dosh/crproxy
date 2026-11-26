import { useQuery } from "@tanstack/react-query";
import { Boxes, Database, Gauge, HardDrive, Layers3, RefreshCw, TerminalSquare } from "lucide-react";
import { Link } from "@/lib/router";
import { api } from "@/api/client";
import type { EventBucket, EventTimeline } from "@/api/types";
import { CopyButton } from "@/components/common/copy-button";
import { StatusBadge } from "@/components/common/status-badge";
import { MetricCard } from "@/components/data/metric-card";
import { ErrorState, LoadingState } from "@/components/feedback/states";
import { PageHeader } from "@/components/layout/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import { formatBytes, formatTime, percent } from "@/lib/utils";

export function OverviewPage() {
  const overview = useQuery({ queryKey: ["overview"], queryFn: () => api.overview(), refetchInterval: 30_000 });
  const timeline = useQuery({ queryKey: ["events", "timeline", 24], queryFn: () => api.eventTimeline(24), refetchInterval: 60_000 });
  const system = useQuery({ queryKey: ["system", "info"], queryFn: () => api.systemInfo(), staleTime: 60_000 });

  if (overview.isLoading) return <LoadingState label="正在汇总 Registry 状态…" />;
  if (overview.error || !overview.data) return <ErrorState error={overview.error} onRetry={() => void overview.refetch()} />;
  const data = overview.data;
  const storage = data.storage;
  const storageTone = storage.state === "normal" ? "success" : storage.state === "high" ? "warning" : "destructive";
  return <>
    <PageHeader title="总览" description="本地仓库、代理缓存、上游健康度与磁盘容量的统一运行视图。" actions={<Button variant="outline" size="sm" onClick={() => { void overview.refetch(); void timeline.refetch(); }} disabled={overview.isFetching}><RefreshCw className={`h-4 w-4 ${overview.isFetching ? "animate-spin" : ""}`} />刷新</Button>} />
    <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <MetricCard label="本地仓库" value={String(data.repositories.local)} hint="完整 OCI pull / push / delete" icon={Boxes} />
      <MetricCard label="代理缓存仓库" value={String(data.repositories.cache)} hint="只读 pull-through cache" icon={Layers3} />
      <MetricCard label="上游健康" value={`${data.upstreams.healthy}/${data.upstreams.total}`} hint="健康状态由连接测试与回源结果更新" icon={Database} tone={data.upstreams.total > 0 && data.upstreams.healthy === data.upstreams.total ? "success" : "warning"} />
      <MetricCard label="24h 缓存命中率" value={`${(data.cacheHitRate * 100).toFixed(1)}%`} hint={`${data.requests24h.cacheHits} 命中 / ${data.requests24h.cacheMisses} 未命中`} icon={Gauge} tone={data.cacheHitRate >= 0.7 ? "success" : "default"} />
    </div>

    <div className="mt-4 grid gap-4 xl:grid-cols-[minmax(0,1.65fr)_minmax(300px,1fr)]">
      <Card><CardHeader className="flex-row items-start justify-between gap-3"><div><CardTitle>24 小时请求趋势</CardTitle><CardDescription>按小时展示命中、回源未命中与上游错误。</CardDescription></div><Badge variant="secondary">{data.requests24h.pullRequests} 次 pull</Badge></CardHeader><CardContent>{timeline.isLoading ? <LoadingState label="加载趋势…" /> : timeline.error || !timeline.data ? <ErrorState error={timeline.error} onRetry={() => void timeline.refetch()} /> : <TimelineChart timeline={timeline.data} />}</CardContent></Card>

      <Card><CardHeader><CardTitle>存储水位</CardTitle><CardDescription>本地与缓存共享同一 CAS，物理容量只计算一次。</CardDescription></CardHeader><CardContent className="space-y-5">
        <div><div className="mb-2 flex items-center justify-between gap-2"><span className="text-sm font-medium">物理容量</span><StatusBadge value={storage.state} /></div><Progress value={percent(storage.usage.physicalBytes, storage.settings.maxBytes)} tone={storageTone} /><div className="mt-2 flex justify-between text-xs text-muted-foreground"><span>{formatBytes(storage.usage.physicalBytes)}</span><span>上限 {formatBytes(storage.settings.maxBytes)}</span></div></div>
        <div><div className="mb-2 flex items-center justify-between"><span className="text-sm font-medium">缓存配额</span><span className="text-xs text-muted-foreground">{Math.round(storage.cacheRatio * 100)}%</span></div><Progress value={percent(storage.usage.cacheOnlyBytes, storage.settings.cacheMaxBytes)} tone={storage.cacheRatio >= 0.9 ? "destructive" : storage.cacheRatio >= storage.settings.highWatermark ? "warning" : "default"} /><div className="mt-2 flex justify-between text-xs text-muted-foreground"><span>{formatBytes(storage.usage.cacheOnlyBytes)}</span><span>上限 {formatBytes(storage.settings.cacheMaxBytes)}</span></div></div>
        <div className="grid grid-cols-2 gap-3 text-sm"><InfoCell label="磁盘可用" value={formatBytes(storage.disk.availableBytes)} /><InfoCell label="活跃下载" value={String(storage.activeDownloads)} /><InfoCell label="临时文件" value={formatBytes(storage.tempBytes)} /><InfoCell label="GC 状态" value={storage.gc.running ? "运行中" : storage.gc.lastFinishedAt ? "已结束" : "未运行"} /></div>
        <Button variant="outline" size="sm" className="w-full" asChild><Link href="/storage"><HardDrive className="h-4 w-4" />进入存储与磁盘</Link></Button>
      </CardContent></Card>
    </div>

    <div className="mt-4 grid gap-4 xl:grid-cols-2">
      <Card><CardHeader className="flex-row items-center justify-between"><div><CardTitle>上游状态</CardTitle><CardDescription>支持 Docker Hub、GHCR、Quay、registry.k8s.io 与自定义 OCI Registry。</CardDescription></div><Button variant="ghost" size="sm" asChild><Link href="/upstreams">管理</Link></Button></CardHeader><CardContent className="p-0"><div className="overflow-x-auto"><table className="w-full text-sm"><thead className="border-y bg-muted/40 text-left text-xs text-muted-foreground"><tr><th className="px-5 py-2.5 font-medium">名称</th><th className="px-3 py-2.5 font-medium">路由</th><th className="px-3 py-2.5 font-medium">鉴权</th><th className="px-5 py-2.5 text-right font-medium">状态</th></tr></thead><tbody className="divide-y">{data.upstreams.items.map((upstream) => <tr key={upstream.id}><td className="px-5 py-3"><p className="font-medium">{upstream.name}</p><p className="max-w-52 truncate text-xs text-muted-foreground">{upstream.endpoint}</p></td><td className="px-3 py-3 font-mono text-xs">{upstream.routeHost || `/${system.data?.proxyPathPrefix ?? "r"}/${upstream.routePrefix}`}</td><td className="px-3 py-3 text-xs uppercase">{upstream.authType}</td><td className="px-5 py-3 text-right"><StatusBadge value={upstream.enabled ? upstream.healthState : "disabled"} labels={{ disabled: "已停用" }} /></td></tr>)}{data.upstreams.items.length === 0 ? <tr><td colSpan={4} className="px-5 py-10 text-center text-muted-foreground">尚未配置上游</td></tr> : null}</tbody></table></div></CardContent></Card>

      <Card><CardHeader><CardTitle>最近活动</CardTitle><CardDescription>最近 30 条 manifest、blob 与缓存事件。</CardDescription></CardHeader><CardContent>{data.activity.length === 0 ? <div className="py-10 text-center text-sm text-muted-foreground">暂无 Registry 活动</div> : <div className="space-y-3">{data.activity.slice(0, 8).map((event) => <div key={event.id} className="flex gap-3"><span className={`mt-1 h-2 w-2 shrink-0 rounded-full ${event.result === "success" ? "bg-success" : "bg-destructive"}`} /><div className="min-w-0 flex-1"><div className="flex flex-wrap items-center gap-2 text-sm"><span className="font-medium">{event.type}</span>{event.cacheHit === true ? <Badge variant="success">HIT</Badge> : event.cacheHit === false ? <Badge variant="secondary">MISS</Badge> : null}<span className="ml-auto text-xs text-muted-foreground">{formatTime(event.at)}</span></div><p className="truncate font-mono text-xs text-muted-foreground">{event.digest || event.message || event.upstreamId || "—"} · {formatBytes(event.size)}</p></div></div>)}</div>}</CardContent></Card>
    </div>

    <QuickCommands systemInfo={system.data} upstream={data.upstreams.items[0]} />
  </>;
}

function InfoCell({ label, value }: { label: string; value: string }) {
  return <div className="rounded-md border bg-muted/20 p-2.5"><p className="text-[11px] text-muted-foreground">{label}</p><p className="mt-0.5 truncate font-medium tabular-nums">{value}</p></div>;
}

function TimelineChart({ timeline }: { timeline: EventTimeline }) {
  const max = Math.max(1, ...timeline.buckets.map((item) => item.cacheHits + item.cacheMisses + item.upstreamErrors));
  const totals = timeline.buckets.reduce((summary, item) => ({ hits: summary.hits + item.cacheHits, misses: summary.misses + item.cacheMisses, errors: summary.errors + item.upstreamErrors }), { hits: 0, misses: 0, errors: 0 });
  return <div><div className="mb-4 flex flex-wrap gap-4 text-xs"><Legend color="bg-success" label={`命中 ${totals.hits}`} /><Legend color="bg-primary" label={`未命中 ${totals.misses}`} /><Legend color="bg-destructive" label={`上游错误 ${totals.errors}`} /></div><div className="flex h-48 items-end gap-[3px] rounded-md border bg-muted/15 p-3" aria-label="24 小时请求趋势">{timeline.buckets.map((bucket) => <Bar key={bucket.at} bucket={bucket} max={max} />)}</div><div className="mt-2 flex justify-between text-[10px] text-muted-foreground"><span>{formatHour(timeline.buckets[0]?.at)}</span><span>{formatHour(timeline.buckets[Math.floor(timeline.buckets.length / 2)]?.at)}</span><span>{formatHour(timeline.buckets[timeline.buckets.length - 1]?.at)}</span></div></div>;
}

function Bar({ bucket, max }: { bucket: EventBucket; max: number }) {
  const total = bucket.cacheHits + bucket.cacheMisses + bucket.upstreamErrors;
  const height = total === 0 ? 2 : Math.max(4, Math.round((total / max) * 100));
  const hitShare = total > 0 ? (bucket.cacheHits / total) * 100 : 0;
  const missShare = total > 0 ? (bucket.cacheMisses / total) * 100 : 0;
  return <div className="group relative flex h-full flex-1 items-end" title={`${formatTime(bucket.at)}\n命中 ${bucket.cacheHits} · 未命中 ${bucket.cacheMisses} · 错误 ${bucket.upstreamErrors}\n服务 ${formatBytes(bucket.bytesServed)} · 缓存 ${formatBytes(bucket.bytesCached)}`}><div className="flex w-full flex-col overflow-hidden rounded-sm bg-border/60 transition-opacity group-hover:opacity-75" style={{ height: `${height}%` }}>{bucket.upstreamErrors > 0 ? <div className="bg-destructive" style={{ flexGrow: bucket.upstreamErrors }} /> : null}{bucket.cacheMisses > 0 ? <div className="bg-primary" style={{ flexGrow: bucket.cacheMisses }} /> : null}{bucket.cacheHits > 0 ? <div className="bg-success" style={{ flexGrow: bucket.cacheHits }} /> : null}{total === 0 ? <div className="h-full bg-border" /> : null}</div>{total > 0 ? <span className="pointer-events-none absolute inset-x-0 bottom-0 hidden text-center text-[8px] text-white/90 group-hover:block">{hitShare >= 50 ? `${Math.round(hitShare)}%` : missShare >= 50 ? `${Math.round(missShare)}%` : ""}</span> : null}</div>;
}

function Legend({ color, label }: { color: string; label: string }) {
  return <span className="inline-flex items-center gap-1.5 text-muted-foreground"><span className={`h-2 w-2 rounded-sm ${color}`} />{label}</span>;
}

function formatHour(value?: string) {
  if (!value) return "";
  return new Date(value).toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false });
}

function QuickCommands({ systemInfo, upstream }: { systemInfo?: import("@/api/types").SystemInfo; upstream?: import("@/api/types").Upstream }) {
  const url = systemInfo?.publicURL || window.location.origin;
  const host = url.replace(/^https?:\/\//, "");
  const insecure = url.startsWith("http://") ? " --tls-verify=false" : "";
  const proxy = `podman pull${insecure} ${host}/${systemInfo?.proxyPathPrefix ?? "r"}/${upstream?.routePrefix ?? "dockerhub"}/${upstream?.repositoryPrefix ? `${upstream.repositoryPrefix}/` : ""}busybox:latest`;
  const local = `podman tag busybox ${host}/local/demo:latest && podman push${insecure} ${host}/local/demo:latest`;
  const login = `podman login${insecure} ${host}`;
  return <Card className="mt-4"><CardHeader><div className="flex items-center gap-2"><TerminalSquare className="h-4 w-4 text-primary" /><CardTitle>Podman 快速开始</CardTitle></div><CardDescription>以下命令已按当前公开地址生成；生产环境请使用 HTTPS。</CardDescription></CardHeader><CardContent className="grid gap-3 lg:grid-cols-3"><CommandBox label="登录 Registry" value={login} /><CommandBox label="通过代理缓存拉取" value={proxy} /><CommandBox label="推送到本地仓库" value={local} /></CardContent></Card>;
}

function CommandBox({ label, value }: { label: string; value: string }) {
  return <div className="min-w-0 rounded-md border bg-muted/25 p-3"><div className="mb-2 flex items-center justify-between gap-2"><span className="text-xs font-medium">{label}</span><CopyButton value={value} label="" className="h-7 px-1.5" /></div><code className="block break-all font-mono text-[11px] leading-5 text-muted-foreground">{value}</code></div>;
}
