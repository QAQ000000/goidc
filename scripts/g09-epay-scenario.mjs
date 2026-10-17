// Source/candidate controller helper. No real gateway or capacity certification.
import { createHash, timingSafeEqual } from 'node:crypto';
import { performance } from 'node:perf_hooks';
import { G09RequestError } from './g09-scenario-lib.mjs';

const hash = value => createHash('sha256').update(value).digest('hex');
const id = value => Number.isSafeInteger(value) && value > 0;
const label = value => typeof value === 'string' && /^[a-zA-Z0-9_-]{1,40}$/.test(value);
const cookieOK = value => typeof value === 'string' && /^zjmfgo_session=[a-zA-Z0-9._~-]+$/.test(value);
const fail = (code, status = null, uncertain = false) => new G09RequestError(code, status, uncertain);
function isolatedOrigin(raw, publicHeader = false) {
  let u;
  try { u = new URL(raw); } catch { throw fail('epay_origin_invalid'); }
  if (u.protocol !== 'http:' || !(publicHeader ? ['127.0.0.1', 'localhost'] : ['127.0.0.1']).includes(u.hostname) || !u.port || raw !== u.origin || u.username || u.password) throw fail('epay_origin_invalid');
  return u.origin;
}
function syncRecord(callback, value) {
  const out = callback(Object.freeze(value));
  if (out && typeof out.then === 'function') { out.catch?.(() => {}); throw fail('epay_sync_recorder_required'); }
}
function signature(values, key) {
  const fields = [...values].filter(([name, value]) => name !== 'sign' && name !== 'sign_type' && value !== '').sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0);
  return createHash('md5').update(fields.map(([name, value]) => `${name}=${value}`).join('&') + key).digest('hex');
}

/** Validate the exact checkout produced by the compiled standard Epay adapter.
 * This parser never fetches the checkout URL or its callback/return targets.
 * Credentials and original callback form are private controller inputs only.
 */
export function validateG09EpayCheckout({ checkoutURL, merchantOrigin, publicOrigin, pid, key, provider, invoiceId, amountCents }) {
  const merchant = isolatedOrigin(merchantOrigin), origin = isolatedOrigin(publicOrigin, true);
  if (!/^\d{1,20}$/.test(pid) || typeof key !== 'string' || key.length < 8 || key.length > 256 || !['epay_alipay', 'epay_wxpay'].includes(provider) || !id(invoiceId) || !id(amountCents) || amountCents > 1000 || typeof checkoutURL !== 'string' || checkoutURL.length > 8192) throw fail('epay_checkout_invalid');
  let u;
  try { u = new URL(checkoutURL); } catch { throw fail('epay_checkout_invalid'); }
  if (u.origin !== merchant || u.pathname !== '/submit.php' || u.hash || u.username || u.password) throw fail('epay_checkout_invalid');
  const names = ['pid', 'type', 'notify_url', 'return_url', 'out_trade_no', 'name', 'money', 'sign', 'sign_type'];
  const values = u.searchParams;
  if ([...values].length !== names.length || names.some(name => values.getAll(name).length !== 1) || [...values.keys()].some(name => !names.includes(name))) throw fail('epay_checkout_invalid');
  const reference = values.get('out_trade_no');
  if (values.get('pid') !== pid || values.get('type') !== provider.slice(5) || values.get('sign_type') !== 'MD5' || !new RegExp(`^epay_${invoiceId}_[0-9a-f]{32}$`).test(reference) || values.get('money') !== `${Math.floor(amountCents / 100)}.${String(amountCents % 100).padStart(2, '0')}` || values.get('notify_url') !== `${origin}/api/v1/payments/${provider}/webhook` || values.get('return_url') !== `${origin}/console/invoices/${invoiceId}` || !values.get('name') || values.get('name').length > 256 || !/^[0-9a-f]{32}$/i.test(values.get('sign'))) throw fail('epay_checkout_invalid');
  const expected = signature(values, key);
  if (!timingSafeEqual(Buffer.from(expected), Buffer.from(values.get('sign').toLowerCase()))) throw fail('epay_checkout_signature_invalid');
  return Object.freeze({ providerPaymentId: reference, amountCents, invoiceId });
}

/** Bounded one-shot external settlement plus one deliberate identical duplicate
 * notification. The caller's request must be createG09APIClient (or equivalent
 * independently verified HTTP adapter). It persists intent mutations separately.
 * No ordinary retries, checkout fetch, query/close/refund or unknown recovery.
 */
export function createG09EpaySettlementExecutor({ request, apiOrigin, merchantOrigin, publicOrigin, runId, pid, key, accounts, persistPrivate, record, fetchImpl = fetch, maxFlows = 1000, timeoutMs = 30000 }) {
  const target = isolatedOrigin(apiOrigin), merchant = isolatedOrigin(merchantOrigin), origin = isolatedOrigin(publicOrigin, true);
  if (typeof request !== 'function' || !label(runId) || !/^\d{1,20}$/.test(pid) || typeof key !== 'string' || key.length < 8 || key.length > 256 || typeof persistPrivate !== 'function' || typeof record !== 'function' || typeof fetchImpl !== 'function' || !id(maxFlows) || maxFlows > 1000 || !id(timeoutMs) || timeoutMs > 30000 || !Array.isArray(accounts) || !accounts.length || accounts.length > 2000 || accounts.some(a => !id(a.id) || !cookieOK(a.cookie) || !id(a.budgetCents)) || new Set(accounts.map(a => a.id)).size !== accounts.length || new Set(accounts.map(a => a.cookie)).size !== accounts.length) throw fail('epay_executor_invalid');
  const budgets = new Map(accounts.map(a => [a.id, { cookie: a.cookie, remainingCents: a.budgetCents, writes: 0 }]));
  const seen = new Set(), invoices = new Set(), busy = new Set(), quarantined = new Set();
  const stopped = new AbortController();
  let callbacksAttempted = 0;
  const execute = async ({ flow, signal } = {}) => {
    if (stopped.signal.aborted) throw fail('epay_executor_stopped');
    if (!flow || !label(flow.id) || flow.kind !== 'external_settle' || !id(flow.customerId) || !id(flow.invoiceId) || !id(flow.amountCents) || flow.amountCents > 1000 || !['epay_alipay', 'epay_wxpay'].includes(flow.provider) || !cookieOK(flow.cookie)) throw fail('epay_flow_invalid');
    const f = structuredClone(flow), budget = budgets.get(f.customerId);
    if (seen.has(f.id) || seen.size >= maxFlows || invoices.has(f.invoiceId) || busy.has(f.customerId) || quarantined.has(f.customerId) || !budget || budget.cookie !== f.cookie || budget.writes >= 50 || budget.remainingCents < f.amountCents) throw fail('epay_flow_admission_refused');
    seen.add(f.id); invoices.add(f.invoiceId); busy.add(f.customerId); budget.writes++; budget.remainingCents -= f.amountCents;
    const combined = AbortSignal.any([stopped.signal, AbortSignal.timeout(timeoutMs), ...(signal ? [signal] : [])]);
    let mutationAttempted = false;
    const call = async (step, path, body) => {
      combined.throwIfAborted();
      if (body !== undefined) mutationAttempted = true;
      const out = await request({ flowId: f.id, step, path, method: body === undefined ? 'GET' : 'POST', cookie: f.cookie, body, signal: combined });
      combined.throwIfAborted();
      if (!Number.isInteger(out?.status) || out.status < 200 || out.status >= 300) throw fail('epay_api_rejected', out?.status ?? null, body !== undefined);
      return out.data;
    };
    try {
      const me = await call('identity', '/api/v1/me');
      if (me?.id !== f.customerId || me.role !== 'customer') throw fail('epay_identity_mismatch');
      const before = await call('invoice-before', `/api/v1/invoices/${f.invoiceId}`);
      if (before?.id !== f.invoiceId || before.user_id !== f.customerId || before.status !== 'unpaid' || before.amount_cents !== f.amountCents || !id(before.version)) throw fail('epay_invoice_admission_mismatch');
      const intent = await call('intent', `/api/v1/invoices/${f.invoiceId}/payments`, { provider: f.provider, expected_version: before.version });
      const checkout = validateG09EpayCheckout({ checkoutURL: intent?.checkout_url, merchantOrigin: merchant, publicOrigin: origin, pid, key, provider: f.provider, invoiceId: f.invoiceId, amountCents: f.amountCents });
      const trade = `g09_${hash(`${runId}\0${f.id}`).slice(0, 48)}`;
      const form = new URLSearchParams({ pid, type: f.provider.slice(5), out_trade_no: checkout.providerPaymentId, trade_no: trade, money: `${Math.floor(f.amountCents / 100)}.${String(f.amountCents % 100).padStart(2, '0')}`, trade_status: 'TRADE_SUCCESS' });
      form.set('sign', signature(form, key)); form.set('sign_type', 'MD5');
      const body = form.toString(), payloadSha256 = hash(body), path = `/api/v1/payments/${f.provider}/webhook`;
      // Exactly one frozen original form is persisted before either dispatch.
      syncRecord(persistPrivate, { kind: 'epay_callback_original', flowId: f.id, method: 'POST', path, body, payloadSha256, plannedAttempts: 2 });
      for (const attempt of [1, 2]) {
        combined.throwIfAborted();
        const sequence = ++callbacksAttempted;
        const meta = { flowId: f.id, method: 'POST', path, callbackSequence: sequence, attempt, duplicate: attempt === 2, payloadSha256 };
        syncRecord(record, { event: 'epay_callback_started', ...meta });
        const startedAt = performance.now();
        let status = null, succeeded = false;
        try {
          const response = await fetchImpl(`${target}${path}`, { method: 'POST', headers: { 'Content-Type': 'application/x-www-form-urlencoded' }, body, redirect: 'error', signal: combined });
          status = response.status;
          const reader = response.body?.getReader();
          const parts = []; let bytes = 0;
          if (reader) {
            try {
              while (true) {
                const part = await reader.read(); if (part.done) break;
                bytes += part.value.byteLength;
                if (bytes > 64) { await reader.cancel(); throw fail('epay_callback_response_budget', status, true); }
                parts.push(Buffer.from(part.value));
              }
            } finally { reader.releaseLock(); }
          }
          combined.throwIfAborted();
          if (status !== 200 || Buffer.concat(parts).toString('utf8') !== 'success') throw fail('epay_callback_rejected', status, true);
          succeeded = true;
        } catch (error) {
          throw error instanceof G09RequestError ? error : fail('epay_callback_transport_failed', status, true);
        } finally {
          syncRecord(record, { event: 'epay_callback_finished', ...meta, status, succeeded, uncertain: !succeeded, startedAt, endedAt: performance.now() });
        }
      }
      const after = await call('invoice-after', `/api/v1/invoices/${f.invoiceId}`);
      if (after?.id !== f.invoiceId || after.user_id !== f.customerId || after.amount_cents !== f.amountCents || after.status !== 'paid' || typeof after.paid_at !== 'string' || !Number.isFinite(Date.parse(after.paid_at))) throw fail('epay_paid_invoice_not_observed', null, true);
      return { status: 200, accepted: true, succeeded: true, invoiceId: f.invoiceId, customerId: f.customerId, callbackAttempts: 2, accountingVerified: false, deliveryVerified: false, candidateVerified: false, capacityVerified: false };
    } catch (error) {
      if (mutationAttempted) quarantined.add(f.customerId);
      throw error instanceof G09RequestError ? error : fail('epay_flow_failed', null, mutationAttempted);
    } finally { busy.delete(f.customerId); }
  };
  execute.stop = () => stopped.abort();
  execute.snapshot = () => ({ stopped: stopped.signal.aborted, allocatedFlows: seen.size, callbacksAttempted, quarantinedCustomers: [...quarantined], budgets: [...budgets].map(([customerId, b]) => ({ customerId, remainingCents: b.remainingCents, writes: b.writes })), candidateVerified: false, capacityVerified: false });
  return execute;
}
