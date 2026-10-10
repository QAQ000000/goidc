"use client";

import { useState } from "react";
import { RefreshCw, Search, UserRoundCog } from "lucide-react";
import { api } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace, PermissionGate } from "./workspace";
import { Failure, Loading } from "./common";
import { AdminPagination } from "./admin-pagination";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";

type Customer = { id: number; username: string; email: string; display_name: string; balance_cents: number; income_cents?: number; created_at: string; sales_version: number };
type Owner = { id: number; username: string; display_name: string };
type Page = { items: Customer[]; total: number; limit: number; offset: number };

function money(cents = 0) { return `¥${(cents / 100).toFixed(2)}`; }

export function AdminCustomerResourcesView() {
  const [query, setQuery] = useState("");
  const [draft, setDraft] = useState("");
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState(25);
  const [owner, setOwner] = useState("");
  const [busy, setBusy] = useState<number | null>(null);
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const resources = useResource<Page>(`/admin/customers/page?q=${encodeURIComponent(query)}&limit=${pageSize}&offset=${page * pageSize}&sales_owner_id=none`, false);
  const owners = useResource<Owner[]>("/admin/sales-owners", false);
  function search(event: React.FormEvent) { event.preventDefault(); setQuery(draft.trim()); setPage(0); }
  async function assign(customer: Customer) {
    if (!owner) return;
    setBusy(customer.id); setNotice(""); setError("");
    try {
      await api(`/admin/customers/${customer.id}/sales-owner`, { method: "PUT", body: JSON.stringify({ sales_owner_id: Number(owner), expected_version: customer.sales_version }) });
      setNotice(`客户 ${customer.display_name || customer.username} 已加入销售名下`);
      resources.refresh();
    } catch (e) { setError((e as Error).message); }
    finally { setBusy(null); }
  }
  return <Workspace title="客户资源池" admin><div className="space-y-5"><div className="flex flex-wrap items-end justify-between gap-3"><div><h1 className="page-title">客户资源池</h1><p className="mt-2 text-sm text-muted-foreground">查看尚未分配销售的客户，并将客户加入指定销售名下。</p></div><Button type="button" variant="outline" size="icon" title="刷新资源池" aria-label="刷新资源池" onClick={() => { resources.refresh(); owners.refresh(); }}><RefreshCw /></Button></div><div className="flex flex-wrap items-center gap-3 border-y py-4"><form onSubmit={search} className="flex min-w-[240px] flex-1 gap-2"><Input value={draft} onChange={event => setDraft(event.target.value)} placeholder="搜索客户名称、邮箱或账号" aria-label="搜索客户" /><Button type="submit"><Search />搜索</Button></form><Select value={owner} onValueChange={setOwner}><SelectTrigger className="w-64"><SelectValue placeholder="选择销售" /></SelectTrigger><SelectContent>{owners.data?.map(item => <SelectItem key={item.id} value={String(item.id)}>{item.display_name || item.username}</SelectItem>)}</SelectContent></Select><PermissionGate permission="customers.manage"><span className="text-xs text-muted-foreground">选择销售后可分配</span></PermissionGate></div>{notice && <p className="text-sm text-emerald-700" role="status">{notice}</p>}{error && <p className="text-sm text-destructive" role="alert">{error}</p>}{resources.error || owners.error ? <Failure error={resources.error ?? owners.error!} retry={() => { resources.refresh(); owners.refresh(); }} /> : !resources.data || !owners.data ? <Loading /> : <><div className="overflow-x-auto border-y"><Table><TableHeader><TableRow><TableHead>ID</TableHead><TableHead>客户</TableHead><TableHead>收入</TableHead><TableHead>余额</TableHead><TableHead>创建时间</TableHead><TableHead className="text-right">操作</TableHead></TableRow></TableHeader><TableBody>{resources.data.items.map(customer => <TableRow key={customer.id}><TableCell>{customer.id}</TableCell><TableCell><p className="font-medium">{customer.display_name || customer.username}</p><p className="text-xs text-muted-foreground">{customer.email}</p></TableCell><TableCell>{money(customer.income_cents)}</TableCell><TableCell>{money(customer.balance_cents)}</TableCell><TableCell>{new Date(customer.created_at).toLocaleString("zh-CN")}</TableCell><TableCell className="text-right"><Button size="sm" disabled={!owner || busy === customer.id} onClick={() => void assign(customer)}><UserRoundCog />{busy === customer.id ? "分配中" : "添加"}</Button></TableCell></TableRow>)}{!resources.data.items.length && <TableRow><TableCell colSpan={6} className="py-12 text-center text-muted-foreground">暂无未分配客户</TableCell></TableRow>}</TableBody></Table></div><AdminPagination page={page} pageSize={pageSize} total={resources.data.total} onPageChange={setPage} onPageSizeChange={value => { setPageSize(value); setPage(0); }} /></>}</div></Workspace>;
}
