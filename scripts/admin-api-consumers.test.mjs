import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, rm } from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import { collectAdminConsumers, matchPagePattern, matchApiCandidates, validateRequirements } from './admin-api-consumers.mjs';

async function fixture(t, sources) {
  // Link only the parser dependency; application source remains an isolated temporary fixture.
  const root = await mkdtemp(path.join(os.tmpdir(), 'goidc-api-consumers-'));
  const tsPath = path.resolve('web/node_modules/typescript');
  const { symlink } = await import('node:fs/promises');
  await mkdir(path.join(root, 'web/node_modules'), { recursive: true });
  await symlink(tsPath, path.join(root, 'web/node_modules/typescript'), 'dir');
  for (const [file, text] of Object.entries({
    'web/package.json': '{"private":true}',
    'web/src/lib/api.ts': 'export function api(path, options) {} export function upload(path, form, method) {} export function download(path) {}',
    'web/src/hooks/use-resource.ts': 'export function useResource(path) {}',
    ...sources,
  })) {
    await mkdir(path.dirname(path.join(root, file)), { recursive: true });
    await writeFile(path.join(root, file), text);
  }
  t.after(() => rm(root, { recursive: true, force: true }));
  return root;
}

test('recurses aliases, reexports, namespace calls, literal imports and cycles without claiming exact reachability', async t => {
  const root = await fixture(t, {
    'web/src/app/admin/customers/[id]/page.tsx': 'import { Panel } from "@/parts"; export default Panel;',
    'web/src/parts/index.ts': 'export { Panel } from "./panel";',
    'web/src/parts/panel.tsx': 'import { request } from "./requests"; import * as requests from "@/lib/api"; import "./cycle"; import("./lazy"); export function Panel() { request("/admin/customers/42"); requests.download("/admin/files/4"); }',
    'web/src/parts/requests.ts': 'export { api as request } from "@/lib/api";',
    'web/src/parts/cycle.ts': 'import "./panel";',
    'web/src/parts/lazy.ts': 'import { useResource as resource } from "@/hooks/use-resource"; resource("/admin/jobs");',
  });
  const result = await collectAdminConsumers({ root, routes: [
    { method: 'GET', path: '/api/v1/admin/customers/{id}' },
    { method: 'GET', path: '/api/v1/admin/files/{id}' },
    { method: 'GET', path: '/api/v1/admin/jobs' },
  ] });
  assert.equal(result.scope, 'file-level-candidate-reachability');
  assert.equal(result.pages[0].path, '/admin/customers/[id]');
  assert.equal(result.requests.length, 3);
  assert.ok(result.requests.every(item => item.matchStatus === 'matched-static'));
  assert.ok(result.requests.every(item => item.pages.includes('/admin/customers/[id]')));
  assert.ok(result.pages[0].dependencies.includes('web/src/parts/lazy.ts'));
  assert.match(result.limitations.join(' '), /render branches/);
});

test('preserves templates, conditionals, null branches and method uncertainty', async t => {
  const root = await fixture(t, {
    'web/src/app/admin/page.tsx': `import { api, upload, download } from "@/lib/api";
      import { useResource } from "@/hooks/use-resource";
      api(\`/admin/customers/\${id}?tab=profile\`, { method: editing ? "PATCH" : "POST" });
      api(endpoint, options);
      api("/admin/customers", { method: "POST", ...options });
      api("/admin/customers", { ...options, method: "POST" });
      upload("/admin/files", form, customMethod);
      download("/admin/files/12");
      useResource(enabled ? "/admin/jobs" : null);
      useResource(enabled ? "/admin/jobs" : "/admin/runtime");`,
  });
  const result = await collectAdminConsumers({ root, routes: [
    { method: 'PATCH', path: '/api/v1/admin/customers/{id}' },
    { method: 'POST', path: '/api/v1/admin/customers' },
    { method: 'GET', path: '/api/v1/admin/jobs' },
    { method: 'GET', path: '/api/v1/admin/runtime' },
  ] });
  const request = result.requests[0];
  assert.equal(request.pathKind, 'template');
  assert.equal(request.pathUnknown, true);
  assert.deepEqual(request.methods, ['PATCH', 'POST']);
  assert.equal(request.matchStatus, 'candidate');
  assert.deepEqual(request.matches, ['PATCH /api/v1/admin/customers/{id}']);
  assert.equal(result.requests[1].pathKind, 'unresolved');
  assert.equal(result.requests[1].methodUnknown, true);
  assert.equal(result.requests[1].matchStatus, 'unresolved-method');
  assert.equal(result.requests[2].methodUnknown, true);
  assert.equal(result.requests[3].methodUnknown, false);
  assert.equal(result.requests[4].methodUnknown, true);
  assert.equal(result.requests[5].methods[0], 'GET');
  assert.equal(result.requests[6].nullable, true);
  assert.equal(result.requests[7].pathKind, 'finite-alternatives');
});

test('does not infer unknown wrapper paths, unrelated function names or dynamic imports are covered', async t => {
  const root = await fixture(t, {
    'web/src/app/admin/page.tsx': 'import { api } from "@/lib/api"; import("./" + tab); function apiLookalike() {} apiLookalike("/admin/missing"); api(endpoint);',
  });
  const result = await collectAdminConsumers({ root, routes: [{ method: 'GET', path: '/api/v1/admin/jobs' }] });
  assert.equal(result.requests.length, 1);
  assert.equal(result.requests[0].matchStatus, 'unresolved-path');
  assert.deepEqual(result.requests[0].matches, []);
  assert.equal(result.unresolvedDependencies.length, 1);
});

test('finite paths require every known method/path pair and computed option keys remain uncertain', async t => {
  const root = await fixture(t, {
    'web/src/app/admin/page.tsx': `import { api } from "@/lib/api";
      api(enabled ? "/admin/jobs" : "/admin/missing");
      api("/admin/jobs", { [key]: value });
      api("/admin/jobs", { ["method"]: "POST" });`,
  });
  const result = await collectAdminConsumers({ root, routes: [{ method: 'GET', path: '/api/v1/admin/jobs' }] });
  assert.equal(result.requests[0].matchStatus, 'partially-matched-static');
  assert.equal(result.requests[0].candidateMatches[1].methods[0].matches.length, 0);
  assert.equal(result.requests[1].methodUnknown, true);
  assert.deepEqual(result.requests[2].methods, ['POST']);
  assert.equal(result.requests[2].matchStatus, 'unmatched-static');
});

test('type-only imports do not create request reachability and export-star aliases resolve', async t => {
  const root = await fixture(t, {
    'web/src/app/admin/page.tsx': 'import type { Hidden } from "./hidden"; import { type Hidden as OtherHidden } from "./hidden"; import { api } from "./barrel"; api("/admin/jobs");',
    'web/src/app/admin/hidden.ts': 'import { api } from "@/lib/api"; export type Hidden = string; api("/admin/hidden");',
    'web/src/app/admin/barrel.ts': 'export * from "@/lib/api";',
  });
  const result = await collectAdminConsumers({ root });
  assert.equal(result.requests.length, 1);
  assert.equal(result.pages[0].dependencies.includes('web/src/app/admin/hidden.ts'), false);
});

test('matches static, Next dynamic and catch-all routes and concrete Go route parameters', () => {
  assert.equal(matchPagePattern('/admin/logs/[kind]', '/admin/logs/system'), true);
  assert.equal(matchPagePattern('/admin/logs/[kind]', '/admin/logs/system/nested'), false);
  assert.equal(matchPagePattern('/admin/files/[...path]', '/admin/files/a/b'), true);
  assert.equal(matchPagePattern('/admin/files/[...path]', '/admin/files'), false);
  assert.equal(matchPagePattern('/admin/files/[[...path]]', '/admin/files'), true);
  assert.equal(matchPagePattern('/admin/**', '/admin'), true);
  assert.equal(matchPagePattern('/admin/jobs', '/admin/jobs?tab=queue'), true);
  assert.equal(matchApiCandidates('/admin/files/12/download?token=ignored', [{ method: 'GET', path: '/api/v1/admin/files/{id}/download' }]).length, 1);
  assert.equal(matchApiCandidates('/admin/files/12/delete', [{ method: 'GET', path: '/api/v1/admin/files/{id}/download' }]).length, 0);
});

const validRequirement = () => ({
  schemaVersion: 1,
  domains: [{ id: 'customers' }],
  requirements: [{
    id: 'customers.read', domain: 'customers', required: true, disposition: 'reuse',
    pages: ['/admin/customers/[id]'], reason: 'Reuse existing customer API',
    current: { endpoints: ['GET /api/v1/admin/customers/{id}'] }, legacy: { evidence: [] },
    api: { status: 'implemented' }, ui: { status: 'wired' }, evidence: ['internal/httpapi/admin.go:1'],
    verification: { static: 'source-reviewed', unit: 'not-run', database: 'not-run', browser: 'not-run', external: 'not-applicable' },
  }],
});

test('declarations keep required/disposition and API/UI/verification independent; exclusions require a reason', () => {
  const declaration = validRequirement();
  assert.deepEqual(validateRequirements(declaration, { pages: [{ path: '/admin/customers/[id]' }] }), []);
  declaration.requirements[0].disposition = 'excluded';
  assert.match(validateRequirements(declaration).join('\n'), /excluded\/deferred cannot be required/);
  declaration.requirements[0].required = false;
  declaration.requirements[0].reason = '';
  declaration.requirements[0].api.status = 'PASS';
  declaration.requirements[0].verification.database = 'passed';
  declaration.requirements[0].pages = ['/admin/no-such-page'];
  const errors = validateRequirements(declaration, { pages: [{ path: '/admin/customers/[id]' }] }).join('\n');
  assert.match(errors, /reason is required/);
  assert.match(errors, /invalid API axis/);
  assert.match(errors, /invalid verification axis: database/);
  assert.match(errors, /page scope matches no current page/);
});

test('declaration validation checks real source files and registered operations', async t => {
  const root = await fixture(t, { 'web/src/app/admin/customers/[id]/page.tsx': 'export default function Page() { return null; }' });
  const declaration = validRequirement();
  declaration.requirements[0].evidence = ['web/src/app/admin/customers/[id]/page.tsx'];
  await assert.rejects(collectAdminConsumers({ root, requirements: declaration, routes: [{ method: 'GET', path: '/api/v1/admin/jobs' }] }), /declared endpoint is not registered/);
  declaration.requirements[0].current.endpoints = [];
  declaration.requirements[0].evidence = ['web/src/missing.ts'];
  await assert.rejects(collectAdminConsumers({ root, requirements: declaration }), /source does not exist/);
});
