#!/usr/bin/env node
import { cp, lstat, mkdir, mkdtemp, readFile, realpath, rename, rm, symlink, writeFile } from 'node:fs/promises';
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { apiTarget, builderFiles, builderVersion, fileHash, git, inventory, noLinks, selectedDigest, sha256, sourceProof, templateFiles, verifyCandidate } from './release-candidate-lib.mjs';

export async function prepareDestination(repo, destination) {
  const output = path.resolve(destination);
  const source = await realpath(repo);
  if (output === source || source.startsWith(`${output}/`) || (output.startsWith(`${source}/`) && !output.startsWith(`${source}/output/`))) throw new Error('Output must be new /tmp directory or repository output/ child');
  if (!output.startsWith('/tmp/') && !output.startsWith(`${source}/output/`)) throw new Error('Output outside permitted candidate directories');
  // Check existing ancestors before mkdir can follow a link and create files
  // outside the intended output tree.
  let ancestor = path.dirname(output);
  while (true) {
    try { await noLinks(ancestor); break; }
    catch (error) {
      if (error.code !== 'ENOENT') throw error;
      ancestor = path.dirname(ancestor);
    }
  }
  await mkdir(path.dirname(output), { recursive: true });
  await noLinks(path.dirname(output));
  await mkdir(output, { mode: 0o700 }); // EEXIST: never overwrite a prior build.
  return output;
}
export async function buildCandidate({ source, revision, output, dependencies, apiInternalURL }) {
  const target = apiTarget(apiInternalURL);
  source = await realpath(source);
  const proof = await sourceProof(source, revision);
  const builder = selectedDigest(proof.files, builderFiles);
  // Running an uncommitted or different builder cannot label an older commit.
  for (const file of builder.files) if (await fileHash(path.join(path.dirname(fileURLToPath(import.meta.url)), '..', file.path)) !== file.sha256) throw new Error('Running builder differs from candidate commit');
  dependencies = await noLinks(path.resolve(dependencies));
  if (!(await lstat(dependencies)).isDirectory()) throw new Error('Shared dependencies must be a real directory');
  const lock = await readFile(path.join(source, 'web/package-lock.json'));
  const committedLock = proof.files.find(file => file.path === 'web/package-lock.json');
  if (sha256(lock) !== committedLock?.sha256) throw new Error('Selected commit lockfile differs from dependency checkout');
  // The dependency owner's tracked lock must match; no npm install/ci is run.
  if (await fileHash(path.join(dependencies, '../package-lock.json')) !== committedLock.sha256) throw new Error('Shared dependency owner lockfile mismatch');
  const depsInventory = await inventory(dependencies, { dependencies: true });
  const depsSHA = sha256(JSON.stringify(depsInventory));
  const root = await prepareDestination(source, output);
  let checkout;
  try {
    checkout = await mkdtemp(path.join(root, '.checkout-'));
    const env = { PATH: process.env.PATH, HOME: process.env.HOME, LANG: 'C.UTF-8', NEXT_TELEMETRY_DISABLED: '1', API_INTERNAL_URL: target, NODE_ENV: 'production' };
    function run(command, args, cwd = checkout, capture = false) {
      const result = spawnSync(command, args, { cwd, env, encoding: 'utf8', stdio: capture ? 'pipe' : 'inherit', maxBuffer: 16 * 1024 * 1024 });
      if (result.error || result.status !== 0) throw new Error(`Build command failed: ${command} ${args.join(' ')}${capture ? `\n${result.stderr ?? ''}` : ''}`);
      return capture ? result.stdout.trim() : '';
    }
    run('git', ['clone', '--shared', '--no-checkout', '--quiet', source, checkout], root);
    run('git', ['checkout', '--detach', '--quiet', proof.commit]);
    if (JSON.stringify(await sourceProof(checkout, proof.commit)) !== JSON.stringify(proof)) throw new Error('Fixed checkout source mismatch');
    const tools = { go: run('bash', ['scripts/go.sh', 'version'], checkout, true), node: process.version, npm: run('npm', ['--version'], checkout, true), platform: `${process.platform}/${process.arch}` };
    const binaries = {};
    await mkdir(path.join(root, 'bin'));
    for (const name of ['api', 'worker', 'migrate']) {
      run('bash', ['scripts/go.sh', 'build', '-mod=readonly', '-trimpath', '-buildvcs=true', '-o', path.join(root, 'bin', name), `./cmd/${name}`]);
      binaries[name] = run('bash', ['scripts/go.sh', 'version', '-m', path.join(root, 'bin', name)], checkout, true).split('\n').slice(1).join('\n');
      if (!binaries[name].includes(`vcs.revision=${proof.commit}`) || !binaries[name].includes('vcs.modified=false')) throw new Error(`Go build source proof invalid: ${name}`);
    }
    await symlink(dependencies, path.join(checkout, 'web/node_modules'));
    const nextVersion = JSON.parse(await readFile(path.join(dependencies, 'next/package.json'), 'utf8')).version;
    run('npm', ['run', 'build', '--', '--webpack'], path.join(checkout, 'web'));
    // Build caches are not runtime assets; all remaining .next production files
    // are moved as one tree and inventoried, never cherry-picked by extension.
    await rm(path.join(checkout, 'web/.next/cache'), { recursive: true, force: true });
    await mkdir(path.join(root, 'web'));
    await rename(path.join(checkout, 'web/.next'), path.join(root, 'web/.next'));
    for (const name of ['public', 'package.json', 'package-lock.json', 'next.config.ts']) await cp(path.join(checkout, 'web', name), path.join(root, 'web', name), { recursive: true, dereference: false });
    await mkdir(path.join(root, 'migrations'));
    const migrations = proof.files.filter(file => /^db\/migrations\/\d.*\.sql$/.test(file.path));
    for (const file of migrations) await cp(path.join(checkout, file.path), path.join(root, file.path.slice(3)));
    await cp(path.join(checkout, 'deploy'), path.join(root, 'deploy'), { recursive: true, dereference: false });
    if (JSON.stringify(await sourceProof(checkout, proof.commit)) !== JSON.stringify(proof)) throw new Error('Build modified tracked source');
    if (JSON.stringify(await sourceProof(source, proof.commit)) !== JSON.stringify(proof)) throw new Error('Invoking source changed during build');
    if (sha256(JSON.stringify(await inventory(dependencies, { dependencies: true }))) !== depsSHA) throw new Error('Shared dependencies changed during build');
    await rm(checkout, { recursive: true, force: true });
    checkout = undefined;
    const manifest = {
      format: 'zjmfgo-release-candidate-v1', createdAt: new Date().toISOString(), source: proof,
      builder: { version: builderVersion, ...builder }, templates: selectedDigest(proof.files, templateFiles), tools,
      locks: { goMod: proof.files.find(file => file.path === 'go.mod').sha256, goSum: proof.files.find(file => file.path === 'go.sum').sha256, web: committedLock.sha256 },
      sharedDependencies: { sha256: depsSHA, files: depsInventory.length, policy: 'read-only reuse; full tree before/after; trusted owner required; no installation' },
      commands: { go: 'bash scripts/go.sh build -mod=readonly -trimpath -buildvcs=true', web: 'npm run build -- --webpack' }, binaries, migrations,
      web: { bundler: 'webpack', nextVersion, apiInternalURL: target, buildID: (await readFile(path.join(root, 'web/.next/BUILD_ID'), 'utf8')).trim(), cacheExcluded: true, runtimeDependenciesIncluded: false },
      artifacts: await inventory(root),
      scope: 'Local preparation only. No deploy, services, proxy, database, old chunk retention or source authenticity of uncontrolled external builds proven.',
    };
    const raw = JSON.stringify(manifest, null, 2) + '\n';
    await writeFile(path.join(root, 'manifest.json'), raw, { mode: 0o600, flag: 'wx' });
    const digest = sha256(raw);
    await verifyCandidate(root, digest, source);
    return { root, commit: proof.commit, manifestSHA256: digest };
  } catch (error) {
    await rm(root, { recursive: true, force: true }); // Only our exclusively created output.
    throw error;
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try {
    const args = process.argv.slice(2);
    const options = {};
    const names = { '--source': 'source', '--commit': 'revision', '--output': 'output', '--shared-deps': 'dependencies', '--api-internal-url': 'apiInternalURL' };
    for (let i = 0; i < args.length; i += 2) {
      if (!names[args[i]] || !args[i + 1] || options[names[args[i]]]) throw new Error('Usage: node scripts/build-release-candidate.mjs --source REPO --commit COMMIT --output NEW_OUTPUT_DIR --shared-deps REAL_NODE_MODULES --api-internal-url ORIGIN');
      options[names[args[i]]] = args[i + 1];
    }
    if (Object.keys(options).length !== 5) throw new Error('All five build options are required');
    console.log(JSON.stringify(await buildCandidate(options), null, 2));
  } catch (error) { console.error(`[release-candidate] ${error.message}`); process.exitCode = 1; }
}
