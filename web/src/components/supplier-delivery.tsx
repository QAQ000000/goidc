"use client";

import { useRef, useState } from "react";
import { Play, RefreshCw, ShieldCheck } from "lucide-react";
import { api, type Service } from "@/lib/api";
import { useAdminPermission } from "./workspace";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Checkbox } from "./ui/checkbox";
import { catalogSelectStyle } from "./catalog-admin";

export function SupplierServiceDelivery({service,admin,refresh}:{service:Service;admin:boolean;refresh:()=>void}) {
  const manage=useAdminPermission("services.manage");
  const [busy,setBusy]=useState(false);
  const [error,setError]=useState("");
  const keys=useRef<Record<string,string>>({});
  const verification=service.jobs?.find(job=>job.status==="waiting_verification");
  const inProgress=service.jobs?.some(job=>["pending","running","waiting_verification"].includes(job.status));
  const canCreate=!service.upstream_id&&["queued","failed","provisioning"].includes(service.status);
  const canSync=!!service.upstream_id&&["active","suspended","provisioning","failed"].includes(service.status);
  async function action(value:"create"|"sync") {
    if (!admin||!manage||busy||inProgress) return;
    setBusy(true);setError("");
    keys.current[value]??=crypto.randomUUID();
    try {
      await api(`/admin/services/${service.id}/actions`,{method:"POST",headers:{"Idempotency-Key":`web-supplier-${value}-${service.id}-${keys.current[value]}`},body:JSON.stringify({action:value})});
      delete keys.current[value];refresh();
    } catch (e) {setError((e as Error).message);} finally {setBusy(false);}
  }
  return <section className="border-y py-5 my-6 space-y-4">
    <div><h2 className="font-semibold">供应商交付</h2><p className="text-sm text-muted-foreground mt-1">{service.upstream_id?`上游资源 #${service.upstream_id}`:"等待上游资源开通"}</p></div>
    {verification?<p role="status" className="text-sm text-amber-800">任务 #{verification.id} 的上游结果待核验。请核对供应商账单和资源状态后登记结果；核验前账户交付将暂停。</p>:<p className="text-xs text-muted-foreground">当前支持开通和同步。暂停、恢复、终止、续费和自动取消暂不可用；需要取消服务请联系管理员。</p>}
    {admin&&manage&&<div className="flex flex-wrap gap-2">{canCreate&&<Button variant="outline" disabled={busy||inProgress} onClick={()=>void action("create")}><Play size={16}/>{service.status==="failed"?"重试开通":"开通资源"}</Button>}{canSync&&<Button variant="outline" disabled={busy||inProgress} onClick={()=>void action("sync")}><RefreshCw size={16}/>同步上游状态</Button>}</div>}
    {admin&&manage&&verification&&<SupplierVerification key={`${verification.id}:${verification.attempts}`} service={service} job={verification} refresh={refresh}/>}
    {error&&<p role="alert" className="text-sm text-destructive break-all">{error}</p>}
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
  return <form onSubmit={submit} className="border-t pt-5 space-y-4 max-w-2xl"><div className="flex items-center gap-2"><ShieldCheck size={18}/><h3 className="font-medium">核验任务 #{job.id} · 第 {job.attempts} 次尝试</h3></div><fieldset disabled={busy} className="space-y-4"><label className="grid gap-2 text-sm">核验结果<select required className={catalogSelectStyle} value={outcome} onChange={e=>{setOutcome(e.target.value as typeof outcome);setAcknowledged(false);}}><option value="">选择实际结果</option><option value="delivered">上游资源已交付</option><option value="not_applied">确认上游未产生结果</option></select></label>{outcome==="delivered"&&<label className="grid gap-2 text-sm">上游资源 ID<Input required type="number" min={1} step={1} value={upstreamID} onChange={e=>setUpstreamID(e.target.value)}/></label>}{outcome==="not_applied"&&<div className="space-y-3 text-sm"><p className="text-amber-800">请先确认上游没有已支付或待支付账单，也没有已开通或仍在处理的资源。提交后账户解除阻塞，该任务标记失败，可再手动重试。</p><label className="flex items-start gap-3"><Checkbox checked={acknowledged} onCheckedChange={value=>setAcknowledged(value===true)}/><span>已核对上游账单、资源和处理中的订单，确认均未产生本次交付结果</span></label></div>}<label className="grid gap-2 text-sm">核验依据<Textarea required minLength={8} maxLength={1000} value={note} onChange={e=>setNote(e.target.value)} placeholder="记录核验时间、账单和资源状态"/></label></fieldset>{error&&<p role="alert" className="text-sm text-destructive break-all">{error}</p>}<Button disabled={busy||!outcome||note.trim().length<8||(outcome==="not_applied"&&!acknowledged)}><ShieldCheck size={16}/>{busy?"登记中":"登记核验结果"}</Button></form>;
}
