import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { createRequire, Module } from 'node:module';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const web = fileURLToPath(new URL('../web/', import.meta.url));
const require = createRequire(path.join(web, 'package.json'));
const ts = require('typescript'), React = require('react'), cache = new Map();
let current, actor, liveActor, calls, storage, resources, errors, loadingReads, reads, refreshed, respond, failStorage;
const changed = (a, b) => !a || !b || a.length !== b.length || a.some((value, index) => value !== b[index]);
const hooks = {
  ...React,
  useState(initial) { const state = current, index = state.cursor++; if (!(index in state.values)) state.values[index] = typeof initial === 'function' ? initial() : initial; return [state.values[index], next => { state.values[index] = typeof next === 'function' ? next(state.values[index]) : next; }]; },
  useRef(initial) { const state = current, index = state.cursor++; if (!(index in state.values)) state.values[index] = { current: initial }; return state.values[index]; },
  useEffect(effect, deps) { const state = current, index = state.cursor++, previous = state.values[index]; if (changed(previous?.deps, deps)) state.effects.push(() => { previous?.cleanup?.(); state.values[index] = { deps, cleanup: effect() }; }); },
};
class ApiError extends Error { constructor(status, code) { super(code); this.status = status; this.code = code; } }
const state = () => ({ cursor: 0, values: [], effects: [] });
function within(state, action) { const previous = current; current = state; state.cursor = 0; let result; try { result = action(); } finally { current = previous; } state.effects.splice(0).forEach(effect => effect()); return result; }
const ui = new Proxy({}, { get: (_, name) => props => name === 'Dialog' && !props.open ? null : React.createElement(({ Button: 'button', Input: 'input', Textarea: 'textarea', Checkbox: 'checkbox', Table: 'table', TableRow: 'tr', TableCell: 'td', TableHead: 'th' })[name] ?? String(name).toLowerCase(), props, props.children) });
const allowed = permission => actor?.role === 'super_admin' || actor?.permissions.includes(permission);
function load(filename) {
  if (cache.has(filename)) return cache.get(filename).exports;
  const module = new Module(filename); cache.set(filename, module); const external = createRequire(filename);
  module.require = name => {
    if (name === 'react') return hooks;
    if (name === 'next/link') return { __esModule: true, default: props => React.createElement('a', props, props.children) };
    if (name === './workspace') return { Workspace: props => React.createElement('main', props, props.children), useAdminIdentity: () => actor, useAdminPermission: allowed };
    if (name.includes('/ui/')) return ui;
    if (name === './admin-pagination') return { AdminPagination: props => React.createElement('pagination', props) };
    if (name === '@/hooks/use-resource') return { useResource: url => { if (url) reads.push(url); return { data: url ? resources.get(url) ?? null : null, error: errors.get(url) ?? null, isLoading: loadingReads.has(url), refresh() { refreshed.push(url); } }; } };
    if (name === '@/hooks/use-admin-list-navigation') return { useAdminListNavigation() {
      const [draft, setDraft] = hooks.useState(''), [query, setQuery] = hooks.useState(''), [status, setStatus] = hooks.useState(''), [page, setPage] = hooks.useState(0), [extra, setExtra] = hooks.useState({ binding: '', limit: '25' });
      return { ready: true, draft, setDraft, query, setQuery, status, setStatus, page, setPage, extra, setExtra };
    } };
    if (name === '@/lib/api') return { ApiError, money: cents => `¥${(cents / 100).toFixed(2)}`, date: value => value, api: async (url, options = {}) => { const call = { url, ...options }; calls.push(call); return respond(call); } };
    if (name.startsWith('@/') || name.startsWith('.')) {
      const base = name.startsWith('@/') ? path.join(web, 'src', name.slice(2)) : path.resolve(path.dirname(filename), name);
      const source = [base, `${base}.tsx`, `${base}.ts`].find(candidate => existsSync(candidate));
      if (source && /\.tsx?$/.test(source)) return load(source);
    }
    return external(name);
  };
  const exposed = filename.endsWith('admin-manual-resources.tsx') ? '\nmodule.exports.ResourceEditor = ResourceEditor; module.exports.ReleaseResource = ReleaseResource;' : '';
  const compiled = ts.transpileModule(readFileSync(filename, 'utf8'), { reportDiagnostics: true, compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true } });
  assert.equal((compiled.diagnostics ?? []).filter(item => item.category === ts.DiagnosticCategory.Error).length, 0);
  module._compile(compiled.outputText + exposed, filename); return module.exports;
}
const { useManualResourceMutation } = load(path.join(web, 'src/components/admin-manual-resource-mutation.ts'));
const { AdminManualResourcesView, manualResourceCost, ResourceEditor, ReleaseResource } = load(path.join(web, 'src/components/admin-manual-resources.tsx'));
const resource = overrides => ({ id: 11, name: 'Test resource', resource_type: 'server', provider: 'Provider', region: 'CN', address: '192.0.2.10', upstream_id: 'machine-11', cost_cents: 1234, currency: 'CNY', upstream_expires_at: null, status: 'available', notes: '', version: 3, has_credentials: true, binding_id: null, service_id: null, customer_id: null, bound_at: null, created_at: '2026-10-21T00:00:00Z', updated_at: '2026-10-21T00:00:00Z', ...overrides });
const bound = () => resource({ binding_id: 19, service_id: 31, customer_id: 41, bound_at: '2026-10-21T00:00:00Z' });
const newInput = { kind: 'create', target: null, body: { name: 'Safe new resource', resource_type: 'server', cost_cents: 1234, notes: 'ordinary note' } };
const updateInput = { kind: 'update', target: 11, body: { expected_version: 3, notes: 'changed note' } };
const releaseInput = { kind: 'release', target: 11, serviceId: 31, bindingId: 19, body: { expected_version: 3, note: 'machine reclaimed' } };
function receipt(call) { const body = JSON.parse(call.body); return { id: 11, version: body.expected_version ? body.expected_version + 1 : 1, replayed: false, ...(call.url.endsWith('/release') ? { service_id: 31, binding_id: 19 } : {}) }; }
function reset() {
  actor = { id: 7, role: 'admin', permissions: ['resources.view', 'resources.manage', 'services.view'], customer_scope: 'all', customer_ownerless: true }; liveActor = actor;
  calls = []; storage = new Map(); resources = new Map(); errors = new Map(); loadingReads = new Set(); reads = []; refreshed = []; failStorage = false;
  resources.set('/admin/manual-resources?limit=25&offset=0', { items: [resource(), { ...bound(), id: 12 }], total: 27, limit: 25, offset: 0 });
  resources.set('/admin/manual-resources/11', resource());
  respond = async call => call.url === '/me' ? liveActor : call.method ? receipt(call) : resources.get(call.url) ?? { total: 27 };
  globalThis.window = { setTimeout, clearTimeout, addEventListener() {}, removeEventListener() {}, confirm: () => true };
  globalThis.sessionStorage = { getItem: key => storage.get(key) ?? null, setItem(key, value) { if (failStorage) throw new Error('storage failed'); storage.set(key, value); }, removeItem: key => storage.delete(key) };
}
const settle = () => new Promise(resolve => setTimeout(resolve, 5));
const writes = () => calls.filter(call => ['POST', 'PATCH'].includes(call.method));
function mountMutation() { const state = { cursor: 0, values: [], effects: [] }; return { render: () => within(state, () => useManualResourceMutation(actor)), unmount: () => state.values.forEach(value => value?.cleanup?.()) }; }
async function readyMutation() { const view = mountMutation(); view.render(); await settle(); assert.equal(view.render().ready, true); return view; }
class Renderer {
  instances = new Map(); nodes = [];
  constructor(Component = AdminManualResourcesView, props = {}) { this.Component = Component; this.props = props; }
  render() {
    this.nodes = []; const visited = new Set();
    const walk = (node, key = 'root') => {
      if (Array.isArray(node)) return node.map((child, index) => walk(child, `${key}/${child?.key ?? index}`));
      if (node === null || node === undefined || typeof node === 'boolean') return null;
      if (typeof node !== 'object') return String(node);
      if (typeof node.type === 'function') {
        const identity = `${key}:${node.type.name}:${node.key ?? ''}`; visited.add(identity);
        if (!this.instances.has(identity)) this.instances.set(identity, state());
        return walk(within(this.instances.get(identity), () => node.type(node.props)), `${identity}/result`);
      }
      const result = { type: node.type, props: node.props, children: walk(node.props?.children, `${key}/children`) }; this.nodes.push(result); return result;
    };
    this.tree = walk(React.createElement(this.Component, this.props));
    for (const [key, value] of this.instances) if (!visited.has(key)) { value.values.forEach(item => item?.cleanup?.()); this.instances.delete(key); }
    return this;
  }
  text(node = this.tree) { return Array.isArray(node) ? node.map(value => this.text(value)).join('') : typeof node === 'string' ? node : node ? this.text(node.children) : ''; }
  find(type, text) { const found = this.nodes.find(node => node.type === type && (text === undefined || this.text(node) === text)); assert.ok(found, `${type} ${text}`); return found.props; }
  button(text) { return this.find('button', text); }
  label(label) { const found = this.nodes.find(node => node.props?.['aria-label'] === label); assert.ok(found, label); return found.props; }
  unmount() { this.instances.forEach(value => value.values.forEach(item => item?.cleanup?.())); this.instances.clear(); }
}

test('inventory access, full-scope creation and dual status remain separate with server-side filters/pagination', async () => {
  reset(); actor = { ...actor, permissions: [] }; const denied = new Renderer().render(); assert.match(denied.text(), /无权限/); assert.equal(reads.length, 0); denied.unmount();
  reset(); actor = { ...actor, customer_scope: 'owned' }; liveActor = actor; const scoped = new Renderer().render(); await settle(); scoped.render(); assert.equal(scoped.nodes.some(node => node.type === 'button' && scoped.text(node) === '新建库存'), false); assert.match(scoped.text(), /未分配库存不可见/); scoped.unmount();
  reset(); const view = new Renderer().render(); await settle(); view.render(); assert.match(view.text(), /可用.*已占用|已占用.*可用/); assert.equal(view.nodes.filter(node => node.type === 'article').length, 2);
  assert.ok(view.nodes.some(node => node.type === 'a' && node.props.href === '/admin/resources'));
  view.find('pagination').onPageChange(1); view.render(); assert.ok(reads.includes('/admin/manual-resources?limit=25&offset=25'));
  view.label('搜索人工库存').onChange({ target: { value: 'provider & ip' } }); view.render(); view.nodes.find(node => node.type === 'form').props.onSubmit({ preventDefault() {} }); view.render();
  assert.ok(reads.includes('/admin/manual-resources?limit=25&offset=0&q=provider+%26+ip'));
  view.label('库存状态筛选').onChange({ target: { value: 'available' } }); view.render(); view.label('库存占用筛选').onChange({ target: { value: 'unbound' } }); view.render();
  assert.ok(reads.includes('/admin/manual-resources?limit=25&offset=0&q=provider+%26+ip&status=available&binding=unbound'));
  view.unmount();
});
test('inventory read failures and invalid records fail closed with region recovery while filters remain usable', async () => {
  const url = '/admin/manual-resources?limit=25&offset=0';
  for (const failure of [new Error('inventory temporarily unavailable'), new ApiError(403, 'forbidden')]) {
    reset(); errors.set(url, failure);
    const view = new Renderer().render(); await settle(); view.render();
    assert.ok(view.label('搜索人工库存'));
    assert.equal(view.nodes.some(node => node.type === 'article' || node.type === 'pagination'), false);
    assert.doesNotMatch(view.text(), /暂无符合条件|Test resource/);
    if (failure.status === 403) assert.match(view.text(), /没有查看人工库存列表的权限/);
    else { assert.match(view.text(), /inventory temporarily unavailable/); view.button('重新加载').onClick(); assert.ok(refreshed.includes(url)); }
    view.unmount();
  }
  reset(); resources.set(url, { items: [resource({ currency: 'USD' })], total: 1, limit: 25, offset: 0 });
  const invalid = new Renderer().render(); await settle(); invalid.render();
  assert.match(invalid.text(), /库存列表响应无效/);
  assert.equal(invalid.nodes.some(node => node.type === 'article' || node.type === 'pagination'), false);
  assert.doesNotMatch(invalid.text(), /暂无符合条件/);
  invalid.button('重新加载').onClick(); assert.ok(refreshed.includes(url)); invalid.unmount();
});
test('empty inventory is distinct from initial loading and retains server pagination', async () => {
  const url = '/admin/manual-resources?limit=25&offset=0';
  reset(); resources.delete(url); loadingReads.add(url);
  const view = new Renderer().render(); await settle(); view.render();
  assert.match(view.text(), /正在加载人工库存列表/);
  assert.doesNotMatch(view.text(), /暂无符合条件/);
  assert.equal(view.nodes.some(node => node.type === 'pagination'), false);
  loadingReads.delete(url); resources.set(url, { items: [], total: 0, limit: 25, offset: 0 }); view.render();
  assert.match(view.text(), /暂无符合条件的人工库存/); assert.equal(view.find('pagination').total, 0);
  assert.equal(view.nodes.some(node => node.type === 'article'), false); view.unmount();
});
test('inventory mobile cards retain identity, occupancy, cost and credentials metadata without financial status labels', async () => {
  reset(); const view = new Renderer().render(); await settle(); view.render();
  const cards = view.nodes.filter(node => node.type === 'article');
  assert.equal(cards.length, 2);
  assert.match(view.text(cards[0]), /Test resource.*#11.*server/);
  for (const expected of ['192.0.2.10', 'machine-11', 'Provider', 'CN', '可用', '未占用', '¥12.34', '凭据', '版本 3']) assert.ok(view.text(cards[0]).includes(expected), expected);
  assert.match(view.text(cards[1]), /可用.*已占用.*服务 #31.*客户 #41/);
  assert.doesNotMatch(view.text(), /可提现/);
  assert.equal(view.label('人工库存列表')['aria-label'], '人工库存列表'); view.unmount();
});
test('authority changes remount the inventory workspace and discard unsent write-only fields', async () => {
  reset(); const view = new Renderer().render(); await settle(); view.render(); view.button('新建库存').onClick(); view.render();
  view.nodes.filter(node => node.type === 'checkbox')[1].props.onCheckedChange(true); view.render();
  view.label('写入密码').onChange({ target: { value: 'private-unsent-password' } }); view.render();
  actor = { ...actor, permissions: ['resources.view'], customer_scope: 'owned' }; liveActor = actor;
  view.render(); await settle(); view.render();
  assert.equal(view.nodes.some(node => node.props?.['aria-label'] === '写入密码'), false);
  assert.equal(view.nodes.some(node => node.type === 'button' && view.text(node) === '新建库存'), false);
  assert.equal(writes().length, 0); assert.equal(storage.size, 0); view.unmount();
});
test('actual creation preview is read-only, secret writes are not echoed or persisted, and success refreshes current facts', async () => {
  reset(); const view = new Renderer().render(); await settle(); view.render(); view.button('新建库存').onClick(); view.render();
  view.label('名称').onChange({ target: { value: '新建测试库存' } }); view.label('实际资源标识').onChange({ target: { value: 'real-machine-1' } }); view.label('库存成本').onChange({ target: { value: '12.34' } }); view.render();
  view.nodes.find(node => node.type === 'checkbox').props.onCheckedChange(true); view.render(); view.label('写入账号').onChange({ target: { value: 'private-login' } });
  view.nodes.filter(node => node.type === 'checkbox')[1].props.onCheckedChange(true); view.render(); view.label('写入密码').onChange({ target: { value: 'private-password' } }); view.render();
  view.nodes.filter(node => node.type === 'form').at(-1).props.onSubmit({ preventDefault() {} }); view.render(); assert.equal(writes().length, 0); assert.match(view.text(), /本次写入，不显示内容/); assert.doesNotMatch(view.text(), /private-login|private-password/);
  let finish; respond = async call => call.url === '/me' ? liveActor : new Promise(resolve => { finish = resolve; }); view.button('确认保存库存').onClick(); await settle(); view.render(); assert.equal(writes().length, 1);
  const input = JSON.parse(writes()[0].body); assert.equal(input.cost_cents, 1234); assert.equal(input.username, 'private-login'); assert.equal(input.password, 'private-password'); assert.doesNotMatch([...storage.values()][0], /private-login|private-password|"body"/);
  finish(receipt(writes()[0])); await settle(); view.render(); assert.equal(storage.size, 0); assert.ok(refreshed.includes('/admin/manual-resources?limit=25&offset=0')); assert.match(view.text(), /操作回执已确认/); view.unmount();
});
test('actual conflict recovery rebases only the submitted public draft onto reviewed facts and requires fresh confirmation', async () => {
  reset(); const view = new Renderer().render(); await settle(); view.render(); view.button('编辑').onClick(); view.render();
  view.label('库存备注').onChange({ target: { value: 'operator intended note' } }); view.render(); view.nodes.filter(node => node.type === 'form').at(-1).props.onSubmit({ preventDefault() {} }); view.render();
  respond = async call => { if (call.url === '/me') return liveActor; throw new ApiError(409, 'manual_resource_version_conflict'); };
  view.button('确认保存库存').onClick(); await settle(); view.render(); const original = writes()[0]; assert.equal(JSON.parse(original.body).expected_version, 3);
  resources.set('/admin/manual-resources/11', resource({ version: 4, provider: 'changed elsewhere', cost_cents: 2000 }));
  respond = async call => call.url === '/me' ? liveActor : resources.get(call.url);
  view.button('读取当前库存事实').onClick(); await settle(); view.render(); assert.match(view.text(), /当前版本 4/);
  view.button('已核对，结束旧请求并重新编辑').onClick(); await settle(); view.render(); assert.equal(view.label('库存备注').value, 'operator intended note'); assert.equal(view.label('提供方').value, 'changed elsewhere'); assert.equal(view.label('库存成本').value, '20.00'); assert.equal(writes().length, 1);
  view.nodes.filter(node => node.type === 'form').at(-1).props.onSubmit({ preventDefault() {} }); view.render(); respond = async call => call.url === '/me' ? liveActor : receipt(call); view.button('确认保存库存').onClick(); await settle(); view.render();
  assert.deepEqual(JSON.parse(writes()[1].body), { expected_version: 4, notes: 'operator intended note' }); assert.notEqual(writes()[1].headers['Idempotency-Key'], original.headers['Idempotency-Key']); view.unmount();
});
test('money is converted with exact cents and no floating point parsing', () => {
  assert.equal(manualResourceCost('12.34'), 1234); assert.equal(manualResourceCost('90000000000000.00'), 9000000000000000);
  for (const value of ['1e2', '-1', '1.234', 'NaN', '90000000000000.01']) assert.throws(() => manualResourceCost(value));
});
test('public unknown requests restore identical body/key and authority failures never release them', async () => {
  for (const failure of [new TypeError('lost'), new ApiError(401, 'unauthorized'), new ApiError(403, 'forbidden'), new ApiError(404, 'not_found'), new ApiError(503, 'unavailable')]) {
    reset(); let view = await readyMutation(); respond = async call => { if (call.url === '/me') return liveActor; throw failure; };
    assert.equal(await view.render().run(updateInput), null); const original = writes()[0]; assert.ok(original); assert.equal(view.render().pending.phase, 'uncertain'); view.unmount();
    view = await readyMutation(); respond = async call => call.url === '/me' ? liveActor : { ...receipt(call), replayed: true };
    assert.ok(await view.render().run({ ...updateInput, body: { expected_version: 99 } })); assert.equal(writes()[1].body, original.body); assert.equal(writes()[1].headers['Idempotency-Key'], original.headers['Idempotency-Key']); assert.equal(storage.size, 0); view.unmount();
  }
});
test('secret input never enters browser storage and refreshed marker blocks new writes', async () => {
  reset(); let view = await readyMutation(); respond = async call => { if (call.url === '/me') return liveActor; throw new TypeError('lost'); };
  const input = { ...newInput, body: { ...newInput.body, username: 'secret-login', password: 'secret-password' } };
  await view.render().run(input); const original = writes()[0], saved = [...storage.values()][0];
  assert.doesNotMatch(saved, /secret-login|secret-password|ordinary note|"body"/); assert.equal(JSON.parse(saved).sensitive, true);
  respond = async call => call.url === '/me' ? liveActor : { ...receipt(call), replayed: true }; assert.ok(await view.render().run()); assert.equal(writes()[1].body, original.body); view.unmount();
  reset(); view = await readyMutation(); respond = async call => { if (call.url === '/me') return liveActor; throw new TypeError('lost'); }; await view.render().run(input); view.unmount();
  view = await readyMutation(); assert.equal(view.render().pending.body, undefined); await view.render().run(newInput); assert.equal(writes().length, 1); assert.equal(storage.size, 1); view.unmount();
});
test('409 holds original version until explicit fresh-facts review and a new intent', async () => {
  reset(); const view = await readyMutation(); respond = async call => { if (call.url === '/me') return liveActor; throw new ApiError(409, 'manual_resource_version_conflict'); };
  await view.render().run(updateInput); const original = writes()[0]; assert.equal(view.render().pending.phase, 'rejected'); assert.equal(await view.render().run(updateInput), null); assert.equal(await view.render().endRejected(), null);
  resources.set('/admin/manual-resources/11', resource({ version: 4 })); respond = async call => call.url === '/me' ? liveActor : resources.get(call.url);
  assert.equal((await view.render().review()).resource.version, 4); assert.equal((await view.render().endRejected()).resource.version, 4); assert.equal(storage.size, 0);
  respond = async call => call.url === '/me' ? liveActor : receipt(call); assert.ok(await view.render().run({ ...updateInput, body: { expected_version: 4, notes: 'reviewed' } })); assert.notEqual(writes()[1].headers['Idempotency-Key'], original.headers['Idempotency-Key']); view.unmount();
});
test('an unknown first result cannot become a releasable rejection after any later validation or conflict response', async () => {
  for (const failure of [new ApiError(400, 'invalid_manual_resource'), new ApiError(422, 'invalid'), new ApiError(409, 'manual_resource_version_conflict')]) {
    reset(); let view = await readyMutation(); respond = async call => { if (call.url === '/me') return liveActor; throw new TypeError('first result lost'); };
    await view.render().run(updateInput); const original = writes()[0]; view.unmount();
    view = await readyMutation(); respond = async call => { if (call.url === '/me') return liveActor; throw failure; };
    await view.render().run(); assert.equal(view.render().pending.phase, 'uncertain'); assert.equal(await view.render().review(), null); assert.equal(await view.render().endRejected(), null); assert.equal(storage.size, 1);
    assert.equal(writes()[1].body, original.body); assert.equal(writes()[1].headers['Idempotency-Key'], original.headers['Idempotency-Key']); view.unmount();
  }
});
test('live actor/scope/permission and malformed receipts keep intent, without invoices permissions', async () => {
  for (const change of [user => ({ ...user, id: 8 }), user => ({ ...user, permissions: ['resources.view'] }), user => ({ ...user, customer_scope: 'owned' })]) {
    reset(); const view = await readyMutation(); liveActor = change(actor); assert.equal(await view.render().run(updateInput), null); assert.equal(writes().length, 0); assert.equal(storage.size, 1); assert.ok(calls.every(call => call.portal === 'admin')); view.unmount();
  }
  for (const value of [{ id: 12, version: 4, replayed: false }, { id: 11, version: 8, replayed: false }, { id: 11, version: 4 }, { id: 11, version: 4, replayed: false, service_id: 99, binding_id: 19 }]) {
    reset(); const view = await readyMutation(); respond = async call => call.url === '/me' ? liveActor : value;
    assert.equal(await view.render().run(Object.hasOwn(value, 'service_id') ? releaseInput : updateInput), null); assert.equal(storage.size, 1); view.unmount();
  }
  reset(); const view = await readyMutation(); respond = async call => { if (call.url === '/me') return liveActor; liveActor = { ...actor, id: 8 }; return receipt(call); };
  assert.equal(await view.render().run(updateInput), null); assert.equal(storage.size, 1); assert.equal(writes().length, 1); view.unmount();
});
test('corrupt/unwritable storage, double clicks and unmounted completions fail safely', async () => {
  reset(); storage.set('goidc:manual-resource-intent:admin:7', '{bad'); let view = mountMutation(); view.render(); await settle(); assert.equal(view.render().ready, false); await view.render().run(newInput); assert.equal(writes().length, 0); view.unmount();
  reset(); view = await readyMutation(); failStorage = true; await view.render().run(newInput); assert.equal(writes().length, 0); assert.equal(view.render().blocked, true); view.unmount();
  reset(); view = await readyMutation(); let finish; respond = async call => call.url === '/me' ? liveActor : new Promise(resolve => { finish = resolve; });
  const action = view.render(), first = action.run(updateInput); await action.run(updateInput); await settle(); assert.equal(writes().length, 1); view.unmount(); finish(receipt(writes()[0])); assert.equal(await first, null); assert.equal(storage.size, 1);
});
test('bound editor writes only metadata and retired status cannot reactivate', async () => {
  reset(); const inputs = [], mutation = { frozen: false, run: async input => { inputs.push(input); return null; } };
  const editor = new Renderer(ResourceEditor, { resource: bound(), mutation, onSuccess() {} }).render();
  assert.equal(editor.label('IP 或主机名').disabled, true); assert.equal(editor.label('编辑库存状态').disabled, true); assert.doesNotMatch(editor.text(), /写入密码/);
  editor.label('库存成本').onChange({ target: { value: '25.67' } }); editor.render(); editor.find('form').onSubmit({ preventDefault() {} }); editor.render(); assert.equal(inputs.length, 0);
  editor.button('确认保存库存').onClick(); await settle(); assert.deepEqual(inputs[0].body, { expected_version: 3, cost_cents: 2567 }); editor.unmount();
  const retired = new Renderer(ResourceEditor, { resource: resource({ status: 'retired' }), mutation, onSuccess() {} }).render(); assert.equal(retired.label('编辑库存状态').disabled, true); retired.unmount();
});
test('editing a note preserves untouched expiry seconds/milliseconds and uses the original version', async () => {
  reset(); const inputs = [], mutation = { frozen: false, run: async input => { inputs.push(input); return null; } };
  const item = resource({ upstream_expires_at: '2026-11-01T03:04:56.789Z' });
  const editor = new Renderer(ResourceEditor, { resource: item, mutation, onSuccess() {} }).render();
  assert.equal(editor.label('上游到期时间').value, '2026-11-01T11:04');
  editor.label('库存备注').onChange({ target: { value: 'only the note changed' } }); editor.render(); editor.find('form').onSubmit({ preventDefault() {} }); editor.render(); editor.button('确认保存库存').onClick(); await settle();
  assert.deepEqual(inputs[0].body, { expected_version: 3, notes: 'only the note changed' }); editor.unmount();
});
test('release requires services.view, real terminated state, reason and separate confirmation', async () => {
  reset(); const inputs = [], mutation = { frozen: false, run: async input => { inputs.push(input); return null; } };
  const noView = new Renderer(ReleaseResource, { resource: bound(), actor: { ...actor, permissions: ['resources.view', 'resources.manage'] }, mutation, onSuccess() {} }).render();
  assert.match(noView.text(), /无服务查看权限/); assert.equal(reads.length, 0); assert.equal(noView.nodes.some(node => node.type === 'button'), false); noView.unmount();
  for (const status of ['active', 'suspended', 'queued']) {
    resources.set('/admin/services/31/management', { service_id: 31, status }); const denied = new Renderer(ReleaseResource, { resource: bound(), actor, mutation, onSuccess() {} }).render(); assert.equal(denied.nodes.some(node => node.type === 'button' && denied.text(node) === '核对释放影响'), false); denied.unmount();
  }
  resources.set('/admin/services/31/management', { service_id: 31, status: 'terminated' }); const allowed = new Renderer(ReleaseResource, { resource: bound(), actor, mutation, onSuccess() {} }).render();
  allowed.label('库存回收说明').onChange({ target: { value: '已回收机器并核对' } }); allowed.nodes.find(node => node.type === 'checkbox').props.onCheckedChange(true); allowed.render(); allowed.button('核对释放影响').onClick(); allowed.render(); assert.equal(inputs.length, 0);
  allowed.button('确认显式释放库存').onClick(); await settle(); assert.deepEqual(inputs[0], { ...releaseInput, body: { expected_version: 3, note: '已回收机器并核对' } }); allowed.unmount();
});
