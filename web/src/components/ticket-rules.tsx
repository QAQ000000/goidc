"use client";

import { useState } from "react";
import { Pencil, Plus, RefreshCw, Save, Trash2 } from "lucide-react";
import { api } from "@/lib/api";
import { ticketStatusInput } from "@/lib/admin-configuration-inputs";
import { useResource } from "@/hooks/use-resource";
import { AdminPagination } from "./admin-pagination";
import { AdminListPage, AdminListRegion } from "./admin-list-page";
import { ResourceTable, type ResourceColumn } from "./resource-table";
import { Badge } from "./ui/badge";
import { PermissionGate, Workspace, useAdminPermission } from "./workspace";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "./ui/dialog";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Textarea } from "./ui/textarea";

type Page<T> = { items: T[]; total: number; limit: number; offset: number };
type Status = { id: number; slug: string; title: string; sort_order: number; builtin: boolean; active: boolean; version: number };
type Rule = { id: number; department_id: number | null; product_id: number | null; blocked_keywords: string[]; auto_reply: string; active: boolean; version: number };
const idempotencyKey = () => (typeof crypto !== "undefined" ? crypto.randomUUID() : `ticket-${Date.now()}`);

export function TicketStatusesView() {
  const [query, setQuery] = useState({ offset: 0, limit: 20 });
  const resource = useResource<Page<Status>>(`/admin/ticket-statuses?offset=${query.offset}&limit=${query.limit}`, false);
  const [form, setForm] = useState<Status | null>(null);
  const [title, setTitle] = useState(""); const [slug, setSlug] = useState(""); const [sort, setSort] = useState(0); const [active, setActive] = useState(true);
  const [message, setMessage] = useState(""); const [busy, setBusy] = useState(false);
  const openCreate = () => { setForm({ id: 0, slug: "", title: "", sort_order: 0, builtin: false, active: true, version: 0 }); setTitle(""); setSlug(""); setSort(0); setActive(true); setMessage(""); };
  const openEdit = (item: Status) => { setForm(item); setTitle(item.title); setSlug(item.slug); setSort(item.sort_order); setActive(item.active); setMessage(""); };
  async function save(event: React.FormEvent) { event.preventDefault(); if (busy) return; setBusy(true); setMessage(""); try { const body = ticketStatusInput({ id: form?.id, version: form?.version, slug, title, sort_order: Number(sort), active }); await api(form?.id ? `/admin/ticket-statuses/${form.id}` : "/admin/ticket-statuses", { method: form?.id ? "PATCH" : "POST", headers: { "Idempotency-Key": idempotencyKey() }, body: JSON.stringify(body) }); setForm(null); resource.refresh(); } catch (error) { setMessage((error as Error).message); } finally { setBusy(false); } }
  async function remove(item: Status) { if (item.builtin || busy || !window.confirm(`确认删除状态“${item.title}”吗？`)) return; setBusy(true); setMessage(""); try { await api(`/admin/ticket-statuses/${item.id}`, { method: "DELETE", headers: { "Idempotency-Key": idempotencyKey() }, body: JSON.stringify({ expected_version: item.version }) }); resource.refresh(); } catch (error) { setMessage((error as Error).message); } finally { setBusy(false); } }
  const allowed = useAdminPermission("tickets.view");
  const identity = (item: Status) => <div className="space-y-1 break-words"><p className="font-medium">{item.title} {item.builtin && <Badge variant="outline">内置</Badge>}</p><p className="break-all font-mono text-xs text-muted-foreground">#{item.id} · {item.slug}</p></div>;
  const state = (item: Status) => <Badge variant="outline">{item.active ? "启用" : "停用"}</Badge>;
  const actions = (item: Status) => <PermissionGate permission="tickets.manage"><div className="flex gap-1"><Button disabled={busy} size="icon-sm" variant="ghost" title="编辑" aria-label={`编辑${item.title}`} onClick={() => openEdit(item)}><Pencil /></Button>{!item.builtin && <Button disabled={busy} size="icon-sm" variant="ghost" title="删除" aria-label={`删除${item.title}`} onClick={() => void remove(item)}><Trash2 /></Button>}</div></PermissionGate>;
  const columns: ResourceColumn<Status>[] = [
    { id: "name", header: "状态 / 标识", render: identity, className: "min-w-48 whitespace-normal" },
    { id: "sort", header: "排序", render: item => item.sort_order },
    { id: "active", header: "状态", render: state },
    { id: "actions", header: "操作", render: actions },
  ];
  return <Workspace title="工单状态" admin><AdminListPage title="工单状态" description="维护工单状态的展示定义；内置状态不可重命名或删除。"
    actions={<><Button variant="outline" disabled={resource.isLoading} onClick={resource.refresh}><RefreshCw />刷新</Button><PermissionGate permission="tickets.manage"><Button onClick={openCreate}><Plus />添加状态</Button></PermissionGate></>}
    feedback={message && <p role="alert" className="text-sm text-destructive">{message}</p>}>
    <AdminListRegion title="工单状态列表" allowed={allowed} resource={resource} retry={resource.refresh} summary={resource.data && `共 ${resource.data.total} 条`}
      pagination={data => <AdminPagination total={data.total} page={Math.floor(query.offset / query.limit)} pageSize={query.limit} onPageChange={page => setQuery({ ...query, offset: page * query.limit })} onPageSizeChange={limit => setQuery({ limit, offset: 0 })} />}>
      {data => <ResourceTable items={data.items} rowKey={item => item.id} columns={columns} ariaLabel="工单状态列表" className="min-w-0" empty="暂无工单状态"
        renderMobile={item => <article className="space-y-4 p-4"><div className="flex items-start justify-between gap-3">{identity(item)}{state(item)}</div><p className="text-xs text-muted-foreground">排序 {item.sort_order}</p>{actions(item)}</article>} />}
    </AdminListRegion>
  </AdminListPage><Dialog open={form !== null} onOpenChange={open => { if (!open && !busy) setForm(null); }}><DialogContent><DialogHeader><DialogTitle>{form?.id ? "编辑工单状态" : "添加工单状态"}</DialogTitle><DialogDescription>保存会写入版本和审计记录，重复提交不会重复变更。</DialogDescription></DialogHeader>{form && <form onSubmit={save} className="space-y-4"><label className="grid gap-2 text-sm"><Label>状态标识</Label><Input required pattern="[a-z][a-z0-9_\-]{0,63}" disabled={!!form.id} value={slug} onChange={event => setSlug(event.target.value)} /></label><label className="grid gap-2 text-sm"><Label>标题</Label><Input required maxLength={120} disabled={form.builtin} value={title} onChange={event => setTitle(event.target.value)} /></label><label className="grid gap-2 text-sm"><Label>排序</Label><Input type="number" value={sort} onChange={event => setSort(Number(event.target.value))} /></label><label className="flex items-center gap-2 text-sm"><Checkbox checked={active} onCheckedChange={value => setActive(value === true)} />启用</label>{message && <p role="alert" className="text-sm text-destructive">{message}</p>}<div className="flex gap-2 border-t pt-4"><Button disabled={busy}><Save />{busy ? "保存中" : "保存"}</Button><Button type="button" variant="outline" onClick={() => setForm(null)}>取消</Button></div></form>}</DialogContent></Dialog></Workspace>;
}

export function TicketRulesView() {
  const [query, setQuery] = useState({ offset: 0, limit: 20 });
  const resource = useResource<Page<Rule>>(`/admin/ticket-rules?offset=${query.offset}&limit=${query.limit}`, false);
  const [form, setForm] = useState<Rule | null>(null); const [keywords, setKeywords] = useState(""); const [reply, setReply] = useState(""); const [department, setDepartment] = useState(""); const [product, setProduct] = useState(""); const [active, setActive] = useState(true); const [message, setMessage] = useState(""); const [busy, setBusy] = useState(false);
  const openCreate = () => { setForm({ id: 0, department_id: null, product_id: null, blocked_keywords: [], auto_reply: "", active: true, version: 0 }); setKeywords(""); setReply(""); setDepartment(""); setProduct(""); setActive(true); setMessage(""); };
  const openEdit = (item: Rule) => { setForm(item); setKeywords(item.blocked_keywords.join(", ")); setReply(item.auto_reply); setDepartment(item.department_id ? String(item.department_id) : ""); setProduct(item.product_id ? String(item.product_id) : ""); setActive(item.active); setMessage(""); };
  async function save(event: React.FormEvent) { event.preventDefault(); if (busy) return; setBusy(true); setMessage(""); try { const body = { department_id: department ? Number(department) : null, product_id: product ? Number(product) : null, blocked_keywords: keywords.split(",").map(value => value.trim()).filter(Boolean), auto_reply: reply, active, ...(form && form.id ? { expected_version: form.version } : {}) }; await api(form?.id ? `/admin/ticket-rules/${form.id}` : "/admin/ticket-rules", { method: form?.id ? "PATCH" : "POST", headers: { "Idempotency-Key": idempotencyKey() }, body: JSON.stringify(body) }); setForm(null); resource.refresh(); } catch (error) { setMessage((error as Error).message); } finally { setBusy(false); } }
  async function remove(item: Rule) { if (busy || !window.confirm("确认删除这条工单传递规则吗？")) return; setBusy(true); setMessage(""); try { await api(`/admin/ticket-rules/${item.id}`, { method: "DELETE", headers: { "Idempotency-Key": idempotencyKey() }, body: JSON.stringify({ expected_version: item.version }) }); resource.refresh(); } catch (error) { setMessage((error as Error).message); } finally { setBusy(false); } }
   const allowed = useAdminPermission("tickets.view");
  const scope = (item: Rule) => <div className="space-y-1 text-sm"><p className="font-medium">规则 #{item.id}</p><p>部门：{item.department_id ?? "全部"} · 商品：{item.product_id ?? "全部"}</p></div>;
  const keywordSummary = (item: Rule) => <p className="break-words">{item.blocked_keywords.join("、") || "无屏蔽关键字"}</p>;
  const response = (item: Rule) => item.auto_reply ? <details className="max-w-md text-sm"><summary className="cursor-pointer text-primary">查看自动回复</summary><p className="mt-2 whitespace-pre-wrap break-words">{item.auto_reply}</p></details> : <span className="text-muted-foreground">无自动回复</span>;
  const state = (item: Rule) => <Badge variant="outline">{item.active ? "启用" : "停用"}</Badge>;
  const actions = (item: Rule) => <PermissionGate permission="tickets.manage"><div className="flex gap-1"><Button disabled={busy} size="icon-sm" variant="ghost" title="编辑" aria-label={`编辑规则${item.id}`} onClick={() => openEdit(item)}><Pencil /></Button><Button disabled={busy} size="icon-sm" variant="ghost" title="删除" aria-label={`删除规则${item.id}`} onClick={() => void remove(item)}><Trash2 /></Button></div></PermissionGate>;
  const columns: ResourceColumn<Rule>[] = [
    { id: "scope", header: "规则 / 匹配范围", render: scope, className: "min-w-48" },
    { id: "keywords", header: "屏蔽关键字", render: keywordSummary, className: "min-w-40 max-w-64 whitespace-normal" },
    { id: "reply", header: "自动回复", render: response, className: "min-w-40 whitespace-normal" },
    { id: "active", header: "状态", render: state },
    { id: "actions", header: "操作", render: actions },
  ];
  return <Workspace title="工单传递" admin><AdminListPage title="工单传递" description="维护部门、商品匹配条件、屏蔽词及自动回复；空条件表示全部。"
    actions={<><Button variant="outline" disabled={resource.isLoading} onClick={resource.refresh}><RefreshCw />刷新</Button><PermissionGate permission="tickets.manage"><Button onClick={openCreate}><Plus />添加规则</Button></PermissionGate></>}
    feedback={message && <p role="alert" className="text-sm text-destructive">{message}</p>}>
    <AdminListRegion title="工单传递规则列表" allowed={allowed} resource={resource} retry={resource.refresh} summary={resource.data && `共 ${resource.data.total} 条`}
      pagination={data => <AdminPagination total={data.total} page={Math.floor(query.offset / query.limit)} pageSize={query.limit} onPageChange={page => setQuery({ ...query, offset: page * query.limit })} onPageSizeChange={limit => setQuery({ limit, offset: 0 })} />}>
      {data => <ResourceTable items={data.items} rowKey={item => item.id} columns={columns} ariaLabel="工单传递规则列表" className="min-w-0" empty="暂无传递规则"
        renderMobile={item => <article className="space-y-4 p-4"><div className="flex items-start justify-between gap-3">{scope(item)}{state(item)}</div>{keywordSummary(item)}{response(item)}{actions(item)}</article>} />}
    </AdminListRegion>
  </AdminListPage><Dialog open={form !== null} onOpenChange={open => { if (!open && !busy) setForm(null); }}><DialogContent><DialogHeader><DialogTitle>{form?.id ? "编辑工单传递规则" : "添加工单传递规则"}</DialogTitle><DialogDescription>关联编号可留空表示全部部门或产品，保存时会校验引用存在。</DialogDescription></DialogHeader>{form && <form onSubmit={save} className="space-y-4"><div className="grid gap-4 sm:grid-cols-2"><label className="grid gap-2 text-sm"><Label>部门编号</Label><Input inputMode="numeric" value={department} onChange={event => setDepartment(event.target.value)} /></label><label className="grid gap-2 text-sm"><Label>产品编号</Label><Input inputMode="numeric" value={product} onChange={event => setProduct(event.target.value)} /></label></div><label className="grid gap-2 text-sm"><Label>屏蔽关键字</Label><Input placeholder="关键词用逗号分隔" value={keywords} onChange={event => setKeywords(event.target.value)} /></label><label className="grid gap-2 text-sm"><Label>自动回复</Label><Textarea maxLength={10000} value={reply} onChange={event => setReply(event.target.value)} /></label><label className="flex items-center gap-2 text-sm"><Checkbox checked={active} onCheckedChange={value => setActive(value === true)} />启用</label>{message && <p role="alert" className="text-sm text-destructive">{message}</p>}<div className="flex gap-2 border-t pt-4"><Button disabled={busy}><Save />{busy ? "保存中" : "保存"}</Button><Button type="button" variant="outline" onClick={() => setForm(null)}>取消</Button></div></form>}</DialogContent></Dialog></Workspace>;
}
