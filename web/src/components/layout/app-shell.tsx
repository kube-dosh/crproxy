import { useEffect, useRef, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Activity, Boxes, ChevronsLeft, ChevronsRight, Database, HardDrive, Info, KeyRound, Layers3, LayoutDashboard, LogOut, Menu, Moon, ScrollText, Settings2, Sun, UploadCloud, Users, X, type LucideIcon } from "lucide-react";
import { api } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { useSessionQuery } from "@/features/auth/use-session";
import { navigate, usePathname } from "@/lib/router";
import { useTheme } from "@/lib/theme";
import { cn } from "@/lib/utils";

type NavItem = { href: string; label: string; icon: LucideIcon; manageOnly?: boolean; adminOnly?: boolean };

const primaryNav: NavItem[] = [
  { href: "/", label: "总览", icon: LayoutDashboard },
  { href: "/local", label: "本地镜像", icon: Boxes },
  { href: "/cache", label: "代理缓存", icon: Layers3 },
  { href: "/upstreams", label: "上游仓库", icon: Database },
  { href: "/entries", label: "缓存条目", icon: Activity, manageOnly: true },
  { href: "/storage", label: "存储与磁盘", icon: HardDrive, manageOnly: true },
];

const secondaryNav: NavItem[] = [
  { href: "/users", label: "用户与 Token", icon: Users, adminOnly: true },
  { href: "/audit", label: "审计日志", icon: ScrollText, manageOnly: true },
  { href: "/uploads", label: "上传会话", icon: UploadCloud, manageOnly: true },
  { href: "/settings", label: "系统设置", icon: Settings2, manageOnly: true },
  { href: "/about", label: "关于", icon: Info },
];

export function AppShell({ children }: { children: ReactNode }) {
  const session = useSessionQuery();
  const pathname = usePathname();
  const queryClient = useQueryClient();
  const { theme, toggleTheme } = useTheme();
  const [mobileOpen, setMobileOpen] = useState(false);
  const [collapsed, setCollapsed] = useState(() => localStorage.getItem("crproxy-sidebar-collapsed") === "true");
  const menuRef = useRef<HTMLButtonElement>(null);
  const user = session.data?.user;
  const canManage = user?.role === "admin" || user?.role === "operator";
  const isAdmin = user?.role === "admin";

  useEffect(() => {
    localStorage.setItem("crproxy-sidebar-collapsed", String(collapsed));
  }, [collapsed]);
  useEffect(() => setMobileOpen(false), [pathname]);
  useEffect(() => {
    if (!mobileOpen) return;
    const previous = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") { setMobileOpen(false); window.setTimeout(() => menuRef.current?.focus(), 0); }
    };
    window.addEventListener("keydown", onKey);
    return () => { document.body.style.overflow = previous; window.removeEventListener("keydown", onKey); };
  }, [mobileOpen]);

  const logout = async () => {
    await api.logout().catch(() => undefined);
    queryClient.clear();
    navigate("/login", { replace: true });
  };

  const visible = (item: NavItem) => (!item.adminOnly || isAdmin) && (!item.manageOnly || canManage);
  const renderNav = (items: NavItem[]) => items.filter(visible).map((item) => {
    const active = item.href === "/" ? pathname === "/" : pathname.startsWith(item.href);
    const Icon = item.icon;
    return <a key={item.href} href={item.href} aria-current={active ? "page" : undefined} onClick={(event) => { event.preventDefault(); navigate(item.href); }} className={cn("flex h-9 items-center gap-3 rounded-md px-3 text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring", active ? "bg-primary/10 font-medium text-primary" : "text-muted-foreground hover:bg-muted hover:text-foreground", collapsed && "lg:justify-center lg:px-0")} title={collapsed ? item.label : undefined}><Icon className="h-4 w-4 shrink-0" />{!collapsed ? <span className="truncate">{item.label}</span> : null}</a>;
  });

  return (
    <div className="flex min-h-screen w-full min-w-0 flex-col bg-background">
      <header className="sticky top-0 z-40 flex h-14 shrink-0 items-center border-b bg-card/95 px-3 backdrop-blur sm:px-5">
        <Button ref={menuRef} variant="ghost" size="icon" className="mr-2 lg:hidden" onClick={() => setMobileOpen(true)} aria-label="打开导航"><Menu className="h-5 w-5" /></Button>
        <a href="/" onClick={(event) => { event.preventDefault(); navigate("/"); }} className="flex min-w-0 items-center gap-2.5">
          <span className="grid h-8 w-8 place-items-center rounded-lg bg-primary text-sm font-bold text-primary-foreground">C</span>
          <span className="min-w-0"><span className="block truncate text-sm font-semibold leading-4">CRProxy</span><span className="block truncate text-[10px] uppercase tracking-[0.14em] text-muted-foreground">OCI Registry</span></span>
        </a>
        <div className="ml-auto flex items-center gap-1.5">
          <Badge variant={session.data ? "success" : "secondary"} className="hidden sm:inline-flex">{session.data ? "已连接" : "未连接"}</Badge>
          <Button variant="ghost" size="icon" onClick={toggleTheme} aria-label="切换主题">{theme === "dark" ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}</Button>
          <Button variant="ghost" size="sm" onClick={() => navigate("/account")} className="max-w-40"><KeyRound className="h-4 w-4" /><span className="truncate">{user?.username ?? "账户"}</span><Badge variant="outline" className="hidden sm:inline-flex">{roleLabel(user?.role)}</Badge></Button>
          <Button variant="ghost" size="icon" onClick={() => void logout()} aria-label="退出登录"><LogOut className="h-4 w-4" /></Button>
        </div>
      </header>
      <div className="flex min-h-0 w-full min-w-0 flex-1">
        <aside id="app-navigation" aria-label="主导航" className={cn("fixed inset-y-0 left-0 z-50 flex w-60 shrink-0 flex-col border-r bg-card transition-transform lg:static lg:z-30 lg:h-auto lg:translate-x-0", collapsed && "lg:w-[4.25rem]", mobileOpen ? "visible translate-x-0" : "invisible -translate-x-full lg:visible")}>
          <div className="flex h-14 shrink-0 items-center justify-between border-b px-4 lg:hidden"><span className="font-semibold">导航</span><Button variant="ghost" size="icon" onClick={() => setMobileOpen(false)} aria-label="关闭导航"><X className="h-4 w-4" /></Button></div>
          <nav className="min-h-0 flex-1 space-y-1 overflow-y-auto p-3">
            <p className={cn("px-3 pb-2 pt-1 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground", collapsed && "lg:text-center")}>{collapsed ? "•" : "Registry"}</p>
            {renderNav(primaryNav)}
            <div className="pt-4"><p className={cn("px-3 pb-2 text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground", collapsed && "lg:text-center")}>{collapsed ? "•" : "Operations"}</p>{renderNav(secondaryNav)}</div>
          </nav>
          <div className="hidden shrink-0 border-t p-3 lg:block"><Button variant="ghost" size="sm" className={cn("w-full text-muted-foreground", collapsed ? "px-0" : "justify-start")} onClick={() => setCollapsed((value) => !value)}>{collapsed ? <ChevronsRight className="h-4 w-4" /> : <><ChevronsLeft className="h-4 w-4" />收起侧栏</>}</Button></div>
        </aside>
        {mobileOpen ? <button type="button" aria-label="关闭导航遮罩" onClick={() => setMobileOpen(false)} className="fixed inset-0 z-40 bg-slate-950/35 lg:hidden" /> : null}
        <main className="min-h-0 w-full min-w-0 flex-1 overflow-x-hidden p-4 sm:p-6 lg:p-8"><div className="mx-auto w-full max-w-[1480px] animate-fade-in">{children}</div></main>
      </div>
    </div>
  );
}

export function roleLabel(role?: string) {
  return role === "admin" ? "管理员" : role === "operator" ? "运维" : role === "viewer" ? "只读" : "—";
}
