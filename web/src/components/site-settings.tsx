"use client";
import { useState } from "react";
import { Tabs } from "radix-ui";
import { useRouter } from "next/navigation";
import { RefreshCw, Save } from "lucide-react";
import { api, ApiError, date, type SiteSettings } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace, useAdminPermission } from "./workspace";
import { Loading, Failure } from "./common";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Checkbox } from "./ui/checkbox";
import { useSettingsDraft } from "./settings-draft";
import { useSite } from "./site-provider";

export function SiteSettingsView({ initialTab = "basic", title = "站点设置" }: { initialTab?: string; title?: string } = {}) {
  const resource = useResource<SiteSettings>("/admin/site-settings", false);
  return <Workspace title={title} admin><div className="flex items-center justify-between gap-3 mb-6"><h1 className="page-title">{title}</h1></div>{resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <SettingsForm key={resource.data.version} initial={resource.data} initialTab={initialTab} refresh={resource.refresh} />}</Workspace>;
}
function SettingsForm({ initial, initialTab }: { initial: SiteSettings; initialTab: string; refresh: () => void }) {
  const [form, setForm] = useState(initial);
  const [baseline, setBaseline] = useState(initial);
  const dirty = JSON.stringify(form) !== JSON.stringify(baseline);

  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const [tab, setTab] = useState(initialTab);
  const site = useSite();
  const router = useRouter();
  const manage = useAdminPermission("settings.manage");
  const drafts = useSettingsDraft(form, baseline, manage);
  async function reload() {
    if (busy || (dirty && !window.confirm("重新加载将丢弃未保存的更改，是否继续？"))) return;
    setBusy(true); setError("");
    try { const latest = await api<SiteSettings>("/admin/site-settings"); setForm(latest); setBaseline(latest); drafts.clear(); setSaved(false); }
    catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  }
  const preferences = form.admin_settings;
  function updatePreferences(patch: Partial<SiteSettings["admin_settings"]>) {
    setForm(current => ({ ...current, admin_settings: { ...current.admin_settings, ...patch } }));
  }
  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault(); if (busy || !manage || !dirty) return;
    setBusy(true); setError(""); setSaved(false);
    const { version, updated_at: _updated, ...profile } = form;
    void _updated;
    try {
      const updated = await api<SiteSettings>("/admin/site-settings", { method: "PUT", body: JSON.stringify({ ...profile, expected_version: version }) });
      setForm(updated); setBaseline(updated); drafts.clear(); setSaved(true); site.refresh(); router.refresh();
    } catch (e) { setError(e instanceof ApiError && e.status === 409 ? "设置已被其他管理员更新，当前更改尚未保存。请重新加载最新设置后修改。" : (e as Error).message); }
    finally { setBusy(false); }
  }
  function field(key: keyof Omit<SiteSettings, "admin_settings" | "registration_enabled" | "email_verification_required" | "maintenance_enabled" | "auth_captcha_enabled" | "version" | "updated_at">, label: string, limit: number, type = "text") {
    return <label className="grid gap-2 text-sm min-w-0">{label}<Input aria-label={label} type={type} value={form[key]} maxLength={limit} onChange={event => setForm({ ...form, [key]: event.target.value })} /></label>;
  }
  return <form onSubmit={save} className="max-w-4xl">{manage && drafts.draft && <div role="status" className="border rounded p-3 mb-3 text-sm">{drafts.draft.baseVersion === baseline.version ? "发现未保存的会话草稿。" : "发现旧版本草稿，恢复前需要确认，保存前请逐项检查。"}<Button type="button" variant="outline" disabled={busy} onClick={() => { const recovered = drafts.recover(); if (recovered) setForm(recovered); }}>恢复草稿</Button><Button type="button" variant="outline" disabled={busy} onClick={drafts.clear}>丢弃草稿</Button></div>}<div className="flex justify-end mb-3"><Button type="button" variant="outline" disabled={busy} onClick={reload}><RefreshCw />重新加载</Button></div><Tabs.Root value={tab} onValueChange={setTab}><Tabs.List aria-label="站点设置分区" className="flex overflow-x-auto border-b mb-6">{[{value:"basic",label:"基础信息"},{value:"localization",label:"本地化"},{value:"display",label:"显示设置"},{value:"security",label:"基础安全"},{value:"maintenance",label:"维护模式"},{value:"debug",label:"Debug 调试"}].map(item=><Tabs.Trigger key={item.value} value={item.value} className="shrink-0 border-b-2 border-transparent px-5 py-3 text-sm text-muted-foreground data-[state=active]:border-primary data-[state=active]:text-primary data-[state=active]:font-medium">{item.label}</Tabs.Trigger>)}</Tabs.List><fieldset disabled={busy || !manage} className="min-w-0"><Tabs.Content value="basic" forceMount hidden={tab!=="basic"} className="space-y-8">
    <section className="border-t pt-5"><h2 className="text-base font-semibold mb-5">品牌与网站</h2><div className="grid sm:grid-cols-2 gap-5">{field("site_name", "站点名称", 80)}{field("company_name", "公司名称", 160)}{field("logo_url", "Logo 地址", 2048)}{field("copyright", "版权信息", 160)}<label className="grid gap-2 text-sm sm:col-span-2">网站描述<Textarea aria-label="网站描述" value={form.description} maxLength={300} rows={3} onChange={event => setForm({ ...form, description: event.target.value })} /></label></div></section>
    <section className="border-t pt-5"><h2 className="text-base font-semibold mb-5">联系信息</h2><div className="grid sm:grid-cols-2 gap-5">{field("support_email", "联系邮箱", 254, "email")}{field("support_phone", "联系电话", 80)}{field("support_url", "支持链接", 2048)}{field("address", "公司地址", 300)}</div></section>
    <section className="border-t pt-5"><h2 className="text-base font-semibold mb-5">条款与备案</h2><div className="grid sm:grid-cols-2 gap-5">{field("terms_url", "服务条款链接", 2048)}{field("privacy_url", "隐私政策链接", 2048)}{field("record_number", "备案号", 120)}</div></section>
    </Tabs.Content><Tabs.Content value="localization" forceMount hidden={tab!=="localization"} className="space-y-8"><section className="border-t pt-5"><h2 className="text-base font-semibold mb-5">本地化</h2><div className="grid sm:grid-cols-2 gap-5"><label className="grid gap-2 text-sm">默认语言<select className="h-9 rounded-md border bg-background px-3" value={preferences.default_language} onChange={event => updatePreferences({ default_language: event.target.value as "zh_CN" | "en_US" })}><option value="zh_CN">简体中文</option><option value="en_US">English</option></select></label><label className="flex items-center gap-3 text-sm self-end"><Checkbox checked={preferences.language_menu_enabled} onCheckedChange={checked => updatePreferences({ language_menu_enabled: checked === true })} />启用前台语言选择菜单</label></div></section></Tabs.Content><Tabs.Content value="display" forceMount hidden={tab!=="display"} className="space-y-8"><section className="border-t pt-5"><h2 className="text-base font-semibold mb-5">显示设置</h2><div className="grid sm:grid-cols-2 gap-5"><label className="grid gap-2 text-sm">每页显示记录<select className="h-9 rounded-md border bg-background px-3" value={preferences.records_per_page} onChange={event => updatePreferences({ records_per_page: Number(event.target.value) as SiteSettings["admin_settings"]["records_per_page"] })}><option value={10}>10 条</option><option value={20}>20 条</option><option value={50}>50 条</option><option value={100}>100 条</option></select></label><label className="flex items-center gap-3 text-sm"><Checkbox checked={preferences.custom_customer_id_enabled} onCheckedChange={checked => updatePreferences({ custom_customer_id_enabled: checked === true })} />启用自定义客户 ID</label></div></section></Tabs.Content><Tabs.Content value="security" forceMount hidden={tab!=="security"}>
    <label className="flex items-center gap-3 text-sm mb-5"><Checkbox checked={form.email_verification_required} onCheckedChange={checked => setForm({...form, email_verification_required: checked === true})} />注册需要邮箱验证码</label>
    <section className="border-t pt-5"><h2 className="text-base font-semibold mb-5">账号注册与登录</h2><div className="grid gap-4"><label className="flex items-center gap-3 text-sm"><Checkbox checked={form.registration_enabled} onCheckedChange={checked => setForm({ ...form, registration_enabled: checked === true })} />开放邮箱注册</label><label className="flex items-center gap-3 text-sm"><Checkbox checked={form.auth_captcha_enabled} onCheckedChange={checked => setForm({ ...form, auth_captcha_enabled: checked === true })} />登录、注册和找回密码需要图形验证码</label><label className="flex items-center gap-3 text-sm"><Checkbox checked={preferences.frontend_ip_check} onCheckedChange={checked => updatePreferences({ frontend_ip_check: checked === true })} />启用前台登录 IP 检查</label><label className="flex items-center gap-3 text-sm"><Checkbox checked={preferences.admin_ip_check} onCheckedChange={checked => updatePreferences({ admin_ip_check: checked === true })} />启用后台登录 IP 检查</label><label className="grid gap-2 text-sm max-w-sm">管理员登录时长（天）<Input type="number" min={1} max={30} value={preferences.admin_login_days} onChange={event => updatePreferences({ admin_login_days: Number(event.target.value) })} /></label></div></section>
    </Tabs.Content><Tabs.Content value="maintenance" forceMount hidden={tab!=="maintenance"}>
    <section className="border-t pt-5"><h2 className="text-base font-semibold mb-5">维护模式</h2><div className="grid gap-5"><label className="flex items-center gap-3 text-sm"><Checkbox checked={form.maintenance_enabled} onCheckedChange={checked => setForm({ ...form, maintenance_enabled: checked === true })} />启用维护模式</label><label className="grid gap-2 text-sm">维护公告<Textarea aria-label="维护公告" value={form.maintenance_message} maxLength={1000} rows={4} onChange={event => setForm({ ...form, maintenance_message: event.target.value })} /></label>{field("maintenance_url", "维护跳转地址", 2048)}</div></section>
    </Tabs.Content><Tabs.Content value="debug" forceMount hidden={tab!=="debug"} className="space-y-8"><section className="border-t pt-5"><h2 className="text-base font-semibold mb-5">Debug 调试</h2><label className="flex items-center gap-3 text-sm"><Checkbox checked={preferences.debug_enabled} onCheckedChange={checked => updatePreferences({ debug_enabled: checked === true })} />启用调试日志</label><p className="text-xs text-muted-foreground mt-3">调试开关只影响可观测性输出，不会在客户接口中暴露敏感配置。</p></section></Tabs.Content>
    <div className="border-t mt-8 pt-5 space-y-3">{error && <div role="alert" className="text-destructive text-sm break-words">{error}<Button type="button" variant="link" onClick={reload}>重新加载</Button></div>}{saved && <p role="status" className="text-sm text-emerald-700">设置已保存</p>}<div className="flex flex-wrap justify-between items-center gap-4"><span className="text-xs text-muted-foreground">版本 {form.version} · {date(form.updated_at)}</span><div className="flex flex-wrap gap-3">{manage && <Button disabled={busy || !dirty}><Save />{busy ? "保存中" : "保存更改"}</Button>}<Button type="button" variant="outline" disabled={busy || !manage || !dirty} onClick={()=>{setForm(baseline);drafts.clear();setError("");setSaved(false);}}>取消更改</Button></div></div></div>
  </fieldset></Tabs.Root></form>;
}
