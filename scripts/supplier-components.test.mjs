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
let permissions;
let resources;
let requests;
let refreshes;
let navigations;
let respond;
let timers;
let pending;
const react = {
  ...React,
  useState(initial) {
    const state = current;
    const index = state.cursor++;
    if (!(index in state.values)) state.values[index] = typeof initial === 'function' ? initial() : initial;
    return [state.values[index], next => { state.values[index] = typeof next === 'function' ? next(state.values[index]) : next; }];
  },
  useRef(initial) {
    const state = current;
    const index = state.cursor++;
    if (!(index in state.values)) state.values[index] = { current: initial };
    return state.values[index];
  },
  useEffect(effect, dependencies) {
    const state = current;
    const index = state.cursor++;
    const previous = state.values[index];
    if (!previous || dependencies?.some((value, i) => value !== previous.dependencies?.[i])) state.effects.push(() => {
      previous?.cleanup?.();
      state.values[index] = { dependencies, cleanup: effect() };
    });
  },
};
class ApiError extends Error { constructor(status, code, message) { super(message); this.status = status; this.code = code; } }
function load(filename) {
  if (cache.has(filename)) return cache.get(filename).exports;
  const module = new Module(filename);
  cache.set(filename, module);
  module.filename = filename;
  module.paths = Module._nodeModulePaths(path.dirname(filename));
  const external = createRequire(filename);
  module.require = specifier => {
    if (specifier === 'react') return react;
    if (specifier === 'next/navigation') return { useRouter: () => ({ push: value => navigations.push(value) }) };
    if (specifier === './workspace') return { Workspace: ({ children }) => children, useAdminPermission: permission => permissions.has(permission), useAdminIdentity: () => ({ id: 3 }) };
    if (specifier === './catalog-admin') return { catalogSelectStyle: 'test-select' };
    if (specifier === '@/hooks/use-pending-setting') return { usePendingSetting: () => pending };
    if (specifier === '@/hooks/use-resource') return { useResource: url => ({ data: resources.get(url)?.data ?? null, error: resources.get(url)?.error ?? null, refresh: () => refreshes.push(url) }) };
    if (specifier === '@/lib/api') return {
      ApiError, date: value => value, money: value => `CNY ${(value / 100).toFixed(2)}`,
      api: async (url, options = {}) => {
        const request = { url, method: options.method ?? 'GET', body: options.body ? JSON.parse(options.body) : undefined, headers: options.headers };
        requests.push(request);
        return respond(request);
      },
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
const { SupplierAccountEditor, SuppliersView, ImportProduct } = load(path.join(web, 'src/components/suppliers-admin.tsx'));
const { SupplierCatalogView } = load(path.join(web, 'src/components/supplier-catalog.tsx'));
const { SupplierAPISettingsView } = load(path.join(web, 'src/components/supplier-api-settings.tsx'));
const { SupplierServiceDelivery, SupplierLifecycle } = load(path.join(web, 'src/components/supplier-delivery.tsx'));
const { ChannelFieldEditor } = load(path.join(web, 'src/components/channel-instances.tsx'));
const { Button } = load(path.join(web, 'src/components/ui/button.tsx'));
const { Input } = load(path.join(web, 'src/components/ui/input.tsx'));
const { Textarea } = load(path.join(web, 'src/components/ui/textarea.tsx'));
const { Checkbox } = load(path.join(web, 'src/components/ui/checkbox.tsx'));
const field = (key, type, extra = {}) => ({ key, label: key, type, required: false, secret: false, ...extra });
const capabilities = { catalog: true, product: true, create: true, sync: false, power: false, reset_password: false, rescue: false, vnc: false, reinstall: false, suspend: false, resume: false, terminate: false, renew: false };
const descriptor = { id: 'custom_supplier', name: '自定义供应商', category: 'provision', instance_mode: 'supplier', schema_version: 3, factory_available: true, fields: [field('endpoint', 'text', { required: true }), field('port', 'number', { min: 1, max: 65535, integer: true, default: 443 }), field('region', 'select', { options: ['east', 'west'], default: 'west' }), field('sandbox', 'boolean'), field('zones', 'multiselect', { options: ['a', 'b'] }), field('notes', 'textarea'), field('token', 'text', { secret: true, required: true }), field('certificate', 'textarea', { secret: true, required: true })] };
const provider = { id: 'custom_supplier_v2', name: '自定义供应商', capabilities, descriptor };
const config = { endpoint: 'https://supplier.invalid', port: 443, region: 'west', sandbox: false, zones: [], notes: '' };
const account = { id: 9, name: '上游 A', provider: provider.id, base_url: '', username: '', active: true, version: 6, credentials_configured: true, product_count: 2, schema_version: 3, factory_available: true, config, secure_fields: { token: true, certificate: true }, configuration_status: 'valid' };
const quantityProduct = { id: 11, name: '固定配置云主机', description: '', revision: 'revision-1', supported: true, stock: 10, prices: { monthly: { amount_cents: 1000, setup_cents: 100 } }, options: [{ id: 7, name: '数据盘', type: 'quantity', required: true, choices: [{ id: 999, name: 'GB', min_qty: 0, max_qty: 10, prices: { monthly: { amount_cents: 50, setup_cents: 5 } } }] }], fields: [] };
const event = { preventDefault() {} };
function reset() {
  permissions = new Set(['products.manage']); requests = []; refreshes = []; navigations = []; timers = [];
  respond = async () => ({ id: 12 });
  resources = new Map([['/admin/supplier-providers', { data: [provider] }], ['/admin/suppliers', { data: [account] }], ['/admin/catalog-groups/category', { data: [] }]]);
  pending = { ready: true, unknown: false, pending: { current: null }, remember: value => { pending.pending.current = value; }, resolve: () => { pending.pending.current = null; pending.unknown = false; }, setUnknown: value => { pending.unknown = value; } };
  globalThis.window = { setTimeout: callback => { timers.push(callback); return callback; }, clearTimeout: callback => { timers = timers.filter(value => value !== callback); }, confirm: () => true, addEventListener() {}, removeEventListener() {} };
  globalThis.sessionStorage = { getItem: () => null, setItem() {}, removeItem() {} };
  globalThis.document = { addEventListener() {}, removeEventListener() {} };
}
function mount(component, props = {}) {
  const state = { cursor: 0, values: [], effects: [] };
  return () => {
    state.cursor = 0; current = state;
    let tree;
    try { tree = component(props); } finally { current = undefined; }
    for (const effect of state.effects.splice(0)) effect();
    return tree;
  };
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
const button = (tree, label) => find(tree, item => item.type === Button && text(item).includes(label));
const input = (tree, label) => find(tree, item => (item.type === Input || item.type === Textarea) && item.props['aria-label'] === label);
const check = (tree, label) => find(tree, item => item.type === Checkbox && item.props['aria-label'] === label);
const form = tree => find(tree, item => item.type === 'form');
const normalField = (tree, key) => find(tree, item => item.type === ChannelFieldEditor && item.props.field.key === key);
function manager(component) {
  const tree = mount(component)();
  const child = find(tree, item => ['SuppliersManager', 'Catalog', 'SettingsContent'].includes(item.type?.name));
  return mount(child.type, child.props);
}
function flushTimers() { for (const callback of timers.splice(0)) callback(); }
function editor(props = {}) { return mount(SupplierAccountEditor, { providers: [provider], onDone: () => refreshes.push('saved'), onCancel() {}, ...props }); }

test('new supplier forms use typed module fields and submit each named credential separately', async () => {
  reset();
  const render = editor();
  input(render(), '供应商名称').props.onChange({ target: { value: '  上游 B  ' } });
  normalField(render(), 'endpoint').props.onChange('https://second.invalid');
  normalField(render(), 'notes').props.onChange('<可见配置>');
  normalField(render(), 'sandbox').props.onChange(false);
  normalField(render(), 'zones').props.onChange(['b']);
  await form(render()).props.onSubmit(event);
  assert.equal(requests.length, 0);
  assert.match(text(render()), /token|certificate/);
  input(render(), 'token').props.onChange({ target: { value: 'test-token' } });
  input(render(), 'certificate').props.onChange({ target: { value: 'test-certificate' } });
  await form(render()).props.onSubmit(event);
  assert.equal(requests.length, 1);
  assert.equal(requests[0].url, '/admin/suppliers');
  assert.equal(requests[0].method, 'POST');
  assert.deepEqual(requests[0].body, { name: '上游 B', provider: provider.id, active: true, config: { endpoint: 'https://second.invalid', port: 443, region: 'west', sandbox: false, zones: ['b'], notes: '<可见配置>' }, schema_version: 3, secure_config: { token: 'test-token', certificate: 'test-certificate' }, clear_secure_fields: [] });
  assert.deepEqual(refreshes, ['saved']);
});

test('existing supplier identity stays locked while each secret independently retains or rotates', async () => {
  reset();
  const render = editor({ account });
  for (const key of Object.keys(config)) {
    const control = normalField(render(), key);
    assert.equal(control.props.disabled, true);
    control.props.onChange('must-not-retarget');
  }
  assert.equal(input(render(), 'token').props.value, '');
  assert.equal(input(render(), 'certificate').props.value, '');
  assert.equal(input(render(), 'certificate').props.placeholder, '已配置，留空保留');
  input(render(), 'token').props.onChange({ target: { value: 'rotated-test-token' } });
  await form(render()).props.onSubmit(event);
  assert.deepEqual(requests[0].body.config, config);
  assert.deepEqual(requests[0].body.secure_config, { token: 'rotated-test-token' });
  assert.equal(requests[0].body.expected_version, 6);
  assert.equal(requests[0].body.expected_schema_version, 3);
  assert.equal(requests[0].body.schema_version, 3);
  assert.equal(requests[0].url, '/admin/suppliers/9');
  assert.equal(requests[0].method, 'PUT');
  assert.equal(input(render(), 'token').props.value, '');
});

test('clearing a required secret disables the account and cannot silently preserve an enabled invalid account', async () => {
  reset();
  const render = editor({ account });
  check(render(), '清除token').props.onCheckedChange(true);
  assert.equal(check(render(), '启用供应商').props.checked, false);
  assert.equal(input(render(), 'token').props.disabled, true);
  check(render(), '启用供应商').props.onCheckedChange(true);
  await form(render()).props.onSubmit(event);
  assert.deepEqual(requests, []);
  assert.match(text(render()), /启用接口前请配置token/);
  check(render(), '启用供应商').props.onCheckedChange(false);
  await form(render()).props.onSubmit(event);
  assert.equal(requests[0].body.active, false);
  assert.deepEqual(requests[0].body.clear_secure_fields, ['token']);
  assert.deepEqual(requests[0].body.secure_config, {});
});

test('choosing a different provider resets its field defaults and drops previous credential drafts', async () => {
  reset();
  const other = { id: 'other_supplier', name: '另一个供应商', capabilities, descriptor: { ...descriptor, id: 'other', schema_version: 1, fields: [field('region', 'select', { options: ['north'], default: 'north' })] } };
  const render = editor({ providers: [provider, other] });
  input(render(), 'token').props.onChange({ target: { value: 'must-not-follow-selection' } });
  find(render(), item => item.type === 'select' && item.props['aria-label'] === '供应商类型').props.onChange({ target: { value: other.id } });
  await form(render()).props.onSubmit(event);
  assert.deepEqual(requests[0].body.config, { region: 'north' });
  assert.deepEqual(requests[0].body.secure_config, {});
  assert.equal(requests[0].body.provider, other.id);
});

test('unsupported field types, missing implementations, incompatible schemas and missing factories cannot submit', async () => {
  for (const props of [
    { providers: [{ ...provider, descriptor: { ...descriptor, fields: [field('upload', 'file')] } }] },
    { providers: [{ ...provider, descriptor: { ...descriptor, fields: [field('upload', 'file', { secret: true })] } }] },
    { providers: [] , account },
    { providers: [{ ...provider, descriptor: undefined }], account },
    { account: { ...account, schema_version: 99 } },
    { account: { ...account, configuration_status: 'implementation_missing' } },
    { account: { ...account, factory_available: false } },
    { account: { ...account, configuration_status: 'invalid', configuration_errors: [{ field: 'endpoint', code: 'required' }] } },
  ]) {
    reset();
    const render = editor(props);
    assert.equal(button(render(), '保存更改').props.disabled, true);
    await form(render()).props.onSubmit(event);
    assert.deepEqual(requests, []);
  }
});

test('missing named credentials can be repaired without changing the fixed account configuration', async () => {
  reset();
  const render = editor({ account: { ...account, configuration_status: 'invalid', secure_fields: { token: false, certificate: true }, configuration_errors: [{ field: 'token', code: 'required' }] } });
  assert.equal(button(render(), '保存更改').props.disabled, false);
  assert.match(renderToStaticMarkup(render()), /原配置需要处理/);
  input(render(), 'token').props.onChange({ target: { value: 'repaired-test-token' } });
  await form(render()).props.onSubmit(event);
  assert.deepEqual(requests[0].body.config, config);
  assert.deepEqual(requests[0].body.secure_config, { token: 'repaired-test-token' });
});

test('revocation and synchronous double submission cannot send unauthorized or duplicate account writes', async () => {
  reset();
  const render = editor({ account });
  permissions.clear();
  await form(render()).props.onSubmit(event);
  assert.deepEqual(requests, []);
  permissions.add('products.manage');
  let finish;
  respond = () => new Promise(resolve => { finish = resolve; });
  const submit = form(render()).props.onSubmit;
  const inFlight = submit(event);
  await submit(event);
  assert.equal(requests.length, 1);
  assert.equal(button(render(), '保存中').props.disabled, true);
  finish({ id: account.id });
  await inFlight;
});

test('orphan and incompatible suppliers retain a narrow disable-only action without retargeting or re-enabling', async () => {
  for (const changed of [{ provider: 'removed' }, { schema_version: 99 }]) {
    reset();
    resources.set('/admin/suppliers', { data: [{ ...account, ...changed }] });
    const render = manager(SuppliersView);
    assert.equal(find(render(), item => item.type === Button && item.props['aria-label'] === '编辑 上游 A').props.disabled, true);
    button(render(), '停用').props.onClick();
    await Promise.resolve(); await Promise.resolve();
    assert.deepEqual(requests[0].body, { disable_only: true, active: false, expected_version: 6 });
    assert.equal(requests[0].url, '/admin/suppliers/9');
  }
  reset();
  resources.set('/admin/suppliers', { data: [{ ...account, blocked_job_id: 2 }] });
  const render = manager(SuppliersView);
  assert.equal(button(render(), '停用').props.disabled, true);
  await button(render(), '停用').props.onClick();
  assert.deepEqual(requests, []);
});

test('catalogue uses explicit module capability and configuration readiness, including channels without secrets', async () => {
  for (const [providers, saved, expected] of [
    [[{ ...provider, capabilities: { ...capabilities, catalog: false } }], account, /不支持读取商品目录/],
    [[], account, /实现未编入/],
    [[provider], { ...account, configuration_status: 'invalid' }, /配置待修正/],
    [[provider], { ...account, schema_version: 2 }, /配置版本不兼容/],
  ]) {
    reset(); resources.set('/admin/supplier-providers', { data: providers }); resources.set('/admin/suppliers', { data: [saved] });
    const render = manager(SupplierCatalogView);
    find(render(), item => item.type === 'select' && item.props['aria-label'] === '选择供应商').props.onChange({ target: { value: '9' } });
    assert.equal(button(render(), '只读拉取上游目录').props.disabled, true);
    assert.match(text(render()), expected);
    await button(render(), '只读拉取上游目录').props.onClick();
    assert.deepEqual(requests, []);
  }
  reset();
  const publicProvider = { ...provider, descriptor: { ...descriptor, fields: [] } };
  resources.set('/admin/supplier-providers', { data: [publicProvider] });
  resources.set('/admin/suppliers', { data: [{ ...account, config: {}, secure_fields: {}, credentials_configured: false }] });
  const render = manager(SupplierCatalogView);
  find(render(), item => item.type === 'select' && item.props['aria-label'] === '选择供应商').props.onChange({ target: { value: '9' } });
  assert.equal(button(render(), '只读拉取上游目录').props.disabled, false);
  respond = async () => [];
  button(render(), '只读拉取上游目录').props.onClick();
  await Promise.resolve(); await Promise.resolve();
  assert.equal(requests[0].url, '/admin/suppliers/9/catalog');
});

test('API settings connection probes include both account and schema versions and unavailable accounts stay disabled', async () => {
  reset();
  const settings = { enabled: true, require_certification: false, require_verified_phone: false, version: 1, updated_at: '2026-10-18', providers: [{ provider, descriptor }], accounts: [account], account_total: 1 };
  resources.set('/admin/supplier-api-settings', { data: settings });
  const render = manager(SupplierAPISettingsView);
  render(); flushTimers();
  assert.equal(button(render(), '测试连接').props.disabled, false);
  button(render(), '测试连接').props.onClick();
  await Promise.resolve(); await Promise.resolve();
  assert.deepEqual(requests[0].body, { expected_version: 6, expected_schema_version: 3 });
  assert.ok(requests[0].headers['Idempotency-Key']);
  reset(); resources.set('/admin/supplier-api-settings', { data: { ...settings, accounts: [{ ...account, configuration_status: 'schema_incompatible' }] } });
  const unavailable = manager(SupplierAPISettingsView);
  unavailable(); flushTimers();
  assert.equal(button(unavailable(), '测试连接').props.disabled, true);
  assert.match(text(unavailable()), /配置版本不兼容/);
  await button(unavailable(), '测试连接').props.onClick();
  assert.deepEqual(requests, []);
});

test('quantity import uses the actual quantity and multiplies unit and setup prices alongside select options', async () => {
  for (const quantity of ['0', '3', '10']) {
    reset();
    const product = { ...quantityProduct, options: [...quantityProduct.options, { id: 8, name: '地域', type: 'select', required: true, choices: [{ id: 23, name: '华东', prices: { monthly: { amount_cents: 200, setup_cents: 20 } } }] }] };
    const render = mount(ImportProduct, { account, product });
    const control = input(render(), '数据盘数量');
    assert.equal(control.props.min, 0); assert.equal(control.props.max, 10); assert.equal(control.props.step, 1);
    control.props.onChange({ target: { value: quantity } });
    find(render(), item => item.type === 'select' && item.props['aria-label'] === '地域').props.onChange({ target: { value: '23' } });
    await form(render()).props.onSubmit(event);
    assert.equal(requests.length, 1);
    assert.deepEqual(requests[0].body.config_options, { 7: quantity, 8: '23' });
    assert.equal(requests[0].body.maximum_cost_cents, 1320 + Number(quantity) * 55);
    assert.equal(requests[0].body.active, false);
    assert.equal(requests[0].body.expected_supplier_version, 6);
    assert.equal(requests[0].body.expected_upstream_revision, 'revision-1');
    assert.deepEqual(navigations, ['/admin/products/12']);
  }
});

test('quantity bounds, safe integers and cost ceilings reject invalid imports before any request', async () => {
  for (const quantity of ['', '-1', '1.5', '11', '1e1', '9007199254740992']) {
    reset();
    const render = mount(ImportProduct, { account, product: quantityProduct });
    input(render(), '数据盘数量').props.onChange({ target: { value: quantity } });
    await form(render()).props.onSubmit(event);
    assert.deepEqual(requests, []);
    assert.match(text(render()), /数量必须为 0 至 10 之间的整数/);
  }
  reset();
  const render = mount(ImportProduct, { account, product: quantityProduct });
  input(render(), '数据盘数量').props.onChange({ target: { value: '3' } });
  const costLabel = find(render(), item => item.type === 'label' && text(item).startsWith('上游最高成本'));
  find(costLabel, item => item.type === Input).props.onChange({ target: { value: '11.00' } });
  await form(render()).props.onSubmit(event);
  assert.deepEqual(requests, []);
  assert.match(text(render()), /上游最高成本低于当前规格和初装费合计/);
  button(render(), '取消更改').props.onClick();
  assert.equal(input(render(), '数据盘数量').props.value, '0');
});

test('unknown product option types remain visible as unsupported and cannot submit', async () => {
  reset();
  const render = mount(ImportProduct, { account, product: { ...quantityProduct, options: [{ ...quantityProduct.options[0], type: 'staged' }] } });
  assert.equal(button(render(), '导入为下架商品').props.disabled, true);
  assert.match(text(render()), /不支持的规格类型：staged/);
  await form(render()).props.onSubmit(event);
  assert.deepEqual(requests, []);
});

test('account errors preserve escaped messages and the edited credential for an explicit retry', async () => {
  reset();
  const render = editor({ account });
  input(render(), 'token').props.onChange({ target: { value: 'retry-test-token' } });
  respond = async () => { throw new ApiError(409, 'version_conflict', '<账户已被其他管理员更新>'); };
  await form(render()).props.onSubmit(event);
  assert.equal(input(render(), 'token').props.value, 'retry-test-token');
  assert.match(text(render()), /<账户已被其他管理员更新>/);
  const html = renderToStaticMarkup(render());
  assert.match(html, /&lt;账户已被其他管理员更新&gt;/);
  assert.doesNotMatch(html, /<账户已被其他管理员更新>/);
  assert.equal(button(render(), '保存更改').props.disabled, false);
  assert.deepEqual(refreshes, []);
});

const lifecycleService = extra => ({ id: 41, name: '测试上游服务', provision_mode: 'supplier', upstream_id: '701', status: 'active', supplier_capabilities: { ...capabilities, sync: true, power: true, vnc: true }, jobs: [], ...extra });
const lifecycleJob = (action = 'suspend') => ({ id: 91, action, status: 'waiting_manual', attempts: 0, manual_lifecycle: true });
function lifecycle(service = lifecycleService()) {
  return mount(SupplierLifecycle, { service, refresh: () => refreshes.push('service') });
}
function verificationOutcome(tree, value) {
  find(tree, item => item.type === 'select' && item.props['aria-label'] === '人工生命周期核验结果').props.onChange({ target: { value } });
}

test('manual supplier lifecycle requests retain the confirmed status and termination requires explicit risk acknowledgement', async () => {
  reset(); permissions.add('services.manage');
  const service = lifecycleService();
  const render = lifecycle(service);
  assert.match(text(render()), /其他动作只登记待人工任务/);
  button(render(), '登记上游终止').props.onClick();
  assert.equal(button(render(), '确认提交').props.disabled, true);
  await button(render(), '确认提交').props.onClick();
  assert.equal(requests.length, 0);
  check(render(), '确认终止风险').props.onCheckedChange(true);
  respond = async () => ({ job_id: 91, service_id: 41, action: 'terminate', status: 'waiting_manual' });
  await button(render(), '确认提交').props.onClick();
  assert.deepEqual(requests[0].body, { action: 'terminate' });
  assert.equal(requests[0].url, '/admin/services/41/actions');
  assert.ok(requests[0].headers['Idempotency-Key']);
  assert.match(text(render()), /等待在上游实际处理及核验；当前服务状态保留/);
  assert.equal(service.status, 'active');
});

test('manual lifecycle verification fixes the original resource, supports zero attempts, and requires absence acknowledgement', async () => {
  reset(); permissions.add('services.manage');
  const render = lifecycle(lifecycleService({ jobs: [lifecycleJob()] }));
  verificationOutcome(render(), 'delivered');
  input(render(), '人工生命周期核验依据').props.onChange({ target: { value: '已在上游核对暂停状态' } });
  respond = async () => ({ verified: true });
  await form(render()).props.onSubmit(event);
  assert.equal(requests[0].url, '/admin/services/41/supplier-lifecycle/verify');
  assert.deepEqual(requests[0].body, { job_id: 91, expected_attempts: 0, upstream_id: 701, outcome: 'delivered', note: '已在上游核对暂停状态', absence_acknowledged: false });
  reset(); permissions.add('services.manage');
  const notApplied = lifecycle(lifecycleService({ jobs: [lifecycleJob('resume')], status: 'suspended' }));
  verificationOutcome(notApplied(), 'not_applied');
  input(notApplied(), '人工生命周期核验依据').props.onChange({ target: { value: '已核对本次恢复操作没有执行' } });
  assert.equal(button(notApplied(), '提交并核验本次结果').props.disabled, true);
  await form(notApplied()).props.onSubmit(event);
  assert.equal(requests.length, 0);
  assert.match(text(notApplied()), /保留既存资源及当前服务状态/);
  check(notApplied(), '确认本次生命周期操作未生效').props.onCheckedChange(true);
  respond = async () => ({ verified: true });
  await form(notApplied()).props.onSubmit(event);
  assert.equal(requests[0].body.upstream_id, 0);
  assert.equal(requests[0].body.absence_acknowledged, true);
});

test('unknown lifecycle results survive remount and refresh while retries preserve the exact request identity', async () => {
  reset(); permissions.add('services.manage');
  const service = lifecycleService();
  let render = lifecycle(service);
  button(render(), '登记上游暂停').props.onClick();
  respond = async () => { throw new Error('连接中断'); };
  await button(render(), '确认提交').props.onClick();
  const original = structuredClone(requests[0]);
  assert.equal(pending.unknown, true);
  assert.equal(button(render(), '登记上游暂停').props.disabled, true);
  render = lifecycle(service);
  assert.match(text(render()), /刷新页面后仍保留原请求/);
  respond = async () => lifecycleService({ jobs: [lifecycleJob()] });
  await button(render(), '读取最新服务状态').props.onClick();
  assert.equal(requests[1].method, 'GET');
  assert.equal(pending.unknown, true);
  respond = async () => ({ job_id: 91, service_id: 41, action: 'suspend', status: 'waiting_manual', replayed: true });
  await button(render(), '重试原请求').props.onClick();
  assert.deepEqual(requests[2], original);
  assert.equal(pending.unknown, false);
  assert.equal(pending.pending.current, null);
});

test('read-only staff and customers cannot manage lifecycle tasks and waiting manual blocks operational commands', async () => {
  reset();
  const service = lifecycleService({ jobs: [lifecycleJob()] });
  const reader = lifecycle(service);
  assert.match(text(reader()), /登记和核验需要服务管理权限/);
  assert.equal(nodes(reader()).some(item => item.type === 'form'), false);
  const customer = mount(SupplierServiceDelivery, { service, admin: false, refresh() {} });
  assert.match(text(customer()), /等待管理员处理及核验/);
  assert.equal(nodes(customer()).some(item => item.type === SupplierLifecycle), false);
  assert.equal(button(customer(), '开机').props.disabled, true);
  assert.equal(button(customer(), 'VNC').props.disabled, true);
  await button(customer(), '开机').props.onClick();
  assert.deepEqual(requests, []);
});

test('lifecycle sends do not duplicate on rapid submit or accept malformed recovery requests', async () => {
  reset(); permissions.add('services.manage');
  const render = lifecycle();
  button(render(), '登记上游暂停').props.onClick();
  let finish;
  respond = () => new Promise(resolve => { finish = resolve; });
  const submit = button(render(), '确认提交').props.onClick;
  const first = submit();
  await submit();
  assert.equal(requests.length, 1);
  finish({ job_id: 91, service_id: 41, action: 'suspend', status: 'waiting_manual' });
  await first;
  reset(); permissions.add('services.manage');
  pending.unknown = true;
  pending.pending.current = { path: '/admin/services/999/actions', body: '{"action":"terminate"}', key: 'foreign-service-key' };
  const malformed = lifecycle();
  assert.match(text(malformed()), /原请求恢复资料不完整/);
  assert.equal(nodes(malformed()).some(item => item.type === Button && text(item) === '重试原请求'), false);
  assert.equal(button(malformed(), '登记上游暂停').props.disabled, true);
});

test('unknown lifecycle verification keeps the original resource and explicitly confirms termination or an unapplied action', async () => {
  reset(); permissions.add('services.manage');
  const service = lifecycleService({ jobs: [{ ...lifecycleJob('terminate'), status: 'waiting_verification', attempts: 1, manual_lifecycle: false }] });
  const parent = mount(SupplierServiceDelivery, { service, admin: true, refresh: () => refreshes.push('service') });
  const child = find(parent(), item => item.type?.name === 'SupplierVerification');
  const render = mount(child.type, child.props);
  const choose = value => find(render(), item => item.type === 'select' && item.props['aria-label'] === '未知上游结果核验').props.onChange({ target: { value } });
  assert.match(text(render()), /原动作可能已发送/);
  choose('delivered');
  const resource = find(render(), item => item.type === Input && item.props.type === 'number');
  assert.equal(resource.props.readOnly, true);
  assert.equal(resource.props.value, '701');
  // Even an injected change event cannot retarget a lifecycle confirmation.
  resource.props.onChange({ target: { value: '999' } });
  find(render(), item => item.type === Textarea).props.onChange({ target: { value: '已在原供应商核对资源实际终止' } });
  await form(render()).props.onSubmit(event);
  assert.equal(requests.length, 0);
  check(render(), '确认未知终止结果风险').props.onCheckedChange(true);
  await form(render()).props.onSubmit(event);
  assert.equal(requests[0].url, '/admin/services/41/supplier-verification');
  assert.equal(requests[0].body.upstream_id, 701);
  assert.equal(requests[0].body.expected_attempts, 1);
  choose('not_applied');
  assert.match(text(render()), /原资源可以继续存在/);
  const acknowledgement = find(render(), item => item.type === Checkbox);
  await form(render()).props.onSubmit(event);
  assert.equal(requests.length, 1);
  acknowledgement.props.onCheckedChange(true);
  await form(render()).props.onSubmit(event);
  assert.equal(requests[1].body.upstream_id, 0);
  assert.equal(requests[1].body.absence_acknowledged, true);
});
