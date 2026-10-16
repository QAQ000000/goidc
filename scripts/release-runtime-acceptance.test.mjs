import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { assetArtifact, pageAssets, validateTestDSN } from './release-runtime-acceptance-lib.mjs';

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

test('controller fixture paths do not follow symlinked roots', async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), 'runtime-acceptance-'));
  await mkdir(path.join(root, 'real')); await symlink(path.join(root, 'real'), path.join(root, 'link'));
  await writeFile(path.join(root, 'real', 'marker'), 'fixture');
  const { noLinks } = await import('./release-candidate-lib.mjs');
  await assert.rejects(noLinks(path.join(root, 'link')), /Symlink/);
});
