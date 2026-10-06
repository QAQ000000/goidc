"use client";

import { useState } from "react";
import { Plus, Save, Trash2 } from "lucide-react";
import { api, type TicketReplyPreset, type TicketReplyPresetCategory } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace, PermissionGate } from "./workspace";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Failure, Loading } from "./common";

export function TicketPresetsView() {
  const { data: categories, error: categoryError, refresh: refreshCategories } = useResource<TicketReplyPresetCategory[]>("/admin/ticket-reply-preset-categories");
  const { data: presets, error: presetError, refresh: refreshPresets } = useResource<TicketReplyPreset[]>("/admin/ticket-reply-presets");
  const [categoryName, setCategoryName] = useState("");
  const [presetCategory, setPresetCategory] = useState("none");
  const [presetTitle, setPresetTitle] = useState("");
  const [presetBody, setPresetBody] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  async function createCategory(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!categoryName.trim()) return;
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
    if (presetCategory === "none" || !presetTitle.trim() || !presetBody.trim()) return;
    setBusy(true);
    setMessage("");
    try {
      await api("/admin/ticket-reply-presets", { method: "POST", body: JSON.stringify({ category_id: Number(presetCategory), title: presetTitle, body: presetBody }) });
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
    if (!window.confirm("删除此预设回复？")) return;
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
  if (categoryError || presetError) return <Workspace title="预设回复" admin><Failure error={categoryError ?? presetError!} retry={() => { refreshCategories(); refreshPresets(); }} /></Workspace>;
  if (!categories || !presets) return <Workspace title="预设回复" admin><Loading /></Workspace>;
  return <Workspace title="预设回复" admin><div className="flex flex-wrap items-end justify-between gap-4 mb-8"><div><h1 className="page-title">预设回复</h1><p className="text-muted-foreground mt-2">维护管理员常用的工单回复内容</p></div></div>{message && <p role="alert" className="text-destructive mb-4">{message}</p>}<div className="grid xl:grid-cols-[minmax(0,0.8fr)_minmax(0,1.2fr)] gap-6"><section className="data-surface p-6"><h2 className="font-semibold">新增分类</h2><PermissionGate permission="ticket_presets.manage"><form className="mt-4 flex gap-3" onSubmit={event => void createCategory(event)}><Input value={categoryName} onChange={event => setCategoryName(event.target.value)} placeholder="例如：账单问题" maxLength={120} /><Button disabled={busy || categoryName.trim() === ""}><Plus />添加</Button></form></PermissionGate><div className="mt-6 space-y-2">{categories.map(category => <div key={category.id} className="flex items-center justify-between border-t py-3"><span>{category.name}</span><span className="text-xs text-muted-foreground">{category.active ? "启用" : "停用"}</span></div>)}</div></section><section className="data-surface p-6"><h2 className="font-semibold">新增预设回复</h2><form className="mt-4 space-y-4" onSubmit={event => void createPreset(event)}><Select value={presetCategory} onValueChange={setPresetCategory}><SelectTrigger><SelectValue placeholder="选择分类" /></SelectTrigger><SelectContent><SelectItem value="none">选择分类</SelectItem>{categories.filter(category => category.active).map(category => <SelectItem key={category.id} value={String(category.id)}>{category.name}</SelectItem>)}</SelectContent></Select><Input value={presetTitle} onChange={event => setPresetTitle(event.target.value)} placeholder="回复标题" maxLength={240} /><Textarea value={presetBody} onChange={event => setPresetBody(event.target.value)} placeholder="回复内容" rows={5} maxLength={10000} /><Button disabled={busy || presetCategory === "none" || !presetTitle.trim() || !presetBody.trim()}><Save />保存预设</Button></form></section></div><section className="data-surface p-6 mt-6"><h2 className="font-semibold">现有预设</h2><div className="mt-4 space-y-4">{presets.map(preset => <article key={preset.id} className="border-t pt-4"><div className="flex flex-wrap items-start justify-between gap-3"><div><p className="font-medium">{preset.title}</p><p className="text-xs text-muted-foreground mt-1">{preset.category_name}</p></div><PermissionGate permission="ticket_presets.manage"><Button variant="ghost" size="icon-sm" title="删除预设" aria-label={`删除 ${preset.title}`} disabled={busy} onClick={() => void removePreset(preset.id)}><Trash2 /></Button></PermissionGate></div><p className="whitespace-pre-wrap break-words text-sm text-muted-foreground mt-3">{preset.body}</p></article>)}{presets.length === 0 && <p className="text-sm text-muted-foreground">暂无预设回复</p>}</div></section></Workspace>;
}
