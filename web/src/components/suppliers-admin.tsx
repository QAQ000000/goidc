"use client";

import { useRef, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { ArrowLeft, Download, Pencil, Plus, RefreshCw, Save } from "lucide-react";
import { api, money, type CatalogCategory, type SupplierAccount, type SupplierProduct, type SupplierProvider } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace, useAdminPermission } from "./workspace";
import { Failure, Loading, cycles } from "./common";
import { catalogSelectStyle } from "./catalog-admin";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Checkbox } from "./ui/checkbox";
import { Table, TableHeader, TableHead, TableBody, TableRow, TableCell } from "./ui/table";

type AccountDraft = {id:number;name:string;provider:string;base_url:string;username:string;password:string;active:boolean;expected_version:number};
const emptyAccount = (provider:string):AccountDraft => ({id:0,name:"",provider,base_url:"",username:"",password:"",active:true,expected_version:0});
function cents(value:string) {
  if (!/^\d{1,7}(?:\.\d{1,2})?$/.test(value)) throw new Error("金额最多保留两位小数且不能超过 100 万元");
  const result = Math.round(Number(value)*100);
  if (result > 100000000) throw new Error("金额不能超过 100 万元");
  return result;
}
function amount(value:number) { return (value/100).toFixed(2); }
function upstreamCost(product:SupplierProduct, cycle:string, options:Record<string,string>) {
  const base=product.prices?.[cycle];
  if (!base) return 0;
  return base.amount_cents+base.setup_cents+(product.options??[]).reduce((total,option)=>{
    const choice=(option.choices??[]).find(c=>String(c.id)===options[String(option.id)]);
    const price=choice?.prices?.[cycle];
    return total+(price?.amount_cents??0)+(price?.setup_cents??0);
  },0);
}
function unsupportedLabel(reason?:string) {
  return ({detail_required:"需要查看商品配置",unsupported_currency:"仅支持人民币",unsupported_cycle:"不支持所选账期",unsupported_option_type:"暂不支持此规格类型",out_of_stock:"上游库存不足"} as Record<string,string>)[reason??""]??"当前商品配置暂不支持导入";
}

export function SuppliersView({ returnHref }: { returnHref?: string }) {
  return <Workspace title="供应商" admin>{returnHref && <Button type="button" variant="outline" size="sm" className="mb-5" asChild><Link href={returnHref}>返回应用列表</Link></Button>}<SuppliersManager /></Workspace>;
}

function SuppliersManager() {
  const manage = useAdminPermission("products.manage");
  const providers = useResource<SupplierProvider[]>("/admin/supplier-providers", false);
  const accounts = useResource<SupplierAccount[]>("/admin/suppliers", false);
  const [draft,setDraft] = useState<AccountDraft|null>(null);
  const [selected,setSelected] = useState<SupplierAccount|null>(null);
  const [catalog,setCatalog] = useState<SupplierProduct[]|null>(null);
  const [catalogBusy,setCatalogBusy] = useState(false);
  const [productID,setProductID] = useState<number|null>(null);
  const [busy,setBusy] = useState(false);
  const [error,setError] = useState("");
  const [notice,setNotice] = useState("");
  const catalogRequest = useRef(0);
  const [catalogSearch,setCatalogSearch] = useState("");
  const [supportFilter,setSupportFilter] = useState("all");
  const detail = useResource<SupplierProduct>(selected && productID ? `/admin/suppliers/${selected.id}/products/${productID}` : null, false);

  async function saveAccount(event:React.FormEvent) {
    event.preventDefault();
    if (!draft || !manage || busy) return;
    setBusy(true); setError(""); setNotice("");
    try {
      const {id,...body} = draft;
      await api(`/admin/suppliers${id?`/${id}`:""}`, {method:id?"PUT":"POST",body:JSON.stringify(body)});
      setDraft(null); accounts.refresh(); catalogRequest.current++;setCatalogBusy(false);setSelected(null); setCatalog(null); setProductID(null);
      setNotice("供应商已保存");
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  async function loadCatalog(account:SupplierAccount) {
    if (!manage || busy) return;
    const request = ++catalogRequest.current;
    setSelected(account); setCatalog(null); setProductID(null); setCatalogSearch(""); setSupportFilter("all"); setCatalogBusy(true); setError(""); setNotice("");
    try { const products=await api<SupplierProduct[]>(`/admin/suppliers/${account.id}/catalog`,{method:"POST"});if(request===catalogRequest.current){setCatalog(products);setNotice("上游目录已读取");} }
    catch (e) { if(request===catalogRequest.current)setError((e as Error).message); }
    finally { if(request===catalogRequest.current)setCatalogBusy(false); }
  }
  const visibleCatalog = catalog?.filter(product => `${product.name} ${product.id}`.toLowerCase().includes(catalogSearch.toLowerCase()) && (supportFilter === "all" || (supportFilter === "supported" ? product.supported || product.unsupported_reason === "detail_required" : !product.supported && product.unsupported_reason !== "detail_required"))) ?? [];
  const failure = providers.error ?? accounts.error;
  return <div className="space-y-7">
    <div className="flex flex-wrap items-end justify-between gap-3"><div><h1 className="page-title">供应商</h1><p className="text-sm text-muted-foreground mt-2">管理上游账户与本地商品导入</p></div>{manage && providers.data && <Button onClick={()=>{setDraft(emptyAccount(providers.data?.[0]?.id??""));setError("");setNotice("");}} disabled={busy||providers.data.length===0}><Plus />新增供应商</Button>}</div>
    {notice && <p role="status" className="text-sm text-emerald-700">{notice}</p>}
    {error && <p role="alert" className="text-sm text-destructive break-all">{error}</p>}
    {failure ? <Failure error={failure} retry={()=>{providers.refresh();accounts.refresh();}}/> : !providers.data || !accounts.data ? <Loading/> : <>
      {providers.data?.length===0 && <p className="border-y py-8 text-muted-foreground">暂无可用供应商类型</p>}
      <div className="overflow-x-auto border-y"><Table><TableHeader><TableRow><TableHead>供应商</TableHead><TableHead>接口 / 账户</TableHead><TableHead>状态</TableHead><TableHead>本地商品</TableHead><TableHead>版本 / 风险</TableHead><TableHead>操作</TableHead></TableRow></TableHeader><TableBody>{accounts.data.map(account=><TableRow key={account.id}><TableCell className="min-w-40"><p className="font-medium break-words">{account.name}</p><p className="text-xs text-muted-foreground mt-1">{providers.data?.find(p=>p.id===account.provider)?.name??account.provider}</p></TableCell><TableCell className="max-w-xs break-all"><p>{account.base_url}</p><p className="text-xs text-muted-foreground mt-1">{account.username}</p></TableCell><TableCell className="whitespace-nowrap">{account.active?"已启用":"已停用"}<p className="text-xs text-muted-foreground mt-1">{account.credentials_configured?"凭据已配置":"凭据未配置"}</p></TableCell><TableCell>{account.product_count}</TableCell><TableCell>{account.version}{account.blocked_job_id&&<p className="text-xs text-amber-700 mt-1">待核对任务 #{account.blocked_job_id}</p>}</TableCell><TableCell>{manage&&<div className="flex gap-2"><Button variant="outline" size="icon-sm" title="编辑供应商" aria-label={`编辑 ${account.name}`} disabled={busy} onClick={()=>{setDraft({...account,password:"",expected_version:account.version});setError("");setNotice("");}}><Pencil size={14}/></Button><Button variant="outline" size="sm" disabled={busy||catalogBusy||!account.active||!account.credentials_configured} onClick={()=>void loadCatalog(account)}><RefreshCw size={14}/>目录</Button></div>}</TableCell></TableRow>)}{accounts.data.length===0&&<TableRow><TableCell colSpan={6} className="py-10 text-center text-muted-foreground">暂无供应商</TableCell></TableRow>}</TableBody></Table></div>
    </>}
    {draft && providers.data && <section className="border-t pt-5 max-w-4xl"><div className="flex items-center justify-between gap-3 mb-5"><h2 className="text-lg font-semibold">{draft.id?"编辑供应商":"新增供应商"}</h2><Button variant="ghost" disabled={busy} onClick={()=>{setDraft(null);setError("");}}>取消</Button></div><form onSubmit={saveAccount}><fieldset disabled={busy||!manage} className="grid sm:grid-cols-2 gap-4"><h3 className="sm:col-span-2 text-sm font-semibold border-b pb-3">账户与接口</h3><label className="grid gap-2 text-sm">名称<Input required maxLength={120} value={draft.name} onChange={e=>setDraft({...draft,name:e.target.value})}/></label><label className="grid gap-2 text-sm">类型<select className={catalogSelectStyle} required value={draft.provider} disabled={!!draft.id} onChange={e=>setDraft({...draft,provider:e.target.value})}>{providers.data.map(p=><option key={p.id} value={p.id}>{p.name}</option>)}</select></label><label className="grid gap-2 text-sm">接口地址<Input required type="url" placeholder="https://" disabled={!!draft.id} value={draft.base_url} onChange={e=>setDraft({...draft,base_url:e.target.value})}/></label><label className="grid gap-2 text-sm">用户名<Input required autoComplete="off" disabled={!!draft.id} value={draft.username} onChange={e=>setDraft({...draft,username:e.target.value})}/></label>{!!draft.id&&<p className="text-xs text-muted-foreground">接口地址和用户名已固定；更换账户请新增供应商。</p>}<h3 className="sm:col-span-2 text-sm font-semibold border-b pb-3 pt-3">安全与状态</h3><label className="grid gap-2 text-sm">{draft.id?"新密码（留空保留）":"密码"}<Input required={!draft.id} type="password" autoComplete="new-password" value={draft.password} onChange={e=>setDraft({...draft,password:e.target.value})}/></label><label className="flex items-center gap-2 text-sm"><Checkbox checked={draft.active} onCheckedChange={value=>setDraft({...draft,active:value===true})}/>启用供应商</label>{error && <p role="alert" className="text-sm text-destructive break-all">{error}</p>}<div className="sm:col-span-2 border-t pt-4 flex gap-3"><Button disabled={busy}><Save/>{busy?"保存中":"保存更改"}</Button><Button type="button" variant="outline" disabled={busy} onClick={()=>{setDraft(null);setError("");}}>取消更改</Button></div></fieldset></form></section>}
    {selected && <section className="border-t pt-6"><div className="flex flex-wrap justify-between gap-3 items-center mb-5"><div><h2 className="text-lg font-semibold">{selected.name} · 上游目录</h2><p className="text-sm text-muted-foreground mt-1">预览目录不会创建本地商品</p></div><Button variant="ghost" onClick={()=>{catalogRequest.current++;setCatalogBusy(false);setSelected(null);setCatalog(null);setProductID(null);setError("");setNotice("");}}><ArrowLeft/>返回供应商</Button></div>{catalogBusy?<Loading/>:catalog && <div className="divide-y border-y"><div className="flex flex-wrap gap-3 pb-4"><Input aria-label="搜索上游商品" className="sm:max-w-sm" placeholder="商品名称或上游 ID" value={catalogSearch} onChange={e=>setCatalogSearch(e.target.value)}/><select aria-label="目录导入状态" className={`${catalogSelectStyle} sm:max-w-xs`} value={supportFilter} onChange={e=>setSupportFilter(e.target.value)}><option value="all">全部商品</option><option value="supported">可导入或待配置</option><option value="unsupported">不支持导入</option></select></div><div className="overflow-x-auto"><Table><TableHeader><TableRow><TableHead>商品 / 上游 ID</TableHead><TableHead>库存</TableHead><TableHead>账期价格</TableHead><TableHead>状态</TableHead><TableHead>操作</TableHead></TableRow></TableHeader><TableBody>{visibleCatalog.map(product=><TableRow key={product.id}><TableCell className="min-w-48"><h3 className="font-medium break-words">{product.name}</h3><p className="text-xs text-muted-foreground mt-1">#{product.id}</p></TableCell><TableCell>{product.stock??"未提供"}</TableCell><TableCell className="min-w-40 text-xs">{Object.entries(product.prices??{}).map(([cycle,price])=><p key={cycle}>{cycles[cycle]??cycle} {money(price.amount_cents)}</p>)}</TableCell><TableCell className="text-xs">{product.supported?"可导入":unsupportedLabel(product.unsupported_reason)}</TableCell><TableCell><Button size="sm" variant="outline" disabled={!product.supported&&product.unsupported_reason!=="detail_required"} onClick={()=>setProductID(product.id)}>{product.unsupported_reason==="detail_required"?"查看配置":"配置导入"}</Button></TableCell></TableRow>)}{visibleCatalog.length===0&&<TableRow><TableCell colSpan={5} className="py-10 text-center text-muted-foreground">{catalog.length===0?"上游目录为空":"没有符合筛选条件的商品"}</TableCell></TableRow>}</TableBody></Table></div></div>}{productID && (detail.error?<Failure error={detail.error} retry={detail.refresh}/>:!detail.data?<Loading/>:<ImportProduct key={`${selected.id}:${productID}:${detail.data.revision}`} account={selected} product={detail.data}/>)}</section>}
  </div>;
}

function ImportProduct({account,product}:{account:SupplierAccount;product:SupplierProduct}) {
  const router=useRouter(); const manage=useAdminPermission("products.manage");
  const categories=useResource<CatalogCategory[]>("/admin/catalog-groups/category",false);
  const availableCycles=Object.keys(product.prices??{}).filter(c=>["monthly","quarterly","annually","once"].includes(c));
  const [cycle,setCycle]=useState(availableCycles[0]??"");
  const [name,setName]=useState(product.name);
  const [slug,setSlug]=useState("");
  const [categoryID,setCategoryID]=useState("");
  const [price,setPrice]=useState(amount(product.prices?.[availableCycles[0]]?.amount_cents??0));
  const [setup,setSetup]=useState(amount(product.prices?.[availableCycles[0]]?.setup_cents??0));
  const [maxCostOverride,setMaxCostOverride]=useState<string|null>(null);
  const [stock,setStock]=useState(product.stock==null?"":String(product.stock));
  const [options,setOptions]=useState<Record<string,string>>({});
  const [fields,setFields]=useState<Record<string,string>>({});
  const estimatedCost=upstreamCost(product,cycle,options);
  const maxCost=maxCostOverride??amount(estimatedCost);
  const [busy,setBusy]=useState(false);const [error,setError]=useState("");
  async function submit(event:React.FormEvent) {
    event.preventDefault(); if (!manage||busy||!product.supported||!product.revision) return;
    setBusy(true);setError("");
    try {
      if (cents(maxCost)<estimatedCost) throw new Error("上游最高成本低于当前规格和初装费合计");
      const result=await api<{id:number}>(`/admin/suppliers/${account.id}/import`,{method:"POST",body:JSON.stringify({upstream_product_id:product.id,expected_upstream_revision:product.revision,expected_supplier_version:account.version,name:name.trim(),slug:slug.trim(),category_id:categoryID?Number(categoryID):null,billing_cycle:cycle,price_cents:cents(price),setup_fee_cents:cents(setup),maximum_cost_cents:cents(maxCost),stock:stock===""?null:Number(stock),active:false,config_options:options,custom_fields:fields})});
      router.push(`/admin/products/${result.id}`);
    } catch (e) {setError((e as Error).message);} finally {setBusy(false);}
  }
  return <section className="border-t pt-6 mt-6 max-w-5xl space-y-5"><div><h3 className="text-lg font-semibold">导入 {product.name}</h3><p className="text-sm text-muted-foreground mt-1">选择固定的上游配置。客户使用本地购物车；导入后商品默认下架，可在商品编辑页上架。</p></div>{!product.supported?<p className="text-sm text-destructive">{unsupportedLabel(product.unsupported_reason)}</p>:<form onSubmit={submit} className="space-y-5"><fieldset disabled={!manage||busy} className="space-y-5"><h4 className="font-medium text-sm border-b pb-3">本地商品</h4><div className="grid sm:grid-cols-2 xl:grid-cols-4 gap-4"><label className="grid gap-2 text-sm">本地商品名称<Input required maxLength={180} value={name} onChange={e=>setName(e.target.value)}/></label><label className="grid gap-2 text-sm">公开标识<Input required maxLength={120} pattern="[a-z0-9\-]+" placeholder="例如 cloud-starter" value={slug} onChange={e=>setSlug(e.target.value)}/></label><label className="grid gap-2 text-sm">分类<select className={catalogSelectStyle} value={categoryID} onChange={e=>setCategoryID(e.target.value)}><option value="">未分类</option>{categories.data?.map(c=><option key={c.id} value={c.id}>{c.section_name?`${c.section_name} / `:""}{c.name}</option>)}</select></label><label className="grid gap-2 text-sm">账期<select className={catalogSelectStyle} required value={cycle} onChange={e=>setCycle(e.target.value)}>{availableCycles.map(c=><option key={c} value={c}>{cycles[c]??c}</option>)}</select></label></div>
    {(product.options??[]).length>0 && <div className="border-t pt-4 space-y-4"><h4 className="font-medium">上游固定配置</h4>{(product.options??[]).map(option=><label key={option.id} className="grid gap-2 text-sm">{option.name}{option.required&&" *"}<select className={catalogSelectStyle} required={option.required} value={options[String(option.id)]??""} onChange={e=>setOptions({...options,[option.id]:e.target.value})}><option value="">请选择</option>{(option.choices??[]).map(choice=><option key={choice.id} value={String(choice.id)}>{choice.name}{choice.prices?.[cycle]?` · ${money(choice.prices[cycle].amount_cents+choice.prices[cycle].setup_cents)}`:""}</option>)}</select></label>)}</div>}
    {(product.fields??[]).length>0 && <div className="border-t pt-4 space-y-4"><h4 className="font-medium">上游自定义字段</h4>{(product.fields??[]).map(field=><label key={field.id} className="grid gap-2 text-sm">{field.name}{field.required&&" *"}{field.description&&<span className="text-xs text-muted-foreground">{field.description}</span>}{(field.choices??[]).length>0?<select className={catalogSelectStyle} required={field.required} value={fields[String(field.id)]??""} onChange={e=>setFields({...fields,[field.id]:e.target.value})}><option value="">请选择</option>{(field.choices??[]).map(choice=><option key={choice} value={choice}>{choice}</option>)}</select>:field.type==="textarea"?<Textarea required={field.required} value={fields[String(field.id)]??""} onChange={e=>setFields({...fields,[field.id]:e.target.value})}/>:<Input required={field.required} type={field.type==="password"?"password":"text"} autoComplete="off" value={fields[String(field.id)]??""} onChange={e=>setFields({...fields,[field.id]:e.target.value})}/>}</label>)}</div>}
    <h4 className="font-medium text-sm border-b pb-3">售价与成本控制</h4><div className="grid sm:grid-cols-2 xl:grid-cols-4 gap-4"><label className="grid gap-2 text-sm">本地售价（元）<Input required inputMode="decimal" value={price} onChange={e=>setPrice(e.target.value)}/></label><label className="grid gap-2 text-sm">初装费（元）<Input required inputMode="decimal" value={setup} onChange={e=>setSetup(e.target.value)}/></label><label className="grid gap-2 text-sm">上游最高成本（元）<Input required inputMode="decimal" value={maxCost} onChange={e=>setMaxCostOverride(e.target.value)}/></label><label className="grid gap-2 text-sm">本地库存<Input type="number" min={0} max={2147483647} placeholder="留空不限量" value={stock} onChange={e=>setStock(e.target.value)}/></label></div><p className="text-xs text-muted-foreground">售价由本地独立设定。当前上游规格及初装费合计 {money(estimatedCost)}；最高成本不能低于此金额。</p>{categories.error&&<Failure error={categories.error} retry={categories.refresh}/>}<div className="border-t pt-4 flex flex-wrap gap-3"><Button disabled={busy||!cycle||!categories.data||!product.revision}><Download/>{busy?"导入中":"导入为下架商品"}</Button><Button type="button" variant="outline" disabled={busy} onClick={()=>{setCycle(availableCycles[0]??"");setName(product.name);setSlug("");setCategoryID("");setPrice(amount(product.prices?.[availableCycles[0]]?.amount_cents??0));setSetup(amount(product.prices?.[availableCycles[0]]?.setup_cents??0));setMaxCostOverride(null);setStock(product.stock==null?"":String(product.stock));setOptions({});setFields({});setError("");}}>取消更改</Button></div></fieldset>{error&&<p role="alert" className="text-sm text-destructive break-all">{error}</p>}</form>}</section>;
}
