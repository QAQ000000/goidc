"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { Dialog, Tooltip } from "radix-ui";
import { ArrowUpRight, ChevronDown, ChevronRight, Home, LayoutDashboard, LogOut, Menu, PanelLeftClose, PanelLeftOpen, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { adminDomain, adminDomains, adminEntry, adminNavigation, adminNavigationGroups, adminNavigationItem, can } from "@/lib/admin-navigation";
import type { User } from "@/lib/api";
import { SiteBrand } from "./site-provider";

function Hint({ label, children }: { label: string; children: React.ReactNode }) {
  return <Tooltip.Root><Tooltip.Trigger asChild>{children}</Tooltip.Trigger><Tooltip.Portal><Tooltip.Content side="right" sideOffset={8} className="admin-tooltip">{label}</Tooltip.Content></Tooltip.Portal></Tooltip.Root>;
}

function Navigation({ user, path, close }: { user: User | null; path: string; close?: () => void }) {
  const [collapsed, setCollapsed] = useState<string[]>([]);
  const current = adminNavigationItem(path);
  const allowed = adminNavigation.filter(item => can(user, item.permission));
  const domain = adminDomain(path);
  return <nav className="admin-navigation" aria-label="管理导航">
    {adminNavigationGroups.map(group => {
      const items = allowed.filter(item => item.group === group.id);
      if (!items.length || (!close && !domain.groups.includes(group.id))) return null;
      const expanded = !collapsed.includes(group.id);
      return <div className="admin-nav-group" key={group.id}>
        <button className="admin-group-toggle" aria-expanded={expanded} aria-controls={`admin-nav-${close ? "mobile" : "desktop"}-${group.id}`} onClick={() => setCollapsed(previous => expanded ? [...previous, group.id] : previous.filter(id => id !== group.id))}>
          <span>{group.label}</span><ChevronDown size={12} className={expanded ? "" : "-rotate-90"} />
        </button>
        <div id={`admin-nav-${close ? "mobile" : "desktop"}-${group.id}`} className={`admin-group-items ${expanded ? "" : "collapsed"}`}>
          {items.map(({ href, label, icon: Icon }) => <Hint key={href} label={label}><Link href={href} className={`admin-nav-link ${current?.href === href ? "active" : ""}`} aria-current={current?.href === href ? "page" : undefined} aria-label={label} onClick={close}>
            <Icon size={18} aria-hidden="true" /><span className="admin-nav-label">{label}</span>
          </Link></Hint>)}
        </div>
      </div>;
    })}
    {!user && <p className="admin-nav-loading" role="status">正在读取导航</p>}
  </nav>;
}

function Brand({ user }: { user: User | null }) {
  return <Link className="admin-brand" href={user ? adminEntry(user) : "/admin"} aria-label="管理控制台首页">
    <img
      src="/assets/admin-header-logo.png"
      alt="智简魔方"
      className="admin-header-logo h-[30px] w-auto max-w-[150px] object-contain"
    />
    <span className="admin-brand-full sr-only"><SiteBrand compact /></span>
    <LayoutDashboard className="admin-brand-compact" size={24} aria-hidden="true" />
  </Link>;
}

export function AdminShell({ children, title, user, error, refresh, logout, logoutError }: {
  children: React.ReactNode;
  title: string;
  user: User | null;
  error: Error | null;
  refresh: () => void;
  logout: () => Promise<void>;
  logoutError: string;
}) {
  const path = usePathname();
  const current = adminNavigationItem(path);
  const group = adminNavigationGroups.find(item => item.id === current?.group);
  const domain = adminDomain(path);
  const home = path === "/admin";
  const [compact, setCompact] = useState(false);
  const [drawer, setDrawer] = useState(false);

  useEffect(() => {
    const media = window.matchMedia("(min-width: 768px)");
    const closeOnDesktop = () => { if (media.matches) setDrawer(false); };
    media.addEventListener("change", closeOnDesktop);
    return () => media.removeEventListener("change", closeOnDesktop);
  }, []);

  return <Tooltip.Provider delayDuration={300}><div className={`admin-shell mf-admin-shell ${home ? "is-home" : ""} ${compact ? "is-compact" : ""}`}>
    <div className="admin-global-header">
      <Brand user={user} />
      <nav className="admin-domain-navigation" aria-label="业务域导航">
        {adminDomains.map(item => {
          const entry = adminNavigation.find(link => link.href === item.href && can(user, link.permission)) ?? adminNavigation.find(link => item.groups.includes(link.group) && can(user, link.permission));
          if (!entry) return null;
          return <Link key={item.id} href={entry.href} className={domain.id === item.id ? "active" : ""} aria-current={domain.id === item.id ? "true" : undefined}>{item.label}</Link>;
        })}
      </nav>
      <div className="admin-global-account">
        <img src="/assets/country/CN.png" alt="CN" className="h-4 w-6 rounded-sm object-cover hidden sm:inline-block" />
        {user ? <>
          <span className="admin-avatar" aria-hidden="true">{Array.from(user.display_name)[0] || "A"}</span>
          <Link href="/admin/account" className="admin-user-name" title="个人账户">{user.display_name}</Link>
          <Link href="/admin" className="admin-header-home-btn text-white/80 hover:text-white hidden sm:inline-flex items-center" title="工作台" aria-label="工作台">
            <Home size={18} />
          </Link>
          <Button className="admin-front-button text-white hover:bg-white/10" variant="ghost" size="sm" asChild><Link href="/"><ArrowUpRight size={14} />查看官网</Link></Button>
          <Button className="text-white hover:bg-white/10" variant="ghost" size="icon" aria-label="退出登录" title="退出登录" onClick={() => void logout()}><LogOut size={16} /></Button>
        </> : error ? <Button size="sm" variant="outline" onClick={refresh}>重试身份读取</Button> : <span className="text-xs text-white/80" role="status">连接中</span>}
      </div>
    </div>
    <aside className="admin-sidebar">
      <div className="admin-sidebar-domain">{domain.label}</div>
      <Navigation key={path} user={user} path={path} />
      <Hint label="查看官网"><Link href="/" className="admin-front-link"><ArrowUpRight size={18} /><span className="admin-nav-label">查看官网</span></Link></Hint>
    </aside>
    <div className="admin-workarea">
      <header className="admin-topbar">
        <div className="admin-topbar-leading">
          <Button className="admin-collapse-button" variant="ghost" size="icon" title={compact ? "展开导航" : "收起导航"} aria-label={compact ? "展开导航" : "收起导航"} aria-expanded={!compact} onClick={() => setCompact(value => !value)}>{compact ? <PanelLeftOpen /> : <PanelLeftClose />}</Button>
          <Dialog.Root open={drawer} onOpenChange={setDrawer}>
            <Dialog.Trigger asChild><Button className="admin-menu-button" variant="ghost" size="icon" aria-label="打开管理菜单"><Menu /></Button></Dialog.Trigger>
            <Dialog.Portal><Dialog.Overlay className="admin-drawer-overlay" /><Dialog.Content className="admin-drawer" aria-describedby={undefined}>
              <Dialog.Title className="sr-only">管理导航</Dialog.Title>
              <div className="admin-drawer-header"><Brand user={user} /><Dialog.Close asChild><Button variant="ghost" size="icon" aria-label="关闭管理菜单"><X /></Button></Dialog.Close></div>
              <Navigation key={path} user={user} path={path} close={() => setDrawer(false)} />
              <Link href="/" className="admin-front-link" onClick={() => setDrawer(false)}><ArrowUpRight size={18} />查看官网</Link>
            </Dialog.Content></Dialog.Portal>
          </Dialog.Root>
          <nav className="admin-breadcrumbs" aria-label="面包屑">
            {group && <><span className="admin-breadcrumb-group">{group.label}</span><ChevronRight className="admin-breadcrumb-group" size={14} aria-hidden="true" /></>}
            {current && current.label !== title ? <><Link href={current.href}>{current.label}</Link><ChevronRight size={14} aria-hidden="true" /></> : null}
            <span aria-current="page">{title}</span>
          </nav>
        </div>
      </header>
      <main className="admin-content">
        {logoutError && <p role="alert" className="text-destructive mb-4">{logoutError}</p>}
        {error && <p role="alert" className="text-destructive mb-4">身份读取失败：{error.message}</p>}
        {children}
      </main>
    </div>
  </div></Tooltip.Provider>;
}
