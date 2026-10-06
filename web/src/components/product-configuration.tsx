"use client";
import { useEffect, useState } from "react";
import { Plus, Pencil, Save, Trash2 } from "lucide-react";
import { api, money, type Product, type ProductOptionGroup, type ProductOption } from "@/lib/api";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Checkbox } from "./ui/checkbox";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "./ui/dialog";
import { optionGroupDescendants } from "@/lib/product-options";
import { makeTierDrafts, parseTierChanges, QuantityTierEditor, tierCycles } from "./product-option-tiers";
import { ConfigurationUpgradePolicy } from "./configuration-upgrades";
import { ProductUpgradeTargets } from "./product-upgrades";

const emptyGroup:ProductOptionGroup={id:0,name:"",slug:"",required:false,active:true,sort_order:0,option_type:0,qty_minimum:0,qty_maximum:0,qty_stage:0,unit:"",options:[]};
const emptyOption:ProductOption={id:0,label:"",value:"",price_delta_cents:0,setup_delta_cents:0,sort_order:0,active:true,qty_minimum:0,qty_maximum:0};
type Edit={kind:"group";group:ProductOptionGroup}|{kind:"option";group:ProductOptionGroup;option:ProductOption};
function parseDelta(input:string){const match=input.match(/^(-?)(\d{1,7})(?:\.(\d{1,2}))?$/);if(!match)throw new Error("金额最多保留两位小数，可用负数表示减价");const amount=(Number(match[2])*100+Number((match[3]??"").padEnd(2,"0")))*(match[1]? -1:1);if(Math.abs(amount)>100000000)throw new Error("增减金额不能超过 100 万元");return amount;}
const quantityOptionTypes = new Set([4,7,9,11,14,15,16,17,18,19]);
type FormState={dirty:boolean;busy:boolean};

export function ProductConfigurationPanel({product,manage,onReload}:{product:Product;manage:boolean;onReload:()=>void}){
 const [edit,setEdit]=useState<Edit|null>(null);
 const [formState,setFormState]=useState<FormState>({dirty:false,busy:false});
 const [reloadEpoch,setReloadEpoch]=useState(0);
 const [loadError,setLoadError]=useState("");
 async function reload(){try{const fresh=await api<Product>(`/admin/products/${product.id}`);if(edit){const group=fresh.option_groups?.find(g=>g.id===edit.group.id);if(edit.group.id===0)setEdit({...edit});else if(!group)setEdit(null);else if(edit.kind==="group")setEdit({kind:"group",group});else {const option=group.options.find(o=>o.id===edit.option.id);setEdit(edit.option.id===0?{...edit,group}:option?{kind:"option",group,option}:null);}}setReloadEpoch(value=>value+1);setLoadError("");onReload();}catch(e){setLoadError((e as Error).message);}}
 const groups=product.option_groups??[];
 return <div className="max-w-4xl space-y-5"><div className="flex flex-wrap justify-between items-start gap-4"><p className="text-sm text-muted-foreground max-w-xl">每组选择一项，增减价在所选账期生效。修改会要求旧购物车重新确认；已购服务使用下单金额。</p>{manage&&<Button onClick={()=>setEdit({kind:"group",group:emptyGroup})}><Plus/>新增配置组</Button>}</div>
 <ConfigurationUpgradePolicy key={`${product.id}:${product.revision}`} product={product} manage={manage} onReload={onReload}/>
 <ProductUpgradeTargets key={`targets:${product.id}:${product.revision}`} product={product} manage={manage} onReload={onReload}/>
 {loadError&&<p role="alert" className="text-destructive text-sm">{loadError}</p>}
 {groups.length===0&&<p className="text-center py-12 text-muted-foreground border rounded-md">暂无配置组</p>}
 {groups.map(group=><section key={group.id} className="border rounded-md overflow-hidden"><div className="p-5 bg-muted/30 flex flex-wrap items-start justify-between gap-4"><div className="min-w-0 flex-1"><h2 className="font-semibold break-all">{group.name}</h2><p className="text-xs text-muted-foreground mt-2 break-all">{group.slug} · {group.active?"启用":"停用"} · {group.required?"必选":"可选"} · 排序 {group.sort_order}{group.unit?` · 单位 ${group.unit}`:""}{group.qty_minimum||group.qty_maximum?` · 范围 ${group.qty_minimum??0}-${group.qty_maximum??"不限"}`:""}</p></div><div className="flex flex-wrap gap-2"><Button variant="outline" size="sm" aria-label={`查看配置组 ${group.name}`} onClick={()=>setEdit({kind:"group",group})}><Pencil/>{manage?"编辑组":"查看组"}</Button>{manage&&<Button variant="outline" size="sm" aria-label={`添加选项 ${group.name}`} onClick={()=>setEdit({kind:"option",group,option:emptyOption})}><Plus/>添加选项</Button>}</div></div>
 <div className="divide-y">{group.options.map(option=><article key={option.id} className="p-5 flex flex-wrap items-center justify-between gap-4"><div className="min-w-0 flex-1"><p className="font-medium break-all">{option.label}</p><p className="text-xs text-muted-foreground mt-2 break-all">{option.value} · {option.active?"启用":"停用"} · 排序 {option.sort_order}{option.qty_minimum||option.qty_maximum?` · 范围 ${option.qty_minimum??0}-${option.qty_maximum??"不限"}`:""}</p></div><div className="text-xs text-muted-foreground"><p>周期增减 {money(option.price_delta_cents)}</p><p className="mt-1">初装增减 {money(option.setup_delta_cents)}</p>{option.pricing&&option.pricing.length>0&&<p className="mt-1 break-all">{option.pricing.map(price=>`${price.billing_cycle} ${money(price.price_cents)}`).join(" · ")}</p>}{option.price_tiers&&option.price_tiers.length>0&&<p className="mt-1">数量阶梯 {option.price_tiers.length} 段</p>}</div><Button variant="ghost" size="sm" aria-label={`查看选项 ${option.label}`} onClick={()=>setEdit({kind:"option",group,option})}>{manage?"编辑":"查看"}</Button></article>)}{group.options.length===0&&<p className="p-5 text-sm text-muted-foreground">暂无选项，添加后可设为必选。</p>}</div></section>)}
 <Dialog open={!!edit} onOpenChange={open=>{if(!open&&!formState.busy&&(!formState.dirty||window.confirm("放弃未保存的配置修改？"))){setEdit(null);setFormState({dirty:false,busy:false});}}}><DialogContent className="max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>{edit?.kind==="group"?(edit.group.id?"配置组详情":"新增配置组"):(edit?.option.id?"配置选项详情":"新增配置选项")}</DialogTitle><DialogDescription>有关联购物车或订单的配置需要停用，不能直接删除。</DialogDescription></DialogHeader>{edit&&<ConfigurationForm key={`${edit.kind}:${edit.group.id}:${edit.kind==="option"?edit.option.id:0}:${product.revision}:${reloadEpoch}`} edit={edit} product={product} manage={manage} onStateChange={setFormState} onSaved={()=>{setFormState({dirty:false,busy:false});setEdit(null);onReload();}} onReload={()=>void reload()}/>}</DialogContent></Dialog>
 </div>;
}

function ConfigurationForm({edit,product,manage,onSaved,onReload,onStateChange}:{edit:Edit;product:Product;manage:boolean;onSaved:()=>void;onReload:()=>void;onStateChange:(state:FormState)=>void}){
 const [group,setGroup]=useState(edit.group);
 const [option,setOption]=useState(edit.kind==="option"?edit.option:emptyOption);
 const [price,setPrice]=useState((option.price_delta_cents/100).toFixed(2));
 const [setup,setSetup]=useState((option.setup_delta_cents/100).toFixed(2));
 const quantityOption=edit.kind==="option"&&quantityOptionTypes.has(group.option_type??0);
 const initialTierCycle=option.price_tiers?.some(t=>t.billing_cycle===product.billing_cycle)?product.billing_cycle:(option.price_tiers?.[0]?.billing_cycle??product.billing_cycle);
 const [tierCycle,setTierCycle]=useState(tierCycles[initialTierCycle]?initialTierCycle:"monthly");
 const [initialTiers]=useState(()=>makeTierDrafts(option.price_tiers));
 const [tierDrafts,setTierDrafts]=useState(initialTiers);
 const [error,setError]=useState("");
 const [busy,setBusy]=useState(false);
 const changedCycles=Object.keys(tierCycles).filter(cycle=>JSON.stringify(tierDrafts[cycle])!==JSON.stringify(initialTiers[cycle]));
 const originalOption=edit.kind==="option"?edit.option:emptyOption;
 const dirty=manage&&(edit.kind==="group"?JSON.stringify(group)!==JSON.stringify(edit.group):JSON.stringify(option)!==JSON.stringify(originalOption)||price!==(originalOption.price_delta_cents/100).toFixed(2)||setup!==(originalOption.setup_delta_cents/100).toFixed(2)||changedCycles.length>0);
 useEffect(()=>{onStateChange({dirty,busy});},[dirty,busy,onStateChange]);
 useEffect(()=>{if(!dirty)return;const warn=(event:BeforeUnloadEvent)=>{event.preventDefault();event.returnValue="";};window.addEventListener("beforeunload",warn);return()=>window.removeEventListener("beforeunload",warn);},[dirty]);
 const groupPath=`/admin/products/${product.id}/option-groups`;
 const path=edit.kind==="group"?`${groupPath}${group.id?`/${group.id}`:""}`:`${groupPath}/${group.id}/options${option.id?`/${option.id}`:""}`;
 const exists=edit.kind==="group"?group.id>0:option.id>0;
 const descendants=optionGroupDescendants(product.option_groups??[],group.id);
 const parentGroups=(product.option_groups??[]).filter(candidate=>candidate.option_type===20&&!descendants.has(candidate.id));
 const parentGroup=(product.option_groups??[]).find(candidate=>candidate.id===group.parent_group_id);
 function changeParentGroup(parentID:number){setGroup(current=>({...current,parent_group_id:parentID,options:current.options.map(choice=>({...choice,parent_option_id:0}))}));}
 async function save(e:React.FormEvent){
  e.preventDefault();if(!manage||busy)return;setBusy(true);setError("");
  try {
   if(group.parent_group_id&&edit.kind==="option"&&!option.parent_option_id)throw new Error("请选择父选项");
   if(group.parent_group_id&&edit.kind==="group"&&group.options.some(choice=>!choice.parent_option_id))throw new Error("请为每个选项选择父选项");
   const body=edit.kind==="group"?{
    name:group.name,slug:group.slug,required:group.required,active:group.active,sort_order:group.sort_order,
    option_type:group.option_type??0,parent_group_id:group.parent_group_id??0,
    option_parents:Object.fromEntries(group.options.map(choice=>[choice.id,group.parent_group_id?choice.parent_option_id??0:0])),
    qty_minimum:group.qty_minimum??0,qty_maximum:group.qty_maximum??0,qty_stage:group.qty_stage??0,unit:group.unit??"",expected_revision:product.revision,upgrade_enabled:group.upgrade_enabled??true
   }:{label:option.label,value:option.value,active:option.active,upgrade_enabled:option.upgrade_enabled??true,sort_order:option.sort_order,parent_option_id:option.parent_option_id??0,price_delta_cents:parseDelta(price),setup_delta_cents:parseDelta(setup),qty_minimum:option.qty_minimum??0,qty_maximum:option.qty_maximum??0,expected_revision:product.revision,...(changedCycles.length>0?{price_tier_changes:parseTierChanges(tierDrafts,changedCycles)}:{})};
   await api(path,{method:exists?"PUT":"POST",body:JSON.stringify(body)}); onSaved();
  }
  catch(e){setError((e as Error).message);} finally{setBusy(false);}
 }
 async function remove(){if(!manage||busy||!window.confirm("永久删除此配置？"))return;setBusy(true);setError("");try{await api(path,{method:"DELETE",body:JSON.stringify({expected_revision:product.revision})});onSaved();}catch(e){setError((e as Error).message);}finally{setBusy(false);}}
 return <form onSubmit={save} className="space-y-4">
  <fieldset disabled={busy||!manage} className="space-y-4">
   {edit.kind==="group" ? <>
    <label className="grid gap-2 text-sm">名称<Input aria-label="配置组名称" required maxLength={120} value={group.name} onChange={e=>setGroup({...group,name:e.target.value})}/></label>
    <label className="grid gap-2 text-sm">标识<Input aria-label="配置组标识" required pattern={"[a-z][a-z0-9\\-]*"} maxLength={120} value={group.slug} onChange={e=>setGroup({...group,slug:e.target.value})}/></label>
    <div className="grid sm:grid-cols-2 gap-4"><label className="grid gap-2 text-sm">配置类型<Select value={String(group.option_type??0)} onValueChange={value=>setGroup({...group,option_type:Number(value),parent_group_id:Number(value)===20?group.parent_group_id:0})}><SelectTrigger aria-label="配置类型"><SelectValue/></SelectTrigger><SelectContent><SelectItem value="0">固定选项</SelectItem><SelectItem value="4">数量</SelectItem><SelectItem value="20">多级选择</SelectItem>{group.option_type!==undefined&&![0,4,20].includes(group.option_type)&&<SelectItem value={String(group.option_type)}>旧类型 {group.option_type}</SelectItem>}</SelectContent></Select></label><label className="grid gap-2 text-sm">单位<Input aria-label="数量单位" maxLength={32} value={group.unit??""} onChange={e=>setGroup({...group,unit:e.target.value})}/></label><label className="grid gap-2 text-sm">最小数量<Input aria-label="配置组最小数量" type="number" min={0} value={group.qty_minimum??0} onChange={e=>setGroup({...group,qty_minimum:Number(e.target.value)})}/></label><label className="grid gap-2 text-sm">最大数量<Input aria-label="配置组最大数量" type="number" min={0} value={group.qty_maximum??0} onChange={e=>setGroup({...group,qty_maximum:Number(e.target.value)})}/></label><label className="grid gap-2 text-sm">步长<Input aria-label="配置组数量步长" type="number" min={0} value={group.qty_stage??0} onChange={e=>setGroup({...group,qty_stage:Number(e.target.value)})}/></label></div>
    {group.option_type===20&&<>
     <label className="grid gap-2 text-sm">父配置组<Select value={String(group.parent_group_id??0)} onValueChange={value=>changeParentGroup(Number(value))}><SelectTrigger aria-label="父配置组" className="w-full"><SelectValue/></SelectTrigger><SelectContent><SelectItem value="0">根配置组</SelectItem>{parentGroups.map(parent=><SelectItem key={parent.id} value={String(parent.id)}>{parent.name}{!parent.active?"（停用）":""}</SelectItem>)}</SelectContent></Select></label>
     {!!group.parent_group_id&&group.options.map(choice=><label key={choice.id} className="grid gap-2 text-sm">{choice.label} 的父选项<Select value={choice.parent_option_id?String(choice.parent_option_id):""} onValueChange={value=>setGroup(current=>({...current,options:current.options.map(item=>item.id===choice.id?{...item,parent_option_id:Number(value)}:item)}))}><SelectTrigger aria-label={`${choice.label} 的父选项`} className="w-full"><SelectValue placeholder="选择父选项"/></SelectTrigger><SelectContent>{parentGroup?.options.map(parent=><SelectItem key={parent.id} value={String(parent.id)}>{parent.label}{!parent.active?"（停用）":""}</SelectItem>)}</SelectContent></Select></label>)}
    </>}
    <label className="grid gap-2 text-sm">排序<Input aria-label="配置组排序" type="number" required min={0} max={1000000} value={group.sort_order} onChange={e=>setGroup({...group,sort_order:Number(e.target.value)})}/></label><label className="flex gap-3 items-center text-sm"><Checkbox checked={group.active} onCheckedChange={v=>setGroup({...group,active:v===true})}/>启用配置组</label><label className="flex gap-3 items-center text-sm"><Checkbox checked={group.required} disabled={!group.required&&!group.options.some(o=>o.active)} onCheckedChange={v=>setGroup({...group,required:v===true})}/>客户必须选择</label>{!group.options.some(o=>o.active)&&<p className="text-xs text-muted-foreground">先保存配置组并添加启用选项，再开启必选。</p>}
   </> : <>
    <label className="grid gap-2 text-sm">名称<Input aria-label="选项名称" required maxLength={120} value={option.label} onChange={e=>setOption({...option,label:e.target.value})}/></label><label className="grid gap-2 text-sm">提交值<Input aria-label="选项提交值" required maxLength={120} value={option.value} onChange={e=>setOption({...option,value:e.target.value})}/></label>
    {!!group.parent_group_id&&<label className="grid gap-2 text-sm">父选项<Select value={option.parent_option_id?String(option.parent_option_id):""} onValueChange={value=>setOption({...option,parent_option_id:Number(value)})}><SelectTrigger aria-label="父选项" className="w-full"><SelectValue placeholder="选择父选项"/></SelectTrigger><SelectContent>{parentGroup?.options.map(parent=><SelectItem key={parent.id} value={String(parent.id)}>{parent.label}{!parent.active?"（停用）":""}</SelectItem>)}</SelectContent></Select></label>}
    <div className="grid sm:grid-cols-2 gap-4"><label className="grid gap-2 text-sm">周期增减（元）<Input aria-label="选项周期增减" inputMode="decimal" required value={price} onChange={e=>setPrice(e.target.value)}/></label><label className="grid gap-2 text-sm">初装增减（元）<Input aria-label="选项初装增减" inputMode="decimal" required value={setup} onChange={e=>setSetup(e.target.value)}/></label><label className="grid gap-2 text-sm">最小数量<Input aria-label="选项最小数量" type="number" min={0} value={option.qty_minimum??0} onChange={e=>setOption({...option,qty_minimum:Number(e.target.value)})}/></label><label className="grid gap-2 text-sm">最大数量<Input aria-label="选项最大数量" type="number" min={0} value={option.qty_maximum??0} onChange={e=>setOption({...option,qty_maximum:Number(e.target.value)})}/></label></div>
    <label className="grid gap-2 text-sm">排序<Input aria-label="选项排序" required type="number" min={0} max={1000000} value={option.sort_order} onChange={e=>setOption({...option,sort_order:Number(e.target.value)})}/></label><label className="flex gap-3 items-center text-sm"><Checkbox checked={option.active} onCheckedChange={v=>setOption({...option,active:v===true})}/>启用选项</label><p className="text-xs text-muted-foreground">增减后周期价与初装费各自不能低于零。</p>
   </>}
   <label className="flex gap-3 items-center text-sm"><Checkbox checked={(edit.kind==="group"?group.upgrade_enabled:option.upgrade_enabled)??true} onCheckedChange={v=>edit.kind==="group"?setGroup({...group,upgrade_enabled:v===true}):setOption({...option,upgrade_enabled:v===true})}/>允许已购服务变更此配置</label>
  </fieldset>
  {quantityOption&&option.id>0&&<QuantityTierEditor drafts={tierDrafts} cycle={tierCycle} changedCycles={changedCycles} manage={manage} busy={busy} onChange={setTierDrafts} onCycleChange={setTierCycle}/>}
  {error&&<p role="alert" className="text-destructive text-sm break-all">{error}<Button type="button" variant="link" disabled={busy} onClick={()=>{if(!dirty||window.confirm("放弃未保存的配置修改并重新加载？"))onReload();}}>重新加载</Button></p>}
  {manage&&<div className="flex justify-between gap-3 pt-4 border-t">{exists?<Button type="button" variant="outline" disabled={busy} onClick={()=>void remove()}><Trash2/>删除{edit.kind==="group"?"配置组":"选项"}</Button>:<span/>}<Button disabled={busy}><Save/><span>保存{edit.kind==="group"?"配置组":"选项"}</span></Button></div>}
 </form>;
}
