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

function loader(overrides = {}) {
  const cache = new Map();
  return function load(filename) {
    if (cache.has(filename)) return cache.get(filename).exports;
    const module = new Module(filename);
    cache.set(filename, module);
    module.filename = filename;
    module.paths = Module._nodeModulePaths(path.dirname(filename));
    const external = createRequire(filename);
    module.require = specifier => {
      if (Object.hasOwn(overrides, specifier)) return overrides[specifier];
      if (specifier.startsWith('.') || specifier.startsWith('@/')) {
        const base = specifier.startsWith('@/') ? path.join(web, 'src', specifier.slice(2)) : path.resolve(path.dirname(filename), specifier);
        const source = [base, `${base}.tsx`, `${base}.ts`].find(candidate => existsSync(candidate));
        if (source && /\.tsx?$/.test(source)) return load(source);
      }
      return external(specifier);
    };
    module._compile(ts.transpileModule(readFileSync(filename, 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true } }).outputText, filename);
    return module.exports;
  };
}

const load = loader();
const { AdminListPage, AdminListRegion } = load(path.join(web, 'src/components/admin-list-page.tsx'));
const { ResourceTable } = load(path.join(web, 'src/components/resource-table.tsx'));
const { ApiError } = load(path.join(web, 'src/lib/api.ts'));
const render = (Component, props) => renderToStaticMarkup(React.createElement(Component, props));
const node = (text, tag = 'p') => React.createElement(tag, null, text);

test('standard page preserves domain slots and optional description without taking over mutations', () => {
  const html = render(AdminListPage, {
    title: '人工库存', description: '库存与占用分开管理', actions: node('创建入口'),
    filters: node('查询草稿', 'form'), feedback: node('原请求待核验'),
    children: node('已授权结果'), dialogs: node('独立确认抽屉'),
  });
  const labels = ['人工库存', '创建入口', '查询草稿', '原请求待核验', '已授权结果', '独立确认抽屉'];
  for (let index = 1; index < labels.length; index++) assert.ok(html.indexOf(labels[index]) > html.indexOf(labels[index - 1]));
  assert.match(html, /库存与占用分开管理/);
  const noDescription = render(AdminListPage, { title: '客户', children: null });
  assert.doesNotMatch(noDescription, /<p/);
});

test('list region never renders stale results, counts or pagination after permission or query failure', () => {
  for (const props of [
    { allowed: false },
    { invalid: node('查询参数无效') },
    { resource: { data: { total: 42 }, error: new ApiError(403, 'forbidden', 'private reason'), isLoading: false } },
    { resource: { data: { total: 42 }, error: new Error('区域读取失败'), isLoading: false } },
  ]) {
    let children = 0, pagination = 0;
    const html = render(AdminListRegion, {
      title: '客户结果', summary: '旧汇总42', resource: { data: { total: 42 }, error: null, isLoading: false }, retry() {},
      children() { children++; return node('旧客户信息'); }, pagination() { pagination++; return node('旧分页'); }, ...props,
    });
    assert.equal(children, 0); assert.equal(pagination, 0);
    assert.doesNotMatch(html, /旧汇总42|旧客户信息|旧分页|private reason/);
  }
  const denied = render(AdminListRegion, {
    title: '客户', allowed: false, invalid: node('隐藏查询参数'), resource: { data: null, error: null, isLoading: true }, retry() {}, children: () => null,
  });
  assert.match(denied, /没有查看客户的权限/); assert.doesNotMatch(denied, /隐藏查询参数|正在加载|aria-busy="true"/);
});

test('list region uses administrator login on 401 and never retries writes or exposes stale data', () => {
  const html = render(AdminListRegion, {
    title: '库存', summary: '旧汇总', invalid: '陈旧数据校验失败', resource: { data: ['private'], error: new ApiError(401, 'unauthorized', 'old token'), isLoading: false },
    retry() { throw new Error('must not retry while rendering'); }, children: () => node('旧结果'), pagination: () => node('旧页码'),
  });
  assert.match(html, /href="\/admin-login"/);
  assert.doesNotMatch(html, /href="\/login"|旧结果|旧汇总|旧页码|old token|陈旧数据校验失败/);
});

test('list region preserves successful query content during refresh and keeps initial skeleton local', () => {
  const props = { title: '库存', summary: '共42条', resource: { data: { total: 42 }, error: null, isLoading: true }, retry() {}, children: value => node(`库存数据${value.total}`), pagination: value => node(`页码${value.total}`) };
  const refreshing = render(AdminListRegion, props);
  assert.match(refreshing, /aria-busy="true"/); assert.match(refreshing, /正在刷新/);
  assert.match(refreshing, /共42条/); assert.match(refreshing, /库存数据42/); assert.match(refreshing, /页码42/);
  const initial = render(AdminListRegion, { ...props, resource: { data: null, error: null, isLoading: true } });
  assert.match(initial, /正在加载库存/); assert.doesNotMatch(initial, /共42条|库存数据42|页码42/);
  const zero = render(AdminListRegion, { ...props, resource: { data: 0, error: null, isLoading: false }, children: value => node(`有效值${value}`), pagination: undefined });
  assert.match(zero, /有效值0/); assert.doesNotMatch(zero, /正在加载/);
});

function elements(root, condition) {
  if (Array.isArray(root)) return root.flatMap(child => elements(child, condition));
  if (!root || typeof root !== 'object' || !root.type) return [];
  return [...(condition(root) ? [root] : []), ...elements(root.props.children, condition)];
}

test('retry action invokes only the caller callback and errors hide previous rows until success', () => {
  let retries = 0, rows = 0;
  const root = AdminListRegion({ title: '客户', resource: { data: [1], error: new Error('读取失败'), isLoading: false }, retry() { retries++; }, children() { rows++; return null; } });
  assert.equal(retries, 0); assert.equal(rows, 0);
  const buttons = elements(root, element => typeof element.props.onClick === 'function');
  assert.equal(buttons.length, 1);
  buttons[0].props.onClick(); assert.equal(retries, 1); assert.equal(rows, 0);
});

test('responsive resource table shares records and stable keys, applies column styles and renders one empty state', () => {
  const props = {
    ariaLabel: '库存查询结果', tableClassName: 'min-w-[900px]', headerClassName: 'header-base', rowClassName: row => `inventory-${row.id}`,
    columns: [{ id: 'name', header: '名称', headerClassName: 'name-header', className: 'name-cell', render: row => row.name }],
    items: [{ id: 7, name: '<private>' }, { id: 9, name: '资源9' }], rowKey: row => row.id,
    renderMobile: row => node(`移动资源${row.id}`, 'article'), empty: node('无符合条件的资源'),
  };
  const html = render(ResourceTable, props);
  assert.match(html, /aria-label="库存查询结果"/); assert.match(html, /hidden md:block/); assert.match(html, /md:hidden/);
  assert.match(html, /name-header/); assert.match(html, /header-base/); assert.match(html, /name-cell/); assert.match(html, /inventory-7/);
  assert.match(html, /min-w-\[900px\]/); assert.match(html, /&lt;private&gt;/);
  assert.match(html, /移动资源7/); assert.match(html, /移动资源9/);
  const tree = ResourceTable(props);
  const cards = elements(tree, element => element.props.role === 'listitem');
  assert.deepEqual(cards.map(element => element.key), ['7', '9']);
  const empty = render(ResourceTable, { ...props, items: [] });
  assert.equal(empty.match(/无符合条件的资源/g)?.length, 1);
  assert.doesNotMatch(empty, /<table|<article|<td/);
});

test('controlled pagination bounds page changes and rejects fractional, malformed or unsafe jumps', () => {
  let draft = '', pages = [], sizes = [];
  const paginationLoad = loader({ react: { ...React, useState: () => [draft, value => { draft = value; }] } });
  const { AdminPagination } = paginationLoad(path.join(web, 'src/components/admin-pagination.tsx'));
  const base = { total: 75, page: 1, pageSize: 25, onPageChange: value => pages.push(value), onPageSizeChange: value => sizes.push(value) };
  const renderTree = (props = {}) => AdminPagination({ ...base, ...props });
  const find = (root, label) => elements(root, element => element.props['aria-label'] === label)[0];
  find(renderTree(), '上一页').props.onClick(); find(renderTree(), '下一页').props.onClick();
  assert.deepEqual(pages, [0, 2]); pages = [];
  const first = find(renderTree({ page: 0 }), '上一页'); assert.equal(first.props.disabled, true); first.props.onClick();
  const last = find(renderTree({ page: 2 }), '下一页'); assert.equal(last.props.disabled, true); last.props.onClick();
  assert.deepEqual(pages, []);
  for (const value of ['0', '-1', '1.5', '2abc', '1e1', '4', '9007199254740992']) {
    draft = value; find(renderTree(), '跳转页码').props.onSubmit({ preventDefault() {} });
  }
  assert.deepEqual(pages, []);
  draft = '3'; find(renderTree(), '跳转页码').props.onSubmit({ preventDefault() {} });
  assert.deepEqual(pages, [2]); assert.equal(draft, '');
  const selector = elements(renderTree(), element => typeof element.props.onValueChange === 'function')[0];
  selector.props.onValueChange('50'); assert.deepEqual(sizes, [50]);
  const empty = find(renderTree({ total: 0, page: 0 }), '下一页'); assert.equal(empty.props.disabled, true);
  const html = render(paginationLoad(path.join(web, 'src/components/admin-pagination.tsx')).AdminPagination, { ...base, pageSize: 10, pageSizes: [25, 50] });
  assert.match(html, /aria-label="列表分页"/); assert.match(html, /aria-label="每页记录数"/); assert.match(html, /aria-label="目标页码"/);
  assert.match(html, /10条\/页/); assert.doesNotMatch(html, /\[#[0-9a-f]+\]/i);
});
