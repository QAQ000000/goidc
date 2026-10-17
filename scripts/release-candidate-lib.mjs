import { createHash } from 'node:crypto';
import { createReadStream } from 'node:fs';
import { lstat, readdir, readFile, realpath } from 'node:fs/promises';
import path from 'node:path';
import { execFileSync } from 'node:child_process';

export const builderVersion = 1;
export const builderFiles = ['scripts/build-release-candidate.mjs', 'scripts/verify-release-candidate.mjs', 'scripts/release-candidate-lib.mjs'];
export const templateFiles = ['web/next.config.ts', 'web/package.json', 'scripts/go.sh', 'scripts/check-web-build-runtime.mjs', 'deploy/systemd/zjmfgo-api.service', 'deploy/systemd/zjmfgo-worker.service', 'deploy/systemd/zjmfgo-web.service'];
export const sha256 = data => createHash('sha256').update(data).digest('hex');
export const git = (repo, ...args) => execFileSync('git', ['-C', repo, ...args], { encoding: 'utf8', maxBuffer: 32 * 1024 * 1024 }).trim();
export function safeRelative(name) {
  if (typeof name !== 'string' || !name || name.includes('\\') || /[\x00-\x1f\x7f]/.test(name) || path.posix.isAbsolute(name) || name.split('/').some(x => !x || x === '.' || x === '..')) throw new Error(`Unsafe relative path: ${name}`);
  return name;
}
export function protectedPath(name) {
  return name.split('/').some(part => /^\.env(?:\.|$)/i.test(part) || /\.(?:pem|key|p12|pfx)$/i.test(part) || ['node_modules', '.git'].includes(part));
}
export async function fileHash(filename) {
  const hash = createHash('sha256');
  for await (const chunk of createReadStream(filename)) hash.update(chunk);
  return hash.digest('hex');
}
// Artifacts never follow links, including a symlinked root or parent component.
export async function noLinks(filename) {
  const absolute = path.resolve(filename);
  let current = path.parse(absolute).root;
  for (const part of absolute.slice(current.length).split(path.sep).filter(Boolean)) {
    current = path.join(current, part);
    if ((await lstat(current)).isSymbolicLink()) throw new Error(`Symlink is forbidden: ${current}`);
  }
  return absolute;
}
export async function inventory(root, { dependencies = false } = {}) {
  await noLinks(root);
  const files = [];
  async function visit(relative) {
    const directory = path.join(root, relative);
    for (const name of (await readdir(directory)).sort()) {
      const rel = relative ? `${relative}/${name}` : name;
      safeRelative(rel);
      const filename = path.join(root, rel);
      const stat = await lstat(filename);
      if (stat.isSymbolicLink()) {
        if (!dependencies) throw new Error(`Symlink is forbidden: ${rel}`);
        const target = await realpath(filename);
        if (!target.startsWith(`${root}${path.sep}`)) throw new Error(`Dependency link escapes tree: ${rel}`);
        files.push({ path: rel, link: path.relative(root, target) });
      } else if (stat.isDirectory()) await visit(rel);
      else if (stat.isFile()) {
        if (!dependencies && protectedPath(rel)) throw new Error(`Protected file in artifact: ${rel}`);
        files.push({ path: rel, bytes: stat.size, sha256: await fileHash(filename), executable: Boolean(stat.mode & 0o111) });
      } else throw new Error(`Special file is forbidden: ${rel}`);
    }
  }
  await visit('');
  return files;
}
export async function sourceProof(repo, revision) {
  const commit = git(repo, 'rev-parse', '--verify', `${revision}^{commit}`);
  if (!/^[a-f0-9]{40}$/.test(commit)) throw new Error('Expected full Git commit');
  // Copy ignored source-side .env into neither checkout nor build. All tracked
  // modifications and untracked nonignored files make invocation fail closed.
  if (git(repo, 'status', '--porcelain', '--untracked-files=all')) throw new Error('Source checkout is dirty');
  const records = execFileSync('git', ['-C', repo, 'ls-tree', '-r', '-z', commit], { encoding: 'utf8', maxBuffer: 32 * 1024 * 1024 }).split('\0').filter(Boolean);
  const files = records.map(record => {
    const divider = record.indexOf('\t');
    const header = record.slice(0, divider), filename = record.slice(divider + 1);
    safeRelative(filename);
    const [mode, kind, oid] = header.split(' ');
    // The tracked root .env.example is a source template only; it is never
    // copied into the candidate and Next does not load it as configuration.
    if (kind !== 'blob' || !['100644', '100755'].includes(mode) || (protectedPath(filename) && filename !== '.env.example')) throw new Error(`Unsupported or protected source: ${filename}`);
    const content = execFileSync('git', ['-C', repo, 'cat-file', 'blob', oid], { maxBuffer: 32 * 1024 * 1024 });
    return { path: filename, sha256: sha256(content), bytes: content.length };
  });
  return { commit, tree: git(repo, 'rev-parse', `${commit}^{tree}`), files };
}
export function selectedDigest(files, names) {
  const selected = names.map(name => {
    const found = files.find(file => file.path === name);
    if (!found) throw new Error(`Required source missing: ${name}`);
    return found;
  });
  return { files: selected, sha256: sha256(JSON.stringify(selected)) };
}
export function apiTarget(value) {
  const url = new URL(value);
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash || url.pathname !== '/') throw new Error('API target must be an HTTP(S) origin without credentials/path/query');
  return url.origin;
}
export async function verifyCandidate(root, expectedManifestSHA, sourceRepo) {
  if (!/^[a-f0-9]{64}$/.test(expectedManifestSHA ?? '')) throw new Error('Trusted manifest SHA256 is required');
  await noLinks(root);
  await noLinks(path.join(root, 'manifest.json'));
  const raw = await readFile(path.join(root, 'manifest.json'));
  if (sha256(raw) !== expectedManifestSHA) throw new Error('Manifest digest mismatch');
  const manifest = JSON.parse(raw);
  if (manifest.format !== 'zjmfgo-release-candidate-v1' || manifest.builder.version !== builderVersion || !/^[a-f0-9]{40}$/.test(manifest.source.commit)) throw new Error('Unsupported manifest');
  if (!['go', 'node', 'npm'].every(name => typeof manifest.tools?.[name] === 'string' && manifest.tools[name])) throw new Error('Toolchain version missing');
  const actual = (await inventory(root)).filter(file => file.path !== 'manifest.json');
  for (const file of manifest.artifacts) safeRelative(file.path);
  if (JSON.stringify(actual) !== JSON.stringify(manifest.artifacts)) throw new Error('Artifact inventory mismatch (missing, added or altered file)');
  const byPath = new Map(actual.map(file => [file.path, file]));
  for (const name of ['bin/api', 'bin/worker', 'bin/migrate', 'web/.next/BUILD_ID', 'web/.next/routes-manifest.json', 'web/.next/required-server-files.json', 'web/.next/server/pages-manifest.json', 'web/package.json', 'web/package-lock.json', 'web/next.config.ts', ...templateFiles.filter(file => file.startsWith('deploy/'))]) {
    if (!byPath.get(name)?.bytes) throw new Error(`Required artifact missing or empty: ${name}`);
  }
  if (!actual.some(file => file.path.startsWith('web/.next/static/')) || !actual.some(file => file.path.startsWith('web/.next/server/app/'))) throw new Error('Web production output is incomplete');
  for (const name of ['api', 'worker', 'migrate']) {
    if (!byPath.get(`bin/${name}`).executable || !manifest.binaries[name]?.includes(`vcs.revision=${manifest.source.commit}`) || !manifest.binaries[name]?.includes('vcs.modified=false')) throw new Error(`Binary provenance missing: ${name}`);
  }
  const buildID = (await readFile(path.join(root, 'web/.next/BUILD_ID'), 'utf8')).trim();
  if (!buildID || buildID !== manifest.web.buildID || manifest.web.bundler !== 'webpack') throw new Error('BUILD_ID or bundler mismatch');
  const target = apiTarget(manifest.web.apiInternalURL);
  const routes = JSON.parse(await readFile(path.join(root, 'web/.next/routes-manifest.json'), 'utf8'));
  const rewrites = Array.isArray(routes.rewrites) ? routes.rewrites : Object.values(routes.rewrites ?? {}).flat();
  if (!rewrites.some(rule => rule.source === '/api/:path*' && rule.destination === `${target}/api/:path*`)) throw new Error('API rewrite mismatch');
  for (const group of ['builder', 'templates']) {
    const digest = selectedDigest(manifest.source.files, group === 'builder' ? builderFiles : templateFiles);
    if (digest.sha256 !== manifest[group].sha256) throw new Error(`${group} digest mismatch`);
  }
  for (const file of actual.filter(file => file.path.startsWith('migrations/') || file.path.startsWith('deploy/') || ['web/package.json', 'web/package-lock.json', 'web/next.config.ts'].includes(file.path))) {
    const originalPath = file.path.startsWith('migrations/') ? `db/${file.path}` : file.path;
    if (manifest.source.files.find(source => source.path === originalPath)?.sha256 !== file.sha256) throw new Error(`Source artifact mismatch: ${file.path}`);
  }
  const migrationSources = manifest.source.files.filter(file => /^db\/migrations\/\d.*\.sql$/.test(file.path));
  if (!migrationSources.length || JSON.stringify(manifest.migrations) !== JSON.stringify(migrationSources)) throw new Error('Migration checksum list mismatch');
  for (const file of migrationSources) if (!byPath.has(file.path.slice(3))) throw new Error(`Migration missing: ${file.path}`);
  for (const file of manifest.source.files.filter(file => file.path.startsWith('web/public/'))) {
    if (byPath.get(file.path)?.sha256 !== file.sha256) throw new Error(`Public source artifact mismatch: ${file.path}`);
  }
  if (manifest.locks.goMod !== manifest.source.files.find(file => file.path === 'go.mod')?.sha256 || manifest.locks.goSum !== manifest.source.files.find(file => file.path === 'go.sum')?.sha256 || manifest.locks.web !== manifest.source.files.find(file => file.path === 'web/package-lock.json')?.sha256) throw new Error('Lockfile digest mismatch');
  if (sourceRepo) {
    const source = await sourceProof(sourceRepo, manifest.source.commit);
    if (JSON.stringify(source) !== JSON.stringify(manifest.source)) throw new Error('Trusted source proof mismatch');
  }
  return manifest;
}
