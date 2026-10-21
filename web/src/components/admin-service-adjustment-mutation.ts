"use client";

import { useEffect, useRef, useState } from "react";
import { api, ApiError, type User } from "@/lib/api";

export type ServiceManagement = {
  service_id: number; customer_id: number; service_version: number; owner_generation: number; status: string; billing_cycle: string;
  pricing_mode: "contract" | "fixed"; fixed_amount_cents: number | null; contract_base_cents: number; contract_discount_cents: number; contract_amount_cents: number; amount_cents: number;
  next_due_at: string | null; term_mode: "follow_supplier" | "managed"; supplier_next_due_at: string | null; supplier_observed_at: string | null;
};
export type AdjustmentKind = "pricing" | "correction" | "compensation";
export type AdjustmentQuote = {
  service_id: number; customer_id: number; service_version: number; mode: string; expires_at: string; quote_hash: string;
  fixed_amount_cents?: number | null; contract_base_cents?: number; contract_discount_cents?: number; contract_amount_cents?: number; amount_cents?: number;
  billing_cycle?: string; kind?: string; current_due_at: string | null; next_due_at?: string; supplier_next_due_at?: string | null; supplier_observed_at?: string | null;
};
export type AdjustmentRequest = { kind: AdjustmentKind; baseline: ServiceManagement; body: Record<string, unknown>; quote: AdjustmentQuote };
type Intent = AdjustmentRequest & { key: string; phase: "uncertain" | "rejected" };
type State = { ready: boolean; pending: Intent | null; busy: boolean; error: string; blocked: boolean; reviewed: ServiceManagement | null };
const writes = new Set<string>();
const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);
const positive = (value: unknown): value is number => typeof value === "number" && Number.isSafeInteger(value) && value > 0;
const cents = (value: unknown): value is number => typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
const hash = (value: unknown): value is string => typeof value === "string" && /^[0-9a-f]{64}$/.test(value);
const allowed = (user: User, permission: string) => user.role === "super_admin" || user.role === "admin" && user.permissions?.includes(permission);
export const serviceAdjustmentAuthority = (user: User) => JSON.stringify([user.id, user.role, [...(user.permissions ?? [])].sort(), user.customer_scope, user.customer_ownerless]);
export const adjustmentPermission = (kind: AdjustmentKind) => kind === "pricing" ? "services.pricing" : kind === "correction" ? "services.term.correct" : "services.term.compensate";
export function serviceTermMicros(value: unknown): bigint | null {
  if (typeof value !== "string") return null;
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,6}))?(Z|[+-]\d{2}:\d{2})$/.exec(value);
  if (!match || Number(match[1]) < 2000 || Number(match[1]) > 9999 || Number(match[4]) > 23 || Number(match[5]) > 59 || Number(match[6]) > 59) return null;
  const year = Number(match[1]), month = Number(match[2]), day = Number(match[3]);
  if (month < 1 || month > 12 || day < 1 || day > new Date(Date.UTC(year, month, 0)).getUTCDate()) return null;
  if (match[8] !== "Z" && (Number(match[8].slice(1, 3)) > 23 || Number(match[8].slice(4)) > 59)) return null;
  const milliseconds = Date.parse(`${value.slice(0, 19)}${match[8]}`);
  return Number.isFinite(milliseconds) ? BigInt(milliseconds) * BigInt(1000) + BigInt((match[7] ?? "").padEnd(6, "0")) : null;
}
const nullableTime = (value: unknown) => value === null || serviceTermMicros(value) !== null;
export function validServiceManagement(value: unknown): value is ServiceManagement {
  return object(value) && positive(value.service_id) && positive(value.customer_id) && positive(value.service_version) && Number.isSafeInteger(value.owner_generation) && Number(value.owner_generation) >= 0 && typeof value.status === "string" && typeof value.billing_cycle === "string" && ["contract", "fixed"].includes(String(value.pricing_mode)) && ["follow_supplier", "managed"].includes(String(value.term_mode)) && ["contract_base_cents", "contract_discount_cents", "contract_amount_cents", "amount_cents"].every(key => cents(value[key])) && Number(value.contract_base_cents) - Number(value.contract_discount_cents) === value.contract_amount_cents && (value.pricing_mode === "fixed" ? cents(value.fixed_amount_cents) && value.amount_cents === value.fixed_amount_cents : value.fixed_amount_cents === null && value.amount_cents === value.contract_amount_cents) && ["next_due_at", "supplier_next_due_at", "supplier_observed_at"].every(key => nullableTime(value[key]));
}
export function validAdjustmentQuote(value: unknown, request: Pick<AdjustmentRequest, "kind" | "baseline" | "body">): value is AdjustmentQuote {
  if (!object(value) || value.service_id !== request.baseline.service_id || value.customer_id !== request.baseline.customer_id || value.service_version !== request.body.expected_version || !hash(value.quote_hash) || serviceTermMicros(value.expires_at) === null || !nullableTime(value.current_due_at) || serviceTermMicros(value.current_due_at) !== serviceTermMicros(request.baseline.next_due_at)) return false;
  if (request.kind === "pricing") return value.mode === request.body.mode && value.fixed_amount_cents === (request.body.mode === "fixed" ? request.body.fixed_amount_cents : null) && ["contract_base_cents", "contract_discount_cents", "contract_amount_cents", "amount_cents"].every(key => cents(value[key])) && value.contract_base_cents === request.baseline.contract_base_cents && value.contract_discount_cents === request.baseline.contract_discount_cents && Number(value.contract_base_cents) - Number(value.contract_discount_cents) === value.contract_amount_cents && value.amount_cents === (value.mode === "fixed" ? value.fixed_amount_cents : value.contract_amount_cents) && value.billing_cycle === request.baseline.billing_cycle;
  return value.kind === request.kind && value.mode === request.baseline.term_mode && serviceTermMicros(value.next_due_at) === serviceTermMicros(request.body.next_due_at) && nullableTime(value.supplier_next_due_at) && nullableTime(value.supplier_observed_at);
}
function validRequest(value: unknown): value is AdjustmentRequest {
  if (!object(value) || !["pricing", "correction", "compensation"].includes(String(value.kind)) || !validServiceManagement(value.baseline) || !object(value.body)) return false;
  const input = value.body, kind = value.kind as AdjustmentKind;
  if (input.expected_version !== value.baseline.service_version || !hash(input.quote_hash) || typeof input.reason !== "string" || input.reason !== input.reason.trim() || !input.reason || input.reason.includes("\0") || Array.from(input.reason).length > (kind === "pricing" ? 240 : 500)) return false;
  if (kind === "pricing") {
    if (Object.keys(input).some(key => !["expected_version", "mode", "fixed_amount_cents", "quote_hash", "reason"].includes(key)) || !(input.mode === "fixed" && cents(input.fixed_amount_cents) || input.mode === "contract" && !Object.hasOwn(input, "fixed_amount_cents"))) return false;
  } else if (Object.keys(input).some(key => !["expected_version", "kind", "next_due_at", "quote_hash", "reason"].includes(key)) || input.kind !== kind || serviceTermMicros(input.next_due_at) === null) return false;
  return validAdjustmentQuote(value.quote, value as AdjustmentRequest) && value.quote.quote_hash === input.quote_hash;
}
function validStoredIntent(value: unknown): value is Intent {
  if (!object(value)) return false;
  const key = value.key, phase = value.phase, fields = Object.keys(value);
  return validRequest(value) && fields.every(field => ["kind", "baseline", "body", "quote", "key", "phase"].includes(field)) && typeof key === "string" && /^service-adjustment-[0-9a-f-]{36}$/i.test(key) && ["uncertain", "rejected"].includes(String(phase));
}
function endpoint(service: number, kind: AdjustmentKind, preview = false) { return `/admin/services/${service}/${kind === "pricing" ? `renewal-pricing${preview ? "/preview" : ""}` : preview ? "term-adjustment-quotes" : "term-adjustments"}`; }

export function useServiceAdjustmentManagement(actor: User, serviceId: number, customerId: number, ownerGeneration: number, serviceVersion: number) {
  const authority = serviceAdjustmentAuthority(actor);
  const [revision, setRevision] = useState(0), [state, setState] = useState<{ token: string; data: ServiceManagement | null; error: Error | null }>({ token: "", data: null, error: null });
  const token = `${authority}:${serviceId}:${customerId}:${ownerGeneration}:${serviceVersion}:${revision}`;
  useEffect(() => {
    let active = true;
    const identity = async () => {
      const user = await api<User>("/me", { portal: "admin" });
      if (!user || serviceAdjustmentAuthority(user) !== authority || !allowed(user, "services.view")) throw new Error("管理员身份或服务查看权限已变化。");
    };
    void (async () => {
      try {
        await identity(); if (!active) return;
        const value = await api<unknown>(`/admin/services/${serviceId}/management`, { portal: "admin" });
        if (!validServiceManagement(value) || value.service_id !== serviceId || value.customer_id !== customerId || value.owner_generation !== ownerGeneration || value.service_version !== serviceVersion) throw new Error("管理响应的服务、客户、归属代次或版本已变化，请刷新服务后核对。");
        await identity(); if (active) setState({ token, data: value, error: null });
      } catch (error) { if (active) setState({ token, data: null, error: error as Error }); }
    })();
    return () => { active = false; };
  }, [authority, serviceId, customerId, ownerGeneration, serviceVersion, token]);
  return { data: state.token === token ? state.data : null, error: state.token === token ? state.error : null, isLoading: state.token !== token, refresh: () => setRevision(value => value + 1) };
}

export function useServiceAdjustmentMutation(actor: User, serviceId: number, customerId: number, ownerGeneration: number) {
  const storageKey = `goidc:service-adjustment:admin:${actor.id}:${serviceId}`;
  const authority = serviceAdjustmentAuthority(actor);
  const [state, setState] = useState<State>({ ready: false, pending: null, busy: false, error: "", blocked: false, reviewed: null });
  const active = useRef(true), running = useRef(false), original = useRef<Intent | null>(null), reviewed = useRef<ServiceManagement | null>(null);
  useEffect(() => {
    active.current = true;
    const timer = window.setTimeout(() => {
      try {
        const raw = sessionStorage.getItem(storageKey), value: unknown = raw === null ? null : JSON.parse(raw);
        if (raw !== null && (!validStoredIntent(value) || value.baseline.service_id !== serviceId)) throw new Error("服务调整恢复资料不可用，已阻止新操作。请核对原请求，勿删除资料后重复提交。");
        original.current = value as Intent | null;
        if (active.current) setState({ ready: true, pending: value as Intent | null, busy: false, error: "", blocked: false, reviewed: null });
      } catch (error) { if (active.current) setState(previous => ({ ...previous, blocked: true, error: (error as Error).message })); }
    }, 0);
    return () => { active.current = false; window.clearTimeout(timer); };
  }, [storageKey, serviceId]);
  useEffect(() => {
    if (!state.pending) return;
    const guard = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    window.addEventListener("beforeunload", guard); return () => window.removeEventListener("beforeunload", guard);
  }, [state.pending]);
  function update(patch: Partial<State>) { if (active.current) setState(previous => ({ ...previous, ...patch })); }
  async function identity(kind?: AdjustmentKind) {
    const user = await api<User>("/me", { portal: "admin" });
    if (!user || serviceAdjustmentAuthority(user) !== authority || !allowed(user, "services.view") || kind && !allowed(user, adjustmentPermission(kind))) throw new Error("管理员身份、客户范围或服务权限已变化，原请求保留，请使用原账户核对。");
  }
  async function facts(kind?: AdjustmentKind, baseline?: ServiceManagement) {
    await identity(kind); if (!active.current) throw new Error("原页面已离开。");
    const value = await api<unknown>(`/admin/services/${serviceId}/management`, { portal: "admin" });
    if (!validServiceManagement(value) || value.service_id !== serviceId || value.customer_id !== customerId || value.owner_generation !== ownerGeneration || baseline && (value.customer_id !== baseline.customer_id || value.owner_generation !== baseline.owner_generation)) throw new Error("服务归属或归属代次已变化，已阻止旧意图，请先核对当前客户。");
    await identity(kind); return value;
  }
  async function preview(kind: AdjustmentKind, baseline: ServiceManagement, body: Record<string, unknown>) {
    if (!active.current || !state.ready || state.blocked || original.current || running.current) return null;
    running.current = true; update({ busy: true, error: "" });
    try {
      const latest = await facts(kind, baseline);
      if (latest.service_version !== baseline.service_version) throw new Error("服务版本已变化，请刷新当前管理状态并重新预览。");
      if (!active.current) return null;
      const value = await api<unknown>(endpoint(serviceId, kind, true), { method: "POST", body: JSON.stringify(body), portal: "admin" });
      if (!validAdjustmentQuote(value, { kind, baseline, body })) throw new Error("预览响应与当前服务、版本或输入不一致。");
      const after = await facts(kind, baseline);
      if (after.service_version !== baseline.service_version) throw new Error("预览期间服务版本已变化，请刷新并重新预览。");
      return active.current ? value : null;
    } catch (error) { update({ error: (error as Error).message }); return null; }
    finally { running.current = false; update({ busy: false }); }
  }
  async function run(input?: AdjustmentRequest) {
    if (!active.current || !state.ready || state.blocked || running.current || writes.has(storageKey) || original.current?.phase === "rejected") return null;
    running.current = true; writes.add(storageKey); update({ busy: true, error: "", reviewed: null }); reviewed.current = null;
    let attempt = original.current, businessFailure = false; const priorUnknown = !!attempt;
    try {
      if (!attempt) {
        if (!validRequest(input) || input.baseline.service_id !== serviceId || input.baseline.customer_id !== customerId) throw new Error("服务调整输入、摘要或归属不一致。");
        const before = await facts(input.kind, input.baseline);
        if (!active.current) return null;
        if (before.service_version !== input.baseline.service_version) throw new Error("服务版本已变化，请刷新当前管理状态，重新预览并人工确认。");
        if (Date.parse(input.quote.expires_at) <= Date.now()) throw new Error("预览已过期，请重新预览并人工确认。");
        if (sessionStorage.getItem(storageKey) !== null) throw new Error("存在尚未恢复的原请求，不能覆盖。");
        const next: Intent = { ...input, key: `service-adjustment-${crypto.randomUUID()}`, phase: "uncertain" };
        sessionStorage.setItem(storageKey, JSON.stringify(next)); original.current = next; attempt = next; update({ pending: next });
      }
      if (priorUnknown) await facts(attempt.kind, attempt.baseline);
      if (!active.current) return null;
      let value: unknown;
      try { value = await api<unknown>(endpoint(serviceId, attempt.kind), { method: attempt.kind === "pricing" ? "PUT" : "POST", body: JSON.stringify(attempt.body), portal: "admin", headers: { "Idempotency-Key": attempt.key } }); }
      catch (error) { businessFailure = true; throw error; }
      if (!object(value) || value.service_id !== serviceId || value.customer_id !== attempt.baseline.customer_id || typeof value.replayed !== "boolean") throw new Error("服务调整回执缺少原客户或服务确认。");
      if (attempt.kind === "pricing") {
        const unchanged = attempt.baseline.pricing_mode === attempt.body.mode && (attempt.body.mode === "contract" || attempt.baseline.fixed_amount_cents === attempt.body.fixed_amount_cents);
        const writtenVersion = value.version;
        if (!validAdjustmentQuote(value, attempt) || value.quote_hash !== attempt.body.quote_hash || writtenVersion !== Number(attempt.body.expected_version) + (unchanged ? 0 : 1)) throw new Error("价格回执金额、摘要或版本与原请求不一致。");
      } else if (value.service_version !== Number(attempt.body.expected_version) + 1 || value.kind !== attempt.kind || value.mode !== "managed" || serviceTermMicros(value.next_due_at) !== serviceTermMicros(attempt.body.next_due_at) || serviceTermMicros(value.before_due_at) !== serviceTermMicros(attempt.baseline.next_due_at) || !nullableTime(value.supplier_next_due_at) || !nullableTime(value.supplier_observed_at)) throw new Error("期限回执与原类型、期限或版本不一致。");
      await facts(attempt.kind, attempt.baseline);
      if (!active.current) return null;
      sessionStorage.removeItem(storageKey); original.current = null; update({ pending: null, error: "", reviewed: null }); return value;
    } catch (error) {
      const definite = !priorUnknown && businessFailure && error instanceof ApiError && (error.status === 400 || error.status === 422 || error.status === 409 && error.code !== "idempotency_conflict");
      if (attempt && definite) {
        const rejected: Intent = { ...attempt, phase: "rejected" };
        try { sessionStorage.setItem(storageKey, JSON.stringify(rejected)); original.current = rejected; attempt = rejected; } catch { update({ blocked: true }); }
      }
      update({ pending: attempt, error: `${(error as Error).message}${attempt ? definite ? "。原请求已明确拒绝；先读取当前事实，再重新预览确认。" : "。结果尚未确认，保留原body与请求键，勿重复创建意图。" : ""}` });
      return null;
    } finally { running.current = false; writes.delete(storageKey); update({ busy: false }); }
  }
  async function review() {
    const pending = original.current;
    if (!active.current || !pending || pending.phase !== "rejected" || running.current) return null;
    running.current = true; reviewed.current = null; update({ busy: true, reviewed: null, error: "" });
    try { const latest = await facts(pending.kind, pending.baseline); if (!active.current) return null; reviewed.current = latest; update({ reviewed: latest }); return latest; }
    catch (error) { update({ error: (error as Error).message }); return null; }
    finally { running.current = false; update({ busy: false }); }
  }
  async function endRejected() {
    const pending = original.current;
    if (!active.current || !pending || pending.phase !== "rejected" || !reviewed.current || running.current) return null;
    running.current = true; update({ busy: true, error: "" });
    try {
      const latest = await facts(pending.kind, pending.baseline); if (!active.current) return null;
      sessionStorage.removeItem(storageKey); original.current = null; reviewed.current = null; update({ pending: null, reviewed: null, error: "" }); return { latest, original: pending };
    } catch (error) { update({ error: (error as Error).message }); return null; }
    finally { running.current = false; update({ busy: false }); }
  }
  return { ...state, preview, run, review, endRejected, frozen: !state.ready || state.blocked || state.busy || !!state.pending };
}
