import { performance } from 'node:perf_hooks';

const defaultClock = { now: () => performance.now(), setTimeout, clearTimeout };
export const G09_HISTOGRAM_BOUNDS = Object.freeze([1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 30000, 60000, 120000, 600000, Infinity]);
const bounds = G09_HISTOGRAM_BOUNDS;
const counters = () => ({ planned: 0, started: 0, dispatched: 0, completed: 0, accepted: 0, succeeded: 0, failed: 0, timeout: 0, dropped: 0, cancelled: 0, unfinished: 0 });
const histogram = () => ({ count: 0, sum: 0, max: 0, buckets: bounds.map(() => 0) });
function observe(h, value) { h.count++; h.sum += value; h.max = Math.max(h.max, value); h.buckets[bounds.findIndex(bound => value <= bound)]++; }
// Null quantiles mean no samples, never an SLO pass (JS null <= limit is true).
// Upper estimates may conservatively fail a limit between histogram boundaries.
function metric(h) {
  const quantile = p => { if (!h.count) return null; let count = 0; for (let i = 0; i < bounds.length; i++) { count += h.buckets[i]; if (count >= Math.ceil(h.count * p)) return Math.min(bounds[i], h.max); } };
  return { count: h.count, meanMs: h.count ? h.sum / h.count : null, maxMs: h.count ? h.max : null, p95UpperMs: quantile(.95), p99UpperMs: quantile(.99), buckets: bounds.map((upper, i) => ({ upperMs: Number.isFinite(upper) ? upper : null, count: h.buckets[i] })) };
}
const group = () => ({ ...counters(), latency: histogram(), admissionLag: histogram(), scheduleLag: histogram() });
const label = value => typeof value === 'string' && /^[a-zA-Z0-9_-]{1,48}$/.test(value);
const integer = (value, min, max) => Number.isSafeInteger(value) && value >= min && value <= max;

export function validateG09Profile(profile) {
  if (!profile || typeof profile !== 'object') throw new Error('Fixed load profile required');
  let target;
  try { target = new URL(profile.origin); } catch { throw new Error('Explicit loopback origin required'); }
  if (target.protocol !== 'http:' || target.hostname !== '127.0.0.1' || (!target.port || Number(target.port) < 1) || target.username || target.password || target.pathname !== '/' || target.search || target.hash || target.origin !== profile.origin) throw new Error('Only canonical http://127.0.0.1:<port> origin allowed');
  for (const [key, min, max] of [['maxConcurrency', 1, 1024], ['maxOperations', 1, 100000], ['requestTimeoutMs', 1, 600000], ['maxScheduleLagMs', 0, 60000], ['drainTimeoutMs', 1, 600000]]) if (!integer(profile[key], min, max)) throw new Error(`Invalid ${key}`);
  if (!Array.isArray(profile.phases) || !profile.phases.length || profile.phases.length > 16) throw new Error('Bounded phases required');
  let offset = 0, total = 0;
  const names = new Set(), categories = new Map(), streams = [];
  for (const phase of profile.phases) {
    if (!phase || !label(phase.name) || names.has(phase.name) || !integer(phase.durationMs, 1, 3600000) || !Array.isArray(phase.streams) || !phase.streams.length || phase.streams.length > 32) throw new Error('Invalid phase');
    names.add(phase.name); const phaseCategories = new Set();
    for (const stream of phase.streams) {
      if (!stream || !label(stream.category) || !['http_request', 'business_transaction'].includes(stream.unit) || (categories.has(stream.category) && categories.get(stream.category) !== stream.unit) || phaseCategories.has(stream.category) || !Number.isFinite(stream.ratePerSecond) || stream.ratePerSecond <= 0 || stream.ratePerSecond > 10000) throw new Error('Invalid stream');
      categories.set(stream.category, stream.unit); phaseCategories.add(stream.category);
      // Slots are [phase start, phase end); no burst-sized task array is allocated.
      const count = Math.ceil(phase.durationMs * stream.ratePerSecond / 1000);
      total += count;
      if (total > profile.maxOperations || categories.size > 32) throw new Error('Planned workload exceeds budget');
      streams.push({ phase: phase.name, category: stream.category, unit: stream.unit, offset, duration: phase.durationMs, interval: 1000 / stream.ratePerSecond, count });
    }
    offset += phase.durationMs;
    if (offset > 3600000) throw new Error('Total duration exceeds budget');
  }
  return { origin: target.origin, durationMs: offset, planned: total, streams };
}

// A detached, canonical copy binds reports to the actual admission/deadline
// budgets as well as the phase plan. No credentials or arbitrary input fields.
export function canonicalG09Profile(profile) {
  validateG09Profile(profile);
  const { origin, maxConcurrency, maxOperations, requestTimeoutMs, maxScheduleLagMs, drainTimeoutMs } = profile;
  return { origin, maxConcurrency, maxOperations, requestTimeoutMs, maxScheduleLagMs, drainTimeoutMs,
    phases: profile.phases.map(p => ({ name: p.name, durationMs: p.durationMs, streams: p.streams.map(s => ({ category: s.category, unit: s.unit, ratePerSecond: s.ratePerSecond })) })) };
}

// This helper enforces redirects and target policy. Injected performers are trusted
// controller code and must enforce the same policy for every transaction request.
export async function performG09Fetch({ origin, signal, pathname = '/' }) {
  validateG09Origin(origin);
  if (typeof pathname !== 'string' || !pathname.startsWith('/') || pathname.startsWith('//') || new URL(pathname, origin).origin !== origin) throw new Error('Same-origin path required');
  const response = await fetch(new URL(pathname, origin), { signal, redirect: 'error' });
  // Consume the body so completion measures the whole HTTP request, not headers.
  if (response.body) { const reader = response.body.getReader(); let bytes = 0; try { while (true) { const part = await reader.read(); if (part.done) break; bytes += part.value.byteLength; if (bytes > 1048576) { await reader.cancel(); throw new Error('Response body budget exceeded'); } } } finally { reader.releaseLock(); } }
  return { status: response.status };
}
function validateG09Origin(origin) {
  validateG09Profile({ origin, phases: [{ name: 'validation', durationMs: 1, streams: [{ category: 'validation', unit: 'http_request', ratePerSecond: 1 }] }], maxConcurrency: 1, maxOperations: 1, requestTimeoutMs: 1, maxScheduleLagMs: 0, drainTimeoutMs: 1 });
}

/**
 * Fixed open-loop slots, one attempt per work unit (never automatic retries).
 * perform is trusted controller code: it owns subrequests and redirect policy.
 * record is synchronous/streaming; no payload, headers, errors or responses are
 * retained. Slow/throwing recorders affect measured capacity and cannot hide it.
 * Abort-ignoring performers are failed/unfinished after the bounded drain; JS
 * cannot forcibly terminate their arbitrary side effects. Controllers must use
 * abortable I/O and own process teardown. This drain is not worker backlog drain.
 */
export async function runG09Load({ profile, perform = performG09Fetch, record = () => {}, clock = defaultClock, signal } = {}) {
  profile = canonicalG09Profile(profile);
  const plan = validateG09Profile(profile);
  if (typeof perform !== 'function' || typeof record !== 'function' || !clock || !['now', 'setTimeout', 'clearTimeout'].every(key => typeof clock[key] === 'function')) throw new Error('Invalid load callbacks');
  // Copy all configuration before yielding; caller mutations cannot change a run.
  const config = { maxConcurrency: profile.maxConcurrency, requestTimeoutMs: profile.requestTimeoutMs, maxScheduleLagMs: profile.maxScheduleLagMs, drainTimeoutMs: profile.drainTimeoutMs };
  const startedAt = clock.now(), total = group(), phases = Object.create(null), categories = Object.create(null), phaseCategories = Object.create(null);
  const streams = plan.streams.map(stream => ({ ...stream, index: 0 }));
  for (const stream of streams) { phases[stream.phase] ??= group(); categories[stream.category] ??= group(); phaseCategories[`${stream.phase}/${stream.category}`] ??= group(); }
  const active = new Map(); let nextId = 0, timer, drainTimer, stopped = false, finalized = false, stopAt, drainDeadline, reason = null, recordFailed = false;
  const groups = stream => [total, phases[stream.phase], categories[stream.category], phaseCategories[`${stream.phase}/${stream.category}`]];
  const increment = (stream, key) => { for (const g of groups(stream)) g[key]++; };
  let finish;
  const result = new Promise(resolve => { finish = resolve; });
  function emit(row) {
    if (recordFailed) return;
    try { const returned = record(Object.freeze(row)); if (returned && typeof returned.then === 'function') { returned.catch?.(() => {}); throw new Error('Async recorder unsupported'); } }
    catch { recordFailed = true; reason = 'record_failed'; stop('record_failed'); }
  }
  function drop(stream, index, why, now) {
    increment(stream, 'planned'); increment(stream, 'dropped');
    const plannedAt = startedAt + stream.offset + index * stream.interval;
    for (const g of groups(stream)) observe(g.scheduleLag, Math.max(0, now - plannedAt));
    emit({ event: 'dropped', id: ++nextId, phase: stream.phase, category: stream.category, unit: stream.unit, attempt: 1, plannedAt, observedAt: now, scheduleLagMs: Math.max(0, now - plannedAt), reason: why });
  }
  function complete(task, response, rejected = false, unfinished = false) {
    if (finalized || !active.has(task.id)) return;
    if (clock.now() >= task.deadline) abort(task, 'timeout');
    // A response microtask may run before an overdue drain timer. It cannot
    // turn work unobserved at the drain boundary into a successful run.
    if (stopped && clock.now() >= drainDeadline) unfinished = true;
    if (unfinished) abort(task, 'cancelled');
    clock.clearTimeout(task.timer); active.delete(task.id);
    if (task.dispatchedAt === null) for (const g of groups(task.stream)) observe(g.scheduleLag, Math.max(0, clock.now() - task.plannedAt));
    phases[task.stream.phase].lastEndedAt = clock.now();
    const endedAt = clock.now(), status = Number.isInteger(response?.status) && response.status >= 100 && response.status <= 599 ? response.status : null;
    const httpOK = status !== null && status >= 200 && status < 300;
    const accepted = !rejected && !task.reason && httpOK && response.accepted !== false;
    const succeeded = accepted && response.succeeded !== false;
    if (!unfinished) increment(task.stream, 'completed');
    if (accepted) increment(task.stream, 'accepted');
    increment(task.stream, succeeded ? 'succeeded' : 'failed');
    if (task.reason === 'timeout') increment(task.stream, 'timeout');
    if (task.reason === 'cancelled') increment(task.stream, 'cancelled');
    if (unfinished) increment(task.stream, 'unfinished');
    if (!unfinished) for (const g of groups(task.stream)) observe(g.latency, Math.max(0, endedAt - task.startedAt));
    emit({ event: 'finished', id: task.id, phase: task.stream.phase, category: task.stream.category, unit: task.stream.unit, attempt: 1, plannedAt: task.plannedAt, startedAt: task.startedAt, admittedAt: task.startedAt, deadlineAt: task.deadline, dispatched: task.dispatchedAt !== null, dispatchedAt: task.dispatchedAt, dispatchLagMs: task.dispatchedAt === null ? null : task.dispatchedAt - task.plannedAt, admissionLagMs: task.startedAt - task.plannedAt, endedAt, scheduleLagMs: (task.dispatchedAt ?? endedAt) - task.plannedAt, latencyMs: unfinished ? null : Math.max(0, endedAt - task.startedAt), status, accepted, succeeded, completed: !unfinished, reason: unfinished ? 'unfinished' : task.reason ?? (rejected ? 'request_failed' : succeeded ? null : 'not_successful') });
    maybeFinish();
  }
  function abort(task, why) { if (!task.reason) { task.reason = why; task.controller.abort(); } }
  function launch(stream, index, now) {
    const id = ++nextId, plannedAt = startedAt + stream.offset + index * stream.interval;
    const task = { id, stream, plannedAt, startedAt: now, deadline: now + config.requestTimeoutMs, dispatchedAt: null, controller: new AbortController(), reason: null };
    active.set(id, task); increment(stream, 'planned'); increment(stream, 'started');
    for (const g of groups(stream)) observe(g.admissionLag, now - plannedAt);
    task.timer = clock.setTimeout(() => abort(task, 'timeout'), config.requestTimeoutMs);
    emit({ event: 'started', id, phase: stream.phase, category: stream.category, unit: stream.unit, attempt: 1, plannedAt, startedAt: now, admittedAt: now, deadlineAt: task.deadline, admissionLagMs: now - plannedAt });
    if (stopped) abort(task, 'cancelled');
    Promise.resolve().then(() => {
      const dispatchNow = clock.now();
      // Timers cannot run during synchronous recorder/performer work. Absolute
      // deadlines are authoritative, both here and in complete().
      if (dispatchNow >= task.deadline) abort(task, 'timeout');
      else if (dispatchNow >= startedAt + stream.offset + stream.duration || dispatchNow - plannedAt > config.maxScheduleLagMs) abort(task, 'schedule_late');
      if (task.controller.signal.aborted) throw new Error('Aborted before dispatch');
      task.dispatchedAt = dispatchNow; increment(stream, 'dispatched');
      for (const g of groups(stream)) observe(g.scheduleLag, dispatchNow - plannedAt);
      return perform({ requestId: id, streamSequence: index, unit: stream.unit, origin: plan.origin, phase: stream.phase, category: stream.category, attempt: 1, plannedAt, startedAt: now, admittedAt: now, dispatchedAt: dispatchNow, deadlineAt: task.deadline, signal: task.controller.signal });
    }).then(response => complete(task, response), () => complete(task, null, true));
  }
  function maybeFinish() {
    if (finalized || !stopped || active.size) return;
    finalized = true; clock.clearTimeout(timer); clock.clearTimeout(drainTimer); signal?.removeEventListener('abort', cancelled);
    const endedAt = clock.now();
    const summarize = (g, duration) => { const { latency, admissionLag, scheduleLag, lastEndedAt, ...counts } = g; void lastEndedAt; return { ...counts, measurementDurationMs: duration, latency: metric(latency), admissionLag: metric(admissionLag), scheduleLag: metric(scheduleLag), successfulThroughputPerSecond: duration > 0 ? g.succeeded * 1000 / duration : 0 }; };
    const mapped = (obj, duration) => Object.fromEntries(Object.entries(obj).map(([key, value]) => [key, summarize(value, duration(key))]));
    const phaseDuration = name => { const stream = plan.streams.find(stream => stream.phase === name); return Math.max(0, Math.max(Math.min(stopAt, startedAt + stream.offset + stream.duration), phases[name].lastEndedAt ?? 0) - (startedAt + stream.offset)); };
    finish({ unit: 'work_unit', profile, workloadPlanned: plan.planned, ok: !reason && total.failed === 0 && total.dropped === 0, reason, origin: plan.origin, startedAt, stoppedAt: stopAt, drainDeadlineAt: drainDeadline, endedAt, plannedDurationMs: plan.durationMs, elapsedMs: endedAt - startedAt, total: summarize(total, endedAt - startedAt), phases: mapped(phases, phaseDuration), categories: Object.fromEntries(Object.entries(categories).map(([key, value]) => [key, { unit: streams.find(stream => stream.category === key).unit, ...summarize(value, endedAt - startedAt) }])), phaseCategories: Object.fromEntries(Object.entries(phaseCategories).map(([key, value]) => [key, { unit: streams.find(stream => `${stream.phase}/${stream.category}` === key).unit, ...summarize(value, phaseDuration(key.split('/')[0])) }])), quantiles: 'bounded histogram upper estimates; startedAt aliases admittedAt; started counts admissions, dispatched counts actual performer calls; latency includes recorder/scheduler overhead and excludes unfinished operations; scheduleLag measures dispatch, or terminal observation for undispatched work; admissionLag measures admission; controller HTTP subrequests are separate; throughput uses actual observation windows; work units are not necessarily HTTP requests', recordsRetained: 0 });
  }
  function stop(why = null) {
    if (stopped) return;
    stopped = true; reason = why; stopAt = clock.now(); drainDeadline = stopAt + config.drainTimeoutMs; clock.clearTimeout(timer);
    if (why) {
      for (const task of active.values()) abort(task, 'cancelled');
      for (const stream of streams) while (stream.index < stream.count) drop(stream, stream.index++, why, stopAt);
    }
    drainTimer = clock.setTimeout(() => {
      for (const task of [...active.values()]) { abort(task, 'cancelled'); complete(task, null, true, true); }
      maybeFinish();
    }, Math.max(0, drainDeadline - clock.now()));
    maybeFinish();
  }
  function tick() {
    if (stopped) return;
    let now = clock.now();
    for (const stream of streams) {
      now = clock.now();
      const due = Math.min(stream.count, Math.max(0, Math.floor((now - startedAt - stream.offset) / stream.interval) + 1));
      // Never replay a backlog of elapsed slots as a catch-up burst.
      while (stream.index < due) {
        const index = stream.index++, plannedAt = startedAt + stream.offset + index * stream.interval;
        if (index < due - 1 || now >= startedAt + stream.offset + stream.duration || now - plannedAt > config.maxScheduleLagMs) drop(stream, index, 'schedule_late', now);
        else if (active.size >= config.maxConcurrency) drop(stream, index, 'concurrency_limit', now);
        else launch(stream, index, now);
        if (stopped) return;
      }
    }
    if (now >= startedAt + plan.durationMs) { stop(); return; }
    now = clock.now();
    const next = Math.min(startedAt + plan.durationMs, ...streams.filter(stream => stream.index < stream.count).map(stream => startedAt + stream.offset + stream.index * stream.interval));
    timer = clock.setTimeout(tick, Math.max(0, next - clock.now()));
  }
  function cancelled() { stop('cancelled'); }
  if (signal?.aborted) cancelled(); else { signal?.addEventListener('abort', cancelled, { once: true }); tick(); }
  return result;
}
