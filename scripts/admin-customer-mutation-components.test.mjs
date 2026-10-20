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
let current, actor, calls, storage, resources, respond, failStorage, refreshes, liveResources;
const cache = new Map();
const changed = (a, b) => !a || !b || a.length !== b.length || a.some((value, i) => value !== b[i]);
const react = {
  ...React,
  useCallback: callback => callback,
  useState(initial) { const state = current, index = state.cursor++; if (!(index in state.values)) state.values[index] = typeof initial === 'function' ? initial() : initial; return [state.values[index], next => { state.values[index] = typeof next === 'function' ? next(state.values[index]) : next; }]; },
  useRef(initial) { const state = current, index = state.cursor++; if (!(index in state.values)) state.values[index] = { current: initial }; return state.values[index]; },
  useEffect(effect, deps) { const state = current, index = state.cursor++, old = state.values[index]; if (changed(old?.deps, deps)) state.effects.push(() => { old?.cleanup?.(); state.values[index] = { deps, cleanup: effect() }; }); },
};
class ApiError extends Error { constructor(status, code, message = code) { super(message); this.status = status; this.code = code; } }
const control = name => props => React.createElement(name, props, props.children);
const Button = control('button'), Input = control('input'), Select = control('select');
const ui = new Proxy({}, { get: (_, name) => name === 'Button' ? Button : name === 'Input' ? Input : name === 'Select' ? Select : control(String(name)) });
function load(filename) {
  if (cache.has(filename)) return cache.get(filename).exports;
  const module = new Module(filename); cache.set(filename, module); module.filename = filename; module.paths = Module._nodeModulePaths(path.dirname(filename));
  const external = createRequire(filename);
  module.require = specifier => {
    if (specifier === 'react') return react;
    if (specifier === 'next/link') return { __esModule: true, default: control('a') };
    if (specifier === './workspace') return { Workspace: control('main'), PermissionGate: ({ children }) => children, useAdminPermission: () => true, useAdminIdentity: () => actor };
    if (specifier === './common') return { Loading: control('loading'), Failure: control('failure') };
    if (specifier === '@/hooks/use-resource') return { useResource: (...args) => liveResources ? load(path.join(web, 'src/hooks/use-resource.ts')).useResource(...args) : ({ data: resources.get(args[0]) ?? null, error: null, refresh() { refreshes.push(args[0]); } }) };
    if (specifier.includes('/ui/')) return ui;
    if (specifier === '@/lib/api') return { ApiError, authPortal: () => 'admin', api: async (url, options) => { const call = { url, ...options }; calls.push(call); return respond(call); } };
    if (specifier.startsWith('@/') || specifier.startsWith('.')) {
      const base = specifier.startsWith('@/') ? path.join(web, 'src', specifier.slice(2)) : path.resolve(path.dirname(filename), specifier);
      const source = [base, `${base}.tsx`, `${base}.ts`].find(candidate => existsSync(candidate));
      if (source && /\.tsx?$/.test(source)) return load(source);
    }
    return external(specifier);
  };
  module._compile(ts.transpileModule(readFileSync(filename, 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true } }).outputText, filename);
  return module.exports;
}
const { CustomerActions, CustomerSalesAction, CustomerProfileAction, CustomerGroupAction } = load(path.join(web, 'src/components/admin-customers.tsx'));
const { AdminCustomerResourcesView } = load(path.join(web, 'src/components/admin-customer-resources.tsx'));
const row = { id: 41, username: 'customer41', display_name: '旧列表姓名', email: 'old@example.test', status: 'active', sales_owner_id: null, sales_version: 2, group_id: null };
function mount(Component, props = {}) { const state = { cursor: 0, values: [], effects: [] }; return { props, render() { state.cursor = 0; current = state; let tree; try { tree = Component(props); } finally { current = undefined; } for (const effect of state.effects.splice(0)) effect(); return tree; }, unmount() { for (const value of state.values) value?.cleanup?.(); } }; }
function nodes(tree) { if (!tree || typeof tree !== 'object') return []; if (Array.isArray(tree)) return tree.flatMap(nodes); return [tree, ...React.Children.toArray(tree.props?.children).flatMap(nodes)]; }
function text(tree) { if (typeof tree === 'string' || typeof tree === 'number') return String(tree); if (!tree) return ''; if (Array.isArray(tree)) return tree.map(text).join(''); return React.Children.toArray(tree.props?.children).map(text).join(''); }
function button(tree, label) { const result = nodes(tree).find(item => item.type === Button && text(item).includes(label)); assert.ok(result, label); return result; }
function select(tree) { const result = nodes(tree).find(item => item.type === Select); assert.ok(result); return result; }
function notice(tree) { const result = nodes(tree).find(item => item.type?.name === 'CustomerMutationNotice'); assert.ok(result); return result.type(result.props); }
function form(tree) { const result = nodes(tree).find(item => item.type === 'form'); assert.ok(result); return result; }
const submit = tree => form(tree).props.onSubmit({ preventDefault() {} });
const settle = () => new Promise(resolve => setTimeout(resolve, 5));
function reset() {
  actor = { id: 7 }; calls = []; storage = new Map(); refreshes = []; failStorage = false; liveResources = false;
  resources = new Map([
    ['/admin/customers/41', { ...row, display_name: '当前详情姓名', email: 'current@example.test', profile_version: 11, sales_version: 9 }],
    ['/admin/customers/41/group', { customer_id: 41, group_id: null, group_membership_version: 6 }],
    ['/admin/customer-groups?options=true', [{ id: 3, name: 'VIP' }]],
    ['/admin/sales-owners', [{ id: 12, username: 'sales12', display_name: '销售十二' }]],
  ]);
  respond = async call => success(call);
  globalThis.window = { setTimeout, clearTimeout };
  globalThis.sessionStorage = { getItem: key => storage.get(key) ?? null, setItem(key, value) { if (failStorage) throw new Error('浏览器存储失败'); storage.set(key, value); }, removeItem: key => storage.delete(key) };
}
function success(call) {
  const body = JSON.parse(call.body), version = body.expected_version + 1;
  return call.url.endsWith('/group') ? { customer_id: 41, group_id: body.group_id, group_membership_version: version, replayed: false } : call.url.endsWith('/sales-owner') ? { customer_id: 41, sales_owner_id: body.sales_owner_id, sales_version: version, replayed: false } : { user_id: 41, profile_version: version, replayed: false };
}
async function ready(Component, extra = {}) { const view = mount(Component, { row, refresh() { refreshes.push('list'); }, onBusyChange() {}, ...extra }); view.render(); await settle(); view.render(); return view; }
async function sales() { const view = await ready(CustomerSalesAction); button(view.render(), '').props.onClick(); view.render(); return view; }

test('sales assignment uses fresh detail version and replays the frozen request after remount', async () => {
  reset(); const view = await sales(); select(view.render()).props.onValueChange('12');
  respond = async () => { throw new TypeError('network lost'); };
  await submit(view.render()); assert.equal(calls.length, 1);
  const first = calls[0]; assert.deepEqual(JSON.parse(first.body), { sales_owner_id: 12, expected_version: 9 }); assert.ok(first.headers['Idempotency-Key']); assert.equal(storage.size, 1);
  assert.equal(select(view.render()).props.disabled, true); view.unmount();
  resources.set('/admin/customers/41', { ...row, sales_owner_id: 12, sales_version: 10 });
  const restored = await sales(); assert.equal(select(restored.render()).props.value, '12');
  respond = async call => success(call); await submit(restored.render());
  assert.equal(calls[1].body, first.body); assert.equal(calls[1].headers['Idempotency-Key'], first.headers['Idempotency-Key']); assert.equal(storage.size, 0); restored.unmount();
});

test('group assignment reads its independent membership version and preserves explicit null', async () => {
  reset(); resources.set('/admin/customers/41/group', { customer_id: 41, group_id: 3, group_membership_version: 6 });
  const view = await ready(CustomerGroupAction); select(view.render()).props.onValueChange('none');
  await submit(view.render()); assert.deepEqual(JSON.parse(calls[0].body), { group_id: null, expected_version: 6 }); assert.ok(calls[0].headers['Idempotency-Key']);
  assert.equal(calls[0].url, '/admin/customers/41/group'); assert.equal(storage.size, 0); view.unmount();
});

test('profile and group are separate submissions, so a failed group does not replay a successful profile', async () => {
  reset(); const profile = await ready(CustomerProfileAction);
  assert.ok(nodes(profile.render()).some(node => node.type === CustomerGroupAction));
  await submit(profile.render());
  assert.deepEqual(JSON.parse(calls[0].body), { display_name: '当前详情姓名', email: 'current@example.test', status: 'active', expected_version: 11 });
  assert.equal(calls[0].method, 'PATCH'); assert.ok(calls[0].headers['Idempotency-Key']);
  const group = await ready(CustomerGroupAction); select(group.render()).props.onValueChange('3');
  respond = async () => { throw new ApiError(503, 'internal_error'); }; await submit(group.render()); const first = calls[1];
  resources.set('/admin/customers/41/group', { customer_id: 41, group_id: 3, group_membership_version: 7 });
  respond = async call => success(call); await submit(group.render());
  assert.equal(calls.length, 3); assert.equal(calls[2].body, first.body); assert.equal(calls[2].headers['Idempotency-Key'], first.headers['Idempotency-Key']);
  assert.equal(calls.filter(call => call.method === 'PATCH').length, 1); assert.equal(storage.size, 0); profile.unmount(); group.unmount();
});

test('idempotency conflicts and lost authority retain the original request without offering a replacement', async () => {
  for (const failure of [new ApiError(409, 'idempotency_conflict'), new ApiError(401, 'unauthorized'), new ApiError(403, 'permission_denied'), new ApiError(404, 'not_found')]) {
    reset(); const group = await ready(CustomerGroupAction); select(group.render()).props.onValueChange('3');
    respond = async () => { throw failure; }; await submit(group.render());
    const state = nodes(group.render()).find(node => node.type?.name === 'CustomerMutationNotice').props.mutation;
    assert.equal(state.rejected, false); assert.ok(state.pending); assert.equal(storage.size, 1); assert.equal(select(group.render()).props.disabled, true);
    assert.ok(!text(notice(group.render())).includes('重新读取并编辑')); group.unmount();
  }
});

test('a definitive version conflict requires explicit discard before reading a fresh version', async () => {
  reset(); const group = await ready(CustomerGroupAction); select(group.render()).props.onValueChange('3');
  respond = async () => { throw new ApiError(409, 'version_conflict'); }; await submit(group.render());
  assert.equal(storage.size, 1); assert.equal(button(group.render(), '使用原请求重试').props.disabled, true);
  button(notice(group.render()), '重新读取并编辑').props.onClick(); assert.equal(storage.size, 0);
  resources.set('/admin/customers/41/group', { customer_id: 41, group_id: null, group_membership_version: 8 });
  select(group.render()).props.onValueChange('3'); respond = async call => success(call); await submit(group.render());
  assert.equal(JSON.parse(calls[1].body).expected_version, 8); assert.notEqual(calls[1].headers['Idempotency-Key'], calls[0].headers['Idempotency-Key']); group.unmount();
});

test('pending drafts are isolated by real administrator and action across actor changes', async () => {
  reset(); const group = await ready(CustomerGroupAction); select(group.render()).props.onValueChange('3'); respond = async () => { throw new TypeError('lost'); }; await submit(group.render());
  const original = calls[0]; assert.ok([...storage.keys()][0].includes(':7:41:group'));
  actor = { id: 8 }; group.render(); await settle(); assert.equal(select(group.render()).props.disabled, false); assert.equal(select(group.render()).props.value, 'none'); assert.equal(nodes(group.render()).find(node => node.type?.name === 'CustomerMutationNotice').props.mutation.pending, null);
  actor = { id: 7 }; group.render(); await settle(); assert.equal(select(group.render()).props.disabled, true);
  respond = async call => success(call); await submit(group.render()); assert.equal(calls[1].body, original.body); assert.equal(calls[1].headers['Idempotency-Key'], original.headers['Idempotency-Key']); group.unmount();
});

test('unavailable persistence, corrupt recovery data and absent identity block submission', async () => {
  reset(); const group = await ready(CustomerGroupAction); select(group.render()).props.onValueChange('3'); failStorage = true; await submit(group.render()); assert.equal(calls.length, 0); group.unmount();
  reset(); storage.set('zjmfgo:customer-mutation:v1:7:41:group', '{broken'); const corrupt = await ready(CustomerGroupAction); assert.equal(button(corrupt.render(), '保存分组').props.disabled, true); await submit(corrupt.render()); assert.equal(calls.length, 0); corrupt.unmount();
  reset(); actor = null; const anonymous = await ready(CustomerGroupAction); select(anonymous.render()).props.onValueChange('3'); assert.equal(button(anonymous.render(), '保存分组').props.disabled, true); await submit(anonymous.render()); assert.equal(calls.length, 0); anonymous.unmount();
});

test('rapid duplicate submissions send once and a late response cannot replace another actor draft', async () => {
  reset(); const group = await ready(CustomerGroupAction); select(group.render()).props.onValueChange('3');
  let finish; respond = () => new Promise(resolve => { finish = resolve; });
  const tree = group.render(), first = submit(tree), duplicate = submit(tree);
  assert.equal(calls.length, 1); assert.equal(storage.size, 1); await duplicate;
  actor = { id: 8 }; group.render(); await settle();
  finish(success(calls[0])); await first;
  assert.equal(nodes(group.render()).find(node => node.type?.name === 'CustomerMutationNotice').props.mutation.pending, null);
  assert.equal(select(group.render()).props.value, 'none'); assert.equal(refreshes.filter(value => value === 'list').length, 0); group.unmount();
});

test('a malformed successful response remains unconfirmed and cannot discard the saved request', async () => {
  reset(); const group = await ready(CustomerGroupAction); select(group.render()).props.onValueChange('3');
  respond = async () => ({}); await submit(group.render());
  assert.equal(storage.size, 1); assert.equal(select(group.render()).props.disabled, true); assert.match(text(notice(group.render())), /缺少有效操作确认/);
  const first = calls[0]; respond = async call => success(call); await submit(group.render()); assert.equal(calls[1].body, first.body); assert.equal(calls[1].headers['Idempotency-Key'], first.headers['Idempotency-Key']); assert.equal(storage.size, 0); group.unmount();
});

test('recovery validates the exact action body and leaves corrupt saved intents blocked', async () => {
  const cases = [
    ['group', []], ['group', null], ['group', {}], ['group', { expected_version: 6 }],
    ['group', { group_id: null, expected_version: 0 }], ['group', { group_id: null, expected_version: 1.5 }], ['group', { group_id: null, expected_version: '6' }],
    ['group', { group_id: 0, expected_version: 6 }], ['group', { group_id: '3', expected_version: 6 }], ['group', { group_id: 3, expected_version: 6, extra: true }],
    ['sales-owner', { sales_owner_id: -1, expected_version: 9 }], ['sales-owner', { sales_owner_id: null, expected_version: Number.MAX_SAFE_INTEGER + 1 }], ['sales-owner', { expected_version: 9 }], ['sales-owner', { sales_owner_id: 12, expected_version: 9, group_id: 3 }],
    ['profile', { expected_version: 11 }], ['profile', { expected_version: 11, profile: {} }], ['profile', { expected_version: 11, display_name: '' }],
    ['profile', { expected_version: 11, email: null }], ['profile', { expected_version: 11, email: 'invalid' }], ['profile', { expected_version: 11, status: 'pending' }],
  ];
  for (const [action, body] of cases) {
    reset(); const key = `zjmfgo:customer-mutation:v1:7:41:${action}`, saved = JSON.stringify({ version: 1, key: 'frozen-original-key', body: JSON.stringify(body) }); storage.set(key, saved);
    const view = action === 'sales-owner' ? await sales() : await ready(action === 'profile' ? CustomerProfileAction : CustomerGroupAction);
    const mutation = nodes(view.render()).find(node => node.type?.name === 'CustomerMutationNotice').props.mutation;
    assert.equal(mutation.ready, false, `${action} ${JSON.stringify(body)}`); assert.equal(mutation.pending, null); assert.match(text(notice(view.render())), /恢复信息损坏/);
    await submit(view.render()); assert.equal(calls.length, 0); assert.equal(storage.get(key), saved); assert.ok(!text(notice(view.render())).includes('重新读取并编辑')); view.unmount();
  }
  for (const badKey of [' leading-original-key', 'trailing-original-key ', 'line\nbreak-key']) {
    reset(); const key = 'zjmfgo:customer-mutation:v1:7:41:group', saved = JSON.stringify({ version: 1, key: badKey, body: JSON.stringify({ group_id: 3, expected_version: 6 }) }); storage.set(key, saved);
    const view = await ready(CustomerGroupAction); await submit(view.render()); assert.equal(calls.length, 0); assert.equal(storage.get(key), saved); assert.match(text(notice(view.render())), /恢复信息损坏/); view.unmount();
  }
});

test('confirmation must match frozen target, exact version transition and a boolean replay marker', async () => {
  const actions = [
    ['group', CustomerGroupAction, [ { group_id: null }, { group_membership_version: 5 }, { group_membership_version: 8 }, { group_membership_version: '7' }, { replayed: undefined }, { replayed: 'false' } ]],
    ['sales-owner', CustomerSalesAction, [ { sales_owner_id: null }, { sales_version: 9 }, { sales_version: 11 }, { sales_version: '10' }, { replayed: undefined }, { replayed: 0 } ]],
    ['profile', CustomerProfileAction, [ { profile_version: 11 }, { profile_version: 13 }, { profile_version: '12' }, { replayed: undefined }, { replayed: null } ]],
  ];
  for (const [action, Component, failures] of actions) {
    for (const mismatch of failures) {
      reset(); const view = action === 'sales-owner' ? await sales() : await ready(Component);
      if (action !== 'profile') select(view.render()).props.onValueChange(action === 'group' ? '3' : '12');
      respond = async call => ({ ...success(call), ...mismatch }); await submit(view.render()); const first = calls[0];
      const mutation = nodes(view.render()).find(node => node.type?.name === 'CustomerMutationNotice').props.mutation;
      assert.equal(storage.size, 1, `${action} ${JSON.stringify(mismatch)}`); assert.ok(mutation.pending); assert.equal(mutation.rejected, false); assert.match(text(notice(view.render())), /缺少有效操作确认/);
      respond = async call => ({ ...success(call), replayed: true }); await submit(view.render()); assert.equal(calls[1].body, first.body); assert.equal(calls[1].headers['Idempotency-Key'], first.headers['Idempotency-Key']); assert.equal(storage.size, 0); view.unmount();
    }
  }
});

test('group replay may confirm a matching no-op at the original membership version', async () => {
  reset(); storage.set('zjmfgo:customer-mutation:v1:7:41:group', JSON.stringify({ version: 1, key: 'original-noop-group-key', body: JSON.stringify({ group_id: null, expected_version: 6 }) }));
  const view = await ready(CustomerGroupAction); respond = async call => ({ ...success(call), group_membership_version: 6, replayed: true }); await submit(view.render()); assert.equal(storage.size, 0); assert.equal(calls[0].headers['Idempotency-Key'], 'original-noop-group-key'); view.unmount();
});

test('the production action subtree remounts by actual administrator and aborts former resource reads', async () => {
  reset(); const actions = mount(CustomerActions, { row, refresh() {} });
  const first = actions.render(); assert.equal(first.key, '7:41');
  const firstTree = first.type(first.props); assert.ok(nodes(firstTree).some(node => node.type === CustomerSalesAction));
  actor = { id: 8 }; const next = actions.render(); assert.equal(next.key, '8:41'); assert.notEqual(next.key, first.key);
  actor = null; assert.equal(actions.render(), null); actions.unmount();
  // Exercise the real resource hook's unmount boundary reached by those keys.
  reset(); liveResources = true; const deferred = [];
  respond = call => new Promise(resolve => { deferred.push({ call, resolve }); });
  const old = await ready(CustomerSalesAction); button(old.render(), '').props.onClick(); old.render();
  assert.equal(deferred.length, 2); assert.ok(deferred.every(item => !item.call.signal.aborted)); old.unmount();
  assert.ok(deferred.every(item => item.call.signal.aborted));
  actor = { id: 8 }; const fresh = await ready(CustomerSalesAction); button(fresh.render(), '').props.onClick(); fresh.render(); assert.equal(deferred.length, 4);
  deferred[0].resolve(resources.get('/admin/sales-owners')); deferred[1].resolve(resources.get('/admin/customers/41')); await settle();
  assert.ok(!nodes(fresh.render()).some(node => node.type === 'form')); assert.equal(calls.filter(call => call.method === 'PUT').length, 0);
  fresh.unmount(); for (const item of deferred.slice(2)) item.resolve(null);
});

test('a confirmed mutation after unmount clears only its own receipt and does not refresh the new actor UI', async () => {
  reset(); const group = await ready(CustomerGroupAction); select(group.render()).props.onValueChange('3'); let finish;
  respond = () => new Promise(resolve => { finish = resolve; }); const request = submit(group.render()); group.unmount();
  actor = { id: 8 }; const fresh = await ready(CustomerGroupAction); finish(success(calls[0])); await request;
  assert.equal(refreshes.filter(item => item === 'list').length, 0); assert.equal(storage.size, 0); assert.equal(select(fresh.render()).props.value, 'none'); fresh.unmount();
});

async function resourceSales() {
  resources.set('/admin/customers/page?q=&limit=25&offset=0&sales_owner_id=none', { items: [row], total: 1, limit: 25, offset: 0 });
  const outer = mount(AdminCustomerResourcesView), child = nodes(outer.render()).find(node => node.type?.name === 'AdminCustomerResourcesForActor');
  assert.ok(child); const list = mount(child.type, child.props);
  const action = nodes(list.render()).find(node => node.type === CustomerSalesAction);
  assert.ok(action, 'resource pool must render the shared sales assignment action'); assert.equal(action.key, childKey('7:41'));
  const view = await ready(action.type, action.props); button(view.render(), '').props.onClick(); view.render();
  return { view, unmount() { view.unmount(); list.unmount(); outer.unmount(); } };
}
const childKey = key => React.Children.toArray(React.createElement('span', { key }))[0].key;

test('resource pool assignment uses the shared actor/customer intent and recovers it from the customer list', async () => {
  reset(); const pool = await resourceSales(); select(pool.view.render()).props.onValueChange('12');
  respond = async () => { throw new TypeError('resource pool connection lost'); }; await submit(pool.view.render());
  assert.equal(calls.length, 1); const first = calls[0];
  assert.equal(first.url, '/admin/customers/41/sales-owner'); assert.deepEqual(JSON.parse(first.body), { sales_owner_id: 12, expected_version: 9 });
  assert.ok(first.headers['Idempotency-Key']); assert.ok(storage.has('zjmfgo:customer-mutation:v1:7:41:sales-owner'));
  assert.equal(select(pool.view.render()).props.disabled, true); pool.unmount();
  resources.set('/admin/customers/41', { ...row, sales_owner_id: 12, sales_version: 10 });
  const list = await sales(); respond = async call => ({ ...success(call), replayed: true }); await submit(list.render());
  assert.equal(calls[1].body, first.body); assert.equal(calls[1].headers['Idempotency-Key'], first.headers['Idempotency-Key']); assert.equal(storage.size, 0); list.unmount();
});

test('resource pool restores an unresolved list assignment instead of replacing its body with the current row', async () => {
  reset(); const list = await sales(); select(list.render()).props.onValueChange('12');
  respond = async () => { throw new ApiError(503, 'internal_error'); }; await submit(list.render()); const first = calls[0]; list.unmount();
  resources.set('/admin/customers/41', { ...row, sales_owner_id: 12, sales_version: 10 });
  const pool = await resourceSales(); assert.equal(select(pool.view.render()).props.value, '12'); assert.equal(select(pool.view.render()).props.disabled, true);
  respond = async call => ({ ...success(call), replayed: true }); await submit(pool.view.render());
  assert.equal(calls[1].body, first.body); assert.equal(calls[1].headers['Idempotency-Key'], first.headers['Idempotency-Key']); assert.equal(storage.size, 0); pool.unmount();
});

test('resource pool reads and shared actions remount by actual administrator and do not render without identity', () => {
  reset(); resources.set('/admin/customers/page?q=&limit=25&offset=0&sales_owner_id=none', { items: [row], total: 1, limit: 25, offset: 0 });
  const outer = mount(AdminCustomerResourcesView);
  const first = nodes(outer.render()).find(node => node.type?.name === 'AdminCustomerResourcesForActor'); assert.equal(first.key, childKey('7'));
  const old = mount(first.type, first.props); assert.equal(nodes(old.render()).find(node => node.type === CustomerSalesAction).key, childKey('7:41'));
  actor = { id: 8 }; const second = nodes(outer.render()).find(node => node.type?.name === 'AdminCustomerResourcesForActor'); assert.equal(second.key, childKey('8')); assert.notEqual(second.key, first.key); old.unmount();
  const fresh = mount(second.type, second.props); assert.equal(nodes(fresh.render()).find(node => node.type === CustomerSalesAction).key, childKey('8:41'));
  actor = null; assert.ok(!nodes(outer.render()).some(node => node.type?.name === 'AdminCustomerResourcesForActor'));
  assert.equal(calls.length, 0); fresh.unmount(); outer.unmount();
});
