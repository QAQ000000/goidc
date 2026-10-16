import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { buildDualWebPlan, renderDualWebNginx } from './release-dual-web-lib.mjs';

function inputs() {
  const candidate = (kind, apiPort, webPort) => {
    const api = `http://127.0.0.1:${apiPort}`, web = `http://127.0.0.1:${webPort}`, publicOrigin = 'http://127.0.0.1:53188';
    return {
      root: kind === 'old' ? '/releases/old' : '/releases/new', manifestSHA256: (kind === 'old' ? 'a' : 'b').repeat(64),
      manifest: {
        migrations: [{ path: 'db/migrations/001_init.sql', bytes: 5, sha256: 'c'.repeat(64) }],
        source: { commit: '1'.repeat(40) }, // Two actual builds of the same code are explicitly allowed.
        web: { appRoot: 'artifact/web/web', apiInternalURL: api, webOrigin: web, publicOrigin, buildID: kind },
        artifacts: [`chunks/${kind}.js`, 'css/shared.css'].map(name => ({ path: `artifact/web/web/.next/static/${name}`, bytes: 5, sha256: createHash('sha256').update(name).digest('hex'), executable: false })),
      },
      description: { apiInternalURL: api, webOrigin: web, publicOrigin, environment: { inherit: false, fixed: { PUBLIC_ORIGIN: publicOrigin, PUBLIC_ORIGINS: publicOrigin, API_INTERNAL_URL: api, HTTP_ADDR: `127.0.0.1:${apiPort}`, HOSTNAME: '127.0.0.1', PORT: String(webPort) } } },
    };
  };
  return { old: candidate('old', 58088, 53088), next: candidate('next', 58089, 53089) };
}

test('distinct anchored builds use one public entry and five separate listeners', () => {
  const input = inputs(), before = JSON.stringify(input), plan = buildDualWebPlan(input);
  assert.equal(plan.publicOrigin, 'http://127.0.0.1:53188');
  assert.deepEqual(plan.uniqueURLs, { old: ['/_next/static/chunks/old.js'], next: ['/_next/static/chunks/next.js'] });
  assert.deepEqual(plan.retention.counts, { old: 2, next: 2, shared: 1, union: 3 });
  assert.equal(JSON.stringify(input), before);
});

test('historical launches lacking an explicit public contract cannot be promoted', () => {
  for (const key of ['publicOrigin', 'webOrigin']) {
    const input = inputs(); delete input.old.manifest.web[key];
    assert.throws(() => buildDualWebPlan(input), /origins must match/);
  }
  const input = inputs(); delete input.old.description.publicOrigin;
  assert.throws(() => buildDualWebPlan(input), /origin required/);
});

test('origin disagreement, listener collision and build identity reuse block planning', () => {
  for (const mutate of [
    v => { v.next.description.publicOrigin = 'http://127.0.0.1:53189'; },
    v => { v.next.manifest.web.apiInternalURL = v.old.manifest.web.apiInternalURL; v.next.description.apiInternalURL = v.old.description.apiInternalURL; v.next.description.environment.fixed.API_INTERNAL_URL = v.old.description.apiInternalURL; v.next.description.environment.fixed.HTTP_ADDR = '127.0.0.1:58088'; },
    v => { v.next.manifestSHA256 = v.old.manifestSHA256; },
    v => { v.next.manifest.web.buildID = v.old.manifest.web.buildID; },
    v => { v.next.description.environment.fixed.PUBLIC_ORIGINS += ',http://localhost:53188'; },
  ]) { const input = inputs(); mutate(input); assert.throws(() => buildDualWebPlan(input)); }
});

test('external targets, aliases, privileged ports and Nginx string injection fail closed', () => {
  for (const value of ['http://localhost:53188', 'http://127.0.0.1:80', 'http://127.0.0.1:8088', 'http://127.0.0.1:3088', 'http://127.0.0.1:053188', 'http://127.0.0.1:53188/', 'http://127.0.0.1:53188?x', 'http://127.0.0.1:53188/#x', 'https://127.0.0.1:53188', 'http://127.0.0.2:53188', 'http://127.0.0.1:53188;return 200;']) {
    const input = inputs(); input.old.description.publicOrigin = value;
    assert.throws(() => buildDualWebPlan(input), undefined, value);
  }
});

test('new builds need distinct resources and same-URL byte conflicts are never hidden by priority', () => {
  const same = inputs(); same.next.manifest.artifacts = structuredClone(same.old.manifest.artifacts);
  assert.throws(() => buildDualWebPlan(same), /unique static URL/);
  const conflict = inputs(); conflict.next.manifest.artifacts[1].sha256 = 'c'.repeat(64);
  assert.throws(() => buildDualWebPlan(conflict), /Conflicting retained static/);
});

test('switch and restore route through the selected Web while preserving both static generations', () => {
  const plan = buildDualWebPlan(inputs());
  for (const phase of ['old', 'next', 'restored']) {
    const config = renderDualWebNginx(plan, { phase });
    assert.match(config, /listen 127\.0\.0\.1:53188;/);
    assert.match(config, new RegExp(`proxy_pass http://127\\.0\\.0\\.1:${phase === 'next' ? '53089' : '53088'};`));
    assert.ok(config.includes('alias "/releases/old/artifact/web/web/.next/static/chunks/old.js";'));
    assert.ok(config.includes('alias "/releases/new/artifact/web/web/.next/static/chunks/next.js";'));
    assert.match(config, /worker_shutdown_timeout 15s;/);
    assert.match(config, /proxy_next_upstream off;/); // Never automatically replay a write to another backend.
    assert.match(config, new RegExp(`proxy_pass http://127\\.0\\.0\\.1:${phase === 'next' ? '58089' : '58088'};`));
    assert.match(config, /location \^~ \/api\/ \{/);
    assert.match(config, /\$upstream_addr/);
    assert.doesNotMatch(config, /include\s/);
  }
});

test('exact static locations guard raw aliases and methods; unknown static URLs never reach Web', () => {
  const config = renderDualWebNginx(buildDualWebPlan(inputs()), { phase: 'next' });
  const locations = [...config.matchAll(/location = "\/_next\/static\/[^\n]+ \{([\s\S]*?)\n    \}/g)];
  assert.equal(locations.length, 3);
  for (const [, body] of locations) {
    assert.match(body, /limit_rate \$dual_drain_rate;/);
    assert.match(body, /if \(\$retention_exact_request = 0\) \{ return 404; \}/);
    assert.match(body, /if \(\$request_method !~ \^\(GET\|HEAD\)\$\) \{ return 405; \}/);
  }
  assert.match(config, /location \^~ \/_next\/static\/ \{ return 404; \}/);
  assert.match(config, /map \$request_uri \$dual_raw_path/);
  assert.match(config, /if \(\$dual_unsafe_path = 1\) \{ return 404; \}/);
  assert.match(config, /map \$http_host \$public_host_allowed/);
  assert.match(config, /if \(\$public_host_allowed = 0\) \{ return 421; \}/);
});

test('proxy overwrites client forwarded identity, preserves actual Origin, and never logs auth bodies', () => {
  const config = renderDualWebNginx(buildDualWebPlan(inputs()), { phase: 'old' });
  assert.match(config, /proxy_set_header X-Forwarded-Host "127\.0\.0\.1:53188";/);
  assert.match(config, /proxy_set_header X-Forwarded-Proto http;/);
  assert.match(config, /proxy_set_header X-Forwarded-For \$remote_addr;/);
  assert.match(config, /proxy_set_header Forwarded "";/);
  assert.doesNotMatch(config, /proxy_set_header Origin|\$http_cookie|\$http_authorization|\$request_body|\$request_uri';/);
});

test('untrusted phase or modified retention rows cannot inject directives into any generation', () => {
  const plan = buildDualWebPlan(inputs());
  assert.throws(() => renderDualWebNginx(plan, { phase: 'next;return 200;' }));
  plan.retention.entries[0].path += '";include /etc/passwd;#';
  assert.throws(() => renderDualWebNginx(plan, { phase: 'old' }));
});

test('shared-schema preparation rejects changed or missing migration contracts', () => {
  for (const alter of [
    input => { delete input.old.manifest.migrations; },
    input => { input.next.manifest.migrations[0].sha256 = 'd'.repeat(64); },
    input => { input.next.manifest.migrations.push(input.next.manifest.migrations[0]); },
  ]) { const input = inputs(); alter(input); assert.throws(() => buildDualWebPlan(input), /migration/i); }
});

test('private in-flight drain uses bounded real static transfer only when explicitly requested', () => {
  const config = renderDualWebNginx(buildDualWebPlan(inputs()), { phase: 'old' });
  assert.match(config, /map \$http_x_release_drain \$dual_drain_rate \{ default 0; "1" 16384; \}/);
  assert.equal((config.match(/limit_rate \$dual_drain_rate;/g) ?? []).length, 3);
});
