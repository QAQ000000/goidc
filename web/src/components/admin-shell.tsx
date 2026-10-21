"use client";

// Layout, sidebar and account menu adapted from Next Shadcn Dashboard Starter
// v1.0.0: app/dashboard/layout.tsx, components/layout/app-sidebar.tsx and header.tsx.
// MIT; see web/licenses/next-shadcn-dashboard-starter.MIT.
import { useState } from "react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { Collapsible } from "radix-ui";
import { ArrowUpRight, ChevronRight, ChevronsUpDown, Layers3, LogOut, UserRound } from "lucide-react";
import { adminDomain, adminDomains, adminEntry, adminNavigation, adminNavigationItem, can } from "@/lib/admin-navigation";
import type { User } from "@/lib/api";
import { useSite } from "./site-provider";
import { Button } from "./ui/button";
import { Breadcrumb, BreadcrumbItem, BreadcrumbLink, BreadcrumbList, BreadcrumbPage, BreadcrumbSeparator } from "./ui/breadcrumb";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger } from "./ui/dropdown-menu";
import { Separator } from "./ui/separator";
import { Sidebar, SidebarContent, SidebarFooter, SidebarGroup, SidebarGroupLabel, SidebarHeader, SidebarInset, SidebarMenu, SidebarMenuButton, SidebarMenuItem, SidebarMenuSub, SidebarMenuSubButton, SidebarMenuSubItem, SidebarRail, SidebarTrigger, useSidebar } from "./ui/sidebar";

function AppSidebar({ user, logout, busy }: { user: User | null; logout: () => void; busy: boolean }) {
  const pathname = usePathname();
  const current = adminNavigationItem(pathname);
  const domain = adminDomain(pathname);
  const { state, isMobile, setOpenMobile } = useSidebar();
  const { site } = useSite();
  const allowed = adminNavigation.filter(item => can(user, item.permission));
  const close = () => setOpenMobile(false);
  return <Sidebar collapsible="icon">
    <SidebarHeader>
      <SidebarMenu><SidebarMenuItem><SidebarMenuButton size="lg" asChild tooltip="管理控制台首页">
        <Link href={user ? adminEntry(user) : "/admin"} prefetch={false} onClick={close}>
          <span className="flex aspect-square size-8 shrink-0 items-center justify-center rounded-lg bg-sidebar-primary text-sidebar-primary-foreground"><Layers3 className="size-4" /></span>
          <span className="grid flex-1 text-left text-sm leading-tight"><span className="truncate font-semibold">{site?.site_name || "GoIDC"}</span><span className="truncate text-xs text-muted-foreground">管理控制台</span></span>
        </Link>
      </SidebarMenuButton></SidebarMenuItem></SidebarMenu>
    </SidebarHeader>
    <SidebarContent><SidebarGroup>
      <SidebarGroupLabel>工作空间</SidebarGroupLabel>
      <SidebarMenu aria-label="管理导航">
        {adminDomains.map(item => {
          const links = allowed.filter(link => item.groups.includes(link.group));
          if (!links.length) return null;
          const Icon = links[0].icon;
          const active = domain.id === item.id;
          if (links.length === 1 || state === "collapsed" && !isMobile) return <SidebarMenuItem key={item.id}>
            <SidebarMenuButton asChild tooltip={item.label} isActive={active}><Link href={links[0].href} prefetch={false} onClick={close}><Icon /><span>{item.label}</span></Link></SidebarMenuButton>
          </SidebarMenuItem>;
          return <Collapsible.Root asChild key={`${item.id}:${active}`} defaultOpen={active} className="group/collapsible"><SidebarMenuItem>
            <Collapsible.Trigger asChild><SidebarMenuButton tooltip={item.label} isActive={active}><Icon /><span>{item.label}</span><ChevronRight className="ml-auto transition-transform duration-200 group-data-[state=open]/collapsible:rotate-90" /></SidebarMenuButton></Collapsible.Trigger>
            <Collapsible.Content><SidebarMenuSub>{links.map(link => <SidebarMenuSubItem key={link.href}>
              <SidebarMenuSubButton asChild isActive={current?.href === link.href}><Link href={link.href} prefetch={false} aria-current={current?.href === link.href ? "page" : undefined} onClick={close}><span>{link.label}</span></Link></SidebarMenuSubButton>
            </SidebarMenuSubItem>)}</SidebarMenuSub></Collapsible.Content>
          </SidebarMenuItem></Collapsible.Root>;
        })}
      </SidebarMenu>
    </SidebarGroup></SidebarContent>
    <SidebarFooter>
      {user && <SidebarMenu><SidebarMenuItem><DropdownMenu>
        <DropdownMenuTrigger asChild><SidebarMenuButton size="lg" aria-label="管理员账户菜单" className="data-[state=open]:bg-sidebar-accent">
          <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-muted font-medium">{Array.from(user.display_name || user.username)[0] || "A"}</span>
          <span className="grid flex-1 text-left text-sm leading-tight"><span className="truncate font-semibold">{user.display_name || user.username}</span><span className="truncate text-xs text-muted-foreground">{user.email}</span></span><ChevronsUpDown className="ml-auto size-4" />
        </SidebarMenuButton></DropdownMenuTrigger>
        <DropdownMenuContent className="min-w-56 rounded-lg" side={isMobile ? "top" : "right"} align="end" sideOffset={4}>
          <DropdownMenuLabel className="max-w-64 truncate">{user.display_name || user.username}</DropdownMenuLabel><DropdownMenuSeparator />
          <DropdownMenuItem asChild><Link href="/admin/account" prefetch={false} onClick={close}><UserRound />个人账户</Link></DropdownMenuItem>
          <DropdownMenuItem asChild><Link href="/" onClick={close}><ArrowUpRight />查看官网</Link></DropdownMenuItem>
          <DropdownMenuSeparator /><DropdownMenuItem disabled={busy} onSelect={logout}><LogOut />{busy ? "正在退出…" : "退出登录"}</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu></SidebarMenuItem></SidebarMenu>}
    </SidebarFooter>
    <SidebarRail />
  </Sidebar>;
}

export function AdminShell({ children, title, user, error, refresh, logout, logoutError }: {
  children: React.ReactNode; title: string; user: User | null; error: Error | null;
  refresh: () => void; logout: () => Promise<void>; logoutError: string;
}) {
  const path = usePathname();
  const current = adminNavigationItem(path);
  const domain = adminDomain(path);
  const [loggingOut, setLoggingOut] = useState(false);
  async function handleLogout() {
    if (loggingOut) return;
    setLoggingOut(true);
    try { await logout(); } finally { setLoggingOut(false); }
  }
  return <>
    <a href="#admin-main" className="fixed -top-20 left-3 z-[100] rounded border border-ring bg-card px-4 py-2 focus:top-2">跳到主内容</a>
    <AppSidebar user={user} logout={() => void handleLogout()} busy={loggingOut} />
    <SidebarInset className="min-w-0" id="admin-main" tabIndex={-1}>
      <header className="sticky top-0 z-20 flex h-14 shrink-0 items-center justify-between gap-2 border-b bg-background/90 backdrop-blur-md">
        <div className="flex min-w-0 items-center gap-2 px-4">
          <SidebarTrigger className="-ml-1 shrink-0" />
          <Separator orientation="vertical" className="mr-2 data-[orientation=vertical]:h-4" />
          <Breadcrumb><BreadcrumbList className="flex-nowrap">
            <BreadcrumbItem className="hidden shrink-0 md:flex"><BreadcrumbLink asChild><Link href={user ? adminEntry(user) : "/admin"}>{domain.label}</Link></BreadcrumbLink></BreadcrumbItem>
            <BreadcrumbSeparator className="hidden md:block" />
            {current && current.label !== title && <><BreadcrumbItem className="hidden md:flex"><BreadcrumbLink asChild><Link href={current.href}>{current.label}</Link></BreadcrumbLink></BreadcrumbItem><BreadcrumbSeparator className="hidden md:block" /></>}
            <BreadcrumbItem className="min-w-0"><BreadcrumbPage className="truncate">{title}</BreadcrumbPage></BreadcrumbItem>
          </BreadcrumbList></Breadcrumb>
        </div>
        <div className="flex shrink-0 items-center gap-2 px-4">
          <Button asChild variant="ghost" size="sm"><Link href="/"><ArrowUpRight /><span className="hidden sm:inline">查看官网</span><span className="sr-only sm:hidden">查看官网</span></Link></Button>
          {error && <Button variant="outline" size="sm" onClick={refresh}>重试身份读取</Button>}
        </div>
      </header>
      <div className="dashboard-content min-w-0 flex-1 p-4 md:p-6">
        {logoutError && <p role="alert" className="mb-4 text-destructive">{logoutError}</p>}
        {error && <p role="alert" className="mb-4 text-destructive">身份读取失败：{error.message}</p>}
        {children}
      </div>
    </SidebarInset>
  </>;
}
