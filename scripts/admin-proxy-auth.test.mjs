import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

const source = await readFile(new URL('../web/src/proxy.ts', import.meta.url), 'utf8');

test('admin SSR routes authenticate at the proxy before maintenance/page rendering', () => {
  const adminStart = source.indexOf('const adminRoute =');
  const maintenanceBypass = source.indexOf('["/admin-login", "/maintenance", "/forgot-password", "/reset-password"]');
  assert.ok(adminStart >= 0 && maintenanceBypass > adminStart, 'admin guard must run before bypass routes');
  const guard = source.slice(adminStart, maintenanceBypass);
  assert.match(guard, /request\.cookies\.get\("zjmfgo_session"\)/);
  assert.match(guard, /fetch\(`\$\{origin\}\/api\/v1\/me`/);
  assert.match(guard, /Cookie: `zjmfgo_session=\$\{cookie\.value\}`/);
  assert.match(guard, /user\?\.role === "admin" \|\| user\?\.role === "super_admin"/);
  assert.match(guard, /new URL\("\/admin-login", request\.url\)/);
  assert.match(guard, /new URL\("\/", request\.url\)/);
  assert.equal((guard.match(/status: 307/g) ?? []).length, 3, 'missing, customer and failed-auth outcomes must be explicit redirects');
  assert.equal((guard.match(/"Cache-Control": "no-store"/g) ?? []).length, 3, 'all auth redirects must be uncached');
});

test('API rewrite remains before the admin SSR guard and preserves header sanitization', () => {
  const api = source.indexOf('if (/^\\/api(?:\\/|$)/i.test(path))');
  const admin = source.indexOf('const adminRoute =');
  assert.ok(api >= 0 && api < admin);
  assert.match(source.slice(api, admin), /headers\.delete\("x-forwarded-for"\)/);
  assert.match(source.slice(api, admin), /headers\.delete\("x-real-ip"\)/);
  assert.match(source.slice(api, admin), /headers\.delete\("forwarded"\)/);
});
