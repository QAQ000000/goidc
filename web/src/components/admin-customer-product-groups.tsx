"use client";

import Link from "next/link";
import { useState } from "react";
import { Pencil, Plus, RefreshCw, Save, Search, Trash2 } from "lucide-react";
import { api, type CustomerGroup, type CustomerProductDiscount, type CustomerProductGroup, type ProductPage } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { AdminPagination } from "./admin-pagination";
import { Failure, Loading } from "./common";
import { Workspace, PermissionGate, useAdminPermission } from "./workspace";
import { Button } from "./ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "./ui/dialog";
import { Input } from "./ui/input";

export function CustomerGroupTabs({ active }: { active: "customers" | "products" | "discounts" }) {
  const tabs = [
    ["customers", "客户分组", "/admin/customer-groups"],
    ["products", "商品分组", "/admin/customer-groups?tab=products"],
    ["discounts", "折扣设置", "/admin/customer-groups?tab=discounts"],
  ] as const;
  return <nav aria-label="客户分组设置" className="flex flex-wrap border-b border-[#ebeef5] gap-5">{tabs.map(([id, label, href]) => <Link key={id} href={href} className={`px-1 pb-3 text-xs border-b-2 ${active === id ? "border-[#409eff] text-[#409eff] font-semibold" : "border-transparent text-[#909399] hover:text-[#606266]"}`}>{label}</Link>)}</nav>;
}

type GroupPage = { items: CustomerProductGroup[]; total: number; limit: number; offset: number };
type Form = { id?: number; name: string; product_ids: number[]; version?: number };

export function CustomerProductGroupsView() {
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState(25);
  const resource = useResource<GroupPage>(`/admin/customer-product-groups?q=${encodeURIComponent(query)}&limit=${pageSize}&offset=${page * pageSize}`, false);
  const manage = useAdminPermission("customer_groups.manage");
  const [form, setForm] = useState<Form | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function remove(item: CustomerProductGroup) {
    if (!manage || busy || !window.confirm(`确定删除商品分组“${item.name}”吗？`)) return;
    setBusy(true); setError("");
    try {
      await api(`/admin/customer-product-groups/${item.id}`, { method: "DELETE", headers: { "Idempotency-Key": `customer-product-group-delete-${item.id}-${item.version}-${crypto.randomUUID()}` }, body: JSON.stringify({ expected_version: item.version }) });
      resource.refresh();
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }

  return <Workspace title="商品分组" admin><div className="bg-white rounded border border-[#ebeef5] shadow-sm p-4 sm:p-5 space-y-5"><div><h1 className="page-title">客户分组与折扣</h1><p className="mt-2 text-sm text-muted-foreground">将商品归入商品分组后，可为不同客户组设置独立折扣。</p></div><CustomerGroupTabs active="products"/><div className="flex flex-wrap items-center justify-between gap-3"><PermissionGate permission="customer_groups.manage"><Button size="sm" onClick={() => { setError(""); setForm({ name: "", product_ids: [] }); }}><Plus />添加商品分组</Button></PermissionGate><div className="flex items-center gap-2"><div className="relative"><Input value={query} onChange={e => { setQuery(e.target.value); setPage(0); }} placeholder="搜索商品分组" aria-label="搜索商品分组" className="h-8 w-52 pr-8 text-xs"/><Search size={13} className="absolute right-2.5 top-1/2 -translate-y-1/2 text-muted-foreground"/></div><Button variant="outline" size="icon" title="刷新" aria-label="刷新" onClick={resource.refresh}><RefreshCw size={14}/></Button></div></div>{error && !form && <p role="alert" className="text-sm text-destructive">{error}</p>}{resource.error ? <Failure error={resource.error} retry={resource.refresh}/> : !resource.data ? <Loading/> : <><div className="overflow-x-auto border-y"><table className="w-full text-sm"><thead className="bg-[#f5f7fa]"><tr><th className="px-3 py-3 text-left">商品分组</th><th className="px-3 py-3 text-left">商品</th><th className="px-3 py-3 text-left">版本</th><th className="px-3 py-3 text-right">操作</th></tr></thead><tbody>{resource.data.items.length === 0 ? <tr><td colSpan={4} className="py-14 text-center text-muted-foreground">暂无商品分组</td></tr> : resource.data.items.map(item => <tr key={item.id} className="border-t"><td className="px-3 py-3 font-medium">{item.name}<div className="text-xs text-muted-foreground">#{item.id}</div></td><td className="px-3 py-3"><span className="text-xs text-muted-foreground">{item.product_count} 个商品</span><div className="mt-1 flex flex-wrap gap-1">{item.products.slice(0, 8).map(product => <span key={product.id} className="rounded bg-muted px-1.5 py-0.5 text-xs">{product.name}</span>)}{item.products.length > 8 && <span className="text-xs text-muted-foreground">+{item.products.length - 8}</span>}</div></td><td className="px-3 py-3 text-xs text-muted-foreground">v{item.version}</td><td className="px-3 py-3 text-right"><PermissionGate permission="customer_groups.manage"><Button size="icon-sm" variant="ghost" title="编辑" aria-label={`编辑${item.name}`} onClick={() => { setError(""); setForm({ id: item.id, name: item.name, product_ids: item.product_ids, version: item.version }); }}><Pencil size={14}/></Button><Button size="icon-sm" variant="ghost" title="删除" aria-label={`删除${item.name}`} onClick={() => void remove(item)}><Trash2 size={14}/></Button></PermissionGate></td></tr>)}</tbody></table></div><AdminPagination total={resource.data.total} page={page} pageSize={pageSize} onPageChange={setPage} onPageSizeChange={size => { setPageSize(size); setPage(0); }}/></>}</div><Dialog open={form !== null} onOpenChange={open => { if (!open && !busy) setForm(null); }}><DialogContent className="max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>{form?.id ? "编辑商品分组" : "添加商品分组"}</DialogTitle><DialogDescription>选择真实商品加入分组，保存后可在折扣设置中配置客户组价格。</DialogDescription></DialogHeader>{form && <ProductGroupForm form={form} setForm={setForm} manage={manage} busy={busy} setBusy={setBusy} onSaved={() => { setForm(null); resource.refresh(); }} onCancel={() => setForm(null)}/>}</DialogContent></Dialog></Workspace>;
}

function ProductGroupForm({ form, setForm, manage, busy, setBusy, onSaved, onCancel }: { form: Form; setForm: (value: Form) => void; manage: boolean; busy: boolean; setBusy: (value: boolean) => void; onSaved: () => void; onCancel: () => void }) {
  const products = useResource<ProductPage>("/admin/products/page?active=true&limit=100&offset=0", false);
  const [productQuery, setProductQuery] = useState("");
  const [formError, setFormError] = useState("");
  async function save(event: React.FormEvent) {
    event.preventDefault(); if (!manage || busy) return; setBusy(true); setFormError("");
    try {
      const body = { name: form.name, product_ids: form.product_ids, ...(form.id ? { expected_version: form.version } : {}) };
      await api(form.id ? `/admin/customer-product-groups/${form.id}` : "/admin/customer-product-groups", { method: form.id ? "PATCH" : "POST", headers: { "Idempotency-Key": `customer-product-group-${form.id ? `update-${form.id}-${form.version}` : "create"}-${crypto.randomUUID()}` }, body: JSON.stringify(body) });
      onSaved();
    } catch (e) { setFormError((e as Error).message); } finally { setBusy(false); }
  }
  const available = (products.data?.items ?? []).filter(product => !productQuery.trim() || product.name.toLowerCase().includes(productQuery.toLowerCase()) || product.slug.toLowerCase().includes(productQuery.toLowerCase()));
  return <form onSubmit={save} className="space-y-4"><fieldset disabled={busy || !manage} className="space-y-4"><label className="grid gap-2 text-sm">分组名称<Input required maxLength={120} value={form.name} onChange={e => setForm({ ...form, name: e.target.value })}/></label><div className="space-y-2"><div className="flex items-center justify-between gap-2"><label className="text-sm">选择商品</label><span className="text-xs text-muted-foreground">已选 {form.product_ids.length} 个</span></div><Input placeholder="搜索商品名称或标识" aria-label="搜索商品" value={productQuery} onChange={e => setProductQuery(e.target.value)}/>{products.error ? <Failure error={products.error} retry={products.refresh}/> : !products.data ? <Loading/> : <div className="max-h-64 overflow-y-auto rounded border">{available.length === 0 ? <p className="p-5 text-center text-xs text-muted-foreground">暂无匹配商品</p> : available.map(product => { const checked = form.product_ids.includes(product.id); return <label key={product.id} className="flex items-center gap-3 border-b px-3 py-2 last:border-b-0 text-sm"><input type="checkbox" checked={checked} onChange={e => setForm({ ...form, product_ids: e.target.checked ? [...form.product_ids, product.id] : form.product_ids.filter(id => id !== product.id) })}/><span>{product.name}<span className="ml-2 text-xs text-muted-foreground">{product.slug}</span></span></label>; })}</div>}</div></fieldset>{formError && <p role="alert" className="text-sm text-destructive break-all">{formError}</p>}<div className="flex justify-end gap-2 border-t pt-4"><Button type="button" variant="outline" onClick={onCancel}>取消</Button><Button disabled={busy}><Save/>{busy ? "保存中" : "保存"}</Button></div></form>;
}

type DiscountPage = { items: CustomerProductDiscount[]; total: number; limit: number; offset: number };
export function CustomerProductDiscountsView() {
  const [query, setQuery] = useState(""); const [page, setPage] = useState(0); const [pageSize, setPageSize] = useState(25);
  const resource = useResource<DiscountPage>(`/admin/customer-product-discounts?q=${encodeURIComponent(query)}&limit=${pageSize}&offset=${page * pageSize}`, false);
  const groups = useResource<CustomerGroup[]>("/admin/customer-groups?options=true", false);
  const productGroups = useResource<GroupPage>("/admin/customer-product-groups?limit=100&offset=0", false);
  const manage = useAdminPermission("customer_groups.manage");
  const [editing, setEditing] = useState<CustomerProductDiscount | null>(null); const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  async function save() {
    if (!editing || !manage || busy) return; setBusy(true); setError("");
    try { await api("/admin/customer-product-discounts", { method: "PUT", headers: { "Idempotency-Key": `customer-product-discount-${editing.customer_group_id}-${editing.product_group_id}-${editing.version}-${crypto.randomUUID()}` }, body: JSON.stringify({ customer_group_id: editing.customer_group_id, product_group_id: editing.product_group_id, discount_type: editing.discount_type, discount_percent: editing.discount_type === "percent" ? editing.discount_percent : 0, fixed_discount_cents: editing.discount_type === "fixed" ? editing.fixed_discount_cents : 0, expected_version: editing.version }) }); setEditing(null); resource.refresh(); } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  return <Workspace title="折扣设置" admin><div className="bg-white rounded border border-[#ebeef5] shadow-sm p-4 sm:p-5 space-y-5"><div><h1 className="page-title">客户分组与折扣</h1><p className="mt-2 text-sm text-muted-foreground">商品分组专属规则优先于客户组默认减免，包含商品、配置和初装费；命中多条取实际减免最多的一条。百分比表示减免比例（20% 即减去 20%），固定金额按每件服务减免，最多减至零元。</p></div><p className="text-xs text-muted-foreground">未设置表示移除专属规则并使用客户组默认减免。新服务保存分组折后续费基价，已有服务不重算；优惠码仅在明确允许当前客户组时叠加。</p><CustomerGroupTabs active="discounts"/><div className="flex items-center justify-between gap-3"><Input value={query} onChange={e => { setQuery(e.target.value); setPage(0); }} placeholder="搜索客户组或商品分组" aria-label="搜索折扣" className="max-w-sm"/><Button variant="outline" size="icon" title="刷新" aria-label="刷新" onClick={resource.refresh}><RefreshCw size={14}/></Button></div>{error && !editing && <p role="alert" className="text-sm text-destructive">{error}</p>}{resource.error || groups.error || productGroups.error ? <Failure error={resource.error ?? groups.error ?? productGroups.error!} retry={() => { resource.refresh(); groups.refresh(); productGroups.refresh(); }}/> : !resource.data || !groups.data || !productGroups.data ? <Loading/> : <><div className="overflow-x-auto border-y"><table className="w-full text-sm"><thead className="bg-[#f5f7fa]"><tr><th className="px-3 py-3 text-left">商品分组</th><th className="px-3 py-3 text-left">客户组</th><th className="px-3 py-3 text-left">折扣</th><th className="px-3 py-3 text-right">操作</th></tr></thead><tbody>{resource.data.items.length === 0 ? <tr><td colSpan={4} className="py-14 text-center text-muted-foreground">暂无折扣配置</td></tr> : resource.data.items.map(item => <tr key={`${item.product_group_id}-${item.customer_group_id}`} className="border-t"><td className="px-3 py-3">{item.product_group_name}</td><td className="px-3 py-3">{item.customer_group_name}</td><td className="px-3 py-3">{item.discount_type === "none" ? "未设置" : item.discount_type === "percent" ? `减免 ${item.discount_percent}%` : `每件减免 ${item.fixed_discount_cents} 分`}</td><td className="px-3 py-3 text-right"><PermissionGate permission="customer_groups.manage"><Button size="sm" variant="outline" onClick={() => setEditing({ ...item })}>{item.discount_type === "none" ? "设置" : "编辑"}</Button></PermissionGate></td></tr>)}</tbody></table></div><AdminPagination total={resource.data.total} page={page} pageSize={pageSize} onPageChange={setPage} onPageSizeChange={size => { setPageSize(size); setPage(0); }}/></>}</div><Dialog open={editing !== null} onOpenChange={open => { if (!open && !busy) setEditing(null); }}><DialogContent><DialogHeader><DialogTitle>编辑折扣</DialogTitle><DialogDescription>{editing?.product_group_name} · {editing?.customer_group_name}</DialogDescription></DialogHeader>{editing && <div className="space-y-4"><fieldset disabled={busy || !manage} className="space-y-3"><label className="grid gap-2 text-sm">折扣类型<select className="h-9 rounded border px-3" value={editing.discount_type} onChange={e => setEditing({ ...editing, discount_type: e.target.value as CustomerProductDiscount["discount_type"] })}><option value="none">未设置（使用客户组默认）</option><option value="percent">百分比</option><option value="fixed">固定金额（分）</option></select></label>{editing.discount_type === "percent" && <label className="grid gap-2 text-sm">减免百分比（%）<Input type="number" min={1} max={100} value={editing.discount_percent} onChange={e => setEditing({ ...editing, discount_percent: Number(e.target.value), fixed_discount_cents: 0 })}/></label>}{editing.discount_type === "fixed" && <label className="grid gap-2 text-sm">每件固定减免（分）<Input type="number" min={1} value={editing.fixed_discount_cents} onChange={e => setEditing({ ...editing, fixed_discount_cents: Number(e.target.value), discount_percent: 0 })}/></label>}</fieldset>{error && <p role="alert" className="text-sm text-destructive">{error}</p>}<div className="flex justify-end gap-2 border-t pt-4"><Button variant="outline" onClick={() => setEditing(null)}>取消</Button><Button onClick={() => void save()} disabled={busy}>{busy ? "保存中" : "保存"}</Button></div></div>}</DialogContent></Dialog></Workspace>;
}
