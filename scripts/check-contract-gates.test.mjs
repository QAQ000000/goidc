import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { checkContractGates } from './check-contract-gates.mjs';

async function fixture(run) {
  const cwd = await mkdtemp(path.join(os.tmpdir(), 'contract-gates-'));
  try { await run(cwd); } finally { await rm(cwd, { recursive: true, force: true }); }
}

async function writeManifest(cwd, manifest) {
  await mkdir(path.join(cwd, 'audit'), { recursive: true });
  await writeFile(path.join(cwd, 'audit/contract-gates.json'), `${JSON.stringify(manifest)}\n`);
}

const valid = {
  schemaVersion: 1,
  contracts: [{
    id: 'payments',
    contract: 'docs/payments.md',
    implementation: ['internal/payment'],
    tests: ['./scripts/go.sh test ./...'],
    evaluation: [],
    review: { required: true, owners: ['payment'] },
    releaseEvidence: { required: true, kind: 'isolated-pg' },
  }],
};

test('accepts a complete contract entry and checks repository paths', async () => fixture(async cwd => {
  await mkdir(path.join(cwd, 'docs'), { recursive: true });
  await mkdir(path.join(cwd, 'internal/payment'), { recursive: true });
  await writeFile(path.join(cwd, 'docs/payments.md'), '# payments\n');
  await writeManifest(cwd, valid);
  const report = await checkContractGates({ cwd });
  assert.equal(report.ok, true, JSON.stringify(report.errors));
  assert.equal(report.contractCount, 1);
}));

test('rejects dated contracts, missing implementations and missing review owners', async () => fixture(async cwd => {
  await mkdir(path.join(cwd, 'docs'), { recursive: true });
  await writeFile(path.join(cwd, 'docs/payments-20261018.md'), '# snapshot\n');
  await writeManifest(cwd, {
    ...valid,
    contracts: [{ ...valid.contracts[0], contract: 'docs/payments-20261018.md', implementation: ['internal/missing'], review: { required: true, owners: [] } }],
  });
  const report = await checkContractGates({ cwd });
  assert.equal(report.ok, false);
  assert.ok(report.errors.some(error => error.path.endsWith('.contract')));
  assert.ok(report.errors.some(error => error.message === 'path does not exist'));
  assert.ok(report.errors.some(error => error.path.endsWith('.review')));
}));
