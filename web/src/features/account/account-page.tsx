import { useQuery } from "@tanstack/react-query";
import { CalendarDays, KeyRound, ShieldCheck, UserRound } from "lucide-react";
import { api } from "@/api/client";
import { PageHeader } from "@/components/layout/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { TokenPanel } from "@/features/account/token-panel";
import { useSessionQuery } from "@/features/auth/use-session";
import { roleLabel } from "@/components/layout/app-shell";
import { Link } from "@/lib/router";
import { formatTime } from "@/lib/utils";

export function AccountPage() {
  const session = useSessionQuery();
  const system = useQuery({ queryKey: ["system", "info"], queryFn: api.systemInfo, staleTime: 60_000 });
  const user = session.data?.user;
  return <>
    <PageHeader title="账户与访问令牌" description="查看当前身份、修改密码并管理 Podman 等镜像客户端使用的访问令牌。" actions={<Button variant="outline" size="sm" asChild><Link href="/account/password"><KeyRound className="h-4 w-4" />修改密码</Link></Button>} />
    <div className="grid gap-4 xl:grid-cols-[320px_minmax(0,1fr)]">
      <Card className="h-fit"><CardHeader><div className="mb-2 grid h-12 w-12 place-items-center rounded-xl bg-primary/10 text-primary"><UserRound className="h-6 w-6" /></div><CardTitle>{user?.username}</CardTitle><CardDescription>{roleLabel(user?.role)} · 本地账号</CardDescription></CardHeader><CardContent className="space-y-3 text-sm"><Field icon={ShieldCheck} label="角色权限" value={roleLabel(user?.role)} /><Field icon={CalendarDays} label="账号创建" value={formatTime(user?.createdAt)} /><Field icon={KeyRound} label="最近登录" value={formatTime(user?.lastLoginAt)} /><div className="rounded-md border bg-muted/20 p-3 text-xs leading-5 text-muted-foreground">当前登录方式：浏览器会话。登录状态受有效期与无操作超时保护。</div><div className="flex flex-wrap gap-1"><Badge variant="outline">OCI {system.data?.ociSpec || "1.1"}</Badge><Badge variant="outline">本地账号</Badge></div></CardContent></Card>
      <TokenPanel />
    </div>
  </>;
}

function Field({ icon: Icon, label, value }: { icon: typeof ShieldCheck; label: string; value: string }) {
  return <div className="flex items-center gap-3 rounded-md border p-2.5"><Icon className="h-4 w-4 text-muted-foreground" /><div className="min-w-0"><p className="text-[11px] text-muted-foreground">{label}</p><p className="truncate font-medium">{value}</p></div></div>;
}
