"use client";
import { useEffect, useRef, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { api, ApiError, money, type PromoCode } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace, useAdminPermission, useAdminIdentity } from "./workspace";
import { Loading, Failure } from "./common";
import { AdminStatusTabs } from "./admin-status-tabs";
import { AdminPagination } from "./admin-pagination";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Checkbox } from "./ui/checkbox";
import { Textarea } from "./ui/textarea";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "./ui/dialog";
import { Table, TableHeader, TableHead, TableBody, TableRow, TableCell } from "./ui/table";
import { catalogSelectStyle } from "./catalog-admin";
function PageHeading({title,description,children}:{title:string;description:string;children:React.ReactNode}){return <div className="flex flex-wrap items-center justify-between gap-3"><div><h1 className="text-xl font-semibold">{title}</h1><p className="text-sm text-muted-foreground">{description}</p></div>{children}</div>;}
const statuses={all:"全部",active:"有效",expired:"过期",disabled:"停用",scheduled:"未开始",exhausted:"已耗尽"};
type Page={items:PromoCode[];total:number;limit:number;offset:number};
type Draft={code:string;discount_type:"percent"|"fixed"|"free";value:string;max_uses:string;starts_at:string;expires_at:string;active:boolean;notes:string;once_per_customer:boolean;only_new_customer:boolean;recurring:boolean;recurring_limit:string;upgrade_only:boolean};
type Attempt={key:string;body:string;path:string;method:"POST"|"PATCH";draft:Draft;editing:PromoCode|null};

const blank=():Draft=>({code:"",discount_type:"percent",value:"10",max_uses:"0",starts_at:"",expires_at:"",active:true,notes:"",once_per_customer:false,only_new_customer:false,recurring:false,recurring_limit:"0",upgrade_only:false});
function local(value:string|null){if(!value)return "";const d=new Date(value);return `${d.getFullYear()}-${String(d.getMonth()+1).padStart(2,"0")}-${String(d.getDate()).padStart(2,"0")}T${String(d.getHours()).padStart(2,"0")}:${String(d.getMinutes()).padStart(2,"0")}:${String(d.getSeconds()).padStart(2,"0")}`;}
function from(item:PromoCode):Draft{return {...blank(),...item,value:String(item.value),max_uses:String(item.max_uses),recurring_limit:String(item.recurring_limit),starts_at:local(item.starts_at),expires_at:local(item.expires_at)};}
export function PromoCodesView(){
 const identity=useAdminIdentity();
 return <PromoCodesPanel key={identity?.id||"unavailable"} actorID={identity?.id}/>;
}
function PromoCodesPanel({actorID}:{actorID:number|undefined}){
 const params=useSearchParams(),router=useRouter(),manage=useAdminPermission("promos.manage");
 const storageKey=`zjmfgo:promo-pending-write:v1:${actorID||"unavailable"}`;
 const q=params.get("q")||"",rawStatus=params.get("status")||"all",status=rawStatus in statuses?rawStatus:"all";
 const size=[25,50,100].includes(Number(params.get("limit")))?Number(params.get("limit")):25;
 const rawPage=Number(params.get("page")||0),page=Number.isSafeInteger(rawPage)&&rawPage>=0?rawPage:0;
 const {data,error,refresh}=useResource<Page>(`/admin/promo-codes/page?q=${encodeURIComponent(q)}&status=${status}&limit=${size}&offset=${page*size}`,false);
 const [search,setSearch]=useState(q),[open,setOpen]=useState(false),[editing,setEditing]=useState<PromoCode|null>(null),[draft,setDraft]=useState<Draft>(blank);
 const [busy,setBusy]=useState(false),[formError,setFormError]=useState(""),[conflict,setConflict]=useState(false),[notice,setNotice]=useState("");
 const [pending,setPending]=useState<Attempt|null>(null),[rejectedCreate,setRejectedCreate]=useState(false),attempt=useRef<Attempt|null>(null);
 useEffect(()=>{queueMicrotask(()=>setSearch(q));},[q]);
 useEffect(()=>{try{const raw=sessionStorage.getItem(storageKey);if(!raw){queueMicrotask(()=>setPending(null));return;}if(raw){const saved=JSON.parse(raw) as Attempt;if(saved.key&&saved.body&&saved.draft&&/^\/admin\/promo-codes(?:\/\d+)?$/.test(saved.path)&&["POST","PATCH"].includes(saved.method))queueMicrotask(()=>setPending(saved));}}catch{/* Never automatically resend. */}},[storageKey]);
 function navigate(changes:Record<string,string|number>){const next=new URLSearchParams(params.toString());for(const [key,value]of Object.entries(changes)){if(value===""||value==="all"||key==="page"&&value===0)next.delete(key);else next.set(key,String(value));}router.push(`/admin/promo-codes?${next}`);}
 function edit(item:PromoCode|null){setEditing(item);setDraft(item?from(item):blank());attempt.current=null;setFormError("");setConflict(false);setRejectedCreate(false);setOpen(true);}
 function recover(){if(!pending)return;attempt.current=pending;setEditing(pending.editing);setDraft(pending.draft);setFormError("上次提交结果未确认。核对列表后重试将使用原幂等键和原请求，不会自动提交。");setConflict(false);setRejectedCreate(false);setOpen(true);}
 function clear(){attempt.current=null;setPending(null);sessionStorage.removeItem(storageKey);}
 function close(){if(!busy){setOpen(false);setFormError("");setConflict(false);}}
 async function latest(){
  if(!editing)return;
  setBusy(true);
  try{
   let offset=0,item:PromoCode|undefined;
   while(!item){
    const result=await api<Page>(`/admin/promo-codes/page?q=${encodeURIComponent(editing.code)}&status=all&limit=100&offset=${offset}`);
    item=result.items.find(row=>row.id===editing.id);
    offset+=result.limit;
    if(item||offset>=result.total||result.items.length===0||result.limit<1)break;
   }
   if(!item)throw new Error("记录已不可见，请刷新列表");
   setEditing(item);setDraft(from(item));clear();setConflict(false);setFormError("");
  }catch(e){setFormError((e as Error).message);}finally{setBusy(false);}
 }
 async function submit(event:React.FormEvent){
  event.preventDefault();if(busy||!manage||conflict)return;
  if(pending&&!attempt.current){setFormError("请先恢复并核对上次未确认的提交");return;}
  try{
   // A recovered request is immutable: replay stored bytes, including original dates.
   if(!attempt.current){
    const unchanged=(key:"starts_at"|"expires_at")=>editing&&draft[key]===local(editing[key]);
    const start=unchanged("starts_at")?editing!.starts_at:draft.starts_at?new Date(draft.starts_at).toISOString():null;
    const end=unchanged("expires_at")?editing!.expires_at:draft.expires_at?new Date(draft.expires_at).toISOString():null;
    if(!unchanged("starts_at")&&!unchanged("expires_at")&&start&&end&&Date.parse(end)<=Date.parse(start)){setFormError("失效时间必须晚于开始时间");return;}
    const shared={max_uses:Number(draft.max_uses),starts_at:start,expires_at:end,notes:draft.notes};
    const body=JSON.stringify(editing?{...shared,active:draft.active,expected_version:editing.version}:{...shared,starts_at:start||"",expires_at:end||"",code:draft.code.trim().toUpperCase(),discount_type:draft.discount_type,value:draft.discount_type==="free"?0:Number(draft.value),once_per_customer:draft.once_per_customer,only_new_customer:draft.only_new_customer,recurring:draft.recurring,recurring_limit:draft.recurring?Number(draft.recurring_limit):0,upgrade_only:draft.upgrade_only});
    attempt.current={key:crypto.randomUUID(),body,path:editing?`/admin/promo-codes/${editing.id}`:"/admin/promo-codes",method:editing?"PATCH":"POST",draft,editing};
   }
   const current=attempt.current;
   sessionStorage.setItem(storageKey,JSON.stringify(current));setPending(current);setBusy(true);setFormError("");
   await api(current.path,{method:current.method,headers:{"Idempotency-Key":current.key},body:current.body});
   clear();setOpen(false);setNotice(editing?"优惠码已保存":"优惠码已创建");refresh();
  }catch(e){
   setFormError((e as Error).message);
   if(e instanceof ApiError&&e.status>=400&&e.status<500&&![408,429].includes(e.status)){
    if(["promo_version_conflict","idempotency_conflict"].includes(e.code)){
     setConflict(true);setRejectedCreate(!editing&&e.code==="idempotency_conflict");
    }else clear();
   }
  }finally{setBusy(false);}
 }
 function abandonRejectedCreate(discard:boolean){
  if(!rejectedCreate||busy)return;
  clear();setConflict(false);setRejectedCreate(false);setFormError("");
  if(discard){setDraft(blank());setOpen(false);}
 }
 function field<K extends keyof Draft>(key:K,value:Draft[K]){setDraft(old=>({...old,[key]:value}));}
 return <Workspace title="优惠码" admin><div className="min-w-0 space-y-5"><PageHeading title="优惠码" description="购买折扣、循环续费和升级优惠">{manage&&<Button disabled={!!pending} onClick={()=>edit(null)}>新增优惠码</Button>}</PageHeading>{notice&&<p role="status" className="text-sm text-emerald-700">{notice}</p>}
 {manage&&pending&&!open&&<div className="border p-3 space-y-2 text-sm" role="status"><p>存在未确认提交，刷新不会自动重发。</p><Button variant="outline" onClick={recover}>恢复上次提交</Button></div>}
 <AdminStatusTabs label="优惠码状态标签" value={status} states={{active:"有效",expired:"过期"}} onChange={value=>navigate({status:value,page:0})}/>
 <form className="flex flex-wrap gap-2" onSubmit={e=>{e.preventDefault();navigate({q:search.trim(),page:0});}}><Input className="w-full sm:w-72 min-w-0" aria-label="搜索优惠码或备注" placeholder="搜索优惠码或备注" value={search} onChange={e=>setSearch(e.target.value)}/><Button variant="outline">搜索</Button><label className="flex shrink-0 items-center gap-2 whitespace-nowrap text-sm">状态<select aria-label="优惠码状态" className={catalogSelectStyle} value={status} onChange={e=>navigate({status:e.target.value,page:0})}>{Object.entries(statuses).map(([key,label])=><option key={key} value={key}>{label}</option>)}</select></label></form>
 {error?<Failure error={error} retry={refresh}/>:!data?<Loading/>:<><div className="data-surface max-w-full overflow-x-auto"><Table className="min-w-[1120px]"><TableHeader><TableRow>{["ID","优惠码","类型","价值","循环优惠","已使用 / 最大次数","开始时间","失效时间","状态","操作"].map(label=><TableHead key={label}>{label}</TableHead>)}</TableRow></TableHeader><TableBody>{!data.items.length?<TableRow><TableCell colSpan={10} className="text-center h-28">暂无优惠码{page>0&&<Button variant="outline" onClick={()=>navigate({page:0})}>返回第一页</Button>}</TableCell></TableRow>:data.items.map(item=><TableRow key={item.id}><TableCell>{item.id}</TableCell><TableCell className="font-mono break-all max-w-52">{item.code}{item.upgrade_only&&<p className="text-xs font-sans">仅商品升级</p>}</TableCell><TableCell>{item.discount_type==="percent"?"百分比":item.discount_type==="fixed"?"固定金额":"免初装费"}</TableCell><TableCell>{item.discount_type==="percent"?`${item.value}%`:item.discount_type==="fixed"?money(item.value):"初装费"}</TableCell><TableCell>{item.recurring?item.recurring_limit?`${item.recurring_limit} 次 / 件服务`:"每件服务不限次数":"不循环"}</TableCell><TableCell>{item.used_count} / {item.max_uses||"不限"}</TableCell><TableCell>{item.starts_at?new Date(item.starts_at).toLocaleString():"不限"}</TableCell><TableCell>{item.expires_at?new Date(item.expires_at).toLocaleString():"不限"}</TableCell><TableCell>{statuses[item.status as keyof typeof statuses]||"未知"}</TableCell><TableCell>{manage?<Button size="sm" variant="outline" disabled={!!pending} onClick={()=>edit(item)}>编辑</Button>:"只读"}</TableCell></TableRow>)}</TableBody></Table></div><AdminPagination total={data.total} page={page} pageSize={size} onPageChange={value=>navigate({page:value})} onPageSizeChange={value=>navigate({limit:value,page:0})}/></>}
 <Dialog open={open&&manage} onOpenChange={value=>{if(!value)close();}}><DialogContent showCloseButton={!busy} className="max-h-[90dvh] overflow-y-auto min-w-0"><DialogHeader><DialogTitle>{editing?"编辑优惠码":"新增优惠码"}</DialogTitle><DialogDescription>{editing?"代码、优惠价值和适用条件不可更改。加载最新将替换当前草稿。":"升级优惠仅作用于升级差额。时间使用浏览器本地时区，留空表示不限。"}</DialogDescription></DialogHeader><form onSubmit={submit} className="space-y-4 min-w-0"><fieldset disabled={busy||!!pending} className="grid sm:grid-cols-2 gap-4 min-w-0">
 <label className="grid gap-2 text-sm min-w-0">优惠码<Input aria-label="优惠码" required pattern="[A-Za-z0-9](?:[A-Za-z0-9_]|-){2,49}" maxLength={50} disabled={!!editing} value={draft.code} onChange={e=>field("code",e.target.value.toUpperCase())}/></label><label className="grid gap-2 text-sm">优惠类型<select aria-label="优惠类型" className={catalogSelectStyle} disabled={!!editing} value={draft.discount_type} onChange={e=>{field("discount_type",e.target.value as Draft["discount_type"]);field("recurring",false);field("upgrade_only",false);}}><option value="percent">百分比</option><option value="fixed">固定金额（分）</option><option value="free">免初装费</option></select></label>
 <label className="grid gap-2 text-sm">优惠值<Input aria-label="优惠值" required type="number" step={1} min={draft.discount_type==="free"?0:1} max={draft.discount_type==="percent"?100:undefined} disabled={!!editing||draft.discount_type==="free"} value={draft.value} onChange={e=>field("value",e.target.value)}/></label><label className="grid gap-2 text-sm">最大使用次数（0为不限）<Input aria-label="最大使用次数" required type="number" min={0} step={1} value={draft.max_uses} onChange={e=>field("max_uses",e.target.value)}/></label>
 {([['starts_at','开始时间'],['expires_at','失效时间']] as const).map(([key,label])=><label key={key} className="grid gap-2 text-sm min-w-0">{label}（本地时区）<Input className="min-w-0 max-w-full" aria-label={label} type="datetime-local" step={1} value={draft[key]} onChange={e=>field(key,e.target.value)}/></label>)}
 {editing?<label className="flex items-center gap-2 text-sm"><Checkbox aria-label="启用优惠码" checked={draft.active} onCheckedChange={v=>field("active",v===true)}/>启用优惠码</label>:<>{([['once_per_customer','每客户一次'],['only_new_customer','仅新客户'],['recurring','循环续费优惠'],['upgrade_only','仅用于商品升级']] as const).map(([key,label])=><label key={key} className="flex items-center gap-2 text-sm"><Checkbox checked={draft[key]} disabled={key==="recurring"&&(draft.discount_type==="free"||draft.upgrade_only)||key==="upgrade_only"&&(draft.discount_type==="free"||draft.recurring)} onCheckedChange={v=>field(key,v===true)}/>{label}</label>)}{draft.recurring&&<label className="grid gap-2 text-sm">每件服务优惠续费次数（0不限）<Input aria-label="优惠续费次数" required type="number" min={0} max={10000} value={draft.recurring_limit} onChange={e=>field("recurring_limit",e.target.value)}/></label>}</>}
 <label className="grid gap-2 text-sm sm:col-span-2">备注<Textarea aria-label="备注" maxLength={2000} value={draft.notes} onChange={e=>field("notes",e.target.value)}/></label></fieldset>{formError&&<p role="alert" className="text-sm text-destructive break-all">{formError}</p>}{rejectedCreate&&<div className="space-y-2 text-sm"><p>服务器已明确拒绝本请求的幂等键。请先核对列表，确认已有记录与本次草稿的关系；只有显式放弃原请求后，下一次保存才会使用新键。</p><Button type="button" variant="outline" disabled={busy} onClick={()=>abandonRejectedCreate(false)}>已核对，放弃原请求并重新编辑</Button><Button type="button" variant="outline" disabled={busy} onClick={()=>abandonRejectedCreate(true)}>放弃本次草稿</Button></div>}{(conflict||pending)&&editing&&<Button type="button" variant="outline" disabled={busy} onClick={()=>void latest()}>加载最新并替换草稿</Button>}<DialogFooter><Button type="button" variant="outline" disabled={busy} onClick={close}>取消</Button><Button type="submit" disabled={busy||conflict}>{busy?"保存中":pending?"使用原请求重试":"保存"}</Button></DialogFooter></form></DialogContent></Dialog>
 </div></Workspace>;
}
