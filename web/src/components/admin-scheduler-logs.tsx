"use client";
import {useEffect,useState} from "react";
import {useResource} from "@/hooks/use-resource";
import {date} from "@/lib/api";
import {Workspace} from "./workspace";
import {AdminPagination} from "./admin-pagination";
import {Failure,Loading} from "./common";
import {Button} from "./ui/button";
import {Input} from "./ui/input";
import {Table,TableBody,TableCell,TableHead,TableHeader,TableRow} from "./ui/table";
const tasks:Record<string,string>={affiliate_settlement:"推介佣金结算",service_expiry_notifications:"服务到期通知调度",billing_notifications:"账单通知调度",supplier_sync_schedule:"供应商同步调度",overdue_service_suspensions:"逾期暂停入队",automatic_renewals:"自动续费处理",automatic_payment_reconciliation:"自动支付查单"};
const states:Record<string,string>={running:"运行中 / 结束未确认",succeeded:"成功",failed:"失败",interrupted:"已中断"};
type Run={id:number;task_key:string;started_at:string;finished_at:string|null;duration_ms:number|null;status:string;affected_count:number|null;error_code:string};
type Result={items:Run[];total:number};
type Trend={items:{day:string;total:number;succeeded:number;failed:number;running:number;interrupted:number}[]};
export function AdminSchedulerLogs(){
 const [params,setParams]=useState<URLSearchParams|null>(null);
 useEffect(()=>{const restore=()=>setParams(new URLSearchParams(window.location.search));restore();window.addEventListener("popstate",restore);return()=>window.removeEventListener("popstate",restore);},[]);
 const query=params?.toString();const logs=useResource<Result>(params?`/admin/logs/jobs?${query}`:null,false);const trend=useResource<Trend>(params?`/admin/logs/jobs/trend?${query}`:null,false);
 const limit=Number(params?.get("limit"))||25;const page=Math.floor((Number(params?.get("offset"))||0)/limit);
 function change(key:string,value:string){const next=new URLSearchParams(params??undefined);if(value)next.set(key,value);else next.delete(key);if(key!=="offset")next.delete("offset");window.history.replaceState(null,"",`${window.location.pathname}?${next}`);setParams(next);}
 return <Workspace admin title="定时任务日志"><div className="min-w-0 max-w-full space-y-5"><h1 className="page-title">定时任务日志</h1><p className="text-sm text-muted-foreground">记录启用后真实发生的调度执行；未回填历史。只供超级管理员查看。通知调度成功表示入队完成，不代表投递成功；供应商同步调度成功不代表外部操作成功。进程异常退出可能保留运行中记录，不能据此自动重试。</p><div className="flex flex-wrap gap-3"><select aria-label="任务类别" value={params?.get("task")??""} onChange={e=>change("task",e.target.value)}><option value="">全部任务</option>{Object.entries(tasks).map(([k,v])=><option key={k} value={k}>{v}</option>)}</select><select aria-label="运行状态" value={params?.get("status")??""} onChange={e=>change("status",e.target.value)}><option value="">全部状态</option>{Object.entries(states).map(([k,v])=><option key={k} value={k}>{v}</option>)}</select>{["from","to"].map(key=><label key={key}>{key==="from"?"开始日期（上海）":"结束日期（上海）"}<Input type="date" value={params?.get(key)??""} onChange={e=>change(key,e.target.value)}/></label>)}<Button onClick={()=>{logs.refresh();trend.refresh();}}>刷新</Button><Button variant="outline" onClick={()=>{window.history.replaceState(null,"",window.location.pathname);setParams(new URLSearchParams());}}>清空筛选</Button></div>
 {logs.error?<Failure error={logs.error} retry={logs.refresh}/>:!logs.data?<Loading/>:<><div className="min-w-0 overflow-x-auto"><Table className="min-w-[900px]"><TableHeader><TableRow>{["ID","任务","开始时间","结束时间","耗时（毫秒）","状态","处理数","安全错误码"].map(x=><TableHead key={x}>{x}</TableHead>)}</TableRow></TableHeader><TableBody>{logs.data.items.map(i=><TableRow key={i.id}><TableCell>{i.id}</TableCell><TableCell>{tasks[i.task_key]}</TableCell><TableCell>{date(i.started_at)}</TableCell><TableCell>{i.finished_at?date(i.finished_at):"—"}</TableCell><TableCell>{i.duration_ms??"—"}</TableCell><TableCell>{states[i.status]}</TableCell><TableCell>{i.affected_count??"—"}</TableCell><TableCell>{i.error_code||"—"}</TableCell></TableRow>)}</TableBody></Table></div>{!logs.data.items.length && <p role="status" className="py-8 text-center text-sm text-muted-foreground">暂无真实调度记录</p>}<AdminPagination total={logs.data.total} page={page} pageSize={limit} onPageChange={p=>change("offset",String(p*limit))} onPageSizeChange={n=>change("limit",String(n))}/></>}
 <h2 className="text-lg font-semibold">近31天执行趋势（上海时间）</h2>{trend.error?<Failure error={trend.error} retry={trend.refresh}/>:!trend.data?<Loading/>:<div className="min-w-0 overflow-x-auto"><Table className="min-w-[600px]"><TableHeader><TableRow>{["日期","总数","成功","失败","运行中","中断"].map(x=><TableHead key={x}>{x}</TableHead>)}</TableRow></TableHeader><TableBody>{trend.data.items.map(i=><TableRow key={i.day}><TableCell>{i.day}</TableCell><TableCell>{i.total}</TableCell><TableCell>{i.succeeded}</TableCell><TableCell>{i.failed}</TableCell><TableCell>{i.running}</TableCell><TableCell>{i.interrupted}</TableCell></TableRow>)}{!trend.data.items.length&&<TableRow><TableCell colSpan={6}>暂无趋势数据</TableCell></TableRow>}</TableBody></Table></div>}</div></Workspace>;
}
