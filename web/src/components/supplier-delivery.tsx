"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import { OctagonX, Play, Power, PowerOff, RefreshCw, RotateCw, ShieldCheck, Zap, LifeBuoy, Monitor, LogOut } from "lucide-react";
import { api, ApiError, type Service } from "@/lib/api";
import { usePendingSetting, type PendingSetting } from "@/hooks/use-pending-setting";
import { useAdminIdentity, useAdminPermission } from "./workspace";
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
  const [lifecyclePending,setLifecyclePending]=useState(false);
  const keys=useRef<Record<string,{key:string;payload:string}>>({});
  const verification=service.jobs?.find(job=>job.status==="waiting_verification");
  const inProgress=service.jobs?.some(job=>["pending","running","waiting_manual","waiting_verification"].includes(job.status))||!!service.supplier_running_job_id||lifecyclePending;
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
    <p className="text-sm text-muted-foreground">本站支持自动向上游续费，付款后核验上游账单及新到期时间。暂停、恢复和终止由管理员按实际能力处理并核验，升级请联系管理员。</p>
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
    {admin&&<SupplierLifecycle key={service.id} service={service} refresh={refresh} onPendingChange={setLifecyclePending}/>}
    {!admin&&service.jobs?.some(job=>job.manual_lifecycle&&job.status==="waiting_manual")&&<p role="status" className="text-sm text-amber-800">暂停、恢复或终止任务正在等待管理员处理及核验；当前服务状态为最近一次确认的上游状态。</p>}
    {!capabilities&&<p role="status" className="text-sm text-muted-foreground">运维能力暂不可用，请刷新服务信息后重试。</p>}
    {error&&<p role="alert" className="text-sm text-destructive break-all">{error}</p>}
    <Dialog open={!!confirmation} onOpenChange={open=>{if(!open&&!busy)setConfirmation(null);}}><DialogContent showCloseButton={!busy}><DialogHeader><DialogTitle>确认{confirmLabel}</DialogTitle><DialogDescription>{forced?"强制操作会立即中断运行，可能导致未保存数据丢失或文件系统损坏。":"此操作会中断当前连接及服务运行。"}</DialogDescription></DialogHeader><p className="text-sm break-all">{service.name} · #{service.id}</p>{forced&&<label className="flex items-start gap-3 text-sm"><Checkbox aria-label="确认强制操作风险" disabled={busy} checked={acknowledged} onCheckedChange={value=>setAcknowledged(value===true)}/><span>已确认数据风险，继续执行强制操作</span></label>}{error&&<p role="alert" className="text-sm text-destructive">{error}</p>}<DialogFooter><Button variant="outline" disabled={busy} onClick={()=>setConfirmation(null)}>取消</Button><Button variant="destructive" disabled={busy||inProgress||(forced&&!acknowledged)} onClick={()=>void confirmPower()}>{busy?"提交中":"确认执行"}</Button></DialogFooter></DialogContent></Dialog>
    <Dialog open={rescueOpen} onOpenChange={open=>{if(!busy&&!open){setRescueOpen(false);setSystem("");setTempPassword("");setPasswordConfirmation("");setError("");}}}><DialogContent showCloseButton={!busy}><DialogHeader><DialogTitle>切换救援系统</DialogTitle><DialogDescription>供应商接受后会重启至救援环境。请先保存工作并确认允许重启。</DialogDescription></DialogHeader><p className="text-sm break-all">{service.name} · #{service.id}</p><form id="supplier-rescue-form" onSubmit={submitRescue} className="space-y-4"><fieldset disabled={busy} className="space-y-4"><label className="grid gap-2 text-sm">救援系统<select required aria-label="救援系统" className={catalogSelectStyle} value={system} onChange={event=>setSystem(event.target.value)}><option value="">选择救援系统</option><option value="1">Linux</option><option value="2">Windows</option></select></label><label className="grid gap-2 text-sm">临时密码<Input aria-label="临时密码" type="password" autoComplete="new-password" required minLength={12} maxLength={128} pattern="[!-~]{12,128}" value={tempPassword} onChange={event=>setTempPassword(event.target.value)}/></label><label className="grid gap-2 text-sm">确认临时密码<Input aria-label="确认临时密码" type="password" autoComplete="new-password" required minLength={12} maxLength={128} pattern="[!-~]{12,128}" value={passwordConfirmation} onChange={event=>setPasswordConfirmation(event.target.value)}/></label></fieldset>{error&&<p role="alert" className="text-sm text-destructive break-all">{error}</p>}<DialogFooter><Button type="button" variant="outline" disabled={busy} onClick={()=>{setRescueOpen(false);setSystem("");setTempPassword("");setPasswordConfirmation("");}}>取消</Button><Button type="submit" form="supplier-rescue-form" disabled={busy||inProgress||!system||!tempPassword||tempPassword!==passwordConfirmation}>{busy?"提交中":"提交救援请求"}</Button></DialogFooter></form></DialogContent></Dialog>
    </div>
  </section>;
}

type LifecycleAction = "suspend" | "resume" | "terminate";
type LifecycleVerification = { job_id:number;expected_attempts:number;upstream_id:number;outcome:"delivered"|"not_applied";note:string;absence_acknowledged:boolean };
const lifecycleLabels:Record<LifecycleAction,string>={suspend:"暂停",resume:"恢复",terminate:"终止"};
const lifecycleStates:Record<string,string>={active:"运行中",suspended:"已暂停",terminated:"已终止",waiting_manual:"等待人工处理及核验",pending:"待处理",running:"执行中",waiting_verification:"等待核验",succeeded:"已完成",failed:"失败",cancelled:"已取消"};
const isLifecycleAction=(value:unknown):value is LifecycleAction=>typeof value==="string"&&Object.hasOwn(lifecycleLabels,value);

function validLifecycleRequest(request:PendingSetting,serviceID:number) {
  try {
    if (!request.key||request.key.length<8||request.key.length>128) return false;
    const body=JSON.parse(request.body) as Record<string,unknown>;
    if (!body||typeof body!=="object"||Array.isArray(body)) return false;
    if (request.path===`/admin/services/${serviceID}/actions`) return Object.keys(body).length===1&&isLifecycleAction(body.action);
    return request.path===`/admin/services/${serviceID}/supplier-lifecycle/verify`
      &&Object.keys(body).sort().join(",")==="absence_acknowledged,expected_attempts,job_id,note,outcome,upstream_id"
      &&Number.isSafeInteger(body.job_id)&&Number(body.job_id)>0&&Number.isSafeInteger(body.expected_attempts)&&Number(body.expected_attempts)>=0
      &&typeof body.note==="string"&&body.note.trim().length>=8&&body.note.length<=1000&&typeof body.absence_acknowledged==="boolean"
      &&((body.outcome==="delivered"&&Number.isSafeInteger(body.upstream_id)&&Number(body.upstream_id)>0&&!body.absence_acknowledged)
        ||(body.outcome==="not_applied"&&body.upstream_id===0&&body.absence_acknowledged));
  } catch { return false; }
}

export function SupplierLifecycle({service,refresh,onPendingChange}:{service:Service;refresh:()=>void;onPendingChange?:(pending:boolean)=>void}) {
  const actor=useAdminIdentity();
  const manage=useAdminPermission("services.manage");
  const {pending,unknown,setUnknown,ready,remember,resolve}=usePendingSetting(`supplier-lifecycle-${service.id}`,actor?.id??null);
  const [busy,setBusy]=useState(false);
  const [error,setError]=useState("");
  const [message,setMessage]=useState("");
  const [confirmation,setConfirmation]=useState<LifecycleAction|null>(null);
  const [acknowledged,setAcknowledged]=useState(false);
  const [outcome,setOutcome]=useState<""|"delivered"|"not_applied">("");
  const [note,setNote]=useState("");
  const [absence,setAbsence]=useState(false);
  const running=useRef(false);
  const manual=service.jobs?.find(job=>job.manual_lifecycle&&job.status==="waiting_manual"&&isLifecycleAction(job.action));
  const inflight=service.jobs?.some(job=>["pending","running","waiting_manual","waiting_verification"].includes(job.status));
  const blocked=busy||!ready||unknown;
  const requestInvalid=unknown&&(!pending.current||!validLifecycleRequest(pending.current,service.id));
  const commands:LifecycleAction[]=service.status==="active"?["suspend","terminate"]:service.status==="suspended"?["resume","terminate"]:[];
  const unavailable=!!service.supplier_running_job_id||!service.upstream_id||!service.supplier_capabilities;
  useEffect(()=>{onPendingChange?.(!ready||busy||unknown);},[ready,busy,unknown,onPendingChange]);
  async function send(request:PendingSetting) {
    if (!manage||!ready||running.current||!validLifecycleRequest(request,service.id)) return;
    running.current=true;setBusy(true);setError("");setMessage("");
    remember(request);
    try {
      const result=await api<{verified?:boolean;job_id?:number;service_id?:number;action?:string;status?:string}>(request.path!,{method:"POST",headers:{"Idempotency-Key":request.key},body:request.body});
      const body=JSON.parse(request.body) as {action?:string};
      if (body.action) {
        if (!Number.isSafeInteger(result.job_id)||Number(result.job_id)<1||result.service_id!==service.id||result.action!==body.action||!result.status||!Object.hasOwn(lifecycleStates,result.status)) throw new Error("任务回执关联不完整，请核对原请求");
        setMessage(result.status==="waiting_manual"?`任务 #${result.job_id} 已登记，等待在上游实际处理及核验；当前服务状态保留。`:`任务 #${result.job_id} 已受理，请刷新核对实际结果。`);
      } else {
        if (result.verified!==true) throw new Error("核验回执尚未确认，请保留原请求");
        setMessage("核验结果已保存，请以刷新后的服务状态和任务状态为准。");setOutcome("");setNote("");setAbsence(false);
      }
      resolve();setConfirmation(null);setAcknowledged(false);refresh();
    } catch (failure) {
      if (failure instanceof ApiError&&failure.status>=400&&failure.status<500) {
        resolve();setError(`${failure.message}。请刷新后核对当前服务和任务状态。`);
      } else {setUnknown(true);setError(`${(failure as Error).message}。结果尚未确认，原请求已保留；请读取最新状态或重试原请求。`);}
      refresh();
    } finally {running.current=false;setBusy(false);}
  }
  async function readLatest() {
    if (!ready||running.current) return;
    running.current=true;setBusy(true);setError("");
    try {
      const latest=await api<Service>(`/admin/services/${service.id}`);
      if (latest.id!==service.id||latest.provision_mode!=="supplier") throw new Error("未读取到对应服务");
      let job:NonNullable<Service["jobs"]>[number]|undefined;
      if (pending.current&&validLifecycleRequest(pending.current,service.id)) {
        const body=JSON.parse(pending.current.body) as {job_id?:number;action?:string};
        job=latest.jobs?.find(item=>body.job_id?item.id===body.job_id:item.action===body.action);
      }
      setMessage(`最新服务状态：${lifecycleStates[latest.status]??latest.status}${job?`；任务 #${job.id}：${lifecycleStates[job.status]??job.status}`:""}。${unknown?"原请求仍保留，重试将使用相同内容和请求键。":""}`);
      refresh();
    } catch (failure) {setError((failure as Error).message);} finally {running.current=false;setBusy(false);}
  }
  async function confirmAction() {
    if (!confirmation||blocked||unavailable||inflight||(confirmation==="terminate"&&!acknowledged)) return;
    await send({path:`/admin/services/${service.id}/actions`,key:`web-supplier-lifecycle-${crypto.randomUUID()}`,body:JSON.stringify({action:confirmation})});
  }
  async function verify(event:React.FormEvent) {
    event.preventDefault();
    if (!manual||blocked||!outcome||note.trim().length<8||(outcome==="not_applied"&&!absence)||(manual.action==="terminate"&&outcome==="delivered"&&!acknowledged)) return;
    const upstreamID=Number(service.upstream_id);
    if (outcome==="delivered"&&(!Number.isSafeInteger(upstreamID)||upstreamID<1)) {setError("缺少原上游资源标识，不能登记已生效。");return;}
    const body:LifecycleVerification={job_id:manual.id,expected_attempts:manual.attempts,upstream_id:outcome==="delivered"?upstreamID:0,outcome,note:note.trim(),absence_acknowledged:outcome==="not_applied"&&absence};
    await send({path:`/admin/services/${service.id}/supplier-lifecycle/verify`,key:`web-supplier-lifecycle-${crypto.randomUUID()}`,body:JSON.stringify(body)});
  }
  return <section className="border-t pt-5 space-y-4">
    <div><h3 className="font-medium">服务暂停、恢复与终止</h3><p className="mt-2 text-sm text-muted-foreground">支持自动接口的动作进入后台执行；其他动作只登记待人工任务。请先在原供应商处理，再读取上游状态核验；核验前保留当前服务状态。</p></div>
    {message&&<p role="status" className="text-sm break-words">{message}</p>}
    {unknown&&<div role="status" className="rounded border p-3 space-y-3 text-sm"><p>{requestInvalid?"原请求恢复资料不完整，已暂停操作；请联系支持核对，勿清空后重复提交。":"上次提交结果尚未确认，已暂停新操作。刷新页面后仍保留原请求。"}</p><div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={busy||!ready} onClick={readLatest}>读取最新服务状态</Button>{manage&&!requestInvalid&&<Button type="button" variant="outline" disabled={busy||!ready} onClick={async()=>{if(pending.current)await send(pending.current);}}>重试原请求</Button>}</div></div>}
    {manual&&<p role="status" className="text-sm text-amber-800">任务 #{manual.id}：等待上游{lifecycleLabels[manual.action as LifecycleAction]}及核验。平台未自动发送该动作；请核对原供应商资源 #{service.upstream_id}。账单变更不会证明上游操作未执行，请核对实际状态后完成核验，或明确确认本次操作未生效。</p>}
    {!manage&&<p className="text-sm text-muted-foreground">当前账号仅可查看处理状态，登记和核验需要服务管理权限。</p>}
    {manage&&<>
      <div className="flex flex-wrap gap-2">{commands.map(action=><Button key={action} type="button" variant={action==="terminate"?"destructive":"outline"} disabled={blocked||unavailable||!!inflight} onClick={()=>{setConfirmation(action);setAcknowledged(false);setError("");}}>{service.supplier_capabilities?.[action]?lifecycleLabels[action]:`登记上游${lifecycleLabels[action]}`}</Button>)}</div>
      {!service.supplier_capabilities&&<p role="status" className="text-sm text-muted-foreground">供应商能力暂不可用，请刷新后核对。</p>}
      {manual&&!unknown&&<form onSubmit={verify} className="max-w-2xl space-y-4 rounded border p-4"><h4 className="text-sm font-medium">核验人工{lifecycleLabels[manual.action as LifecycleAction]}任务 #{manual.id}</h4><fieldset disabled={busy||!ready} className="space-y-4"><label className="grid gap-2 text-sm">本次操作结果<select aria-label="人工生命周期核验结果" required className={catalogSelectStyle} value={outcome} onChange={event=>{setOutcome(event.target.value as typeof outcome);setAbsence(false);setAcknowledged(false);}}><option value="">请选择实际结果</option><option value="delivered">上游已{lifecycleLabels[manual.action as LifecycleAction]}，读取并核验</option><option value="not_applied">确认本次操作未生效</option></select></label>
        {outcome==="delivered"&&<p className="text-sm text-muted-foreground">核验固定读取原资源 #{service.upstream_id}，仅在上游返回本次目标状态时保存完成；不会向上游发送动作。</p>}
        {outcome==="not_applied"&&<label className="flex items-start gap-3 text-sm"><Checkbox aria-label="确认本次生命周期操作未生效" checked={absence} onCheckedChange={value=>setAbsence(value===true)}/><span>已核对原上游，确认本次操作未生效且无待处理动作。仅结束本任务，保留既存资源及当前服务状态。</span></label>}
        {manual.action==="terminate"&&outcome==="delivered"&&<label className="flex items-start gap-3 text-sm"><Checkbox aria-label="确认上游终止核验风险" checked={acknowledged} onCheckedChange={value=>setAcknowledged(value===true)}/><span>已确认原上游资源实际终止。通过核验后会记录服务终止，无法恢复原资源。</span></label>}
        <label className="grid gap-2 text-sm">核验依据<Textarea aria-label="人工生命周期核验依据" required minLength={8} maxLength={1000} value={note} onChange={event=>setNote(event.target.value)} placeholder="记录上游处理和核对依据，至少 8 个字符"/></label></fieldset><Button disabled={busy||!ready||!outcome||note.trim().length<8||(outcome==="not_applied"&&!absence)||(manual.action==="terminate"&&outcome==="delivered"&&!acknowledged)}><ShieldCheck size={16}/>{busy?"核验中":"提交并核验本次结果"}</Button></form>}
    </>}
    {error&&<p role="alert" className="text-sm text-destructive break-words">{error}</p>}
    <Dialog open={!!confirmation} onOpenChange={open=>{if(!busy&&!open)setConfirmation(null);}}><DialogContent showCloseButton={!busy}><DialogHeader><DialogTitle>{confirmation?`${service.supplier_capabilities?.[confirmation]?"确认":"登记上游"}${lifecycleLabels[confirmation]}`:"服务操作"}</DialogTitle><DialogDescription>{confirmation&&service.supplier_capabilities?.[confirmation]?"该动作将在后台向原供应商提交，实际状态以任务结果及上游回读为准。":"本次仅登记待人工任务，不会自动调用上游。请在原供应商完成操作后返回核验。"}</DialogDescription></DialogHeader><p className="text-sm break-all">{service.name} · 服务 #{service.id} · 原上游资源 #{service.upstream_id}</p>{confirmation==="terminate"&&<label className="flex items-start gap-3 text-sm"><Checkbox aria-label="确认终止风险" disabled={busy} checked={acknowledged} onCheckedChange={value=>setAcknowledged(value===true)}/><span>终止会结束服务并可能删除上游资源，已确认必要备份和业务影响。</span></label>}{error&&<p role="alert" className="text-sm text-destructive">{error}</p>}<DialogFooter><Button type="button" variant="outline" disabled={busy} onClick={()=>setConfirmation(null)}>取消</Button><Button type="button" variant={confirmation==="terminate"?"destructive":"default"} disabled={!manage||blocked||unavailable||!!inflight||(confirmation==="terminate"&&!acknowledged)} onClick={confirmAction}>{busy?"提交中":"确认提交"}</Button></DialogFooter></DialogContent></Dialog>
  </section>;
}

function SupplierVerification({service,job,refresh}:{service:Service;job:NonNullable<Service["jobs"]>[number];refresh:()=>void}) {
  const [outcome,setOutcome]=useState<""|"delivered"|"not_applied">("");
  const [upstreamID,setUpstreamID]=useState(service.upstream_id||"");
  const lifecycle=isLifecycleAction(job.action)?job.action:null;
  const [terminationAcknowledged,setTerminationAcknowledged]=useState(false);
  const [note,setNote]=useState("");
  const [acknowledged,setAcknowledged]=useState(false);
  const [busy,setBusy]=useState(false);
  const [error,setError]=useState("");
  async function submit(event:React.FormEvent) {
    event.preventDefault();
    if (busy||!outcome||!note.trim()||(outcome==="not_applied"&&!acknowledged)||(lifecycle==="terminate"&&outcome==="delivered"&&!terminationAcknowledged)) return;
    const numericID=Number(lifecycle?service.upstream_id:upstreamID);
    if (outcome==="delivered"&&(!Number.isSafeInteger(numericID)||numericID<1)) {setError("请输入有效的上游资源 ID");return;}
    setBusy(true);setError("");
    try {
      await api(`/admin/services/${service.id}/supplier-verification`,{method:"POST",body:JSON.stringify({job_id:job.id,expected_attempts:job.attempts,upstream_id:outcome==="delivered"?numericID:0,outcome,note:note.trim(),absence_acknowledged:outcome==="not_applied"&&acknowledged})});
      refresh();
    } catch (e) {setError((e as Error).message);} finally {setBusy(false);}
  }
  return <form onSubmit={submit} className="border-t pt-5 space-y-4 max-w-2xl"><div className="flex items-center gap-2"><ShieldCheck size={18}/><h3 className="font-medium">核验未知上游结果 · 任务 #{job.id} · 第 {job.attempts} 次尝试</h3></div><p className="text-sm text-muted-foreground">原动作可能已发送，核验只读取原资源状态，不会重复发送动作。</p><fieldset disabled={busy} className="space-y-4"><label className="grid gap-2 text-sm">核验结果<select required aria-label="未知上游结果核验" className={catalogSelectStyle} value={outcome} onChange={e=>{setOutcome(e.target.value as typeof outcome);setAcknowledged(false);setTerminationAcknowledged(false);}}><option value="">选择实际结果</option><option value="delivered">{lifecycle?`上游已${lifecycleLabels[lifecycle]}，读取并核验`:job.action==="renew"?"上游账单已支付且续费已生效":"上游资源已交付"}</option><option value="not_applied">确认本次操作未生效</option></select></label>{outcome==="delivered"&&<label className="grid gap-2 text-sm">{lifecycle?"原上游资源 ID":"上游资源 ID"}<Input required readOnly={!!lifecycle} type="number" min={1} step={1} value={lifecycle?service.upstream_id:upstreamID} onChange={e=>setUpstreamID(e.target.value)}/></label>}{outcome==="not_applied"&&<div className="space-y-3 text-sm"><p className="text-amber-800">{lifecycle?"请核对原上游资源，确认本次操作未生效且没有仍在处理的动作。原资源可以继续存在，登记只结束本任务。":job.action==="renew"?"请确认本次续费没有已支付、待支付或处理中的上游账单。原资源仍然存在。登记未生效后，先处理客户已支付的本地账单，禁止直接重复收费。":"请先确认上游没有已支付或待支付账单，也没有已开通或仍在处理的资源。登记后账户解除阻塞，该任务标记失败。"}</p><label className="flex items-start gap-3"><Checkbox checked={acknowledged} onCheckedChange={value=>setAcknowledged(value===true)}/><span>{lifecycle?"已核对原资源和处理中动作，确认本次操作未生效":"已核对上游账单、资源和处理中的订单，确认均未产生本次交付结果"}</span></label></div>}{lifecycle==="terminate"&&outcome==="delivered"&&<label className="flex items-start gap-3 text-sm"><Checkbox aria-label="确认未知终止结果风险" checked={terminationAcknowledged} onCheckedChange={value=>setTerminationAcknowledged(value===true)}/><span>已确认原上游资源实际终止，通过核验后记录服务终止。</span></label>}<label className="grid gap-2 text-sm">核验依据<Textarea required minLength={8} maxLength={1000} value={note} onChange={e=>setNote(e.target.value)} placeholder="记录核验时间、账单和资源状态"/></label></fieldset>{error&&<p role="alert" className="text-sm text-destructive break-all">{error}</p>}<Button disabled={busy||!outcome||note.trim().length<8||(outcome==="not_applied"&&!acknowledged)||(lifecycle==="terminate"&&outcome==="delivered"&&!terminationAcknowledged)}><ShieldCheck size={16}/>{busy?"登记中":"登记核验结果"}</Button></form>;
}
