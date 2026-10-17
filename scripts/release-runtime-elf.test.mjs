import test from 'node:test';
import assert from 'node:assert/strict';
import { copyFile, mkdir, mkdtemp, readFile, rename, rm, writeFile } from 'node:fs/promises';
import { execFileSync } from 'node:child_process';
import os from 'node:os';
import path from 'node:path';
import { inventory } from './release-candidate-lib.mjs';
import { verifyELFDependencies } from './accept-release-runtime.mjs';

const library = 'libzjmfgo_elf_unit_20261017.so', transitive = 'libzjmfgo_elf_missing_20261017.so';
async function fixture(t, search = '$ORIGIN/deps', recursive = false) {
  const temporary = await mkdtemp(path.join(os.tmpdir(), 'runtime-elf-'));
  t.after(() => rm(temporary, { recursive: true, force: true }));
  const root = path.join(temporary, 'sealed'), app = path.join(root, 'artifact/web/app'), sources = path.join(temporary, 'sources');
  await mkdir(path.join(app, 'deps'), { recursive: true }); await mkdir(sources);
  const compile = (name, code, args) => {
    const source = path.join(sources, name); return writeFile(source, code).then(() => execFileSync('/usr/bin/cc', ['-shared', '-fPIC', '-nostdlib', source, ...args], { stdio: 'pipe' }));
  };
  if (recursive) await compile('nested.c', 'int nested(void){return 7;}', ['-Wl,-soname,' + transitive, '-o', path.join(sources, transitive)]);
  await compile('dep.c', recursive ? 'extern int nested(void); int answer(void){return nested();}' : 'int answer(void){return 7;}', ['-Wl,-soname,' + library, ...(recursive ? ['-Wl,--no-as-needed', path.join(sources, transitive)] : []), '-o', path.join(app, 'deps', library)]);
  await compile('entry.c', 'extern int answer(void); int call(void){return answer();}', ['-Wl,--no-as-needed', path.join(app, 'deps', library), '-Wl,-rpath,' + search, '-o', path.join(app, 'fixture.node')]);
  const manifest = { artifacts: await inventory(root) }, entries = [{ path: 'artifact/web/app/fixture.node' }];
  return { root, app, manifest, entries, check: () => verifyELFDependencies(manifest, root, entries) };
}

test('real readelf resolves inventoried ORIGIN dependency without executing ELF', async t => {
  const f = await fixture(t), result = await f.check();
  assert.equal(result.objects.length, 2);
  assert.deepEqual(result.dependencies.map(row => [row.needed, row.provider, row.path]), [[library, 'bundled', `artifact/web/app/deps/${library}`]]);
});

test('same-name ELF outside actual search path cannot satisfy dependency', async t => {
  const f = await fixture(t); await rename(path.join(f.app, 'deps'), path.join(f.app, 'elsewhere'));
  f.manifest.artifacts = await inventory(f.root);
  await assert.rejects(f.check(), /missing or ambiguous required target system library/);
});

test('ORIGIN traversal outside sealed Web and same-name ambiguity fail closed', async t => {
  const escape = await fixture(t, '$ORIGIN/../../../outside');
  await assert.rejects(escape.check(), /escapes sealed Web/);
  const ambiguous = await fixture(t, '$ORIGIN/deps:$ORIGIN/other');
  await mkdir(path.join(ambiguous.app, 'other')); await copyFile(path.join(ambiguous.app, 'deps', library), path.join(ambiguous.app, 'other', library));
  ambiguous.manifest.artifacts = await inventory(ambiguous.root);
  await assert.rejects(ambiguous.check(), /Ambiguous same-name/);
});

test('bundled dependency must remain mapped, correctly hashed and actual ELF', async t => {
  const tampered = await fixture(t), filename = path.join(tampered.app, 'deps', library);
  await writeFile(filename, Buffer.concat([await readFile(filename), Buffer.from('changed')]));
  await assert.rejects(tampered.check(), /digest mismatch/);
  tampered.manifest.artifacts = await inventory(tampered.root);
  tampered.manifest.artifacts = tampered.manifest.artifacts.filter(row => row.path !== `artifact/web/app/deps/${library}`);
  await assert.rejects(tampered.check(), /unmapped or special/);
  const nonELF = await fixture(t); await writeFile(path.join(nonELF.app, 'deps', library), 'not an ELF');
  nonELF.manifest.artifacts = await inventory(nonELF.root);
  await assert.rejects(nonELF.check(), /readelf|ELF/);
});

test('packaged ELF recursive NEEDED cannot hide a missing host dependency', async t => {
  const f = await fixture(t, '$ORIGIN/deps', true);
  await assert.rejects(f.check(), /missing or ambiguous required target system library/);
});

test('absolute build-machine search directories are recorded but never searched', async t => {
  const f = await fixture(t, '/tmp/foreign-build-tree:$ORIGIN/deps');
  const result = await f.check();
  assert.deepEqual(result.ignoredSearchPaths, [{ object: 'artifact/web/app/fixture.node', path: '/tmp/foreign-build-tree' }]);
  assert.equal(result.dependencies[0].provider, 'bundled');
});
