"use client";

import Link from "next/link";
import { Search, RefreshCw, Server } from "lucide-react";
import { useAdminListNavigation } from "@/hooks/use-admin-list-navigation";
import { useResource } from "@/hooks/use-resource";
import { Workspace, useAdminPermission } from "./workspace";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { AdminPagination } from "./admin-pagination";
import { Input } from "./ui/input";
import { Table, TableHeader, TableHead, TableBody, TableRow, TableCell } from "./ui/table";

type ResourceItem = {
  id:number; name:string; domain:string; status:string; username:string;
  upstream_id:string; customer_name:string; supplier_id:number|null; supplier_name:string|null;
  resource_info?:{ips?:string[];dedicated_ip?:string;assigned_ips?:string;name?:string;power?:string};
};
type ResourcePageData = { total:number; items:ResourceItem[]; suppliers:{id:number;name:string}[]; limit:number; offset:number };
const states:Record<string,string>={pending_payment:"待付款",queued:"排队中",provisioning:"开通中",active:"运行中",suspended:"已暂停",terminated:"已终止",failed:"失败",cancelled:"已取消",import_review_required:"待核验"};

const stateKeys=Object.keys(states);
const defaults={ip:"",supplier_id:"",page_size:"20"};
export function ResourcesView() {
  const canOpenServiceDetails=useAdminPermission("services.view");
  const nav=useAdminListNavigation(true,stateKeys,"",defaults);
  const {draft,setDraft,query,setQuery,status,setStatus,page,setPage,extra,setExtra}=nav;
  const ip=extra.ip, supplier=extra.supplier_id;
  const limit=[10,20,50,100].includes(Number(extra.page_size))?Number(extra.page_size):20;
  const offset=page*limit;
  const setIP=(value:string)=>setExtra({...extra,ip:value});
  const setSupplier=(value:string)=>setExtra({...extra,supplier_id:value});
  const path=`/admin/resources?q=${encodeURIComponent(query)}&ip=${encodeURIComponent(ip)}&supplier_id=${supplier}&status=${status}&limit=${limit}&offset=${offset}`;
  const resources=useResource<ResourcePageData>(nav.ready?path:null,false);
  function applySearch(event:React.FormEvent) { event.preventDefault(); setQuery(draft.trim()); setPage(0); }
  function updateFilter(setter:(value:string)=>void,value:string) { setter(value); setPage(0); }
  return <Workspace title="服务器资源" admin><div className="space-y-5">
    <div className="flex flex-wrap items-end justify-between gap-3"><div><h1 className="page-title">服务器资源</h1><p className="mt-2 text-sm text-muted-foreground">查看已登记服务的资源摘要与上游供应商。</p></div><Button type="button" variant="outline" size="icon" aria-label="刷新服务器资源" title="刷新" onClick={resources.refresh}><RefreshCw/></Button></div>
    <div className="flex flex-wrap gap-2" aria-label="资源视图切换"><Button variant="default" aria-current="page">已购服务资源</Button><Button asChild variant="outline"><Link href="/admin/manual-resources">人工库存</Link></Button></div>
    <div className="flex flex-wrap items-end gap-3 border-y py-4">
      <form onSubmit={applySearch} className="flex min-w-[240px] flex-1 gap-2"><Input value={draft} onChange={e=>setDraft(e.target.value)} placeholder="搜索资源名称、域名、客户或上游 ID" aria-label="搜索服务器资源"/><Button type="submit"><Search/>搜索</Button></form>
      <label className="grid gap-1 text-sm">IP 地址<Input className="w-48" value={ip} onChange={e=>updateFilter(setIP,e.target.value)} placeholder="筛选 IP" aria-label="按 IP 筛选"/></label>
      <label className="grid gap-1 text-sm">供应商<select className="h-10 min-w-44 rounded-md border bg-background px-3 text-sm" value={supplier} onChange={e=>updateFilter(setSupplier,e.target.value)} aria-label="按供应商筛选"><option value="">全部供应商</option>{resources.data?.suppliers.map(item=><option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
      <label className="grid gap-1 text-sm">状态<select className="h-10 min-w-36 rounded-md border bg-background px-3 text-sm" value={status} onChange={e=>updateFilter(setStatus,e.target.value)} aria-label="按资源状态筛选"><option value="">全部状态</option>{Object.entries(states).map(([key,label])=><option key={key} value={key}>{label}</option>)}</select></label>
      <Button variant="outline" onClick={()=>{setDraft("");setQuery("");setStatus("");setExtra(defaults);setPage(0);}}>重置筛选</Button>
    </div>
    {resources.error?<Failure error={resources.error} retry={resources.refresh}/>:!resources.data?<Loading/>:<>
      <div className="overflow-x-auto border-y"><Table><TableHeader><TableRow><TableHead>资源</TableHead><TableHead>客户</TableHead><TableHead>IP 地址</TableHead><TableHead>供应商 / 上游 ID</TableHead><TableHead>状态</TableHead><TableHead>摘要</TableHead><TableHead>详情</TableHead></TableRow></TableHeader><TableBody>
        {resources.data.items.map(item=>{const ips=[item.resource_info?.dedicated_ip,...(item.resource_info?.ips??[])].filter((value):value is string=>!!value);return <TableRow key={item.id}><TableCell className="min-w-44"><p className="flex items-center gap-2 font-medium"><Server size={15}/>{item.name||item.resource_info?.name||`服务 #${item.id}`}</p><p className="mt-1 text-xs text-muted-foreground">#{item.id} · {item.domain||"未设置域名"}</p></TableCell><TableCell>{item.customer_name||"—"}</TableCell><TableCell className="min-w-40 font-mono text-xs">{ips.length?ips.join(" · "):item.resource_info?.assigned_ips||"—"}</TableCell><TableCell className="min-w-40">{item.supplier_name||"本地 / 未绑定"}{item.upstream_id&&<p className="mt-1 break-all font-mono text-xs text-muted-foreground">{item.upstream_id}</p>}</TableCell><TableCell>{states[item.status]??item.status}</TableCell><TableCell className="min-w-32 text-sm">{item.username&&<p>登录用户：{item.username}</p>}{item.resource_info?.power&&<p className="text-muted-foreground">电源：{item.resource_info.power}</p>}{!item.username&&!item.resource_info?.power&&"—"}</TableCell><TableCell>{canOpenServiceDetails?<Link className="text-primary hover:underline" href={`/admin/services/${item.id}`}>查看服务详情</Link>:"—"}</TableCell></TableRow>})}
        {resources.data.items.length===0&&<TableRow><TableCell colSpan={7} className="h-32 text-center text-muted-foreground">没有符合条件的服务器资源</TableCell></TableRow>}
      </TableBody></Table></div>
      <AdminPagination total={resources.data.total} page={page} pageSize={limit} pageSizes={[10,20,50,100]} onPageChange={setPage} onPageSizeChange={value=>{setExtra({...extra,page_size:String(value)});setPage(0);}}/>

    </>}
  </div></Workspace>;
}
