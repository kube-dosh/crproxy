import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, Loader2, RefreshCw, RotateCcw, Trash2, UploadCloud } from "lucide-react";
import { toast } from "sonner";
import { api, errorMessage } from "@/api/client";
import type { Upload, UploadsPolicy } from "@/api/types";
import { ConfirmDialog } from "@/components/common/confirm-dialog";
import { StatusBadge } from "@/components/common/status-badge";
import { EmptyState, ErrorState, LoadingState } from "@/components/feedback/states";
import { PageHeader } from "@/components/layout/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useSessionQuery } from "@/features/auth/use-session";
import { formatBytes, formatTime } from "@/lib/utils";

export function UploadsPage() {
  const session = useSessionQuery();
  const manage = session.data?.user.role === "admin" || session.data?.user.role === "operator";
  const queryClient = useQueryClient();
  const [activeOnly, setActiveOnly] = useState(true);
  const [pending, setPending] = useState<Upload | null>(null);
  const [settings, setSettings] = useState<UploadsPolicy | null>(null);
  const [confirmReset, setConfirmReset] = useState(false);
  const uploads = useQuery({ queryKey: ["uploads", activeOnly], queryFn: () => api.uploads(activeOnly), refetchInterval: 10_000 });
  const runtime = useQuery({ queryKey: ["runtime-config"], queryFn: api.runtimeSettings });
  useEffect(() => { if (runtime.data) setSettings(runtime.data.uploads); }, [runtime.data]);
  const cancel = useMutation({ mutationFn: (upload: Upload) => api.deleteUpload(upload.id), onSuccess: async () => { toast.success("上传会话已取消"); setPending(null); await queryClient.invalidateQueries({ queryKey: ["uploads"] }); await queryClient.invalidateQueries({ queryKey: ["storage"] }); }, onError: (error) => toast.error(errorMessage(error)) });
  const save = useMutation({
    mutationFn: (value: UploadsPolicy) => api.updateRuntimeSection("uploads", runtime.data?.sections.uploads.version ?? 0, value),
    onSuccess: (data) => {
      queryClient.setQueryData(["runtime-config"], data);
      setSettings(data.uploads);
      toast.success("上传有效期已保存，新建上传立即使用新设置");
    },
    onError: async (error) => { toast.error(errorMessage(error)); await runtime.refetch(); },
  });
  const reset = useMutation({
    mutationFn: (version: number) => api.resetRuntimeSection("uploads", version),
    onSuccess: (data) => {
      queryClient.setQueryData(["runtime-config"], data);
      setSettings(data.uploads);
      setConfirmReset(false);
      toast.success("上传有效期已恢复系统默认");
    },
    onError: async (error) => { toast.error(errorMessage(error)); await runtime.refetch(); },
  });
  return <>
    <PageHeader title="上传会话" description="查看和管理通过 Podman 推送镜像时产生的上传会话。取消会话会删除未完成的上传数据。" actions={<div className="flex gap-2"><Button variant="outline" size="sm" onClick={() => setActiveOnly((value) => !value)}>{activeOnly ? "显示全部" : "仅显示活动"}</Button><Button variant="outline" size="sm" onClick={() => void uploads.refetch()}><RefreshCw className={`h-4 w-4 ${uploads.isFetching ? "animate-spin" : ""}`} />刷新</Button></div>} />
    <Card className="mb-4"><CardHeader><div className="flex min-w-0 flex-wrap items-start justify-between gap-2"><div className="min-w-0"><div className="flex items-center gap-2"><UploadCloud className="h-4 w-4 text-primary" /><CardTitle>上传有效期</CardTitle></div><CardDescription className="mt-1.5">仅影响新建上传会话；过期上传会被自动清理。修改无需重启。</CardDescription></div><div className="flex shrink-0 flex-wrap gap-1.5"><Badge variant={runtime.data?.sections.uploads.source === "runtime" ? "default" : "outline"}>{runtime.data?.sections.uploads.source === "runtime" ? "已自定义" : "系统默认"}</Badge><Badge variant="secondary">新建上传生效</Badge></div></div>{runtime.data?.sections.uploads.updatedBy ? <p className="text-[11px] text-muted-foreground">最后修改：{runtime.data.sections.uploads.updatedBy} · {formatTime(runtime.data.sections.uploads.updatedAt)}</p> : null}</CardHeader><CardContent>{runtime.isLoading ? <LoadingState label="正在读取上传配置…" /> : runtime.error || !runtime.data || !settings ? <ErrorState error={runtime.error} onRetry={() => void runtime.refetch()} /> : <div className="flex min-w-0 flex-col gap-3 sm:flex-row sm:items-end"><div className="min-w-0 flex-1 space-y-1.5"><Label htmlFor="uploads-ttl">上传有效期</Label><Input id="uploads-ttl" value={settings.uploadsTtl} onChange={(event) => setSettings({ uploadsTtl: event.target.value })} placeholder="24h" disabled={!manage || save.isPending} /></div><div className="flex flex-wrap gap-2">{runtime.data.sections.uploads.source === "runtime" ? <Button variant="ghost" onClick={() => setConfirmReset(true)} disabled={!manage || reset.isPending}><RotateCcw className="h-4 w-4" />恢复默认</Button> : null}<Button onClick={() => settings && save.mutate(settings)} disabled={!manage || save.isPending}>{save.isPending ? <Loader2 className="h-4 w-4 animate-spin" /> : <CheckCircle2 className="h-4 w-4" />}保存</Button></div></div>}</CardContent></Card>
    <Card><CardContent className="p-0">{uploads.isLoading ? <LoadingState /> : uploads.error || !uploads.data ? <ErrorState error={uploads.error} onRetry={() => void uploads.refetch()} /> : uploads.data.uploads.length === 0 ? <EmptyState title={activeOnly ? "没有活动上传" : "没有上传会话"} description="Podman push 进行中时可在这里观察和取消上传。" /> : <div className="overflow-x-auto"><table className="w-full min-w-[900px] text-sm"><thead className="border-b bg-muted/40 text-left text-xs text-muted-foreground"><tr><th className="px-4 py-3 font-medium">上传 ID</th><th className="px-3 py-3 font-medium">仓库</th><th className="px-3 py-3 font-medium">用户</th><th className="px-3 py-3 text-right font-medium">已接收</th><th className="px-3 py-3 font-medium">状态</th><th className="px-3 py-3 font-medium">创建</th><th className="px-3 py-3 font-medium">过期</th><th className="px-4 py-3 text-right font-medium">操作</th></tr></thead><tbody className="divide-y">{uploads.data.uploads.map((upload) => <tr key={upload.id} className="hover:bg-muted/25"><td className="px-4 py-3 font-mono text-xs">{upload.id}</td><td className="px-3 py-3"><p className="font-medium">{upload.repository}</p></td><td className="px-3 py-3 text-xs">{upload.username || "匿名"}</td><td className="px-3 py-3 text-right tabular-nums">{formatBytes(upload.size)}</td><td className="px-3 py-3"><StatusBadge value={upload.state} labels={{ created: "等待数据", uploading: "上传中", completed: "已完成", cancelled: "已取消" }} /></td><td className="whitespace-nowrap px-3 py-3 text-xs text-muted-foreground">{formatTime(upload.createdAt)}</td><td className="whitespace-nowrap px-3 py-3 text-xs text-muted-foreground">{formatTime(upload.expiresAt)}</td><td className="px-4 py-3 text-right">{manage ? <Button variant="ghost" size="icon-sm" className="text-destructive" onClick={() => setPending(upload)} aria-label="取消上传"><Trash2 className="h-3.5 w-3.5" /></Button> : null}</td></tr>)}</tbody></table></div>}</CardContent></Card>
    <div className="mt-3 flex items-center gap-2 text-xs text-muted-foreground"><UploadCloud className="h-4 w-4" />上传过期后会自动清理；服务重启不会丢失已接收的数据。</div>
    <ConfirmDialog open={pending !== null} onOpenChange={(open) => { if (!open) setPending(null); }} title="取消上传会话？" description={<>将删除上传 <span className="font-mono">{pending?.id}</span> 的临时数据和记录；客户端需要重新开始 push。</>} confirmLabel="取消上传" pending={cancel.isPending} onConfirm={() => { if (pending) cancel.mutate(pending); }} />
    <ConfirmDialog open={confirmReset} onOpenChange={setConfirmReset} title="恢复上传有效期的默认值？" description="将撤销页面中保存的上传有效期，恢复系统默认值并立即生效。" confirmLabel="恢复默认" destructive={false} pending={reset.isPending} onConfirm={() => { if (runtime.data) reset.mutate(runtime.data.sections.uploads.version); }} />
  </>;
}
