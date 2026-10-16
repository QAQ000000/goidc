import { lstat, mkdir, readFile, readdir, readlink, realpath, writeFile } from 'node:fs/promises';
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
export async function auditProcesses(releaseRoot, expected) {
  const rows = [];
  for (const name of (await readdir('/proc')).filter(value => /^\d+$/.test(value))) {
    let exe, cwd;
    try { [exe, cwd] = await Promise.all([readlink(`/proc/${name}/exe`), readlink(`/proc/${name}/cwd`)]); } catch { continue; }
    const item = expected.find(row => exe === path.join(releaseRoot, row.executable) && cwd === path.join(releaseRoot, row.cwd));
    if (!item) continue;
    rows.push({ name: item.name, pid: Number(name), executable: item.executable, cwd: item.cwd, sha256: await fileHash(`/proc/${name}/exe`) });
  }
  return rows;
}
export async function assertSocket(filename, expectedPort) {
  await noLinks(filename);
  requireThat((await lstat(filename)).isSocket() && path.basename(filename) === `.s.PGSQL.${expectedPort}`, 'Explicit matching PostgreSQL Unix socket required');
}
export async function writeJSON(filename, data) {
  await writeFile(filename, JSON.stringify(data, null, 2) + '\n', { mode: 0o600, flag: 'wx' });
}
