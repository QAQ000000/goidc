"use client";
import { Suspense, useRef, useState } from "react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { RefreshCw, Save } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace, useAdminPermission } from "./workspace";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Input } from "./ui/input";

type AuthSettings = {
  login_enabled: boolean; registration_enabled: boolean; registration_email_enabled: boolean; registration_phone_enabled: boolean;
  login_username_enabled: boolean; login_email_enabled: boolean; login_phone_enabled: boolean;
  email_verification_required: boolean; phone_verification_required: boolean;
  verification_email_enabled: boolean; verification_sms_enabled: boolean;
  auth_captcha_enabled: boolean; captcha_auto_enabled: boolean;
  login_max_attempts: number; login_lockout_minutes: number;
  email_channel_ready: boolean; sms_channel_ready: boolean; version: number; updated_at: string;
};
type MFASettings = { customer_enabled: boolean; admin_enabled: boolean; version: number; updated_at: string };

function SwitchRow({ label, checked, onChange }: { label: string; checked: boolean; onChange: (value: boolean) => void }) {
  return <label className="flex min-w-0 items-center gap-3 text-sm"><Checkbox checked={checked} onCheckedChange={value => onChange(value === true)} /><span>{label}</span></label>;
}
function Tabs({ active }: { active: "auth" | "captcha" | "mfa" }) { return <nav className="flex flex-wrap gap-4 border-b pb-3 text-sm"><Link href="/admin/auth-settings" aria-current={active === "auth" ? "page" : undefined} className={active === "auth" ? "text-primary font-medium" : ""}>注册登录</Link><Link href="/admin/auth-settings?tab=captcha" aria-current={active === "captcha" ? "page" : undefined} className={active === "captcha" ? "text-primary font-medium" : ""}>验证码与安全</Link><Link href="/admin/security/mfa" aria-current={active === "mfa" ? "page" : undefined} className={active === "mfa" ? "text-primary font-medium" : ""}>二次验证</Link></nav>; }

function AuthForm({ initial, tab }: { initial: AuthSettings; tab: "auth" | "captcha" }) {
  const [form, setForm] = useState(initial), [base, setBase] = useState(initial);
  const [busy, setBusy] = useState(false), [message, setMessage] = useState("");
  const retry = useRef<{ body: string; key: string } | null>(null);
  const manage = useAdminPermission("settings.manage"), dirty = JSON.stringify(form) !== JSON.stringify(base);
  const set = (key: keyof AuthSettings, value: boolean | number) => { retry.current = null; setForm(previous => ({ ...previous, [key]: value })); };
  async function reload() { if (dirty && !window.confirm("丢弃未保存的更改？")) return; setBusy(true); try { const latest = await api<AuthSettings>("/admin/auth-settings"); setForm(latest); setBase(latest); retry.current = null; setMessage(""); } catch (error) { setMessage((error as Error).message); } finally { setBusy(false); } }
  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault(); if (!manage || busy || !dirty) return; setBusy(true); setMessage("");
    const body = JSON.stringify({ ...form, expected_version: base.version });
    if (retry.current?.body !== body) retry.current = { body, key: crypto.randomUUID() };
    try { const saved = await api<AuthSettings>("/admin/auth-settings", { method: "PUT", headers: { "Idempotency-Key": retry.current.key }, body }); setForm(saved); setBase(saved); retry.current = null; setMessage("设置已保存"); }
    catch (error) { setMessage(error instanceof ApiError && error.status === 409 ? "设置版本已变化，请重新加载。" : (error as Error).message); }
    finally { setBusy(false); }
  }
  return <form onSubmit={save} className="max-w-3xl space-y-6"><div className="flex flex-wrap items-center justify-between gap-3"><h1 className="page-title">注册登录与验证码</h1><Button type="button" variant="outline" size="icon" title="重新加载" aria-label="重新加载" disabled={busy} onClick={() => void reload()}><RefreshCw /></Button></div><Tabs active={tab} /><fieldset disabled={!manage || busy} className="space-y-7">
    {tab === "auth" && <section className="space-y-4 border-t pt-5"><h2 className="text-base font-semibold">注册与登录</h2><div className="grid gap-3 sm:grid-cols-2"><SwitchRow label="开放客户登录" checked={form.login_enabled} onChange={value => set("login_enabled", value)} /><SwitchRow label="开放客户注册" checked={form.registration_enabled} onChange={value => set("registration_enabled", value)} /><SwitchRow label="用户名登录" checked={form.login_username_enabled} onChange={value => set("login_username_enabled", value)} /><SwitchRow label="邮箱登录" checked={form.login_email_enabled} onChange={value => set("login_email_enabled", value)} /><SwitchRow label="手机登录" checked={form.login_phone_enabled} onChange={value => set("login_phone_enabled", value)} /></div></section>}
    {tab === "captcha" && <><section className="space-y-4 border-t pt-5"><h2 className="text-base font-semibold">验证码与通知渠道</h2><div className="grid gap-3 sm:grid-cols-2"><SwitchRow label="注册需要邮箱验证" checked={form.email_verification_required} onChange={value => set("email_verification_required", value)} /><SwitchRow label="邮件验证码渠道" checked={form.verification_email_enabled} onChange={value => set("verification_email_enabled", value)} /><SwitchRow label="短信验证码渠道" checked={form.verification_sms_enabled} onChange={value => set("verification_sms_enabled", value)} /><SwitchRow label="图形验证码" checked={form.auth_captcha_enabled} onChange={value => set("auth_captcha_enabled", value)} /></div><p className="text-xs text-muted-foreground">邮件渠道：{form.email_channel_ready ? "已启用" : "未就绪"}；短信渠道：{form.sms_channel_ready ? "已启用" : "未就绪"}</p></section>
    <section className="space-y-4 border-t pt-5"><h2 className="text-base font-semibold">登录安全策略</h2><div className="grid gap-4 sm:grid-cols-2"><label className="grid gap-2 text-sm">管理员连续失败次数<Input type="number" min={1} max={20} value={form.login_max_attempts} onChange={event => set("login_max_attempts", Number(event.target.value))} /></label><label className="grid gap-2 text-sm">封禁分钟数<Input type="number" min={1} max={1440} value={form.login_lockout_minutes} onChange={event => set("login_lockout_minutes", Number(event.target.value))} /></label></div></section></>}
    {message && <p role="status" className="text-sm">{message}</p>}<div className="flex flex-wrap items-center gap-3 border-t pt-4"><span className="text-xs text-muted-foreground">版本 {form.version}</span><Button disabled={!dirty || busy}><Save />保存更改</Button><Button type="button" variant="outline" disabled={!dirty || busy} onClick={() => { if (window.confirm("丢弃未保存的更改？")) { setForm(base); retry.current = null; setMessage(""); } }}>取消更改</Button></div>
  </fieldset></form>;
}
function AuthSettingsContent() { const resource = useResource<AuthSettings>("/admin/auth-settings", false); const params = useSearchParams(); const tab = params.get("tab") === "captcha" ? "captcha" : "auth"; return <Workspace title="注册登录与验证码" admin>{resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : resource.data ? <AuthForm key={resource.data.version} initial={resource.data} tab={tab} /> : <Loading />}</Workspace>; }
export function AuthSettingsPanel() { return <Suspense fallback={<Loading />}><AuthSettingsContent /></Suspense>; }

function MFAForm({ initial }: { initial: MFASettings }) {
  const [form, setForm] = useState(initial), [base, setBase] = useState(initial);
  const [busy, setBusy] = useState(false), [message, setMessage] = useState("");
  const retry = useRef<{ body: string; key: string } | null>(null);
  const manage = useAdminPermission("settings.manage"), dirty = form.customer_enabled !== base.customer_enabled || form.admin_enabled !== base.admin_enabled;
  async function reload() { if (dirty && !window.confirm("丢弃未保存的更改？")) return; setBusy(true); try { const latest = await api<MFASettings>("/admin/security/mfa"); setForm(latest); setBase(latest); retry.current = null; setMessage(""); } catch (error) { setMessage((error as Error).message); } finally { setBusy(false); } }
  async function save(event: React.FormEvent<HTMLFormElement>) { event.preventDefault(); if (!manage || busy || !dirty) return; setBusy(true); setMessage(""); const body = JSON.stringify({ customer_enabled: form.customer_enabled, admin_enabled: form.admin_enabled, expected_version: base.version }); if (retry.current?.body !== body) retry.current = { body, key: crypto.randomUUID() }; try { const saved = await api<MFASettings>("/admin/security/mfa", { method: "PUT", headers: { "Idempotency-Key": retry.current.key }, body }); setForm(saved); setBase(saved); retry.current = null; setMessage("MFA 策略已保存"); } catch (error) { setMessage(error instanceof ApiError && error.status === 409 ? "策略版本已变化，请重新加载。" : (error as Error).message); } finally { setBusy(false); } }
  return <form onSubmit={save} className="max-w-3xl space-y-6"><div className="flex flex-wrap items-center justify-between gap-3"><h1 className="page-title">二次验证</h1><Button type="button" variant="outline" size="icon" title="重新加载" aria-label="重新加载" disabled={busy} onClick={() => void reload()}><RefreshCw /></Button></div><Tabs active="mfa" /><fieldset disabled={!manage || busy} className="space-y-5"><section className="space-y-4 border-t pt-5"><SwitchRow label="允许客户启用二次验证" checked={form.customer_enabled} onChange={value => { retry.current = null; setForm({ ...form, customer_enabled: value }); }} /><SwitchRow label="允许管理员启用二次验证" checked={form.admin_enabled} onChange={value => { retry.current = null; setForm({ ...form, admin_enabled: value }); }} /><p className="text-xs text-muted-foreground">已启用的个人二次验证仍在登录时生效；个人密钥与恢复码由账户本人管理。</p></section>{message && <p role="status" className="text-sm">{message}</p>}<div className="flex flex-wrap items-center gap-3 border-t pt-4"><span className="text-xs text-muted-foreground">版本 {form.version}</span><Button disabled={!dirty || busy}><Save />保存更改</Button><Button type="button" variant="outline" disabled={!dirty || busy} onClick={() => { if (window.confirm("丢弃未保存的更改？")) { setForm(base); retry.current = null; setMessage(""); } }}>取消更改</Button></div></fieldset></form>;
}
export function MFASettingsPanel() { const resource = useResource<MFASettings>("/admin/security/mfa", false); return <Workspace title="二次验证" admin>{resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : resource.data ? <MFAForm key={resource.data.version} initial={resource.data} /> : <Loading />}</Workspace>; }
