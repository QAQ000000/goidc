"use client";
import Link from "next/link";
import { useState } from "react";
import { api, money } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace } from "./workspace";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
type Package = {id:number;name:string;capacity_gb:number;price_cents:number;stock:number|null;version:number;fulfillment_mode:string};
export function ServiceTrafficView({id}:{id:number}) {
 const resource=useResource<Package[]>(`/services/${id}/traffic-products`,false);
 const [busy,setBusy]=useState(false),[error,setError]=useState("");
 async function buy(item:Package) { if(busy||!window.confirm(`确认购买 ${item.name}（${money(item.price_cents)}）吗？付款后等待管理员交付。`))return;setBusy(true);setError("");try{const result=await api<{invoice_id:number}>(`/services/${id}/traffic-orders`,{method:"POST",headers:{"Idempotency-Key":`traffic-buy-${crypto.randomUUID()}`},body:JSON.stringify({traffic_product_id:item.id,expected_version:item.version})});window.location.assign(`/console/invoices/${result.invoice_id}`);}catch(e){setError((e as Error).message);resource.refresh();}finally{setBusy(false);}}
 return <Workspace title="购买流量包"><Button variant="ghost" asChild><Link href={`/console/services/${id}`}>返回服务</Link></Button><h1 className="page-title mt-4">购买流量包</h1><p className="text-sm text-muted-foreground mt-2">支付成功后等待管理员在资源端交付；交付核验前不会显示已增加流量。</p>{error&&<p role="alert" className="text-destructive mt-4">{error}</p>}{resource.error?<Failure error={resource.error} retry={resource.refresh}/>:!resource.data?<Loading/>:<div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3 mt-6">{resource.data.map(item=><section key={item.id} className="data-surface p-5"><h2 className="font-medium">{item.name}</h2><p className="text-2xl mt-3">{item.capacity_gb} GB</p><p className="money mt-2">{money(item.price_cents)}</p><p className="text-xs text-muted-foreground mt-2">库存：{item.stock??"不限"} · 人工交付</p><Button className="mt-4" disabled={busy} onClick={()=>void buy(item)}>创建账单</Button></section>)}{resource.data.length===0&&<p className="py-12 text-muted-foreground">暂无适用于当前服务的流量包</p>}</div>}</Workspace>;
}
