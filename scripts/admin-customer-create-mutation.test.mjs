import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createRequire, Module } from 'node:module';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const web = fileURLToPath(new URL('../web/', import.meta.url));
const require = createRequire(path.join(web, 'package.json'));
const ts = require('typescript');
let current, calls, respond;
const changed = (before, after) => !before || before.length !== after.length || before.some((value, index) => value !== after[index]);
const react = {
  useState(initial) { const state = current, index = state.cursor++; if (!(index in state.values)) state.values[index] = typeof initial === 'function' ? initial() : initial; return [state.values[index], next => { state.values[index] = typeof next === 'function' ? next(state.values[index]) : next; }]; },
  useRef(initial) { const state = current, index = state.cursor++; if (!(index in state.values)) state.values[index] = { current: initial }; return state.values[index]; },
  useEffect(effect, deps) { const state = current, index = state.cursor++, previous = state.values[index]; if (changed(previous?.deps, deps)) state.effects.push(() => { previous?.cleanup?.(); state.values[index] = { deps, cleanup: effect() }; }); },
};
class ApiError extends Error { constructor(status, code) { super(code); this.status = status; this.code = code; } }
const filename = path.join(web, 'src/components/admin-customer-create-mutation.ts');
const module = new Module(filename);
module.require = name => name === 'react' ? react : name === '@/lib/api' ? { ApiError, api: async (url, options) => { const call = { url, ...options }; calls.push(call); return respond(call); } } : require(name);
module._compile(ts.transpileModule(readFileSync(filename, 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText, filename);
const { useCustomerCreateMutation } = module.exports;
function mount() {
  const state = { cursor: 0, values: [], effects: [] };
  return { actor: 7, allowed: true, render() { state.cursor = 0; current = state; const result = useCustomerCreateMutation(this.actor, this.allowed); current = null; state.effects.splice(0).forEach(effect => effect()); return result; }, unmount() { state.values.forEach(value => value?.cleanup?.()); } };
}
const payload = { email: 'customer@example.test', password: 'memory-only-password', status: 'active', custom_fields: { number_value: 0, boolean_value: false }, custom_fields_version: 3 };
const success = replayed => ({ id: 41, email: payload.email, username: 'customer', display_name: 'customer', status: 'active', profile_version: 1, replayed });
function reset() {
  calls = [];
  respond = async () => success(false);
  // Accessing either storage is a test failure: initial passwords stay in memory.
  globalThis.sessionStorage = new Proxy({}, { get() { throw new Error('password must not enter session storage'); } });
  globalThis.localStorage = new Proxy({}, { get() { throw new Error('password must not enter local storage'); } });
}

test('lost creation retains exact in-memory body/key through auth failures, conflicts and final replay', async () => {
  reset(); const view = mount();
  for (const failure of [new TypeError('network lost'), new ApiError(401, 'unauthorized'), new ApiError(403, 'permission_denied'), new ApiError(409, 'idempotency_conflict'), new ApiError(503, 'unavailable')]) {
    respond = async () => { throw failure; };
    assert.equal(await view.render().run(calls.length ? { email: 'replacement@example.test', password: 'changed' } : payload), null);
    assert.equal(view.render().uncertain, true);
    assert.equal(view.render().frozen, true);
    assert.equal(view.render().conflict, false);
  }
  respond = async () => success(true);
  assert.equal((await view.render().run({})).id, 41);
  assert.equal(calls.length, 6);
  for (const call of calls) {
    assert.equal(call.url, '/admin/customers'); assert.equal(call.method, 'POST');
    assert.equal(call.body, JSON.stringify(payload));
    assert.equal(call.headers['Idempotency-Key'], calls[0].headers['Idempotency-Key']);
  }
  assert.equal(view.render().frozen, false); view.unmount();
});

test('permission loss blocks retry without releasing the unresolved intent', async () => {
  reset(); const view = mount(); respond = async () => { throw new TypeError('network'); };
  await view.render().run(payload); const original = calls[0];
  view.allowed = false; assert.equal(view.render().uncertain, true);
  await view.render().run({}); assert.equal(calls.length, 1);
  view.allowed = true; respond = async () => success(true);
  assert.ok(await view.render().run({}));
  assert.equal(calls[1].body, original.body); assert.equal(calls[1].headers['Idempotency-Key'], original.headers['Idempotency-Key']); view.unmount();
});

test('definitive validation and version refusals permit corrected input with a fresh key', async () => {
  for (const failure of [new ApiError(400, 'invalid_custom_field'), new ApiError(422, 'invalid_customer'), new ApiError(409, 'stale_customer_fields')]) {
    reset(); const view = mount(); respond = async () => { throw failure; };
    await view.render().run(payload); assert.equal(view.render().uncertain, false); assert.equal(view.render().frozen, false);
    respond = async () => success(false);
    assert.ok(await view.render().run({ ...payload, custom_fields_version: 4 }));
    assert.notEqual(calls[1].headers['Idempotency-Key'], calls[0].headers['Idempotency-Key']);
    assert.equal(JSON.parse(calls[1].body).custom_fields_version, 4); view.unmount();
  }
});

test('missing or mismatched success confirmations keep the creation locked for original replay', async () => {
  for (const result of [{}, { ...success(false), id: 0 }, { ...success(false), email: 'another@example.test' }, { ...success(false), profile_version: 2 }]) {
    reset(); const view = mount(); respond = async () => result;
    assert.equal(await view.render().run(payload), null); assert.equal(view.render().uncertain, true); assert.equal(view.render().frozen, true); view.unmount();
  }
});

test('duplicate clicks send once and late completion cannot confirm another administrator', async () => {
  reset(); const view = mount(); let finish;
  respond = () => new Promise(resolve => { finish = resolve; });
  const mutation = view.render(), first = mutation.run(payload);
  await mutation.run(payload); assert.equal(calls.length, 1);
  view.actor = 8; assert.equal(view.render().uncertain, false);
  finish(success(false)); assert.equal(await first, null); assert.equal(view.render().busy, false); view.unmount();
});
