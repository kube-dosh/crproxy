import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Database, Loader2, Pencil, Plus, ShieldAlert, Trash2, Wifi } from "lucide-react";
import { toast } from "sonner";
import { api, errorMessage } from "@/api/client";
import type { Upstream, UpstreamInput } from "@/api/types";
import { ConfirmDialog } from "@/components/common/confirm-dialog";
import { StatusBadge } from "@/components/common/status-badge";
import { EmptyState, ErrorState, LoadingState } from "@/components/feedback/states";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { useSessionQuery } from "@/features/auth/use-session";
import { authTypeLabel, upstreamHealthMessageLabel } from "@/lib/labels";
import { formatTime } from "@/lib/utils";

type FormState = {
  id: string; name: string; endpoint: string; routeHost: string; routePrefix: string; repositoryPrefix: string;
  authType: Upstream["authType"]; username: string; password: string; bearerToken: string; clearCredentials: boolean;
  caPem: string; insecureSkipVerify: boolean; tagTtlSeconds: string; requestTimeoutSeconds: string; maxBlobBytes: string; clearMaxBlobBytes: boolean; enabled: boolean;
};

const emptyForm: FormState = { id: "", name: "", endpoint: "https://registry-1.docker.io", routeHost: "", routePrefix: "", repositoryPrefix: "", authType: "challenge", username: "", password: "", bearerToken: "", clearCredentials: false, caPem: "", insecureSkipVerify: false, tagTtlSeconds: "60", requestTimeoutSeconds: "30", maxBlobBytes: "", clearMaxBlobBytes: false, enabled: true };

export function UpstreamsPage() {
  const session = useSessionQuery();
  const manage = session.data?.user.role === "admin" || session.data?.user.role === "operator";
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState<Upstream | null>(null);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [form, setForm] = useState<FormState>(emptyForm);
  const [pendingDelete, setPendingDelete] = useState<Upstream | null>(null);
  const upstreams = useQuery({ queryKey: ["upstreams"], queryFn: api.upstreams });
  const save = useMutation({
    mutationFn: async () => {
      const input = toInput(form, editing);
      return editing ? api.updateUpstream(editing.id, input) : api.createUpstream(input);
    },
    onSuccess: async () => { toast.success(editing ? "上游配置已更新" : "上游已创建"); setDialogOpen(false); await queryClient.invalidateQueries({ queryKey: ["upstreams"] }); await queryClient.invalidateQueries({ queryKey: ["overview"] }); await queryClient.invalidateQueries({ queryKey: ["repositories"] }); },
    onError: (error) => toast.error(errorMessage(error)),
  });
  const remove = useMutation({ mutationFn: (item: Upstream) => api.deleteUpstream(item.id), onSuccess: async () => { toast.success("上游已删除，相关缓存记录已清理"); setPendingDelete(null); await queryClient.invalidateQueries({ queryKey: ["upstreams"] }); await queryClient.invalidateQueries({ queryKey: ["repositories"] }); await queryClient.invalidateQueries({ queryKey: ["overview"] }); }, onError: (error) => toast.error(errorMessage(error)) });
  const test = useMutation({ mutationFn: (id: string) => api.testUpstream(id), onSuccess: async (_, id) => { const name = upstreams.data?.upstreams.find((item) => item.id === id)?.name; toast.success(`${name ?? "上游"}连接正常`); await queryClient.invalidateQueries({ queryKey: ["upstreams"] }); await queryClient.invalidateQueries({ queryKey: ["overview"] }); }, onError: (error) => toast.error(`连接测试失败：${errorMessage(error)}`) });
  const openCreate = () => { setEditing(null); setForm(emptyForm); setDialogOpen(true); };
  const openEdit = (item: Upstream) => { setEditing(item); setForm(fromUpstream(item)); setDialogOpen(true); };
  return <>
    <PageHeader title="上游仓库" description="配置多个只读镜像上游，支持 Docker Hub、GHCR、Quay、Kubernetes 镜像仓库与自定义镜像来源。可设置登录凭证、证书、缓存刷新与访问路由。" actions={manage ? <Button onClick={openCreate}><Plus className="h-4 w-4" />新增上游</Button> : null} />
    {upstreams.isLoading ? <LoadingState /> : upstreams.error || !upstreams.data ? <ErrorState error={upstreams.error} onRetry={() => void upstreams.refetch()} /> : upstreams.data.upstreams.length === 0 ? <EmptyState title="尚未配置上游" description="至少配置一个上游后，才能通过代理路径拉取并缓存镜像。" action={manage ? <Button onClick={openCreate}><Plus className="h-4 w-4" />新增上游</Button> : undefined} /> : <div className="grid gap-4 xl:grid-cols-2">{upstreams.data.upstreams.map((upstream) => <Card key={upstream.id}><CardContent className="pt-5"><div className="flex items-start gap-3"><span className="grid h-10 w-10 shrink-0 place-items-center rounded-lg bg-primary/10 text-primary"><Database className="h-5 w-5" /></span><div className="min-w-0 flex-1"><div className="flex flex-wrap items-center gap-2"><h2 className="truncate font-semibold">{upstream.name}</h2><StatusBadge value={upstream.enabled ? upstream.healthState : "disabled"} labels={{ disabled: "已停用" }} /></div><p className="mt-1 truncate font-mono text-xs text-muted-foreground" title={upstream.endpoint}>{upstream.endpoint}</p></div>{manage ? <div className="flex gap-1"><Button variant="ghost" size="icon-sm" onClick={() => test.mutate(upstream.id)} disabled={test.isPending} aria-label={`测试 ${upstream.name}`}>{test.isPending && test.variables === upstream.id ? <Loader2 className="h-4 w-4 animate-spin" /> : <Wifi className="h-4 w-4" />}</Button><Button variant="ghost" size="icon-sm" onClick={() => openEdit(upstream)} aria-label={`编辑 ${upstream.name}`}><Pencil className="h-4 w-4" /></Button><Button variant="ghost" size="icon-sm" className="text-destructive" onClick={() => setPendingDelete(upstream)} aria-label={`删除 ${upstream.name}`}><Trash2 className="h-4 w-4" /></Button></div> : null}</div>
      <dl className="mt-4 grid grid-cols-2 gap-x-4 gap-y-3 border-t pt-4 text-xs sm:grid-cols-3"><Field label="代理路径" value={`/r/${upstream.routePrefix}`} mono /><Field label="专属访问域名" value={upstream.routeHost || "—"} mono /><Field label="身份验证" value={authTypeLabel(upstream.authType) + (upstream.hasCredentials ? " · 已保存登录凭证" : "")} /><Field label="标签缓存有效期" value={`${upstream.tagTtlSeconds}s`} /><Field label="请求超时" value={`${upstream.requestTimeoutSeconds}s`} /><Field label="最近检查" value={formatTime(upstream.healthCheckedAt)} /></dl>
      {upstream.healthMessage ? <p className="mt-3 rounded-md border border-warning/25 bg-warning/10 px-3 py-2 text-xs text-warning">{upstreamHealthMessageLabel(upstream.healthMessage)}</p> : null}
      {upstream.insecureSkipVerify ? <p className="mt-3 flex items-center gap-2 rounded-md border border-destructive/25 bg-destructive/10 px-3 py-2 text-xs text-destructive"><ShieldAlert className="h-3.5 w-3.5 shrink-0" />已关闭上游 HTTPS 证书校验，仅应在受控测试环境使用。</p> : null}
    </CardContent></Card>)}</div>}
    <Dialog open={dialogOpen} onOpenChange={(open) => { if (!save.isPending) setDialogOpen(open); }}><DialogContent className="max-w-3xl"><DialogHeader><DialogTitle>{editing ? "编辑上游" : "新增上游"}</DialogTitle><DialogDescription>上游地址必须使用 HTTPS。保存后立即生效，无需重启服务。</DialogDescription></DialogHeader><UpstreamForm form={form} setForm={setForm} editing={editing} />{save.error ? <div role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">{errorMessage(save.error)}</div> : null}<DialogFooter><Button variant="outline" onClick={() => setDialogOpen(false)} disabled={save.isPending}>取消</Button><Button onClick={() => save.mutate()} disabled={save.isPending || !form.id.trim() || !form.name.trim() || !form.endpoint.trim()}>{save.isPending ? <Loader2 className="h-4 w-4 animate-spin" /> : null}{editing ? "保存修改" : "创建上游"}</Button></DialogFooter></DialogContent></Dialog>
    <ConfirmDialog open={pendingDelete !== null} onOpenChange={(open) => { if (!open) setPendingDelete(null); }} title="删除上游？" description={<>将删除 <span className="font-mono">{pendingDelete?.name}</span> 及其缓存记录；本地镜像不受影响。删除前请确认没有客户端仍在使用该代理地址。</>} confirmLabel="删除上游" pending={remove.isPending} onConfirm={() => { if (pendingDelete) remove.mutate(pendingDelete); }} />
  </>;
}

function UpstreamForm({ form, setForm, editing }: { form: FormState; setForm: (next: FormState) => void; editing: Upstream | null }) {
  const set = <K extends keyof FormState>(key: K, value: FormState[K]) => setForm({ ...form, [key]: value });
  return <div className="grid gap-4 sm:grid-cols-2">
    <div className="space-y-1.5"><Label htmlFor="up-id">标识</Label><Input id="up-id" value={form.id} disabled={editing !== null} onChange={(event) => set("id", event.target.value)} placeholder="dockerhub" /><p className="text-[11px] text-muted-foreground">小写字母、数字、下划线或连字符。</p></div>
    <div className="space-y-1.5"><Label htmlFor="up-name">显示名称</Label><Input id="up-name" value={form.name} onChange={(event) => set("name", event.target.value)} placeholder="Docker Hub" /></div>
    <div className="space-y-1.5 sm:col-span-2"><Label htmlFor="up-endpoint">上游地址</Label><Input id="up-endpoint" value={form.endpoint} onChange={(event) => set("endpoint", event.target.value)} placeholder="https://registry-1.docker.io" /></div>
    <div className="space-y-1.5"><Label htmlFor="up-route-prefix">代理路径前缀</Label><Input id="up-route-prefix" value={form.routePrefix} onChange={(event) => set("routePrefix", event.target.value)} placeholder="默认等于标识" /></div>
    <div className="space-y-1.5"><Label htmlFor="up-route-host">专属访问域名（可选）</Label><Input id="up-route-host" value={form.routeHost} onChange={(event) => set("routeHost", event.target.value)} placeholder="docker.example.com" /></div>
    <div className="space-y-1.5"><Label htmlFor="up-repo-prefix">仓库名前缀（可选）</Label><Input id="up-repo-prefix" value={form.repositoryPrefix} onChange={(event) => set("repositoryPrefix", event.target.value)} placeholder="library" /><p className="text-[11px] text-muted-foreground">仅当上游需要固定仓库前缀时填写。</p></div>
    <div className="space-y-1.5"><Label htmlFor="up-auth">身份验证方式</Label><Select id="up-auth" value={form.authType} onChange={(event) => set("authType", event.target.value as FormState["authType"])}><option value="anonymous">匿名</option><option value="challenge">自动获取令牌</option><option value="basic">用户名密码</option><option value="bearer">固定访问令牌</option></Select></div>
    {form.authType === "basic" ? <><div className="space-y-1.5"><Label htmlFor="up-user">用户名</Label><Input id="up-user" autoComplete="off" value={form.username} onChange={(event) => set("username", event.target.value)} /></div><div className="space-y-1.5"><Label htmlFor="up-pass">密码</Label><Input id="up-pass" type="password" autoComplete="new-password" value={form.password} onChange={(event) => set("password", event.target.value)} placeholder={editing?.hasCredentials ? "留空则保留已保存凭证" : ""} /></div></> : null}
    {form.authType === "bearer" ? <div className="space-y-1.5 sm:col-span-2"><Label htmlFor="up-token">访问令牌</Label><Input id="up-token" type="password" autoComplete="new-password" value={form.bearerToken} onChange={(event) => set("bearerToken", event.target.value)} placeholder={editing?.hasCredentials ? "留空则保留已保存凭证" : ""} /></div> : null}
    {editing?.hasCredentials && (form.authType === "anonymous" || form.authType === "challenge") ? <label className="flex items-center gap-2 text-sm sm:col-span-2"><input type="checkbox" className="h-4 w-4 accent-primary" checked={form.clearCredentials} onChange={(event) => set("clearCredentials", event.target.checked)} />清除已保存的上游凭证</label> : null}
    <div className="space-y-1.5"><Label htmlFor="up-tag-ttl">标签缓存有效期（秒）</Label><Input id="up-tag-ttl" type="number" min="1" value={form.tagTtlSeconds} onChange={(event) => set("tagTtlSeconds", event.target.value)} /></div>
    <div className="space-y-1.5"><Label htmlFor="up-timeout">请求超时（秒）</Label><Input id="up-timeout" type="number" min="1" value={form.requestTimeoutSeconds} onChange={(event) => set("requestTimeoutSeconds", event.target.value)} /></div>
    <div className="space-y-1.5"><Label htmlFor="up-max-blob">单个镜像层上限（字节）</Label><Input id="up-max-blob" type="number" min="1" value={form.maxBlobBytes} onChange={(event) => set("maxBlobBytes", event.target.value)} placeholder="使用全局上限" /></div>
    <div className="flex flex-col justify-end gap-2 pb-1"><label className="flex items-center gap-2 text-sm"><input type="checkbox" className="h-4 w-4 accent-primary" checked={form.enabled} onChange={(event) => set("enabled", event.target.checked)} />启用该上游</label><label className="flex items-center gap-2 text-sm"><input type="checkbox" className="h-4 w-4 accent-destructive" checked={form.insecureSkipVerify} onChange={(event) => set("insecureSkipVerify", event.target.checked)} />跳过 HTTPS 证书校验</label></div>
    <div className="space-y-1.5 sm:col-span-2"><Label htmlFor="up-ca">自定义证书（可选）</Label><Textarea id="up-ca" value={form.caPem} onChange={(event) => set("caPem", event.target.value)} placeholder="粘贴 CA 证书内容" /></div>
  </div>;
}

function Field({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return <div className="min-w-0"><dt className="text-muted-foreground">{label}</dt><dd className={`mt-0.5 truncate ${mono ? "font-mono" : ""}`} title={value}>{value}</dd></div>;
}

function fromUpstream(value: Upstream): FormState {
  return { ...emptyForm, id: value.id, name: value.name, endpoint: value.endpoint, routeHost: value.routeHost || "", routePrefix: value.routePrefix, repositoryPrefix: value.repositoryPrefix || "", authType: value.authType, caPem: value.caPem || "", insecureSkipVerify: value.insecureSkipVerify, tagTtlSeconds: String(value.tagTtlSeconds), requestTimeoutSeconds: String(value.requestTimeoutSeconds), maxBlobBytes: value.maxBlobBytes ? String(value.maxBlobBytes) : "", enabled: value.enabled };
}

function toInput(form: FormState, editing: Upstream | null): UpstreamInput {
  const username = form.username.trim();
  const password = form.password;
  const bearerToken = form.bearerToken.trim();
  const suppliedCredentials = username !== "" || password !== "" || bearerToken !== "";
  const input: UpstreamInput = {
    id: form.id.trim(), name: form.name.trim(), endpoint: form.endpoint.trim(), routeHost: form.routeHost.trim(), routePrefix: form.routePrefix.trim(), repositoryPrefix: form.repositoryPrefix.trim(),
    authType: form.authType, caPem: form.caPem.trim(), insecureSkipVerify: form.insecureSkipVerify, tagTtlSeconds: Number(form.tagTtlSeconds), requestTimeoutSeconds: Number(form.requestTimeoutSeconds), enabled: form.enabled,
    replaceCredentials: editing === null || suppliedCredentials || form.clearCredentials,
  };
  if (form.maxBlobBytes.trim()) input.maxBlobBytes = Number(form.maxBlobBytes);
  if (editing?.maxBlobBytes && !form.maxBlobBytes.trim()) input.clearMaxBlobBytes = true;
  if (suppliedCredentials) input.credentials = { username, password, bearerToken };
  return input;
}
