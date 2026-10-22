"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { date, type Service, type User } from "@/lib/api";
import { can } from "@/lib/admin-navigation";
import { useAdminIdentity } from "./workspace";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Checkbox } from "./ui/checkbox";
import { serviceAdjustmentCents } from "./admin-service-adjustments";
import { serviceAdjustmentAuthority } from "./admin-service-adjustment-mutation";
import { useServiceRenewalMutation, type RenewalInvoice, type RenewalQuote } from "./admin-service-renewal-mutation";

const money = (amount: number) => { const value = BigInt(amount); return `¥${value / BigInt(100)}.${String(value % BigInt(100)).padStart(2, "0")}`; };
const panel = "min-w-0 space-y-4 rounded border bg-card p-4 sm:p-5";
const invoiceStatus: Record<string, string> = { unpaid: "未付款", paid: "已付款", cancelled: "已取消", refunded: "已退款", partially_refunded: "部分退款" };

export function AdminServiceRenewals({ service, refresh }: { service: Service; refresh: () => void }) {
  const actor = useAdminIdentity();
  if (!actor || !can(actor, "services.view") || !can(actor, "invoices.view")) return <section className={panel}><h2 className="font-semibold">管理员代续费</h2><p role="status" className="text-sm text-muted-foreground">需要服务与账单查看权限。</p></section>;
  const owner = service.owner_generation;
  if (![service.id, service.user_id, service.version].every(v => Number.isSafeInteger(v) && v > 0) || typeof owner !== "number" || !Number.isSafeInteger(owner) || owner < 0) return <p role="alert" className="text-destructive">服务身份或归属代次缺失，请重新读取服务。</p>;
  return <RenewalWorkspace key={`${serviceAdjustmentAuthority(actor)}:${service.id}:${service.user_id}:${owner}`} actor={actor} service={service} owner={owner} refresh={refresh} />;
}

function RenewalWorkspace({ actor, service, owner, refresh }: { actor: User; service: Service; owner: number; refresh: () => void }) {
  const mutation = useServiceRenewalMutation(actor, service, owner);
  const canCreate = can(actor, "services.renew"), canFixed = canCreate && can(actor, "services.renew.override"), canPay = can(actor, "services.renew.pay_balance");
  const [mode, setMode] = useState<"normal" | "fixed">("normal");
  const [amount, setAmount] = useState(""), [reason, setReason] = useState(""), [payReason, setPayReason] = useState("");
  const [quote, setQuote] = useState<RenewalQuote | null>(null), [invoice, setInvoice] = useState<RenewalInvoice | null>(null);
  const [invoiceID, setInvoiceID] = useState(""), [confirmed, setConfirmed] = useState(false), [payConfirmed, setPayConfirmed] = useState(false);
  const [error, setError] = useState(""), [expiredHash, setExpiredHash] = useState("");
  const expired = !!quote && expiredHash === quote.quote_hash;
  useEffect(() => {
    if (!quote?.can_create) return;
    const timer = window.setTimeout(() => setExpiredHash(quote.quote_hash), Math.max(0, Date.parse(quote.expires_at) - Date.now()));
    return () => window.clearTimeout(timer);
  }, [quote]);
  function clearPreview() { setQuote(null); setConfirmed(false); setError(""); }
  async function preview() {
    clearPreview(); setInvoice(null); setPayConfirmed(false);
    try { setQuote(await mutation.preview(mode === "fixed" ? serviceAdjustmentCents(amount) : undefined)); }
    catch (e) { setError((e as Error).message); }
  }
  async function readInvoice(id: number) {
    setInvoice(null); setPayConfirmed(false); setError("");
    const value = await mutation.readInvoice(id); if (value) { setInvoice(value); setInvoiceID(String(value.id)); }
  }
  async function create() {
    if (!quote || !confirmed || !quote.can_create || expired || mutation.frozen) return;
    const result = await mutation.run({ kind: mode, quote, body: { expected_version: quote.service_version, quote_hash: quote.quote_hash, reason: reason.trim(), ...(mode === "fixed" ? { amount_cents: quote.amount_cents } : {}) } });
    if (result) { clearPreview(); setInvoice(result.facts.invoice); setPayConfirmed(false); setReason(""); refresh(); }
  }
  async function pay() {
    if (!invoice || !payConfirmed || mutation.frozen) return;
    const result = await mutation.run({ kind: "pay", invoice, body: { expected_version: invoice.version, amount_cents: invoice.amount_cents, reason: payReason.trim() } });
    if (result) { setInvoice(result.facts.invoice); setPayConfirmed(false); setPayReason(""); refresh(); }
  }
  async function recover() {
    const result = await mutation.run();
    if (result) { clearPreview(); setInvoice(result.facts.invoice); setPayConfirmed(false); refresh(); }
  }
  async function finishRejected() {
    const result = await mutation.finishRejected();
    if (result) { clearPreview(); setInvoice(result.invoice); setPayConfirmed(false); refresh(); }
  }
  const current = mutation.facts;
  return <section className={panel} aria-label="管理员代续费">
    <header className="space-y-1"><h2 className="text-base font-semibold">管理员代续费</h2><p className="text-sm text-muted-foreground">客户 #{service.user_id} · 服务 #{service.id} · {service.name}。先报价、代建未付单，再单独确认余额付款；本次固定应付不改变长期续费价。</p></header>
    {(error || mutation.error) && <p role="alert" className="break-words text-sm text-destructive">{error || mutation.error}</p>}
    {mutation.pending && <div className="space-y-3 rounded border p-3 text-sm" role="status">
      <p>原请求：{mutation.pending.kind === "pay" ? `客户余额付款 · 账单 #${mutation.pending.invoice?.id}` : mutation.pending.kind === "fixed" ? "本次固定应付建单" : "普通续费建单"} · {money(mutation.pending.kind === "pay" ? mutation.pending.invoice!.amount_cents : mutation.pending.quote!.amount_cents)}</p>
      <p className="break-words text-muted-foreground">原因：{String(mutation.pending.body.reason)}</p>
      <p className="text-muted-foreground">{mutation.pending.phase === "rejected" ? "原请求已明确拒绝，重新操作前先核对当前事实。" : "结果待核验，已保存原请求和请求键。恢复不会创建新意图；请勿清除浏览器资料后重复操作。"}</p>
      <Button variant="outline" disabled={mutation.busy || mutation.blocked} onClick={() => void (mutation.pending?.phase === "rejected" ? finishRejected() : recover())}>{mutation.pending.phase === "rejected" ? "读取当前事实并结束被拒请求" : "恢复原续费请求"}</Button>
    </div>}
    {mutation.receipt && <div role="status" className="space-y-2 rounded bg-muted/40 p-3 text-sm">
      <p>{mutation.receipt.replayed ? "原请求历史回执已恢复" : "本次请求已核验"}：账单 {mutation.receipt.invoice_no} · {money(mutation.receipt.amount_cents)} · {invoiceStatus[mutation.receipt.status] ?? mutation.receipt.status}</p>
      {mutation.receipt.renewal_status && <p>{mutation.receipt.renewal_status === "pending" ? "本地收款已完成，供应商续费仍待处理；请继续核对服务、任务和上游回读。" : "该次本地续费结算已完成。"}</p>}
      <p className="text-muted-foreground">历史回执不代表当前状态，以下为另行读取的当前事实。</p>
    </div>}
    {current && <p className="text-sm text-muted-foreground">当前服务版本 v{current.management.service_version} · 到期 {current.management.next_due_at ? date(current.management.next_due_at) : "未设置"}{current.invoice ? ` · 当前账单 ${invoiceStatus[current.invoice.status] ?? current.invoice.status}（v${current.invoice.version}）` : ""}</p>}
    <div className="grid min-w-0 items-start gap-5 xl:grid-cols-2">
      <div className="min-w-0 space-y-4 rounded border p-4 text-sm">
        <h3 className="font-semibold">1. 预览并代建续费单</h3>
        {!canCreate ? <p className="text-muted-foreground">无代建权限。已有续费账单可在右侧读取；余额付款需要独立授权。</p> : <>
          {canFixed && <label className="block space-y-2"><span>续费方式</span><select aria-label="续费方式" className="h-10 w-full rounded border bg-background px-3" value={mode} disabled={mutation.frozen} onChange={e => { setMode(e.target.value as "normal" | "fixed"); clearPreview(); }}><option value="normal">普通续费（当前有效定价）</option><option value="fixed">本次固定应付</option></select></label>}
          {mode === "fixed" && <label className="block space-y-2"><span>本次应付金额（元）</span><Input aria-label="本次应付金额（元）" inputMode="decimal" value={amount} disabled={mutation.frozen} placeholder="请输入金额，允许 0.00" onChange={e => { setAmount(e.target.value); clearPreview(); }}/></label>}
          <Button variant="outline" disabled={mutation.frozen} onClick={() => void preview()}>预览续费报价</Button>
          {quote && <div className="space-y-3 rounded bg-muted/30 p-3">
            <p>周期 {quote.billing_cycle} · 基础 {money(quote.base_cents)} · 优惠 {money(quote.discount_cents)}</p><p className="font-semibold">本次应付 {money(quote.amount_cents)}</p>
            {!quote.can_create ? <><p>已有续费单 #{quote.pending_invoice_id}。上述金额仅为当前估价，付款以该账单为准。</p><Button variant="outline" disabled={mutation.frozen} onClick={() => void readInvoice(quote.pending_invoice_id)}>读取已有续费账单</Button></> : <>
              <p className="text-muted-foreground">有效至 {date(quote.expires_at)} · 服务版本 v{quote.service_version}</p>
              {expired && <p role="alert" className="text-destructive">报价已过期，请重新预览。</p>}
              <label className="block space-y-2"><span>代续费原因</span><Textarea aria-label="代续费原因" value={reason} disabled={mutation.frozen} onChange={e => setReason(e.target.value)} placeholder="1–240 字，留存审计"/></label>
              <label className="flex items-start gap-2"><Checkbox checked={confirmed} disabled={mutation.frozen || expired} onCheckedChange={v => setConfirmed(v === true)}/><span>确认给客户 #{service.user_id} 的服务 #{service.id} 生成 {money(quote.amount_cents)} 未付续费单{quote.amount_cents === 0 ? "；零元仍需下一步显式结算" : ""}。</span></label>
              <Button disabled={mutation.frozen || expired || !confirmed || !reason.trim()} onClick={() => void create()}>确认代建续费单</Button>
            </>}
          </div>}
        </>}
      </div>
      <div className="min-w-0 space-y-4 rounded border p-4 text-sm">
        <h3 className="font-semibold">2. 核对账单与客户余额付款</h3>
        <form className="flex flex-wrap items-end gap-2" onSubmit={e => { e.preventDefault(); if (/^[1-9]\d*$/.test(invoiceID) && Number.isSafeInteger(Number(invoiceID))) void readInvoice(Number(invoiceID)); else setError("请输入有效的续费账单 ID。"); }}>
          <label className="min-w-0 flex-1 space-y-2"><span>续费账单 ID</span><Input aria-label="续费账单 ID" inputMode="numeric" value={invoiceID} disabled={mutation.frozen} onChange={e => { setInvoiceID(e.target.value); setInvoice(null); setPayConfirmed(false); }}/></label><Button type="submit" variant="outline" disabled={mutation.frozen}>读取续费账单</Button>
        </form>
        {invoice && <div className="space-y-3 rounded bg-muted/30 p-3">
          <p className="break-all"><Link href={`/admin/invoices/${invoice.id}`} className="text-primary underline">{invoice.invoice_no}</Link> · {invoiceStatus[invoice.status] ?? invoice.status} · v{invoice.version}</p><p className="font-semibold">账单应付 {money(invoice.amount_cents)}</p>
          {invoice.status === "unpaid" && !invoice.import_review_required && !invoice.payment_pending ? canPay ? <>
            <label className="block space-y-2"><span>余额代付原因</span><Textarea aria-label="余额代付原因" value={payReason} disabled={mutation.frozen} onChange={e => setPayReason(e.target.value)} placeholder="1–500 字，留存审计"/></label>
            <label className="flex items-start gap-2"><Checkbox checked={payConfirmed} disabled={mutation.frozen} onCheckedChange={v => setPayConfirmed(v === true)}/><span>确认从客户 #{service.user_id} 余额支付 {money(invoice.amount_cents)}，结算账单 #{invoice.id}{invoice.amount_cents === 0 ? "（零元显式结算）" : ""}。</span></label>
            <Button disabled={mutation.frozen || !payConfirmed || !payReason.trim()} onClick={() => void pay()}>确认客户余额付款</Button>
          </> : <p className="text-muted-foreground">无客户余额代付权限。</p> : <p className="text-muted-foreground">当前账单不可余额代付；请在账单详情核对状态、支付或审核事项。</p>}
        </div>}
        <p className="text-xs text-muted-foreground">余额不足会保留未付账单，不自动充值；本地付款不代表上游已续费。</p>
      </div>
    </div>
  </section>;
}
