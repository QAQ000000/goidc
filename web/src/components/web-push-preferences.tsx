"use client";

import { useCallback, useEffect, useState } from "react";
import { RefreshCw } from "lucide-react";
import { api, ApiError, currentAuthPortal, type AuthPortal } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { isNotificationVersionConflict } from "@/lib/notification-center";
import { serializedWebPushSubscription, webPushAvailable, webPushCapability, webPushEndpointHash, webPushExpectedVersion, webPushKey, webPushKeyMatches, type WebPushCapability, type WebPushState, type WebPushSubscriptionRecord } from "@/lib/web-push";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { WebPushHistory } from "./web-push-history";

const capabilityText: Record<WebPushCapability, string> = {
  checking: "正在检查浏览器通知支持情况。",
  insecure: "浏览器通知需要 HTTPS；本地开发可使用 localhost。当前连接无法开启通知。",
  unsupported: "当前浏览器或系统不支持后台推送，请使用支持 Web Push 的浏览器。",
  denied: "浏览器已阻止此站点通知。请在浏览器的网站设置中允许通知，再刷新。",
  default: "尚未授权通知；只有点击开启按钮后，浏览器才会询问是否允许。",
  granted: "浏览器已允许通知。订阅此设备并开启业务偏好后即可接收。",
};

export function WebPushPreferences({ onAvailabilityChange }: { onAvailabilityChange?: (available: boolean) => void }) {
  // Browser permission and subscription calls can outlive this page. Keep the
  // initiating portal instead of reading a later navigation's identity.
  const portal = currentAuthPortal();
  const resource = useResource<WebPushState>("/me/notification-web-push", false, portal);
  const [capability, setCapability] = useState<WebPushCapability>("checking");
  const [subscription, setSubscription] = useState<PushSubscription | null>(null);
  const [endpointHash, setEndpointHash] = useState("");
  const [instanceID, setInstanceID] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [resetRequired, setResetRequired] = useState(false);
  const [sessionExpired, setSessionExpired] = useState(false);
  const [iosInstall, setIOSInstall] = useState(false);
  const data = resource.data;
  const selected = data?.instances.find(item => item.id === instanceID) ?? data?.instances[0];
  const current = data?.subscriptions.find(item => item.endpoint_hash === endpointHash);
  const available = webPushAvailable(data);

  useEffect(() => { onAvailabilityChange?.(available); }, [available, onAvailabilityChange]);

  const inspectBrowser = useCallback(async () => {
    const supported = "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
    setCapability(webPushCapability(window.isSecureContext, supported, supported ? Notification.permission : "default"));
    setIOSInstall(/iPad|iPhone|iPod/.test(navigator.userAgent) && !window.matchMedia("(display-mode: standalone)").matches && !(navigator as Navigator & { standalone?: boolean }).standalone);
    if (!window.isSecureContext || !supported) return;
    const registration = await navigator.serviceWorker.getRegistration("/");
    const local = await registration?.pushManager.getSubscription() ?? null;
    setSubscription(local);
    setEndpointHash(local ? await webPushEndpointHash(local.endpoint) : "");
  }, []);

  useEffect(() => {
    const frame = window.requestAnimationFrame(() => {
      void inspectBrowser().catch(() => setMessage("无法读取浏览器订阅；请刷新后重试。"));
    });
    const expired = (event: Event) => {
      const expiredPortal = (event as CustomEvent<{ portal?: AuthPortal }>).detail?.portal;
      if (expiredPortal && expiredPortal !== portal) return;
      setSessionExpired(true); setMessage("登录会话已失效，请重新登录后管理浏览器通知。该会话绑定的订阅将停止投递。");
    };
    window.addEventListener("zjmfgo:unauthorized", expired);
    return () => { window.cancelAnimationFrame(frame); window.removeEventListener("zjmfgo:unauthorized", expired); };
  }, [inspectBrowser, portal]);

  function failure(error: unknown) {
    if (error instanceof ApiError && (error.status === 401 || error.status === 403 || error.code === "notification_session_expired")) {
      setSessionExpired(true); setMessage("当前会话无法管理通知，请重新登录并确认账户状态。");
    } else if (isNotificationVersionConflict(error)) {
      setMessage("订阅已在其他页面更新，请刷新订阅后核对并重试。"); resource.refresh();
    } else if (error instanceof ApiError && error.code === "web_push_endpoint_owned") {
      setResetRequired(true); setMessage("此浏览器订阅属于其他登录身份。请主动重置此浏览器订阅，再为当前账户开启；不会继承其他账户的通知。");
    } else setMessage(error instanceof Error ? error.message : "浏览器通知操作失败，请重试。");
  }

  async function enable() {
    if (!selected || !data || busy || sessionExpired || !["default", "granted"].includes(capability)) return;
    setBusy(true); setMessage("");
    // Request permission directly from the click, before any asynchronous work.
    try {
      const permissionRequest = Notification.permission === "granted" ? Promise.resolve("granted" as const) : Notification.requestPermission();
      const permission = await permissionRequest;
      setCapability(permission);
      if (permission !== "granted") { setMessage(permission === "denied" ? "通知授权已被拒绝，可在浏览器的网站设置中调整。" : "尚未允许通知，未创建订阅。"); return; }
      const registration = await navigator.serviceWorker.register("/notification-sw.js", { scope: "/", updateViaCache: "none" });
      const ready = registration.active ? registration : await navigator.serviceWorker.ready;
      let local = await ready.pushManager.getSubscription();
      if (local && !webPushKeyMatches(local, selected.public_key)) {
        setSubscription(local); setEndpointHash(await webPushEndpointHash(local.endpoint));
        setResetRequired(true); setMessage("此浏览器使用另一通知实例或旧公钥。请主动重置此浏览器订阅，再重新开启。"); return;
      }
      local ??= await ready.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: webPushKey(selected.public_key) });
      setSubscription(local);
      const hash = await webPushEndpointHash(local.endpoint); setEndpointHash(hash);
      await api("/me/notification-web-push/subscriptions", { portal, method: "POST", body: JSON.stringify({ instance_id: selected.id, subscription: serializedWebPushSubscription(local), expected_version: webPushExpectedVersion(data, hash) }) });
      setResetRequired(false); setMessage("此浏览器已订阅。请在业务通知渠道中主动开启浏览器通知偏好，并选择通知范围；订阅本身不会开启业务通知。");
      resource.refresh();
    } catch (error) {
      failure(error);
      // Keep the local subscription on an uncertain response so refresh can
      // reconcile a successful server write without creating a second device.
      resource.refresh();
    } finally { setBusy(false); }
  }

  async function remove(item: WebPushSubscriptionRecord) {
    if (busy || sessionExpired || !window.confirm("撤销此浏览器的通知订阅？此设备将停止接收后续通知。")) return;
    setBusy(true); setMessage("");
    try {
      await api(`/me/notification-web-push/subscriptions/${encodeURIComponent(String(item.id))}`, { portal, method: "DELETE", body: JSON.stringify({ expected_version: item.version }) });
      resource.refresh();
      if (item.endpoint_hash === endpointHash && subscription) {
        if (!(await subscription.unsubscribe())) throw new Error("服务器订阅已撤销；浏览器本地订阅未能移除，请重置或检查浏览器设置。");
        setSubscription(null); setEndpointHash("");
      }
      setResetRequired(false); setMessage("订阅已撤销；重新接收需要再次开启此浏览器通知。");
    } catch (error) { failure(error); }
    finally { setBusy(false); }
  }

  async function resetLocal() {
    if (busy || !subscription || !window.confirm("重置本地浏览器订阅？此浏览器原登录身份也将停止接收通知。此操作不会将其订阅转移给当前账户。")) return;
    if (current?.revocable && !sessionExpired) { await remove(current); return; }
    setBusy(true); setMessage("");
    try {
      if (!(await subscription.unsubscribe())) throw new Error("重置失败，请检查浏览器通知设置后重试。");
      setSubscription(null); setEndpointHash(""); setResetRequired(false);
      setMessage("本地订阅已重置，请点击开启，为当前账户创建新的订阅。"); resource.refresh();
    } catch (error) { failure(error); }
    finally { setBusy(false); }
  }

  return <div className="mt-5 space-y-3 border-t pt-5">
    <div className="flex flex-wrap items-center justify-between gap-3"><h3 className="text-sm font-medium">浏览器后台通知</h3><Button type="button" size="sm" variant="outline" disabled={busy} onClick={() => { resource.refresh(); void inspectBrowser().catch(failure); }}><RefreshCw className="size-4" />刷新浏览器订阅</Button></div>
    <p className="text-sm text-muted-foreground">页面关闭后，在浏览器和系统支持时仍可收到通知。通知只显示通用提示，详情需登录账户查看。每个浏览器单独订阅，退出登录或会话失效后停止该会话的推送；重新登录后需重新确认订阅。</p>
    <p className="text-sm" role="status">{capabilityText[capability]}</p>
    {iosInstall && <p className="text-sm text-muted-foreground">iPhone / iPad 需要 iOS 16.4 或更新系统：在 Safari 分享菜单中选择“添加到主屏幕”，再从主屏幕打开后开启通知。</p>}
    {resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !data ? <Loading /> : <>
      {!data.instances.length ? <p className="text-sm text-muted-foreground">管理员尚未启用可用的浏览器通知渠道，暂时无法订阅。</p> : <>
        <label className="grid gap-2 text-sm">浏览器通知渠道<select className="h-10 min-w-0 rounded-md border bg-background px-3" aria-label="浏览器通知渠道" disabled={busy || sessionExpired} value={selected?.id ?? ""} onChange={event => setInstanceID(event.target.value)}>{data.instances.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
        <Button type="button" variant="outline" disabled={busy || sessionExpired || !["default", "granted"].includes(capability) || resetRequired} onClick={() => enable()}>{busy ? "处理中…" : current?.active && current.current_session ? "重新确认此浏览器通知" : "开启此浏览器通知"}</Button>
      </>}
      {subscription && (!current?.active || !current.current_session || resetRequired) && <Button type="button" variant="outline" disabled={busy} onClick={() => resetLocal()}>重置此浏览器订阅</Button>}
      <div className="space-y-2">{!data.subscriptions.some(item => item.revocable) ? <p className="text-sm text-muted-foreground">当前账户没有浏览器订阅。</p> : data.subscriptions.filter(item => item.revocable).map((item, index) => <div key={item.id} className="flex flex-wrap items-center justify-between gap-3 rounded-md border p-3"><div className="text-sm"><p>{item.endpoint_hash === endpointHash ? "此浏览器" : `其他浏览器 ${index + 1}`} · {data.instances.find(instance => instance.id === item.instance_id)?.name ?? "通知渠道当前不可用"}</p><p className="mt-1 text-xs text-muted-foreground">{!item.active ? "订阅已失效或配置已变更；请撤销或在对应浏览器重新确认" : item.current_session ? "当前登录会话" : "其他登录会话；会话失效后停止投递"}</p></div><Button type="button" size="sm" variant="outline" disabled={busy || sessionExpired} onClick={() => remove(item)}>撤销订阅</Button></div>)}</div>
    </>}
    {message && <p role="status" className="break-words text-sm">{message}</p>}
    <WebPushHistory />
  </div>;
}
