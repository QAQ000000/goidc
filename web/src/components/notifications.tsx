"use client";

import { useState } from "react";
import { Mail, RotateCcw, Save } from "lucide-react";
import { api } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Failure, Loading, Status } from "@/components/common";
import { Workspace, PermissionGate, PermissionFields } from "@/components/workspace";

type Template = { id: number; kind: string; name: string; subject: string; body: string; active: boolean; updated_at: string };
type EmailJob = { id: number; recipient: string; subject: string; kind: string; status: string; attempts: number; last_error: string; available_at: string; sent_at: string | null; created_at: string };

export function NotificationsView() {
  const templates = useResource<Template[]>("/admin/notification-templates");
  const outbox = useResource<EmailJob[]>("/admin/email-outbox?limit=50");
  const error = templates.error ?? outbox.error;
  const refresh = () => { templates.refresh(); outbox.refresh(); };
  return <Workspace title="通知管理" admin><div className="flex items-start justify-between gap-4 mb-8"><div><h1 className="page-title">通知管理</h1><p className="text-muted-foreground mt-2">维护邮件模板并处理投递队列</p></div><Mail className="text-muted-foreground" size={22} /></div>{error ? <Failure error={error} retry={refresh} /> : !templates.data || !outbox.data ? <Loading /> : <div className="space-y-6"><section className="data-surface p-6"><h2 className="font-semibold">邮件模板</h2><p className="text-xs text-muted-foreground mt-1">密码重置模板支持变量 <code>{"{{reset_link}}"}</code>，保存时不会展开或记录令牌。</p><div className="mt-5 space-y-5">{templates.data.map((template) => <TemplateEditor key={template.id} template={template} refresh={templates.refresh} />)}{templates.data.length === 0 && <p className="text-sm text-muted-foreground">暂无模板</p>}</div></section><section className="data-surface p-6"><div className="flex items-center justify-between gap-4"><div><h2 className="font-semibold">邮件队列</h2><p className="text-xs text-muted-foreground mt-1">正文不在列表 API 返回，失败任务可重新排队</p></div><Button variant="outline" size="sm" onClick={outbox.refresh}><RotateCcw size={15} />刷新</Button></div><div className="overflow-auto mt-5"><Table><TableHeader><TableRow><TableHead>收件人</TableHead><TableHead>主题</TableHead><TableHead>类型</TableHead><TableHead>状态</TableHead><TableHead>尝试次数</TableHead><TableHead>错误</TableHead><TableHead /></TableRow></TableHeader><TableBody>{outbox.data.length === 0 ? <TableRow><TableCell colSpan={7} className="h-20 text-center text-muted-foreground">暂无邮件</TableCell></TableRow> : outbox.data.map((job) => <TableRow key={job.id}><TableCell>{job.recipient}</TableCell><TableCell>{job.subject}</TableCell><TableCell>{job.kind}</TableCell><TableCell><Status value={job.status} /></TableCell><TableCell>{job.attempts}</TableCell><TableCell className="max-w-xs truncate text-xs text-destructive">{job.last_error || "-"}</TableCell><TableCell>{job.status !== "sent" && <PermissionGate permission="notifications.manage"><RetryEmail id={job.id} refresh={outbox.refresh} /></PermissionGate>}</TableCell></TableRow>)}</TableBody></Table></div></section></div>}</Workspace>;
}

function TemplateEditor({ template, refresh }: { template: Template; refresh: () => void }) {
  const [name, setName] = useState(template.name);
  const [subject, setSubject] = useState(template.subject);
  const [body, setBody] = useState(template.body);
  const [active, setActive] = useState(template.active);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault(); setBusy(true); setMessage("");
    try { await api("/admin/notification-templates/" + template.id, { method: "PATCH", body: JSON.stringify({ name, subject, body, active }) }); setMessage("已保存"); refresh(); }
    catch (e) { setMessage((e as Error).message); } finally { setBusy(false); }
  }
  return <PermissionFields permission="notifications.manage"><form className="border-t pt-5 first:border-t-0 first:pt-0 space-y-3" onSubmit={(event) => void save(event)}><div className="flex flex-wrap items-center justify-between gap-3"><div><p className="font-medium">{template.name}</p><p className="text-xs text-muted-foreground mt-1">{template.kind}</p></div><label className="flex items-center gap-2 text-sm"><Checkbox checked={active} onCheckedChange={(value) => setActive(value === true)} />启用</label></div><Input value={name} onChange={(event) => setName(event.target.value)} aria-label="模板名称" placeholder="模板名称" maxLength={120} /><Input value={subject} onChange={(event) => setSubject(event.target.value)} aria-label="邮件主题" placeholder="邮件主题" maxLength={240} /><Textarea value={body} onChange={(event) => setBody(event.target.value)} aria-label="邮件正文" rows={7} maxLength={100000} /><div className="flex items-center gap-3"><Button disabled={busy || !name.trim() || !subject.trim() || !body.trim()}><Save size={15} />保存模板</Button>{message && <span className="text-sm text-muted-foreground" role="status">{message}</span>}</div></form></PermissionFields>;
}

function RetryEmail({ id, refresh }: { id: number; refresh: () => void }) {
  const [busy, setBusy] = useState(false);
  async function retry() { setBusy(true); try { await api("/admin/email-outbox/" + id + "/retry", { method: "POST", body: "{}" }); refresh(); } finally { setBusy(false); } }
  return <Button size="icon-sm" variant="ghost" title="重新排队" aria-label="重新排队" disabled={busy} onClick={() => void retry()}><RotateCcw size={15} /></Button>;
}
