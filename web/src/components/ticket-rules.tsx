"use client";
import { useState } from "react";
import { api } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace } from "./workspace";
import { Button } from "./ui/button";
type Page<T>={items:T[];total:number;limit:number;offset:number};
export function TicketStatusesView(){const r=useResource<Page<{id:number;title:string}>>("/admin/ticket-statuses",false);const [title,setTitle]=useState("");return <Workspace title="工单状态" admin><h1 className="page-title">工单状态</h1><div className="flex gap-2 my-4"><input aria-label="状态标题" value={title} onChange={e=>setTitle(e.target.value)}/><Button disabled={!title} onClick={async()=>{await api("/admin/ticket-statuses",{method:"POST",headers:{"Idempotency-Key":crypto.randomUUID()},body:JSON.stringify({slug:title.toLowerCase().replace(/\s+/g,"-"),title,sort_order:99})});setTitle("");r.refresh();}}>添加</Button></div>{r.error?<p role="alert">加载失败</p>:!r.data?<p>加载中</p>:r.data.items.length===0?<p>暂无工单状态</p>:<ul>{r.data.items.map(x=><li key={x.id}>{x.title}</li>)}</ul>}</Workspace>}
export function TicketRulesView(){const r=useResource<Page<{id:number;blocked_keywords:string[];auto_reply:string}>>("/admin/ticket-rules",false);return <Workspace title="工单传递" admin><h1 className="page-title">工单传递</h1>{r.error?<p role="alert">加载失败</p>:!r.data?<p>加载中</p>:r.data.items.length===0?<p>暂无传递规则</p>:<ul>{r.data.items.map(x=><li key={x.id}>{(x.blocked_keywords||[]).join("、")} {x.auto_reply}</li>)}</ul>}</Workspace>}
