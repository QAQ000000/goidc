"use client";
import { useState } from "react";
import { api, ApiError, money } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { useAdminListNavigation } from "@/hooks/use-admin-list-navigation";
import { Workspace, useAdminPermission } from "./workspace";
import { Loading, Failure } from "./common";
import { AdminPagination } from "./admin-pagination";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Table, TableHeader, TableBody, TableHead, TableCell, TableRow } from "./ui/table";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "./ui/dialog";

const states = ["all", "active", "disabled"];
const defaults = { limit: "20" };
type Row = { id:number; username:string; display_name:string; email:string; status:string; limit_cents:number; used_cents:number; available_cents:number; version:number };
type Page = { items:Row[]; total:number; limit:number; offset:number };
type Draft = { row:Row; amount:string; note:string; key:string; confirmed:boolean; pending:boolean };
function cents(raw:string):number|null {
  if (!/^\d+(?:\.\d{1,2})?$/.test(raw.trim())) return null;
  const [whole,fraction=""] = raw.trim().split(".");
  const value = Number(whole)*100+Number(fraction.padEnd(2,"0"));
  return Number.isSafeInteger(value) ? value : null;
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
  const open = (row:Row) => {setConflict(false);setError("");setDraft({row,amount:(row.limit_cents/100).toFixed(2),note:"",key:crypto.randomUUID(),confirmed:false,pending:false});};
  const edit = (patch:Partial<Draft>) => {if(draft&&!draft.pending)setDraft({...draft,...patch,confirmed:false,key:crypto.randomUUID()});};
  const save = async () => {
    if(!draft||!manage)return;
    const amount=cents(draft.amount);
    if(amount===null||amount<draft.row.used_cents){setError("请输入有效金额（最多两位小数），授权额度不能低于已用额度。");return;}
    if(!draft.confirmed){setDraft({...draft,confirmed:true});return;}
    setBusy(true);setError("");setDraft({...draft,pending:true});
    try {
      await api(`/admin/credit/${draft.row.id}/limit`,{method:"PUT",headers:{"Idempotency-Key":`credit-${draft.key}`},body:JSON.stringify({limit_cents:amount,expected_version:draft.row.version,note:draft.note})});
      setDraft(null);resource.refresh();
    } catch(e) {
      setError((e as Error).message);
      setConflict(e instanceof ApiError && e.status===409);
      // A transport failure has an unknown result: preserve the exact payload and key for retry.
      setDraft({...draft,pending:!(e instanceof ApiError)});
    } finally {setBusy(false);}
  };
  const refreshDraft = async () => {
    if(!draft)return;
    setBusy(true);
    try {
      const page=await api<Page>(`/admin/credit?q=${draft.row.id}&limit=100`);
      const row=page.items.find(item=>item.id===draft.row.id);
      if(!row)throw new Error("当前权限范围内找不到此账户");
      setDraft({...draft,row,key:crypto.randomUUID(),confirmed:false,pending:false});setConflict(false);setError("");
    }catch(e){setError((e as Error).message);}finally{setBusy(false);}
  };
  return <Workspace title="信用额管理" admin><div className="space-y-5"><div><h1 className="page-title">信用额管理</h1><p className="text-sm text-muted-foreground mt-2">查看客户授权额度、已用额度和可用额度。消费、还款与冻结操作尚未开放。</p></div><form className="flex flex-wrap gap-3" onSubmit={e=>{e.preventDefault();navigation.setQuery(navigation.draft);navigation.setPage(0);}}><Input className="max-w-sm" aria-label="搜索客户" placeholder="搜索客户姓名、邮箱或 ID" value={navigation.draft} onChange={e=>navigation.setDraft(e.target.value)}/><select className="border rounded px-3 py-2" aria-label="客户状态" value={navigation.status} onChange={e=>{navigation.setStatus(e.target.value);navigation.setPage(0);}}><option value="all">全部状态</option><option value="active">启用</option><option value="disabled">停用</option></select><Button type="submit">搜索</Button><Button type="button" variant="outline" onClick={()=>{navigation.setQuery("");navigation.setDraft("");navigation.setStatus("all");navigation.setExtra({...defaults});navigation.setPage(0);}}>重置</Button><Button type="button" variant="outline" onClick={resource.refresh}>刷新</Button></form>{!view?<p role="alert">没有查看信用额的权限</p>:resource.error?<Failure error={resource.error} retry={resource.refresh}/>:!resource.data?<Loading/>:<><div className="overflow-x-auto"><Table><TableHeader><TableRow>{["客户","状态","授权额度","已用额度","可用额度","操作"].map(label=><TableHead key={label}>{label}</TableHead>)}</TableRow></TableHeader><TableBody>{resource.data.items.map(row=><TableRow key={row.id}><TableCell>{row.display_name||row.username}<div className="text-xs text-muted-foreground">#{row.id} · {row.email}</div></TableCell><TableCell>{row.status==="active"?"启用":"停用"}</TableCell><TableCell>{money(row.limit_cents)}</TableCell><TableCell>{money(row.used_cents)}</TableCell><TableCell>{money(row.available_cents)}</TableCell><TableCell>{manage&&<Button variant="outline" size="sm" onClick={()=>open(row)}>设置额度</Button>}</TableCell></TableRow>)}{resource.data.items.length===0&&<TableRow><TableCell colSpan={6} className="py-12 text-center">暂无数据</TableCell></TableRow>}</TableBody></Table></div><AdminPagination total={resource.data.total} page={navigation.page} pageSize={size} onPageChange={navigation.setPage} onPageSizeChange={limit=>{navigation.setExtra({...navigation.extra,limit:String(limit)});navigation.setPage(0);}}/></>}</div><Dialog open={draft!==null} onOpenChange={open=>{if(!open&&!busy&&!draft?.pending)setDraft(null);}}><DialogContent><DialogHeader><DialogTitle>设置授权额度</DialogTitle><DialogDescription>{draft?.row.display_name||draft?.row.username} · 当前版本 {draft?.row.version}。保存只调整授信额度。</DialogDescription></DialogHeader>{draft&&<div className="space-y-4"><fieldset disabled={busy||draft.pending||!manage} className="space-y-4"><label className="grid gap-2 text-sm">授权额度（元）<Input inputMode="decimal" value={draft.amount} onChange={e=>edit({amount:e.target.value})}/></label><label className="grid gap-2 text-sm">调整备注<Textarea maxLength={240} value={draft.note} onChange={e=>edit({note:e.target.value})}/></label></fieldset>{draft.confirmed&&<p className="text-sm">确认将授权额度设置为 {money(cents(draft.amount)??0)}？</p>}{error&&<p role="alert" className="text-sm text-destructive">{error}</p>}{conflict&&<Button variant="outline" disabled={busy} onClick={()=>void refreshDraft()}>刷新账户版本并保留草稿</Button>}{draft.pending&&!busy&&<p className="text-sm">上次请求结果尚未确认，请以原请求重试确认结果。</p>}<DialogFooter><Button variant="outline" disabled={busy||draft.pending} onClick={()=>setDraft(null)}>取消</Button><Button disabled={busy||!manage} onClick={()=>void save()}>{busy?"保存中":draft.pending?"重试确认结果":draft.confirmed?"确认保存":"保存"}</Button></DialogFooter></div>}</DialogContent></Dialog></Workspace>;
}
