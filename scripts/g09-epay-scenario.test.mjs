import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { createG09EpaySettlementExecutor, validateG09EpayCheckout } from './g09-epay-scenario.mjs';

const merchantOrigin = 'http://127.0.0.1:39001', publicOrigin = 'http://localhost:3088', apiOrigin = 'http://127.0.0.1:39002', key = 'g09-private-synthetic-secret', cookie = 'zjmfgo_session=synthetic';
const digest = value => createHash('sha256').update(value).digest('hex');
// Independent signing oracle: sorted object keys, not the helper's entry list.
function signedCheckout(provider = 'epay_alipay', invoice = 7, changes = {}) {
  const data = { pid: '1000', type: provider.slice(5), notify_url: `${publicOrigin}/api/v1/payments/${provider}/webhook`, return_url: `${publicOrigin}/console/invoices/${invoice}`, out_trade_no: `epay_${invoice}_${'a'.repeat(32)}`, name: `账单 ${invoice}`, money: '10.00', ...changes };
  const input = Object.keys(data).sort().filter(k => data[k] !== '').map(k => `${k}=${data[k]}`).join('&');
  const sign = createHash('md5').update(input + key).digest('hex');
  return `${merchantOrigin}/submit.php?${new URLSearchParams({ ...data, sign, sign_type: 'MD5' })}`;
}
function checkoutConfig(extra = {}) { return { checkoutURL: signedCheckout(), merchantOrigin, publicOrigin, pid: '1000', key, provider: 'epay_alipay', invoiceId: 7, amountCents: 1000, ...extra }; }
function fixture({ changeRequest, fetchImpl, persistPrivate, record, ...options } = {}) {
  const privates = [], events = [], calls = [], callbacks = [];
  const request = async r => {
    calls.push(r);
    if (changeRequest) { const changed = await changeRequest(r); if (changed !== undefined) return changed; }
    if (r.path === '/api/v1/me') return { status: 200, data: { id: 1, role: 'customer' } };
    if (r.path.endsWith('/payments')) return { status: 201, data: { checkout_url: signedCheckout(r.body.provider, Number(r.path.split('/')[4])) } };
    const invoiceId = Number(r.path.split('/')[4]);
    return { status: 200, data: { id: invoiceId, user_id: 1, amount_cents: 1000, version: 1, status: r.step === 'invoice-after' ? 'paid' : 'unpaid', paid_at: r.step === 'invoice-after' ? '2026-10-17T00:00:00Z' : null } };
  };
  const actualFetch = async (url, init) => {
    callbacks.push({ url, ...init });
    return fetchImpl ? fetchImpl(url, init) : new Response('success', { status: 200 });
  };
  const execute = createG09EpaySettlementExecutor({ request, apiOrigin, merchantOrigin, publicOrigin, runId: 'epay-guard', pid: '1000', key, accounts: [{ id: 1, cookie, budgetCents: 10000 }], persistPrivate: persistPrivate ?? (r => privates.push(r)), record: record ?? (r => events.push(r)), fetchImpl: actualFetch, ...options });
  return { execute, privates, events, calls, callbacks };
}
const flow = (extra = {}) => ({ id: 'pay-1', kind: 'external_settle', customerId: 1, cookie, invoiceId: 7, amountCents: 1000, provider: 'epay_alipay', ...extra });

test('checkout validates exact signed loopback payment identities for both channels', () => {
  for (const provider of ['epay_alipay', 'epay_wxpay']) assert.equal(validateG09EpayCheckout(checkoutConfig({ checkoutURL: signedCheckout(provider), provider })).providerPaymentId, `epay_7_${'a'.repeat(32)}`);
});
test('signed wrong owner/amount/target/type and duplicate parameters never pass checkout admission', () => {
  for (const changes of [{ notify_url: `${apiOrigin}/bad` }, { return_url: `${publicOrigin}/console/invoices/8` }, { money: '10' }, { money: '10.01' }, { type: 'wxpay' }, { out_trade_no: `epay_8_${'a'.repeat(32)}` }, { pid: '1001' }]) assert.throws(() => validateG09EpayCheckout(checkoutConfig({ checkoutURL: signedCheckout('epay_alipay', 7, changes) })));
  for (const value of [`${signedCheckout()}&pid=1000`, signedCheckout().replace('submit.php', 'other'), signedCheckout().replace('39001', '39003'), `${signedCheckout()}#fragment`, signedCheckout().replace(/sign=[0-9a-f]+/, 'sign=' + '0'.repeat(32))]) assert.throws(() => validateG09EpayCheckout(checkoutConfig({ checkoutURL: value })));
});
test('both signed notifications reuse one persisted original and only public hashes are recorded', async () => {
  const f = fixture(); const result = await f.execute({ flow: flow() });
  assert.equal(result.succeeded, true); assert.equal(result.accountingVerified, false); assert.equal(result.capacityVerified, false);
  assert.deepEqual(f.calls.map(c => c.step), ['identity', 'invoice-before', 'intent', 'invoice-after']);
  assert.equal(f.privates.length, 1); assert.equal(f.callbacks.length, 2); assert.equal(f.callbacks[0].body, f.callbacks[1].body);
  assert.equal(f.privates[0].body, f.callbacks[0].body); assert.equal(f.privates[0].payloadSha256, digest(f.callbacks[0].body));
  const values = Object.fromEntries(new URLSearchParams(f.callbacks[0].body));
  const input = Object.keys(values).sort().filter(k => !['sign','sign_type'].includes(k) && values[k] !== '').map(k => `${k}=${values[k]}`).join('&');
  assert.equal(values.sign, createHash('md5').update(input + key).digest('hex'));
  for (const callback of f.callbacks) { assert.equal(callback.url, `${apiOrigin}/api/v1/payments/epay_alipay/webhook`); assert.equal(callback.headers.Cookie, undefined); assert.equal(callback.redirect, 'error'); }
  assert.equal(f.events.length, 4); assert.equal(f.events[2].duplicate, true);
  for (const secret of [cookie, key, f.callbacks[0].body, values.sign, values.out_trade_no]) assert.equal(JSON.stringify(f.events).includes(secret), false);
  await assert.rejects(f.execute({ flow: flow({ id: 'pay-2' }) }), /admission_refused/);
});
test('identity and invoice admission fail before any intent or callback', async () => {
  for (const step of ['identity','invoice-before']) {
    const f = fixture({ changeRequest: r => r.step === step ? { status: 200, data: { id: 2, role: 'customer' } } : undefined });
    await assert.rejects(f.execute({ flow: flow() })); assert.equal(f.calls.some(c => c.step === 'intent'), false); assert.equal(f.callbacks.length, 0);
  }
});
test('bad checkout after intent freezes customer before any callback and preserves reserved budget', async () => {
  const f = fixture({ changeRequest: r => r.step === 'intent' ? { status: 201, data: { checkout_url: `${apiOrigin}/unexpected` } } : undefined });
  await assert.rejects(f.execute({ flow: flow() })); assert.equal(f.callbacks.length, 0); assert.deepEqual(f.execute.snapshot().quarantinedCustomers, [1]); assert.equal(f.execute.snapshot().budgets[0].remainingCents, 9000);
  await assert.rejects(f.execute({ flow: flow({ id: 'new', invoiceId: 8 }) }), /admission_refused/);
});
test('original private persistence fails closed and async recorder cannot dispatch notification', async () => {
  for (const persistPrivate of [() => { throw new Error('PRIVATE BODY secret'); }, async () => {}]) {
    const f = fixture({ persistPrivate }); await assert.rejects(f.execute({ flow: flow() }), e => !e.message.includes('PRIVATE BODY'));
    assert.equal(f.callbacks.length, 0); assert.deepEqual(f.execute.snapshot().quarantinedCustomers, [1]);
  }
});
test('unknown first notification does not retry or submit the deliberate duplicate', async () => {
  const f = fixture({ fetchImpl: async () => { throw new Error('remote secret'); } });
  await assert.rejects(f.execute({ flow: flow() }), e => e.uncertain && !e.message.includes('secret'));
  assert.equal(f.callbacks.length, 1); assert.equal(f.events.at(-1).succeeded, false); assert.equal(f.events.at(-1).uncertain, true);
});
test('bounded callback reply rejects oversized, redirect, wrong success and invalid status without replay', async () => {
  for (const [body, status] of [['x'.repeat(65),200],['success',302],['success\n',200],['fail',500]]) {
    const f = fixture({ fetchImpl: async () => new Response(body,{ status }) }); await assert.rejects(f.execute({ flow: flow() })); assert.equal(f.callbacks.length, 1);
  }
});
test('whole-flow deadline includes callback wait and freezes unknown intent result', async () => {
  const f = fixture({ timeoutMs: 20, fetchImpl: async (_,init) => new Promise((_, reject) => { init.signal.addEventListener('abort', () => reject(new Error('aborted')), { once: true }); }) });
  // Keep the loop alive: AbortSignal timeout itself uses an unref timer.
  const hold = setTimeout(() => {}, 1000);
  try { await assert.rejects(f.execute({ flow: flow() }), e => e.uncertain); } finally { clearTimeout(hold); }
  assert.equal(f.callbacks.length, 1); assert.equal(f.calls.some(c => c.step === 'invoice-after'), false);
});
test('shared customer cannot overlap, cancellation stops new flows and no public capacity claim is set', async () => {
  let release; const ready = new Promise(resolve => { release = resolve; });
  const f = fixture({ changeRequest: async r => { if (r.step === 'identity') await ready; } });
  const first = f.execute({ flow: flow() });
  await assert.rejects(f.execute({ flow: flow({ id:'second', invoiceId:8 }) }), /admission_refused/);
  f.execute.stop(); release(); await assert.rejects(first); await assert.rejects(f.execute({ flow: flow({ id:'third',invoiceId:9 }) }), /stopped/);
  assert.equal(f.execute.snapshot().capacityVerified,false); assert.equal(f.callbacks.length,0);
});
