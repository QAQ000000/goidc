"use client";

import { useEffect, useRef, useState } from "react";
import { api, ApiError, type Service, type User } from "@/lib/api";
import { can } from "@/lib/admin-navigation";
import { serviceAdjustmentAuthority, serviceTermMicros, validServiceManagement, type ServiceManagement } from "./admin-service-adjustment-mutation";

export type RenewalQuote = {
  customer_id: number; service_id: number; service_version: number; billing_cycle: string;
  base_cents: number; discount_cents: number; amount_cents: number; current_due_at: string | null;
  pending_invoice_id: number; can_create: boolean; expires_at: string; quote_hash: string;
};
export type RenewalInvoice = {
  id: number; user_id: number; invoice_no: string; version: number; status: string; amount_cents: number;
  invoice_kind: string; import_review_required: boolean; payment_pending: boolean;
  items: { service_id: number; item_type: string; amount_cents: number }[];
};
type Kind = "normal" | "fixed" | "pay";
type Request = { kind: Kind; body: Record<string, unknown>; quote?: RenewalQuote; invoice?: RenewalInvoice };
type Intent = Request & { key: string; authority: string; customer: number; service: number; owner: number; phase: "uncertain" | "rejected" };
export type RenewalReceipt = { invoice_id: number; invoice_no: string; customer_id: number; service_id: number; amount_cents: number; status: string; replayed: boolean; renewal_status?: string };
type Facts = { management: ServiceManagement; invoice: RenewalInvoice | null };
type State = { ready: boolean; busy: boolean; blocked: boolean; pending: Intent | null; error: string; facts: Facts | null; receipt: RenewalReceipt | null };
const writes = new Set<string>();
const object = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const integer = (v: unknown, min = 0): v is number => typeof v === "number" && Number.isSafeInteger(v) && v >= min;
const hash = (v: unknown) => typeof v === "string" && /^[a-f0-9]{64}$/.test(v);
const text = (v: unknown, max: number) => typeof v === "string" && v === v.trim() && !!v && !v.includes("\0") && Array.from(v).length <= max;
const nullableTime = (v: unknown) => v === null || serviceTermMicros(v) !== null;
const permission = (kind: Kind) => kind === "pay" ? "services.renew.pay_balance" : "services.renew";
export function validRenewalQuote(value: unknown, service: Service, expected: number, fixed?: number): value is RenewalQuote {
  if (!object(value) || value.service_id !== service.id || value.customer_id !== service.user_id || value.service_version !== expected || value.billing_cycle !== service.billing_cycle || ![value.base_cents, value.discount_cents, value.amount_cents, value.pending_invoice_id].every(v => integer(v)) || Number(value.base_cents) - Number(value.discount_cents) !== value.amount_cents || !nullableTime(value.current_due_at) || serviceTermMicros(value.expires_at) === null || typeof value.can_create !== "boolean") return false;
  return (value.can_create ? value.pending_invoice_id === 0 && hash(value.quote_hash) : integer(value.pending_invoice_id, 1) && value.quote_hash === "") && (fixed === undefined || value.base_cents === fixed && value.amount_cents === fixed && value.discount_cents === 0);
}
export function validRenewalInvoice(value: unknown, service: number, customer: number, id: number): value is RenewalInvoice {
  return object(value) && value.id === id && value.user_id === customer && integer(value.version, 1) && text(value.invoice_no, 128) && integer(value.amount_cents) && typeof value.status === "string" && value.invoice_kind === "automatic" && typeof value.import_review_required === "boolean" && typeof value.payment_pending === "boolean" && Array.isArray(value.items) && value.items.length === 1 && object(value.items[0]) && value.items[0].service_id === service && value.items[0].item_type === "renew" && value.items[0].amount_cents === value.amount_cents;
}
function validRequest(v: unknown, service: Service): v is Request & Record<string, unknown> {
  if (!object(v) || !["normal", "fixed", "pay"].includes(String(v.kind)) || !object(v.body) || !integer(v.body.expected_version, 1) || !text(v.body.reason, v.kind === "pay" ? 500 : 240)) return false;
  const keys = v.kind === "normal" ? ["expected_version", "quote_hash", "reason"] : v.kind === "fixed" ? ["expected_version", "quote_hash", "reason", "amount_cents"] : ["expected_version", "reason", "amount_cents"];
  if (Object.keys(v.body).length !== keys.length || Object.keys(v.body).some(k => !keys.includes(k))) return false;
  if (v.kind === "pay") return object(v.invoice) && integer(v.invoice.id, 1) && validRenewalInvoice(v.invoice, service.id, service.user_id, v.invoice.id) && v.invoice.status === "unpaid" && !v.invoice.import_review_required && !v.invoice.payment_pending && v.body.amount_cents === v.invoice.amount_cents && v.body.expected_version === v.invoice.version;
  return (v.kind !== "fixed" || integer(v.body.amount_cents)) && validRenewalQuote(v.quote, service, v.body.expected_version, v.kind === "fixed" ? Number(v.body.amount_cents) : undefined) && v.quote.can_create && v.quote.quote_hash === v.body.quote_hash;
}
function endpoint(service: number, request: Request) {
  return `/admin/services/${service}/${request.kind === "pay" ? `renewals/${request.invoice!.id}/pay` : request.kind === "fixed" ? "fixed-renewals" : "renewals"}`;
}

// One persisted intent per administrator/service spans both creation modes and payment.
// An ambiguous attempt can only replay its original body/key, even after expiry.
export function useServiceRenewalMutation(actor: User, service: Service, owner: number) {
  const authority = serviceAdjustmentAuthority(actor), storageKey = `goidc:service-renewal:admin:${actor.id}:${service.id}`;
  const active = useRef(true), running = useRef(false), original = useRef<Intent | null>(null);
  const [state, setState] = useState<State>({ ready: false, busy: false, blocked: false, pending: null, error: "", facts: null, receipt: null });
  const update = (patch: Partial<State>) => { if (active.current) setState(previous => ({ ...previous, ...patch })); };
  useEffect(() => {
    active.current = true;
    const timer = window.setTimeout(() => {
      try {
        const raw = sessionStorage.getItem(storageKey), value: unknown = raw === null ? null : JSON.parse(raw);
        if (value !== null && (!object(value) || !validRequest(value, service) || !/^service-renewal-[a-f0-9-]{36}$/i.test(String(value.key)) || value.authority !== authority || value.customer !== service.user_id || value.service !== service.id || value.owner !== owner || !["uncertain", "rejected"].includes(String(value.phase)))) throw new Error("原续费请求的身份、归属或恢复资料不匹配，已阻止新操作。请使用原账户核对，勿清除资料后重试。");
        original.current = value as Intent | null;
        update({ ready: true, pending: original.current });
      } catch (error) { update({ blocked: true, error: (error as Error).message }); }
    }, 0);
    return () => { active.current = false; window.clearTimeout(timer); };
    // The caller remounts on identity, scope and ownership changes, but not service version.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [storageKey, authority, owner, service.id, service.user_id]);
  useEffect(() => {
    if (!state.pending) return;
    const guard = (e: BeforeUnloadEvent) => { e.preventDefault(); e.returnValue = ""; };
    window.addEventListener("beforeunload", guard); return () => window.removeEventListener("beforeunload", guard);
  }, [state.pending]);
  async function identity(kind?: Kind) {
    const live = await api<User>("/me", { portal: "admin" });
    if (!live || serviceAdjustmentAuthority(live) !== authority || !can(live, "services.view") || !can(live, "invoices.view") || kind && (!can(live, permission(kind)) || kind === "fixed" && !can(live, "services.renew.override"))) throw new Error("管理员身份、权限或客户范围已变化，请重新登录原账户核对。");
    if (!active.current) throw new Error("原页面已离开。");
  }
  async function facts(kind?: Kind, invoiceId?: number): Promise<Facts> {
    await identity(kind);
    const management = await api<unknown>(`/admin/services/${service.id}/management`, { portal: "admin" });
    if (!validServiceManagement(management) || management.service_id !== service.id || management.customer_id !== service.user_id || management.owner_generation !== owner) throw new Error("服务归属或归属代次已变化，请核对当前客户。");
    let invoice: RenewalInvoice | null = null;
    if (invoiceId !== undefined) {
      const data = await api<unknown>(`/admin/invoices/${invoiceId}`, { portal: "admin" });
      if (!validRenewalInvoice(data, service.id, service.user_id, invoiceId)) throw new Error("账单并非当前客户与服务的单项自动续费单，已阻止付款。");
      invoice = { id: data.id, user_id: data.user_id, invoice_no: data.invoice_no, version: data.version, status: data.status, amount_cents: data.amount_cents, invoice_kind: data.invoice_kind, import_review_required: data.import_review_required, payment_pending: data.payment_pending, items: data.items.map(item => ({ service_id: item.service_id, item_type: item.item_type, amount_cents: item.amount_cents })) };
    }
    await identity(kind);
    return { management, invoice };
  }
  async function readInvoice(id: number) {
    if (!active.current || !state.ready || state.blocked || running.current || !integer(id, 1)) return null;
    running.current = true; update({ busy: true, error: "", facts: null });
    try { const data = await facts(undefined, id); update({ facts: data }); return data.invoice; }
    catch (error) { update({ error: (error as Error).message }); return null; }
    finally { running.current = false; update({ busy: false }); }
  }
  async function preview(fixed?: number) {
    if (!active.current || !state.ready || state.blocked || original.current || running.current) return null;
    running.current = true; update({ busy: true, error: "", receipt: null });
    const kind = fixed === undefined ? "normal" : "fixed";
    try {
      if (fixed !== undefined && !integer(fixed)) throw new Error("金额必须是非负整数分。");
      const before = await facts(kind);
      const body = { expected_version: before.management.service_version, ...(fixed === undefined ? {} : { amount_cents: fixed }) };
      const value = await api<unknown>(`/admin/services/${service.id}/${kind === "fixed" ? "fixed-renewal-quotes" : "renewal-quotes"}`, { method: "POST", portal: "admin", body: JSON.stringify(body) });
      if (!validRenewalQuote(value, service, body.expected_version, fixed) || serviceTermMicros(value.current_due_at) !== serviceTermMicros(before.management.next_due_at)) throw new Error("续费报价的服务、金额、期限或版本不一致。");
      const after = await facts(kind);
      if (after.management.service_version !== before.management.service_version) throw new Error("报价期间服务版本已变化，请重新预览。");
      return active.current ? value : null;
    } catch (error) { update({ error: (error as Error).message }); return null; }
    finally { running.current = false; update({ busy: false }); }
  }
  async function run(input?: Request) {
    if (!active.current || !state.ready || state.blocked || running.current || writes.has(storageKey) || original.current?.phase === "rejected") return null;
    running.current = true; writes.add(storageKey); update({ busy: true, error: "", receipt: null, facts: null });
    let attempt = original.current, businessFailure = false; const retry = !!attempt;
    try {
      if (!attempt) {
        if (!validRequest(input, service)) throw new Error("续费输入、报价或账单不一致。");
        const before = await facts(input.kind, input.invoice?.id);
        if (input.kind === "pay" ? before.invoice?.version !== input.body.expected_version || before.invoice?.amount_cents !== input.body.amount_cents || before.invoice?.status !== "unpaid" || before.invoice?.payment_pending || before.invoice?.import_review_required : before.management.service_version !== input.body.expected_version || Date.parse(input.quote!.expires_at) <= Date.now()) throw new Error("报价已过期或当前服务/账单已变化，请重新读取并确认。");
        if (sessionStorage.getItem(storageKey) !== null) throw new Error("存在未恢复请求，不能覆盖。");
        const next: Intent = { ...input, key: `service-renewal-${crypto.randomUUID()}`, authority, customer: service.user_id, service: service.id, owner, phase: "uncertain" };
        sessionStorage.setItem(storageKey, JSON.stringify(next)); original.current = next; attempt = next; update({ pending: next });
      } else await facts(attempt.kind); // Replay must not depend on current invoice status/version or expired quote.
      if (!active.current) return null;
      let value: unknown;
      try { value = await api<unknown>(endpoint(service.id, attempt), { method: "POST", portal: "admin", headers: { "Idempotency-Key": attempt.key }, body: JSON.stringify(attempt.body) }); }
      catch (error) { businessFailure = true; throw error; }
      const amount = attempt.kind === "pay" ? attempt.body.amount_cents : attempt.quote!.amount_cents;
      if (!object(value) || value.customer_id !== service.user_id || value.service_id !== service.id || !integer(value.invoice_id, 1) || !text(value.invoice_no, 128) || value.amount_cents !== amount || typeof value.replayed !== "boolean" || (attempt.kind === "pay" ? value.invoice_id !== attempt.invoice!.id || value.invoice_no !== attempt.invoice!.invoice_no || value.status !== "paid" || !["pending", "completed"].includes(String(value.renewal_status)) || !integer(value.balance_cents) : value.status !== "unpaid" || !integer(value.version, 1))) throw new Error("续费回执与原客户、服务、账单或金额不一致，请恢复原请求核验。");
      const currentFacts = await facts(attempt.kind, value.invoice_id);
      if (!active.current) return null;
      if (currentFacts.invoice?.invoice_no !== value.invoice_no) throw new Error("当前账单与原回执编号不一致。");
      sessionStorage.removeItem(storageKey); original.current = null;
      const receipt = value as RenewalReceipt;
      update({ pending: null, receipt, facts: currentFacts }); return { receipt, facts: currentFacts };
    } catch (error) {
      const rejected = !retry && businessFailure && error instanceof ApiError && ([400, 422].includes(error.status) || error.status === 409 && error.code !== "idempotency_conflict");
      if (attempt && rejected) {
        attempt = { ...attempt, phase: "rejected" };
        try { sessionStorage.setItem(storageKey, JSON.stringify(attempt)); original.current = attempt; } catch { update({ blocked: true }); }
      }
      update({ pending: attempt, error: `${(error as Error).message}${attempt ? rejected ? "。请求明确拒绝；读取当前事实后才能重新确认。" : "。结果尚未确认，仅可恢复原请求，勿重复建单或扣款。" : ""}` }); return null;
    } finally { running.current = false; writes.delete(storageKey); update({ busy: false }); }
  }
  async function finishRejected() {
    const pending = original.current;
    if (!active.current || !state.ready || state.blocked || !pending || pending.phase !== "rejected" || running.current) return null;
    running.current = true; update({ busy: true, error: "", facts: null });
    try {
      const latest = await facts(pending.kind, pending.invoice?.id);
      if (!active.current) return null;
      sessionStorage.removeItem(storageKey); original.current = null; update({ pending: null, facts: latest }); return latest;
    } catch (error) { update({ error: (error as Error).message }); return null; }
    finally { running.current = false; update({ busy: false }); }
  }
  return { ...state, preview, readInvoice, run, finishRejected, frozen: !state.ready || state.blocked || state.busy || !!state.pending };
}
