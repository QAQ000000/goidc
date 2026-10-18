import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { createRequire, Module } from 'node:module';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

// Exercise the actual TypeScript helpers and API paths with a small browser
// fixture, following the existing component tests without adding a runner.
const web = fileURLToPath(new URL('../web/', import.meta.url));
const require = createRequire(path.join(web, 'package.json'));
const ts = require('typescript');
function loader(stubs = new Map()) {
  const cache = new Map();
  return function load(filename) {
    if (cache.has(filename)) return cache.get(filename).exports;
    const module = new Module(filename);
    cache.set(filename, module);
    module.filename = filename;
    module.paths = Module._nodeModulePaths(path.dirname(filename));
    const external = createRequire(filename);
    module.require = specifier => {
      if (stubs.has(specifier)) return stubs.get(specifier);
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
    const errors = (compiled.diagnostics ?? []).filter(item => item.category === ts.DiagnosticCategory.Error);
    assert.deepEqual(errors.map(item => ts.flattenDiagnosticMessageText(item.messageText, '\n')), []);
    module._compile(compiled.outputText, filename);
    return module.exports;
  };
}
const load = loader();
const { downloadFilename, saveBlob } = load(path.join(web, 'src/lib/download.ts'));
const client = load(path.join(web, 'src/lib/api.ts'));

function browser(t, options = {}) {
  const state = { blobs: [], revoked: [], anchors: [], attached: new Set(), timers: [], events: [], navigations: [] };
  for (const name of ['document', 'window']) {
    const previous = Object.getOwnPropertyDescriptor(globalThis, name);
    t.after(() => previous ? Object.defineProperty(globalThis, name, previous) : delete globalThis[name]);
  }
  globalThis.document = {
    createElement(tag) {
      assert.equal(tag, 'a');
      if (options.createError) throw options.createError;
      const anchor = {
        href: '', download: '', clicks: 0, removed: false,
        click() {
          assert(state.attached.has(anchor), 'the anchor is attached before clicking');
          anchor.clicks++;
          if (options.clickError) throw options.clickError;
        },
        remove() { anchor.removed = true; state.attached.delete(anchor); },
      };
      state.anchors.push(anchor);
      return anchor;
    },
    body: { appendChild(anchor) { state.attached.add(anchor); } },
  };
  globalThis.window = {
    setTimeout(callback, ms) { state.timers.push({ callback, ms }); },
    dispatchEvent(event) { state.events.push(event); },
    location: { assign(url) { state.navigations.push(url); } },
  };
  t.mock.method(URL, 'createObjectURL', blob => { state.blobs.push(blob); return `blob:download-${state.blobs.length}`; });
  t.mock.method(URL, 'revokeObjectURL', url => { state.revoked.push(url); });
  return state;
}

test('download names prefer RFC5987 UTF-8 with optional language and preserve literal percent names', () => {
  assert.equal(downloadFilename('attachment; filename="fallback.html"; filename*=UTF-8\'zh-CN\'%E5%90%88%E5%90%8C%20%231.html', 'item.html'), '合同 #1.html');
  assert.equal(downloadFilename("attachment; FILENAME*=utf-8''%E6%8A%A5%E8%A1%A8.csv; filename=regular.csv"), '报表.csv');
  assert.equal(downloadFilename('attachment; filename="budget; Q1.csv"'), 'budget; Q1.csv');
  assert.equal(downloadFilename('attachment; filename="report%20.csv"'), 'report%20.csv');
  assert.equal(downloadFilename('attachment; filename="a\\"b.csv"'), 'a"b.csv');
});

test('malformed or unsupported extended names fall back to the ordinary name then caller name', () => {
  for (const extended of ["UTF-8''%ZZ", "UTF-8''%E4", "ISO-8859-1''%A3.csv", "UTF-8''", 'not-an-extended-value']) {
    assert.equal(downloadFilename(`attachment; filename*=\"${extended}\"; filename="regular.csv"`, 'item.csv'), 'regular.csv');
    assert.equal(downloadFilename(`attachment; filename*=${extended}`, 'item.csv'), 'item.csv');
  }
  assert.equal(downloadFilename('attachment; filename="unterminated', 'item.csv'), 'item.csv');
  assert.equal(downloadFilename(null, 'item.csv'), 'item.csv');
  assert.equal(downloadFilename('attachment; filename=""', ''), 'download');
});

test('download names cannot retain path components or control characters', () => {
  assert.equal(downloadFilename("attachment; filename*=UTF-8''..%2Fprivate%2Freport.csv"), 'report.csv');
  assert.equal(downloadFilename(null, '..\\private\\report.csv'), 'report.csv');
  assert.equal(downloadFilename("attachment; filename*=UTF-8''%0D%0A%00report.csv"), 'report.csv');
  assert.equal(downloadFilename('attachment; filename=".."', 'fallback.csv'), 'fallback.csv');
});

test('saving a blob removes its temporary anchor and defers releasing the URL', t => {
  const state = browser(t);
  const blob = new Blob(['content'], { type: 'text/csv' });
  saveBlob(blob, 'report.csv');
  assert.deepEqual(state.blobs, [blob]);
  assert.equal(state.anchors[0].download, 'report.csv');
  assert.equal(state.anchors[0].clicks, 1);
  assert.equal(state.anchors[0].removed, true);
  assert.equal(state.attached.size, 0);
  assert.deepEqual(state.revoked, [], 'never revoke synchronously after clicking');
  assert.equal(state.timers.length, 1);
  assert.ok(state.timers[0].ms >= 1000);
  state.timers[0].callback();
  assert.deepEqual(state.revoked, ['blob:download-1']);
});

test('click and element creation failures propagate while still releasing all resources', t => {
  const failure = new Error('click blocked');
  const state = browser(t, { clickError: failure });
  assert.throws(() => saveBlob(new Blob(['content']), 'report.csv'), error => error === failure);
  assert.equal(state.anchors[0].removed, true);
  assert.equal(state.attached.size, 0);
  assert.deepEqual(state.revoked, []);
  state.timers[0].callback();
  assert.deepEqual(state.revoked, ['blob:download-1']);
});

test('element creation failure also schedules URL cleanup without hiding the error', t => {
  const failure = new Error('DOM unavailable');
  const state = browser(t, { createError: failure });
  assert.throws(() => saveBlob(new Blob(['content']), 'report.csv'), error => error === failure);
  assert.equal(state.anchors.length, 0);
  assert.equal(state.timers.length, 1);
  state.timers[0].callback();
  assert.deepEqual(state.revoked, ['blob:download-1']);
});

test('fetchDownload retains authorized fetch options and uses a server filename with fallback', async t => {
  const state = browser(t);
  const requests = [];
  const headers = ["attachment; filename*=UTF-8''%E6%96%87%E4%BB%B6.pdf", "attachment; filename*=UTF-8''%ZZ"];
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    requests.push({ url, options });
    return new Response('bytes', { headers: { 'Content-Type': 'application/pdf', 'Content-Disposition': headers.shift() } });
  });
  await client.fetchDownload({ id: 7, filename: 'item.pdf' }, '/customer-files/7/file');
  await client.fetchDownload({ id: 8, filename: 'fallback.pdf' });
  assert.deepEqual(requests.map(item => item.url), ['/api/v1/customer-files/7/file', '/api/v1/downloads/8/file']);
  for (const { options } of requests) assert.deepEqual(options, { credentials: 'same-origin', cache: 'no-store', headers: { 'X-Requested-With': 'zjmfgo' } });
  assert.deepEqual(state.anchors.map(item => item.download), ['文件.pdf', 'fallback.pdf']);
  assert.deepEqual(await Promise.all(state.blobs.map(blob => blob.text())), ['bytes', 'bytes']);
});

test('remote downloads keep their HTTP(S) URL branch and reject credentials or unsafe schemes', async t => {
  const state = browser(t);
  let target = 'https://files.example.test/report.csv';
  t.mock.method(globalThis, 'fetch', async () => Response.json({ data: { url: target } }));
  await client.fetchDownload({ id: 7, filename: 'report.csv' });
  assert.deepEqual(state.navigations, [target]);
  for (target of ['javascript:alert(1)', 'https://user:secret@files.example.test/report.csv']) {
    await assert.rejects(client.fetchDownload({ id: 7, filename: 'report.csv' }), /外部下载地址无效/);
  }
  assert.equal(state.blobs.length, 0);
  assert.equal(state.anchors.length, 0);
});

for (const [name, invoke, defaultCode, defaultMessage] of [
  ['download', () => client.download('/tickets/7/attachments/8'), 'request_failed', '下载失败，请稍后重试'],
  ['fetchDownload', () => client.fetchDownload({ id: 7, filename: 'item.csv' }), 'download_failed', '下载失败'],
]) {
  test(`${name} preserves unauthorized/maintenance events and its own default errors`, async t => {
    const state = browser(t);
    let response = Response.json({ error: { code: 'unauthorized', message: '请先登录' } }, { status: 401 });
    t.mock.method(globalThis, 'fetch', async () => response);
    await assert.rejects(invoke(), error => error instanceof client.ApiError && error.status === 401 && error.code === 'unauthorized');
    assert.equal(state.events[0].type, 'zjmfgo:unauthorized');
    response = Response.json({ error: { code: 'site_maintenance', message: '维护中' } }, { status: 503 });
    await assert.rejects(invoke(), error => error.code === 'site_maintenance');
    assert.equal(state.events[1].type, 'zjmfgo:maintenance');
    response = new Response('not JSON', { status: 500 });
    await assert.rejects(invoke(), error => error.code === defaultCode && error.message === defaultMessage);
    assert.equal(state.blobs.length, 0);
    assert.equal(state.anchors.length, 0);
  });
}

function reportExport(t, download) {
  const state = { cleanups: [], updates: [] };
  const hooks = {
    useState: initial => [initial, value => { state.updates.push(value); }],
    useRef: current => ({ current }),
    useLayoutEffect: effect => { state.cleanups.push(effect()); },
  };
  const { ReportExport } = loader(new Map([
    ['react', hooks], ['@/lib/api', { download }],
    ['@/components/ui/button', { Button: () => null }],
    ['./common', {}], ['lucide-react', { Download: () => null }],
  ]))(path.join(web, 'src/components/report-shared.tsx'));
  const element = ReportExport({ path: '/admin/reports/daily?year=2026', filename: 'daily.csv', disabled: false });
  state.click = element.props.children[0].props.onClick;
  t.after(() => state.cleanups.forEach(cleanup => cleanup?.()));
  return state;
}

test('report export retains its pending lock and invalidates a download after unmount/filter change', async t => {
  const state = browser(t);
  let finish;
  let requests = 0;
  const report = reportExport(t, url => {
    requests++;
    assert.equal(url, '/admin/reports/daily?year=2026&format=csv');
    return new Promise(resolve => { finish = resolve; });
  });
  report.click(); report.click();
  assert.equal(requests, 1);
  report.cleanups[0]();
  finish(new Blob(['date'], { type: 'text/csv' }));
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(state.blobs.length, 0);
});

test('report export keeps MIME errors retryable and saves only the subsequent CSV response', async t => {
  const state = browser(t);
  let requests = 0;
  const report = reportExport(t, async () => new Blob(['data'], { type: ++requests === 1 ? 'application/json' : 'text/csv' }));
  report.click();
  await new Promise(resolve => setImmediate(resolve));
  assert(report.updates.some(value => typeof value === 'string' && value.includes('CSV')));
  assert.equal(state.blobs.length, 0);
  report.click();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(state.anchors[0].download, 'daily.csv');
  assert.equal(state.anchors[0].removed, true);
  assert.equal(state.revoked.length, 0);
});
