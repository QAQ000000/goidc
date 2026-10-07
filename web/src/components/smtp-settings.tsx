"use client";

import { useState } from "react";
import { RefreshCw, Save, Trash2 } from "lucide-react";
import { useResource } from "@/hooks/use-resource";
import { api, date } from "@/lib/api";
import { Workspace, useAdminPermission } from "./workspace";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Checkbox } from "./ui/checkbox";

type Settings = {
  configured: boolean;
  encryption_configured: boolean;
  enabled: boolean;
  host: string;
  port: number;
  username: string;
  from: string;
  password_configured: boolean;
  version: number;
  updated_at: string;
};

export function SMTPSettingsView() {
  const resource = useResource<Settings>("/admin/smtp-settings", false);
  return (
    <Workspace title="邮件发送设置" admin>
      <h1 className="page-title mb-6">邮件发送设置</h1>
      {resource.error ? (
        <Failure error={resource.error} retry={resource.refresh} />
      ) : !resource.data ? (
        <Loading />
      ) : (
        <SMTPForm key={resource.data.version} initial={resource.data} />
      )}
    </Workspace>
  );
}

function SMTPForm({ initial }: { initial: Settings }) {
  const [baseline, setBaseline] = useState(initial);
  const [form, setForm] = useState(initial);
  const [password, setPassword] = useState("");
  const [clear, setClear] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const manage = useAdminPermission("@super");
  const dirty =
    password !== "" ||
    clear ||
    form.enabled !== baseline.enabled ||
    form.host !== baseline.host ||
    form.port !== baseline.port ||
    form.username !== baseline.username ||
    form.from !== baseline.from;
  function changed(patch: Partial<Settings>) {
    setForm((current) => ({ ...current, ...patch }));
    setSaved(false);
  }
  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (busy || !manage) return;
    setBusy(true);
    setError("");
    setSaved(false);
    try {
      const updated = await api<Settings>("/admin/smtp-settings", {
        method: "PUT",
        body: JSON.stringify({
          enabled: form.enabled,
          host: form.host,
          port: form.port,
          username: form.username,
          from: form.from,
          ...(password ? { password } : {}),
          clear_password: clear,
          expected_version: form.version,
        }),
      });
      setBaseline(updated);
      setForm(updated);
      setPassword("");
      setClear(false);
      setSaved(true);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function reload() {
    if (
      busy ||
      !manage ||
      (dirty && !window.confirm("重新加载会放弃未保存的修改，是否继续？"))
    )
      return;
    setBusy(true);
    setError("");
    setSaved(false);
    try {
      const updated = await api<Settings>("/admin/smtp-settings");
      setBaseline(updated);
      setForm(updated);
      setPassword("");
      setClear(false);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <form onSubmit={submit} autoComplete="off" className="max-w-4xl space-y-5">
      <div className="flex items-center justify-between gap-3">
        <p className="text-sm text-muted-foreground">
          配置来源：{baseline.configured ? "后台配置" : "部署配置"}
        </p>
        <Button
          type="button"
          variant="outline"
          size="icon"
          title="重新加载邮件配置"
          aria-label="重新加载邮件配置"
          disabled={busy || !manage}
          onClick={() => void reload()}
        >
          <RefreshCw />
        </Button>
      </div>
      <fieldset disabled={busy || !manage} className="space-y-5 min-w-0">
        <label className="flex items-center gap-2 text-sm">
          <Checkbox
            checked={form.enabled}
            disabled={clear}
            onCheckedChange={(checked) =>
              changed({ enabled: checked === true })
            }
          />
          启用邮件发送
        </label>
        <section className="grid sm:grid-cols-2 gap-5 border-y py-5">
          <label className="grid gap-2 text-sm min-w-0">
            SMTP 主机
            <Input
              aria-label="SMTP 主机"
              value={form.host}
              required={form.enabled}
              maxLength={253}
              onChange={(event) => changed({ host: event.target.value })}
            />
          </label>
          <label className="grid gap-2 text-sm min-w-0">
            端口
            <Input
              aria-label="SMTP 端口"
              type="number"
              min={1}
              max={65535}
              required
              value={form.port}
              onChange={(event) =>
                changed({ port: Number(event.target.value) })
              }
            />
          </label>
          <label className="grid gap-2 text-sm min-w-0">
            发件地址
            <Input
              aria-label="发件地址"
              type="email"
              value={form.from}
              required={form.enabled}
              maxLength={254}
              onChange={(event) => changed({ from: event.target.value })}
            />
          </label>
          <label className="grid gap-2 text-sm min-w-0">
            认证用户名
            <Input
              aria-label="SMTP 用户名"
              value={form.username}
              maxLength={320}
              onChange={(event) => changed({ username: event.target.value })}
            />
          </label>
          <div className="grid gap-2 text-sm min-w-0">
            <label className="grid gap-2">
              认证密码
              <span
                className={clear ? "text-destructive" : "text-muted-foreground"}
              >
                {clear
                  ? "待清除"
                  : form.password_configured
                    ? "已配置"
                    : "未配置"}
              </span>
              <Input
                aria-label="SMTP 密码"
                type="password"
                disabled={clear}
                autoComplete="new-password"
                value={password}
                maxLength={4096}
                placeholder={
                  form.password_configured ? "留空保留现有密码" : "输入认证密码"
                }
                onChange={(event) => {
                  setPassword(event.target.value);
                  setSaved(false);
                }}
              />
            </label>
            {form.password_configured && (
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="justify-self-start"
                disabled={form.enabled}
                onClick={() => {
                  if (clear) setClear(false);
                  else if (
                    window.confirm("确认清除已保存的SMTP密码？保存后无法恢复。")
                  ) {
                    setClear(true);
                    setPassword("");
                  }
                  setSaved(false);
                }}
              >
                <Trash2 />
                {clear ? "保留密码" : "清除密码"}
              </Button>
            )}
          </div>
        </section>
        {!baseline.encryption_configured && (
          <p role="status" className="text-sm text-amber-700">
            服务端凭据加密尚未配置
          </p>
        )}
        {error && (
          <p role="alert" className="text-sm text-destructive break-words">
            {error}
          </p>
        )}
        {saved && (
          <p role="status" className="text-sm text-emerald-700">
            邮件配置已保存
          </p>
        )}
        <div className="flex flex-wrap items-center justify-between gap-3">
          <span className="text-xs text-muted-foreground">
            版本 {form.version} · {date(form.updated_at)}
          </span>
          <div className="flex flex-wrap gap-3">
            <Button
              disabled={
                !dirty || (!!password && !baseline.encryption_configured)
              }
            >
              <Save />
              {busy ? "保存中" : "保存更改"}
            </Button>
            <Button
              type="button"
              variant="outline"
              disabled={!dirty}
              onClick={() => {
                setForm(baseline);
                setPassword("");
                setClear(false);
                setError("");
                setSaved(false);
              }}
            >
              取消更改
            </Button>
          </div>
        </div>
      </fieldset>
    </form>
  );
}
