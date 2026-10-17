import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, readdir, rm } from 'node:fs/promises';
import os from 'node:os';
import { launchG09TracedChild } from './g09-runtime-process.mjs';
import { createG09ProcessSampler } from './g09-process-sampler.mjs';

test('invalid inputs refuse before spawn without reflecting private values', async () => {
  for (const value of [undefined, {}, { executable: '/secret/../secret', args: ['private-argument'] }, { executable: '/usr/bin/sleep', cwd: '/tmp', tracePrefix: '/tmp/trace', env: {}, stopSeconds: 31 }]) {
    await assert.rejects(launchG09TracedChild(value), { message: 'runtime_options_invalid' });
  }
});
test('private trace directory required', async () => {
  await assert.rejects(launchG09TracedChild({ executable: '/usr/bin/sleep', args: ['30'], cwd: '/tmp', env: {}, tracePrefix: '/tmp/trace' }), { message: 'runtime_paths_invalid' });
});
test('real daemon tracer preserves direct child, sampler, exec audit and TERM cleanup', { skip: process.env.G09_RUNTIME_PROCESS_TEST !== '1' }, async () => {
  const dir = await mkdtemp(`${os.tmpdir()}/g09-process-`);
  let handle;
  try {
    handle = await launchG09TracedChild({ executable: '/usr/bin/sleep', args: ['30'], cwd: '/tmp', env: { PATH: '/usr/bin' }, tracePrefix: `${dir}/candidate` });
    assert.equal(handle.identity.parentPID, process.pid);
    assert.notEqual(handle.pid, handle.tracerPID);
    const sample = await createG09ProcessSampler({ targets: [handle.identity], clockTicksPerSecond: 100 })();
    assert.equal(sample.processes[0].pid, handle.pid);
    await handle.stop();
    await handle.stop();
    assert.deepEqual(await handle.done, { code: null, signal: 'SIGTERM', failed: false });
    const trace = await readFile(`${dir}/candidate.${handle.pid}`, 'utf8');
    assert.match(trace, /execve\("\/usr\/bin\/sleep".*= 0/);
    assert.match(trace, /SIGTERM/);
    const tracer = await readFile(`/proc/${handle.tracerPID}/stat`, 'utf8').catch(() => null);
    assert.ok(tracer === null || /\) [ZX] /.test(tracer));
    assert.ok((await readdir(dir)).every(x => /^candidate\.\d+$/.test(x)));
  } finally { await handle?.stop().catch(() => {}); await rm(dir, { recursive: true, force: true }); }
});
test('failed exec is bounded and sanitized', { skip: process.env.G09_RUNTIME_PROCESS_TEST !== '1' }, async () => {
  const dir = await mkdtemp(`${os.tmpdir()}/g09-failure-`);
  try {
    await assert.rejects(launchG09TracedChild({ executable: '/etc/hostname', cwd: '/tmp', env: {}, tracePrefix: `${dir}/candidate`, stopSeconds: 1 }), { message: 'runtime_launch_failed' });
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test('existing trace output is refused without truncation', async () => {
  const { writeFile } = await import('node:fs/promises');
  const dir = await mkdtemp(`${os.tmpdir()}/g09-collision-`);
  try {
    await writeFile(`${dir}/candidate.123`, 'preserve');
    await assert.rejects(launchG09TracedChild({ executable: '/usr/bin/sleep', cwd: '/tmp', env: {}, tracePrefix: `${dir}/candidate` }), { message: 'runtime_paths_invalid' });
    assert.equal(await readFile(`${dir}/candidate.123`, 'utf8'), 'preserve');
  } finally { await rm(dir, { recursive: true, force: true }); }
});
test('TERM refusal forces owned cleanup and reports deadline failure', { skip: process.env.G09_RUNTIME_PROCESS_TEST !== '1' }, async () => {
  const dir = await mkdtemp(`${os.tmpdir()}/g09-stubborn-`);
  let handle;
  try {
    handle = await launchG09TracedChild({ executable: process.execPath, args: ['-e', 'process.on("SIGTERM",()=>{}); require("fs").writeFileSync(process.argv[1],"ready"); setInterval(()=>{},1000)', `${dir}/ready`], cwd: '/tmp', env: {}, tracePrefix: `${dir}/candidate`, stopSeconds: 1 });
    let ready = false;
    for (let i = 0; i < 100; i++) {
      if (await readFile(`${dir}/ready`, 'utf8').catch(() => '') === 'ready') { ready = true; break; }
      await new Promise(resolve => setTimeout(resolve, 20));
    }
    assert.ok(ready);
    await assert.rejects(handle.stop(), { message: 'runtime_term_deadline' });
    assert.equal((await handle.done).signal, 'SIGKILL');
  } finally { await handle?.stop().catch(() => {}); await rm(dir, { recursive: true, force: true }); }
});

test('natural app exit still permits complete tracer cleanup', { skip: process.env.G09_RUNTIME_PROCESS_TEST !== '1' }, async () => {
  const dir = await mkdtemp(`${os.tmpdir()}/g09-natural-`);
  let handle;
  try {
    handle = await launchG09TracedChild({ executable: '/usr/bin/sleep', args: ['0.2'], cwd: '/tmp', env: {}, tracePrefix: `${dir}/candidate` });
    assert.deepEqual(await handle.done, { code: 0, signal: null, failed: false });
    await handle.stop();
    const tracer = await readFile(`/proc/${handle.tracerPID}/stat`, 'utf8').catch(() => null);
    assert.ok(tracer === null || /\) [ZX] /.test(tracer));
  } finally { await handle?.stop().catch(() => {}); await rm(dir, { recursive: true, force: true }); }
});

test('leader loss with surviving descendants refuses stale group signals', { skip: process.env.G09_RUNTIME_CONTAINMENT_TEST !== '1' }, async () => {
  // Run only inside disposable PID namespace: the outer namespace owns final
  // containment. No signal is sent to an unanchored surviving group.
  const dir = await mkdtemp(`${os.tmpdir()}/g09-leader-loss-`);
  let handle;
  try {
    handle = await launchG09TracedChild({ executable: process.execPath, args: ['-e', 'const c=require("child_process").spawn("/usr/bin/sleep",["1.2"],{stdio:"ignore"});require("fs").writeFileSync(process.argv[1],String(c.pid));setTimeout(()=>process.exit(0),200)', `${dir}/descendant`], cwd: '/tmp', env: {}, tracePrefix: `${dir}/candidate`, stopSeconds: 1 });
    await handle.done;
    const descendant = Number(await readFile(`${dir}/descendant`, 'utf8'));
    await assert.rejects(handle.stop(), { message: 'runtime_ownership_lost' });
    const raw = await readFile(`/proc/${descendant}/stat`, 'utf8');
    assert.ok(!/\) [ZX] /.test(raw), 'unanchored descendant was not signaled');
    const tracer = await readFile(`/proc/${handle.tracerPID}/stat`, 'utf8').catch(() => null);
    assert.ok(tracer === null || /\) [ZX] /.test(tracer), 'owned tracer was still cleaned');
    await new Promise(resolve => setTimeout(resolve, 1300));
  } finally { await handle?.stop().catch(() => {}); await rm(dir, { recursive: true, force: true }); }
});
