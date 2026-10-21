"use client";

import { useEffect, useRef, useState } from "react";
import { api, ApiError } from "@/lib/api";

type Attempt = { body: string; key: string };
type State = { busy: boolean; uncertain: boolean; conflict: boolean; error: string };
export type CustomerCreateResult = { id: number; email: string; username: string; display_name: string; status: string; profile_version: number; replayed: boolean };
const emptyState: State = { busy: false, uncertain: false, conflict: false, error: "" };

// Creation contains an initial password. Keep unresolved requests in this
// mounted view's memory; never persist them to browser storage. Permissions may
// change without disproving an earlier execution, so they cannot release a key.
export function useCustomerCreateMutation(actorID: number | undefined, allowed: boolean) {
  const attempts = useRef(new Map<number, Attempt>());
  const sending = useRef(new Set<number>());
  const active = useRef<{ actorID: number | undefined; allowed: boolean } | null>(null);
  const [states, setStates] = useState<Record<number, State>>({});
  useEffect(() => {
    active.current = { actorID, allowed };
    return () => { active.current = null; };
  }, [actorID, allowed]);
  const current = actorID ? states[actorID] ?? emptyState : emptyState;
  function update(id: number, patch: Partial<State>) {
    setStates(previous => ({ ...previous, [id]: { ...(previous[id] ?? emptyState), ...patch } }));
  }
  async function run(payload: Record<string, unknown>): Promise<CustomerCreateResult | null> {
    if (!actorID || !allowed || sending.current.has(actorID)) return null;
    sending.current.add(actorID);
    let attempt = attempts.current.get(actorID);
    if (!attempt) {
      attempt = { body: JSON.stringify(payload), key: `customer-create-${crypto.randomUUID()}` };
      attempts.current.set(actorID, attempt);
    }
    update(actorID, { busy: true, uncertain: false, conflict: false, error: "" });
    try {
      const result = await api<unknown>("/admin/customers", { method: "POST", body: attempt.body, headers: { "Idempotency-Key": attempt.key } });
      const frozen = JSON.parse(attempt.body) as Record<string, unknown>;
      if (!result || typeof result !== "object" || Array.isArray(result)) throw new Error("创建响应缺少有效确认");
      const value = result as Partial<CustomerCreateResult>;
      if (!Number.isSafeInteger(value.id) || Number(value.id) < 1 || value.profile_version !== 1 || typeof value.replayed !== "boolean" || value.email !== String(frozen.email ?? "").trim().toLowerCase() || typeof value.username !== "string" || typeof value.display_name !== "string" || value.status !== (frozen.status || "active")) throw new Error("创建响应与原客户意图不一致");
      attempts.current.delete(actorID);
      update(actorID, { uncertain: false, conflict: false, error: "" });
      return active.current?.actorID === actorID && active.current.allowed ? value as CustomerCreateResult : null;
    } catch (error) {
      const definite = error instanceof ApiError && (error.status === 400 || error.status === 422 || error.status === 409 && error.code !== "idempotency_conflict");
      if (definite) attempts.current.delete(actorID);
      update(actorID, {
        uncertain: !definite,
        conflict: definite && error instanceof ApiError && error.status === 409,
        error: definite ? (error as Error).message : `${(error as Error).message}。创建结果待核对，请保留本页面并使用原请求重试。`,
      });
      return null;
    } finally {
      sending.current.delete(actorID);
      update(actorID, { busy: false });
    }
  }
  return { ...current, run, frozen: current.busy || current.uncertain, setError: (error: string) => { if (actorID) update(actorID, { error }); } };
}
