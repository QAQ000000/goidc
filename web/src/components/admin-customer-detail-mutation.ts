"use client";

import { useEffect,useRef,useState } from "react";
import { api,ApiError,type User } from "@/lib/api";

type Attempt={ path: string; method: string; body: string; key: string };
const isObject=(value: unknown): value is Record<string,unknown> => !!value&&typeof value==="object"&&!Array.isArray(value);

// Detail forms contain only ordinary profile/contact fields. Persist the exact
// intent before sending so switching tabs or refreshing cannot replace its key.
export function useCustomerDetailMutation(actorID: number|undefined,customerID: number,action: "profile"|"contacts",admin=true) {
  const scope=actorID? `goidc:detail-intent:${admin? "admin":"customer"}:${actorID}:${customerID}:${action}`:"";
  const [state,setState]=useState<{ scope: string; ready: boolean; attempt: Attempt|null; error: string; conflict: boolean; blocked: boolean }>({ scope: "",ready: false,attempt: null,error: "",conflict: false,blocked: false });
  const [busy,setBusy]=useState(false);
  const sending=useRef(false);
  const active=useRef(scope);
  const original=useRef<Attempt|null>(null);
  function validBody(body: unknown,method: string) {
    if(!isObject(body)) return false;
    if((action==="profile"||method!=="POST")&&(typeof body.expected_version!=="number"||!Number.isSafeInteger(body.expected_version)||body.expected_version<1)) return false;
    const allowed=action==="profile"? ["expected_version","display_name","email","status","profile"]:["expected_version","username","email","company_name","phone_number","status","invoice_emails","support_emails"];
    if(Object.keys(body).some(key => !allowed.includes(key))) return false;
    if(action==="profile"&&body.profile!==undefined&&(!isObject(body.profile)||Object.keys(body.profile).some(key => !["gender","company_name","phone","qq","country","province","address","postcode","source_channel","payment_method","language","admin_note","receive_marketing","custom_fields","custom_fields_version"].includes(key)))) return false;
    return true;
  }
  const root=admin? `/admin/customers/${customerID}`:"";
  const portal=admin? "admin":"customer";
  async function identity() {
    const user=await api<User>("/me",{ portal });
    const authorized=isObject(user)&&user.id===actorID&&(admin? user.role==="super_admin"||user.role==="admin"&&Array.isArray(user.permissions)&&user.permissions.includes("customers.manage"):user.role==="customer"&&user.id===customerID);
    if(!authorized) throw new Error("登录身份或客户管理权限已变化，请使用原账户核对；原请求保持不变。");
  }
  function validPath(path: string,method: string) {
    return action==="profile"? admin&&path===root&&method==="PATCH":(path===`${root}/contacts`&&method==="POST")||(new RegExp(`^${root}/contacts/[1-9][0-9]*$`).test(path)&&(method==="PATCH"||method==="DELETE"));
  }
  useEffect(() => {
    active.current=scope;
    original.current=null;
    if(!scope) return;
    const timer=window.setTimeout(() => {
      try {
        const raw=sessionStorage.getItem(scope);
        const attempt: unknown=raw? JSON.parse(raw):null;
        if(attempt&&(!isObject(attempt)||typeof attempt.path!=="string"||typeof attempt.method!=="string"||!validPath(attempt.path,attempt.method)||typeof attempt.key!=="string"||attempt.key.length<8||attempt.key.length>128||attempt.key.trim()!==attempt.key||/[\u0000-\u001f\u007f]/.test(attempt.key)||typeof attempt.body!=="string"||attempt.body.length>100000||!validBody(JSON.parse(attempt.body),attempt.method))) throw new Error("原请求恢复信息不可用，请联系管理员核对；当前操作已暂停。");
        if(active.current===scope) { original.current=attempt as Attempt|null; setState({ scope,ready: true,attempt: attempt as Attempt|null,error: attempt? "发现待核验的原请求，请使用原请求重试。":"",conflict: false,blocked: false }); }
      } catch(error) { if(active.current===scope) setState({ scope,ready: true,attempt: null,error: (error as Error).message,conflict: false,blocked: true }); }
    },0);
    return () => { window.clearTimeout(timer); if(active.current===scope) active.current=""; };
    // A scope owns the path and validator for the entire mounted form.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  },[scope]);
  const current=state.scope===scope;
  const uncertain=current&&!!state.attempt;
  const ready=current&&state.ready&&!state.blocked;
  async function run<T>(path: string,method: string,payload: unknown): Promise<T|null> {
    if(!scope||!ready||active.current!==scope||sending.current||state.conflict&&!original.current) return null;
    sending.current=true; setBusy(true);
    let attempt=original.current;
    let businessFailure=false;
    try {
      if(!attempt) {
        if(!validPath(path,method)||!validBody(payload,method)) throw new Error("请求目标或字段与当前客户动作不匹配");
        if(sessionStorage.getItem(scope)) throw new Error("发现未恢复的原请求，请刷新本区域后核对");
        const next={ path,method,body: JSON.stringify(payload),key: `detail-${crypto.randomUUID()}` };
        sessionStorage.setItem(scope,JSON.stringify(next));
        attempt=next; original.current=next;
      }
      if(active.current===scope) setState({ scope,ready: true,attempt,error: "",conflict: false,blocked: false });
      await identity();
      if(active.current!==scope) return null;
      let result: unknown;
      try { result=await api<unknown>(attempt.path,{ method: attempt.method,body: attempt.body,portal,headers: { "Idempotency-Key": attempt.key } }); }
      catch(error) { businessFailure=true; throw error; }
      const body=JSON.parse(attempt.body) as Record<string,unknown>;
      const positive=(value: unknown): value is number => typeof value==="number"&&Number.isSafeInteger(value)&&value>0;
      const valid=isObject(result)&&typeof result.replayed==="boolean"&&(action==="profile"? result.user_id===customerID&&result.profile_version===Number(body.expected_version)+1:result.customer_id===customerID&&positive(result.id)&&(attempt.method==="DELETE"? result.deleted===true&&String(result.id)===attempt.path.split("/").at(-1):result.version===(attempt.method==="POST"? 1:Number(body.expected_version)+1)&&(attempt.method!=="PATCH"||String(result.id)===attempt.path.split("/").at(-1))));
      if(!valid) throw new Error("响应缺少有效的操作确认，请使用原请求核验");
      await identity();
      if(active.current!==scope) return null;
      sessionStorage.removeItem(scope); original.current=null;
      if(active.current!==scope) return null;
      setState({ scope,ready: true,attempt: null,error: "",conflict: false,blocked: false });
      return result as T;
    } catch(error) {
      // Authorization errors and lost responses cannot disprove an earlier
      // execution. A definite validation/version refusal can release the intent.
      const definite=businessFailure&&error instanceof ApiError&&(error.status===400||error.status===422||error.status===409&&error.code!=="idempotency_conflict");
      if(definite) { try { sessionStorage.removeItem(scope); original.current=null; attempt=null; } catch { /* Retain the original intent when storage is unavailable. */ } }
      if(active.current===scope) setState({ scope,ready: true,attempt,error: attempt? `${(error as Error).message}。结果待核验，请使用原请求重试。`:(error as Error).message,conflict: definite&&error instanceof ApiError&&error.status===409,blocked: !attempt&&!definite });
      return null;
    } finally { sending.current=false; if(active.current===scope) setBusy(false); }
  }
  async function readLatest<T>(path: string): Promise<T|null> {
    if(!scope||!ready||active.current!==scope||sending.current) return null;
    if(path!==(action==="profile"? root:`${root}/contacts`)) return null;
    sending.current=true; setBusy(true);
    try {
      await identity();
      if(active.current!==scope) return null;
      const result=await api<T>(path,{ portal });
      await identity();
      return active.current===scope? result:null;
    } catch(error) {
      if(active.current===scope) setState(previous => ({ ...previous,error: (error as Error).message }));
      return null;
    } finally { sending.current=false; if(active.current===scope) setBusy(false); }
  }
  async function acknowledgeConflict() {
    if(!scope||!ready||active.current!==scope||sending.current||original.current) return false;
    if(!state.conflict) return true;
    sending.current=true; setBusy(true);
    try {
      await identity();
      if(active.current!==scope) return false;
      setState(previous => ({ ...previous,conflict: false,error: "" }));
      return true;
    } catch(error) {
      if(active.current===scope) setState(previous => ({ ...previous,error: (error as Error).message }));
      return false;
    } finally { sending.current=false; if(active.current===scope) setBusy(false); }
  }
  return { run,readLatest,ready,busy,acknowledgeConflict,uncertain,frozen: !ready||busy||uncertain,conflict: current&&state.conflict,error: current? state.error:"",setError: (error: string) => setState(previous => ({ ...previous,error })) };
}
