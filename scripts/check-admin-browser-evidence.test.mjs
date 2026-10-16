import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, writeFile, readFile, rm } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import os from 'node:os';
import { fileURLToPath } from 'node:url';
import { checkEvidence } from './check-admin-browser-evidence.mjs';

const digest = text => createHash('sha256').update(text).digest('hex');
const script = fileURLToPath(new URL('./check-admin-browser-evidence.mjs', import.meta.url));
async function fixture(run) {
  const root = await mkdtemp(path.join(os.tmpdir(), 'admin-browser-evidence-'));
  try { await writeFile(path.join(root, 'capture.png'), 'current screenshot'); await run(root); }
  finally { await rm(root, { recursive: true, force: true }); }
}
const entry = overrides => ({ newRoute: '/admin/jobs', width: 320, result: 'PASS', scope: 'historical layout only', screenshot: 'capture.png', screenshotSha256: digest('historical screenshot'), ...overrides });
const declaration = { status: 'mismatch', expectedSha256: digest('historical screenshot'), observedSha256: digest('current screenshot'), usableForIntegrityProof: false, checkedAt: '2026-10-16', reason: 'Historical path reused; revalidation required.' };

test('distinguishes matching, mismatched, unestablished, absent and invalid evidence without changing input', async () => fixture(async root => {
  const manifest = { pages: [entry({ screenshotSha256: digest('current screenshot') }), entry({}), entry({ screenshotSha256: undefined }), entry({ screenshot: 'absent.png' }), entry({ screenshotSha256: 'broken' }), entry({ screenshot: root })] };
  const before = JSON.stringify(manifest);
  const report = await checkEvidence({ root, manifest });
  assert.deepEqual(report.pages.map(e => e.status), ['verified', 'mismatch', 'missing_hash', 'missing_file', 'invalid_hash', 'read_error']);
  assert.equal(report.summary.strict_failures, 5);
  assert.equal(report.pages[0].usableForIntegrityProof, true);
  assert.ok(report.pages.slice(1).every(e => !e.usableForIntegrityProof));
  assert.equal(JSON.stringify(manifest), before);
  assert.equal(await readFile(path.join(root, 'capture.png'), 'utf8'), 'current screenshot');
}));

test('a declared historical mismatch is visible and unusable, and does not hide a later overwrite or changed expected hash', async () => fixture(async root => {
  const manifest = { pages: [entry({ screenshotIntegrity: declaration }), entry({ screenshotSha256: undefined })] };
  const report = await checkEvidence({ root, manifest });
  assert.equal(report.strictPassed, false);
  assert.equal(report.overallIncomplete, true);
  assert.equal(report.recordingConsistent, true);
  assert.equal(report.summary.declared_mismatch, 1);
  assert.equal(report.summary.missing_hash, 1);
  assert.equal(report.pages[0].usableForIntegrityProof, false);
  manifest.pages[0].screenshotSha256 = digest('different history');
  assert.equal((await checkEvidence({ root, manifest })).strictPassed, false);
  manifest.pages[0].screenshotSha256 = declaration.expectedSha256;
  await writeFile(path.join(root, 'capture.png'), 'later overwrite');
  const drift = await checkEvidence({ root, manifest });
  assert.equal(drift.strictPassed, false);
  assert.equal(drift.summary.unexpected_mismatch, 1);
}));

test('a status label alone cannot exempt mismatches from strict checks', async () => fixture(async root => {
  for (const screenshotIntegrity of [{ status: 'mismatch' }, { ...declaration, usableForIntegrityProof: true }, { ...declaration, reason: '' }]) {
    assert.equal((await checkEvidence({ root, manifest: { pages: [entry({ screenshotIntegrity })] } })).strictPassed, false);
  }
}));

test('CLI reports JSON and enforces strict exit code while leaving manifest and screenshots untouched', async () => fixture(async root => {
  const filename = path.join(root, 'manifest.json');
  const original = JSON.stringify({ pages: [entry({})] });
  await writeFile(filename, original);
  const args = [script, '--manifest', filename, '--root', root];
  const permissive = spawnSync(process.execPath, args, { encoding: 'utf8' });
  assert.equal(permissive.status, 0, permissive.stderr);
  assert.equal(JSON.parse(permissive.stdout).summary.unexpected_mismatch, 1);
  const strict = spawnSync(process.execPath, [...args, '--strict'], { encoding: 'utf8' });
  assert.equal(strict.status, 1, strict.stderr);
  assert.equal(JSON.parse(strict.stdout).strictPassed, false);
  assert.equal(await readFile(filename, 'utf8'), original);
  assert.equal(await readFile(path.join(root, 'capture.png'), 'utf8'), 'current screenshot');
  await writeFile(filename, JSON.stringify({ pages: [entry({ screenshotIntegrity: declaration })] }));
  const declared = spawnSync(process.execPath, [...args, '--strict'], { encoding: 'utf8' });
  assert.equal(declared.status, 1, declared.stderr);
  const acknowledged = spawnSync(process.execPath, [...args, '--strict-unacknowledged'], { encoding: 'utf8' });
  assert.equal(acknowledged.status, 0, acknowledged.stderr);
  assert.equal(JSON.parse(acknowledged.stdout).overallIncomplete, true);
  assert.equal(JSON.parse(acknowledged.stdout).recordingConsistent, true);
  assert.equal(JSON.parse(declared.stdout).summary.mismatch, 1);
  await writeFile(filename, JSON.stringify({ pages: [entry({ screenshotSha256: undefined })] }));
  const unestablished = spawnSync(process.execPath, [...args, '--strict'], { encoding: 'utf8' });
  assert.equal(unestablished.status, 1, unestablished.stderr);
  assert.equal(JSON.parse(unestablished.stdout).summary.missing_hash, 1);
  assert.equal(JSON.parse(unestablished.stdout).summary.mismatch, 0);
  await writeFile(filename, JSON.stringify({ pages: [entry({ screenshotSha256: digest('current screenshot') })] }));
  const verified = spawnSync(process.execPath, [...args, '--strict'], { encoding: 'utf8' });
  assert.equal(verified.status, 0, verified.stderr);
  assert.equal(JSON.parse(verified.stdout).overallIncomplete, false);
}));
