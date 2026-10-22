"use client";

import { useEffect, useRef, useState } from "react";
import { api, ApiError, type Service, type User } from "@/lib/api";
import { can } from "@/lib/admin-navigation";
import { serviceAdjustmentAuthority, serviceTermMicros, validServiceManagement, type ServiceManagement } from "./admin-service-adjustment-mutation";
import { validRenewalInvoice, type RenewalInvoice } from "./admin-service-renewal-mutation";

export type CollectionDetails = { received_at: string; method: string; receiving_account: string; transaction_no: string; evidence_ref: string; evidence_sha256: string; reason: string };
type CollectionBody = CollectionDetails & { expected_version: number; expected_service_version: number; amount_cents: number; quote_hash: string };
export type CollectionInvoice = RenewalInvoice & { created_at: string };
type CollectionQuote = { service_id: number; customer_id: number; invoice_id: number; invoice_no: string; invoice_version: number; service_version: number; amount_cents: number; received_at: string; method: string; receiving_account: string; transaction_no: string; quote_hash: string; expires_at: string };
export type CollectionRequest = { invoice: CollectionInvoice; body: CollectionBody; quote: CollectionQuote };
type Intent = CollectionRequest & { key: string; authority: string; owner: number; phase: "uncertain" | "rejected" };
type Receipt = { service_id: number; customer_id: number; invoice_id: number; invoice_no: string; invoice_version: number; service_version: number; payment_id: number; collection_id: number; amount_cents: number; received_at: string; status: "paid"; renewal_status: "completed" | "pending"; replayed: boolean };
type Facts = { management: ServiceManagement; invoice: CollectionInvoice };
type State = { ready: boolean; busy: boolean; blocked: boolean; pending: Intent | null; error: string; receipt: Receipt | null; facts: Facts | null };
const writes = new Set<string>();
const object = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const integer = (v: unknown, min = 1): v is number => typeof v === "number" && Number.isSafeInteger(v) && v >= min;
const hash = (v: unknown) => typeof v === "string" && /^[a-f0-9]{64}$/.test(v);
const text = (v: unknown, max: number, multiline = false) => typeof v === "string" && v === v.trim() && !!v && !(multiline ? /[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/ : /[\x00-\x1f\x7f]/).test(v) && Array.from(v).length <= max;
const fields = ["expected_version", "expected_service_version", "amount_cents", "received_at", "method", "receiving_account", "transaction_no", "evidence_ref", "evidence_sha256", "reason", "quote_hash"];
export function validCollectionInvoice(value: unknown, service: Service, id: number): value is CollectionInvoice {
  return object(value) && serviceTermMicros(value.created_at) !== null && validRenewalInvoice(value, service.id, service.user_id, id);
}
export function validCollectionRequest(v: unknown, service: Service): v is CollectionRequest & Record<string, unknown> {
  if (!object(v) || !object(v.invoice) || !integer(v.invoice.id) || !validCollectionInvoice(v.invoice, service, v.invoice.id) || !object(v.body) || !object(v.quote)) return false;
  const { body: b, invoice: i, quote: q } = v;
  const received = serviceTermMicros(b.received_at), created = serviceTermMicros(i.created_at);
  if (Object.keys(b).length !== fields.length || Object.keys(b).some(k => !fields.includes(k)) || b.expected_version !== i.version || !integer(b.expected_service_version) || !integer(b.amount_cents) || b.amount_cents !== i.amount_cents || i.status !== "unpaid" || i.payment_pending || i.import_review_required || received === null || created === null || received < created || !["bank_transfer", "cash", "other_offline"].includes(String(b.method)) || !text(b.receiving_account, 120) || !text(b.transaction_no, 160) || !text(b.evidence_ref, 500) || !hash(b.evidence_sha256) || !text(b.reason, 500, true) || !hash(b.quote_hash)) return false;
  return q.service_id === service.id && q.customer_id === service.user_id && q.invoice_id === i.id && q.invoice_no === i.invoice_no && q.invoice_version === b.expected_version && q.service_version === b.expected_service_version && q.amount_cents === b.amount_cents && serviceTermMicros(q.received_at) === received && q.method === b.method && q.receiving_account === b.receiving_account && q.transaction_no === b.transaction_no && q.quote_hash === b.quote_hash && serviceTermMicros(q.expires_at) !== null;
}
const endpoint = (service: number, invoice: number, preview = false) => `/admin/services/${service}/renewals/${invoice}/manual-collection${preview ? "-quotes" : "s"}`;

// Separate from balance payment: one durable, exact offline collection intent per actor/service.
export function useServiceManualCollection(actor: User, service: Service, owner: number) {
  const authority = serviceAdjustmentAuthority(actor), storageKey = `goidc:service-manual-collection:admin:${actor.id}:${service.id}`;
  const active = useRef(true), running = useRef(false), original = useRef<Intent | null>(null), previewBody = useRef("");
  const [state, setState] = useState<State>({ ready: false, busy: false, blocked: false, pending: null, error: "", receipt: null, facts: null });
  const update = (patch: Partial<State>) => { if (active.current) setState(previous => ({ ...previous, ...patch })); };
  useEffect(() => {
    active.current = true;
    const timer = window.setTimeout(() => {
      try {
        const raw = sessionStorage.getItem(storageKey), value: unknown = raw === null ? null : JSON.parse(raw);
        if (value !== null && (!object(value) || Object.keys(value).some(k => !["invoice", "body", "quote", "key", "authority", "owner", "phase"].includes(k)) || !validCollectionRequest(value, service) || !/^service-collection-[a-f0-9-]{36}$/i.test(String(value.key)) || value.authority !== authority || value.owner !== owner || !["uncertain", "rejected"].includes(String(value.phase)))) throw new Error("原人工收款的身份、归属或恢复资料不匹配，已阻止新收款。请用原账户核对，勿删除资料后重收。");
        original.current = value as Intent | null; update({ ready: true, pending: original.current });
      } catch (e) { update({ blocked: true, error: (e as Error).message }); }
    }, 0);
    return () => { active.current = false; window.clearTimeout(timer); };
    // The caller remounts on authority/ownership, never merely on service version.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [storageKey, authority, owner, service.id, service.user_id]);
  useEffect(() => {
    if (!state.pending) return;
    const guard = (e: BeforeUnloadEvent) => { e.preventDefault(); e.returnValue = ""; };
    window.addEventListener("beforeunload", guard); return () => window.removeEventListener("beforeunload", guard);
  }, [state.pending]);
  async function identity() {
    const live = await api<User>("/me", { portal: "admin" });
    if (!live || serviceAdjustmentAuthority(live) !== authority || !["services.view", "invoices.view", "invoices.collect_manual"].every(p => can(live, p))) throw new Error("管理员身份、权限或客户范围已变化，请使用原账户核对。");
    if (!active.current) throw new Error("原页面已离开。");
  }
  async function facts(id: number): Promise<Facts> {
    await identity();
    const management = await api<unknown>(`/admin/services/${service.id}/management`, { portal: "admin" });
    if (!validServiceManagement(management) || management.service_id !== service.id || management.customer_id !== service.user_id || management.owner_generation !== owner) throw new Error("服务归属或归属代次已变化，请核对当前客户。");
    const value = await api<unknown>(`/admin/invoices/${id}`, { portal: "admin" });
    if (!validCollectionInvoice(value, service, id)) throw new Error("账单并非当前客户与服务的单项自动续费单，或创建时间缺失。");
    const invoice: CollectionInvoice = { id: value.id, user_id: value.user_id, invoice_no: value.invoice_no, version: value.version, status: value.status, amount_cents: value.amount_cents, created_at: value.created_at, invoice_kind: value.invoice_kind, import_review_required: value.import_review_required, payment_pending: value.payment_pending, items: value.items.map(i => ({ service_id: i.service_id, item_type: i.item_type, amount_cents: i.amount_cents })) };
    await identity(); return { management, invoice };
  }
  async function readInvoice(id: number) {
    if (!active.current || !state.ready || state.blocked || original.current || running.current || !integer(id)) return null;
    running.current = true; previewBody.current = ""; update({ busy: true, error: "", facts: null });
    try { const latest = await facts(id); update({ facts: latest }); return active.current ? latest.invoice : null; }
    catch (e) { update({ error: (e as Error).message }); return null; }
    finally { running.current = false; update({ busy: false }); }
  }
  async function preview(invoice: CollectionInvoice, details: CollectionDetails) {
    if (!active.current || !state.ready || state.blocked || original.current || running.current) return null;
    running.current = true; previewBody.current = ""; update({ busy: true, error: "", receipt: null });
    try {
      const before = await facts(invoice.id);
      if (before.invoice.version !== invoice.version || before.invoice.amount_cents !== invoice.amount_cents) throw new Error("账单已变化，请重新读取账单并核对。");
      const body = { expected_version: before.invoice.version, expected_service_version: before.management.service_version, amount_cents: before.invoice.amount_cents, received_at: details.received_at, method: details.method, receiving_account: details.receiving_account.trim(), transaction_no: details.transaction_no.trim(), evidence_ref: details.evidence_ref.trim(), evidence_sha256: details.evidence_sha256, reason: details.reason.trim() };
      // Validate all user inputs before sending; a reference string is never evidence bytes.
      const tentative = { invoice: before.invoice, body: { ...body, quote_hash: "a".repeat(64) }, quote: { service_id: service.id, customer_id: service.user_id, invoice_id: invoice.id, invoice_no: invoice.invoice_no, invoice_version: body.expected_version, service_version: body.expected_service_version, ...body, quote_hash: "a".repeat(64), expires_at: "9999-01-01T00:00:00Z" } };
      if (!validCollectionRequest(tentative, service) || serviceTermMicros(body.received_at)! > BigInt(Date.now()) * BigInt(1000)) throw new Error("请核对正数全额账单、实际到账时间（含时区）、线下来源、凭据 SHA-256 和原因。");
      const quote = await api<unknown>(endpoint(service.id, invoice.id, true), { method: "POST", portal: "admin", body: JSON.stringify(body) });
      const request = { invoice: before.invoice, body: { ...body, quote_hash: object(quote) ? quote.quote_hash : null }, quote };
      if (!validCollectionRequest(request, service) || Date.parse(request.quote.expires_at) <= Date.now()) throw new Error("人工收款预览的账单、金额、来源或有效期不一致，请重新预览。");
      const after = await facts(invoice.id);
      if (after.management.service_version !== body.expected_service_version || after.invoice.version !== body.expected_version || after.invoice.status !== "unpaid" || after.invoice.amount_cents !== body.amount_cents || after.invoice.payment_pending || after.invoice.import_review_required) throw new Error("预览期间服务或账单已变化，请重新读取。");
      previewBody.current = JSON.stringify(request); update({ facts: after }); return active.current ? request : null;
    } catch (e) { update({ error: (e as Error).message }); return null; }
    finally { running.current = false; update({ busy: false }); }
  }
  async function run(input?: CollectionRequest) {
    if (!active.current || !state.ready || state.blocked || running.current || writes.has(storageKey) || original.current?.phase === "rejected") return null;
    running.current = true; writes.add(storageKey); update({ busy: true, error: "", receipt: null, facts: null });
    let attempt = original.current, businessFailure = false; const retry = !!attempt;
    try {
      if (!attempt) {
        if (!validCollectionRequest(input, service) || JSON.stringify(input) !== previewBody.current) throw new Error("收款内容已变化，请重新预览并确认。");
        const before = await facts(input.invoice.id);
        if (before.management.service_version !== input.body.expected_service_version || before.invoice.version !== input.body.expected_version || before.invoice.amount_cents !== input.body.amount_cents || before.invoice.status !== "unpaid" || before.invoice.payment_pending || before.invoice.import_review_required || Date.parse(input.quote.expires_at) <= Date.now()) throw new Error("预览已过期或账单/服务已变化，请重新读取并确认。");
        if (sessionStorage.getItem(storageKey) !== null) throw new Error("存在未恢复的人工收款请求，不能覆盖。");
        const next: Intent = { ...JSON.parse(previewBody.current), key: `service-collection-${crypto.randomUUID()}`, authority, owner, phase: "uncertain" };
        sessionStorage.setItem(storageKey, JSON.stringify(next)); original.current = next; attempt = next; previewBody.current = ""; update({ pending: next });
      } else await facts(attempt.invoice.id); // History replay ignores current paid status, versions and quote expiry.
      if (!active.current) return null;
      let value: unknown;
      try { value = await api<unknown>(endpoint(service.id, attempt.invoice.id), { method: "POST", portal: "admin", headers: { "Idempotency-Key": attempt.key }, body: JSON.stringify(attempt.body) }); }
      catch (e) { businessFailure = true; throw e; }
      if (!object(value) || value.service_id !== service.id || value.customer_id !== service.user_id || value.invoice_id !== attempt.invoice.id || value.invoice_no !== attempt.invoice.invoice_no || value.amount_cents !== attempt.body.amount_cents || serviceTermMicros(value.received_at) !== serviceTermMicros(attempt.body.received_at) || !integer(value.invoice_version) || value.invoice_version <= attempt.body.expected_version || !integer(value.service_version) || value.service_version < attempt.body.expected_service_version || !integer(value.payment_id) || !integer(value.collection_id) || value.status !== "paid" || !["pending", "completed"].includes(String(value.renewal_status)) || typeof value.replayed !== "boolean") throw new Error("人工收款回执与原客户、服务、账单或到账事实不一致，请恢复原请求核验。");
      const latest = await facts(attempt.invoice.id);
      if (!active.current) return null;
      if (latest.invoice.invoice_no !== value.invoice_no) throw new Error("当前账单与原回执编号不一致。");
      sessionStorage.removeItem(storageKey); original.current = null;
      const receipt = value as Receipt; update({ pending: null, receipt, facts: latest }); return { receipt, facts: latest };
    } catch (e) {
      const rejected = !retry && businessFailure && e instanceof ApiError && ([400, 422].includes(e.status) || e.status === 409 && e.code !== "idempotency_conflict");
      if (attempt && rejected) {
        attempt = { ...attempt, phase: "rejected" };
        try { sessionStorage.setItem(storageKey, JSON.stringify(attempt)); original.current = attempt; } catch { update({ blocked: true }); }
      }
      update({ pending: attempt, error: `${(e as Error).message}${attempt ? rejected ? "。请求明确拒绝；读取当前事实后才能重新确认。" : "。结果尚未确认，仅可恢复原人工收款请求，勿重复收款。" : ""}` }); return null;
    } finally { running.current = false; writes.delete(storageKey); update({ busy: false }); }
  }
  async function finishRejected() {
    const pending = original.current;
    if (!active.current || !state.ready || state.blocked || !pending || pending.phase !== "rejected" || running.current) return null;
    running.current = true; update({ busy: true, error: "", facts: null });
    try {
      const latest = await facts(pending.invoice.id); if (!active.current) return null;
      sessionStorage.removeItem(storageKey); original.current = null; update({ pending: null, facts: latest }); return latest;
    } catch (e) { update({ error: (e as Error).message }); return null; }
    finally { running.current = false; update({ busy: false }); }
  }
  return { ...state, readInvoice, preview, run, finishRejected, frozen: !state.ready || state.blocked || state.busy || !!state.pending };
}
