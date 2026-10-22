"use client";

import Link from "next/link";
import { date, money } from "@/lib/api";
import type { Application } from "./invoice-applications";
import { ApplicationStatus } from "./invoice-application-status";
import { ResourceTable, type ResourceColumn } from "./resource-table";
import { Button } from "./ui/button";

export function AdminInvoiceApplicationResults({ items, suffix }: { items: Application[]; suffix: string }) {
  const href = (item: Application) => `/admin/invoice-applications/${item.id}${suffix}`;
  const identity = (item: Application) => <div className="space-y-1"><Link className="font-medium text-primary hover:underline" href={href(item)}>申请 #{item.id}</Link><p className="text-xs text-muted-foreground">{date(item.created_at)}</p></div>;
  const customer = (item: Application) => <div className="space-y-1 break-words"><p>{item.customer_name || `客户 #${item.user_id}`}</p><p className="break-all text-xs text-muted-foreground">{item.customer_email}</p></div>;
  const title = (item: Application) => <div className="space-y-1 break-words"><p>{item.title || "未提供抬头"}</p><p className="text-xs text-muted-foreground">{item.profile_type === "company" ? "公司" : "个人"}</p></div>;
  const action = (item: Application) => <Button asChild variant="outline" size="sm"><Link href={href(item)} aria-label={`查看申请 #${item.id}`}>查看详情</Link></Button>;
  const columns: ResourceColumn<Application>[] = [
    { id: "identity", header: "申请 / 提交时间", render: identity, className: "min-w-44" },
    { id: "customer", header: "客户", render: customer, className: "min-w-36 max-w-60 whitespace-normal" },
    { id: "title", header: "发票抬头", render: title, className: "min-w-40 max-w-72 whitespace-normal" },
    { id: "amount", header: "申请金额", render: item => money(item.amount_cents), className: "money font-medium" },
    { id: "status", header: "审核状态", render: item => <ApplicationStatus status={item.status} /> },
    { id: "actions", header: "操作", render: action },
  ];
  return <ResourceTable items={items} rowKey={item => item.id} columns={columns} ariaLabel="发票申请列表" className="min-w-0" empty="当前状态下暂无发票申请"
    renderMobile={item => <article className="space-y-4 p-4"><div className="flex flex-wrap items-start justify-between gap-3">{identity(item)}<ApplicationStatus status={item.status} /></div>{title(item)}<p className="money text-lg font-semibold">{money(item.amount_cents)}</p>{customer(item)}{action(item)}</article>} />;
}
