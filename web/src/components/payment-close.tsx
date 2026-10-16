"use client";

import { useEffect, useRef, useState } from "react";
import { api, ApiError, date, money, type User } from "@/lib/api";
import { can } from "@/lib/admin-navigation";
import { useAdminIdentity, useAdminPermission } from "./workspace";
import type { PaymentRecord } from "./online-payment";
import { Button } from "./ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "./ui/dialog";

type Operation = { id: number; operation_id: string; status: string; result_code: string; actor_id: number; expected_version: number; started_at: string; completed_at: string | null };
type CloseFacts = { payment_id: number; invoice_id: number; status: string; version: number; close_supported: boolean; operations_total: number; anomalies_total: number; operations: Operation[]; anomalies: { id: number; status: string; amount_cents: number; external_transaction_id: string; created_at: string }[] };
type InvoiceFacts = { id: number; status: string; version: number; payment_close_anomaly?: boolean; payments: PaymentRecord[] };
type Pending = { path: string; key: string; body: string; rejected: boolean };
type Receipt = { payment_id: number; invoice_id: number; status: string; version: number; operation_id: string; replayed: boolean };
const closeStatuses: Record<string, string> = { checking: "关单核验中", closed: "渠道确认最终关闭", succeeded: "渠道报告已收款，未解除保护", unknown: "关单结果未知，继续冻结", error: "未确认关闭，原支付保留" };
const rejectionCodes = new Set(["stale_invoice", "idempotency_conflict", "invoice_not_unpaid", "payment_not_closable", "payment_close_unavailable"]);

export function PaymentClose({ invoiceId, payment, admin, refresh }: { invoiceId: number; payment: PaymentRecord; admin: boolean; refresh: () => void }) {
  const actor = useAdminIdentity();
  const view = useAdminPermission("invoices.view");
  const allowed = useAdminPermission("payments.close");
  if (!admin || !view) return null;
  return <ClosePanel key={`${actor?.id ?? 0}:${view}:${allowed}:${invoiceId}:${payment.id}`} actor={actor?.id ?? 0} allowed={allowed} invoiceId={invoiceId} payment={payment} refresh={refresh} />;
}
function ClosePanel({ actor, allowed, invoiceId, payment, refresh }: { actor: number; allowed: boolean; invoiceId: number; payment: PaymentRecord; refresh: () => void }) {
  const path = `/admin/invoices/${invoiceId}/payments/${payment.id}/close`;
  const storage = `zjmfgo:payment-close:${actor}:${invoiceId}:${payment.id}`;
  const [open, setOpen] = useState(false);
  const [facts, setFacts] = useState<CloseFacts | null>(null);
  const [invoice, setInvoice] = useState<InvoiceFacts | null>(null);
  const [pending, setPending] = useState<Pending | null>(null);
  const [ready, setReady] = useState(false);
  const [storageError, setStorageError] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [confirm, setConfirm] = useState(false);
  const [reviewed, setReviewed] = useState(false);
  const running = useRef(false);
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  useEffect(() => {
    queueMicrotask(() => {
      if (!mounted.current) return;
      try {
        const raw = sessionStorage.getItem(storage);
        if (raw) {
          const saved = JSON.parse(raw) as Pending;
          const body = JSON.parse(saved.body) as { expected_version: number };
          if (saved.path !== path || typeof saved.key !== "string" || saved.key.length < 8 || saved.key.length > 128 || typeof saved.rejected !== "boolean" || !Number.isSafeInteger(body.expected_version) || body.expected_version < 1 || Object.keys(body).length !== 1) throw new Error();
          setPending(saved);
        }
        setReady(true);
      } catch { setStorageError("原关单恢复资料无法读取，已暂停新关单。请联系支持核实，勿清空后重试。"); }
    });
  }, [path, storage]);
  useEffect(() => {
    if (!pending && !busy) return;
    const leave = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    window.addEventListener("beforeunload", leave);
    return () => window.removeEventListener("beforeunload", leave);
  }, [pending, busy]);
  async function identity(write: boolean) {
    const live = await api<User>("/me");
    if (live.id !== actor || !can(live, "invoices.view") || (write && !can(live, "payments.close"))) throw new Error("员工身份或关单权限已变化，请使用原账户核实；原请求保留。");
  }
  async function read() {
    if (running.current) return;
    running.current = true; setBusy(true); setError(""); setReviewed(false); setConfirm(false);
    try {
      await identity(false);
      const next = await api<CloseFacts>(path);
      const nextInvoice = await api<InvoiceFacts>(`/admin/invoices/${invoiceId}`);
      await identity(false);
      if (!mounted.current) return;
      if (next.payment_id !== payment.id || next.invoice_id !== invoiceId || nextInvoice.id !== invoiceId || !Number.isSafeInteger(next.version) || nextInvoice.version !== next.version || !Array.isArray(next.operations) || !Array.isArray(next.anomalies) || !Array.isArray(nextInvoice.payments)) throw new Error("关单事实不完整或读取期间已更新，请重新读取。");
      setFacts(next); setInvoice(nextInvoice); setReviewed(true); refresh();
    } catch (failure) { if (mounted.current) { setError((failure as Error).message); setFacts(null); setInvoice(null); } }
    finally { running.current = false; if (mounted.current) setBusy(false); }
  }
  const blocked = payment.close_anomaly || !!facts?.anomalies.length || invoice?.payment_close_anomaly;
  const currentPayment = invoice?.payments.find(row => row.id === payment.id);
  const eligible = allowed && invoice?.status === "unpaid" && !blocked && facts?.close_supported && currentPayment?.close_enabled === true && (facts.status === "pending" || facts.status === "expired");
  async function send(original?: Pending) {
    if (running.current || !ready || storageError || (!original && (pending || !eligible || !confirm || !facts))) return;
    if (!original && !window.confirm(`确认向渠道请求最终关闭支付单 #${payment.id}（${money(payment.amount_cents)}）？只有可信关闭结果才解除保护；未知结果继续冻结。`)) return;
    const request: Pending = original ? { ...original, rejected: false } : { path, key: `web-close-${crypto.randomUUID()}`, body: JSON.stringify({ expected_version: facts!.version }), rejected: false };
    running.current = true; setBusy(true); setError(""); setNotice("");
    let dispatched = false;
    let responseReceived = false;
    try {
      await identity(true);
      if (!mounted.current) return;
      try { sessionStorage.setItem(storage, JSON.stringify(request)); }
      catch { throw new Error("浏览器无法保存原关单请求，已停止发送。"); }
      setPending(request); setReviewed(false); setConfirm(false); dispatched = true;
      const result = await api<Receipt>(request.path, { method: "POST", body: request.body, headers: { "Idempotency-Key": request.key } });
      responseReceived = true;
      if (!result || result.invoice_id !== invoiceId || result.payment_id !== payment.id || result.status !== "closed" || !Number.isSafeInteger(result.version) || result.version < 1 || typeof result.operation_id !== "string" || !result.operation_id) throw new Error("关单回执不完整，请保留原请求核实。");
      await identity(true);
      if (!mounted.current) return;
      try { sessionStorage.removeItem(storage); }
      catch { throw new Error("回执已确认，但原关单请求无法清理。请继续以原请求核实，勿重新发起。"); }
      setPending(null); setOpen(false); setFacts(null); setInvoice(null); setConfirm(false);
      setNotice(result.replayed ? "已确认原关单回执，未再次调用渠道。请刷新核对账单与异常状态。" : "渠道已确认该支付单最终关闭，原历史记录保留。账单可否重新付款以最新状态为准。");
      refresh();
    } catch (failure) {
      if (!mounted.current) return;
      if (dispatched && !responseReceived && failure instanceof ApiError && rejectionCodes.has(failure.code)) {
        const rejected = { ...request, rejected: true };
        try { sessionStorage.setItem(storage, JSON.stringify(rejected)); setPending(rejected); }
        catch { setStorageError("原请求拒绝状态无法保存，已停止新操作，请保留原键核实。"); }
        setError(`${failure.message}。原载荷与版本未变，请显式读取最新事实后核对被拒绝请求。`);
      } else setError(`${(failure as Error).message}${dispatched ? " 结果尚未确认，继续保留原请求和付款保护；请勿重复关单。" : " 未发送关单请求。"}`);
    } finally { running.current = false; if (mounted.current) setBusy(false); }
  }
  async function endRejected() {
    if (running.current || !pending?.rejected || !reviewed || !facts || !window.confirm("已核对最新事实，确认结束被拒绝的原请求？这不会更改渠道或账单状态。")) return;
    running.current = true; setBusy(true); setError("");
    try {
      await identity(true);
      if (!mounted.current) return;
      sessionStorage.removeItem(storage); setPending(null); setReviewed(false); setConfirm(false); setOpen(false);
    } catch (failure) { if (mounted.current) setError((failure as Error).message); }
    finally { running.current = false; if (mounted.current) setBusy(false); }
  }
  function closeDialog() {
    if (busy) return;
    setOpen(false); setConfirm(false);
  }
  const canOffer = payment.close_enabled && allowed;
  const showHistory = !!payment.close_status || payment.status === "closed" || !!payment.close_anomaly;
  return <div className="mt-2 max-w-80 space-y-2 text-xs whitespace-normal break-words">
    {(payment.status === "pending" || payment.status === "expired") && !payment.close_supported && <p className="text-muted-foreground">此渠道未提供可信关单能力。过期、未付款查询或人工确认均不能解除付款保护。</p>}
    {payment.close_status && <p>{closeStatuses[payment.close_status] ?? "关单状态待核验"}</p>}
    {payment.close_anomaly && <p role="alert" className="text-destructive">已关闭支付出现迟到收款异常，账单继续冻结，需财务核验；请勿重新付款或取消。</p>}
    {canOffer && <Button size="sm" variant="outline" disabled={busy || !!pending || !ready || !!storageError} onClick={() => { setOpen(true); void read(); }}>可信关单</Button>}
    {showHistory && <Button size="sm" variant="outline" disabled={busy} onClick={() => { setOpen(true); void read(); }}>查看关单事实</Button>}
    {!ready && !storageError && canOffer && <p>正在检查本员工原关单请求…</p>}
    {storageError && <p role="alert" className="text-destructive">{storageError}</p>}
    {pending && <div role="status" className="rounded border p-2 space-y-2"><p>原关单请求仍需核实，原键和载荷已保留。刷新不会自动提交，也不会换键重试渠道。</p><div className="flex flex-wrap gap-2"><Button size="sm" disabled={busy || !allowed || !!storageError} onClick={() => void send(pending)}>以原请求核实关单结果</Button><Button size="sm" variant="outline" disabled={busy} onClick={() => { setOpen(true); void read(); }}>读取最新关单事实</Button>{pending.rejected && reviewed && <Button size="sm" variant="outline" disabled={busy || !allowed} onClick={() => void endRejected()}>已核对，结束被拒绝请求</Button>}</div></div>}
    {!open && error && <p role="alert" className="text-destructive">{error}</p>}{notice && <p role="status">{notice}</p>}
    <Dialog open={open} onOpenChange={value => { if (!value) closeDialog(); }}><DialogContent showCloseButton={!busy} className="min-w-0 max-h-[85dvh] overflow-y-auto"><DialogHeader><DialogTitle>支付单 #{payment.id} · 可信关单</DialogTitle><DialogDescription>仅向支持能力的渠道请求最终关闭。此处没有“人工标记已关闭”；本地过期、未付查询和未知结果都不解除保护。</DialogDescription></DialogHeader>
      {busy && <p role="status" className="text-sm">正在读取或核实，请勿重复提交。</p>}
      {facts && <p className="text-xs text-muted-foreground">关单操作共 {facts.operations_total} 条，收款异常共 {facts.anomalies_total} 条；各展示最近 50 条。</p>}
      {facts && <div className="space-y-3 text-sm break-words"><p>原支付：#{facts.payment_id} · {money(payment.amount_cents)} · 当前 {facts.status} · 账单版本 {facts.version}</p>{blocked && <p role="alert" className="text-destructive">存在迟到收款异常，禁止解锁或新付款，需财务核验。</p>}{facts.operations.length === 0 ? <p>暂无关单操作记录。</p> : facts.operations.map(operation => <article key={operation.id} className="rounded border p-2 space-y-1"><p>操作 #{operation.id} · {closeStatuses[operation.status] ?? operation.status}</p><p className="break-all">渠道操作号：{operation.operation_id}</p><p>执行员工 #{operation.actor_id} · 原账单版本 {operation.expected_version}</p><p>{date(operation.started_at)}{operation.completed_at ? ` · 完成 ${date(operation.completed_at)}` : ""}</p>{operation.result_code && <p>结果码：{operation.result_code}</p>}</article>)}{facts.anomalies.map(anomaly => <article key={anomaly.id} className="rounded border border-destructive p-2 space-y-1"><p>迟到收款异常 #{anomaly.id} · 待核验 · {money(anomaly.amount_cents)}</p><p className="break-all">渠道流水：{anomaly.external_transaction_id || "未提供"}</p><p>{date(anomaly.created_at)}</p></article>)}</div>}
      {eligible && !pending && <label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={confirm} disabled={busy} onChange={event => setConfirm(event.target.checked)} />我已核对原支付，理解仅以渠道可信最终关闭结果解锁，未知结果继续冻结。</label>}
      {!allowed && <p className="text-sm text-muted-foreground">当前只读；请求关单需要独立的支付关单权限。</p>}
      {error && <p role="alert" className="text-sm text-destructive break-words">{error}</p>}
      <DialogFooter className="flex-wrap"><Button variant="outline" disabled={busy} onClick={closeDialog}>{pending ? "关闭，保留原请求" : "取消"}</Button><Button variant="outline" disabled={busy} onClick={() => void read()}>读取最新关单事实</Button>{eligible && !pending && <Button variant="destructive" disabled={busy || !confirm || !ready || !!storageError} onClick={() => void send()}>向渠道请求最终关单</Button>}</DialogFooter>
    </DialogContent></Dialog>
  </div>;
}
