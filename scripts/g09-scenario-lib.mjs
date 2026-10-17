import { createHash } from 'node:crypto';
import { performance } from 'node:perf_hooks';

const hash = text => createHash('sha256').update(text).digest('hex');
const label = value => typeof value === 'string' && /^[a-zA-Z0-9_-]{1,40}$/.test(value);
const id = value => Number.isSafeInteger(value) && value > 0;
const session = value => typeof value === 'string' && /^zjmfgo_session=[a-zA-Z0-9._~-]+$/.test(value);
function requestPath(path, method) {
  if (typeof path !== 'string' || !/^\/api\/v1\/(?:me|cart(?:\/items)?|orders|tickets|catalog\/page|invoices\/page|admin\/(?:invoices\/page|jobs\/queue)|invoices\/[1-9]\d*(?:\/(?:pay|payments))?|services\/[1-9]\d*(?:\/renew)?)(?:\?[^#]*)?$/.test(path)) return false;
  const u = new URL(path, 'http://127.0.0.1:1');
  if (!u.search) return true;
  if (method !== 'GET' || !/(?:\/page|\/queue)$/.test(u.pathname)) return false;
  const seen = new Set();
  for (const [key, value] of u.searchParams) {
    if (seen.has(key) || !['limit', 'offset'].includes(key) || !/^(0|[1-9]\d*)$/.test(value) || !Number.isSafeInteger(Number(value)) || Number(value) > (key === 'limit' ? 100 : 100000) || (key === 'limit' && Number(value) < 1)) return false;
    seen.add(key);
  }
  return true;
}
function origin(value, header = false) {
  let u;
  try { u = new URL(value); } catch { throw new Error('Invalid isolated origin'); }
  if (u.protocol !== 'http:' || !(header ? ['127.0.0.1', 'localhost'] : ['127.0.0.1']).includes(u.hostname) || !u.port || u.origin !== value || u.username || u.password) throw new Error('Explicit isolated HTTP origin required');
  return u.origin;
}
function syncCall(fn, row) {
  const result = fn(Object.freeze(row));
  if (result && typeof result.then === 'function') { result.catch?.(() => {}); throw new Error('Synchronous evidence recorder required'); }
}

export class G09RequestError extends Error {
  constructor(code, status = null, uncertain = false) {
    super(code); this.name = 'G09RequestError'; this.code = code; this.status = status; this.uncertain = uncertain;
  }
}

/** One attempt per actual HTTP request. persistPrivate must durably save the
 * original mutation BEFORE dispatch, in a private file (including its key/body).
 * record receives metadata only. Neither callback may be async. No retry, redirect,
 * credential response, or remote exception text is copied into public evidence.
 * This helper does not establish schema/process/network isolation by itself.
 */
export function createG09APIClient({ apiOrigin, publicOrigin, runId, persistPrivate, record, fetchImpl = fetch, now = () => performance.now(), maxRequests = 50000, maxBodyBytes = 1048576 }) {
  const target = origin(apiOrigin), headerOrigin = origin(publicOrigin, true);
  if (!label(runId) || typeof persistPrivate !== 'function' || typeof record !== 'function' || typeof fetchImpl !== 'function' || !id(maxRequests) || maxRequests > 100000 || !id(maxBodyBytes) || maxBodyBytes > 1048576) throw new Error('Invalid scenario client configuration');
  let sequence = 0;
  const mutations = new Set();
  return async function request({ flowId, step, path, method = 'GET', body, cookie = '', signal }) {
    if (!label(flowId) || !label(step) || !['GET', 'POST'].includes(method) || !requestPath(path, method) || (method === 'GET' && body !== undefined) || typeof cookie !== 'string' || (cookie && !session(cookie))) throw new G09RequestError('invalid_request');
    if (sequence >= maxRequests) throw new G09RequestError('request_budget_exhausted');
    const requestSequence = ++sequence, key = `g09-${runId}-${flowId}-${step}`;
    if (method === 'POST') {
      if (mutations.has(key)) throw new G09RequestError('mutation_already_attempted');
      mutations.add(key);
    }
    const serialized = method === 'POST' ? JSON.stringify(body ?? {}) : undefined;
    if (serialized !== undefined && Buffer.byteLength(serialized) > 65536) throw new G09RequestError('request_body_budget_exceeded');
    const payloadSha256 = hash(serialized ?? ''), keySha256 = method === 'POST' ? hash(key) : null;
    const meta = { requestSequence, flowId, step, method, path, payloadSha256, keySha256 };
    // Recorder failures fail closed. No request has been sent yet.
    if (method === 'POST') syncCall(persistPrivate, { ...meta, key, body: serialized });
    syncCall(record, { event: 'http_started', ...meta });
    const startedAt = now();
    let status = null, uncertain = false, code = null, response;
    try {
      signal?.throwIfAborted();
      const headers = { Accept: 'application/json', Origin: headerOrigin, 'X-Requested-With': 'zjmfgo' };
      if (cookie) headers.Cookie = cookie;
      if (method === 'POST') { headers['Content-Type'] = 'application/json'; headers['Idempotency-Key'] = key; }
      response = await fetchImpl(`${target}${path}`, { method, headers, body: serialized, signal, redirect: 'error' });
      status = response.status;
      const parts = []; let bytes = 0;
      if (response.body) {
        const reader = response.body.getReader();
        try {
          while (true) {
            const part = await reader.read();
            if (part.done) break;
            bytes += part.value.byteLength;
            if (bytes > maxBodyBytes) { await reader.cancel(); throw new G09RequestError('response_budget_exceeded', status, method === 'POST'); }
            parts.push(Buffer.from(part.value));
          }
        } finally { reader.releaseLock(); }
      }
      signal?.throwIfAborted();
      if (status < 200 || status >= 300) throw new G09RequestError('http_rejected', status, method === 'POST' && status >= 500);
      let parsed;
      try { parsed = JSON.parse(Buffer.concat(parts).toString('utf8')); } catch { throw new G09RequestError('invalid_response', status, method === 'POST'); }
      if (!parsed || typeof parsed !== 'object' || !Object.hasOwn(parsed, 'data') || Object.hasOwn(parsed, 'error')) throw new G09RequestError('invalid_response', status, method === 'POST');
      return { status, data: parsed.data };
    } catch (error) {
      const safe = error instanceof G09RequestError ? error : new G09RequestError('transport_failed', status, method === 'POST');
      code = safe.code; uncertain = safe.uncertain; throw safe;
    } finally {
      syncCall(record, { event: 'http_finished', ...meta, status, succeeded: code === null, code, uncertain, startedAt, endedAt: now() });
    }
  };
}

/** Executes an explicitly allocated business flow. Allocation, rate scheduling,
 * candidate identity, queue drain, and SQL assertions belong to the controller.
 * Success means the HTTP chain completed; provision/notification delivery and
 * accounting still require independent worker/SQL evidence.
 */
export function createG09ScenarioExecutor({ request, runId, accounts, maxFlows = 10000 }) {
  if (typeof request !== 'function' || !label(runId) || !id(maxFlows) || maxFlows > 100000) throw new Error('Invalid scenario executor');
  if (!Array.isArray(accounts) || !accounts.length || accounts.length > 2000 || accounts.some(a => !id(a.id) || !id(a.budgetCents) || !session(a.cookie)) || new Set(accounts.map(a => a.id)).size !== accounts.length || new Set(accounts.map(a => a.cookie)).size !== accounts.length) throw new Error('Frozen unique customer identities and budgets required');
  const budgets = new Map(accounts.map(a => [a.id, { cookie: a.cookie, remaining: a.budgetCents, writes: 0 }]));
  const seen = new Set(), usedInvoices = new Set(), busyCustomers = new Set(), busyServices = new Set(), quarantinedCustomers = new Set(), quarantinedServices = new Set();
  const stopController = new AbortController();
  const execute = async function execute({ flow, signal }) {
    if (stopController.signal.aborted) throw new G09RequestError('scenario_stopped');
    if (!flow || !label(flow.id) || !id(flow.customerId) || typeof flow.cookie !== 'string' || !flow.cookie || !['balance_pay', 'local_renew', 'purchase_local', 'purchase_supplier', 'ticket'].includes(flow.kind)) throw new G09RequestError('invalid_flow');
    const f = structuredClone(flow);
    const combinedSignal = signal ? AbortSignal.any([signal, stopController.signal]) : stopController.signal;
    combinedSignal.throwIfAborted();
    if (seen.has(f.id) || seen.size >= maxFlows) throw new G09RequestError('flow_budget_or_replay');
    seen.add(f.id);
    const purchase = f.kind.startsWith('purchase_');
    if (quarantinedCustomers.has(f.customerId) || quarantinedServices.has(f.serviceId)) throw new G09RequestError('resource_quarantined');
    if (busyCustomers.has(f.customerId) || (f.kind === 'local_renew' && busyServices.has(f.serviceId))) throw new G09RequestError('allocation_busy');
    if ((f.kind === 'balance_pay' && !id(f.invoiceId)) || (f.kind === 'local_renew' && (!id(f.serviceId) || !Number.isFinite(Date.parse(f.beforeDue)))) || (purchase && (!id(f.productId) || !Array.isArray(f.optionIds) || !f.optionIds.every(id) || !f.optionQuantities || !f.purchaseFields)) || (f.kind === 'ticket' && !id(f.departmentId))) throw new G09RequestError('invalid_flow_resources');
    const amount = f.kind === 'ticket' ? 0 : f.amountCents;
    const budget = budgets.get(f.customerId);
    if (!budget || budget.cookie !== f.cookie || budget.writes >= 50 || !Number.isSafeInteger(amount) || amount < 0 || amount > 1000 || (f.kind !== 'ticket' && amount === 0) || budget.remaining < amount) throw new G09RequestError('customer_budget_exhausted');
    if (f.kind === 'balance_pay' && usedInvoices.has(f.invoiceId)) throw new G09RequestError('invoice_already_allocated');
    budget.writes++; budget.remaining -= amount;
    if (f.kind === 'balance_pay') usedInvoices.add(f.invoiceId);
    busyCustomers.add(f.customerId);
    if (f.kind === 'local_renew') busyServices.add(f.serviceId);
    let mutationAttempted = false;
    const call = async (step, path, body) => {
      combinedSignal.throwIfAborted();
      if (body !== undefined) mutationAttempted = true;
      const out = await request({ flowId: f.id, step, path, method: body === undefined ? 'GET' : 'POST', body, cookie: f.cookie, signal: combinedSignal });
      if (!Number.isInteger(out?.status) || out.status < 200 || out.status >= 300) throw new G09RequestError('http_rejected', out?.status ?? null, body !== undefined && (!Number.isInteger(out?.status) || out.status >= 500));
      return out;
    };
    let invoiceId = null, orderId = null, ticketId = null;
    try {
      // Verify the frozen manifest's cookie identity through the actual API,
      // including two distinct sessions accidentally assigned to the same user.
      const identity = (await call('identity', '/api/v1/me')).data;
      if (identity?.id !== f.customerId || identity.role !== 'customer') throw new G09RequestError('customer_identity_mismatch');
      if (f.kind === 'ticket') {
        const result = await call('ticket', '/api/v1/tickets', { subject: `g09-${runId}-${f.id}`, body: 'fixture public body', priority: 'normal', department_id: f.departmentId, idempotency_key: `g09-${runId}-${f.id}-ticket` });
        ticketId = result.data?.id;
        if (!id(ticketId)) throw new G09RequestError('invalid_ticket_receipt', result.status, true);
      } else {
        if (purchase) {
          const cart = (await call('cart-before', '/api/v1/cart')).data;
          if (!id(cart?.version) || !Array.isArray(cart.items) || cart.items.length !== 0) throw new G09RequestError('cart_not_empty');
          await call('cart-add', '/api/v1/cart/items', { version: cart.version, product_id: f.productId, quantity: 1, billing_cycle: 'monthly', option_ids: f.optionIds, option_quantities: f.optionQuantities, purchase_fields: f.purchaseFields });
          const updated = (await call('cart-after', '/api/v1/cart')).data;
          const item = updated?.items?.[0];
          if (!id(updated?.version) || updated.version <= cart.version || updated.items.length !== 1 || !id(item?.id) || item.product_id !== f.productId || item.quantity !== 1 || item.total_cents !== amount) throw new G09RequestError('invalid_cart_receipt', null, true);
          const order = (await call('checkout', '/api/v1/orders', { cart_version: updated.version, item_ids: [item.id] })).data;
          invoiceId = order?.invoice_id; orderId = order?.order_id;
          if (!id(invoiceId) || !id(orderId) || order.total_cents !== amount) throw new G09RequestError('invalid_order_receipt', null, true);
        } else if (f.kind === 'local_renew') {
          const renewal = (await call('renew', `/api/v1/services/${f.serviceId}/renew`, {})).data;
          invoiceId = renewal?.invoice_id;
          if (!id(invoiceId) || renewal.service_id !== f.serviceId || renewal.amount_cents !== amount) throw new G09RequestError('invalid_renewal_receipt', null, true);
        } else invoiceId = f.invoiceId;
        const invoice = (await call('invoice-before', `/api/v1/invoices/${invoiceId}`)).data;
        if (invoice?.id !== invoiceId || invoice.user_id !== f.customerId || invoice.amount_cents !== amount || invoice.status !== 'unpaid' || !id(invoice.version)) throw new G09RequestError('invoice_admission_mismatch');
        const payment = (await call('pay', `/api/v1/invoices/${invoiceId}/pay`, { expected_version: invoice.version })).data;
        if (payment?.invoice_id !== invoiceId || payment.status !== 'paid' || payment.amount_cents !== amount) throw new G09RequestError('invalid_payment_receipt', null, true);
        // Do not call a real worker from here. Accepted purchase is not delivered.
        if (f.kind === 'local_renew') {
          const service = (await call('service-after', `/api/v1/services/${f.serviceId}`)).data;
          if (!Number.isFinite(Date.parse(service?.next_due_at)) || Date.parse(service.next_due_at) <= Date.parse(f.beforeDue)) throw new G09RequestError('renewal_not_observed', null, true);
        }
      }
      combinedSignal.throwIfAborted();
      return { status: 200, accepted: true, succeeded: true, customerId: f.customerId, invoiceId, orderId, ticketId, serviceId: f.serviceId ?? null, deliveryVerified: false, accountingVerified: false };
    } catch (error) {
      // Even a known payment rejection can leave an accepted cart/order/renewal.
      // A new key must never bypass this unresolved chain. Only a new isolated
      // run or an independently implemented original-request resolution can resume.
      if (mutationAttempted) {
        quarantinedCustomers.add(f.customerId);
        if (f.serviceId) quarantinedServices.add(f.serviceId);
      }
      throw error;
    } finally {
      busyCustomers.delete(f.customerId);
      if (f.kind === 'local_renew') busyServices.delete(f.serviceId);
    }
  };
  execute.stop = () => stopController.abort();
  execute.snapshot = () => ({ stopped: stopController.signal.aborted, allocatedFlows: seen.size, quarantinedCustomers: [...quarantinedCustomers], quarantinedServices: [...quarantinedServices], budgets: [...budgets].map(([customerId, b]) => ({ customerId, remainingCents: b.remaining, writes: b.writes })) });
  return execute;
}
