"use client";

import { Plus, Trash2 } from "lucide-react";
import { type NOKVMTemplate } from "@/lib/api";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { catalogSelectStyle } from "./catalog-admin";

export function NOKVMTemplateFields({templates,onChange}:{templates:NOKVMTemplate[];onChange:(templates:NOKVMTemplate[])=>void}) {
  function update(index:number,values:Partial<NOKVMTemplate>){onChange(templates.map((item,i)=>i===index?{...item,...values}:item));}
  return <div className="border-t pt-5 space-y-4">
    <div className="flex flex-wrap items-center justify-between gap-3"><h3 className="font-medium">可重装镜像</h3><Button type="button" variant="outline" disabled={templates.length>=100} onClick={()=>onChange([...templates,{id:0,name:"",group:"",login:"root"}])}><Plus size={16}/>添加镜像</Button></div>
    {!templates.length&&<p className="text-sm text-muted-foreground">未配置可重装镜像</p>}
    {templates.map((template,index)=><div key={index} className="grid gap-3 border-b pb-4 sm:grid-cols-2">
      <label className="grid gap-2 text-sm">镜像 ID<Input aria-label={`镜像 ${index+1} ID`} required type="number" step={1} min={1} max={2147483647} value={template.id} onChange={e=>update(index,{id:Number(e.target.value)})}/></label>
      <label className="grid gap-2 text-sm">镜像名称<Input aria-label={`镜像 ${index+1} 名称`} required maxLength={120} value={template.name} onChange={e=>update(index,{name:e.target.value})}/></label>
      <label className="grid gap-2 text-sm">系统分组<Input aria-label={`镜像 ${index+1} 分组`} maxLength={80} value={template.group} onChange={e=>update(index,{group:e.target.value})}/></label>
      <div className="flex items-end gap-2"><label className="grid gap-2 text-sm flex-1 min-w-0">登录用户名<select className={catalogSelectStyle} aria-label={`镜像 ${index+1} 登录用户名`} value={template.login} onChange={e=>update(index,{login:e.target.value as NOKVMTemplate["login"]})}><option value="root">root</option><option value="administrator">administrator</option></select></label><Button type="button" variant="ghost" size="icon" title="删除镜像" aria-label={`删除镜像 ${index+1}`} onClick={()=>onChange(templates.filter((_,i)=>i!==index))}><Trash2 size={16}/></Button></div>
    </div>)}
  </div>;
}
