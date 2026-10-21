import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createRequire, Module } from 'node:module';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const web = fileURLToPath(new URL('../web/', import.meta.url));
const require = createRequire(path.join(web, 'package.json'));
const ts = require('typescript');
let permissions = new Set(), query = new URLSearchParams();
const Link = () => null;
const filename = path.join(web, 'src/components/admin-customer-business-actions.tsx');
const module = new Module(filename);
module.require = name => {
  if (name === 'next/link') return { __esModule: true, default: Link };
  if (name === 'next/navigation') return { useSearchParams: () => query };
  if (name === './workspace') return { useAdminPermission: permission => permissions.has(permission) };
  if (name === './ui/button') return { Button: () => null };
  return require(name);
};
module._compile(ts.transpileModule(readFileSync(filename, 'utf8'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true },
}).outputText, filename);
const { CustomerBusinessCreateActions } = module.exports;
function links(kind, allowed, search = '') {
  permissions = new Set(allowed); query = new URLSearchParams(search);
  const result = [];
  function visit(node) {
    if (Array.isArray(node)) return node.forEach(visit);
    if (!node || typeof node !== 'object') return;
    if (node.type === Link) result.push(new URL(node.props.href, 'http://fixture.test'));
    visit(node.props?.children);
  }
  visit(CustomerBusinessCreateActions({ customerId: 41, kind }));
  return result;
}

test('billing links carry the fixed customer and exact detail return query', () => {
  const search = new URLSearchParams({ q: 'a&客户', limit: '25', tab: 'invoices', return_to: '/admin/transactions?source=wallet' });
  const result = links('billing', ['orders.manage', 'invoices.manage', 'invoices.view'], search.toString());
  assert.deepEqual(result.map(url => url.pathname), ['/admin/orders/new', '/admin/invoices/new']);
  for (const url of result) {
    assert.equal(url.searchParams.get('customer_id'), '41');
    assert.equal(url.searchParams.get('return_to'), `/admin/customers/41?${search}`);
  }
});
test('creation actions follow their own write permission and section', () => {
  assert.deepEqual(links('billing', ['customers.manage']), []);
  assert.deepEqual(links('tickets', ['orders.manage', 'invoices.manage']), []);
  assert.deepEqual(links('billing', ['orders.manage']).map(url => url.pathname), ['/admin/orders/new']);
  assert.deepEqual(links('billing', ['invoices.manage']), []);
  assert.deepEqual(links('billing', ['invoices.manage', 'invoices.view']).map(url => url.pathname), ['/admin/invoices/new']);
  assert.deepEqual(links('tickets', ['tickets.manage']).map(url => url.pathname), ['/admin/tickets/new']);
});
test('missing return filters still yields a fixed customer detail return path', () => {
  const [url] = links('tickets', ['tickets.manage']);
  assert.equal(url.searchParams.get('return_to'), '/admin/customers/41');
  assert.equal(url.searchParams.getAll('customer_id').length, 1);
});
