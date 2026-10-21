"use client";

import { useEffect, useRef, useState, type ReactNode, type RefObject } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { useResource } from "@/hooks/use-resource";
import { api, type User } from "@/lib/api";
import { can } from "@/lib/admin-navigation";
import { Button } from "./ui/button";
import { Failure, Loading } from "./common";
import { useAdminIdentity } from "./workspace";

type Domain = "order" | "invoice" | "ticket";
export type CreateCustomer = { id: number; name: string; username: string; display_name: string; email: string; status: string };
type ParsedContext = { customerId: number | null; returnTo: string; query: string; error: string | null };
export type AdminCustomerCreateContext = ParsedContext & { actorId: number; actorIdentity: string; customer: CreateCustomer | null; setLocked: (locked: boolean) => void };

export function safeAdminCreateReturn(value: string, customerId: number | null) {
  if (!value.startsWith("/admin/") || /[\\\u0000-\u0020\u007f]/.test(value)) return null;
  const pathname = value.split(/[?#]/)[0];
  const customer = /^\/admin\/customers\/([1-9]\d*)(?:\/files)?$/.exec(pathname);
  if (customer) return Number.isSafeInteger(Number(customer[1])) && (!customerId || Number(customer[1]) === customerId) ? value : null;
  return /^\/admin\/(?:orders|invoices|tickets|suppliers\/orders)$/.test(pathname) ? value : null;
}

export function parseAdminCustomerCreateContext(query: string, fallback: string): ParsedContext {
  const params = new URLSearchParams(query);
  const ids = params.getAll("customer_id");
  let customerId: number | null = null;
  let error: string | null = null;
  if (ids.length) {
    if (ids.length !== 1 || !/^[1-9]\d*$/.test(ids[0]) || !Number.isSafeInteger(Number(ids[0]))) error = "客户参数无效，请从客户详情重新进入；不会改为全部客户。";
    else customerId = Number(ids[0]);
  }
  const returns = params.getAll("return_to");
  const requested = returns.length === 1 ? safeAdminCreateReturn(returns[0], customerId) : null;
  if (returns.length && !requested) error = error ?? "返回地址无效，请从客户详情或业务列表重新进入。";
  return { customerId, query, returnTo: requested ?? (customerId ? `/admin/customers/${customerId}` : fallback), error };
}

const permissions = { order: "orders.manage", invoice: "invoices.manage", ticket: "tickets.manage" };
const optionPaths = { order: "/admin/orders/create-options?kind=customers", invoice: "/admin/invoice-customers?", ticket: "/admin/tickets/create-options?kind=customers" };
function actorIdentity(user: User) { return JSON.stringify([user.id, user.role, [...(user.permissions ?? [])].sort(), user.customer_scope, user.customer_ownerless, user.ticket_scope, user.ticket_unassigned]); }
export async function assertCreateActor(context: AdminCustomerCreateContext, domain: Domain) {
  const user = await api<User>("/me", { portal: "admin" });
  if (actorIdentity(user) !== context.actorIdentity || !can(user, permissions[domain]) || (domain === "invoice" && !can(user, "invoices.view"))) throw new Error("管理员身份或权限已变化，已阻止发送。请重新读取身份；未确认的原请求仍需核对。");
}
export function validCreateResult(domain: Domain, value: unknown) {
  if (!value || typeof value !== "object") return false;
  const result = value as Record<string, unknown>;
  const id = result[domain === "order" ? "order_id" : domain === "invoice" ? "invoice_id" : "id"];
  return typeof id === "number" && Number.isSafeInteger(id) && id > 0;
}
function intentKey(context: Pick<AdminCustomerCreateContext, "actorId" | "customerId">, domain: Domain) { return `zjmfgo:admin-create-intent:${domain}:${context.actorId}:${context.customerId ?? "global"}`; }
type CreateIntent = { key: string; body?: string; customerId: number | null; query: string; returnTo: string; sensitive: boolean; createdAt: string };
export function rememberCreateIntent(context: AdminCustomerCreateContext, domain: Domain, request: { key: string; body: string }, sensitive = false) {
  const payload = JSON.parse(request.body) as Record<string, unknown>;
  const customerId = payload[domain === "invoice" ? "user_id" : "customer_id"];
  if (typeof customerId !== "number" || !Number.isSafeInteger(customerId) || customerId <= 0 || (context.customerId && customerId !== context.customerId)) throw new Error("请求客户不一致，已停止发送");
  const record: CreateIntent = { key: request.key, customerId, query: context.query, returnTo: context.returnTo, sensitive, createdAt: new Date().toISOString(), ...(!sensitive ? { body: request.body } : {}) };
  try { sessionStorage.setItem(intentKey(context, domain), JSON.stringify(record)); } catch { throw new Error("浏览器无法保存请求核对标记，已停止发送。请允许此站点使用会话存储后再试。"); }
}
export function validStoredCreateIntent(value: unknown, context: Pick<AdminCustomerCreateContext, "customerId">, domain: Domain): value is CreateIntent {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const record = value as CreateIntent;
  const prefix = domain === "order" ? "admin-order" : domain === "invoice" ? "manual-save" : "admin-ticket";
  if (typeof record.key !== "string" || !new RegExp(`^${prefix}-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, "i").test(record.key)) return false;
  if (!Number.isSafeInteger(record.customerId) || (record.customerId ?? 0) <= 0 || context.customerId !== null && record.customerId !== context.customerId) return false;
  if (typeof record.query !== "string" || typeof record.returnTo !== "string" || !safeAdminCreateReturn(record.returnTo, record.customerId) || typeof record.sensitive !== "boolean" || typeof record.createdAt !== "string" || !Number.isFinite(Date.parse(record.createdAt))) return false;
  const parsed = parseAdminCustomerCreateContext(record.query, `/admin/${domain === "order" ? "orders" : domain === "invoice" ? "invoices" : "tickets"}`);
  if (parsed.error || parsed.customerId !== context.customerId || parsed.returnTo !== record.returnTo) return false;
  if (record.sensitive) return record.body === undefined;
  if (typeof record.body !== "string" || record.body.length > 2_000_000) return false;
  try {
    const body = JSON.parse(record.body);
    if (!body || typeof body !== "object" || Array.isArray(body) || body[domain === "invoice" ? "user_id" : "customer_id"] !== record.customerId) return false;
    if (domain === "ticket" || domain === "order" && (!Array.isArray(body.items) || body.items.some((item: { purchase_fields?: unknown }) => item.purchase_fields && Object.keys(item.purchase_fields).length > 0))) return false;
    return true;
  } catch { return false; }
}
export function forgetCreateIntent(context: AdminCustomerCreateContext, domain: Domain, key: string) {
  try { const stored = JSON.parse(sessionStorage.getItem(intentKey(context, domain)) ?? "null"); if (stored?.key === key) sessionStorage.removeItem(intentKey(context, domain)); } catch { /* Keep an unreadable marker for manual review. */ }
}

export function AdminCustomerCreateBoundary({ domain, fallback, children }: { domain: Domain; fallback: string; children: (context: AdminCustomerCreateContext) => ReactNode }) {
  const user = useAdminIdentity();
  const params = useSearchParams();
  if (!user) return <p role="alert">管理员身份不可用，请重新读取身份后再创建。</p>;
  if (!can(user, permissions[domain]) || (domain === "invoice" && !can(user, "invoices.view"))) return <p role="alert">没有创建此业务的权限。</p>;
  return <CreateContextForActor key={actorIdentity(user)} actorId={user.id} actorIdentity={actorIdentity(user)} domain={domain} incoming={parseAdminCustomerCreateContext(params.toString(), fallback)}>{children}</CreateContextForActor>;
}

function CreateContextForActor({ actorId, actorIdentity, domain, incoming, children }: { actorId: number; actorIdentity: string; domain: Domain; incoming: ParsedContext; children: (context: AdminCustomerCreateContext) => ReactNode }) {
  const [locked, setLocked] = useState<ParsedContext | null>(null);
  const context = locked ?? incoming;
  const path = !context.error && context.customerId ? `${optionPaths[domain]}${domain === "invoice" ? "" : "&"}customer_id=${context.customerId}&limit=1&offset=0` : null;
  const resource = useResource<{ items: Partial<CreateCustomer>[] }>(path, false);
  const candidate = resource.data?.items?.length === 1 ? resource.data.items[0] : null;
  const customer = candidate?.id === context.customerId && typeof candidate.email === "string" ? {
    id: candidate.id, email: candidate.email, name: candidate.name || candidate.display_name || candidate.username || `客户 #${candidate.id}`,
    username: candidate.username ?? "", display_name: candidate.display_name || candidate.name || candidate.username || "", status: candidate.status ?? "active",
  } as CreateCustomer : null;
  const changed = locked && incoming.query !== locked.query;
  if (context.error) return <p role="alert" className="text-sm text-destructive">{context.error}</p>;
  return <>
    {changed && <p role="alert" className="mb-4 rounded border p-3 text-sm">当前操作尚在提交或等待核对，继续保留原客户和原请求。请先完成当前操作，再进入地址中的其他客户。</p>}
    <CreateIntentRecovery key={`${actorId}:${context.query}`} domain={domain} context={{ ...context, actorId, actorIdentity, customer, setLocked: value => setLocked(value ? context : null) }}>{value => resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : path && (!resource.data || resource.isLoading) ? <Loading /> : context.customerId && !customer ? <div className="space-y-3"><p role="alert" className="text-sm text-destructive">指定客户不可用于此操作，可能已停用或不在当前范围；不会改为全部客户。</p><Button type="button" variant="outline" onClick={resource.refresh}>重新核对客户</Button></div> : children(value)}</CreateIntentRecovery>
  </>;
}

function CreateIntentRecovery({ context, domain, children }: { context: AdminCustomerCreateContext; domain: Domain; children: (context: AdminCustomerCreateContext) => ReactNode }) {
  const router = useRouter();
  const [record, setRecord] = useState<CreateIntent | null | undefined>(undefined);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [invalid, setInvalid] = useState(false);
  const active = useCreateActivity();
  const storageKey = intentKey(context, domain);
  const { customerId } = context;
  useEffect(() => {
    let next: CreateIntent | null = null;
    let malformed = false;
    try { const raw = sessionStorage.getItem(storageKey); if (raw !== null) { const value: unknown = JSON.parse(raw); if (validStoredCreateIntent(value, { customerId }, domain)) next = value; else malformed = true; } }
    catch { malformed = true; }
    const timer = window.setTimeout(() => { setInvalid(malformed); setRecord(next); }, 0);
    return () => window.clearTimeout(timer);
  }, [storageKey, customerId, domain]);
  async function retry() {
    if (!record?.body || busy) return;
    setBusy(true); setError(""); context.setLocked(true);
    try {
      await assertCreateActor(context, domain); if (!active.current) return;
      const result = await api<Record<string, number>>(`/admin/${domain === "order" ? "orders" : domain === "invoice" ? "invoices" : "tickets"}`, { method: "POST", body: record.body, headers: { "Idempotency-Key": record.key } });
      if (!active.current) return;
      if (!validCreateResult(domain, result)) throw new Error("响应缺少有效业务编号，原请求仍需核对");
      await assertCreateActor(context, domain); if (!active.current) return;
      forgetCreateIntent(context, domain, record.key);
      router.push(`/admin/${domain === "order" ? "orders" : domain === "invoice" ? "invoices" : "tickets"}/${result[domain === "order" ? "order_id" : domain === "invoice" ? "invoice_id" : "id"]}`); router.refresh();
    } catch (e) { if (active.current) setError((e as Error).message); } finally { if (active.current) setBusy(false); }
  }
  if (record === undefined) return <Loading />;
  if (invalid) return <p role="alert" className="text-sm text-destructive">原请求核对记录损坏或客户不匹配，已阻止发送和重新创建。请先人工核对业务结果。</p>;
  if (!record) return children(context);
  return <section className="space-y-4 rounded border p-4"><p role="alert">此客户仍有未确认的创建请求，已阻止重新创建。请先核对原请求结果。</p>{context.customer ? <FixedCreateCustomer customer={context.customer} /> : <p>客户 #{record.customerId}</p>}<p className="break-all text-sm">原请求：{record.key}</p>{record.body ? <Button type="button" disabled={busy} onClick={retry}>{busy ? "核对中…" : "使用原请求核对"}</Button> : <p className="text-sm text-muted-foreground">原内容可能含凭据，仅保留在原页面内存中。请返回仍打开的原页面重试；刷新后请在业务列表核对结果，不能自动换新请求。</p>}<Button type="button" disabled={busy} variant="outline" onClick={async () => { if (window.confirm("请确认已在业务列表核实原请求结果，且不会重复创建。解除后仍请勿重新提交已创建的业务。")) { setBusy(true); try { await assertCreateActor(context, domain); if (!active.current) return; sessionStorage.removeItem(storageKey); setRecord(null); context.setLocked(false); } catch (e) { if (active.current) setError((e as Error).message); } finally { if (active.current) setBusy(false); } } }}>已人工核对结果，解除阻断</Button>{error && <p role="alert" className="text-sm text-destructive">{error}</p>}</section>;
}

export function FixedCreateCustomer({ customer }: { customer: CreateCustomer }) {
  return <div className="space-y-1 rounded border bg-muted/40 p-3 text-sm" aria-label="固定客户"><p className="font-medium">{customer.name} · #{customer.id}</p><p className="break-all text-muted-foreground">{customer.email}</p><p className="text-xs text-muted-foreground">从客户详情进入，客户已固定，不能更换或清空。{customer.status !== "active" ? " 此客户已停用。" : ""}</p></div>;
}

export function useCreateActivity() {
  const active = useRef(true);
  useEffect(() => { active.current = true; return () => { active.current = false; }; }, []);
  return active;
}

export function useCreateLeaveGuard(dirty: boolean, pending: boolean, leaving: RefObject<boolean>, label: string) {
  useEffect(() => {
    if (!dirty && !pending) return;
    const unload = (event: BeforeUnloadEvent) => { if (!leaving.current) { event.preventDefault(); event.returnValue = ""; } };
    const click = (event: MouseEvent) => {
      const link = event.target instanceof Element ? event.target.closest("a[href]") : null;
      if (!link || leaving.current || event.ctrlKey || event.metaKey) return;
      if (pending || !window.confirm(`${label}尚未保存，确认离开？`)) { event.preventDefault(); event.stopPropagation(); }
    };
    window.addEventListener("beforeunload", unload); document.addEventListener("click", click, true);
    return () => { window.removeEventListener("beforeunload", unload); document.removeEventListener("click", click, true); };
  }, [dirty, pending, leaving, label]);
}
