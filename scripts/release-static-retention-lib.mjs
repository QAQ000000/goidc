import path from 'node:path';
import { lstat } from 'node:fs/promises';
import { fileHash, noLinks } from './release-candidate-lib.mjs';

// Callers must independently verify both runtime candidates and their external
// manifest anchors first. Planning does not authenticate manifests. The optional
// file verifier reads only. Namespace mounts are fixed, never arbitrary host paths.
const roots = { old: '/releases/old', next: '/releases/new' };
const contentTypes = {
  '.js': 'application/javascript', '.css': 'text/css', '.json': 'application/json', '.map': 'application/json',
  '.woff2': 'font/woff2', '.woff': 'font/woff', '.ttf': 'font/ttf', '.otf': 'font/otf', '.eot': 'application/vnd.ms-fontobject',
  '.svg': 'image/svg+xml', '.png': 'image/png', '.jpg': 'image/jpeg', '.jpeg': 'image/jpeg', '.gif': 'image/gif',
  '.webp': 'image/webp', '.avif': 'image/avif', '.ico': 'image/x-icon', '.txt': 'text/plain',
};
function check(ok, message) { if (!ok) throw new Error(message); }
function relative(value) {
  check(typeof value === 'string' && /^[A-Za-z0-9_.@/()+\[\]-]+$/.test(value) && value.split('/').every(part => part && part !== '.' && part !== '..'), 'Unsafe retention artifact path');
  check(!value.startsWith('/') && value.split('/').every(part => !/^\.env(?:\.|$)/i.test(part) && !/^id_(?:rsa|dsa|ecdsa|ed25519)(?:\.|$)/i.test(part) && !/\.(?:key|pem|p12|pfx|jks|keystore)$/i.test(part) && !/^(?:secrets?|credentials|private[-_]?keys?)(?:\.[^.]+)?$/i.test(part)), 'Protected retention artifact path');
  return value;
}
function collect(input, release) {
  check(input?.root === roots[release], 'Retention mount root must be fixed');
  const manifest = input.manifest;
  const commit = manifest?.source?.commit ?? null, manifestSHA256 = input.manifestSHA256 ?? null;
  check(commit === null || (typeof commit === 'string' && /^[a-f0-9]{40}$/.test(commit)), 'Invalid retention source commit');
  check(manifestSHA256 === null || (typeof manifestSHA256 === 'string' && /^[a-f0-9]{64}$/.test(manifestSHA256)), 'Invalid retention external manifest SHA256');
  check(Array.isArray(manifest?.artifacts) && manifest.artifacts.length > 0, 'Retention artifact inventory required');
  const appRoot = relative(manifest.web?.appRoot);
  check(appRoot.startsWith('artifact/web/'), 'Retention appRoot must belong to Web artifact');
  const prefix = `${appRoot}/.next/static/`, seen = new Set(), entries = [];
  for (const row of manifest.artifacts) {
    const filename = relative(row?.path);
    check(!seen.has(filename), 'Duplicate retention artifact path'); seen.add(filename);
    if (!filename.startsWith(prefix)) continue;
    check(!Object.hasOwn(row, 'link') && Number.isSafeInteger(row.bytes) && row.bytes >= 0 && /^[a-f0-9]{64}$/.test(row.sha256 ?? '') && typeof row.executable === 'boolean', 'Retention static entry must be an inventoried ordinary file');
    const suffix = filename.slice(prefix.length), contentType = contentTypes[path.posix.extname(suffix).toLowerCase()];
    check(contentType && suffix.split('/').every(part => !part.startsWith('.')), 'Unsupported or hidden retained static file');
    entries.push({ url: `/_next/static/${suffix}`, release, path: filename, bytes: row.bytes, sha256: row.sha256, contentType, sources: [{ release, path: filename, commit, manifestSHA256 }] });
  }
  check(entries.length > 0, 'Retention package contains no static files');
  return entries.sort((a, b) => a.url.localeCompare(b.url, 'en'));
}

export function buildStaticRetention({ old, next }) {
  const oldEntries = collect(old, 'old'), newEntries = collect(next, 'next'), union = new Map(oldEntries.map(row => [row.url, row]));
  let shared = 0;
  for (const row of newEntries) {
    const existing = union.get(row.url);
    if (existing) { check(existing.bytes === row.bytes && existing.sha256 === row.sha256, 'Conflicting retained static URL bytes or SHA256'); shared++; }
    union.set(row.url, existing ? { ...row, sources: [...existing.sources, ...row.sources] } : row); // Identical shared URL always uses the new package.
  }
  const entries = [...union.values()].sort((a, b) => a.url.localeCompare(b.url, 'en'));
  return { version: 1, entries, oldEntries, counts: { old: oldEntries.length, next: newEntries.length, shared, union: entries.length } };
}

function validateEntry(row) {
  check(row && Object.hasOwn(roots, row.release), 'Invalid retained release identity');
  relative(row.path);
  check(typeof row.url === 'string' && row.url.startsWith('/_next/static/'), 'Invalid retained URL');
  const suffix = relative(row.url.slice('/_next/static/'.length));
  check(suffix.split('/').every(part => !part.startsWith('.')) && row.path.startsWith('artifact/web/') && row.path.endsWith(`/.next/static/${suffix}`), 'Retained URL/artifact binding mismatch');
  check(Number.isSafeInteger(row.bytes) && row.bytes >= 0 && /^[a-f0-9]{64}$/.test(row.sha256 ?? '') && row.contentType === contentTypes[path.posix.extname(suffix).toLowerCase()] && row.contentType, 'Invalid retained static metadata');
  if (row.sources !== undefined) {
    check(Array.isArray(row.sources) && row.sources.length > 0, 'Invalid retained sources');
    const releases = new Set();
    for (const source of row.sources) {
      check(source && Object.hasOwn(roots, source.release) && !releases.has(source.release), 'Invalid or duplicate retained source release'); releases.add(source.release);
      relative(source.path);
      check(source.path.startsWith('artifact/web/') && source.path.endsWith(`/.next/static/${suffix}`) && (source.commit === null || (typeof source.commit === 'string' && /^[a-f0-9]{40}$/.test(source.commit))) && (source.manifestSHA256 === null || (typeof source.manifestSHA256 === 'string' && /^[a-f0-9]{64}$/.test(source.manifestSHA256))), 'Invalid retained source provenance');
    }
    check(row.sources.some(source => source.release === row.release && source.path === row.path), 'Selected retained provider lacks matching source');
  }
}

// root is one already verified package root. Filter a mixed plan by provider
// before calling. Mounts must remain immutable between verification and serving;
// this does not claim race resistance against concurrent filesystem mutation.
export async function verifyStaticRetentionFiles(root, entries) {
  check(typeof root === 'string' && path.isAbsolute(root) && Array.isArray(entries) && entries.length > 0, 'Absolute retention root and nonempty entries required');
  const absolute = await noLinks(root);
  check((await lstat(absolute)).isDirectory(), 'Retention root must be an ordinary directory');
  const evidence = [], seen = new Set();
  for (const row of entries) {
    validateEntry(row);
    check(!seen.has(row.url), 'Duplicate retained URL'); seen.add(row.url);
    const filename = path.join(absolute, row.path);
    await noLinks(filename);
    const stat = await lstat(filename);
    check(stat.isFile() && stat.size === row.bytes, 'Retained static file type or size mismatch');
    const digest = await fileHash(filename);
    check(digest === row.sha256, 'Retained static file SHA256 mismatch');
    evidence.push({ url: row.url, path: row.path, bytes: stat.size, sha256: digest });
  }
  return evidence;
}
const quote = value => JSON.stringify(value);
const regexEscape = value => value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

export function staticRetentionParts(plan, { phase, generation, locationGuards = false, drainRate = false } = {}) {
  check(typeof drainRate === 'boolean', 'Invalid private drain option');
  check(typeof locationGuards === 'boolean', 'Invalid location guard option');
  check(['old', 'union', 'restored'].includes(phase) && typeof generation === 'string' && /^[A-Za-z0-9_-]{1,80}$/.test(generation), 'Invalid retention Nginx phase, generation or port');
  check(plan?.version === 1 && Array.isArray(plan.entries) && Array.isArray(plan.oldEntries), 'Invalid retention plan');
  // Revalidate public plan inputs before interpolation, including rows unused
  // in this phase. A modified plan cannot inject Nginx directives or file paths.
  for (const rows of [plan.entries, plan.oldEntries]) {
    const seen = new Set(); check(rows.length > 0, 'Empty retention phase');
    for (const row of rows) { validateEntry(row); check(!seen.has(row.url), 'Duplicate retained URL'); seen.add(row.url); }
  }
  const entries = phase === 'old' ? plan.oldEntries : plan.entries;
  const rawRequests = entries.flatMap(row => [
    `    ${quote(row.url)} 1;`,
    `    ${quote(`~^${regexEscape(row.url)}\\?[A-Za-z0-9_.~=&+\\-]*$`)} 1;`,
  ]).join('\n');
  const locations = entries.map(row => `    location = ${quote(row.url)} {${locationGuards ? '\n      if ($retention_exact_request = 0) { return 404; }\n      if ($request_method !~ ^(GET|HEAD)$) { return 405; }' : ''}
      alias ${quote(`${roots[row.release]}/${row.path}`)};${drainRate ? '\n      limit_rate $dual_drain_rate;' : ''}
      types { }
      default_type ${row.contentType};
      add_header Cache-Control $retention_cache_control always;
      add_header X-Release-Generation ${quote(generation)} always;
      add_header X-Content-Type-Options nosniff always;
    }`).join('\n');
  return { rawRequests, locations };
}

export function renderStaticRetentionNginx(plan, { port = 53188, phase, generation } = {}) {
  check(port === 53188, 'Invalid retention Nginx port');
  const { rawRequests, locations } = staticRetentionParts(plan, { phase, generation });
  return `daemon off;
master_process on;
worker_processes 1;
pid /mutable/nginx.pid;
error_log /mutable/error.log notice;
events { worker_connections 128; }
http {
  lua_package_path "/www/server/nginx/lib/lua/?.lua;/www/server/nginx/lib/lua/?/init.lua;;";
  lua_package_cpath "/www/server/nginx/lib/lua/?.so;;";
  access_log /mutable/access.log;
  client_body_temp_path /mutable/client_body;
  proxy_temp_path /mutable/proxy;
  fastcgi_temp_path /mutable/fastcgi;
  uwsgi_temp_path /mutable/uwsgi;
  scgi_temp_path /mutable/scgi;
  default_type application/octet-stream;
  gzip off;
  gzip_static off;
  server_tokens off;
  map_hash_bucket_size 512;
  map_hash_max_size 8192;
  map $status $retention_cache_control {
    default "no-store";
    200 "public, max-age=300, immutable";
    206 "public, max-age=300, immutable";
    304 "public, max-age=300, immutable";
  }
  map $request_uri $retention_exact_request {
    default 0;
${rawRequests}
  }
  server {
    listen 127.0.0.1:53188;
    server_name _;
    root /mutable/empty;
    autoindex off;
    disable_symlinks on;
    if ($retention_exact_request = 0) { return 404; }
    if ($request_method !~ ^(GET|HEAD)$) { return 405; }
    add_header Cache-Control $retention_cache_control always;
    add_header X-Release-Generation ${quote(generation)} always;
    add_header X-Content-Type-Options nosniff always;
${locations}
    location / { return 404; }
  }
}
`;
}
