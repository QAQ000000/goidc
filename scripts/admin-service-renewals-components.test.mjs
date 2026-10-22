import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { createRequire, Module } from 'node:module';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const web = fileURLToPath(new URL('../web/', import.meta.url)), require = createRequire(path.join(web, 'package.json'));
const ts = require('typescript'), React = require('react'), cache = new Map();
let current, actor, liveActor, management, calls, storage, respond, failStorage, refreshes;
const changed = (a, b) => !a || !b || a.length !== b.length || a.some((value, index) => value !== b[index]);
const hooks = {
  ...React,
  useState(initial) { const state = current, index = state.cursor++; if (!(index in state.values)) state.values[index] = typeof initial === 'function' ? initial() : initial; return [state.values[index], next => { state.values[index] = typeof next === 'function' ? next(state.values[index]) : next; }]; },
  useRef(initial) { const state = current, index = state.cursor++; if (!(index in state.values)) state.values[index] = { current: initial }; return state.values[index]; },
  useEffect(effect, deps) { const state = current, index = state.cursor++, previous = state.values[index]; if (changed(previous?.deps, deps)) state.effects.push(() => { previous?.cleanup?.(); state.values[index] = { deps, cleanup: effect() }; }); },
};
class ApiError extends Error { constructor(status, code) { super(code); this.status = status; this.code = code; } }
const state = () => ({ cursor: 0, values: [], effects: [] });
function within(state, action) { const before = current; current = state; state.cursor = 0; let result; try { result = action(); } finally { current = before; } state.effects.splice(0).forEach(effect => effect()); return result; }
const ui = new Proxy({}, { get: (_, name) => props => React.createElement(({ Button: 'button', Input: 'input', Textarea: 'textarea', Checkbox: 'checkbox' })[name] ?? String(name).toLowerCase(), props, props.children) });
function load(filename) {
  if (cache.has(filename)) return cache.get(filename).exports;
  const module = new Module(filename); cache.set(filename, module); const external = createRequire(filename);
  module.require = name => {
    if (name === 'react') return hooks;
    if (name === './workspace') return { useAdminIdentity: () => actor, useAdminPermission: permission => actor?.role === 'super_admin' || actor?.permissions.includes(permission) };
    if (name.includes('/ui/')) return ui;
    if (name === '@/lib/api') return { ApiError, date: value => value, api: async (url, options = {}) => { const call = { url, ...options }; calls.push(call); return respond(call); } };
    if (name.startsWith('@/') || name.startsWith('.')) {
      const base = name.startsWith('@/') ? path.join(web, 'src', name.slice(2)) : path.resolve(path.dirname(filename), name);
      const source = [base, `${base}.tsx`, `${base}.ts`].find(candidate => existsSync(candidate)); if (source && /\.tsx?$/.test(source)) return load(source);
    }
    return external(name);
  };
  const compiled = ts.transpileModule(readFileSync(filename, 'utf8'), { reportDiagnostics: true, compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true } });
  assert.equal((compiled.diagnostics ?? []).filter(item => item.category === ts.DiagnosticCategory.Error).length, 0);
  module._compile(compiled.outputText, filename); return module.exports;
}
const { useServiceRenewalMutation, validRenewalInvoice, validRenewalQuote } = load(path.join(web, 'src/components/admin-service-renewal-mutation.ts'));
const { AdminServiceRenewals } = load(path.join(web, 'src/components/admin-service-renewals.tsx'));
const root = '/admin/services/31';
const service = { id: 31, user_id: 41, version: 3, owner_generation: 0, billing_cycle: 'monthly', name: 'service' };
const facts = (overrides = {}) => ({ service_id: 31, customer_id: 41, service_version: 3, owner_generation: 0, status: 'active', billing_cycle: 'monthly', pricing_mode: 'contract', fixed_amount_cents: null, contract_base_cents: 1000, contract_discount_cents: 200, contract_amount_cents: 800, amount_cents: 800, next_due_at: '2026-11-01T00:00:00.000123Z', term_mode: 'follow_supplier', supplier_next_due_at: null, supplier_observed_at: null, ...overrides });
const quote = (overrides = {}) => ({ customer_id: 41, service_id: 31, service_version: 3, billing_cycle: 'monthly', base_cents: 1000, discount_cents: 200, amount_cents: 800, current_due_at: facts().next_due_at, pending_invoice_id: 0, can_create: true, expires_at: new Date(Date.now() + 60000).toISOString(), quote_hash: 'a'.repeat(64), ...overrides });
const invoice = (overrides = {}) => ({ id: 51, user_id: 41, invoice_no: 'INV-51', version: 1, invoice_kind: 'automatic', status: 'unpaid', amount_cents: 800, import_review_required: false, payment_pending: false, items: [{ item_type: 'renew', service_id: 31, amount_cents: 800 }], ...overrides });
const input = (kind = 'normal', amount = 800) => {
  if (kind === 'pay') return { kind, invoice: invoice(), body: { expected_version: 1, amount_cents: 800, reason: 'confirmed balance payment' } };
  const q = kind === 'fixed' ? quote({ base_cents: amount, discount_cents: 0, amount_cents: amount }) : quote();
  return { kind, quote: q, body: { expected_version: 3, quote_hash: q.quote_hash, reason: 'confirmed renewal', ...(kind === 'fixed' ? { amount_cents: amount } : {}) } };
};
let currentInvoice;
function receipt(call) {
  const body = JSON.parse(call.body), pay = call.url.endsWith('/pay');
  return { invoice_id: 51, invoice_no: 'INV-51', service_id: 31, customer_id: 41, amount_cents: body.amount_cents ?? 800, status: pay ? 'paid' : 'unpaid', replayed: false, ...(pay ? { renewal_status: 'completed', balance_cents: 5000 } : { version: 1 }) };
}
function reset() {
  actor = { id: 7, role: 'admin', permissions: ['services.view', 'invoices.view', 'services.renew', 'services.renew.override', 'services.renew.pay_balance'], customer_scope: 'all', customer_ownerless: true }; liveActor = actor;
  management = facts(); currentInvoice = invoice(); calls = []; storage = new Map(); failStorage = false;
  respond = async call => call.url === '/me' ? liveActor : call.url.endsWith('/management') ? management : call.url === '/admin/invoices/51' ? currentInvoice : call.url.endsWith('renewal-quotes') ? quote() : receipt(call);
  globalThis.window = { setTimeout, clearTimeout, addEventListener() {}, removeEventListener() {} };
  globalThis.sessionStorage = { getItem: key => storage.get(key) ?? null, setItem: (key, value) => { if (failStorage) throw new Error('storage unavailable'); storage.set(key, value); }, removeItem: key => storage.delete(key) };
}
const settle = () => new Promise(resolve => setTimeout(resolve, 10));
function mutation() {
  const instance = state(); return { render: () => within(instance, () => useServiceRenewalMutation(actor, service, 0)), unmount: () => instance.values.forEach(item => item?.cleanup?.()) };
}
async function ready() { const view = mutation(); view.render(); await settle(); view.render(); return view; }
const writes = () => calls.filter(c => c.headers?.['Idempotency-Key']);

for (const kind of ['normal', 'fixed', 'pay']) test(`${kind}: unknown result survives remount and expired preview, replays exact original body/key`, async () => {
  reset(); let view = await ready();
  const originalRespond = respond;
  respond = async call => { if (call.headers) throw new TypeError('lost response'); return originalRespond(call); };
  await view.render().run(input(kind)); assert.equal(storage.size, 1); const original = writes()[0]; assert.ok(original);
  view.unmount(); const [key, raw] = [...storage][0], saved = JSON.parse(raw); if (saved.quote) saved.quote.expires_at = '2026-01-01T00:00:00Z'; storage.set(key, JSON.stringify(saved));
  view = await ready(); management = facts({ service_version: 5 }); if (kind === 'pay') currentInvoice = invoice({ status: 'paid', version: 2 });
  respond = async call => call.headers ? { ...receipt(call), replayed: true } : originalRespond(call);
  const result = await view.render().run(); assert.ok(result); assert.equal(result.receipt.replayed, true); assert.equal(result.facts.management.service_version, 5); assert.equal(writes()[1].body, original.body); assert.equal(writes()[1].headers['Idempotency-Key'], original.headers['Idempotency-Key']); assert.equal(storage.size, 0); view.unmount();
});
test('permission, scope and ownership changes block writes and late receipts never clear original intent', async () => {
  for (const change of [() => { liveActor = { ...actor, id: 8 }; }, () => { liveActor = { ...actor, customer_scope: 'assigned' }; }, () => { liveActor = { ...actor, permissions: ['services.view', 'invoices.view'] }; }, () => { management = facts({ owner_generation: 2 }); }, () => { management = facts({ customer_id: 42 }); }]) {
    reset(); const view = await ready(); change(); assert.equal(await view.render().run(input()), null); assert.equal(writes().length, 0); view.unmount();
  }
  reset(); const view = await ready(), original = respond;
  respond = async call => { if (call.headers) { liveActor = { ...actor, id: 8 }; return receipt(call); } return original(call); };
  assert.equal(await view.render().run(input()), null); assert.equal(storage.size, 1); assert.equal(view.render().receipt, null); view.unmount();
});
test('only-pay role reads actual invoice but cannot request/create normal or fixed quotes', async () => {
  reset(); actor = { ...actor, permissions: ['services.view', 'invoices.view', 'services.renew.pay_balance'] }; liveActor = actor; const view = await ready();
  assert.equal(await view.render().preview(), null); assert.equal(await view.render().preview(0), null); assert.equal(await view.render().run(input()), null); assert.equal(await view.render().run(input('fixed')), null); assert.equal(writes().length, 0);
  assert.equal((await view.render().readInvoice(51)).amount_cents, 800); currentInvoice = invoice({ status: 'paid', version: 2 }); assert.ok(await view.render().run(input('pay')) === null); // latest status blocks new payment
  currentInvoice = invoice(); assert.ok(await view.render().run(input('pay'))); view.unmount();
});
test('only-create role cannot pay; invalid or foreign invoice cannot become a payment target', async () => {
  reset(); actor = { ...actor, permissions: ['services.view', 'invoices.view', 'services.renew'] }; liveActor = actor; let view = await ready(); assert.equal(await view.render().run(input('pay')), null); assert.equal(writes().length, 0); view.unmount();
  for (const mismatch of [{ user_id: 42 }, { invoice_kind: 'manual' }, { items: [] }, { items: [...invoice().items, ...invoice().items] }, { items: [{ service_id: 32, item_type: 'renew', amount_cents: 800 }] }, { amount_cents: NaN }]) { reset(); currentInvoice = invoice(mismatch); view = await ready(); assert.equal(await view.render().readInvoice(51), null); assert.equal(writes().length, 0); view.unmount(); }
});
test('fixed zero remains explicit separate creation and payment, money and preview mutations are checked', async () => {
  reset(); const view = await ready(); currentInvoice = invoice({ amount_cents: 0, items: [{ item_type: 'renew', service_id: 31, amount_cents: 0 }] });
  assert.ok(await view.render().run(input('fixed', 0))); assert.equal(writes().length, 1); assert.match(writes()[0].url, /fixed-renewals$/); assert.equal(JSON.parse(writes()[0].body).amount_cents, 0); assert.equal(writes().some(c => c.url.endsWith('/pay')), false);
  const payInput = { kind: 'pay', invoice: currentInvoice, body: { expected_version: 1, amount_cents: 0, reason: 'explicit zero settlement' } };
  assert.ok(await view.render().run(payInput)); assert.equal(JSON.parse(writes()[1].body).amount_cents, 0); assert.notEqual(writes()[0].headers['Idempotency-Key'], writes()[1].headers['Idempotency-Key']); view.unmount();
  reset(); const invalid = await ready(); for (const bad of [input('fixed', -1), input('fixed', 0.1), { ...input(), body: { ...input().body, unexpected: true } }, { ...input(), quote: quote({ amount_cents: 801 }) }, { ...input(), quote: quote({ expires_at: 'not-a-date' }) }]) assert.equal(await invalid.render().run(bad), null); assert.equal(writes().length, 0); invalid.unmount();
});
test('first definite refusal requires facts before a fresh intent; earlier unknown cannot become a definite rejection', async () => {
  reset(); let view = await ready(); const standard = respond;
  respond = async call => { if (call.headers) throw new ApiError(409, 'insufficient_balance'); return standard(call); };
  await view.render().run(input('pay')); const firstKey = writes()[0].headers['Idempotency-Key']; assert.equal(view.render().pending.phase, 'rejected'); assert.equal(await view.render().run(input('pay')), null); assert.ok(await view.render().finishRejected());
  respond = standard; assert.ok(await view.render().run(input('pay'))); assert.notEqual(writes()[1].headers['Idempotency-Key'], firstKey); view.unmount();
  reset(); view = await ready(); respond = async call => { if (call.headers) throw new TypeError('lost'); return standard(call); }; await view.render().run(input());
  respond = async call => { if (call.headers) throw new ApiError(409, 'stale_renewal_quote'); return standard(call); }; await view.render().run(); assert.equal(view.render().pending.phase, 'uncertain'); assert.equal(await view.render().finishRejected(), null); assert.equal(storage.size, 1); view.unmount();
});
test('failure reading facts after successful write never becomes definite rejection', async () => {
  reset(); const view = await ready(), standard = respond; let committed = false;
  respond = async call => { if (call.headers) { committed = true; return receipt(call); } if (committed && call.url === '/admin/invoices/51') throw new ApiError(400, 'read_failed'); return standard(call); };
  await view.render().run(input()); assert.equal(view.render().pending.phase, 'uncertain'); assert.equal(storage.size, 1); view.unmount();
});
test('synchronous double-click, unavailable storage and corrupt recovery never cause a second write', async () => {
  reset(); let view = await ready(); await Promise.all([view.render().run(input()), view.render().run(input())]); assert.equal(writes().length, 1); view.unmount();
  reset(); view = await ready(); failStorage = true; await view.render().run(input()); assert.equal(writes().length, 0); view.unmount();
  reset(); storage.set('goidc:service-renewal:admin:7:31', '{bad'); view = await ready(); assert.equal(view.render().blocked, true); await view.render().run(input()); assert.equal(writes().length, 0); view.unmount();
});
test('quote validates pending-invoice estimate and never trusts current estimate as old invoice amount', () => {
  const pending = quote({ can_create: false, pending_invoice_id: 51, quote_hash: '' }); assert.ok(validRenewalQuote(pending, service, 3)); assert.equal(validRenewalQuote({ ...pending, quote_hash: 'a'.repeat(64) }, service, 3), false); assert.ok(validRenewalInvoice(invoice(), 31, 41, 51));
});
