"use client";

import { useRef, useState } from "react";
import { Disc3, RefreshCw } from "lucide-react";
import { api, date, type Service, type NOKVMCDROMImage, type NOKVMCDROMListing } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { useAdminPermission } from "./workspace";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "./ui/dialog";
import { Failure, Loading } from "./common";

export function NOKVMCDROMPanel({service,admin,refresh}:{service:Service;admin:boolean;refresh:()=>void}) {
 const base=`${admin?"/admin":""}/services/${service.id}/cdrom`;
 const resource=useResource<NOKVMCDROMListing>(base,false);
 const [selected,setSelected]=useState<{image:NOKVMCDROMImage;operation:"mount"|"unmount"}|null>(null),[ack,setAck]=useState(false),[busy,setBusy]=useState(false),[error,setError]=useState("");
 const key=useRef<{target:string;value:string}|null>(null);
 const manage=useAdminPermission("services.manage"),canManage=!admin||manage;
 const pending=service.jobs?.some(j=>["pending","running","waiting_manual","waiting_verification"].includes(j.status));
 const blocked=service.status!=="active"||pending||["processing","suspended","network_limited"].includes(service.resource_info?.power??"");
 const mounted=resource.data?.mounted_id??"";
 function close(){if(busy)return;setSelected(null);setAck(false);setError("");}
 async function submit(e:React.FormEvent){
  e.preventDefault();if(!selected||!ack||busy||blocked)return;
  const target=`${selected.operation}:${selected.image.id}`;
  if(key.current?.target!==target)key.current={target,value:crypto.randomUUID()};
  setBusy(true);setError("");
  try{await api(base,{method:"POST",headers:{"Idempotency-Key":key.current.value},body:JSON.stringify({operation:selected.operation,image_id:selected.image.id,risk_acknowledged:true})});key.current=null;setSelected(null);setAck(false);resource.refresh();refresh();}
  catch(e){setError((e as Error).message);refresh();}finally{setBusy(false);}
 }
 return <section aria-label="虚拟光驱" className="border-y my-6 py-6 space-y-5">
  <div className="flex flex-wrap items-center justify-between gap-3"><h2 className="font-semibold flex items-center gap-2"><Disc3 size={18}/>虚拟光驱</h2><Button variant="outline" size="icon" aria-label="刷新光驱目录" onClick={resource.refresh}><RefreshCw size={16}/></Button></div>
  {resource.error?<Failure error={resource.error} retry={resource.refresh}/>:!resource.data?<Loading/>:<>
   <p className="text-sm text-muted-foreground">{mounted?`已挂载 #${mounted}`:"未挂载镜像"} · 更新于 {date(resource.data.fetched_at)}</p>
   {!!pending&&<p role="status" className="text-sm text-muted-foreground">服务操作正在处理或等待核验，请完成后再继续。</p>}
   <div className="divide-y border-y">{resource.data.items.map(image=><article key={image.id} className="py-4 flex flex-wrap items-center justify-between gap-4"><div className="min-w-0 flex-1"><p className="text-sm font-medium break-all">{image.name}</p><p className="text-xs text-muted-foreground break-all">{image.description}</p></div>{canManage&&<Button variant="outline" size="sm" disabled={!!blocked||(!!mounted&&mounted!==image.id)} onClick={()=>{setSelected({image,operation:mounted===image.id?"unmount":"mount"});setAck(false);setError("");}}>{mounted===image.id?"卸载":"挂载"}</Button>}</article>)}{!resource.data.items.length&&<p className="py-8 text-center text-sm text-muted-foreground">暂无可用镜像</p>}</div>
   <p className="text-xs text-muted-foreground">挂载或卸载可能影响主机启动。供应商受理后请核验实际光驱状态和启动顺序。</p>
  </>}
  <Dialog open={!!selected} onOpenChange={value=>{if(!value)close();}}><DialogContent showCloseButton={!busy}><DialogHeader><DialogTitle>{selected?.operation==="mount"?"挂载":"卸载"}光驱</DialogTitle><DialogDescription>此操作可能影响主机启动。提交后请核验实际光驱状态和启动顺序。</DialogDescription></DialogHeader><p className="text-sm break-all">{service.name} · {selected?.image.name}</p><form onSubmit={submit} className="space-y-4"><label className="flex items-start gap-3 text-sm"><Checkbox aria-label="确认光驱启动风险" checked={ack} disabled={busy} onCheckedChange={value=>setAck(value===true)}/><span>我已了解可能影响主机启动，并会核验实际状态</span></label>{error&&<p role="alert" className="text-sm text-destructive break-all">{error}</p>}<DialogFooter><Button type="button" variant="outline" disabled={busy} onClick={close}>取消</Button><Button type="submit" disabled={busy||!!blocked||!ack}>{busy?"提交中":`确认${selected?.operation==="mount"?"挂载":"卸载"}`}</Button></DialogFooter></form></DialogContent></Dialog>
 </section>;
}
