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
async function live(pid, start) {
  try { const p = await proc(pid); return p.start === start && !['Z', 'X'].includes(p.state); } catch { return false; }
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
  let exited = false, tracer = null, stopping;
  const done = new Promise(resolve => {
    child.once('error', () => { exited = true; resolve({ code: null, signal: null, failed: true }); });
    child.once('exit', (code, signal) => { exited = true; resolve({ code, signal, failed: false }); });
  });
  async function cleanup() {
    if (child.pid) signal(child.pid, 'SIGTERM');
    let graceful = await until(() => !child.pid || !groupExists(child.pid), stopSeconds * 1000);
    if (!graceful && child.pid) signal(child.pid, 'SIGKILL');
    const appGone = await until(() => exited && (!child.pid || !groupExists(child.pid)), 2000);
    if (tracer && await live(tracer.pid, tracer.start)) {
      signal(tracer.pid, 'SIGTERM');
      if (!await until(async () => !await live(tracer.pid, tracer.start), 2000)) signal(tracer.pid, 'SIGKILL');
    }
    const tracerGone = !tracer || await until(async () => !await live(tracer.pid, tracer.start), 2000);
    if (!appGone || !tracerGone) throw fail('runtime_cleanup_failed');
    if (!graceful) throw fail('runtime_term_deadline');
  }
  const stop = () => stopping ??= cleanup();
  try {
    const ready = await until(async () => {
      if (exited) throw fail('runtime_early_exit');
      try {
        const p = await proc(child.pid);
        const status = await readFile(`/proc/${child.pid}/status`, 'utf8');
        const tracerPID = Number(status.match(/^TracerPid:\s+(\d+)/m)?.[1]);
        if (!tracerPID || p.parent !== process.pid || p.group !== child.pid) return false;
        const t = await proc(tracerPID);
        if (t.group !== tracerPID || t.session !== tracerPID) throw fail('runtime_tracer_identity');
        tracer = { pid: tracerPID, start: t.start };
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
