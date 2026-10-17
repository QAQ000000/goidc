#!/usr/bin/env node
import { chmod, copyFile, lstat, mkdir, readFile, readdir, rename, rm, statfs, writeFile } from 'node:fs/promises';
import { execFileSync } from 'node:child_process';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { fileHash, inventory, noLinks, sha256 } from './release-candidate-lib.mjs';
import { pinnedJSON, runControlled, guardReserve } from './release-runtime-builder-lib.mjs';

const platform = `${process.platform}/${process.arch}`;
const registry = 'https://registry.npmjs.org';
const MiB = 1024 * 1024;
const hashPattern = /^[a-f0-9]{64}$/;
const gitEnvironment = { PATH: '/usr/bin:/bin', HOME: '/nonexistent', LANG: 'C.UTF-8', GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null' };
function sourceGit(root, args, encoding = 'utf8') {
  return execFileSync('/usr/bin/git', ['-c', 'core.fsmonitor=false', '-c', 'core.hooksPath=/dev/null', '-C', root, ...args], { env: gitEnvironment, encoding, timeout: 30000, maxBuffer: 16 * MiB, stdio: ['ignore', 'pipe', 'pipe'] });
}
export const defaultInstallBudget = Object.freeze({ cacheBytes: 150 * MiB, outputBytes: 700 * MiB, reserveBytes: 512 * MiB, maxSeconds: 1800 });

function integer(value, name) {
  if (!/^\d+$/.test(String(value ?? '')) || !Number.isSafeInteger(Number(value)) || Number(value) <= 0) throw new Error(`BLOCKED: invalid positive integer ${name}`);
  return Number(value);
}
export function validateRegistry(value = registry) {
  let url;
  try { url = new URL(value); } catch { throw new Error('BLOCKED: registry invalid'); }
  if (url.href !== `${registry}/`) throw new Error('BLOCKED: registry must be official HTTPS npm origin without credentials/query/fragment');
  return registry;
}
export function installBudget(input = {}) {
  const b = Object.fromEntries(Object.entries(defaultInstallBudget).map(([key, value]) => [key, integer(input[key] ?? value, key)]));
  b.maxPeakBytes = integer(input.maxPeakBytes ?? b.cacheBytes + b.outputBytes + 64 * MiB, 'maxPeakBytes');
  if (!Number.isSafeInteger(b.cacheBytes + b.outputBytes) || b.maxPeakBytes < b.cacheBytes + b.outputBytes) throw new Error('BLOCKED: peak must cover cache and output allowances');
  return b;
}
export async function availableSpace(root) {
  const fs = await statfs(root, { bigint: true });
  return { bytes: Number(fs.bavail * fs.bsize), inodes: Number(fs.ffree) };
}
async function ordinary(filename) {
  if (!path.isAbsolute(filename)) throw new Error('BLOCKED: input paths must be absolute');
  await noLinks(filename);
  if (!(await lstat(filename)).isFile()) throw new Error('BLOCKED: ordinary input file required');
}
async function jsonInput(filename, expectedSHA256) {
  try { return await pinnedJSON(filename, expectedSHA256); }
  catch (error) {
    if (error instanceof SyntaxError) throw new Error('BLOCKED: anchored JSON is malformed');
    throw error;
  }
}
async function treeBytes(root) {
  let bytes = 0;
  for (const name of await readdir(root)) {
    const filename = path.join(root, name), s = await lstat(filename);
    if (s.isDirectory()) bytes += await treeBytes(filename);
    else if (s.isFile()) bytes += s.size;
    else throw new Error('BLOCKED: special file in private installation scratch');
  }
  return bytes;
}
export async function cleanSourceInput(source, revision) {
  if (!path.isAbsolute(source)) throw new Error('BLOCKED: source must be absolute');
  const root = await noLinks(source);
  if (sourceGit(root, ['status', '--porcelain', '--untracked-files=all']).trim()) throw new Error('BLOCKED: source checkout is dirty');
  if (!/^[a-f0-9]{40}$/.test(revision ?? '') || sourceGit(root, ['rev-parse', 'HEAD']).trim() !== revision) throw new Error('BLOCKED: source HEAD must equal selected full commit');
  const files = {};
  for (const name of ['package.json', 'package-lock.json']) {
    const filename = path.join(root, 'web', name); await ordinary(filename);
    const content = await readFile(filename);
    const committed = sourceGit(root, ['show', `${revision}:web/${name}`], null);
    if (!content.equals(committed)) throw new Error('BLOCKED: source lock/package differs from fixed Git blob');
    files[name] = { filename, bytes: content.length, sha256: sha256(content) };
  }
  return { root, revision, files, lockSHA256: files['package-lock.json'].sha256 };
}
// Every non-root package must be a registry tarball with a strong npm integrity
// record. Local/git/link dependencies or credential-bearing URLs fail closed.
export function validateLock(lock) {
  if (lock.lockfileVersion !== 3 || !lock.packages || !lock.packages['']) throw new Error('BLOCKED: npm lockfile v3 required');
  let count = 0;
  for (const [name, entry] of Object.entries(lock.packages)) {
    if (name === '') continue;
    if (!name.startsWith('node_modules/') || entry.link || typeof entry.resolved !== 'string') throw new Error('BLOCKED: registry-only locked dependencies required');
    let url; try { url = new URL(entry.resolved); } catch { throw new Error('BLOCKED: invalid locked registry URL'); }
    if (url.origin !== registry || url.username || url.password || url.search || url.hash || url.protocol !== 'https:') throw new Error('BLOCKED: locked dependency origin is not credential-free official HTTPS registry');
    if (!/^sha512-[A-Za-z0-9+/]+={0,2}$/.test(entry.integrity ?? '')) throw new Error('BLOCKED: every locked tarball requires SHA512 integrity');
    count++;
  }
  return count;
}
export function installEnvironment(root) {
  // Construct from nothing: no proxy, NODE_OPTIONS, tokens, inherited npmrc,
  // shared cache or production provider credentials. Lifecycle scripts disabled.
  return { PATH: '/usr/bin:/bin', HOME: path.join(root, 'home'), TMPDIR: path.join(root, 'tmp'), LANG: 'C.UTF-8', CI: '1', NODE_ENV: 'development', NPM_CONFIG_USERCONFIG: path.join(root, 'user.npmrc'), NPM_CONFIG_GLOBALCONFIG: path.join(root, 'global.npmrc'), NPM_CONFIG_CACHE: path.join(root, 'cache'), NPM_CONFIG_REGISTRY: registry, NPM_CONFIG_AUDIT: 'false', NPM_CONFIG_FUND: 'false', NPM_CONFIG_IGNORE_SCRIPTS: 'true', NPM_CONFIG_UPDATE_NOTIFIER: 'false' };
}
export async function trustedToolchain(o) {
  for (const key of ['node', 'nodeLicense', 'npmCLI']) await ordinary(o[key]);
  if (!path.isAbsolute(o.npmRoot)) throw new Error('BLOCKED: npm root must be absolute');
  await noLinks(o.npmRoot);
  if (o.npmCLI !== path.join(o.npmRoot, 'bin/npm-cli.js')) throw new Error('BLOCKED: npm CLI must be fixed npm-root/bin/npm-cli.js');
  const nodeInput = await jsonInput(o.nodeProvenance, o.nodeProvenanceSHA256);
  const npmInput = await jsonInput(o.npmProvenance, o.npmProvenanceSHA256);
  const n = nodeInput.value, p = npmInput.value;
  // Validate anchors and complete tool bytes BEFORE invoking either executable.
  if (n.format !== 'zjmfgo-runtime-node-provenance-v1' || n.platform !== platform || typeof n.source !== 'string' || !n.source.trim() || !/^v\d+\.\d+\.\d+$/.test(n.version ?? '') || !hashPattern.test(n.sha256 ?? '') || !hashPattern.test(n.licenseSHA256 ?? '') || await fileHash(o.node) !== n.sha256 || await fileHash(o.nodeLicense) !== n.licenseSHA256 || !((await lstat(o.node)).mode & 0o111)) throw new Error('BLOCKED: anchored Node/license bytes mismatch');
  const files = await inventory(o.npmRoot, { dependencies: true });
  if (p.format !== 'zjmfgo-runtime-npm-provenance-v1' || p.platform !== platform || p.nodeVersion !== n.version || typeof p.source !== 'string' || !p.source.trim() || !/^\d+\.\d+\.\d+$/.test(p.version ?? '') || sha256(JSON.stringify(files)) !== p.inputTreeSHA256 || JSON.stringify(files) !== JSON.stringify(p.files)) throw new Error('BLOCKED: anchored complete npm tree mismatch');
  const env = { PATH: '/usr/bin:/bin', HOME: '/nonexistent', LANG: 'C.UTF-8', NPM_CONFIG_USERCONFIG: '/dev/null', NPM_CONFIG_GLOBALCONFIG: '/nonexistent/zjmfgo-global.npmrc', NPM_CONFIG_UPDATE_NOTIFIER: 'false' };
  function version(args) { try { return execFileSync(o.node, args, { env, encoding: 'utf8', timeout: 10000, maxBuffer: MiB, stdio: ['ignore', 'pipe', 'pipe'] }).trim(); } catch { throw new Error('BLOCKED: anchored tool version check failed'); } }
  if (version(['--version']) !== n.version || version([o.npmCLI, '--version']) !== p.version) throw new Error('BLOCKED: anchored Node/npm versions mismatch');
  return { node: o.node, npmCLI: o.npmCLI, nodeVersion: n.version, npmVersion: p.version, platform, nodeSHA256: n.sha256, licenseSHA256: n.licenseSHA256, npmTreeSHA256: p.inputTreeSHA256, nodeProvenanceSHA256: o.nodeProvenanceSHA256, npmProvenanceSHA256: o.npmProvenanceSHA256 };
}
async function unusedOutput(filename) {
  if (!path.isAbsolute(filename)) throw new Error('BLOCKED: output must be absolute');
  await noLinks(path.dirname(filename));
  try { await lstat(filename); } catch (error) { if (error.code === 'ENOENT') return; throw error; }
  throw new Error('BLOCKED: exclusive output already exists');
}
export async function preflight(o) {
  validateRegistry(o.registryURL);
  await unusedOutput(o.output);
  // Provenance always stays in the exclusive root; returned SHA must be retained
  // outside it by caller. Do not emit a misleading self-authenticating sidecar.
  const source = await cleanSourceInput(o.source, o.revision);
  let lock; try { lock = JSON.parse(await readFile(source.files['package-lock.json'].filename, 'utf8')); } catch { throw new Error('BLOCKED: fixed lock JSON malformed'); }
  const packages = validateLock(lock);
  const budget = installBudget(o.budget), available = await availableSpace(path.dirname(o.output));
  if (available.bytes < budget.maxPeakBytes + budget.reserveBytes || available.inodes < 10000) throw new Error('BLOCKED: insufficient disk bytes/inodes for install peak plus reserve');
  const tools = await trustedToolchain(o);
  return { status: 'PREFLIGHT_ONLY', source, packages, tools, registry, output: o.output, budget, available, args: [o.npmCLI, 'ci', '--registry', registry, '--include=dev', '--ignore-scripts', '--no-audit', '--no-fund'], installationExecuted: false };
}
async function treeMode(root, readonly) {
  const s = await lstat(root); if (s.isSymbolicLink()) return;
  if (readonly && s.isDirectory()) {
    for (const name of await readdir(root)) await treeMode(path.join(root, name), readonly);
  }
  await chmod(root, readonly ? s.mode & ~0o222 : s.mode | 0o700);
  if (!readonly && s.isDirectory()) for (const name of await readdir(root)) await treeMode(path.join(root, name), readonly);
}
export async function prepareDependencyInput(o) {
  const plan = await preflight(o);
  if (!o.execute) return plan;
  const root = o.output, budget = plan.budget;
  // mkdir itself is the exclusive ownership claim; pre-existing paths are never
  // removed, even if a concurrent creator wins between preflight and this call.
  await mkdir(root, { mode: 0o700 });
  const stat = await lstat(root), rootIdentity = { dev: stat.dev, ino: stat.ino };
  const controlledBudget = { maxBuildSeconds: budget.maxSeconds, maxPeakBytes: budget.maxPeakBytes, minReserveBytes: budget.reserveBytes, minReserveInodes: 10000 };
  try {
    for (const name of ['home', 'tmp', 'cache', 'install']) await mkdir(path.join(root, name), { mode: 0o700 });
    for (const name of ['user.npmrc', 'global.npmrc']) await writeFile(path.join(root, name), '', { flag: 'wx', mode: 0o600 });
    const install = path.join(root, 'install');
    for (const [name, file] of Object.entries(plan.source.files)) {
      await copyFile(file.filename, path.join(install, name));
      if (await fileHash(path.join(install, name)) !== file.sha256) throw new Error('BLOCKED: copied fixed source input changed');
    }
    const startedAt = new Date().toISOString();
    const log = await runControlled(o.node, plan.args, { cwd: install, env: installEnvironment(root), root, budget: controlledBudget, initialFreeBytes: plan.available.bytes, capture: true });
    // Log is a bounded capture from credential-free registry requests only;
    // never stream the caller environment or arbitrary npm error output.
    await writeFile(path.join(root, 'npm-ci.log'), log + '\n', { flag: 'wx', mode: 0o600 });
    const cacheBytes = await treeBytes(path.join(root, 'cache'));
    if (cacheBytes > budget.cacheBytes) throw new Error('BLOCKED: npm cache exceeds cache allowance');
    if (await fileHash(path.join(install, 'package-lock.json')) !== plan.source.lockSHA256 || await fileHash(path.join(install, 'package.json')) !== plan.source.files['package.json'].sha256) throw new Error('BLOCKED: npm modified fixed package/lock input');
    if (JSON.stringify(await cleanSourceInput(o.source, o.revision)) !== JSON.stringify(plan.source) || JSON.stringify(await trustedToolchain(o)) !== JSON.stringify(plan.tools)) throw new Error('BLOCKED: source/toolchain changed during installation');
    await rename(path.join(install, 'node_modules'), path.join(root, 'node_modules'));
    const dependencies = path.join(root, 'node_modules');
    const before = await inventory(dependencies, { dependencies: true });
    if (!before.length) throw new Error('BLOCKED: npm produced empty dependency tree');
    const dependencyBytes = before.reduce((sum, file) => sum + (file.bytes ?? 0), 0);
    if (dependencyBytes > budget.outputBytes) throw new Error('BLOCKED: installed tree exceeds output allowance');
    await treeMode(dependencies, true);
    const files = await inventory(dependencies, { dependencies: true });
    if (JSON.stringify(files) !== JSON.stringify(before)) throw new Error('BLOCKED: dependency tree changed during immutable sealing');
    // Remove only this invocation\'s scratch; retain locked inputs and real log.
    for (const name of ['home', 'tmp', 'cache', 'user.npmrc', 'global.npmrc']) await rm(path.join(root, name), { recursive: true, force: false });
    await guardReserve(root, controlledBudget, plan.available.bytes);
    const inputTreeSHA256 = sha256(JSON.stringify(files));
    const proof = { format: 'zjmfgo-runtime-dependency-provenance-v1', lockSHA256: plan.source.lockSHA256, inputTreeSHA256, installation: { command: 'npm ci', args: plan.args.slice(1), source: 'successful controlled npm ci from fixed clean Git package/lock blobs and independently anchored official Node/npm inputs', registry, integrityVerified: true, lifecycleScripts: 'disabled', nodeVersion: plan.tools.nodeVersion, npmVersion: plan.tools.npmVersion, platform, immutability: 'exclusive0700 output; node_modules files/directories have no write bits; complete inventory identical before/after sealing; later builder requires private read-only mount' }, files, evidence: { startedAt, completedAt: new Date().toISOString(), command: { executableSHA256: plan.tools.nodeSHA256, args: plan.args }, tools: plan.tools, sourceCommit: o.revision, packageJSONSHA256: plan.source.files['package.json'].sha256, lockSHA256: plan.source.lockSHA256, logPath: 'npm-ci.log', logSHA256: await fileHash(path.join(root, 'npm-ci.log')), budget, measured: { dependencyBytes, cacheBytes, inputFiles: files.length, finalAvailable: await availableSpace(root) } } };
    const provenance = path.join(root, 'dependencies.json');
    await writeFile(provenance, JSON.stringify(proof, null, 2) + '\n', { flag: 'wx', mode: 0o400 });
    return { status: 'INSTALLED', installationExecuted: true, dependencies, provenance, provenanceSHA256: await fileHash(provenance), inputTreeSHA256, fileCount: files.length, dependencyBytes, anchorInstruction: 'retain returned provenanceSHA256 independently outside exclusive output before use' };
  } catch (error) {
    const current = await lstat(root).catch(() => null);
    if (current && !current.isSymbolicLink() && current.dev === rootIdentity.dev && current.ino === rootIdentity.ino) { await treeMode(root, false); await rm(root, { recursive: true, force: true }); }
    throw error;
  }
}
export function parseArgs(args) {
  const options = {}, names = { '--source': 'source', '--commit': 'revision', '--output': 'output', '--node': 'node', '--node-license': 'nodeLicense', '--node-provenance': 'nodeProvenance', '--node-provenance-sha256': 'nodeProvenanceSHA256', '--npm-root': 'npmRoot', '--npm-cli': 'npmCLI', '--npm-provenance': 'npmProvenance', '--npm-provenance-sha256': 'npmProvenanceSHA256', '--registry': 'registryURL', '--cache-bytes': 'cacheBytes', '--output-bytes': 'outputBytes', '--reserve-bytes': 'reserveBytes', '--max-peak-bytes': 'maxPeakBytes', '--max-seconds': 'maxSeconds' };
  args = [...args]; if (args.at(-1) === '--execute') { options.execute = true; args.pop(); }
  for (let i = 0; i < args.length; i += 2) { if (!names[args[i]] || !args[i + 1] || options[names[args[i]]]) throw new Error('Expected explicit named inputs; --execute must be final'); options[names[args[i]]] = args[i + 1]; }
  if (Object.values(names).some(name => !options[name])) throw new Error(`Required options: ${Object.keys(names).join(' ')}`);
  options.budget = Object.fromEntries(['cacheBytes', 'outputBytes', 'reserveBytes', 'maxPeakBytes', 'maxSeconds'].map(name => [name, options[name]]));
  return options;
}
if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try { console.log(JSON.stringify(await prepareDependencyInput(parseArgs(process.argv.slice(2))), null, 2)); }
  catch (error) { console.error(`[dependency-input] ${error.message}`); process.exitCode = 1; }
}
