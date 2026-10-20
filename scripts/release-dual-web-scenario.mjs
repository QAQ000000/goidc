#!/usr/bin/env node
// Runs only inside the outer controller's private, unprivileged namespace.
// Reports contain identities, status and digests, never credentials or Cookies.
import { readFile, readdir, readlink, realpath, lstat, rename, writeFile } from 'node:fs/promises';
import http from 'node:http';
import net from 'node:net';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { fileHash, sha256 } from './release-candidate-lib.mjs';
import { launch, stop, liveProcessGroup, sharpLoaderProbeCode } from './accept-release-runtime.mjs';
import { controlledRuntimeEnvironment, makeLaunchDescription, resolveLaunchStep } from './release-runtime-builder-lib.mjs';
import { runtimeInventory } from './release-runtime-lib.mjs';
import { buildDualWebPlan } from './release-dual-web-lib.mjs';
import { assertAvailablePort, auditProcesses, jsonFile, pageAssets, assetArtifact, pause, requireThat as check, validateTestDSN, waitFor, writeJSON } from './release-runtime-acceptance-lib.mjs';

const roots = { old: '/releases/old', next: '/releases/new' };
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
const MAX_BODY = 32 * 1024 * 1024;
const MAX_OBSERVATIONS = 4000;
const TRACE = ['-ff', '-yy', '-qq', '-s', '256', '-e', 'trace=%file,%network,fchmod,fchown,ftruncate,fallocate', '-e', 'raw=sendto,sendmsg,sendmmsg,recvfrom,recvmsg,recvmmsg'];
const diagnosticScripts = new Set(['release-dual-web-scenario.mjs', 'release-runtime-acceptance-lib.mjs', 'release-candidate-lib.mjs', 'release-runtime-builder-lib.mjs', 'release-runtime-lib.mjs', 'release-dual-web-lib.mjs', 'release-static-retention-lib.mjs', 'accept-release-runtime.mjs']);
export function safeErrorFrames(error) {
  if (typeof error?.stack !== 'string') return [];
  // Drop the message and all function names. Only fixed controller file names
  // and numeric source locations survive; no arbitrary path or stack text does.
  return error.stack.split('\n').slice(1).flatMap(line => {
    const match = line.match(/(?:\/control\/|\/scripts\/)([A-Za-z0-9-]+\.mjs):(\d+):(\d+)\)?$/);
    return match && diagnosticScripts.has(match[1]) ? [{ script: match[1], line: Number(match[2]), column: Number(match[3]) }] : [];
  }).slice(0, 12);
}
export function safeMappingEvidence(filename, kind = 'unknown') {
  const basename = path.posix.basename(String(filename));
  return { class: ['bundled', 'host', 'tooling', 'nginx', 'lua', 'unknown'].includes(kind) ? kind : 'unknown', basename: /^[A-Za-z0-9._+@-]{1,128}$/.test(basename) ? basename : 'redacted' };
}
export function anchoredBasenameMatch(filename, digest, byBasename) {
  const row = byBasename.get(path.posix.basename(String(filename)));
  if (!row || !row.digests.has(digest)) return null;
  return row.kinds.has('host') ? 'host' : 'tooling';
}

export function responseEvidence(response, { phase, route, purpose } = {}) {
  return { phase, route, purpose, method: response.method, status: response.status, generation: response.headers['x-release-generation'] ?? null,
    started: response.started, ended: response.ended, durationMS: response.ended - response.started, bytes: response.body.length, sha256: sha256(response.body) };
}
export function sessionCookie(headers) {
  const values = headers['set-cookie'];
  check(Array.isArray(values), 'Session Cookie absent');
  const cookies = values.filter(value => /^zjmfgo_session=/.test(value));
  check(cookies.length === 1, 'Exactly one session Cookie required');
  const [cookie, ...attributes] = cookies[0].split(';').map(value => value.trim());
  check(/^zjmfgo_session=[A-Za-z0-9_-]{16,256}$/.test(cookie), 'Invalid session Cookie');
  const names = attributes.map(value => value.split('=')[0].toLowerCase());
  check(new Set(names).size === names.length && attributes.some(value => /^httponly$/i.test(value)) && attributes.some(value => /^path=\/$/i.test(value)) && attributes.some(value => /^samesite=lax$/i.test(value)) && !names.includes('secure') && !names.includes('domain'), 'Session Cookie transport attributes differ');
  return cookie;
}
export function noStore(headers) { return /(?:^|,)\s*no-store\s*(?:,|$)/i.test(headers['cache-control'] ?? ''); }
export function publicRedirect(location, origin) {
  check(typeof location === 'string' && location.length > 0, 'Redirect location absent');
  const target = new URL(location, origin);
  check(target.origin === origin && !target.username && !target.password, 'Public redirect exposes nonpublic origin');
  return target.pathname;
}
export function selectDrainAsset(entries) {
  const selected = entries.filter(row => /\.js$/.test(row.url) && row.bytes >= 65536 && row.bytes <= 131072).sort((a, b) => a.bytes - b.bytes || a.url.localeCompare(b.url))[0];
  check(selected, 'No real sealed 64–128 KiB JavaScript chunk for private drain instrumentation');
  return selected;
}
export function rawAttackTargets(known) {
  return ['/_next/static/../../login', '/_next/static/../../api/v1/site', '/_next/static/%2e%2e/login', '/_next/static/%2fapi',
    '/_next/static/%5cserver.js', '/_next/static/%252e%252e/login', '/_next/static/%25', '/_next/static//missing.js',
    '/_next/static/\\missing.js', '/_next/static/%00x', '/_next/static/%1fx', '/_next/static/%7fx', '/_next/static/\x01x', '/_next/static/\x00x',
    '/_next/static/%', '/_next/static/%zz', '/.env', '/artifact/web/server.js', '/control/request.json', '/mutable/nginx.conf',
    '/releases/old/manifest.json', '/node_modules/next/package.json', '/_next/static/absent-acceptance.js', `${known}/../absent.js`];
}
export function rawRequestBytes(origin, target, { method = 'GET', host = new URL(origin).host } = {}) {
  check(!/[\r\n]/.test(target) && !/[\r\n]/.test(host) && /^[A-Z]+$/.test(method), 'Invalid raw request envelope');
  return Buffer.from(`${method} ${target} HTTP/1.1\r\nHost: ${host}\r\nAccept-Encoding: identity\r\nConnection: close\r\nContent-Length: 0\r\n\r\n`, 'latin1');
}
export function parseRawResponse(data) {
  const end = data.indexOf('\r\n\r\n'); check(end >= 0 && end < 65536, 'Raw response headers absent or oversized');
  const lines = data.subarray(0, end).toString('latin1').split('\r\n'), match = lines.shift().match(/^HTTP\/1\.[01] ([0-9]{3})(?: |$)/);
  check(match, 'Invalid raw response status'); const headers = {};
  for (const line of lines) { const colon = line.indexOf(':'); check(colon > 0, 'Invalid raw response header'); const name = line.slice(0, colon).toLowerCase(); headers[name] = headers[name] ? `${headers[name]}, ${line.slice(colon + 1).trim()}` : line.slice(colon + 1).trim(); }
  return { status: Number(match[1]), headers, body: data.subarray(end + 4) };
}
export function identicalIdentity(a, b) {
  return ['pid', 'ppid', 'group', 'startTicks', 'uid', 'gid', 'exe', 'sha256', 'mountNamespace', 'netNamespace'].every(key => a[key] === b[key]) && same(a.cmdline, b.cmdline);
}
export function traceConnectsAPI(text, api) {
  const port = new URL(api).port;
  return text.split('\n').some(line => /\bconnect\(/.test(line) && line.includes(`sin_port=htons(${port})`) && line.includes('inet_addr("127.0.0.1")') && / = (?:0|-1 EINPROGRESS\b)/.test(line));
}
export function accessUpstream(text, phase, route, upstream) {
  return text.split('\n').some(line => { const parts = line.trim().split(' '); return parts.length === 6 && parts[1] === phase && parts[3] === 'GET' && parts[4] === route && parts[5] === new URL(upstream).host; });
}

function deadline(promise, ms, label) {
  let timer; return Promise.race([promise, new Promise((_, reject) => { timer = setTimeout(() => reject(new Error(label)), ms); })]).finally(() => clearTimeout(timer));
}
function beginHTTP(origin, route, { method = 'GET', headers = {}, body, timeoutMS = 10000 } = {}) {
  const state = { started: Date.now(), ended: null, method, bytes: 0, finished: false, headers: null }, chunks = [];
  let firstResolve, firstReject, request;
  const first = new Promise((resolve, reject) => { firstResolve = resolve; firstReject = reject; });
  first.catch(() => {});
  const done = new Promise((resolve, reject) => {
    const fail = () => { firstReject(new Error('HTTP response failed')); reject(new Error('HTTP response failed')); };
    const url = new URL(origin);
    request = http.request({ hostname: url.hostname, port: url.port, method, path: route, agent: false,
      headers: { Host: url.host, 'Accept-Encoding': 'identity', Connection: 'close', ...headers, ...(body === undefined ? {} : { 'Content-Length': Buffer.byteLength(body) }) } }, response => {
      state.headers = response.headers; state.status = response.statusCode;
      response.on('data', chunk => { state.bytes += chunk.length; if (state.bytes > MAX_BODY) { request.destroy(); return; } chunks.push(chunk); firstResolve(state); });
      response.once('error', fail); response.once('aborted', fail);
      response.once('end', () => { state.finished = true; state.ended = Date.now(); firstResolve(state); resolve({ ...state, body: Buffer.concat(chunks) }); });
    });
    request.once('error', fail); request.setTimeout(timeoutMS, () => request.destroy());
    const absoluteTimer = setTimeout(() => request.destroy(), timeoutMS); request.once('close', () => clearTimeout(absoluteTimer));
    request.end(body);
  });
  done.catch(() => {});
  return { state, first, done, cancel: () => request.destroy() };
}
async function requestHTTP(origin, route, options) { return beginHTTP(origin, route, options).done; }
async function rawHTTP(origin, target, options) {
  const started = Date.now(), url = new URL(origin);
  const data = await new Promise((resolve, reject) => {
    const chunks = []; let bytes = 0;
    const socket = net.createConnection({ host: '127.0.0.1', port: Number(url.port) });
    const timer = setTimeout(() => socket.destroy(new Error('Raw HTTP timeout')), 5000);
    socket.once('connect', () => socket.end(rawRequestBytes(origin, target, options)));
    socket.on('data', chunk => { bytes += chunk.length; if (bytes > 1024 * 1024) socket.destroy(new Error('Raw response too large')); else chunks.push(chunk); });
    socket.once('error', () => reject(new Error('Raw HTTP failed')));
    socket.once('end', () => resolve(Buffer.concat(chunks))); socket.once('close', () => clearTimeout(timer));
  });
  return { ...parseRawResponse(data), started, ended: Date.now(), method: options?.method ?? 'GET' };
}
async function processIdentity(pid) {
  const [raw, status, cmdline, exe, mountNamespace, netNamespace, digest] = await Promise.all([
    readFile(`/proc/${pid}/stat`, 'utf8'), readFile(`/proc/${pid}/status`, 'utf8'), readFile(`/proc/${pid}/cmdline`), readlink(`/proc/${pid}/exe`),
    readlink(`/proc/${pid}/ns/mnt`), readlink(`/proc/${pid}/ns/net`), fileHash(`/proc/${pid}/exe`)]);
  const fields = raw.slice(raw.lastIndexOf(')') + 2).split(' '), uids = status.match(/^Uid:\s+(.*)$/m)?.[1].trim().split(/\s+/).map(Number), gids = status.match(/^Gid:\s+(.*)$/m)?.[1].trim().split(/\s+/).map(Number);
  check(uids?.length === 4 && new Set(uids).size === 1 && gids?.length === 4 && new Set(gids).size === 1, 'Mixed process identity');
  return { pid, ppid: Number(fields[1]), group: Number(fields[2]), startTicks: fields[19], uid: uids[0], gid: gids[0], exe, sha256: digest,
    cmdline: cmdline.toString().split('\0').filter(Boolean), mountNamespace, netNamespace };
}
async function workers(master) {
  const result = [];
  for (const name of (await readdir('/proc')).filter(value => /^\d+$/.test(value))) {
    const row = await processIdentity(Number(name)).catch(() => null);
    if (row && row.ppid === master.pid && row.exe === '/tools/nginx' && row.cmdline.join(' ').includes('worker process')) result.push(row);
  }
  return result;
}
async function loadedMaps(pid, { root, manifest, baseline, tooling }) {
  const allowed = new Map();
  const kindByPath = new Map();
  const allowedByBasename = new Map();
  const allow = async (filename, digest, kind) => {
    if (typeof filename !== 'string' || !/^[a-f0-9]{64}$/.test(digest ?? '')) return;
    allowed.set(filename, digest); kindByPath.set(filename, kind);
    if (kind === 'host' || kind === 'tooling') {
      const basename = path.posix.basename(filename), row = allowedByBasename.get(basename) ?? { digests: new Set(), kinds: new Set() };
      row.digests.add(digest); row.kinds.add(kind); allowedByBasename.set(basename, row);
    }
    const canonical = await realpath(filename).catch(() => null);
    if (canonical) { allowed.set(canonical, digest); kindByPath.set(canonical, kind); }
  };
  for (const row of [...(tooling.hostLibraries ?? []), ...(tooling.libraries ?? []), ...(tooling.luaFiles ?? [])]) { await allow(row.path, row.sha256, row.path.endsWith('.so') ? 'tooling' : 'lua'); if (row.resolved) await allow(row.resolved, row.sha256, row.path.endsWith('.so') ? 'tooling' : 'lua'); }
  for (const row of tooling.hostLibraries ?? []) await allow(row.path, row.sha256, 'host');
  allowed.set('/tools/nginx', tooling.nginxSHA256);
  kindByPath.set('/tools/nginx', 'nginx');
  if (root) for (const row of manifest.artifacts.filter(row => !row.link)) await allow(`${root}/${row.path}`, row.sha256, 'bundled');
  for (const row of baseline?.dependencies ?? []) if (row.provider === 'host') await allow(row.path, row.sha256, 'host');
  const mappings = [...new Set((await readFile(`/proc/${pid}/maps`, 'utf8')).split('\n').map(line => line.match(/^\S+\s+\S+\s+\S+\s+\S+\s+\S+\s+(\/.*)$/)?.[1]).filter(Boolean))], result = [];
  for (const filename of mappings) {
    if (filename === '/dev/zero (deleted)') continue;
    check(!filename.endsWith(' (deleted)'), 'Deleted loaded mapping');
    const actual = await realpath(filename); let expected = allowed.get(filename) ?? allowed.get(actual), kind = kindByPath.get(filename) ?? kindByPath.get(actual) ?? 'unknown';
    if (!expected) {
      const digest = await fileHash(filename), basenameKind = anchoredBasenameMatch(filename, digest, allowedByBasename);
      if (basenameKind) { expected = digest; kind = basenameKind; }
    }
    if (!expected) { const error = new Error('Unanchored loaded object'); error.safeEvidence = safeMappingEvidence(filename, kind); throw error; }
    if (await fileHash(filename) !== expected) { const error = new Error('Modified loaded object'); error.safeEvidence = safeMappingEvidence(filename, kind); throw error; }
    result.push({ path: filename, sha256: expected });
  }
  check(result.length > 0, 'Actual loaded maps absent'); return { pid, objects: result };
}

export async function runInner() {
  let phase = 'bootstrap', fixtureSequence = 0, interrupted = false, observer, observerPromise, bridge;
  const owned = [], bridgeSockets = new Set(), evidence = { checks: [], fixture: [], migrations: [], identities: [], loadedMaps: [], reloads: [], observations: [] };
  const signal = () => { interrupted = true; observer && (observer.running = false); for (const item of owned) { try { process.kill(-item.child.pid, 'SIGTERM'); } catch {} } };
  process.once('SIGTERM', signal); process.once('SIGINT', signal);
  const mark = name => { check(!interrupted, 'Scenario interrupted'); phase = name; };
  const call = async operation => {
    check(!interrupted, 'Scenario interrupted'); const sequence = ++fixtureSequence;
    await writeJSON(`/fixture/command-${sequence}.json`, { operation });
    const result = await waitFor(() => jsonFile(`/fixture/result-${sequence}.json`).catch(() => null), 40, 'bounded fixture response');
    check(result.ok === true && !interrupted, 'Fixture operation failed');
    // Fixture responses are a reviewed metadata protocol. Never copy arbitrary keys.
    const row = { operation, ok: true };
    for (const name of ['migrationSchemaSHA256', 'migrationChecks', 'safeInputs', 'externalChannelsDisabled', 'continuityChecks', 'activeSessions', 'successfulLogins', 'sessionIdentityStable', 'businessUnchanged', 'finalVerified', 'schemaRemoved']) if (Object.hasOwn(result, name)) row[name] = result[name];
    evidence.fixture.push(row); return result;
  };
  const closeBridge = async () => { for (const socket of bridgeSockets) socket.destroy(); if (bridge?.listening) await new Promise(resolve => bridge.close(resolve)); };
  const stopOwned = async () => {
    let failure = false;
    for (const handle of [...owned].reverse()) { try { await stop(handle, 15); } catch { failure = true; } }
    check(!failure && owned.every(handle => handle.exited && handle.groupGone), 'Owned process graceful exit not proven');
  };
  try {
    const request = await jsonFile('/control/request.json'), { plan, tooling } = request;
    check(process.getuid() === request.uid && request.uid > 0 && process.getgid() === request.gid && request.gid > 0, 'Ordinary UID/GID required');
    const mountNamespace = await readlink('/proc/self/ns/mnt'), netNamespace = await readlink('/proc/self/ns/net');
    check(mountNamespace !== request.parentMountNamespace && netNamespace !== request.parentNetNamespace, 'Private namespaces required');
    const interfaces = (await readFile('/proc/net/dev', 'utf8')).split('\n').slice(2).filter(line => line.includes(':')).map(line => line.split(':')[0].trim()).sort();
    check(same(interfaces, ['lo']), 'Only loopback interface permitted');
    check(Array.isArray(request.hiddenSources) && request.hiddenSources.length > 0, 'Source visibility boundaries absent');
    for (const hidden of [...request.hiddenSources, '/usr/local/btgo/bin/go', '/usr/bin/go', '/usr/bin/npm', '/usr/bin/npx', '/usr/local/bin/npm']) check(!(await lstat(hidden).catch(error => { if (error.code === 'ENOENT') return null; throw error; })), 'Source/toolchain visible');
    check(tooling?.nginxSHA256 === request.nginxSHA256 && await fileHash('/tools/nginx') === request.nginxSHA256 && await fileHash('/usr/bin/strace') === tooling.straceSHA256, 'Tool executable anchor mismatch');
    check(Array.isArray(tooling.hostLibraries) && tooling.hostLibraries.length && Array.isArray(tooling.luaFiles), 'Complete tooling baseline required');
    for (const row of [...tooling.hostLibraries, ...tooling.libraries, ...tooling.luaFiles]) {
      const info = await lstat(row.path); check(info.isFile() && await fileHash(row.path) === row.sha256, 'Tool dependency anchor mismatch');
      if (row.bytes !== undefined) check(info.size === row.bytes, 'Tool Lua size mismatch');
      if (row.resolved) check(await fileHash(row.resolved) === row.sha256, 'Tool resolved provider mismatch');
    }
    if (tooling.loaderCacheSHA256) check(await fileHash('/etc/ld.so.cache') === tooling.loaderCacheSHA256, 'Loader cache anchor mismatch');
    const releases = {};
    const mountRows = (await readFile('/proc/self/mountinfo', 'utf8')).split('\n').map(line => line.split(' '));
    for (const key of ['old', 'next']) {
      const root = roots[key], manifest = await jsonFile(`${root}/manifest.json`);
      check(await fileHash(`${root}/manifest.json`) === plan[key].manifestSHA256 && manifest.source.commit === plan[key].commit, 'Manifest anchor mismatch');
      check(mountRows.some(row => row[4] === root && row[5].split(',').includes('ro')), 'Readonly release mount missing');
      const description = await jsonFile(`${root}/${manifest.launch.path}`);
      check(await fileHash(`${root}/${manifest.launch.path}`) === manifest.artifacts.find(row => row.path === manifest.launch.path && !row.link)?.sha256, 'Launch artifact mismatch');
      const expected = makeLaunchDescription({ web: manifest.web, binaries: manifest.binaries, runtime: manifest.runtime, webOrigin: plan[key].web, publicOrigin: plan.publicOrigin, runtimeUser: 'www' });
      check(same(description, expected), 'Complete declared launch contract differs');
      check(same(await runtimeInventory(root), manifest.artifacts), 'Initial runtime inventory mismatch');
      const env = await controlledRuntimeEnvironment(description, request.fixturePaths.config, { uid: request.uid });
      const database = validateTestDSN(env.DATABASE_URL, request.schema);
      for (const row of request.hostBaseline[key].ignoredSearchPaths ?? []) check(!(await lstat(row.path).catch(error => { if (error.code === 'ENOENT') return null; throw error; })), 'Unreviewed ELF search directory visible');
      const steps = Object.fromEntries(description.steps.map(step => [step.name, step]));
      for (const name of ['migrate', 'api', 'web']) await resolveLaunchStep(root, steps[name]);
      releases[key] = { root, manifest, description, env, database, steps };
    }
    check(same(buildDualWebPlan(Object.fromEntries(Object.entries(releases).map(([key, row]) => [key, { root: row.root, manifest: row.manifest, description: row.description, manifestSHA256: plan[key].manifestSHA256 }]))), plan), 'Plan differs from actual declared packages');
    check(releases.old.env.DATABASE_URL === releases.next.env.DATABASE_URL && request.pgSocket === `/pg/.s.PGSQL.${releases.old.database.port}` && (await lstat(request.pgSocket)).isSocket(), 'Exact shared-schema database bridge required');
    evidence.isolation = { uid: process.getuid(), gid: process.getgid(), netNamespace, networkNamespace: netNamespace, mountNamespace, pidNamespace: await readlink('/proc/self/ns/pid'), loopbackOnly: true, sourceHidden: true, toolchainHidden: true, readOnlyReleases: true };
    await writeJSON('/mutable/isolation.json', evidence.isolation);
    bridge = net.createServer(client => {
      const upstream = net.createConnection({ path: request.pgSocket });
      for (const socket of [client, upstream]) { bridgeSockets.add(socket); socket.once('close', () => bridgeSockets.delete(socket)); }
      client.pipe(upstream); upstream.pipe(client); upstream.once('error', () => client.destroy()); client.once('error', () => upstream.destroy());
    });
    await new Promise((resolve, reject) => { bridge.once('error', reject); bridge.listen(releases.old.database.port, '127.0.0.1', resolve); });
    function service(key, name, step) {
      check(!interrupted, 'Scenario interrupted'); const row = releases[key];
      const handle = launch('/usr/bin/strace', [...TRACE, '-o', `/mutable/logs/${key}-${name}.trace`, `${row.root}/${step.executable}`, ...(step.args ?? [])],
        { cwd: `${row.root}/${step.cwd}`, env: { ...row.env, HOME: `/mutable/${key}/home`, TMPDIR: `/mutable/${key}/tmp`, NEXT_TELEMETRY_DISABLED: '1' } });
      owned.push(handle); return handle;
    }
    for (const key of ['old', 'next']) {
      mark(`${key}-native-loader`); const row = releases[key];
      if (row.manifest.artifacts.some(item => item.path.endsWith('.node') && !item.link)) {
        const code = sharpLoaderProbeCode.replaceAll('/release/', `${row.root}/`).replace("'/release'", JSON.stringify(row.root)).replace('.hostBaseline;', `.hostBaseline.${key};`).replace('/mutable/native-loader.json', `/mutable/${key}/native-loader.json`);
        const handle = service(key, 'native-loader', { executable: row.manifest.runtime.node.path, cwd: row.manifest.web.appRoot, args: ['-e', code] });
        await deadline(handle.done, 30000, 'Native loader deadline'); check(!handle.failed && handle.code === 0, 'Native loader proof failed');
        const result = await jsonFile(`/mutable/${key}/native-loader.json`); check(result.ok && result.matchesStaticResolution && result.uid === request.uid && result.gid === request.gid, 'Native loader evidence invalid');
        evidence.checks.push({ key, nativeLoader: result });
      }
      for (const number of [1, 2]) {
        mark(`${key}-migrate-${number}`); const handle = service(key, `migrate-${number}`, row.steps.migrate);
        await deadline(handle.done, 120000, 'Migration deadline'); check(!handle.failed && handle.code === 0 && !(await liveProcessGroup(handle.child.pid)).length, 'Package migration failed');
        const result = await call('verify-migrations'); evidence.migrations.push({ key, number, exit: 0, migrationChecks: result.migrationChecks, migrationSchemaSHA256: result.migrationSchemaSHA256 });
      }
    }
    mark('prepare'); const prepared = await call('prepare');
    check(prepared.safeInputs === true && prepared.externalChannelsDisabled === true && typeof prepared.siteMarker === 'string' && /^DualWebSSR_release_runtime_[a-f0-9]{24,64}$/.test(prepared.siteMarker), 'Fixture safe inputs or SSR marker absent');
    const credentialInfo = await lstat(request.fixturePaths.credentials);
    check(credentialInfo.isFile() && (credentialInfo.mode & 0o777) === 0o600 && credentialInfo.uid === request.uid, 'Private credential file required');
    let credentials = await jsonFile(request.fixturePaths.credentials);
    const internal = {}; evidence.internal = internal;
    async function internalPages(key) {
      const row = releases[key], origin = plan[key].web, pages = [], assets = new Map();
      const result = { pages, assets: [], proxy: null }; internal[key] = result;
      for (const route of ['/', '/login', '/admin-login']) {
        const routeName = route === '/' ? 'home' : route.slice(1);
        mark(`${key}-page-${routeName}-html`);
        const response = await requestHTTP(origin, route);
        evidence.checks.push(responseEvidence(response, { phase, route, purpose: 'internal-html' }));
        check(response.status === 200, 'Internal page HTTP status differs');
        const referenced = pageAssets(response.body.toString('utf8'), origin);
        pages.push({ route, status: response.status, sha256: sha256(response.body), assets: referenced });
        for (const resource of referenced) {
          if (assets.has(resource)) continue;
          mark(`${key}-page-${routeName}-asset`);
          const artifact = assetArtifact(resource, row.manifest.web.appRoot), record = row.manifest.artifacts.find(item => item.path === artifact);
          check(record && !record.link && record.bytes > 0, 'Referenced asset absent from candidate inventory');
          const data = await requestHTTP(origin, resource);
          evidence.checks.push(responseEvidence(data, { phase, route: resource, purpose: 'internal-page-asset' }));
          check(data.status === 200 && data.body.length === record.bytes && sha256(data.body) === record.sha256 && await fileHash(`${row.root}/${artifact}`) === record.sha256, 'Internal HTTP asset differs from candidate');
          const item = { resource, artifact, bytes: data.body.length, sha256: sha256(data.body) }; assets.set(resource, item); result.assets.push(item);
        }
      }
      mark(`${key}-site-proxy`);
      const route = '/api/v1/site', direct = await requestHTTP(plan[key].api, route), proxy = await requestHTTP(origin, route);
      for (const [purpose, response] of [['internal-site-direct', direct], ['internal-site-proxy', proxy]]) evidence.checks.push(responseEvidence(response, { phase, route, purpose }));
      check(direct.status === 200 && proxy.status === 200 && direct.body.equals(proxy.body), 'Web site proxy differs from direct API');
      result.proxy = { route, sha256: sha256(direct.body), equal: true }; return result;
    }
    for (const key of ['old', 'next']) {
      const row = releases[key]; mark(`${key}-api-ready`); row.apiHandle = service(key, 'api', row.steps.api);
      await waitFor(async () => { check(!row.apiHandle.exited && !interrupted, 'API exited'); return requestHTTP(plan[key].api, '/readyz', { timeoutMS: 500 }).then(r => r.status === 200).catch(() => false); }, row.description.timeouts.readySeconds, 'API ready');
      mark(`${key}-web-ready`); row.webHandle = service(key, 'web', row.steps.web);
      await waitFor(async () => { check(!row.webHandle.exited && !interrupted, 'Web exited'); return requestHTTP(plan[key].web, '/', { timeoutMS: 1000 }).then(r => r.status === 200).catch(() => false); }, row.description.timeouts.readySeconds, 'Web ready');
      mark(`${key}-verify-pages`); await internalPages(key);
      mark(`${key}-ssr-marker`); const html = await requestHTTP(plan[key].web, '/');
      evidence.checks.push(responseEvidence(html, { phase, route: '/', purpose: 'internal-ssr-marker' }));
      check(html.status === 200 && noStore(html.headers) && html.body.toString().includes(`<title>${prepared.siteMarker}</title>`), 'SSR used fallback site metadata or allows storage');
      for (const route of ['/login', '/admin-login', '/api/v1/site']) {
        mark(`${key}-cache-${route.slice(1).replaceAll('/', '-')}`);
        const response = await requestHTTP(plan[key].web, route);
        evidence.checks.push(responseEvidence(response, { phase, route, purpose: 'internal-dynamic-no-store' }));
        check(response.status === 200 && noStore(response.headers), 'Internal Web dynamic cache policy differs');
      }
      mark(`${key}-process-identities`);
      const ids = await auditProcesses(row.root, ['api', 'web'].map(name => ({ ...row.steps[name], name: `${key}-${name}`, traceName: `${key}-${name}` })), '/mutable/logs');
      check(ids.length === 2 && ['api', 'web'].every(name => ids.some(item => item.name === `${key}-${name}`)), 'Actual package service identities absent');
      for (const id of ids) {
        mark(`${id.name}-process-identity`);
        const actual = await processIdentity(id.pid); check(actual.uid === request.uid && actual.gid === request.gid && actual.netNamespace === netNamespace && actual.mountNamespace === mountNamespace, 'Service process escaped identity/namespace');
        check(id.sha256 === row.manifest.artifacts.find(item => item.path === id.executable)?.sha256, 'Service executable inventory differs');
        evidence.identities.push({ ...id, identity: actual }); mark(`${id.name}-loaded-maps`);
        evidence.loadedMaps.push(await loadedMaps(id.pid, { root: row.root, manifest: row.manifest, baseline: request.hostBaseline[key], tooling }));
      }
    }
    evidence.internal = internal;
    const unique = {};
    for (const key of ['old', 'next']) {
      const referenced = new Set(internal[key].pages.flatMap(row => row.assets).map(value => new URL(value, plan.publicOrigin).pathname));
      unique[key] = plan.retention.entries.find(row => plan.uniqueURLs[key].includes(row.url) && referenced.has(row.url));
      check(unique[key], 'Each package needs a real HTML-referenced unique resource');
    }
    evidence.uniqueResources = unique;
    const drainAsset = selectDrainAsset(plan.retention.entries), observations = evidence.observations;
    async function record(origin, route, options = {}, purpose = 'check') {
      const result = await requestHTTP(origin, route, options); evidence.checks.push(responseEvidence(result, { phase, route, purpose }));
      if (!route.startsWith('/_next/static/')) {
        check(noStore(result.headers), 'Dynamic response must prohibit storage');
        if (origin === plan.publicOrigin && [301, 302, 303, 307, 308].includes(result.status)) publicRedirect(result.headers.location, plan.publicOrigin);
      }
      check(evidence.checks.length < 3000, 'Bounded check evidence exceeded'); return result;
    }
    function goodAsset(response, entry, method = 'GET', generation) {
      check(response.status === 200 && response.headers['content-type']?.split(';')[0] === entry.contentType && response.headers['x-content-type-options'] === 'nosniff' && response.headers['cache-control'] === 'public, max-age=300, immutable', 'Static status/MIME/cache/nosniff mismatch');
      check(Number(response.headers['content-length']) === entry.bytes && (method === 'HEAD' ? response.body.length === 0 : response.body.length === entry.bytes && sha256(response.body) === entry.sha256), 'Static exact bytes/HEAD mismatch');
      if (generation) check(response.headers['x-release-generation'] === generation, 'Static generation differs');
    }
    async function activateConfig(name) {
      check(await fileHash(`/control/${name}.conf`) === request.configHashes[name], 'Readonly phase config anchor differs');
      const bytes = await readFile(`/control/${name}.conf`), pending = `/mutable/nginx-${name}.pending.conf`;
      await writeFile(pending, bytes, { flag: 'wx', mode: 0o600 });
      const syntax = launch('/tools/nginx', ['-p', '/mutable/', '-c', pending, '-e', '/mutable/bootstrap.log', '-t'], { env: { PATH: '/usr/bin:/bin', LANG: 'C.UTF-8' }, cwd: '/mutable' });
      owned.push(syntax); await deadline(syntax.done, 5000, 'Nginx syntax deadline'); check(!syntax.failed && syntax.code === 0, 'Real Nginx config syntax failed');
      check(await fileHash(pending) === request.configHashes[name], 'Tested config changed'); await rename(pending, '/mutable/nginx.conf');
      evidence.checks.push({ phase: name, configSHA256: request.configHashes[name], realNginxSyntax: true });
    }
    mark('old'); await activateConfig('old');
    const nginx = launch('/tools/nginx', ['-p', '/mutable/', '-c', '/mutable/nginx.conf', '-e', '/mutable/bootstrap.log'], { env: { PATH: '/usr/bin:/bin', LANG: 'C.UTF-8' }, cwd: '/mutable' }); owned.push(nginx);
    await waitFor(async () => { check(!nginx.exited && !interrupted, 'Nginx master exited'); return requestHTTP(plan.publicOrigin, '/').then(r => r.status === 200 && r.headers['x-release-generation'] === 'old').catch(() => false); }, 10, 'public old ready');
    await pause(150); const master = await processIdentity(nginx.child.pid);
    check(master.exe === '/tools/nginx' && master.sha256 === request.nginxSHA256 && master.uid === request.uid && master.gid === request.gid && master.group === nginx.child.pid && master.netNamespace === netNamespace && master.mountNamespace === mountNamespace && master.cmdline.join(' ').includes('nginx: master process') && Number((await readFile('/mutable/nginx.pid', 'utf8')).trim()) === master.pid, 'Owned Nginx master identity mismatch');
    evidence.nginx = { master, tooling, privateDrainInstrumentation: { header: 'X-Release-Drain: 1', bytesPerSecond: 16384, productionFeature: false, resource: drainAsset.url, bytes: drainAsset.bytes } };
    evidence.loadedMaps.push(await loadedMaps(master.pid, { tooling }));
    async function authenticatedMaster() {
      const actual = await processIdentity(master.pid); check(identicalIdentity(master, actual) && Number((await readFile('/mutable/nginx.pid', 'utf8')).trim()) === master.pid, 'Refusing signal to changed Nginx master'); return actual;
    }
    async function validWorkers() {
      const rows = await workers(master);
      for (const row of rows) check(row.uid === request.uid && row.gid === request.gid && row.sha256 === master.sha256 && row.group === master.group && row.netNamespace === netNamespace && row.mountNamespace === mountNamespace, 'Nginx worker identity differs');
      return rows;
    }
    check((await validWorkers()).length === 1, 'Exactly one active Nginx worker expected');
    const actor = { customer: { id: prepared.customerID, role: 'customer' }, admin: { id: prepared.adminID, role: 'super_admin' } };
    for (const name of ['customer', 'admin']) {
      check(Number.isSafeInteger(actor[name].id) && actor[name].id > 0 && typeof credentials[name]?.email === 'string' && typeof credentials[name]?.password === 'string', 'Fixture actor invalid');
      const login = await record(plan.publicOrigin, '/api/v1/auth/login', { method: 'POST', headers: { Origin: plan.publicOrigin, 'X-Requested-With': 'zjmfgo', 'Content-Type': 'application/json' }, body: JSON.stringify({ identifier: credentials[name].email, password: credentials[name].password }) }, `${name}-single-login`);
      check(login.status === 200, 'Real actor login failed'); actor[name].cookie = sessionCookie(login.headers);
    }
    credentials = null;
    check(actor.customer.cookie !== actor.admin.cookie, 'Distinct actor sessions required');
    async function me(origin, person, expectedStatus = 200) {
      const result = await record(origin, '/api/v1/me', { headers: { Cookie: person.cookie } }, 'session-identity');
      check(result.status === expectedStatus, 'Session API status differs');
      if (expectedStatus === 200) { const data = JSON.parse(result.body).data; check(data.id === person.id && data.role === person.role, 'Session identity/role changed'); }
      return result;
    }
    async function verifyStaticAndPages(name) {
      for (const entry of plan.retention.entries) goodAsset(await record(plan.publicOrigin, entry.url, {}, 'retained-union'), entry, 'GET', name);
      for (const key of ['old', 'next']) {
        const entry = unique[key]; goodAsset(await record(plan.publicOrigin, entry.url, { method: 'HEAD' }, 'static-head'), entry, 'HEAD', name);
        goodAsset(await record(plan.publicOrigin, `${entry.url}?acceptance=1`, {}, 'static-query'), entry, 'GET', name);
        const normal = await requestHTTP(plan.publicOrigin, entry.url);
        check(normal.headers.etag && normal.headers['last-modified'], 'Static validators absent');
        const conditional = await record(plan.publicOrigin, entry.url, { headers: { 'If-None-Match': normal.headers.etag } }, 'static-304');
        check(conditional.status === 304 && conditional.body.length === 0 && conditional.headers['x-content-type-options'] === 'nosniff' && conditional.headers['cache-control'] === 'public, max-age=300, immutable', 'Static 304 metadata mismatch');
      }
      const activeKey = name === 'next' ? 'next' : 'old', manifest = releases[activeKey].manifest;
      for (const route of ['/', '/login', '/admin-login']) {
        const result = await record(plan.publicOrigin, route, {}, 'public-html');
        const expectedTitle = route === '/admin-login' ? '<title>登录 - 财务系统</title>' : `<title>${prepared.siteMarker}</title>`;
        check(result.status === 200 && result.headers['x-release-generation'] === name && result.body.toString().includes(expectedTitle), 'Public real SSR HTML/generation differs');
        const assets = pageAssets(result.body.toString(), plan.publicOrigin);
        for (const resource of assets) {
          const record = manifest.artifacts.find(row => row.path === assetArtifact(resource, manifest.web.appRoot));
          check(record && !record.link, 'Public HTML references another package or unknown asset');
          const data = await requestHTTP(plan.publicOrigin, resource); check(data.status === 200 && data.body.length === record.bytes && sha256(data.body) === record.sha256, 'Public referenced asset mismatch');
        }
      }
    }
    async function verifyNegative(name) {
      for (const target of rawAttackTargets(unique.old.url)) {
        const result = await rawHTTP(plan.publicOrigin, target);
        check(result.status >= 400 && result.status < 500, 'Raw static/path attack accepted');
        // Core HTTP parser errors occur before location/header filters. Record
        // these separately; application and guard errors must carry no-store.
        if (result.status !== 400) check(result.headers['cache-control'] === 'no-store' && result.headers['x-content-type-options'] === 'nosniff', 'Negative response cache/nosniff differs');
        check(!result.body.equals((await readFile(`${roots.old}/${unique.old.path}`))) && sha256(result.body) !== unique.old.sha256 && sha256(result.body) !== unique.next.sha256, 'Negative request leaked sealed asset');
        evidence.checks.push({ ...responseEvidence(result, { phase: name, route: JSON.stringify(target), purpose: 'raw-negative' }), coreParserError: result.status === 400 });
      }
      for (const method of ['POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS', 'TRACE']) {
        const result = await rawHTTP(plan.publicOrigin, unique.old.url, { method });
        check(result.status === 405 && result.headers['cache-control'] === 'no-store', 'Unsafe static method accepted');
        evidence.checks.push(responseEvidence(result, { phase: name, route: unique.old.url, purpose: 'unsafe-method' }));
      }
      const badHost = await rawHTTP(plan.publicOrigin, '/', { host: 'invalid.example' }); check(badHost.status === 421 && badHost.headers['cache-control'] === 'no-store', 'Bad Host accepted');
      evidence.checks.push(responseEvidence(badHost, { phase: name, route: '/', purpose: 'bad-host' }));
    }
    async function verifyPhase(name) {
      mark(name); const key = name === 'next' ? 'next' : 'old', active = plan[key];
      const logOffset = (await readFile('/mutable/access.log')).length;
      for (const who of ['customer', 'admin']) {
        await me(plan.publicOrigin, actor[who]);
        const permission = await record(plan.publicOrigin, '/api/v1/admin/site-settings', { headers: { Cookie: actor[who].cookie } }, `${who}-permission`);
        check(permission.status === (who === 'customer' ? 403 : 200), 'Admin permission boundary differs');
        const route = who === 'customer' ? '/console/account' : '/admin';
        const ssr = await record(plan.publicOrigin, route, { headers: { Cookie: actor[who].cookie } }, `${who}-protected-ssr`);
        check(ssr.status === 200 && ssr.body.toString().includes(`<title>${prepared.siteMarker}</title>`), 'Authenticated SSR failed or fallback');
      }
      const deniedAdmin = await record(plan.publicOrigin, '/admin', { headers: { Cookie: actor.customer.cookie } }, 'customer-admin-rejected');
      check([302, 303, 307, 308].includes(deniedAdmin.status) && new URL(deniedAdmin.headers.location, plan.publicOrigin).pathname === '/', 'Customer admin SSR permission bypass');
      for (const invalid of [undefined, 'zjmfgo_session=invalid_acceptance_cookie']) {
        const headers = invalid ? { Cookie: invalid } : {};
        const unauth = await record(plan.publicOrigin, '/api/v1/me', { headers }, 'anonymous-invalid-api'); check(unauth.status === 401, 'Anonymous/invalid API accepted');
        for (const [route, destination] of [['/console/account', '/login'], ['/admin', '/admin-login']]) {
          const denied = await record(plan.publicOrigin, route, { headers }, 'anonymous-invalid-ssr');
          check([302, 303, 307, 308].includes(denied.status) && new URL(denied.headers.location, plan.publicOrigin).pathname === destination, 'Anonymous/invalid SSR accepted');
        }
      }
      for (const who of ['customer', 'admin']) for (const headers of [
        { Origin: 'http://invalid.example', 'X-Requested-With': 'zjmfgo' }, { Origin: plan.publicOrigin }, { Origin: plan.publicOrigin, 'X-Requested-With': 'invalid' },
      ]) {
        const result = await record(plan.publicOrigin, '/api/v1/auth/logout', { method: 'POST', headers: { ...headers, Cookie: actor[who].cookie } }, 'rejected-origin-or-requested-with');
        check(result.status === 403, 'Origin/requested-with failure mutated auth'); await me(plan.publicOrigin, actor[who]);
      }
      const spoofed = { Forwarded: 'for=198.51.100.7;host=invalid.example;proto=https', 'X-Forwarded-Host': 'invalid.example:443', 'X-Forwarded-Proto': 'https', 'X-Forwarded-For': '198.51.100.7', 'X-Real-IP': '198.51.100.7' };
      for (const who of ['customer', 'admin']) {
        const result = await record(plan.publicOrigin, '/api/v1/me', { headers: { ...spoofed, Cookie: actor[who].cookie } }, 'spoofed-forwarding-session');
        const data = JSON.parse(result.body).data;
        check(result.status === 200 && data.id === actor[who].id && data.role === actor[who].role, 'Spoofed forwarding changed session identity');
        const permission = await record(plan.publicOrigin, '/api/v1/admin/site-settings', { headers: { ...spoofed, Cookie: actor[who].cookie } }, 'spoofed-forwarding-permission');
        check(permission.status === (who === 'customer' ? 403 : 200), 'Spoofed forwarding changed permission');
        const route = who === 'customer' ? '/console/account' : '/admin';
        const ssr = await record(plan.publicOrigin, route, { headers: { ...spoofed, Cookie: actor[who].cookie } }, 'spoofed-forwarding-ssr');
        check(ssr.status === 200 && ssr.body.toString().includes(`<title>${prepared.siteMarker}</title>`), 'Spoofed forwarding changed real SSR');
      }
      const spoofedHome = await record(plan.publicOrigin, '/', { headers: spoofed }, 'spoofed-forwarding-home');
      check(spoofedHome.status === 200 && spoofedHome.body.toString().includes(`<title>${prepared.siteMarker}</title>`), 'Spoofed forwarding changed site metadata');
      const spoofedRedirect = await record(plan.publicOrigin, '/admin', { headers: spoofed }, 'spoofed-forwarding-redirect');
      check([302, 303, 307, 308].includes(spoofedRedirect.status) && publicRedirect(spoofedRedirect.headers.location, plan.publicOrigin) === '/admin-login', 'Spoofed forwarding exposed nonpublic redirect');
      await verifyStaticAndPages(name); await verifyNegative(name);
      await waitFor(async () => {
        const log = (await readFile('/mutable/access.log')).subarray(logOffset).toString();
        return accessUpstream(log, name, '/api/v1/me', active.api) && accessUpstream(log, name, '/api/v1/admin/site-settings', active.api) && accessUpstream(log, name, '/console/account', active.web) && accessUpstream(log, name, '/admin', active.web);
      }, 5, 'real public API/Web upstream logs');
      const identity = evidence.identities.find(row => row.name === `${key}-web`);
      const trace = await readFile(`/mutable/logs/${key}-web.trace.${identity.pid}`, 'utf8');
      check(traceConnectsAPI(trace, active.api), 'Actual protected SSR Web to API connection absent');
      evidence.checks.push({ phase: name, actualPublicAPIUpstream: new URL(active.api).host, actualPublicWebUpstream: new URL(active.web).host, actualSSRAPITrace: { pid: identity.pid, destination: active.api, connected: true }, authenticatedSSR: true, permissionNegatives: true });
      await call('verify-session-continuity');
      for (const worker of await validWorkers()) evidence.loadedMaps.push(await loadedMaps(worker.pid, { tooling }));
    }
    await verifyPhase('old');
    function startObserver() {
      observer = { running: true, failure: false };
      observerPromise = (async () => {
        while (observer.running && !interrupted) {
          for (const [route, entry, person] of [[unique.old.url, unique.old], [unique.next.url, unique.next], ['/', null], ['/api/v1/me', null, actor.customer], ['/api/v1/me', null, actor.admin]]) {
            if (!observer.running || interrupted) break;
            try {
              const result = await requestHTTP(plan.publicOrigin, route, person ? { headers: { Cookie: person.cookie } } : {});
              check(result.status === 200 && ['old', 'next', 'restored'].includes(result.headers['x-release-generation']), 'Observer response failed');
              if (entry) goodAsset(result, entry);
              else if (person) { check(noStore(result.headers), 'Observed API permits storage'); const data = JSON.parse(result.body).data; check(data.id === person.id && data.role === person.role, 'Observed session changed'); }
              else check(result.body.toString().includes(`<title>${prepared.siteMarker}</title>`), 'Observed fallback HTML');
              if (!entry) check(noStore(result.headers), 'Observed dynamic response permits storage');
              check(observations.length < MAX_OBSERVATIONS, 'Bounded observations exceeded');
              observations.push(responseEvidence(result, { phase, route, purpose: 'continuous-read-only' }));
            } catch { observer.failure = true; observer.running = false; break; }
          }
          if (observer.running) await pause(100);
        }
      })();
    }
    startObserver();
    async function reload(name) {
      mark(`reload-${name}`); check(!observer.failure, 'Observer failed');
      const before = await validWorkers(); check(before.length === 1, 'Single old worker required before reload');
      const oldWorker = before[0], sourceGeneration = name === 'next' ? 'old' : 'next';
      const drain = beginHTTP(plan.publicOrigin, drainAsset.url, { headers: { 'X-Release-Drain': '1' }, timeoutMS: 14000 });
      try {
        await drain.first; check(!drain.state.finished && drain.state.bytes > 0 && drain.state.bytes < drainAsset.bytes && drain.state.headers['x-release-generation'] === sourceGeneration, 'Drain request must be actually in flight');
        await activateConfig(name); await authenticatedMaster(); const hupAt = Date.now(); process.kill(master.pid, 'SIGHUP');
        const switched = await waitFor(async () => {
          check(!nginx.exited && !observer.failure && !interrupted, 'Reload master/observer failed');
          const response = await requestHTTP(plan.publicOrigin, '/', { timeoutMS: 2000 }).catch(() => null);
          const rows = await validWorkers(), newWorker = rows.find(row => row.pid !== oldWorker.pid && row.startTicks !== oldWorker.startTicks);
          if (response?.status === 200 && response.headers['x-release-generation'] === name && newWorker) return { newWorker, rows, response };
          return null;
        }, 6, 'new generation worker');
        const newGenerationAt = Date.now(), actualOld = await processIdentity(oldWorker.pid).catch(() => null);
        // Nginx changes the retiring worker's title to "shutting down".
        check(actualOld && ['pid', 'startTicks', 'exe', 'sha256', 'uid', 'gid', 'netNamespace', 'mountNamespace'].every(field => actualOld[field] === oldWorker[field]) && !drain.state.finished && drain.state.bytes < drainAsset.bytes, 'Old worker/drain did not survive generation switch');
        const result = await drain.done; goodAsset(result, drainAsset, 'GET', sourceGeneration);
        await waitFor(async () => { const row = await processIdentity(oldWorker.pid).catch(() => null); return !row || row.startTicks !== oldWorker.startTicks; }, 8, 'old worker natural drain exit');
        check(!observer.failure, 'Continuous reload observations failed');
        evidence.reloads.push({ to: name, masterAuthenticatedBeforeHUP: true, oldWorker, newWorker: switched.newWorker, oldWorkerAliveWithInflightRequestAfterNewGeneration: true,
          timeline: { requestStarted: result.started, hupAt, newGenerationAt, originalResponseEnded: result.ended, oldWorkerExitedAt: Date.now() },
          originalResponse: responseEvidence(result, { phase: sourceGeneration, route: drainAsset.url, purpose: 'private-inflight-drain' }), originalBodySHA256: drainAsset.sha256, oldWorkerExitedNaturally: true, masterStayed: identicalIdentity(master, await authenticatedMaster()) });
      } finally { drain.cancel(); }
      await verifyPhase(name);
    }
    await reload('next'); await reload('restored');
    mark('stop-observer'); observer.running = false; await observerPromise; check(!observer.failure && observations.length > 0, 'Continuous observer failed');
    check(evidence.reloads.length === 2 && ['old', 'next', 'restored'].every(name => observations.some(row => row.generation === name)), 'Continuous observations lack all generations');
    for (const reload of evidence.reloads) check(observations.some(row => row.started < reload.timeline.hupAt) && observations.some(row => row.started >= reload.timeline.hupAt && row.ended <= reload.timeline.oldWorkerExitedAt), 'Continuous observations do not bracket reload/drain');
    evidence.continuous = { requests: observations.length, errors: 0, boundedMaximum: MAX_OBSERVATIONS, observerStoppedBeforeLogout: true, bothReloadsCovered: true };
    mark('final'); await call('verify-final');
    for (const who of ['customer', 'admin']) {
      const result = await record(plan.publicOrigin, '/api/v1/auth/logout', { method: 'POST', headers: { Cookie: actor[who].cookie, Origin: plan.publicOrigin, 'X-Requested-With': 'zjmfgo' } }, `${who}-single-logout`);
      check(result.status === 200, 'Real logout failed');
    }
    for (const key of ['old', 'next']) for (const who of ['customer', 'admin']) {
      await me(plan[key].api, actor[who], 401);
      for (const [route, destination] of [['/console/account', '/login'], ['/admin', '/admin-login']]) {
        const result = await record(plan[key].web, route, { headers: { Cookie: actor[who].cookie } }, 'logged-out-direct-web');
        check([302, 303, 307, 308].includes(result.status) && new URL(result.headers.location, plan[key].web).pathname === destination, 'Direct Web accepted logged out session');
      }
    }
    await call('verify-logout');
    mark('stop-owned'); await stopOwned(); await closeBridge();
    for (const origin of [plan.old.api, plan.old.web, plan.next.api, plan.next.web, plan.publicOrigin, `http://127.0.0.1:${releases.old.database.port}`]) await assertAvailablePort(Number(new URL(origin).port));
    mark('fixture-cleanup'); await call('cleanup');
    const done = await waitFor(() => jsonFile('/fixture/done.json').catch(() => null), 20, 'fixture final cleanup'); check(done.ok && done.schemaRemoved, 'Fixture schema removal not proven');
    await writeJSON('/mutable/result.json', { ok: true, allOwnedExited: true, listenersGone: true, fixtureCleaned: true, fixtureSchemaRemoved: true, ...evidence });
  } catch (error) {
    const frames = safeErrorFrames(error);
    if (observer) observer.running = false;
    await observerPromise?.catch(() => {});
    let allOwnedExited = false; try { await stopOwned(); allOwnedExited = true; } catch {}
    await closeBridge().catch(() => {});
    // The outer controller is the sole abort writer after it has stopped the
    // namespace, avoiding command sequence races on asynchronous termination.
    const mapping = error?.safeEvidence && { mapping: error.safeEvidence };
    const failure = { ok: false, phase, reason: interrupted ? 'Scenario interrupted' : 'Controlled acceptance boundary failed', frames, ...(mapping ?? {}), allOwnedExited, listenersGone: false, fixtureCleaned: false, ...evidence };
    await writeJSON('/mutable/failure.json', failure).catch(() => {}); await writeJSON('/mutable/result.json', failure).catch(() => {});
    throw new Error('Controlled dual Web scenario failed');
  } finally { process.removeListener('SIGTERM', signal); process.removeListener('SIGINT', signal); }
}
if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try { check(process.argv.length === 3 && process.argv[2] === '--inner', 'Only isolated --inner mode supported'); await runInner(); }
  catch { console.error('[release-dual-web] Controlled scenario failed; inspect sanitized phase evidence'); process.exitCode = 1; }
}
