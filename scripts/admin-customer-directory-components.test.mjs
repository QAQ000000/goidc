import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import path from 'node:path';
import test from 'node:test';
import vm from 'node:vm';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const require = createRequire(path.join(root, 'web/package.json'));
const ts = require('typescript'), React = require('react');
const tick = () => new Promise(resolve => setImmediate(resolve));
const customer = (id = 41) => ({ id, username: `customer${id}`, email: `c${id}@example.test`, display_name: `客户 ${id}`, status: 'active', balance_cents: 12345, created_at: '2026-10-01T10:00:00Z', group_id: null, group_name: null, sales_owner_id: null, sales_owner_name: null, sales_version: 1 });
const page = (items = [customer()], total = items.length) => ({ items, total, limit: 25, offset: 0 });

// Execute the actual directory, shared page/table, URL parser and resource hook.
// Only transport, browser APIs, shell/permissions and UI primitives are doubles.
function fixture(query = '', options = {}) {
  let current, tree, nextType = 0;
  const instances = new Map(), types = new Map(), effects = [], cache = new Map();
  const env = {
    query, calls: [], navigation: [], copies: [], refreshes: [], definitionReads: [],
    actor: { id: 7, role: 'admin', permissions: ['customers.view'], customer_scope: 'all', customer_ownerless: true },
    result: page(), errors: new Map(), ...options,
  };
  const changed = (a, b) => !a || !b || a.length !== b.length || a.some((value, i) => !Object.is(value, b[i]));
  const hooks = {
    ...React,
    useState(initial) { const owner = current, index = owner.cursor++; if (!(index in owner.values)) owner.values[index] = typeof initial === 'function' ? initial() : initial; return [owner.values[index], next => { owner.values[index] = typeof next === 'function' ? next(owner.values[index]) : next; }]; },
    useRef(initial) { const index = current.cursor++; return current.values[index] ??= { current: initial }; },
    useEffect(callback, deps) { const owner = current, index = owner.cursor++, previous = owner.values[index]; if (changed(previous?.deps, deps)) effects.push(() => { previous?.cleanup?.(); owner.values[index] = { deps, cleanup: callback() }; }); },
    useMemo(factory, deps) { const index = current.cursor++, previous = current.values[index]; if (changed(previous?.deps, deps)) current.values[index] = { deps, value: factory() }; return current.values[index].value; },
    useCallback(callback, deps) { return hooks.useMemo(() => callback, deps); },
  };
  class ApiError extends Error { constructor(status, code, message = code) { super(message); Object.assign(this, { status, code }); } }
  env.ApiError = ApiError;
  const primitive = tag => props => React.createElement(tag, props, props.children);
  const ui = new Proxy({
    Button: primitive('button'), Input: primitive('input'),
    Table: primitive('table'), TableHeader: primitive('thead'), TableBody: primitive('tbody'), TableHead: primitive('th'), TableRow: primitive('tr'), TableCell: primitive('td'),
    Dialog: props => props.open ? props.children : null,
  }, { get: (target, name) => target[name] ??= primitive(String(name).toLowerCase()) });
  const can = permission => !!env.actor && (env.actor.role === 'super_admin' || env.actor.permissions.includes(permission));
  const history = kind => (_state, _unused, href) => { env.navigation.push({ kind, href }); env.query = new URL(href, 'https://local.test').search.slice(1); };
  const mocks = {
    react: hooks,
    'next/navigation': { useSearchParams: () => new URLSearchParams(env.query) },
    'next/link': { default: primitive('a') },
    'lucide-react': new Proxy({}, { get: (_, name) => primitive(`icon-${String(name)}`) }),
    './workspace': { Workspace: props => props.children, useAdminIdentity: () => env.actor, useAdminPermission: can, PermissionGate: props => can(props.permission) ? props.children : null },
    './common': { Loading: () => React.createElement('p', { role: 'status' }, '加载中'), Failure: ({ error, retry }) => React.createElement('button', { onClick: retry }, error.message), Status: ({ value }) => React.createElement('span', null, value) },
    './customer-field-inputs': { useCustomerFieldDefinitions: enabled => { env.definitionReads.push(enabled); return { fields: [], ready: true, error: null, refresh() {} }; } },
    '@/lib/api': {
      ApiError, authPortal: (_path, portal) => portal ?? 'admin', money: cents => `¥${(cents / 100).toFixed(2)}`, date: value => value.slice(0, 10),
      api: async (endpoint, request = {}) => {
        const call = { endpoint, request }; env.calls.push(call);
        assert.ok(!request.method || request.method === 'GET', 'directory must remain read only');
        if (env.respond) { const value = env.respond(call); if (value !== undefined) return await value; }
        if (env.errors.has(endpoint)) throw env.errors.get(endpoint);
        if (endpoint.startsWith('/admin/customers/page?')) return env.result;
        if (endpoint.startsWith('/admin/customer-groups')) return [{ id: 3, name: 'VIP' }];
        if (endpoint === '/admin/sales-owners') return [{ id: 12, username: 'sales12', display_name: '销售十二' }];
        if (endpoint.startsWith('/admin/customer-levels')) return { items: [], total: 0 };
        throw new Error(`Unexpected API ${endpoint}`);
      },
    },
  };
  function load(relative) {
    if (cache.has(relative)) return cache.get(relative).exports;
    const filename = path.join(root, relative), module = { exports: {} }; cache.set(relative, module);
    const { outputText } = ts.transpileModule(readFileSync(filename, 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX } });
    vm.runInNewContext(outputText, {
      module, exports: module.exports, URLSearchParams, URL, Number, Error, TextEncoder, AbortController, window: { history: { pushState: history('push'), replaceState: history('replace') }, setTimeout, clearTimeout },
      navigator: { clipboard: { writeText: async value => { if (env.copyFailure) throw new Error('denied'); env.copies.push(value); } } },
      require: id => {
        if (Object.hasOwn(mocks, id)) return mocks[id];
        if (id.includes('/ui/')) return ui;
        if (id.startsWith('.') || id.startsWith('@/')) {
          const base = id.startsWith('@/') ? `web/src/${id.slice(2)}` : path.join(path.dirname(relative), id);
          for (const candidate of [`${base}.ts`, `${base}.tsx`]) if (existsSync(path.join(root, candidate))) return load(candidate);
        }
        return require(id);
      },
    }, { filename });
    return module.exports;
  }
  const Component = load('web/src/components/admin-customers.tsx').AdminCustomersView;
  function expand(element, location, seen) {
    if (!element || typeof element !== 'object') return element;
    if (Array.isArray(element)) return element.map((child, index) => expand(child, `${location}:${index}`, seen));
    if (!element.type) return element;
    const keyed = `${location}:${element.key ?? ''}`;
    if (typeof element.type === 'function') {
      if (!types.has(element.type)) types.set(element.type, nextType++);
      const id = `${keyed}:component${types.get(element.type)}`;
      let owner = instances.get(id); if (!owner) { owner = { values: [], cursor: 0 }; instances.set(id, owner); }
      seen.add(id); const parent = current; current = owner; owner.cursor = 0;
      const result = element.type(element.props); current = parent;
      return expand(result, `${id}:result`, seen);
    }
    return { ...element, props: { ...element.props, children: expand(element.props.children, `${keyed}:children`, seen) } };
  }
  env.render = () => {
    const seen = new Set(); tree = expand(React.createElement(Component), 'root', seen);
    for (const [id, owner] of instances) if (!seen.has(id)) { for (const value of owner.values) value?.cleanup?.(); instances.delete(id); }
    for (const effect of effects.splice(0)) effect();
    return tree;
  };
  env.unmount = () => { for (const owner of instances.values()) for (const value of owner.values) value?.cleanup?.(); instances.clear(); };
  env.all = () => nodes(tree);
  env.text = () => text(tree);
  env.find = predicate => { const match = env.all().find(predicate); assert.ok(match, `missing ${predicate}`); return match; };
  env.button = label => env.find(node => node.type === 'button' && text(node) === label);
  env.ready = async () => { env.render(); await tick(); env.render(); return env; };
  return env;
}
function nodes(tree) { if (!tree || typeof tree !== 'object') return []; if (Array.isArray(tree)) return tree.flatMap(nodes); return [tree, ...nodes(tree.props?.children)]; }
function text(tree) { if (typeof tree === 'string' || typeof tree === 'number') return String(tree); if (!tree) return ''; if (Array.isArray(tree)) return tree.map(text).join(''); return text(tree.props?.children); }
const directoryCalls = env => env.calls.filter(call => call.endpoint.startsWith('/admin/customers/page?'));
const params = endpoint => new URL(endpoint, 'https://local.test').searchParams;

test('directory keeps strict paged API, typed filters, authorized options and desktop/mobile detail return context', async () => {
  const query = new URLSearchParams({ q: '原客户', company: 'example', group_id: '3', custom_fields: JSON.stringify({ number: 0, enabled: false }), limit: '50', offset: '50' }).toString();
  const env = fixture(query, { result: page([customer()], 120) });
  env.actor.permissions.push('customer_groups.view', 'customers.manage');
  await env.ready();
  assert.equal(directoryCalls(env).length, 1);
  const call = directoryCalls(env)[0];
  assert.equal(call.request.portal, 'admin'); assert.equal(call.endpoint.startsWith('/api/v1/'), false);
  assert.equal(params(call.endpoint).get('limit'), '50'); assert.equal(params(call.endpoint).get('offset'), '50');
  assert.deepEqual(JSON.parse(params(call.endpoint).get('custom_fields')), { number: 0, enabled: false });
  assert.ok(env.calls.some(call => call.endpoint === '/admin/customer-groups?options=true'));
  assert.ok(!env.calls.some(call => call.endpoint === '/admin/sales-owners'));
  const table = env.find(node => node.type === 'table'); assert.equal(table.props['aria-label'], '客户列表');
  assert.equal(env.all().filter(node => node.type === 'th').length, 8);
  const card = env.find(node => node.type === 'article'); assert.match(text(card), /客户 41/); assert.match(text(card), /¥123.45/);
  for (const link of env.all().filter(node => node.type === 'a' && node.props.href?.startsWith('/admin/customers/41?'))) {
    assert.equal(params(link.props.href).get('company'), 'example'); assert.equal(params(link.props.href).get('offset'), '50');
  }
  assert.ok(env.all().some(node => node.type === 'a' && node.props.href.startsWith('/admin/customers/new?')));
  assert.match(env.text(), /共 120 位客户/); env.unmount();
});

test('editing filter drafts does not request until submit and query navigation resets pagination', async () => {
  const env = await fixture('q=before&limit=50&offset=50', { result: page([customer()], 120) }).ready();
  env.find(node => node.type === 'input' && node.props['aria-label'] === '客户关键词').props.onChange({ target: { value: 'after' } });
  env.render(); assert.equal(directoryCalls(env).length, 1); assert.equal(env.navigation.length, 0);
  env.find(node => node.type === 'form').props.onSubmit({ preventDefault() {} });
  assert.equal(env.navigation.at(-1).kind, 'push');
  assert.equal(params(env.navigation.at(-1).href).get('q'), 'after'); assert.equal(params(env.navigation.at(-1).href).get('offset'), null);
  await env.ready(); assert.equal(directoryCalls(env).length, 2); assert.equal(params(directoryCalls(env)[1].endpoint).get('q'), 'after');
  env.button('清空条件').props.onClick(); assert.equal(env.navigation.at(-1).href, '/admin/customers'); env.unmount();
});

test('pagination preserves filters and page size changes reset offset without deriving a fake total', async () => {
  const env = await fixture('q=needle&company=vendor&limit=25', { result: page([customer()], 120) }).ready();
  env.button('>').props.onClick();
  assert.equal(params(env.navigation.at(-1).href).get('offset'), '25'); assert.equal(params(env.navigation.at(-1).href).get('company'), 'vendor');
  await env.ready();
  env.find(node => node.type === 'select' && node.props.value === '25').props.onValueChange('50');
  assert.equal(params(env.navigation.at(-1).href).get('limit'), '50'); assert.equal(params(env.navigation.at(-1).href).get('offset'), null);
  assert.equal(params(env.navigation.at(-1).href).get('q'), 'needle'); env.unmount();
});

test('permission and malformed URL failures prevent list reads and never turn into empty data', async () => {
  const denied = fixture(); denied.actor.permissions = []; await denied.ready();
  assert.equal(denied.calls.length, 0); assert.match(denied.text(), /没有查看客户查询结果的权限/); assert.doesNotMatch(denied.text(), /暂无客户/); denied.unmount();
  for (const query of ['limit=1&limit=2', 'custom_fields=%7Bbad', 'custom_fields=%7B%22x%22%3Anull%7D', 'offset=-1']) {
    const env = await fixture(query).ready();
    assert.equal(directoryCalls(env).length, 0, query); assert.ok(env.all().some(node => node.props?.role === 'alert')); assert.doesNotMatch(env.text(), /暂无客户/);
    env.button('清空条件').props.onClick(); assert.equal(env.navigation.at(-1).href, '/admin/customers'); env.unmount();
  }
});

test('server permission failures, ordinary failures and loading keep independent recovery states', async () => {
  const forbidden = fixture(); forbidden.errors.set('/admin/customers/page?limit=25', new forbidden.ApiError(403, 'denied'));
  await forbidden.ready(); assert.match(forbidden.text(), /没有查看客户查询结果的权限/); assert.doesNotMatch(forbidden.text(), /暂无客户/); forbidden.unmount();
  const failed = fixture(); failed.errors.set('/admin/customers/page?limit=25', new Error('列表服务不可用'));
  await failed.ready(); assert.match(failed.text(), /列表服务不可用/); assert.doesNotMatch(failed.text(), /暂无客户/);
  failed.errors.clear(); failed.button('重新加载').props.onClick(); await failed.ready();
  assert.equal(directoryCalls(failed).length, 2); assert.match(failed.text(), /客户 41/); failed.unmount();
  let finish; const loading = fixture('', { respond: call => call.endpoint.startsWith('/admin/customers/page?') ? new Promise(resolve => { finish = resolve; }) : undefined });
  loading.render(); assert.ok(loading.all().some(node => node.props?.['aria-label'] === '正在加载客户查询结果')); assert.doesNotMatch(loading.text(), /暂无客户/);
  finish(page()); await tick(); loading.render(); assert.match(loading.text(), /客户 41/); loading.unmount();
});

test('empty results distinguish unrestricted and filtered scope and retain pagination and clear action', async () => {
  const empty = await fixture('', { result: page([]) }).ready(); assert.match(empty.text(), /暂无客户/); assert.equal(empty.all().some(node => node.type === 'table'), false); empty.unmount();
  const filtered = await fixture('q=missing', { result: page([]) }).ready();
  assert.match(filtered.text(), /没有符合条件的客户/); filtered.button('清空筛选条件').props.onClick(); assert.equal(filtered.navigation.at(-1).href, '/admin/customers');
  assert.match(filtered.text(), /共 0 条/); filtered.unmount();
});

test('actor and URL changes abort former reads and cannot render a late response from a prior scope', async () => {
  let finish;
  const env = fixture('q=first', { respond: call => call.endpoint.includes('q=first') ? new Promise(resolve => { finish = resolve; }) : undefined });
  env.render(); const old = directoryCalls(env)[0];
  env.actor = { ...env.actor, id: 8, customer_scope: 'owned' }; env.query = 'q=second'; await env.ready();
  assert.equal(old.request.signal.aborted, true); assert.equal(directoryCalls(env).length, 2);
  finish(page([{ ...customer(99), display_name: '旧员工范围数据' }])); await tick(); env.render();
  assert.doesNotMatch(env.text(), /旧员工范围数据/); assert.match(env.text(), /客户 41/);
  env.actor = { ...env.actor, permissions: [] }; env.render(); assert.doesNotMatch(env.text(), /客户 41/); env.unmount();
});

test('out of range page uses replace history and copy customer ID preserves success and failure feedback', async () => {
  const env = await fixture('q=kept&limit=25&offset=100', { result: page([customer()], 26) }).ready();
  assert.equal(env.navigation.at(-1).kind, 'replace'); assert.equal(params(env.navigation.at(-1).href).get('offset'), '25'); assert.equal(params(env.navigation.at(-1).href).get('q'), 'kept');
  const copy = env.find(node => node.type === 'button' && node.props['aria-label'] === '复制客户编号 41');
  const target = {}; copy.props.onClick({ currentTarget: target }); await tick(); assert.deepEqual(env.copies, ['41']); assert.equal(target.title, '已复制');
  env.copyFailure = true; copy.props.onClick({ currentTarget: target }); await tick(); assert.equal(target.title, '复制失败，请手动复制编号'); env.unmount();
});

test('mobile filter sheet edits the shared draft and applies filters only after explicit submit', async () => {
  const env = await fixture().ready();
  assert.equal(env.definitionReads.at(-1), false);
  env.button('筛选').props.onClick(); env.render();
  assert.equal(env.definitionReads.at(-1), true);
  const forms = env.all().filter(node => node.type === 'form');
  const sheet = forms.find(node => node.props.className === 'space-y-5'); assert.ok(sheet);
  const keyword = nodes(sheet).find(node => node.type === 'input' && node.props['aria-label'] === '客户关键词');
  keyword.props.onChange({ target: { value: 'mobile' } }); env.render();
  assert.equal(directoryCalls(env).length, 1); assert.equal(env.navigation.length, 0);
  const updated = env.all().find(node => node.type === 'form' && node.props.className === 'space-y-5'); updated.props.onSubmit({ preventDefault() {} });
  assert.equal(params(env.navigation.at(-1).href).get('q'), 'mobile');
  await env.ready(); assert.equal(directoryCalls(env).length, 2);
  assert.equal(env.all().some(node => node.type === 'form' && node.props.className === 'space-y-5'), false); env.unmount();
});

test('refresh retains current rows and shows progress while its read is pending', async () => {
  const env = await fixture().ready(); let finish;
  env.respond = call => call.endpoint.startsWith('/admin/customers/page?') ? new Promise(resolve => { finish = resolve; }) : undefined;
  env.find(node => node.type === 'button' && node.props['aria-label'] === '刷新客户列表').props.onClick(); env.render();
  assert.match(env.text(), /客户 41/); assert.match(env.text(), /正在刷新/);
  assert.equal(env.find(node => node.type === 'button' && node.props['aria-label'] === '刷新客户列表').props.disabled, true);
  finish(page([customer(42)])); await tick(); env.render();
  assert.match(env.text(), /客户 42/); assert.doesNotMatch(env.text(), /客户 41/); env.unmount();
});
