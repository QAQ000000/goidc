"use client";
import {useState} from "react";
import {useResource} from "@/hooks/use-resource";
import {api,ApiError} from "@/lib/api";
import {Workspace} from "./workspace";
import {Button} from "./ui/button";
import {Input} from "./ui/input";
import {Failure,Loading} from "./common";
type Settings={version:number;schema_version:number;enabled:boolean;settings:{expiry_days:number[];billing_grace_days:number}};
export function AdminSchedulerSettings(){
 const r=useResource<Settings>("/admin/scheduler-settings",false);
 const [saved,setSaved]=useState(false);
 if(r.error)return <Workspace admin title="任务配置"><Failure error={r.error} retry={r.refresh}/></Workspace>;
 if(!r.data)return <Workspace admin title="任务配置"><Loading/></Workspace>;
 return <Workspace admin title="任务配置">{saved&&<p role="status" className="mb-4 text-sm">配置已保存</p>}<SchedulerForm key={r.data.version} data={r.data} reload={r.refresh} onSaved={()=>setSaved(true)} onEdit={()=>setSaved(false)}/></Workspace>;
}
function SchedulerForm({data,reload,onSaved,onEdit}:{data:Settings;reload:()=>void;onSaved:()=>void;onEdit:()=>void}){
 const [days,setDays]=useState(data.settings.expiry_days.join(",")); const [grace,setGrace]=useState(String(data.settings.billing_grace_days)); const [enabled,setEnabled]=useState(data.enabled); const [busy,setBusy]=useState(false);const [error,setError]=useState("");const [conflict,setConflict]=useState(false);const [attempt,setAttempt]=useState<{body:string;key:string}|null>(null);
 function cancel(){setDays(data.settings.expiry_days.join(","));setGrace(String(data.settings.billing_grace_days));setEnabled(data.enabled);setError("");setConflict(false);setAttempt(null)}
 async function save(){setError("");onEdit();setConflict(false);const tokens=days.trim()===""?[]:days.split(",").map(v=>v.trim());const expiry=tokens.map(Number);if(tokens.some(v=>!/^\d+$/.test(v))||expiry.some(v=>v<1||v>365)||new Set(expiry).size!==expiry.length||!/^\d+$/.test(grace)||Number(grace)>365){setError("请输入不重复的 1 到 365 天提醒天数及 0 到 365 天账单宽限期。");return};const body=JSON.stringify({expected_version:data.version,enabled,settings:{expiry_days:expiry,billing_grace_days:Number(grace)}});const mutation=attempt?.body===body?attempt:{body,key:crypto.randomUUID()};setAttempt(mutation);setBusy(true);try{await api("/admin/scheduler-settings",{method:"PUT",headers:{"Idempotency-Key":mutation.key},body});onSaved();reload()}catch(e){setError(e instanceof Error?e.message:"保存失败，请稍后重试");setConflict(e instanceof ApiError&&e.status===409)}finally{setBusy(false)}}
 return <div className="max-w-xl space-y-5"><h1 className="page-title">定时任务配置</h1><p className="text-sm text-muted-foreground">设置服务到期及账单提醒。关闭后暂停发送这些自动提醒。</p><label className="flex items-center gap-2"><input type="checkbox" checked={enabled} disabled={busy} onChange={e=>{onEdit();setEnabled(e.target.checked)}}/>启用自动提醒</label><label className="block text-sm">服务到期提醒提前天数（逗号分隔；留空关闭到期提醒）<Input disabled={busy} value={days} onChange={e=>{onEdit();setDays(e.target.value)}}/></label><label className="block text-sm">账单逾期提醒宽限天数<Input disabled={busy} type="number" min="0" max="365" value={grace} onChange={e=>{onEdit();setGrace(e.target.value)}}/></label>{error&&<p role="alert" className="text-sm text-destructive">{error}</p>}<div className="flex gap-3"><Button disabled={busy} onClick={save}>{busy?"保存中":"保存配置"}</Button><Button variant="outline" disabled={busy} onClick={cancel}>取消修改</Button>{conflict&&<Button variant="outline" disabled={busy} onClick={reload}>重新加载配置</Button>}</div></div>
}
