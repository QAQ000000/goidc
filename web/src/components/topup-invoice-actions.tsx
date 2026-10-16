"use client";

import { useRef, useState } from "react";
import Link from "next/link";
import { api } from "@/lib/api";
import { Button } from "./ui/button";
import { OnlinePayment, type PaymentRecord } from "./online-payment";

export type TopupState = { status: "created" | "accepted" | "credited" | "pending_liability" | "cancelled"; settings_version: number; accepted_at?: string | null; failure_code?: string };
const messages: Record<TopupState["status"], string> = {
  created: "充值账单待付款，仅支持在线支付。",
  accepted: "充值付款已接纳，资金结果仍须核实。请继续原支付单或核对支付状态，暂不可取消。",
  credited: "充值已入账，可在账户余额查看流水。",
  pending_liability: "已收到款，余额待核对。请联系财务处理，请勿重复付款。",
  cancelled: "充值账单已取消。",
};

export function TopupInvoiceActions({ invoice, admin, refresh }: { invoice: { id: number; version: number; amount_cents: number; import_review_required: boolean; payment_pending: boolean; payments?: PaymentRecord[]; topup?: TopupState }; admin: boolean; refresh: () => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const request = useRef<{ key: string; body: string } | null>(null);
  const lock = useRef(false);
  const status = invoice.topup?.status;
  async function cancel() {
    if (lock.current || status !== "created" || !window.confirm("确认取消此未付款充值账单？")) return;
    lock.current = true;
    setBusy(true);
    setError("");
    request.current ??= { key: `web-topup-cancel-${crypto.randomUUID()}`, body: JSON.stringify({ expected_version: invoice.version, note: "客户取消未付款充值" }) };
    try {
      await api(`/balance/topups/${invoice.id}/cancel`, { method: "POST", body: request.current.body, headers: { "Idempotency-Key": request.current.key } });
      refresh();
    } catch (e) { setError((e as Error).message); refresh(); }
    finally { lock.current = false; setBusy(false); }
  }
  return <div className="space-y-3 max-w-xl">
    <p role="status" className="text-sm">{status ? messages[status] : "正在读取充值状态，请刷新账单确认后再操作。"}</p>
    <div className="flex flex-wrap gap-2">
      {!admin && (status === "created" || status === "accepted") && <OnlinePayment invoice={{ ...invoice, topup: invoice.topup }} refresh={refresh} />}
      {!admin && status === "created" && <Button size="sm" variant="outline" disabled={busy || invoice.payment_pending} onClick={() => void cancel()}>{busy ? "正在取消" : "取消充值账单"}</Button>}
      {!admin && <Button variant="outline" size="sm" asChild><Link href="/console/account">查看账户余额</Link></Button>}
    </div>
    {admin && <p className="text-xs text-muted-foreground">充值账单暂不支持退款，请核对原支付与余额入账事实。</p>}
    {error && <p role="alert" className="text-sm text-destructive break-words">{error}</p>}
  </div>;
}
