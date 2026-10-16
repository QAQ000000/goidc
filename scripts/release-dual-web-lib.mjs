import { buildStaticRetention, staticRetentionParts } from './release-static-retention-lib.mjs';

const check = (ok, message) => { if (!ok) throw new Error(message); };
const digest = value => /^[a-f0-9]{64}$/.test(value ?? '');

function origin(value) {
  let url;
  try { url = new URL(value); } catch { throw new Error('Explicit isolated origin required'); }
  check(url.origin === value && url.protocol === 'http:' && url.hostname === '127.0.0.1' && Number(url.port) > 1024 && !['8088', '3088'].includes(url.port), 'Unsafe isolated origin');
  return value;
}

// This binds already authenticated manifests/launch descriptions. The caller
// must run verifyRuntimeCandidate with independent anchors before planning, then
// recheck actual bytes before/after execution. This function alone proves none
// of source authenticity, on-disk integrity or runtime behavior.
export function buildDualWebPlan({ old, next }) {
  const releases = [old, next];
  const descriptions = releases.map(({ manifest, description, manifestSHA256 }) => {
    check(digest(manifestSHA256) && /^[a-f0-9]{40}$/.test(manifest?.source?.commit ?? ''), 'Dual Web requires anchored source identities');
    const api = origin(manifest.web?.apiInternalURL), web = origin(description?.webOrigin), publicOrigin = origin(description?.publicOrigin);
    check(manifest.web?.webOrigin === web && manifest.web?.publicOrigin === publicOrigin && description.apiInternalURL === api, 'Explicit build/launch origins must match');
    const env = description.environment;
    check(env?.inherit === false && env.fixed?.PUBLIC_ORIGIN === publicOrigin && env.fixed.PUBLIC_ORIGINS === publicOrigin && env.fixed.API_INTERNAL_URL === api && env.fixed.HTTP_ADDR === new URL(api).host && env.fixed.HOSTNAME === '127.0.0.1' && String(env.fixed.PORT) === new URL(web).port, 'Dual Web launch environment mismatch');
    check(typeof manifest.web.buildID === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(manifest.web.buildID), 'Actual Web build identity required');
    return { api, web, publicOrigin, buildID: manifest.web.buildID, manifestSHA256, commit: manifest.source.commit };
  });
  check(descriptions[0].publicOrigin === descriptions[1].publicOrigin, 'Both releases must declare the same public origin');
  const endpoints = [descriptions[0].api, descriptions[0].web, descriptions[1].api, descriptions[1].web, descriptions[0].publicOrigin];
  check(new Set(endpoints).size === 5, 'API, Web and public listeners must all differ');
  check(old.manifestSHA256 !== next.manifestSHA256 && descriptions[0].buildID !== descriptions[1].buildID, 'Distinct normally built candidates required');
  const retention = buildStaticRetention({ old, next });
  const oldOnly = retention.oldEntries.filter(row => !retention.entries.find(item => item.url === row.url)?.sources.some(source => source.release === 'next'));
  const nextOnly = retention.entries.filter(row => row.sources.every(source => source.release === 'next'));
  check(oldOnly.length && nextOnly.length, 'Each candidate needs a unique static URL; actual HTML references still require runtime checks');
  return { version: 1, publicOrigin: descriptions[0].publicOrigin, old: descriptions[0], next: descriptions[1], retention, uniqueURLs: { old: oldOnly.map(row => row.url), next: nextOnly.map(row => row.url) } };
}

// All three stages retain the union: new HTML can already escape through a
// prewarm/operator request. Restoring HTML/upstream must never drop issued URLs.
export function renderDualWebNginx(plan, { phase } = {}) {
  check(plan?.version === 1 && ['old', 'next', 'restored'].includes(phase), 'Invalid dual Web phase');
  const publicOrigin = origin(plan.publicOrigin);
  check(plan.old?.publicOrigin === publicOrigin && plan.next?.publicOrigin === publicOrigin, 'Dual public origin changed');
  const endpoints = [origin(plan.old.api), origin(plan.old.web), origin(plan.next.api), origin(plan.next.web), publicOrigin];
  check(new Set(endpoints).size === 5 && plan.old.buildID !== plan.next.buildID, 'Invalid dual Web targets');
  const active = phase === 'next' ? plan.next : plan.old;
  const { rawRequests, locations } = staticRetentionParts(plan.retention, { phase: 'union', generation: phase, locationGuards: true });
  return `daemon off;
master_process on;
worker_processes 1;
worker_shutdown_timeout 15s;
pid /mutable/nginx.pid;
error_log /mutable/error.log notice;
events { worker_connections 256; }
http {
  lua_package_path "/www/server/nginx/lib/lua/?.lua;/www/server/nginx/lib/lua/?/init.lua;;";
  lua_package_cpath "/www/server/nginx/lib/lua/?.so;;";
  log_format bounded '$status $request_method $uri';
  access_log /mutable/access.log bounded;
  client_body_temp_path /mutable/client_body;
  proxy_temp_path /mutable/proxy;
  fastcgi_temp_path /mutable/fastcgi;
  uwsgi_temp_path /mutable/uwsgi;
  scgi_temp_path /mutable/scgi;
  client_max_body_size 1m;
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
  map $http_host $public_host_allowed { default 0; "${new URL(publicOrigin).host}" 1; }
  server {
    listen ${new URL(publicOrigin).host};
    server_name 127.0.0.1;
    root /mutable/empty;
    autoindex off;
    disable_symlinks on;
    if ($public_host_allowed = 0) { return 421; }
    add_header Cache-Control no-store always;
    add_header X-Release-Generation "${phase}" always;
    add_header X-Content-Type-Options nosniff always;
${locations}
    location ^~ /_next/static/ { return 404; }
    location = /_next/static { return 404; }
    location ~ ^/(?:[.]|artifact(?:/|$)|launch(?:/|$)|control(?:/|$)|mutable(?:/|$)|releases(?:/|$)|node_modules(?:/|$)) { return 404; }
    location / {
      proxy_pass ${active.web};
      proxy_http_version 1.1;
      proxy_set_header Host "${new URL(publicOrigin).host}";
      proxy_set_header X-Forwarded-Host "${new URL(publicOrigin).host}";
      proxy_set_header X-Forwarded-Proto http;
      proxy_set_header X-Forwarded-For $remote_addr;
      proxy_set_header Forwarded "";
      proxy_set_header Connection "";
      proxy_connect_timeout 2s;
      proxy_read_timeout 15s;
      proxy_send_timeout 15s;
      proxy_next_upstream off;
      proxy_buffering off;
      proxy_request_buffering off;
      proxy_hide_header Cache-Control;
      proxy_hide_header X-Release-Generation;
      proxy_hide_header X-Content-Type-Options;
    }
  }
}
`;
}
