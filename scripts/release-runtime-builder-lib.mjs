import { lstat, mkdir, readFile, readdir, realpath, statfs, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { spawn } from 'node:child_process';
import { selectStandaloneTraces, traceSelectionFormat, traceSelectionManifestPaths } from './release-runtime-trace-selection.mjs';
import { apiTarget, fileHash, inventory, noLinks, safeRelative, sha256 } from './release-candidate-lib.mjs';

export const budgetComponents = ['checkout', 'buildTemporary', 'standalone', 'sealed', 'runCopy', 'testDatabaseAndLogs'];
export const controlledPATH = '/usr/local/btgo/bin:/usr/local/bin:/usr/bin:/bin';
// Both Go work files and Node/Next os.tmpdir() allocations belong to the same
// filesystem/namespace as the candidate and its measured reserve.
export async function ownedBuildTemporaryEnvironment(root) {
  await noLinks(root);
  const scratch = path.join(root, '.scratch');
  await mkdir(scratch, { recursive: true, mode: 0o700 });
  await noLinks(scratch);
  const temporary = path.join(root, '.scratch/tmp');
  await mkdir(temporary, { recursive: true, mode: 0o700 });
  await noLinks(temporary);
  return { TMPDIR: temporary, GOTMPDIR: temporary };
}
const hashPattern = /^[a-f0-9]{64}$/;
export async function pinnedJSON(filename, expectedSHA256) {
  if (!hashPattern.test(expectedSHA256 ?? '')) throw new Error('BLOCKED: external JSON SHA256 anchor is required');
  await noLinks(filename);
  const raw = await readFile(filename);
  if (raw.length > 64 * 1024 * 1024 || sha256(raw) !== expectedSHA256) throw new Error('Pinned input digest mismatch');
  return { raw, value: JSON.parse(raw) };
}
export function validateBudget(budget, available) {
  if (budget.format !== 'zjmfgo-runtime-budget-v1' || typeof budget.approvedBy !== 'string' || !budget.approvedBy.trim()) throw new Error('BLOCKED: measured disk budget record required');
  for (const key of ['maxPeakBytes', 'minReserveBytes', 'minReserveInodes', 'maxBuildSeconds', ...budgetComponents.map(x => `components.${x}`)]) {
    const value = key.startsWith('components.') ? budget.components?.[key.slice(11)] : budget[key];
    if (!Number.isSafeInteger(value) || value <= 0) throw new Error(`BLOCKED: missing/invalid budget ${key}`);
  }
  const peak = budgetComponents.reduce((sum, key) => sum + budget.components[key], 0);
  if (!Number.isSafeInteger(peak) || peak > budget.maxPeakBytes) throw new Error('BLOCKED: peak components exceed recorded maximum');
  if (available.bytes < budget.maxPeakBytes + budget.minReserveBytes || available.inodes < budget.minReserveInodes) throw new Error('BLOCKED: insufficient disk bytes/inodes for recorded peak and reserve');
  return { available, approvedPeakBytes: budget.maxPeakBytes, componentSumBytes: peak };
}
export async function freeSpace(root) {
  const fs = await statfs(root, { bigint: true });
  return { bytes: Number(fs.bavail * fs.bsize), inodes: Number(fs.ffree) };
}
export async function guardReserve(root, budget, initialFreeBytes) {
  const available = await freeSpace(root);
  if (available.bytes < budget.minReserveBytes || available.inodes < budget.minReserveInodes || initialFreeBytes - available.bytes > budget.maxPeakBytes) throw new Error('BLOCKED: disk reserve/recorded peak crossed during build');
  return available;
}
// No shell, no inherited Node/Go options, bounded output and only our process
// group is stopped on time/disk failure. A failed command cannot fall back to npm.
export async function runControlled(command, args, { cwd, env, root, budget, initialFreeBytes, capture = false }) {
  await guardReserve(root, budget, initialFreeBytes);
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { cwd, env, detached: true, stdio: capture ? ['ignore', 'pipe', 'pipe'] : ['ignore', 'inherit', 'inherit'] });
    let output = '', failure, checking = false;
    const stop = error => { if (!failure) { failure = error; try { process.kill(-child.pid, 'SIGKILL'); } catch {} } };
    if (capture) for (const stream of [child.stdout, child.stderr]) stream.on('data', data => { output += data; if (output.length > 16 * 1024 * 1024) stop(new Error('Controlled command output limit exceeded')); });
    const timer = setInterval(async () => {
      if (checking) return;
      checking = true;
      try { await guardReserve(root, budget, initialFreeBytes); } catch (error) { stop(error); }
      checking = false;
    }, 250);
    const deadline = setTimeout(() => stop(new Error('Controlled build command timed out')), budget.maxBuildSeconds * 1000);
    child.once('error', error => { clearInterval(timer); clearTimeout(deadline); reject(error); });
    child.once('close', code => {
      clearInterval(timer); clearTimeout(deadline);
      // Successful tools must not leave background build children behind.
      try { process.kill(-child.pid, 'SIGKILL'); } catch {}
      if (failure || code !== 0) reject(failure ?? new Error(`Controlled command failed: ${path.basename(command)} (${code})`));
      else resolve(output.trim());
    });
  });
}
export async function validateDependencies(dependencies, provenance, lockSHA256, tools) {
  dependencies = await noLinks(path.resolve(dependencies));
  if (!(await lstat(dependencies)).isDirectory()) throw new Error('Dependency root must be a real directory');
  const install = provenance.installation;
  let registry;
  try { registry = new URL(install?.registry); } catch { throw new Error('BLOCKED: trusted registry origin required'); }
  if (registry.protocol !== 'https:' || registry.username || registry.password || registry.search || registry.hash) throw new Error('BLOCKED: trusted registry must not contain credentials/query/fragment');
  if (provenance.format !== 'zjmfgo-runtime-dependency-provenance-v1' || provenance.lockSHA256 !== lockSHA256 || install?.command !== 'npm ci' || !install.source?.trim() || !/^https:\/\//.test(install.registry ?? '') || install.integrityVerified !== true || !install.immutability?.trim()) throw new Error('BLOCKED: trusted locked installation provenance required');
  if (install.nodeVersion !== tools.node || install.npmVersion !== tools.npm || install.platform !== tools.platform) throw new Error('Dependency installation toolchain/platform mismatch');
  const files = await inventory(dependencies, { dependencies: true });
  const inputTreeSHA256 = sha256(JSON.stringify(files));
  if (inputTreeSHA256 !== provenance.inputTreeSHA256 || JSON.stringify(files) !== JSON.stringify(provenance.files)) throw new Error('Dependency provenance input tree mismatch');
  return { files, inputTreeSHA256, bytes: files.reduce((sum, file) => sum + (file.bytes ?? 0), 0) };
}
export function runtimeTarget(apiInternalURL, webOrigin) {
  const api = apiTarget(apiInternalURL), web = apiTarget(webOrigin);
  for (const origin of [api, web]) {
    const url = new URL(origin);
    if (url.protocol !== 'http:' || url.hostname !== '127.0.0.1' || !url.port || ['8088', '3088'].includes(url.port)) throw new Error('Isolated runtime target must use explicit independent IPv4 loopback ports (not 8088/3088)');
  }
  if (api === web) throw new Error('API and Web ports must differ');
  return { api, web };
}
export function makeLaunchDescription({ web, binaries, runtime, webOrigin, runtimeUser }) {
  const targets = runtimeTarget(web.apiInternalURL, webOrigin);
  if (!/^[a-z_][a-z0-9_-]*[$]?$/.test(runtimeUser ?? '') || runtimeUser === 'root') throw new Error('Ordinary runtime user is required');
  const keys = ['DATABASE_URL', 'MFA_ENCRYPTION_KEY', 'PURCHASE_FIELD_ENCRYPTION_KEY', 'PROVISION_ENCRYPTION_KEY'];
  return {
    version: 1, configVersion: 'zjmfgo-runtime-config-v1', runtimeUser,
    node: runtime.node.path, apiInternalURL: targets.api, webOrigin: targets.web, appRoot: web.appRoot, entry: web.entry,
    binaries: Object.fromEntries(Object.entries(binaries).map(([name, binary]) => [name, binary.path])),
    environment: { inherit: false, allowlist: keys, forbidden: ['NODE_PATH', 'NODE_OPTIONS', '__NEXT_PRIVATE_STANDALONE_CONFIG'], fixed: { NODE_ENV: 'production', HOSTNAME: '127.0.0.1', PORT: new URL(targets.web).port, HTTP_ADDR: new URL(targets.api).host, PUBLIC_ORIGIN: targets.web, PUBLIC_ORIGINS: targets.web, COOKIE_SECURE: 'false', PAYMENT_MOCK_ENABLED: 'false' } },
    config: { external: true, mode: '0600', required: keys, format: 'JSON object {version, values}; never dotenv/shell', owner: 'launch uid' },
    host: runtime.host, timeouts: { readySeconds: 30, workerSeconds: 30, stopSeconds: 15 },
    writablePaths: ['cache', 'tmp', 'state', 'logs'].map(name => ({ root: 'mutable', path: name })),
    releaseVerification: 'full-inventory-before-and-after',
    pathResolution: 'executable and cwd relative to release root; args passed literally, relative file arguments resolve from cwd; use resolveLaunchStep',
    writeAudit: 'pending isolated runtime acceptance; do not assume unobserved writable paths',
    steps: [
      { name: 'verify', requirement: 'trusted manifest anchor, clean source proof, run-copy full inventory, host and external config; no process started on failure' },
      { name: 'migrate', executable: binaries.migrate.path, cwd: 'artifact', repeat: 2, requirement: 'both exit 0; exact candidate migration checksums/schema before API' },
      { name: 'api', executable: binaries.api.path, cwd: 'artifact', readyURL: `${targets.api}/readyz`, requirement: 'bounded ready wait; readiness alone is not business acceptance' },
      { name: 'web', executable: runtime.node.path, args: [path.posix.relative(web.appRoot, web.entry)], cwd: web.appRoot, requirement: 'after API ready; real pages, chunks and proxy acceptance' },
      { name: 'worker', executable: binaries.worker.path, cwd: 'artifact', requirement: 'after API ready; isolated simulation resume business acceptance' },
      { name: 'stop', requirement: 'TERM only owned PIDs; bounded wait, own listeners gone; sealed full inventory unchanged' },
    ],
  };
}
// Resolve only against an independently verified runtime-copy root. Never use
// the invoking process cwd to resolve package files. Arguments retain normal
// child-process semantics: relative file arguments are relative to resolved cwd.
export async function resolveLaunchStep(releaseRoot, step) {
  const root = await noLinks(path.resolve(releaseRoot));
  if (!(await lstat(root)).isDirectory()) throw new Error('Release root must be a directory');
  const executable = await noLinks(path.join(root, safeRelative(step.executable)));
  const cwd = await noLinks(path.join(root, safeRelative(step.cwd)));
  const stat = await lstat(executable);
  if (!stat.isFile() || !(stat.mode & 0o111) || !(await lstat(cwd)).isDirectory()) throw new Error('Launch executable/cwd invalid');
  const args = step.args ?? [];
  if (!Array.isArray(args) || args.some(argument => typeof argument !== 'string' || /[\x00-\x1f\x7f]/.test(argument))) throw new Error('Launch arguments invalid');
  return { executable, args: [...args], cwd };
}
export async function controlledRuntimeEnvironment(description, configFile, { uid = process.getuid?.() } = {}) {
  await noLinks(configFile);
  const stat = await lstat(configFile);
  if (!stat.isFile() || (stat.mode & 0o777) !== 0o600 || stat.uid !== uid || stat.size > 65536) throw new Error('Controlled config permissions/owner/type invalid');
  let config;
  try { config = JSON.parse(await readFile(configFile, 'utf8')); } catch { throw new Error('Controlled config JSON invalid'); }
  if (config.version !== description.configVersion || !config.values || typeof config.values !== 'object' || Array.isArray(config.values)) throw new Error('Controlled config version/shape invalid');
  const allowed = new Set(description.environment.allowlist);
  for (const key of Object.keys(config.values)) if (!allowed.has(key) || typeof config.values[key] !== 'string' || !config.values[key] || config.values[key].includes('\0')) throw new Error('Controlled config contains invalid/unallowed key');
  for (const key of description.config.required) if (!config.values[key]) throw new Error(`Controlled config missing required key: ${key}`);
  for (const key of ['MFA_ENCRYPTION_KEY', 'PURCHASE_FIELD_ENCRYPTION_KEY', 'PROVISION_ENCRYPTION_KEY']) if (!/^[a-fA-F0-9]{64}$/.test(config.values[key] ?? '')) throw new Error(`Controlled config invalid key format: ${key}`);
  // Intentionally build an environment from nothing. No PATH, HOME, inherited
  // module options, provider credentials or external notification channels.
  return { ...config.values, ...description.environment.fixed };
}
export async function ordinaryFiles(root, prefix = '') {
  const result = [];
  for (const name of (await readdir(path.join(root, prefix))).sort()) {
    const relative = prefix ? `${prefix}/${name}` : name;
    safeRelative(relative);
    const stat = await lstat(path.join(root, relative));
    if (stat.isDirectory()) result.push(...await ordinaryFiles(root, relative));
    else if (stat.isFile()) result.push(relative);
    else throw new Error(`Unexpected link/special file: ${relative}`);
  }
  return result;
}
export function standaloneConfig(serverText) {
  // Next16 writes a one-line JSON literal. Parse data only, never eval generated JS.
  const match = serverText.match(/^const nextConfig = (\{[^\n]*\})\r?$/m);
  if (!match) throw new Error('Unrecognized standalone serialized configuration');
  const config = JSON.parse(match[1]);
  if (config.output !== 'standalone' || config.distDir !== './.next') throw new Error('Standalone effective output/distDir mismatch');
  return config;
}
export function validateRewrite(config, apiInternalURL) {
  const rewrites = Array.isArray(config.rewrites) ? config.rewrites : Object.values(config.rewrites ?? {}).flat();
  if (!rewrites.some(rule => rule.source === '/api/:path*' && rule.destination === `${apiInternalURL}/api/:path*`)) throw new Error('Effective standalone/API rewrite mismatch');
}
// While the checkout and readonly input mount still exist, resolve every nft
// relative path and compare the actual framework-selected standalone file.
export async function collectTraceEvidence({ checkout, standalone, metaRoot, source, dependencies }) {
  const buildRoot = path.join(checkout, 'web/.next');
  const allNames = (await ordinaryFiles(buildRoot)).filter(name => !name.startsWith('standalone/') && name.endsWith('.nft.json'));
  const manifests = {};
  for (const name of traceSelectionManifestPaths) manifests[name] = JSON.parse(await readFile(path.join(buildRoot, name), 'utf8'));
  const requiredFiles = JSON.parse(await readFile(path.join(buildRoot, 'required-server-files.json'), 'utf8')).files;
  const nextVersion = JSON.parse(await readFile(path.join(checkout, 'web/node_modules/next/package.json'), 'utf8')).version;
  const metadata = { format: traceSelectionFormat, nextVersion, manifests, requiredFiles, available: allNames.sort() };
  const selection = selectStandaloneTraces(metadata), names = selection.selected, omitted = [];
  await mkdir(metaRoot, { recursive: true });
  await writeFile(path.join(metaRoot, 'trace-selection.json'), JSON.stringify(metadata, null, 2) + '\n', { flag: 'wx' });
  for (const entry of selection.omitted) {
    const raw = await readFile(path.join(buildRoot, entry.name));
    const destination = path.join(metaRoot, 'traces', entry.name);
    await mkdir(path.dirname(destination), { recursive: true });
    await writeFile(destination, raw, { flag: 'wx' });
    omitted.push({ path: `artifact/meta/traces/${entry.name}`, sha256: sha256(raw), reason: entry.reason });
  }
  const sourceFiles = new Map(source.files.map(file => [file.path, file]));
  const depFiles = new Map(dependencies.files.map(file => [file.path, file]));
  const traces = [], selected = new Map();
  for (const name of names) {
    const filename = path.join(buildRoot, name), raw = await readFile(filename), trace = JSON.parse(raw);
    if (trace.version !== 1 || !Array.isArray(trace.files)) throw new Error('Unsupported trace schema');
    const entries = [];
    for (const input of trace.files) {
      if (typeof input !== 'string' || !input || input.includes('\\') || /[\x00-\x1f\x7f]/.test(input) || path.isAbsolute(input)) throw new Error('Invalid trace input');
      const absolute = path.resolve(path.dirname(filename), input);
      if (!absolute.startsWith(`${checkout}/`)) throw new Error('Trace input escapes declared checkout');
      const canonical = await realpath(absolute);
      if (!canonical.startsWith(`${checkout}/`)) throw new Error('Trace link escapes declared checkout');
      const stat = await lstat(absolute);
      if (!stat.isFile()) throw new Error('Trace directory/link/special entry forbidden; no recursive fallback');
      // Reject parent-component links as well; the dependency bind is a real dir.
      await noLinks(absolute);
      const relative = safeRelative(path.relative(checkout, absolute));
      if (relative.startsWith('web/.next/standalone/')) throw new Error('Trace references own standalone output');
      let kind, sourcePath;
      if (relative.startsWith('web/node_modules/')) { kind = 'dependency'; sourcePath = relative.slice(17); }
      else if (sourceFiles.has(relative)) { kind = 'source'; sourcePath = relative; }
      else if (relative.startsWith('web/.next/')) kind = 'generated';
      else throw new Error(`Unmapped trace input: ${relative}`);
      const digest = await fileHash(absolute);
      const record = kind === 'dependency' ? depFiles.get(sourcePath) : sourceFiles.get(sourcePath);
      if (kind !== 'generated' && record?.sha256 !== digest) throw new Error('Trace source/provenance digest mismatch');
      const destination = path.join(standalone, relative);
      await noLinks(destination);
      if (!(await lstat(destination)).isFile() || await fileHash(destination) !== digest) throw new Error('Trace selected output is missing/changed');
      const artifactPath = `artifact/web/${relative}`;
      entries.push({ input, artifactPath, kind, ...(sourcePath ? { sourcePath } : {}) });
      if (kind === 'dependency') selected.set(artifactPath, { path: artifactPath, sourcePath });
    }
    const tracePath = `artifact/meta/traces/${name}`;
    await mkdir(path.dirname(path.join(metaRoot, 'traces', name)), { recursive: true });
    await writeFile(path.join(metaRoot, 'traces', name), raw, { flag: 'wx' });
    traces.push({ path: tracePath, entries });
  }
  // Generated standalone must not smuggle in untraced packages. Every runtime
  // dependency is a normal selected file backed by its exact trusted input.
  for (const relative of await ordinaryFiles(standalone)) {
    if (!relative.startsWith('web/node_modules/')) continue;
    const sourcePath = relative.slice(17), artifactPath = `artifact/web/${relative}`;
    if (!selected.has(artifactPath) || await fileHash(path.join(standalone, relative)) !== depFiles.get(sourcePath)?.sha256) throw new Error(`Unmapped standalone dependency: ${relative}`);
  }
  return { selectionPath: 'artifact/meta/trace-selection.json', traces, omitted, files: [...selected.values()].sort((a, b) => a.path.localeCompare(b.path)) };
}
