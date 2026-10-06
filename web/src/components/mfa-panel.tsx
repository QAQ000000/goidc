"use client";

import { useState } from "react";
import { Check, Copy, ShieldCheck, ShieldOff } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useResource } from "@/hooks/use-resource";

type Status = { enabled: boolean };
type Enrollment = { secret: string; otpauth_uri: string; recovery_codes: string[] };

export function MfaPanel() {
  const [status, setStatus] = useState<Status | null>(null);
  const statusResource = useResource<Status>("/account/mfa");
  const [enrollment, setEnrollment] = useState<Enrollment | null>(null);
  const [code, setCode] = useState("");
  const [password, setPassword] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);

  const currentStatus = status ?? statusResource.data;

  async function enroll() {
    setBusy(true); setMessage("");
    try { setEnrollment(await api<Enrollment>("/account/mfa/enroll", { method: "POST", body: "{}" })); }
    catch (error) { setMessage((error as Error).message); }
    finally { setBusy(false); }
  }
  async function confirm() {
    setBusy(true); setMessage("");
    try { setStatus(await api<Status>("/account/mfa/confirm", { method: "POST", body: JSON.stringify({ code }) })); setEnrollment(null); setCode(""); setMessage("MFA 已启用"); }
    catch (error) { setMessage((error as Error).message); }
    finally { setBusy(false); }
  }
  async function disable() {
    setBusy(true); setMessage("");
    try { setStatus(await api<Status>("/account/mfa/disable", { method: "POST", body: JSON.stringify({ code, password }) })); setCode(""); setPassword(""); setMessage("MFA 已关闭"); }
    catch (error) { setMessage((error as Error).message); }
    finally { setBusy(false); }
  }

  return <section className="data-surface p-6 mb-6">
    <div className="surface-head">
      <div><h2 className="font-semibold flex items-center gap-2"><ShieldCheck size={18} />两步验证</h2><p className="text-xs text-muted-foreground mt-1">使用身份验证器保护登录和账户设置。</p></div>
      {currentStatus && <span className="text-sm text-muted-foreground">{currentStatus.enabled ? "已启用" : "未启用"}</span>}
    </div>
    {!currentStatus ? <p className="text-sm text-muted-foreground mt-4">加载中...</p> : currentStatus.enabled ? <div className="mt-4 space-y-3">
      <p className="text-sm">登录时需要输入身份验证器生成的 6 位验证码。</p>
      <div className="grid md:grid-cols-3 gap-3"><Input value={password} onChange={(event) => setPassword(event.target.value)} type="password" placeholder="当前密码" aria-label="当前密码" autoComplete="current-password" /><Input value={code} onChange={(event) => setCode(event.target.value.replace(/[^0-9A-Fa-f]/g, "").slice(0, 10).toUpperCase())} inputMode="text" maxLength={10} placeholder="验证码或恢复码" aria-label="验证码或恢复码" /><Button variant="outline" disabled={busy || (code.length !== 6 && code.length !== 10) || password.length < 8} onClick={() => void disable()}><ShieldOff size={16} />关闭两步验证</Button></div>
    </div> : <div className="mt-4 space-y-4">
      {!enrollment ? <Button disabled={busy} onClick={() => void enroll()}><ShieldCheck size={16} />设置两步验证</Button> : <div className="space-y-3">
        <p className="text-sm">在身份验证器中添加以下密钥，然后输入应用生成的验证码完成设置。</p>
        <div className="flex flex-wrap items-center gap-2"><code className="rounded border bg-muted px-3 py-2 text-sm break-all">{enrollment.secret}</code><Button size="sm" variant="outline" onClick={() => void navigator.clipboard?.writeText(enrollment.secret)}><Copy size={14} />复制密钥</Button></div>
        <div className="rounded border bg-muted/40 p-3"><p className="text-sm font-medium">恢复码（仅显示一次）</p><p className="text-xs text-muted-foreground mt-1 mb-2">请保存到密码管理器。每个恢复码只能使用一次。</p><div className="grid grid-cols-2 md:grid-cols-4 gap-2">{enrollment.recovery_codes.map((item) => <code key={item} className="text-sm">{item}</code>)}</div></div>
        <div className="grid md:grid-cols-3 gap-3"><div className="md:col-span-2"><Label htmlFor="mfa-confirm-code">验证码</Label><Input id="mfa-confirm-code" value={code} onChange={(event) => setCode(event.target.value.replace(/\D/g, "").slice(0, 6))} inputMode="numeric" maxLength={6} autoComplete="one-time-code" placeholder="6 位验证码" /></div><Button className="self-end" disabled={busy || code.length !== 6} onClick={() => void confirm()}><Check size={16} />确认启用</Button></div>
      </div>}
    </div>}
    {(message || statusResource.error?.message) && <p role="alert" className="text-sm text-destructive mt-3">{message || statusResource.error?.message}</p>}
  </section>;
}
