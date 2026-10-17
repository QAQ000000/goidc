import assert from 'node:assert/strict';
import { readFileSync, existsSync } from 'node:fs';
import { createRequire, Module } from 'node:module';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

// Render the actual shared UI without starting the project or adding a runner.
const web = fileURLToPath(new URL('../web/', import.meta.url));
const require = createRequire(path.join(web, 'package.json'));
const ts = require('typescript');
const React = require('react');
const { renderToStaticMarkup } = require('react-dom/server');
const cache = new Map();
function load(filename) {
  if (cache.has(filename)) return cache.get(filename).exports;
  const module = new Module(filename);
  cache.set(filename, module);
  module.filename = filename;
  module.paths = Module._nodeModulePaths(path.dirname(filename));
  const external = createRequire(filename);
  module.require = specifier => {
    if (specifier.startsWith('.') || specifier.startsWith('@/')) {
      const base = specifier.startsWith('@/') ? path.join(web, 'src', specifier.slice(2)) : path.resolve(path.dirname(filename), specifier);
      const source = [base, `${base}.tsx`, `${base}.ts`].find(candidate => existsSync(candidate));
      if (source && /\.tsx?$/.test(source)) return load(source);
    }
    return external(specifier);
  };
  module._compile(ts.transpileModule(readFileSync(filename, 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true } }).outputText, filename);
  return module.exports;
}
const { ResourceTable, ResourcePagination, DetailFields } = load(path.join(web, 'src/components/resource-table.tsx'));
const { MutationDialog } = load(path.join(web, 'src/components/mutation-dialog.tsx'));
const render = (component, props) => renderToStaticMarkup(React.createElement(component, props));

test('resource table renders conditional columns, escaped cells and matching empty span', () => {
  for (const admin of [false, true]) {
    const props = { headers: ['编号', ...(admin ? ['客户'] : []), '状态'], rowKey: row => row.id, renderRow: row => React.createElement('td', null, row.value), empty: '无申请' };
    const empty = render(ResourceTable, { ...props, items: [] });
    assert.match(empty, new RegExp(`colSpan="${admin ? 3 : 2}"`, 'i'));
    assert.match(empty, /无申请/);
    const populated = render(ResourceTable, { ...props, items: [{ id: 7, value: '<private>' }] });
    assert.match(populated, /&lt;private&gt;/);
    assert.doesNotMatch(populated, /无申请/);
  }
});

test('detail fields omit absent values and retain numeric zero', () => {
  const html = render(DetailFields, { fields: { 数量: 0, 空白: '', 未填: undefined, 账号: 'A&B' } });
  assert.match(html, /数量/); assert.match(html, />0</); assert.match(html, /A&amp;B/);
  assert.doesNotMatch(html, /空白|未填/);
});

test('pagination respects first, middle, last and empty pages and bounded previous offset', () => {
  for (const [total, offset, previous, next] of [[0, 0, true, true], [45, 0, true, false], [45, 20, false, false], [45, 40, false, true]]) {
    let target;
    const props = { total, offset, limit: 20, unit: '申请', onOffsetChange: value => { target = value; } };
    const element = ResourcePagination(props);
    const buttons = React.Children.toArray(element.props.children).at(-1).props.children;
    assert.equal(buttons[0].props.disabled, previous);
    assert.equal(buttons[1].props.disabled, next);
    buttons[0].props.onClick(); assert.equal(target, Math.max(0, offset - 20));
    buttons[1].props.onClick(); assert.equal(target, offset + 20);
    assert.match(render(ResourcePagination, props), new RegExp(`${total} 个申请`));
  }
});

test('mutation dialog blocks all dismiss controls while busy and preserves validation', () => {
  for (const busy of [false, true]) {
    let closed = 0;
    const dialog = MutationDialog({ open: true, busy, onClose: () => closed++, title: '确认', children: '资料', confirm: '提交', onConfirm: () => {}, confirmDisabled: true, showCloseButton: !busy });
    dialog.props.onOpenChange(true); assert.equal(closed, 0);
    dialog.props.onOpenChange(false); assert.equal(closed, busy ? 0 : 1);
    const content = dialog.props.children;
    assert.equal(content.props.showCloseButton, !busy);
    const footer = React.Children.toArray(content.props.children).at(-1);
    const [cancel, confirm] = footer.props.children;
    assert.equal(cancel.props.disabled, busy);
    assert.equal(confirm.props.disabled, true);
  }
});

test('column tables share headers and cells with correct formatting and empty span', () => {
  const columns = [{ header: '编号', render: row => row.id }, { header: '金额', className: 'money', render: row => `¥${row.amount}` }];
  const props = { columns, rowKey: row => row.id };
  const empty = render(ResourceTable, { ...props, items: [] });
  assert.match(empty, /colSpan="2"/i);
  const html = render(ResourceTable, { ...props, items: [{ id: 9, amount: 42 }] });
  assert.match(html, /编号/); assert.match(html, /金额/); assert.match(html, /money/); assert.match(html, /¥42/);
});
