import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, writeFile, rm, rename, symlink } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { verifyG09ResidentTraces } from './g09-runtime-trace.mjs';
const hash = 'a'.repeat(64);
const failure = { message: 'g09_resident_trace_invalid' };
async function fixture(fn) {
  const traceRoot = await mkdtemp(path.join(os.tmpdir(), 'g09-trace-'));
  const manifest = { binaries: { api: { path: 'artifact/bin/api' }, worker: { path: 'artifact/bin/worker' } }, runtime: { node: { path: 'artifact/runtime/node' } }, web: { appRoot: 'artifact/web', entry: 'artifact/web/server.js' }, artifacts: ['artifact/bin/api', 'artifact/bin/worker', 'artifact/runtime/node'].map(path => ({ path, sha256: hash })) };
  const description = { steps: [{ name: 'api', executable: 'artifact/bin/api', cwd: 'artifact' }, { name: 'worker', executable: 'artifact/bin/worker', cwd: 'artifact' }, { name: 'web', executable: 'artifact/runtime/node', cwd: 'artifact/web', args: ['server.js'] }] };
  const identities = ['api', 'web', 'worker_0', 'worker_1'].map((name, i) => {
    const step = description.steps.find(x => x.name === (name.startsWith('worker_') ? 'worker' : name));
    return { name, pid: 100 + i, executable: `/release/${step.executable}`, cwd: `/release/${step.cwd}`, executableSHA256: hash };
  });
  const trace = (id, args = id.name === 'web' ? ['server.js'] : []) => `execve(${JSON.stringify(id.executable)}, ${JSON.stringify([id.executable, ...args]).replaceAll(',', ', ')}, 0x7fffabc /* 14 vars */) = 0\nopenat(AT_FDCWD, "/release/manifest.json", O_RDONLY) = 3\n`;
  const filename = id => path.join(traceRoot, `${id.name}.trace.${id.pid}`);
  for (const id of identities) await writeFile(filename(id), trace(id));
  try { await fn({ options: { traceRoot, manifest, description, identities }, trace, filename }); }
  finally { await rm(traceRoot, { recursive: true, force: true }); }
}
test('all four same-PID execs bind manifest executable, argv and identity hash', async () => fixture(async ({ options }) => {
  const result = await verifyG09ResidentTraces(options);
  assert.equal(result.coverage, 'same-pid-successful-execve');
  assert.deepEqual(result.processes.map(x => x.name), ['api', 'web', 'worker_0', 'worker_1']);
  assert.equal(result.processes[1].argumentCount, 1);
  assert.ok(result.processes.every(x => /^[a-f0-9]{64}$/.test(x.traceSHA256)));
  assert.ok(!JSON.stringify(result).includes('server.js'));
}));
test('missing or wrong PID trace cannot be rescued by successful neighboring trace', async () => fixture(async ({ options, filename }) => {
  const id = options.identities[0];
  await rename(filename(id), path.join(options.traceRoot, `${id.name}.trace.${id.pid + 500}`));
  await assert.rejects(verifyG09ResidentTraces(options), failure);
}));
test('identity list rejects duplicate/missing/extra names and repeated PIDs', async () => {
  for (const change of [ids => ids.pop(), ids => ids.push({ ...ids[0], name: 'other' }), ids => ids[1].name = 'api', ids => ids[1].pid = ids[0].pid]) {
    await fixture(async ({ options }) => { change(options.identities); await assert.rejects(verifyG09ResidentTraces(options), failure); });
  }
});
test('failed, truncated, abbreviated, repeated and substituted exec records refuse', async () => {
  const edits = [
    raw => raw.replace(') = 0', ') = -1 ENOENT (No such file or directory)'),
    raw => raw.slice(0, -1),
    raw => raw.replace('"server.js"', '"server.j"...'),
    raw => raw.replace('"server.js"', '...'),
    raw => raw.replace('"server.js"', '"private-wrong-arg"'),
    raw => raw.replace('execve("/release/artifact/runtime/node"', 'execve("/tmp/node"'),
    raw => raw + raw.split('\n')[0] + '\n',
    raw => raw.replace(') = 0', ' <unfinished ...>'),
    raw => raw.replace('execve(', 'execveat('),
  ];
  for (const edit of edits) await fixture(async ({ options, filename, trace }) => {
    const id = options.identities[1]; await writeFile(filename(id), edit(trace(id)));
    await assert.rejects(verifyG09ResidentTraces(options), failure);
  });
});
test('C octal escapes decode exactly, unknown or malformed escapes refuse', async () => fixture(async ({ options, filename, trace }) => {
  const id = options.identities[1];
  await writeFile(filename(id), trace(id).replace('"server.js"', '"\\163erver.js"'));
  await verifyG09ResidentTraces(options);
  await writeFile(filename(id), trace(id).replace('"server.js"', '"\\qerver.js"'));
  await assert.rejects(verifyG09ResidentTraces(options), failure);
}));
test('manifest identity hash, paths and launch arguments cannot diverge', async () => {
  for (const change of [
    o => o.identities[0].executableSHA256 = 'b'.repeat(64),
    o => o.identities[0].executable = '/tmp/api',
    o => o.identities[0].cwd = '/tmp',
    o => o.description.steps[2].args = ['private-wrong-arg'],
    o => o.manifest.binaries.api.path = '../api',
    o => o.manifest.artifacts.push({ ...o.manifest.artifacts[0] }),
  ]) await fixture(async ({ options }) => { change(options); await assert.rejects(verifyG09ResidentTraces(options), failure); });
});
test('symlink traces are refused and errors never reflect filesystem details', async () => fixture(async ({ options, filename }) => {
  const first = filename(options.identities[0]), target = `${first}.private`;
  await rename(first, target); await symlink(target, first);
  await assert.rejects(verifyG09ResidentTraces(options), failure);
}));

test('single environment variable uses real strace singular var spelling', async () => fixture(async ({ options, filename, trace }) => {
  const id = options.identities[0];
  await writeFile(filename(id), trace(id).replace('14 vars', '1 var'));
  await verifyG09ResidentTraces(options);
}));
