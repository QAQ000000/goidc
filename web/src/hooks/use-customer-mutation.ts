"use client";

import { useEffect, useRef, useState } from "react";
import { api, ApiError } from "@/lib/api";

type Attempt = { version: 1; key: string; body: string };
type DraftState = { scope: string; attempt: Attempt | null; error: string; rejected: boolean };
type MutationAction = "sales-owner" | "group" | "profile";

function plainObject(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === "object" && !Array.isArray(value) && Object.getPrototypeOf(value) === Object.prototype;
}
function positiveInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0;
}
function frozenBody(action: MutationAction, raw: string): Record<string, unknown> {
  const invalid = () => new Error("原请求恢复信息损坏，请联系管理员核对操作结果");
  let body: unknown;
  try { body = JSON.parse(raw); } catch { throw invalid(); }
  if (!plainObject(body) || !positiveInteger(body.expected_version)) throw invalid();
  if (action !== "profile") {
    const target = action === "group" ? "group_id" : "sales_owner_id";
    if (Object.keys(body).length !== 2 || !Object.hasOwn(body, target) || (body[target] !== null && !positiveInteger(body[target]))) throw invalid();
  } else {
    const fields = Object.keys(body).filter(key => key !== "expected_version");
    if (!fields.length || fields.some(key => !["display_name", "email", "status"].includes(key))) throw invalid();
    if (Object.hasOwn(body, "display_name") && (typeof body.display_name !== "string" || !body.display_name.trim() || body.display_name.length > 120)) throw invalid();
    if (Object.hasOwn(body, "email") && (typeof body.email !== "string" || body.email.length > 254 || !/^[^\s@]+@[^\s@]+$/.test(body.email.trim()))) throw invalid();
    if (Object.hasOwn(body, "status") && body.status !== "active" && body.status !== "disabled") throw invalid();
  }
  return body;
}
function recoverAttempt(action: MutationAction, raw: string): Attempt {
  let value: unknown;
  try { value = JSON.parse(raw); } catch { throw new Error("原请求恢复信息损坏，请联系管理员核对操作结果"); }
  if (!plainObject(value) || value.version !== 1 || typeof value.key !== "string" || value.key.length < 8 || value.key.length > 128 || value.key.trim() !== value.key || /[\u0000-\u001f\u007f]/.test(value.key) || typeof value.body !== "string") throw new Error("原请求恢复信息损坏，请联系管理员核对操作结果");
  frozenBody(action, value.body);
  return value as Attempt;
}

// Each action owns its saved request. Current rows/versions must never replace
// the body of an unresolved request, including after a refresh or actor change.
export function useCustomerMutation(actorID: number | undefined, customerID: number, action: MutationAction, path: string, method: "PUT" | "PATCH") {
  const scope = actorID ? `zjmfgo:customer-mutation:v1:${actorID}:${customerID}:${action}` : "";
  const [state, setState] = useState<DraftState>({ scope: "", attempt: null, error: "", rejected: false });
  const [busyScope, setBusyScope] = useState("");
  const [revision, setRevision] = useState(0);
  const sending = useRef(false);
  const activeScope = useRef(scope);
  useEffect(() => {
    activeScope.current = scope;
    if (!scope) return;
    const timer = window.setTimeout(() => {
      try {
        const raw = sessionStorage.getItem(scope);
        let attempt: Attempt | null = null;
        if (raw) attempt = recoverAttempt(action, raw);
        if (activeScope.current === scope) setState({ scope, attempt, error: "", rejected: false });
      } catch (error) {
        if (activeScope.current === scope) setState({ scope, attempt: null, error: (error as Error).message || "无法读取原请求恢复信息", rejected: true });
      }
    }, 0);
    return () => {
      window.clearTimeout(timer);
      if (activeScope.current === scope) activeScope.current = "";
    };
  }, [scope, revision, action]);
  const visible = !!scope && state.scope === scope;
  const attempt = visible ? state.attempt : null;
  const ready = visible && (!state.rejected || !!attempt);

  async function submit(body: Record<string, unknown>): Promise<boolean> {
    if (!scope || !ready || sending.current || state.rejected) return false;
    sending.current = true;
    setBusyScope(scope);
    let original = attempt;
    try {
      if (!original) {
        // Check storage synchronously as well, so remounts cannot overwrite an
        // unresolved intent before React has rendered the recovered draft.
        const raw = sessionStorage.getItem(scope);
        if (raw) throw new Error("发现待确认请求，请重新打开窗口恢复原请求");
        const next: Attempt = { version: 1, key: crypto.randomUUID(), body: JSON.stringify(body) };
        frozenBody(action, next.body);
        sessionStorage.setItem(scope, JSON.stringify(next));
        original = next;
      }
      setState({ scope, attempt: original, error: "", rejected: false });
      const frozen = frozenBody(action, original.body);
      const result = await api<Record<string, unknown>>(path, { method, headers: { "Idempotency-Key": original.key }, body: original.body });
      const idField = action === "profile" ? "user_id" : "customer_id";
      const versionField = action === "profile" ? "profile_version" : action === "group" ? "group_membership_version" : "sales_version";
      const expected = Number(frozen.expected_version);
      const validVersion = result && positiveInteger(result[versionField]) && (result[versionField] === expected + 1 || (action === "group" && result[versionField] === expected));
      const target = action === "group" ? "group_id" : "sales_owner_id";
      if (!plainObject(result) || result[idField] !== customerID || typeof result.replayed !== "boolean" || !validVersion || (action !== "profile" && result[target] !== frozen[target])) throw new Error("响应缺少有效操作确认，请使用原请求重试核验");
      // A failed storage cleanup leaves the confirmed request available for a
      // harmless replay; it never permits a replacement while it remains saved.
      try { sessionStorage.removeItem(scope); } catch { /* Confirmed by API. */ }
      if (activeScope.current !== scope) return false;
      setState({ scope, attempt: null, error: "", rejected: false });
      return true;
    } catch (error) {
      if (activeScope.current === scope) {
        const definite = error instanceof ApiError && (error.status === 400 || error.status === 422 || (error.status === 409 && error.code !== "idempotency_conflict"));
        setState({ scope, attempt: original, error: (error as Error).message || "请求结果尚未确认", rejected: definite || !original });
      }
      return false;
    } finally {
      sending.current = false;
      if (activeScope.current === scope) setBusyScope("");
    }
  }

  // Only a definitive validation/version rejection permits a fresh edit. An
  // auth/scope failure after an unknown response does not disprove prior success.
  function editRejected(): boolean {
    if (!visible || !state.rejected || !state.attempt || sending.current) return false;
    try {
      sessionStorage.removeItem(scope);
      setState({ scope, attempt: null, error: "", rejected: false });
      return true;
    } catch {
      setState(current => ({ ...current, error: "无法清理已拒绝请求，请检查浏览器存储后重试" }));
      return false;
    }
  }
  return {
    ready, pending: attempt, busy: busyScope === scope && !!scope,
    error: visible ? state.error : "", rejected: visible && state.rejected,
    submit, editRejected, reload: () => setRevision(value => value + 1),
  };
}
