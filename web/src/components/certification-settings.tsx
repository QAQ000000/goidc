"use client";

import { useState } from "react";
import { ExternalLink, RefreshCw, Save } from "lucide-react";
import Link from "next/link";
import { api, ApiError, date } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace, useAdminPermission } from "./workspace";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Badge } from "./ui/badge";

type Provider = { id: string; name: string; capabilities?: string[] };
type Instance = { id: string; implementation_id: string; display_name: string; enabled: boolean; secret_configured: boolean; version: number };
type Settings = {
  enabled: boolean;
  require_for_purchase: boolean;
  default_provider_id: string;
  advanced_enabled: boolean;
  auto_update_name: boolean;
  require_phone_match: boolean;
  notify_on_approval: boolean;
  suspend_unverified: boolean;
  allow_file_upload: boolean;
  version: number;
  updated_at?: string;
  providers: Provider[];
  instances: Instance[];
};

function mutationKey() {
  return typeof crypto !== "undefined" && "randomUUID" in crypto ? crypto.randomUUID() : `certification-settings-${Date.now()}`;
}

export function CertificationSettingsView() {
  const resource = useResource<Settings>("/admin/certification-settings", false);
  const [tab, setTab] = useState<"basic" | "interface">("basic");
  return <Workspace title="实名认证设置" admin>
    <div className="space-y-5">
      <div className="flex items-center justify-between gap-3"><div><h1 className="page-title">实名认证设置</h1><p className="mt-2 text-sm text-muted-foreground">管理个人与企业实名认证策略，以及实际使用的认证接口。</p></div><Button type="button" variant="outline" size="icon" title="重新加载" aria-label="重新加载" onClick={resource.refresh}><RefreshCw size={16} /></Button></div>
      {resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <>
        <div className="flex flex-wrap gap-1 border-b" role="tablist" aria-label="实名认证设置分类">
          <button type="button" role="tab" aria-selected={tab === "basic"} className={`border-b-2 px-4 py-2 text-sm ${tab === "basic" ? "border-primary text-primary font-medium" : "border-transparent text-muted-foreground"}`} onClick={() => setTab("basic")}>基础设置</button>
          <button type="button" role="tab" aria-selected={tab === "interface"} className={`border-b-2 px-4 py-2 text-sm ${tab === "interface" ? "border-primary text-primary font-medium" : "border-transparent text-muted-foreground"}`} onClick={() => setTab("interface")}>接口设置</button>
        </div>
        {tab === "basic" ? <BasicSettings key={resource.data.version} data={resource.data} refresh={resource.refresh} /> : <InterfaceSettings data={resource.data} />}
      </>}
    </div>
  </Workspace>;
}

function BasicSettings({ data, refresh }: { data: Settings; refresh: () => void }) {
  const manage = useAdminPermission("certifications.manage");
  const [form, setForm] = useState(data);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const dirty = JSON.stringify(form) !== JSON.stringify(data);
  function patch(name: keyof Settings, value: boolean | string) { setForm(current => ({ ...current, [name]: value })); setError(""); }
  async function save(event: React.FormEvent) {
    event.preventDefault();
    if (!manage || busy || !dirty) return;
    setBusy(true); setError("");
    try {
      await api("/admin/certification-settings", { method: "PUT", headers: { "Idempotency-Key": mutationKey() }, body: JSON.stringify({
        enabled: form.enabled, require_for_purchase: form.require_for_purchase, default_provider_id: form.default_provider_id,
        advanced_enabled: form.advanced_enabled, auto_update_name: form.auto_update_name, require_phone_match: form.require_phone_match,
        notify_on_approval: form.notify_on_approval, suspend_unverified: form.suspend_unverified, allow_file_upload: form.allow_file_upload,
        expected_version: data.version,
      }) });
      refresh();
    } catch (e) {
      setError(e instanceof ApiError && e.status === 409 ? "设置已被其他管理员更新，请重新加载。" : (e as Error).message);
    } finally { setBusy(false); }
  }
  return <form onSubmit={save} className="max-w-3xl space-y-6">
    <fieldset disabled={!manage || busy} className="space-y-5">
      <div className="border-y py-5 space-y-4">
        <label className="flex items-center gap-3 text-sm"><Checkbox checked={form.enabled} onCheckedChange={value => patch("enabled", value === true)} />启用实名认证</label>
        <label className="flex items-center gap-3 text-sm"><Checkbox checked={form.advanced_enabled} onCheckedChange={value => patch("advanced_enabled", value === true)} />启用高级设置<span className="text-xs text-amber-700">（仅界面，尚未实现业务逻辑）</span></label>
        <label className="flex items-center gap-3 text-sm"><Checkbox checked={form.auto_update_name} onCheckedChange={value => patch("auto_update_name", value === true)} />认证通过后自动更新客户姓名</label>
        <label className="flex items-center gap-3 text-sm"><Checkbox checked={form.require_phone_match} onCheckedChange={value => patch("require_phone_match", value === true)} />认证手机号必须与绑定手机号一致</label>
        <label className="flex items-center gap-3 text-sm"><Checkbox checked={form.notify_on_approval} onCheckedChange={value => patch("notify_on_approval", value === true)} />人工审核通过后自动发送短信<span className="text-xs text-amber-700">（尚未生效）</span></label>
        <label className="flex items-center gap-3 text-sm"><Checkbox checked={form.require_for_purchase} onCheckedChange={value => patch("require_for_purchase", value === true)} />未认证禁止购买<span className="text-xs text-muted-foreground">（下单时校验）</span></label>
        <label className="flex items-center gap-3 text-sm"><Checkbox checked={form.suspend_unverified} onCheckedChange={value => patch("suspend_unverified", value === true)} />未认证暂停已开通产品<span className="text-xs text-amber-700">（尚未生效）</span></label>
        <label className="flex items-center gap-3 text-sm"><Checkbox checked={form.allow_file_upload} onCheckedChange={value => patch("allow_file_upload", value === true)} />允许上传认证文件<span className="text-xs text-amber-700">（尚未生效）</span></label>
      </div>
      <label className="grid max-w-md gap-2 text-sm">默认认证接口<select className="h-9 rounded-md border bg-background px-3" value={form.default_provider_id} onChange={event => patch("default_provider_id", event.target.value)}><option value="">请选择接口</option>{data.providers.map(provider => <option key={provider.id} value={provider.id}>{provider.name}</option>)}</select></label>
    </fieldset>
    <div className="flex flex-wrap items-center justify-between gap-3 border-t pt-5"><div className="text-xs text-muted-foreground">版本 {data.version}{data.updated_at ? ` · 最后更新 ${date(data.updated_at)}` : ""}{error && <span className="ml-3 text-destructive">{error}</span>}</div><div className="flex gap-2"><Button type="button" variant="outline" disabled={!dirty || busy} onClick={() => { setForm(data); setError(""); }}>取消更改</Button>{manage && <Button type="submit" disabled={!dirty || busy}><Save size={16} />{busy ? "保存中…" : "保存更改"}</Button>}</div></div>
  </form>;
}

function InterfaceSettings({ data }: { data: Settings }) {
  const canManageChannels = useAdminPermission("@super");
  return <section className="space-y-5">
    <div className="rounded-md border bg-muted/25 p-4 text-sm text-muted-foreground">认证接口由 Go 编译进二进制并通过实例配置管理，密钥只显示是否已配置。新增或修改接口实例后，返回本页重新选择默认接口。</div>
    <div className="overflow-x-auto border-y"><table className="w-full text-sm"><thead><tr className="text-left text-muted-foreground"><th className="px-3 py-3">接口名称</th><th>标识</th><th>状态</th><th>密钥</th><th>版本</th></tr></thead><tbody>{data.instances.map(instance => <tr key={instance.id} className="border-t"><td className="px-3 py-3 font-medium">{instance.display_name}</td><td>{instance.implementation_id}</td><td><Badge variant="outline" className={instance.enabled ? "text-emerald-700 bg-emerald-50 border-emerald-200" : "text-muted-foreground"}>{instance.enabled ? "启用" : "停用"}</Badge></td><td>{instance.secret_configured ? "已配置" : "未配置"}</td><td>{instance.version}</td></tr>)}{!data.instances.length && <tr><td colSpan={5} className="py-12 text-center text-muted-foreground">暂无认证接口实例</td></tr>}</tbody></table></div>
    {canManageChannels && <Button asChild variant="outline"><Link href="/admin/channels?category=certification"><ExternalLink size={16} />管理认证接口</Link></Button>}
  </section>;
}
