import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createRequire, Module } from 'node:module';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const web = fileURLToPath(new URL('../web/', import.meta.url));
const require = createRequire(path.join(web, 'package.json'));
const ts = require('typescript'), React = require('react');
let current, calls, storage, respond, failStorage, reads, resources, visiblePermissions, actor, sessionActor, identityRespond, identityCalls;
const changed = (a, b) => !a || a.length !== b.length || a.some((value, index) => value !== b[index]);
const hooks = {
  useState(initial) { const state = current, index = state.cursor++; if (!(index in state.values)) state.values[index] = typeof initial === 'function' ? initial() : initial; return [state.values[index], next => { state.values[index] = typeof next === 'function' ? next(state.values[index]) : next; }]; },
  useRef(initial) { const state = current, index = state.cursor++; if (!(index in state.values)) state.values[index] = { current: initial }; return state.values[index]; },
  useEffect(effect, deps) { const state = current, index = state.cursor++, previous = state.values[index]; if (changed(previous?.deps, deps)) state.effects.push(() => { previous?.cleanup?.(); state.values[index] = { deps, cleanup: effect() }; }); },
};
class ApiError extends Error { constructor(status, code) { super(code); this.status = status; this.code = code; } }
const filename = path.join(web, 'src/components/admin-customer-referrer-mutation.ts');
const module = new Module(filename);
module.require = name => name === 'react' ? hooks : name === '@/lib/api' ? { ApiError, api: async (url, options = {}) => {
  const call = { url, ...options };
  if (url === '/me') { identityCalls.push(call); return identityRespond ? identityRespond(call) : sessionActor; }
  calls.push(call); return respond(call);
} } : name === '@/lib/admin-navigation' ? { can: (user, permission) => !!user && (user.role === 'super_admin' || user.role === 'admin' && user.permissions?.includes(permission)) } : require(name);
module._compile(ts.transpileModule(readFileSync(filename, 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText, filename);
const { useCustomerReferrerMutation } = module.exports;
const state = () => ({ cursor: 0, values: [], effects: [] });
function within(scope, callback) { scope.cursor = 0; const previous = current; current = scope; const result = callback(); current = previous; scope.effects.splice(0).forEach(effect => effect()); return result; }
function mount() {
  const scope = state();
  return { actorID: 7, customerID: 41, allowed: true, render() { return within(scope, () => useCustomerReferrerMutation(this.actorID, this.customerID, this.allowed)); }, unmount() { scope.values.forEach(value => value?.cleanup?.()); } };
}
const settle = () => new Promise(resolve => setTimeout(resolve, 5));
async function ready() { const view = mount(); view.render(); await settle(); assert.equal(view.render().ready, true); return view; }
const input = { referrer_customer_id: 51, expected_version: 3, reason: '人工核对推荐关系' };
const relation = (overrides = {}) => ({ customer_id: 41, referral_version: 3, attributed: true, referrer_visible: true, referrer_customer_id: 61, referrer_name: '敏感推荐人姓名', effective_scope: 'future_invoices_only', ...overrides });
function success(body = input, overrides = {}) { return relation({ referral_version: body.expected_version + 1, attributed: body.referrer_customer_id !== null, referrer_customer_id: body.referrer_customer_id, referrer_name: body.referrer_customer_id === null ? null : '新推荐人', changed: true, replayed: false, ...overrides }); }
function reset() {
  calls = []; storage = new Map(); failStorage = false; reads = []; resources = new Map(); visiblePermissions = new Set(['customers.manage', 'affiliates.manage']); identityCalls = []; identityRespond = null;
  actor = { id: 7, role: 'admin', permissions: ['customers.manage', 'affiliates.manage'], customer_scope: 'all', customer_ownerless: true, sales_enabled: false };
  sessionActor = { ...actor };
  globalThis.window = { setTimeout, clearTimeout };
  globalThis.sessionStorage = { getItem: key => storage.get(key) ?? null, setItem(key, value) { if (failStorage) throw new Error('storage unavailable'); storage.set(key, value); }, removeItem: key => storage.delete(key) };
  respond = async call => call.method === 'PUT' ? success(JSON.parse(call.body)) : relation({ referral_version: 5 });
}

test('referrer lost response survives remount and authority failures with exact original PUT/body/key', async () => {
  reset(); let view = await ready(); respond = async () => { throw new TypeError('lost'); };
  await view.render().run(input); const original = calls[0]; view.unmount();
  for (const failure of [new ApiError(401, 'unauthorized'), new ApiError(403, 'permission_denied'), new ApiError(404, 'not_found'), new ApiError(409, 'idempotency_conflict'), new ApiError(503, 'unavailable')]) {
    view = await ready(); assert.deepEqual(view.render().pending, input); respond = async () => { throw failure; };
    await view.render().run({ ...input, reason: 'must not replace' }); assert.equal(view.render().uncertain, true); view.unmount();
  }
  view = await ready(); respond = async () => success(input, { replayed: true, referrer_visible: false, referrer_customer_id: null, referrer_name: null });
  assert.ok(await view.render().run()); assert.equal(storage.size, 0);
  for (const call of calls) { assert.equal(call.url, '/admin/customers/41/referrer'); assert.equal(call.method, 'PUT'); assert.equal(call.body, original.body); assert.equal(call.headers['Idempotency-Key'], original.headers['Idempotency-Key']); }
  view.unmount();
});
test('definitive 409 remains rejected across remount and cannot change version before explicit reviewed restart', async () => {
  reset(); let view = await ready(); respond = async () => { throw new ApiError(409, 'version_conflict'); };
  await view.render().run(input); const original = calls[0]; assert.equal(view.render().rejected, true); view.unmount();
  view = await ready(); await view.render().run({ ...input, expected_version: 5 }); assert.equal(calls.length, 1);
  assert.equal(await view.render().reopenReviewed(), null);
  respond = async () => relation({ referral_version: 5 }); assert.equal((await view.render().reviewCurrent()).referral_version, 5);
  await view.render().run({ ...input, expected_version: 5 }); assert.equal(calls.length, 2); assert.equal(calls[1].method, undefined);
  assert.equal((await view.render().reopenReviewed()).referral_version, 5); assert.equal(storage.size, 0);
  respond = async call => success(JSON.parse(call.body)); assert.ok(await view.render().run({ ...input, expected_version: 5 }));
  assert.equal(JSON.parse(calls[2].body).expected_version, 5); assert.notEqual(calls[2].headers['Idempotency-Key'], original.headers['Idempotency-Key']); view.unmount();
});
test('permission loss blocks all mutation/review requests without releasing pending intent', async () => {
  reset(); const view = await ready(); respond = async () => { throw new TypeError('lost'); }; await view.render().run(input); const original = calls[0];
  view.allowed = false; view.render(); await settle(); await view.render().run(); await view.render().reviewCurrent(); assert.equal(calls.length, 1); assert.equal(storage.size, 1);
  view.allowed = true; view.render(); await settle(); respond = async () => success(input, { replayed: true }); assert.ok(await view.render().run()); assert.equal(calls[1].body, original.body); assert.equal(calls[1].headers['Idempotency-Key'], original.headers['Idempotency-Key']); view.unmount();
});
test('no-op and explicit clearing accept only the matching receipt version and attribution', async () => {
  reset(); let view = await ready(); respond = async () => success(input, { changed: false, referral_version: 3 }); assert.ok(await view.render().run(input)); view.unmount();
  reset(); view = await ready(); const clear = { ...input, referrer_customer_id: null }; respond = async () => success(clear); assert.ok(await view.render().run(clear)); assert.equal(JSON.parse(calls[0].body).referrer_customer_id, null); view.unmount();
  for (const value of [{}, success(input, { customer_id: 99 }), success(input, { referral_version: 6 }), success(input, { referrer_customer_id: 52 }), success(input, { effective_scope: 'all_invoices' })]) {
    reset(); view = await ready(); respond = async () => value; assert.equal(await view.render().run(input), null); assert.equal(view.render().uncertain, true); assert.equal(storage.size, 1); view.unmount();
  }
});
test('corrupt recovery and unavailable storage fail closed before any network write', async () => {
  for (const value of ['false', JSON.stringify({ key: 'saved-valid-key', body: JSON.stringify({ ...input, password: 'forbidden' }), phase: 'uncertain', error: '' })]) {
    reset(); storage.set('goidc:referrer-intent:admin:7:41', value); const view = mount(); view.render(); await settle(); assert.equal(view.render().ready, false); await view.render().run(input); assert.equal(calls.length, 0); view.unmount();
  }
  reset(); const view = await ready(); failStorage = true; await view.render().run(input); assert.equal(calls.length, 0); assert.equal(view.render().ready, false); view.unmount();
});
test('duplicate clicks send once and late completion cannot confirm another actor or customer', async () => {
  for (const switchTo of ['actor', 'customer']) {
    reset(); const view = await ready(); let finish; respond = () => new Promise(resolve => { finish = resolve; });
    const action = view.render(), first = action.run(input); await action.run(input); await settle(); assert.equal(calls.length, 1);
    if (switchTo === 'actor') view.actorID = 8; else view.customerID = 42;
    view.render(); await settle(); finish(success(input)); assert.equal(await first, null); assert.equal(view.render().pending, null); view.unmount();
  }
});
test('unchanged Provider with a different cookie principal cannot send, acknowledge or release the original intent', async () => {
  reset(); const view = await ready();
  sessionActor = { ...actor, id: 8 }; assert.equal(await view.render().run(input), null); assert.equal(calls.length, 0); assert.equal(storage.size, 0);
  sessionActor = { ...actor }; respond = async () => { throw new TypeError('lost'); }; await view.render().run(input); const original = calls[0]; const saved = [...storage.values()][0];
  sessionActor = { ...actor, id: 8 }; await view.render().run(); assert.equal(calls.length, 1); assert.equal(view.render().uncertain, true); assert.equal([...storage.values()][0].includes(JSON.parse(saved).key), true);
  sessionActor = { ...actor }; respond = async () => { sessionActor = { ...actor, id: 8 }; return success(input); };
  assert.equal(await view.render().run(), null); assert.equal(storage.size, 1); assert.equal(view.render().uncertain, true);
  sessionActor = { ...actor }; respond = async () => success(input, { replayed: true }); assert.ok(await view.render().run());
  for (const call of calls) { assert.equal(call.body, original.body); assert.equal(call.headers['Idempotency-Key'], original.headers['Idempotency-Key']); }
  assert.ok(identityCalls.every(call => call.portal === 'admin')); view.unmount();
});
test('cookie permission loss or identity-query refusals never become definitive business rejection', async () => {
  reset(); const view = await ready(); respond = async () => { throw new TypeError('lost'); }; await view.render().run(input);
  for (const identity of [{ ...actor, permissions: ['customers.manage'] }, { ...actor, role: 'customer' }]) {
    sessionActor = identity; await view.render().run(); assert.equal(calls.length, 1); assert.equal(view.render().uncertain, true); assert.equal(view.render().rejected, false);
  }
  sessionActor = { ...actor };
  for (const refusal of [new ApiError(400, 'identity_invalid'), new ApiError(409, 'identity_conflict')]) {
    identityRespond = async () => { throw refusal; }; await view.render().run(); assert.equal(calls.length, 1); assert.equal(view.render().uncertain, true); assert.equal(view.render().rejected, false);
  }
  identityRespond = null; respond = async () => success(input, { replayed: true }); assert.ok(await view.render().run()); view.unmount();
});
test('review and explicit restart recheck the cookie principal before reading or releasing a rejected intent', async () => {
  reset(); const view = await ready(); respond = async () => { throw new ApiError(409, 'version_conflict'); }; await view.render().run(input);
  sessionActor = { ...actor, id: 8 }; assert.equal(await view.render().reviewCurrent(), null); assert.equal(calls.length, 1); assert.equal(storage.size, 1);
  sessionActor = { ...actor }; respond = async () => relation({ referral_version: 5 }); assert.ok(await view.render().reviewCurrent());
  sessionActor = { ...actor, id: 8 }; assert.equal(await view.render().reopenReviewed(), null); assert.equal(storage.size, 1); assert.equal(view.render().rejected, true);
  sessionActor = { ...actor }; assert.ok(await view.render().reviewCurrent()); assert.ok(await view.render().reopenReviewed()); assert.equal(storage.size, 0); view.unmount();
});

const ui = new Proxy({}, { get: (_, name) => props => {
  if (name === 'Dialog' && !props.open) return null;
  const tag = ({ Button: 'button', Input: 'input', Textarea: 'textarea', Checkbox: 'checkbox', Table: 'table', TableRow: 'tr', TableCell: 'td', TableHead: 'th' })[name] || String(name).toLowerCase();
  return React.createElement(tag, props, props.children);
} });
const componentFilename = path.join(web, 'src/components/admin-customer-referrer.tsx');
const componentModule = new Module(componentFilename);
componentModule.require = name => {
  if (name === 'react') return hooks;
  if (name === './workspace') return { useAdminIdentity: () => actor, useAdminPermission: permission => visiblePermissions.has(permission) };
  if (name === '@/lib/api') return { date: value => value };
  if (name === '@/hooks/use-resource') return { useResource: url => {
    reads.push(url); const found = resources.get(url);
    return { data: found?.data ?? null, error: found?.error ?? null, isLoading: false, refresh() { reads.push(`refresh:${url}`); } };
  } };
  if (name === './admin-customer-referrer-mutation') return module.exports;
  if (name.startsWith('./ui/')) return ui;
  if (name === 'lucide-react') return new Proxy({}, { get: () => props => React.createElement('i', props) });
  return require(name);
};
componentModule._compile(ts.transpileModule(readFileSync(componentFilename, 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true } }).outputText, componentFilename);
const { CustomerReferrerPanel } = componentModule.exports;
class Renderer {
  instances = new Map(); nodes = [];
  constructor(onChanged = () => {}) { this.onChanged = onChanged; }
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
    this.tree = walk(React.createElement(CustomerReferrerPanel, { customerId: 41, onChanged: this.onChanged }));
    for (const [key, value] of this.instances) if (!visited.has(key)) { value.values.forEach(item => item?.cleanup?.()); this.instances.delete(key); }
    return this;
  }
  text(node = this.tree) { return Array.isArray(node) ? node.map(value => this.text(value)).join('') : typeof node === 'string' ? node : node ? this.text(node.children) : ''; }
  button(text) { const found = this.nodes.find(node => node.type === 'button' && this.text(node) === text); assert.ok(found, `button ${text} exists`); return found.props; }
  byLabel(label) { const found = this.nodes.find(node => node.props?.['aria-label'] === label); assert.ok(found, `control ${label} exists`); return found.props; }
  unmount() { for (const value of this.instances.values()) value.values.forEach(item => item?.cleanup?.()); this.instances.clear(); }
}
function resourceFixtures() {
  resources.set('/admin/customers/41/referrer', { data: relation({ referrer_visible: false, referrer_customer_id: null, referrer_name: null }) });
  resources.set('/admin/customers/41/referrer/history?limit=10&offset=0', { data: { items: [{ id: 1, customer_id: 41, actor_id: 7, reason: '历史调整原因', version_before: 1, version_after: 2, created_at: '2026-10-21', old_attributed: true, old_referrer_visible: false, old_referrer_customer_id: null, old_referrer_name: null, new_attributed: false, new_referrer_visible: true, new_referrer_customer_id: null, new_referrer_name: null }], total: 11, limit: 10, offset: 0 } });
  resources.set('/admin/customers/41/referrer/options?q=&limit=10&offset=0', { data: { items: [{ id: 51, username: 'candidate', display_name: '候选客户', email: 'candidate@example.test' }], total: 11, limit: 10, offset: 0 } });
}
test('actual panel requires both permissions, hides out-of-scope identity and independently paginates history/candidates', async () => {
  reset(); resourceFixtures();
  for (const permissions of [[], ['customers.manage'], ['affiliates.manage']]) {
    visiblePermissions = new Set(permissions); const renderer = new Renderer().render(); assert.equal(reads.length, 0); assert.match(renderer.text(), /无权限/); renderer.unmount();
  }
  visiblePermissions = new Set(['customers.manage', 'affiliates.manage']); const renderer = new Renderer().render(); await settle(); renderer.render();
  assert.match(renderer.text(), /身份不在当前客户范围，已隐藏/); assert.doesNotMatch(renderer.text(), /敏感推荐人姓名/); assert.ok(reads.includes('/admin/customers/41/referrer/history?limit=10&offset=0')); assert.equal(reads.some(url => url.includes('/options')), false);
  renderer.button('调整推荐人').onClick(); renderer.render(); assert.ok(reads.includes('/admin/customers/41/referrer/options?q=&limit=10&offset=0'));
  function buttons(node) { if (Array.isArray(node)) return node.flatMap(buttons); if (!node || typeof node !== 'object') return []; return [...(node.type === 'button' ? [node] : []), ...buttons(node.children)]; }
  const candidateNav = renderer.nodes.find(node => node.props?.['aria-label'] === '推荐客户候选分页');
  buttons(candidateNav).find(node => renderer.text(node) === '下一页').props.onClick(); renderer.render(); assert.ok(reads.includes('/admin/customers/41/referrer/options?q=&limit=10&offset=10'));
  renderer.byLabel('搜索推荐客户').onChange({ target: { value: 'A&B' } }); renderer.render(); renderer.button('查询候选').onClick(); renderer.render(); assert.ok(reads.includes('/admin/customers/41/referrer/options?q=A%26B&limit=10&offset=0'));
  const historyNav = renderer.nodes.find(node => node.props?.['aria-label'] === '推荐人调整历史分页');
  buttons(historyNav).find(node => renderer.text(node) === '下一页').props.onClick(); renderer.render(); assert.ok(reads.includes('/admin/customers/41/referrer/history?limit=10&offset=10'));
  visiblePermissions.delete('affiliates.manage'); reads = []; renderer.render(); assert.equal(reads.length, 0); assert.doesNotMatch(renderer.text(), /历史调整原因|调整推荐人 · 提交版本/); renderer.unmount();
});
test('actual confirmation performs only dedicated PUT, keeps explicit null and refreshes relationship/history after confirmation', async () => {
  reset(); resourceFixtures(); let changedCount = 0; const renderer = new Renderer(() => changedCount++).render(); await settle(); renderer.render();
  renderer.button('调整推荐人').onClick(); renderer.render();
  resources.set('/admin/customers/41/referrer', { data: relation({ referral_version: 5 }) }); renderer.render();
  const clear = renderer.nodes.filter(node => node.type === 'input' && node.props.type === 'radio').at(-1);
  clear.props.onChange(); renderer.byLabel('推荐人调整原因').onChange({ target: { value: '明确清除原因' } }); renderer.nodes.find(node => node.type === 'checkbox').props.onCheckedChange(true); renderer.render();
  renderer.nodes.find(node => node.type === 'form').props.onSubmit({ preventDefault() {} }); renderer.render(); assert.equal(calls.length, 0); assert.match(renderer.text(), /确认调整推荐人/);
  await renderer.button('确认调整推荐人').onClick(); renderer.render(); await settle(); renderer.render();
  // onClick starts the async operation; wait for its confirmation before inspecting.
  assert.equal(calls.length, 1); assert.equal(calls[0].url, '/admin/customers/41/referrer'); assert.equal(calls[0].method, 'PUT'); assert.deepEqual(JSON.parse(calls[0].body), { referrer_customer_id: null, expected_version: 3, reason: '明确清除原因' });
  assert.equal(changedCount, 1); assert.ok(reads.includes('refresh:/admin/customers/41/referrer')); assert.ok(reads.includes('refresh:/admin/customers/41/referrer/history?limit=10&offset=0')); renderer.unmount();
});
