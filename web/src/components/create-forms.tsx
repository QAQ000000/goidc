"use client";
import { useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { ArrowLeft, Send } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Label } from "@/components/ui/label";
import { Select,SelectContent,SelectItem,SelectTrigger,SelectValue } from "@/components/ui/select";
import { api,ApiError,type TicketDepartment } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace } from "./workspace";

export function CreateTicketView(){
 const router=useRouter();const [priority,setPriority]=useState("normal");const [department,setDepartment]=useState("none");const [busy,setBusy]=useState(false);const [error,setError]=useState("");const [uncertain,setUncertain]=useState(false);const {data:departments, error:departmentError}=useResource<TicketDepartment[]>("/ticket-departments");
 async function submit(e:React.FormEvent<HTMLFormElement>){e.preventDefault();setBusy(true);setError("");const form=new FormData(e.currentTarget);try{const ticket=await api<{id:number}>("/tickets",{method:"POST",body:JSON.stringify({subject:form.get("subject"),body:form.get("body"),priority,department_id:department === "none" ? undefined : Number(department)})});router.push(`/console/tickets/${ticket.id}`);}catch(e){setError((e as Error).message);if(!(e instanceof ApiError))setUncertain(true);}finally{setBusy(false)}}
 return <Workspace title="提交工单"><Button variant="ghost" className="mb-5" asChild><Link href="/console/tickets"><ArrowLeft size={16}/>我的工单</Link></Button><h1 className="page-title mb-8">提交工单</h1><form className="max-w-2xl space-y-6" onSubmit={submit}><Field label="主题" id="subject"><Input id="subject" name="subject" required maxLength={80}/></Field><div className="grid sm:grid-cols-2 gap-5"><Field label="优先级" id="priority"><Select value={priority} onValueChange={setPriority}><SelectTrigger id="priority"><SelectValue/></SelectTrigger><SelectContent><SelectItem value="low">低</SelectItem><SelectItem value="normal">普通</SelectItem><SelectItem value="high">高</SelectItem><SelectItem value="urgent">紧急</SelectItem></SelectContent></Select></Field><Field label="处理部门" id="department"><Select value={department} onValueChange={setDepartment} disabled={!departments}><SelectTrigger id="department"><SelectValue placeholder={departmentError?"部门加载失败":"选择部门（可选）"}/></SelectTrigger><SelectContent><SelectItem value="none">未指定部门</SelectItem>{departments?.map(item=><SelectItem key={item.id} value={String(item.id)}>{item.name}</SelectItem>)}</SelectContent></Select></Field></div>{departmentError&&<p role="alert" className="text-sm text-destructive">部门列表加载失败，仍可提交未指定部门的工单。</p>}<Field label="问题描述" id="body"><Textarea id="body" name="body" required rows={8} maxLength={3000}/></Field>{error&&<p role="alert" className="text-destructive">{error}</p>}{uncertain&&<p role="alert" className="text-destructive">提交状态未知，请先查看工单列表。</p>}<Button disabled={busy||uncertain}><Send size={16}/>{busy?"提交中":"提交工单"}</Button></form></Workspace>;
}
function Field({label,id,children}:{label:string;id:string;children:React.ReactNode}){return <div className="space-y-2"><Label htmlFor={id}>{label}</Label>{children}</div>}
