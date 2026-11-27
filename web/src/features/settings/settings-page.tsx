import { useEffect, useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Activity, ArrowRight, Boxes, Globe2, HardDrive, KeyRound, LockKeyhole, Network, RefreshCw, ScrollText, ServerCog, ShieldCheck, UploadCloud, type LucideIcon } from "lucide-react";
import { toast } from "sonner";
import { api, errorMessage } from "@/api/client";
import type { AnonymousRegistryAction, OutboundProxyPolicy, RegistryAuthPolicy, RuntimeSection, RuntimeSectionMeta, SecurityPolicy, WebSessionPolicy } from "@/api/types";
import { ConfirmDialog } from "@/components/common/confirm-dialog";
import { CopyButton } from "@/components/common/copy-button";
import { ErrorState, LoadingState } from "@/components/feedback/states";
import { PageHeader } from "@/components/layout/page-header";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select } from "@/components/ui/select";
import { useSessionQuery } from "@/features/auth/use-session";
import { Link } from "@/lib/router";
import { formatTime } from "@/lib/utils";

type OutboundProxyForm = { httpProxy: string; httpsProxy: string; noProxy: string; username: string; password: string; clearPassword: boolean };
type RegistryAccessMode = "authenticated" | "anonymous-pull" | "anonymous-push" | "anonymous-full";

export function SettingsPage() {
  const session = useSessionQuery();
  const admin = session.data?.user.role === "admin";
  const queryClient = useQueryClient();
  const runtime = useQuery({ queryKey: ["runtime-config"], queryFn: api.runtimeSettings });
  const system = useQuery({ queryKey: ["system", "info"], queryFn: api.systemInfo });
  const upstreams = useQuery({ queryKey: ["upstreams"], queryFn: api.upstreams });
  const [authForm, setAuthForm] = useState<RegistryAuthPolicy | null>(null);
  const [webForm, setWebForm] = useState<WebSessionPolicy | null>(null);
  const [outboundForm, setOutboundForm] = useState<OutboundProxyForm | null>(null);
  const [proxyForm, setProxyForm] = useState<{ disabled: boolean } | null>(null);
  const [securityForm, setSecurityForm] = useState<SecurityPolicy | null>(null);
  const [pendingReset, setPendingReset] = useState<RuntimeSection | null>(null);

  useEffect(() => {
    if (!runtime.data) return;
    setAuthForm(runtime.data.registryAuth);
    setWebForm(runtime.data.webSession);
    setOutboundForm({ ...runtime.data.outboundProxy, password: "", clearPassword: false });
    setProxyForm({ disabled: runtime.data.proxy.disabled });
    setSecurityForm({ hsts: runtime.data.security.hsts });
  }, [runtime.data]);

  const save = useMutation({
    mutationFn: ({ section, version, values }: { section: RuntimeSection; version: number; values: object }) => api.updateRuntimeSection(section, version, values),
    onSuccess: async (data, variables) => {
      queryClient.setQueryData(["runtime-config"], data);
      toast.success(`${sectionLabel(variables.section)}已保存，新请求立即生效`);
      await queryClient.invalidateQueries({ queryKey: ["system", "info"] });
    },
    onError: async (error) => {
      toast.error(errorMessage(error));
      await runtime.refetch();
    },
  });
  const reset = useMutation({
    mutationFn: ({ section, version }: { section: RuntimeSection; version: number }) => api.resetRuntimeSection(section, version),
    onSuccess: async (data, variables) => {
      queryClient.setQueryData(["runtime-config"], data);
      setPendingReset(null);
      toast.success(`${sectionLabel(variables.section)}已恢复系统默认`);
      await queryClient.invalidateQueries({ queryKey: ["system", "info"] });
    },
    onError: (error) => toast.error(errorMessage(error)),
  });
  const testProxy = useMutation({
    mutationFn: () => api.testOutboundProxy(),
    onSuccess: (result) => toast.success(`代理连接正常，耗时 ${result.durationMs}ms`),
    onError: (error) => toast.error(`代理测试失败：${errorMessage(error)}`),
  });

  if (runtime.isLoading || system.isLoading) return <LoadingState label="正在读取系统设置…" />;
  if (runtime.error || !runtime.data || system.error || !system.data) return <ErrorState error={runtime.error || system.error} onRetry={() => { void runtime.refetch(); void system.refetch(); }} />;
  if (!authForm || !webForm || !outboundForm || !proxyForm || !securityForm) return <LoadingState label="正在载入设置…" />;

  const info = system.data;
  const config = runtime.data;
  const registryURL = info.registryURL || info.publicURL;
  const managementURL = info.managementURL || info.publicURL;
  const secure = managementURL.startsWith("https://");
  const registryAccessMode = accessModeFor(authForm);
  const requiresAuthentication = registryAccessMode !== "anonymous-full";
  const usesBearer = requiresAuthentication && authForm.challenge === "bearer";
  const saveSection = (section: RuntimeSection) => (values: object) => save.mutate({ section, version: config.sections[section].version, values });

  return <>
    <PageHeader
      title="系统设置"
      description="统一管理镜像访问、登录会话、网络与安全策略。修改后会按页面提示即时生效。"
      actions={<Button variant="outline" size="sm" onClick={() => { void runtime.refetch(); void system.refetch(); }}><RefreshCw className={`h-4 w-4 ${runtime.isFetching ? "animate-spin" : ""}`} />刷新</Button>}
    />
    <Alert className={`mb-4 ${secure ? "border-success/30 bg-success/10" : "border-warning/30 bg-warning/10"}`}>
      <ShieldCheck className={`h-4 w-4 ${secure ? "text-success" : "text-warning"}`} />
      <AlertTitle>{secure ? "HTTPS 已启用" : "当前访问未使用 HTTPS"}</AlertTitle>
      <AlertDescription>{secure ? "管理台连接已受加密保护。" : "生产环境请通过 HTTPS 或反向代理提供访问，避免账号和登录状态在传输中泄露。"}</AlertDescription>
    </Alert>

    <div className="grid min-w-0 gap-4 xl:grid-cols-2">
      <SettingsCard title="镜像仓库访问控制" description="配置哪些镜像操作无需登录；仍受保护的操作使用下方验证方式。" icon={<KeyRound className="h-4 w-4 text-primary" />} meta={config.sections["registry-auth"]} effect="下一次请求生效" footer={<SectionActions disabled={!admin || save.isPending} onSave={() => saveSection("registry-auth")(authForm)} meta={config.sections["registry-auth"]} onReset={() => setPendingReset("registry-auth")} />}>
        <div className="grid min-w-0 gap-3 sm:grid-cols-2">
          <Field label="访问模式" htmlFor="auth-access-mode" hint="选择哪些镜像操作可以不登录直接执行。"><Select id="auth-access-mode" value={registryAccessMode} disabled={!admin} onChange={(event) => setAuthForm({ ...authForm, ...accessPolicyFor(event.target.value as RegistryAccessMode) })}><option value="authenticated">需要登录</option><option value="anonymous-pull">匿名只读（拉取）</option><option value="anonymous-push">匿名读写（拉取与推送）</option><option value="anonymous-full">完全匿名（含删除与仓库列表）</option></Select></Field>
          {requiresAuthentication ? <Field label="验证方式" htmlFor="auth-challenge" hint="用于仍需登录的操作。用户名密码方式直接验证账号；短期访问令牌方式会在后台自动续期。"><Select id="auth-challenge" value={authForm.challenge} disabled={!admin} onChange={(event) => setAuthForm({ ...authForm, challenge: event.target.value as RegistryAuthPolicy["challenge"] })}><option value="bearer">短期访问令牌</option><option value="basic">用户名密码认证</option></Select></Field> : null}
          <div className={`rounded-md border p-3 text-xs leading-5 sm:col-span-2 ${registryAccessMode === "anonymous-push" || registryAccessMode === "anonymous-full" ? "border-warning/40 bg-warning/10 text-foreground" : "bg-muted/15 text-muted-foreground"}`}>{accessModeNotice(registryAccessMode, usesBearer)}</div>
          {usesBearer ? <>
            <Field label="服务标识" htmlFor="auth-service" hint="用于识别访问令牌的签发对象，通常保持默认值。修改后，已签发的短期令牌会立即失效。"><Input id="auth-service" value={authForm.service} disabled={!admin} onChange={(event) => setAuthForm({ ...authForm, service: event.target.value })} /></Field>
            <Field label="令牌有效期" htmlFor="auth-token-ttl" hint="短期访问令牌的有效时长。缩短有效期更安全，但客户端会更频繁地续期。"><Input id="auth-token-ttl" value={authForm.tokenTtl} disabled={!admin} onChange={(event) => setAuthForm({ ...authForm, tokenTtl: event.target.value })} placeholder="5m" /></Field>
            <Field label="令牌服务地址" htmlFor="auth-realm" hint="客户端获取短期令牌时使用的地址。留空时自动使用当前访问地址；通过反向代理部署时，请填写客户端可访问的地址。"><Input id="auth-realm" value={authForm.realm} disabled={!admin} onChange={(event) => setAuthForm({ ...authForm, realm: event.target.value })} placeholder="留空使用当前访问地址" /></Field>
          </> : null}
          {requiresAuthentication && !usesBearer ? <p className="rounded-md border bg-muted/15 p-3 text-xs leading-5 text-muted-foreground sm:col-span-2">用户名密码方式直接验证账号或访问令牌，不需要配置令牌服务参数。</p> : null}
          {!requiresAuthentication ? <p className="rounded-md border bg-muted/15 p-3 text-xs leading-5 text-muted-foreground sm:col-span-2">完全匿名模式下，所有镜像操作都无需登录，因此不显示验证与令牌配置；切回其他模式后会沿用原有设置。</p> : null}
        </div>
      </SettingsCard>

      <SettingsCard title="管理台登录会话" description="调整管理台登录的有效时长。修改后对新会话生效，已有会话在下一次续期时采用新设置。" icon={<LockKeyhole className="h-4 w-4 text-primary" />} meta={config.sections["web-session"]} effect="新会话及下次续期生效" footer={<SectionActions disabled={!admin || save.isPending} onSave={() => saveSection("web-session")(webForm)} meta={config.sections["web-session"]} onReset={() => setPendingReset("web-session")} />}>
        <div className="grid min-w-0 gap-3 sm:grid-cols-2">
          <Field label="登录后最长有效时间" htmlFor="session-ttl" hint="超过该时长后需要重新登录。"><Input id="session-ttl" value={webForm.sessionTtl} disabled={!admin} onChange={(event) => setWebForm({ ...webForm, sessionTtl: event.target.value })} placeholder="12h" /></Field>
          <Field label="无操作自动退出" htmlFor="session-idle" hint="连续未操作达到该时长后自动退出登录。"><Input id="session-idle" value={webForm.idleTimeout} disabled={!admin} onChange={(event) => setWebForm({ ...webForm, idleTimeout: event.target.value })} placeholder="30m" /></Field>
        </div>
        <p className="mt-3 text-xs text-muted-foreground">登录后最长有效时间不超过 168 小时，无操作自动退出时间不能大于总时长。</p>
      </SettingsCard>

      <SettingsCard title="出站网络代理" description="访问 Docker Hub、GHCR、Quay、Kubernetes 镜像仓库等镜像来源时使用的网络代理。" icon={<Network className="h-4 w-4 text-primary" />} meta={config.sections["outbound-proxy"]} effect="下一次上游请求生效" footer={<div className="flex flex-wrap items-center gap-2"><Button variant="outline" size="sm" onClick={() => testProxy.mutate()} disabled={testProxy.isPending}>{testProxy.isPending ? "测试中…" : "测试当前代理"}</Button><SectionActions disabled={!admin || save.isPending} onSave={() => saveSection("outbound-proxy")(outboundForm)} meta={config.sections["outbound-proxy"]} onReset={() => setPendingReset("outbound-proxy")} /></div>}>
        <div className="grid min-w-0 gap-3 sm:grid-cols-2">
          <Field label="HTTP 代理" htmlFor="proxy-http"><Input id="proxy-http" value={outboundForm.httpProxy} disabled={!admin} onChange={(event) => setOutboundForm({ ...outboundForm, httpProxy: event.target.value })} placeholder="http://127.0.0.1:1080" /></Field>
          <Field label="HTTPS 代理" htmlFor="proxy-https"><Input id="proxy-https" value={outboundForm.httpsProxy} disabled={!admin} onChange={(event) => setOutboundForm({ ...outboundForm, httpsProxy: event.target.value })} placeholder="http://127.0.0.1:1080" /></Field>
          <Field label="不经过代理的地址" htmlFor="proxy-no" hint="多个地址用逗号分隔。"><Input id="proxy-no" value={outboundForm.noProxy} disabled={!admin} onChange={(event) => setOutboundForm({ ...outboundForm, noProxy: event.target.value })} placeholder="localhost,127.0.0.1,.internal" /></Field>
          <Field label="代理用户名" htmlFor="proxy-user"><Input id="proxy-user" value={outboundForm.username} disabled={!admin} onChange={(event) => setOutboundForm({ ...outboundForm, username: event.target.value })} autoComplete="off" /></Field>
          <Field label="代理密码" htmlFor="proxy-password"><Input id="proxy-password" type="password" value={outboundForm.password} disabled={!admin} onChange={(event) => setOutboundForm({ ...outboundForm, password: event.target.value, clearPassword: false })} placeholder={config.outboundProxy.passwordConfigured ? `已配置（${passwordSourceLabel(config.outboundProxy.passwordSource)}），留空保持` : "未配置"} autoComplete="new-password" /></Field>
          <label className="flex items-center gap-2 self-end rounded-md border bg-muted/15 p-3 text-sm"><Checkbox checked={outboundForm.clearPassword} disabled={!admin || !config.outboundProxy.passwordConfigured} onChange={(event) => setOutboundForm({ ...outboundForm, clearPassword: event.target.checked, password: "" })} />清除已保存的代理密码</label>
        </div>
        <p className="mt-3 text-xs text-muted-foreground">密码保存后不会回显。留空表示保持当前密码，勾选可清除已保存的密码。</p>
      </SettingsCard>

      <SettingsCard title="代理服务开关" description="关闭后，本地镜像仓库继续可用，通过各上游地址访问的代理缓存将暂停。" icon={<HardDrive className="h-4 w-4 text-primary" />} meta={config.sections.proxy} effect="下一次请求生效" footer={<SectionActions disabled={!admin || save.isPending} onSave={() => saveSection("proxy")(proxyForm)} meta={config.sections.proxy} onReset={() => setPendingReset("proxy")} />}>
        <label className="flex items-start gap-2 rounded-md border bg-muted/15 p-3 text-sm"><Checkbox checked={proxyForm.disabled} disabled={!admin} onChange={(event) => setProxyForm({ disabled: event.target.checked })} /><span><span className="font-medium">暂停上游代理服务</span><span className="mt-0.5 block text-xs text-muted-foreground">暂停后，所有代理镜像地址暂时不可访问。请先确认客户端不再依赖缓存服务。</span></span></label>
      </SettingsCard>

      <SettingsCard title="HTTPS 强制访问" description="在 HTTPS 环境下，要求浏览器后续访问自动使用 HTTPS。" icon={<ShieldCheck className="h-4 w-4 text-primary" />} meta={config.sections.security} effect="下一次响应生效" footer={<SectionActions disabled={!admin || save.isPending} onSave={() => saveSection("security")(securityForm)} meta={config.sections.security} onReset={() => setPendingReset("security")} />}>
        <label className="flex items-start gap-2 rounded-md border bg-muted/15 p-3 text-sm"><Checkbox checked={securityForm.hsts} disabled={!admin || config.startup.publicScheme !== "https"} onChange={(event) => setSecurityForm({ hsts: event.target.checked })} /><span><span className="font-medium">启用 HTTPS 强制访问</span><span className="mt-0.5 block text-xs text-muted-foreground">{config.startup.publicScheme === "https" ? "启用后，浏览器会在有效期内自动使用 HTTPS 访问控制台。" : "当前访问地址不是 HTTPS，暂时无法启用。请先完成 HTTPS 配置。"}</span></span></label>
      </SettingsCard>
    </div>

    <div className="mt-4 grid min-w-0 gap-4 xl:grid-cols-2">
      <Card><CardHeader><div className="flex items-center gap-2"><Boxes className="h-4 w-4 text-primary" /><CardTitle>常用设置</CardTitle></div><CardDescription>按运维场景前往对应页面调整设置。</CardDescription></CardHeader><CardContent className="grid gap-2 sm:grid-cols-2"><ConfigLink href="/storage" icon={HardDrive} title="存储与磁盘" detail="容量、阈值与空间回收" /><ConfigLink href="/uploads" icon={UploadCloud} title="上传会话" detail="上传有效期与清理" /><ConfigLink href="/audit" icon={ScrollText} title="审计日志" detail="日志保留期（管理员）" /><ConfigLink href="/upstreams" icon={Globe2} title="上游仓库" detail="镜像来源、凭证与缓存刷新" /></CardContent></Card>

      <Card><CardHeader><div className="flex items-center gap-2"><ServerCog className="h-4 w-4 text-primary" /><CardTitle>服务信息</CardTitle></div><CardDescription>当前实例的公开访问与运行概况。</CardDescription></CardHeader><CardContent className="grid min-w-0 gap-3 text-sm sm:grid-cols-2"><ReadonlyField label="镜像仓库地址" value={registryURL} mono /><ReadonlyField label="管理台地址" value={managementURL} mono /><ReadonlyField label="监听模式" value={info.planeMode === "split" ? "独立端口" : "共用端口"} /><ReadonlyField label="版本" value={info.version} mono /><ReadonlyField label="启动时间" value={formatTime(info.startedAt)} /><ReadonlyField label="上游数量" value={String(upstreams.data?.upstreams.length ?? 0)} /><ReadonlyField label="HTTPS" value={secure ? "已启用" : "未启用"} /><ReadonlyField label="代理缓存" value={config.proxy.disabled ? "已暂停" : "运行中"} /></CardContent></Card>
    </div>

    <div className="mt-4">
      <Card><CardHeader><div className="flex items-center gap-2"><Activity className="h-4 w-4 text-primary" /><CardTitle>客户端接入</CardTitle></div><CardDescription>常用服务地址，可直接复制使用。</CardDescription></CardHeader><CardContent className="grid min-w-0 gap-3 sm:grid-cols-2 xl:grid-cols-4"><Endpoint label="镜像仓库地址" value={`${registryURL}/v2/`} /><Endpoint label="服务状态" value={`${managementURL}/readyz`} /><Endpoint label="监控指标" value={`${managementURL}${info.metricsPath}`} /><Endpoint label="管理台地址" value={managementURL} /></CardContent></Card>
    </div>

    <Card className="mt-4"><CardHeader><CardTitle>运维提示</CardTitle><CardDescription>建议在变更前完成必要准备。</CardDescription></CardHeader><CardContent className="grid gap-3 text-sm text-muted-foreground sm:grid-cols-3"><p className="rounded-md border bg-muted/15 p-3">定期检查磁盘可用空间、缓存水位和上传会话。</p><p className="rounded-md border bg-muted/15 p-3">恢复默认设置只影响当前页面，不会删除镜像数据。</p><p className="rounded-md border bg-muted/15 p-3">迁移实例前请停止写入，并完整备份配置与数据。</p></CardContent></Card>

    <ConfirmDialog open={pendingReset !== null} onOpenChange={(open) => { if (!open) setPendingReset(null); }} title="恢复此页面的默认设置？" description={<>{pendingReset ? `将撤销“${sectionLabel(pendingReset)}”页面中的修改，恢复为系统默认值。` : "将恢复为系统默认值。"}该操作保存后立即生效。</>} confirmLabel="恢复默认" pending={reset.isPending} onConfirm={() => { if (pendingReset && runtime.data) reset.mutate({ section: pendingReset, version: runtime.data.sections[pendingReset].version }); }} />
  </>;
}

function SettingsCard({ title, description, icon, meta, effect, children, footer }: { title: string; description: string; icon: ReactNode; meta: RuntimeSectionMeta; effect: string; children: ReactNode; footer?: ReactNode }) {
      return <Card className="min-w-0"><CardHeader><div className="flex min-w-0 flex-wrap items-start justify-between gap-2"><div className="min-w-0"><div className="flex items-center gap-2"><span className="shrink-0">{icon}</span><CardTitle className="min-w-0">{title}</CardTitle></div><CardDescription className="mt-1.5">{description}</CardDescription></div><div className="flex shrink-0 flex-wrap gap-1.5"><Badge variant={meta.source === "runtime" ? "default" : "outline"}>{meta.source === "runtime" ? "已自定义" : "系统默认"}</Badge><Badge variant="secondary">{effect}</Badge></div></div>{meta.updatedBy ? <p className="text-[11px] text-muted-foreground">最后修改：{meta.updatedBy} · {formatTime(meta.updatedAt)}</p> : null}</CardHeader><CardContent className="min-w-0">{children}</CardContent>{footer ? <div className="flex min-w-0 flex-wrap items-center justify-end gap-2 border-t px-5 py-3">{footer}</div> : null}</Card>;
}

function SectionActions({ disabled, onSave, onReset, meta }: { disabled: boolean; onSave: () => void; onReset: () => void; meta: RuntimeSectionMeta }) {
  return <>{meta.source === "runtime" ? <Button variant="ghost" size="sm" onClick={onReset} disabled={disabled}>恢复默认</Button> : null}<Button size="sm" onClick={onSave} disabled={disabled}>保存</Button></>;
}

function Field({ label, htmlFor, hint, children }: { label: string; htmlFor: string; hint?: ReactNode; children: ReactNode }) {
  return <div className="min-w-0 space-y-1.5"><Label htmlFor={htmlFor}>{label}</Label>{children}{hint ? <p className="text-[11px] leading-4 text-muted-foreground">{hint}</p> : null}</div>;
}

function ReadonlyField({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return <div className="min-w-0 rounded-md border bg-muted/15 p-2.5"><dt className="text-[11px] text-muted-foreground">{label}</dt><dd className={`mt-0.5 break-words ${mono ? "font-mono text-xs" : "text-sm"}`}>{value}</dd></div>;
}

function Endpoint({ label, value }: { label: string; value: string }) {
  return <div className="min-w-0 rounded-md border bg-muted/15 p-2.5"><div className="flex items-center justify-between gap-2"><p className="text-xs text-muted-foreground">{label}</p><CopyButton value={value} label="" className="h-7 px-1.5" /></div><code className="mt-1 block break-all font-mono text-xs">{value}</code></div>;
}

function ConfigLink({ href, icon: Icon, title, detail }: { href: string; icon: LucideIcon; title: string; detail: string }) {
  return <Link href={href} className="group flex min-w-0 items-center gap-3 rounded-md border p-3 transition-colors hover:bg-accent"><span className="grid h-9 w-9 shrink-0 place-items-center rounded-md bg-primary/10 text-primary"><Icon className="h-4 w-4" /></span><span className="min-w-0 flex-1"><span className="block font-medium">{title}</span><span className="block truncate text-xs text-muted-foreground">{detail}</span></span><ArrowRight className="h-4 w-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5" /></Link>;
}

function sectionLabel(section: RuntimeSection) {
  const labels: Record<RuntimeSection, string> = { "registry-auth": "镜像仓库访问控制", "web-session": "管理台登录会话", storage: "存储策略", uploads: "上传有效期", audit: "审计保留期", proxy: "代理服务", "outbound-proxy": "出站网络代理", security: "HTTPS 强制访问" };
  return labels[section];
}

function passwordSourceLabel(source: OutboundProxyPolicy["passwordSource"]) {
  return source === "runtime" ? "页面设置" : source === "startup" ? "系统默认" : "未配置";
}

function accessModeFor(policy: RegistryAuthPolicy): RegistryAccessMode {
  if (!policy.allowAnonymous) return "authenticated";
  const actions = new Set(policy.anonymousActions);
  if (actions.has("delete") || actions.has("catalog")) return "anonymous-full";
  if (actions.has("push")) return "anonymous-push";
  return "anonymous-pull";
}

function accessPolicyFor(mode: RegistryAccessMode): Pick<RegistryAuthPolicy, "allowAnonymous" | "anonymousActions"> {
  const actions: Record<RegistryAccessMode, AnonymousRegistryAction[]> = {
    authenticated: [],
    "anonymous-pull": ["pull"],
    "anonymous-push": ["pull", "push"],
    "anonymous-full": ["pull", "push", "delete", "catalog"],
  };
  return { allowAnonymous: mode !== "authenticated", anonymousActions: actions[mode] };
}

function accessModeDescription(mode: RegistryAccessMode) {
  switch (mode) {
  case "anonymous-pull":
    return "未登录访客可以查看仓库并拉取镜像；推送、删除和仓库列表等操作仍需登录。";
  case "anonymous-push":
    return "未登录访客可以拉取和推送镜像，适合受信内网或隔离环境；删除和仓库列表等操作仍需登录。";
  case "anonymous-full":
    return "未登录访客可以拉取、推送、删除镜像并浏览仓库列表，风险最高，仅建议在完全可信的内网或离线环境启用。";
  default:
    return "所有镜像操作都需要登录后执行。";
  }
}

function accessModeNotice(mode: RegistryAccessMode, usesBearer: boolean) {
  const description = accessModeDescription(mode);
  if (usesBearer) return `${description} 修改服务标识会使已签发的短期令牌立即失效。`;
  if (mode !== "anonymous-full") return `${description} 用户名密码方式不需要额外配置令牌服务。`;
  return description;
}
