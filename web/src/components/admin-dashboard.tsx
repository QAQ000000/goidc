"use client";

import Link from "next/link";
import { ArrowRight, BarChart3, ClipboardList, MessageSquare, Package, Receipt, RefreshCw, Server, Users } from "lucide-react";
import { useResource } from "@/hooks/use-resource";
import { money } from "@/lib/api";
import { Button } from "./ui/button";
import { Failure, Loading } from "./common";
import { PermissionGate, Workspace, useAdminPermission } from "./workspace";

type Dashboard = { customers: number; active_products: number; pending_orders: number; unpaid_invoices: number; open_tickets: number; unpaid_cents: number };
type Income = { months: { month: string; income_cents: number; invoice_count: number }[]; total: { income_cents: number; invoice_count: number } };

export function AdminDashboard() {
  return <Workspace title="工作台" admin><DashboardContent /></Workspace>;
}

function DashboardContent() {
  const summary = useResource<Dashboard>("/admin/dashboard");
  const canReport = useAdminPermission("reports.view");
  const year = new Date().getFullYear();
  const income = useResource<Income>(canReport ? `/admin/reports/income?year=${year}` : null);
  const refresh = () => { summary.refresh(); income.refresh(); };
  const stats = summary.data ? [
    { label: "客户总数", value: summary.data.customers, icon: Users, href: "/admin/customers", permission: "customers.view", color: "text-blue-600 bg-blue-50" },
    { label: "在售产品", value: summary.data.active_products, icon: Package, href: "/admin/products", permission: "products.view", color: "text-emerald-600 bg-emerald-50" },
    { label: "待处理订单", value: summary.data.pending_orders, icon: ClipboardList, href: "/admin/orders", permission: "orders.view", color: "text-amber-600 bg-amber-50" },
    { label: "未支付账单", value: summary.data.unpaid_invoices, icon: Receipt, href: "/admin/invoices", permission: "invoices.view", color: "text-red-600 bg-red-50" },
  ] : [];
  return <div className="space-y-5">
    <div className="flex items-center justify-between gap-3"><h1 className="text-lg font-semibold">工作台</h1><Button variant="outline" size="icon" aria-label="刷新工作台" title="刷新工作台" onClick={refresh}><RefreshCw size={16} /></Button></div>
    {summary.error ? <Failure error={summary.error} retry={summary.refresh} /> : !summary.data ? <Loading /> : <>
      <div className="grid grid-cols-2 gap-3 xl:grid-cols-4">{stats.map(item => <section key={item.label} className="data-surface flex min-w-0 items-center gap-4 p-5"><span className={`grid h-11 w-11 shrink-0 place-items-center rounded-md ${item.color}`}><item.icon size={23} /></span><div className="min-w-0"><div className="text-sm text-muted-foreground">{item.label}</div><strong className="mt-1 block text-2xl font-semibold tabular-nums">{item.value.toLocaleString("zh-CN")}</strong><PermissionGate permission={item.permission}><Link href={item.href} className="mt-1 inline-flex items-center gap-1 text-xs text-primary">查看详情<ArrowRight size={12} /></Link></PermissionGate></div></section>)}</div>
      <div className="grid gap-5 xl:grid-cols-[minmax(0,2fr)_minmax(280px,1fr)]">
        <section className="data-surface min-w-0"><SectionHeading title="待办事项" /><div className="divide-y px-5">
          <Todo label="待处理订单" value={summary.data.pending_orders.toLocaleString("zh-CN")} href="/admin/orders" permission="orders.view" icon={ClipboardList} />
          <Todo label="未支付账单" value={`${summary.data.unpaid_invoices} 笔`} href="/admin/invoices" permission="invoices.view" icon={Receipt} />
          <Todo label="待收金额" value={money(summary.data.unpaid_cents)} href="/admin/invoices" permission="invoices.view" icon={Receipt} />
          <PermissionGate permission="tickets.view"><Todo label="待处理工单" value={`${summary.data.open_tickets} 个`} href="/admin/tickets" permission="tickets.view" icon={MessageSquare} /></PermissionGate>
        </div></section>
        <section className="data-surface min-w-0"><SectionHeading title="快捷入口" /><div className="grid grid-cols-2 gap-1 p-3">{[
          { label: "客户管理", href: "/admin/customers", permission: "customers.view", icon: Users },
          { label: "订单管理", href: "/admin/orders", permission: "orders.view", icon: ClipboardList },
          { label: "产品服务", href: "/admin/services", permission: "services.view", icon: Server },
          { label: "账单管理", href: "/admin/invoices", permission: "invoices.view", icon: Receipt },
          { label: "工单管理", href: "/admin/tickets", permission: "tickets.view", icon: MessageSquare },
          { label: "统计报表", href: "/admin/reports", permission: "reports.view", icon: BarChart3 },
        ].map(item => <PermissionGate key={item.href} permission={item.permission}><Link href={item.href} className="flex min-h-20 flex-col items-center justify-center gap-2 rounded-md text-sm text-muted-foreground transition-colors hover:bg-muted hover:text-primary"><item.icon size={22} />{item.label}</Link></PermissionGate>)}</div></section>
      </div>
    </>}
    <PermissionGate permission="reports.view"><section className="data-surface min-w-0"><SectionHeading title={`${year} 年收入统计`} href="/admin/reports" />{income.error ? <div className="p-5"><Failure error={income.error} retry={income.refresh} /></div> : !income.data ? <div className="p-5"><Loading /></div> : <div className="p-5"><div className="mb-6 flex flex-wrap gap-x-10 gap-y-3"><div className="text-sm text-muted-foreground">年度收入<strong className="mt-1 block text-2xl font-semibold text-foreground money">{money(income.data.total.income_cents)}</strong></div><div className="text-sm text-muted-foreground">已支付账单<strong className="mt-1 block text-2xl font-semibold text-foreground">{income.data.total.invoice_count.toLocaleString("zh-CN")}</strong></div></div><IncomeChart income={income.data} /></div>}</section></PermissionGate>
  </div>;
}

function SectionHeading({ title, href }: { title: string; href?: string }) {
  return <div className="flex min-h-12 items-center justify-between gap-3 border-b px-5"><h2 className="text-sm font-semibold">{title}</h2>{href && <Link href={href} className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-primary">查看报表<ArrowRight size={12} /></Link>}</div>;
}

function Todo({ label, value, href, permission, icon: Icon }: { label: string; value: string; href: string; permission: string; icon: typeof Users }) {
  return <div className="flex min-h-14 items-center gap-3"><Icon size={17} className="shrink-0 text-muted-foreground" /><span className="text-sm">{label}</span><strong className="ml-auto text-sm tabular-nums">{value}</strong><PermissionGate permission={permission}><Link href={href} aria-label={`查看${label}`} title={`查看${label}`} className="grid h-8 w-8 shrink-0 place-items-center text-muted-foreground hover:text-primary"><ArrowRight size={16} /></Link></PermissionGate></div>;
}

function IncomeChart({ income }: { income: Income }) {
  const months = Array.from({ length: 12 }, (_, index) => {
    const month = income.months.find(item => Number(item.month.slice(-2)) === index + 1);
    return { label: `${index + 1} 月`, value: month?.income_cents ?? 0 };
  });
  const max = Math.max(...months.map(item => item.value), 1);
  return <div className="overflow-x-auto"><div className="min-w-[540px]" role="img" aria-label={`月度收入：${months.map(item => `${item.label} ${money(item.value)}`).join("，")}`}><div className="grid h-40 grid-cols-12 items-end gap-3 border-b bg-[repeating-linear-gradient(to_top,transparent,transparent_39px,var(--border)_40px)] px-2">{months.map(item => <div key={item.label} className="flex h-full items-end justify-center"><div title={`${item.label}：${money(item.value)}`} className="w-full max-w-10 rounded-t-sm bg-blue-500/80" style={{ height: item.value === 0 ? 0 : `${Math.max(item.value / max * 100, 2)}%` }} /></div>)}</div><div className="mt-3 grid grid-cols-12 gap-3 px-2 text-center text-xs text-muted-foreground">{months.map(item => <span key={item.label}>{item.label}</span>)}</div>{income.total.invoice_count === 0 && <p className="mt-4 text-center text-sm text-muted-foreground">暂无收入记录</p>}</div></div>;
}
