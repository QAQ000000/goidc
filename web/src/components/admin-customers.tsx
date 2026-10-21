"use client";

import { Suspense, useEffect, useMemo, useState } from "react";
import { useSearchParams } from "next/navigation";
import Link from "next/link";
import {
  Plus,
  Search,
  CreditCard,
  KeyRound,
  Pencil,
  ContactRound,
  UserRoundCog,
  Save,
  ChevronDown,
  Filter,
  RefreshCw,
  FileText,
} from "lucide-react";
import { api, type CustomerGroup } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { useCustomerMutation } from "@/hooks/use-customer-mutation";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "./ui/dialog";
import { Failure, Loading } from "./common";
import { Workspace, PermissionGate, useAdminPermission, useAdminIdentity } from "./workspace";
import { AdminPagination } from "./admin-pagination";
import { AdminListPage, AdminListRegion } from "./admin-list-page";
import { CustomerDirectoryResults, type AdminCustomer, type CustomerDirectoryPage } from "./admin-customer-directory-results";
import { useCustomerFieldDefinitions } from "./customer-field-inputs";
import { DirectoryCustomFields, DirectoryOptionState, DirectorySelect, LevelRuleFilter, certificationLabels, customerDirectoryActorKey, directoryQuery, readDirectoryQuery, type DirectoryFilters } from "./admin-customer-directory-filters";

type SalesOwner = { id: number; username: string; display_name: string };

export function AdminCustomersView() {
  return <Workspace title="客户列表" admin><Suspense fallback={<Loading />}><CustomerDirectoryFromURL /></Suspense></Workspace>;
}

function CustomerDirectoryFromURL() {
  const search = useSearchParams().toString();
  const actor = useAdminIdentity();
  return <CustomerDirectory key={`${customerDirectoryActorKey(actor)}:${search}`} source={search} />;
}

function CustomerDirectory({ source }: { source: string }) {
  const initial = useMemo(() => readDirectoryQuery(source), [source]);
  const applied = initial.filters;
  const [draft, setDraft] = useState<DirectoryFilters>(applied);
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [mobileOpen, setMobileOpen] = useState(false);
  const [formError, setFormError] = useState("");
  const canView = useAdminPermission("customers.view");
  const canReadGroups = useAdminPermission("customer_groups.view");
  const canReadSalesOwners = useAdminPermission("@super");
  const canReadLevels = useAdminPermission("customer_levels.view");
  const groups = useResource<CustomerGroup[]>(canView && canReadGroups ? "/admin/customer-groups?options=true" : null, false);
  const salesOwners = useResource<SalesOwner[]>(canView && canReadSalesOwners ? "/admin/sales-owners" : null, false);
  const definitions = useCustomerFieldDefinitions(canView && (showAdvanced || mobileOpen || Object.keys(applied.customFields).length > 0));
  const query = directoryQuery(applied, initial.limit, initial.offset);
  const suffix = `?${query}`;
  const resource = useResource<CustomerDirectoryPage>(canView && !initial.error ? `/admin/customers/page?${query}` : null, false);
  const pageSize = initial.limit;
  const page = Math.floor(initial.offset / pageSize);
  const lastOffset = resource.data ? Math.max(0, Math.ceil(resource.data.total / pageSize) - 1) * pageSize : null;

  useEffect(() => {
    if (lastOffset !== null && initial.offset > lastOffset) {
      window.history.replaceState(null, "", `/admin/customers?${directoryQuery(applied, pageSize, lastOffset)}`);
    }
  }, [lastOffset, initial.offset, applied, pageSize]);

  function change(patch: Partial<DirectoryFilters>) { setDraft(current => ({ ...current, ...patch })); }
  function navigate(filters: DirectoryFilters, limit = pageSize, offset = 0) {
    window.history.pushState(null, "", `/admin/customers?${directoryQuery(filters, limit, offset)}`);
  }
  function submit(event: React.FormEvent) {
    event.preventDefault();
    if (Object.keys(draft.customFields).length > 20) { setFormError("自定义字段最多同时筛选20项，请减少条件。"); return; }
    if (draft.level_id && !/^[1-9]\d*$/.test(draft.level_id)) { setFormError("等级规则编号需为正整数。"); return; }
    const size = new TextEncoder().encode(JSON.stringify(draft.customFields)).length;
    if (size > 16384 || Object.values(draft.customFields).some(value => typeof value === "number" && !Number.isFinite(value))) { setFormError("自定义字段条件无效或过长，请检查输入。"); return; }
    setFormError(""); setMobileOpen(false);
    navigate(draft);
  }
  function reset() { window.history.pushState(null, "", "/admin/customers"); setDraft(readDirectoryQuery("").filters); setFormError(""); setMobileOpen(false); }

  const summary = [
    applied.q && `关键词：${applied.q}`,
    applied.status && `状态：${applied.status === "active" ? "已启用" : "已停用"}`,
    applied.group_id && `分组：${applied.group_id === "none" ? "未分组" : groups.data?.find(group => String(group.id) === applied.group_id)?.name ?? `#${applied.group_id}`}`,
    applied.sales_owner_id && `销售：${applied.sales_owner_id === "none" ? "无归属" : salesOwners.data?.find(owner => String(owner.id) === applied.sales_owner_id)?.display_name ?? `#${applied.sales_owner_id}`}`,
    applied.company && `公司：${applied.company}`, applied.phone && `资料手机：${applied.phone}`, applied.qq && `QQ：${applied.qq}`,
    applied.certification_status && `认证：${certificationLabels[applied.certification_status] ?? applied.certification_status}`,
    applied.level_id && `符合规则：#${applied.level_id}`,
    ...Object.entries(applied.customFields).map(([key, value]) => `${definitions.fields.find(field => field.field_key === key)?.label ?? key}：${typeof value === "boolean" ? value ? "是" : "否" : value}`),
  ].filter(Boolean);

  const basicFields = <>
    <label className="min-w-0 space-y-1.5 text-sm text-muted-foreground"><span className="block">关键词</span><Input aria-label="客户关键词" placeholder="姓名、用户名或邮箱" value={draft.q} maxLength={120} onChange={event => change({ q: event.target.value })} className="h-9 rounded border-border bg-card" /></label>
    <DirectorySelect label="账号状态" value={draft.status} onChange={status => change({ status })} options={[{ value: "active", label: "已启用" }, { value: "disabled", label: "已停用" }]} />
    <div className="space-y-1.5"><DirectorySelect label="客户分组" value={draft.group_id} onChange={group_id => change({ group_id })} searchable options={[{ value: "none", label: "未分组" }, ...(groups.data ?? []).map(group => ({ value: String(group.id), label: group.name }))]} /><DirectoryOptionState allowed={canReadGroups} data={groups.data} error={groups.error} retry={groups.refresh} label="分组" /></div>
    <div className="space-y-1.5"><DirectorySelect label="销售归属" value={draft.sales_owner_id} onChange={sales_owner_id => change({ sales_owner_id })} searchable options={[{ value: "none", label: "无销售归属" }, ...(salesOwners.data ?? []).map(owner => ({ value: String(owner.id), label: owner.display_name || owner.username }))]} /><DirectoryOptionState allowed={canReadSalesOwners} data={salesOwners.data} error={salesOwners.error} retry={salesOwners.refresh} label="销售" /></div>
  </>;
  const advancedFields = <div className="space-y-5"><div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">{([["company", "公司", 200], ["phone", "资料手机", 80], ["qq", "QQ", 40]] as const).map(([key, label, maxLength]) => <label key={key} className="space-y-1.5 text-sm text-muted-foreground"><span className="block">{label}</span><Input aria-label={label} maxLength={maxLength} placeholder="包含文字" value={draft[key]} onChange={event => change({ [key]: event.target.value })} className="h-9 rounded border-border bg-card" /></label>)}<DirectorySelect label="实名认证" value={draft.certification_status} onChange={certification_status => change({ certification_status })} options={Object.entries(certificationLabels).map(([value, label]) => ({ value, label }))} /><LevelRuleFilter value={draft.level_id} onChange={level_id => change({ level_id })} canReadLevels={canReadLevels} enabled={showAdvanced || mobileOpen} /></div><div className="space-y-3 border-t border-border pt-4"><h3 className="text-sm font-medium text-foreground">自定义字段条件</h3><p className="text-xs text-muted-foreground">文字按包含匹配，其余类型精确匹配；多个条件同时满足。</p>{definitions.error ? <div role="alert" className="text-sm text-destructive">字段定义加载失败。<button type="button" className="ml-2 underline" onClick={definitions.refresh}>重试</button></div> : !definitions.ready ? <p role="status" className="text-sm text-muted-foreground">正在加载字段定义…</p> : !definitions.fields.length && !Object.keys(draft.customFields).length ? <p className="text-sm text-muted-foreground">暂无启用字段</p> : <DirectoryCustomFields fields={definitions.fields} values={draft.customFields} onChange={customFields => change({ customFields })} />}</div></div>;
  const filterButtons = <div className="flex flex-wrap items-center gap-2"><Button type="submit" className="rounded bg-primary hover:bg-primary/90"><Search size={16} />查询</Button><Button type="button" variant="outline" className="rounded shadow-none" onClick={reset}>清空</Button></div>;
  const detailHref = (customer: AdminCustomer) => `/admin/customers/${customer.id}${suffix}`;

  return <AdminListPage
    title="客户列表"
    description="查找客户，进入客户工作区管理资料与业务。"
    actions={canView && <div className="flex gap-2"><Button type="button" variant="outline" className="rounded shadow-none" aria-label="刷新客户列表" disabled={resource.isLoading} onClick={resource.refresh}><RefreshCw size={16} className={resource.isLoading ? "animate-spin" : ""} />{resource.isLoading ? "刷新中" : "刷新"}</Button><PermissionGate permission="customers.manage"><Button asChild className="rounded bg-primary hover:bg-primary/90"><Link href={`/admin/customers/new${suffix}`}><Plus size={16} />新建客户</Link></Button></PermissionGate></div>}
    filters={canView && <section aria-label="客户筛选" className="rounded border border-border bg-card p-4 sm:p-5"><div className="flex items-center justify-between gap-3 sm:hidden"><span className="text-sm text-muted-foreground">按客户资料筛选</span><Button type="button" variant="outline" className="rounded shadow-none" onClick={() => setMobileOpen(true)}><Filter size={16} />筛选</Button></div><form onSubmit={submit} className="hidden space-y-4 sm:block"><div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-[minmax(220px,1.5fr)_repeat(3,minmax(140px,1fr))]">{basicFields}</div><div className="flex flex-wrap items-center justify-between gap-3"><Button type="button" variant="ghost" className="h-8 rounded px-0 text-primary hover:bg-transparent hover:text-primary/90" onClick={() => setShowAdvanced(!showAdvanced)} aria-expanded={showAdvanced} aria-controls="customer-advanced-filters"><Filter size={14} />高级筛选<ChevronDown size={14} className={showAdvanced ? "rotate-180" : ""} /></Button>{filterButtons}</div>{showAdvanced && <div id="customer-advanced-filters" className="border-t border-border pt-5">{advancedFields}</div>}{formError && <p role="alert" className="text-sm text-destructive">{formError}</p>}</form></section>}
    feedback={canView && <div aria-label="已应用的筛选条件" className="flex flex-wrap items-center gap-2 text-xs"><span className="text-muted-foreground">当前条件</span>{summary.length ? <>{summary.map((label, index) => <span key={index} className="max-w-full break-all rounded border border-border bg-card px-2 py-1 text-muted-foreground">{label}</span>)}<button type="button" className="text-primary hover:underline" onClick={reset}>清空条件</button></> : <span className="text-muted-foreground">全部客户</span>}</div>}
    dialogs={canView && <Dialog open={mobileOpen} onOpenChange={setMobileOpen}><DialogContent className="fixed inset-y-0 right-0 left-auto top-0 h-dvh max-h-dvh w-[min(100%,400px)] translate-x-0 translate-y-0 overflow-y-auto rounded-none sm:hidden"><DialogHeader><DialogTitle>筛选客户</DialogTitle><DialogDescription>查询后应用条件，返回客户列表仍保留。</DialogDescription></DialogHeader><form onSubmit={submit} className="space-y-5"><div className="grid gap-4">{basicFields}</div><div className="border-t border-border pt-4">{advancedFields}</div>{formError && <p role="alert" className="text-sm text-destructive">{formError}</p>}<div className="sticky bottom-0 border-t border-border bg-card py-3">{filterButtons}</div></form></DialogContent></Dialog>}
  >
    <AdminListRegion
      title="客户查询结果"
      allowed={canView}
      invalid={initial.error ? <><p className="mb-3">{initial.error}</p><Button variant="outline" onClick={reset}>清空条件</Button></> : null}
      resource={resource}
      retry={resource.refresh}
      summary={resource.data && <span className="tabular-nums">共 {resource.data.total} 位客户</span>}
      pagination={data => <AdminPagination total={data.total} page={page} pageSize={pageSize} pageSizes={Array.from(new Set([25, 50, 100, pageSize])).sort((a, b) => a - b)} onPageChange={next => navigate(applied, pageSize, next * pageSize)} onPageSizeChange={limit => navigate(applied, limit)} />}
    >
      {data => <CustomerDirectoryResults items={data.items} detailHref={detailHref} filtered={summary.length > 0} reset={reset} />}
    </AdminListRegion>
  </AdminListPage>;
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
        className="h-7 text-xs px-2 border-border text-muted-foreground hover:text-primary hover:border-primary"
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
          className="h-7 text-xs px-2 border-border text-muted-foreground hover:text-primary hover:border-primary"
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
        className="h-7 w-7 border-border text-muted-foreground hover:text-primary hover:border-primary"
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
            <DialogDescription className="break-all text-xs text-muted-foreground">
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
      className="h-7 w-7 border-border text-muted-foreground hover:text-primary hover:border-primary"><UserRoundCog size={13} /></Button>
    <Dialog open={open} onOpenChange={value => { if (!mutation.busy) setOpen(value); }}>
      <DialogContent className="max-w-md"><DialogHeader><DialogTitle>销售归属</DialogTitle><DialogDescription className="text-xs text-muted-foreground">调整客户 #{row.id} 的专属销售顾问</DialogDescription></DialogHeader>
        {directory.error && !mutation.pending ? <Failure error={directory.error} retry={directory.refresh} /> : detail.error && !mutation.pending ? <Failure error={detail.error} retry={detail.refresh} /> : (!directory.data || !detail.data) && !mutation.pending ? <Loading /> :
          <form onSubmit={save} className="space-y-4">
            <Select value={frozen ? (frozen.sales_owner_id == null ? "none" : String(frozen.sales_owner_id)) : selectedOwner} onValueChange={setOwner} disabled={mutation.busy || !!mutation.pending}>
              <SelectTrigger className="w-full text-xs border-border" aria-label="销售管理员"><SelectValue /></SelectTrigger>
              <SelectContent><SelectItem value="none">无归属</SelectItem>{directory.data?.map(item => <SelectItem key={item.id} value={String(item.id)}>{item.display_name} · {item.username}</SelectItem>)}</SelectContent>
            </Select>
            {frozen && <p className="text-xs text-muted-foreground">原请求销售归属：{frozen.sales_owner_id == null ? "无归属" : `管理员 #${frozen.sales_owner_id}`}</p>}
            <CustomerMutationNotice mutation={mutation} reload={() => { if (mutation.editRejected()) { setOwner(null); detail.refresh(); refresh(); } }} />
            <div className="flex justify-end gap-2"><Button type="button" variant="outline" size="sm" disabled={mutation.busy} onClick={() => setOpen(false)} className="text-xs border-border">取消</Button>
              <Button disabled={mutation.busy || !mutation.ready || mutation.rejected || (!mutation.pending && (!detail.data || selectedOwner === currentOwner))} size="sm" className="bg-primary hover:bg-primary/90 text-white text-xs"><Save size={13} className="mr-1" />{mutation.busy ? "保存中" : mutation.pending ? "使用原请求重试" : "保存归属"}</Button>
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
    <label className="block text-xs text-muted-foreground">客户分组（单独保存）</label>
    {membership.error && !mutation.pending ? <Failure error={membership.error} retry={membership.refresh} /> : groups.error && !mutation.pending ? <Failure error={groups.error} retry={groups.refresh} /> : (!membership.data || !groups.data) && !mutation.pending ? <Loading /> : <>
      <Select value={selected} onValueChange={value => { setGroupID(value); setSaved(false); }} disabled={mutation.busy || !!mutation.pending}>
        <SelectTrigger className="w-full text-xs h-8 border-border" aria-label="客户分组"><SelectValue placeholder="客户分组" /></SelectTrigger>
        <SelectContent><SelectItem value="none">未分组</SelectItem>{groups.data?.map(group => <SelectItem key={group.id} value={String(group.id)}>{group.name}</SelectItem>)}</SelectContent>
      </Select>
      <CustomerMutationNotice mutation={mutation} reload={() => { if (mutation.editRejected()) { setGroupID(null); membership.refresh(); } }} />
      <div className="flex justify-end"><Button type="submit" size="sm" disabled={mutation.busy || !mutation.ready || mutation.rejected || (!mutation.pending && (!membership.data || selected === current))}>{mutation.busy ? "保存中" : mutation.pending ? "使用原请求重试" : "保存分组"}</Button></div>
      {saved && <p role="status" className="text-xs text-emerald-700">分组已保存</p>}
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
        <div><label className="block text-xs text-muted-foreground mb-1">姓名</label><Input aria-label="客户姓名" value={frozen?.display_name ?? displayName ?? detail.data?.display_name ?? ""} onChange={event => { setDisplayName(event.target.value); setSaved(false); }} className="text-xs h-8 border-border" maxLength={120} required /></div>
        <div><label className="block text-xs text-muted-foreground mb-1">邮箱</label><Input aria-label="客户邮箱" type="email" value={frozen?.email ?? email ?? detail.data?.email ?? ""} onChange={event => { setEmail(event.target.value); setSaved(false); }} className="text-xs h-8 border-border" maxLength={254} required /></div>
        <div><label className="block text-xs text-muted-foreground mb-1">状态</label><Select value={frozen?.status ?? status ?? detail.data?.status ?? "active"} onValueChange={value => { setStatus(value); setSaved(false); }}><SelectTrigger className="w-full text-xs h-8 border-border" aria-label="客户状态"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="active">正常</SelectItem><SelectItem value="disabled">已停用</SelectItem></SelectContent></Select></div>
      </fieldset>
      {!mutation.pending && detail.error && <Failure error={detail.error} retry={detail.refresh} />}
      {!mutation.pending && !detail.data && !detail.error && <Loading />}
      <CustomerMutationNotice mutation={mutation} reload={() => { if (mutation.editRejected()) { setDisplayName(null); setEmail(null); setStatus(null); detail.refresh(); } }} />
      <div className="flex justify-end pt-2"><Button disabled={mutation.busy || !mutation.ready || mutation.rejected || (!mutation.pending && !detail.data)} size="sm" className="bg-primary hover:bg-primary/90 text-white text-xs"><Save size={13} className="mr-1" />{mutation.busy ? "保存中" : mutation.pending ? "使用原请求重试" : "保存资料"}</Button></div>
      {saved && <p role="status" className="text-xs text-emerald-700">资料已保存</p>}
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
        <label className="block text-xs text-muted-foreground mb-1">新密码</label>
        <Input
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          placeholder="请输入至少6位新密码"
          required
          minLength={6}
          className="text-xs h-8 border-border"
        />
      </div>
      <div className="flex justify-end pt-2">
        <Button
          disabled={busy || !password.trim()}
          size="sm"
          className="bg-primary hover:bg-primary/90 text-white text-xs"
        >
          {busy ? "重置中..." : "确认重置"}
        </Button>
      </div>
      {message && <p role="status" className="text-xs text-emerald-700">{message}</p>}
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
        <label className="block text-xs text-muted-foreground mb-1">
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
          className="text-xs h-8 border-border"
        />
      </div>
      <div className="flex justify-end pt-2">
        <Button
          disabled={busy || !amount}
          size="sm"
          className="bg-primary hover:bg-primary/90 text-white text-xs"
        >
          {busy ? "处理中..." : "确认充值"}
        </Button>
      </div>
      {message && <p role="status" className="text-xs text-emerald-700">{message}</p>}
    </form>
  );
}
