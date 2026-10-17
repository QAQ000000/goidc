import { lstat, readFile } from 'node:fs/promises';
import path from 'node:path';
import { sha256, noLinks } from './release-candidate-lib.mjs';
import { verifyRuntimeCandidate } from './release-runtime-lib.mjs';
import { freeSpace, pinnedJSON, validateBudget } from './release-runtime-builder-lib.mjs';
import { validateG09Profile } from './g09-load-lib.mjs';

const requireThat = (value, code) => { if (!value) throw new Error(code); };
const isInt = (value, min, max) => Number.isSafeInteger(value) && value >= min && value <= max;
const hash = /^[a-f0-9]{64}$/;
const deepFreeze = value => {
  if (value && typeof value === 'object' && !Object.isFrozen(value)) {
    for (const item of Object.values(value)) deepFreeze(item);
    Object.freeze(value);
  }
  return value;
};

const phase = (name, durationMs, readRate, businessRate = 0) => ({
  name, durationMs,
  streams: [
    { category: 'read', unit: 'http_request', ratePerSecond: readRate },
    ...(businessRate ? [
      { category: 'balance_pay_and_settlement', unit: 'business_transaction', ratePerSecond: businessRate * 0.4 },
      { category: 'local_renew', unit: 'business_transaction', ratePerSecond: businessRate * 0.3 },
      { category: 'purchase_local', unit: 'business_transaction', ratePerSecond: businessRate * 0.1 },
      { category: 'purchase_supplier', unit: 'business_transaction', ratePerSecond: businessRate * 0.1 },
      { category: 'ticket', unit: 'business_transaction', ratePerSecond: businessRate * 0.1 },
    ] : []),
  ],
});

export const G09_CAPACITY_PROFILE_FIXED = deepFreeze({
  schemaVersion: 1,
  mode: 'sealed_candidate_capacity',
  origin: null,
  limits: Object.freeze({ maxConcurrency: 64, maxOperations: 40000, requestTimeoutMs: 15000, maxScheduleLagMs: 100, drainTimeoutMs: 30000 }),
  phases: Object.freeze([
    phase('warmup', 120000, 10, 0.2),
    phase('steady', 600000, 30, 1),
    phase('peak', 180000, 60, 2),
    phase('cohort_prepare', 120000, 10),
    phase('drain', 600000, 10),
  ]),
  transactionMixPercent: Object.freeze({ balance_pay_and_settlement: 40, local_renew: 30, purchase_local: 10, purchase_supplier: 10, ticket: 10 }),
  thresholds: Object.freeze({
    readP95Ms: 500, readP99Ms: 1000, writeP95Ms: 1000, writeP99Ms: 2000,
    maxUnexpectedFailureRatio: 0.001, maxScheduleDropRatio: 0.01, minAcceptedBusinessRatio: 0.99,
    cohortDrainSeconds: 600, cohortExpectedSucceeded: 600,
    finalMinuteWindows: 2, maxGlobalReadyRunning: 100, maxFutureRetry: 100, maxOldestReadySeconds: 900,
  }),
  data: Object.freeze({ customers: 1000, services: 2000, invoices: 10000, suppliers: 10, workers: 2, provisionCohort: 300, emailCohort: 150, smsCohort: 150, unknownSupplierWrites: 1 }),
});

function canonical(value) {
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`;
  if (value && typeof value === 'object') return `{${Object.keys(value).sort().map(key => `${JSON.stringify(key)}:${canonical(value[key])}`).join(',')}}`;
  return JSON.stringify(value);
}

export function validateG09CapacityProfile(input) {
  requireThat(input && typeof input === 'object' && !Array.isArray(input), 'g09_capacity_profile_invalid');
  let url;
  try { url = new URL(input.origin); } catch { throw new Error('g09_capacity_origin_invalid'); }
  requireThat(url.protocol === 'http:' && url.hostname === '127.0.0.1' && isInt(Number(url.port), 1025, 65535) &&
    !url.username && !url.password && url.pathname === '/' && !url.search && !url.hash && url.origin === input.origin &&
    !['8088', '3088'].includes(url.port), 'g09_capacity_origin_invalid');
  const expected = { ...G09_CAPACITY_PROFILE_FIXED, origin: input.origin };
  requireThat(canonical(input) === canonical(expected), 'g09_capacity_profile_drift');
  return Object.freeze({ ...expected, phases: expected.phases.map(p => Object.freeze({ ...p, streams: Object.freeze(p.streams.map(s => Object.freeze({ ...s }))) })) });
}

export function g09CapacityLoadProfile(input) {
  const fixed = validateG09CapacityProfile(input);
  return {
    origin: fixed.origin,
    ...fixed.limits,
    phases: fixed.phases,
  };
}

export function validateG09CapacityCandidateManifest(manifest, manifestSHA256, sourceCommit) {
  requireThat(hash.test(manifestSHA256 ?? '') && /^[a-f0-9]{40}$/.test(sourceCommit ?? ''), 'g09_candidate_anchor_invalid');
  requireThat(manifest?.format === 'zjmfgo-release-candidate-v2' && manifest.builder?.version === 2 &&
    manifest.source?.commit === sourceCommit && Array.isArray(manifest.source?.files) &&
    manifest.source.files.every(row => hash.test(row?.sha256 ?? '')) && Array.isArray(manifest.migrations) && manifest.migrations.length > 0 &&
    manifest.artifacts?.length > 0 && manifest.launch?.path === 'launch/description.json', 'g09_candidate_manifest_invalid');
  for (const name of ['api', 'worker', 'migrate']) {
    const info = manifest.binaries?.[name]?.buildInfo ?? '';
    requireThat(manifest.binaries[name].path === `artifact/bin/${name}` &&
      info.includes(`vcs.revision=${sourceCommit}`) && /(?:^|\s)vcs\.modified=false(?:\s|$)/.test(info) &&
      !info.includes('vcs.modified=true'), 'g09_candidate_binary_identity_invalid');
  }
  const launch = manifest.web;
  for (const value of [launch?.apiInternalURL, launch?.webOrigin, launch?.publicOrigin]) {
    let url;
    try { url = new URL(value); } catch { throw new Error('g09_candidate_origin_invalid'); }
    requireThat(url.protocol === 'http:' && url.hostname === '127.0.0.1' && isInt(Number(url.port), 1025, 65535) && !['8088', '3088'].includes(url.port), 'g09_candidate_origin_invalid');
  }
  return Object.freeze({ manifestSHA256, sourceCommit, candidateVerified: false, capacityVerified: false });
}

export async function preflightG09Capacity({ profilePath, candidatePath, manifestSHA256, sourcePath, budgetPath, budgetSHA256, runRoot }) {
  for (const [name, value] of Object.entries({ profilePath, candidatePath, sourcePath, budgetPath, runRoot })) {
    requireThat(typeof value === 'string' && path.isAbsolute(value), `g09_${name}_absolute_required`);
  }
  const profileFile = await noLinks(profilePath);
  const profileInfo = await lstat(profileFile);
  requireThat(profileInfo.isFile() && profileInfo.size <= 1024 * 1024, 'g09_capacity_profile_file_invalid');
  let profile;
  try { profile = validateG09CapacityProfile(JSON.parse(await readFile(profileFile, 'utf8'))); } catch (error) { throw new Error(error.message.startsWith('g09_') ? error.message : 'g09_capacity_profile_json_invalid'); }
  const candidate = await noLinks(candidatePath);
  const source = await noLinks(sourcePath);
  const budgetFile = await noLinks(budgetPath);
  const run = path.resolve(runRoot);
  requireThat(run.startsWith('/tmp/') || run.startsWith('/dev/shm/'), 'g09_capacity_run_root_invalid');
  requireThat(!(await lstat(run).catch(error => { if (error.code === 'ENOENT') return null; throw error; })), 'g09_capacity_run_root_exists');
  requireThat(![candidate, source, profileFile, budgetFile].some(item => run === item || run.startsWith(`${item}/`)), 'g09_capacity_run_root_overlaps_input');
  const manifest = await verifyRuntimeCandidate(candidate, manifestSHA256, source);
  const identity = validateG09CapacityCandidateManifest(manifest, manifestSHA256, manifest.source.commit);
  requireThat(profile.origin === manifest.web.apiInternalURL, 'g09_capacity_origin_candidate_mismatch');
  const budget = (await pinnedJSON(budgetFile, budgetSHA256)).value;
  const free = await freeSpace(path.dirname(run));
  const budgetEvidence = validateBudget(budget, free);
  const candidateBytes = manifest.artifacts.reduce((sum, row) => sum + (row.bytes ?? 0), 0);
  requireThat(candidateBytes <= budget.components.runCopy, 'g09_capacity_run_copy_budget_invalid');
  const loadPlan = validateG09Profile(g09CapacityLoadProfile(profile));
  return {
    status: 'PREFLIGHT_ONLY', mode: 'sealed_candidate_capacity', profile: { origin: profile.origin, phases: profile.phases.map(row => row.name), thresholds: profile.thresholds, data: profile.data },
    candidate: identity, runRoot: run, budgetEvidence, offeredLoadPlanned: loadPlan.planned,
    candidateVerified: true, capacityVerified: false, executionAvailable: false,
  };
}
