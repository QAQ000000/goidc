"use client";

import { useRef, useState } from "react";
import Link from "next/link";
import { OctagonX, Play, Power, PowerOff, RefreshCw, RotateCw, ShieldCheck, Zap, LifeBuoy, Monitor, LogOut } from "lucide-react";
import { api, type Service } from "@/lib/api";
import { useAdminPermission } from "./workspace";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Checkbox } from "./ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "./ui/dialog";
import { catalogSelectStyle } from "./catalog-admin";
import { NOKVMPasswordReset } from "./nokvm-password";
import { NOKVMReinstall } from "./nokvm-reinstall";

const commands=[{action:"power_on",label:"开机",icon:Power},{action:"power_off",label:"关机",icon:PowerOff},{action:"reboot",label:"重启",icon:RotateCw},{action:"force_off",label:"硬关机",icon:OctagonX},{action:"force_reboot",label:"硬重启",icon:Zap},{action:"exit_rescue_system",label:"退出救援系统",icon:LogOut}];

export function SupplierServiceDelivery({service,admin,refresh}:{service:Service;admin:boolean;refresh:()=>void}) {
  const manage=useAdminPermission("services.manage");
  const authorized=!admin||manage;
  const capabilities=service.supplier_capabilities;
  const base=`${admin?"/admin":""}/services/${service.id}`;
  const [busy,setBusy]=useState(false);
  const [error,setError]=useState("");
  const [confirmation,setConfirmation]=useState<string|null>(null);
  const [acknowledged,setAcknowledged]=useState(false);
  const [rescueOpen,setRescueOpen]=useState(false);
  const [system,setSystem]=useState("");
  const [tempPassword,setTempPassword]=useState("");
  const [passwordConfirmation,setPasswordConfirmation]=useState("");
  const keys=useRef<Record<string,{key:string;payload:string}>>({});
  const verification=service.jobs?.find(job=>job.status==="waiting_verification");
  const inProgress=service.jobs?.some(job=>["pending","running","waiting_verification"].includes(job.status))||!!service.supplier_running_job_id;
  const canCreate=!service.upstream_id&&["queued","failed","provisioning"].includes(service.status);
  const canSync=!!service.upstream_id&&["active","suspended","provisioning","failed"].includes(service.status);
  const canOperate=!!service.upstream_id&&service.status==="active";
  const accountBlocked=!!service.supplier_running_job_id;
  const forced=confirmation==="force_off"||confirmation==="force_reboot";
  const confirmLabel=commands.find(command=>command.action===confirmation)?.label;
  async function vnc() {
    if (!authorized||!capabilities?.vnc||busy||inProgress) return;
    const popup=window.open("about:blank","_blank");
    if (popup) popup.opener=null;
    setBusy(true);setError("");
    try {
      const result=await api<{url:string}>(`${base}/vnc`,{method:"POST",body:"{}"});
      const url=new URL(result.url);
      if (!["http:","https:"].includes(url.protocol)) throw new Error("供应商返回的控制台地址无效");
      if (!popup) throw new Error("浏览器阻止弹出控制台窗口，请允许弹出窗口后重试");
      popup.location.replace(url.href);
    } catch (e) {popup?.close();setError((e as Error).message);} finally {setBusy(false);}
  }
  async function action(value:string,payload:Record<string,unknown>={}):Promise<boolean> {
    if (!authorized||busy||inProgress||accountBlocked) return false;
    if (["create","sync"].includes(value)&&!admin) return false;
    const supported=value==="create"?capabilities?.create:value==="sync"?capabilities?.sync:["rescue_system","exit_rescue_system"].includes(value)?capabilities?.rescue:capabilities?.power;
    if (!supported) return false;
    setBusy(true);setError("");
    const requestBody=JSON.stringify({action:value,...payload});
    if (!keys.current[value]||keys.current[value].payload!==requestBody) keys.current[value]={key:crypto.randomUUID(),payload:requestBody};
    try {
      await api(`${base}/actions`,{method:"POST",headers:{"Idempotency-Key":`web-supplier-${value}-${service.id}-${keys.current[value].key}`},body:requestBody});
      delete keys.current[value];refresh();return true;
    } catch (e) {setError((e as Error).message);refresh();return false;} finally {setBusy(false);}
  }
  async function submitRescue(event:React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (system!=="1"&&system!=="2") return;
    if (tempPassword!==passwordConfirmation) {setError("两次临时密码不一致");return;}
    if (await action("rescue_system",{system:Number(system),temp_password:tempPassword})) {setRescueOpen(false);setSystem("");setTempPassword("");setPasswordConfirmation("");}
  }
  function requestPower(value:string) {setError("");setAcknowledged(false);setConfirmation(value);}
  async function confirmPower() {
    if (!confirmation||(forced&&!acknowledged)) return;
    const current=confirmation;
    if (await action(current)) {setConfirmation(null);setAcknowledged(false);}
  }
  return <section className="bg-white border-b">
    <div className="service-section-heading"><h2 className="text-sm font-medium">接口命令</h2><span className="text-xs text-muted-foreground">{service.upstream_id?`上游资源 #${service.upstream_id}`:"等待上游资源开通"}</span></div>
    <div className="p-5 sm:p-6 space-y-4">
    <p className="text-sm text-muted-foreground">本站支持自动向上游续费，付款后核验上游账单及新到期时间。取消和升级仍需联系管理员在上游处理并核验结果。</p>
    {inProgress&&!verification&&!accountBlocked&&<p role="status" className="text-sm text-muted-foreground">运维任务处理中，完成前暂停其他操作。请刷新查看结果。</p>}
    {verification&&<p role="status" className="text-sm text-amber-800">任务 #{verification.id} 的上游结果待核验，相关操作暂停。</p>}
    {accountBlocked&&service.supplier_blocking_service_id!==service.id&&<p role="status" className="text-sm text-amber-800">供应商账户被任务 #{service.supplier_running_job_id} 占用。{admin?<Link className="underline underline-offset-4 ml-1" href={`/admin/services/${service.supplier_blocking_service_id}`}>查看服务 #{service.supplier_blocking_service_id} 并核验</Link>:"请联系管理员核验后继续。"}</p>}
    {authorized&&<div className="flex flex-wrap gap-2">
      {admin&&capabilities?.create&&<Button variant="outline" disabled={!canCreate||busy||!!inProgress} onClick={()=>void action("create")}><Play size={16}/>{service.status==="failed"?"重试开通":"开通"}</Button>}
      {capabilities?.power&&commands.filter(command=>command.action!=="exit_rescue_system").map(({action:command,label,icon:Icon})=><Button key={command} variant="outline" disabled={!canOperate||busy||!!inProgress} onClick={()=>command==="power_on"?void action(command):requestPower(command)}><Icon size={16}/>{label}</Button>)}
      {capabilities?.reinstall&&<NOKVMReinstall service={service} admin={admin} disabled={!canOperate||busy||!!inProgress} refresh={refresh}/>}
      {capabilities?.reset_password&&<NOKVMPasswordReset service={service} admin={admin} disabled={!canOperate||busy||!!inProgress} refresh={refresh}/>}
      {capabilities?.rescue&&<Button variant="outline" disabled={!canOperate||busy||!!inProgress} onClick={()=>{setError("");setRescueOpen(true);}}><LifeBuoy size={16}/>救援系统</Button>}
      {capabilities?.rescue&&<Button variant="outline" disabled={!canOperate||busy||!!inProgress} onClick={()=>requestPower("exit_rescue_system")}><LogOut size={16}/>退出救援系统</Button>}
      {capabilities?.vnc&&<Button variant="outline" disabled={!canOperate||busy||!!inProgress} onClick={()=>void vnc()}><Monitor size={16}/>VNC</Button>}
      {admin&&capabilities?.sync&&<Button variant="outline" disabled={!canSync||busy||!!inProgress} onClick={()=>void action("sync")}><RefreshCw size={16}/>拉取信息</Button>}
    </div>}
    {admin&&manage&&verification&&<SupplierVerification key={`${verification.id}:${verification.attempts}`} service={service} job={verification} refresh={refresh}/>}
    {!capabilities&&<p role="status" className="text-sm text-muted-foreground">运维能力暂不可用，请刷新服务信息后重试。</p>}
    {error&&<p role="alert" className="text-sm text-destructive break-all">{error}</p>}
    <Dialog open={!!confirmation} onOpenChange={open=>{if(!open&&!busy)setConfirmation(null);}}><DialogContent showCloseButton={!busy}><DialogHeader><DialogTitle>确认{confirmLabel}</DialogTitle><DialogDescription>{forced?"强制操作会立即中断运行，可能导致未保存数据丢失或文件系统损坏。":"此操作会中断当前连接及服务运行。"}</DialogDescription></DialogHeader><p className="text-sm break-all">{service.name} · #{service.id}</p>{forced&&<label className="flex items-start gap-3 text-sm"><Checkbox aria-label="确认强制操作风险" disabled={busy} checked={acknowledged} onCheckedChange={value=>setAcknowledged(value===true)}/><span>已确认数据风险，继续执行强制操作</span></label>}{error&&<p role="alert" className="text-sm text-destructive">{error}</p>}<DialogFooter><Button variant="outline" disabled={busy} onClick={()=>setConfirmation(null)}>取消</Button><Button variant="destructive" disabled={busy||inProgress||(forced&&!acknowledged)} onClick={()=>void confirmPower()}>{busy?"提交中":"确认执行"}</Button></DialogFooter></DialogContent></Dialog>
    <Dialog open={rescueOpen} onOpenChange={open=>{if(!busy&&!open){setRescueOpen(false);setSystem("");setTempPassword("");setPasswordConfirmation("");setError("");}}}><DialogContent showCloseButton={!busy}><DialogHeader><DialogTitle>切换救援系统</DialogTitle><DialogDescription>供应商接受后会重启至救援环境。请先保存工作并确认允许重启。</DialogDescription></DialogHeader><p className="text-sm break-all">{service.name} · #{service.id}</p><form id="supplier-rescue-form" onSubmit={submitRescue} className="space-y-4"><fieldset disabled={busy} className="space-y-4"><label className="grid gap-2 text-sm">救援系统<select required aria-label="救援系统" className={catalogSelectStyle} value={system} onChange={event=>setSystem(event.target.value)}><option value="">选择救援系统</option><option value="1">Linux</option><option value="2">Windows</option></select></label><label className="grid gap-2 text-sm">临时密码<Input aria-label="临时密码" type="password" autoComplete="new-password" required minLength={12} maxLength={128} pattern="[!-~]{12,128}" value={tempPassword} onChange={event=>setTempPassword(event.target.value)}/></label><label className="grid gap-2 text-sm">确认临时密码<Input aria-label="确认临时密码" type="password" autoComplete="new-password" required minLength={12} maxLength={128} pattern="[!-~]{12,128}" value={passwordConfirmation} onChange={event=>setPasswordConfirmation(event.target.value)}/></label></fieldset>{error&&<p role="alert" className="text-sm text-destructive break-all">{error}</p>}<DialogFooter><Button type="button" variant="outline" disabled={busy} onClick={()=>{setRescueOpen(false);setSystem("");setTempPassword("");setPasswordConfirmation("");}}>取消</Button><Button type="submit" form="supplier-rescue-form" disabled={busy||inProgress||!system||!tempPassword||tempPassword!==passwordConfirmation}>{busy?"提交中":"提交救援请求"}</Button></DialogFooter></form></DialogContent></Dialog>
    </div>
  </section>;
}

function SupplierVerification({service,job,refresh}:{service:Service;job:NonNullable<Service["jobs"]>[number];refresh:()=>void}) {
  const [outcome,setOutcome]=useState<""|"delivered"|"not_applied">("");
  const [upstreamID,setUpstreamID]=useState(service.upstream_id||"");
  const [note,setNote]=useState("");
  const [acknowledged,setAcknowledged]=useState(false);
  const [busy,setBusy]=useState(false);
  const [error,setError]=useState("");
  async function submit(event:React.FormEvent) {
    event.preventDefault();
    if (busy||!outcome||!note.trim()||(outcome==="not_applied"&&!acknowledged)) return;
    const numericID=Number(upstreamID);
    if (outcome==="delivered"&&(!Number.isSafeInteger(numericID)||numericID<1)) {setError("请输入有效的上游资源 ID");return;}
    setBusy(true);setError("");
    try {
      await api(`/admin/services/${service.id}/supplier-verification`,{method:"POST",body:JSON.stringify({job_id:job.id,expected_attempts:job.attempts,upstream_id:outcome==="delivered"?numericID:0,outcome,note:note.trim(),absence_acknowledged:outcome==="not_applied"&&acknowledged})});
      refresh();
    } catch (e) {setError((e as Error).message);} finally {setBusy(false);}
  }
  return <form onSubmit={submit} className="border-t pt-5 space-y-4 max-w-2xl"><div className="flex items-center gap-2"><ShieldCheck size={18}/><h3 className="font-medium">核验任务 #{job.id} · 第 {job.attempts} 次尝试</h3></div><fieldset disabled={busy} className="space-y-4"><label className="grid gap-2 text-sm">核验结果<select required className={catalogSelectStyle} value={outcome} onChange={e=>{setOutcome(e.target.value as typeof outcome);setAcknowledged(false);}}><option value="">选择实际结果</option><option value="delivered">{job.action==="renew"?"上游账单已支付且续费已生效":"上游资源已交付"}</option><option value="not_applied">确认上游未产生结果</option></select></label>{outcome==="delivered"&&<label className="grid gap-2 text-sm">上游资源 ID<Input required type="number" min={1} step={1} value={upstreamID} onChange={e=>setUpstreamID(e.target.value)}/></label>}{outcome==="not_applied"&&<div className="space-y-3 text-sm"><p className="text-amber-800">{job.action==="renew"?"请确认本次续费没有已支付、待支付或处理中的上游账单。原资源仍然存在。登记未生效后，先处理客户已支付的本地账单，禁止直接重复收费。":"请先确认上游没有已支付或待支付账单，也没有已开通或仍在处理的资源。登记后账户解除阻塞，该任务标记失败。"}</p><label className="flex items-start gap-3"><Checkbox checked={acknowledged} onCheckedChange={value=>setAcknowledged(value===true)}/><span>已核对上游账单、资源和处理中的订单，确认均未产生本次交付结果</span></label></div>}<label className="grid gap-2 text-sm">核验依据<Textarea required minLength={8} maxLength={1000} value={note} onChange={e=>setNote(e.target.value)} placeholder="记录核验时间、账单和资源状态"/></label></fieldset>{error&&<p role="alert" className="text-sm text-destructive break-all">{error}</p>}<Button disabled={busy||!outcome||note.trim().length<8||(outcome==="not_applied"&&!acknowledged)}><ShieldCheck size={16}/>{busy?"登记中":"登记核验结果"}</Button></form>;
}
