"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { useCallback, useEffect, useRef, useState } from "react";
import { ArrowLeft, ArrowRight, RefreshCw } from "lucide-react";
import { SiteBrand, SiteFooter, useSite } from "./site-provider";
import { api, fetchCaptcha, type CaptchaChallenge } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

function Shell({ children, title, description }: { children: React.ReactNode; title: string; description: string }) {
  const { site } = useSite();
  return <main className="min-h-screen grid place-items-center px-6 py-8 bg-white"><div className="auth-form"><Link href={site?.maintenance_enabled ? "/admin-login" : "/login"} className="flex items-center gap-2 text-muted-foreground mb-10"><ArrowLeft size={16} />返回登录</Link><div className="mb-6"><SiteBrand /></div><h1 className="text-2xl font-semibold mb-3">{title}</h1><p className="text-muted-foreground mb-8">{description}</p>{children}<div className="mt-8"><SiteFooter /></div></div></main>;
}

export function ForgotPasswordForm() {
  const { site, refresh: refreshSite } = useSite();
  const [identifier, setIdentifier] = useState("");
  const [channel, setChannel] = useState<"email" | "sms">("email");
  const [challengeId, setChallengeId] = useState("");
  const [code, setCode] = useState("");
  const [channels, setChannels] = useState<string[]>([]);
  const [complete, setComplete] = useState(false);
  const [resendAt, setResendAt] = useState(0);
  const [clock, setClock] = useState(0);
  useEffect(() => { const timer = window.setInterval(() => setClock(Date.now()), 1000); return () => window.clearInterval(timer); }, []);
  useEffect(() => { let active = true; api<{channels: string[]}>("/auth/verification/channels").then(result => { if (active) { setChannels(result.channels); if (!result.channels.includes("email") && result.channels.includes("sms")) setChannel("sms"); } }).catch(e => { if (active) setError((e as Error).message); }); return () => { active = false; }; }, []);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [captcha, setCaptcha] = useState<CaptchaChallenge | null>(null);
  const [captchaAnswer, setCaptchaAnswer] = useState("");
  const [captchaBusy, setCaptchaBusy] = useState(false);
  const captchaRequest = useRef<AbortController | null>(null);
  const refreshCaptcha = useCallback(async (preserveError = false) => {
    captchaRequest.current?.abort(); const controller = new AbortController(); captchaRequest.current = controller; setCaptcha(null); setCaptchaAnswer(""); setCaptchaBusy(true);
    try { const next = await fetchCaptcha("forgot-password", controller.signal); if (!controller.signal.aborted) { setCaptcha(next.enabled && next.captcha_id && next.image ? next : null); setCaptchaAnswer(""); if (!preserveError) setError(""); if (!next.enabled) refreshSite(); } }
    catch (e) { if (!controller.signal.aborted) setError(current => preserveError && current ? current : (e as Error).message); }
    finally { if (!controller.signal.aborted) setCaptchaBusy(false); }
  }, [refreshSite]);
  useEffect(() => { const timer = site?.auth_captcha_enabled ? window.setTimeout(() => void refreshCaptcha(), 0) : undefined; return () => { if (timer) window.clearTimeout(timer); captchaRequest.current?.abort(); }; }, [site?.auth_captcha_enabled, refreshCaptcha]);
  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setError("");
    setMessage("");
    try {
      if (challengeId) {
        const form = new FormData(event.currentTarget);
        const password = String(form.get("new_password"));
        if (password !== form.get("confirm_password")) { setError("两次输入的密码不一致"); return; }
        await api("/auth/password-reset/confirm", { method: "POST", body: JSON.stringify({ challenge_id: challengeId, code, password }) });
        setComplete(true);
        setMessage("密码已重置，请使用新密码登录。");
        return;
      }
      const result = await api<{challenge_id: string; expires_in: number; resend_after: number}>("/auth/password-reset/request", { method: "POST", body: JSON.stringify({ identifier, channel, captcha_id: captcha?.captcha_id, captcha_answer: captchaAnswer }) });
      setChallengeId(result.challenge_id);
      setResendAt(Date.now() + result.resend_after * 1000);
      setMessage("验证码已加入发送队列，请输入 6 位验证码并设置新密码。");
      if (site?.auth_captcha_enabled) void refreshCaptcha(true);
    } catch (e) {
      setError((e as Error).message);
      if (site?.auth_captcha_enabled) void refreshCaptcha(true);
    } finally {
      setBusy(false);
    }
  }
  return <Shell title="找回密码" description="接收验证码后设置新密码。"><form className="space-y-5" onSubmit={submit}><div className="space-y-2"><Label htmlFor="identifier">账号或邮箱</Label><Input id="identifier" disabled={!!challengeId} value={identifier} onChange={event => setIdentifier(event.target.value)} autoComplete="username" maxLength={254} required /></div><div className="flex gap-4 text-sm"><label><input type="radio" disabled={!channels.includes("email") || !!challengeId} checked={channel === "email"} onChange={() => setChannel("email")} /> 邮箱</label><label><input type="radio" disabled={!channels.includes("sms") || !!challengeId} checked={channel === "sms"} onChange={() => setChannel("sms")} /> 短信</label></div>{site?.auth_captcha_enabled && !challengeId && <div className="space-y-2"><Label htmlFor="captcha_answer">图形验证码</Label><div className="flex items-center gap-3"><div className="h-16 w-[190px] overflow-hidden border bg-slate-50"><img src={captcha?.image} alt="图形验证码" className="h-16 w-[190px]" /></div><Button type="button" variant="outline" size="icon" title="刷新验证码" aria-label="刷新验证码" onClick={() => void refreshCaptcha()} disabled={captchaBusy}><RefreshCw className={captchaBusy ? "animate-spin" : ""} /></Button></div><Input id="captcha_answer" value={captchaAnswer} onChange={event => setCaptchaAnswer(event.target.value.toUpperCase().replace(/[^2-9A-HJ-NP-Z]/g, "").slice(0, 6))} autoComplete="off" maxLength={6} required /></div>}{challengeId && <><Input aria-label="验证码" inputMode="numeric" pattern="[0-9]{6}" autoComplete="one-time-code" maxLength={6} value={code} onChange={e => setCode(e.target.value.replace(/\D/g, "").slice(0, 6))} required /><Input aria-label="新密码" type="password" minLength={8} maxLength={72} autoComplete="new-password" name="new_password" required /><Input aria-label="确认新密码" type="password" autoComplete="new-password" minLength={8} maxLength={72} name="confirm_password" required /></>}{challengeId && !complete && <Button type="button" variant="outline" disabled={busy || clock < resendAt} onClick={() => { setChallengeId(""); setCode(""); setMessage(""); if (site?.auth_captcha_enabled) void refreshCaptcha(); }}>{clock < resendAt ? `${Math.ceil((resendAt - clock) / 1000)} 秒后可重新发送` : "重新获取验证码"}</Button>}{error && <p role="alert" className="text-destructive text-sm">{error}</p>}{message && <p role="status" className="text-sm text-emerald-700">{message}</p>}<Button className="w-full" disabled={complete || busy || (!challengeId && (!channels.includes(channel) || (!!site?.auth_captcha_enabled && (!captcha?.captcha_id || captchaBusy))))}>{complete ? "密码已重置" : busy ? "提交中" : challengeId ? "重置密码" : "发送验证码"}<ArrowRight size={16} /></Button></form>{complete && <Link className="block mt-4 underline" href="/login">返回登录</Link>}</Shell>;
}

export function ResetPasswordForm() {
  const params = useSearchParams();
  const challengeId = params.get("challenge_id") ?? "";
  const [code, setCode] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (password !== confirm) {
      setError("两次输入的密码不一致");
      return;
    }
    setBusy(true);
    setError("");
    setMessage("");
    try {
      await api("/auth/password-reset/confirm", { method: "POST", body: JSON.stringify({ challenge_id: challengeId, code, password }) });
      setMessage("密码已重置，请使用新密码登录。");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return <Shell title="设置新密码" description="输入验证码并设置新密码。"><form className="space-y-5" onSubmit={submit}><Input aria-label="验证码" inputMode="numeric" maxLength={6} value={code} onChange={e => setCode(e.target.value.replace(/\D/g, "").slice(0, 6))} required /><div className="space-y-2"><Label htmlFor="password">新密码</Label><Input id="password" type="password" value={password} onChange={event => setPassword(event.target.value)} autoComplete="new-password" minLength={8} maxLength={72} required /></div><div className="space-y-2"><Label htmlFor="confirm">确认密码</Label><Input id="confirm" type="password" value={confirm} onChange={event => setConfirm(event.target.value)} autoComplete="new-password" minLength={8} maxLength={72} required /></div>{!challengeId && <p role="alert" className="text-destructive text-sm">重置请求缺少 challenge_id。</p>}{error && <p role="alert" className="text-destructive text-sm">{error}</p>}{message ? <p role="status" className="text-sm text-emerald-700">{message} <Link className="underline ml-1" href="/login">返回登录</Link></p> : <Button className="w-full" disabled={busy || !challengeId}>{busy ? "保存中" : "重置密码"}<ArrowRight size={16} /></Button>}</form></Shell>;
}
