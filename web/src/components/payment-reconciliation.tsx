"use client";

import { useState } from "react";
import { RefreshCw } from "lucide-react";
import { api, date } from "@/lib/api";
import { Button } from "./ui/button";
import { useAdminPermission } from "./workspace";
import type { PaymentRecord } from "./online-payment";

export function PaymentReconciliation({
  invoiceId,
  payment,
  admin,
  refresh,
}: {
  invoiceId: number;
  payment: PaymentRecord;
  admin: boolean;
  refresh: () => void;
}) {
  const allowed = useAdminPermission("payments.reconcile");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  if (!payment.provider.startsWith("epay_")) return null;
  async function query() {
    setBusy(true);
    setError("");
    setMessage("");
    try {
      const result = await api<{ status: string }>(
        `${admin ? "/admin" : ""}/invoices/${invoiceId}/payments/${payment.id}/reconcile`,
        { method: "POST" },
      );
      setMessage(
        result.status === "pending" || result.status === "expired"
          ? "渠道尚未确认付款，原支付单继续保留。"
          : "付款已确认，账单已更新。",
      );
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
      refresh();
    }
  }
  const statusLabels: Record<string, string> = {
    checking: "查询处理中",
    pending: "渠道尚未确认付款",
    expired: "本地支付已过期，渠道尚未确认付款",
    succeeded: "付款已确认",
    error: "上次查询未确认结果，可稍后重试",
  };
  return (
    <div className="mt-2 max-w-64 space-y-2 text-xs">
      {(payment.status === "pending" || payment.status === "expired") && (!admin || allowed) && payment.query_enabled !== false && (
        <Button size="sm" variant="outline" disabled={busy || payment.close_status === "checking" || payment.close_anomaly} onClick={() => void query()}>
          <RefreshCw className={busy ? "animate-spin" : ""} />
          {busy ? "查询中" : "查询付款结果"}
        </Button>
      )}
      {payment.checked_at && (
        <p className="text-muted-foreground whitespace-normal">
          {payment.check_source === "worker" ? "自动查询" : "手动查询"} · {date(payment.checked_at)} · {statusLabels[payment.check_status ?? ""] ?? "已查询"}
        </p>
      )}
      {(payment.status === "pending" || payment.status === "expired") && (
        <p className="text-muted-foreground whitespace-normal">
          支付过期或查到未付款不代表商户已关单；只有支持可信关单的渠道确认最终关闭后才能解除保护。未知结果继续冻结，请联系管理员核验。
        </p>
      )}
      {(payment.status === "pending" || payment.status === "expired") && (
        <p className="text-muted-foreground whitespace-normal">
          {payment.query_enabled === false
            ? "渠道查询未启用"
            : payment.reconciliation_exhausted
              ? "自动查询次数已用完，待人工核对"
              : payment.next_reconcile_at
                ? `下次自动查询：${date(payment.next_reconcile_at)}`
                : "待人工核对"}
        </p>
      )}
      {message && <p role="status" className="whitespace-normal">{message}</p>}
      {error && <p role="alert" className="text-destructive whitespace-normal break-words">{error}</p>}
    </div>
  );
}
