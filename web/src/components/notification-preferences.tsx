"use client";

import { useState } from "react";
import { ExternalLink, RefreshCw } from "lucide-react";
import { api, ApiError, date } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { useAdminFormLeave } from "@/hooks/use-admin-form-leave";
import { canEnableNotification, isNotificationVersionConflict, rebaseNotificationPreference, safeNotificationBindingURL, type NotificationPreference, type NotificationPreferences as Preferences } from "@/lib/notification-center";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { WebPushPreferences } from "./web-push-preferences";
import { Checkbox } from "./ui/checkbox";

export function NotificationPreferences() {
  const resource = useResource<Preferences>("/me/notification-preferences", false);
  const [drafts, setDrafts] = useState<Record<string, NotificationPreference>>({});
  const [conflicts, setConflicts] = useState<Record<string, boolean>>({});
  const [instance, setInstance] = useState("");
  const [challenge, setChallenge] = useState<{ url: string; expires_at: string; instance_id: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [forbidden, setForbidden] = useState(false);
  const [message, setMessage] = useState("");
  const [webPushAvailable, setWebPushAvailable] = useState(false);
  const data = resource.data;
  const botInstances = data?.instances.filter(item => item.channel === "telegram") ?? [];
  const dirty = Object.keys(drafts).some(channel => {
    const current = data?.channels.find(item => item.channel === channel);
    return drafts[channel].enabled !== current?.enabled || (drafts[channel].event_scope ?? "invoice.paid") !== (current?.event_scope ?? "invoice.paid");
  });
  useAdminFormLeave(dirty || busy);

  function failure(error: unknown, fallback: string) {
    if (error instanceof ApiError && error.status === 403) {
      setForbidden(true);
      setMessage("当前会话无权修改通知设置。草稿已保留，请确认账户权限后重新加载。");
    } else setMessage(error instanceof Error ? error.message : fallback);
  }

  async function save(preference: NotificationPreference) {
    if (busy || forbidden) return;
    setBusy(true); setMessage("");
    try {
      await api("/me/notification-preferences", { method: "PATCH", body: JSON.stringify({ channel: preference.channel, enabled: preference.enabled, event_scope: preference.event_scope ?? "invoice.paid", expected_version: preference.version }) });
      setDrafts(current => { const next = { ...current }; delete next[preference.channel]; return next; });
      setConflicts(current => ({ ...current, [preference.channel]: false }));
      setMessage(`${preference.name}通知偏好已保存`); resource.refresh();
    } catch (error) {
      if (isNotificationVersionConflict(error)) {
        setConflicts(current => ({ ...current, [preference.channel]: true }));
        setMessage("通知偏好已在其他页面更新。草稿已保留，请加载最新版本后核对并保存。");
      } else failure(error, "保存失败");
    } finally { setBusy(false); }
  }

  async function rebase(channel: string) {
    setBusy(true); setMessage("");
    try {
      const latest = await api<Preferences>("/me/notification-preferences");
      const preference = latest.channels.find(item => item.channel === channel);
      if (!preference) throw new Error("该通知渠道当前不可用，请取消草稿并刷新。");
      setDrafts(current => ({ ...current, [channel]: rebaseNotificationPreference(current[channel] ?? preference, preference) }));
      setConflicts(current => ({ ...current, [channel]: false }));
      setMessage("已加载最新版本，草稿已保留；请核对绑定状态后保存。"); resource.refresh();
    } catch (error) { failure(error, "加载失败"); }
    finally { setBusy(false); }
  }

  async function bind() {
    const selected = instance || botInstances[0]?.id;
    if (!selected || busy || forbidden) return;
    setBusy(true); setMessage("");
    try {
      const result = await api<{ url: string; expires_at: string }>("/me/notification-bindings/challenges", { method: "POST", body: JSON.stringify({ instance_id: selected }) });
      const url = safeNotificationBindingURL(result.url);
      if (!url) throw new Error("绑定链接无效，请联系管理员检查机器人配置。");
      setChallenge({ ...result, url, instance_id: selected });
      setMessage("已创建一次性绑定链接。请在机器人私聊中点击开始，再返回刷新绑定状态。");
    } catch (error) { failure(error, "创建绑定失败"); }
    finally { setBusy(false); }
  }

  async function unbind(id: string | number, channel: string, version: number) {
    if (busy || forbidden || !window.confirm("解除此通知绑定？后续通知将不再发送到该身份，重新接收需要再次验证绑定并开启偏好。")) return;
    setBusy(true); setMessage("");
    try {
      await api(`/me/notification-bindings/${encodeURIComponent(String(id))}`, { method: "DELETE", body: JSON.stringify({ expected_version: version }) });
      setDrafts(current => { const next = { ...current }; delete next[channel]; return next; });
      setConflicts(current => ({ ...current, [channel]: false }));
      setChallenge(null); setMessage("绑定已解除，请确认最新通知偏好。"); resource.refresh();
    } catch (error) {
      if (isNotificationVersionConflict(error)) setMessage("绑定已在其他页面更新。当前草稿已保留，请刷新绑定状态后重试。");
      else failure(error, "解除绑定失败");
    } finally { setBusy(false); }
  }

  return <section className="data-surface mb-6 min-w-0 p-4 sm:p-6">
    <div className="flex flex-wrap items-center justify-between gap-3"><h2 className="font-semibold">业务通知渠道</h2><Button type="button" size="sm" variant="outline" disabled={busy} onClick={() => { setForbidden(false); resource.refresh(); }}><RefreshCw className="size-4" />刷新绑定状态</Button></div>
    <p className="mt-2 text-sm text-muted-foreground">机器人、浏览器通知和站内信默认关闭。完成身份绑定或浏览器订阅后仍需主动开启业务通知；开启后接收管理员已启用的各类业务事件，包括下单、付款、服务状态和到期提醒。既有“仅付款成功”订阅需主动扩展，扩展后不补发历史通知。邮件、短信沿用现有设置，工单订阅和验证码单独管理。</p>
    {resource.error instanceof ApiError && resource.error.status === 403 ? <p role="alert" className="mt-4 text-sm text-destructive">当前账户无权查看通知设置。</p> : resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !data ? <Loading /> : <>
      <div className="mt-5 divide-y">{data.channels.map(preference => {
        const form = drafts[preference.channel] ?? preference;
        const available = canEnableNotification(preference, data.targets, data.instances, webPushAvailable);
        const business = form.event_scope === "business";
        const unchanged = form.enabled === preference.enabled && (form.event_scope ?? "invoice.paid") === (preference.event_scope ?? "invoice.paid");
        return <div key={preference.channel} className="space-y-3 py-4">
          <label className="flex items-start gap-3 text-sm"><Checkbox disabled={busy || forbidden || (!available && !form.enabled)} checked={form.enabled} onCheckedChange={value => setDrafts(current => ({ ...current, [preference.channel]: { ...form, enabled: value === true } }))} /><span>通过{preference.name}{business ? "接收业务通知" : "仅接收付款成功通知"}{preference.requires_binding && !available && <span className="mt-1 block text-xs text-muted-foreground">{preference.channel === "web_push" ? "请先在下方订阅浏览器通知。" : "暂无可用的已验证绑定；请先完成可用机器人的私聊绑定。"}</span>}</span></label>
          <div className="flex flex-wrap items-center gap-2"><Button type="button" size="sm" disabled={busy || forbidden || !!conflicts[preference.channel] || unchanged || (form.enabled && !available)} onClick={() => void save(form)}>保存{preference.name}偏好</Button>{!business && <Button type="button" size="sm" variant="outline" disabled={busy || forbidden || !available || !!conflicts[preference.channel]} onClick={() => {
            const expanded = { ...form, enabled: true, event_scope: "business" as const };
            setDrafts(current => ({ ...current, [preference.channel]: expanded }));
            void save(expanded);
          }}>扩展为业务通知</Button>}{drafts[preference.channel] && <Button type="button" size="sm" variant="ghost" disabled={busy} onClick={() => { setDrafts(current => { const next = { ...current }; delete next[preference.channel]; return next; }); setConflicts(current => ({ ...current, [preference.channel]: false })); }}>取消草稿</Button>}{conflicts[preference.channel] && <Button type="button" size="sm" variant="outline" disabled={busy || forbidden} onClick={() => void rebase(preference.channel)}>加载最新版本并保留草稿</Button>}</div>
        </div>;
      })}</div>
      <div className="mt-3 space-y-3 border-t pt-5"><h3 className="text-sm font-medium">已验证的通知身份</h3>{!data.targets.length ? <p className="text-sm text-muted-foreground">尚未绑定机器人。</p> : data.targets.filter(target => target.channel === "telegram").map(target => <div key={target.id} className="flex min-w-0 flex-wrap items-center justify-between gap-3 rounded-md border p-3"><div className="min-w-0 text-sm"><p className="break-all">{target.label}</p><p className="mt-1 text-xs text-muted-foreground">{data.channels.find(item => item.channel === target.channel)?.name ?? target.channel}{!data.instances.some(item => item.id === target.instance_id) && " · 通知实例当前不可用"}</p></div><Button type="button" size="sm" variant="outline" disabled={busy || forbidden} onClick={() => void unbind(target.id, target.channel, target.version)}>解除绑定</Button></div>)}</div>
      {!!botInstances.length && <div className="mt-5 space-y-3"><label className="grid gap-2 text-sm">选择机器人<select className="h-10 min-w-0 rounded-md border bg-background px-3" aria-label="选择通知机器人" value={instance || botInstances[0].id} disabled={busy || forbidden} onChange={event => { setInstance(event.target.value); setChallenge(null); }}>{botInstances.map(item => <option key={item.id} value={item.id}>{item.name}{item.bot_username ? ` (@${item.bot_username})` : ""}</option>)}</select></label><Button type="button" variant="outline" disabled={busy || forbidden} onClick={() => void bind()}>创建一次性绑定链接</Button><p className="text-xs text-muted-foreground">链接有效期为 10 分钟，成功绑定后立即失效。链接仅供本人使用，请勿转发。请在机器人私聊中完成绑定；群聊不支持。重新生成后请使用最新链接。</p></div>}
      {challenge && !data.targets.some(target => target.instance_id === challenge.instance_id) && <div className="mt-4 space-y-2 rounded-md border bg-muted/20 p-4"><Button asChild variant="outline"><a href={challenge.url} target="_blank" rel="noopener noreferrer"><ExternalLink className="size-4" />打开机器人完成绑定</a></Button><p className="text-xs text-muted-foreground">有效至 {date(challenge.expires_at)}。完成后点击“刷新绑定状态”，再开启并保存通知偏好。</p></div>}
      {!botInstances.length && data.channels.some(item => item.channel === "telegram") && <p className="mt-4 text-sm text-muted-foreground">当前没有可绑定的机器人，请联系管理员完成渠道配置。</p>}
    </>}
    <WebPushPreferences onAvailabilityChange={setWebPushAvailable} />
    {message && <p role="status" className="mt-4 break-words text-sm">{message}</p>}
  </section>;
}

type InboxMessage = { id: number; subject: string; body: string; read_at: string | null; created_at: string };
export function NotificationInbox() {
  const [offset, setOffset] = useState(0);
  const [busy, setBusy] = useState<number | null>(null);
  const [message, setMessage] = useState("");
  const resource = useResource<{ items: InboxMessage[]; has_more: boolean }>(`/me/notification-inbox?limit=20&offset=${offset}`, false);
  async function read(id: number) {
    setBusy(id); setMessage("");
    try { await api(`/me/notification-inbox/${id}/read`, { method: "POST", body: "{}" }); resource.refresh(); }
    catch (error) { setMessage(error instanceof ApiError && error.status === 403 ? "当前账户无权修改此通知。" : (error as Error).message); }
    finally { setBusy(null); }
  }
  return <section className="data-surface mb-6 min-w-0 p-4 sm:p-6"><div className="flex flex-wrap items-center justify-between gap-3"><h2 className="font-semibold">我的站内通知</h2><Button type="button" size="sm" variant="outline" onClick={resource.refresh}><RefreshCw className="size-4" />刷新通知</Button></div>{resource.error instanceof ApiError && resource.error.status === 403 ? <p role="alert" className="mt-4 text-sm text-destructive">当前账户无权查看站内通知。</p> : resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <><div className="mt-4 divide-y">{!resource.data.items.length ? <p className="py-5 text-sm text-muted-foreground">暂无站内通知</p> : resource.data.items.map(item => <article key={item.id} className="space-y-2 py-4"><div className="flex flex-wrap items-start justify-between gap-3"><h3 className="min-w-0 break-words text-sm font-medium">{item.subject}</h3><span className="text-xs text-muted-foreground">{item.read_at ? "已读" : "未读"}</span></div><p className="whitespace-pre-wrap break-words text-sm">{item.body}</p><div className="flex flex-wrap items-center justify-between gap-3"><time className="text-xs text-muted-foreground">{date(item.created_at)}</time>{!item.read_at && <Button type="button" size="sm" variant="outline" disabled={busy !== null} onClick={() => void read(item.id)}>{busy === item.id ? "保存中" : "标记已读"}</Button>}</div></article>)}</div><div className="mt-4 flex items-center justify-between gap-3"><Button type="button" size="sm" variant="outline" disabled={offset === 0 || busy !== null} onClick={() => setOffset(Math.max(0, offset - 20))}>上一页</Button><span className="text-xs text-muted-foreground">第 {offset / 20 + 1} 页</span><Button type="button" size="sm" variant="outline" disabled={!resource.data.has_more || busy !== null} onClick={() => setOffset(offset + 20)}>下一页</Button></div></>}{message && <p role="status" className="mt-3 text-sm">{message}</p>}</section>;
}
