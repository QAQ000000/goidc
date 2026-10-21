"use client";

import { useEffect, useRef, useState } from "react";
import { api, ApiError, type User } from "@/lib/api";

export type ManualResource = {
  id: number; name: string; resource_type: string; provider: string; region: string; address: string; upstream_id: string;
  cost_cents: number; currency: "CNY"; upstream_expires_at: string | null; status: "available" | "disabled" | "retired";
  notes: string; version: number; has_credentials: boolean; created_at: string; updated_at: string;
  binding_id: number | null; service_id: number | null; customer_id: number | null; bound_at: string | null;
};
export type ManualResourceKind = "create" | "update" | "release";
export type ManualResourceRequest = { kind: ManualResourceKind; target: number | null; body: Record<string, unknown>; serviceId?: number; bindingId?: number };
type Intent = Omit<ManualResourceRequest, "body"> & { key: string; body?: string; sensitive: boolean; phase: "uncertain" | "rejected" };
export type ManualResourceReceipt = { id: number; version: number; replayed: boolean; service_id?: number; binding_id?: number };
type Facts = { resource: ManualResource | null; total?: number };
type MutationState = { ready: boolean; pending: Intent | null; error: string; busy: boolean; facts: Facts | null; blocked: boolean };
const activeWrites = new Set<string>();
const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);
const positive = (value: unknown): value is number => typeof value === "number" && Number.isSafeInteger(value) && value > 0;
const fields = ["name", "resource_type", "provider", "region", "address", "upstream_id", "cost_cents", "upstream_expires_at", "status", "notes", "username", "password"];
export const manualResourceAuthority = (user: User) => JSON.stringify([user.id, user.role, [...(user.permissions ?? [])].sort(), user.customer_scope, user.customer_ownerless]);
const permission = (user: User, name: string) => user.role === "super_admin" || user.role === "admin" && user.permissions?.includes(name);
const fullScope = (user: User) => user.role === "super_admin" || user.customer_scope === "all";
export function validManualResource(value: unknown): value is ManualResource {
  if (!object(value) || !positive(value.id) || !positive(value.version) || value.currency !== "CNY" || typeof value.cost_cents !== "number" || !Number.isSafeInteger(value.cost_cents) || value.cost_cents < 0 || value.cost_cents > 9000000000000000 || !["available", "disabled", "retired"].includes(String(value.status)) || typeof value.has_credentials !== "boolean") return false;
  if (!["name", "resource_type", "provider", "region", "address", "upstream_id", "notes", "created_at", "updated_at"].every(key => typeof value[key] === "string") || !(value.upstream_expires_at === null || typeof value.upstream_expires_at === "string")) return false;
  return value.binding_id === null ? value.service_id === null && value.customer_id === null && value.bound_at === null : positive(value.binding_id) && positive(value.service_id) && positive(value.customer_id) && typeof value.bound_at === "string";
}
function validBody(intent: Pick<Intent, "kind" | "target" | "serviceId" | "bindingId">, body: unknown) {
  if (!object(body)) return false;
  if (intent.kind === "release") return positive(intent.target) && positive(intent.serviceId) && positive(intent.bindingId) && Object.keys(body).length === 2 && positive(body.expected_version) && typeof body.note === "string" && Array.from(body.note.trim()).length >= 3 && Array.from(body.note.trim()).length <= 2000;
  if (intent.kind === "create" ? intent.target !== null : !positive(intent.target) || !positive(body.expected_version)) return false;
  if (Object.keys(body).some(key => !fields.includes(key) && !(intent.kind === "update" && key === "expected_version"))) return false;
  if (intent.kind === "create" && (typeof body.name !== "string" || !body.name.trim())) return false;
  return Object.keys(body).some(key => fields.includes(key));
}
function validStored(value: unknown): value is Intent {
  if (!object(value) || Object.keys(value).some(key => !["kind", "target", "key", "body", "sensitive", "phase", "serviceId", "bindingId"].includes(key)) || !["create", "update", "release"].includes(String(value.kind)) || typeof value.key !== "string" || !/^manual-resource-[0-9a-f-]{36}$/i.test(value.key) || typeof value.sensitive !== "boolean" || !["uncertain", "rejected"].includes(String(value.phase))) return false;
  if (value.kind === "create" ? value.target !== null : !positive(value.target)) return false;
  if (value.kind === "release" && (!positive(value.serviceId) || !positive(value.bindingId) || value.sensitive)) return false;
  if (value.sensitive) return value.body === undefined;
  if (typeof value.body !== "string" || value.body.length > 100000) return false;
  try { const body = JSON.parse(value.body); return validBody(value as Intent, body) && !Object.hasOwn(body, "username") && !Object.hasOwn(body, "password"); } catch { return false; }
}
function endpoint(intent: Intent) { return `/admin/manual-resources${intent.target ? `/${intent.target}` : ""}${intent.kind === "release" ? "/release" : ""}`; }
function persisted(intent: Intent) { return JSON.stringify(intent.sensitive ? { ...intent, body: undefined } : intent); }

// Secret payloads live only in this mounted workspace. Their durable marker
// deliberately cannot reproduce the request after refresh or an authority remount.
export function useManualResourceMutation(actor: User) {
  const storageKey = `goidc:manual-resource-intent:admin:${actor.id}`;
  const authority = manualResourceAuthority(actor);
  const [state, setState] = useState<MutationState>({ ready: false, pending: null, error: "", busy: false, facts: null, blocked: false });
  const active = useRef(true), running = useRef(false), original = useRef<Intent | null>(null);
  const latestFacts = useRef<Facts | null>(null);
  useEffect(() => {
    active.current = true;
    const timer = window.setTimeout(() => {
      try {
        const raw = sessionStorage.getItem(storageKey), value: unknown = raw === null ? null : JSON.parse(raw);
        if (raw !== null && !validStored(value)) throw new Error("库存请求恢复标记损坏，已阻止新操作。请核对原操作，勿清空标记后重新提交。");
        original.current = value as Intent | null;
        if (active.current) setState({ ready: true, pending: value as Intent | null, error: "", busy: false, facts: null, blocked: false });
      } catch (error) { if (active.current) setState(previous => ({ ...previous, ready: false, blocked: true, error: (error as Error).message })); }
    }, 0);
    return () => { active.current = false; window.clearTimeout(timer); };
  }, [storageKey]);
  useEffect(() => {
    if (!state.pending) return;
    const guard = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    window.addEventListener("beforeunload", guard);
    return () => window.removeEventListener("beforeunload", guard);
  }, [state.pending]);
  function update(patch: Partial<MutationState>) { if (active.current) setState(previous => ({ ...previous, ...patch })); }
  async function identity(kind?: ManualResourceKind) {
    const user = await api<User>("/me", { portal: "admin" });
    if (!user || manualResourceAuthority(user) !== authority || !permission(user, "resources.view") || !permission(user, "resources.manage") || kind === "create" && !fullScope(user) || kind === "release" && !permission(user, "services.view")) throw new Error("管理员身份、客户范围或库存权限已变化；原请求保持不变，请使用原账户核对。");
  }
  async function run(input?: ManualResourceRequest): Promise<ManualResourceReceipt | null> {
    if (!active.current || !state.ready || state.blocked || running.current || activeWrites.has(storageKey) || original.current?.phase === "rejected" || original.current && !original.current.body) return null;
    running.current = true; activeWrites.add(storageKey); update({ busy: true, error: "", facts: null }); latestFacts.current = null;
    let attempt = original.current, businessFailure = false;
    const priorUnknown = !!attempt;
    try {
      if (!attempt) {
        if (!input || !validBody(input, input.body)) throw new Error("库存请求字段、对象或版本无效。");
        if (sessionStorage.getItem(storageKey) !== null) throw new Error("存在尚未恢复的原请求，已阻止覆盖。");
        const next: Intent = { ...input, body: JSON.stringify(input.body), key: `manual-resource-${crypto.randomUUID()}`, sensitive: Object.hasOwn(input.body, "username") || Object.hasOwn(input.body, "password"), phase: "uncertain" };
        sessionStorage.setItem(storageKey, persisted(next)); attempt = next; original.current = next; update({ pending: next });
      }
      await identity(attempt.kind);
      if (!active.current) return null;
      let value: unknown;
      try { value = await api<unknown>(endpoint(attempt), { method: attempt.kind === "update" ? "PATCH" : "POST", portal: "admin", body: attempt.body, headers: { "Idempotency-Key": attempt.key } }); }
      catch (error) { businessFailure = true; throw error; }
      const body = JSON.parse(attempt.body!) as Record<string, unknown>;
      if (!object(value) || !positive(value.id) || value.id !== (attempt.target ?? value.id) || value.version !== (attempt.kind === "create" ? 1 : Number(body.expected_version) + 1) || typeof value.replayed !== "boolean" || attempt.kind === "release" && (value.service_id !== attempt.serviceId || value.binding_id !== attempt.bindingId)) throw new Error("响应缺少与原库存请求一致的确认，仍需使用原请求核验。");
      await identity(attempt.kind);
      if (!active.current) return null;
      sessionStorage.removeItem(storageKey); original.current = null;
      update({ pending: null, error: "", facts: null }); return value as ManualResourceReceipt;
    } catch (error) {
      const definite = !priorUnknown && businessFailure && error instanceof ApiError && (error.status === 400 || error.status === 422 || error.status === 409 && error.code !== "idempotency_conflict");
      if (attempt && definite) {
        const rejected: Intent = { ...attempt, phase: "rejected" };
        try { sessionStorage.setItem(storageKey, persisted(rejected)); original.current = rejected; attempt = rejected; }
        catch { update({ blocked: true }); }
      }
      update({ pending: attempt, blocked: !attempt || state.blocked, error: `${(error as Error).message}${attempt ? definite ? "。原请求已拒绝，须读取当前事实并人工核对后重新编辑。" : "。结果待核验，保留原请求，勿换键重复操作。" : ""}` });
      return null;
    } finally { running.current = false; activeWrites.delete(storageKey); update({ busy: false }); }
  }
  async function review() {
    const attempt = original.current;
    if (!active.current || !attempt || attempt.phase !== "rejected" || running.current) return null;
    running.current = true; latestFacts.current = null; update({ busy: true, error: "", facts: null });
    try {
      await identity(attempt.kind);
      if (!active.current) return null;
      let facts: Facts;
      if (attempt.target) {
        const resource = await api<unknown>(`/admin/manual-resources/${attempt.target}`, { portal: "admin" });
        if (!validManualResource(resource) || resource.id !== attempt.target) throw new Error("当前库存响应无效，不能结束原请求。");
        facts = { resource };
      } else {
        const page = await api<{ total: number }>("/admin/manual-resources?limit=1&offset=0", { portal: "admin" });
        if (!page || !Number.isSafeInteger(page.total) || page.total < 0) throw new Error("当前库存列表响应无效。");
        facts = { resource: null, total: page.total };
      }
      await identity(attempt.kind);
      if (!active.current) return null;
      latestFacts.current = facts; update({ facts }); return facts;
    } catch (error) { update({ error: (error as Error).message }); return null; }
    finally { running.current = false; update({ busy: false }); }
  }
  async function endRejected() {
    const attempt = original.current, facts = latestFacts.current;
    if (!active.current || !attempt || attempt.phase !== "rejected" || !facts || running.current) return null;
    running.current = true; update({ busy: true, error: "" });
    try {
      await identity(attempt.kind);
      if (!active.current) return null;
      sessionStorage.removeItem(storageKey); original.current = null; latestFacts.current = null;
      update({ pending: null, facts: null, error: "", blocked: false }); return facts;
    } catch (error) { update({ error: (error as Error).message }); return null; }
    finally { running.current = false; update({ busy: false }); }
  }
  return { ...state, run, review, endRejected, frozen: !state.ready || state.blocked || state.busy || !!state.pending };
}
