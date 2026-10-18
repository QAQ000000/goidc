import test from 'node:test';
import assert from 'node:assert/strict';
import { chmod, cp, mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import { execFileSync, spawnSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseRuntimeBuildOptions } from './build-release-runtime-candidate.mjs';
import { runtimeBuilderFiles } from './release-runtime-lib.mjs';
import { fileHash, inventory, sha256 } from './release-candidate-lib.mjs';
import { budgetComponents, collectTraceEvidence, controlledRuntimeEnvironment, freeSpace, makeLaunchDescription, makeOriginBuildRecord, ownedBuildTemporaryEnvironment, pinnedJSON, resolveLaunchStep, runControlled, runtimeTarget, standaloneConfig, validateBudget, validateDependencies, validateRewrite } from './release-runtime-builder-lib.mjs';

async function fixture(t) {
  const root = await mkdtemp(path.join(os.tmpdir(), 'zjmfgo-runtime-builder-test-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  return root;
}
const budget = () => ({ format: 'zjmfgo-runtime-budget-v1', approvedBy: 'test-only', maxPeakBytes: 1024 * 1024 * 16, minReserveBytes: 1, minReserveInodes: 1, maxBuildSeconds: 5, components: Object.fromEntries(budgetComponents.map(name => [name, 1024])) });
const web = { appRoot: 'artifact/web/web', entry: 'artifact/web/web/server.js', apiInternalURL: 'http://127.0.0.1:58088' };
const binaries = Object.fromEntries(['api', 'worker', 'migrate'].map(name => [name, { path: `artifact/bin/${name}` }]));
const runtime = { node: { path: 'artifact/runtime/node' }, host: { platform: 'linux/x64', requirements: ['test baseline'] } };
const launch = () => makeLaunchDescription({ web, binaries, runtime, webOrigin: 'http://127.0.0.1:53088', runtimeUser: 'www' });

test('Go and Node temporary writes stay in owned candidate filesystem', async t => {
  const root = await fixture(t), temporary = await ownedBuildTemporaryEnvironment(root);
  assert.deepEqual(temporary, { TMPDIR: path.join(root, '.scratch/tmp'), GOTMPDIR: path.join(root, '.scratch/tmp') });
  const nodeFile = execFileSync(process.execPath, ['-e', `const fs=require('node:fs'),os=require('node:os'),path=require('node:path');const dir=fs.mkdtempSync(path.join(os.tmpdir(),'node-'));fs.writeFileSync(path.join(dir,'proof'),'owned');process.stdout.write(dir)`], { env: temporary, encoding: 'utf8' });
  assert(nodeFile.startsWith(temporary.TMPDIR + '/'));
  assert.equal(await readFile(path.join(nodeFile, 'proof'), 'utf8'), 'owned');
  const go = execFileSync('/usr/local/btgo/bin/go', ['env', 'GOTMPDIR'], { env: { ...temporary, GOTOOLCHAIN: 'local', GOENV: 'off' }, encoding: 'utf8' }).trim();
  assert.equal(go, temporary.GOTMPDIR);
  // Prove parent-component links cannot redirect private temporary writes.
  const unsafe = await fixture(t), outside = await fixture(t);
  await symlink(outside, path.join(unsafe, '.scratch'));
  await assert.rejects(ownedBuildTemporaryEnvironment(unsafe), /Symlink/);
});

test('recorded peak accounts for all six components and hard reserve', () => {
  const input = budget();
  assert.equal(validateBudget(input, { bytes: 1024 ** 3, inodes: 100 }).componentSumBytes, 6144);
  for (const name of budgetComponents) {
    const missing = structuredClone(input); delete missing.components[name];
    assert.throws(() => validateBudget(missing, { bytes: 1024 ** 3, inodes: 100 }), /budget/);
  }
  assert.throws(() => validateBudget(input, { bytes: 1, inodes: 100 }), /insufficient/);
  assert.throws(() => validateBudget(input, { bytes: 1024 ** 3, inodes: 0 }), /insufficient/);
  assert.throws(() => validateBudget({ ...input, maxPeakBytes: 1024 }, { bytes: 1024 ** 3, inodes: 100 }), /exceed/);
});

test('external provenance requires independently pinned bytes and forbids input symlinks', async t => {
  const root = await fixture(t), filename = path.join(root, 'proof.json');
  const raw = JSON.stringify({ source: 'fixture' }); await writeFile(filename, raw);
  assert.deepEqual((await pinnedJSON(filename, sha256(raw))).value, { source: 'fixture' });
  await assert.rejects(pinnedJSON(filename), /anchor/);
  await assert.rejects(pinnedJSON(filename, 'a'.repeat(64)), /digest/);
  await symlink(filename, path.join(root, 'proof-link.json'));
  await assert.rejects(pinnedJSON(path.join(root, 'proof-link.json'), sha256(raw)), /Symlink/);
});

test('dependency hash alone cannot stand in for trusted installation provenance', async t => {
  const root = await fixture(t), deps = path.join(root, 'node_modules'); await mkdir(deps);
  await writeFile(path.join(deps, 'package.json'), '{}');
  const files = await inventory(deps, { dependencies: true });
  const tools = { node: process.version, npm: '11.0.0', platform: 'linux/x64' }, lock = 'b'.repeat(64);
  const proof = { format: 'zjmfgo-runtime-dependency-provenance-v1', lockSHA256: lock, inputTreeSHA256: sha256(JSON.stringify(files)), files, installation: { command: 'npm ci', source: 'trusted fixture install', registry: 'https://registry.npmjs.org', integrityVerified: true, nodeVersion: tools.node, npmVersion: tools.npm, platform: tools.platform, immutability: 'test input owned exclusively' } };
  await validateDependencies(deps, proof, lock, tools);
  for (const key of ['source', 'immutability', 'integrityVerified']) {
    const altered = structuredClone(proof); delete altered.installation[key];
    await assert.rejects(validateDependencies(deps, altered, lock, tools), /provenance/);
  }
  await assert.rejects(validateDependencies(deps, proof, 'c'.repeat(64), tools), /provenance/);
  await assert.rejects(validateDependencies(deps, proof, lock, { ...tools, node: 'v0' }), /toolchain/);
  await writeFile(path.join(deps, 'package.json'), '{"tampered":true}');
  await assert.rejects(validateDependencies(deps, proof, lock, tools), /tree mismatch/);
});

test('ports are explicit isolated loopback targets, baked config is data-only', () => {
  assert.equal(runtimeTarget(web.apiInternalURL, 'http://127.0.0.1:53088').api, web.apiInternalURL);
  for (const target of ['http://127.0.0.1:8088', 'http://127.0.0.1:3088', 'http://127.0.0.1:1024', 'http://127.0.0.1:1', 'https://127.0.0.1:58088', 'http://[::1]:58088', 'http://localhost:58088', 'http://example.com:58088', 'http://127.0.0.1', 'http://u:p@127.0.0.1:58088']) assert.throws(() => runtimeTarget(target, 'http://127.0.0.1:53088'));
  const config = standaloneConfig('const nextConfig = {"output":"standalone","distDir":"./.next"}\n');
  assert.equal(config.output, 'standalone');
  assert.throws(() => standaloneConfig('const nextConfig = (() => { throw 1 })()'), /Unrecognized/);
  assert.throws(() => standaloneConfig('const nextConfig = {"output":"export","distDir":"./.next"}\n'), /mismatch/);
  const routes = { rewrites: { beforeFiles: [{ source: '/api/:path*', destination: `${web.apiInternalURL}/api/:path*` }] } };
  validateRewrite(routes, web.apiInternalURL);
  assert.throws(() => validateRewrite(routes, 'http://127.0.0.1:58089'), /rewrite mismatch/);
});

test('public origin defaults to Web, may use a distinct isolated port and never changes API/HTTP listeners', () => {
  const webOrigin = 'http://127.0.0.1:53088', publicOrigin = 'http://127.0.0.1:53188';
  assert.deepEqual(runtimeTarget(web.apiInternalURL, webOrigin), { api: web.apiInternalURL, web: webOrigin, public: webOrigin });
  assert.equal(runtimeTarget(web.apiInternalURL, webOrigin, publicOrigin).public, publicOrigin);
  for (const origin of [web.apiInternalURL, 'http://127.0.0.1:1024', 'http://127.0.0.1:8088', 'http://127.0.0.1:3088', 'https://127.0.0.1:53188', 'http://localhost:53188', 'http://[::1]:53188', 'http://127.0.0.1', 'http://u:p@127.0.0.1:53188', 'http://127.0.0.1:53188/path']) {
    assert.throws(() => runtimeTarget(web.apiInternalURL, webOrigin, origin));
  }
  const description = makeLaunchDescription({ web, binaries, runtime, webOrigin, publicOrigin, runtimeUser: 'www' });
  assert.equal(description.webOrigin, webOrigin);
  assert.equal(description.publicOrigin, publicOrigin);
  assert.equal(description.environment.fixed.PORT, '53088');
  assert.equal(description.environment.fixed.HOSTNAME, '127.0.0.1');
  assert.equal(description.environment.fixed.API_INTERNAL_URL, web.apiInternalURL);
  assert.equal(description.environment.fixed.HTTP_ADDR, '127.0.0.1:58088');
  assert.equal(description.environment.fixed.PUBLIC_ORIGIN, publicOrigin);
  assert.equal(description.environment.fixed.PUBLIC_ORIGINS, publicOrigin);
  const record = makeOriginBuildRecord({ apiInternalURL: web.apiInternalURL, webOrigin, publicOrigin, sourceCommit: 'a'.repeat(40) });
  assert.equal(record.sourceCommit, 'a'.repeat(40));
  assert.equal(record.publicOrigin, publicOrigin);
  assert.deepEqual(record.environment, { API_INTERNAL_URL: web.apiInternalURL, PUBLIC_ORIGIN: publicOrigin, PUBLIC_ORIGINS: publicOrigin });
  assert.equal(makeOriginBuildRecord({ apiInternalURL: web.apiInternalURL, webOrigin, sourceCommit: 'a'.repeat(40) }).publicOrigin, webOrigin);
  assert.throws(() => makeOriginBuildRecord({ apiInternalURL: web.apiInternalURL, webOrigin }), /fixed source commit/);
});

test('builder CLI keeps public-origin optional and retains explicit duplicate/execute validation', () => {
  const names = ['source', 'commit', 'output', 'dependencies', 'dependency-provenance', 'dependency-provenance-sha256', 'node', 'node-license', 'node-provenance', 'node-provenance-sha256', 'budget', 'budget-sha256', 'api-internal-url', 'web-origin', 'runtime-user'];
  const args = names.flatMap(name => [`--${name}`, `fixture-${name}`]);
  assert.equal(parseRuntimeBuildOptions(args).publicOrigin, undefined);
  assert.equal(parseRuntimeBuildOptions(args).execute, undefined);
  const publicOrigin = 'http://127.0.0.1:53189';
  const options = parseRuntimeBuildOptions([...args, '--public-origin', publicOrigin, '--execute']);
  assert.equal(options.publicOrigin, publicOrigin);
  assert.equal(options.execute, true);
  assert.equal(args.length, names.length * 2);
  assert.throws(() => parseRuntimeBuildOptions([...args, '--public-origin']), /explicit named/);
  assert.throws(() => parseRuntimeBuildOptions([...args, '--public-origin', publicOrigin, '--public-origin', publicOrigin]), /explicit named/);
  assert.throws(() => parseRuntimeBuildOptions([...args, '--execute', '--public-origin', publicOrigin]), /explicit named/);
  assert.throws(() => parseRuntimeBuildOptions(args.slice(2)), /Required options/);
});

test('launch binds same package paths, readiness order and bounded cleanup', () => {
  const description = launch();
  assert.equal(description.node, runtime.node.path);
  assert.equal(description.steps.find(step => step.name === 'web').cwd, web.appRoot);
  assert.equal(description.steps.find(step => step.name === 'migrate').repeat, 2);
  assert.equal(description.environment.fixed.HTTP_ADDR, '127.0.0.1:58088');
  assert.equal(description.environment.fixed.API_INTERNAL_URL, web.apiInternalURL);
  assert.equal(description.environment.inherit, false);
  assert.equal(description.publicOrigin, description.webOrigin);
  assert.equal(description.environment.fixed.PUBLIC_ORIGIN, description.webOrigin);
  assert(description.writablePaths.every(item => item.root === 'mutable'));
  assert.throws(() => makeLaunchDescription({ web, binaries, runtime, webOrigin: 'http://127.0.0.1:53088', runtimeUser: 'root' }), /Ordinary/);
});

test('controlled config rejects mode/version/missing/foreign secrets without echoing values; environment ignores inherited values', async t => {
  const root = await fixture(t), filename = path.join(root, 'config.json'), description = launch();
  const values = { DATABASE_URL: 'postgres://test-only', MFA_ENCRYPTION_KEY: 'a'.repeat(64), PURCHASE_FIELD_ENCRYPTION_KEY: 'b'.repeat(64), PROVISION_ENCRYPTION_KEY: 'c'.repeat(64) };
  async function put(config, mode = 0o600) { await writeFile(filename, JSON.stringify(config), { mode }); await chmod(filename, mode); }
  await put({ version: description.configVersion, values });
  const environment = await controlledRuntimeEnvironment(description, filename);
  assert.deepEqual(Object.keys(environment).sort(), [...Object.keys(values), ...Object.keys(description.environment.fixed)].sort());
  for (const key of ['PATH', 'HOME', 'NODE_PATH', 'NODE_OPTIONS', 'SMTP_PASSWORD', 'EPAY_KEY']) assert.equal(environment[key], undefined);
  await put({ version: 'wrong', values }); await assert.rejects(controlledRuntimeEnvironment(description, filename), /version/);
  await put({ version: description.configVersion, values }, 0o644); await assert.rejects(controlledRuntimeEnvironment(description, filename), /permissions/);
  const missing = { ...values }; delete missing.DATABASE_URL;
  await put({ version: description.configVersion, values: missing }); await assert.rejects(controlledRuntimeEnvironment(description, filename), /missing required key: DATABASE_URL/);
  await writeFile(filename, '{malformed-secret-do-not-echo');
  await assert.rejects(controlledRuntimeEnvironment(description, filename), error => error.message === 'Controlled config JSON invalid');
  await put({ version: description.configVersion, values: { ...values, NODE_OPTIONS: 'do-not-echo-this-secret' } });
  await assert.rejects(controlledRuntimeEnvironment(description, filename), error => !error.message.includes('do-not-echo') && /unallowed/.test(error.message));
});

test('public origins cannot be inherited or supplied through external config', async t => {
  const root = await fixture(t), filename = path.join(root, 'config.json');
  const description = makeLaunchDescription({ web, binaries, runtime, webOrigin: 'http://127.0.0.1:53088', publicOrigin: 'http://127.0.0.1:53188', runtimeUser: 'www' });
  const values = { DATABASE_URL: 'postgres://test-only', MFA_ENCRYPTION_KEY: 'a'.repeat(64), PURCHASE_FIELD_ENCRYPTION_KEY: 'b'.repeat(64), PROVISION_ENCRYPTION_KEY: 'c'.repeat(64) };
  const saved = { PUBLIC_ORIGIN: process.env.PUBLIC_ORIGIN, PUBLIC_ORIGINS: process.env.PUBLIC_ORIGINS };
  try {
    process.env.PUBLIC_ORIGIN = process.env.PUBLIC_ORIGINS = 'http://127.0.0.1:3088';
    await writeFile(filename, JSON.stringify({ version: description.configVersion, values }), { mode: 0o600 });
    const environment = await controlledRuntimeEnvironment(description, filename);
    assert.equal(environment.PUBLIC_ORIGIN, description.publicOrigin);
    assert.equal(environment.PUBLIC_ORIGINS, description.publicOrigin);
    for (const key of ['PUBLIC_ORIGIN', 'PUBLIC_ORIGINS', 'PORT', 'API_INTERNAL_URL']) {
      await writeFile(filename, JSON.stringify({ version: description.configVersion, values: { ...values, [key]: 'foreign-origin' } }));
      await assert.rejects(controlledRuntimeEnvironment(description, filename), /unallowed/);
    }
  } finally {
    for (const [key, value] of Object.entries(saved)) if (value === undefined) delete process.env[key]; else process.env[key] = value;
  }
});

async function traceFixture(t) {
  const root = await fixture(t), checkout = path.join(root, 'checkout'), standalone = path.join(checkout, 'web/.next/standalone'), metaRoot = path.join(root, 'meta');
  const depRoot = path.join(checkout, 'web/node_modules');
  await mkdir(path.join(depRoot, 'next'), { recursive: true });
  await writeFile(path.join(depRoot, 'next/package.json'), '{"name":"next","version":"16.3.8"}');
  await mkdir(path.join(standalone, 'web/node_modules/next'), { recursive: true });
  await cp(path.join(depRoot, 'next/package.json'), path.join(standalone, 'web/node_modules/next/package.json'));
  const traceFile = path.join(checkout, 'web/.next/next-server.js.nft.json');
  await writeFile(traceFile, JSON.stringify({ version: 1, files: ['../node_modules/next/package.json'] }));
  await writeFile(path.join(checkout, 'web/.next/next-minimal-server.js.nft.json'), JSON.stringify({ version: 1, files: [] }));
  const manifests = { 'server/pages-manifest.json': {}, 'server/app-paths-manifest.json': {}, 'server/middleware-manifest.json': { version: 3, middleware: {}, functions: {} }, 'server/functions-config-manifest.json': { version: 1, functions: {} }, 'required-server-files.json': { files: [] } };
  for (const [name, value] of Object.entries(manifests)) { await mkdir(path.dirname(path.join(checkout, 'web/.next', name)), { recursive: true }); await writeFile(path.join(checkout, 'web/.next', name), JSON.stringify(value)); }
  const dependencies = { files: await inventory(depRoot, { dependencies: true }) };
  return { root, checkout, standalone, metaRoot, traceFile, source: { files: [] }, dependencies };
}

test('same-build trace evidence maps every selected dependency byte and retains exact trace', async t => {
  const options = await traceFixture(t), result = await collectTraceEvidence(options);
  assert.deepEqual(result.files, [{ path: 'artifact/web/web/node_modules/next/package.json', sourcePath: 'next/package.json' }]);
  assert.equal(result.traces[0].entries[0].kind, 'dependency');
  assert.equal(await readFile(path.join(options.metaRoot, 'traces/next-server.js.nft.json'), 'utf8'), await readFile(options.traceFile, 'utf8'));
  assert.deepEqual(result.omitted, [{ path: 'artifact/meta/traces/next-minimal-server.js.nft.json', sha256: await fileHash(path.join(options.checkout, 'web/.next/next-minimal-server.js.nft.json')), reason: 'next16-copyTracedFiles-uses-next-server-not-minimal-server' }]);
});

test('trace escapes, symlinks, directories, untracked source and changed outputs fail closed', async t => {
  for (const problem of ['escape', 'link', 'directory', 'untracked', 'tampered', 'extraDependency']) {
    const options = await traceFixture(t);
    if (problem === 'escape') await writeFile(options.traceFile, JSON.stringify({ version: 1, files: ['../../../outside'] }));
    if (problem === 'directory') await writeFile(options.traceFile, JSON.stringify({ version: 1, files: ['../node_modules/next'] }));
    if (problem === 'link') { await symlink('next/package.json', path.join(options.checkout, 'web/node_modules/link.json')); await writeFile(options.traceFile, JSON.stringify({ version: 1, files: ['../node_modules/link.json'] })); }
    if (problem === 'untracked') { await writeFile(path.join(options.checkout, 'untracked.json'), '{}'); await writeFile(options.traceFile, JSON.stringify({ version: 1, files: ['../../untracked.json'] })); }
    if (problem === 'tampered') await writeFile(path.join(options.standalone, 'web/node_modules/next/package.json'), '{"changed":true}');
    if (problem === 'extraDependency') await writeFile(path.join(options.standalone, 'web/node_modules/next/extra.js'), 'extra');
    await assert.rejects(collectTraceEvidence(options), undefined, problem);
  }
});

test('unknown Next trace names fail closed rather than entering the standalone closure', async t => {
  const options = await traceFixture(t);
  await writeFile(path.join(options.checkout, 'web/.next/unexpected.nft.json'), JSON.stringify({ version: 1, files: [] }));
  await assert.rejects(collectTraceEvidence(options), /Unknown or unselected Next trace/);
});

test('build command is refused before spawn on reserve failure and bounded on timeout', async t => {
  const root = await fixture(t), initialFreeBytes = (await freeSpace(root)).bytes, input = { ...budget(), maxPeakBytes: Number.MAX_SAFE_INTEGER };
  await assert.rejects(runControlled(process.execPath, ['-e', 'throw Error("must not run")'], { cwd: root, env: {}, root, budget: { ...input, minReserveBytes: Number.MAX_SAFE_INTEGER }, initialFreeBytes }), /reserve/);
  await assert.rejects(runControlled(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { cwd: root, env: {}, root, budget: { ...input, maxBuildSeconds: 1 }, initialFreeBytes }), /timed out/);
  assert.equal(await runControlled(process.execPath, ['-e', 'process.stdout.write("ok")'], { cwd: root, env: {}, root, budget: input, initialFreeBytes, capture: true }), 'ok');
});


test('private readonly bind places actual tracing input inside checkout without copying dependencies', async t => {
  if (process.platform !== 'linux' || spawnSync('/usr/bin/unshare', ['--mount', '--propagation', 'private', 'true']).status !== 0) { t.skip('BLOCKED: private mount namespace unavailable'); return; }
  const root = await fixture(t), input = path.join(root, 'trusted-input'), mounted = path.join(root, 'checkout/web/node_modules');
  await mkdir(input); await mkdir(mounted, { recursive: true }); await writeFile(path.join(input, 'tiny.js'), 'trusted-fixture');
  const code = `
    const fs = require('node:fs');
    const cp = require('node:child_process');
    const [input, mounted] = process.argv.slice(1);
    cp.execFileSync('/usr/bin/mount', ['--bind', input, mounted]);
    cp.execFileSync('/usr/bin/mount', ['-o', 'remount,bind,ro', mounted]);
    if (fs.realpathSync(mounted + '/tiny.js') !== mounted + '/tiny.js') throw Error('escaped realpath');
    if (fs.readFileSync(mounted + '/tiny.js', 'utf8') !== 'trusted-fixture') throw Error('bad bytes');
    try { fs.writeFileSync(mounted + '/tiny.js', 'changed'); throw Error('write unexpectedly succeeded'); }
    catch (error) { if (error.code !== 'EROFS') throw error; }
    process.stdout.write('readonly-contained');
  `;
  assert.equal(execFileSync('/usr/bin/unshare', ['--mount', '--propagation', 'private', process.execPath, '-e', code, input, mounted], { encoding: 'utf8' }), 'readonly-contained');
  assert.equal(await readFile(path.join(input, 'tiny.js'), 'utf8'), 'trusted-fixture');
  // After child exit the parent still sees its empty ordinary mountpoint.
  await assert.rejects(readFile(path.join(mounted, 'tiny.js')), { code: 'ENOENT' });
});


test('Web launch step resolves after relocation and executes server.js from app cwd', async t => {
  const root = await fixture(t), old = path.join(root, 'original'), moved = path.join(root, 'relocated');
  const description = launch(), step = description.steps.find(item => item.name === 'web');
  assert.deepEqual(step.args, ['server.js']);
  await mkdir(path.join(old, description.appRoot), { recursive: true });
  await mkdir(path.join(old, 'artifact/runtime'), { recursive: true });
  // A tiny fixture wrapper invokes the real trusted test Node. This test covers
  // path resolution only; bundled Node byte provenance is a verifier concern.
  const quotedNode = "'" + process.execPath.replaceAll("'", "'\\''") + "'";
  await writeFile(path.join(old, step.executable), '#!/bin/sh\nexec ' + quotedNode + ' "$@"\n', { mode: 0o700 });
  await writeFile(path.join(old, description.entry), 'process.stdout.write(JSON.stringify({cwd:process.cwd(),entry:require.resolve("./server.js")}))');
  await cp(old, moved, { recursive: true });
  await rm(old, { recursive: true });
  const resolved = await resolveLaunchStep(moved, step);
  assert.equal(resolved.executable, path.join(moved, step.executable));
  const result = spawnSync(resolved.executable, resolved.args, { cwd: resolved.cwd, env: {}, encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(JSON.parse(result.stdout), { cwd: path.join(moved, description.appRoot), entry: path.join(moved, description.entry) });
  await assert.rejects(resolveLaunchStep(moved, { ...step, executable: '../escape' }), /Unsafe/);
  await assert.rejects(resolveLaunchStep(moved, { ...step, cwd: '../escape' }), /Unsafe/);
});


test('running transitive builder helpers must match the independently clean selected source', async t => {
  const root = await fixture(t), source = path.join(root, 'source'), runner = path.join(root, 'runner');
  const project = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
  for (const name of runtimeBuilderFiles) {
    for (const target of [source, runner]) {
      await mkdir(path.dirname(path.join(target, name)), { recursive: true });
      await cp(path.join(project, name), path.join(target, name));
    }
  }
  const git = (...args) => execFileSync('/usr/bin/git', ['-C', source, ...args], { encoding: 'utf8', env: { PATH: '/usr/bin:/bin', GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null' } }).trim();
  git('init', '--quiet'); git('config', 'user.name', 'Builder Fixture'); git('config', 'user.email', 'fixture@example.test');
  git('add', '.'); git('commit', '--quiet', '-m', 'fixed clean builder source');
  const revision = git('rev-parse', 'HEAD');
  const code = `
    import { pathToFileURL } from 'node:url';
    const [builder, source, revision] = process.argv.slice(1);
    const { buildRuntimeCandidate } = await import(pathToFileURL(builder));
    try { await buildRuntimeCandidate({ source, revision }); process.exitCode = 2; }
    catch (error) { process.stderr.write(error.message); process.exitCode = 1; }
  `;
  for (const name of ['scripts/build-release-candidate.mjs', 'scripts/release-candidate-lib.mjs', 'scripts/module-manifest-lib.mjs']) {
    const original = await readFile(path.join(runner, name));
    await writeFile(path.join(runner, name), Buffer.concat([original, Buffer.from('\n// changed external running helper\n')]));
    const result = spawnSync(process.execPath, ['--input-type=module', '-e', code, path.join(runner, 'scripts/build-release-runtime-candidate.mjs'), source, revision], { encoding: 'utf8', env: { PATH: '/usr/bin:/bin' } });
    assert.equal(result.status, 1, result.stderr);
    assert.match(result.stderr, /Running builder\/config differs from selected commit/);
    assert.equal(git('status', '--porcelain'), '');
    await writeFile(path.join(runner, name), original);
  }
});
