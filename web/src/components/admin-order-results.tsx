"use client";

import type { ReactNode } from "react";
import Link from "next/link";
import { date, money } from "@/lib/api";
import type { Row } from "./admin-data-lists";
import { ResourceTable, type ResourceColumn } from "./resource-table";
import { Status } from "./common";
import { OrderRefundOrderStatus } from "./external-refund-order";
import { Button } from "./ui/button";

// Presentation only; selection, permissions, commissions and mutations belong to the caller.
export function AdminOrderResults({ items, suffix, supplier, selectAll, selection, actions, commission }: {
  items: Row[];
  suffix: string;
  supplier: boolean;
  selectAll: ReactNode;
  selection: (row: Row) => ReactNode;
  actions: (row: Row) => ReactNode;
  commission: (row: Row) => ReactNode;
}) {
  const href = (row: Row) => `/admin/orders/${row.id}${suffix}`;
  const identity = (row: Row) => <div className="space-y-1"><Link href={href(row)} className="break-all font-mono text-xs text-primary hover:underline">{String(row.order_no || `#${row.id}`)}</Link><p className="break-words font-medium">{String(row.customer_name || "未提供客户名称")}</p><p className="text-xs text-muted-foreground">{date(String(row.created_at))}</p></div>;
  const product = (row: Row) => <div className="space-y-1 break-words"><p>{String(row.product_names || "未提供商品摘要")}</p><p className="break-all font-mono text-xs text-muted-foreground">{String(row.service_domains || "未分配服务域名")}</p><p className="break-all font-mono text-xs text-muted-foreground">{String(row.service_ips || "未分配 IP")}</p></div>;
  const payment = (row: Row) => <div className="space-y-1"><p className="money font-medium">{money(Number(row.total_cents))}</p><div>{row.invoice_id == null ? <span className="text-xs text-muted-foreground">未生成账单</span> : <Status value={String(row.invoice_status)} />}</div><p className="text-xs text-muted-foreground">{String(row.payment_method || "未记录付款方式")}</p></div>;
  const status = (row: Row) => <div className="space-y-2"><div><OrderRefundOrderStatus row={row} /></div><div className="flex flex-wrap items-center gap-1.5"><span className="text-xs text-muted-foreground">审核</span><OrderRefundOrderStatus row={row} review /></div></div>;
  const context = (row: Row) => <details className="min-w-0 text-xs"><summary className="cursor-pointer rounded py-2 font-medium focus-visible:outline-2 focus-visible:outline-ring">备注与销售{supplier ? " / 供应商" : ""}</summary><dl className="mt-2 space-y-3 break-words">
    <div><dt className="text-muted-foreground">客户资料备注</dt><dd className="mt-1 whitespace-pre-wrap">{String(row.customer_note || "无")}</dd></div>
    <div><dt className="text-muted-foreground">订单备注</dt><dd className="mt-1 whitespace-pre-wrap">{String(row.notes || "无")}</dd></div>
    <div><dt className="text-muted-foreground">客户销售</dt><dd className="mt-1">{String(row.sales_owner_name || "未分配")}{commission(row)}</dd></div>
    {supplier && <div><dt className="text-muted-foreground">供应商 / 成本快照</dt><dd className="mt-1 space-y-1">{(Array.isArray(row.supplier_resources) ? row.supplier_resources : []).map((resource: Record<string, unknown>) => <p key={String(resource.service_id)}>{String(resource.supplier_name || `供应商 #${resource.supplier_id}`)} · 服务 #{String(resource.service_id)} · {resource.cost_cents == null ? "成本未记录" : `${money(Number(resource.cost_cents))} CNY`}</p>)}</dd></div>}
  </dl></details>;
  const controls = (row: Row) => <div className="flex flex-wrap items-center gap-2"><Button asChild size="sm" variant="outline"><Link href={href(row)}>查看订单</Link></Button>{actions(row)}</div>;
  const columns: ResourceColumn<Row>[] = [
    { id: "selection", header: selectAll, render: selection, className: "w-10" },
    { id: "identity", header: "订单 / 客户 / 时间", render: identity, className: "min-w-48 max-w-64 whitespace-normal" },
    { id: "product", header: "产品 / 服务", render: product, className: "min-w-44 max-w-64 whitespace-normal" },
    { id: "payment", header: "净应付 / 付款", render: payment },
    { id: "status", header: "订单状态 / 审核", render: status, className: "max-w-60 whitespace-normal" },
    { id: "context", header: "补充信息", render: context, className: "min-w-36 max-w-64 whitespace-normal" },
    { id: "actions", header: "操作", render: controls, className: "min-w-28 max-w-60 whitespace-normal" },
  ];
  return <ResourceTable items={items} rowKey={row => String(row.id)} columns={columns} ariaLabel="订单列表" className="min-w-0" empty="当前条件下暂无订单"
    renderMobile={row => <article className="space-y-4 p-4">
      <div className="flex items-start gap-3">{selection(row)}<div className="min-w-0 flex-1">{identity(row)}</div></div>
      {status(row)}{product(row)}{payment(row)}{context(row)}{controls(row)}
    </article>} />;
}
