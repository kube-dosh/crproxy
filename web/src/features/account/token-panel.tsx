import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Loader2, Plus, ShieldAlert, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { api, errorMessage } from "@/api/client";
import type { APIToken, Role } from "@/api/types";
import { ConfirmDialog } from "@/components/common/confirm-dialog";
import { CopyButton } from "@/components/common/copy-button";
import { EmptyState, ErrorState, LoadingState } from "@/components/feedback/states";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select } from "@/components/ui/select";
import { useSessionQuery } from "@/features/auth/use-session";
import { scopeLabel } from "@/lib/labels";
import { formatTime } from "@/lib/utils";

const allScopes = ["pull", "push", "delete", "manage", "admin"] as const;
const roleScopes: Record<Role, string[]> = { admin: ["pull", "push", "delete", "manage", "admin"], operator: ["pull", "push", "delete", "manage"], viewer: ["pull"] };

export function TokenPanel() {
  const session = useSessionQuery();
  const queryClient = useQueryClient();
  const [createOpen, setCreateOpen] = useState(false);
  const [name, setName] = useState("");
  const [expiresInDays, setExpiresInDays] = useState("90");
  const [scopes, setScopes] = useState<string[]>(["pull"]);
  const [created, setCreated] = useState<string | null>(null);
  const [pendingRevoke, setPendingRevoke] = useState<APIToken | null>(null);
  const tokens = useQuery({ queryKey: ["tokens", "self"], queryFn: api.tokens });
  const allowed = roleScopes[session.data?.user.role ?? "viewer"] ?? ["pull"];
  const create = useMutation({
    mutationFn: () => api.createToken({ name: name.trim(), scopes, expiresInDays: Number(expiresInDays) }),
    onSuccess: async (response) => { setCreated(response.token); setCreateOpen(false); setName(""); setScopes(["pull"]); await queryClient.invalidateQueries({ queryKey: ["tokens"] }); toast.success("访问令牌已创建"); },
    onError: (error) => toast.error(errorMessage(error)),
  });
  const revoke = useMutation({ mutationFn: (token: APIToken) => api.revokeToken(token.id), onSuccess: async () => { toast.success("访问令牌已撤销"); setPendingRevoke(null); await queryClient.invalidateQueries({ queryKey: ["tokens"] }); }, onError: (error) => toast.error(errorMessage(error)) });
  const toggleScope = (scope: string) => setScopes((current) => current.includes(scope) ? current.filter((item) => item !== scope) : [...current, scope]);
  return <Card><CardHeader className="flex-row items-start justify-between gap-3"><div><div className="flex items-center gap-2"><KeyRound className="h-4 w-4 text-primary" /><CardTitle>访问令牌</CardTitle></div><CardDescription className="mt-1.5">用于 Podman 等镜像客户端登录；令牌仅在创建时显示一次，请妥善保存。</CardDescription></div><Button size="sm" onClick={() => { setScopes(["pull"]); setCreateOpen(true); }}><Plus className="h-4 w-4" />创建令牌</Button></CardHeader><CardContent>{tokens.isLoading ? <LoadingState /> : tokens.error || !tokens.data ? <ErrorState error={tokens.error} onRetry={() => void tokens.refetch()} /> : tokens.data.tokens.length === 0 ? <EmptyState title="还没有访问令牌" description="创建后可用于镜像客户端登录，避免在脚本中保存账户密码。" /> : <div className="overflow-x-auto"><table className="w-full min-w-[700px] text-sm"><thead className="border-b bg-muted/30 text-left text-xs text-muted-foreground"><tr><th className="px-3 py-2.5 font-medium">名称</th><th className="px-3 py-2.5 font-medium">标识前缀</th><th className="px-3 py-2.5 font-medium">权限</th><th className="px-3 py-2.5 font-medium">创建</th><th className="px-3 py-2.5 font-medium">过期</th><th className="px-3 py-2.5 font-medium">状态</th><th className="px-3 py-2.5 text-right font-medium">操作</th></tr></thead><tbody className="divide-y">{tokens.data.tokens.map((token) => <tr key={token.id}><td className="px-3 py-2.5 font-medium">{token.name}</td><td className="px-3 py-2.5 font-mono text-xs">{token.prefix}</td><td className="px-3 py-2.5"><div className="flex flex-wrap gap-1">{token.scopes.map((scope) => <Badge key={scope} variant="secondary">{scopeLabel(scope)}</Badge>)}</div></td><td className="whitespace-nowrap px-3 py-2.5 text-xs text-muted-foreground">{formatTime(token.createdAt)}</td><td className="whitespace-nowrap px-3 py-2.5 text-xs text-muted-foreground">{token.expiresAt ? formatTime(token.expiresAt) : "永不过期"}</td><td className="px-3 py-2.5">{token.revokedAt ? <Badge variant="secondary">已撤销</Badge> : token.expiresAt && new Date(token.expiresAt) <= new Date() ? <Badge variant="warning">已过期</Badge> : <Badge variant="success">有效</Badge>}</td><td className="px-3 py-2.5 text-right">{token.revokedAt ? null : <Button variant="ghost" size="icon-sm" className="text-destructive" onClick={() => setPendingRevoke(token)} aria-label={`撤销 ${token.name}`}><Trash2 className="h-3.5 w-3.5" /></Button>}</td></tr>)}</tbody></table></div>}</CardContent>
    <Dialog open={createOpen} onOpenChange={setCreateOpen}><DialogContent><DialogHeader><DialogTitle>创建访问令牌</DialogTitle><DialogDescription>令牌权限不能超过当前账号角色。具备管理权限的令牌可用于自动化管理任务。</DialogDescription></DialogHeader><div className="space-y-4"><div className="space-y-1.5"><Label htmlFor="token-name">名称</Label><Input id="token-name" value={name} onChange={(event) => setName(event.target.value)} placeholder="ci-build" maxLength={100} /></div><div className="space-y-1.5"><Label htmlFor="token-expiry">有效期</Label><Select id="token-expiry" value={expiresInDays} onChange={(event) => setExpiresInDays(event.target.value)}><option value="30">30 天</option><option value="90">90 天</option><option value="180">180 天</option><option value="365">1 年</option><option value="0">永不过期</option></Select></div><div className="space-y-2"><Label>权限范围</Label><div className="grid grid-cols-2 gap-2">{allScopes.map((scope) => { const disabled = !allowed.includes(scope); return <label key={scope} className={`flex items-center gap-2 rounded-md border p-2 text-sm ${disabled ? "opacity-45" : ""}`}><input type="checkbox" className="h-4 w-4 accent-primary" disabled={disabled} checked={scopes.includes(scope)} onChange={() => toggleScope(scope)} /><span>{scopeLabel(scope)}</span>{disabled ? <span className="ml-auto text-[10px] text-muted-foreground">不可授予</span> : null}</label>; })}</div></div>{create.error ? <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">{errorMessage(create.error)}</p> : null}</div><DialogFooter><Button variant="outline" onClick={() => setCreateOpen(false)} disabled={create.isPending}>取消</Button><Button onClick={() => create.mutate()} disabled={create.isPending || !name.trim() || scopes.length === 0}>{create.isPending ? <Loader2 className="h-4 w-4 animate-spin" /> : null}创建</Button></DialogFooter></DialogContent></Dialog>
    <Dialog open={created !== null} onOpenChange={(open) => { if (!open) setCreated(null); }}><DialogContent><DialogHeader><div className="mb-1 grid h-9 w-9 place-items-center rounded-lg bg-warning/10 text-warning"><ShieldAlert className="h-4 w-4" /></div><DialogTitle>请立即保存访问令牌</DialogTitle><DialogDescription>关闭后将无法再次查看。若令牌泄露，请立即撤销。</DialogDescription></DialogHeader><div className="flex items-start gap-2 rounded-md border bg-muted/20 p-3"><code className="min-w-0 flex-1 break-all font-mono text-xs">{created}</code><CopyButton value={created ?? ""} /></div><DialogFooter><Button onClick={() => setCreated(null)}>我已保存</Button></DialogFooter></DialogContent></Dialog>
    <ConfirmDialog open={pendingRevoke !== null} onOpenChange={(open) => { if (!open) setPendingRevoke(null); }} title="撤销访问令牌？" description={<>撤销 <span className="font-mono">{pendingRevoke?.name}</span> 后，使用该令牌的客户端会立即失去访问权限。</>} confirmLabel="撤销令牌" pending={revoke.isPending} onConfirm={() => { if (pendingRevoke) revoke.mutate(pendingRevoke); }} />
  </Card>;
}
