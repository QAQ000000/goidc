import test from 'node:test';
import assert from 'node:assert/strict';
import { G09_CAPACITY_PROFILE_FIXED, g09CapacityLoadProfile, validateG09CapacityCandidateManifest, validateG09CapacityProfile } from './g09-capacity-contract.mjs';
import { validateG09Profile } from './g09-load-lib.mjs';
import { parseG09CapacityOptions } from './accept-g09-capacity.mjs';

const profile = origin => JSON.parse(JSON.stringify({ ...G09_CAPACITY_PROFILE_FIXED, origin }));
const origin = 'http://127.0.0.1:58088';

test('fixed G09 profile binds all five windows, rates, cohort and safety thresholds', () => {
  const value = validateG09CapacityProfile(profile(origin));
  assert.deepEqual(value.phases.map(phase => phase.name), ['warmup', 'steady', 'peak', 'cohort_prepare', 'drain']);
  assert.deepEqual(value.data, { customers: 1000, services: 2000, invoices: 10000, suppliers: 10, workers: 2, provisionCohort: 300, emailCohort: 150, smsCohort: 150, unknownSupplierWrites: 1 });
  assert.equal(g09CapacityLoadProfile(profile(origin)).maxOperations, 40000);
  assert.equal(validateG09Profile(g09CapacityLoadProfile(profile(origin))).planned, 38187);
  assert.throws(() => validateG09CapacityProfile({ ...profile(origin), thresholds: { ...profile(origin).thresholds, maxFutureRetry: 101 } }), /g09_capacity_profile_drift/);
  assert.throws(() => validateG09CapacityProfile({ ...profile('http://127.0.0.1:8088') }), /g09_capacity_origin_invalid/);
});

test('candidate manifest gate binds clean source, required binaries and independent loopback origins', () => {
  const commit = 'a'.repeat(40), checksum = 'b'.repeat(64);
  const buildInfo = `vcs.revision=${commit}\nvcs.modified=false`;
  const manifest = {
    format: 'zjmfgo-release-candidate-v2', builder: { version: 2 }, source: { commit, files: [{ path: 'go.mod', sha256: checksum }] },
    migrations: [{ path: 'db/migrations/001_init.sql', sha256: checksum }], artifacts: [{ path: 'artifact/bin/api', bytes: 1, sha256: checksum }],
    launch: { path: 'launch/description.json' }, binaries: { api: { path: 'artifact/bin/api', buildInfo }, worker: { path: 'artifact/bin/worker', buildInfo }, migrate: { path: 'artifact/bin/migrate', buildInfo } },
    web: { apiInternalURL: origin, webOrigin: 'http://127.0.0.1:58089', publicOrigin: 'http://127.0.0.1:58090' },
  };
  assert.deepEqual(validateG09CapacityCandidateManifest(manifest, checksum, commit).capacityVerified, false);
  assert.throws(() => validateG09CapacityCandidateManifest({ ...manifest, web: { ...manifest.web, apiInternalURL: 'http://localhost:58088' } }, checksum, commit), /g09_candidate_origin_invalid/);
  assert.throws(() => validateG09CapacityCandidateManifest({ ...manifest, binaries: { ...manifest.binaries, worker: { ...manifest.binaries.worker, buildInfo: buildInfo.replace('false', 'true') } } }, checksum, commit), /g09_candidate_binary_identity_invalid/);
});

test('capacity CLI requires absolute pinned inputs and opt-in execution', () => {
  const args = ['--profile', '/tmp/profile.json', '--candidate', '/tmp/candidate', '--manifest-sha256', 'a'.repeat(64), '--source', '/tmp/source', '--budget', '/tmp/budget.json', '--budget-sha256', 'b'.repeat(64), '--run-root', '/tmp/run'];
  assert.equal(parseG09CapacityOptions(args).execute, undefined);
  assert.equal(parseG09CapacityOptions([...args, '--execute']).execute, true);
  for (const bad of [args.slice(1), [...args, '--profile', '/other/profile'], [...args, '--execute', '--execute'], [...args, '--manifest-sha256', 'bad']]) assert.throws(() => parseG09CapacityOptions(bad), /g09_capacity_cli_/);
});
