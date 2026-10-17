import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { once } from 'node:events';
import { createG09APIClient, createG09ScenarioExecutor } from './g09-scenario-lib.mjs';

const cookie = 'zjmfgo_session=private-session';
const customer = { id: 1, cookie, budgetCents: 98000 };
const flow = (kind, extra = {}) => ({ id: 'flow1', kind, customerId: 1, cookie, amountCents: 1000, ...extra });
const response = (data, status = 200) => new Response(JSON.stringify({ data }), { status });
function client(options = {}) {
  const publicRows = [], privateRows = [], calls = [];
  const request = createG09APIClient({ apiOrigin: 'http://127.0.0.1:12345', publicOrigin: 'http://localhost:12346', runId: 'run1', record: row => publicRows.push(row), persistPrivate: row => privateRows.push(row), fetchImpl: async (...args) => { calls.push(args); return response({}); }, ...options });
  return { request, publicRows, privateRows, calls };
}
function executor(request, options = {}) { return createG09ScenarioExecutor({ request: row => {
  if (row.step === 'identity') return Promise.resolve({ status: 200, data: { id: 1, role: 'customer' } });
  if (row.step === 'invoice-before') return Promise.resolve({ status: 200, data: { id: Number(row.path.split('/')[4]), user_id: 1, amount_cents: 1000, status: 'unpaid', version: 3 } });
  return request(row);
}, runId: 'run1', accounts: [customer], ...options }); }
const rejects = (promise, code, uncertain) => assert.rejects(promise, error => error.code === code && (uncertain === undefined || error.uncertain === uncertain));

test('original mutation persists before dispatch and public HTTP evidence omits body, key, cookie and errors', async () => {
  const order = [], privateRows = [], publicRows = [];
  const c = client({ persistPrivate: row => { order.push('persist'); privateRows.push(row); }, record: row => { order.push(row.event); publicRows.push(row); }, fetchImpl: async (url, options) => {
    order.push('fetch'); assert.equal(url, 'http://127.0.0.1:12345/api/v1/invoices/1/pay');
    assert.equal(options.redirect, 'error'); assert.equal(options.headers.Cookie, cookie);
    assert.equal(options.headers.Origin, 'http://localhost:12346');
    assert.equal(options.headers['Idempotency-Key'], privateRows[0].key);
    assert.equal(options.body, privateRows[0].body);
    throw new Error('remote SECRET private-session');
  } });
  await rejects(c.request({ flowId: 'f', step: 'pay', path: '/api/v1/invoices/1/pay', method: 'POST', cookie, body: { secret: 'SECRET' } }), 'transport_failed', true);
  assert.deepEqual(order, ['persist', 'http_started', 'fetch', 'http_finished']);
  assert.ok(!/SECRET|private-session|Idempotency-Key/.test(JSON.stringify(publicRows)));
  assert.equal(publicRows[1].uncertain, true);
});

test('client rejects external origins, arbitrary queries, duplicate pagination, and duplicate mutation keys before I/O', async () => {
  for (const apiOrigin of ['https://127.0.0.1:12345', 'http://localhost:12345', 'http://example.com:80', 'http://127.0.0.1:12345/']) assert.throws(() => client({ apiOrigin }));
  const c = client();
  for (const path of ['http://example.com/', '//example.com/', '/api/v1/catalog/page?password=SECRET', '/api/v1/catalog/page?limit=1&limit=2', '/api/v1/catalog/page?offset=-1', '/api/v1/catalog/page?limit=101', '/api/v1/cart?offset=1', '/api/v1/../admin']) await rejects(c.request({ flowId: 'f', step: 'r', path }), 'invalid_request');
  assert.equal(c.calls.length, 0); assert.equal(c.publicRows.length, 0);
  await c.request({ flowId: 'f', step: 'pay', path: '/api/v1/invoices/1/pay', method: 'POST', body: {} });
  await rejects(c.request({ flowId: 'f', step: 'pay', path: '/api/v1/invoices/2/pay', method: 'POST', body: { changed: true } }), 'mutation_already_attempted');
  assert.equal(c.calls.length, 1); assert.equal(c.privateRows.length, 1);
});

test('private recorder failure, request budget, and already aborted dispatch fail closed', async () => {
  const c = client({ persistPrivate() { throw new Error('storage unavailable'); } });
  await assert.rejects(c.request({ flowId: 'f', step: 'pay', path: '/api/v1/invoices/1/pay', method: 'POST', body: {} }));
  assert.equal(c.calls.length, 0);
  const limited = client({ maxRequests: 1 });
  await limited.request({ flowId: 'f', step: 'r', path: '/api/v1/catalog/page?limit=25&offset=0' });
  await rejects(limited.request({ flowId: 'f', step: 'r2', path: '/api/v1/catalog/page' }), 'request_budget_exhausted');
  const aborted = client();
  await rejects(aborted.request({ flowId: 'f', step: 'r', path: '/api/v1/cart', signal: AbortSignal.abort() }), 'transport_failed', false);
  assert.equal(aborted.calls.length, 0);
});

test('real redirect is rejected without contacting target or leaking response contents', async t => {
  let destinationHits = 0;
  const destination = http.createServer((req, res) => { destinationHits++; res.end('secret'); });
  destination.listen(0, '127.0.0.1'); await once(destination, 'listening');
  const source = http.createServer((req, res) => { res.writeHead(302, { Location: `http://127.0.0.1:${destination.address().port}/` }); res.end(); });
  source.listen(0, '127.0.0.1'); await once(source, 'listening');
  t.after(() => { source.closeAllConnections(); destination.closeAllConnections(); source.close(); destination.close(); });
  const c = client({ apiOrigin: `http://127.0.0.1:${source.address().port}`, fetchImpl: fetch });
  await rejects(c.request({ flowId: 'f', step: 'r', path: '/api/v1/cart' }), 'transport_failed');
  assert.equal(destinationHits, 0);
});

test('409 is failure, malformed success is uncertain, and streamed response is bounded', async () => {
  const conflict = client({ fetchImpl: async () => new Response('SECRET', { status: 409 }) });
  await rejects(conflict.request({ flowId: 'f', step: 'pay', path: '/api/v1/invoices/1/pay', method: 'POST' }), 'http_rejected', false);
  assert.ok(!JSON.stringify(conflict.publicRows).includes('SECRET'));
  const malformed = client({ fetchImpl: async () => new Response('not json') });
  await rejects(malformed.request({ flowId: 'f', step: 'pay', path: '/api/v1/invoices/1/pay', method: 'POST' }), 'invalid_response', true);
  const oversized = client({ maxBodyBytes: 8, fetchImpl: async () => response('too many bytes') });
  await rejects(oversized.request({ flowId: 'f', step: 'r', path: '/api/v1/cart' }), 'response_budget_exceeded', false);
});

test('purchase follows actual cart/version/checkout/pay chain without claiming worker or SQL completion', async () => {
  const calls = [];
  const data = [
    { version: 1, items: [] }, {},
    { version: 2, items: [{ id: 9, product_id: 5, quantity: 1, total_cents: 1000 }] },
    { invoice_id: 10, order_id: 8, total_cents: 1000 },
    { invoice_id: 10, status: 'paid', amount_cents: 1000 },
  ];
  const execute = executor(async row => { calls.push(row); return { status: 201, data: data.shift() }; });
  const out = await execute({ flow: flow('purchase_local', { productId: 5, optionIds: [], optionQuantities: {}, purchaseFields: {} }) });
  assert.equal(out.succeeded, true); assert.equal(out.invoiceId, 10); assert.equal(out.deliveryVerified, false); assert.equal(out.accountingVerified, false);
  assert.deepEqual(calls.map(c => c.step), ['cart-before', 'cart-add', 'cart-after', 'checkout', 'pay']);
  assert.deepEqual(calls[3].body, { cart_version: 2, item_ids: [9] });
  assert.deepEqual(calls[4].body, { expected_version: 3 });
  assert.equal(execute.snapshot().budgets[0].remainingCents, 97000);
  await rejects(execute({ flow: flow('purchase_local') }), 'flow_budget_or_replay');
});

test('lost mutation reply quarantines customer/service and no new key can issue another mutation', async () => {
  let calls = 0;
  const execute = executor(async () => { calls++; const e = new Error(); e.uncertain = true; throw e; });
  await assert.rejects(execute({ flow: flow('local_renew', { serviceId: 7, beforeDue: '2026-11-01T00:00:00Z' }) }));
  assert.deepEqual(execute.snapshot().quarantinedServices, [7]);
  await rejects(execute({ flow: flow('local_renew', { id: 'new-key', serviceId: 7, beforeDue: '2026-11-01T00:00:00Z' }) }), 'resource_quarantined');
  await rejects(execute({ flow: flow('balance_pay', { id: 'other-key', invoiceId: 9 }) }), 'resource_quarantined');
  assert.equal(calls, 1);
});

test('accepted renewal then rejected payment remains quarantined; receipt money mismatch cannot pass', async () => {
  let calls = 0;
  const execute = executor(async () => { calls++; if (calls === 1) return { status: 201, data: { invoice_id: 5, service_id: 7, amount_cents: 1000 } }; throw Object.assign(new Error('409'), { status: 409 }); });
  await assert.rejects(execute({ flow: flow('local_renew', { serviceId: 7, beforeDue: '2026-11-01T00:00:00Z' }) }));
  assert.deepEqual(execute.snapshot().quarantinedCustomers, [1]); assert.equal(calls, 2);
  const wrong = executor(async () => ({ status: 201, data: { invoice_id: 5, status: 'paid', amount_cents: 999 } }));
  await rejects(wrong({ flow: flow('balance_pay', { invoiceId: 5 }) }), 'invalid_payment_receipt', true);
});

test('concurrent customer allocation fails instead of moving workload; stop aborts active flow and prohibits new flows', async () => {
  let unblock, entered;
  const ready = new Promise(resolve => { entered = resolve; });
  const execute = executor(async row => { entered(); await new Promise(resolve => { unblock = resolve; }); assert.equal(row.signal.aborted, true); return { status: 201, data: { invoice_id: 5, status: 'paid', amount_cents: 1000 } }; });
  const first = execute({ flow: flow('balance_pay', { invoiceId: 5 }) });
  await ready;
  await rejects(execute({ flow: flow('balance_pay', { id: 'busy', invoiceId: 6 }) }), 'allocation_busy');
  execute.stop(); unblock(); await assert.rejects(first);
  await rejects(execute({ flow: flow('balance_pay', { id: 'after-stop', invoiceId: 7 }) }), 'scenario_stopped');
  assert.deepEqual(execute.snapshot().quarantinedCustomers, [1]);
});

test('frozen account binding, per-write price, per-customer funds, and fifty-write limit are enforced', async () => {
  const execute = executor(async row => ({ status: 201, data: { id: 1, invoice_id: Number(row.path.split('/')[4]), status: 'paid', amount_cents: 1000 } }));
  await rejects(execute({ flow: flow('balance_pay', { cookie: 'zjmfgo_session=other', invoiceId: 1 }) }), 'customer_budget_exhausted');
  await rejects(execute({ flow: flow('balance_pay', { id: 'high', amountCents: 1001, invoiceId: 1 }) }), 'customer_budget_exhausted');
  for (let i = 0; i < 50; i++) await execute({ flow: flow('ticket', { id: `ticket${i}`, departmentId: 1 }) });
  await rejects(execute({ flow: flow('ticket', { id: 'ticket51', departmentId: 1 }) }), 'customer_budget_exhausted');
  const poor = executor(async () => response({}), { accounts: [{ ...customer, budgetCents: 999 }] });
  await rejects(poor({ flow: flow('balance_pay', { invoiceId: 1 }) }), 'customer_budget_exhausted');
});

test('renewal requires observed due advancement and used manual invoice cannot be reallocated', async () => {
  const execute = executor(async row => ({ status: 201, data: row.step === 'renew' ? { invoice_id: 1, service_id: 7, amount_cents: 1000 } : row.step === 'pay' ? { invoice_id: 1, status: 'paid', amount_cents: 1000 } : { next_due_at: '2026-12-01T00:00:00Z' } }));
  assert.equal((await execute({ flow: flow('local_renew', { serviceId: 7, beforeDue: '2026-11-01T00:00:00Z' }) })).succeeded, true);
  const pay = executor(async () => ({ status: 201, data: { invoice_id: 3, status: 'paid', amount_cents: 1000 } }));
  await pay({ flow: flow('balance_pay', { invoiceId: 3 }) });
  await rejects(pay({ flow: flow('balance_pay', { id: 'again', invoiceId: 3 }) }), 'invoice_already_allocated');
});

test('invoice identity and price are checked BEFORE debit, using the real manual-invoice version contract', async () => {
  let debits = 0;
  const rows = [];
  const execute = createG09ScenarioExecutor({ runId: 'run1', accounts: [customer], request: async row => {
    rows.push(row);
    if (row.step === 'identity') return { status: 200, data: { id: 1, role: 'customer' } };
    if (row.step === 'invoice-before') return { status: 200, data: { id: 9, user_id: 1, amount_cents: 5000, status: 'unpaid', version: 4 } };
    if (row.step === 'pay') debits += 5000;
    return { status: 201, data: {} };
  } });
  await rejects(execute({ flow: flow('balance_pay', { invoiceId: 9 }) }), 'invoice_admission_mismatch');
  assert.equal(debits, 0); assert.equal(rows.some(r => r.step === 'pay'), false);
});

test('duplicate session slots and mismatched API identity cannot bypass customer limits', async () => {
  assert.throws(() => executor(() => {}, { accounts: [customer, { ...customer, id: 2 }] }));
  let calls = 0;
  const execute = createG09ScenarioExecutor({ runId: 'run1', accounts: [customer], request: async () => { calls++; return { status: 200, data: { id: 2, role: 'customer' } }; } });
  await rejects(execute({ flow: flow('ticket', { departmentId: 1 }) }), 'customer_identity_mismatch');
  assert.equal(calls, 1);
});
