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
let resources;
let permissions;
let requests;
let refreshes;
let respond;
class ApiError extends Error { constructor(status, code, message) { super(message); this.status = status; this.code = code; } }
const react = {
  ...React, useEffect() {},
  useState(initial) {
    const state = current;
    const index = state.cursor++;
    if (!(index in state.values)) state.values[index] = typeof initial === 'function' ? initial() : initial;
    return [state.values[index], next => { state.values[index] = typeof next === 'function' ? next(state.values[index]) : next; }];
  },
  useRef(initial) { const state = current; const index = state.cursor++; if (!(index in state.values)) state.values[index] = { current: initial }; return state.values[index]; },
};
function load(filename) {
  if (cache.has(filename)) return cache.get(filename).exports;
  const module = new Module(filename);
  cache.set(filename, module);
  module.filename = filename;
  module.paths = Module._nodeModulePaths(path.dirname(filename));
  const external = createRequire(filename);
  module.require = specifier => {
    if (specifier === 'react') return react;
    if (specifier === './workspace') return { Workspace: ({ children }) => children, useAdminPermission: permission => permissions.has(permission) };
    if (specifier === '@/hooks/use-resource') return { useResource: resourcePath => ({ data: resources.get(resourcePath)?.data ?? null, error: resources.get(resourcePath)?.error ?? null, refresh: () => refreshes.push(resourcePath) }) };
    if (specifier === '@/lib/api') return { ApiError, date: value => value, api: async (url, options = {}) => { const request = { url, method: options.method ?? 'GET', body: options.body ? JSON.parse(options.body) : undefined, headers: options.headers }; requests.push(request); return respond(request); } };
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
const { CertificationSettingsView } = load(path.join(web, 'src/components/certification-settings.tsx'));
const { CertificationView } = load(path.join(web, 'src/components/certification.tsx'));
const { Checkbox } = load(path.join(web, 'src/components/ui/checkbox.tsx'));
const { Input } = load(path.join(web, 'src/components/ui/input.tsx'));
const { Button } = load(path.join(web, 'src/components/ui/button.tsx'));
const settingsPath = '/admin/certification-settings';
const customerPath = '/account/certification';
const manual = { id: 'manual', name: '人工认证', category: 'certification', fields: [], schema_version: 1 };
const remote = { id: 'external_test', name: '外部认证', category: 'certification', capabilities: ['submit'], fields: [{ key: 'token', label: 'Token', type: 'text', required: true, secret: true }], schema_version: 2 };
const manualInstance = { id: 'manual-default', category: 'certification', implementation_id: 'manual', display_name: '人工默认实例', enabled: true, config: {}, schema_version: 1, factory_available: true, configuration_status: 'valid', secret_configured: false, version: 1 };
const remoteInstance = { ...manualInstance, id: 'external-second', implementation_id: 'external_test', display_name: '外部第二实例', schema_version: 2, secure_fields: { token: true }, secret_configured: true };
const settings = { enabled: true, require_for_purchase: false, default_provider_id: 'manual', default_instance_id: 'manual-default', advanced_enabled: false, auto_update_name: true, require_phone_match: false, notify_on_approval: false, suspend_unverified: false, allow_file_upload: false, version: 8, providers: [manual, remote], instances: [manualInstance, remoteInstance] };
function reset(data = settings) { resources = new Map([[settingsPath, { data }], [customerPath, { data: { certification_type: 'individual', status: 'unverified', provider_id: 'manual', version: 1, enabled: true } }]]); permissions = new Set(['certifications.manage']); requests = []; refreshes = []; respond = async () => ({}); }
function mount(component, props = {}) { const state = { cursor: 0, values: [] }; return () => { state.cursor = 0; current = state; try { return component(props); } finally { current = undefined; } }; }
function nodes(element) { if (!element || typeof element !== 'object') return []; if (Array.isArray(element)) return element.flatMap(nodes); return [element, ...React.Children.toArray(element.props?.children).flatMap(nodes)]; }
function text(element) { if (typeof element === 'string' || typeof element === 'number') return String(element); if (!element) return ''; if (Array.isArray(element)) return element.map(text).join(''); return React.Children.toArray(element.props?.children).map(text).join(''); }
function find(tree, predicate) { const result = nodes(tree).find(predicate); assert.ok(result, 'expected component/control'); return result; }
function basic(data = settings) { reset(data); const tree = mount(CertificationSettingsView)(); const element = find(tree, item => item.type.name === 'BasicSettings'); return mount(element.type, element.props); }
const select = tree => find(tree, item => item.type === 'select');
const form = tree => find(tree, item => item.type === 'form');
const checkbox = (tree, index = 0) => nodes(tree).filter(item => item.type === Checkbox)[index];
const button = (tree, label) => find(tree, item => item.type === Button && text(item).includes(label));
const event = { preventDefault() {} };

test('settings select a specific instance and submit its implementation, version and unchanged unsupported flags', async () => {
  const render = basic();
  select(render()).props.onChange({ target: { value: 'external-second' } });
  assert.equal(select(render()).props.value, 'external-second');
  await form(render()).props.onSubmit(event);
  assert.equal(requests.length, 1);
  assert.equal(requests[0].url, settingsPath);
  assert.equal(requests[0].method, 'PUT');
  assert.equal(requests[0].body.default_instance_id, 'external-second');
  assert.equal(requests[0].body.default_provider_id, 'external_test');
  assert.equal(requests[0].body.expected_version, 8);
  for (const key of ['advanced_enabled', 'notify_on_approval', 'suspend_unverified', 'allow_file_upload']) assert.equal(requests[0].body[key], false);
  assert.ok(requests[0].headers['Idempotency-Key']);
  assert.deepEqual(refreshes, [settingsPath]);
  for (const index of [1, 4, 6, 7]) assert.equal(checkbox(render(), index).props.disabled, true);
});

test('missing, disabled, incompatible and credential-incomplete current instances stay visible without fallback', async () => {
  const cases = [
    [{ ...settings, default_instance_id: 'missing-current' }, /当前实例不可用|不存在/],
    [{ ...settings, instances: [{ ...manualInstance, enabled: false }, remoteInstance] }, /已停用/],
    [{ ...settings, instances: [{ ...manualInstance, schema_version: 99 }, remoteInstance] }, /配置版本不兼容/],
    [{ ...settings, instances: [{ ...manualInstance, factory_available: false }, remoteInstance] }, /运行工厂/],
    [{ ...settings, providers: [remote] }, /缺少此认证实现/],
    [{ ...settings, default_provider_id: 'external_test', default_instance_id: 'external-second', providers: [manual, { ...remote, capabilities: [] }] }, /不支持提交申请/],
    [{ ...settings, default_provider_id: 'external_test', default_instance_id: 'external-second', instances: [manualInstance, { ...remoteInstance, secure_fields: { token: false }, secret_configured: false }] }, /凭据不完整/],
    [{ ...settings, default_provider_id: 'external_test', default_instance_id: 'manual-default' }, /实现与所选实例不一致/],
  ];
  for (const [data, issue] of cases) {
    const render = basic(data);
    assert.equal(select(render()).props.value, data.default_instance_id);
    assert.match(text(render()), issue);
    checkbox(render()).props.onCheckedChange(false);
    assert.equal(button(render(), '保存更改').props.disabled, true);
    await form(render()).props.onSubmit(event);
    assert.equal(requests.length, 0);
    // Recovery is always an explicit choice, never the first available row.
    if (data.providers.some(provider => provider.id === 'external_test') && data.instances.some(instance => instance.id === 'external-second' && instance.secure_fields?.token)) {
      select(render()).props.onChange({ target: { value: 'external-second' } });
      assert.equal(select(render()).props.value, 'external-second');
    }
  }
});

test('legacy manual mapping is explicit while legacy external settings cannot pick an arbitrary instance', () => {
  assert.equal(select(basic({ ...settings, default_instance_id: undefined })()).props.value, 'manual-default');
  const render = basic({ ...settings, default_instance_id: undefined, default_provider_id: 'external_test' });
  assert.equal(select(render()).props.value, '');
  assert.match(text(render()), /不存在|明确选择/);
});

test('interface directory reports readiness and distinguishes a manual instance from missing credentials', () => {
  reset({ ...settings, instances: [manualInstance, { ...remoteInstance, secure_fields: { token: false }, secret_configured: false }] });
  const render = mount(CertificationSettingsView);
  find(render(), item => item.type === 'button' && text(item) === '接口设置').props.onClick();
  const element = find(render(), item => item.type.name === 'InterfaceSettings');
  const tree = mount(element.type, element.props)();
  const manualRow = find(tree, item => item.type === 'tr' && text(item).includes('manual-default'));
  assert.match(text(manualRow), /可用于新申请/);
  assert.match(text(manualRow), /无需密钥/);
  const remoteRow = find(tree, item => item.type === 'tr' && text(item).includes('external-second'));
  assert.match(text(remoteRow), /不可用于新申请/);
  assert.match(text(remoteRow), /凭据不完整/);
  assert.match(text(remoteRow), /未配置/);
});

test('settings preserve drafts and show actual instance errors instead of calling all conflicts a version mismatch', async () => {
  const render = basic();
  select(render()).props.onChange({ target: { value: 'external-second' } });
  respond = async () => { throw new ApiError(409, 'channel_unavailable', '所选实例已停用'); };
  await form(render()).props.onSubmit(event);
  assert.match(text(render()), /所选实例已停用/);
  assert.doesNotMatch(text(render()), /其他管理员更新/);
  assert.equal(select(render()).props.value, 'external-second');
  respond = async () => { throw new ApiError(409, 'version_conflict', 'stale'); };
  await form(render()).props.onSubmit(event);
  assert.match(text(render()), /其他管理员更新/);
  assert.equal(select(render()).props.value, 'external-second');
  assert.deepEqual(refreshes, []);
});

test('permission revocation blocks saving a previously edited settings form', async () => {
  const render = basic();
  select(render()).props.onChange({ target: { value: 'external-second' } });
  permissions.clear();
  assert.equal(find(render(), item => item.type === 'fieldset').props.disabled, true);
  await form(render()).props.onSubmit(event);
  assert.equal(requests.length, 0);
});

test('customer submission retries reuse a stable key and payload, while a successful or edited form gets a new key', async () => {
  reset();
  const render = mount(CertificationView);
  const tree = render();
  nodes(tree).filter(item => item.type === Input)[0].props.onChange({ target: { value: '示例客户' } });
  nodes(render()).filter(item => item.type === Input)[1].props.onChange({ target: { value: 'example-id' } });
  respond = async () => { throw new Error('network unavailable'); };
  await form(render()).props.onSubmit(event);
  await form(render()).props.onSubmit(event);
  assert.equal(requests[0].headers['Idempotency-Key'], requests[1].headers['Idempotency-Key']);
  assert.deepEqual(requests[0].body, requests[1].body);
  nodes(render()).filter(item => item.type === Input)[0].props.onChange({ target: { value: '未知失败后修改的客户' } });
  await form(render()).props.onSubmit(event);
  assert.notEqual(requests[1].headers['Idempotency-Key'], requests[2].headers['Idempotency-Key']);
  assert.equal(requests[2].body.payload.name, '未知失败后修改的客户');
  nodes(render()).filter(item => item.type === Input)[0].props.onChange({ target: { value: '示例客户' } });
  await form(render()).props.onSubmit(event);
  assert.notEqual(requests[0].headers['Idempotency-Key'], requests[3].headers['Idempotency-Key']);
  assert.deepEqual(requests[0].body, requests[3].body);
  respond = async () => ({});
  await form(render()).props.onSubmit(event);
  assert.equal(requests[3].headers['Idempotency-Key'], requests[4].headers['Idempotency-Key']);
  assert.match(text(render()), /正在处理/);
  await form(render()).props.onSubmit(event);
  assert.notEqual(requests[4].headers['Idempotency-Key'], requests[5].headers['Idempotency-Key']);
  respond = async () => { throw new Error('network unavailable'); };
  nodes(render()).filter(item => item.type === Input)[0].props.onChange({ target: { value: '修改后的客户' } });
  await form(render()).props.onSubmit(event);
  assert.notEqual(requests[5].headers['Idempotency-Key'], requests[6].headers['Idempotency-Key']);
  assert.equal(requests[6].body.payload.name, '修改后的客户');
  assert.deepEqual(refreshes, [customerPath, customerPath]);
});

test('changing identity type creates a different request and synchronous double submission stays locked', async () => {
  reset();
  const render = mount(CertificationView);
  let finish;
  respond = () => new Promise(resolve => { finish = resolve; });
  const initialForm = form(render());
  const pending = initialForm.props.onSubmit(event);
  initialForm.props.onSubmit(event);
  assert.equal(requests.length, 1);
  finish({}); await pending;
  const previous = requests[0].headers['Idempotency-Key'];
  find(render(), item => item.type === 'button' && text(item) === '企业认证').props.onClick();
  respond = async () => ({});
  await form(render()).props.onSubmit(event);
  assert.equal(requests[1].body.type, 'company');
  assert.notEqual(requests[1].headers['Idempotency-Key'], previous);
});

test('customer processing messages keep technical uncertainty separate from a real rejection', () => {
  for (const [processing_state, provider_id, expected] of [
    ['queued', 'external_test', /等待认证渠道处理/],
    ['processing', 'external_test', /认证渠道正在处理/],
    ['needs_review', 'external_test', /进一步核验.*不表示认证被驳回/],
    ['needs_review', 'manual', /等待人工审核/],
  ]) {
    reset(); resources.set(customerPath, { data: { certification_type: 'individual', status: 'pending', provider_id, processing_state, version: 2 } });
    const tree = mount(CertificationView)();
    assert.match(text(tree), expected);
    assert.doesNotMatch(text(tree), /提交个人或企业资料后由管理员审核|驳回原因/);
  }
  reset(); resources.set(customerPath, { data: { certification_type: 'individual', status: 'rejected', provider_id: 'external_test', processing_state: 'finished', rejection_reason: '<证件已过期>', version: 3 } });
  const html = renderToStaticMarkup(mount(CertificationView)());
  assert.match(html, /驳回原因：&lt;证件已过期&gt;/);
  assert.doesNotMatch(html, /进一步核验/);
});
