import assert from 'node:assert/strict';
import { readFileSync, existsSync } from 'node:fs';
import { createRequire } from 'node:module';
import path from 'node:path';
import test from 'node:test';
import vm from 'node:vm';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const require = createRequire(path.join(root, 'web/package.json'));
const ts = require('typescript'), React = require('react');
class ApiError extends Error { constructor(status, code, message) { super(message); Object.assign(this, { status, code }); } }
const fixedCustomer = id => ({ id, name: `客户 ${id}`, username: `customer${id}`, display_name: `客户 ${id}`, email: `c${id}@example.test`, status: 'active' });
const tick = () => new Promise(resolve => setTimeout(resolve, 1));
const componentFiles = { order: 'admin-order-create', invoice: 'manual-invoices', ticket: 'admin-tickets' };
const componentNames = { order: 'AdminOrderCreateView', invoice: 'ManualInvoiceEditorView', ticket: 'AdminTicketCreateView' };
const exactPaths = { order: '/admin/orders/create-options?kind=customers&customer_id=42&limit=1&offset=0', invoice: '/admin/invoice-customers?customer_id=42&limit=1&offset=0', ticket: '/admin/tickets/create-options?kind=customers&customer_id=42&limit=1&offset=0' };
const page = items => ({ items, total: items.length, limit: 10, offset: 0 });

// A small keyed renderer executes the actual function components and hooks. Each
// component gets its own hooks, and key changes run effect cleanup before late
// transports settle. It does not replace any create/context business logic.
function fixture(domain = 'order', query = 'customer_id=42', respond) {
  let current, rendered, nextType = 0, uuidCounter = 0;
  const instances = new Map(), types = new Map(), effects = [], cache = new Map();
  const calls = [], navigation = [], storage = new Map(), prompts = [], listeners = new Map();
  const env = { query, actor: { id: 7, role: 'admin', permissions: ['orders.manage', 'invoices.manage', 'invoices.view', 'tickets.manage'] }, calls, navigation, storage, prompts };
  const changed = (previous, next) => !previous || !next || previous.length !== next.length || previous.some((value, index) => !Object.is(value, next[index]));
  const hooks = {
    ...React,
    useState(initial) { const instance = current, index = instance.cursor++; if (!(index in instance.values)) instance.values[index] = typeof initial === 'function' ? initial() : initial; return [instance.values[index], next => { instance.values[index] = typeof next === 'function' ? next(instance.values[index]) : next; }]; },
    useRef(initial) { const index = current.cursor++; return current.values[index] ??= { current: initial }; },
    useEffect(callback, dependencies) { const instance = current, index = instance.cursor++, previous = instance.values[index]; if (changed(previous?.dependencies, dependencies)) effects.push(() => { previous?.cleanup?.(); instance.values[index] = { dependencies, cleanup: callback() }; }); },
    useMemo(factory, dependencies) { const index = current.cursor++, previous = current.values[index]; if (changed(previous?.dependencies, dependencies)) current.values[index] = { dependencies, value: factory() }; return current.values[index].value; },
    useCallback(callback, dependencies) { return hooks.useMemo(() => callback, dependencies); },
    useId() { return hooks.useMemo(() => `id-${++uuidCounter}`, []); },
  };
  const primitive = tag => props => React.createElement(tag, props, props.children);
  const Button = primitive('button'), Input = primitive('input'), Textarea = primitive('textarea'), Link = primitive('a');
  const ui = new Proxy({ Button, Input, Textarea, Dialog: props => props.open ? props.children : null }, { get: (target, name) => target[name] ??= primitive(String(name)) });
  const browser = { location: { search: `?${query}` }, confirm: message => { prompts.push(message); return true; }, setTimeout, clearTimeout, addEventListener: (name, listener) => listeners.set(`window:${name}`, listener), removeEventListener: name => listeners.delete(`window:${name}`) };
  const document = { addEventListener: (name, listener) => listeners.set(`document:${name}`, listener), removeEventListener: name => listeners.delete(`document:${name}`) };
  let permissions;
  async function api(endpoint, options = {}) {
    calls.push({ endpoint, options });
    if (respond) { const override = respond(endpoint, options, env); if (override !== undefined) return await override; }
    if (endpoint === '/me') return env.serverActor ?? env.actor;
    if (endpoint.includes('kind=customers') || endpoint.startsWith('/admin/invoice-customers')) {
      const id = new URL(endpoint, 'https://local.test').searchParams.get('customer_id');
      return page([fixedCustomer(id ? Number(id) : 42)]);
    }
    if (endpoint === '/admin/orders/create-options') return { payment_providers: [], mail_enabled: false };
    if (endpoint.includes('kind=products')) return page([{ id: 11, revision: 1, name: '测试产品', billing_cycle: 'monthly', pricing: [{ active: true, billing_cycle: 'monthly' }], option_groups: [], purchase_field_definitions: [] }]);
    if (endpoint === '/admin/tickets/create-options') return { priorities: [{ value: 'normal', label: '普通' }], mail_enabled: false };
    if (endpoint.includes('kind=departments')) return page([{ id: 3, name: '支持' }]);
    if (endpoint.includes('kind=services') || endpoint.includes('kind=assignees')) return page([]);
    if (endpoint === '/admin/orders/quote') return { quote_hash: 'quote-hash', items: [], subtotal_cents: 100, discount_cents: 0, total_cents: 100, review_status: 'pending', generate_invoice: true };
    if (endpoint === '/admin/orders') return { order_id: 99, invoice_id: 98 };
    if (endpoint === '/admin/invoices') return { invoice_id: 99, version: 1 };
    if (endpoint === '/admin/tickets') return { id: 99, ticket_no: 'T99', email_status: 'not_requested' };
    throw new Error(`Unexpected API ${endpoint}`);
  }
  const mocks = {
    react: hooks,
    'next/navigation': { useRouter: () => ({ push: href => navigation.push(href), refresh() {} }), useSearchParams: () => new URLSearchParams(env.query) },
    'next/link': { default: Link },
    'lucide-react': new Proxy({}, { get: (_, name) => primitive(`icon-${String(name)}`) }),
    '@/lib/api': { api, ApiError, authPortal: () => 'admin', money: cents => `¥${cents / 100}`, date: String, upload: async () => ({}) },
    './workspace': { Workspace: props => props.children, useAdminIdentity: () => env.actor, useAdminPermission: permission => permissions.can(env.actor, permission), PermissionGate: props => props.children },
    './common': { Failure: ({ error }) => React.createElement('p', { role: 'alert' }, error.message), Loading: () => React.createElement('p', null, 'Loading'), cycles: { monthly: '月付' } },
    './purchase-discount-details': { PurchaseDiscountDetails: () => null, PurchaseDiscountSummary: () => null },
    './product-option-choice': { ProductOptionChoice: () => null },
    './promo-customer-groups': { PromoCustomerGroups: () => null },
    './catalog-admin': { catalogSelectStyle: '' },
    './admin-pagination': { AdminPagination: () => null },
  };
  function load(relative) {
    if (cache.has(relative)) return cache.get(relative).exports;
    const filename = path.join(root, relative), module = { exports: {} }; cache.set(relative, module);
    const { outputText } = ts.transpileModule(readFileSync(filename, 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX } });
    vm.runInNewContext(outputText, {
      module, exports: module.exports, URLSearchParams, URL, Number, Error, TextEncoder, FormData, AbortController, crypto: { randomUUID: () => `00000000-0000-4000-8000-${String(++uuidCounter).padStart(12, "0")}` },
      window: browser, document, sessionStorage: { getItem: key => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, value), removeItem: key => storage.delete(key) },
      require: id => {
        if (Object.hasOwn(mocks, id)) return mocks[id];
        if (id.includes('/ui/')) return ui;
        if (id.startsWith('.') || id.startsWith('@/')) {
          const source = id.startsWith('@/') ? `web/src/${id.slice(2)}` : path.join(path.dirname(relative), id);
          for (const candidate of [`${source}.ts`, `${source}.tsx`]) if (existsSync(path.join(root, candidate))) return load(candidate);
        }
        return require(id);
      },
    }, { filename });
    return module.exports;
  }
  permissions = load('web/src/lib/admin-navigation.ts'); mocks['@/lib/admin-navigation'] = permissions;
  const Component = load(`web/src/components/${componentFiles[domain]}.tsx`)[componentNames[domain]];
  function expand(element, location, seen) {
    if (!element || typeof element !== 'object') return element;
    if (Array.isArray(element)) return element.map((child, index) => expand(child, `${location}:${index}`, seen));
    if (!element.type) return element;
    const keyed = `${location}:${element.key ?? ''}`;
    if (typeof element.type === 'function') {
      if (!types.has(element.type)) types.set(element.type, nextType++);
      const id = `${keyed}:component${types.get(element.type)}`;
      let instance = instances.get(id); if (!instance) { instance = { values: [], cursor: 0 }; instances.set(id, instance); }
      seen.add(id); const parent = current; current = instance; instance.cursor = 0;
      const result = element.type(element.props); current = parent;
      return expand(result, `${id}:result`, seen);
    }
    return { ...element, props: { ...element.props, children: expand(element.props.children, `${keyed}:children`, seen) } };
  }
  env.render = () => {
    const seen = new Set(); rendered = expand(React.createElement(Component), 'root', seen);
    for (const [id, instance] of instances) if (!seen.has(id)) { for (const value of instance.values) value?.cleanup?.(); instances.delete(id); }
    for (const effect of effects.splice(0)) effect();
    return rendered;
  };
  env.unmount = () => { for (const instance of instances.values()) for (const value of instance.values) value?.cleanup?.(); instances.clear(); };
  env.all = () => nodes(rendered);
  env.find = predicate => env.all().find(predicate);
  env.input = label => env.find(node => node.props?.['aria-label'] === label || node.props?.id === label);
  env.button = label => env.find(node => node.type === 'button' && text(node) === label);
  env.text = () => text(rendered);
  env.context = load('web/src/components/admin-customer-context.tsx');
  env.settle = async () => { for (let index = 0; index < 6; index++) { env.render(); await tick(); } return rendered; };
  return env;
}
function nodes(tree) { if (!tree || typeof tree !== 'object') return []; if (Array.isArray(tree)) return tree.flatMap(nodes); return [tree, ...nodes(tree.props?.children)]; }
function text(tree) { if (tree == null || typeof tree === 'boolean') return ''; if (typeof tree !== 'object') return String(tree); if (Array.isArray(tree)) return tree.map(text).join(''); return text(tree.props?.children); }
const event = { preventDefault() {} };
async function prepare(f, domain) {
  await f.settle();
  if (domain === 'order') {
    f.input('添加其他产品').props.onChange({ target: { value: '11' } }); f.render();
    await f.find(node => node.type === 'form').props.onSubmit(event); await f.settle();
  } else if (domain === 'invoice') {
    for (const [id, value] of [['manual-invoice-due', '2026-11-01T12:00'], ['invoice-description-1', '人工项目'], ['invoice-amount-1', '1.00']]) { f.input(id).props.onChange({ target: { value } }); f.render(); }
  } else {
    for (const [label, value] of [['处理部门', '3'], ['工单标题', '测试标题'], ['工单内容', '测试内容']]) { f.input(label).props.onChange({ target: { value } }); f.render(); }
    await f.settle();
  }
}
async function submit(f, domain) {
  if (domain === 'order') { f.button('提交订单')?.props.onClick(); f.button('使用原请求重试')?.props.onClick(); f.render(); return f.button('确定').props.onClick(); }
  return f.find(node => node.type === 'form').props.onSubmit(event);
}

test('explicit customer and return contexts are strict and preserve a safe customer tab query', () => {
  const f = fixture(), { parseAdminCustomerCreateContext: parse, safeAdminCreateReturn: safe } = f.context;
  for (const query of ['customer_id=', 'customer_id=0', 'customer_id=-1', 'customer_id=+42', 'customer_id=4.2', 'customer_id=42x', 'customer_id=42&customer_id=42', 'customer_id=9007199254740992', 'customer_id=0042']) assert(parse(query, '/admin/orders').error, query);
  const back = '/admin/customers/42?q=张三&limit=25&tab=orders&return_to=%2Fadmin%2Forders%3Fstatus%3Dunpaid';
  assert.equal(parse(`customer_id=42&return_to=${encodeURIComponent(back)}`, '/admin/orders').returnTo, back);
  for (const bad of ['https://outside.test', '//outside.test', '/admin/../login', '/admin/customers/43', '/admin/customers/42/edit', '/admin/customers/9007199254740992', '/admin/%63ustomers/42', '/admin/customers/42\\evil', '/admin/customers/42\n', '/login']) assert.equal(safe(bad, 42), null, bad);
  assert(parse('return_to=/admin/orders&return_to=/admin/invoices', '/admin/orders').error);
  f.unmount();
});
for (const domain of ['order', 'invoice', 'ticket']) {
  test(`${domain}: invalid or duplicate fixed customer stops before any business or directory request`, async () => {
    for (const query of ['customer_id=', 'customer_id=42&customer_id=43', 'customer_id=9007199254740992']) {
      const f = fixture(domain, query); try { await f.settle(); assert.match(f.text(), /客户参数无效/); assert.equal(f.calls.length, 0); assert(!f.find(node => node.type === 'form')); } finally { f.unmount(); }
    }
  });
  test(`${domain}: fixed identity uses its precise business endpoint without customers.view`, async () => {
    const f = fixture(domain); try {
      await f.settle(); assert(f.calls.some(call => call.endpoint === exactPaths[domain])); assert.match(f.text(), /客户 42/);
      assert(!f.input('客户搜索')); assert(!f.input('工单客户')); assert(!f.input('账单客户'));
      assert(!f.calls.some(call => call.endpoint.includes('/admin/customers')));
      assert(!f.calls.some(call => call.endpoint.includes('kind=customers&q=') || call.endpoint.includes('invoice-customers?q=')));
    } finally { f.unmount(); }
  });
  test(`${domain}: missing, forbidden, or empty fixed lookup never falls back to another customer`, async () => {
    for (const failure of [403, 404, 'empty', 'mismatch']) {
      const f = fixture(domain, 'customer_id=42', endpoint => { if (endpoint !== exactPaths[domain]) return; if (typeof failure === 'number') return Promise.reject(new ApiError(failure, 'not_found', '指定客户不可用')); return page(failure === 'empty' ? [] : [fixedCustomer(43)]); });
      try { await f.settle(); assert.match(f.text(), /指定客户不可/); assert(!f.find(node => node.type === 'form')); assert.equal(f.calls.length, 1); } finally { f.unmount(); }
    }
  });
  test(`${domain}: global create keeps its existing customer selector`, async () => {
    const f = fixture(domain, ''); try { await f.settle(); assert(f.calls.some(call => call.endpoint.includes('kind=customers&q=') || call.endpoint.includes('invoice-customers?q='))); assert(f.input(domain === 'invoice' ? '账单客户' : domain === 'ticket' ? '工单客户' : '客户')); } finally { f.unmount(); }
  });
  test(`${domain}: cancel returns to the original customer tab and warns about unsaved input`, async () => {
    const back = `/admin/customers/42?tab=${domain}s&q=keyword&limit=50`;
    const f = fixture(domain, `customer_id=42&return_to=${encodeURIComponent(back)}`); try { await prepare(f, domain); f.button('取消').props.onClick(); assert.equal(f.navigation.at(-1), back); assert(f.prompts.some(message => /尚未保存/.test(message))); } finally { f.unmount(); }
  });
  test(`${domain}: unknown result retains the exact customer, body and key across URL changes`, async () => {
    let attempts = 0;
    const f = fixture(domain, 'customer_id=42', (endpoint, options) => { if (options.method && endpoint === `/admin/${domain === 'order' ? 'orders' : domain === 'invoice' ? 'invoices' : 'tickets'}` && attempts++ === 0) return Promise.reject(new TypeError('transport disconnected')); });
    try {
      await prepare(f, domain); await submit(f, domain); await f.settle();
      assert.match(f.text(), /原请求|同一请求/); assert.equal(f.button('取消').props.disabled, true);
      f.query = 'customer_id=43'; await f.settle(); assert.match(f.text(), /保留原客户/); assert.match(f.text(), /客户 42/); assert(!f.calls.some(call => /customer_id=43(?:&|$)/.test(call.endpoint)));
      await submit(f, domain); await f.settle();
      const writes = f.calls.filter(call => call.options.method && call.endpoint === `/admin/${domain === 'order' ? 'orders' : domain === 'invoice' ? 'invoices' : 'tickets'}`);
      assert.equal(writes.length, 2); assert.equal(writes[0].options.body, writes[1].options.body); assert.equal(writes[0].options.headers['Idempotency-Key'], writes[1].options.headers['Idempotency-Key']);
      assert.equal(JSON.parse(writes[0].options.body)[domain === 'invoice' ? 'user_id' : 'customer_id'], 42);
      assert(f.navigation.at(-1).startsWith(`/admin/${domain === 'order' ? 'orders' : domain === 'invoice' ? 'invoices' : 'tickets'}/99`));
    } finally { f.unmount(); }
  });
  test(`${domain}: changing administrator discards the old form and ignores its late write result`, async () => {
    let resolveWrite;
    const f = fixture(domain, 'customer_id=42', (endpoint, options) => { if (options.method && endpoint === `/admin/${domain === 'order' ? 'orders' : domain === 'invoice' ? 'invoices' : 'tickets'}`) return new Promise(resolve => { resolveWrite = resolve; }); });
    try {
      await prepare(f, domain); const pending = submit(f, domain); f.render(); await tick(); assert(resolveWrite);
      f.actor = { ...f.actor, id: 8 }; f.query = 'customer_id=43'; await f.settle(); assert.match(f.text(), /客户 43/);
      const sentinel = `zjmfgo:admin-${domain === 'invoice' ? 'invoice' : domain}-draft:8:43`; f.storage.set(sentinel, 'new-admin-draft');
      resolveWrite(domain === 'order' ? { order_id: 99 } : domain === 'invoice' ? { invoice_id: 99 } : { id: 99, ticket_no: 'T99', email_status: 'not_requested' }); await pending; await f.settle();
      assert.deepEqual(f.navigation, []); assert.equal(f.storage.get(sentinel), 'new-admin-draft'); assert.match(f.text(), /客户 43/);
    } finally { f.unmount(); }
  });
}

test('fixed lookup late response cannot replace the new URL identity', async () => {
  let finish;
  const f = fixture('order', 'customer_id=42', endpoint => endpoint === exactPaths.order ? new Promise(resolve => { finish = resolve; }) : undefined);
  try { f.render(); f.query = 'customer_id=43'; await f.settle(); assert.match(f.text(), /客户 43/); finish(page([fixedCustomer(42)])); await f.settle(); assert.match(f.text(), /客户 43/); assert.doesNotMatch(f.text(), /客户 42/); } finally { f.unmount(); }
});

for (const domain of ['order', 'invoice', 'ticket']) {
  const endpoint = `/admin/${domain === 'order' ? 'orders' : domain === 'invoice' ? 'invoices' : 'tickets'}`;
  test(`${domain}: authorization/conflict after an unknown write never releases the original intent`, async () => {
    for (const [status, code] of [[401, 'unauthorized'], [403, 'permission_denied'], [404, 'not_found'], [409, 'idempotency_conflict'], [409, 'stale_revision']]) {
      let attempts = 0;
      const f = fixture(domain, 'customer_id=42', (url, options) => { if (url === endpoint && options.method) return Promise.reject(++attempts === 1 ? new TypeError('lost response') : new ApiError(status, code, '无法核对')); });
      try {
        await prepare(f, domain); await submit(f, domain); await f.settle(); await submit(f, domain); await f.settle();
        const writes = f.calls.filter(call => call.endpoint === endpoint && call.options.method);
        assert.equal(writes.length, 2); assert.equal(writes[0].options.body, writes[1].options.body); assert.deepEqual(writes[0].options.headers, writes[1].options.headers); assert.equal(f.button('取消').props.disabled, true);
        assert(f.storage.has(`zjmfgo:admin-create-intent:${domain}:7:42`)); assert.deepEqual(f.navigation, []);
      } finally { f.unmount(); }
    }
  });
  test(`${domain}: malformed success preserves the pending intent instead of navigating`, async () => {
    const f = fixture(domain, 'customer_id=42', (url, options) => url === endpoint && options.method ? {} : undefined);
    try { await prepare(f, domain); await submit(f, domain); await f.settle(); assert.deepEqual(f.navigation, []); assert.equal(f.button('取消').props.disabled, true); assert(f.storage.has(`zjmfgo:admin-create-intent:${domain}:7:42`)); } finally { f.unmount(); }
  });
  test(`${domain}: same administrator permission remount restores its pending request blocker`, async () => {
    const f = fixture(domain, 'customer_id=42', (url, options) => url === endpoint && options.method ? Promise.reject(new TypeError('lost response')) : undefined);
    try {
      await prepare(f, domain); await submit(f, domain); await f.settle(); const record = JSON.parse(f.storage.get(`zjmfgo:admin-create-intent:${domain}:7:42`));
      f.actor = { ...f.actor, ticket_scope: 'assigned' }; await f.settle();
      assert.match(f.text(), /已阻止重新创建/); assert(f.text().includes(record.key)); assert(!f.find(node => node.type === 'form'));
      assert.equal(f.calls.filter(call => call.endpoint === endpoint && call.options.method).length, 1);
      if (domain === 'ticket') { assert.equal(record.body, undefined); assert.match(f.text(), /可能含凭据/); }
    } finally { f.unmount(); }
  });
  test(`${domain}: stale portal identity blocks writes even if the provider has not refreshed`, async () => {
    const f = fixture(domain); try {
      await prepare(f, domain); f.serverActor = { ...f.actor, id: 9 }; await submit(f, domain); await f.settle();
      assert.equal(f.calls.filter(call => call.endpoint === endpoint && call.options.method).length, 0); assert.match(f.text(), /身份或权限已变化/); assert.deepEqual(f.navigation, []);
    } finally { f.unmount(); }
  });
  test(`${domain}: identity change between the write and acknowledgement preserves its marker`, async () => {
    let finish;
    const f = fixture(domain, 'customer_id=42', (url, options) => url === endpoint && options.method ? new Promise(resolve => { finish = resolve; }) : undefined);
    try {
      await prepare(f, domain); const pending = submit(f, domain); await tick(); assert(finish); f.serverActor = { ...f.actor, id: 9 };
      finish(domain === 'order' ? { order_id: 99 } : domain === 'invoice' ? { invoice_id: 99 } : { id: 99 }); await pending; await f.settle();
      assert.deepEqual(f.navigation, []); assert(f.storage.has(`zjmfgo:admin-create-intent:${domain}:7:42`)); assert.equal(f.button('取消').props.disabled, true);
    } finally { f.unmount(); }
  });
}

test('invoice refresh recovery resends only the stored original body/key and confirms the current actor', async () => {
  const first = fixture('invoice', 'customer_id=42', (url, options) => url === '/admin/invoices' && options.method ? Promise.reject(new TypeError('lost response')) : undefined);
  let second;
  try {
    await prepare(first, 'invoice'); await submit(first, 'invoice'); await first.settle(); const original = first.calls.find(call => call.endpoint === '/admin/invoices' && call.options.method);
    second = fixture('invoice'); for (const [key, value] of first.storage) second.storage.set(key, value); first.unmount();
    await second.settle(); assert(!second.find(node => node.type === 'form')); assert.match(second.text(), /未确认的创建请求/);
    await second.button('使用原请求核对').props.onClick(); await second.settle();
    const retried = second.calls.find(call => call.endpoint === '/admin/invoices' && call.options.method); assert.equal(retried.options.body, original.options.body); assert.equal(retried.options.headers["Idempotency-Key"], original.options.headers["Idempotency-Key"]); assert.equal(second.navigation.at(-1), '/admin/invoices/99');
    assert.equal(second.calls.filter(call => call.endpoint === '/me').length, 2); assert(!second.storage.has('zjmfgo:admin-create-intent:invoice:7:42'));
  } finally { first.unmount(); second?.unmount(); }
});

test('sensitive order fields remain out of session storage and restore only a blocker', async () => {
  const f = fixture('order', 'customer_id=42', (url, options) => {
    if (url.includes('kind=products')) return page([{ id: 11, revision: 1, name: '敏感配置产品', billing_cycle: 'monthly', pricing: [{ active: true, billing_cycle: 'monthly' }], option_groups: [], purchase_field_definitions: [{ key: 'password', label: '服务器密码', type: 'text', encrypted: true, active: true, scope: 'purchase', max_length: 255 }] }]);
    if (url === '/admin/orders' && options.method) return Promise.reject(new TypeError('lost response'));
  });
  let next;
  try {
    await f.settle(); f.input('添加其他产品').props.onChange({ target: { value: '11' } }); f.render(); f.input('服务器密码').props.onChange({ target: { value: 'secret-purchase-value' } }); f.render();
    await f.find(node => node.type === 'form').props.onSubmit(event); await f.settle(); await submit(f, 'order'); await f.settle();
    assert(f.calls.find(call => call.endpoint === '/admin/orders').options.body.includes('secret-purchase-value'));
    for (const value of f.storage.values()) assert(!value.includes('secret-purchase-value'));
    const marker = JSON.parse(f.storage.get('zjmfgo:admin-create-intent:order:7:42')); assert.equal(marker.sensitive, true); assert.equal(marker.body, undefined);
    next = fixture('order'); for (const [key, value] of f.storage) next.storage.set(key, value); f.unmount(); await next.settle();
    assert.match(next.text(), /原内容可能含凭据/); assert(!next.find(node => node.type === 'form')); assert(!next.button('使用原请求核对')); assert(next.text().includes(marker.key));
  } finally { f.unmount(); next?.unmount(); }
});

test('stored recovery rejects malformed keys, bodies, customers, returns and sensitive bodies before POST', async () => {
  const valid = { key: 'manual-save-00000000-0000-4000-8000-000000000001', body: JSON.stringify({ user_id: 42, due_at: '2026-11-01T00:00:00Z', expected_version: 0, items: [{ description: '项目', amount_cents: 100 }] }), customerId: 42, query: 'customer_id=42', returnTo: '/admin/customers/42', sensitive: false, createdAt: '2026-10-21T12:00:00Z' };
  for (const value of [[], {}, { ...valid, key: 'other-key' }, { ...valid, body: 'null' }, { ...valid, body: JSON.stringify({ user_id: 43 }) }, { ...valid, customerId: 43 }, { ...valid, query: 'customer_id=' }, { ...valid, returnTo: '//evil.test' }, { ...valid, sensitive: true }]) {
    const f = fixture('invoice'); try { f.storage.set('zjmfgo:admin-create-intent:invoice:7:42', JSON.stringify(value)); await f.settle(); assert.match(f.text(), /记录损坏或客户不匹配/); assert(!f.button('使用原请求核对')); assert(!f.find(node => node.type === 'form')); assert(!f.calls.some(call => call.options.method)); } finally { f.unmount(); }
  }
});

for (const domain of ['order', 'invoice', 'ticket']) {
  const endpoint = `/admin/${domain === 'order' ? 'orders' : domain === 'invoice' ? 'invoices' : 'tickets'}`;
  test(`${domain}: successful business POST followed by a rejected identity check keeps the exact intent`, async () => {
    for (const status of [400, 401, 403, 409]) {
      let written = false;
      const f = fixture(domain, 'customer_id=42', (url, options) => {
        if (url === endpoint && options.method) written = true;
        if (url === '/me' && written) return Promise.reject(new ApiError(status, 'identity_rejected', '身份核验拒绝'));
      });
      try {
        await prepare(f, domain); await submit(f, domain); await f.settle(); assert(written);
        assert.deepEqual(f.navigation, []); assert.equal(f.button('取消').props.disabled, true);
        const original = f.calls.find(call => call.endpoint === endpoint && call.options.method);
        const marker = JSON.parse(f.storage.get(`zjmfgo:admin-create-intent:${domain}:7:42`));
        assert.equal(marker.key, original.options.headers['Idempotency-Key']); if (marker.body) assert.equal(marker.body, original.options.body);
      } finally { f.unmount(); }
    }
  });
  test(`${domain}: the first business POST authorization rejection remains unresolved conservatively`, async () => {
    for (const status of [401, 403, 404]) {
      const f = fixture(domain, 'customer_id=42', (url, options) => url === endpoint && options.method ? Promise.reject(new ApiError(status, 'unauthorized', '无法核验请求')) : undefined);
      try {
        await prepare(f, domain); await submit(f, domain); await f.settle();
        assert.deepEqual(f.navigation, []); assert.equal(f.button('取消').props.disabled, true); assert(f.storage.has(`zjmfgo:admin-create-intent:${domain}:7:42`));
      } finally { f.unmount(); }
    }
  });
}

test('ticket draft preserves only title/priority and removes legacy plaintext bodies before restoring', async () => {
  const f = fixture('ticket');
  const key = 'zjmfgo:admin-ticket-draft:7:42';
  try {
    f.storage.set(key, JSON.stringify({ subject: '旧工单标题', body: 'legacy-private-password', priority: 'high' }));
    await f.settle(); assert(!f.storage.get(key).includes('legacy-private-password'));
    f.button('恢复草稿').props.onClick(); f.render(); assert.equal(f.input('工单标题').props.value, '旧工单标题'); assert.equal(f.input('工单内容').props.value, '');
    f.input('工单内容').props.onChange({ target: { value: 'new-private-password' } }); f.render();
    f.input('工单标题').props.onChange({ target: { value: '新的标题' } }); await f.settle();
    const draft = JSON.parse(f.storage.get(key)); assert.equal(draft.subject, '新的标题'); assert.equal(draft.priority, 'high'); assert.equal(draft.body, undefined);
    for (const value of f.storage.values()) { assert(!value.includes('legacy-private-password')); assert(!value.includes('new-private-password')); }
  } finally { f.unmount(); }
});
