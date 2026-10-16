"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Failure, Loading } from "./common";

export type CustomerFieldDefinition = { field_key: string; label: string; field_type: "text" | "textarea" | "date" | "number" | "boolean" | "select"; required: boolean; choices: string[]; description: string; validation_pattern?: string };
type DefinitionsPage = { items: CustomerFieldDefinition[]; total: number; limit: number; offset: number; definitions_version: number };
export function useCustomerFieldDefinitions(enabled: boolean) {
  const [revision, setRevision] = useState(0);
  const [state, setState] = useState<{ revision: number; fields: CustomerFieldDefinition[]; version: number; error: Error | null } | null>(null);
  const refresh = useCallback(() => setRevision(v => v + 1), []);
  useEffect(() => {
    if (!enabled) return;
    const controller = new AbortController();
    async function load() {
      const fields: CustomerFieldDefinition[] = [];
      let offset = 0, version: number | undefined;
      do {
        const page = await api<DefinitionsPage>(`/admin/customer-field-definitions?limit=100&offset=${offset}`, { signal: controller.signal });
        if (version !== undefined && page.definitions_version !== version) throw new Error("字段定义在加载期间变更，请重试获取完整列表");
        version = page.definitions_version;
        fields.push(...page.items);
        offset += page.items.length;
        if (offset >= page.total) break;
        if (!page.items.length) throw new Error("字段列表分页不完整，请重试");
      } while (!controller.signal.aborted);
      if (!controller.signal.aborted) setState({ revision, fields, version: version ?? 0, error: null });
    }
    void load().catch((error: Error) => { if (!controller.signal.aborted) setState({ revision, fields: [], version: 0, error }); });
    return () => controller.abort();
  }, [enabled, revision]);
  const current = enabled && state?.revision === revision ? state : null;
  return { fields: current?.fields ?? [], version: current?.version ?? 0, error: current?.error ?? null, ready: !!current && !current.error, refresh };
}
export function customerFieldValues(fields: CustomerFieldDefinition[], values: Record<string, unknown>) {
  const output: Record<string, unknown> = {};
  for (const field of fields) {
    let value = values[field.field_key];
    const empty = value === undefined || value === null || value === "";
    if (empty) { if (field.required) throw new Error(`请填写${field.label}`); output[field.field_key] = null; continue; }
    if (field.field_type === "number") {
      value = typeof value === "number" ? value : Number(value);
      if (!Number.isFinite(value)) throw new Error(`${field.label}需为有效数字`);
    } else if (field.field_type === "boolean") {
      if (typeof value !== "boolean") throw new Error(`请选择${field.label}`);
    } else {
      const text = String(value);
      if (field.required && !text.trim()) throw new Error(`请填写${field.label}`);
      if (field.field_type === "select" && !field.choices.includes(text)) throw new Error(`${field.label}的选项已变更，请重新选择`);
      if (field.field_type === "date" && !/^\d{4}-\d{2}-\d{2}$/.test(text)) throw new Error(`${field.label}需为有效日期`);
      value = text;
    }
    output[field.field_key] = value;
  }
  return output;
}
export function CustomerFieldInputs({ definitions, values, onChange, disabled = false, singleColumn = false }: { definitions: { fields: CustomerFieldDefinition[]; ready: boolean; error: Error | null; refresh: () => void }; values: Record<string, unknown>; onChange: (values: Record<string, unknown>) => void; disabled?: boolean; singleColumn?: boolean }) {
  return <section className="min-w-0 rounded border bg-background p-4 space-y-3"><h2 className="text-sm font-semibold">自定义字段</h2>{definitions.error ? <Failure error={definitions.error} retry={definitions.refresh} /> : !definitions.ready ? <Loading /> : !definitions.fields.length ? <p className="text-sm text-muted-foreground">暂无启用的自定义字段</p> : <div className={singleColumn ? "grid min-w-0 gap-4" : "grid min-w-0 gap-4 sm:grid-cols-2 xl:grid-cols-4"}>{definitions.fields.map(field => {
    const value = values[field.field_key];
    const update = (next: unknown) => onChange({ ...values, [field.field_key]: next });
    const shared = { disabled, required: field.required, "aria-label": field.label, className: "w-full min-w-0 mt-1.5" };
    return <label key={field.field_key} className="min-w-0 text-xs">{field.label}{field.required && <span className="text-destructive"> *</span>}{field.field_type === "boolean" || field.field_type === "select" ? <select {...shared} className="mt-1.5 h-9 w-full min-w-0 rounded border bg-background px-2" value={value === null || value === undefined ? "" : String(value)} onChange={event => update(event.target.value === "" ? null : field.field_type === "boolean" ? event.target.value === "true" : event.target.value)}><option value="">请选择{field.required ? "" : "（可留空）"}</option>{field.field_type === "boolean" ? <><option value="true">是</option><option value="false">否</option></> : <>{value != null && value !== "" && !field.choices.includes(String(value)) && <option value={String(value)} disabled>{String(value)}（旧选项）</option>}{field.choices.map(choice => <option key={choice} value={choice}>{choice}</option>)}</>}</select> : field.field_type === "textarea" ? <Textarea {...shared} value={String(value ?? "")} onChange={event => update(event.target.value)} /> : <Input {...shared} type={field.field_type === "number" ? "number" : field.field_type === "date" ? "date" : "text"} step={field.field_type === "number" ? "any" : undefined} value={String(value ?? "")} onChange={event => update(field.field_type === "number" ? event.target.value === "" ? null : Number(event.target.value) : event.target.value)} />}{field.description && <span className="mt-1 block break-words text-muted-foreground">{field.description}</span>}</label>;
  })}</div>}</section>;
}

// Hold the exact payload and key until a definite response arrives. Passwords
// remain only in memory and are never written to browser storage.
export function useCustomerMutation() {
  const running = useRef(false);
  const pending = useRef<{ path: string; method: string; body: string; key: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [uncertain, setUncertain] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [error, setError] = useState("");
  async function run<T>(path: string, method: string, payload: unknown): Promise<T | null> {
    if (running.current) return null;
    running.current = true;
    const request = uncertain && pending.current ? pending.current : { path, method, body: JSON.stringify(payload), key: `customer-${crypto.randomUUID()}` };
    pending.current = request;
    setBusy(true); setError(""); setConflict(false);
    try {
      const result = await api<T>(request.path, { method: request.method, body: request.body, headers: { "Idempotency-Key": request.key } });
      pending.current = null; setUncertain(false); return result;
    } catch (e) {
      if (e instanceof ApiError && e.status < 500) { pending.current = null; setUncertain(false); setConflict(e.status === 409); setError(`${e.message}${e.status === 409 ? "。输入已保留，请更新版本后重新保存。" : ""}`); }
      else { setUncertain(true); setError("提交结果暂时未知，请使用原请求重试；当前输入已锁定，避免重复创建或修改。"); }
      return null;
    } finally { running.current = false; setBusy(false); }
  }
  return { run, busy, uncertain, conflict, error, setError, frozen: busy || uncertain };
}
export function CustomerMutationMessage({ mutation }: { mutation: ReturnType<typeof useCustomerMutation> }) { return mutation.error ? <p role="alert" className="text-sm text-destructive break-words">{mutation.error}</p> : null; }
export function customerListSuffix() {
  const source = new URLSearchParams(window.location.search), output = new URLSearchParams();
  for (const key of ["q", "status", "page", "limit", "offset", "group_id", "sales_owner_id", "date_from", "date_to"]) { const value = source.get(key); if (value) output.set(key, value); }
  return output.size ? `?${output.toString()}` : "";
}
export function useCustomerDraft<T>(key: string | null, snapshot: T, dirty: boolean) {
  const shape = useRef(snapshot);
  const [loadedKey, setLoadedKey] = useState<string | null>(null);
  const [draft, setDraft] = useState<T | null>(null);
  const leaving = useRef(false);
  useEffect(() => {
    if (!key) return;
    const timer = window.setTimeout(() => {
      try {
        const raw = sessionStorage.getItem(key);
        if (raw) {
          const value: unknown = JSON.parse(raw);
          if (value && typeof value === "object" && !Array.isArray(value) && Object.entries(shape.current as Record<string, unknown>).every(([name, expected]) => {
            const actual = (value as Record<string, unknown>)[name];
            return typeof actual === typeof expected && (typeof expected !== "object" || !!actual && !Array.isArray(actual));
          })) setDraft(value as T);
        }
      } catch {}
      setLoadedKey(key);
    }, 0);
    return () => window.clearTimeout(timer);
  }, [key]);
  useEffect(() => {
    if (!key || loadedKey !== key || !dirty || leaving.current) return;
    try { sessionStorage.setItem(key, JSON.stringify(snapshot)); } catch {}
  }, [key, loadedKey, dirty, snapshot]);
  useEffect(() => {
    if (!dirty) return;
    const unload = (e: BeforeUnloadEvent) => { if (!leaving.current) { e.preventDefault(); e.returnValue = ""; } };
    const click = (e: MouseEvent) => { const anchor = e.target instanceof Element ? e.target.closest("a[href]") : null; if (anchor && !leaving.current && !e.ctrlKey && !e.metaKey && !window.confirm("资料尚未保存，确认离开？草稿会保留。")) { e.preventDefault(); e.stopPropagation(); } };
    window.addEventListener("beforeunload", unload); document.addEventListener("click", click, true);
    return () => { window.removeEventListener("beforeunload", unload); document.removeEventListener("click", click, true); };
  }, [dirty]);
  function clear() { if (key) { try { sessionStorage.removeItem(key); } catch {} } setDraft(null); }
  return { draft: loadedKey === key ? draft : null, clear, leave: () => { leaving.current = true; clear(); } };
}
export function CustomerDraftNotice({ available, restore, discard, disabled }: { available: boolean; restore: () => void; discard: () => void; disabled: boolean }) { return available ? <div role="status" className="flex flex-wrap items-center gap-2 rounded border p-3 text-sm"><span>发现未保存的资料草稿；密码需重新填写。</span><Button type="button" variant="outline" disabled={disabled} onClick={restore}>恢复草稿</Button><Button type="button" variant="outline" disabled={disabled} onClick={discard}>丢弃草稿</Button></div> : null; }
