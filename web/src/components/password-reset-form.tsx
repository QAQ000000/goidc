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
      await api("/auth/password-reset/request", { method: "POST", body: JSON.stringify({ identifier, captcha_id: captcha?.captcha_id, captcha_answer: captchaAnswer }) });
      setMessage("如果账号存在，重置链接会发送到绑定邮箱，请检查收件箱。");
      if (site?.auth_captcha_enabled) void refreshCaptcha(true);
    } catch (e) {
      setError((e as Error).message);
      if (site?.auth_captcha_enabled) void refreshCaptcha(true);
    } finally {
      setBusy(false);
    }
  }
  return <Shell title="找回密码" description="输入账号或邮箱，我们会发送一次性重置链接。"><form className="space-y-5" onSubmit={submit}><div className="space-y-2"><Label htmlFor="identifier">账号或邮箱</Label><Input id="identifier" value={identifier} onChange={event => setIdentifier(event.target.value)} autoComplete="username" maxLength={254} required /></div>{site?.auth_captcha_enabled && <div className="space-y-2"><Label htmlFor="captcha_answer">图形验证码</Label><div className="flex items-center gap-3"><div className="h-16 w-[190px] overflow-hidden border bg-slate-50"><img src={captcha?.image} alt="图形验证码" className="h-16 w-[190px]" /></div><Button type="button" variant="outline" size="icon" title="刷新验证码" aria-label="刷新验证码" onClick={() => void refreshCaptcha()} disabled={captchaBusy}><RefreshCw className={captchaBusy ? "animate-spin" : ""} /></Button></div><Input id="captcha_answer" value={captchaAnswer} onChange={event => setCaptchaAnswer(event.target.value.toUpperCase().replace(/[^2-9A-HJ-NP-Z]/g, "").slice(0, 6))} autoComplete="off" maxLength={6} required /></div>}{error && <p role="alert" className="text-destructive text-sm">{error}</p>}{message && <p role="status" className="text-sm text-emerald-700">{message}</p>}<Button className="w-full" disabled={busy || (!!site?.auth_captcha_enabled && (!captcha?.captcha_id || captchaBusy))}>{busy ? "提交中" : "发送重置链接"}<ArrowRight size={16} /></Button></form></Shell>;
}

export function ResetPasswordForm() {
  const params = useSearchParams();
  const token = params.get("token") ?? "";
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
      await api("/auth/password-reset/confirm", { method: "POST", body: JSON.stringify({ token, password }) });
      setMessage("密码已重置，请使用新密码登录。");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return <Shell title="设置新密码" description="重置链接只可使用一次，并在 30 分钟后失效。"><form className="space-y-5" onSubmit={submit}><div className="space-y-2"><Label htmlFor="password">新密码</Label><Input id="password" type="password" value={password} onChange={event => setPassword(event.target.value)} autoComplete="new-password" minLength={8} maxLength={72} required /></div><div className="space-y-2"><Label htmlFor="confirm">确认密码</Label><Input id="confirm" type="password" value={confirm} onChange={event => setConfirm(event.target.value)} autoComplete="new-password" minLength={8} maxLength={72} required /></div>{!token && <p role="alert" className="text-destructive text-sm">重置链接缺少令牌。</p>}{error && <p role="alert" className="text-destructive text-sm">{error}</p>}{message ? <p role="status" className="text-sm text-emerald-700">{message} <Link className="underline ml-1" href="/login">返回登录</Link></p> : <Button className="w-full" disabled={busy || !token}>{busy ? "保存中" : "重置密码"}<ArrowRight size={16} /></Button>}</form></Shell>;
}
