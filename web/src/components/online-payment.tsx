"use client";

import { useEffect, useRef, useState } from "react";
import { CreditCard, ExternalLink } from "lucide-react";
import { api, ApiError, money, type User } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Button } from "./ui/button";
import { useAdminIdentity } from "./workspace";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "./ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "./ui/select";

type Provider = { name: string; label: string; instance_id: string; simulation: boolean };
export type PaymentRecord = {
  id: number;
  provider: string;
  provider_payment_id: string;
  external_transaction_id?: string;
  checkout_url?: string;
  status: string;
  expired: boolean;
  amount_cents: number;
  created_at: string;
  completed_at: string | null;
  check_status?: string | null;
  check_code?: string | null;
  checked_at?: string | null;
  check_source?: "manual" | "worker" | null;
  query_enabled?: boolean;
  next_reconcile_at?: string | null;
  auto_reconcile_attempts?: number;
  reconciliation_exhausted?: boolean;
  close_supported?: boolean;
  close_enabled?: boolean;
  close_status?: string | null;
  close_code?: string | null;
  close_requested_at?: string | null;
  close_anomaly?: boolean;
  close_frozen?: boolean;
};
export const paymentLabels: Record<string, string> = {
  mock: "模拟支付",
  epay_alipay: "支付宝",
  epay_wxpay: "微信支付",
  epay_qqpay: "QQ 钱包",
  epay_bank: "网银支付",
};

function checkoutLink(value: string) {
  try {
    const url = new URL(value);
    return !url.username &&
      !url.password &&
      (url.protocol === "https:" ||
        (url.protocol === "http:" &&
          ["localhost", "127.0.0.1", "[::1]"].includes(url.hostname)))
      ? value
      : "";
  } catch {
    return "";
  }
}

type PayableInvoice = {
  id: number; version: number; status?: string; amount_cents: number; import_review_required: boolean; payment_pending: boolean;
  payments?: PaymentRecord[]; topup?: { status: string }; topup_can_retry?: boolean; payment_close_anomaly?: boolean;
};
type IntentRequest = { path: string; key: string; body: string; rejected: boolean };
export function OnlinePayment(props: { invoice: PayableInvoice; refresh: () => void }) {
  const actor = useAdminIdentity();
  return <OnlinePaymentPanel key={`${actor?.id ?? 0}:${props.invoice.id}`} {...props} actor={actor?.role === "customer" ? actor.id : 0} />;
}
function OnlinePaymentPanel({ invoice, refresh, actor }: { invoice: PayableInvoice; refresh: () => void; actor: number }) {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [selectedOption, setSelectedOption] = useState("");
  const path = `/invoices/${invoice.id}/payments`;
  const storage = `zjmfgo:payment-intent:${actor}:${invoice.id}`;
  const [request, setRequest] = useState<IntentRequest | null>(null);
  const [ready, setReady] = useState(false);
  const [storageError, setStorageError] = useState("");
  const [reviewed, setReviewed] = useState(false);
  const [latestFacts, setLatestFacts] = useState("");
  const [notice, setNotice] = useState("");
  const running = useRef(false);
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  useEffect(() => {
    if (!actor) return;
    queueMicrotask(() => {
      if (!mounted.current) return;
      try {
        const raw = sessionStorage.getItem(storage);
        if (raw) {
          const saved = JSON.parse(raw) as IntentRequest;
          const body = JSON.parse(saved.body) as { provider: string; channel_instance_id: string; expected_version: number };
          if (saved.path !== path || typeof saved.key !== "string" || saved.key.length < 8 || saved.key.length > 128 || typeof saved.rejected !== "boolean" || typeof body.provider !== "string" || !body.provider || typeof body.channel_instance_id !== "string" || !Number.isSafeInteger(body.expected_version) || body.expected_version < 1 || Object.keys(body).some(key => !["provider", "channel_instance_id", "expected_version"].includes(key))) throw new Error();
          setRequest(saved);
        }
        setReady(true);
      } catch { setStorageError("原支付请求恢复资料无法读取，已暂停新付款。请联系支持核实，不要删除恢复资料后重复付款。"); }
    });
  }, [actor, path, storage]);
  useEffect(() => {
    if (!request && !busy) return;
    const leave = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    window.addEventListener("beforeunload", leave);
    return () => window.removeEventListener("beforeunload", leave);
  }, [request, busy]);
  const providers = useResource<Provider[]>(
    open ? "/payment-providers" : null,
    false,
  );
  const options = (providers.data ?? []).filter(value => !invoice.topup || !value.simulation);
  const chosen =
    options.find((value) => `${value.name}:${value.instance_id}` === selectedOption) ||
    options.find((value) => !value.simulation) ||
    options[0];
  const provider = chosen?.name || "";
  const instanceID = chosen?.instance_id || "";
  const [checkout, setCheckout] = useState<{ url: string; paymentId: number } | null>(null);
  const pending = invoice.payments?.find(
    (value) =>
      (value.status === "pending" || (invoice.topup?.status === "accepted" && value.status === "expired")) &&
      !value.close_frozen &&
      value.provider !== "mock" &&
      value.checkout_url,
  );
  const checkoutPayment = invoice.payments?.find(value => value.id === checkout?.paymentId);
  const showPaymentAction = !invoice.status || invoice.status === "unpaid";
  const link = !showPaymentAction || invoice.payment_close_anomaly ? "" : checkoutLink((checkout && !checkoutPayment?.close_frozen && (!checkoutPayment || ["pending", "expired"].includes(checkoutPayment.status)) ? checkout.url : "") || pending?.checkout_url || "");
  const topupRetry = invoice.topup?.status === "accepted" && invoice.topup_can_retry === true;
  const eligible = !invoice.import_review_required && !invoice.payment_pending && !invoice.payment_close_anomaly && (!invoice.status || invoice.status === "unpaid") && (!invoice.topup || invoice.topup.status === "created" || topupRetry);
  async function identity() {
    const live = await api<User>("/me");
    if (live.id !== actor || live.role !== "customer") throw new Error("付款账户已变化，请使用原客户账户核实；原请求保留。");
  }
  async function readLatest() {
    if (running.current) return;
    running.current = true; setBusy(true); setError(""); setReviewed(false);
    try {
      await identity();
      const current = await api<PayableInvoice>(`/invoices/${invoice.id}`);
      await identity();
      if (!mounted.current) return;
      if (current.id !== invoice.id || !Number.isSafeInteger(current.version)) throw new Error("账单事实不完整，请重新读取。");
      setLatestFacts(`最新账单状态 ${current.status} · 版本 ${current.version}。原支付请求载荷和版本未改变。`); setReviewed(true); refresh();
    } catch (failure) { if (mounted.current) setError((failure as Error).message); }
    finally { running.current = false; if (mounted.current) setBusy(false); }
  }
  async function create(original?: IntentRequest) {
    if (running.current || !actor || !ready || storageError || (!original && (request || !eligible || !provider || !instanceID))) return;
    const next: IntentRequest = original ? { ...original, rejected: false } : { path, key: `web-payment-${invoice.id}-${crypto.randomUUID()}`, body: JSON.stringify({ provider, channel_instance_id: instanceID, expected_version: invoice.version }), rejected: false };
    running.current = true; setBusy(true); setError(""); setNotice("");
    let dispatched = false;
    let responseReceived = false;
    try {
      await identity();
      if (!mounted.current) return;
      try { sessionStorage.setItem(storage, JSON.stringify(next)); }
      catch { throw new Error("浏览器无法保存原支付请求，已停止发送，请检查浏览器存储。"); }
      setRequest(next); setReviewed(false); setLatestFacts(""); dispatched = true;
      const result = await api<{ payment_id: number; invoice_id: number; provider: string; amount_cents: number; checkout_url: string; status: string }>(next.path, { method: "POST", body: next.body, headers: { "Idempotency-Key": next.key } });
      responseReceived = true;
      const requestedProvider = (JSON.parse(next.body) as { provider: string }).provider;
      const validCheckout = result?.provider === "mock" ? /^mock:\/\/pay\/mock_[a-f0-9]+$/.test(result.checkout_url) : !!checkoutLink(result?.checkout_url);
      if (!result || result.invoice_id !== invoice.id || result.provider !== requestedProvider || !Number.isSafeInteger(result.payment_id) || result.payment_id < 1 || result.amount_cents !== invoice.amount_cents || !["pending", "expired", "closed", "succeeded", "failed"].includes(result.status) || (["pending", "expired"].includes(result.status) && !validCheckout)) throw new Error("支付回执不完整，请保留原请求核实结果。");
      await identity();
      if (!mounted.current) return;
      try { sessionStorage.removeItem(storage); }
      catch { throw new Error("支付回执已确认，但恢复资料未能清理。请继续以原请求核实，不要重新创建支付。"); }
      setRequest(null); setReviewed(false); setLatestFacts("");
      setCheckout(["pending", "expired"].includes(result.status) ? { url: result.checkout_url, paymentId: result.payment_id } : null);
      setNotice(result.status === "closed" ? "原支付已关闭，原请求回执已核实。请刷新账单，符合条件后可重新选择支付。" : result.status === "succeeded" ? "原支付已成功，请核对最新账单。" : result.status === "failed" ? "原支付已失败，原请求回执已核实，请刷新账单确认后续操作。" : result.provider === "mock" ? "模拟支付单已确认，等待测试回调，勿重复创建。" : "支付单已确认，请继续此支付单，勿重复创建。");
      setOpen(false); refresh();
    } catch (failure) {
      if (!mounted.current) return;
      if (dispatched && !responseReceived && failure instanceof ApiError && [400, 404, 409, 422].includes(failure.status)) {
        const rejected = { ...next, rejected: true };
        try { sessionStorage.setItem(storage, JSON.stringify(rejected)); setRequest(rejected); }
        catch { setStorageError("原支付拒绝状态无法保存，请保留原请求并联系支持核实。"); }
        setError(`${failure.message}。原请求已保留，请显式读取最新账单事实，不会自动换版本或渠道重试。`);
      } else setError(`${(failure as Error).message}${dispatched ? " 结果尚未确认，请以原请求核实，勿重复付款。" : " 未发送支付请求。"}`);
    } finally { running.current = false; if (mounted.current) setBusy(false); }
  }
  async function endRejected() {
    if (running.current || !request?.rejected || !reviewed || !window.confirm("已核对最新账单，确认结束被拒绝的原支付请求？结束后仍须重新选择并确认，才会创建新的支付单。")) return;
    running.current = true; setBusy(true); setError("");
    try { await identity(); if (!mounted.current) return; sessionStorage.removeItem(storage); setRequest(null); setReviewed(false); setLatestFacts(""); setOpen(false); }
    catch (failure) { if (mounted.current) setError((failure as Error).message); }
    finally { running.current = false; if (mounted.current) setBusy(false); }
  }
  return (
    <>
      {showPaymentAction && <Button
        size="sm"
        variant="outline"
        disabled={
          busy || !actor || !ready || !!storageError || !!request || !eligible
        }
        onClick={() => setOpen(true)}
      >
        <CreditCard />
        在线支付
      </Button>}
      {topupRetry && !invoice.payment_close_anomaly && <p className="text-xs text-muted-foreground whitespace-normal">原支付已可信关闭，可重新选择在线支付；沿用原充值接纳快照与额度，不重复预留。</p>}
      {invoice.payment_close_anomaly && <p role="alert" className="text-xs text-destructive whitespace-normal">存在已关闭支付的迟到收款异常，付款已冻结，请联系客服核验，勿重复付款。</p>}
      {!invoice.payment_close_anomaly && invoice.payments?.some(value => value.close_frozen && ["pending", "expired"].includes(value.status)) && <p role="status" className="text-xs text-muted-foreground whitespace-normal">原支付正在关单核验或关单结果未知，付款入口已冻结，请联系管理员核验。</p>}
      {storageError && <p role="alert" className="text-xs text-destructive whitespace-normal">{storageError}</p>}
      {request && <div role="status" className="w-full rounded border p-3 space-y-2 text-sm"><p>原支付请求仍需核实，原渠道、版本和幂等键均已保留；刷新不会自动提交。</p><div className="flex flex-wrap gap-2"><Button size="sm" disabled={busy || !!storageError} onClick={() => void create(request)}>以原请求核实支付单</Button><Button size="sm" variant="outline" disabled={busy} onClick={() => void readLatest()}>读取最新账单事实</Button>{request.rejected && reviewed && <Button size="sm" variant="outline" disabled={busy} onClick={() => void endRejected()}>已核对，结束被拒绝请求</Button>}</div>{latestFacts && <p>{latestFacts}</p>}</div>}
      {!open && error && <p role="alert" className="w-full text-sm text-destructive whitespace-normal break-words">{error}</p>}
      {notice && <p role="status" className="w-full text-sm whitespace-normal">{notice}</p>}
      {link && (
        <Button variant="outline" size="sm" asChild>
          <a href={link} target="_blank" rel="noopener noreferrer">
            <ExternalLink />
            {invoice.topup?.status === "accepted" ? "查看原支付单" : "前往付款"}
          </a>
        </Button>
      )}
      {invoice.topup?.status === "accepted" && (pending?.expired || pending?.status === "expired") && <p className="text-xs text-muted-foreground whitespace-normal">本地已过期，收款结果仍待核实，请勿重复创建付款。</p>}
      <Dialog
        open={open}
        onOpenChange={(value) => {
          if (!busy) setOpen(value);
        }}
      >
        <DialogContent className="min-w-0 w-[calc(100vw-2rem)] max-w-lg grid-cols-[minmax(0,1fr)] overflow-x-hidden max-h-[calc(100dvh-2rem)] overflow-y-auto p-4 sm:p-6">
          <DialogHeader>
            <DialogTitle>在线支付</DialogTitle>
          </DialogHeader>
          <p className="money text-xl font-semibold">
            {money(invoice.amount_cents)}
          </p>
          <label className="grid min-w-0 gap-2 text-sm">
            支付渠道
            <Select
              value={chosen ? `${chosen.name}:${chosen.instance_id}` : ""}
              disabled={busy || !!request}
              onValueChange={setSelectedOption}
            >
              <SelectTrigger aria-label="支付渠道" className="w-full min-w-0 max-w-full [&>span]:min-w-0 [&>span]:truncate">
                <SelectValue placeholder="选择支付渠道" />
              </SelectTrigger>
              <SelectContent className="max-w-[calc(100vw-3rem)]">
                {options.map((value) => (
                  <SelectItem key={`${value.name}:${value.instance_id}`} value={`${value.name}:${value.instance_id}`} className="min-w-0 [&_span]:truncate">
                    {value.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </label>
          {!providers.error && providers.data && !options.length && <p className="text-sm text-muted-foreground">暂无可用在线支付渠道，请稍后重试。</p>}
          {(error || providers.error) && (
            <p role="alert" className="text-sm text-destructive break-words">
              {error || providers.error?.message}
            </p>
          )}
          <DialogFooter className="min-w-0 [&>button]:max-w-full">
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => setOpen(false)}
            >
              返回
            </Button>
            <Button
              disabled={busy || !!request || !ready || !!storageError || !eligible || !!providers.error || !provider || !options.length}
              onClick={() => void create()}
            >
              <CreditCard />
              {busy ? "处理中" : "创建支付单"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
