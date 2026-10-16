#!/usr/bin/env node
import { chmod, chown, copyFile, cp, lstat, mkdir, readFile, readdir, readlink, realpath, rm, stat, writeFile } from 'node:fs/promises';
import { execFileSync, spawn, spawnSync } from 'node:child_process';
import net from 'node:net';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { fileHash, git, noLinks, sourceProof } from './release-candidate-lib.mjs';
import { controllerFiles, assertAvailablePort, assertSocket, auditProcesses, jsonFile, pause, requireThat, validateTestDSN, verifyPages, waitFor, writeJSON } from './release-runtime-acceptance-lib.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const toolEnv = { PATH: '/usr/bin:/bin', LANG: 'C.UTF-8', GOTOOLCHAIN: 'local', GOENV: 'off', GOPROXY: 'off', GOSUMDB: 'off' };
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
// This acceptance entry is specific to the pinned sharp 0.35.5 package. It
// exercises native libvips from memory and records only loaded-library metadata.
export const sharpLoaderProbeCode = String.raw`
const fs=require('fs'),path=require('path'),crypto=require('crypto');
(async()=>{
 const manifest=JSON.parse(fs.readFileSync('/release/manifest.json'));
 const baseline=JSON.parse(fs.readFileSync('/control/request.json')).hostBaseline;
 for(const ignored of baseline.ignoredSearchPaths)if(fs.existsSync(ignored.path))throw Error('Unreviewed ELF search directory visible');
 const packageFile=path.resolve('node_modules/sharp/package.json');
 if(JSON.parse(fs.readFileSync(packageFile)).version!=='0.35.5')throw Error('Unreviewed sharp version');
 const sharp=require(path.resolve('node_modules/sharp'));
 const png=await sharp({create:{width:1,height:1,channels:4,background:{r:0,g:0,b:0,alpha:1}}}).png().toBuffer();
 if(png.length<8||png.subarray(0,8).toString('hex')!=='89504e470d0a1a0a')throw Error('Native PNG operation failed');
 const maps=[...new Set(fs.readFileSync('/proc/self/maps','utf8').split('\n').map(line=>line.match(/^\S+\s+\S+\s+\S+\s+\S+\s+\S+\s+(\/.*)$/)?.[1]).filter(Boolean).map(value=>fs.realpathSync(value)))];
 const digest=filename=>crypto.createHash('sha256').update(fs.readFileSync(filename)).digest('hex');
 const loaded=[];
 for(const mapped of maps.filter(value=>value.startsWith('/release/'))){
  const relative=mapped.slice('/release/'.length),record=manifest.artifacts.find(row=>row.path===relative);
  if(!record||record.link||digest(mapped)!==record.sha256)throw Error('Loaded package ELF differs from inventory');
  loaded.push({provider:'bundled',path:relative,sha256:record.sha256});
 }
 for(const dependency of baseline.dependencies){
  const target=dependency.provider==='bundled'?'/release/'+dependency.path:dependency.path;
  const expected=fs.statSync(target);
  const mapped=maps.find(filename=>{const actual=fs.statSync(filename);return actual.dev===expected.dev&&actual.ino===expected.ino});
  if(!mapped||digest(mapped)!==dependency.sha256)throw Error('Actual loader provider differs from static dependency resolution');
 }
 for(const native of manifest.artifacts.filter(row=>row.path.endsWith('.node')&&!row.link))if(!loaded.some(row=>row.path===native.path))throw Error('Package native ELF was not loaded');
 const host=[...new Map(baseline.dependencies.filter(row=>row.provider==='host').map(row=>{const expected=fs.statSync(row.path),mappedPath=maps.find(filename=>{const actual=fs.statSync(filename);return actual.dev===expected.dev&&actual.ino===expected.ino});return [row.path,{provider:'host',path:row.path,mappedPath,sha256:row.sha256}]})).values()];
 fs.writeFileSync('/mutable/native-loader.json',JSON.stringify({ok:true,uid:process.getuid(),gid:process.getgid(),entry:'sharp@0.35.5 memory 1x1 PNG',pngBytes:png.length,loaded:[...loaded,...host],matchesStaticResolution:true}),{mode:384,flag:'wx'});
})().catch(()=>process.exitCode=1);
`;
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
export function launch(executable, args, options = {}) {
  const child = spawn(executable, args, { env: toolEnv, stdio: 'ignore', detached: true, ...options });
  const handle = { child, exited: false, code: null, failed: false };
  handle.done = new Promise(resolve => {
    child.once('error', () => { handle.failed = true; handle.exited = true; resolve(); });
    child.once('close', code => { handle.exited = true; handle.code = code; resolve(); });
  });
  return handle;
}
export async function liveProcessGroup(group) {
  const members = [];
  for (const name of (await readdir('/proc')).filter(value => /^\d+$/.test(value))) {
    const raw = await readFile(`/proc/${name}/stat`, 'utf8').catch(() => null); if (!raw) continue;
    const fields = raw.slice(raw.lastIndexOf(')') + 2).split(' ');
    if (Number(fields[2]) === group && !['Z', 'X'].includes(fields[0])) members.push(Number(name));
  }
  return members;
}
export async function stop(handle, seconds = 15) {
  if (!handle) return;
  try { process.kill(-handle.child.pid, 'SIGTERM'); } catch {}
  const graceful = await waitFor(async () => handle.exited && !(await liveProcessGroup(handle.child.pid)).length, seconds, 'owned target process group TERM').then(() => true).catch(() => false);
  if (!graceful) { try { process.kill(-handle.child.pid, 'SIGKILL'); } catch {} await deadline(handle.done, 5000, 'Owned wrapper KILL deadline').catch(() => {}); throw new Error('Owned process exceeded TERM deadline; KILL required'); }
  handle.groupGone = true;
}
async function waitHealthy(handle, check, seconds, label) {
  return waitFor(async () => { requireThat(!handle?.exited, `Owned process exited before ${label}`); return check(); }, seconds, label);
}
export async function copyIndependent(source, target) {
  await cp(source, target, { recursive: true, dereference: false, errorOnExist: true, force: false });
  await chmod(target, 0o755);
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
export async function verifyELFDependencies(manifest, sealed, entries) {
  const inventory = new Map(manifest.artifacts.map(row => [row.path, row]));
  const libraries = command('/usr/sbin/ldconfig', ['-p']).split('\n');
  const evidence = { inspection: 'readelf only; no ELF execution or ldd', objects: [], dependencies: [], ignoredSearchPaths: [] }, visited = new Set();
  async function inspect(binary, inherited = []) {
    const filename = path.join(sealed, binary.path);
    await noLinks(filename);
    const record = inventory.get(binary.path), digest = await fileHash(filename);
    requireThat(record && !record.link && digest === record.sha256 && (!binary.sha256 || digest === binary.sha256), 'ELF inventory digest mismatch');
    const elf = command('/usr/bin/readelf', ['-h', '-l', '-d', filename]);
    requireThat(/Class:\s+ELF64\b/.test(elf) && /Machine:\s+Advanced Micro Devices X86-64\b/.test(elf), 'Unsupported runtime ELF format/architecture');
    const interpreter = elf.match(/Requesting program interpreter: ([^\]]+)/)?.[1] ?? null;
    const needed = [...elf.matchAll(/Shared library: \[([^\]]+)\]/g)].map(row => row[1]).sort();
    if (binary.needed) requireThat(binary.interpreter === interpreter && same(binary.needed, needed), 'Actual ELF baseline differs from manifest');
    if (interpreter) requireThat(['/lib/', '/lib64/', '/usr/lib/', '/usr/lib64/'].some(root => interpreter.startsWith(root)) && (await stat(interpreter)).isFile(), 'BLOCKED: target ELF loader missing or outside mounted library roots');
    const rpath = elf.match(/\(RPATH\)[^\n]*\[([^\]]*)\]/)?.[1], runpath = elf.match(/\(RUNPATH\)[^\n]*\[([^\]]*)\]/)?.[1];
    function directories(value) {
      return (value?.split(':') ?? []).flatMap(item => {
        if (!/^\$(?:ORIGIN|\{ORIGIN\})(?:\/|$)/.test(item)) {
          requireThat(item.startsWith('/') && !item.includes('$'), 'Unsupported relative or variable ELF search path');
          evidence.ignoredSearchPaths.push({ object: binary.path, path: item }); return [];
        }
        const suffix = item.replace(/^\$(?:ORIGIN|\{ORIGIN\})/, '');
        requireThat(!suffix.includes('$') && !suffix.includes('\\'), 'Unsupported ELF search path variable');
        const relative = path.posix.normalize(`${path.posix.dirname(binary.path)}${suffix}`);
        requireThat(relative === 'artifact/web' || relative.startsWith('artifact/web/'), 'ELF search path escapes sealed Web artifact');
        return [relative];
      });
    }
    // DT_RUNPATH is direct-only. DT_RPATH is inherited when RUNPATH is absent.
    const ownRpath = runpath === undefined ? directories(rpath) : [], ownRunpath = directories(runpath);
    const search = [...new Set([...ownRpath, ...inherited, ...ownRunpath])];
    const key = `${binary.path}\0${search.join('\0')}`; if (visited.has(key)) return; visited.add(key);
    evidence.objects.push({ path: binary.path, sha256: digest, interpreter, needed, rpath: rpath ?? null, runpath: runpath ?? null });
    for (const name of needed) {
      requireThat(/^[A-Za-z0-9_.+\-]+$/.test(name), 'Unsupported ELF dependency name');
      const found = [];
      for (const directory of search) {
        const relative = `${directory}/${name}`, target = path.join(sealed, relative), info = await lstat(target).catch(error => { if (error.code === 'ENOENT') return null; throw error; });
        if (!info) continue;
        requireThat(info.isFile() && !info.isSymbolicLink() && inventory.has(relative), 'ELF package dependency is unmapped or special');
        await noLinks(target);
        const input = inventory.get(relative);
        requireThat(!input.link && input.sha256 === await fileHash(target), 'ELF package dependency digest mismatch');
        found.push(relative);
      }
      requireThat(new Set(found).size <= 1, 'Ambiguous same-name package ELF dependency');
      if (found.length) {
        const relative = found[0];
        evidence.dependencies.push({ requestedBy: binary.path, needed: name, provider: 'bundled', path: relative, sha256: inventory.get(relative).sha256 });
        await inspect({ path: relative }, [...new Set([...ownRpath, ...inherited])]);
      } else {
        const cached = libraries.filter(line => line.trim().startsWith(`${name} `) && line.includes('x86-64')).map(line => line.split('=>')[1]?.trim()).filter(Boolean);
        const providers = [];
        for (const cachedPath of cached) {
          const actual = await realpath(cachedPath);
          requireThat(['/lib/', '/lib64/', '/usr/lib/', '/usr/lib64/'].some(root => actual.startsWith(root)), 'Host ELF dependency outside mounted library roots');
          requireThat((await stat(actual)).isFile(), 'Host ELF dependency is not a file');
          providers.push(actual);
        }
        const unique = [...new Set(providers)];
        requireThat(unique.length === 1, 'BLOCKED: missing or ambiguous required target system library');
        evidence.dependencies.push({ requestedBy: binary.path, needed: name, provider: 'host', path: unique[0], sha256: await fileHash(unique[0]) });
      }
    }
  }
  for (const binary of entries) await inspect(binary);
  return evidence;
}
export async function verifyHostBaseline(manifest, sealed) {
  const host = manifest.runtime.host;
  const expected = [...Object.values(manifest.binaries).map(row => row.path), manifest.runtime.node.path, ...manifest.artifacts.filter(row => row.path.endsWith('.node') && !row.link).map(row => row.path)].sort();
  requireThat(Array.isArray(host.elf) && same(host.elf.map(row => row.path).sort(), expected), 'BLOCKED: complete actual ELF host baseline required');
  return verifyELFDependencies(manifest, sealed, host.elf);
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
  for (const name of [...controllerFiles, 'scripts/release-runtime-lib.mjs']) {
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
  requireThat(git(options.source, 'rev-parse', 'HEAD') === manifest.source.commit, 'Candidate fixture checkout HEAD must equal candidate commit');
  const fixtureInput = manifest.source.files.find(row => row.path === 'internal/store/release_runtime_fixture_test.go');
  requireThat(fixtureInput && await fileHash(path.join(options.source, fixtureInput.path)) === fixtureInput.sha256, 'Actual Go fixture differs from selected candidate source');
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
  const hostBaseline = await verifyHostBaseline(manifest, options.sealed);
  requireThat(await fileHash(process.execPath) === await fileHash(path.join(options.sealed, manifest.runtime.node.path)), 'BLOCKED: controller Node must match trusted bundled Node');
  command('/usr/bin/unshare', ['--mount', '--propagation', 'private', '/usr/bin/true']);
  command('/usr/bin/setpriv', ['--reuid', String(uid), '--regid', String(gid), '--clear-groups', '--', '/usr/bin/bwrap', '--unshare-all', '--ro-bind', '/usr', '/usr', '--symlink', 'usr/lib', '/lib', '--symlink', 'usr/lib64', '/lib64', '--uid', String(uid), '--gid', String(gid), '--cap-drop', 'ALL', '--', '/usr/bin/true']);
  return { manifest, runner, description, uid, gid, database, databaseURL, runRoot, budget, available, budgetEvidence, databaseFree, databasePeak, hostBaseline };
}
async function fixtureCall(dir, state, operation, seconds = 30) {
  const sequence = ++state.sequence;
  await writeJSON(path.join(dir, `command-${sequence}.json`), { operation });
  const result = await waitHealthy(state.process, () => jsonFile(path.join(dir, `result-${sequence}.json`)).catch(() => null), seconds, `fixture ${operation}`);
  requireThat(result.ok === true, `Fixture rejected ${operation}`);
  return result;
}
async function superviseInner() {
  let phase = 'bootstrap', phaseSequence = 0;
  const markPhase = async name => { phase = name; await writeJSON(`/mutable/phase-${++phaseSequence}.json`, { phase: name }); };
  await markPhase('bootstrap');
  const { controlledRuntimeEnvironment } = await import('./release-runtime-builder-lib.mjs');
  const request = await jsonFile('/control/request.json'), manifest = await jsonFile('/release/manifest.json'), description = await jsonFile('/release/launch/description.json');
  requireThat(process.getuid() === request.uid && process.getgid() === request.gid, 'Ordinary runtime identity mismatch');
  // The dynamic loader still sees an ELF's literal absolute search entries.
  // Their omission from static resolution is safe only when this namespace
  // proves those build-machine directories are absent before any native load.
  for (const ignored of request.hostBaseline?.ignoredSearchPaths ?? []) requireThat(!(await lstat(ignored.path).catch(error => { if (error.code === 'ENOENT') return null; throw error; })), 'Unreviewed ELF search directory is visible in runtime namespace');
  const env = await controlledRuntimeEnvironment(description, '/fixture/config.json', { uid: request.uid });
  const database = validateTestDSN(env.DATABASE_URL, request.schema);
  await markPhase('namespace');
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
    const handle = launch('/usr/bin/strace', ['-ff', '-yy', '-qq', '-s', '256', '-o', `/mutable/logs/${name}.trace`, '-e', 'trace=%file,%network,fchmod,fchown,ftruncate,fallocate', '-e', 'raw=sendto,sendmsg,sendmmsg,recvfrom,recvmsg,recvmmsg', executable, ...argv], { env: { ...env, HOME: '/mutable/home', TMPDIR: '/mutable/tmp', NEXT_TELEMETRY_DISABLED: '1' }, cwd: `/release/${step.cwd}` });
    owned.push(handle); return handle;
  }
  async function oneMigration(name, step) { const h = service(name, step); await deadline(h.done, 120000, 'Migration deadline exceeded'); requireThat(!h.failed && h.code === 0, 'Same-package migration failed'); await call('verify-migrations'); }
  try {
    const steps = Object.fromEntries(description.steps.map(row => [row.name, row]));
    await markPhase('native-loader');
    let nativeLoader = { status: 'N/A', reason: 'No inventoried native Node ELF' };
    if (manifest.artifacts?.some(row => row.path.endsWith('.node') && !row.link)) {
      const native = service('native-loader', { executable: manifest.runtime.node.path, cwd: manifest.web.appRoot, args: ['-e', sharpLoaderProbeCode] });
      await deadline(native.done, 30000, 'Native loader deadline exceeded');
      requireThat(!native.failed && native.code === 0, 'Actual native loader differs from static ELF resolution');
      nativeLoader = await jsonFile('/mutable/native-loader.json'); requireThat(nativeLoader.ok && nativeLoader.matchesStaticResolution, 'Native loader proof incomplete');
    }
    await markPhase('migrate-first'); await oneMigration('migrate-first', steps.migrate);
    await markPhase('migrate-second'); await oneMigration('migrate-second', steps.migrate);
    await markPhase('fixture-prepare');
    const prepared = await call('prepare'); requireThat(prepared.safeInputs, 'Fixture external-effect boundaries not proven');
    await markPhase('api-ready'); const api = service('api', steps.api);
    await waitHealthy(api, async () => { try { const r = await fetch(steps.api.readyURL, { signal: AbortSignal.timeout(500) }); return r.status === 200; } catch { return false; } }, description.timeouts.readySeconds, 'API readiness');
    await markPhase('web-ready'); const web = service('web', steps.web);
    await waitHealthy(web, async () => { try { const r = await fetch(description.webOrigin, { signal: AbortSignal.timeout(500) }); return r.status === 200; } catch { return false; } }, description.timeouts.readySeconds, 'Web readiness');
    await markPhase('pages-assets'); const pages = await verifyPages(manifest, '/release', description.webOrigin);
    await markPhase('worker-first'); let worker = service('worker-first', steps.worker);
    await waitHealthy(worker, async () => !(await call('verify-first')).pending, description.timeouts.workerSeconds, 'worker simulation completion');
    await markPhase('identities'); const identities = await auditProcesses('/release', [steps.api, steps.web, steps.worker].map((step, i) => ({ ...step, name: ['api', 'web', 'worker'][i], traceName: ['api', 'web', 'worker-first'][i] })), '/mutable/logs');
    requireThat(['api', 'web', 'worker'].every(name => identities.some(row => row.name === name)), 'Actual package runtime identity missing');
    for (const identity of identities) requireThat(manifest.artifacts.find(row => row.path === identity.executable)?.sha256 === identity.sha256, 'Actual running executable differs from sealed artifact');
    await markPhase('worker-stop'); await stop(worker, description.timeouts.stopSeconds);
    await markPhase('worker-restart');
    worker = service('worker-restart', steps.worker);
    // Observe a complete scheduler polling window before checking duplicate execution.
    await pause(6000); requireThat(!worker.exited, 'Restarted worker exited'); await call('verify-restart');
    const restartIdentities = await auditProcesses('/release', [{ ...steps.worker, name: 'worker-restart', traceName: 'worker-restart' }], '/mutable/logs');
    requireThat(restartIdentities.length === 1 && restartIdentities[0].sha256 === manifest.artifacts.find(row => row.path === steps.worker.executable)?.sha256, 'Actual restarted package worker identity missing');
    await markPhase('runtime-stop'); for (const handle of [...owned].reverse()) await stop(handle, description.timeouts.stopSeconds);
    await new Promise(resolve => bridge.close(resolve));
    for (const target of [description.apiInternalURL, description.webOrigin]) await assertAvailablePort(Number(new URL(target).port));
    await markPhase('fixture-finish'); await call('finish'); await waitFor(() => jsonFile('/fixture/done.json').catch(() => null), 15, 'fixture cleanup');
    await markPhase('complete');
    await writeJSON('/mutable/result.json', { ok: true, pages, identities, restartIdentities, nativeLoader, migrations: evidence.filter(row => row.operation === 'verify-migrations'), fixture: evidence, allOwnedExited: owned.every(row => row.exited && row.groupGone), listenersGone: true });
    process.removeListener('SIGTERM', emergencyStop); process.removeListener('SIGINT', emergencyStop);
  } catch (error) {
    for (const handle of [...owned].reverse()) await stop(handle).catch(() => {});
    bridge.close(); await call('abort').catch(() => {});
    await writeJSON('/mutable/failure.json', { ok: false, phase: 'runtime', step: phase, reason: 'Runtime boundary or acceptance check failed' }).catch(() => {});
    throw error;
  }
}
export async function acceptRuntime(options) {
  const pre = await preflight(options);
  if (!options.execute) return { status: 'PREFLIGHT_ONLY', commit: pre.manifest.source.commit, runnerCommit: pre.runner.commit, budget: pre.budgetEvidence, hostBaseline: pre.hostBaseline, fullAcceptanceExecuted: false };
  const { runtimeInventory } = await import('./release-runtime-lib.mjs');
  const { guardReserve } = await import('./release-runtime-builder-lib.mjs');
  const { manifest, runRoot, uid, gid } = pre;
  await mkdir(runRoot, { mode: 0o755 });
  const release = path.join(runRoot, 'release'), control = path.join(runRoot, 'control'), fixture = path.join(runRoot, 'fixture'), mutable = path.join(runRoot, 'mutable');
  let fixtureProcess, sandbox, timer, failure;
  const interrupted = () => { failure ??= new Error('Controller interrupted'); stop(sandbox).catch(() => {}); };
  process.once('SIGTERM', interrupted); process.once('SIGINT', interrupted);
  const report = { status: 'FAIL', commit: manifest.source.commit, runnerCommit: pre.runner.commit, manifestSHA256: options.manifestSHA, hostBaseline: pre.hostBaseline, checks: [] };
  try {
    timer = setInterval(() => Promise.all([
      guardReserve(runRoot, pre.budget, pre.available.bytes),
      guardReserve(path.dirname(options.pgSocket), { ...pre.budget, maxPeakBytes: pre.databasePeak }, pre.databaseFree.bytes),
    ]).catch(error => { failure ??= error; stop(sandbox).catch(() => {}); }), 250);
    for (const directory of [control, fixture, mutable]) await mkdir(directory, { mode: directory === fixture ? 0o700 : 0o755 });
    await copyIndependent(options.sealed, release);
    requireThat(same(await runtimeInventory(release), manifest.artifacts), 'Independent runtime-copy inventory differs');
    await mkdir(path.join(release, manifest.web.appRoot, '.next/cache'), { mode: 0o755, recursive: true });
    for (const name of ['home', 'tmp', 'logs', 'state', 'cache']) { const directory = path.join(mutable, name); await mkdir(directory, { mode: 0o700 }); await chown(directory, uid, gid); }
    await chown(mutable, uid, gid);
    for (const file of controllerFiles) await copyFile(path.join(here, '..', file), path.join(control, path.basename(file)));
    const goEnv = { ...toolEnv, HOME: '/root' }, goTemporary = path.join(runRoot, 'go-tmp'); await mkdir(goTemporary, { mode: 0o700 });
    const fixtureEnv = { ...goEnv, GOCACHE: command('/usr/local/btgo/bin/go', ['env', 'GOCACHE'], goEnv), GOMODCACHE: command('/usr/local/btgo/bin/go', ['env', 'GOMODCACHE'], goEnv), TMPDIR: goTemporary, GOTMPDIR: goTemporary, ZJMFGO_RELEASE_RUNTIME_FIXTURE: '1', ZJMFGO_TEST_DATABASE_URL: pre.databaseURL, ZJMFGO_RELEASE_RUNTIME_FIXTURE_DIR: fixture };
    fixtureProcess = launch('/usr/local/btgo/bin/go', ['test', '-mod=readonly', '-run', '^TestReleaseRuntimeFixture$', '-count=1', '-timeout=15m', './internal/store'], { cwd: options.source, env: fixtureEnv });
    const ready = await waitHealthy(fixtureProcess, () => jsonFile(path.join(fixture, 'schema-ready.json')).catch(() => null), 120, 'fixture schema creation');
    requireThat(ready.ok && ready.pgVersion >= 180000 && ready.pgVersion < 190000 && ready.database === pre.database.database, 'Fixture PG18 identity invalid');
    const config = await jsonFile(path.join(fixture, 'config.json')); validateTestDSN(config.values.DATABASE_URL, ready.schema);
    requireThat(!failure, 'Disk reserve crossed before runtime launch');
    await chown(fixture, uid, gid); await chown(path.join(fixture, 'config.json'), uid, gid);
    await writeJSON(path.join(control, 'request.json'), { uid, gid, schema: ready.schema, pgSocket: `/pg/${path.basename(options.pgSocket)}`, hiddenSource: options.source, parentNetNamespace: await readlink('/proc/self/ns/net'), hostBaseline: pre.hostBaseline });
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
