import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
const require = createRequire(new URL('../web/package.json', import.meta.url));
const ts = require('typescript');
const source = readFileSync(new URL('../web/src/lib/admin-configuration-inputs.ts', import.meta.url), 'utf8');
const output = { exports: {} };
new Function('exports', ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText)(output.exports);
const { siteContentInput, customerLevelInput, ticketStatusInput } = output.exports;

test('content writes exclude read metadata and fields not accepted by each endpoint', () => {
  const row = { id: 7, version: 4, created_at: 'server-only', updated_at: 'server-only', path: [1, 7], depth: 2, file_count: 3, child_count: 1, effective_active: true, name: '分类', parent_id: 1, sort_order: '2', active: true, description: '说明', label: '导航', href: '/news', icon: '', open_new_tab: false, field_key: 'hero', field_type: 'text', required: true, url: 'https://example.test', logo_url: '' };
  for (const kind of ['fields', 'navigation', 'links', 'groups']) {
    const body = JSON.parse(JSON.stringify(siteContentInput(kind, row)));
    assert.equal(body.expected_version, 4);
    assert.equal(body.sort_order, 2);
    for (const key of ['id', 'version', 'created_at', 'updated_at', 'path', 'depth', 'file_count', 'child_count', 'effective_active']) assert.ok(!Object.hasOwn(body, key), `${kind} excludes ${key}`);
  }
  assert.deepEqual(siteContentInput('navigation', row), { parent_id: 1, label: '导航', href: '/news', icon: '', sort_order: 2, open_new_tab: false, active: true, expected_version: 4 });
  assert.equal(siteContentInput('fields', row).label, '导航');
  assert.deepEqual(siteContentInput('groups', row, true), { expected_version: 4 });
  assert.equal(siteContentInput('links', row, true).url, 'https://example.test');
  assert.equal(siteContentInput('fields', row, true).field_key, 'hero');
  assert.ok(!Object.hasOwn(siteContentInput('links', { ...row, id: undefined }), 'expected_version'));
});

test('level edits retain all six thresholds and CAS while excluding computed customer count', () => {
  const form = { id: 3, version: 6, matching_customer_count: 99, name: '规则', min_income_cents: 1001, min_purchase_count: 2, min_login_count: 3, min_recent_login_count: 4, min_renewal_count: 5, min_recent_renewal_count: 6, sort_order: -1, active: true };
  assert.deepEqual(customerLevelInput(form), { name: '规则', min_income_cents: 1001, min_purchase_count: 2, min_login_count: 3, min_recent_login_count: 4, min_renewal_count: 5, min_recent_renewal_count: 6, sort_order: -1, active: true, expected_version: 6 });
  assert.ok(!Object.hasOwn(customerLevelInput({ ...form, id: undefined }), 'expected_version'));
});

test('ticket status create sends slug while update sends only editable fields and CAS', () => {
  const form = { slug: 'custom', title: '自定义', sort_order: 5, active: true };
  assert.deepEqual(ticketStatusInput(form), { slug: 'custom', title: '自定义', sort_order: 5, active: true });
  assert.deepEqual(ticketStatusInput({ ...form, id: 7, version: 3 }), { title: '自定义', sort_order: 5, active: true, expected_version: 3 });
});
