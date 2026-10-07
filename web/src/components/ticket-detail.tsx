"use client";

import { useRef, useState } from "react";
import Link from "next/link";
import { ArrowLeft, Check, Download, GitMerge, Paperclip, Send, StickyNote, Trash2, UserRoundCheck } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { api, date, download, type Ticket, type TicketAssignee, type TicketAttachment, type TicketDepartment, type TicketReplyPreset, type User, upload } from "@/lib/api";
import { can } from "@/lib/admin-navigation";
import { useResource } from "@/hooks/use-resource";
import { Workspace, PermissionGate } from "./workspace";
import { Failure, Loading, Status } from "./common";

function fileSize(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
}

function AttachmentRow({ attachment, ticketID, onDelete, admin }: { attachment: TicketAttachment; ticketID: number; admin: boolean; onDelete: (attachment: TicketAttachment) => void }) {
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  async function save() {
    setBusy(true);
    setMessage("");
    try {
      const blob = await download(`/tickets/${ticketID}/attachments/${attachment.id}`);
      const href = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = href;
      link.download = attachment.filename;
      document.body.appendChild(link);
      link.click();
      link.remove();
      URL.revokeObjectURL(href);
    } catch (error) {
      setMessage((error as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return <li className="flex flex-wrap items-center justify-between gap-3 border-t py-3">
    <div className="min-w-0">
      <p className="truncate font-medium" title={attachment.filename}>{attachment.filename}</p>
      {(attachment.note_id != null || attachment.reply_id != null) && <p className="text-xs text-muted-foreground">{attachment.note_id != null ? "内部备注附件 · 仅管理员可见" : "回复附件"}</p>}
      <p className="text-xs text-muted-foreground">{fileSize(attachment.size_bytes)} · {attachment.uploader_name ?? "工单参与者"} · {date(attachment.created_at)}</p>
      {message && <p className="text-xs text-destructive mt-1" role="alert">{message}</p>}
    </div>
    <div className="flex shrink-0 items-center gap-2">
      <Button variant="outline" size="sm" disabled={busy} onClick={() => void save()}><Download />{busy ? "下载中" : "下载"}</Button>
      <PermissionGate permission="tickets.manage" bypass={!admin}><Button variant="ghost" size="icon-sm" aria-label={`删除 ${attachment.filename}`} title="删除附件" onClick={() => onDelete(attachment)}><Trash2 /></Button></PermissionGate>
    </div>
  </li>;
}

export function TicketDetailView({ id, admin = false }: { id: number; admin?: boolean }) {
  const { data: user } = useResource<User>("/me");
  const { data, error, refresh } = useResource<Ticket>(`/tickets/${id}`);
  const { data: presets } = useResource<TicketReplyPreset[]>(admin && can(user, "tickets.manage") && can(user, "ticket_presets.view") ? "/admin/ticket-reply-presets" : null);
  const fileRef = useRef<HTMLInputElement>(null);
  const [selectedFile, setSelectedFile] = useState<File | null>(null);
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [attachmentBusy, setAttachmentBusy] = useState(false);
  const [attachmentMessage, setAttachmentMessage] = useState("");
  const [dialog, setDialog] = useState(false);
  const [assignmentDepartment, setAssignmentDepartment] = useState<string | null>(null);
  const [assignmentAssignee, setAssignmentAssignee] = useState<string | null>(null);
  const selectedDepartment = assignmentDepartment ?? (data?.department_id == null ? "none" : String(data.department_id));
  const staffAccess = admin && data?.admin_access;
  const { data: departments } = useResource<TicketDepartment[]>(staffAccess ? "/admin/ticket-departments" : null);
  const { data: assignees, error: assigneeError, refresh: refreshAssignees } = useResource<TicketAssignee[]>(staffAccess && can(user, "tickets.manage") ? `/admin/ticket-assignees?department_id=${selectedDepartment === "none" ? "0" : selectedDepartment}` : null, false);
  const [assignmentBusy, setAssignmentBusy] = useState(false);
  const [assignmentMessage, setAssignmentMessage] = useState("");
  const [presetID, setPresetID] = useState("none");
  const [workflowBusy, setWorkflowBusy] = useState(false);
  const [workflowMessage, setWorkflowMessage] = useState("");
  const [transferReason, setTransferReason] = useState("");
  const [mergeTarget, setMergeTarget] = useState("");
  const [noteBody, setNoteBody] = useState("");
  const [panel, setPanel] = useState("reply");

  async function mutate(close = false) {
    if (!data) return;
    setBusy(true);
    setMessage("");
    try {
      await api(`/tickets/${id}${close ? "" : "/replies"}`, { method: close ? "PATCH" : "POST", body: JSON.stringify({ version: data.version, ...(!close ? { body } : {}) }) });
      if (!close) setBody("");
      setDialog(false);
      refresh();
    } catch (error) {
      setMessage((error as Error).message);
      refresh();
    } finally {
      setBusy(false);
    }
  }

  async function addAttachment(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selectedFile) return;
    setAttachmentBusy(true);
    setAttachmentMessage("");
    try {
      const form = new FormData();
      form.append("file", selectedFile);
      await upload(`/tickets/${id}/attachments`, form);
      setSelectedFile(null);
      if (fileRef.current) fileRef.current.value = "";
      refresh();
    } catch (error) {
      setAttachmentMessage((error as Error).message);
    } finally {
      setAttachmentBusy(false);
    }
  }

  async function removeAttachment(attachment: TicketAttachment) {
    if (!window.confirm(`删除附件“${attachment.filename}”？`)) return;
    setAttachmentMessage("");
    try {
      await api(`/tickets/${id}/attachments/${attachment.id}`, { method: "DELETE" });
      refresh();
    } catch (error) {
      setAttachmentMessage((error as Error).message);
    }
  }

  const saveAssignment = async () => {
    if (!data) return;
    setAssignmentBusy(true);
    setAssignmentMessage("");
    try {
      const departmentValue = assignmentDepartment ?? (data.department_id === null ? "none" : String(data.department_id));
      const assigneeValue = assignmentAssignee ?? (data.assignee_id === null ? "none" : String(data.assignee_id));
      await api(`/admin/tickets/${id}/assignment`, { method: "PATCH", body: JSON.stringify({ department_id: departmentValue === "none" ? null : Number(departmentValue), assignee_id: assigneeValue === "none" ? null : Number(assigneeValue) }) });
      setAssignmentMessage("已保存");
      setAssignmentDepartment(null);
      setAssignmentAssignee(null);
      refresh();
    } catch (error) {
      setAssignmentMessage((error as Error).message);
    } finally {
      setAssignmentBusy(false);
    }
  };

  async function receiveTicket() {
    setWorkflowBusy(true);
    setWorkflowMessage("");
    try {
      await api(`/admin/tickets/${id}/receive`, { method: "POST", body: JSON.stringify({}) });
      refresh();
    } catch (error) {
      setWorkflowMessage((error as Error).message);
    } finally {
      setWorkflowBusy(false);
    }
  }

  async function transferTicket() {
    if (!data) return;
    const departmentValue = assignmentDepartment ?? (data.department_id === null ? "none" : String(data.department_id));
    const assigneeValue = assignmentAssignee ?? (data.assignee_id === null ? "none" : String(data.assignee_id));
    if (departmentValue === "none" && assigneeValue === "none") {
      setWorkflowMessage("请选择部门或处理人");
      return;
    }
    setWorkflowBusy(true);
    setWorkflowMessage("");
    try {
      await api(`/admin/tickets/${id}/transfer`, { method: "POST", body: JSON.stringify({ department_id: departmentValue === "none" ? null : Number(departmentValue), assignee_id: assigneeValue === "none" ? null : Number(assigneeValue), reason: transferReason }) });
      setTransferReason("");
      setAssignmentDepartment(null);
      setAssignmentAssignee(null);
      refresh();
    } catch (error) {
      setWorkflowMessage((error as Error).message);
    } finally {
      setWorkflowBusy(false);
    }
  }

  async function mergeTicket() {
    const target = Number(mergeTarget);
    if (!Number.isSafeInteger(target) || target < 1 || target === id) {
      setWorkflowMessage("请输入另一个有效工单编号");
      return;
    }
    setWorkflowBusy(true);
    setWorkflowMessage("");
    try {
      await api("/admin/tickets/merge", { method: "POST", body: JSON.stringify({ ticket_ids: [id, target] }) });
      setMergeTarget("");
      refresh();
    } catch (error) {
      setWorkflowMessage((error as Error).message);
    } finally {
      setWorkflowBusy(false);
    }
  }

  async function createNote(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (noteBody.trim() === "") return;
    setWorkflowBusy(true);
    setWorkflowMessage("");
    try {
      await api(`/admin/tickets/${id}/notes`, { method: "POST", body: JSON.stringify({ body: noteBody }) });
      setNoteBody("");
      refresh();
    } catch (error) {
      setWorkflowMessage((error as Error).message);
    } finally {
      setWorkflowBusy(false);
    }
  }

  return <Workspace title="工单详情" admin={admin}>
    <Button variant="ghost" className="mb-5" asChild><Link href={admin ? "/admin/tickets" : "/console/tickets"}><ArrowLeft />工单列表</Link></Button>
    {error ? <Failure error={error} retry={refresh} /> : !data ? <Loading /> : <>
      <div className="flex flex-wrap gap-4 justify-between items-start mb-6"><div><h1 className="page-title break-words">{data.subject}</h1><p className="text-xs text-muted-foreground mt-3 break-all">{data.ticket_no}</p></div><div className="flex gap-2"><Status value={data.status} /><Status value={data.priority} /></div></div>
      {(data.legacy_ticket_no || data.legacy_status_label) && <p className="text-sm text-muted-foreground mb-6 break-words">{data.legacy_ticket_no && <>原工单号：{data.legacy_ticket_no}　</>}{data.legacy_status_label && <>导入时状态：{data.legacy_status_label}</>}</p>}
      {admin && panel !== "options" && workflowMessage && <p role="alert" className="mb-4 text-sm text-destructive">{workflowMessage}</p>}
      {admin && <div className="mb-5 border-y bg-white"><div className="flex flex-wrap gap-x-6 gap-y-3 p-4 text-sm"><span>部门：{data.department_name || "未指定"}</span><span>处理人：{data.assignee_name || "未分派"}</span><PermissionGate permission="customers.view"><Link className="text-primary" href={`/admin/customers/${data.user_id}`}>客户 #{data.user_id}</Link></PermissionGate>{data.service_id && <PermissionGate permission="services.view"><Link className="text-primary" href={`/admin/services/${data.service_id}`}>服务 #{data.service_id}</Link></PermissionGate>}</div><div className="flex overflow-x-auto border-t" aria-label="工单操作">{[{id:"reply",label:"添加回复"},{id:"notes",label:"内部备注"},{id:"options",label:"处理选项"},{id:"attachments",label:"附件"}].filter(item=>data.admin_access||item.id==="reply"||item.id==="attachments").map(item=><button key={item.id} type="button" aria-pressed={panel===item.id} className={`shrink-0 border-b-2 px-5 py-3 text-sm ${panel===item.id?"border-primary text-primary":"border-transparent text-muted-foreground"}`} onClick={()=>setPanel(item.id)}>{item.label}</button>)}</div></div>}
      <div hidden={admin && panel !== "options"}>
      {admin && data.admin_access && <PermissionGate permission="tickets.manage"><section className="data-surface p-6 mb-6"><div className="surface-head"><div><h2 className="font-semibold">工单处理</h2><p className="text-xs text-muted-foreground mt-1">接单、分派或转移到其他处理组</p></div></div><div className="grid sm:grid-cols-2 gap-4 mt-4"><label className="text-sm"><span className="block text-muted-foreground mb-2">处理部门</span><Select value={assignmentDepartment ?? (data.department_id === null ? "none" : String(data.department_id))} onValueChange={value => { setAssignmentDepartment(value); setAssignmentAssignee("none"); }}><SelectTrigger className="w-full"><SelectValue /></SelectTrigger><SelectContent>{user?.ticket_unassigned && <SelectItem value="none">未指定部门</SelectItem>}{departments?.map(item => <SelectItem key={item.id} value={String(item.id)} disabled={item.active === false}>{item.name}{item.active === false ? "（已停用）" : ""}</SelectItem>)}</SelectContent></Select></label><label className="text-sm"><span className="block text-muted-foreground mb-2">处理人</span><Select value={assignmentAssignee ?? (data.assignee_id === null ? "none" : String(data.assignee_id))} onValueChange={setAssignmentAssignee} disabled={!assignees}><SelectTrigger className="w-full"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="none">未分派</SelectItem>{data.assignee_id !== null && assignees && !assignees.some(item => item.id === data.assignee_id) && <SelectItem value={String(data.assignee_id)} disabled>{data.assignee_name ?? "原处理人"}（当前不可分派）</SelectItem>}{assignees?.map(item => <SelectItem key={item.id} value={String(item.id)}>{item.display_name}（{item.username}）</SelectItem>)}</SelectContent></Select></label></div><div className="flex flex-wrap items-center gap-3 mt-4"><Button disabled={assignmentBusy || !departments || !assignees} onClick={() => void saveAssignment()}>保存分派</Button>{data.assignee_id === null && <Button variant="outline" disabled={workflowBusy} onClick={() => void receiveTicket()}><UserRoundCheck />接单</Button>}<Input className="max-w-xs" value={transferReason} onChange={event => setTransferReason(event.target.value)} placeholder="转移备注（可选）" maxLength={500} /><Button variant="outline" disabled={workflowBusy || !assignees || (!assignmentDepartment && !assignmentAssignee)} onClick={() => void transferTicket()}>转移</Button>{assigneeError && <Failure error={assigneeError} retry={refreshAssignees} />}{assignmentMessage && <span className="text-sm text-muted-foreground">{assignmentMessage}</span>}</div><div className="flex flex-wrap items-center gap-3 mt-4 border-t pt-4"><Input className="max-w-xs" type="number" min={1} value={mergeTarget} onChange={event => setMergeTarget(event.target.value)} placeholder="合并目标工单编号" /><Button variant="outline" disabled={workflowBusy || mergeTarget.trim() === ""} onClick={() => void mergeTicket()}><GitMerge />合并工单</Button>{workflowMessage && <span role="alert" className="text-sm text-destructive">{workflowMessage}</span>}</div></section></PermissionGate>}
      </div>
      <div className={`space-y-5 ${admin ? "bg-white border-y p-5" : "max-w-3xl"}`}>
        <article className="border-t py-5"><p className="text-xs text-muted-foreground mb-4">问题描述</p><p className="whitespace-pre-wrap break-words leading-7">{data.body}</p></article>
        {admin && data.admin_access && <section hidden={panel !== "notes"} className="border-t py-5"><div className="flex items-center gap-2"><StickyNote className="text-muted-foreground" /><h2 className="font-semibold">内部备注</h2></div><div className="mt-3 space-y-3">{(data.notes ?? []).map(note => <article key={note.id} className="border rounded-md p-3"><div className="text-xs text-muted-foreground mb-2">{note.author_name} · {date(note.created_at)}</div><p className="whitespace-pre-wrap break-words text-sm">{note.body}</p></article>)}{(data.notes ?? []).length === 0 && <p className="text-sm text-muted-foreground">暂无内部备注</p>}</div><PermissionGate permission="tickets.manage"><form className="mt-4 flex flex-wrap gap-3" onSubmit={event => void createNote(event)}><Textarea className="min-w-[min(100%,32rem)]" value={noteBody} onChange={event => setNoteBody(event.target.value)} placeholder="仅管理员可见" maxLength={10000} rows={3} /><Button type="submit" variant="outline" disabled={workflowBusy || noteBody.trim() === ""}><StickyNote />添加备注</Button></form></PermissionGate></section>}
        <div hidden={admin && panel !== "attachments"}>
        <section className="border-t py-5"><div className="flex flex-wrap items-center justify-between gap-3"><div><h2 className="font-semibold">附件</h2><p className="text-xs text-muted-foreground mt-1">单个文件最大 10 MiB</p></div><Paperclip className="text-muted-foreground" /></div><ul className="mt-3">{(data.attachments ?? []).map(attachment => <AttachmentRow key={attachment.id} attachment={attachment} ticketID={id} admin={admin && data.admin_access} onDelete={removeAttachment} />)}</ul>{(data.attachments ?? []).length === 0 && <p className="text-sm text-muted-foreground py-3">暂无附件</p>}{data.status !== "closed" && <PermissionGate permission="tickets.manage" bypass={!admin || !data.admin_access}><form className="mt-4 flex flex-wrap items-center gap-3" onSubmit={event => void addAttachment(event)}><Input ref={fileRef} type="file" className="max-w-md" onChange={event => setSelectedFile(event.target.files?.[0] ?? null)} /><Button type="submit" variant="outline" disabled={!selectedFile || attachmentBusy}><Paperclip />{attachmentBusy ? "上传中" : "上传附件"}</Button></form></PermissionGate>}{attachmentMessage && <p role="alert" className="text-sm text-destructive mt-2">{attachmentMessage}</p>}</section>
        </div>
        {data.replies.map(reply => <article key={reply.id} className="border-t py-5"><div className="text-sm flex flex-wrap gap-3 mb-4"><strong>{reply.author_name}</strong><span className="text-muted-foreground">{date(reply.created_at)}</span></div><p className="whitespace-pre-wrap break-words leading-7">{reply.body}</p></article>)}
        {message && <p role="alert" className="text-destructive">{message}</p>}
        <div hidden={admin && panel !== "reply"}>
        {data.status !== "closed" && <PermissionGate permission="tickets.manage" bypass={!admin || !data.admin_access}><form className="border-t pt-6 space-y-4" onSubmit={event => { event.preventDefault(); void mutate(); }}><div className="flex flex-wrap items-center justify-between gap-3"><label htmlFor="reply" className="font-medium">回复工单</label>{admin && data.admin_access && presets && <Select value={presetID} onValueChange={value => { setPresetID(value); const preset = presets?.find(item => String(item.id) === value); if (preset) setBody(preset.body); }}><SelectTrigger className="w-full sm:w-64"><SelectValue placeholder="插入预设回复" /></SelectTrigger><SelectContent><SelectItem value="none">不使用预设回复</SelectItem>{presets?.filter(item => item.active && item.category_active).map(item => <SelectItem key={item.id} value={String(item.id)}>{item.category_name} · {item.title}</SelectItem>)}</SelectContent></Select>}</div><Textarea id="reply" value={body} onChange={event => setBody(event.target.value)} required rows={5} maxLength={3000} /><div className="flex gap-3"><Button disabled={busy || body.trim() === ""}><Send />{busy ? "提交中" : "发送回复"}</Button><Dialog open={dialog} onOpenChange={setDialog}><DialogTrigger asChild><Button variant="outline" type="button" disabled={busy}><Check />关闭工单</Button></DialogTrigger><DialogContent><DialogHeader><DialogTitle>关闭此工单？</DialogTitle><DialogDescription>关闭后将停止接收回复。</DialogDescription></DialogHeader><DialogFooter><DialogClose asChild><Button variant="outline">取消</Button></DialogClose><Button disabled={busy} onClick={() => void mutate(true)}>确认关闭</Button></DialogFooter></DialogContent></Dialog></div></form></PermissionGate>}
        </div>
      </div>
    </>}
  </Workspace>;
}
