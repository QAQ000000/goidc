"use client";

import { useState } from "react";
import { useSearchParams } from "next/navigation";
import { Plus, Pencil, Trash2, RefreshCw } from "lucide-react";
import { api, type CustomerGroup } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Checkbox } from "./ui/checkbox";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "./ui/dialog";
import { AdminListPage, AdminListRegion } from "./admin-list-page";
import { ResourceTable, type ResourceColumn } from "./resource-table";
import { Badge } from "./ui/badge";
import { Workspace, PermissionGate, useAdminPermission } from "./workspace";
import { AdminPagination } from "./admin-pagination";
import {
  CustomerGroupTabs,
  CustomerProductDiscountsView,
  CustomerProductGroupsView,
} from "./admin-customer-product-groups";

export function AdminCustomerGroupsView() {
  const searchParams = useSearchParams();
  const tab = searchParams.get("tab");
  if (tab === "products") return <CustomerProductGroupsView />;
  if (tab === "discounts") return <CustomerProductDiscountsView />;

  return <AdminCustomerGroupsListView />;
}

function AdminCustomerGroupsListView() {
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState(50);
  const [search, setSearch] = useState("");
  const endpoint = `/admin/customer-groups?q=${encodeURIComponent(search.trim())}&limit=${pageSize}&offset=${page * pageSize}`;
  const allowed = useAdminPermission("customer_groups.view");
  const resource = useResource<{ items: CustomerGroup[]; total: number }>(allowed ? endpoint : null, false);
  const { refresh } = resource;
  const manage = useAdminPermission("customer_groups.manage");

  const [dialogOpen, setDialogOpen] = useState(false);
  const [editingGroup, setEditingGroup] = useState<CustomerGroup | null>(null);

  // Form states
  const [name, setName] = useState("");
  const [color, setColor] = useState("#64748b");
  const [discount, setDiscount] = useState("0");
  const [separateInvoices, setSeparateInvoices] = useState(false);
  const [suspendDeleteExempt, setSuspendDeleteExempt] = useState(false);
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState("");
  const [actionError, setActionError] = useState("");

  function openCreate() {
    setEditingGroup(null);
    setName("");
    setColor("#64748b");
    setDiscount("0");
    setSeparateInvoices(false);
    setSuspendDeleteExempt(false);
    setFormError("");
    setDialogOpen(true);
  }

  function openEdit(group: CustomerGroup) {
    setEditingGroup(group);
    setName(group.name);
    setColor(group.color || "#64748b");
    setDiscount(String(group.discount_percent ?? 0));
    setSeparateInvoices(Boolean(group.separate_invoices));
    setSuspendDeleteExempt(Boolean(group.suspend_delete_exempt));
    setFormError("");
    setDialogOpen(true);
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!manage || busy) return;
    setBusy(true);
    setFormError("");

    try {
      if (editingGroup) {
        await api(`/admin/customer-groups/${editingGroup.id}`, {
          method: "PATCH",
          headers: { "Idempotency-Key": `customer-group-update-${editingGroup.id}-${editingGroup.version}-${crypto.randomUUID()}` },
          body: JSON.stringify({
            name,
            color,
            discount_percent: Number(discount),
            separate_invoices: separateInvoices,
            suspend_delete_exempt: suspendDeleteExempt,
            expected_version: editingGroup.version,
          }),
        });
      } else {
        await api("/admin/customer-groups", {
          method: "POST",
          headers: { "Idempotency-Key": `customer-group-create-${crypto.randomUUID()}` },
          body: JSON.stringify({
            name,
            color,
            discount_percent: Number(discount),
            separate_invoices: separateInvoices,
            suspend_delete_exempt: suspendDeleteExempt,
          }),
        });
      }
      setDialogOpen(false);
      refresh();
    } catch (err) {
      setFormError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function handleDelete(group: CustomerGroup) {
    if (!manage || busy) return;
    if (group.member_count > 0) {
      setActionError("该分组下仍有客户，无法直接删除。请先将客户移至其他分组。");
      return;
    }
    if (!window.confirm(`确定要删除客户分组 "${group.name}" 吗？`)) return;

    setBusy(true); setActionError("");
    try {
      await api(`/admin/customer-groups/${group.id}`, {
        method: "DELETE",
        headers: { "Idempotency-Key": `customer-group-delete-${group.id}-${group.version}-${crypto.randomUUID()}` },
        body: JSON.stringify({ expected_version: group.version }),
      });
      refresh();
    } catch (err) {
      setActionError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const identity = (group: CustomerGroup) => <div className="space-y-1"><p className="flex items-center gap-2 font-medium"><span aria-hidden="true" className="size-3 shrink-0 rounded-full border border-border" style={{ backgroundColor: group.color || "#64748b" }} /><span className="break-all">{group.name}</span></p><p className="text-xs text-muted-foreground">#{group.id} · 版本 {group.version} · {group.color || "未设颜色"}</p></div>;
  const rules = (group: CustomerGroup) => <div className="flex flex-wrap gap-1"><Badge variant="outline">{group.separate_invoices ? "拆分账单" : "合并账单"}</Badge>{group.suspend_delete_exempt && <Badge variant="outline">豁免逾期暂停 / 删除</Badge>}</div>;
  const actions = (group: CustomerGroup) => <PermissionGate permission="customer_groups.manage"><div className="flex gap-1"><Button size="icon-sm" variant="ghost" disabled={busy} title="编辑" aria-label={`编辑${group.name}`} onClick={() => openEdit(group)}><Pencil /></Button><Button size="icon-sm" variant="ghost" disabled={busy || group.member_count > 0} title={group.member_count > 0 ? "组内有客户时不可删除" : "删除分组"} aria-label={`删除${group.name}`} onClick={() => void handleDelete(group)}><Trash2 /></Button></div></PermissionGate>;
  const columns: ResourceColumn<CustomerGroup>[] = [
    { id: "name", header: "客户分组", render: identity, className: "max-w-72 whitespace-normal" },
    { id: "discount", header: "默认减免", render: group => `减免 ${group.discount_percent}%` },
    { id: "rules", header: "账单 / 生命周期规则", render: rules, className: "max-w-72 whitespace-normal" },
    { id: "members", header: "客户数量", render: group => group.member_count },
    { id: "actions", header: "操作", render: actions },
  ];
  return <Workspace title="客户分组" admin><AdminListPage title="客户分组" description="默认减免适用于新购商品、配置和初装费；20% 表示减免20%，商品分组专属规则优先。"
    actions={<><Button variant="outline" disabled={resource.isLoading} onClick={refresh}><RefreshCw />刷新</Button><PermissionGate permission="customer_groups.manage"><Button onClick={openCreate}><Plus />添加客户分组</Button></PermissionGate></>}
    filters={<div className="space-y-4"><CustomerGroupTabs active="customers" /><div className="rounded border border-border bg-card p-4"><Input aria-label="搜索分组" placeholder="搜索分组名称" className="w-full sm:max-w-sm" value={search} onChange={e => { setSearch(e.target.value); setPage(0); }} /></div></div>}
    feedback={<><p className="text-sm text-muted-foreground">新服务保存分组折后续费基价，已有订单与服务不重算；优惠码仅在明确允许当前客户组时叠加。</p>{actionError && <p role="alert" className="break-all text-sm text-destructive">{actionError}</p>}</>}>
    <AdminListRegion title="客户分组列表" allowed={allowed} resource={resource} retry={refresh} summary={resource.data && `共 ${resource.data.total} 个分组`}
      pagination={data => <AdminPagination total={data.total} page={page} pageSize={pageSize} onPageChange={setPage} onPageSizeChange={size => { setPageSize(size); setPage(0); }} />}>
      {data => <ResourceTable items={data.items} rowKey={group => group.id} columns={columns} className="min-w-0" ariaLabel="客户分组列表" empty="暂无客户分组"
        renderMobile={group => <article className="space-y-4 p-4">{identity(group)}<div className="flex flex-wrap gap-2 text-sm"><span>减免 {group.discount_percent}%</span><span>客户 {group.member_count} 位</span></div>{rules(group)}{actions(group)}</article>} />}
    </AdminListRegion>
    <Dialog open={dialogOpen && manage} onOpenChange={open => { if (!busy) setDialogOpen(open); }}><DialogContent className="max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>{editingGroup ? "编辑客户分组" : "添加客户分组"}</DialogTitle><DialogDescription>设置名称、业务标记颜色、默认减免和账单规则。</DialogDescription></DialogHeader>
      <form onSubmit={handleSubmit} className="space-y-4"><fieldset disabled={busy || !manage} className="space-y-4">
        <label className="grid gap-2 text-sm">客户组名称<Input required maxLength={120} value={name} onChange={e => setName(e.target.value)} placeholder="例如：VIP大客户、渠道代理" /></label>
        <div className="space-y-2"><p className="text-sm">组颜色</p><div className="flex gap-2"><Input type="color" aria-label="组颜色选择" className="h-10 w-12 shrink-0 p-1" value={color} onChange={e => setColor(e.target.value)} /><Input aria-label="组颜色值" maxLength={7} value={color} onChange={e => setColor(e.target.value)} className="min-w-0 font-mono" /></div></div>
        <label className="grid gap-2 text-sm">默认减免百分比（%）<Input type="number" min={0} max={100} value={discount} onChange={e => setDiscount(e.target.value)} /></label>
        <p className="text-xs text-muted-foreground">0 表示不减免，20 表示减免20%，100 表示免单。</p>
        <label className="flex items-start gap-2 text-sm"><Checkbox checked={separateInvoices} onCheckedChange={value => setSeparateInvoices(value === true)} />拆分账单（每个商品独立生成账单）</label>
        <label className="flex items-start gap-2 text-sm"><Checkbox checked={suspendDeleteExempt} onCheckedChange={value => setSuspendDeleteExempt(value === true)} />豁免逾期自动暂停与删除</label>
      </fieldset>{formError && <p role="alert" className="break-all text-sm text-destructive">{formError}</p>}<div className="flex justify-end gap-2 border-t pt-4"><Button type="button" variant="outline" disabled={busy} onClick={() => setDialogOpen(false)}>取消</Button><Button disabled={busy || !manage || !name.trim()}>{busy ? "保存中" : "保存更改"}</Button></div></form>
    </DialogContent></Dialog>
  </AdminListPage></Workspace>;
}
