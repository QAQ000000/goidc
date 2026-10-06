"use client";

import { useState } from "react";
import { Plus, Pencil, Trash2, Save } from "lucide-react";
import { api, type TicketDepartment } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace } from "./workspace";
import { Loading, Failure, Status } from "./common";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Checkbox } from "./ui/checkbox";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "./ui/dialog";
import { Table, TableHeader, TableHead, TableRow, TableBody, TableCell } from "./ui/table";

type Form = { id?: number; name: string; slug: string; active: boolean; customer_visible: boolean; sort_order: number; expected_version: number };
export function TicketDepartmentsView() {
  const { data, error, refresh } = useResource<TicketDepartment[]>("/admin/ticket-departments");
  const [form, setForm] = useState<Form | null>(null);
  const [deleting, setDeleting] = useState<TicketDepartment | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [notice, setNotice] = useState("");
  function begin() { setMessage(""); setNotice(""); }
  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault(); if (!form || busy) return;
    setBusy(true); setMessage("");
    try {
      const payload = form.id ? { name: form.name, active: form.active, customer_visible: form.customer_visible, sort_order: form.sort_order, expected_version: form.expected_version } : { name: form.name, slug: form.slug, customer_visible: form.customer_visible, sort_order: form.sort_order };
      await api(form.id ? `/admin/ticket-departments/${form.id}` : "/admin/ticket-departments", { method: form.id ? "PATCH" : "POST", body: JSON.stringify(payload) });
      setForm(null); refresh(); setNotice("部门已保存");
    } catch (e) { setMessage((e as Error).message); } finally { setBusy(false); }
  }
  async function remove() {
    if (!deleting || busy) return;
    setBusy(true); setMessage("");
    try {
      await api(`/admin/ticket-departments/${deleting.id}`, { method: "DELETE", body: JSON.stringify({ expected_version: deleting.version }) });
      setDeleting(null); refresh(); setNotice("部门已删除");
    } catch (e) { setMessage((e as Error).message); } finally { setBusy(false); }
  }
  return <Workspace title="工单部门" admin>
    <div className="flex items-center justify-between gap-4 mb-6"><h1 className="page-title">工单部门</h1><Button onClick={() => { begin(); setForm({ name: "", slug: "", active: true, customer_visible: true, sort_order: 0, expected_version: 0 }); }}><Plus />新增部门</Button></div>
    {notice && <p role="status" className="text-sm text-emerald-700 mb-4">{notice}</p>}
    {error ? <Failure error={error} retry={refresh} /> : !data ? <Loading /> : <div className="data-surface overflow-x-auto"><Table><TableHeader><TableRow><TableHead>部门</TableHead><TableHead>状态</TableHead><TableHead>排序</TableHead><TableHead>工单</TableHead><TableHead>授权管理员</TableHead><TableHead className="text-right">操作</TableHead></TableRow></TableHeader><TableBody>{data.map(item => <TableRow key={item.id}><TableCell className="whitespace-normal break-all max-w-72"><div className="font-medium">{item.name}</div><div className="text-xs text-muted-foreground">{item.slug}</div></TableCell><TableCell><Status value={item.active ? "active" : "disabled"} />{item.customer_visible === false && <p className="text-xs text-muted-foreground mt-1">客户不可见</p>}</TableCell><TableCell>{item.sort_order}</TableCell><TableCell>{item.ticket_count}</TableCell><TableCell>{item.member_count}</TableCell><TableCell><div className="flex justify-end gap-1"><Button variant="ghost" size="icon-sm" aria-label={`编辑部门 ${item.name}`} title="编辑部门" onClick={() => { begin(); setForm({ id: item.id, name: item.name, slug: item.slug, active: item.active === true, customer_visible: item.customer_visible !== false, sort_order: item.sort_order ?? 0, expected_version: item.version ?? 1 }); }}><Pencil /></Button><Button variant="ghost" size="icon-sm" title="删除部门" aria-label={`删除部门 ${item.name}`} disabled={(item.ticket_count ?? 0) > 0 || (item.member_count ?? 0) > 0} onClick={() => { begin(); setDeleting(item); }}><Trash2 /></Button></div></TableCell></TableRow>)}{data.length === 0 && <TableRow><TableCell colSpan={6} className="text-center py-10 text-muted-foreground">暂无部门</TableCell></TableRow>}</TableBody></Table></div>}
    <Dialog open={form !== null} onOpenChange={open => { if (!open && !busy) setForm(null); }}><DialogContent className="grid-cols-[minmax(0,1fr)] max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>{form?.id ? "编辑部门" : "新增部门"}</DialogTitle><DialogDescription>工单部门资料</DialogDescription></DialogHeader>{form && <form onSubmit={save}><fieldset disabled={busy} className="space-y-4"><div className="space-y-2"><Label htmlFor="department-name">部门名称</Label><Input id="department-name" value={form.name} onChange={e => setForm({ ...form, name: e.target.value })} required maxLength={255} /></div><div className="space-y-2"><Label htmlFor="department-slug">标识</Label><Input id="department-slug" value={form.slug} onChange={e => setForm({ ...form, slug: e.target.value })} required disabled={!!form.id} pattern="[a-z](?:[a-z0-9]|-){0,119}" maxLength={120} /></div><div className="space-y-2"><Label htmlFor="department-sort">排序</Label><Input id="department-sort" type="number" value={form.sort_order} onChange={e => setForm({ ...form, sort_order: Number(e.target.value) })} min={-100000} max={100000} required /></div>{form.id && <label className="flex items-center gap-2 text-sm"><Checkbox checked={form.active} onCheckedChange={checked => setForm({ ...form, active: checked === true })} />启用部门</label>}<label className="flex items-center gap-2 text-sm"><Checkbox checked={form.customer_visible} onCheckedChange={checked => setForm({ ...form, customer_visible: checked === true })} />向客户显示部门及已有工单</label><p className="text-xs text-muted-foreground">取消显示后，客户无法读取此部门的工单或附件；管理员按原部门授权继续处理。</p>{message && <p role="alert" className="text-destructive text-sm">{message}</p>}<div className="flex justify-end"><Button><Save />{busy ? "保存中" : "保存部门"}</Button></div></fieldset></form>}</DialogContent></Dialog>
    <Dialog open={deleting !== null} onOpenChange={open => { if (!open && !busy) setDeleting(null); }}><DialogContent className="grid-cols-[minmax(0,1fr)]"><DialogHeader><DialogTitle>删除部门</DialogTitle><DialogDescription className="break-all">{deleting?.name}</DialogDescription></DialogHeader>{message && <p role="alert" className="text-destructive text-sm">{message}</p>}<div className="flex justify-end gap-2"><Button variant="outline" disabled={busy} onClick={() => setDeleting(null)}>取消</Button><Button variant="destructive" disabled={busy} onClick={() => void remove()}><Trash2 />删除</Button></div></DialogContent></Dialog>
  </Workspace>;
}
