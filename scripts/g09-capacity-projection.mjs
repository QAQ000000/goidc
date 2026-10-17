const fail = code => { throw new Error(code); };
const topKeys = ['customers', 'services', 'invoices', 'supplierAccounts', 'unknownSupplierAccount'];
const rowKeys = Object.freeze({
  customers: ['id', 'balanceCents'],
  services: ['id', 'customerId'],
  invoices: ['id', 'customerId', 'amountCents'],
  supplierAccounts: ['id'],
  unknownSupplierAccount: ['id'],
});
const fixedCounts = Object.freeze({ customers: 1000, services: 2000, invoices: 10000, suppliers: 10 });
const sensitive = /(?:password|passwd|secret|token|credential|authorization|cookie|database|db|dsn|sql|query|header|connection|private.?key)/i;
const statusKeys = new Set(['capacityVerified', 'businessSuccessGenerated']);

function plainObject(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value) &&
    (Object.getPrototypeOf(value) === Object.prototype || Object.getPrototypeOf(value) === null);
}

function inspectForbidden(value, path = '$') {
  if (Array.isArray(value)) { value.forEach((item, index) => inspectForbidden(item, `${path}[${index}]`)); return; }
  if (!plainObject(value)) return;
  for (const [key, child] of Object.entries(value)) {
    if (sensitive.test(key)) fail(`g09_projection_sensitive_field:${path}.${key}`);
    if (statusKeys.has(key) && child === true) fail(`g09_projection_status_true:${path}.${key}`);
    inspectForbidden(child, `${path}.${key}`);
  }
}

function exactKeys(value, allowed, code) {
  if (!plainObject(value) || Object.keys(value).sort().join(',') !== [...allowed].sort().join(',')) fail(code);
}

function positiveID(value, code) {
  if (!Number.isSafeInteger(value) || value <= 0) fail(code);
}

function fixedRows(rows, key, expectedCount) {
  if (!Array.isArray(rows) || rows.length !== expectedCount) fail(`g09_projection_${key}_count`);
  const seen = new Set();
  for (const row of rows) {
    exactKeys(row, rowKeys[key], `g09_projection_${key}_fields`);
    positiveID(row.id, `g09_projection_${key}_id`);
    if (seen.has(row.id)) fail(`g09_projection_${key}_duplicate`);
    seen.add(row.id);
  }
  return rows;
}

/** Validate detached JSON emitted by the Go g09projection package. */
export function parseG09CapacityProjection(input) {
  let value = input;
  if (typeof input === 'string') {
    try { value = JSON.parse(input); } catch { fail('g09_projection_json_invalid'); }
  }
  if (!plainObject(value)) fail('g09_projection_plain_object_required');
  inspectForbidden(value);
  exactKeys(value, topKeys, 'g09_projection_top_level_fields');
  const customers = fixedRows(value.customers, 'customers', fixedCounts.customers);
  const services = fixedRows(value.services, 'services', fixedCounts.services);
  const invoices = fixedRows(value.invoices, 'invoices', fixedCounts.invoices);
  const supplierAccounts = fixedRows(value.supplierAccounts, 'supplierAccounts', fixedCounts.suppliers);
  exactKeys(value.unknownSupplierAccount, rowKeys.unknownSupplierAccount, 'g09_projection_unknown_supplier_fields');
  positiveID(value.unknownSupplierAccount.id, 'g09_projection_unknown_supplier_id');
  if (supplierAccounts.some(row => row.id === value.unknownSupplierAccount.id)) fail('g09_projection_unknown_supplier_overlap');

  const customerIDs = new Set(customers.map(row => row.id));
  for (const row of customers) if (!Number.isSafeInteger(row.balanceCents) || row.balanceCents < 0) fail('g09_projection_customer_amount');
  for (const row of services) if (!customerIDs.has(row.customerId)) fail('g09_projection_service_owner');
  for (const row of invoices) {
    if (!customerIDs.has(row.customerId)) fail('g09_projection_invoice_owner');
    if (!Number.isSafeInteger(row.amountCents) || row.amountCents < 0) fail('g09_projection_invoice_amount');
  }
  return Object.freeze({
    customers: Object.freeze(customers.map(row => Object.freeze({ id: row.id, balanceCents: row.balanceCents }))),
    services: Object.freeze(services.map(row => Object.freeze({ id: row.id, customerId: row.customerId }))),
    invoices: Object.freeze(invoices.map(row => Object.freeze({ id: row.id, customerId: row.customerId, amountCents: row.amountCents }))),
    supplierAccounts: Object.freeze(supplierAccounts.map(row => Object.freeze({ id: row.id }))),
    unknownSupplierAccount: Object.freeze({ id: value.unknownSupplierAccount.id }),
  });
}

