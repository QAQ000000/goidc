"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError, date, money, type User } from "@/lib/api";
import { can } from "@/lib/admin-navigation";
import { useAdminIdentity, useAdminPermission } from "./workspace";
import { Button } from "./ui/button";
import { Textarea } from "./ui/textarea";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "./ui/dialog";
import { Loading } from "./common";

type Liability = {
  invoice_id: number; user_id: number; amount_cents: number; version: number; status: string; replayed: boolean;
  settlement: null | { payment_id: number; status: "credited" | "pending_liability"; amount_cents: number; ledger_id: number | null; failure_code: string; created_at: string };
  resolution: null | { id: number; invoice_id: number; payment_id: number; user_id: number; amount_cents: number; ledger_id: number; actor_id: number; note: string; created_at: string };
};
type Pending = { path: string; key: string; body: string };

export function TopupLiabilityPanel({ invoiceId, refreshInvoice }: { invoiceId: number; refreshInvoice: () => void }) {
  const actor = useAdminIdentity();
  const view = useAdminPermission("invoices.view"), resolve = useAdminPermission("balance.topup.resolve");
  return <LiabilityPanel key={`${actor?.id ?? "none"}:${view}:${resolve}:${invoiceId}`} actor={actor?.id ?? 0} invoiceId={invoiceId} view={view} resolve={resolve} refreshInvoice={refreshInvoice} />;
}
function LiabilityPanel({ actor, invoiceId, view, resolve, refreshInvoice }: { actor: number; invoiceId: number; view: boolean; resolve: boolean; refreshInvoice: () => void }) {
  const path = `/admin/invoices/${invoiceId}/topup-liability`;
  const writePath = `${path}/resolve`;
  const storage = `zjmfgo:topup-liability:${actor}:${invoiceId}`;
  const [pending, setPending] = useState<Pending | null>(null);
  const [storageReady, setStorageReady] = useState(false);
  const [storageError, setStorageError] = useState("");
  const [data, setData] = useState<Liability | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState("");
  const [open, setOpen] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [reviewed, setReviewed] = useState(false);
  const [receipt, setReceipt] = useState("");
  const [identityFailed, setIdentityFailed] = useState(false);
  const running = useRef(false);
  const readRequest = useRef<AbortController | null>(null);
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; readRequest.current?.abort(); }; }, []);

  useEffect(() => {
    if (!actor) return;
    queueMicrotask(() => {
      if (!mounted.current) return;
      try {
        const raw = sessionStorage.getItem(storage);
        if (raw) {
          const saved = JSON.parse(raw) as Pending;
          const body = JSON.parse(saved.body) as { expected_version: number; note: string };
          if (saved.path !== writePath || typeof saved.key !== "string" || saved.key.length < 8 || saved.key.length > 128 || !Number.isSafeInteger(body.expected_version) || body.expected_version < 1 || typeof body.note !== "string" || !body.note.trim() || Array.from(body.note).length > 1000 || Object.keys(body).some(key => !["expected_version", "note"].includes(key))) throw new Error();
          setPending(saved);
        }
        setStorageReady(true);
      } catch { setStorageError("原补账恢复资料无法读取，已暂停新补账。请联系支持核实，不要清空恢复资料后重复操作。"); }
    });
  }, [actor, storage, writePath]);
  useEffect(() => {
    if (!pending && !busy) return;
    const leave = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    window.addEventListener("beforeunload", leave);
    return () => window.removeEventListener("beforeunload", leave);
  }, [pending, busy]);

  const load = useCallback(async () => {
    if (!view || !actor || running.current) return;
    readRequest.current?.abort();
    const controller = new AbortController(); readRequest.current = controller;
    setLoading(true); setData(null); setError(""); setConfirm(false);
    try {
      const before = await api<User>("/me", { signal: controller.signal });
      if (before.id !== actor || !can(before, "invoices.view")) throw new Error("员工身份或查看权限已变化，请使用原账户重新打开账单；原恢复资料保留。");
      const next = await api<Liability>(path, { signal: controller.signal });
      const after = await api<User>("/me", { signal: controller.signal });
      if (after.id !== actor || !can(after, "invoices.view")) throw new Error("员工身份或查看权限已变化，请使用原账户重新打开账单；原恢复资料保留。");
      if (!next || next.invoice_id !== invoiceId || !Number.isSafeInteger(next.version)) throw new Error("补账事实响应不完整，请重新读取。");
      if (!controller.signal.aborted) { setData(next); setIdentityFailed(false); setReviewed(true); }
    } catch (failure) {
      if (!controller.signal.aborted) { setError((failure as Error).message); setIdentityFailed(true); }
    } finally { if (!controller.signal.aborted) setLoading(false); }
  }, [actor, invoiceId, path, view]);
  useEffect(() => { const timer = window.setTimeout(() => void load(), 0); return () => { window.clearTimeout(timer); readRequest.current?.abort(); }; }, [load]);

  useEffect(() => {
    const focus = () => { setData(null); void load(); };
    const visibility = () => {
      if (document.visibilityState === "hidden") setData(null);
      else void load();
    };
    window.addEventListener("focus", focus);
    document.addEventListener("visibilitychange", visibility);
    return () => { window.removeEventListener("focus", focus); document.removeEventListener("visibilitychange", visibility); };
  }, [load]);

  function closeDraft() {
    if (busy) return;
    if (note && !pending && !window.confirm("放弃尚未提交的补账说明？")) return;
    setOpen(false); setConfirm(false); setNote("");
  }
  async function send(original?: Pending) {
    if (running.current || !view || !resolve || !storageReady || identityFailed || (conflict && !reviewed)) return;
    const request = original ?? { path: writePath, key: `web-topup-resolve-${crypto.randomUUID()}`, body: JSON.stringify({ expected_version: data?.version, note: note.trim() }) };
    if (!original && (pending || conflict || !confirm || !note.trim() || Array.from(note.trim()).length > 1000 || !data || data.resolution || data.settlement?.status !== "pending_liability")) return;
    running.current = true; setBusy(true); setError(""); setReceipt("");
    let dispatched = false;
    try {
      const live = await api<User>("/me");
      if (live.id !== actor || !can(live, "invoices.view") || !can(live, "balance.topup.resolve")) {
        setIdentityFailed(true); setData(null);
        throw new Error("员工身份或补账权限已变化，未发送补账请求；请使用原账户核实，原恢复资料保留。");
      }
      if (!mounted.current) return;
      try { sessionStorage.setItem(storage, JSON.stringify(request)); }
      catch { if (!original) throw new Error("浏览器无法保存原补账请求，已停止发送。请检查浏览器存储后重试。"); }
      setPending(request);
      dispatched = true;
      const result = await api<Liability>(request.path, { method: "POST", body: request.body, headers: { "Idempotency-Key": request.key } });
      if (!result || result.invoice_id !== invoiceId || !result.resolution || result.resolution.invoice_id !== invoiceId || !Number.isSafeInteger(result.resolution.ledger_id) || result.resolution.ledger_id < 1 || !Number.isSafeInteger(result.resolution.id) || result.resolution.id < 1 || !Number.isSafeInteger(result.resolution.amount_cents) || result.resolution.amount_cents < 1 || result.status !== "credited") throw new Error("补账回执不完整，结果未知，请保留原请求核实。");
      let receiptActor: User;
      try { receiptActor = await api<User>("/me"); }
      catch (failure) { if (mounted.current) { setIdentityFailed(true); setData(null); } throw failure; }
      if (!mounted.current) return;
      if (receiptActor.id !== actor || !can(receiptActor, "invoices.view") || !can(receiptActor, "balance.topup.resolve")) {
        setIdentityFailed(true); setData(null);
        throw new Error("员工身份或权限已变化，补账回执暂不展示；请以原员工身份核实原请求。");
      }
      // A storage cleanup failure leaves the original request available for a
      // safe replay rather than silently creating a fresh operation.
      try { sessionStorage.removeItem(storage); }
      catch { setData(result); setError("补账回执已确认，但浏览器恢复资料尚未清理。请继续以原请求核实，勿发起新补账。"); return; }
      setPending(null); setConflict(false); setData(result); setNote(""); setConfirm(false); setOpen(false);
      setReceipt(result.replayed ? "原补账请求回执已确认，未重复入账。" : "已将原收款补入原客户余额。");
      refreshInvoice();
    } catch (failure) {
      if (!mounted.current) return;
      const e = failure as Error;
      if (!dispatched) { setIdentityFailed(true); setData(null); setError(`${e.message} 原请求未改变。请重新读取最新补账事实。`); }
      else if (e instanceof ApiError && e.status === 409) { setConflict(true); setReviewed(false); setError("补账请求发生冲突，原键和原载荷保留。请显式读取最新事实并核对，系统不会自动更换请求。"); }
      else setError(`补账结果尚未确认或当前无权核实。${e.message} 请保留原请求，恢复身份或权限后核实，勿重复补账。`);
    } finally { running.current = false; if (mounted.current) setBusy(false); }
  }
  function endRejected() {
    if (!conflict || !reviewed || busy || !window.confirm("已核对最新事实，确认结束此被拒绝的原请求？只有再次填写说明并确认后才会产生新请求。")) return;
    try { sessionStorage.removeItem(storage); setPending(null); setConflict(false); setOpen(false); setConfirm(false); setNote(""); setError(""); }
    catch { setError("无法清理原请求，已暂停新补账，请检查浏览器存储。"); }
  }
  const eligible = data?.status === "pending_liability" && data.settlement?.status === "pending_liability" && !data.resolution;
  return <section className="min-w-0 border rounded-md p-4 space-y-4" aria-label="充值待补账处理"><div className="flex flex-wrap items-center justify-between gap-3"><h2 className="font-semibold">充值待补账处理</h2><Button type="button" size="sm" variant="outline" disabled={loading || busy || !view} onClick={() => void load()}>{loading ? "读取中" : "读取最新补账事实"}</Button></div>
    {!view ? <p className="text-sm">缺少账单查看权限，无法读取补账事实。原恢复资料仍保留。</p> : loading ? <Loading /> : data ? <div className="space-y-3 text-sm"><dl className="grid gap-3 sm:grid-cols-2"><div><dt className="text-muted-foreground">原客户</dt><dd>#{data.user_id}</dd></div><div><dt className="text-muted-foreground">原收款金额</dt><dd className="money">{money(data.amount_cents)}</dd></div>{data.settlement && <><div><dt className="text-muted-foreground">原支付单</dt><dd>#{data.settlement.payment_id}</dd></div><div><dt className="text-muted-foreground">原结算时间</dt><dd>{date(data.settlement.created_at)}</dd></div><div className="sm:col-span-2 min-w-0"><dt className="text-muted-foreground">原结算事实</dt><dd className="break-words">{data.settlement.status === "pending_liability" ? "原收款未入余额" : "原收款已入余额"}{data.settlement.failure_code ? ` · ${data.settlement.failure_code}` : ""}</dd></div></>}</dl>{data.resolution ? <div className="border rounded p-3 space-y-2"><p className="font-medium">已补入原客户余额 · {money(data.resolution.amount_cents)}</p><p>解决时间：{date(data.resolution.created_at)} · 余额流水 #{data.resolution.ledger_id} · 经办员工 #{data.resolution.actor_id}</p><p className="whitespace-pre-wrap break-words">处理说明：{data.resolution.note}</p><p>原失败结算保留，不会再次收款。</p></div> : !data.settlement ? <p>尚无收款结算，不可执行补账。</p> : eligible ? <p>已收到款但尚未入余额。补账仅履行原收款责任，不重新收款，不恢复客户状态。</p> : <p>此充值结算无需补账。</p>}</div> : <p className="text-sm text-muted-foreground">补账事实尚未读取成功，不可执行新补账。</p>}
    {!resolve && <p role="status" className="text-sm text-muted-foreground">当前为只读查看；处理需要账单查看及充值待补账权限。</p>}
    {!storageReady && !storageError && <p role="status" className="text-sm">正在检查本员工的原请求恢复资料，完成前不可新补账。</p>}
    {storageError && <p role="alert" className="text-sm text-destructive">{storageError}</p>}
    {pending && <div role="status" className="rounded border p-3 space-y-3 text-sm"><p>存在尚需核实的原补账请求。已解决状态不会删除此恢复入口；刷新不会自动提交。</p><div className="flex flex-wrap gap-2"><Button type="button" size="sm" disabled={busy || loading || !view || !resolve || identityFailed || (conflict && !reviewed)} onClick={() => void send(pending)}>以原请求核实补账结果</Button>{conflict && reviewed && <Button type="button" variant="outline" size="sm" disabled={busy} onClick={endRejected}>已核对，结束被拒绝请求</Button>}</div>{conflict && !reviewed && <p>请先点击“读取最新补账事实”核对当前状态。</p>}</div>}
    {error && <p role="alert" className="text-sm text-destructive break-words">{error}</p>}{receipt && <p role="status" className="text-sm text-emerald-700">{receipt}</p>}
    {eligible && resolve && view && <Button type="button" size="sm" disabled={!storageReady || !!pending || busy || loading || identityFailed || conflict} onClick={() => { setOpen(true); setConfirm(false); }}>处理待补账</Button>}
    <Dialog open={open} onOpenChange={value => { if (!value) closeDraft(); }}><DialogContent showCloseButton={!busy} className="max-h-[85dvh] overflow-y-auto"><DialogTitle>确认补入原客户余额</DialogTitle><DialogDescription>仅把已收到的 {data ? money(data.amount_cents) : "原收款"} 补入原客户 #{data?.user_id}，不再次收款，不支持部分补账。请核实原支付和结算事实。</DialogDescription><form className="space-y-4" onSubmit={event => { event.preventDefault(); void send(); }}><label className="grid gap-2 text-sm">补账处理说明（必填，最多1000字）<Textarea required maxLength={1000} value={note} disabled={busy || !!pending} onChange={event => { setNote(event.target.value); setConfirm(false); }} /></label><label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={confirm} disabled={busy || !!pending} onChange={event => setConfirm(event.target.checked)} />我已核对原收款及待补账原因，确认补入原客户余额。</label><div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={busy} onClick={closeDraft}>{pending ? "关闭，保留原请求" : "取消未提交草稿"}</Button><Button disabled={busy || !!pending || !confirm || !note.trim() || identityFailed || conflict}>{busy ? "提交中" : "确认补账"}</Button></div>{pending && <p role="status" className="text-sm">原请求已保存。可关闭窗口，在账单面板使用原请求核实结果。</p>}{error && <p role="alert" className="text-sm text-destructive break-words">{error}</p>}</form></DialogContent></Dialog>
  </section>;
}
