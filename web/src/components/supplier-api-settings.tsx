"use client";

import { useEffect, useState } from "react";
import { CheckCircle2, PlugZap, RefreshCw, Save, XCircle } from "lucide-react";
import { usePendingSetting } from "@/hooks/use-pending-setting";
import { useResource } from "@/hooks/use-resource";
import { ApiError, api, date } from "@/lib/api";
import { Workspace, useAdminIdentity, useAdminPermission } from "./workspace";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";

type Capabilities = { catalog: boolean; product: boolean; create: boolean; sync: boolean; power: boolean; reset_password: boolean; rescue: boolean; vnc: boolean; reinstall: boolean; suspend: boolean; resume: boolean; terminate: boolean; renew: boolean };
type ProviderDefinition = { provider: { id: string; name: string; capabilities: Capabilities }; descriptor: { id: string; name: string; fields?: { key: string; label: string; type: string; required: boolean; secret: boolean; options?: string[] }[]; capabilities?: string[] } };
type Account = { id: number; name: string; provider: string; base_url: string; username: string; active: boolean; version: number; credentials_configured: boolean; product_count: number; running_job_id?: number | null };
type ConnectionResult = { ok: boolean; message: string; product_count: number; duration_ms: number; checked_at: string };
type Settings = { enabled: boolean; require_certification: boolean; require_verified_phone: boolean; version: number; updated_at: string; providers: ProviderDefinition[]; accounts: Account[]; account_total: number };
type ResourceForm = Pick<Settings, "enabled" | "require_certification" | "require_verified_phone">;
type ResourceDraft = { base_version: number; form: ResourceForm };

function idempotencyKey(prefix: string) {
  return typeof crypto !== "undefined" && "randomUUID" in crypto ? `${prefix}-${crypto.randomUUID()}` : `${prefix}-${Date.now()}-${Math.random()}`;
}

const capabilityLabels: Record<string, string> = { catalog: "目录", product: "商品", create: "开通", sync: "同步", power: "电源", reset_password: "重置密码", rescue: "救援", vnc: "VNC", reinstall: "重装", suspend: "暂停", resume: "恢复", terminate: "终止", renew: "续费" };
const uncertain = (error: unknown) => !(error instanceof ApiError) || error.status >= 500;

export function SupplierAPISettingsView() {
  const resource = useResource<Settings>("/admin/supplier-api-settings", false);
  return <Workspace title="供应商 API 设置" admin><div className="space-y-8"><div className="flex items-center justify-between gap-3"><div><h1 className="page-title">供应商 API 设置</h1><p className="mt-2 text-sm text-muted-foreground">管理客户资源 API 的开放条件，并查看编译进系统的上游供应商。</p></div><Button type="button" variant="outline" size="icon" title="重新加载" aria-label="重新加载" disabled={!resource.data} onClick={resource.refresh}><RefreshCw size={16} /></Button></div>{resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <SettingsContent key={resource.data.version} data={resource.data} refresh={resource.refresh} />}</div></Workspace>;
}

function SettingsContent({ data, refresh }: { data: Settings; refresh: () => void }) {
  const actor = useAdminIdentity();
  const manage = useAdminPermission("products.manage");
  const storageKey = actor ? `zjmfgo:resource-api-settings:${actor.id}` : null;
  const pending = usePendingSetting("resource-api-settings", actor?.id ?? null);
  const [form, setForm] = useState<ResourceForm>(() => ({ enabled: data.enabled, require_certification: data.require_certification, require_verified_phone: data.require_verified_phone }));
  const [draft, setDraft] = useState<ResourceDraft | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [saved, setSaved] = useState(false);
  const [testBusy, setTestBusy] = useState<number | null>(null);
  const [testResults, setTestResults] = useState<Record<number, ConnectionResult>>({});
  const dirty = form.enabled !== data.enabled || form.require_certification !== data.require_certification || form.require_verified_phone !== data.require_verified_phone;
  const unknown = pending.unknown;

  useEffect(() => {
    if (!pending.ready) return;
    let active = true;
    const timer = window.setTimeout(() => {
      if (!active) return;
      if (pending.unknown && pending.pending.current) {
        try {
          const request = JSON.parse(pending.pending.current.body) as ResourceForm;
          if (typeof request.enabled === "boolean" && typeof request.require_certification === "boolean" && typeof request.require_verified_phone === "boolean") {
            setForm({ enabled: request.enabled, require_certification: request.require_certification, require_verified_phone: request.require_verified_phone });
          }
          setMessage("上次保存结果暂无法确认。请仅重试原请求，勿离开或刷新页面。");
        } catch { setMessage("上次保存结果暂无法确认；请重试原请求或联系管理员核对。"); }
      } else if (storageKey && manage) {
        try {
          const raw = sessionStorage.getItem(storageKey);
          const value = raw ? JSON.parse(raw) as ResourceDraft : null;
          if (value && Number.isInteger(value.base_version) && value.form && typeof value.form.enabled === "boolean" && typeof value.form.require_certification === "boolean" && typeof value.form.require_verified_phone === "boolean") setDraft(value);
        } catch { /* Session storage is optional. */ }
      }
      setLoaded(true);
    }, 0);
    return () => { active = false; window.clearTimeout(timer); };
  }, [pending.ready, pending.unknown, pending.pending, storageKey, manage]);

  useEffect(() => {
    if (!loaded || !storageKey || !manage || unknown || saved) return;
    const timer = window.setTimeout(() => {
      try {
        if (dirty) sessionStorage.setItem(storageKey, JSON.stringify({ base_version: data.version, form } satisfies ResourceDraft));
        else sessionStorage.removeItem(storageKey);
      } catch { /* Draft recovery is best-effort; saved settings remain on the server. */ }
    }, 0);
    return () => window.clearTimeout(timer);
  }, [loaded, storageKey, manage, unknown, saved, dirty, data.version, form]);

  useEffect(() => {
    if (!loaded || (!dirty && !unknown)) return;
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    const click = (event: MouseEvent) => {
      const anchor = event.target instanceof Element ? event.target.closest("a[href]") : null;
      if (anchor && !event.ctrlKey && !event.metaKey && !event.shiftKey && (unknown || !window.confirm("离开后将丢弃未保存更改，会话草稿会保留。确认离开？"))) {
        event.preventDefault();
        event.stopPropagation();
      }
    };
    window.addEventListener("beforeunload", warn);
    document.addEventListener("click", click, true);
    return () => { window.removeEventListener("beforeunload", warn); document.removeEventListener("click", click, true); };
  }, [loaded, dirty, unknown]);

  function clearDraft() {
    if (storageKey) {
      try { sessionStorage.removeItem(storageKey); } catch { /* Session storage is optional. */ }
    }
    setDraft(null);
  }

  function cancelChanges() {
    if (!dirty || !window.confirm("确认取消更改并丢弃当前设置草稿？")) return;
    setForm({ enabled: data.enabled, require_certification: data.require_certification, require_verified_phone: data.require_verified_phone });
    clearDraft();
    setError("");
    setMessage("");
    setSaved(false);
  }

  function recoverDraft() {
    if (!draft || (draft.base_version !== data.version && !window.confirm("草稿版本与当前设置不同。恢复后请逐项核对再保存，是否继续？"))) return;
    setForm(draft.form);
    clearDraft();
    setError("");
    setMessage("");
  }

  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!manage || !loaded || busy || (!dirty && !unknown)) return;
    if (!unknown && data.enabled && !form.enabled && !window.confirm("关闭资源 API 后，所有客户现有 Bearer API 调用也会立即被拒绝。确认关闭？")) return;
    setBusy(true);
    setError("");
    setMessage("");
    const body = unknown && pending.pending.current ? pending.pending.current.body : JSON.stringify({ ...form, expected_version: data.version });
    const operation = unknown && pending.pending.current ? pending.pending.current : { body, key: idempotencyKey("resource-api-settings"), path: "/admin/supplier-api-settings" };
    pending.remember(operation);
    try {
      const result = await api<Settings>("/admin/supplier-api-settings", { method: "PUT", headers: { "Idempotency-Key": operation.key }, body: operation.body });
      pending.resolve();
      clearDraft();
      setForm({ enabled: result.enabled, require_certification: result.require_certification, require_verified_phone: result.require_verified_phone });
      setSaved(true);
      setMessage("设置已保存");
      refresh();
    } catch (cause) {
      if (uncertain(cause)) {
        pending.setUnknown(true);
        setMessage("保存结果暂无法确认。请仅重试原请求，勿离开或刷新页面。");
      } else {
        pending.resolve();
        if (cause instanceof ApiError && cause.code === "version_conflict") {
          if (storageKey) {
            try { sessionStorage.setItem(storageKey, JSON.stringify({ base_version: data.version, form } satisfies ResourceDraft)); } catch { /* See above. */ }
          }
          setMessage("设置版本已被其他管理员更新。草稿已保留，请重新加载并对照最新值后恢复。");
          refresh();
        } else {
          setError((cause as Error).message);
        }
      }
    } finally { setBusy(false); }
  }

  async function testConnection(account: Account) {
    if (!manage || testBusy !== null || !loaded) return;
    setTestBusy(account.id);
    setError("");
    try {
      const result = await api<ConnectionResult>(`/admin/suppliers/${account.id}/test-connection`, { method: "POST", headers: { "Idempotency-Key": idempotencyKey(`supplier-test-${account.id}`) }, body: JSON.stringify({ expected_version: account.version }) });
      setTestResults(current => ({ ...current, [account.id]: result }));
    } catch (cause) { setError((cause as Error).message); }
    finally { setTestBusy(null); }
  }

  return <>
    {manage && draft && !unknown && <div role="status" className="flex flex-wrap items-center gap-3 rounded-md border p-3 text-sm"><span>{draft.base_version === data.version ? "发现未保存的会话草稿。" : "发现旧版本草稿，恢复后需要对照当前设置逐项检查。"}</span><Button type="button" variant="outline" disabled={!loaded || busy} onClick={recoverDraft}>恢复草稿</Button><Button type="button" variant="outline" disabled={!loaded || busy} onClick={clearDraft}>丢弃草稿</Button></div>}
    <form onSubmit={save} className="max-w-4xl space-y-5">
      <section className="space-y-5 rounded-md border bg-card p-5">
        <div><h2 className="font-semibold">资源 API</h2><p className="mt-1 text-sm text-muted-foreground">关闭后，客户 API 密钥签发和已有客户 Bearer 调用都会被拒绝。</p></div>
        <fieldset disabled={!manage || !loaded || busy || unknown} className="space-y-5">
          <label className="flex items-start gap-3 text-sm"><Checkbox checked={form.enabled} onCheckedChange={checked => setForm(current => ({ ...current, enabled: checked === true }))} /><span><strong className="font-medium">是否开启资源 API</strong><span className="mt-1 block text-xs text-muted-foreground">开启后，客户可按下方条件申请资源 API 密钥。</span></span></label>
          {form.enabled && <div className="space-y-4 border-t pt-5"><div><h3 className="font-medium">API 密钥获取条件</h3><p className="mt-1 text-xs text-muted-foreground">只有满足已开启的条件，客户才可以创建资源 API 密钥。</p></div><div className="grid gap-4 sm:grid-cols-2"><label className="flex items-start gap-3 text-sm"><Checkbox checked={form.require_certification} onCheckedChange={checked => setForm(current => ({ ...current, require_certification: checked === true }))} /><span><strong className="font-medium">需要实名认证</strong><span className="mt-1 block text-xs text-muted-foreground">必须存在通过审核且资料有效的实名认证。</span></span></label><label className="flex items-start gap-3 text-sm"><Checkbox checked={form.require_verified_phone} onCheckedChange={checked => setForm(current => ({ ...current, require_verified_phone: checked === true }))} /><span><strong className="font-medium">需要绑定手机号</strong><span className="mt-1 block text-xs text-muted-foreground">必须完成手机验证并保持号码已绑定。</span></span></label></div></div>}
        </fieldset>
        {(error || message || unknown) && <p role={error ? "alert" : "status"} className={`break-words text-sm ${error ? "text-destructive" : "text-muted-foreground"}`}>{error || message}</p>}
        <div className="flex flex-wrap items-center justify-between gap-3 border-t pt-4"><span className="text-xs text-muted-foreground">版本 {data.version} · 最后更新 {date(data.updated_at)}{saved && <span className="ml-2 text-emerald-700">已保存</span>}</span><div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={!loaded || busy || unknown || !dirty} onClick={cancelChanges}>取消更改</Button>{manage && <Button disabled={!loaded || busy || (!dirty && !unknown)}><Save size={16} />{busy ? "保存中…" : unknown ? "重试原请求" : "保存更改"}</Button>}</div></div>
      </section>
    </form>

    <section className="space-y-4"><div><h2 className="text-lg font-semibold">已编译供应商</h2><p className="mt-1 text-sm text-muted-foreground">供应商适配器编译进 Go 二进制；这里仅展示能力和配置 schema。</p></div><div className="overflow-x-auto border-y"><Table><TableHeader><TableRow><TableHead>供应商</TableHead><TableHead>实现标识</TableHead><TableHead>配置字段</TableHead><TableHead>能力</TableHead></TableRow></TableHeader><TableBody>{data.providers.map(item => <TableRow key={item.provider.id}><TableCell className="min-w-36"><p className="font-medium">{item.provider.name}</p><p className="mt-1 text-xs text-muted-foreground">{item.provider.id}</p></TableCell><TableCell className="font-mono text-xs">{item.descriptor.id}</TableCell><TableCell className="min-w-48 text-sm">{(item.descriptor.fields ?? []).map(field => <p key={field.key}>{field.label}{field.required ? " · 必填" : ""}{field.secret ? " · 密钥" : ""}</p>)}</TableCell><TableCell className="min-w-56"><div className="flex flex-wrap gap-1">{Object.entries(item.provider.capabilities).filter(([, enabled]) => enabled).map(([key]) => <span key={key} className="rounded bg-muted px-2 py-1 text-xs">{capabilityLabels[key] ?? key}</span>)}</div></TableCell></TableRow>)}{data.providers.length === 0 && <TableRow><TableCell colSpan={4} className="py-10 text-center text-muted-foreground">暂无已编译供应商</TableCell></TableRow>}</TableBody></Table></div></section>

    <section className="space-y-4"><div><h2 className="text-lg font-semibold">供应商账户与连通性</h2><p className="mt-1 text-sm text-muted-foreground">测试只读取上游目录，不会导入商品或执行开通动作。</p></div><div className="overflow-x-auto border-y"><Table><TableHeader><TableRow><TableHead>账户</TableHead><TableHead>接口 / 用户名</TableHead><TableHead>状态</TableHead><TableHead>本地商品</TableHead><TableHead>连通性</TableHead></TableRow></TableHeader><TableBody>{data.accounts.map(account => { const result = testResults[account.id]; return <TableRow key={account.id}><TableCell className="min-w-36"><p className="font-medium">{account.name}</p><p className="mt-1 text-xs text-muted-foreground">{data.providers.find(item => item.provider.id === account.provider)?.provider.name ?? account.provider}</p></TableCell><TableCell className="max-w-xs break-all"><p>{account.base_url}</p><p className="mt-1 text-xs text-muted-foreground">{account.username}</p></TableCell><TableCell className="whitespace-nowrap">{account.active ? "已启用" : "已停用"}<p className="mt-1 text-xs text-muted-foreground">{account.credentials_configured ? "凭据已配置" : "凭据未配置"} · 版本 {account.version}</p></TableCell><TableCell>{account.product_count}</TableCell><TableCell className="min-w-52"><div className="flex flex-wrap items-center gap-2"><Button type="button" variant="outline" size="sm" disabled={!manage || !loaded || testBusy !== null || !account.active || !account.credentials_configured} onClick={() => void testConnection(account)}><PlugZap size={14} />{testBusy === account.id ? "测试中…" : "测试连接"}</Button>{result && <span className={`inline-flex items-center gap-1 text-xs ${result.ok ? "text-emerald-700" : "text-destructive"}`}>{result.ok ? <CheckCircle2 size={14} /> : <XCircle size={14} />}{result.message}</span>}</div>{result && <p className="mt-1 text-xs text-muted-foreground">目录 {result.product_count} 个 · {result.duration_ms} ms · {date(result.checked_at)}</p>}{account.running_job_id && <p className="mt-1 text-xs text-amber-700">交付任务 #{account.running_job_id} 待核对</p>}</TableCell></TableRow>; })}{data.accounts.length === 0 && <TableRow><TableCell colSpan={5} className="py-10 text-center text-muted-foreground">暂无供应商账户</TableCell></TableRow>}</TableBody></Table></div><p className="text-xs text-muted-foreground">共 {data.account_total} 个账户。密钥字段只返回“已配置”状态，修改凭据请返回供应商管理页面。</p></section>
  </>;
}
