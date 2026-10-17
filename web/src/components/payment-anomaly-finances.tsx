"use client";
import { date, money } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { ExceptionPager, useExceptionQuery } from "./payment-exception-common";
import type { AnomalyEntry, AnomalySummary, ExceptionPage } from "./payment-exception-types";

export function PaymentAnomalyFinances({ invoiceId }: { invoiceId: number }) {
  const { offset: getOffset, update } = useExceptionQuery();
  const offset = getOffset("anomaly_entries_offset");
  const base = `/admin/invoices/${invoiceId}`;
  const summary = useResource<AnomalySummary>(`${base}/payment-anomaly-summary`, false);
  const entries = useResource<ExceptionPage<AnomalyEntry>>(`${base}/payment-anomaly-entries?limit=10&offset=${offset}`, false);
  return <section aria-labelledby="anomaly-finances-title" className="space-y-3 min-w-0"><h3 id="anomaly-finances-title" className="font-semibold">异常责任资金汇总与收退流水</h3><p className="text-sm text-muted-foreground">以下为普通销售之外的责任资金，按可信原交易去重。重复通知数不代表收款笔数；空交易号等未核验责任单独列示，未计入可核验收款。退回金额始终以其他员工确认的事实为准，不改写普通销售收入、退款及客户余额。</p><Button size="sm" variant="outline" onClick={() => { summary.refresh(); entries.refresh(); }}>读取异常资金事实</Button>
    {summary.error ? <Failure error={summary.error} retry={summary.refresh} /> : !summary.data ? <Loading /> : <div role="region" aria-label="异常责任资金汇总" className="grid min-w-0 gap-3 sm:grid-cols-2 lg:grid-cols-3">{[["可核验异常收款", summary.data.anomaly_received_cents], ["已确认外部退回", summary.data.anomaly_refunded_cents], ["可核验待退责任", summary.data.anomaly_pending_cents], ["单列未核验责任", summary.data.unverified_amount_cents]].map(([label, value]) => <div key={label} className="rounded border p-3 min-w-0"><p className="text-sm text-muted-foreground">{label}</p><p className="money break-words">{money(Number(value))} <span className="text-xs">{summary.data!.currency}</span></p></div>)}<div className="rounded border p-3 text-sm">未解决组 {summary.data.unresolved_groups} · 原身份未核验组 {summary.data.unverified_groups}</div></div>}
    {entries.error ? <Failure error={entries.error} retry={entries.refresh} /> : !entries.data ? <Loading /> : <>{!entries.data.items.length && <p className="text-sm text-muted-foreground">暂无异常责任资金流水。</p>}{entries.data.items.map(entry => <article key={entry.event_key} className="rounded border p-3 space-y-2 text-sm min-w-0 break-words"><div className="flex flex-wrap justify-between gap-2"><strong>{entry.direction === "income" ? "异常责任收款" : entry.direction === "expense" ? "异常责任资金退回" : "原身份未核验责任"}</strong><span className="money">{entry.direction === "expense" ? "−" : entry.direction === "income" ? "+" : ""}{money(entry.amount_cents)}</span></div><p>责任组 #{entry.anchor_anomaly_id} · 原支付 #{entry.payment_id}{entry.refund_id ? ` · 退回登记 #${entry.refund_id}` : ""}</p><p className="break-all">{entry.direction === "expense" ? "退回流水" : "原渠道流水"}：{entry.transaction_no || "缺失，未核验"}</p><p>{date(entry.occurred_at)} · {entry.verified ? "已有匹配事实" : "不能据此解除保护"}</p><Button size="sm" variant="outline" onClick={() => update({ exception_group: entry.anchor_anomaly_id })}>选中关联责任组</Button></article>)}<ExceptionPager total={entries.data.total} offset={offset} count={entries.data.items.length} change={value => update({ anomaly_entries_offset: value })} /></>}
  </section>;
}
