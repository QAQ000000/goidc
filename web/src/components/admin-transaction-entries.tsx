"use client";

import Link from "next/link";
import { useState } from "react";
import { Search, RefreshCw, SlidersHorizontal } from "lucide-react";
import { date, money } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { useAdminListNavigation } from "@/hooks/use-admin-list-navigation";
import { Workspace, useAdminPermission } from "./workspace";
import { Failure, Loading } from "./common";
import { AdminStatusTabs } from "./admin-status-tabs";
import { paymentLabels } from "./online-payment";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";

type Entry = {
  event_key: string; source: string; id: number; user_id: number;
  invoice_id: number | null; invoice_no: string | null; occurred_at: string;
  customer_name: string; customer_username: string; sales_owner_name: string | null;
  channel: string; direction: string; amount_cents: number; transaction_no: string;
  description: string; kind: string; balance_after: number | null;
};
type EntriesPage = { items: Entry[]; total: number; limit: number; offset: number };
type CashSummary = { gateway_income_cents: number; external_refund_cents: number; net_gateway_cents: number };
const kinds = { topup: "充值", product: "产品", renew: "续费", other: "其他" };
const kindValues = Object.keys(kinds);
const defaults = { customer_id: "", from: "", to: "", direction: "", channel: "", min_cents: "", max_cents: "", limit: "20" };
const channels: Record<string, string> = { gateway_topup: "在线充值入账", balance: "账户余额", admin_topup: "后台充值", credit: "信用额度", unrecorded: "未记录渠道", ...paymentLabels };
const sources: Record<string, string> = { payment: "成功支付", external_refund: "外部退款到账", ledger: "余额记录", invoice: "已付账单" };
const directions: Record<string, string> = { income: "收入", expense: "支出", neutral: "账单结算" };

// Convert decimal input without floating-point currency arithmetic.
function cents(value: string) {
  if (!value) return "";
  if (!/^\d{1,12}(?:\.\d{1,2})?$/.test(value)) throw new Error("金额需为非负数字，最多保留两位小数");
  const [whole, fraction = ""] = value.split(".");
  return String(BigInt(whole) * BigInt(100) + BigInt(fraction.padEnd(2, "0")));
}
function amountInput(value: string) {
  if (!/^\d+$/.test(value)) return value;
  const amount = BigInt(value);
  return `${amount / BigInt(100)}.${String(amount % BigInt(100)).padStart(2, "0")}`;
}

export function AdminTransactionEntriesView() {
  const allowed = useAdminPermission("invoices.view");
  const customerLinks = useAdminPermission("customers.view");
  const nav = useAdminListNavigation(true, kindValues, "all", defaults);
  const [advanced, setAdvanced] = useState(false);
  const [filterError, setFilterError] = useState("");
  const limit = [20, 50, 100].includes(Number(nav.extra.limit)) ? Number(nav.extra.limit) : 20;
  const params = new URLSearchParams();
  if (nav.query) params.set("q", nav.query);
  if (nav.status !== "all") params.set("kind", nav.status);
  for (const [key, value] of Object.entries(nav.extra)) if (value && key !== "limit") params.set(key, value);
  params.set("limit", String(limit));
  params.set("offset", String(nav.page * limit));
  const resource = useResource<EntriesPage>(allowed && nav.ready ? `/admin/transactions/page?${params}` : null, false);
  const summaryParams = new URLSearchParams(params);
  summaryParams.delete("limit"); summaryParams.delete("offset");
  const summary = useResource<CashSummary>(allowed && nav.ready ? `/admin/transactions/summary?${summaryParams}` : null, false);
  const returnHref = `/admin/transactions${nav.suffix}`;
  function apply(form: HTMLFormElement) {
    const values = new FormData(form);
    try {
      const next = { ...nav.extra };
      for (const key of Object.keys(defaults)) if (values.has(key)) next[key] = String(values.get(key) ?? "");
      for (const key of ["min_cents", "max_cents"]) if (values.has(key)) next[key] = cents(String(values.get(key) ?? ""));
      if (next.from && next.to && next.from > next.to) throw new Error("结束日期不能早于开始日期");
      if (next.min_cents && next.max_cents && BigInt(next.min_cents) > BigInt(next.max_cents)) throw new Error("最高金额不能低于最低金额");
      nav.setExtra(next); nav.setQuery(String(values.get("q") ?? "").trim()); nav.setPage(0); setFilterError("");
    } catch (error) { setFilterError((error as Error).message); }
  }
  return <Workspace title="交易流水" admin>
    <div className="admin-page-heading mb-5 flex flex-wrap items-start justify-between gap-3">
      <div><h1 className="text-xl font-semibold">交易流水</h1><p className="mt-1 text-sm text-muted-foreground">查看成功支付、余额收支和已完成账单；金额单位为人民币。</p></div>
      <Button variant="outline" onClick={() => { resource.refresh(); summary.refresh(); }} disabled={!allowed}><RefreshCw size={15} />刷新</Button>
    </div>
    {!allowed ? <p role="alert" className="rounded border p-6 text-destructive">无权查看交易流水，需要账单查看权限。</p> : !nav.ready ? <Loading /> : <>
      <AdminStatusTabs value={nav.status} states={kinds} label="交易类型" onChange={value => { nav.setStatus(value); nav.setPage(0); }} />
      <form key={nav.suffix} onSubmit={event => { event.preventDefault(); apply(event.currentTarget); }} className="mb-5 rounded border bg-card p-4">
        <div className="flex flex-wrap gap-3">
          <Input name="q" aria-label="关键词" maxLength={120} defaultValue={nav.query} placeholder="搜索客户、账单号、流水号或描述" className="w-full sm:w-80" />
          <Button type="submit"><Search size={15} />搜索</Button>
          <Button type="button" variant="outline" onClick={() => setAdvanced(value => !value)} aria-expanded={advanced || Object.entries(nav.extra).some(([key, value]) => key !== "limit" && Boolean(value))}><SlidersHorizontal size={15} />高级搜索</Button>
          <Button type="button" variant="outline" onClick={() => { nav.setQuery(""); nav.setDraft(""); nav.setStatus("all"); nav.setExtra({ ...defaults }); nav.setPage(0); setFilterError(""); }}>清空</Button>
        </div>
        {(advanced || Object.entries(nav.extra).some(([key, value]) => key !== "limit" && value)) && <div className="mt-4 grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
          <label className="text-sm">客户编号<Input name="customer_id" type="number" min="1" step="1" defaultValue={nav.extra.customer_id} placeholder="客户编号" /></label>
          <label className="text-sm">开始日期<Input name="from" type="date" defaultValue={nav.extra.from} /></label>
          <label className="text-sm">结束日期<Input name="to" type="date" defaultValue={nav.extra.to} /></label>
          <label className="text-sm">方向<select name="direction" className="mt-1 h-9 w-full rounded border px-3" defaultValue={nav.extra.direction}><option value="">全部方向</option>{Object.entries(directions).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
          <label className="text-sm">付款渠道<select name="channel" className="mt-1 h-9 w-full rounded border px-3" defaultValue={nav.extra.channel}><option value="">全部渠道</option>{nav.extra.channel && !channels[nav.extra.channel] && <option value={nav.extra.channel}>{nav.extra.channel}</option>}{Object.entries(channels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
          <label className="text-sm">最低金额（元）<Input name="min_cents" inputMode="decimal" defaultValue={amountInput(nav.extra.min_cents)} placeholder="0.00" /></label>
          <label className="text-sm">最高金额（元）<Input name="max_cents" inputMode="decimal" defaultValue={amountInput(nav.extra.max_cents)} placeholder="不限" /></label>
          <p className="self-end text-xs text-muted-foreground">日期按北京时间筛选，包含结束当天。</p>
        </div>}
        {filterError && <p role="alert" className="mt-3 text-sm text-destructive">{filterError}</p>}
      </form>
      {summary.error ? <Failure error={summary.error} retry={summary.refresh} /> : !summary.data ? <Loading /> : <section aria-label="网关收退款汇总" className="mb-5 grid gap-3 sm:grid-cols-3">
        {[["网关收款", summary.data.gateway_income_cents], ["外部退款到账", summary.data.external_refund_cents], ["网关净额", summary.data.net_gateway_cents]].map(([label, amount]) => <div key={label} className="min-w-0 rounded border bg-card p-4"><p className="text-sm text-muted-foreground">{label}</p><p className="mt-2 break-all text-xl font-semibold">{money(Number(amount))}</p></div>)}
        <p className="text-xs text-muted-foreground sm:col-span-3">按当前筛选汇总全部匹配记录；原收款与已确认外部退款分别保留，净额为两者之差。余额和信用结算不计入此汇总。</p>
      </section>}
      {resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <>
        <div className="overflow-x-auto rounded border bg-card"><Table>
          <TableHeader><TableRow>{["客户名", "时间", "付款方式", "描述", "金额", "销售（当前）", "流水号", "类型 / 来源", "操作"].map(label => <TableHead key={label} className="whitespace-nowrap">{label}</TableHead>)}</TableRow></TableHeader>
          <TableBody>{resource.data.items.length === 0 ? <TableRow><TableCell colSpan={9} className="h-32 text-center text-muted-foreground">暂无数据</TableCell></TableRow> : resource.data.items.map(row => <TableRow key={row.event_key}>
            <TableCell><div className="whitespace-nowrap">{customerLinks ? <Link className="text-primary hover:underline" href={`/admin/customers/${row.user_id}?return_to=${encodeURIComponent(returnHref)}`}>{row.customer_name}</Link> : row.customer_name}</div><div className="text-xs text-muted-foreground">#{row.user_id} · {row.customer_username}</div></TableCell>
            <TableCell className="whitespace-nowrap">{date(row.occurred_at)}</TableCell>
            <TableCell className="whitespace-nowrap">{channels[row.channel] ?? row.channel}</TableCell>
            <TableCell className="min-w-44 max-w-80"><p className="break-words">{row.description}</p>{row.invoice_no && <p className="text-xs text-muted-foreground">{row.invoice_no}</p>}</TableCell>
            <TableCell className="whitespace-nowrap"><div className={row.direction === "expense" ? "text-destructive" : ""}>{row.direction === "expense" ? "−" : row.direction === "income" ? "+" : ""}{money(row.amount_cents)}</div><div className="text-xs text-muted-foreground">{row.channel === "gateway_topup" && row.direction === "neutral" ? "余额内部入账" : directions[row.direction]}{row.balance_after !== null && <><br />余额 {money(row.balance_after)}</>}</div></TableCell>
            <TableCell className="whitespace-nowrap">{row.sales_owner_name ?? "—"}</TableCell>
            <TableCell className="max-w-56 break-all font-mono text-xs">{row.transaction_no}</TableCell>
            <TableCell className="whitespace-nowrap">{kinds[row.kind as keyof typeof kinds] ?? row.kind}<p className="text-xs text-muted-foreground">{sources[row.source] ?? row.source}</p></TableCell>
            <TableCell>{row.invoice_id ? <Link className="whitespace-nowrap text-primary hover:underline" href={`/admin/invoices/${row.invoice_id}?return_to=${encodeURIComponent(returnHref)}`}>查看账单</Link> : "—"}</TableCell>
          </TableRow>)}</TableBody>
        </Table></div>
        <div className="mt-4 flex flex-wrap items-center justify-between gap-3 text-sm">
          <span>共 {resource.data.total} 条 · 第 {nav.page + 1} / {Math.max(1, Math.ceil(resource.data.total / limit))} 页</span>
          <div className="flex flex-wrap items-center gap-2"><label>每页 <select aria-label="每页条数" value={limit} className="h-9 rounded border px-2" onChange={event => { nav.setExtra({ ...nav.extra, limit: event.target.value }); nav.setPage(0); }}>{[20, 50, 100].map(value => <option value={value} key={value}>{value} 条</option>)}</select></label>
            <Button variant="outline" size="sm" disabled={nav.page === 0} onClick={() => nav.setPage(value => value - 1)}>上一页</Button>
            <Button variant="outline" size="sm" disabled={(nav.page + 1) * limit >= resource.data.total} onClick={() => nav.setPage(value => value + 1)}>下一页</Button>
            <form key={nav.page} className="flex items-center gap-2" onSubmit={event => { event.preventDefault(); const page = Number(new FormData(event.currentTarget).get("page")); if (Number.isInteger(page) && page >= 1 && page <= Math.max(1, Math.ceil(resource.data!.total / limit))) nav.setPage(page - 1); }}><Input aria-label="前往页码" name="page" type="number" min="1" max={Math.max(1, Math.ceil(resource.data.total / limit))} defaultValue={nav.page + 1} className="w-20" /><Button variant="outline" size="sm" type="submit">前往</Button></form>
          </div>
        </div>
      </>}
    </>}
  </Workspace>;
}
