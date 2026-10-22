"use client";

import type { ReactNode } from "react";
import Link from "next/link";
import { RefreshCw, Search, Server } from "lucide-react";
import { date, money, type Service, type ServicePage } from "@/lib/api";
import { AdminListPage, AdminListRegion } from "./admin-list-page";
import { AdminServiceBatchRenewals } from "./admin-service-batch-renewals";
import { AdminPagination } from "./admin-pagination";
import { ResourceTable, type ResourceColumn } from "./resource-table";
import { Status, cycles, statusNames } from "./common";
import { OrderRefundHoldLabel } from "./external-refund-order";
import { PermissionGate, useAdminPermission } from "./workspace";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";

function due(service: Service) {
  return service.next_due_at ? date(service.next_due_at) : service.billing_cycle === "once" ? "一次性服务" : "尚未起算";
}

// Layout only. The caller owns the existing URL query, polling and mutations.
export function AdminServiceList({ resource, states, status, setStatus, draft, setDraft, search, page, setPage, suffix, renderActions }: {
  resource: { data: ServicePage | null; error: Error | null; isLoading: boolean; refresh: () => void };
  states: readonly string[];
  status: string;
  setStatus: (status: string) => void;
  draft: string;
  setDraft: (draft: string) => void;
  search: () => void;
  page: number;
  setPage: (page: number) => void;
  suffix: string;
  renderActions: (service: Service) => ReactNode;
}) {
  const allowed = useAdminPermission("services.view");
  const href = (service: Service) => `/admin/services/${service.id}${suffix}`;
  const identity = (service: Service) => <Link className="block min-w-0 text-primary hover:underline" href={href(service)}>
    <span className="flex items-center gap-2 font-medium"><Server className="size-4 shrink-0" /><span className="break-words">{service.name}</span></span>
    <span className="mt-1 block text-xs text-muted-foreground">#{service.id} · {service.service_group_name ?? "未分组"}</span>
  </Link>;
  const customer = (service: Service) => <span>{service.customer_name || `客户 #${service.user_id}`}</span>;
  const address = (service: Service) => <div className="max-w-60 space-y-1 break-all font-mono text-xs"><p>{service.domain || "未设置主机名"}</p><p className="text-muted-foreground">{service.resource_info?.dedicated_ip || service.resource_info?.ips?.[0] || "未分配 IP"}</p></div>;
  const price = (service: Service) => <div><p className="money">{money(service.recurring_price_cents)}</p><p className="mt-1 text-xs text-muted-foreground">{cycles[service.billing_cycle] || service.billing_cycle}</p></div>;
  const state = (service: Service) => <div className="flex flex-wrap gap-1.5"><Status value={service.status} /><OrderRefundHoldLabel value={service.external_refund_order_hold} /></div>;
  const columns: ResourceColumn<Service>[] = [
    { id: "service", header: "服务", render: identity, className: "min-w-44 max-w-72 whitespace-normal" },
    { id: "customer", header: "客户", render: customer },
    { id: "address", header: "主机名 / IP", render: address },
    { id: "price", header: "合同周期价", render: price },
    { id: "due", header: "客户到期时间", render: due },
    { id: "status", header: "状态", render: state },
    { id: "actions", header: "操作", render: service => <div className="space-y-2"><Button asChild size="sm" variant="outline"><Link href={href(service)}>查看服务</Link></Button>{renderActions(service)}</div> },
  ];
  return <AdminListPage title="产品服务" description="查看服务运行状态、合同周期价与客户到期时间。"
    actions={<Button variant="outline" disabled={resource.isLoading || !allowed} onClick={resource.refresh}><RefreshCw className="size-4" />刷新服务列表</Button>}
    filters={<form aria-label="服务查询" className="grid min-w-0 gap-3 rounded-xl border bg-card p-4 sm:grid-cols-[12rem_minmax(0,1fr)_auto] sm:items-end" onSubmit={event => { event.preventDefault(); search(); }}>
      <label className="min-w-0 space-y-2 text-sm"><span>服务状态</span><Select value={status || "all"} onValueChange={value => setStatus(value === "all" ? "" : value)}><SelectTrigger aria-label="服务状态筛选" className="w-full"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="all">全部状态</SelectItem>{states.map(value => <SelectItem key={value} value={value}>{statusNames[value] || value}</SelectItem>)}</SelectContent></Select></label>
      <label className="min-w-0 space-y-2 text-sm"><span>关键词</span><Input aria-label="搜索服务" value={draft} onChange={event => setDraft(event.target.value)} placeholder="搜索服务名称或标识" /></label>
      <Button type="submit" variant="outline"><Search className="size-4" />搜索</Button>
    </form>}>
    <AdminServiceBatchRenewals services={allowed && !resource.error ? resource.data?.items ?? [] : []} refresh={resource.refresh}/>
    <AdminListRegion title="服务查询结果" allowed={allowed} resource={resource} retry={resource.refresh}
      summary={resource.data && `共 ${resource.data.total} 个服务`}
      pagination={data => <AdminPagination total={data.total} page={page} pageSize={20} onPageChange={setPage} />}>
      {data => <ResourceTable items={data.items} rowKey={service => service.id} columns={columns} className="min-w-0" ariaLabel="服务列表" empty="当前条件下暂无服务"
        renderMobile={service => <article className="space-y-4 p-4">
          <div className="flex items-start justify-between gap-3"><div className="min-w-0">{identity(service)}</div>{state(service)}</div>
          <dl className="grid grid-cols-2 gap-4 text-sm"><div><dt className="text-xs text-muted-foreground">客户</dt><dd className="mt-1 break-words">{customer(service)}</dd></div><div><dt className="text-xs text-muted-foreground">合同周期价</dt><dd className="mt-1">{price(service)}</dd></div><div className="col-span-2"><dt className="text-xs text-muted-foreground">主机名 / IP</dt><dd className="mt-1">{address(service)}</dd></div><div className="col-span-2"><dt className="text-xs text-muted-foreground">客户到期时间</dt><dd className="mt-1">{due(service)}</dd></div></dl>
          <div className="flex flex-wrap items-center gap-2"><Button asChild variant="outline"><Link href={href(service)}>查看服务</Link></Button><PermissionGate permission="customers.view"><Button asChild variant="ghost"><Link href={`/admin/customers/${service.user_id}?tab=services`}>查看客户</Link></Button></PermissionGate></div>
          {renderActions(service)}
        </article>} />}
    </AdminListRegion>
  </AdminListPage>;
}
