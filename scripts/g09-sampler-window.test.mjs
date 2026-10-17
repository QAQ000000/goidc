import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn, execFileSync } from 'node:child_process';
import { once } from 'node:events';
import { mkdtemp, chmod, readFile, stat, rm, readlink, realpath } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { performance } from 'node:perf_hooks';
import { createG09SamplerWindow } from './g09-sampler-window.mjs';
const hz = Number(execFileSync('getconf', ['CLK_TCK'], { encoding: 'utf8' }).trim());
async function fixture(t, { realClock = false, count = 3, lag = 0, thresholds = {} } = {}) {
  const dir = await mkdtemp(`${tmpdir()}/g09-window-`);
  await chmod(dir, 0o700);
  const child = spawn(process.execPath, ['-e', "process.on('message',()=>{process.chdir('/');process.send('changed')});process.stdout.write('ready');setInterval(()=>{},1000)"], { cwd: dir, env: {}, stdio: ['ignore', 'pipe', 'ignore', 'ipc'] });
  const gone = once(child, 'exit');
  t.after(async () => { if (child.exitCode === null && child.signalCode === null) child.kill(); await gone; await rm(dir, { recursive: true, force: true }); });
  await once(child.stdout, 'data');
  const owned = { name: 'worker_0', pid: child.pid, uid: process.getuid(), executable: await realpath(process.execPath), cwd: dir, netNamespace: await readlink('/proc/self/ns/net'), mountNamespace: await readlink('/proc/self/ns/mnt') };
  let now = 10;
  const clock = realClock ? undefined : { now: () => now, sleep: async ms => { now += ms + lag; } };
  const start = realClock ? performance.now() + 150 : now + 100;
  const profile = { startAtMs: start, endAtMs: start + (count - 1) * 2000 + 100, maxSamples: count, maxScheduleLagMs: realClock ? 300 : 10, thresholds: { rssBytes: 1e10, threads: 1000, fdCount: 1000, cpuPercentOneCore: 100000, requireIO: false, ...thresholds } };
  const stopped = [];
  const options = { children: [owned], clockTicksPerSecond: hz, profile, journalPath: `${dir}/samples.jsonl`, stop: value => stopped.push(value), ...(clock ? { clock } : {}) };
  const window = await createG09SamplerWindow(options);
  return { window, options, profile, stopped, dir, child, gone };
}

test('real fixed two-second slots persist real owned-child samples with conservation and private journal', async t => {
  const f = await fixture(t, { realClock: true, count: 2 });
  const result = await f.window.run();
  assert.equal(result.state, 'complete');
  assert.equal(result.planned, 2); assert.equal(result.persisted, 2); assert.equal(result.missing, 0);
  assert.equal(result.capacityVerified, false); assert.equal(f.stopped.length, 0);
  const raw = await readFile(f.options.journalPath, 'utf8'), records = raw.trim().split('\n').map(JSON.parse);
  const rows = records.filter(r => r.kind === 'sample');
  assert.equal(rows.length, 2); assert.ok(Math.abs(rows[1].plannedAtMs - rows[0].plannedAtMs - 2000) < 1e-6);
  assert.equal(rows[0].sample.processes[0].cpuPercentOneCore, null);
  assert.ok(rows[1].sample.processes[0].intervalMs >= 1700);
  const first = rows[0].sample.processes[0];
  if (!first.ioAvailable) assert.equal(first.io, null);
  assert.equal((await stat(f.options.journalPath)).mode & 0o777, 0o600);
  assert.ok(!raw.includes(f.dir)); assert.ok(!raw.includes(f.options.children[0].executable));
  await assert.rejects(f.window.run(), /window_run_refused/);
});

test('frozen profile cannot be relaxed by later caller mutation', async t => {
  const f = await fixture(t, { thresholds: { rssBytes: 1 } });
  f.profile.thresholds.rssBytes = 1e12; f.profile.maxSamples = 100;
  const result = await f.window.run();
  assert.equal(result.failureCode, 'window_resource_limit');
  assert.equal(result.persisted, 1); assert.equal(result.missing, 2); assert.equal(f.stopped.length, 1);
});

test('late scheduler records failure once without catch-up sampling', async t => {
  const f = await fixture(t, { lag: 4001 });
  const result = await f.window.run();
  assert.equal(result.failureCode, 'window_slot_missed'); assert.equal(result.attempted, 0);
  assert.equal(result.missing, 3); assert.equal(f.stopped.length, 1);
  await assert.rejects(f.window.run()); assert.equal(f.stopped.length, 1);
});

test('exited owned process fails and invokes stop without signalling any process itself', async t => {
  const f = await fixture(t);
  f.child.kill(); await f.gone;
  const result = await f.window.run();
  assert.equal(result.failureCode, 'window_sample_failed'); assert.equal(result.persisted, 0); assert.equal(f.stopped.length, 1);
});

test('journal permission change latches persistence failure before collecting', async t => {
  const f = await fixture(t);
  await chmod(f.options.journalPath, 0o644);
  const result = await f.window.run();
  assert.equal(result.state, 'failed'); assert.equal(result.persisted, 0); assert.equal(f.stopped.length, 1);
});

test('private directory, existing journal, invalid budget and non-child registration are refused', async t => {
  const f = await fixture(t);
  await assert.rejects(createG09SamplerWindow(f.options), /window_journal_create_failed/);
  await assert.rejects(createG09SamplerWindow({ ...f.options, profile: { ...f.profile, maxSamples: 4 } }), /window_profile_invalid/);
  await assert.rejects(createG09SamplerWindow({ ...f.options, children: [{ ...f.options.children[0], pid: process.pid }] }), /proc_/);
  await chmod(f.dir, 0o755);
  await assert.rejects(createG09SamplerWindow({ ...f.options, journalPath: `${f.dir}/other.jsonl` }), /window_journal_create_failed/);
  await chmod(f.dir, 0o700);
  await f.window.run();
});

 test('non-finite or backwards scheduling clock fails closed with one stop', async t => {
  for (const bad of [NaN, -1]) {
    const f = await fixture(t);
    f.options.clock.sleep = async () => { f.options.clock.now = () => bad; };
    const result = await f.window.run();
    assert.equal(result.failureCode, 'window_clock_invalid'); assert.equal(f.stopped.length, 1);
  }
});

test('unavailable IO remains null and an explicitly required IO budget fails closed', async t => {
  const f = await fixture(t, { count: 1, thresholds: { requireIO: true } });
  const result = await f.window.run();
  const rows = (await readFile(f.options.journalPath, 'utf8')).trim().split('\n').map(JSON.parse);
  const sample = rows.find(r => r.kind === 'sample').sample.processes[0];
  assert.equal(sample.cpuPercentOneCore, null);
  assert.equal(result.state, sample.ioAvailable ? 'complete' : 'failed');
  if (!sample.ioAvailable) { assert.equal(sample.io, null); assert.equal(result.failureCode, 'window_resource_limit'); }
});

test('actual owned-child cwd change invalidates the registered identity and stops once', async t => {
  const f = await fixture(t);
  const changed = once(f.child, 'message'); f.child.send('change-cwd'); await changed;
  const result = await f.window.run();
  assert.equal(result.failureCode, 'window_sample_failed'); assert.equal(result.persisted, 0); assert.equal(f.stopped.length, 1);
});
