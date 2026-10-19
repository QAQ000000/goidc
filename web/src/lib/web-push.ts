export type WebPushInstance = { id: string; name: string; public_key: string; version: number };
export type WebPushSubscriptionRecord = { id: string | number; instance_id: string; endpoint_hash: string; version: number; current_session: boolean; active: boolean; revocable: boolean };
export type WebPushState = { instances: WebPushInstance[]; subscriptions: WebPushSubscriptionRecord[] };
export type WebPushCapability = "checking" | "insecure" | "unsupported" | "denied" | "default" | "granted";

export function webPushCapability(secure: boolean, supported: boolean, permission: NotificationPermission): WebPushCapability {
  if (!secure) return "insecure";
  if (!supported) return "unsupported";
  return permission;
}

export function webPushAvailable(state: WebPushState | null) {
  return !!state?.subscriptions.some(item => item.active && item.current_session && state.instances.some(instance => instance.id === item.instance_id));
}

export function webPushExpectedVersion(state: WebPushState, endpointHash: string) {
  return state.subscriptions.find(item => item.endpoint_hash === endpointHash)?.version ?? 0;
}

export function webPushKey(value: string): Uint8Array<ArrayBuffer> {
  if (!/^[A-Za-z0-9_-]+$/.test(value)) throw new Error("浏览器通知公钥无效，请联系管理员。");
  const encoded = value.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - value.length % 4) % 4);
  const bytes = Uint8Array.from(atob(encoded), character => character.charCodeAt(0));
  if (bytes.length !== 65 || bytes[0] !== 4) throw new Error("浏览器通知公钥无效，请联系管理员。");
  return bytes;
}

export function webPushKeyMatches(subscription: PushSubscription, publicKey: string) {
  const current = subscription.options.applicationServerKey;
  if (!current) return false;
  const expected = webPushKey(publicKey);
  const actual = new Uint8Array(current);
  return expected.length === actual.length && expected.every((value, index) => value === actual[index]);
}

export async function webPushEndpointHash(endpoint: string) {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(endpoint));
  return Array.from(new Uint8Array(digest), value => value.toString(16).padStart(2, "0")).join("");
}

export function serializedWebPushSubscription(subscription: PushSubscription) {
  const value = subscription.toJSON();
  if (!value.endpoint || !value.keys?.p256dh || !value.keys?.auth) throw new Error("浏览器未返回完整订阅，请重试或更换浏览器。");
  return { endpoint: value.endpoint, keys: { p256dh: value.keys.p256dh, auth: value.keys.auth } };
}

export type WebPushHistoryItem = { id: string | number; event_kind: string; name: string; created_at: string; variables: { key: string; label: string; value: string }[] };
export type WebPushHistoryPage = { items: WebPushHistoryItem[]; has_more: boolean; limit: number; offset: number };
