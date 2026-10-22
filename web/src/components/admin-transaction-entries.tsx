"use client";

import { useState } from "react";
import { Search, RefreshCw, SlidersHorizontal } from "lucide-react";
import { money } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { useAdminListNavigation } from "@/hooks/use-admin-list-navigation";
import { Workspace, useAdminPermission } from "./workspace";
import { AdminStatusTabs } from "./admin-status-tabs";
import { AdminListPage, AdminListRegion } from "./admin-list-page";
import { AdminPagination } from "./admin-pagination";
import { AdminTransactionResults, transactionKinds as kinds, transactionChannels as channels, transactionDirections as directions, type TransactionEntry } from "./admin-transaction-results";
import { Card, CardContent, CardHeader, CardTitle } from "./ui/card";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
type EntriesPage = { items: TransactionEntry[]; total: number; limit: number; offset: number };
type CashSummary = { gateway_income_cents: number; manual_income_cents: number; credit_repayment_cents: number; cash_income_cents: number; external_refund_cents: number; manual_refund_cents: number; net_gateway_cents: number; net_manual_cents: number; net_cash_cents: number };
const kindValues = Object.keys(kinds);
const defaults = { customer_id: "", from: "", to: "", direction: "", channel: "", min_cents: "", max_cents: "", limit: "20" };

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
    <AdminListPage title="交易流水" description="追溯现金、钱包及账单结算记录；金额单位为人民币，日期按北京时间筛选。"
      actions={<Button variant="outline" onClick={() => { resource.refresh(); summary.refresh(); }} disabled={!allowed || !nav.ready}><RefreshCw size={15} />刷新交易流水</Button>}
      filters={allowed && nav.ready ? (<form key={nav.suffix} onSubmit={event => { event.preventDefault(); apply(event.currentTarget); }} aria-label="交易流水查询" className="min-w-0 rounded-xl border bg-card p-4">
        <AdminStatusTabs value={nav.status} states={kinds} label="交易类型" onChange={value => { nav.setStatus(value); nav.setPage(0); }} />
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
          <label className="text-sm">方向<select name="direction" className="mt-1 h-10 w-full rounded-md border border-input bg-background px-3" defaultValue={nav.extra.direction}><option value="">全部方向</option>{Object.entries(directions).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
          <label className="text-sm">付款渠道<select name="channel" className="mt-1 h-10 w-full rounded-md border border-input bg-background px-3" defaultValue={nav.extra.channel}><option value="">全部渠道</option>{nav.extra.channel && !channels[nav.extra.channel] && <option value={nav.extra.channel}>{nav.extra.channel}</option>}{Object.entries(channels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
          <label className="text-sm">最低金额（元）<Input name="min_cents" inputMode="decimal" defaultValue={amountInput(nav.extra.min_cents)} placeholder="0.00" /></label>
          <label className="text-sm">最高金额（元）<Input name="max_cents" inputMode="decimal" defaultValue={amountInput(nav.extra.max_cents)} placeholder="不限" /></label>
          <p className="self-end text-xs text-muted-foreground">日期按北京时间筛选，包含结束当天。</p>
        </div>}
        {filterError && <p role="alert" className="mt-3 text-sm text-destructive">{filterError}</p>}
      </form>) : undefined}>
      <AdminListRegion title="现金收支汇总" allowed={allowed} resource={summary} retry={summary.refresh}>
        {data => <div className="min-w-0 space-y-3 p-4">
        <div className="grid min-w-0 gap-4 sm:grid-cols-3">{[["现金收入", data.cash_income_cents], ["外部退款到账", data.external_refund_cents], ["现金净额", data.net_cash_cents]].map(([label, amount]) => <Card key={label} className="min-w-0 gap-3 shadow-none"><CardHeader className="px-5"><CardTitle><h2 className="text-sm font-medium text-muted-foreground">{label}</h2></CardTitle></CardHeader><CardContent className="px-5"><p className="money break-all text-2xl font-semibold">{money(Number(amount))}</p></CardContent></Card>)}</div>
        <div className="rounded-xl border bg-card p-4 text-xs text-muted-foreground"><p>按全部匹配记录汇总，不受当前分页限制。现金收入包含网关收款、人工线下收款及历史信用还款；现金净额＝现金收入－已确认外部退款。钱包内转和账单结算不重复计入。</p>
          <details className="mt-3"><summary className="cursor-pointer py-1 font-medium text-foreground focus-visible:outline-2 focus-visible:outline-ring">查看收支来源</summary><dl className="mt-3 grid gap-3 sm:grid-cols-2 xl:grid-cols-3">{[["网关收款", data.gateway_income_cents], ["人工线下收款", data.manual_income_cents], ["历史信用还款", data.credit_repayment_cents], ["人工线下退款", data.manual_refund_cents], ["网关净额", data.net_gateway_cents], ["人工线下净额", data.net_manual_cents]].map(([label, amount]) => <div key={label}><dt>{label}</dt><dd className="money mt-1 text-sm text-foreground">{money(Number(amount))}</dd></div>)}</dl></details>
        </div>
      </div>}
      </AdminListRegion>
      <AdminListRegion title="交易流水查询结果" allowed={allowed} resource={resource} retry={resource.refresh}
        summary={resource.data && `共 ${resource.data.total} 条流水`}
        pagination={result => <AdminPagination total={result.total} page={nav.page} pageSize={limit} pageSizes={[20, 50, 100]} onPageChange={nav.setPage} onPageSizeChange={value => { nav.setExtra({ ...nav.extra, limit: String(value) }); nav.setPage(0); }} />}>
        {result => <AdminTransactionResults items={result.items} customerLinks={customerLinks} returnHref={returnHref} />}
      </AdminListRegion>
    </AdminListPage>
  </Workspace>;
}
