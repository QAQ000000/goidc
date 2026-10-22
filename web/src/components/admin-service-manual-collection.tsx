"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import { date, type Service, type User } from "@/lib/api";
import { can } from "@/lib/admin-navigation";
import { useAdminIdentity } from "./workspace";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Checkbox } from "./ui/checkbox";
import { serviceAdjustmentAuthority } from "./admin-service-adjustment-mutation";
import { useServiceManualCollection, type CollectionDetails, type CollectionInvoice, type CollectionRequest } from "./admin-service-manual-collection-mutation";

const panel = "min-w-0 space-y-4 rounded border bg-card p-4 sm:p-5";
const money = (n: number) => { const v = BigInt(n); return `¥${v / BigInt(100)}.${String(v % BigInt(100)).padStart(2, "0")}`; };
const methods: Record<string, string> = { bank_transfer: "银行转账", cash: "现金", other_offline: "其他线下方式" };
const statuses: Record<string, string> = { unpaid: "未付款", paid: "已付款", cancelled: "已取消", refunded: "已退款", partially_refunded: "部分退款" };
const emptyDetails: CollectionDetails = { received_at: "", method: "bank_transfer", receiving_account: "", transaction_no: "", evidence_ref: "", evidence_sha256: "", reason: "" };

export function AdminServiceManualCollection({ service, refresh }: { service: Service; refresh: () => void }) {
  const actor = useAdminIdentity();
  if (!actor || !["services.view", "invoices.view", "invoices.collect_manual"].every(p => can(actor, p))) return <section className={panel}><h2 className="font-semibold">人工续费收款</h2><p role="status" className="text-sm text-muted-foreground">需要服务查看、账单查看和人工收款权限。</p></section>;
  const owner = service.owner_generation;
  if (![service.id, service.user_id, service.version].every(v => Number.isSafeInteger(v) && v > 0) || typeof owner !== "number" || !Number.isSafeInteger(owner) || owner < 0) return <p role="alert" className="text-destructive">服务身份或归属代次缺失，请重新读取服务。</p>;
  return <CollectionWorkspace key={`${serviceAdjustmentAuthority(actor)}:${service.id}:${service.user_id}:${owner}`} actor={actor} service={service} owner={owner} refresh={refresh}/>;
}

function CollectionWorkspace({ actor, service, owner, refresh }: { actor: User; service: Service; owner: number; refresh: () => void }) {
  const mutation = useServiceManualCollection(actor, service, owner);
  const [invoiceID, setInvoiceID] = useState(""), [invoice, setInvoice] = useState<CollectionInvoice | null>(null);
  const [details, setDetails] = useState<CollectionDetails>(emptyDetails), [request, setRequest] = useState<CollectionRequest | null>(null);
  const [confirmed, setConfirmed] = useState(false), [error, setError] = useState(""), [expiredHash, setExpiredHash] = useState("");
  const [hashing, setHashing] = useState(false), [evidenceFile, setEvidenceFile] = useState("");
  const fileGeneration = useRef(0), formRevision = useRef(0), active = useRef(true);
  // Cancel pending file reads on unmount; this ref is a generation counter, not a DOM node.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => { active.current = true; return () => { active.current = false; fileGeneration.current++; }; }, []);
  useEffect(() => {
    if (!request) return;
    const timer = window.setTimeout(() => setExpiredHash(request.quote.quote_hash), Math.max(0, Date.parse(request.quote.expires_at) - Date.now()));
    return () => window.clearTimeout(timer);
  }, [request]);
  const expired = !!request && expiredHash === request.quote.quote_hash;
  function invalidate() { formRevision.current++; setRequest(null); setConfirmed(false); setError(""); }
  function change(key: keyof CollectionDetails, value: string) { invalidate(); setDetails(previous => ({ ...previous, [key]: value })); }
  async function digestFile(file?: File) {
    const generation = ++fileGeneration.current; invalidate(); setEvidenceFile(""); setDetails(previous => ({ ...previous, evidence_sha256: "" }));
    if (!file) { setHashing(false); return; }
    if (file.size > 20 * 1024 * 1024) { setHashing(false); setError("请选择不超过 20 MB 的到账凭据文件，或粘贴可信归档提供的摘要。"); return; }
    setHashing(true);
    try {
      const digest = await crypto.subtle.digest("SHA-256", await file.arrayBuffer());
      if (!active.current || fileGeneration.current !== generation) return;
      const hex = Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, "0")).join("");
      invalidate(); setDetails(previous => ({ ...previous, evidence_sha256: hex })); setEvidenceFile(file.name);
    } catch { if (active.current && fileGeneration.current === generation) setError("无法计算凭据文件摘要，请使用 HTTPS 或粘贴可信归档的 SHA-256。"); }
    finally { if (active.current && fileGeneration.current === generation) setHashing(false); }
  }
  async function readInvoice() {
    invalidate(); setInvoice(null);
    if (!/^[1-9]\d*$/.test(invoiceID) || !Number.isSafeInteger(Number(invoiceID))) { setError("请输入有效的续费账单 ID。"); return; }
    const revision = formRevision.current, value = await mutation.readInvoice(Number(invoiceID));
    if (active.current && revision === formRevision.current) setInvoice(value);
  }
  async function preview() {
    invalidate(); if (!invoice || hashing) return;
    const revision = formRevision.current, value = await mutation.preview(invoice, details);
    if (active.current && revision === formRevision.current) setRequest(value);
  }
  async function submit() {
    if (!request || !confirmed || expired || hashing || mutation.frozen) return;
    const result = await mutation.run(request);
    if (result) { invalidate(); setInvoice(result.facts.invoice); refresh(); }
  }
  async function recover() {
    const result = await mutation.run();
    if (result) { invalidate(); setInvoiceID(String(result.facts.invoice.id)); setInvoice(result.facts.invoice); refresh(); }
  }
  async function finishRejected() {
    const latest = await mutation.finishRejected();
    if (latest) { invalidate(); setInvoice(latest.invoice); setInvoiceID(String(latest.invoice.id)); refresh(); }
  }
  const pending = mutation.pending, receipt = mutation.receipt, facts = mutation.facts;
  const payable = invoice && invoice.status === "unpaid" && invoice.amount_cents > 0 && !invoice.import_review_required && !invoice.payment_pending;
  return <section className={panel} aria-label="人工续费收款">
    <header className="space-y-1"><h2 className="text-base font-semibold">人工续费收款</h2><p className="text-sm text-muted-foreground">客户 #{service.user_id} · 服务 #{service.id}。仅登记已核实的线下全额到账，不扣客户余额；本地收款不代表上游续费完成。</p></header>
    {(error || mutation.error) && <p role="alert" className="break-words text-sm text-destructive">{error || mutation.error}</p>}
    {pending && <div role="status" className="space-y-3 rounded border p-3 text-sm">
      <p>原人工收款：账单 {pending.invoice.invoice_no} · {money(pending.body.amount_cents)} · {methods[pending.body.method]}</p>
      <p className="break-all">流水：{pending.body.transaction_no} · 到账：{pending.body.received_at}</p>
      <p className="text-muted-foreground">{pending.phase === "rejected" ? "原请求已明确拒绝。重新预览前先核对当前账单和服务。" : "已保存原始收款内容和请求键。结果待核验时只能恢复原请求，请勿清除浏览器资料后重复收款。"}</p>
      <Button variant="outline" disabled={mutation.busy || mutation.blocked} onClick={() => void (pending.phase === "rejected" ? finishRejected() : recover())}>{pending.phase === "rejected" ? "核对事实并结束被拒收款" : "恢复原人工收款请求"}</Button>
    </div>}
    {receipt && <div role="status" className="space-y-2 rounded bg-muted/40 p-3 text-sm">
      <p>{receipt.replayed ? "原人工收款历史回执已恢复" : "人工收款已核验"}：{receipt.invoice_no} · {money(receipt.amount_cents)} · 收款记录 #{receipt.collection_id} · 支付记录 #{receipt.payment_id}</p>
      <p>{receipt.renewal_status === "pending" ? "本地收款完成，供应商续费仍待处理，请核对任务和上游回读。" : "该次本地续费结算完成。"}</p>
      <p className="text-muted-foreground">历史回执与当前状态分别展示。</p>
    </div>}
    {facts && <p className="text-sm text-muted-foreground">当前账单 {statuses[facts.invoice.status] ?? facts.invoice.status}（v{facts.invoice.version}）· 当前服务 v{facts.management.service_version} · 到期 {facts.management.next_due_at ? date(facts.management.next_due_at) : "未设置"}</p>}
    <form className="flex flex-wrap items-end gap-2" onSubmit={e => { e.preventDefault(); void readInvoice(); }}>
      <label className="min-w-0 flex-1 space-y-2"><span className="text-sm">人工收款续费账单 ID</span><Input aria-label="人工收款续费账单 ID" inputMode="numeric" value={invoiceID} disabled={mutation.frozen || hashing} onChange={e => { invalidate(); setInvoiceID(e.target.value); setInvoice(null); }}/></label>
      <Button type="submit" variant="outline" disabled={mutation.frozen || hashing}>读取人工收款账单</Button>
    </form>
    {invoice && <div className="space-y-2 rounded bg-muted/30 p-3 text-sm">
      <p className="break-all"><Link className="text-primary underline" href={`/admin/invoices/${invoice.id}`}>{invoice.invoice_no}</Link> · {statuses[invoice.status] ?? invoice.status} · v{invoice.version}</p>
      <p className="font-semibold">全额实收 {money(invoice.amount_cents)}</p><p className="text-muted-foreground">账单创建：{date(invoice.created_at)}。金额和版本来自该续费账单。</p>
      {!payable && <p role="status">当前账单不可人工收款。仅支持无待确认支付、已复核的正数未付单；零元请使用独立显式结算。</p>}
    </div>}
    {payable && <div className="space-y-4">
      <fieldset disabled={mutation.frozen} className="grid min-w-0 gap-4 text-sm md:grid-cols-2">
        <label className="min-w-0 space-y-2"><span>实际到账时间（含时区）</span><Input aria-label="实际到账时间（含时区）" value={details.received_at} placeholder="2026-10-22T10:30:00+08:00" onChange={e => change("received_at", e.target.value)}/><Button type="button" variant="outline" size="sm" onClick={() => change("received_at", new Date().toISOString())}>填入当前时间</Button></label>
        <label className="min-w-0 space-y-2"><span>线下收款方式</span><select aria-label="线下收款方式" className="h-10 w-full rounded border bg-background px-3" value={details.method} onChange={e => change("method", e.target.value)}>{Object.entries(methods).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
        <label className="min-w-0 space-y-2"><span>收款账户标识</span><Input aria-label="收款账户标识" value={details.receiving_account} maxLength={120} placeholder="例如对公账户尾号1234，勿填密码" onChange={e => change("receiving_account", e.target.value)}/></label>
        <label className="min-w-0 space-y-2"><span>原始到账流水号</span><Input aria-label="原始到账流水号" value={details.transaction_no} maxLength={160} onChange={e => change("transaction_no", e.target.value)}/></label>
        <label className="min-w-0 space-y-2 md:col-span-2"><span>到账凭据归档引用</span><Input aria-label="到账凭据归档引用" value={details.evidence_ref} maxLength={500} placeholder="可信档案编号或引用，不含密钥" onChange={e => change("evidence_ref", e.target.value)}/></label>
        <label className="min-w-0 space-y-2 md:col-span-2"><span>凭据文件（仅本地计算摘要）</span><Input aria-label="凭据文件（仅本地计算摘要）" type="file" onChange={e => void digestFile(e.target.files?.[0])}/><span className="block text-xs text-muted-foreground">文件不上传，不代替凭据归档。请确保文件与上述归档对应；最多 20 MB。{hashing ? "正在计算 SHA-256…" : evidenceFile ? `已计算：${evidenceFile}` : "也可填写可信归档提供的摘要。"}</span></label>
        <label className="min-w-0 space-y-2 md:col-span-2"><span>凭据 SHA-256</span><Input aria-label="凭据 SHA-256" className="font-mono text-xs" value={details.evidence_sha256} maxLength={64} onChange={e => { fileGeneration.current++; setHashing(false); setEvidenceFile(""); change("evidence_sha256", e.target.value); }}/></label>
        <label className="min-w-0 space-y-2 md:col-span-2"><span>人工收款原因</span><Textarea aria-label="人工收款原因" value={details.reason} maxLength={500} onChange={e => change("reason", e.target.value)}/></label>
      </fieldset>
      <Button variant="outline" disabled={mutation.frozen || hashing} onClick={() => void preview()}>预览人工收款</Button>
    </div>}
    {request && <div className="space-y-3 rounded bg-muted/30 p-3 text-sm">
      <h3 className="font-semibold">核对线下到账事实</h3>
      <p className="break-all">账单 {request.invoice.invoice_no} · 客户 #{service.user_id} · 服务 #{service.id} · 全额 {money(request.body.amount_cents)}</p>
      <p className="break-all">{methods[request.body.method]} · {request.body.receiving_account} · {request.body.transaction_no}</p>
      <p className="break-all">到账 {request.body.received_at} · 凭据 {request.body.evidence_ref}</p>
      <p className="break-all font-mono text-xs">SHA-256：{request.body.evidence_sha256}</p><p className="break-words">原因：{request.body.reason}</p>
      <p className="text-muted-foreground">预览有效至 {date(request.quote.expires_at)}。修改任何收款内容后须重新预览。</p>
      {expired && <p role="alert" className="text-destructive">人工收款预览已过期，请重新预览。</p>}
      <label className="flex items-start gap-2"><Checkbox aria-label="确认真实全额到账" checked={confirmed} disabled={mutation.frozen || expired || hashing} onCheckedChange={value => setConfirmed(value === true)}/><span>已核对真实全额到账、原始流水与归档凭据，确认登记并结算此续费账单，不扣客户余额。</span></label>
      <Button disabled={mutation.frozen || !confirmed || expired || hashing} onClick={() => void submit()}>确认登记人工收款</Button>
    </div>}
  </section>;
}
