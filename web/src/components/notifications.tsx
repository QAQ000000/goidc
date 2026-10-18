"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { usePendingSetting } from "@/hooks/use-pending-setting";
import { useAdminFormLeave } from "@/hooks/use-admin-form-leave";
import { ArrowLeft, Pencil, Plus, RotateCcw, Save } from "lucide-react";
import { api, ApiError, date } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { useAdminListNavigation, useAdminListReturnHref } from "@/hooks/use-admin-list-navigation";
import { AdminPagination } from "./admin-pagination";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "./ui/table";
import { Failure, Loading, Status } from "./common";
import { Workspace, useAdminPermission, useAdminIdentity } from "./workspace";
import { NotificationCenter } from "./notification-center";

type Template = {
  version: number;
  id: number;
  channel: "email" | "sms";
  kind: string;
  name: string;
  subject: string;
  body: string;
  provider_template_id: string;
  active: boolean;
  updated_at: string;
};
type EmailJob = {
  id: number;
  recipient: string;
  subject: string;
  kind: string;
  status: string;
  attempts: number;
  max_attempts: number;
  dead_letter_at: string | null;
  last_error: string;
  sent_at: string | null;
  created_at: string;
};
type SMSJob = Omit<EmailJob, "subject">;
type TemplatePage = { items: Template[]; total: number; limit: number; offset: number };

export function NotificationsView() {
  return (
    <Workspace title="通知管理" admin>
      <NotificationManager />
    </Workspace>
  );
}
const templateStates = ["all", "active", "disabled"] as const;
const templateDefaults = { channel: "email", limit: "25" };
function NotificationManager() {
  const [tab, setTab] = useState("center");
  const [centerDirty, setCenterDirty] = useState(false);
  const navigation = useAdminListNavigation(true, templateStates, "all", templateDefaults);
  const templateChannel = navigation.extra.channel === "sms" ? "sms" : "email";
  const templateLimit = [25, 50, 100].includes(Number(navigation.extra.limit)) ? Number(navigation.extra.limit) : 25;
  const templatePage = navigation.page;
  const templateQuery = navigation.query;
  const setTemplatePage = navigation.setPage;
  const templates = useResource<TemplatePage>(tab === "templates" && navigation.ready ? `/admin/notification-templates/page?channel=${templateChannel}&q=${encodeURIComponent(templateQuery)}&status=${navigation.status}&limit=${templateLimit}&offset=${templatePage * templateLimit}` : null, false);
  const outbox = useResource<EmailJob[]>(tab === "outbox" ? "/admin/email-outbox?limit=50" : null);
  const smsOutbox = useResource<SMSJob[]>(tab === "outbox" ? "/admin/sms-outbox?limit=50" : null);
  const manage = useAdminPermission("notifications.manage");
  const [outboxChannel, setOutboxChannel] = useState<"email" | "sms">("email");
  const [edit, setEdit] = useState<Template | null>(null);
  const [editingDirty, setEditingDirty] = useState(false);
  const [editingBusy, setEditingBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const refresh = () => {
    templates.refresh();
    outbox.refresh();
    smsOutbox.refresh();
  };
  const activeOutbox = outboxChannel === "email" ? outbox : smsOutbox;
  const error = tab === "templates" ? templates.error : activeOutbox.error;
  const list = templates.data?.items ?? [];
  if (edit)
    return (
      <>
        <Button
          variant="ghost"
          className="mb-4"
          disabled={editingBusy}
          onClick={() => { if (!editingDirty || window.confirm("模板有未保存的修改，确定返回并丢弃吗？")) { setEdit(null); setEditingDirty(false); } }}
        >
          <ArrowLeft />
          返回{edit.channel === "sms" ? "短信" : "邮件"}模板
        </Button>
        <h1 className="page-title mb-5 break-all">
          {edit.id ? edit.name : `新增${edit.channel === "sms" ? "短信" : "邮件"}模板`}
        </h1>
        <TemplateEditor
          key={edit.id}
          template={edit}
          manage={manage}
          onBusy={setEditingBusy}
          onDirty={setEditingDirty}
          saved={() => {
            setEdit(null);
            setNotice(`${edit.channel === "sms" ? "短信" : "邮件"}模板已保存`);
            templates.refresh();
          }}
        />
      </>
    );
  return (
    <>
      <div className="mb-5 flex flex-wrap items-center justify-between gap-3">
        <h1 className="page-title">通知管理</h1>
        <div className="flex gap-2">
          {tab !== "center" && <Button
            variant="outline"
            size="icon"
            title="刷新通知"
            aria-label="刷新通知"
            onClick={refresh}
          >
            <RotateCcw />
          </Button>}
          {manage && tab === "templates" && (
            <Button asChild><Link href={`/admin/notifications/new${navigation.suffix}`}><Plus />新增模板</Link></Button>
          )}
        </div>
      </div>
      <nav aria-label="通知分区" className="mb-5 flex overflow-x-auto border-b">
        {[
          ["center", "统一通知中心"],
          ["templates", "邮件 / 短信模板"],
          ["outbox", "邮件 / 短信队列"],
        ].map(([value, label]) => (
          <button
            type="button"
            key={value}
            aria-current={tab === value ? "page" : undefined}
            className={`shrink-0 border-b-2 px-5 py-3 text-sm ${tab === value ? "border-primary text-primary font-medium" : "border-transparent text-muted-foreground"}`}
            onClick={() => { if (value === tab || !centerDirty || window.confirm("通知中心有未保存的修改，确定切换并丢弃草稿吗？")) setTab(value); }}
          >
            {label}
          </button>
        ))}
      </nav>
      {notice && (
        <p role="status" className="mb-4 text-sm text-emerald-700">
          {notice}
        </p>
      )}
      {tab === "templates" && (
        <div className="mb-4 flex flex-wrap items-center gap-3">
          <div className="flex border-b">
            {([["email", "邮件模板"], ["sms", "短信模板"]] as const).map(([value, label]) => (
              <button type="button" key={value} className={`border-b-2 px-4 py-2 text-sm ${templateChannel === value ? "border-primary text-primary font-medium" : "border-transparent text-muted-foreground"}`} onClick={() => { navigation.setExtra({ ...navigation.extra, channel: value }); navigation.setQuery(""); setTemplatePage(0); }}>
                {label}
              </button>
            ))}
          </div>
          <Input
            className="max-w-md"
            aria-label={`搜索${templateChannel === "sms" ? "短信" : "邮件"}模板`}
            placeholder="模板名称、主题或类型"
            value={templateQuery}
            onChange={(event) => { navigation.setQuery(event.target.value); setTemplatePage(0); }}
          />
          <select aria-label="模板状态" value={navigation.status} onChange={(event) => { navigation.setStatus(event.target.value); setTemplatePage(0); }} className="rounded border px-3 py-2 text-sm"><option value="all">全部状态</option><option value="active">启用</option><option value="disabled">停用</option></select>
        </div>
      )}
      {tab === "center" ? <NotificationCenter onDirty={setCenterDirty} /> : error ? (
        <Failure error={error} retry={refresh} />
      ) : tab === "templates" ? (
        !templates.data ? (
          <Loading />
        ) : (
          <>
          <div className="overflow-x-auto border-y">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>ID</TableHead>
                  <TableHead>模板名称</TableHead>
                  <TableHead>类型</TableHead>
                  <TableHead>{templateChannel === "sms" ? "供应商模板 ID" : "邮件主题"}</TableHead>
                  <TableHead>状态</TableHead>
                  <TableHead>更新时间</TableHead>
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {list.map((item) => (
                  <TableRow key={item.id}>
                    <TableCell>{item.id}</TableCell>
                    <TableCell className="max-w-64 whitespace-normal break-all">
                      {item.name}
                    </TableCell>
                    <TableCell>{item.kind}</TableCell>
                    <TableCell className="max-w-80 whitespace-normal break-all">
                      {templateChannel === "sms" ? (item.provider_template_id || "未填写") : item.subject}
                    </TableCell>
                    <TableCell>
                      <Status value={item.active ? "active" : "disabled"} />
                    </TableCell>
                    <TableCell>{date(item.updated_at)}</TableCell>
                    <TableCell className="text-right">
                      <Button
                        size="sm"
                        variant="ghost"
                        aria-label={`${manage ? "编辑" : "查看"}模板 ${item.name}`}
                        onClick={() => {
                          setNotice("");
                          setEdit(item);
                        }}
                      >
                        <Pencil />
                        {manage ? "编辑" : "查看"}
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
                {!list.length && (
                  <TableRow>
                    <TableCell
                      colSpan={7}
                      className="py-10 text-center text-muted-foreground"
                    >
                      暂无符合条件的模板
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </div>
          <AdminPagination total={templates.data.total} page={templatePage} pageSize={templateLimit} onPageChange={setTemplatePage} onPageSizeChange={(limit) => { navigation.setExtra({ ...navigation.extra, limit: String(limit) }); setTemplatePage(0); }} />
          </>
        )
      ) : !activeOutbox.data ? (
        <Loading />
      ) : (
        <>
        <div className="mb-4 flex border-b">
          {([['email', '邮件队列'], ['sms', '短信队列']] as const).map(([value, label]) => (
            <button type="button" key={value} aria-current={outboxChannel === value ? "page" : undefined} className={`border-b-2 px-4 py-2 text-sm ${outboxChannel === value ? "border-primary text-primary font-medium" : "border-transparent text-muted-foreground"}`} onClick={() => setOutboxChannel(value)}>
              {label}
            </button>
          ))}
        </div>
        <div className="overflow-x-auto border-y">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>ID</TableHead>
                  <TableHead>{outboxChannel === "email" ? "收件人 / 主题" : "接收号码"}</TableHead>
                <TableHead>类型</TableHead>
                <TableHead>状态</TableHead>
                <TableHead>尝试次数</TableHead>
                <TableHead>创建 / 发送时间</TableHead>
                <TableHead>错误</TableHead>
                <TableHead className="text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {activeOutbox.data.map((job) => (
                <TableRow key={job.id}>
                  <TableCell>{job.id}</TableCell>
                  <TableCell className="max-w-80 whitespace-normal break-all">
                    <p>{job.recipient}</p>
                    {outboxChannel === "email" && <p className="mt-1 text-xs text-muted-foreground">{(job as EmailJob).subject}</p>}
                  </TableCell>
                  <TableCell>{job.kind}</TableCell>
                  <TableCell>
                    <Status value={job.status} />
                  </TableCell>
                  <TableCell>{job.attempts} / {job.max_attempts}</TableCell>
                  <TableCell>
                    {date(job.created_at)}
                    <p className="mt-1 text-xs text-muted-foreground">
                      {job.sent_at ? date(job.sent_at) : job.dead_letter_at ? `转入死信 ${date(job.dead_letter_at)}` : "尚未发送"}
                    </p>
                  </TableCell>
                  <TableCell className="max-w-64 whitespace-normal break-all text-xs text-destructive">
                    {job.last_error || "-"}
                  </TableCell>
                  <TableCell className="text-right">
                    {manage && job.status !== "sent" && job.status !== "expired" && (
                      <RetryOutbox id={job.id} channel={outboxChannel} refresh={activeOutbox.refresh} />
                    )}
                  </TableCell>
                </TableRow>
              ))}
              {!activeOutbox.data.length && (
                <TableRow>
                  <TableCell
                    colSpan={8}
                    className="py-10 text-center text-muted-foreground"
                  >
                    {outboxChannel === "email" ? "暂无邮件" : "暂无短信"}
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>
        </>
      )}
    </>
  );
}
export function TemplateEditor({
  template,
  manage,
  saved,
  onBusy,
  onDirty,
}: {
  template: Template;
  manage: boolean;
  saved: () => void;
  onBusy: (value: boolean) => void;
  onDirty: (value: boolean) => void;
}) {
  const [form, setForm] = useState(template);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [forbidden, setForbidden] = useState(false);
  const canWrite = manage && !forbidden;
  const actor = useAdminIdentity();
  const request = usePendingSetting(`template-create:${template.channel}`, actor?.id ?? null);
  const dirty = JSON.stringify(form) !== JSON.stringify(template);
  useAdminFormLeave(dirty || (!template.id && request.unknown));
  useEffect(() => { onDirty(dirty); }, [dirty, onDirty]);
  async function save(event?: React.FormEvent) {
    event?.preventDefault();
    if (!canWrite || busy || (!template.id && !request.ready)) return;
    setBusy(true);
    onBusy(true);
    setError("");
    try {
      const body = JSON.stringify({
        ...(template.id ? { expected_version: template.version } : { kind: form.kind, channel: form.channel }),
        name: form.name, subject: form.subject, body: form.body,
        provider_template_id: form.provider_template_id, active: form.active,
      });
      if (template.id) {
        await api(`/admin/notification-templates/${template.id}`, { method: "PATCH", body });
      } else {
        const pending = request.pending.current ?? { body, key: crypto.randomUUID(), path: "/admin/notification-templates" };
        request.remember(pending);
        await api(pending.path!, { method: "POST", body: pending.body, headers: { "Idempotency-Key": pending.key } });
        request.resolve();
      }
      saved();
    } catch (e) {
      if (e instanceof ApiError && e.status === 403) setForbidden(true);
      if (!template.id && (request.unknown || !(e instanceof ApiError) || e.status >= 500 || e.status === 408)) {
        request.setUnknown(true); setError("创建结果尚未确认，请重试原请求确认结果。");
      } else {
        if (!template.id) request.resolve();
        setError(template.id && e instanceof ApiError && e.status === 409 ? "模板已被其他管理员修改。请保留所需内容，返回列表刷新后重新编辑。" : (e as Error).message);
      }
    } finally {
      setBusy(false);
      onBusy(false);
    }
  }
  return (
    <form
      onSubmit={(event) => void save(event)}
      className="max-w-4xl space-y-5"
    >
      <fieldset disabled={busy || !canWrite || (!template.id && (!request.ready || request.unknown))} className="space-y-5">
        <section className="space-y-4 border-b pb-5">
          <h2 className="text-sm font-semibold">模板资料</h2>
          <div className="grid gap-4 sm:grid-cols-2">
            <label className="grid gap-2 text-sm">
              模板名称
              <Input
                required
                aria-label="模板名称"
                maxLength={120}
                value={form.name}
                onChange={(event) =>
                  setForm({ ...form, name: event.target.value })
                }
              />
            </label>
            <label className="grid gap-2 text-sm">
              类型
              <Input
                required
                aria-label="模板类型"
                list="ticket-template-kinds"
                readOnly={template.id > 0}
                maxLength={80}
                pattern={"[a-z0-9_\\-]+"}
                value={form.kind}
                onChange={(event) =>
                  setForm({ ...form, kind: event.target.value })
                }
              />
            </label>
          </div>
          <datalist id="ticket-template-kinds"><option value="ticket_customer_created" /><option value="ticket_customer_replied" /><option value="ticket_staff_replied" /></datalist>
          <p className="text-xs text-muted-foreground">工单事件类型：ticket_customer_created（客户创建）、ticket_customer_replied（客户回复）、ticket_staff_replied（客服回复）。ticket_created 保留为管理员代建通知。</p>
          {form.channel === "email" ? <label className="grid gap-2 text-sm">
            邮件主题
            <Input required aria-label="邮件主题" maxLength={240} value={form.subject} onChange={(event) => setForm({ ...form, subject: event.target.value })} />
          </label> : <label className="grid gap-2 text-sm">
            供应商模板 ID（可选）
            <Input aria-label="供应商模板 ID" maxLength={160} value={form.provider_template_id} onChange={(event) => setForm({ ...form, provider_template_id: event.target.value })} />
          </label>}
          <label className="flex items-center gap-2 text-sm">
            <Checkbox
              checked={form.active}
              onCheckedChange={(value) =>
                setForm({ ...form, active: value === true })
              }
            />
            启用模板
          </label>
        </section>
        <section className="space-y-4">
          <h2 className="text-sm font-semibold">{form.channel === "sms" ? "短信内容" : "邮件正文"}</h2>
          <Textarea
            required
            aria-label={form.channel === "sms" ? "短信内容" : "邮件正文"}
            rows={form.channel === "sms" ? 6 : 14}
            maxLength={100000}
            value={form.body}
            onChange={(event) => setForm({ ...form, body: event.target.value })}
          />
        </section>
      </fieldset>
      {error && (
        <p role="alert" className="text-sm text-destructive break-all">
          {error}
        </p>
      )}
      {!template.id && request.unknown && <p role="status" className="text-sm text-amber-700">上次创建尚未确认。已保留原请求，确认结果前不能修改内容或创建另一条记录。</p>}
      {canWrite && (
        <div className="flex flex-wrap gap-3 border-t pt-4">
          <Button disabled={busy || (!template.id && !request.ready)} type={!template.id && request.unknown ? "button" : "submit"} onClick={!template.id && request.unknown ? () => void save() : undefined}>
            <Save />
            {busy ? "保存中" : !template.id && request.unknown ? "重试原请求" : "保存模板"}
          </Button>
          <Button
            type="button"
            variant="outline"
            disabled={busy || (!template.id && request.unknown)}
            onClick={() => {
              if (dirty && !window.confirm("确定丢弃未保存的内容？")) return;
              setForm(template);
              setError("");
            }}
          >
            取消更改
          </Button>
        </div>
      )}
    </form>
  );
}
function RetryOutbox({ id, channel, refresh }: { id: number; channel: "email" | "sms"; refresh: () => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function retry() {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      await api(`/admin/${channel}-outbox/${id}/retry`, {
        method: "POST",
        body: "{}",
      });
      refresh();
    } catch (e) {
      setError(e instanceof ApiError && e.status === 409 ? "模板已被其他管理员修改。请保留所需内容，返回列表刷新后重新编辑。" : (e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="space-y-2">
      <Button
        size="icon-sm"
        variant="ghost"
        title="重新排队"
        aria-label={`重新排队${channel === "email" ? "邮件" : "短信"} ${id}`}
        disabled={busy}
        onClick={() => void retry()}
      >
        <RotateCcw size={15} />
      </Button>
      {error && (
        <p
          role="alert"
          className="max-w-56 whitespace-normal break-all text-xs text-destructive"
        >
          {error}
        </p>
      )}
    </div>
  );
}

export function NotificationTemplateCreateView(){return <Workspace title="创建通知模板" admin><NotificationTemplateCreate/></Workspace>;}
function NotificationTemplateCreate(){
 const router=useRouter();const params=useSearchParams();const channel=params.get("channel")==="sms"?"sms":"email";
 const manage=useAdminPermission("notifications.manage");const view=useAdminPermission("notifications.view");const allowed=manage&&view;
 const returnHref=useAdminListReturnHref("/admin/notifications",templateStates,"all",{channel,limit:"25"});
 const access=useResource<TemplatePage>(`/admin/notification-templates/page?channel=${channel}&limit=1`,false);
 const [busy,setBusy]=useState(false);const [,setDirty]=useState(false);
 const [template]=useState<Template>(()=>({id:0,version:1,channel,kind:"",name:"",subject:"",body:"",provider_template_id:"",active:false,updated_at:""}));
 return <div className="max-w-4xl"><div className="mb-6 flex flex-wrap items-center justify-between gap-3"><div><h1 className="page-title">创建{channel==="sms"?"短信":"邮件"}模板</h1><p className="mt-2 text-sm text-muted-foreground">填写通知模板资料和内容</p></div><Button variant="outline" disabled={busy} asChild><Link href={returnHref}>返回模板列表</Link></Button></div>
 {!allowed?<p role="alert" className="text-sm text-destructive">没有创建通知模板的权限。</p>:access.error?<Failure error={access.error} retry={access.refresh}/>:!access.data?<Loading/>:<>{channel==="sms"&&<p className="mb-5 text-sm text-muted-foreground">短信使用系统配置的供应商。供应商要求已审核模板时，请填写对应模板 ID。<Link href="/admin/sms-settings" className="ml-2 text-primary">查看短信设置</Link></p>}<TemplateEditor key={channel} template={{...template,channel}} manage={manage} saved={()=>router.replace(returnHref)} onBusy={setBusy} onDirty={setDirty}/></>}
 </div>;
}
