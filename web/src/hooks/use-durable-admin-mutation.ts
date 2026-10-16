"use client";

import { useEffect, useRef, useState } from "react";
import { api, ApiError, type User } from "@/lib/api";
import { can } from "@/lib/admin-navigation";

export type DurableRequest = { path: string; body: string; key: string; kind: string; target: number; rejected: boolean };
export type ReadFactsResult = string | { text: string; canEndRejected?: boolean };
type Options<T> = {
  actor: number;
  scope: string;
  readPermission?: string;
  validate: (request: DurableRequest) => boolean;
  permission: (request: DurableRequest) => string;
  receipt: (result: T, request: DurableRequest) => boolean;
  rejectionCodes: ReadonlySet<string>;
  readFacts: (request: DurableRequest) => Promise<ReadFactsResult>;
  onSuccess: (result: T, request: DurableRequest) => void;
};

// Stores the complete original request before dispatch. Business-specific
// validators and receipt matchers remain in their owning feature.
export function useDurableAdminMutation<T>(options: Options<T>) {
  const { actor, scope, validate } = options;
  const storage = `zjmfgo:admin-mutation:${actor}:${scope}`;
  const [pending, setPending] = useState<DurableRequest | null>(null);
  const [ready, setReady] = useState(false);
  const [storageError, setStorageError] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [reviewed, setReviewed] = useState(false);
  const [latestFacts, setLatestFacts] = useState("");
  const running = useRef(false);
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  useEffect(() => {
    if (!actor) return;
    queueMicrotask(() => {
      if (!mounted.current) return;
      try {
        const raw = sessionStorage.getItem(storage);
        if (raw) {
          const request = JSON.parse(raw) as DurableRequest;
          if (!request || typeof request.key !== "string" || request.key.length < 8 || request.key.length > 128 || typeof request.body !== "string" || typeof request.rejected !== "boolean" || !Number.isSafeInteger(request.target) || request.target < 1 || !validate(request)) throw new Error();
          setPending(request);
        }
        setReady(true);
      } catch { setStorageError("原请求恢复资料无法读取，已暂停新操作。请联系支持核实，勿清空后重新提交。"); }
    });
  }, [actor, storage, validate]);
  useEffect(() => {
    if (!pending && !busy) return;
    const leave = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    window.addEventListener("beforeunload", leave);
    return () => window.removeEventListener("beforeunload", leave);
  }, [pending, busy]);
  async function identity(permission?: string) {
    const live = await api<User>("/me");
    if (!actor || live.id !== actor || !can(live, "invoices.view") || (permission && !can(live, permission))) throw new Error("员工身份或权限已变化，请使用原账户核实；原请求保留。");
  }
  async function execute(input?: { path: string; body: object; kind: string; target: number }) {
    if (running.current || !ready || storageError || (!input && !pending) || (input && pending)) return;
    const request: DurableRequest = input ? { path: input.path, body: JSON.stringify(input.body), kind: input.kind, target: input.target, key: `admin-exception-${crypto.randomUUID()}`, rejected: false } : { ...pending!, rejected: false };
    if (!validate(request)) { setError("请求内容与当前案件不匹配，已停止提交。"); return; }
    running.current = true; setBusy(true); setError("");
    let dispatched = false;
    let responseReceived = false;
    try {
      await identity(options.permission(request));
      if (!mounted.current) return;
      try { sessionStorage.setItem(storage, JSON.stringify(request)); }
      catch { throw new Error("浏览器无法保存原请求，已停止发送。"); }
      setPending(request); setReviewed(false); setLatestFacts(""); dispatched = true;
      const result = await api<T>(request.path, { method: "POST", body: request.body, headers: { "Idempotency-Key": request.key } });
      responseReceived = true;
      if (!options.receipt(result, request)) throw new Error("回执尚未完成或关联不完整，请保留原请求核实。");
      await identity(options.permission(request));
      if (!mounted.current) return;
      try { sessionStorage.removeItem(storage); }
      catch { throw new Error("回执已确认，但恢复资料未能清理，请继续核实原请求。"); }
      setPending(null); setReviewed(false); setLatestFacts(""); options.onSuccess(result, request);
    } catch (failure) {
      if (!mounted.current) return;
      if (dispatched && !responseReceived && failure instanceof ApiError && options.rejectionCodes.has(failure.code)) {
        const rejected = { ...request, rejected: true };
        try { sessionStorage.setItem(storage, JSON.stringify(rejected)); setPending(rejected); }
        catch { setStorageError("原请求拒绝状态无法保存，请保留原资料核实。"); }
        setError(`${failure.message}。原版本、载荷与键已保留，请显式读取最新事实；不会自动换版本提交。`);
      } else setError(`${(failure as Error).message}${dispatched ? " 提交结果尚未确认，勿换键重复操作。" : " 未发送新请求。"}`);
    } finally { running.current = false; if (mounted.current) setBusy(false); }
  }
  async function readLatest() {
    if (running.current || !pending) return;
    running.current = true; setBusy(true); setError(""); setReviewed(false);
    try {
      await identity(options.readPermission);
      const facts = await options.readFacts(pending);
      await identity(options.readPermission);
      if (mounted.current) {
        setLatestFacts(typeof facts === "string" ? facts : facts.text);
        setReviewed(typeof facts === "string" || facts.canEndRejected !== false);
      }
    } catch (failure) { if (mounted.current) setError((failure as Error).message); }
    finally { running.current = false; if (mounted.current) setBusy(false); }
  }
  async function endRejected() {
    if (running.current || !pending?.rejected || !reviewed || !window.confirm("已核对最新事实，确认结束被拒绝的原请求？这不会改变业务或资金状态。新的操作仍须重新确认。")) return;
    running.current = true; setBusy(true); setError("");
    try {
      await identity(options.permission(pending));
      if (!mounted.current) return;
      sessionStorage.removeItem(storage); setPending(null); setReviewed(false); setLatestFacts("");
    } catch (failure) { if (mounted.current) setError((failure as Error).message); }
    finally { running.current = false; if (mounted.current) setBusy(false); }
  }
  return { pending, ready, storageError, busy, error, reviewed, latestFacts, execute, readLatest, endRejected, identity };
}
