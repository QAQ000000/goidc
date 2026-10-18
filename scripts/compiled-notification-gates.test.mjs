import test from 'node:test';
import assert from 'node:assert/strict';
import { chmod, link, mkdtemp, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { contractBuildEnvironment, runContractCommand } from './contract-process.mjs';
import { inspectModuleBinary } from './check-module-binaries.mjs';
import { integrationBinaryEnvironment, integrationSuites, unitSuites, validateIntegrationURL, verifyRequiredGoTests } from './test-compiled-notifications.mjs';

const pkg = 'github.com/codex/zjmfgo/internal/example';
const suites = { 'internal/example': ['TestRequired'] };
const events = (...items) => items.map(item => JSON.stringify({ Package: pkg, ...item })).join('\n');

test('required Go suites refuse skipped, missing, incomplete or malformed execution evidence', () => {
  const complete = events({ Action: 'run', Test: 'TestRequired' }, { Action: 'pass', Test: 'TestRequired' }, { Action: 'pass' });
  assert.deepEqual(verifyRequiredGoTests(complete, suites), { tests: 1, packages: 1, skipped: 0 });
  for (const evidence of [
    events({ Action: 'pass' }),
    events({ Action: 'pass', Test: 'TestRequired' }, { Action: 'pass' }),
    events({ Action: 'run', Test: 'TestRequired' }, { Action: 'skip', Test: 'TestRequired' }, { Action: 'pass' }),
    events({ Action: 'run', Test: 'TestRequired' }, { Action: 'pass', Test: 'TestRequired' }),
    `${complete}\n${events({ Action: 'skip', Test: 'TestRequired/conditional' })}`,
    `${complete}\n${events({ Action: 'fail' })}`,
    `${complete}\nnot JSON`,
  ]) assert.throws(() => verifyRequiredGoTests(evidence, suites), /did not|invalid JSON/);
  const required = Object.values(integrationSuites).flat();
  assert.ok(required.includes('TestNotificationCenterUpgradeFrom160'));
  assert.ok(required.includes('TestNotificationCenterProcessRecovery'));
  assert.ok(required.includes('TestTelegramProtocolResultsPersistThroughNotificationStore'));
  assert.equal([...required, ...Object.values(unitSuites).flat()].some(name => /BrowserFixture|ProcessHelper/.test(name)), false);
});

test('integration DSN cannot inherit a business database or connection override', () => {
  const valid = 'postgres://fixture:fixture@127.0.0.1:15432/goidc_ci_test?sslmode=disable';
  assert.equal(validateIntegrationURL(valid), valid);
  for (const raw of [undefined, '', valid.replace('127.0.0.1', 'localhost'), valid.replace('goidc_ci_test', 'business'), valid.replace(':15432', ''), `${valid}&host=production.example`, `${valid}&sslmode=prefer`, `${valid}#fragment`, valid.replace('/goidc_ci_test', '/goidc_ci_test/other')]) {
    assert.throws(() => validateIntegrationURL(raw), /loopback PostgreSQL/);
  }
  const env = contractBuildEnvironment({ PATH: '/usr/bin', HOME: '/tmp/test', DATABASE_URL: 'never-inherit', PROVISION_ENCRYPTION_KEY: 'never-inherit', SMTP_PASSWORD: 'never-inherit', ZJMFGO_TEST_DATABASE_URL: valid, ZJMFGO_BROWSER_READY_FILE: 'never-inherit', ZJMFGO_UPGRADE_API_BINARY: '/never-inherit/api', ZJMFGO_UPGRADE_WORKER_BINARY: '/never-inherit/worker', ZJMFGO_UPGRADE_BINARY_ACCEPTANCE: '1', GOFLAGS: '-unsafe' });
  assert.deepEqual(Object.keys(env).sort(), ['GOENV', 'GOTOOLCHAIN', 'HOME', 'LANG', 'LC_ALL', 'PATH']);
});

test('explicit upgrade binary acceptance cannot silently fall back or accept unsafe executable paths', async t => {
  const temporary = await mkdtemp(path.join(os.tmpdir(), 'goidc-upgrade-env-test-'));
  t.after(() => rm(temporary, { recursive: true, force: true }));
  const api = path.join(temporary, 'api'), worker = path.join(temporary, 'worker');
  for (const filename of [api, worker]) {
    await writeFile(filename, '#!/bin/sh\nexit 0\n');
    await chmod(filename, 0o700);
  }
  const valid = { ZJMFGO_UPGRADE_API_BINARY: api, ZJMFGO_UPGRADE_WORKER_BINARY: worker, ZJMFGO_UPGRADE_BINARY_ACCEPTANCE: '1' };
  assert.deepEqual(await integrationBinaryEnvironment({}), {});
  assert.deepEqual(await integrationBinaryEnvironment(valid), valid);
  for (const key of Object.keys(valid)) {
    const partial = { ...valid }; delete partial[key];
    await assert.rejects(integrationBinaryEnvironment(partial), /requires both/);
  }
  for (const flag of ['', '0', 'true', ' 1']) await assert.rejects(integrationBinaryEnvironment({ ...valid, ZJMFGO_UPGRADE_BINARY_ACCEPTANCE: flag }), /explicit flag 1/);
  await assert.rejects(integrationBinaryEnvironment({ ...valid, ZJMFGO_UPGRADE_API_BINARY: 'relative/api' }), /absolute regular executable/);
  await assert.rejects(integrationBinaryEnvironment({ ...valid, ZJMFGO_UPGRADE_API_BINARY: temporary }), /absolute regular executable/);
  await assert.rejects(integrationBinaryEnvironment({ ...valid, ZJMFGO_UPGRADE_API_BINARY: path.join(temporary, 'missing') }), /absolute regular executable/);
  const linked = path.join(temporary, 'linked');
  await symlink(api, linked);
  await assert.rejects(integrationBinaryEnvironment({ ...valid, ZJMFGO_UPGRADE_API_BINARY: linked }), /absolute regular executable/);
  await chmod(api, 0o600);
  await assert.rejects(integrationBinaryEnvironment(valid), /absolute regular executable/);
  await chmod(api, 0o700);
  await writeFile(api, '');
  await assert.rejects(integrationBinaryEnvironment(valid), /absolute regular executable/);
});

test('contract commands enforce time and output bounds', async () => {
  await assert.rejects(runContractCommand(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { env: {}, timeoutMs: 100, label: 'deadline fixture' }), /deadline/);
  await assert.rejects(runContractCommand(process.execPath, ['-e', 'process.stdout.write("x".repeat(4096))'], { env: {}, timeoutMs: 5000, maxBytes: 512, label: 'output fixture' }), /output limit/);
});

test('real binary inspection denies network syscalls and requires clean JSON exit', async t => {
  const temporary = await mkdtemp(path.join(os.tmpdir(), 'goidc-module-tracer-test-'));
  t.after(() => rm(temporary, { recursive: true, force: true }));
  const binary = path.join(temporary, 'fixture');
  const source = path.join(temporary, 'fixture.go');
  await writeFile(source, `package main
import ("os"; "path/filepath"; "syscall")
func main() {
  if len(os.Args) != 2 || os.Args[1] != "--module-manifest" { os.Exit(3) }
  switch filepath.Base(os.Args[0]) {
  case "network-fixture": _, _ = syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
  case "stderr-fixture": _, _ = os.Stderr.WriteString("unexpected stderr\\n")
  }
  _, _ = os.Stdout.WriteString("{\\"fixture\\":true}\\n")
}
`);
  const build = await runContractCommand('bash', ['scripts/go.sh', 'build', '-o', binary, source], { cwd: path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..'), env: contractBuildEnvironment(), timeoutMs: 60000 });
  assert.equal(build.status, 0, build.stderr);
  assert.deepEqual(await inspectModuleBinary(binary, temporary, 'valid'), { fixture: true });
  const network = path.join(temporary, 'network-fixture'), stderr = path.join(temporary, 'stderr-fixture');
  await link(binary, network);
  await link(binary, stderr);
  await assert.rejects(inspectModuleBinary(network, temporary, 'network'), /attempted network access/);
  await assert.rejects(inspectModuleBinary(stderr, temporary, 'stderr'), /introspection failed/);
});
