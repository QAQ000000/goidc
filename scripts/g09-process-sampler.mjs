// Linux /proc sampler for explicitly owned direct children. No discovery,
// signals, environment/command-line reads, query text or arbitrary proc paths.
import { readFile, readdir, readlink } from 'node:fs/promises';
import path from 'node:path';
import { performance } from 'node:perf_hooks';

const integer = (n, min, max) => Number.isSafeInteger(n) && n >= min && n <= max;
const decimal = value => typeof value === 'string' && /^(0|[1-9][0-9]{0,19})$/.test(value);
const cleanPath = value => typeof value === 'string' && path.isAbsolute(value) && path.normalize(value) === value;
const failure = code => new Error(code); // Never expose raw filesystem errors.

export function parseG09ProcStat(raw, pid) {
  const close = raw.lastIndexOf(')');
  if (!raw.startsWith(`${pid} (`) || close < 0) throw failure('proc_stat_invalid');
  const f = raw.slice(close + 1).trim().split(/\s+/);
  if (f.length < 22 || !/^[RSDTtZXIWPK]$/.test(f[0]) || ![f[1], f[11], f[12], f[17], f[19]].every(decimal)) throw failure('proc_stat_invalid');
  const parentPID = Number(f[1]), threads = Number(f[17]);
  if (!integer(parentPID, 1, 4194304) || !integer(threads, 1, 1000000) || ['Z', 'X'].includes(f[0])) throw failure('proc_not_live');
  return { parentPID, threads, startTicks: f[19], userTicks: f[11], systemTicks: f[12] };
}

function statusFields(raw) {
  const uid = raw.match(/^Uid:\s+(\d+)\s+(\d+)\s+(\d+)\s+(\d+)\s*$/m);
  const rss = raw.match(/^VmRSS:\s+(\d+)\s+kB\s*$/m);
  if (!uid || !rss || !uid.slice(1).every(n => n === uid[1]) || !integer(Number(uid[1]), 0, 4294967295) || !decimal(rss[1]) || !Number.isSafeInteger(Number(rss[1]) * 1024)) throw failure('proc_status_invalid');
  return { uid: Number(uid[1]), rssBytes: Number(rss[1]) * 1024 };
}

export function parseG09ProcIO(raw) {
  const out = {};
  for (const key of ['rchar', 'wchar', 'syscr', 'syscw', 'read_bytes', 'write_bytes', 'cancelled_write_bytes']) {
    const match = raw.match(new RegExp(`^${key}:\\s+(\\d+)\\s*$`, 'm'));
    if (!match || !decimal(match[1])) throw failure('proc_io_invalid');
    out[key] = match[1]; // Decimal strings avoid silently losing uint64 bits.
  }
  return out;
}

async function identity(pid) {
  const root = `/proc/${pid}`;
  const [raw, status, executable, cwd, netNamespace, mountNamespace] = await Promise.all([
    readFile(`${root}/stat`, 'utf8'), readFile(`${root}/status`, 'utf8'), readlink(`${root}/exe`),
    readlink(`${root}/cwd`), readlink(`${root}/ns/net`), readlink(`${root}/ns/mnt`),
  ]);
  return { ...parseG09ProcStat(raw, pid), ...statusFields(status), executable, cwd, netNamespace, mountNamespace };
}

// Call only after the controller has launched and independently verified the
// candidate executable hash. This identity is not proof of a sealed candidate.
export async function captureG09OwnedProcess({ name, pid, uid, executable, cwd, netNamespace, mountNamespace } = {}) {
  if (!/^[a-z][a-z0-9_-]{0,31}$/.test(name ?? '') || !integer(pid, 2, 4194304) || pid === process.pid || !integer(uid, 0, 4294967295) || !cleanPath(executable) || !cleanPath(cwd) || !/^net:\[\d+\]$/.test(netNamespace ?? '') || !/^mnt:\[\d+\]$/.test(mountNamespace ?? '')) throw failure('proc_target_invalid');
  try {
    const actual = await identity(pid);
    if (actual.parentPID !== process.pid || actual.uid !== uid || actual.executable !== executable || actual.cwd !== cwd || actual.netNamespace !== netNamespace || actual.mountNamespace !== mountNamespace) throw failure('proc_identity_mismatch');
    return Object.freeze({ name, pid, uid, executable, cwd, netNamespace, mountNamespace, parentPID: process.pid, startTicks: actual.startTicks });
  } catch { throw failure('proc_capture_failed'); }
}

const identityKeys = ['parentPID', 'startTicks', 'uid', 'executable', 'cwd', 'netNamespace', 'mountNamespace'];
function sameIdentity(expected, actual) {
  return identityKeys.every(key => expected[key] === actual[key]);
}

// Caller owns the two-second schedule and durable sample journal. This helper
// never schedules, scans /proc, reads children recursively or signals a process.
// Once any collection fails, further samples are refused for this sampler.
export function createG09ProcessSampler({ targets, clockTicksPerSecond, maxSamples = 1000 } = {}) {
  if (!Array.isArray(targets) || !targets.length || targets.length > 16 || !integer(clockTicksPerSecond, 1, 100000) || !integer(maxSamples, 1, 10000)) throw failure('proc_sampler_invalid');
  const names = new Set(), pids = new Set();
  const owned = targets.map(t => {
    if (!t || t.parentPID !== process.pid || !integer(t.pid, 2, 4194304) || t.pid === process.pid || !/^[a-z][a-z0-9_-]{0,31}$/.test(t.name ?? '') || names.has(t.name) || pids.has(t.pid) || !decimal(t.startTicks) || !integer(t.uid, 0, 4294967295) || !cleanPath(t.executable) || !cleanPath(t.cwd) || !/^net:\[\d+\]$/.test(t.netNamespace ?? '') || !/^mnt:\[\d+\]$/.test(t.mountNamespace ?? '')) throw failure('proc_target_invalid');
    names.add(t.name); pids.add(t.pid);
    return Object.freeze({ ...t });
  });
  let failed = false, running = false, count = 0;
  const previous = new Map();
  return async function sample() {
    if (failed || running || count >= maxSamples) { failed = true; throw failure('proc_sampling_refused'); }
    running = true;
    try {
      const startedAtMs = performance.now();
      const rows = await Promise.all(owned.map(async t => {
        const first = await identity(t.pid);
        if (!sameIdentity(t, first)) throw failure('proc_identity_changed');
        const [rawIO, fds] = await Promise.all([
          readFile(`/proc/${t.pid}/io`, 'utf8').catch(error => { if (error.code === 'ENOENT') return null; throw error; }),
          readdir(`/proc/${t.pid}/fd`),
        ]);
        const last = await identity(t.pid), sampledAtMs = performance.now();
        if (!sameIdentity(t, last) || !fds.every(fd => /^[0-9]+$/.test(fd))) throw failure('proc_identity_changed');
        // Some kernels omit CONFIG_TASK_IO_ACCOUNTING. The second identity
        // check distinguishes that missing proc file from an exited process.
        const io = rawIO === null ? null : parseG09ProcIO(rawIO), ticks = BigInt(last.userTicks) + BigInt(last.systemTicks), prev = previous.get(t.name);
        let cpuPercentOneCore = null, intervalMs = null;
        if (prev) {
          intervalMs = sampledAtMs - prev.at;
          if (ticks < prev.ticks || intervalMs <= 0) throw failure('proc_counters_invalid');
          if ((io === null) !== (prev.io === null)) throw failure('proc_counters_invalid');
          if (io !== null) for (const key of ['rchar', 'wchar', 'syscr', 'syscw', 'read_bytes', 'write_bytes']) if (BigInt(io[key]) < BigInt(prev.io[key])) throw failure('proc_counters_invalid');
          cpuPercentOneCore = Number(ticks - prev.ticks) * 100000 / (clockTicksPerSecond * intervalMs);
          if (!Number.isFinite(cpuPercentOneCore)) throw failure('proc_counters_invalid');
        }
        previous.set(t.name, { ticks, at: sampledAtMs, io });
        return { name: t.name, pid: t.pid, uid: t.uid, startTicks: t.startTicks, sampledAtMs, intervalMs, userTicks: last.userTicks, systemTicks: last.systemTicks, cpuPercentOneCore, rssBytes: last.rssBytes, threads: last.threads, fdCount: fds.length, ioAvailable: io !== null, ioUnavailableReason: io === null ? 'kernel_proc_io_unavailable' : null, io };
      }));
      if (failed) throw failure('proc_sampling_refused'); // Concurrent misuse invalidates the in-flight sample too.
      return { sequence: ++count, startedAtMs, endedAtMs: performance.now(), clockTicksPerSecond, processes: rows, candidateVerified: false, capacityVerified: false };
    } catch { failed = true; throw failure('proc_sample_failed'); }
    finally { running = false; }
  };
}
