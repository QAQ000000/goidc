import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { runReadLoad } from './load-test.mjs';

async function server(t, handler) {
  const instance = http.createServer(handler);
  instance.listen(0, '127.0.0.1');
  await once(instance, 'listening');
  t.after(() => new Promise(resolve => instance.close(resolve)));
  return `http://127.0.0.1:${instance.address().port}`;
}

test('catalog load consumes complete GETs and keeps its capacity scope explicit', async t => {
  let requests = 0;
  const origin = await server(t, (req, res) => {
    assert.equal(req.method, 'GET');
    assert.equal(req.url, '/api/v1/catalog');
    requests++;
    res.end('{"data":[]}');
  });
  const report = await runReadLoad({ origin, seconds: .3, rps: 10 });
  assert.equal(report.ok, true);
  assert.equal(report.total.succeeded, 3);
  assert.equal(requests, 3);
  assert.equal(report.capacityVerified, false);
  assert.equal(report.scope, 'catalog_http_read');
});

test('the CLI reports HTTP failure and refuses redirects without reaching their target', async t => {
  let targetRequests = 0;
  const target = await server(t, (_req, res) => { targetRequests++; res.end('unexpected'); });
  const origin = await server(t, (_req, res) => { res.writeHead(302, { location: target }); res.end(); });
  const child = spawn(process.execPath, ['scripts/load-test.mjs', '--origin', origin, '--seconds', '.1', '--rps', '10'], { cwd: new URL('..', import.meta.url), stdio: ['ignore', 'pipe', 'pipe'] });
  let output = '';
  child.stdout.on('data', chunk => { output += chunk; });
  const [code] = await once(child, 'close');
  assert.equal(code, 1);
  assert.equal(JSON.parse(output).total.failed, 1);
  assert.equal(targetRequests, 0);
});

test('invalid targets and excessive budgets fail before dispatch', async () => {
  await assert.rejects(runReadLoad({ origin: 'https://example.com' }), /127\.0\.0\.1/);
  await assert.rejects(runReadLoad({ origin: 'http://127.0.0.1:12345', seconds: 3600, rps: 100 }), /budget/);
});
