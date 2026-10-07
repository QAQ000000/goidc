"use client";

import { useState } from "react";
import { RefreshCw, Save, Trash2 } from "lucide-react";
import { api, ApiError, date, type PaymentSettings } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace, useAdminPermission } from "./workspace";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Input } from "./ui/input";

const paymentTypes = [
  { value: "alipay", label: "支付宝" },
  { value: "wxpay", label: "微信支付" },
  { value: "qqpay", label: "QQ 钱包" },
  { value: "bank", label: "网银支付" },
];

export function PaymentSettingsView() {
  const resource = useResource<PaymentSettings>("/admin/payment-settings", false);
  return <Workspace title="支付渠道" admin>
    <h1 className="page-title mb-6">支付渠道</h1>
    {resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <PaymentSettingsForm key={resource.data.version} initial={resource.data} />}
  </Workspace>;
}

function PaymentSettingsForm({ initial }: { initial: PaymentSettings }) {
  const [baseline, setBaseline] = useState(initial);
  const [form, setForm] = useState(initial);
  const [key, setKey] = useState("");
  const [clearKey, setClearKey] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const settingsManage = useAdminPermission("settings.manage");
  const superAdmin = useAdminPermission("@super");
  const manage = settingsManage && superAdmin;
  const dirty = clearKey || key !== "" || form.enabled !== baseline.enabled || form.mock_enabled !== baseline.mock_enabled || form.api_url !== baseline.api_url || form.pid !== baseline.pid || form.types.join(",") !== baseline.types.join(",");

  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy || !manage) return;
    setBusy(true); setError(""); setSaved(false);
    try {
      const updated = await api<PaymentSettings>("/admin/payment-settings", {
        method: "PUT",
        body: JSON.stringify({ expected_version: form.version, enabled: form.enabled, mock_enabled: form.mock_enabled, api_url: form.api_url, pid: form.pid, types: form.types, ...(key ? { key } : {}), ...(clearKey ? { clear_key: true } : {}) }),
      });
      setBaseline(updated); setForm(updated); setKey(""); setClearKey(false); setSaved(true);
    } catch (e) {
      setError(e instanceof ApiError && e.code === "version_conflict" ? "支付配置已被其他管理员更新，当前更改尚未保存。" : e instanceof ApiError && e.code === "payment_credentials_in_use" ? "当前商户仍有关联支付等待处理，暂时无法变更或清除凭据。" : (e as Error).message);
    } finally { setBusy(false); }
  }

  async function reload() {
    if (busy || !manage) return;
    if (dirty && !window.confirm("重新加载会放弃当前未保存的更改，是否继续？")) return;
    setBusy(true); setError(""); setSaved(false);
    try {
      const updated = await api<PaymentSettings>("/admin/payment-settings");
      setBaseline(updated); setForm(updated); setKey(""); setClearKey(false);
    } catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  }

  function edit(patch: Partial<PaymentSettings>) { setForm(current => ({ ...current, ...patch })); setSaved(false); }
  function cancel() { setForm(baseline); setKey(""); setClearKey(false); setError(""); setSaved(false); }
  function clearMerchantKey() {
    if (form.enabled || busy || !manage) return;
    if (!window.confirm("确认清除已保存的商户密钥？保存更改后将无法恢复。")) return;
    setClearKey(true); setKey(""); setSaved(false);
  }

  return <form onSubmit={save} autoComplete="off" className="max-w-4xl">
    <div className="flex items-center justify-end mb-5"><Button type="button" variant="outline" size="icon" title="重新加载支付配置" aria-label="重新加载支付配置" disabled={busy || !manage} onClick={() => void reload()}><RefreshCw /></Button></div>
    <fieldset disabled={busy || !manage} className="min-w-0 space-y-8">
      <section className="border-t pt-5">
        <div className="flex flex-wrap items-center justify-between gap-3 mb-5"><h2 className="text-base font-semibold">易支付</h2><span className="text-xs text-muted-foreground">{baseline.configured ? "后台配置已保存" : "使用部署配置"}</span></div>
        <label className="flex items-center gap-3 text-sm mb-5"><Checkbox checked={form.enabled} disabled={clearKey} onCheckedChange={checked => edit({ enabled: checked === true })} />启用在线支付</label>
        <div className="grid sm:grid-cols-2 gap-5">
          <label className="grid gap-2 text-sm min-w-0 sm:col-span-2">网关地址<Input aria-label="网关地址" type="url" value={form.api_url} maxLength={2048} required={form.enabled && form.types.length > 0} onChange={event => edit({ api_url: event.target.value })} /></label>
          <label className="grid gap-2 text-sm min-w-0">商户 ID<Input aria-label="商户 ID" value={form.pid} inputMode="numeric" pattern="[0-9]{1,20}" maxLength={20} required={form.enabled && form.types.length > 0} onChange={event => edit({ pid: event.target.value })} /></label>
          <div className="grid gap-2 text-sm min-w-0"><label className="grid gap-2"><span className="flex flex-wrap justify-between gap-2">商户密钥<span className={clearKey ? "text-destructive" : form.key_configured ? "text-emerald-700" : "text-muted-foreground"}>{clearKey ? "待清除" : form.key_configured ? "已配置" : "未配置"}</span></span><Input aria-label="商户密钥" type="password" autoComplete="new-password" value={key} disabled={clearKey} maxLength={1024} required={form.enabled && form.types.length > 0 && !form.key_configured} placeholder={form.key_configured ? "留空保留现有密钥" : "输入商户密钥"} onChange={event => { setKey(event.target.value); setSaved(false); }} /></label>{form.key_configured && (clearKey ? <Button type="button" variant="outline" size="sm" className="justify-self-start" onClick={() => setClearKey(false)}>保留密钥</Button> : <Button type="button" variant="outline" size="sm" className="justify-self-start" disabled={form.enabled} onClick={clearMerchantKey}><Trash2 />清除密钥</Button>)}</div>
        </div>
        {!baseline.encryption_configured && <p role="status" className="text-sm text-amber-700 mt-4">服务端凭据加密尚未配置</p>}
      </section>
      <section className="border-t pt-5">
        <h2 className="text-base font-semibold mb-5">支付方式</h2>
        <div className="grid grid-cols-2 sm:grid-cols-4 gap-4">{paymentTypes.map(item => <label key={item.value} className="flex items-center gap-3 text-sm"><Checkbox checked={form.types.includes(item.value)} onCheckedChange={checked => edit({ types: checked === true ? [...form.types, item.value] : form.types.filter(type => type !== item.value) })} />{item.label}</label>)}</div>
      </section>
      <section className="border-t pt-5">
        <h2 className="text-base font-semibold mb-5">测试支付</h2>
        <label className="flex items-center gap-3 text-sm"><Checkbox checked={form.mock_enabled} onCheckedChange={checked => edit({ mock_enabled: checked === true })} />启用模拟支付</label>
      </section>
      <div className="border-t pt-5 space-y-3">
        {error && <div role="alert" className="text-destructive text-sm break-words">{error}</div>}
        {saved && <p role="status" className="text-sm text-emerald-700">支付配置已保存</p>}
        <div className="flex flex-wrap justify-between items-center gap-4">
          <span className="text-xs text-muted-foreground">版本 {form.version} · {date(form.updated_at)}</span>
          <div className="flex flex-wrap gap-3"><Button disabled={busy || !dirty}><Save />{busy ? "保存中" : "保存更改"}</Button><Button type="button" variant="outline" disabled={busy || !dirty} onClick={cancel}>取消更改</Button></div>
        </div>
      </div>
    </fieldset>
  </form>;
}
