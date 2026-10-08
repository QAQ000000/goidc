"use client";

import { useState } from "react";
import { ArrowLeft, Pencil, Plus, RotateCcw, Save } from "lucide-react";
import { api, date } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
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
import { Workspace, useAdminPermission } from "./workspace";

type Template = {
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
  last_error: string;
  sent_at: string | null;
  created_at: string;
};

export function NotificationsView() {
  return (
    <Workspace title="通知管理" admin>
      <NotificationManager />
    </Workspace>
  );
}
function NotificationManager() {
  const templates = useResource<Template[]>("/admin/notification-templates");
  const outbox = useResource<EmailJob[]>("/admin/email-outbox?limit=50");
  const manage = useAdminPermission("notifications.manage");
  const [tab, setTab] = useState("templates");
  const [templateChannel, setTemplateChannel] = useState<"email" | "sms">("email");
  const [query, setQuery] = useState("");
  const [edit, setEdit] = useState<Template | null>(null);
  const [editingBusy, setEditingBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const refresh = () => {
    templates.refresh();
    outbox.refresh();
  };
  const error = tab === "templates" ? templates.error : outbox.error;
  const list =
    templates.data?.filter((item) => item.channel === templateChannel).filter((item) =>
      `${item.name} ${item.subject} ${item.kind}`
        .toLowerCase()
        .includes(query.toLowerCase()),
    ) ?? [];
  if (edit)
    return (
      <>
        <Button
          variant="ghost"
          className="mb-4"
          disabled={editingBusy}
          onClick={() => setEdit(null)}
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
          <Button
            variant="outline"
            size="icon"
            title="刷新通知"
            aria-label="刷新通知"
            onClick={refresh}
          >
            <RotateCcw />
          </Button>
          {manage && tab === "templates" && (
            <Button
              onClick={() => {
                setNotice("");
                setEdit({
                  id: 0,
                  channel: templateChannel,
                  kind: "",
                  name: "",
                  subject: "",
                  body: "",
                  provider_template_id: "",
                  active: false,
                  updated_at: "",
                });
              }}
            >
              <Plus />
              新增模板
            </Button>
          )}
        </div>
      </div>
      <nav aria-label="通知分区" className="mb-5 flex overflow-x-auto border-b">
        {[
          ["templates", "模板管理"],
          ["outbox", "投递队列"],
        ].map(([value, label]) => (
          <button
            type="button"
            key={value}
            aria-current={tab === value ? "page" : undefined}
            className={`shrink-0 border-b-2 px-5 py-3 text-sm ${tab === value ? "border-primary text-primary font-medium" : "border-transparent text-muted-foreground"}`}
            onClick={() => setTab(value)}
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
              <button type="button" key={value} className={`border-b-2 px-4 py-2 text-sm ${templateChannel === value ? "border-primary text-primary font-medium" : "border-transparent text-muted-foreground"}`} onClick={() => { setTemplateChannel(value); setQuery(""); }}>
                {label}
              </button>
            ))}
          </div>
          <Input
            className="max-w-md"
            aria-label={`搜索${templateChannel === "sms" ? "短信" : "邮件"}模板`}
            placeholder="模板名称、主题或类型"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
          />
        </div>
      )}
      {error ? (
        <Failure error={error} retry={refresh} />
      ) : tab === "templates" ? (
        !templates.data ? (
          <Loading />
        ) : (
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
        )
      ) : !outbox.data ? (
        <Loading />
      ) : (
        <div className="overflow-x-auto border-y">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>ID</TableHead>
                <TableHead>收件人 / 主题</TableHead>
                <TableHead>类型</TableHead>
                <TableHead>状态</TableHead>
                <TableHead>尝试次数</TableHead>
                <TableHead>创建 / 发送时间</TableHead>
                <TableHead>错误</TableHead>
                <TableHead className="text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {outbox.data.map((job) => (
                <TableRow key={job.id}>
                  <TableCell>{job.id}</TableCell>
                  <TableCell className="max-w-80 whitespace-normal break-all">
                    <p>{job.recipient}</p>
                    <p className="mt-1 text-xs text-muted-foreground">
                      {job.subject}
                    </p>
                  </TableCell>
                  <TableCell>{job.kind}</TableCell>
                  <TableCell>
                    <Status value={job.status} />
                  </TableCell>
                  <TableCell>{job.attempts}</TableCell>
                  <TableCell>
                    {date(job.created_at)}
                    <p className="mt-1 text-xs text-muted-foreground">
                      {job.sent_at ? date(job.sent_at) : "尚未发送"}
                    </p>
                  </TableCell>
                  <TableCell className="max-w-64 whitespace-normal break-all text-xs text-destructive">
                    {job.last_error || "-"}
                  </TableCell>
                  <TableCell className="text-right">
                    {manage && job.status !== "sent" && (
                      <RetryEmail id={job.id} refresh={outbox.refresh} />
                    )}
                  </TableCell>
                </TableRow>
              ))}
              {!outbox.data.length && (
                <TableRow>
                  <TableCell
                    colSpan={8}
                    className="py-10 text-center text-muted-foreground"
                  >
                    暂无邮件
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>
      )}
    </>
  );
}
function TemplateEditor({
  template,
  manage,
  saved,
  onBusy,
}: {
  template: Template;
  manage: boolean;
  saved: () => void;
  onBusy: (value: boolean) => void;
}) {
  const [form, setForm] = useState(template);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function save(event: React.FormEvent) {
    event.preventDefault();
    if (!manage || busy) return;
    setBusy(true);
    onBusy(true);
    setError("");
    try {
      await api(
        `/admin/notification-templates${template.id ? `/${template.id}` : ""}`,
        {
          method: template.id ? "PATCH" : "POST",
          body: JSON.stringify({
            ...(template.id ? {} : { kind: form.kind }),
            ...(template.id ? {} : { channel: form.channel }),
            name: form.name,
            subject: form.subject,
            body: form.body,
            provider_template_id: form.provider_template_id,
            active: form.active,
          }),
        },
      );
      saved();
    } catch (e) {
      setError((e as Error).message);
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
      <fieldset disabled={busy || !manage} className="space-y-5">
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
      {manage && (
        <div className="flex flex-wrap gap-3 border-t pt-4">
          <Button disabled={busy}>
            <Save />
            {busy ? "保存中" : "保存模板"}
          </Button>
          <Button
            type="button"
            variant="outline"
            disabled={busy}
            onClick={() => {
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
function RetryEmail({ id, refresh }: { id: number; refresh: () => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function retry() {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      await api("/admin/email-outbox/" + id + "/retry", {
        method: "POST",
        body: "{}",
      });
      refresh();
    } catch (e) {
      setError((e as Error).message);
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
        aria-label={`重新排队邮件 ${id}`}
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
