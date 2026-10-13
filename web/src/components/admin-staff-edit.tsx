"use client";

import Link from "next/link";
import { useRef, useState } from "react";
import { api, ApiError, type TicketDepartment } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { useAdminFormLeave } from "@/hooks/use-admin-form-leave";
import type { Staff, StaffForm, Role } from "./admin-access";
import { StaffFields } from "./admin-staff-fields";
import { Workspace } from "./workspace";
import { Loading, Failure } from "./common";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "./ui/dialog";

export function AdminStaffEditView({ id }: { id: number }) {
  const staff = useResource<Staff>(`/admin/staff/${id}`, false);
  const roles = useResource<Role[]>("/admin/roles");
  const departments = useResource<TicketDepartment[]>("/admin/ticket-departments");
  const [notice, setNotice] = useState("");
  const error = staff.error ?? roles.error ?? departments.error;
  const refresh = () => { staff.refresh(); roles.refresh(); departments.refresh(); };
  return <Workspace title="编辑管理员" admin>
    <div className="flex flex-wrap justify-between gap-3 mb-6"><h1 className="page-title">编辑管理员</h1><Button variant="outline" asChild><Link href="/admin/access">返回管理员列表</Link></Button></div>
    {notice && <p role="status" className="mb-4 text-sm text-emerald-700">{notice}</p>}
    {error ? <Failure error={error} retry={refresh} /> : !staff.data || !roles.data || !departments.data ? <Loading /> : staff.data.role !== "admin" ? <p role="status" className="data-surface p-6">超级管理员账号受保护，请前往个人账户设置修改。</p> :
      <StaffEditor key={`${id}:${staff.data.admin_version}`} staff={staff.data} roles={roles.data} departments={departments.data} reload={refresh} saved={() => { setNotice("管理员资料已保存，账号权限变更会使原会话失效。"); refresh(); }} />}
  </Workspace>;
}

function StaffEditor({ staff, roles, departments, reload, saved }: { staff: Staff; roles: Role[]; departments: TicketDepartment[]; reload: () => void; saved: () => void }) {
  const original: StaffForm = { id: staff.id, username: staff.username, email: staff.email, display_name: staff.display_name, status: staff.status, admin_role_id: String(staff.admin_role_id), password: "", expected_version: staff.admin_version, ticket_scope: staff.ticket_scope ?? "departments", ticket_unassigned: staff.ticket_unassigned ?? false, ticket_department_ids: staff.ticket_department_ids ?? [], customer_scope: staff.customer_scope ?? "owned", customer_ownerless: staff.customer_ownerless ?? false, sales_enabled: staff.sales_enabled ?? false };
  const [form, setForm] = useState(original);
  const [confirmPassword, setConfirmPassword] = useState("");
  const [dialog, setDialog] = useState<"save" | "reset" | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [conflict, setConflict] = useState(false);
  const [denied, setDenied] = useState(false);
  const [unknown, setUnknown] = useState(false);
  const [awaitingReload, setAwaitingReload] = useState(false);
  useAdminFormLeave(!awaitingReload && (unknown || JSON.stringify(form) !== JSON.stringify(original) || confirmPassword !== ""));
  const pending = useRef<{ body: string; key: string } | null>(null);
  const inFlight = useRef(false);
  async function save() {
    if (inFlight.current) return;
    inFlight.current = true; setBusy(true); setError("");
    const { id, ...fields } = form;
    const body = JSON.stringify({ ...fields, admin_role_id: Number(fields.admin_role_id) });
    if (pending.current?.body !== body) pending.current = { body, key: crypto.randomUUID() };
    try {
      await api(`/admin/staff/${id}`, { method: "PATCH", body, headers: { "Idempotency-Key": pending.current.key } });
      setDialog(null); setAwaitingReload(true); saved();
    } catch (e) {
      setError((e as Error).message); setDialog(null);
      setConflict(e instanceof ApiError && e.code === "version_conflict");
      setDenied(e instanceof ApiError && (e.status === 401 || e.status === 403));
      setUnknown(!(e instanceof ApiError) || e.status >= 500 || e.status === 408);
    } finally { inFlight.current = false; setBusy(false); }
  }
  function reset() { setForm(original); setConfirmPassword(""); setError(""); setConflict(false); setDialog(null); }
  return <div className="data-surface p-4 sm:p-6 max-w-4xl">
    <p className="text-sm text-muted-foreground mb-6">版本 {staff.admin_version} · 修改角色、客户范围、工单范围、密码或账号状态时，现有会话与 API 密钥将被撤销。</p>
    <form onSubmit={event => { event.preventDefault(); setError(""); if (form.password !== confirmPassword) { setError("两次输入的密码不一致"); return; } setDialog("save"); }}>
      <fieldset disabled={busy || denied || unknown || awaitingReload} className="space-y-4 min-w-0">
        <StaffFields staffForm={form} setStaffForm={setForm} roles={roles} departments={departments} />
        <div className="space-y-2"><Label htmlFor="staff-confirm-password">确认新密码</Label><Input id="staff-confirm-password" type="password" value={confirmPassword} onChange={event => setConfirmPassword(event.target.value)} autoComplete="new-password" maxLength={72} /></div>
        {error && <p role="alert" className="text-destructive text-sm break-words">{error}</p>}
        {conflict && <Button type="button" variant="outline" onClick={reload}>重新加载最新资料（丢弃当前更改）</Button>}
        <div className="flex flex-wrap gap-3 border-t pt-4">{!denied && <Button disabled={form.admin_role_id === "none" || busy || conflict}>{busy ? "保存中" : "提交"}</Button>}<Button type="button" variant="outline" onClick={() => setDialog("reset")}>重置 / 取消更改</Button></div>
      </fieldset>
    </form>
    {awaitingReload && <p role="status" className="mt-4 text-sm">已保存，正在读取最新资料。<Button type="button" variant="outline" onClick={reload}>重新读取</Button></p>}
    {unknown && <div role="alert" className="mt-4 space-y-3 text-sm"><p>保存结果尚未确认，请重试原请求；确认前已锁定表单。</p><Button disabled={busy} onClick={() => void save()}>重试原保存请求</Button></div>}
    <Dialog open={dialog !== null} onOpenChange={open => { if (!open && !busy) setDialog(null); }}><DialogContent><DialogHeader><DialogTitle>{dialog === "save" ? "确认修改管理员" : "取消当前更改"}</DialogTitle><DialogDescription>{dialog === "save" ? "请确认账号状态与授权范围。权限或凭据变更会撤销该管理员原有会话和 API 密钥。" : "重置后将恢复本次加载的资料，未提交的更改将丢弃。"}</DialogDescription></DialogHeader><div className="flex justify-end gap-2"><Button variant="outline" disabled={busy} onClick={() => setDialog(null)}>取消</Button><Button disabled={busy} onClick={() => dialog === "save" ? void save() : reset()}>{busy ? "保存中" : "确定"}</Button></div></DialogContent></Dialog>
  </div>;
}
