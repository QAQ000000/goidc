"use client";

import { useEffect, useRef, useState } from "react";
import { api, ApiError, type User } from "@/lib/api";
import { can } from "@/lib/admin-navigation";
import { serviceAdjustmentAuthority } from "./admin-service-adjustment-mutation";

export type ServiceCommand = Record<string, unknown> & { path: string; body: Record<string, unknown>; context: Record<string, unknown> };
type Pending = ServiceCommand & { key: string; authority: string; rejected: boolean };
type Journal<T> = { pending: Pending | null; command: ServiceCommand | null; receipt: T | null };
export const record = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
export const whole = (v: unknown, min = 0): v is number => typeof v === "number" && Number.isSafeInteger(v) && v >= min;
export const digest = (v: unknown): v is string => typeof v === "string" && /^[a-f0-9]{64}$/.test(v);
export const reasonText = (v: unknown, max = 500): v is string => typeof v === "string" && v === v.trim() && !!v && !/[\x00-\x1f\x7f]/.test(v) && Array.from(v).length <= max;
const writes = new Set<string>();

// Shared transport journal only. Domain modules still validate every DTO,
// permission, immutable manifest and response; this hook never guesses a CRUD API.
export function useServiceCommand<T>(actor: User, scope: string, permissions: string[], valid: (v: unknown) => v is ServiceCommand, validReceipt: (v: unknown, c: ServiceCommand) => v is T) {
  const authority = serviceAdjustmentAuthority(actor), storageKey = `goidc:service-command:${actor.id}:${scope}`;
  const snapshot = useRef<string | null>(null);
  const active = useRef(true), running = useRef(false), journal = useRef<Journal<T>>({ pending: null, command: null, receipt: null });
  const [state, setState] = useState<Journal<T> & { ready: boolean; blocked: boolean; busy: boolean; error: string }>({ ready: false, blocked: false, busy: false, error: "", pending: null, command: null, receipt: null });
  const update = (patch: Partial<typeof state>) => { if (active.current) setState(s => ({ ...s, ...patch })); };
  function save(next: Journal<T>) {
    if (sessionStorage.getItem(storageKey) !== snapshot.current) throw new Error("另一页面已更新原操作，请重新打开核对，不能覆盖恢复资料。");
    const raw = JSON.stringify({ authority, ...next });
    sessionStorage.setItem(storageKey, raw); snapshot.current = raw;
    journal.current = next; update(next);
  }
  useEffect(() => {
    active.current = true;
    const timer = window.setTimeout(() => {
      try {
        const raw = sessionStorage.getItem(storageKey), v: unknown = raw ? JSON.parse(raw) : null;
        if (v !== null) {
          if (!record(v) || v.authority !== authority || !valid(v.command) || (v.pending !== null && (!record(v.pending) || !valid(v.pending) || v.pending.authority !== authority || !/^service-command-[a-f0-9-]{36}$/i.test(String(v.pending.key)) || typeof v.pending.rejected !== "boolean" || JSON.stringify({ path: v.pending.path, body: v.pending.body, context: v.pending.context }) !== JSON.stringify(v.command))) || (v.receipt !== null && (v.pending !== null || !validReceipt(v.receipt, v.command)))) throw new Error("原操作的身份、范围或恢复资料不匹配，请保留资料并使用原账户核对。");
          journal.current = { pending: v.pending as Pending | null, command: v.command, receipt: v.receipt as T | null };
        }
        snapshot.current = raw; update({ ready: true, ...journal.current });
      } catch (e) { update({ blocked: true, error: (e as Error).message }); }
    }, 0);
    return () => { active.current = false; window.clearTimeout(timer); };
    // Callers remount on actor/authority, never on a changed service version/owner.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [storageKey, authority]);
  useEffect(() => {
    if (!state.pending) return;
    const guard = (e: BeforeUnloadEvent) => { e.preventDefault(); e.returnValue = ""; };
    window.addEventListener("beforeunload", guard); return () => window.removeEventListener("beforeunload", guard);
  }, [state.pending]);
  async function identity() {
    const live = await api<User>("/me", { portal: "admin" });
    if (!active.current || !live || serviceAdjustmentAuthority(live) !== authority || !permissions.every(p => can(live, p))) throw new Error("管理员身份、权限或客户范围已变化，请使用原账户核对。");
  }
  async function run(input?: ServiceCommand) {
    if (!active.current || !state.ready || state.blocked || running.current || writes.has(storageKey) || journal.current.pending?.rejected) return null;
    running.current = true; writes.add(storageKey); update({ busy: true, error: "" });
    let pending = journal.current.pending, sent = false; const retry = !!pending;
    try {
      await identity();
      if (!pending) {
        if (!valid(input)) throw new Error("操作内容无效，请重新核对。");
        pending = { ...JSON.parse(JSON.stringify(input)), key: `service-command-${crypto.randomUUID()}`, authority, rejected: false };
        save({ pending, command: input, receipt: null });
      }
      if (!pending) throw new Error("原请求缺失，不能提交。");
      let value: unknown;
      try { sent = true; value = await api<unknown>(pending.path, { method: "POST", portal: "admin", headers: { "Idempotency-Key": pending.key }, body: JSON.stringify(pending.body) }); }
      catch (e) {
        if (!retry && e instanceof ApiError && ([400, 422].includes(e.status) || e.status === 409 && !["idempotency_conflict", "renewal_batch_busy", "renewal_batch_capacity"].includes(e.code))) {
          pending = { ...pending, rejected: true }; save({ pending, command: journal.current.command, receipt: null });
        }
        throw e;
      }
      const command = journal.current.command!;
      if (!validReceipt(value, command)) throw new Error("回执与原请求不一致，保留原请求以恢复核验。");
      await identity();
      save({ pending: null, command, receipt: value }); return value;
    } catch (e) {
      update({ error: `${(e as Error).message}${sent && journal.current.pending ? journal.current.pending.rejected ? "。明确拒绝；读取事实后才能结束此请求。" : "。结果待核验，只能恢复原请求。" : ""}` }); return null;
    } finally { running.current = false; writes.delete(storageKey); update({ busy: false }); }
  }
  async function clear(readFacts: () => Promise<unknown>) {
    if (running.current || state.blocked || !state.ready || journal.current.pending && !journal.current.pending.rejected) return false;
    running.current = true; update({ busy: true, error: "" });
    try { await identity(); await readFacts(); await identity(); if (sessionStorage.getItem(storageKey) !== snapshot.current) throw new Error("原操作已由另一页面更新，请重新读取。"); sessionStorage.removeItem(storageKey); snapshot.current = null; journal.current = { pending: null, command: null, receipt: null }; update(journal.current); return true; }
    catch (e) { update({ error: (e as Error).message }); return false; }
    finally { running.current = false; update({ busy: false }); }
  }
  async function reconcile(readReceipt: () => Promise<unknown>) {
    if (running.current || state.blocked || !state.ready || !journal.current.pending?.rejected || !journal.current.command) return false;
    running.current = true; update({ busy: true, error: "" });
    try {
      await identity(); const value = await readReceipt(); await identity();
      if (!validReceipt(value, journal.current.command)) throw new Error("核对结果与原清单不符。");
      save({ pending: null, command: journal.current.command, receipt: value }); return true;
    } catch (e) { update({ error: (e as Error).message }); return false; }
    finally { running.current = false; update({ busy: false }); }
  }
  return { ...state, identity, run, clear, reconcile, frozen: !state.ready || state.blocked || state.busy || !!state.pending };
}
