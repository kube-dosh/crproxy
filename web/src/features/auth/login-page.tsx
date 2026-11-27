import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Boxes, Layers3, Loader2, LockKeyhole, ShieldCheck } from "lucide-react";
import { api, errorMessage, setCSRFToken } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { sessionQueryKey } from "@/features/auth/use-session";
import { navigate } from "@/lib/router";
import type { Session } from "@/api/types";

export function LoginPage() {
  const queryClient = useQueryClient();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const mutation = useMutation({
    mutationFn: () => api.login(username.trim(), password),
    onSuccess: (data: Session) => {
      setCSRFToken(data.csrfToken);
      queryClient.setQueryData(sessionQueryKey, data);
      navigate("/", { replace: true });
    },
  });
  return (
    <div className="grid min-h-screen bg-background lg:grid-cols-[1.08fr_1fr]">
      <section className="relative hidden overflow-hidden bg-[#071a36] p-12 text-white lg:flex lg:flex-col">
        <div className="absolute inset-0 opacity-25" style={{ backgroundImage: "linear-gradient(rgba(255,255,255,.08) 1px, transparent 1px), linear-gradient(90deg, rgba(255,255,255,.08) 1px, transparent 1px)", backgroundSize: "44px 44px" }} />
        <div className="absolute -right-32 -top-32 h-96 w-96 rounded-full bg-blue-500/25 blur-3xl" />
        <div className="absolute -bottom-48 left-16 h-[28rem] w-[28rem] rounded-full bg-cyan-400/10 blur-3xl" />
        <div className="relative flex items-center gap-3"><div className="grid h-10 w-10 place-items-center rounded-xl bg-blue-500 font-bold shadow-lg shadow-blue-950/30">C</div><div><p className="font-semibold">CRProxy 控制台</p><p className="text-xs text-blue-200/75">镜像仓库与缓存</p></div></div>
        <div className="relative my-auto max-w-xl">
          <p className="mb-5 text-xs font-semibold uppercase tracking-[0.25em] text-blue-300">容器镜像管理平台</p>
          <h1 className="text-4xl font-semibold leading-tight tracking-tight xl:text-5xl">本地镜像与多上游缓存<br /><span className="text-blue-300">统一管理，安全可审计。</span></h1>
          <p className="mt-6 max-w-lg text-sm leading-7 text-slate-300">统一管理私有镜像、多上游按需缓存、团队账号与存储容量，让内网、边缘节点和 CI/CD 更快、更稳定地获取容器镜像。</p>
          <div className="mt-10 grid max-w-lg grid-cols-3 gap-4">
            <Feature icon={Boxes} title="私有镜像仓库" text="推送、拉取与删除" />
            <Feature icon={Layers3} title="多上游缓存" text="按需缓存公共镜像" />
            <Feature icon={ShieldCheck} title="安全可控" text="账号、令牌与 HTTPS" />
          </div>
        </div>
        <p className="relative text-xs text-slate-400">CRProxy · 自托管镜像仓库与缓存</p>
      </section>
      <section className="flex items-center justify-center p-6 sm:p-10">
        <Card className="w-full max-w-md shadow-lg"><CardContent className="pt-6">
          <div className="mb-7"><div className="mb-4 grid h-10 w-10 place-items-center rounded-lg border bg-card text-primary shadow-sm"><LockKeyhole className="h-5 w-5" /></div><h2 className="text-2xl font-semibold tracking-tight">登录控制台</h2><p className="mt-1.5 text-sm text-muted-foreground">使用管理员或运维账号继续。</p></div>
          <form className="space-y-4" onSubmit={(event) => { event.preventDefault(); mutation.mutate(); }}>
            <div className="space-y-1.5"><Label htmlFor="username">用户名</Label><Input id="username" autoFocus autoComplete="username" value={username} onChange={(event) => setUsername(event.target.value)} placeholder="admin" /></div>
            <div className="space-y-1.5"><Label htmlFor="password">密码</Label><Input id="password" type="password" autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} placeholder="输入密码" /></div>
            {mutation.error ? <div role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">{errorMessage(mutation.error)}</div> : null}
            <Button type="submit" className="w-full" disabled={mutation.isPending || !username.trim() || !password}>{mutation.isPending ? <Loader2 className="h-4 w-4 animate-spin" /> : <LockKeyhole className="h-4 w-4" />}登录</Button>
          </form>
          <div className="mt-6 rounded-lg border bg-card p-3 text-xs leading-5 text-muted-foreground">登录与账号变更会记录在审计日志中；生产环境建议启用 HTTPS。</div>
        </CardContent></Card>
      </section>
    </div>
  );
}

function Feature({ icon: Icon, title, text }: { icon: typeof Boxes; title: string; text: string }) {
  return <div className="rounded-lg border border-white/10 bg-white/5 p-3 backdrop-blur-sm"><Icon className="h-4 w-4 text-blue-300" /><p className="mt-3 text-sm font-medium">{title}</p><p className="mt-0.5 text-xs text-slate-400">{text}</p></div>;
}
