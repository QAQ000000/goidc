import path from 'node:path';
import { safeRelative } from './release-candidate-lib.mjs';

export const traceSelectionFormat = 'zjmfgo-next-standalone-trace-selection-v1';
export const traceSelectionManifestPaths = ['server/pages-manifest.json', 'server/app-paths-manifest.json', 'server/middleware-manifest.json', 'server/functions-config-manifest.json'];
function check(ok, message) { if (!ok) throw new Error(message); }
function object(value) { return value && typeof value === 'object' && !Array.isArray(value); }
function safe(value) { safeRelative(value); check(!value.includes(':'), 'Unsafe trace selection path'); return value; }
// Pure metadata rule for the reviewed Next16.3.8 webpack copyTracedFiles. It
// never decides selection by probing whether a destination happened to exist.
export function selectStandaloneTraces(metadata) {
  check(metadata?.format === traceSelectionFormat && metadata.nextVersion === '16.3.8', 'Unsupported Next standalone trace selection contract');
  check(object(metadata.manifests) && Array.isArray(metadata.requiredFiles) && Array.isArray(metadata.available), 'Trace selection metadata missing');
  check(Object.keys(metadata.manifests).sort().join('\n') === [...traceSelectionManifestPaths].sort().join('\n'), 'Unexpected trace selection manifests');
  const [pages, app, middleware, functions] = traceSelectionManifestPaths.map(name => metadata.manifests[name]);
  check(object(pages) && object(app) && middleware?.version === 3 && object(middleware.middleware) && object(middleware.functions) && functions?.version === 1 && object(functions.functions), 'Invalid Next standalone selection manifests');
  // Edge asset copying is a different non-nft path. Fail closed until that
  // contract is represented instead of claiming its assets are trace-selected.
  check(!Object.keys(middleware.middleware).length && !Object.keys(middleware.functions).length, 'Edge standalone trace selection is unsupported');
  const selected = new Set(['next-server.js.nft.json']);
  const excluded = new Map([['next-minimal-server.js.nft.json', 'next16-copyTracedFiles-uses-next-server-not-minimal-server']]);
  for (const [route, entry] of Object.entries(pages)) {
    check(typeof route === 'string' && route.startsWith('/') && typeof entry === 'string', 'Invalid pages manifest route');
    safe(entry); check(entry.startsWith('pages/'), 'Pages trace selection path mismatch');
    if (entry.endsWith('.js')) selected.add(`server/${entry}.nft.json`);
    else {
      check(entry.endsWith('.html'), 'Unknown pages manifest entry type');
      excluded.set(`server/${entry.slice(0, -5)}.js.nft.json`, 'next16-copyTracedFiles-skips-static-html-page');
    }
  }
  for (const [route, entry] of Object.entries(app)) {
    check(typeof route === 'string' && route.startsWith('/') && typeof entry === 'string', 'Invalid app manifest route');
    safe(entry); check(entry.startsWith('app/') && entry.endsWith('.js'), 'App trace selection path mismatch');
    selected.add(`server/${entry}.nft.json`);
  }
  const nodeMiddleware = functions.functions['/_middleware'];
  if (nodeMiddleware !== undefined) {
    check(nodeMiddleware?.runtime === 'nodejs', 'Unknown middleware runtime selection');
    selected.add('server/middleware.js.nft.json');
  }
  for (const entry of metadata.requiredFiles) safe(entry);
  if (metadata.requiredFiles.includes('.next/server/instrumentation.js')) selected.add('server/instrumentation.js.nft.json');
  const available = new Set();
  for (const name of metadata.available) { safe(name); check(name.endsWith('.nft.json') && !available.has(name), 'Duplicate/invalid available trace'); available.add(name); }
  for (const name of selected) check(available.has(name), `Missing selected Next trace: ${name}`);
  check(available.has('next-minimal-server.js.nft.json'), 'Missing reviewed Next minimal trace omission');
  const omitted = [];
  for (const name of available) if (!selected.has(name)) {
    check(excluded.has(name), `Unknown or unselected Next trace: ${name}`);
    omitted.push({ name, reason: excluded.get(name) });
  }
  return { selected: [...selected].sort(), omitted: omitted.sort((a,b) => a.name.localeCompare(b.name)) };
}
