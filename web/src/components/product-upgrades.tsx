"use client";
import { useRef, useState } from "react";
import { ArrowUpDown, Calculator, Save } from "lucide-react";
import { api, date, money, type ConfigurationChange, type Product, type Service } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { changeProductOption, configurationRuleError, isQuantityOption, optionQuantityBounds, resolveProductOptions } from "@/lib/product-options";
import { Checkbox } from "./ui/checkbox";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { DialogFooter } from "./ui/dialog";
import { Failure, Loading, cycles } from "./common";
import { ProductOptionChoice } from "./product-option-choice";

type Target = { product_id:number;allow_downgrade:boolean };
type TargetPolicy = { revision:number;targets:Target[];candidates:{id:number;name:string;active:boolean;compatible:boolean}[] };

export function ProductUpgradeTargets({product,manage,onReload}:{product:Product;manage:boolean;onReload:()=>void}) {
  const {data,error,refresh}=useResource<TargetPolicy>(`/admin/products/${product.id}/upgrade-targets`,false);
  if(error)return <Failure error={error} retry={refresh}/>;
  if(!data)return <Loading/>;
  return <ProductUpgradeTargetForm key={data.revision} product={product} policy={data} manage={manage} onReload={onReload}/>;
}
function ProductUpgradeTargetForm({product,policy,manage,onReload}:{product:Product;policy:TargetPolicy;manage:boolean;onReload:()=>void}) {
  const [targets,setTargets]=useState<Target[]>(policy.targets);
  const [query,setQuery]=useState("");
  const [busy,setBusy]=useState(false);
  const [error,setError]=useState("");
  async function save(){setBusy(true);setError("");try{await api(`/admin/products/${product.id}/upgrade-targets`,{method:"PUT",body:JSON.stringify({expected_revision:policy.revision,targets})});onReload();}catch(e){setError((e as Error).message);}finally{setBusy(false);}}
  return <section className="border-b pb-5 space-y-4">
    <div className="flex flex-wrap justify-between gap-3 items-center"><h2 className="font-semibold">商品升级目标</h2>{manage&&<Button size="sm" variant="outline" disabled={busy} onClick={()=>void save()}><Save/>保存升级目标</Button>}</div>
    <Input aria-label="搜索升级目标" placeholder="搜索商品" value={query} onChange={e=>setQuery(e.target.value)}/>
    <div className="divide-y">{policy.candidates.filter(candidate=>candidate.name.includes(query)).map(candidate=>{
      const selected=targets.find(target=>target.product_id===candidate.id);
      return <div key={candidate.id} className="py-3 flex flex-wrap justify-between gap-3">
        <label className="flex gap-2 items-center text-sm min-w-0">
          <Checkbox aria-label={`允许升级至 ${candidate.name}`} checked={!!selected} disabled={!manage||busy||(!candidate.compatible&&!selected)} onCheckedChange={checked=>setTargets(checked===true?[...targets,{product_id:candidate.id,allow_downgrade:false}]:targets.filter(target=>target.product_id!==candidate.id))}/>
          <span className="break-all">{candidate.name}{!candidate.active&&"（下架）"}{!candidate.compatible&&"（类型或交付模式不兼容）"}</span>
        </label>
        <label className="flex gap-2 items-center text-sm"><Checkbox aria-label={`允许降级至 ${candidate.name}`} checked={selected?.allow_downgrade??false} disabled={!manage||busy||!selected||!candidate.compatible} onCheckedChange={checked=>setTargets(targets.map(target=>target.product_id===candidate.id?{...target,allow_downgrade:checked===true}:target))}/>允许降级返还</label>
      </div>;
    })}</div>
    {policy.candidates.length===0&&<p className="text-sm text-muted-foreground">暂无可选商品</p>}{error&&<p role="alert" className="text-sm text-destructive">{error}</p>}
  </section>;
}

type UpgradeOptions = {source_revision:number;products:(Product & {upgrade_allow_downgrade:boolean})[]};
export function ProductChangeForm({service,onSubmitted,onBusyChange}:{service:Service;onSubmitted:()=>void;onBusyChange:(busy:boolean)=>void}) {
  const {data,error,refresh}=useResource<UpgradeOptions>(`/services/${service.id}/product-upgrade-options`,false);
  const [target,setTarget]=useState(0);
  const [busy,setBusy]=useState(false);
  if(error)return <Failure error={error} retry={refresh}/>;
  if(!data)return <Loading/>;
  const product=data.products.find(item=>item.id===target);
  return <div className="space-y-4"><label className="grid gap-2 text-sm">目标商品<Select value={target?String(target):""} disabled={busy} onValueChange={value=>setTarget(Number(value))}><SelectTrigger aria-label="升级目标商品" className="w-full"><SelectValue placeholder="选择商品"/></SelectTrigger><SelectContent>{data.products.map(item=><SelectItem key={item.id} value={String(item.id)}>{item.name}</SelectItem>)}</SelectContent></Select></label>{data.products.length===0&&<p className="text-sm text-muted-foreground">暂无可用升级商品</p>}{product&&<ProductChangeSelection key={`${product.id}:${product.revision}`} service={service} product={product} sourceRevision={data.source_revision} onSubmitted={onSubmitted} onBusyChange={value=>{setBusy(value);onBusyChange(value);}}/>}</div>;
}

function ProductChangeSelection({service,product,sourceRevision,onSubmitted,onBusyChange}:{service:Service;product:Product;sourceRevision:number;onSubmitted:()=>void;onBusyChange:(busy:boolean)=>void}) {
  const [choices,setChoices]=useState<Record<number,number>>({});
  const [quantities,setQuantities]=useState<Record<number,number>>({});
  const [quote,setQuote]=useState<ConfigurationChange|null>(null);
  const [promoCode,setPromoCode]=useState("");
  const [busy,setBusy]=useState(false);
  const [error,setError]=useState("");
  const submitKey=useRef("");
  const groups=(product.option_groups??[]).map(group=>({...group,options:group.options.filter(option=>!option.hidden)}));
  const resolved=resolveProductOptions(groups,choices);
  const quantityFor=(group:typeof groups[number],option:typeof group.options[number])=>quantities[option.id]??optionQuantityBounds(group,option).min;
  const ruleError=resolved.complete?configurationRuleError(product.configuration_rules,groups,resolved.ids,Object.fromEntries(resolved.branches.flatMap(({group,options,selected})=>{const option=options.find(candidate=>candidate.id===selected);return option&&isQuantityOption(group)?[[option.id,quantityFor(group,option)]]:[];}))):null;
  function working(value:boolean){setBusy(value);onBusyChange(value);}
  async function calculate(){if(ruleError){setError(ruleError);return;}working(true);setError("");setQuote(null);try{const optionQuantities=Object.fromEntries(resolved.branches.flatMap(({group,options,selected})=>{const option=options.find(item=>item.id===selected);return option&&isQuantityOption(group)?[[option.id,quantityFor(group,option)]]:[];}));const result=await api<ConfigurationChange>(`/services/${service.id}/product-quotes`,{method:"POST",body:JSON.stringify({target_product_id:product.id,target_revision:product.revision,expected_revision:sourceRevision,expected_version:service.version,option_ids:resolved.ids,option_quantities:optionQuantities,promo_code:promoCode.trim().toUpperCase()||undefined})});setQuote(result);submitKey.current=`web-product-change-${result.id}-${crypto.randomUUID()}`;}catch(e){setError((e as Error).message);}finally{working(false);}}
  async function submit(){if(!quote)return;working(true);setError("");try{await api(`/services/${service.id}/configuration-changes`,{method:"POST",body:JSON.stringify({quote_id:quote.id}),headers:{"Idempotency-Key":submitKey.current}});onSubmitted();}catch(e){setError((e as Error).message);}finally{working(false);}}
  return <div className="space-y-4"><fieldset disabled={busy} className="space-y-4">{resolved.branches.map(({group,options,selected})=>{const current=options.find(option=>option.id===selected);return <div key={group.id} className="space-y-2"><span className="block text-sm">{group.name}</span><ProductOptionChoice group={group} options={options} selected={selected} label={`升级 ${group.name}`} onChange={id=>{setChoices(changeProductOption(groups,resolved.selected,group.id,id));setQuote(null);}} />{current&&isQuantityOption(group)&&<label className="grid gap-2 text-xs text-muted-foreground">数量<Input aria-label={`升级 ${group.name} 数量`} type="number" {...optionQuantityBounds(group,current)} value={quantityFor(group,current)} onChange={e=>{setQuantities({...quantities,[current.id]:Number(e.target.value)});setQuote(null);}}/></label>}</div>;})}<label className="grid gap-2 text-sm">升级优惠码<Input aria-label="升级优惠码" maxLength={50} value={promoCode} placeholder="可选，升级专用优惠码" onChange={e=>{setPromoCode(e.target.value.toUpperCase());setQuote(null);}}/></label></fieldset>{ruleError&&<p role="alert" className="text-sm text-destructive">{ruleError}</p>}{quote&&<dl className="border-y py-4 text-sm space-y-3"><div className="flex flex-wrap justify-between gap-2"><dt>升级后续费金额</dt><dd>{money(quote.new_recurring_cents)} / {cycles[service.billing_cycle]}</dd></div><div className="flex flex-wrap justify-between gap-2"><dt>本期应付差额</dt><dd className="font-semibold">{money(quote.amount_cents)}</dd></div>{quote.upgrade_discount_cents>0&&<div className="flex flex-wrap justify-between gap-2"><dt>升级优惠</dt><dd>{money(quote.upgrade_discount_cents)} · {quote.upgrade_promo_code}</dd></div>}{quote.credit_cents>0&&<div className="flex flex-wrap justify-between gap-2"><dt>交付后返还余额</dt><dd>{money(quote.credit_cents)}</dd></div>}<div className="flex flex-wrap justify-between gap-2"><dt>报价有效期</dt><dd>{date(quote.expires_at)}</dd></div></dl>}{error&&<p role="alert" className="text-sm text-destructive">{error}</p>}<DialogFooter><Button variant="outline" disabled={busy||!resolved.complete||!!ruleError} onClick={()=>void calculate()}><Calculator/>计算升级差额</Button>{quote&&<Button disabled={busy} onClick={()=>void submit()}><ArrowUpDown/>生成升级账单</Button>}</DialogFooter></div>;
}
