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
let current, cart, calls, handler, destinations;
const changed = (before, after) => !before || !after || before.length !== after.length || before.some((value, i) => value !== after[i]);
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
  useEffect(effect, dependencies) {
    const state = current, index = state.cursor++, before = state.values[index];
    if (changed(before?.dependencies, dependencies)) state.effects.push(() => { before?.cleanup?.(); state.values[index] = { dependencies, cleanup: effect() }; });
  },
};
class ApiError extends Error {}
const control = name => props => React.createElement(name, props, props.children);
const Button = control('button');
const Checkbox = control('input');
const ui = new Proxy({}, { get: (_, name) => name === 'Button' ? Button : name === 'Checkbox' ? Checkbox : control(String(name)) });
function load(filename) {
  if (cache.has(filename)) return cache.get(filename).exports;
  const module = new Module(filename);
  cache.set(filename, module);
  module.filename = filename;
  module.paths = Module._nodeModulePaths(path.dirname(filename));
  const external = createRequire(filename);
  module.require = specifier => {
    if (specifier === 'react') return react;
    if (specifier === 'next/navigation') return { useRouter: () => ({ push: value => destinations.push(value) }), useSearchParams: () => new URLSearchParams() };
    if (specifier === 'next/link') return { __esModule: true, default: control('a') };
    if (specifier === './workspace') return { Workspace: control('main'), PermissionGate: ({ children }) => children, useAdminPermission: () => true };
    if (specifier === './common') return { cycles: { monthly: '月' }, Loading: () => null, Failure: () => null };
    if (specifier === './purchase-fields') return { PurchaseFieldInputs: () => null, PurchaseFieldDetails: () => null, purchaseFieldValues: () => ({}) };
    if (specifier === '@/hooks/use-resource') return { useResource: url => ({ data: url === '/cart' ? cart : undefined, error: null, refresh() {} }) };
    if (specifier.includes('/ui/')) return ui;
    if (specifier === '@/lib/api') return {
      ApiError, money: cents => `¥${(cents / 100).toFixed(2)}`,
      api: async (url, options = {}) => { const call = { url, ...options, body: options.body ? JSON.parse(options.body) : undefined }; calls.push(call); return handler(call); },
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
const { CartView } = load(path.join(web, 'src/components/cart-view.tsx'));
const { PurchaseDiscountDetails, PurchaseDiscountSummary } = load(path.join(web, 'src/components/purchase-discount-details.tsx'));
const renderMarkup = (component, props) => renderToStaticMarkup(React.createElement(component, props));
const line = (extra = {}) => ({ item_id: 1, product_id: 12, quantity: 1, gross_cents: 1000, group_discount_cents: 200, promo_discount_cents: 100, net_cents: 700, recurring_cents: 800, group_snapshot: { source: 'customer_group', customer_group_name: 'VIP', discount_type: 'percent', discount_percent: 20 }, ...extra });
const quote = (hash = 'confirmed-quote', amount = 700) => ({ quote_hash: hash, subtotal_cents: 1000, group_discount_cents: 200, promo_discount_cents: 100, discount_cents: 300, total_cents: amount, items: [line({ net_cents: amount })], promo_code: 'VIPCODE' });
const item = id => ({ id, product_id: id + 10, name: `产品${id}`, billing_cycle: 'monthly', quantity: 1, price_cents: 1000, setup_fee_cents: 0, total_cents: 1000, current: true, active: true, options_valid: true, fields_valid: true, option_ids: [], option_quantities: {}, purchase_fields: {} });
function reset() {
  cart = { id: 'cart', version: 7, items: [item(1), item(2)], currency: 'CNY', promo: null, discount_cents: 0 };
  calls = []; destinations = [];
  handler = async call => call.url === '/cart/quote' ? quote() : { order_id: 42 };
  globalThis.window = { addEventListener() {}, removeEventListener() {} };
}
function mount() {
  const state = { cursor: 0, values: [], effects: [] };
  return {
    render() {
      state.cursor = 0; current = state;
      let tree;
      try { tree = CartView(); } finally { current = undefined; }
      for (const effect of state.effects.splice(0)) effect();
      return tree;
    },
    unmount() { for (const value of state.values) value?.cleanup?.(); },
  };
}
function nodes(tree) {
  if (!tree || typeof tree !== 'object') return [];
  if (Array.isArray(tree)) return tree.flatMap(nodes);
  return [tree, ...React.Children.toArray(tree.props?.children).flatMap(nodes)];
}
function text(tree) {
  if (typeof tree === 'string' || typeof tree === 'number') return String(tree);
  if (!tree) return '';
  if (Array.isArray(tree)) return tree.map(text).join('');
  return React.Children.toArray(tree.props?.children).map(text).join('');
}
function checkout(tree) { const result = nodes(tree).find(node => node.type === Button && text(node).includes('确认订单')); assert.ok(result); return result; }
function select(view, id, selected = true) { const node = nodes(view.render()).find(node => node.type === Checkbox && node.props['aria-label'] === `选择 产品${id}`); assert.ok(node); node.props.onCheckedChange(selected); }
const settle = async () => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); };
function deferred() { let resolve, reject; const promise = new Promise((a, b) => { resolve = a; reject = b; }); return { promise, resolve, reject }; }

test('pricing details distinguish group and coupon reductions, per-unit recurring basis and override', () => {
  let html = renderMarkup(PurchaseDiscountDetails, { pricing: line() });
  assert.match(html, /VIP.*减免 20%/); assert.match(html, /分组减免 -¥2.00.*优惠码减免 -¥1.00/);
  assert.match(html, /本次应付 ¥7.00/); assert.match(html, /每件服务续费基价 ¥8.00/);
  html = renderMarkup(PurchaseDiscountDetails, { pricing: line({ group_snapshot: { source: 'product_group', customer_group_name: '渠道', product_group_name: '云主机', discount_type: 'fixed', fixed_discount_cents: 100 } }) });
  assert.match(html, /渠道.*云主机.*每件减免 ¥1.00/);
  html = renderMarkup(PurchaseDiscountDetails, { pricing: line({ group_snapshot: { source: 'override' } }) });
  assert.match(html, /管理员内部价格，不叠加分组减免/);
  assert.equal(renderMarkup(PurchaseDiscountDetails, {}), '');
});

test('legacy orders retain only their saved total discount without fabricating split amounts', () => {
  const html = renderMarkup(PurchaseDiscountSummary, { discountCents: 300, promoCode: 'HISTORICAL' });
  assert.match(html, /优惠 ¥3.00.*HISTORICAL/); assert.doesNotMatch(html, /分组减免|优惠码减免/);
  const split = renderMarkup(PurchaseDiscountSummary, { discountCents: 300, groupDiscountCents: 200, promoDiscountCents: 100 });
  assert.match(split, /分组减免 -¥2.00/); assert.match(split, /优惠码减免 -¥1.00/);
});

test('checkout waits for a quote of the exact selection and submits its hash', async () => {
  reset(); const pending = deferred(); handler = async call => call.url === '/cart/quote' ? pending.promise : { order_id: 42 };
  const view = mount(); assert.equal(checkout(view.render()).props.disabled, true);
  select(view, 1); let tree = view.render();
  assert.equal(checkout(tree).props.disabled, true); assert.match(text(tree), /正在计算所选项目报价/);
  await checkout(tree).props.onClick(); assert.equal(calls.filter(call => call.url === '/orders').length, 0);
  assert.deepEqual(calls[0].body, { cart_version: 7, item_ids: [1] });
  pending.resolve(quote('selection-1', 713)); await settle(); tree = view.render();
  assert.equal(checkout(tree).props.disabled, false); assert.match(text(tree), /¥7.13/);
  await checkout(tree).props.onClick();
  assert.deepEqual(calls.find(call => call.url === '/orders').body, { cart_version: 7, item_ids: [1], quote_hash: 'selection-1' });
  assert.deepEqual(destinations, ['/console/orders/42']); view.unmount();
});

test('an old response cannot replace a newer selection or enable checkout after a version change', async () => {
  reset(); const first = deferred(), second = deferred(); let n = 0;
  handler = async () => ++n === 1 ? first.promise : second.promise;
  const view = mount(); select(view, 1); view.render(); const firstSignal = calls[0].signal;
  select(view, 2); assert.equal(checkout(view.render()).props.disabled, true);
  assert.equal(firstSignal.aborted, true);
  second.resolve(quote('selection-1-2', 1321)); await settle();
  assert.match(text(view.render()), /¥13.21/);
  first.resolve(quote('stale-selection', 1)); await settle();
  assert.match(text(view.render()), /¥13.21/); assert.doesNotMatch(text(view.render()), /¥0.01/);
  cart = { ...cart, version: 8 };
  assert.equal(checkout(view.render()).props.disabled, true); view.unmount();
});

test('quote failures and missing confirmation hashes block submission until a successful retry', async () => {
  for (const failure of [new Error('分组规则已变更'), { ...quote(), quote_hash: '' }]) {
    reset(); let n = 0;
    handler = async call => { if (call.url === '/orders') return { order_id: 42 }; if (++n === 1) { if (failure instanceof Error) throw failure; return failure; } return quote('retried'); };
    const view = mount(); select(view, 1); view.render(); await settle(); let tree = view.render();
    assert.equal(checkout(tree).props.disabled, true); assert.ok(nodes(tree).some(node => node.props.role === 'alert'));
    await checkout(tree).props.onClick(); assert.equal(calls.some(call => call.url === '/orders'), false);
    nodes(tree).find(node => node.type === Button && text(node) === '重新报价').props.onClick();
    tree = view.render(); assert.equal(checkout(tree).props.disabled, true); await settle();
    assert.equal(checkout(view.render()).props.disabled, false); view.unmount();
  }
});

test('an unknown checkout result preserves the original hash, request body and idempotency key for retry', async () => {
  reset(); let submissions = 0;
  handler = async call => { if (call.url === '/cart/quote') return quote('frozen-unknown-result'); if (++submissions === 1) throw new TypeError('network failed'); return { order_id: 42 }; };
  const view = mount(); select(view, 1); view.render(); await settle();
  await checkout(view.render()).props.onClick();
  await checkout(view.render()).props.onClick();
  const writes = calls.filter(call => call.url === '/orders');
  assert.equal(writes.length, 2); assert.deepEqual(writes[0].body, writes[1].body);
  assert.equal(writes[0].headers['Idempotency-Key'], writes[1].headers['Idempotency-Key']);
  assert.equal(writes[0].body.quote_hash, 'frozen-unknown-result'); view.unmount();
});
