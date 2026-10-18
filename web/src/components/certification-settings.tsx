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
import { channelConfigurationErrors, channelSchemaCompatible, type ChannelDescriptor, type ChannelInstance } from "@/lib/channel-configuration";

type Provider = ChannelDescriptor;
type Instance = ChannelInstance;
type Settings = {
  enabled: boolean;
  require_for_purchase: boolean;
  default_provider_id: string;
  default_instance_id?: string;
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

function instanceIssue(instance: Instance | undefined, providers: Provider[]): string {
  if (!instance) return "当前认证实例不存在，请明确选择其他实例。";
  if (!instance.enabled) return "当前认证实例已停用。";
  const provider = providers.find(item => item.id === instance.implementation_id);
  if (!provider || instance.configuration_status === "implementation_missing") return "当前构建缺少此认证实现。";
  if (instance.category !== "certification") return "当前实例不属于实名认证渠道。";
  if (!instance.factory_available) return "当前认证实现缺少运行工厂。";
  if (provider.id !== "manual" && !provider.capabilities?.includes("submit")) return "当前认证实现不支持提交申请。";
  if (!channelSchemaCompatible(provider, instance)) return "当前认证实例配置版本不兼容。";
  if (instance.configuration_status !== "valid" || channelConfigurationErrors(provider, instance.config, true, {}, [], instance).length) return "当前认证实例配置或凭据不完整，请先在接口管理中修正。";
  return "";
}

function selectedSettings(data: Settings): Settings {
  return { ...data, default_instance_id: data.default_instance_id ?? (data.default_provider_id === "manual" ? "manual-default" : "") };
}

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
  const [form, setForm] = useState(() => selectedSettings(data));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const dirty = JSON.stringify(form) !== JSON.stringify(selectedSettings(data));
  const selected = data.instances.find(instance => instance.id === form.default_instance_id);
  const selectionError = instanceIssue(selected, data.providers) || (selected?.implementation_id !== form.default_provider_id ? "认证实现与所选实例不一致，请明确重新选择实例。" : "");
  function patch(name: keyof Settings, value: boolean | string) { setForm(current => ({ ...current, [name]: value })); setError(""); }
  async function save(event: React.FormEvent) {
    event.preventDefault();
    if (!manage || busy || !dirty) return;
    if (selectionError) { setError(selectionError); return; }
    setBusy(true); setError("");
    try {
      await api("/admin/certification-settings", { method: "PUT", headers: { "Idempotency-Key": mutationKey() }, body: JSON.stringify({
        enabled: form.enabled, require_for_purchase: form.require_for_purchase, default_provider_id: form.default_provider_id, default_instance_id: form.default_instance_id,
        advanced_enabled: form.advanced_enabled, auto_update_name: form.auto_update_name, require_phone_match: form.require_phone_match,
        notify_on_approval: form.notify_on_approval, suspend_unverified: form.suspend_unverified, allow_file_upload: form.allow_file_upload,
        expected_version: data.version,
      }) });
      refresh();
    } catch (e) {
      setError(e instanceof ApiError && e.status === 409 && e.code === "version_conflict" ? "设置已被其他管理员更新，请重新加载。" : (e as Error).message);
    } finally { setBusy(false); }
  }
  return <form onSubmit={save} className="max-w-3xl space-y-6">
    <fieldset disabled={!manage || busy} className="min-w-0 space-y-5">
      <div className="border-y py-5 space-y-4">
        <label className="flex items-center gap-3 text-sm"><Checkbox checked={form.enabled} onCheckedChange={value => patch("enabled", value === true)} />启用实名认证</label>
        <label className="flex items-center gap-3 text-sm"><Checkbox checked={form.advanced_enabled} disabled />启用高级设置<span className="text-xs text-amber-700">（尚未实现）</span></label>
        <label className="flex items-center gap-3 text-sm"><Checkbox checked={form.auto_update_name} onCheckedChange={value => patch("auto_update_name", value === true)} />认证通过后自动更新客户姓名</label>
        <label className="flex items-center gap-3 text-sm"><Checkbox checked={form.require_phone_match} onCheckedChange={value => patch("require_phone_match", value === true)} />认证手机号必须与绑定手机号一致</label>
        <label className="flex items-center gap-3 text-sm"><Checkbox checked={form.notify_on_approval} disabled />人工审核通过后自动发送短信<span className="text-xs text-amber-700">（尚未生效）</span></label>
        <label className="flex items-center gap-3 text-sm"><Checkbox checked={form.require_for_purchase} onCheckedChange={value => patch("require_for_purchase", value === true)} />未认证禁止购买<span className="text-xs text-muted-foreground">（下单时校验）</span></label>
        <label className="flex items-center gap-3 text-sm"><Checkbox checked={form.suspend_unverified} disabled />未认证暂停已开通产品<span className="text-xs text-amber-700">（尚未生效）</span></label>
        <label className="flex items-center gap-3 text-sm"><Checkbox checked={form.allow_file_upload} disabled />允许上传认证文件<span className="text-xs text-amber-700">（尚未生效）</span></label>
      </div>
      <label className="grid min-w-0 max-w-md gap-2 text-sm">默认认证实例<select aria-label="默认认证实例" className="h-9 w-full min-w-0 rounded-md border bg-background px-3" value={form.default_instance_id} onChange={event => {
        const instance = data.instances.find(item => item.id === event.target.value);
        if (!instance || instanceIssue(instance, data.providers)) return;
        setForm(current => ({ ...current, default_instance_id: instance.id, default_provider_id: instance.implementation_id })); setError("");
      }}><option value="">请选择实例</option>{form.default_instance_id && !selected && <option value={form.default_instance_id}>当前实例不可用（{form.default_instance_id}）</option>}{data.instances.map(instance => {
        const issue = instanceIssue(instance, data.providers);
        return <option key={instance.id} value={instance.id} disabled={!!issue}>{instance.display_name} · {instance.id} · {instance.implementation_id}{issue ? `（${issue}）` : ""}</option>;
      })}</select></label>
      <p className="text-xs text-muted-foreground">新申请绑定所选实例及其配置；更改默认实例不会替换已提交申请的认证渠道。</p>
      {selectionError && <p role="alert" className="text-sm text-amber-700">{selectionError}</p>}
    </fieldset>
    <div className="flex flex-wrap items-center justify-between gap-3 border-t pt-5"><div className="text-xs text-muted-foreground">版本 {data.version}{data.updated_at ? ` · 最后更新 ${date(data.updated_at)}` : ""}{error && <span role="alert" className="ml-3 text-destructive">{error}</span>}</div><div className="flex gap-2"><Button type="button" variant="outline" disabled={!dirty || busy} onClick={() => { setForm(selectedSettings(data)); setError(""); }}>取消更改</Button>{manage && <Button type="submit" disabled={!dirty || busy || !!selectionError}><Save size={16} />{busy ? "保存中…" : "保存更改"}</Button>}</div></div>
  </form>;
}

function InterfaceSettings({ data }: { data: Settings }) {
  const canManageChannels = useAdminPermission("@super");
  return <section className="space-y-5">
    <div className="rounded-md border bg-muted/25 p-4 text-sm text-muted-foreground">认证接口通过具体实例配置管理，密钥只显示是否已配置。新增或修改实例后，重新加载本页并明确选择默认实例；历史申请继续使用提交时绑定的渠道。</div>
    <div className="overflow-x-auto border-y"><table className="w-full text-sm"><thead><tr className="text-left text-muted-foreground"><th className="px-3 py-3">接口名称</th><th>实例 / 实现</th><th>状态</th><th>密钥</th><th>版本</th></tr></thead><tbody>{data.instances.map(instance => {
      const provider = data.providers.find(item => item.id === instance.implementation_id);
      const issue = instanceIssue(instance, data.providers);
      const needsSecret = provider ? (provider.fields ?? []).some(field => field.secret) : true;
      return <tr key={instance.id} className="border-t"><td className="px-3 py-3 font-medium">{instance.display_name}</td><td><p>{instance.id}</p><p className="text-xs text-muted-foreground">{instance.implementation_id}</p></td><td className="max-w-sm py-3"><Badge variant="outline" className={!issue ? "text-emerald-700 bg-emerald-50 border-emerald-200" : "text-amber-700"}>{issue ? "不可用于新申请" : "可用于新申请"}</Badge>{issue && <p className="mt-1 text-xs text-muted-foreground">{issue}</p>}</td><td>{!needsSecret ? "无需密钥" : instance.secret_configured ? "已配置" : "未配置"}</td><td>{instance.version}</td></tr>;
    })}{!data.instances.length && <tr><td colSpan={5} className="py-12 text-center text-muted-foreground">暂无认证接口实例</td></tr>}</tbody></table></div>
    {canManageChannels && <Button asChild variant="outline"><Link href="/admin/channels?category=certification"><ExternalLink size={16} />管理认证接口</Link></Button>}
  </section>;
}
