"use client";

import { useEffect, useRef, useState } from "react";
import { Power, PowerOff, RotateCw, RefreshCw, OctagonX, Zap, Pause, Play, Trash2, KeyRound } from "lucide-react";
import { api, type DeliveryConfig, type NOKVMResources, type Service } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { useAdminPermission } from "./workspace";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Checkbox } from "./ui/checkbox";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "./ui/dialog";
import { catalogSelectStyle } from "./catalog-admin";
import { Failure, Loading } from "./common";
import { NOKVMPasswordReset } from "./nokvm-password";
import { NOKVMReinstall } from "./nokvm-reinstall";
import { NOKVMTemplateFields } from "./nokvm-templates";

export const defaultNOKVMDelivery:DeliveryConfig={profile_id:"",resources:{areas_id:0,core:2,cpu_mode:"host",memory:2048,data_disk_size:40,net_out:100,net_in:100,snapshoot:0,backups:0,templates_id:0,ip_num:1,flow_limit:0,nat_acl_limit:0,nat_web_limit:0,nodes_id:0,login:"root"}};
const resourceFields:{key:Exclude<keyof NOKVMResources,"login"|"cpu_mode">;label:string;min:number;max:number}[]=[
  {key:"areas_id",label:"区域 ID",min:1,max:2147483647},{key:"templates_id",label:"系统模板 ID",min:1,max:2147483647},
  {key:"core",label:"CPU 核数",min:1,max:1024},{key:"memory",label:"内存（MB）",min:128,max:16777216},
  {key:"data_disk_size",label:"数据盘（GB）",min:0,max:1048576},{key:"net_out",label:"出站带宽（Mbps）",min:0,max:100000000},
  {key:"net_in",label:"入站带宽（Mbps）",min:0,max:100000000},{key:"flow_limit",label:"流量限额（GB，0 不限）",min:0,max:100000000},
  {key:"ip_num",label:"IP 数量",min:0,max:100000000},{key:"snapshoot",label:"快照数量",min:0,max:100000000},
  {key:"backups",label:"备份数量",min:0,max:100000000},{key:"nodes_id",label:"节点 ID（0 自动分配）",min:0,max:2147483647},
  {key:"nat_acl_limit",label:"NAT 转发数量",min:0,max:100000000},{key:"nat_web_limit",label:"NAT 网站数量",min:0,max:100000000},
];

export function NOKVMProductFields({value,onChange}:{value:DeliveryConfig;onChange:(value:DeliveryConfig)=>void}) {
  const profiles=useResource<{id:string;name:string;type:string}[]>("/admin/provision-profiles",false);
  if(profiles.error)return <Failure error={profiles.error} retry={profiles.refresh}/>;
  if(!profiles.data)return <Loading/>;
  return <section className="rounded-md border p-5 space-y-5"><div><h2 className="font-semibold">NOKVM 资源交付</h2><p className="text-xs text-muted-foreground mt-2">新购服务按此固定规格开通。修改商品后，已购服务继续使用下单时的交付规格。</p></div>
    {!profiles.data.length&&<p role="alert" className="text-sm text-destructive">暂无可用供应商，请联系系统管理员配置。</p>}
    <div className="grid sm:grid-cols-2 gap-4"><label className="grid gap-2 text-sm">NOKVM 供应商<select required aria-label="NOKVM 供应商" className={catalogSelectStyle} value={value.profile_id} onChange={e=>onChange({...value,profile_id:e.target.value})}><option value="">选择供应商</option>{!profiles.data.some(p=>p.id===value.profile_id)&&value.profile_id&&<option value={value.profile_id}>{value.profile_id}（暂不可用）</option>}{profiles.data.map(p=><option key={p.id} value={p.id}>{p.name}</option>)}</select></label><label className="grid gap-2 text-sm">系统登录用户名<select aria-label="系统登录用户名" className={catalogSelectStyle} value={value.resources.login} onChange={e=>onChange({...value,resources:{...value.resources,login:e.target.value as NOKVMResources["login"]}})}><option value="root">root（Linux）</option><option value="administrator">administrator（Windows）</option></select></label>
    {resourceFields.map(field=><label key={field.key} className="grid gap-2 text-sm">{field.label}<Input aria-label={field.label} type="number" step={1} min={field.min} max={field.max} required value={value.resources[field.key]} onChange={e=>onChange({...value,resources:{...value.resources,[field.key]:Number(e.target.value)}})}/></label>)}<label className="grid gap-2 text-sm">CPU 模式<Input aria-label="CPU 模式" maxLength={64} value={value.resources.cpu_mode} onChange={e=>onChange({...value,resources:{...value.resources,cpu_mode:e.target.value}})}/></label></div>
    <NOKVMTemplateFields templates={value.templates??[]} onChange={templates=>onChange({...value,templates})}/>
  </section>;
}

const powers:Record<string,string>={on:"开机",off:"关机",processing:"处理中",suspended:"上游暂停",network_limited:"上游流量限制"};
 export const serviceActionLabels:Record<string,string>={create:"开通资源",suspend:"暂停服务",resume:"恢复服务",terminate:"终止资源",sync:"刷新资源",upgrade:"变更配置",power_on:"开机",power_off:"关机",reboot:"重启",force_off:"强制关机",force_reboot:"强制重启",reset_password:"重置系统密码",rescue_system:"切换救援系统",exit_rescue_system:"退出救援系统",reinstall:"重装系统",snapshot_create:"创建快照",snapshot_delete:"删除快照",snapshot_restore:"恢复快照",backup_create:"创建备份",backup_delete:"删除备份",backup_restore:"恢复备份",nat_acl_create:"创建端口转发",nat_acl_delete:"删除端口转发",nat_web_create:"创建 NAT 建站",nat_web_delete:"删除 NAT 建站",cdrom_mount:"挂载光驱",cdrom_unmount:"卸载光驱"};
const powerActions=[{action:"power_on",icon:Power},{action:"power_off",icon:PowerOff},{action:"reboot",icon:RotateCw},{action:"force_off",icon:OctagonX},{action:"force_reboot",icon:Zap}];
export function isPowerAction(action:string){return powerActions.some(item=>item.action===action);}
export function NOKVMServiceDelivery({service,admin,refresh}:{service:Service;admin:boolean;refresh:()=>void}) {
  const manage=useAdminPermission("services.manage");
  const [busy,setBusy]=useState(false),[error,setError]=useState("");
  const [credentials,setCredentials]=useState<{username:string;password:string}|null>(null);
  const [confirmation,setConfirmation]=useState<string|null>(null),[acknowledged,setAcknowledged]=useState(false);
  const keys=useRef<Record<string,string>>({});
  useEffect(()=>{if(!credentials)return;const timer=window.setTimeout(()=>setCredentials(null),60000);return ()=>window.clearTimeout(timer);},[credentials]);
  const inflight=service.jobs?.some(j=>["pending","running","waiting_verification"].includes(j.status));
  const verification=service.jobs?.find(j=>j.status==="waiting_verification");
  const passwordPending=service.jobs?.some(j=>["reset_password","reinstall","snapshot_restore","backup_restore"].includes(j.action)&&["pending","running","waiting_verification"].includes(j.status));
  const forced=confirmation==="force_off"||confirmation==="force_reboot";
  const power=service.resource_info?.power;
  const powerBlocked=busy||inflight||power==="processing"||power==="suspended"||power==="network_limited";
  function confirm(action:string){setAcknowledged(false);setConfirmation(action);}
  async function action(action:string){
    if(busy||inflight)return;
    setBusy(true);setError("");keys.current[action]??=crypto.randomUUID();
    try{await api(`${admin?"/admin":""}/services/${service.id}/actions`,{method:"POST",headers:{"Idempotency-Key":keys.current[action]},body:JSON.stringify({action})});delete keys.current[action];setConfirmation(null);refresh();}catch(e){setError((e as Error).message);}finally{setBusy(false);}
  }
  async function reveal(){setBusy(true);setError("");try{setCredentials(await api(`${admin?"/admin":""}/services/${service.id}/credentials`,{method:"POST",body:"{}"}));}catch(e){setError((e as Error).message);}finally{setBusy(false);}}
  return <section className="data-surface p-6 mb-6 space-y-4"><div><h2 className="font-semibold">NOKVM 交付</h2><p className="text-sm text-muted-foreground mt-2 break-all">{service.resource_info?.name||"等待资源开通"}{service.resource_info?.power&&` · ${powers[service.resource_info.power]??service.resource_info.power}`}</p>{!!service.resource_info?.ips?.length&&<p className="font-mono text-sm mt-3 break-all">{service.resource_info.ips.join(" · ")}</p>}</div>
    {service.installed_os?.name&&<p className="text-sm break-all">已核验系统：{service.installed_os.name}</p>}
    {verification?.request_payload?.name&&<p className="text-sm break-all">待核验镜像：{verification.request_payload.name}</p>}
    {verification&&<p role="status" className="rounded-md border border-amber-500/40 bg-amber-500/5 p-4 text-sm">供应商操作结果待核验，相关操作已暂停。{admin?"请先核对供应商实际资源，再登记核验结果。":"管理员核验后将继续处理，请勿重复提交本次操作。"}</p>}
    {(!admin||manage)&&<div className="flex flex-wrap gap-2">
      {service.status==="active"&&powerActions.map(({action:command,icon:Icon})=><Button key={command} variant="outline" title={serviceActionLabels[command]} disabled={powerBlocked||(command==="power_on"?power==="on":power==="off")} onClick={()=>command==="power_on"?void action(command):confirm(command)}><Icon size={16}/>{serviceActionLabels[command]}</Button>)}
      {service.status==="active"&&<NOKVMPasswordReset service={service} admin={admin} disabled={!!powerBlocked} refresh={refresh}/>}
      {service.status==="active"&&<NOKVMReinstall service={service} admin={admin} disabled={!!powerBlocked} refresh={refresh}/>}
      {admin&&["queued","failed","provisioning"].includes(service.status)&&!service.upstream_id&&<Button variant="outline" disabled={busy||inflight} onClick={()=>void action("create")}>开通资源</Button>}
      {service.status==="active"&&<Button variant="outline" disabled={busy||inflight} onClick={()=>void action("suspend")}><Pause size={16}/>暂停服务</Button>}
      {service.status==="suspended"&&<Button variant="outline" disabled={busy||inflight} onClick={()=>void action("resume")}><Play size={16}/>恢复服务</Button>}
      {["active","suspended"].includes(service.status)&&<Button variant="outline" disabled={busy||inflight} onClick={()=>void action("sync")}><RefreshCw size={16}/>刷新资源</Button>}
      {service.upstream_id&&["active","suspended","provisioning","failed"].includes(service.status)&&<Button variant="outline" disabled={busy||inflight} onClick={()=>confirm("terminate")}><Trash2 size={16}/>终止资源</Button>}
      {["active","suspended"].includes(service.status)&&<Button variant="outline" disabled={busy||!!verification||passwordPending} onClick={()=>void reveal()}><KeyRound size={16}/>查看登录密码</Button>}
    </div>}
    {admin&&manage&&verification&&<VerificationForm key={verification.id} serviceID={service.id} job={verification} refresh={refresh}/>}
    {error&&<p role="alert" className="text-sm text-destructive break-all">{error}</p>}
    <Dialog open={!!confirmation} onOpenChange={open=>{if(!open&&!busy)setConfirmation(null);}}><DialogContent showCloseButton={!busy}><DialogHeader><DialogTitle>确认{serviceActionLabels[confirmation??""]}</DialogTitle><DialogDescription>{confirmation==="terminate"?"终止将永久删除供应商资源，无法恢复。":forced?"强制操作会立即中断运行，可能导致未保存数据丢失或文件系统损坏。":"此操作会中断当前连接及服务运行。"}</DialogDescription></DialogHeader><p className="text-sm break-all">{service.name} · #{service.id}</p>{forced&&<label className="flex items-start gap-3 text-sm"><Checkbox aria-label="确认强制操作风险" disabled={busy} checked={acknowledged} onCheckedChange={v=>setAcknowledged(v===true)}/><span>已确认数据风险，继续强制操作</span></label>}{error&&<p role="alert" className="text-sm text-destructive break-all">{error}</p>}<DialogFooter><Button variant="outline" disabled={busy} onClick={()=>setConfirmation(null)}>取消</Button><Button variant="destructive" disabled={busy||inflight||(forced&&!acknowledged)} onClick={()=>confirmation&&void action(confirmation)}>{busy?"提交中":"确认"}</Button></DialogFooter></DialogContent></Dialog>
    <Dialog open={!!credentials} onOpenChange={open=>{if(!open)setCredentials(null);}}><DialogContent><DialogHeader><DialogTitle>服务登录凭据</DialogTitle></DialogHeader><p className="text-xs text-muted-foreground">一分钟后自动隐藏。请妥善保管，并在首次登录后修改密码。</p><p className="font-mono break-all">用户名：{credentials?.username}</p><p className="font-mono break-all" aria-label="服务登录密码">密码：{credentials?.password}</p><Button variant="outline" onClick={()=>setCredentials(null)}>隐藏密码</Button></DialogContent></Dialog>
  </section>;
}

function VerificationForm({serviceID,job,refresh}:{serviceID:number;job:NonNullable<Service["jobs"]>[number];refresh:()=>void}) {
  const [resolution,setResolution]=useState(""),[resource,setResource]=useState(""),[note,setNote]=useState("");
  const [checked,setChecked]=useState(false),[busy,setBusy]=useState(false),[error,setError]=useState("");
  const [password,setPassword]=useState(""),[confirmation,setConfirmation]=useState("");
  const [login,setLogin]=useState("root"),[os,setOS]=useState("");
  const restored=job.action==="snapshot_restore"||job.action==="backup_restore";
  const reinstall=job.action==="reinstall"&&resolution==="applied";
  const needsPassword=reinstall||(restored&&resolution==="applied");
  async function submit(e:React.FormEvent){
    e.preventDefault();if(!checked||busy)return;
    if(needsPassword&&password!==confirmation){setError("两次系统密码不一致");return;}
    setBusy(true);setError("");
    try{await api(`/admin/services/${serviceID}/delivery-verification`,{method:"POST",body:JSON.stringify({job_id:job.id,resolution,upstream_id:resource,note,...(needsPassword?{password}:{}),...(restored&&resolution==="applied"?{username:login,os_name:os}:{})})});setPassword("");setConfirmation("");refresh();}catch(e){setError((e as Error).message);}finally{setBusy(false);}
  }
  return <form onSubmit={submit} className="space-y-4 border-t pt-5">
    <h3 className="font-medium">核验任务 #{job.id} · {serviceActionLabels[job.action]??job.action}</h3>
    <fieldset disabled={busy} className="space-y-4">
      <label className="grid gap-2 text-sm">核验结果<select className={catalogSelectStyle} required aria-label="核验结果" value={resolution} onChange={e=>{setResolution(e.target.value);setPassword("");setConfirmation("");}}><option value="">选择已核验的结果</option>{job.action!=="sync"&&<option value="applied">上游操作已完成</option>}<option value="not_applied">{job.action==="sync"?"结束任务，允许重新同步":job.action==="reinstall"?"已确认上游未执行重装":"已确认上游操作未完成"}</option></select></label>
      {job.action==="reinstall"&&resolution==="not_applied"&&<p className="text-sm text-muted-foreground">须确认上游未执行重装，原系统及登录凭据仍有效。安装仍在进行时，请保留待核验任务。</p>}
      {restored&&resolution==="not_applied"&&<p className="text-sm text-muted-foreground">须确认供应商未执行恢复，原系统和登录凭据仍有效；恢复仍在进行时请保留待核验任务。</p>}
      {restored&&resolution==="applied"&&<><label className="grid gap-2 text-sm">恢复后的系统名称<Input aria-label="恢复后的系统名称" required maxLength={120} value={os} onChange={e=>setOS(e.target.value)}/></label><label className="grid gap-2 text-sm">恢复后的登录用户名<select aria-label="恢复后的登录用户名" className={catalogSelectStyle} value={login} onChange={e=>setLogin(e.target.value)}><option value="root">root</option><option value="administrator">administrator</option></select></label></>}
      {job.action==="create"&&resolution==="applied"&&<label className="grid gap-2 text-sm">供应商资源 ID<Input aria-label="供应商资源 ID" required pattern="[1-9][0-9]{0,18}" value={resource} onChange={e=>setResource(e.target.value)}/></label>}
      {needsPassword&&<><p className="text-sm text-muted-foreground break-all">{restored?`已核验恢复还原点：${job.request_payload?.point?.name??job.request_payload?.point_id}`:`已核验安装镜像：${job.request_payload?.name}`}。登记已实际登录验证的系统密码。</p><label className="grid gap-2 text-sm">核验后的系统密码<Input aria-label="核验后的系统密码" type="password" autoComplete="new-password" required minLength={12} maxLength={128} pattern="[!-~]{12,128}" value={password} onChange={e=>setPassword(e.target.value)}/></label><label className="grid gap-2 text-sm">再次输入核验后的系统密码<Input aria-label="再次输入核验后的系统密码" type="password" autoComplete="new-password" required minLength={12} maxLength={128} pattern="[!-~]{12,128}" value={confirmation} onChange={e=>setConfirmation(e.target.value)}/></label></>}
      <label className="grid gap-2 text-sm">核验依据<Textarea aria-label="核验依据" required minLength={3} maxLength={2000} placeholder="记录核验时间、资源及供应商操作结果" value={note} onChange={e=>setNote(e.target.value)}/></label>
      <label className="flex items-start gap-3 text-sm"><Checkbox checked={checked} onCheckedChange={v=>setChecked(v===true)}/><span>已在供应商控制台核验实际资源及本次操作</span></label>
    </fieldset>
    {resolution==="not_applied"&&job.action!=="sync"&&<p className="text-xs text-muted-foreground">登记后该任务结束；如需重试，请另行提交服务动作。</p>}
    {error&&<p role="alert" className="text-destructive text-sm break-all">{error}</p>}<Button disabled={busy||!checked||!resolution}>{busy?"登记中":"登记核验结果"}</Button>
  </form>;
}
