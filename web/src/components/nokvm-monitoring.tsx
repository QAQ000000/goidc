"use client";

import { useState } from "react";
import { RefreshCw, Download, Activity } from "lucide-react";
import { Tabs } from "radix-ui";
import { Area, AreaChart, CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { type NOKVMMonitoring, type Service } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Button } from "./ui/button";
import { Failure, Loading } from "./common";
import { catalogSelectStyle } from "./catalog-admin";

type Metric="cpu"|"network"|"disk";
type Point={at:number;cpu:number|null;incoming:number|null;outgoing:number|null;read:number|null;write:number|null};
const periods=[{value:"1h",label:"1小时"},{value:"6h",label:"6小时"},{value:"24h",label:"24小时"},{value:"7d",label:"7天"}];
const timestamp=(value:string|number)=>new Date(value).toLocaleString("zh-CN",{month:"2-digit",day:"2-digit",hour:"2-digit",minute:"2-digit"});
function gib(value:string){
 const bytes=BigInt(value),unit=BigInt(1)<<BigInt(30),fraction=(bytes%unit)*BigInt(100)/unit;
 return `${bytes/unit}.${fraction<BigInt(10)?"0":""}${fraction} GiB`;
}

export function NOKVMMonitoringPanel({service,admin}:{service:Service;admin:boolean}){
 const [period,setPeriod]=useState("1h"),[metric,setMetric]=useState<Metric>("cpu"),[disk,setDisk]=useState("vda");
 const monitoring=useResource<NOKVMMonitoring>(`${admin?"/admin":""}/services/${service.id}/monitoring?period=${period}`,false);
 const data=monitoring.data;
 const points:Point[]=(data?.samples??[]).map(p=>({at:Date.parse(p.at),cpu:p.cpu_percent,incoming:p.network_in_kbps,outgoing:p.network_out_kbps,read:p.disks[disk]?.read_kbps??null,write:p.disks[disk]?.write_kbps??null}));
 const usage=data?.usage;
 const over=usage&&usage.limit_bytes!==null&&BigInt(usage.limit_bytes)>BigInt(0)&&BigInt(usage.total_bytes)>BigInt(usage.limit_bytes);
 function download(){
  if(!data)return;
  const rows=[["time_utc","cpu_percent","network_in_KB_s","network_out_KB_s","vda_read_KB_s","vda_write_KB_s","vdb_read_KB_s","vdb_write_KB_s"],...data.samples.map(p=>[p.at,p.cpu_percent,p.network_in_kbps,p.network_out_kbps,p.disks.vda?.read_kbps,p.disks.vda?.write_kbps,p.disks.vdb?.read_kbps,p.disks.vdb?.write_kbps])];
  const url=URL.createObjectURL(new Blob(["\uFEFF"+rows.map(row=>row.map(value=>value??"").join(",")).join("\r\n")],{type:"text/csv;charset=utf-8"}));
  const link=document.createElement("a");link.href=url;link.download=`service-${service.id}-monitoring-${period}.csv`;link.click();setTimeout(()=>URL.revokeObjectURL(url),1000);
 }
 return <section aria-label="资源监控" className="mb-6 min-w-0 border-y py-6 space-y-5">
  <div className="flex flex-wrap items-center justify-between gap-3"><h2 className="font-semibold flex items-center gap-2"><Activity size={18}/>资源监控</h2><div className="flex gap-2"><Button variant="ghost" size="icon" aria-label="导出监控 CSV" title="导出监控 CSV" disabled={!data?.samples.length} onClick={download}><Download size={16}/></Button><Button variant="outline" size="icon" aria-label="刷新监控" title="刷新监控" onClick={monitoring.refresh}><RefreshCw size={16}/></Button></div></div>
  <div role="group" aria-label="监控时间范围" className="inline-flex max-w-full border rounded-md p-1 gap-1">{periods.map(p=><Button key={p.value} variant={period===p.value?"secondary":"ghost"} size="sm" aria-pressed={period===p.value} onClick={()=>setPeriod(p.value)}>{p.label}</Button>)}</div>
  {monitoring.error?<Failure error={monitoring.error} retry={monitoring.refresh}/>:!data?<div className="h-80"><Loading/></div>:<>
   <dl className="grid grid-cols-2 lg:grid-cols-4 gap-x-5 gap-y-4 border-y py-4 text-sm">
    {[{label:"累计入站",value:usage?gib(usage.in_bytes):"暂不可用"},{label:"累计出站",value:usage?gib(usage.out_bytes):"暂不可用"},{label:"累计流量",value:usage?gib(usage.total_bytes):"暂不可用"},{label:"供应商流量限额",value:usage?.limit_bytes===null||!usage?"暂不可用":usage.limit_bytes==="0"?"不限":gib(usage.limit_bytes)}].map(item=><div key={item.label} className="min-w-0"><dt className="text-muted-foreground text-xs">{item.label}</dt><dd className={`mt-2 font-mono break-all ${over&&item.label==="累计流量"?"text-destructive":""}`}>{item.value}</dd></div>)}
   </dl>
   <Tabs.Root value={metric} onValueChange={value=>setMetric(value as Metric)} className="min-w-0">
    <div className="flex flex-wrap items-center justify-between gap-3 mb-4"><Tabs.List aria-label="监控指标" className="flex border-b gap-5">{([{value:"cpu",label:"CPU"},{value:"network",label:"网络"},{value:"disk",label:"磁盘 I/O"}] as const).map(item=><Tabs.Trigger key={item.value} value={item.value} className="border-b-2 border-transparent px-1 py-2 text-sm text-muted-foreground data-[state=active]:border-primary data-[state=active]:text-foreground">{item.label}</Tabs.Trigger>)}</Tabs.List>{metric==="disk"&&<select aria-label="监控磁盘" className={`${catalogSelectStyle} w-40`} value={disk} onChange={e=>setDisk(e.target.value)}><option value="vda">系统盘 vda</option><option value="vdb">数据盘 vdb</option></select>}</div>
    {(["cpu","network","disk"] as const).map(kind=><Tabs.Content key={kind} value={kind}><MonitoringPlot metric={kind} points={points}/></Tabs.Content>)}
   </Tabs.Root>
   <div className="flex flex-wrap justify-between gap-2 text-xs text-muted-foreground"><span>{timestamp(data.start)} 至 {timestamp(data.end)} · {data.samples.length} 条采样</span><span>更新于 {timestamp(data.fetched_at)}</span></div>
  </>}
 </section>;
}

function MonitoringPlot({metric,points}:{metric:Metric;points:Point[]}){
 const keys=metric==="cpu"?["cpu"]:metric==="network"?["incoming","outgoing"]:["read","write"];
 const available=points.some(p=>keys.some(key=>p[key as keyof Point]!==null));
 const unit=metric==="cpu"?"%":"KB/s";
 const axes=<><CartesianGrid vertical={false} stroke="#e5e7eb"/><XAxis dataKey="at" type="number" domain={["dataMin","dataMax"]} scale="time" tickFormatter={value=>new Date(value).toLocaleTimeString("zh-CN",{hour:"2-digit",minute:"2-digit"})} minTickGap={45} tick={{fontSize:11}} axisLine={false} tickLine={false}/><YAxis width={55} domain={metric==="cpu"?[0,(max:number)=>Math.max(100,max)]:[0,"auto"]} tick={{fontSize:11}} axisLine={false} tickLine={false}/><Tooltip labelFormatter={value=>timestamp(Number(value))} formatter={(value,name)=>[`${Number(value).toLocaleString("zh-CN",{maximumFractionDigits:2})} ${unit}`,name]} contentStyle={{borderRadius:6,fontSize:12}}/></>;
 return <div className="min-w-0"><div className="flex justify-between gap-2 text-xs text-muted-foreground mb-3"><span>{unit}</span><span>{metric==="cpu"?"CPU使用率":metric==="network"?"入站 · 出站":"读取 · 写入"}</span></div><div className="h-64 min-w-0 w-full" role="img" aria-label={`${metric==="cpu"?"CPU使用率":metric==="network"?"网络速率":"磁盘速率"}监控图表`}>{!available?<div role="status" className="h-full flex items-center justify-center text-sm text-muted-foreground">暂无监测数据</div>:<ResponsiveContainer width="100%" height="100%" minWidth={0}>{metric==="network"?<AreaChart data={points} margin={{top:5,right:5,left:0,bottom:0}}>{axes}<Area dataKey="incoming" name="入站" stroke="#2563eb" fill="#2563eb" fillOpacity={0.08} strokeWidth={2} connectNulls={false} isAnimationActive={false}/><Area dataKey="outgoing" name="出站" stroke="#dc755c" fill="#dc755c" fillOpacity={0.08} strokeWidth={2} connectNulls={false} isAnimationActive={false}/></AreaChart>:<LineChart data={points} margin={{top:5,right:5,left:0,bottom:0}}>{axes}{metric==="cpu"?<Line dataKey="cpu" name="CPU" stroke="#167660" strokeWidth={2} dot={false} connectNulls={false} isAnimationActive={false}/>:<><Line dataKey="read" name="读取" stroke="#2563eb" strokeWidth={2} dot={false} connectNulls={false} isAnimationActive={false}/><Line dataKey="write" name="写入" stroke="#dc755c" strokeWidth={2} dot={false} connectNulls={false} isAnimationActive={false}/></>}</LineChart>}</ResponsiveContainer>}</div></div>;
}
