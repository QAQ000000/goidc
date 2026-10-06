"use client";

import { useRef, useState } from "react";
import { Globe2, Network, Plus, RefreshCw, Trash2 } from "lucide-react";
import { api, date, type Service, type NOKVMNATListing, type NOKVMNATItem } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { useAdminPermission } from "./workspace";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Checkbox } from "./ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "./ui/dialog";
import { Failure, Loading } from "./common";

type Kind="acl"|"web";
type Confirmation={operation:"create"|"delete";item?:NOKVMNATItem};
export function NOKVMNATPanel({service,admin,refresh}:{service:Service;admin:boolean;refresh:()=>void}) {
 const [kind,setKind]=useState<Kind>("acl"),[confirmation,setConfirmation]=useState<Confirmation|null>(null);
 const [name,setName]=useState(""),[protocol,setProtocol]=useState("tcp"),[domain,setDomain]=useState(""),[interiorPort,setInteriorPort]=useState(""),[ack,setAck]=useState(false),[match,setMatch]=useState(""),[busy,setBusy]=useState(false),[error,setError]=useState("");
 const key=useRef<{target:string;value:string}|null>(null);
 const base=`${admin?"/admin":""}/services/${service.id}/nat/${kind}`;
 const resource=useResource<NOKVMNATListing>(base,false);
 const manage=useAdminPermission("services.manage"),canManage=!admin||manage;
 const pending=service.jobs?.some(j=>["pending","running","waiting_manual","waiting_verification"].includes(j.status));
 const blocked=service.status!=="active"||pending||["processing","suspended","network_limited"].includes(service.resource_info?.power??"");
 const label=kind==="acl"?"端口转发":"NAT 建站";
 const validPort=/^[1-9][0-9]{0,4}$/.test(interiorPort)&&Number(interiorPort)<=65535;
 const validDomain=/^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/i.test(domain)&&domain.length<=253;
 const validName=name.trim()===name&&name.length>0&&name.length<=255;
 const ready=confirmation?.operation==="delete"?match===service.name:kind==="acl"?validName&&validPort:validDomain&&validPort;
 function open(value:Confirmation){setConfirmation(value);setName("");setProtocol("tcp");setDomain("");setInteriorPort("");setAck(false);setMatch("");setError("");}
 function close(){if(!busy)setConfirmation(null);}
 async function submit(e:React.FormEvent){
  e.preventDefault();if(!confirmation||!ready||!ack||blocked||busy)return;
  const payload=confirmation.operation==="delete"?{operation:"delete",point_id:confirmation.item!.id,risk_acknowledged:true}:{operation:"create",point_id:"",...(kind==="acl"?{name,type:protocol,interior_port:Number(interiorPort)}:{domain,exterior_port:80,interior_port:Number(interiorPort)}),risk_acknowledged:true};
  const target=JSON.stringify([kind,payload]);if(key.current?.target!==target)key.current={target,value:crypto.randomUUID()};
  setBusy(true);setError("");
  try{await api(base,{method:"POST",headers:{"Idempotency-Key":key.current.value},body:JSON.stringify(payload)});key.current=null;setConfirmation(null);resource.refresh();refresh();}
  catch(e){setError((e as Error).message);refresh();}finally{setBusy(false);}
 }
 return <section aria-label="NAT 服务" className="border-y my-6 py-6 space-y-5">
  <div className="flex flex-wrap items-center justify-between gap-3"><h2 className="font-semibold flex items-center gap-2"><Network size={18}/>NAT 服务</h2><Button size="icon" variant="outline" title="刷新 NAT 列表" aria-label="刷新 NAT 列表" onClick={resource.refresh}><RefreshCw size={16}/></Button></div>
  <div className="flex flex-wrap items-center justify-between gap-3"><div role="group" aria-label="NAT 类型" className="flex gap-2"><Button variant={kind==="acl"?"secondary":"ghost"} aria-pressed={kind==="acl"} onClick={()=>setKind("acl")}><Network size={16}/>端口转发</Button><Button variant={kind==="web"?"secondary":"ghost"} aria-pressed={kind==="web"} onClick={()=>setKind("web")}><Globe2 size={16}/>建站</Button></div>{canManage&&<Button variant="outline" disabled={!!blocked||!resource.data||resource.data.limit===0||resource.data.items.length>=resource.data.limit} onClick={()=>open({operation:"create"})}><Plus size={16}/>创建</Button>}</div>
  {resource.error?<Failure error={resource.error} retry={resource.refresh}/>:!resource.data?<Loading/>:<><p className="text-sm text-muted-foreground">{resource.data.limit===0?`此服务未购买${label}`:`${label} ${resource.data.items.length} / ${resource.data.limit} · 更新于 ${date(resource.data.fetched_at)}`}</p>{!!pending&&<p role="status" className="text-sm text-muted-foreground">服务操作正在处理或等待核验，请完成后再继续。</p>}<div className="divide-y border-y">{resource.data.items.map(item=><article key={item.id} className="py-4 flex flex-wrap items-center justify-between gap-4"><div className="min-w-0 flex-1"><p className="font-medium text-sm break-all">{kind==="acl"?item.name:item.domain}</p><p className="text-xs text-muted-foreground mt-1 break-all">{kind==="acl"?`${item.type.toUpperCase()} · `:""}{item.exterior_port} → {item.interior_port} · #{item.id}</p></div>{canManage&&<Button variant="ghost" size="icon" title={`删除 ${kind==="acl"?item.name:item.domain}`} aria-label={`删除 ${kind==="acl"?item.name:item.domain}`} disabled={!!blocked} onClick={()=>open({operation:"delete",item})}><Trash2 size={16}/></Button>}</article>)}{!resource.data.items.length&&resource.data.limit>0&&<p className="py-8 text-center text-sm text-muted-foreground">暂无{label}</p>}</div><p className="text-xs text-muted-foreground">操作提交后需管理员核验供应商实际结果，核验前无法提交其他服务动作。</p></>}
  <Dialog open={!!confirmation} onOpenChange={value=>{if(!value)close();}}><DialogContent showCloseButton={!busy} className="max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>{confirmation?.operation==="delete"?"删除":"创建"}{label}</DialogTitle><DialogDescription>{confirmation?.operation==="delete"?"删除可能立即中断当前连接，请确认目标及受影响业务。":"创建后外部连接可能公开服务，请确认内部端口已做好访问控制。"}</DialogDescription></DialogHeader><p className="text-sm break-all">{service.name} · #{service.id}{confirmation?.item&&<> · {kind==="acl"?confirmation.item.name:confirmation.item.domain}</>}</p><form onSubmit={submit} className="space-y-4"><fieldset disabled={busy} className="space-y-4">{confirmation?.operation==="create"?<>{kind==="acl"?<><label className="grid gap-2 text-sm">服务名称<Input required maxLength={255} value={name} onChange={e=>setName(e.target.value)}/></label><label className="grid gap-2 text-sm">协议<select className="h-9 rounded-md border px-3" value={protocol} onChange={e=>setProtocol(e.target.value)}><option value="tcp">TCP</option><option value="udp">UDP</option><option value="all">TCP 和 UDP</option></select></label></>:<><label className="grid gap-2 text-sm">域名<Input required type="text" maxLength={253} autoCapitalize="none" value={domain} onChange={e=>setDomain(e.target.value)}/></label><p className="text-sm text-muted-foreground">外网端口 80 · HTTP</p></>}<label className="grid gap-2 text-sm">内网端口<Input required type="number" min={1} max={65535} value={interiorPort} onChange={e=>setInteriorPort(e.target.value)}/></label></>:<label className="grid gap-2 text-sm">输入服务名称确认<Input required autoComplete="off" value={match} onChange={e=>setMatch(e.target.value)}/></label>}<label className="flex items-start gap-3 text-sm"><Checkbox checked={ack} onCheckedChange={v=>setAck(v===true)}/><span>已确认{confirmation?.operation==="delete"?"删除将中断此映射或网站":"公开端口与访问控制"}的风险</span></label></fieldset>{error&&<p role="alert" className="text-sm text-destructive break-all">{error}</p>}<DialogFooter><Button type="button" variant="outline" disabled={busy} onClick={close}>取消</Button><Button type="submit" variant={confirmation?.operation==="delete"?"destructive":"default"} disabled={!ready||!ack||!!blocked||busy}>{busy?"提交中":"确认提交"}</Button></DialogFooter></form></DialogContent></Dialog>
 </section>;
}
