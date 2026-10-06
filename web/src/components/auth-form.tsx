"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { ArrowRight, RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api, fetchCaptcha, type CaptchaChallenge, type User } from "@/lib/api";
import { SiteBrand, SiteFooter, useSite } from "./site-provider";
import { Failure, Loading } from "./common";
export function AuthForm({ register = false, adminLogin = false }: { register?: boolean; adminLogin?: boolean }) {
  const router = useRouter();
  const { site, error: siteError, refresh } = useSite();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [mfaChallenge, setMfaChallenge] = useState("");
  const [mfaCode, setMfaCode] = useState("");
  const [captcha, setCaptcha] = useState<CaptchaChallenge | null>(null);
  const [captchaAnswer, setCaptchaAnswer] = useState("");
  const [captchaBusy, setCaptchaBusy] = useState(false);
  const captchaRequest = useRef<AbortController | null>(null);
  const captchaAction = register ? "register" : "login" as const;
  const refreshCaptcha = useCallback(async (preserveError = false) => {
    captchaRequest.current?.abort();
    const controller = new AbortController();
    captchaRequest.current = controller;
    setCaptcha(null);
    setCaptchaAnswer("");
    setCaptchaBusy(true);
    try {
      const next = await fetchCaptcha(captchaAction, controller.signal);
      if (!controller.signal.aborted) { setCaptcha(next.enabled && next.captcha_id && next.image ? next : null); setCaptchaAnswer(""); if (!preserveError) setError(""); if (!next.enabled) refresh(); }
    } catch (e) {
      if (!controller.signal.aborted && !preserveError) setError((e as Error).message);
    } finally {
      if (!controller.signal.aborted) setCaptchaBusy(false);
    }
  }, [captchaAction, refresh]);
  useEffect(() => {
    const timer = site?.auth_captcha_enabled && !mfaChallenge ? window.setTimeout(() => void refreshCaptcha(), 0) : undefined;
    return () => { if (timer) window.clearTimeout(timer); captchaRequest.current?.abort(); };
  }, [site?.auth_captcha_enabled, mfaChallenge, refreshCaptcha]);
  async function submit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const form = new FormData(e.currentTarget);
    try {
      const username = String(form.get("username"));
      const password = String(form.get("password"));
      if (register) {
        await api("/auth/register", {
          method: "POST",
          body: JSON.stringify({
            username,
            password,
            email: form.get("email"),
            display_name: form.get("display_name"),
            captcha_id: captcha?.captcha_id,
            captcha_answer: captchaAnswer,
          }),
        });
        router.push("/login");
        return;
      }
      const user = mfaChallenge
        ? await api<User>("/auth/login/mfa", { method: "POST", body: JSON.stringify({ challenge: mfaChallenge, code: mfaCode }) })
        : await api<User>("/auth/login", { method: "POST", body: JSON.stringify({ username, password, captcha_id: captcha?.captcha_id, captcha_answer: captchaAnswer }) });
      if (adminLogin && user.role !== "admin" && user.role !== "super_admin") {
        await api("/auth/logout", { method: "POST" });
        setError("需要管理员账户");
        return;
      }
      router.push(user.role === "customer" ? "/" : "/admin");
      router.refresh();
    } catch (e) {
      const failure = e as Error & { code?: string; details?: Record<string, unknown> };
      if (failure.code === "mfa_required" && typeof failure.details?.challenge === "string") {
        setMfaChallenge(failure.details.challenge);
        setError("");
      } else {
        setError(failure.message);
        if (site?.auth_captcha_enabled) void refreshCaptcha(true);
      }
    } finally {
      setBusy(false);
    }
  }
  return (
    <main className="min-h-screen grid place-items-center px-6 py-8 bg-white">
      <div className="auth-form">
        <Link
          href="/"
          className="flex items-center gap-3 text-2xl font-semibold mb-14"
        >
          <SiteBrand />
        </Link>
        <h1 className="text-2xl font-semibold mb-3">
          {register ? "创建账户" : adminLogin ? "管理员登录" : "登录账户"}
        </h1>
        <p className="text-muted-foreground mb-8">
          {register ? "云服务客户中心" : adminLogin ? "管理控制台" : "欢迎回来"}
        </p>
        {register && siteError ? <Failure error={siteError} retry={refresh} /> : register && !site ? <Loading /> : register && !site?.registration_enabled ? <p role="status" className="py-6 text-muted-foreground">本站暂未开放注册</p> : <form className="space-y-5" onSubmit={submit}>
          {site?.auth_captcha_enabled && !mfaChallenge && (
            <div className="space-y-2">
              <Label htmlFor="captcha_answer">图形验证码</Label>
              <div className="flex items-center gap-3"><div className="h-16 w-[190px] overflow-hidden border bg-slate-50"><img src={captcha?.image} alt="图形验证码" className="h-16 w-[190px]" /></div><Button type="button" variant="outline" size="icon" title="刷新验证码" aria-label="刷新验证码" onClick={() => void refreshCaptcha()} disabled={captchaBusy}><RefreshCw className={captchaBusy ? "animate-spin" : ""} /></Button></div>
              <Input id="captcha_answer" name="captcha_answer" value={captchaAnswer} onChange={event => setCaptchaAnswer(event.target.value.toUpperCase().replace(/[^2-9A-HJ-NP-Z]/g, "").slice(0, 6))} autoComplete="off" maxLength={6} required />
            </div>
          )}
          <div className="space-y-2">
            <Label htmlFor="username">{register ? "账号" : "账号或邮箱"}</Label>
            <Input
              id="username"
              name="username"
              autoComplete="username"
              required
              maxLength={register ? 48 : 254}
              minLength={register ? 3 : 1}
            />
          </div>
          {mfaChallenge && !register && (
            <div className="space-y-2">
              <Label htmlFor="mfa_code">身份验证器验证码</Label>
              <Input id="mfa_code" name="mfa_code" inputMode="text" pattern="[0-9A-Fa-f]{6,10}" maxLength={10} autoComplete="one-time-code" value={mfaCode} onChange={(event) => setMfaCode(event.target.value.replace(/[^0-9A-Fa-f]/g, "").slice(0, 10).toUpperCase())} required autoFocus />
              <p className="text-xs text-muted-foreground">输入身份验证器的 6 位验证码，也可以输入一次性恢复码。</p>
            </div>
          )}
          {register && (
            <>
              <div className="space-y-2">
                <Label htmlFor="display_name">姓名</Label>
                <Input
                  id="display_name"
                  name="display_name"
                  required
                  maxLength={40}
                  autoComplete="name"
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="email">邮箱</Label>
                <Input
                  id="email"
                  name="email"
                  type="email"
                  required
                  autoComplete="email"
                />
              </div>
            </>
          )}
          <div className="space-y-2">
            <Label htmlFor="password">密码</Label>
            <Input
              id="password"
              name="password"
              type="password"
              minLength={8}
              maxLength={72}
              autoComplete={register ? "new-password" : "current-password"}
              required
            />
          </div>
          {error && (
            <p role="alert" className="text-destructive text-sm">
              {error}
            </p>
          )}
          <Button className="w-full" disabled={busy || (!!site?.auth_captcha_enabled && !mfaChallenge && (!captcha?.captcha_id || captchaBusy))}>
            {busy ? "提交中" : mfaChallenge ? "验证并登录" : register ? "创建账户" : "登录"}
            <ArrowRight size={16} />
          </Button>
        </form>}
        {!register && <div className="mt-4 text-sm"><Link className="text-primary underline underline-offset-4" href="/forgot-password">忘记密码？</Link></div>}
        {!adminLogin && (register || (site?.registration_enabled && !siteError)) && <div className="mt-6 text-sm text-muted-foreground">
          {register ? "已有账户？" : "还没有账户？"}
          <Link
            className="ml-2 text-primary underline underline-offset-4"
            href={register ? "/login" : "/register"}
          >
            {register ? "登录" : "注册"}
          </Link>
        </div>}
        <div className="mt-8"><SiteFooter /></div>
      </div>
    </main>
  );
}
