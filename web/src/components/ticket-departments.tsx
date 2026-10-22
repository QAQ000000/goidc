"use client";

import { useState } from "react";
import { Plus, Pencil, Trash2, Save, RefreshCw } from "lucide-react";
import { api, type TicketDepartment } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace, useAdminPermission } from "./workspace";
import { Status } from "./common";
import { AdminListPage, AdminListRegion } from "./admin-list-page";
import { ResourceTable, type ResourceColumn } from "./resource-table";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Checkbox } from "./ui/checkbox";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "./ui/dialog";

type Form = { id?: number; name: string; slug: string; active: boolean; customer_visible: boolean; sort_order: number; expected_version: number };
export function TicketDepartmentsView() {
  const allowed = useAdminPermission("tickets.view");
  const manage = useAdminPermission("@super");
  const resource = useResource<TicketDepartment[]>(allowed ? "/admin/ticket-departments" : null, false);
  const { refresh } = resource;
  const [form, setForm] = useState<Form | null>(null);
  const [original, setOriginal] = useState<Form | null>(null);
  if (form && !original) setOriginal(form);
  if (!form && original) setOriginal(null);
  const [deleting, setDeleting] = useState<TicketDepartment | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [notice, setNotice] = useState("");
  function begin() { setMessage(""); setNotice(""); }
  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault(); if (!form || busy || !manage) return;
    setBusy(true); setMessage("");
    try {
      const payload = form.id ? { name: form.name, active: form.active, customer_visible: form.customer_visible, sort_order: form.sort_order, expected_version: form.expected_version } : { name: form.name, slug: form.slug, customer_visible: form.customer_visible, sort_order: form.sort_order };
      await api(form.id ? `/admin/ticket-departments/${form.id}` : "/admin/ticket-departments", { method: form.id ? "PATCH" : "POST", body: JSON.stringify(payload) });
      setForm(null); refresh(); setNotice("部门已保存");
    } catch (e) { setMessage((e as Error).message); } finally { setBusy(false); }
  }
  async function remove() {
    if (!deleting || busy || !manage) return;
    setBusy(true); setMessage("");
    try {
      await api(`/admin/ticket-departments/${deleting.id}`, { method: "DELETE", body: JSON.stringify({ expected_version: deleting.version }) });
      setDeleting(null); refresh(); setNotice("部门已删除");
    } catch (e) { setMessage((e as Error).message); } finally { setBusy(false); }
  }
  const identity = (item: TicketDepartment) => <div className="space-y-1 break-all"><p className="font-medium">{item.name}</p><p className="text-xs text-muted-foreground">{item.slug} · #{item.id} · 版本 {item.version}</p></div>;
  const state = (item: TicketDepartment) => <div className="space-y-1"><Status value={item.active ? "active" : "disabled"} /><p className="text-xs text-muted-foreground">{item.customer_visible === false ? "客户不可见" : "客户可见"}</p></div>;
  const actions = (item: TicketDepartment) => manage && <div className="flex gap-1"><Button variant="ghost" size="icon-sm" aria-label={`编辑部门 ${item.name}`} title="编辑部门" disabled={busy} onClick={() => { begin(); setForm({ id: item.id, name: item.name, slug: item.slug, active: item.active === true, customer_visible: item.customer_visible !== false, sort_order: item.sort_order ?? 0, expected_version: item.version ?? 1 }); }}><Pencil /></Button><Button variant="ghost" size="icon-sm" title="删除部门" aria-label={`删除部门 ${item.name}`} disabled={busy || (item.ticket_count ?? 0) > 0 || (item.member_count ?? 0) > 0} onClick={() => { begin(); setDeleting(item); }}><Trash2 /></Button></div>;
  const columns: ResourceColumn<TicketDepartment>[] = [
    { id: "name", header: "部门", render: identity, className: "max-w-72 whitespace-normal" },
    { id: "state", header: "状态 / 可见性", render: state },
    { id: "sort", header: "排序", render: item => item.sort_order },
    { id: "tickets", header: "工单", render: item => item.ticket_count },
    { id: "members", header: "授权管理员", render: item => item.member_count },
    { id: "actions", header: "操作", render: actions },
  ];
  return <Workspace title="工单部门" admin><AdminListPage title="工单部门" description="管理接收工单的部门；启用状态与客户可见性分别控制。"
    actions={<><Button variant="outline" disabled={resource.isLoading} onClick={refresh}><RefreshCw />刷新</Button>{manage && <Button onClick={() => { begin(); setForm({ name: "", slug: "", active: true, customer_visible: true, sort_order: 0, expected_version: 0 }); }}><Plus />新增部门</Button>}</>}
    feedback={notice && <p role="status" className="text-sm text-muted-foreground">{notice}</p>}>
    <AdminListRegion title="工单部门列表" allowed={allowed} resource={resource} retry={refresh} summary={resource.data && `共 ${resource.data.length} 个部门`}>
      {data => <ResourceTable items={data} rowKey={item => item.id} columns={columns} className="min-w-0" ariaLabel="工单部门列表" empty="暂无部门"
        renderMobile={item => <article className="space-y-4 p-4"><div className="flex items-start justify-between gap-3">{identity(item)}{state(item)}</div><p className="text-sm text-muted-foreground">排序 {item.sort_order} · 工单 {item.ticket_count} · 授权管理员 {item.member_count}</p>{actions(item)}</article>} />}
    </AdminListRegion>
  </AdminListPage>
    <Dialog open={form !== null} onOpenChange={open => { if (!open && !busy) setForm(null); }}><DialogContent className="grid-cols-[minmax(0,1fr)] max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>{form?.id ? "编辑部门" : "新增部门"}</DialogTitle><DialogDescription>工单部门资料</DialogDescription></DialogHeader>{form && <form onSubmit={save}><fieldset disabled={busy || !manage} className="space-y-4"><div className="space-y-2"><Label htmlFor="department-name">部门名称</Label><Input id="department-name" value={form.name} onChange={e => setForm({ ...form, name: e.target.value })} required maxLength={255} /></div><div className="space-y-2"><Label htmlFor="department-slug">标识</Label><Input id="department-slug" value={form.slug} onChange={e => setForm({ ...form, slug: e.target.value })} required disabled={!!form.id} pattern="[a-z](?:[a-z0-9]|-){0,119}" maxLength={120} /></div><div className="space-y-2"><Label htmlFor="department-sort">排序</Label><Input id="department-sort" type="number" value={form.sort_order} onChange={e => setForm({ ...form, sort_order: Number(e.target.value) })} min={-100000} max={100000} required /></div>{form.id && <label className="flex items-center gap-2 text-sm"><Checkbox checked={form.active} onCheckedChange={checked => setForm({ ...form, active: checked === true })} />启用部门</label>}<label className="flex items-center gap-2 text-sm"><Checkbox checked={form.customer_visible} onCheckedChange={checked => setForm({ ...form, customer_visible: checked === true })} />向客户显示部门及已有工单</label><p className="text-xs text-muted-foreground">取消显示后，客户无法读取此部门的工单或附件；管理员按原部门授权继续处理。</p>{message && <p role="alert" className="text-destructive text-sm">{message}</p>}<div className="flex flex-wrap gap-3 border-t pt-4"><Button><Save />{busy ? "保存中" : "保存部门"}</Button><Button type="button" variant="outline" onClick={()=>{setForm(original);setMessage("");}}>取消更改</Button><Button type="button" variant="ghost" onClick={()=>setForm(null)}>返回</Button></div></fieldset></form>}</DialogContent></Dialog>
    <Dialog open={deleting !== null} onOpenChange={open => { if (!open && !busy) setDeleting(null); }}><DialogContent className="grid-cols-[minmax(0,1fr)]"><DialogHeader><DialogTitle>删除部门</DialogTitle><DialogDescription className="break-all">{deleting?.name}</DialogDescription></DialogHeader>{message && <p role="alert" className="text-destructive text-sm">{message}</p>}<div className="flex justify-end gap-2"><Button variant="outline" disabled={busy} onClick={() => setDeleting(null)}>取消</Button><Button variant="destructive" disabled={busy} onClick={() => void remove()}><Trash2 />删除</Button></div></DialogContent></Dialog>
  </Workspace>;
}
