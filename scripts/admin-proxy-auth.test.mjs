import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import vm from 'node:vm';
import test from 'node:test';

const require = createRequire(new URL('../web/package.json', import.meta.url));
const ts = require('typescript');
const source = await readFile(new URL('../web/src/proxy.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } });
const publicOrigin = 'https://billing.example.test';

class NextResponse extends Response {
  static next(options) { const response = new NextResponse(null); response.next = true; response.forwarded = options?.request?.headers; return response; }
  static redirect(url, options) { return new NextResponse(null, { ...options, headers: { ...options?.headers, Location: String(url) } }); }
}
function harness(options = {}) {
  const { maintenance = false, maintenanceURL = '', role = 'customer', adminRole = 'admin', meStatus = 200, meError = false, malformed = false, siteError = false } = options;
  const configuredOrigin = Object.hasOwn(options, 'configuredOrigin') ? options.configuredOrigin : publicOrigin;
  const calls = [], module = { exports: {} };
  const fetch = async (url, options) => {
    calls.push({ url, options });
    if (url.endsWith('/api/v1/site')) {
      if (siteError) throw new Error('site unavailable');
      return Response.json({ data: { maintenance_enabled: maintenance, maintenance_url: maintenanceURL } });
    }
    if (url.endsWith('/api/v1/me')) {
      if (meError) throw new Error('account unavailable');
      const portal = new Headers(options.headers).get('X-Auth-Portal');
      return Response.json({ data: malformed ? null : { id: portal === 'admin' ? 9 : 7, role: portal === 'admin' ? adminRole : role } }, { status: meStatus });
    }
    if (url === 'http://127.0.0.1:53088/maintenance') return new Response('<main>maintenance</main>');
    throw new Error('Unexpected fetch destination');
  };
  const env = { API_INTERNAL_URL: 'http://127.0.0.1:58088', WEB_INTERNAL_URL: 'http://127.0.0.1:53088', ...(configuredOrigin === undefined ? {} : { PUBLIC_ORIGIN: configuredOrigin }) };
  vm.runInNewContext(outputText, { module, exports: module.exports, require: name => { assert.equal(name, 'next/server'); return { NextResponse }; }, process: { env }, fetch, Headers, URL, AbortSignal }, { filename: 'proxy.js' });
  const invoke = (path, cookie, headers = {}, requestOrigin = publicOrigin) => {
    const jar = typeof cookie === 'string' ? { zjmfgo_session: cookie } : cookie ?? {};
    return module.exports.proxy({ nextUrl: new URL(path, requestOrigin), url: new URL(path, requestOrigin).href, headers: new Headers(headers), cookies: { get: name => jar[name] ? { value: jar[name] } : undefined } });
  };
  return { invoke, calls };
}
function redirect(response, destination, origin = publicOrigin) {
  assert.equal(response.status, 307); assert.equal(response.headers.get('Location'), new URL(destination, origin).href); assert.equal(response.headers.get('Cache-Control'), 'no-store');
}

test('console missing/invalid sessions receive HTTP login redirects before rendering', async () => {
  for (const path of ['/console', '/console/contracts', '/cart', '/cart/']) {
    const anonymous = harness(); redirect(await anonymous.invoke(path), '/login');
    assert.equal(anonymous.calls.filter(row => row.url.endsWith('/me')).length, 0);
    const invalid = harness({ meStatus: 401 }); redirect(await invalid.invoke(path, 'invalid'), '/login');
  }
});
test('customer workspaces use the customer cookie even when the browser also has an administrator cookie', async () => {
  for (const path of ['/console/contracts', '/cart']) {
    const role = 'customer';
    const h = harness({ role }); assert.equal((await h.invoke(path, { zjmfgo_session: 'session', zjmfgo_admin_session: 'other-admin' })).next, true);
    const call = h.calls.find(row => row.url.endsWith('/me'));
    const headers = new Headers(call.options.headers);
    assert.equal(headers.get('Cookie'), 'zjmfgo_session=session'); assert.equal(headers.get('X-Auth-Portal'), 'customer');
    assert.equal(call.options.cache, 'no-store'); assert(call.options.signal instanceof AbortSignal);
  }
});
test('administrator-only browsers remain anonymous in customer workspaces and may open customer login forms', async () => {
  for (const path of ['/console', '/console/orders', '/console/account', '/cart', '/cart/']) {
    const h = harness();
    redirect(await h.invoke(path, { zjmfgo_admin_session: 'staff' }), '/login');
    assert.equal(h.calls.filter(row => row.url.endsWith('/me')).length, 0);
  }
  for (const path of ['/login', '/register']) {
    const h = harness();
    assert.equal((await h.invoke(path, { zjmfgo_admin_session: 'staff' })).next, true);
    assert.equal(h.calls.some(row => row.url.endsWith('/me')), false);
  }
});
test('a wrong-role customer cookie is rejected in its own portal without borrowing the admin identity', async () => {
  for (const role of ['admin', 'super_admin', 'unexpected']) for (const path of ['/console/account', '/cart']) {
    const h = harness({ role });
    redirect(await h.invoke(path, { zjmfgo_session: 'wrong-role', zjmfgo_admin_session: 'staff' }), '/login');
    const calls = h.calls.filter(row => row.url.endsWith('/me'));
    assert.equal(calls.length, 1);
    assert.equal(new Headers(calls[0].options.headers).get('Cookie'), 'zjmfgo_session=wrong-role');
  }
});
test('console account failures return uncached 503 rather than render or masquerade as logout', async () => {
  for (const options of [{ meStatus: 500 }, { meStatus: 403 }, { meError: true }, { malformed: true }]) {
    const response = await harness(options).invoke('/console/contracts', 'session');
    assert.equal(response.status, 503); assert.equal(response.headers.get('Cache-Control'), 'no-store'); assert.equal(response.headers.get('Retry-After'), '60'); assert.equal(response.next, undefined);
  }
});
test('maintenance blocks every public portal even with an administrator cookie while retaining the external maintenance URL', async () => {
  for (const [cookie, options] of [[undefined, {}], ['customer', {}], ['invalid', { meStatus: 401 }]]) {
    const h = harness({ maintenance: true, ...options }); const response = await h.invoke('/console/contracts', cookie);
    assert.equal(response.status, 503); assert.equal(response.headers.get('Cache-Control'), 'no-store'); assert.match(await response.text(), /maintenance/);
    const target = 'https://status.example.test/window';
    const external = await harness({ maintenance: true, maintenanceURL: target, ...options }).invoke('/console/contracts', cookie);
    assert.equal(external.status, 307); assert.equal(external.headers.get('Location'), target); assert.equal(external.headers.get('Cache-Control'), 'no-store');
  }
  for (const adminRole of ['admin', 'super_admin']) for (const path of ['/', '/products/1', '/news', '/knowledgebase', '/downloads']) for (const cookies of [undefined, { zjmfgo_session: 'customer' }, { zjmfgo_admin_session: 'staff' }, { zjmfgo_session: 'customer', zjmfgo_admin_session: 'staff' }]) {
    const h = harness({ maintenance: true, adminRole });
    const response = await h.invoke(path, cookies);
    assert.equal(response.status, 503); assert.equal(response.next, undefined);
    assert.equal(response.headers.get('Cache-Control'), 'no-store'); assert.equal(response.headers.get('Retry-After'), '60');
    assert.match(await response.text(), /maintenance/);
    assert.equal(h.calls.some(row => row.url.endsWith('/me')), false, 'public maintenance pages must not inspect or borrow an administrator identity');
    const target = 'https://status.example.test/window';
    const external = await harness({ maintenance: true, maintenanceURL: target, adminRole }).invoke(path, cookies);
    assert.equal(external.status, 307); assert.equal(external.headers.get('Location'), target);
    assert.equal(external.headers.get('Cache-Control'), 'no-store');
  }
  assert.equal((await harness({ siteError: true }).invoke('/console/contracts')).status, 503);
  for (const options of [{ meStatus: 500 }, { meError: true }]) assert.equal((await harness({ maintenance: true, ...options }).invoke('/console/contracts', 'session')).status, 503);
});
test('admin guard keeps staff bypass and existing customer/missing/401/500/network outcomes', async () => {
  for (const maintenance of [false, true]) {
    for (const adminRole of ['admin', 'super_admin']) {
      const h = harness({ maintenance, adminRole }); assert.equal((await h.invoke('/admin', { zjmfgo_admin_session: 'staff', zjmfgo_session: 'customer' })).next, true); assert.equal(h.calls.length, 1);
      const headers = new Headers(h.calls[0].options.headers);
      assert.equal(headers.get('Cookie'), 'zjmfgo_admin_session=staff'); assert.equal(headers.get('X-Auth-Portal'), 'admin');
    }
    redirect(await harness({ maintenance }).invoke('/admin'), '/admin-login');
    const customerOnly = harness({ maintenance }); redirect(await customerOnly.invoke('/admin', 'customer'), '/admin-login'); assert.equal(customerOnly.calls.length, 0);
    redirect(await harness({ maintenance, adminRole: 'customer' }).invoke('/admin', { zjmfgo_admin_session: 'wrong-role' }), '/admin-login');
    for (const options of [{ meStatus: 401 }, { meStatus: 500 }, { meError: true }]) redirect(await harness({ maintenance, ...options }).invoke('/admin/site-settings', { zjmfgo_admin_session: 'invalid' }), '/admin-login');
  }
});
test('auth redirects use configured public origin despite hostile forwarding/Host and request origin', async () => {
  const headers = { Host: 'hostile.example', Forwarded: 'host=hostile.example;proto=http', 'X-Forwarded-Host': 'hostile.example', 'X-Forwarded-Proto': 'http', 'X-Forwarded-For': '203.0.113.1' };
  for (const [path, destination] of [['/admin', '/admin-login'], ['/console/contracts', '/login']]) {
    redirect(await harness().invoke(path, undefined, headers, 'http://hostile.example'), destination);
    for (const configuredOrigin of ['https://user:password@public.example', 'https://public.example/path', 'https://public.example/?query=1', 'https://public.example/#fragment', 'file:///tmp/x', 'invalid', '']) await assert.rejects(harness({ configuredOrigin }).invoke(path), /Invalid|URL/);
  }
  await assert.rejects(harness({ configuredOrigin: 'https://user:password@hostile.example', meStatus: 401 }).invoke('/console/contracts', 'invalid', headers, 'http://hostile.example'), /Invalid/);
  redirect(await harness({ configuredOrigin: undefined }).invoke('/console/contracts', undefined, {}, 'http://127.0.0.1:53088'), '/login', 'http://127.0.0.1:53088');
});
test('API rewrite and public/exempt pages remain independent of console auth', async () => {
  const h = harness({ maintenance: true });
  for (const path of ['/api/v1/me', '/API/v1/me', '/aPi/v1/me']) {
    const result = await h.invoke(path, 'session', { Forwarded: 'for=203.0.113.1', 'X-Forwarded-For': '203.0.113.1', 'X-Real-IP': '203.0.113.1', Cookie: 'zjmfgo_session=session', Origin: publicOrigin });
    assert.equal(result.next, true); for (const name of ['Forwarded', 'X-Forwarded-For', 'X-Real-IP']) assert.equal(result.forwarded.get(name), null);
    assert.equal(result.forwarded.get('Cookie'), 'zjmfgo_session=session'); assert.equal(result.forwarded.get('Origin'), publicOrigin);
  }
  for (const path of ['/admin-login', '/maintenance', '/forgot-password', '/reset-password']) assert.equal((await h.invoke(path)).next, true);
  assert.equal(h.calls.length, 0);
  const publicPage = harness(); assert.equal((await publicPage.invoke('/console-public')).next, true); assert.equal(publicPage.calls.length, 1);
});
