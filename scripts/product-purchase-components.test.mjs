import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { createRequire, Module } from 'node:module';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const web = path.join(root, 'web');
const require = createRequire(path.join(web, 'package.json'));
const ts = require('typescript');
const React = require('react');
const cache = new Map(), controls = new Map();
let active, resources, requests, resourceReads, destinations, latest, respond;
const changed = (before, next) => !before || !next || before.length !== next.length || before.some((value, index) => value !== next[index]);
const react = {
  ...React,
  cache: callback => callback,
  useState(initial) {
    const state = active, index = state.cursor++;
    if (!(index in state.values)) state.values[index] = typeof initial === 'function' ? initial() : initial;
    return [state.values[index], next => { state.values[index] = typeof next === 'function' ? next(state.values[index]) : next; }];
  },
  useRef(initial) { const state = active, index = state.cursor++; if (!(index in state.values)) state.values[index] = { current: initial }; return state.values[index]; },
  useId() { return react.useRef(`purchase-${active.cursor}`).current; },
  useMemo(callback, dependencies) { const state = active, index = state.cursor++; if (changed(state.values[index]?.dependencies, dependencies)) state.values[index] = { dependencies, value: callback() }; return state.values[index].value; },
  useCallback(callback, dependencies) { return react.useMemo(() => callback, dependencies); },
  useEffect(callback, dependencies) { const state = active, index = state.cursor++, before = state.values[index]; if (changed(before?.dependencies, dependencies)) state.effects.push(() => { before?.cleanup?.(); state.values[index] = { dependencies, cleanup: callback() }; }); },
};
function control(name) { if (!controls.has(name)) controls.set(name, props => React.createElement(name.toLowerCase(), props, props.children)); return controls.get(name); }
const Link = control('Link');
const icons = new Proxy({}, { get: (_, name) => control(String(name)) });
const common = { cycles: { monthly: '月付', annually: '年付', once: '一次性' }, Failure: control('Failure'), Loading: control('Loading') };
class ApiError extends Error { constructor(status, code, message) { super(message); Object.assign(this, { status, code }); } }
function load(filename) {
  if (cache.has(filename)) return cache.get(filename).exports;
  const module = new Module(filename); cache.set(filename, module); module.filename = filename; module.paths = Module._nodeModulePaths(path.dirname(filename));
  const external = createRequire(filename);
  module.require = specifier => {
    if (specifier === 'react') return react;
    if (specifier === 'server-only') return {};
    if (specifier === 'next/link') return { __esModule: true, default: Link };
    if (specifier === 'next/navigation') return { useRouter: () => ({ push: href => destinations.push(href), replace: href => destinations.push(href) }), notFound: () => { throw new Error('NOT_FOUND'); } };
    if (specifier === 'lucide-react') return icons;
    if (specifier.includes('/ui/')) return new Proxy({}, { get: (_, name) => control(String(name)) });
    if (specifier === './workspace') return { Workspace: control('Workspace') };
    if (specifier === './common') return common;
    if (specifier === '@/hooks/use-resource') return { useResource: endpoint => { resourceReads.push(endpoint); return resources.get(endpoint) ?? { data: null, error: null, refresh() {} }; } };
    if (specifier === '@/lib/api') return {
      ApiError, money: value => `¥${(Number(value) / 100).toFixed(2)}`,
      api: async (endpoint, options) => {
        const call = { endpoint, method: options?.method ?? 'GET', body: options?.body ? JSON.parse(options.body) : undefined, portal: options?.portal };
        requests.push(call);
        if (endpoint === '/me') { if (latest instanceof Error) throw latest; return latest; }
        if (respond) return respond(call);
        if (endpoint === '/cart') return { version: 9, items: [] };
        return { version: 10 };
      },
    };
    if (specifier.startsWith('.') || specifier.startsWith('@/')) {
      const base = specifier.startsWith('@/') ? path.join(web, 'src', specifier.slice(2)) : path.resolve(path.dirname(filename), specifier);
      const source = [`${base}.tsx`, `${base}.ts`, base].find(candidate => existsSync(candidate));
      if (source && /\.tsx?$/.test(source)) return load(source);
    }
    return external(specifier);
  };
  const compiled = ts.transpileModule(readFileSync(filename, 'utf8'), { reportDiagnostics: true, compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true } });
  assert.deepEqual((compiled.diagnostics ?? []).filter(item => item.category === ts.DiagnosticCategory.Error), []);
  module._compile(compiled.outputText, filename); return module.exports;
}
function mount(component, initialProps = {}) {
  const state = { cursor: 0, values: [], effects: [] }; let props = initialProps;
  return {
    render(next) { if (next) props = next; state.cursor = 0; active = state; let tree; try { tree = component(props); } finally { active = undefined; } for (const effect of state.effects.splice(0)) effect(); return tree; },
    unmount() { for (const value of state.values) value?.cleanup?.(); },
  };
}
function nodes(tree) { if (!tree || typeof tree !== 'object') return []; if (Array.isArray(tree)) return tree.flatMap(nodes); return [tree, ...React.Children.toArray(tree.props?.children).flatMap(nodes)]; }
function text(tree) { if (typeof tree === 'string' || typeof tree === 'number') return String(tree); if (!tree) return ''; if (Array.isArray(tree)) return tree.map(text).join(''); return React.Children.toArray(tree.props?.children).map(text).join(''); }
function find(tree, predicate, message = 'expected product control') { const value = nodes(tree).find(predicate); assert(value, `${message}: ${text(tree)}`); return value; }
function input(tree, label) { return find(tree, node => node.props?.['aria-label'] === label, `input ${label}`); }
function button(tree) { return find(tree, node => node.type === control('Button') && /加入购物车|管理员账号不可购买|登录后购买|暂不可购买/.test(text(node)), 'purchase action'); }
function change(node, value) { assert.equal(typeof node.props.onChange, 'function'); node.props.onChange({ target: { value: String(value) } }); }
const event = { preventDefault() {}, currentTarget: { checkValidity: () => true, reportValidity: () => true } };
async function submit(view, tree = view.render()) { const form = nodes(tree).find(node => node.type === 'form' && node.props.onSubmit); if (form) await form.props.onSubmit(event); else await button(tree).props.onClick(); }
const identity = role => ({ id: 7, role, display_name: 'Fixture', permissions: [] });
function reset(role = 'customer') {
  resources = new Map(); requests = []; resourceReads = []; destinations = []; latest = role ? identity(role) : new ApiError(401, 'unauthorized', '请先登录'); respond = null;
  resources.set('/me', { data: role ? identity(role) : null, error: null, refresh() {} });
}
function product(extra = {}) { return { id: 42, name: '独立详情云主机', slug: 'cloud-42', description: '产品说明', revision: 1, billing_cycle: 'monthly', price_cents: 1000, setup_fee_cents: 100, stock: 20, active: true, option_groups: [], purchase_field_definitions: [], ...extra }; }
function option(id, extra = {}) { return { id, label: `规格 ${id}`, value: `option-${id}`, active: true, price_delta_cents: 0, setup_delta_cents: 0, sort_order: id, ...extra }; }
function group(id, options, extra = {}) { return { id, name: `配置 ${id}`, slug: `group-${id}`, required: true, active: true, sort_order: id, option_type: 1, options, ...extra }; }
function field(key, extra = {}) { return { key, label: key, type: 'text', scope: 'purchase', active: true, required: false, choices: [], pattern: '', max_length: 120, description: '', ...extra }; }
const formModule = load(path.join(web, 'src/components/product-purchase-form.tsx'));
const { ProductPurchaseForm } = formModule;
const { ProductPurchaseView } = load(path.join(web, 'src/components/product-purchase-view.tsx'));
const { CatalogView } = load(path.join(web, 'src/components/catalog-view.tsx'));
const { ProductOptionChoice } = load(path.join(web, 'src/components/product-option-choice.tsx'));
const { PurchaseFieldInputs } = load(path.join(web, 'src/components/purchase-fields.tsx'));
function purchase(value = product(), role = 'customer') { reset(role); return mount(ProductPurchaseForm, { product: value }); }
function choice(tree, groupID) { return find(tree, node => node.type === ProductOptionChoice && node.props.group.id === groupID, `configuration ${groupID}`); }
function choose(view, groupID, optionID) { choice(view.render(), groupID).props.onChange(optionID); }
function fieldControls(tree) { const element = find(tree, node => node.type === PurchaseFieldInputs, 'purchase fields'); return mount(PurchaseFieldInputs, element.props).render(); }

test('catalog cards expose product-detail links and never mount purchase controls or write the cart', async () => {
  reset(); const item = product({ option_groups: [group(1, [option(11)])], purchase_field_definitions: [field('hostname', { required: true })] });
  resources.set('/catalog/page?q=&section_id=&category_id=&limit=12&offset=0', { data: { items: [item], total: 1, limit: 12, offset: 0 }, error: null, refresh() {} });
  resources.set('/catalog/sections', { data: [], error: null, refresh() {} }); resources.set('/categories', { data: [], error: null, refresh() {} });
  const view = mount(CatalogView), catalog = view.render();
  const tree = [catalog, ...nodes(catalog).filter(node => node.type?.name === 'ProductCard').map(node => mount(node.type, node.props).render())];
  const details = nodes(tree).filter(node => node.type === Link && node.props.href === '/products/42');
  assert(details.length >= 1);
  assert.equal(nodes(tree).some(node => node.type === ProductOptionChoice || node.type === PurchaseFieldInputs || node.type === ProductPurchaseForm), false);
  assert.equal(nodes(tree).some(node => node.type === control('Button') && /加入购物车/.test(text(node))), false);
  for (const link of details) if (link.props.onClick) await link.props.onClick(event);
  assert.deepEqual(requests, []);
  assert.equal(resourceReads.some(endpoint => typeof endpoint === 'string' && endpoint.startsWith('/cart')), false);
});

test('product detail revalidates the public product and blocks stale configuration on load failures', () => {
  reset(); const initial = product(), fresh = product({ name: '重新加载后的配置', revision: 2 });
  resources.set('/catalog/42', { data: fresh, error: null, refresh() {} });
  const tree = mount(ProductPurchaseView, { initialProduct: initial }).render();
  const form = find(tree, node => node.type === ProductPurchaseForm);
  assert.equal(form.props.product, fresh);
  assert(resourceReads.includes('/catalog/42'));
  resources.set('/catalog/42', { data: fresh, error: new Error('商品加载失败'), refresh() {} });
  const failed = mount(ProductPurchaseView, { initialProduct: initial }).render();
  assert.equal(nodes(failed).some(node => node.type === ProductPurchaseForm), false);
  assert(nodes(failed).some(node => node.type === common.Failure));
  assert.deepEqual(requests, []);
});

test('detail changes billing cycle and sends one exact cart mutation after rechecking customer identity', async () => {
  const view = purchase(product({ pricing: [
    { billing_cycle: 'monthly', price_cents: 1000, setup_fee_cents: 100, active: true },
    { billing_cycle: 'annually', price_cents: 10000, setup_fee_cents: 200, active: true },
  ] }));
  let tree = view.render();
  assert.match(text(tree), /¥10\.00/); assert.match(text(tree), /¥1\.00/);
  const cycle = find(tree, node => node.type === control('Select') && node.props.value === 'monthly', 'billing cycle selector');
  cycle.props.onValueChange('annually');
  tree = view.render(); assert.match(text(tree), /¥100\.00/); assert.match(text(tree), /¥2\.00/);
  await submit(view, tree);
  assert.deepEqual(requests.map(call => call.endpoint), ['/me', '/cart', '/cart/items']);
  assert(requests.every(call => call.portal === 'customer'));
  assert.equal(requests[2].method, 'POST');
  assert.deepEqual(requests[2].body, { version: 9, product_id: 42, quantity: 1, billing_cycle: 'annually', option_ids: [], option_quantities: {}, purchase_fields: {} });
  assert.deepEqual(destinations, ['/cart']);
});

test('service quantity preserves 1–100 integer bounds and multiplies recurring and setup amounts together', async () => {
  for (const amount of [1, 3, 100]) {
    const view = purchase(product({ stock: null })); let tree = view.render();
    const quantity = input(tree, '购买数量'); assert.equal(quantity.props.min, 1); assert.equal(quantity.props.max, 100); assert.equal(quantity.props.step, 1);
    change(quantity, amount); tree = view.render();
    await submit(view, tree);
    assert.equal(requests.at(-1).body.quantity, amount);
    assert.match(text(tree), new RegExp(`¥${(amount * 1100 / 100).toFixed(2).replace('.', '\\.')}`));
  }
  for (const invalid of ['', 0, -1, 1.5, 101, 'not-a-number', '9007199254740992']) {
    const view = purchase(); change(input(view.render(), '购买数量'), invalid);
    const tree = view.render(); assert.equal(button(tree).props.disabled, true);
    await submit(view, tree);
    assert.equal(requests.some(call => call.endpoint === '/cart' || call.endpoint === '/cart/items'), false, `invalid quantity ${invalid}`);
  }
});

test('quantity options honor minimum, maximum and step at display and mutation boundaries', async () => {
  const value = product({ option_groups: [group(1, [option(11, { price_delta_cents: 100, setup_delta_cents: 10, qty_minimum: 2, qty_maximum: 8 })], { name: '数据盘', option_type: 4, qty_minimum: 2, qty_maximum: 8, qty_stage: 2 })] });
  const view = purchase(value); let tree = view.render();
  const amount = input(tree, '数据盘 数量'); assert.equal(amount.props.min, 2); assert.equal(amount.props.max, 8); assert.equal(amount.props.step, 2);
  change(amount, 4); tree = view.render(); await submit(view, tree);
  assert.deepEqual(requests.at(-1).body.option_ids, [11]); assert.deepEqual(requests.at(-1).body.option_quantities, { 11: 4 });
  assert.match(text(tree), /¥14\.00/); assert.match(text(tree), /¥1\.40/);
  for (const invalid of [0, 1, 3, 10, 2.5]) {
    const form = purchase(value); change(input(form.render(), '数据盘 数量'), invalid); const invalidTree = form.render();
    assert.equal(button(invalidTree).props.disabled, true); await submit(form, invalidTree);
    assert.equal(requests.some(call => call.endpoint === '/cart' || call.endpoint === '/cart/items'), false);
  }
});

test('explicit zero-quantity options remain zero in the payload and charge no option amount', async () => {
  const view = purchase(product({ option_groups: [group(1, [option(11, { price_delta_cents: 500, setup_delta_cents: 200, qty_minimum: 0, qty_maximum: 10, allow_zero_quantity: true })], { name: '额外地址', option_type: 19, qty_minimum: 0, qty_maximum: 10, allow_zero_quantity: true, qty_stage: 1 })] }));
  const tree = view.render(); assert.equal(input(tree, '额外地址 数量').props.value, 0);
  await submit(view, tree);
  assert.deepEqual(requests.at(-1).body.option_quantities, { 11: 0 });
  assert.match(text(tree), /¥10\.00/); assert.match(text(tree), /¥1\.00/);
});

test('graduated and volume tier prices use selected quantity and tier setup exactly once per service', async () => {
  for (const [mode, recurring] of [['graduated', '¥13.40'], ['volume', '¥12.80']]) {
    const view = purchase(product({ option_groups: [group(1, [option(11, { qty_minimum: 1, qty_maximum: 8, price_tiers: [
      { billing_cycle: 'monthly', qty_minimum: 1, qty_maximum: 2, price_cents: 100, setup_fee_cents: 10, active: true, pricing_mode: mode },
      { billing_cycle: 'monthly', qty_minimum: 3, qty_maximum: 8, price_cents: 70, setup_fee_cents: 30, active: true, pricing_mode: mode },
    ] })], { name: '按量资源', option_type: 4, qty_minimum: 1, qty_maximum: 8 })] }));
    change(input(view.render(), '按量资源 数量'), 4);
    const tree = view.render(); assert(text(tree).includes(recurring)); assert.match(text(tree), /¥1\.30/);
    await submit(view, tree); assert.deepEqual(requests.at(-1).body.option_quantities, { 11: 4 });
  }
});

test('changing a parent option clears every descendant and excludes hidden branch choices from cart payload', async () => {
  const view = purchase(product({ option_groups: [
    group(1, [option(11, { label: '华东' }), option(12, { label: '华南' })], { name: '地域' }),
    group(2, [option(21, { parent_option_id: 11 }), option(22, { parent_option_id: 12 })], { name: '机房', parent_group_id: 1 }),
    group(3, [option(31, { parent_option_id: 21 }), option(32, { parent_option_id: 22 })], { name: '节点', parent_group_id: 2 }),
  ] }));
  let tree = view.render(); assert.equal(choice(tree, 2).props.selected, 21); assert.equal(choice(tree, 3).props.selected, 31);
  choose(view, 1, 12); tree = view.render();
  assert.equal(choice(tree, 2).props.selected, 22); assert.equal(choice(tree, 3).props.selected, 32);
  await submit(view, tree); assert.deepEqual(requests.at(-1).body.option_ids, [12, 22, 32]);
  assert.equal(requests.at(-1).body.option_ids.includes(21), false); assert.equal(requests.at(-1).body.option_ids.includes(31), false);
});

test('purchase inputs keep required customer data in memory and filter admin and inactive fields from payload', async () => {
  const view = purchase(product({ purchase_field_definitions: [field('hostname', { required: true }), field('admin_note', { scope: 'admin' }), field('obsolete', { active: false }), field('password', { encrypted: true })] }));
  let tree = view.render();
  await submit(view, tree);
  assert.equal(requests.some(call => call.endpoint === '/cart/items'), false);
  assert.match(text(view.render()), /请填写hostname/);
  let fields = fieldControls(view.render());
  assert.equal(nodes(fields).some(node => node.props?.['aria-label'] === 'admin_note' || node.props?.['aria-label'] === 'obsolete'), false);
  change(input(fields, 'hostname'), 'test-host');
  fields = fieldControls(view.render()); assert.equal(input(fields, 'password').props.type, 'password'); change(input(fields, 'password'), 'temporary-password');
  tree = view.render(); await submit(view, tree);
  assert.deepEqual(requests.at(-1).body.purchase_fields, { hostname: 'test-host', password: 'temporary-password' });
  assert.equal(destinations.some(destination => /password|hostname|temporary/.test(destination)), false);
  assert.deepEqual(find(view.render(), node => node.type === PurchaseFieldInputs).props.values, {});
  assert.equal(text(view.render()).includes('temporary-password'), false);
});

test('price gaps, malformed configuration, zero stock and load failures block cart writes even from stale handlers', async () => {
  const cases = [
    product({ stock: 0 }),
    product({ active: false }),
    product({ category_visible: false }),
    product({ price_cents: -1 }),
    product({ price_cents: Number.MAX_SAFE_INTEGER, setup_fee_cents: 1 }),
    product({ pricing: [{ billing_cycle: 'monthly', price_cents: 1000, setup_fee_cents: 100, active: false }] }),
    product({ option_groups: [group(1, [option(11, { price_tiers: [{ billing_cycle: 'monthly', qty_minimum: 2, qty_maximum: 3, price_cents: 10, setup_fee_cents: 0 }] })], { name: '价格缺口', option_type: 4, qty_minimum: 1 })] }),
    product({ option_groups: [group(1, [], { required: true })] }),
    product({ option_groups: [group(1, [option(11)])], configuration_rules: [{ id: 1, name: '损坏规则', enabled: true, conditions: [], results: [] }] }),
  ];
  for (const value of cases) {
    const view = purchase(value), tree = view.render(); assert.equal(button(tree).props.disabled, true);
    await submit(view, tree); assert.equal(requests.some(call => call.endpoint === '/cart' || call.endpoint === '/cart/items'), false);
  }
  const view = purchase(); respond = async call => { if (call.endpoint === '/cart') throw new Error('购物车暂不可用'); return {}; };
  await submit(view); assert.deepEqual(requests.map(call => call.endpoint), ['/me', '/cart']); assert.match(text(view.render()), /购物车暂不可用/);
});

test('an administrator-only browser sees the ordinary anonymous storefront and can log into a separate customer account', async () => {
  const anonymous = purchase(product(), null), initial = anonymous.render();
  assert.equal(button(initial).props.disabled, false);
  assert.equal(text(initial).includes('管理员账号不可购买'), false);
  assert.equal(text(initial).includes('独立客户账号'), false);
  await submit(anonymous, initial);
  assert.deepEqual(requests.map(call => call.endpoint), ['/me']);
  assert.equal(requests.some(call => call.endpoint === '/cart' || call.endpoint === '/cart/items'), false);
  assert(destinations.includes('/login') || nodes(anonymous.render()).some(node => node.type === Link && node.props.href === '/login'));
  latest = identity('customer'); requests.length = 0; destinations.length = 0;
  await submit(anonymous);
  assert.deepEqual(requests.map(call => call.endpoint), ['/me', '/cart', '/cart/items']);
  assert.deepEqual(destinations, ['/cart']);
});

test('product route loads public data without account credentials and keeps purchase values outside metadata and JSON-LD', async () => {
  reset(); const publicProduct = product({ name: '云主机<script>unsafe</script>', purchase_field_definitions: [field('root_password', { encrypted: true, description: '内部资料字段说明' })], option_groups: [group(1, [option(11)])] });
  const fetches = [], originalFetch = globalThis.fetch;
  try {
    globalThis.fetch = async (url, options) => { fetches.push({ url, options }); return Response.json({ data: publicProduct }); };
    const route = load(path.join(web, 'src/app/products/[id]/page.tsx'));
    const metadata = await route.generateMetadata({ params: Promise.resolve({ id: '42' }) });
    assert.equal(metadata.title, publicProduct.name);
    assert.equal(JSON.stringify(metadata).includes('root_password'), false);
    assert.equal(JSON.stringify(metadata).includes('内部资料字段说明'), false);
    const tree = await route.default({ params: Promise.resolve({ id: '42' }) });
    const schema = find(tree, node => node.type === 'script' && node.props.type === 'application/ld+json');
    const json = schema.props.dangerouslySetInnerHTML.__html;
    assert.equal(json.includes('<script>'), false);
    const parsed = JSON.parse(json);
    assert.equal(parsed.name, publicProduct.name);
    assert.deepEqual(Object.keys(parsed).sort(), ['@context', '@type', 'description', 'name', 'url']);
    assert.equal(json.includes('root_password'), false);
    assert(nodes(tree).some(node => node.type === ProductPurchaseView));
    assert(fetches.length >= 2);
    for (const call of fetches) {
      assert.match(call.url, /\/api\/v1\/catalog\/42$/);
      assert.equal(call.options.cache, 'no-store');
      assert.equal(call.options.headers, undefined);
      assert.equal(call.options.credentials, undefined);
    }
    const before = fetches.length;
    await assert.rejects(route.default({ params: Promise.resolve({ id: '../admin' }) }), /NOT_FOUND/);
    assert.equal(fetches.length, before);
    globalThis.fetch = async () => new Response(null, { status: 404 });
    await assert.rejects(route.default({ params: Promise.resolve({ id: '900' }) }), /NOT_FOUND/);
  } finally { globalThis.fetch = originalFetch; }
});

test('native configuration rules prevent invalid selections and allow the corrected selection', async () => {
  const value = product({ option_groups: [group(1, [option(11)], { name: '系统' }), group(2, [option(21), option(22)], { name: '内存' })], configuration_rules: [{ id: 5, name: '系统最小内存', enabled: true, conditions: [{ group_id: 1, option_id: 11, operator: 'equals', qty_minimum: null, qty_maximum: null }], results: [{ group_id: 2, option_id: 22, operator: 'equals', qty_minimum: null, qty_maximum: null }] }] });
  const view = purchase(value), invalid = view.render(); assert.equal(button(invalid).props.disabled, true); assert.match(text(invalid), /系统最小内存/);
  await submit(view, invalid); assert.equal(requests.some(call => call.endpoint === '/cart/items'), false);
  choose(view, 2, 22); const corrected = view.render(); assert.equal(button(corrected).props.disabled, false);
  await submit(view, corrected); assert.deepEqual(requests.at(-1).body.option_ids, [11, 22]);
});

test('invalid customer purchase fields block stale submits instead of relying only on browser constraints', async () => {
  const cases = [
    [field('hostname', { max_length: 4 }), 'too-long'],
    [field('region', { type: 'select', choices: ['华东', '华南'] }), '过期选项'],
    [field('website', { type: 'url' }), 'javascript:alert(1)'],
    [field('website', { type: 'url' }), 'https://user:password@example.test/'],
    [field('password', { encrypted: true }), '********'],
    [field('password', { encrypted: true }), 'enc:not-new-input'],
  ];
  for (const [definition, value] of cases) {
    const view = purchase(product({ purchase_field_definitions: [definition] }));
    const fields = find(view.render(), node => node.type === PurchaseFieldInputs); fields.props.onChange({ [definition.key]: value });
    const tree = view.render();
    await submit(view, tree); assert.equal(requests.some(call => call.endpoint === '/cart/items'), false, definition.key);
    assert(nodes(view.render()).some(node => node.props?.role === 'alert' && text(node).includes(definition.key)), definition.key);
  }
});

test('a duplicate submit from the same rendered form creates only one cart mutation', async () => {
  const view = purchase(), tree = view.render();
  const first = submit(view, tree), second = submit(view, tree);
  await Promise.all([first, second]);
  assert.deepEqual(requests.map(call => call.endpoint), ['/me', '/cart', '/cart/items']);
  assert.equal(requests.filter(call => call.method === 'POST').length, 1);
  assert.deepEqual(destinations, ['/cart']);
  await submit(view);
  assert.equal(requests.filter(call => call.method === 'POST').length, 1, 'navigation-pending success must stay locked');
  assert.deepEqual(destinations, ['/cart']);
});

test('changing a quantity-option ancestor discards hidden quantities before revisiting and submitting a new branch', async () => {
  const view = purchase(product({ option_groups: [
    group(1, [option(11), option(12)], { name: '地域' }),
    group(2, [option(21, { parent_option_id: 11, qty_minimum: 2, qty_maximum: 10 }), option(22, { parent_option_id: 12, qty_minimum: 2, qty_maximum: 10 })], { name: '带宽', parent_group_id: 1, option_type: 4, qty_minimum: 2, qty_maximum: 10, qty_stage: 2 }),
  ] }));
  change(input(view.render(), '带宽 数量'), 8); assert.equal(input(view.render(), '带宽 数量').props.value, 8);
  choose(view, 1, 12); assert.equal(input(view.render(), '带宽 数量').props.value, 2);
  choose(view, 1, 11); assert.equal(input(view.render(), '带宽 数量').props.value, 2);
  choose(view, 1, 12); await submit(view);
  assert.deepEqual(requests.at(-1).body.option_ids, [12, 22]); assert.deepEqual(requests.at(-1).body.option_quantities, { 22: 2 });
});

test('an unavailable selected billing cycle and insufficient service stock block stale submit handlers', async () => {
  const view = purchase(product({ pricing: [{ billing_cycle: 'monthly', price_cents: 1000, setup_fee_cents: 100, active: true }] }));
  const select = find(view.render(), node => node.type === control('Select') && node.props.value === 'monthly');
  select.props.onValueChange('annually'); const missing = view.render(); assert.equal(button(missing).props.disabled, true); assert.match(text(missing), /账期暂不可用/);
  await submit(view, missing); assert.equal(requests.some(call => call.endpoint === '/cart'), false);
  const stock = purchase(product({ stock: 1 })); change(input(stock.render(), '购买数量'), 2); const unavailable = stock.render(); assert.equal(button(unavailable).props.disabled, true);
  await submit(stock, unavailable); assert.equal(requests.some(call => call.endpoint === '/cart'), false); assert.match(text(unavailable), /可售库存/);
});
