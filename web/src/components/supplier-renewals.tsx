"use client";
import Link from "next/link";
import { useAdminListNavigation } from "@/hooks/use-admin-list-navigation";
import { useResource } from "@/hooks/use-resource";
import { money } from "@/lib/api";
import { Workspace, PermissionGate } from "./workspace";
import { Failure, Loading, Status } from "./common";
import { AdminPagination } from "./admin-pagination";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Table, TableHeader, TableHead, TableBody, TableRow, TableCell } from "./ui/table";
const states=["pending","paid","cancelled","refunded"];
const defaults={limit:"25",supplier_id:"",customer_id:"",date_from:"",date_to:""};
type Item={id:number;order_no:string;customer_name:string;product_name:string;service_domain:string;service_ips:string;created_at:string;total_cents:number;invoice_id:number;status:string;payment_method:string;supplier_name:string;supplier_id:string;cost_cents:number|null;upstream_renewal_status:string|null};
export function SupplierRenewalsView(){
 const nav=useAdminListNavigation(true,states,"all",defaults);
 const limit=[25,50,100].includes(Number(nav.extra.limit))?Number(nav.extra.limit):25;
 const params=new URLSearchParams({q:nav.query,limit:String(limit),offset:String(nav.page*limit)});
 for(const key of ["supplier_id","customer_id","date_from","date_to"])if(nav.extra[key])params.set(key,nav.extra[key]);
 if(nav.status!=="all")params.set("status",nav.status);
 const resource=useResource<{items:Item[];total:number}>(nav.ready?`/admin/supplier-renewals?${params}`:null,false);
 return <Workspace title="供应商续费订单" admin><div className="space-y-5"><div className="flex flex-wrap justify-between gap-3"><div><h1 className="page-title">供应商续费订单</h1><p className="mt-2 text-sm text-muted-foreground">付款后自动向上游续费；查看上游任务状态确认结果。成功核验后记录上游成本。</p></div><Button variant="outline" asChild><Link href="/admin/suppliers/orders">供应商订单</Link></Button></div>
 <form className="flex flex-wrap gap-3" onSubmit={e=>{e.preventDefault();nav.setQuery(nav.draft);nav.setPage(0);}}><Input className="sm:max-w-sm" aria-label="搜索续费订单" placeholder="账单号、客户、产品或邮箱" value={nav.draft} onChange={e=>nav.setDraft(e.target.value)}/><Button>搜索</Button><select aria-label="续费状态" value={nav.status} onChange={e=>{nav.setStatus(e.target.value);nav.setPage(0);}}><option value="all">全部状态</option>{states.map(value=><option key={value} value={value}>{{pending:"待支付",paid:"已支付",cancelled:"已取消",refunded:"已退款"}[value]}</option>)}</select></form>
 <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">{[{key:"supplier_id",label:"供应商ID",type:"number"},{key:"customer_id",label:"客户ID",type:"number"},{key:"date_from",label:"开始日期",type:"date"},{key:"date_to",label:"结束日期",type:"date"}].map(field=><label key={field.key} className="grid gap-1 text-sm">{field.label}<Input type={field.type} min={field.type==="number"?1:undefined} aria-label={`续费${field.label}`} value={nav.extra[field.key]} onChange={e=>{nav.setExtra({...nav.extra,[field.key]:e.target.value});nav.setPage(0);}}/></label>)}<Button variant="outline" onClick={()=>{nav.setExtra(defaults);nav.setQuery("");nav.setDraft("");nav.setStatus("all");nav.setPage(0);}}>重置筛选</Button></div>
 {resource.error?<Failure error={resource.error} retry={resource.refresh}/>:!resource.data?<Loading/>:<><div className="overflow-x-auto border-y"><Table><TableHeader><TableRow>{["ID / 账单","客户","产品 / 供应商","IP / 域名","续费账单时间","金额","付款方式 / 状态","成本"].map(label=><TableHead key={label}>{label}</TableHead>)}</TableRow></TableHeader><TableBody>{resource.data.items.map(item=><TableRow key={item.id}><TableCell>#{item.id}<PermissionGate permission="invoices.view"><Link className="block text-primary" href={`/admin/invoices/${item.invoice_id}?supplier_return=${encodeURIComponent('/admin/suppliers/renewals'+nav.suffix)}`}>{item.order_no}</Link></PermissionGate></TableCell><TableCell>{item.customer_name}</TableCell><TableCell>{item.product_name}<p className="text-xs text-muted-foreground">{item.supplier_name||`供应商 #${item.supplier_id}`}</p></TableCell><TableCell>{item.service_ips||item.service_domain||"—"}</TableCell><TableCell>{new Date(item.created_at).toLocaleString("zh-CN")}</TableCell><TableCell>{money(item.total_cents)}</TableCell><TableCell>{item.payment_method||"—"}<Status value={item.status}/></TableCell><TableCell>{item.upstream_renewal_status?<Status value={item.upstream_renewal_status}/>:<span className="text-xs text-muted-foreground">未排队</span>}{item.cost_cents==null?"未记录":money(item.cost_cents)}</TableCell></TableRow>)}{!resource.data.items.length&&<TableRow><TableCell colSpan={8} className="h-32 text-center text-muted-foreground">暂无供应商服务的本地续费账单</TableCell></TableRow>}</TableBody></Table></div><AdminPagination total={resource.data.total} page={nav.page} pageSize={limit} onPageChange={nav.setPage} onPageSizeChange={value=>{nav.setExtra({...nav.extra,limit:String(value)});nav.setPage(0);}}/></>}
 </div></Workspace>;
}
