"use client";
import { CustomerCreditOverview } from "./customer-credit";
import { TicketNotificationPreferences } from "./ticket-notification-preferences";
import { NotificationInbox, NotificationPreferences } from "./notification-preferences";
import { ContactsPanel } from "./admin-customer-detail";
import { AccountVerification } from "./account-verification";
import { useState } from "react";
import Link from "next/link";
import { KeyRound, Copy } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Table, TableHeader, TableHead, TableBody, TableRow, TableCell } from "@/components/ui/table";
import { api, date, money, type ApiKey, type User } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Failure, Loading, Status } from "./common";
import { Workspace } from "./workspace";
import { MfaPanel } from "./mfa-panel";
import { CustomerTopup } from "./customer-topup";
import { PageHeading } from "./resource-page";

type Account = {
  phone?: string;
  phone_verified?: boolean;
  email_verified?: boolean;
  balance_cents: number;
  ledger: {
    id: number;
    entry_type: string;
    reference_type?: string;
    reference_id?: number;
    delta_cents: number;
    balance_after: number;
    note: string;
    created_at: string;
  }[];
};

export function AccountView() {
  const { data, error, refresh } = useResource<Account>("/account");
  const me = useResource<User>("/me");
  return (
    <Workspace title="账户余额">
      <PageHeading title="账户余额" description="余额流水与可用金额" />
      <CustomerCreditOverview />
      {error ? <Failure error={error} retry={refresh} /> : !data ? <Loading /> : (
        <>
          <section className="data-surface p-6 mb-6">
            <p className="text-sm text-muted-foreground">可用余额</p>
            <strong className="text-3xl money block mt-2">{money(data.balance_cents)}</strong>
          </section>
          <CustomerTopup />
          <PasswordForm />
          {me.error ? <Failure error={me.error} retry={me.refresh} /> : !me.data ? <Loading /> : <AccountVerification email={me.data.email} phone={data.phone} phoneVerified={data.phone_verified} emailVerified={data.email_verified} onVerified={refresh} />}
          <TicketNotificationPreferences />
          <NotificationPreferences />
          <NotificationInbox />
          <MfaPanel />
          <ApiKeysPanel />
          <ContactsPanel />
          <div className="data-surface overflow-auto">
            <Table>
              <TableHeader><TableRow><TableHead>时间</TableHead><TableHead>类型</TableHead><TableHead>变动</TableHead><TableHead>余额</TableHead><TableHead>备注</TableHead></TableRow></TableHeader>
              <TableBody>
                {data.ledger.length === 0 ? <TableRow><TableCell colSpan={5} className="h-28 text-center text-muted-foreground">暂无流水</TableCell></TableRow> : data.ledger.map((entry) => (
                  <TableRow key={entry.id}>
                    <TableCell>{date(entry.created_at)}</TableCell>
                    <TableCell>{entry.entry_type === "payment" ? "账单支付" : entry.entry_type === "refund" ? "退款" : entry.reference_type === "gateway_topup" || entry.entry_type === "online_topup" ? "在线充值入账" : entry.entry_type === "topup" ? "管理员充值" : "余额调整"}</TableCell>
                    <TableCell className={entry.delta_cents < 0 ? "text-destructive money" : "text-emerald-700 money"}>{entry.delta_cents > 0 ? "+" : ""}{money(entry.delta_cents)}</TableCell>
                    <TableCell className="money">{money(entry.balance_after)}</TableCell>
                    <TableCell>{entry.note || "-"}{entry.reference_type === "gateway_topup" && entry.reference_id && <Link className="block text-primary hover:underline" href={`/console/invoices/${entry.reference_id}?from=topup`}>查看充值账单</Link>}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        </>
      )}
    </Workspace>
  );
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
