"use client";

import { useState } from "react";
import { Plus, Pencil, Save, Trash2 } from "lucide-react";
import { api, type TicketReplyPreset, type TicketReplyPresetCategory } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace, PermissionGate, PermissionFields, useAdminPermission } from "./workspace";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Failure, Loading } from "./common";
import { Table, TableHeader, TableHead, TableBody, TableRow, TableCell } from "./ui/table";

export function TicketPresetsView() {
  return <Workspace title="预设回复" admin><PresetManager /></Workspace>;
}
function PresetManager() {
  const manage = useAdminPermission("ticket_presets.manage");
  const { data: categories, error: categoryError, refresh: refreshCategories } = useResource<TicketReplyPresetCategory[]>("/admin/ticket-reply-preset-categories");
  const { data: presets, error: presetError, refresh: refreshPresets } = useResource<TicketReplyPreset[]>("/admin/ticket-reply-presets");
  const [categoryName, setCategoryName] = useState("");
  const [presetCategory, setPresetCategory] = useState("none");
  const [presetTitle, setPresetTitle] = useState("");
  const [presetBody, setPresetBody] = useState("");
  const [editingPreset, setEditingPreset] = useState<TicketReplyPreset | null>(null);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  async function createCategory(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!manage || busy || !categoryName.trim()) return;
    setBusy(true);
    setMessage("");
    try {
      await api("/admin/ticket-reply-preset-categories", { method: "POST", body: JSON.stringify({ name: categoryName }) });
      setCategoryName("");
      refreshCategories();
    } catch (error) {
      setMessage((error as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function createPreset(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!manage || busy || presetCategory === "none" || !presetTitle.trim() || !presetBody.trim()) return;
    setBusy(true);
    setMessage("");
    try {
      await api(`/admin/ticket-reply-presets${editingPreset ? `/${editingPreset.id}` : ""}`, { method: editingPreset ? "PATCH" : "POST", body: JSON.stringify({ category_id: Number(presetCategory), title: presetTitle, body: presetBody }) });
      setEditingPreset(null);
      setPresetTitle("");
      setPresetBody("");
      refreshPresets();
    } catch (error) {
      setMessage((error as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function removePreset(id: number) {
    if (!manage || busy || !window.confirm("删除此预设回复？")) return;
    setBusy(true);
    setMessage("");
    try {
      await api(`/admin/ticket-reply-presets/${id}`, { method: "DELETE" });
      refreshPresets();
    } catch (error) {
      setMessage((error as Error).message);
    } finally {
      setBusy(false);
    }
  }
  if (categoryError || presetError) return <Failure error={categoryError ?? presetError!} retry={() => { refreshCategories(); refreshPresets(); }} />;
  if (!categories || !presets) return <Loading />;
  return <><div className="flex flex-wrap items-end justify-between gap-4 mb-5"><div><h1 className="page-title">预设回复</h1><p className="text-muted-foreground mt-2">维护管理员常用的工单回复内容</p></div></div>{message && <p role="alert" className="text-destructive mb-4">{message}</p>}<div className="grid xl:grid-cols-[minmax(0,0.8fr)_minmax(0,1.2fr)] gap-5"><section className="border-y py-4"><h2 className="font-semibold">新增分类</h2><PermissionGate permission="ticket_presets.manage"><form className="mt-4 flex gap-3" onSubmit={event => void createCategory(event)}><Input disabled={busy} aria-label="分类名称" value={categoryName} onChange={event => setCategoryName(event.target.value)} placeholder="例如：账单问题" maxLength={120} /><Button disabled={busy || categoryName.trim() === ""}><Plus />添加</Button></form></PermissionGate><div className="mt-4 space-y-2">{categories.map(category => <div key={category.id} className="flex items-center justify-between border-t py-3"><span>{category.name}</span><span className="text-xs text-muted-foreground">{category.active ? "启用" : "停用"}</span></div>)}</div></section><section className="border-y py-4"><h2 className="font-semibold">{editingPreset ? "编辑预设回复" : "新增预设回复"}</h2><PermissionFields permission="ticket_presets.manage"><form className="mt-4 space-y-4" onSubmit={event => void createPreset(event)}><fieldset disabled={busy} className="space-y-4"><Select value={presetCategory} onValueChange={setPresetCategory}><SelectTrigger aria-label="预设回复分类"><SelectValue placeholder="选择分类" /></SelectTrigger><SelectContent><SelectItem value="none">选择分类</SelectItem>{categories.filter(category => category.active).map(category => <SelectItem key={category.id} value={String(category.id)}>{category.name}</SelectItem>)}</SelectContent></Select><Input aria-label="回复标题" value={presetTitle} onChange={event => setPresetTitle(event.target.value)} placeholder="回复标题" maxLength={240} /><Textarea aria-label="回复内容" value={presetBody} onChange={event => setPresetBody(event.target.value)} placeholder="回复内容" rows={5} maxLength={10000} /><div className="flex flex-wrap gap-3 border-t pt-4"><Button disabled={busy || presetCategory === "none" || !presetTitle.trim() || !presetBody.trim()}><Save />保存预设</Button><Button type="button" variant="outline" disabled={busy} onClick={() => { setPresetTitle(editingPreset?.title ?? ""); setPresetBody(editingPreset?.body ?? ""); setPresetCategory(editingPreset ? String(editingPreset.category_id) : "none"); setMessage(""); }}>取消更改</Button>{editingPreset&&<Button type="button" variant="ghost" disabled={busy} onClick={()=>{setEditingPreset(null);setPresetTitle("");setPresetBody("");setPresetCategory("none");setMessage("");}}>返回</Button>}</div></fieldset></form></PermissionFields></section></div><section className="mt-5"><h2 className="font-semibold mb-4">现有预设</h2><div className="overflow-x-auto border-y"><Table><TableHeader><TableRow><TableHead>ID</TableHead><TableHead>标题</TableHead><TableHead>分类</TableHead><TableHead>回复内容</TableHead><TableHead>操作</TableHead></TableRow></TableHeader><TableBody>{presets.map(preset=><TableRow key={preset.id}><TableCell>{preset.id}</TableCell><TableCell className="max-w-64 whitespace-normal break-all">{preset.title}</TableCell><TableCell>{preset.category_name}</TableCell><TableCell className="max-w-xl whitespace-pre-wrap break-all">{preset.body}</TableCell><TableCell><PermissionGate permission="ticket_presets.manage"><Button type="button" variant="ghost" size="icon-sm" title="编辑预设" aria-label={`编辑 ${preset.title}`} disabled={busy} onClick={()=>{setEditingPreset(preset);setPresetTitle(preset.title);setPresetBody(preset.body);setPresetCategory(String(preset.category_id));setMessage("");}}><Pencil /></Button><Button variant="ghost" size="icon-sm" title="删除预设" aria-label={`删除 ${preset.title}`} disabled={busy} onClick={()=>void removePreset(preset.id)}><Trash2 /></Button></PermissionGate></TableCell></TableRow>)}{presets.length===0&&<TableRow><TableCell colSpan={5} className="py-10 text-center text-muted-foreground">暂无预设回复</TableCell></TableRow>}</TableBody></Table></div></section></>;
}
