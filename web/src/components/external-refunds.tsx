"use client";

import { useRef, useState } from "react";
import { api, ApiError, money } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { useAdminIdentity, useAdminPermission } from "./workspace";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Textarea } from "./ui/textarea";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "./ui/dialog";
import type { ManualInvoiceRecord } from "./manual-invoices";
import type { PaymentRecord } from "./online-payment";

type Refund = { id: number; invoice_id: number; payment_id: number; user_id: number; amount_cents: number; status: "pending_review" | "confirmed" | "rejected"; version: number; method: string; provider_refund_id: string; received_at: string; evidence_ref: string; evidence_sha256: string; note: string; requested_by: number; reviewed_by: number | null; review_note: string; created_at: string; updated_at: string };
type Page = { items: Refund[]; total: number; limit: number; offset: number };
type Invoice = Omit<ManualInvoiceRecord, "items"> & { external_payment_paid: boolean; order_id: number | null; credit_payment?: unknown; items: { description: string; amount_cents: number; item_type: string; service_id: number | null }[]; payments: PaymentRecord[] };
const statuses = { pending_review: "待独立复核", confirmed: "已确认到账", rejected: "已驳回" };
const methods: Record<string, string> = { original_route: "原路退回", bank_transfer: "人工银行转账", other: "其他人工退付" };
const blank = () => ({ payment_id: "", method: "original_route", provider_refund_id: "", received_at: "", evidence_ref: "", evidence_sha256: "", note: "" });

export function ExternalRefunds({ invoice, refreshInvoice }: { invoice: Invoice; refreshInvoice: () => void }) {
  const view = useAdminPermission("external_refunds.view");
  const create = useAdminPermission("external_refunds.create");
  const review = useAdminPermission("external_refunds.review");
  const user = useAdminIdentity();
  const [offset, setOffset] = useState(0);
  const path = `/admin/invoices/${invoice.id}/external-refunds`;
  const list = useResource<Page>(view ? `${path}?limit=10&offset=${offset}` : null, false);
  const [draft, setDraft] = useState(blank);
  const [open, setOpen] = useState(false);
  const [action, setAction] = useState<{ record: Refund; kind: "confirm" | "reject" } | null>(null);
  const [note, setNote] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const [conflict, setConflict] = useState(false);
  const retry = useRef<{ payload: string; key: string } | null>(null);
  const payments = invoice.payments.filter(p => p.status === "succeeded" && p.amount_cents === invoice.amount_cents);
  const eligible = invoice.invoice_kind === "manual" && invoice.status === "paid" && invoice.external_payment_paid && !invoice.order_id && !invoice.credit_payment && !invoice.import_review_required && invoice.items.every(i => i.item_type === "manual" && !i.service_id) && payments.length > 0;
  const activeRefund = list.data?.items.some(r => r.status !== "rejected");
  const update = (key: keyof ReturnType<typeof blank>, value: string) => { setDraft(d => ({ ...d, [key]: value })); setError(""); };
  async function write(url: string, body: object) {
    const payload = JSON.stringify(body);
    const fingerprint = `${url}:${payload}`;
    if (retry.current?.payload !== fingerprint) retry.current = { payload: fingerprint, key: `external-refund-${crypto.randomUUID()}` };
    setBusy(true); setError(""); setNotice("");
    try {
      await api<{ refund: Refund; replayed: boolean }>(url, { method: "POST", body: payload, headers: { "Idempotency-Key": retry.current.key } });
      setOpen(false); setAction(null); setDraft(blank()); setNote(""); setConflict(false); retry.current = null;
      setNotice("退款记录已保存。原收款记录保留，未增加客户余额。"); list.refresh(); refreshInvoice();
    } catch (e) {
      setError((e as Error).message);
      if (e instanceof ApiError && e.status === 409) { setConflict(true); list.refresh(); refreshInvoice(); setError(`${e.message}。已请求刷新版本，草稿已保留；请核对最新账单与记录后再提交。`); }
    } finally { setBusy(false); }
  }
  function submit(now: number) {
    const received = new Date(draft.received_at);
    if (!payments.some(p => p.id === Number(draft.payment_id)) || !draft.provider_refund_id.trim() || !draft.evidence_ref.trim() || !/^[a-fA-F0-9]{64}$/.test(draft.evidence_sha256) || !draft.note.trim() || !Number.isFinite(received.getTime()) || received.getTime() > now) { setError("请选择原支付，填写退款流水、有效到账时间（不可晚于现在）、证据引用、64 位 SHA-256 和说明。"); return; }
    if (!window.confirm(`确认登记整单外部退款 ${money(invoice.amount_cents)}？此操作不执行退款，独立复核后才确认出款事实。`)) return;
    void write(path, { ...draft, payment_id: Number(draft.payment_id), amount_cents: invoice.amount_cents, provider_refund_id: draft.provider_refund_id.trim(), evidence_ref: draft.evidence_ref.trim(), note: draft.note.trim(), evidence_sha256: draft.evidence_sha256.toLowerCase(), received_at: received.toISOString(), expected_version: invoice.version });
  }
  function submitReview() {
    if (!action || !note.trim()) { setError("请填写复核说明。"); return; }
    const record = list.data?.items.find(r => r.id === action.record.id) ?? action.record;
    if (record.status !== "pending_review") { setError("该记录已被处理，请关闭后查看最新结果。"); return; }
    if (!window.confirm(action.kind === "confirm" ? `确认已核实付款人真实到账 ${money(record.amount_cents)}，并完成独立复核？确认将记录出款并更新账单。` : "确认驳回该退款登记？驳回不会执行任何资金操作。")) return;
    void write(`${path}/${record.id}/${action.kind}`, { expected_version: record.version, note: note.trim() });
  }
  if (!view) return null;
  return <section className="rounded-xl border bg-card p-4 sm:p-6 space-y-4 min-w-0">
    <div className="flex flex-wrap gap-3 items-center justify-between"><h2 className="font-semibold">外部退款登记与复核</h2><div className="flex flex-wrap gap-2"><Button size="sm" variant="outline" disabled={busy} onClick={() => { list.refresh(); refreshInvoice(); }}>刷新</Button>{create && <Button size="sm" disabled={busy || !eligible || !list.data || !!list.error || activeRefund} onClick={() => { setOpen(true); setError(""); setConflict(false); }}>登记外部退款</Button>}</div></div>
    <p className="text-sm text-muted-foreground">这里只登记已在外部发生的退款，不执行退款、不入客户余额。确认须核实真实到账并由其他员工独立复核；原收款记录永久保留。仅支持无交付及其他业务关联的普通人工账单整单退款，最终资格由服务端重验。</p>
    {!eligible && <p className="text-sm">此账单当前不满足登记条件（须已付普通人工账单、匹配成功外部收款且无订单、资源或信用等关联）。</p>}
    {notice && <p role="status" className="text-sm">{notice}</p>}
    {list.error ? <Failure error={list.error} retry={list.refresh} /> : !list.data ? <Loading /> : <>
      {list.data.items.length === 0 ? <p className="text-sm text-muted-foreground">暂无外部退款记录。</p> : <div className="space-y-3">{list.data.items.map(r => <article key={r.id} className="rounded-lg border p-3 space-y-2 text-sm break-all min-w-0">
        <div className="flex flex-wrap gap-2 justify-between"><strong>#{r.id} · {money(r.amount_cents)}</strong><span>{statuses[r.status]}</span></div>
        <p>原支付 #{r.payment_id} · {methods[r.method] ?? r.method} · 退款流水 {r.provider_refund_id}</p><p>实际到账：{new Date(r.received_at).toLocaleString("zh-CN")}</p><p>证据引用：{r.evidence_ref}</p><p className="text-xs">SHA-256：{r.evidence_sha256}</p><p>登记说明：{r.note}</p><p>申请人 #{r.requested_by} · 复核人 {r.reviewed_by ? `#${r.reviewed_by}` : "待复核"} · 版本 {r.version}</p>{r.review_note && <p>复核说明：{r.review_note}</p>}
        {review && r.status === "pending_review" && (r.requested_by === user?.id ? <p className="text-muted-foreground">申请人不得自行复核，请由其他有权限员工处理。</p> : <div className="flex flex-wrap gap-2">{(["confirm", "reject"] as const).map(kind => <Button key={kind} size="sm" variant={kind === "confirm" ? "destructive" : "outline"} disabled={busy} onClick={() => { setAction({ record: r, kind }); setNote(""); setError(""); setConflict(false); }}>{kind === "confirm" ? "确认真实到账" : "驳回登记"}</Button>)}</div>)}
      </article>)}</div>}
      <div className="flex flex-wrap gap-3 items-center justify-between text-sm"><span>共 {list.data.total} 条 · 第 {Math.floor(offset / 10) + 1} 页</span><div className="flex gap-2"><Button size="sm" variant="outline" disabled={offset === 0 || busy} onClick={() => setOffset(Math.max(0, offset - 10))}>上一页</Button><Button size="sm" variant="outline" disabled={offset + list.data.items.length >= list.data.total || busy} onClick={() => setOffset(offset + 10)}>下一页</Button></div></div>
    </>}
    <Dialog open={open} onOpenChange={value => { if (!busy) setOpen(value); }}><DialogContent className="max-h-[90dvh] overflow-y-auto min-w-0" showCloseButton={!busy}><DialogHeader><DialogTitle>登记外部退款 · {money(invoice.amount_cents)}</DialogTitle><DialogDescription>仅登记已经发生的整单退付。请勿填写密钥或完整个人敏感资料。取消不会提交。</DialogDescription></DialogHeader><fieldset disabled={busy} className="space-y-3 min-w-0">
      <div className="space-y-1"><Label htmlFor="refund-payment">原外部支付</Label><select id="refund-payment" className="w-full min-w-0 rounded-md border p-2 text-sm" value={draft.payment_id} onChange={e => update("payment_id", e.target.value)}><option value="">请选择成功收款</option>{payments.map(p => <option key={p.id} value={p.id}>#{p.id} · {p.provider} · {money(p.amount_cents)}</option>)}</select></div>
      <div className="space-y-1"><Label htmlFor="refund-method">资金路径</Label><select id="refund-method" className="w-full rounded-md border p-2 text-sm" value={draft.method} onChange={e => update("method", e.target.value)}>{Object.entries(methods).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></div>
      {([ ["provider_refund_id", "外部退款流水", "text", 200], ["received_at", "实际到账时间", "datetime-local", 100], ["evidence_ref", "证据引用（受控凭证编号或位置）", "text", 500], ["evidence_sha256", "证据 SHA-256（64 位十六进制）", "text", 64] ] as const).map(([key, label, type, max]) => <div className="space-y-1" key={key}><Label htmlFor={`refund-${key}`}>{label}</Label><Input id={`refund-${key}`} type={type} maxLength={max} value={draft[key]} onChange={e => update(key, e.target.value)} /></div>)}
      <Label htmlFor="refund-note">登记说明</Label><Textarea id="refund-note" maxLength={1000} value={draft.note} onChange={e => update("note", e.target.value)} />
    </fieldset>{error && <p role="alert" className="text-sm text-destructive break-words">{error}</p>}{conflict && <p className="text-sm">当前账单版本 {invoice.version}。核对后提交将使用此版本。</p>}<DialogFooter><Button variant="outline" disabled={busy} onClick={() => setOpen(false)}>取消</Button><Button disabled={busy || !eligible || !create || !list.data || !!list.error} onClick={() => submit(Date.now())}>{busy ? "提交中…" : "核对并登记"}</Button></DialogFooter></DialogContent></Dialog>
    <Dialog open={!!action} onOpenChange={value => { if (!value && !busy) setAction(null); }}><DialogContent className="max-h-[90dvh] overflow-y-auto" showCloseButton={!busy}><DialogHeader><DialogTitle>{action?.kind === "confirm" ? "独立复核：确认真实到账" : "驳回外部退款登记"}</DialogTitle><DialogDescription>核对原支付、退款流水、到账时间与证据。商户受理不代表付款人已到账；申请人不得自审。</DialogDescription></DialogHeader><p className="text-sm">记录 #{action?.record.id} · {money(action?.record.amount_cents ?? 0)}</p><Label htmlFor="refund-review-note">复核说明（必填）</Label><Textarea id="refund-review-note" disabled={busy} maxLength={1000} value={note} onChange={e => setNote(e.target.value)} />{error && <p role="alert" className="text-sm text-destructive break-words">{error}</p>}{conflict && <p className="text-sm">草稿已保留，请核对刷新后的记录再提交。</p>}<DialogFooter><Button variant="outline" disabled={busy} onClick={() => setAction(null)}>取消</Button><Button variant="destructive" disabled={busy || !note.trim() || !review || action?.record.requested_by === user?.id || !!list.error || !list.data} onClick={() => submitReview()}>{busy ? "提交中…" : action?.kind === "confirm" ? "核实并确认" : "确认驳回"}</Button></DialogFooter></DialogContent></Dialog>
  </section>;
}
