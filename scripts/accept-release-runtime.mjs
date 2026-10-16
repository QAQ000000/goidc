#!/usr/bin/env node
import { cp, chmod, lstat, mkdir, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import { execFileSync, spawn, spawnSync } from 'node:child_process';
import path from 'node:path';
import { pathToFileURL, fileURLToPath } from 'node:url';
import { runtimeInventory, verifyRuntimeCandidate } from './release-runtime-lib.mjs';
import { controlledRuntimeEnvironment } from './release-runtime-builder-lib.mjs';
import { assertAvailablePort, assertSocket, assetArtifact, auditProcesses, getBytes, jsonFile, pageAssets, requireThat, sha256, validateTestDSN, waitFor, writeJSON } from './release-runtime-acceptance-lib.mjs';

const scriptRoot = path.dirname(fileURLToPath(import.meta.url));
const www = 1000;
const names = { '--sealed': 'sealed', '--manifest-sha256': 'manifestSHA', '--source': 'source', '--config': 'config', '--fixture-dir': 'fixtureDir', '--api-port': 'apiPort', '--web-port': 'webPort', '--pg-socket': 'pgSocket', '--execute': 'execute' };
function args() {
  const out = {};
  for (let i = 2; i < process.argv.length; i++) {
    const name = process.argv[i];
    if (!names[name]) throw new Error(`Unknown option: ${name}`);
    if (name === '--execute') { if (i !== process.argv.length - 1) throw new Error('--execute must be final'); out.execute = true; continue; }
    const value = process.argv[++i]; if (!value || out[names[name]]) throw new Error(`Missing or duplicate ${name}`); out[names[name]] = value;
  }
  for (const key of ['sealed', 'manifestSHA', 'source', 'config', 'fixtureDir', 'apiPort', 'webPort', 'pgSocket']) if (!out[key]) throw new Error(`Required: ${Object.keys(names).join(' ')}`);
  return out;
}
function run(command, argv, options = {}) {
  const result = spawnSync(command, argv, { encoding: 'utf8', stdio: options.capture ? 'pipe' : 'inherit', cwd: options.cwd, env: options.env, maxBuffer: 4 * 1024 * 1024 });
  if (result.error || result.status !== 0) throw new Error(`Controlled command failed: ${path.basename(command)}`);
  return options.capture ? result.stdout.trim() : '';
}
async function ownedCopy(source, destination) {
  await mkdir(destination, { recursive: true, mode: 0o755 });
  run('/bin/cp', ['-a', '--reflink=never', `${source}/.`, destination]);
  async function check(root) { for (const item of await readdir(root, { withFileTypes: true })) { const filename = path.join(root, item.name), stat = await lstat(filename); requireThat(stat.nlink === 1, 'Runtime copy contains a hard link'); if (item.isDirectory()) await check(filename); } }
  await check(destination);
}
function bridgeDSN(databaseURL, port) {
  const url = new URL(databaseURL); url.hostname = '127.0.0.1'; url.port = String(port); return url.toString();
}
async function fixtureCommand(dir, operation, number) {
  await writeJSON(path.join(dir, `command-${number}.json`), { operation });
}
async function fixtureResult(dir, operation) {
  return waitFor(async () => {
    const files = (await readdir(dir)).filter(name => name.startsWith('result-') && name.endsWith('.json')).sort();
    const found = await Promise.all(files.map(file => jsonFile(path.join(dir, file)).catch(() => null)));
    return found.reverse().find(result => result && (result.operation === operation || result.phase === operation || result.phase === `verified-${operation}` || (operation === 'prepare' && result.phase === 'prepared')));
  }, 900, `fixture ${operation}`);
}
function writeInnerScripts(root, release, mutable, env) {
  const checker = `import {readFile,writeFile} from 'node:fs/promises'; import {createHash} from 'node:crypto'; const origin=process.argv[2], api=process.argv[3], app=process.argv[4], out=process.argv[5]; const hash=b=>createHash('sha256').update(b).digest('hex'); const assets=new Set(); const pages=[]; for(const route of ['/', '/login','/admin-login']){const r=await fetch(origin+route);if(r.status!==200)throw Error('page '+route);const b=Buffer.from(await r.arrayBuffer());pages.push({route,status:r.status,sha256:hash(b)});for(const m of b.toString().matchAll(/(?:src|href)=["']([^"']+)["']/g)){const u=new URL(m[1],origin);if(u.origin===origin&&/\\.(js|css)$/.test(u.pathname))assets.add(u.pathname)}};if(!assets.size)throw Error('no same-origin assets');const checked=[];for(const asset of assets){const r=await fetch(origin+asset);if(r.status!==200)throw Error('asset '+asset);const body=Buffer.from(await r.arrayBuffer());const rel=app+'/.next/static/'+decodeURIComponent(asset.slice('/_next/static/'.length));const disk=await readFile(rel);if(!disk.equals(body))throw Error('asset bytes differ '+asset);checked.push({asset,status:r.status,bytes:body.length,sha256:hash(body)})}const direct=Buffer.from(await (await fetch(api+'/api/v1/site')).arrayBuffer());const proxy=Buffer.from(await (await fetch(origin+'/api/v1/site')).arrayBuffer());if(!direct.equals(proxy))throw Error('proxy differs');await writeFile(out,JSON.stringify({ok:true,pages,assets:checked,proxy:{equal:true,sha256:hash(direct)}}));`;
  return { checker, env };
}
async function execute(options, manifest, baseEnv, copyRoot, fixture, fixtureProcess) {
  const config = await jsonFile(options.config); const dsn = config.values.DATABASE_URL; const pg = validateTestDSN(dsn, fixture.schema); const bridgePort = Number(options.apiPort) + 1000; await assertSocket(options.pgSocket, pg.port); await assertAvailablePort(Number(options.apiPort)); await assertAvailablePort(Number(options.webPort)); await assertAvailablePort(bridgePort);
  const mutable = path.join(copyRoot, '..', 'mutable'); await mkdir(path.join(mutable, 'home', 'www'), { recursive: true, mode: 0o700 }); await mkdir(path.join(mutable, 'control'), { mode: 0o700 });
  const inner = writeInnerScripts(copyRoot, copyRoot, mutable, baseEnv); await writeFile(path.join(mutable, 'page-check.mjs'), inner.checker, { mode: 0o700 });
  const webOrigin = `http://127.0.0.1:${options.webPort}`, apiOrigin = `http://127.0.0.1:${options.apiPort}`;
  const env = { ...baseEnv, DATABASE_URL: bridgeDSN(dsn, bridgePort), HTTP_ADDR: `127.0.0.1:${options.apiPort}`, PUBLIC_ORIGIN: webOrigin, PUBLIC_ORIGINS: webOrigin, API_INTERNAL_URL: apiOrigin, HOSTNAME: '127.0.0.1', PORT: String(options.webPort), NODE_ENV: 'production', NEXT_TELEMETRY_DISABLED: '1', HOME: '/mutable/home/www', PATH: '/usr/local/bin:/usr/bin:/bin' };
  const node = path.join(copyRoot, manifest.runtime.node.path), api = path.join(copyRoot, manifest.binaries.api.path), migrate = path.join(copyRoot, manifest.binaries.migrate.path), worker = path.join(copyRoot, manifest.binaries.worker.path), app = path.join(copyRoot, manifest.web.appRoot);
  const socketInside = `/pg/${path.basename(options.pgSocket)}`;
  const script = `set -eu\nNODE=${JSON.stringify(node)}\nSOCKET=${JSON.stringify(socketInside)}\nBRIDGE=${bridgePort}\nAPI=${JSON.stringify(api)}\nMIGRATE=${JSON.stringify(migrate)}\nWORKER=${JSON.stringify(worker)}\nAPP=${JSON.stringify(app)}\nAPIURL=${JSON.stringify(apiOrigin)}\nWEBURL=${JSON.stringify(webOrigin)}\n$NODE -e 'const net=require("net"),p=process.argv[1],s=process.argv[2];net.createServer(c=>{const u=net.createConnection({path:s});c.pipe(u);u.pipe(c);u.on("error",()=>c.destroy())}).listen(Number(p),"127.0.0.1")' "$BRIDGE" "$SOCKET" & bridge=$!\ntrap 'kill "$bridge" "$api_pid" "$web_pid" "$worker_pid" 2>/dev/null || true' EXIT TERM INT\nDATABASE_URL=${JSON.stringify(env.DATABASE_URL)} "$MIGRATE"\nDATABASE_URL=${JSON.stringify(env.DATABASE_URL)} "$MIGRATE"\ntouch /mutable/migrations-done\nwhile [ ! -f /mutable/control/fixture-prepared ]; do sleep .1; done\n"$API" & api_pid=$!\nfor i in $(seq 1 300); do curl -fsS "$APIURL/readyz" >/dev/null && break; sleep .1; done\n"$NODE" "$APP/server.js" & web_pid=$!\nfor i in $(seq 1 300); do curl -fsS "$WEBURL/" >/dev/null && break; sleep .1; done\n"$NODE" /mutable/page-check.mjs "$WEBURL" "$APIURL" "$APP" /mutable/page-report.json\n"$WORKER" & worker_pid=$!\nwhile :; do if [ -f /mutable/control/stop-worker ]; then kill "$worker_pid" 2>/dev/null || true; wait "$worker_pid" || true; rm -f /mutable/control/stop-worker; fi; if [ -f /mutable/control/restart-worker ]; then "$WORKER" & worker_pid=$!; rm -f /mutable/control/restart-worker; fi; if [ -f /mutable/control/finish ]; then break; fi; sleep .1; done\n`;
  await writeFile(path.join(mutable, 'run.sh'), script, { mode: 0o700 });
  const bwrap = ['/usr/bin/bwrap', '--die-with-parent', '--unshare-all', '--proc', '/proc', '--dev', '/dev', '--ro-bind', '/usr', '/usr', '--ro-bind', '/bin', '/bin', '--ro-bind', '/lib', '/lib', '--ro-bind', '/lib64', '/lib64', '--ro-bind', path.dirname(options.pgSocket), '/pg', '--ro-bind', copyRoot, '/release', '--bind', mutable, '/mutable', '--chdir', '/release/artifact', '--setenv', 'PATH', env.PATH, '--setenv', 'HOME', env.HOME, '--setenv', 'DATABASE_URL', env.DATABASE_URL, '--setenv', 'MFA_ENCRYPTION_KEY', env.MFA_ENCRYPTION_KEY, '--setenv', 'PURCHASE_FIELD_ENCRYPTION_KEY', env.PURCHASE_FIELD_ENCRYPTION_KEY, '--setenv', 'PROVISION_ENCRYPTION_KEY', env.PROVISION_ENCRYPTION_KEY, '--setenv', 'HTTP_ADDR', env.HTTP_ADDR, '--setenv', 'PUBLIC_ORIGIN', env.PUBLIC_ORIGIN, '--setenv', 'API_INTERNAL_URL', env.API_INTERNAL_URL, '--setenv', 'NODE_ENV', 'production', '--setenv', 'NEXT_TELEMETRY_DISABLED', '1', '/bin/sh', '/mutable/run.sh'];
  const child = spawn('/usr/bin/setpriv', ['--reuid', String(www), '--regid', String(www), '--init-groups', '--', ...bwrap], { stdio: 'inherit', detached: true });
  const stop = async () => { try { process.kill(-child.pid, 'SIGTERM'); } catch {} await new Promise(resolve => child.once('close', resolve)); };
  try { await waitFor(async () => (await lstat(path.join(mutable, 'migrations-done')).catch(() => null))?.isFile(), 900, 'same-package migrations'); await fixtureCommand(options.fixtureDir, 'prepare', 1); await waitFor(async () => (await fixtureResult(options.fixtureDir, 'prepared').catch(() => null))?.ok, 90, 'fixture prepare'); await writeFile(path.join(mutable, 'control/fixture-prepared'), 'ready\n'); await waitFor(async () => (await lstat(path.join(mutable, 'page-report.json')).catch(() => null))?.isFile(), 60, 'Web page/chunk acceptance'); await fixtureCommand(options.fixtureDir, 'verify-first', 2); await waitFor(async () => { const result = await fixtureResult(options.fixtureDir, 'first').catch(() => null); return result?.ok ? result : null; }, 90, 'worker first acceptance'); await writeFile(path.join(mutable, 'control/stop-worker'), 'stop\n'); await waitFor(async () => !(await lstat(path.join(mutable, 'control/stop-worker')).catch(() => null)), 15, 'worker stop'); await fixtureCommand(options.fixtureDir, 'verify-restart', 3); await writeFile(path.join(mutable, 'control/restart-worker'), 'restart\n'); await waitFor(async () => !(await lstat(path.join(mutable, 'control/restart-worker')).catch(() => null)), 15, 'worker restart'); await fixtureCommand(options.fixtureDir, 'verify-restart', 4); await waitFor(async () => (await fixtureResult(options.fixtureDir, 'restart').catch(() => null))?.ok, 90, 'worker restart acceptance'); await writeFile(path.join(mutable, 'control/finish'), 'finish\n'); await fixtureCommand(options.fixtureDir, 'finish', 5); await stop(); await waitFor(async () => (await fixtureResult(options.fixtureDir, 'cleaned').catch(() => null))?.ok, 30, 'fixture cleanup'); try { process.kill(fixtureProcess.pid, 'SIGTERM'); } catch {} return { pageReport: await jsonFile(path.join(mutable, 'page-report.json')), processExit: true }; } catch (error) { await fixtureCommand(options.fixtureDir, 'abort', 99).catch(() => {}); try { process.kill(-child.pid, 'SIGTERM'); } catch {} try { process.kill(fixtureProcess.pid, 'SIGTERM'); } catch {} throw error; }
}
function startFixture(options) {
  const databaseURL = process.env.ZJMFGO_TEST_DATABASE_URL;
  requireThat(databaseURL, 'BLOCKED: ZJMFGO_TEST_DATABASE_URL is required for the isolated fixture');
  validateTestDSN(databaseURL);
  const env = { ...process.env, ZJMFGO_RELEASE_RUNTIME_FIXTURE: '1', ZJMFGO_TEST_DATABASE_URL: databaseURL, ZJMFGO_RELEASE_RUNTIME_FIXTURE_DIR: path.resolve(options.fixtureDir) };
  const child = spawn('/usr/local/btgo/bin/go', ['test', '-run', '^TestReleaseRuntimeFixture$', '-count=1', '-timeout=15m', './internal/store'], { cwd: path.resolve(options.source), env, stdio: ['ignore', 'pipe', 'pipe'], detached: true });
  let output = ''; child.stdout.on('data', chunk => { output += chunk.toString().slice(-4096); }); child.stderr.on('data', chunk => { output += chunk.toString().slice(-4096); });
  child.once('close', code => { if (code !== 0) writeFile(path.join(options.fixtureDir, 'fixture-failure.txt'), `fixture exited ${code}\n`).catch(() => {}); });
  return child;
}
export async function acceptRuntime(options) {
  requireThat(process.platform === 'linux' && process.arch === 'x64', 'BLOCKED: Linux x64 required'); requireThat(process.getuid?.() === 0, 'BLOCKED: controller requires root to enter www runtime'); await lstat('/usr/bin/bwrap').catch(() => { throw new Error('BLOCKED: bubblewrap unavailable'); });
  const manifest = await verifyRuntimeCandidate(path.resolve(options.sealed), options.manifestSHA, path.resolve(options.source));
  requireThat(manifest.launch?.runtimeUser === 'www', 'Runtime package must declare www'); requireThat(Number(options.apiPort) > 1024 && Number(options.webPort) > 1024 && options.apiPort !== options.webPort, 'Independent ports required');
  if (!options.execute) return { status: 'PREFLIGHT_ONLY', manifest: manifest.source.commit, ports: { api: Number(options.apiPort), web: Number(options.webPort) } };
  await mkdir(path.resolve(options.fixtureDir), { mode: 0o700 });
  requireThat((await readdir(path.resolve(options.fixtureDir))).length === 0, 'Fixture directory must start empty');
  const fixtureProcess = startFixture(options);
  const fixture = await waitFor(async () => jsonFile(path.join(options.fixtureDir, 'schema-ready.json')).catch(() => null), 90, 'fixture schema');
  run('/bin/chown', ['-R', 'www:www', path.resolve(options.fixtureDir)]);
  const fixtureConfig = path.join(options.fixtureDir, 'config.json');
  if (options.config !== fixtureConfig) options.config = fixtureConfig;
  validateTestDSN((await jsonFile(options.config)).values.DATABASE_URL, fixture.schema); await assertSocket(options.pgSocket, validateTestDSN((await jsonFile(options.config)).values.DATABASE_URL, fixture.schema).port);
  const parent = path.resolve(path.dirname(options.sealed)); const runRoot = path.join(parent, `.runtime-run-${process.pid}`); await mkdir(runRoot, { mode: 0o700 }); const release = path.join(runRoot, 'release'); await ownedCopy(path.resolve(options.sealed), release); const before = await runtimeInventory(path.resolve(options.sealed));
  try { const env = await controlledRuntimeEnvironment(manifest.launch, options.config, { uid: www }); const result = await execute(options, manifest, env, release, fixture, fixtureProcess); const after = await runtimeInventory(path.resolve(options.sealed)); requireThat(JSON.stringify(before) === JSON.stringify(after), 'Sealed candidate changed during acceptance'); return { status: 'ACCEPTED_PREPARATION_ONLY', ...result, sealedSHA256: sha256(JSON.stringify(after)) }; } finally { await rm(runRoot, { recursive: true, force: true }); }
}
if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) { try { console.log(JSON.stringify(await acceptRuntime(args()), null, 2)); } catch (error) { console.error(`[release-runtime-acceptance] ${error.message}`); process.exitCode = 1; } }
