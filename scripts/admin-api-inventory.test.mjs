import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, readFile, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { collectCurrentRoutes, collectLegacyRoutes, parseLegacyRouteSource, generate } from './admin-api-inventory.mjs';

async function fixture(t) {
  const root = await mkdtemp(path.join(os.tmpdir(), 'admin-api-inventory-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const put = async (name, value) => { await mkdir(path.dirname(path.join(root, name)), { recursive: true }); await writeFile(path.join(root, name), value); };
  await put('internal/httpapi/permissions.go', `package httpapi
var adminRoutePermissions = func() map[string]string {
 m := map[string]string{}
 m["GET /api/v1/admin/items/{id}"] = "items.view"
 for _, operation := range []string{"POST /api/v1/admin/items"} { m[operation] = "items.manage" }
 groups := map[string][]string{"audit.view": {"GET /api/v1/admin/logs"}}
 for permission, operations := range groups { for _, operation := range operations {
  if _, ok := m[operation]; ok { panic("duplicate admin permission route") }
  m[operation] = permission
 } }
 detailPermissions(m)
 return m
}()
func detailPermissions(m map[string]string) { m["GET /api/v1/admin/reports/details"] = "reports.view" }
// Strings outside the real initializer must not grant permission.
func unusedPermission(m map[string]string) { m["GET /api/v1/admin/secret"] = "@super" }
`);
  await put('internal/httpapi/server.go', `package httpapi
func (s *Server) Handler() {
 mux.HandleFunc("GET /api/v1/admin/items/{id}", s.auth(s.item, true))
 mux.HandleFunc("POST /api/v1/admin/items", s.sessionAuth(s.create, true))
 mux.HandleFunc("GET /api/v1/admin/logs", s.auditHTTP(s.logs))
 mux.HandleFunc("GET /healthz", func() {})
 // mux.HandleFunc("GET /api/v1/admin/fake", s.fake)
}
`);
  await put('internal/httpapi/reports/details.go', `package reports
func (s *Server) registerReports(mux *http.ServeMux) {
 mux.HandleFunc(
  "GET /api/v1/admin/reports/details",
  s.auth(s.reportDetails, true),
 )
}
`);
  await put('internal/httpapi/ignored_test.go', `not valid Go; mux.HandleFunc("GET /api/v1/admin/test-only", s.test)`);
  return { root, put };
}

test('Go AST scans all production files, wrappers, comments and actual initializer permission forms', async t => {
  const { root } = await fixture(t);
  const result = await collectCurrentRoutes(root);
  assert.deepEqual(result.summary, { registrations: 5, administrative: 4, productionFiles: 3 });
  const details = result.routes.find(route => route.path.endsWith('/reports/details'));
  assert.equal(details.method, 'GET');
  assert.equal(details.handler, 's.reportDetails');
  assert.equal(details.authWrapper, 's.auth');
  assert.equal(details.registrationFunction, 'registerReports');
  assert.equal(details.permission, 'reports.view');
  assert.equal(details.source, 'internal/httpapi/reports/details.go');
  assert.equal(details.line, 3);
  assert.equal(details.permissionEvidence[0].source, 'internal/httpapi/permissions.go');
  assert.equal(result.routes.find(route => route.path === '/healthz').permission, null);
  assert.equal(result.routes.find(route => route.path.endsWith('/logs')).authWrapper, 's.auditHTTP');
  assert.ok(result.sources.every(source => /^[a-f0-9]{64}$/.test(source.sha256) && !path.isAbsolute(source.path)));
});

test('missing permission cannot be rescued by an uncalled helper or unrelated string', async t => {
  const { root, put } = await fixture(t);
  await put('internal/httpapi/secret.go', 'package httpapi\nfunc register() { mux.HandleFunc("GET /api/v1/admin/secret", s.auth(s.secret, true)) }');
  await assert.rejects(collectCurrentRoutes(root), /missing adminRoutePermissions.*secret/);
});

test('duplicate Go operation also rejects equivalent parameter names across registration files', async t => {
  const { root, put } = await fixture(t);
  await put('internal/httpapi/duplicate.go', 'package httpapi\nfunc register() { mux.HandleFunc("GET /api/v1/admin/items/{other}", s.auth(s.other, true)) }');
  await assert.rejects(collectCurrentRoutes(root), /duplicate current operation/);
});

test('unsupported dynamic Go registration fails closed instead of silently dropping a route', async t => {
  const { root, put } = await fixture(t);
  await put('internal/httpapi/dynamic.go', 'package httpapi\nfunc register() { mux.HandleFunc(pattern, s.auth(s.dynamic, true)) }');
  await assert.rejects(collectCurrentRoutes(root), /nonliteral route pattern/);
});

test('duplicate baseline permission fails even if Go would overwrite the map entry', async t => {
  const { root, put } = await fixture(t);
  await put('internal/httpapi/permissions.go', `package httpapi
var adminRoutePermissions = func() map[string]string {
 m := map[string]string{}
 m["GET /api/v1/admin/items/{id}"] = "items.view"
 m["GET /api/v1/admin/items/{id}"] = "@super"
 return m
}()
`);
  await assert.rejects(collectCurrentRoutes(root), /duplicate permission operation/);
});

test('PHP lexer keeps case, nested groups, ANY, rule, dynamic declarations and comments separate', () => {
  const source = `<?php
Route::POST($domain . "/login", "admin/Public/login");
Route::group($domain, function () {
 Route::get("users", "admin/User/list");
 Route::group("nested", function () {
  Route::Any("search", "admin/User/search");
  Route::rule("sync", "admin/User/sync", "GET|POST");
 });
 // It's disabled: Route::POST("hidden", "admin/User/hidden");
 /* Route::resource("disabled", "admin/Disabled"); */
 Route::RESOURCE("credit", "admin/Credit");
 Route::controller("services", "admin/Services");
 $not_a_route = 'Route::get("fake", "admin/Fake/list")';
});
Route::get("outside", "admin/User/outside");
`;
  const parsed = parseLegacyRouteSource(source, 'data/route/admin.php');
  assert.equal(parsed.routes.length, 5);
  assert.equal(parsed.routes[0].path, '/{legacy_admin}/login');
  assert.equal(parsed.routes[2].path, '/{legacy_admin}/nested/search');
  assert.equal(parsed.routes[2].kind, 'any');
  assert.deepEqual(parsed.routes[3].methods, ['GET', 'POST']);
  assert.equal(parsed.routes.at(-1).path, '/outside');
  assert.deepEqual(parsed.dynamicRoutes.map(route => route.kind), ['resource', 'controller']);
  assert.ok(parsed.dynamicRoutes.every(route => route.method === null && route.resolution === 'dynamic_not_expanded'));
  assert.equal(parsed.commentedRoutes.length, 2);
  assert.equal(parsed.commentedRoutes[0].path, '/{legacy_admin}/hidden');
});

test('legacy include traversal records missing/dynamic paths, resolves local files and handles cycles', async t => {
  const { root, put } = await fixture(t);
  await put('legacy/data/route/admin.php', `<?php
include_once CMF_ROOT . "extra.php";
include_once CMF_ROOT . "missing.php";
require $configured;
Route::get($unknown, "admin/Unknown/list");
Route::controller("services", "admin/Services");
`);
  await put('legacy/extra.php', `<?php
require __DIR__ . "/data/route/admin.php";
Route::GET("extra", "admin/Extra/list");
Route::GET("extra", "admin/Extra/list");
`);
  const result = await collectLegacyRoutes(path.join(root, 'legacy'));
  assert.equal(result.routes.length, 3);
  assert.equal(result.dynamicRoutes.length, 1);
  assert.equal(result.sources.length, 2);
  assert.deepEqual(result.unresolvedIncludes.map(value => value.resolution).sort(), ['dynamic_expression', 'missing_file']);
  assert.equal(result.summary.unresolvedExpressions, 1);
  assert.equal(result.summary.duplicateOperations, 1);
  assert.ok(result.sources.every(source => !source.path.startsWith(root)));
});

test('generation is deterministic, leaves existing evidence untouched on failure, and requires explicit legacy root', async t => {
  const { root, put } = await fixture(t);
  await put('legacy/data/route/admin.php', '<?php\nRoute::get("old", "admin/Old/index");');
  const legacyRoot = path.join(root, 'legacy');
  const result = await generate({ root, legacyRoot });
  assert.equal(result.current.summary.administrative, 4);
  const filename = path.join(root, 'output/audit/admin-api-inventory.json');
  const before = await readFile(filename, 'utf8');
  assert.ok(!before.includes(root));
  await generate({ root, legacyRoot });
  assert.equal(await readFile(filename, 'utf8'), before);
  assert.match(await readFile(path.join(root, 'output/audit/admin-api-inventory.md'), 'utf8'), /不代表接口业务完整性/);
  await put('internal/httpapi/secret.go', 'package httpapi\nfunc register() { mux.HandleFunc("GET /api/v1/admin/secret", s.secret) }');
  await assert.rejects(generate({ root, legacyRoot }), /missing adminRoutePermissions/);
  assert.equal(await readFile(filename, 'utf8'), before);
  await assert.rejects(collectLegacyRoutes(), /legacyRoot is required/);
});
