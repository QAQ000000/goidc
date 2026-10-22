"use client";

import { useState } from "react";
import { Pencil, Plus, RefreshCw, Save, Trash2 } from "lucide-react";
import { api } from "@/lib/api";
import { customerLevelInput } from "@/lib/admin-configuration-inputs";
import { useResource } from "@/hooks/use-resource";
import { AdminPagination } from "./admin-pagination";
import { AdminListPage, AdminListRegion } from "./admin-list-page";
import { ResourceTable, type ResourceColumn } from "./resource-table";
import { Badge } from "./ui/badge";
import { Workspace, PermissionGate, useAdminPermission } from "./workspace";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "./ui/dialog";
import { Input } from "./ui/input";
import { Label } from "./ui/label";

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
      const body = customerLevelInput(form);
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
  const allowed = useAdminPermission("customer_levels.view");
  const identity = (item: Level) => <div className="space-y-1 break-words"><p className="font-medium">{item.name}</p><p className="text-xs text-muted-foreground">#{item.id} · 排序 {item.sort_order}</p></div>;
  const criteria = (item: Level) => <dl className="grid grid-cols-2 gap-x-5 gap-y-2 text-xs"><div><dt className="text-muted-foreground">收入门槛</dt><dd>{item.min_income_cents.toLocaleString()} 分</dd></div><div><dt className="text-muted-foreground">购买商品数</dt><dd>{item.min_purchase_count}</dd></div><div><dt className="text-muted-foreground">累计登录</dt><dd>{item.min_login_count}</dd></div><div><dt className="text-muted-foreground">最近登录</dt><dd>{item.min_recent_login_count}</dd></div><div><dt className="text-muted-foreground">累计续费</dt><dd>{item.min_renewal_count}</dd></div><div><dt className="text-muted-foreground">最近续费</dt><dd>{item.min_recent_renewal_count}</dd></div></dl>;
  const state = (item: Level) => <Badge variant="outline">{item.active ? "启用" : "停用"}</Badge>;
  const actions = (item: Level) => <PermissionGate permission="customer_levels.manage"><div className="flex gap-1"><Button disabled={busy} size="icon-sm" variant="ghost" title="编辑" aria-label={`编辑${item.name}`} onClick={() => edit(item)}><Pencil /></Button><Button disabled={busy} size="icon-sm" variant="ghost" title="删除" aria-label={`删除${item.name}`} onClick={() => void remove(item)}><Trash2 /></Button></div></PermissionGate>;
  const columns: ResourceColumn<Level>[] = [
    { id: "name", header: "等级名称", render: identity, className: "min-w-40 max-w-64 whitespace-normal" },
    { id: "criteria", header: "匹配条件（全部满足）", render: criteria, className: "min-w-64" },
    { id: "count", header: "匹配客户", render: item => item.matching_customer_count },
    { id: "state", header: "状态", render: state },
    { id: "actions", header: "操作", render: actions },
  ];
  return <Workspace title="客户等级" admin><AdminListPage title="客户等级" description="客户满足全部收入、购买、登录与续费阈值时计入匹配人数；等级规则与客户组折扣独立。"
    actions={<><Button variant="outline" disabled={resource.isLoading} onClick={resource.refresh}><RefreshCw />刷新</Button><PermissionGate permission="customer_levels.manage"><Button onClick={() => { setMessage(""); setForm(emptyForm()); }}><Plus />添加客户等级</Button></PermissionGate></>}
    filters={<div className="flex flex-wrap items-center gap-3"><Input className="w-full sm:max-w-xs" aria-label="关键词" placeholder="搜索等级名称" value={query.q} onChange={event => change({ q: event.target.value })} /><select className="h-11 rounded-md border border-input bg-background px-3 text-sm md:h-9" aria-label="启用状态" value={query.active} onChange={event => change({ active: event.target.value })}><option value="">全部状态</option><option value="true">启用</option><option value="false">停用</option></select></div>}
    feedback={message && !form && <p role="alert" className="text-sm text-destructive">{message}</p>}>
    <AdminListRegion title="客户等级列表" allowed={allowed} resource={resource} retry={resource.refresh} summary={resource.data && `共 ${resource.data.total} 条 · 最近条件按 ${resource.data.recent_window_days} 天统计`}
      pagination={data => <AdminPagination total={data.total} page={Math.floor(query.offset / query.limit)} pageSize={query.limit} onPageChange={page => change({ offset: page * query.limit })} onPageSizeChange={limit => change({ limit, offset: 0 })} />}>
      {data => <ResourceTable items={data.items} rowKey={item => item.id} columns={columns} ariaLabel="客户等级列表" className="min-w-0" empty="当前条件下暂无客户等级"
        renderMobile={item => <article className="space-y-4 p-4"><div className="flex items-start justify-between gap-3">{identity(item)}{state(item)}</div>{criteria(item)}<p className="text-sm">匹配客户 {item.matching_customer_count} 人</p>{actions(item)}</article>} />}
    </AdminListRegion>
  </AdminListPage><Dialog open={form !== null} onOpenChange={open => { if (!open && !busy) setForm(null); }}><DialogContent className="max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>{form?.id ? "编辑客户等级" : "添加客户等级"}</DialogTitle><DialogDescription>客户满足全部阈值后计入匹配客户数，保存会产生版本。</DialogDescription></DialogHeader>{form && <form onSubmit={save} className="space-y-4"><fieldset disabled={busy || !manage} className="space-y-4"><label className="space-y-2 text-sm"><Label>客户等级名称</Label><Input required maxLength={120} value={form.name} onChange={event => setForm({ ...form, name: event.target.value })} /></label><div className="grid sm:grid-cols-2 gap-4">{([["min_income_cents","收入门槛（分）"],["min_purchase_count","购买商品数量"],["min_login_count","累计登录次数"],["min_recent_login_count","最近登录次数"],["min_renewal_count","累计续费次数"],["min_recent_renewal_count","最近续费次数"]] as const).map(([field,label]) => <label className="space-y-2 text-sm" key={field}><Label>{label}</Label><Input type="number" min={0} value={form[field]} onChange={event => setNumber(field, event.target.value)} /></label>)}</div><label className="space-y-2 text-sm"><Label>显示排序</Label><Input type="number" min={-100000} max={100000} value={form.sort_order} onChange={event => setNumber("sort_order", event.target.value)} /></label><label className="flex items-center gap-2 text-sm"><Checkbox checked={form.active} onCheckedChange={checked => setForm({ ...form, active: checked === true })} />启用</label>{message && <p role="alert" className="text-sm text-destructive">{message}</p>}<div className="flex flex-wrap gap-2 border-t pt-4"><Button><Save />{busy ? "保存中" : "保存"}</Button><Button type="button" variant="outline" onClick={() => setForm(null)}>取消</Button></div></fieldset></form>}</DialogContent></Dialog></Workspace>;
}
