"use client";

import { useState, type ReactNode } from "react";
import Link from "next/link";
import { ArrowUpRight, ClipboardList, FileText, MessageSquare, RefreshCw, ShieldCheck, WalletCards, type LucideIcon } from "lucide-react";
import { useResource } from "@/hooks/use-resource";
import { ApiError, money } from "@/lib/api";
import { can } from "@/lib/admin-navigation";
import { cn } from "@/lib/utils";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Skeleton } from "./ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";
import { Workspace, useAdminIdentity } from "./workspace";

type DashboardSummary = {
  pending_orders: number; unpaid_invoices: number; open_tickets: number;
  today_paid_orders: number; month_paid_orders: number; month_income_cents: number;
  pending_certifications: number; pending_withdrawals: number; today_new_clients: number;
};
type ProductRanking = {
  product_id: number; name: string; new_order_count: number; new_quantity: number;
  renewal_order_count: number; renewal_quantity: number; total_amount_cents: number;
};
type StaffRanking = {
  sales_owner_id: number; sales_owner_name: string; customer_count: number; invoice_count: number; amount_cents: number;
};
type Ranking<T> = { items: T[]; total: number; month: string; limit: number; currency: string; timezone: string };

const panelClass = "min-w-0 rounded border border-border bg-card text-card-foreground";
const compactControl = "h-11 rounded shadow-none md:h-9";

function shanghaiMonth() {
  const parts = new Intl.DateTimeFormat("en", { timeZone: "Asia/Shanghai", year: "numeric", month: "2-digit" }).formatToParts(new Date());
  return `${parts.find(part => part.type === "year")!.value}-${parts.find(part => part.type === "month")!.value}`;
}
function updateLabel(timestamp: number | null) {
  if (!timestamp) return "尚未更新";
  return `更新于 ${new Intl.DateTimeFormat("zh-CN", { timeZone: "Asia/Shanghai", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }).format(timestamp)}`;
}
function SectionLoading({ label, rows = 3 }: { label: string; rows?: number }) {
  return <div role="status" aria-label={label} className="space-y-3 p-5"><span className="sr-only">{label}</span>{Array.from({ length: rows }, (_, index) => <Skeleton key={index} className="h-9 w-full rounded" />)}</div>;
}
function SectionError({ error, retry, loading }: { error: Error; retry: () => void; loading: boolean }) {
  return <div role="alert" className="space-y-3 p-5 text-sm">
    <p className="break-words text-destructive">{error instanceof ApiError && error.status === 403 ? "当前账号无权查看此区域，请联系管理员核对权限。" : error.message}</p>
    <Button type="button" variant="outline" disabled={loading} onClick={retry} className={compactControl}><RefreshCw aria-hidden="true" className={cn("size-4", loading && "animate-spin")} />{loading ? "正在重试" : "重新加载"}</Button>
  </div>;
}
function PermissionNotice({ children }: { children: ReactNode }) {
  return <p role="status" className="p-5 text-sm text-muted-foreground">{children}</p>;
}

export function AdminDashboard() {
  const user = useAdminIdentity();
  const accessKey = JSON.stringify([
    user?.id, user?.role, user?.permissions, user?.customer_scope,
    user?.customer_ownerless, user?.ticket_scope, user?.ticket_unassigned,
  ]);
  return <Workspace title="工作台" admin><DashboardContent key={accessKey} /></Workspace>;
}

function DashboardContent() {
  const user = useAdminIdentity();
  const canViewDashboard = can(user, "dashboard.view");
  const canViewReports = can(user, "reports.view");
  const summary = useResource<DashboardSummary>(canViewDashboard ? "/admin/dashboard" : null, false);
  const [month, setMonth] = useState(shanghaiMonth);
  const [monthDraft, setMonthDraft] = useState(month);
  const [limit, setLimit] = useState("5");
  const [maximumMonth] = useState(() => `${Number(shanghaiMonth().slice(0, 4)) + 1}-12`);
  const validMonth = /^\d{4}-(0[1-9]|1[0-2])$/.test(monthDraft) && monthDraft >= "2000-01" && monthDraft <= maximumMonth;
  const rankingQuery = `month=${month}&limit=${limit}`;
  const products = useResource<Ranking<ProductRanking>>(canViewReports ? `/admin/reports/product-ranking?${rankingQuery}` : null, false);
  const staff = useResource<Ranking<StaffRanking>>(canViewReports ? `/admin/reports/staff-ranking?${rankingQuery}` : null, false);
  const refreshing = summary.isLoading || products.isLoading || staff.isLoading;
  const refreshAll = () => { summary.refresh(); products.refresh(); staff.refresh(); };
  const metrics = [
    { label: "今日已付订单", value: summary.data?.today_paid_orders.toLocaleString("zh-CN"), description: "按今日创建时间统计" },
    { label: "本月已付订单", value: summary.data?.month_paid_orders.toLocaleString("zh-CN"), description: "按本月创建时间统计" },
    { label: "本月业务收款", value: summary.data ? money(summary.data.month_income_cents) : undefined, description: "含余额业务消费，排除充值 · CNY" },
    { label: "今日新增客户", value: summary.data?.today_new_clients.toLocaleString("zh-CN"), description: "按上海自然日统计" },
  ];
  const todos: { label: string; count: number | undefined; permission: string; href: string; action: string; description: string; icon: LucideIcon }[] = [
    { label: "待处理工单", count: summary.data?.open_tickets, permission: "tickets.view", href: "/admin/tickets", action: "查看工单", description: "处理中与待回复，不含合并来源；进入全部工单列表", icon: MessageSquare },
    { label: "待付款订单", count: summary.data?.pending_orders, permission: "orders.view", href: "/admin/orders?status=pending", action: "查看待付款订单", description: "状态为待付款的订单", icon: ClipboardList },
    { label: "待付款账单", count: summary.data?.unpaid_invoices, permission: "invoices.view", href: "/admin/invoices?status=unpaid", action: "查看待付款账单", description: "状态为未付款的账单", icon: FileText },
    { label: "待审核实名", count: summary.data?.pending_certifications, permission: "certifications.view", href: "/admin/certifications", action: "进入实名审核", description: "进入实名列表后选择待审核", icon: ShieldCheck },
    { label: "待审核提现", count: summary.data?.pending_withdrawals, permission: "affiliates.view", href: "/admin/affiliate", action: "进入推广管理", description: "已提交与审核中；进入推广后选择提现", icon: WalletCards },
  ];
  return <div className="space-y-6">
    <header className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
      <div><h1 className="text-[22px] font-semibold leading-[30px] text-foreground">工作台</h1><p className="mt-1 text-sm text-muted-foreground">运营概览与待处理事项</p></div>
      <div className="flex flex-wrap items-center gap-3"><span className="text-xs text-muted-foreground" aria-live="polite">{summary.error ? "指标更新失败" : summary.isLoading ? "正在更新指标…" : updateLabel(summary.updatedAt)} · 上海时间</span><Button type="button" variant="outline" disabled={refreshing} onClick={refreshAll} className={compactControl}><RefreshCw aria-hidden="true" className={cn("size-4", refreshing && "animate-spin")} />{refreshing ? "正在刷新" : "刷新数据"}</Button></div>
    </header>
    <section aria-label="运营指标" aria-busy={summary.isLoading}>
      {!canViewDashboard ? <div className={panelClass}><PermissionNotice>当前账号没有工作台查看权限。</PermissionNotice></div> : summary.error ? <div className={panelClass}><SectionError error={summary.error} retry={summary.refresh} loading={summary.isLoading} /></div> : <div className="grid grid-cols-1 gap-4 md:grid-cols-2 min-[75rem]:grid-cols-4">{metrics.map(metric => <article key={metric.label} className={cn(panelClass, "p-5")}><h2 className="text-sm text-muted-foreground">{metric.label}</h2>{summary.isLoading || !summary.data ? <Skeleton className="my-3 h-9 w-28 rounded" /> : <p className="my-2 text-[28px] font-semibold leading-10 tabular-nums tracking-tight">{metric.value}</p>}<p className="text-xs leading-5 text-muted-foreground">{metric.description}</p></article>)}</div>}
    </section>
    <section aria-labelledby="dashboard-todos" className={panelClass}>
      <div className="flex flex-wrap items-baseline justify-between gap-2 border-b border-border px-5 py-4"><h2 id="dashboard-todos" className="text-base font-semibold">待处理事项</h2><span className="text-xs text-muted-foreground">当前授权范围 · {summary.error ? "更新失败" : summary.isLoading ? "正在更新…" : updateLabel(summary.updatedAt)}</span></div>
      {!canViewDashboard ? <PermissionNotice>当前账号没有工作台查看权限。</PermissionNotice> : summary.error ? <SectionError error={summary.error} retry={summary.refresh} loading={summary.isLoading} /> : <div className="grid grid-cols-1 divide-y divide-border md:grid-cols-2 md:divide-y-0 min-[75rem]:grid-cols-5">{todos.map(({ label, count, permission, href, action, description, icon: Icon }) => <article key={label} className="flex min-w-0 flex-col gap-3 p-5"><div className="flex items-center gap-2 text-sm"><Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" /><h3>{label}</h3></div>{!can(user, permission) ? <p className="text-sm text-muted-foreground">无查看权限</p> : <>{summary.isLoading || !summary.data ? <Skeleton className="h-9 w-16 rounded" /> : <p className="text-[26px] font-semibold leading-9 tabular-nums">{count?.toLocaleString("zh-CN")}</p>}<p className="min-h-10 text-xs leading-5 text-muted-foreground">{description}</p><Link href={href} className="mt-auto inline-flex min-h-11 w-fit items-center gap-1 text-sm text-primary underline-offset-4 hover:underline focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-ring md:min-h-9">{action}<ArrowUpRight aria-hidden="true" className="size-3.5" /></Link></>}</article>)}</div>}
    </section>
    <section aria-labelledby="dashboard-ranking" className="space-y-4">
      <div className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
        <div><h2 id="dashboard-ranking" className="text-base font-semibold">业务收款排名</h2><p className="mt-1 text-xs leading-5 text-muted-foreground">按上海自然月统计 · CNY · 月份与 Top N 仅影响排名</p></div>
        {canViewReports && <div className="flex flex-wrap items-center gap-3"><label className="flex items-center gap-2 text-sm" htmlFor="dashboard-ranking-month">月份<Input id="dashboard-ranking-month" type="month" value={monthDraft} min="2000-01" max={maximumMonth} aria-invalid={!validMonth} aria-describedby={!validMonth ? "dashboard-ranking-period-error" : undefined} onChange={event => { const value = event.target.value; setMonthDraft(value); if (/^\d{4}-(0[1-9]|1[0-2])$/.test(value) && value >= "2000-01" && value <= maximumMonth) setMonth(value); }} className={cn(compactControl, "w-44 bg-card")} /></label><Select value={limit} onValueChange={setLimit}><SelectTrigger aria-label="排名展示数量" className={cn(compactControl, "w-28 bg-card")}><SelectValue /></SelectTrigger><SelectContent>{[5, 10, 20].map(value => <SelectItem key={value} value={String(value)}>Top {value}</SelectItem>)}</SelectContent></Select></div>}
      </div>
      {canViewReports && !validMonth && <p role="alert" id="dashboard-ranking-period-error" className="text-sm text-destructive">请选择 2000年1月至{maximumMonth.slice(0, 4)}年12月内的月份。当前仍展示 {month} 的排名。</p>}
      {!canViewReports ? <div className={panelClass}><PermissionNotice>当前账号没有报表查看权限，商品与人员排名不可用。</PermissionNotice></div> : <div className="grid grid-cols-1 items-start gap-6 min-[75rem]:grid-cols-2">
        <RankingPanel id="dashboard-product-ranking" title="商品业务收款排名" subtitle="仅新购与续费；件数与单数分别统计" updatedAt={products.updatedAt} loading={products.isLoading} error={products.error} retry={products.refresh} empty={products.data?.items.length === 0} total={products.data?.total} shown={products.data?.items.length} month={month}>
          <Table className="min-w-[520px]"><TableHeader><TableRow><TableHead className="w-12 pl-5">排名</TableHead><TableHead>商品</TableHead><TableHead className="text-right">新购</TableHead><TableHead className="text-right">续费</TableHead><TableHead className="pr-5 text-right">业务收款</TableHead></TableRow></TableHeader><TableBody>{products.data?.items.map((item, index) => <TableRow key={item.product_id}><TableCell className="pl-5"><RankBadge rank={index + 1} /></TableCell><TableCell className="max-w-48 whitespace-normal"><span className="block break-words font-medium">{item.name}</span><span className="text-xs text-muted-foreground">#{item.product_id}</span></TableCell><TableCell className="text-right tabular-nums"><span className="block">{item.new_quantity.toLocaleString("zh-CN")} 件</span><span className="text-xs text-muted-foreground">{item.new_order_count.toLocaleString("zh-CN")} 单</span></TableCell><TableCell className="text-right tabular-nums"><span className="block">{item.renewal_quantity.toLocaleString("zh-CN")} 项</span><span className="text-xs text-muted-foreground">{item.renewal_order_count.toLocaleString("zh-CN")} 账单</span></TableCell><TableCell className="pr-5 text-right font-medium tabular-nums">{money(item.total_amount_cents)}</TableCell></TableRow>)}</TableBody></Table>
        </RankingPanel>
        <RankingPanel id="dashboard-staff-ranking" title="人员业务收款排名" subtitle="按客户当前销售归属统计，排除未分配" updatedAt={staff.updatedAt} loading={staff.isLoading} error={staff.error} retry={staff.refresh} empty={staff.data?.items.length === 0} total={staff.data?.total} shown={staff.data?.items.length} month={month}>
          <Table className="min-w-[480px]"><TableHeader><TableRow><TableHead className="w-12 pl-5">排名</TableHead><TableHead>销售人员</TableHead><TableHead className="text-right">客户数</TableHead><TableHead className="text-right">账单数</TableHead><TableHead className="pr-5 text-right">业务收款</TableHead></TableRow></TableHeader><TableBody>{staff.data?.items.map((item, index) => <TableRow key={item.sales_owner_id}><TableCell className="pl-5"><RankBadge rank={index + 1} /></TableCell><TableCell className="max-w-40 whitespace-normal"><span className="block break-words font-medium">{item.sales_owner_name}</span><span className="text-xs text-muted-foreground">#{item.sales_owner_id}</span></TableCell><TableCell className="text-right tabular-nums">{item.customer_count.toLocaleString("zh-CN")}</TableCell><TableCell className="text-right tabular-nums">{item.invoice_count.toLocaleString("zh-CN")}</TableCell><TableCell className="pr-5 text-right font-medium tabular-nums">{money(item.amount_cents)}</TableCell></TableRow>)}</TableBody></Table>
        </RankingPanel>
      </div>}
      {canViewReports && <p className="text-xs leading-5 text-muted-foreground">业务收款含余额消费及历史信用实际回款，排除充值，不等于外部现金、利润或提成。已确认外部退款不在此指标扣减；纯余额退款账单不计入。商品排名仅含新购与续费，人员排名还含升级等业务，二者合计可能不同。调整客户销售归属会改变人员历史排名。</p>}
    </section>
  </div>;
}

function RankBadge({ rank }: { rank: number }) {
  return <span className={cn("inline-flex size-6 items-center justify-center rounded text-xs font-medium tabular-nums", rank <= 3 ? "bg-accent text-primary" : "text-muted-foreground")}>{rank}</span>;
}
function RankingPanel({ id, title, subtitle, updatedAt, loading, error, retry, empty, total, shown, month, children }: {
  id: string; title: string; subtitle: string; updatedAt: number | null; loading: boolean; error: Error | null; retry: () => void;
  empty: boolean; total: number | undefined; shown: number | undefined; month: string; children: ReactNode;
}) {
  return <section aria-labelledby={id} aria-busy={loading} className={panelClass}>
    <div className="space-y-1 border-b border-border px-5 py-4"><div className="flex flex-wrap items-center justify-between gap-2"><h3 id={id} className="text-sm font-semibold">{title}</h3><Button type="button" variant="ghost" size="icon" disabled={loading} onClick={retry} aria-label={`刷新${title}`} className="size-11 rounded md:size-9"><RefreshCw aria-hidden="true" className={cn("size-4", loading && "animate-spin")} /></Button></div><p className="text-xs leading-5 text-muted-foreground">{subtitle}</p><p className="text-xs text-muted-foreground">{month} · {loading ? "正在更新…" : error ? "更新失败" : updateLabel(updatedAt)}</p></div>
    {error ? <SectionError error={error} retry={retry} loading={loading} /> : loading || shown === undefined ? <SectionLoading label={`正在加载${title}`} rows={5} /> : empty ? <div role="status" className="px-5 py-12 text-center text-sm text-muted-foreground">{month} 当前授权范围内暂无符合口径的记录</div> : children}
    {!error && !loading && shown !== undefined && <p className="border-t border-border px-5 py-3 text-xs text-muted-foreground">展示前 {shown} 名，共 {total} 个符合口径的对象</p>}
  </section>;
}
