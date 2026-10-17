import test from 'node:test';
import assert from 'node:assert/strict';
import { runG09Load } from './g09-load-lib.mjs';
import { assessG09Workload } from './g09-workload-report.mjs';

class Clock {
  time = 0; timers = new Map(); id = 0;
  now = () => this.time;
  setTimeout = (fn, delay) => { const id = ++this.id; this.timers.set(id, { at: this.time + delay, fn }); return id; };
  clearTimeout = id => this.timers.delete(id);
  async flush() { for (let n = 0; n < 12; n++) await Promise.resolve(); }
  async advance(end) {
    await this.flush();
    while (true) {
      const entry = [...this.timers].sort((a, b) => a[1].at - b[1].at || a[0] - b[0]).find(([, v]) => v.at <= end);
      if (!entry) break;
      this.timers.delete(entry[0]); this.time = entry[1].at; entry[1].fn(); await this.flush();
    }
    this.time = end; await this.flush();
  }
}
const fixedProfile = () => ({ origin: 'http://127.0.0.1:12345', maxConcurrency: 100, maxOperations: 1000, requestTimeoutMs: 1000, maxScheduleLagMs: 5, drainTimeoutMs: 2000, phases: [{ name: 'steady', durationMs: 1000, streams: [{ category: 'read', unit: 'http_request', ratePerSecond: 20 }, { category: 'transaction', unit: 'business_transaction', ratePerSecond: 5 }] }] });
async function run(perform) {
  const clock = new Clock(), profile = fixedProfile();
  const pending = runG09Load({ profile, clock, perform: perform ? ctx => perform(ctx, clock) : () => ({ status: 200 }) });
  await clock.advance(4000);
  return { profile, summary: await pending };
}
const failed = (report, suffix) => report.checks.some(c => c.name.endsWith(suffix) && !c.pass);

test('actual load-library result can pass work-unit gates while all capacity gates remain explicitly unverified', async () => {
  const { summary, profile } = await run();
  const report = assessG09Workload(summary, profile);
  assert.equal(report.workloadPassed, true, JSON.stringify(report));
  assert.equal(report.capacityVerified, false);
  assert.equal(report.pendingGates.length, 4);
});

test('missing, null or falsified latency evidence never satisfies a read SLO', async () => {
  const { summary, profile } = await run();
  for (const change of [g => { g.latency.p99UpperMs = null; }, g => { g.latency.count = 0; }, g => { g.latency.buckets[0].count--; }, g => { g.latency.p99UpperMs = -1; }]) {
    const modified = structuredClone(summary); change(modified.phaseCategories['steady/read']);
    const report = assessG09Workload(modified, profile);
    assert.equal(report.workloadPassed, false); assert.equal(failed(report, ':latency_evidence'), true);
  }
});

test('409 and unfinished operations fail even with fast successful samples', async () => {
  const rejected = await run(({ streamSequence, category }) => ({ status: category === 'read' && streamSequence === 0 ? 409 : 200 }));
  const rejectionReport = assessG09Workload(rejected.summary, rejected.profile);
  assert.equal(rejectionReport.workloadPassed, false); assert.equal(failed(rejectionReport, ':failure_budget'), true);
  const hung = await run(({ category }) => category === 'transaction' ? new Promise(() => {}) : { status: 200 });
  const hungReport = assessG09Workload(hung.summary, hung.profile);
  assert.equal(hungReport.workloadPassed, false); assert.equal(failed(hungReport, ':no_unfinished_or_cancelled'), true);
});

test('throughput uses measured trailing duration, not configured rate or a forged throughput field', async () => {
  const { summary, profile } = await run();
  summary.phaseCategories['steady/read'].measurementDurationMs = 10000;
  summary.phaseCategories['steady/read'].successfulThroughputPerSecond = 1000000;
  const report = assessG09Workload(summary, profile);
  assert.equal(report.workloadPassed, false); assert.equal(failed(report, ':throughput'), true);
});

test('fixed plan mismatch, controller stop, totals mismatch or omitted category cannot pass', async () => {
  const { summary, profile } = await run();
  for (const change of [s => { s.workloadPlanned--; }, s => { s.reason = 'record_failed'; }, s => { s.total.planned--; }, s => { delete s.phaseCategories['steady/transaction']; }, s => { s.phaseCategories['steady/read'].succeeded++; }]) {
    const modified = structuredClone(summary); change(modified);
    assert.equal(assessG09Workload(modified, profile).workloadPassed, false);
  }
});

test('successes require actual dispatch and the exact frozen admission/deadline budgets', async () => {
  const { summary, profile } = await run();
  const modified = structuredClone(summary);
  modified.total.dispatched = 0;
  for (const group of Object.values(modified.phaseCategories)) group.dispatched = 0;
  assert.equal(assessG09Workload(modified, profile).workloadPassed, false);
  for (const key of ['maxConcurrency', 'requestTimeoutMs', 'maxScheduleLagMs', 'drainTimeoutMs', 'maxOperations']) {
    const stricter = structuredClone(profile); stricter[key]--;
    const report = assessG09Workload(summary, stricter);
    assert.equal(report.workloadPassed, false);
    assert.equal(failed(report, 'fixed_admission_and_deadline_budgets'), true);
  }
});

test('multi-request business latency is not mislabeled as individual HTTP write latency', async () => {
  const { summary, profile } = await run(({ category }, clock) => category === 'transaction' ? new Promise(resolve => clock.setTimeout(() => resolve({ status: 201 }), 600)) : { status: 200 });
  const report = assessG09Workload(summary, profile);
  assert.equal(report.checks.some(c => c.name === 'stream:steady/transaction:http_read_latency'), false);
  assert.equal(report.capacityVerified, false);
  assert.ok(report.pendingGates.includes('HTTP subrequest write latency and complete request evidence'));
});

test('shortening a category observation window cannot erase the real phase tail', async () => {
  const { summary, profile } = await run((ctx, clock) => new Promise(resolve => clock.setTimeout(() => resolve({ status: 200 }), 400)));
  assert.equal(assessG09Workload(summary, profile).workloadPassed, false);
  summary.phaseCategories['steady/read'].measurementDurationMs = 1000;
  summary.phaseCategories['steady/transaction'].measurementDurationMs = 1000;
  const report = assessG09Workload(summary, profile);
  assert.equal(report.workloadPassed, false); assert.equal(failed(report, ':phase_window_consistency'), true);
});

test('an arbitrary histogram with smaller boundaries cannot replace the producer fixed histogram', async () => {
  const { summary, profile } = await run();
  summary.phaseCategories['steady/read'].latency.buckets[9].upperMs = 999;
  const report = assessG09Workload(summary, profile);
  assert.equal(report.workloadPassed, false); assert.equal(failed(report, ':latency_evidence'), true);
});

test('delayed schedule stop or an overrun drain deadline cannot be hidden in rolled-up counts', async () => {
  const { summary, profile } = await run();
  const lateStop = structuredClone(summary);
  lateStop.stoppedAt += 1000; lateStop.endedAt += 1000; lateStop.drainDeadlineAt += 1000;
  lateStop.elapsedMs += 1000; lateStop.total.measurementDurationMs += 1000;
  assert.equal(failed(assessG09Workload(lateStop, profile), 'schedule_stop_observed_within_budget'), true);
  const lateDrain = structuredClone(summary);
  lateDrain.endedAt = lateDrain.drainDeadlineAt + 1;
  lateDrain.elapsedMs = lateDrain.endedAt - lateDrain.startedAt;
  lateDrain.total.measurementDurationMs = lateDrain.elapsedMs;
  assert.equal(failed(assessG09Workload(lateDrain, profile), 'absolute_drain_budget'), true);
});
