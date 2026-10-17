#!/usr/bin/env node
// The candidate runtime phase of G09. This does not execute or pass capacity.
import { chmod, chown, copyFile, lstat, mkdir, open, readFile, readlink, rm, writeFile } from 'node:fs/promises';
import { spawn, spawnSync } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import net from 'node:net';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { fileHash, git, noLinks, sourceProof } from './release-candidate-lib.mjs';
import { verifyRuntimeCandidate, runtimeInventory } from './release-runtime-lib.mjs';
import { freeSpace, pinnedJSON, validateBudget, guardReserve, controlledRuntimeEnvironment } from './release-runtime-builder-lib.mjs';
import { assertAvailablePort, assertSocket, auditTraceFiles, jsonFile, pause, requireThat, verifyPages, waitFor, writeJSON } from './release-runtime-acceptance-lib.mjs';
import { copyIndependent, launch, namespaceArgs, stop, verifyHostBaseline } from './accept-release-runtime.mjs';
import { verifyG09ResidentTraces } from './g09-runtime-trace.mjs';
import { recoverG09ControlHistory } from './g09-runtime-recovery.mjs';
import { launchG09TracedChild } from './g09-runtime-process.mjs';
import { createG09ProcessSampler } from './g09-process-sampler.mjs';
import { g09RuntimeFiles, g09MigrationInputs, validateG09RuntimeDSN, validateG09Command, writeG09ControlJSON, readG09ControlJSON } from './g09-runtime-contract.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const tools = { PATH: '/usr/bin:/bin', LANG: 'C.UTF-8', GOTOOLCHAIN: 'local', GOENV: 'off', GOPROXY: 'off', GOSUMDB: 'off' };
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
function command(exe, args, env = tools) {
  const r = spawnSync(exe, args, { env, encoding: 'utf8', maxBuffer: 4 * 1024 * 1024 });
  requireThat(!r.error && r.status === 0, 'g09_host_command_failed'); return r.stdout.trim();
}
async function deadline(promise, ms) {
  let timer;
  try { return await Promise.race([promise, new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('g09_deadline')), ms); })]); }
  finally { clearTimeout(timer); }
}
async function available(handle, predicate, seconds) {
  return waitFor(async () => { requireThat(!handle.exited, 'g09_process_exited'); return predicate(); }, seconds, 'g09_bounded_readiness');
}
export async function g09RuntimePreflight(options) {
  requireThat(process.platform === 'linux' && process.arch === 'x64' && process.getuid() === 0, 'g09_root_controller_required');
  for (const name of ['sealed', 'source', 'pgSocket', 'budget']) options[name] = await noLinks(path.resolve(options[name]));
  const manifest = await verifyRuntimeCandidate(options.sealed, options.manifestSHA, options.source);
  requireThat(manifest.source.commit === options.runnerCommit && git(options.source, 'rev-parse', 'HEAD') === options.runnerCommit, 'g09_same_source_candidate_required');
  const runnerRoot = path.join(here, '..'), proof = await sourceProof(runnerRoot, options.runnerCommit);
  requireThat(git(runnerRoot, 'rev-parse', 'HEAD') === options.runnerCommit, 'g09_runner_head_invalid');
  for (const filename of [...g09RuntimeFiles, 'internal/store/g09_candidate_fixture_test.go']) {
    const expected = proof.files.find(row => row.path === filename);
    requireThat(expected && await fileHash(path.join(runnerRoot, filename)) === expected.sha256 &&
      await fileHash(path.join(options.source, filename)) === expected.sha256, 'g09_controller_source_changed');
  }
  const description = await jsonFile(path.join(options.sealed, manifest.launch.path));
  requireThat(description.runtimeUser === 'www', 'g09_ordinary_user_required');
  const account = command('/usr/bin/getent', ['passwd', 'www']).split(':');
  const uid = Number(account[2]), gid = Number(account[3]);
  requireThat(Number.isSafeInteger(uid) && uid > 0 && Number.isSafeInteger(gid) && gid > 0, 'g09_ordinary_user_invalid');
  const databaseURL = process.env.ZJMFGO_TEST_DATABASE_URL, database = validateG09RuntimeDSN(databaseURL);
  await assertSocket(options.pgSocket, database.port);
  for (const origin of [description.apiInternalURL, description.webOrigin]) {
    const url = new URL(origin);
    requireThat(url.hostname === '127.0.0.1' && url.protocol === 'http:' && Number(url.port) !== database.port, 'g09_application_target_invalid');
    await assertAvailablePort(Number(url.port));
  }
  const runRoot = path.resolve(options.runRoot);
  requireThat((runRoot.startsWith('/tmp/') || runRoot.startsWith('/dev/shm/')) &&
    !(await lstat(runRoot).catch(error => { if (error.code === 'ENOENT') return null; throw error; })), 'g09_exclusive_run_required');
  requireThat(![options.sealed, options.source].some(base => runRoot === base || runRoot.startsWith(`${base}/`)), 'g09_run_must_be_independent');
  const parent = await noLinks(path.dirname(runRoot)), availableSpace = await freeSpace(parent);
  const budget = (await pinnedJSON(options.budget, options.budgetSHA)).value;
  const budgetEvidence = validateBudget(budget, availableSpace);
  requireThat(manifest.artifacts.reduce((sum, row) => sum + (row.bytes ?? 0), 0) <= budget.components.runCopy, 'g09_copy_budget_invalid');
  const databaseFree = await freeSpace(path.dirname(options.pgSocket));
  requireThat(databaseFree.bytes >= budget.components.testDatabaseAndLogs + budget.minReserveBytes, 'g09_database_reserve_invalid');
  const hostBaseline = await verifyHostBaseline(manifest, options.sealed);
  requireThat(await fileHash(process.execPath) === await fileHash(path.join(options.sealed, manifest.runtime.node.path)), 'g09_controller_node_mismatch');
  command('/usr/bin/unshare', ['--mount', '--propagation', 'private', '/usr/bin/true']);
  command('/usr/bin/setpriv', ['--reuid', String(uid), '--regid', String(gid), '--clear-groups', '--', '/usr/bin/bwrap', '--unshare-all', '--ro-bind', '/usr', '/usr', '--symlink', 'usr/lib', '/lib', '--symlink', 'usr/lib64', '/lib64', '--uid', String(uid), '--gid', String(gid), '--cap-drop', 'ALL', '--', '/usr/bin/true']);
  return { manifest, description, uid, gid, databaseURL, database, runRoot, budget, budgetEvidence, availableSpace, hostBaseline };
}

async function fixtureCall(directory, state, action, uid = process.getuid(), gid = process.getgid()) {
  requireThat(!state.failed, 'g09_control_frozen');
  const sequence = ++state.sequence;
  try {
    const request = validateG09Command({ runId: state.runId, sequence, action }, state.runId, sequence);
    await writeG09ControlJSON(path.join(directory, `command-${sequence}.json`), request, { uid, gid });
    const result = await waitFor(async () => {
      const name = path.join(directory, `result-${sequence}.json`);
      const info = await lstat(name).catch(error => { if (error.code === 'ENOENT') return null; throw error; });
      return info ? readG09ControlJSON(name, { uid }) : null;
    }, 30, 'g09_fixture_response');
    requireThat(result.ok === true && result.runId === state.runId && result.sequence === sequence && result.action === action, 'g09_fixture_rejected');
    return result;
  } catch { state.failed = true; throw new Error('g09_control_failed'); }
}

async function inner() {
  const request = await jsonFile('/control/request.json'), manifest = await jsonFile('/release/manifest.json');
  const description = await jsonFile('/release/launch/description.json');
  requireThat(process.getuid() === request.uid && process.getgid() === request.gid, 'g09_inner_identity');
  const env = await controlledRuntimeEnvironment(description, '/fixture/config.json', { uid: request.uid });
  const database = validateG09RuntimeDSN(env.DATABASE_URL, request.schema);
  const interfaces = (await readFile('/proc/net/dev', 'utf8')).split('\n').slice(2).filter(x => x.includes(':')).map(x => x.split(':')[0].trim()).sort();
  requireThat(same(interfaces, ['lo']) && await readlink('/proc/self/ns/net') !== request.parentNetNamespace, 'g09_network_not_isolated');
  for (const item of request.hostBaseline.ignoredSearchPaths) requireThat(!(await lstat(item.path).catch(() => null)), 'g09_loader_path_visible');
  for (const filename of [request.hiddenSource, '/usr/local/btgo/bin/go', '/usr/bin/npm']) requireThat(!(await lstat(filename).catch(() => null)), 'g09_source_toolchain_visible');
  const sockets = new Set();
  const bridge = net.createServer(client => {
    const upstream = net.createConnection({ path: request.pgSocket });
    sockets.add(client); sockets.add(upstream);
    client.once('close', () => sockets.delete(client)); upstream.once('close', () => sockets.delete(upstream));
    upstream.on('error', () => client.destroy()); client.on('error', () => upstream.destroy());
    client.pipe(upstream); upstream.pipe(client);
  });
  await new Promise((resolve, reject) => { bridge.once('error', reject); bridge.listen(database.port, '127.0.0.1', resolve); });
  const state = { runId: request.runId, sequence: 0 }, resident = [], migrations = [], processSamples = [], observations = [];
  const stepMap = Object.fromEntries(description.steps.map(row => [row.name, row]));
  let phase = 'ready', interrupting = false, completed = false;
  const mark = async value => { phase = value; await writeJSON(`/mutable/phase-${value}.json`, { phase: value }); };
  const stopResidents = async () => { let failure; for (const handle of [...resident].reverse()) await handle.stop().catch(error => { failure ??= error; }); if (failure) throw failure; };
  const closeBridge = async () => { for (const socket of sockets) socket.destroy(); await new Promise(resolve => bridge.close(resolve)); };
  const interrupted = () => { if (interrupting) return; interrupting = true; stopResidents().finally(() => process.exit(1)); };
  process.once('SIGTERM', interrupted); process.once('SIGINT', interrupted);
  try {
    await fixtureCall('/fixture', state, 'ready');
    for (const which of ['first', 'second']) {
      await mark(`migrate-${which}`);
      const step = stepMap.migrate;
      const handle = launch('/usr/bin/strace', ['-ff', '-yy', '-qq', '-s', '256', '-o', `/mutable/logs/migrate-${which}.trace`, '-e', 'trace=%file,%network,fchmod,fchown,ftruncate,fallocate', '-e', 'raw=sendto,sendmsg,sendmmsg,recvfrom,recvmsg,recvmmsg', `/release/${step.executable}`, ...(step.args ?? [])], { cwd: `/release/${step.cwd}`, env });
      try { await deadline(handle.done, 120000); requireThat(!handle.failed && handle.code === 0, 'g09_migration_failed'); }
      finally { await stop(handle, 5); }
      migrations.push(await fixtureCall('/fixture', state, 'verify-migrations'));
    }
    requireThat(migrations[0].migrationCount === request.migrationCount && migrations[0].migrationSHA256 === migrations[1].migrationSHA256, 'g09_migration_digest_changed');
    const identities = [];
    for (const name of ['api', 'web', 'worker_0', 'worker_1']) {
      await mark(`start-${name}`);
      const step = stepMap[name.startsWith('worker_') ? 'worker' : name];
      const executable = `/release/${step.executable}`;
      const actual = await fileHash(executable), expected = manifest.artifacts.find(row => row.path === step.executable)?.sha256;
      requireThat(actual === expected && expected, 'g09_candidate_executable_mismatch');
      const handle = await launchG09TracedChild({ executable, args: step.args ?? [], cwd: `/release/${step.cwd}`, env: { ...env, HOME: '/mutable/home', TMPDIR: '/mutable/tmp', NEXT_TELEMETRY_DISABLED: '1' }, tracePrefix: `/mutable/logs/${name}.trace` });
      resident.push(handle);
      identities.push({ ...handle.identity, name, executableSHA256: actual, tracerPID: handle.tracerPID });
      if (['api', 'web'].includes(name)) {
        await waitFor(async () => { try { const r = await fetch(name === 'api' ? step.readyURL : description.webOrigin, { redirect: 'manual', signal: AbortSignal.timeout(500) }); return r.status === 200; } catch { return false; } }, description.timeouts.readySeconds, 'g09_application_readiness');
      }
    }
    await mark('sample');
    const clockTicksPerSecond = request.clockTicksPerSecond;
    const sample = createG09ProcessSampler({ targets: identities.map(({ executableSHA256, tracerPID, ...row }) => row), clockTicksPerSecond, maxSamples: 2 });
    processSamples.push(await sample());
    observations.push(await fixtureCall('/fixture', state, 'snapshot'));
    const pages = await verifyPages(manifest, '/release', description.webOrigin);
    await pause(2200); processSamples.push(await sample());
    observations.push(await fixtureCall('/fixture', state, 'snapshot'));
    await mark('stop'); await stopResidents(); await closeBridge();
    for (const origin of [description.apiInternalURL, description.webOrigin]) await assertAvailablePort(Number(new URL(origin).port));
    await fixtureCall('/fixture', state, 'finish'); completed = true;
    await writeJSON('/mutable/result.json', { ok: true, mode: 'candidate_runtime_bootstrap', candidateRuntimeVerified: true, capacityVerified: false, workerReadinessVerified: false, identities, processSamples, observations, migrations, pages, allOwnedExited: true, listenersGone: true, controlSequence: state.sequence });
  } catch {
    await stopResidents().catch(() => {}); await closeBridge().catch(() => {});
    await writeJSON('/mutable/failure.json', { ok: false, phase, capacityVerified: false }).catch(() => {});
    throw new Error('g09_inner_failed');
  } finally {
    process.removeListener('SIGTERM', interrupted); process.removeListener('SIGINT', interrupted);
    if (!completed) { /* outer waits for namespace exit before sending abort */ }
  }
}

export async function acceptG09Runtime(options) {
  const pre = await g09RuntimePreflight(options);
  if (!options.execute) return { status: 'PREFLIGHT_ONLY', sourceCommit: pre.manifest.source.commit, capacityVerified: false };
  const { runRoot, manifest, uid, gid } = pre;
  const runId = `g09_candidate_${randomBytes(12).toString('hex')}`;
  const release = path.join(runRoot, 'release'), control = path.join(runRoot, 'control');
  const fixture = path.join(runRoot, 'fixture'), mutable = path.join(runRoot, 'mutable');
  let fixtureProcess, fixtureLog, fixtureDeadline, activeBuild, sandbox, timer, failure, checking = false;
  const report = { status: 'FAIL', mode: 'candidate_runtime_bootstrap', runId, sourceCommit: manifest.source.commit,
    runnerCommit: options.runnerCommit, manifestSHA256: options.manifestSHA, capacityVerified: false, workerReadinessVerified: false };
  const interrupted = () => { failure ??= new Error('g09_interrupted'); if (activeBuild) stop(activeBuild).catch(() => {}); if (sandbox) stop(sandbox).catch(() => {}); };
  await mkdir(runRoot, { mode: 0o755 });
  process.once('SIGTERM', interrupted); process.once('SIGINT', interrupted);
  try {
    timer = setInterval(async () => {
      if (checking || failure) return; checking = true;
      try { await guardReserve(runRoot, pre.budget, pre.availableSpace.bytes); }
      catch { failure ??= new Error('g09_resource_budget'); if (activeBuild) await stop(activeBuild, 5).catch(() => {}); if (sandbox) await stop(sandbox).catch(() => {}); }
      finally { checking = false; }
    }, 2000);
    await copyIndependent(options.sealed, release);
    requireThat(same(await runtimeInventory(release), manifest.artifacts), 'g09_copy_changed');
    await mkdir(control, { mode: 0o755 });
    for (const file of g09RuntimeFiles) { const target = path.join(control, path.basename(file)); await copyFile(path.join(here, '..', file), target); await chmod(target, 0o444); }
    for (const directory of [fixture, mutable]) { await mkdir(directory, { mode: 0o700 }); await chown(directory, uid, gid); }
    for (const name of ['logs', 'cache', 'home', 'tmp']) { const directory = path.join(mutable, name); await mkdir(directory, { mode: 0o700 }); await chown(directory, uid, gid); }
    const goTemporary = path.join(runRoot, 'go-tmp'); await mkdir(goTemporary, { mode: 0o700 });
    const fixtureTemporary = path.join(runRoot, 'fixture-tmp'); await mkdir(fixtureTemporary, { mode: 0o700 }); await chown(fixtureTemporary, uid, gid);
    const socketDirectory = path.join(runRoot, 'socket'); await mkdir(socketDirectory, { mode: 0o755 });
    const fixtureBinary = path.join(runRoot, 'host-fixture.test');
    const goWrapper = path.join(options.source, 'scripts/go.sh');
    const moduleCache = command(goWrapper, ['env', 'GOMODCACHE'], { ...tools, HOME: '/root' });
    await noLinks(moduleCache);
    const goCache = path.join(goTemporary, 'cache'); await mkdir(goCache, { mode: 0o700 });
    const goEnv = { ...tools, HOME: '/root', TMPDIR: goTemporary, GOTMPDIR: goTemporary, GOCACHE: goCache, GOMODCACHE: moduleCache };
    const compileLog = await open(path.join(runRoot, 'fixture-compile.log'), 'wx', 0o600);
    await guardReserve(runRoot, pre.budget, pre.availableSpace.bytes);
    // Compiler inputs, including shared module cache, are read-only. Only this
    // exclusively owned run root is writable; compilation has no host network.
    const compile = activeBuild = launch('/usr/bin/bwrap', ['--die-with-parent', '--unshare-net', '--ro-bind', '/', '/', '--bind', runRoot, runRoot, '--proc', '/proc', '--dev', '/dev', '--cap-drop', 'ALL', '--chdir', options.source, '--', goWrapper, 'test', '-mod=readonly', '-c', '-o', fixtureBinary, './internal/store'], { cwd: options.source, env: goEnv, stdio: ['ignore', compileLog.fd, compileLog.fd] });
    try { await deadline(compile.done, Math.min(pre.budget.maxBuildSeconds, 300) * 1000); requireThat(!compile.failed && compile.code === 0, 'g09_fixture_compile_failed'); }
    finally { try { await stop(compile, 5); } finally { activeBuild = null; await compileLog.close(); } }
    await guardReserve(runRoot, pre.budget, pre.availableSpace.bytes);
    requireThat(!failure, 'g09_reserve_crossed_during_compile');
    await chmod(fixtureBinary, 0o555);
    const bootstrap = path.join(control, 'bootstrap.private.json');
    await writeG09ControlJSON(bootstrap, { runId, bridgeDirectory: socketDirectory, migrations: g09MigrationInputs(manifest) });
    await chown(bootstrap, uid, gid);
    const fixtureEnv = { ...tools, TMPDIR: fixtureTemporary, ZJMFGO_G09_CANDIDATE_FIXTURE: '1',
      ZJMFGO_TEST_DATABASE_URL: pre.databaseURL, ZJMFGO_G09_CANDIDATE_FIXTURE_DIR: fixture,
      ZJMFGO_G09_CANDIDATE_BOOTSTRAP_CONFIG: bootstrap };
    // Includes the fixture's own 8-minute context and bounded deferred cleanup.
    // Unknown control results must never cause an early TERM that skips that defer.
    fixtureDeadline = Date.now() + (9 * 60 + 15) * 1000;
    fixtureLog = await open(path.join(runRoot, 'host-fixture.log'), 'wx', 0o600);
    fixtureProcess = launch('/usr/bin/setpriv', ['--reuid', String(uid), '--regid', String(gid), '--clear-groups', '--', fixtureBinary, '-test.run=^TestG09CandidateFixture$', '-test.timeout=9m', '-test.v'], { cwd: fixtureTemporary, env: fixtureEnv, stdio: ['ignore', fixtureLog.fd, fixtureLog.fd] });
    const ready = await available(fixtureProcess, async () => {
      const filename = path.join(fixture, 'schema-ready.json');
      return await lstat(filename).catch(() => null) ? readG09ControlJSON(filename, { uid }) : null;
    }, 30);
    requireThat(ready.ok && ready.runId === runId && ready.pgVersion >= 180000 && ready.pgVersion < 190000 && ready.database === pre.database.database && ready.applicationName === `g09_candidate_${ready.schema}`, 'g09_fixture_identity_invalid');
    const config = await readG09ControlJSON(path.join(fixture, 'config.json'), { uid });
    validateG09RuntimeDSN(config.values.DATABASE_URL, ready.schema);
    report.schema = ready.schema;
    requireThat(!failure, 'g09_reserve_crossed_before_start');
    await writeJSON(path.join(control, 'request.json'), { runId, uid, gid, schema: ready.schema,
      migrationCount: manifest.migrations.length, pgSocket: `/pg/${path.basename(options.pgSocket)}`,
      hiddenSource: options.source, parentNetNamespace: await readlink('/proc/self/ns/net'),
      clockTicksPerSecond: Number(command('/usr/bin/getconf', ['CLK_TCK'])), hostBaseline: pre.hostBaseline });
    await chmod(path.join(control, 'request.json'), 0o444);
    const stagedSocket = path.join(socketDirectory, path.basename(options.pgSocket)); await writeFile(stagedSocket, '', { flag: 'wx', mode: 0o644 });
    const args = namespaceArgs({ release, control, fixture, mutable, pgSocket: stagedSocket, uid, gid, appRoot: manifest.web.appRoot });
    await writeJSON(path.join(control, 'mount-request.json'), { parentPID: process.pid, parentMountNamespace: await readlink('/proc/self/ns/mnt'),
      originalSocket: options.pgSocket, stagedSocket, uid, gid,
      argv: [...args, `/release/${manifest.runtime.node.path}`, '/control/accept-g09-runtime.mjs', '--inner'] });
    sandbox = launch('/usr/bin/unshare', ['--mount', '--propagation', 'private', path.join(release, manifest.runtime.node.path), path.join(control, 'accept-g09-runtime.mjs'), '--mount-worker', path.join(control, 'mount-request.json')]);
    await deadline(sandbox.done, 6 * 60 * 1000);
    requireThat(!failure && !sandbox.failed && sandbox.code === 0, 'g09_namespace_failed');
    const runtime = await jsonFile(path.join(mutable, 'result.json'));
    requireThat(runtime.ok && runtime.candidateRuntimeVerified && runtime.capacityVerified === false && runtime.allOwnedExited && runtime.listenersGone, 'g09_runtime_proof_incomplete');
    requireThat(same(await runtimeInventory(release), manifest.artifacts), 'g09_runtime_copy_changed');
    const residentTrace = await verifyG09ResidentTraces({ traceRoot: path.join(mutable, 'logs'), identities: runtime.identities, manifest, description: pre.description });
    const audit = await auditTraceFiles(path.join(mutable, 'logs'), [`/release/${manifest.web.appRoot}/.next/cache`]);
    await deadline(fixtureProcess.done, 30000);
    requireThat(!fixtureProcess.failed && fixtureProcess.code === 0, 'g09_fixture_failed');
    report.runtime = runtime; report.residentTrace = residentTrace; report.audit = audit; report.status = 'PASS';
  } catch { failure ??= new Error('g09_acceptance_failed'); }
  finally {
    clearInterval(timer);
    process.removeListener('SIGTERM', interrupted); process.removeListener('SIGINT', interrupted);
    await stop(sandbox, 5).catch(error => { failure ??= error; });
    if (fixtureProcess) {
      // No command writer may remain when reconstructing the acknowledged history.
      const history = sandbox && !sandbox.groupGone
        ? { status: 'unknown', frozen: true }
        : await recoverG09ControlHistory(fixture, runId, { uid });
      report.controlRecovery = { status: history.status, frozen: history.frozen };
      if (['unknown', 'failed'].includes(history.status)) failure ??= new Error('g09_control_history_unproven');
      if (!fixtureProcess.exited && history.status === 'ready') {
        await fixtureCall(fixture, { ...history.state }, 'abort', uid, gid).catch(() => {
          failure ??= new Error('g09_abort_result_unknown');
          report.controlRecovery = { status: 'unknown', frozen: true };
        });
      }
      // Never kill a live fixture merely because a command result is unknown.
      // Allow its own context timeout and deferred schema cleanup to complete.
      await deadline(fixtureProcess.done, Math.max(1, fixtureDeadline - Date.now())).catch(() => {
        failure ??= new Error('g09_fixture_exit_unproven');
      });
      if (!fixtureProcess.exited) await stop(fixtureProcess, 5).catch(error => { failure ??= error; });
    }
    await fixtureLog?.close();
    const done = await readG09ControlJSON(path.join(fixture, 'done.json'), { uid }).catch(() => null);
    report.fixtureSchemaRemoved = done?.runId === runId && done.ok && done.schemaRemoved === true;
    if (fixtureProcess && !report.fixtureSchemaRemoved) failure ??= new Error('g09_schema_cleanup_unproven');
    try {
      requireThat(same(await runtimeInventory(options.sealed), manifest.artifacts) && await fileHash(path.join(options.sealed, 'manifest.json')) === options.manifestSHA, 'g09_sealed_changed');
      report.sealedUnchanged = true;
    } catch { failure ??= new Error('g09_sealed_changed'); report.sealedUnchanged = false; }
    if (failure) { report.status = 'FAIL'; report.failureCode = 'g09_boundary_or_acceptance_failed'; }
    await writeJSON(path.join(runRoot, 'report.json'), report);
    await rm(path.join(fixture, 'config.json'), { force: true });
    await rm(release, { recursive: true, force: true }); await rm(control, { recursive: true, force: true });
    await rm(goTemporaryPath(runRoot), { recursive: true, force: true });
    await rm(path.join(runRoot, 'fixture-tmp'), { recursive: true, force: true });
    await rm(path.join(runRoot, 'host-fixture.test'), { force: true });
  }
  requireThat(!failure, 'g09_runtime_acceptance_failed');
  return { status: 'ACCEPTED_RUNTIME_PREPARATION_ONLY', report: path.join(runRoot, 'report.json'), capacityVerified: false };
}
function goTemporaryPath(root) { return path.join(root, 'go-tmp'); }

async function mountWorker(filename) {
  const request = await jsonFile(filename);
  requireThat(process.ppid === request.parentPID && await readlink('/proc/self/ns/mnt') !== request.parentMountNamespace &&
    await readlink(`/proc/${process.ppid}/ns/mnt`) === request.parentMountNamespace, 'g09_private_mount_required');
  command('/usr/bin/mount', ['--bind', request.originalSocket, request.stagedSocket]);
  try {
    const child = spawn('/usr/bin/setpriv', ['--reuid', String(request.uid), '--regid', String(request.gid), '--clear-groups', '--pdeathsig', 'KILL', '--', '/usr/bin/bwrap', ...request.argv], { env: tools, stdio: 'ignore' });
    const code = await new Promise(resolve => { child.once('error', () => resolve(1)); child.once('close', resolve); });
    requireThat(code === 0, 'g09_private_runtime_failed');
  } finally { command('/usr/bin/umount', [request.stagedSocket]); }
}
export function parseG09RuntimeOptions(args) {
  const options = {}, names = { '--sealed': 'sealed', '--manifest-sha256': 'manifestSHA', '--source': 'source', '--runner-commit': 'runnerCommit', '--run-root': 'runRoot', '--pg-socket': 'pgSocket', '--budget': 'budget', '--budget-sha256': 'budgetSHA' };
  args = [...args];
  if (args.at(-1) === '--execute') { options.execute = true; args.pop(); }
  requireThat(args.length % 2 === 0, 'g09_explicit_unique_options');
  for (let n = 0; n < args.length; n += 2) { requireThat(names[args[n]] && args[n+1] && !options[names[args[n]]], 'g09_explicit_unique_options'); options[names[args[n]]] = args[n+1]; }
  requireThat(Object.values(names).every(name => options[name]), 'g09_required_options');
  return options;
}
if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try {
    if (process.argv[2] === '--inner' && process.argv.length === 3) await inner();
    else if (process.argv[2] === '--mount-worker' && process.argv.length === 4) await mountWorker(process.argv[3]);
    else console.log(JSON.stringify(await acceptG09Runtime(parseG09RuntimeOptions(process.argv.slice(2))), null, 2));
  } catch { console.error('[g09-runtime] Controlled precondition or acceptance failed; private values are not logged'); process.exitCode = 1; }
}
