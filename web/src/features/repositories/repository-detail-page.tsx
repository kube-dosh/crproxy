import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Boxes, FileStack, Layers3, RefreshCw, Tags, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { api, errorMessage } from "@/api/client";
import type { ContentEntry, RepositorySummary, TagSummary } from "@/api/types";
import { ConfirmDialog } from "@/components/common/confirm-dialog";
import { CopyButton } from "@/components/common/copy-button";
import { StatusBadge } from "@/components/common/status-badge";
import { EmptyState, ErrorState, LoadingState } from "@/components/feedback/states";
import { PageHeader } from "@/components/layout/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useSessionQuery } from "@/features/auth/use-session";
import { Link } from "@/lib/router";
import { formatBytes, formatTime, shortDigest } from "@/lib/utils";

type Tab = "tags" | "manifests" | "blobs" | "referrers";
type DeleteTarget = { kind: "repository" | "tag" | "manifest"; value: string; label: string };

const tabs: { id: Tab; label: string }[] = [
  { id: "tags", label: "Tags" }, { id: "manifests", label: "Manifests" }, { id: "blobs", label: "Blobs" }, { id: "referrers", label: "Referrers" },
];

export function RepositoryDetailPage({ id }: { id: number }) {
  const session = useSessionQuery();
  const manage = session.data?.user.role === "admin" || session.data?.user.role === "operator";
  const queryClient = useQueryClient();
  const [tab, setTab] = useState<Tab>("tags");
  const [target, setTarget] = useState<DeleteTarget | null>(null);
  const repository = useQuery({ queryKey: ["repository", id], queryFn: () => api.repository(id) });
  const upstreams = useQuery({ queryKey: ["upstreams"], queryFn: api.upstreams });
  const system = useQuery({ queryKey: ["system", "info"], queryFn: api.systemInfo, staleTime: 60_000 });
  const tags = useQuery({ queryKey: ["repository", id, "tags"], queryFn: () => api.repositoryTags(id), enabled: tab === "tags" });
  const manifests = useQuery({ queryKey: ["repository", id, "manifests"], queryFn: () => api.repositoryManifests(id, "?limit=200"), enabled: tab === "manifests" });
  const blobs = useQuery({ queryKey: ["repository", id, "blobs"], queryFn: () => api.repositoryBlobs(id, "?limit=200"), enabled: tab === "blobs" });
  const referrers = useQuery({ queryKey: ["repository", id, "referrers"], queryFn: () => api.repositoryReferrers(id), enabled: tab === "referrers" });
  const mutate = useMutation({
    mutationFn: async (item: DeleteTarget) => {
      const repo = repository.data as RepositorySummary;
      if (item.kind === "repository") return repo.kind === "local" ? api.deleteRepository(id) : api.evictRepository(id);
      if (item.kind === "tag") return api.deleteTag(id, item.value);
      return api.deleteManifest(id, item.value);
    },
    onSuccess: async () => { toast.success("删除任务已完成"); setTarget(null); await queryClient.invalidateQueries({ queryKey: ["repository", id] }); await queryClient.invalidateQueries({ queryKey: ["repositories"] }); await queryClient.invalidateQueries({ queryKey: ["overview"] }); await queryClient.invalidateQueries({ queryKey: ["storage"] }); },
    onError: (error) => toast.error(errorMessage(error)),
  });
  if (repository.isLoading) return <LoadingState label="正在读取仓库…" />;
  if (repository.error || !repository.data) return <ErrorState error={repository.error} onRetry={() => void repository.refetch()} />;
  const repo = repository.data;
  const upstream = upstreams.data?.upstreams.find((item) => item.id === repo.upstreamId);
  const host = (system.data?.publicURL || window.location.origin).replace(/^https?:\/\//, "");
  const path = repo.kind === "local" ? repo.name : `${system.data?.proxyPathPrefix ?? "r"}/${upstream?.routePrefix || repo.namespace}/${repo.name}`;
  const pull = `podman pull${(system.data?.publicURL || window.location.origin).startsWith("http://") ? " --tls-verify=false" : ""} ${host}/${path}:latest`;
  return <>
    <PageHeader breadcrumb={<Link href={repo.kind === "local" ? "/local" : "/cache"} className="inline-flex items-center gap-1 text-primary hover:underline"><ArrowLeft className="h-3.5 w-3.5" />{repo.kind === "local" ? "本地镜像" : "代理缓存"}</Link>} title={repo.name} description={`${repo.kind === "local" ? "本地仓库" : "代理缓存仓库"} · namespace ${repo.namespace}`} actions={<><Button variant="outline" size="sm" onClick={() => void queryClient.invalidateQueries({ queryKey: ["repository", id] })}><RefreshCw className="h-4 w-4" />刷新</Button>{manage ? <Button variant={repo.kind === "local" ? "destructive" : "outline"} size="sm" onClick={() => setTarget({ kind: "repository", value: String(repo.id), label: repo.name })}><Trash2 className="h-4 w-4" />{repo.kind === "local" ? "删除仓库" : "清理缓存"}</Button> : null}</>} />
    <div className="mb-4 grid gap-3 sm:grid-cols-2 xl:grid-cols-4"><Stat icon={Tags} label="Tags" value={repo.tagCount} /><Stat icon={FileStack} label="Manifests" value={repo.manifestCount} /><Stat icon={Layers3} label="Blobs" value={repo.blobCount} /><Stat icon={Boxes} label="逻辑大小" value={formatBytes(repo.size)} /></div>
    <Card className="mb-4"><CardHeader className="pb-3"><CardTitle className="text-base">Podman 命令</CardTitle></CardHeader><CardContent><div className="flex items-start gap-2 rounded-md border bg-muted/20 p-3"><code className="min-w-0 flex-1 break-all font-mono text-xs leading-5">{pull}</code><CopyButton value={pull} /></div>{repo.kind === "cache" ? <p className="mt-2 text-xs text-muted-foreground">代理路由只读；podman push 返回 405。标签 TTL {upstream ? `${upstream.tagTtlSeconds}s` : "—"}，缓存清理不会删除上游镜像。</p> : <p className="mt-2 text-xs text-muted-foreground">本地仓库支持 push、tag 删除与 manifest 删除。删除引用后，未再被引用的 CAS 内容由后台 GC 回收。</p>}</CardContent></Card>
    <div className="mb-3 flex gap-1 overflow-x-auto rounded-lg border bg-card p-1">{tabs.map((item) => <button key={item.id} type="button" onClick={() => setTab(item.id)} className={`rounded-md px-4 py-2 text-sm font-medium transition-colors ${tab === item.id ? "bg-primary/10 text-primary" : "text-muted-foreground hover:bg-muted"}`}>{item.label}</button>)}</div>
    <Card><CardContent className="p-0">
      {tab === "tags" ? <TagsPanel query={tags} manage={manage} kind={repo.kind} onDelete={(tag) => setTarget({ kind: "tag", value: tag.tag, label: tag.tag })} /> : null}
      {tab === "manifests" ? <ContentsPanel query={manifests} manage={manage && repo.kind === "local"} onDelete={(entry) => setTarget({ kind: "manifest", value: entry.digest, label: shortDigest(entry.digest) })} /> : null}
      {tab === "blobs" ? <ContentsPanel query={blobs} manage={false} onDelete={() => undefined} /> : null}
      {tab === "referrers" ? <ReferrersPanel query={referrers} /> : null}
    </CardContent></Card>
    <ConfirmDialog open={target !== null} onOpenChange={(open) => { if (!open) setTarget(null); }} title={target?.kind === "repository" ? (repo.kind === "local" ? "删除本地仓库？" : "清理代理缓存？") : target?.kind === "tag" ? "删除 tag？" : "删除 manifest？"} description={target?.kind === "repository" ? (repo.kind === "local" ? "将移除仓库内所有 tag、manifest 与 blob 引用；共享 CAS 内容不会被直接删除。" : "将移除该仓库的全部缓存内容，上游不受影响。") : target?.kind === "tag" ? <>删除 tag <span className="font-mono">{target.label}</span>，manifest 仍可按 digest 拉取。</> : <>删除 manifest <span className="font-mono">{target?.label}</span> 及其指向的 tag。</>} confirmLabel={repo.kind === "cache" && target?.kind === "repository" ? "清理缓存" : "删除"} pending={mutate.isPending} onConfirm={() => { if (target) mutate.mutate(target); }} />
  </>;
}

function Stat({ icon: Icon, label, value }: { icon: typeof Tags; label: string; value: string | number }) {
  return <div className="flex items-center gap-3 rounded-lg border bg-card p-4 shadow-panel"><span className="grid h-9 w-9 place-items-center rounded-md bg-primary/10 text-primary"><Icon className="h-4 w-4" /></span><div><p className="text-xs text-muted-foreground">{label}</p><p className="font-semibold tabular-nums">{value}</p></div></div>;
}

function TagsPanel({ query, manage, kind, onDelete }: { query: ReturnType<typeof useQuery<{ total: number; tags: TagSummary[] }>>; manage: boolean; kind: string; onDelete: (tag: TagSummary) => void }) {
  if (query.isLoading) return <LoadingState />;
  if (query.error || !query.data) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  if (query.data.tags.length === 0) return <EmptyState title="没有 tag" description="该仓库当前没有可用的标签。" />;
  return <div className="overflow-x-auto"><table className="w-full min-w-[760px] text-sm"><thead className="border-b bg-muted/40 text-left text-xs text-muted-foreground"><tr><th className="px-4 py-3 font-medium">Tag</th><th className="px-3 py-3 font-medium">Digest</th><th className="px-3 py-3 font-medium">Media type</th><th className="px-3 py-3 text-right font-medium">大小</th><th className="px-3 py-3 font-medium">检查时间</th><th className="px-3 py-3 font-medium">状态</th>{manage && kind === "local" ? <th className="px-4 py-3 text-right font-medium">操作</th> : null}</tr></thead><tbody className="divide-y">{query.data.tags.map((tag) => <tr key={tag.tag} className="hover:bg-muted/25"><td className="px-4 py-3 font-medium">{tag.tag}</td><td className="max-w-64 px-3 py-3"><div className="flex items-center gap-1"><code className="truncate font-mono text-xs" title={tag.digest}>{shortDigest(tag.digest)}</code><CopyButton value={tag.digest} label="" className="h-7 px-1" /></div></td><td className="max-w-56 truncate px-3 py-3 text-xs text-muted-foreground" title={tag.mediaType}>{tag.mediaType}</td><td className="px-3 py-3 text-right tabular-nums">{formatBytes(tag.size)}</td><td className="whitespace-nowrap px-3 py-3 text-xs text-muted-foreground">{formatTime(tag.checkedAt)}</td><td className="px-3 py-3">{tag.stale ? <Badge variant="warning">已过期</Badge> : <Badge variant="success">有效</Badge>}</td>{manage && kind === "local" ? <td className="px-4 py-3 text-right"><Button variant="ghost" size="icon-sm" className="text-destructive" onClick={() => onDelete(tag)} aria-label={`删除 tag ${tag.tag}`}><Trash2 className="h-3.5 w-3.5" /></Button></td> : null}</tr>)}</tbody></table></div>;
}

function ContentsPanel({ query, manage, onDelete }: { query: ReturnType<typeof useQuery<{ total: number; entries: ContentEntry[] }>>; manage: boolean; onDelete: (entry: ContentEntry) => void }) {
  if (query.isLoading) return <LoadingState />;
  if (query.error || !query.data) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  if (query.data.entries.length === 0) return <EmptyState title="没有内容" description="当前分类下没有 manifest 或 blob。" />;
  return <div className="overflow-x-auto"><table className="w-full min-w-[860px] text-sm"><thead className="border-b bg-muted/40 text-left text-xs text-muted-foreground"><tr><th className="px-4 py-3 font-medium">Digest</th><th className="px-3 py-3 font-medium">类型</th><th className="px-3 py-3 text-right font-medium">大小</th><th className="px-3 py-3 text-right font-medium">引用</th><th className="px-3 py-3 font-medium">最近使用</th><th className="px-3 py-3 font-medium">状态</th>{manage ? <th className="px-4 py-3 text-right font-medium">操作</th> : null}</tr></thead><tbody className="divide-y">{query.data.entries.map((entry) => <tr key={`${entry.kind}-${entry.digest}`} className="hover:bg-muted/25"><td className="max-w-72 px-4 py-3"><div className="flex items-center gap-1"><code className="truncate font-mono text-xs" title={entry.digest}>{shortDigest(entry.digest)}</code><CopyButton value={entry.digest} label="" className="h-7 px-1" /></div></td><td className="px-3 py-3"><Badge variant="secondary">{entry.kind}</Badge><span className="ml-2 max-w-48 truncate text-xs text-muted-foreground" title={entry.mediaType}>{entry.mediaType}</span></td><td className="px-3 py-3 text-right tabular-nums">{formatBytes(entry.size)}</td><td className="px-3 py-3 text-right tabular-nums" title={`local=${entry.localRefs} cache=${entry.cacheRefs}`}>{entry.refCount}</td><td className="whitespace-nowrap px-3 py-3 text-xs text-muted-foreground">{formatTime(entry.lastUsedAt)}</td><td className="px-3 py-3"><StatusBadge value={entry.state} /></td>{manage ? <td className="px-4 py-3 text-right"><Button variant="ghost" size="icon-sm" className="text-destructive" onClick={() => onDelete(entry)} aria-label="删除 manifest"><Trash2 className="h-3.5 w-3.5" /></Button></td> : null}</tr>)}</tbody></table></div>;
}

function ReferrersPanel({ query }: { query: ReturnType<typeof useQuery<{ total: number; referrers: import("@/api/types").RepositoryReferrer[] }>> }) {
  if (query.isLoading) return <LoadingState />;
  if (query.error || !query.data) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  if (query.data.referrers.length === 0) return <EmptyState title="没有 referrer" description="该仓库没有已记录的 OCI artifact referrer。" />;
  return <div className="overflow-x-auto"><table className="w-full min-w-[720px] text-sm"><thead className="border-b bg-muted/40 text-left text-xs text-muted-foreground"><tr><th className="px-4 py-3 font-medium">Subject</th><th className="px-3 py-3 font-medium">Referrer digest</th><th className="px-3 py-3 font-medium">Artifact type</th><th className="px-3 py-3 text-right font-medium">大小</th><th className="px-4 py-3 font-medium">创建时间</th></tr></thead><tbody className="divide-y">{query.data.referrers.map((item) => <tr key={item.digest}><td className="max-w-56 px-4 py-3"><code className="block truncate font-mono text-xs" title={item.subjectDigest}>{shortDigest(item.subjectDigest)}</code></td><td className="max-w-56 px-3 py-3"><code className="block truncate font-mono text-xs" title={item.digest}>{shortDigest(item.digest)}</code></td><td className="px-3 py-3 text-xs">{item.artifactType || item.mediaType}</td><td className="px-3 py-3 text-right tabular-nums">{formatBytes(item.size)}</td><td className="whitespace-nowrap px-4 py-3 text-xs text-muted-foreground">{formatTime(item.createdAt)}</td></tr>)}</tbody></table></div>;
}
