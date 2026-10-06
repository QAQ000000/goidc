"use client";

import { useState } from "react";
import { Plus, Pencil, Trash2, Save, Search, ChevronLeft, ChevronRight, Copy, ShieldCheck } from "lucide-react";
import { api, type User, type TicketDepartment, date } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace } from "./workspace";
import { Loading, Failure, Status } from "./common";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Textarea } from "./ui/textarea";
import { Checkbox } from "./ui/checkbox";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "./ui/dialog";
import { Select, SelectTrigger, SelectValue, SelectContent, SelectItem } from "./ui/select";
import { Table, TableHeader, TableHead, TableRow, TableBody, TableCell } from "./ui/table";

type Permission = { code: string; group: string; label: string };
type Role = { id: number; name: string; description: string; active: boolean; permissions: string[]; version: number; member_count: number };
type Staff = User & { status: string; admin_role_id: number | null; admin_role_name: string | null; admin_role_active: boolean | null; admin_version: number; mfa_enabled: boolean; created_at: string; ticket_department_ids: number[] };
type RoleForm = { id?: number; name: string; description: string; active: boolean; permissions: string[]; expected_version: number };
type StaffForm = { id?: number; username: string; email: string; display_name: string; status: string; admin_role_id: string; password: string; expected_version: number; ticket_scope: "all" | "departments"; ticket_unassigned: boolean; ticket_department_ids: number[]; customer_scope: "all" | "owned" | "assigned"; customer_ownerless: boolean; sales_enabled: boolean };
const blankRole = (): RoleForm => ({ name: "", description: "", active: true, permissions: [], expected_version: 0 });
const blankStaff = (): StaffForm => ({ username: "", email: "", display_name: "", status: "active", admin_role_id: "none", password: "", expected_version: 0, ticket_scope: "departments", ticket_unassigned: false, ticket_department_ids: [], customer_scope: "owned", customer_ownerless: false, sales_enabled: false });

export function AdminAccessView() {
  const roles = useResource<Role[]>("/admin/roles");
  const permissions = useResource<Permission[]>("/admin/permissions");
  const departments = useResource<TicketDepartment[]>("/admin/ticket-departments");
  const [query, setQuery] = useState("");
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(0);
  const staff = useResource<Staff[]>(`/admin/staff?limit=20&offset=${page * 20}&q=${encodeURIComponent(search)}`);
  const [tab, setTab] = useState<"staff" | "roles">("staff");
  const [roleForm, setRoleForm] = useState<RoleForm | null>(null);
  const [staffForm, setStaffForm] = useState<StaffForm | null>(null);
  const [deleting, setDeleting] = useState<Role | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const failure = roles.error ?? permissions.error ?? staff.error ?? departments.error;
  const refresh = () => { roles.refresh(); staff.refresh(); permissions.refresh(); departments.refresh(); };
  const begin = () => { setError(""); setNotice(""); };

  async function saveRole(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault(); if (!roleForm || busy) return;
    setBusy(true); setError("");
    const { id, ...payload } = roleForm;
    try {
      await api(id ? `/admin/roles/${id}` : "/admin/roles", { method: id ? "PATCH" : "POST", body: JSON.stringify(payload) });
      setRoleForm(null); refresh(); setNotice("角色已保存");
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  async function saveStaff(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault(); if (!staffForm || busy) return;
    setBusy(true); setError("");
    const { id, ...fields } = staffForm;
    try {
      await api(id ? `/admin/staff/${id}` : "/admin/staff", { method: id ? "PATCH" : "POST", body: JSON.stringify({ ...fields, admin_role_id: Number(fields.admin_role_id) }) });
      setStaffForm(null); refresh(); setNotice("管理员已保存");
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  async function deleteRole() {
    if (!deleting || busy) return;
    setBusy(true); setError("");
    try {
      await api(`/admin/roles/${deleting.id}`, { method: "DELETE", body: JSON.stringify({ expected_version: deleting.version }) });
      setDeleting(null); refresh(); setNotice("角色已删除");
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  function togglePermission(code: string, checked: boolean) {
    if (!roleForm || !permissions.data) return;
    const selected = new Set(roleForm.permissions);
    if (checked) {
      selected.add(code);
      const view = code === "invoices.refund" ? "invoices.view" : code.replace(/\.manage$/, ".view");
      if (permissions.data.some(p => p.code === view)) selected.add(view);
    } else {
      selected.delete(code);
      if (code.endsWith(".view")) {
        selected.delete(code.replace(/\.view$/, ".manage"));
        if (code === "invoices.view") selected.delete("invoices.refund");
      }
    }
    setRoleForm({ ...roleForm, permissions: [...selected] });
  }

  return <Workspace title="管理员与权限" admin>
    <div className="flex flex-wrap items-center justify-between gap-4 mb-6">
      <h1 className="page-title">管理员与权限</h1>
      {!failure && <Button disabled={!roles.data || !permissions.data || !departments.data} onClick={() => { begin(); if (tab === "staff") setStaffForm(blankStaff()); else setRoleForm(blankRole()); }}><Plus />{tab === "staff" ? "新增管理员" : "新增角色"}</Button>}
    </div>
    <div role="tablist" aria-label="账号与角色" className="flex border-b mb-6 gap-6">
      {([['staff', '管理员'], ['roles', '角色权限']] as const).map(([value, label]) => <button key={value} role="tab" aria-selected={tab === value} aria-controls={`access-${value}`} id={`tab-${value}`} onClick={() => { setTab(value); begin(); }} className={`pb-3 border-b-2 text-sm ${tab === value ? "border-primary font-semibold" : "border-transparent text-muted-foreground"}`}>{label}</button>)}
    </div>
    {notice && <p role="status" className="text-sm text-emerald-700 mb-4">{notice}</p>}
    {failure ? <Failure error={failure} retry={refresh} /> : !roles.data || !staff.data || !permissions.data || !departments.data ? <Loading /> : tab === "staff" ?
      <section role="tabpanel" id="access-staff" aria-labelledby="tab-staff">
        <form onSubmit={event => { event.preventDefault(); setPage(0); setSearch(query.trim()); }} className="flex gap-2 mb-4 max-w-md"><Input aria-label="搜索管理员" value={query} onChange={e => setQuery(e.target.value)} placeholder="用户名或姓名" maxLength={120} /><Button variant="outline" size="icon" title="搜索管理员" aria-label="搜索"><Search /></Button></form>
        <div className="data-surface overflow-x-auto"><Table><TableHeader><TableRow><TableHead>管理员</TableHead><TableHead>角色</TableHead><TableHead>状态</TableHead><TableHead>两步验证</TableHead><TableHead>创建时间</TableHead><TableHead className="text-right">操作</TableHead></TableRow></TableHeader><TableBody>
          {staff.data.map(person => <TableRow key={person.id}><TableCell><div className="font-medium max-w-60 whitespace-normal break-all">{person.display_name}</div><div className="text-xs text-muted-foreground max-w-60 whitespace-normal break-all">{person.username}<br />{person.email}</div></TableCell><TableCell className="whitespace-normal max-w-48 break-all">{person.role === "super_admin" ? <span className="inline-flex items-center gap-1"><ShieldCheck size={14} />超级管理员</span> : <>{person.admin_role_name}{person.admin_role_active === false && <span className="block text-destructive text-xs">角色已停用</span>}</>}</TableCell><TableCell><Status value={person.status} /></TableCell><TableCell>{person.mfa_enabled ? "已启用" : "未启用"}</TableCell><TableCell>{date(person.created_at)}</TableCell><TableCell className="text-right">{person.role === "admin" && <Button variant="ghost" size="icon-sm" title="编辑管理员" aria-label={`编辑 ${person.username}`} onClick={() => { begin(); setStaffForm({ id: person.id, username: person.username, email: person.email, display_name: person.display_name, status: person.status, admin_role_id: String(person.admin_role_id), password: "", expected_version: person.admin_version, ticket_scope: person.ticket_scope ?? "departments", ticket_unassigned: person.ticket_unassigned ?? false, ticket_department_ids: person.ticket_department_ids ?? [], customer_scope: person.customer_scope ?? "owned", customer_ownerless: person.customer_ownerless ?? false, sales_enabled: person.sales_enabled ?? false }); }}><Pencil /></Button>}</TableCell></TableRow>)}
          {staff.data.length === 0 && <TableRow><TableCell colSpan={6} className="py-10 text-center text-muted-foreground">暂无管理员</TableCell></TableRow>}
        </TableBody></Table></div>
        <div className="flex justify-end items-center gap-3 mt-4"><Button variant="outline" size="icon" aria-label="上一页" title="上一页" disabled={page === 0} onClick={() => setPage(page - 1)}><ChevronLeft /></Button><span className="text-sm">第 {page + 1} 页</span><Button variant="outline" size="icon" aria-label="下一页" title="下一页" disabled={staff.data.length < 20} onClick={() => setPage(page + 1)}><ChevronRight /></Button></div>
      </section> :
      <section role="tabpanel" id="access-roles" aria-labelledby="tab-roles"><div className="data-surface overflow-x-auto"><Table><TableHeader><TableRow><TableHead>角色</TableHead><TableHead>状态</TableHead><TableHead>权限</TableHead><TableHead>管理员</TableHead><TableHead className="text-right">操作</TableHead></TableRow></TableHeader><TableBody>
        {roles.data.map(role => <TableRow key={role.id}><TableCell className="max-w-80 whitespace-normal break-all"><div className="font-medium">{role.name}</div><div className="text-xs text-muted-foreground mt-1">{role.description}</div></TableCell><TableCell><Status value={role.active ? "active" : "disabled"} /></TableCell><TableCell>{role.permissions.length} 项</TableCell><TableCell>{role.member_count}</TableCell><TableCell><div className="flex justify-end gap-1"><Button variant="ghost" size="icon-sm" title="编辑角色" aria-label={`编辑角色 ${role.name}`} onClick={() => { begin(); setRoleForm({ id: role.id, name: role.name, description: role.description, active: role.active, permissions: role.permissions, expected_version: role.version }); }}><Pencil /></Button><Button variant="ghost" size="icon-sm" title="复制角色" aria-label={`复制角色 ${role.name}`} onClick={() => { begin(); setRoleForm({ ...blankRole(), name: `${role.name.slice(0, 70)} 副本`, description: role.description, permissions: [...role.permissions] }); }}><Copy /></Button><Button variant="ghost" size="icon-sm" title={role.member_count > 0 ? "角色仍有关联管理员" : "删除角色"} aria-label={`删除角色 ${role.name}`} disabled={role.member_count > 0} onClick={() => { begin(); setDeleting(role); }}><Trash2 /></Button></div></TableCell></TableRow>)}
        {roles.data.length === 0 && <TableRow><TableCell colSpan={5} className="py-10 text-center text-muted-foreground">暂无角色</TableCell></TableRow>}
      </TableBody></Table></div></section>}

    <Dialog open={roleForm !== null} onOpenChange={open => { if (!open && !busy) setRoleForm(null); }}><DialogContent className="sm:max-w-2xl max-h-[90dvh] overflow-y-auto grid-cols-[minmax(0,1fr)]"><DialogHeader><DialogTitle>{roleForm?.id ? "编辑角色" : "新增角色"}</DialogTitle><DialogDescription>权限配置</DialogDescription></DialogHeader>{roleForm && <form onSubmit={saveRole} className="min-w-0"><fieldset disabled={busy} className="space-y-4 min-w-0">
      <div className="space-y-2"><Label htmlFor="role-name">角色名称</Label><Input id="role-name" value={roleForm.name} onChange={e => setRoleForm({ ...roleForm, name: e.target.value })} required maxLength={80} /></div>
      <div className="space-y-2"><Label htmlFor="role-description">描述</Label><Textarea id="role-description" value={roleForm.description} onChange={e => setRoleForm({ ...roleForm, description: e.target.value })} maxLength={500} rows={2} /></div>
      <label className="flex items-center gap-2 text-sm"><Checkbox checked={roleForm.active} onCheckedChange={checked => setRoleForm({ ...roleForm, active: checked === true })} />启用角色</label>
      <div className="grid sm:grid-cols-2 gap-x-6 gap-y-4 border-t pt-4">{[...new Set(permissions.data?.map(p => p.group))].map(group => <fieldset key={group} className="min-w-0"><legend className="font-medium text-sm mb-2">{group}</legend><div className="space-y-2">{permissions.data?.filter(p => p.group === group).map(permission => <label key={permission.code} className="flex gap-2 items-start text-sm"><Checkbox className="mt-0.5" checked={roleForm.permissions.includes(permission.code)} onCheckedChange={checked => togglePermission(permission.code, checked === true)} /><span className="break-words">{permission.label}</span></label>)}</div></fieldset>)}</div>
      {error && <p role="alert" className="text-destructive text-sm">{error}</p>}<div className="flex justify-end"><Button><Save />{busy ? "保存中" : "保存角色"}</Button></div>
    </fieldset></form>}</DialogContent></Dialog>

    <Dialog open={staffForm !== null} onOpenChange={open => { if (!open && !busy) setStaffForm(null); }}><DialogContent className="max-h-[90dvh] overflow-y-auto grid-cols-[minmax(0,1fr)]"><DialogHeader><DialogTitle>{staffForm?.id ? "编辑管理员" : "新增管理员"}</DialogTitle><DialogDescription>普通管理员账户</DialogDescription></DialogHeader>{staffForm && <form onSubmit={saveStaff}><fieldset disabled={busy} className="space-y-4 min-w-0">
      <div className="space-y-2"><Label htmlFor="staff-username">用户名</Label><Input id="staff-username" value={staffForm.username} onChange={e => setStaffForm({ ...staffForm, username: e.target.value })} required minLength={3} maxLength={48} pattern="(?:[a-z0-9_]|-)+" autoComplete="off" /></div>
      <div className="space-y-2"><Label htmlFor="staff-name">姓名</Label><Input id="staff-name" value={staffForm.display_name} onChange={e => setStaffForm({ ...staffForm, display_name: e.target.value })} required maxLength={120} /></div>
      <div className="space-y-2"><Label htmlFor="staff-email">邮箱</Label><Input id="staff-email" type="email" value={staffForm.email} onChange={e => setStaffForm({ ...staffForm, email: e.target.value })} required maxLength={254} /></div>
      <div className="space-y-2"><Label htmlFor="staff-role">角色</Label><Select value={staffForm.admin_role_id} onValueChange={value => setStaffForm({ ...staffForm, admin_role_id: value })}><SelectTrigger id="staff-role" className="w-full"><SelectValue placeholder="选择角色" /></SelectTrigger><SelectContent><SelectItem value="none">选择角色</SelectItem>{roles.data?.map(role => <SelectItem key={role.id} value={String(role.id)} disabled={!role.active}>{role.name}{!role.active ? "（已停用）" : ""}</SelectItem>)}</SelectContent></Select></div>
      <div className="space-y-2"><Label htmlFor="staff-password">{staffForm.id ? "新密码（留空保留）" : "密码"}</Label><Input id="staff-password" type="password" value={staffForm.password} onChange={e => setStaffForm({ ...staffForm, password: e.target.value })} required={!staffForm.id} minLength={8} maxLength={72} autoComplete="new-password" /></div>
      <label className="flex gap-2 items-center text-sm"><Checkbox checked={staffForm.status === "active"} onCheckedChange={checked => setStaffForm({ ...staffForm, status: checked ? "active" : "disabled" })} />启用账号</label>
      <fieldset className="border-t pt-4 space-y-3"><legend className="font-medium text-sm">客户范围</legend>
        <Select value={staffForm.customer_scope} onValueChange={value => setStaffForm({ ...staffForm, customer_scope: value as StaffForm["customer_scope"], customer_ownerless: value === "all" ? false : staffForm.customer_ownerless })}><SelectTrigger aria-label="客户范围" className="w-full"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="owned">本人名下客户</SelectItem><SelectItem value="assigned">所有已归属客户</SelectItem><SelectItem value="all">全部客户</SelectItem></SelectContent></Select>
        {staffForm.customer_scope !== "all" && <label className="flex items-center gap-2 text-sm"><Checkbox checked={staffForm.customer_ownerless} onCheckedChange={checked => setStaffForm({ ...staffForm, customer_ownerless: checked === true })} />无归属客户</label>}
        <label className="flex items-center gap-2 text-sm"><Checkbox checked={staffForm.sales_enabled} onCheckedChange={checked => setStaffForm({ ...staffForm, sales_enabled: checked === true })} />销售资格</label>
      </fieldset>
      <fieldset className="border-t pt-4 space-y-3"><legend className="font-medium text-sm">工单部门范围</legend>
        <Select value={staffForm.ticket_scope} onValueChange={value => setStaffForm({ ...staffForm, ticket_scope: value as "all" | "departments" })}><SelectTrigger aria-label="工单部门范围" className="w-full"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="departments">指定部门</SelectItem><SelectItem value="all">所有部门及未指定工单</SelectItem></SelectContent></Select>
        {staffForm.ticket_scope === "departments" && <div className="space-y-2"><label className="flex items-center gap-2 text-sm"><Checkbox checked={staffForm.ticket_unassigned} onCheckedChange={checked => setStaffForm({ ...staffForm, ticket_unassigned: checked === true })} />未指定部门工单</label>{departments.data?.map(department => <label key={department.id} className="flex items-start gap-2 text-sm"><Checkbox className="mt-0.5" checked={staffForm.ticket_department_ids.includes(department.id)} onCheckedChange={checked => setStaffForm({ ...staffForm, ticket_department_ids: checked === true ? [...staffForm.ticket_department_ids, department.id] : staffForm.ticket_department_ids.filter(id => id !== department.id) })} /><span className="break-words">{department.name}{department.active === false && "（已停用）"}</span></label>)}</div>}
      </fieldset>
      {error && <p role="alert" className="text-destructive text-sm">{error}</p>}<div className="flex justify-end"><Button disabled={staffForm.admin_role_id === "none"}><Save />{busy ? "保存中" : "保存管理员"}</Button></div>
    </fieldset></form>}</DialogContent></Dialog>

    <Dialog open={deleting !== null} onOpenChange={open => { if (!open && !busy) setDeleting(null); }}><DialogContent className="grid-cols-[minmax(0,1fr)]"><DialogHeader><DialogTitle>删除角色</DialogTitle><DialogDescription className="break-all">{deleting?.name}</DialogDescription></DialogHeader>{error && <p role="alert" className="text-destructive text-sm">{error}</p>}<div className="flex justify-end gap-2"><Button variant="outline" disabled={busy} onClick={() => setDeleting(null)}>取消</Button><Button variant="destructive" disabled={busy} onClick={() => void deleteRole()}><Trash2 />删除</Button></div></DialogContent></Dialog>
  </Workspace>;
}
