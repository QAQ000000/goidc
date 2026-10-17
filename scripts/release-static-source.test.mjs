import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { sourceProof } from './release-candidate-lib.mjs';
import { runtimeInventory } from './release-runtime-lib.mjs';
import { verifySealedStaticSource } from './accept-release-static-retention.mjs';

async function fixture(t) {
  const root = await mkdtemp(path.join(os.tmpdir(), 'retained-static-source-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const source = path.join(root, 'source'), sealed = path.join(root, 'sealed');
  await mkdir(source); await mkdir(path.join(sealed, 'artifact/web/site/.next/static'), { recursive: true });
  const git = (...args) => execFileSync('/usr/bin/git', ['-C', source, ...args], { stdio: 'pipe' });
  git('init', '--quiet'); git('config', 'user.name', 'Fixture'); git('config', 'user.email', 'fixture@local.invalid');
  await writeFile(path.join(source, 'source.txt'), 'frozen source'); git('add', 'source.txt'); git('commit', '--quiet', '-m', 'fixture');
  const artifact = path.join(sealed, 'artifact/web/site/.next/static/chunk.js'); await writeFile(artifact, 'old static asset');
  // Intentionally has no runtime launch contract: accepting static bytes must
  // neither require a valid launch nor claim that this is a runnable package.
  const manifest = { format: 'zjmfgo-release-candidate-v2', source: await sourceProof(source, 'HEAD'), web: { appRoot: 'artifact/web/site' }, artifacts: await runtimeInventory(sealed) };
  const filename = path.join(sealed, 'manifest.json'); await writeFile(filename, JSON.stringify(manifest));
  const anchor = createHash('sha256').update(await readFile(filename)).digest('hex');
  return { source, sealed, artifact, filename, anchor, manifest };
}

test('historical static verification authenticates frozen source and bytes without accepting a launch contract', async t => {
  const f = await fixture(t);
  assert.deepEqual(await verifySealedStaticSource(f.sealed, f.anchor, f.source), f.manifest);
  assert.equal(f.manifest.launch, undefined);
});

test('historical static verification rejects stale anchors, altered bytes and extra files', async t => {
  const f = await fixture(t);
  await assert.rejects(verifySealedStaticSource(f.sealed, '0'.repeat(64), f.source), /anchor/);
  await writeFile(f.artifact, 'bad static asset');
  await assert.rejects(verifySealedStaticSource(f.sealed, f.anchor, f.source), /inventory/);
  await writeFile(f.artifact, 'old static asset'); await writeFile(path.join(f.sealed, 'artifact/extra.txt'), 'unexpected');
  await assert.rejects(verifySealedStaticSource(f.sealed, f.anchor, f.source), /inventory/);
});

test('historical static verification refuses a dirty checkout even when the manifest anchor matches', async t => {
  const f = await fixture(t); await writeFile(path.join(f.source, 'source.txt'), 'changed source');
  await assert.rejects(verifySealedStaticSource(f.sealed, f.anchor, f.source), /dirty/);
});
