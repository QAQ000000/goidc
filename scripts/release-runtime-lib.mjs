import { existsSync } from 'node:fs';
import { lstat, readdir, readFile, readlink, realpath } from 'node:fs/promises';
import { execFileSync } from 'node:child_process';
import path from 'node:path';
import { selectStandaloneTraces, traceSelectionManifestPaths } from './release-runtime-trace-selection.mjs';
import { apiTarget, fileHash, noLinks, safeRelative, selectedDigest, sha256, sourceProof } from './release-candidate-lib.mjs';

export const schemaVersion = 2;
export const runtimeFormat = 'zjmfgo-release-candidate-v2';
export const runtimeBuilderFiles = ['scripts/build-release-runtime-candidate.mjs', 'scripts/release-runtime-builder-lib.mjs', 'scripts/release-runtime-trace-selection.mjs', 'scripts/release-runtime-lib.mjs', 'scripts/verify-release-runtime.mjs', 'scripts/build-release-candidate.mjs', 'scripts/release-candidate-lib.mjs', 'scripts/go.sh', 'scripts/check-web-build-runtime.mjs', 'web/next.config.ts'];
const hashPattern = /^[a-f0-9]{64}$/;
const inside = (root, file) => file.startsWith(`${root}/`);
const equal = (a, b) => JSON.stringify(a) === JSON.stringify(b);
const nonempty = value => typeof value === 'string' && value.trim().length > 0;
function requireThat(value, message) { if (!value) throw new Error(message); }
function parseJSON(raw, label) { try { return JSON.parse(raw); } catch { throw new Error(`Invalid ${label} JSON`); } }
function safe(name) { safeRelative(name); requireThat(!name.includes(':'), 'Unsafe runtime path'); return name; }
function protectedRuntimePath(name) {
  return name.split('/').some(part => /^\.env/i.test(part) || /\.(?:pem|key|p12|pfx)$/i.test(part) || ['.git', '.ssh', '.npmrc', '.netrc', 'id_rsa', 'id_ed25519'].includes(part.toLowerCase()));
}
function artifactPath(name) {
  safe(name);
  requireThat(name.startsWith('artifact/') || name.startsWith('launch/'), 'Only sealed artifact/launch paths are permitted');
  requireThat(!protectedRuntimePath(name), 'Protected file in sealed runtime');
  requireThat(!name.split('/').includes('node_modules') || name.startsWith('artifact/web/'), 'Dependencies outside Web runtime');
  return name;
}

// No ignored mutable subtree: everything in a sealed root is inventoried. Links
// are records, never traversal instructions; all ultimate targets stay in Web.
export async function runtimeInventory(root) {
  root = await noLinks(path.resolve(root));
  requireThat((await lstat(root)).isDirectory(), 'Sealed root must be a directory');
  const files = [];
  async function visit(relative) {
    for (const name of (await readdir(path.join(root, relative))).sort()) {
      const rel = relative ? `${relative}/${name}` : name;
      safe(rel);
      const filename = path.join(root, rel), stat = await lstat(filename);
      if (rel === 'manifest.json') {
        requireThat(stat.isFile() && stat.nlink === 1, 'Manifest must be an independent ordinary file');
        continue;
      }
      if (rel !== 'artifact' && rel !== 'launch') artifactPath(rel);
      requireThat(!protectedRuntimePath(rel), 'Protected file in sealed runtime');
      if (stat.isSymbolicLink()) {
        requireThat(rel.startsWith('artifact/web/'), 'Links are permitted only within Web runtime');
        const target = await readlink(filename);
        requireThat(nonempty(target) && !path.posix.isAbsolute(target) && !target.includes('\\') && !/[\x00-\x1f\x7f:]/.test(target), 'Unsafe runtime link target');
        const lexical = path.resolve(path.dirname(filename), target);
        requireThat(inside(path.join(root, 'artifact/web'), lexical), 'Runtime link escapes Web tree');
        let resolved;
        try { resolved = await realpath(filename); } catch { throw new Error('Runtime link is dangling or cyclic'); }
        requireThat(inside(path.join(root, 'artifact/web'), resolved), 'Runtime link escapes Web tree');
        artifactPath(path.relative(root, resolved));
        const resolvedStat = await lstat(resolved);
        requireThat(resolvedStat.isFile() || resolvedStat.isDirectory(), 'Runtime link targets a special file');
        files.push({ path: rel, link: target, resolved: path.relative(root, resolved) });
      } else if (stat.isDirectory()) await visit(rel);
      else if (stat.isFile()) {
        requireThat(stat.nlink === 1, 'Hard-linked sealed files are forbidden');
        files.push({ path: rel, bytes: stat.size, sha256: await fileHash(filename), executable: Boolean(stat.mode & 0o111) });
      } else throw new Error('Special file in sealed runtime');
    }
  }
  await visit('');
  return files;
}

function uniqueRecords(records, label) {
  requireThat(Array.isArray(records), `${label} must be an array`);
  const map = new Map();
  for (const record of records) {
    safe(record?.path);
    requireThat(!map.has(record.path), `Duplicate ${label} path`);
    map.set(record.path, record);
  }
  return map;
}
function recordFile(record, label) {
  requireThat(record && Number.isSafeInteger(record.bytes) && record.bytes >= 0 && hashPattern.test(record.sha256 ?? '') && typeof record.executable === 'boolean' && record.link === undefined, `${label} must be an inventoried file`);
}
function loopbackOrigin(value) {
  const origin = apiTarget(value), url = new URL(origin);
  requireThat(url.protocol === 'http:' && url.hostname === '127.0.0.1' && url.port && !['8088', '3088'].includes(url.port), 'Runtime origin must use an independent explicit IPv4 loopback HTTP port');
  return origin;
}
export function readRuntimeGoBuildInfo(filename, goCommand = existsSync('/usr/local/btgo/bin/go') ? '/usr/local/btgo/bin/go' : 'go') {
  try { return execFileSync(goCommand, ['version', '-m', filename], { encoding: 'utf8', maxBuffer: 1024 * 1024 }).trim().split(/\r?\n/).slice(1).join('\n'); }
  catch { throw new Error('Cannot read actual Go binary build info'); }
}

// This validates sealed metadata and actual bytes, not runtime health or source
// authenticity without verifyRuntimeCandidate's independently supplied anchors.
export async function validateRuntimeManifest(root, manifest, actual, { goCommand } = {}) {
  requireThat(manifest?.format === runtimeFormat && manifest.builder?.version === schemaVersion, 'Unsupported runtime manifest');
  requireThat(/^[a-f0-9]{40}$/.test(manifest.source?.commit ?? ''), 'Runtime source commit missing');
  const source = uniqueRecords(manifest.source.files, 'source');
  for (const file of source.values()) requireThat(hashPattern.test(file.sha256 ?? ''), 'Source checksum missing');
  requireThat(['go', 'node', 'npm', 'platform'].every(key => nonempty(manifest.tools?.[key])), 'Runtime toolchain missing');
  requireThat(manifest.tools.platform === `${process.platform}/${process.arch}`, 'Runtime platform mismatch');
  const builder = selectedDigest(manifest.source.files, runtimeBuilderFiles);
  requireThat(equal(builder.files, manifest.builder.files) && builder.sha256 === manifest.builder.sha256, 'Runtime builder source mismatch');
  const byPath = uniqueRecords(actual, 'artifact');
  async function file(name) {
    artifactPath(name);
    let resolved;
    try { resolved = await realpath(path.join(root, name)); } catch { throw new Error('Required runtime file missing'); }
    requireThat(inside(path.resolve(root), resolved), 'Required file escapes sealed root');
    const record = byPath.get(path.relative(path.resolve(root), resolved));
    recordFile(record, 'Required runtime artifact');
    return record;
  }
  async function json(name) { await file(name); return parseJSON(await readFile(path.join(root, name), 'utf8'), 'sealed metadata'); }
  for (const [key, name] of [['goMod', 'go.mod'], ['goSum', 'go.sum'], ['web', 'web/package-lock.json']]) requireThat(hashPattern.test(manifest.locks?.[key] ?? '') && manifest.locks[key] === source.get(name)?.sha256, 'Runtime lockfile mismatch');
  for (const name of ['api', 'worker', 'migrate']) {
    const binary = manifest.binaries?.[name];
    requireThat(binary?.path === `artifact/bin/${name}`, 'Runtime binary path mismatch');
    const record = await file(binary.path);
    requireThat(record.executable && nonempty(binary.buildInfo), 'Runtime binary executable/provenance missing');
    requireThat(binary.buildInfo.split('\n').filter(line => /\bvcs\.revision=/.test(line)).length === 1 && binary.buildInfo.includes(`vcs.revision=${manifest.source.commit}`) && /(?:^|\s)vcs\.modified=false(?:\s|$)/.test(binary.buildInfo) && !binary.buildInfo.includes('vcs.modified=true'), 'Go binary source mismatch');
    requireThat(readRuntimeGoBuildInfo(path.join(root, binary.path), goCommand) === binary.buildInfo, 'Actual Go build info mismatch');
  }
  const node = manifest.runtime?.node;
  requireThat(node?.path === 'artifact/runtime/node' && node.version === manifest.tools.node && nonempty(node.source) && node.licensePath === 'artifact/runtime/LICENSE', 'Node runtime provenance missing');
  requireThat((await file(node.path)).executable && (await file(node.licensePath)).bytes > 0, 'Node runtime/mandatory license missing');
  requireThat(node.provenancePath === 'artifact/meta/node.json' && hashPattern.test(node.provenanceSHA256 ?? '') && (await file(node.provenancePath)).sha256 === node.provenanceSHA256, 'Node provenance digest mismatch');
  const nodeProof = await json(node.provenancePath);
  requireThat(nodeProof.format === 'zjmfgo-runtime-node-provenance-v1' && nodeProof.version === node.version && nodeProof.platform === manifest.tools.platform && nodeProof.source === node.source && nodeProof.sha256 === (await file(node.path)).sha256 && nodeProof.licenseSHA256 === (await file(node.licensePath)).sha256, 'Node/license source bytes mismatch');
  const host = manifest.runtime.host;
  requireThat(host?.platform === manifest.tools.platform && Array.isArray(host.requirements) && host.requirements.length > 0 && host.requirements.every(nonempty), 'Runtime host baseline missing');
  const migrations = manifest.source.files.filter(row => /^db\/migrations\/\d.*\.sql$/.test(row.path));
  requireThat(migrations.length && equal(migrations, manifest.migrations), 'Runtime migration checksum list mismatch');
  for (const migration of migrations) requireThat((await file(`artifact/${migration.path.slice(3)}`)).sha256 === migration.sha256, 'Runtime migration source mismatch');
  const deps = manifest.dependencies;
  requireThat(deps?.lockSHA256 === manifest.locks.web && hashPattern.test(deps.inputTreeSHA256 ?? '') && deps.provenancePath === 'artifact/meta/dependencies.json', 'Dependency lock/provenance missing');
  const provenanceFile = await file(deps.provenancePath);
  requireThat(provenanceFile.sha256 === deps.provenanceSHA256, 'Dependency provenance anchor mismatch');
  const provenance = await json(deps.provenancePath), installation = provenance.installation;
  requireThat(provenance.format === 'zjmfgo-runtime-dependency-provenance-v1' && provenance.lockSHA256 === deps.lockSHA256 && provenance.inputTreeSHA256 === deps.inputTreeSHA256, 'Dependency provenance identity mismatch');
  requireThat(installation?.command === 'npm ci' && installation.integrityVerified === true && nonempty(installation.source) && nonempty(installation.immutability) && installation.nodeVersion === manifest.tools.node && installation.npmVersion === manifest.tools.npm && installation.platform === manifest.tools.platform, 'Trusted dependency installation record missing');
  const registry = new URL(installation.registry);
  requireThat(!registry.username && !registry.password && !registry.search && !registry.hash && (registry.protocol === 'https:' || (registry.protocol === 'http:' && ['127.0.0.1', 'localhost', '[::1]'].includes(registry.hostname))), 'Unsafe dependency installation registry');
  const inputFiles = uniqueRecords(provenance.files, 'dependency input');
  requireThat(inputFiles.size > 0 && sha256(JSON.stringify(provenance.files)) === deps.inputTreeSHA256, 'Dependency input tree mismatch');
  for (const input of inputFiles.values()) {
    requireThat(!protectedRuntimePath(input.path), 'Protected dependency input');
    if (input.link !== undefined) {
      safe(input.link);
      requireThat(inputFiles.has(input.link) || [...inputFiles.keys()].some(name => name.startsWith(`${input.link}/`)), 'Dependency input link is not closed');
    } else recordFile(input, 'Dependency input');
  }
  for (const input of inputFiles.values()) if (input.link !== undefined) {
    let current = input.path;
    const visited = new Set();
    while (inputFiles.get(current)?.link !== undefined) {
      requireThat(!visited.has(current), 'Dependency input link cycle');
      visited.add(current);
      current = inputFiles.get(current).link;
    }
    requireThat(!protectedRuntimePath(current), 'Protected dependency link target');
  }
  const selected = uniqueRecords(deps.files, 'selected dependency');
  for (const selectedFile of selected.values()) {
    requireThat(selectedFile.path.startsWith('artifact/web/'), 'Selected dependency outside Web');
    safe(selectedFile.sourcePath);
    const input = inputFiles.get(selectedFile.sourcePath), output = byPath.get(selectedFile.path);
    recordFile(input, 'Selected dependency input'); recordFile(output, 'Selected dependency output');
    requireThat(input.sha256 === output.sha256 && input.bytes === output.bytes && input.executable === output.executable, 'Selected dependency bytes mismatch');
  }
  for (const output of actual) if (output.path.includes('/node_modules/') && output.link === undefined) requireThat(selected.has(output.path), 'Unmapped runtime dependency file');
  requireThat(Array.isArray(deps.traces) && deps.traces.length > 0, 'Dependency tracing proof missing');
  requireThat(deps.selectionPath === 'artifact/meta/trace-selection.json', 'Next trace selection proof missing');
  const selectionMetadata = await json(deps.selectionPath);
  const selection = selectStandaloneTraces(selectionMetadata);
  const webTraceRoot = `${manifest.web.appRoot}/.next`;
  for (const name of traceSelectionManifestPaths) requireThat(equal(selectionMetadata.manifests[name], await json(`${webTraceRoot}/${name}`)), 'Trace selection manifest differs from standalone metadata');
  requireThat(equal(selectionMetadata.requiredFiles, (await json(`${webTraceRoot}/required-server-files.json`)).files) && selectionMetadata.nextVersion === manifest.web.nextVersion, 'Trace selection required files/Next version mismatch');
  requireThat(equal(deps.traces.map(trace => trace.path), selection.selected.map(name => `artifact/meta/traces/${name}`)), 'Selected Next trace set mismatch');
  requireThat(Array.isArray(deps.omitted) && deps.omitted.length === selection.omitted.length, 'Next trace omission proof missing');
  for (const [index, entry] of selection.omitted.entries()) {
    const omitted = deps.omitted[index], expectedPath = `artifact/meta/traces/${entry.name}`;
    requireThat(omitted.path === expectedPath && omitted.reason === entry.reason && omitted.sha256 === (await file(expectedPath)).sha256, 'Next trace omission bytes/reason mismatch');
    const raw = await json(expectedPath);
    requireThat(raw.version === 1 && Array.isArray(raw.files), 'Omitted trace schema invalid');
  }
  const expectedTraceArtifacts = [...selection.selected, ...selection.omitted.map(entry => entry.name)].map(name => `artifact/meta/traces/${name}`).sort();
  requireThat(equal(actual.filter(record => record.path.startsWith('artifact/meta/traces/')).map(record => record.path).sort(), expectedTraceArtifacts), 'Unknown or missing trace proof artifact');
  const tracePaths = new Set(), traced = new Set();
  for (const trace of deps.traces) {
    requireThat(trace.path?.startsWith('artifact/meta/traces/') && !tracePaths.has(trace.path), 'Duplicate/unsafe trace proof');
    tracePaths.add(trace.path);
    const traceFile = await file(trace.path), data = await json(trace.path);
    if (trace.sha256 !== undefined) requireThat(trace.sha256 === traceFile.sha256, 'Trace checksum mismatch');
    requireThat(data.version === 1 && Array.isArray(data.files) && data.files.length === new Set(data.files).size && Array.isArray(trace.entries) && data.files.length === trace.entries.length, 'Trace mapping is incomplete');
    const mapped = new Set();
    for (const entry of trace.entries) {
      requireThat(nonempty(entry.input) && !path.posix.isAbsolute(entry.input) && !/[\\\x00-\x1f\x7f:]/.test(entry.input) && data.files.includes(entry.input) && !mapped.has(entry.input), 'Unsafe/duplicate trace input');
      mapped.add(entry.input);
      const output = await file(entry.artifactPath);
      const traceOrigin = path.posix.join('web/.next', trace.path.slice('artifact/meta/traces/'.length));
      const inputPath = path.posix.normalize(path.posix.join(path.posix.dirname(traceOrigin), entry.input));
      safe(inputPath);
      requireThat(entry.artifactPath === `artifact/web/${inputPath}`, 'Trace input/output path binding mismatch');
      if (entry.kind === 'dependency') {
        requireThat(inputPath === `web/node_modules/${entry.sourcePath}` && selected.get(output.path)?.sourcePath === entry.sourcePath, 'Trace dependency is not mapped to trusted input');
        traced.add(output.path);
      } else if (entry.kind === 'source') {
        safe(entry.sourcePath);
        requireThat(inputPath === entry.sourcePath && source.get(entry.sourcePath)?.sha256 === output.sha256, 'Trace source checksum mismatch');
      } else requireThat(entry.kind === 'generated' && entry.sourcePath === undefined && inputPath.startsWith('web/.next/') && !inputPath.startsWith('web/.next/standalone/'), 'Unknown trace provenance kind');
    }
  }
  for (const selectedFile of selected.values()) requireThat(traced.has(selectedFile.path), 'Selected dependency missing from trace closure');
  const web = manifest.web;
  artifactPath(web?.appRoot);
  requireThat((web.appRoot === 'artifact/web' || web.appRoot.startsWith('artifact/web/')) && web.entry === `${web.appRoot}/server.js` && web.bundler === 'webpack' && nonempty(web.buildID), 'Standalone Web identity mismatch');
  const target = loopbackOrigin(web.apiInternalURL);
  const nextFile = [...selected.values()].find(row => row.sourcePath === 'next/package.json');
  requireThat(nextFile && (await json(nextFile.path)).version === web.nextVersion, 'Actual Next version mismatch');
  await file(web.entry);
  const server = await readFile(path.join(root, web.entry), 'utf8'), configs = [...server.matchAll(/^const nextConfig = (.+)$/gm)];
  requireThat(configs.length === 1, 'Standalone embedded configuration missing');
  const config = parseJSON(configs[0][1], 'standalone configuration');
  requireThat(config.output === 'standalone' && ['.next', './.next'].includes(config.distDir) && (!config.env || Object.keys(config.env).length === 0), 'Standalone embedded configuration mismatch');
  if (web.effectiveStandaloneConfigSHA256 !== undefined) requireThat(web.effectiveStandaloneConfigSHA256 === sha256(JSON.stringify(config)), 'Standalone configuration digest mismatch');
  const buildID = (await readFile(path.join(root, (await file(`${web.appRoot}/.next/BUILD_ID`)).path), 'utf8')).trim();
  requireThat(buildID === web.buildID, 'Runtime BUILD_ID mismatch');
  const routes = await json(`${web.appRoot}/.next/routes-manifest.json`);
  if (web.routesManifestSHA256 !== undefined) requireThat(web.routesManifestSHA256 === (await file(`${web.appRoot}/.next/routes-manifest.json`)).sha256, 'Routes manifest digest mismatch');
  const rewrites = Array.isArray(routes.rewrites) ? routes.rewrites : Object.values(routes.rewrites ?? {}).flat();
  const apiRules = rewrites.filter(rule => rule.source === '/api/:path*');
  requireThat(apiRules.length === 1 && apiRules[0].destination === `${target}/api/:path*`, 'Runtime API rewrite mismatch');
  const required = await json(`${web.appRoot}/.next/required-server-files.json`);
  requireThat(required.config?.output === 'standalone' && ['.next', './.next'].includes(required.config.distDir) && (!required.config.env || Object.keys(required.config.env).length === 0), 'Required server configuration mismatch');
  await file(`${web.appRoot}/.next/server/pages-manifest.json`);
  requireThat(actual.some(row => row.path.startsWith(`${web.appRoot}/.next/static/`) && row.bytes > 0) && actual.some(row => row.path.startsWith(`${web.appRoot}/.next/server/app/`) && row.bytes > 0), 'Standalone Web production output missing');
  requireThat(!actual.some(row => row.path === `${web.appRoot}/.next/cache` || row.path.startsWith(`${web.appRoot}/.next/cache/`)), 'Mutable Web cache in sealed artifact');
  for (const sourceFile of source.values()) if (sourceFile.path.startsWith('web/public/')) requireThat((await file(`${web.appRoot}/public/${sourceFile.path.slice('web/public/'.length)}`)).sha256 === sourceFile.sha256, 'Runtime public source mismatch');
  requireThat(manifest.launch?.path === 'launch/description.json', 'Launch description missing');
  const launch = await json(manifest.launch.path);
  requireThat(launch.version === 1 && nonempty(launch.configVersion) && nonempty(launch.runtimeUser) && !['root', '0'].includes(launch.runtimeUser.trim().toLowerCase()), 'Unsafe runtime user/config version');
  requireThat(launch.node === node.path && launch.apiInternalURL === target && launch.appRoot === web.appRoot && launch.entry === web.entry && ['api', 'worker', 'migrate'].every(name => launch.binaries?.[name] === manifest.binaries[name].path), 'Launch artifact binding mismatch');
  const webOrigin = loopbackOrigin(launch.webOrigin), webURL = new URL(webOrigin), apiURL = new URL(target);
  requireThat(webOrigin !== target, 'API and Web ports must be separate');
  requireThat(launch.environment?.inherit === false && launch.environment.fixed?.NODE_ENV === 'production' && launch.environment.fixed.HOSTNAME === webURL.hostname.replace(/^\[|\]$/g, '') && String(launch.environment.fixed.PORT) === webURL.port && launch.environment.fixed.HTTP_ADDR === `${apiURL.hostname}:${apiURL.port}` && launch.environment.fixed.PUBLIC_ORIGIN === webOrigin, 'Launch environment/rewrite binding mismatch');
  const permittedFixed = new Set(['NODE_ENV', 'HOSTNAME', 'PORT', 'HTTP_ADDR', 'PUBLIC_ORIGIN', 'PUBLIC_ORIGINS', 'COOKIE_SECURE', 'PAYMENT_MOCK_ENABLED', 'API_INTERNAL_URL', 'WEB_INTERNAL_URL', 'NEXT_TELEMETRY_DISABLED']);
  requireThat(Object.keys(launch.environment.fixed).every(key => permittedFixed.has(key)) && (launch.environment.fixed.API_INTERNAL_URL === undefined || launch.environment.fixed.API_INTERNAL_URL === target) && (launch.environment.fixed.WEB_INTERNAL_URL === undefined || launch.environment.fixed.WEB_INTERNAL_URL === webOrigin) && (launch.environment.fixed.PUBLIC_ORIGINS === undefined || launch.environment.fixed.PUBLIC_ORIGINS === webOrigin) && (launch.environment.fixed.COOKIE_SECURE === undefined || launch.environment.fixed.COOKIE_SECURE === 'false') && (launch.environment.fixed.PAYMENT_MOCK_ENABLED === undefined || launch.environment.fixed.PAYMENT_MOCK_ENABLED === 'false'), 'Unexpected fixed runtime configuration');
  const keys = launch.environment.allowlist;
  const allowedConfig = new Set(['DATABASE_URL', 'MFA_ENCRYPTION_KEY', 'PURCHASE_FIELD_ENCRYPTION_KEY', 'PROVISION_ENCRYPTION_KEY']);
  requireThat(Array.isArray(keys) && keys.every(key => allowedConfig.has(key)) && new Set(keys).size === keys.length, 'Unsafe environment allowlist');
  requireThat(Array.isArray(launch.environment.forbidden) && ['NODE_PATH', 'NODE_OPTIONS', '__NEXT_PRIVATE_STANDALONE_CONFIG'].every(key => launch.environment.forbidden.includes(key)), 'Forbidden environment contract missing');
  requireThat(launch.config?.external === true && launch.config.mode === '0600' && Array.isArray(launch.config.required) && launch.config.required.includes('DATABASE_URL') && launch.config.required.every(key => keys.includes(key)), 'External controlled configuration contract missing');
  requireThat(Array.isArray(launch.writablePaths) && launch.writablePaths.length > 0 && launch.writablePaths.every(row => row.root === 'mutable' && nonempty(safe(row.path))) && new Set(launch.writablePaths.map(row => row.path)).size === launch.writablePaths.length, 'Mutable paths must be separate from sealed artifact');
  requireThat(launch.releaseVerification === 'full-inventory-before-and-after' && equal(launch.host, host), 'Launch verification/host binding missing');
  requireThat(Array.isArray(launch.steps) && equal(launch.steps.map(step => step.name), ['verify', 'migrate', 'api', 'web', 'worker', 'stop']) && [launch.timeouts?.readySeconds, launch.timeouts?.workerSeconds, launch.timeouts?.stopSeconds].every(value => Number.isFinite(value) && value > 0 && value <= 300), 'Launch order/deadlines missing');
  const steps = Object.fromEntries(launch.steps.map(step => [step.name, step]));
  requireThat(['migrate', 'api', 'worker'].every(name => steps[name].executable === manifest.binaries[name].path && steps[name].cwd === 'artifact' && steps[name].args === undefined) && steps.migrate.repeat === 2 && steps.api.readyURL === `${target}/readyz` && steps.web.executable === node.path && steps.web.cwd === web.appRoot && equal(steps.web.args, [path.posix.relative(web.appRoot, web.entry)]), 'Launch executable/order binding mismatch');
  return manifest;
}

export async function verifyRuntimeCandidate(root, trustedManifestSHA, sourceRepo, options = {}) {
  requireThat(hashPattern.test(trustedManifestSHA ?? ''), 'Trusted manifest SHA256 is required');
  requireThat(nonempty(sourceRepo), 'Trusted clean source repository is required');
  root = await noLinks(path.resolve(root));
  await noLinks(path.join(root, 'manifest.json'));
  const raw = await readFile(path.join(root, 'manifest.json'));
  requireThat(sha256(raw) === trustedManifestSHA, 'Runtime manifest digest mismatch');
  const manifest = parseJSON(raw, 'runtime manifest');
  requireThat(manifest.format === runtimeFormat, 'Unsupported runtime manifest');
  const proof = await sourceProof(sourceRepo, manifest.source?.commit);
  requireThat(equal(proof, manifest.source), 'Trusted runtime source proof mismatch');
  const actual = await runtimeInventory(root);
  requireThat(equal(actual, manifest.artifacts), 'Runtime artifact inventory mismatch');
  return validateRuntimeManifest(root, manifest, actual, options);
}
