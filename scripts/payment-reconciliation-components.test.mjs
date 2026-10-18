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
let requests;
let refreshes;
let respond;
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
    if (specifier === './workspace') return { useAdminPermission: permission => permissions.has(permission) };
    if (specifier === '@/lib/api') return {
      date: value => `日期 ${value}`,
      api: async (url, options = {}) => {
        const request = { url, method: options.method ?? 'GET' };
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
  const compiled = ts.transpileModule(readFileSync(filename, 'utf8'), {
    reportDiagnostics: true,
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true },
  });
  assert.deepEqual((compiled.diagnostics ?? []).filter(item => item.category === ts.DiagnosticCategory.Error), []);
  module._compile(compiled.outputText, filename);
  return module.exports;
}
const { PaymentReconciliation } = load(path.join(web, 'src/components/payment-reconciliation.tsx'));
const { Button } = load(path.join(web, 'src/components/ui/button.tsx'));
const payment = {
  id: 17, provider: 'custom_card', provider_payment_id: 'frozen-reference',
  status: 'pending', expired: false, amount_cents: 1000,
  created_at: '2026-10-18T10:00:00Z', completed_at: null, query_enabled: true,
};
function reset() {
  permissions = new Set(['payments.reconcile']);
  requests = [];
  refreshes = 0;
  respond = async () => ({ status: 'pending' });
}
function mount(props = {}) {
  const values = { invoiceId: 42, payment: { ...payment }, admin: false, refresh: () => { refreshes++; }, ...props };
  const state = { cursor: 0, values: [] };
  const render = () => {
    state.cursor = 0;
    current = state;
    try { return PaymentReconciliation(values); } finally { current = undefined; }
  };
  return { props: values, render };
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
const buttons = tree => nodes(tree).filter(item => item.type === Button);
function button(tree) { const result = buttons(tree)[0]; assert.ok(result, 'expected reconciliation control'); return result; }
function feedback(tree, role) { return nodes(tree).find(item => item.props?.role === role); }

test('a non-Epay method with query capability uses the customer reconciliation API', async () => {
  reset();
  permissions.clear();
  const { render } = mount();
  assert.equal(button(render()).props.disabled, false);
  await button(render()).props.onClick();
  assert.deepEqual(requests, [{ url: '/invoices/42/payments/17/reconcile', method: 'POST' }]);
  assert.equal(refreshes, 1);
  assert.match(text(feedback(render(), 'status')), /渠道尚未确认付款.*原支付单继续保留/);
  assert.doesNotMatch(text(render()), /付款已确认/);
});

test('an authorized administrator can reconcile an expired payment through the admin API', async () => {
  reset();
  respond = async () => ({ status: 'succeeded' });
  const { render } = mount({ admin: true, payment: { ...payment, status: 'expired' } });
  await button(render()).props.onClick();
  assert.deepEqual(requests, [{ url: '/admin/invoices/42/payments/17/reconcile', method: 'POST' }]);
  assert.match(text(feedback(render(), 'status')), /付款已确认，账单已更新/);
  assert.equal(refreshes, 1);
});

test('the built-in mock stays hidden without imposing a naming rule on other implementations', () => {
  reset();
  assert.equal(mount({ payment: { ...payment, provider: 'mock' } }).render(), null);
  assert.equal(button(mount({ payment: { ...payment, provider: 'mock_external' } }).render()).props.disabled, false);
  assert.deepEqual(requests, []);
});

test('missing capability and unavailable original configuration cannot expose a usable query control', () => {
  for (const [query_enabled, expected] of [
    [undefined, /尚未取得支付查询能力.*刷新账单/],
    [false, /未启用查询或原支付配置不可用.*管理员核验/],
  ]) {
    reset();
    const { render } = mount({ payment: { ...payment, query_enabled, next_reconcile_at: '2026-10-18T11:00:00Z' } });
    assert.equal(buttons(render()).length, 0);
    assert.match(text(render()), expected);
    assert.doesNotMatch(text(render()), /下次自动查询/);
    assert.deepEqual(requests, []);
  }
});

test('permission changes remove the admin action and explain its absence', () => {
  reset();
  const { render } = mount({ admin: true });
  assert.equal(buttons(render()).length, 1);
  permissions.clear();
  assert.equal(buttons(render()).length, 0);
  assert.match(text(render()), /缺少支付查询权限/);
  assert.deepEqual(requests, []);
  permissions.add('payments.reconcile');
  assert.equal(buttons(render()).length, 1);
});

test('terminal payments retain query history without exposing a new reconciliation action', () => {
  for (const status of ['succeeded', 'closed', 'failed']) {
    reset();
    const tree = mount({ payment: { ...payment, status, checked_at: '2026-10-18T10:01:00Z', check_source: 'worker', check_status: 'succeeded' } }).render();
    assert.equal(buttons(tree).length, 0);
    assert.match(text(tree), /自动查询 · 日期 2026-10-18T10:01:00Z · 付款已确认/);
    assert.doesNotMatch(text(tree), /待人工核对|未启用查询/);
  }
});

test('busy and blocked controls reject clicks, including synchronous duplicate submissions', async () => {
  reset();
  let finish;
  respond = () => new Promise(resolve => { finish = resolve; });
  const { render } = mount();
  const initial = button(render());
  const pending = initial.props.onClick();
  await initial.props.onClick();
  assert.equal(requests.length, 1);
  assert.equal(button(render()).props.disabled, true);
  assert.match(text(button(render())), /查询中/);
  finish({ status: 'pending' });
  await pending;
  assert.equal(refreshes, 1);
  assert.equal(button(render()).props.disabled, false);

  for (const flags of [{ check_status: 'checking' }, { close_status: 'checking' }, { close_anomaly: true }]) {
    reset();
    const blocked = mount({ payment: { ...payment, ...flags } });
    const control = button(blocked.render());
    assert.equal(control.props.disabled, true);
    await control.props.onClick();
    assert.deepEqual(requests, []);
    assert.equal(refreshes, 0);
  }
});

test('an unknown close result still allows a payment query while the payment entry stays frozen', async () => {
  reset();
  const { render } = mount({ payment: { ...payment, close_frozen: true, close_status: 'unknown' } });
  assert.equal(button(render()).props.disabled, false);
  assert.match(text(render()), /未知结果继续冻结/);
  await button(render()).props.onClick();
  assert.equal(requests.length, 1);
});

test('unknown responses and request failures preserve uncertainty, refresh facts and allow retry', async () => {
  reset();
  const { render } = mount();
  respond = async () => ({ status: 'unexpected' });
  await button(render()).props.onClick();
  assert.match(text(feedback(render(), 'status')), /未返回可确认的付款结果.*原支付单继续保留/);
  assert.doesNotMatch(text(feedback(render(), 'status')), /付款已确认/);
  respond = async () => { throw new Error('<渠道查询未确认>，请稍后重试'); };
  await button(render()).props.onClick();
  assert.equal(feedback(render(), 'status'), undefined);
  assert.match(text(feedback(render(), 'alert')), /<渠道查询未确认>/);
  const html = renderToStaticMarkup(render());
  assert.match(html, /&lt;渠道查询未确认&gt;/);
  assert.doesNotMatch(html, /<渠道查询未确认>/);
  assert.equal(button(render()).props.disabled, false);
  respond = async () => ({ status: 'expired' });
  await button(render()).props.onClick();
  assert.equal(feedback(render(), 'alert'), undefined);
  assert.match(text(feedback(render(), 'status')), /渠道尚未确认付款/);
  assert.equal(requests.length, 3);
  assert.equal(refreshes, 3);
});

test('query scheduling and exhaustion keep their distinct meanings alongside historical evidence', () => {
  reset();
  const { props, render } = mount({ payment: { ...payment, checked_at: '2026-10-18T10:01:00Z', check_source: 'manual', check_status: 'error', next_reconcile_at: '2026-10-18T11:00:00Z' } });
  assert.match(text(render()), /手动查询 · 日期 2026-10-18T10:01:00Z · 上次查询未确认结果/);
  assert.match(text(render()), /下次自动查询：日期 2026-10-18T11:00:00Z/);
  props.payment = { ...props.payment, reconciliation_exhausted: true };
  assert.match(text(render()), /自动查询次数已用完，待人工核对/);
  assert.doesNotMatch(text(render()), /下次自动查询/);
  assert.equal(button(render()).props.disabled, false);
});
