import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { mkdtemp, mkdir, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const guard = fileURLToPath(new URL('./check-web-build-runtime.mjs', import.meta.url));
const run = cwd => spawnSync(process.execPath, [guard], { cwd, encoding: 'utf8', timeout: 5000 });

test('actual Linux processes protect the same real Web directory without blocking isolated fixtures', { skip: process.platform !== 'linux', timeout: 15000 }, async t => {
  const root = await mkdtemp(path.join(os.tmpdir(), 'zjmfgo-build-guard-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const web = path.join(root, 'web');
  const isolated = path.join(root, 'isolated-web');
  const alias = path.join(root, 'alias-web');
  await mkdir(web); await mkdir(isolated); await symlink(web, alias);
  // These children really run with the selected cwd / kernel cmdline. No fake
  // proc tree, source-text assertions, sockets, application DB or service used.
  const childFile = path.join(root, 'fixture.cjs');
  const cliFile = path.join(root, 'next');
  const source = `if (process.env.GUARD_CHILD_TITLE) process.title = process.env.GUARD_CHILD_TITLE; process.send('ready'); setInterval(() => {}, 1000);`;
  await writeFile(childFile, source); await writeFile(cliFile, source);
  async function child(file, args = [], title) {
    const p = spawn(process.execPath, [file, ...args], { cwd: web, env: { ...process.env, ...(title ? { GUARD_CHILD_TITLE: title } : {}) }, stdio: ['ignore', 'ignore', 'ignore', 'ipc'] });
    await once(p, 'message');
    return p;
  }
  async function stop(p) { const done = once(p, 'exit'); p.kill('SIGTERM'); await done; }
  assert.equal(run(web).status, 0, 'idle directory');
  const runtime = await child(childFile, [], 'next-server (runtime-guard-test)');
  try {
    const denied = run(web);
    assert.equal(denied.status, 1); assert.match(denied.stderr, /拒绝构建/); assert.match(denied.stderr, new RegExp(String(runtime.pid)));
    assert.equal(run(alias).status, 1, 'symlink cannot bypass real cwd comparison');
    assert.equal(run(isolated).status, 0, 'another worktree can build');
  } finally { await stop(runtime); }
  for (const action of ['start', 'dev']) {
    const cli = await child(cliFile, [action, '-p', '0']);
    try { assert.equal(run(web).status, 1, `actual CLI cmdline next ${action}`); }
    finally { await stop(cli); }
  }
  const fixture = await child(childFile, ['next', 'start', 'browser-worker-test']);
  try { assert.equal(run(web).status, 0, 'browser / worker fixture mentioning next is not a runtime'); }
  finally { await stop(fixture); }
  assert.equal(run(web).status, 0, 'exited runtime does not block');
});
