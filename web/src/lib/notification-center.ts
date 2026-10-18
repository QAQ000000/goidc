export type NotificationChannel = { channel: string; name: string; requires_binding?: boolean };
export type NotificationInstance = { id: string; name: string; channel: string; bot_username?: string };
export type NotificationPreference = NotificationChannel & { enabled: boolean; version: number; requires_binding: boolean };
export type NotificationTarget = { id: string | number; channel: string; instance_id: string; label: string; version: number };
export type NotificationPreferences = { channels: NotificationPreference[]; targets: NotificationTarget[]; instances: NotificationInstance[] };
export type NotificationRule = { id: string; event_kind: string; channel: string; enabled: boolean; version: number; instance_id: string | null; template_id: string | null };
export type NotificationTemplate = { id: string; event_kind: string; channel: string; subject: string; body: string; active: boolean; version: number };
export type NotificationDelivery = { id: string | number; event_kind: string; user_id: number; channel: string; instance_id: string | null; status: string; code: string; attempt_count?: number; attempts?: number; created_at: string; provider_message_id: string; legacy_outbox_id?: number | null };
export type NotificationCenterData = { channels: NotificationChannel[]; rules: NotificationRule[]; templates: NotificationTemplate[]; deliveries: NotificationDelivery[]; instances: NotificationInstance[]; has_more?: boolean };

const deliveryStates: Record<string, { label: string; detail?: string }> = {
  pending: { label: "等待发送" },
  sending: { label: "发送中" },
  accepted: { label: "渠道已接受", detail: "渠道已接受请求，不代表最终送达或已读。" },
  delivered: { label: "站内信已写入" },
  failed: { label: "发送失败" },
  unknown: { label: "结果未知", detail: "外部结果尚未确认，须核查；不会自动重试，以免重复发送。" },
  skipped: { label: "已跳过" },
  archived: { label: "原日志已清理", detail: "原邮件或短信日志已按保留策略清理，历史投递结果不可再查询。" },
  cancelled: { label: "已取消" },
  legacy_queued: { label: "原队列已入队", detail: "邮件、短信沿用原队列状态，请在投递队列查看发送结果。" },
};

export function notificationDeliveryState(status: string) {
  return deliveryStates[status] ?? { label: status };
}

export function notificationChannelName(channels: NotificationChannel[], channel: string) {
  return channels.find(item => item.channel === channel)?.name ?? channel;
}

export function notificationEventName(event: string) {
  return event === "invoice.paid" ? "付款成功" : event;
}

export function canEnableNotification(preference: NotificationPreference, targets: NotificationTarget[], instances: NotificationInstance[]) {
  return !preference.requires_binding || targets.some(target => target.channel === preference.channel && instances.some(instance => instance.id === target.instance_id && instance.channel === target.channel));
}

export function isNotificationVersionConflict(error: unknown) {
  return typeof error === "object" && error !== null && "status" in error && error.status === 409 && "code" in error && error.code === "version_conflict";
}

// Only a verified bot deep link is offered to the user; no arbitrary URL or
// user-entered chat ID can become a bound notification target.
export function safeNotificationBindingURL(raw: string) {
  try {
    const url = new URL(raw);
    return url.protocol === "https:" && url.hostname === "t.me" && !url.port && !url.username && !url.password && /^\/[A-Za-z0-9_]+$/.test(url.pathname) && !!url.searchParams.get("start") ? url.href : null;
  } catch {
    return null;
  }
}

export function rebaseNotificationPreference(draft: NotificationPreference, latest: NotificationPreference): NotificationPreference {
  return { ...latest, enabled: draft.enabled };
}
