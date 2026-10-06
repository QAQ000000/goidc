"use client";
import { useRef, useState } from "react";
import Link from "next/link";
import { ArrowUpDown, Calculator, Save, X } from "lucide-react";
import { api, date, money, type ConfigurationChange, type Product, type Service } from "@/lib/api";
import { changeProductOption, isQuantityOption, optionQuantityBounds, resolveProductOptions } from "@/lib/product-options";
import { useResource } from "@/hooks/use-resource";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Input } from "./ui/input";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "./ui/dialog";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Failure, Loading, Status, cycles } from "./common";
import { useAdminPermission } from "./workspace";
import { ProductChangeForm } from "./product-upgrades";
import { ProductOptionChoice } from "./product-option-choice";
import { RenewalBenefitDetails } from "./renewal-benefit";

export function ConfigurationUpgradePolicy({product,manage,onReload}:{product:Product;manage:boolean;onReload:()=>void}) {
  const [enabled,setEnabled]=useState(!!product.config_upgrade_enabled);
  const [downgrade,setDowngrade]=useState(!!product.config_downgrade_enabled);
  const [busy,setBusy]=useState(false);
  const [error,setError]=useState("");
  async function save() {
    setBusy(true);setError("");
    try { await api(`/admin/products/${product.id}/configuration-upgrade-policy`,{method:"PUT",body:JSON.stringify({enabled,allow_downgrade:enabled&&downgrade,expected_revision:product.revision})});onReload(); }
    catch(e){setError((e as Error).message);}finally{setBusy(false);}
  }
  return <section className="border-b pb-5 space-y-3"><h2 className="font-semibold">已购服务配置变更</h2><div className="flex flex-wrap items-center gap-5">
    <label className="flex items-center gap-2 text-sm"><Checkbox checked={enabled} disabled={!manage||busy} onCheckedChange={value=>setEnabled(value===true)}/>开放配置变更</label>
    <label className="flex items-center gap-2 text-sm"><Checkbox checked={enabled&&downgrade} disabled={!manage||busy||!enabled} onCheckedChange={value=>setDowngrade(value===true)}/>允许降配差额返还余额</label>
    {manage&&<Button size="sm" variant="outline" disabled={busy||(enabled===!!product.config_upgrade_enabled&&(enabled&&downgrade)===!!product.config_downgrade_enabled)} onClick={()=>void save()}><Save/>保存策略</Button>}
  </div>{error&&<p role="alert" className="text-sm text-destructive">{error}</p>}</section>;
}

export function ServiceConfigurationChanges({service,admin,refresh}:{service:Service;admin:boolean;refresh:()=>void}) {
  const canManage=useAdminPermission("services.manage");
  const [open,setOpen]=useState<"configuration"|"product"|"billing_cycle"|null>(null);
  const [busy,setBusy]=useState(false);
  const [error,setError]=useState("");
  const changes=service.configuration_changes??[];
  const pending=changes.some(change=>["unpaid","queued","waiting_manual"].includes(change.status));
  async function cancel(id:number) {
    setBusy(true);setError("");
    try{await api(`/services/${service.id}/configuration-changes/${id}`,{method:"DELETE"});refresh();}
    catch(e){setError((e as Error).message);}finally{setBusy(false);}
  }
  async function retry(id:number) {
    setBusy(true);setError("");
    try{await api(`/admin/services/${service.id}/configuration-changes/${id}/retry`,{method:"POST",body:"{}"});refresh();}
    catch(e){setError((e as Error).message);}finally{setBusy(false);}
  }
  return <section className="border-y py-5 my-6 space-y-4">
    <div className="flex flex-wrap items-start justify-between gap-3"><div><h2 className="font-semibold">当前配置</h2><p className="text-sm text-muted-foreground mt-1">续费金额 {money(service.renewal_amount_cents ?? service.recurring_price_cents)} / {cycles[service.billing_cycle]??service.billing_cycle}</p>{service.renewal_promo_code && <><p className="text-xs text-muted-foreground mt-2">周期原价 {money(service.recurring_price_cents)}</p><RenewalBenefitDetails terms={service} used={service.renewal_discounts_used}/></>}</div><div className="flex flex-wrap gap-2">{!admin&&service.provision_mode!=="nokvm"&&service.product_upgrade_available&&service.status==="active"&&<Button variant="outline" disabled={pending} onClick={()=>setOpen("product")}><ArrowUpDown/>升级商品</Button>}{!admin&&service.provision_mode!=="nokvm"&&service.config_upgrade_enabled&&service.status==="active"&&<Button variant="outline" disabled={pending} onClick={()=>setOpen("configuration")}><ArrowUpDown/>变更配置</Button>}{!admin&&service.config_upgrade_enabled&&service.status==="active"&&service.billing_cycle!=="once"&&<Button variant="outline" disabled={pending} onClick={()=>setOpen("billing_cycle")}><ArrowUpDown/>变更账期</Button>}</div></div>
    {!!service.renewal_promo_code && <p className="text-xs text-muted-foreground">续费优惠在配置、账期或商品变更后保留，并按新的周期原价计算。变更差价和降配返还按原价计算。</p>}
    {(service.config_snapshot??[]).map(choice=><div key={choice.group_id} className="flex flex-wrap justify-between gap-3 text-sm"><span>{choice.group_name}：{choice.label}{(choice.quantity??1)>1?` × ${choice.quantity}`:""}</span><span className="text-muted-foreground">{money(choice.price_delta_cents)} / {cycles[service.billing_cycle]}</span></div>)}
    {changes.length>0&&<div className="divide-y border-t">{changes.map(change=><div key={change.id} className="py-3 space-y-2 text-sm"><div className="flex flex-wrap justify-between gap-3"><span className="break-all">{change.change_type==="product"?`商品升级 #${change.id} · ${change.target_product_name}`:change.change_type==="billing_cycle"?`账期变更 #${change.id} · ${cycles[change.billing_cycle??service.billing_cycle]??change.billing_cycle} → ${cycles[change.new_billing_cycle??""]??change.new_billing_cycle}`:`配置变更 #${change.id}`} · {money(change.new_recurring_cents)} / {cycles[change.new_billing_cycle??service.billing_cycle]}</span><Status value={change.status}/></div><p className="text-xs text-muted-foreground break-words">{change.config_snapshot.map(choice=>`${choice.group_name}：${choice.label}${(choice.quantity??1)>1?` × ${choice.quantity}`:""}`).join(" · ")}</p><div className="flex flex-wrap items-center gap-3"><span>应付 {money(change.amount_cents)}{change.credit_cents>0?` · 交付后返还余额 ${money(change.credit_cents)}`:""}</span>{!admin&&change.status==="unpaid"&&<><Button size="sm" variant="outline" asChild><Link href={`/console/invoices/${change.invoice_id}`}>支付账单 #{change.invoice_id}</Link></Button><Button size="icon" variant="ghost" aria-label={`取消服务变更 ${change.id}`} title="取消服务变更" disabled={busy} onClick={()=>void cancel(change.id)}><X/></Button></>}</div></div>)}</div>}
    {admin&&canManage&&changes.filter(change=>service.jobs?.some(job=>job.id===change.job_id&&job.status==="failed")).map(change=><Button key={change.id} variant="outline" disabled={busy} onClick={()=>void retry(change.id)}><ArrowUpDown/>重试配置交付 #{change.id}</Button>)}
    {error&&<p role="alert" className="text-sm text-destructive">{error}</p>}
    <Dialog open={open!==null} onOpenChange={next=>{if(!busy&&!next)setOpen(null);}}><DialogContent className="max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>{open==="product"?"升级商品":open==="billing_cycle"?"变更服务账期":`变更 ${service.name} 配置`}</DialogTitle></DialogHeader>{open==="product"?<ProductChangeForm key={`${service.id}:${service.version}`} service={service} onBusyChange={setBusy} onSubmitted={()=>{setOpen(null);refresh();}}/>:open==="configuration"?<ConfigurationChangeForm key={`${service.id}:${service.version}`} service={service} onBusyChange={setBusy} onSubmitted={()=>{setOpen(null);refresh();}}/>:open==="billing_cycle"?<BillingCycleChangeForm key={`${service.id}:${service.version}`} service={service} onBusyChange={setBusy} onSubmitted={()=>{setOpen(null);refresh();}}/>:null}</DialogContent></Dialog>
  </section>;
}

function BillingCycleChangeForm({service,onSubmitted,onBusyChange}:{service:Service;onSubmitted:()=>void;onBusyChange:(busy:boolean)=>void}) {
  const [target,setTarget]=useState(""); const [quote,setQuote]=useState<ConfigurationChange|null>(null); const [busy,setBusy]=useState(false); const [error,setError]=useState(""); const key=useRef("");
  function working(value:boolean){setBusy(value);onBusyChange(value);}
  async function calculate(){if(!target)return;working(true);setError("");setQuote(null);try{const result=await api<ConfigurationChange>(`/services/${service.id}/billing-cycle-quotes`,{method:"POST",body:JSON.stringify({target_billing_cycle:target,expected_version:service.version,expected_revision:service.product_revision})});setQuote(result);key.current=`web-billing-cycle-${result.id}-${crypto.randomUUID()}`;}catch(e){setError((e as Error).message);}finally{working(false);}}
  async function submit(){if(!quote)return;working(true);setError("");try{await api(`/services/${service.id}/configuration-changes`,{method:"POST",body:JSON.stringify({quote_id:quote.id}),headers:{"Idempotency-Key":key.current}});onSubmitted();}catch(e){setError((e as Error).message);}finally{working(false);}}
  return <div className="space-y-4"><label className="grid gap-2 text-sm">目标账期<Select value={target} disabled={busy} onValueChange={value=>{setTarget(value);setQuote(null);}}><SelectTrigger aria-label="目标账期"><SelectValue placeholder="选择账期"/></SelectTrigger><SelectContent>{["monthly","quarterly","annually"].filter(c=>c!==service.billing_cycle).map(c=><SelectItem key={c} value={c}>{cycles[c]}</SelectItem>)}</SelectContent></Select></label>{quote&&<dl className="border-y py-4 text-sm space-y-3"><div className="flex justify-between gap-2"><dt>变更后周期价</dt><dd>{money(quote.new_recurring_cents)} / {cycles[quote.new_billing_cycle??target]}</dd></div><div className="flex justify-between gap-2"><dt>本期应付差额</dt><dd className="font-semibold">{money(quote.amount_cents)}</dd></div>{quote.credit_cents>0&&<div className="flex justify-between gap-2"><dt>交付后返还余额</dt><dd>{money(quote.credit_cents)}</dd></div>}</dl>}{error&&<p role="alert" className="text-sm text-destructive">{error}</p>}<DialogFooter><Button variant="outline" disabled={busy||!target} onClick={()=>void calculate()}><Calculator/>计算差额</Button>{quote&&<Button disabled={busy} onClick={()=>void submit()}><ArrowUpDown/>生成变更账单</Button>}</DialogFooter></div>;
}

function ConfigurationChangeForm({service,onSubmitted,onBusyChange}:{service:Service;onSubmitted:()=>void;onBusyChange:(busy:boolean)=>void}) {
  const {data:product,error:loadError,refresh}=useResource<Product>(`/services/${service.id}/upgrade-options`,false);
  const [choices,setChoices]=useState<Record<number,number>>({});
  const [quantities,setQuantities]=useState<Record<number,number>>({...service.option_quantities});
  const [quote,setQuote]=useState<ConfigurationChange|null>(null);
  const [busy,setBusy]=useState(false);
  const [error,setError]=useState("");
  const submitKey=useRef("");
  const groups=product?.option_groups??[];
  const original=Object.fromEntries((service.config_snapshot??[]).map(choice=>[choice.group_id,choice.id]));
  const resolved=resolveProductOptions(groups,{...original,...choices});
  const quantityFor=(group:typeof groups[number],option:typeof group.options[number])=>quantities[option.id]??optionQuantityBounds(group,option).min;
  function working(next:boolean){setBusy(next);onBusyChange(next);}
  async function calculate() {
    if(!product)return;
    working(true);setError("");setQuote(null);
    try {
      const selectedQuantities=Object.fromEntries(resolved.branches.flatMap(({group,options,selected})=>{const option=options.find(candidate=>candidate.id===selected);return option&&isQuantityOption(group)?[[option.id,quantityFor(group,option)]]:[];}));
      const result=await api<ConfigurationChange>(`/services/${service.id}/configuration-quotes`,{method:"POST",body:JSON.stringify({option_ids:resolved.ids,option_quantities:selectedQuantities,expected_version:service.version,expected_revision:product.revision})});
      setQuote(result);submitKey.current=`web-config-change-${result.id}-${crypto.randomUUID()}`;
    }catch(e){setError((e as Error).message);}finally{working(false);}
  }
  async function submit() {
    if(!quote)return;
    working(true);setError("");
    try{await api(`/services/${service.id}/configuration-changes`,{method:"POST",body:JSON.stringify({quote_id:quote.id}),headers:{"Idempotency-Key":submitKey.current}});onSubmitted();}
    catch(e){setError((e as Error).message);}finally{working(false);}
  }
  if(loadError)return <Failure error={loadError} retry={refresh}/>;
  if(!product)return <Loading/>;
  return <div className="space-y-4"><fieldset disabled={busy} className="space-y-4">{resolved.branches.map(({group,options,selected})=>{
    const current=options.find(option=>option.id===selected);
    return <div key={group.id} className="space-y-2"><span className="block text-sm">{group.name}</span><ProductOptionChoice group={group} options={options.filter(option=>!option.hidden||option.id===original[group.id])} selected={selected} label={`变更 ${group.name}`} disabled={busy||group.upgrade_enabled===false} optionDisabled={option=>option.upgrade_enabled===false&&option.id!==original[group.id]} onChange={id=>{setChoices(changeProductOption(groups,resolved.selected,group.id,id));setQuote(null);}} />{current&&isQuantityOption(group)&&<label className="grid gap-2 text-xs text-muted-foreground">数量<Input aria-label={`变更 ${group.name} 数量`} type="number" {...optionQuantityBounds(group,current)} value={quantityFor(group,current)} disabled={group.upgrade_enabled===false||current.upgrade_enabled===false} onChange={event=>{setQuantities({...quantities,[current.id]:Number(event.target.value)});setQuote(null);}}/></label>}</div>;
  })}</fieldset>{quote&&<dl className="border-y py-4 text-sm space-y-3"><div className="flex flex-wrap justify-between gap-2"><dt>变更后续费金额</dt><dd>{money(quote.new_recurring_cents)} / {cycles[service.billing_cycle]}</dd></div><div className="flex flex-wrap justify-between gap-2"><dt>本期应付差额</dt><dd className="font-semibold">{money(quote.amount_cents)}</dd></div>{quote.credit_cents>0&&<div className="flex flex-wrap justify-between gap-2"><dt>交付后返还余额</dt><dd>{money(quote.credit_cents)}</dd></div>}<div className="flex flex-wrap justify-between gap-2"><dt>报价有效期</dt><dd>{date(quote.expires_at)}</dd></div></dl>}{error&&<p role="alert" className="text-sm text-destructive">{error}</p>}<DialogFooter><Button variant="outline" disabled={busy||!resolved.complete} onClick={()=>void calculate()}><Calculator/>计算差额</Button>{quote&&<Button disabled={busy} onClick={()=>void submit()}><ArrowUpDown/>生成变更账单</Button>}</DialogFooter></div>;
}
