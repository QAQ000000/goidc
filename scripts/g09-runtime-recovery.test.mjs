import test from 'node:test';
import assert from 'node:assert/strict';
import { chmod, mkdtemp, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { recoverG09ControlHistory } from './g09-runtime-recovery.mjs';
import { writeG09ControlJSON } from './g09-runtime-contract.mjs';
const runId = 'g09_candidate_0123456789abcdef01234567';
async function fixture(fn) { const dir = await mkdtemp(path.join(os.tmpdir(), 'g09-recovery-')); try { await fn(dir); } finally { await rm(dir, { recursive: true, force: true }); } }
async function command(dir, sequence, action = 'ready', extra = {}) { await writeG09ControlJSON(path.join(dir, `command-${sequence}.json`), { runId, sequence, action, ...extra }); }
async function result(dir, sequence, action = 'ready', extra = {}) { await writeG09ControlJSON(path.join(dir, `result-${sequence}.json`), { runId, sequence, action, ok: true, ...extra }); }
const isUnknown = value => { assert.equal(value.status, 'unknown'); assert.equal(value.frozen, true); assert.equal(value.state, undefined); };

test('only empty or wholly acknowledged success history returns next abort state', async () => fixture(async dir => {
 assert.deepEqual((await recoverG09ControlHistory(dir, runId)).state, { runId, sequence: 0 });
 for (const [sequence, action] of [[1,'ready'],[2,'verify-migrations'],[3,'verify-migrations'],[4,'snapshot']]) { await command(dir, sequence, action); await result(dir, sequence, action); }
 assert.deepEqual(await recoverG09ControlHistory(dir, runId), { status:'ready', frozen:false, state:{runId,sequence:4} });
}));
test('missing ack, orphan ack and non-contiguous histories freeze', async () => {
 for (const scenario of ['missing','orphan','gap','result-gap']) await fixture(async dir => {
  if (scenario === 'missing') await command(dir,1);
  if (scenario === 'orphan') await result(dir,1);
  if (scenario === 'gap') { await command(dir,2); await result(dir,2); }
  if (scenario === 'result-gap') { await command(dir,1); await result(dir,2); }
  isUnknown(await recoverG09ControlHistory(dir,runId));
 });
});
test('wrong identity, sequence, action, nonboolean ok and unsupported request freeze', async () => {
 for (const extra of [{runId:'other'},{sequence:2},{action:'abort'},{ok:'true'},{ok:null}]) await fixture(async dir => { await command(dir,1);await result(dir,1,'ready',extra);isUnknown(await recoverG09ControlHistory(dir,runId)); });
 await fixture(async dir=>{await command(dir,1,'prepare');await result(dir,1,'prepare');isUnknown(await recoverG09ControlHistory(dir,runId));});
});
test('failed ack and successful terminal ack never expose a next state', async () => {
 for (const action of ['ready','finish','abort']) await fixture(async dir => { await command(dir,1,action);await result(dir,1,action,{ok:false,errorCode:'redacted'});assert.deepEqual(await recoverG09ControlHistory(dir,runId),{status:'failed',frozen:true,sequence:1,action}); });
 for (const action of ['finish','abort']) await fixture(async dir=>{await command(dir,1,action);await result(dir,1,action);assert.deepEqual(await recoverG09ControlHistory(dir,runId),{status:'terminal',frozen:true,sequence:1,action});});
});
test('history after a failure or terminal is inconsistent and freezes', async () => {
 for (const [action,ok] of [['ready',false],['abort',true],['finish',true]]) await fixture(async dir=>{await command(dir,1,action);await result(dir,1,action,{ok});await command(dir,2,'snapshot');await result(dir,2,'snapshot');isUnknown(await recoverG09ControlHistory(dir,runId));});
});
test('malformed, ambiguous and excessive filename sequences freeze', async () => {
 for (const name of ['command-0.json','command-01.json','command-33.json','result-9007199254740993.json','command-1.json.tmp','result-x.json']) await fixture(async dir=>{await writeFile(path.join(dir,name),'{}',{mode:0o600});isUnknown(await recoverG09ControlHistory(dir,runId));});
});
test('unsafe permissions, symlinks, wrong owner authorization and malformed JSON freeze', async () => {
 for (const scenario of ['mode','link','json','uid','directory']) await fixture(async dir=>{
  await command(dir,1);await result(dir,1);
  if(scenario==='mode')await chmod(path.join(dir,'result-1.json'),0o644);
  if(scenario==='directory')await chmod(dir,0o755);
  if(scenario==='link'){await rm(path.join(dir,'result-1.json'));await symlink('command-1.json',path.join(dir,'result-1.json'));}
  if(scenario==='json')await writeFile(path.join(dir,'result-1.json'),'{');
  isUnknown(await recoverG09ControlHistory(dir,runId,scenario==='uid'?{uid:process.getuid()+1}:{}));
 });
});
