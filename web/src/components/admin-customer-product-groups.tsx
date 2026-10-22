"use client";

import Link from "next/link";
import { useState } from "react";
import { Pencil, Plus, RefreshCw, Save, Trash2 } from "lucide-react";
import { api, type CustomerProductDiscount, type CustomerProductGroup, type ProductPage } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { AdminPagination } from "./admin-pagination";
import { AdminListPage, AdminListRegion } from "./admin-list-page";
import { ResourceTable, type ResourceColumn } from "./resource-table";
import { Badge } from "./ui/badge";
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
  return <nav aria-label="客户分组设置" className="flex flex-wrap gap-2">{tabs.map(([id, label, href]) => <Button asChild key={id} size="sm" variant={active === id ? "default" : "outline"}><Link href={href} aria-current={active === id ? "page" : undefined}>{label}</Link></Button>)}</nav>;
}

type GroupPage = { items: CustomerProductGroup[]; total: number; limit: number; offset: number };
type Form = { id?: number; name: string; product_ids: number[]; version?: number };

export function CustomerProductGroupsView() {
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState(25);
  const allowed = useAdminPermission("customer_groups.view");
  const resource = useResource<GroupPage>(allowed ? `/admin/customer-product-groups?q=${encodeURIComponent(query)}&limit=${pageSize}&offset=${page * pageSize}` : null, false);
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

  const identity = (item: CustomerProductGroup) => <div className="space-y-1 break-all"><p className="font-medium">{item.name}</p><p className="text-xs text-muted-foreground">#{item.id} · 版本 {item.version}</p></div>;
  const products = (item: CustomerProductGroup) => <div className="space-y-2"><p className="text-xs text-muted-foreground">{item.product_count} 个商品</p><div className="flex flex-wrap gap-1">{item.products.slice(0, 8).map(product => <Badge variant="outline" key={product.id} className="max-w-full whitespace-normal break-all">{product.name}</Badge>)}{item.products.length > 8 && <span className="text-xs text-muted-foreground">另 {item.products.length - 8} 个</span>}</div></div>;
  const actions = (item: CustomerProductGroup) => <PermissionGate permission="customer_groups.manage"><div className="flex gap-1"><Button size="icon-sm" variant="ghost" disabled={busy} title="编辑" aria-label={`编辑${item.name}`} onClick={() => { setError(""); setForm({ id: item.id, name: item.name, product_ids: item.product_ids, version: item.version }); }}><Pencil /></Button><Button size="icon-sm" variant="ghost" disabled={busy} title="删除" aria-label={`删除${item.name}`} onClick={() => void remove(item)}><Trash2 /></Button></div></PermissionGate>;
  const columns: ResourceColumn<CustomerProductGroup>[] = [
    { id: "name", header: "商品分组", render: identity, className: "max-w-72 whitespace-normal" },
    { id: "products", header: "关联商品", render: products, className: "max-w-xl whitespace-normal" },
    { id: "actions", header: "操作", render: actions },
  ];
  return <Workspace title="商品分组" admin><AdminListPage title="商品分组" description="将商品加入定价分组，为不同客户组配置专属减免；与购买目录和已购服务导航分组独立。"
    actions={<><Button variant="outline" disabled={resource.isLoading} onClick={resource.refresh}><RefreshCw />刷新</Button><PermissionGate permission="customer_groups.manage"><Button onClick={() => { setError(""); setForm({ name: "", product_ids: [] }); }}><Plus />添加商品分组</Button></PermissionGate></>}
    filters={<div className="space-y-4"><CustomerGroupTabs active="products" /><div className="rounded border border-border bg-card p-4"><Input value={query} onChange={e => { setQuery(e.target.value); setPage(0); }} placeholder="搜索商品分组" aria-label="搜索商品分组" className="w-full sm:max-w-sm" /></div></div>}
    feedback={error && !form && <p role="alert" className="break-all text-sm text-destructive">{error}</p>}>
    <AdminListRegion title="商品分组列表" allowed={allowed} resource={resource} retry={resource.refresh} summary={resource.data && `共 ${resource.data.total} 个分组`}
      pagination={data => <AdminPagination total={data.total} page={page} pageSize={pageSize} onPageChange={setPage} onPageSizeChange={size => { setPageSize(size); setPage(0); }} />}>
      {data => <ResourceTable items={data.items} rowKey={item => item.id} columns={columns} ariaLabel="商品分组列表" className="min-w-0" empty="暂无商品分组"
        renderMobile={item => <article className="space-y-4 p-4">{identity(item)}{products(item)}{actions(item)}</article>} />}
    </AdminListRegion>
  </AdminListPage><Dialog open={form !== null} onOpenChange={open => { if (!open && !busy) setForm(null); }}><DialogContent className="max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>{form?.id ? "编辑商品分组" : "添加商品分组"}</DialogTitle><DialogDescription>选择真实商品加入分组，保存后可在折扣设置中配置客户组价格。</DialogDescription></DialogHeader>{form && <ProductGroupForm form={form} setForm={setForm} manage={manage} busy={busy} setBusy={setBusy} onSaved={() => { setForm(null); resource.refresh(); }} onCancel={() => setForm(null)}/>}</DialogContent></Dialog></Workspace>;
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
  return <form onSubmit={save} className="space-y-4"><fieldset disabled={busy || !manage} className="space-y-4"><label className="grid gap-2 text-sm">分组名称<Input required maxLength={120} value={form.name} onChange={e => setForm({ ...form, name: e.target.value })}/></label><div className="space-y-2"><div className="flex items-center justify-between gap-2"><label className="text-sm">选择商品</label><span className="text-xs text-muted-foreground">已选 {form.product_ids.length} 个</span></div><Input placeholder="搜索商品名称或标识" aria-label="搜索商品" value={productQuery} onChange={e => setProductQuery(e.target.value)}/>{products.error ? <Failure error={products.error} retry={products.refresh}/> : !products.data ? <Loading/> : <div className="max-h-64 overflow-y-auto rounded border">{available.length === 0 ? <p className="p-5 text-center text-xs text-muted-foreground">暂无匹配商品</p> : available.map(product => { const checked = form.product_ids.includes(product.id); return <label key={product.id} className="flex items-center gap-3 border-b px-3 py-2 last:border-b-0 text-sm"><input type="checkbox" checked={checked} onChange={e => setForm({ ...form, product_ids: e.target.checked ? [...form.product_ids, product.id] : form.product_ids.filter(id => id !== product.id) })}/><span>{product.name}<span className="ml-2 text-xs text-muted-foreground">{product.slug}</span></span></label>; })}</div>}</div></fieldset>{formError && <p role="alert" className="text-sm text-destructive break-all">{formError}</p>}<div className="flex justify-end gap-2 border-t pt-4"><Button type="button" variant="outline" disabled={busy} onClick={onCancel}>取消</Button><Button disabled={busy || !manage}><Save/>{busy ? "保存中" : "保存"}</Button></div></form>;
}

type DiscountPage = { items: CustomerProductDiscount[]; total: number; limit: number; offset: number };
export function CustomerProductDiscountsView() {
  const [query, setQuery] = useState(""); const [page, setPage] = useState(0); const [pageSize, setPageSize] = useState(25);
  const allowed = useAdminPermission("customer_groups.view");
  const resource = useResource<DiscountPage>(allowed ? `/admin/customer-product-discounts?q=${encodeURIComponent(query)}&limit=${pageSize}&offset=${page * pageSize}` : null, false);
  const manage = useAdminPermission("customer_groups.manage");
  const [editing, setEditing] = useState<CustomerProductDiscount | null>(null); const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  async function save() {
    if (!editing || !manage || busy) return; setBusy(true); setError("");
    try { await api("/admin/customer-product-discounts", { method: "PUT", headers: { "Idempotency-Key": `customer-product-discount-${editing.customer_group_id}-${editing.product_group_id}-${editing.version}-${crypto.randomUUID()}` }, body: JSON.stringify({ customer_group_id: editing.customer_group_id, product_group_id: editing.product_group_id, discount_type: editing.discount_type, discount_percent: editing.discount_type === "percent" ? editing.discount_percent : 0, fixed_discount_cents: editing.discount_type === "fixed" ? editing.fixed_discount_cents : 0, expected_version: editing.version }) }); setEditing(null); resource.refresh(); } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  const discount = (item: CustomerProductDiscount) => item.discount_type === "none" ? "未设置（使用客户组默认）" : item.discount_type === "percent" ? `减免 ${item.discount_percent}%` : `每件减免 ${item.fixed_discount_cents} 分`;
  const actions = (item: CustomerProductDiscount) => <PermissionGate permission="customer_groups.manage"><Button size="sm" variant="outline" disabled={busy} aria-label={`设置${item.product_group_name}与${item.customer_group_name}的折扣`} onClick={() => { setError(""); setEditing({ ...item }); }}>{item.discount_type === "none" ? "设置" : "编辑"}</Button></PermissionGate>;
  const columns: ResourceColumn<CustomerProductDiscount>[] = [
    { id: "product", header: "商品分组", render: item => item.product_group_name, className: "max-w-72 whitespace-normal break-all" },
    { id: "customer", header: "客户分组", render: item => item.customer_group_name, className: "max-w-72 whitespace-normal break-all" },
    { id: "discount", header: "减免规则", render: discount, className: "max-w-72 whitespace-normal" },
    { id: "actions", header: "操作", render: actions },
  ];
  return <Workspace title="折扣设置" admin><AdminListPage title="折扣设置" description="商品分组规则覆盖客户组默认减免；命中多组时取实际减免最多的一条，固定金额按每件计算且最多减至零元。"
    actions={<Button variant="outline" disabled={resource.isLoading} onClick={resource.refresh}><RefreshCw />刷新</Button>}
    filters={<div className="space-y-4"><CustomerGroupTabs active="discounts" /><div className="rounded border border-border bg-card p-4"><Input value={query} onChange={e => { setQuery(e.target.value); setPage(0); }} placeholder="搜索客户组或商品分组" aria-label="搜索折扣" className="w-full sm:max-w-sm" /></div></div>}
    feedback={<><p className="text-sm text-muted-foreground">百分比表示减免比例：20% 即减去20%；未设置使用客户组默认。新服务保存折后续费基价，已有服务不重算；优惠码仅在明确允许当前客户组时叠加。</p>{error && !editing && <p role="alert" className="break-all text-sm text-destructive">{error}</p>}</>}>
    <AdminListRegion title="客户分组折扣列表" allowed={allowed} resource={resource} retry={resource.refresh} summary={resource.data && `共 ${resource.data.total} 组对应关系`}
      pagination={data => <AdminPagination total={data.total} page={page} pageSize={pageSize} onPageChange={setPage} onPageSizeChange={size => { setPageSize(size); setPage(0); }} />}>
      {data => <ResourceTable items={data.items} rowKey={item => `${item.product_group_id}-${item.customer_group_id}`} columns={columns} ariaLabel="客户分组折扣列表" className="min-w-0" empty="暂无折扣配置"
        renderMobile={item => <article className="space-y-4 p-4"><dl className="grid gap-2 text-sm"><div><dt className="text-xs text-muted-foreground">商品分组</dt><dd className="break-all font-medium">{item.product_group_name}</dd></div><div><dt className="text-xs text-muted-foreground">客户分组</dt><dd className="break-all">{item.customer_group_name}</dd></div><div><dt className="text-xs text-muted-foreground">减免规则</dt><dd>{discount(item)}</dd></div></dl>{actions(item)}</article>} />}
    </AdminListRegion>
  </AdminListPage><Dialog open={editing !== null} onOpenChange={open => { if (!open && !busy) setEditing(null); }}><DialogContent className="max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>编辑折扣</DialogTitle><DialogDescription>{editing?.product_group_name} · {editing?.customer_group_name}</DialogDescription></DialogHeader>{editing && <div className="space-y-4"><fieldset disabled={busy || !manage} className="space-y-3"><label className="grid gap-2 text-sm">折扣类型<select className="h-9 rounded border px-3" value={editing.discount_type} onChange={e => setEditing({ ...editing, discount_type: e.target.value as CustomerProductDiscount["discount_type"] })}><option value="none">未设置（使用客户组默认）</option><option value="percent">百分比</option><option value="fixed">固定金额（分）</option></select></label>{editing.discount_type === "percent" && <label className="grid gap-2 text-sm">减免百分比（%）<Input type="number" min={1} max={100} value={editing.discount_percent} onChange={e => setEditing({ ...editing, discount_percent: Number(e.target.value), fixed_discount_cents: 0 })}/></label>}{editing.discount_type === "fixed" && <label className="grid gap-2 text-sm">每件固定减免（分）<Input type="number" min={1} value={editing.fixed_discount_cents} onChange={e => setEditing({ ...editing, fixed_discount_cents: Number(e.target.value), discount_percent: 0 })}/></label>}</fieldset>{error && <p role="alert" className="text-sm text-destructive">{error}</p>}<div className="flex justify-end gap-2 border-t pt-4"><Button variant="outline" disabled={busy} onClick={() => setEditing(null)}>取消</Button><Button onClick={() => void save()} disabled={busy}>{busy ? "保存中" : "保存"}</Button></div></div>}</DialogContent></Dialog></Workspace>;
}
