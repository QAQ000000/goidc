import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import path from 'node:path';
import test from 'node:test';
import vm from 'node:vm';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const require = createRequire(path.join(root, 'web/package.json'));
const ts = require('typescript');
const React = require('react');
const primitive = tag => props => React.createElement(tag, props, props.children);
const Button = primitive('button');
const Link = primitive('a');
const ui = new Proxy({}, { get: (_, name) => name === 'Button' ? Button : primitive(String(name)) });
function load(relative, mocks = {}, globals = {}) {
  const filename = path.join(root, relative);
  const { outputText } = ts.transpileModule(readFileSync(filename, 'utf8'), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX },
  });
  const module = { exports: {} };
  vm.runInNewContext(outputText, {
    module, exports: module.exports, URLSearchParams, URL, Number, Error, Headers, Response, Request, Blob, FormData, AbortController, AbortSignal, Event, CustomEvent,
    process: { env: { API_INTERNAL_URL: 'http://api.example.test' } }, ...globals,
    require: id => {
      if (Object.hasOwn(mocks, id)) return mocks[id];
      if (id.includes('/ui/')) return ui;
      if (id.startsWith('.')) {
        const relativeSource = path.join(path.dirname(relative), id);
        for (const candidate of [`${relativeSource}.ts`, `${relativeSource}.tsx`]) if (existsSync(path.join(root, candidate))) return load(candidate, mocks, globals);
      }
      return require(id);
    },
  }, { filename });
  return module.exports;
}
const react = { ...React, useState: initial => [typeof initial === 'function' ? initial() : initial, () => {}], useEffect() {}, useContext: context => context._currentValue };
function nodes(tree) {
  if (!tree || typeof tree !== 'object') return [];
  if (Array.isArray(tree)) return tree.flatMap(nodes);
  return [tree, ...React.Children.toArray(tree.props?.children).flatMap(nodes)];
}
function text(tree) {
  if (typeof tree === 'string' || typeof tree === 'number') return String(tree);
  if (!tree) return '';
  if (Array.isArray(tree)) return tree.map(text).join('');
  return React.Children.toArray(tree.props?.children).map(text).join('');
}
class ApiError extends Error { constructor(status, code, message) { super(message); Object.assign(this, { status, code }); } }
const user = role => ({ id: 7, display_name: 'Fixture', role, permissions: [] });

test('server session guard reads only the selected portal cookie and forwards the matching header', async () => {
  for (const admin of [false, true]) for (const role of admin ? ['admin', 'super_admin'] : ['customer']) {
    const redirect = Symbol('redirect');
    const lookups = [], calls = [];
    const cookieName = admin ? 'zjmfgo_admin_session' : 'zjmfgo_session';
    const { requireUser } = load('web/src/lib/session.ts', {
      'next/headers': { cookies: async () => ({ get: name => { lookups.push(name); return { value: name === cookieName ? 'own-session' : 'other-session' }; } }) },
      'next/navigation': { redirect: () => { throw redirect; } },
    }, { fetch: async (url, options) => { calls.push({ url, options }); return Response.json({ data: user(role) }); } });
    assert.equal((await requireUser(admin)).role, role);
    assert.deepEqual(lookups, [cookieName]); assert.equal(calls.length, 1);
    const headers = new Headers(calls[0].options.headers);
    assert.equal(headers.get('Cookie'), `${cookieName}=own-session`);
    assert.equal(headers.get('X-Auth-Portal'), admin ? 'admin' : 'customer');
    assert.equal(calls[0].options.cache, 'no-store');
  }
});
test('server session guard never borrows the other portal cookie or redirects across portals', async () => {
  for (const admin of [false, true]) for (const failure of ['absent', 'expired', 'wrong-role']) {
    const redirect = Symbol('redirect'); let destination, reads = 0;
    const cookieName = admin ? 'zjmfgo_admin_session' : 'zjmfgo_session';
    const { requireUser } = load('web/src/lib/session.ts', {
      'next/headers': { cookies: async () => ({ get: name => failure === 'absent' && name === cookieName ? undefined : { value: `${name}-fixture` } }) },
      'next/navigation': { redirect: href => { destination = href; throw redirect; } },
    }, { fetch: async () => { reads++; return Response.json({ data: user(admin ? 'customer' : 'admin') }, { status: failure === 'expired' ? 401 : 200 }); } });
    await assert.rejects(requireUser(admin), error => error === redirect);
    assert.equal(destination, admin ? '/admin-login' : '/login');
    assert.equal(reads, failure === 'absent' ? 0 : 1);
  }
});

test('administrators without business grants stay in the admin portal and can find personal settings', () => {
  const navigation = load('web/src/lib/admin-navigation.ts');
  for (const role of ['admin', 'super_admin']) {
    assert.equal(navigation.can(user(role), '@account'), true);
    assert(navigation.adminEntry(user(role)).startsWith('/admin'));
    assert.equal(navigation.adminNavigationItem('/admin/account').permission, '@account');
  }
  assert.equal(navigation.adminEntry(user('admin')), '/admin/account');
  assert.equal(navigation.can(user('customer'), '@account'), false);
});
test('admin shell keeps its personal account link and navigation for staff without business permissions', () => {
  const navigation = load('web/src/lib/admin-navigation.ts');
  const { AdminShell } = load('web/src/components/admin-shell.tsx', {
    react,
    'next/link': { default: Link },
    'next/navigation': { usePathname: () => '/admin/account' },
    '@/lib/admin-navigation': navigation,
    './site-provider': { SiteBrand: () => null },
  });
  const tree = AdminShell({ title: '个人账户', user: user('admin'), error: null, refresh() {}, logout: async () => {}, logoutError: '', children: 'security' });
  assert(nodes(tree).some(node => node.type === Link && node.props.href === '/admin/account'));
  const sidebar = nodes(tree).find(node => typeof node.type === 'function' && node.type.name === 'Navigation');
  assert(sidebar);
  const links = nodes(sidebar.type(sidebar.props)).filter(node => node.type === Link);
  assert.deepEqual(links.map(node => node.props.href), ['/admin/account']);
});

// Catalog and purchase-role behavior is exercised with the actual stateful
// ProductPurchaseForm in product-purchase-components.test.mjs.

test('public workspace treats an admin-only browser as anonymous and uses only customer identity', () => {
  for (const role of [null, 'customer']) {
    const calls = [], groups = [];
    const { Workspace } = load('web/src/components/workspace.tsx', {
      react,
      'next/link': { default: Link },
      'next/navigation': { usePathname: () => '/', useRouter: () => ({ replace() {} }) },
      '@/hooks/use-resource': { useResource: endpoint => { calls.push(endpoint); return { data: endpoint === '/me' && role ? user(role) : null, error: endpoint === '/me' && !role ? new ApiError(401, 'unauthorized', '请登录客户账号') : null, refresh() {} }; } },
      '@/lib/api': { api() {} },
      '@/lib/admin-navigation': load('web/src/lib/admin-navigation.ts'),
      '@/hooks/use-service-navigation': { useServiceNavigation: enabled => { groups.push(enabled); return {}; } },
      '@/lib/service-icons': { serviceIcon: () => () => null },
      './site-provider': { SiteBrand: () => null, SiteFooter: () => null },
      './admin-shell': { AdminShell: primitive('main') },
    });
    const tree = Workspace({ title: '目录', children: 'content' });
    const customer = role === 'customer';
    assert.equal(nodes(tree).some(node => node.type === Link && node.props.href === '/admin' && /返回管理后台/.test(text(node))), false);
    assert.equal(text(tree).includes('管理员账号仅用于后台管理'), false);
    assert.equal(nodes(tree).some(node => node.type === Link && node.props.href === '/login'), !customer);
    assert.equal(calls.includes('/account/certification'), customer);
    assert.deepEqual(groups, [customer]);
  }
});
test('admin personal account page mounts shared security without customer balance components', async () => {
  const AccountSecurity = () => null;
  const Workspace = primitive('main');
  const { AdminAccountView } = load('web/src/components/admin-account.tsx', {
    './account-security': { AccountSecurity }, './resource-page': { PageHeading: () => null }, './workspace': { Workspace },
  });
  const tree = AdminAccountView();
  assert.equal(tree.type, Workspace); assert.equal(tree.props.admin, true);
  assert.equal(nodes(tree).filter(node => node.type === AccountSecurity).length, 1);
  const calls = [];
  const Page = load('web/src/app/admin/account/page.tsx', { '@/lib/session': { requireUser: async admin => { calls.push(admin); return user('admin'); } }, '@/components/admin-account': { AdminAccountView } }).default;
  assert.equal((await Page()).type, AdminAccountView); assert.deepEqual(calls, [true]);
});
test('shared personal security mounts its own key data without touching customer finances', () => {
  const paths = [];
  const { AccountSecurity } = load('web/src/components/account-security.tsx', {
    react,
    '@/lib/api': { date: String, api() { throw new Error('render must not write'); } },
    '@/hooks/use-resource': { useResource: endpoint => { paths.push(endpoint); return { data: [], error: null, refresh() {} }; } },
    './common': { Loading: () => null, Status: () => null },
    './mfa-panel': { MfaPanel: () => null },
    './ticket-notification-preferences': { TicketNotificationPreferences: () => null },
    './notification-preferences': { NotificationInbox: () => null, NotificationPreferences: () => null },
  });
  for (const child of nodes(AccountSecurity())) if (typeof child.type === 'function') child.type(child.props);
  assert.deepEqual(paths, ['/api-keys']);
});
test('public content header exposes customer login independently of any administrator session', () => {
  for (const [role, destination, label] of [['customer', '/console/orders', '客户中心'], [null, '/login', '登录']]) {
    const { PublicShell } = load('web/src/components/public-content.tsx', {
      react,
      'next/link': { default: Link },
      '@/lib/api': { ApiError },
      '@/hooks/use-resource': { useResource: () => ({ data: role ? user(role) : null }) },
      './site-provider': { useSite: () => ({ site: {} }), SiteBrand: () => null, SiteFooter: () => null },
    });
    const tree = PublicShell({ title: '新闻', children: 'content' });
    assert(nodes(tree).some(node => node.type === Link && node.props.href === destination && text(node) === label));
    assert.equal(text(tree).includes('返回管理后台'), false);
  }
});

test('client maintenance guard hides public content without inspecting administrator identity and preserves recovery pages', () => {
  const destinations = [], child = React.createElement('main', null, 'customer-business-content');
  for (const maintenance of [false, true]) for (const pathname of ['/', '/products/42', '/news', '/knowledgebase', '/downloads', '/console/account', '/login', '/register', '/administrator', '/admin', '/admin/account', '/admin-login', '/maintenance', '/forgot-password', '/reset-password']) {
    const identityReads = [];
    const { SiteAvailability, MaintenanceScreen } = load('web/src/components/site-availability.tsx', {
      react,
      'next/link': { default: Link },
      'next/navigation': { usePathname: () => pathname, useRouter: () => ({ replace: destination => destinations.push(destination), refresh() {} }) },
      '@/hooks/use-resource': { useResource: path => { identityReads.push(path); throw new Error('maintenance must not probe an administrator session'); } },
      '@/hooks/use-unauthorized-redirect': { useUnauthorizedRedirect() {} },
      './site-provider': { useSite: () => ({ site: { maintenance_enabled: maintenance }, refresh() {} }), SiteBrand: () => null, SiteFooter: () => null },
    });
    const exempt = pathname === '/admin' || pathname.startsWith('/admin/') || ['/admin-login', '/maintenance', '/forgot-password', '/reset-password'].includes(pathname);
    const tree = SiteAvailability({ children: child });
    assert.equal(tree === child, !maintenance || exempt, pathname);
    if (maintenance && !exempt) assert.equal(tree.type, MaintenanceScreen, pathname);
    assert.deepEqual(identityReads, []);
  }
  assert.deepEqual(destinations, []);
});

function clientAPI(pathname, response = () => Response.json({ data: { ok: true } })) {
  const calls = [], events = [], saves = [];
  const browser = { location: { pathname }, dispatchEvent: event => { events.push(event); return true; } };
  const helpers = load('web/src/lib/api.ts', {
    './download': { downloadFilename: (_header, fallback) => fallback, saveBlob: (blob, filename) => saves.push({ blob, filename }) },
  }, { window: browser, fetch: async (url, options) => { calls.push({ url, options }); return response(url, options); } });
  return { helpers, calls, events, saves, browser };
}
test('real API helper selects customer on public pages and administrator only for admin paths or explicit scope', async () => {
  for (const [page, endpoint, portal] of [
    ['/', '/me', 'customer'], ['/products/42', '/me', 'customer'], ['/login', '/auth/login', 'customer'],
    ['/console/account', '/account/mfa', 'customer'], ['/admin', '/me', 'admin'], ['/admin/account', '/account/password', 'admin'],
    ['/admin-login', '/auth/login', 'admin'], ['/', '/admin/customers', 'admin'], ['/administrator', '/me', 'customer'],
  ]) {
    const { helpers, calls } = clientAPI(page);
    await helpers.api(endpoint);
    assert.equal(calls.length, 1); const call = calls[0];
    assert.equal(call.url, `/api/v1${endpoint}`);
    assert.equal(new Headers(call.options.headers).get('X-Auth-Portal'), portal, `${page} ${endpoint}`);
    assert.equal(call.options.credentials, 'same-origin'); assert.equal(call.options.cache, 'no-store');
    assert.equal(call.options.portal, undefined, 'custom portal selector must not leak into fetch RequestInit');
  }
  const explicit = clientAPI('/admin/account');
  await explicit.helpers.api('/me', { portal: 'customer', headers: new Headers({ 'X-Fixture': 'preserved' }) });
  const headers = new Headers(explicit.calls[0].options.headers);
  assert.equal(headers.get('X-Auth-Portal'), 'customer'); assert.equal(headers.get('X-Fixture'), 'preserved');
  assert.equal(explicit.calls[0].options.portal, undefined);
  await explicit.helpers.api('/admin/customers', { portal: 'customer' });
  assert.equal(new Headers(explicit.calls[1].options.headers).get('X-Auth-Portal'), 'admin', 'fixed admin APIs always select the admin session');
});
test('uploads and business downloads use the same portal selection as ordinary JSON requests', async () => {
  for (const page of ['/console/account', '/admin/account']) {
    const portal = page.startsWith('/admin') ? 'admin' : 'customer';
    const { helpers, calls, saves } = clientAPI(page, url => url.endsWith('/upload') ? Response.json({ data: { ok: true } }) : new Response('business-file', { headers: { 'Content-Disposition': 'attachment; filename="file.txt"' } }));
    const body = new FormData(); body.set('file', new Blob(['fixture']), 'fixture.txt');
    await helpers.upload('/shared/upload', body);
    assert.equal(await (await helpers.download('/shared/document')).text(), 'business-file');
    await helpers.fetchDownload({ id: 3, filename: 'file.txt' }, '/shared/download');
    assert.equal(calls.length, 3); assert.equal(saves.length, 1);
    for (const call of calls) {
      assert.equal(new Headers(call.options.headers).get('X-Auth-Portal'), portal);
      assert.equal(call.options.credentials, 'same-origin'); assert.equal(call.options.cache, 'no-store');
    }
    assert.equal(new Headers(calls[0].options.headers).has('Content-Type'), false, 'browser must add multipart boundary');
  }
});
test('credential failures do not impersonate session expiry and each portal reports its own unauthorized event', async () => {
  for (const page of ['/console/account', '/admin/account']) {
    const { helpers, events } = clientAPI(page, () => Response.json({ error: { code: 'unauthorized', message: '会话已过期' } }, { status: 401 }));
    await assert.rejects(helpers.api('/me'), error => error.status === 401);
    assert.equal(events.length, 1); assert.equal(events[0].type, 'zjmfgo:unauthorized');
    assert.equal(events[0].detail.portal, page.startsWith('/admin') ? 'admin' : 'customer');
    for (const endpoint of ['/auth/login', '/auth/login/mfa', '/auth/register']) await assert.rejects(helpers.api(endpoint), error => error.status === 401);
    assert.equal(events.length, 1, 'wrong credentials must not dispatch global logout events');
  }
});

function statefulHook(loadHook) {
  let cursor = 0; const values = [], effects = [];
  const changed = (before, next) => !before || before.length !== next.length || before.some((value, index) => value !== next[index]);
  const hooks = {
    ...React,
    useState(initial) {
      const index = cursor++;
      if (!(index in values)) values[index] = typeof initial === 'function' ? initial() : initial;
      return [values[index], next => { values[index] = typeof next === 'function' ? next(values[index]) : next; }];
    },
    useEffect(callback, dependencies) {
      const index = cursor++, previous = values[index];
      if (changed(previous?.dependencies, dependencies)) effects.push(() => { previous?.cleanup?.(); values[index] = { dependencies, cleanup: callback() }; });
    },
    useCallback(callback, dependencies) {
      const index = cursor++, previous = values[index];
      if (changed(previous?.dependencies, dependencies)) values[index] = { dependencies, callback };
      return values[index].callback;
    },
  };
  const hook = loadHook(hooks);
  return {
    render(...args) { cursor = 0; const result = hook(...args); for (const effect of effects.splice(0)) effect(); return result; },
    unmount() { for (const value of values) value?.cleanup?.(); },
  };
}
function resourceHook(helpers) {
  return statefulHook(hooks => {
    const { useResource } = load('web/src/hooks/use-resource.ts', { react: hooks, '@/lib/api': helpers });
    return portal => useResource('/me', false, portal);
  });
}
const flushResources = () => new Promise(resolve => setImmediate(resolve));
test('real resource hook hides previous administrator data immediately when its portal changes', async () => {
  const replies = [];
  const client = clientAPI('/admin/account', (_url, options) => new Promise(resolve => replies.push({ resolve, options })));
  const view = resourceHook(client.helpers);
  try {
    assert.equal(view.render('admin').data, null);
    replies[0].resolve(Response.json({ data: user('admin') })); await flushResources();
    assert.equal(view.render('admin').data.role, 'admin');
    assert.equal(view.render('customer').data, null, 'shared /me must never reuse another portal identity');
    assert.equal(new Headers(replies[1].options.headers).get('X-Auth-Portal'), 'customer');
    replies[1].resolve(Response.json({ data: user('customer') })); await flushResources();
    assert.equal(view.render('customer').data.role, 'customer');
  } finally { view.unmount(); }
});
test('real resource hook aborts the old portal request and ignores its late response', async () => {
  const replies = [];
  const client = clientAPI('/admin/account', (_url, options) => new Promise(resolve => replies.push({ resolve, options })));
  const view = resourceHook(client.helpers);
  try {
    view.render('admin'); view.render('customer');
    assert.equal(replies[0].options.signal.aborted, true);
    replies[0].resolve(Response.json({ data: user('admin') })); await flushResources();
    assert.equal(view.render('customer').data, null);
    replies[1].resolve(Response.json({ data: user('customer') })); await flushResources();
    assert.equal(view.render('customer').data.role, 'customer');
  } finally { view.unmount(); }
});
test('customer credit keeps its snapshot after administrator expiry and pins all asynchronous reads to the customer portal', async () => {
  const replies = [], browser = new EventTarget();
  const client = clientAPI('/console/credit', (_url, options) => new Promise(resolve => replies.push({ resolve, options })));
  const view = statefulHook(hooks => load('web/src/components/customer-credit.tsx', {
    react: hooks, '@/lib/api': client.helpers,
    'next/link': { default: Link }, 'next/navigation': { useRouter() {}, useSearchParams() {} },
    './workspace': { Workspace: primitive('main') }, './common': { Failure: primitive('div'), Loading: primitive('div') },
    './admin-pagination': { AdminPagination: primitive('nav') },
  }, { window: browser }).useCustomerCreditResource);
  try {
    assert.equal(view.render('/credit/account').data, null);
    client.browser.location.pathname = '/admin/account';
    replies[0].resolve(Response.json({ data: user('customer') })); await flushResources();
    const snapshot = { available_cents: 12000, used_cents: 8000 };
    replies[1].resolve(Response.json({ data: snapshot })); await flushResources();
    replies[2].resolve(Response.json({ data: user('customer') })); await flushResources();
    assert.equal(client.calls.length, 3);
    assert.deepEqual(client.calls.map(call => call.url), ['/api/v1/me', '/api/v1/credit/account', '/api/v1/me']);
    for (const call of client.calls) assert.equal(new Headers(call.options.headers).get('X-Auth-Portal'), 'customer');
    assert.deepEqual(view.render('/credit/account').data, snapshot);

    browser.dispatchEvent(new CustomEvent('zjmfgo:unauthorized', { detail: { portal: 'admin', path: '/me' } }));
    const retained = view.render('/credit/account');
    assert.deepEqual(retained.data, snapshot);
    assert.equal(retained.error, null);
    assert.equal(client.calls.length, 3, 'administrator expiry must not reload customer credit');

    browser.dispatchEvent(new CustomEvent('zjmfgo:unauthorized', { detail: { portal: 'customer', path: '/me' } }));
    const revoked = view.render('/credit/account');
    assert.equal(revoked.data, null);
    assert.match(revoked.error.message, /登录已失效/);
    assert.equal(client.calls.length, 3, 'customer expiry must not request more credit data');
  } finally { view.unmount(); }
});
test('unauthorized redirects ignore failures from the other portal and redirect only once for their own session', () => {
  for (const [pathname, portal, destination] of [['/admin/account', 'admin', '/admin-login'], ['/console/account', 'customer', '/login'], ['/console', 'customer', '/login'], ['/cart', 'customer', '/login'], ['/products/42', 'customer', null]]) {
    const browser = new EventTarget(), destinations = [], cleanups = [];
    const { helpers } = clientAPI(pathname);
    const { useUnauthorizedRedirect } = load('web/src/hooks/use-unauthorized-redirect.ts', {
      react: { ...react, useRef: value => ({ current: value }), useEffect: callback => { const cleanup = callback(); if (cleanup) cleanups.push(cleanup); } },
      'next/navigation': { usePathname: () => pathname, useRouter: () => ({ replace: href => destinations.push(href) }) },
      '@/lib/api': helpers,
    }, { window: browser });
    useUnauthorizedRedirect();
    browser.dispatchEvent(new CustomEvent('zjmfgo:unauthorized', { detail: { portal: portal === 'admin' ? 'customer' : 'admin', path: '/me' } }));
    assert.deepEqual(destinations, [], `${pathname} borrowed the other portal's expiry`);
    for (let count = 0; count < 2; count++) browser.dispatchEvent(new CustomEvent('zjmfgo:unauthorized', { detail: { portal, path: '/me' } }));
    assert.deepEqual(destinations, destination ? [destination] : []);
    for (const cleanup of cleanups) cleanup();
    browser.dispatchEvent(new CustomEvent('zjmfgo:unauthorized', { detail: { portal, path: '/me' } }));
    assert.deepEqual(destinations, destination ? [destination] : [], 'unmounted listener must be removed');
  }
});
