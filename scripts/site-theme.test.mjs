import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { createRequire, Module } from 'node:module';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const web = fileURLToPath(new URL('../web/', import.meta.url));
const require = createRequire(path.join(web, 'package.json'));
const ts = require('typescript');
const React = require('react');
const { renderToStaticMarkup } = require('react-dom/server');
function loader(stubs = new Map()) {
  const cache = new Map();
  return function load(filename) {
    if (cache.has(filename)) return cache.get(filename).exports;
    const module = new Module(filename);
    cache.set(filename, module);
    module.filename = filename;
    module.paths = Module._nodeModulePaths(path.dirname(filename));
    const external = createRequire(filename);
    module.require = specifier => {
      if (stubs.has(specifier)) return stubs.get(specifier);
      if (specifier.startsWith('.') || specifier.startsWith('@/')) {
        const base = specifier.startsWith('@/') ? path.join(web, 'src', specifier.slice(2)) : path.resolve(path.dirname(filename), specifier);
        const source = [base, `${base}.tsx`, `${base}.ts`].find(candidate => existsSync(candidate));
        if (source && /\.tsx?$/.test(source)) return load(source);
      }
      return external(specifier);
    };
    const compiled = ts.transpileModule(readFileSync(filename, 'utf8'), {
      reportDiagnostics: true,
      compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true },
    });
    assert.deepEqual((compiled.diagnostics ?? []).filter(item => item.category === ts.DiagnosticCategory.Error), []);
    module._compile(compiled.outputText, filename);
    return module.exports;
  };
}
const helpers = loader()(path.join(web, 'src/lib/site-theme.ts'));
const initial = { site_name: '示例云', logo_url: '/logo.png', company_name: '示例公司', copyright: 'Copyright 2026', record_number: '备案123', support_email: 'help@example.test', support_phone: '12345', address: '示例地址', terms_url: '/terms', privacy_url: '/privacy', support_url: '/support', theme: { template: 'classic', primary_color: '#123456', logo_variant: 'compact', footer_text: '欢迎使用' } };
function view() {
  const state = { pathname: '/', data: null, error: null, effects: [], refreshes: 0, requests: [] };
  const refresh = () => { state.refreshes++; };
  const { SiteProvider, SiteBrand, SiteFooter } = loader(new Map([
    ['react', { ...React, useEffect: effect => { state.effects.push(effect); } }],
    ['next/navigation', { usePathname: () => state.pathname }],
    ['next/image', { __esModule: true, default: ({ src, alt, sizes }) => React.createElement('img', { src, alt, sizes }) }],
    ['@/hooks/use-resource', { useResource: (url, keepPrevious) => { state.requests.push({ url, keepPrevious }); return { data: state.data, error: state.error, refresh }; } }],
  ]))(path.join(web, 'src/components/site-provider.tsx'));
  state.render = (site = initial) => renderToStaticMarkup(React.createElement(SiteProvider, { initial: site }, React.createElement(React.Fragment, null, React.createElement(SiteBrand), React.createElement(SiteFooter))));
  return state;
}

test('SSR renders the initial theme, brand and footer before any client effect runs', () => {
  const state = view();
  const html = state.render();
  assert.match(html, /data-site-template="classic"/);
  assert.match(html, /--primary:#123456/);
  assert.match(html, /--background:#f6f1e8/);
  assert.match(html, /body:has\(> \.site-theme-scope\)/);
  assert.match(html, /data-logo-variant="compact"/);
  assert.match(html, /sizes="24px"/);
  assert.match(html, /欢迎使用/);
  assert.equal(state.refreshes, 0);
  assert.deepEqual(state.requests, [{ url: '/site', keepPrevious: true }]);
  assert.equal(html, state.render(), 'SSR and initial hydration use the same deterministic values');
});

test('refreshed site data updates all display settings and a legacy response resets to safe defaults', () => {
  const state = view();
  state.render();
  state.data = { ...initial, theme: { template: 'minimal', primary_color: '#ffff00', logo_variant: 'text', footer_text: '刷新后的页脚' } };
  let html = state.render();
  assert.match(html, /data-site-template="minimal"/);
  assert.match(html, /--primary:#ffff00/);
  assert.match(html, /--primary-foreground:#000000/);
  assert.match(html, /刷新后的页脚/);
  assert.doesNotMatch(html, /<img|欢迎使用/);
  state.data = { ...initial, theme: undefined };
  html = state.render();
  assert.match(html, /data-site-template="default"/);
  assert.match(html, /--primary:#15705e/);
  assert.doesNotMatch(html, /刷新后的页脚/);
  assert.doesNotThrow(() => state.render(null));
});

test('fixed presets have different backgrounds, borders and radii without accepting custom code', () => {
  const tokens = ['default', 'classic', 'minimal'].map(template => helpers.siteThemeTokens({ ...initial.theme, template }));
  for (const key of ['--background', '--border', '--site-surface-radius']) assert.equal(new Set(tokens.map(value => value[key])).size, 3);
  for (const input of [undefined, null, [], 'bad', { template: 'https://evil.test/code.js', primary_color: '#fff; background:url(evil)', logo_variant: 'html', footer_text: 1 }]) {
    assert.deepEqual(helpers.normalizeSiteTheme(input), { ...helpers.defaultSiteTheme });
  }
  assert.equal(helpers.normalizeSiteTheme({ footer_text: '😀'.repeat(500) }).footer_text.length, 1000);
  assert.equal(helpers.normalizeSiteTheme({ footer_text: '😀'.repeat(501) }).footer_text, '');
  const css = helpers.siteThemePortalCSS({ template: '</style><script>alert(1)</script>', primary_color: 'red;display:none', footer_text: '</style><script>alert(1)</script>' });
  assert.doesNotMatch(css, /<|script|display:none/);
  assert.match(css, /--primary:#15705e/);
});

function luminance(hex) {
  const values = [1, 3, 5].map(start => parseInt(hex.slice(start, start + 2), 16) / 255).map(value => value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4);
  return values.reduce((sum, value, index) => sum + value * [0.2126, 0.7152, 0.0722][index], 0);
}
function contrast(a, b) { const values = [luminance(a), luminance(b)]; return (Math.max(...values) + 0.05) / (Math.min(...values) + 0.05); }
test('configured light and dark fills retain readable button labels and text links on every preset', () => {
  for (const template of ['default', 'classic', 'minimal']) {
    for (const color of ['#ffffff', '#ffff00', '#000000', '#123456', '#165DFF', '#808080', '#ff0000', '#00ff00']) {
      const tokens = helpers.siteThemeTokens({ template, primary_color: color });
      assert.equal(tokens['--primary'], color);
      assert(contrast(tokens['--primary'], tokens['--primary-foreground']) >= 4.5, `${color} button`);
      assert(contrast(tokens['--site-primary-text'], tokens['--background']) >= 4.5, `${color} link on background`);
      assert(contrast(tokens['--site-primary-text'], tokens['--card']) >= 4.5, `${color} link on card`);
    }
  }
  assert.equal(helpers.siteThemeTokens({ primary_color: '#ffffff' })['--primary-foreground'], '#000000');
  assert.equal(helpers.siteThemeTokens({ primary_color: '#000000' })['--primary-foreground'], '#ffffff');
});

test('the actual Tailwind compiler keeps all primary text variants readable without changing brand fills', async () => {
  const { compile } = require('@tailwindcss/node');
  const { transform } = require('lightningcss');
  const compiler = await compile(readFileSync(path.join(web, 'src/app/globals.css'), 'utf8'), { base: path.join(web, 'src/app'), onDependency() {} });
  const candidates = ['text-primary', 'hover:text-primary', '[&_a]:text-primary', 'md:focus-visible:text-primary'];
  const css = compiler.build([...candidates, 'bg-primary']);
  const rules = [];
  transform({ filename: 'site-theme.css', code: Buffer.from(css), visitor: { Rule(rule) { if (rule.type === 'style') rules.push(rule.value); } } });
  const classNames = selector => selector.flatMap(item => item.type === 'class' ? [item.name] : item.selectors ? item.selectors.flatMap(classNames) : []);
  const variable = declaration => declaration.value?.value?.[0]?.value?.name?.ident;
  const color = rule => rule.declarations.declarations.find(declaration => declaration.value?.propertyId?.property === 'color');
  const add = (a, b) => a.map((value, index) => value + b[index]);
  const compare = (a, b) => a[0] - b[0] || a[1] - b[1] || a[2] - b[2];
  function specificity(selector) {
    return selector.reduce((sum, item) => {
      if (item.type === 'id') return add(sum, [1, 0, 0]);
      if (item.type === 'class' || item.type === 'attribute') return add(sum, [0, 1, 0]);
      if (item.type === 'type' || item.type === 'pseudo-element') return add(sum, [0, 0, 1]);
      if (item.type !== 'pseudo-class' || ['where', 'scope'].includes(item.kind)) return sum;
      if (item.selectors) return add(sum, item.selectors.map(specificity).sort(compare).at(-1));
      return add(sum, [0, 1, 0]);
    }, [0, 0, 0]);
  }
  for (const candidate of candidates) {
    const matching = rules.filter(rule => rule.selectors.some(selector => classNames(selector).includes(candidate)) && color(rule));
    const themed = matching.find(rule => variable(color(rule)) === '--site-primary-text');
    const builtin = matching.find(rule => variable(color(rule)) === '--primary');
    assert.ok(themed && builtin, `both theme and built-in text rules compile for ${candidate}`);
    assert.ok(themed.selectors[0].some(item => item.type === 'pseudo-class' && item.kind === 'has'), 'theme rule is restricted to a public scope');
    assert.ok(compare(specificity(themed.selectors[0]), specificity(builtin.selectors[0])) > 0, 'theme rule wins even when the built-in rule is emitted later');
    assert.equal(themed.declarations.importantDeclarations.length, 0, 'no important override affects caller styles');
    assert.equal(color(themed).value.value[0].value.fallback[0].value.name.ident, '--primary', 'the original primary remains the fallback');
  }
  const fill = rules.find(rule => rule.selectors.some(selector => classNames(selector).includes('bg-primary')));
  assert.ok(fill, 'the actual background utility compiles');
  assert.equal(variable(fill.declarations.declarations[0]), '--primary', 'brand fill keeps the configured color');
});

test('admin and admin-login stay isolated during SSR and client route transitions', () => {
  const state = view();
  for (const pathname of ['/admin', '/admin/settings/theme', '/admin-login', '/admin-login/']) {
    state.pathname = pathname;
    const html = state.render();
    assert.doesNotMatch(html, /site-theme-scope|data-site-template|<style|--primary|欢迎使用/);
    assert.match(html, /data-logo-variant="full"/);
  }
  for (const pathname of ['/', '/console/services', '/login', '/register', '/maintenance', '/news', '/administrator']) {
    state.pathname = pathname;
    assert.match(state.render(), /data-site-template="classic"/);
  }
  state.pathname = '/admin';
  assert.doesNotMatch(state.render(), /site-theme-scope|<style/);
});

test('logo modes retain the accessible name, and a missing image always falls back to text', () => {
  const state = view();
  for (const variant of ['full', 'compact', 'text']) {
    const site = { ...initial, theme: { ...initial.theme, logo_variant: variant } };
    const html = state.render(site);
    assert.match(html, /示例云/);
    if (variant === 'text') assert.doesNotMatch(html, /<img/);
    else { assert.match(html, /<img[^>]*alt="示例云"/); assert.match(html, new RegExp(`sizes="${variant === 'compact' ? '24px' : '144px'}"`)); }
    assert.doesNotMatch(state.render({ ...site, logo_url: '' }), /<img/);
  }
});

test('theme footer is escaped plain text appended alongside legal and support information', () => {
  const html = view().render({ ...initial, theme: { ...initial.theme, footer_text: '<img src=x onerror=alert(1)>\n第二行' } });
  assert.match(html, /&lt;img src=x onerror=alert\(1\)&gt;/);
  assert.match(html, /whitespace-pre-wrap/);
  assert.match(html, /示例公司/); assert.match(html, /Copyright 2026/); assert.match(html, /备案123/);
  assert.match(html, /help@example.test/); assert.match(html, /服务条款/); assert.match(html, /隐私政策/);
  assert.doesNotMatch(html, /<img src="?x|<script/);
});

test('focus, visibility and timer refresh only visible pages and unregister on cleanup', t => {
  const previousWindow = globalThis.window;
  const previousDocument = globalThis.document;
  t.after(() => { globalThis.window = previousWindow; globalThis.document = previousDocument; });
  const listeners = new Map();
  const state = view();
  globalThis.document = { visibilityState: 'visible', addEventListener: (name, fn) => listeners.set(name, fn), removeEventListener: (name, fn) => { assert.equal(listeners.get(name), fn); listeners.delete(name); } };
  let interval;
  globalThis.window = { addEventListener: (name, fn) => listeners.set(name, fn), removeEventListener: (name, fn) => { assert.equal(listeners.get(name), fn); listeners.delete(name); }, setInterval: (fn, ms) => { assert.equal(ms, 30000); interval = fn; return 7; }, clearInterval: id => { assert.equal(id, 7); interval = undefined; } };
  state.render();
  const cleanup = state.effects[0]();
  listeners.get('focus')(); interval(); listeners.get('visibilitychange')();
  assert.equal(state.refreshes, 3);
  document.visibilityState = 'hidden';
  listeners.get('focus')(); interval(); listeners.get('visibilitychange')();
  assert.equal(state.refreshes, 3);
  document.visibilityState = 'visible';
  listeners.get('zjmfgo:maintenance')();
  assert.equal(state.refreshes, 4);
  cleanup();
  assert.equal(listeners.size, 0); assert.equal(interval, undefined);
});

test('actual resource hook retains the latest successful theme when a refresh fails', async () => {
  const slots = [];
  let cursor = 0;
  let effects = [];
  let response = initial;
  const failure = new Error('temporarily unavailable');
  const hooks = {
    useState(initialValue) { const index = cursor++; if (!(index in slots)) slots[index] = initialValue; return [slots[index], next => { slots[index] = typeof next === 'function' ? next(slots[index]) : next; }]; },
    useCallback: value => value,
    useMemo(factory, deps) { const index = cursor++, previous = slots[index]; if (!previous || deps.some((dep, i) => dep !== previous.deps[i])) slots[index] = { deps, value: factory() }; return slots[index].value; },
    useEffect(effect, deps) { const index = cursor++; const previous = slots[index]; if (!previous || deps.some((dep, i) => dep !== previous[i])) { slots[index] = deps; effects.push(effect); } },
  };
  const { useResource } = loader(new Map([['react', hooks], ['@/lib/api', {
    authPortal: (_path, portal) => portal ?? 'customer',
    api: async (endpoint, options) => { assert.equal(endpoint, '/site'); assert.equal(options.portal, 'customer'); if (response instanceof Error) throw response; return response; },
  }]]))(path.join(web, 'src/hooks/use-resource.ts'));
  const render = () => { cursor = 0; return useResource('/site', true); };
  const flush = async () => { const pending = effects; effects = []; pending.forEach(effect => effect()); await new Promise(resolve => setImmediate(resolve)); };
  render(); await flush();
  let resource = render();
  assert.equal(resource.data, initial);
  response = { ...initial, theme: { ...initial.theme, template: 'minimal' } };
  resource.refresh(); render(); await flush();
  resource = render();
  assert.equal(resource.data.theme.template, 'minimal');
  response = failure;
  resource.refresh(); render(); await flush();
  resource = render();
  assert.equal(resource.data.theme.template, 'minimal');
  assert.equal(resource.error, failure);
});

test('SSR site loader preserves the public theme and returns null on an unavailable endpoint', async t => {
  const { getSite } = loader(new Map([['server-only', {}], ['react', { cache: fn => fn }]]))(path.join(web, 'src/lib/site-server.ts'));
  let fail = false;
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    assert.match(url, /\/api\/v1\/site$/);
    assert.equal(options.cache, 'no-store');
    return fail ? new Response('', { status: 503 }) : Response.json({ data: initial });
  });
  assert.deepEqual(await getSite(), initial);
  fail = true;
  assert.equal(await getSite(), null);
});
