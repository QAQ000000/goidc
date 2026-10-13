"use client";
import type { StaffForm, Role } from "./admin-access";
import type { TicketDepartment } from "@/lib/api";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Checkbox } from "./ui/checkbox";
import { Select, SelectTrigger, SelectValue, SelectContent, SelectItem } from "./ui/select";

export function StaffFields({ staffForm, setStaffForm, roles, departments }: {
 staffForm: StaffForm; setStaffForm: (form: StaffForm) => void; roles: Role[]; departments: TicketDepartment[];
}) {
 return <div className="space-y-4 min-w-0">
      <div className="space-y-2"><Label htmlFor="staff-username">用户名</Label><Input id="staff-username" value={staffForm.username} onChange={e => setStaffForm({ ...staffForm, username: e.target.value })} required minLength={3} maxLength={48} pattern="(?:[a-z0-9_]|-)+" autoComplete="off" /></div>
      <div className="space-y-2"><Label htmlFor="staff-name">姓名</Label><Input id="staff-name" value={staffForm.display_name} onChange={e => setStaffForm({ ...staffForm, display_name: e.target.value })} required maxLength={120} /></div>
      <div className="space-y-2"><Label htmlFor="staff-email">邮箱</Label><Input id="staff-email" type="email" value={staffForm.email} onChange={e => setStaffForm({ ...staffForm, email: e.target.value })} required maxLength={254} /></div>
      <div className="space-y-2"><Label htmlFor="staff-role">角色</Label><Select value={staffForm.admin_role_id} onValueChange={value => setStaffForm({ ...staffForm, admin_role_id: value })}><SelectTrigger id="staff-role" className="w-full"><SelectValue placeholder="选择角色" /></SelectTrigger><SelectContent><SelectItem value="none">选择角色</SelectItem>{roles?.map(role => <SelectItem key={role.id} value={String(role.id)} disabled={!role.active}>{role.name}{!role.active ? "（已停用）" : ""}</SelectItem>)}</SelectContent></Select></div>
      <div className="space-y-2"><Label htmlFor="staff-password">{staffForm.id ? "新密码（留空保留）" : "密码"}</Label><Input id="staff-password" type="password" value={staffForm.password} onChange={e => setStaffForm({ ...staffForm, password: e.target.value })} required={!staffForm.id} minLength={8} maxLength={72} autoComplete="new-password" /></div>
      <label className="flex gap-2 items-center text-sm"><Checkbox checked={staffForm.status === "active"} onCheckedChange={checked => setStaffForm({ ...staffForm, status: checked ? "active" : "disabled" })} />启用账号</label>
      <fieldset className="border-t pt-4 space-y-3"><legend className="font-medium text-sm">客户范围</legend>
        <Select value={staffForm.customer_scope} onValueChange={value => setStaffForm({ ...staffForm, customer_scope: value as StaffForm["customer_scope"], customer_ownerless: value === "all" ? false : staffForm.customer_ownerless })}><SelectTrigger aria-label="客户范围" className="w-full"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="owned">本人名下客户</SelectItem><SelectItem value="assigned">所有已归属客户</SelectItem><SelectItem value="all">全部客户</SelectItem></SelectContent></Select>
        {staffForm.customer_scope !== "all" && <label className="flex items-center gap-2 text-sm"><Checkbox checked={staffForm.customer_ownerless} onCheckedChange={checked => setStaffForm({ ...staffForm, customer_ownerless: checked === true })} />无归属客户</label>}
        <label className="flex items-center gap-2 text-sm"><Checkbox checked={staffForm.sales_enabled} onCheckedChange={checked => setStaffForm({ ...staffForm, sales_enabled: checked === true })} />销售资格</label>
      </fieldset>
      <fieldset className="border-t pt-4 space-y-3"><legend className="font-medium text-sm">工单部门范围</legend>
        <Select value={staffForm.ticket_scope} onValueChange={value => setStaffForm({ ...staffForm, ticket_scope: value as "all" | "departments" })}><SelectTrigger aria-label="工单部门范围" className="w-full"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="departments">指定部门</SelectItem><SelectItem value="all">所有部门及未指定工单</SelectItem></SelectContent></Select>
        {staffForm.ticket_scope === "departments" && <div className="space-y-2"><label className="flex items-center gap-2 text-sm"><Checkbox checked={staffForm.ticket_unassigned} onCheckedChange={checked => setStaffForm({ ...staffForm, ticket_unassigned: checked === true })} />未指定部门工单</label>{departments?.map(department => <label key={department.id} className="flex items-start gap-2 text-sm"><Checkbox className="mt-0.5" checked={staffForm.ticket_department_ids.includes(department.id)} onCheckedChange={checked => setStaffForm({ ...staffForm, ticket_department_ids: checked === true ? [...staffForm.ticket_department_ids, department.id] : staffForm.ticket_department_ids.filter(id => id !== department.id) })} /><span className="break-words">{department.name}{department.active === false && "（已停用）"}</span></label>)}</div>}
      </fieldset>
 </div>;
}
