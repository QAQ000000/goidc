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
const { useServiceAdjustmentMutation, useServiceAdjustmentManagement, serviceTermMicros } = load(path.join(web, 'src/components/admin-service-adjustment-mutation.ts'));
const { AdminServiceAdjustments, serviceAdjustmentCents } = load(path.join(web, 'src/components/admin-service-adjustments.tsx'));
const root = '/admin/services/31';
const facts = overrides => ({ service_id: 31, customer_id: 41, service_version: 3, owner_generation: 0, status: 'active', billing_cycle: 'monthly', pricing_mode: 'contract', fixed_amount_cents: null, contract_base_cents: 1000, contract_discount_cents: 200, contract_amount_cents: 800, amount_cents: 800, next_due_at: '2026-11-01T00:00:00.000123Z', term_mode: 'follow_supplier', supplier_next_due_at: null, supplier_observed_at: null, ...overrides });
const pricing = (body = { expected_version: 3, mode: 'fixed', fixed_amount_cents: 700 }, baseline = facts(), overrides = {}) => ({ service_id: 31, customer_id: 41, service_version: body.expected_version, mode: body.mode, fixed_amount_cents: body.mode === 'fixed' ? body.fixed_amount_cents : null, contract_base_cents: 1000, contract_discount_cents: 200, contract_amount_cents: 800, amount_cents: body.mode === 'fixed' ? body.fixed_amount_cents : 800, billing_cycle: 'monthly', current_due_at: baseline.next_due_at, expires_at: new Date(Date.now() + 60000).toISOString(), quote_hash: 'a'.repeat(64), ...overrides });
const term = (body, baseline = facts(), overrides = {}) => ({ service_id: 31, customer_id: 41, service_version: body.expected_version, kind: body.kind, mode: baseline.term_mode, current_due_at: baseline.next_due_at, supplier_next_due_at: null, supplier_observed_at: null, next_due_at: body.next_due_at, expires_at: new Date(Date.now() + 60000).toISOString(), quote_hash: 'b'.repeat(64), ...overrides });
const input = (overrides = {}) => { const baseline = facts(), quote = pricing(); return { kind: 'pricing', baseline, quote, body: { expected_version: 3, mode: 'fixed', fixed_amount_cents: 700, quote_hash: quote.quote_hash, reason: 'customer ticket confirms fixed price' }, ...overrides }; };
const termInput = () => { const baseline = facts(), body = { expected_version: 3, kind: 'compensation', next_due_at: '2026-12-01T08:00:00+08:00', reason: 'service incident compensation' }, quote = term(body); return { kind: 'compensation', baseline, quote, body: { ...body, quote_hash: quote.quote_hash } }; };
function receipt(call) {
  const body = JSON.parse(call.body);
  if (call.url.endsWith('/renewal-pricing')) {
    const original = input();
    return { ...pricing(body), version: body.expected_version + (body.mode === management.pricing_mode && (body.mode === 'contract' || body.fixed_amount_cents === management.fixed_amount_cents) ? 0 : 1), replayed: false, quote_hash: body.quote_hash, current_due_at: original.baseline.next_due_at };
  }
  return { service_id: 31, customer_id: 41, service_version: body.expected_version + 1, kind: body.kind, mode: 'managed', before_due_at: facts().next_due_at, next_due_at: body.next_due_at, supplier_next_due_at: null, supplier_observed_at: null, replayed: false };
}
function reset() {
  actor = { id: 7, role: 'admin', permissions: ['services.view', 'services.pricing', 'services.term.correct', 'services.term.compensate'], customer_scope: 'all', customer_ownerless: true }; liveActor = actor;
  management = facts(); calls = []; storage = new Map(); failStorage = false; refreshes = 0;
  respond = async call => call.url === '/me' ? liveActor : call.url.endsWith('/management') ? management : call.url.endsWith('/renewal-pricing/preview') ? pricing(JSON.parse(call.body), management) : call.url.endsWith('/term-adjustment-quotes') ? term(JSON.parse(call.body), management) : receipt(call);
  globalThis.window = { setTimeout: (fn, delay) => { const timer = setTimeout(fn, delay); timer.unref?.(); return timer; }, clearTimeout, addEventListener() {}, removeEventListener() {}, confirm: () => true };
  globalThis.sessionStorage = { getItem: key => storage.get(key) ?? null, setItem(key, value) { if (failStorage) throw new Error('storage failed'); storage.set(key, value); }, removeItem: key => storage.delete(key) };
}
const settle = () => new Promise(resolve => setTimeout(resolve, 5));
const writes = () => calls.filter(call => call.url.endsWith('/renewal-pricing') || call.url.endsWith('/term-adjustments'));
function mountMutation() { const values = state(); return { render: () => within(values, () => useServiceAdjustmentMutation(actor, 31, 41, 0)), unmount: () => values.values.forEach(value => value?.cleanup?.()) }; }
async function readyMutation() { const view = mountMutation(); view.render(); await settle(); assert.equal(view.render().ready, true); return view; }
class Renderer {
  instances = new Map(); nodes = []; props = { service: { id: 31, user_id: 41, owner_generation: 0, version: 3 }, refresh: () => refreshes++ };
  render() {
    this.nodes = []; const visited = new Set();
    const walk = (node, key = 'root') => {
      if (Array.isArray(node)) return node.map((child, index) => walk(child, `${key}/${child?.key ?? index}`));
      if (node === null || node === undefined || typeof node === 'boolean') return null;
      if (typeof node !== 'object') return String(node);
      if (typeof node.type === 'function') {
        const identity = `${key}:${node.type.name}:${node.key ?? ''}`; visited.add(identity); if (!this.instances.has(identity)) this.instances.set(identity, state());
        return walk(within(this.instances.get(identity), () => node.type(node.props)), `${identity}/result`);
      }
      const result = { type: node.type, props: node.props, children: walk(node.props?.children, `${key}/children`) }; this.nodes.push(result); return result;
    };
    this.tree = walk(React.createElement(AdminServiceAdjustments, this.props));
    for (const [key, value] of this.instances) if (!visited.has(key)) { value.values.forEach(item => item?.cleanup?.()); this.instances.delete(key); }
    return this;
  }
  text(node = this.tree) { return Array.isArray(node) ? node.map(value => this.text(value)).join('') : typeof node === 'string' ? node : node ? this.text(node.children) : ''; }
  button(text) { const found = this.nodes.find(node => node.type === 'button' && this.text(node) === text); assert.ok(found, text); return found.props; }
  label(label) { const found = this.nodes.find(node => node.props?.['aria-label'] === label); assert.ok(found, label); return found.props; }
  form(index) { return this.nodes.filter(node => node.type === 'form')[index].props; }
  unmount() { this.instances.forEach(value => value.values.forEach(item => item?.cleanup?.())); this.instances.clear(); }
}
async function readyRenderer() { const view = new Renderer().render(); await settle(); view.render(); return view; }

test('management needs services.view, rechecks live identity and keeps actions independently permissioned', async () => {
  reset(); actor = { ...actor, permissions: ['services.manage'] }; const denied = new Renderer().render(); assert.match(denied.text(), /无服务查看权限/); assert.equal(calls.length, 0); denied.unmount();
  reset(); actor = { ...actor, permissions: ['services.view', 'services.term.compensate'] }; liveActor = actor; const view = await readyRenderer(); assert.match(view.text(), /无长期改价权限/); assert.deepEqual(view.nodes.find(node => node.props?.['aria-label'] === '客户期限调整类型').children.filter(Boolean).map(node => node.props?.value), ['compensation']);
  assert.equal(calls.filter(call => call.url === '/me').length, 2); assert.ok(calls.every(call => call.portal === 'admin')); view.unmount();
  reset(); liveActor = { ...actor, id: 8 }; const stale = await readyRenderer(); assert.match(stale.text(), /身份或服务查看权限已变化/); assert.equal(calls.some(call => call.url.endsWith('/management')), false); stale.unmount();
});
test('strict management ownership/version rejects mismatches and a late account result is not exposed', async () => {
  for (const mismatch of [{ customer_id: 42 }, { owner_generation: 2 }, { service_version: 4 }]) {
    reset(); management = facts(mismatch); const view = await readyRenderer(); assert.match(view.text(), /归属代次或版本已变化/); assert.equal(view.nodes.some(node => node.type === 'form'), false); view.unmount();
  }
  reset(); respond = async call => { if (call.url === '/me') return liveActor; liveActor = { ...actor, id: 8 }; return management; }; const stale = await readyRenderer(); assert.match(stale.text(), /身份或服务查看权限已变化/); assert.equal(stale.nodes.some(node => node.type === 'form'), false); stale.unmount();
});
test('missing or malformed service identity never becomes a real zero ownership generation', async () => {
  for (const invalid of [{ owner_generation: undefined }, { owner_generation: null }, { owner_generation: -1 }, { owner_generation: '0' }, { owner_generation: 0.5 }, { id: 0 }, { user_id: -1 }, { version: undefined }]) {
    reset(); const view = new Renderer(); view.props.service = { ...view.props.service, ...invalid }; view.render(); await settle(); view.render(); assert.match(view.text(), /服务身份、版本或归属代次缺失/); assert.equal(calls.length, 0); assert.equal(view.nodes.some(node => node.type === 'form'), false); view.unmount();
  }
});
test('pricing preview is read-only, zero fixed amount requires explicit confirmation and contract omits fixed input', async () => {
  reset(); const view = await readyRenderer(); view.label('目标定价模式').onChange({ target: { value: 'fixed' } }); view.render(); view.label('固定周期应付').onChange({ target: { value: '0.00' } }); view.label('长期改价原因').onChange({ target: { value: 'confirmed zero recurring charge' } }); view.render();
  view.form(0).onSubmit({ preventDefault() {} }); await settle(); view.render(); assert.equal(writes().length, 0); const preview = calls.find(call => call.url.endsWith('/renewal-pricing/preview')); assert.deepEqual(JSON.parse(preview.body), { expected_version: 3, mode: 'fixed', fixed_amount_cents: 0 });
  assert.equal(view.button('确认修改长期定价').disabled, true); view.nodes.find(node => node.type === 'checkbox').props.onCheckedChange(true); view.render(); assert.equal(view.button('确认修改长期定价').disabled, false);
  view.button('确认修改长期定价').onClick(); await settle(); view.render(); assert.equal(writes().length, 1); assert.equal(JSON.parse(writes()[0].body).fixed_amount_cents, 0); assert.ok(writes()[0].headers['Idempotency-Key']); assert.equal(refreshes, 1); view.unmount();
  reset(); const contract = await readyRenderer(); contract.label('长期改价原因').onChange({ target: { value: 'restore original contract' } }); contract.render(); contract.form(0).onSubmit({ preventDefault() {} }); await settle(); contract.render();
  const original = calls.find(call => call.url.endsWith('/renewal-pricing/preview')); assert.deepEqual(JSON.parse(original.body), { expected_version: 3, mode: 'contract' }); contract.button('确认修改长期定价').onClick(); await settle(); assert.equal(Object.hasOwn(JSON.parse(writes()[0].body), 'fixed_amount_cents'), false); contract.unmount();
});
test('term UI preserves microseconds and reason binding, compensation only extends and changes invalidate preview', async () => {
  reset(); const view = await readyRenderer(); view.label('客户期限调整类型').onChange({ target: { value: 'compensation' } }); view.label('客户期限调整原因').onChange({ target: { value: 'service incident credit time' } }); view.render();
  view.label('目标客户期限').onChange({ target: { value: '2026-11-01T08:00:00.000122+08:00' } }); view.render(); view.form(1).onSubmit({ preventDefault() {} }); await settle(); view.render(); assert.match(view.text(), /免费补偿必须严格延长/); assert.equal(calls.some(call => call.url.endsWith('/term-adjustment-quotes')), false);
  view.label('目标客户期限').onChange({ target: { value: '2026-12-01T08:00:00.000124+08:00' } }); view.render(); view.form(1).onSubmit({ preventDefault() {} }); await settle(); view.render(); assert.equal(writes().length, 0); assert.equal(view.button('确认调整客户期限').disabled, true);
  view.label('客户期限调整原因').onChange({ target: { value: 'reason changed' } }); view.render(); assert.equal(view.nodes.some(node => node.type === 'button' && view.text(node) === '确认调整客户期限'), false);
  view.form(1).onSubmit({ preventDefault() {} }); await settle(); view.render(); view.nodes.find(node => node.type === 'checkbox').props.onCheckedChange(true); view.render(); view.button('确认调整客户期限').onClick(); await settle();
  assert.equal(writes().length, 1); assert.equal(JSON.parse(writes()[0].body).next_due_at, '2026-12-01T08:00:00.000124+08:00'); assert.equal(JSON.parse(writes()[0].body).reason, 'reason changed'); view.unmount();
});
test('rejected adjustment UI rereads facts and keeps the draft for a fresh preview without automatic resubmission', async () => {
  reset(); const view = await readyRenderer(); view.props.refresh = () => { refreshes++; view.props.service = { ...view.props.service, version: management.service_version }; };
  view.label('目标定价模式').onChange({ target: { value: 'fixed' } }); view.render(); view.label('固定周期应付').onChange({ target: { value: '6.00' } }); view.label('长期改价原因').onChange({ target: { value: 'keep this confirmed request reason' } }); view.render();
  const baseRespond = respond; let reject = true; respond = async call => { if (reject && call.url.endsWith('/renewal-pricing')) throw new ApiError(409, 'stale_pricing_quote'); return baseRespond(call); };
  view.form(0).onSubmit({ preventDefault() {} }); await settle(); view.render(); view.button('确认修改长期定价').onClick(); await settle(); view.render(); const original = writes()[0]; assert.match(view.text(), /原调整请求已拒绝/); assert.equal(storage.size, 1);
  management = facts({ service_version: 4 }); view.button('读取当前管理事实').onClick(); await settle(); view.render(); assert.equal(writes().length, 1); view.button('已核对，结束旧请求并重新预览').onClick(); await settle(); view.render(); await settle(); view.render();
  assert.equal(refreshes, 1); assert.equal(storage.size, 0); assert.equal(view.label('固定周期应付').value, '6.00'); assert.equal(view.label('长期改价原因').value, 'keep this confirmed request reason'); assert.equal(writes().length, 1); assert.equal(view.nodes.some(node => node.type === 'button' && view.text(node) === '确认修改长期定价'), false);
  reject = false; view.form(0).onSubmit({ preventDefault() {} }); await settle(); view.render(); assert.equal(writes().length, 1); view.button('确认修改长期定价').onClick(); await settle(); view.render(); assert.equal(writes().length, 2); assert.equal(JSON.parse(writes()[1].body).expected_version, 4); assert.notEqual(writes()[1].headers['Idempotency-Key'], original.headers['Idempotency-Key']); view.unmount();
});
test('visible term preview expiry disables confirmation and a new preview requires a new explicit confirmation', async () => {
  reset(); const view = await readyRenderer(), timers = []; window.setTimeout = callback => { timers.push(callback); return timers.length; }; window.clearTimeout = () => {};
  view.label('目标客户期限').onChange({ target: { value: '2026-12-01T08:00:00+08:00' } }); view.label('客户期限调整原因').onChange({ target: { value: 'ticket confirms this due date' } }); view.render(); view.form(1).onSubmit({ preventDefault() {} }); await settle(); view.render(); view.nodes.find(node => node.type === 'checkbox').props.onCheckedChange(true); view.render(); assert.equal(view.button('确认调整客户期限').disabled, false);
  timers.at(-1)(); view.render(); assert.match(view.text(), /预览已过期/); assert.equal(view.button('确认调整客户期限').disabled, true); assert.equal(writes().length, 0); assert.equal(calls.filter(call => call.url.endsWith('/term-adjustment-quotes')).length, 1);
  const baseRespond = respond; respond = async call => call.url.endsWith('/term-adjustment-quotes') ? term(JSON.parse(call.body), management, { quote_hash: 'd'.repeat(64) }) : baseRespond(call);
  view.form(1).onSubmit({ preventDefault() {} }); await settle(); view.render(); assert.equal(view.text().includes('预览已过期'), false); assert.equal(view.button('确认调整客户期限').disabled, true); assert.equal(writes().length, 0); view.unmount();
});
test('malformed previews and authority changes cannot expose a confirmation or write an intent', async () => {
  for (const invalid of [{ service_id: 32 }, { customer_id: 42 }, { contract_base_cents: 1001 }, { current_due_at: '2027-01-01T00:00:00Z' }, { quote_hash: 'invalid' }]) {
    reset(); const view = await readyRenderer(), baseRespond = respond; respond = async call => call.url.endsWith('/renewal-pricing/preview') ? { ...pricing(JSON.parse(call.body), management), ...invalid } : baseRespond(call);
    view.label('长期改价原因').onChange({ target: { value: 'preview must match current facts' } }); view.render(); view.form(0).onSubmit({ preventDefault() {} }); await settle(); view.render(); assert.match(view.text(), /预览响应与当前服务、版本或输入不一致/); assert.equal(view.nodes.some(node => node.type === 'button' && view.text(node) === '确认修改长期定价'), false); assert.equal(storage.size, 0); assert.equal(writes().length, 0); view.unmount();
  }
  reset(); const view = await readyRenderer(), baseRespond = respond; respond = async call => { if (call.url.endsWith('/renewal-pricing/preview')) { liveActor = { ...actor, permissions: ['services.view'] }; return pricing(JSON.parse(call.body), management); } return baseRespond(call); };
  view.label('长期改价原因').onChange({ target: { value: 'permission changes during preview' } }); view.render(); view.form(0).onSubmit({ preventDefault() {} }); await settle(); view.render(); assert.match(view.text(), /权限已变化/); assert.equal(view.nodes.some(node => node.type === 'button' && view.text(node) === '确认修改长期定价'), false); assert.equal(writes().length, 0); view.unmount();
});
test('exact integer cents and RFC3339 validation reject ambiguous or impossible input', () => {
  assert.equal(serviceAdjustmentCents('12.34'), 1234); assert.equal(serviceAdjustmentCents('0'), 0);
  for (const invalid of ['1e2', '-1', '1.234', '90071992547409.92']) assert.throws(() => serviceAdjustmentCents(invalid));
  assert.equal(serviceTermMicros('2026-11-01T08:00:00.000123+08:00'), serviceTermMicros('2026-11-01T00:00:00.000123Z'));
  for (const invalid of ['2026-02-30T00:00:00Z', '2026-01-01T24:00:00Z', '2026-01-01T00:00:00.1234567Z', '2026-01-01T00:00:00', '1999-01-01T00:00:00Z']) assert.equal(serviceTermMicros(invalid), null);
});
test('unknown and authorization failures restore exact body/key after remount even after preview expiry', async () => {
  for (const failure of [new TypeError('network'), new ApiError(401, 'unauthorized'), new ApiError(403, 'forbidden'), new ApiError(404, 'not_found'), new ApiError(503, 'unavailable')]) {
    reset(); let view = await readyMutation(); const originalInput = input(); respond = async call => { if (call.url === '/me') return liveActor; if (call.url.endsWith('/management')) return management; throw failure; };
    await view.render().run(originalInput); const original = writes()[0]; assert.ok(original); assert.equal(storage.size, 1); view.unmount();
    const saved = JSON.parse([...storage.values()][0]); saved.quote.expires_at = '2026-01-01T00:00:00Z'; storage.set([...storage.keys()][0], JSON.stringify(saved));
    view = await readyMutation(); respond = async call => call.url === '/me' ? liveActor : call.url.endsWith('/management') ? management : { ...receipt(call), replayed: true };
    assert.ok(await view.render().run()); assert.equal(writes()[1].body, original.body); assert.equal(writes()[1].headers['Idempotency-Key'], original.headers['Idempotency-Key']); assert.equal(storage.size, 0); view.unmount();
  }
});
test('unknown retries cannot become editable rejections; first definitive conflict requires facts/new preview/new key', async () => {
  for (const failure of [new ApiError(400, 'invalid_service_pricing'), new ApiError(422, 'invalid'), new ApiError(409, 'stale_pricing_quote')]) {
    reset(); let view = await readyMutation(); respond = async call => { if (call.url === '/me') return liveActor; if (call.url.endsWith('/management')) return management; throw new TypeError('lost'); };
    await view.render().run(input()); view.unmount(); view = await readyMutation(); respond = async call => { if (call.url === '/me') return liveActor; if (call.url.endsWith('/management')) return management; throw failure; };
    await view.render().run(); assert.equal(view.render().pending.phase, 'uncertain'); assert.equal(await view.render().review(), null); assert.equal(await view.render().endRejected(), null); view.unmount();
  }
  reset(); const view = await readyMutation(); respond = async call => { if (call.url === '/me') return liveActor; if (call.url.endsWith('/management')) return management; throw new ApiError(409, 'stale_pricing_quote'); };
  await view.render().run(input()); const original = writes()[0]; assert.equal(view.render().pending.phase, 'rejected'); assert.equal(await view.render().endRejected(), null);
  management = facts({ service_version: 4 }); assert.equal((await view.render().review()).service_version, 4); assert.equal((await view.render().endRejected()).latest.service_version, 4); assert.equal(storage.size, 0);
  const body = { expected_version: 4, mode: 'fixed', fixed_amount_cents: 600 }, quote = pricing(body, management); respond = async call => call.url === '/me' ? liveActor : call.url.endsWith('/management') ? management : receipt(call);
  assert.ok(await view.render().run({ kind: 'pricing', baseline: management, quote, body: { ...body, quote_hash: quote.quote_hash, reason: 'new confirmed preview' } })); assert.notEqual(writes()[1].headers['Idempotency-Key'], original.headers['Idempotency-Key']); view.unmount();
});
test('expired preview cannot submit or auto-requote and stale service/ownership cannot dispatch', async () => {
  reset(); const view = await readyMutation(), expired = input(); expired.quote.expires_at = '2026-01-01T00:00:00Z'; assert.equal(await view.render().run(expired), null); assert.equal(writes().length, 0); assert.equal(storage.size, 0); assert.match(view.render().error, /预览已过期/);
  management = facts({ service_version: 4 }); assert.equal(await view.render().run(input()), null); assert.equal(writes().length, 0); assert.equal(storage.size, 0); view.unmount();
  for (const mismatch of [{ owner_generation: 1 }, { customer_id: 42 }]) { reset(); const owned = await readyMutation(); management = facts(mismatch); assert.equal(await owned.render().run(input()), null); assert.equal(writes().length, 0); owned.unmount(); }
});
test('malformed receipts, lost live authority and same-customer owner generation changes preserve original intent', async () => {
  for (const mismatch of [{ service_id: 32 }, { customer_id: 42 }, { version: 9 }, { amount_cents: 701 }, { quote_hash: 'c'.repeat(64) }, { replayed: undefined }]) {
    reset(); const view = await readyMutation(); respond = async call => call.url === '/me' ? liveActor : call.url.endsWith('/management') ? management : { ...receipt(call), ...mismatch };
    assert.equal(await view.render().run(input()), null); assert.equal(storage.size, 1); view.unmount();
  }
  reset(); let view = await readyMutation(); respond = async call => { if (call.url === '/me') return liveActor; if (call.url.endsWith('/management')) return management; liveActor = { ...actor, id: 8 }; return receipt(call); };
  assert.equal(await view.render().run(input()), null); assert.equal(storage.size, 1); view.unmount(); liveActor = actor; management = facts({ owner_generation: 2 }); view = await readyMutation(); await view.render().run(); assert.equal(writes().length, 1); assert.equal(storage.size, 1); view.unmount();
});
test('term confirmation validates original instant and exact version; pricing no-op accepts unchanged version', async () => {
  reset(); let view = await readyMutation(); assert.ok(await view.render().run(termInput())); assert.equal(storage.size, 0); view.unmount();
  reset(); view = await readyMutation(); const noOp = input(), quote = pricing({ expected_version: 3, mode: 'contract' }); noOp.quote = quote; noOp.body = { expected_version: 3, mode: 'contract', quote_hash: quote.quote_hash, reason: 'confirm unchanged contract' };
  assert.ok(await view.render().run(noOp)); assert.equal(storage.size, 0); view.unmount();
});
test('corrupt or unavailable storage prevents writes; duplicate and late requests cannot clear another mounted intent', async () => {
  reset(); storage.set('goidc:service-adjustment:admin:7:31', '{broken'); let view = mountMutation(); view.render(); await settle(); assert.equal(view.render().ready, false); await view.render().run(input()); assert.equal(writes().length, 0); view.unmount();
  reset(); view = await readyMutation(); failStorage = true; await view.render().run(input()); assert.equal(writes().length, 0); view.unmount();
  reset(); view = await readyMutation(); let finish; respond = async call => call.url === '/me' ? liveActor : call.url.endsWith('/management') ? management : new Promise(resolve => { finish = resolve; }); const action = view.render(), first = action.run(input()); await action.run(input()); await settle(); assert.equal(writes().length, 1); view.unmount();
  const restored = await readyMutation(); await restored.render().run(); assert.equal(writes().length, 1); finish(receipt(writes()[0])); assert.equal(await first, null); assert.equal(storage.size, 1); restored.unmount();
});
