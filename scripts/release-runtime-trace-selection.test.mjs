import test from 'node:test';
import assert from 'node:assert/strict';
import { selectStandaloneTraces, traceSelectionFormat } from './release-runtime-trace-selection.mjs';
function metadata() {
  return { format: traceSelectionFormat, nextVersion: '16.3.8', manifests: {
    'server/pages-manifest.json': { '/ssr': 'pages/ssr.js', '/500': 'pages/500.html' },
    'server/app-paths-manifest.json': { '/dashboard/page': 'app/dashboard/page.js', '/api/route': 'app/api/route.js' },
    'server/middleware-manifest.json': { version: 3, middleware: {}, functions: {} },
    'server/functions-config-manifest.json': { version: 1, functions: { '/_middleware': { runtime: 'nodejs' } } },
  }, requiredFiles: ['.next/server/instrumentation.js'], available: ['next-server.js.nft.json', 'next-minimal-server.js.nft.json', 'server/app/dashboard/page.js.nft.json', 'server/app/api/route.js.nft.json', 'server/pages/ssr.js.nft.json', 'server/pages/500.js.nft.json', 'server/middleware.js.nft.json', 'server/instrumentation.js.nft.json'] };
}
test('same-build manifests select full server, Node app/pages/middleware/instrumentation and explicitly omit minimal/static', () => {
  const result = selectStandaloneTraces(metadata());
  assert.deepEqual(result.selected, ['next-server.js.nft.json', 'server/app/api/route.js.nft.json', 'server/app/dashboard/page.js.nft.json', 'server/instrumentation.js.nft.json', 'server/middleware.js.nft.json', 'server/pages/ssr.js.nft.json']);
  assert.deepEqual(result.omitted, [{ name: 'next-minimal-server.js.nft.json', reason: 'next16-copyTracedFiles-uses-next-server-not-minimal-server' }, { name: 'server/pages/500.js.nft.json', reason: 'next16-copyTracedFiles-skips-static-html-page' }]);
});
test('selection never derives from destination existence and missing selected trace fails', () => {
  const m = metadata(); m.available = m.available.filter(x => x !== 'server/app/dashboard/page.js.nft.json');
  assert.throws(() => selectStandaloneTraces(m), /Missing selected/);
});
test('unknown, duplicate, traversal, middleware ambiguity, unsupported framework/edge routes fail closed', () => {
  const changes = [m => m.available.push('server/unmapped.js.nft.json'), m => m.available.push(m.available[0]), m => m.manifests['server/app-paths-manifest.json']['/bad/page']='../outside.js', m => m.nextVersion='16.4.0', m => m.manifests['server/functions-config-manifest.json'].functions['/_middleware'].runtime='edge', m => m.manifests['server/middleware-manifest.json'].functions['/edge']={ files:['edge.js'] }];
  for (const change of changes) { const m=metadata();change(m);assert.throws(() => selectStandaloneTraces(m)); }
});
