#!/usr/bin/env node
import { createHash } from 'node:crypto';
import { mkdtemp, readFile, readdir, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { contractBuildEnvironment, runContractCommand } from './contract-process.mjs';
import { validateModulePair, validateModuleProfile } from './module-manifest-lib.mjs';

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const runtimeEnvironment = Object.freeze({ PATH: '/usr/bin:/bin', LANG: 'C', LC_ALL: 'C' });

export async function inspectModuleBinary(binary, directory, name) {
  // Deny network syscalls before execution, then reject even an attempted one.
  // Trace stderr shares the output limit; a loop cannot grow an unbounded file.
  // Following all threads/children also catches networking from package init.
  const result = await runContractCommand('strace', ['-f', '-qqq', '-e', 'trace=%network', '-e', 'inject=%network:error=EPERM', '-e', 'signal=none', binary, '--module-manifest'], {
    cwd: directory, env: runtimeEnvironment, timeoutMs: 5000, maxBytes: 2 * 1024 * 1024, label: `${name} module introspection`,
  });
  if (result.stderr.includes('(INJECTED)')) throw new Error(`${name} module introspection attempted network access`);
  // strace 5.x can emit this diagnostic when a Go thread exits while another
  // thread terminates the process. It contains no decoded syscall. Network
  // attempts remain denied and their INJECTED records are rejected above.
  const stderr = result.stderr.split('\n').filter(line => !/^\[pid\s+\d+\]\s+\?\?\?\?\(\s+<detached \.\.\.>$/.test(line)).join('\n');
  if (result.status !== 0 || result.signal || stderr !== '') throw new Error(`${name} module introspection failed`);
  try { return JSON.parse(result.stdout); } catch { throw new Error(`${name} module introspection returned invalid JSON`); }
}

export async function checkModuleBinaries() {
  if (process.platform !== 'linux') throw new Error('Module binary gate requires Linux and strace');
  const profile = JSON.parse(await readFile(path.join(repository, 'audit/module-build-profile.json'), 'utf8'));
  if (validateModuleProfile(profile).length !== 9) throw new Error('Default build profile must contain exactly nine modules');
  const temporary = await mkdtemp(path.join(os.tmpdir(), 'goidc-module-binaries-'));
  try {
    const manifests = {}, binaries = {};
    for (const name of ['api', 'worker']) {
      const binary = path.join(temporary, name);
      const result = await runContractCommand('bash', ['scripts/go.sh', 'build', '-mod=readonly', '-trimpath', '-o', binary, `./cmd/${name}`], {
        cwd: repository, env: contractBuildEnvironment(), timeoutMs: 300000, label: `${name} module build`,
      });
      if (result.status !== 0 || result.signal) throw new Error(`${name} module build failed`);
      binaries[name] = createHash('sha256').update(await readFile(binary)).digest('hex');
      manifests[name] = await inspectModuleBinary(binary, temporary, name);
    }
    validateModulePair(profile, manifests);
    const expected = ['api', 'worker'];
    if (JSON.stringify((await readdir(temporary)).sort()) !== JSON.stringify(expected)) throw new Error('Module introspection created unexpected files');
    return { format: 'goidc-module-binary-check-v1', profile: profile.profile, modules: profile.modules.length, binaries, descriptors: manifests.api.modules, networkSyscalls: 0, runtimeConfiguration: 'empty', temporaryFilesRemoved: true };
  } finally {
    await rm(temporary, { recursive: true, force: true });
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try {
    if (process.argv.length !== 2) throw new Error('Usage: node scripts/check-module-binaries.mjs');
    process.stdout.write(`${JSON.stringify(await checkModuleBinaries(), null, 2)}\n`);
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}
