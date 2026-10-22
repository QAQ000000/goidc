"use client";

import { useEffect, useRef, useState } from "react";
import { api, ApiError, type Service, type User } from "@/lib/api";
import { can } from "@/lib/admin-navigation";
import { serviceAdjustmentAuthority, serviceTermMicros, validServiceManagement, type ServiceManagement } from "./admin-service-adjustment-mutation";

type Job = { id: number; action: string; status: string; completed_at: string | null };
export type StatusFacts = { management: ServiceManagement; mode: string; jobs: Job[] };
type Body = { expected_version: number; source_job_id: number; target_status: string; reason: string; evidence: string; quote_hash: string };
type Quote = { service_id: number; customer_id: number; service_version: number; current_status: string; target_status: string; source_job_id: number; source_action: string; source_completed_at: string; provision_mode: string; expires_at: string; quote_hash: string };
export type StatusRequest = { body: Body; quote: Quote };
type Intent = StatusRequest & { authority: string; owner: number; key: string; phase: "uncertain" | "rejected" };
type Receipt = { service_id: number; customer_id: number; service_version: number; before_status: string; status: string; source_job_id: number; source_action: string; replayed: boolean };
type State = { ready: boolean; blocked: boolean; busy: boolean; pending: Intent | null; facts: StatusFacts | null; receipt: Receipt | null; error: string };
const writes = new Set<string>();
const object = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const positive = (v: unknown): v is number => typeof v === "number" && Number.isSafeInteger(v) && v > 0;
const hash = (v: unknown) => typeof v === "string" && /^[a-f0-9]{64}$/.test(v);
const text = (v: unknown, max: number) => typeof v === "string" && !!v && v === v.trim() && !/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/.test(v) && Array.from(v).length <= max;
export const statusSourceAction = (target: string) => target === "suspended" ? "suspend" : "resume";
export const latestLifecycleJob = (jobs: Job[]) => jobs.filter(j => ["create", "suspend", "resume", "terminate"].includes(j.action)).reduce<Job | null>((latest, job) => !latest || job.id > latest.id ? job : latest, null);
export function validStatusRequest(v: unknown, service: Service): v is StatusRequest & Record<string, unknown> {
  if (!object(v) || !object(v.body) || !object(v.quote)) return false;
  const b = v.body, q = v.quote;
  return Object.keys(b).length === 6 && Object.keys(b).every(k => ["expected_version", "source_job_id", "target_status", "reason", "evidence", "quote_hash"].includes(k)) && positive(b.expected_version) && positive(b.source_job_id) && ["active", "suspended"].includes(String(b.target_status)) && text(b.reason, 500) && text(b.evidence, 2000) && hash(b.quote_hash) && q.quote_hash === b.quote_hash && q.service_id === service.id && q.customer_id === service.user_id && q.service_version === b.expected_version && q.source_job_id === b.source_job_id && q.target_status === b.target_status && q.current_status === (b.target_status === "active" ? "suspended" : "active") && q.source_action === statusSourceAction(String(b.target_status)) && ["manual", "simulation"].includes(String(q.provision_mode)) && serviceTermMicros(q.source_completed_at) !== null && serviceTermMicros(q.expires_at) !== null;
}
const endpoint = (id: number, preview = false) => `/admin/services/${id}/status-correction${preview ? "-quotes" : "s"}`;

export function useServiceStatusCorrection(actor: User, service: Service, owner: number) {
  const authority = serviceAdjustmentAuthority(actor), storageKey = `goidc:service-status:admin:${actor.id}:${service.id}`;
  const active = useRef(true), running = useRef(false), original = useRef<Intent | null>(null), accepted = useRef("");
  const [state, setState] = useState<State>({ ready: false, blocked: false, busy: false, pending: null, facts: null, receipt: null, error: "" });
  const update = (patch: Partial<State>) => { if (active.current) setState(previous => ({ ...previous, ...patch })); };
  useEffect(() => {
    active.current = true;
    const timer = window.setTimeout(() => {
      try {
        const raw = sessionStorage.getItem(storageKey), value: unknown = raw === null ? null : JSON.parse(raw);
        if (value !== null && (!object(value) || !validStatusRequest(value, service) || Object.keys(value).some(k => !["body", "quote", "authority", "owner", "key", "phase"].includes(k)) || value.authority !== authority || value.owner !== owner || !/^service-status-[a-f0-9-]{36}$/i.test(String(value.key)) || !["uncertain", "rejected"].includes(String(value.phase)))) throw new Error("原状态纠错的身份、归属或恢复资料不匹配，已阻止新操作。请使用原账户核对，勿清除资料后重试。");
        original.current = value as Intent | null; update({ ready: true, pending: original.current });
      } catch (e) { update({ blocked: true, error: (e as Error).message }); }
    }, 0);
    return () => { active.current = false; window.clearTimeout(timer); };
    // Caller remounts on identity/scope/ownership, not version, status or provision mode.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [storageKey, authority, owner, service.id, service.user_id]);
  useEffect(() => {
    if (!state.pending) return;
    const guard = (e: BeforeUnloadEvent) => { e.preventDefault(); e.returnValue = ""; };
    window.addEventListener("beforeunload", guard); return () => window.removeEventListener("beforeunload", guard);
  }, [state.pending]);
  async function identity() {
    const live = await api<User>("/me", { portal: "admin" });
    if (!live || serviceAdjustmentAuthority(live) !== authority || !["services.view", "services.status.correct"].every(p => can(live, p))) throw new Error("管理员身份、状态纠错权限或客户范围已变化，请使用原账户核对。");
    if (!active.current) throw new Error("原页面已离开。");
  }
  async function facts(): Promise<StatusFacts> {
    await identity();
    const management = await api<unknown>(`/admin/services/${service.id}/management`, { portal: "admin" });
    if (!validServiceManagement(management) || management.service_id !== service.id || management.customer_id !== service.user_id || management.owner_generation !== owner) throw new Error("服务归属或归属代次已变化，请核对当前客户。");
    const detail = await api<unknown>(`/admin/services/${service.id}`, { portal: "admin" });
    if (!object(detail) || detail.id !== service.id || detail.user_id !== service.user_id || detail.owner_generation !== owner || detail.version !== management.service_version || detail.status !== management.status || !["manual", "simulation", "supplier", "nokvm"].includes(String(detail.provision_mode)) || !Array.isArray(detail.jobs) || !detail.jobs.every(j => object(j) && positive(j.id) && typeof j.action === "string" && typeof j.status === "string" && (j.completed_at === null || serviceTermMicros(j.completed_at) !== null)) || new Set(detail.jobs.map(j => j.id)).size !== detail.jobs.length) throw new Error("当前服务状态、任务或版本不一致，请重新读取。");
    const jobs = detail.jobs.map(j => ({ id: j.id as number, action: j.action as string, status: j.status as string, completed_at: j.completed_at as string | null }));
    await identity(); return { management, mode: String(detail.provision_mode), jobs };
  }
  async function read() {
    if (!active.current || !state.ready || state.blocked || original.current || running.current) return null;
    running.current = true; accepted.current = ""; update({ busy: true, error: "", facts: null });
    try { const latest = await facts(); update({ facts: latest }); return active.current ? latest : null; }
    catch (e) { update({ error: (e as Error).message }); return null; }
    finally { running.current = false; update({ busy: false }); }
  }
  async function preview(baseline: StatusFacts, source: number, reason: string, evidence: string) {
    if (!active.current || !state.ready || state.blocked || original.current || running.current) return null;
    running.current = true; accepted.current = ""; update({ busy: true, error: "", receipt: null });
    try {
      const before = await facts(), target = before.management.status === "active" ? "suspended" : "active", job = latestLifecycleJob(before.jobs);
      if (before.management.service_version !== baseline.management.service_version || before.management.status !== baseline.management.status || before.mode !== baseline.mode) throw new Error("服务状态或版本已变化，请重新读取后核对。");
      if (!["manual", "simulation"].includes(before.mode) || !["active", "suspended"].includes(before.management.status) || !positive(source) || job && (job.id !== source || job.status !== "succeeded" || job.action !== statusSourceAction(target) || !job.completed_at)) throw new Error("仅人工/模拟服务支持纠错；来源须为最新成功的对应暂停/恢复任务，不能绕过后续失败或取消尝试。");
      const body = { expected_version: before.management.service_version, source_job_id: source, target_status: target, reason: reason.trim(), evidence: evidence.trim() };
      if (!text(body.reason, 500) || !text(body.evidence, 2000)) throw new Error("请填写1–500字原因及1–2000字非秘密完成凭据，不能包含非法控制字符。");
      const quote = await api<unknown>(endpoint(service.id, true), { method: "POST", portal: "admin", body: JSON.stringify(body) });
      const request = { body: { ...body, quote_hash: object(quote) ? quote.quote_hash : null }, quote };
      if (!validStatusRequest(request, service) || request.quote.current_status !== before.management.status || request.quote.provision_mode !== before.mode || job && serviceTermMicros(request.quote.source_completed_at) !== serviceTermMicros(job.completed_at) || Date.parse(request.quote.expires_at) <= Date.now()) throw new Error("纠错预览的服务、来源任务、完成时间或有效期不一致。");
      const after = await facts();
      if (after.management.service_version !== body.expected_version || after.management.status !== before.management.status || after.mode !== before.mode || JSON.stringify(latestLifecycleJob(after.jobs)) !== JSON.stringify(job)) throw new Error("预览期间服务或最新生命周期任务已变化，请重新读取。");
      accepted.current = JSON.stringify(request); update({ facts: after }); return active.current ? request : null;
    } catch (e) { update({ error: (e as Error).message }); return null; }
    finally { running.current = false; update({ busy: false }); }
  }
  async function run(input?: StatusRequest) {
    if (!active.current || !state.ready || state.blocked || running.current || writes.has(storageKey) || original.current?.phase === "rejected") return null;
    running.current = true; writes.add(storageKey); update({ busy: true, error: "", receipt: null, facts: null });
    let attempt = original.current, businessFailure = false; const retry = !!attempt;
    try {
      if (!attempt) {
        if (!validStatusRequest(input, service) || JSON.stringify(input) !== accepted.current) throw new Error("纠错内容已变化，请重新预览并确认。");
        const before = await facts();
        if (before.management.service_version !== input.body.expected_version || before.management.status !== input.quote.current_status || before.mode !== input.quote.provision_mode || Date.parse(input.quote.expires_at) <= Date.now()) throw new Error("预览已过期或服务已变化，请重新读取并确认。");
        if (sessionStorage.getItem(storageKey) !== null) throw new Error("存在未恢复的状态纠错请求，不能覆盖。");
        const next: Intent = { ...JSON.parse(accepted.current), authority, owner, key: `service-status-${crypto.randomUUID()}`, phase: "uncertain" };
        sessionStorage.setItem(storageKey, JSON.stringify(next)); original.current = next; attempt = next; accepted.current = ""; update({ pending: next });
      } else await facts(); // Replay is independent of current status, source job and old quote expiry.
      if (!active.current) return null;
      let value: unknown;
      try { value = await api<unknown>(endpoint(service.id), { method: "POST", portal: "admin", headers: { "Idempotency-Key": attempt.key }, body: JSON.stringify(attempt.body) }); }
      catch (e) { businessFailure = true; throw e; }
      if (!object(value) || value.service_id !== service.id || value.customer_id !== service.user_id || value.service_version !== attempt.body.expected_version + 1 || value.before_status !== attempt.quote.current_status || value.status !== attempt.body.target_status || value.source_job_id !== attempt.body.source_job_id || value.source_action !== attempt.quote.source_action || typeof value.replayed !== "boolean") throw new Error("状态纠错回执与原服务、状态、版本或来源任务不一致，请恢复原请求核验。");
      const latest = await facts(); if (!active.current) return null;
      sessionStorage.removeItem(storageKey); original.current = null;
      const receipt = value as Receipt; update({ pending: null, receipt, facts: latest }); return { receipt, facts: latest };
    } catch (e) {
      const rejected = !retry && businessFailure && e instanceof ApiError && ([400, 422].includes(e.status) || e.status === 409 && e.code !== "idempotency_conflict");
      if (attempt && rejected) { attempt = { ...attempt, phase: "rejected" }; try { sessionStorage.setItem(storageKey, JSON.stringify(attempt)); original.current = attempt; } catch { update({ blocked: true }); } }
      update({ pending: attempt, error: `${(e as Error).message}${attempt ? rejected ? "。请求明确拒绝；读取当前事实后才能重新确认。" : "。结果尚未确认，仅可恢复原状态纠错请求，勿重复修改。" : ""}` }); return null;
    } finally { running.current = false; writes.delete(storageKey); update({ busy: false }); }
  }
  async function finishRejected() {
    const pending = original.current;
    if (!active.current || !state.ready || state.blocked || !pending || pending.phase !== "rejected" || running.current) return null;
    running.current = true; update({ busy: true, error: "", facts: null });
    try { const latest = await facts(); if (!active.current) return null; sessionStorage.removeItem(storageKey); original.current = null; update({ pending: null, facts: latest }); return latest; }
    catch (e) { update({ error: (e as Error).message }); return null; }
    finally { running.current = false; update({ busy: false }); }
  }
  return { ...state, read, preview, run, finishRejected, frozen: !state.ready || state.blocked || state.busy || !!state.pending };
}
