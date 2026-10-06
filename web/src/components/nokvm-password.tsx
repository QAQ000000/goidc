"use client";

import { useRef, useState } from "react";
import { Dice5, Eye, EyeOff, KeyRound } from "lucide-react";
import { api, type Service } from "@/lib/api";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "./ui/dialog";

export function NOKVMPasswordReset({service,admin,disabled,refresh}:{service:Service;admin:boolean;disabled:boolean;refresh:()=>void}) {
  const [open,setOpen]=useState(false);
  const [password,setPassword]=useState(""),[confirmation,setConfirmation]=useState("");
  const [visible,setVisible]=useState(false),[busy,setBusy]=useState(false),[error,setError]=useState("");
  const attempt=useRef<{key:string;password:string}|null>(null);

  function close(){
    if(busy)return;
    setOpen(false);setPassword("");setConfirmation("");setVisible(false);setError("");
    attempt.current=null;
  }
  function generate(){
    const bytes=crypto.getRandomValues(new Uint8Array(18));
    const generated="Zg!"+Array.from(bytes,b=>b.toString(16).padStart(2,"0")).join("");
    setPassword(generated);setConfirmation(generated);setError("");
  }
  async function submit(event:React.FormEvent<HTMLFormElement>){
    event.preventDefault();
    if(busy||disabled)return;
    if(password!==confirmation){setError("两次密码不一致");return;}
    if(attempt.current?.password!==password)attempt.current={key:crypto.randomUUID(),password};
    setBusy(true);setError("");
    try{
      await api(`${admin?"/admin":""}/services/${service.id}/reset-password`,{method:"POST",headers:{"Idempotency-Key":attempt.current.key},body:JSON.stringify({password})});
      setPassword("");setConfirmation("");setVisible(false);attempt.current=null;setOpen(false);refresh();
    }catch(e){setError((e as Error).message);refresh();}finally{setBusy(false);}
  }
  return <>
    <Button variant="outline" disabled={disabled} onClick={()=>setOpen(true)}><KeyRound size={16}/>重置系统密码</Button>
    <Dialog open={open} onOpenChange={value=>{if(!value)close();}}><DialogContent showCloseButton={!busy}>
      <DialogHeader><DialogTitle>重置系统密码</DialogTitle><DialogDescription>重置可能中断当前连接。新密码确认后，原密码失效。</DialogDescription></DialogHeader>
      <p className="text-sm break-all">{service.name} · #{service.id}</p>
      <form onSubmit={submit} className="space-y-5">
        <fieldset disabled={busy} className="space-y-4 min-w-0">
          <label className="grid gap-2 text-sm">新系统密码
            <span className="flex gap-2 min-w-0"><Input className="min-w-0" aria-label="新系统密码" autoComplete="new-password" type={visible?"text":"password"} required minLength={12} maxLength={128} pattern="[!-~]{12,128}" title="12至128位非空格可见ASCII字符" value={password} onChange={e=>{setPassword(e.target.value);setError("");}}/>
              <Button type="button" size="icon" variant="outline" aria-label="生成随机系统密码" title="生成随机系统密码" onClick={generate}><Dice5 size={16}/></Button>
              <Button type="button" size="icon" variant="outline" aria-label={visible?"隐藏系统密码":"显示系统密码"} title={visible?"隐藏系统密码":"显示系统密码"} onClick={()=>setVisible(v=>!v)}>{visible?<EyeOff size={16}/>:<Eye size={16}/>}</Button>
            </span>
          </label>
          <label className="grid gap-2 text-sm">确认新系统密码<Input aria-label="确认新系统密码" autoComplete="new-password" type={visible?"text":"password"} required minLength={12} maxLength={128} pattern="[!-~]{12,128}" value={confirmation} onChange={e=>{setConfirmation(e.target.value);setError("");}}/></label>
        </fieldset>
        {error&&<p role="alert" className="text-sm text-destructive break-all">{error}</p>}
        <DialogFooter><Button type="button" variant="outline" disabled={busy} onClick={close}>取消</Button><Button type="submit" disabled={busy||disabled||!password||!confirmation}>{busy?"提交中":"确认重置"}</Button></DialogFooter>
      </form>
    </DialogContent></Dialog>
  </>;
}
