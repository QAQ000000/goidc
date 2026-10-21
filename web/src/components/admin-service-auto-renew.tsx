"use client";

import { useEffect, useRef, useState } from "react";
import { api, type Service, type User } from "@/lib/api";
import { can } from "@/lib/admin-navigation";
import { useAdminIdentity, useAdminPermission } from "./workspace";
import { Button } from "./ui/button";

type Target = { serviceId: number; customerId: number; ownerGeneration: number };
type Intent = Target & { schema: 1; actor: string; enabled: boolean };
type Observation = { target: Target; enabled: boolean; sameOwner: boolean; sameAuthority: boolean };
const positive = (value: unknown): value is number => typeof value === "number" && Number.isSafeInteger(value) && value > 0;
const generation = (value: unknown): value is number => typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);
const identity = (actor: User) => JSON.stringify([actor.id, actor.role, [...(actor.permissions ?? [])].sort(), actor.customer_scope, actor.customer_ownerless, actor.ticket_scope, actor.ticket_unassigned]);
const activeWrites = new Set<string>();
function target(service: Service): Target | null {
  const owner = (service as Service & { owner_generation?: unknown }).owner_generation;
  return positive(service.id) && positive(service.user_id) && generation(owner) ? { serviceId: service.id, customerId: service.user_id, ownerGeneration: owner } : null;
}
const sameTarget = (left: Target, right: Target) => left.serviceId === right.serviceId && left.customerId === right.customerId && left.ownerGeneration === right.ownerGeneration;
function validIntent(value: unknown, serviceId: number): value is Intent {
  return object(value) && Object.keys(value).length === 6 && value.schema === 1 && value.serviceId === serviceId && positive(value.customerId) && generation(value.ownerGeneration) && typeof value.actor === "string" && value.actor.length > 0 && value.actor.length <= 8192 && typeof value.enabled === "boolean";
}
const eligible = (service: Service) => service.billing_cycle !== "once" && ["active", "suspended"].includes(service.status);
async function verifyActor(actor: User) {
  const live = await api<User>("/me", { portal: "admin" });
  if (!live || identity(live) !== identity(actor) || !can(live, "services.manage")) throw new Error("管理员身份、权限或客户范围已变化，已停止操作；请刷新并重新核对。");
}

export function AdminServiceAutoRenew({ service, refresh }: { service: Service; refresh: () => void }) {
  const actor = useAdminIdentity(), manage = useAdminPermission("services.manage");
  if (!actor || !manage) return null;
  const owner = target(service);
  if (!owner || typeof service.auto_renew !== "boolean") return <p role="alert" className="text-sm text-destructive">自动续费归属或状态数据不完整，请刷新服务详情。</p>;
  return <AutoRenewForActor key={`${identity(actor)}:${JSON.stringify(owner)}`} service={service} actor={actor} owner={owner} refresh={refresh} />;
}

function AutoRenewForActor({ service, actor, owner, refresh }: { service: Service; actor: User; owner: Target; refresh: () => void }) {
  const storageKey = `goidc:admin-auto-renew:${actor.id}:${service.id}`;
  const [ready, setReady] = useState(false), [pending, setPending] = useState<Intent | null>(null), [desired, setDesired] = useState<boolean | null>(null);
  const [busy, setBusy] = useState(false), [error, setError] = useState(""), [storageError, setStorageError] = useState("");
  const [observation, setObservation] = useState<Observation | null>(null), [notice, setNotice] = useState("");
  const active = useRef(true), running = useRef(false), current = useRef<Intent | null>(null);
  useEffect(() => { active.current = true; return () => { active.current = false; }; }, []);
  useEffect(() => {
    const timer = window.setTimeout(() => {
      if (!active.current) return;
      try {
        const raw = sessionStorage.getItem(storageKey); let stored: unknown = null;
        try { stored = raw === null ? null : JSON.parse(raw); } catch { throw new Error("原自动续费核对记录损坏，已暂停新操作；请保留记录并人工核对。"); }
        if (raw !== null && !validIntent(stored, service.id)) throw new Error("原自动续费核对记录损坏，已暂停新操作；请保留记录并人工核对。");
        current.current = stored as Intent | null; setPending(current.current); setReady(true);
      } catch (failure) { setStorageError((failure as Error).message); }
    }, 0);
    return () => window.clearTimeout(timer);
  }, [storageKey, service.id]);
  useEffect(() => {
    if (!pending && !busy && desired === null) return;
    const leave = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    window.addEventListener("beforeunload", leave); return () => window.removeEventListener("beforeunload", leave);
  }, [pending, busy, desired]);
  async function readTarget() {
    const latest = await api<Service>(`/admin/services/${owner.serviceId}`, { portal: "admin" });
    const latestOwner = target(latest);
    if (!latestOwner || latestOwner.serviceId !== owner.serviceId || typeof latest.auto_renew !== "boolean") throw new Error("服务归属或自动续费响应无效，尚未完成核对。");
    return { latest, latestOwner };
  }
  async function submit() {
    if (!ready || storageError || running.current || activeWrites.has(storageKey) || current.current || desired === null || !eligible(service) || !active.current) return;
    const intent: Intent = { schema: 1, ...owner, actor: identity(actor), enabled: desired };
    running.current = true; activeWrites.add(storageKey); setBusy(true); setError(""); setNotice(""); let dispatched = false;
    try {
      await verifyActor(actor); if (!active.current) return;
      const { latest, latestOwner } = await readTarget(); if (!active.current) return;
      if (!sameTarget(owner, latestOwner)) throw new Error("服务客户或归属代次已变化，旧操作已停止；请刷新后重新确认。");
      if (!eligible(latest)) throw new Error("当前服务状态或账期不支持自动续费，请刷新核对。");
      await verifyActor(actor); if (!active.current) return;
      if (latest.auto_renew === intent.enabled) { setDesired(null); setNotice("重新读取的当前状态已与目标一致，本次没有发送修改。"); refresh(); return; }
      if (sessionStorage.getItem(storageKey)) throw new Error("存在尚未恢复的原操作，请重新打开本区域核对。");
      sessionStorage.setItem(storageKey, JSON.stringify(intent)); current.current = intent; setPending(intent); dispatched = true;
      const result = await api<unknown>(`/admin/services/${intent.serviceId}/auto-renew`, { method: "PATCH", body: JSON.stringify({ enabled: intent.enabled, expected_customer_id: intent.customerId, expected_owner_generation: intent.ownerGeneration }), portal: "admin" });
      if (!active.current) return;
      if (!object(result) || result.service_id !== intent.serviceId || result.auto_renew !== intent.enabled) throw new Error("自动续费响应未确认原服务及目标状态。");
      await verifyActor(actor); if (!active.current) return;
      const confirmed = await readTarget(); if (!active.current) return;
      if (!sameTarget(intent, confirmed.latestOwner)) throw new Error("修改后服务归属已变化，旧客户意图已停止，请读取现状核对。");
      if (confirmed.latest.auto_renew !== intent.enabled) throw new Error("当前自动续费状态与原目标不同，请读取现状核对。");
      await verifyActor(actor); if (!active.current) return;
      sessionStorage.removeItem(storageKey); current.current = null; setPending(null); setDesired(null); setNotice("已重新读取并确认当前自动续费状态；本次未立即扣款。"); refresh();
    } catch (failure) {
      if (active.current) setError(`${(failure as Error).message}${dispatched ? " 原操作结果待核对，不会自动重复提交。" : ""}`);
    } finally { running.current = false; activeWrites.delete(storageKey); if (active.current) setBusy(false); }
  }
  async function inspect() {
    const intent = current.current;
    if (!intent || running.current || !active.current) return;
    running.current = true; setBusy(true); setError(""); setObservation(null);
    try {
      await verifyActor(actor); if (!active.current) return;
      const { latest, latestOwner } = await readTarget(); if (!active.current) return;
      await verifyActor(actor); if (!active.current) return;
      setObservation({ target: latestOwner, enabled: latest.auto_renew, sameOwner: sameTarget(intent, latestOwner), sameAuthority: intent.actor === identity(actor) });
      refresh();
    } catch (failure) { if (active.current) setError((failure as Error).message); } finally { running.current = false; if (active.current) setBusy(false); }
  }
  async function endReview() {
    const intent = current.current;
    if (!intent || !observation || running.current || !window.confirm("已核对当前服务归属和自动续费状态，结束原操作核对？这不会重发修改；如仍需更改，须重新确认新操作。")) return;
    running.current = true; setBusy(true); setError("");
    try {
      await verifyActor(actor); if (!active.current) return;
      const { latest, latestOwner } = await readTarget(); if (!active.current) return;
      if (!sameTarget(latestOwner, observation.target) || latest.auto_renew !== observation.enabled) throw new Error("刚核对的归属或状态已变化，请重新读取现状。");
      await verifyActor(actor); if (!active.current) return;
      sessionStorage.removeItem(storageKey); current.current = null; setPending(null); setDesired(null); setObservation(null); setNotice("原操作核对已结束，未重发修改；如有需要，请重新选择并确认。"); refresh();
    } catch (failure) { if (active.current) setError((failure as Error).message); } finally { running.current = false; if (active.current) setBusy(false); }
  }
  const frozen = !ready || !!storageError || busy || !!pending;
  return <section className="space-y-4 rounded border p-4" aria-label="自动续费设置">
    <div><h3 className="text-sm font-medium">自动续费</h3><p className="mt-1 text-xs text-muted-foreground">当前：{service.auto_renew ? "开启" : "关闭"}。仅修改后续自动续费意图，不立即创建续费账单或扣款；实际执行仍受余额、服务和供应商状态约束。</p></div>
    {service.auto_renew_error && <p role="status" className="text-sm text-amber-700">最近自动续费提示：{service.auto_renew_error}</p>}
    {storageError && <p role="alert" className="text-sm text-destructive">{storageError}</p>}{error && <p role="alert" className="break-words text-sm text-destructive">{error}</p>}{notice && <p role="status" className="text-sm">{notice}</p>}
    {pending ? <div aria-label="原自动续费操作核对" className="space-y-3 text-sm"><p>原操作结果待核对：服务 #{pending.serviceId} · 客户 #{pending.customerId} · 归属代次 {pending.ownerGeneration} · 目标{pending.enabled ? "开启" : "关闭"}。</p>{(!sameTarget(pending, owner) || pending.actor !== identity(actor)) && <p role="alert">管理员权限范围或服务归属已变化，旧操作已停止；仅可核对现状后结束记录，不会对新归属重发原意图。</p>}<Button type="button" variant="outline" disabled={busy} onClick={() => void inspect()}>读取当前自动续费状态</Button>{observation && <><p>已读取：客户 #{observation.target.customerId} · 归属代次 {observation.target.ownerGeneration} · 当前{observation.enabled ? "开启" : "关闭"}。{!observation.sameOwner || !observation.sameAuthority ? "原客户或权限范围的意图已停止。" : observation.enabled === pending.enabled ? "当前值与原目标一致；这只说明读取时的状态，不证明原请求执行次数。" : "当前值与原目标不同，不会自动重发；需要时结束核对后重新确认。"}</p><Button type="button" variant="outline" disabled={busy} onClick={() => void endReview()}>已核对，结束原操作</Button></>}</div> : <>
      {!eligible(service) && <p className="text-sm text-muted-foreground">仅已启用或已暂停的非一次性服务可设置自动续费。</p>}
      {desired === null ? <Button type="button" variant="outline" disabled={frozen || !eligible(service)} onClick={() => { setDesired(!service.auto_renew); setError(""); setNotice(""); }}>{service.auto_renew ? "关闭自动续费" : "开启自动续费"}</Button> : <div aria-label="自动续费修改确认" className="space-y-3 text-sm"><p>确认将服务 #{owner.serviceId}（客户 #{owner.customerId} · 归属代次 {owner.ownerGeneration}）自动续费设为“{desired ? "开启" : "关闭"}”？本次不立即扣款。</p><div className="flex flex-wrap gap-2"><Button type="button" disabled={frozen || !eligible(service)} onClick={() => void submit()}>确认{desired ? "开启" : "关闭"}自动续费</Button><Button type="button" variant="outline" disabled={busy} onClick={() => setDesired(null)}>取消修改</Button></div></div>}
    </>}
  </section>;
}
