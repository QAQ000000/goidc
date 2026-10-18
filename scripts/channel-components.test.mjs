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
let allowed = false;
let resources = new Map();
function load(filename) {
  if (cache.has(filename)) return cache.get(filename).exports;
  const module = new Module(filename);
  cache.set(filename, module);
  module.filename = filename;
  module.paths = Module._nodeModulePaths(path.dirname(filename));
  const external = createRequire(filename);
  module.require = specifier => {
    if (specifier === './workspace') return { Workspace: ({ children }) => children, useAdminPermission: () => allowed };
    if (specifier === './catalog-admin') return { catalogSelectStyle: 'test-select' };
    if (specifier === '@/hooks/use-resource') return { useResource: resourcePath => ({ data: resources.get(resourcePath), error: null, refresh() {} }) };
    if (specifier.startsWith('.') || specifier.startsWith('@/')) {
      const base = specifier.startsWith('@/') ? path.join(web, 'src', specifier.slice(2)) : path.resolve(path.dirname(filename), specifier);
      const source = [base, `${base}.tsx`, `${base}.ts`].find(candidate => existsSync(candidate));
      if (source && /\.tsx?$/.test(source)) return load(source);
    }
    return external(specifier);
  };
  const compiled = ts.transpileModule(readFileSync(filename, 'utf8'), { reportDiagnostics: true, compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true } });
  const errors = (compiled.diagnostics ?? []).filter(item => item.category === ts.DiagnosticCategory.Error);
  if (errors.length) throw new Error(errors.map(item => ts.flattenDiagnosticMessageText(item.messageText, '\n')).join('\n'));
  module._compile(compiled.outputText, filename);
  return module.exports;
}
const configuration = load(path.join(web, 'src/lib/channel-configuration.ts'));
const { ChannelFieldEditor, ChannelDirectoryRow, ChannelConfigurationIssues } = load(path.join(web, 'src/components/channel-instances.tsx'));
const { AppCard } = load(path.join(web, 'src/components/admin-apps.tsx'));
const { SuppliersView } = load(path.join(web, 'src/components/suppliers-admin.tsx'));
const render = (component, props) => renderToStaticMarkup(React.createElement(component, props));
const field = (key, type, extra = {}) => ({ key, label: key, type, required: false, secret: false, ...extra });
const descriptor = { id: 'example', name: 'Example', category: 'sms', factory_available: true, version: '1.0.0', schema_version: 2, contract_version: 1, fields: [field('region', 'select', { options: ['a', 'b'], default: 'b', required: true }), field('key', 'text', { secret: true, required: true }), field('signature', 'text', { secret: true, required: true })] };
const instance = { id: 'instance', category: 'sms', implementation_id: 'example', display_name: 'SMS', enabled: true, sort_order: 7, config: { region: 'b' }, secret_configured: true, secure_fields: { key: true, signature: true }, schema_version: 2, factory_available: true, configuration_status: 'valid', version: 12 };
const rowProps = { index: 1, category: 'sms', onInstall() {}, onEdit() {}, onClone() {}, onTest() {}, onRefresh() {} };
const button = (html, label) => html.match(new RegExp(`<button[^>]*aria-label="${label}"[^>]*>`))?.[0] ?? '';

test('defaults are submitted as data and never overwrite existing false, zero, empty arrays or secrets', () => {
  const desc = { ...descriptor, fields: [...descriptor.fields, field('enabled', 'boolean', { default: true }), field('limit', 'number', { default: 5 }), field('modes', 'multiselect', { options: ['a'], default: ['a'] }), field('emptyToggle', 'boolean'), field('emptyOptions', 'multiselect'), field('privateDefault', 'text', { secret: true, default: 'must-not-copy' })] };
  const values = configuration.initialChannelConfig(desc, { enabled: false, limit: 0, modes: [] });
  assert.deepEqual(values, { region: 'b', enabled: false, limit: 0, modes: [], emptyToggle: false, emptyOptions: [] });
  const a = configuration.initialChannelConfig(desc);
  a.modes.push('changed');
  assert.deepEqual(desc.fields.find(item => item.key === 'modes').default, ['a']);
});

test('configuration validation rejects unsupported, unknown, secret-in-config and invalid typed data', () => {
  const desc = { ...descriptor, fields: [field('enabled', 'boolean', { required: true }), field('amount', 'number', { min: 1, max: 10 }), field('region', 'select', { options: ['a'] }), field('modes', 'multiselect', { options: ['a', 'b'], required: true }), field('secret', 'text', { secret: true }), field('unhandled', 'date')] };
  const errors = configuration.channelConfigurationErrors(desc, { enabled: 'false', amount: 11, region: 'wrong', modes: ['a', 'a'], secret: 'DO-NOT-EXPOSE', unknown: 'VALUE-NOT-IN-ERROR' }, false, {}, []);
  assert.equal(errors.length, 7);
  assert.match(errors.join(' '), /未知字段|不能放入普通配置|开关值|超出允许范围|有效的region|无效选项|尚不支持/);
  assert.doesNotMatch(errors.join(' '), /DO-NOT-EXPOSE|VALUE-NOT-IN-ERROR/);
  assert.deepEqual(configuration.channelConfigurationErrors({ ...desc, fields: desc.fields.slice(0, 4) }, { enabled: false, amount: 1, region: 'a', modes: ['b'] }, false, {}, []), []);
  assert.match(configuration.channelConfigurationErrors({ ...desc, fields: [field('port', 'number', { integer: true })] }, { port: 2.5 }, false, {}, []).join(' '), /整数/);
});

test('each credential independently retains, replaces and clears while disabled drafts permit missing secrets', () => {
  assert.deepEqual(configuration.channelConfigurationErrors(descriptor, instance.config, true, {}, [], instance), []);
  assert.match(configuration.channelConfigurationErrors(descriptor, instance.config, true, {}, ['signature'], instance).join(' '), /signature/);
  assert.match(configuration.channelConfigurationErrors(descriptor, instance.config, true, { signature: 'replacement' }, ['signature'], instance).join(' '), /不能同时替换和清除/);
  assert.deepEqual(configuration.channelConfigurationErrors(descriptor, instance.config, false, {}, ['key', 'signature'], instance), []);
  const legacy = { ...instance, secure_fields: undefined };
  assert.equal(configuration.channelSecureConfigured(descriptor, legacy, 'key'), false);
  assert.equal(configuration.channelSecureConfigured({ ...descriptor, fields: [descriptor.fields[1]] }, legacy, 'key'), true);
});

test('sanitized legacy configuration errors explain cleanup without requiring hidden values', () => {
  const html = render(ChannelConfigurationIssues, { descriptor, instance: { ...instance, config: {}, validation_errors: [{ field: 'legacy_field', code: 'unknown_field' }, { field: 'key', code: 'secret_in_config' }, { field: 'region', code: 'invalid_option' }] } });
  assert.match(html, /legacy_field/); assert.match(html, /unknown_field/); assert.match(html, /重新保存时移除/);
  assert.match(html, /secret_in_config/); assert.match(html, /需要保留的凭据请在密钥栏补充/);
  assert.match(html, /invalid_option/); assert.match(html, /已加密的凭据留空会保留/);
  assert.doesNotMatch(html, /replacement|new-secret/);
  assert.equal(render(ChannelConfigurationIssues, { descriptor, instance }), '');
});

test('mutation preserves concurrency/schema/sort and isolates replacement and clear operations', () => {
  const body = configuration.channelMutationBody(descriptor, { displayName: 'new', enabled: false, config: instance.config, secureConfig: { key: '', signature: 'new-secret', unknown: 'no' }, clearSecureFields: ['key', 'unknown'] }, instance);
  assert.deepEqual(body, { category: 'sms', implementation_id: 'example', display_name: 'new', enabled: false, sort_order: 7, config: { region: 'b' }, schema_version: 2, secure_config: { signature: 'new-secret' }, clear_secure_fields: ['key'], expected_version: 12, expected_schema_version: 2 });
  assert.deepEqual(configuration.channelDisableBody(instance), { enabled: false, expected_version: 12 });
  assert.match(configuration.channelConfigurationErrors(descriptor, instance.config, false, {}, [], { ...instance, schema_version: 1 }).join(' '), /迁移/);
});

test('directory retains every instance whose implementation disappeared', () => {
  const missing = { ...instance, id: 'old', implementation_id: 'removed', schema_version: 1 };
  const rows = configuration.channelDirectoryRows([descriptor, { ...descriptor, id: 'new' }], [instance, missing]);
  assert.equal(rows.length, 3);
  assert.equal(rows[0].instance.id, 'instance');
  assert.equal(rows[1].descriptor.id, 'new');
  assert.equal(rows[2].descriptor, undefined);
  assert.equal(rows[2].instance.id, 'old');
});

test('actual field components render boolean, textarea, enum, number bounds and explicit unsupported error', () => {
  const boolean = render(ChannelFieldEditor, { field: field('toggle', 'boolean'), value: false, onChange() {} });
  assert.match(boolean, /role="checkbox"/); assert.match(boolean, /aria-checked="false"/);
  assert.match(render(ChannelFieldEditor, { field: field('message', 'textarea'), value: '<safe>', onChange() {} }), /<textarea[^>]*>&lt;safe&gt;<\/textarea>/);
  const select = render(ChannelFieldEditor, { field: field('region', 'select', { options: ['a', 'b'] }), value: undefined, onChange() {} });
  assert.match(select, /<option value="" selected="">请选择/);
  assert.doesNotMatch(select, /<option value="a" selected/);
  assert.match(render(ChannelFieldEditor, { field: descriptor.fields[0], value: 'b', onChange() {} }), /<option value="b" selected="">b/);
  const number = render(ChannelFieldEditor, { field: field('size', 'number', { min: 1, max: 20 }), value: 0, onChange() {} });
  assert.match(number, /min="1"/); assert.match(number, /max="20"/); assert.match(number, /value="0"/);
  assert.match(render(ChannelFieldEditor, { field: field('date', 'date'), onChange() {} }), /role="alert"/);
});

test('multiselect works for any field key and sends independent selected values', () => {
  let value;
  const element = ChannelFieldEditor({ field: field('regions', 'multiselect', { options: ['a', 'b'] }), value: ['a'], onChange: next => { value = next; } });
  const labels = element.props.children[1].props.children;
  labels[1].props.children[0].props.onCheckedChange(true);
  assert.deepEqual(value, ['a', 'b']);
  labels[0].props.children[0].props.onCheckedChange(false);
  assert.deepEqual(value, []);
  const html = render(ChannelFieldEditor, { field: field('regions', 'multiselect', { options: ['a', 'b'] }), value: ['a'], onChange() {} });
  assert.equal((html.match(/role="checkbox"/g) ?? []).length, 2);
});

test('missing implementation and incompatible schemas remain visible with stop/delete available', () => {
  for (const props of [{ instance }, { descriptor, instance: { ...instance, schema_version: 1, configuration_status: 'schema_incompatible' } }]) {
    const html = render(ChannelDirectoryRow, { ...rowProps, ...props });
    assert.match(html, /SMS/);
    assert.match(html, /实现缺失|配置版本不兼容/);
    assert.ok(button(html, '停用接口'));
    assert.doesNotMatch(button(html, '停用接口'), /\sdisabled(?:=|\s|>)/);
    assert.doesNotMatch(button(html, '删除接口配置'), /\sdisabled(?:=|\s|>)/);
    assert.match(button(render(ChannelDirectoryRow, { ...rowProps, ...props, instance: { ...props.instance, enabled: false } }), '启用接口'), /\sdisabled(?:=|\s|>)/);
    assert.doesNotMatch(html, /new-secret/);
  }
});

test('provision descriptors route to real configuration without generic instance creation', () => {
  for (const [mode, href] of [['supplier', '/admin/suppliers'], ['profile', '/admin/products']]) {
    const desc = { ...descriptor, category: 'provision', instance_mode: mode, factory_available: false };
    const html = render(ChannelDirectoryRow, { ...rowProps, category: 'provision', descriptor: desc });
    assert.match(html, new RegExp(`href="${href}"`));
    assert.doesNotMatch(html, /新增配置<\/button>/);
  }
});

test('app cards separate enabled/config verification and permit only same-origin configuration routes', () => {
  const item = { descriptor, factory_available: true, instance_count: 2, enabled_instance_count: 1, secret_configured_count: 2, status: 'enabled', configuration_url: '/admin/channels?category=sms', schema_incompatible_count: 1 };
  const html = render(AppCard, { item, returnHref: '/admin/apps' });
  assert.match(html, /模块 1\.0\.0/); assert.match(html, /配置版本 2/); assert.match(html, /契约版本 1/);
  assert.match(html, /有已启用实例/); assert.match(html, /1 个实例的配置版本不兼容/); assert.match(html, /启用数量不代表配置已验证/);
  for (const url of ['https://evil.invalid/admin/channels', '/admin/users']) assert.doesNotMatch(render(AppCard, { item: { ...item, configuration_url: url }, returnHref: '/admin/apps' }), /管理配置/);
  const external = render(AppCard, { item: { ...item, descriptor: { ...descriptor, category: 'provision', instance_mode: 'profile' }, factory_available: false, configuration_url: '/admin/products' }, returnHref: '/admin/apps' });
  assert.match(external, /href="\/admin\/products\?return_to=/);
  assert.match(external, /独立配置入口/);
  assert.doesNotMatch(external, /尚未提供运行工厂/);
  const missing = render(AppCard, { item: { ...item, factory_available: false, status: 'implementation_missing' }, returnHref: '/admin/apps' });
  assert.match(missing, /管理配置/); assert.match(missing, /保留原配置/);
  const legacy = render(AppCard, { item: { ...item, descriptor: { ...descriptor, category: 'provision', instance_mode: 'supplier' }, unsupported_instance_count: 2, configuration_url: '/admin/suppliers' }, returnHref: '/admin/apps' });
  assert.match(legacy, /href="\/admin\/channels\?category=provision"/); assert.match(legacy, /2 个旧渠道配置/);
});

test('supplier orphan controls respect manage permission, block jobs, and never offer re-enable', () => {
  const account = { id: 9, provider: 'removed', name: 'Legacy supplier', base_url: 'https://supplier.invalid', username: 'account', active: true, credentials_configured: true, version: 4, product_count: 2 };
  resources = new Map([['/admin/supplier-providers', []], ['/admin/suppliers', [account]]]);
  allowed = false;
  let html = render(SuppliersView, {});
  assert.match(html, /实现未编入当前构建/);
  assert.doesNotMatch(html, /aria-label="编辑 Legacy supplier"|>停用<\/button>/);
  allowed = true;
  html = render(SuppliersView, {});
  assert.match(button(html, '编辑 Legacy supplier'), /\sdisabled(?:=|\s|>)/);
  const stop = html.match(/<button[^>]*>停用<\/button>/)?.[0];
  assert.ok(stop); assert.doesNotMatch(stop, /\sdisabled(?:=|\s|>)/);
  assert.match(html.match(/<button[^>]*>[\s\S]*?目录<\/button>/g)?.at(-1) ?? '', /disabled=""/);
  resources.set('/admin/suppliers', [{ ...account, blocked_job_id: 3 }]);
  html = render(SuppliersView, {});
  assert.match(html.match(/<button[^>]*>停用<\/button>/)?.[0] ?? '', /\sdisabled(?:=|\s|>)/);
  assert.match(html, /待核对任务 #3/);
  resources.set('/admin/suppliers', [{ ...account, active: false }]);
  html = render(SuppliersView, {});
  assert.doesNotMatch(html, />停用<\/button>|>启用<\/button>/);
  allowed = false; resources = new Map();
});
