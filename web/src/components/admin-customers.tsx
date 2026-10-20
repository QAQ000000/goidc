"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import {
  Plus,
  Search,
  Users,
  CreditCard,
  KeyRound,
  Pencil,
  ContactRound,
  UserRoundCog,
  Save,
  ArrowUpDown,
  FileText,
} from "lucide-react";
import { api, type CustomerGroup } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { useCustomerMutation } from "@/hooks/use-customer-mutation";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "./ui/dialog";
import { Table, TableHeader, TableHead, TableBody, TableRow, TableCell } from "./ui/table";
import { Failure, Loading } from "./common";
import { Workspace, PermissionGate, useAdminPermission, useAdminIdentity } from "./workspace";
import { AdminPagination } from "./admin-pagination";

type SalesOwner = { id: number; username: string; display_name: string };

type AdminCustomer = {
  id: number;
  username: string;
  email: string;
  display_name: string;
  status: string;
  balance_cents: number;
  created_at: string;
  sales_owner_id: number | null;
  sales_owner_name: string | null;
  sales_version: number;
  group_id: number | null;
  group_name: string | null;
  active_services_count?: number;
  total_services_count?: number;
  income_cents?: number;
  expense_cents?: number;
  company_name?: string | null;
  phone_number?: string | null;
};

export function AdminCustomersView() {
  const [query, setQuery] = useState("");
  const [draftQuery, setDraftQuery] = useState("");
  const [groupFilter, setGroupFilter] = useState("all");
  const [statusFilter, setStatusFilter] = useState("all");
  const [salesFilter, setSalesFilter] = useState("all");
  const [showAdvanced, setShowAdvanced] = useState(false);

  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState(50);

  const canReadGroups = useAdminPermission("customer_groups.view");
  const canReadSalesOwners = useAdminPermission("@super");
  const groups = useResource<CustomerGroup[]>(canReadGroups ? "/admin/customer-groups?options=true" : null, false);
  const salesOwners = useResource<SalesOwner[]>(canReadSalesOwners ? "/admin/sales-owners" : null, false);

  const endpoint = `/admin/customers/page?q=${encodeURIComponent(query)}&limit=${pageSize}&offset=${page * pageSize}&group_id=${groupFilter}&sales_owner_id=${salesFilter}${statusFilter === "all" ? "" : `&status=${statusFilter}`}`;
  const { data, error, refresh } = useResource<{ items: AdminCustomer[]; total: number }>(endpoint, false);
  const filteredData = data?.items ?? [];
  const totalCount = data?.total ?? 0;
  const lastPage = Math.max(0, Math.ceil(totalCount / pageSize) - 1);
  if (data && page > lastPage) setPage(lastPage);

  function handleSearch(e?: React.FormEvent) {
    if (e) e.preventDefault();
    setQuery(draftQuery.trim());
    setPage(0);
  }

  function handleReset() {
    setDraftQuery("");
    setQuery("");
    setGroupFilter("all");
    setStatusFilter("all");
    setSalesFilter("all");
    setPage(0);
  }

  return (
    <Workspace title="客户列表" admin>
      <div className="bg-white rounded border border-[#ebeef5] shadow-sm p-4 sm:p-5 space-y-4 max-w-full overflow-hidden">
        {/* Top guide text matching MoFang */}
        <div className="text-xs text-[#606266]">
          系统内所有客户信息，点击姓名可查看更多详细信息并对客户进行管理。{" "}
          <a href="#" className="text-[#409eff] hover:underline ml-1">
            帮助文档
          </a>
        </div>

        {/* Action bar matching MoFang */}
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex flex-wrap items-center gap-2">
            <PermissionGate permission="customers.manage">
              <Button
                asChild
                className="bg-[#67c23a] hover:bg-[#85ce61] text-white text-xs h-8 px-3.5 rounded shadow-none font-normal"
              >
                <Link href="/admin/customers/new">
                  <Plus size={14} className="mr-1" />
                  添加客户
                </Link>
              </Button>
            </PermissionGate>
            <Button
              variant="outline"
              onClick={() => setShowAdvanced(!showAdvanced)}
              className="border-[#dcdfe6] text-[#606266] hover:bg-[#f5f7fa] text-xs h-8 px-3.5 rounded font-normal"
            >
              高级搜索
            </Button>
            <Button
              variant="outline"
              asChild
              className="border-[#dcdfe6] text-[#606266] hover:bg-[#f5f7fa] text-xs h-8 px-3.5 rounded font-normal"
            >
              <Link href="/admin/customer-groups">
                <Users size={14} className="mr-1" />
                管理客户分组
              </Link>
            </Button>
          </div>

          <form onSubmit={handleSearch} className="flex items-center gap-2">
            <Input
              value={draftQuery}
              onChange={(e) => setDraftQuery(e.target.value)}
              placeholder="搜索姓名、邮箱或账号..."
              className="h-8 w-48 text-xs border-[#dcdfe6] focus-visible:border-[#409eff]"
            />
            <Button
              type="submit"
              size="sm"
              className="h-8 bg-[#409eff] hover:bg-[#337ecc] text-white text-xs px-3"
            >
              <Search size={13} className="mr-1" />
              搜索
            </Button>
          </form>
        </div>

        {/* Advanced Search Drawer */}
        {showAdvanced && (
          <div className="bg-[#f5f7fa] p-4 rounded border border-[#e4e7ed] space-y-3">
            <div className="grid grid-cols-1 sm:grid-cols-2 md:grid-cols-4 gap-3">
              <div>
                <label className="block text-xs text-[#606266] mb-1">关键字搜索</label>
                <Input
                  value={draftQuery}
                  onChange={(e) => setDraftQuery(e.target.value)}
                  placeholder="姓名 / 邮箱 / 账号"
                  className="h-8 text-xs bg-white border-[#dcdfe6]"
                />
              </div>

              <div>
                <label className="block text-xs text-[#606266] mb-1">客户分组</label>
                <Select value={groupFilter} onValueChange={(value) => { setGroupFilter(value); setPage(0); }}>
                  <SelectTrigger className="h-8 text-xs bg-white border-[#dcdfe6]">
                    <SelectValue placeholder="全部分组" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="all">全部分组</SelectItem>
                    <SelectItem value="none">未分组</SelectItem>
                    {groups.data?.map((g) => (
                      <SelectItem key={g.id} value={String(g.id)}>
                        {g.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>

              <div>
                <label className="block text-xs text-[#606266] mb-1">状态</label>
                <Select value={statusFilter} onValueChange={(value) => { setStatusFilter(value); setPage(0); }}>
                  <SelectTrigger className="h-8 text-xs bg-white border-[#dcdfe6]">
                    <SelectValue placeholder="全部状态" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="all">全部状态</SelectItem>
                    <SelectItem value="active">正常</SelectItem>
                    <SelectItem value="disabled">已停用</SelectItem>
                  </SelectContent>
                </Select>
              </div>

              <div>
                <label className="block text-xs text-[#606266] mb-1">销售归属</label>
                <Select value={salesFilter} onValueChange={(value) => { setSalesFilter(value); setPage(0); }}>
                  <SelectTrigger className="h-8 text-xs bg-white border-[#dcdfe6]">
                    <SelectValue placeholder="全部销售" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="all">全部销售</SelectItem>
                    <SelectItem value="none">无归属</SelectItem>
                    {salesOwners.data?.map((s) => (
                      <SelectItem key={s.id} value={String(s.id)}>
                        {s.display_name || s.username}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            </div>

            <div className="flex justify-end gap-2 pt-1">
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={handleReset}
                className="h-7 text-xs border-[#dcdfe6]"
              >
                重置
              </Button>
              <Button
                type="button"
                size="sm"
                onClick={() => handleSearch()}
                className="h-7 text-xs bg-[#409eff] text-white hover:bg-[#337ecc]"
              >
                应用筛选
              </Button>
            </div>
          </div>
        )}

        {/* Table Container */}
        <div className="border border-[#ebeef5] rounded overflow-hidden">
          {error ? (
            <Failure error={error} retry={refresh} />
          ) : !data ? (
            <Loading />
          ) : (
            <>
              <div className="overflow-x-auto">
                <Table>
                  <TableHeader className="bg-[#f5f7fa]">
                    <TableRow className="border-[#ebeef5] hover:bg-[#f5f7fa]">
                      <TableHead className="w-16 text-xs font-semibold text-[#909399] py-3">
                        <span className="inline-flex items-center gap-1">
                          ID <ArrowUpDown size={11} className="opacity-60" />
                        </span>
                      </TableHead>
                      <TableHead className="text-xs font-semibold text-[#909399] py-3">
                        <span className="inline-flex items-center gap-1">
                          姓名 <ArrowUpDown size={11} className="opacity-60" />
                        </span>
                      </TableHead>
                      <TableHead className="text-xs font-semibold text-[#909399] py-3">
                        手机号/邮箱
                      </TableHead>
                      <TableHead className="text-xs font-semibold text-[#909399] py-3">
                        服务
                      </TableHead>
                      <TableHead className="text-xs font-semibold text-[#909399] py-3">
                        <span className="inline-flex items-center gap-1">
                          收入/支出 <ArrowUpDown size={11} className="opacity-60" />
                        </span>
                      </TableHead>
                      <TableHead className="text-xs font-semibold text-[#909399] py-3">
                        <span className="inline-flex items-center gap-1">
                          余额 <ArrowUpDown size={11} className="opacity-60" />
                        </span>
                      </TableHead>
                      <TableHead className="text-xs font-semibold text-[#909399] py-3">
                        客户分组
                      </TableHead>
                      <TableHead className="text-xs font-semibold text-[#909399] py-3">
                        <span className="inline-flex items-center gap-1">
                          状态 <ArrowUpDown size={11} className="opacity-60" />
                        </span>
                      </TableHead>
                      <TableHead className="text-xs font-semibold text-[#909399] py-3">
                        销售
                      </TableHead>
                      <TableHead className="text-xs font-semibold text-[#909399] py-3">
                        信用额（已用/总计）
                      </TableHead>
                      <TableHead className="text-xs font-semibold text-[#909399] py-3 text-right">
                        操作
                      </TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {filteredData.length === 0 ? (
                      <TableRow>
                        <TableCell colSpan={11} className="h-32 text-center text-xs text-[#909399]">
                          暂无客户记录
                        </TableCell>
                      </TableRow>
                    ) : (
                      filteredData.map((cust) => (
                        <TableRow
                          key={cust.id}
                          className="border-[#ebeef5] hover:bg-[#f5f7fa] transition-colors"
                        >
                          {/* ID */}
                          <TableCell className="py-3 text-xs font-mono">
                            <Link
                              href={`/admin/customers/${cust.id}`}
                              className="text-[#409eff] hover:underline"
                            >
                              {cust.id}
                            </Link>
                          </TableCell>

                          {/* 姓名 */}
                          <TableCell className="py-3 text-xs">
                            <Link
                              href={`/admin/customers/${cust.id}`}
                              className="text-[#409eff] hover:underline font-medium"
                            >
                              {cust.display_name || cust.username}
                            </Link>
                            {cust.company_name && (
                              <div className="text-[11px] text-[#909399] mt-0.5">
                                {cust.company_name}
                              </div>
                            )}
                          </TableCell>

                          {/* 手机号/邮箱 */}
                          <TableCell className="py-3 text-xs">
                            {cust.phone_number && (
                              <div className="text-[#303133]">{cust.phone_number}</div>
                            )}
                            <div className="text-[#909399] text-[11px]">{cust.email}</div>
                          </TableCell>

                          {/* 服务 */}
                          <TableCell className="py-3 text-xs">
                            <Link
                              href={`/admin/customers/${cust.id}?tab=services`}
                              className="text-[#409eff] hover:underline"
                            >
                              {cust.active_services_count ?? 0}({cust.total_services_count ?? 0})
                            </Link>
                          </TableCell>

                          {/* 收入/支出 */}
                          <TableCell className="py-3 text-xs">
                            <div className="text-[#303133]">
                              ¥{((cust.income_cents ?? 0) / 100).toFixed(2)}
                            </div>
                            <div className="text-[#909399] text-[11px]">
                              ¥{((cust.expense_cents ?? 0) / 100).toFixed(2)}
                            </div>
                          </TableCell>

                          {/* 余额 */}
                          <TableCell className="py-3 text-xs font-medium text-[#303133]">
                            ¥{(cust.balance_cents / 100).toFixed(2)}
                          </TableCell>

                          {/* 客户分组 */}
                          <TableCell className="py-3 text-xs text-[#606266]">
                            {cust.group_name || "-"}
                          </TableCell>

                          {/* 状态 */}
                          <TableCell className="py-3 text-xs">
                            {cust.status === "active" ? (
                              <span className="inline-flex items-center px-2 py-0.5 rounded text-[11px] font-medium bg-[#f0f9eb] text-[#67c23a] border border-[#e1f3d8]">
                                正常
                              </span>
                            ) : (
                              <span className="inline-flex items-center px-2 py-0.5 rounded text-[11px] font-medium bg-[#f4f4f5] text-[#909399] border border-[#e9e9eb]">
                                已停用
                              </span>
                            )}
                          </TableCell>

                          {/* 销售 */}
                          <TableCell className="py-3 text-xs text-[#606266]">
                            {cust.sales_owner_name || "-"}
                          </TableCell>

                          {/* 信用额 */}
                          <TableCell className="py-3 text-xs text-[#909399]">
                            <div>¥0</div>
                            <div className="text-[11px]">¥0.00</div>
                          </TableCell>

                          {/* 操作 */}
                          <TableCell className="py-3 text-xs text-right whitespace-nowrap">
                            <CustomerActions row={cust} refresh={refresh} />
                          </TableCell>
                        </TableRow>
                      ))
                    )}
                  </TableBody>
                </Table>
              </div>

              <AdminPagination
                total={totalCount}
                page={page}
                pageSize={pageSize}
                onPageChange={setPage}
                onPageSizeChange={(ps) => {
                  setPageSize(ps);
                  setPage(0);
                }}
              />
            </>
          )}
        </div>
      </div>
    </Workspace>
  );
}

export function CustomerActions({ row, refresh }: { row: AdminCustomer; refresh: () => void }) {
  const actor = useAdminIdentity();
  if (!actor) return null;
  return <CustomerActionsForActor key={`${actor.id}:${row.id}`} row={row} refresh={refresh} />;
}

function CustomerActionsForActor({ row, refresh }: { row: AdminCustomer; refresh: () => void }) {
  const detailHref = `/admin/customers/${row.id}`;
  return (
    <div className="flex items-center justify-end gap-1.5">
      <Button
        size="sm"
        variant="outline"
        asChild
        className="h-7 text-xs px-2 border-[#dcdfe6] text-[#606266] hover:text-[#409eff] hover:border-[#409eff]"
      >
        <Link href={`${detailHref}?tab=contacts`}>
          <ContactRound size={13} className="mr-1" />
          联系人
        </Link>
      </Button>

      <PermissionGate permission="customer_files.view">
        <Button
          size="sm"
          variant="outline"
          asChild
          className="h-7 text-xs px-2 border-[#dcdfe6] text-[#606266] hover:text-[#409eff] hover:border-[#409eff]"
        >
          <Link href={`/admin/customers/${row.id}/files`}>
            <FileText size={13} className="mr-1" />
            文件
          </Link>
        </Button>
      </PermissionGate>

      <PermissionGate permission="@super">
        <CustomerSalesAction row={row} refresh={refresh} />
      </PermissionGate>

      <PermissionGate permission="customers.manage">
        <CustomerActionDialog row={row} title="编辑客户" icon={Pencil}>
          {(onBusyChange) => (
            <CustomerProfileAction row={row} refresh={refresh} onBusyChange={onBusyChange} />
          )}
        </CustomerActionDialog>
        <CustomerActionDialog row={row} title="重置密码" icon={KeyRound}>
          {(onBusyChange) => (
            <CustomerPasswordAction row={row} onBusyChange={onBusyChange} />
          )}
        </CustomerActionDialog>
      </PermissionGate>

      <PermissionGate permission="balance.manage">
        <CustomerActionDialog row={row} title="余额充值" icon={CreditCard}>
          {(onBusyChange) => (
            <CustomerBalanceAction row={row} refresh={refresh} onBusyChange={onBusyChange} />
          )}
        </CustomerActionDialog>
      </PermissionGate>
    </div>
  );
}

function CustomerActionDialog({
  row,
  title,
  icon: Icon,
  children,
}: {
  row: AdminCustomer;
  title: string;
  icon: React.ComponentType<{ size?: number }>;
  children: (onBusyChange: (busy: boolean) => void) => React.ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  return (
    <>
      <Button
        size="icon-sm"
        variant="outline"
        title={title}
        aria-label={`${title} ${row.username}`}
        onClick={() => setOpen(true)}
        className="h-7 w-7 border-[#dcdfe6] text-[#606266] hover:text-[#409eff] hover:border-[#409eff]"
      >
        <Icon size={13} />
      </Button>
      <Dialog
        open={open}
        onOpenChange={(value) => {
          if (!busy) setOpen(value);
        }}
      >
        <DialogContent className="grid-cols-[minmax(0,1fr)] max-w-md">
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
            <DialogDescription className="break-all text-xs text-[#909399]">
              客户 #{row.id} · {row.display_name || row.username}
            </DialogDescription>
          </DialogHeader>
          {open && children(setBusy)}
        </DialogContent>
      </Dialog>
    </>
  );
}

function useCustomerActorValue<T>(actorID: number | undefined, initial: T): [T, (value: T) => void] {
  const [state, setState] = useState({ actorID, value: initial });
  return [state.actorID === actorID ? state.value : initial, value => setState({ actorID, value })];
}

export function CustomerSalesAction({ row, refresh }: { row: AdminCustomer; refresh: () => void }) {
  const actor = useAdminIdentity();
  const [open, setOpen] = useState(false);
  const [owner, setOwner] = useCustomerActorValue<string | null>(actor?.id, null);
  const mutation = useCustomerMutation(actor?.id, row.id, "sales-owner", `/admin/customers/${row.id}/sales-owner`, "PUT");
  const directory = useResource<SalesOwner[]>(open ? "/admin/sales-owners" : null, false);
  const detail = useResource<AdminCustomer>(open ? `/admin/customers/${row.id}` : null, false);
  const currentOwner = (detail.data ?? row).sales_owner_id == null ? "none" : String((detail.data ?? row).sales_owner_id);
  const selectedOwner = owner ?? currentOwner;
  const frozen = mutation.pending ? JSON.parse(mutation.pending.body) as { sales_owner_id: number | null } : null;

  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!mutation.pending && (!detail.data || selectedOwner === currentOwner)) return;
    if (await mutation.submit({ sales_owner_id: selectedOwner === "none" ? null : Number(selectedOwner), expected_version: Number(detail.data?.sales_version) })) {
      setOpen(false);
      refresh();
      detail.refresh();
    }
  }

  return <>
    <Button size="icon-sm" variant="outline" title="调整销售归属" aria-label={`调整 ${row.display_name} 的销售归属`}
      onClick={() => { setOwner(null); setOpen(true); detail.refresh(); }}
      className="h-7 w-7 border-[#dcdfe6] text-[#606266] hover:text-[#409eff] hover:border-[#409eff]"><UserRoundCog size={13} /></Button>
    <Dialog open={open} onOpenChange={value => { if (!mutation.busy) setOpen(value); }}>
      <DialogContent className="max-w-md"><DialogHeader><DialogTitle>销售归属</DialogTitle><DialogDescription className="text-xs text-[#909399]">调整客户 #{row.id} 的专属销售顾问</DialogDescription></DialogHeader>
        {directory.error && !mutation.pending ? <Failure error={directory.error} retry={directory.refresh} /> : detail.error && !mutation.pending ? <Failure error={detail.error} retry={detail.refresh} /> : (!directory.data || !detail.data) && !mutation.pending ? <Loading /> :
          <form onSubmit={save} className="space-y-4">
            <Select value={frozen ? (frozen.sales_owner_id == null ? "none" : String(frozen.sales_owner_id)) : selectedOwner} onValueChange={setOwner} disabled={mutation.busy || !!mutation.pending}>
              <SelectTrigger className="w-full text-xs border-[#dcdfe6]" aria-label="销售管理员"><SelectValue /></SelectTrigger>
              <SelectContent><SelectItem value="none">无归属</SelectItem>{directory.data?.map(item => <SelectItem key={item.id} value={String(item.id)}>{item.display_name} · {item.username}</SelectItem>)}</SelectContent>
            </Select>
            {frozen && <p className="text-xs text-muted-foreground">原请求销售归属：{frozen.sales_owner_id == null ? "无归属" : `管理员 #${frozen.sales_owner_id}`}</p>}
            <CustomerMutationNotice mutation={mutation} reload={() => { if (mutation.editRejected()) { setOwner(null); detail.refresh(); refresh(); } }} />
            <div className="flex justify-end gap-2"><Button type="button" variant="outline" size="sm" disabled={mutation.busy} onClick={() => setOpen(false)} className="text-xs border-[#dcdfe6]">取消</Button>
              <Button disabled={mutation.busy || !mutation.ready || mutation.rejected || (!mutation.pending && (!detail.data || selectedOwner === currentOwner))} size="sm" className="bg-[#409eff] hover:bg-[#337ecc] text-white text-xs"><Save size={13} className="mr-1" />{mutation.busy ? "保存中" : mutation.pending ? "使用原请求重试" : "保存归属"}</Button>
            </div>
          </form>}
      </DialogContent>
    </Dialog>
  </>;
}

type CustomerMutation = ReturnType<typeof useCustomerMutation>;
function CustomerMutationNotice({ mutation, reload }: { mutation: CustomerMutation; reload: () => void }) {
  return <>
    {mutation.pending && <p role="status" className="text-xs text-muted-foreground">上次操作尚未确认，已保留原请求。重试使用原内容和操作编号，确认前不能改成另一项操作。</p>}
    {mutation.error && <p role="alert" className="text-destructive text-xs">{mutation.error}</p>}
    {mutation.rejected && mutation.pending && <Button type="button" size="sm" variant="outline" disabled={mutation.busy} onClick={reload}>请求已拒绝，重新读取并编辑</Button>}
    {!mutation.ready && mutation.error && !mutation.pending && <Button type="button" size="sm" variant="outline" onClick={mutation.reload}>重新读取恢复信息</Button>}
  </>;
}

type GroupMembership = { customer_id: number; group_id: number | null; group_membership_version: number };
export function CustomerGroupAction({ row, refresh, onBusyChange }: { row: AdminCustomer; refresh: () => void; onBusyChange: (busy: boolean) => void }) {
  const actor = useAdminIdentity();
  const membership = useResource<GroupMembership>(`/admin/customers/${row.id}/group`, false);
  const groups = useResource<CustomerGroup[]>("/admin/customer-groups?options=true", false);
  const mutation = useCustomerMutation(actor?.id, row.id, "group", `/admin/customers/${row.id}/group`, "PUT");
  const [groupID, setGroupID] = useCustomerActorValue<string | null>(actor?.id, null);
  const [saved, setSaved] = useCustomerActorValue(actor?.id, false);
  useEffect(() => { onBusyChange(mutation.busy); }, [mutation.busy, onBusyChange]);
  const frozen = mutation.pending ? JSON.parse(mutation.pending.body) as { group_id: number | null } : null;
  const current = membership.data?.group_id == null ? "none" : String(membership.data.group_id);
  const selected = frozen ? (frozen.group_id == null ? "none" : String(frozen.group_id)) : groupID ?? current;
  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!mutation.pending && !membership.data) return;
    setSaved(false);
    if (await mutation.submit({ group_id: selected === "none" ? null : Number(selected), expected_version: membership.data?.group_membership_version })) {
      setSaved(true); setGroupID(null); membership.refresh(); refresh();
    }
  }
  return <form className="space-y-3 border-t pt-4" onSubmit={submit}>
    <label className="block text-xs text-[#606266]">客户分组（单独保存）</label>
    {membership.error && !mutation.pending ? <Failure error={membership.error} retry={membership.refresh} /> : groups.error && !mutation.pending ? <Failure error={groups.error} retry={groups.refresh} /> : (!membership.data || !groups.data) && !mutation.pending ? <Loading /> : <>
      <Select value={selected} onValueChange={value => { setGroupID(value); setSaved(false); }} disabled={mutation.busy || !!mutation.pending}>
        <SelectTrigger className="w-full text-xs h-8 border-[#dcdfe6]" aria-label="客户分组"><SelectValue placeholder="客户分组" /></SelectTrigger>
        <SelectContent><SelectItem value="none">未分组</SelectItem>{groups.data?.map(group => <SelectItem key={group.id} value={String(group.id)}>{group.name}</SelectItem>)}</SelectContent>
      </Select>
      <CustomerMutationNotice mutation={mutation} reload={() => { if (mutation.editRejected()) { setGroupID(null); membership.refresh(); } }} />
      <div className="flex justify-end"><Button type="submit" size="sm" disabled={mutation.busy || !mutation.ready || mutation.rejected || (!mutation.pending && (!membership.data || selected === current))}>{mutation.busy ? "保存中" : mutation.pending ? "使用原请求重试" : "保存分组"}</Button></div>
      {saved && <p role="status" className="text-xs text-[#67c23a]">分组已保存</p>}
    </>}
  </form>;
}

export function CustomerProfileAction({ row, refresh, onBusyChange }: { row: AdminCustomer; refresh: () => void; onBusyChange: (busy: boolean) => void }) {
  const actor = useAdminIdentity();
  const canAssignGroup = useAdminPermission("customer_groups.manage");
  const detail = useResource<AdminCustomer & { profile_version: number }>(`/admin/customers/${row.id}`, false);
  const [displayName, setDisplayName] = useCustomerActorValue<string | null>(actor?.id, null);
  const [email, setEmail] = useCustomerActorValue<string | null>(actor?.id, null);
  const [status, setStatus] = useCustomerActorValue<string | null>(actor?.id, null);
  const [groupBusy, setGroupBusy] = useState(false);
  const [saved, setSaved] = useCustomerActorValue(actor?.id, false);
  const mutation = useCustomerMutation(actor?.id, row.id, "profile", `/admin/customers/${row.id}`, "PATCH");
  const frozen = mutation.pending ? JSON.parse(mutation.pending.body) as { display_name: string; email: string; status: string } : null;
  useEffect(() => { onBusyChange(mutation.busy || groupBusy); }, [mutation.busy, groupBusy, onBusyChange]);
  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!mutation.pending && !detail.data) return;
    setSaved(false);
    if (await mutation.submit({ display_name: displayName ?? detail.data?.display_name, email: email ?? detail.data?.email, status: status ?? detail.data?.status, expected_version: detail.data?.profile_version })) {
      setSaved(true); detail.refresh(); refresh();
    }
  }
  return <div className="space-y-4">
    <form className="space-y-4" onSubmit={submit}>
      <fieldset disabled={mutation.busy || !!mutation.pending} className="space-y-4">
        <div><label className="block text-xs text-[#606266] mb-1">姓名</label><Input aria-label="客户姓名" value={frozen?.display_name ?? displayName ?? detail.data?.display_name ?? ""} onChange={event => { setDisplayName(event.target.value); setSaved(false); }} className="text-xs h-8 border-[#dcdfe6]" maxLength={120} required /></div>
        <div><label className="block text-xs text-[#606266] mb-1">邮箱</label><Input aria-label="客户邮箱" type="email" value={frozen?.email ?? email ?? detail.data?.email ?? ""} onChange={event => { setEmail(event.target.value); setSaved(false); }} className="text-xs h-8 border-[#dcdfe6]" maxLength={254} required /></div>
        <div><label className="block text-xs text-[#606266] mb-1">状态</label><Select value={frozen?.status ?? status ?? detail.data?.status ?? "active"} onValueChange={value => { setStatus(value); setSaved(false); }}><SelectTrigger className="w-full text-xs h-8 border-[#dcdfe6]" aria-label="客户状态"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="active">正常</SelectItem><SelectItem value="disabled">已停用</SelectItem></SelectContent></Select></div>
      </fieldset>
      {!mutation.pending && detail.error && <Failure error={detail.error} retry={detail.refresh} />}
      {!mutation.pending && !detail.data && !detail.error && <Loading />}
      <CustomerMutationNotice mutation={mutation} reload={() => { if (mutation.editRejected()) { setDisplayName(null); setEmail(null); setStatus(null); detail.refresh(); } }} />
      <div className="flex justify-end pt-2"><Button disabled={mutation.busy || !mutation.ready || mutation.rejected || (!mutation.pending && !detail.data)} size="sm" className="bg-[#409eff] hover:bg-[#337ecc] text-white text-xs"><Save size={13} className="mr-1" />{mutation.busy ? "保存中" : mutation.pending ? "使用原请求重试" : "保存资料"}</Button></div>
      {saved && <p role="status" className="text-xs text-[#67c23a]">资料已保存</p>}
    </form>
    {canAssignGroup && <CustomerGroupAction key={`${actor?.id ?? "unavailable"}:${row.id}`} row={row} refresh={refresh} onBusyChange={setGroupBusy} />}
  </div>;
}

function CustomerPasswordAction({
  row,
  onBusyChange,
}: {
  row: AdminCustomer;
  onBusyChange: (busy: boolean) => void;
}) {
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!password.trim() || password.length < 6) {
      setMessage("密码至少6位");
      return;
    }
    setBusy(true);
    onBusyChange(true);
    setMessage("");
    try {
      await api(`/admin/customers/${row.id}/password`, {
        method: "POST",
        body: JSON.stringify({ password: password.trim() }),
      });
      setPassword("");
      setMessage("密码已重置");
    } catch (e) {
      setMessage((e as Error).message);
    } finally {
      setBusy(false);
      onBusyChange(false);
    }
  }

  return (
    <form className="space-y-4" onSubmit={submit}>
      <div>
        <label className="block text-xs text-[#606266] mb-1">新密码</label>
        <Input
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          placeholder="请输入至少6位新密码"
          required
          minLength={6}
          className="text-xs h-8 border-[#dcdfe6]"
        />
      </div>
      <div className="flex justify-end pt-2">
        <Button
          disabled={busy || !password.trim()}
          size="sm"
          className="bg-[#409eff] hover:bg-[#337ecc] text-white text-xs"
        >
          {busy ? "重置中..." : "确认重置"}
        </Button>
      </div>
      {message && <p role="status" className="text-xs text-[#67c23a]">{message}</p>}
    </form>
  );
}

function CustomerBalanceAction({
  row,
  refresh,
  onBusyChange,
}: {
  row: AdminCustomer;
  refresh: () => void;
  onBusyChange: (busy: boolean) => void;
}) {
  const [amount, setAmount] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const cents = Math.round(Number(amount) * 100);
    if (!Number.isSafeInteger(cents) || cents < 1) {
      setMessage("金额无效");
      return;
    }
    setBusy(true);
    onBusyChange(true);
    setMessage("");
    try {
      await api(`/admin/customers/${row.id}/balance`, {
        method: "POST",
        body: JSON.stringify({ amount_cents: cents, note: "管理台充值" }),
        headers: { "Idempotency-Key": `web-topup-${row.id}-${crypto.randomUUID()}` },
      });
      setAmount("");
      setMessage("已充值");
      refresh();
    } catch (e) {
      setMessage((e as Error).message);
    } finally {
      setBusy(false);
      onBusyChange(false);
    }
  }

  return (
    <form className="space-y-4" onSubmit={submit}>
      <div>
        <label className="block text-xs text-[#606266] mb-1">
          充值金额（元）· 当前余额: ¥{(row.balance_cents / 100).toFixed(2)}
        </label>
        <Input
          type="number"
          min="0.01"
          step="0.01"
          value={amount}
          onChange={(event) => setAmount(event.target.value)}
          placeholder="请输入充值金额"
          required
          disabled={busy}
          className="text-xs h-8 border-[#dcdfe6]"
        />
      </div>
      <div className="flex justify-end pt-2">
        <Button
          disabled={busy || !amount}
          size="sm"
          className="bg-[#409eff] hover:bg-[#337ecc] text-white text-xs"
        >
          {busy ? "处理中..." : "确认充值"}
        </Button>
      </div>
      {message && <p role="status" className="text-xs text-[#67c23a]">{message}</p>}
    </form>
  );
}
