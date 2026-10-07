"use client";
import { canDiscard, useEditorState, type EditorState } from "./file-editor-state";
import { useState } from "react";
import Link from "next/link";
import { Plus, Save, RefreshCw, Trash2, ArrowUpRight } from "lucide-react";
import { api, date, upload, type AdminContentArticle, type ContentCategory, type ContentTag } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace, PermissionGate, useAdminPermission } from "./workspace";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Checkbox } from "./ui/checkbox";
import { Table,TableHeader,TableHead,TableBody,TableRow,TableCell } from "./ui/table";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "./ui/dialog";

const kindNames = { news: "新闻", knowledge: "知识库" };
const statusNames = { draft: "草稿", published: "发布", archived: "归档" };
const accessNames = { public: "公开", login: "登录后可见", active_service: "有效服务客户可见" };
type Kind = "news" | "knowledge";
type PageData = { items: AdminContentArticle[]; total: number };
const selectStyle = "h-9 w-full min-w-0 rounded-md border border-input bg-white px-3 text-sm";
const emptyArticle = (kind: Kind): AdminContentArticle => ({ id: 0, content_type: kind, category_id: null, category_name: null, categories: [], tags: [], sort_order: 0, title: "", slug: "", excerpt: "", body: "", body_html: "", seo_title: "", seo_description: "", seo_canonical: "", status: "draft", access_level: "public", published_at: null, version: 0, author_id: null, views: 0, created_at: "", updated_at: "", scheduled: false });
const emptyCategory = (kind: Kind): ContentCategory => ({ id: 0, content_type: kind, name: "", slug: "", description: "", sort_order: 0, active: true, version: 0, article_count: 0 });
const emptyTag = (kind: Kind): ContentTag => ({ id: 0, content_type: kind, name: "", slug: "", sort_order: 0, active: true, version: 0, article_count: 0 });

export function ContentAdminView() {
  return <Workspace title="新闻与知识库" admin><ContentManager /></Workspace>;
}
function ContentManager() {
 const [editor,setEditor]=useState<EditorState>({busy:false,dirty:false});
  const [kind, setKind] = useState<Kind>("news");
  const [mode, setMode] = useState("articles");
  const [status, setStatus] = useState("");
  const [category, setCategory] = useState("");
  const [query, setQuery] = useState("");
  const [search, setSearch] = useState("");
  const [offset, setOffset] = useState(0);
  const [article, setArticle] = useState<AdminContentArticle | null>(null);
  const [categoryEdit, setCategoryEdit] = useState<ContentCategory | null>(null);
  const [tagEdit, setTagEdit] = useState<ContentTag | null>(null);
  const [tag, setTag] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const items = useResource<PageData>(`/admin/content?content_type=${kind}&status=${status}&category_id=${category}&tag_id=${tag}&q=${encodeURIComponent(query)}&limit=20&offset=${offset}`, false);
  const categories = useResource<ContentCategory[]>(`/admin/content-categories?content_type=${kind}`, false);
  const tags = useResource<ContentTag[]>(`/admin/content-tags?content_type=${kind}`, false);
  const manage = useAdminPermission("content.manage");
  async function openArticle(id: number) {
    if(busy||editor.busy)return;
    setBusy(true); setError("");
    try { setArticle(await api<AdminContentArticle>(`/admin/content/${id}`)); }
    catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  }
  function refresh() { items.refresh(); categories.refresh(); tags.refresh(); }
  function changedKind(value: Kind) { setKind(value); setOffset(0); setCategory(""); setTag(""); }
  return <><div className="flex flex-wrap justify-between items-end gap-4 mb-8"><div><h1 className="page-title">新闻与知识库</h1><p className="text-sm text-muted-foreground mt-2">管理草稿、发布内容、分类和标签</p></div><div className="flex gap-2"><Button variant="outline" size="icon" aria-label="重新加载内容" onClick={refresh}><RefreshCw /></Button><PermissionGate permission="content.manage"><Button disabled={busy||editor.busy} onClick={() => mode === "articles" ? setArticle(emptyArticle(kind)) : mode === "categories" ? setCategoryEdit(emptyCategory(kind)) : setTagEdit(emptyTag(kind))}><Plus />{mode === "articles" ? "新增文章" : mode === "categories" ? "新增分类" : "新增标签"}</Button></PermissionGate></div></div>
    <nav aria-label="内容管理分区" className="flex flex-wrap gap-1 border-b mb-5"><div className="flex overflow-x-auto">{(["news", "knowledge"] as Kind[]).map(k => <button type="button" key={k} aria-current={kind === k ? "page" : undefined} className={`shrink-0 border-b-2 px-4 py-3 text-sm ${kind === k ? "border-primary text-primary font-medium" : "border-transparent text-muted-foreground"}`} disabled={busy||editor.busy} onClick={() => {if(canDiscard(editor))changedKind(k);}}>{kindNames[k]}</button>)}</div><div className="flex overflow-x-auto sm:ml-auto">{(["articles", "categories", "tags"] as const).map(value => <button type="button" key={value} aria-current={mode === value ? "page" : undefined} className={`shrink-0 border-b-2 px-4 py-3 text-sm ${mode === value ? "border-primary text-primary font-medium" : "border-transparent text-muted-foreground"}`} disabled={busy||editor.busy} onClick={() => {if(canDiscard(editor))setMode(value);}}>{value === "articles" ? "文章" : value === "categories" ? "分类" : "标签"}</button>)}</div></nav>
    {error && <p role="alert" className="text-sm text-destructive mb-4">{error}</p>}
    {mode === "articles" && <form className="grid sm:grid-cols-[1fr_1fr_2fr_auto] gap-3 mb-6" onSubmit={e => { e.preventDefault(); setQuery(search); setOffset(0); }}><select aria-label="文章状态" className={selectStyle} value={status} onChange={e => { setStatus(e.target.value); setOffset(0); }}><option value="">全部状态</option>{Object.entries(statusNames).map(([key, label]) => <option key={key} value={key}>{label}</option>)}</select><select aria-label="文章分类" className={selectStyle} value={category} onChange={e => { setCategory(e.target.value); setOffset(0); }}><option value="">全部分类</option><option value="0">未分类</option>{categories.data?.map(c => <option key={c.id} value={c.id}>{c.name}{!c.active && "（隐藏）"}</option>)}</select><Input aria-label="搜索文章" placeholder="搜索标题或标识" value={search} onChange={e => setSearch(e.target.value)} /><Button variant="outline">搜索</Button></form>}
    {mode === "articles" && <div className="flex items-center gap-3 mb-5"><label className="text-sm" htmlFor="admin-content-tag">标签</label><select id="admin-content-tag" className={`${selectStyle} max-w-xs`} value={tag} onChange={e => { setTag(e.target.value); setOffset(0); }}><option value="">全部标签</option>{tags.data?.map(t => <option key={t.id} value={t.id}>{t.name}</option>)}</select></div>}
    {categories.error || tags.error || (mode === "articles" && items.error) ? <Failure error={categories.error ?? tags.error ?? items.error!} retry={refresh} /> : !categories.data || !tags.data || (mode === "articles" && !items.data) ? <Loading /> : mode === "articles" ? <><div className="overflow-x-auto border-y"><Table><TableHeader><TableRow><TableHead>ID</TableHead><TableHead>标题</TableHead><TableHead>分类 / 标签</TableHead><TableHead>发布时间</TableHead><TableHead>状态 / 范围</TableHead><TableHead>管理</TableHead></TableRow></TableHeader><TableBody>{items.data!.items.map(item=><TableRow key={item.id}><TableCell>{item.id}</TableCell><TableCell className="min-w-48"><p className="font-medium break-words">{item.title}</p><p className="text-xs text-muted-foreground mt-1">版本 {item.version} · {item.views} 次阅读 · 排序 {item.sort_order}</p></TableCell><TableCell className="max-w-xs break-words">{item.categories.map(c=>c.name).join("、")||"未分类"}<p className="text-xs text-muted-foreground mt-1">{item.tags.map(t=>t.name).join("、")}</p></TableCell><TableCell className="whitespace-nowrap">{item.published_at?date(item.published_at):"尚未发布"}{item.scheduled&&<p className="text-xs text-muted-foreground">定时发布</p>}</TableCell><TableCell className="whitespace-nowrap">{statusNames[item.status]}<p className="text-xs text-muted-foreground mt-1">{accessNames[item.access_level]}</p></TableCell><TableCell><Button size="sm" variant="outline" disabled={busy} aria-label={`查看 ${item.title}`} onClick={()=>void openArticle(item.id)}>{manage?"编辑":"查看"}</Button></TableCell></TableRow>)}{items.data!.items.length===0&&<TableRow><TableCell colSpan={6} className="h-28 text-center text-muted-foreground">暂无文章</TableCell></TableRow>}</TableBody></Table></div><div className="flex justify-between items-center gap-3 mt-5 text-sm"><span>{items.data!.total} 篇文章</span><div className="flex gap-2"><Button variant="outline" disabled={offset === 0} onClick={() => setOffset(v => Math.max(0, v - 20))}>上一页</Button><Button variant="outline" disabled={offset + 20 >= items.data!.total} onClick={() => setOffset(v => v + 20)}>下一页</Button></div></div></> : mode === "categories" ? <div className="divide-y border-y">{categories.data.map(c => <article key={c.id} className="py-5 flex flex-wrap justify-between gap-3"><div className="min-w-0 flex-1"><p className="font-medium break-words">{c.name}</p><p className="text-xs text-muted-foreground mt-2">{c.active ? "显示" : "隐藏"} · {c.article_count} 篇文章 · 排序 {c.sort_order}</p><p className="text-sm text-muted-foreground mt-2 whitespace-pre-wrap break-words">{c.description}</p></div><Button variant="outline" aria-label={`查看分类 ${c.name}`} onClick={() => setCategoryEdit(c)}>{manage ? "编辑" : "查看"}</Button></article>)}{categories.data.length === 0 && <p className="py-16 text-center text-muted-foreground">暂无分类</p>}</div> : <div className="divide-y border-y">{tags.data.map(t => <article key={t.id} className="py-5 flex items-center justify-between gap-3"><div><p className="font-medium">{t.name}</p><p className="text-xs text-muted-foreground mt-2">{t.active ? "显示" : "隐藏"} · {t.article_count} 篇文章 · 排序 {t.sort_order}</p></div><Button variant="outline" onClick={() => setTagEdit(t)}>{manage ? "编辑" : "查看"}</Button></article>)}{tags.data.length === 0 && <p className="py-16 text-center text-muted-foreground">暂无标签</p>}</div>}
    <Dialog open={!!article} onOpenChange={open => { if (!open&&canDiscard(editor)) setArticle(null); }}><DialogContent className="sm:max-w-3xl max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>{article?.id ? (manage ? "编辑文章" : "查看文章") : "新增文章"}</DialogTitle><DialogDescription>正文支持纯文本及图文内容，发布后按可见范围供客户阅读。</DialogDescription></DialogHeader>{article && <ArticleForm report={setEditor} key={`${article.id}:${article.version}`} initial={article} categories={categories.data ?? []} tags={tags.data ?? []} manage={manage} onClose={() => setArticle(null)} onSaved={refresh} onReload={() => void openArticle(article.id)} />}</DialogContent></Dialog>
    <Dialog open={!!categoryEdit} onOpenChange={open => { if (!open&&canDiscard(editor)) setCategoryEdit(null); }}><DialogContent className="max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>{categoryEdit?.id ? (manage ? "编辑分类" : "查看分类") : "新增分类"}</DialogTitle><DialogDescription>隐藏分类会隐藏其文章；关联文章需移走后才能删除分类。</DialogDescription></DialogHeader>{categoryEdit && <CategoryForm report={setEditor} key={`${categoryEdit.id}:${categoryEdit.version}`} initial={categoryEdit} manage={manage} onClose={() => setCategoryEdit(null)} onSaved={refresh} onReload={async () => { const list = await api<ContentCategory[]>(`/admin/content-categories?content_type=${kind}`); setCategoryEdit(list.find(c => c.id === categoryEdit.id) ?? null); categories.refresh(); }} />}</DialogContent></Dialog>
    <Dialog open={!!tagEdit} onOpenChange={open => { if (!open&&canDiscard(editor)) setTagEdit(null); }}><DialogContent><DialogHeader><DialogTitle>{tagEdit?.id ? (manage ? "编辑标签" : "查看标签") : "新增标签"}</DialogTitle><DialogDescription>标签用于文章筛选。隐藏标签不会显示在客户页面。</DialogDescription></DialogHeader>{tagEdit && <TagForm report={setEditor} key={`${tagEdit.id}:${tagEdit.version}`} initial={tagEdit} manage={manage} onClose={() => setTagEdit(null)} onSaved={refresh} onReload={async () => { const list = await api<ContentTag[]>(`/admin/content-tags?content_type=${kind}`); setTagEdit(list.find(t => t.id === tagEdit.id) ?? null); tags.refresh(); }} />}</DialogContent></Dialog>
  </>;
}
function ArticleForm({ initial, categories, tags, manage, onClose, onSaved, onReload ,report}:{report:(state:EditorState)=>void; initial: AdminContentArticle; categories: ContentCategory[]; tags: ContentTag[]; manage: boolean; onClose: () => void; onSaved: () => void; onReload: () => void }) {
  const [form, setForm] = useState(initial);
  const [categoryIDs, setCategoryIDs] = useState<number[]>(initial.categories.map(c => c.id));
  const [tagIDs, setTagIDs] = useState<number[]>(initial.tags.map(t => t.id));
  const [published, setPublished] = useState(initial.published_at ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const dirty=manage&&(JSON.stringify(form)!==JSON.stringify(initial)||JSON.stringify(categoryIDs)!==JSON.stringify(initial.categories.map(c=>c.id))||JSON.stringify(tagIDs)!==JSON.stringify(initial.tags.map(t=>t.id))||published!==(initial.published_at??""));useEditorState(busy,dirty,report);
 async function save(e: React.FormEvent) {
    e.preventDefault(); if (busy || !manage) return;
    setBusy(true); setError("");
    try {
      if (published && (!/(Z|[+-]\d{2}:\d{2})$/.test(published) || !Number.isFinite(Date.parse(published)))) throw new Error("请填写有效的日期时间及其时区，例如 2026-10-06T12:00:00+08:00");
      await api(`/admin/content${form.id ? `/${form.id}` : ""}`, { method: form.id ? "PUT" : "POST", body: JSON.stringify({ content_type: form.content_type, category_id: form.category_id, category_ids: categoryIDs, tag_ids: tagIDs, sort_order: form.sort_order, title: form.title, slug: form.slug, excerpt: form.excerpt, body: form.body, body_html: form.body_html, seo: { title: form.seo_title, description: form.seo_description, canonical: form.seo_canonical }, status: form.status, access_level: form.access_level, published_at: published ? new Date(published).toISOString() : null, expected_version: form.version }) });
      onSaved(); onClose();
    } catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  }
  async function remove() {
    if (!manage || busy || !window.confirm("永久删除这篇文章？")) return;
    setBusy(true); setError("");
    try { await api(`/admin/content/${form.id}`, { method: "DELETE", body: JSON.stringify({ expected_version: form.version }) }); onSaved(); onClose(); }
    catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  }
  return <form onSubmit={save} className="space-y-5">
    <fieldset disabled={busy || !manage} className="space-y-5"><h3 className="border-b pb-3 text-sm font-semibold">基本信息与发布</h3>
      <div className="grid sm:grid-cols-2 gap-4">
        <label className="grid gap-2 text-sm">标题<Input aria-label="文章标题" value={form.title} required maxLength={240} onChange={e => setForm({ ...form, title: e.target.value })} /></label>
        <label className="grid gap-2 text-sm">标识<Input aria-label="文章标识" value={form.slug} required maxLength={120} pattern={"[a-z][a-z0-9\\-]*"} onChange={e => setForm({ ...form, slug: e.target.value })} /></label>
        <label className="grid gap-2 text-sm">主分类<select aria-label="编辑文章主分类" className={selectStyle} value={form.category_id ?? ""} onChange={e => { const next = e.target.value ? Number(e.target.value) : null; setCategoryIDs(next ? [next, ...categoryIDs.filter(id => id !== next)] : []); setForm({ ...form, category_id: next }); }}><option value="">未分类</option>{categories.filter(c => c.content_type === form.content_type).map(c => <option key={c.id} value={c.id}>{c.name}{!c.active && "（隐藏）"}</option>)}</select></label>
        <label className="grid gap-2 text-sm">排序<Input aria-label="文章排序" type="number" min={0} max={1000000} value={form.sort_order} onChange={e => setForm({ ...form, sort_order: Number(e.target.value) })} /></label>
        <label className="grid gap-2 text-sm">状态<select aria-label="编辑文章状态" className={selectStyle} value={form.status} onChange={e => setForm({ ...form, status: e.target.value as AdminContentArticle["status"] })}>{Object.entries(statusNames).map(([key, label]) => <option key={key} value={key}>{label}</option>)}</select></label>
        {form.content_type === "knowledge" && <label className="grid gap-2 text-sm">可见范围<select aria-label="文章可见范围" className={selectStyle} value={form.access_level} onChange={e => setForm({ ...form, access_level: e.target.value as AdminContentArticle["access_level"] })}>{Object.entries(accessNames).map(([key, label]) => <option key={key} value={key}>{label}</option>)}</select></label>}
        <label className="grid gap-2 text-sm sm:col-span-2">发布时间<Input aria-label="发布时间" placeholder="例如 2026-10-06T12:00:00+08:00" value={published} onChange={e => setPublished(e.target.value)} /><span className="text-xs text-muted-foreground">留空保留已发布时间；首次发布时立即发布。</span></label>
      </div>
      <fieldset className="space-y-2"><legend className="text-sm">附加分类</legend><div className="grid sm:grid-cols-2 gap-2">{categories.filter(c => c.id !== form.category_id).map(c => <label key={c.id} className="flex items-center gap-2 text-sm"><Checkbox disabled={!form.category_id} checked={categoryIDs.includes(c.id)} onCheckedChange={v => setCategoryIDs(v === true ? [...categoryIDs, c.id] : categoryIDs.filter(id => id !== c.id))} />{c.name}{!c.active && "（隐藏）"}</label>)}</div></fieldset>
      <fieldset className="space-y-2"><legend className="text-sm">标签</legend><div className="grid sm:grid-cols-2 gap-2">{tags.map(t => <label key={t.id} className="flex items-center gap-2 text-sm"><Checkbox checked={tagIDs.includes(t.id)} onCheckedChange={v => setTagIDs(v === true ? [...tagIDs, t.id] : tagIDs.filter(id => id !== t.id))} />{t.name}{!t.active && "（隐藏）"}</label>)}</div></fieldset>
      <h3 className="border-b pb-3 pt-3 text-sm font-semibold">文章内容</h3><label className="grid gap-2 text-sm">摘要<Textarea aria-label="文章摘要" value={form.excerpt} maxLength={1000} rows={2} onChange={e => setForm({ ...form, excerpt: e.target.value })} /></label>
      <label className="grid gap-2 text-sm">正文纯文本<Textarea aria-label="文章正文" required value={form.body} maxLength={20000} rows={6} onChange={e => setForm({ ...form, body: e.target.value })} /></label>
      <label className="grid gap-2 text-sm">正文 HTML（仅允许安全白名单标签）<Textarea aria-label="文章正文 HTML" value={form.body_html} maxLength={100000} rows={8} onChange={e => setForm({ ...form, body_html: e.target.value })} /></label>
      <fieldset className="space-y-3 border-t pt-4"><legend className="text-sm font-semibold">SEO 元数据</legend><label className="grid gap-2 text-sm">SEO 标题<Input maxLength={240} value={form.seo_title} onChange={e => setForm({ ...form, seo_title: e.target.value })} /></label><label className="grid gap-2 text-sm">SEO 描述<Textarea maxLength={1000} rows={2} value={form.seo_description} onChange={e => setForm({ ...form, seo_description: e.target.value })} /></label><label className="grid gap-2 text-sm">Canonical 路径<Input placeholder="/news/123" value={form.seo_canonical} onChange={e => setForm({ ...form, seo_canonical: e.target.value })} /></label></fieldset>
      {form.id > 0 && <label className="grid gap-2 text-sm">插入图片（单张不超过 5 MiB）<input type="file" accept="image/png,image/jpeg,image/gif,image/webp" onChange={async e => { const file = e.target.files?.[0]; if (!file) return; const data = new FormData(); data.append("file", file); setBusy(true); setError(""); e.currentTarget.value = ""; try { const result = await upload<{url:string}>(`/admin/content/${form.id}/images`, data); setForm(current => ({ ...current, body_html: `${current.body_html}<p><img src="${result.url}" alt="" /></p>` })); } catch (err) { setError((err as Error).message); } finally { setBusy(false); } }} /></label>}
    </fieldset>
    {error && <p role="alert" className="text-sm text-destructive break-words">{error}{form.id > 0 && <Button type="button" variant="link" onClick={()=>{if(canDiscard({busy,dirty||JSON.stringify(categoryIDs)!==JSON.stringify(initial.categories.map(c=>c.id))||JSON.stringify(tagIDs)!==JSON.stringify(initial.tags.map(t=>t.id))||published!==(initial.published_at??"")}))onReload();}}>重新加载</Button>}</p>}
    <div className="flex flex-wrap justify-between gap-3 border-t pt-4"><div className="flex gap-2">{form.id > 0 && manage && <Button type="button" variant="outline" disabled={busy} onClick={() => void remove()}><Trash2 />删除文章</Button>}{form.id > 0 && <Button variant="ghost" type="button" asChild><Link href={`/${form.content_type === "news" ? "news" : "knowledgebase"}/${form.id}`} target="_blank"><ArrowUpRight />客户页面</Link></Button>}</div>{manage && <div className="flex gap-2"><Button type="button" variant="outline" disabled={busy} onClick={()=>{setForm(initial);setCategoryIDs(initial.categories.map(c=>c.id));setTagIDs(initial.tags.map(t=>t.id));setPublished(initial.published_at??"");setError("");}}>取消更改</Button><Button type="button" variant="ghost" disabled={busy} onClick={()=>{if(canDiscard({busy,dirty}))onClose();}}>关闭</Button><Button disabled={busy}><Save />{busy ? "保存中" : "保存文章"}</Button></div>}</div>
  </form>;
}
function CategoryForm({ initial, manage, onClose, onSaved, onReload ,report}:{report:(state:EditorState)=>void; initial: ContentCategory; manage: boolean; onClose: () => void; onSaved: () => void; onReload: () => Promise<void> }) {
  const [form, setForm] = useState(initial);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const dirty=manage&&(JSON.stringify(form)!==JSON.stringify(initial));useEditorState(busy,dirty,report);
 async function save(e: React.FormEvent) {
    e.preventDefault(); if (busy || !manage) return; setBusy(true); setError("");
    try { await api(`/admin/content-categories${form.id ? `/${form.id}` : ""}`, { method: form.id ? "PUT" : "POST", body: JSON.stringify({ content_type: form.content_type, name: form.name, slug: form.slug, description: form.description, sort_order: form.sort_order, active: form.active, expected_version: form.version }) }); onSaved(); onClose(); }
    catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  async function remove() {
    if (!manage || busy || !window.confirm("删除这个分类？")) return; setBusy(true); setError("");
    try { await api(`/admin/content-categories/${form.id}`, { method: "DELETE", body: JSON.stringify({ expected_version: form.version }) }); onSaved(); onClose(); }
    catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  return <form onSubmit={save} className="space-y-5"><fieldset disabled={busy || !manage} className="space-y-4"><label className="grid gap-2 text-sm">分类名称<Input aria-label="分类名称" required maxLength={120} value={form.name} onChange={e => setForm({ ...form, name: e.target.value })} /></label><label className="grid gap-2 text-sm">分类标识<Input aria-label="分类标识" required pattern={"[a-z][a-z0-9\\-]*"} maxLength={120} value={form.slug} onChange={e => setForm({ ...form, slug: e.target.value })} /></label><label className="grid gap-2 text-sm">分类说明<Textarea aria-label="分类说明" maxLength={500} value={form.description} onChange={e => setForm({ ...form, description: e.target.value })} /></label><label className="grid gap-2 text-sm">排序<Input aria-label="分类排序" type="number" min={0} max={1000000} value={form.sort_order} onChange={e => setForm({ ...form, sort_order: Number(e.target.value) })} /></label><label className="flex gap-3 items-center text-sm"><Checkbox checked={form.active} onCheckedChange={checked => setForm({ ...form, active: checked === true })} />显示分类</label></fieldset>{error && <p role="alert" className="text-sm text-destructive break-words">{error}{form.id > 0 && <Button type="button" variant="link" onClick={() => void onReload().catch(e => setError(e.message))}>重新加载</Button>}</p>}<div className="flex justify-between gap-3 border-t pt-4">{form.id > 0 && manage ? <Button type="button" variant="outline" disabled={busy} onClick={() => void remove()}><Trash2 />删除分类</Button> : <span />}{manage && <Button disabled={busy}><Save />{busy ? "保存中" : "保存分类"}</Button>}</div></form>;
}

function TagForm({ initial, manage, onClose, onSaved, onReload ,report}:{report:(state:EditorState)=>void; initial: ContentTag; manage: boolean; onClose: () => void; onSaved: () => void; onReload: () => Promise<void> }) {
  const [form, setForm] = useState(initial);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const dirty=manage&&(JSON.stringify(form)!==JSON.stringify(initial));useEditorState(busy,dirty,report);
 async function save(e: React.FormEvent) {
    e.preventDefault(); if (!manage || busy) return; setBusy(true); setError("");
    try { await api(`/admin/content-tags${form.id ? `/${form.id}` : ""}`, { method: form.id ? "PUT" : "POST", body: JSON.stringify({ content_type: form.content_type, name: form.name, slug: form.slug, sort_order: form.sort_order, active: form.active, expected_version: form.version }) }); onSaved(); onClose(); }
    catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  async function remove() {
    if (!manage || busy || !window.confirm("删除这个标签？")) return; setBusy(true); setError("");
    try { await api(`/admin/content-tags/${form.id}`, { method: "DELETE", body: JSON.stringify({ expected_version: form.version }) }); onSaved(); onClose(); }
    catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  return <form onSubmit={save} className="space-y-5"><fieldset disabled={busy || !manage} className="space-y-4">
    <label className="grid gap-2 text-sm">标签名称<Input required maxLength={120} value={form.name} onChange={e => setForm({ ...form, name: e.target.value })} /></label>
    <label className="grid gap-2 text-sm">标签标识<Input required maxLength={120} pattern={"[a-z][a-z0-9\\-]*"} value={form.slug} onChange={e => setForm({ ...form, slug: e.target.value })} /></label>
    <label className="grid gap-2 text-sm">排序<Input type="number" min={0} max={1000000} value={form.sort_order} onChange={e => setForm({ ...form, sort_order: Number(e.target.value) })} /></label>
    <label className="flex items-center gap-3 text-sm"><Checkbox checked={form.active} onCheckedChange={v => setForm({ ...form, active: v === true })} />显示标签</label>
  </fieldset>{error && <p role="alert" className="text-sm text-destructive">{error}{form.id > 0 && <Button variant="link" type="button" onClick={() => void onReload().catch(e => setError(e.message))}>重新加载</Button>}</p>}
  <div className="flex justify-between border-t pt-4">{form.id > 0 && manage ? <Button type="button" variant="outline" disabled={busy} onClick={() => void remove()}><Trash2 />删除标签</Button> : <span />}{manage && <Button disabled={busy}><Save />保存标签</Button>}</div></form>;
}
