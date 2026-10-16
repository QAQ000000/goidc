import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm, writeFile, symlink } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { auditDualTraceFiles, dualControllerFiles, dualNamespaceArgs, parseDualWebOptions, validateDualRunRoot } from './accept-release-dual-web.mjs';

const named = { old: '/tmp/input-old', next: '/tmp/input-next', 'old-source': '/tmp/source', 'next-source': '/tmp/source', 'old-manifest-sha256': 'a'.repeat(64), 'next-manifest-sha256': 'b'.repeat(64), 'runner-commit': 'c'.repeat(40), 'run-root': '/tmp/zjmfgo-dual-web-case', 'pg-socket': '/tmp/pg/.s.PGSQL.55439', budget: '/tmp/budget.json', 'budget-sha256': 'd'.repeat(64) };
const argv = () => Object.entries(named).flatMap(([key, value]) => [`--${key}`, value]);
const manifest = () => ({ web: { appRoot: 'artifact/web/web' }, binaries: { api: { path: 'artifact/bin/api' }, migrate: { path: 'artifact/bin/migrate' } }, runtime: { node: { path: 'artifact/runtime/node' } } });
const manifests = { old: manifest(), next: manifest() };

test('dual controller CLI is explicit, anchored, absolute and preflight by default', () => {
  const args = argv(), before = [...args], options = parseDualWebOptions(args);
  assert.equal(options.execute, undefined);
  assert.deepEqual(args, before);
  assert.equal(options.oldSHA, named['old-manifest-sha256']);
  assert.equal(parseDualWebOptions([...args, '--execute']).execute, true);
  for (const bad of [args.slice(2), [...args, '--old', '/other'], [...args, '--execute', '--execute'], [...args, '--execute', '--foreign', 'value'], [...args, '--pg-socket'], [...args, '--unknown', 'value'], [...args, 'constructor', 'value']]) assert.throws(() => parseDualWebOptions(bad));
  for (const [key, value] of [['old-manifest-sha256', 'a'], ['runner-commit', 'c'], ['old', 'relative'], ['budget-sha256', 'bad']]) {
    const altered = { ...named, [key]: value };
    assert.throws(() => parseDualWebOptions(Object.entries(altered).flatMap(([name, data]) => [`--${name}`, data])));
  }
});

test('exclusive run roots cannot overlap immutable input, source or private PG paths', () => {
  assert.equal(validateDualRunRoot('/tmp/zjmfgo-dual-web-case', ['/tmp/input-old', '/tmp/source']), '/tmp/zjmfgo-dual-web-case');
  assert.equal(validateDualRunRoot('/dev/shm/zjmfgo-dual-web-case', ['/tmp/input']), '/dev/shm/zjmfgo-dual-web-case');
  for (const output of ['/tmp', '/', '/www/wwwroot/dual', '/tmp/zjmfgo-dual-web-case/child', '/tmp/zjmfgo-dual-web-case;command']) assert.throws(() => validateDualRunRoot(output, ['/tmp/source']));
  for (const input of ['/tmp', '/tmp/zjmfgo-dual-web-case', '/tmp/zjmfgo-dual-web-case/source']) assert.throws(() => validateDualRunRoot('/tmp/zjmfgo-dual-web-case', [input]));
});

test('namespace mounts two independent readonly releases, separate caches and only the explicit PG socket', () => {
  const args = dualNamespaceArgs({ releases: { old: '/tmp/run/old', next: '/tmp/run/next' }, control: '/tmp/run/control', fixture: '/tmp/run/fixture', mutable: '/tmp/run/mutable', pgSocket: '/tmp/run/socket/.s.PGSQL.55439', uid: 1000, gid: 1000, manifests });
  for (const flag of ['--unshare-all', '--clearenv', '--die-with-parent']) assert(args.includes(flag));
  const mounts = [];
  for (let index = 0; index < args.length; index++) if (['--ro-bind', '--bind'].includes(args[index])) mounts.push(args.slice(index, index + 3));
  assert(mounts.some(row => JSON.stringify(row) === JSON.stringify(['--ro-bind', '/tmp/run/old', '/releases/old'])));
  assert(mounts.some(row => JSON.stringify(row) === JSON.stringify(['--ro-bind', '/tmp/run/next', '/releases/new'])));
  assert(mounts.some(row => JSON.stringify(row) === JSON.stringify(['--bind', '/tmp/run/mutable/old/cache', '/releases/old/artifact/web/web/.next/cache'])));
  assert(mounts.some(row => JSON.stringify(row) === JSON.stringify(['--bind', '/tmp/run/mutable/next/cache', '/releases/new/artifact/web/web/.next/cache'])));
  assert(mounts.some(row => JSON.stringify(row) === JSON.stringify(['--ro-bind', '/tmp/run/socket/.s.PGSQL.55439', '/pg/.s.PGSQL.55439'])));
  assert(!mounts.some(row => ['/root', '/www/wwwroot/zjmfgo', '/usr/bin', '/usr/local/btgo', '/'].includes(row[1])));
  assert.deepEqual(args.slice(-3), ['/releases/new/artifact/runtime/node', '/control/release-dual-web-scenario.mjs', '--inner']);
  assert.equal(args[args.indexOf('--cap-drop') + 1], 'ALL');
  assert.throws(() => dualNamespaceArgs({ uid: 0, gid: 0 }), /Ordinary/);
  assert(dualControllerFiles.includes('scripts/release-dual-web-scenario.mjs'));
  assert(dualControllerFiles.includes('scripts/release-runtime-trace-selection.mjs'));
});

async function traces(t) {
  const root = await mkdtemp(path.join(os.tmpdir(), 'dual-outer-trace-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const rows = {};
  for (const label of ['old', 'next']) {
    const release = label === 'old' ? 'old' : 'new';
    for (const number of [1, 2]) rows[`${label}-migrate-${number}.trace.${100 + Object.keys(rows).length}`] = `execve("/releases/${release}/artifact/bin/migrate", ["migrate"], 0x0 /* 1 var */) = 0\n`;
    rows[`${label}-api.trace.${100 + Object.keys(rows).length}`] = `execve("/releases/${release}/artifact/bin/api", ["api"], 0x0 /* 1 var */) = 0\n`;
    rows[`${label}-web.trace.${100 + Object.keys(rows).length}`] = `execve("/releases/${release}/artifact/runtime/node", ["node", "server.js"], 0x0 /* 1 var */) = 0\n`;
  }
  async function save() { for (const [name, raw] of Object.entries(rows)) await writeFile(path.join(root, name), raw); }
  await save(); return { root, rows, save };
}

test('dual syscall audit requires four actual migrations and both API/Web executions', async t => {
  const f = await traces(t), result = await auditDualTraceFiles(f.root, manifests);
  assert.equal(result.files.length, 8);
  assert.deepEqual(result.migrationExecutions, ['next-migrate-1', 'next-migrate-2', 'old-migrate-1', 'old-migrate-2']);
  const key = Object.keys(f.rows).find(name => name.startsWith('old-migrate-1'));
  f.rows[key] = f.rows[key].replace('/releases/old/', '/releases/new/'); await f.save();
  await assert.rejects(auditDualTraceFiles(f.root, manifests), /Four actual/);
});

test('dual syscall audit accepts only declared caches and loopback traffic', async t => {
  const f = await traces(t), key = Object.keys(f.rows).find(name => name.startsWith('next-web'));
  const original = f.rows[key];
  f.rows[key] += 'openat(AT_FDCWD, "/releases/new/artifact/web/web/.next/cache/test", O_WRONLY|O_CREAT, 0600) = 3\nconnect(4, {sa_family=AF_INET, sin_port=htons(58089), sin_addr=inet_addr("127.0.0.1")}, 16) = 0\n';
  await f.save(); assert.equal((await auditDualTraceFiles(f.root, manifests)).mutationCalls, 1);
  for (const violation of ['openat(AT_FDCWD, "/releases/new/manifest.json", O_WRONLY|O_CREAT, 0600) = -1 EROFS', 'connect(4, {sa_family=AF_INET, sin_port=htons(443), sin_addr=inet_addr("203.0.113.1")}, 16) = -1 ENETUNREACH', 'sendto(4, 0xabc, 10, 0, 0, 0) = 10']) {
    f.rows[key] = `${original}${violation}\n`; await f.save(); await assert.rejects(auditDualTraceFiles(f.root, manifests));
  }
});

test('dual syscall audit fails missing Web identity, missing traces and special trace files', async t => {
  const f = await traces(t), key = Object.keys(f.rows).find(name => name.startsWith('next-web'));
  f.rows[key] = ''; await f.save(); await assert.rejects(auditDualTraceFiles(f.root, manifests), /API\/Web/);
  await rm(f.root, { recursive: true }); await assert.rejects(auditDualTraceFiles(f.root, manifests));
  const linked = await traces(t), target = path.join(linked.root, Object.keys(linked.rows)[0]);
  await symlink(target, path.join(linked.root, 'foreign.trace.999')); await assert.rejects(auditDualTraceFiles(linked.root, manifests), /type\/size/);
});
