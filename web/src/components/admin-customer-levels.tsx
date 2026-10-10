"use client";

import { useState } from "react";
import { Pencil, Plus, RefreshCw, Save, Trash2 } from "lucide-react";
import { api } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { AdminPagination } from "./admin-pagination";
import { Failure, Loading } from "./common";
import { Workspace, PermissionGate, useAdminPermission } from "./workspace";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "./ui/dialog";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";

type Level = {
  id: number;
  name: string;
  min_income_cents: number;
  min_purchase_count: number;
  min_login_count: number;
  min_recent_login_count: number;
  min_renewal_count: number;
  min_recent_renewal_count: number;
  sort_order: number;
  active: boolean;
  version: number;
  matching_customer_count: number;
};
type Result = { items: Level[]; total: number; limit: number; offset: number; recent_window_days: number };
type Form = Omit<Level, "id" | "version" | "matching_customer_count"> & { id?: number; version?: number };

const emptyForm = (): Form => ({ name: "", min_income_cents: 0, min_purchase_count: 0, min_login_count: 0, min_recent_login_count: 0, min_renewal_count: 0, min_recent_renewal_count: 0, sort_order: 0, active: true });
const newKey = (prefix: string) => `${prefix}-${crypto.randomUUID()}`;

export function AdminCustomerLevelsView() {
  const [query, setQuery] = useState({ q: "", active: "", offset: 0, limit: 25 });
  const resource = useResource<Result>(`/admin/customer-levels?q=${encodeURIComponent(query.q)}&active=${query.active}&offset=${query.offset}&limit=${query.limit}`, false);
  const manage = useAdminPermission("customer_levels.manage");
  const [form, setForm] = useState<Form | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  function change(patch: Partial<typeof query>) { setQuery(current => ({ ...current, ...patch, ...(patch.offset === undefined ? { offset: 0 } : {}) })); }
  function edit(item: Level) { setMessage(""); setForm({ ...item }); }
  async function save(event: React.FormEvent) {
    event.preventDefault(); if (!form || busy || !manage) return;
    setBusy(true); setMessage("");
    try {
      const body = { ...form, ...(form.id ? { expected_version: form.version } : {}) };
      await api(form.id ? `/admin/customer-levels/${form.id}` : "/admin/customer-levels", { method: form.id ? "PATCH" : "POST", headers: { "Idempotency-Key": newKey("customer-level") }, body: JSON.stringify(body) });
      setForm(null); resource.refresh();
    } catch (error) { setMessage((error as Error).message); } finally { setBusy(false); }
  }
  async function remove(item: Level) {
    if (!manage || busy || !window.confirm(`确认删除客户等级“${item.name}”吗？`)) return;
    setBusy(true); setMessage("");
    try { await api(`/admin/customer-levels/${item.id}`, { method: "DELETE", headers: { "Idempotency-Key": newKey("customer-level-delete") }, body: JSON.stringify({ expected_version: item.version }) }); resource.refresh(); }
    catch (error) { setMessage((error as Error).message); } finally { setBusy(false); }
  }
  function setNumber(field: keyof Form, value: string) { setForm(current => current ? { ...current, [field]: Math.max(0, Number(value) || 0) } : current); }
  return <Workspace title="客户等级" admin><div className="min-w-0 max-w-full space-y-5"><div className="flex flex-wrap items-center justify-between gap-3"><div><h1 className="page-title">客户等级</h1><p className="mt-2 text-sm text-muted-foreground">按客户收入、购买、登录和续费条件匹配等级；最近登录与续费按最近 {resource.data?.recent_window_days ?? 30} 天统计。</p></div><div className="flex gap-2"><Button variant="outline" size="icon" title="刷新" aria-label="刷新" onClick={resource.refresh}><RefreshCw /></Button><PermissionGate permission="customer_levels.manage"><Button onClick={() => { setMessage(""); setForm(emptyForm()); }}><Plus />添加客户等级</Button></PermissionGate></div></div><div className="flex flex-wrap gap-3 border-y py-4"><Input aria-label="关键词" placeholder="搜索等级名称" value={query.q} onChange={event => change({ q: event.target.value })} /><select aria-label="启用状态" value={query.active} onChange={event => change({ active: event.target.value })}><option value="">全部状态</option><option value="true">启用</option><option value="false">停用</option></select></div>{message && !form && <p role="alert" className="text-sm text-destructive">{message}</p>}{resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <><div className="min-w-0 overflow-x-auto"><Table className="min-w-[1150px]"><TableHeader><TableRow>{["等级名称","收入门槛","购买商品数","累计登录","最近登录(30天)","续费次数","最近续费(30天)","匹配客户","状态","操作"].map(label => <TableHead key={label}>{label}</TableHead>)}</TableRow></TableHeader><TableBody>{resource.data.items.map(item => <TableRow key={item.id}><TableCell><span className="font-medium">{item.name}</span><div className="text-xs text-muted-foreground">排序 {item.sort_order}</div></TableCell><TableCell>{item.min_income_cents.toLocaleString()} 分</TableCell><TableCell>{item.min_purchase_count}</TableCell><TableCell>{item.min_login_count}</TableCell><TableCell>{item.min_recent_login_count}</TableCell><TableCell>{item.min_renewal_count}</TableCell><TableCell>{item.min_recent_renewal_count}</TableCell><TableCell>{item.matching_customer_count}</TableCell><TableCell>{item.active ? "启用" : "停用"}</TableCell><TableCell><div className="flex gap-1"><PermissionGate permission="customer_levels.manage"><Button size="icon-sm" variant="ghost" title="编辑" aria-label={`编辑${item.name}`} onClick={() => edit(item)}><Pencil /></Button><Button size="icon-sm" variant="ghost" title="删除" aria-label={`删除${item.name}`} onClick={() => void remove(item)}><Trash2 /></Button></PermissionGate></div></TableCell></TableRow>)}{resource.data.items.length === 0 && <TableRow><TableCell colSpan={10} className="py-12 text-center text-muted-foreground">暂无客户等级</TableCell></TableRow>}</TableBody></Table></div><AdminPagination total={resource.data.total} page={Math.floor(resource.data.offset / resource.data.limit)} pageSize={resource.data.limit} onPageChange={page => change({ offset: page * resource.data!.limit })} onPageSizeChange={limit => change({ limit, offset: 0 })} /></>}</div><Dialog open={form !== null} onOpenChange={open => { if (!open && !busy) setForm(null); }}><DialogContent className="max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>{form?.id ? "编辑客户等级" : "添加客户等级"}</DialogTitle><DialogDescription>客户满足全部阈值后计入匹配客户数，保存会产生版本。</DialogDescription></DialogHeader>{form && <form onSubmit={save} className="space-y-4"><fieldset disabled={busy || !manage} className="space-y-4"><label className="space-y-2 text-sm"><Label>客户等级名称</Label><Input required maxLength={120} value={form.name} onChange={event => setForm({ ...form, name: event.target.value })} /></label><div className="grid sm:grid-cols-2 gap-4">{([["min_income_cents","收入门槛（分）"],["min_purchase_count","购买商品数量"],["min_login_count","累计登录次数"],["min_recent_login_count","最近登录次数"],["min_renewal_count","累计续费次数"],["min_recent_renewal_count","最近续费次数"]] as const).map(([field,label]) => <label className="space-y-2 text-sm" key={field}><Label>{label}</Label><Input type="number" min={0} value={form[field]} onChange={event => setNumber(field, event.target.value)} /></label>)}</div><label className="space-y-2 text-sm"><Label>显示排序</Label><Input type="number" min={-100000} max={100000} value={form.sort_order} onChange={event => setNumber("sort_order", event.target.value)} /></label><label className="flex items-center gap-2 text-sm"><Checkbox checked={form.active} onCheckedChange={checked => setForm({ ...form, active: checked === true })} />启用</label>{message && <p role="alert" className="text-sm text-destructive">{message}</p>}<div className="flex flex-wrap gap-2 border-t pt-4"><Button><Save />{busy ? "保存中" : "保存"}</Button><Button type="button" variant="outline" onClick={() => setForm(null)}>取消</Button></div></fieldset></form>}</DialogContent></Dialog></Workspace>;
}
