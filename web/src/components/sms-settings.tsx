"use client";
import { useState } from "react";
import { Save, Send, RefreshCw } from "lucide-react";
import { api, date } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace, useAdminPermission } from "./workspace";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Checkbox } from "./ui/checkbox";

type Settings = { provider: "generic_http_json"; endpoint: string; enabled: boolean; token_configured: boolean; encryption_configured: boolean; configured: boolean; version: number; updated_at: string };
export function SMSSettingsView() {
  const resource = useResource<Settings>("/admin/sms-settings", false);
  return <Workspace title="短信发送设置" admin><div className="flex justify-between mb-6"><h1 className="page-title">短信发送设置</h1><Button type="button" variant="outline" size="icon" aria-label="重新加载短信配置" title="重新加载短信配置" onClick={resource.refresh}><RefreshCw /></Button></div>{resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <SMSForm key={resource.data.version} initial={resource.data} />}</Workspace>;
}
function SMSForm({ initial }: { initial: Settings }) {
  const manage = useAdminPermission("@super");
  const [baseline, setBaseline] = useState(initial);
  const [form, setForm] = useState(initial);
  const [token, setToken] = useState("");
  const [clear, setClear] = useState(false);
  const [recipient, setRecipient] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const dirty = form.endpoint !== baseline.endpoint || form.enabled !== baseline.enabled || !!token || clear;
  async function save(event: React.FormEvent) {
    event.preventDefault(); if (busy || !manage) return;
    setBusy(true); setError(""); setMessage("");
    try { const next = await api<Settings>("/admin/sms-settings", {method:"PUT", body:JSON.stringify({provider:form.provider,endpoint:form.endpoint,enabled:form.enabled,...(token ? {token}:{}),clear_token:clear,expected_version:baseline.version})}); setForm(next); setBaseline(next); setToken(""); setClear(false); setMessage("短信配置已保存"); } catch(e) {setError((e as Error).message);} finally {setBusy(false);}
  }
  async function test() {
    if (busy || !manage || dirty) return;
    setBusy(true); setError(""); setMessage("");
    try { const result = await api<{queued:boolean}>("/admin/sms-settings/test", {method:"POST",body:JSON.stringify({recipient,expected_version:baseline.version})}); setMessage(result.queued ? "测试短信已加入发送队列" : "测试短信未加入发送队列"); } catch(e) {setError((e as Error).message);} finally {setBusy(false);}
  }
  return <form className="max-w-4xl space-y-5" autoComplete="off" onSubmit={save}><p className="text-sm text-muted-foreground">配置来源：{baseline.configured ? "后台配置" : "部署配置"}</p><fieldset disabled={busy || !manage} className="space-y-5 min-w-0"><label className="flex items-center gap-2 text-sm"><Checkbox checked={form.enabled} disabled={clear} onCheckedChange={value => setForm({...form,enabled:value === true})} />启用短信发送</label><section className="grid sm:grid-cols-2 gap-5 border-y py-5"><label className="grid gap-2 text-sm">服务商<select className="h-9 border rounded-md px-3 bg-background" value={form.provider} disabled><option value="generic_http_json">通用 HTTP JSON</option></select></label><label className="grid gap-2 text-sm">发送端点<Input type="url" required={form.enabled} value={form.endpoint} maxLength={2048} onChange={e => setForm({...form,endpoint:e.target.value})} /></label><label className="grid gap-2 text-sm">认证令牌<span className="text-muted-foreground">{form.token_configured ? "已配置" : "未配置"}</span><Input type="password" autoComplete="new-password" value={token} disabled={clear} maxLength={4096} placeholder="留空保留现有令牌" onChange={e => setToken(e.target.value)} /></label>{form.token_configured && <label className="flex items-center gap-2 text-sm"><Checkbox checked={clear} disabled={form.enabled} onCheckedChange={value => {setClear(value === true);setToken("");}} />清除令牌</label>}</section>{!baseline.encryption_configured && <p className="text-sm text-amber-700">服务端凭据加密尚未配置</p>}<div className="flex flex-wrap justify-between gap-3"><span className="text-xs text-muted-foreground">版本 {baseline.version} · {date(baseline.updated_at)}</span><Button disabled={!dirty || (!!token && !baseline.encryption_configured)}><Save />保存更改</Button></div><div className="flex flex-wrap gap-3 border-t pt-5"><Input aria-label="测试收件手机号" className="max-w-sm" type="tel" value={recipient} onChange={e => setRecipient(e.target.value)} /><Button type="button" variant="outline" disabled={dirty || !recipient || !baseline.enabled} onClick={() => void test()}><Send />测试发送</Button></div></fieldset>{error && <p role="alert" className="text-destructive text-sm">{error}</p>}{message && <p role="status" className="text-emerald-700 text-sm">{message}</p>}</form>;
}
