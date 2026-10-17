// Invoked only by TestG09SourceAPIScenarioSmoke with its private manifest.
// This exercises source HTTP handlers. It does not run workers or measure capacity.
import fs from 'node:fs';
import path from 'node:path';
import { createG09APIClient, createG09ScenarioExecutor } from './g09-scenario-lib.mjs';

function privateManifest(filename) {
  if (!path.isAbsolute(filename) || path.normalize(filename) !== filename) throw new Error('manifest_path');
  const directory = path.dirname(filename);
  for (let current = directory; ; current = path.dirname(current)) {
    const stat = fs.lstatSync(current);
    if (!stat.isDirectory() || stat.isSymbolicLink()) throw new Error('manifest_directory');
    if (path.dirname(current) === current) break;
  }
  const parent = fs.statSync(directory);
  if ((parent.mode & 0o777) !== 0o700 || parent.uid !== process.getuid()) throw new Error('manifest_directory');
  const fd = fs.openSync(filename, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW);
  try {
    const stat = fs.fstatSync(fd);
    if (!stat.isFile() || (stat.mode & 0o777) !== 0o600 || stat.uid !== process.getuid() || stat.size > 65536) throw new Error('manifest_file');
    const manifest = JSON.parse(fs.readFileSync(fd, 'utf8'));
    if (manifest.schemaVersion !== 1 || manifest.mode !== 'source_api' || !Array.isArray(manifest.flows) || manifest.flows.length !== 4 || manifest.flows.map(f => f.kind).join(',') !== 'balance_pay,local_renew,purchase_local,ticket') throw new Error('manifest_shape');
    return { manifest, directory };
  } finally { fs.closeSync(fd); }
}

function exclusive(filename) {
  return fs.openSync(filename, fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | fs.constants.O_NOFOLLOW, 0o600);
}
function append(fd, row) {
  fs.writeFileSync(fd, `${JSON.stringify(row)}\n`);
  fs.fsyncSync(fd);
}

let execute, requestFD, mutationFD, resultFD;
try {
  if (process.argv.length !== 3) throw new Error('manifest_argument');
  const { manifest, directory } = privateManifest(process.argv[2]);
  resultFD = exclusive(path.join(directory, 'result.json'));
  requestFD = exclusive(path.join(directory, 'requests.jsonl'));
  mutationFD = exclusive(path.join(directory, 'mutations.private.jsonl'));
  const directoryFD = fs.openSync(directory, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY);
  try { fs.fsyncSync(directoryFD); } finally { fs.closeSync(directoryFD); }
  let started = 0, finished = 0;
  const request = createG09APIClient({
    apiOrigin: manifest.apiOrigin, publicOrigin: manifest.publicOrigin, runId: manifest.runId,
    maxRequests: 64,
    persistPrivate: row => append(mutationFD, row),
    record: row => {
      if (row.event === 'http_started') started++;
      if (row.event === 'http_finished') finished++;
      append(requestFD, row);
    },
  });
  execute = createG09ScenarioExecutor({ request, runId: manifest.runId, accounts: manifest.accounts, maxFlows: 4 });
  const results = [];
  const signal = AbortSignal.timeout(30000);
  for (const flow of manifest.flows) results.push({ kind: flow.kind, ...await execute({ flow, signal }) });
  if (started !== 17 || finished !== started || results.some(r => !r.succeeded || r.deliveryVerified || r.accountingVerified)) throw new Error('incomplete_http_chain');
  append(resultFD, { schemaVersion: 1, mode: 'source_api', ok: true, candidateVerified: false, workerVerified: false, httpRequests: started, results, snapshot: execute.snapshot() });
  process.stdout.write('G09 source API HTTP scenario completed; worker/candidate/capacity unverified\n');
} catch {
  // Never print exception text, response bodies, cookies, mutation keys or DSNs.
  if (resultFD !== undefined) {
    try { append(resultFD, { schemaVersion: 1, mode: 'source_api', ok: false, code: 'source_api_smoke_failed' }); } catch {}
  }
  process.stderr.write('G09 source API smoke failed; inspect private run files\n');
  process.exitCode = 1;
} finally {
  execute?.stop();
  for (const fd of [requestFD, mutationFD, resultFD]) if (fd !== undefined) fs.closeSync(fd);
}
