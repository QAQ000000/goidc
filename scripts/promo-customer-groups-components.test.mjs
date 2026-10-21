import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { createRequire, Module } from 'node:module';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const web = fileURLToPath(new URL('../web/', import.meta.url));
const require = createRequire(path.join(web, 'package.json'));
const ts = require('typescript');
const React = require('react');
const cache = new Map();
let current, calls, identityCalls, respond, storage, resourcePaths, groupPage, failStorage, orderResources;
const orderActor = { id: 7, role: 'admin', permissions: ['orders.manage', 'promos.manage', 'promos.view'] };
const orderContext = {
  actorId: orderActor.id,
  actorIdentity: JSON.stringify([orderActor.id, orderActor.role, [...orderActor.permissions].sort(), orderActor.customer_scope, orderActor.customer_ownerless, orderActor.ticket_scope, orderActor.ticket_unassigned]),
  customerId: null, customer: null, query: '', returnTo: '/admin/orders', error: null, setLocked() {},
};
const changed = (before, after) => !before || !after || before.length !== after.length || before.some((value, i) => value !== after[i]);
const react = {
  ...React,
  useState(initial) {
    const state = current, index = state.cursor++;
    if (!(index in state.values)) state.values[index] = typeof initial === 'function' ? initial() : initial;
    return [state.values[index], next => { state.values[index] = typeof next === 'function' ? next(state.values[index]) : next; }];
  },
  useRef(initial) {
    const state = current, index = state.cursor++;
    if (!(index in state.values)) state.values[index] = { current: initial };
    return state.values[index];
  },
  useEffect(effect, dependencies) {
    const state = current, index = state.cursor++, before = state.values[index];
    if (changed(before?.dependencies, dependencies)) state.effects.push(() => { before?.cleanup?.(); state.values[index] = { dependencies, cleanup: effect() }; });
  },
};
class ApiError extends Error { constructor(status, code, message) { super(message); this.status = status; this.code = code; } }
const control = name => props => React.createElement(name, props, props.children);
const Button = control('button');
const Input = control('input');
const ui = new Proxy({}, { get: (_, name) => name === 'Button' ? Button : name === 'Input' ? Input : control(String(name)) });
function load(filename) {
  if (cache.has(filename)) return cache.get(filename).exports;
  const module = new Module(filename);
  cache.set(filename, module);
  module.filename = filename;
  module.paths = Module._nodeModulePaths(path.dirname(filename));
  const external = createRequire(filename);
  module.require = specifier => {
    if (specifier === 'react') return react;
    if (specifier === 'next/navigation') return { useRouter: () => ({ push() {}, refresh() {} }) };
    if (specifier === './workspace') return { Workspace: control('main'), useAdminPermission: () => true };
    if (specifier === './common') return { cycles: { monthly: '月' }, Loading: () => null, Failure: () => null };
    if (specifier === '@/hooks/use-resource') return { useResource: url => { resourcePaths.push(url); return { data: url?.startsWith('/admin/promo-codes/customer-groups') ? groupPage : orderResources.get(url) ?? null, error: null, refresh() {} }; } };
    if (specifier.includes('/ui/')) return ui;
    if (specifier === '@/lib/api') return { ApiError, money: cents => `¥${(cents / 100).toFixed(2)}`, api: async (url, options = {}) => { const call = { url, ...options }; if (url === '/me') { identityCalls.push(call); return orderActor; } calls.push(call); return respond(call); } };
    if (specifier.startsWith('.') || specifier.startsWith('@/')) {
      const base = specifier.startsWith('@/') ? path.join(web, 'src', specifier.slice(2)) : path.resolve(path.dirname(filename), specifier);
      const source = [base, `${base}.tsx`, `${base}.ts`].find(candidate => existsSync(candidate));
      if (source && /\.tsx?$/.test(source)) return load(source);
    }
    return external(specifier);
  };
  const compiled = ts.transpileModule(readFileSync(filename, 'utf8'), { reportDiagnostics: true, compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true } });
  assert.deepEqual((compiled.diagnostics ?? []).filter(item => item.category === ts.DiagnosticCategory.Error), []);
  module._compile(compiled.outputText, filename);
  return module.exports;
}
const { AdminOrderCreateView } = load(path.join(web, 'src/components/admin-order-create.tsx'));
const OrderForm = AdminOrderCreateView().props.children.props.children(orderContext).type;
const { PromoCustomerGroups } = load(path.join(web, 'src/components/promo-customer-groups.tsx'));
function nodes(tree) {
  if (!tree || typeof tree !== 'object') return [];
  if (Array.isArray(tree)) return tree.flatMap(nodes);
  return [tree, ...React.Children.toArray(tree.props?.children).flatMap(nodes)];
}
function text(tree) {
  if (typeof tree === 'string' || typeof tree === 'number') return String(tree);
  if (!tree) return '';
  if (Array.isArray(tree)) return tree.map(text).join('');
  return React.Children.toArray(tree.props?.children).map(text).join('');
}
function mount(Component, props = {}) {
  const state = { cursor: 0, values: [], effects: [] };
  return { props, render() { state.cursor = 0; current = state; let tree; try { tree = Component(props); } finally { current = undefined; } for (const effect of state.effects.splice(0)) effect(); return tree; } };
}
function reset() {
  calls = []; identityCalls = []; resourcePaths = []; storage = new Map(); orderResources = new Map(); failStorage = false;
  groupPage = { items: [{ id: 1, name: 'VIP' }, { id: 2, name: '代理商' }], total: 12, limit: 10, offset: 0 };
  respond = async () => ({ id: 42, code: 'GROUPCODE' });
  globalThis.window = { addEventListener() {}, removeEventListener() {}, setTimeout, clearTimeout };
  globalThis.document = { addEventListener() {}, removeEventListener() {} };
  globalThis.sessionStorage = {
    getItem: key => storage.get(key) ?? null,
    setItem(key, value) { if (failStorage) throw new Error('重试信息无法保存'); storage.set(key, value); },
    removeItem: key => storage.delete(key),
  };
}
function dialog(props = {}) {
  const element = nodes(mount(OrderForm, { context: orderContext }).render()).find(node => node.type?.name === 'PromoCodeCreateDialog');
  assert.ok(element);
  return mount(element.type, { actorID: 7, open: true, onOpenChange() {}, onCreated() {}, ...props });
}
const settle = async () => { await new Promise(resolve => setImmediate(resolve)); };
const input = (tree, label) => { const node = nodes(tree).find(item => item.type === Input && item.props['aria-label'] === label); assert.ok(node, label); return node; };
const button = (tree, label) => { const node = nodes(tree).find(item => item.type === Button && text(item) === label); assert.ok(node, label); return node; };
function submit(view) { nodes(view.render()).find(item => item.type === 'form').props.onSubmit({ preventDefault() {} }); }
function draft(view, code = 'GROUPCODE') { input(view.render(), '新优惠码').props.onChange({ target: { value: code } }); nodes(view.render()).find(item => item.type === PromoCustomerGroups).props.onChange([2, 1]); }

test('coupon group selection searches its narrow API, canonicalizes across pages, and limits selections', () => {
  reset(); const view = mount(PromoCustomerGroups, { value: [2], onChange(ids) { view.props.value = ids; } });
  let tree = view.render();
  nodes(tree).find(node => node.type === 'input' && node.props['aria-label'] === '适用客户组 VIP').props.onChange({ target: { checked: true } });
  assert.deepEqual(view.props.value, [1, 2]);
  button(view.render(), '下一页客户组').props.onClick();
  assert.match(resourcePaths.at(-1), /offset=0$/); view.render(); assert.match(resourcePaths.at(-1), /offset=10$/);
  input(view.render(), '搜索适用客户组').props.onChange({ target: { value: '代理' } });
  tree = view.render(); assert.match(resourcePaths.at(-1), /q=%E4%BB%A3%E7%90%86&limit=10&offset=0$/);
  assert.match(text(tree), /#1 ×.*#2 ×/);
  button(tree, '清空客户组限制').props.onClick(); assert.deepEqual(view.props.value, []);
  assert.match(text(view.render()), /全部客户；不叠加客户组优惠/);
  view.props.value = Array.from({ length: 100 }, (_, index) => index + 2);
  tree = view.render();
  assert.equal(nodes(tree).find(node => node.type === 'input' && node.props['aria-label'] === '适用客户组 VIP').props.disabled, true);
  assert.equal(nodes(tree).find(node => node.type === 'input' && node.props['aria-label'] === '适用客户组 代理商').props.disabled, false);
  assert.ok(resourcePaths.every(url => url?.startsWith('/admin/promo-codes/customer-groups')));
});

test('unknown coupon creation survives closing and remounting, then retries identical bytes and key', async () => {
  reset(); let submissions = 0; const created = [];
  respond = async () => { if (++submissions === 1) throw new TypeError('network failed'); return { id: 42, code: 'GROUPCODE' }; };
  let view = dialog({ onCreated: code => created.push(code) }); draft(view); submit(view); await settle();
  assert.equal(calls.length, 1); const original = calls[0];
  assert.deepEqual(JSON.parse(original.body).applies_customer_group_ids, [2, 1]);
  assert.equal(nodes(view.render()).find(node => node.type === 'fieldset').props.disabled, true);
  assert.match(text(view.render()), /上次创建结果尚未确认/);
  button(view.render(), '取消').props.onClick();
  view = dialog({ onCreated: code => created.push(code) }); view.render(); await settle();
  assert.equal(calls.length, 1, 'remount must not automatically resend');
  assert.equal(input(view.render(), '新优惠码').props.value, 'GROUPCODE');
  submit(view); await settle();
  assert.equal(calls.length, 2); assert.equal(calls[1].body, original.body); assert.deepEqual(calls[1].headers, original.headers);
  assert.deepEqual(created, ['GROUPCODE']); assert.equal(storage.size, 0);
});

test('rapid duplicate coupon submissions share one in-flight request', async () => {
  reset(); let finish; respond = () => new Promise(resolve => { finish = resolve; });
  const view = dialog(); draft(view); const form = nodes(view.render()).find(node => node.type === 'form');
  form.props.onSubmit({ preventDefault() {} }); form.props.onSubmit({ preventDefault() {} });
  assert.equal(calls.length, 1); finish({ id: 42, code: 'GROUPCODE' }); await settle();
  assert.equal(storage.size, 0);
});

test('coupon conflict requires explicit abandonment while a definite rejection allows a new request', async () => {
  for (const conflict of [true, false]) {
    reset(); respond = async () => { throw new ApiError(409, conflict ? 'idempotency_conflict' : 'promo_code_exists', '请核对'); };
    const view = dialog(); draft(view); submit(view); await settle();
    if (conflict) {
      assert.equal(storage.size, 1); assert.equal(button(view.render(), '使用原请求重试').props.disabled, true);
      submit(view); await settle(); assert.equal(calls.length, 1);
      button(view.render(), '已核对，放弃原请求并重新编辑').props.onClick();
    }
    assert.equal(storage.size, 0);
    input(view.render(), '新优惠码').props.onChange({ target: { value: 'ANOTHER' } });
    respond = async () => ({ id: 43, code: 'ANOTHER' }); submit(view); await settle();
    assert.equal(calls.length, 2); assert.notEqual(calls[0].headers['Idempotency-Key'], calls[1].headers['Idempotency-Key']);
    assert.equal(JSON.parse(calls[1].body).code, 'ANOTHER');
  }
});

test('coupon creation refuses an unpersisted request and actor recovery remains isolated', async () => {
  reset(); failStorage = true; const view = dialog(); draft(view); submit(view); await settle();
  assert.equal(calls.length, 0); assert.match(text(view.render()), /重试信息无法保存/);
  failStorage = false; respond = async () => { throw new TypeError('network failed'); }; submit(view); await settle();
  assert.equal(storage.size, 1);
  const another = dialog({ actorID: 8 }); another.render(); await settle();
  assert.equal(input(another.render(), '新优惠码').props.value, '');
  assert.equal(calls.length, 1);
});

test('an incompatible coupon can be removed explicitly before requoting the same order', async () => {
  reset();
  const customer = { id: 17, name: '客户', email: 'client@example.test', status: 'active' };
  const product = { id: 11, name: '云主机', revision: 1, pricing: [{ billing_cycle: 'monthly', price_cents: 1000, active: true }] };
  orderResources.set('/admin/orders/create-options', { payment_providers: [], mail_enabled: false });
  orderResources.set('/admin/orders/create-options?kind=customers&q=&limit=10&offset=0', { items: [customer], total: 1, limit: 10, offset: 0 });
  orderResources.set('/admin/orders/create-options?kind=products&q=&limit=10&offset=0', { items: [product], total: 1, limit: 10, offset: 0 });
  const view = mount(OrderForm, { context: orderContext });
  nodes(view.render()).find(node => node.type === 'select' && node.props['aria-label'] === '客户').props.onChange({ target: { value: '17' } });
  nodes(view.render()).find(node => node.type === 'select' && node.props['aria-label'] === '添加其他产品').props.onChange({ target: { value: '11' } });
  input(view.render(), '优惠码').props.onChange({ target: { value: 'NO-STACK' } });
  respond = async () => { throw new ApiError(409, 'promo_group_discount_incompatible', '此优惠码不能与客户组减免叠加'); };
  submit(view); await settle();
  assert.equal(calls.length, 1); assert.equal(JSON.parse(calls[0].body).promo_code, 'NO-STACK');
  assert.match(text(view.render()), /此优惠码不能与客户组减免叠加/);
  respond = async () => ({ quote_hash: 'without-promo', subtotal_cents: 1000, discount_cents: 100, group_discount_cents: 100, promo_discount_cents: 0, total_cents: 900, items: [] });
  button(view.render(), '移除优惠码并重新报价').props.onClick(); await settle();
  assert.equal(calls.length, 2); const first = JSON.parse(calls[0].body), second = JSON.parse(calls[1].body);
  assert.equal(second.promo_code, ''); assert.deepEqual(second.items, first.items); assert.equal(second.customer_id, first.customer_id);
  assert.equal(input(view.render(), '优惠码').props.value, '');
  assert.equal(nodes(view.render()).some(node => node.type === Button && text(node) === '移除优惠码并重新报价'), false);
  assert.equal(identityCalls.length, 2); assert.ok(identityCalls.every(call => call.portal === 'admin'));
});
