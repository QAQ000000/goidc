import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdtemp, mkdir, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import http from 'node:http';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { createRequire } from 'node:module';
import { setTimeout as delay } from 'node:timers/promises';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const web = path.join(root, 'web');
const require = createRequire(path.join(web, 'package.json'));
const ts = require('typescript');
const listen = server => new Promise(resolve => server.listen(0, '127.0.0.1', resolve));

// Exercise the real Next proxy + rewrite pipeline, not just Headers.delete.
// A disposable app imports the current project proxy/config and a local echo
// backend; no application database, live service or external provider is used.
for (const mode of ['development', 'production']) {
test(`Next ${mode} API rewrite rejects caller IP assertions and preserves business headers/body`, { timeout: 120_000 }, async t => {
  const fixture = await mkdtemp(path.join(tmpdir(), 'zjmfgo-proxy-test-'));
  t.after(() => rm(fixture, { recursive: true, force: true }));
  const backend = http.createServer(async (req, res) => {
    let body = '';
    for await (const chunk of req) body += chunk;
    res.setHeader('Content-Type', 'application/json');
    res.end(JSON.stringify({ data: { maintenance_enabled: false }, url: req.url, method: req.method, headers: req.headers, body }));
  });
  await listen(backend);
  t.after(() => { backend.closeAllConnections(); backend.close(); });
  const reservation = http.createServer();
  await listen(reservation);
  const port = reservation.address().port;
  await new Promise(resolve => reservation.close(resolve));

  await symlink(path.join(web, 'node_modules'), path.join(fixture, 'node_modules'));
  await mkdir(path.join(fixture, 'app'));
  await writeFile(path.join(fixture, 'package.json'), '{"private":true}');
  await writeFile(path.join(fixture, 'app/layout.js'), 'export default function Layout({children}) { return <html><body>{children}</body></html>; }');
  await writeFile(path.join(fixture, 'app/page.js'), 'export default function Page() { return <main>Proxy fixture</main>; }');
  for (const [source, target] of [['src/proxy.ts', 'proxy.js'], ['next.config.ts', 'next.config.mjs']]) {
    const { outputText } = ts.transpileModule(await readFile(path.join(web, source), 'utf8'), {
      compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
    });
    await writeFile(path.join(fixture, target), outputText);
  }
  let logs = '';
  const env = { ...process.env, NODE_ENV: mode, NEXT_TELEMETRY_DISABLED: '1', API_INTERNAL_URL: `http://127.0.0.1:${backend.address().port}` };
  if (mode === 'production') {
    const build = spawn(process.execPath, [require.resolve('next/dist/bin/next'), 'build', '--webpack'], { cwd: fixture, env, stdio: ['ignore', 'pipe', 'pipe'] });
    build.stdout.on('data', chunk => { logs += chunk; });
    build.stderr.on('data', chunk => { logs += chunk; });
    const killBuild = setTimeout(() => build.kill('SIGKILL'), 60_000);
    const [code] = await once(build, 'exit');
    clearTimeout(killBuild);
    assert.equal(code, 0, logs);
  }
  const command = mode === 'production' ? ['start'] : ['dev', '--webpack'];
  const next = spawn(process.execPath, [require.resolve('next/dist/bin/next'), ...command, '-H', '127.0.0.1', '-p', String(port)], {
    cwd: fixture,
    env,
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  next.stdout.on('data', chunk => { logs += chunk; });
  next.stderr.on('data', chunk => { logs += chunk; });
  t.after(async () => {
    if (next.exitCode === null && next.signalCode === null) {
      const exited = once(next, 'exit');
      next.kill('SIGTERM');
      const kill = setTimeout(() => next.kill('SIGKILL'), 5000);
      await exited;
      clearTimeout(kill);
    }
  });
  const origin = `http://127.0.0.1:${port}`;
  const deadline = Date.now() + 60_000;
  for (;;) {
    try {
      const response = await fetch(`${origin}/api/v1/site`, { signal: AbortSignal.timeout(3000) });
      if (response.ok) break;
    } catch {}
    assert(next.exitCode === null && next.signalCode === null && Date.now() < deadline, `Next did not become ready:\n${logs}`);
    await delay(100);
  }
  const body = JSON.stringify({ identifier: 'proxy-test@example.test', password: 'fixture-only' });
  for (const prefix of ['/api', '/API', '/aPi']) {
  for (const headers of [
    { 'X-Forwarded-For': '203.0.113.10' },
    { 'X-Real-IP': '203.0.113.11' },
    { 'X-Forwarded-For': '203.0.113.12, 127.0.0.1', 'X-Real-IP': '203.0.113.13', Forwarded: 'for=203.0.113.14' },
    { 'X-Forwarded-For': '203.0.113.15', 'X-Middleware-Subrequest': 'proxy:proxy:proxy:proxy:proxy' },
  ]) {
    const response = await fetch(`${origin}${prefix}/v1/auth/login?probe=1`, {
      method: 'POST', redirect: 'error', signal: AbortSignal.timeout(5000), body,
      headers: { ...headers, 'Content-Type': 'application/json', Cookie: 'zjmfgo_session=fixture', Origin: origin, 'X-Requested-With': 'zjmfgo', 'Idempotency-Key': 'fixture-once' },
    });
    assert.equal(response.status, 200, logs);
    const echo = await response.json();
    for (const header of ['x-forwarded-for', 'x-real-ip', 'forwarded']) assert.equal(echo.headers[header], undefined, `caller ${header} reached Go`);
    assert.equal(echo.headers.cookie, 'zjmfgo_session=fixture');
    assert.equal(echo.headers.origin, origin);
    assert.equal(echo.headers['x-requested-with'], 'zjmfgo');
    assert.equal(echo.headers['idempotency-key'], 'fixture-once');
    assert.equal(echo.method, 'POST');
    assert.equal(echo.url, '/api/v1/auth/login?probe=1');
    assert.equal(echo.body, body);
  }
  }
  // Payment callbacks remain independent of page maintenance/auth checks.
  const callback = await fetch(`${origin}/api/v1/payments/epay/notify?out_trade_no=fixture&sign=fixture`, { signal: AbortSignal.timeout(5000) });
  assert.equal(callback.status, 200);
  assert.equal((await callback.json()).url, '/api/v1/payments/epay/notify?out_trade_no=fixture&sign=fixture');
});
}
