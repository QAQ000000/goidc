import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import { apiTarget, builderFiles, builderVersion, inventory, selectedDigest, sha256, sourceProof, templateFiles, verifyCandidate } from './release-candidate-lib.mjs';
import { buildCandidate, prepareDestination } from './build-release-candidate.mjs';

async function temporary(t) {
  const root = await mkdtemp(path.join(os.tmpdir(), 'zjmfgo-release-unit-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  return root;
}
async function put(root, filename, content, executable = false) {
  await mkdir(path.dirname(path.join(root, filename)), { recursive: true });
  await writeFile(path.join(root, filename), content, { mode: executable ? 0o755 : 0o644 });
}
function command(root, ...args) { return execFileSync('git', ['-C', root, ...args], { encoding: 'utf8' }).trim(); }
async function repo(t) {
  const root = await temporary(t);
  command(root, 'init', '--quiet');
  command(root, 'config', 'user.email', 'fixture@example.test');
  command(root, 'config', 'user.name', 'Fixture');
  for (const name of new Set([...builderFiles, ...templateFiles, 'go.mod', 'go.sum', 'web/package-lock.json', 'db/migrations/001_fixture.sql'])) await put(root, name, `fixture ${name}\n`);
  await put(root, '.gitignore', 'ignored\n');
  command(root, 'add', '.'); command(root, 'commit', '--quiet', '-m', 'fixture');
  return root;
}
async function candidate(t) {
  const source = await repo(t);
  const proof = await sourceProof(source, 'HEAD');
  const root = await temporary(t);
  for (const name of ['api', 'worker', 'migrate']) await put(root, `bin/${name}`, 'binary-fixture-not-executable-code', true);
  for (const file of proof.files.filter(file => file.path.startsWith('deploy/') || ['web/package.json', 'web/package-lock.json', 'web/next.config.ts'].includes(file.path))) await put(root, file.path, await readFile(path.join(source, file.path)));
  await put(root, 'migrations/001_fixture.sql', await readFile(path.join(source, 'db/migrations/001_fixture.sql')));
  await put(root, 'web/.next/BUILD_ID', 'fixture-build-id\n');
  await put(root, 'web/.next/routes-manifest.json', JSON.stringify({ rewrites: { beforeFiles: [], afterFiles: [{ source: '/api/:path*', destination: 'http://127.0.0.1:8088/api/:path*' }], fallback: [] } }));
  await put(root, 'web/.next/required-server-files.json', '{}');
  await put(root, 'web/.next/server/pages-manifest.json', '{}');
  await put(root, 'web/.next/server/app/index.js', 'fixture-server');
  await put(root, 'web/.next/static/chunks/index.js', 'fixture-client');
  const manifest = {
    format: 'zjmfgo-release-candidate-v1', source: proof,
    builder: { version: builderVersion, ...selectedDigest(proof.files, builderFiles) },
    templates: selectedDigest(proof.files, templateFiles), tools: { go: 'fixture-go', node: process.version, npm: 'fixture-npm' },
    locks: { goMod: proof.files.find(file => file.path === 'go.mod').sha256, goSum: proof.files.find(file => file.path === 'go.sum').sha256, web: proof.files.find(file => file.path === 'web/package-lock.json').sha256 },
    binaries: Object.fromEntries(['api', 'worker', 'migrate'].map(name => [name, `vcs.revision=${proof.commit}\nvcs.modified=false`])),
    migrations: proof.files.filter(file => file.path.startsWith('db/migrations/')),
    web: { apiInternalURL: 'http://127.0.0.1:8088', buildID: 'fixture-build-id', bundler: 'webpack' }, artifacts: await inventory(root),
  };
  const seal = async () => {
    const raw = JSON.stringify(manifest, null, 2) + '\n';
    await writeFile(path.join(root, 'manifest.json'), raw);
    return sha256(raw);
  };
  return { root, source, manifest, seal, digest: await seal() };
}

test('fixed clean Git proof rejects modified, untracked, protected and symlink source', async t => {
  const root = await repo(t);
  const before = await sourceProof(root, 'HEAD');
  await put(root, 'ignored', 'ignored fixture is never copied');
  assert.deepEqual(await sourceProof(root, 'HEAD'), before);
  await put(root, 'go.mod', 'modified');
  await assert.rejects(sourceProof(root, 'HEAD'), /dirty/);
  command(root, 'checkout', '--', 'go.mod');
  await put(root, 'untracked', 'untracked');
  await assert.rejects(sourceProof(root, 'HEAD'), /dirty/);
  await rm(path.join(root, 'untracked'));
  await put(root, '.env', 'fixture-only-secret'); command(root, 'add', '.env'); command(root, 'commit', '--quiet', '-m', 'unsafe fixture');
  await assert.rejects(sourceProof(root, 'HEAD'), /protected/);
  command(root, 'rm', '--quiet', '.env');
  await symlink('go.mod', path.join(root, 'linked-source')); command(root, 'add', '.'); command(root, 'commit', '--quiet', '-m', 'linked fixture');
  await assert.rejects(sourceProof(root, 'HEAD'), /Unsupported/);
});

test('builder refuses dirty source before creating destination or invoking build tools', async t => {
  const root = await repo(t);
  await put(root, 'go.mod', 'modified');
  const output = path.join(root, 'output', 'must-not-exist');
  await assert.rejects(buildCandidate({ source: root, revision: 'HEAD', output, dependencies: '/not-used', apiInternalURL: 'http://127.0.0.1:8088' }), /dirty/);
  await assert.rejects(readFile(path.join(output, 'manifest.json')), /ENOENT/);
});

test('verify actual fixture files, source and manifest anchor; detect corruption/missing/extra', async t => {
  const f = await candidate(t);
  assert.equal((await verifyCandidate(f.root, f.digest, f.source)).source.commit, f.manifest.source.commit);
  await put(f.root, 'web/.next/static/chunks/index.js', 'tampered');
  await assert.rejects(verifyCandidate(f.root, f.digest), /inventory mismatch/);
  await put(f.root, 'web/.next/static/chunks/index.js', 'fixture-client');
  await rm(path.join(f.root, 'bin/worker'));
  await assert.rejects(verifyCandidate(f.root, f.digest), /inventory mismatch/);
  await put(f.root, 'bin/worker', 'binary-fixture-not-executable-code', true);
  await put(f.root, 'unexpected', 'additional');
  await assert.rejects(verifyCandidate(f.root, f.digest), /inventory mismatch/);
  await rm(path.join(f.root, 'unexpected'));
  await put(f.root, 'manifest.json', '{}');
  await assert.rejects(verifyCandidate(f.root, f.digest), /Manifest digest mismatch/);
  await assert.rejects(verifyCandidate(f.root, undefined), /Trusted manifest/);
});

test('required-file, source checksum, BUILD_ID, rewrite and template checks are independent', async t => {
  for (const scenario of ['missing', 'migration', 'build-id', 'rewrite', 'template', 'lock', 'binary']) {
    await t.test(scenario, async t => {
      const f = await candidate(t);
      if (scenario === 'missing') await rm(path.join(f.root, 'bin/migrate'));
      if (scenario === 'migration') await put(f.root, 'migrations/001_fixture.sql', 'wrong SQL');
      if (scenario === 'build-id') f.manifest.web.buildID = 'wrong-id';
      if (scenario === 'rewrite') f.manifest.web.apiInternalURL = 'http://127.0.0.1:9999';
      if (scenario === 'template') f.manifest.templates.sha256 = '0'.repeat(64);
      if (scenario === 'lock') f.manifest.locks.goMod = '0'.repeat(64);
      if (scenario === 'binary') f.manifest.binaries.worker = 'vcs.modified=true';
      // Intentionally reseal our synthetic fixture to test semantic invariants,
      // rather than only exercising the trusted manifest hash gate.
      f.manifest.artifacts = (await inventory(f.root)).filter(file => file.path !== 'manifest.json');
      await assert.rejects(verifyCandidate(f.root, await f.seal()), /missing|mismatch|invalid/i);
    });
  }
});

test('path traversal, symlinked roots/parents/files and dependency escapes are refused', async t => {
  const f = await candidate(t);
  f.manifest.artifacts[0].path = '../escape';
  await assert.rejects(verifyCandidate(f.root, await f.seal()), /Unsafe/);
  const root = await temporary(t);
  await symlink(f.root, path.join(root, 'linked'));
  await assert.rejects(verifyCandidate(path.join(root, 'linked'), f.digest), /Symlink/);
  await symlink('/etc/passwd', path.join(f.root, 'outside'));
  await assert.rejects(inventory(f.root), /Symlink/);
  const deps = await temporary(t);
  await symlink('/etc/passwd', path.join(deps, 'escape'));
  await assert.rejects(inventory(deps, { dependencies: true }), /escapes/);
  await assert.rejects(prepareDestination(f.source, path.join(f.source, 'web/.next')), /Output/);
  await assert.rejects(prepareDestination(f.source, path.join(root, 'linked', 'child')), /Symlink/);
  await assert.rejects(prepareDestination(f.source, root), /EEXIST/);
});

test('API target has no credentials/query/path and is an explicit origin', () => {
  assert.equal(apiTarget('http://127.0.0.1:8088/'), 'http://127.0.0.1:8088');
  for (const value of ['http://user:secret@example.test', 'http://example.test/?key=secret', 'http://example.test/path', 'http://example.test/#token', 'file:///tmp/file']) assert.throws(() => apiTarget(value));
});
