"use client";

import Link from "next/link";
import { Users } from "lucide-react";
import { date, money } from "@/lib/api";
import { Button } from "./ui/button";
import { Status } from "./common";
import { ResourceTable, type ResourceColumn } from "./resource-table";

export type AdminCustomer = {
  id: number;
  username: string;
  email: string;
  display_name: string;
  status: string;
  balance_cents: number;
  created_at: string;
  sales_owner_id: number | null;
  sales_owner_name: string | null;
  sales_version: number;
  group_id: number | null;
  group_name: string | null;
  active_services_count?: number;
  total_services_count?: number;
  income_cents?: number;
  expense_cents?: number;
  company_name?: string | null;
  phone_number?: string | null;
};

export type CustomerDirectoryPage = { items: AdminCustomer[]; total: number; limit: number; offset: number };

function CustomerDirectoryStatus({ status }: { status: string }) {
  return <span className="[&_[data-slot=badge]]:rounded"><Status value={status} /></span>;
}

function customerColumns(detailHref: (customer: AdminCustomer) => string): readonly ResourceColumn<AdminCustomer>[] {
  return [
    {
      id: "id",
      header: "编号",
      className: "px-4 py-3 tabular-nums",
      render: customer => <button type="button" className="text-primary hover:underline" title="复制客户编号" aria-label={`复制客户编号 ${customer.id}`} onClick={event => {
        const target = event.currentTarget;
        void navigator.clipboard.writeText(String(customer.id)).then(() => { target.title = "已复制"; }).catch(() => { target.title = "复制失败，请手动复制编号"; });
      }}>#{customer.id}</button>,
    },
    {
      id: "customer",
      header: "客户",
      className: "px-4 py-3",
      render: customer => <div className="flex items-center gap-3"><span aria-hidden="true" className="flex size-9 shrink-0 items-center justify-center rounded-full bg-muted text-xs font-semibold text-muted-foreground">{Array.from(customer.display_name || customer.username)[0]}</span><div className="min-w-0"><Link href={detailHref(customer)} className="font-medium text-foreground hover:underline">{customer.display_name || customer.username}</Link><div className="mt-1 text-xs text-muted-foreground">{customer.username} · {customer.email}</div>{customer.company_name && <div className="mt-1 text-xs text-muted-foreground">{customer.company_name}</div>}</div></div>,
    },
    { id: "status", header: "账号状态", className: "px-4 py-3", render: customer => <CustomerDirectoryStatus status={customer.status} /> },
    { id: "group", header: "客户分组", className: "px-4 py-3 text-muted-foreground", render: customer => customer.group_name ? <span className="rounded-md border border-border px-2 py-1 text-xs">{customer.group_name}</span> : "未分组" },
    { id: "sales", header: "销售归属", className: "px-4 py-3 text-muted-foreground", render: customer => customer.sales_owner_name || "无归属" },
    { id: "balance", header: "钱包余额", headerClassName: "text-right", className: "px-4 py-3 text-right tabular-nums", render: customer => money(customer.balance_cents) },
    { id: "created", header: "注册时间", className: "px-4 py-3 text-xs tabular-nums text-muted-foreground", render: customer => date(customer.created_at) },
    { id: "actions", header: "操作", className: "px-4 py-3", render: customer => <Button asChild variant="outline" size="sm"><Link href={detailHref(customer)}>查看客户</Link></Button> },
  ];
}

function CustomerDirectoryCard({ customer, href }: { customer: AdminCustomer; href: string }) {
  return <article className="space-y-3 p-4">
    <div className="flex items-start justify-between gap-3"><div className="min-w-0"><Link href={href} className="break-words font-medium text-primary">{customer.display_name || customer.username}</Link><p className="mt-1 text-xs text-muted-foreground">#{customer.id} · {customer.username}</p></div><CustomerDirectoryStatus status={customer.status} /></div>
    <p className="break-all text-sm text-muted-foreground">{customer.email}</p>
    <dl className="grid grid-cols-2 gap-x-3 gap-y-2 text-xs">
      <div><dt className="text-muted-foreground">客户分组</dt><dd className="mt-1 text-muted-foreground">{customer.group_name || "未分组"}</dd></div>
      <div><dt className="text-muted-foreground">销售归属</dt><dd className="mt-1 text-muted-foreground">{customer.sales_owner_name || "无归属"}</dd></div>
      <div><dt className="text-muted-foreground">钱包余额</dt><dd className="mt-1 tabular-nums">{money(customer.balance_cents)}</dd></div>
      <div><dt className="text-muted-foreground">注册时间</dt><dd className="mt-1 text-muted-foreground">{date(customer.created_at)}</dd></div>
    </dl>
    <Button variant="outline" className="w-full rounded shadow-none" asChild><Link href={href}>查看客户</Link></Button>
  </article>;
}

export function CustomerDirectoryResults({ items, detailHref, filtered, reset }: {
  items: readonly AdminCustomer[];
  detailHref: (customer: AdminCustomer) => string;
  filtered: boolean;
  reset: () => void;
}) {
  if (!items.length) return <div className="space-y-3 px-4 py-14 text-center"><Users size={28} className="mx-auto text-muted-foreground/60" /><p className="text-sm text-muted-foreground">{filtered ? "没有符合条件的客户" : "暂无客户"}</p>{filtered && <Button variant="outline" className="rounded shadow-none" onClick={reset}>清空筛选条件</Button>}</div>;
  return <ResourceTable items={items} rowKey={customer => customer.id} columns={customerColumns(detailHref)} ariaLabel="客户列表" className="min-w-0" tableClassName="min-w-[940px] [&_thead]:bg-muted/50" headerClassName="h-10 px-4 text-xs text-muted-foreground" rowClassName="border-border hover:bg-muted/50" renderMobile={customer => <CustomerDirectoryCard customer={customer} href={detailHref(customer)} />} />;
}
