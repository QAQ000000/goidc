import test from 'node:test';
import assert from 'node:assert/strict';
import { G09_CAPACITY_PROFILE_FIXED } from './g09-capacity-contract.mjs';
import { createG09CapacityController, createG09CapacityControllerFromProjection } from './g09-capacity-controller.mjs';
import { parseG09CapacityProjection } from './g09-capacity-projection.mjs';

const profile = { ...G09_CAPACITY_PROFILE_FIXED, origin: 'http://127.0.0.1:58088' };
const resources = () => ({
  customers: Array.from({ length: 1000 }, (_, i) => ({ id: i + 1, balanceCents: 98000 })),
  services: Array.from({ length: 2000 }, (_, i) => ({ id: i + 1, customerId: Math.floor(i / 2) + 1 })),
  invoices: Array.from({ length: 10000 }, (_, i) => ({ id: i + 1, customerId: Math.floor(i / 10) + 1, amountCents: 1000 })),
  supplierAccounts: Array.from({ length: 10 }, (_, i) => ({ id: i + 1 })),
  unknownSupplierAccount: { id: 99 },
});

function fixture(overrides = {}) {
  const calls = [];
  const lifecycle = {
    start: async context => calls.push(['start', context.runRoot]),
    drain: async () => calls.push(['drain']),
    stop: async () => calls.push(['stop']),
  };
  const observer = {
    start: async () => calls.push(['observe-start']),
    snapshot: async () => ({ sampleCount: 1 }),
    stop: async () => calls.push(['observe-stop']),
  };
  const runner = async ({ record, signal }) => {
    record({ event: 'finished', status: 200, accepted: true, succeeded: true });
    assert.equal(signal.aborted, false);
    return { ok: true, total: { succeeded: 1 } };
  };
  return { calls, controller: createG09CapacityController({ profile, resources: resources(), lifecycle, observer, runner, perform: () => ({ status: 200 }), ...overrides }) };
}

test('preflight projects only fixed resources and refuses execution by default', async () => {
  const { controller, calls } = fixture();
  const preflight = controller.preflight();
  assert.equal(preflight.status, 'PREFLIGHT_ONLY');
  assert.deepEqual(preflight.resourceProjection, { customers: 1000, services: 2000, invoices: 10000, suppliers: 10 });
  assert.equal(preflight.capacityVerified, false);
  assert.equal(preflight.businessSuccessGenerated, false);
  await assert.rejects(controller.run({ runRoot: '/tmp/g09-controller-test' }), /g09_capacity_execution_disabled/);
  assert.deepEqual(calls, []);
});

test('Go projection JSON crosses the strict boundary before reaching the controller', () => {
  const source = resources();
  const parsed = parseG09CapacityProjection(JSON.stringify(source));
  assert.equal(Object.isFrozen(parsed), true);
  assert.deepEqual(parsed.customers[0], source.customers[0]);
  const { controller } = fixture({});
  const fromProjection = createG09CapacityControllerFromProjection({
    profile, projection: JSON.stringify(source), lifecycle: controller ? { start: async () => {}, drain: async () => {}, stop: async () => {} } : undefined,
    observer: { start: async () => {}, snapshot: async () => ({}), stop: async () => {} }, runner: async () => ({ ok: true }), perform: () => ({ status: 200 }),
  });
  assert.equal(fromProjection.preflight().resourceProjection.customers, 1000);
});

test('projection rejects status claims, credentials/database fields, malformed rows, IDs, ownership and amounts', () => {
  const source = resources();
  assert.throws(() => parseG09CapacityProjection(JSON.stringify({ ...source, capacityVerified: true })), /g09_projection_status_true/);
  assert.throws(() => parseG09CapacityProjection(JSON.stringify({ ...source, database: 'postgres://secret' })), /g09_projection_sensitive_field/);
  assert.throws(() => parseG09CapacityProjection(JSON.stringify({ ...source, customers: source.customers.map((row, i) => i === 1 ? { ...row, id: 1 } : row) })), /g09_projection_customers_duplicate/);
  assert.throws(() => parseG09CapacityProjection(JSON.stringify({ ...source, services: source.services.map((row, i) => i === 0 ? { ...row, customerId: 999999 } : row) })), /g09_projection_service_owner/);
  assert.throws(() => parseG09CapacityProjection(JSON.stringify({ ...source, customers: source.customers.map((row, i) => i === 0 ? { ...row, balanceCents: -1 } : row) })), /g09_projection_customer_amount/);
  assert.throws(() => parseG09CapacityProjection(JSON.stringify({ ...source, invoices: source.invoices.map((row, i) => i === 0 ? { ...row, extra: 1 } : row) })), /g09_projection_invoices_fields/);
  assert.throws(() => parseG09CapacityProjection(JSON.stringify({ ...source, invoices: source.invoices.map((row, i) => i === 0 ? { ...row, amountCents: -1 } : row) })), /g09_projection_invoice_amount/);
});

test('explicit run composes start, existing load runner, observation, drain and stop', async () => {
  const { controller, calls } = fixture();
  const result = await controller.run({ execute: true, runRoot: '/tmp/g09-controller-test' });
  assert.equal(result.status, 'OBSERVED_RUN_ONLY');
  assert.deepEqual(calls, [['start', '/tmp/g09-controller-test'], ['observe-start'], ['drain'], ['stop'], ['observe-stop']]);
  assert.equal(result.lifecycle.drained, true);
  assert.equal(result.capacityVerified, false);
  assert.equal(result.businessSuccessGenerated, false);
});

test('performer receives the shared allocator and can reserve only a bounded lease', async () => {
  const runner = async ({ perform }) => {
    const lease = perform({ flowId: 'controller-ticket-1', kind: 'ticket', customerId: 7 });
    assert.equal(lease.customerId, 7);
    return { ok: true, total: { succeeded: 1 } };
  };
  const { controller } = fixture({ runner, perform: request => request.allocator.reserve(request) });
  const result = await controller.run({ execute: true, runRoot: '/tmp/g09-controller-test' });
  assert.equal(result.allocation.leasedFlowCount, 1);
  assert.equal(result.allocation.capacityVerified, false);
  assert.equal(result.allocation.businessSuccessGenerated, false);
});

test('runner failure aborts and closes lifecycle, without converting failure to success', async () => {
  const { controller, calls } = fixture({ runner: async () => { throw new Error('runner_failure'); } });
  const result = await controller.run({ execute: true, runRoot: '/tmp/g09-controller-test' });
  assert.equal(result.status, 'FAILED_CLOSED');
  assert.equal(result.error, 'runner_failure');
  assert.deepEqual(calls, [['start', '/tmp/g09-controller-test'], ['observe-start'], ['drain'], ['stop'], ['observe-stop']]);
  assert.equal(result.capacityVerified, false);
});

test('partial start failure still attempts stop and reports a closed failure', async () => {
  const calls = [];
  const lifecycle = {
    start: async () => { calls.push('start'); throw new Error('fixture_start_failed'); },
    drain: async () => { calls.push('drain'); },
    stop: async () => { calls.push('stop'); },
  };
  const observer = { start: async () => calls.push('observe-start'), snapshot: async () => ({}), stop: async () => calls.push('observe-stop') };
  const controller = createG09CapacityController({ profile, resources: resources(), lifecycle, observer, perform: () => ({ status: 200 }) });
  const result = await controller.run({ execute: true, runRoot: '/tmp/g09-controller-test' });
  assert.deepEqual(calls, ['start', 'drain', 'stop', 'observe-stop']);
  assert.equal(result.status, 'FAILED_CLOSED');
  assert.equal(result.error, 'fixture_start_failed');
  assert.equal(result.lifecycle.stopped, true);
});

test('production-shaped roots and incomplete adapters are rejected before startup', async () => {
  const { controller } = fixture({ perform: undefined });
  await assert.rejects(controller.run({ execute: true, runRoot: '/www/wwwroot/g09-run' }), /g09_controller_run_root_invalid/);
  await assert.rejects(controller.run({ execute: true, runRoot: '/tmp/g09-controller-test' }), /g09_controller_performer_required/);
});

