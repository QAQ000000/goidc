"use client";

import { useRef, useState } from "react";
import Link from "next/link";
import { api, ApiError, date, money } from "@/lib/api";
import { Button } from "./ui/button";
import { TopupLiabilityPanel } from "./topup-liability-panel";
import { OnlinePayment, type PaymentRecord } from "./online-payment";

export type TopupState = { status: "created" | "accepted" | "credited" | "pending_liability" | "cancelled"; settings_version: number; accepted_at?: string | null; failure_code?: string; ledger_id?: number | null; resolution?: { amount_cents: number; ledger_id: number; created_at: string } | null };
const messages: Record<TopupState["status"], string> = {
  created: "充值账单待付款，仅支持在线支付。",
  accepted: "充值付款已接纳，资金结果仍须核实。请继续原支付单或核对支付状态，暂不可取消。",
  credited: "充值已入账，可在账户余额查看流水。",
  pending_liability: "已收到款，余额待核对。请联系财务处理，请勿重复付款。",
  cancelled: "充值账单已取消。",
};

export function TopupInvoiceActions({ invoice, admin, refresh }: { invoice: { id: number; status?: string; version: number; amount_cents: number; import_review_required: boolean; payment_pending: boolean; topup_can_retry?: boolean; payment_close_anomaly?: boolean; payments?: PaymentRecord[]; topup?: TopupState }; admin: boolean; refresh: () => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [unknown, setUnknown] = useState(false);
  const request = useRef<{ key: string; body: string } | null>(null);
  const lock = useRef(false);
  const status = invoice.topup?.status;
  const closedRetry = status === "accepted" && invoice.topup_can_retry === true && !!invoice.payments?.length && invoice.payments.every(payment => payment.status === "closed");
  const cancelProtected = invoice.payment_pending || invoice.payment_close_anomaly || invoice.payments?.some(payment => payment.status !== "closed" || payment.close_frozen);
  const canCancel = invoice.status === "unpaid" && (status === "created" || closedRetry) && !cancelProtected;
  async function cancel() {
    if (lock.current || (!unknown && !canCancel) || !window.confirm(unknown ? "确认以原请求核实取消结果？不会更换原版本或幂等键。" : closedRetry ? "原支付均已可信关闭。确认取消此充值账单并释放原接纳预留额度？" : "确认取消此未付款充值账单？")) return;
    lock.current = true;
    setBusy(true);
    setError("");
    request.current ??= { key: `web-topup-cancel-${crypto.randomUUID()}`, body: JSON.stringify({ expected_version: invoice.version, note: "客户取消未付款充值" }) };
    try {
      await api(`/balance/topups/${invoice.id}/cancel`, { method: "POST", body: request.current.body, headers: { "Idempotency-Key": request.current.key } });
      request.current = null;
      setUnknown(false);
      refresh();
    } catch (e) {
      const uncertain = !(e instanceof ApiError) || e.status >= 500;
      setUnknown(uncertain);
      if (!uncertain) request.current = null;
      setError(uncertain ? "取消结果暂未确认，请以原请求确认结果。" : (e as Error).message);
      refresh();
    }
    finally { lock.current = false; setBusy(false); }
  }
  return <div className="space-y-3 max-w-xl">
    <p role="status" className="text-sm">{invoice.status === "refunded" ? "已退款，原入账及余额冲正保留历史。" : invoice.payment_close_anomaly ? "已关闭支付出现迟到收款异常，请联系财务核验，勿重复付款或取消。" : invoice.topup_can_retry ? "原支付已可信关闭，原充值接纳仍保留，可重新在线支付，或取消充值释放预留额度。" : invoice.topup?.resolution ? "原充值收款已补入账户余额，请查看余额流水。" : status ? messages[status] : "正在读取充值状态，请刷新账单确认后再操作。"}</p>
    {!admin && invoice.topup?.resolution && <p className="text-sm">入账金额 {money(invoice.topup.resolution.amount_cents)} · {date(invoice.topup.resolution.created_at)} · 余额流水 #{invoice.topup.resolution.ledger_id}</p>}
    {admin && <TopupLiabilityPanel invoiceId={invoice.id} refreshInvoice={refresh} />}
    <div className="flex flex-wrap gap-2">
      {!admin && <OnlinePayment invoice={{ ...invoice, topup: invoice.topup }} refresh={refresh} />}
      {!admin && (status === "created" || closedRetry || unknown) && <Button size="sm" variant="outline" disabled={busy || (!unknown && !canCancel)} onClick={() => void cancel()}>{busy ? "正在取消" : unknown ? "以原请求确认取消结果" : closedRetry ? "取消充值并释放预留" : "取消充值账单"}</Button>}
      {!admin && <Button variant="outline" size="sm" asChild><Link href="/console/account">查看账户余额</Link></Button>}
    </div>
    {admin && <p className="text-xs text-muted-foreground">充值整单外部退款请使用下方“外部退款登记与复核”，核对原支付、原始入账及余额冻结事实；此处不执行商户退款。</p>}
    {error && <p role="alert" className="text-sm text-destructive break-words">{error}</p>}
  </div>;
}
