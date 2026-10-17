import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, rm } from 'node:fs/promises';
import { spawnSync } from 'node:child_process';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { checkDocs } from './check-doc-governance.mjs';

const script = fileURLToPath(new URL('./check-doc-governance.mjs', import.meta.url));

async function fixture(run) {
  const cwd = await mkdtemp(path.join(os.tmpdir(), 'doc-governance-'));
  try { await run(cwd); } finally { await rm(cwd, { recursive: true, force: true }); }
}

function git(cwd, args) {
  const result = spawnSync('git', ['-C', cwd, ...args], { encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
}

async function baseline(cwd) {
  await mkdir(path.join(cwd, 'docs/archive'), { recursive: true });
  await mkdir(path.join(cwd, 'docs/generated'), { recursive: true });
  await writeFile(path.join(cwd, 'docs/INDEX.md'), '# Documentation index\n');
  await writeFile(path.join(cwd, 'docs/archive/maintenance-20261017.tar.gz'), Buffer.from([0, 1, 2, 3]));
  await writeFile(path.join(cwd, 'docs/archive/old-20261017.md'), '# historical\n');
  await writeFile(path.join(cwd, 'docs/audit.json'), '{}\n');
  await writeFile(path.join(cwd, 'docs/generated/legacy-inventory.json'), '{}\n');
  git(cwd, ['init', '-q']);
  git(cwd, ['config', 'user.email', 'test@example.com']);
  git(cwd, ['config', 'user.name', 'Doc Governance Test']);
  git(cwd, ['add', '.']);
  git(cwd, ['commit', '-qm', 'baseline']);
}

test('requires the index and tolerates existing dated and machine-artifact baseline files', async () => fixture(async cwd => {
  await baseline(cwd);
  const report = await checkDocs({ cwd });
  assert.equal(report.ok, true, JSON.stringify(report.errors));
  assert.deepEqual(report.checks.dateSuffix.newFiles, []);
  assert.deepEqual(report.checks.artifacts.newFiles, []);
  assert.ok(report.baselineArtifacts.includes('docs/archive/maintenance-20261017.tar.gz'));
}));

test('rejects newly added dated Markdown and JSON/binary artifacts', async () => fixture(async cwd => {
  await baseline(cwd);
  await writeFile(path.join(cwd, 'docs/payment-contract-20261018.md'), '# new snapshot\n');
  await writeFile(path.join(cwd, 'docs/route-review.json'), '{}\n');
  await writeFile(path.join(cwd, 'docs/capture.bin'), Buffer.from([0, 255, 1]));
  await writeFile(path.join(cwd, '.gitignore'), 'docs/ignored.json\n');
  await writeFile(path.join(cwd, 'docs/ignored.json'), '{}\n');
  const report = await checkDocs({ cwd });
  assert.equal(report.ok, false);
  assert.deepEqual(report.checks.dateSuffix.newFiles, ['docs/payment-contract-20261018.md']);
  assert.deepEqual(report.checks.artifacts.newFiles, ['docs/capture.bin', 'docs/ignored.json', 'docs/route-review.json']);
  assert.deepEqual(report.errors.map(error => error.rule), ['date-suffix', 'artifact', 'artifact', 'artifact']);
}));

test('enforces active Markdown aggregate and per-file budgets', async () => fixture(async cwd => {
  await mkdir(path.join(cwd, 'docs'), { recursive: true });
  await writeFile(path.join(cwd, 'docs/INDEX.md'), '# index\n');
  await writeFile(path.join(cwd, 'docs/large.md'), 'x'.repeat(40));
  await writeFile(path.join(cwd, 'docs/other.md'), 'y'.repeat(30));
  const report = await checkDocs({ cwd, maxActiveBytes: 50, maxFileBytes: 35 });
  assert.equal(report.ok, false);
  assert.equal(report.checks.activeSize.bytes, 78);
  assert.deepEqual(report.checks.activeSize.oversized.map(file => file.path), ['docs/large.md']);
  assert.ok(report.errors.some(error => error.rule === 'file-size'));
  assert.ok(report.errors.some(error => error.rule === 'active-size'));
}));

test('CLI emits a machine-readable report and non-zero status on a missing index', async () => fixture(async cwd => {
  await mkdir(path.join(cwd, 'docs'), { recursive: true });
  await writeFile(path.join(cwd, 'docs/current.md'), '# current\n');
  const result = spawnSync(process.execPath, [script, '--root', cwd], { encoding: 'utf8' });
  assert.equal(result.status, 1, result.stderr);
  const report = JSON.parse(result.stdout);
  assert.equal(report.ok, false);
  assert.equal(report.checks.index.present, false);
  assert.ok(report.errors.some(error => error.rule === 'index'));
}));

test('diff-base catches a dated document already committed in the current revision', async () => fixture(async cwd => {
  await baseline(cwd);
  const base = spawnSync('git', ['-C', cwd, 'rev-parse', 'HEAD'], { encoding: 'utf8' }).stdout.trim();
  await writeFile(path.join(cwd, 'docs/committed-20261018.md'), '# committed snapshot\n');
  git(cwd, ['add', 'docs/committed-20261018.md']);
  git(cwd, ['commit', '-qm', 'new docs']);
  const report = await checkDocs({ cwd, baselineRef: base });
  assert.equal(report.ok, false);
  assert.deepEqual(report.checks.dateSuffix.newFiles, ['docs/committed-20261018.md']);
  assert.equal(report.baselineRef, base);
}));
