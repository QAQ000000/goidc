import { createHash } from 'node:crypto';
import { createReadStream } from 'node:fs';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';

export const moduleProfilePath = 'audit/module-build-profile.json';
export const moduleManifestFormat = 'goidc-compiled-modules-v1';
const profileFormat = 'goidc-module-build-profile-v1';
const evidenceFormat = 'goidc-module-build-evidence-v1';
const hash = bytes => createHash('sha256').update(bytes).digest('hex');
async function hashFile(filename) {
  const digest = createHash('sha256');
  for await (const chunk of createReadStream(filename)) digest.update(chunk);
  return digest.digest('hex');
}
const hashPattern = /^[a-f0-9]{64}$/;
const equal = (a, b) => JSON.stringify(a) === JSON.stringify(b);
const recordKeys = ['category', 'id', 'version', 'schema_version', 'contract_version', 'instance_mode', 'factory_available'];
function requireThat(value, message) { if (!value) throw new Error(message); }
function keys(value, expected, label) {
  requireThat(value && typeof value === 'object' && !Array.isArray(value) && equal(Object.keys(value).sort(), [...expected].sort()), `Invalid ${label} fields`);
}
function parse(raw, label) {
  requireThat(typeof raw === 'string' && Buffer.byteLength(raw) <= 2 * 1024 * 1024, `Invalid ${label} size`);
  try { return JSON.parse(raw); } catch { throw new Error(`Invalid ${label} JSON`); }
}
function records(modules, withDigest) {
  requireThat(Array.isArray(modules) && modules.length > 0 && modules.length <= 1000, 'Invalid compiled module records');
  const out = [];
  let previous = '';
  for (const item of modules) {
    keys(item, withDigest ? [...recordKeys, 'descriptor_sha256'] : recordKeys, 'compiled module');
    requireThat(['payment', 'oauth', 'sms', 'mail', 'bot', 'certification', 'provision', 'storage', 'captcha'].includes(item.category) && /^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$/.test(item.id), 'Invalid compiled module identity');
    requireThat(typeof item.version === 'string' && /^[a-zA-Z0-9][a-zA-Z0-9_.+-]{0,127}$/.test(item.version) && Number.isSafeInteger(item.schema_version) && item.schema_version > 0 && item.contract_version === 1, 'Unsupported compiled module version');
    requireThat(['channel', 'supplier', 'profile'].includes(item.instance_mode) && typeof item.factory_available === 'boolean', 'Invalid compiled module runtime contract');
    if (withDigest) requireThat(hashPattern.test(item.descriptor_sha256 ?? ''), 'Invalid compiled descriptor digest');
    const identity = `${item.category}:${item.id}`;
    requireThat(identity > previous, 'Compiled modules must be sorted and unique');
    previous = identity;
    out.push(Object.fromEntries(recordKeys.map(key => [key, item[key]])));
  }
  return out;
}
export function validateModuleProfile(profile) {
  keys(profile, ['format', 'profile', 'contract_version', 'modules'], 'module profile');
  requireThat(profile.format === profileFormat && profile.profile === 'default' && profile.contract_version === 1, 'Unsupported module build profile');
  return records(profile.modules, false);
}
export function validateModulePair(profile, manifests) {
  const expected = validateModuleProfile(profile);
  for (const binary of ['api', 'worker']) {
    const manifest = manifests[binary];
    keys(manifest, ['format', 'profile', 'binary', 'contract_version', 'modules'], 'compiled module manifest');
    requireThat(manifest.format === moduleManifestFormat && manifest.profile === profile.profile && manifest.binary === binary && manifest.contract_version === profile.contract_version, `Compiled module manifest contract mismatch: ${binary}`);
    requireThat(equal(records(manifest.modules, true), expected), `Compiled modules differ from required profile: ${binary}`);
  }
  requireThat(equal(manifests.api.modules, manifests.worker.modules), 'API/worker compiled module descriptor mismatch');
  return manifests;
}

// Only builders execute the trusted just-built binaries. Offline verification
// checks the sealed capture and its binary/source hashes without executing
// application code supplied by an artifact directory.
export async function captureModuleBuildEvidence({ source, root, metaRelative, binaryPaths, run }) {
  const profileRaw = await readFile(path.join(source, moduleProfilePath), 'utf8');
  const profile = parse(profileRaw, 'module profile');
  validateModuleProfile(profile);
  const raw = {}, manifests = {};
  for (const binary of ['api', 'worker']) {
    raw[binary] = run(path.join(root, binaryPaths[binary]), ['--module-manifest'], source, true);
    manifests[binary] = parse(raw[binary], 'compiled module manifest');
  }
  validateModulePair(profile, manifests);
  await mkdir(path.join(root, metaRelative), { recursive: true });
  const profilePath = `${metaRelative}/module-build-profile.json`;
  await writeFile(path.join(root, profilePath), profileRaw, { flag: 'wx' });
  const evidence = { format: evidenceFormat, profile_path: profilePath, profile_sha256: hash(profileRaw), manifests: {} };
  for (const binary of ['api', 'worker']) {
    const filename = `${metaRelative}/modules-${binary}.json`;
    const bytes = `${JSON.stringify(manifests[binary], null, 2)}\n`;
    await writeFile(path.join(root, filename), bytes, { flag: 'wx' });
    evidence.manifests[binary] = { path: filename, sha256: hash(bytes), binary_sha256: await hashFile(path.join(root, binaryPaths[binary])) };
  }
  return evidence;
}

export async function verifyModuleBuildEvidence({ root, evidence, sourceFiles, artifacts, metaRelative, binaryPaths }) {
  const sourceProfile = sourceFiles.find(file => file.path === moduleProfilePath);
  // Historical releases did not claim this proof. A source tree containing the
  // profile is a mandatory marker; it cannot drop evidence to fall back.
  if (!sourceProfile) {
    requireThat(evidence === undefined, 'Module build evidence has no source profile');
    return;
  }
  keys(evidence, ['format', 'profile_path', 'profile_sha256', 'manifests'], 'module build evidence');
  requireThat(evidence.format === evidenceFormat && evidence.profile_path === `${metaRelative}/module-build-profile.json` && evidence.profile_sha256 === sourceProfile.sha256, 'Module build profile source mismatch');
  const byPath = new Map(artifacts.map(file => [file.path, file]));
  function requireFile(filename, digest) {
    const file = byPath.get(filename);
    requireThat(file?.bytes > 0 && file.link === undefined && hashPattern.test(digest ?? '') && file.sha256 === digest, 'Module build artifact digest mismatch');
  }
  requireFile(evidence.profile_path, evidence.profile_sha256);
  const profile = parse(await readFile(path.join(root, evidence.profile_path), 'utf8'), 'module profile');
  keys(evidence.manifests, ['api', 'worker'], 'module build binaries');
  const manifests = {};
  for (const binary of ['api', 'worker']) {
    const record = evidence.manifests[binary];
    keys(record, ['path', 'sha256', 'binary_sha256'], 'module build record');
    requireThat(record.path === `${metaRelative}/modules-${binary}.json`, 'Module build manifest path mismatch');
    requireFile(record.path, record.sha256);
    requireFile(binaryPaths[binary], record.binary_sha256);
    manifests[binary] = parse(await readFile(path.join(root, record.path), 'utf8'), 'compiled module manifest');
  }
  validateModulePair(profile, manifests);
}
