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
let resourcePaths = [];
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
    if (specifier === '@/hooks/use-resource') return { useResource: resourcePath => { resourcePaths.push(resourcePath); return { data: resources.get(resourcePath)?.data ?? null, error: resources.get(resourcePath)?.error ?? null, refresh: () => refreshes.push(resourcePath) }; } };
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
function reset() { resources = new Map(); permissions = new Set(); requests = []; refreshes = []; resourcePaths = []; respond = async () => ({}); globalThis.window = { confirm: () => true }; }
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
const preference = { channel: 'telegram', name: 'Telegram Bot', enabled: false, version: 0, requires_binding: true, event_scope: 'business' };
const target = { id: 'target-1', channel: 'telegram', instance_id: 'bot-1', label: '机器人用户 · **42', version: 3 };
const instance = { id: 'bot-1', name: '账单机器人', channel: 'telegram', bot_username: 'invoice_bot' };
function setPreferences(overrides = {}) { resources.set(prefPath, { data: { channels: [preference], targets: [], instances: [instance], ...overrides } }); }
const template = { id: 'invoice-paid-telegram', event_kind: 'invoice.paid', channel: 'telegram', subject: '付款成功', body: '账单 {{invoice_no}} 已付款 {{amount}}', active: true, version: 1 };
const eventCatalog = [{ kind: 'invoice.paid', name: '账单已结清', description: '来自服务端的付款事件说明', variables: [{ key: 'invoice_no', label: '账单号' }, { key: 'amount', label: '金额' }] }, { kind: 'service.suspended', name: '服务已暂停', description: '来自服务端的暂停事件说明', variables: [{ key: 'service_name', label: '服务名称' }] }];
const rule = { id: 'invoice-paid-telegram', event_kind: 'invoice.paid', channel: 'telegram', enabled: false, version: 2, instance_id: 'bot-1', template_id: template.id };
function setCenter(overrides = {}) { permissions = new Set(['notifications.view', 'notifications.manage']); resources.set(centerPath, { data: { events: eventCatalog, channels: [{ channel: 'telegram', name: 'Telegram Bot', requires_binding: true }], rules: [rule], templates: [template], deliveries: [], instances: [instance], has_more: false, ...overrides } }); }

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
  assert.deepEqual(requests[0].body, { channel: 'telegram', enabled: true, event_scope: 'business', expected_version: 0 });
  assert.equal(checkbox(render()).props.checked, true);
  assert.equal(button(render(), '保存Telegram Bot偏好').props.disabled, true);
  await button(render(), '加载最新版本并保留草稿').props.onClick(); await settle();
  assert.equal(checkbox(render()).props.checked, true);
  conflict = false; await button(render(), '保存Telegram Bot偏好').props.onClick(); await settle();
  assert.deepEqual(requests.at(-1).body, { channel: 'telegram', enabled: true, event_scope: 'business', expected_version: 4 });
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

test('server event catalog owns labels and unknown persisted kinds remain discoverable', () => {
  const unknown = { ...rule, event_kind: 'custom.future-event' };
  const options = helpers.notificationEventOptions({ events: eventCatalog, rules: [rule, unknown], templates: [template], deliveries: [] });
  assert.deepEqual(options, [{ kind: 'invoice.paid', name: '账单已结清' }, { kind: 'service.suspended', name: '服务已暂停' }, { kind: 'custom.future-event', name: 'custom.future-event' }]);
  assert.equal(helpers.notificationEventName('invoice.paid'), 'invoice.paid');
  assert.equal(helpers.notificationEventName('service.suspended', eventCatalog), '服务已暂停');
  assert.deepEqual(helpers.notificationEventOptions({ rules: [unknown], templates: [], deliveries: [] }), [{ kind: 'custom.future-event', name: 'custom.future-event' }]);
});

test('event selection shows only its routes and retains drafts while moving between business events', () => {
  reset();
  const manyEvents = [...eventCatalog, ...Array.from({ length: 9 }, (_, i) => ({ kind: `future.event-${i}`, name: `未来事件 ${i}`, variables: [] }))];
  const rules = manyEvents.flatMap(event => ['telegram', 'inbox'].map(channel => ({ ...rule, id: `${event.kind}-${channel}`, event_kind: event.kind, channel })));
  setCenter({ events: manyEvents, rules });
  const render = mount(NotificationCenter);
  assert.equal(nodes(render()).filter(item => item.type === 'form').length, 2);
  checkbox(render()).props.onCheckedChange(true);
  const select = () => nodes(render()).find(item => item.type === 'select' && item.props['aria-label'] === '筛选通知事件');
  select().props.onChange({ target: { value: 'service.suspended' } });
  assert.equal(nodes(render()).filter(item => item.type === 'form').length, 2);
  assert.match(text(render()), /来自服务端的暂停事件说明/);
  assert.equal(checkbox(render()).props.checked, false);
  assert.match(text(render()), /切换事件会保留未保存的草稿/);
  select().props.onChange({ target: { value: 'invoice.paid' } });
  assert.equal(checkbox(render()).props.checked, true);
  assert.equal(button(render(), '保存规则').props.disabled, false);
});

test('template help uses only the selected event variables and missing catalog does not invent invoice variables', () => {
  reset();
  const serviceTemplate = { ...template, id: 'service-suspended-telegram', event_kind: 'service.suspended', subject: '暂停', body: '{{service_name}}已暂停' };
  setCenter({ templates: [template, serviceTemplate] });
  const render = mount(NotificationCenter);
  nodes(render()).find(item => item.type === 'button' && text(item) === '渠道模板').props.onClick();
  nodes(render()).find(item => item.type === 'select' && item.props['aria-label'] === '筛选通知事件').props.onChange({ target: { value: 'service.suspended' } });
  assert.equal(nodes(render()).filter(item => item.type === 'form').length, 1);
  const help = nodes(render()).find(item => item.type?.name === 'NotificationTemplateHelp');
  const helpText = text(help.type(help.props));
  assert.match(helpText, /\{\{service_name\}\}（服务名称）/);
  assert.doesNotMatch(helpText, /invoice_no|amount/);
  setCenter({ events: undefined, rules: [{ ...rule, event_kind: 'service.suspended' }], templates: [serviceTemplate] });
  const fallback = nodes(render()).find(item => item.type?.name === 'NotificationTemplateHelp');
  assert.match(text(fallback.type(fallback.props)), /变量说明不可用/);
  assert.doesNotMatch(text(fallback.type(fallback.props)), /invoice_no|amount/);
});

test('delivery event filtering requests server pagination, encodes kind and resets the page', () => {
  reset(); setCenter({ has_more: true });
  const render = mount(NotificationCenter);
  nodes(render()).find(item => item.type === 'button' && text(item) === '投递记录').props.onClick();
  button(render(), '下一页').props.onClick(); render();
  assert.equal(resourcePaths.at(-1), '/admin/notification-center?limit=50&offset=50');
  resources.set('/admin/notification-center?limit=50&offset=50', resources.get(centerPath));
  nodes(render()).find(item => item.type === 'select' && item.props['aria-label'] === '筛选通知事件').props.onChange({ target: { value: 'custom/event ?' } }); render();
  assert.equal(resourcePaths.at(-1), '/admin/notification-center?limit=50&offset=0&event_kind=custom%2Fevent%20%3F');
  resources.set(resourcePaths.at(-1), { data: { ...resources.get(centerPath).data, has_more: false } });
  assert.equal(button(render(), '上一页').props.disabled, true);
  assert.equal(button(render(), '下一页').props.disabled, true);
});

test('delivery rows use server names and safely display unknown events', () => {
  const deliveries = ['service.suspended', 'custom.future-event'].map((event_kind, id) => ({ id, event_kind, channel: 'telegram', user_id: 7, status: 'pending', created_at: '2026-10-18' }));
  const html = renderToStaticMarkup(React.createElement(NotificationDeliveryTable, { deliveries, channels: [], events: eventCatalog }));
  assert.match(html, /服务已暂停/); assert.match(html, /custom.future-event/);
});

test('legacy subscriptions keep invoice scope on normal saves and require explicit business expansion', async () => {
  reset(); setPreferences({ targets: [target], channels: [{ ...preference, enabled: true, event_scope: 'invoice.paid', version: 3 }] });
  const render = mount(NotificationPreferences);
  assert.match(text(render()), /仅接收付款成功通知/);
  assert.match(text(render()), /扩展后不补发历史通知/);
  checkbox(render()).props.onCheckedChange(false);
  await button(render(), '保存Telegram Bot偏好').props.onClick(); await settle();
  assert.deepEqual(requests.at(-1).body, { channel: 'telegram', enabled: false, event_scope: 'invoice.paid', expected_version: 3 });
  await button(render(), '扩展为业务通知').props.onClick(); await settle();
  assert.deepEqual(requests.at(-1).body, { channel: 'telegram', enabled: true, event_scope: 'business', expected_version: 3 });
  assert.match(text(render()), /工单订阅和验证码单独管理/);
});

test('business expansion conflict preserves scope and opt-in on explicit CAS rebase', async () => {
  reset(); setPreferences({ targets: [target], channels: [{ ...preference, enabled: true, event_scope: 'invoice.paid', version: 3 }] });
  let conflict = true;
  respond = async request => {
    if (request.method === 'GET') return { channels: [{ ...preference, enabled: false, event_scope: 'invoice.paid', version: 9 }], targets: [target], instances: [instance] };
    if (conflict) throw new ApiError(409, 'version_conflict');
    return {};
  };
  const render = mount(NotificationPreferences);
  await button(render(), '扩展为业务通知').props.onClick(); await settle();
  assert.match(text(render()), /通过Telegram Bot接收业务通知/);
  assert.equal(button(render(), '保存Telegram Bot偏好').props.disabled, true);
  await button(render(), '加载最新版本并保留草稿').props.onClick(); await settle();
  conflict = false;
  await button(render(), '保存Telegram Bot偏好').props.onClick(); await settle();
  assert.deepEqual(requests.at(-1).body, { channel: 'telegram', enabled: true, event_scope: 'business', expected_version: 9 });
});

test('missing preference scope remains conservative even when toggled back on', async () => {
  reset(); setPreferences({ targets: [target], channels: [{ ...preference, event_scope: undefined }] });
  const render = mount(NotificationPreferences);
  assert.match(text(render()), /仅接收付款成功通知/);
  checkbox(render()).props.onCheckedChange(true);
  await button(render(), '保存Telegram Bot偏好').props.onClick(); await settle();
  assert.deepEqual(requests.at(-1).body, { channel: 'telegram', enabled: true, event_scope: 'invoice.paid', expected_version: 0 });
});

test('a failed filtered delivery request still allows clearing the event filter', () => {
  reset(); setCenter(); const render = mount(NotificationCenter);
  nodes(render()).find(item => item.type === 'button' && text(item) === '投递记录').props.onClick();
  nodes(render()).find(item => item.type === 'select' && item.props['aria-label'] === '筛选通知事件').props.onChange({ target: { value: 'service.suspended' } });
  resources.set('/admin/notification-center?limit=50&offset=0&event_kind=service.suspended', { error: new ApiError(500) });
  const filter = nodes(render()).find(item => item.type === 'select' && item.props['aria-label'] === '筛选通知事件');
  assert.ok(filter); assert.match(text(filter), /全部事件/);
  filter.props.onChange({ target: { value: '' } }); render();
  assert.equal(resourcePaths.at(-1), centerPath);
});
