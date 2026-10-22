"use client";

import { useState } from "react";
import { Plus, Pencil, Save, Trash2, RefreshCw } from "lucide-react";
import {
  api,
  type TicketReplyPreset,
  type TicketReplyPresetCategory,
} from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import {
  Workspace,
  PermissionGate,
  PermissionFields,
  useAdminPermission,
} from "./workspace";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "./ui/select";
import { AdminListPage, AdminListRegion } from "./admin-list-page";
import { ResourceTable, type ResourceColumn } from "./resource-table";
import { Badge } from "./ui/badge";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "./ui/dialog";


export function TicketPresetsView() {
  return (
    <Workspace title="预设回复" admin>
      <PresetManager />
    </Workspace>
  );
}
function PresetManager() {
  const manage = useAdminPermission("ticket_presets.manage");
  const allowed = useAdminPermission("ticket_presets.view");
  const categoryResource = useResource<TicketReplyPresetCategory[]>(allowed ? "/admin/ticket-reply-preset-categories" : null, false);
  const presetResource = useResource<TicketReplyPreset[]>(allowed ? "/admin/ticket-reply-presets" : null, false);
  const { data: categories, error: categoryError, refresh: refreshCategories } = categoryResource;
  const { data: presets, error: presetError, refresh: refreshPresets } = presetResource;
  const [categoryOpen, setCategoryOpen] = useState(false);
  const [presetOpen, setPresetOpen] = useState(false);
  const [categoryName, setCategoryName] = useState("");
  const [categorySort, setCategorySort] = useState(0);
  const [categoryActive, setCategoryActive] = useState(true);
  const [editingCategory, setEditingCategory] =
    useState<TicketReplyPresetCategory | null>(null);
  const [presetCategory, setPresetCategory] = useState("none");
  const [presetTitle, setPresetTitle] = useState("");
  const [presetBody, setPresetBody] = useState("");
  const [presetActive, setPresetActive] = useState(true);
  const [editingPreset, setEditingPreset] = useState<TicketReplyPreset | null>(
    null,
  );
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  async function createCategory(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!manage || busy || !categoryName.trim()) return;
    setBusy(true);
    setMessage("");
    try {
      await api(
        `/admin/ticket-reply-preset-categories${editingCategory ? `/${editingCategory.id}` : ""}`,
        {
          method: editingCategory ? "PATCH" : "POST",
          body: JSON.stringify({
            name: categoryName,
            sort_order: categorySort,
            ...(editingCategory ? { active: categoryActive } : {}),
          }),
        },
      );
      setCategoryOpen(false);
      setCategoryName("");
      setCategorySort(0);
      setCategoryActive(true);
      setEditingCategory(null);
      refreshCategories();
      refreshPresets();
    } catch (error) {
      setMessage((error as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function createPreset(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (
      !manage ||
      busy ||
      presetCategory === "none" ||
      !presetTitle.trim() ||
      !presetBody.trim()
    )
      return;
    setBusy(true);
    setMessage("");
    try {
      await api(
        `/admin/ticket-reply-presets${editingPreset ? `/${editingPreset.id}` : ""}`,
        {
          method: editingPreset ? "PATCH" : "POST",
          body: JSON.stringify({
            ...(editingPreset?.category_id === Number(presetCategory)
              ? {}
              : { category_id: Number(presetCategory) }),
            title: presetTitle,
            body: presetBody,
            ...(editingPreset ? { active: presetActive } : {}),
          }),
        },
      );
      setPresetOpen(false);
      setEditingPreset(null);
      setPresetTitle("");
      setPresetBody("");
      setPresetCategory("none");
      setPresetActive(true);
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
      if (editingPreset?.id === id) {
        setEditingPreset(null);
        setPresetTitle("");
        setPresetBody("");
        setPresetCategory("none");
        setPresetActive(true);
      }
      refreshPresets();
    } catch (error) {
      setMessage((error as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function removeCategory(id: number) {
    if (!manage || busy || !window.confirm("删除此预设回复分类？")) return;
    setBusy(true);
    setMessage("");
    try {
      await api(`/admin/ticket-reply-preset-categories/${id}`, {
        method: "DELETE",
      });
      if (editingCategory?.id === id) {
        setEditingCategory(null);
        setCategoryName("");
        setCategorySort(0);
        setCategoryActive(true);
      }
      refreshCategories();
    } catch (error) {
      setMessage((error as Error).message);
    } finally {
      setBusy(false);
    }
  }
  const categoryActions = (category: TicketReplyPresetCategory) => <PermissionGate permission="ticket_presets.manage"><div className="flex gap-1"><Button type="button" variant="ghost" size="icon-sm" aria-label={`编辑分类 ${category.name}`} title="编辑分类" disabled={busy} onClick={() => { setCategoryOpen(true); setEditingCategory(category); setCategoryName(category.name); setCategorySort(category.sort_order); setCategoryActive(category.active); setMessage(""); }}><Pencil /></Button><Button type="button" variant="ghost" size="icon-sm" aria-label={`删除分类 ${category.name}`} title="删除分类" disabled={busy || !presets || !!presetError || presets.some(item => item.category_id === category.id)} onClick={() => void removeCategory(category.id)}><Trash2 /></Button></div></PermissionGate>;
  const presetActions = (preset: TicketReplyPreset) => <PermissionGate permission="ticket_presets.manage"><div className="flex gap-1"><Button type="button" variant="ghost" size="icon-sm" title="编辑预设" aria-label={`编辑 ${preset.title}`} disabled={busy || !categories || !!categoryError} onClick={() => { setPresetOpen(true); setEditingPreset(preset); setPresetTitle(preset.title); setPresetBody(preset.body); setPresetCategory(String(preset.category_id)); setPresetActive(preset.active); setMessage(""); }}><Pencil /></Button><Button variant="ghost" size="icon-sm" title="删除预设" aria-label={`删除 ${preset.title}`} disabled={busy} onClick={() => void removePreset(preset.id)}><Trash2 /></Button></div></PermissionGate>;
  const state = (active: boolean) => <Badge variant="outline">{active ? "启用" : "停用"}</Badge>;
  const body = (preset: TicketReplyPreset) => <details className="max-w-xl text-sm"><summary className="cursor-pointer text-muted-foreground">查看完整回复</summary><p className="mt-2 whitespace-pre-wrap break-all">{preset.body}</p></details>;
  const categoryColumns: ResourceColumn<TicketReplyPresetCategory>[] = [
    { id: "name", header: "分类", render: category => <div className="space-y-1 break-all"><p className="font-medium">{category.name}</p><p className="text-xs text-muted-foreground">#{category.id} · 排序 {category.sort_order}</p></div>, className: "max-w-60 whitespace-normal" },
    { id: "state", header: "状态", render: category => state(category.active) },
    { id: "actions", header: "操作", render: categoryActions },
  ];
  const presetColumns: ResourceColumn<TicketReplyPreset>[] = [
    { id: "title", header: "回复标题", render: preset => <div className="space-y-1 break-all"><p className="font-medium">{preset.title}</p><p className="text-xs text-muted-foreground">#{preset.id} · {preset.category_name}</p></div>, className: "max-w-64 whitespace-normal" },
    { id: "state", header: "状态", render: preset => state(preset.active) },
    { id: "body", header: "回复内容", render: body, className: "max-w-xl whitespace-normal" },
    { id: "actions", header: "操作", render: presetActions },
  ];
  return <AdminListPage title="预设回复" description="维护客服常用回复，分类与回复分别加载；预设文字不会直接发送给客户。"
    actions={<><Button variant="outline" disabled={categoryResource.isLoading || presetResource.isLoading} onClick={() => { refreshCategories(); refreshPresets(); }}><RefreshCw />刷新</Button>{manage && <><Button variant="outline" onClick={() => { setEditingCategory(null); setCategoryName(""); setCategorySort(0); setCategoryActive(true); setMessage(""); setCategoryOpen(true); }}><Plus />新增分类</Button><Button disabled={!categories || !!categoryError} onClick={() => { setEditingPreset(null); setPresetCategory("none"); setPresetTitle(""); setPresetBody(""); setPresetActive(true); setMessage(""); setPresetOpen(true); }}><Plus />新增预设回复</Button></>}</>}
    feedback={message && !categoryOpen && !presetOpen && <p role="alert" className="break-all text-sm text-destructive">{message}</p>}>
    <div className="grid items-start gap-5 xl:grid-cols-[minmax(0,0.8fr)_minmax(0,1.2fr)]">
      <div className="min-w-0 space-y-5">

        <AdminListRegion title="预设回复分类列表" allowed={allowed} resource={categoryResource} retry={refreshCategories} summary={categories && `共 ${categories.length} 个分类`}>
          {data => <ResourceTable items={data} rowKey={category => category.id} columns={categoryColumns} ariaLabel="预设回复分类列表" className="min-w-0" empty="暂无预设回复分类"
            renderMobile={category => <article className="space-y-3 p-4"><div className="flex items-start justify-between gap-3"><p className="min-w-0 break-all font-medium">{category.name}</p>{state(category.active)}</div><p className="text-xs text-muted-foreground">#{category.id} · 排序 {category.sort_order}</p>{categoryActions(category)}</article>} />}
        </AdminListRegion>
      </div>
      <div className="min-w-0 space-y-5">

        <AdminListRegion title="预设回复列表" allowed={allowed} resource={presetResource} retry={refreshPresets} summary={presets && `共 ${presets.length} 条回复`}>
          {data => <ResourceTable items={data} rowKey={preset => preset.id} columns={presetColumns} ariaLabel="预设回复列表" className="min-w-0" empty="暂无预设回复"
            renderMobile={preset => <article className="space-y-3 p-4"><div className="flex items-start justify-between gap-3"><p className="min-w-0 break-all font-medium">{preset.title}</p>{state(preset.active)}</div><p className="break-all text-xs text-muted-foreground">#{preset.id} · {preset.category_name}</p>{body(preset)}{presetActions(preset)}</article>} />}
        </AdminListRegion>
      </div>
    </div>
    <Dialog open={categoryOpen && manage} onOpenChange={open => { if (!busy) setCategoryOpen(open); }}><DialogContent className="max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>{editingCategory ? "编辑分类" : "新增分类"}</DialogTitle><DialogDescription>分类用于组织预设文字；已有回复的分类不可删除。</DialogDescription></DialogHeader>
{message && <p role="alert" className="break-all text-sm text-destructive">{message}</p>}

          <PermissionGate permission="ticket_presets.manage">
            <form
              className="mt-4 space-y-4"
              onSubmit={(event) => void createCategory(event)}
            >
              <Input
                required
                disabled={busy}
                aria-label="分类名称"
                value={categoryName}
                onChange={(event) => setCategoryName(event.target.value)}
                placeholder="例如：账单问题"
                maxLength={120}
              />
              <label className="grid gap-2 text-sm">
                排序
                <Input
                  aria-label="分类排序"
                  type="number"
                  min={-100000}
                  max={100000}
                  required
                  disabled={busy}
                  value={categorySort}
                  onChange={(event) =>
                    setCategorySort(Number(event.target.value))
                  }
                />
              </label>
              {editingCategory && (
                <label className="flex items-center gap-2 text-sm">
                  <Checkbox
                    disabled={busy}
                    checked={categoryActive}
                    onCheckedChange={(checked) =>
                      setCategoryActive(checked === true)
                    }
                  />
                  启用分类
                </label>
              )}
              <div className="flex flex-wrap gap-2 border-t pt-4">
                <Button disabled={busy || categoryName.trim() === ""}>
                  {editingCategory ? <Save /> : <Plus />}
                  {editingCategory ? "保存分类" : "添加分类"}
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  disabled={busy}
                  onClick={() => {
                    setCategoryName(editingCategory?.name ?? "");
                    setCategorySort(editingCategory?.sort_order ?? 0);
                    setCategoryActive(editingCategory?.active ?? true);
                    setMessage("");
                  }}
                >
                  取消分类更改
                </Button>
                {editingCategory && (
                  <Button
                    type="button"
                    variant="ghost"
                    disabled={busy}
                    onClick={() => {
                      setEditingCategory(null);
                      setCategoryName("");
                      setCategorySort(0);
                      setCategoryActive(true);
                      setMessage("");
                    }}
                  >
                    返回新增分类
                  </Button>
                )}
              </div>
            </form>
          </PermissionGate>
    </DialogContent></Dialog>
    <Dialog open={presetOpen && manage} onOpenChange={open => { if (!busy) setPresetOpen(open); }}><DialogContent className="max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>{editingPreset ? "编辑预设回复" : "新增预设回复"}</DialogTitle><DialogDescription>维护客服可插入工单草稿的文字，保存不会直接发送。</DialogDescription></DialogHeader>
{message && <p role="alert" className="break-all text-sm text-destructive">{message}</p>}

          <PermissionFields permission="ticket_presets.manage">
            <form
              className="mt-4 space-y-4"
              onSubmit={(event) => void createPreset(event)}
            >
              <fieldset disabled={busy || !categories || !!categoryError} className="space-y-4">
                <Select
                  value={presetCategory}
                  onValueChange={setPresetCategory}
                >
                  <SelectTrigger aria-label="预设回复分类">
                    <SelectValue placeholder="选择分类" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="none">选择分类</SelectItem>
                    {(categories ?? [])
                      .filter(
                        (category) =>
                          category.active ||
                          category.id === editingPreset?.category_id,
                      )
                      .map((category) => (
                        <SelectItem
                          key={category.id}
                          value={String(category.id)}
                          disabled={!category.active}
                        >
                          {category.name}
                          {!category.active && "（停用）"}
                        </SelectItem>
                      ))}
                  </SelectContent>
                </Select>
                <Input
                  aria-label="回复标题"
                  value={presetTitle}
                  onChange={(event) => setPresetTitle(event.target.value)}
                  placeholder="回复标题"
                  maxLength={240}
                />
                <Textarea
                  aria-label="回复内容"
                  value={presetBody}
                  onChange={(event) => setPresetBody(event.target.value)}
                  placeholder="回复内容"
                  rows={5}
                  maxLength={10000}
                />
                {editingPreset && (
                  <label className="flex items-center gap-2 text-sm">
                    <Checkbox
                      checked={presetActive}
                      onCheckedChange={(checked) =>
                        setPresetActive(checked === true)
                      }
                    />
                    启用预设回复
                  </label>
                )}
                <div className="flex flex-wrap gap-3 border-t pt-4">
                  <Button
                    disabled={
                      busy ||
                      presetCategory === "none" ||
                      !presetTitle.trim() ||
                      !presetBody.trim()
                    }
                  >
                    <Save />
                    保存预设
                  </Button>
                  <Button
                    type="button"
                    variant="outline"
                    disabled={busy}
                    onClick={() => {
                      setPresetTitle(editingPreset?.title ?? "");
                      setPresetBody(editingPreset?.body ?? "");
                      setPresetActive(editingPreset?.active ?? true);
                      setPresetCategory(
                        editingPreset
                          ? String(editingPreset.category_id)
                          : "none",
                      );
                      setMessage("");
                    }}
                  >
                    取消更改
                  </Button>
                  {editingPreset && (
                    <Button
                      type="button"
                      variant="ghost"
                      disabled={busy}
                      onClick={() => {
                        setEditingPreset(null);
                        setPresetTitle("");
                        setPresetBody("");
                        setPresetCategory("none");
                        setPresetActive(true);
                        setMessage("");
                      }}
                    >
                      返回
                    </Button>
                  )}
                </div>
              </fieldset>
            </form>
          </PermissionFields>
    </DialogContent></Dialog>
  </AdminListPage>;
}
