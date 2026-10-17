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
import { copyIndependent, launch, liveProcessGroup, stop, verifyHostBaseline } from './accept-release-runtime.mjs';
import { buildDualWebPlan, renderDualWebNginx } from './release-dual-web-lib.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const runnerRoot = path.resolve(here, '..');
const toolEnv = { PATH: '/usr/bin:/bin', LANG: 'C.UTF-8', GOTOOLCHAIN: 'local', GOENV: 'off', GOPROXY: 'off', GOSUMDB: 'off' };
const equal = (left, right) => JSON.stringify(left) === JSON.stringify(right);
const nginx = '/www/server/nginx/sbin/nginx';
const luaRoot = '/www/server/nginx/lib/lua';
const toolLibraries = ['/usr/local/lib/libluajit-5.1.so.2', '/usr/local/lib/libjemalloc.so.2'];
const maximumMilliseconds = 15 * 60 * 1000;
export const dualFixtureActiveMilliseconds = 10 * 60 * 1000;
const cleanupWindowMilliseconds = 90 * 1000;
const psql = '/www/server/pgsql/bin/psql';
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
export async function waitDualScenario({ sandbox, fixture, fixtureDirectory, milliseconds }) {
  const unexpectedFixtureExit = fixture.done.then(async () => {
    const done = await jsonFile(path.join(fixtureDirectory, 'done.json')).catch(() => null);
    requireThat(!fixture.failed && fixture.code === 0 && done?.ok === true && done.phase === 'cleaned' && done.schemaRemoved === true, 'Fixture exited before controlled successful cleanup');
    // A normal cleanup exits before the inner controller writes its final
    // report. Continue observing the sandbox rather than treating that as FAIL.
    return sandbox.done;
  });
  await deadline(Promise.race([sandbox.done, unexpectedFixtureExit]), milliseconds, 'Fixture-safe scenario deadline exceeded');
  requireThat(!fixture.exited || (!fixture.failed && fixture.code === 0), 'Fixture failed during sandbox shutdown');
}
export async function stopDualOwned(handle, { gracefulSeconds = 15, killedSeconds = 5, stopHandle = stop, groupMembers = liveProcessGroup } = {}) {
  if (!handle) return { groupGone: true, forced: false };
  let stopFailure;
  try { await stopHandle(handle, gracefulSeconds); } catch (error) { stopFailure = error; }
  await waitFor(async () => handle.exited && !(await groupMembers(handle.child.pid)).length, killedSeconds, 'owned process group disappearance after TERM/KILL');
  handle.groupGone = true;
  return { groupGone: true, forced: Boolean(stopFailure) };
}
async function namespaceMembers(isolation) {
  const namespace = isolation?.pidNamespace ?? isolation?.namespaces?.pid;
  requireThat(typeof namespace === 'string' && /^pid:\[\d+\]$/.test(namespace) && namespace !== await readlink('/proc/self/ns/pid'), 'Owned private PID namespace proof missing');
  const members = [];
  for (const name of (await readdir('/proc')).filter(value => /^\d+$/.test(value))) {
    const actual = await readlink(`/proc/${name}/ns/pid`).catch(() => null);
    if (actual !== namespace) continue;
    const raw = await readFile(`/proc/${name}/stat`, 'utf8').catch(() => null);
    if (raw && !['Z', 'X'].includes(raw.slice(raw.lastIndexOf(')') + 2).split(' ')[0])) members.push(Number(name));
  }
  return members;
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
export function pgStorageSettings(raw, expected) {
  let settings; try { settings = JSON.parse(raw); } catch { throw new Error('Controlled PostgreSQL settings response invalid'); }
  requireThat(settings.database === expected.database && settings.port === expected.port && settings.address === '127.0.0.1' && settings.version >= 180000 && settings.version < 190000, 'PostgreSQL storage query target identity mismatch');
  requireThat(typeof settings.dataDirectory === 'string' && path.isAbsolute(settings.dataDirectory) && typeof settings.socketDirectories === 'string' && typeof settings.logDirectory === 'string' && ['on', 'off'].includes(settings.loggingCollector) && settings.logDestination === 'stderr', 'Unsupported PostgreSQL data/log storage configuration');
  requireThat(settings.defaultTablespace === '' && settings.tempTablespaces === '' && settings.customTablespaces === 0, 'Unbudgeted PostgreSQL tablespaces are not supported');
  return settings;
}
async function databaseStorageProof(databaseURL, database, socket) {
  const parsed = new URL(databaseURL);
  await noLinks(psql);
  const query = "SELECT json_build_object('database',current_database(),'port',current_setting('port')::int,'address',host(inet_server_addr()),'version',current_setting('server_version_num')::int,'dataDirectory',current_setting('data_directory'),'socketDirectories',current_setting('unix_socket_directories'),'logDirectory',current_setting('log_directory'),'logDestination',current_setting('log_destination'),'loggingCollector',current_setting('logging_collector'),'defaultTablespace',current_setting('default_tablespace'),'tempTablespaces',current_setting('temp_tablespaces'),'customTablespaces',(SELECT count(*)::int FROM pg_tablespace WHERE spcname NOT IN ('pg_default','pg_global')))::text";
  // Credentials never enter argv or inherited PG variables. This is a single
  // readonly identity/settings query; all diagnostics are suppressed.
  const raw = command(psql, ['-X', '--no-password', '-A', '-t', '-v', 'ON_ERROR_STOP=1', '-c', query], { ...toolEnv, PGHOST: '127.0.0.1', PGPORT: String(database.port), PGDATABASE: database.database, PGUSER: decodeURIComponent(parsed.username), PGPASSWORD: decodeURIComponent(parsed.password), PGCONNECT_TIMEOUT: '5', PGSSLMODE: 'disable', PGAPPNAME: 'release_dual_storage_preflight', PGOPTIONS: '-c default_transaction_read_only=on -c statement_timeout=5000' });
  const settings = pgStorageSettings(raw, database);
  const data = await noLinks(await realpath(settings.dataDirectory));
  const pidLines = (await readFile(path.join(data, 'postmaster.pid'), 'utf8')).trim().split('\n');
  const pid = Number(pidLines[0]);
  requireThat(Number.isSafeInteger(pid) && pid > 1 && Number(pidLines[3]) === database.port && await realpath(pidLines[1]) === data, 'PostgreSQL postmaster identity/port mismatch');
  const exe = await readlink(`/proc/${pid}/exe`), argv = (await readFile(`/proc/${pid}/cmdline`)).toString().split('\0').filter(Boolean);
  requireThat(path.basename(exe) === 'postgres' && argv.includes('-D') && await realpath(argv[argv.indexOf('-D') + 1]) === data, 'PostgreSQL postmaster executable/data identity mismatch');
  const socketDirectory = await realpath(path.dirname(socket));
  const configuredSockets = await Promise.all(settings.socketDirectories.split(',').map(value => value.trim()).filter(value => value.startsWith('/')).map(value => realpath(value)));
  requireThat(configuredSockets.includes(socketDirectory) && Number((await readFile(`${socket}.lock`, 'utf8')).split('\n')[0]) === pid, 'PostgreSQL queried socket/postmaster binding mismatch');
  const rawStat = await readFile(`/proc/${pid}/stat`, 'utf8');
  const roots = [{ role: 'data', path: data }, { role: 'wal', path: await noLinks(await realpath(path.join(data, 'pg_wal'))) }];
  if (settings.loggingCollector === 'on') {
    const configuredLog = path.isAbsolute(settings.logDirectory) ? settings.logDirectory : path.join(data, settings.logDirectory);
    roots.push({ role: 'log', path: await noLinks(await realpath(configuredLog)) });
  } else {
    // Without the collector, stderr/stdout may still write a supervisor-owned
    // file. Refuse pipes/sockets/device sinks we cannot attribute to a disk.
    for (const fd of [1, 2]) {
      const target = await readlink(`/proc/${pid}/fd/${fd}`);
      requireThat(path.isAbsolute(target) && !target.endsWith(' (deleted)'), 'PostgreSQL non-collector logging target cannot be budgeted');
      const filename = await noLinks(await realpath(target)), info = await lstat(filename);
      requireThat(info.isFile(), 'PostgreSQL logging descriptor must identify an ordinary disk file');
      roots.push({ role: `fd${fd}-log`, path: path.dirname(filename), file: filename });
    }
  }
  for (const root of roots) requireThat((await lstat(root.path)).isDirectory(), 'PostgreSQL data/WAL/log budget target must be a directory');
  return { settings, pid, executable: exe, executableSHA256: await fileHash(exe), startTicks: rawStat.slice(rawStat.lastIndexOf(')') + 2).split(' ')[19], psqlSHA256: await fileHash(psql), socketDirectory, roots };
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
  const databaseStorage = await databaseStorageProof(databaseURL, database, inputs.pgSocket), databasePeak = budget.components.testDatabaseAndLogs;
  const runDevice = (await lstat(parent)).dev, databaseFilesystems = [];
  for (const root of databaseStorage.roots) {
    const dev = (await lstat(root.path)).dev;
    if (databaseFilesystems.some(row => row.dev === dev)) continue;
    const available = await freeSpace(root.path);
    requireThat(available.bytes >= (dev === runDevice ? budget.maxPeakBytes : databasePeak) + budget.minReserveBytes && available.inodes >= budget.minReserveInodes, 'Actual PostgreSQL data/WAL/log filesystem reserve insufficient');
    databaseFilesystems.push({ root: root.path, dev, sharesRunFilesystem: dev === runDevice, available });
  }
  const tooling = await toolingProof(), hostBefore = await dualHostSnapshot();
  command('/usr/bin/unshare', ['--mount', '--propagation', 'private', '/usr/bin/true']);
  command('/usr/bin/setpriv', ['--reuid', String(uid), '--regid', String(gid), '--clear-groups', '--', '/usr/bin/bwrap', '--unshare-all', '--ro-bind', '/usr', '/usr', '--symlink', 'usr/lib', '/lib', '--symlink', 'usr/lib64', '/lib64', '--uid', String(uid), '--gid', String(gid), '--cap-drop', 'ALL', '--', '/usr/bin/true']);
  const goModCache = await noLinks(command('/usr/local/btgo/bin/go', ['env', 'GOMODCACHE'], { ...toolEnv, HOME: '/root' }));
  requireThat((await lstat(goModCache)).isDirectory(), 'Existing readonly Go module input required');
  return { inputs, runner, manifests, descriptions, hostBaseline, plan, uid, gid, databaseURL, database, runRoot, budget, budgetEvidence, available, databaseStorage, databaseFilesystems, databasePeak, goModCache, tooling, hostBefore };
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
  const report = { status: 'FAIL', scope: 'isolated same-source dual Web preparation only; no production switch or schema upgrade', runnerCommit: pre.runner.commit, commit: pre.manifests.next.source.commit, oldManifestSHA256: pre.inputs.oldSHA, nextManifestSHA256: pre.inputs.nextSHA, plan: pre.plan, planSHA256: sha256(JSON.stringify(pre.plan)), tooling: pre.tooling, hostBaseline: pre.hostBaseline, budget: { ...pre.budgetEvidence, databaseStorage: pre.databaseStorage, databaseFilesystems: pre.databaseFilesystems, fixtureActiveMilliseconds: dualFixtureActiveMilliseconds, cleanupWindowMilliseconds, goCache: 'private run-root cache; readonly existing module input; no download' }, hostBefore: pre.hostBefore, configHashes: {} };
  for (const phase of ['old', 'next', 'restored']) report.configHashes[phase] = sha256(renderDualWebNginx(pre.plan, { phase }));
  if (!options.execute) return { ...report, status: 'PREFLIGHT_ONLY', fullAcceptanceExecuted: false };
  const { runRoot, uid, gid, manifests, inputs } = pre;
  await mkdir(runRoot, { mode: 0o755 });
  const control = path.join(runRoot, 'control'), fixture = path.join(runRoot, 'fixture'), mutable = path.join(runRoot, 'mutable'), goTemporary = path.join(runRoot, 'go-tmp'), goCache = path.join(runRoot, 'go-cache'), fixtureRequest = path.join(runRoot, '.fixture-request.json');
  const releases = { old: path.join(runRoot, 'old'), next: path.join(runRoot, 'next') };
  const started = Date.now(); let fixtureProcess, sandbox, timer, failure, monitoring = false, monitorTask, stopping, fixtureStarted;
  const stopSandbox = () => stopping ??= stopDualOwned(sandbox).then(result => { report.sandboxStop = result; if (result.forced) failure ??= new Error('Sandbox required KILL'); return result.groupGone; }).catch(error => { failure ??= error; return false; });
  const interrupt = () => { failure ??= new Error('Dual Web controller interrupted'); if (sandbox) void stopSandbox(); };
  process.once('SIGTERM', interrupt); process.once('SIGINT', interrupt);
  const ensureBudget = async () => {
    requireThat(Date.now() - started < maximumMilliseconds, 'Dual Web 15-minute deadline exceeded');
    await guardReserve(runRoot, pre.budget, pre.available.bytes);
    for (const fs of pre.databaseFilesystems.filter(row => !row.sharesRunFilesystem)) await guardReserve(fs.root, { ...pre.budget, maxPeakBytes: pre.databasePeak }, fs.available.bytes);
    if (fixtureStarted) requireThat(Date.now() - fixtureStarted < dualFixtureActiveMilliseconds, 'Fixture cleanup window reached');
    if (await lstat(goCache).catch(() => null)) {
      const cache = await boundedUsage(goCache), temporary = await boundedUsage(goTemporary);
      requireThat(cache.bytes + temporary.bytes <= pre.budget.components.buildTemporary, 'Private Go cache/temporary component budget exceeded');
      report.observedGoPeakBytes = Math.max(report.observedGoPeakBytes ?? 0, cache.bytes + temporary.bytes);
    }
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
    await mkdir(control, { mode: 0o755 }); await mkdir(fixture, { mode: 0o700 }); await ownDirectory(mutable, uid, gid); await mkdir(goTemporary, { mode: 0o700 }); await mkdir(goCache, { mode: 0o700 });
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
    requireThat(Date.now() < started + maximumMilliseconds - cleanupWindowMilliseconds, 'Insufficient fixture cleanup window before launch');
    const goEnv = { ...toolEnv, HOME: '/root' };
    const fixtureEnv = { ...goEnv, TMPDIR: goTemporary, GOTMPDIR: goTemporary, GOCACHE: goCache, GOMODCACHE: pre.goModCache, ZJMFGO_RELEASE_DUAL_WEB_FIXTURE: '1', ZJMFGO_RELEASE_DUAL_WEB_FIXTURE_ALLOW_SCHEMA: '1', ZJMFGO_RELEASE_DUAL_WEB_FIXTURE_DIR: fixture, ZJMFGO_TEST_DATABASE_URL: pre.databaseURL };
    await writeJSON(fixtureRequest, { parentPID: process.pid, parentMountNamespace: pre.hostBefore.mountNamespace, goModCache: pre.goModCache, cwd: inputs.nextSource, environment: fixtureEnv });
    fixtureStarted = Date.now();
    fixtureProcess = launch('/usr/bin/unshare', ['--mount', '--propagation', 'private', process.execPath, path.join(control, 'accept-release-dual-web.mjs'), '--fixture-worker', fixtureRequest]);
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
    requireThat(Date.now() < Math.min(fixtureStarted + dualFixtureActiveMilliseconds, started + maximumMilliseconds - cleanupWindowMilliseconds), 'Insufficient fixture cleanup window before namespace launch');
    sandbox = launch('/usr/bin/unshare', ['--mount', '--propagation', 'private', path.join(releases.next, manifests.next.runtime.node.path), path.join(control, 'accept-release-dual-web.mjs'), '--mount-worker', mountRequest]);
    await waitDualScenario({ sandbox, fixture: fixtureProcess, fixtureDirectory: fixture, milliseconds: Math.min(fixtureStarted + dualFixtureActiveMilliseconds, started + maximumMilliseconds - cleanupWindowMilliseconds) - Date.now() });
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
    let applicationsGone = !sandbox;
    if (sandbox) {
      applicationsGone = await stopSandbox();
      try {
        const isolation = await jsonFile(path.join(mutable, 'isolation.json'));
        await waitFor(async () => !(await namespaceMembers(isolation)).length, 5, 'private PID namespace disappearance');
        report.privatePIDNamespaceGone = true;
      } catch (error) {
        failure ??= error; report.privatePIDNamespaceGone = false;
        // Early bwrap startup can fail before inner isolation evidence exists.
        // A proven empty original group still permits the fixture's own strict
        // application_name connection check to attempt safe abort cleanup.
        if (await lstat(path.join(mutable, 'isolation.json')).catch(() => null)) applicationsGone = false;
      }
    }
    if (fixtureProcess && !fixtureProcess.exited && applicationsGone) {
      // Only after all application connections are gone may the fixture drop its schema.
      const commands = (await readdir(fixture).catch(() => [])).filter(name => /^command-\d+\.json$/.test(name));
      const sequence = Math.max(0, ...commands.map(name => Number(name.match(/\d+/)[0]))) + 1;
      await writeJSON(path.join(fixture, `command-${sequence}.json`), { operation: 'abort' }).catch(error => { failure ??= error; });
      await deadline(fixtureProcess.done, 30000, 'Fixture abort deadline').catch(error => { failure ??= error; });
    }
    if (fixtureProcess) await stopDualOwned(fixtureProcess).then(result => { report.fixtureStop = result; if (result.forced) failure ??= new Error('Fixture required KILL'); }).catch(error => { failure ??= error; });
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
      requireThat(equal(await databaseStorageProof(pre.databaseURL, pre.database, inputs.pgSocket), pre.databaseStorage), 'PostgreSQL storage/process/tool identity changed'); report.databaseStorageUnchanged = true;
      report.hostAfter = await dualHostSnapshot(); requireThat(equal(report.hostAfter, pre.hostBefore), 'Host namespace/resident process identity changed'); report.hostUnchanged = true;
      requireThat(equal(await sourceProof(runnerRoot, inputs.runnerCommit), pre.runner), 'Runner source changed');
      for (const label of ['old', 'next']) requireThat(equal(await sourceProof(inputs[`${label}Source`], manifests[label].source.commit), manifests[label].source), 'Candidate source changed');
    } catch (error) { failure ??= error; }
    report.runtime ??= await jsonFile(path.join(mutable, 'result.json')).catch(() => null);
    report.hostOrdinaryIdentity ??= await jsonFile(path.join(mutable, 'host-identity.json')).catch(() => null);
    // Config/credentials and run copies are always discarded; retain only bounded sanitized evidence.
    for (const name of ['config.json', 'credentials.json', 'config.json.tmp', 'credentials.json.tmp']) await rm(path.join(fixture, name), { force: true }).catch(error => { failure ??= error; });
    for (const directory of [releases.old, releases.next, control, goTemporary, goCache, fixtureRequest]) await rm(directory, { force: true, recursive: true }).catch(error => { failure ??= error; });
    if (failure) { report.status = 'FAIL'; report.reason = 'Controlled precondition, execution or cleanup failed; inspect sanitized evidence'; }
    await writeJSON(path.join(runRoot, 'report.json'), report);
    process.removeListener('SIGTERM', interrupt); process.removeListener('SIGINT', interrupt);
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
export async function dualFixtureWorker(filename) {
  const request = await jsonFile(filename);
  requireThat(process.ppid === request.parentPID && await readlink('/proc/self/ns/mnt') !== request.parentMountNamespace && await readlink(`/proc/${process.ppid}/ns/mnt`) === request.parentMountNamespace, 'Refusing module cache mount outside owned private fixture namespace');
  await noLinks(request.goModCache);
  let mounted = false, child;
  try {
    command('/usr/bin/mount', ['--bind', request.goModCache, request.goModCache]); mounted = true;
    command('/usr/bin/mount', ['-o', 'remount,bind,ro', request.goModCache]);
    const mount = (await readFile('/proc/self/mountinfo', 'utf8')).split('\n').find(line => line.split(' ')[4] === request.goModCache);
    requireThat(mount?.split(' ')[5].split(',').includes('ro'), 'Existing module cache readonly mount not proven');
    child = launch('/usr/local/btgo/bin/go', ['test', '-mod=readonly', '-run', '^TestReleaseDualWebFixture$', '-count=1', '-timeout=14m', './internal/store'], { cwd: request.cwd, env: request.environment, detached: false });
    await child.done; requireThat(!child.failed && child.code === 0, 'Private Go fixture failed');
  } finally {
    if (child && !child.exited) { try { child.child.kill('SIGTERM'); } catch {} await deadline(child.done, 15000, 'Fixture wrapper child stop deadline'); }
    if (mounted) command('/usr/bin/umount', [request.goModCache]);
  }
}
if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try {
    if (process.argv[2] === '--mount-worker' && process.argv.length === 4) await dualMountWorker(process.argv[3]);
    else if (process.argv[2] === '--fixture-worker' && process.argv.length === 4) await dualFixtureWorker(process.argv[3]);
    else console.log(JSON.stringify(await acceptDualWeb(parseDualWebOptions(process.argv.slice(2))), null, 2));
  } catch { console.error('[release-dual-web] Controlled precondition, execution or cleanup failed; secret values are never logged'); process.exitCode = 1; }
}
