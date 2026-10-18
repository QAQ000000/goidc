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
const { renderToStaticMarkup } = require('react-dom/server');
const cache = new Map();
let current;
let resources = new Map();
let permissions = new Set();
let requests = [];
let refreshes = [];
let respond;
class ApiError extends Error { constructor(status, code = 'test', message = '请求失败') { super(message); this.status = status; this.code = code; } }
const react = { ...React, useEffect() {}, useState(initial) {
  const state = current;
  const index = state.cursor++;
  if (!(index in state.values)) state.values[index] = typeof initial === 'function' ? initial() : initial;
  return [state.values[index], next => { state.values[index] = typeof next === 'function' ? next(state.values[index]) : next; }];
} };
function load(filename) {
  if (cache.has(filename)) return cache.get(filename).exports;
  const module = new Module(filename);
  cache.set(filename, module);
  module.filename = filename;
  module.paths = Module._nodeModulePaths(path.dirname(filename));
  const external = createRequire(filename);
  module.require = specifier => {
    if (specifier === 'react') return react;
    if (specifier === './workspace') return { useAdminPermission: permission => permissions.has(permission) };
    if (specifier === '@/hooks/use-admin-form-leave') return { useAdminFormLeave() {} };
    if (specifier === '@/hooks/use-resource') return { useResource: resourcePath => ({ data: resources.get(resourcePath)?.data ?? null, error: resources.get(resourcePath)?.error ?? null, refresh: () => refreshes.push(resourcePath) }) };
    if (specifier === '@/lib/api') return { ApiError, date: value => value, api: async (requestPath, options = {}) => { const request = { path: requestPath, method: options.method ?? 'GET', body: options.body ? JSON.parse(options.body) : undefined }; requests.push(request); return respond(request); } };
    if (specifier.startsWith('.') || specifier.startsWith('@/')) {
      const base = specifier.startsWith('@/') ? path.join(web, 'src', specifier.slice(2)) : path.resolve(path.dirname(filename), specifier);
      const source = [base, `${base}.tsx`, `${base}.ts`].find(candidate => existsSync(candidate));
      if (source && /\.tsx?$/.test(source)) return load(source);
    }
    return external(specifier);
  };
  const compiled = ts.transpileModule(readFileSync(filename, 'utf8'), { reportDiagnostics: true, compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true } });
  const errors = (compiled.diagnostics ?? []).filter(item => item.category === ts.DiagnosticCategory.Error);
  assert.equal(errors.length, 0, errors.map(item => ts.flattenDiagnosticMessageText(item.messageText, '\n')).join('\n'));
  module._compile(compiled.outputText, filename);
  return module.exports;
}
const helpers = load(path.join(web, 'src/lib/notification-center.ts'));
const { NotificationPreferences, NotificationInbox } = load(path.join(web, 'src/components/notification-preferences.tsx'));
const { NotificationCenter, NotificationDeliveryTable } = load(path.join(web, 'src/components/notification-center.tsx'));
const { Checkbox } = load(path.join(web, 'src/components/ui/checkbox.tsx'));
const { Button } = load(path.join(web, 'src/components/ui/button.tsx'));

async function settle() { await new Promise(resolve => setImmediate(resolve)); }
function reset() { resources = new Map(); permissions = new Set(); requests = []; refreshes = []; respond = async () => ({}); globalThis.window = { confirm: () => true }; }
function mount(component, props = {}) {
  const state = { cursor: 0, values: [] };
  return () => { state.cursor = 0; current = state; try { return component(props); } finally { current = undefined; } };
}
function nodes(element) {
  if (!element || typeof element !== 'object') return [];
  if (Array.isArray(element)) return element.flatMap(nodes);
  return [element, ...React.Children.toArray(element.props?.children).flatMap(nodes)];
}
function text(element) { if (typeof element === 'string' || typeof element === 'number') return String(element); if (!element) return ''; if (Array.isArray(element)) return element.map(text).join(''); return React.Children.toArray(element.props?.children).map(text).join(''); }
function button(tree, label) { const item = nodes(tree).find(item => item.type === Button && text(item) === label); assert.ok(item, `missing button ${label}`); return item; }
function checkbox(tree, index = 0) { const item = nodes(tree).filter(item => item.type === Checkbox)[index]; assert.ok(item, `missing checkbox ${index}`); return item; }
const prefPath = '/me/notification-preferences';
const inboxPath = '/me/notification-inbox?limit=20&offset=0';
const centerPath = '/admin/notification-center?limit=50&offset=0';
const preference = { channel: 'telegram', name: 'Telegram Bot', enabled: false, version: 0, requires_binding: true };
const target = { id: 'target-1', channel: 'telegram', instance_id: 'bot-1', label: '机器人用户 · **42', version: 3 };
const instance = { id: 'bot-1', name: '账单机器人', channel: 'telegram', bot_username: 'invoice_bot' };
function setPreferences(overrides = {}) { resources.set(prefPath, { data: { channels: [preference], targets: [], instances: [instance], ...overrides } }); }
const template = { id: 'invoice-paid-telegram', event_kind: 'invoice.paid', channel: 'telegram', subject: '付款成功', body: '账单 {{invoice_no}} 已付款 {{amount}}', active: true, version: 1 };
const rule = { id: 'invoice-paid-telegram', event_kind: 'invoice.paid', channel: 'telegram', enabled: false, version: 2, instance_id: 'bot-1', template_id: template.id };
function setCenter(overrides = {}) { permissions = new Set(['notifications.view', 'notifications.manage']); resources.set(centerPath, { data: { channels: [{ channel: 'telegram', name: 'Telegram Bot', requires_binding: true }], rules: [rule], templates: [template], deliveries: [], instances: [instance], has_more: false, ...overrides } }); }

test('delivery rendering distinguishes accepted, unknown and legacy queues without offering retries', () => {
  const deliveries = ['accepted', 'unknown', 'legacy_queued'].map((status, id) => ({ id, event_kind: 'invoice.paid', channel: 'telegram', user_id: 7, status, code: '<safe>', attempt_count: 1, created_at: '2026-10-18', provider_message_id: 'provider-1' }));
  const html = renderToStaticMarkup(React.createElement(NotificationDeliveryTable, { deliveries, channels: [{ channel: 'telegram', name: 'Telegram Bot' }] }));
  assert.match(html, /渠道已接受/); assert.match(html, /不代表最终送达或已读/);
  assert.match(html, /结果未知/); assert.match(html, /不会自动重试/); assert.match(html, /原队列已入队/);
  assert.match(html, /&lt;safe&gt;/); assert.doesNotMatch(html, /<button|<safe>/);
});

test('binding deep links reject arbitrary destinations and unverified identities cannot enable notifications', () => {
  assert.equal(helpers.canEnableNotification(preference, [], [instance]), false);
  assert.equal(helpers.canEnableNotification(preference, [target], [instance]), true);
  assert.equal(helpers.canEnableNotification(preference, [target], []), false);
  for (const url of ['http://t.me/invoice_bot?start=a', 'https://evil.example/?start=a', 'https://t.me.evil.example/invoice_bot?start=a', 'https://x@t.me/invoice_bot?start=a', 'https://t.me/invoice_bot', 'javascript:alert(1)', 'https://t.me/invoice_bot/other?start=a']) assert.equal(helpers.safeNotificationBindingURL(url), null, url);
  assert.equal(helpers.safeNotificationBindingURL('https://t.me/invoice_bot?start=opaque'), 'https://t.me/invoice_bot?start=opaque');
  reset(); setPreferences(); const render = mount(NotificationPreferences); const tree = render();
  assert.equal(checkbox(tree).props.checked, false); assert.equal(checkbox(tree).props.disabled, true);
  assert.equal(button(tree, '保存Telegram Bot偏好').props.disabled, true);
  assert.match(text(tree), /请先完成可用机器人的私聊绑定/); assert.doesNotMatch(text(tree), /chat_id/);
});

test('a binding on a disabled or incompatible instance stays removable and cannot enable notifications', () => {
  reset(); setPreferences({ targets: [target], instances: [] });
  const render = mount(NotificationPreferences);
  assert.equal(checkbox(render()).props.disabled, true);
  assert.equal(button(render(), '保存Telegram Bot偏好').props.disabled, true);
  assert.equal(button(render(), '解除绑定').props.disabled, false);
  assert.match(text(render()), /通知实例当前不可用/);
});

test('challenge creation uses the selected instance and displays a one-time link without enabling preference', async () => {
  reset(); setPreferences(); respond = async () => ({ url: 'https://t.me/invoice_bot?start=opaque', expires_at: '2026-10-18T03:10:00Z' });
  const render = mount(NotificationPreferences);
  await button(render(), '创建一次性绑定链接').props.onClick(); await settle();
  assert.deepEqual(requests[0], { path: '/me/notification-bindings/challenges', method: 'POST', body: { instance_id: 'bot-1' } });
  assert.ok(nodes(render()).some(item => item.type === 'a' && item.props.href === 'https://t.me/invoice_bot?start=opaque'));
  assert.equal(checkbox(render()).props.checked, false);
  assert.match(text(render()), /10 分钟/); assert.match(text(render()), /群聊不支持/);
});

test('preference opt-in sends version zero only after binding; CAS reload retains the chosen value', async () => {
  reset(); setPreferences({ targets: [target] }); let conflict = true;
  respond = async request => {
    if (request.method === 'GET') return { channels: [{ ...preference, version: 4 }], targets: [target], instances: [instance] };
    if (conflict) throw new ApiError(409, 'version_conflict');
    return {};
  };
  const render = mount(NotificationPreferences);
  checkbox(render()).props.onCheckedChange(true);
  await button(render(), '保存Telegram Bot偏好').props.onClick(); await settle();
  assert.deepEqual(requests[0].body, { channel: 'telegram', enabled: true, expected_version: 0 });
  assert.equal(checkbox(render()).props.checked, true);
  assert.equal(button(render(), '保存Telegram Bot偏好').props.disabled, true);
  await button(render(), '加载最新版本并保留草稿').props.onClick(); await settle();
  assert.equal(checkbox(render()).props.checked, true);
  conflict = false; await button(render(), '保存Telegram Bot偏好').props.onClick(); await settle();
  assert.deepEqual(requests.at(-1).body, { channel: 'telegram', enabled: true, expected_version: 4 });
});

test('permission revocation locks preference writes while retaining the current draft', async () => {
  reset(); setPreferences({ targets: [target] }); respond = async () => { throw new ApiError(403); };
  const render = mount(NotificationPreferences); checkbox(render()).props.onCheckedChange(true);
  await button(render(), '保存Telegram Bot偏好').props.onClick(); await settle();
  assert.equal(checkbox(render()).props.checked, true); assert.equal(checkbox(render()).props.disabled, true);
  assert.equal(button(render(), '保存Telegram Bot偏好').props.disabled, true);
  assert.match(text(render()), /草稿已保留/);
});

test('business conflicts keep the draft and show their actual reason without a version-conflict flow', async () => {
  reset(); setPreferences({ targets: [target] });
  respond = async () => { throw new ApiError(409, 'notification_binding_required', '请先绑定通知目标'); };
  const render = mount(NotificationPreferences); checkbox(render()).props.onCheckedChange(true);
  await button(render(), '保存Telegram Bot偏好').props.onClick(); await settle();
  assert.equal(checkbox(render()).props.checked, true);
  assert.match(text(render()), /请先绑定通知目标/);
  assert.doesNotMatch(text(render()), /已在其他页面更新|加载最新版本并保留草稿/);
  assert.equal(button(render(), '保存Telegram Bot偏好').props.disabled, false);
});

test('unbinding submits the target CAS version and refreshes identity and preferences together', async () => {
  reset(); setPreferences({ targets: [target], channels: [{ ...preference, enabled: true }] });
  const render = mount(NotificationPreferences); await button(render(), '解除绑定').props.onClick(); await settle();
  assert.deepEqual(requests[0], { path: '/me/notification-bindings/target-1', method: 'DELETE', body: { expected_version: 3 } });
  assert.deepEqual(refreshes, [prefPath]); assert.match(text(render()), /绑定已解除/);
});

test('admin permissions gate viewing, editing and webhook registration independently', () => {
  reset(); setCenter(); permissions = new Set(); const denied = mount(NotificationCenter); assert.match(text(denied()), /没有通知查看权限/);
  permissions = new Set(['notifications.view']); const readonly = mount(NotificationCenter);
  assert.equal(nodes(readonly()).find(item => item.type === 'fieldset').props.disabled, true);
  assert.ok(!nodes(readonly()).some(item => item.type === Button && text(item) === '保存规则'));
  const channelTab = nodes(readonly()).find(item => item.type === 'button' && text(item) === '通知渠道'); channelTab.props.onClick();
  assert.ok(!nodes(readonly()).some(item => item.type === Button && text(item) === '配置机器人回调'));
  permissions.add('settings.manage'); assert.ok(button(readonly(), '配置机器人回调'));
});

test('admin rule saves the selected compiled instance and template with CAS', async () => {
  reset(); setCenter(); const render = mount(NotificationCenter);
  checkbox(render()).props.onCheckedChange(true);
  await nodes(render()).find(item => item.type === 'form').props.onSubmit({ preventDefault() {} });
  // Event handlers intentionally fire async commands without returning them.
  await settle();
  assert.deepEqual(requests[0], { path: '/admin/notification-center/rules/invoice-paid-telegram', method: 'PATCH', body: { enabled: true, instance_id: 'bot-1', template_id: template.id, expected_version: 2 } });
  assert.deepEqual(refreshes, [centerPath]);
});

test('admin template CAS conflict preserves text and explicit rebase uses the latest version', async () => {
  reset(); setCenter(); let conflict = true;
  respond = async request => {
    if (request.method === 'GET') return { ...resources.get(centerPath).data, templates: [{ ...template, version: 8, body: '其他管理员内容' }] };
    if (conflict) throw new ApiError(409, 'version_conflict');
    return {};
  };
  const render = mount(NotificationCenter); nodes(render()).find(item => item.type === 'button' && text(item) === '渠道模板').props.onClick(); await settle();
  nodes(render()).find(item => item.props.rows === 5).props.onChange({ target: { value: '我的草稿 {{invoice_no}}' } });
  nodes(render()).find(item => item.type === 'form').props.onSubmit({ preventDefault() {} }); await settle();
  assert.equal(nodes(render()).find(item => item.props.rows === 5).props.value, '我的草稿 {{invoice_no}}');
  assert.equal(button(render(), '保存渠道模板').props.disabled, true);
  await button(render(), '加载最新版本并保留草稿').props.onClick(); await settle();
  assert.equal(nodes(render()).find(item => item.props.rows === 5).props.value, '我的草稿 {{invoice_no}}');
  conflict = false; nodes(render()).find(item => item.type === 'form').props.onSubmit({ preventDefault() {} }); await settle();
  assert.equal(requests.at(-1).body.expected_version, 8); assert.equal(requests.at(-1).body.body, '我的草稿 {{invoice_no}}');
});

test('template editing uses store length limits and validation conflicts do not become false CAS warnings', async () => {
  reset(); setCenter(); respond = async () => { throw new ApiError(409, 'invalid_notification_template', '通知模板含不支持的变量'); };
  const render = mount(NotificationCenter); nodes(render()).find(item => item.type === 'button' && text(item) === '渠道模板').props.onClick(); await settle();
  assert.ok(nodes(render()).some(item => item.props.maxLength === 240));
  assert.ok(nodes(render()).some(item => item.props.maxLength === 4000));
  nodes(render()).find(item => item.props.rows === 5).props.onChange({ target: { value: '{{unsupported}}' } });
  nodes(render()).find(item => item.type === 'form').props.onSubmit({ preventDefault() {} }); await settle();
  assert.match(text(render()), /通知模板含不支持的变量/);
  assert.doesNotMatch(text(render()), /已被其他管理员修改|加载最新版本并保留草稿/);
  assert.equal(button(render(), '保存渠道模板').props.disabled, false);
});

test('personal inbox marks the owned message read and has bounded independent pagination', async () => {
  reset(); resources.set(inboxPath, { data: { items: [{ id: 9, subject: '<私密通知>', body: '<script>unsafe</script>', created_at: '2026-10-18', read_at: null }], has_more: false } });
  const render = mount(NotificationInbox);
  assert.equal(button(render(), '上一页').props.disabled, true); assert.equal(button(render(), '下一页').props.disabled, true);
  await button(render(), '标记已读').props.onClick(); await settle();
  assert.deepEqual(requests[0], { path: '/me/notification-inbox/9/read', method: 'POST', body: {} });
  assert.deepEqual(refreshes, [inboxPath]);
  assert.ok(!nodes(render()).some(item => item.props?.dangerouslySetInnerHTML));
});
