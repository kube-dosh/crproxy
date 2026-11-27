import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, ChevronLeft, ChevronRight, Loader2, RefreshCw, RotateCcw, ScrollText } from "lucide-react";
import { toast } from "sonner";
import { api } from "@/api/client";
import { errorMessage } from "@/api/client";
import type { AuditPolicy } from "@/api/types";
import { ConfirmDialog } from "@/components/common/confirm-dialog";
import { StatusBadge } from "@/components/common/status-badge";
import { Toolbar } from "@/components/common/toolbar";
import { EmptyState, ErrorState, LoadingState } from "@/components/feedback/states";
import { PageHeader } from "@/components/layout/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select } from "@/components/ui/select";
import { useSessionQuery } from "@/features/auth/use-session";
import { actorTypeLabel, auditActionLabel, auditActionOptions, auditMessageLabel, auditTargetIdLabel, targetTypeLabel } from "@/lib/labels";
import { formatTime } from "@/lib/utils";

type CursorPage = { before?: number; label: string };

export function AuditPage() {
  const session = useSessionQuery();
  const admin = session.data?.user.role === "admin";
  const queryClient = useQueryClient();
  const [result, setResult] = useState("");
  const [action, setAction] = useState("");
  const [pages, setPages] = useState<CursorPage[]>([{ label: "最新" }]);
  const [settings, setSettings] = useState<AuditPolicy | null>(null);
  const [confirmReset, setConfirmReset] = useState(false);
  const cursor = pages[pages.length - 1]?.before;
  const audit = useQuery({ queryKey: ["audit", result, action, cursor], queryFn: () => api.audit({ before: cursor, result: result || undefined, action: action || undefined }) });
  const runtime = useQuery({ queryKey: ["runtime-config"], queryFn: api.runtimeSettings });
  useEffect(() => { if (runtime.data) setSettings(runtime.data.audit); }, [runtime.data]);
  const reset = (nextResult = result, nextAction = action) => { setResult(nextResult); setAction(nextAction); setPages([{ label: "最新" }]); };
  const save = useMutation({
    mutationFn: (value: AuditPolicy) => api.updateRuntimeSection("audit", runtime.data?.sections.audit.version ?? 0, value),
    onSuccess: (data) => {
      queryClient.setQueryData(["runtime-config"], data);
      setSettings(data.audit);
      toast.success("审计保留期已保存，后台清理任务立即采用新策略");
    },
    onError: async (error) => { toast.error(errorMessage(error)); await runtime.refetch(); },
  });
  const restore = useMutation({
    mutationFn: (version: number) => api.resetRuntimeSection("audit", version),
    onSuccess: (data) => {
      queryClient.setQueryData(["runtime-config"], data);
      setSettings(data.audit);
      setConfirmReset(false);
      toast.success("审计保留期已恢复系统默认");
    },
    onError: async (error) => { toast.error(errorMessage(error)); await runtime.refetch(); },
  });
  return <>
    <PageHeader title="审计日志" description="记录登录、账号、访问令牌、上游配置、缓存清理和镜像删改等关键操作；密码与登录凭证不会出现在日志中。" actions={<Button variant="outline" size="sm" onClick={() => void audit.refetch()}><RefreshCw className={`h-4 w-4 ${audit.isFetching ? "animate-spin" : ""}`} />刷新</Button>} />
    <Card className="mb-4"><CardHeader><div className="flex min-w-0 flex-wrap items-start justify-between gap-2"><div className="min-w-0"><div className="flex items-center gap-2"><ScrollText className="h-4 w-4 text-primary" /><CardTitle>审计保留期</CardTitle></div><CardDescription className="mt-1.5">系统会按保留期自动清理较早的审计记录；该配置仅管理员可修改。</CardDescription></div><div className="flex shrink-0 flex-wrap gap-1.5"><Badge variant={runtime.data?.sections.audit.source === "runtime" ? "default" : "outline"}>{runtime.data?.sections.audit.source === "runtime" ? "已自定义" : "系统默认"}</Badge><Badge variant="secondary">自动清理</Badge></div></div>{runtime.data?.sections.audit.updatedBy ? <p className="text-[11px] text-muted-foreground">最后修改：{runtime.data.sections.audit.updatedBy} · {formatTime(runtime.data.sections.audit.updatedAt)}</p> : null}</CardHeader><CardContent>{runtime.isLoading ? <LoadingState label="正在读取审计配置…" /> : runtime.error || !runtime.data || !settings ? <ErrorState error={runtime.error} onRetry={() => void runtime.refetch()} /> : <div className="flex min-w-0 flex-col gap-3 sm:flex-row sm:items-end"><div className="min-w-0 flex-1 space-y-1.5"><Label htmlFor="audit-retention">保留期</Label><Input id="audit-retention" value={settings.auditRetention} onChange={(event) => setSettings({ auditRetention: event.target.value })} placeholder="2160h" disabled={!admin || save.isPending} /><p className="text-xs text-muted-foreground">例如 `720h`（30 天）、`8760h`（365 天）。</p></div><div className="flex flex-wrap gap-2">{runtime.data.sections.audit.source === "runtime" ? <Button variant="ghost" onClick={() => setConfirmReset(true)} disabled={!admin || restore.isPending}><RotateCcw className="h-4 w-4" />恢复默认</Button> : null}<Button onClick={() => settings && save.mutate(settings)} disabled={!admin || save.isPending}>{save.isPending ? <Loader2 className="h-4 w-4 animate-spin" /> : <CheckCircle2 className="h-4 w-4" />}保存</Button></div></div>}{!admin ? <p className="mt-3 text-xs text-muted-foreground">运维账号可查看审计日志，但只有管理员可以修改保留期。</p> : null}</CardContent></Card>
    <Toolbar><div className="flex min-w-0 flex-1 flex-col gap-2 sm:flex-row"><Select className="sm:w-52" value={action} onChange={(event) => reset(result, event.target.value)}><option value="">全部操作</option>{auditActionOptions.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}</Select><Select className="sm:w-36" value={result} onChange={(event) => reset(event.target.value, action)}><option value="">全部结果</option><option value="success">成功</option><option value="failure">失败</option><option value="denied">拒绝</option><option value="accepted">已受理</option></Select></div><span className="self-center text-xs text-muted-foreground">{pages.map((page) => page.label).join(" / ")}</span></Toolbar>
    {audit.isLoading ? <LoadingState /> : audit.error || !audit.data ? <ErrorState error={audit.error} onRetry={() => void audit.refetch()} /> : audit.data.events.length === 0 ? <EmptyState title="没有审计事件" description="当前筛选条件下没有记录。" /> : <><div className="rounded-lg border bg-card shadow-panel"><div className="overflow-x-auto"><table className="w-full min-w-[900px] text-sm"><thead className="border-b bg-muted/40 text-left text-xs text-muted-foreground"><tr><th className="px-4 py-3 font-medium">时间</th><th className="px-3 py-3 font-medium">操作</th><th className="px-3 py-3 font-medium">结果</th><th className="px-3 py-3 font-medium">操作者</th><th className="px-3 py-3 font-medium">对象</th><th className="px-3 py-3 font-medium">来源 IP</th><th className="px-4 py-3 font-medium">说明</th></tr></thead><tbody className="divide-y">{audit.data.events.map((event) => <tr key={event.id} className="hover:bg-muted/25"><td className="whitespace-nowrap px-4 py-3 text-xs text-muted-foreground">{formatTime(event.at)}</td><td className="px-3 py-3 text-xs">{auditActionLabel(event.action)}</td><td className="px-3 py-3"><StatusBadge value={event.result} /></td><td className="px-3 py-3 text-xs"><p>{event.actorId || actorTypeLabel(event.actorType)}</p><p className="text-muted-foreground">{actorTypeLabel(event.actorType)}</p></td><td className="max-w-56 px-3 py-3 text-xs"><p>{targetTypeLabel(event.targetType)}</p><p className="truncate font-mono text-muted-foreground" title={auditTargetIdLabel(event.targetType, event.targetId)}>{auditTargetIdLabel(event.targetType, event.targetId)}</p></td><td className="px-3 py-3 font-mono text-xs">{event.ip || "—"}</td><td className="max-w-64 px-4 py-3 text-xs text-muted-foreground"><p className="line-clamp-2" title={event.message}>{auditMessageLabel(event.message) || "—"}</p><p className="truncate" title={event.userAgent}>{event.userAgent || ""}</p></td></tr>)}</tbody></table></div></div><div className="mt-3 flex items-center justify-between"><Button variant="outline" size="sm" disabled={pages.length <= 1} onClick={() => setPages((current) => current.slice(0, -1))}><ChevronLeft className="h-4 w-4" />上一页</Button><Button variant="outline" size="sm" disabled={!audit.data.nextBefore} onClick={() => { if (audit.data.nextBefore) setPages((current) => [...current, { before: audit.data.nextBefore, label: `第 ${current.length + 1} 页` }]); }}>下一页<ChevronRight className="h-4 w-4" /></Button></div></>}
    <ConfirmDialog open={confirmReset} onOpenChange={setConfirmReset} title="恢复审计保留期的默认值？" description="将撤销页面中保存的保留期，恢复系统默认值并立即生效。" confirmLabel="恢复默认" destructive={false} pending={restore.isPending} onConfirm={() => { if (runtime.data) restore.mutate(runtime.data.sections.audit.version); }} />
  </>;
}
