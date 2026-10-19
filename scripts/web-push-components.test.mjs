import assert from 'node:assert/strict';
import { webcrypto } from 'node:crypto';
import { existsSync, readFileSync } from 'node:fs';
import { createRequire, Module } from 'node:module';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';

const web = fileURLToPath(new URL('../web/', import.meta.url));
const require = createRequire(path.join(web, 'package.json'));
const React = require('react');
const ts = require('typescript');
const { renderToStaticMarkup } = require('react-dom/server');
let current;
let resources;
let requests;
let refreshes;
let resourcePaths;
let resourcePortals;
let requestPortals;
let actions;
let respond;
let local;
let registration;
let permissionResult;
let effectQueue;
let pendingBrowser;
const cache = new Map();
class ApiError extends Error { constructor(status, code = 'test', message = '请求失败') { super(message); this.status = status; this.code = code; } }
const react = { ...React,
  useState(initial) { const state = current; const i = state.cursor++; if (!(i in state.values)) state.values[i] = typeof initial === 'function' ? initial() : initial; return [state.values[i], next => { state.values[i] = typeof next === 'function' ? next(state.values[i]) : next; }]; },
  useCallback(callback, deps) { const state = current; const i = state.cursor++; if (!state.values[i] || deps.some((value, n) => value !== state.values[i].deps[n])) state.values[i] = { deps, callback }; return state.values[i].callback; },
  useEffect(callback, deps) { const state = current; const i = state.cursor++; const previous = state.values[i]; if (!previous || deps.some((value, n) => value !== previous.deps[n])) { const effect = { deps, cleanup: undefined }; state.values[i] = effect; effectQueue.push(() => { previous?.cleanup?.(); effect.cleanup = callback(); }); } },
};
function load(filename) {
  if (cache.has(filename)) return cache.get(filename).exports;
  const module = new Module(filename); cache.set(filename, module); module.filename = filename;
  module.paths = Module._nodeModulePaths(path.dirname(filename)); const external = createRequire(filename);
  module.require = specifier => {
    if (specifier === 'react') return react;
    if (specifier === './workspace') return { useAdminPermission: () => true };
    if (specifier === '@/hooks/use-admin-form-leave') return { useAdminFormLeave() {} };
    if (specifier === '@/hooks/use-resource') return { useResource: (requestPath, _keepPrevious, portal) => { resourcePaths.push(requestPath); resourcePortals.push({ path: requestPath, portal }); return { data: resources.get(requestPath)?.data ?? null, error: resources.get(requestPath)?.error ?? null, refresh: () => refreshes.push(requestPath) }; } };
    if (specifier === '@/lib/api') return { ApiError, date: value => value, currentAuthPortal: apiHelpers.currentAuthPortal, authPortal: apiHelpers.authPortal, api: async (requestPath, options = {}) => { const request = { path: requestPath, method: options.method ?? 'GET', body: options.body ? JSON.parse(options.body) : undefined }; requests.push(request); requestPortals.push(apiHelpers.authPortal(requestPath, options.portal)); actions.push('api:' + request.method); return respond(request); } };
    if (specifier.startsWith('.') || specifier.startsWith('@/')) {
      const base = specifier.startsWith('@/') ? path.join(web, 'src', specifier.slice(2)) : path.resolve(path.dirname(filename), specifier);
      const source = [base, `${base}.tsx`, `${base}.ts`].find(candidate => existsSync(candidate));
      if (source && /\.tsx?$/.test(source)) return load(source);
    }
    return external(specifier);
  };
  module._compile(ts.transpileModule(readFileSync(filename, 'utf8'), { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true } }).outputText, filename);
  return module.exports;
}
const apiHelpers = load(path.join(web, 'src/lib/api.ts'));
const helper = load(path.join(web, 'src/lib/web-push.ts'));
const { WebPushPreferences } = load(path.join(web, 'src/components/web-push-preferences.tsx'));
const { WebPushHistory } = load(path.join(web, 'src/components/web-push-history.tsx'));
const { Failure, Loading } = load(path.join(web, 'src/components/common.tsx'));
const { Button } = load(path.join(web, 'src/components/ui/button.tsx'));
const { Checkbox } = load(path.join(web, 'src/components/ui/checkbox.tsx'));
const { NotificationCenter } = load(path.join(web, 'src/components/notification-center.tsx'));
const statePath = '/me/notification-web-push';
const keyBytes = Uint8Array.from({ length: 65 }, (_, i) => i ? i : 4);
const key = Buffer.from(keyBytes).toString('base64url');
const instance = { id: 'push-1', name: '浏览器通知', public_key: key, version: 3 };
const endpoint = 'https://fcm.googleapis.com/fcm/send/secret-capability-endpoint';
function makeSubscription(bytes = keyBytes, target = endpoint) {
  return { endpoint: target, options: { applicationServerKey: bytes.slice().buffer }, toJSON() { return { endpoint: target, keys: { p256dh: 'browser-key', auth: 'browser-auth' } }; }, async unsubscribe() { actions.push('unsubscribe'); local = null; return true; } };
}
function reset(pathname = '/console/account') {
  resources = new Map([[statePath, { data: { instances: [instance], subscriptions: [] } }]]);
  requests = []; refreshes = []; resourcePaths = []; resourcePortals = []; requestPortals = []; actions = []; effectQueue = []; pendingBrowser = new Set(); local = null; permissionResult = 'granted'; respond = async () => ({});
  registration = { active: {}, pushManager: { async getSubscription() { return local; }, async subscribe(options) { actions.push('subscribe'); assert.equal(options.userVisibleOnly, true); assert.deepEqual(new Uint8Array(options.applicationServerKey), keyBytes); local = makeSubscription(); return local; } } };
  const events = new EventTarget();
  globalThis.window = { location: { pathname }, requestAnimationFrame: callback => setImmediate(callback), cancelAnimationFrame: handle => clearImmediate(handle), isSecureContext: true, PushManager: function () {}, Notification: {}, matchMedia: () => ({ matches: false }), addEventListener: events.addEventListener.bind(events), removeEventListener: events.removeEventListener.bind(events), dispatchEvent: events.dispatchEvent.bind(events), confirm: () => true };
  Object.defineProperty(globalThis, 'navigator', { configurable: true, value: { userAgent: 'TestBrowser', serviceWorker: { getRegistration: async () => registration, register: async (script, options) => { actions.push('register'); assert.equal(script, '/notification-sw.js'); assert.deepEqual(options, { scope: '/', updateViaCache: 'none' }); return registration; }, ready: Promise.resolve(registration) } } });
  globalThis.Notification = { permission: 'default', requestPermission: async () => { actions.push('permission'); return permissionResult; } };
  Object.defineProperty(globalThis, 'crypto', { configurable: true, value: { subtle: { digest(...args) { const operation = webcrypto.subtle.digest(...args); pendingBrowser.add(operation); operation.finally(() => pendingBrowser.delete(operation)); return operation; } } } });
}
function mount(props = {}, Component = WebPushPreferences) {
  const state = { cursor: 0, values: [] };
  return () => { state.cursor = 0; current = state; try { return Component(props); } finally { current = undefined; } };
}
async function effects() { const queue = effectQueue.splice(0); queue.forEach(effect => effect()); await settle(); }
async function settle() { for (let n = 0; n < 5; n++) { await new Promise(resolve => setImmediate(resolve)); await Promise.all([...pendingBrowser]); } }
function nodes(element) { if (!element || typeof element !== 'object') return []; if (Array.isArray(element)) return element.flatMap(nodes); return [element, ...React.Children.toArray(element.props?.children).flatMap(nodes)]; }
function text(element) { if (typeof element === 'string' || typeof element === 'number') return String(element); if (!element) return ''; if (Array.isArray(element)) return element.map(text).join(''); return React.Children.toArray(element.props?.children).map(text).join(''); }
function button(tree, label) { const item = nodes(tree).find(item => item.type === Button && text(item) === label); assert.ok(item, `missing button ${label}`); return item; }
async function click(render, label) { await button(render(), label).props.onClick(); await settle(); }

test('page entry checks support without requesting notification permission or creating a subscription', async () => {
  reset(); const render = mount(); render(); await effects();
  assert.match(text(render()), /尚未授权通知/); assert.deepEqual(actions, []); assert.deepEqual(requests, []);
  assert.equal(button(render(), '开启此浏览器通知').props.disabled, false);
});

test('insecure context, unsupported browser and denied permission show distinct blocked states', async () => {
  for (const [mode, expression] of [['insecure', /需要 HTTPS/], ['unsupported', /不支持后台推送/], ['denied', /浏览器已阻止/]]) {
    reset(); if (mode === 'insecure') window.isSecureContext = false; if (mode === 'unsupported') delete window.PushManager; if (mode === 'denied') Notification.permission = 'denied';
    const render = mount(); render(); await effects(); assert.match(text(render()), expression); assert.equal(button(render(), '开启此浏览器通知').props.disabled, true); assert.deepEqual(actions, []);
  }
});

test('opt-in requests permission from click, registers notification-only worker and saves version zero without enabling business preference', async () => {
  reset(); const render = mount(); render(); await effects(); await click(render, '开启此浏览器通知');
  assert.deepEqual(actions, ['permission', 'register', 'subscribe', 'api:POST']);
  assert.deepEqual(requests, [{ path: statePath + '/subscriptions', method: 'POST', body: { instance_id: instance.id, subscription: { endpoint, keys: { p256dh: 'browser-key', auth: 'browser-auth' } }, expected_version: 0 } }]);
  assert.match(text(render()), /订阅本身不会开启业务通知/); assert.doesNotMatch(text(render()), /secret-capability|browser-auth/);
});

test('subscription opt-in keeps the initiating portal across permission, worker registration and subscription awaits', async () => {
  for (const [page, portal, otherPage] of [['/admin/account', 'admin', '/console/account'], ['/console/account', 'customer', '/admin/account']]) for (const stage of ['permission', 'registration', 'subscription']) {
    reset(page); const render = mount(); render(); await effects();
    let release, entered;
    const gate = new Promise(resolve => { release = resolve; });
    const paused = new Promise(resolve => { entered = resolve; });
    const delay = callback => async (...args) => { const result = await callback(...args); entered(); await gate; return result; };
    if (stage === 'permission') Notification.requestPermission = delay(Notification.requestPermission);
    if (stage === 'registration') navigator.serviceWorker.register = delay(navigator.serviceWorker.register);
    if (stage === 'subscription') registration.pushManager.subscribe = delay(registration.pushManager.subscribe);
    const operation = button(render(), '开启此浏览器通知').props.onClick();
    await paused;
    assert.deepEqual(requests, []);
    window.location.pathname = otherPage;
    release(); await operation; await settle();
    assert.equal(requests.length, 1); assert.equal(requests[0].method, 'POST');
    assert.deepEqual(requestPortals, [portal], `${portal} changed session after awaiting ${stage}`);
    assert.equal(resourcePortals.find(item => item.path === statePath).portal, portal);
  }
});

test('subscription management reacts only to its own portal unauthorized event', async () => {
  for (const [page, portal] of [['/admin/account', 'admin'], ['/console/account', 'customer']]) {
    reset(page); const render = mount(); render(); await effects();
    window.dispatchEvent(new CustomEvent('zjmfgo:unauthorized', { detail: { path: '/me', portal: portal === 'admin' ? 'customer' : 'admin' } }));
    assert.equal(button(render(), '开启此浏览器通知').props.disabled, false);
    assert.doesNotMatch(text(render()), /登录会话已失效/);
    window.dispatchEvent(new CustomEvent('zjmfgo:unauthorized', { detail: { path: '/me', portal } }));
    assert.equal(button(render(), '开启此浏览器通知').props.disabled, true);
    assert.match(text(render()), /登录会话已失效/);
    await button(render(), '开启此浏览器通知').props.onClick(); await settle();
    assert.deepEqual(requests, []); assert.deepEqual(actions, []);
  }
});

test('dismissed or denied prompt never subscribes and remains recoverable', async () => {
  for (const permission of ['default', 'denied']) { reset(); permissionResult = permission; const render = mount(); render(); await effects(); await click(render, '开启此浏览器通知'); assert.deepEqual(actions, ['permission']); assert.deepEqual(requests, []); assert.match(text(render()), permission === 'default' ? /未创建订阅/ : /通知授权已被拒绝/); }
});

test('reconfirmation reuses this browser endpoint with its CAS version after a new login', async () => {
  reset(); local = makeSubscription(); const hash = await helper.webPushEndpointHash(endpoint);
  resources.get(statePath).data.subscriptions = [{ id: 4, instance_id: instance.id, endpoint_hash: hash, version: 7, active: true, revocable: true, current_session: false }];
  const render = mount(); render(); await effects(); await click(render, '开启此浏览器通知');
  assert.equal(requests[0].body.expected_version, 7); assert.ok(!actions.includes('subscribe')); assert.ok(!actions.includes('unsubscribe'));
});

test('key rotation never silently destroys existing subscription; reset is an explicit action', async () => {
  reset(); const other = keyBytes.slice(); other[4] = 99; local = makeSubscription(other);
  const render = mount(); render(); await effects(); await click(render, '开启此浏览器通知');
  assert.deepEqual(requests, []); assert.ok(!actions.includes('unsubscribe')); assert.match(text(render()), /旧公钥/);
  await click(render, '重置此浏览器订阅'); assert.ok(actions.includes('unsubscribe')); assert.match(text(render()), /为当前账户创建新的订阅/);
});

test('an endpoint owned by another account is not inherited or automatically unsubscribed', async () => {
  reset(); local = makeSubscription(); respond = async () => { throw new ApiError(409, 'web_push_endpoint_owned'); };
  const render = mount(); render(); await effects(); await click(render, '开启此浏览器通知');
  assert.match(text(render()), /属于其他登录身份/); assert.ok(!actions.includes('unsubscribe')); assert.equal(button(render(), '开启此浏览器通知').props.disabled, true);
  await click(render, '重置此浏览器订阅'); assert.ok(actions.includes('unsubscribe'));
});

test('uncertain save keeps local subscription for reconciliation; CAS conflict refreshes without an overwrite', async () => {
  for (const error of [new Error('网络暂时不可用'), new ApiError(409, 'version_conflict')]) {
    reset(); respond = async () => { throw error; }; const render = mount(); render(); await effects(); await click(render, '开启此浏览器通知');
    assert.equal(requests.length, 1); assert.ok(!actions.includes('unsubscribe')); assert.ok(refreshes.includes(statePath));
    assert.match(text(render()), error.code === 'version_conflict' ? /其他页面更新/ : /网络暂时不可用/);
  }
});

test('remote revocation uses its version and leaves an unrelated local endpoint intact', async () => {
  reset(); local = makeSubscription(); resources.get(statePath).data.subscriptions = [{ id: 'remote-5', instance_id: instance.id, endpoint_hash: 'other-hash', version: 8, current_session: false, active: true, revocable: true }];
  const render = mount(); render(); await effects(); await click(render, '撤销订阅');
  assert.deepEqual(requests, [{ path: statePath + '/subscriptions/remote-5', method: 'DELETE', body: { expected_version: 8 } }]); assert.ok(!actions.includes('unsubscribe'));
});

test('local revoke persists server revocation before unsubscribing; rejection preserves browser subscription', async () => {
  for (const fail of [false, true]) {
    reset(); local = makeSubscription(); const hash = await helper.webPushEndpointHash(endpoint);
    resources.get(statePath).data.subscriptions = [{ id: 5, instance_id: instance.id, endpoint_hash: hash, version: 9, current_session: true, active: true, revocable: true }];
    if (fail) respond = async () => { throw new ApiError(409, 'version_conflict'); };
    const render = mount(); render(); await effects(); await click(render, '撤销订阅');
    assert.equal(actions[0], 'api:DELETE'); assert.equal(actions.includes('unsubscribe'), !fail);
  }
});

test('availability requires active subscription, matching usable instance and current session', () => {
  const record = { id: 1, instance_id: instance.id, endpoint_hash: 'hash', version: 1, active: true, revocable: true, current_session: true };
  assert.equal(helper.webPushAvailable(null), false);
  assert.equal(helper.webPushAvailable({ instances: [instance], subscriptions: [record] }), true);
  for (const altered of [{ ...record, active: false }, { ...record, current_session: false }, { ...record, instance_id: 'missing' }]) assert.equal(helper.webPushAvailable({ instances: [instance], subscriptions: [altered] }), false);
  assert.equal(helper.webPushExpectedVersion({ subscriptions: [record] }, 'hash'), 1); assert.equal(helper.webPushExpectedVersion({ subscriptions: [record] }, 'new'), 0);
  for (const invalid of ['', 'abc', '!key', Buffer.alloc(65).toString('base64url')]) assert.throws(() => helper.webPushKey(invalid));
});

function worker() {
  const listeners = {}; const notifications = []; const opened = []; let windows = [];
  const self = { location: { origin: 'https://account.example' }, addEventListener(type, callback) { listeners[type] = callback; }, registration: { showNotification: async (title, options) => notifications.push({ title, options }) }, clients: { matchAll: async () => windows, openWindow: async url => opened.push(url) } };
  vm.runInNewContext(readFileSync(path.join(web, 'public/notification-sw.js'), 'utf8'), { self, URL });
  return { listeners, notifications, opened, setWindows(value) { windows = value; } };
}

test('notification-only worker treats absent or malformed payloads as fixed customer notifications', async () => {
  const { listeners, notifications } = worker(); assert.deepEqual(Object.keys(listeners).sort(), ['notificationclick', 'push']);
  for (const data of [undefined, { json() { throw new Error('invalid JSON'); } }, { json: () => null }, { json: () => 'arbitrary payload' }]) {
    let waited; listeners.push({ data, waitUntil(promise) { waited = promise; } }); await waited;
    const notification = notifications.at(-1);
    assert.equal(notification.title, '账户通知'); assert.equal(notification.options.body, '您有新的账户通知，请登录查看。');
    assert.equal(notification.options.data.destination, '/console/account'); assert.equal(notification.options.tag, 'goidc-account-notification');
    assert.equal(notification.options.icon, '/assets/notification-icon-192.png');
    assert.deepEqual(Object.keys(notification.options.data), ['destination']);
  }
});

test('push payloads select only the exact two account paths while business content never reaches notification display', async () => {
  const state = worker();
  for (const url of ['/admin/account', '/console/account', '/admin', '/admin/account?secret=invoice', '/admin/account#invoice', 'https://account.example/admin/account', 'https://evil.example/', '//evil.example/admin/account', 'javascript:alert(1)', '/admin%2faccount', '/console/services', '', null]) {
    let waited;
    state.listeners.push({ data: { json: () => ({ url, title: 'private-title', body: 'private-invoice-amount', variables: { password: 'private-password' }, data: { destination: 'https://evil.example/' } }) }, waitUntil(promise) { waited = promise; } });
    await waited;
    const notification = state.notifications.at(-1), admin = url === '/admin/account';
    assert.equal(notification.title, '账户通知'); assert.equal(notification.options.body, '您有新的账户通知，请登录查看。');
    assert.equal(notification.options.data.destination, admin ? '/admin/account' : '/console/account');
    assert.equal(notification.options.tag, admin ? 'goidc-admin-notification' : 'goidc-account-notification');
    assert.doesNotMatch(JSON.stringify(notification), /private-|evil\.example|invoice|password/);
  }
});

test('notification clicks ignore untrusted paths and focus only exact same-origin windows for the selected portal', async () => {
  for (const destination of ['/admin/account', '/console/account', '/admin/account?next=evil', 'https://account.example/admin/account', 'https://evil.example/', '//evil.example/', 'javascript:alert(1)', '/console/services', undefined]) {
    const state = worker(), path = destination === '/admin/account' ? '/admin/account' : '/console/account';
    let focused = false, closed = false, waited;
    state.setWindows([
      { url: `https://evil.example${path}`, focus() { throw new Error('foreign window'); } },
      { url: `https://account.example${path}?secret=invoice`, focus() { throw new Error('unrelated URL'); } },
      { url: `https://account.example${path === '/admin/account' ? '/console/account' : '/admin/account'}`, focus() { throw new Error('wrong portal'); } },
    ]);
    const event = { notification: { data: { destination, url: 'https://evil.example/', title: 'private-title' }, close() { closed = true; } }, waitUntil(promise) { waited = promise; } };
    state.listeners.notificationclick(event); await waited;
    assert.equal(closed, true); assert.deepEqual(state.opened, [`https://account.example${path}`]);
    state.setWindows([{ url: `https://account.example${path}`, async focus() { focused = true; } }]);
    state.listeners.notificationclick(event); await waited; assert.equal(focused, true); assert.equal(state.opened.length, 1);
  }
});


test('invalid-session or rotated-key records remain revocable while explicit tombstones stay hidden and retain CAS', async () => {
  reset(); resources.get(statePath).data.subscriptions = [
    { id: 9, instance_id: instance.id, endpoint_hash: 'expired', version: 4, active: false, revocable: true, current_session: false },
    { id: 10, instance_id: instance.id, endpoint_hash: 'revoked', version: 6, active: false, revocable: false, current_session: true },
  ];
  const render = mount(); render(); await effects(); assert.match(text(render()), /订阅已失效或配置已变更/);
  assert.equal(nodes(render()).filter(item => item.type === Button && text(item) === '撤销订阅').length, 1);
  assert.equal(helper.webPushExpectedVersion(resources.get(statePath).data, 'revoked'), 6);
  await click(render, '撤销订阅'); assert.deepEqual(requests[0], { path: statePath + '/subscriptions/9', method: 'DELETE', body: { expected_version: 4 } });
});

test('no configured push instance keeps subscriptions readable without offering opt-in', async () => {
  reset(); resources.get(statePath).data.instances = []; const render = mount(); render(); await effects();
  assert.match(text(render()), /管理员尚未启用/); assert.ok(!nodes(render()).some(item => item.type === Button && text(item) === '开启此浏览器通知'));
});

test('iOS browser explains home-screen installation without invoking an install or permission prompt', async () => {
  reset(); navigator.userAgent = 'Mozilla iPhone'; const render = mount(); render(); await effects();
  assert.match(text(render()), /添加到主屏幕/); assert.match(text(render()), /iOS 16.4/); assert.deepEqual(actions, []);
});


test('session-expired business conflict locks subscription writes and explains sign-in recovery', async () => {
  reset(); respond = async () => { throw new ApiError(409, 'notification_session_expired', '登录会话已失效'); };
  const render = mount(); render(); await effects(); await click(render, '开启此浏览器通知');
  assert.match(text(render()), /重新登录/); assert.equal(button(render(), '开启此浏览器通知').props.disabled, true);
});

test('Web Push templates show fixed read-only privacy copy and only toggle active state', async () => {
  reset(); const centerPath = '/admin/notification-center?limit=50&offset=0';
  resources.set(centerPath, { data: { channels: [{ channel: 'web_push', name: '浏览器通知' }], events: [{ kind: 'order.created', name: '订单已创建', variables: [{ key: 'amount', label: '金额' }] }], instances: [], rules: [], templates: [{ id: 'push-order', event_kind: 'order.created', channel: 'web_push', subject: 'stale private title', body: 'secret amount {{amount}}', version: 3, active: true }], deliveries: [] } });
  const state = { cursor: 0, values: [] };
  const render = () => { current = state; state.cursor = 0; try { return NotificationCenter({}); } finally { current = undefined; } };
  const tab = nodes(render()).find(item => item.type === 'button' && text(item) === '渠道模板'); tab.props.onClick();
  const fields = nodes(render()).filter(item => ['账户通知', '您有新的账户通知，请登录查看。'].includes(item.props?.value));
  assert.equal(fields.length, 2); assert.ok(fields.every(item => item.props.readOnly === true));
  assert.match(text(render()), /不支持业务变量或自定义正文/); assert.doesNotMatch(text(render()), /secret amount|stale private title|支持变量/);
  nodes(render()).find(item => item.type === Checkbox).props.onCheckedChange(false);
  const form = nodes(render()).find(item => item.type === 'form'); form.props.onSubmit({ preventDefault() {} }); await settle();
  assert.deepEqual(requests[0], { path: '/admin/notification-center/templates/push-order', method: 'PATCH', body: { subject: '账户通知', body: '您有新的账户通知，请登录查看。', active: false, expected_version: 3 } });
});


test('push history has independent loading, empty and retry states without blocking subscription controls', async () => {
  reset(); const historyPath = statePath + '/history?limit=20&offset=0';
  const history = mount({}, WebPushHistory); assert.ok(nodes(history()).some(item => item.type === Loading));
  resources.set(historyPath, { data: { items: [], has_more: false, limit: 20, offset: 0 } });
  assert.match(text(history()), /暂无浏览器通知记录/); assert.equal(button(history(), '上一页通知记录').props.disabled, true); assert.equal(button(history(), '下一页通知记录').props.disabled, true);
  resources.set(historyPath, { error: new Error('记录暂时不可用') }); const failure = nodes(history()).find(item => item.type === Failure); assert.ok(failure); failure.props.retry(); assert.equal(refreshes.at(-1), historyPath);
  const preferences = mount(); preferences(); await effects(); assert.equal(button(preferences(), '开启此浏览器通知').props.disabled, false); assert.ok(nodes(preferences()).some(item => item.type === WebPushHistory));
  assert.deepEqual(requests, []);
});

test('push history renders server event names and only nonempty whitelisted values as escaped text', () => {
  reset(); resources.set(statePath + '/history?limit=20&offset=0', { data: { items: [{ id: 7, name: '<订单已创建>', event_kind: 'order.created', created_at: '2026-10-19T08:00:00Z', variables: [
    { key: 'order_no', label: '订单号', value: '<script>alert(1)</script>' }, { key: 'blank', label: '空白字段', value: '  ' }, { key: 'empty', label: '空字段', value: '' },
  ] }], has_more: false, limit: 20, offset: 0 } });
  const history = mount({}, WebPushHistory); const tree = history(); const article = nodes(tree).find(item => item.type === 'article'); const html = renderToStaticMarkup(article);
  assert.match(html, /&lt;订单已创建&gt;/); assert.match(html, /&lt;script&gt;alert\(1\)&lt;\/script&gt;/); assert.doesNotMatch(html, /<script|空白字段|空字段/);
  assert.match(text(tree), /平台接受不代表已显示或已读/); assert.match(text(tree), /结果未知/); assert.deepEqual(requests, []);
});

test('push history paginates independently and allows returning from a failed later page', async () => {
  reset(); const first = statePath + '/history?limit=20&offset=0'; const second = statePath + '/history?limit=20&offset=20';
  resources.set(first, { data: { items: [], has_more: true, limit: 20, offset: 0 } });
  const history = mount({}, WebPushHistory); await click(history, '下一页通知记录'); history(); assert.ok(resourcePaths.includes(second)); assert.ok(nodes(history()).some(item => item.type === Loading));
  resources.set(second, { error: new Error('下一页暂时不可用') }); assert.ok(nodes(history()).some(item => item.type === Failure)); assert.equal(button(history(), '上一页通知记录').props.disabled, false);
  await click(history, '上一页通知记录'); history(); assert.equal(resourcePaths.at(-1), first); assert.match(text(history()), /第 1 页/); assert.deepEqual(requests, []);
});
