"use client";

import { useState } from "react";
import { useSearchParams } from "next/navigation";
import { Plus, Search, Pencil, Trash2, RefreshCw } from "lucide-react";
import { api, type CustomerGroup } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Checkbox } from "./ui/checkbox";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "./ui/dialog";
import { Table, TableHeader, TableHead, TableBody, TableRow, TableCell } from "./ui/table";
import { Failure, Loading } from "./common";
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
  const { data, error, refresh } = useResource<{ items: CustomerGroup[]; total: number }>(endpoint);
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
      alert("该分组下仍有客户，无法直接删除！请先将客户移至其他分组。");
      return;
    }
    if (!window.confirm(`确定要删除客户分组 "${group.name}" 吗？`)) return;

    setBusy(true);
    try {
      await api(`/admin/customer-groups/${group.id}`, {
        method: "DELETE",
        headers: { "Idempotency-Key": `customer-group-delete-${group.id}-${group.version}-${crypto.randomUUID()}` },
        body: JSON.stringify({ expected_version: group.version }),
      });
      refresh();
    } catch (err) {
      alert((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const groups = data?.items ?? [];

  return (
    <Workspace title="客户分组" admin>
      <div className="bg-white rounded border border-[#ebeef5] shadow-sm p-4 sm:p-5 space-y-4 max-w-full overflow-hidden">
        {/* Top guide text matching MoFang #/customer-group */}
        <div className="text-xs text-[#606266]">
          客户分组功能可以很轻松的区分您的客户，并能给客户组中的客户提供特别的待遇。{" "}
          <a
            href="https://bbs.idcsmart.com"
            target="_blank"
            rel="noreferrer"
            className="text-[#409eff] hover:underline ml-1"
          >
            帮助文档
          </a>
        </div>

        <CustomerGroupTabs active="customers" />

        {/* Action Toolbar */}
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-2">
            <PermissionGate permission="customer_groups.manage">
              <Button
                onClick={openCreate}
                size="sm"
                className="h-8 bg-[#1cb24d] hover:bg-[#169941] text-white text-xs px-3.5 rounded shadow-none font-normal"
              >
                <Plus size={14} className="mr-1" />
                添加客户分组
              </Button>
            </PermissionGate>
          </div>

          <div className="flex items-center gap-2">
            <div className="relative">
              <Input
                value={search}
                onChange={(e) => {
                  setSearch(e.target.value);
                  setPage(0);
                }}
                placeholder="搜索分组名称"
                className="h-8 w-48 text-xs border-[#dcdfe6] pr-8"
                aria-label="搜索分组"
              />
              <Search
                size={13}
                className="absolute right-2.5 top-1/2 -translate-y-1/2 text-[#909399] pointer-events-none"
              />
            </div>
            <Button
              variant="outline"
              size="icon"
              className="h-8 w-8 text-[#606266] border-[#dcdfe6]"
              title="刷新列表"
              aria-label="刷新列表"
              onClick={refresh}
            >
              <RefreshCw size={13} />
            </Button>
          </div>
        </div>

        {/* Data Table */}
        {error ? (
          <Failure error={error} retry={refresh} />
        ) : !data ? (
          <Loading />
        ) : (
          <>
            <div className="overflow-x-auto">
              <Table>
                <TableHeader className="bg-[#f5f7fa]">
                  <TableRow className="border-b border-[#ebeef5] hover:bg-[#f5f7fa]">
                    <TableHead className="w-16 text-center text-xs font-semibold text-[#909399] py-3">ID</TableHead>
                    <TableHead className="text-xs font-semibold text-[#909399] py-3">客户组名称</TableHead>
                    <TableHead className="text-xs font-semibold text-[#909399] py-3">组颜色</TableHead>
                    <TableHead className="text-xs font-semibold text-[#909399] py-3">商品折扣</TableHead>
                    <TableHead className="text-xs font-semibold text-[#909399] py-3">拆分账单</TableHead>
                    <TableHead className="text-xs font-semibold text-[#909399] py-3">客户数量</TableHead>
                    <TableHead className="text-right text-xs font-semibold text-[#909399] py-3 pr-4">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {groups.length === 0 ? (
                    <TableRow>
                      <TableCell colSpan={7} className="h-32 text-center text-xs text-[#909399]">
                        暂无数据
                      </TableCell>
                    </TableRow>
                  ) : (
                    groups.map((group) => (
                      <TableRow
                        key={group.id}
                        className="border-b border-[#ebeef5] hover:bg-[#f5f7fa] text-xs text-[#606266]"
                      >
                        <TableCell className="text-center font-mono text-xs text-[#909399]">
                          {group.id}
                        </TableCell>
                        <TableCell>
                          <div className="flex items-center gap-2 font-medium text-[#303133]">
                            <span
                              className="w-3 h-3 rounded-full border border-black/10 inline-block shrink-0"
                              style={{ backgroundColor: group.color || "#64748b" }}
                            />
                            <span>{group.name}</span>
                          </div>
                        </TableCell>
                        <TableCell>
                          <span className="font-mono text-xs text-[#909399]">
                            {group.color || "-"}
                          </span>
                        </TableCell>
                        <TableCell>
                          <span className="text-[#67c23a] font-medium">
                            {group.discount_percent}%
                          </span>
                        </TableCell>
                        <TableCell>
                          <span className="text-[#909399]">
                            {group.separate_invoices ? "拆分" : "合并"}
                          </span>
                        </TableCell>
                        <TableCell>
                          <span className="font-semibold text-[#303133]">
                            {group.member_count}
                          </span>
                        </TableCell>
                        <TableCell className="text-right pr-4">
                          <div className="flex items-center justify-end gap-2">
                            <PermissionGate permission="customer_groups.manage">
                              <Button
                                size="sm"
                                variant="outline"
                                className="h-7 text-xs border-[#dcdfe6] text-[#409eff] hover:border-[#409eff] hover:bg-[#ecf5ff]"
                                onClick={() => openEdit(group)}
                              >
                                <Pencil size={12} className="mr-1" />
                                编辑
                              </Button>
                              <Button
                                size="sm"
                                variant="outline"
                                className="h-7 text-xs border-[#dcdfe6] text-[#f56c6c] hover:border-[#f56c6c] hover:bg-[#fef0f0]"
                                disabled={busy || group.member_count > 0}
                                title={group.member_count > 0 ? "组内有客户时不可删除" : "删除分组"}
                                onClick={() => void handleDelete(group)}
                              >
                                <Trash2 size={12} className="mr-1" />
                                删除
                              </Button>
                            </PermissionGate>
                          </div>
                        </TableCell>
                      </TableRow>
                    ))
                  )}
                </TableBody>
              </Table>
            </div>

            <AdminPagination
              total={data?.total ?? 0}
              page={page}
              pageSize={pageSize}
              onPageChange={setPage}
              onPageSizeChange={setPageSize}
            />
          </>
        )}
      </div>

        {/* Add/Edit Group Dialog */}
        <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
          <DialogContent className="sm:max-w-md bg-white border border-[#ebeef5] rounded shadow-lg p-0 overflow-hidden">
            <DialogHeader className="px-5 py-4 border-b border-[#ebeef5] bg-[#fafafa]">
              <DialogTitle className="text-sm font-semibold text-[#303133]">
                {editingGroup ? "编辑客户分组" : "添加客户分组"}
              </DialogTitle>
              <DialogDescription className="text-xs text-[#909399]">
                设置客户分组名称、组颜色、折扣策略与账单规则。
              </DialogDescription>
            </DialogHeader>

            <form onSubmit={handleSubmit} className="p-5 space-y-4 text-xs">
              {formError && (
                <div className="p-2.5 bg-[#fef0f0] border border-[#fde2e2] text-[#f56c6c] rounded">
                  {formError}
                </div>
              )}

              <div className="space-y-1.5">
                <label className="font-medium text-[#303133] block">
                  <span className="text-red-500 mr-1">*</span>客户组名称
                </label>
                <Input
                  required
                  maxLength={120}
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="例如：VIP大客户、渠道代理"
                  className="h-8 text-xs border-[#dcdfe6]"
                />
              </div>

              <div className="space-y-1.5">
                <label className="font-medium text-[#303133] block">
                  <span className="text-red-500 mr-1">*</span>组颜色
                </label>
                <div className="flex items-center gap-2">
                  <Input
                    type="color"
                    value={color}
                    onChange={(e) => setColor(e.target.value)}
                    className="w-10 h-8 p-1 border-[#dcdfe6] cursor-pointer"
                  />
                  <Input
                    type="text"
                    maxLength={7}
                    value={color}
                    onChange={(e) => setColor(e.target.value)}
                    className="h-8 text-xs border-[#dcdfe6] font-mono flex-1"
                  />
                </div>
              </div>

              <div className="space-y-1.5">
                <label className="font-medium text-[#303133] block">
                  商品折扣百分比（%）
                </label>
                <Input
                  type="number"
                  min="0"
                  max="100"
                  value={discount}
                  onChange={(e) => setDiscount(e.target.value)}
                  placeholder="0 - 100"
                  className="h-8 text-xs border-[#dcdfe6]"
                />
                <span className="text-[#909399] text-[11px] block">
                  该组客户在购买适用商品时享受的全局折扣，0 为不打折。
                </span>
              </div>

              <div className="space-y-2 pt-2 border-t border-[#ebeef5]">
                <label className="flex items-center gap-2 cursor-pointer">
                  <Checkbox
                    checked={separateInvoices}
                    onCheckedChange={(val) => setSeparateInvoices(val === true)}
                  />
                  <span className="text-[#303133]">拆分账单（每个商品独立生成账单）</span>
                </label>
                <label className="flex items-center gap-2 cursor-pointer">
                  <Checkbox
                    checked={suspendDeleteExempt}
                    onCheckedChange={(val) => setSuspendDeleteExempt(val === true)}
                  />
                  <span className="text-[#303133]">豁免逾期自动暂停与删除</span>
                </label>
              </div>

              <div className="px-5 py-3 -mx-5 -mb-5 mt-5 border-t border-[#ebeef5] bg-[#fafafa] flex items-center justify-end gap-2">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="h-8 text-xs border-[#dcdfe6]"
                  disabled={busy}
                  onClick={() => setDialogOpen(false)}
                >
                  取消
                </Button>
                <Button
                  type="submit"
                  size="sm"
                  className="h-8 bg-[#409eff] hover:bg-[#337ecc] text-white text-xs px-4 font-normal shadow-none"
                  disabled={busy || !name.trim()}
                >
                  保存更改
                </Button>
              </div>
            </form>
          </DialogContent>
        </Dialog>
    </Workspace>
  );
}
