"use client";

import { useState, type ComponentProps } from "react";
import { RefreshCw, Search } from "lucide-react";
import { useResource } from "@/hooks/use-resource";
import { Workspace, PermissionGate, useAdminIdentity } from "./workspace";
import { Failure, Loading } from "./common";
import { AdminPagination } from "./admin-pagination";
import { CustomerSalesAction } from "./admin-customers";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";

type Customer = ComponentProps<typeof CustomerSalesAction>["row"];
type Page = { items: Customer[]; total: number; limit: number; offset: number };

function money(cents = 0) { return `¥${(cents / 100).toFixed(2)}`; }

export function AdminCustomerResourcesView() {
  const actor = useAdminIdentity();
  return <Workspace title="客户资源池" admin>{actor ? <AdminCustomerResourcesForActor key={actor.id} actorID={actor.id} /> : <Loading />}</Workspace>;
}

function AdminCustomerResourcesForActor({ actorID }: { actorID: number }) {
  const [query, setQuery] = useState("");
  const [draft, setDraft] = useState("");
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState(25);
  const resources = useResource<Page>(`/admin/customers/page?q=${encodeURIComponent(query)}&limit=${pageSize}&offset=${page * pageSize}&sales_owner_id=none`, false);
  function search(event: React.FormEvent) { event.preventDefault(); setQuery(draft.trim()); setPage(0); }

  return <div className="space-y-5">
    <div className="flex flex-wrap items-end justify-between gap-3">
      <div><h1 className="page-title">客户资源池</h1><p className="mt-2 text-sm text-muted-foreground">查看尚未分配销售的客户，在客户对应的归属窗口中选择销售并确认。</p></div>
      <Button type="button" variant="outline" size="icon" title="刷新资源池" aria-label="刷新资源池" onClick={resources.refresh}><RefreshCw /></Button>
    </div>
    <form onSubmit={search} className="flex flex-wrap gap-2 border-y py-4">
      <Input className="min-w-[240px] flex-1" value={draft} onChange={event => setDraft(event.target.value)} placeholder="搜索客户名称、邮箱或账号" aria-label="搜索客户" />
      <Button type="submit"><Search />搜索</Button>
    </form>
    {resources.error ? <Failure error={resources.error} retry={resources.refresh} /> : !resources.data ? <Loading /> : <>
      <div className="overflow-x-auto border-y"><Table>
        <TableHeader><TableRow><TableHead>ID</TableHead><TableHead>客户</TableHead><TableHead>收入</TableHead><TableHead>余额</TableHead><TableHead>创建时间</TableHead><TableHead className="text-right">销售归属</TableHead></TableRow></TableHeader>
        <TableBody>{resources.data.items.map(customer => <TableRow key={customer.id}>
          <TableCell>{customer.id}</TableCell>
          <TableCell><p className="font-medium">{customer.display_name || customer.username}</p><p className="text-xs text-muted-foreground">{customer.email}</p></TableCell>
          <TableCell>{money(customer.income_cents)}</TableCell><TableCell>{money(customer.balance_cents)}</TableCell>
          <TableCell>{new Date(customer.created_at).toLocaleString("zh-CN")}</TableCell>
          <TableCell className="text-right"><PermissionGate permission="@super"><CustomerSalesAction key={`${actorID}:${customer.id}`} row={customer} refresh={resources.refresh} /></PermissionGate></TableCell>
        </TableRow>)}{!resources.data.items.length && <TableRow><TableCell colSpan={6} className="py-12 text-center text-muted-foreground">暂无未分配客户</TableCell></TableRow>}</TableBody>
      </Table></div>
      <AdminPagination page={page} pageSize={pageSize} total={resources.data.total} onPageChange={setPage} onPageSizeChange={value => { setPageSize(value); setPage(0); }} />
    </>}
  </div>;
}
