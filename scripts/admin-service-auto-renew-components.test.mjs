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
const service = overrides => ({ id: 31, user_id: 41, owner_generation: 0, name: '人工服务', version: 3, provision_mode: 'manual', billing_cycle: 'monthly', status: 'active', auto_renew: false, auto_renew_error: '', ...overrides });
const resource = overrides => ({ id: 11, name: '独立库存', address: '192.0.2.11', provider: '测试提供方', region: 'CN', upstream_id: 'resource-11', version: 5, status: 'available', service_id: null, binding_id: null, has_credentials: true, ...overrides });
// A small keyed renderer executes the actual function components and hooks. Each
// component gets its own hooks, and key changes run effect cleanup before late
// transports settle. It does not replace any create/context business logic.
function fixture(respond) {
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
    if (endpoint === '/admin/services/31/auto-renew') { const body = JSON.parse(options.body); env.service = { ...env.service, auto_renew: body.enabled }; return { service_id: 31, auto_renew: body.enabled }; }
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
  const Component = load('web/src/components/admin-service-auto-renew.tsx').AdminServiceAutoRenew;
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
  env.settle = async () => { for (let index = 0; index < 6; index++) { env.render(); await tick(); } return rendered; };
  return env;
}
function nodes(tree) { if (!tree || typeof tree !== 'object') return []; if (Array.isArray(tree)) return tree.flatMap(nodes); return [tree, ...nodes(tree.props?.children)]; }
function text(tree) { if (tree == null || typeof tree === 'boolean') return ''; if (typeof tree !== 'object') return String(tree); if (Array.isArray(tree)) return tree.map(text).join(''); return text(tree.props?.children); }
const markerKey = 'goidc:admin-auto-renew:7:31';
const writes = f => f.calls.filter(call => call.endpoint.endsWith('/auto-renew') && call.options.method === 'PATCH');
const click = async (f, label) => { const button = f.button(label); assert(button, `Missing ${label}: ${f.text()}`); assert(!button.props.disabled, `Disabled ${label}`); await button.props.onClick?.(); await f.settle(); };
async function preview(f, enabled = true) { await f.settle(); await click(f, enabled ? '开启自动续费' : '关闭自动续费'); }
async function submit(f, enabled = true) { await click(f, enabled ? '确认开启自动续费' : '确认关闭自动续费'); }

for (const enabled of [true, false]) test(`explicit ${enabled} confirmation fixes the owner guard without a fictional key or service version CAS and rereads the service`, async () => {
  const f = fixture(); try {
    f.service.auto_renew = !enabled; await preview(f, enabled); assert.match(f.text(), /不立即扣款/); assert.equal(writes(f).length, 0);
    await submit(f, enabled); assert.equal(writes(f).length, 1); assert.deepEqual(JSON.parse(writes(f)[0].options.body), { enabled, expected_customer_id: 41, expected_owner_generation: 0 }); assert.equal(writes(f)[0].options.headers, undefined);
    assert.equal(f.calls.filter(call => call.endpoint === '/admin/services/31').length, 2); assert(f.calls.filter(call => call.endpoint === '/me').length >= 3);
    assert.equal(f.refreshes, 1); assert(!f.storage.has(markerKey)); assert.match(f.text(), /已重新读取并确认/);
  } finally { f.unmount(); }
});
test('services.view without services.manage has no write entry or background requests', async () => {
  const f = fixture(); try { f.actor.permissions = ['services.view']; await f.settle(); assert.equal(f.text(), ''); assert.equal(f.calls.length, 0); } finally { f.unmount(); }
});
test('cancel drops only an unsubmitted confirmation', async () => {
  const f = fixture(); try { await preview(f); await click(f, '取消修改'); assert(f.button('开启自动续费')); assert.equal(writes(f).length, 0); assert(!f.storage.has(markerKey)); } finally { f.unmount(); }
});
test('current target already matches the desired state avoids an unnecessary patch', async () => {
  const f = fixture(endpoint => endpoint === '/admin/services/31' ? service({ auto_renew: true }) : undefined); try { await preview(f); await submit(f); assert.equal(writes(f).length, 0); assert.match(f.text(), /本次没有发送修改/); assert.equal(f.refreshes, 1); } finally { f.unmount(); }
});
for (const overrides of [{ user_id: 42 }, { owner_generation: 1 }, { user_id: 41, owner_generation: 2 }]) test(`preflight changed owner stops old write: ${JSON.stringify(overrides)}`, async () => {
  const f = fixture(endpoint => endpoint === '/admin/services/31' ? service(overrides) : undefined); try { await preview(f); await submit(f); assert.equal(writes(f).length, 0); assert.match(f.text(), /旧操作已停止/); assert(!f.storage.has(markerKey)); } finally { f.unmount(); }
});
test('a new service version with the same ownership keeps a valid confirmed intent', async () => {
  const f = fixture((endpoint, options, env) => { if (endpoint === '/admin/services/31') return { ...env.service, version: 12 }; }); try { await preview(f); f.service = { ...f.service, version: 9 }; await f.settle(); assert(f.button('确认开启自动续费')); await submit(f); assert.equal(writes(f).length, 1); assert(!f.storage.has(markerKey)); } finally { f.unmount(); }
});
for (const change of ['actor', 'permission', 'scope']) test(`actual /me ${change} change before dispatch stops the write`, async () => {
  const f = fixture(); try {
    await preview(f); f.serverActor = { ...f.actor, ...(change === 'actor' ? { id: 8 } : change === 'permission' ? { permissions: ['services.view'] } : { customer_scope: 'owned' }) };
    await submit(f); assert.equal(writes(f).length, 0); assert.match(f.text(), /身份、权限或客户范围已变化/);
  } finally { f.unmount(); }
});
test('an actual authority change between service preflight and PATCH is checked again', async () => {
  let checks = 0;
  const f = fixture((endpoint, options, env) => endpoint === '/me' && ++checks === 2 ? { ...env.actor, customer_scope: 'owned' } : undefined); try { await preview(f); await submit(f); assert.equal(writes(f).length, 0); assert.match(f.text(), /身份、权限或客户范围已变化/); } finally { f.unmount(); }
});
test('lost PATCH response never exposes a blind retry; matching target is observed and explicitly ended', async () => {
  const f = fixture((endpoint, options, env) => {
    if (endpoint.endsWith('/auto-renew')) { env.service = { ...env.service, auto_renew: true }; return Promise.reject(new TypeError('connection lost')); }
  }); try {
    await preview(f); await submit(f); assert.equal(writes(f).length, 1); const stored = JSON.parse(f.storage.get(markerKey)); assert.deepEqual(stored.enabled, true); assert.equal(stored.customerId, 41); assert.equal(stored.ownerGeneration, 0);
    assert.match(f.text(), /不会自动重复提交/); assert(!f.button('确认开启自动续费')); assert(!f.text().includes('使用原请求重试'));
    await click(f, '读取当前自动续费状态'); assert.equal(writes(f).length, 1); assert(f.storage.has(markerKey)); assert.match(f.text(), /不证明原请求执行次数/);
    await click(f, '已核对，结束原操作'); assert.equal(writes(f).length, 1); assert(!f.storage.has(markerKey)); assert.equal(f.prompts.length, 1);
  } finally { f.unmount(); }
});
test('a mismatching value after a lost response is not overwritten until a separate fresh confirmation', async () => {
  let failed = false;
  const f = fixture(endpoint => { if (endpoint.endsWith('/auto-renew') && !failed) { failed = true; return Promise.reject(new TypeError('lost before response')); } }); try {
    await preview(f); await submit(f); await click(f, '读取当前自动续费状态'); assert.match(f.text(), /当前值与原目标不同/); assert.equal(writes(f).length, 1);
    await click(f, '已核对，结束原操作'); assert.equal(writes(f).length, 1); await preview(f); await submit(f); assert.equal(writes(f).length, 2);
  } finally { f.unmount(); }
});
test('unknown marker survives unmount and unrelated service version updates', async () => {
  const f = fixture(endpoint => endpoint.endsWith('/auto-renew') ? Promise.reject(new TypeError('lost')) : undefined); try {
    await preview(f); await submit(f); const raw = f.storage.get(markerKey); f.unmount(); f.service = { ...f.service, version: 30 }; await f.settle();
    assert.match(f.text(), /原操作结果待核对/); assert.equal(f.storage.get(markerKey), raw); assert(!f.button('开启自动续费')); await click(f, '读取当前自动续费状态'); assert.equal(writes(f).length, 1);
  } finally { f.unmount(); }
});
test('ownership A to B to A remounts cannot reuse an older generation intent', async () => {
  const f = fixture(endpoint => endpoint.endsWith('/auto-renew') ? Promise.reject(new TypeError('lost')) : undefined); try {
    await preview(f); await submit(f); const raw = f.storage.get(markerKey);
    f.service = { ...f.service, user_id: 42, owner_generation: 1 }; await f.settle(); assert.match(f.text(), /服务归属已变化/); assert(!f.button('开启自动续费')); assert.equal(f.storage.get(markerKey), raw);
    f.service = { ...f.service, user_id: 41, owner_generation: 2 }; await f.settle(); await click(f, '读取当前自动续费状态'); assert.match(f.text(), /原客户或权限范围的意图已停止/); assert.equal(writes(f).length, 1);
    await click(f, '已核对，结束原操作'); assert(!f.storage.has(markerKey)); assert.equal(writes(f).length, 1);
  } finally { f.unmount(); }
});
test('administrator scope change remounts with a stopped marker; revocation hides all writes', async () => {
  const f = fixture(endpoint => endpoint.endsWith('/auto-renew') ? Promise.reject(new TypeError('lost')) : undefined); try {
    await preview(f); await submit(f); const raw = f.storage.get(markerKey); f.actor = { ...f.actor, customer_scope: 'owned' }; await f.settle();
    assert.match(f.text(), /管理员权限范围或服务归属已变化/); assert.equal(f.storage.get(markerKey), raw); await click(f, '读取当前自动续费状态'); assert.match(f.text(), /原客户或权限范围的意图已停止/);
    f.actor = { ...f.actor, permissions: ['services.view'] }; await f.settle(); assert.equal(f.text(), ''); assert.equal(f.storage.get(markerKey), raw); assert.equal(writes(f).length, 1);
  } finally { f.unmount(); }
});
test('another administrator cannot restore or clear the previous administrator marker', async () => {
  const f = fixture(endpoint => endpoint.endsWith('/auto-renew') ? Promise.reject(new TypeError('lost')) : undefined); try {
    await preview(f); await submit(f); const raw = f.storage.get(markerKey); f.actor = { ...f.actor, id: 8 }; await f.settle(); assert(f.button('开启自动续费')); assert.equal(f.storage.get(markerKey), raw);
  } finally { f.unmount(); }
});
for (const phase of ['write', 'read']) test(`late ${phase} response after owner/actor change is ignored`, async () => {
  let finish, holdRead = false;
  const f = fixture((endpoint, options) => {
    if (endpoint.endsWith('/auto-renew')) return phase === 'write' ? new Promise(resolve => { finish = resolve; }) : Promise.reject(new TypeError('lost'));
    if (endpoint === '/admin/services/31' && holdRead) return new Promise(resolve => { finish = resolve; });
  }); try {
    await preview(f); await submit(f); if (phase === 'read') { holdRead = true; f.button('读取当前自动续费状态').props.onClick(); await f.settle(); }
    assert(finish); const raw = f.storage.get(markerKey); f.service = { ...f.service, owner_generation: 1, user_id: 42 }; f.actor = { ...f.actor, id: 8 }; await f.settle();
    finish(phase === 'write' ? { service_id: 31, auto_renew: true } : service({ auto_renew: true })); await f.settle(); assert.equal(f.refreshes, 0); assert.equal(f.storage.get(markerKey), raw); assert(!f.text().includes('当前值与原目标一致'));
  } finally { f.unmount(); }
});
for (const failure of [401, 403, 409]) test(`post-write /me failure ${failure} preserves the unknown marker`, async () => {
  let posted = false;
  const f = fixture((endpoint, options, env) => {
    if (endpoint.endsWith('/auto-renew')) { posted = true; env.service = { ...env.service, auto_renew: true }; return { service_id: 31, auto_renew: true }; }
    if (endpoint === '/me' && posted) return Promise.reject(new ApiError(failure, 'authority_changed', 'read identity failed'));
  }); try { await preview(f); await submit(f); assert(f.storage.has(markerKey)); assert.equal(f.refreshes, 0); assert.match(f.text(), /原操作结果待核对/); assert(!f.button('已核对，结束原操作')); } finally { f.unmount(); }
});
test('post-write owner change retains the old intent and does not report success', async () => {
  const f = fixture((endpoint, options, env) => { if (endpoint.endsWith('/auto-renew')) { env.service = { ...env.service, user_id: 42, owner_generation: 1, auto_renew: true }; return { service_id: 31, auto_renew: true }; } }); try { await preview(f); await submit(f); assert(f.storage.has(markerKey)); assert.equal(f.refreshes, 0); assert.match(f.text(), /服务归属已变化/); } finally { f.unmount(); }
});
test('an ownership conflict after preflight retains the exact owner intent and requires actual state review', async () => {
  const f = fixture((endpoint, options, env) => {
    if (endpoint.endsWith('/auto-renew')) { env.service = { ...env.service, user_id: 42, owner_generation: 1 }; return Promise.reject(new ApiError(409, 'service_owner_changed', '服务客户已变更')); }
  }); try {
    await preview(f); await submit(f); assert.deepEqual(JSON.parse(writes(f)[0].options.body), { enabled: true, expected_customer_id: 41, expected_owner_generation: 0 });
    assert.equal(JSON.parse(f.storage.get(markerKey)).customerId, 41); assert.equal(JSON.parse(f.storage.get(markerKey)).ownerGeneration, 0); assert.match(f.text(), /原操作结果待核对/);
    assert(!f.button('开启自动续费')); await click(f, '读取当前自动续费状态'); assert.match(f.text(), /原客户或权限范围的意图已停止/); assert.equal(writes(f).length, 1);
    await click(f, '已核对，结束原操作'); assert.equal(writes(f).length, 1); assert(!f.storage.has(markerKey));
  } finally { f.unmount(); }
});
for (const result of [{}, { service_id: 32, auto_renew: true }, { service_id: 31, auto_renew: 'true' }]) test(`malformed PATCH result remains unknown: ${JSON.stringify(result)}`, async () => {
  const f = fixture(endpoint => endpoint.endsWith('/auto-renew') ? result : undefined); try { await preview(f); await submit(f); assert(f.storage.has(markerKey)); assert.equal(f.refreshes, 0); assert.match(f.text(), /未确认原服务及目标状态/); } finally { f.unmount(); }
});
test('inspecting a forbidden/missing service or changed actual authority never ends recovery', async () => {
  let mode = 'write';
  const f = fixture((endpoint, options, env) => {
    if (endpoint.endsWith('/auto-renew')) return Promise.reject(new TypeError('lost'));
    if (endpoint === '/admin/services/31' && mode === 'forbidden') return Promise.reject(new ApiError(404, 'not_found', 'not found'));
    if (endpoint === '/me' && mode === 'scope') return { ...env.actor, customer_scope: 'owned' };
  }); try {
    await preview(f); await submit(f); for (const changed of ['forbidden', 'scope']) { mode = changed; await click(f, '读取当前自动续费状态'); assert(!f.button('已核对，结束原操作')); assert(f.storage.has(markerKey)); } assert.equal(writes(f).length, 1);
  } finally { f.unmount(); }
});
test('ending a reviewed marker rereads facts and refuses changed state', async () => {
  const f = fixture(endpoint => endpoint.endsWith('/auto-renew') ? Promise.reject(new TypeError('lost')) : undefined); try {
    await preview(f); await submit(f); await click(f, '读取当前自动续费状态'); f.service = { ...f.service, auto_renew: true }; await f.settle(); await click(f, '已核对，结束原操作');
    assert(f.storage.has(markerKey)); assert.match(f.text(), /请重新读取现状/); assert.equal(writes(f).length, 1);
  } finally { f.unmount(); }
});
test('storage failure before dispatch prevents all writes; corrupt or nonboolean markers block new intents', async () => {
  const unavailable = fixture(); try { await preview(unavailable); unavailable.failStorage = true; await submit(unavailable); assert.equal(writes(unavailable).length, 0); assert.match(unavailable.text(), /storage failed/); } finally { unavailable.unmount(); }
  for (const raw of ['{broken', JSON.stringify({ schema: 1, serviceId: 31, customerId: 41, ownerGeneration: 0, actor: 'x', enabled: 'true' }), JSON.stringify({ schema: 1, serviceId: 31, customerId: 41, ownerGeneration: 0, actor: 'x', enabled: true, password: 'unexpected' })]) {
    const f = fixture(); try { f.storage.set(markerKey, raw); await f.settle(); assert.match(f.text(), /记录损坏/); assert.equal(f.button('开启自动续费').props.disabled, true); assert.equal(writes(f).length, 0); } finally { f.unmount(); }
  }
});
for (const overrides of [{ billing_cycle: 'once' }, { status: 'terminated' }, { status: 'pending_payment' }]) test(`nonrenewable service blocks confirmation: ${JSON.stringify(overrides)}`, async () => {
  const f = fixture(); try { f.service = service(overrides); await f.settle(); assert.equal(f.button('开启自动续费').props.disabled, true); assert.equal(writes(f).length, 0); } finally { f.unmount(); }
});
for (const overrides of [{ owner_generation: undefined }, { owner_generation: -1 }, { user_id: 0 }, { auto_renew: 'false' }]) test(`invalid service projection does not offer a write: ${JSON.stringify(overrides)}`, async () => {
  const f = fixture(); try { f.service = service(overrides); await f.settle(); assert.match(f.text(), /数据不完整/); assert(!f.button('开启自动续费')); assert.equal(f.calls.length, 0); } finally { f.unmount(); }
});
