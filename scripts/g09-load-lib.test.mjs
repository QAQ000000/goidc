import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { runG09Load, validateG09Profile, performG09Fetch } from './g09-load-lib.mjs';

class Clock {
  time = 0; timers = new Map(); id = 0;
  now = () => this.time;
  setTimeout = (fn, delay) => { const id = ++this.id; this.timers.set(id, { at: this.time + delay, fn }); return id; };
  clearTimeout = id => this.timers.delete(id);
  async flush() { for (let n = 0; n < 12; n++) await Promise.resolve(); }
  async advance(end, stalled = false) {
    await this.flush();
    if (stalled) this.time = end;
    while (true) {
      const entry = [...this.timers].sort((a,b) => a[1].at-b[1].at || a[0]-b[0]).find(([, v]) => v.at <= end);
      if (!entry) break;
      this.timers.delete(entry[0]); if (!stalled) this.time = entry[1].at;
      entry[1].fn(); await this.flush();
    }
    this.time = end; await this.flush();
  }
}
const profile = changes => ({ origin: 'http://127.0.0.1:12345', maxConcurrency: 2, maxOperations: 100, requestTimeoutMs: 1000, maxScheduleLagMs: 5, drainTimeoutMs: 100, phases: [{ name: 'steady', durationMs: 100, streams: [{ category: 'read', unit: 'http_request', ratePerSecond: 100 }] }], ...changes });
const delayed = (clock, delay, status = 200) => ({ signal }) => new Promise((resolve, reject) => {
  const timer = clock.setTimeout(() => resolve({ status }), delay);
  signal.addEventListener('abort', () => { clock.clearTimeout(timer); reject(new Error('private secret payload')); }, { once: true });
});

test('fixed target/profile validation rejects redirects of intent and unbounded work before performing', async () => {
  for (const origin of ['http://localhost:12345', 'http://127.0.0.2:12345', 'http://127.0.0.1', 'https://127.0.0.1:12345', 'http://user:secret@127.0.0.1:12345', 'http://127.0.0.1:12345/path']) assert.throws(() => validateG09Profile(profile({ origin })));
  assert.throws(() => validateG09Profile(profile({ maxOperations: 1 })));
  assert.throws(() => validateG09Profile());
  let called = false;
  await assert.rejects(runG09Load({ profile: null, perform: () => { called = true; } }));
  assert.equal(called, false);
});

test('open-loop overload drops slots without slowing the offered schedule or hiding 409', async () => {
  const clock = new Clock(), rows = [];
  const pending = runG09Load({ profile: profile({ maxConcurrency: 1 }), clock, record: row => rows.push(row), perform: delayed(clock, 25, 409) });
  await clock.advance(200); const result = await pending;
  assert.equal(result.total.planned, 10); assert.equal(result.total.started, 4); assert.equal(result.total.dropped, 6);
  assert.equal(result.total.completed, 4); assert.equal(result.total.succeeded, 0); assert.equal(result.total.accepted, 0); assert.equal(result.total.failed, 4);
  assert.deepEqual(rows.filter(row => row.event === 'started').map(row => row.plannedAt), [0,30,60,90]);
  assert.equal(result.total.latency.maxMs, 25); assert.equal(result.total.latency.p99UpperMs, 25);
  assert.equal(result.ok, false); assert.equal(result.recordsRetained, 0);
});

test('event-loop stall records elapsed slots as misses rather than a catch-up burst', async () => {
  const clock = new Clock(), starts = [];
  const pending = runG09Load({ profile: profile(), clock, perform: data => { starts.push(data.plannedAt); return { status: 200 }; } });
  await clock.flush(); await clock.advance(55, true); await clock.advance(200);
  const result = await pending;
  assert.deepEqual(starts, [0,50,60,70,80,90]);
  assert.equal(result.total.dropped, 4); assert.equal(result.total.scheduleLag.maxMs, 45);
  assert.equal(result.total.planned, 10);
});

test('timeout aborts the actual attempt; an abort-ignoring performer is unfinished at the bounded drain deadline', async () => {
  const clock = new Clock(); let aborted = false;
  const pending = runG09Load({ profile: profile({ maxConcurrency: 1, requestTimeoutMs: 15 }), clock, perform: ({ signal }) => { signal.addEventListener('abort', () => { aborted = true; }); return new Promise(() => {}); } });
  await clock.advance(200); const result = await pending;
  assert.equal(aborted, true); assert.equal(result.endedAt, 200); assert.equal(result.total.started, 1);
  assert.equal(result.total.timeout, 1); assert.equal(result.total.unfinished, 1); assert.equal(result.total.completed, 0); assert.equal(result.total.failed, 1);
  assert.equal(result.total.succeeded, 0); assert.equal(result.ok, false); assert.equal(clock.timers.size, 0);
});

test('cancellation drops all future slots, aborts active network work and does not expose rejection secrets', async () => {
  const clock = new Clock(), controller = new AbortController(), rows = [];
  const pending = runG09Load({ profile: profile(), clock, signal: controller.signal, record: row => rows.push(row), perform: delayed(clock, 500) });
  await clock.advance(5); controller.abort(); await clock.advance(105); const result = await pending;
  assert.equal(result.reason, 'cancelled'); assert.equal(result.total.planned, 10); assert.equal(result.total.started, 1); assert.equal(result.total.dropped, 9);
  assert.equal(result.total.cancelled, 1); assert.equal(result.total.completed, 1); assert.equal(result.total.unfinished, 0);
  assert.equal(JSON.stringify(rows).includes('secret'), false);
});

test('phase/category statistics preserve units and actual successful throughput including drain', async () => {
  const clock = new Clock();
  const pending = runG09Load({ profile: profile({ phases: [{ name: 'one', durationMs: 100, streams: [{ category: 'checkout', unit: 'business_transaction', ratePerSecond: 10 }] }, { name: 'two', durationMs: 100, streams: [{ category: 'read', unit: 'http_request', ratePerSecond: 10 }] }] }), clock, perform: delayed(clock, 50) });
  await clock.advance(300); const result = await pending;
  assert.equal(result.ok, true); assert.equal(result.total.planned, 2); assert.equal(result.total.succeeded, 2);
  assert.equal(result.total.successfulThroughputPerSecond, 10); assert.equal(result.categories.checkout.unit, 'business_transaction');
  assert.equal(result.phases.one.succeeded, 1); assert.equal(result.phaseCategories['two/read'].completed, 1);
  assert.equal(result.categories.read.latency.p95UpperMs, 50);
});

test('recorder failure stops all new dispatch and produces explicit failed run', async () => {
  const clock = new Clock(); let calls = 0;
  const pending = runG09Load({ profile: profile(), clock, record: () => { throw new Error('disk full secret'); }, perform: () => { calls++; return { status: 200 }; } });
  await clock.advance(200); const result = await pending;
  assert.equal(result.reason, 'record_failed'); assert.equal(result.ok, false); assert.equal(calls, 0); assert.equal(result.total.planned, 10);
});

test('default fetch refuses an actual redirect without contacting redirected endpoint', async () => {
  let targetCalls = 0;
  const server = http.createServer((request, response) => {
    if (request.url === '/target') { targetCalls++; response.end('secret'); } else { response.writeHead(302, { location: '/target' }); response.end(); }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  try { await assert.rejects(performG09Fetch({ origin: `http://127.0.0.1:${server.address().port}`, signal: new AbortController().signal })); assert.equal(targetCalls, 0); }
  finally { await new Promise(resolve => server.close(resolve)); }
});

test('late successful completion uses actual phase observation duration; accepted business failure stays failed', async () => {
  const clock = new Clock();
  const pending = runG09Load({ profile: profile({ phases: [{ name: 'short', durationMs: 10, streams: [{ category: 'transaction', unit: 'business_transaction', ratePerSecond: 100 }] }] }), clock, perform: delayed(clock, 25) });
  await clock.advance(100); const result = await pending;
  assert.equal(result.endedAt, 25); assert.equal(result.total.successfulThroughputPerSecond, 40);
  assert.equal(result.phases.short.measurementDurationMs, 25); assert.equal(result.phases.short.successfulThroughputPerSecond, 40);
  const secondClock = new Clock();
  const business = runG09Load({ profile: profile({ phases: [{ name: 'short', durationMs: 10, streams: [{ category: 'transaction', unit: 'business_transaction', ratePerSecond: 100 }] }] }), clock: secondClock, perform: () => ({ status: 200, accepted: true, succeeded: false, body: 'never logged' }) });
  await secondClock.advance(20); const failed = await business;
  assert.equal(failed.total.accepted, 1); assert.equal(failed.total.succeeded, 0); assert.equal(failed.total.failed, 1);
});

test('cooperative timeout abort releases its slot and counts a failed completed attempt', async () => {
  const clock = new Clock();
  const pending = runG09Load({ profile: profile({ phases: [{ name: 'short', durationMs: 10, streams: [{ category: 'read', unit: 'http_request', ratePerSecond: 100 }] }], requestTimeoutMs: 15 }), clock, perform: delayed(clock, 500) });
  await clock.advance(100); const result = await pending;
  assert.equal(result.endedAt, 15); assert.equal(result.total.timeout, 1); assert.equal(result.total.completed, 1);
  assert.equal(result.total.failed, 1); assert.equal(result.total.unfinished, 0); assert.equal(clock.timers.size, 0);
});

test('already cancelled run dispatches nothing and safely handles prototype-shaped labels', async () => {
  const controller = new AbortController(); controller.abort(); const clock = new Clock(); let calls = 0;
  const result = await runG09Load({ profile: profile({ phases: [{ name: '__proto__', durationMs: 10, streams: [{ category: 'constructor', unit: 'http_request', ratePerSecond: 100 }] }] }), clock, signal: controller.signal, perform: () => { calls++; return { status: 200 }; } });
  assert.equal(calls, 0); assert.equal(result.total.dropped, 1); assert.equal(result.phases.__proto__.dropped, 1);
});
