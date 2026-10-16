"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { ArrowRight, RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api, ApiError, fetchCaptcha, type CaptchaChallenge, type User } from "@/lib/api";
import { SiteBrand, SiteFooter, useSite } from "./site-provider";
import { Failure, Loading } from "./common";
import { CustomerFieldInputs, customerFieldValues, type CustomerFieldDefinition } from "./customer-field-inputs";
type RegistrationFields = { items: CustomerFieldDefinition[]; definitions_version: string };
export function AuthForm({ register = false }: { register?: boolean }) {
  const router = useRouter();
  const { site, error: siteError, refresh } = useSite();
  const [schema, setSchema] = useState<RegistrationFields | null>(null);
  const [schemaError, setSchemaError] = useState<Error | null>(null);
  const [schemaBusy, setSchemaBusy] = useState(false);
  const [schemaChanged, setSchemaChanged] = useState(false);
  const [schemaNotice, setSchemaNotice] = useState("");
  const [registrationDisabled, setRegistrationDisabled] = useState(false);
  const [unknownRegistration, setUnknownRegistration] = useState(false);
  const [customFields, setCustomFields] = useState<Record<string, unknown>>({});
  const submitting = useRef(false);
  const loadedSchema = useRef<RegistrationFields | null>(null);
  const schemaRequest = useRef<AbortController | null>(null);
  const loadRegistrationFields = useCallback(async () => {
    schemaRequest.current?.abort();
    const controller = new AbortController();
    schemaRequest.current = controller;
    setSchemaBusy(true); setSchemaError(null);
    try {
      const next = await api<RegistrationFields>("/auth/registration-fields", { signal: controller.signal });
      if (!next || !Array.isArray(next.items) || typeof next.definitions_version !== "string" || !next.definitions_version || next.items.some(field => !field || typeof field.field_key !== "string" || typeof field.label !== "string" || typeof field.description !== "string" || typeof field.required !== "boolean" || !["text", "textarea", "number", "date", "select", "boolean"].includes(field.field_type) || !Array.isArray(field.choices) || field.choices.some(choice => typeof choice !== "string")) || new Set(next.items.map(field => field.field_key)).size !== next.items.length) throw new Error("注册字段响应不完整，请重新加载");
      if (controller.signal.aborted) return;
      const previousSchema = loadedSchema.current;
      loadedSchema.current = next;
      setCustomFields(previous => {
        const kept: Record<string, unknown> = {};
        for (const field of next.items) {
          const value = previous[field.field_key];
          if (previousSchema?.items.find(old => old.field_key === field.field_key)?.field_type !== field.field_type) continue;
          if (value === undefined) continue;
          if (field.field_type === "select" && value != null && !field.choices.includes(String(value))) continue;
          if (field.field_type === "boolean" && value != null && typeof value !== "boolean") continue;
          if (field.field_type !== "boolean" && typeof value === "boolean") continue;
          kept[field.field_key] = value;
        }
        return kept;
      });
      setSchema(next); setSchemaChanged(false);
      setSchemaNotice("已加载最新注册字段。仅保留仍公开且类型、选项兼容的输入；已移除或不再适用的值已清除。请核对后手动提交。");
    } catch (failure) {
      if (!controller.signal.aborted) {
        setSchemaError(failure as Error);
        if (failure instanceof ApiError && failure.status === 403 && failure.code === "registration_disabled") setRegistrationDisabled(true);
      }
    } finally { if (!controller.signal.aborted) setSchemaBusy(false); }
  }, []);
  useEffect(() => {
    if (!register || !site?.registration_enabled) return;
    const timer = window.setTimeout(() => void loadRegistrationFields(), 0);
    return () => { window.clearTimeout(timer); schemaRequest.current?.abort(); };
  }, [register, site?.registration_enabled, loadRegistrationFields]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [email, setEmail] = useState("");
  const [verificationId, setVerificationId] = useState("");
  const [verificationCode, setVerificationCode] = useState("");
  const [verificationRecipient, setVerificationRecipient] = useState("");
  const [verificationBusy, setVerificationBusy] = useState(false);
  const [verificationMessage, setVerificationMessage] = useState("");
  const [resendAt, setResendAt] = useState(0);
  const [clock, setClock] = useState(0);
  useEffect(() => { const timer = window.setInterval(() => setClock(Date.now()), 1000); return () => window.clearInterval(timer); }, []);
  async function requestVerification() {
    if (verificationBusy || Date.now() < resendAt) return;
    setVerificationBusy(true); setError(""); setVerificationMessage("");
    try {
      const result = await api<{ challenge_id: string; resend_after: number }>("/auth/verification/request", { method: "POST", body: JSON.stringify({ purpose: "register", channel: "email", recipient: email, captcha_id: captcha?.captcha_id, captcha_answer: captchaAnswer }) });
      setVerificationId(result.challenge_id); setVerificationRecipient(email); setVerificationCode(""); setResendAt(Date.now() + result.resend_after * 1000); setVerificationMessage("验证码已加入发送队列");
    } catch (e) { setError((e as Error).message); }
    finally { setVerificationBusy(false); if (site?.auth_captcha_enabled) void refreshCaptcha(true); }
  }
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
      if (!controller.signal.aborted) setError(current => preserveError && current ? current : (e as Error).message);
    } finally {
      if (!controller.signal.aborted) setCaptchaBusy(false);
    }
  }, [captchaAction, refresh]);
  useEffect(() => {
    const timer = site?.auth_captcha_enabled && !mfaChallenge ? window.setTimeout(() => void refreshCaptcha(true), 0) : undefined;
    return () => { if (timer) window.clearTimeout(timer); captchaRequest.current?.abort(); };
  }, [site?.auth_captcha_enabled, mfaChallenge, refreshCaptcha]);
  async function submit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (submitting.current || (register && (!schema || schemaBusy || schemaError || schemaChanged || registrationDisabled || unknownRegistration))) return;
    let customValues: Record<string, unknown> = {};
    if (register) {
      try { customValues = customerFieldValues(schema!.items, customFields); }
      catch (failure) { setError((failure as Error).message); return; }
    }
    submitting.current = true;
    setBusy(true);
    setError("");
    const form = new FormData(e.currentTarget);
    try {
      const identifier = String(form.get("identifier") ?? "");
      const password = String(form.get("password"));
      if (register) {
        const result = await api<{ id: number }>("/auth/register", {
          method: "POST",
          body: JSON.stringify({
            password,
            custom_fields: customValues,
            custom_fields_version: schema!.definitions_version,
            email: form.get("email"),
            display_name: form.get("display_name"),
            captcha_id: captcha?.captcha_id,
            captcha_answer: captchaAnswer,
            ...(site?.email_verification_required ? { verification_id: verificationId, verification_code: verificationCode } : {}),
          }),
        });
        if (!result || !Number.isSafeInteger(result.id) || result.id <= 0) throw new Error("注册结果响应不完整");
        router.push("/login");
        return;
      }
      const user = mfaChallenge
        ? await api<User>("/auth/login/mfa", { method: "POST", body: JSON.stringify({ challenge: mfaChallenge, code: mfaCode }) })
        : await api<User>("/auth/login", { method: "POST", body: JSON.stringify({ identifier, password, captcha_id: captcha?.captcha_id, captcha_answer: captchaAnswer }) });
      router.push(user.role === "customer" ? "/" : "/admin");
      router.refresh();
    } catch (e) {
      const failure = e as Error & { code?: string; details?: Record<string, unknown> };
      if (register && failure.code === "registration_fields_changed") {
        setSchemaChanged(true); setError("注册字段已变更，输入保留在当前页面。请加载最新字段并核对后再提交。");
      } else if (register && failure.code === "registration_disabled") {
        setRegistrationDisabled(true); setError("本站暂未开放注册");
      } else if (register && (!(e instanceof ApiError) || e.status >= 500)) {
        setUnknownRegistration(true); setError("注册结果暂时未知，账户可能已创建。请先尝试登录或找回密码核实，避免重复创建。此页已停止再次提交。");
      } else if (failure.code === "mfa_required" && typeof failure.details?.challenge === "string") {
        setMfaChallenge(failure.details.challenge);
        setError("");
      } else {
        setError(failure.message);
        if (site?.auth_captcha_enabled && !mfaChallenge) void refreshCaptcha(true);
      }
    } finally {
      submitting.current = false; setBusy(false);
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
          {register ? "创建账户" : "登录账户"}
        </h1>
        <p className="text-muted-foreground mb-8">
          {register ? "云服务客户中心" : "欢迎回来"}
        </p>
        {register && siteError ? <Failure error={siteError} retry={refresh} /> : register && !site ? <Loading /> : register && (!site?.registration_enabled || registrationDisabled) ? <p role="status" className="py-6 text-muted-foreground">本站暂未开放注册</p> : <form className="space-y-5" onSubmit={submit}>
          {site?.auth_captcha_enabled && !mfaChallenge && (
            <div className="space-y-2">
              <Label htmlFor="captcha_answer">图形验证码</Label>
              <div className="flex items-center gap-3"><div className="h-16 w-[190px] overflow-hidden border bg-slate-50"><img src={captcha?.image} alt="图形验证码" className="h-16 w-[190px]" /></div><Button type="button" variant="outline" size="icon" title="刷新验证码" aria-label="刷新验证码" onClick={() => void refreshCaptcha()} disabled={captchaBusy}><RefreshCw className={captchaBusy ? "animate-spin" : ""} /></Button></div>
              <Input id="captcha_answer" name="captcha_answer" value={captchaAnswer} onChange={event => setCaptchaAnswer(event.target.value.toUpperCase().replace(/[^2-9A-HJ-NP-Z]/g, "").slice(0, 6))} autoComplete="off" maxLength={6} required />
            </div>
          )}
          {!register && <div className="space-y-2">
            <Label htmlFor="identifier">邮箱或手机号</Label>
            <Input
              id="identifier"
              name="identifier"
              autoComplete="username"
              required
              maxLength={254}
              autoCapitalize="none"
              spellCheck={false}
              placeholder="邮箱或已验证手机号"
            />
            <p className="text-xs text-muted-foreground">国内手机号可直接输入，国际号码请加国家区号（如 +1）。</p>
          </div>}
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
                  value={email}
                  onChange={event => { setEmail(event.target.value); setVerificationId(""); setVerificationMessage(""); }}
                />
              </div>
              {site?.email_verification_required && <div className="space-y-2"><Label htmlFor="verification_code">邮箱验证码</Label><div className="flex gap-2"><Input id="verification_code" value={verificationCode} onChange={event => setVerificationCode(event.target.value.replace(/\D/g, "").slice(0, 6))} inputMode="numeric" pattern="[0-9]{6}" maxLength={6} autoComplete="one-time-code" required /><Button type="button" variant="outline" disabled={verificationBusy || !email || clock < resendAt || (!!site.auth_captcha_enabled && (!captcha?.captcha_id || !captchaAnswer || captchaBusy))} onClick={() => void requestVerification()}>{verificationBusy ? "发送中" : clock < resendAt ? `${Math.ceil((resendAt - clock) / 1000)} 秒` : "发送验证码"}</Button></div>{verificationMessage && <p role="status" className="text-sm text-emerald-700">{verificationMessage}</p>}</div>}
            </>
          )}
          {register && <>
            {schemaChanged && <div role="alert" className="space-y-2 text-sm"><p>注册字段已更新，请加载最新字段并重新核对。</p><Button type="button" variant="outline" disabled={schemaBusy || busy} onClick={() => void loadRegistrationFields()}>加载最新注册字段</Button></div>}
            {schemaNotice && schema && <p role="status" className="text-xs text-muted-foreground break-words">{schemaNotice}</p>}
            <CustomerFieldInputs singleColumn definitions={{ fields: schema?.items ?? [], ready: !!schema && !schemaBusy, error: schemaError, refresh: () => void loadRegistrationFields() }} values={customFields} onChange={setCustomFields} disabled={busy || schemaBusy || schemaChanged || unknownRegistration} />
            {unknownRegistration && <p className="text-sm"><Link href="/login" className="underline">检查登录</Link> · <Link href="/forgot-password" className="underline">找回密码</Link></p>}
          </>}
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
          <Button className="w-full" disabled={busy || (register && (!schema || schemaBusy || !!schemaError || schemaChanged || unknownRegistration)) || (register && !!site?.email_verification_required && (!verificationId || email !== verificationRecipient || verificationCode.length !== 6)) || (!!site?.auth_captcha_enabled && !mfaChallenge && (!captcha?.captcha_id || captchaBusy))}>
            {busy ? "提交中" : mfaChallenge ? "验证并登录" : register ? "创建账户" : "登录"}
            <ArrowRight size={16} />
          </Button>
        </form>}
        {!register && <div className="mt-4 text-sm"><Link className="text-primary underline underline-offset-4" href="/forgot-password">忘记密码？</Link></div>}
        {(register || (site?.registration_enabled && !siteError)) && <div className="mt-6 text-sm text-muted-foreground">
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
