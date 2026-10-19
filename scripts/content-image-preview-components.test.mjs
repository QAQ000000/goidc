import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { createRequire, Module } from 'node:module';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const web = fileURLToPath(new URL('../web/', import.meta.url));
const require = createRequire(path.join(web, 'package.json'));
const React = require('react');
const ts = require('typescript');
const cache = new Map(), controls = new Map();
let current, requests, article;
const react = { ...React, useState(initial) {
  const state = current, index = state.cursor++;
  if (!(index in state.values)) state.values[index] = typeof initial === 'function' ? initial() : initial;
  return [state.values[index], next => { state.values[index] = typeof next === 'function' ? next(state.values[index]) : next; }];
} };
function control(name) {
  if (!controls.has(name)) controls.set(name, props => React.createElement(name.toLowerCase(), props, props.children));
  return controls.get(name);
}
function load(filename) {
  if (cache.has(filename)) return cache.get(filename).exports;
  const module = new Module(filename); cache.set(filename, module); module.filename = filename;
  module.paths = Module._nodeModulePaths(path.dirname(filename));
  const external = createRequire(filename);
  module.require = specifier => {
    if (specifier === 'react') return react;
    if (specifier === 'next/link') return { __esModule: true, default: control('Link') };
    if (specifier === 'next/image') return { __esModule: true, default: control('Image') };
    if (specifier === 'lucide-react' || specifier.includes('/ui/')) return new Proxy({}, { get: (_, name) => control(String(name)) });
    if (specifier === './workspace') return { Workspace: control('Workspace'), PermissionGate: control('PermissionGate'), useAdminPermission: () => true };
    if (specifier === './common') return { Loading: control('Loading'), Failure: control('Failure') };
    if (specifier === './file-editor-state') return { canDiscard: () => true, useEditorState() {} };
    if (specifier === '@/hooks/use-resource') return { useResource: endpoint => ({ data: endpoint.startsWith('/admin/content?') ? { items: [article], total: 1 } : [], error: null, refresh() {} }) };
    if (specifier === '@/lib/api') return {
      date: value => value,
      api: async (endpoint, options = {}) => {
        requests.push({ endpoint, method: options.method ?? 'GET', body: options.body ? JSON.parse(options.body) : undefined });
        return article;
      },
      upload: async endpoint => { requests.push({ endpoint, method: 'POST' }); return { url: '/api/v1/content-images/18' }; },
    };
    if (specifier.startsWith('.')) {
      const base = path.resolve(path.dirname(filename), specifier);
      const source = [`${base}.tsx`, `${base}.ts`, base].find(candidate => existsSync(candidate));
      if (source && /\.tsx?$/.test(source)) return load(source);
    }
    return external(specifier);
  };
  const compiled = ts.transpileModule(readFileSync(filename, 'utf8'), { reportDiagnostics: true, compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true } });
  assert.deepEqual((compiled.diagnostics ?? []).filter(diagnostic => diagnostic.category === ts.DiagnosticCategory.Error), []);
  module._compile(compiled.outputText, filename);
  return module.exports;
}
function mount(component, props = {}) {
  const state = { cursor: 0, values: [] };
  return () => { current = state; state.cursor = 0; try { return component(props); } finally { current = undefined; } };
}
function nodes(tree) { return !tree || typeof tree !== 'object' ? [] : Array.isArray(tree) ? tree.flatMap(nodes) : [tree, ...React.Children.toArray(tree.props?.children).flatMap(nodes)]; }
function text(tree) { return typeof tree === 'string' || typeof tree === 'number' ? String(tree) : !tree ? '' : Array.isArray(tree) ? tree.map(text).join('') : React.Children.toArray(tree.props?.children).map(text).join(''); }
function find(tree, predicate) { const node = nodes(tree).find(predicate); assert(node, `missing control: ${text(tree)}`); return node; }
const { AdminContentImagePreview, adminContentImagePreviews } = load(path.join(web, 'src/components/admin-content-image-preview.tsx'));
const { ContentAdminView } = load(path.join(web, 'src/components/content-admin.tsx'));
function fixture() {
  article = { id: 8, content_type: 'knowledge', title: 'Draft', slug: 'draft', body: '正文', body_html: '<p><img src="/api/v1/content-images/12" alt="正文图" /></p>', status: 'draft', access_level: 'login', published_at: null, categories: [], tags: [], category_id: null, sort_order: 0, version: 3, seo_title: '', seo_description: '', seo_canonical: '' };
  requests = [];
}
async function editor() {
  fixture();
  const manager = mount(ContentAdminView().props.children.type);
  await find(manager(), node => node.type === control('Button') && node.props['aria-label'] === '查看 Draft').props.onClick();
  const form = find(manager(), node => node.type?.name === 'ArticleForm');
  return mount(form.type, form.props);
}
function previewImages(tree) { return nodes(AdminContentImagePreview(find(tree, node => node.type === AdminContentImagePreview).props)).filter(node => node.type === control('Image')); }

test('canonical uploaded image references become fixed administrator preview URLs and deduplicate', () => {
  const html = '<img src="/api/v1/content-images/12"><IMG title="a > b" SRC=\'/api/v1/content-images/13\'><img src=/api/v1/content-images/14><img src="/api/v1/content-images/12">';
  assert.deepEqual(adminContentImagePreviews(html), [12, 13, 14].map(id => ({ source: `/api/v1/content-images/${id}`, preview: `/api/v1/admin/content-images/${id}` })));
  const images = nodes(AdminContentImagePreview({ html })).filter(node => node.type === control('Image'));
  assert.equal(images.length, 3);
  for (const image of images) { assert.equal(image.props.unoptimized, true); assert.match(image.props.src, /^\/api\/v1\/admin\/content-images\/\d+$/); }
});

test('external, forged administrator, malformed and non-image references never enter a preview', () => {
  const sources = ['https://example.test/api/v1/content-images/12', '//example.test/api/v1/content-images/12', '/api/v1/admin/content-images/12', '/api/v1/content-images/12?auth_portal=admin', '/api/v1/content-images/../12', '/api/v1/content-images/0', '/api/v1/content-images/-1', '/api/v1/content-images/12#fragment', 'javascript:alert(1)', 'data:image/svg+xml,unsafe'];
  const html = sources.map(source => `<img src="${source}">`).join('') + '<a href="/api/v1/content-images/12">link</a><img data-src="/api/v1/content-images/12"><img title=\'src="/api/v1/content-images/12"\'>';
  assert.deepEqual(adminContentImagePreviews(html), []);
  assert.equal(AdminContentImagePreview({ html }), null);
});

test('the real article editor previews draft images with administrator URLs but saves canonical public HTML', async () => {
  const render = await editor();
  let tree = render();
  assert.equal(previewImages(tree)[0].props.src, '/api/v1/admin/content-images/12');
  const field = find(tree, node => node.props?.['aria-label'] === '文章正文 HTML');
  const html = `${field.props.value}<img src="/api/v1/content-images/15">`;
  field.props.onChange({ target: { value: html } });
  tree = render();
  assert.deepEqual(previewImages(tree).map(image => image.props.src), ['/api/v1/admin/content-images/12', '/api/v1/admin/content-images/15']);
  assert.equal(find(tree, node => node.props?.['aria-label'] === '文章正文 HTML').props.value, html);
  await tree.props.onSubmit({ preventDefault() {} });
  const saved = requests.find(request => request.method === 'PUT');
  assert.equal(saved.body.body_html, html);
  assert.equal(saved.body.body_html.includes('/api/v1/admin/content-images/'), false);
  assert.equal(article.body_html.includes('/api/v1/admin/'), false);
});

test('upload insertion keeps its canonical public URL while the adjacent preview uses the administrator route', async () => {
  const render = await editor();
  const file = find(render(), node => node.type === 'input' && node.props.type === 'file');
  const input = { value: 'image.png', files: [new File(['image'], 'image.png', { type: 'image/png' })] };
  await file.props.onChange({ target: input, currentTarget: input });
  const tree = render();
  const html = find(tree, node => node.props?.['aria-label'] === '文章正文 HTML').props.value;
  assert.match(html, /src="\/api\/v1\/content-images\/18"/);
  assert.equal(html.includes('/api/v1/admin/content-images/'), false);
  assert.equal(previewImages(tree).at(-1).props.src, '/api/v1/admin/content-images/18');
  await tree.props.onSubmit({ preventDefault() {} });
  assert.equal(requests.find(request => request.method === 'PUT').body.body_html, html);
  const customerLink = find(tree, node => node.type === control('Link') && text(node).includes('客户页面'));
  assert.equal(customerLink.props.href, '/knowledgebase/8');
});
