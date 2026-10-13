"use client";

import Link from "next/link";
import { useEffect, useRef, useState } from "react";
import { RefreshCw } from "lucide-react";
import { ApiError, api, date, money } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { usePendingSetting } from "@/hooks/use-pending-setting";
import { useAdminListNavigation } from "@/hooks/use-admin-list-navigation";
import { Workspace, useAdminIdentity, useAdminPermission } from "./workspace";
import { Failure, Loading } from "./common";
import { AdminPagination } from "./admin-pagination";
import { Button } from "./ui/button";
import { Input } from "./ui/input";

type Item = { order_id: number; invoice_id: number; customer_id: number; sales_owner_id: number; sales_owner_name: string; basis_cents: number; purchase_rate_bps: number; tier_extra_rate_bps: number; monthly_turnover_cents: number; commission_cents: number; cash_received_at: string; rule_version: number; staff_version: number; owner_version: number; tier_version: number; status?: string; reversed_at?: string | null };
type Preview = { month: string; items: Item[]; total_commission_cents?: number; commission_cents: number; reversed_cents: number; net_commission_cents: number; preview_digest: string };
type Settlement = Omit<Preview, "preview_digest"> & { id: number; status: string; created_at: string };
type Page = { items: Settlement[]; total: number; limit: number; offset: number };
const states: readonly string[] = [];
const defaults = { limit: "20", detail: "", order_ids: "" };
const labels: Record<string, string> = { confirmed: "已确认", partially_reversed: "部分冲正", reversed: "已冲正" };
function lastClosedMonth() {
  const parts = new Intl.DateTimeFormat("en-CA", { timeZone: "Asia/Shanghai", year: "numeric", month: "2-digit" }).formatToParts(new Date());
  const year = Number(parts.find(part => part.type === "year")?.value);
  const month = Number(parts.find(part => part.type === "month")?.value);
  return `${month === 1 ? year - 1 : year}-${String(month === 1 ? 12 : month - 1).padStart(2, "0")}`;
}
function orderIDs(text: string) {
  const values = text.trim().split(/[\s,，]+/);
  if (!text.trim() || values.length > 100 || values.some(value => !/^[1-9]\d{0,14}$/.test(value) || !Number.isSafeInteger(Number(value)))) throw new Error("请输入1至100个有效订单ID，以逗号或空格分隔");
  const ids = values.map(Number);
  if (new Set(ids).size !== ids.length) throw new Error("订单ID不能重复");
  return ids.sort((a, b) => a - b);
}

export function AdminSalesSettlements() {
  const allowed = useAdminPermission("sales.view");
  const nav = useAdminListNavigation(true, states, "all", defaults);
  const limit = [20, 50, 100].includes(Number(nav.extra.limit)) ? Number(nav.extra.limit) : 20;
  const resource = useResource<Page>(allowed && nav.ready ? `/admin/sales-settlements?limit=${limit}&offset=${nav.page * limit}` : null, false);
  const detailID = /^[1-9]\d*$/.test(nav.extra.detail) ? nav.extra.detail : "";
  const detail = useResource<Settlement>(allowed && detailID ? `/admin/sales-settlements/${detailID}` : null, false);
  return <Workspace title="销售结算" admin>
    <div className="mb-5 flex flex-wrap items-start justify-between gap-3"><div><h1 className="page-title">销售结算</h1><p className="mt-2 text-sm text-muted-foreground">按已结束月份确认新购订单提成，保存销售归属、规则和金额快照。</p></div><Button variant="outline" asChild><Link href="/admin/sales-settings">销售设置</Link></Button></div>
    {!allowed ? <p role="alert">没有查看销售结算的权限。</p> : !nav.ready ? <Loading /> : <div className="space-y-6">
      <CreateSettlement initialIDs={nav.extra.order_ids} onSaved={id => { nav.setExtra({ ...nav.extra, detail: String(id), order_ids: "" }); nav.setPage(0); resource.refresh(); detail.refresh(); }} />
      {detailID && <section className="space-y-4 rounded border bg-card p-4"><div className="flex flex-wrap items-center justify-between gap-3"><h2 className="font-semibold">结算详情 #{detailID}</h2><div className="flex gap-2"><Button variant="outline" size="sm" onClick={detail.refresh}>刷新详情</Button><Button variant="outline" size="sm" onClick={() => nav.setExtra({ ...nav.extra, detail: "" })}>返回列表</Button></div></div>{detail.error ? <Failure error={detail.error} retry={detail.refresh} /> : !detail.data ? <Loading /> : <><p className="text-sm">{detail.data.month} · {labels[detail.data.status] ?? detail.data.status} · {date(detail.data.created_at)}</p><Totals value={detail.data} /><ItemsTable items={detail.data.items} /></>}</section>}
      <section className="space-y-4"><div className="flex flex-wrap items-center justify-between gap-3"><h2 className="font-semibold">结算记录</h2><Button variant="outline" onClick={resource.refresh}><RefreshCw size={15} />刷新列表</Button></div>{resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <><div className="overflow-x-auto rounded border bg-card"><table className="w-full min-w-[640px] text-sm"><thead><tr className="border-b text-left text-muted-foreground">{["编号", "月份", "确认佣金", "已冲正", "净佣金", "状态", "确认时间", "操作"].map(label => <th key={label} className="whitespace-nowrap px-3 py-3">{label}</th>)}</tr></thead><tbody>{!resource.data.items.length && <tr><td colSpan={8} className="py-12 text-center text-muted-foreground">暂无结算记录</td></tr>}{resource.data.items.map(row => <tr key={row.id} className="border-b"><td className="px-3 py-3">#{row.id}</td><td className="px-3">{row.month}</td><td className="px-3">{money(row.commission_cents)}</td><td className="px-3">{money(row.reversed_cents)}</td><td className="px-3">{money(row.net_commission_cents)}</td><td className="whitespace-nowrap px-3">{labels[row.status] ?? row.status}</td><td className="whitespace-nowrap px-3">{date(row.created_at)}</td><td className="px-3"><Button variant="ghost" size="sm" onClick={() => { nav.setExtra({ ...nav.extra, detail: String(row.id) }); detail.refresh(); }}>详情</Button></td></tr>)}</tbody></table></div><AdminPagination total={resource.data.total} page={nav.page} pageSize={limit} pageSizes={[20, 50, 100]} onPageChange={nav.setPage} onPageSizeChange={size => { nav.setExtra({ ...nav.extra, limit: String(size) }); nav.setPage(0); }} /></>}</section>
    </div>}
  </Workspace>;
}

function CreateSettlement({ initialIDs, onSaved }: { initialIDs: string; onSaved: (id: number) => void }) {
  const actor = useAdminIdentity();
  const manage = useAdminPermission("sales.manage");
  const fullScope = actor?.role === "super_admin" || actor?.customer_scope === "all";
  const pending = usePendingSetting("sales-settlement", actor?.id ?? null);
  const draftKey = actor ? `zjmfgo:sales-settlement-draft:${actor.id}` : null;
  const [month, setMonth] = useState(lastClosedMonth);
  const [ids, setIDs] = useState(initialIDs);
  const [preview, setPreview] = useState<Preview | null>(null);
  const [busy, setBusy] = useState(false);
  const [forbidden, setForbidden] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const flight = useRef(false);
  const locked = busy || pending.unknown || !pending.ready || forbidden;
  const dirty = ids !== initialIDs || month !== lastClosedMonth();
  function clearDraft() { if (draftKey) { try { sessionStorage.removeItem(draftKey); } catch { /* Optional storage. */ } } }
  function edit(nextMonth: string, nextIDs: string) {
    setMonth(nextMonth); setIDs(nextIDs); setPreview(null); setMessage("");
    if (draftKey) { try { if (!nextIDs && nextMonth === lastClosedMonth()) sessionStorage.removeItem(draftKey); else sessionStorage.setItem(draftKey, JSON.stringify({ month: nextMonth, ids: nextIDs })); } catch { /* Optional storage. */ } }
  }
  useEffect(() => {
    if (!pending.ready) return;
    if (!pending.unknown) {
      if (draftKey && !initialIDs) { try { const raw = sessionStorage.getItem(draftKey); const draft = raw ? JSON.parse(raw) : null; if (draft && typeof draft.month === "string" && typeof draft.ids === "string") queueMicrotask(() => { setMonth(draft.month); setIDs(draft.ids); setMessage("已恢复未确认的会话草稿，请重新预览核对。"); }); } catch { /* Optional storage. */ } }
      return;
    }
    const value = pending.pending.current;
    if (!value) return;
    try {
      const input = JSON.parse(value.body);
      queueMicrotask(() => { setMonth(String(input.month ?? "")); setIDs(Array.isArray(input.order_ids) ? input.order_ids.join(", ") : ""); });
    } catch { /* Keep original request intact for retry. */ }
  }, [pending.ready, pending.unknown, pending.pending, draftKey, initialIDs]);
  useEffect(() => {
    if (!preview && !pending.unknown && !dirty) return;
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    const click = (event: MouseEvent) => { const anchor = event.target instanceof Element ? event.target.closest("a[href]") : null; if (anchor && !event.ctrlKey && !event.metaKey && !event.shiftKey && (pending.unknown || !window.confirm("结算预览尚未确认，确认离开？"))) { event.preventDefault(); event.stopPropagation(); } };
    window.addEventListener("beforeunload", warn); document.addEventListener("click", click, true);
    return () => { window.removeEventListener("beforeunload", warn); document.removeEventListener("click", click, true); };
  }, [preview, pending.unknown, dirty]);
  function reset() { if ((preview || dirty) && !window.confirm("确认取消当前结算预览并丢弃草稿？")) return; clearDraft(); setPreview(null); setIDs(initialIDs); setMonth(lastClosedMonth()); setError(""); setMessage(""); }
  async function loadPreview(event: React.FormEvent) {
    event.preventDefault(); if (locked || flight.current || !fullScope) return;
    let input;
    try { input = { month, order_ids: orderIDs(ids) }; if (!/^\d{4}-\d{2}$/.test(month) || month > lastClosedMonth()) throw new Error("请选择已结束月份"); } catch (cause) { setError((cause as Error).message); return; }
    flight.current = true; setBusy(true); setError(""); setMessage(""); setPreview(null);
    try { setPreview(await api<Preview>("/admin/sales-settlements/preview", { method: "POST", body: JSON.stringify(input) })); }
    catch (cause) { setError((cause as Error).message); if (cause instanceof ApiError && [401, 403].includes(cause.status)) setForbidden(true); }
    finally { flight.current = false; setBusy(false); }
  }
  async function confirm() {
    if (!manage || !fullScope || flight.current || !pending.ready || forbidden || (!preview && !pending.unknown)) return;
    if (!pending.unknown && !window.confirm(`确认 ${month} 的 ${preview!.items.length} 个新购订单佣金 ${money(preview!.commission_cents)}？确认将生成不可重复结算的账务快照，不会向员工打款或修改客户余额。`)) return;
    const wasUnknown = pending.unknown;
    const operation = wasUnknown && pending.pending.current ? pending.pending.current : { body: JSON.stringify({ month, order_ids: orderIDs(ids), preview_digest: preview!.preview_digest }), key: `sales-settlement-${crypto.randomUUID()}`, path: "/admin/sales-settlements" };
    pending.remember(operation); flight.current = true; setBusy(true); setError(""); setMessage("");
    try { const result = await api<Settlement>("/admin/sales-settlements", { method: "POST", headers: { "Idempotency-Key": operation.key }, body: operation.body }); clearDraft(); pending.resolve(); setPreview(null); setIDs(""); setMessage(`结算 #${result.id} 已确认，可在详情查看快照与退款冲正。`); onSaved(result.id); }
    catch (cause) {
      if (!(cause instanceof ApiError) || cause.status >= 500 || cause.status === 408) { pending.setUnknown(true); setError("结算结果暂无法确认。请重试原请求，勿重复新建结算。"); }
      else if ([401, 403].includes(cause.status)) { if (!wasUnknown) pending.resolve(); setForbidden(true); setPreview(null); setError(wasUnknown ? "登录或权限已失效，原结算请求已保留。请使用同一管理员重新登录，恢复权限后重试原请求。" : cause.message); }
      else { pending.resolve(); setError(cause.status === 409 ? `${cause.message}。请重新预览核对最新数据。` : cause.message); if (cause.status === 409) setPreview(null); }
    } finally { flight.current = false; setBusy(false); }
  }
  return <section className="space-y-4 rounded border bg-card p-4"><h2 className="font-semibold">新建结算</h2><p className="text-xs leading-6 text-muted-foreground">结算需具备销售管理权限及全部客户范围。按所选月份现金到账的新购订单计算；信用付款需全部还清，续费及升级另行处理。首次确认使用当前销售归属与提成规则并固化快照，后续配置变动不重算历史。退款会冲正对应佣金。</p>
    {pending.unknown && <p role="status" className="text-sm text-amber-700">有一笔结算结果尚未确认，原请求已保留。请在权限恢复后重试原请求。</p>}
    {!fullScope ? <>{pending.unknown ? <p role="status" className="text-sm text-amber-700">当前客户范围暂不允许重试原结算请求。原请求已保留，请恢复全部客户范围后再重试。</p> : null}<p className="text-sm text-muted-foreground">当前客户范围仅支持查看授权范围内的完整结算记录，请联系有全部客户范围的管理员确认结算。</p></> : <>
      <form onSubmit={loadPreview} className="space-y-4"><fieldset disabled={locked} className="grid gap-4 sm:grid-cols-[180px_1fr]"><label className="grid min-w-0 gap-2 text-sm">到账月份<Input type="month" aria-label="到账月份" max={lastClosedMonth()} required value={month} onChange={event => edit(event.target.value, ids)} /></label><label className="grid min-w-0 gap-2 text-sm">订单ID（最多100个）<Input aria-label="结算订单ID" value={ids} placeholder="例如：12, 13, 14" required onChange={event => edit(month, event.target.value)} /></label></fieldset><div className="flex flex-wrap gap-2"><Button type="submit" variant="outline" disabled={locked}>{busy ? "处理中…" : "预览结算"}</Button><Button type="button" variant="outline" disabled={locked} onClick={reset}>取消更改</Button></div></form>
      {preview && <div className="space-y-4 border-t pt-4"><h3 className="font-medium">结算预览 · {preview.month}</h3><Totals value={preview} /><ItemsTable items={preview.items} />{manage && <Button disabled={locked} onClick={() => void confirm()}>确认结算</Button>}</div>}
      {pending.unknown && <Button disabled={busy || !manage || forbidden} onClick={() => void confirm()}>重试原结算请求</Button>}
    </>}
    {error && <p role="alert" className="break-words text-sm text-destructive">{error}</p>}{message && <p role="status" className="text-sm">{message}</p>}
  </section>;
}

function Totals({ value }: { value: Pick<Preview, "commission_cents" | "reversed_cents" | "net_commission_cents"> }) {
  return <div className="grid gap-3 sm:grid-cols-3">{[["确认佣金", value.commission_cents], ["已冲正", value.reversed_cents], ["净佣金", value.net_commission_cents]].map(([label, amount]) => <div key={String(label)} className="rounded border bg-muted/20 p-3"><p className="text-xs text-muted-foreground">{label}</p><p className="mt-1 text-lg font-semibold">{money(Number(amount))}</p></div>)}</div>;
}
function ItemsTable({ items }: { items: Item[] }) {
  const invoices = useAdminPermission("invoices.view");
  const orders = useAdminPermission("orders.view");
  return <div className="overflow-x-auto rounded border"><table className="w-full min-w-[900px] text-sm"><thead><tr className="border-b text-left text-muted-foreground">{["订单 / 账单", "销售", "现金到账", "计佣净额", "月营业额", "基础 / 阶梯", "佣金", "规则版本", "状态"].map(label => <th key={label} className="whitespace-nowrap px-3 py-3">{label}</th>)}</tr></thead><tbody>{items.map(item => <tr key={item.invoice_id} className="border-b"><td className="whitespace-nowrap px-3 py-3">{orders ? <Link className="text-primary" href={`/admin/orders/${item.order_id}`}>订单 #{item.order_id}</Link> : `订单 #${item.order_id}`}<br />{invoices ? <Link className="text-primary" href={`/admin/invoices/${item.invoice_id}`}>账单 #{item.invoice_id}</Link> : `账单 #${item.invoice_id}`}</td><td className="px-3">{item.sales_owner_name}</td><td className="whitespace-nowrap px-3">{date(item.cash_received_at)}</td><td className="px-3">{money(item.basis_cents)}</td><td className="px-3">{money(item.monthly_turnover_cents)}</td><td className="whitespace-nowrap px-3">{(item.purchase_rate_bps / 100).toFixed(2)}% / {(item.tier_extra_rate_bps / 100).toFixed(2)}%</td><td className="px-3">{money(item.commission_cents)}</td><td className="whitespace-nowrap px-3">组 v{item.rule_version} · 阶梯 v{item.tier_version}</td><td className="whitespace-nowrap px-3">{item.status ? labels[item.status] ?? item.status : "待确认"}{item.reversed_at && <p className="text-xs text-muted-foreground">{date(item.reversed_at)}</p>}</td></tr>)}</tbody></table></div>;
}
