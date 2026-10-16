#!/usr/bin/env node
import { cp, lstat, mkdir, readFile, readlink, realpath, rename, rm, writeFile } from 'node:fs/promises';
import { execFileSync, spawnSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { prepareDestination } from './build-release-candidate.mjs';
import { fileHash, git, inventory, noLinks, selectedDigest, sha256, sourceProof } from './release-candidate-lib.mjs';
import { collectTraceEvidence, controlledPATH, freeSpace, guardReserve, makeLaunchDescription, ordinaryFiles, pinnedJSON, runControlled, runtimeTarget, standaloneConfig, validateBudget, validateDependencies, validateRewrite } from './release-runtime-builder-lib.mjs';

const scriptRoot = path.dirname(fileURLToPath(import.meta.url));
const goCommand = '/usr/local/btgo/bin/go';
const platform = `${process.platform}/${process.arch}`;
function syncRun(command, args, cwd, env, capture = false) {
  const result = spawnSync(command, args, { cwd, env, encoding: 'utf8', stdio: capture ? 'pipe' : 'inherit', maxBuffer: 16 * 1024 * 1024 });
  if (result.error || result.status !== 0) throw new Error(`Controlled command failed: ${path.basename(command)} (${result.status})`);
  return capture ? result.stdout.trim() : '';
}
async function assertOwnedInputs(request) {
  for (const [filename, digest] of [[request.node, request.nodeProof.sha256], [request.nodeLicense, request.nodeProof.licenseSHA256]]) {
    await noLinks(filename);
    if (!(await lstat(filename)).isFile() || await fileHash(filename) !== digest) throw new Error('Controlled Node/license digest mismatch');
  }
  if (!((await lstat(request.node)).mode & 0o111)) throw new Error('Node executable bit missing');
  if (request.nodeProof.format !== 'zjmfgo-runtime-node-provenance-v1' || !request.nodeProof.source?.trim() || request.nodeProof.platform !== platform) throw new Error('BLOCKED: controlled Node provenance required');
}
async function elfBaseline(root, files, env) {
  const binaries = [];
  for (const name of files) {
    const filename = path.join(root, name);
    const dynamic = syncRun('/usr/bin/readelf', ['-l', '-d', filename], root, env, true);
    binaries.push({ path: name, sha256: await fileHash(filename), interpreter: dynamic.match(/Requesting program interpreter: ([^\]]+)/)?.[1] ?? null, needed: [...dynamic.matchAll(/Shared library: \[([^\]]+)\]/g)].map(match => match[1]).sort() });
  }
  return { platform, requirements: [...new Set(binaries.flatMap(item => [...(item.interpreter ? [`loader ${item.interpreter}`] : []), ...item.needed.map(lib => `system library ${lib}`)]))].sort(), elf: binaries, compatibility: 'Exact target host loader/library/architecture check required before launch; no arbitrary-distribution portability claim' };
}

// This entry can only execute beneath our parent-created private mount
// namespace. Refuse the original parent namespace before any mount mutation.
async function namespaceBuild(requestPath) {
  const request = JSON.parse(await readFile(requestPath, 'utf8'));
  if (process.ppid !== request.parentPID || await readlink('/proc/self/ns/mnt') === await readlink(`/proc/${process.ppid}/ns/mnt`) || await readlink(`/proc/${process.ppid}/ns/mnt`) !== request.parentMountNamespace) throw new Error('Refusing build outside its private mount namespace');
  const { root, checkout, dependencies, proof, budget, initialFreeBytes, node, tools } = request;
  const env = { PATH: controlledPATH, HOME: path.join(root, '.scratch/home'), LANG: 'C.UTF-8', NODE_ENV: 'production', NEXT_TELEMETRY_DISABLED: '1', API_INTERNAL_URL: request.apiInternalURL, ZJMFGO_RUNTIME_BUILD: '1', GOTOOLCHAIN: 'local', GOENV: 'off', GOPROXY: 'off', GOSUMDB: 'off', GOCACHE: path.join(root, '.scratch/go-build'), GOMODCACHE: request.goModCache };
  await mkdir(env.HOME, { recursive: true });
  const mounted = path.join(checkout, 'web/node_modules');
  await mkdir(mounted);
  syncRun('/usr/bin/mount', ['--bind', request.goModCache, request.goModCache], root, env);
  syncRun('/usr/bin/mount', ['-o', 'remount,bind,ro', request.goModCache], root, env);
  syncRun('/usr/bin/mount', ['--bind', dependencies, mounted], root, env);
  syncRun('/usr/bin/mount', ['-o', 'remount,bind,ro', mounted], root, env);
  const mountInfo = (await readFile('/proc/self/mountinfo', 'utf8')).split('\n').find(line => line.split(' ')[4] === mounted);
  if (!mountInfo || !mountInfo.split(' ')[5].split(',').includes('ro')) throw new Error('Readonly dependency input mount not proven');
  await assertOwnedInputs(request);
  await validateDependencies(mounted, request.dependencyProof, request.lockSHA256, tools);
  const run = (command, args, cwd = checkout, capture = false) => syncRun(command, args, cwd, env, capture);
  const artifact = path.join(root, 'artifact');
  await mkdir(path.join(artifact, 'bin'), { recursive: true });
  const binaries = {};
  for (const name of ['api', 'worker', 'migrate']) {
    await guardReserve(root, budget, initialFreeBytes);
    run('bash', ['scripts/go.sh', 'build', '-mod=readonly', '-trimpath', '-buildvcs=true', '-o', path.join(artifact, 'bin', name), `./cmd/${name}`]);
    const buildInfo = run(goCommand, ['version', '-m', path.join(artifact, 'bin', name)], checkout, true).split('\n').slice(1).join('\n');
    if (!buildInfo.includes(`vcs.revision=${proof.commit}`) || !buildInfo.includes('vcs.modified=false')) throw new Error(`Go binary source proof invalid: ${name}`);
    binaries[name] = { path: `artifact/bin/${name}`, buildInfo };
  }
  run(node, ['../scripts/check-web-build-runtime.mjs'], path.join(checkout, 'web'));
  run(node, ['node_modules/next/dist/bin/next', 'build', '--webpack'], path.join(checkout, 'web'));
  await guardReserve(root, budget, initialFreeBytes);
  const standalone = path.join(checkout, 'web/.next/standalone');
  await noLinks(standalone);
  const serverFiles = JSON.parse(await readFile(path.join(checkout, 'web/.next/required-server-files.json'), 'utf8'));
  // Derive the actual app root from the framework's same-build metadata.
  const appRelative = path.relative(checkout, serverFiles.appDir);
  if (!appRelative || appRelative.startsWith('..') || path.isAbsolute(appRelative) || serverFiles.relativeAppDir !== appRelative || serverFiles.config.outputFileTracingRoot !== checkout) throw new Error('Standalone app/tracing root metadata mismatch');
  const app = path.join(standalone, appRelative);
  const effectiveConfig = standaloneConfig(await readFile(path.join(app, 'server.js'), 'utf8'));
  const routes = JSON.parse(await readFile(path.join(app, '.next/routes-manifest.json'), 'utf8'));
  validateRewrite(routes, request.apiInternalURL);
  if (effectiveConfig.rewrites) validateRewrite(effectiveConfig, request.apiInternalURL);
  await cp(path.join(checkout, 'web/public'), path.join(app, 'public'), { recursive: true, dereference: false, errorOnExist: true, force: false });
  await cp(path.join(checkout, 'web/.next/static'), path.join(app, '.next/static'), { recursive: true, dereference: false, errorOnExist: true, force: false });
  // Explicit same-build metadata; never load or ship TS configuration at runtime.
  await cp(path.join(checkout, 'web/.next/required-server-files.json'), path.join(app, '.next/required-server-files.json'));
  await mkdir(path.join(artifact, 'meta'), { recursive: true });
  const traceEvidence = await collectTraceEvidence({ checkout, standalone, metaRoot: path.join(artifact, 'meta'), source: proof, dependencies: request.dependencyProof });
  await rename(standalone, path.join(artifact, 'web'));
  const web = { appRoot: `artifact/web/${appRelative}`, entry: `artifact/web/${appRelative}/server.js`, buildID: (await readFile(path.join(artifact, 'web', appRelative, '.next/BUILD_ID'), 'utf8')).trim(), bundler: 'webpack', nextVersion: JSON.parse(await readFile(path.join(mounted, 'next/package.json'), 'utf8')).version, apiInternalURL: request.apiInternalURL, effectiveStandaloneConfigSHA256: sha256(JSON.stringify(effectiveConfig)), routesManifestSHA256: await fileHash(path.join(artifact, 'web', appRelative, '.next/routes-manifest.json')), runtimeDependenciesIncluded: true };
  await mkdir(path.join(artifact, 'runtime'));
  await cp(node, path.join(artifact, 'runtime/node'));
  await cp(request.nodeLicense, path.join(artifact, 'runtime/LICENSE'));
  await writeFile(path.join(artifact, 'meta/dependencies.json'), request.dependencyProofRaw, { flag: 'wx' });
  await writeFile(path.join(artifact, 'meta/node.json'), request.nodeProofRaw, { flag: 'wx' });
  await writeFile(path.join(artifact, 'meta/budget.json'), request.budgetRaw, { flag: 'wx' });
  const migrations = proof.files.filter(file => /^db\/migrations\/\d.*\.sql$/.test(file.path));
  await mkdir(path.join(artifact, 'migrations'));
  for (const file of migrations) await cp(path.join(checkout, file.path), path.join(root, 'artifact', file.path.slice(3)));
  const elfFiles = [...Object.values(binaries).map(x => x.path), 'artifact/runtime/node', ...(await ordinaryFiles(path.join(artifact, 'web'))).filter(file => file.endsWith('.node')).map(file => `artifact/web/${file}`)];
  const host = await elfBaseline(root, elfFiles, env);
  if (!host.requirements.length) host.requirements.push('Linux kernel/architecture compatible with declared ELF files; no dynamic loader detected');
  const runtime = { node: { path: 'artifact/runtime/node', version: tools.node, source: request.nodeProof.source, licensePath: 'artifact/runtime/LICENSE', sha256: request.nodeProof.sha256, provenancePath: 'artifact/meta/node.json', provenanceSHA256: request.nodeProvenanceSHA256 }, host };
  const description = makeLaunchDescription({ web, binaries, runtime, webOrigin: request.webOrigin, runtimeUser: request.runtimeUser });
  await mkdir(path.join(root, 'launch'));
  await writeFile(path.join(root, 'launch/description.json'), JSON.stringify(description, null, 2) + '\n', { flag: 'wx' });
  if (JSON.stringify(await sourceProof(checkout, proof.commit)) !== JSON.stringify(proof)) throw new Error('Build changed tracked checkout source');
  await validateDependencies(mounted, request.dependencyProof, request.lockSHA256, tools);
  await assertOwnedInputs(request);
  await writeFile(path.join(root, '.build-result.json'), JSON.stringify({ binaries, web, runtime, migrations, traceEvidence }), { flag: 'wx' });
}

export async function buildRuntimeCandidate(options) {
  if (platform !== 'linux/x64') throw new Error('BLOCKED: this builder currently supports the reviewed Linux x64 host only');
  const source = await realpath(options.source);
  const proof = await sourceProof(source, options.revision);
  if (git(source, 'rev-parse', 'HEAD') !== proof.commit) throw new Error('Source HEAD must be the selected fixed commit');
  const { runtimeBuilderFiles, runtimeInventory, verifyRuntimeCandidate } = await import('./release-runtime-lib.mjs');
  const builder = { version: 2, ...selectedDigest(proof.files, runtimeBuilderFiles) };
  for (const file of builder.files) if (await fileHash(path.join(scriptRoot, '..', file.path)) !== file.sha256) throw new Error('Running builder/config differs from selected commit');
  const depInput = await pinnedJSON(options.dependencyProvenance, options.dependencyProvenanceSHA256);
  const nodeInput = await pinnedJSON(options.nodeProvenance, options.nodeProvenanceSHA256);
  const budgetInput = await pinnedJSON(options.budget, options.budgetSHA256);
  const targets = runtimeTarget(options.apiInternalURL, options.webOrigin);
  const dependencies = await noLinks(path.resolve(options.dependencies));
  const node = await noLinks(path.resolve(options.node));
  const nodeLicense = await noLinks(path.resolve(options.nodeLicense));
  const tools = { go: execFileSync(goCommand, ['version'], { encoding: 'utf8', env: { PATH: controlledPATH, GOTOOLCHAIN: 'local', GOENV: 'off' } }).trim(), node: nodeInput.value.version, npm: depInput.value.installation?.npmVersion, platform };
  const locks = { goMod: proof.files.find(x => x.path === 'go.mod').sha256, goSum: proof.files.find(x => x.path === 'go.sum').sha256, web: proof.files.find(x => x.path === 'web/package-lock.json').sha256 };
  const preliminary = { node, nodeLicense, nodeProof: nodeInput.value };
  await assertOwnedInputs(preliminary);
  if (execFileSync(node, ['--version'], { encoding: 'utf8', env: { PATH: controlledPATH } }).trim() !== tools.node) throw new Error('Controlled Node actual version mismatch');
  await validateDependencies(dependencies, depInput.value, locks.web, tools);
  // No mount mutation here: verify namespace capability before output/clone.
  if (spawnSync('/usr/bin/unshare', ['--mount', '--propagation', 'private', 'true'], { env: { PATH: controlledPATH }, stdio: 'ignore' }).status !== 0) throw new Error('BLOCKED: private mount namespace unavailable; no symlink/full-copy fallback');
  const parent = await noLinks(path.dirname(path.resolve(options.output)));
  const available = await freeSpace(parent);
  const budget = budgetInput.value;
  const budgetEvidence = validateBudget(budget, available);
  const checkoutBytes = proof.files.reduce((sum, file) => sum + file.bytes, 0);
  if (checkoutBytes > budget.components.checkout || (await lstat(node)).size > budget.components.sealed) throw new Error('BLOCKED: measured source/Node exceeds component budget');
  const summary = { commit: proof.commit, tree: proof.tree, dependencyInputTreeSHA256: depInput.value.inputTreeSHA256, nodeSHA256: nodeInput.value.sha256, budgetSHA256: options.budgetSHA256, budgetEvidence, target: targets, scope: 'Preparation only; no deployment, services, database, or runtime acceptance' };
  if (!options.execute) return { ...summary, status: 'PREFLIGHT_ONLY', fullBuildExecuted: false };
  const root = await prepareDestination(source, options.output);
  const checkout = path.join(root, '.checkout');
  const env = { PATH: controlledPATH, LANG: 'C.UTF-8', GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null' };
  const run = (command, args) => runControlled(command, args, { cwd: root, env, root, budget, initialFreeBytes: available.bytes });
  try {
    await run('git', ['clone', '--shared', '--no-checkout', '--quiet', source, checkout]);
    await run('git', ['-C', checkout, 'checkout', '--detach', '--quiet', proof.commit]);
    if (JSON.stringify(await sourceProof(checkout, proof.commit)) !== JSON.stringify(proof)) throw new Error('Fixed checkout source proof mismatch');
    const goModCache = execFileSync(goCommand, ['env', 'GOMODCACHE'], { encoding: 'utf8', env: { PATH: controlledPATH, HOME: process.env.HOME, GOENV: 'off', GOTOOLCHAIN: 'local' } }).trim();
    await noLinks(goModCache);
    const request = { root, checkout, dependencies, proof, builder, tools, budget, initialFreeBytes: available.bytes, parentPID: process.pid, parentMountNamespace: await readlink('/proc/self/ns/mnt'), node, nodeLicense, nodeProof: nodeInput.value, nodeProofRaw: nodeInput.raw.toString(), nodeProvenanceSHA256: options.nodeProvenanceSHA256, dependencyProof: depInput.value, dependencyProofRaw: depInput.raw.toString(), lockSHA256: locks.web, budgetRaw: budgetInput.raw.toString(), goModCache, apiInternalURL: targets.api, webOrigin: targets.web, runtimeUser: options.runtimeUser };
    const requestPath = path.join(root, '.build-request.json');
    await writeFile(requestPath, JSON.stringify(request), { mode: 0o600, flag: 'wx' });
    await run('/usr/bin/unshare', ['--mount', '--propagation', 'private', node, fileURLToPath(import.meta.url), '--namespace-worker', requestPath]);
    const built = JSON.parse(await readFile(path.join(root, '.build-result.json'), 'utf8'));
    // unshare exit guarantees the private readonly bind no longer exists.
    for (const name of ['.checkout', '.scratch', '.build-request.json', '.build-result.json']) await rm(path.join(root, name), { recursive: true, force: true });
    if (JSON.stringify(await sourceProof(source, proof.commit)) !== JSON.stringify(proof)) throw new Error('Invoking source changed during build');
    await validateDependencies(dependencies, depInput.value, locks.web, tools);
    const manifest = { format: 'zjmfgo-release-candidate-v2', createdAt: new Date().toISOString(), source: proof, builder, tools, locks, binaries: built.binaries, runtime: built.runtime, web: built.web, migrations: built.migrations, dependencies: { provenancePath: 'artifact/meta/dependencies.json', provenanceSHA256: options.dependencyProvenanceSHA256, lockSHA256: locks.web, inputTreeSHA256: depInput.value.inputTreeSHA256, ...built.traceEvidence }, launch: { path: 'launch/description.json' }, commands: { go: 'bash scripts/go.sh build -mod=readonly -trimpath -buildvcs=true', web: 'controlled-node node_modules/next/dist/bin/next build --webpack', isolation: 'private mount namespace; readonly dependency bind inside fixed tracing root' }, budget: { ...budgetEvidence, pinnedSHA256: options.budgetSHA256, finalFree: await guardReserve(root, budget, available.bytes) }, artifacts: await runtimeInventory(root), scope: summary.scope };
    const raw = JSON.stringify(manifest, null, 2) + '\n';
    await writeFile(path.join(root, 'manifest.json'), raw, { mode: 0o600, flag: 'wx' });
    const manifestSHA256 = sha256(raw);
    await verifyRuntimeCandidate(root, manifestSHA256, source, { goCommand });
    // Trust anchor deliberately returned separately; a file inside the sealed
    // directory cannot authenticate itself. Caller must save this result.
    return { ...summary, status: 'SEALED_NOT_RUNTIME_ACCEPTED', root, manifestSHA256 };
  } catch (error) {
    await rm(root, { recursive: true, force: true }); // Exclusively created by this invocation.
    throw error;
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try {
    if (process.argv[2] === '--namespace-worker') {
      if (process.argv.length !== 4) throw new Error('Invalid namespace worker invocation');
      await namespaceBuild(process.argv[3]);
    } else {
      const options = {};
      const names = { '--source': 'source', '--commit': 'revision', '--output': 'output', '--dependencies': 'dependencies', '--dependency-provenance': 'dependencyProvenance', '--dependency-provenance-sha256': 'dependencyProvenanceSHA256', '--node': 'node', '--node-license': 'nodeLicense', '--node-provenance': 'nodeProvenance', '--node-provenance-sha256': 'nodeProvenanceSHA256', '--budget': 'budget', '--budget-sha256': 'budgetSHA256', '--api-internal-url': 'apiInternalURL', '--web-origin': 'webOrigin', '--runtime-user': 'runtimeUser' };
      const args = process.argv.slice(2);
      if (args.at(-1) === '--execute') { options.execute = true; args.pop(); }
      for (let i = 0; i < args.length; i += 2) {
        if (!names[args[i]] || !args[i + 1] || options[names[args[i]]]) throw new Error('Expected explicit named build inputs; --execute must be last (omit for preflight only)');
        options[names[args[i]]] = args[i + 1];
      }
      if (Object.keys(names).some(name => !options[names[name]])) throw new Error(`Required options: ${Object.keys(names).join(' ')} [--execute]`);
      console.log(JSON.stringify(await buildRuntimeCandidate(options), null, 2));
    }
  } catch (error) { console.error(`[release-runtime] ${error.message}`); process.exitCode = 1; }
}
