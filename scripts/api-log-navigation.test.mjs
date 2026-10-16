import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const require = createRequire(path.join(root, 'web/package.json'));
const ts = require('typescript');
const React = require('react');
const { renderToStaticMarkup } = require('react-dom/server');

// Execute the checked-in modules. Only framework I/O and visual primitives are
// replaced: permission decisions, page control flow and log tabs stay real.
function load(relative, mocks = {}) {
  const filename = path.join(root, relative);
  const { outputText } = ts.transpileModule(readFileSync(filename, 'utf8'), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX },
  });
  const module = { exports: {} };
  vm.runInNewContext(outputText, {
    module, exports: module.exports,
    require: id => Object.hasOwn(mocks, id) ? mocks[id] : require(id),
    URLSearchParams,
  }, { filename });
  return module.exports;
}

const navigation = load('web/src/lib/admin-navigation.ts');
const users = [
  { name: 'audit-only', role: 'admin', permissions: ['audit.view'], api: true, notifications: false },
  { name: 'notification-only', role: 'admin', permissions: ['notifications.view'], api: false, notifications: true },
  { name: 'super', role: 'super_admin', permissions: [], api: true, notifications: true },
  { name: 'unprivileged admin', role: 'admin', permissions: [], api: false, notifications: false },
  { name: 'customer with irrelevant grants', role: 'customer', permissions: ['audit.view', 'notifications.view'], api: false, notifications: false },
];

test('log navigation decisions match the registered Go route permissions for every identity', () => {
  const backend = readFileSync(path.join(root, 'internal/httpapi/admin_permissions.go'), 'utf8');
  for (const kind of ['api', 'sms', 'inbox', 'email']) {
    const href = `/admin/logs/${kind}`;
    const route = `GET /api/v1${href}`;
    const matches = [...backend.matchAll(/m\["([^"]+)"\] = "([^"]+)"/g)].filter(match => match[1] === route);
    assert.equal(matches.length, 1, `${route} must declare one explicit permission`);
    const item = navigation.adminNavigationItem(href);
    assert.equal(item?.href, href);
    assert.equal(item.permission, matches[0][2], `${href} menu differs from Go authorization`);
    for (const user of users) {
      const visible = navigation.adminNavigation.filter(item => navigation.can(user, item.permission));
      assert.equal(visible.some(item => item.href === href), kind === 'api' ? user.api : user.notifications, `${user.name}: ${href}`);
    }
  }
});

for (const user of users) {
  test(`API page executes the permission gate for ${user.name}`, async () => {
    const redirect = Symbol('redirect');
    const Page = load('web/src/app/admin/logs/api/page.tsx', {
      '@/lib/admin-navigation': navigation,
      '@/lib/session': { requireUser: async admin => { assert.equal(admin, true); return user; } },
      'next/navigation': { redirect: href => { assert.equal(href, navigation.adminEntry(user)); throw redirect; } },
      '@/components/admin-notification-logs': { AdminNotificationLogs: () => null },
    }).default;
    if (user.api) assert.equal((await Page()).props.kind, 'api');
    else await assert.rejects(Page(), error => error === redirect);
  });

  test(`rendered log tabs and denied page obey ${user.name} grants`, () => {
    const resourcePaths = [];
    const primitive = tag => ({ children }) => React.createElement(tag, null, children);
    const { AdminNotificationLogs } = load('web/src/components/admin-notification-logs.tsx', {
      // Model the query state after the browser's mount effect has restored it.
      // This makes unauthorized fetch suppression observable, beyond SSR alone.
      react: { ...React, useState: () => [new URLSearchParams('q=fixture'), () => {}] },
      '@/lib/admin-navigation': navigation,
      '@/lib/api': { date: String },
      '@/hooks/use-resource': { useResource: path => { resourcePaths.push(path); return { data: null, error: null, refresh() {} }; } },
      './workspace': { Workspace: primitive('main'), useAdminIdentity: () => user },
      './admin-pagination': { AdminPagination: () => null },
      './common': { Failure: () => null, Loading: () => null },
      './ui/button': { Button: primitive('span') },
      './ui/input': { Input: () => null },
      './ui/table': Object.fromEntries(['Table', 'TableBody', 'TableCell', 'TableHead', 'TableHeader', 'TableRow'].map(name => [name, primitive('span')])),
      'next/link': { default: ({ href, children }) => React.createElement('a', { href }, children) },
    });
    for (const kind of ['api', 'sms']) {
      const html = renderToStaticMarkup(React.createElement(AdminNotificationLogs, { kind }));
      const allowed = kind === 'api' ? user.api : user.notifications;
      assert.equal(html.includes('没有查看此日志的权限'), !allowed);
      assert.equal(html.includes('href="/admin/logs/api"'), allowed && user.api);
      for (const tab of ['sms', 'inbox', 'email']) assert.equal(html.includes(`href="/admin/logs/${tab}"`), allowed && user.notifications);
    }
    assert.deepEqual(resourcePaths, [user.api ? '/admin/logs/api?q=fixture' : null, user.notifications ? '/admin/logs/sms?q=fixture' : null]);
  });
}
