import assert from 'node:assert/strict';
import { readFileSync, existsSync } from 'node:fs';
import { createRequire, Module } from 'node:module';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const web = fileURLToPath(new URL('../web/', import.meta.url));
const require = createRequire(path.join(web, 'package.json'));
const ts = require('typescript'), React = require('react');
let current, actor, permissions, calls, storage, resources, respond, failStorage, refreshed, completed, resourceReads;
const cache = new Map();
const changed = (a, b) => !a || !b || a.length !== b.length || a.some((value, index) => value !== b[index]);
const react = {
  ...React,
  useState(initial) { const state = current, index = state.cursor++; if (!(index in state.values)) state.values[index] = typeof initial === 'function' ? initial() : initial; return [state.values[index], next => { state.values[index] = typeof next === 'function' ? next(state.values[index]) : next; }]; },
  useRef(initial) { const state = current, index = state.cursor++; if (!(index in state.values)) state.values[index] = { current: initial }; return state.values[index]; },
  useEffect(effect, deps) { const state = current, index = state.cursor++, previous = state.values[index]; if (changed(previous?.deps, deps)) state.effects.push(() => { previous?.cleanup?.(); state.values[index] = { deps, cleanup: effect() }; }); },
  useCallback(callback, deps) { const state = current, index = state.cursor++, previous = state.values[index]; if (!previous || changed(previous.deps, deps)) state.values[index] = { deps, callback }; return state.values[index].callback; },
};
class ApiError extends Error { constructor(status, code) { super(code); this.status = status; this.code = code; } }
const control = name => props => React.createElement(name, props, props.children);
const Button = control('button'), Input = control('input'), Textarea = control('textarea');
const ui = new Proxy({}, { get: (_, name) => name === 'Button' ? Button : name === 'Input' ? Input : name === 'Textarea' ? Textarea : control(String(name)) });
function load(filename) {
  if (cache.has(filename)) return cache.get(filename).exports;
  const module = new Module(filename); cache.set(filename, module);
  const external = createRequire(filename);
  module.require = name => {
    if (name === 'react') return react;
    if (name === 'next/link') return { __esModule: true, default: control('a') };
    if (name === './workspace') return { useAdminIdentity: () => actor, useAdminPermission: permission => permissions.has(permission) };
    if (name === './common') return { Loading: control('loading') };
    if (name.includes('/ui/')) return ui;
    if (name === '@/lib/admin-navigation') return { can: (user, permission) => user?.permissions.includes(permission) };
    if (name === '@/lib/api') return { ApiError, money: value => `¥${(value / 100).toFixed(2)}`, api: async (url, options) => { const call = { url, ...options }; calls.push(call); return respond(call); } };
    if (name === '@/hooks/use-resource') return { useResource: url => { if (url) resourceReads.push(url); return { data: url ? resources.get(url) ?? null : null, error: null, isLoading: false, refresh() { refreshed.push(url); } }; } };
    if (name.startsWith('@/') || name.startsWith('.')) {
      const base = name.startsWith('@/') ? path.join(web, 'src', name.slice(2)) : path.resolve(path.dirname(filename), name);
      const source = [base, `${base}.tsx`, `${base}.ts`].find(candidate => existsSync(candidate));
      if (source && /\.tsx?$/.test(source)) return load(source);
    }
    return external(name);
  };
  const expose = filename.endsWith('admin-customer-topup.tsx') ? '\nmodule.exports.CustomerTopupForActor = CustomerTopupForActor;' : '';
  module._compile(ts.transpileModule(readFileSync(filename, 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true } }).outputText + expose, filename);
  return module.exports;
}
const { CustomerTopupButton, CustomerTopupForActor } = load(path.join(web, 'src/components/admin-customer-topup.tsx'));
const optionsPath = '/admin/customers/41/topup-options';
const options = { enabled: true, min_topup_cents: 100, max_topup_cents: 10000, max_balance_cents: 0, balance_cents: 2300, reserved_cents: 0, available_capacity_cents: null, require_active_order: false, has_active_order: false, version: 6 };
function reset() {
  permissions = new Set(['invoices.view', 'invoices.manage']); actor = { id: 7, role: 'admin', permissions: [...permissions] };
  calls = []; storage = new Map(); failStorage = false; refreshed = []; completed = []; resourceReads = []; resources = new Map([[optionsPath, options]]);
  globalThis.window = { addEventListener() {}, removeEventListener() {}, confirm: () => true };
  globalThis.sessionStorage = { getItem: key => storage.get(key) ?? null, setItem(key, value) { if (failStorage) throw new Error('storage failed'); storage.set(key, value); }, removeItem: key => storage.delete(key) };
  respond = async call => call.url === '/me' ? actor : call.url === optionsPath ? options : receipt(call);
}
function receipt(call) { const body = JSON.parse(call.body); return { invoice_id: 99, invoice_no: 'T-test', amount_cents: body.amount_cents, status: 'unpaid', topup_status: 'created', version: 1, settings_version: body.expected_version, user_id: 41, replayed: false }; }
function mount(Component = CustomerTopupForActor, extra = {}) {
  const state = { cursor: 0, values: [], effects: [] }, props = { actorId: 7, customerId: 41, customerName: 'Test customer', onCreated: result => completed.push(result), ...extra };
  return { props, render() { current = state; state.cursor = 0; const tree = Component(props); current = null; state.effects.splice(0).forEach(effect => effect()); return tree; }, unmount() { state.values.forEach(value => value?.cleanup?.()); } };
}
const settle = () => new Promise(resolve => setTimeout(resolve, 2));
const nodes = tree => !tree || typeof tree !== 'object' ? [] : Array.isArray(tree) ? tree.flatMap(nodes) : [tree, ...React.Children.toArray(tree.props?.children).flatMap(nodes)];
const text = tree => typeof tree === 'string' || typeof tree === 'number' ? String(tree) : !tree ? '' : Array.isArray(tree) ? tree.map(text).join('') : React.Children.toArray(tree.props?.children).map(text).join('');
function button(tree, label) { const found = nodes(tree).find(node => node.type === Button && text(node).includes(label)); assert.ok(found, label); return found; }
async function ready(extra) { const view = mount(CustomerTopupForActor, extra); view.render(); await settle(); button(view.render(), '代建未付').props.onClick(); return view; }
function fill(view, amount = '12.34', note = '客户工单代建') {
  const tree = view.render(); nodes(tree).find(node => node.type === Input).props.onChange({ target: { value: amount } }); nodes(tree).find(node => node.type === Textarea).props.onChange({ target: { value: note } });
  nodes(view.render()).find(node => node.type === 'form').props.onSubmit({ preventDefault() {} });
}
const posts = () => calls.filter(call => call.method === 'POST');

test('topup entry requires both invoice permissions and a real administrator', () => {
  reset(); for (const allowed of [[], ['invoices.view'], ['invoices.manage']]) { permissions = new Set(allowed); const view = mount(CustomerTopupButton); assert.equal(view.render(), null); view.unmount(); }
  permissions = new Set(['invoices.view', 'invoices.manage']); actor = null; const view = mount(CustomerTopupButton); assert.equal(view.render(), null); view.unmount();
});
test('preview is read only and confirmation creates an unpaid invoice without crediting wallet', async () => {
  reset(); const view = mount(); view.render(); await settle(); view.render(); assert.deepEqual(resourceReads, []); button(view.render(), '代建未付').props.onClick(); view.render(); assert.ok(resourceReads.includes(optionsPath)); assert.equal(posts().length, 0); assert.match(text(view.render()), /不会直接增加钱包余额/);
  fill(view); assert.match(text(view.render()), /¥12.34/); assert.equal(posts().length, 0);
  await button(view.render(), '确认创建未付账单').props.onClick(); await settle();
  assert.equal(posts().length, 1); assert.deepEqual(JSON.parse(posts()[0].body), { amount_cents: 1234, expected_version: 6, note: '客户工单代建' });
  assert.equal(completed.length, 1); assert.equal(storage.size, 0); assert.match(text(view.render()), /创建回执不代表当前支付状态/); assert.ok(!calls.some(call => call.url.endsWith('/balance'))); view.unmount();
});
test('amount and qualification validation prevent invalid or floating point requests', async () => {
  for (const amount of ['0', '1.234', '1e2', '-1', '0.99', '100.01']) {
    reset(); const view = await ready(); fill(view, amount); assert.equal(nodes(view.render()).some(node => node.type === Button && text(node).includes('确认创建未付账单')), false, amount); assert.equal(posts().length, 0); view.unmount();
  }
  reset(); resources.set(optionsPath, { ...options, available_capacity_cents: 1000 }); const capacity = await ready(); fill(capacity); assert.match(text(capacity.render()), /超过当前可用充值容量/); capacity.unmount();
});
test('lost response and post authorization errors replay the same body and key after remount', async () => {
  for (const failure of [new TypeError('network'), new ApiError(401, 'unauthorized'), new ApiError(403, 'permission_denied'), new ApiError(503, 'unavailable')]) {
    reset(); const view = await ready(); fill(view); respond = async call => { if (call.url === '/me') return actor; throw failure; };
    button(view.render(), '确认创建未付账单').props.onClick(); await settle(); const first = posts()[0]; assert.ok(first); assert.equal(storage.size, 1); view.unmount();
    const restored = await ready(); assert.match(text(restored.render()), /原请求结果待核验/); respond = async call => call.url === '/me' ? actor : ({ ...receipt(call), replayed: true });
    button(restored.render(), '使用原请求核验').props.onClick(); await settle(); assert.equal(posts()[1].body, first.body); assert.equal(posts()[1].headers['Idempotency-Key'], first.headers['Idempotency-Key']); assert.equal(storage.size, 0); restored.unmount();
  }
});
test('409 requires explicit facts review and ending the rejected request before a new confirmation', async () => {
  reset(); const view = await ready(); fill(view); respond = async call => { if (call.url === '/me') return actor; throw new ApiError(409, 'version_conflict'); };
  button(view.render(), '确认创建未付账单').props.onClick(); await settle(); assert.equal(storage.size, 1); assert.equal(button(view.render(), '使用原请求核验').props.disabled, true);
  respond = async call => call.url === '/me' ? actor : ({ ...options, version: 7 }); button(view.render(), '读取最新设置').props.onClick(); await settle();
  assert.match(text(view.render()), /当前设置版本 7/); button(view.render(), '已核对，结束被拒绝请求').props.onClick(); await settle(); assert.equal(storage.size, 0); assert.ok(nodes(view.render()).some(node => node.type === 'form')); assert.equal(posts().length, 1); view.unmount();
});
test('malformed receipt and a lost current permission preserve the original intent', async () => {
  reset(); const view = await ready(); fill(view); respond = async call => call.url === '/me' ? actor : { ...receipt(call), amount_cents: 999 };
  button(view.render(), '确认创建未付账单').props.onClick(); await settle(); assert.equal(storage.size, 1); assert.equal(completed.length, 0);
  permissions.delete('invoices.manage'); assert.equal(button(view.render(), '使用原请求核验').props.disabled, true); button(view.render(), '使用原请求核验').props.onClick(); await settle(); assert.equal(posts().length, 1); view.unmount();
});
test('corrupt saved intent and failed storage stop new financial writes', async () => {
  reset(); storage.set('zjmfgo:admin-mutation:7:customer-topup:41', JSON.stringify({ path: '/admin/customers/42/topups', target: 42, kind: 'customer_topup_create', key: 'saved-key-123', body: '{}', rejected: false })); const corrupt = await ready(); assert.match(text(corrupt.render()), /恢复资料无法读取/); fill(corrupt); assert.equal(posts().length, 0); corrupt.unmount();
  reset(); const view = await ready(); fill(view); failStorage = true; button(view.render(), '确认创建未付账单').props.onClick(); await settle(); assert.equal(posts().length, 0); view.unmount();
});
test('rapid confirm clicks dispatch once and unmounted late results cannot refresh another customer', async () => {
  reset(); const view = await ready(); fill(view); let finish; respond = async call => call.url === '/me' ? actor : new Promise(resolve => { finish = resolve; });
  const confirm = button(view.render(), '确认创建未付账单'); confirm.props.onClick(); confirm.props.onClick(); await settle(); assert.equal(posts().length, 1); view.unmount();
  const other = await ready({ customerId: 42 }); finish(receipt(posts()[0])); await settle(); assert.equal(completed.length, 0); assert.equal(storage.size, 1); other.unmount();
});

test('disabled recharge and order prerequisites block new creation but permit original replay recovery', async () => {
  for (const settings of [{ ...options, enabled: false }, { ...options, require_active_order: true, has_active_order: false }]) {
    reset(); resources.set(optionsPath, settings); const view = await ready(); fill(view); assert.equal(posts().length, 0); assert.equal(nodes(view.render()).some(node => node.type === Button && text(node).includes('确认创建未付账单')), false); view.unmount();
  }
  reset(); storage.set('zjmfgo:admin-mutation:7:customer-topup:41', JSON.stringify({ path: '/admin/customers/41/topups', target: 41, kind: 'customer_topup_create', key: 'original-topup-key', body: JSON.stringify({ amount_cents: 1234, expected_version: 6, note: '已发送的原请求' }), rejected: false }));
  resources.set(optionsPath, { ...options, enabled: false }); const recovered = await ready(); button(recovered.render(), '使用原请求核验').props.onClick(); await settle(); assert.equal(posts().length, 1); assert.equal(posts()[0].headers['Idempotency-Key'], 'original-topup-key'); assert.equal(completed.length, 1); recovered.unmount();
});
