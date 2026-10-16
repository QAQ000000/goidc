"use client";
import { useCallback, useState } from "react";
import { useAdminIdentity, useAdminPermission } from "./workspace";
import type { PaymentRecord } from "./online-payment";
import { PaymentCloseVerifications } from "./payment-close-verifications";
import { PaymentAnomalyGroups } from "./payment-anomaly-groups";
import { PaymentAnomalyFinances } from "./payment-anomaly-finances";

export function PaymentExceptionPanel({ invoiceId, payments, refresh }: { invoiceId: number; payments: PaymentRecord[]; refresh: () => void }) {
  const actor = useAdminIdentity();
  const view = useAdminPermission("invoices.view");
  const anomalyView = useAdminPermission("payment_anomalies.view");
  const [revision, setRevision] = useState(0);
  const refreshAll = useCallback(() => { setRevision(value => value + 1); refresh(); }, [refresh]);
  if (!actor || !view) return null;
  return <section aria-labelledby="payment-exception-title" className="rounded-xl border bg-card p-4 sm:p-6 space-y-6 min-w-0"><div className="space-y-2"><h2 id="payment-exception-title" className="font-semibold">支付异常核验</h2><p className="text-sm text-muted-foreground">所有处理都绑定本账单原支付及证据。原关单、收款通知和退款历史永久保留；一个案件解决不代表其他未知或异常责任同时解除。</p></div><PaymentCloseVerifications invoiceId={invoiceId} payments={payments} refresh={refreshAll} revision={revision} />{anomalyView ? <><PaymentAnomalyGroups invoiceId={invoiceId} refresh={refreshAll} revision={revision} /><PaymentAnomalyFinances key={`${actor.id}:${invoiceId}:${revision}`} invoiceId={invoiceId} /></> : <p className="text-sm text-muted-foreground">迟到收款责任组、退回证据和异常资金流水需要独立的异常查看权限。</p>}</section>;
}
