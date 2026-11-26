import { useQuery } from "@tanstack/react-query";
import { Boxes, Database, GitBranch, HardDrive, Layers3, LockKeyhole, Server, ShieldCheck } from "lucide-react";
import { api } from "@/api/client";
import { PageHeader } from "@/components/layout/page-header";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { formatTime } from "@/lib/utils";

const capabilities = [
  { icon: Boxes, title: "完整 OCI Distribution", text: "pull、push、chunked / monolithic upload、mount、catalog、tags、referrers、range、delete 与错误语义。" },
  { icon: Layers3, title: "多上游 Pull-Through Cache", text: "Docker Hub、GHCR、Quay、registry.k8s.io 与任意 HTTPS OCI Registry；路径和 Host 双路由。" },
  { icon: LockKeyhole, title: "认证与加密", text: "Basic、PAT、Docker Bearer Token、HTTPS/TLS 热加载、可信代理、Secure Cookie 与 CSRF。" },
  { icon: HardDrive, title: "共享 CAS 与容量治理", text: "本地与缓存共享内容寻址存储；配额、水位、LRU、GC、fsck 与崩溃恢复。" },
  { icon: Database, title: "SQLite 元数据", text: "modernc.org/sqlite 纯 Go 驱动，rollback journal，不启用 WAL；外键和 FULL synchronous。" },
  { icon: ShieldCheck, title: "审计与可观测性", text: "管理动作与 Registry 事件审计、24h 聚合、Prometheus 低基数指标、后台任务进度。" },
];

export function AboutPage() {
  const system = useQuery({ queryKey: ["system", "info"], queryFn: api.systemInfo, staleTime: 60_000 });
  return <>
    <PageHeader title="关于 CRProxy" description="面向私有环境与边缘节点的轻量级 OCI Registry + 多上游代理缓存。" />
    <div className="grid gap-4 lg:grid-cols-[minmax(0,1.2fr)_minmax(300px,0.8fr)]">
      <Card><CardHeader><div className="mb-2 grid h-12 w-12 place-items-center rounded-xl bg-primary text-lg font-bold text-primary-foreground">C</div><CardTitle>CRProxy</CardTitle><CardDescription>Self-hosted OCI Registry &amp; Pull-Through Cache</CardDescription></CardHeader><CardContent className="space-y-2 text-sm text-muted-foreground"><p>CRProxy 以单个 Go 二进体内嵌管理台，面向单节点、自有磁盘和受控网络环境。它不复刻 Harbor 的项目治理与扫描体系，而是把 OCI Distribution 协议、多上游缓存、本地账号和磁盘治理做完整。</p><div className="flex flex-wrap gap-2 pt-2"><Badge variant="outline">version {system.data?.version || "—"}</Badge><Badge variant="outline">OCI {system.data?.ociSpec || "1.1"}</Badge><Badge variant="outline">started {formatTime(system.data?.startedAt)}</Badge></div></CardContent></Card>
      <Card><CardHeader><div className="flex items-center gap-2"><Server className="h-4 w-4 text-primary" /><CardTitle>边界</CardTitle></div><CardDescription>当前基线明确不包含。</CardDescription></CardHeader><CardContent className="space-y-2 text-sm text-muted-foreground"><p>OIDC / LDAP / SAML</p><p>对象存储与多实例共享缓存</p><p>镜像扫描、复制与策略引擎</p><p>仓库级 ACL、复杂路径重写、Docker schema1</p><p className="pt-2 text-xs">这些能力不应以降低 Registry 协议完整性或代理只读边界为代价。</p></CardContent></Card>
    </div>
    <div className="mt-4 grid gap-4 sm:grid-cols-2 xl:grid-cols-3">{capabilities.map(({ icon: Icon, title, text }) => <Card key={title}><CardContent className="pt-5"><span className="grid h-9 w-9 place-items-center rounded-lg bg-primary/10 text-primary"><Icon className="h-4 w-4" /></span><h2 className="mt-3 font-semibold">{title}</h2><p className="mt-1 text-sm leading-6 text-muted-foreground">{text}</p></CardContent></Card>)}</div>
    <p className="mt-6 flex items-center justify-center gap-2 text-xs text-muted-foreground"><GitBranch className="h-3.5 w-3.5" />Built with Go, React, shadcn/ui and Podman-first acceptance.</p>
  </>;
}
