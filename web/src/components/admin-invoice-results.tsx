"use client";

import type { ReactNode } from "react";
import Link from "next/link";
import { money } from "@/lib/api";
import type { InvoiceRow } from "./transactions";
import { ResourceTable, type ResourceColumn } from "./resource-table";
import { Status } from "./common";
import { time, OrderLink } from "./transaction-links";
import { Button } from "./ui/button";

export function invoiceKindLabel(kind?: string) {
  return kind === "topup" ? "在线充值账单" : kind === "manual" ? "人工账单" : "自动账单";
}

// Layout only: selection, permission checks and financial mutations stay with the caller.
export function AdminInvoiceResults({ items, suffix, selectAll, selection, actions }: {
  items: InvoiceRow[];
  suffix: string;
  selectAll?: ReactNode;
  selection?: (row: InvoiceRow) => ReactNode;
  actions: (row: InvoiceRow) => ReactNode;
}) {
  const href = (row: InvoiceRow) => `/admin/invoices/${row.id}${suffix}`;
  const identity = (row: InvoiceRow) => <div className="space-y-1"><Link className="break-all font-mono text-xs text-primary hover:underline" href={href(row)}>{row.invoice_no}</Link><p className="break-words font-medium">{row.customer_name || `客户 #${row.user_id}`}</p><p className="text-xs text-muted-foreground">{invoiceKindLabel(row.invoice_kind)}</p></div>;
  const source = (row: InvoiceRow) => row.order_id ? <OrderLink admin id={row.order_id} label={row.order_no ?? `#${row.order_id}`} /> : <span className="text-muted-foreground">无关联订单</span>;
  const status = (row: InvoiceRow) => <div className="space-y-1.5"><Status value={row.status} />{row.import_review_required && <p className="text-xs text-destructive">导入待复核</p>}{row.payment_pending && <p className="text-xs text-muted-foreground">支付待确认</p>}{row.payment_close_anomaly && <p className="text-xs text-destructive">关单收款异常</p>}</div>;
  const controls = (row: InvoiceRow) => <div className="flex flex-wrap gap-2"><Button asChild variant="outline" size="sm"><Link href={href(row)}>查看账单</Link></Button>{actions(row)}</div>;
  const columns: ResourceColumn<InvoiceRow>[] = [
    ...(selection ? [{ id: "selection", header: selectAll, render: selection, className: "w-10" }] : []),
    { id: "identity", header: "账单 / 客户", render: identity, className: "min-w-48 max-w-72 whitespace-normal" },
    { id: "source", header: "关联订单", render: source, className: "max-w-56 whitespace-normal break-all" },
    { id: "amount", header: "应收金额", render: row => money(row.amount_cents), className: "money font-medium" },
    { id: "status", header: "账单状态", render: status, className: "whitespace-normal" },
    { id: "due", header: "到期时间", render: row => time(row.due_at) },
    { id: "actions", header: "操作", render: controls, className: "min-w-32 max-w-64 whitespace-normal" },
  ];
  return <ResourceTable items={items} rowKey={row => row.id} columns={columns} ariaLabel="账单列表" className="min-w-0" empty="当前条件下暂无账单"
    renderMobile={row => <article className="space-y-4 p-4">
      <div className="flex items-start gap-3">{selection?.(row)}<div className="min-w-0 flex-1">{identity(row)}</div></div>
      <div className="flex flex-wrap items-start justify-between gap-3"><p className="money text-lg font-semibold">{money(row.amount_cents)}</p>{status(row)}</div>
      <dl className="grid gap-3 text-sm"><div><dt className="text-xs text-muted-foreground">关联订单</dt><dd className="mt-1 break-all">{source(row)}</dd></div><div><dt className="text-xs text-muted-foreground">到期时间</dt><dd className="mt-1">{time(row.due_at)}</dd></div></dl>
      {controls(row)}
    </article>} />;
}
