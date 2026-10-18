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
let current, timers, nextTimer, site, captchaResponse, apiHandler, calls, refreshes, captchaRequests;
let now = Date.parse('2026-10-18T00:00:00Z');
const realNow = Date.now;
Date.now = () => now;
process.on('exit', () => { Date.now = realNow; });
const depsChanged = (before, after) => !before || !after || before.length !== after.length || before.some((value, i) => value !== after[i]);
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
  useId() { return react.useRef(`captcha-test-${current.cursor}`).current; },
  useCallback(callback, dependencies) {
    const state = current, index = state.cursor++;
    if (depsChanged(state.values[index]?.dependencies, dependencies)) state.values[index] = { dependencies, callback };
    return state.values[index].callback;
  },
  useEffect(effect, dependencies) {
    const state = current, index = state.cursor++, before = state.values[index];
    if (depsChanged(before?.dependencies, dependencies)) state.effects.push(() => { before?.cleanup?.(); state.values[index] = { dependencies, cleanup: effect() }; });
  },
};
class ApiError extends Error {
  constructor(status, code, message, details = {}) { super(message); Object.assign(this, { status, code, details }); }
}
const refreshSite = () => { refreshes++; };
const router = { push() {}, refresh() {} };
const Button = props => React.createElement('button', props);
const Input = props => React.createElement('input', props);
const Label = props => React.createElement('label', props);
function load(filename) {
  if (cache.has(filename)) return cache.get(filename).exports;
  const module = new Module(filename);
  cache.set(filename, module);
  module.filename = filename;
  module.paths = Module._nodeModulePaths(path.dirname(filename));
  const external = createRequire(filename);
  module.require = specifier => {
    if (specifier === 'react') return react;
    if (specifier === 'next/navigation') return { useRouter: () => router, useSearchParams: () => new URLSearchParams() };
    if (specifier === 'next/link') return { __esModule: true, default: props => React.createElement('a', props) };
    if (specifier === './site-provider') return { useSite: () => ({ site, refresh: refreshSite }), SiteBrand: () => null, SiteFooter: () => null };
    if (specifier === './common') return { Failure: () => null, Loading: () => null };
    if (specifier === './customer-field-inputs') return { CustomerFieldInputs: () => null, customerFieldValues: () => ({}) };
    if (specifier === '@/components/ui/button') return { Button };
    if (specifier === '@/components/ui/input') return { Input };
    if (specifier === '@/components/ui/label') return { Label };
    if (specifier === '@/lib/api') return {
      ApiError,
      fetchCaptcha: async (action, signal) => { captchaRequests.push({ action, signal }); return typeof captchaResponse === 'function' ? captchaResponse(action, signal) : captchaResponse; },
      api: async (url, options) => { calls.push({ url, body: options?.body ? JSON.parse(options.body) : undefined }); return apiHandler(url, options); },
    };
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
const { CaptchaField, useCaptcha, captchaPresentation, captchaProofReady, registerCaptchaWidget } = load(path.join(web, 'src/components/captcha-field.tsx'));
const { AuthForm } = load(path.join(web, 'src/components/auth-form.tsx'));
const { ForgotPasswordForm } = load(path.join(web, 'src/components/password-reset-form.tsx'));
const { AdminLoginForm } = load(path.join(web, 'src/components/admin-login-form.tsx'));
const image = 'data:image/png;base64,aW1hZ2U=';
const issued = (challenge = { kind: 'image', image }) => ({ enabled: true, captcha_id: 'challenge-1', expires_at: new Date(now + 300000).toISOString(), challenge });
function reset() {
  timers = new Map(); nextTimer = 1; refreshes = 0; calls = []; captchaRequests = [];
  site = { auth_captcha_enabled: true, registration_enabled: true };
  captchaResponse = issued();
  apiHandler = url => {
    if (url === '/auth/verification/channels') return { channels: ['email'] };
    if (url === '/auth/registration-fields') return { items: [], definitions_version: 'v1' };
    if (url === '/auth/register') return { id: 1 };
    return { role: 'admin' };
  };
  globalThis.window = {
    setTimeout(callback, delay) { const id = nextTimer++; timers.set(id, { callback, at: now + delay }); return id; },
    clearTimeout(id) { timers.delete(id); }, setInterval() { return 0; }, clearInterval() {},
  };
  globalThis.localStorage = { getItem() { return null; }, setItem() {}, removeItem() {} };
}
function mount(component, initialProps = {}) {
  const state = { cursor: 0, values: [], effects: [] };
  let props = initialProps;
  const render = nextProps => {
    if (nextProps) props = nextProps;
    state.cursor = 0; current = state;
    let tree;
    try { tree = component(props); } finally { current = undefined; }
    for (const effect of state.effects.splice(0)) effect();
    return tree;
  };
  return { render, unmount() { for (const value of state.values) value?.cleanup?.(); } };
}
async function advance(milliseconds = 0) {
  now += milliseconds;
  for (const [id, timer] of [...timers]) if (timer.at <= now && timers.delete(id)) timer.callback();
  await Promise.resolve(); await Promise.resolve();
}
function nodes(element) {
  if (!element || typeof element !== 'object') return [];
  if (Array.isArray(element)) return element.flatMap(nodes);
  return [element, ...React.Children.toArray(element.props?.children).flatMap(nodes)];
}
function text(element) {
  if (typeof element === 'string' || typeof element === 'number') return String(element);
  if (!element) return '';
  if (Array.isArray(element)) return element.map(text).join('');
  return React.Children.toArray(element.props?.children).map(text).join('');
}
function find(tree, predicate) { const result = nodes(tree).find(predicate); assert.ok(result, 'expected component/control'); return result; }
const field = tree => find(tree, item => item.type === CaptchaField);
const submitButton = tree => find(tree, item => (item.type === Button || item.type === 'button') && !item.props.type && /登录|创建账户|发送验证码|重置密码/.test(text(item)) || item.type === 'button' && item.props.type === 'submit');
const event = { preventDefault() {}, currentTarget: { values: { identifier: 'a@example.test', password: 'password', email: 'a@example.test', display_name: 'Test' } } };
const OriginalFormData = globalThis.FormData;
globalThis.FormData = class { constructor(form) { this.form = form; } get(name) { return this.form.values?.[name] ?? ''; } };
process.on('exit', () => { globalThis.FormData = OriginalFormData; });

test('image answers preserve provider-specific text and compatibility fallback', async () => {
  reset(); captchaResponse = { ...issued(), challenge: undefined, image };
  const hook = mount(() => useCaptcha({ action: 'login', active: true, refreshSite }));
  hook.render(); await advance();
  let captcha = hook.render();
  assert.equal(captcha.ready, false);
  const view = mount(CaptchaField, { captcha });
  const input = find(view.render(), item => item.type === 'input');
  input.props.onChange({ target: { value: '图 a0!? lowercase' } });
  captcha = hook.render();
  assert.equal(captcha.ready, true);
  assert.deepEqual(captcha.fields, { captcha_id: 'challenge-1', captcha_proof: { kind: 'image', answer: '图 a0!? lowercase' }, captcha_answer: '图 a0!? lowercase' });
  assert.equal(captchaProofReady(captcha.challenge.challenge, { kind: 'image', answer: '图'.repeat(400) }), false);
});

test('native click renderer normalizes points, rejects bounds, caps and resets, and supports keyboard', () => {
  reset();
  const challenge = captchaPresentation(issued({ kind: 'click', image, width: 200, height: 100, max_points: 2 }));
  let proof = null;
  const controller = () => ({ challenge: { ...issued(challenge), challenge }, proof, busy: false, error: '', refresh() {}, setProof(next) { proof = next; }, fail() {} });
  const outer = mount(CaptchaField, { captcha: controller() });
  const child = find(outer.render(), item => typeof item.type === 'function' && item.type.name === 'ClickCaptcha');
  const view = mount(child.type, child.props);
  let tree = view.render();
  let button = find(tree, item => item.props?.['aria-label'] === '点击验证码图片');
  const click = (clientX, clientY) => ({ detail: 1, clientX, clientY, currentTarget: { getBoundingClientRect: () => ({ left: 10, top: 20, width: 100, height: 50 }) } });
  button.props.onClick(click(-1, 25)); assert.equal(proof, null);
  button.props.onClick(click(35, 45));
  assert.deepEqual(proof, { kind: 'click', points: [{ x: 0.25, y: 0.5 }] });
  tree = view.render({ ...child.props, proof });
  button = find(tree, item => item.props?.['aria-label'] === '点击验证码图片');
  button.props.onKeyDown({ key: 'ArrowRight', shiftKey: false, preventDefault() {} });
  tree = view.render({ ...child.props, proof });
  button = find(tree, item => item.props?.['aria-label'] === '点击验证码图片');
  button.props.onKeyDown({ key: 'Enter', preventDefault() {} });
  assert.deepEqual(proof.points[1], { x: 0.55, y: 0.5 });
  tree = view.render({ ...child.props, proof });
  find(tree, item => item.props?.['aria-label'] === '点击验证码图片').props.onClick(click(50, 50));
  assert.equal(proof.points.length, 2);
  find(tree, item => item.type === 'button' && text(item) === '重新选择').props.onClick();
  assert.equal(proof, null);
  assert.equal(captchaProofReady(challenge, { kind: 'click', points: [{ x: NaN, y: 0 }] }), false);
  assert.equal(captchaProofReady(challenge, { kind: 'widget', token: 'success' }), false);
});

test('compiled widget extensions submit tokens and stale callbacks cannot fill another challenge', async () => {
  reset();
  const Widget = () => null;
  registerCaptchaWidget('test_local_widget', Widget);
  assert.throws(() => registerCaptchaWidget('test_local_widget', Widget));
  captchaResponse = issued({ kind: 'widget', renderer: 'test_local_widget', public_data: { sitekey: 'public' } });
  const hook = mount(() => useCaptcha({ action: 'login', active: true, refreshSite }));
  hook.render(); await advance();
  let captcha = hook.render();
  const view = mount(CaptchaField, { captcha });
  const oldWidget = find(view.render(), item => item.type === Widget);
  oldWidget.props.onToken('verified-token');
  captcha = hook.render(); assert.equal(captcha.ready, true);
  assert.deepEqual(captcha.fields.captcha_proof, { kind: 'widget', token: 'verified-token' });
  captchaResponse = { ...captchaResponse, captcha_id: 'challenge-2' };
  await captcha.refresh(); captcha = hook.render();
  oldWidget.props.onToken('stale-token'); oldWidget.props.onError('stale error');
  captcha = hook.render(); assert.equal(captcha.ready, false); assert.equal(captcha.proof, null); assert.equal(captcha.error, '');
  const nextWidget = find(view.render({ captcha }), item => item.type === Widget);
  nextWidget.props.onToken('next-token'); assert.equal(hook.render().ready, true);
  nextWidget.props.onToken(null); assert.equal(hook.render().ready, false);
  nextWidget.props.onError('供应商暂不可用');
  assert.equal(hook.render().error, '供应商暂不可用'); assert.equal(hook.render().ready, false);
});

test('unknown kinds/renderers and malformed images fail closed with a visible retry', async () => {
  for (const challenge of [{ kind: 'unknown' }, { kind: 'widget', renderer: 'not_compiled' }, { kind: 'image', renderer: 'remote_renderer', image }, { kind: 'image', image: 'javascript:alert(1)' }, { kind: 'click', image, width: 200, height: 100, max_points: 33 }]) {
    reset(); captchaResponse = issued(challenge);
    const hook = mount(() => useCaptcha({ action: 'login', active: true, refreshSite }));
    hook.render(); await advance(); const captcha = hook.render();
    assert.equal(captcha.ready, false); assert.equal(captcha.challenge, null); assert.ok(captcha.error);
    const tree = mount(CaptchaField, { captcha }).render();
    find(tree, item => item.props?.role === 'alert'); find(tree, item => item.type === 'button' && text(item) === '刷新验证码');
  }
});

test('refresh aborts stale requests; expiry clears proof and disabled response refreshes site', async () => {
  reset(); const pending = [];
  captchaResponse = () => new Promise(resolve => pending.push(resolve));
  const hook = mount(() => useCaptcha({ action: 'login', active: true, refreshSite }));
  hook.render(); await advance();
  const second = hook.render().refresh();
  assert.equal(captchaRequests[0].signal.aborted, true);
  pending[1]({ ...issued(), captcha_id: 'latest' }); await second;
  pending[0]({ ...issued(), captcha_id: 'stale' }); await advance();
  let captcha = hook.render(); assert.equal(captcha.challenge.captcha_id, 'latest');
  captcha.setProof({ kind: 'image', answer: 'answer' }); captcha = hook.render(); assert.equal(captcha.ready, true);
  now += 300001; assert.equal(captcha.canSubmit(), false);
  await advance(); captcha = hook.render();
  assert.equal(captcha.proof, null); assert.equal(captcha.ready, false); assert.match(captcha.error, /过期/);
  captchaResponse = { enabled: false }; await captcha.refresh();
  assert.equal(refreshes, 1); assert.equal(hook.render().ready, false);
});

test('challenge and proof byte limits include UTF-8 and serialized JSON escaping', () => {
  reset();
  const widget = { kind: 'widget', renderer: 'test_local_widget' };
  assert.equal(captchaProofReady(widget, { kind: 'widget', token: 'a'.repeat(12 * 1024) }), true);
  assert.equal(captchaProofReady(widget, { kind: 'widget', token: 'a'.repeat(12 * 1024 + 1) }), false);
  assert.equal(captchaProofReady(widget, { kind: 'widget', token: '"'.repeat(10000) }), false);
  assert.equal(captchaProofReady(widget, { kind: 'widget', token: '图'.repeat(5000) }), false);
  assert.throws(() => captchaPresentation(issued({ kind: 'image', image, prompt: 'x'.repeat(64 * 1024) })), /过大/);
});

test('action and MFA stage transitions reject callbacks from the previous challenge', async () => {
  reset();
  const instance = mount(props => useCaptcha({ ...props, refreshSite }), { action: 'login', active: true });
  instance.render(); await advance();
  const previous = instance.render(); previous.setProof({ kind: 'image', answer: 'old' });
  assert.equal(instance.render().ready, true);
  let next = instance.render({ action: 'register', active: true });
  assert.equal(next.ready, false); assert.equal(next.challenge, null); assert.deepEqual(next.fields, {});
  previous.setProof({ kind: 'image', answer: 'stale' });
  captchaResponse = { ...issued(), captcha_id: 'register-challenge' };
  await advance(); next = instance.render(); assert.equal(next.proof, null);
  previous.fail('old provider error'); assert.equal(instance.render().error, '');
  next.setProof({ kind: 'image', answer: 'new' }); assert.equal(instance.render().ready, true);
  instance.render({ action: 'register', active: false });
  next.setProof({ kind: 'image', answer: 'stale stage' }); next.fail('stale stage error');
  await advance();
  const inactive = instance.render(); assert.equal(inactive.ready, true); assert.equal(inactive.proof, null); assert.deepEqual(inactive.fields, {});
});

test('all auth forms submit typed proofs, block missing proof, and preserve auth errors after refresh', async () => {
  for (const [component, url, props] of [[AuthForm, '/auth/login', {}], [AuthForm, '/auth/register', { register: true }], [AdminLoginForm, '/auth/login', {}], [ForgotPasswordForm, '/auth/password-reset/request', {}]]) {
    reset(); const instance = mount(component, props);
    let tree = instance.render(); await advance(); tree = instance.render();
    assert.equal(submitButton(tree).props.disabled, true, component.name);
    await find(tree, item => item.type === 'form').props.onSubmit(event);
    assert.equal(calls.some(call => call.url === url), false);
    field(tree).props.captcha.setProof({ kind: 'image', answer: 'Abc 01' });
    tree = instance.render(); assert.equal(submitButton(tree).props.disabled, false, component.name);
    apiHandler = target => { if (target === url) throw new ApiError(400, 'credentials_invalid', '保留这条认证错误'); return {}; };
    await find(tree, item => item.type === 'form').props.onSubmit(event); await advance(); tree = instance.render();
    assert.deepEqual(calls.find(call => call.url === url).body.captcha_proof, { kind: 'image', answer: 'Abc 01' });
    assert.match(text(tree), /保留这条认证错误/); assert.equal(field(tree).props.captcha.proof, null);
    instance.unmount();
  }
});

test('MFA stage remains submittable after image proof is cleared and uses only MFA endpoint', async () => {
  for (const component of [AuthForm, AdminLoginForm]) {
    reset(); const instance = mount(component);
    instance.render(); await advance(); let tree = instance.render();
    field(tree).props.captcha.setProof({ kind: 'image', answer: 'ABCDEF' }); tree = instance.render();
    apiHandler = url => { if (url === '/auth/login') throw new ApiError(401, 'mfa_required', 'mfa', { challenge: 'mfa-challenge' }); return { role: 'admin' }; };
    await find(tree, item => item.type === 'form').props.onSubmit(event);
    tree = instance.render(); await advance(); tree = instance.render();
    assert.equal(nodes(tree).some(item => item.type === CaptchaField), false);
    assert.equal(submitButton(tree).props.disabled, false);
    await find(tree, item => item.type === 'form').props.onSubmit(event);
    assert.equal(calls.at(-1).url, '/auth/login/mfa'); assert.equal('captcha_proof' in calls.at(-1).body, false);
    instance.unmount();
  }
});
