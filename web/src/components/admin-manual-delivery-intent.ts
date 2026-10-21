"use client";

import { useEffect, useRef, useState } from "react";
import { api, ApiError, type Service, type User } from "@/lib/api";
import { can } from "@/lib/admin-navigation";
import { orderRefundBlocksDelivery } from "./external-refund-order";

export type ManualInventoryChoice = { id: number; name: string; address: string; provider: string; region: string; upstream_id: string; version: number; status: string; service_id: number | null; binding_id: number | null; has_credentials: boolean };
export type ManualDeliveryBody = { job_id: number; note: string; manual_resource_id?: number; resource_expected_version?: number; expected_version?: number; upstream_id?: string; username?: string };
export type ManualDeliveryIntent = { serviceId: number; jobId: number; action: string; key: string; body: string | null; phase: "uncertain" | "rejected"; resource: Pick<ManualInventoryChoice, "id" | "name" | "address" | "version"> | null; serviceVersion: number };
export type ManualDeliveryReceipt = { service_id: number; job_id: number; action: string; status: "succeeded"; replayed: boolean };
const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);
export const positiveManualID = (value: unknown): value is number => typeof value === "number" && Number.isSafeInteger(value) && value > 0;
export const manualActorIdentity = (actor: User) => JSON.stringify([actor.id, actor.role, [...(actor.permissions ?? [])].sort(), actor.customer_scope, actor.customer_ownerless, actor.ticket_scope, actor.ticket_unassigned]);
const definiteCodes = new Set(["invalid_delivery", "invalid_delivery_state", "stale_service", "order_not_deliverable", "manual_resource_version_conflict", "manual_resource_version_required", "manual_resource_unavailable", "manual_resource_identity_required", "manual_resource_credentials_invalid", "manual_resource_encryption_unconfigured", "credentials_unavailable", "resource_required", "cancellation_job_blocked", "delivery_conflict"]);
const inflight = new Set<string>();
export function manualDeliveryBlocked(service: Service, action: string) {
  if (service.provision_mode !== "manual" || orderRefundBlocksDelivery(service.external_refund_order_hold) || ["cancelled", "terminated", "pending_payment", "import_review_required"].includes(service.status) || service.invoice_status === "refunded") return true;
  const states: Record<string, string[]> = { create: ["queued", "failed", "provisioning"], suspend: ["active"], resume: ["suspended"], terminate: ["active", "suspended", "failed", "queued", "provisioning"], sync: ["active", "suspended"], renew: ["active", "suspended"] };
  return !(states[action] ?? ["active"]).includes(service.status) || action === "create" && service.invoice_status !== "paid";
}
export function validManualInventory(value: unknown): value is ManualInventoryChoice {
  return object(value) && positiveManualID(value.id) && positiveManualID(value.version) && typeof value.name === "string" && !!value.name.trim() && typeof value.address === "string" && typeof value.provider === "string" && typeof value.region === "string" && typeof value.upstream_id === "string" && ["available", "disabled", "retired"].includes(String(value.status)) && (value.service_id === null || positiveManualID(value.service_id)) && (value.binding_id === null || positiveManualID(value.binding_id)) && typeof value.has_credentials === "boolean";
}
function validBody(value: unknown, intent: ManualDeliveryIntent) {
  if (!object(value) || value.job_id !== intent.jobId || typeof value.note !== "string" || value.note.trim() !== value.note || new TextEncoder().encode(value.note).length < 3 || new TextEncoder().encode(value.note).length > 2000 || value.note.includes("\0")) return false;
  if (intent.resource) return Object.keys(value).length === 5 && value.manual_resource_id === intent.resource.id && value.resource_expected_version === intent.resource.version && value.expected_version === intent.serviceVersion;
  return Object.keys(value).every(key => ["job_id", "upstream_id", "username", "note"].includes(key)) && typeof value.upstream_id === "string" && typeof value.username === "string" && new TextEncoder().encode(value.upstream_id).length <= 256 && new TextEncoder().encode(value.username).length <= 256 && (intent.action !== "create" || !!value.upstream_id.trim());
}
export function validManualDeliveryIntent(value: unknown, serviceId: number): value is ManualDeliveryIntent {
  if (!object(value) || value.serviceId !== serviceId || !positiveManualID(value.jobId) || !positiveManualID(value.serviceVersion) || typeof value.action !== "string" || !["create", "suspend", "resume", "terminate", "sync", "renew", "upgrade", "power_on", "power_off", "reboot", "force_off", "force_reboot", "reset_password", "reinstall", "rescue_system", "exit_rescue_system"].includes(value.action) || typeof value.key !== "string" || !/^manual-delivery-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value.key) || !["uncertain", "rejected"].includes(String(value.phase))) return false;
  if (value.resource !== null && (!object(value.resource) || !positiveManualID(value.resource.id) || !positiveManualID(value.resource.version) || typeof value.resource.name !== "string" || typeof value.resource.address !== "string" || value.action !== "create")) return false;
  if (value.body === null) return value.resource === null;
  if (typeof value.body !== "string" || value.body.length > 20000) return false;
  try { return validBody(JSON.parse(value.body), value as ManualDeliveryIntent); } catch { return false; }
}
export async function verifyManualDeliveryActor(actor: User, inventory: boolean) {
  const live = await api<User>("/me", { portal: "admin" });
  if (!live || manualActorIdentity(live) !== manualActorIdentity(actor) || !can(live, "services.manage") || inventory && (!can(live, "resources.view") || !can(live, "resources.manage"))) throw new Error("管理员身份、权限或范围已变化；原请求保留，请使用原身份核对。");
}
export function useManualDeliveryIntent(actor: User, service: Service, onSuccess: () => void) {
  const storageKey = `goidc:manual-delivery:${actor.id}:${service.id}`;
  const [ready, setReady] = useState(false), [pending, setPending] = useState<ManualDeliveryIntent | null>(null), [busy, setBusy] = useState(false), [error, setError] = useState(""), [storageError, setStorageError] = useState("");
  const [facts, setFacts] = useState<{ text: string; canEnd: boolean } | null>(null);
  const current = useRef<ManualDeliveryIntent | null>(null), active = useRef(true), running = useRef(false);
  useEffect(() => { active.current = true; return () => { active.current = false; }; }, []);
  useEffect(() => {
    const timer = window.setTimeout(() => {
      if (!active.current) return;
      try {
        const raw = sessionStorage.getItem(storageKey); const value: unknown = raw === null ? null : JSON.parse(raw);
        if (raw !== null && !validManualDeliveryIntent(value, service.id)) throw new Error("原交付记录损坏，已暂停新操作；请人工核对原任务，勿清空后重复交付。");
        current.current = value as ManualDeliveryIntent | null; setPending(current.current); setReady(true);
      } catch (failure) { setStorageError((failure as Error).message); }
    }, 0);
    return () => window.clearTimeout(timer);
  }, [storageKey, service.id]);
  useEffect(() => {
    if (!pending && !busy) return;
    const leave = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    window.addEventListener("beforeunload", leave); return () => window.removeEventListener("beforeunload", leave);
  }, [pending, busy]);
  function save(intent: ManualDeliveryIntent) {
    // A legacy manually entered account can be private: retain its exact body in
    // the live page only. Inventory requests contain no account/password fields.
    const persisted = intent.resource ? intent : { ...intent, body: null };
    sessionStorage.setItem(storageKey, JSON.stringify(persisted)); current.current = intent; if (active.current) setPending(intent);
  }
  async function execute(input?: { body: ManualDeliveryBody; action: string; resource: ManualInventoryChoice | null; serviceVersion: number }) {
    if (!ready || storageError || running.current || inflight.has(storageKey) || !active.current || input && current.current || !input && (!current.current?.body || current.current.phase === "rejected")) return;
    running.current = true; inflight.add(storageKey); setBusy(true); setError(""); setFacts(null);
    let intent = current.current; const priorUnknown = !!intent; let dispatched = false, businessFailure = false;
    try {
      await verifyManualDeliveryActor(actor, !!(intent?.resource || input?.resource)); if (!active.current) return;
      if (input) {
        const body = JSON.stringify(input.body);
        const next: ManualDeliveryIntent = { serviceId: service.id, jobId: input.body.job_id, action: input.action, key: `manual-delivery-${crypto.randomUUID()}`, body, phase: "uncertain", resource: input.resource ? { id: input.resource.id, name: input.resource.name, address: input.resource.address, version: input.resource.version } : null, serviceVersion: input.serviceVersion };
        if (!validManualDeliveryIntent(next, service.id)) throw new Error("交付请求无效，已停止发送。");
        // Re-read the actual service/task and inventory before dispatch. These
        // checks never rewrite the preview's versions or its fixed task.
        const latest = await api<Service>(`/admin/services/${service.id}`, { portal: "admin" }); if (!active.current) return;
        const job = latest.jobs?.find(item => item.id === next.jobId);
        if (latest.id !== service.id || latest.version !== next.serviceVersion || !job || job.action !== next.action || job.status !== "waiting_manual" || manualDeliveryBlocked(latest, next.action)) throw new Error("服务或任务已变化，请刷新后重新核对预览；尚未发送交付。");
        if (next.resource) {
          const inventory = await api<unknown>(`/admin/manual-resources/${next.resource.id}`, { portal: "admin" }); if (!active.current) return;
          if (!validManualInventory(inventory) || inventory.id !== next.resource.id || inventory.version !== next.resource.version || inventory.status !== "available" || inventory.binding_id !== null || inventory.service_id !== null) throw new Error("库存版本或占用已变化，请重新读取并确认；尚未发送交付。");
        }
        await verifyManualDeliveryActor(actor, !!next.resource); if (!active.current) return;
        if (sessionStorage.getItem(storageKey)) throw new Error("发现尚未恢复的原交付请求，请重新打开本区域核对。");
        save(next); intent = next;
      }
      if (!intent?.body) throw new Error("原请求仅有安全标记，请回仍打开的原页面核对；不会新建交付。");
      dispatched = true;
      const result = await api<unknown>(`/admin/services/${intent.serviceId}/delivery`, { method: "POST", body: intent.body, headers: { "Idempotency-Key": intent.key }, portal: "admin" }).catch(failure => { businessFailure = !priorUnknown && failure instanceof ApiError && [400, 409, 422].includes(failure.status) && definiteCodes.has(failure.code); throw failure; });
      if (!active.current) return;
      if (!object(result) || result.service_id !== intent.serviceId || result.job_id !== intent.jobId || result.action !== intent.action || result.status !== "succeeded" || typeof result.replayed !== "boolean") throw new Error("交付回执缺少原服务或任务确认，请保留原请求核对。");
      await verifyManualDeliveryActor(actor, !!intent.resource); if (!active.current) return;
      sessionStorage.removeItem(storageKey); current.current = null; setPending(null); onSuccess();
    } catch (failure) {
      if (!active.current) return;
      if (intent && dispatched) { const next = { ...intent, phase: businessFailure ? "rejected" as const : "uncertain" as const }; try { save(next); } catch { setStorageError("原交付核对状态无法保存，请保留页面和原请求核对。"); } }
      setError(`${(failure as Error).message}${dispatched && !businessFailure ? " 原交付结果待核验，不能更换任务、库存、版本或请求键。" : ""}`);
    } finally { running.current = false; inflight.delete(storageKey); if (active.current) setBusy(false); }
  }
  async function readLatest() {
    if (!current.current || running.current || !active.current) return;
    const intent = current.current; running.current = true; setBusy(true); setError(""); setFacts(null);
    try {
      await verifyManualDeliveryActor(actor, !!intent.resource); if (!active.current) return;
      const latest = await api<Service>(`/admin/services/${intent.serviceId}`, { portal: "admin" }); if (!active.current) return;
      if (latest.id !== intent.serviceId || !positiveManualID(latest.version)) throw new Error("服务响应无效，未完成核对。");
      const job = latest.jobs?.find(item => item.id === intent.jobId); if (!job || job.action !== intent.action) throw new Error("未读到原任务，请核对任务历史后再处理。");
      let text = `服务 #${latest.id} · ${latest.status} · 版本 ${latest.version}；原任务 #${intent.jobId} · ${job.status}。`;
      if (intent.resource) {
        const resource = await api<unknown>(`/admin/manual-resources/${intent.resource.id}`, { portal: "admin" }); if (!active.current) return;
        if (!validManualInventory(resource) || resource.id !== intent.resource.id) throw new Error("库存响应无效，未完成核对。");
        text += ` 库存 #${resource.id} · ${resource.status} · 版本 ${resource.version} · ${resource.service_id === null ? "未占用" : `占用服务 #${resource.service_id}`}。`;
      }
      await verifyManualDeliveryActor(actor, !!intent.resource); if (!active.current) return;
      setFacts({ text: `${text}原请求未作修改。`, canEnd: intent.phase === "rejected" || intent.body === null && job.status === "succeeded" });
    } catch (failure) { if (active.current) setError((failure as Error).message); } finally { running.current = false; if (active.current) setBusy(false); }
  }
  async function endReviewed() {
    const intent = current.current;
    if (!intent || !facts?.canEnd || running.current || !window.confirm("已核对原服务、任务与库存事实，确认结束此核对记录？这不会重新交付；下一次交付仍需重新读取并确认。")) return false;
    running.current = true; setBusy(true); setError("");
    try { await verifyManualDeliveryActor(actor, !!intent.resource); if (!active.current) return false; sessionStorage.removeItem(storageKey); current.current = null; setPending(null); setFacts(null); onSuccess(); return true; }
    catch (failure) { if (active.current) setError((failure as Error).message); return false; } finally { running.current = false; if (active.current) setBusy(false); }
  }
  return { ready, pending, busy, error, storageError, facts, execute, readLatest, endReviewed };
}
