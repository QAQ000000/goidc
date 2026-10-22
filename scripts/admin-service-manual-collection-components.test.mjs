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
const { useServiceManualCollection, validCollectionRequest } = load(path.join(web, 'src/components/admin-service-manual-collection-mutation.ts'));
const service = { id: 31, user_id: 41, version: 3, owner_generation: 0, billing_cycle: 'monthly', name: 'service' };
const facts = (overrides = {}) => ({ service_id: 31, customer_id: 41, service_version: 3, owner_generation: 0, status: 'active', billing_cycle: 'monthly', pricing_mode: 'contract', fixed_amount_cents: null, contract_base_cents: 1000, contract_discount_cents: 200, contract_amount_cents: 800, amount_cents: 800, next_due_at: '2026-11-01T00:00:00.000123Z', term_mode: 'follow_supplier', supplier_next_due_at: null, supplier_observed_at: null, ...overrides });
const invoice = (overrides = {}) => ({ id: 51, user_id: 41, invoice_no: 'INV-51', version: 1, created_at: '2026-01-01T00:00:00.000123Z', invoice_kind: 'automatic', status: 'unpaid', amount_cents: 800, import_review_required: false, payment_pending: false, items: [{ item_type: 'renew', service_id: 31, amount_cents: 800 }], ...overrides });
const details = (overrides = {}) => ({ received_at: '2026-01-01T08:01:00.000456+08:00', method: 'bank_transfer', receiving_account: 'account ending 1234', transaction_no: 'BANK-51', evidence_ref: 'archive://51', evidence_sha256: 'b'.repeat(64), reason: 'verified full payment', ...overrides });
let currentInvoice;
function quote(call) { const body = JSON.parse(call.body); return { service_id: 31, customer_id: 41, invoice_id: 51, invoice_no: 'INV-51', invoice_version: body.expected_version, service_version: body.expected_service_version, amount_cents: body.amount_cents, received_at: '2026-01-01T00:01:00.000456Z', method: body.method, receiving_account: body.receiving_account, transaction_no: body.transaction_no, quote_hash: 'a'.repeat(64), expires_at: new Date(Date.now() + 60000).toISOString() }; }
function receipt(call) { const b = JSON.parse(call.body); return { service_id: 31, customer_id: 41, invoice_id: 51, invoice_no: 'INV-51', invoice_version: 2, service_version: 4, payment_id: 61, collection_id: 71, amount_cents: b.amount_cents, received_at: '2026-01-01T00:01:00.000456Z', status: 'paid', renewal_status: 'completed', replayed: false }; }
function reset() {
  actor = { id: 7, role: 'admin', permissions: ['services.view', 'invoices.view', 'invoices.collect_manual'], customer_scope: 'all', customer_ownerless: true }; liveActor = actor;
  management = facts(); currentInvoice = invoice(); calls = []; storage = new Map(); failStorage = false;
  respond = async call => call.url === '/me' ? liveActor : call.url.endsWith('/management') ? management : call.url === '/admin/invoices/51' ? currentInvoice : call.url.endsWith('manual-collection-quotes') ? quote(call) : receipt(call);
  globalThis.window = { setTimeout, clearTimeout, addEventListener() {}, removeEventListener() {} };
  globalThis.sessionStorage = { getItem: key => storage.get(key) ?? null, setItem: (key, value) => { if (failStorage) throw new Error('storage unavailable'); storage.set(key, value); }, removeItem: key => { if (failStorage) throw new Error('storage unavailable'); storage.delete(key); } };
}
const settle = () => new Promise(resolve => setTimeout(resolve, 10));
async function ready() { const instance = state(), view = { render: () => within(instance, () => useServiceManualCollection(actor, service, 0)), unmount: () => instance.values.forEach(item => item?.cleanup?.()) }; view.render(); await settle(); view.render(); return view; }
const writes = () => calls.filter(c => c.headers?.['Idempotency-Key']);
const previewCalls = () => calls.filter(c => c.url.endsWith('/manual-collection-quotes'));
const prepare = async view => { const i = await view.render().readInvoice(51); assert.ok(i); const request = await view.render().preview(i, details()); assert.ok(request); return request; };

test('collect-only administrator reads real invoice and settles the immutable preview without balance/renew permission', async () => {
  reset(); const view = await ready(), input = await prepare(view);
  assert.equal(input.body.expected_version, 1); assert.equal(input.body.expected_service_version, 3); assert.equal(input.body.amount_cents, 800); assert.equal(input.body.evidence_sha256, 'b'.repeat(64));
  assert.ok(await view.render().run(input)); assert.equal(writes().length, 1); assert.deepEqual(JSON.parse(writes()[0].body), input.body); assert.equal(storage.size, 0); assert.ok(calls.every(c => c.portal === 'admin')); view.unmount();
});
test('unknown collection survives remount, paid invoice, changed versions and expired quote with exact original body/key', async () => {
  reset(); let view = await ready(), input = await prepare(view); const standard = respond;
  respond = async call => { if (call.headers) throw new TypeError('response lost'); return standard(call); };
  await view.render().run(input); const original = writes()[0]; assert.equal(view.render().pending.phase, 'uncertain'); view.unmount();
  const [key, raw] = [...storage][0], saved = JSON.parse(raw); saved.quote.expires_at = '2026-01-01T00:00:00Z'; storage.set(key, JSON.stringify(saved));
  currentInvoice = invoice({ status: 'paid', version: 2 }); management = facts({ service_version: 5 }); view = await ready();
  respond = async call => call.headers ? { ...receipt(call), replayed: true } : standard(call);
  const result = await view.render().run(); assert.ok(result); assert.equal(result.receipt.replayed, true); assert.equal(result.facts.management.service_version, 5); assert.equal(writes()[1].body, original.body); assert.equal(writes()[1].headers['Idempotency-Key'], original.headers['Idempotency-Key']); assert.equal(storage.size, 0); view.unmount();
});
test('actual invoice rejects foreign, purchase, multi-line and missing timestamp; zero cannot preview', async () => {
  for (const mismatch of [{ user_id: 42 }, { invoice_kind: 'manual' }, { created_at: undefined }, { items: [] }, { items: [...invoice().items, ...invoice().items] }, { items: [{ service_id: 32, item_type: 'renew', amount_cents: 800 }] }]) {
    reset(); currentInvoice = invoice(mismatch); const view = await ready(); assert.equal(await view.render().readInvoice(51), null); assert.equal(writes().length, 0); view.unmount();
  }
  reset(); currentInvoice = invoice({ amount_cents: 0, items: [{ service_id: 31, item_type: 'renew', amount_cents: 0 }] }); const view = await ready(); assert.equal(await view.render().preview(currentInvoice, details()), null); assert.equal(previewCalls().length, 0); view.unmount();
});
test('invalid evidence, source and precise timestamps never reach preview; reference text is not hashed as fallback', async () => {
  for (const invalid of [{ evidence_sha256: '' }, { evidence_sha256: 'a'.repeat(63) }, { evidence_sha256: 'A'.repeat(64) }, { evidence_ref: '' }, { received_at: '2026-01-01T00:00:00Z' }, { received_at: '2026-01-01T00:01:00' }, { received_at: '2026-02-30T00:00:00Z' }, { received_at: '2026-01-01T00:01:00.1234567Z' }, { received_at: '9999-01-01T00:00:00Z' }, { method: 'online' }, { transaction_no: 'line\nbreak' }, { reason: '' }]) {
    reset(); const view = await ready(); assert.equal(await view.render().preview(invoice(), details(invalid)), null); assert.equal(previewCalls().length, 0); assert.equal(writes().length, 0); view.unmount();
  }
});
test('mutated body, forged quote and stale invoice cannot be silently substituted for preview snapshot', async () => {
  reset(); const view = await ready(), input = await prepare(view);
  for (const changed of [{ ...input, body: { ...input.body, reason: 'new reason' } }, { ...input, body: { ...input.body, evidence_sha256: 'c'.repeat(64) } }, { ...input, body: { ...input.body, extra: true } }, { ...input, quote: { ...input.quote, amount_cents: 801 } }]) assert.equal(await view.render().run(changed), null);
  currentInvoice = invoice({ version: 2 }); assert.equal(await view.render().run(input), null); assert.equal(writes().length, 0); view.unmount();
});
test('preview echo mismatches and facts changed during preview block collection', async () => {
  for (const mismatch of [{ invoice_id: 52 }, { amount_cents: 900 }, { received_at: '2026-01-01T00:01:00.000457Z' }, { service_version: 4 }, { transaction_no: 'other' }, { quote_hash: 'invalid' }]) {
    reset(); const standard = respond, view = await ready(); respond = async call => call.url.endsWith('/manual-collection-quotes') ? { ...quote(call), ...mismatch } : standard(call);
    assert.equal(await view.render().preview(invoice(), details()), null); assert.equal(writes().length, 0); view.unmount();
  }
  reset(); const standard = respond, view = await ready(); respond = async call => { if (call.url.endsWith('/manual-collection-quotes')) { management = facts({ service_version: 4 }); return quote(call); } return standard(call); };
  assert.equal(await view.render().preview(invoice(), details()), null); view.unmount();
});
test('reader, changed identity, scope, permission, ownership and late success cannot clear original intent', async () => {
  for (const change of [() => { liveActor = { ...actor, id: 8 }; }, () => { liveActor = { ...actor, customer_scope: 'assigned' }; }, () => { liveActor = { ...actor, permissions: ['services.view', 'invoices.view'] }; }, () => { management = facts({ owner_generation: 2 }); }, () => { management = facts({ customer_id: 42 }); }]) {
    reset(); const view = await ready(), input = await prepare(view); change(); assert.equal(await view.render().run(input), null); assert.equal(writes().length, 0); view.unmount();
  }
  reset(); actor = { ...actor, permissions: ['services.view', 'invoices.view'] }; liveActor = actor; let view = await ready(); assert.equal(await view.render().readInvoice(51), null); assert.equal(await view.render().preview(invoice(), details()), null); assert.equal(previewCalls().length, 0); view.unmount();
  reset(); view = await ready(); const input = await prepare(view), standard = respond; respond = async call => { if (call.headers) { liveActor = { ...actor, id: 8 }; return receipt(call); } return standard(call); };
  assert.equal(await view.render().run(input), null); assert.equal(storage.size, 1); assert.equal(view.render().receipt, null); view.unmount();
});
test('initial definite refusal requires current facts; a previously unknown request never becomes editable after rejection', async () => {
  reset(); let view = await ready(), input = await prepare(view); const standard = respond;
  respond = async call => { if (call.headers) throw new ApiError(409, 'manual_collection_source_occupied'); return standard(call); };
  await view.render().run(input); assert.equal(view.render().pending.phase, 'rejected'); assert.equal(await view.render().run(input), null); assert.ok(await view.render().finishRejected()); assert.equal(storage.size, 0); respond = standard;
  input = await prepare(view); assert.ok(await view.render().run(input)); assert.notEqual(writes()[0].headers['Idempotency-Key'], writes()[1].headers['Idempotency-Key']); view.unmount();
  reset(); view = await ready(); input = await prepare(view); respond = async call => { if (call.headers) throw new TypeError('lost'); return standard(call); }; await view.render().run(input);
  respond = async call => { if (call.headers) throw new ApiError(409, 'stale_manual_collection_quote'); return standard(call); }; await view.render().run(); assert.equal(view.render().pending.phase, 'uncertain'); assert.equal(await view.render().finishRejected(), null); assert.equal(storage.size, 1); view.unmount();
});
test('bad receipt, failed post-write read and idempotency conflict retain exact original request', async () => {
  for (const mismatch of [{ invoice_id: 52 }, { customer_id: 42 }, { amount_cents: 801 }, { received_at: '2026-01-01T00:01:00Z' }, { payment_id: 0 }, { collection_id: 0 }, { status: 'unpaid' }, { invoice_version: 1 }, { renewal_status: 'success' }]) {
    reset(); const view = await ready(), input = await prepare(view), standard = respond; respond = async call => call.headers ? { ...receipt(call), ...mismatch } : standard(call);
    await view.render().run(input); assert.equal(view.render().pending.phase, 'uncertain'); assert.equal(storage.size, 1); view.unmount();
  }
  reset(); let view = await ready(), input = await prepare(view); const standard = respond; let committed = false;
  respond = async call => { if (call.headers) { committed = true; return receipt(call); } if (committed && call.url === '/admin/invoices/51') throw new ApiError(400, 'read_failed'); return standard(call); };
  await view.render().run(input); assert.equal(view.render().pending.phase, 'uncertain'); assert.equal(storage.size, 1); view.unmount();
  reset(); view = await ready(); input = await prepare(view); respond = async call => { if (call.headers) throw new ApiError(409, 'idempotency_conflict'); return standard(call); }; await view.render().run(input); assert.equal(view.render().pending.phase, 'uncertain'); view.unmount();
});
test('double clicks, failed storage, corrupt recovery and changed owner block duplicate writes', async () => {
  reset(); let view = await ready(), input = await prepare(view); await Promise.all([view.render().run(input), view.render().run(input)]); assert.equal(writes().length, 1); view.unmount();
  reset(); view = await ready(); input = await prepare(view); failStorage = true; await view.render().run(input); assert.equal(writes().length, 0); view.unmount();
  reset(); storage.set('goidc:service-manual-collection:admin:7:31', '{bad'); view = await ready(); assert.equal(view.render().blocked, true); assert.equal(await view.render().preview(invoice(), details()), null); assert.equal(writes().length, 0); view.unmount();
});
test('persisted recovery refuses changed ownership or scope instead of opening a fresh intent', async () => {
  for (const field of ['owner', 'authority']) {
    reset(); let view = await ready(); const input = await prepare(view), standard = respond;
    respond = async call => { if (call.headers) throw new TypeError('lost response'); return standard(call); };
    await view.render().run(input); view.unmount(); const [key, raw] = [...storage][0], saved = JSON.parse(raw);
    saved[field] = field === 'owner' ? 2 : 'other actor/scope'; storage.set(key, JSON.stringify(saved));
    view = await ready(); assert.equal(view.render().blocked, true); assert.equal(await view.render().run(), null); assert.equal(writes().length, 1); assert.equal(storage.size, 1); view.unmount();
  }
});
test('unmount during submission keeps original receipt recovery without exposing a late success', async () => {
  reset(); const view = await ready(), input = await prepare(view), standard = respond;
  respond = async call => { if (call.headers) { view.unmount(); return receipt(call); } return standard(call); };
  assert.equal(await view.render().run(input), null); assert.equal(storage.size, 1); assert.equal(writes().length, 1); assert.equal(view.render().receipt, null);
});
