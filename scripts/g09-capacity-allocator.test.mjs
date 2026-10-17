import test from 'node:test';
import assert from 'node:assert/strict';
import { G09_CAPACITY_PROFILE_FIXED } from './g09-capacity-contract.mjs';
import { createG09CapacityAllocator } from './g09-capacity-allocator.mjs';

const profile = { ...G09_CAPACITY_PROFILE_FIXED, origin: 'http://127.0.0.1:58088' };
const resources = () => ({
  customers: Array.from({ length: 1000 }, (_, i) => ({ id: i + 1, balanceCents: 98000 })),
  services: Array.from({ length: 2000 }, (_, i) => ({ id: i + 1, customerId: Math.floor(i / 2) + 1 })),
  invoices: Array.from({ length: 10000 }, (_, i) => ({ id: i + 1, customerId: Math.floor(i / 10) + 1 })),
  supplierAccounts: Array.from({ length: 10 }, (_, i) => ({ id: i + 1 })),
  unknownSupplierAccount: { id: 99 },
});
const allocator = (limits, data = resources(), inputProfile = profile) => createG09CapacityAllocator({ profile: inputProfile, resources: data, limits });

test('fixed profile data gates the deterministic allocation inventory', () => {
  const first = allocator(), second = allocator();
  const request = { flowId: 'purchase-001', kind: 'purchase_local', amountCents: 1000, lane: 'hot' };
  assert.deepEqual(first.reserve(request), second.reserve(request));
  assert.ok(first.reserve({ flowId: 'normal-001', kind: 'ticket', lane: 'normal' }).customerId > 200);
  assert.equal(first.snapshot().mode, 'allocation_only');
  assert.deepEqual(first.snapshot().profileData, profile.data);
  assert.equal(first.snapshot().capacityVerified, false);
  assert.equal(first.snapshot().businessSuccessGenerated, false);
  assert.throws(() => allocator(undefined, { ...resources(), invoices: resources().invoices.slice(1) }), /g09_allocator_invoices_count/);
  assert.throws(() => allocator(undefined, resources(), { ...profile, data: { ...profile.data, services: 2001 } }), /g09_capacity_profile_drift/);
});

test('customer write, funds, spend-per-write and service budgets fail closed', () => {
  const writeLimited = allocator({ maxWritesPerCustomer: 1 });
  writeLimited.reserve({ flowId: 'one', kind: 'ticket', customerId: 10 });
  assert.throws(() => writeLimited.reserve({ flowId: 'two', kind: 'ticket', customerId: 10 }), /g09_allocator_customer_write_limit/);

  const fundsLimited = allocator({ maxSpendPerCustomerCents: 1000 });
  fundsLimited.reserve({ flowId: 'funds-one', kind: 'purchase_local', customerId: 20, amountCents: 1000 });
  assert.throws(() => fundsLimited.reserve({ flowId: 'funds-two', kind: 'purchase_local', customerId: 20, amountCents: 1 }), /g09_allocator_funds_exhausted/);
  assert.throws(() => allocator().reserve({ flowId: 'large', kind: 'purchase_local', customerId: 1, amountCents: 1001 }), /g09_allocator_funds_exhausted/);

  assert.throws(() => allocator({ maxServicesPerCustomer: 2 }).reserve({ flowId: 'service-cap', kind: 'purchase_local', customerId: 1 }), /g09_allocator_customer_service_limit/);
});

test('invoice and service ownership, repeated allocation and duplicate leases are rejected', () => {
  const value = allocator();
  value.reserve({ flowId: 'pay', kind: 'balance_pay_and_settlement', customerId: 1, invoiceId: 1, amountCents: 1000 });
  assert.throws(() => value.reserve({ flowId: 'pay-again', kind: 'balance_pay_and_settlement', customerId: 1, invoiceId: 1, amountCents: 1000 }), /g09_allocator_invoice_exhausted/);
  assert.throws(() => value.reserve({ flowId: 'cross-invoice', kind: 'balance_pay_and_settlement', customerId: 1, invoiceId: 11 }), /g09_allocator_invoice_owner_mismatch/);
  value.reserve({ flowId: 'renew', kind: 'local_renew', customerId: 1, serviceId: 1, amountCents: 1000 });
  assert.throws(() => value.reserve({ flowId: 'renew-again', kind: 'local_renew', customerId: 1, serviceId: 1 }), /g09_allocator_service_busy/);
  assert.throws(() => value.reserve({ flowId: 'cross-service', kind: 'local_renew', customerId: 1, serviceId: 3 }), /g09_allocator_service_owner_mismatch/);
  assert.throws(() => value.reserve({ flowId: 'pay', kind: 'ticket', customerId: 1 }), /g09_allocator_duplicate_lease/);
});

test('supplier account budget is deterministic and unknown writes stay isolated', () => {
  const value = allocator({ maxWritesPerSupplierAccount: 1 });
  const supplier = value.reserve({ flowId: 'supplier-1', kind: 'purchase_supplier', customerId: 5, amountCents: 1000 });
  assert.ok(supplier.supplierAccountId >= 1 && supplier.supplierAccountId <= 10);
  assert.throws(() => value.reserve({ flowId: 'supplier-2', kind: 'purchase_supplier', customerId: 6, supplierAccountId: supplier.supplierAccountId }), /g09_allocator_supplier_account_limit/);
  const unknown = value.reserve({ flowId: 'unknown-1', kind: 'unknown_supplier_write', customerId: 7, supplierAccountId: 99 });
  assert.equal(unknown.isolated, true);
  assert.equal(unknown.supplierAccountId, 99);
  assert.throws(() => value.reserve({ flowId: 'unknown-2', kind: 'unknown_supplier_write', customerId: 8, supplierAccountId: 99 }), /g09_allocator_unknown_supplier_quota/);
  assert.throws(() => value.reserve({ flowId: 'misspelled', kind: 'supplier_pay', customerId: 1 }), /g09_allocator_unknown_write_isolated/);
  assert.throws(() => value.reserve({ flowId: 'misdirected-unknown', kind: 'unknown_supplier_write', customerId: 1, supplierAccountId: 1 }), /g09_allocator_unknown_supplier_quota/);
  assert.equal(value.snapshot().isolatedUnknownWrites, 1);
});

test('resource maps and lease requests reject malformed, aliased or over-broad input', () => {
  const duplicate = resources();
  duplicate.customers[1] = { ...duplicate.customers[0] };
  assert.throws(() => allocator(undefined, duplicate), /g09_allocator_customers_duplicate/);
  assert.throws(() => allocator().reserve({ flowId: 'unknown-field', kind: 'ticket', customerId: 1, sql: 'UPDATE' }), /g09_allocator_request_invalid/);
  assert.throws(() => allocator().reserve({ flowId: 'missing-lane', kind: 'ticket', lane: 'fallback' }), /g09_allocator_customer_lane_invalid/);
  assert.throws(() => allocator().reserve({ flowId: 'invalid-customer', kind: 'ticket', customerId: 1001 }), /g09_allocator_customer_unknown/);
});
