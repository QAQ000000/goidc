"use client";

import { useState } from "react";
import { Pencil, Plus, RefreshCw, Save, Trash2 } from "lucide-react";
import { api } from "@/lib/api";
import { siteContentInput } from "@/lib/admin-configuration-inputs";
import { useResource } from "@/hooks/use-resource";
import { Failure, Loading } from "./common";
import { Workspace, PermissionGate, useAdminPermission } from "./workspace";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "./ui/dialog";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { AdminListPage, AdminListRegion } from "./admin-list-page";
import { AdminPagination } from "./admin-pagination";
import { ResourceTable, type ResourceColumn } from "./resource-table";
import { Badge } from "./ui/badge";

// Content records have different schemas per kind; the API validates each schema.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
type Row = Record<string, any>;
type PageResult = { items: Row[]; total: number; limit: number; offset: number };
const endpoint = { fields: "/admin/site-fields", navigation: "/admin/navigation", links: "/admin/friendly-links", groups: "/admin/file-groups" } as const;
const titles = { fields: "官网自定义字段", navigation: "导航管理", links: "友情链接", groups: "文件分组" } as const;
type Kind = keyof typeof endpoint;

function empty(kind: Kind): Row {
  if (kind === "fields") return { field_key: "", label: "", field_type: "text", description: "", required: false, active: true, sort_order: 0 };
  if (kind === "navigation") return { parent_id: null, label: "", href: "/", icon: "", sort_order: 0, open_new_tab: false, active: true };
  if (kind === "links") return { name: "", url: "https://", logo_url: "", description: "", sort_order: 0, active: true };
  return { parent_id: null, name: "", description: "", sort_order: 0, active: true };
}

export function SiteThemeSettingsView() {
  const resource = useResource<Row>("/admin/theme-settings", false);
  const [form, setForm] = useState<Row | null>(null); const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  function begin() { if (resource.data) { setError(""); setForm({ ...resource.data }); } }
  async function save(event: React.FormEvent) { event.preventDefault(); if (!form || busy) return; setBusy(true); setError(""); try { await api("/admin/theme-settings", { method: "PUT", headers: { "Idempotency-Key": `theme-${crypto.randomUUID()}` }, body: JSON.stringify({ ...form, expected_version: resource.data?.version }) }); setForm(null); resource.refresh(); } catch (e) { setError((e as Error).message); } finally { setBusy(false); } }
  return <Workspace title="主题模板" admin><div className="min-w-0 max-w-3xl space-y-6"><div className="flex flex-wrap items-center justify-between gap-3"><div><h1 className="page-title">主题模板</h1><p className="mt-2 text-sm text-muted-foreground">配置官网外观、品牌色和页脚信息。</p></div><Button variant="outline" size="icon" title="刷新" aria-label="刷新" onClick={resource.refresh}><RefreshCw /></Button></div>{resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <section className="border p-5 space-y-4"><div className="flex justify-between gap-3"><h2 className="font-medium">当前主题</h2><PermissionGate permission="settings.manage"><Button variant="outline" onClick={begin}><Pencil size={16} />编辑</Button></PermissionGate></div><dl className="grid gap-3 sm:grid-cols-2 text-sm"><div><dt className="text-muted-foreground">模板</dt><dd>{resource.data.template}</dd></div><div><dt className="text-muted-foreground">主色</dt><dd className="flex items-center gap-2"><span className="h-4 w-4 border" style={{ backgroundColor: resource.data.primary_color }} />{resource.data.primary_color}</dd></div><div><dt className="text-muted-foreground">Logo</dt><dd>{resource.data.logo_variant}</dd></div><div><dt className="text-muted-foreground">版本</dt><dd>{resource.data.version}</dd></div></dl><p className="text-sm whitespace-pre-wrap">{resource.data.footer_text || "未设置页脚文案"}</p></section>}{error && <p role="alert" className="text-sm text-destructive">{error}</p>}<Dialog open={!!form} onOpenChange={open => !open && setForm(null)}><DialogContent><DialogHeader><DialogTitle>编辑主题模板</DialogTitle><DialogDescription>用于官网、客户中心和普通登录页面；管理端保持原样。保存后刷新页面即可生效，已打开页面也会在重新聚焦或可见时每30秒更新。</DialogDescription></DialogHeader>{form && <form onSubmit={save} className="space-y-4"><Label>模板<select className="mt-1 w-full rounded border px-3 py-2" value={form.template} onChange={e => setForm({ ...form, template: e.target.value })}><option value="default">默认 · 中性背景与标准圆角</option><option value="classic">经典 · 暖色背景与方角</option><option value="minimal">极简 · 白色背景与小圆角</option></select></Label><Label>主色<Input type="text" pattern="#[0-9A-Fa-f]{6}" value={form.primary_color} onChange={e => setForm({ ...form, primary_color: e.target.value })} /></Label><Label>Logo 样式<select className="mt-1 w-full rounded border px-3 py-2" value={form.logo_variant} onChange={e => setForm({ ...form, logo_variant: e.target.value })}><option value="full">完整 Logo 与站名</option><option value="compact">紧凑 Logo 与站名</option><option value="text">仅站名</option></select></Label><Label>页脚文案<textarea className="mt-1 min-h-24 w-full rounded border p-3" value={form.footer_text} onChange={e => setForm({ ...form, footer_text: e.target.value })} /></Label><Button type="submit" disabled={busy}><Save size={16} />{busy ? "保存中" : "保存"}</Button></form>}</DialogContent></Dialog></div></Workspace>;
}

export function SiteContentCrudView({ kind }: { kind: Kind }) {
  const managePermission = kind === "groups" ? "downloads.manage" : "content.manage";
  const [query, setQuery] = useState({ q: "", active: "", offset: 0, limit: 25 });
  const resource = useResource<PageResult>(`${endpoint[kind]}?q=${encodeURIComponent(query.q)}&active=${query.active}&offset=${query.offset}&limit=${query.limit}`, false);
  const [form, setForm] = useState<Row | null>(null); const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  function begin(item?: Row) { setError(""); setForm(item ? { ...item } : empty(kind)); }
  async function save(event: React.FormEvent) { event.preventDefault(); if (!form || busy) return; setBusy(true); setError(""); const id = form.id; const body = siteContentInput(kind, form); try { await api(`${endpoint[kind]}${id ? `/${id}` : ""}`, { method: id ? (kind === "groups" ? "PUT" : "PATCH") : "POST", headers: { "Idempotency-Key": `${kind}-${id || "new"}-${crypto.randomUUID()}` }, body: JSON.stringify(body) }); setForm(null); resource.refresh(); } catch (e) { setError((e as Error).message); } finally { setBusy(false); } }
  async function remove(item: Row) { if (busy || !window.confirm(`确认删除“${item.label || item.name}”吗？`)) return; setBusy(true); setError(""); try { await api(`${endpoint[kind]}/${item.id}`, { method: "DELETE", headers: { "Idempotency-Key": `${kind}-delete-${item.id}-${crypto.randomUUID()}` }, body: JSON.stringify(siteContentInput(kind, item, true)) }); resource.refresh(); } catch (e) { setError((e as Error).message); } finally { setBusy(false); } }
  const heading = titles[kind]; const nameKey = kind === "fields" || kind === "navigation" ? "label" : "name";
  const allowed = useAdminPermission(kind === "groups" ? "downloads.view" : "content.view");
  const identity = (item: Row) => <div className="space-y-1 break-words"><p className="font-medium">{item[nameKey] || item.field_key}</p><p className="text-xs text-muted-foreground">#{item.id}{kind === "fields" && ` · ${item.field_key}`}</p></div>;
  const detail = (item: Row) => <div className="max-w-md space-y-1 break-all">{kind === "fields" ? item.field_type : kind === "navigation" ? item.href : kind === "links" ? <><p>{item.url}</p><p className="text-xs text-muted-foreground">{item.description || "无说明"}</p></> : item.description || "无说明"}</div>;
  const state = (item: Row) => <Badge variant="outline">{item.active ? "启用" : "停用"}</Badge>;
  const actions = (item: Row) => <PermissionGate permission={managePermission}><div className="flex flex-wrap gap-2"><Button size="sm" variant="outline" disabled={busy} aria-label={`编辑 ${item[nameKey] || item.field_key}`} onClick={() => begin(item)}><Pencil size={14} />编辑</Button><Button size="icon" variant="ghost" disabled={busy} title="删除" aria-label={`删除 ${item[nameKey] || item.field_key}`} onClick={() => remove(item)}><Trash2 size={16} /></Button></div></PermissionGate>;
  const columns: ResourceColumn<Row>[] = [
    { id: "name", header: "名称", render: identity, className: "min-w-40 max-w-72 whitespace-normal" },
    { id: "detail", header: kind === "fields" ? "类型" : kind === "navigation" || kind === "links" ? "链接" : "说明", render: detail, className: "min-w-48 whitespace-normal" },
    { id: "state", header: "状态", render: state },
    { id: "sort", header: "排序", render: item => item.sort_order },
    { id: "version", header: "版本", render: item => item.version },
    { id: "actions", header: "操作", render: actions },
  ];
  return <Workspace title={heading} admin><AdminListPage title={heading} description={kind === "groups" ? "管理下载中心共用的文件分类。" : "管理官网内容与展示配置。"}
    actions={<><Button variant="outline" disabled={resource.isLoading} onClick={resource.refresh}><RefreshCw />刷新</Button><PermissionGate permission={managePermission}><Button onClick={() => begin()}><Plus size={16} />新增</Button></PermissionGate></>}
    filters={<div className="flex flex-wrap items-center gap-3"><Input className="w-full sm:max-w-xs" aria-label="关键词" placeholder={`搜索${heading}`} value={query.q} onChange={e => setQuery({ ...query, q: e.target.value, offset: 0 })} /><select aria-label="状态" className="h-11 rounded-md border border-input bg-background px-3 text-sm md:h-9" value={query.active} onChange={e => setQuery({ ...query, active: e.target.value, offset: 0 })}><option value="">全部状态</option><option value="true">启用</option><option value="false">停用</option></select></div>}
    feedback={error && <p role="alert" className="text-sm text-destructive">{error}</p>}>
    <AdminListRegion title={`${heading}列表`} allowed={allowed} resource={resource} retry={resource.refresh} summary={resource.data && `共 ${resource.data.total} 条`}
      pagination={data => <AdminPagination total={data.total} page={Math.floor(query.offset / query.limit)} pageSize={query.limit} onPageChange={page => setQuery({ ...query, offset: page * query.limit })} />}>
      {data => <ResourceTable items={data.items} rowKey={item => item.id} columns={columns} ariaLabel={`${heading}列表`} className="min-w-0" empty="当前条件下暂无记录"
        renderMobile={item => <article className="space-y-4 p-4"><div className="flex items-start justify-between gap-3">{identity(item)}{state(item)}</div>{detail(item)}<p className="text-xs text-muted-foreground">排序 {item.sort_order} · 版本 {item.version}</p>{actions(item)}</article>} />}
    </AdminListRegion>
  </AdminListPage><Dialog open={!!form} onOpenChange={open => !open && setForm(null)}><DialogContent className="max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>{form?.id ? "编辑" : "新增"}{heading}</DialogTitle><DialogDescription>字段带 * 的内容必须填写。</DialogDescription></DialogHeader>{form && <form onSubmit={save} className="space-y-4"><Label>{kind === "fields" ? "标识 *" : "名称 *"}<Input required value={kind === "fields" ? form.field_key : kind === "navigation" ? form.label : form.name} onChange={e => setForm({ ...form, ...(kind === "fields" ? { field_key: e.target.value } : kind === "navigation" ? { label: e.target.value } : { name: e.target.value }) })} /></Label>{kind === "fields" && <Label>字段名称 *<Input required maxLength={120} value={form.label} onChange={e => setForm({ ...form, label: e.target.value })} /></Label>}{kind === "fields" && <Label>类型<select className="mt-1 w-full rounded border px-3 py-2" value={form.field_type} onChange={e => setForm({ ...form, field_type: e.target.value })}>{["text", "textarea", "url", "email", "number", "boolean"].map(v => <option key={v}>{v}</option>)}</select></Label>}{kind === "navigation" && <><Label>链接 *<Input required value={form.href} onChange={e => setForm({ ...form, href: e.target.value })} /></Label><Label>图标<Input value={form.icon} onChange={e => setForm({ ...form, icon: e.target.value })} /></Label><label className="flex items-center gap-2 text-sm"><Checkbox checked={!!form.open_new_tab} onCheckedChange={v => setForm({ ...form, open_new_tab: !!v })} />新窗口打开</label></>}{kind === "links" && <><Label>链接 *<Input required type="url" value={form.url} onChange={e => setForm({ ...form, url: e.target.value })} /></Label><Label>Logo 地址<Input type="url" value={form.logo_url} onChange={e => setForm({ ...form, logo_url: e.target.value })} /></Label></>}{kind === "groups" && <p className="text-sm text-muted-foreground">文件分组与下载中心分类共用，文件可在下载中心页面关联。</p>}{kind !== "navigation" && <Label>说明<textarea className="mt-1 min-h-20 w-full rounded border p-3" value={form.description || ""} onChange={e => setForm({ ...form, description: e.target.value })} /></Label>}<Label>排序<Input type="number" value={form.sort_order} onChange={e => setForm({ ...form, sort_order: Number(e.target.value) })} /></Label><label className="flex items-center gap-2 text-sm"><Checkbox checked={!!form.active} onCheckedChange={v => setForm({ ...form, active: !!v })} />启用</label><Button type="submit" disabled={busy}><Save size={16} />{busy ? "保存中" : "保存"}</Button></form>}</DialogContent></Dialog></Workspace>;
}
