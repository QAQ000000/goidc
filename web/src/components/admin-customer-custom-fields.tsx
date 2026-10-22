"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { Pencil, Plus, RefreshCw, Trash2 } from "lucide-react";
import { ApiError } from "@/lib/api";
import { AdminPagination } from "./admin-pagination";
import { AdminListPage, AdminListRegion } from "./admin-list-page";
import { ResourceTable, type ResourceColumn } from "./resource-table";
import { Badge } from "./ui/badge";
import { Workspace, useAdminIdentity, useAdminPermission } from "./workspace";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "./ui/dialog";
import { emptyFieldForm, fieldForm, fieldTypes, formPayload, useCustomerFieldAdminState, type CustomerAdminField, type FieldDraft, type FieldForm, type FieldRequest } from "./customer-field-admin-state";

type Result = { items: CustomerAdminField[]; total: number; limit: number; offset: number };
export function AdminCustomerCustomFieldsView() {
  const actor = useAdminIdentity(), manage = useAdminPermission("customers.manage");
  return <Workspace title="客户自定义字段" admin><FieldManager key={`${actor?.id ?? "none"}:${manage}`} actor={actor?.id ?? 0} manage={manage} /></Workspace>;
}
function FieldManager({ actor, manage }: { actor: number; manage: boolean }) {
  const router = useRouter(), params = useSearchParams();
  const q = params.get("q") ?? "", type = params.get("field_type") ?? "", active = params.get("active") ?? "";
  const rawPage = Number(params.get("page") ?? 1), page = Number.isSafeInteger(rawPage) && rawPage > 0 && Number.isSafeInteger((rawPage - 1) * 100) ? rawPage - 1 : 0;
  const size = [25,50,100].includes(Number(params.get("limit"))) ? Number(params.get("limit")) : 25;
  const query = new URLSearchParams({ q, field_type: type, active, limit: String(size), offset: String(page * size) });
  const path = `/admin/customer-fields?${query}`;
  const state = useCustomerFieldAdminState(actor, manage), { read, hideIdentity } = state;
  const [revision, setRevision] = useState(0);
  const [list, setList] = useState<{ path: string; revision: number; data: Result | null; error: Error | null } | null>(null);
  const [search, setSearch] = useState(q);
  const [draft, setDraft] = useState<FieldDraft | null>(null);
  const [latest, setLatest] = useState<FieldForm | null>(null);
  const [detailMessage, setDetailMessage] = useState("");
  const [reading, setReading] = useState(false);
  const [deleting, setDeleting] = useState<CustomerAdminField | null>(null);
  const detailRequest = useRef<AbortController | null>(null);
  const refresh = useCallback(() => setRevision(value => value + 1), []);
  useEffect(() => { queueMicrotask(() => setSearch(q)); }, [q]);
  useEffect(() => {
    const focus = () => { hideIdentity(); refresh(); };
    const visibility = () => { if (document.visibilityState === "hidden") hideIdentity(); else focus(); };
    window.addEventListener("focus", focus); document.addEventListener("visibilitychange", visibility);
    return () => { window.removeEventListener("focus", focus); document.removeEventListener("visibilitychange", visibility); detailRequest.current?.abort(); };
  }, [hideIdentity, refresh]);
  useEffect(() => {
    if (!manage || !actor) return;
    const controller = new AbortController();
    void read<Result>(path, controller.signal).then(data => { if (!controller.signal.aborted) setList({ path, revision, data, error: null }); }).catch((error: Error) => { if (!controller.signal.aborted) setList({ path, revision, data: null, error }); });
    return () => controller.abort();
  }, [path, revision, manage, actor, read]);
  const current = state.identityReady && list?.path === path && list.revision === revision ? list : null;
  const dirty = !!draft && JSON.stringify(draft.form) !== JSON.stringify(draft.original);
  const locked = !manage || !state.ready || !state.identityReady || state.busy || reading || !!state.pending;
  function change(values: Record<string, string>, reset = true) {
    const next = new URLSearchParams(params.toString());
    for (const [key,value] of Object.entries(values)) { if (value) next.set(key,value); else next.delete(key); }
    if (reset) next.delete("page");
    router.push(`/admin/customer-fields${next.size ? `?${next}` : ""}`, { scroll: false });
  }
  function update(patch: Partial<FieldForm>) {
    if (!draft || locked) return;
    const next = { ...draft, form: { ...draft.form, ...patch } };
    setDraft(next); state.persistDraft(String(next.form.id ?? "new"), next);
  }
  async function close() {
    if (state.busy || reading) return;
    try { await state.verify(); } catch (e) { state.setError((e as Error).message); return; }
    if (state.pending) { setDraft(null); return; }
    if (dirty && !window.confirm("放弃尚未提交的字段更改？")) return;
    if (draft && state.drafts[String(draft.form.id ?? "new")] && !await state.replaceDraft(String(draft.form.id ?? "new"), null)) return;
    setDraft(null); setLatest(null); setDetailMessage("");
  }
  async function detail(id: number) {
    detailRequest.current?.abort(); const controller = new AbortController(); detailRequest.current = controller;
    setReading(true); setDetailMessage("");
    try { return await read<CustomerAdminField>(`/admin/customer-fields/${id}`, controller.signal); }
    finally { if (!controller.signal.aborted) setReading(false); }
  }
  async function edit(item: CustomerAdminField) {
    if (locked || draft) return;
    if (state.drafts[String(item.id)]) { state.setError("此字段有本员工未提交草稿，请先选择恢复或丢弃该草稿。"); return; }
    try { const form = fieldForm(await detail(item.id)); setDraft({ form, original: form }); setLatest(null); }
    catch (e) { state.setError((e as Error).message); }
  }
  async function restore(key: string) {
    if (locked || draft) return;
    const saved = state.drafts[key];
    if (!saved) return;
    setDraft(saved); setLatest(null);
    if (saved.form.id) {
      try {
        const next = fieldForm(await detail(saved.form.id));
        setLatest(next);
        if (next.version !== saved.original.version) setDetailMessage("服务端字段已变化。输入草稿保留，请选择保留输入并采用最新版本，或以最新对象替换草稿。");
      } catch (e) { setDetailMessage(e instanceof ApiError && e.status === 404 ? "该字段已不存在，草稿保留。请核实后关闭或联系支持。" : (e as Error).message); }
    }
  }
  async function reread() {
    if (state.busy || reading) return;
    const request = state.pending?.request;
    const id = request?.method !== "POST" && request ? Number(request.path.split("/").at(-1)) : draft?.form.id;
    try {
      if (id) { const next = fieldForm(await detail(id)); setLatest(next); setDetailMessage("已读取最新字段，请核对后明确选择如何处理保留的输入。"); }
      else { await read<Result>(path); setDetailMessage("已重新读取当前字段列表，请核对标识冲突等拒绝原因。"); }
      state.setReviewed(true); refresh();
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) { setLatest(null); setDetailMessage("该字段已不存在。此事实不替代原请求回执；原请求恢复入口仍保留。"); state.setReviewed(true); }
      else { state.setError((e as Error).message); state.setReviewed(false); }
    }
  }
  async function applyLatest(keep: boolean) {
    if (!draft || !latest || locked || !window.confirm(keep ? "保留输入并采用最新版本？提交前请重新核对字段规则。" : "以最新服务端对象替换当前草稿？")) return;
    const form = keep ? { ...draft.form, id: latest.id, field_key: latest.field_key, version: latest.version } : latest;
    const next = { form, original: latest }; if (!await state.replaceDraft(String(form.id), JSON.stringify(form) === JSON.stringify(latest) ? null : next)) return; setDraft(next); setDetailMessage(""); setLatest(null);
  }
  async function save(event: React.FormEvent) {
    event.preventDefault(); if (locked || !draft || !dirty || detailMessage) return;
    try {
      const payload = formPayload(draft.form);
      const request: FieldRequest = { path: draft.form.id ? `/admin/customer-fields/${draft.form.id}` : "/admin/customer-fields", method: draft.form.id ? "PATCH" : "POST", key: `web-customer-field-${crypto.randomUUID()}`, body: JSON.stringify(payload), draftKey: String(draft.form.id ?? "new"), label: draft.form.label };
      if (await state.send(request)) { setDraft(null); setLatest(null); }
    } catch (e) { state.setError((e as Error).message); }
    finally { refresh(); }
  }
  async function prepareDelete(item: CustomerAdminField) {
    if (locked || draft) return;
    try { setDeleting(await detail(item.id)); }
    catch (e) { state.setError((e as Error).message); }
  }
  async function remove() {
    if (!deleting || locked) return;
    const request: FieldRequest = { path: `/admin/customer-fields/${deleting.id}`, method: "DELETE", key: `web-customer-field-${crypto.randomUUID()}`, body: JSON.stringify({ expected_version: deleting.version }), draftKey: String(deleting.id), label: deleting.label };
    await state.send(request); setDeleting(null); refresh();
  }
  const notices = <>
    {state.storageError && <div role="alert" className="text-sm text-destructive space-y-2"><p>{state.storageError}</p><Button type="button" size="sm" variant="outline" disabled={state.busy} onClick={state.reloadStorage}>重新读取本员工恢复资料</Button></div>}
    {state.error && <p role="alert" className="text-sm text-destructive break-words">{state.error}</p>}
    {state.receipt && <p role="status" className="text-sm text-emerald-700">{state.receipt}</p>}
    {state.pending && <div className="rounded border p-3 space-y-3 text-sm" role="status"><p>原{state.pending.request.method === "DELETE" ? "删除" : state.pending.request.method === "POST" ? "新增" : "修改"}请求待核实：{state.identityReady ? state.pending.request.label : "原员工请求（核实身份后显示目标）"}。即使目标已删除、停用或不在当前页，原请求仍保留；刷新不会自动提交。</p>{state.pending.rejected && <p>明确拒绝：{state.identityReady ? state.pending.reason : "请核实原员工身份后查看"}。请显式读取最新事实，不能自动换键。</p>}<div className="flex flex-wrap gap-2"><Button type="button" size="sm" disabled={state.busy || !manage || !state.ready || reading || state.pending.rejected && !state.reviewed} onClick={async () => { if (state.pending && await state.send(state.pending.request,true)) { setDraft(null); setDeleting(null); } refresh(); }}>以原请求核实结果</Button><Button type="button" size="sm" variant="outline" disabled={state.busy || reading} onClick={() => void reread()}>读取最新字段事实</Button>{state.pending.rejected && state.reviewed && <Button type="button" size="sm" variant="outline" disabled={!state.identityReady || state.busy || reading} onClick={() => void state.endRejected()}>已核对，结束被拒绝请求</Button>}</div></div>}
  </>;
  if (!manage) return <p role="alert">需要客户管理权限才能配置字段；原员工空间的草稿与请求保留。</p>;
  const actions = (item: CustomerAdminField) => <div className="flex flex-wrap gap-1"><Button size="sm" variant="outline" aria-label={`编辑${item.label}`} disabled={locked || !!draft} onClick={() => void edit(item)}><Pencil />编辑</Button><Button size="sm" variant="ghost" aria-label={`删除${item.label}`} disabled={locked || !!draft} onClick={() => void prepareDelete(item)}><Trash2 />删除</Button></div>;
  const registration = (item: CustomerAdminField) => <div className="space-y-1"><p>{item.show_on_register ? "注册标志：是" : "注册标志：否"}</p><p className="text-xs text-muted-foreground">{!item.active ? "不公开：已停用" : item.admin_only ? "不公开：仅管理员可见" : item.show_on_register ? "实际公开" : "不在注册展示"}</p></div>;
  const columns: ResourceColumn<CustomerAdminField>[] = [
    { id: "field", header: "字段名称 / 标识", render: item => <div className="max-w-sm space-y-1"><p className="break-words font-medium">{item.label}</p><p className="break-all font-mono text-xs text-muted-foreground">{item.field_key}</p>{item.description && <p className="whitespace-pre-wrap break-words text-xs text-muted-foreground">{item.description}</p>}</div> },
    { id: "type", header: "类型", render: item => fieldTypes[item.field_type] },
    { id: "registration", header: "注册展示", render: registration },
    { id: "required", header: "必填 / 可见范围", render: item => <div className="space-y-1"><p>{item.required ? "必填" : "选填"}</p><p className="text-xs text-muted-foreground">{item.admin_only ? "仅管理员可见" : "客户可见"}</p></div> },
    { id: "active", header: "状态", render: item => <Badge variant={item.active ? "secondary" : "outline"}>{item.active ? "启用" : "停用"}</Badge> },
    { id: "sort", header: "排序", render: item => item.sort_order },
    { id: "actions", header: "操作", render: actions },
  ];
  const matching = list?.path === path && list.revision === revision ? list : null;
  const resource = { data: current?.data ?? null, error: matching?.error ?? null, isLoading: !matching || !current && !matching.error };
  return <AdminListPage title="客户自定义字段" description="定义客户资料字段；仅启用、注册显示且非仅管理员可见的字段公开于注册。"
    actions={<><Button type="button" variant="outline" aria-label="刷新字段列表" onClick={refresh}><RefreshCw />刷新</Button><Button disabled={locked || !!draft || !!state.drafts.new} onClick={() => { const form = emptyFieldForm(); setDraft({ form, original: form }); setLatest(null); setDetailMessage(""); state.setError(""); }}><Plus />添加字段</Button></>}
    filters={<form className="flex flex-wrap items-end gap-3 rounded border border-border bg-card p-4" onSubmit={event => { event.preventDefault(); if (Array.from(search.trim()).length > 100) { state.setError("关键词最多100个Unicode字符"); return; } change({q:search.trim()}); }}><label className="grid gap-2 text-sm min-w-0 w-full sm:w-72">关键词<Input placeholder="字段名、标识或描述（字面搜索）" value={search} onChange={event => setSearch(event.target.value)} /></label><label className="grid gap-2 text-sm">字段类型<select className="h-9 rounded border border-input bg-background px-2" value={type} onChange={event => change({field_type:event.target.value})}><option value="">全部类型</option>{Object.entries(fieldTypes).map(([key,label]) => <option key={key} value={key}>{label}</option>)}</select></label><label className="grid gap-2 text-sm">启用状态<select className="h-9 rounded border border-input bg-background px-2" value={active} onChange={event => change({active:event.target.value})}><option value="">全部状态</option><option value="true">启用</option><option value="false">停用</option></select></label><Button variant="outline">搜索</Button><Button type="button" variant="outline" onClick={() => router.push("/admin/customer-fields",{scroll:false})}>重置筛选</Button></form>}>
    {notices}
    {!state.ready && !state.storageError && <p role="status" className="text-sm">正在检查本员工的草稿与原请求，完成前不可写入。</p>}
    {state.identityReady && Object.keys(state.drafts).length > 0 && <section className="border rounded p-3 space-y-2 text-sm"><h2 className="font-medium">本员工未提交草稿</h2>{Object.entries(state.drafts).map(([key,value]) => <div key={key} className="flex flex-wrap items-center gap-2"><span className="break-words">{key === "new" ? "新增" : `字段 #${key}`} · {value.form.label || "未命名"}</span><Button size="sm" variant="outline" disabled={locked || !!draft} onClick={() => void restore(key)}>恢复草稿</Button><Button size="sm" variant="outline" disabled={locked || !!draft} onClick={() => { if (window.confirm("确认丢弃此未提交草稿？不会清理任何原请求。")) void state.replaceDraft(key,null); }}>丢弃草稿</Button></div>)}</section>}
    <AdminListRegion title="客户字段定义列表" allowed={manage} resource={resource} retry={refresh}
      pagination={data => <AdminPagination total={data.total} page={page} pageSize={size} onPageChange={next => change({page:String(next+1)},false)} onPageSizeChange={next => change({limit:String(next)})} />}>
      {data => <ResourceTable items={data.items} rowKey={item => item.id} columns={columns} ariaLabel="客户字段定义列表" className="min-w-0" tableClassName="min-w-[950px]"
        empty={data.total ? <>当前页已超出记录范围。<Button variant="link" onClick={() => change({page:"1"},false)}>回到第一页</Button></> : "暂无符合条件的客户自定义字段"}
        renderMobile={item => <article className="space-y-3 p-4"><dl className="grid gap-3 text-sm">{columns.map(column => <div key={column.id} className="min-w-0"><dt className="text-xs text-muted-foreground">{column.header}</dt><dd className="break-words">{column.render(item)}</dd></div>)}</dl></article>} />}
    </AdminListRegion>
    <Dialog open={!!draft} onOpenChange={value => { if (!value) close(); }}><DialogContent showCloseButton={!state.busy && !reading} className="max-h-[85dvh] overflow-y-auto"><DialogTitle>{draft?.form.id ? "编辑客户自定义字段" : "添加客户自定义字段"}</DialogTitle><DialogDescription>标识创建后不可修改。仅管理员可见优先于注册显示；内部必填不要求注册用户填写。</DialogDescription>{!state.identityReady ? <div className="space-y-2"><p>正在核实原员工身份，暂不展示字段草稿；输入与原请求保留。</p><Button variant="outline" onClick={refresh}>重新核实身份</Button></div> : draft && <><div className="space-y-3">{state.error && <p role="alert" className="text-sm text-destructive break-words">{state.error}</p>}{detailMessage && <p role="status" className="text-sm">{detailMessage}</p>}{latest && !state.pending && <div className="flex flex-wrap gap-2"><Button size="sm" variant="outline" onClick={() => applyLatest(true)}>保留输入并采用最新版本</Button><Button size="sm" variant="outline" onClick={() => applyLatest(false)}>用最新字段替换草稿</Button></div>}{draft.form.id && <Button size="sm" variant="outline" disabled={state.busy || reading} onClick={() => void reread()}>读取最新字段事实</Button>}</div><FieldEditor form={draft.form} disabled={locked} dirty={dirty} blocked={!!detailMessage} onChange={update} onSubmit={save} onClose={close} busy={state.busy} pending={!!state.pending} /></>}{state.pending && <p className="text-sm">原请求已保存。关闭弹窗后，可在页面恢复区以原请求核实，关闭不会删除请求。</p>}</DialogContent></Dialog>
    <Dialog open={!!deleting} onOpenChange={value => { if (!value && !state.busy) setDeleting(null); }}><DialogContent showCloseButton={!state.busy} className="max-h-[85dvh] overflow-y-auto"><DialogTitle>确认删除字段定义</DialogTitle><DialogDescription>{state.identityReady ? <>删除“{deleting?.label}”（{deleting?.field_key}，版本{deleting?.version}）只移除定义，不清理客户已有资料值；活动表单不再展示该定义。此操作需要确认。</> : "请先核实原员工身份；暂不展示删除目标，原资料保留。"}</DialogDescription>{state.error && <p role="alert" className="text-sm text-destructive">{state.error}</p>}<div className="flex flex-wrap gap-2"><Button variant="outline" disabled={state.busy} onClick={() => setDeleting(null)}>取消删除</Button><Button variant="destructive" disabled={locked} onClick={() => void remove()}>确认删除定义</Button></div></DialogContent></Dialog>
  </AdminListPage>;
}
function FieldEditor({ form, disabled, dirty, blocked, onChange, onSubmit, onClose, busy, pending }: { form: FieldForm; disabled: boolean; dirty: boolean; blocked: boolean; onChange: (value: Partial<FieldForm>) => void; onSubmit: (event: React.FormEvent) => void; onClose: () => void; busy: boolean; pending: boolean }) {
  return <form onSubmit={onSubmit} className="space-y-4"><fieldset disabled={disabled} className="space-y-4"><div className="grid sm:grid-cols-2 gap-4"><label className="grid gap-2 text-sm">字段标识<Input required pattern="[a-z][a-z0-9_]{1,63}" maxLength={64} disabled={!!form.id} value={form.field_key} onChange={event => onChange({field_key:event.target.value})} /></label><label className="grid gap-2 text-sm">字段名称<Input required value={form.label} onChange={event => onChange({label:event.target.value})} /></label></div><label className="grid gap-2 text-sm">字段类型<select className="h-9 w-full rounded border bg-background px-3" value={form.field_type} onChange={event => onChange({field_type:event.target.value,choicesText:event.target.value === "select" ? form.choicesText : ""})}>{Object.entries(fieldTypes).map(([key,label]) => <option key={key} value={key}>{label}</option>)}</select></label>{form.field_type === "select" && <label className="grid gap-2 text-sm">选项（英文逗号分隔）<Input required value={form.choicesText} onChange={event => onChange({choicesText:event.target.value})} /></label>}<label className="grid gap-2 text-sm">向客户显示的描述<Input value={form.description} onChange={event => onChange({description:event.target.value})} /></label><label className="grid gap-2 text-sm">验证正则（Go regexp，最多500字节）<Input value={form.validation_pattern} onChange={event => onChange({validation_pattern:event.target.value})} /></label><label className="grid gap-2 text-sm">显示排序<Input type="number" min={-100000} max={100000} step={1} value={form.sort_order} onChange={event => onChange({sort_order:Number(event.target.value)})} /></label><div className="grid sm:grid-cols-2 gap-3">{([['required','必填'],['show_on_register','注册时显示'],['admin_only','仅管理员可见'],['active','启用']] as const).map(([key,label]) => <label key={key} className="flex gap-2 items-center text-sm"><input type="checkbox" checked={form[key]} onChange={event => onChange({[key]:event.target.checked})} />{label}</label>)}</div></fieldset><div className="flex flex-wrap gap-2 border-t pt-4"><Button disabled={disabled || !dirty || blocked}>{busy ? "提交中" : "保存字段"}</Button><Button type="button" variant="outline" disabled={busy} onClick={onClose}>{pending ? "关闭，保留原请求" : "取消更改"}</Button></div></form>;
}
