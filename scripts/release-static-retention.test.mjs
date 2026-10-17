import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { lstat, mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { buildStaticRetention, renderStaticRetentionNginx, verifyStaticRetentionFiles } from './release-static-retention-lib.mjs';

function file(name, bytes = 'asset', appRoot = 'artifact/web/web') {
  return { path: `${appRoot}/.next/static/${name}`, bytes: Buffer.byteLength(bytes), sha256: createHash('sha256').update(bytes).digest('hex'), executable: false };
}
function inputs() {
  return {
    old: { root: '/releases/old', manifest: { web: { appRoot: 'artifact/web/old' }, artifacts: [file('chunks/old.js', 'old', 'artifact/web/old'), file('css/shared.css', 'shared', 'artifact/web/old'), { path: 'artifact/web/old/server.js', bytes: 4, sha256: 'a'.repeat(64), executable: false }] } },
    next: { root: '/releases/new', manifest: { web: { appRoot: 'artifact/web/web' }, artifacts: [file('chunks/new.js', 'new'), file('css/shared.css', 'shared'), file('chunks/app/[id]/page.js', 'dynamic-route chunk'), file('media/font.woff2', 'font')] } },
  };
}
const render = (plan, phase = 'union', generation = 'g2') => renderStaticRetentionNginx(plan, { port: 53188, phase, generation });

test('verified manifest inventories produce an exact union, shared URLs prefer new bytes, no server artifact leaks', () => {
  const original = inputs(), before = JSON.stringify(original), plan = buildStaticRetention(original);
  assert.deepEqual(plan.counts, { old: 2, next: 4, shared: 1, union: 5 });
  assert.deepEqual(plan.entries.map(row => row.url).sort(), ['/_next/static/chunks/app/[id]/page.js', '/_next/static/chunks/new.js', '/_next/static/chunks/old.js', '/_next/static/css/shared.css', '/_next/static/media/font.woff2']);
  const shared = plan.entries.find(row => row.url.endsWith('shared.css'));
  assert.equal(shared.release, 'next'); assert.equal(shared.path, 'artifact/web/web/.next/static/css/shared.css');
  assert.equal(plan.oldEntries.find(row => row.url.endsWith('shared.css')).release, 'old');
  assert.equal(JSON.stringify(original), before);
});

test('same URL with unequal size or SHA fails rather than selecting one or falling back', () => {
  for (const field of ['bytes', 'sha256']) {
    const source = inputs(), row = source.next.manifest.artifacts.find(row => row.path.endsWith('shared.css'));
    row[field] = field === 'bytes' ? row.bytes + 1 : 'b'.repeat(64);
    assert.throws(() => buildStaticRetention(source), /Conflicting retained static URL/);
  }
  const duplicate = inputs(); duplicate.old.manifest.artifacts.push({ ...duplicate.old.manifest.artifacts[0] });
  assert.throws(() => buildStaticRetention(duplicate), /Duplicate retention artifact/);
});

test('shared entries retain both fixed source commits and external manifest anchors', () => {
  const source = inputs();
  source.old.manifest.source = { commit: '1'.repeat(40) }; source.old.manifestSHA256 = 'a'.repeat(64);
  source.next.manifest.source = { commit: '2'.repeat(40) }; source.next.manifestSHA256 = 'b'.repeat(64);
  const plan = buildStaticRetention(source), shared = plan.entries.find(row => row.url.endsWith('shared.css'));
  assert.equal(shared.release, 'next');
  assert.deepEqual(shared.sources, [
    { release: 'old', path: 'artifact/web/old/.next/static/css/shared.css', commit: '1'.repeat(40), manifestSHA256: 'a'.repeat(64) },
    { release: 'next', path: 'artifact/web/web/.next/static/css/shared.css', commit: '2'.repeat(40), manifestSHA256: 'b'.repeat(64) },
  ]);
  assert.equal(plan.oldEntries.find(row => row.url.endsWith('shared.css')).sources.length, 1);
  assert.deepEqual(buildStaticRetention(inputs()).entries[0].sources[0].commit, null);
  for (const patch of ['bad', 'a'.repeat(63), 'a'.repeat(64) + '\n']) {
    const invalid = inputs(); invalid.next.manifestSHA256 = patch;
    assert.throws(() => buildStaticRetention(invalid), /external manifest SHA256/);
  }
});

test('traversal, encoded aliases, query/hash, directives, controls and key paths are refused at input', () => {
  for (const name of ['../escape.js', './alias.js', 'chunks//page.js', '/absolute.js', 'chunks/%2e%2e/page.js', 'chunks/%5Bid%5D/page.js', 'a.js?x=1', 'a.js#hash', 'a.js;return 200;', 'a\n.js', 'a\\b.js', 'a".js', '${uri}.js', 'a{b}.js', '.env', '.env.production', 'private.key', 'id_rsa', 'credentials.json', 'secrets/token.js', '.hidden/file.js']) {
    const source = inputs(); source.next.manifest.artifacts.push(file(name));
    assert.throws(() => buildStaticRetention(source), /Unsafe|Protected|Unsupported/, name);
  }
  for (const appRoot of ['../web', 'artifact/web/../other', 'artifact/web/app?query', 'artifact/web/app;return', 'artifact/runtime']) {
    const source = inputs(); source.next.manifest.web.appRoot = appRoot;
    assert.throws(() => buildStaticRetention(source), /Unsafe|Web artifact/, appRoot);
  }
  const foreign = inputs(); foreign.old.root = '/host/source';
  assert.throws(() => buildStaticRetention(foreign), /mount root/);
  const unrelated = inputs(); unrelated.next.manifest.artifacts.push({ path: '../../nginx.conf' });
  assert.throws(() => buildStaticRetention(unrelated), /Unsafe/);
});

test('only inventoried ordinary static files with safe metadata and supported MIME types are selected', () => {
  for (const change of [{ link: 'other' }, { link: undefined }, { bytes: -1 }, { bytes: 1.5 }, { sha256: 'bogus' }, { executable: undefined }]) {
    const source = inputs(); Object.assign(source.next.manifest.artifacts[0], change);
    assert.throws(() => buildStaticRetention(source), /ordinary file/);
  }
  const unknown = inputs(); unknown.next.manifest.artifacts.push(file('document.html'));
  assert.throws(() => buildStaticRetention(unknown), /Unsupported/);
  const empty = inputs(); empty.next.manifest.artifacts = [empty.old.manifest.artifacts.at(-1)];
  assert.throws(() => buildStaticRetention(empty), /no static/);
});

test('complete Nginx config phases retain union on restored, fixed roots/port, MIME/cache/method/default404', () => {
  const plan = buildStaticRetention(inputs()), old = render(plan, 'old', 'g1'), union = render(plan), restored = render(plan, 'restored', 'g3');
  assert.equal((old.match(/location = /g) ?? []).length, 2);
  assert.equal((union.match(/location = /g) ?? []).length, 5);
  assert.equal((restored.match(/location = /g) ?? []).length, 5);
  assert.match(old, /alias "\/releases\/old\/artifact\/web\/old\/\.next\/static\/css\/shared\.css"/);
  assert.match(union, /alias "\/releases\/new\/artifact\/web\/web\/\.next\/static\/css\/shared\.css"/);
  for (const config of [old, union, restored]) {
    assert.match(config, /^daemon off;\nmaster_process on;\nworker_processes 1;/); assert.match(config, /events \{/); assert.match(config, /http \{/);
    assert.match(config, /listen 127\.0\.0\.1:53188;/); assert.match(config, /pid \/mutable\/nginx\.pid;/);
    assert.match(config, /if \(\$request_method !~ \^\(GET\|HEAD\)\$\) \{ return 405; \}/);
    assert.match(config, /map \$request_uri \$retention_exact_request/); assert.match(config, /if \(\$retention_exact_request = 0\) \{ return 404; \}/);
    assert.match(config, /location \/ \{ return 404; \}/); assert.match(config, /disable_symlinks on;/);
    assert.doesNotMatch(config, /\b(?:include|proxy_pass|try_files|rewrite|error_page)\b/);
    assert.doesNotMatch(config, /\/api\/|server\.js|\.env/);
    assert.equal((config.match(/Cache-Control/g) ?? []).length, (config.match(/location = /g) ?? []).length + 1);
    assert.match(config, /default "no-store";/); assert.match(config, /200 "public, max-age=300, immutable";/);
    assert.match(config, /gzip off;/); assert.match(config, /gzip_static off;/);
    assert.match(config, /lua_package_path "\/www\/server\/nginx\/lib\/lua\/\?\.lua;/);
  }
  assert.match(union, /default_type text\/css;/); assert.match(union, /default_type font\/woff2;/);
  assert.match(union, /default_type application\/javascript;/);
  assert.match(restored, /X-Release-Generation "g3" always/);
});

test('raw request map allows literal exact URLs and safe queries, never normalized/encoded aliases', () => {
  const config = render(buildStaticRetention(inputs()));
  const patterns = [...config.matchAll(/^    ("(?:[^"\\]|\\.)*") 1;$/gm)].map(row => JSON.parse(row[1]));
  const allowed = request => patterns.some(pattern => pattern.startsWith('~') ? new RegExp(pattern.slice(1)).test(request) : request === pattern);
  for (const request of ['/_next/static/chunks/new.js', '/_next/static/chunks/new.js?v=1&tag=a-b_c', '/_next/static/chunks/app/[id]/page.js', '/_next/static/chunks/app/[id]/page.js?q=x']) assert.equal(allowed(request), true, request);
  for (const request of ['/_next/static/chunks/%6eew.js', '/_next/static/chunks/../chunks/new.js', '/_next//static/chunks/new.js', '/_next/static/chunks/app/%5Bid%5D/page.js', '/_next/static/chunks/new.js/extra', '/_next/static/chunks/new.js?x=%0a', '/_next/static/chunks/new.js#fragment', '/_next/static/chunks/new.js?x=bad value', '/_next/static/chunks/new.js\n', '/_next/static/', '/.env', '/api/v1/site', '/_next/static/unknown.js']) assert.equal(allowed(request), false, request);
});

test('renderer revalidates modified plans and rejects options which could inject config', () => {
  const clean = buildStaticRetention(inputs());
  for (const options of [{ port: 80, phase: 'union', generation: 'g2' }, { phase: 'bogus', generation: 'g2' }, { phase: 'union', generation: 'g2"; return 200;' }]) assert.throws(() => renderStaticRetentionNginx(clean, options), /Invalid retention Nginx/);
  for (const patch of [{ release: '__proto__' }, { path: 'artifact/web/app/.next/static/../../secrets.key' }, { url: '/api/v1/site' }, { contentType: 'text/plain; return 200' }, { sha256: null }]) {
    const plan = structuredClone(clean); Object.assign(plan.entries[0], patch);
    assert.throws(() => render(plan), /Invalid|Unsafe|Protected|binding/, JSON.stringify(patch));
  }
  const duplicate = structuredClone(clean); duplicate.entries.push({ ...duplicate.entries[0] });
  assert.throws(() => render(duplicate), /Duplicate retained URL/);
  for (const patch of [{ commit: 'bad' }, { manifestSHA256: 'a"; return 200;' }, { path: '../../secret.key' }, { release: 'foreign' }]) {
    const changed = structuredClone(clean); Object.assign(changed.entries[0].sources[0], patch);
    assert.throws(() => render(changed), /Invalid|Unsafe|Protected/);
  }
});

async function actualFiles(t) {
  const root = await mkdtemp(path.join(os.tmpdir(), 'retained-files-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const data = 'verified bytes', row = file('chunks/fixture.js', data);
  const source = inputs(); source.next.manifest.artifacts = [row];
  const entry = buildStaticRetention(source).entries.find(row => row.release === 'next');
  const filename = path.join(root, row.path); await mkdir(path.dirname(filename), { recursive: true }); await writeFile(filename, data);
  return { root, entry, filename, data };
}

test('file verifier reads real ordinary files and returns matching evidence without mutation', async t => {
  const f = await actualFiles(t), before = await lstat(f.filename), original = JSON.stringify(f.entry);
  assert.deepEqual(await verifyStaticRetentionFiles(f.root, [f.entry]), [{ url: f.entry.url, path: f.entry.path, bytes: Buffer.byteLength(f.data), sha256: f.entry.sha256 }]);
  assert.equal(await readFile(f.filename, 'utf8'), f.data); assert.equal((await lstat(f.filename)).mtimeMs, before.mtimeMs);
  assert.equal(JSON.stringify(f.entry), original);
  await assert.rejects(verifyStaticRetentionFiles(f.root, [f.entry, f.entry]), /Duplicate retained URL/);
});

test('file verifier rejects missing files, changed size, same-size tamper and invalid entry paths', async t => {
  const missing = await actualFiles(t); await rm(missing.filename);
  await assert.rejects(verifyStaticRetentionFiles(missing.root, [missing.entry]), /ENOENT/);
  const changed = await actualFiles(t); await writeFile(changed.filename, changed.data + '!');
  await assert.rejects(verifyStaticRetentionFiles(changed.root, [changed.entry]), /size mismatch/);
  await writeFile(changed.filename, 'x'.repeat(Buffer.byteLength(changed.data)));
  await assert.rejects(verifyStaticRetentionFiles(changed.root, [changed.entry]), /SHA256 mismatch/);
  await assert.rejects(verifyStaticRetentionFiles(changed.root, [{ ...changed.entry, path: '../outside.js' }]), /Unsafe/);
});

test('file verifier refuses symlink at root, ancestor, directory component or final file', async t => {
  const f = await actualFiles(t), parent = path.dirname(f.root), basename = path.basename(f.root), link = `${f.root}-link`;
  t.after(() => rm(link, { force: true })); await symlink(f.root, link);
  await assert.rejects(verifyStaticRetentionFiles(link, [f.entry]), /Symlink/);
  const ancestor = `${f.root}-ancestor`; t.after(() => rm(ancestor, { force: true })); await symlink(parent, ancestor);
  await assert.rejects(verifyStaticRetentionFiles(path.join(ancestor, basename), [f.entry]), /Symlink/);
  const fileTarget = `${f.filename}-original`; await rm(f.filename); await writeFile(fileTarget, f.data); await symlink(fileTarget, f.filename);
  await assert.rejects(verifyStaticRetentionFiles(f.root, [f.entry]), /Symlink/);
  const directory = await actualFiles(t), original = path.dirname(directory.filename), moved = `${original}-original`;
  await rm(original, { recursive: true }); await mkdir(moved); await writeFile(path.join(moved, 'fixture.js'), directory.data); await symlink(moved, original);
  await assert.rejects(verifyStaticRetentionFiles(directory.root, [directory.entry]), /Symlink/);
});

test('file verifier rejects directories and actual Unix sockets without reading special files', async t => {
  const directory = await actualFiles(t); await rm(directory.filename); await mkdir(directory.filename);
  await assert.rejects(verifyStaticRetentionFiles(directory.root, [directory.entry]), /type or size/);
  const socket = await actualFiles(t); await rm(socket.filename);
  const server = net.createServer(); await new Promise((resolve, reject) => { server.once('error', reject); server.listen(socket.filename, resolve); });
  t.after(() => new Promise(resolve => server.close(resolve)));
  await assert.rejects(verifyStaticRetentionFiles(socket.root, [socket.entry]), /type or size/);
});
