import test from 'node:test';
import assert from 'node:assert/strict';
import { chmod, lstat, mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import { execFileSync, spawnSync } from 'node:child_process';
import os from 'node:os';
import path from 'node:path';
import { fileHash, inventory, sha256 } from './release-candidate-lib.mjs';
import { validateDependencies } from './release-runtime-builder-lib.mjs';
import { cleanSourceInput, installBudget, installEnvironment, parseArgs, preflight, prepareDependencyInput, trustedToolchain, validateLock, validateRegistry } from './prepare-runtime-dependency-input.mjs';
const GiB = 1024 ** 3;
async function fixture(t, behavior = 'ok') {
  const root = await mkdtemp(path.join(os.tmpdir(), 'zjmfgo-dependency-input-test-'));
  t.after(async () => { spawnSync('/usr/bin/chmod', ['-R', 'u+w', root]); await rm(root, { recursive: true, force: true }); });
  const source = path.join(root, 'source'), npmRoot = path.join(root, 'npm');
  await mkdir(path.join(source, 'web'), { recursive: true }); await mkdir(path.join(npmRoot, 'bin'), { recursive: true });
  const lock = { lockfileVersion: 3, packages: { '': { name: 'fixture' }, 'node_modules/fixture': { version: '1.0.0', resolved: 'https://registry.npmjs.org/fixture/-/fixture-1.0.0.tgz', integrity: 'sha512-YWJjZA==' } } };
  await writeFile(path.join(source, 'web/package.json'), '{"name":"fixture"}'); await writeFile(path.join(source, 'web/package-lock.json'), JSON.stringify(lock));
  const git = (...args) => execFileSync('/usr/bin/git', ['-C', source, ...args], { encoding: 'utf8' }).trim();
  git('init', '-q'); git('config', 'user.email', 'test@example.invalid'); git('config', 'user.name', 'test'); git('add', '.'); git('commit', '-qm', 'fixture');
  const node = path.join(root, 'fake-node'), npmCLI = path.join(npmRoot, 'bin/npm-cli.js'), nodeLicense = path.join(root, 'LICENSE');
  // Tiny owned simulator for control-flow tests; never a claimed real npm run.
  await writeFile(node, `#!/bin/sh\nif [ "$1" = --version ]; then echo v24.16.0; exit 0; fi\nif [ "$2" = --version ]; then echo 11.13.0; exit 0; fi\n${behavior === 'timeout' ? '/usr/bin/sleep 5' : behavior === 'fail' ? 'exit 7' : ''}\n/usr/bin/mkdir -p node_modules/fixture\n/usr/bin/printf fixture > node_modules/fixture/file\n/usr/bin/printf 'owned fixture install complete'\n`, { mode: 0o700 });
  await writeFile(npmCLI, 'trusted fake cli'); await writeFile(nodeLicense, 'fixture license');
  const nodeProvenance = path.join(root, 'node.json'), npmProvenance = path.join(root, 'npm.json');
  const n = { format: 'zjmfgo-runtime-node-provenance-v1', platform: 'linux/x64', version: 'v24.16.0', source: 'unit fixture, not production provenance', sha256: await fileHash(node), licenseSHA256: await fileHash(nodeLicense) };
  const files = await inventory(npmRoot, { dependencies: true });
  await writeFile(nodeProvenance, JSON.stringify(n));
  await writeFile(npmProvenance, JSON.stringify({ format: 'zjmfgo-runtime-npm-provenance-v1', platform: 'linux/x64', nodeVersion: n.version, version: '11.13.0', source: 'unit fixture, not production provenance', inputTreeSHA256: sha256(JSON.stringify(files)), files }));
  return { root, lock, source, revision: git('rev-parse', 'HEAD'), output: path.join(root, 'output'), node, nodeLicense, nodeProvenance, nodeProvenanceSHA256: await fileHash(nodeProvenance), npmRoot, npmCLI, npmProvenance, npmProvenanceSHA256: await fileHash(npmProvenance), registryURL: 'https://registry.npmjs.org', budget: { cacheBytes: 1024, outputBytes: 1024, reserveBytes: 1, maxPeakBytes: 1024 * 1024, maxSeconds: 5 } };
}

test('install budget and registry fail closed without echoing credential-bearing input', () => {
  assert.deepEqual(installBudget({ cacheBytes: 10, outputBytes: 20, reserveBytes: 30, maxPeakBytes: 40, maxSeconds: 50 }), { cacheBytes: 10, outputBytes: 20, reserveBytes: 30, maxPeakBytes: 40, maxSeconds: 50 });
  for (const value of [0, -1, NaN, Infinity, '1.5']) assert.throws(() => installBudget({ maxSeconds: value }), /integer/);
  assert.throws(() => installBudget({ cacheBytes: 10, outputBytes: 20, maxPeakBytes: 29 }), /cover/);
  assert.equal(validateRegistry(), 'https://registry.npmjs.org');
  for (const value of ['http://registry.npmjs.org', 'https://u:do-not-print@registry.npmjs.org', 'https://registry.npmjs.org/?token=do-not-print', 'https://other.invalid']) assert.throws(() => validateRegistry(value), e => /registry/.test(e.message) && !e.message.includes('do-not-print'));
  assert.throws(() => parseArgs(['--execute', '--source', '/tmp']), /explicit/);
});
test('controlled environment omits inherited tokens/options/proxies and uses private config/cache', () => {
  const env = installEnvironment('/tmp/owned');
  for (const key of ['NODE_OPTIONS', 'NODE_PATH', 'NPM_TOKEN', 'HTTP_PROXY', 'HTTPS_PROXY', 'DATABASE_URL']) assert.equal(env[key], undefined);
  assert.equal(env.NPM_CONFIG_IGNORE_SCRIPTS, 'true'); assert.equal(env.NPM_CONFIG_CACHE, '/tmp/owned/cache'); assert.equal(env.PATH, '/usr/bin:/bin');
});
test('lock requires only official HTTPS tarballs and SHA512 integrity', async t => {
  const f = await fixture(t); assert.equal(validateLock(f.lock), 1);
  for (const patch of [{ integrity: undefined }, { resolved: 'file:../shared' }, { resolved: 'https://u:p@registry.npmjs.org/pkg.tgz' }, { link: true }]) { const lock = structuredClone(f.lock); Object.assign(lock.packages['node_modules/fixture'], patch); assert.throws(() => validateLock(lock), /BLOCKED/); }
});
test('preflight performs no installation or output mutation', async t => {
  const f = await fixture(t); const p = await preflight(f); assert.equal(p.status, 'PREFLIGHT_ONLY'); assert.equal(p.installationExecuted, false); await assert.rejects(lstat(f.output), { code: 'ENOENT' });
});
test('source requires full clean fixed commit and rejects symlinked lock', async t => {
  const f = await fixture(t); const input = await cleanSourceInput(f.source, f.revision); assert.match(input.lockSHA256, /^[a-f0-9]{64}$/);
  await assert.rejects(cleanSourceInput(f.source, f.revision.slice(0, 7)), /full commit/);
  await writeFile(path.join(f.source, 'web/package-lock.json'), '{}'); await assert.rejects(cleanSourceInput(f.source, f.revision), /dirty/);
  execFileSync('/usr/bin/git', ['-C', f.source, 'checkout', '--', 'web/package-lock.json']);
  await symlink(f.source, path.join(f.root, 'source-link')); await assert.rejects(cleanSourceInput(path.join(f.root, 'source-link'), f.revision), /Symlink/);
});
test('external Node/npm anchors and full tool trees are checked before invocation', async t => {
  const f = await fixture(t); await trustedToolchain(f);
  await assert.rejects(trustedToolchain({ ...f, nodeProvenanceSHA256: 'a'.repeat(64) }), /digest/);
  await writeFile(f.npmCLI, 'altered'); await assert.rejects(trustedToolchain(f), /npm tree/);
});
test('existing output and insufficient budget never delete or create paths', async t => {
  const f = await fixture(t); await mkdir(f.output); await writeFile(path.join(f.output, 'keep'), 'keep'); await assert.rejects(prepareDependencyInput({ ...f, execute: true }), /already exists/); assert.equal(await readFile(path.join(f.output, 'keep'), 'utf8'), 'keep');
  await assert.rejects(preflight({ ...f, output: path.join(f.root, 'other'), budget: { ...f.budget, maxPeakBytes: 1000 * GiB } }), /insufficient disk/); await assert.rejects(lstat(path.join(f.root, 'other')), { code: 'ENOENT' });
});
test('owned simulator success produces builder-compatible proof and sealed tree', async t => {
  const f = await fixture(t); const result = await prepareDependencyInput({ ...f, execute: true }); const proof = JSON.parse(await readFile(result.provenance, 'utf8'));
  assert.equal(result.status, 'INSTALLED'); assert.equal(await fileHash(result.provenance), result.provenanceSHA256); assert.equal((await lstat(path.join(result.dependencies, 'fixture/file'))).mode & 0o222, 0); assert.equal((await lstat(path.join(result.dependencies, 'fixture'))).mode & 0o222, 0);
  await validateDependencies(result.dependencies, proof, proof.lockSHA256, { node: 'v24.16.0', npm: '11.13.0', platform: 'linux/x64' });
  await assert.rejects(lstat(path.join(f.output, 'cache')), { code: 'ENOENT' }); assert.equal(proof.evidence.sourceCommit, f.revision); assert.match(proof.evidence.logSHA256, /^[a-f0-9]{64}$/);
});
for (const behavior of ['fail', 'timeout']) test(`owned simulator ${behavior} removes only exclusive output`, async t => {
  const f = await fixture(t, behavior); f.budget.maxSeconds = 1;
  await assert.rejects(prepareDependencyInput({ ...f, execute: true }), behavior === 'timeout' ? /timed out/ : /failed/);
  await assert.rejects(lstat(f.output), { code: 'ENOENT' }); assert.equal(await readFile(f.npmCLI, 'utf8'), 'trusted fake cli');
});
