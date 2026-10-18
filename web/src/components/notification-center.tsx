"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { RefreshCw } from "lucide-react";
import { api, ApiError, date } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { useAdminFormLeave } from "@/hooks/use-admin-form-leave";
import { isNotificationVersionConflict, notificationChannelName, notificationDeliveryState, notificationEventName, type NotificationCenterData, type NotificationDelivery, type NotificationRule, type NotificationTemplate } from "@/lib/notification-center";
import { useAdminPermission } from "./workspace";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Badge } from "./ui/badge";
import { Checkbox } from "./ui/checkbox";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";

type Section = "rules" | "templates" | "deliveries" | "channels";
const sections: { value: Section; label: string }[] = [{ value: "rules", label: "路由规则" }, { value: "templates", label: "渠道模板" }, { value: "deliveries", label: "投递记录" }, { value: "channels", label: "通知渠道" }];

export function NotificationCenter({ onDirty }: { onDirty?: (dirty: boolean) => void }) {
  const view = useAdminPermission("notifications.view");
  const manage = useAdminPermission("notifications.manage");
  const settings = useAdminPermission("settings.manage");
  const [section, setSection] = useState<Section>("rules");
  const [offset, setOffset] = useState(0);
  const resource = useResource<NotificationCenterData>(view ? `/admin/notification-center?limit=50&offset=${offset}` : null, false);
  const [ruleDrafts, setRuleDrafts] = useState<Record<string, NotificationRule>>({});
  const [templateDrafts, setTemplateDrafts] = useState<Record<string, NotificationTemplate>>({});
  const [conflicts, setConflicts] = useState<Record<string, boolean>>({});
  const [busy, setBusy] = useState("");
  const [forbidden, setForbidden] = useState(false);
  const [message, setMessage] = useState("");
  const dirty = Object.keys(ruleDrafts).length > 0 || Object.keys(templateDrafts).length > 0;
  const canWrite = manage && !forbidden;
  const data = resource.data;
  useAdminFormLeave(dirty || !!busy);
  useEffect(() => { onDirty?.(dirty || !!busy); return () => onDirty?.(false); }, [dirty, busy, onDirty]);

  function mutationError(error: unknown, key: string) {
    if (error instanceof ApiError && error.status === 403) {
      setForbidden(true); setMessage("当前账号已无权修改通知配置。草稿已保留，请确认权限后重新加载。");
    } else if (isNotificationVersionConflict(error)) {
      setConflicts(current => ({ ...current, [key]: true })); setMessage("配置已被其他管理员修改。草稿已保留，请加载最新版本后核对并保存。");
    } else setMessage((error as Error).message);
  }

  async function saveRule(form: NotificationRule) {
    if (busy || !canWrite) return;
    const key = `rule:${form.id}`; setBusy(key); setMessage("");
    try {
      await api(`/admin/notification-center/rules/${encodeURIComponent(form.id)}`, { method: "PATCH", body: JSON.stringify({ enabled: form.enabled, instance_id: form.instance_id || null, template_id: form.template_id || null, expected_version: form.version }) });
      setRuleDrafts(current => { const next = { ...current }; delete next[form.id]; return next; });
      setConflicts(current => ({ ...current, [key]: false }));
      setMessage("付款成功通知规则已保存"); resource.refresh();
    } catch (error) { mutationError(error, key); }
    finally { setBusy(""); }
  }

  async function saveTemplate(form: NotificationTemplate) {
    if (busy || !canWrite) return;
    const key = `template:${form.id}`; setBusy(key); setMessage("");
    try {
      await api(`/admin/notification-center/templates/${encodeURIComponent(form.id)}`, { method: "PATCH", body: JSON.stringify({ subject: form.subject, body: form.body, active: form.active, expected_version: form.version }) });
      setTemplateDrafts(current => { const next = { ...current }; delete next[form.id]; return next; });
      setConflicts(current => ({ ...current, [key]: false }));
      setMessage("付款成功渠道模板已保存"); resource.refresh();
    } catch (error) { mutationError(error, key); }
    finally { setBusy(""); }
  }

  async function rebase(kind: "rule" | "template", id: string) {
    const key = `${kind}:${id}`; setBusy(key); setMessage("");
    try {
      const latest = await api<NotificationCenterData>(`/admin/notification-center?limit=50&offset=${offset}`);
      if (kind === "rule") {
        const item = latest.rules.find(item => item.id === id);
        if (!item) throw new Error("该规则已不可用，请取消草稿并刷新。");
        setRuleDrafts(current => ({ ...current, [id]: { ...(current[id] ?? item), version: item.version } }));
      } else {
        const item = latest.templates.find(item => item.id === id);
        if (!item) throw new Error("该模板已不可用，请取消草稿并刷新。");
        setTemplateDrafts(current => ({ ...current, [id]: { ...(current[id] ?? item), version: item.version } }));
      }
      setConflicts(current => ({ ...current, [key]: false })); setMessage("已加载最新版本，草稿已保留；核对后可再次保存。"); resource.refresh();
    } catch (error) { mutationError(error, key); }
    finally { setBusy(""); }
  }

  async function configureWebhook(instanceID: string) {
    if (busy || !settings || forbidden) return;
    const key = `webhook:${instanceID}`; setBusy(key); setMessage("");
    try {
      await api(`/admin/notification-center/channels/${encodeURIComponent(instanceID)}/webhook`, { method: "POST", body: "{}" });
      setMessage("机器人回调已配置。用户可在账户中发起私聊绑定；此操作不代表通知已送达。"); resource.refresh();
    } catch (error) { mutationError(error, key); }
    finally { setBusy(""); }
  }

  function resetRule(id: string) { setRuleDrafts(current => { const next = { ...current }; delete next[id]; return next; }); setConflicts(current => ({ ...current, [`rule:${id}`]: false })); }
  function resetTemplate(id: string) { setTemplateDrafts(current => { const next = { ...current }; delete next[id]; return next; }); setConflicts(current => ({ ...current, [`template:${id}`]: false })); }

  if (!view || resource.error instanceof ApiError && resource.error.status === 403) return <p role="alert" className="rounded-md border p-5 text-sm text-muted-foreground">当前账号没有通知查看权限。</p>;
  return <section className="min-w-0 space-y-4" aria-label="统一通知中心">
    <div className="flex flex-wrap items-start justify-between gap-3"><p className="max-w-3xl text-sm text-muted-foreground">付款成功通知支持机器人和站内信。关闭规则或停用模板仅影响付款成功；邮件、短信使用各自的模板与队列，工单和验证码独立设置。</p><Button type="button" size="sm" variant="outline" disabled={!!busy} onClick={() => { setForbidden(false); resource.refresh(); }}><RefreshCw className="size-4" />刷新通知中心</Button></div>
    <nav aria-label="通知中心分区" className="flex overflow-x-auto border-b">{sections.map(item => <button key={item.value} type="button" aria-current={section === item.value ? "page" : undefined} className={`shrink-0 border-b-2 px-4 py-2 text-sm ${section === item.value ? "border-primary font-medium text-primary" : "border-transparent text-muted-foreground"}`} onClick={() => setSection(item.value)}>{item.label}</button>)}</nav>
    {message && <p role="status" className="break-words text-sm">{message}</p>}
    {resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !data ? <Loading /> : section === "rules" ? <div className="space-y-4">{!data.rules.length && <p className="py-8 text-center text-sm text-muted-foreground">暂无通知规则</p>}{data.rules.map(rule => {
      const form = ruleDrafts[rule.id] ?? rule;
      const key = `rule:${rule.id}`;
      const instances = data.instances.filter(item => item.channel === rule.channel);
      const templates = data.templates.filter(item => item.channel === rule.channel && item.event_kind === rule.event_kind);
      return <form key={rule.id} className="data-surface min-w-0 space-y-4 p-4 sm:p-5" onSubmit={event => { event.preventDefault(); void saveRule(form); }}>
        <h3 className="text-sm font-semibold">{notificationEventName(rule.event_kind)} · {notificationChannelName(data.channels, rule.channel)}</h3>
        <fieldset disabled={!canWrite || !!busy} className="space-y-4"><label className="flex items-center gap-3 text-sm"><Checkbox checked={form.enabled} onCheckedChange={value => setRuleDrafts(current => ({ ...current, [rule.id]: { ...form, enabled: value === true } }))} />启用此付款成功通知规则</label><div className="grid min-w-0 gap-4 md:grid-cols-2">{instances.length > 0 && <label className="grid min-w-0 gap-2 text-sm">通知实例<select aria-label={`${notificationChannelName(data.channels, rule.channel)}通知实例`} className="h-10 min-w-0 rounded-md border bg-background px-3" required={form.enabled} value={form.instance_id ?? ""} onChange={event => setRuleDrafts(current => ({ ...current, [rule.id]: { ...form, instance_id: event.target.value || null } }))}><option value="">请选择通知实例</option>{form.instance_id && !instances.some(item => item.id === form.instance_id) && <option value={form.instance_id}>当前实例不可用（{form.instance_id}）</option>}{instances.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>}<label className="grid min-w-0 gap-2 text-sm">渠道模板<select aria-label={`${notificationChannelName(data.channels, rule.channel)}渠道模板`} className="h-10 min-w-0 rounded-md border bg-background px-3" required value={form.template_id ?? ""} onChange={event => setRuleDrafts(current => ({ ...current, [rule.id]: { ...form, template_id: event.target.value || null } }))}><option value="">请选择渠道模板</option>{form.template_id && !templates.some(item => item.id === form.template_id) && <option value={form.template_id}>当前模板不可用</option>}{templates.map(item => <option key={item.id} value={item.id}>{notificationEventName(item.event_kind)} · {item.id}{item.active ? "" : "（已停用）"}</option>)}</select></label></div></fieldset>
        {data.channels.find(item => item.channel === rule.channel)?.requires_binding && !instances.length && <p className="text-sm text-amber-700">当前没有可用通知实例。请先在接口设置中配置，用户完成绑定并开启偏好后才会投递。</p>}
        <div className="flex flex-wrap gap-2">{manage && <Button size="sm" disabled={!canWrite || !!busy || !ruleDrafts[rule.id] || !!conflicts[key]}>{busy === key ? "保存中" : "保存规则"}</Button>}{ruleDrafts[rule.id] && <Button type="button" size="sm" variant="ghost" disabled={!!busy} onClick={() => resetRule(rule.id)}>取消草稿</Button>}{conflicts[key] && <Button type="button" size="sm" variant="outline" disabled={!!busy || !canWrite} onClick={() => void rebase("rule", rule.id)}>加载最新版本并保留草稿</Button>}</div>
      </form>;
    })}</div> : section === "templates" ? <div className="space-y-4">{!data.templates.length && <p className="py-8 text-center text-sm text-muted-foreground">暂无统一渠道模板</p>}{data.templates.map(template => {
      const form = templateDrafts[template.id] ?? template;
      const key = `template:${template.id}`;
      return <form key={template.id} className="data-surface min-w-0 space-y-4 p-4 sm:p-5" onSubmit={event => { event.preventDefault(); void saveTemplate(form); }}><h3 className="text-sm font-semibold">{notificationEventName(template.event_kind)} · {notificationChannelName(data.channels, template.channel)}</h3><fieldset className="space-y-4" disabled={!canWrite || !!busy}><label className="grid gap-2 text-sm">通知主题<Input required value={form.subject} maxLength={240} onChange={event => setTemplateDrafts(current => ({ ...current, [template.id]: { ...form, subject: event.target.value } }))} /></label><label className="grid gap-2 text-sm">通知内容<Textarea value={form.body} required maxLength={4000} rows={5} onChange={event => setTemplateDrafts(current => ({ ...current, [template.id]: { ...form, body: event.target.value } }))} /></label><label className="flex items-center gap-3 text-sm"><Checkbox checked={form.active} onCheckedChange={value => setTemplateDrafts(current => ({ ...current, [template.id]: { ...form, active: value === true } }))} />启用此付款成功模板</label></fieldset><p className="text-xs text-muted-foreground">{"支持变量 {{invoice_no}}（账单号）和 {{amount}}（金额），以纯文本发送。停用后，该渠道的付款成功通知将被跳过。"}</p><div className="flex flex-wrap gap-2">{manage && <Button size="sm" disabled={!canWrite || !!busy || !templateDrafts[template.id] || !!conflicts[key]}>{busy === key ? "保存中" : "保存渠道模板"}</Button>}{templateDrafts[template.id] && <Button type="button" size="sm" variant="ghost" disabled={!!busy} onClick={() => resetTemplate(template.id)}>取消草稿</Button>}{conflicts[key] && <Button type="button" size="sm" variant="outline" disabled={!!busy || !canWrite} onClick={() => void rebase("template", template.id)}>加载最新版本并保留草稿</Button>}</div></form>;
    })}</div> : section === "deliveries" ? <><p className="text-xs text-muted-foreground">“渠道已接受”不代表最终送达或已读。“结果未知”需要人工核查，系统不会自动重试。</p><NotificationDeliveryTable deliveries={data.deliveries} channels={data.channels} /><div className="flex items-center justify-between gap-3"><Button type="button" size="sm" variant="outline" disabled={offset === 0 || !!busy} onClick={() => setOffset(Math.max(0, offset - 50))}>上一页</Button><span className="text-xs text-muted-foreground">第 {offset / 50 + 1} 页 · 每页 50 条</span><Button type="button" size="sm" variant="outline" disabled={!data.has_more || !!busy} onClick={() => setOffset(offset + 50)}>下一页</Button></div></> : <div className="space-y-4"><div className="flex flex-wrap gap-2">{data.channels.map(channel => <Badge key={channel.channel} variant="outline">{channel.name}</Badge>)}</div>{settings && <Button asChild variant="outline" size="sm"><Link href="/admin/channels?category=bot">配置机器人通知实例</Link></Button>}{!data.instances.length && <p className="py-5 text-sm text-muted-foreground">当前没有可用机器人实例。站内信由系统提供。</p>}{data.instances.map(instance => <div key={instance.id} className="data-surface min-w-0 space-y-3 p-4"><h3 className="break-words text-sm font-medium">{instance.name}</h3><p className="break-all text-xs text-muted-foreground">{notificationChannelName(data.channels, instance.channel)}{instance.bot_username ? ` · @${instance.bot_username}` : ""}</p>{settings && <><p className="text-xs text-muted-foreground">将当前站点回调地址注册到机器人平台。请先确认站点使用公网可访问的 HTTPS 地址。</p><Button type="button" size="sm" variant="outline" disabled={!!busy || forbidden} onClick={() => void configureWebhook(instance.id)}>{busy === `webhook:${instance.id}` ? "配置中" : "配置机器人回调"}</Button></>}</div>)}</div>}
  </section>;
}

export function NotificationDeliveryTable({ deliveries, channels }: { deliveries: NotificationDelivery[]; channels: NotificationCenterData["channels"] }) {
  return <div className="min-w-0 overflow-x-auto border-y"><Table><TableHeader><TableRow><TableHead>ID / 创建时间</TableHead><TableHead>事件 / 客户</TableHead><TableHead>通知渠道</TableHead><TableHead>投递状态</TableHead><TableHead>尝试次数</TableHead><TableHead>结果详情</TableHead></TableRow></TableHeader><TableBody>{deliveries.map(delivery => {
    const status = notificationDeliveryState(delivery.status);
    return <TableRow key={delivery.id}><TableCell><span className="break-all">{delivery.id}</span><p className="mt-1 text-xs text-muted-foreground">{date(delivery.created_at)}</p></TableCell><TableCell>{notificationEventName(delivery.event_kind)}<p className="mt-1 text-xs text-muted-foreground">客户 #{delivery.user_id}</p></TableCell><TableCell>{notificationChannelName(channels, delivery.channel)}{delivery.instance_id && <p className="mt-1 max-w-48 whitespace-normal break-all text-xs text-muted-foreground">{delivery.instance_id}</p>}</TableCell><TableCell className="max-w-72 whitespace-normal"><Badge variant="outline" className={delivery.status === "unknown" ? "text-amber-700" : undefined}>{status.label}</Badge>{status.detail && <p className="mt-2 text-xs text-muted-foreground">{status.detail}</p>}</TableCell><TableCell>{delivery.attempt_count ?? delivery.attempts ?? 0}</TableCell><TableCell className="max-w-72 whitespace-normal break-all text-xs"><p>{delivery.code || "—"}</p>{delivery.provider_message_id && <p className="mt-1 text-muted-foreground">渠道消息编号：{delivery.provider_message_id}</p>}{delivery.legacy_outbox_id && <p className="mt-1 text-muted-foreground">原队列 #{delivery.legacy_outbox_id}</p>}</TableCell></TableRow>;
  })}{!deliveries.length && <TableRow><TableCell colSpan={6} className="py-10 text-center text-muted-foreground">暂无符合条件的投递记录</TableCell></TableRow>}</TableBody></Table></div>;
}
