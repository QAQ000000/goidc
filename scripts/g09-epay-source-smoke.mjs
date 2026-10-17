// Invoked only by the explicit Go source-API smoke with its private manifest.
import fs from 'node:fs';
import path from 'node:path';
import { createG09APIClient } from './g09-scenario-lib.mjs';
import { createG09EpaySettlementExecutor } from './g09-epay-scenario.mjs';
let execute, publicFD, privateFD, resultFD;
const append = (fd, row) => { fs.writeFileSync(fd, JSON.stringify(row) + '\n'); fs.fsyncSync(fd); };
try {
  if (process.argv.length !== 3) throw new Error('manifest_argument');
  const filename = process.argv[2], directory = path.dirname(filename);
  if (!path.isAbsolute(filename) || path.normalize(filename) !== filename || fs.realpathSync(directory) !== directory) throw new Error('manifest_path');
  const dir = fs.lstatSync(directory);
  if (!dir.isDirectory() || (dir.mode & 0o777) !== 0o700 || dir.uid !== process.getuid()) throw new Error('manifest_directory');
  const input = fs.openSync(filename, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW);
  let manifest;
  try {
    const st = fs.fstatSync(input);
    if (!st.isFile() || st.nlink !== 1 || (st.mode & 0o777) !== 0o600 || st.uid !== process.getuid() || st.size > 65536) throw new Error('manifest_file');
    manifest = JSON.parse(fs.readFileSync(input, 'utf8'));
  } finally { fs.closeSync(input); }
  if (manifest.schemaVersion !== 1 || manifest.mode !== 'source_epay' || manifest.flows?.length !== 2 || manifest.flows[0].provider !== 'epay_alipay' || manifest.flows[1].provider !== 'epay_wxpay') throw new Error('manifest_shape');
  const exclusive = name => fs.openSync(path.join(directory, name), fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | fs.constants.O_NOFOLLOW, 0o600);
  resultFD = exclusive('epay-result.json'); publicFD = exclusive('epay-requests.jsonl'); privateFD = exclusive('epay-mutations.private.jsonl');
  const parent = fs.openSync(directory, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY);
  try { fs.fsyncSync(parent); } finally { fs.closeSync(parent); }
  let httpStarted = 0, httpFinished = 0, callbackStarted = 0, callbackFinished = 0;
  const record = row => {
    if (row.event === 'http_started') httpStarted++;
    if (row.event === 'http_finished') httpFinished++;
    if (row.event === 'epay_callback_started') callbackStarted++;
    if (row.event === 'epay_callback_finished') callbackFinished++;
    append(publicFD, row);
  };
  const persistPrivate = row => append(privateFD, row);
  const request = createG09APIClient({ ...manifest, maxRequests: 16, record, persistPrivate });
  execute = createG09EpaySettlementExecutor({ ...manifest, request, maxFlows: 2, persistPrivate, record });
  const results = [];
  const signal = AbortSignal.timeout(30000);
  for (const flow of manifest.flows) results.push(await execute({ flow, signal }));
  if (httpStarted !== 8 || httpFinished !== 8 || callbackStarted !== 4 || callbackFinished !== 4 || results.some(r => !r.succeeded || r.accountingVerified || r.capacityVerified)) throw new Error('count_mismatch');
  append(resultFD, { mode: 'source_epay', ok: true, candidateVerified: false, capacityVerified: false, actualHTTPRequests: httpStarted + callbackStarted, intentMutations: 2, frozenCallbackForms: 2, callbackAttempts: callbackStarted, results, snapshot: execute.snapshot() });
  process.stdout.write('G09 signed Epay source API chains completed; real merchant/candidate/capacity unverified\n');
} catch {
  if (resultFD !== undefined) try { append(resultFD, { mode: 'source_epay', ok: false, code: 'source_epay_smoke_failed' }); } catch {}
  process.stderr.write('G09 signed Epay source API smoke failed; inspect private evidence\n'); process.exitCode = 1;
} finally {
  execute?.stop();
  for (const fd of [publicFD, privateFD, resultFD]) if (fd !== undefined) fs.closeSync(fd);
}
