"use client";

import Link from "next/link";
import { Pencil, Plus, RefreshCw, Save, Trash2 } from "lucide-react";
import { useState } from "react";
import { api, date, money } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { AdminPagination } from "./admin-pagination";
import { Failure, Loading, Status } from "./common";
import { Workspace, PermissionGate, useAdminPermission } from "./workspace";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "./ui/dialog";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";

type Product = { id: number; name: string };
type Page<T> = { items: T[]; total: number; limit: number; offset: number };
type TrafficProduct = { id: number; name: string; capacity_gb: number; price_cents: number; active: boolean; stock: number | null; sold_count: number; version: number; created_at: string; applicable_product_ids: number[]; applicable_products: Product[] };
type ProductForm = { id?: number; name: string; capacity_gb: number; price_cents: number; active: boolean; stock: string; applicable_product_ids: number[]; version?: number };
type TrafficOrder = { id: number; order_no: string; customer_name: string; product_name: string; capacity_gb: number; amount_cents: number; status: string; payment_method: string; fulfillment_status: string; fulfillment_note?: string; service_domain?: string; version: number; created_at: string };

const idempotencyKey = (prefix: string) => `${prefix}-${crypto.randomUUID()}`;
const emptyProduct = (): ProductForm => ({ name: "", capacity_gb: 1, price_cents: 1, active: true, stock: "", applicable_product_ids: [] });
const orderStates: Record<string, string> = { pending: "待支付", paid: "已支付", cancelled: "已取消", refunded: "已退款" };
const fulfillmentNames: Record<string,string> = {pending_payment:"等待付款",waiting_manual:"待人工交付",fulfilled:"已核验交付",cancelled:"已取消"};

export function AdminTrafficProductsView() {
  const [query, setQuery] = useState({ q: "", active: "", offset: 0, limit: 25 });
  const resource = useResource<Page<TrafficProduct>>(`/admin/traffic-products?q=${encodeURIComponent(query.q)}&active=${query.active}&limit=${query.limit}&offset=${query.offset}`, false);
  const [productQuery,setProductQuery]=useState({q:"",offset:0});
  const products = useResource<Page<Product>>(`/admin/products/page?q=${encodeURIComponent(productQuery.q)}&limit=100&offset=${productQuery.offset}`, false);
  const manage = useAdminPermission("products.manage");
  const [form, setForm] = useState<ProductForm | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const productItems = products.data?.items ?? [];
  const change = (patch: Partial<typeof query>) => setQuery(current => ({ ...current, ...patch, ...(patch.offset === undefined ? { offset: 0 } : {}) }));

  function edit(item: TrafficProduct) {
    setMessage("");
    setForm({ id: item.id, name: item.name, capacity_gb: item.capacity_gb, price_cents: item.price_cents, active: item.active, stock: item.stock == null ? "" : String(item.stock), applicable_product_ids: item.applicable_product_ids ?? [], version: item.version });
  }
  async function save(event: React.FormEvent) {
    event.preventDefault(); if (!form || busy || !manage) return;
    setBusy(true); setMessage("");
    try {
      const body = { name: form.name, capacity_gb: Number(form.capacity_gb), price_cents: Number(form.price_cents), active: form.active, stock: form.stock.trim() === "" ? null : Number(form.stock), applicable_product_ids: form.applicable_product_ids, ...(form.id ? { expected_version: form.version } : {}) };
      await api(form.id ? `/admin/traffic-products/${form.id}` : "/admin/traffic-products", { method: form.id ? "PATCH" : "POST", headers: { "Idempotency-Key": idempotencyKey("traffic-product") }, body: JSON.stringify(body) });
      setForm(null); resource.refresh();
    } catch (error) { setMessage((error as Error).message); } finally { setBusy(false); }
  }
  async function remove(item: TrafficProduct) {
    if (!manage || busy || !window.confirm(`确认删除流量包“${item.name}”吗？有订单的流量包只能停用。`)) return;
    setBusy(true); setMessage("");
    try { await api(`/admin/traffic-products/${item.id}`, { method: "DELETE", headers: { "Idempotency-Key": idempotencyKey("traffic-product-delete") }, body: JSON.stringify({ expected_version: item.version }) }); resource.refresh(); }
    catch (error) { setMessage((error as Error).message); } finally { setBusy(false); }
  }
  function toggleProduct(productID: number) { setForm(current => current ? { ...current, applicable_product_ids: current.applicable_product_ids.includes(productID) ? current.applicable_product_ids.filter(id => id !== productID) : [...current.applicable_product_ids, productID] } : current); }

  return <Workspace title="流量包管理" admin>
    <div className="min-w-0 max-w-full space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3"><div><h1 className="page-title">流量包管理</h1><p className="mt-2 text-sm text-muted-foreground">为已关联流量限制的产品配置可购买流量包；库存和销量来自真实购买记录。</p></div><div className="flex gap-2"><Button variant="outline" size="icon" title="刷新" aria-label="刷新" onClick={resource.refresh}><RefreshCw /></Button><PermissionGate permission="products.manage"><Button onClick={() => { setMessage(""); setForm(emptyProduct()); }}><Plus />新增流量包</Button></PermissionGate></div></div>
      <div className="flex flex-wrap gap-3 border-y py-4"><Input aria-label="搜索流量包" placeholder="按名称搜索" value={query.q} onChange={event => change({ q: event.target.value })} /><select aria-label="启用状态" value={query.active} onChange={event => change({ active: event.target.value })}><option value="">全部状态</option><option value="true">启用</option><option value="false">停用</option></select></div>
      {message && !form && <p role="alert" className="text-sm text-destructive">{message}</p>}
      {resource.error || products.error ? <Failure error={resource.error ?? products.error!} retry={() => { resource.refresh(); products.refresh(); }} /> : !resource.data || !products.data ? <Loading /> : <>
        <div className="min-w-0 overflow-x-auto"><Table className="min-w-[1050px]"><TableHeader><TableRow>{["ID", "流量包名称", "流量（GB）", "价格", "启用", "已售/库存", "适用产品", "创建时间", "管理"].map(label => <TableHead key={label}>{label}</TableHead>)}</TableRow></TableHeader><TableBody>
          {resource.data.items.map(item => <TableRow key={item.id}><TableCell className="font-mono text-xs">{item.id}</TableCell><TableCell className="font-medium">{item.name}</TableCell><TableCell>{item.capacity_gb}</TableCell><TableCell className="money">{money(item.price_cents)}</TableCell><TableCell><Status value={item.active ? "active" : "disabled"} /></TableCell><TableCell>{item.sold_count} / {item.stock == null ? "不限" : item.stock}</TableCell><TableCell className="max-w-xs whitespace-normal">{(item.applicable_products ?? []).map(product => product.name).join("、") || "未限制产品"}</TableCell><TableCell className="whitespace-nowrap">{date(item.created_at)}</TableCell><TableCell><div className="flex gap-1"><PermissionGate permission="products.manage"><Button size="icon-sm" variant="ghost" title="编辑" aria-label={`编辑${item.name}`} onClick={() => edit(item)}><Pencil /></Button><Button size="icon-sm" variant="ghost" title="删除" aria-label={`删除${item.name}`} onClick={() => void remove(item)}><Trash2 /></Button></PermissionGate></div></TableCell></TableRow>)}
          {resource.data.items.length === 0 && <TableRow><TableCell colSpan={9} className="py-14 text-center text-muted-foreground">暂无流量包</TableCell></TableRow>}
        </TableBody></Table></div>
        <AdminPagination total={resource.data.total} page={Math.floor(resource.data.offset / resource.data.limit)} pageSize={resource.data.limit} onPageChange={page => change({ offset: page * resource.data!.limit })} onPageSizeChange={limit => change({ limit, offset: 0 })} />
      </>}
    </div>
    <Dialog open={form !== null} onOpenChange={open => { if (!open && !busy) setForm(null); }}><DialogContent className="max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>{form?.id ? "编辑流量包" : "新增流量包"}</DialogTitle><DialogDescription>保存会产生版本，已有订单的流量包不能删除，只能停用。</DialogDescription></DialogHeader>{form && <form onSubmit={save} className="space-y-4"><fieldset disabled={busy || !manage} className="space-y-4"><label className="space-y-2 text-sm"><Label>流量包名称</Label><Input required maxLength={120} value={form.name} onChange={event => setForm({ ...form, name: event.target.value })} /></label><div className="grid sm:grid-cols-2 gap-4"><label className="space-y-2 text-sm"><Label>流量（GB）</Label><Input required type="number" min={1} value={form.capacity_gb} onChange={event => setForm({ ...form, capacity_gb: Number(event.target.value) || 0 })} /></label><label className="space-y-2 text-sm"><Label>价格（分）</Label><Input required type="number" min={1} value={form.price_cents} onChange={event => setForm({ ...form, price_cents: Number(event.target.value) || 0 })} /></label></div><label className="space-y-2 text-sm"><Label>库存（留空表示不限）</Label><Input type="number" min={0} value={form.stock} onChange={event => setForm({ ...form, stock: event.target.value })} /></label><label className="flex items-center gap-2 text-sm"><Checkbox checked={form.active} onCheckedChange={checked => setForm({ ...form, active: checked === true })} />启用销售</label><fieldset className="space-y-2"><legend className="text-sm font-medium">适用产品（不选择表示不限制）</legend><Input aria-label="搜索适用产品" value={productQuery.q} onChange={e=>setProductQuery({q:e.target.value,offset:0})} placeholder="按商品名称搜索"/><p className="text-xs text-muted-foreground">已选择 {form.applicable_product_ids.length} 项，切换搜索保留已选关联。</p><div className="grid gap-2 max-h-44 overflow-y-auto border p-3">{productItems.map(product => <label key={product.id} className="flex items-center gap-2 text-sm"><Checkbox checked={form.applicable_product_ids.includes(product.id)} onCheckedChange={() => toggleProduct(product.id)} />{product.name}</label>)}{productItems.length === 0 && <p className="text-sm text-muted-foreground">暂无可关联产品</p>}</div><div className="flex gap-2"><Button type="button" variant="outline" disabled={productQuery.offset===0} onClick={()=>setProductQuery(v=>({...v,offset:Math.max(0,v.offset-100)}))}>上一页商品</Button><Button type="button" variant="outline" disabled={!products.data||productQuery.offset+100>=products.data.total} onClick={()=>setProductQuery(v=>({...v,offset:v.offset+100}))}>下一页商品</Button></div></fieldset>{message && <p role="alert" className="text-sm text-destructive">{message}</p>}<div className="flex flex-wrap gap-2 border-t pt-4"><Button><Save />{busy ? "保存中" : "保存更改"}</Button><Button type="button" variant="outline" onClick={() => setForm(null)}>取消更改</Button></div></fieldset></form>}</DialogContent></Dialog>
  </Workspace>;
}

export function AdminTrafficOrdersView() {
  const [query, setQuery] = useState({ q: "", status: "", offset: 0, limit: 25 });
  const resource = useResource<Page<TrafficOrder>>(`/admin/traffic-orders?q=${encodeURIComponent(query.q)}&status=${query.status}&limit=${query.limit}&offset=${query.offset}`, false);
  const manage = useAdminPermission("orders.manage");
  const [message, setMessage] = useState("");
  const change = (patch: Partial<typeof query>) => setQuery(current => ({ ...current, ...patch, ...(patch.offset === undefined ? { offset: 0 } : {}) }));
  async function cancel(item: TrafficOrder) {
    if (!manage || !window.confirm(`确认取消流量包订单 ${item.order_no} 吗？`)) return;
    setMessage("");
    try { await api(`/admin/traffic-orders/${item.id}/cancel`, { method: "POST", headers: { "Idempotency-Key": idempotencyKey("traffic-order-cancel") }, body: JSON.stringify({ expected_version: item.version }) }); resource.refresh(); } catch (error) { setMessage((error as Error).message); }
  }
  return <Workspace title="流量包订单" admin><div className="min-w-0 max-w-full space-y-5"><div className="flex flex-wrap items-start justify-between gap-3"><div><h1 className="page-title">流量包订单</h1><p className="mt-2 text-sm text-muted-foreground">查看客户购买流量包的真实记录；没有购买记录时显示真实空态。</p></div><Button variant="outline" size="icon" title="刷新" aria-label="刷新" onClick={resource.refresh}><RefreshCw /></Button></div><div className="flex flex-wrap gap-3 border-y py-4"><Input aria-label="搜索流量包订单" placeholder="订单号、客户或产品" value={query.q} onChange={event => change({ q: event.target.value })} /><select aria-label="订单状态" value={query.status} onChange={event => change({ status: event.target.value })}><option value="">全部状态</option>{Object.entries(orderStates).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></div>{message && <p role="alert" className="text-sm text-destructive">{message}</p>}{resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <><div className="min-w-0 overflow-x-auto"><Table className="min-w-[1000px]"><TableHeader><TableRow>{["ID", "客户名", "产品", "时间", "金额", "付款状态", "支付方式", "服务", "操作"].map(label => <TableHead key={label}>{label}</TableHead>)}</TableRow></TableHeader><TableBody>{resource.data.items.map(item => <TableRow key={item.id}><TableCell className="font-mono text-xs"><Link className="text-primary hover:underline" href={`/admin/traffic-orders/${item.id}`}>{item.order_no}</Link></TableCell><TableCell>{item.customer_name}</TableCell><TableCell>{item.product_name}<div className="text-xs text-muted-foreground">{item.capacity_gb} GB</div></TableCell><TableCell className="whitespace-nowrap">{date(item.created_at)}</TableCell><TableCell className="money">{money(item.amount_cents)}</TableCell><TableCell><Status value={item.status} /></TableCell><TableCell>{item.payment_method || "-"}</TableCell><TableCell className="font-mono text-xs">{item.service_domain || "-"}</TableCell><TableCell>{item.status === "pending" && <PermissionGate permission="orders.manage"><Button size="sm" variant="destructive" onClick={() => void cancel(item)}>取消</Button></PermissionGate>}</TableCell></TableRow>)}{resource.data.items.length === 0 && <TableRow><TableCell colSpan={9} className="py-14 text-center text-muted-foreground">暂无流量包订单</TableCell></TableRow>}</TableBody></Table></div><AdminPagination total={resource.data.total} page={Math.floor(resource.data.offset / resource.data.limit)} pageSize={resource.data.limit} onPageChange={page => change({ offset: page * resource.data!.limit })} onPageSizeChange={limit => change({ limit, offset: 0 })} /></>}</div></Workspace>;
}

export function AdminTrafficOrderDetailView({ id }: { id: number }) {
  const resource = useResource<TrafficOrder>(`/admin/traffic-orders/${id}`, false);
  const manage = useAdminPermission("services.manage");
  const [note,setNote]=useState(""),[message,setMessage]=useState(""),[busy,setBusy]=useState(false);
  async function verify(event:React.FormEvent) {event.preventDefault();if(!resource.data||busy||!manage||!window.confirm("确认已在资源端实际增加流量并完成核验？此操作只记录核验，不会调用上游或虚增资源额度。"))return;setBusy(true);setMessage("");try{await api(`/admin/traffic-orders/${id}/delivery-verification`,{method:"POST",headers:{"Idempotency-Key":idempotencyKey("traffic-delivery")},body:JSON.stringify({expected_version:resource.data.version,note,acknowledged:true})});resource.refresh();}catch(e){setMessage((e as Error).message);}finally{setBusy(false);}}
  if(resource.data)return <Workspace title="流量包订单详情" admin><Button variant="ghost" className="mb-4" asChild><Link href="/admin/traffic-orders">返回流量包订单</Link></Button><h1 className="page-title">{resource.data.order_no}</h1><p className="mt-2">{resource.data.customer_name} · {resource.data.product_name} · {resource.data.capacity_gb} GB · {money(resource.data.amount_cents)}</p><div className="flex gap-3 mt-4"><Status value={resource.data.status}/><span>{fulfillmentNames[resource.data.fulfillment_status]??resource.data.fulfillment_status}</span></div><p className="mt-3 text-sm text-muted-foreground">{resource.data.fulfillment_note||"付款后需在资源端真实交付，再由管理员记录核验结果。"}</p>{resource.data.status==="paid"&&resource.data.fulfillment_status==="waiting_manual"&&manage&&<form onSubmit={verify} className="max-w-xl space-y-3 mt-6"><Label>真实交付核验说明</Label><Input required minLength={5} maxLength={2000} value={note} onChange={e=>setNote(e.target.value)} placeholder="填写资源端核验方式和结果"/><Button disabled={busy}>确认实际交付完成</Button></form>}{message&&<p role="alert" className="text-destructive mt-3">{message}</p>}</Workspace>;
  return <Workspace title="流量包订单详情" admin><Button variant="ghost" className="mb-4" asChild><Link href="/admin/traffic-orders">返回流量包订单</Link></Button>{resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : <Loading />}</Workspace>;
}
