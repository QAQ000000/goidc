"use client";

import { useRef, useState } from "react";
import { Archive, Camera, Plus, RefreshCw, RotateCcw, Trash2 } from "lucide-react";
import { api, date, type Service, type NOKVMStorageListing, type NOKVMStoragePoint } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { useAdminPermission } from "./workspace";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Checkbox } from "./ui/checkbox";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "./ui/dialog";
import { Failure, Loading } from "./common";

type Kind="snapshot"|"backup";
type Confirmation={operation:"create"|"delete"|"restore";point?:NOKVMStoragePoint};
export function NOKVMStoragePanel({service,admin,refresh}:{service:Service;admin:boolean;refresh:()=>void}){
 const [kind,setKind]=useState<Kind>("snapshot");
 const base=`${admin?"/admin":""}/services/${service.id}/storage/${kind}`;
 const resource=useResource<NOKVMStorageListing>(base,false);
 const [confirmation,setConfirmation]=useState<Confirmation|null>(null),[name,setName]=useState(""),[ack,setAck]=useState(false),[busy,setBusy]=useState(false),[error,setError]=useState("");
 const key=useRef<{target:string;value:string}|null>(null);
 const manage=useAdminPermission("services.manage"),canManage=!admin||manage;
 const pending=service.jobs?.some(j=>["pending","running","waiting_manual","waiting_verification"].includes(j.status));
 const blocked=service.status!=="active"||pending||["processing","suspended","network_limited"].includes(service.resource_info?.power??"");
 const label=kind==="snapshot"?"快照":"备份";
 const operation=confirmation?.operation;
 const verb=operation==="create"?"创建":operation==="delete"?"删除":"恢复";
 function open(value:Confirmation){setConfirmation(value);setName("");setAck(false);setError("");}
 function close(){if(busy)return;setConfirmation(null);setName("");setAck(false);setError("");}
 async function submit(e:React.FormEvent){
  e.preventDefault();if(!confirmation||busy||blocked||!ack||(operation!=="create"&&name!==service.name))return;
  const target=`${kind}:${operation}:${confirmation.point?.id??""}`;
  if(key.current?.target!==target)key.current={target,value:crypto.randomUUID()};
  setBusy(true);setError("");
  try{await api(base,{method:"POST",headers:{"Idempotency-Key":key.current.value},body:JSON.stringify({operation,point_id:confirmation.point?.id??"",risk_acknowledged:true})});key.current=null;setConfirmation(null);setAck(false);setName("");resource.refresh();refresh();}
  catch(e){setError((e as Error).message);refresh();}finally{setBusy(false);}
 }
 return <section aria-label="快照与备份" className="border-y my-6 py-6 space-y-5">
  <div className="flex flex-wrap justify-between items-center gap-3"><h2 className="font-semibold flex items-center gap-2"><Archive size={18}/>快照与备份</h2><Button variant="outline" size="icon" aria-label="刷新还原点" onClick={resource.refresh}><RefreshCw size={16}/></Button></div>
  <div className="flex flex-wrap justify-between gap-3"><div role="group" aria-label="还原点类型" className="flex gap-2"><Button variant={kind==="snapshot"?"secondary":"ghost"} aria-pressed={kind==="snapshot"} onClick={()=>setKind("snapshot")}><Camera size={16}/>快照</Button><Button variant={kind==="backup"?"secondary":"ghost"} aria-pressed={kind==="backup"} onClick={()=>setKind("backup")}><Archive size={16}/>备份</Button></div>{canManage&&<Button variant="outline" disabled={!!blocked||!resource.data||resource.data.limit===0||resource.data.items.length>=resource.data.limit} onClick={()=>open({operation:"create"})}><Plus size={16}/>创建{label}</Button>}</div>
  {resource.error?<Failure error={resource.error} retry={resource.refresh}/>:!resource.data?<Loading/>:<>
   <p className="text-sm text-muted-foreground">{resource.data.limit===0?`此服务未启用${label}`:`${label} ${resource.data.items.length} / ${resource.data.limit} · 更新于 ${date(resource.data.fetched_at)}`}</p>
   {!!pending&&<p role="status" className="text-sm text-muted-foreground">服务操作正在处理或等待核验，请完成后再继续。</p>}
   <div className="divide-y border-y">{resource.data.items.map(point=><article key={point.id} className="py-4 flex flex-wrap items-center justify-between gap-4"><div className="min-w-0 flex-1"><p className="text-sm font-medium break-all">{point.name}</p><p className="text-xs text-muted-foreground mt-1 break-all">{date(point.created_at)} · #{point.id}</p></div>{canManage&&<div className="flex gap-2"><Button variant="outline" size="sm" disabled={!!blocked} aria-label={`恢复 ${point.name}`} onClick={()=>open({operation:"restore",point})}><RotateCcw size={14}/>恢复</Button><Button variant="ghost" size="sm" disabled={!!blocked} aria-label={`删除 ${point.name}`} onClick={()=>open({operation:"delete",point})}><Trash2 size={14}/>删除</Button></div>}</article>)}{!resource.data.items.length&&resource.data.limit>0&&<p className="py-8 text-center text-sm text-muted-foreground">暂无{label}</p>}</div>
   <p className="text-xs text-muted-foreground">创建、删除和恢复均需核验供应商实际结果。恢复会覆盖主机数据并中断连接，请先备份。</p>
  </>}
  <Dialog open={!!confirmation} onOpenChange={value=>{if(!value)close();}}><DialogContent showCloseButton={!busy} className="max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>{verb}{label}</DialogTitle><DialogDescription>{operation==="restore"?"恢复会将主机数据回退到所选还原点，当前数据可能丢失；恢复后的系统与密码需重新核验。":operation==="delete"?"删除后该还原点无法再用于恢复，请确认已保留其他备份。":"创建可能短暂影响磁盘性能，提交后请等待操作结果核验。"}</DialogDescription></DialogHeader><p className="text-sm break-all">{service.name} · #{service.id}{confirmation?.point&&<> · {confirmation.point.name}</>}</p><form onSubmit={submit} className="space-y-4"><fieldset disabled={busy} className="space-y-4">{operation!=="create"&&<label className="grid gap-2 text-sm">输入服务名称确认<Input aria-label="输入服务名称确认" required autoComplete="off" value={name} onChange={e=>setName(e.target.value)}/></label>}<label className="flex items-start gap-3 text-sm"><Checkbox aria-label="确认还原点操作风险" checked={ack} onCheckedChange={value=>setAck(value===true)}/><span>{operation==="restore"?"已备份重要数据，确认数据回退与服务中断风险":operation==="delete"?"已确认删除该还原点，无法撤销":"已确认创建操作对主机运行的影响"}</span></label></fieldset>{error&&<p role="alert" className="text-sm text-destructive break-all">{error}</p>}<DialogFooter><Button type="button" variant="outline" disabled={busy} onClick={close}>取消</Button><Button type="submit" variant={operation==="create"?"default":"destructive"} disabled={busy||!!blocked||!ack||(operation!=="create"&&name!==service.name)}>{busy?"提交中":`确认${verb}${label}`}</Button></DialogFooter></form></DialogContent></Dialog>
 </section>;
}
