import { validateG09Profile, canonicalG09Profile, G09_HISTOGRAM_BOUNDS } from './g09-load-lib.mjs';

const counterNames = ['planned', 'started', 'dispatched', 'completed', 'accepted', 'succeeded', 'failed', 'timeout', 'dropped', 'cancelled', 'unfinished'];
const count = n => Number.isSafeInteger(n) && n >= 0;
const finite = n => typeof n === 'number' && Number.isFinite(n) && n >= 0;
const own = (object, key) => object && Object.hasOwn(object, key) ? object[key] : null;
function validCounters(g) {
  return g && counterNames.every(key => count(g[key])) &&
    g.planned === g.started + g.dropped && g.started === g.completed + g.unfinished && g.accepted <= g.dispatched &&
    g.started === g.succeeded + g.failed && g.accepted <= g.completed && g.succeeded <= g.accepted &&
    g.dispatched <= g.started && g.timeout <= g.failed && g.cancelled <= g.failed && g.unfinished <= g.failed;
}
function validLatency(g) {
  const h = g?.latency;
  if (!h || !count(h.count) || h.count !== g.completed || !Array.isArray(h.buckets) || h.buckets.length !== G09_HISTOGRAM_BOUNDS.length || h.buckets.some((b, i) => b.upperMs !== (Number.isFinite(G09_HISTOGRAM_BOUNDS[i]) ? G09_HISTOGRAM_BOUNDS[i] : null)) || h.buckets.reduce((n, b) => n + (count(b.count) ? b.count : NaN), 0) !== h.count) return false;
  let previous = 0, lastOccupiedLower = 0, lastOccupiedUpper = null;
  for (let i = 0; i < h.buckets.length; i++) {
    const b = h.buckets[i];
    if (b.count > 0) { lastOccupiedLower = previous; lastOccupiedUpper = b.upperMs; }
    if (b.upperMs === null) { if (i !== h.buckets.length - 1) return false; }
    else { if (!finite(b.upperMs) || b.upperMs <= previous) return false; previous = b.upperMs; }
  }
  if (h.count === 0) return h.p95UpperMs === null && h.p99UpperMs === null && h.maxMs === null;
  if (![h.p95UpperMs, h.p99UpperMs, h.maxMs, h.meanMs].every(finite) || h.p95UpperMs > h.p99UpperMs || h.p99UpperMs > h.maxMs || h.meanMs > h.maxMs) return false;
  if ((lastOccupiedLower > 0 && h.maxMs <= lastOccupiedLower) || (lastOccupiedUpper !== null && h.maxMs > lastOccupiedUpper)) return false;
  // Recompute conservative quantiles from the actual histogram. A hand-written
  // low p99 or null coercion cannot turn a missing/slow sample into a pass.
  const upper = p => { let n = 0; for (const b of h.buckets) { n += b.count; if (n >= Math.ceil(h.count * p)) return Math.min(b.upperMs ?? Infinity, h.maxMs); } };
  return h.p95UpperMs === upper(.95) && h.p99UpperMs === upper(.99);
}

/** Assess ONLY the open-loop work-unit report. This cannot verify HTTP subrequest
 * latency for business transactions, worker drain, money, candidate identity,
 * real-world delivery, or production capacity. These must remain separate gates.
 */
export function assessG09Workload(summary, profile, { minAcceptanceRatio = .99, maxFailureRatio = .001, maxDropRatio = .01, readP95Ms = 500, readP99Ms = 1000 } = {}) {
  const plan = validateG09Profile(profile);
  if (![minAcceptanceRatio, maxFailureRatio, maxDropRatio].every(n => Number.isFinite(n) && n >= 0 && n <= 1) || !finite(readP95Ms) || !finite(readP99Ms) || readP95Ms > readP99Ms) throw new Error('Invalid fixed workload thresholds');
  const checks = [];
  const check = (name, pass, details = {}) => checks.push({ name, pass: pass === true, ...details });
  let profileMatches = false;
  try { profileMatches = JSON.stringify(canonicalG09Profile(summary?.profile)) === JSON.stringify(canonicalG09Profile(profile)); } catch {}
  check('fixed_admission_and_deadline_budgets', profileMatches);
  check('fixed_plan_identity', summary?.origin === plan.origin && summary?.unit === 'work_unit' && summary?.workloadPlanned === plan.planned && summary?.plannedDurationMs === plan.durationMs);
  check('no_controller_stop', summary?.reason === null);
  check('full_observation_window', finite(summary?.startedAt) && finite(summary?.endedAt) && finite(summary?.stoppedAt) && summary.stoppedAt >= summary.startedAt + plan.durationMs && summary.endedAt >= summary.stoppedAt);
  check('schedule_stop_observed_within_budget', finite(summary?.stoppedAt) && summary.stoppedAt <= summary.startedAt + plan.durationMs + profile.maxScheduleLagMs);
  check('absolute_drain_budget', finite(summary?.drainDeadlineAt) && summary.drainDeadlineAt === summary.stoppedAt + profile.drainTimeoutMs && summary.endedAt <= summary.drainDeadlineAt);
  check('elapsed_window_consistency', finite(summary?.elapsedMs) && finite(summary?.total?.measurementDurationMs) && summary.elapsedMs === summary.endedAt - summary.startedAt && summary.total.measurementDurationMs === summary.elapsedMs);
  check('total_conservation', validCounters(summary?.total) && summary.total.planned === plan.planned);
  const combined = Object.fromEntries(counterNames.map(key => [key, 0]));
  const expectedKeys = plan.streams.map(s => `${s.phase}/${s.category}`);
  check('exact_phase_categories', summary?.phaseCategories && Object.keys(summary.phaseCategories).length === expectedKeys.length && expectedKeys.every(key => Object.hasOwn(summary.phaseCategories, key)));
  for (const stream of plan.streams) {
    const key = `${stream.phase}/${stream.category}`, g = own(summary?.phaseCategories, key);
    const prefix = `stream:${key}`;
    const valid = validCounters(g);
    check(`${prefix}:conservation`, valid && g.unit === stream.unit && g.planned === stream.count);
    if (!valid) continue;
    for (const field of counterNames) combined[field] += g[field];
    check(`${prefix}:acceptance`, g.accepted >= Math.ceil(stream.count * minAcceptanceRatio), { accepted: g.accepted, planned: stream.count });
    check(`${prefix}:failure_budget`, g.failed <= stream.count * maxFailureRatio, { failed: g.failed, planned: stream.count });
    check(`${prefix}:drop_budget`, g.dropped <= stream.count * maxDropRatio, { dropped: g.dropped, planned: stream.count });
    check(`${prefix}:no_unfinished_or_cancelled`, g.unfinished === 0 && g.cancelled === 0);
    const durationOK = finite(g.measurementDurationMs) && g.measurementDurationMs >= stream.duration;
    const phase = own(summary?.phases, stream.phase);
    check(`${prefix}:phase_window_consistency`, durationOK && finite(phase?.measurementDurationMs) && g.measurementDurationMs === phase.measurementDurationMs && g.measurementDurationMs <= summary.endedAt - summary.startedAt - stream.offset);
    const throughput = durationOK && g.measurementDurationMs > 0 ? g.succeeded * 1000 / g.measurementDurationMs : null;
    const required = 1000 / stream.interval * minAcceptanceRatio;
    check(`${prefix}:throughput`, throughput !== null && throughput >= required, { observedPerSecond: throughput, requiredPerSecond: required });
    const latencyOK = validLatency(g);
    check(`${prefix}:latency_evidence`, latencyOK && g.latency.count > 0);
    if (stream.unit === 'http_request') check(`${prefix}:http_read_latency`, latencyOK && finite(g.latency.p95UpperMs) && finite(g.latency.p99UpperMs) && g.latency.p95UpperMs <= readP95Ms && g.latency.p99UpperMs <= readP99Ms);
  }
  for (const phaseName of new Set(plan.streams.map(s => s.phase))) {
    const g = own(summary?.phases, phaseName);
    const streams = plan.streams.filter(s => s.phase === phaseName);
    check(`phase:${phaseName}:conservation`, validCounters(g) && counterNames.every(field => g[field] === streams.reduce((n, s) => n + (own(summary?.phaseCategories, `${phaseName}/${s.category}`)?.[field] ?? NaN), 0)));
  }
  check('aggregate_equals_streams', validCounters(summary?.total) && counterNames.every(key => summary.total[key] === combined[key]));
  return { scope: 'open_loop_work_units_only', workloadPassed: checks.every(c => c.pass), capacityVerified: false, checks, pendingGates: ['HTTP subrequest write latency and complete request evidence', 'frozen worker cohort and bounded periodic background', 'per-entity money, inventory and renewal SQL invariants', 'sealed candidate/process/network identity and cleanup'] };
}
