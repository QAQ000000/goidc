"use client";

import Link from "next/link";
import { date, money } from "@/lib/api";
import { paymentLabels } from "./online-payment";
import { ResourceTable, type ResourceColumn } from "./resource-table";
import { Button } from "./ui/button";

export type TransactionEntry = {
  event_key: string; source: string; id: number; user_id: number;
  invoice_id: number | null; invoice_no: string | null; occurred_at: string;
  customer_name: string; customer_username: string; sales_owner_name: string | null;
  channel: string; direction: string; amount_cents: number; transaction_no: string;
  description: string; kind: string; balance_after: number | null;
};

export const transactionKinds = { topup: "充值", product: "产品", renew: "续费", other: "其他" };
export const transactionChannels: Record<string, string> = { gateway_topup: "在线充值入账", balance: "账户余额", admin_topup: "后台充值", credit: "历史信用结算", credit_repayment: "历史信用还款", manual_offline: "人工线下", unrecorded: "未记录渠道", ...paymentLabels };
export const transactionDirections: Record<string, string> = { income: "收入", expense: "支出", neutral: "账单结算" };
const sources: Record<string, string> = { payment: "成功支付", manual_receipt: "人工线下收款", external_refund: "外部退款到账", ledger: "余额记录", invoice: "已付账单", credit_consumption: "历史信用消费", credit_repayment: "历史信用还款" };

// Only presentation: all filtering, totals and authorisation come from the caller.
export function AdminTransactionResults({ items, customerLinks, returnHref }: { items: TransactionEntry[]; customerLinks: boolean; returnHref: string }) {
  const customer = (row: TransactionEntry) => <div className="space-y-1 break-words">{customerLinks ? <Link className="font-medium text-primary hover:underline" href={`/admin/customers/${row.user_id}?return_to=${encodeURIComponent(returnHref)}`}>{row.customer_name}</Link> : <p className="font-medium">{row.customer_name}</p>}<p className="text-xs text-muted-foreground">#{row.user_id} · {row.customer_username}</p></div>;
  const description = (row: TransactionEntry) => <div className="space-y-1 break-words"><p>{row.description}</p>{row.invoice_no && <p className="break-all font-mono text-xs text-muted-foreground">{row.invoice_no}</p>}</div>;
  const amount = (row: TransactionEntry) => <div className="space-y-1"><p className={`money font-medium ${row.direction === "expense" ? "text-destructive" : ""}`}>{row.direction === "expense" ? "−" : row.direction === "income" ? "+" : ""}{money(row.amount_cents)}</p><p className="text-xs text-muted-foreground">{transactionChannels[row.channel] ?? row.channel} · {row.channel === "gateway_topup" && row.direction === "neutral" ? "余额内部入账" : transactionDirections[row.direction] ?? row.direction}</p>{row.balance_after !== null && <p className="money text-xs text-muted-foreground">余额 {money(row.balance_after)}</p>}</div>;
  const source = (row: TransactionEntry) => <div className="space-y-1"><p>{transactionKinds[row.kind as keyof typeof transactionKinds] ?? row.kind}</p><p className="text-xs text-muted-foreground">{sources[row.source] ?? row.source}</p></div>;
  const reference = (row: TransactionEntry) => <dl className="space-y-2 text-xs"><div><dt className="text-muted-foreground">销售（当前）</dt><dd className="mt-1 break-words">{row.sales_owner_name ?? "—"}</dd></div><div><dt className="text-muted-foreground">流水号</dt><dd className="mt-1 break-all font-mono">{row.transaction_no || "—"}</dd></div></dl>;
  const action = (row: TransactionEntry) => row.invoice_id ? <Button asChild variant="outline" size="sm"><Link href={`/admin/invoices/${row.invoice_id}?return_to=${encodeURIComponent(returnHref)}`}>查看账单</Link></Button> : <span className="text-xs text-muted-foreground">无关联账单</span>;
  const columns: ResourceColumn<TransactionEntry>[] = [
    { id: "customer", header: "客户", render: customer, className: "min-w-36 max-w-52 whitespace-normal" },
    { id: "time", header: "发生时间", render: row => date(row.occurred_at) },
    { id: "description", header: "描述 / 账单", render: description, className: "min-w-44 max-w-72 whitespace-normal" },
    { id: "amount", header: "金额 / 渠道", render: amount, className: "min-w-40 whitespace-normal" },
    { id: "source", header: "类型 / 来源", render: source, className: "whitespace-normal" },
    { id: "reference", header: "销售 / 流水号", render: reference, className: "min-w-40 max-w-56 whitespace-normal" },
    { id: "action", header: "操作", render: action },
  ];
  return <ResourceTable items={items} rowKey={row => row.event_key} columns={columns} className="min-w-0" ariaLabel="交易流水列表" empty="当前条件下暂无流水"
    renderMobile={row => <article className="space-y-4 p-4"><div className="flex flex-wrap items-start justify-between gap-3"><div className="min-w-0">{customer(row)}</div>{amount(row)}</div><p className="text-xs text-muted-foreground">{date(row.occurred_at)}</p>{description(row)}{source(row)}{reference(row)}{action(row)}</article>} />;
}
