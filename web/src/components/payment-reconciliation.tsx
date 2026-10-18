"use client";

import { useRef, useState } from "react";
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
  const inFlight = useRef(false);
  const pending = payment.status === "pending" || payment.status === "expired";
  const queryEnabled = payment.query_enabled === true;
  const permitted = !admin || allowed;
  const checking = busy || payment.check_status === "checking";
  const blocked = payment.close_status === "checking" || payment.close_anomaly === true;
  if (payment.provider === "mock") return null;
  async function query() {
    if (inFlight.current || !pending || !queryEnabled || !permitted || checking || blocked) return;
    inFlight.current = true;
    setBusy(true);
    setError("");
    setMessage("");
    try {
      const result = await api<{ status: string }>(
        `${admin ? "/admin" : ""}/invoices/${invoiceId}/payments/${payment.id}/reconcile`,
        { method: "POST" },
      );
      setMessage(
        result.status === "succeeded"
          ? "付款已确认，账单已更新。"
          : result.status === "pending" || result.status === "expired"
            ? "渠道尚未确认付款，原支付单继续保留。"
            : "渠道未返回可确认的付款结果，请刷新账单后核验；原支付单继续保留。",
      );
    } catch (e) {
      setError(e instanceof Error ? e.message : "支付查询失败，请稍后重试；原支付单继续保留。");
    } finally {
      inFlight.current = false;
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
      {pending && permitted && queryEnabled && (
        <Button size="sm" variant="outline" disabled={checking || blocked} onClick={query}>
          <RefreshCw className={checking ? "animate-spin" : ""} />
          {checking ? "查询中" : "查询付款结果"}
        </Button>
      )}
      {pending && !permitted && <p className="text-muted-foreground whitespace-normal">缺少支付查询权限，请联系有权限的管理员核验。</p>}
      {pending && blocked && (
        <p className="text-muted-foreground whitespace-normal">
          {payment.close_anomaly ? "存在关单异常，支付查询已冻结，请联系管理员核验。" : "支付单正在关单核验，暂不能查询付款结果。"}
        </p>
      )}
      {payment.checked_at && (
        <p className="text-muted-foreground whitespace-normal">
          {payment.check_source === "worker" ? "自动查询" : "手动查询"} · {date(payment.checked_at)} · {statusLabels[payment.check_status ?? ""] ?? "已查询"}
        </p>
      )}
      {pending && (
        <p className="text-muted-foreground whitespace-normal">
          支付过期或查到未付款不代表商户已关单；只有支持可信关单的渠道确认最终关闭后才能解除保护。未知结果继续冻结，请联系管理员核验。
        </p>
      )}
      {pending && (
        <p className="text-muted-foreground whitespace-normal">
          {!queryEnabled
            ? payment.query_enabled === false
              ? "当前支付渠道未启用查询或原支付配置不可用，请联系管理员核验。"
              : "尚未取得支付查询能力，请刷新账单后重试。"
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
