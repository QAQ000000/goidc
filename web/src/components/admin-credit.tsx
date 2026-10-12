"use client";
import { useEffect, useState,useRef } from "react";
import { api, ApiError, money } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { useAdminListNavigation } from "@/hooks/use-admin-list-navigation";
import { Workspace, useAdminIdentity, useAdminPermission } from "./workspace";
import { Loading, Failure } from "./common";
import { AdminPagination } from "./admin-pagination";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Table, TableHeader, TableBody, TableHead, TableCell, TableRow } from "./ui/table";
import { CreditRecords } from "./credit-records";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "./ui/dialog";

const states = ["all", "active", "disabled"];
const creditMaxCents = 100000000000;
const defaults = { limit: "20",tab:"customers",customer_id:"",date_from:"",date_to:"",credit_status:"" };
type Row = { id:number; username:string; display_name:string; email:string; status:string; limit_cents:number; used_cents:number; available_cents:number; version:number; frozen:boolean };
type Page = { items:Row[]; total:number; limit:number; offset:number };
type Draft = { row:Row; amount:string; note:string; key:string; confirmed:boolean; pending:boolean };
function cents(raw:string):number|null {
  if (!/^\d+(?:\.\d{1,2})?$/.test(raw.trim())) return null;
  const [whole,fraction=""] = raw.trim().split(".");
  const value = Number(whole)*100+Number(fraction.padEnd(2,"0"));
  return Number.isSafeInteger(value) && value <= creditMaxCents ? value : null;
}
export function CreditManagementView() {
  const navigation = useAdminListNavigation(true, states, "all", defaults);
  const size = [20,50,100].includes(Number(navigation.extra.limit)) ? Number(navigation.extra.limit) : 20;
  const resource = useResource<Page>(navigation.ready ? `/admin/credit?q=${encodeURIComponent(navigation.query)}&status=${navigation.status==="all"?"":navigation.status}&limit=${size}&offset=${navigation.page*size}` : null, false);
  const view = useAdminPermission("credit.view");
  const manage = useAdminPermission("credit.manage");
  const [draft,setDraft] = useState<Draft|null>(null);
  const [error,setError] = useState("");
  const [busy,setBusy] = useState(false);
  const [conflict,setConflict]=useState(false);
  const [freezeRequest,setFreezeRequest]=useState<{row:Row;key:string;body:string;pending:boolean}|null>(null);
  const actor = useAdminIdentity();
  const pendingKey = actor ? `zjmfgo:pending-credit:${actor.id}` : null;
  const [readyKey, setReadyKey] = useState<string|null>(null);
  useEffect(() => {
    let restored:Draft|null=null;
    try {
      const raw=pendingKey ? sessionStorage.getItem(pendingKey) : null;
      if(raw){const value=JSON.parse(raw) as Draft;if(value.pending && value.confirmed && Number.isSafeInteger(value.row?.id) && typeof value.amount==="string" && typeof value.note==="string" && typeof value.key==="string")restored=value;}
    }catch{/* Browser storage is optional. */}
    let active=true;
    queueMicrotask(()=>{if(active){setDraft(restored);setReadyKey(pendingKey);}});
    return ()=>{active=false;};
  },[pendingKey]);
  useEffect(()=>{
    if(!draft?.pending)return;
    const warn=(event:BeforeUnloadEvent)=>{event.preventDefault();event.returnValue="";};
    window.addEventListener("beforeunload",warn);
    return ()=>window.removeEventListener("beforeunload",warn);
  },[draft?.pending]);
  const rememberPending=(value:Draft|null)=>{if(pendingKey){try{if(value)sessionStorage.setItem(pendingKey,JSON.stringify(value));else sessionStorage.removeItem(pendingKey);}catch{/* In-memory retry remains available. */}}};
  const freezeActorKey=useRef("");
  useEffect(()=>{void api<{id:number}>("/me").then(user=>{freezeActorKey.current=`credit-freeze-pending:${user.id}`;const raw=sessionStorage.getItem(freezeActorKey.current);if(raw){try{setFreezeRequest(JSON.parse(raw));}catch{sessionStorage.removeItem(freezeActorKey.current);}}});},[]);
  useEffect(()=>{if(!freezeRequest?.pending)return;const leave=(event:BeforeUnloadEvent)=>{event.preventDefault();};window.addEventListener("beforeunload",leave);return()=>window.removeEventListener("beforeunload",leave);},[freezeRequest]);
  const open = (row:Row) => {setConflict(false);setError("");setDraft({row,amount:(row.limit_cents/100).toFixed(2),note:"",key:crypto.randomUUID(),confirmed:false,pending:false});};
  const edit = (patch:Partial<Draft>) => {if(draft&&!draft.pending)setDraft({...draft,...patch,confirmed:false,key:crypto.randomUUID()});};
  const save = async () => {
    if(!draft||!manage||!pendingKey||readyKey!==pendingKey)return;
    const amount=cents(draft.amount);
    if(amount===null||amount<draft.row.used_cents){setError("请输入有效金额（最多两位小数），授权额度不能低于已用额度。");return;}
    if(!draft.confirmed){setDraft({...draft,confirmed:true});return;}
    setBusy(true);setError("");setDraft({...draft,pending:true});rememberPending({...draft,pending:true});
    try {
      await api(`/admin/credit/${draft.row.id}/limit`,{method:"PUT",headers:{"Idempotency-Key":`credit-${draft.key}`},body:JSON.stringify({limit_cents:amount,expected_version:draft.row.version,note:draft.note})});
      rememberPending(null);setDraft(null);resource.refresh();
    } catch(e) {
      setError((e as Error).message);
      setConflict(e instanceof ApiError && e.status===409);
      // A transport failure has an unknown result: preserve the exact payload and key for retry.
      if(e instanceof ApiError && e.status<500)rememberPending(null);
      setDraft({...draft,pending:!(e instanceof ApiError) || e.status >= 500});
    } finally {setBusy(false);}
  };
  const refreshDraft = async () => {
    if(!draft)return;
    setBusy(true);
    try {
      const page=await api<Page>(`/admin/credit?customer_id=${draft.row.id}&limit=1`);
      const row=page.items.find(item=>item.id===draft.row.id);
      if(!row)throw new Error("当前权限范围内找不到此账户");
      setDraft({...draft,row,key:crypto.randomUUID(),confirmed:false,pending:false});setConflict(false);setError("");
    }catch(e){setError((e as Error).message);}finally{setBusy(false);}
  };
  const freeze = (row:Row) => {setError("");setFreezeRequest({row,key:crypto.randomUUID(),body:JSON.stringify({frozen:!row.frozen,expected_version:row.version,note:row.frozen?"管理员解冻":"管理员冻结"}),pending:false});};
  const saveFreeze = async () => {
   if(!freezeRequest)return;setBusy(true);setError("");
   try{await api(`/admin/credit/${freezeRequest.row.id}/freeze`,{method:"PUT",headers:{"Idempotency-Key":`freeze-${freezeRequest.key}`},body:freezeRequest.body});if(freezeActorKey.current)sessionStorage.removeItem(freezeActorKey.current);setFreezeRequest(null);resource.refresh();}catch(e){setError((e as Error).message);const pending=!(e instanceof ApiError)||e.status>=500;setFreezeRequest({...freezeRequest,pending});if(freezeActorKey.current){if(pending)sessionStorage.setItem(freezeActorKey.current,JSON.stringify({...freezeRequest,pending}));else sessionStorage.removeItem(freezeActorKey.current);}}finally{setBusy(false);}
  };
  const tab=navigation.extra.tab;
  return <Workspace title="信用额管理" admin><div className="space-y-5"><div><h1 className="page-title">信用额管理</h1><p className="text-sm text-muted-foreground mt-2">查看客户授信额度及占用情况，按客户调整授权额度。</p></div>{error&&!draft&&<p role="alert" className="text-destructive">{error}</p>}<nav className="flex flex-wrap gap-3">{[["consumptions","消费记录"],["repayments","还款记录"],["customers","客户列表"]].map(([key,label])=><Button key={key} variant={tab===key?"default":"outline"} onClick={()=>{navigation.setExtra({...navigation.extra,tab:key});navigation.setPage(0);}}>{label}</Button>)}</nav>{tab!=="customers"?<CreditRecords kind={tab==="repayments"?"repayments":"consumptions"} extra={navigation.extra} setExtra={navigation.setExtra} query={navigation.query} page={navigation.page} size={size} setQuery={q=>{navigation.setQuery(q);navigation.setDraft(q);}} setPage={navigation.setPage} setSize={limit=>navigation.setExtra({...navigation.extra,limit:String(limit)})}/>:<><form className="flex flex-wrap gap-3" onSubmit={e=>{e.preventDefault();navigation.setQuery(navigation.draft);navigation.setPage(0);}}><Input className="max-w-sm" aria-label="搜索客户" placeholder="搜索客户姓名、邮箱或 ID" value={navigation.draft} onChange={e=>navigation.setDraft(e.target.value)}/><select className="border rounded px-3 py-2" aria-label="客户状态" value={navigation.status} onChange={e=>{navigation.setStatus(e.target.value);navigation.setPage(0);}}><option value="all">全部状态</option><option value="active">启用</option><option value="disabled">停用</option></select><Button type="submit">搜索</Button><Button type="button" variant="outline" onClick={()=>{navigation.setQuery("");navigation.setDraft("");navigation.setStatus("all");navigation.setExtra({...defaults});navigation.setPage(0);}}>重置</Button><Button type="button" variant="outline" onClick={resource.refresh}>刷新</Button></form>{!view?<p role="alert">没有查看信用额的权限</p>:resource.error?<Failure error={resource.error} retry={resource.refresh}/>:!resource.data?<Loading/>:<><div className="overflow-x-auto"><Table><TableHeader><TableRow>{["客户","状态","授权额度","已用额度","可用额度","操作"].map(label=><TableHead key={label}>{label}</TableHead>)}</TableRow></TableHeader><TableBody>{resource.data.items.map(row=><TableRow key={row.id}><TableCell>{row.display_name||row.username}<div className="text-xs text-muted-foreground">#{row.id} · {row.email}</div></TableCell><TableCell>{row.frozen?"信用冻结":row.status==="active"?"启用":"停用"}</TableCell><TableCell>{money(row.limit_cents)}</TableCell><TableCell>{money(row.used_cents)}</TableCell><TableCell>{money(row.available_cents)}</TableCell><TableCell>{manage&&<><Button variant="outline" size="sm" onClick={()=>open(row)}>设置额度</Button><Button variant="outline" size="sm" onClick={()=>freeze(row)}>{row.frozen?"解冻":"冻结"}</Button></>}</TableCell></TableRow>)}{resource.data.items.length===0&&<TableRow><TableCell colSpan={6} className="py-12 text-center">暂无数据</TableCell></TableRow>}</TableBody></Table></div><AdminPagination total={resource.data.total} page={navigation.page} pageSize={size} pageSizes={[20,50,100]} onPageChange={navigation.setPage} onPageSizeChange={limit=>{navigation.setExtra({...navigation.extra,limit:String(limit)});navigation.setPage(0);}}/></>}</>}</div><Dialog open={freezeRequest!==null} onOpenChange={open=>{if(!open&&!busy&&!freezeRequest?.pending)setFreezeRequest(null);}}><DialogContent><DialogHeader><DialogTitle>{freezeRequest?.row.frozen?"解冻信用账户":"冻结信用账户"}</DialogTitle><DialogDescription>冻结后拒绝新信用消费，还款不受影响。账户版本 {freezeRequest?.row.version}。</DialogDescription></DialogHeader>{error&&<p role="alert">{error}</p>}{freezeRequest?.pending&&<p>上次请求结果未知，请使用原请求重试确认。</p>}<DialogFooter><Button variant="outline" disabled={busy||freezeRequest?.pending} onClick={()=>setFreezeRequest(null)}>取消</Button><Button disabled={busy||!manage} onClick={()=>void saveFreeze()}>{freezeRequest?.pending?"重试确认结果":"确认"}</Button></DialogFooter></DialogContent></Dialog><Dialog open={draft!==null} onOpenChange={open=>{if(!open&&!busy&&!draft?.pending)setDraft(null);}}><DialogContent><DialogHeader><DialogTitle>设置授权额度</DialogTitle><DialogDescription>{draft?.row.display_name||draft?.row.username} · 当前版本 {draft?.row.version}。保存只调整授信额度。</DialogDescription></DialogHeader>{draft&&<div className="space-y-4"><fieldset disabled={busy||draft.pending||!manage} className="space-y-4"><label className="grid gap-2 text-sm">授权额度（元）<Input inputMode="decimal" value={draft.amount} onChange={e=>edit({amount:e.target.value})}/></label><label className="grid gap-2 text-sm">调整备注<Textarea maxLength={240} value={draft.note} onChange={e=>edit({note:e.target.value})}/></label></fieldset>{draft.confirmed&&<p className="text-sm">确认将授权额度设置为 {money(cents(draft.amount)??0)}？</p>}{error&&<p role="alert" className="text-sm text-destructive">{error}</p>}{conflict&&<Button variant="outline" disabled={busy} onClick={()=>void refreshDraft()}>刷新账户版本并保留草稿</Button>}{draft.pending&&!busy&&<p className="text-sm">上次请求结果尚未确认，请以原请求重试确认结果。</p>}<DialogFooter><Button variant="outline" disabled={busy||draft.pending} onClick={()=>setDraft(null)}>取消</Button><Button disabled={busy||!manage} onClick={()=>void save()}>{busy?"保存中":draft.pending?"重试确认结果":draft.confirmed?"确认保存":"保存"}</Button></DialogFooter></div>}</DialogContent></Dialog></Workspace>;
}
