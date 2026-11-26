import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, CheckCircle2, Database, HardDrive, Loader2, Play, RefreshCw, ShieldCheck, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { api, errorMessage } from "@/api/client";
import type { Job, StorageSettings } from "@/api/types";
import { ConfirmDialog } from "@/components/common/confirm-dialog";
import { MetricCard } from "@/components/data/metric-card";
import { EmptyState, ErrorState, LoadingState } from "@/components/feedback/states";
import { PageHeader } from "@/components/layout/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Progress } from "@/components/ui/progress";
import { Select } from "@/components/ui/select";
import { useSessionQuery } from "@/features/auth/use-session";
import { formatBytes, formatTime, percent } from "@/lib/utils";

export function StoragePage() {
  const session = useSessionQuery();
  const manage = session.data?.user.role === "admin" || session.data?.user.role === "operator";
  const queryClient = useQueryClient();
  const [groupBy, setGroupBy] = useState<"upstream" | "repository">("upstream");
  const [settings, setSettings] = useState<StorageSettings | null>(null);
  const [confirmGC, setConfirmGC] = useState(false);
  const [confirmFsck, setConfirmFsck] = useState(false);
  const [lastJob, setLastJob] = useState<Job | null>(null);
  const summary = useQuery({ queryKey: ["storage", "summary"], queryFn: api.storageSummary, refetchInterval: 15_000 });
  const breakdown = useQuery({ queryKey: ["storage", "breakdown", groupBy], queryFn: () => api.storageBreakdown(groupBy) });
  const jobs = useQuery({ queryKey: ["storage", "jobs"], queryFn: api.jobs, refetchInterval: 5_000 });
  useEffect(() => { if (summary.data) setSettings(summary.data.settings); }, [summary.data]);
  const save = useMutation({ mutationFn: (value: StorageSettings) => api.updateStorageSettings(value), onSuccess: async (value) => { setSettings(value); toast.success("存储设置已保存并立即生效"); await queryClient.invalidateQueries({ queryKey: ["storage"] }); await queryClient.invalidateQueries({ queryKey: ["overview"] }); }, onError: (error) => toast.error(errorMessage(error)) });
  const gc = useMutation({ mutationFn: api.runGC, onSuccess: async (job) => { setLastJob(job); setConfirmGC(false); toast.success("GC 任务已启动"); await queryClient.invalidateQueries({ queryKey: ["storage", "jobs"] }); }, onError: (error) => toast.error(errorMessage(error)) });
  const fsck = useMutation({ mutationFn: (mode: "quick" | "full") => api.runFsck(mode), onSuccess: async (job) => { setLastJob(job); setConfirmFsck(false); toast.success("存储校验任务已启动"); await queryClient.invalidateQueries({ queryKey: ["storage", "jobs"] }); }, onError: (error) => toast.error(errorMessage(error)) });
  if (summary.isLoading) return <LoadingState label="正在统计存储与磁盘…" />;
  if (summary.error || !summary.data) return <ErrorState error={summary.error} onRetry={() => void summary.refetch()} />;
  const data = summary.data;
  const tone = data.state === "normal" ? "success" : data.state === "high" ? "warning" : "destructive";
  const maxBreakdown = Math.max(1, ...(breakdown.data?.items.map((item) => item.bytes) ?? [1]));
  return <>
    <PageHeader title="存储与磁盘" description="SQLite 元数据、共享 CAS、缓存水位、GC 与 fsck 的统一运维面板。" actions={<><Button variant="outline" size="sm" onClick={() => void queryClient.invalidateQueries({ queryKey: ["storage"] })}><RefreshCw className="h-4 w-4" />刷新</Button>{manage ? <><Button variant="outline" size="sm" onClick={() => setConfirmFsck(true)}><ShieldCheck className="h-4 w-4" />fsck</Button><Button size="sm" onClick={() => setConfirmGC(true)}><Play className="h-4 w-4" />运行 GC</Button></> : null}</>} />
    <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4"><MetricCard label="物理用量" value={formatBytes(data.usage.physicalBytes)} hint={`上限 ${formatBytes(data.settings.maxBytes)}`} icon={HardDrive} tone={tone} /><MetricCard label="本地内容" value={formatBytes(data.usage.localBytes)} hint={`${data.usage.localContentCount} 个本地内容`} icon={Database} /><MetricCard label="仅缓存内容" value={formatBytes(data.usage.cacheOnlyBytes)} hint={`上限 ${formatBytes(data.settings.cacheMaxBytes)}`} icon={Trash2} /><MetricCard label="磁盘可用" value={formatBytes(data.disk.availableBytes)} hint={`最低保留 ${formatBytes(data.settings.minFreeBytes)}`} icon={AlertTriangle} tone={data.disk.availableBytes <= data.settings.minFreeBytes ? "destructive" : "success"} /></div>

    <div className="mt-4 grid gap-4 xl:grid-cols-[minmax(0,1.35fr)_minmax(320px,1fr)]">
      <Card><CardHeader><CardTitle>容量水位</CardTitle><CardDescription>高水位触发缓存回收；低水位是回收目标。</CardDescription></CardHeader><CardContent className="space-y-5"><Waterline label="物理容量" value={percent(data.usage.physicalBytes, data.settings.maxBytes)} tone={tone} detail={`${formatBytes(data.usage.physicalBytes)} / ${formatBytes(data.settings.maxBytes)}`} /><Waterline label="缓存配额" value={percent(data.usage.cacheOnlyBytes, data.settings.cacheMaxBytes)} tone={data.cacheRatio >= 0.9 ? "destructive" : data.cacheRatio >= data.settings.highWatermark ? "warning" : "default"} detail={`${formatBytes(data.usage.cacheOnlyBytes)} / ${formatBytes(data.settings.cacheMaxBytes)}`} /><div className="grid grid-cols-2 gap-3 sm:grid-cols-4"><SmallStat label="磁盘水位" value={`${data.disk.usedPercent.toFixed(1)}%`} /><SmallStat label="临时文件" value={formatBytes(data.tempBytes)} /><SmallStat label="活跃下载" value={String(data.activeDownloads)} /><SmallStat label="共享内容" value={formatBytes(data.usage.sharedBytes)} /></div><div className="rounded-md border bg-muted/20 p-3"><div className="flex items-center justify-between gap-2"><span className="text-sm font-medium">最近 GC</span><Badge variant={data.gc.running ? "warning" : data.gc.lastError ? "destructive" : data.gc.lastFinishedAt ? "success" : "secondary"}>{data.gc.running ? "运行中" : data.gc.lastError ? "失败" : data.gc.lastFinishedAt ? "已完成" : "尚未运行"}</Badge></div><p className="mt-1 text-xs text-muted-foreground">{data.gc.lastFinishedAt ? `完成于 ${formatTime(data.gc.lastFinishedAt)}` : "后台每 15 分钟按水位检查一次。"}</p>{data.gc.lastError ? <p className="mt-2 text-xs text-destructive">{data.gc.lastError}</p> : null}{data.gc.lastResult ? <p className="mt-2 text-xs text-muted-foreground">回收 {data.gc.lastResult.orphanContents} 个内容、{formatBytes(data.gc.lastResult.orphanBytes)}；淘汰 {data.gc.lastResult.evictedRepositories} 个缓存仓库。</p> : null}</div></CardContent></Card>

      <Card><CardHeader><CardTitle>阈值与配额</CardTitle><CardDescription>{manage ? "保存后无需重启，立即影响本地写入和代理缓存写入。" : "只读账号不能修改存储设置。"}</CardDescription></CardHeader><CardContent>{settings ? <SettingsForm value={settings} onChange={setSettings} disabled={!manage || save.isPending} /> : null}{manage ? <Button className="mt-4 w-full" onClick={() => settings && save.mutate(settings)} disabled={save.isPending}>{save.isPending ? <Loader2 className="h-4 w-4 animate-spin" /> : <CheckCircle2 className="h-4 w-4" />}保存设置</Button> : null}</CardContent></Card>
    </div>

    <Card className="mt-4"><CardHeader className="flex-row items-end justify-between gap-3"><div><CardTitle>容量明细</CardTitle><CardDescription>共享内容只在物理容量中计算一次。</CardDescription></div><Select className="w-40" value={groupBy} onChange={(event) => setGroupBy(event.target.value as "upstream" | "repository")}><option value="upstream">按上游</option><option value="repository">按仓库</option></Select></CardHeader><CardContent>{breakdown.isLoading ? <LoadingState /> : breakdown.error || !breakdown.data ? <ErrorState error={breakdown.error} onRetry={() => void breakdown.refetch()} /> : breakdown.data.items.length === 0 ? <EmptyState title="暂无容量明细" /> : <div className="space-y-3">{breakdown.data.items.map((item) => <div key={item.key} className="grid grid-cols-[minmax(120px,220px)_1fr_auto] items-center gap-3"><div className="min-w-0"><p className="truncate text-sm font-medium" title={item.name}>{item.name}</p><p className="text-[11px] text-muted-foreground">{item.repositoryCount} 仓库 · {item.contentCount} 内容</p></div><Progress value={percent(item.bytes, maxBreakdown)} /><span className="w-20 text-right text-xs tabular-nums text-muted-foreground">{formatBytes(item.bytes)}</span></div>)}</div>}</CardContent></Card>

    <Card className="mt-4"><CardHeader><CardTitle>后台任务</CardTitle><CardDescription>GC、fsck 与缓存淘汰均可轮询进度；进程重启时未完成任务会标记为失败。</CardDescription></CardHeader><CardContent>{lastJob ? <JobDetail job={lastJob} /> : null}{jobs.isLoading ? <LoadingState /> : jobs.error || !jobs.data ? <ErrorState error={jobs.error} onRetry={() => void jobs.refetch()} /> : jobs.data.jobs.length === 0 ? <EmptyState title="暂无后台任务" /> : <div className="overflow-x-auto"><table className="w-full min-w-[720px] text-sm"><thead className="border-b bg-muted/40 text-left text-xs text-muted-foreground"><tr><th className="px-3 py-2.5 font-medium">任务</th><th className="px-3 py-2.5 font-medium">类型</th><th className="px-3 py-2.5 font-medium">状态</th><th className="px-3 py-2.5 font-medium">发起人</th><th className="px-3 py-2.5 font-medium">开始</th><th className="px-3 py-2.5 font-medium">结束</th><th className="px-3 py-2.5 font-medium">结果</th></tr></thead><tbody className="divide-y">{jobs.data.jobs.map((job) => <JobRow key={job.id} job={job} />)}</tbody></table></div>}</CardContent></Card>

    <ConfirmDialog open={confirmGC} onOpenChange={setConfirmGC} title="运行一次完整 GC？" description="将删除孤立内容，并在达到高水位时按 LRU 淘汰代理缓存；本地仓库内容受保护。" confirmLabel="运行 GC" destructive={false} pending={gc.isPending} onConfirm={() => gc.mutate()} />
    <ConfirmDialog open={confirmFsck} onOpenChange={setConfirmFsck} title="校验存储完整性" description={<>quick 模式检查元数据、文件存在与大小；full 模式还会重算 digest，可能持续较长时间。<div className="mt-3 flex gap-2"><Button size="sm" variant="outline" onClick={() => fsck.mutate("quick")} disabled={fsck.isPending}>Quick fsck</Button><Button size="sm" variant="outline" onClick={() => fsck.mutate("full")} disabled={fsck.isPending}>Full fsck</Button></div></>} confirmLabel="关闭" destructive={false} onConfirm={() => setConfirmFsck(false)} />
  </>;
}

function SettingsForm({ value, onChange, disabled }: { value: StorageSettings; onChange: (value: StorageSettings) => void; disabled: boolean }) {
  const update = <K extends keyof StorageSettings>(key: K, next: StorageSettings[K]) => onChange({ ...value, [key]: next });
  return <div className="grid gap-3 sm:grid-cols-2"><NumberField label="物理上限（字节）" value={value.maxBytes} onChange={(next) => update("maxBytes", next)} disabled={disabled} /><NumberField label="缓存上限（字节）" value={value.cacheMaxBytes} onChange={(next) => update("cacheMaxBytes", next)} disabled={disabled} /><NumberField label="最低剩余磁盘（字节）" value={value.minFreeBytes} onChange={(next) => update("minFreeBytes", next)} disabled={disabled} /><div className="space-y-1.5"><Label htmlFor="storage-on-full">缓存满时</Label><Select id="storage-on-full" value={value.onFull} onChange={(event) => update("onFull", event.target.value as StorageSettings["onFull"])} disabled={disabled}><option value="bypass">bypass：绕过缓存直接回源</option><option value="reject">reject：返回 507</option></Select></div><NumberField label="高水位（0–1）" value={value.highWatermark} onChange={(next) => update("highWatermark", next)} disabled={disabled} step="0.01" /><NumberField label="低水位（0–1）" value={value.lowWatermark} onChange={(next) => update("lowWatermark", next)} disabled={disabled} step="0.01" /></div>;
}

function NumberField({ label, value, onChange, disabled, step = "1" }: { label: string; value: number; onChange: (value: number) => void; disabled: boolean; step?: string }) {
  return <div className="space-y-1.5"><Label>{label}</Label><Input type="number" min="0" step={step} value={value} onChange={(event) => onChange(Number(event.target.value))} disabled={disabled} /></div>;
}

function Waterline({ label, value, tone, detail }: { label: string; value: number; tone: "default" | "success" | "warning" | "destructive"; detail: string }) {
  return <div><div className="mb-2 flex items-center justify-between"><span className="text-sm font-medium">{label}</span><span className="text-xs tabular-nums text-muted-foreground">{value}%</span></div><Progress value={value} tone={tone} /><p className="mt-1.5 text-xs text-muted-foreground">{detail}</p></div>;
}

function SmallStat({ label, value }: { label: string; value: string }) {
  return <div className="rounded-md border bg-muted/20 p-2.5"><p className="text-[11px] text-muted-foreground">{label}</p><p className="mt-0.5 truncate text-sm font-medium tabular-nums">{value}</p></div>;
}

function JobRow({ job }: { job: Job }) {
  const result = parseResult(job.resultJson);
  return <tr><td className="px-3 py-2.5 font-mono text-xs">{job.id}</td><td className="px-3 py-2.5 text-xs">{job.type}</td><td className="px-3 py-2.5"><JobState state={job.state} /></td><td className="px-3 py-2.5 font-mono text-xs text-muted-foreground">{job.requestedBy || "system"}</td><td className="whitespace-nowrap px-3 py-2.5 text-xs text-muted-foreground">{formatTime(job.startedAt)}</td><td className="whitespace-nowrap px-3 py-2.5 text-xs text-muted-foreground">{formatTime(job.finishedAt)}</td><td className="max-w-56 px-3 py-2.5 text-xs text-muted-foreground">{job.error || summarizeResult(result)}</td></tr>;
}

function JobDetail({ job }: { job: Job }) {
  return <div className="mb-4 rounded-md border bg-muted/20 p-3"><div className="flex flex-wrap items-center gap-2"><span className="font-mono text-xs">{job.id}</span><JobState state={job.state} /><span className="ml-auto text-xs text-muted-foreground">{job.type}</span></div>{job.progressJson ? <p className="mt-2 text-xs text-muted-foreground">{parseProgress(job.progressJson)}</p> : null}{job.error ? <p className="mt-2 text-xs text-destructive">{job.error}</p> : null}</div>;
}

function JobState({ state }: { state: Job["state"] }) {
  const variant = state === "succeeded" ? "success" : state === "failed" ? "destructive" : "warning";
  return <Badge variant={variant}>{state === "queued" ? "排队中" : state === "running" ? "运行中" : state === "succeeded" ? "已完成" : "失败"}</Badge>;
}

function parseResult(raw?: string): Record<string, unknown> | null {
  if (!raw) return null;
  try { return JSON.parse(raw) as Record<string, unknown>; } catch { return null; }
}

function parseProgress(raw?: string): string {
  if (!raw) return "";
  try { const value = JSON.parse(raw) as { message?: string; at?: string }; return [value.message, value.at ? formatTime(value.at) : ""].filter(Boolean).join(" · "); } catch { return raw; }
}

function summarizeResult(result: Record<string, unknown> | null): string {
  if (!result) return "—";
  if ("orphanContents" in result) return `孤立内容 ${result.orphanContents}，孤儿字节 ${formatBytes(Number(result.orphanBytes || 0))}`;
  if ("checkedContents" in result) return `检查 ${result.checkedContents} 内容，问题 ${Array.isArray(result.issues) ? result.issues.length : 0}`;
  return "任务完成";
}
