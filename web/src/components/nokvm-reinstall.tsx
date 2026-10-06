"use client";

import { useRef, useState } from "react";
import { Disc3, RefreshCw } from "lucide-react";
import { api, type NOKVMTemplate, type Service } from "@/lib/api";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Checkbox } from "./ui/checkbox";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "./ui/dialog";
import { catalogSelectStyle } from "./catalog-admin";

export function NOKVMReinstall({service,admin,disabled,refresh}:{service:Service;admin:boolean;disabled:boolean;refresh:()=>void}) {
  const [open,setOpen]=useState(false),[loading,setLoading]=useState(false),[busy,setBusy]=useState(false);
  const [templates,setTemplates]=useState<NOKVMTemplate[]>([]),[selected,setSelected]=useState("");
  const [ack,setAck]=useState(false),[name,setName]=useState(""),[error,setError]=useState("");
  const key=useRef<{template:number;key:string}|null>(null);
  const base=`${admin?"/admin":""}/services/${service.id}`;
  function close(){if(busy)return;setOpen(false);setAck(false);setName("");setSelected("");setError("");}
  async function load(){setLoading(true);setError("");try{setTemplates(await api<NOKVMTemplate[]>(`${base}/reinstall-templates`));}catch(e){setError((e as Error).message);}finally{setLoading(false);}}
  async function submit(e:React.FormEvent){
    e.preventDefault();
    if(busy||disabled||!ack||name!==service.name||!selected)return;
    const template=Number(selected);
    if(key.current?.template!==template)key.current={template,key:crypto.randomUUID()};
    setBusy(true);setError("");
    try{await api(`${base}/reinstall`,{method:"POST",headers:{"Idempotency-Key":key.current.key},body:JSON.stringify({template_id:template,data_loss_acknowledged:true})});key.current=null;setOpen(false);setSelected("");setName("");setAck(false);refresh();}catch(e){setError((e as Error).message);refresh();}finally{setBusy(false);}
  }
  return <>
    <Button variant="outline" disabled={disabled} onClick={()=>{setOpen(true);void load();}}><Disc3 size={16}/>重装系统</Button>
    <Dialog open={open} onOpenChange={value=>{if(!value)close();}}><DialogContent showCloseButton={!busy} className="max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>重装系统</DialogTitle><DialogDescription>重装将清除系统盘数据并中断服务，无法撤销。请先完成备份，数据盘也可能受供应商重装策略影响。</DialogDescription></DialogHeader>
      <p className="text-sm break-all">{service.name} · #{service.id}</p>
      <form onSubmit={submit} className="space-y-4"><fieldset disabled={busy||loading} className="space-y-4">
        <label className="grid gap-2 text-sm">系统镜像<select aria-label="系统镜像" required className={catalogSelectStyle} value={selected} onChange={e=>{setSelected(e.target.value);setAck(false);setName("");}}><option value="">{loading?"加载中":"选择系统镜像"}</option>{templates.map(t=><option key={t.id} value={t.id}>{t.group?`${t.group} / `:""}{t.name}</option>)}</select></label>
        {!loading&&!templates.length&&<p role="status" className="text-sm text-muted-foreground">暂无可用重装镜像，请联系管理员。</p>}
        <label className="grid gap-2 text-sm">输入服务名称确认<Input aria-label="输入服务名称确认" required autoComplete="off" value={name} onChange={e=>setName(e.target.value)}/></label>
        <label className="flex items-start gap-3 text-sm"><Checkbox aria-label="确认重装数据清除风险" checked={ack} onCheckedChange={v=>setAck(v===true)}/><span>已备份重要数据，确认重装并承担数据清除风险</span></label>
      </fieldset>{error&&<p role="alert" className="text-sm text-destructive break-all">{error}</p>}
      <DialogFooter><Button type="button" variant="outline" disabled={busy||loading} onClick={()=>void load()}><RefreshCw size={16}/>刷新镜像</Button><Button type="button" variant="outline" disabled={busy} onClick={close}>取消</Button><Button type="submit" variant="destructive" disabled={busy||loading||disabled||!selected||!ack||name!==service.name}>{busy?"提交中":"确认重装"}</Button></DialogFooter></form>
    </DialogContent></Dialog>
  </>;
}
