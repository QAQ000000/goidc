"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { RefreshCw } from "lucide-react";
import { useResource } from "@/hooks/use-resource";
import { date } from "@/lib/api";
import { AdminPagination } from "./admin-pagination";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";
import { Failure, Loading } from "./common";
import { Workspace } from "./workspace";

type Item = {source: string; id: number; service_id: number | null; customer_id: number | null; customer: string; product: string; action: string; description: string; status: string; updated_at: string; has_error: boolean};
type Result = {items: Item[]; total: number; limit: number; offset: number};
type Summary = {items: {source: string; total: number; pending: number; running: number; waiting: number; failed: number}[]};
const sources: Record<string,string> = {provision:"开通任务",email:"邮件队列",sms:"短信队列"};
const statuses: Record<string,string> = {pending:"待处理",queued:"排队中",running:"运行中",sending:"投递中",waiting_manual:"等待人工处理",waiting_verification:"等待核验",succeeded:"成功",sent:"已发送",failed:"失败",dead_letter:"死信",cancelled:"已取消",expired:"已过期"};
const actions: Record<string,string> = {create:"开通",suspend:"暂停",resume:"恢复",terminate:"终止",sync:"同步",upgrade:"升级",reinstall:"重装",reset_password:"重置密码",reboot:"重启",power_on:"开机",power_off:"关机",operation:"服务操作",notification:"通知投递"};
export function AdminJobsView({queue=false}:{queue?:boolean}) {
 const [params,setParams]=useState<URLSearchParams|null>(null);
 useEffect(()=>{const restore=()=>setParams(new URLSearchParams(window.location.search));restore();window.addEventListener("popstate",restore);return ()=>window.removeEventListener("popstate",restore);},[]);
 const query=params?.toString()??"";
 const resource=useResource<Result|Summary>(queue ? (params ? `/admin/jobs/queue?${query}` : null) : "/admin/jobs",false);
 function change(key:string,value:string){const next=new URLSearchParams(params??undefined);value?next.set(key,value):next.delete(key);if(key!=="offset")next.delete("offset");window.history.replaceState(null,"",`${window.location.pathname}?${next}`);setParams(next);}
 const limit=Number(params?.get("limit"))||25;
 const page=Math.floor((Number(params?.get("offset"))||0)/limit);
 return <Workspace admin title={queue?"任务队列":"任务状态概览"}><div className="space-y-5"><div className="flex justify-between gap-3"><div><h1 className="page-title">{queue?"任务队列":"任务状态概览"}</h1><p className="mt-2 text-sm text-muted-foreground">仅显示当前账号有权查看的服务及通知任务。概览为队列实时汇总，不代表定时任务执行日志。</p><p className="mt-2 text-sm text-muted-foreground">部分完成：定时执行趋势、逐次日志及统一重试尚未实现。失败任务请进入对应业务页核验，避免重复执行外部动作。</p></div><Button variant="outline" size="icon" aria-label="刷新任务" onClick={resource.refresh}><RefreshCw /></Button></div>
 {queue&&<div className="flex flex-wrap gap-3 border-y py-4"><Input className="w-60" aria-label="关键词" placeholder="客户、产品或动作关键词" value={params?.get("q")??""} onChange={e=>change("q",e.target.value)}/><select className="rounded border p-2 text-sm" aria-label="任务来源" value={params?.get("source")??""} onChange={e=>change("source",e.target.value)}><option value="">全部来源</option>{Object.entries(sources).map(([k,v])=><option key={k} value={k}>{v}</option>)}</select><select className="rounded border p-2 text-sm" aria-label="任务状态" value={params?.get("status")??""} onChange={e=>change("status",e.target.value)}><option value="">全部状态</option>{Object.entries(statuses).map(([k,v])=><option key={k} value={k}>{v}</option>)}</select>{["from","to"].map(key=><label key={key} className="text-sm">{key==="from"?"操作开始日期":"操作结束日期"}<Input type="date" value={params?.get(key)??""} onChange={e=>change(key,e.target.value)}/></label>)}<Button variant="outline" onClick={()=>{window.history.replaceState(null,"",window.location.pathname);setParams(new URLSearchParams());}}>重置</Button></div>}
 {resource.error?<Failure error={resource.error} retry={resource.refresh}/>:!resource.data?<Loading/>:queue?<><div className="overflow-x-auto border-y"><Table><TableHeader><TableRow>{["客户","产品","动作","描述","来源","状态","最后操作时间","操作"].map(v=><TableHead key={v}>{v}</TableHead>)}</TableRow></TableHeader><TableBody>{(resource.data as Result).items.map(item=><TableRow key={`${item.source}-${item.id}`}><TableCell>{item.customer}</TableCell><TableCell>{item.product}</TableCell><TableCell>{actions[item.action]??"业务任务"}</TableCell><TableCell>{item.description}{item.has_error&&<span className="block text-xs text-destructive">存在错误，请在业务页核验</span>}</TableCell><TableCell>{sources[item.source]}</TableCell><TableCell>{statuses[item.status]??"未知状态"}</TableCell><TableCell>{date(item.updated_at)}</TableCell><TableCell>{item.service_id&&item.source==="provision"?<Link className="text-primary underline" href={`/admin/services/${item.service_id}`}>查看服务</Link>:<span className="text-xs text-muted-foreground">暂不支持统一重试</span>}</TableCell></TableRow>)}{!(resource.data as Result).items.length&&<TableRow><TableCell colSpan={8} className="py-12 text-center">暂无符合条件的任务</TableCell></TableRow>}</TableBody></Table></div><AdminPagination total={(resource.data as Result).total} page={page} pageSize={limit} onPageChange={p=>change("offset",String(p*limit))} onPageSizeChange={n=>change("limit",String(n))}/></>:<div className="overflow-x-auto"><Table><TableHeader><TableRow>{["来源","总数","待处理","运行中","等待人工 / 核验","失败 / 死信"].map(v=><TableHead key={v}>{v}</TableHead>)}</TableRow></TableHeader><TableBody>{(resource.data as Summary).items.map(i=><TableRow key={i.source}><TableCell>{sources[i.source]}</TableCell><TableCell>{i.total}</TableCell><TableCell>{i.pending}</TableCell><TableCell>{i.running}</TableCell><TableCell>{i.waiting}</TableCell><TableCell>{i.failed}</TableCell></TableRow>)}{!(resource.data as Summary).items.length&&<TableRow><TableCell colSpan={6} className="py-12 text-center">暂无可见任务</TableCell></TableRow>}</TableBody></Table></div>}</div></Workspace>;
}
