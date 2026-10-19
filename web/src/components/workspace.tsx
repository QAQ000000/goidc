"use client";
import { createContext, useContext, useEffect, useState, Fragment } from "react";
import Link from "next/link";
import { SiteBrand, SiteFooter } from "./site-provider";
import { usePathname, useRouter } from "next/navigation";
import {
  Package,
  Download,
  ClipboardList,
  Server,
  Receipt,
  WalletCards,
  MessageSquare,
  ShoppingCart,
  Menu,
  LogOut,
  ChevronRight,
  ArrowUpRight,
  X,
  BadgeCheck,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { api, type User } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { adminNavigation, can, adminEntry } from "@/lib/admin-navigation";
import { useServiceNavigation } from "@/hooks/use-service-navigation";
import { serviceIcon } from "@/lib/service-icons";
import { AdminShell } from "./admin-shell";

type AccessContextValue = { user: User | null; error: Error | null; refresh: () => void };
const AccessContext = createContext<AccessContextValue | null>(null);
export function AdminAccessProvider({ initialUser, children }: { initialUser: User; children: React.ReactNode }) {
  const { data, error, refresh } = useResource<User>("/me", false);
  const value = { user: data ?? (error ? null : initialUser), error, refresh };
  return <AccessContext.Provider value={value}>{children}</AccessContext.Provider>;
}
export function useAdminPermission(permission: string) {
  return can(useContext(AccessContext)?.user ?? null, permission);
}
export function useAdminIdentity() {
  return useContext(AccessContext)?.user ?? null;
}
export function PermissionGate({ permission, children, bypass = false }: { permission: string; children: React.ReactNode; bypass?: boolean }) {
  const allowed = useAdminPermission(permission);
  return bypass || allowed ? children : null;
}
export function PermissionFields({ permission, children }: { permission: string; children: React.ReactNode }) {
  return <fieldset disabled={!useAdminPermission(permission)} className="contents">{children}</fieldset>;
}

const customerNav = [
  { href: "/", label: "选购产品", icon: Package },
  { href: "/cart", label: "购物车", icon: ShoppingCart },
  { href: "/console/orders", label: "我的订单", icon: ClipboardList },
  { href: "/console/services", label: "我的服务", icon: Server },
  { href: "/console/invoices", label: "我的账单", icon: Receipt },
  { href: "/console/contracts", label: "我的合同", icon: Receipt },
  { href: "/console/invoice-applications", label: "发票申请", icon: Receipt },
  { href: "/console/account", label: "账户余额", icon: WalletCards },
  { href: "/console/affiliate", label: "推广返利", icon: WalletCards },
  { href: "/console/certification", label: "实名认证", icon: BadgeCheck },
  { href: "/console/tickets", label: "支持工单", icon: MessageSquare },
  { href: "/downloads", label: "下载中心", icon: Download },
  { href: "/console/files", label: "我的文件", icon: Download },
];

export function Workspace({
  children,
  title,
  admin = false,
}: {
  children: React.ReactNode;
  title: string;
  admin?: boolean;
}) {
  const access = useContext(AccessContext);
  const customerResource = useResource<User>(admin ? null : "/me");
  const user = admin ? access?.user ?? null : customerResource.data;
  const error = admin ? access?.error ?? null : customerResource.error;
  const refresh = admin ? access?.refresh ?? customerResource.refresh : customerResource.refresh;
  const path = usePathname();
  const router = useRouter();
  const staff = user?.role === "admin" || user?.role === "super_admin";
  const customerRoute = path === "/console" || path.startsWith("/console/") || path === "/cart" || path.startsWith("/cart/");
  useEffect(() => {
    if (!admin && staff && customerRoute) router.replace("/admin");
  }, [admin, staff, customerRoute, router]);
  const [open, setOpen] = useState(false);
  const [logoutError, setLogoutError] = useState("");
  const { data: impersonation } = useResource<{ active: boolean; admin_name?: string }>("/auth/impersonation", false);
  // Legacy hides the 实名认证 menu entry whenever certifi_open is off; mirror
  // that with the enabled flag from GET /account/certification. Absent data
  // keeps the entry visible so a loading state never hides it by accident.
  const { data: certification } = useResource<{ enabled?: boolean }>(!admin && user?.role === "customer" ? "/account/certification" : null, false);
  const [impersonationBusy, setImpersonationBusy] = useState(false);
  const [impersonationError, setImpersonationError] = useState("");
  const navigation = admin ? adminNavigation.filter(item => can(user, item.permission)) : staff ? customerNav.filter(item => item.href === "/" || item.href === "/downloads").map(item => item.href === "/" ? { ...item, label: "产品目录" } : item) : customerNav.filter(item => item.href !== "/console/certification" || certification?.enabled !== false);
  const groups = useServiceNavigation(!admin && user?.role === "customer");
  async function exitImpersonation() {
    if (impersonationBusy) return;
    setImpersonationBusy(true); setImpersonationError("");
    try {
      await api("/auth/impersonation/exit", { method: "POST" });
      router.push("/admin"); router.refresh();
    } catch (e) {
      setImpersonationError((e as Error).message);
      setImpersonationBusy(false);
    }
  }
  async function logout() {
    if (impersonation?.active) {
      await exitImpersonation();
      return;
    }
    try {
      await api("/auth/logout", { method: "POST" });
      router.push(admin ? "/admin-login" : "/login");
      router.refresh();
    } catch (e) {
      setLogoutError((e as Error).message);
    }
  }
  if (admin) {
    return <AdminShell title={title} user={user} error={error} refresh={refresh} logout={logout} logoutError={logoutError}>{children}</AdminShell>;
  }
  if (staff && customerRoute) {
    return <main className="grid min-h-screen place-items-center p-6"><p role="status">管理员账号仅用于后台管理，正在返回管理后台。</p></main>;
  }
  return (
    <AccessContext.Provider value={{ user, error, refresh }}><div className="workspace">
      {open && (
        <button
          className="fixed inset-0 bg-black/20 z-30 sm:hidden"
          aria-label="关闭菜单"
          onClick={() => setOpen(false)}
        />
      )}
      <aside className={`sidebar ${open ? "open" : ""}`}>
        <Link
          href={admin && user ? adminEntry(user) : "/"}
          className="brand flex-col items-start gap-2"
        >
          <SiteBrand compact />
          <span className="text-xs font-normal text-muted-foreground">
            {admin ? "管理端" : "云服务"}
          </span>
        </Link>
        <button
          className="mobile-menu absolute right-3 top-3 p-2"
          onClick={() => setOpen(false)}
          aria-label="关闭菜单"
        >
          <X size={18} />
        </button>
        <div className="nav-heading">{staff ? "官网预览" : "客户中心"}</div>
        <nav className="min-h-0 overflow-y-auto" aria-label={staff ? "公开导航" : "客户导航"}>
          {navigation.map(({ href, label, icon: Icon }) => (
            <Fragment key={href}><Link
              key={href}
              href={href}
              className={`nav-link ${path === href ? "active" : ""}`}
              aria-current={path === href ? "page" : undefined}
              onClick={() => setOpen(false)}
            >
              <Icon size={18} />
              {label}
            </Link>
            {!admin&&href==="/console/services"&&groups.data?.filter(g=>g.visible).map(g=>{const GroupIcon=serviceIcon(g.icon);const target=`/console/services/group/${g.id}`;return <Link key={g.id} href={target} className={`nav-link pl-8 text-xs ${path===target?"active":""}`} aria-current={path===target?"page":undefined} onClick={()=>setOpen(false)}><GroupIcon size={15}/><span className="min-w-0 truncate flex-1">{g.name}</span><span aria-label={`${g.active_count} 个运行中服务`}>{g.active_count}</span></Link>;})}
            {!admin&&href==="/console/services"&&groups.error&&<button className="text-xs text-destructive px-4 py-2" onClick={groups.refresh}>服务分组加载失败，重试</button>}
            </Fragment>
          ))}
        </nav>
        <div className="mt-auto pt-8 border-t">
          <Link
            href={
              admin
                ? "/"
                : user?.role === "admin" || user?.role === "super_admin"
                  ? "/admin"
                  : "/console/tickets"
            }
            className="nav-link"
          >
            {admin || user?.role === "admin" || user?.role === "super_admin" ? (
              <>
                <ArrowUpRight size={18} />
                {admin ? "查看官网" : "返回管理后台"}
              </>
            ) : (
              <>
                <MessageSquare size={18} />
                联系支持
              </>
            )}
          </Link>
          <div className="px-3 mt-6 text-xs text-muted-foreground max-h-[25dvh] overflow-y-auto">
            <SiteFooter />
          </div>
        </div>
      </aside>
      <div className="min-w-0">
        <header className="topbar">
          <div className="flex items-center gap-3">
            <Button
              className="mobile-menu"
              variant="ghost"
              size="icon"
              aria-label="打开菜单"
              onClick={() => setOpen(true)}
            >
              <Menu size={20} />
            </Button>
            <span className="hidden sm:inline text-muted-foreground">
              {staff ? "官网预览" : "客户中心"}
            </span>
            <ChevronRight
              size={14}
              className="hidden sm:block text-muted-foreground"
            />
            <span className="font-medium">{title}</span>
          </div>
          <div className="flex items-center gap-3">
            {user ? (
              <>
                <span className="text-sm max-w-32 truncate">
                  {user.display_name}
                </span>
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label="退出登录"
                  title="退出登录"
                  onClick={logout}
                >
                  <LogOut size={18} />
                </Button>
              </>
            ) : error ? (
              <Button size="sm" asChild>
                <Link href="/login">登录</Link>
              </Button>
            ) : (
              <span className="text-xs text-muted-foreground">连接中</span>
            )}
          </div>
        </header>
        <main className="content">
          {impersonation?.active && <div className="mb-5 flex flex-wrap items-center justify-between gap-3 border border-amber-300 bg-amber-50 px-4 py-3 text-sm text-amber-950"><span>当前以客户身份查看{impersonation.admin_name ? `，原管理员：${impersonation.admin_name}` : ""}。客户操作会按该客户权限执行。</span><Button type="button" variant="outline" size="sm" disabled={impersonationBusy} onClick={() => void exitImpersonation()}>{impersonationBusy ? "返回中..." : "返回管理后台"}</Button></div>}
          {impersonationError && <p role="alert" className="text-destructive mb-4">{impersonationError}</p>}
          {logoutError && (
            <p role="alert" className="text-destructive mb-4">
              {logoutError}
            </p>
          )}
          {children}
        </main>
      </div>
    </div></AccessContext.Provider>
  );
}
