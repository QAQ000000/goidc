#!/usr/bin/env node
// Isolated static retention acceptance only. Never starts Web/API/worker or
// signals the machine's Nginx. Every request is inside a private net namespace.
import { spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { chmod, chown, copyFile, lstat, mkdir, readFile, readdir, readlink, rename, statfs, writeFile } from 'node:fs/promises';
import http from 'node:http';
import net from 'node:net';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const hash = data => createHash('sha256').update(data).digest('hex');
const digest = async filename => hash(await readFile(filename));
const check = (condition, message) => { if (!condition) throw new Error(message); };
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
const json = async filename => JSON.parse(await readFile(filename, 'utf8'));
const writeJSON = (filename, data) => writeFile(filename, `${JSON.stringify(data, null, 2)}\n`, { mode: 0o600, flag: 'wx' });
const env = { PATH: '/usr/bin:/bin', LANG: 'C.UTF-8' };

async function until(fn, seconds, message) {
  const deadline = Date.now() + seconds * 1000;
  while (Date.now() < deadline) { const result = await fn(); if (result) return result; await delay(25); }
  throw new Error(message);
}
function launch(binary, args, options = {}) {
  const child = spawn(binary, args, { env, stdio: 'ignore', ...options });
  const handle = { child, exited: false, status: null };
  handle.done = new Promise(resolve => {
    child.once('error', () => { handle.exited = true; handle.status = -1; resolve(); });
    child.once('close', code => { handle.exited = true; handle.status = code; resolve(); });
  });
  return handle;
}
async function terminate(handle, group = false) {
  if (!handle || handle.exited) return;
  try { process.kill(group ? -handle.child.pid : handle.child.pid, 'SIGTERM'); } catch {}
  try { await until(() => handle.exited, 10, 'Owned process TERM deadline'); }
  catch (error) { try { process.kill(group ? -handle.child.pid : handle.child.pid, 'SIGKILL'); } catch {} throw error; }
}
async function ownedNginxIdentity(pid, expectedSHA) {
  check(await digest(`/proc/${pid}/exe`) === expectedSHA, 'Owned Nginx executable mismatch');
  const status = await readFile(`/proc/${pid}/status`, 'utf8');
  const uid = Number(status.match(/^Uid:\s+(\d+)/m)?.[1]), gid = Number(status.match(/^Gid:\s+(\d+)/m)?.[1]);
  check(uid === 1000 && gid === 1000, 'Nginx uid/gid mismatch');
  const raw = await readFile(`/proc/${pid}/stat`, 'utf8');
  const fields = raw.slice(raw.lastIndexOf(')') + 2).split(' ');
  return { pid, sha256: expectedSHA, uid, gid, ppid: Number(fields[1]), processGroup: Number(fields[2]), startTicks: fields[19],
    mountNamespace: await readlink(`/proc/${pid}/ns/mnt`), netNamespace: await readlink(`/proc/${pid}/ns/net`),
    cmdline: (await readFile(`/proc/${pid}/cmdline`)).toString().split('\0').filter(Boolean) };
}
async function directoryUsage(root) {
  let bytes = 0, files = 0;
  for (const entry of await readdir(root, { withFileTypes: true })) {
    const name = path.join(root, entry.name), info = await lstat(name).catch(error => { if (error.code === 'ENOENT') return null; throw error; });
    if (!info) continue; // Owned temporary and PID files may disappear on exit.
    check(!info.isSymbolicLink(), 'Unexpected acceptance output link');
    if (info.isDirectory()) { const sub = await directoryUsage(name); bytes += sub.bytes; files += sub.files; }
    else { check(info.isFile(), 'Unexpected acceptance output type'); bytes += info.size; files++; }
  }
  return { bytes, files };
}
function request(resource, method = 'GET', headers = {}) {
  return new Promise((resolve, reject) => {
    const start = performance.now();
    const req = http.request({ host: '127.0.0.1', port: 53188, path: resource, method, headers: { Connection: 'close', ...headers }, agent: false }, res => {
      const chunks = []; let size = 0;
      res.on('data', chunk => { size += chunk.length; if (size > 32 * 1024 * 1024) res.destroy(new Error('Response limit')); else chunks.push(chunk); });
      res.on('error', reject);
      res.on('end', () => resolve({ status: res.statusCode, headers: res.headers, body: Buffer.concat(chunks), durationMs: performance.now() - start }));
    });
    req.setTimeout(5000, () => req.destroy(new Error('Request timeout'))); req.on('error', reject); req.end();
  });
}

async function inner() {
  const input = await json('/control/request.json');
  const report = { status: 'FAIL', scope: 'static only; no Web/API/database or production switch', phases: [], continuous: [], negative: [] };
  let master, observer, observing = false;
  try {
    check(process.getuid() === 1000 && process.getgid() === 1000, 'Ordinary uid/gid required');
    const namespace = await readlink('/proc/self/ns/net'); check(namespace !== input.parentNamespace, 'Private net namespace required');
    const interfaces = await readdir('/sys/class/net').catch(() => []);
    // /sys is deliberately absent. /proc/net/dev provides the actual netns view.
    const devices = (await readFile('/proc/net/dev', 'utf8')).split('\n').filter(line => line.includes(':')).map(line => line.split(':')[0].trim());
    check(devices.length === 1 && devices[0] === 'lo' && interfaces.length === 0, 'Loopback-only namespace required');
    report.isolation = { uid: process.getuid(), gid: process.getgid(), namespace, mountNamespace: await readlink('/proc/self/ns/mnt'), devices, sourceHidden: !(await lstat(input.sourcePath).catch(() => null)) };
    check(report.isolation.mountNamespace !== input.parentMountNamespace, 'Private mount namespace required');
    check(report.isolation.sourceHidden, 'Source must be hidden');
    check(await digest('/tools/nginx') === input.nginxSHA256, 'Nginx bytes changed');
    const identities = [];
    async function activate(phase, initial = false) {
      report.step = `activate-${phase}`;
      const startedAt = new Date().toISOString();
      await copyFile(`/control/${phase}.conf`, '/mutable/nginx.next.conf');
      const syntax = launch('/tools/nginx', ['-p', '/mutable/', '-c', '/mutable/nginx.next.conf', '-e', '/mutable/bootstrap.log', '-t']);
      await until(() => syntax.exited, 10, 'Nginx syntax check deadline'); check(syntax.status === 0, 'Nginx syntax check failed');
      await rename('/mutable/nginx.next.conf', '/mutable/nginx.conf');
      if (initial) master = launch('/tools/nginx', ['-p', '/mutable/', '-c', '/mutable/nginx.conf', '-e', '/mutable/bootstrap.log']);
      else {
        check(!master.exited && Number((await readFile('/mutable/nginx.pid', 'utf8')).trim()) === master.child.pid, 'Owned master PID mismatch');
        const identity = await ownedNginxIdentity(master.child.pid, input.nginxSHA256);
        check(identity.startTicks === identities[0].master.startTicks && identity.netNamespace === namespace, 'Refuse signal to foreign Nginx');
        master.child.kill('SIGHUP');
      }
      await until(async () => {
        check(!master.exited, 'Owned Nginx exited');
        return request(input.oldUnique.url).then(r => r.status === 200 && r.headers['x-release-generation'] === phase).catch(() => false);
      }, 10, 'New worker generation not observed');
      const masterIdentity = await ownedNginxIdentity(master.child.pid, input.nginxSHA256);
      check(masterIdentity.netNamespace === namespace && masterIdentity.mountNamespace === report.isolation.mountNamespace, 'Nginx namespace differs from controller');
      const workers = [];
      for (const pid of (await readdir('/proc')).filter(n => /^\d+$/.test(n))) {
        if (Number(pid) === master.child.pid || await readlink(`/proc/${pid}/exe`).catch(() => '') !== '/tools/nginx') continue;
        const row = await ownedNginxIdentity(Number(pid), input.nginxSHA256).catch(error => { if (['ENOENT', 'ESRCH'].includes(error.code)) return null; throw error; });
        if (!row) continue;
        if (row.ppid === master.child.pid) workers.push(row);
      }
      check(workers.length > 0, 'No actual Nginx workers observed');
      if (!initial) check(workers.some(w => !identities.at(-1).workers.some(old => old.pid === w.pid)), 'No new worker identity after reload');
      identities.push({ phase, startedAt, readyAt: new Date().toISOString(), master: masterIdentity, workers });
    }
    const checked = []; report.checked = checked;
    async function verify(entry, phase) {
      const result = await request(entry.url), head = await request(entry.url, 'HEAD');
      check(result.status === 200 && result.body.length === entry.bytes && hash(result.body) === entry.sha256, 'Static HTTP bytes mismatch');
      check(head.status === 200 && head.body.length === 0 && Number(head.headers['content-length']) === entry.bytes, 'HEAD metadata mismatch');
      check(result.headers['x-release-generation'] === phase && head.headers['x-release-generation'] === phase, 'Unexpected worker generation');
      check(result.headers['content-type']?.split(';')[0] === entry.contentType, 'Static MIME mismatch');
      check(result.headers['x-content-type-options'] === 'nosniff' && result.headers['cache-control'] === 'public, max-age=300, immutable' && head.headers['cache-control'] === result.headers['cache-control'], 'Static cache/security headers missing');
      check(!result.headers['content-encoding'] && !head.headers['content-encoding'], 'Unexpected compressed bytes');
      checked.push({ phase, url: entry.url, bytes: result.body.length, sha256: hash(result.body), contentType: result.headers['content-type'], head: true });
    }
    await activate('old', true);
    for (const entry of input.oldEntries) await verify(entry, 'old');
    const hiddenNew = await request(input.newUnique.url);
    check(hiddenNew.status === 404 && hiddenNew.headers['cache-control'] === 'no-store', 'New-only resource exposed/cached in old phase');
    report.phases.push({ phase: 'old', entries: input.oldEntries.length });
    observing = true;
    observer = (async () => {
      while (observing) {
        const result = await request(input.oldUnique.url);
        report.continuous.push({ status: result.status, sha256: hash(result.body), generation: result.headers['x-release-generation'], durationMs: result.durationMs });
        check(result.status === 200 && hash(result.body) === input.oldUnique.sha256, 'Old resource interrupted during reload');
        await delay(10);
      }
    })();
    // Keep a rejection handler attached while the main acceptance is active.
    const observed = observer.then(() => null, error => error);
    await delay(40);
    for (const phase of ['union', 'restored']) {
      await activate(phase);
      for (const entry of input.entries) await verify(entry, phase);
      report.phases.push({ phase, entries: input.entries.length });
    }
    const query = await request(`${input.oldUnique.url}?v=old_1&cache=keep`);
    check(query.status === 200 && hash(query.body) === input.oldUnique.sha256, 'Safe query changed resource');
    report.safeQuery = true;
    const base = await request(input.oldUnique.url);
    check(typeof base.headers.etag === 'string', 'ETag missing');
    const conditional = await request(input.oldUnique.url, 'GET', { 'If-None-Match': base.headers.etag });
    check(conditional.status === 304 && conditional.body.length === 0 && conditional.headers['cache-control'] === 'public, max-age=300, immutable' && conditional.headers.etag === base.headers.etag, 'Conditional response invalid');
    const stale = await request(input.oldUnique.url, 'GET', { 'If-None-Match': '"invalid-retention-etag"' });
    check(stale.status === 200 && hash(stale.body) === input.oldUnique.sha256, 'Stale conditional bytes invalid');
    report.conditionals = { notModified: 304, stale: 200 };
    const attacks = [
      '/.env', '/manifest.json', '/server.js', '/api/v1/site', '/', '/_next/static/', '/_next/static/missing.js', '/_next/static/.env', '/_next/static/manifest.json',
      '/_next/static/../server.js', '/_next/static/%2e%2e/server.js', '/_next/static/%252e%252e/server.js',
      input.oldUnique.url.replace('/_next/static/', '/_next/static/./'),
      input.oldUnique.url.replace('/_next/static/', '/_next/static/%2f'),
      input.oldUnique.url.replace('/_next/static/', '/_next/static/%5c'),
      input.oldUnique.url.replace('/_next/static/', '/_next/static//'),
      input.oldUnique.url.replace('/_next/static/', '/_next/static/%2E%2E/'),
      input.oldUnique.url.replace('/_next/static/', '/_next/static/%252f'),
      `${input.oldUnique.url}%00`, `${input.oldUnique.url}/extra`, `${input.oldUnique.url}/`, `${input.oldUnique.url}?x=%0a`, `${input.oldUnique.url}%`,
    ];
    for (const resource of attacks) {
      const result = await request(resource);
      check([400, 404].includes(result.status), 'Unsafe/unknown URI accepted');
      check(result.headers['cache-control'] === 'no-store', 'Unknown resource cached');
      check(!result.body.includes(Buffer.from('/releases/')) && !result.body.includes(Buffer.from('/mutable/')), 'Error disclosed private path');
      report.negative.push({ path: resource, status: result.status });
    }
    for (const method of ['POST', 'PUT', 'DELETE', 'OPTIONS']) {
      const result = await request(input.oldUnique.url, method); check(result.status === 405, 'Unexpected method accepted');
      check(result.headers['cache-control'] === 'no-store', 'Method error cached');
      report.negative.push({ method, status: result.status });
    }
    observing = false; const observerError = await observed; if (observerError) throw observerError;
    check(report.continuous.length >= 3 && ['union', 'restored'].every(g => report.continuous.some(r => r.generation === g)), 'Continuous observation incomplete');
    report.checked = checked;
    report.identities = identities;
    report.master = { pid: master.child.pid, sha256: await digest(`/proc/${master.child.pid}/exe`) };
    const mapped = [...new Set((await readFile(`/proc/${master.child.pid}/maps`, 'utf8')).split('\n').map(line => line.match(/^\S+\s+\S+\s+\S+\s+\S+\s+\S+\s+(\/.*)$/)?.[1]).filter(Boolean))];
    report.toolingMaps = await Promise.all(mapped.map(async filename => ({ path: filename, sha256: await digest(filename) })));
    await terminate(master); check(master.status === 0, 'Nginx did not exit cleanly');
    const live = [];
    for (const name of (await readdir('/proc')).filter(n => /^\d+$/.test(n))) if (await readlink(`/proc/${name}/exe`).catch(() => '') === '/tools/nginx') live.push(Number(name));
    check(live.length === 0, 'Owned Nginx child survived');
    const server = net.createServer(); await new Promise((resolve, reject) => { server.once('error', reject); server.listen(53188, '127.0.0.1', () => server.close(resolve)); });
    report.allOwnedExited = true; report.listenerGone = true; report.status = 'PASS';
  } catch (error) {
    report.reason = 'Static protocol, isolation, process or cleanup check failed';
    throw error;
  } finally {
    observing = false; await observer?.catch(() => {}); await terminate(master);
    await writeJSON('/mutable/result.json', report);
  }
}

export async function acceptStaticRetention(options) {
  const { verifyRuntimeCandidate } = await import('./release-runtime-lib.mjs');
  const { sourceProof, noLinks } = await import('./release-candidate-lib.mjs');
  const { buildStaticRetention, renderStaticRetentionNginx } = await import('./release-static-retention-lib.mjs');
  const runnerSource = path.resolve(here, '..');
  const runner = await sourceProof(runnerSource); check(runner.commit === options.runnerCommit, 'Clean pinned runner required');
  const old = await verifyRuntimeCandidate(options.old, options.oldSHA, options.oldSource);
  const next = await verifyRuntimeCandidate(options.next, options.nextSHA, options.nextSource);
  const plan = buildStaticRetention({ old: { manifest: old, root: '/releases/old' }, next: { manifest: next, root: '/releases/new' } });
  const oldURLs = new Set(plan.oldEntries.map(e => e.url));
  const newUnique = plan.entries.find(e => !oldURLs.has(e.url));
  // Derive old-only identity from original manifests, independent of renderer metadata.
  const nextURLs = new Set(next.artifacts.filter(e => e.path.startsWith(`${next.web.appRoot}/.next/static/`)).map(e => `/_next/static/${e.path.slice(`${next.web.appRoot}/.next/static/`.length)}`));
  const stableOld = plan.oldEntries.find(e => !nextURLs.has(e.url));
  check(stableOld && newUnique, 'Distinct old and new static resources required');
  const root = path.resolve(options.runRoot);
  check(/^\/tmp\/zjmfgo-static-retention-[A-Za-z0-9_-]+$/.test(root), 'Exclusive /tmp static acceptance root required');
  await noLinks('/tmp'); check(!(await lstat(root).catch(() => null)), 'Acceptance output already exists');
  const nginx = '/www/server/nginx/sbin/nginx'; await noLinks(nginx);
  const nginxSHA256 = await digest(nginx);
  const libraries = await readFile('/etc/ld.so.cache');
  const luaFiles = [];
  async function recordLua(directory) {
    for (const name of await readdir(directory)) {
      const filename = path.join(directory, name), info = await lstat(filename);
      check(!info.isSymbolicLink(), 'Unexpected Lua input link');
      if (info.isDirectory()) await recordLua(filename);
      else { check(info.isFile(), 'Unexpected Lua input type'); luaFiles.push({ path: filename, bytes: info.size, sha256: await digest(filename) }); }
    }
  }
  await recordLua('/www/server/nginx/lib/lua');
  const version = spawnSync(nginx, ['-V'], { env, encoding: 'utf8' }); check(version.status === 0, 'Nginx version unavailable');
  const elf = spawnSync('/usr/bin/readelf', ['-l', '-d', nginx], { env, encoding: 'utf8' }); check(elf.status === 0, 'Nginx ELF metadata unavailable');
  const needed = [...elf.stdout.matchAll(/Shared library: \[([^\]]+)\]/g)].map(m => m[1]).sort();
  const preliminary = { scope: 'static retention preparation only', runnerCommit: runner.commit, oldCommit: old.source.commit, nextCommit: next.source.commit, counts: plan.counts, nginxSHA256,
    tooling: { nginxVersion: version.stderr.trim(), needed, loaderCacheSHA256: hash(libraries), luaFiles, luajitSHA256: await digest('/usr/local/lib/libluajit-5.1.so.2') },
    budget: { maxOutputBytes: 32 * 1024 * 1024, minimumFreeBytes: 512 * 1024 * 1024, maximumOutputFiles: 4096 }, sourceProof: { commit: runner.commit, tree: runner.tree } };
  if (!options.execute) return { status: 'PREFLIGHT_ONLY', ...preliminary };
  const available = await statfs('/tmp'); check(available.bavail * available.bsize > 544 * 1024 * 1024, 'Minimum disk reserve unavailable');
  await mkdir(root, { mode: 0o755 });
  const control = path.join(root, 'control'), mutable = path.join(root, 'mutable');
  await mkdir(control, { mode: 0o755 }); await mkdir(mutable, { mode: 0o700 }); await chown(mutable, 1000, 1000);
  await mkdir(path.join(mutable, 'tmp'), { mode: 0o700 }); await chown(path.join(mutable, 'tmp'), 1000, 1000);
  const configHashes = {};
  for (const phase of ['old', 'union', 'restored']) {
    const conf = renderStaticRetentionNginx(plan, { port: 53188, phase, generation: phase });
    await writeFile(path.join(control, `${phase}.conf`), conf, { mode: 0o444, flag: 'wx' }); configHashes[phase] = hash(conf);
  }
  await copyFile(fileURLToPath(import.meta.url), path.join(control, 'controller.mjs')); await chmod(path.join(control, 'controller.mjs'), 0o444);
  await writeJSON(path.join(control, 'request.json'), { oldEntries: plan.oldEntries, entries: plan.entries, oldUnique: stableOld, newUnique, nginxSHA256, sourcePath: runnerSource, parentNamespace: await readlink('/proc/self/ns/net'), parentMountNamespace: await readlink('/proc/self/ns/mnt') });
  await chmod(path.join(control, 'request.json'), 0o444);
  const args = ['--die-with-parent', '--unshare-all', '--clearenv', '--proc', '/proc', '--dev', '/dev', '--bind', path.join(mutable, 'tmp'), '/tmp', '--dir', '/etc', '--dir', '/usr', '--dir', '/tools'];
  for (const p of ['/lib', '/lib64', '/usr/lib', '/usr/lib64', '/etc/passwd', '/etc/group', '/etc/nsswitch.conf', '/etc/hosts', '/etc/ld.so.cache']) args.push('--ro-bind', p, p);
  // Explicit host tooling dependency, not a bundled application dependency.
  args.push('--ro-bind', '/usr/local/lib/libluajit-5.1.so.2', '/usr/local/lib/libluajit-5.1.so.2');
  args.push('--ro-bind', '/www/server/nginx/lib/lua', '/www/server/nginx/lib/lua', '--ro-bind', nginx, '/tools/nginx');
  args.push('--ro-bind', path.join(options.next, next.runtime.node.path), '/tools/node', '--ro-bind', control, '/control', '--bind', mutable, '/mutable');
  for (const [label, source, manifest] of [['old', options.old, old], ['new', options.next, next]]) {
    const relative = `${manifest.web.appRoot}/.next/static`;
    args.push('--ro-bind', path.join(source, relative), `/releases/${label}/${relative}`);
  }
  args.push('--uid', '1000', '--gid', '1000', '--cap-drop', 'ALL', '--chdir', '/mutable', '/tools/node', '/control/controller.mjs', '--inner');
  let child, reserveFailure, interval;
  const report = { status: 'FAIL', ...preliminary, oldManifestSHA256: options.oldSHA, nextManifestSHA256: options.nextSHA, configHashes };
  const stopOnSignal = () => { reserveFailure ??= new Error('Controller interrupted'); terminate(child, true).catch(() => {}); };
  process.once('SIGTERM', stopOnSignal); process.once('SIGINT', stopOnSignal);
  try {
    child = launch('/usr/bin/bwrap', args, { detached: true });
    interval = setInterval(async () => {
      try {
        const now = await statfs(root), usage = await directoryUsage(root);
        check(now.bavail * now.bsize >= 512 * 1024 * 1024, 'Minimum disk reserve crossed');
        check(usage.bytes <= report.budget.maxOutputBytes && usage.files <= report.budget.maximumOutputFiles && now.ffree > 4096, 'Output resource budget crossed');
      }
      catch (error) { reserveFailure ??= error; terminate(child, true).catch(() => {}); }
    }, 250);
    await until(() => child.exited, 120, 'Static acceptance deadline');
    check(!reserveFailure && child.status === 0, 'Isolated Nginx acceptance failed');
    report.runtime = await json(path.join(mutable, 'result.json')); check(report.runtime.status === 'PASS', 'Runtime evidence incomplete');
    check(await digest(nginx) === nginxSHA256 && await digest('/usr/local/lib/libluajit-5.1.so.2') === preliminary.tooling.luajitSHA256, 'Host Nginx tooling changed');
    for (const file of luaFiles) check(await digest(file.path) === file.sha256, 'Lua tooling input changed');
    await verifyRuntimeCandidate(options.old, options.oldSHA, options.oldSource); await verifyRuntimeCandidate(options.next, options.nextSHA, options.nextSource);
    report.sealedUnchanged = true; report.status = 'PASS';
  } finally {
    clearInterval(interval); process.removeListener('SIGTERM', stopOnSignal); process.removeListener('SIGINT', stopOnSignal);
    await terminate(child, true).catch(() => { report.status = 'FAIL'; });
    report.wrapperExited = child?.exited === true;
    await writeJSON(path.join(root, 'report.json'), report);
  }
  check(report.status === 'PASS', 'Static retention failed; inspect sanitized report');
  return { status: 'ACCEPTED_STATIC_PREPARATION_ONLY', report: path.join(root, 'report.json'), counts: plan.counts };
}

function parse(args) {
  const names = { '--old': 'old', '--old-sha256': 'oldSHA', '--old-source': 'oldSource', '--next': 'next', '--next-sha256': 'nextSHA', '--next-source': 'nextSource', '--runner-commit': 'runnerCommit', '--run-root': 'runRoot' };
  const options = {};
  while (args.length) {
    const key = args.shift(); if (key === '--execute') { check(!options.execute, 'Duplicate execute'); options.execute = true; continue; }
    check(names[key] && !options[names[key]] && args[0] && !args[0].startsWith('--'), 'Explicit unique arguments required'); options[names[key]] = args.shift();
  }
  for (const name of Object.values(names)) check(typeof options[name] === 'string', `Required argument ${name}`);
  check(/^[a-f0-9]{40}$/.test(options.runnerCommit) && /^[a-f0-9]{64}$/.test(options.oldSHA) && /^[a-f0-9]{64}$/.test(options.nextSHA), 'Full commit and manifest anchors required');
  for (const name of ['old', 'next', 'oldSource', 'nextSource', 'runRoot']) check(path.isAbsolute(options[name]), 'Absolute input paths required');
  return options;
}
if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try {
    if (process.argv[2] === '--inner' && process.argv.length === 3) await inner();
    else console.log(JSON.stringify(await acceptStaticRetention(parse(process.argv.slice(2)))));
  } catch { console.error('[release-static-retention] Acceptance failed; inspect sanitized phase/report evidence'); process.exitCode = 1; }
}
