import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronLeft, ChevronRight, RefreshCw, Search } from "lucide-react";
import { api } from "@/api/client";
import { StatusBadge } from "@/components/common/status-badge";
import { Toolbar } from "@/components/common/toolbar";
import { EmptyState, ErrorState, LoadingState } from "@/components/feedback/states";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { formatTime } from "@/lib/utils";

type CursorPage = { before?: number; label: string };

export function AuditPage() {
  const [result, setResult] = useState("");
  const [actionInput, setActionInput] = useState("");
  const [action, setAction] = useState("");
  const [pages, setPages] = useState<CursorPage[]>([{ label: "最新" }]);
  const cursor = pages[pages.length - 1]?.before;
  const audit = useQuery({ queryKey: ["audit", result, action, cursor], queryFn: () => api.audit({ before: cursor, result: result || undefined, action: action || undefined }) });
  const reset = (nextResult = result, nextAction = action) => { setResult(nextResult); setAction(nextAction); setPages([{ label: "最新" }]); };
  return <>
    <PageHeader title="审计日志" description="记录登录、用户、Token、上游、缓存清理、GC、fsck 与内容删除操作；不记录密码、PAT、Session 或上游凭证。" actions={<Button variant="outline" size="sm" onClick={() => void audit.refetch()}><RefreshCw className={`h-4 w-4 ${audit.isFetching ? "animate-spin" : ""}`} />刷新</Button>} />
    <Toolbar><form className="flex min-w-0 flex-1 flex-col gap-2 sm:flex-row" onSubmit={(event) => { event.preventDefault(); reset(result, actionInput.trim()); }}><div className="relative min-w-0 flex-1 sm:max-w-sm"><Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" /><Input className="pl-9" value={actionInput} onChange={(event) => setActionInput(event.target.value)} placeholder="按 action 精确筛选，如 upstream.update" /></div><Select className="sm:w-36" value={result} onChange={(event) => reset(event.target.value, action)}><option value="">全部结果</option><option value="success">成功</option><option value="failure">失败</option><option value="denied">拒绝</option><option value="accepted">已受理</option></Select><Button type="submit" variant="outline">筛选</Button></form><span className="self-center text-xs text-muted-foreground">{pages.map((page) => page.label).join(" / ")}</span></Toolbar>
    {audit.isLoading ? <LoadingState /> : audit.error || !audit.data ? <ErrorState error={audit.error} onRetry={() => void audit.refetch()} /> : audit.data.events.length === 0 ? <EmptyState title="没有审计事件" description="当前筛选条件下没有记录。" /> : <><div className="rounded-lg border bg-card shadow-panel"><div className="overflow-x-auto"><table className="w-full min-w-[1000px] text-sm"><thead className="border-b bg-muted/40 text-left text-xs text-muted-foreground"><tr><th className="px-4 py-3 font-medium">时间</th><th className="px-3 py-3 font-medium">动作</th><th className="px-3 py-3 font-medium">结果</th><th className="px-3 py-3 font-medium">操作者</th><th className="px-3 py-3 font-medium">目标</th><th className="px-3 py-3 font-medium">来源 IP</th><th className="px-3 py-3 font-medium">Request ID</th><th className="px-4 py-3 font-medium">说明</th></tr></thead><tbody className="divide-y">{audit.data.events.map((event) => <tr key={event.id} className="hover:bg-muted/25"><td className="whitespace-nowrap px-4 py-3 text-xs text-muted-foreground">{formatTime(event.at)}</td><td className="px-3 py-3 font-mono text-xs">{event.action}</td><td className="px-3 py-3"><StatusBadge value={event.result} /></td><td className="px-3 py-3 text-xs"><p>{event.actorId || event.actorType}</p><p className="text-muted-foreground">{event.actorType}</p></td><td className="max-w-56 px-3 py-3 text-xs"><p>{event.targetType || "—"}</p><p className="truncate font-mono text-muted-foreground" title={event.targetId}>{event.targetId || "—"}</p></td><td className="px-3 py-3 font-mono text-xs">{event.ip || "—"}</td><td className="px-3 py-3 font-mono text-[10px] text-muted-foreground">{event.requestId || "—"}</td><td className="max-w-64 px-4 py-3 text-xs text-muted-foreground"><p className="line-clamp-2" title={event.message}>{event.message || "—"}</p><p className="truncate" title={event.userAgent}>{event.userAgent || ""}</p></td></tr>)}</tbody></table></div></div><div className="mt-3 flex items-center justify-between"><Button variant="outline" size="sm" disabled={pages.length <= 1} onClick={() => setPages((current) => current.slice(0, -1))}><ChevronLeft className="h-4 w-4" />上一页</Button><Button variant="outline" size="sm" disabled={!audit.data.nextBefore} onClick={() => { if (audit.data.nextBefore) setPages((current) => [...current, { before: audit.data.nextBefore, label: `第 ${current.length + 1} 页` }]); }}>下一页<ChevronRight className="h-4 w-4" /></Button></div></>}
  </>;
}
