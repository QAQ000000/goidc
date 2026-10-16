#!/usr/bin/env node
// Opt-in outer controller only. The private scenario owns actual HTTP/switching.
import { chmod, chown, copyFile, lstat, mkdir, readFile, readdir, readlink, realpath, rm, writeFile } from 'node:fs/promises';
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { fileHash, git, noLinks, sha256, sourceProof } from './release-candidate-lib.mjs';
import { pinnedJSON, freeSpace, guardReserve, validateBudget, controlledRuntimeEnvironment } from './release-runtime-builder-lib.mjs';
import { runtimeInventory, verifyRuntimeCandidate } from './release-runtime-lib.mjs';
import { assertAvailablePort, assertSocket, auditTraceLine, jsonFile, mutableAudit, requireThat, validateTestDSN, waitFor, writeJSON } from './release-runtime-acceptance-lib.mjs';
import { copyIndependent, launch, stop, verifyHostBaseline } from './accept-release-runtime.mjs';
import { buildDualWebPlan, renderDualWebNginx } from './release-dual-web-lib.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const runnerRoot = path.resolve(here, '..');
const toolEnv = { PATH: '/usr/bin:/bin', LANG: 'C.UTF-8', GOTOOLCHAIN: 'local', GOENV: 'off', GOPROXY: 'off', GOSUMDB: 'off' };
const equal = (left, right) => JSON.stringify(left) === JSON.stringify(right);
const nginx = '/www/server/nginx/sbin/nginx';
const luaRoot = '/www/server/nginx/lib/lua';
const toolLibraries = ['/usr/local/lib/libluajit-5.1.so.2', '/usr/local/lib/libjemalloc.so.2'];
const maximumMilliseconds = 15 * 60 * 1000;
export const dualControllerFiles = [
  'scripts/accept-release-dual-web.mjs', 'scripts/release-dual-web-scenario.mjs',
  'scripts/accept-release-runtime.mjs', 'scripts/release-runtime-acceptance-lib.mjs',
  'scripts/release-runtime-lib.mjs', 'scripts/release-runtime-builder-lib.mjs',
  'scripts/release-runtime-trace-selection.mjs', 'scripts/release-candidate-lib.mjs',
  'scripts/release-dual-web-lib.mjs', 'scripts/release-static-retention-lib.mjs',
];
function command(binary, args, env = toolEnv) {
  const result = spawnSync(binary, args, { env, encoding: 'utf8', maxBuffer: 4 * 1024 * 1024, timeout: 10000 });
  requireThat(!result.error && result.status === 0, 'BLOCKED: required controlled host tool failed');
  return result.stdout.trim();
}
async function deadline(promise, milliseconds, message) {
  let timer;
  try { return await Promise.race([promise, new Promise((_, reject) => { timer = setTimeout(() => reject(new Error(message)), Math.max(1, milliseconds)); })]); }
  finally { clearTimeout(timer); }
}
export function parseDualWebOptions(argv) {
  const names = { '--old': 'old', '--next': 'next', '--old-source': 'oldSource', '--next-source': 'nextSource', '--old-manifest-sha256': 'oldSHA', '--next-manifest-sha256': 'nextSHA', '--runner-commit': 'runnerCommit', '--run-root': 'runRoot', '--pg-socket': 'pgSocket', '--budget': 'budget', '--budget-sha256': 'budgetSHA' };
  const args = [...argv], options = {};
  if (args.at(-1) === '--execute') { options.execute = true; args.pop(); }
  for (let index = 0; index < args.length; index += 2) {
    const name = Object.hasOwn(names, args[index]) && names[args[index]], value = args[index + 1];
    requireThat(name && value && !value.startsWith('--') && !Object.hasOwn(options, name), 'Explicit unique options required; --execute must be final');
    options[name] = value;
  }
  requireThat(Object.values(names).every(name => options[name]), `Required options: ${Object.keys(names).join(' ')} [--execute]`);
  requireThat(/^[a-f0-9]{40}$/.test(options.runnerCommit) && ['oldSHA', 'nextSHA', 'budgetSHA'].every(key => /^[a-f0-9]{64}$/.test(options[key])), 'Full independent commit/digest anchors required');
  requireThat(['old', 'next', 'oldSource', 'nextSource', 'runRoot', 'pgSocket', 'budget'].every(key => path.isAbsolute(options[key])), 'Absolute controlled input paths required');
  return options;
}
export function validateDualRunRoot(root, inputs) {
  root = path.resolve(root);
  requireThat(/^\/(?:tmp|dev\/shm)\/zjmfgo-dual-web-[A-Za-z0-9_-]+$/.test(root), 'New exclusive /tmp or /dev/shm dual Web run root required');
  for (const input of inputs) {
    const absolute = path.resolve(input);
    requireThat(root !== absolute && !root.startsWith(`${absolute}/`) && !absolute.startsWith(`${root}/`), 'Runtime output and immutable/private inputs must be disjoint');
  }
  return root;
}
export function dualNamespaceArgs({ releases, control, fixture, mutable, pgSocket, uid, gid, manifests, libraries = toolLibraries.map(filename => ({ path: filename, resolved: filename })) }) {
  requireThat(Number.isSafeInteger(uid) && uid > 0 && Number.isSafeInteger(gid) && gid > 0, 'Ordinary namespace identity required');
  const args = ['--die-with-parent', '--unshare-all', '--clearenv', '--proc', '/proc', '--dev', '/dev', '--dir', '/etc', '--dir', '/usr', '--dir', '/usr/bin', '--dir', '/tools'];
  for (const directory of ['/lib', '/lib64', '/usr/lib', '/usr/lib64']) args.push('--ro-bind', directory, directory);
  for (const file of ['/etc/passwd', '/etc/group', '/etc/nsswitch.conf', '/etc/hosts', '/etc/localtime', '/etc/ld.so.cache']) args.push('--ro-bind', file, file);
  for (const library of libraries) {
    requireThat(toolLibraries.includes(library.path) && library.resolved.startsWith('/usr/local/lib/'), 'Unexpected tool library mount');
    args.push('--ro-bind', library.resolved, library.path);
    if (library.resolved !== library.path) args.push('--ro-bind', library.resolved, library.resolved);
  }
  args.push('--ro-bind', luaRoot, luaRoot, '--ro-bind', nginx, '/tools/nginx', '--ro-bind', '/usr/bin/strace', '/usr/bin/strace');
  args.push('--ro-bind', control, '/control', '--bind', fixture, '/fixture', '--bind', mutable, '/mutable', '--bind', path.join(mutable, 'tmp'), '/tmp');
  for (const label of ['old', 'next']) {
    const target = label === 'old' ? '/releases/old' : '/releases/new';
    args.push('--ro-bind', releases[label], target, '--bind', path.join(mutable, label, 'cache'), `${target}/${manifests[label].web.appRoot}/.next/cache`);
  }
  args.push('--dir', '/pg', '--ro-bind', pgSocket, `/pg/${path.basename(pgSocket)}`, '--uid', String(uid), '--gid', String(gid), '--cap-drop', 'ALL', '--chdir', '/mutable', `/releases/new/${manifests.next.runtime.node.path}`, '/control/release-dual-web-scenario.mjs', '--inner');
  return args;
}
async function runnerProof(commit) {
  requireThat(/^[a-f0-9]{40}$/.test(commit ?? ''), 'Full runner commit required');
  const proof = await sourceProof(runnerRoot, commit);
  requireThat(git(runnerRoot, 'rev-parse', 'HEAD') === commit, 'Pinned runner HEAD mismatch');
  for (const filename of dualControllerFiles) {
    const record = proof.files.find(row => row.path === filename);
    requireThat(record && await fileHash(path.join(runnerRoot, filename)) === record.sha256, 'Runner transitive control module differs from clean fixed source');
  }
  return proof;
}
async function toolingProof() {
  await noLinks(nginx); await noLinks(luaRoot);
  const luaFiles = [];
  async function visit(directory) {
    for (const name of (await readdir(directory)).sort()) {
      const filename = path.join(directory, name), info = await lstat(filename);
      if (info.isDirectory()) await visit(filename);
      else { requireThat(info.isFile() && info.nlink === 1, 'Lua tooling must use ordinary independent files'); luaFiles.push({ path: filename, bytes: info.size, sha256: await fileHash(filename) }); }
    }
  }
  await visit(luaRoot);
  const libraries = [];
  for (const filename of toolLibraries) {
    const canonical = await noLinks(await realpath(filename));
    requireThat(canonical.startsWith('/usr/local/lib/') && (await lstat(canonical)).isFile(), 'Unexpected private Nginx library provider');
    libraries.push({ path: filename, resolved: canonical, sha256: await fileHash(canonical) });
  }
  const version = spawnSync(nginx, ['-V'], { env: toolEnv, encoding: 'utf8', maxBuffer: 65536, timeout: 10000 });
  requireThat(!version.error && version.status === 0, 'Nginx tooling version unavailable');
  const elf = command('/usr/bin/readelf', ['-h', '-l', '-d', nginx]);
  requireThat(/Class:\s+ELF64\b/.test(elf) && /Machine:\s+Advanced Micro Devices X86-64\b/.test(elf), 'Nginx ELF architecture mismatch');
  const cached = command('/usr/sbin/ldconfig', ['-p']).split('\n'), hostLibraries = [], visited = new Set();
  async function inspect(filename, library = false) {
    const canonical = await noLinks(await realpath(filename));
    if (visited.has(canonical)) return; visited.add(canonical);
    const metadata = command('/usr/bin/readelf', ['-l', '-d', canonical]);
    const needed = [...metadata.matchAll(/Shared library: \[([^\]]+)\]/g)].map(row => row[1]).sort();
    if (library) hostLibraries.push({ path: canonical, sha256: await fileHash(canonical), needed });
    const interpreter = metadata.match(/Requesting program interpreter: ([^\]]+)/)?.[1];
    if (interpreter) await inspect(interpreter, true);
    for (const name of needed) {
      requireThat(/^[A-Za-z0-9_.+\-]+$/.test(name), 'Unexpected tool ELF dependency name');
      const explicit = libraries.find(row => path.basename(row.path) === name);
      const candidates = explicit ? [explicit.resolved] : cached.filter(line => line.trim().startsWith(`${name} `) && line.includes('x86-64')).map(line => line.split('=>')[1]?.trim()).filter(Boolean);
      const providers = [...new Set(await Promise.all(candidates.map(provider => realpath(provider))))];
      requireThat(providers.length === 1 && ['/lib/', '/lib64/', '/usr/lib/', '/usr/lib64/', '/usr/local/lib/'].some(root => providers[0].startsWith(root)), 'Missing/ambiguous/unmounted tool ELF dependency');
      await inspect(providers[0], true);
    }
  }
  await inspect(nginx); await inspect('/usr/bin/strace');
  for (const row of libraries) await inspect(row.resolved, true);
  for (const row of luaFiles.filter(file => file.path.endsWith('.so'))) await inspect(row.path);
  hostLibraries.sort((a, b) => a.path.localeCompare(b.path, 'en'));
  return { nginxSHA256: await fileHash(nginx), nginxVersion: version.stderr.trim(), needed: [...elf.matchAll(/Shared library: \[([^\]]+)\]/g)].map(row => row[1]).sort(), loaderCacheSHA256: await fileHash('/etc/ld.so.cache'), straceSHA256: await fileHash('/usr/bin/strace'), libraries, luaFiles, hostLibraries };
}
export async function dualHostSnapshot() {
  const resident = [];
  for (const name of (await readdir('/proc')).filter(value => /^\d+$/.test(value))) {
    const exe = await readlink(`/proc/${name}/exe`).catch(() => '');
    const cwd = await readlink(`/proc/${name}/cwd`).catch(() => '');
    const cmdline = (await readFile(`/proc/${name}/cmdline`).catch(() => Buffer.alloc(0))).toString().split('\0').filter(Boolean);
    const project = exe.startsWith('/www/wwwroot/zjmfgo/bin/') || (cwd === '/www/wwwroot/zjmfgo/web' && cmdline[0]?.startsWith('next-server'));
    const server = exe === nginx || (path.basename(exe) === 'postgres' && cmdline.includes('-D'));
    if (!project && !server) continue;
    const raw = await readFile(`/proc/${name}/stat`, 'utf8').catch(() => null);
    requireThat(raw, 'Resident process changed during identity snapshot');
    resident.push({ pid: Number(name), exe, cwd, startTicks: raw.slice(raw.lastIndexOf(')') + 2).split(' ')[19] });
  }
  resident.sort((a, b) => a.pid - b.pid);
  return { mountNamespace: await readlink('/proc/self/ns/mnt'), netNamespace: await readlink('/proc/self/ns/net'), resident };
}
export async function preflightDualWeb(options) {
  requireThat(process.platform === 'linux' && process.arch === 'x64' && process.getuid?.() === 0, 'BLOCKED: Linux x64 root outer controller required');
  const inputs = { ...options };
  for (const key of ['old', 'next', 'oldSource', 'nextSource', 'pgSocket', 'budget']) inputs[key] = await noLinks(path.resolve(inputs[key]));
  const runner = await runnerProof(inputs.runnerCommit);
  const manifests = {}, descriptions = {}, hostBaseline = {};
  for (const label of ['old', 'next']) {
    manifests[label] = await verifyRuntimeCandidate(inputs[label], inputs[`${label}SHA`], inputs[`${label}Source`]);
    requireThat(git(inputs[`${label}Source`], 'rev-parse', 'HEAD') === manifests[label].source.commit, 'Fixed candidate source checkout HEAD mismatch');
    descriptions[label] = await jsonFile(path.join(inputs[label], manifests[label].launch.path));
    requireThat(descriptions[label].runtimeUser === 'www', 'Both packages must declare ordinary www runtime user');
    hostBaseline[label] = await verifyHostBaseline(manifests[label], inputs[label]);
    requireThat(await fileHash(process.execPath) === await fileHash(path.join(inputs[label], manifests[label].runtime.node.path)), 'Controller and both bundled Node executables must match');
  }
  requireThat(manifests.old.source.commit === manifests.next.source.commit, 'Preparation scope requires two builds of the same final source commit');
  for (const filename of ['internal/store/release_dual_web_fixture_test.go', 'internal/store/release_runtime_fixture_test.go']) {
    const input = manifests.next.source.files.find(row => row.path === filename);
    requireThat(input && await fileHash(path.join(inputs.nextSource, filename)) === input.sha256, 'Actual dual fixture source differs from selected candidate source');
  }
  const plan = buildDualWebPlan(Object.fromEntries(['old', 'next'].map(label => [label, { manifest: manifests[label], description: descriptions[label], manifestSHA256: inputs[`${label}SHA`], root: label === 'old' ? '/releases/old' : '/releases/new' }])));
  const account = command('/usr/bin/getent', ['passwd', 'www']).split(':'), uid = Number(account[2]), gid = Number(account[3]);
  requireThat(account[0] === 'www' && Number.isSafeInteger(uid) && uid > 0 && Number.isSafeInteger(gid) && gid > 0, 'Ordinary www uid/gid required');
  const databaseURL = process.env.ZJMFGO_TEST_DATABASE_URL, database = validateTestDSN(databaseURL);
  await assertSocket(inputs.pgSocket, database.port);
  const ports = [plan.old.api, plan.old.web, plan.next.api, plan.next.web, plan.publicOrigin].map(origin => Number(new URL(origin).port));
  requireThat(!ports.includes(database.port), 'PG bridge and application listeners must differ');
  for (const port of ports) await assertAvailablePort(port);
  const runRoot = validateDualRunRoot(inputs.runRoot, [inputs.old, inputs.next, inputs.oldSource, inputs.nextSource, inputs.budget, inputs.pgSocket, runnerRoot]);
  const parent = await noLinks(path.dirname(runRoot));
  requireThat(!(await lstat(runRoot).catch(error => { if (error.code === 'ENOENT') return null; throw error; })), 'Exclusive acceptance output already exists');
  const budget = (await pinnedJSON(inputs.budget, inputs.budgetSHA)).value;
  requireThat(budget.minReserveBytes >= 512 * 1024 * 1024 && budget.minReserveInodes >= 10000, 'Dual Web requires at least 512MiB and 10000 inode reserve');
  const available = await freeSpace(parent), budgetEvidence = validateBudget(budget, available);
  requireThat(Object.values(manifests).reduce((sum, manifest) => sum + manifest.artifacts.reduce((bytes, row) => bytes + (row.bytes ?? 0), 0), 0) <= budget.components.runCopy, 'Both independent runtime copies exceed recorded budget');
  const databaseRoot = path.dirname(inputs.pgSocket), databaseFree = await freeSpace(databaseRoot), databasePeak = budget.components.testDatabaseAndLogs + budget.components.buildTemporary;
  const databaseSharesRunFilesystem = (await lstat(parent)).dev === (await lstat(databaseRoot)).dev;
  requireThat(databaseFree.bytes >= databasePeak + budget.minReserveBytes && databaseFree.inodes >= budget.minReserveInodes, 'PG socket/tool-cache filesystem reserve insufficient');
  const tooling = await toolingProof(), hostBefore = await dualHostSnapshot();
  command('/usr/bin/unshare', ['--mount', '--propagation', 'private', '/usr/bin/true']);
  command('/usr/bin/setpriv', ['--reuid', String(uid), '--regid', String(gid), '--clear-groups', '--', '/usr/bin/bwrap', '--unshare-all', '--ro-bind', '/usr', '/usr', '--symlink', 'usr/lib', '/lib', '--symlink', 'usr/lib64', '/lib64', '--uid', String(uid), '--gid', String(gid), '--cap-drop', 'ALL', '--', '/usr/bin/true']);
  return { inputs, runner, manifests, descriptions, hostBaseline, plan, uid, gid, databaseURL, database, runRoot, budget, budgetEvidence, available, databaseRoot, databaseFree, databasePeak, databaseSharesRunFilesystem, tooling, hostBefore };
}
async function boundedUsage(root) {
  let bytes = 0, files = 0;
  async function visit(directory) {
    for (const name of await readdir(directory)) {
      const filename = path.join(directory, name), info = await lstat(filename).catch(error => { if (error.code === 'ENOENT') return null; throw error; });
      if (!info) continue;
      if (info.isDirectory()) await visit(filename);
      else { bytes += info.size; files++; }
    }
  }
  await visit(root); return { bytes, files };
}
async function ownDirectory(directory, uid, gid) { await mkdir(directory, { mode: 0o700 }); await chown(directory, uid, gid); }
export async function auditDualTraceFiles(root, manifests) {
  const files = (await readdir(root)).filter(name => /\.trace\.\d+$/.test(name)).sort();
  requireThat(files.length > 0, 'Actual dual Web syscall traces missing');
  const cacheRoots = ['old', 'next'].map(label => `/releases/${label === 'old' ? 'old' : 'new'}/${manifests[label].web.appRoot}/.next/cache`);
  const migrations = new Set(), serviceGroups = new Set(), evidence = []; let mutations = 0, network = 0;
  for (const name of files) {
    const filename = path.join(root, name), info = await lstat(filename);
    requireThat(info.isFile() && info.nlink === 1 && info.size <= 16 * 1024 * 1024, 'Dual runtime trace type/size invalid');
    const group = name.split('.trace.')[0];
    for (const line of (await readFile(filename, 'utf8')).split('\n')) {
      const checked = auditTraceLine(line, cacheRoots); mutations += Number(checked.mutation); network += Number(checked.network);
      for (const label of ['old', 'next']) {
        const release = label === 'old' ? 'old' : 'new';
        if ([`${label}-migrate-1`, `${label}-migrate-2`].includes(group) && line.includes(`execve("/releases/${release}/${manifests[label].binaries.migrate.path}"`) && /\)\s+= 0$/.test(line)) migrations.add(group);
        if (line.includes(`execve("/releases/${release}/${manifests[label].binaries.api.path}"`) && /\)\s+= 0$/.test(line)) serviceGroups.add(`${label}-api` === group ? group : '');
        if (line.includes(`execve("/releases/${release}/${manifests[label].runtime.node.path}"`) && /\)\s+= 0$/.test(line)) serviceGroups.add(`${label}-web` === group ? group : '');
      }
    }
    evidence.push({ path: name, bytes: info.size, sha256: await fileHash(filename) });
  }
  requireThat(['old-migrate-1', 'old-migrate-2', 'next-migrate-1', 'next-migrate-2'].every(name => migrations.has(name)), 'Four actual same-package dual migration executions must be audited');
  requireThat(['old-api', 'old-web', 'next-api', 'next-web'].every(name => serviceGroups.has(name)), 'Both actual API/Web executions must be audited');
  return { files: evidence, mutationCalls: mutations, networkCalls: network, migrationExecutions: [...migrations].sort(), auditedUndeclaredFileMutationAttempts: 0, nonLoopbackAttempts: 0, coverage: 'file/network syscall metadata only; no write payload buffers; both API/Web and four migrations' };
}
export async function acceptDualWeb(options) {
  const pre = await preflightDualWeb(options);
  const report = { status: 'FAIL', scope: 'isolated same-source dual Web preparation only; no production switch or schema upgrade', runnerCommit: pre.runner.commit, commit: pre.manifests.next.source.commit, oldManifestSHA256: pre.inputs.oldSHA, nextManifestSHA256: pre.inputs.nextSHA, plan: pre.plan, planSHA256: sha256(JSON.stringify(pre.plan)), tooling: pre.tooling, hostBaseline: pre.hostBaseline, budget: { ...pre.budgetEvidence, databaseSharesRunFilesystem: pre.databaseSharesRunFilesystem, databaseSamplingScope: 'Explicit socket parent filesystem; a separate PostgreSQL data filesystem needs its own external budget observer' }, hostBefore: pre.hostBefore, configHashes: {} };
  for (const phase of ['old', 'next', 'restored']) report.configHashes[phase] = sha256(renderDualWebNginx(pre.plan, { phase }));
  if (!options.execute) return { ...report, status: 'PREFLIGHT_ONLY', fullAcceptanceExecuted: false };
  const { runRoot, uid, gid, manifests, inputs } = pre;
  await mkdir(runRoot, { mode: 0o755 });
  const control = path.join(runRoot, 'control'), fixture = path.join(runRoot, 'fixture'), mutable = path.join(runRoot, 'mutable'), goTemporary = path.join(runRoot, 'go-tmp');
  const releases = { old: path.join(runRoot, 'old'), next: path.join(runRoot, 'next') };
  const started = Date.now(); let fixtureProcess, sandbox, timer, failure, monitoring = false, monitorTask, stopping;
  const stopSandbox = () => stopping ??= stop(sandbox).catch(error => { failure ??= error; });
  const interrupt = () => { failure ??= new Error('Dual Web controller interrupted'); if (sandbox) void stopSandbox(); };
  process.once('SIGTERM', interrupt); process.once('SIGINT', interrupt);
  const ensureBudget = async () => {
    requireThat(Date.now() - started < maximumMilliseconds, 'Dual Web 15-minute deadline exceeded');
    await guardReserve(runRoot, pre.budget, pre.available.bytes);
    if (!pre.databaseSharesRunFilesystem) await guardReserve(pre.databaseRoot, { ...pre.budget, maxPeakBytes: pre.databasePeak }, pre.databaseFree.bytes);
    if (await lstat(mutable).catch(() => null)) {
      const usage = await boundedUsage(mutable);
      requireThat(usage.bytes <= pre.budget.components.testDatabaseAndLogs && usage.files <= 4096, 'Mutable cache/log/evidence budget exceeded');
      report.observedMutablePeak = { bytes: Math.max(report.observedMutablePeak?.bytes ?? 0, usage.bytes), files: Math.max(report.observedMutablePeak?.files ?? 0, usage.files) };
    }
  };
  try {
    timer = setInterval(async () => {
      if (monitoring) return; monitoring = true;
      try { monitorTask = ensureBudget(); await monitorTask; } catch (error) { failure ??= error; if (sandbox) void stopSandbox(); }
      finally { monitoring = false; }
    }, 250);
    await mkdir(control, { mode: 0o755 }); await mkdir(fixture, { mode: 0o700 }); await ownDirectory(mutable, uid, gid); await mkdir(goTemporary, { mode: 0o700 });
    for (const name of ['tmp', 'logs', 'home', 'state', 'cache', 'client_body', 'proxy', 'fastcgi', 'uwsgi', 'scgi', 'empty']) await ownDirectory(path.join(mutable, name), uid, gid);
    for (const label of ['old', 'next']) {
      await copyIndependent(inputs[label], releases[label]);
      requireThat(equal(await runtimeInventory(releases[label]), manifests[label].artifacts), 'Independent dual runtime copy differs');
      await mkdir(path.join(releases[label], manifests[label].web.appRoot, '.next/cache'), { mode: 0o755, recursive: true });
      await ownDirectory(path.join(mutable, label), uid, gid);
      for (const name of ['cache', 'home', 'tmp', 'state', 'logs']) await ownDirectory(path.join(mutable, label, name), uid, gid);
    }
    for (const filename of dualControllerFiles) { const dest = path.join(control, path.basename(filename)); await copyFile(path.join(runnerRoot, filename), dest); await chmod(dest, 0o444); }
    for (const phase of ['old', 'next', 'restored']) {
      const conf = renderDualWebNginx(pre.plan, { phase });
      requireThat(sha256(conf) === report.configHashes[phase], 'Preflight Nginx config changed');
      await writeFile(path.join(control, `${phase}.conf`), conf, { mode: 0o444, flag: 'wx' });
    }
    await ensureBudget(); requireThat(!failure, 'Resource boundary failed before fixture launch');
    const goEnv = { ...toolEnv, HOME: '/root' };
    const fixtureEnv = { ...goEnv, TMPDIR: goTemporary, GOTMPDIR: goTemporary, GOCACHE: command('/usr/local/btgo/bin/go', ['env', 'GOCACHE'], goEnv), GOMODCACHE: command('/usr/local/btgo/bin/go', ['env', 'GOMODCACHE'], goEnv), ZJMFGO_RELEASE_DUAL_WEB_FIXTURE: '1', ZJMFGO_RELEASE_DUAL_WEB_FIXTURE_ALLOW_SCHEMA: '1', ZJMFGO_RELEASE_DUAL_WEB_FIXTURE_DIR: fixture, ZJMFGO_TEST_DATABASE_URL: pre.databaseURL };
    fixtureProcess = launch('/usr/local/btgo/bin/go', ['test', '-mod=readonly', '-run', '^TestReleaseDualWebFixture$', '-count=1', '-timeout=14m', './internal/store'], { cwd: inputs.nextSource, env: fixtureEnv });
    const ready = await waitFor(async () => { requireThat(!failure && !fixtureProcess.exited, 'Fixture exited/resource boundary failed before schema-ready'); return jsonFile(path.join(fixture, 'schema-ready.json')).catch(() => null); }, 120, 'dual fixture schema-ready');
    requireThat(ready.ok && ready.pgVersion >= 180000 && ready.pgVersion < 190000 && ready.database === pre.database.database && /^release_runtime_[a-f0-9]{24,64}$/.test(ready.schema), 'Dedicated PG18 fixture identity mismatch');
    await chown(fixture, uid, gid);
    for (const name of ['config.json', 'credentials.json']) {
      const filename = path.join(fixture, name); await noLinks(filename);
      const info = await lstat(filename); requireThat(info.isFile() && info.nlink === 1 && info.size <= 65536 && (info.mode & 0o777) === 0o600, 'Private fixture input type/mode mismatch');
      await chown(filename, uid, gid);
    }
    for (const label of ['old', 'next']) {
      const env = await controlledRuntimeEnvironment(pre.descriptions[label], path.join(fixture, 'config.json'), { uid });
      const database = validateTestDSN(env.DATABASE_URL, ready.schema);
      requireThat(equal(database, pre.database), 'Private runtime database identity mismatch');
    }
    const request = { uid, gid, plan: pre.plan, pgSocket: `/pg/${path.basename(inputs.pgSocket)}`, schema: ready.schema, nginxSHA256: pre.tooling.nginxSHA256, parentNetNamespace: pre.hostBefore.netNamespace, parentMountNamespace: pre.hostBefore.mountNamespace, hiddenSources: [...new Set([inputs.oldSource, inputs.nextSource, runnerRoot])], hostBaseline: pre.hostBaseline, configHashes: report.configHashes, tooling: pre.tooling, fixturePaths: { config: '/fixture/config.json', credentials: '/fixture/credentials.json' } };
    await writeJSON(path.join(control, 'request.json'), request); await chmod(path.join(control, 'request.json'), 0o444);
    const socketDirectory = path.join(runRoot, 'socket'); await mkdir(socketDirectory, { mode: 0o755 });
    const stagedSocket = path.join(socketDirectory, path.basename(inputs.pgSocket)); await writeFile(stagedSocket, '', { flag: 'wx', mode: 0o644 });
    const argv = dualNamespaceArgs({ releases, control, fixture, mutable, pgSocket: stagedSocket, uid, gid, manifests, libraries: pre.tooling.libraries });
    const mountRequest = path.join(control, 'mount-request.json');
    await writeJSON(mountRequest, { parentPID: process.pid, parentMountNamespace: pre.hostBefore.mountNamespace, originalSocket: inputs.pgSocket, stagedSocket, uid, gid, argv, hostIdentityOutput: path.join(mutable, 'host-identity.json') });
    requireThat(!failure, 'Resource boundary failed before namespace launch');
    sandbox = launch('/usr/bin/unshare', ['--mount', '--propagation', 'private', path.join(releases.next, manifests.next.runtime.node.path), path.join(control, 'accept-release-dual-web.mjs'), '--mount-worker', mountRequest]);
    await deadline(sandbox.done, maximumMilliseconds - (Date.now() - started), 'Dual Web execution deadline');
    requireThat(!failure && !sandbox.failed && sandbox.code === 0, 'Dual Web namespace failed/resource boundary crossed');
    report.runtime = await jsonFile(path.join(mutable, 'result.json'));
    requireThat(report.runtime.ok && report.runtime.allOwnedExited && report.runtime.listenersGone && report.runtime.fixtureCleaned, 'Inner dual Web execution/exit/fixture proof incomplete');
    report.hostOrdinaryIdentity = await jsonFile(path.join(mutable, 'host-identity.json'));
    requireThat(report.hostOrdinaryIdentity.uid === uid && report.hostOrdinaryIdentity.gid === gid && report.hostOrdinaryIdentity.groupsCleared, 'Actual ordinary host identity not proven');
    const done = await waitFor(() => jsonFile(path.join(fixture, 'done.json')).catch(() => null), 30, 'dual fixture successful cleanup');
    requireThat(done.ok && done.phase === 'cleaned' && done.schemaRemoved, 'Fixture successful cleanup not proven');
    await deadline(fixtureProcess.done, 30000, 'Fixture process exit deadline');
    requireThat(!fixtureProcess.failed && fixtureProcess.code === 0, 'Dual Go fixture failed');
    report.audit = await auditDualTraceFiles(path.join(mutable, 'logs'), manifests);
    report.mutable = await mutableAudit(mutable);
    await ensureBudget(); report.status = 'PASS';
  } catch (error) { failure ??= error; }
  finally {
    clearInterval(timer);
    await monitorTask?.catch(error => { failure ??= error; });
    process.removeListener('SIGTERM', interrupt); process.removeListener('SIGINT', interrupt);
    if (sandbox) await stopSandbox();
    if (fixtureProcess && !fixtureProcess.exited) {
      // Only after all application connections are gone may the fixture drop its schema.
      const commands = (await readdir(fixture).catch(() => [])).filter(name => /^command-\d+\.json$/.test(name));
      const sequence = Math.max(0, ...commands.map(name => Number(name.match(/\d+/)[0]))) + 1;
      await writeJSON(path.join(fixture, `command-${sequence}.json`), { operation: 'abort' }).catch(error => { failure ??= error; });
      await deadline(fixtureProcess.done, 30000, 'Fixture abort deadline').catch(error => { failure ??= error; });
    }
    if (fixtureProcess) await stop(fixtureProcess).catch(error => { failure ??= error; });
    const cleanup = await jsonFile(path.join(fixture, 'done.json')).catch(() => null);
    report.fixtureCleanup = cleanup && { ok: cleanup.ok, phase: cleanup.phase, schemaRemoved: cleanup.schemaRemoved };
    report.fixtureSchemaRemoved = cleanup?.schemaRemoved === true;
    if (fixtureProcess && !report.fixtureSchemaRemoved) failure ??= new Error('Owned schema cleanup not proven');
    report.sealedUnchanged = {}; report.copiesUnchanged = {};
    for (const label of ['old', 'next']) {
      try {
        requireThat(equal(await runtimeInventory(inputs[label]), manifests[label].artifacts) && await fileHash(path.join(inputs[label], 'manifest.json')) === inputs[`${label}SHA`], 'Original sealed input changed'); report.sealedUnchanged[label] = true;
        if (await lstat(releases[label]).catch(() => null)) { requireThat(equal(await runtimeInventory(releases[label]), manifests[label].artifacts) && await fileHash(path.join(releases[label], 'manifest.json')) === inputs[`${label}SHA`], 'Read-only dual release copy changed'); report.copiesUnchanged[label] = true; }
      } catch (error) { failure ??= error; }
    }
    try {
      requireThat(equal(await toolingProof(), pre.tooling), 'Pinned host Nginx/Lua tools changed'); report.toolingUnchanged = true;
      report.hostAfter = await dualHostSnapshot(); requireThat(equal(report.hostAfter, pre.hostBefore), 'Host namespace/resident process identity changed'); report.hostUnchanged = true;
      requireThat(equal(await sourceProof(runnerRoot, inputs.runnerCommit), pre.runner), 'Runner source changed');
      for (const label of ['old', 'next']) requireThat(equal(await sourceProof(inputs[`${label}Source`], manifests[label].source.commit), manifests[label].source), 'Candidate source changed');
    } catch (error) { failure ??= error; }
    report.runtime ??= await jsonFile(path.join(mutable, 'result.json')).catch(() => null);
    report.hostOrdinaryIdentity ??= await jsonFile(path.join(mutable, 'host-identity.json')).catch(() => null);
    // Config/credentials and run copies are always discarded; retain only bounded sanitized evidence.
    for (const name of ['config.json', 'credentials.json', 'config.json.tmp', 'credentials.json.tmp']) await rm(path.join(fixture, name), { force: true }).catch(error => { failure ??= error; });
    for (const directory of [releases.old, releases.next, control, goTemporary]) await rm(directory, { force: true, recursive: true }).catch(error => { failure ??= error; });
    if (failure) { report.status = 'FAIL'; report.reason = 'Controlled precondition, execution or cleanup failed; inspect sanitized evidence'; }
    await writeJSON(path.join(runRoot, 'report.json'), report);
  }
  requireThat(!failure, 'Dual Web acceptance failed; sanitized report retained under exclusive run root');
  return { status: 'ACCEPTED_PREPARATION_ONLY', report: path.join(runRoot, 'report.json'), runnerCommit: pre.runner.commit, commit: pre.manifests.next.source.commit, oldManifestSHA256: inputs.oldSHA, nextManifestSHA256: inputs.nextSHA };
}
export async function dualMountWorker(filename) {
  const request = await jsonFile(filename);
  requireThat(process.ppid === request.parentPID && await readlink('/proc/self/ns/mnt') !== request.parentMountNamespace && await readlink(`/proc/${process.ppid}/ns/mnt`) === request.parentMountNamespace, 'Refusing PG socket staging outside owned private mount namespace');
  requireThat(Number.isSafeInteger(request.uid) && request.uid > 0 && Number.isSafeInteger(request.gid) && request.gid > 0 && request.argv[request.argv.indexOf('--uid') + 1] === String(request.uid) && request.argv[request.argv.indexOf('--gid') + 1] === String(request.gid), 'Ordinary outer/inner identity binding mismatch');
  let child, mounted = false;
  try {
    command('/usr/bin/mount', ['--bind', request.originalSocket, request.stagedSocket]); mounted = true;
    command('/usr/bin/mount', ['-o', 'remount,bind,ro', request.stagedSocket]);
    child = launch('/usr/bin/setpriv', ['--reuid', String(request.uid), '--regid', String(request.gid), '--clear-groups', '--pdeathsig', 'KILL', '--', '/usr/bin/bwrap', ...request.argv], { detached: false });
    const identity = await waitFor(async () => {
      requireThat(!child.exited, 'Ordinary namespace wrapper exited before host identity proof');
      const status = await readFile(`/proc/${child.child.pid}/status`, 'utf8').catch(() => '');
      const uid = status.match(/^Uid:\s+(\d+)\s+(\d+)\s+(\d+)\s+(\d+)/m), gid = status.match(/^Gid:\s+(\d+)\s+(\d+)\s+(\d+)\s+(\d+)/m), groups = status.match(/^Groups:\s*([^\n]*)/m)?.[1]?.trim();
      if (!uid?.slice(1).every(value => Number(value) === request.uid) || !gid?.slice(1).every(value => Number(value) === request.gid) || groups !== '') return null;
      return { pid: child.child.pid, uid: request.uid, gid: request.gid, groupsCleared: true, evidence: 'all four host /proc Uid/Gid values after setpriv; no supplementary groups', mountNamespace: await readlink('/proc/self/ns/mnt') };
    }, 5, 'Host ordinary identity observation');
    await writeJSON(request.hostIdentityOutput, identity);
    await child.done; requireThat(!child.failed && child.code === 0, 'Private dual Web scenario failed');
  } finally {
    // Child shares this owned process group; the outer stop covers the entire tree.
    if (child && !child.exited) { try { child.child.kill('SIGTERM'); } catch {} await deadline(child.done, 15000, 'Namespace wrapper stop deadline'); }
    if (mounted) command('/usr/bin/umount', [request.stagedSocket]);
  }
}
if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try {
    if (process.argv[2] === '--mount-worker' && process.argv.length === 4) await dualMountWorker(process.argv[3]);
    else console.log(JSON.stringify(await acceptDualWeb(parseDualWebOptions(process.argv.slice(2))), null, 2));
  } catch { console.error('[release-dual-web] Controlled precondition, execution or cleanup failed; secret values are never logged'); process.exitCode = 1; }
}
