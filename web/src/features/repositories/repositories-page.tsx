import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { RefreshCw, Search, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { api, errorMessage } from "@/api/client";
import type { RepositorySummary } from "@/api/types";
import { ConfirmDialog } from "@/components/common/confirm-dialog";
import { Pagination } from "@/components/common/pagination";
import { Toolbar } from "@/components/common/toolbar";
import { EmptyState, ErrorState, LoadingState } from "@/components/feedback/states";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { useSessionQuery } from "@/features/auth/use-session";
import { Link } from "@/lib/router";
import { formatBytes, formatTime } from "@/lib/utils";

const pageSize = 25;

export function RepositoriesPage({ kind }: { kind: "local" | "cache" }) {
  const session = useSessionQuery();
  const manage = session.data?.user.role === "admin" || session.data?.user.role === "operator";
  const queryClient = useQueryClient();
  const [search, setSearch] = useState("");
  const [applied, setApplied] = useState("");
  const [offset, setOffset] = useState(0);
  const [upstream, setUpstream] = useState("");
  const [pendingDelete, setPendingDelete] = useState<RepositorySummary | null>(null);
  const upstreams = useQuery({ queryKey: ["upstreams"], queryFn: api.upstreams, enabled: kind === "cache" });
  const repositories = useQuery({
    queryKey: ["repositories", kind, applied, upstream, offset],
    queryFn: () => api.repositories({ kind, q: applied, upstreamId: upstream || undefined, limit: pageSize, offset }),
  });
  const remove = useMutation({
    mutationFn: async (repo: RepositorySummary) => kind === "local" ? api.deleteRepository(repo.id) : api.evictRepository(repo.id),
    onSuccess: async () => { toast.success(kind === "local" ? "仓库已删除，未使用数据将在后续空间回收中清理" : "缓存清理任务已提交"); setPendingDelete(null); await queryClient.invalidateQueries({ queryKey: ["repositories"] }); await queryClient.invalidateQueries({ queryKey: ["storage"] }); await queryClient.invalidateQueries({ queryKey: ["overview"] }); },
    onError: (error) => toast.error(errorMessage(error)),
  });
  const resetPage = () => setOffset(0);
  const title = kind === "local" ? "本地镜像" : "代理缓存";
  return <>
    <PageHeader title={title} description={kind === "local" ? "管理保存在当前实例中的镜像仓库，支持推送、拉取和删除。" : "从多个上游按需缓存镜像；代理仓库只读，清理缓存不会影响上游镜像。"} />
    <Toolbar><form className="flex min-w-0 flex-1 flex-col gap-2 sm:flex-row" onSubmit={(event) => { event.preventDefault(); setApplied(search.trim()); resetPage(); }}><div className="relative min-w-0 flex-1 sm:max-w-sm"><Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" /><Input className="pl-9" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="搜索仓库名" /></div>{kind === "cache" ? <Select className="sm:w-48" value={upstream} onChange={(event) => { setUpstream(event.target.value); resetPage(); }}><option value="">全部上游</option>{upstreams.data?.upstreams.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</Select> : null}<Button type="submit" variant="outline">搜索</Button></form><Button variant="ghost" size="icon" onClick={() => void repositories.refetch()} aria-label="刷新"><RefreshCw className={`h-4 w-4 ${repositories.isFetching ? "animate-spin" : ""}`} /></Button></Toolbar>
    {repositories.isLoading ? <LoadingState /> : repositories.error || !repositories.data ? <ErrorState error={repositories.error} onRetry={() => void repositories.refetch()} /> : repositories.data.repositories.length === 0 ? <EmptyState title="没有匹配的仓库" description={kind === "local" ? "推送镜像后，本地仓库会出现在这里。" : "通过代理拉取镜像后，镜像会按需缓存到这里。"} /> : <div className="rounded-lg border bg-card shadow-panel"><div className="overflow-x-auto"><table className="w-full min-w-[820px] text-sm"><thead className="border-b bg-muted/40 text-left text-xs text-muted-foreground"><tr><th className="px-4 py-3 font-medium">仓库</th>{kind === "cache" ? <th className="px-3 py-3 font-medium">上游</th> : null}<th className="px-3 py-3 text-right font-medium">标签</th><th className="px-3 py-3 text-right font-medium">镜像清单</th><th className="px-3 py-3 text-right font-medium">镜像层</th><th className="px-3 py-3 text-right font-medium">大小</th><th className="px-3 py-3 font-medium">最近访问</th><th className="px-4 py-3 text-right font-medium">操作</th></tr></thead><tbody className="divide-y">{repositories.data.repositories.map((repo) => <tr key={repo.id} className="hover:bg-muted/25"><td className="max-w-xs px-4 py-3"><Link href={`/repositories/${repo.id}`} className="block truncate font-medium text-primary hover:underline">{repo.name}</Link></td>{kind === "cache" ? <td className="px-3 py-3"><span className="rounded bg-muted px-2 py-0.5 text-xs">{upstreams.data?.upstreams.find((item) => item.id === repo.upstreamId)?.name || "—"}</span></td> : null}<td className="px-3 py-3 text-right tabular-nums">{repo.tagCount}</td><td className="px-3 py-3 text-right tabular-nums">{repo.manifestCount}</td><td className="px-3 py-3 text-right tabular-nums">{repo.blobCount}</td><td className="px-3 py-3 text-right tabular-nums">{formatBytes(repo.size)}</td><td className="whitespace-nowrap px-3 py-3 text-xs text-muted-foreground">{formatTime(repo.lastSeenAt || repo.createdAt)}</td><td className="px-4 py-3 text-right"><div className="flex justify-end gap-1"><Button variant="ghost" size="sm" asChild><Link href={`/repositories/${repo.id}`}>详情</Link></Button>{manage ? <Button variant="ghost" size="icon-sm" className="text-destructive" onClick={() => setPendingDelete(repo)} aria-label={kind === "local" ? "删除仓库" : "清理缓存"}><Trash2 className="h-3.5 w-3.5" /></Button> : null}</div></td></tr>)}</tbody></table></div><div className="p-3"><Pagination total={repositories.data.total} limit={pageSize} offset={offset} onChange={setOffset} /></div></div>}
    <ConfirmDialog open={pendingDelete !== null} onOpenChange={(open) => { if (!open) setPendingDelete(null); }} title={kind === "local" ? "删除本地仓库？" : "清理代理缓存？"} description={kind === "local" ? <>将删除 <span className="font-mono">{pendingDelete?.name}</span> 的标签、镜像清单与镜像层引用；仍被其他仓库使用的数据不会被删除。</> : <>将清理 <span className="font-mono">{pendingDelete?.name}</span> 的全部缓存内容。上游镜像不受影响，下次拉取时会重新回源。</>} confirmLabel={kind === "local" ? "删除" : "清理缓存"} pending={remove.isPending} onConfirm={() => { if (pendingDelete) remove.mutate(pendingDelete); }} />
  </>;
}

export function LocalRepositoriesPage() { return <RepositoriesPage kind="local" />; }
export function CacheRepositoriesPage() { return <RepositoriesPage kind="cache" />; }
