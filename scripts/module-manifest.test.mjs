import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { captureModuleBuildEvidence, moduleManifestFormat, moduleProfilePath, validateModulePair, validateModuleProfile, verifyModuleBuildEvidence } from './module-manifest-lib.mjs';

const hash = value => createHash('sha256').update(value).digest('hex');
const module = { category: 'sms', id: 'fixture', version: 'builtin', schema_version: 1, contract_version: 1, instance_mode: 'channel', factory_available: true };
const profile = { format: 'goidc-module-build-profile-v1', profile: 'default', contract_version: 1, modules: [module] };
const listing = binary => ({ format: moduleManifestFormat, profile: 'default', binary, contract_version: 1, modules: [{ ...module, descriptor_sha256: 'a'.repeat(64) }] });
const pair = () => ({ api: listing('api'), worker: listing('worker') });

test('required composition rejects missing, extra, incompatible or divergent executable contracts', () => {
  validateModulePair(profile, pair());
  for (const mutate of [
    value => value.api.modules.pop(),
    value => value.api.modules.push({ ...value.api.modules[0], id: 'other' }),
    value => { value.worker.modules[0].schema_version = 2; },
    value => { value.worker.modules[0].factory_available = false; },
    value => { value.worker.modules[0].instance_mode = 'supplier'; },
    value => { value.worker.modules[0].descriptor_sha256 = 'b'.repeat(64); },
    value => { value.worker.contract_version = 2; },
    value => { value.worker.binary = 'api'; },
    value => { value.api.modules[0].config = { key: 'forbidden' }; },
  ]) {
    const manifests = pair(); mutate(manifests);
    assert.throws(() => validateModulePair(profile, manifests), /Invalid|Unsupported|differ|mismatch/);
  }
  assert.throws(() => validateModuleProfile({ ...profile, modules: [module, module] }), /sorted and unique/);
});

test('default profile requires the CAPTCHA and Telegram Bot factories by identity', async () => {
  const current = JSON.parse(await readFile(new URL('../audit/module-build-profile.json', import.meta.url), 'utf8'));
  const modules = validateModuleProfile(current);
  assert.deepEqual(modules.find(item => item.category === 'bot' && item.id === 'telegram_bot'), { category: 'bot', id: 'telegram_bot', version: 'builtin', schema_version: 1, contract_version: 1, instance_mode: 'channel', factory_available: true });
  assert.deepEqual(modules.find(item => item.category === 'captcha' && item.id === 'builtin_png'), { category: 'captcha', id: 'builtin_png', version: 'builtin', schema_version: 1, contract_version: 1, instance_mode: 'profile', factory_available: true });
  assert.throws(() => validateModuleProfile({ ...current, modules: [{ ...module, category: 'unknown' }] }), /Invalid compiled module identity/);
});

async function fixture(t, metaRelative = 'artifact/meta') {
  const root = await mkdtemp(path.join(os.tmpdir(), 'goidc-module-manifest-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const source = path.join(root, 'source');
  await mkdir(path.join(source, 'audit'), { recursive: true });
  const profileRaw = `${JSON.stringify(profile)}\n`;
  await writeFile(path.join(source, moduleProfilePath), profileRaw);
  const binaryPaths = { api: 'artifact/bin/api', worker: 'artifact/bin/worker' };
  await mkdir(path.join(root, 'artifact/bin'), { recursive: true });
  for (const binary of Object.values(binaryPaths)) await writeFile(path.join(root, binary), `not executable test bytes: ${binary}`);
  const calls = [];
  const run = (filename, args, cwd, capture) => {
    calls.push({ filename, args, cwd, capture });
    return JSON.stringify(listing(path.basename(filename)));
  };
  const evidence = await captureModuleBuildEvidence({ source, root, metaRelative, binaryPaths, run });
  const sourceFiles = [{ path: moduleProfilePath, sha256: hash(profileRaw) }];
  async function artifacts() {
    const names = [evidence.profile_path, ...Object.values(evidence.manifests).map(item => item.path), ...Object.values(binaryPaths)];
    return Promise.all(names.map(async name => { const raw = await readFile(path.join(root, name)); return { path: name, sha256: hash(raw), bytes: raw.length }; }));
  }
  const options = { root, evidence, sourceFiles, metaRelative, binaryPaths, artifacts: await artifacts() };
  return { options, calls, source, root, artifacts };
}

test('capture executes only introspection and verification binds source/listings/binary hashes without execution', async t => {
  const f = await fixture(t);
  assert.equal(f.calls.length, 2);
  for (const call of f.calls) {
    assert.deepEqual(call.args, ['--module-manifest']);
    assert.equal(call.cwd, f.source);
    assert.equal(call.capture, true);
  }
  await verifyModuleBuildEvidence(f.options);
  assert.equal(f.calls.length, 2, 'offline verification must not execute artifact binaries');
  for (const mutate of [
    value => { delete value.evidence; },
    value => { value.evidence.profile_sha256 = '0'.repeat(64); },
    value => { value.evidence.profile_path = '../profile.json'; },
    value => { value.evidence.manifests.worker.binary_sha256 = '0'.repeat(64); },
    value => { value.evidence.manifests.api.path = 'artifact/meta/modules-worker.json'; },
    value => { value.artifacts = value.artifacts.filter(item => item.path !== value.evidence.manifests.api.path); },
  ]) {
    const options = structuredClone(f.options); mutate(options);
    await assert.rejects(verifyModuleBuildEvidence(options), /Invalid|mismatch/);
  }
  await writeFile(path.join(f.root, f.options.evidence.manifests.worker.path), JSON.stringify({ ...listing('worker'), modules: [] }));
  f.options.artifacts = await f.artifacts();
  f.options.evidence.manifests.worker.sha256 = f.options.artifacts.find(item => item.path === f.options.evidence.manifests.worker.path).sha256;
  await assert.rejects(verifyModuleBuildEvidence(f.options), /Invalid compiled module records/);
});

test('legacy sources retain their previous scope and cannot claim unanchored module evidence', async () => {
  await verifyModuleBuildEvidence({ sourceFiles: [], evidence: undefined });
  await assert.rejects(verifyModuleBuildEvidence({ sourceFiles: [], evidence: {} }), /no source profile/);
});
