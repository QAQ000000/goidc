import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, readFile, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { normalizePath, parseLegacyCalls, routeKey, routeFingerprint, validateReview, generate } from './route-coverage.mjs';

test('namespaces and parameters remain distinct', () => {
  assert.equal(normalizePath('/v1/hosts/[:id]/'), '/v1/hosts/:id');
  assert.equal(normalizePath('/api/v1/hosts/{id}'), '/api/v1/hosts/:id');
  assert.notEqual(normalizePath('/v1/hosts'), normalizePath('/api/v1/hosts'));
});

test('reference grammar preserves group prefixes, methods, comments and duplicates', () => {
  const calls = parseLegacyCalls([
    'Route::get($domain . "/login", "admin/Public/login");',
    'Route::group("api", function () {',
    '  Route::post("api/host", "api/Host/edit");',
    '})->middleware("ApiCheck");',
    'Route::post("api/host/sync", "api/Host/syncInfo");',
    'Route::group("v1", function () {',
    '  Route::rule("actions", "openapi/Host/action", "GET|POST");',
    '  Route::any("any", "openapi/Host/any");',
    '  // Route::group("wrong", function () {',
    '  /*',
    '  Route::get("disabled", "openapi/Host/disabled");',
    '  */',
    '  Route::get("hosts", "openapi/Host/list");',
    '  Route::get("hosts", "openapi/Host/list");',
    '});',
  ].join('\n'), 'data/route/api.php');
  assert.equal(calls.length, 8);
  assert.equal(calls[0].effective_path, '/:legacy_admin/login');
  assert.equal(calls[1].effective_path, '/api/api/host');
  assert.equal(calls[2].effective_path, '/api/host/sync');
  assert.deepEqual(calls[3].methods, ['GET', 'POST']);
  assert.deepEqual(calls[4].methods, ['ANY']);
  assert.equal(calls[5].commented, true);
  assert.equal(calls[6].effective_path, '/v1/hosts');
  assert.notEqual(routeKey(calls[6]), routeKey(calls[7]));
  assert.equal(routeFingerprint(calls[6]), routeFingerprint(calls[7]));
});

test('verdicts require valid targets, evidence, follow-up and wire tests', () => {
  const base = { fingerprint: 'f', domain: 'services', state: 'covered', reason: 'reason', contract_delta: 'delta', evidence: ['file'] };
  const api = 'GET /api/v1/services', pages = new Set(['/console/services']), routes = new Set([api]);
  const check = r => validateReview(r, 'key', 'f', routes, pages);
  assert.throws(() => check({ ...base, fingerprint: 'old' }), /stale/);
  assert.throws(() => check(base), /without API/);
  assert.throws(() => check({ ...base, go_routes: ['GET /v1/hosts'] }), /unknown Go/);
  assert.throws(() => check({ ...base, state: 'frontend_replaced', pages: ['/missing'] }), /unknown Next/);
  assert.throws(() => check({ ...base, state: 'missing_candidate' }), /follow-up/);
  assert.throws(() => check({ ...base, state: 'matched', go_routes: [api] }), /wire test/);
  assert.equal(check({ ...base, go_routes: [api] }).state, 'covered');
});

test('strict generation rejects incomplete evidence without replacing output and reports source drift', async () => {
  const cwd = await mkdtemp(path.join(os.tmpdir(), 'route-coverage-test-'));
  try {
    for (const dir of ['docs/generated', 'docs/audit', 'internal/httpapi', 'web/src/app/console/services', 'legacy/data/route', 'legacy/app/api/controller']) await mkdir(path.join(cwd, dir), { recursive: true });
    const declaration = 'Route::post("api/host/sync", "api/Host/syncInfo");';
    const route = parseLegacyCalls(declaration, 'data/route/api.php')[0];
    await writeFile(path.join(cwd, 'legacy/data/route/api.php'), '\n' + declaration + '\nRoute::get("extra", "api/Host/extra");');
    await writeFile(path.join(cwd, 'legacy/app/api/controller/HostController.php'), '<?php\npublic function syncInfo() {}');
    await writeFile(path.join(cwd, 'docs/generated/legacy-inventory.json'), JSON.stringify({ source: path.join(cwd, 'legacy'), routes: [route] }));
    await writeFile(path.join(cwd, 'internal/httpapi/server.go'), 'mux.HandleFunc("POST /api/host/sync", s.sync)');
    await writeFile(path.join(cwd, 'web/src/app/console/services/page.tsx'), '');
    const manifestPath = path.join(cwd, 'docs/audit/route-reviews.json');
    await writeFile(manifestPath, '{"reviews":{}}');
    const outputPath = path.join(cwd, 'docs/generated/route-coverage.json');
    await writeFile(outputPath, 'previous artifact');
    await assert.rejects(generate({ cwd, strict: true }), /1 routes still/);
    assert.equal(await readFile(outputPath, 'utf8'), 'previous artifact');
    assert.equal((await generate({ cwd })).unreviewed_routes, 1);
    const review = { fingerprint: routeFingerprint(route), domain: 'suppliers', state: 'covered', reason: 'signed subset', contract_delta: 'not all PHP fields', evidence: ['internal/httpapi/server.go'], go_routes: ['POST /api/host/sync'] };
    await writeFile(manifestPath, JSON.stringify({ reviews: { [routeKey(route)]: review } }));
    assert.equal((await generate({ cwd, strict: true })).unreviewed_routes, 0);
    const result = JSON.parse(await readFile(outputPath, 'utf8'));
    assert.equal(result.routes[0].legacy.source_state, 'line_drift');
    assert.equal(result.routes[0].legacy.current_line, 2);
    assert.equal(result.routes[0].legacy_handler.resolved, true);
    assert.equal(result.routes[0].wire_compatible, false);
    assert.equal(result.current_only_legacy_calls.length, 1);
    await writeFile(manifestPath, JSON.stringify({ reviews: { [routeKey(route)]: { ...review, evidence: ['missing'] } } }));
    await assert.rejects(generate({ cwd, strict: true }), /ENOENT/);
    await writeFile(manifestPath, JSON.stringify({ reviews: { orphan: review } }));
    await assert.rejects(generate({ cwd }), /orphan review/);
  } finally { await rm(cwd, { recursive: true, force: true }); }
});
