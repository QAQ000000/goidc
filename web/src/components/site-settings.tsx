"use client";
import { useState } from "react";
import { useRouter } from "next/navigation";
import { RefreshCw, Save } from "lucide-react";
import { api, date, type SiteSettings } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace, useAdminPermission } from "./workspace";
import { Loading, Failure } from "./common";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Checkbox } from "./ui/checkbox";
import { useSite } from "./site-provider";

export function SiteSettingsView() {
  const resource = useResource<SiteSettings>("/admin/site-settings", false);
  return <Workspace title="站点设置" admin><div className="flex items-center justify-between gap-3 mb-6"><h1 className="page-title">站点设置</h1><Button variant="outline" size="icon" title="重新加载设置" aria-label="重新加载设置" onClick={resource.refresh}><RefreshCw /></Button></div>{resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <SettingsForm key={resource.data.version} initial={resource.data} refresh={resource.refresh} />}</Workspace>;
}
function SettingsForm({ initial, refresh }: { initial: SiteSettings; refresh: () => void }) {
  const [form, setForm] = useState(initial);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const site = useSite();
  const router = useRouter();
  const manage = useAdminPermission("settings.manage");
  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault(); if (busy) return;
    setBusy(true); setError(""); setSaved(false);
    const { version, updated_at: _updated, ...profile } = form;
    void _updated;
    try {
      const updated = await api<SiteSettings>("/admin/site-settings", { method: "PUT", body: JSON.stringify({ ...profile, expected_version: version }) });
      setForm(updated); setSaved(true); site.refresh(); router.refresh();
    } catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  }
  function field(key: keyof Omit<SiteSettings, "registration_enabled" | "maintenance_enabled" | "auth_captcha_enabled" | "version" | "updated_at">, label: string, limit: number, type = "text") {
    return <label className="grid gap-2 text-sm min-w-0">{label}<Input aria-label={label} type={type} value={form[key]} maxLength={limit} required={key === "site_name"} onChange={event => setForm({ ...form, [key]: event.target.value })} /></label>;
  }
  return <form onSubmit={save} className="max-w-4xl"><fieldset disabled={busy || !manage} className="space-y-8 min-w-0">
    <section className="border-t pt-5"><h2 className="text-base font-semibold mb-5">品牌与网站</h2><div className="grid sm:grid-cols-2 gap-5">{field("site_name", "站点名称", 80)}{field("company_name", "公司名称", 160)}{field("logo_url", "Logo 地址", 2048)}{field("copyright", "版权信息", 160)}<label className="grid gap-2 text-sm sm:col-span-2">网站描述<Textarea aria-label="网站描述" value={form.description} maxLength={300} rows={3} onChange={event => setForm({ ...form, description: event.target.value })} /></label></div></section>
    <section className="border-t pt-5"><h2 className="text-base font-semibold mb-5">联系信息</h2><div className="grid sm:grid-cols-2 gap-5">{field("support_email", "联系邮箱", 254, "email")}{field("support_phone", "联系电话", 80)}{field("support_url", "支持链接", 2048)}{field("address", "公司地址", 300)}</div></section>
    <section className="border-t pt-5"><h2 className="text-base font-semibold mb-5">条款与备案</h2><div className="grid sm:grid-cols-2 gap-5">{field("terms_url", "服务条款链接", 2048)}{field("privacy_url", "隐私政策链接", 2048)}{field("record_number", "备案号", 120)}</div></section>
    <section className="border-t pt-5"><h2 className="text-base font-semibold mb-5">账号注册</h2><div className="grid gap-4"><label className="flex items-center gap-3 text-sm"><Checkbox checked={form.registration_enabled} onCheckedChange={checked => setForm({ ...form, registration_enabled: checked === true })} />开放邮箱注册</label><label className="flex items-center gap-3 text-sm"><Checkbox checked={form.auth_captcha_enabled} onCheckedChange={checked => setForm({ ...form, auth_captcha_enabled: checked === true })} />登录、注册和找回密码需要图形验证码</label></div></section>
    <section className="border-t pt-5"><h2 className="text-base font-semibold mb-5">维护模式</h2><div className="grid gap-5"><label className="flex items-center gap-3 text-sm"><Checkbox checked={form.maintenance_enabled} onCheckedChange={checked => setForm({ ...form, maintenance_enabled: checked === true })} />启用维护模式</label><label className="grid gap-2 text-sm">维护公告<Textarea aria-label="维护公告" value={form.maintenance_message} maxLength={1000} rows={4} onChange={event => setForm({ ...form, maintenance_message: event.target.value })} /></label>{field("maintenance_url", "维护跳转地址", 2048)}</div></section>
    <div className="border-t pt-5 space-y-3">{error && <div role="alert" className="text-destructive text-sm break-words">{error}<Button type="button" variant="link" onClick={refresh}>重新加载</Button></div>}{saved && <p role="status" className="text-sm text-emerald-700">设置已保存</p>}<div className="flex flex-wrap justify-between items-center gap-4"><span className="text-xs text-muted-foreground">版本 {form.version} · {date(form.updated_at)}</span><Button disabled={!manage}><Save />{busy ? "保存中" : "保存设置"}</Button></div></div>
  </fieldset></form>;
}
