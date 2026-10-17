import { validateG09CapacityProfile } from './g09-capacity-contract.mjs';

const fail = code => { throw new Error(code); };
const safeID = value => Number.isSafeInteger(value) && value > 0;
const nonNegative = value => Number.isSafeInteger(value) && value >= 0;
const kinds = new Set(['balance_pay_and_settlement', 'local_renew', 'purchase_local', 'purchase_supplier', 'ticket']);

function orderedUnique(rows, label) {
  if (!Array.isArray(rows) || rows.some(row => !safeID(row?.id))) fail(`g09_allocator_${label}_invalid`);
  const sorted = [...rows].sort((a, b) => a.id - b.id);
  if (new Set(sorted.map(row => row.id)).size !== sorted.length) fail(`g09_allocator_${label}_duplicate`);
  return sorted;
}

function stableIndex(key, length) {
  let hash = 2166136261;
  for (const char of key) hash = Math.imul(hash ^ char.charCodeAt(0), 16777619) >>> 0;
  return hash % length;
}

function requireCount(rows, count, label) {
  if (rows.length !== count) fail(`g09_allocator_${label}_count`);
}

export function createG09CapacityAllocator({ profile: profileInput, resources, limits = {} }) {
  const profile = validateG09CapacityProfile(profileInput);
  if (!resources || typeof resources !== 'object' || Array.isArray(resources)) fail('g09_allocator_resources_invalid');
  const customers = orderedUnique(resources.customers, 'customers');
  const services = orderedUnique(resources.services, 'services');
  const invoices = orderedUnique(resources.invoices, 'invoices');
  const supplierAccounts = orderedUnique(resources.supplierAccounts, 'suppliers');
  const unknownSupplierAccount = resources.unknownSupplierAccount;
  requireCount(customers, profile.data.customers, 'customers');
  requireCount(services, profile.data.services, 'services');
  requireCount(invoices, profile.data.invoices, 'invoices');
  requireCount(supplierAccounts, profile.data.suppliers, 'suppliers');
  if (!safeID(unknownSupplierAccount?.id) || supplierAccounts.some(row => row.id === unknownSupplierAccount.id)) fail('g09_allocator_unknown_supplier_invalid');

  const maxWritesPerCustomer = limits.maxWritesPerCustomer ?? 50;
  const maxSpendPerCustomerCents = limits.maxSpendPerCustomerCents ?? 98000;
  const maxSpendPerWriteCents = limits.maxSpendPerWriteCents ?? 1000;
  const maxServicesPerCustomer = limits.maxServicesPerCustomer ?? 50;
  const maxWritesPerSupplierAccount = limits.maxWritesPerSupplierAccount ?? 20;
  const unknownSupplierWrites = profile.data.unknownSupplierWrites;
  if (![maxWritesPerCustomer, maxSpendPerCustomerCents, maxSpendPerWriteCents, maxServicesPerCustomer, maxWritesPerSupplierAccount].every(nonNegative) ||
      maxWritesPerCustomer > 50 || maxSpendPerCustomerCents > 98000 || maxSpendPerWriteCents > 1000 || maxServicesPerCustomer > 50 || maxWritesPerSupplierAccount > 20) {
    fail('g09_allocator_limits_invalid');
  }

  const customerById = new Map(customers.map(row => [row.id, row]));
  const serviceById = new Map(services.map(row => [row.id, row]));
  const invoiceById = new Map(invoices.map(row => [row.id, row]));
  for (const [label, rows] of [['services', services], ['invoices', invoices]]) {
    for (const row of rows) if (!customerById.has(row.customerId)) fail(`g09_allocator_${label}_owner_invalid`);
  }
  for (const customer of customers) {
    if (!nonNegative(customer.balanceCents)) fail('g09_allocator_customer_balance_invalid');
    const customerServices = services.filter(row => row.customerId === customer.id).length;
    if (customerServices > maxServicesPerCustomer) fail('g09_allocator_customer_service_limit');
  }

  const state = new Map(customers.map(row => [row.id, { writes: 0, spendCents: 0, services: services.filter(item => item.customerId === row.id).length }]));
  const usedInvoices = new Set(), usedServices = new Set(), supplierWrites = new Map(supplierAccounts.map(row => [row.id, 0]));
  const leases = new Map();
  let unknownWriteCount = 0;

  function chooseCustomer(flowId, explicitCustomerId, lane) {
    const pool = lane === 'hot' ? customers.slice(0, Math.ceil(customers.length * 0.2)) :
      lane === 'normal' ? customers.slice(Math.ceil(customers.length * 0.2)) : customers;
    const id = explicitCustomerId ?? pool[stableIndex(flowId, pool.length)].id;
    if (!customerById.has(id)) fail('g09_allocator_customer_unknown');
    if (lane === 'hot' && id > customers[Math.ceil(customers.length * 0.2) - 1].id) fail('g09_allocator_customer_lane_mismatch');
    if (lane === 'normal' && id <= customers[Math.ceil(customers.length * 0.2) - 1].id) fail('g09_allocator_customer_lane_mismatch');
    return customerById.get(id);
  }

  function reserve(request) {
    if (!request || typeof request !== 'object' || Array.isArray(request) || typeof request.flowId !== 'string' || !/^[A-Za-z0-9_-]{1,96}$/.test(request.flowId)) fail('g09_allocator_request_invalid');
    if (Object.keys(request).some(key => !['flowId', 'kind', 'customerId', 'lane', 'amountCents', 'invoiceId', 'serviceId', 'supplierAccountId'].includes(key))) fail('g09_allocator_request_invalid');
    if (leases.has(request.flowId)) fail('g09_allocator_duplicate_lease');
    if (request.lane !== undefined && request.lane !== 'hot' && request.lane !== 'normal') fail('g09_allocator_customer_lane_invalid');
    const unknown = request.kind === 'unknown_supplier_write';
    if (!unknown && !kinds.has(request.kind)) fail('g09_allocator_unknown_write_isolated');
    if (unknown && request.kind !== 'unknown_supplier_write') fail('g09_allocator_unknown_write_isolated');

    const customer = chooseCustomer(request.flowId, request.customerId, request.lane);
    const usage = state.get(customer.id);
    if (usage.writes >= maxWritesPerCustomer) fail('g09_allocator_customer_write_limit');
    const amountCents = request.amountCents ?? 0;
    if (!nonNegative(amountCents) || amountCents > maxSpendPerWriteCents || usage.spendCents + amountCents > maxSpendPerCustomerCents || amountCents > customer.balanceCents - usage.spendCents) fail('g09_allocator_funds_exhausted');
    if ((request.kind === 'purchase_local' || request.kind === 'purchase_supplier') && usage.services >= maxServicesPerCustomer) fail('g09_allocator_customer_service_limit');

    let invoice = null, service = null, supplierAccount = null;
    if (request.invoiceId !== undefined) {
      invoice = invoiceById.get(request.invoiceId);
      if (!invoice || invoice.customerId !== customer.id) fail('g09_allocator_invoice_owner_mismatch');
      if (usedInvoices.has(invoice.id)) fail('g09_allocator_invoice_exhausted');
    }
    if (request.serviceId !== undefined || request.kind === 'local_renew') {
      if (request.serviceId === undefined) {
        const available = services.filter(row => row.customerId === customer.id && !usedServices.has(row.id));
        if (available.length === 0) fail('g09_allocator_services_exhausted');
        service = available[stableIndex(request.flowId, available.length)];
      } else service = serviceById.get(request.serviceId);
      if (!service || service.customerId !== customer.id) fail('g09_allocator_service_owner_mismatch');
      if (request.kind !== 'local_renew') fail('g09_allocator_service_kind_invalid');
      if (usedServices.has(service.id)) fail('g09_allocator_service_busy');
    }

    if (request.kind === 'purchase_supplier') {
      supplierAccount = request.supplierAccountId === undefined ? supplierAccounts[stableIndex(request.flowId, supplierAccounts.length)] : supplierAccounts.find(row => row.id === request.supplierAccountId);
      if (!supplierAccount) fail('g09_allocator_supplier_accounts_exhausted');
      if ((supplierWrites.get(supplierAccount.id) ?? 0) >= maxWritesPerSupplierAccount) fail('g09_allocator_supplier_account_limit');
    }
    if (unknown) {
      if (request.supplierAccountId !== unknownSupplierAccount.id || unknownWriteCount >= unknownSupplierWrites) fail('g09_allocator_unknown_supplier_quota');
      supplierAccount = unknownSupplierAccount;
    } else if (request.supplierAccountId !== undefined && request.kind !== 'purchase_supplier') fail('g09_allocator_supplier_account_kind_invalid');

    const lease = Object.freeze({
      flowId: request.flowId, kind: request.kind, customerId: customer.id, amountCents,
      invoiceId: invoice?.id ?? null, serviceId: service?.id ?? null,
      supplierAccountId: supplierAccount?.id ?? null, isolated: unknown,
    });
    usage.writes++;
    usage.spendCents += amountCents;
    if (request.kind === 'purchase_local' || request.kind === 'purchase_supplier') usage.services++;
    if (invoice) usedInvoices.add(invoice.id);
    if (service) usedServices.add(service.id);
    if (supplierAccount && request.kind === 'purchase_supplier') supplierWrites.set(supplierAccount.id, supplierWrites.get(supplierAccount.id) + 1);
    if (unknown) unknownWriteCount++;
    leases.set(request.flowId, lease);
    return lease;
  }

  function snapshot() {
    return Object.freeze({
      mode: 'allocation_only', profileData: Object.freeze({ ...profile.data }),
      capacityVerified: false, businessSuccessGenerated: false,
      customerUsage: Object.freeze(Object.fromEntries([...state].map(([id, value]) => [id, Object.freeze({ ...value })]))),
      leasedFlowCount: leases.size, leasedInvoiceCount: usedInvoices.size,
      leasedServiceCount: usedServices.size, supplierAccountUsage: Object.freeze(Object.fromEntries(supplierWrites)),
      isolatedUnknownWrites: unknownWriteCount,
    });
  }

  return Object.freeze({ reserve, snapshot });
}
