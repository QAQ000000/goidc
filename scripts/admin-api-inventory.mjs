#!/usr/bin/env node
import { readFile, readdir, mkdir, writeFile, access } from 'node:fs/promises';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { createHash } from 'node:crypto';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const execute = promisify(execFile);
const helper = fileURLToPath(new URL('./lib/admin-api-inventory.go', import.meta.url));
const hash = value => createHash('sha256').update(value).digest('hex');
const slash = value => value.split(path.sep).join('/');
const operationShape = route => route.method + ' ' + route.path.replace(/\{([^}]+)\}/g, (_, name) => name === '$' ? '{$}' : name.endsWith('...') ? '{...}' : '{}');

async function filesUnder(directory) {
  const files = [];
  for (const entry of (await readdir(directory, { withFileTypes: true })).sort((a, b) => a.name.localeCompare(b.name, 'en'))) {
    const file = path.join(directory, entry.name);
    if (entry.isDirectory()) files.push(...await filesUnder(file));
    else if (entry.isFile()) files.push(file);
  }
  return files;
}
async function sourceEvidence(root, sources) {
  return Promise.all([...new Set(sources)].sort().map(async source => ({ path: slash(source), sha256: hash(await readFile(path.join(root, source))) })));
}

// Go's parser reads every production source file without importing application
// code. The helper only evaluates literal permission maps and their local loops.
export async function collectCurrentRoutes(root = process.cwd()) {
  root = path.resolve(root);
  const { stdout } = await execute('go', ['run', helper, root], { cwd: path.dirname(helper), maxBuffer: 16 * 1024 * 1024 });
  const parsed = JSON.parse(stdout);
  const seen = new Map();
  for (const route of parsed.routes) {
    const operation = route.method + ' ' + route.path;
    const previous = seen.get(operationShape(route));
    if (previous) throw new Error(`duplicate current operation ${operation}: ${previous.source}:${previous.line} and ${route.source}:${route.line}`);
    seen.set(operationShape(route), route);
  }
  const routes = parsed.routes.map(route => {
    const operation = route.method + ' ' + route.path;
    const administrative = route.path === '/api/v1/admin' || route.path.startsWith('/api/v1/admin/');
    const permission = administrative ? parsed.permissions[operation] ?? null : null;
    if (administrative && !permission) throw new Error(`missing adminRoutePermissions for ${operation} (${route.source}:${route.line})`);
    return { ...route, operation, administrative, permission, permissionEvidence: administrative ? parsed.permissionEvidence[operation] ?? [] : [] };
  }).sort((a, b) => a.path.localeCompare(b.path, 'en') || a.method.localeCompare(b.method, 'en'));
  return {
    routes,
    sources: await sourceEvidence(root, parsed.sources),
    summary: { registrations: routes.length, administrative: routes.filter(route => route.administrative).length, productionFiles: parsed.sources.length },
    limitations: ['Static registrations in all production HTTP API Go files; build conditions and registration-function reachability require separate review.', 'Permissions are the current adminRoutePermissions baseline; handler and Store checks, data scopes, payloads and business completeness are not inferred.'],
  };
}

// A small PHP lexer keeps strings and comments out of executable declarations.
// No PHP is loaded, required or executed, including included route files.
function tokens(source, firstLine = 1, tolerant = false) {
  const out = [];
  let i = 0, line = firstLine;
  while (i < source.length) {
    const start = i, startLine = line, ch = source[i];
    if (/\s/.test(ch)) { if (ch === '\n') line++; i++; continue; }
    let kind = 'symbol', value;
    if (source.startsWith('//', i) || ch === '#' || source.startsWith('/*', i)) {
      kind = 'comment';
      if (source.startsWith('/*', i)) { const end = source.indexOf('*/', i + 2); i = end < 0 ? source.length : end + 2; }
      else { const end = source.indexOf('\n', i); i = end < 0 ? source.length : end; }
      value = source.slice(start, i); line += (value.match(/\n/g) ?? []).length;
    } else if (ch === '"' || ch === "'") {
      kind = 'string'; value = ''; i++;
      let closed = false;
      while (i < source.length) {
        if (source[i] === ch) { i++; closed = true; break; }
        if (source[i] === '\\' && i + 1 < source.length) {
          const next = source[i + 1];
          value += next === ch || next === '\\' ? next : ch === '"' && 'nrt'.includes(next) ? { n: '\n', r: '\r', t: '\t' }[next] : '\\' + next;
          if (next === '\n') line++;
          i += 2;
        } else { if (source[i] === '\n') line++; value += source[i++]; }
      }
      if (!closed && !tolerant) throw new Error(`unterminated PHP string at line ${startLine}`);
    } else {
      const identifier = source.slice(i).match(/^\$?[A-Za-z_][A-Za-z_0-9]*/);
      if (identifier) { kind = 'identifier'; value = identifier[0]; i += value.length; }
      else if (source.startsWith('::', i)) { value = '::'; i += 2; }
      else { value = ch; i++; }
    }
    out.push({ kind, value, line: startLine, start, end: i });
  }
  return out;
}
function callArguments(list, open, end) {
  const args = [], delimiters = [];
  let start = open + 1;
  for (let i = start; i < end; i++) {
    const token = list[i];
    if (token.kind === 'comment' || token.kind === 'string') continue;
    if ('([{'.includes(token.value)) delimiters.push(token.value);
    else if (')]}'.includes(token.value)) {
      if (token.value === ')' && !delimiters.length) { args.push(list.slice(start, i)); return { args, close: i }; }
      const expected = { ')': '(', ']': '[', '}': '{' }[token.value];
      if (delimiters.pop() !== expected) throw new Error(`unbalanced PHP route at line ${list[open].line}`);
    } else if (token.value === ',' && !delimiters.length) { args.push(list.slice(start, i)); start = i + 1; }
  }
  throw new Error(`unclosed PHP route at line ${list[open].line}`);
}
function literalExpression(parts, variables = {}) {
  let result = '', expectValue = true;
  for (const token of parts.filter(token => token.kind !== 'comment')) {
    if (expectValue) {
      if (token.kind === 'string') result += token.value;
      else if (Object.hasOwn(variables, token.value)) result += variables[token.value];
      else return null;
    } else if (token.value !== '.') return null;
    expectValue = !expectValue;
  }
  return expectValue ? null : result;
}
const legacyPath = (prefix, value) => value === null || prefix === null ? null : '/' + [prefix, value].filter(Boolean).join('/').replace(/^\/+|\/+$/g, '').replace(/\/{2,}/g, '/');

export function parseLegacyRouteSource(text, source) {
  const result = { routes: [], dynamicRoutes: [], commentedRoutes: [], includes: [] };
  function walk(list, prefix = '', commented = false) {
    for (let i = 0; i < list.length; i++) {
      const token = list[i];
      if (token.kind === 'comment') {
        const body = token.value.replace(/^\/\/|^#|^\/\*|\*\/$/g, match => ' '.repeat(match.length));
        const first = body.search(/\bRoute\s*::\s*(?:get|post|put|patch|delete|head|options|rule|any|resource|controller|group)\s*\(/i);
        if (first >= 0) walk(tokens(body.slice(0, first).replace(/[^\n]/g, ' ') + body.slice(first), token.line, true), prefix, true);
        continue;
      }
      if (token.kind !== 'identifier') continue;
      if (/^(include|include_once|require|require_once)$/i.test(token.value)) {
        let end = i + 1; while (end < list.length && list[end].value !== ';') end++;
        result.includes.push({ expression: text.slice(token.end, list[end]?.start ?? text.length).trim(), parts: list.slice(i + 1, end), source, line: token.line, commented }); i = end; continue;
      }
      if (token.value.toLowerCase() !== 'route' || list[i + 1]?.value !== '::' || list[i + 3]?.value !== '(') continue;
      const method = list[i + 2].value.toLowerCase();
      if (!['get', 'post', 'put', 'patch', 'delete', 'head', 'options', 'rule', 'any', 'resource', 'controller', 'group'].includes(method)) continue;
      const { args, close } = callArguments(list, i + 3, list.length);
      const value = literalExpression(args[0] ?? [], { $domain: '{legacy_admin}' });
      if (method === 'group') {
        const callback = args[1] ?? [];
        const open = callback.findIndex(token => token.value === '{');
        if (open < 0 || callback.at(-1)?.value !== '}') throw new Error(`${source}:${token.line}: unresolved route group callback`);
        walk(callback.slice(open + 1, -1), legacyPath(prefix, value), commented);
      } else {
        const handler = literalExpression(args[1] ?? []);
        const path = legacyPath(prefix, value);
        const dynamic = method === 'resource' || method === 'controller';
        const ruleMethods = method === 'rule' ? literalExpression(args[2] ?? []) : null;
        const methods = dynamic ? [] : method === 'rule' ? (ruleMethods ?? 'ANY').toUpperCase().split('|') : [method.toUpperCase()];
        const route = { kind: dynamic ? method : methods.includes('ANY') ? 'any' : 'explicit', method: dynamic ? null : methods.join('|'), methods, path, handler, registrationMethod: method.toUpperCase(), source, line: token.line, commented, resolution: path === null || handler === null ? 'unresolved_expression' : dynamic ? 'dynamic_not_expanded' : 'source_declaration' };
        if (commented) result.commentedRoutes.push(route);
        else (dynamic ? result.dynamicRoutes : result.routes).push(route);
      }
      i = close;
    }
  }
  walk(tokens(text));
  return result;
}

export async function collectLegacyRoutes(legacyRoot) {
  if (!legacyRoot) throw new Error('legacyRoot is required; pass --legacy-root explicitly');
  legacyRoot = path.resolve(legacyRoot);
  const pending = (await filesUnder(path.join(legacyRoot, 'data/route'))).filter(file => file.endsWith('.php'));
  const sources = [], visited = new Set(), routes = [], dynamicRoutes = [], commentedRoutes = [], includes = [], unresolvedIncludes = [];
  while (pending.length) {
    const absolute = pending.shift(), source = slash(path.relative(legacyRoot, absolute));
    if (visited.has(source)) continue;
    visited.add(source); sources.push(source);
    const parsed = parseLegacyRouteSource(await readFile(absolute, 'utf8'), source);
    routes.push(...parsed.routes); dynamicRoutes.push(...parsed.dynamicRoutes); commentedRoutes.push(...parsed.commentedRoutes);
    for (const include of parsed.includes) {
      const value = literalExpression(include.parts, { CMF_ROOT: legacyRoot + path.sep, __DIR__: path.dirname(absolute), __FILE__: absolute });
      const { parts, ...entry } = include;
      if (include.commented) { includes.push({ ...entry, resolution: 'commented' }); continue; }
      if (value === null) { const item = { ...entry, resolution: 'dynamic_expression' }; includes.push(item); unresolvedIncludes.push(item); continue; }
      const target = path.resolve(path.dirname(absolute), value);
      const relative = slash(path.relative(legacyRoot, target));
      let resolution = relative.startsWith('../') || path.isAbsolute(relative) ? 'outside_legacy_root' : 'resolved';
      if (resolution === 'resolved') { try { await access(target); } catch (error) { if (error.code !== 'ENOENT') throw error; resolution = 'missing_file'; } }
      const item = { ...entry, target: relative, resolution }; includes.push(item);
      if (resolution !== 'resolved') unresolvedIncludes.push(item);
      else if (target.endsWith('.php')) pending.push(target);
    }
  }
  const duplicates = new Map();
  for (const route of routes.filter(route => route.path)) for (const method of route.methods) {
    const operation = method + ' ' + route.path;
    if (!duplicates.has(operation)) duplicates.set(operation, []);
    duplicates.get(operation).push({ source: route.source, line: route.line });
  }
  const duplicateOperations = [...duplicates].filter(([, declarations]) => declarations.length > 1).map(([operation, declarations]) => ({ operation, declarations }));
  return {
    routes, dynamicRoutes, commentedRoutes, includes, unresolvedIncludes, duplicateOperations,
    sources: await sourceEvidence(legacyRoot, sources),
    summary: { explicitDeclarations: routes.length, concreteMethodDeclarations: routes.filter(route => route.kind === 'explicit').length, anyDeclarations: routes.filter(route => route.kind === 'any').length, resourceDeclarations: dynamicRoutes.filter(route => route.kind === 'resource').length, controllerDeclarations: dynamicRoutes.filter(route => route.kind === 'controller').length, commentedDeclarations: commentedRoutes.length, unresolvedIncludes: unresolvedIncludes.length, unresolvedExpressions: [...routes, ...dynamicRoutes].filter(route => route.resolution === 'unresolved_expression').length, duplicateOperations: duplicateOperations.length },
    limitations: ['Declarations preserve ANY, resource and controller separately; no runtime expansion, middleware execution, duplicate precedence or protocol compatibility is inferred.', 'Missing or dynamic includes are reported; application PHP and deployment configuration are never executed.'],
  };
}

export async function generate({ root = process.cwd(), legacyRoot, outputDir = 'output/audit' } = {}) {
  const [current, legacy] = await Promise.all([collectCurrentRoutes(root), collectLegacyRoutes(legacyRoot)]);
  const result = { schemaVersion: 1, generator: [{ path: 'scripts/admin-api-inventory.mjs', sha256: hash(await readFile(fileURLToPath(import.meta.url))) }, { path: 'scripts/lib/admin-api-inventory.go', sha256: hash(await readFile(helper)) }], current, legacy, scope: 'Source inventory only; endpoint fields, data scopes, business parity, tests and runtime acceptance require independent evidence.' };
  const target = path.resolve(root, outputDir);
  await mkdir(target, { recursive: true });
  await writeFile(path.join(target, 'admin-api-inventory.json'), JSON.stringify(result, null, 2) + '\n');
  const lines = ['# 后台 API 静态盘点', '', '本文件由源码生成，不代表接口业务完整性、权限链或运行验收通过。完整条目与 SHA-256 见 [JSON](admin-api-inventory.json)。', '', `当前 Go 注册 ${current.summary.registrations} 条，其中后台 ${current.summary.administrative} 条；扫描 ${current.summary.productionFiles} 个生产文件。`, '', `旧版显式声明 ${legacy.summary.explicitDeclarations} 条（ANY ${legacy.summary.anyDeclarations} 条），另有 resource ${legacy.summary.resourceDeclarations}、controller ${legacy.summary.controllerDeclarations}；注释声明 ${legacy.summary.commentedDeclarations}、未解析 include ${legacy.summary.unresolvedIncludes}。动态声明不展开为猜测的运行接口，旧重复声明只登记来源，不认定生效顺序。`, '', '| 当前注册文件 | 所有注册 | 后台注册 |', '| --- | ---: | ---: |'];
  for (const source of [...new Set(current.routes.map(route => route.source))].sort()) lines.push(`| ${source} | ${current.routes.filter(route => route.source === source).length} | ${current.routes.filter(route => route.source === source && route.administrative).length} |`);
  lines.push('', '| 旧版声明文件 | 显式 | resource | controller |', '| --- | ---: | ---: | ---: |');
  for (const source of [...new Set([...legacy.routes, ...legacy.dynamicRoutes].map(route => route.source))].sort()) lines.push(`| ${source} | ${legacy.routes.filter(route => route.source === source).length} | ${legacy.dynamicRoutes.filter(route => route.source === source && route.kind === 'resource').length} | ${legacy.dynamicRoutes.filter(route => route.source === source && route.kind === 'controller').length} |`);
  if (legacy.unresolvedIncludes.length) { lines.push('', '未解析 include：', ''); for (const include of legacy.unresolvedIncludes) lines.push(`- ${include.source}:${include.line} → ${include.target ?? include.expression} (${include.resolution})`); }
  await writeFile(path.join(target, 'admin-api-inventory.md'), lines.join('\n') + '\n');
  const cell = value => String(value ?? '').replace(/\|/g, '&#124;').replace(/\r?\n/g, ' ');
  const catalog = ['# 后台接口目录', '', '本目录仅登记源码中的注册、权限与历史声明。字段、状态机及运行证据由稳定 API 契约和需求处置清单说明；不能用 URL 数量差推断缺失功能。', '', '## 当前后台操作', '', '| 方法与路径 | 入口包装 | 权限 | Handler | 注册来源 |', '| --- | --- | --- | --- | --- |'];
  for (const route of current.routes.filter(route => route.administrative)) catalog.push(`| \`${cell(route.operation)}\` | ${cell(route.authWrapper)} | ${cell(route.permission)} | ${cell(route.handler)} | ${cell(route.source)}:${route.line} |`);
  catalog.push('', '## 当前其他注册', '', '包括后台复用的账号/工单、公开接口、客户专属接口和回调。出现于目录不代表后台身份可调用；按入口包装、portal和领域权限决定。', '', '| 方法与路径 | 入口包装 | Handler | 注册来源 |', '| --- | --- | --- | --- |');
  for (const route of current.routes.filter(route => !route.administrative)) catalog.push(`| \`${cell(route.operation)}\` | ${cell(route.authWrapper)} | ${cell(route.handler)} | ${cell(route.source)}:${route.line} |`);
  catalog.push('', '## 旧后台显式声明', '', 'ANY 保留原意，不能当成一条确定的 HTTP 方法；重复声明只保留来源，不认定运行时生效顺序。', '', '| 方法与路径 | 控制器动作 | 来源 |', '| --- | --- | --- |');
  for (const route of legacy.routes.filter(route => route.source === 'data/route/admin.php')) catalog.push(`| \`${cell(route.method)} ${cell(route.path)}\` | ${cell(route.handler)} | ${cell(route.source)}:${route.line} |`);
  catalog.push('', '## 旧后台动态声明', '', '| 类型 | 路径 | 控制器 | 来源 |', '| --- | --- | --- | --- |');
  for (const route of legacy.dynamicRoutes.filter(route => route.source === 'data/route/admin.php')) catalog.push(`| ${cell(route.kind)} | ${cell(route.path)} | ${cell(route.handler)} | ${cell(route.source)}:${route.line} |`);
  await writeFile(path.join(target, 'admin-api-catalog.md'), catalog.join('\n') + '\n');
  return result;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const args = process.argv.slice(2);
  const flag = name => { const i = args.indexOf(name); if (i < 0) return undefined; if (!args[i + 1] || args[i + 1].startsWith('--')) throw new Error(`${name} requires a value`); return args[i + 1]; };
  const allowed = new Set(['--root', '--legacy-root', '--output-dir']);
  for (let i = 0; i < args.length; i += 2) if (!allowed.has(args[i])) throw new Error('unknown argument: ' + args[i]);
  const result = await generate({ root: flag('--root'), legacyRoot: flag('--legacy-root'), outputDir: flag('--output-dir') });
  console.log(JSON.stringify({ current: result.current.summary, legacy: result.legacy.summary }));
}
