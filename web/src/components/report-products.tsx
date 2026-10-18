"use client";

import { useEffect, useState } from "react";
import { Package } from "lucide-react";
import { money } from "@/lib/api";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { ReportPanel } from "./report-shared";
import { ResourcePagination, ResourceTable } from "./resource-table";
import { Stat } from "./resource-page";

type ProductIncome = {product_id:number;name:string;new_order_amount_cents:number;new_order_count:number;new_quantity:number;renewal_amount_cents:number;renewal_order_count:number;renewal_quantity:number;total_amount_cents:number};
type ProductGroup = {category_id:number;category_name:string;products:ProductIncome[];new_order_amount_cents:number;renewal_amount_cents:number;total_amount_cents:number;new_quantity:number;renewal_quantity:number};
type ProductReport = {groups:ProductGroup[];total:number;limit:number;offset:number;summary:ProductIncome;options:{categories:{id:number;name:string}[];products:{id:number;name:string;category_id:number}[]}};
type Filters={category:string;product:string;q:string;offset:number};
const readFilters=():Filters=>{
  const query=new URLSearchParams(typeof window==="undefined"?"":window.location.search);
  const id=(name:string,minimum:number)=>{const raw=query.get(name)??"";const value=Number(raw);return /^\d+$/.test(raw)&&Number.isSafeInteger(value)&&value>=minimum?String(value):""};
  const offset=Number(query.get("product_offset"));
  return {category:id("product_category",0),product:id("product_id",1),q:Array.from(query.get("product_q")??"").slice(0,120).join(""),offset:Number.isSafeInteger(offset)&&offset>=0?offset:0};
};

export function ProductIncomeGroups({year,month,exportDisabled=false}:{year:number;month:number;exportDisabled?:boolean}) {
  const [filters,setFilters]=useState<Filters>(readFilters);
  const [draft,setDraft]=useState<Filters>(readFilters);
  useEffect(()=>{const restore=()=>{const next=readFilters();setFilters(next);setDraft(next)};window.addEventListener("popstate",restore);return()=>window.removeEventListener("popstate",restore)},[]);
  function apply(next:Filters) {
    const query=new URLSearchParams(window.location.search);
    for(const [key,value] of Object.entries({product_category:next.category,product_id:next.product,product_q:next.q,product_offset:next.offset?String(next.offset):""})) {if(value) query.set(key,value);else query.delete(key)}
    window.history.pushState(null,"",`${window.location.pathname}?${query}`);
    setFilters(next);setDraft(next);
  }
  const query=new URLSearchParams({year:String(year),month:String(month),limit:"10",offset:String(filters.offset)});
  if(filters.category) query.set("category_id",filters.category);
  if(filters.product) query.set("product_id",filters.product);
  if(filters.q) query.set("q",filters.q);
  const path=`/admin/reports/product-income/groups?${query}`;
  const changed=exportDisabled||draft.category!==filters.category||draft.product!==filters.product||draft.q!==filters.q;
  return <><div className="mb-3"><Button type="button" variant="outline" onClick={()=>apply({category:"",product:"",q:"",offset:0})}>重置产品筛选</Button></div><ReportPanel<ProductReport> key={path} path={path} filename={`product-income-${year}-${month}.csv`} exportDisabled={changed} title="产品分类收入" description="折后成交收入按商品分类统计；新购件数取购买快照，续费数量取账单项目。退款现金支出另列于年度收支，成交件数保留历史事实。CSV 导出全部匹配分类。">
    {data=><>
      <form className="mb-5 flex flex-wrap items-end gap-3" onSubmit={event=>{event.preventDefault();apply({...draft,offset:0})}}>
        <label className="space-y-1 text-sm">商品分类<select aria-label="商品分类" className="h-9 w-full rounded-md border bg-background px-3" value={draft.category} onChange={event=>setDraft({...draft,category:event.target.value,product:""})}><option value="">全部分类</option>{data.options.categories.map(item=><option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
        <label className="space-y-1 text-sm">产品<select aria-label="产品筛选" className="h-9 w-full max-w-64 rounded-md border bg-background px-3" value={draft.product} onChange={event=>setDraft({...draft,product:event.target.value})}><option value="">全部产品</option>{data.options.products.filter(item=>!draft.category||String(item.category_id)===draft.category).map(item=><option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
        <label className="space-y-1 text-sm">产品名称<Input aria-label="产品名称搜索" value={draft.q} maxLength={120} onChange={event=>setDraft({...draft,q:event.target.value})}/></label>
        <Button type="submit">筛选产品</Button>
      </form>
      <div className="stat-grid mb-5"><Stat icon={Package} label="筛选结果总收入" value={money(data.summary.total_amount_cents)}/><Stat icon={Package} label="新购件数" value={data.summary.new_quantity}/><Stat icon={Package} label="续费项目数" value={data.summary.renewal_quantity}/></div>
      {!data.groups.length&&<p className="py-10 text-center text-muted-foreground">暂无匹配的产品收入</p>}
      <div className="space-y-3">{data.groups.map(group=><details className="rounded-md border p-3" key={group.category_id} open>
        <summary className="cursor-pointer font-medium">{group.category_name} · 合计 {money(group.total_amount_cents)} · 新购 {group.new_quantity} 件 · 续费 {group.renewal_quantity} 项</summary>
        <ResourceTable items={group.products} rowKey={item=>item.product_id} className="mt-3 overflow-auto" columns={[
          {header:"产品",render:item=>item.name},{header:"新购收入",render:item=>money(item.new_order_amount_cents)},{header:"新购件 / 单",render:item=>`${item.new_quantity} 件 / ${item.new_order_count} 单`},
          {header:"续费收入",render:item=>money(item.renewal_amount_cents)},{header:"续费项 / 账单",render:item=>`${item.renewal_quantity} 项 / ${item.renewal_order_count} 单`},{header:"合计",render:item=>money(item.total_amount_cents)},
        ]}/>
      </details>)}</div>
      <ResourcePagination total={data.total} limit={data.limit} offset={data.offset} unit="分类" onOffsetChange={offset=>apply({...filters,offset})}/>
      <p className="mt-3 text-xs text-muted-foreground">同一订单包含多个产品时，各产品分别计订单数；分类汇总按件数计，不将产品订单数相加当作去重订单数。</p>
    </>}
  </ReportPanel></>;
}
