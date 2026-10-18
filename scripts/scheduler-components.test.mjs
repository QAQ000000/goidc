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
let resourcePaths;
let refreshes;
const react = {
  ...React,
  useState(initial) {
    const state = current;
    const index = state.cursor++;
    if (!(index in state.values)) state.values[index] = typeof initial === 'function' ? initial() : initial;
    return [state.values[index], next => { state.values[index] = typeof next === 'function' ? next(state.values[index]) : next; }];
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
class ApiError extends Error {}
function load(filename) {
  if (cache.has(filename)) return cache.get(filename).exports;
  const module = new Module(filename);
  cache.set(filename, module);
  module.filename = filename;
  module.paths = Module._nodeModulePaths(path.dirname(filename));
  const external = createRequire(filename);
  module.require = specifier => {
    if (specifier === 'react') return react;
    if (specifier === './workspace') return { Workspace: ({ children }) => children };
    if (specifier === './admin-pagination') return { AdminPagination: () => null };
    if (specifier === '@/hooks/use-resource') return { useResource: (url, keepPreviousData) => {
      resourcePaths.push({ url, keepPreviousData });
      return { data: resources.get(url)?.data ?? null, error: resources.get(url)?.error ?? null, refresh: () => refreshes.push(url) };
    } };
    if (specifier === '@/lib/api') return { ApiError, date: value => `日期 ${value}` };
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
const { AdminSchedulerLogs } = load(path.join(web, 'src/components/admin-scheduler-logs.tsx'));
const { Failure, Loading } = load(path.join(web, 'src/components/common.tsx'));
const { Button } = load(path.join(web, 'src/components/ui/button.tsx'));
const { TableCell } = load(path.join(web, 'src/components/ui/table.tsx'));
const logPath = '/admin/logs/jobs?';
const trendPath = '/admin/logs/jobs/trend?';
const task = { key: 'custom_scheduled_job', name: '来自任务目录的名称' };
const run = { id: 17, task_key: task.key, started_at: '2026-10-18T10:00:00Z', finished_at: null, duration_ms: null, status: 'running', affected_count: null, error_code: '' };
const trend = { items: [{ day: '2026-10-18', total: 3, succeeded: 1, failed: 1, running: 1, interrupted: 0 }] };
function reset(search = '') {
  resources = new Map([
    [logPath, { data: { items: [run], total: 1, tasks: [task] } }],
    [trendPath, { data: trend }],
  ]);
  resourcePaths = [];
  refreshes = [];
  globalThis.window = {
    location: { pathname: '/admin/logs/jobs', search },
    history: { replaceState(_state, _title, url) { window.location.search = new URL(url, 'https://test.invalid').search; } },
    addEventListener() {}, removeEventListener() {},
  };
}
function mount() {
  const state = { cursor: 0, values: [], effects: [] };
  const render = () => {
    state.cursor = 0;
    current = state;
    let tree;
    try { tree = AdminSchedulerLogs(); } finally { current = undefined; }
    for (const effect of state.effects.splice(0)) effect();
    return tree;
  };
  render();
  return render;
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
const taskSelect = tree => find(tree, item => item.type === 'select' && item.props['aria-label'] === '任务类别');
const taskOptions = tree => nodes(taskSelect(tree)).filter(item => item.type === 'option').map(item => ({ key: item.props.value, name: text(item) }));
const cell = (tree, value) => find(tree, item => item.type === TableCell && text(item) === value);

test('task options and row labels follow the returned catalog, including name changes', () => {
  reset();
  const render = mount();
  assert.deepEqual(taskOptions(render()), [{ key: '', name: '全部任务' }, task]);
  cell(render(), task.name);
  resources.set(logPath, { data: { items: [run], total: 1, tasks: [{ ...task, name: '目录更新后的名称' }, { key: 'second_job', name: '另一任务' }] } });
  assert.deepEqual(taskOptions(render()), [{ key: '', name: '全部任务' }, { key: task.key, name: '目录更新后的名称' }, { key: 'second_job', name: '另一任务' }]);
  cell(render(), '目录更新后的名称');
  assert.doesNotMatch(text(render()), /来自任务目录的名称|推介佣金结算/);
});

test('unknown historical keys remain visible as escaped text without becoming filter choices', () => {
  reset();
  const keys = ['retired_job', '__proto__', 'toString', '<retired-job>'];
  resources.set(logPath, { data: { items: keys.map((task_key, id) => ({ ...run, id, task_key })), total: keys.length, tasks: [task] } });
  const tree = mount()();
  for (const key of keys) cell(tree, key);
  assert.deepEqual(taskOptions(tree), [{ key: '', name: '全部任务' }, task]);
  const html = renderToStaticMarkup(cell(tree, '<retired-job>'));
  assert.match(html, /&lt;retired-job&gt;/);
  assert.doesNotMatch(html, /<retired-job>/);
});

test('log failure preserves the trend and only retries the failed log request', () => {
  reset();
  const error = new Error('日志读取失败');
  resources.set(logPath, { error });
  const render = mount();
  const failure = find(render(), item => item.type === Failure);
  assert.equal(failure.props.error, error);
  cell(render(), '2026-10-18');
  assert.deepEqual(taskOptions(render()), [{ key: '', name: '全部任务' }]);
  failure.props.retry();
  assert.deepEqual(refreshes, [logPath]);
  resources.set(logPath, { data: { items: [run], total: 1, tasks: [task] } });
  assert.equal(nodes(render()).filter(item => item.type === Failure).length, 0);
  cell(render(), task.name);
});

test('trend failure preserves metadata and log rows and only retries the trend request', () => {
  reset();
  const error = new Error('趋势读取失败');
  resources.set(trendPath, { error });
  const render = mount();
  const failure = find(render(), item => item.type === Failure);
  assert.equal(failure.props.error, error);
  cell(render(), task.name);
  assert.deepEqual(taskOptions(render()), [{ key: '', name: '全部任务' }, task]);
  failure.props.retry();
  assert.deepEqual(refreshes, [trendPath]);
});

test('loading either resource keeps the other resource visible', () => {
  for (const missing of [logPath, trendPath]) {
    reset();
    resources.delete(missing);
    const tree = mount()();
    assert.equal(nodes(tree).filter(item => item.type === Loading).length, 1);
    assert.equal(nodes(tree).filter(item => item.type === Failure).length, 0);
    cell(tree, missing === logPath ? '2026-10-18' : task.name);
  }
});

test('the selected task stays visible while metadata loads or fails, then takes its returned name', () => {
  reset(`?task=${task.key}`);
  const filteredLogPath = `${logPath}task=${task.key}`;
  const filteredTrendPath = `${trendPath}task=${task.key}`;
  resources.set(filteredTrendPath, { data: trend });
  const render = mount();
  const expected = [{ key: '', name: '全部任务' }, { key: task.key, name: `当前任务：${task.key}` }];
  assert.equal(taskSelect(render()).props.value, task.key);
  assert.deepEqual(taskOptions(render()), expected);
  assert.equal(nodes(render()).filter(item => item.type === Loading).length, 1);
  cell(render(), '2026-10-18');
  resources.set(filteredLogPath, { error: new Error('日志暂不可用') });
  assert.deepEqual(taskOptions(render()), expected);
  find(render(), item => item.type === Failure).props.retry();
  assert.deepEqual(refreshes, [filteredLogPath]);
  resources.set(filteredLogPath, { data: { items: [run], total: 1, tasks: [task] } });
  assert.deepEqual(taskOptions(render()), [{ key: '', name: '全部任务' }, task]);
  assert.equal(taskSelect(render()).props.value, task.key);
  assert.doesNotMatch(text(taskSelect(render())), /当前任务：/);
  assert.equal(new URLSearchParams(window.location.search).get('task'), task.key);
});

test('a selected historical task absent from the returned catalog remains identifiable and clearable', () => {
  reset('?task=retired_job');
  const filteredLogPath = `${logPath}task=retired_job`;
  resources.set(filteredLogPath, { data: { items: [{ ...run, task_key: 'retired_job' }], total: 1, tasks: [task] } });
  const render = mount();
  assert.equal(taskSelect(render()).props.value, 'retired_job');
  assert.deepEqual(taskOptions(render()), [{ key: '', name: '全部任务' }, { key: 'retired_job', name: '当前任务：retired_job' }, task]);
  taskSelect(render()).props.onChange({ target: { value: '' } });
  assert.equal(new URLSearchParams(window.location.search).has('task'), false);
  assert.equal(taskSelect(render()).props.value, '');
  assert.deepEqual(taskOptions(render()), [{ key: '', name: '全部任务' }, task]);
});

test('metadata task selection preserves other filters, resets pagination and refreshes both resources', () => {
  reset('?status=failed&offset=50');
  resources.set('/admin/logs/jobs?status=failed&offset=50', resources.get(logPath));
  resources.set('/admin/logs/jobs/trend?status=failed&offset=50', resources.get(trendPath));
  const render = mount();
  assert.deepEqual(taskOptions(render()), [{ key: '', name: '全部任务' }, task]);
  taskSelect(render()).props.onChange({ target: { value: task.key } });
  const tree = render();
  const query = new URLSearchParams(window.location.search);
  assert.equal(query.get('task'), task.key);
  assert.equal(query.get('status'), 'failed');
  assert.equal(query.has('offset'), false);
  assert.equal(taskSelect(tree).props.value, task.key);
  const expected = [
    { url: `/admin/logs/jobs?${query}`, keepPreviousData: false },
    { url: `/admin/logs/jobs/trend?${query}`, keepPreviousData: false },
  ];
  assert.deepEqual(resourcePaths.slice(-2), expected);
  find(tree, item => item.type === Button && text(item) === '刷新').props.onClick();
  assert.deepEqual(refreshes, expected.map(item => item.url));
});
