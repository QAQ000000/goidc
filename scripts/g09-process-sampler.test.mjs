import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn, execFileSync } from 'node:child_process';
import { once } from 'node:events';
import { readlink, realpath } from 'node:fs/promises';
import { captureG09OwnedProcess, createG09ProcessSampler, parseG09ProcStat, parseG09ProcIO } from './g09-process-sampler.mjs';

const hz = Number(execFileSync('getconf', ['CLK_TCK'], { encoding: 'utf8' }).trim());
async function owned(t) {
  const child = spawn(process.execPath, ['--input-type=module', '-e', `import fs from 'node:fs';
    const fds=Array.from({length:8},()=>fs.openSync('/dev/null','r'));
    process.stdout.write('ready\\n');
    setInterval(()=>{let stop=performance.now()+12;while(performance.now()<stop){}},20);`], { cwd: '/tmp', env: {}, stdio: ['ignore', 'pipe', 'ignore'] });
  const gone = once(child, 'exit');
  t.after(async () => { if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM'); await gone; });
  await once(child.stdout, 'data');
  const expected = { name: 'worker_0', pid: child.pid, uid: process.getuid(), executable: await realpath(process.execPath), cwd: '/tmp', netNamespace: await readlink('/proc/self/ns/net'), mountNamespace: await readlink('/proc/self/ns/mnt') };
  return { child, gone, expected, target: await captureG09OwnedProcess(expected) };
}

test('stat parser handles spaces and parentheses in comm without shifting identity or CPU fields', () => {
  const f = Array(50).fill('0');
  Object.assign(f, { 0: 'S', 1: '123', 11: '29', 12: '7', 17: '4', 19: '98765' });
  assert.deepEqual(parseG09ProcStat(`456 (private ) 123 (name)) ${f.join(' ')}\n`, 456), { parentPID: 123, threads: 4, startTicks: '98765', userTicks: '29', systemTicks: '7' });
  for (const raw of ['invalid', `789 (wrong) ${f.join(' ')}`, `456 (zombie) Z ${f.slice(1).join(' ')}`]) assert.throws(() => parseG09ProcStat(raw, 456));
});

test('I/O parser preserves uint64 decimal strings and refuses missing or invalid counters', () => {
  const keys = ['rchar', 'wchar', 'syscr', 'syscw', 'read_bytes', 'write_bytes', 'cancelled_write_bytes'];
  const raw = keys.map(key => `${key}: 18446744073709551615`).join('\n');
  assert.deepEqual(parseG09ProcIO(raw), Object.fromEntries(keys.map(key => [key, '18446744073709551615'])));
  for (const bad of ['', raw.replace('rchar:', 'private-secret:'), raw.replace('rchar: 18446744073709551615', 'rchar: -1')]) assert.throws(() => parseG09ProcIO(bad), /proc_io_invalid/);
});

test('actual owned child samples CPU, RSS, descriptor count and I/O without publishing secrets', async t => {
  const { target } = await owned(t);
  const sample = createG09ProcessSampler({ targets: [target], clockTicksPerSecond: hz, maxSamples: 2 });
  const first = await sample();
  assert.equal(first.processes[0].cpuPercentOneCore, null);
  await new Promise(resolve => setTimeout(resolve, 120));
  const second = await sample(), row = second.processes[0];
  assert.equal(second.sequence, 2);
  assert.equal(second.capacityVerified, false);
  assert.equal(second.candidateVerified, false);
  assert.ok(row.rssBytes > 0 && row.threads > 0 && row.fdCount >= 8);
  assert.ok(row.cpuPercentOneCore > 0 && row.intervalMs >= 100);
  if (row.ioAvailable) {
    for (const value of Object.values(row.io)) assert.match(value, /^\d+$/);
    assert.equal(row.ioUnavailableReason, null);
  } else {
    assert.equal(row.io, null);
    assert.equal(row.ioUnavailableReason, 'kernel_proc_io_unavailable');
  }
  assert.ok(!JSON.stringify(second).includes(target.executable));
  assert.ok(!JSON.stringify(second).includes('private'));
  await assert.rejects(sample(), /proc_sampling_refused/);
});

test('capture refuses current, non-child and mismatching UID/path/namespace identities', async t => {
  const { expected } = await owned(t);
  for (const patch of [{ pid: process.pid }, { pid: process.ppid }, { uid: expected.uid + 1 }, { executable: '/private-secret' }, { cwd: '/' }, { netNamespace: 'net:[0]' }, { mountNamespace: 'mnt:[0]' }]) {
    await assert.rejects(captureG09OwnedProcess({ ...expected, ...patch }), err => /^proc_/.test(err.message) && !err.message.includes('secret'));
  }
});

test('PID reuse, exec/path/UID/namespace change and exited children fail closed and latch', async t => {
  const { child, gone, target } = await owned(t);
  for (const patch of [{ startTicks: String(BigInt(target.startTicks) + 1n) }, { uid: target.uid + 1 }, { executable: '/wrong' }, { cwd: '/' }, { netNamespace: 'net:[0]' }, { mountNamespace: 'mnt:[0]' }]) {
    const sample = createG09ProcessSampler({ targets: [{ ...target, ...patch }], clockTicksPerSecond: hz });
    await assert.rejects(sample(), /proc_sample_failed/);
    await assert.rejects(sample(), /proc_sampling_refused/);
  }
  child.kill('SIGTERM'); await gone;
  const sample = createG09ProcessSampler({ targets: [target], clockTicksPerSecond: hz });
  await assert.rejects(sample(), /proc_sample_failed/);
});

test('duplicate targets, invalid budgets and overlapping samples are refused', async t => {
  const { target } = await owned(t);
  const options = { targets: [target], clockTicksPerSecond: hz };
  for (const patch of [{ targets: [] }, { targets: [target, target] }, { clockTicksPerSecond: 0 }, { maxSamples: Infinity }, { targets: [{ ...target, parentPID: 1 }] }]) assert.throws(() => createG09ProcessSampler({ ...options, ...patch }));
  const sample = createG09ProcessSampler(options), pending = sample();
  await assert.rejects(sample(), /proc_sampling_refused/);
  await assert.rejects(pending, /proc_sample_failed/);
  await assert.rejects(sample(), /proc_sampling_refused/);
});
