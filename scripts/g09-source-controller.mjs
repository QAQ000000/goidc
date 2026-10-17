// Source-only HTTP smoke controller. No worker, merchant or capacity proof.
import fs from 'node:fs';
import path from 'node:path';
import { createG09APIClient, createG09ScenarioExecutor } from './g09-scenario-lib.mjs';
import { createG09EpaySettlementExecutor } from './g09-epay-scenario.mjs';

const profiles = {
  source_api: {
    names: ['result.json', 'requests.jsonl', 'mutations.private.jsonl'],
    counts: { http_started: 17, http_finished: 17 }, maxRequests: 64, maxFlows: 4,
    valid: flows => Array.isArray(flows) && flows.map(f => f.kind).join(',') === 'balance_pay,local_renew,purchase_local,ticket',
    create: createG09ScenarioExecutor, decorate: (flow, result) => ({ kind: flow.kind, ...result }),
    result: count => ({ schemaVersion: 1, workerVerified: false, httpRequests: count.http_started }),
    failure: { schemaVersion: 1, code: 'source_api_smoke_failed' },
    successMessage: 'G09 source API HTTP scenario completed; worker/candidate/capacity unverified',
    failureMessage: 'G09 source API smoke failed; inspect private run files',
  },
  source_epay: {
    names: ['epay-result.json', 'epay-requests.jsonl', 'epay-mutations.private.jsonl'],
    counts: { http_started: 8, http_finished: 8, epay_callback_started: 4, epay_callback_finished: 4 }, maxRequests: 16, maxFlows: 2,
    valid: flows => Array.isArray(flows) && flows.length === 2 && flows[0].provider === 'epay_alipay' && flows[1].provider === 'epay_wxpay',
    create: createG09EpaySettlementExecutor, decorate: (_flow, result) => result,
    result: count => ({ capacityVerified: false, actualHTTPRequests: count.http_started + count.epay_callback_started, intentMutations: 2, frozenCallbackForms: 2, callbackAttempts: count.epay_callback_started }),
    failure: { code: 'source_epay_smoke_failed' },
    successMessage: 'G09 signed Epay source API chains completed; real merchant/candidate/capacity unverified',
    failureMessage: 'G09 signed Epay source API smoke failed; inspect private evidence',
  },
};

export function readPrivateManifest(filename) {
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
    if (!stat.isFile() || stat.nlink !== 1 || (stat.mode & 0o777) !== 0o600 || stat.uid !== process.getuid() || stat.size > 65536) throw new Error('manifest_file');
    return { manifest: JSON.parse(fs.readFileSync(fd, 'utf8')), directory };
  } finally { fs.closeSync(fd); }
}

const append = (fd, row) => { fs.writeFileSync(fd, `${JSON.stringify(row)}\n`); fs.fsyncSync(fd); };
export async function runSourceSmoke(mode) {
  const profile = profiles[mode];
  if (!profile) throw new Error('source_mode');
  let execute;
  const descriptors = [];
  try {
    if (process.argv.length !== 3) throw new Error('manifest_argument');
    const { manifest, directory } = readPrivateManifest(process.argv[2]);
    if (manifest.schemaVersion !== 1 || manifest.mode !== mode || !profile.valid(manifest.flows)) throw new Error('manifest_shape');
    for (const name of profile.names) descriptors.push(fs.openSync(path.join(directory, name), fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | fs.constants.O_NOFOLLOW, 0o600));
    const directoryFD = fs.openSync(directory, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY);
    try { fs.fsyncSync(directoryFD); } finally { fs.closeSync(directoryFD); }
    const count = Object.fromEntries(Object.keys(profile.counts).map(event => [event, 0]));
    const record = row => { if (Object.hasOwn(count, row.event)) count[row.event]++; append(descriptors[1], row); };
    const persistPrivate = row => append(descriptors[2], row);
    const request = createG09APIClient({
      apiOrigin: manifest.apiOrigin, publicOrigin: manifest.publicOrigin, runId: manifest.runId,
      maxRequests: profile.maxRequests, record, persistPrivate,
    });
    execute = profile.create({ ...manifest, request, maxFlows: profile.maxFlows, record, persistPrivate });
    const results = [], signal = AbortSignal.timeout(30000);
    for (const flow of manifest.flows) results.push(profile.decorate(flow, await execute({ flow, signal })));
    if (Object.entries(profile.counts).some(([event, expected]) => count[event] !== expected) || results.some(r => !r.succeeded || r.deliveryVerified || r.accountingVerified || r.capacityVerified)) throw new Error('incomplete_http_chain');
    append(descriptors[0], { mode, ok: true, candidateVerified: false, ...profile.result(count), results, snapshot: execute.snapshot() });
    process.stdout.write(`${profile.successMessage}\n`);
  } catch {
    // Never emit exception text, credentials, request bodies or DSNs.
    if (descriptors[0] !== undefined) try { append(descriptors[0], { mode, ok: false, ...profile.failure }); } catch {}
    process.stderr.write(`${profile.failureMessage}\n`); process.exitCode = 1;
  } finally {
    execute?.stop();
    for (const fd of descriptors) fs.closeSync(fd);
  }
}
