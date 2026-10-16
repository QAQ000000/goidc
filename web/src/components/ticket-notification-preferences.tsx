"use client";

import { useState } from "react";
import { api, ApiError } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { useAdminFormLeave } from "@/hooks/use-admin-form-leave";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Failure, Loading } from "./common";

type Preferences = { email_enabled: boolean; sms_enabled: boolean; version: number };
export function TicketNotificationPreferences() {
  const { data, error, refresh } = useResource<Preferences>("/me/ticket-notification-preferences");
  const [draft, setDraft] = useState<Preferences | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [conflict, setConflict] = useState(false);
  const form = draft ?? data;
  const dirty = !!draft && !!data && (draft.email_enabled !== data.email_enabled || draft.sms_enabled !== data.sms_enabled || draft.version !== data.version);
  useAdminFormLeave(dirty || busy);
  async function save() {
    if (!form) return;
    setBusy(true); setMessage("");
    try {
      await api<Preferences>("/me/ticket-notification-preferences", { method: "PATCH", body: JSON.stringify({ email_enabled: form.email_enabled, sms_enabled: form.sms_enabled, expected_version: form.version }) });
      setDraft(null); setConflict(false); setMessage("工单通知偏好已保存"); refresh();
    } catch (error) {
      if (error instanceof ApiError && error.status === 409) { setConflict(true); setMessage("偏好已在其他页面更新。当前草稿已保留，请加载最新版本后再次保存，或取消草稿。"); }
      else setMessage((error as Error).message);
    } finally { setBusy(false); }
  }
  async function rebase() {
    setBusy(true);
    try { const latest = await api<Preferences>("/me/ticket-notification-preferences"); setDraft({ ...(draft ?? latest), version: latest.version }); setConflict(false); setMessage("已加载最新版本；草稿保留，请核对后保存。"); }
    catch (error) { setMessage((error as Error).message); }
    finally { setBusy(false); }
  }
  return <section className="data-surface p-4 sm:p-6 mb-6"><h2 className="font-semibold">本人工单通知偏好</h2><p className="text-sm text-muted-foreground mt-2">邮件默认开启，仅向已验证邮箱发送；短信默认关闭，开启后仍须已验证手机号。实际发送还取决于渠道和模板配置。联系人邮件订阅单独管理。</p>{error ? <Failure error={error} retry={refresh} /> : !form ? <Loading /> : <form className="space-y-4 mt-5" onSubmit={event => { event.preventDefault(); void save(); }}><label className="flex items-center gap-3"><Checkbox disabled={busy} checked={form.email_enabled} onCheckedChange={value => setDraft({ ...form, email_enabled: value === true })} />接收工单邮件</label><label className="flex items-center gap-3"><Checkbox disabled={busy} checked={form.sms_enabled} onCheckedChange={value => setDraft({ ...form, sms_enabled: value === true })} />接收工单短信</label><div className="flex flex-wrap gap-3 items-center"><Button disabled={!dirty || busy || conflict}>{busy ? "处理中" : "保存通知偏好"}</Button><Button type="button" variant="outline" disabled={!draft || busy} onClick={() => { setDraft(null); setConflict(false); setMessage(""); refresh(); }}>取消</Button>{conflict && <Button type="button" variant="outline" disabled={busy} onClick={() => void rebase()}>加载最新版本并保留草稿</Button>}{dirty && <span className="text-sm text-muted-foreground">有未保存更改</span>}</div></form>}{message && <p role="status" className="mt-3 text-sm break-words">{message}</p>}</section>;
}
