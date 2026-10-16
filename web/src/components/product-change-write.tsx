"use client";
import { createContext, useContext, useEffect, useRef, useState } from "react";
import { api, ApiError, type Service, type User } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Button } from "./ui/button";
type Request={path:string;method:"POST";body:string;key:string};
type State={pending:Request|null;error:string;busy:boolean;retry:()=>Promise<boolean>;blocked:boolean;submit:(quote:number)=>Promise<boolean>};
const Context=createContext<State>({pending:null,error:"",busy:false,retry:async()=>false,blocked:true,submit:async()=>false});
export const useProductChangeWrite=()=>useContext(Context);
export function ProductChangeWriteBoundary({service,admin,refresh,children}:{service:Service;admin:boolean;refresh:()=>void;children:React.ReactNode}){
 const me=useResource<User>(admin?null:"/me",false);
 if(admin)return <>{children}</>;
 if(me.error)return <div role="alert" className="text-sm space-y-2"><p>账户核对失败：{me.error.message}</p><Button variant="outline" onClick={me.refresh}>重新核对账户</Button></div>;
 if(!me.data)return <p role="status" className="text-sm">正在核对服务所属账户…</p>;
 if(me.data.id!==service.user_id||me.data.role!=="customer")return <div role="alert" className="text-sm space-y-2"><p>当前账户不是该服务的客户所有者，禁止服务变更。</p><Button variant="outline" onClick={me.refresh}>重新核对账户</Button></div>;
 return <Owner key={`${me.data.id}:${service.id}`} actor={me.data.id} service={service} refresh={refresh}>{children}</Owner>;
}
function Owner({actor,service,refresh,children}:{actor:number;service:Service;refresh:()=>void;children:React.ReactNode}){
 const storage=`zjmfgo:product-change-submit:${actor}:${service.id}`,path=`/services/${service.id}/configuration-changes`;
 const [pending,setPending]=useState<Request|null>(null),[ready,setReady]=useState(false),[busy,setBusy]=useState(false),[error,setError]=useState("");
 const running=useRef(false);
 useEffect(()=>{try{const raw=sessionStorage.getItem(storage);let value:Request|null=null;if(raw){value=JSON.parse(raw) as Request;if(value.path!==path||value.method!=="POST"||!value.body||!value.key)throw new Error("未确认请求无法识别，请先核对，新的服务变更已暂停");}queueMicrotask(()=>{setPending(value);setReady(true);});}catch(e){queueMicrotask(()=>setError((e as Error).message));}},[storage,path]);
 async function run(request:Request){if(!ready||running.current)return false;running.current=true;setBusy(true);setError("");try{sessionStorage.setItem(storage,JSON.stringify(request));setPending(request);await api(request.path,{method:request.method,body:request.body,headers:{"Idempotency-Key":request.key}});sessionStorage.removeItem(storage);setPending(null);refresh();return true;}catch(e){setError((e as Error).message);if(e instanceof ApiError&&e.status>=400&&e.status<500&&![401,403,408,429].includes(e.status)){sessionStorage.removeItem(storage);setPending(null);}return false;}finally{running.current=false;setBusy(false);}}
 const value:State={pending,error,busy,retry:async()=>pending?run(pending):false,blocked:!ready||busy||!!pending,submit:async quote=>{if(!ready||pending||running.current)return false;return run({path,method:"POST",body:JSON.stringify({quote_id:quote}),key:`web-product-change-${quote}-${crypto.randomUUID()}`});}};
 return <Context.Provider value={value}>{error&&<p role="alert" className="text-sm text-destructive">{error}</p>}{pending&&<div role="status" className="border p-3 text-sm space-y-2"><p>上次商品升级账单提交结果未确认，新的商品、配置和账期操作已暂停。刷新不自动提交，即使已有待付账单仍可核对原回执。</p><Button variant="outline" disabled={busy||!ready} onClick={()=>void run(pending)}>使用原请求核对升级账单</Button></div>}{children}</Context.Provider>;
}

export function ProductChangeWriteNotice({onRecovered}:{onRecovered:()=>void}){
 const write=useProductChangeWrite();
 return <>{write.error&&<p role="alert" className="text-sm text-destructive">{write.error}</p>}{write.pending&&<div role="status" className="border p-3 space-y-2 text-sm"><p>升级账单提交结果未确认，新的操作已暂停。请使用原请求核对回执。</p><Button type="button" variant="outline" disabled={write.busy} onClick={()=>void write.retry().then(success=>{if(success)onRecovered();})}>使用原请求核对升级账单</Button></div>}</>;
}
