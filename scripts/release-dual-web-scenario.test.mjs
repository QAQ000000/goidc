import test from 'node:test';
import assert from 'node:assert/strict';
import { responseEvidence, sessionCookie, selectDrainAsset, rawAttackTargets, rawRequestBytes, parseRawResponse, identicalIdentity, traceConnectsAPI, accessUpstream, noStore, publicRedirect } from './release-dual-web-scenario.mjs';

const origin = 'http://127.0.0.1:53188';

test('evidence copies only status, timing and body digest, excluding response headers and contents', () => {
  const result = responseEvidence({ method: 'POST', status: 200, started: 100, ended: 137,
    headers: { 'set-cookie': ['zjmfgo_session=SECRET'], authorization: 'SECRET', 'x-release-generation': 'old' },
    body: Buffer.from('{"password":"SECRET","email":"SECRET"}'), cookie: 'SECRET', credentials: 'SECRET' }, { phase: 'old', route: '/api/v1/auth/login', purpose: 'login' });
  assert.equal(result.durationMS, 37); assert.equal(result.generation, 'old'); assert.match(result.sha256, /^[a-f0-9]{64}$/);
  assert.doesNotMatch(JSON.stringify(result), /SECRET|password|email|set-cookie|authorization|credentials/);
  assert.deepEqual(Object.keys(result), ['phase', 'route', 'purpose', 'method', 'status', 'generation', 'started', 'ended', 'durationMS', 'bytes', 'sha256']);
});

test('session extraction accepts exactly one named session and never includes Cookie attributes', () => {
  const cookie = `zjmfgo_session=${'a'.repeat(64)}`;
  assert.equal(sessionCookie({ 'set-cookie': ['unrelated=value; Path=/', `${cookie}; Path=/; HttpOnly; SameSite=Lax`] }), cookie);
  for (const headers of [{}, { 'set-cookie': cookie }, { 'set-cookie': [cookie, cookie] }, { 'set-cookie': ['zjmfgo_session=short'] }, { 'set-cookie': [`${cookie}\r\nInjected=1`] }]) assert.throws(() => sessionCookie(headers));
  for (const attributes of ['Path=/; SameSite=Lax', 'HttpOnly; SameSite=Lax', 'HttpOnly; Path=/console; SameSite=Lax', 'HttpOnly; Path=/; SameSite=None', 'HttpOnly; Path=/; SameSite=Lax; Secure', 'HttpOnly; Path=/; SameSite=Lax; Domain=127.0.0.1', 'HttpOnly; Path=/; SameSite=Lax; Path=/other']) assert.throws(() => sessionCookie({ 'set-cookie': [`${cookie}; ${attributes}`] }));
});

test('dynamic no-store permits reviewed composite directives and rejects weak cache policies', () => {
  for (const value of ['no-store', 'private, no-cache, no-store, max-age=0, must-revalidate', 'NO-STORE']) assert.equal(noStore({ 'cache-control': value }), true);
  for (const value of [undefined, 'no-cache', 'private', 'public, max-age=300', 'x-no-store', 'no-store=invalid']) assert.equal(noStore({ 'cache-control': value }), false);
});

test('public redirects resolve only to the public origin and never expose internal endpoints', () => {
  for (const value of ['/login', `${origin}/login`, 'login?next=%2Fadmin']) assert.equal(publicRedirect(value, origin), '/login');
  for (const value of ['http://127.0.0.1:53088/login', 'http://127.0.0.1:58089/login', '//invalid.example/login', 'https://127.0.0.1:53188/login', `http://user:pass@127.0.0.1:53188/login`, '']) assert.throws(() => publicRedirect(value, origin));
});

test('drain selector uses a real bounded chunk and fails closed on only oversized or tiny files', () => {
  const rows = [{ url: '/_next/static/huge.js', bytes: 131073 }, { url: '/_next/static/small.js', bytes: 65535 },
    { url: '/_next/static/not-js.css', bytes: 65536 }, { url: '/_next/static/exact.js', bytes: 65536 }, { url: '/_next/static/large.js', bytes: 131072 }];
  assert.equal(selectDrainAsset(rows).url, '/_next/static/exact.js');
  assert.equal(selectDrainAsset([rows[4]]).bytes, 131072);
  assert.throws(() => selectDrainAsset(rows.slice(0, 3)), /64–128/);
  assert.throws(() => selectDrainAsset([]));
});

test('raw request construction preserves dot segments, escaping, backslash and control bytes exactly', () => {
  const known = '/_next/static/chunks/a.js', targets = rawAttackTargets(known);
  assert.ok(targets.includes('/_next/static/../../api/v1/site'));
  assert.ok(targets.some(value => value.includes('%252e')));
  assert.ok(targets.some(value => value.includes('\x00')));
  assert.ok(targets.some(value => value.includes('\\')));
  for (const target of targets) {
    const bytes = rawRequestBytes(origin, target);
    assert.deepEqual(bytes.subarray(4, 4 + Buffer.byteLength(target, 'latin1')), Buffer.from(target, 'latin1'));
    assert.ok(bytes.includes(Buffer.from('Host: 127.0.0.1:53188\r\n')));
  }
  assert.throws(() => rawRequestBytes(origin, '/x\r\nX-Evil: yes'));
  assert.throws(() => rawRequestBytes(origin, '/', { host: 'x\r\nX-Evil: yes' }));
});

test('raw parser keeps malformed-request error response bytes without URL normalization or JSON decoding', () => {
  const body = Buffer.from([0, 1, 2, 255]);
  const parsed = parseRawResponse(Buffer.concat([Buffer.from('HTTP/1.1 400 Bad Request\r\nContent-Type: text/html\r\nCache-Control: no-store\r\n\r\n'), body]));
  assert.equal(parsed.status, 400); assert.equal(parsed.headers['cache-control'], 'no-store'); assert.deepEqual(parsed.body, body);
  for (const data of ['HTTP/1.1 200 OK', 'BAD 200\r\n\r\n', 'HTTP/1.1 200 OK\r\nmissing-colon\r\n\r\n']) assert.throws(() => parseRawResponse(Buffer.from(data)));
});

test('master identity rejects PID reuse, changed namespaces, executable bytes and process title', () => {
  const row = { pid: 23, ppid: 1, group: 23, startTicks: '100', uid: 1000, gid: 1000, exe: '/tools/nginx', sha256: 'a'.repeat(64), mountNamespace: 'mnt:[2]', netNamespace: 'net:[3]', cmdline: ['nginx: master process /tools/nginx -p /mutable/'] };
  assert.equal(identicalIdentity(row, structuredClone(row)), true);
  for (const key of Object.keys(row)) {
    const changed = structuredClone(row); changed[key] = key === 'cmdline' ? ['other'] : `${row[key]}-changed`;
    assert.equal(identicalIdentity(row, changed), false, key);
  }
});

test('Web to API proof needs an actual matching loopback connect and accepted connect result', () => {
  const line = 'connect(18<TCP:[123]>, {sa_family=AF_INET, sin_port=htons(58089), sin_addr=inet_addr("127.0.0.1")}, 16) = -1 EINPROGRESS (Operation now in progress)';
  assert.equal(traceConnectsAPI(line, 'http://127.0.0.1:58089'), true);
  assert.equal(traceConnectsAPI(line.replace('-1 EINPROGRESS (Operation now in progress)', '0'), 'http://127.0.0.1:58089'), true);
  for (const bad of [line.replace('58089', '58088'), line.replace('127.0.0.1', '198.51.100.1'), line.replace('connect(', 'getpeername('), line.replace('EINPROGRESS', 'ECONNREFUSED')]) assert.equal(traceConnectsAPI(bad, 'http://127.0.0.1:58089'), false);
});

test('upstream attribution requires the requested phase, route and direct Go endpoint', () => {
  const line = '1792222222.123 next 200 GET /api/v1/me 127.0.0.1:58089\n';
  assert.equal(accessUpstream(line, 'next', '/api/v1/me', 'http://127.0.0.1:58089'), true);
  assert.equal(accessUpstream(line, 'old', '/api/v1/me', 'http://127.0.0.1:58089'), false);
  assert.equal(accessUpstream(line, 'next', '/api/v1/admin/site-settings', 'http://127.0.0.1:58089'), false);
  assert.equal(accessUpstream(line, 'next', '/api/v1/me', 'http://127.0.0.1:53089'), false);
  assert.equal(accessUpstream(line.replace('127.0.0.1:58089', '-'), 'next', '/api/v1/me', 'http://127.0.0.1:58089'), false);
});
