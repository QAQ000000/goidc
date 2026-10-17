import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { readPrivateManifest } from './g09-source-controller.mjs';

test('private source manifests reject links, public permissions and oversized input', t => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'g09-source-private-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  fs.chmodSync(directory, 0o700);
  const filename = path.join(directory, 'manifest.json');
  fs.writeFileSync(filename, '{}', { mode: 0o600 });
  assert.deepEqual(readPrivateManifest(filename).manifest, {});
  fs.chmodSync(filename, 0o644);
  assert.throws(() => readPrivateManifest(filename));
  fs.chmodSync(filename, 0o600);
  const hardlink = path.join(directory, 'hardlink');
  fs.linkSync(filename, hardlink);
  assert.throws(() => readPrivateManifest(filename));
  fs.unlinkSync(hardlink);
  const symlink = path.join(directory, 'symlink');
  fs.symlinkSync(filename, symlink);
  assert.throws(() => readPrivateManifest(symlink));
  const alias = `${directory}-alias`;
  fs.symlinkSync(directory, alias); t.after(() => fs.unlinkSync(alias));
  assert.throws(() => readPrivateManifest(path.join(alias, 'manifest.json')));
  fs.chmodSync(directory, 0o755);
  assert.throws(() => readPrivateManifest(filename));
  fs.chmodSync(directory, 0o700);
  fs.writeFileSync(filename, ' '.repeat(65537));
  assert.throws(() => readPrivateManifest(filename));
});

for (const [script, mode, result] of [
  ['g09-source-api-smoke.mjs', 'source_api', 'result.json'],
  ['g09-epay-source-smoke.mjs', 'source_epay', 'epay-result.json'],
]) test(`${mode} refuses an existing evidence file without disclosing private input`, t => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'g09-source-refuse-'));
  fs.chmodSync(directory, 0o700);
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const filename = path.join(directory, 'manifest.json');
  const flows = mode === 'source_api' ? ['balance_pay', 'local_renew', 'purchase_local', 'ticket'].map(kind => ({ kind })) : ['epay_alipay', 'epay_wxpay'].map(provider => ({ provider }));
  fs.writeFileSync(filename, JSON.stringify({ schemaVersion: 1, mode, flows, key: 'never-print-this-secret' }), { mode: 0o600 });
  fs.writeFileSync(path.join(directory, result), 'preserve-me', { mode: 0o600 });
  const child = spawnSync(process.execPath, [fileURLToPath(new URL(script, import.meta.url)), filename], { encoding: 'utf8' });
  assert.equal(child.status, 1);
  assert.equal(child.stdout, '');
  assert.doesNotMatch(child.stderr, /never-print|Error:|manifest.json/);
  assert.match(child.stderr, /failed; inspect private/);
  assert.equal(fs.readFileSync(path.join(directory, result), 'utf8'), 'preserve-me');
  assert.equal(fs.readdirSync(directory).length, 2);
});
