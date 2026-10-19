import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import test from 'node:test';
import vm from 'node:vm';

const require = createRequire(new URL('../web/package.json', import.meta.url));
const ts = require('typescript');
// This installed Next build still exports the compatibility name.
const { unstable_doesMiddlewareMatch: doesProxyMatch } = require('next/experimental/testing/server');
const source = readFileSync(new URL('../web/src/proxy.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText;

class NextResponse extends Response {
  static next() { const response = new NextResponse(null); response.next = true; return response; }
  static redirect(url, options) { return new NextResponse(null, { ...options, headers: { ...options?.headers, Location: String(url) } }); }
}
function harness() {
  const module = { exports: {} }; const calls = [];
  vm.runInNewContext(compiled, { module, exports: module.exports, require: name => { assert.equal(name, 'next/server'); return { NextResponse }; }, URL, Headers, AbortSignal, process: { env: {} }, fetch: async url => { calls.push(url); throw new Error('backend unavailable'); } });
  return { ...module.exports, calls, invoke(path) { return module.exports.proxy({ nextUrl: new URL(path, 'https://account.example'), url: 'https://account.example' + path, headers: new Headers(), cookies: { get() {} } }); } };
}

test('notification worker and manifest bypass maintenance/backend fetches while account page remains protected', async () => {
  const h = harness();
  for (const resource of ['/notification-sw.js', '/manifest.webmanifest']) assert.equal((await h.invoke(resource)).next, true);
  assert.deepEqual(h.calls, []);
  const account = await h.invoke('/console/account'); assert.equal(account.status, 503); assert.notEqual(account.next, true); assert.ok(h.calls.length > 0);
});

test('Next matcher exempts only exact notification assets and keeps API/account/auth boundaries', () => {
  const h = harness();
  for (const resource of ['/notification-sw.js', '/notification-sw.js?v=2', '/manifest.webmanifest']) assert.equal(doesProxyMatch({ config: h.config, nextConfig: {}, url: resource }), false, resource);
  for (const resource of ['/notification-sw.js/extra', '/notification-swXjs', '/manifestXwebmanifest', '/console/account', '/api/v1/me/notification-web-push', '/admin/channels?category=push']) assert.equal(doesProxyMatch({ config: h.config, nextConfig: {}, url: resource }), true, resource);
});
