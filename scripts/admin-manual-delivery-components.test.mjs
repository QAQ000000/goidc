import assert from 'node:assert/strict';
import { readFileSync, existsSync } from 'node:fs';
import { createRequire } from 'node:module';
import path from 'node:path';
import test from 'node:test';
import vm from 'node:vm';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const require = createRequire(path.join(root, 'web/package.json'));
const ts = require('typescript'), React = require('react');
class ApiError extends Error { constructor(status, code, message) { super(message); Object.assign(this, { status, code }); } }
const tick = () => new Promise(resolve => setTimeout(resolve, 1));
const service = overrides => ({ id: 31, name: '人工服务', version: 3, provision_mode: 'manual', status: 'provisioning', invoice_status: 'paid', jobs: [{ id: 71, action: 'create', status: 'waiting_manual' }], ...overrides });
const resource = overrides => ({ id: 11, name: '独立库存', address: '192.0.2.11', provider: '测试提供方', region: 'CN', upstream_id: 'resource-11', version: 5, status: 'available', service_id: null, binding_id: null, has_credentials: true, ...overrides });
// A small keyed renderer executes the actual function components and hooks. Each
// component gets its own hooks, and key changes run effect cleanup before late
// transports settle. It does not replace any create/context business logic.
function fixture(respond, component = 'delivery') {
  const query = '';
  let current, rendered, nextType = 0, uuidCounter = 0;
  const instances = new Map(), types = new Map(), effects = [], cache = new Map();
  const calls = [], navigation = [], storage = new Map(), prompts = [], listeners = new Map();
  const env = { query, actor: { id: 7, role: 'admin', permissions: ['services.manage', 'services.view', 'resources.view', 'resources.manage'], customer_scope: 'all' }, calls, navigation, storage, prompts, service: service(), inventory: resource(), refreshes: 0, failStorage: false };
  const changed = (previous, next) => !previous || !next || previous.length !== next.length || previous.some((value, index) => !Object.is(value, next[index]));
  const hooks = {
    ...React,
    useState(initial) { const instance = current, index = instance.cursor++; if (!(index in instance.values)) instance.values[index] = typeof initial === 'function' ? initial() : initial; return [instance.values[index], next => { instance.values[index] = typeof next === 'function' ? next(instance.values[index]) : next; }]; },
    useRef(initial) { const index = current.cursor++; return current.values[index] ??= { current: initial }; },
    useEffect(callback, dependencies) { const instance = current, index = instance.cursor++, previous = instance.values[index]; if (changed(previous?.dependencies, dependencies)) effects.push(() => { previous?.cleanup?.(); instance.values[index] = { dependencies, cleanup: callback() }; }); },
    useMemo(factory, dependencies) { const index = current.cursor++, previous = current.values[index]; if (changed(previous?.dependencies, dependencies)) current.values[index] = { dependencies, value: factory() }; return current.values[index].value; },
    useCallback(callback, dependencies) { return hooks.useMemo(() => callback, dependencies); },
    useId() { return hooks.useMemo(() => `id-${++uuidCounter}`, []); },
  };
  const primitive = tag => props => React.createElement(tag, props, props.children);
  const Button = primitive('button'), Input = primitive('input'), Textarea = primitive('textarea'), Link = primitive('a');
  const ui = new Proxy({ Button, Input, Textarea, Dialog: props => props.open ? props.children : null }, { get: (target, name) => target[name] ??= primitive(String(name)) });
  const browser = { location: { search: `?${query}` }, confirm: message => { prompts.push(message); return true; }, setTimeout, clearTimeout, addEventListener: (name, listener) => listeners.set(`window:${name}`, listener), removeEventListener: name => listeners.delete(`window:${name}`) };
  const document = { addEventListener: (name, listener) => listeners.set(`document:${name}`, listener), removeEventListener: name => listeners.delete(`document:${name}`) };
  let permissions;
  async function api(endpoint, options = {}) {
    calls.push({ endpoint, options });
    if (respond) { const override = respond(endpoint, options, env); if (override !== undefined) return await override; }
    if (endpoint === '/me') return env.serverActor ?? env.actor;
    if (endpoint === '/admin/services/31/credentials') return { password: 'secret-password' };
    if (endpoint === '/admin/services/31/delivery') return { service_id: 31, job_id: JSON.parse(options.body).job_id, action: env.service.jobs[0].action, status: 'succeeded', replayed: false };
    if (endpoint === '/admin/services/31') return env.service;
    if (endpoint === '/admin/manual-resources/11') return env.inventory;
    if (endpoint.startsWith('/admin/manual-resources?')) { const offset = Number(new URL(endpoint, 'https://local.test').searchParams.get('offset')); return { items: [offset ? resource({ id: 12, name: '第二页库存' }) : env.inventory], total: 11, limit: 10, offset }; }
    throw new Error(`Unexpected API ${endpoint}`);
  }
  const mocks = {
    react: hooks,
    'next/navigation': { useRouter: () => ({ push: href => navigation.push(href), refresh() {} }), useSearchParams: () => new URLSearchParams(env.query) },
    'next/link': { default: Link },
    'radix-ui': { Tabs: new Proxy({}, { get: (_, name) => primitive(String(name)) }) },
    '@/hooks/use-admin-list-navigation': { useAdminListReturnHref: () => '/admin/services' },
    'lucide-react': new Proxy({}, { get: (_, name) => primitive(`icon-${String(name)}`) }),
    '@/lib/api': { api, ApiError, authPortal: () => 'admin', money: cents => `¥${cents / 100}`, date: String, upload: async () => ({}) },
    './workspace': { Workspace: props => props.children, useAdminIdentity: () => env.actor, useAdminPermission: permission => permissions.can(env.actor, permission), PermissionGate: props => props.children },
    './common': { Failure: ({ error }) => React.createElement('p', { role: 'alert' }, error.message), Loading: () => React.createElement('p', null, 'Loading'), cycles: { monthly: '月付' } },
    './purchase-discount-details': { PurchaseDiscountDetails: () => null, PurchaseDiscountSummary: () => null },
    './product-option-choice': { ProductOptionChoice: () => null },
    './supplier-delivery': { SupplierServiceDelivery: () => null },
    './nokvm-delivery': { NOKVMServiceDelivery: () => null, isPowerAction: () => false, serviceActionLabels: {} },
    './service-activity': { ServiceActivityPanel: () => null },
    './configuration-upgrades': { ServiceConfigurationChanges: () => null },
    './purchase-fields': { PurchaseFieldDetails: () => null },
    './renewal-benefit': { RenewalBenefitDetails: () => null },
    './promo-customer-groups': { PromoCustomerGroups: () => null },
    './catalog-admin': { catalogSelectStyle: '' },
    './admin-pagination': { AdminPagination: () => null },
  };
  function load(relative) {
    if (cache.has(relative)) return cache.get(relative).exports;
    const filename = path.join(root, relative), module = { exports: {} }; cache.set(relative, module);
    const { outputText } = ts.transpileModule(readFileSync(filename, 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX } });
    const exposed = relative.endsWith('admin-service-detail.tsx') ? '\nmodule.exports.CredentialsField = CredentialsField;' : '';
    vm.runInNewContext(outputText + exposed, {
      module, exports: module.exports, URLSearchParams, URL, Number, Error, TextEncoder, FormData, AbortController, crypto: { randomUUID: () => `00000000-0000-4000-8000-${String(++uuidCounter).padStart(12, "0")}` },
      window: browser, document, sessionStorage: { getItem: key => storage.get(key) ?? null, setItem: (key, value) => { if (env.failStorage) throw new Error('storage failed'); storage.set(key, value); }, removeItem: key => storage.delete(key) },
      require: id => {
        if (Object.hasOwn(mocks, id)) return mocks[id];
        if (id.includes('/ui/')) return ui;
        if (id.startsWith('.') || id.startsWith('@/')) {
          const source = id.startsWith('@/') ? `web/src/${id.slice(2)}` : path.join(path.dirname(relative), id);
          for (const candidate of [`${source}.ts`, `${source}.tsx`]) if (existsSync(path.join(root, candidate))) return load(candidate);
        }
        return require(id);
      },
    }, { filename });
    return module.exports;
  }
  permissions = load('web/src/lib/admin-navigation.ts'); mocks['@/lib/admin-navigation'] = permissions;
  const Component = component === 'credentials' ? load('web/src/components/admin-service-detail.tsx').CredentialsField : load('web/src/components/admin-manual-delivery.tsx').ManualDelivery;
  function expand(element, location, seen) {
    if (!element || typeof element !== 'object') return element;
    if (Array.isArray(element)) return element.map((child, index) => expand(child, `${location}:${index}`, seen));
    if (!element.type) return element;
    const keyed = `${location}:${element.key ?? ''}`;
    if (typeof element.type === 'function') {
      if (!types.has(element.type)) types.set(element.type, nextType++);
      const id = `${keyed}:component${types.get(element.type)}`;
      let instance = instances.get(id); if (!instance) { instance = { values: [], cursor: 0 }; instances.set(id, instance); }
      seen.add(id); const parent = current; current = instance; instance.cursor = 0;
      const result = element.type(element.props); current = parent;
      return expand(result, `${id}:result`, seen);
    }
    return { ...element, props: { ...element.props, children: expand(element.props.children, `${keyed}:children`, seen) } };
  }
  env.render = () => {
    const seen = new Set(); rendered = expand(React.createElement(Component, { service: env.service, refresh: () => { env.refreshes++; } }), 'root', seen);
    for (const [id, instance] of instances) if (!seen.has(id)) { for (const value of instance.values) value?.cleanup?.(); instances.delete(id); }
    for (const effect of effects.splice(0)) effect();
    return rendered;
  };
  env.unmount = () => { for (const instance of instances.values()) for (const value of instance.values) value?.cleanup?.(); instances.clear(); };
  env.all = () => nodes(rendered);
  env.find = predicate => env.all().find(predicate);
  env.input = label => env.find(node => node.props?.['aria-label'] === label || node.props?.id === label);
  env.button = label => env.find(node => node.type === 'button' && text(node) === label);
  env.text = () => text(rendered);
  env.intent = load('web/src/components/admin-manual-delivery-intent.ts');
  env.settle = async () => { for (let index = 0; index < 6; index++) { env.render(); await tick(); } return rendered; };
  return env;
}
function nodes(tree) { if (!tree || typeof tree !== 'object') return []; if (Array.isArray(tree)) return tree.flatMap(nodes); return [tree, ...nodes(tree.props?.children)]; }
function text(tree) { if (tree == null || typeof tree === 'boolean') return ''; if (typeof tree !== 'object') return String(tree); if (Array.isArray(tree)) return tree.map(text).join(''); return text(tree.props?.children); }
const event = { preventDefault() {} };
const writes = f => f.calls.filter(call => call.endpoint.endsWith('/delivery') && call.options.method === 'POST');
const deliveryKey = 'goidc:manual-delivery:7:31';
const click = async (f, label) => { const button = f.button(label); assert(button, `Missing button ${label}: ${f.text()}`); assert(!button.props.disabled, `Disabled button ${label}`); const result = button.props.onClick?.(); await result; await f.settle(); };
const change = (f, label, value) => { const input = f.input(label); assert(input, `Missing input ${label}`); input.props.onChange({ target: { value } }); f.render(); };
async function inventoryPreview(f) {
  await f.settle(); const modes = f.all().filter(node => node.type === 'input' && node.props.type === 'radio' && node.props.name === 'delivery-mode-31');
  assert.equal(modes.length, 2); modes[1].props.onChange(); await f.settle();
  f.input('选择库存 11').props.onChange(); change(f, '交付核验说明', '核验通过');
  f.find(node => node.type === 'form').props.onSubmit(event); await f.settle();
}
async function manualPreview(f) {
  await f.settle(); change(f, '实际资源标识', 'machine-31'); change(f, '登录用户名', 'private-user'); change(f, '交付核验说明', '私密核验内容');
  f.find(node => node.type === 'form').props.onSubmit(event); await f.settle();
}
async function submit(f) { await click(f, '确认交付'); }

test('inventory mode requests an exact available/unbound page and preserves selection across pages', async () => {
  const f = fixture(); try {
    await inventoryPreview(f); assert.match(f.text(), /预览版本 3/); assert.match(f.text(), /固定任务 #71/); assert.match(f.text(), /版本 5/);
    await click(f, '返回修改'); await click(f, '下一页库存'); assert(f.calls.some(call => call.endpoint === '/admin/manual-resources?q=&status=available&binding=unbound&limit=10&offset=10'));
    assert.match(f.text(), /已选择 #11/); assert(f.input('选择库存 12'));
    change(f, '搜索可用库存', '  机房  '); await click(f, '搜索库存');
    assert(f.calls.some(call => call.endpoint === '/admin/manual-resources?q=%E6%9C%BA%E6%88%BF&status=available&binding=unbound&limit=10&offset=0'));
  } finally { f.unmount(); }
});
test('inventory confirmation posts only fixed task and both preview versions, then refreshes service and inventory', async () => {
  const f = fixture(); try {
    await inventoryPreview(f); const before = f.calls.filter(call => call.endpoint.startsWith('/admin/manual-resources?')).length;
    await submit(f); const request = writes(f)[0]; assert(request);
    assert.deepEqual(JSON.parse(request.options.body), { job_id: 71, manual_resource_id: 11, resource_expected_version: 5, expected_version: 3, note: '核验通过' });
    assert(request.options.headers['Idempotency-Key'].startsWith('manual-delivery-'));
    assert(f.calls.some(call => call.endpoint === '/admin/manual-resources/11')); assert.equal(f.refreshes, 1); assert(!f.storage.has(deliveryKey));
    assert(f.calls.filter(call => call.endpoint.startsWith('/admin/manual-resources?')).length > before);
  } finally { f.unmount(); }
});
for (const permission of ['resources.view', 'resources.manage']) test(`missing ${permission} keeps legacy mode without reading inventory`, async () => {
  const f = fixture(); try {
    f.actor.permissions = f.actor.permissions.filter(value => value !== permission); await manualPreview(f);
    assert.match(f.text(), /确认人工交付/); assert(!f.calls.some(call => call.endpoint.includes('manual-resources')));
    await submit(f); assert.equal(writes(f).length, 1); assert.equal(JSON.parse(writes(f)[0].options.body).upstream_id, 'machine-31');
  } finally { f.unmount(); }
});
test('non-create tasks retain manual completion and never read inventory', async () => {
  const f = fixture(); try {
    f.service = service({ status: 'active', jobs: [{ id: 71, action: 'suspend', status: 'waiting_manual' }] });
    await manualPreview(f); assert(!f.text().includes('从独立库存交付')); await submit(f);
    assert.deepEqual(JSON.parse(writes(f)[0].options.body), { job_id: 71, upstream_id: 'machine-31', username: 'private-user', note: '私密核验内容' });
    assert(!f.calls.some(call => call.endpoint.includes('manual-resources')));
  } finally { f.unmount(); }
});
test('supplier mode, missing manage permission, and absent waiting task have no new delivery form', async () => {
  for (const variation of ['supplier', 'permission', 'job']) {
    const f = fixture(); try {
      if (variation === 'supplier') f.service.provision_mode = 'supplier';
      if (variation === 'permission') f.actor.permissions = [];
      if (variation === 'job') f.service.jobs = [];
      await f.settle(); assert(!f.find(node => node.type === 'form')); assert.equal(f.calls.length, 0);
    } finally { f.unmount(); }
  }
});
for (const changeServer of ['service-version', 'task', 'resource-version', 'resource-bound', 'resource-disabled']) test(`preflight ${changeServer} change never posts or rewrites the preview`, async () => {
  const f = fixture((endpoint, options, env) => {
    if (endpoint === '/admin/services/31') return service(changeServer === 'service-version' ? { version: 4 } : changeServer === 'task' ? { jobs: [{ id: 72, action: 'create', status: 'waiting_manual' }] } : {});
    if (endpoint === '/admin/manual-resources/11') return resource(changeServer === 'resource-version' ? { version: 6 } : changeServer === 'resource-bound' ? { binding_id: 9, service_id: 32 } : changeServer === 'resource-disabled' ? { status: 'disabled' } : {});
  }); try {
    await inventoryPreview(f); await submit(f); assert.equal(writes(f).length, 0); assert.match(f.text(), /尚未发送|重新读取并确认/); assert(!f.storage.has(deliveryKey));
  } finally { f.unmount(); }
});
test('changing visible task/version after preview blocks stale confirmation', async () => {
  const f = fixture(); try { await inventoryPreview(f); f.service = service({ version: 4 }); await f.settle(); await submit(f); assert.equal(writes(f).length, 0); assert.match(f.text(), /原预览不能提交/); } finally { f.unmount(); }
});
test('a lost response freezes the exact inventory intent and retries the original task after service changes', async () => {
  let attempt = 0;
  const f = fixture((endpoint, options) => { if (endpoint.endsWith('/delivery')) return attempt++ === 0 ? Promise.reject(new TypeError('connection lost')) : { service_id: 31, job_id: 71, action: 'create', status: 'succeeded', replayed: true }; }); try {
    await inventoryPreview(f); await submit(f); assert.match(f.text(), /原交付结果待核验/); assert(!f.find(node => node.type === 'form'));
    const original = JSON.parse(f.storage.get(deliveryKey)); assert.equal(JSON.parse(original.body).expected_version, 3);
    f.service = service({ status: 'active', version: 4, jobs: [{ id: 71, action: 'create', status: 'succeeded' }, { id: 72, action: 'sync', status: 'waiting_manual' }] });
    await f.settle(); await click(f, '使用原交付请求核验');
    assert.equal(writes(f).length, 2); assert.equal(writes(f)[0].options.body, writes(f)[1].options.body); assert.equal(writes(f)[0].options.headers['Idempotency-Key'], writes(f)[1].options.headers['Idempotency-Key']); assert.equal(f.refreshes, 1);
  } finally { f.unmount(); }
});
test('same administrator permission remount recovers inventory intent and another administrator remains isolated', async () => {
  const f = fixture(endpoint => endpoint.endsWith('/delivery') ? Promise.reject(new TypeError('lost')) : undefined); try {
    await inventoryPreview(f); await submit(f); const original = f.storage.get(deliveryKey);
    f.actor = { ...f.actor, permissions: [...f.actor.permissions, 'orders.view'] }; await f.settle(); assert.match(f.text(), /原交付结果待核验/); assert.equal(f.storage.get(deliveryKey), original);
    f.actor = { ...f.actor, id: 8 }; await f.settle(); assert(f.find(node => node.type === 'form')); assert.equal(f.storage.get(deliveryKey), original);
  } finally { f.unmount(); }
});
test('a late write after administrator change cannot refresh or delete either administrator intent', async () => {
  let finish;
  const f = fixture(endpoint => endpoint.endsWith('/delivery') ? new Promise(resolve => { finish = resolve; }) : undefined); try {
    await inventoryPreview(f); f.button('确认交付').props.onClick(); await f.settle(); assert(finish); assert(f.storage.has(deliveryKey));
    f.actor = { ...f.actor, id: 8 }; await f.settle(); f.storage.set('goidc:manual-delivery:8:31', 'new-owner-sentinel');
    finish({ service_id: 31, job_id: 71, action: 'create', status: 'succeeded', replayed: false }); await f.settle();
    assert.equal(f.refreshes, 0); assert(f.storage.has(deliveryKey)); assert.equal(f.storage.get('goidc:manual-delivery:8:31'), 'new-owner-sentinel');
  } finally { f.unmount(); }
});
for (const status of [401, 403, 409]) test(`post-success identity ${status} retains uncertain intent`, async () => {
  let posted = false;
  const f = fixture((endpoint, options) => {
    if (endpoint.endsWith('/delivery')) { posted = true; return { service_id: 31, job_id: 71, action: 'create', status: 'succeeded', replayed: false }; }
    if (endpoint === '/me' && posted) return Promise.reject(new ApiError(status, 'stale_service', 'identity verification failed'));
  }); try {
    await inventoryPreview(f); await submit(f); assert.equal(JSON.parse(f.storage.get(deliveryKey)).phase, 'uncertain'); assert.equal(f.refreshes, 0); assert(!f.button('已核对，结束原请求'));
  } finally { f.unmount(); }
});
test('a business conflict after an unknown attempt never releases the original intent', async () => {
  let attempt = 0;
  const f = fixture(endpoint => endpoint.endsWith('/delivery') ? Promise.reject(attempt++ === 0 ? new TypeError('lost') : new ApiError(409, 'delivery_conflict', 'conflict')) : undefined); try {
    await inventoryPreview(f); await submit(f); await click(f, '使用原交付请求核验');
    assert.equal(JSON.parse(f.storage.get(deliveryKey)).phase, 'uncertain'); await click(f, '读取服务任务与库存现状'); assert(!f.button('已核对，结束原请求')); assert.equal(f.refreshes, 0);
  } finally { f.unmount(); }
});
test('a first explicit conflict requires reading current facts and a separate acknowledged end', async () => {
  const f = fixture(endpoint => endpoint.endsWith('/delivery') ? Promise.reject(new ApiError(409, 'manual_resource_version_conflict', '库存版本冲突')) : undefined); try {
    await inventoryPreview(f); await submit(f); assert.equal(JSON.parse(f.storage.get(deliveryKey)).phase, 'rejected'); assert(!f.button('已核对，结束原请求'));
    f.inventory = resource({ version: 6, binding_id: 9, service_id: 32 }); await click(f, '读取服务任务与库存现状'); assert.match(f.text(), /占用服务 #32/); assert.match(f.text(), /原请求未作修改/);
    assert.equal(JSON.parse(JSON.parse(f.storage.get(deliveryKey)).body).resource_expected_version, 5);
    await click(f, '已核对，结束原请求'); assert.equal(f.prompts.length, 1); assert.equal(writes(f).length, 1); assert(!f.storage.has(deliveryKey)); assert.equal(f.refreshes, 1);
  } finally { f.unmount(); }
});
for (const result of [{}, { service_id: 32, job_id: 71, action: 'create', status: 'succeeded', replayed: false }, { service_id: 31, job_id: 71, action: 'create', status: 'succeeded' }]) test(`malformed success receipt retains intent: ${JSON.stringify(result)}`, async () => {
  const f = fixture(endpoint => endpoint.endsWith('/delivery') ? result : undefined); try { await inventoryPreview(f); await submit(f); assert(f.storage.has(deliveryKey)); assert.equal(f.refreshes, 0); assert.match(f.text(), /回执缺少原服务或任务确认/); } finally { f.unmount(); }
});
test('corrupt recovery or unavailable storage blocks dispatch', async () => {
  const corrupt = fixture(); try { corrupt.storage.set(deliveryKey, '{broken'); await corrupt.settle(); assert(!corrupt.find(node => node.type === 'fieldset' && !node.props.disabled)); assert.equal(writes(corrupt).length, 0); } finally { corrupt.unmount(); }
  const unavailable = fixture(); try { await inventoryPreview(unavailable); unavailable.failStorage = true; await submit(unavailable); assert.equal(writes(unavailable).length, 0); assert.match(unavailable.text(), /storage failed/); } finally { unavailable.unmount(); }
});
test('legacy completion keeps account and note only in memory; reload cannot issue a new delivery', async () => {
  const f = fixture(endpoint => endpoint.endsWith('/delivery') ? Promise.reject(new TypeError('lost')) : undefined); try {
    await manualPreview(f); await submit(f); const raw = f.storage.get(deliveryKey), marker = JSON.parse(raw); assert.equal(marker.body, null); assert(!raw.includes('private-user')); assert(!raw.includes('私密核验内容'));
    f.unmount(); await f.settle(); assert.match(f.text(), /刷新后不恢复私密内容/); assert.equal(f.button('使用原交付请求核验').props.disabled, true); assert(!f.find(node => node.type === 'form'));
    await click(f, '读取服务任务与库存现状'); assert(!f.button('已核对，结束原请求'));
    f.service = service({ status: 'active', jobs: [{ id: 71, action: 'create', status: 'succeeded' }] }); await click(f, '读取服务任务与库存现状'); assert(f.button('已核对，结束原请求'));
  } finally { f.unmount(); }
});
for (const overrides of [{ invoice_status: 'unpaid' }, { invoice_status: 'refunded' }, { external_refund_order_hold: {} }, { status: 'terminated' }, { status: 'pending_payment' }, { status: 'cancelled' }]) test(`funding/refund/terminal fence prevents delivery: ${JSON.stringify(overrides)}`, async () => {
  const f = fixture(); try { f.service = service(overrides); await f.settle(); assert.equal(f.find(node => node.type === 'fieldset').props.disabled, true); f.find(node => node.type === 'form').props.onSubmit(event); await f.settle(); assert(!f.button('确认交付')); assert.equal(writes(f).length, 0); } finally { f.unmount(); }
});
test('an inventory without credentials can still be selected; malformed page is blocked', async () => {
  const f = fixture(); try { f.inventory.has_credentials = false; await inventoryPreview(f); assert.match(f.text(), /确认人工交付/); await submit(f); assert.equal(writes(f).length, 1); } finally { f.unmount(); }
  const bad = fixture(endpoint => endpoint.startsWith('/admin/manual-resources?') ? { items: {}, total: 1 } : undefined); try {
    await bad.settle(); bad.all().filter(node => node.type === 'input' && node.props.type === 'radio')[1].props.onChange(); await bad.settle(); assert.match(bad.text(), /库存响应无效/); assert(!bad.input('选择库存 11'));
  } finally { bad.unmount(); }
});
test('manual credentials entry requires a positive binding marker and uses existing authorized POST', async () => {
  for (const marker of [undefined, 0, '11', 11]) {
    const f = fixture(undefined, 'credentials'); try {
      f.service = service({ status: 'active', resource_info: { manual_resource_id: marker } }); await f.settle(); const button = f.input('核验库存绑定后查看密码');
      if (marker !== 11) { assert(!button); continue; }
      assert(button); await button.props.onClick(); await f.settle(); assert.equal(f.input('系统密码').props.value, 'secret-password');
      const credentialCalls = f.calls.filter(call => call.endpoint.endsWith('/credentials')); assert.equal(credentialCalls.length, 1); assert.equal(credentialCalls[0].options.method, 'POST'); assert.equal(credentialCalls[0].options.body, '{}'); assert.equal(f.calls.filter(call => call.endpoint === '/me').length, 2);
    } finally { f.unmount(); }
  }
});
test('late credentials response after actor change never reveals the previous password', async () => {
  let finish;
  const f = fixture(endpoint => endpoint.endsWith('/credentials') ? new Promise(resolve => { finish = resolve; }) : undefined, 'credentials'); try {
    f.service = service({ status: 'active', resource_info: { manual_resource_id: 11 } }); await f.settle(); f.input('核验库存绑定后查看密码').props.onClick(); await f.settle(); assert(finish);
    f.actor = { ...f.actor, id: 8 }; await f.settle(); finish({ password: 'old-actor-password' }); await f.settle(); assert.equal(f.input('系统密码').props.value, '************');
  } finally { f.unmount(); }
});
