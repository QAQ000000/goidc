import test from 'node:test';
import assert from 'node:assert/strict';
import { lstat, mkdtemp, mkdir, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { assetArtifact, auditTraceLine, pageAssets, validateTestDSN } from './release-runtime-acceptance-lib.mjs';
import { copyIndependent, namespaceArgs } from './accept-release-runtime.mjs';

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
  await copyIndependent(source, copy);
  const [a, b] = await Promise.all([lstat(path.join(source, 'nested/file')), lstat(path.join(copy, 'nested/file'))]);
  assert.equal(b.nlink, 1); assert.notEqual(`${a.dev}:${a.ino}`, `${b.dev}:${b.ino}`); assert.equal(b.mode & 0o222, 0);
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
  assert.doesNotThrow(() => auditTraceLine('connect(3, {sa_family=AF_INET, sin_port=htons(55432), sin_addr=inet_addr("127.0.0.1")}, 16) = 0'));
  assert.throws(() => auditTraceLine('openat(AT_FDCWD, "/release/server.js", O_WRONLY) = -1 EROFS'), /Undeclared/);
  assert.throws(() => auditTraceLine('rename("/mutable/a", "/release/b") = -1 EROFS'), /Undeclared/);
  assert.throws(() => auditTraceLine('connect(3, {sa_family=AF_INET, sin_addr=inet_addr("203.0.113.1")}, 16) = -1 ENETUNREACH'), /Non-loopback/);
  assert.doesNotThrow(() => auditTraceLine('mkdir("/release/web/.next/cache/a", 0700) = 0', ['/release/web/.next/cache']));
});
