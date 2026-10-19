import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
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
    module, exports: module.exports, URLSearchParams, Number, Error,
    process: { env: { API_INTERNAL_URL: 'http://api.example.test' } }, ...globals,
    require: id => Object.hasOwn(mocks, id) ? mocks[id] : id.includes('/ui/') ? ui : require(id),
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

test('server session guard separates customer and staff portals without blocking staff account settings', async () => {
  for (const role of ['customer', 'admin', 'super_admin', 'unexpected']) for (const admin of [false, true]) {
    const redirect = Symbol('redirect');
    let destination;
    const { requireUser } = load('web/src/lib/session.ts', {
      'next/headers': { cookies: async () => ({ get: () => ({ value: 'fixture' }) }) },
      'next/navigation': { redirect: href => { destination = href; throw redirect; } },
    }, { fetch: async () => Response.json({ data: user(role) }) });
    const allowed = admin ? ['admin', 'super_admin'].includes(role) : role === 'customer';
    if (allowed) assert.equal((await requireUser(admin)).role, role);
    else if (role === 'unexpected' && !admin) await assert.rejects(requireUser(admin), /无法访问客户中心/);
    else {
      await assert.rejects(requireUser(admin), error => error === redirect);
      assert.equal(destination, admin ? '/' : '/admin');
    }
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

function catalog(identity, latest) {
  const calls = [], destinations = [];
  const product = { id: 3, name: 'Cloud', description: '', billing_cycle: 'monthly', price_cents: 1000, setup_fee_cents: 0, stock: 10, option_groups: [] };
  const mocks = {
    react,
    'next/navigation': { useRouter: () => ({ push: href => destinations.push(href) }) },
    'next/link': { default: Link },
    '@/hooks/use-resource': { useResource: endpoint => ({ data: endpoint === '/me' ? identity : null, error: null, refresh() {} }) },
    '@/lib/api': { ApiError, money: String, api: async endpoint => { calls.push(endpoint); if (endpoint === '/me') { if (latest instanceof Error) throw latest; return latest; } if (endpoint === '/cart') return { version: 1 }; return {}; } },
    './workspace': { Workspace: primitive('main') },
    './common': { cycles: {}, Failure: () => null, Loading: () => null },
    './purchase-fields': { PurchaseFieldInputs: () => null, purchaseFieldValues: () => ({}) },
    './product-option-choice': { ProductOptionChoice: () => null },
    '@/lib/product-options': { resolveProductOptions: () => ({ ids: [], branches: [], complete: true }), configurationRuleError: () => null },
  };
  const { CatalogView } = load('web/src/components/catalog-view.tsx', mocks);
  const tree = CatalogView({ initialProduct: product });
  return { tree, calls, destinations, buy: nodes(tree).find(item => item.type === Button && item.props.onClick && /加入购物车|管理员账号不可购买/.test(text(item))) };
}
test('staff public product preview removes cart entry and disables buying for both administrator roles', async () => {
  for (const role of ['admin', 'super_admin']) {
    const view = catalog(user(role), user(role));
    assert.equal(view.buy.props.disabled, true);
    assert.equal(nodes(view.tree).some(node => node.type === Link && node.props.href === '/cart'), false);
    assert.match(text(view.tree), /独立客户账号/);
    // Even a stale DOM handler cannot start a cart read or write as staff.
    await view.buy.props.onClick();
    assert.deepEqual(view.calls, ['/me']);
  }
});
test('catalog rechecks current identity before cart writes while retaining the customer and anonymous flows', async () => {
  const stale = catalog(user('customer'), user('admin'));
  await stale.buy.props.onClick(); assert.deepEqual(stale.calls, ['/me']);
  const customer = catalog(user('customer'), user('customer'));
  await customer.buy.props.onClick(); assert.deepEqual(customer.calls, ['/me', '/cart', '/cart/items']);
  const anonymous = catalog(null, new ApiError(401, 'unauthorized', '请先登录'));
  await anonymous.buy.props.onClick(); assert.deepEqual(anonymous.calls, ['/me']); assert.deepEqual(anonymous.destinations, ['/login']);
});
test('public workspace suppresses staff customer navigation and customer-only resource requests', () => {
  for (const role of ['admin', 'super_admin', 'customer']) {
    const calls = [], groups = [];
    const { Workspace } = load('web/src/components/workspace.tsx', {
      react,
      'next/link': { default: Link },
      'next/navigation': { usePathname: () => '/', useRouter: () => ({ replace() {} }) },
      '@/hooks/use-resource': { useResource: endpoint => { calls.push(endpoint); return { data: endpoint === '/me' ? user(role) : null, error: null, refresh() {} }; } },
      '@/lib/api': { api() {} },
      '@/lib/admin-navigation': load('web/src/lib/admin-navigation.ts'),
      '@/hooks/use-service-navigation': { useServiceNavigation: enabled => { groups.push(enabled); return {}; } },
      '@/lib/service-icons': { serviceIcon: () => () => null },
      './site-provider': { SiteBrand: () => null, SiteFooter: () => null },
      './admin-shell': { AdminShell: primitive('main') },
    });
    const tree = Workspace({ title: '目录', children: 'content' });
    const customer = role === 'customer';
    assert.equal(nodes(tree).some(node => node.type === Link && node.props.href === '/cart'), customer);
    assert.equal(nodes(tree).some(node => node.type === Link && node.props.href === '/console/orders'), customer);
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
test('public content header routes the current account to its own portal', () => {
  for (const [role, destination, label] of [['admin', '/admin', '返回管理后台'], ['super_admin', '/admin', '返回管理后台'], ['customer', '/console/orders', '客户中心'], [null, '/login', '登录']]) {
    const { PublicShell } = load('web/src/components/public-content.tsx', {
      react,
      'next/link': { default: Link },
      '@/lib/api': { ApiError },
      '@/hooks/use-resource': { useResource: () => ({ data: role ? user(role) : null }) },
      './site-provider': { useSite: () => ({ site: {} }), SiteBrand: () => null, SiteFooter: () => null },
    });
    const tree = PublicShell({ title: '新闻', children: 'content' });
    assert(nodes(tree).some(node => node.type === Link && node.props.href === destination && text(node) === label));
  }
});
