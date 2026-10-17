#!/usr/bin/env node
import { readFile, writeFile, readdir, access, mkdir } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

export const domains = ['identity', 'catalog', 'transactions', 'services', 'tickets', 'suppliers', 'notifications', 'system'];
export const states = ['matched', 'covered', 'frontend_replaced', 'superseded', 'deprecated', 'missing_candidate'];
export const digest = value => createHash('sha256').update(value).digest('hex');
export const routeKey = route => [route.file, route.line, route.method].join(':');
export const routeFingerprint = route => digest(route.expression + '|' + route.controller);

// Namespaces are significant: /v1 and /api/v1 are NOT interchangeable.
export function normalizePath(value) {
  return '/' + value.trim().replace(/^\/+|\/+$/g, '').replace(/\[?:([A-Za-z0-9_]+)\]?/g, ':$1').replace(/\{([A-Za-z0-9_]+)\}/g, ':$1');
}

export function parseLegacyCalls(source, file) {
  const calls = [];
  let group = '';
  let blockComment = false;
  for (const [index, line] of source.split(/\r?\n/).entries()) {
    const commented = blockComment || /^\s*(?:\/\/|#|\/\*)/.test(line);
    if (line.includes('/*') && !line.includes('*/')) blockComment = true;
    if (line.includes('*/')) blockComment = false;
    const groupMatch = line.match(/Route::group\((\$domain|"[^"]*"),/);
    if (!commented && groupMatch) group = groupMatch[1] === '$domain' ? '{legacy_admin}' : JSON.parse(groupMatch[1]);
    if (!commented && /^\s*\}\)/.test(line)) group = '';
    const call = line.match(/Route::(get|post|put|patch|delete|rule|any)\((.*)/);
    if (!call) continue;
    const expression = call[2];
    const local = expression.match(/"([^"]*)"/)?.[1] ?? null;
    const prefix = /^\$domain\s*\./.test(expression) ? '{legacy_admin}' : group;
    let methods = [call[1].toUpperCase()];
    if (call[1] === 'rule') methods = (expression.match(/,\s*"([A-Z|]+)"\s*\)/)?.[1] ?? 'ANY').split('|');
    if (call[1] === 'any') methods = ['ANY'];
    calls.push({ file, line: index + 1, expression, controller: expression.match(/,\s*"([^"]+)"/)?.[1] ?? null,
      method: call[1].toUpperCase(), path: local,
      effective_path: local === null ? null : normalizePath([prefix, local.replace(/^\/+/, '')].filter(Boolean).join('/')),
      methods, group_prefix: prefix, commented });
  }
  return calls;
}

export function validateReview(review, key, fingerprint, goRoutes, pages) {
  if (!review || review.fingerprint !== fingerprint) throw new Error('missing/stale review: ' + key);
  if (!domains.includes(review.domain) || !states.includes(review.state)) throw new Error('invalid verdict: ' + key);
  if (!review.reason?.trim() || !review.contract_delta?.trim() || !review.evidence?.length) throw new Error('incomplete evidence: ' + key);
  if (['matched', 'covered'].includes(review.state) && !review.go_routes?.length) throw new Error('covered without API: ' + key);
  if (review.state === 'frontend_replaced' && !review.pages?.length) throw new Error('frontend replacement without page: ' + key);
  for (const route of review.go_routes ?? []) if (!goRoutes.has(route)) throw new Error('unknown Go route ' + route + ': ' + key);
  for (const page of review.pages ?? []) if (!pages.has(page)) throw new Error('unknown Next page ' + page + ': ' + key);
  if (review.state === 'missing_candidate' && !review.follow_up?.trim()) throw new Error('missing follow-up: ' + key);
  // Only wire-level conformance tests justify matched, not a matching URL.
  if (review.state === 'matched' && !review.wire_test) throw new Error('matched without wire test: ' + key);
  return review;
}

async function filesUnder(directory) {
  const result = [];
  for (const item of await readdir(directory, { withFileTypes: true })) {
    const file = path.join(directory, item.name);
    if (item.isDirectory()) result.push(...await filesUnder(file));
    else result.push(file);
  }
  return result;
}

async function controllerEvidence(root, controller) {
  const [namespace, name, ...actionParts] = controller.split('/');
  const action = actionParts.filter(Boolean).at(-1);
  const directory = path.join(root, 'app', namespace, 'controller');
  const normalized = value => value.replace(/_/g, '').toLowerCase();
  let file;
  try { file = (await readdir(directory)).find(file => normalized(file) === normalized(name + 'Controller.php')); } catch {}
  if (!file || !action) return { resolved: false, reason: 'controller/action absent in current reference tree' };
  const source = await readFile(path.join(directory, file), 'utf8');
  const methods = [...source.matchAll(/(?:public|protected)\s+function\s+([A-Za-z0-9_]+)\s*\(/g)];
  const method = methods.find(value => value[1].toLowerCase() === action.toLowerCase());
  const relative = 'app/' + namespace + '/controller/' + file;
  if (!method) return { resolved: false, file: relative, sha256: digest(source), reason: 'action absent locally; inheritance/dynamic resolution needs review' };
  const next = methods.find(value => value.index > method.index)?.index ?? source.length;
  const body = source.slice(method.index, next);
  return { resolved: true, file: relative, action: method[1], line: source.slice(0, method.index).split(/\r?\n/).length,
    sha256: digest(body), template_rendering: /(?:->(?:view|display|fetch)\(|view\()/.test(body) };
}

export async function generate({ cwd = process.cwd(), strict = false } = {}) {
  const inventoryPath = 'docs/generated/legacy-inventory.json';
  const reviewPath = 'docs/audit/route-reviews.json';
  const serverPath = 'internal/httpapi/server.go';
  const outputPath = 'output/audit/route-coverage.json';
  const inventory = JSON.parse(await readFile(path.join(cwd, inventoryPath), 'utf8'));
  const manifest = JSON.parse(await readFile(path.join(cwd, reviewPath), 'utf8'));
  const server = await readFile(path.join(cwd, serverPath), 'utf8');
  const goRoutes = [...server.matchAll(/mux\.HandleFunc\("([A-Z]+) ([^"\n]+)"/g)].map(match => ({ method: match[1], path: match[2] }));
  const goSet = new Set(goRoutes.map(route => route.method + ' ' + route.path));
  const pages = new Set((await filesUnder(path.join(cwd, 'web/src/app'))).filter(file => file.endsWith('/page.tsx')).map(file => '/' + path.relative(path.join(cwd, 'web/src/app'), path.dirname(file)).replaceAll(path.sep, '/')));
  const currentSources = new Map();
  const sources = [];
  for (const file of new Set(inventory.routes.map(route => route.file))) {
    const source = await readFile(path.join(inventory.source, file), 'utf8');
    currentSources.set(file, parseLegacyCalls(source, file));
    sources.push({ file, sha256: digest(source), calls: currentSources.get(file).length });
  }
  const expectedKeys = new Set(inventory.routes.map(routeKey));
  for (const key of Object.keys(manifest.reviews)) if (!expectedKeys.has(key)) throw new Error('orphan review: ' + key);
  const evidenceCache = new Map();
  const routes = [];
  for (const route of inventory.routes) {
    const key = routeKey(route);
    const matches = currentSources.get(route.file).filter(call => call.expression === route.expression && call.method === route.method);
    const call = matches.find(call => call.line === route.line) ?? matches[0];
    const review = manifest.reviews[key];
    if (review) {
      validateReview(review, key, routeFingerprint(route), goSet, pages);
      for (const evidence of review.evidence) await access(path.join(cwd, evidence.split('#')[0]));
      if (review.wire_test) await access(path.join(cwd, review.wire_test.split('#')[0]));
    }
    if (!evidenceCache.has(route.controller)) evidenceCache.set(route.controller, await controllerEvidence(inventory.source, route.controller));
    const candidates = call?.effective_path ? goRoutes.filter(go => (call.methods.includes(go.method) || call.methods.includes('ANY')) && normalizePath(go.path) === call.effective_path) : [];
    routes.push({ id: key, domain: review?.domain ?? null,
      legacy: { ...route, path: route.expression.match(/"([^"]*)"/)?.[1] ?? null, effective_path: call?.effective_path ?? null, current_line: call?.line ?? null,
        source_state: !call ? 'absent_or_changed' : call.line === route.line ? 'aligned' : 'line_drift', methods: call?.methods ?? [route.method], group_prefix: call?.group_prefix ?? null, commented: call?.commented ?? null },
      legacy_handler: evidenceCache.get(route.controller), review_state: review?.state ?? 'unreviewed', review: review ?? null,
      candidate_go_routes: candidates, wire_compatible: review?.state === 'matched' });
  }
  const counts = Object.fromEntries([...states, 'unreviewed'].map(state => [state, routes.filter(route => route.review_state === state).length]));
  const modules = domains.map(domain => {
    const subset = routes.filter(route => route.domain === domain);
    return { domain, total: subset.length, states: Object.fromEntries(states.map(state => [state, subset.filter(route => route.review_state === state).length])) };
  });
  const currentOnly = [...currentSources.values()].flat().filter(call => !inventory.routes.some(route => route.file === call.file && route.expression === call.expression && route.method === call.method));
  const result = { generated_at: new Date().toISOString(),
    source: { legacy_inventory: inventoryPath, inventory_sha256: digest(JSON.stringify(inventory)), legacy_generated_at: inventory.generatedAt, legacy_root: inventory.source,
      current_legacy_routes: sources, review_manifest: reviewPath, review_manifest_sha256: digest(JSON.stringify(manifest)), go_router: serverPath, go_router_sha256: digest(server) },
    disclaimer: 'Source-call counts include duplicates and inactive declarations. covered/frontend_replaced map capabilities/pages, not old URL/payload compatibility. Missing candidates remain missing; source drift is explicit. Only matched with wire tests asserts protocol compatibility.',
    summary: { legacy_route_calls: routes.length, current_go_route_registrations: goRoutes.length, current_go_unique_routes: goSet.size,
      exact_method_path_candidates: routes.filter(route => route.candidate_go_routes.length).length, unreviewed_routes: counts.unreviewed,
      reviewed_routes: routes.length - counts.unreviewed, verdict_counts: counts, source_drift: routes.filter(route => route.legacy.source_state !== 'aligned').length, modules },
    current_only_legacy_calls: currentOnly, routes };
  // Strict mode refuses to replace a previously reviewed artifact with an incomplete audit.
  if (strict && counts.unreviewed) throw new Error(counts.unreviewed + ' routes still require review');
  await mkdir(path.dirname(path.join(cwd, outputPath)), { recursive: true });
  await writeFile(path.join(cwd, outputPath), JSON.stringify(result, null, 2) + '\n');
  return result.summary;
}

if (process.argv[1] && fileURLToPath(import.meta.url) === path.resolve(process.argv[1])) {
  console.log(JSON.stringify(await generate({ strict: process.argv.includes('--strict') })));
}
