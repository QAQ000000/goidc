import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createRequire, Module } from 'node:module';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const web = fileURLToPath(new URL('../web/', import.meta.url));
const require = createRequire(path.join(web, 'package.json'));
const ts = require('typescript');
let current, storage, calls, respond, failStorage;
const changed = (a, b) => !a || a.length !== b.length || a.some((value, index) => value !== b[index]);
const react = {
  useState(initial) { const state = current, index = state.cursor++; if (!(index in state.values)) state.values[index] = typeof initial === 'function' ? initial() : initial; return [state.values[index], next => { state.values[index] = typeof next === 'function' ? next(state.values[index]) : next; }]; },
  useRef(initial) { const state = current, index = state.cursor++; if (!(index in state.values)) state.values[index] = { current: initial }; return state.values[index]; },
  useEffect(effect, deps) { const state = current, index = state.cursor++, previous = state.values[index]; if (changed(previous?.deps, deps)) state.effects.push(() => { previous?.cleanup?.(); state.values[index] = { deps, cleanup: effect() }; }); },
};
class ApiError extends Error { constructor(status, code) { super(code); this.status = status; this.code = code; } }
const filename = path.join(web, 'src/components/admin-customer-detail-mutation.ts');
const module = new Module(filename);
module.require = name => name === 'react' ? react : name === '@/lib/api' ? { ApiError, api: async (url, options) => { const call = { url, ...options }; calls.push(call); return respond(call); } } : require(name);
module._compile(ts.transpileModule(readFileSync(filename, 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText, filename);
const { useCustomerDetailMutation } = module.exports;
function mount(actor = 7, action = 'profile') {
  const state = { cursor: 0, values: [], effects: [] };
  return { actor, customerID: 41, render() { state.cursor = 0; current = state; const result = useCustomerDetailMutation(this.actor, this.customerID, action); current = null; state.effects.splice(0).forEach(effect => effect()); return result; }, unmount() { state.values.forEach(value => value?.cleanup?.()); } };
}
const settle = () => new Promise(resolve => setTimeout(resolve, 5));
async function ready(action = 'profile') { const view = mount(7, action); view.render(); await settle(); return view; }
function reset() {
  storage = new Map(); calls = []; failStorage = false;
  globalThis.window = { setTimeout, clearTimeout };
  globalThis.sessionStorage = { getItem: key => storage.get(key) ?? null, setItem(key, value) { if (failStorage) throw new Error('storage unavailable'); storage.set(key, value); }, removeItem: key => storage.delete(key) };
  respond = async call => ({ user_id: 41, profile_version: JSON.parse(call.body).expected_version + 1, replayed: false });
}
const payload = { expected_version: 11, profile: { admin_note: 'safe note' } };

test('detail profile restores the exact body and key after a lost response and remount', async () => {
  reset(); const view = await ready(); respond = async () => { throw new TypeError('network'); };
  await view.render().run('/admin/customers/41', 'PATCH', payload); const first = calls[0];
  assert.equal(view.render().uncertain, true); assert.equal(storage.size, 1); view.unmount();
  const restored = await ready(); respond = async () => ({ user_id: 41, profile_version: 12, replayed: true });
  assert.ok(await restored.render().run('/admin/customers/41', 'PATCH', { expected_version: 99 }));
  assert.equal(calls[1].body, first.body); assert.equal(calls[1].headers['Idempotency-Key'], first.headers['Idempotency-Key']); assert.equal(storage.size, 0); restored.unmount();
});
test('authority errors and malformed confirmations retain the saved request', async () => {
  for (const failure of [new ApiError(401, 'unauthorized'), new ApiError(403, 'permission_denied'), new ApiError(404, 'not_found'), new ApiError(409, 'idempotency_conflict'), {}]) {
    reset(); const view = await ready(); respond = async () => { if (failure instanceof Error) throw failure; return failure; };
    assert.equal(await view.render().run('/admin/customers/41', 'PATCH', payload), null); assert.equal(view.render().uncertain, true); assert.equal(storage.size, 1); view.unmount();
  }
});
test('version refusal preserves form ownership and requires a reviewed new intent', async () => {
  reset(); const view = await ready(); respond = async () => { throw new ApiError(409, 'stale_customer_profile'); };
  await view.render().run('/admin/customers/41', 'PATCH', payload);
  assert.equal(view.render().uncertain, false); assert.equal(view.render().conflict, true); assert.equal(storage.size, 0);
  await view.render().run('/admin/customers/41', 'PATCH', { expected_version: 12, profile: { admin_note: 'reviewed' } }); assert.equal(calls.length, 1);
  view.render().acknowledgeConflict(); assert.equal(view.render().conflict, false);
  respond = async () => ({ user_id: 41, profile_version: 13, replayed: false });
  assert.ok(await view.render().run('/admin/customers/41', 'PATCH', { expected_version: 12, profile: { admin_note: 'reviewed' } })); assert.notEqual(calls[1].headers['Idempotency-Key'], calls[0].headers['Idempotency-Key']); view.unmount();
});
test('corrupt saved targets and unavailable storage prevent network submission', async () => {
  reset(); storage.set('goidc:detail-intent:admin:7:41:profile', JSON.stringify({ path: '/admin/customers/42', method: 'PATCH', key: 'original-key', body: JSON.stringify(payload) }));
  const corrupt = await ready(); assert.equal(corrupt.render().ready, false); await corrupt.render().run('/admin/customers/41', 'PATCH', payload); assert.equal(calls.length, 0); corrupt.unmount();
  reset(); const view = await ready(); failStorage = true; await view.render().run('/admin/customers/41', 'PATCH', payload); assert.equal(calls.length, 0); assert.equal(view.render().ready, false); view.unmount();
});
test('contact creates and deletes validate identity and are isolated from profile intents', async () => {
  reset(); const view = await ready('contacts'); respond = async () => ({ id: 9, customer_id: 41, version: 1, replayed: false });
  assert.ok(await view.render().run('/admin/customers/41/contacts', 'POST', { email: 'contact@example.test', username: 'Contact' }));
  respond = async () => ({ id: 10, customer_id: 41, deleted: true, replayed: false });
  assert.equal(await view.render().run('/admin/customers/41/contacts/9', 'DELETE', { expected_version: 1 }), null); assert.equal(view.render().uncertain, true);
  assert.ok([...storage.keys()][0].endsWith(':contacts')); view.unmount();
});
test('late confirmations cannot update another actor and duplicate clicks send once', async () => {
  reset(); const view = await ready(); let finish; respond = () => new Promise(resolve => { finish = resolve; });
  const mutation = view.render(), first = mutation.run('/admin/customers/41', 'PATCH', payload), second = mutation.run('/admin/customers/41', 'PATCH', payload);
  await second; assert.equal(calls.length, 1); view.actor = 8; view.render(); await settle();
  finish({ user_id: 41, profile_version: 12, replayed: false }); assert.equal(await first, null); assert.equal(view.render().uncertain, false); view.unmount();
});

test('object switches abort visible confirmation and preserve other object intents', async () => {
  reset(); const view = await ready(); let finish; respond = () => new Promise(resolve => { finish = resolve; });
  const first = view.render().run('/admin/customers/41', 'PATCH', payload);
  view.customerID = 42; view.render(); await settle();
  finish({ user_id: 41, profile_version: 12, replayed: false }); assert.equal(await first, null);
  assert.equal(view.render().uncertain, false); assert.equal(view.render().ready, true); assert.equal(storage.size, 0); view.unmount();
});

const React = require('react');
let visiblePermissions = new Set(), reads = [];
const uiControl = name => props => React.createElement(name, props, props.children);
const ui = new Proxy({}, { get: (_, name) => uiControl(String(name).toLowerCase()) });
const detailFilename = path.join(web, 'src/components/admin-customer-detail.tsx');
const detailModule = new Module(detailFilename);
detailModule.require = name => {
  if (name === 'react') return react;
  if (name === 'next/link') return { __esModule: true, default: uiControl('a') };
  if (name === 'next/navigation') return {};
  if (name === './workspace') return { useAdminPermission: permission => visiblePermissions.has(permission) };
  if (name === '@/hooks/use-resource') return { useResource: url => { if (url) reads.push(url); return { data: { items: [], total: 0 }, error: null, isLoading: false, refresh() {} }; } };
  if (name === '@/lib/api') return { ApiError };
  if (name.startsWith('./ui/')) return ui;
  if (name.startsWith('./') || name.startsWith('@/')) return {};
  return require(name);
};
detailModule._compile(ts.transpileModule(readFileSync(detailFilename, 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true } }).outputText + '\nmodule.exports.CustomerRelatedPanel = CustomerRelatedPanel;', detailFilename);
function renderPanel(permissions, initialOrders = false) {
  visiblePermissions = new Set(permissions); reads = []; current = { cursor: 0, values: [], effects: [] };
  function walk(node) {
    if (Array.isArray(node)) return node.forEach(walk);
    if (!node || typeof node !== 'object') return;
    if (typeof node.type === 'function') return walk(node.type(node.props));
    React.Children.toArray(node.props?.children).forEach(walk);
  }
  walk(React.createElement(detailModule.exports.CustomerRelatedPanel, { customerId: 41, tab: 'invoices', initialOrders }));
  current = null;
  return reads;
}
test('billing detail independently loads the authorized invoice or order section', () => {
  assert.deepEqual(renderPanel(['customers.view', 'orders.view']), ['/admin/orders/page?customer_id=41&limit=25&offset=0']);
  assert.deepEqual(renderPanel(['customers.view', 'orders.view'], true), ['/admin/orders/page?customer_id=41&limit=25&offset=0']);
  assert.deepEqual(renderPanel(['customers.view', 'invoices.view'], true), ['/admin/invoices/page?customer_id=41&limit=25&offset=0']);
  assert.deepEqual(renderPanel(['customers.view', 'invoices.view', 'orders.view']), ['/admin/invoices/page?customer_id=41&limit=25&offset=0']);
  assert.deepEqual(renderPanel(['customers.view', 'invoices.view', 'orders.view'], true), ['/admin/orders/page?customer_id=41&limit=25&offset=0']);
  assert.deepEqual(renderPanel(['customers.view']), []);
});
