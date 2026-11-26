import { useEffect, type ReactNode } from "react";
import { Loader2 } from "lucide-react";
import { AppShell } from "@/components/layout/app-shell";
import { ForbiddenState, NotFoundState } from "@/components/feedback/forbidden-state";
import { AboutPage } from "@/features/about/about-page";
import { AccountPage } from "@/features/account/account-page";
import { AuditPage } from "@/features/audit/audit-page";
import { CacheEntriesPage } from "@/features/cache/cache-entries-page";
import { ChangePasswordPage } from "@/features/auth/change-password-page";
import { LoginPage } from "@/features/auth/login-page";
import { useSessionQuery, useUnauthorizedRedirect } from "@/features/auth/use-session";
import { OverviewPage } from "@/features/overview/overview-page";
import { CacheRepositoriesPage, LocalRepositoriesPage } from "@/features/repositories/repositories-page";
import { RepositoryDetailPage } from "@/features/repositories/repository-detail-page";
import { SettingsPage } from "@/features/settings/settings-page";
import { StoragePage } from "@/features/storage/storage-page";
import { UpstreamsPage } from "@/features/upstreams/upstreams-page";
import { UploadsPage } from "@/features/uploads/uploads-page";
import { UsersPage } from "@/features/users/users-page";
import { navigate, usePathname } from "@/lib/router";

const pageTitles: Record<string, string> = { "/": "总览", "/local": "本地镜像", "/cache": "代理缓存", "/upstreams": "上游仓库", "/entries": "缓存条目", "/storage": "存储与磁盘", "/users": "用户管理", "/audit": "审计日志", "/uploads": "上传会话", "/settings": "系统设置", "/about": "关于", "/account": "账户与 Token", "/account/password": "修改密码", "/login": "登录" };

export function App() {
  const session = useSessionQuery();
  const pathname = usePathname();
  useUnauthorizedRedirect();
  useEffect(() => {
    document.title = `${pageTitles[pathname] ?? (pathname.startsWith("/repositories/") ? "仓库详情" : "CRProxy")} · CRProxy`;
  }, [pathname]);
  useEffect(() => {
    if (!session.isLoading && !session.data && pathname !== "/login") navigate("/login", { replace: true });
    if (!session.isLoading && session.data && pathname === "/login") navigate("/", { replace: true });
  }, [session.isLoading, session.data, pathname]);

  if (session.isLoading) return <div className="grid min-h-screen place-items-center text-muted-foreground"><Loader2 className="h-5 w-5 animate-spin" /><span className="sr-only">正在加载</span></div>;
  if (pathname === "/login" || !session.data) return <LoginPage />;
  if (session.data.user.mustChangePassword) {
    return <div className="min-h-screen bg-background p-4 sm:p-8"><ChangePasswordPage initial /></div>;
  }
  const role = session.data.user.role;
  const canManage = role === "admin" || role === "operator";
  const isAdmin = role === "admin";
  let page: ReactNode;
  if (pathname === "/") page = <OverviewPage />;
  else if (pathname === "/local") page = <LocalRepositoriesPage />;
  else if (pathname === "/cache") page = <CacheRepositoriesPage />;
  else if (pathname === "/upstreams") page = <UpstreamsPage />;
  else if (pathname === "/entries") page = canManage ? <CacheEntriesPage /> : <ForbiddenState />;
  else if (pathname === "/storage") page = canManage ? <StoragePage /> : <ForbiddenState />;
  else if (pathname === "/users") page = isAdmin ? <UsersPage /> : <ForbiddenState />;
  else if (pathname === "/audit") page = canManage ? <AuditPage /> : <ForbiddenState />;
  else if (pathname === "/uploads") page = canManage ? <UploadsPage /> : <ForbiddenState />;
  else if (pathname === "/settings") page = canManage ? <SettingsPage /> : <ForbiddenState />;
  else if (pathname === "/about") page = <AboutPage />;
  else if (pathname === "/account") page = <AccountPage />;
  else if (pathname === "/account/password") page = <ChangePasswordPage />;
  else {
    const match = pathname.match(/^\/repositories\/(\d+)$/);
    page = match && Number(match[1]) > 0 ? <RepositoryDetailPage id={Number(match[1])} /> : <NotFoundState />;
  }
  return <AppShell>{page}</AppShell>;
}
