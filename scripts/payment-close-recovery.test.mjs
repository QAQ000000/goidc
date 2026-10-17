import assert from 'node:assert/strict';
import { createHash, webcrypto } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const require = createRequire(path.join(root, 'web/package.json'));
const ts = require('typescript');
const React = require('react');
const { renderToStaticMarkup } = require('react-dom/server');
const actor = 7;
const request = { kind: 'lookup', target: 13, key: 'original-high-entropy-lookup-key', rejected: true, body: JSON.stringify({ close_operation_id: 13, expected_version: 3 }) };
const original = {
  id: 21, requested_by: actor, invoice_id: 11, payment_id: 12, close_operation_id: 13,
  operation_id: 'original-close-operation', amount_cents: 500, expected_version: 3,
  request_key_sha256: createHash('sha256').update(request.key).digest('hex'),
  status: 'expired', result_status: '', lease_until: '2020-01-01T00:00:00Z',
};
const page = (items, total = items.length) => ({ invoice_id: 11, payment_id: 12, version: 3, items, total, operations: [{ id: 13, operation_id: original.operation_id }] });

// Execute the actual component recovery callback; replace only network and UI
// dependencies. This catches selecting the wrong history row or stopping early.
function recovery(pages) {
  let options;
  const calls = [];
  const primitive = () => null;
  const mocks = {
    '@/lib/api': { api: async url => { calls.push(url); return pages[calls.length - 1]; }, date: String, money: String },
    '@/hooks/use-resource': { useResource: () => ({ data: null, refresh() {} }) },
    '@/hooks/use-durable-admin-mutation': { useDurableAdminMutation: value => { options = value; return { ready: true }; } },
    './workspace': { useAdminIdentity: () => ({ id: actor }), useAdminPermission: () => true },
    './common': { Failure: primitive, Loading: primitive },
    './ui/button': { Button: primitive },
    './ui/textarea': { Textarea: primitive },
    './ui/dialog': Object.fromEntries(['Dialog', 'DialogContent', 'DialogDescription', 'DialogFooter', 'DialogHeader', 'DialogTitle'].map(name => [name, primitive])),
    './payment-exception-common': { DurableRecovery: primitive, ExceptionPager: primitive, validNote: value => typeof value === 'string' && value.trim().length > 0, useExceptionQuery: () => ({ params: new URLSearchParams(), offset: () => 0, update() {} }) },
  };
  const filename = path.join(root, 'web/src/components/payment-close-verifications.tsx');
  const { outputText } = ts.transpileModule(readFileSync(filename, 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX } });
  const module = { exports: {} };
  vm.runInNewContext(outputText, { module, exports: module.exports, require: id => Object.hasOwn(mocks, id) ? mocks[id] : require(id), crypto: webcrypto, TextEncoder, URLSearchParams }, { filename });
  renderToStaticMarkup(React.createElement(module.exports.PaymentCloseVerifications, { invoiceId: 11, payments: [{ id: 12, amount_cents: 500, close_status: 'unknown' }], refresh() {}, revision: 0 }));
  return { read: pending => options.readFacts(pending ?? request), calls };
}

test('newer records for the same Close cannot hide the exact original lookup', async () => {
  const newer = { ...original, id: 22, request_key_sha256: 'f'.repeat(64), status: 'unknown', result_status: 'unknown' };
  const r = recovery([page([newer, original])]);
  assert.equal((await r.read()).canEndRejected, true);
  assert.equal(r.calls.length, 1);
});

test('recovery scans later pages for the original employee and request digest', async () => {
  const others = Array.from({ length: 100 }, (_, id) => ({ ...original, id: id + 100, requested_by: actor + 1, request_key_sha256: undefined }));
  const r = recovery([page(others, 101), page([original], 101)]);
  assert.equal((await r.read()).canEndRejected, true);
  assert.deepEqual(r.calls.map(url => new URL(url, 'http://fixture').searchParams.get('offset')), ['0', '100']);
});

for (const [name, changes] of Object.entries({
  identity: { requested_by: actor + 1 }, digest: { request_key_sha256: '0'.repeat(64) },
  invoice: { invoice_id: 999 }, payment: { payment_id: 999 }, operation: { operation_id: 'another-operation' },
  amount: { amount_cents: 501 }, version: { expected_version: 4 },
  active: { status: 'checking', lease_until: '2099-01-01T00:00:00Z' },
  lease: { lease_until: '2099-01-01T00:00:00Z' },
  result: { result_status: 'unknown' },
})) {
  test(`recovery keeps the request when ${name} does not match`, async () => {
    assert.equal((await recovery([page([{ ...original, ...changes }])]).read()).canEndRejected, false);
  });
}

test('unconfirmed transport outcomes cannot be retired by reading an expired row', async () => {
  assert.equal((await recovery([page([original])]).read({ ...request, rejected: false })).canEndRejected, false);
});

test('an empty history cannot retire a rejected lookup', async () => {
  assert.equal((await recovery([page([])]).read()).canEndRejected, false);
});
