"use client";

import { useEffect, useRef, useState } from "react";
import { api, ApiError, type User } from "@/lib/api";
import { can } from "@/lib/admin-navigation";

export type CustomerReferrer = {
  customer_id: number;
  referral_version: number;
  attributed: boolean;
  referrer_visible: boolean;
  referrer_customer_id: number | null;
  referrer_name: string | null;
  effective_scope: "future_invoices_only";
};
export type ReferrerInput = { referrer_customer_id: number | null; expected_version: number; reason: string };
export type ReferrerResult = CustomerReferrer & { changed: boolean; replayed: boolean };
type Attempt = { body: string; key: string; phase: "uncertain" | "rejected"; error: string };
type State = { scope: string; ready: boolean; attempt: Attempt | null; error: string; reviewed: CustomerReferrer | null; busy: boolean };
const sending = new Set<string>();
const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);
const positive = (value: unknown): value is number => typeof value === "number" && Number.isSafeInteger(value) && value > 0;
function validInput(value: unknown): value is ReferrerInput {
  return object(value) && Object.keys(value).length === 3 && Object.keys(value).every(key => ["referrer_customer_id", "expected_version", "reason"].includes(key)) && (value.referrer_customer_id === null || positive(value.referrer_customer_id)) && positive(value.expected_version) && typeof value.reason === "string" && value.reason === value.reason.trim() && Array.from(value.reason).length > 0 && Array.from(value.reason).length <= 500 && !value.reason.includes("\0");
}
export function validCustomerReferrer(value: unknown, customerID: number): value is CustomerReferrer {
  if (!object(value) || value.customer_id !== customerID || !positive(value.referral_version) || typeof value.attributed !== "boolean" || typeof value.referrer_visible !== "boolean" || value.effective_scope !== "future_invoices_only") return false;
  if (!value.attributed) return value.referrer_visible && value.referrer_customer_id === null && value.referrer_name === null;
  return value.referrer_visible ? positive(value.referrer_customer_id) && (value.referrer_name === null || typeof value.referrer_name === "string") : value.referrer_customer_id === null && value.referrer_name === null;
}

// This action contains ordinary identifiers and an operator reason. Save its
// exact intent before PUT so a tab switch, remount or lost response keeps the key.
export function useCustomerReferrerMutation(actorID: number | undefined, customerID: number, allowed: boolean) {
  const scope = actorID && positive(customerID) ? `goidc:referrer-intent:admin:${actorID}:${customerID}` : "";
  const root = `/admin/customers/${customerID}/referrer`;
  const [state, setState] = useState<State>({ scope: "", ready: false, attempt: null, error: "", reviewed: null, busy: false });
  const active = useRef({ scope: "", allowed: false });
  const original = useRef<Attempt | null>(null);
  const reviewed = useRef<CustomerReferrer | null>(null);
  const localBusy = useRef(false);
  useEffect(() => {
    active.current = { scope, allowed };
    original.current = null; reviewed.current = null;
    if (!scope || !allowed) return;
    const timer = window.setTimeout(() => {
      try {
        const raw = sessionStorage.getItem(scope);
        const value: unknown = raw ? JSON.parse(raw) : null;
        if (raw !== null && (!object(value) || typeof value.body !== "string" || value.body.length > 10000 || !validInput(JSON.parse(value.body)) || typeof value.key !== "string" || value.key.length < 8 || value.key.length > 128 || value.key.trim() !== value.key || /[\u0000-\u001f\u007f]/.test(value.key) || !["uncertain", "rejected"].includes(String(value.phase)) || typeof value.error !== "string")) throw new Error("原推荐人请求恢复信息不可用，请联系管理员核对；当前操作已暂停。");
        if (active.current.scope === scope && active.current.allowed) {
          original.current = value as Attempt | null;
          setState({ scope, ready: true, attempt: value as Attempt | null, error: value ? String((value as Attempt).error || "发现待核验的原请求，请使用原请求重试。") : "", reviewed: null, busy: false });
        }
      } catch (error) {
        if (active.current.scope === scope && active.current.allowed) setState({ scope, ready: false, attempt: null, error: (error as Error).message, reviewed: null, busy: false });
      }
    }, 0);
    return () => { window.clearTimeout(timer); if (active.current.scope === scope) active.current = { scope: "", allowed: false }; };
  }, [scope, allowed]);
  const current = state.scope === scope && allowed;
  const ready = current && state.ready;
  const pending = current && state.attempt ? JSON.parse(state.attempt.body) as ReferrerInput : null;
  const live = () => allowed && active.current.allowed && active.current.scope === scope;
  function update(patch: Partial<State>) { if (live()) setState(previous => ({ ...previous, ...patch, scope })); }
  async function verifyActor() {
    if (!live()) throw new Error("当前页面身份已变更，请返回原管理员身份核对原请求。");
    const actual = await api<User>("/me", { portal: "admin" });
    if (!live() || actual?.id !== actorID || !can(actual, "customers.manage") || !can(actual, "affiliates.manage")) throw new Error("当前管理员会话或权限已变更；原请求保留，请使用原管理员身份核对。");
  }
  async function run(payload?: ReferrerInput): Promise<ReferrerResult | null> {
    if (!scope || !ready || !live() || localBusy.current || sending.has(scope) || original.current?.phase === "rejected") return null;
    localBusy.current = true; sending.add(scope); update({ busy: true, error: "", reviewed: null }); reviewed.current = null;
    let attempt = original.current;
    let authorityChecked = false;
    let businessRefusal = false;
    try {
      await verifyActor(); authorityChecked = true;
      if (!attempt) {
        if (!validInput(payload)) throw new Error("请选择推荐人或明确清除，并填写原因和当前关系版本。");
        if (sessionStorage.getItem(scope)) throw new Error("发现未恢复的原请求，请重新打开本区域核对。");
        const next: Attempt = { body: JSON.stringify(payload), key: `referrer-${crypto.randomUUID()}`, phase: "uncertain", error: "" };
        sessionStorage.setItem(scope, JSON.stringify(next)); attempt = next; original.current = next;
      }
      update({ attempt });
      let result: unknown;
      try { result = await api<unknown>(root, { method: "PUT", body: attempt.body, headers: { "Idempotency-Key": attempt.key }, portal: "admin" }); }
      catch (error) { businessRefusal = error instanceof ApiError && (error.status === 400 || error.status === 422 || error.status === 409 && error.code !== "idempotency_conflict"); throw error; }
      const input = JSON.parse(attempt.body) as ReferrerInput;
      const flags = object(result) ? { changed: result.changed, replayed: result.replayed } : null;
      if (!validCustomerReferrer(result, customerID) || !flags || typeof flags.changed !== "boolean" || typeof flags.replayed !== "boolean" || result.referral_version !== input.expected_version + (flags.changed ? 1 : 0) || result.attributed !== (input.referrer_customer_id !== null) || result.referrer_visible && result.referrer_customer_id !== input.referrer_customer_id) throw new Error("推荐关系响应缺少与原请求一致的确认，请使用原请求核验。");
      await verifyActor();
      if (!live()) return null;
      sessionStorage.removeItem(scope); original.current = null;
      update({ attempt: null, error: "", reviewed: null });
      return result as ReferrerResult;
    } catch (error) {
      const definite = businessRefusal;
      let message = (error as Error).message;
      if (attempt) {
        const next: Attempt = { ...attempt, phase: definite ? "rejected" : "uncertain", error: definite ? message : `${message}。结果待核验，请使用原请求重试。` };
        try { sessionStorage.setItem(scope, JSON.stringify(next)); original.current = next; attempt = next; }
        catch { message = "无法保存核验状态；原请求仍保留，请保留页面并使用原请求重试。"; }
      }
      update({ attempt, ready: !!attempt || !authorityChecked, error: attempt?.error || message });
      return null;
    } finally { localBusy.current = false; sending.delete(scope); update({ busy: false }); }
  }
  async function reviewCurrent(): Promise<CustomerReferrer | null> {
    if (!ready || !live() || localBusy.current || original.current?.phase !== "rejected") return null;
    localBusy.current = true; reviewed.current = null; update({ busy: true, reviewed: null, error: "" });
    try {
      await verifyActor();
      const result = await api<unknown>(root, { portal: "admin" });
      if (!validCustomerReferrer(result, customerID)) throw new Error("当前推荐关系响应无效，无法继续核对。");
      await verifyActor();
      if (!live()) return null;
      reviewed.current = result; update({ reviewed: result }); return result;
    } catch (error) { update({ error: (error as Error).message }); return null; }
    finally { localBusy.current = false; update({ busy: false }); }
  }
  async function reopenReviewed(): Promise<CustomerReferrer | null> {
    if (!ready || !live() || localBusy.current || original.current?.phase !== "rejected" || !reviewed.current) return null;
    localBusy.current = true; update({ busy: true });
    try {
      await verifyActor();
      if (!live() || !reviewed.current) return null;
      sessionStorage.removeItem(scope);
      const result = reviewed.current; original.current = null; reviewed.current = null;
      update({ attempt: null, reviewed: null, error: "" }); return result;
    } catch (error) { reviewed.current = null; update({ reviewed: null, error: `${(error as Error).message}。未释放原请求，请保留本页面后重新核对。` }); return null; }
    finally { localBusy.current = false; update({ busy: false }); }
  }
  return { ready, busy: current && state.busy, pending, rejected: current && state.attempt?.phase === "rejected", uncertain: current && state.attempt?.phase === "uncertain", frozen: !ready || current && (!!state.attempt || state.busy), error: current ? state.error : "", reviewed: current ? state.reviewed : null, run, reviewCurrent, reopenReviewed };
}
