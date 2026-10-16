import test from 'node:test';
import assert from 'node:assert/strict';
import { chmod, copyFile, link, mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import { execFileSync, spawnSync } from 'node:child_process';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { fileHash, selectedDigest, sha256, sourceProof, verifyCandidate } from './release-candidate-lib.mjs';
import { traceSelectionFormat } from './release-runtime-trace-selection.mjs';
import { readRuntimeGoBuildInfo, runtimeBuilderFiles, runtimeFormat, runtimeInventory, verifyRuntimeCandidate } from './release-runtime-lib.mjs';

const project = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const go = '/usr/local/btgo/bin/go';
async function temp(t) { const dir = await mkdtemp(path.join(os.tmpdir(), 'zjmfgo-runtime-unit-')); t.after(() => rm(dir, { force: true, recursive: true })); return dir; }
async function put(root, name, value, executable = false) {
  await mkdir(path.dirname(path.join(root, name)), { recursive: true });
  await writeFile(path.join(root, name), value, { mode: executable ? 0o755 : 0o644 });
}
function git(root, ...args) { return execFileSync('git', ['-C', root, ...args], { encoding: 'utf8' }).trim(); }

test('the checked-in HEAD contains every v2 builder and verifier input', () => {
  const tracked = new Set(git(project, 'ls-tree', '-r', '--name-only', 'HEAD').split('\n').filter(Boolean));
  for (const filename of runtimeBuilderFiles) assert.ok(tracked.has(filename), `HEAD is missing runtime contract input: ${filename}`);
});

async function sourceFixture(t) {
  const source = await temp(t);
  git(source, 'init', '--quiet'); git(source, 'config', 'user.name', 'Runtime Fixture'); git(source, 'config', 'user.email', 'fixture@example.test');
  for (const name of runtimeBuilderFiles) await put(source, name, `fixture ${name}\n`);
  await put(source, 'go.mod', 'module runtime-fixture\n\ngo 1.25\n');
  await put(source, 'go.sum', 'fixture-go-sum\n');
  await put(source, 'main.go', 'package main\nfunc main() {}\n');
  await put(source, '.gitignore', 'binary\n');
  await put(source, 'web/package-lock.json', '{"lockfileVersion":3}\n');
  await put(source, 'web/public/fixture.txt', 'public-fixture\n');
  await put(source, 'db/migrations/001_fixture.sql', 'SELECT 1;\n');
  git(source, 'add', '.'); git(source, 'commit', '--quiet', '-m', 'runtime unit source');
  // A tiny real Go binary lets the verifier check actual VCS build metadata.
  // It is never executed; no application/Web build or dependency install runs.
  execFileSync(go, ['build', '-mod=readonly', '-trimpath', '-buildvcs=true', '-o', 'binary', '.'], { cwd: source, env: { ...process.env, GOROOT: '', GOTOOLCHAIN: 'local' }, stdio: 'pipe' });
  return { source, proof: await sourceProof(source, 'HEAD'), binary: path.join(source, 'binary') };
}
async function candidate(t, fixture) {
  const root = await temp(t), appRoot = 'artifact/web/web';
  const proof = structuredClone(fixture.proof);
  for (const name of ['api', 'worker', 'migrate']) {
    await mkdir(path.join(root, 'artifact/bin'), { recursive: true });
    await copyFile(fixture.binary, path.join(root, `artifact/bin/${name}`));
    await chmod(path.join(root, `artifact/bin/${name}`), 0o755);
  }
  await put(root, 'artifact/runtime/node', 'unit-node-not-an-executable-runtime', true);
  await put(root, 'artifact/runtime/LICENSE', 'unit license');
  const nodeProof = { format: 'zjmfgo-runtime-node-provenance-v1', version: process.version, platform: `${process.platform}/${process.arch}`, source: 'unit-node fixture; no actual runtime claimed', sha256: await fileHash(path.join(root, 'artifact/runtime/node')), licenseSHA256: await fileHash(path.join(root, 'artifact/runtime/LICENSE')) };
  await put(root, 'artifact/meta/node.json', JSON.stringify(nodeProof));
  await put(root, 'artifact/migrations/001_fixture.sql', 'SELECT 1;\n');
  await put(root, `${appRoot}/server.js`, 'const nextConfig = {"output":"standalone","distDir":"./.next"}\n');
  await put(root, `${appRoot}/.next/BUILD_ID`, 'unit-build\n');
  await put(root, `${appRoot}/.next/routes-manifest.json`, JSON.stringify({ rewrites: { beforeFiles: [], afterFiles: [{ source: '/api/:path*', destination: 'http://127.0.0.1:58088/api/:path*' }], fallback: [] } }));
  await put(root, `${appRoot}/.next/required-server-files.json`, JSON.stringify({ config: { output: 'standalone', distDir: '.next' }, files: [] }));
  await put(root, `${appRoot}/.next/server/pages-manifest.json`, '{}');
  const selectionManifests = { 'server/pages-manifest.json': {}, 'server/app-paths-manifest.json': {}, 'server/middleware-manifest.json': { version: 3, middleware: {}, functions: {} }, 'server/functions-config-manifest.json': { version: 1, functions: {} } };
  for (const [name, value] of Object.entries(selectionManifests)) await put(root, `${appRoot}/.next/${name}`, JSON.stringify(value));
  const selectionPath = 'artifact/meta/trace-selection.json', omittedPath = 'artifact/meta/traces/next-minimal-server.js.nft.json';
  await put(root, omittedPath, JSON.stringify({ version: 1, files: [] }));
  await put(root, selectionPath, JSON.stringify({ format: traceSelectionFormat, nextVersion: '16.3.8', manifests: selectionManifests, requiredFiles: [], available: ['next-minimal-server.js.nft.json', 'next-server.js.nft.json'] }));

  await put(root, `${appRoot}/.next/server/app/page.js`, 'unit-generated-app');
  await put(root, `${appRoot}/.next/static/chunks/page.js`, 'unit-client-chunk');
  await put(root, `${appRoot}/public/fixture.txt`, 'public-fixture\n');
  const depContents = { 'next/package.json': '{"name":"next","version":"16.3.8"}', 'next/index.js': 'unit-runtime-dependency' };
  const inputFiles = [];
  const selected = [];
  const entries = [];
  for (const [name, bytes] of Object.entries(depContents)) {
    const output = `artifact/web/web/node_modules/${name}`;
    await put(root, output, bytes);
    inputFiles.push({ path: name, bytes: Buffer.byteLength(bytes), sha256: sha256(bytes), executable: false });
    selected.push({ path: output, sourcePath: name });
    entries.push({ input: `../node_modules/${name}`, artifactPath: output, kind: 'dependency', sourcePath: name });
  }
  entries.push({ input: 'server/app/page.js', artifactPath: `${appRoot}/.next/server/app/page.js`, kind: 'generated' });
  const provenance = { format: 'zjmfgo-runtime-dependency-provenance-v1', lockSHA256: proof.files.find(row => row.path === 'web/package-lock.json').sha256, inputTreeSHA256: sha256(JSON.stringify(inputFiles)), installation: { command: 'npm ci', source: 'unit fixture, no real install claimed', registry: 'https://registry.npmjs.org/', integrityVerified: true, nodeVersion: process.version, npmVersion: 'unit-npm', platform: `${process.platform}/${process.arch}`, immutability: 'unit fixture owned input' }, files: inputFiles };
  const provenancePath = 'artifact/meta/dependencies.json';
  await put(root, provenancePath, JSON.stringify(provenance));
  const tracePath = 'artifact/meta/traces/next-server.js.nft.json';
  await put(root, tracePath, JSON.stringify({ version: 1, files: entries.map(row => row.input) }));
  const buildInfo = readRuntimeGoBuildInfo(fixture.binary, go);
  const binaries = Object.fromEntries(['api', 'worker', 'migrate'].map(name => [name, { path: `artifact/bin/${name}`, buildInfo }]));
  const host = { platform: `${process.platform}/${process.arch}`, requirements: ['unit host baseline; no portability claimed'] };
  const launch = { version: 1, configVersion: 'runtime-unit-config-v1', runtimeUser: 'www', node: 'artifact/runtime/node', apiInternalURL: 'http://127.0.0.1:58088', webOrigin: 'http://127.0.0.1:53088', appRoot, entry: `${appRoot}/server.js`, binaries: Object.fromEntries(Object.entries(binaries).map(([key, row]) => [key, row.path])), environment: { inherit: false, allowlist: ['DATABASE_URL'], fixed: { NODE_ENV: 'production', HOSTNAME: '127.0.0.1', PORT: '53088', HTTP_ADDR: '127.0.0.1:58088', PUBLIC_ORIGIN: 'http://127.0.0.1:53088' }, forbidden: ['NODE_PATH', 'NODE_OPTIONS', '__NEXT_PRIVATE_STANDALONE_CONFIG'] }, config: { required: ['DATABASE_URL'], mode: '0600', external: true }, timeouts: { readySeconds: 30, workerSeconds: 30, stopSeconds: 15 }, writablePaths: [{ root: 'mutable', path: 'cache' }], releaseVerification: 'full-inventory-before-and-after', steps: [{ name: 'verify' }, { name: 'migrate', executable: binaries.migrate.path, cwd: 'artifact', repeat: 2 }, { name: 'api', executable: binaries.api.path, cwd: 'artifact', readyURL: 'http://127.0.0.1:58088/readyz' }, { name: 'web', executable: 'artifact/runtime/node', args: ['server.js'], cwd: appRoot }, { name: 'worker', executable: binaries.worker.path, cwd: 'artifact' }, { name: 'stop' }], host };
  await put(root, 'launch/description.json', JSON.stringify(launch));
  const manifest = { format: runtimeFormat, source: proof, builder: { version: 2, ...selectedDigest(proof.files, runtimeBuilderFiles) }, tools: { go: 'unit-host-go', node: process.version, npm: 'unit-npm', platform: host.platform }, locks: { goMod: proof.files.find(row => row.path === 'go.mod').sha256, goSum: proof.files.find(row => row.path === 'go.sum').sha256, web: provenance.lockSHA256 }, binaries, runtime: { node: { path: 'artifact/runtime/node', version: process.version, source: 'unit-node fixture; no actual runtime claimed', licensePath: 'artifact/runtime/LICENSE', provenancePath: 'artifact/meta/node.json', provenanceSHA256: await fileHash(path.join(root, 'artifact/meta/node.json')) }, host }, web: { appRoot, entry: `${appRoot}/server.js`, buildID: 'unit-build', bundler: 'webpack', nextVersion: '16.3.8', apiInternalURL: launch.apiInternalURL }, migrations: proof.files.filter(row => row.path.startsWith('db/migrations/')), dependencies: { provenancePath, provenanceSHA256: await fileHash(path.join(root, provenancePath)), lockSHA256: provenance.lockSHA256, inputTreeSHA256: provenance.inputTreeSHA256, files: selected, selectionPath, traces: [{ path: tracePath, entries }], omitted: [{ path: omittedPath, sha256: await fileHash(path.join(root, omittedPath)), reason: 'next16-copyTracedFiles-uses-next-server-not-minimal-server' }] }, launch: { path: 'launch/description.json' }, artifacts: await runtimeInventory(root) };
  async function seal() { manifest.artifacts = await runtimeInventory(root); const raw = JSON.stringify(manifest, null, 2) + '\n'; await put(root, 'manifest.json', raw); return sha256(raw); }
  async function saveProvenance() { await put(root, provenancePath, JSON.stringify(provenance)); manifest.dependencies.provenanceSHA256 = await fileHash(path.join(root, provenancePath)); }
  return { root, appRoot, manifest, provenance, launch, seal, saveProvenance, digest: await seal(), verify: digest => verifyRuntimeCandidate(root, digest, fixture.source, { goCommand: go }) };
}

test('v2 sealed runtime safety contracts with actual Go build metadata and small fixtures', async t => {
  const fixture = await sourceFixture(t);
  await t.test('valid package, internal relative file/directory links, relocation and real CLI', async t => {
    const f = await candidate(t, fixture);
    assert.equal((await f.verify(f.digest)).source.commit, fixture.proof.commit);
    await symlink('next', path.join(f.root, 'artifact/web/web/node_modules/alias'));
    await symlink('next/index.js', path.join(f.root, 'artifact/web/web/node_modules/file-alias'));
    await f.verify(await f.seal());
    const cli = spawnSync(process.execPath, [path.join(project, 'scripts/verify-release-runtime.mjs'), f.root, await f.seal(), fixture.source], { encoding: 'utf8' });
    assert.equal(cli.status, 0, cli.stderr);
    assert.equal(JSON.parse(cli.stdout).verified, true);
    assert.match(cli.stdout, /no startup/);
  });
  for (const [name, mutate, error] of [
    ['wrong anchor', async f => { f.digest = '0'.repeat(64); }, /digest mismatch/],
    ['actual artifact byte tamper', f => put(f.root, `${f.appRoot}/.next/static/chunks/page.js`, 'changed'), /inventory mismatch/],
    ['missing actual worker', f => rm(path.join(f.root, 'artifact/bin/worker')), /inventory mismatch/],
    ['added sealed file', f => put(f.root, 'artifact/unexpected', 'added'), /inventory mismatch/],
    ['executable bit tamper', f => chmod(path.join(f.root, 'artifact/bin/api'), 0o644), /inventory mismatch/],
  ]) await t.test(name, async t => { const f = await candidate(t, fixture); await mutate(f); await assert.rejects(f.verify(f.digest), error); });
  for (const [name, mutate, error] of [
    ['manifest source substitution', async f => { f.manifest.source.tree = 'f'.repeat(40); }, /source proof mismatch/],
    ['v1 format cannot be relabeled', async f => { f.manifest.format = 'zjmfgo-release-candidate-v1'; }, /Unsupported/],
    ['builder source checksum mismatch', async f => { f.manifest.builder.sha256 = '0'.repeat(64); }, /builder source mismatch/],
    ['lock mismatch', async f => { f.manifest.locks.web = '0'.repeat(64); }, /lockfile mismatch/],
    ['actual Go binary metadata differs', async f => { f.manifest.binaries.api.buildInfo += '\n\tbuild\t-extra=forged'; }, /Actual Go build info mismatch/],
    ['Go VCS revision differs', async f => { f.manifest.binaries.api.buildInfo = f.manifest.binaries.api.buildInfo.replace(f.manifest.source.commit, 'a'.repeat(40)); }, /Go binary source mismatch/],
    ['Go dirty provenance', async f => { f.manifest.binaries.api.buildInfo += '\n\tbuild\tvcs.modified=true'; }, /Go binary source mismatch/],
    ['Node source bytes mismatch', f => put(f.root, 'artifact/runtime/node', 'altered-node', true), /Node\/license source bytes mismatch/],
    ['missing Node license', async f => { await rm(path.join(f.root, 'artifact/runtime/LICENSE')); }, /Required runtime file missing/],
    ['host platform mismatch', async f => { f.manifest.runtime.host.platform = 'foreign/architecture'; }, /host baseline/],
    ['migration bytes mismatch', f => put(f.root, 'artifact/migrations/001_fixture.sql', 'SELECT 2;'), /migration source mismatch/],
    ['public bytes mismatch', f => put(f.root, `${f.appRoot}/public/fixture.txt`, 'changed'), /public source mismatch/],
    ['provenance anchor mismatch', async f => { f.manifest.dependencies.provenanceSHA256 = '0'.repeat(64); }, /provenance anchor/],
    ['untrusted installation assertion', async f => { f.provenance.installation.integrityVerified = false; await f.saveProvenance(); }, /installation record/],
    ['dependency input tree tamper', async f => { f.provenance.files[0].sha256 = '0'.repeat(64); await f.saveProvenance(); }, /input tree mismatch/],
    ['selected dependency bytes mismatch', f => put(f.root, 'artifact/web/web/node_modules/next/index.js', 'bad dependency bytes'), /dependency bytes mismatch/],
    ['unmapped dependency file', f => put(f.root, 'artifact/web/web/node_modules/extra/index.js', 'unmapped'), /Unmapped runtime dependency/],
    ['omitted trace reason tampered', async f => { f.manifest.dependencies.omitted[0].reason = 'ignore-missing'; }, /omission bytes\/reason/],
    ['omitted trace bytes tampered', async f => { await put(f.root, 'artifact/meta/traces/next-minimal-server.js.nft.json', JSON.stringify({version:1,files:['changed']})); }, /omission bytes\/reason/],
    ['omitted trace record missing', async f => { f.manifest.dependencies.omitted=[]; }, /omission proof/],
    ['trace selection manifest substituted', async f => { const filename=path.join(f.root, 'artifact/meta/trace-selection.json'); const selection=JSON.parse(await readFile(filename,'utf8'));selection.manifests['server/pages-manifest.json']['/new']='pages/new.html';await put(f.root,'artifact/meta/trace-selection.json',JSON.stringify(selection)); }, /differs from standalone/],
    ['unknown trace proof artifact', async f => { await put(f.root,'artifact/meta/traces/new.js.nft.json',JSON.stringify({version:1,files:[]})); }, /Unknown or missing trace/],
    ['trace mapping omitted', async f => { f.manifest.dependencies.traces[0].entries.pop(); }, /mapping is incomplete/],
    ['trace dependency disguised as generated', async f => { const row = f.manifest.dependencies.traces[0].entries[0]; row.kind = 'generated'; delete row.sourcePath; }, /Unknown trace provenance kind/],
    ['trace absolute input', async f => { f.manifest.dependencies.traces[0].entries[0].input = '/tmp/foreign'; }, /Unsafe\/duplicate trace input/],
    ['trace output path not derived from input', async f => { f.manifest.dependencies.traces[0].entries[0].artifactPath = f.manifest.dependencies.files[1].path; }, /input\/output path binding/],
    ['trace output traversal', async f => { f.manifest.dependencies.traces[0].entries[0].artifactPath = '../foreign'; }, /Unsafe relative/],
    ['trace directory target is not a file closure', async f => { f.manifest.dependencies.traces[0].entries[0].artifactPath = 'artifact/web/web/node_modules'; }, /inventoried file/],
    ['duplicate selected dependency', async f => { f.manifest.dependencies.files.push(f.manifest.dependencies.files[0]); }, /Duplicate selected/],
    ['BUILD_ID mismatch', async f => { f.manifest.web.buildID = 'wrong-build'; }, /BUILD_ID mismatch/],
    ['rewrite differs from build target', f => put(f.root, `${f.appRoot}/.next/routes-manifest.json`, JSON.stringify({ rewrites: [{ source: '/api/:path*', destination: 'http://127.0.0.1:59000/api/:path*' }] })), /API rewrite mismatch/],
    ['ambiguous duplicate API rewrite', f => put(f.root, `${f.appRoot}/.next/routes-manifest.json`, JSON.stringify({ rewrites: Array(2).fill({ source: '/api/:path*', destination: 'http://127.0.0.1:58088/api/:path*' }) })), /API rewrite mismatch/],
    ['embedded standalone config mismatch', f => put(f.root, `${f.appRoot}/server.js`, 'const nextConfig = {"output":"export","distDir":".next"}\n'), /embedded configuration mismatch/],
    ['embedded environment secret rejected', f => put(f.root, `${f.appRoot}/server.js`, 'const nextConfig = {"output":"standalone","distDir":".next","env":{"PRIVATE":"unit-secret"}}\n'), /embedded configuration mismatch/],
    ['Next actual package version mismatch', async f => { f.manifest.web.nextVersion = '0.0.0'; }, /Actual Next version|Trace selection required files\/Next version/],
    ['mutable cache enters sealed output', f => put(f.root, `${f.appRoot}/.next/cache/cache.bin`, 'mutable'), /Mutable Web cache/],
    ['root runtime user', async f => { f.launch.runtimeUser = 'root'; }, /runtime user/],
    ['implicit environment inheritance', async f => { f.launch.environment.inherit = true; }, /environment\/rewrite/],
    ['ambient Node module search path', async f => { f.launch.environment.allowlist.push('NODE_PATH'); }, /environment allowlist/],
    ['configuration included inline', async f => { f.launch.environment.fixed.DATABASE_URL = 'unit-secret-dsn'; }, /fixed runtime/],
    ['config not external', async f => { f.launch.config.external = false; }, /External controlled/],
    ['launch path not tied to candidate', async f => { f.launch.binaries.api = '/www/wwwroot/zjmfgo/bin/api'; }, /artifact binding/],
    ['rewrite overridden at runtime', async f => { f.launch.environment.fixed.HTTP_ADDR = '127.0.0.1:59000'; }, /environment\/rewrite/],
    ['writable root inside sealed release', async f => { f.launch.writablePaths[0].root = 'artifact'; }, /Mutable paths/],
    ['mutable path escape', async f => { f.launch.writablePaths[0].path = '../release'; }, /Unsafe relative/],
    ['launch executable outside candidate', async f => { f.launch.steps[1].executable = '/usr/bin/foreign'; }, /executable\/order binding/],
    ['Web script argument must resolve from declared cwd', async f => { f.launch.steps[3].args = [f.manifest.web.entry]; }, /executable\/order binding/],
    ['launch skips migration', async f => { f.launch.steps.splice(1, 1); }, /order\/deadlines/],
    ['infinite startup deadline', async f => { f.launch.timeouts.readySeconds = 0; }, /deadlines missing/],
  ]) await t.test(name, async t => { const f = await candidate(t, fixture); await mutate(f); await put(f.root, 'launch/description.json', JSON.stringify(f.launch)); await assert.rejects(f.verify(await f.seal()), error); });
  for (const [name, action, error] of [
    ['outside absolute link', f => symlink('/etc/passwd', path.join(f.root, 'artifact/web/outside')), /Unsafe runtime link/],
    ['relative escape link', f => symlink('../../../foreign', path.join(f.root, 'artifact/web/outside')), /escapes Web/],
    ['dangling link', f => symlink('missing', path.join(f.root, 'artifact/web/dangling')), /dangling or cyclic/],
    ['cyclic link', async f => { await symlink('b', path.join(f.root, 'artifact/web/a')); await symlink('a', path.join(f.root, 'artifact/web/b')); }, /dangling or cyclic/],
    ['runtime link outside permitted subtree', f => symlink('../web/server.js', path.join(f.root, 'artifact/bin/alias')), /only within Web/],
    ['hard linked sealed file', f => link(path.join(f.root, 'artifact/runtime/node'), path.join(f.root, 'artifact/runtime/node-link')), /Hard-linked/],
    ['sensitive env file', f => put(f.root, 'artifact/web/.env.production', 'unit-secret'), /Protected/],
    ['sensitive dependency key', f => put(f.root, 'artifact/web/web/node_modules/next/private.pem', 'unit-secret'), /Protected/],
    ['npm credential file', f => put(f.root, 'artifact/web/.npmrc', 'unit-secret'), /Protected/],
    ['mutable sibling inside sealed root', f => mkdir(path.join(f.root, 'mutable')), /Only sealed/],
  ]) await t.test(name, async t => { const f = await candidate(t, fixture); await action(f); await assert.rejects(runtimeInventory(f.root), error); });
  await t.test('source anchor required, dirty trusted source rejected and v1 does not accept v2', async t => {
    const f = await candidate(t, fixture);
    await assert.rejects(verifyRuntimeCandidate(f.root, f.digest), /Trusted clean source/);
    await assert.rejects(verifyRuntimeCandidate(f.root, undefined, fixture.source), /Trusted manifest/);
    await assert.rejects(verifyCandidate(f.root, f.digest, fixture.source), /Unsupported/);
    await put(fixture.source, 'untracked-proof', 'dirty');
    try { await assert.rejects(f.verify(f.digest), /dirty/); } finally { await rm(path.join(fixture.source, 'untracked-proof')); }
  });
  await t.test('CLI refuses missing anchors and does not expose malformed JSON contents', async t => {
    const f = await candidate(t, fixture);
    const missing = spawnSync(process.execPath, [path.join(project, 'scripts/verify-release-runtime.mjs'), f.root], { encoding: 'utf8' });
    assert.equal(missing.status, 1); assert.match(missing.stderr, /Usage/);
    const secret = 'private-fixture-value-do-not-print';
    await put(f.root, 'manifest.json', secret);
    const malformed = spawnSync(process.execPath, [path.join(project, 'scripts/verify-release-runtime.mjs'), f.root, sha256(secret), fixture.source], { encoding: 'utf8' });
    assert.equal(malformed.status, 1); assert.match(malformed.stderr, /Invalid runtime manifest JSON/); assert.ok(!malformed.stderr.includes(secret));
  });
});
