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
let runtimeResource;
let refreshes;
const cache = new Map();
function load(filename) {
  if (cache.has(filename)) return cache.get(filename).exports;
  const module = new Module(filename);
  cache.set(filename, module);
  module.filename = filename;
  module.paths = Module._nodeModulePaths(path.dirname(filename));
  const external = createRequire(filename);
  module.require = specifier => {
    if (specifier === '@/hooks/use-resource') return { useResource: (url, keepPreviousData) => {
      assert.equal(url, '/admin/runtime');
      assert.equal(keepPreviousData, false);
      return { ...runtimeResource, refresh: () => { refreshes++; } };
    } };
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
const client = load(path.join(web, 'src/lib/api.ts'));
const { WorkerRuntimePanel } = load(path.join(web, 'src/components/worker-runtime.tsx'));
const { Failure, Loading } = load(path.join(web, 'src/components/common.tsx'));
const { Button } = load(path.join(web, 'src/components/ui/button.tsx'));
function nodes(element) {
  if (!element || typeof element !== 'object') return [];
  if (Array.isArray(element)) return element.flatMap(nodes);
  return [element, ...React.Children.toArray(element.props?.children).flatMap(nodes)];
}

for (const operation of ['api', 'upload', 'download', 'fetchDownload']) {
  test(`${operation} preserves a validated server correlation ID on failure`, async t => {
    const id = '0123456789abcdef0123456789abcdef';
    t.mock.method(globalThis, 'fetch', async () => new Response(JSON.stringify({ error: { code: 'unavailable', message: '暂时不可用', correlation_id: id } }), { status: 503, headers: { 'X-Request-ID': id } }));
    await assert.rejects(() => client[operation]('/fixture', operation === 'upload' ? new FormData() : undefined), error => {
      assert(error instanceof client.ApiError);
      assert.equal(error.correlationId, id);
      assert.match(error.message, new RegExp(id));
      return true;
    });
  });
}
test('invalid external IDs are omitted, and old APIs without IDs still work', async t => {
  for (const id of ['not-safe-id', 'x'.repeat(4096), null]) {
    t.mock.method(globalThis, 'fetch', async () => new Response(JSON.stringify({ error: { code: 'invalid', message: '校验失败' } }), { status: 400, headers: id === null ? {} : { 'X-Request-ID': id } }));
    await assert.rejects(() => client.api('/fixture'), error => error.correlationId === undefined && error.message === '校验失败');
  }
});
test('runtime errors and loading have independent retry states', () => {
  refreshes = 0;
  runtimeResource = { data: null, error: null };
  assert(nodes(WorkerRuntimePanel()).some(node => node.type === Loading));
  runtimeResource.error = new Error('诊断读取失败');
  const failure = nodes(WorkerRuntimePanel()).find(node => node.type === Failure);
  assert(failure);
  failure.props.retry();
  assert.equal(refreshes, 1);
  const button = nodes(WorkerRuntimePanel()).find(node => node.type === Button);
  button.props.onClick();
  assert.equal(refreshes, 2);
});
test('runtime renders due/future work and missing or stale execution lanes', () => {
  runtimeResource = { error: null, data: {
    status: 'degraded', observed_at: '2026-10-19T12:00:00Z', recent_window_seconds: 3600,
    missing_lanes: ['auto_renew'],
    queues: [{ name: 'email', runnable_pending: 2, future_pending: 3, oldest_waiting_seconds: 410, running: 1, expired_leases: 1, recent_failures: 0, recent_unknown: 1, recent_dead_letters: 2 }],
    lanes: [{ instance_id: 'abc123456789', name: 'auto_renew', state: 'running', status: 'stalled', progress_at: '2026-10-19T11:00:00Z', completed_passes: 7, stall_after_seconds: 300 }],
  } };
  const html = renderToStaticMarkup(React.createElement(WorkerRuntimePanel));
  for (const word of ['存在需检查', '尚未到期', '410', '自动续费', '执行无进展', '过期租约', '60 分钟', '0 / 1 / 2']) assert(html.includes(word), word);
});
