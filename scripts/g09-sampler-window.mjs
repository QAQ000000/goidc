// Bounded process-observation helper, never a capacity or candidate verifier.
import { openSync, writeSync, fsyncSync, closeSync, lstatSync, realpathSync, fstatSync } from 'node:fs';
import path from 'node:path';
import { performance } from 'node:perf_hooks';
import { captureG09OwnedProcess, createG09ProcessSampler } from './g09-process-sampler.mjs';

const fail = code => new Error(code);
const finite = n => typeof n === 'number' && Number.isFinite(n);
const positive = n => finite(n) && n > 0;
const defaultClock = { now: () => performance.now(), sleep: ms => new Promise(resolve => setTimeout(resolve, ms)) };

// Registration, thresholds, journal and complete slot budget are sealed before run.
// Clock injection is for deterministic scheduling regressions only; proc reads
// always use the existing real owned-child sampler.
export async function createG09SamplerWindow({ children, clockTicksPerSecond, profile, journalPath, stop, clock = defaultClock } = {}) {
  if (!profile || !Array.isArray(children) || !children.length || typeof stop !== 'function' || typeof clock.now !== 'function' || typeof clock.sleep !== 'function') throw fail('window_config_invalid');
  const initialNow = clock.now();
  if (!finite(initialNow)) throw fail('window_clock_invalid');
  const { startAtMs, endAtMs, maxSamples, maxScheduleLagMs, thresholds } = profile;
  const keys = ['rssBytes', 'threads', 'fdCount', 'cpuPercentOneCore'];
  if (!finite(startAtMs) || !finite(endAtMs) || startAtMs < initialNow || endAtMs <= startAtMs || endAtMs - startAtMs > 1800000 || !Number.isInteger(maxSamples) || maxSamples < 1 || maxSamples > 1000 || maxSamples !== Math.ceil((endAtMs - startAtMs) / 2000) || !finite(maxScheduleLagMs) || maxScheduleLagMs < 0 || maxScheduleLagMs >= 2000 || !thresholds || !keys.every(k => positive(thresholds[k])) || typeof thresholds.requireIO !== 'boolean') throw fail('window_profile_invalid');
  const frozen = Object.freeze({ startAtMs, endAtMs, maxSamples, maxScheduleLagMs, slotMs: 2000, thresholds: Object.freeze(Object.fromEntries([...keys.map(k => [k, thresholds[k]]), ['requireIO', thresholds.requireIO]])) });
  const targets = [];
  for (const child of children) targets.push(await captureG09OwnedProcess(child));
  const sample = createG09ProcessSampler({ targets, clockTicksPerSecond, maxSamples });
  let lastNow = initialNow;
  const now = () => {
    const value = clock.now();
    if (!finite(value) || value < lastNow) throw fail('window_clock_invalid');
    lastNow = value;
    return value;
  };
  let fd, parentIdentity;
  try {
    const parent = path.dirname(journalPath), st = lstatSync(parent);
    if (!path.isAbsolute(journalPath) || path.normalize(journalPath) !== journalPath || realpathSync(parent) !== parent || !st.isDirectory() || (st.mode & 0o777) !== 0o700 || st.uid !== process.getuid()) throw fail('window_private_directory_invalid');
    parentIdentity = { path: parent, dev: st.dev, ino: st.ino };
    fd = openSync(journalPath, 'wx', 0o600);
    fsyncSync(fd);
    const dirFD = openSync(parent, 'r');
    try { fsyncSync(dirFD); } finally { closeSync(dirFD); }
  } catch { if (fd !== undefined) closeSync(fd); throw fail('window_journal_create_failed'); }
  let state = 'ready', failureCode = null, attempted = 0, persisted = 0, stopped = false;
  const summary = () => ({ state, failureCode, planned: maxSamples, attempted, persisted, missing: maxSamples - persisted, capacityVerified: false, candidateVerified: false, profile: frozen });
  const persist = value => {
    const parent = lstatSync(parentIdentity.path);
    if (!parent.isDirectory() || parent.dev !== parentIdentity.dev || parent.ino !== parentIdentity.ino || (parent.mode & 0o777) !== 0o700 || parent.uid !== process.getuid() || realpathSync(parentIdentity.path) !== parentIdentity.path) throw fail('window_private_directory_invalid');
    const st = fstatSync(fd), named = lstatSync(journalPath);
    if (!named.isFile() || named.dev !== st.dev || named.ino !== st.ino || st.nlink !== 1 || (st.mode & 0o777) !== 0o600 || st.uid !== process.getuid()) throw fail('window_journal_invalid');
    const data = Buffer.from(JSON.stringify(value) + '\n');
    let offset = 0;
    while (offset < data.length) { const n = writeSync(fd, data, offset, data.length - offset); if (n <= 0) throw fail('window_journal_write_failed'); offset += n; }
    fsyncSync(fd);
  };
  const latch = async code => {
    state = 'failed'; failureCode ??= code;
    if (!stopped) { stopped = true; try { await stop(Object.freeze({ code: failureCode, capacityVerified: false })); } catch { /* Original failure remains latched. */ } }
  };
  return Object.freeze({
    profile: frozen,
    async run() {
      if (state !== 'ready') throw fail('window_run_refused');
      state = 'running';
      try {
        persist({ kind: 'profile', profile: frozen, capacityVerified: false });
        for (let slot = 0; slot < maxSamples; slot++) {
          const plannedAtMs = startAtMs + slot * 2000;
          while (now() < plannedAtMs) await clock.sleep(plannedAtMs - now());
          const actualAtMs = now();
          if (!finite(actualAtMs) || actualAtMs >= endAtMs || actualAtMs - plannedAtMs > maxScheduleLagMs) throw fail('window_slot_missed');
          attempted++;
          let result;
          try { result = await sample(); } catch { throw fail('window_sample_failed'); }
          if (now() >= Math.min(endAtMs, plannedAtMs + 2000)) throw fail('window_sample_overran');
          // Existing sampler emits only numeric/enum fields; never serialize targets.
          persist({ kind: 'sample', slot, plannedAtMs, actualAtMs, sample: result });
          persisted++;
          if (now() >= Math.min(endAtMs, plannedAtMs + 2000)) throw fail('window_persistence_overran');
          for (const row of result.processes) {
            if (!keys.slice(0, 3).every(k => finite(row[k]) && row[k] <= frozen.thresholds[k]) || (row.cpuPercentOneCore === null ? slot !== 0 : !finite(row.cpuPercentOneCore) || row.cpuPercentOneCore > frozen.thresholds.cpuPercentOneCore) || (frozen.thresholds.requireIO && !row.ioAvailable)) throw fail('window_resource_limit');
          }
        }
        while (now() < endAtMs) await clock.sleep(endAtMs - now());
        if (attempted !== maxSamples || persisted !== attempted) throw fail('window_sample_conservation');
        state = 'complete';
        persist({ kind: 'summary', ...summary() });
      } catch (error) {
        const code = /^window_[a-z_]+$/.test(error.message) ? error.message : 'window_persistence_or_clock_failed';
        await latch(code);
        try { persist({ kind: 'summary', ...summary() }); } catch { /* Failed journal cannot prove persistence. */ }
      } finally {
        try { closeSync(fd); } catch { await latch('window_journal_close_failed'); }
      }
      return summary();
    },
  });
}
