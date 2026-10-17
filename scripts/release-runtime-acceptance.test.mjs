import test from 'node:test';
import assert from 'node:assert/strict';
import { chmod, copyFile, lstat, mkdtemp, mkdir, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { assetArtifact, auditProcesses, auditTraceLine, pageAssets, validateTestDSN, waitFor, writeJSON } from './release-runtime-acceptance-lib.mjs';
import { copyIndependent, launch, liveProcessGroup, namespaceArgs, stop } from './accept-release-runtime.mjs';

test('acceptance controller accepts only explicit isolated test DSNs', () => {
  const schema = 'release_runtime_0123456789abcdef0123456789abcdef';
  assert.deepEqual(validateTestDSN(`postgres://127.0.0.1:55432/zjmfgo_test?search_path=${schema}`, schema), { port: 55432, database: 'zjmfgo_test' });
  assert.throws(() => validateTestDSN('postgres://db.internal:5432/business', schema), /loopback/);
  assert.throws(() => validateTestDSN(`postgres://127.0.0.1:55432/zjmfgo_test?search_path=public`, schema), /dedicated schema/);
  assert.throws(() => validateTestDSN(`postgres://127.0.0.1:55432/zjmfgo_test?search_path=${schema}&options=-csearch_path%3D${schema}`, schema), /Ambiguous/);
});

test('page checks retain same-origin assets and bind them to sealed static paths', () => {
  const assets = pageAssets('<script src="/_next/static/chunks/app.js"></script><link href="/_next/static/css/app.css" rel="stylesheet">', 'http://127.0.0.1:53088');
  assert.deepEqual(assets, ['/_next/static/chunks/app.js', '/_next/static/css/app.css']);
  assert.equal(assetArtifact(assets[0], 'artifact/web/app'), 'artifact/web/app/.next/static/chunks/app.js');
  assert.throws(() => assetArtifact('/_next/static/../../secret.js', 'artifact/web/app'), /outside sealed/);
  assert.throws(() => pageAssets('<script src="https://cdn.example.test/app.js"></script>', 'http://127.0.0.1:53088'), /same-origin/);
});

test('controller fixture paths do not follow symlinked roots', async t => {
  const root = await mkdtemp(path.join(os.tmpdir(), 'runtime-acceptance-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  await mkdir(path.join(root, 'real')); await symlink(path.join(root, 'real'), path.join(root, 'link'));
  await writeFile(path.join(root, 'real', 'marker'), 'fixture');
  const { noLinks } = await import('./release-candidate-lib.mjs');
  await assert.rejects(noLinks(path.join(root, 'link')), /Symlink/);
});

test('independent copy accepts ordinary directories and never shares file inodes', async t => {
  const root = await mkdtemp(path.join(os.tmpdir(), 'runtime-copy-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const source = path.join(root, 'source'), copy = path.join(root, 'copy');
  await mkdir(path.join(source, 'nested'), { recursive: true }); await writeFile(path.join(source, 'nested/file'), 'original');
  await chmod(source, 0o700);
  await copyIndependent(source, copy);
  const [a, b] = await Promise.all([lstat(path.join(source, 'nested/file')), lstat(path.join(copy, 'nested/file'))]);
  assert.equal(b.nlink, 1); assert.notEqual(`${a.dev}:${a.ino}`, `${b.dev}:${b.ino}`); assert.equal(b.mode & 0o222, 0);
  assert.equal((await lstat(copy)).mode & 0o777, 0o755); assert.equal((await lstat(source)).mode & 0o777, 0o700);
  await writeFile(path.join(source, 'nested/file'), 'changed'); assert.equal(await readFile(path.join(copy, 'nested/file'), 'utf8'), 'original');
});

test('namespace arguments have no inherited environment, source, shared dependencies, or secret argv', () => {
  const args = namespaceArgs({ release: '/tmp/run/release', control: '/tmp/run/control', fixture: '/tmp/run/fixture', mutable: '/tmp/run/mutable', pgSocket: '/tmp/pg/.s.PGSQL.55432', uid: 4321, gid: 4322, appRoot: 'artifact/web/web' });
  assert.ok(args.includes('--unshare-all')); assert.ok(args.includes('--clearenv')); assert.ok(args.includes('--cap-drop'));
  assert.equal(args[args.indexOf('--uid') + 1], '4321');
  const mounts = args.flatMap((value, i) => value === '--ro-bind' || value === '--bind' ? [args[i + 1]] : []);
  assert.ok(!mounts.includes('/usr')); assert.ok(!mounts.includes('/usr/bin')); // Only library directories and exact strace executable are bound.
  assert.ok(!args.some(value => /DATABASE_URL|ENCRYPTION_KEY|node_modules|\/www\/wwwroot/.test(value)));
  assert.ok(args.includes('/release/artifact/web/web/.next/cache'));
});

test('write and network syscall audit rejects successful and denied escapes', () => {
  assert.doesNotThrow(() => auditTraceLine('openat(AT_FDCWD, "/mutable/tmp/a", O_WRONLY|O_CREAT, 0600) = 4'));
  assert.doesNotThrow(() => auditTraceLine('fchmod(4</mutable/tmp/a>, 0600) = 0'));
  assert.doesNotThrow(() => auditTraceLine('ftruncate(4</mutable/tmp/a>, 0) = 0'));
  assert.doesNotThrow(() => auditTraceLine('connect(3, {sa_family=AF_INET, sin_port=htons(55432), sin_addr=inet_addr("127.0.0.1")}, 16) = 0'));
  assert.throws(() => auditTraceLine('openat(AT_FDCWD, "/release/server.js", O_WRONLY) = -1 EROFS'), /Undeclared/);
  assert.throws(() => auditTraceLine('fchmod(4</mutable/../release/server.js>, 0600) = -1 EROFS'), /Undeclared/);
  assert.throws(() => auditTraceLine('fchown(4</release/server.js>, 1000, 1000) = -1 EROFS'), /Undeclared/);
  assert.throws(() => auditTraceLine('ftruncate(4, 0) = -1 EBADF'), /Undeclared/);
  assert.throws(() => auditTraceLine('openat(AT_FDCWD</mutable>, "tmp/../../release/server.js", O_WRONLY) = -1 EROFS'), /Undeclared/);
  assert.doesNotThrow(() => auditTraceLine('openat(AT_FDCWD</release/artifact>, "/mutable/tmp/a", O_WRONLY|O_CREAT, 0600) = 4</mutable/tmp/a>'));
  assert.throws(() => auditTraceLine('rename("/mutable/a", "/release/b") = -1 EROFS'), /Undeclared/);
  assert.throws(() => auditTraceLine('linkat(AT_FDCWD</mutable>, "a", AT_FDCWD</release>, "b", 0) = -1 EROFS'), /Undeclared/);
  assert.throws(() => auditTraceLine('symlinkat("/mutable/a", AT_FDCWD</release>, "b") = -1 EROFS'), /Undeclared/);
  assert.throws(() => auditTraceLine('mknodat(AT_FDCWD</release>, "b", S_IFREG|0600, 0) = -1 EROFS'), /Undeclared/);
  assert.throws(() => auditTraceLine('connect(3, {sa_family=AF_INET, sin_addr=inet_addr("203.0.113.1")}, 16) = -1 ENETUNREACH'), /Non-loopback/);
  assert.throws(() => auditTraceLine('sendto(0x3, 0x1234, 0x80, 0, 0x5678, 0x10) = -1 ENETUNREACH'), /Unresolved.*network/);
  assert.doesNotThrow(() => auditTraceLine('mkdir("/release/web/.next/cache/a", 0700) = 0', ['/release/web/.next/cache']));
});

test('IPv6 mapped loopback accepts only exact IPv4 127.0.0.1 and rejects wildcards', () => {
  const syscall = address => `bind(3, {sa_family=AF_INET6, sin6_port=htons(0), inet_pton(AF_INET6, "${address}", &sin6_addr), sin6_scope_id=0}, 28) = 0`;
  for (const address of ['::1', '::ffff:127.0.0.1', '::ffff:7f00:1', '0:0:0:0:0:ffff:127.0.0.1']) assert.doesNotThrow(() => auditTraceLine(syscall(address)), address);
  for (const address of ['::', '::ffff:0.0.0.0', '::ffff:127.0.0.2', '::ffff:203.0.113.1', '::ffff:cb00:7101', '::127.0.0.1', '2001:db8::1', 'localhost', '::1%eth0']) assert.throws(() => auditTraceLine(syscall(address)), /Non-loopback/, address);
  assert.throws(() => auditTraceLine('bind(3, {sa_family=AF_INET, sin_addr=inet_addr("0.0.0.0")}, 16) = 0'), /Non-loopback/);
  assert.throws(() => auditTraceLine('connect(3, {sa_family=AF_INET6, inet_pton(AF_INET6, "::ffff:203.0.113.1", &sin6_addr)}, 28) = -1 ENETUNREACH'), /Non-loopback/);
});

test('atomic command publication exposes complete JSON and refuses overwrite', async t => {
  const root = await mkdtemp(path.join(os.tmpdir(), 'runtime-command-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const filename = path.join(root, 'command-1.json'), payload = { operation: 'abort', padding: 'x'.repeat(100000) };
  const writing = writeJSON(filename, payload);
  await waitFor(async () => {
    const bytes = await readFile(filename, 'utf8').catch(error => { if (error.code === 'ENOENT') return null; throw error; });
    if (bytes === null) return false;
    assert.deepEqual(JSON.parse(bytes), payload); return true;
  }, 5, 'atomic command');
  await writing;
  await assert.rejects(writeJSON(filename, { operation: 'prepare' }), /overwrite/);
  assert.deepEqual(JSON.parse(await readFile(filename, 'utf8')), payload);
});

test('traced worker identity is the real child and TERM removes the entire group before restart', async t => {
  const root = await mkdtemp(path.join(os.tmpdir(), 'runtime-worker-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  await mkdir(path.join(root, 'bin')); await mkdir(path.join(root, 'artifact'));
  await copyFile(process.execPath, path.join(root, 'bin/node'));
  const args = ['-e', 'process.title="next-server (test)";process.on("SIGTERM",()=>setTimeout(()=>process.exit(0),150));setInterval(()=>{},1000)'];
  let handle;
  t.after(() => stop(handle).catch(() => {}));
  const start = () => launch('/usr/bin/strace', ['-ff', '-yy', '-qq', '-s', '256', '-o', path.join(root, 'worker.trace'), '-e', 'trace=%file,%network,fchmod,fchown,ftruncate,fallocate', path.join(root, 'bin/node'), ...args], { cwd: path.join(root, 'artifact') });
  handle = start();
  const expected = [{ name: 'worker', executable: 'bin/node', cwd: 'artifact', args, traceName: 'worker' }];
  const first = await waitFor(async () => (await auditProcesses(root, expected, root))[0], 5, 'real traced worker');
  assert.notEqual(first.pid, handle.child.pid); assert.deepEqual(first.args, args); assert.equal(first.cwd, path.join(root, 'artifact'));
  await waitFor(async () => (await readFile(`/proc/${first.pid}/cmdline`, 'utf8')).startsWith('next-server (test)'), 5, 'changed process title');
  const renamed = (await auditProcesses(root, expected, root))[0];
  assert.equal(renamed.argumentEvidence, 'same-pid-successful-execve'); assert.deepEqual(renamed.args, args);
  await stop(handle, 5);
  assert.equal(handle.groupGone, true); assert.deepEqual(await liveProcessGroup(handle.child.pid), []);
  assert.equal(await readFile(`/proc/${first.pid}/cmdline`).catch(() => null), null);
  handle = start();
  const restarted = await waitFor(async () => (await auditProcesses(root, expected, root))[0], 5, 'restarted traced worker');
  assert.notEqual(restarted.pid, first.pid); assert.equal(restarted.sha256, first.sha256);
  await stop(handle, 5);
});
