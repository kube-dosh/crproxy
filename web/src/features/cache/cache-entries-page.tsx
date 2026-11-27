import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Search, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { api, errorMessage } from "@/api/client";
import type { ContentEntry } from "@/api/types";
import { ConfirmDialog } from "@/components/common/confirm-dialog";
import { CopyButton } from "@/components/common/copy-button";
import { Pagination } from "@/components/common/pagination";
import { StatusBadge } from "@/components/common/status-badge";
import { Toolbar } from "@/components/common/toolbar";
import { EmptyState, ErrorState, LoadingState } from "@/components/feedback/states";
import { PageHeader } from "@/components/layout/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { useSessionQuery } from "@/features/auth/use-session";
import { contentKindLabel, mediaTypeLabel } from "@/lib/labels";
import { formatBytes, formatTime, shortDigest } from "@/lib/utils";

const pageSize = 50;

export function CacheEntriesPage() {
  const session = useSessionQuery();
  const manage = session.data?.user.role === "admin" || session.data?.user.role === "operator";
  const queryClient = useQueryClient();
  const [upstream, setUpstream] = useState("");
  const [kind, setKind] = useState("");
  const [search, setSearch] = useState("");
  const [applied, setApplied] = useState("");
  const [offset, setOffset] = useState(0);
  const [selected, setSelected] = useState<string[]>([]);
  const [confirming, setConfirming] = useState(false);
  const upstreams = useQuery({ queryKey: ["upstreams"], queryFn: api.upstreams });
  const entries = useQuery({ queryKey: ["cache", "entries", upstream, kind, applied, offset], queryFn: () => api.cacheEntries({ upstreamId: upstream || undefined, kind: kind || undefined, q: applied || undefined, limit: pageSize, offset }) });
  const bulkEvict = useMutation({
    mutationFn: (digests: string[]) => api.evictDigests(digests),
    onSuccess: async () => { toast.success("缓存清理任务已提交"); setConfirming(false); setSelected([]); await queryClient.invalidateQueries({ queryKey: ["cache"] }); await queryClient.invalidateQueries({ queryKey: ["repositories"] }); await queryClient.invalidateQueries({ queryKey: ["storage"] }); },
    onError: (error) => toast.error(errorMessage(error)),
  });
  const toggle = (digest: string) => setSelected((current) => current.includes(digest) ? current.filter((item) => item !== digest) : [...current, digest]);
  const allSelected = entries.data?.entries.length ? entries.data.entries.every((entry) => selected.includes(entry.digest)) : false;
  return <>
    <PageHeader title="缓存条目" description="按镜像摘要查看代理缓存内容；清理后会从所有代理缓存中移除对应内容，本地镜像数据不受影响。" actions={manage ? <Button variant="destructive" disabled={selected.length === 0} onClick={() => setConfirming(true)}><Trash2 className="h-4 w-4" />清理所选 {selected.length > 0 ? `(${selected.length})` : ""}</Button> : null} />
    <Toolbar><form className="flex min-w-0 flex-1 flex-col gap-2 sm:flex-row" onSubmit={(event) => { event.preventDefault(); setApplied(search.trim()); setOffset(0); }}><div className="relative min-w-0 flex-1 sm:max-w-xs"><Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" /><Input className="pl-9" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="镜像摘要" /></div><Select className="sm:w-40" value={kind} onChange={(event) => { setKind(event.target.value); setOffset(0); }}><option value="">全部类型</option><option value="manifest">镜像清单</option><option value="blob">镜像层</option></Select><Select className="sm:w-44" value={upstream} onChange={(event) => { setUpstream(event.target.value); setOffset(0); }}><option value="">全部上游</option>{upstreams.data?.upstreams.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</Select><Button type="submit" variant="outline">筛选</Button></form></Toolbar>
    {entries.isLoading ? <LoadingState /> : entries.error || !entries.data ? <ErrorState error={entries.error} onRetry={() => void entries.refetch()} /> : entries.data.entries.length === 0 ? <EmptyState title="没有缓存条目" description="通过代理拉取镜像后，缓存内容会显示在这里。" /> : <div className="rounded-lg border bg-card shadow-panel"><div className="overflow-x-auto"><table className="w-full min-w-[960px] text-sm"><thead className="border-b bg-muted/40 text-left text-xs text-muted-foreground"><tr><th className="w-10 px-4 py-3"><Checkbox checked={allSelected} onChange={(event) => setSelected(event.target.checked ? entries.data.entries.map((entry) => entry.digest) : [])} aria-label="选择当前页全部" /></th><th className="px-3 py-3 font-medium">镜像摘要</th><th className="px-3 py-3 font-medium">类型</th><th className="px-3 py-3 text-right font-medium">大小</th><th className="px-3 py-3 text-right font-medium">引用</th><th className="px-3 py-3 text-right font-medium">访问次数</th><th className="px-3 py-3 font-medium">最近使用</th><th className="px-3 py-3 font-medium">状态</th></tr></thead><tbody className="divide-y">{entries.data.entries.map((entry) => <EntryRow key={`${entry.kind}-${entry.digest}`} entry={entry} selected={selected.includes(entry.digest)} onToggle={() => toggle(entry.digest)} />)}</tbody></table></div><div className="p-3"><Pagination total={entries.data.total} limit={pageSize} offset={offset} onChange={setOffset} /></div></div>}
    <ConfirmDialog open={confirming} onOpenChange={setConfirming} title="清理所选缓存内容？" description={<>将从代理缓存中移除所选 {selected.length} 项内容；若相同内容仍被本地镜像使用，则不会影响本地数据。</>} confirmLabel="提交清理任务" pending={bulkEvict.isPending} onConfirm={() => bulkEvict.mutate(selected)} />
  </>;
}

function EntryRow({ entry, selected, onToggle }: { entry: ContentEntry; selected: boolean; onToggle: () => void }) {
  return <tr className="hover:bg-muted/25"><td className="px-4 py-3"><Checkbox checked={selected} onChange={onToggle} aria-label={`选择 ${entry.digest}`} /></td><td className="max-w-80 px-3 py-3"><div className="flex items-center gap-1"><code className="truncate font-mono text-xs" title={entry.digest}>{shortDigest(entry.digest)}</code><CopyButton value={entry.digest} label="" className="h-7 px-1" /></div></td><td className="px-3 py-3"><Badge variant="secondary">{contentKindLabel(entry.kind)}</Badge><p className="mt-1 max-w-48 truncate text-xs text-muted-foreground">{mediaTypeLabel(entry.mediaType)}</p></td><td className="px-3 py-3 text-right tabular-nums">{formatBytes(entry.size)}</td><td className="px-3 py-3 text-right tabular-nums">{entry.refCount}</td><td className="px-3 py-3 text-right tabular-nums">{entry.accessCount}</td><td className="whitespace-nowrap px-3 py-3 text-xs text-muted-foreground">{formatTime(entry.lastUsedAt)}</td><td className="px-3 py-3"><StatusBadge value={entry.state} /></td></tr>;
}
