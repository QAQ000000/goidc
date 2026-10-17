// Private controller helper. Launch only inside the already established G09
// namespace/UID boundary; executable hash and service readiness remain caller duties.
import { spawn } from 'node:child_process';
import { readFile, readdir, readlink, realpath, stat } from 'node:fs/promises';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { captureG09OwnedProcess } from './g09-process-sampler.mjs';
const fail = code => new Error(code);
const clean = p => typeof p === 'string' && path.isAbsolute(p) && path.normalize(p) === p && !p.includes('\0');
async function proc(pid) {
  const raw = await readFile(`/proc/${pid}/stat`, 'utf8');
  const f = raw.slice(raw.lastIndexOf(')') + 2).trim().split(/\s+/);
  return { state: f[0], parent: Number(f[1]), group: Number(f[2]), session: Number(f[3]), start: f[19] };
}
async function anchoredState(anchor, tracer = false) {
  if (!anchor) return 'unknown';
  let p;
  try { p = await proc(anchor.pid); } catch {
    return groupExists(anchor.pid) ? 'unknown' : 'gone';
  }
  if (p.start !== anchor.start || p.group !== anchor.pid || p.session !== anchor.pid) return 'unknown';
  if (['Z', 'X'].includes(p.state)) return tracer ? 'gone' : (groupExists(anchor.pid) ? 'unknown' : 'gone');
  try {
    const status = await readFile(`/proc/${anchor.pid}/status`, 'utf8');
    const uids = status.match(/^Uid:\s+(\d+)\s+(\d+)\s+(\d+)\s+(\d+)/m);
    if (!uids || !uids.slice(1).every(uid => Number(uid) === process.getuid())) return 'unknown';
    if (tracer && await readlink(`/proc/${anchor.pid}/exe`) !== '/usr/bin/strace') return 'unknown';
    const last = await proc(anchor.pid);
    if (last.start !== anchor.start || last.group !== anchor.pid || last.session !== anchor.pid || (!tracer && last.parent !== process.pid)) return 'unknown';
    return 'live';
  } catch { return 'unknown'; }
}
async function guardedSignal(anchor, sig, tracer = false) {
  const state = await anchoredState(anchor, tracer);
  if (state === 'unknown') throw fail('runtime_ownership_lost');
  if (state === 'live') signal(anchor.pid, sig);
  return state;
}
function signal(group, sig) { try { process.kill(-group, sig); } catch (e) { if (e.code !== 'ESRCH') throw fail('runtime_signal_failed'); } }
function groupExists(group) { try { process.kill(-group, 0); return true; } catch (e) { if (e.code === 'ESRCH') return false; throw fail('runtime_group_check_failed'); } }
async function until(check, ms) {
  const end = Date.now() + ms;
  do { if (await check()) return true; await delay(20); } while (Date.now() < end);
  return false;
}
export async function launchG09TracedChild({ executable, args = [], cwd, env, tracePrefix, stopSeconds = 5 } = {}) {
  if (!clean(executable) || !clean(cwd) || !clean(tracePrefix) || !Array.isArray(args) || args.length > 256 || args.some(x => typeof x !== 'string' || x.includes('\0') || x.length > 65536) || !env || Object.getPrototypeOf(env) !== Object.prototype || Object.entries(env).some(([k,v]) => !/^[A-Za-z_][A-Za-z0-9_]*$/.test(k) || typeof v !== 'string' || v.includes('\0')) || !Number.isInteger(stopSeconds) || stopSeconds < 1 || stopSeconds > 30) throw fail('runtime_options_invalid');
  try {
    if (await realpath(executable) !== executable || !(await stat(executable)).isFile() || await realpath(cwd) !== cwd || !(await stat(cwd)).isDirectory()) throw fail('invalid');
    const directory = path.dirname(tracePrefix), basename = path.basename(tracePrefix);
    const dir = await stat(directory);
    if (await realpath(directory) !== directory || (await readdir(directory)).some(name => name === basename || name.startsWith(`${basename}.`))) throw fail('invalid');
    if (!dir.isDirectory() || dir.uid !== process.getuid() || (dir.mode & 0o077)) throw fail('invalid');
  } catch { throw fail('runtime_paths_invalid'); }
  // -DDD replaces the direct child with the application and moves the tracer
  // into its own session. Never register the short-lived strace launcher.
  const child = spawn('/usr/bin/strace', ['-DDD', '-I', '1', '-ff', '-yy', '-qq', '-s', '256', '-o', tracePrefix, '-e', 'trace=%file,%network,fchmod,fchown,ftruncate,fallocate', '-e', 'raw=sendto,sendmsg,sendmmsg,recvfrom,recvmsg,recvmmsg', '--', executable, ...args], { cwd, env, detached: true, stdio: 'ignore' });
  let exited = false, tracer = null, app = null, stopping;
  const done = new Promise(resolve => {
    child.once('error', () => { exited = true; resolve({ code: null, signal: null, failed: true }); });
    child.once('exit', (code, signal) => { exited = true; resolve({ code, signal, failed: false }); });
  });
  async function cleanup() {
    let ownershipLost = false, graceful = false;
    try {
      if (child.pid) await guardedSignal(app, 'SIGTERM');
      graceful = await until(async () => !child.pid || await anchoredState(app) === 'gone', stopSeconds * 1000);
      if (!graceful && child.pid) await guardedSignal(app, 'SIGKILL');
    } catch { ownershipLost = true; }
    const appGone = await until(async () => exited && (!child.pid || await anchoredState(app) === 'gone'), ownershipLost ? 20 : 2000);
    // Do not let an application ownership refusal bypass tracer cleanup.
    let tracerGone = false;
    try {
      if (tracer) {
        await guardedSignal(tracer, 'SIGTERM', true);
        if (!await until(async () => await anchoredState(tracer, true) === 'gone', 2000)) await guardedSignal(tracer, 'SIGKILL', true);
        tracerGone = await until(async () => await anchoredState(tracer, true) === 'gone', 2000);
      }
    } catch { ownershipLost = true; }
    if (ownershipLost || !tracer) throw fail('runtime_ownership_lost');
    if (!appGone || !tracerGone) throw fail('runtime_cleanup_failed');
    if (!graceful) throw fail('runtime_term_deadline');
  }
  const stop = () => stopping ??= cleanup();
  try {
    const initial = await proc(child.pid);
    if (initial.parent !== process.pid || initial.group !== child.pid || initial.session !== child.pid) throw fail('runtime_app_identity');
    app = { pid: child.pid, start: initial.start };
    const ready = await until(async () => {
      if (exited) throw fail('runtime_early_exit');
      try {
        const p = await proc(child.pid);
        const status = await readFile(`/proc/${child.pid}/status`, 'utf8');
        const tracerPID = Number(status.match(/^TracerPid:\s+(\d+)/m)?.[1]);
        if (!tracerPID || p.parent !== process.pid || p.group !== child.pid || p.session !== child.pid || p.start !== app.start) return false;
        const t = await proc(tracerPID);
        if (t.group !== tracerPID || t.session !== tracerPID) throw fail('runtime_tracer_identity');
        tracer = { pid: tracerPID, start: t.start };
        if (await anchoredState(tracer, true) !== 'live') throw fail('runtime_tracer_identity');
        return await readlink(`/proc/${child.pid}/exe`) === executable;
      } catch (e) { if (e.message === 'runtime_tracer_identity') throw e; return false; }
    }, 5000);
    if (!ready) throw fail('runtime_ready_deadline');
    const identity = await captureG09OwnedProcess({ name: 'candidate', pid: child.pid, uid: process.getuid(), executable, cwd, netNamespace: await readlink('/proc/self/ns/net'), mountNamespace: await readlink('/proc/self/ns/mnt') });
    return Object.freeze({ pid: child.pid, tracerPID: tracer.pid, tracePrefix, identity, done, stop });
  } catch {
    await stop().catch(() => {});
    throw fail('runtime_launch_failed');
  }
}
