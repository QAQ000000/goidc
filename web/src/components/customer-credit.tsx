"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { api, date, money, type User } from "@/lib/api";
import { Workspace } from "./workspace";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";
import { AdminPagination } from "./admin-pagination";

export type CustomerCreditAccount = { display_enabled: boolean; payment_enabled: boolean; limit_cents: number; used_cents: number; available_cents: number; frozen: boolean; version: number; updated_at: string };
type Consumption = { invoice_id: number; invoice_no: string; amount_cents: number; remaining_cents: number; status: string; version: number; created_at: string; refunded_at: string | null };
type Repayment = { id: number; invoice_id: number; invoice_no: string; amount_cents: number; received_at: string; created_at: string };
type CreditPage = { items: (Consumption | Repayment)[]; total: number; limit: number; offset: number };

// No browser storage or shared cache. Revalidate identity around every read,
// and discard the visible snapshot on focus, session loss, or explicit refresh.
export function useCustomerCreditResource<T>(path: string | null) {
  const [revision, setRevision] = useState(0);
  const [sessionLost, setSessionLost] = useState(false);
  const [currentIdentity, setCurrentIdentity] = useState(0);
  const [state, setState] = useState<{ path: string; revision: number; identity: number; data: T | null; error: Error | null } | null>(null);
  const refresh = useCallback(() => { setSessionLost(false); setRevision(value => value + 1); }, []);
  useEffect(() => {
    const invalidate = () => { setSessionLost(false); refresh(); };
    const revoke = () => setSessionLost(true);
    window.addEventListener("focus", invalidate);
    window.addEventListener("zjmfgo:unauthorized", revoke);
    return () => { window.removeEventListener("focus", invalidate); window.removeEventListener("zjmfgo:unauthorized", revoke); };
  }, [refresh]);
  useEffect(() => {
    if (!path || sessionLost) return;
    const controller = new AbortController();
    let identity = 0;
    async function read() {
      const before = await api<User>("/me", { signal: controller.signal });
      if (before.role !== "customer") throw new Error("仅客户账户可查看信用资料");
      if (controller.signal.aborted) return;
      identity = before.id;
      setCurrentIdentity(identity);
      const data = await api<T>(path!, { signal: controller.signal });
      if (data === undefined || data === null) throw new Error("信用资料响应不完整，请重新加载");
      const after = await api<User>("/me", { signal: controller.signal });
      if (after.id !== identity || after.role !== "customer") throw new Error("账户已切换，请重新加载信用资料");
      if (!controller.signal.aborted) setState({ path: path!, revision, identity, data, error: null });
    }
    void read().catch((error: Error) => { if (!controller.signal.aborted) { setCurrentIdentity(identity); setState({ path, revision, identity, data: null, error }); } });
    return () => controller.abort();
  }, [path, revision, sessionLost]);
  const current = !sessionLost && state?.identity === currentIdentity && state?.path === path && state?.revision === revision ? state : null;
  return { data: current?.data ?? null, error: sessionLost ? new Error("登录已失效，请重新登录后查看信用资料") : current?.error ?? null, identity: current?.identity ?? null, refresh };
}

export function creditDebtLabel(status: string) {
  return status === "outstanding" ? "尚有应还债务" : status === "repaid" ? "已还清" : status === "refunded" ? "信用债务已冲销" : "债务状态待核实";
}
export function CustomerCreditOverview() {
  const resource = useCustomerCreditResource<CustomerCreditAccount>("/credit/account");
  return <section className="data-surface p-4 sm:p-6 mb-6 space-y-4"><div className="flex flex-wrap items-center justify-between gap-3"><h2 className="font-semibold">信用与财务记录</h2><Link href="/console/credit" className="text-sm text-primary underline">信用消费与还款记录</Link></div>{resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : resource.data.display_enabled ? <><div className="grid gap-4 sm:grid-cols-3">{[["授权额度", resource.data.limit_cents], ["已用额度", resource.data.used_cents], ["账面可用额度", resource.data.available_cents]].map(([label, value]) => <div key={label}><p className="text-sm text-muted-foreground">{label}</p><p className="money text-xl mt-2">{money(Number(value))}</p></div>)}</div>{(!resource.data.payment_enabled || resource.data.frozen || resource.data.available_cents === 0) && <p role="status" className="text-sm">当前不可新增信用付款{resource.data.frozen ? "：信用账户已冻结" : !resource.data.payment_enabled ? "：信用付款已关闭" : "：账面可用额度不足"}。历史债务和还款记录仍可查看。</p>}<p className="text-xs text-muted-foreground">信用额度与账户余额独立，账面可用额度不代表具体账单可支付；付款时仍须核验账单和账户条件。</p><Button size="sm" variant="outline" onClick={resource.refresh}>刷新信用概览</Button></> : <p className="text-sm text-muted-foreground">可在财务记录中查看既有信用消费与还款。</p>}</section>;
}

export function InvoiceCreditStatus({ invoiceId, revision }: { invoiceId: number; revision: number | string }) {
  return <InvoiceCreditStatusSnapshot key={`${invoiceId}:${revision}`} invoiceId={invoiceId} />;
}
function InvoiceCreditStatusSnapshot({ invoiceId }: { invoiceId: number }) {
  const resource = useCustomerCreditResource<{ credit: (Consumption & { repaid_cents: number }) | null }>(`/invoices/${invoiceId}/credit`);
  return <section className="border-b py-4 mb-4 space-y-3"><h2 className="font-semibold">信用结算</h2>{resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : !resource.data.credit ? <p className="text-sm text-muted-foreground">此账单没有信用付款记录。</p> : <><p className="text-sm">信用付款完成 · {creditDebtLabel(resource.data.credit.status)}</p><dl className="grid gap-3 text-sm sm:grid-cols-3"><div><dt>信用付款金额</dt><dd className="money">{money(resource.data.credit.amount_cents)}</dd></div><div><dt>剩余应还</dt><dd className="money">{money(resource.data.credit.remaining_cents)}</dd></div><div><dt>已确认还款</dt><dd className="money">{money(resource.data.credit.repaid_cents)}</dd></div></dl>{resource.data.credit.status === "refunded" && <p className="text-sm">该笔信用债务已冲销，不代表现金到账。</p>}<Link className="text-sm text-primary underline" href={`/console/credit?invoice_id=${invoiceId}`}>查看此账单信用记录</Link></>}</section>;
}

export function CustomerCreditView() {
  const search = useSearchParams();
  const router = useRouter();
  const tab = search.get("tab") === "repayments" ? "repayments" : "consumptions";
  const rawPage = Number(search.get("page") ?? 1);
  const page = Number.isSafeInteger(rawPage) && rawPage > 0 && rawPage <= 1000000 ? rawPage - 1 : 0;
  const size = [25, 50, 100].includes(Number(search.get("limit"))) ? Number(search.get("limit")) : 25;
  const q = (search.get("q") ?? "").slice(0, 100);
  const dateFrom = search.get("date_from") ?? "", dateTo = search.get("date_to") ?? "";
  const status = search.get("status") ?? "", invoiceId = search.get("invoice_id") ?? "";
  const params = new URLSearchParams({ limit: String(size), offset: String(page * size) });
  if (q.trim()) params.set("q", q.trim());
  if (dateFrom) params.set("date_from", dateFrom);
  if (dateTo) params.set("date_to", dateTo);
  if (invoiceId) params.set("invoice_id", invoiceId);
  if (tab === "consumptions" && status) params.set("status", status);
  const resource = useCustomerCreditResource<CreditPage>(`/credit/${tab}?${params}`);
  function change(values: Record<string, string>, reset = true) {
    const next = new URLSearchParams(search.toString());
    for (const [key, value] of Object.entries(values)) { if (value) next.set(key, value); else next.delete(key); }
    if (reset) next.delete("page");
    router.push(`/console/credit${next.size ? `?${next}` : ""}`, { scroll: false });
  }
  const returnTo = `/console/credit${search.size ? `?${search}` : ""}`;
  return <Workspace title="信用消费与还款"><div className="min-w-0 space-y-5"><div className="flex flex-wrap justify-between gap-3"><div><h1 className="page-title">信用消费与还款</h1><p className="text-sm text-muted-foreground mt-2">查看信用债务和已确认到账的还款；日期按上海自然日筛选。</p></div><Button variant="outline" onClick={resource.refresh}>刷新记录</Button></div><div className="flex flex-wrap gap-2" aria-label="信用记录类型">{[["consumptions", "信用消费"], ["repayments", "还款记录"]].map(([key, label]) => <Button key={key} aria-pressed={tab === key} variant={tab === key ? "default" : "outline"} onClick={() => change({ tab: key, status: "" })}>{label}</Button>)}</div><div className="grid min-w-0 gap-3 sm:grid-cols-2 lg:grid-cols-3"><label className="text-sm space-y-2">账单号或账单 ID<Input aria-label="搜索本人账单" maxLength={100} value={q} onChange={event => change({ q: event.target.value })} /></label><label className="text-sm space-y-2">账单 ID<Input inputMode="numeric" value={invoiceId} onChange={event => change({ invoice_id: event.target.value })} /></label><label className="text-sm space-y-2">开始日期<Input type="date" value={dateFrom} onChange={event => change({ date_from: event.target.value })} /></label><label className="text-sm space-y-2">结束日期<Input type="date" value={dateTo} onChange={event => change({ date_to: event.target.value })} /></label>{tab === "consumptions" && <label className="text-sm space-y-2">债务状态<select className="h-9 w-full rounded border px-2 bg-background" value={status} onChange={event => change({ status: event.target.value })}><option value="">全部</option><option value="outstanding">尚有应还</option><option value="repaid">已还清</option><option value="refunded">信用债务已冲销</option></select></label>}<div className="flex items-end"><Button variant="outline" onClick={() => router.push(`/console/credit?tab=${tab}`, { scroll: false })}>重置筛选</Button></div></div>{resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <><div className="data-surface min-w-0 overflow-x-auto"><Table className="min-w-[650px]"><TableHeader><TableRow>{(tab === "consumptions" ? ["账单", "信用消费金额", "剩余应还", "信用付款时间", "债务状态"] : ["账单", "到账金额", "到账时间", "登记时间"]).map(label => <TableHead key={label}>{label}</TableHead>)}</TableRow></TableHeader><TableBody>{resource.data.items.map(row => <TableRow key={"id" in row ? row.id : row.invoice_id}><TableCell><Link className="text-primary underline" href={`/console/invoices/${row.invoice_id}?credit_return=${encodeURIComponent(returnTo)}`}>{row.invoice_no}</Link></TableCell><TableCell className="money">{money(row.amount_cents)}</TableCell>{"remaining_cents" in row ? <><TableCell className="money">{money(row.remaining_cents)}</TableCell><TableCell>{date(row.created_at)}</TableCell><TableCell>{creditDebtLabel(row.status)}</TableCell></> : <><TableCell>{date(row.received_at)}</TableCell><TableCell>{date(row.created_at)}</TableCell></>}</TableRow>)}{resource.data.items.length === 0 && <TableRow><TableCell colSpan={tab === "consumptions" ? 5 : 4} className="text-center py-10">{resource.data.total > 0 ? <>当前页已超出记录范围。<Button variant="link" onClick={() => change({ page: "1" }, false)}>回到第一页</Button></> : "暂无符合条件的记录"}</TableCell></TableRow>}</TableBody></Table></div><AdminPagination total={resource.data.total} page={page} pageSize={size} onPageChange={next => change({ page: String(next + 1) }, false)} onPageSizeChange={next => change({ limit: String(next) })} /></>}</div></Workspace>;
}
