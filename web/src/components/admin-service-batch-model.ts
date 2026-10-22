import { digest, record, reasonText, whole, type ServiceCommand } from "./admin-service-command";

export type BatchItem = { service_id: number; expected_version: number; quote_hash?: string; invoice_id?: number; amount_cents?: number };
export type Manifest = { customer_id: number; action: "create" | "pay_balance"; reason: string; items: BatchItem[] };
export type BatchProgress = Omit<Manifest, "items"> & { batch_id: string; items: (BatchItem & { status: string; code?: string; error?: string; result?: { invoice_id: number; invoice_no: string; service_id: number; customer_id: number; amount_cents: number; status: string; renewal_status?: string } })[]; succeeded: number; failed: number; unconfirmed: number; pending: number; replayed: boolean };
const base = "/admin/service-renewal-batches";
export function validManifest(v: unknown): v is Manifest {
  if (!record(v) || Object.keys(v).length !== 4 || !whole(v.customer_id, 1) || !["create", "pay_balance"].includes(String(v.action)) || !reasonText(v.reason, v.action === "create" ? 240 : 500) || !Array.isArray(v.items) || !v.items.length || v.items.length > 20) return false;
  return v.items.every(i => record(i) && whole(i.service_id, 1) && whole(i.expected_version, 1) && (v.action === "create" ? Object.keys(i).length === 3 && digest(i.quote_hash) : Object.keys(i).length === 4 && whole(i.invoice_id, 1) && whole(i.amount_cents))) && new Set(v.items.map(i => i.service_id)).size === v.items.length && (v.action === "create" || new Set(v.items.map(i => i.invoice_id)).size === v.items.length);
}
export function validBatchCommand(v: unknown): v is ServiceCommand {
  if (!record(v) || !record(v.body) || !record(v.context) || !validManifest(v.context.manifest)) return false;
  if (v.path === base) return JSON.stringify(v.body) === JSON.stringify(v.context.manifest);
  const ids = new Set(v.context.manifest.items.map(i => i.service_id));
  return digest(v.context.batch_id) && v.path === `${base}/${v.context.batch_id}/execute` && Object.keys(v.body).length === 1 && Array.isArray(v.body.service_ids) && v.body.service_ids.length > 0 && v.body.service_ids.length <= 20 && new Set(v.body.service_ids).size === v.body.service_ids.length && v.body.service_ids.every(id => ids.has(id));
}
export function validBatchProgress(v: unknown, command: ServiceCommand): v is BatchProgress {
  const m = command.context.manifest as Manifest;
  if (!record(v) || !digest(v.batch_id) || command.context.batch_id && v.batch_id !== command.context.batch_id || v.customer_id !== m.customer_id || v.action !== m.action || v.reason !== m.reason || typeof v.replayed !== "boolean" || !Array.isArray(v.items) || v.items.length !== m.items.length) return false;
  const counts = { succeeded: 0, failed: 0, unconfirmed: 0, pending: 0 };
  for (let n = 0; n < v.items.length; n++) {
    const i = v.items[n], expected = m.items[n];
    if (!record(i) || !["succeeded", "failed", "unconfirmed", "pending"].includes(String(i.status)) || Object.keys(expected).some(k => i[k] !== expected[k as keyof BatchItem])) return false;
    counts[i.status as keyof typeof counts]++;
    if (i.status === "failed" && (typeof i.code !== "string" || typeof i.error !== "string")) return false;
    if (i.status === "succeeded") {
      const r = i.result;
      if (!record(r) || r.service_id !== i.service_id || r.customer_id !== m.customer_id || !whole(r.invoice_id, 1) || typeof r.invoice_no !== "string" || !whole(r.amount_cents) || (m.action === "pay_balance" ? r.invoice_id !== i.invoice_id || r.amount_cents !== i.amount_cents || r.status !== "paid" || !["pending", "completed"].includes(String(r.renewal_status)) : r.status !== "unpaid")) return false;
    }
  }
  return Object.entries(counts).every(([k, count]) => v[k] === count);
}
