import { lstat, mkdir, readFile, readdir, readlink, realpath, rename, rm, writeFile } from 'node:fs/promises';
import net from 'node:net';
import path from 'node:path';
import { fileHash, noLinks, safeRelative, sha256 } from './release-candidate-lib.mjs';

export const controllerFiles = ['scripts/accept-release-runtime.mjs', 'scripts/release-runtime-acceptance-lib.mjs'];
export const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
export function requireThat(condition, message) { if (!condition) throw new Error(message); }
export async function jsonFile(filename) {
  const stat = await lstat(filename);
  requireThat(stat.isFile() && stat.size <= 16 * 1024 * 1024, 'Invalid bounded JSON file');
  try { return JSON.parse(await readFile(filename, 'utf8')); } catch { throw new Error('Invalid acceptance JSON'); }
}
export async function waitFor(check, seconds, label) {
  const until = Date.now() + seconds * 1000;
  while (Date.now() < until) { const result = await check(); if (result) return result; await pause(100); }
  throw new Error(`Timed out: ${label}`);
}
export function validateTestDSN(value, schema) {
  let url;
  try { url = new URL(value); } catch { throw new Error('Dedicated test database URL required'); }
  requireThat(['postgres:', 'postgresql:'].includes(url.protocol) && url.hostname === '127.0.0.1' && /^\d+$/.test(url.port) && Number(url.port) > 1024 && /_test$/.test(url.pathname.slice(1)) && !url.hash, 'Dedicated explicit loopback _test database required');
  requireThat(!url.searchParams.has('host') && !url.searchParams.has('hostaddr') && !url.searchParams.has('port') && !url.searchParams.has('dbname') && !url.searchParams.has('service'), 'Database URL cannot override target identity');
  if (schema !== undefined) {
    requireThat(/^release_runtime_[a-f0-9]{24,64}$/.test(schema), 'Fixture random schema identity required');
    const searchPath = url.searchParams.get('search_path');
    const options = url.searchParams.get('options');
    requireThat(searchPath === schema || options === `-csearch_path=${schema}`, 'Every runtime DSN must select only its dedicated schema');
    requireThat(!(searchPath && options), 'Ambiguous runtime schema options');
  }
  return { port: Number(url.port), database: decodeURIComponent(url.pathname.slice(1)) };
}
export async function assertAvailablePort(port) {
  await new Promise((resolve, reject) => {
    const server = net.createServer();
    server.once('error', () => reject(new Error('Required loopback port unavailable')));
    server.listen(port, '127.0.0.1', () => server.close(resolve));
  });
}
export function pageAssets(html, origin) {
  const assets = new Set();
  // Both HTML attributes and App Router serialized/preload strings carry URLs.
  for (const match of html.matchAll(/(?:src|href)=["']([^"']+)["']|(?:\\?"|')((?:\/|https?:\/\/)[^\s"'<>]+?\.(?:js|css)(?:\?[^\s"'<>]*)?)(?:\\?"|')/g)) {
    const value = (match[1] ?? match[2]).replaceAll('&amp;', '&').replaceAll('\\', '');
    let url; try { url = new URL(value, origin); } catch { continue; }
    if (url.origin === origin && /\.(?:js|css)$/.test(url.pathname)) assets.add(`${url.pathname}${url.search}`);
  }
  requireThat(assets.size > 0, 'Page did not reference real same-origin JS/CSS');
  return [...assets].sort();
}
export function assetArtifact(request, appRoot) {
  const url = new URL(request, 'http://127.0.0.1');
  let name; try { name = decodeURIComponent(url.pathname); } catch { throw new Error('Invalid asset URL'); }
  requireThat(name.startsWith('/_next/static/'), 'Unexpected page JS/CSS asset outside sealed static output');
  return `${appRoot}/.next/static/${safeRelative(name.slice('/_next/static/'.length))}`;
}
export async function getBytes(url, timeoutSeconds = 10) {
  const response = await fetch(url, { redirect: 'manual', headers: { 'accept-encoding': 'identity' }, signal: AbortSignal.timeout(timeoutSeconds * 1000) });
  requireThat(response.status === 200, 'Runtime HTTP request failed');
  const size = Number(response.headers.get('content-length') ?? 0);
  requireThat(size <= 32 * 1024 * 1024, 'Runtime response too large');
  const chunks = []; let bytes = 0;
  for await (const chunk of response.body) { bytes += chunk.length; requireThat(bytes <= 32 * 1024 * 1024, 'Runtime response too large'); chunks.push(chunk); }
  return Buffer.concat(chunks);
}
export async function verifyPages(manifest, releaseRoot, origin) {
  const inventory = new Map(manifest.artifacts.map(row => [row.path, row]));
  const pages = [], assets = new Map();
  for (const route of ['/', '/login', '/admin-login']) {
    const body = await getBytes(`${origin}${route}`), referenced = pageAssets(body.toString('utf8'), origin);
    for (const resource of referenced) {
      if (assets.has(resource)) continue;
      const name = assetArtifact(resource, manifest.web.appRoot), record = inventory.get(name);
      requireThat(record && !record.link && record.bytes > 0, 'Referenced asset absent from candidate inventory');
      const data = await getBytes(`${origin}${resource}`);
      requireThat(data.length === record.bytes && sha256(data) === record.sha256 && await fileHash(path.join(releaseRoot, name)) === record.sha256, 'HTTP asset bytes differ from candidate');
      assets.set(resource, { resource, artifact: name, bytes: data.length, sha256: sha256(data) });
    }
    pages.push({ route, status: 200, sha256: sha256(body), assets: referenced });
  }
  const apiRoute = '/api/v1/site';
  const [direct, proxy] = await Promise.all([getBytes(`${manifest.web.apiInternalURL}${apiRoute}`), getBytes(`${origin}${apiRoute}`)]);
  requireThat(direct.equals(proxy), 'Web proxy differs from direct read-only API response');
  return { pages, assets: [...assets.values()], proxy: { route: apiRoute, sha256: sha256(direct), equal: true } };
}
export async function auditProcesses(releaseRoot, expected, traceRoot) {
  const rows = [];
  for (const name of (await readdir('/proc')).filter(value => /^\d+$/.test(value))) {
    let exe, cwd, cmdline;
    try { [exe, cwd, cmdline] = await Promise.all([readlink(`/proc/${name}/exe`), readlink(`/proc/${name}/cwd`), readFile(`/proc/${name}/cmdline`)]); } catch { continue; }
    const argv = cmdline.toString().split('\0').filter(Boolean);
    // strace -ff remains as the process-group wrapper, while the traced child
    // execs the package binary. Inspect cmdline as well as /proc/exe so the
    // identity check records the actual package PID rather than the wrapper.
    const item = expected.find(row => {
      const expectedExe = path.join(releaseRoot, row.executable);
      const expectedCwd = path.join(releaseRoot, row.cwd);
      const launchIndex = argv.findIndex(value => value === expectedExe || value.endsWith(expectedExe));
      if (cwd !== expectedCwd) return false;
      // A traced child has the package executable as argv[0]. Do not accept a
      // strace wrapper merely because its argv contains the target path.
      return exe === expectedExe || (launchIndex === 0 && path.basename(exe) === path.basename(expectedExe));
    });
    if (!item) continue;
    const expectedExe = path.join(releaseRoot, item.executable), actualHash = await fileHash(`/proc/${name}/exe`);
    requireThat(actualHash === await fileHash(expectedExe), 'Actual runtime executable differs from sealed package binary');
    let args = argv.slice(1), argumentEvidence = 'current-cmdline';
    if (traceRoot && item.traceName) {
      const trace = await readFile(path.join(traceRoot, `${item.traceName}.trace.${name}`), 'utf8').catch(() => '');
      const target = trace.split('\n').find(line => line.startsWith(`execve(${JSON.stringify(expectedExe)}, `) && /\)\s*= 0$/.test(line));
      requireThat(target, 'Actual runtime PID lacks its successful package execve evidence');
      const encoded = target.slice(target.indexOf('['), target.indexOf('], ') + 1);
      let launched; try { launched = JSON.parse(encoded); } catch { throw new Error('Actual runtime execve arguments are incomplete'); }
      requireThat(launched[0] === expectedExe, 'Actual runtime execve target mismatch');
      args = launched.slice(1); argumentEvidence = 'same-pid-successful-execve';
    }
    requireThat(sameArguments(args, item.args ?? []), 'Runtime process arguments differ from package launch');
    rows.push({ name: item.name, pid: Number(name), exe, executable: item.executable, cwd, args, cmdline: argv, argumentEvidence, sha256: actualHash });
  }
  return rows;
}
function sameArguments(a, b) { return JSON.stringify(a) === JSON.stringify(b); }
export async function assertSocket(filename, expectedPort) {
  await noLinks(filename);
  requireThat((await lstat(filename)).isSocket() && path.basename(filename) === `.s.PGSQL.${expectedPort}`, 'Explicit matching PostgreSQL Unix socket required');
}
export async function writeJSON(filename, data) {
  const temporary = `${filename}.${process.pid}.tmp`;
  try {
    await writeFile(temporary, JSON.stringify(data, null, 2) + '\n', { mode: 0o600, flag: 'wx' });
    requireThat(!(await lstat(filename).catch(() => null)), 'Refusing to overwrite published controller evidence');
    // Single controller writer owns the exclusive directory. Reader sees only
    // absent or complete JSON; it never observes a newly created empty command.
    await rename(temporary, filename);
  } finally { await rm(temporary, { force: true }); }
}

export function auditTraceLine(line, cacheRoots = []) {
  // strace omits write buffers: only file/network operations are recorded.
  // A denied attempt still fails acceptance; readonly mounts alone do not prove
  // that the application did not require an undeclared writable path.
  const mutation = /\b(?:open|openat|openat2)\(.*\b(?:O_WRONLY|O_RDWR|O_CREAT|O_TRUNC|O_APPEND)\b/.test(line) || /\b(?:creat|mkdir|mkdirat|unlink|unlinkat|rename|renameat|renameat2|rmdir|truncate|ftruncate|fallocate|chmod|fchmod|fchmodat|fchmodat2|chown|fchown|fchownat|lchown|symlink|link|mknod|utime|utimensat)\(/.test(line);
  if (mutation) {
    const quoted = [...line.matchAll(/"([^"\\]*(?:\\.[^"\\]*)*)"/g)];
    const fdPaths = [...line.matchAll(/\d+<([^>]+)>/g)].map(row => row[1]);
    const paths = quoted.length ? quoted.map(row => {
      // -yy annotates directory fds, including AT_FDCWD. Resolve relative
      // operands against that directory before checking their normalized scope.
      requireThat(!row[1].includes('\\'), 'Undeclared runtime write attempt');
      if (row[1].startsWith('/')) return row[1];
      const directory = line.slice(0, row.index).match(/(?:AT_FDCWD|\d+)<([^>]+)>,\s*$/)?.[1];
      requireThat(directory?.startsWith('/'), 'Undeclared runtime write attempt');
      return path.posix.join(directory, row[1]);
    }) : fdPaths;
    const allowed = value => {
      const normalized = path.posix.normalize(value);
      return normalized === '/dev/null' || normalized.startsWith('/mutable/') || cacheRoots.some(root => {
        const base = path.posix.normalize(root); return normalized === base || normalized.startsWith(`${base}/`);
      });
    };
    // fchmod/fchown/ftruncate are only attributable to a file
    // when strace's -yy fd path is present. Unknown paths fail closed.
    requireThat(paths.length > 0 && paths.every(allowed), 'Undeclared runtime write attempt');
  }
  // Network payload syscalls use raw arguments so trace files never retain
  // request/response bodies. Their pointer-only destinations cannot establish
  // an allowed endpoint, so any such send attempt fails closed.
  requireThat(!/\b(?:sendto|sendmsg|sendmmsg)\(/.test(line), 'Unresolved runtime network send target');
  if (/\b(?:connect|bind)\(/.test(line) && /AF_INET6?/.test(line)) {
    const addresses = [...line.matchAll(/(?:inet_addr|inet_pton)\([^)]*?"([^"]+)"/g)].map(row => row[1]);
    requireThat(addresses.length > 0 && addresses.every(value => value === '127.0.0.1' || value === '::1'), 'Non-loopback runtime network attempt');
  }
  return { mutation, network: /\b(?:connect|sendto|sendmsg)\(/.test(line) };
}
export async function auditTraceFiles(root, cacheRoots = []) {
  const files = (await readdir(root)).filter(name => /\.trace\.\d+$/.test(name)).sort();
  requireThat(files.length > 0, 'Actual runtime syscall audit missing');
  let mutations = 0, network = 0; const evidence = [], migrations = new Set();
  for (const name of files) {
    const filename = path.join(root, name), stat = await lstat(filename);
    requireThat(stat.isFile() && stat.size <= 16 * 1024 * 1024, 'Runtime trace exceeds bounded audit');
    for (const line of (await readFile(filename, 'utf8')).split('\n')) {
      const result = auditTraceLine(line, cacheRoots); mutations += Number(result.mutation); network += Number(result.network);
      if (/execve\("\/release\/artifact\/bin\/migrate".*= 0/.test(line)) migrations.add(name.split('.trace.')[0]);
    }
    evidence.push({ path: name, bytes: stat.size, sha256: await fileHash(filename) });
  }
  requireThat(migrations.has('migrate-first') && migrations.has('migrate-second'), 'Both actual same-package migration executions must be audited');
  return { files: evidence, mutationCalls: mutations, networkCalls: network, auditedUndeclaredFileMutationAttempts: 0, nonLoopbackAttempts: 0, coverage: 'write-capable opens and path mutations; fchmod/fchown/ftruncate/fallocate with -yy fd paths; file/network syscall metadata only, no write payload buffers' };
}

export async function mutableAudit(root) {
  const records = [];
  async function visit(relative = '') {
    for (const name of (await readdir(path.join(root, relative))).sort()) {
      const item = relative ? `${relative}/${name}` : name, filename = path.join(root, item), stat = await lstat(filename);
      safeRelative(item);
      requireThat(!stat.isSymbolicLink(), 'Mutable output must not contain symlinks');
      if (stat.isDirectory()) await visit(item);
      else { requireThat(stat.isFile() && stat.nlink === 1, 'Mutable output contains special or hard-linked file'); records.push({ path: item, bytes: stat.size, sha256: await fileHash(filename) }); }
    }
  }
  await visit(); return records;
}
