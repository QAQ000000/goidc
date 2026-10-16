#!/usr/bin/env node
import { chmod, chown, copyFile, cp, lstat, mkdir, readFile, readdir, readlink, rm, stat, writeFile } from 'node:fs/promises';
import { execFileSync, spawn, spawnSync } from 'node:child_process';
import net from 'node:net';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { fileHash, git, noLinks, sourceProof } from './release-candidate-lib.mjs';
import { controllerFiles, assertAvailablePort, assertSocket, auditProcesses, jsonFile, pause, requireThat, validateTestDSN, verifyPages, waitFor, writeJSON } from './release-runtime-acceptance-lib.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const toolEnv = { PATH: '/usr/bin:/bin', LANG: 'C.UTF-8', GOTOOLCHAIN: 'local', GOENV: 'off', GOPROXY: 'off', GOSUMDB: 'off' };
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
async function deadline(promise, milliseconds, label) {
  let timer;
  try { return await Promise.race([promise, new Promise((_, reject) => { timer = setTimeout(() => reject(new Error(label)), milliseconds); })]); }
  finally { clearTimeout(timer); }
}
function command(executable, args, env = toolEnv) {
  const result = spawnSync(executable, args, { env, encoding: 'utf8', maxBuffer: 4 * 1024 * 1024 });
  requireThat(!result.error && result.status === 0, `BLOCKED: required host command unavailable: ${path.basename(executable)}`);
  return result.stdout.trim();
}
function launch(executable, args, options = {}) {
  const child = spawn(executable, args, { env: toolEnv, stdio: 'ignore', detached: true, ...options });
  const handle = { child, exited: false, code: null, failed: false };
  handle.done = new Promise(resolve => {
    child.once('error', () => { handle.failed = true; handle.exited = true; resolve(); });
    child.once('close', code => { handle.exited = true; handle.code = code; resolve(); });
  });
  return handle;
}
async function stop(handle, seconds = 15) {
  if (!handle) return;
  if (handle.exited) { try { process.kill(-handle.child.pid, 'SIGTERM'); } catch {} return; }
  try { process.kill(-handle.child.pid, 'SIGTERM'); } catch {}
  const graceful = await deadline(handle.done.then(() => true), seconds * 1000, 'TERM deadline').catch(() => false);
  if (!graceful) { try { process.kill(-handle.child.pid, 'SIGKILL'); } catch {} await handle.done; throw new Error('Owned process exceeded TERM deadline; KILL required'); }
}
async function waitHealthy(handle, check, seconds, label) {
  return waitFor(async () => { requireThat(!handle?.exited, `Owned process exited before ${label}`); return check(); }, seconds, label);
}
export async function copyIndependent(source, target) {
  await cp(source, target, { recursive: true, dereference: false, errorOnExist: true, force: false });
  async function visit(relative = '') {
    for (const name of await readdir(path.join(target, relative))) {
      const rel = relative ? `${relative}/${name}` : name, dest = path.join(target, rel), info = await lstat(dest);
      if (info.isDirectory()) { await chmod(dest, 0o755); await visit(rel); }
      else if (info.isFile()) {
        const original = await lstat(path.join(source, rel));
        requireThat(info.nlink === 1 && !(info.dev === original.dev && info.ino === original.ino), 'Independent copy cannot contain hard links');
        await chmod(dest, info.mode & 0o111 ? 0o555 : 0o444);
      } else requireThat(info.isSymbolicLink(), 'Unexpected special file in runtime copy');
    }
  }
  await visit();
}
export async function verifyHostBaseline(manifest, sealed) {
  const host = manifest.runtime.host;
  const expected = [...Object.values(manifest.binaries).map(row => row.path), manifest.runtime.node.path, ...manifest.artifacts.filter(row => row.path.endsWith('.node') && !row.link).map(row => row.path)].sort();
  requireThat(Array.isArray(host.elf) && same(host.elf.map(row => row.path).sort(), expected), 'BLOCKED: complete actual ELF host baseline required');
  for (const binary of host.elf) {
    const filename = path.join(sealed, binary.path), elf = command('/usr/bin/readelf', ['-l', '-d', filename]);
    const interpreter = elf.match(/Requesting program interpreter: ([^\]]+)/)?.[1] ?? null;
    const needed = [...elf.matchAll(/Shared library: \[([^\]]+)\]/g)].map(row => row[1]).sort();
    requireThat(binary.sha256 === await fileHash(filename) && binary.interpreter === interpreter && same(binary.needed, needed), 'Actual ELF baseline differs from manifest');
    if (interpreter) requireThat((await stat(interpreter)).isFile(), 'BLOCKED: target ELF loader missing');
    const libraries = command('/usr/sbin/ldconfig', ['-p']);
    for (const name of needed) requireThat(libraries.split('\n').some(line => line.trim().startsWith(`${name} `) && line.includes('x86-64')), 'BLOCKED: required target system library missing');
  }
}
export function namespaceArgs({ release, control, fixture, mutable, pgSocket, uid, gid, appRoot }) {
  const args = ['--die-with-parent', '--unshare-all', '--clearenv', '--proc', '/proc', '--dev', '/dev', '--tmpfs', '/tmp', '--dir', '/etc', '--dir', '/usr', '--dir', '/usr/bin'];
  for (const directory of ['/lib', '/lib64', '/usr/lib', '/usr/lib64']) if (spawnSync('/usr/bin/test', ['-d', directory]).status === 0) args.push('--ro-bind', directory, directory);
  for (const file of ['/etc/passwd', '/etc/group', '/etc/nsswitch.conf', '/etc/hosts', '/etc/localtime']) if (spawnSync('/usr/bin/test', ['-e', file]).status === 0) args.push('--ro-bind', file, file);
  args.push('--ro-bind', '/usr/bin/strace', '/usr/bin/strace', '--ro-bind', release, '/release', '--ro-bind', control, '/control', '--bind', fixture, '/fixture', '--bind', mutable, '/mutable', '--bind', path.join(mutable, 'cache'), `/release/${appRoot}/.next/cache`, '--dir', '/pg', '--ro-bind', pgSocket, `/pg/${path.basename(pgSocket)}`, '--uid', String(uid), '--gid', String(gid), '--cap-drop', 'ALL', '--chdir', '/release/artifact');
  return args;
}
async function verifyControllerSource(revision) {
  requireThat(/^[a-f0-9]{40}$/.test(revision ?? ''), 'BLOCKED: independent full runner commit required');
  const root = path.join(here, '..'), proof = await sourceProof(root, revision);
  requireThat(git(root, 'rev-parse', 'HEAD') === revision, 'Runner HEAD differs from declared fixed revision');
  for (const name of [...controllerFiles, 'scripts/release-candidate-lib.mjs', 'scripts/release-runtime-builder-lib.mjs', 'scripts/release-runtime-lib.mjs']) {
    const input = proof.files.find(row => row.path === name);
    requireThat(input && await fileHash(path.join(root, name)) === input.sha256, 'BLOCKED: runner differs from independently pinned clean source');
  }
  return proof;
}
export async function preflight(options) {
  const { verifyRuntimeCandidate } = await import('./release-runtime-lib.mjs');
  const { pinnedJSON, freeSpace, validateBudget, runtimeTarget } = await import('./release-runtime-builder-lib.mjs');
  requireThat(process.platform === 'linux' && process.arch === 'x64' && process.getuid?.() === 0, 'BLOCKED: Linux x64 root controller required');
  for (const key of ['sealed', 'source', 'pgSocket', 'budget']) options[key] = await noLinks(path.resolve(options[key]));
  const manifest = await verifyRuntimeCandidate(options.sealed, options.manifestSHA, options.source);
  const runner = await verifyControllerSource(options.runnerCommit);
  requireThat(manifest.source.files.some(row => row.path === 'internal/store/release_runtime_fixture_test.go'), 'BLOCKED: candidate lacks isolated Go fixture');
  const description = await jsonFile(path.join(options.sealed, manifest.launch.path));
  requireThat(description.runtimeUser === 'www', 'BLOCKED: package must declare ordinary www runtime user');
  const account = command('/usr/bin/getent', ['passwd', 'www']).split(':');
  const uid = Number(account[2]), gid = Number(account[3]);
  requireThat(Number.isSafeInteger(uid) && uid > 0 && Number.isSafeInteger(gid) && gid > 0, 'BLOCKED: ordinary www uid/gid required');
  const targets = runtimeTarget(manifest.web.apiInternalURL, description.webOrigin);
  for (const target of [targets.api, targets.web]) await assertAvailablePort(Number(new URL(target).port));
  const databaseURL = process.env.ZJMFGO_TEST_DATABASE_URL;
  const database = validateTestDSN(databaseURL); await assertSocket(options.pgSocket, database.port);
  requireThat(![new URL(targets.api).port, new URL(targets.web).port].includes(String(database.port)), 'Database and application ports must differ');
  const runRoot = path.resolve(options.runRoot), parent = await noLinks(path.dirname(runRoot));
  requireThat((runRoot.startsWith('/tmp/') || runRoot.startsWith('/dev/shm/')) && !runRoot.startsWith(`${options.sealed}/`) && !(await lstat(runRoot).catch(() => null)), 'BLOCKED: new exclusive /tmp or /dev/shm run root required');
  const budget = (await pinnedJSON(options.budget, options.budgetSHA)).value, available = await freeSpace(parent);
  const budgetEvidence = validateBudget(budget, available);
  const databaseFree = await freeSpace(path.dirname(options.pgSocket));
  const databasePeak = budget.components.testDatabaseAndLogs + budget.components.buildTemporary;
  requireThat(databaseFree.bytes >= databasePeak + budget.minReserveBytes && databaseFree.inodes >= budget.minReserveInodes, 'BLOCKED: database/tool cache filesystem reserve insufficient');
  requireThat(manifest.artifacts.reduce((sum, row) => sum + (row.bytes ?? 0), 0) <= budget.components.runCopy, 'BLOCKED: runtime copy exceeds recorded budget');
  await verifyHostBaseline(manifest, options.sealed);
  requireThat(await fileHash(process.execPath) === await fileHash(path.join(options.sealed, manifest.runtime.node.path)), 'BLOCKED: controller Node must match trusted bundled Node');
  command('/usr/bin/unshare', ['--mount', '--propagation', 'private', '/usr/bin/true']);
  command('/usr/bin/setpriv', ['--reuid', String(uid), '--regid', String(gid), '--clear-groups', '--', '/usr/bin/bwrap', '--unshare-all', '--ro-bind', '/usr', '/usr', '--symlink', 'usr/lib', '/lib', '--symlink', 'usr/lib64', '/lib64', '--uid', String(uid), '--gid', String(gid), '--cap-drop', 'ALL', '--', '/usr/bin/true']);
  return { manifest, runner, description, uid, gid, database, databaseURL, runRoot, budget, available, budgetEvidence, databaseFree, databasePeak };
}
async function fixtureCall(dir, state, operation, seconds = 30) {
  const sequence = ++state.sequence;
  await writeJSON(path.join(dir, `command-${sequence}.json`), { operation });
  const result = await waitHealthy(state.process, () => jsonFile(path.join(dir, `result-${sequence}.json`)).catch(() => null), seconds, `fixture ${operation}`);
  requireThat(result.ok === true, `Fixture rejected ${operation}`);
  return result;
}
async function superviseInner() {
  const { controlledRuntimeEnvironment } = await import('./release-runtime-builder-lib.mjs');
  const request = await jsonFile('/control/request.json'), manifest = await jsonFile('/release/manifest.json'), description = await jsonFile('/release/launch/description.json');
  requireThat(process.getuid() === request.uid && process.getgid() === request.gid, 'Ordinary runtime identity mismatch');
  const env = await controlledRuntimeEnvironment(description, '/fixture/config.json', { uid: request.uid });
  const database = validateTestDSN(env.DATABASE_URL, request.schema);
  // New network namespace has only loopback; the sole external endpoint is this explicit Unix socket.
  const interfaces = (await readFile('/proc/net/dev', 'utf8')).split('\n').slice(2).filter(line => line.includes(':')).map(line => line.split(':')[0].trim()).sort();
  requireThat(same(interfaces, ['lo']) && await readlink('/proc/self/ns/net') !== request.parentNetNamespace, 'Unexpected runtime network namespace/interface');
  const bridge = net.createServer(client => { const upstream = net.createConnection({ path: request.pgSocket }); client.pipe(upstream); upstream.pipe(client); upstream.on('error', () => client.destroy()); client.on('error', () => upstream.destroy()); });
  await new Promise((resolve, reject) => { bridge.once('error', reject); bridge.listen(database.port, '127.0.0.1', resolve); });
  requireThat(!(await lstat(request.hiddenSource).catch(() => null)) && !(await lstat('/usr/local/btgo/bin/go').catch(() => null)) && !(await lstat('/usr/bin/npm').catch(() => null)), 'Source/toolchain visible in runtime namespace');
  await writeJSON('/mutable/isolation.json', { uid: process.getuid(), gid: process.getgid(), namespace: await readlink('/proc/self/ns/net'), loopbackOnly: true, originalSourceHidden: true, toolchainHidden: true });
  const owned = [], evidence = [], fixtureState = { sequence: 0 }; let terminating = false;
  async function call(operation) {
    const number = ++fixtureState.sequence; await writeJSON(`/fixture/command-${number}.json`, { operation });
    const result = await waitFor(() => jsonFile(`/fixture/result-${number}.json`).catch(() => null), 30, `fixture ${operation}`);
    requireThat(result.ok, `Fixture rejected ${operation}`); evidence.push({ operation, ...result }); return result;
  }
  const emergencyStop = async () => {
    if (terminating) return; terminating = true;
    for (const handle of [...owned].reverse()) await stop(handle).catch(() => {});
    bridge.close(); await call('abort').catch(() => {}); process.exit(1);
  };
  process.once('SIGTERM', emergencyStop); process.once('SIGINT', emergencyStop);
  function service(name, step) {
    const executable = `/release/${step.executable}`, argv = step.args ?? [];
    const handle = launch('/usr/bin/strace', ['-ff', '-qq', '-s', '256', '-o', `/mutable/logs/${name}.trace`, '-e', 'trace=%file,%network', executable, ...argv], { env: { ...env, HOME: '/mutable/home', TMPDIR: '/mutable/tmp', NEXT_TELEMETRY_DISABLED: '1' }, cwd: `/release/${step.cwd}` });
    owned.push(handle); return handle;
  }
  async function oneMigration(name, step) { const h = service(name, step); await deadline(h.done, 120000, 'Migration deadline exceeded'); requireThat(!h.failed && h.code === 0, 'Same-package migration failed'); await call('verify-migrations'); }
  try {
    timer = setInterval(() => Promise.all([
      guardReserve(runRoot, pre.budget, pre.available.bytes),
      guardReserve(path.dirname(options.pgSocket), { ...pre.budget, maxPeakBytes: pre.databasePeak }, pre.databaseFree.bytes),
    ]).catch(error => { failure ??= error; stop(sandbox).catch(() => {}); }), 250);
    const steps = Object.fromEntries(description.steps.map(row => [row.name, row]));
    await oneMigration('migrate-first', steps.migrate); await oneMigration('migrate-second', steps.migrate);
    const prepared = await call('prepare'); requireThat(prepared.safeInputs, 'Fixture external-effect boundaries not proven');
    const api = service('api', steps.api);
    await waitHealthy(api, async () => { try { const r = await fetch(steps.api.readyURL, { signal: AbortSignal.timeout(500) }); return r.status === 200; } catch { return false; } }, description.timeouts.readySeconds, 'API readiness');
    const web = service('web', steps.web);
    await waitHealthy(web, async () => { try { const r = await fetch(description.webOrigin, { signal: AbortSignal.timeout(500) }); return r.status === 200; } catch { return false; } }, description.timeouts.readySeconds, 'Web readiness');
    const pages = await verifyPages(manifest, '/release', description.webOrigin);
    let worker = service('worker-first', steps.worker);
    await waitHealthy(worker, async () => !(await call('verify-first')).pending, description.timeouts.workerSeconds, 'worker simulation completion');
    const identities = await auditProcesses('/release', [steps.api, steps.web, steps.worker].map((step, i) => ({ ...step, name: ['api', 'web', 'worker'][i] })));
    requireThat(['api', 'web', 'worker'].every(name => identities.some(row => row.name === name)), 'Actual package runtime identity missing');
    for (const identity of identities) requireThat(manifest.artifacts.find(row => row.path === identity.executable)?.sha256 === identity.sha256, 'Actual running executable differs from sealed artifact');
    await stop(worker, description.timeouts.stopSeconds);
    worker = service('worker-restart', steps.worker);
    // Observe a complete scheduler polling window before checking duplicate execution.
    await pause(6000); requireThat(!worker.exited, 'Restarted worker exited'); await call('verify-restart');
    for (const handle of [...owned].reverse()) await stop(handle, description.timeouts.stopSeconds);
    await new Promise(resolve => bridge.close(resolve));
    for (const target of [description.apiInternalURL, description.webOrigin]) await assertAvailablePort(Number(new URL(target).port));
    await call('finish'); await waitFor(() => jsonFile('/fixture/done.json').catch(() => null), 15, 'fixture cleanup');
    await writeJSON('/mutable/result.json', { ok: true, pages, identities, migrations: evidence.filter(row => row.operation === 'verify-migrations'), fixture: evidence, allOwnedExited: owned.every(row => row.exited), listenersGone: true });
    process.removeListener('SIGTERM', emergencyStop); process.removeListener('SIGINT', emergencyStop);
  } catch (error) {
    for (const handle of [...owned].reverse()) await stop(handle).catch(() => {});
    bridge.close(); await call('abort').catch(() => {});
    await writeJSON('/mutable/failure.json', { ok: false, phase: 'runtime', reason: 'Runtime boundary or acceptance check failed' }).catch(() => {});
    throw error;
  }
}
export async function acceptRuntime(options) {
  const pre = await preflight(options);
  if (!options.execute) return { status: 'PREFLIGHT_ONLY', commit: pre.manifest.source.commit, runnerCommit: pre.runner.commit, budget: pre.budgetEvidence, fullAcceptanceExecuted: false };
  const { runtimeInventory } = await import('./release-runtime-lib.mjs');
  const { guardReserve } = await import('./release-runtime-builder-lib.mjs');
  const { manifest, runRoot, uid, gid } = pre;
  await mkdir(runRoot, { mode: 0o755 });
  const release = path.join(runRoot, 'release'), control = path.join(runRoot, 'control'), fixture = path.join(runRoot, 'fixture'), mutable = path.join(runRoot, 'mutable');
  let fixtureProcess, sandbox, timer, failure;
  const interrupted = () => { failure ??= new Error('Controller interrupted'); stop(sandbox).catch(() => {}); };
  process.once('SIGTERM', interrupted); process.once('SIGINT', interrupted);
  const report = { status: 'FAIL', commit: manifest.source.commit, runnerCommit: pre.runner.commit, manifestSHA256: options.manifestSHA, checks: [] };
  try {
    for (const directory of [control, fixture, mutable]) await mkdir(directory, { mode: directory === fixture ? 0o700 : 0o755 });
    await copyIndependent(options.sealed, release);
    requireThat(same(await runtimeInventory(release), manifest.artifacts), 'Independent runtime-copy inventory differs');
    await mkdir(path.join(release, manifest.web.appRoot, '.next/cache'), { mode: 0o755, recursive: true });
    for (const name of ['home', 'tmp', 'logs', 'state', 'cache']) { const directory = path.join(mutable, name); await mkdir(directory, { mode: 0o700 }); await chown(directory, uid, gid); }
    await chown(mutable, uid, gid);
    for (const file of [...controllerFiles, 'scripts/release-candidate-lib.mjs', 'scripts/release-runtime-builder-lib.mjs']) await copyFile(path.join(here, '..', file), path.join(control, path.basename(file)));
    const goEnv = { ...toolEnv, HOME: '/root' }, goTemporary = path.join(runRoot, 'go-tmp'); await mkdir(goTemporary, { mode: 0o700 });
    const fixtureEnv = { ...goEnv, GOCACHE: command('/usr/local/btgo/bin/go', ['env', 'GOCACHE'], goEnv), GOMODCACHE: command('/usr/local/btgo/bin/go', ['env', 'GOMODCACHE'], goEnv), TMPDIR: goTemporary, GOTMPDIR: goTemporary, ZJMFGO_RELEASE_RUNTIME_FIXTURE: '1', ZJMFGO_TEST_DATABASE_URL: pre.databaseURL, ZJMFGO_RELEASE_RUNTIME_FIXTURE_DIR: fixture };
    fixtureProcess = launch('/usr/local/btgo/bin/go', ['test', '-mod=readonly', '-run', '^TestReleaseRuntimeFixture$', '-count=1', '-timeout=15m', './internal/store'], { cwd: options.source, env: fixtureEnv });
    const ready = await waitHealthy(fixtureProcess, () => jsonFile(path.join(fixture, 'schema-ready.json')).catch(() => null), 120, 'fixture schema creation');
    requireThat(ready.ok && ready.pgVersion >= 180000 && ready.pgVersion < 190000 && ready.database === pre.database.database, 'Fixture PG18 identity invalid');
    const config = await jsonFile(path.join(fixture, 'config.json')); validateTestDSN(config.values.DATABASE_URL, ready.schema);
    requireThat(!failure, 'Disk reserve crossed before runtime launch');
    await chown(fixture, uid, gid); await chown(path.join(fixture, 'config.json'), uid, gid);
    await writeJSON(path.join(control, 'request.json'), { uid, gid, schema: ready.schema, pgSocket: `/pg/${path.basename(options.pgSocket)}`, hiddenSource: options.source, parentNetNamespace: await readlink('/proc/self/ns/net') });
    await chmod(path.join(control, 'request.json'), 0o444);
    const socketStageDirectory = path.join(runRoot, 'socket'); await mkdir(socketStageDirectory, { mode: 0o755 });
    const socketStage = path.join(socketStageDirectory, path.basename(options.pgSocket)); await writeFile(socketStage, '', { flag: 'wx', mode: 0o644 });
    const args = namespaceArgs({ release, control, fixture, mutable, pgSocket: socketStage, uid, gid, appRoot: manifest.web.appRoot });
    await writeJSON(path.join(control, 'mount-request.json'), { parentPID: process.pid, parentMountNamespace: await readlink('/proc/self/ns/mnt'), originalSocket: options.pgSocket, stagedSocket: socketStage, argv: [...args, `/release/${manifest.runtime.node.path}`, '/control/accept-release-runtime.mjs', '--inner'] });
    sandbox = launch('/usr/bin/unshare', ['--mount', '--propagation', 'private', path.join(release, manifest.runtime.node.path), path.join(control, 'accept-release-runtime.mjs'), '--mount-worker', path.join(control, 'mount-request.json')]);
    await deadline(sandbox.done, 11 * 60 * 1000, 'Runtime controller deadline exceeded');
    requireThat(!failure && !sandbox.failed && sandbox.code === 0, 'Runtime namespace failed or exceeded resource budget');
    const runtime = await jsonFile(path.join(mutable, 'result.json'));
    requireThat(runtime.ok && runtime.allOwnedExited && runtime.listenersGone, 'Runtime success/exit proof incomplete');
    requireThat(same(await runtimeInventory(release), manifest.artifacts), 'Runtime immutable copy changed');
    const { auditTraceFiles } = await import('./release-runtime-acceptance-lib.mjs');
    const audit = await auditTraceFiles(path.join(mutable, 'logs'), [`/release/${manifest.web.appRoot}/.next/cache`]);
    await waitFor(() => jsonFile(path.join(fixture, 'done.json')).catch(() => null), 30, 'fixture done');
    await fixtureProcess.done; requireThat(fixtureProcess.code === 0, 'Go fixture failed');
    const { mutableAudit } = await import('./release-runtime-acceptance-lib.mjs');
    report.status = 'PASS'; report.runtime = runtime; report.isolation = await jsonFile(path.join(mutable, 'isolation.json')); report.audit = audit; report.mutable = await mutableAudit(mutable);
  } catch (error) { failure = error; }
  finally {
    clearInterval(timer);
    process.removeListener('SIGTERM', interrupted); process.removeListener('SIGINT', interrupted);
    await stop(sandbox).catch(error => { failure ??= error; });
    if (fixtureProcess && !fixtureProcess.exited) {
      // Inner controller normally cleans the schema. If it died, continue the exact sequential protocol.
      const commands = (await readdir(fixture).catch(() => [])).filter(name => /^command-\d+\.json$/.test(name));
      const sequence = Math.max(0, ...commands.map(name => Number(name.match(/\d+/)[0]))) + 1;
      await writeJSON(path.join(fixture, `command-${sequence}.json`), { operation: 'abort' }).catch(() => {});
      await deadline(fixtureProcess.done, 30000, 'Fixture abort deadline').catch(() => {});
      await stop(fixtureProcess).catch(error => { failure ??= error; });
    }
    try { requireThat(same(await runtimeInventory(options.sealed), manifest.artifacts) && await fileHash(path.join(options.sealed, 'manifest.json')) === options.manifestSHA, 'Sealed candidate changed during runtime acceptance'); report.sealedUnchanged = true; }
    catch (error) { failure ??= error; report.sealedUnchanged = false; }
    const cleaned = await jsonFile(path.join(fixture, 'done.json')).catch(() => null);
    report.fixtureSchemaRemoved = cleaned?.schemaRemoved === true;
    if (fixtureProcess && !report.fixtureSchemaRemoved) failure ??= new Error('Owned schema cleanup not proven');
    if (failure) { report.status = 'FAIL'; report.reason = 'Acceptance failed; inspect only sanitized phase evidence'; }
    await writeJSON(path.join(runRoot, 'report.json'), report);
    // Preserve bounded evidence and fixture cleanup result, never config/credentials or the duplicate release.
    await rm(path.join(fixture, 'config.json'), { force: true });
    await rm(release, { force: true, recursive: true }); await rm(control, { force: true, recursive: true }); await rm(path.join(runRoot, 'go-tmp'), { force: true, recursive: true });
  }
  requireThat(!failure, `Runtime acceptance failed; sanitized report: ${path.join(runRoot, 'report.json')}`);
  return { status: 'ACCEPTED_PREPARATION_ONLY', report: path.join(runRoot, 'report.json'), commit: manifest.source.commit, manifestSHA256: options.manifestSHA };
}
async function mountWorker(filename) {
  const request = await jsonFile(filename);
  requireThat(process.ppid === request.parentPID && await readlink('/proc/self/ns/mnt') !== request.parentMountNamespace && await readlink(`/proc/${process.ppid}/ns/mnt`) === request.parentMountNamespace, 'Refusing socket bind outside owned private mount namespace');
  command('/usr/bin/mount', ['--bind', request.originalSocket, request.stagedSocket]);
  const uid = request.argv[request.argv.indexOf('--uid') + 1], gid = request.argv[request.argv.indexOf('--gid') + 1];
  const child = spawn('/usr/bin/setpriv', ['--reuid', uid, '--regid', gid, '--clear-groups', '--pdeathsig', 'KILL', '--', '/usr/bin/bwrap', ...request.argv], { env: toolEnv, stdio: 'ignore' });
  const code = await new Promise(resolve => { child.once('error', () => resolve(1)); child.once('close', resolve); });
  command('/usr/bin/umount', [request.stagedSocket]);
  requireThat(code === 0, 'Runtime namespace did not complete');
}
function parseOptions() {
  const options = {}, names = { '--sealed': 'sealed', '--manifest-sha256': 'manifestSHA', '--source': 'source', '--runner-commit': 'runnerCommit', '--run-root': 'runRoot', '--pg-socket': 'pgSocket', '--budget': 'budget', '--budget-sha256': 'budgetSHA' }, args = process.argv.slice(2);
  if (args.at(-1) === '--execute') { options.execute = true; args.pop(); }
  for (let i = 0; i < args.length; i += 2) { requireThat(names[args[i]] && args[i + 1] && !options[names[args[i]]], 'Explicit unique controller options required; --execute must be final'); options[names[args[i]]] = args[i + 1]; }
  requireThat(Object.values(names).every(name => options[name]), `Required options: ${Object.keys(names).join(' ')} [--execute]`);
  return options;
}
if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try { if (process.argv[2] === '--inner') await superviseInner(); else if (process.argv[2] === '--mount-worker') await mountWorker(process.argv[3]); else console.log(JSON.stringify(await acceptRuntime(parseOptions()), null, 2)); }
  catch { console.error('[release-runtime-acceptance] Controlled precondition or acceptance failed; no secret values are logged'); process.exitCode = 1; }
}
