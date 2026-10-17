import test from 'node:test';
import assert from 'node:assert/strict';
import { chmod, lstat, mkdtemp, readFile, rm, symlink } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { g09RuntimeFiles, g09MigrationInputs, validateG09RuntimeDSN, validateG09Command, writeG09ControlJSON, readG09ControlJSON } from './g09-runtime-contract.mjs';
import { parseG09RuntimeOptions } from './accept-g09-runtime.mjs';

const schema = 'zjmfgo_test_0123456789abcdef';
const runId = 'g09_candidate_0123456789abcdef01234567';
const raw = 'postgres://postgres@127.0.0.1:55439/zjmfgo_isolated_test?sslmode=disable';
const scoped = `${raw}&search_path=${schema}&application_name=g09_candidate_${schema}&connect_timeout=5`;
test('G09 host and runtime DSNs refuse identity overrides and ambiguous scope', () => {
  assert.deepEqual(validateG09RuntimeDSN(raw), { port: 55439, database: 'zjmfgo_isolated_test' });
  assert.deepEqual(validateG09RuntimeDSN(scoped, schema), validateG09RuntimeDSN(raw));
  for (const input of [raw+'&host=/tmp',raw+'&service=x',raw+'&options=-csearch_path=public',raw+'&sslmode=disable',raw.replace('_test?','?'),raw.replace('127.0.0.1','localhost'),raw.replace('55439','543'),raw+'#secret',raw.replace('zjmfgo_isolated_test','x%2Ftest_test')]) assert.throws(() => validateG09RuntimeDSN(input));
  for (const input of [raw,scoped+'&hostaddr=127.0.0.1',scoped.replace(schema,'public'),scoped.replace('connect_timeout=5','connect_timeout=0'),scoped+'&search_path=public']) assert.throws(() => validateG09RuntimeDSN(input,schema));
  assert.throws(() => validateG09RuntimeDSN(scoped,'release_runtime_0123456789abcdef01234567'));
});
test('candidate migration inputs bind exact basenames and hashes without duplicate versions', () => {
  const rows=[{path:'db/migrations/002_x.sql',sha256:'2'.repeat(64)},{path:'db/migrations/001_x.sql',sha256:'1'.repeat(64)}];
  assert.deepEqual(g09MigrationInputs({migrations:rows}).map(x=>x.version),['001_x.sql','002_x.sql']);
  for (const migrations of [[],[...rows,rows[0]],[{path:'../001.sql',sha256:'1'.repeat(64)}],[{...rows[0],sha256:'bad'}]]) assert.throws(()=>g09MigrationInputs({migrations}));
});
test('control actions bind run and strict sequence and cannot carry executable arguments', () => {
  assert.equal(validateG09Command({runId,sequence:1,action:'ready'},runId,1).action,'ready');
  for (const command of [{runId,sequence:2,action:'ready'},{runId,sequence:1,action:'prepare'},{runId:'other',sequence:1,action:'abort'},{runId,sequence:1,action:'snapshot',sql:'SELECT 1'},{runId,sequence:1,action:'abort',pid:1}]) assert.throws(()=>validateG09Command(command,runId,1));
});
test('private publication never overwrites a prior command, including concurrent writers', async () => {
  const root=await mkdtemp(path.join(os.tmpdir(),'g09-control-'));
  try {
    const filename=path.join(root,'command-1.json');
    const writes=await Promise.allSettled([writeG09ControlJSON(filename,{marker:1}),writeG09ControlJSON(filename,{marker:2})]);
    assert.equal(writes.filter(x=>x.status==='fulfilled').length,1);
    const first=await readG09ControlJSON(filename);assert.ok([1,2].includes(first.marker));
    await assert.rejects(writeG09ControlJSON(filename,{marker:3}));
    assert.deepEqual(await readG09ControlJSON(filename),first);assert.equal((await lstat(filename)).mode&0o777,0o600);
    await symlink(filename,path.join(root,'link.json'));await assert.rejects(readG09ControlJSON(path.join(root,'link.json')));
    await chmod(filename,0o644);await assert.rejects(readG09ControlJSON(filename));
  } finally { await rm(root,{recursive:true,force:true}); }
});
test('complete inner runtime import closure is independently pinned and copied', async () => {
  const scripts=path.dirname(fileURLToPath(import.meta.url));const names=new Set(g09RuntimeFiles.map(x=>path.basename(x)));
  for (const file of g09RuntimeFiles) {
    const text=await readFile(path.join(scripts,path.basename(file)),'utf8');
    for (const [,relative] of text.matchAll(/(?:from\s*|import\s*\()\s*['"]\.\/([^'"]+\.mjs)['"]/g)) assert.ok(names.has(relative),`${file}: missing ${relative}`);
  }
});
test('CLI rejects duplicate options and does not accept implicit execution',()=>{
  const args=['--sealed','/tmp/sealed','--manifest-sha256','a'.repeat(64),'--source','/tmp/source','--runner-commit','b'.repeat(40),'--run-root','/tmp/new-run','--pg-socket','/tmp/.s.PGSQL.55439','--budget','/tmp/budget.json','--budget-sha256','c'.repeat(64)];
  assert.equal(parseG09RuntimeOptions(args).execute,undefined);assert.equal(parseG09RuntimeOptions([...args,'--execute']).execute,true);
  for (const bad of [args.slice(1),[...args,'--execute','ignored'],[...args,'--source','/other'],[...args,'--command','anything']]) assert.throws(()=>parseG09RuntimeOptions(bad));
});
