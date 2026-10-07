"use client";
import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Send, Check } from "lucide-react";
export function AccountVerification({email,phone,phoneVerified,emailVerified,onVerified}:{email:string;phone?:string;phoneVerified?:boolean;emailVerified?:boolean;onVerified:()=>void}) {
  const [channel,setChannel]=useState<"email"|"sms">("sms");
  const [recipient,setRecipient]=useState("");
  const [challenge,setChallenge]=useState("");
  const [code,setCode]=useState("");
  const [busy,setBusy]=useState(false);
  const [error,setError]=useState("");
  const [message,setMessage]=useState("");
  const [resendAt,setResendAt]=useState(0);
  const [clock,setClock]=useState(0);
  useEffect(()=>{const timer=window.setInterval(()=>setClock(Date.now()),1000);return()=>window.clearInterval(timer);},[]);
  async function submit(event:React.FormEvent) {
    event.preventDefault(); if(busy)return; setBusy(true);setError("");setMessage("");
    try { if(challenge){await api("/account/verification/confirm",{method:"POST",body:JSON.stringify({challenge_id:challenge,code})});setChallenge("");setCode("");setRecipient("");setMessage("联系方式已验证");onVerified();}
      else {const result=await api<{challenge_id:string;resend_after:number}>("/account/verification/request",{method:"POST",body:JSON.stringify({purpose:channel === "sms" ? "bind_phone":"bind_email",channel,recipient:channel === "email" ? email : recipient})});setChallenge(result.challenge_id);setResendAt(Date.now()+result.resend_after*1000);setMessage("验证码已加入发送队列");}
    }catch(e){setError((e as Error).message);}finally{setBusy(false);}
  }
  return <section className="border-t py-6 mb-6"><h2 className="text-base font-semibold mb-4">联系方式验证</h2><p className="text-sm text-muted-foreground mb-4">手机：{phone || "未绑定"}{phoneVerified ? "（已验证）":""} · 邮箱：{emailVerified ? "已验证":"未验证"}</p><form onSubmit={submit} className="space-y-3 max-w-xl"><div className="flex gap-4 text-sm"><label><input type="radio" checked={channel === "sms"} disabled={!!challenge || busy} onChange={()=>{setChannel("sms");}} /> 手机</label><label><input type="radio" checked={channel === "email"} disabled={!!challenge || busy} onChange={()=>{setChannel("email");}} /> 邮箱</label></div><Input aria-label={channel === "sms" ? "绑定手机号":"当前账户邮箱"} type={channel === "sms" ? "tel":"email"} pattern={channel === "sms" ? "\\+[1-9][0-9]{6,14}":undefined} placeholder={channel === "sms" ? "+8613800000000":""} value={channel === "email" ? email : recipient} readOnly={channel === "email"} onChange={e=>setRecipient(e.target.value)} disabled={!!challenge || busy} required />{challenge && <Input aria-label="联系方式验证码" value={code} onChange={e=>setCode(e.target.value.replace(/\D/g,"").slice(0,6))} pattern="[0-9]{6}" inputMode="numeric" maxLength={6} autoComplete="one-time-code" required />}<div className="flex gap-3"><Button disabled={busy}>{challenge ? <Check />:<Send />}{challenge ? "确认绑定":"发送验证码"}</Button>{challenge && <Button type="button" variant="outline" disabled={busy || clock < resendAt} onClick={()=>{setChallenge("");setCode("");setMessage("");}}>{clock < resendAt ? `${Math.ceil((resendAt-clock)/1000)} 秒后重发`:"重新获取验证码"}</Button>}</div>{error && <p role="alert" className="text-destructive text-sm">{error}</p>}{message && <p role="status" className="text-emerald-700 text-sm">{message}</p>}</form></section>;
}
