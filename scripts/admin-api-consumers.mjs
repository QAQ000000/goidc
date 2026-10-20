import { readFile, readdir, stat, mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { createRequire } from 'node:module';
import { createHash } from 'node:crypto';
import { fileURLToPath } from 'node:url';

const helpers = new Set(['api', 'useResource', 'upload', 'download']);
const methods = new Set(['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS']);
const slash = value => value.split(path.sep).join('/');
const unique = values => [...new Set(values)].sort();
const escape = value => value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
const digest = value => createHash('sha256').update(value).digest('hex');

async function files(directory) {
  const result = [];
  for (const entry of (await readdir(directory, { withFileTypes: true })).sort((a, b) => a.name.localeCompare(b.name, 'en'))) {
    if (entry.isSymbolicLink()) continue;
    const target = path.join(directory, entry.name);
    if (entry.isDirectory()) result.push(...await files(target));
    else result.push(target);
  }
  return result;
}

/** Match concrete Next paths and their declarative dynamic/catch-all patterns. */
export function matchPagePattern(pattern, actual) {
  const clean = value => value.split('?')[0].replace(/\/$/, '') || '/';
  const parts = clean(pattern).split('/').filter(Boolean);
  let source = '^';
  for (const part of parts) {
    if (/^\[\[\.\.\.[^\]]+\]\]$/.test(part)) source += '(?:/.*)?';
    else if (/^\[\.\.\.[^\]]+\]$/.test(part)) source += '/.+';
    else if (/^\[[^\]]+\]$/.test(part)) source += '/[^/]+';
    else if (part === '**') source += '(?:/.*)?';
    else source += `/${escape(part)}`;
  }
  return new RegExp(source + '/?$').test(clean(actual));
}

function cleanApiPath(value) {
  let depth = 0;
  let end = value.length;
  for (let index = 0; index < value.length; index++) {
    if (value.slice(index, index + 2) === '${') { depth++; index++; }
    else if (depth && value[index] === '}') depth--;
    else if (!depth && (value[index] === '?' || value[index] === '#')) { end = index; break; }
  }
  return value.slice(0, end).replace(/^\/api\/v1(?=\/|$)/, '') || '/';
}

/** These are possible route matches, never proof that a dynamic expression is resolved. */
export function matchApiCandidates(requestPath, routes) {
  const request = cleanApiPath(requestPath);
  const template = /\$\{[^}]+\}/.test(request);
  const pattern = request.split(/(\$\{[^}]+\})/).map(part => part.startsWith('${') ? '.*' : escape(part)).join('');
  return routes.filter(route => {
    const serverPath = cleanApiPath(route.path).replace(/\/\{\$\}$/, '/');
    const example = serverPath.replace(/\{[^}]+\}/g, 'parameter');
    if (template) return new RegExp(`^${pattern}/?$`).test(example);
    const serverPattern = serverPath.split(/(\{[^}]+\})/).map(part => part.startsWith('{') ? (part.includes('...') ? '.+' : '[^/]+') : escape(part)).join('');
    return new RegExp(`^${serverPattern}/?$`).test(request);
  });
}

export function validateRequirements(data, { root, pages = [] } = {}) {
  const errors = [];
  if (data?.schemaVersion !== 1) errors.push('schemaVersion must be 1');
  if (!Array.isArray(data?.domains)) errors.push('domains must be an array');
  if (!Array.isArray(data?.requirements)) errors.push('requirements must be an array');
  const ids = new Set();
  const domains = new Set((data?.domains ?? []).map(domain => domain.id));
  for (const item of data?.requirements ?? []) {
    const prefix = item.id ?? '<missing id>';
    if (!item.id || ids.has(item.id)) errors.push(`${prefix}: missing or duplicate id`);
    ids.add(item.id);
    if (!domains.has(item.domain)) errors.push(`${prefix}: unknown domain`);
    if (typeof item.required !== 'boolean') errors.push(`${prefix}: required must be boolean`);
    if (!['reuse', 'optimize', 'add', 'excluded', 'deferred'].includes(item.disposition)) errors.push(`${prefix}: invalid disposition`);
    if (['excluded', 'deferred'].includes(item.disposition) && item.required !== false) errors.push(`${prefix}: excluded/deferred cannot be required`);
    if (!item.reason?.trim()) errors.push(`${prefix}: reason is required`);
    if (!Array.isArray(item.pages) || !item.pages.length) errors.push(`${prefix}: page scope is required`);
    for (const page of item.pages ?? []) {
      if (page !== '/admin' && !page.startsWith('/admin/')) errors.push(`${prefix}: page must be an admin route`);
      if (pages.length && !pages.some(actual => matchPagePattern(page, actual.path ?? actual))) errors.push(`${prefix}: page scope matches no current page: ${page}`);
    }
    if (!Array.isArray(item.current?.endpoints)) errors.push(`${prefix}: current.endpoints is required`);
    if (!Array.isArray(item.legacy?.evidence)) errors.push(`${prefix}: legacy.evidence is required`);
    if (!['implemented', 'partial', 'missing', 'unverified', 'excluded', 'deferred'].includes(item.api?.status)) errors.push(`${prefix}: invalid API axis`);
    if (!['wired', 'partial', 'unwired', 'unverified', 'excluded', 'deferred'].includes(item.ui?.status)) errors.push(`${prefix}: invalid UI axis`);
    for (const axis of ['static', 'unit', 'database', 'browser', 'external']) {
      if (!['source-reviewed', 'not-run', 'not-applicable'].includes(item.verification?.[axis])) errors.push(`${prefix}: invalid verification axis: ${axis}`);
    }
    if (!Array.isArray(item.evidence) || !item.evidence.length) errors.push(`${prefix}: evidence is required`);
    if (root) for (const evidence of item.evidence ?? []) {
      const target = evidence.replace(/:\d+(?::\d+)?$/, '');
      if (path.isAbsolute(target) || target.split('/').includes('..') || target.startsWith('output/')) errors.push(`${prefix}: evidence must be stable repository-relative source`);
    }
  }
  return errors;
}

export async function loadRequirements(root) {
  return JSON.parse(await readFile(path.join(root, 'audit/admin-api-requirements.json'), 'utf8'));
}

/** No application code is executed. Import traversal intentionally over-approximates reachability. */
export async function collectAdminConsumers({ root = process.cwd(), routes = [], requirements = null } = {}) {
  root = path.resolve(root);
  const require = createRequire(path.join(root, 'web/package.json'));
  const ts = require('typescript');
  const appRoot = path.join(root, 'web/src/app');
  const sourceRoot = path.join(root, 'web/src');
  const pages = (await files(path.join(appRoot, 'admin'))).filter(file => /\/page\.(?:tsx|ts|jsx|js)$/.test(file)).map(file => ({
    path: '/' + slash(path.relative(appRoot, path.dirname(file))).split('/').filter(part => !/^\(.*\)$/.test(part) && !part.startsWith('@')).join('/'),
    source: slash(path.relative(root, file)),
    absolute: file,
  }));
  const cache = new Map();
  const unresolvedDependencies = [];
  async function resolveImport(specifier, importer) {
    let target;
    if (specifier.startsWith('@/')) target = path.join(sourceRoot, specifier.slice(2));
    else if (specifier.startsWith('.')) target = path.resolve(path.dirname(importer), specifier);
    else return null;
    if (!target.startsWith(sourceRoot + path.sep) && target !== sourceRoot) return null;
    const candidates = [target, ...['.ts', '.tsx', '.js', '.jsx', '.mjs'].map(extension => target + extension), ...['.ts', '.tsx', '.js', '.jsx'].map(extension => path.join(target, 'index' + extension))];
    for (const candidate of candidates) {
      try { if ((await stat(candidate)).isFile()) return candidate; } catch (error) { if (error.code !== 'ENOENT' && error.code !== 'ENOTDIR') throw error; }
    }
    unresolvedDependencies.push({ source: slash(path.relative(root, importer)), specifier, reason: 'local source import not resolved' });
    return null;
  }
  async function parse(file) {
    if (cache.has(file)) return cache.get(file);
    const text = await readFile(file, 'utf8');
    const ast = ts.createSourceFile(file, text, ts.ScriptTarget.Latest, true);
    if (ast.parseDiagnostics.length) throw new Error(`Cannot parse ${slash(path.relative(root, file))}: ${ts.flattenDiagnosticMessageText(ast.parseDiagnostics[0].messageText, ' ')}`);
    const record = { file, text, ast, imports: new Map(), exports: new Map(), stars: [], dependencies: [], requests: [] };
    cache.set(file, record);
    function dependency(module, kind, line) { record.dependencies.push({ specifier: module, kind, line, target: null }); }
    function visit(node) {
      const line = ast.getLineAndCharacterOfPosition(node.getStart(ast)).line + 1;
      if (ts.isImportDeclaration(node) && ts.isStringLiteral(node.moduleSpecifier)) {
        const clause = node.importClause;
        const named = clause?.namedBindings;
        const runtimeImport = !clause || (!clause.isTypeOnly && (clause.name || !named || ts.isNamespaceImport(named) || named.elements.some(element => !element.isTypeOnly)));
        if (runtimeImport) dependency(node.moduleSpecifier.text, 'import', line);
        if (clause?.name) record.imports.set(clause.name.text, { module: node.moduleSpecifier.text, name: 'default' });
        if (clause?.namedBindings && ts.isNamedImports(clause.namedBindings)) for (const element of clause.namedBindings.elements) {
          if (!element.isTypeOnly && !clause.isTypeOnly) record.imports.set(element.name.text, { module: node.moduleSpecifier.text, name: (element.propertyName ?? element.name).text });
        }
        if (clause?.namedBindings && ts.isNamespaceImport(clause.namedBindings)) record.imports.set(clause.namedBindings.name.text, { module: node.moduleSpecifier.text, name: '*' });
      }
      if (ts.isExportDeclaration(node)) {
        const module = node.moduleSpecifier && ts.isStringLiteral(node.moduleSpecifier) ? node.moduleSpecifier.text : null;
        const named = node.exportClause && ts.isNamedExports(node.exportClause) ? node.exportClause : null;
        const runtimeExport = !node.isTypeOnly && (!named || named.elements.some(element => !element.isTypeOnly));
        if (module && runtimeExport) dependency(module, 'reexport', line);
        if (named && !node.isTypeOnly) for (const element of named.elements) { if (!element.isTypeOnly) record.exports.set(element.name.text, { module, name: (element.propertyName ?? element.name).text }); }
        else if (module && runtimeExport) record.stars.push(module);
      }
      if (ts.isCallExpression(node) && (node.expression.kind === ts.SyntaxKind.ImportKeyword || (ts.isIdentifier(node.expression) && node.expression.text === 'require'))) {
        if (node.arguments[0] && ts.isStringLiteral(node.arguments[0])) dependency(node.arguments[0].text, node.expression.kind === ts.SyntaxKind.ImportKeyword ? 'dynamic-import' : 'require', line);
        else unresolvedDependencies.push({ source: slash(path.relative(root, file)), line, expression: node.getText(ast), reason: 'dynamic dependency expression' });
      }
      ts.forEachChild(node, visit);
    }
    visit(ast);
    for (const item of record.dependencies) item.target = await resolveImport(item.specifier, file);
    return record;
  }
  async function helperExport(file, name, seen = new Set()) {
    const key = `${file}:${name}`;
    if (seen.has(key)) return null;
    seen.add(key);
    const relative = slash(path.relative(sourceRoot, file));
    if ((relative === 'lib/api.ts' && helpers.has(name) && name !== 'useResource') || (relative === 'hooks/use-resource.ts' && name === 'useResource')) return name;
    const record = await parse(file);
    const binding = record.exports.get(name) ?? record.imports.get(name);
    if (binding?.module) {
      const target = await resolveImport(binding.module, file);
      return target ? helperExport(target, binding.name, seen) : null;
    }
    if (binding) return helperExport(file, binding.name, seen);
    for (const module of record.stars) {
      const target = await resolveImport(module, file);
      const found = target && await helperExport(target, name, seen);
      if (found) return found;
    }
    return null;
  }
  async function callHelper(record, expression) {
    if (ts.isIdentifier(expression)) {
      const binding = record.imports.get(expression.text);
      if (binding) {
        const target = await resolveImport(binding.module, record.file);
        return target ? helperExport(target, binding.name) : null;
      }
      return helperExport(record.file, expression.text);
    }
    if (ts.isPropertyAccessExpression(expression) && ts.isIdentifier(expression.expression)) {
      const binding = record.imports.get(expression.expression.text);
      if (binding?.name === '*') {
        const target = await resolveImport(binding.module, record.file);
        return target ? helperExport(target, expression.name.text) : null;
      }
    }
    return null;
  }
  function evaluate(node, record, depth = 0) {
    if (!node || depth > 12) return { values: [], unknown: true, template: false };
    if (ts.isParenthesizedExpression(node) || ts.isAsExpression(node) || ts.isNonNullExpression(node) || ts.isSatisfiesExpression(node)) return evaluate(node.expression, record, depth + 1);
    if (ts.isStringLiteralLike(node)) return { values: [node.text], unknown: false, template: false };
    if (node.kind === ts.SyntaxKind.NullKeyword || node.kind === ts.SyntaxKind.UndefinedKeyword) return { values: [], unknown: false, template: false, nullable: true };
    if (ts.isConditionalExpression(node)) {
      const left = evaluate(node.whenTrue, record, depth + 1), right = evaluate(node.whenFalse, record, depth + 1);
      return { values: unique([...left.values, ...right.values]), unknown: left.unknown || right.unknown, template: left.template || right.template, nullable: left.nullable || right.nullable };
    }
    if (ts.isTemplateExpression(node)) {
      let values = [node.head.text], unknown = false, template = false;
      for (const span of node.templateSpans) {
        const result = evaluate(span.expression, record, depth + 1);
        const choices = result.values.length && !result.unknown ? result.values : ['${' + span.expression.getText(record.ast) + '}'];
        unknown ||= result.unknown || !result.values.length || Boolean(result.nullable) || values.length * choices.length > 32;
        template ||= result.unknown || !result.values.length || Boolean(result.nullable) || result.template;
        values = values.flatMap(prefix => choices.map(choice => prefix + choice + span.literal.text)).slice(0, 32);
      }
      return { values, unknown, template };
    }
    if (ts.isBinaryExpression(node) && node.operatorToken.kind === ts.SyntaxKind.PlusToken) {
      const left = evaluate(node.left, record, depth + 1), right = evaluate(node.right, record, depth + 1);
      if (left.values.length && right.values.length) return { values: left.values.flatMap(a => right.values.map(b => a + b)).slice(0, 32), unknown: left.unknown || right.unknown || left.values.length * right.values.length > 32, template: left.template || right.template };
    }
    return { values: [], unknown: true, template: false };
  }
  function methodInfo(node, record, helper) {
    if (helper === 'useResource' || helper === 'download') return { methods: ['GET'], methodUnknown: false, methodExpression: null };
    if (helper === 'upload') {
      if (!node.arguments[2]) return { methods: ['POST'], methodUnknown: false, methodExpression: null };
      const result = evaluate(node.arguments[2], record);
      return { methods: unique(result.values.filter(value => methods.has(value))), methodUnknown: result.unknown || result.values.some(value => !methods.has(value)), methodExpression: node.arguments[2].getText(record.ast) };
    }
    const options = node.arguments[1];
    if (!options) return { methods: ['GET'], methodUnknown: false, methodExpression: null };
    if (!ts.isObjectLiteralExpression(options)) return { methods: [], methodUnknown: true, methodExpression: options.getText(record.ast) };
    let result = { methods: ['GET'], methodUnknown: false, methodExpression: null };
    for (const property of options.properties) {
      if (ts.isSpreadAssignment(property)) result = { methods: result.methods, methodUnknown: true, methodExpression: options.getText(record.ast) };
      const computedName = property.name && ts.isComputedPropertyName(property.name) ? property.name.expression : null;
      if (computedName && !ts.isStringLiteralLike(computedName)) result = { methods: result.methods, methodUnknown: true, methodExpression: options.getText(record.ast) };
      if (property.name && (property.name.text === 'method' || (computedName && ts.isStringLiteralLike(computedName) && computedName.text === 'method'))) {
        const initializer = ts.isPropertyAssignment(property) ? property.initializer : null;
        const value = evaluate(initializer, record);
        result = { methods: unique(value.values.filter(item => methods.has(item))), methodUnknown: value.unknown || value.values.some(item => !methods.has(item)), methodExpression: property.getText(record.ast) };
      }
    }
    return result;
  }
  const pageResults = [];
  for (const page of pages) {
    const reachable = new Set();
    async function traverse(file) {
      if (reachable.has(file)) return;
      reachable.add(file);
      const record = await parse(file);
      for (const dependency of record.dependencies) if (dependency.target) await traverse(dependency.target);
    }
    await traverse(page.absolute);
    pageResults.push({ path: page.path, source: page.source, dependencies: unique([...reachable].map(file => slash(path.relative(root, file)))), requestIds: [], requirementIds: [] });
  }
  const requests = [];
  for (const record of [...cache.values()].sort((a, b) => a.file.localeCompare(b.file, 'en'))) {
    const calls = [];
    const visit = node => { if (ts.isCallExpression(node)) calls.push(node); ts.forEachChild(node, visit); };
    visit(record.ast);
    for (const node of calls) {
      const helper = await callHelper(record, node.expression);
      if (!helper) continue;
      const source = slash(path.relative(root, record.file));
      const line = record.ast.getLineAndCharacterOfPosition(node.getStart(record.ast)).line + 1;
      const evaluated = evaluate(node.arguments[0], record);
      const info = methodInfo(node, record, helper);
      const candidateMatches = evaluated.values.map(candidate => ({ path: candidate, methods: info.methods.map(method => ({ method, matches: matchApiCandidates(candidate, routes).filter(route => route.method === method).map(route => `${route.method} ${route.path}`) })), unknownMethodCandidates: info.methodUnknown ? matchApiCandidates(candidate, routes).map(route => `${route.method} ${route.path}`) : [] }));
      const matches = unique(candidateMatches.flatMap(candidate => [...candidate.methods.flatMap(method => method.matches), ...candidate.unknownMethodCandidates]));
      const pathKind = evaluated.template ? 'template' : evaluated.unknown ? (evaluated.values.length ? 'partial' : 'unresolved') : evaluated.values.length > 1 ? 'finite-alternatives' : evaluated.values.length ? 'static' : 'disabled';
      const allMatched = candidateMatches.length > 0 && candidateMatches.every(candidate => candidate.methods.length && candidate.methods.every(method => method.matches.length));
      const matchStatus = pathKind === 'disabled' ? 'disabled' : info.methodUnknown ? 'unresolved-method' : pathKind === 'unresolved' ? 'unresolved-path' : evaluated.unknown ? (matches.length ? 'candidate' : 'unresolved-path') : allMatched ? 'matched-static' : matches.length ? 'partially-matched-static' : routes.length ? 'unmatched-static' : 'not-inventoried';
      const id = `${source}:${line}:${node.getStart(record.ast)}`;
      const owners = pageResults.filter(page => page.dependencies.includes(source));
      const item = { id, source, line, helper, callExpression: node.getText(record.ast), pathExpression: node.arguments[0]?.getText(record.ast) ?? null, pathCandidates: evaluated.values, pathKind, pathUnknown: evaluated.unknown, nullable: Boolean(evaluated.nullable), ...info, candidateMatches, matches, matchStatus, pages: owners.map(page => page.path) };
      requests.push(item);
      for (const page of owners) page.requestIds.push(id);
    }
  }
  let declaration = null;
  if (requirements) {
    const errors = validateRequirements(requirements, { root, pages: pageResults });
    const operations = new Set(routes.map(route => `${route.method} ${route.path}`));
    for (const item of requirements.requirements) {
      for (const evidence of item.evidence) {
        const target = evidence.replace(/:\d+(?::\d+)?$/, '');
        try { await stat(path.join(root, target)); } catch (error) { if (error.code === 'ENOENT') errors.push(`${item.id}: source does not exist: ${target}`); else throw error; }
      }
      if (routes.length) for (const operation of item.current.endpoints) if (!operations.has(operation)) errors.push(`${item.id}: declared endpoint is not registered: ${operation}`);
    }
    if (errors.length) throw new Error(`Invalid admin API requirements:\n${errors.join('\n')}`);
    for (const page of pageResults) page.requirementIds = requirements.requirements.filter(item => item.pages.some(pattern => matchPagePattern(pattern, page.path))).map(item => item.id);
    declaration = { source: 'audit/admin-api-requirements.json', sha256: digest(JSON.stringify(requirements)), requirementCount: requirements.requirements.length, uncoveredPages: pageResults.filter(page => !page.requirementIds.length).map(page => page.path) };
  }
  return {
    schemaVersion: 1,
    scope: 'file-level-candidate-reachability',
    limitations: ['Local source imports, reexports and literal dynamic imports are traversed at file level; exports, render branches and runtime call reachability are not proven.', 'Shared modules can contain customer requests unrelated to a particular admin page; page/request association is a candidate.', 'Unknown path expressions and methods remain unresolved; dynamic route candidates do not prove API or UI completeness.', 'No database, browser, external service, application code or deployment environment is executed or read.'],
    summary: { pages: pageResults.length, files: cache.size, requests: requests.length, unresolvedPaths: requests.filter(item => item.pathUnknown).length, unresolvedMethods: requests.filter(item => item.methodUnknown).length, staticMatched: requests.filter(item => item.matchStatus === 'matched-static').length, candidateMatched: requests.filter(item => item.matchStatus === 'candidate').length, partiallyMatchedStatic: requests.filter(item => item.matchStatus === 'partially-matched-static').length, unmatchedStatic: requests.filter(item => item.matchStatus === 'unmatched-static').length, unresolvedDependencies: unresolvedDependencies.length },
    pages: pageResults,
    requests,
    dependencies: [...cache.values()].map(record => ({ source: slash(path.relative(root, record.file)), sha256: digest(record.text), imports: record.dependencies.map(item => ({ ...item, target: item.target ? slash(path.relative(root, item.target)) : null })) })).sort((a, b) => a.source.localeCompare(b.source, 'en')),
    unresolvedDependencies,
    declaration,
  };
}

export async function generate({ root = process.cwd(), routes, outputDir = path.join(root, 'output/audit') } = {}) {
  root = path.resolve(root);
  if (!routes) {
    const { collectCurrentRoutes } = await import('./admin-api-inventory.mjs');
    routes = (await collectCurrentRoutes(root)).routes;
  }
  const result = await collectAdminConsumers({ root, routes, requirements: await loadRequirements(root) });
  await mkdir(outputDir, { recursive: true });
  await writeFile(path.join(outputDir, 'admin-api-consumers.json'), JSON.stringify(result, null, 2) + '\n');
  const lines = ['# 后台页面 API 消费候选索引', '', '由 scripts/admin-api-consumers.mjs 从当前源码生成；不提交生成物。', '', ...result.limitations.map(value => `- ${value}`), '', `页面 ${result.summary.pages}；请求 ${result.summary.requests}；路径不确定 ${result.summary.unresolvedPaths}；方法不确定 ${result.summary.unresolvedMethods}。`, '', '| 页面 | 源码 | 文件依赖 | 请求候选 | 需求声明 |', '| --- | --- | ---: | ---: | ---: |'];
  for (const page of result.pages) lines.push(`| ${page.path} | ${page.source} | ${page.dependencies.length} | ${page.requestIds.length} | ${page.requirementIds.length} |`);
  lines.push('', '## 未解析请求', '', '| 源码 | helper | 路径表达式 | 方法 | 状态 |', '| --- | --- | --- | --- | --- |');
  for (const item of result.requests.filter(item => item.pathUnknown || item.methodUnknown || ['unmatched-static', 'partially-matched-static'].includes(item.matchStatus))) lines.push(`| ${item.source}:${item.line} | ${item.helper} | ${String(item.pathExpression).replaceAll('|', '\\|').replaceAll('\n', ' ')} | ${item.methods.join(', ') || 'unknown'}${item.methodUnknown ? ' + unknown' : ''} | ${item.matchStatus} |`);
  await writeFile(path.join(outputDir, 'admin-api-consumers.md'), lines.join('\n') + '\n');
  return result;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const result = await generate({ root: process.cwd() });
  console.log(JSON.stringify(result.summary));
}
