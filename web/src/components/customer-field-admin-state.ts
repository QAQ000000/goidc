"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError, type User } from "@/lib/api";
import { can } from "@/lib/admin-navigation";

export type CustomerAdminField = { id: number; field_key: string; label: string; field_type: string; description: string; validation_pattern: string; choices: string[]; sort_order: number; admin_only: boolean; required: boolean; show_on_register: boolean; active: boolean; version: number };
export type FieldForm = Omit<CustomerAdminField, "id" | "version"> & { id?: number; version?: number; choicesText: string };
export type FieldDraft = { form: FieldForm; original: FieldForm };
export type FieldRequest = { path: string; method: "POST" | "PATCH" | "DELETE"; key: string; body: string; draftKey: string; label: string };
type Pending = { request: FieldRequest; rejected: boolean; reason: string };
export const fieldTypes: Record<string, string> = { text: "单行文本", textarea: "多行文本", number: "数字", date: "日期", select: "下拉选择", boolean: "开关" };
export function emptyFieldForm(): FieldForm { return { field_key: "", label: "", field_type: "text", description: "", validation_pattern: "", choices: [], choicesText: "", sort_order: 0, admin_only: false, required: false, show_on_register: false, active: true }; }
export function fieldForm(field: CustomerAdminField): FieldForm {
  return { id: field.id, version: field.version, field_key: field.field_key, label: field.label, field_type: field.field_type, description: field.description, validation_pattern: field.validation_pattern, choices: field.choices, choicesText: field.choices.join(","), sort_order: field.sort_order, admin_only: field.admin_only, required: field.required, show_on_register: field.show_on_register, active: field.active };
}
export function formPayload(form: FieldForm) {
  const choices = form.field_type === "select" ? form.choicesText.split(",").map(value => value.trim()) : [];
  if (!/^[a-z][a-z0-9_]{1,63}$/.test(form.field_key) || !form.label.trim() || Array.from(form.label.trim()).length > 120) throw new Error("请填写有效字段标识及名称（最多120字）");
  if (Array.from(form.description).length > 500 || new TextEncoder().encode(form.validation_pattern).length > 500) throw new Error("描述最多500字，Go正则最多500字节");
  if (!Number.isSafeInteger(form.sort_order) || form.sort_order < -100000 || form.sort_order > 100000) throw new Error("显示排序需为-100000到100000的整数");
  if (choices.some(choice => !choice || Array.from(choice).length > 120) || new Set(choices).size !== choices.length) throw new Error("下拉选项须非空、不重复且每项最多120字，请使用英文逗号分隔");
  return { field_key: form.field_key, label: form.label, field_type: form.field_type, description: form.description, validation_pattern: form.validation_pattern, choices, sort_order: form.sort_order, admin_only: form.admin_only, required: form.required, show_on_register: form.show_on_register, active: form.active, ...(form.id ? { expected_version: form.version } : {}) };
}
function validForm(value: unknown): value is FieldForm {
  if (!value || typeof value !== "object") return false;
  const v = value as FieldForm;
  return Object.keys(v).every(key => ["field_key","label","field_type","description","validation_pattern","choices","choicesText","sort_order","admin_only","required","show_on_register","active","id","version"].includes(key)) && ["field_key", "label", "description", "validation_pattern", "choicesText"].every(key => typeof (v as unknown as Record<string, unknown>)[key] === "string") && Object.hasOwn(fieldTypes,v.field_type) && Array.isArray(v.choices) && v.choices.every(choice => typeof choice === "string") && Number.isSafeInteger(v.sort_order) && [v.admin_only,v.required,v.show_on_register,v.active].every(value => typeof value === "boolean") && (v.id === undefined ? v.version === undefined : Number.isSafeInteger(v.id) && v.id > 0 && Number.isSafeInteger(v.version) && v.version! > 0);
}
function validField(value: unknown): value is CustomerAdminField {
  if (!value || typeof value !== "object") return false;
  const field = value as CustomerAdminField;
  return Number.isSafeInteger(field.id) && field.id > 0 && Number.isSafeInteger(field.version) && field.version > 0 && Array.isArray(field.choices) && validForm(fieldForm(field));
}
function validPending(value: unknown): value is Pending {
  if (!value || typeof value !== "object") return false;
  const item = value as Pending, r = item.request;
  if (Object.keys(item).length !== 3 || !Object.keys(item).every(key => ["request","rejected","reason"].includes(key))) return false;
  if (!r || Object.keys(r).length !== 6 || !Object.keys(r).every(key => ["path","method","key","body","draftKey","label"].includes(key))) return false;
  if (!r || typeof r.key !== "string" || !/^[A-Za-z0-9._:-]{8,128}$/.test(r.key) || typeof r.body !== "string" || typeof r.label !== "string" || typeof r.draftKey !== "string" || typeof item.rejected !== "boolean" || typeof item.reason !== "string") return false;
  if (!(r.method === "POST" && r.path === "/admin/customer-fields" && r.draftKey === "new" || ["PATCH", "DELETE"].includes(r.method) && /^\/admin\/customer-fields\/[1-9]\d*$/.test(r.path) && r.draftKey === r.path.split("/").at(-1))) return false;
  if (r.method !== "POST" && !Number.isSafeInteger(Number(r.draftKey))) return false;
  const body = JSON.parse(r.body) as Record<string, unknown>;
  if (!body || typeof body !== "object" || Array.isArray(body)) return false;
  const allowed = r.method === "DELETE" ? ["expected_version"] : ["field_key","label","field_type","description","validation_pattern","choices","sort_order","admin_only","required","show_on_register","active",...(r.method === "PATCH" ? ["expected_version"] : [])];
  if (Object.keys(body).length !== allowed.length || !Object.keys(body).every(key => allowed.includes(key)) || (r.method !== "POST" && (!Number.isSafeInteger(body.expected_version) || Number(body.expected_version) < 1))) return false;
  if (r.method === "DELETE") return true;
  if (!Array.isArray(body.choices)) return false;
  const candidate = { ...body, choicesText: body.choices.join(",") };
  delete (candidate as Record<string, unknown>).expected_version;
  if (!validForm(candidate)) return false;
  const payload = formPayload(candidate);
  return JSON.stringify(payload.choices) === JSON.stringify(body.choices);
}

export function useCustomerFieldAdminState(actor: number, allowed: boolean) {
  const pendingKey = `zjmfgo:customer-field-admin:pending:${actor}`;
  const draftsKey = `zjmfgo:customer-field-admin:drafts:${actor}`;
  const [pending, setPending] = useState<Pending | null>(null);
  const [drafts, setDrafts] = useState<Record<string, FieldDraft>>({});
  const [ready, setReady] = useState(false), [storageError, setStorageError] = useState("");
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  const [identityReady, setIdentityReady] = useState(false), [reviewed, setReviewed] = useState(false);
  const [receipt, setReceipt] = useState("");
  const [storageRevision, setStorageRevision] = useState(0);
  const running = useRef(false), mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  useEffect(() => {
    const timer = window.setTimeout(() => {
      setReady(false); setStorageError("");
      try {
        const rawPending = sessionStorage.getItem(pendingKey), rawDrafts = sessionStorage.getItem(draftsKey);
        const p: unknown = rawPending ? JSON.parse(rawPending) : null;
        const d: unknown = rawDrafts ? JSON.parse(rawDrafts) : {};
        if (p !== null && !validPending(p)) throw new Error();
        if (!d || typeof d !== "object" || Array.isArray(d) || !Object.entries(d).every(([key, draft]) => {
          const v = draft as FieldDraft;
          return !!v && Object.keys(v).length === 2 && Object.keys(v).every(key => ["form","original"].includes(key)) && validForm(v.form) && validForm(v.original) && key === String(v.form.id ?? "new") && v.form.id === v.original.id && v.form.version === v.original.version && v.form.field_key === (v.original.id ? v.original.field_key : v.form.field_key);
        })) throw new Error();
        setPending(p as Pending | null); setDrafts(d as Record<string, FieldDraft>); setReviewed(false); setReady(true);
      } catch { setStorageError("本员工的字段草稿或原请求无法读取，已暂停新写入。请核实恢复资料，不要清空后重复提交。"); }
    }, 0);
    return () => window.clearTimeout(timer);
  }, [pendingKey, draftsKey, storageRevision]);
  const verify = useCallback(async (signal?: AbortSignal) => {
    try {
      const user = await api<User>("/me", { signal });
      if (user.id !== actor || !can(user, "customers.manage")) throw new Error("员工身份或客户配置权限已变化，请使用原员工账户重新打开页面；原资料保留。");
      if (!mounted.current || signal?.aborted) throw new Error("页面已离开，原资料保留");
      setIdentityReady(true);
      return user;
    } catch (e) { if (mounted.current && !signal?.aborted) setIdentityReady(false); setReviewed(false); throw e; }
  }, [actor]);
  const read = useCallback(async <T,>(path: string, signal?: AbortSignal) => {
    await verify(signal);
    try {
      const result = await api<T>(path, { signal });
      await verify(signal);
      if (result === undefined || result === null) throw new Error("字段资料响应不完整，请重新读取");
      if (path.startsWith("/admin/customer-fields?")) {
        const page = result as { items?: unknown; total?: number; limit?: number; offset?: number };
        if (!Array.isArray(page.items) || !page.items.every(validField) || !Number.isSafeInteger(page.total) || page.total! < 0 || !Number.isSafeInteger(page.limit) || page.limit! < 1 || page.limit! > 100 || !Number.isSafeInteger(page.offset) || page.offset! < 0 || page.items.length > page.limit!) throw new Error("字段列表响应不完整，请重新读取");
      } else if (!validField(result)) throw new Error("字段详情响应不完整，请重新读取");
      return result;
    } catch (e) { if (e instanceof ApiError && [401,403].includes(e.status)) { setIdentityReady(false); setReviewed(false); } setReviewed(false); throw e; }
  }, [verify]);
  function persistDraft(key: string, draft: FieldDraft) {
    if (!ready || !allowed || !identityReady || !mounted.current) return false;
    const next = { ...drafts, [key]: draft };
    setDrafts(next);
    try { sessionStorage.setItem(draftsKey, JSON.stringify(next)); return true; }
    catch { setReady(false); setStorageError("浏览器无法保存字段草稿，已暂停新写入；当前内存输入保留。请检查浏览器存储。"); return false; }
  }
  async function replaceDraft(key: string, draft: FieldDraft | null) {
    if (!ready || !allowed) return false;
    try { await verify(); } catch (e) { setError((e as Error).message); return false; }
    if (!mounted.current) return false;
    const next = { ...drafts };
    if (draft) next[key] = draft; else delete next[key];
    try { sessionStorage.setItem(draftsKey, JSON.stringify(next)); setDrafts(next); return true; }
    catch { setReady(false); setStorageError("浏览器无法保存字段草稿，已暂停新写入；当前内存输入保留。请检查浏览器存储。"); return false; }
  }
  function savePending(item: Pending) {
    sessionStorage.setItem(pendingKey, JSON.stringify(item));
    setPending(item);
  }
  async function send(request: FieldRequest, original = false): Promise<boolean> {
    if (!allowed || !ready || running.current || (!original && pending) || (original && pending?.rejected && !reviewed)) return false;
    running.current = true; setBusy(true); setError(""); setReceipt("");
    let dispatched = false;
    try {
      await verify();
      if (!mounted.current) return false;
      const item = { request, rejected: false, reason: "" };
      setReviewed(false);
      try { savePending(item); }
      catch { if (original) setPending(item); if (!original) { setReady(false); setStorageError("浏览器无法保存原请求，已停止发送新写入。请检查存储后重新读取恢复资料。"); throw new Error("原请求尚未发送"); } }
      dispatched = true;
      const result = await api<{ id: number; version?: number; deleted?: boolean }>(request.path, { method: request.method, body: request.body, headers: { "Idempotency-Key": request.key } });
      const expectedId = request.method === "POST" ? null : Number(request.path.split("/").at(-1));
      if (!result || !Number.isSafeInteger(result.id) || result.id < 1 || expectedId !== null && result.id !== expectedId || (request.method === "DELETE" ? result.deleted !== true : !Number.isSafeInteger(result.version) || result.version! < 1)) throw new Error("操作回执不完整，结果尚未确认");
      await verify();
      if (!mounted.current) return false;
      const nextDrafts = { ...drafts }; delete nextDrafts[request.draftKey];
      // Keep the original request if either cleanup fails; replay remains safe.
      sessionStorage.setItem(draftsKey, JSON.stringify(nextDrafts));
      sessionStorage.removeItem(pendingKey);
      setDrafts(nextDrafts); setPending(null); setReviewed(false);
      setReceipt("原操作回执已确认，相关字段配置已刷新，不会重复执行。");
      return true;
    } catch (failure) {
      if (!mounted.current) return false;
      const e = failure as Error;
      if (e instanceof ApiError && [401,403].includes(e.status)) { setIdentityReady(false); setReviewed(false); }
      if (dispatched && e instanceof ApiError && e.status >= 400 && e.status < 500 && ![401,403,404,408,429].includes(e.status)) {
        const rejected = { request, rejected: true, reason: e.message };
        setPending(rejected); setReviewed(false);
        try { sessionStorage.setItem(pendingKey, JSON.stringify(rejected)); } catch { setStorageError("无法保存拒绝状态；原请求仍保留，结束前须重新核对。"); }
        setError(`${e.message}。原请求和草稿保留，请显式读取最新事实后处理，不会自动换键或提交。`);
      } else setError(`${e.message}。${dispatched ? "结果尚需核实，请保留原请求，恢复身份或权限后核实。" : "未发送写入；原员工空间的资料保留。"}`);
      return false;
    } finally { running.current = false; if (mounted.current) setBusy(false); }
  }
  async function endRejected() {
    if (!pending?.rejected || !reviewed || running.current || busy || !window.confirm("已核对明确拒绝与最新事实，确认结束此被拒绝请求？输入草稿保留，重新提交前仍须再次确认。")) return;
    running.current = true; setBusy(true);
    try {
      try { await verify(); } catch (e) { setReviewed(false); setError((e as Error).message); return; }
      if (!mounted.current) return;
      try { sessionStorage.removeItem(pendingKey); setPending(null); setReviewed(false); setError(""); }
      catch { setStorageError("无法清理被拒绝请求，已暂停新写入。"); setReady(false); }
    } finally { running.current = false; if (mounted.current) setBusy(false); }
  }
  useEffect(() => {
    if (!pending && !Object.keys(drafts).length && !busy) return;
    const unload = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    const click = (event: MouseEvent) => {
      const link = event.target instanceof Element ? event.target.closest("a[href]") : null;
      if (link && !event.ctrlKey && !event.metaKey && !window.confirm("当前有未提交草稿或待核实请求。确认离开？资料保留在原员工空间。")) { event.preventDefault(); event.stopPropagation(); }
    };
    // The Navigation API runs before same-document history traversal. Cancel it
    // there; popstate itself cannot be canceled without rewriting user history.
    const navigation = (window as Window & { navigation?: EventTarget }).navigation;
    const navigate = (event: Event) => {
      const transition = event as Event & { navigationType?: string; destination?: { url: string; sameDocument: boolean } };
      if (transition.navigationType !== "traverse" || !transition.destination?.sameDocument || !event.cancelable) return;
      const destination = new URL(transition.destination.url);
      if (destination.pathname === window.location.pathname) return;
      if (!window.confirm("当前有未提交草稿或待核实请求。确认离开？资料保留在原员工空间。")) event.preventDefault();
    };
    window.addEventListener("beforeunload", unload); document.addEventListener("click", click, true); navigation?.addEventListener("navigate", navigate);
    return () => { window.removeEventListener("beforeunload", unload); document.removeEventListener("click", click, true); navigation?.removeEventListener("navigate", navigate); };
  }, [pending, drafts, busy]);
  const hideIdentity = useCallback(() => { setIdentityReady(false); setReviewed(false); }, []);
  return { pending, drafts, ready, storageError, busy, error, setError, identityReady, hideIdentity, reviewed, setReviewed, receipt, read, verify, persistDraft, replaceDraft, send, endRejected, running, reloadStorage: () => setStorageRevision(value => value + 1) };
}
