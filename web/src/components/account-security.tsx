"use client";
import { useState } from "react";
import { KeyRound, Copy } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Table, TableHeader, TableHead, TableBody, TableRow, TableCell } from "@/components/ui/table";
import { api, date, type ApiKey } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Loading, Status } from "./common";
import { MfaPanel } from "./mfa-panel";
import { TicketNotificationPreferences } from "./ticket-notification-preferences";
import { NotificationInbox, NotificationPreferences } from "./notification-preferences";

export function AccountSecurity() {
  return <><PasswordForm /><MfaPanel /><ApiKeysPanel /><TicketNotificationPreferences /><NotificationPreferences /><NotificationInbox /></>;
}

function ApiKeysPanel() {
  const { data, error, refresh } = useResource<ApiKey[]>("/api-keys");
  const [name, setName] = useState("");
  const [expiresAt, setExpiresAt] = useState("");
  const [secret, setSecret] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  async function create(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setMessage("");
    try {
      const result = await api<{ secret: string }>("/api-keys", { method: "POST", body: JSON.stringify({ name, expires_at: expiresAt ? `${expiresAt}T23:59:59Z` : "" }) });
      setName("");
      setExpiresAt("");
      setSecret(result.secret);
      refresh();
    } catch (e) {
      setMessage((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function revoke(id: number) {
    setBusy(true);
    setMessage("");
    try {
      await api(`/api-keys/${id}`, { method: "DELETE", body: "{}" });
      refresh();
    } catch (e) {
      setMessage((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function copySecret() {
    if (!secret) return;
    await navigator.clipboard.writeText(secret);
    setMessage("密钥已复制，请立即保存");
  }
  return <section className="data-surface p-6 mb-6"><div className="surface-head"><div><h2 className="font-semibold flex items-center gap-2"><KeyRound size={17} />API 密钥</h2><p className="text-xs text-muted-foreground mt-1">用于自动化调用 API，创建后完整密钥只显示一次。</p></div></div><form className="grid md:grid-cols-[1fr_12rem_auto] gap-3 mt-4" onSubmit={create}><Input value={name} onChange={(event) => setName(event.target.value)} placeholder="密钥名称" aria-label="密钥名称" maxLength={120} required /><Input type="date" value={expiresAt} onChange={(event) => setExpiresAt(event.target.value)} aria-label="过期日期" /><Button disabled={busy || name.trim() === ""}>创建密钥</Button></form>{secret && <div className="mt-4 border border-amber-200 bg-amber-50 p-3 text-sm"><p className="font-medium">请复制并安全保存此密钥</p><div className="flex items-center gap-2 mt-2"><code className="break-all flex-1">{secret}</code><Button type="button" size="icon" variant="outline" aria-label="复制 API 密钥" title="复制" onClick={() => void copySecret()}><Copy size={15} /></Button></div></div>}{message && <p role="alert" className="text-sm text-destructive mt-3">{message}</p>}{error ? <p role="alert" className="text-sm text-destructive mt-4">{error.message}</p> : !data ? <Loading /> : <div className="overflow-auto mt-5"><Table><TableHeader><TableRow><TableHead>名称</TableHead><TableHead>前缀</TableHead><TableHead>最近使用</TableHead><TableHead>过期时间</TableHead><TableHead>状态</TableHead><TableHead /></TableRow></TableHeader><TableBody>{data.length === 0 ? <TableRow><TableCell colSpan={6} className="h-20 text-center text-muted-foreground">暂无密钥</TableCell></TableRow> : data.map((key) => <TableRow key={key.id}><TableCell>{key.name}</TableCell><TableCell className="font-mono text-xs">{key.prefix}</TableCell><TableCell>{key.last_used_at ? date(key.last_used_at) : "未使用"}</TableCell><TableCell>{key.expires_at ? date(key.expires_at) : "永不过期"}</TableCell><TableCell><Status value={key.revoked_at ? "revoked" : "active"} /></TableCell><TableCell>{!key.revoked_at && <Button size="sm" variant="destructive" disabled={busy} onClick={() => void revoke(key.id)}>撤销</Button>}</TableCell></TableRow>)}</TableBody></Table></div>}</section>;
}

function PasswordForm() {
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (newPassword.length < 8 || newPassword.length > 72) { setMessage("新密码需为 8-72 位"); return; }
    setBusy(true); setMessage("");
    try {
      await api("/account/password", { method: "PUT", body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }) });
      setCurrentPassword(""); setNewPassword(""); setMessage("密码已更新");
    } catch (e) { setMessage((e as Error).message); } finally { setBusy(false); }
  }
  return <section className="data-surface p-6 mb-6"><div className="surface-head"><div><h2 className="font-semibold">修改密码</h2><p className="text-xs text-muted-foreground mt-1">修改后其他登录设备会被退出。</p></div></div><form className="grid md:grid-cols-3 gap-3 mt-4" onSubmit={submit}><Input type="password" autoComplete="current-password" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} placeholder="当前密码" aria-label="当前密码" required /><Input type="password" autoComplete="new-password" value={newPassword} onChange={(event) => setNewPassword(event.target.value)} placeholder="新密码（8-72 位）" aria-label="新密码" minLength={8} maxLength={72} required /><div className="flex items-center gap-3"><Button disabled={busy}>更新密码</Button>{message && <span className="text-xs text-muted-foreground">{message}</span>}</div></form></section>;
}
