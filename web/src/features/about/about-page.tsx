import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { ArrowRight, ArrowUpRight, Boxes, Clock3, GitBranch, Globe2, HardDrive, KeyRound, Layers3, ScrollText, Server } from "lucide-react";
import { api } from "@/api/client";
import { CopyButton } from "@/components/common/copy-button";
import { PageHeader } from "@/components/layout/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Link } from "@/lib/router";
import { formatTime } from "@/lib/utils";

const capabilities = [
  { icon: Boxes, title: "私有镜像仓库", text: "使用标准 Docker / OCI 客户端推送、拉取和删除团队镜像，统一管理镜像与标签。" },
  { icon: Layers3, title: "多上游代理缓存", text: "代理 Docker Hub、GHCR、Quay、Kubernetes 镜像仓库等来源，重复拉取可直接使用本地缓存。" },
  { icon: KeyRound, title: "账号与访问控制", text: "通过本地账号、访问令牌和匿名访问策略，控制谁可以拉取、推送和管理镜像。" },
  { icon: HardDrive, title: "存储与容量管理", text: "随时查看磁盘占用、缓存使用和上传会话，及时清理空间并处理容量风险。" },
  { icon: ScrollText, title: "审计与运行状态", text: "集中查看关键操作记录、请求趋势和上游健康状态，便于日常运维与问题定位。" },
  { icon: Globe2, title: "标准客户端兼容", text: "兼容 Docker、Podman、containerd、nerdctl 等标准 OCI 客户端，无需改造现有工作流。" },
];

const scenarios = [
  "在隔离内网或离线环境中分发镜像",
  "为 CI/CD 构建提供稳定、快速的依赖拉取缓存",
  "为多个集群或团队提供统一的镜像访问入口",
];

const shortcuts = [
  { href: "/local", icon: Boxes, title: "本地镜像", detail: "查看和管理团队镜像" },
  { href: "/cache", icon: Layers3, title: "代理缓存", detail: "检查已缓存的远程镜像" },
  { href: "/upstreams", icon: Globe2, title: "上游仓库", detail: "配置镜像来源与凭证" },
  { href: "/storage", icon: HardDrive, title: "存储与磁盘", detail: "查看容量和清理空间" },
];

export function AboutPage() {
  const system = useQuery({ queryKey: ["system", "info"], queryFn: api.systemInfo, staleTime: 60_000 });
  const info = system.data;
  const managementURL = info?.managementURL || info?.publicURL || window.location.origin;
  const registryURL = info?.registryURL || window.location.origin;

  return <>
    <PageHeader
      title="关于 CRProxy"
      description="轻量、自托管的 OCI 镜像仓库与多上游代理缓存。"
      actions={<Button asChild variant="outline" size="sm"><a href="https://codeberg.org/aido/crproxy" target="_blank" rel="noreferrer">项目仓库<ArrowUpRight className="h-4 w-4" /></a></Button>}
    />

    <div className="grid gap-4 lg:grid-cols-[minmax(0,1.35fr)_minmax(300px,0.65fr)]">
      <Card className="overflow-hidden">
        <CardHeader className="border-b bg-gradient-to-br from-primary/10 via-background to-background">
          <div className="flex items-start gap-4">
            <div className="grid h-12 w-12 shrink-0 place-items-center rounded-xl bg-primary text-lg font-bold text-primary-foreground">C</div>
            <div className="min-w-0">
              <CardTitle className="text-xl">CRProxy</CardTitle>
              <CardDescription className="mt-1">私有镜像仓库与多上游按需缓存</CardDescription>
            </div>
          </div>
        </CardHeader>
        <CardContent className="space-y-4 pt-5">
          <p className="max-w-2xl text-sm leading-6 text-muted-foreground">CRProxy 为团队提供统一的镜像入口：既可以保存和分发私有镜像，也可以按需缓存公共上游镜像，让内网、边缘节点和 CI/CD 环境更快、更稳定地获取容器镜像。</p>
          <div className="flex flex-wrap gap-2">
            <Badge variant="outline">版本 {info?.version || "—"}</Badge>
            <Badge variant="secondary">OCI {info?.ociSpec || "1.1"}</Badge>
            <Badge variant="outline">标准镜像客户端</Badge>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <div className="flex items-center gap-2"><Server className="h-4 w-4 text-primary" /><CardTitle>服务信息</CardTitle></div>
          <CardDescription>当前实例的基本运行信息。</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="min-w-0 rounded-lg border bg-muted/15 p-3">
            <p className="text-xs text-muted-foreground">管理台地址</p>
            <div className="mt-1 flex items-center justify-between gap-2">
              <code className="min-w-0 break-all font-mono text-xs">{managementURL}</code>
              <CopyButton value={managementURL} label="复制管理台地址" className="h-7 shrink-0 px-1.5" />
            </div>
          </div>
          <div className="min-w-0 rounded-lg border bg-muted/15 p-3">
            <p className="text-xs text-muted-foreground">镜像仓库地址</p>
            <div className="mt-1 flex items-center justify-between gap-2">
              <code className="min-w-0 break-all font-mono text-xs">{registryURL}</code>
              <CopyButton value={registryURL} label="复制镜像仓库地址" className="h-7 shrink-0 px-1.5" />
            </div>
          </div>
          <dl className="grid grid-cols-2 gap-3 text-sm">
            <Info label="版本" value={info?.version || "—"} />
            <Info label="OCI 规范" value={info?.ociSpec || "—"} />
            <Info label="启动时间" value={formatTime(info?.startedAt)} icon={<Clock3 className="h-3.5 w-3.5" />} />
            <Info label="客户端" value="Docker / Podman" />
          </dl>
        </CardContent>
      </Card>
    </div>

    <div className="mt-6">
      <div className="mb-3">
        <h2 className="text-base font-semibold">主要能力</h2>
        <p className="mt-1 text-sm text-muted-foreground">围绕镜像分发、缓存与日常运维提供的核心功能。</p>
      </div>
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
        {capabilities.map(({ icon: Icon, title, text }) => <Card key={title}><CardContent className="pt-5"><span className="grid h-9 w-9 place-items-center rounded-lg bg-primary/10 text-primary"><Icon className="h-4 w-4" /></span><h3 className="mt-3 font-semibold">{title}</h3><p className="mt-1 text-sm leading-6 text-muted-foreground">{text}</p></CardContent></Card>)}
      </div>
    </div>

    <div className="mt-4 grid gap-4 lg:grid-cols-2">
      <Card>
        <CardHeader><CardTitle>适用场景</CardTitle><CardDescription>适合需要自建镜像入口、同时希望减少重复外网拉取的团队。</CardDescription></CardHeader>
        <CardContent className="space-y-3">
          {scenarios.map((scenario) => <div key={scenario} className="flex items-start gap-3 rounded-lg border bg-muted/15 p-3 text-sm"><span className="mt-1 h-1.5 w-1.5 shrink-0 rounded-full bg-primary" /><span>{scenario}</span></div>)}
        </CardContent>
      </Card>
      <Card>
        <CardHeader><CardTitle>快速入口</CardTitle><CardDescription>直接前往最常用的管理页面。</CardDescription></CardHeader>
        <CardContent className="grid gap-2 sm:grid-cols-2">
          {shortcuts.map(({ href, icon: Icon, title, detail }) => <Link key={href} href={href} className="group flex min-w-0 items-center gap-3 rounded-lg border p-3 transition-colors hover:bg-accent"><span className="grid h-9 w-9 shrink-0 place-items-center rounded-lg bg-primary/10 text-primary"><Icon className="h-4 w-4" /></span><span className="min-w-0 flex-1"><span className="block text-sm font-medium">{title}</span><span className="block truncate text-xs text-muted-foreground">{detail}</span></span><ArrowRight className="h-4 w-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5" /></Link>)}
        </CardContent>
      </Card>
    </div>

    <p className="mt-6 flex items-center justify-center gap-2 text-center text-xs text-muted-foreground"><GitBranch className="h-3.5 w-3.5" />CRProxy · 专注于私有镜像仓库、多上游缓存与轻量运维</p>
  </>;
}

function Info({ label, value, icon }: { label: string; value: string; icon?: ReactNode }) {
  return <div className="min-w-0 rounded-lg border bg-muted/15 p-3"><dt className="flex items-center gap-1.5 text-xs text-muted-foreground">{icon}{label}</dt><dd className="mt-1 truncate font-medium">{value}</dd></div>;
}
