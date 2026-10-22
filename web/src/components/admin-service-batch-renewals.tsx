"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import { api, date, money, type Service, type User } from "@/lib/api";
import { can } from "@/lib/admin-navigation";
import { useAdminIdentity } from "./workspace";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Checkbox } from "./ui/checkbox";
import { ResourceTable, type ResourceColumn } from "./resource-table";
import { serviceAdjustmentAuthority } from "./admin-service-adjustment-mutation";
import { validRenewalInvoice, validRenewalQuote } from "./admin-service-renewal-mutation";
import { record, whole, useServiceCommand } from "./admin-service-command";
import { validManifest, validBatchCommand, validBatchProgress, type Manifest, type BatchProgress } from "./admin-service-batch-model";

const quoteExpired = (expires: number | null) => expires !== null && expires <= Date.now();
const base = "/admin/service-renewal-batches", permissions = ["services.view", "invoices.view"];
const names: Record<string, string> = { succeeded: "已成功", failed: "确定失败", unconfirmed: "结果未确认", pending: "尚未执行" };
type Prepared = { manifest: Manifest; rows: { id: number; amount: number; note: string }[]; expires: number | null };
export function AdminServiceBatchRenewals({ services, refresh }: { services: Service[]; refresh: () => void }) {
  const actor = useAdminIdentity();
  if (!actor || !permissions.every(p => can(actor, p)) || !can(actor, "services.renew") && !can(actor, "services.renew.pay_balance")) return null;
  return <BatchWorkspace key={serviceAdjustmentAuthority(actor)} actor={actor} services={services} refresh={refresh}/>;
}
function BatchWorkspace({ actor, services, refresh }: { actor: User; services: Service[]; refresh: () => void }) {
  const mutation = useServiceCommand(actor, "renewal-batch", permissions, validBatchCommand, validBatchProgress);
  const [action, setAction] = useState<Manifest["action"]>(can(actor, "services.renew") ? "create" : "pay_balance");
  const [selected, setSelected] = useState<{ id: number; customer: number; name: string }[]>([]), [invoices, setInvoices] = useState<Record<number, string>>({});
  const [reason, setReason] = useState(""), [prepared, setPrepared] = useState<Prepared | null>(null), [issues, setIssues] = useState<string[]>([]);
  const [confirmed, setConfirmed] = useState(false), [executeConfirmed, setExecuteConfirmed] = useState(false), [executeIds, setExecuteIds] = useState<number[]>([]);
  const [progress, setProgress] = useState<BatchProgress | null>(null), [busy, setBusy] = useState(false), [error, setError] = useState("");
  const [expired, setExpired] = useState(false), revision = useRef(0), active = useRef(true), reading = useRef(false);
  useEffect(() => { active.current = true; return () => { active.current = false; }; }, []);
  useEffect(() => { if (!prepared?.expires) return; const timer = window.setTimeout(() => setExpired(true), Math.max(0, prepared.expires - Date.now())); return () => window.clearTimeout(timer); }, [prepared]);
  const current = progress ?? mutation.receipt, hasBatch = !!mutation.command;
  const frozen = busy || mutation.frozen || hasBatch;
  function invalidate() { revision.current++; setPrepared(null); setConfirmed(false); setExpired(false); setIssues([]); setError(""); }
  function choose(s: Service, checked: boolean) {
    invalidate();
    if (!checked) { setSelected(items => items.filter(i => i.id !== s.id)); return; }
    if (selected.length >= 20 || selected.length && selected[0].customer !== s.user_id) { setError("每批最多20项，且必须属于同一客户。"); return; }
    setSelected(items => [...items, { id: s.id, customer: s.user_id, name: s.name }]);
  }
  async function authority(mode: Manifest["action"]) {
    await mutation.identity();
    const live = await api<User>("/me", { portal: "admin" });
    if (!live || serviceAdjustmentAuthority(live) !== serviceAdjustmentAuthority(actor) || !can(live, mode === "create" ? "services.renew" : "services.renew.pay_balance")) throw new Error("当前动作权限或客户范围已变化。");
    if (!active.current) throw new Error("页面已离开。");
  }
  async function prepare() {
    if (frozen || reading.current || !selected.length) return;
    invalidate(); const token = revision.current; reading.current = true; setBusy(true);
    try {
      await authority(action);
      const customer = selected[0].customer;
      const facts = await Promise.all(selected.map(async s => {
        const v = await api<unknown>(`/admin/services/${s.id}`, { portal: "admin" });
        if (!record(v) || v.id !== s.id || v.user_id !== customer || !whole(v.version, 1) || !whole(v.owner_generation) || typeof v.billing_cycle !== "string") throw new Error(`服务 #${s.id} 归属或版本已变化，请重新选择。`);
        return v as unknown as Service;
      }));
      const items: Manifest["items"] = [], rows: Prepared["rows"] = [], failures: string[] = []; let expires: number | null = null;
      if (action === "create") {
        const v = await api<unknown>("/admin/service-renewal-batch-quotes", { method: "POST", portal: "admin", body: JSON.stringify({ customer_id: customer, items: facts.map(s => ({ service_id: s.id, expected_version: s.version })) }) });
        if (!record(v) || v.customer_id !== customer || !Array.isArray(v.items) || v.items.length !== facts.length) throw new Error("批量报价响应无效。");
        for (let n = 0; n < facts.length; n++) {
          const entry = v.items[n], s = facts[n];
          if (!record(entry) || entry.service_id !== s.id) throw new Error("批量报价顺序或服务不一致。");
          if (entry.status === "failed" && typeof entry.code === "string" && typeof entry.error === "string") { failures.push(`#${s.id}：${entry.code} · ${entry.error}`); continue; }
          if (entry.status !== "quoted" || !validRenewalQuote(entry.quote, s, s.version)) throw new Error(`服务 #${s.id} 报价无效。`);
          const q = entry.quote;
          if (!q.can_create) { failures.push(`#${s.id}：已有续费账单 #${q.pending_invoice_id}，切换余额付款后明确读取原单。当前报价不代表原单金额。`); continue; }
          items.push({ service_id: s.id, expected_version: s.version, quote_hash: q.quote_hash });
          rows.push({ id: s.id, amount: q.amount_cents, note: `v${s.version} · 有效至 ${date(q.expires_at)}` });
          expires = Math.min(expires ?? Infinity, Date.parse(q.expires_at));
        }
      } else {
        for (const s of facts) {
          const input = invoices[s.id] ?? "";
          if (!/^[1-9]\d*$/.test(input) || !whole(Number(input), 1)) throw new Error(`请填写服务 #${s.id} 的原续费账单 ID。`);
          const v = await api<unknown>(`/admin/invoices/${input}`, { portal: "admin" });
          if (!validRenewalInvoice(v, s.id, customer, Number(input)) || v.status !== "unpaid" || v.import_review_required || v.payment_pending) throw new Error(`账单 #${input} 必须是该客户/服务的已复核、无未决支付的单项未付续费单。`);
          items.push({ service_id: s.id, invoice_id: v.id, expected_version: v.version, amount_cents: v.amount_cents });
          rows.push({ id: s.id, amount: v.amount_cents, note: `账单 ${v.invoice_no} (#${v.id}) · v${v.version}` });
        }
      }
      const manifest: Manifest = { customer_id: customer, action, reason: reason.trim(), items };
      if (!validManifest(manifest)) { if (active.current && token === revision.current) setIssues(failures); throw new Error("请选择有效服务，并填写操作原因；建单最多240字，付款最多500字。账单不能重复。"); }
      await authority(action);
      if (active.current && token === revision.current) { setIssues(failures); setPrepared({ manifest, rows, expires }); }
    } catch (e) { if (active.current) setError((e as Error).message); }
    finally { reading.current = false; if (active.current) setBusy(false); }
  }
  async function register() {
    if (!prepared || !confirmed || frozen || quoteExpired(prepared.expires)) return;
    try {
      await authority(prepared.manifest.action);
      const result = await mutation.run({ path: base, body: prepared.manifest, context: { manifest: prepared.manifest } });
      if (result && active.current) { setProgress(result); setPrepared(null); setExecuteIds([]); setExecuteConfirmed(false); }
    } catch (e) { if (active.current) setError((e as Error).message); }
  }
  async function readProgress() {
    if (!mutation.command) throw new Error("没有可查询的批次。");
    await authority((mutation.command.context.manifest as Manifest).action);
    const id = mutation.receipt?.batch_id ?? mutation.command.context.batch_id;
    if (typeof id !== "string") throw new Error("登记结果未知，请先恢复原登记请求。");
    const value = await api<unknown>(`${base}/${id}`, { portal: "admin" });
    if (!validBatchProgress(value, mutation.command)) throw new Error("批次进度与原清单不一致。");
    await mutation.identity();
    if (active.current) { setProgress(value); setExecuteConfirmed(false); }
    return value;
  }
  async function queryProgress() {
    if (reading.current) return; reading.current = true; setBusy(true); setError("");
    try { await readProgress(); } catch (e) { if (active.current) { setProgress(null); setError((e as Error).message); } }
    finally { reading.current = false; if (active.current) setBusy(false); }
  }
  async function execute() {
    if (!current || mutation.frozen || busy || reading.current || !executeConfirmed || !executeIds.length) return;
    reading.current = true; setBusy(true); setError("");
    try {
      const latest = await readProgress();
      if (latest.items.some(i => executeIds.includes(i.service_id) && ["succeeded", "unconfirmed"].includes(i.status))) throw new Error("所选项已成功或结果未知，请核对后重新选择；未知项须恢复原请求。");
      const manifest = mutation.command!.context.manifest as Manifest;
      const result = await mutation.run({ path: `${base}/${current.batch_id}/execute`, body: { service_ids: executeIds }, context: { manifest, batch_id: current.batch_id } });
      if (result && active.current) { setProgress(result); setExecuteIds([]); setExecuteConfirmed(false); refresh(); }
    } catch (e) { if (active.current) setError((e as Error).message); }
    finally { reading.current = false; if (active.current) setBusy(false); }
  }
  async function recover() { const value = await mutation.run(); if (value && active.current) { setProgress(value); setExecuteIds([]); setExecuteConfirmed(false); refresh(); } }
  async function close() {
    const ok = await mutation.clear(async () => { const v = await readProgress(); if (v.unconfirmed) throw new Error("仍有未确认项，不能结束此批次。"); });
    if (ok && active.current) { invalidate(); setProgress(null); setExecuteIds([]); setSelected([]); setInvoices({}); setExecuteConfirmed(false); refresh(); }
  }
  const columns: ResourceColumn<BatchProgress["items"][number]>[] = [
    { id: "service", header: "服务 / 选择", render: i => <label className="flex items-center gap-2"><Checkbox aria-label={`选择执行服务 ${i.service_id}`} disabled={mutation.frozen || busy || !["failed", "pending"].includes(i.status)} checked={executeIds.includes(i.service_id)} onCheckedChange={v => { setExecuteConfirmed(false); setExecuteIds(ids => v === true ? [...ids, i.service_id] : ids.filter(id => id !== i.service_id)); }}/><Link className="text-primary" href={`/admin/services/${i.service_id}`}>#{i.service_id}</Link></label> },
    { id: "result", header: "执行事实", render: i => <div className="space-y-1 break-words"><p>{names[i.status]}</p>{i.error && <p className="text-destructive">{i.code} · {i.error}</p>}{i.result && <><p>账单 #{i.result.invoice_id} · {money(i.result.amount_cents)}</p><p>{current?.action === "create" ? "历史建单回执；当前账单另行查看" : i.result.renewal_status === "pending" ? "本地已收款，上游续费待处理" : "本地续费已结算"}</p></>}</div> },
  ];
  return <details className="mb-5 min-w-0 rounded-xl border bg-card p-4" aria-label="同客户批量续费"><summary className="cursor-pointer font-semibold">同客户批量续费{hasBatch ? " · 有已保存批次" : ""}</summary><div className="mt-4 min-w-0 space-y-4 text-sm">
    <p className="text-muted-foreground">同一客户最多20项。先核对清单、登记，再明确执行；登记和查询不会扣款。没有后台自动续跑，关闭页面不能证明在途请求已停止。</p>
    {(error || mutation.error) && <p role="alert" className="break-words text-destructive">{error || mutation.error}</p>}
    {mutation.pending && <div role="status" className="space-y-3 rounded border p-3"><p>原{mutation.pending.path.endsWith("/execute") ? "执行" : "登记"}请求{mutation.pending.rejected ? "明确拒绝" : "结果未知"}，保留原清单、内容和请求键。</p><Button variant="outline" disabled={mutation.busy || mutation.blocked || busy} onClick={() => void (mutation.pending?.rejected ? (mutation.command?.context.batch_id ? mutation.reconcile(readProgress) : mutation.clear(() => mutation.identity())).then(ok => { if (ok) { setProgress(null); invalidate(); } }) : recover())}>{mutation.pending.rejected ? "核对事实并结束被拒请求" : "恢复原批次请求"}</Button></div>}
    {!hasBatch && <>
      <fieldset disabled={frozen} className="min-w-0 space-y-4"><label className="block space-y-2"><span>批次动作</span><select aria-label="批次动作" className="h-9 w-full rounded-md border bg-background px-3" value={action} onChange={e => { invalidate(); setAction(e.target.value as Manifest["action"]); }}><option value="create" disabled={!can(actor, "services.renew")}>仅创建未付续费单</option><option value="pay_balance" disabled={!can(actor, "services.renew.pay_balance")}>用客户余额支付原续费单</option></select></label>
      <p>已选 {selected.length}/20 项{selected.length ? ` · 固定客户 #${selected[0].customer}` : " · 选择首项后固定客户"}。翻页保留已选服务。</p>
      <div className="grid min-w-0 gap-2 lg:grid-cols-2">{services.map(s => <label key={s.id} className="flex min-w-0 items-start gap-2 rounded border p-3"><Checkbox aria-label={`选择批量服务 ${s.id}`} checked={selected.some(i => i.id === s.id)} disabled={!selected.some(i => i.id === s.id) && (selected.length >= 20 || !!selected.length && selected[0].customer !== s.user_id)} onCheckedChange={v => choose(s, v === true)}/><span className="min-w-0 break-words">#{s.id} · {s.name} · 客户 #{s.user_id}</span></label>)}</div>
      {selected.map(s => <div key={s.id} className="flex min-w-0 flex-wrap items-center gap-2 rounded bg-muted/30 p-2"><span className="min-w-0 flex-1 break-words">已选 #{s.id} · {s.name}</span>{action === "pay_balance" && <Input aria-label={`服务 ${s.id} 续费账单 ID`} className="w-40" inputMode="numeric" placeholder="原续费账单 ID" value={invoices[s.id] ?? ""} onChange={e => { invalidate(); setInvoices(v => ({ ...v, [s.id]: e.target.value })); }}/>}<Button variant="ghost" onClick={() => { invalidate(); setSelected(v => v.filter(i => i.id !== s.id)); }}>移除 #{s.id}</Button></div>)}
      <label className="block space-y-2"><span>批量操作原因</span><Textarea aria-label="批量操作原因" maxLength={action === "create" ? 240 : 500} value={reason} onChange={e => { invalidate(); setReason(e.target.value); }}/></label><Button variant="outline" disabled={!selected.length} onClick={() => void prepare()}>{action === "create" ? "读取批量报价" : "读取原续费账单并核对"}</Button></fieldset>
      {issues.map((issue, n) => <p key={n} role="status" className="break-words text-muted-foreground">未纳入清单：{issue}</p>)}
      {prepared && <div className="space-y-3 rounded bg-muted/30 p-3"><h3 className="font-semibold">核对登记清单 · 客户 #{prepared.manifest.customer_id}</h3>{prepared.rows.map(r => <p key={r.id} className="break-words">服务 #{r.id} · {money(r.amount)} · {r.note}</p>)}<p className="whitespace-pre-wrap break-words">原因：{prepared.manifest.reason}</p><p>共 {prepared.rows.length} 项。{action === "create" ? "登记后需在有效期内另行执行建单，不自动付款。" : "金额为原单精确应付，零元也须确认；执行将使用此客户余额。"}</p>{expired && <p role="alert" className="text-destructive">报价已过期，请重新读取。</p>}<label className="flex items-start gap-2"><Checkbox aria-label="确认批量登记清单" checked={confirmed} disabled={frozen || expired} onCheckedChange={v => setConfirmed(v === true)}/><span>已核对以上客户、服务、动作和金额，确认登记此清单。</span></label><Button disabled={frozen || expired || !confirmed} onClick={() => void register()}>登记批次（不执行）</Button></div>}
    </>}
    {current && <div className="min-w-0 space-y-4"><p className="break-all">批次 {current.batch_id}</p><p>客户 #{current.customer_id} · {current.action === "create" ? "仅建未付单" : "客户余额付款"} · 成功 {current.succeeded} / 失败 {current.failed} / 未确认 {current.unconfirmed} / 未执行 {current.pending}</p><p className="text-muted-foreground">以下为历史执行事实；账单和当前期限需进入详情核对。供应商 pending 不代表上游完成。失败项需重新核对并显式选中重试。</p><ResourceTable items={current.items} rowKey={i => i.service_id} columns={columns} ariaLabel="批量续费进度" renderMobile={i => <article className="space-y-3 p-3">{columns.map(c => <div key={c.id}>{c.render(i)}</div>)}</article>}/><label className="flex items-start gap-2"><Checkbox aria-label="确认执行所选续费项" checked={executeConfirmed} disabled={mutation.frozen || busy || !executeIds.length} onCheckedChange={v => setExecuteConfirmed(v === true)}/><span>确认按原清单顺序执行所选 {executeIds.length} 项{current.action === "pay_balance" ? "，允许扣客户余额" : "，只建未付单"}。</span></label><div className="flex flex-wrap gap-2"><Button variant="outline" disabled={mutation.busy || busy || mutation.blocked} onClick={() => void queryProgress()}>查询批次进度</Button><Button disabled={mutation.frozen || busy || !executeConfirmed || !executeIds.length} onClick={() => void execute()}>执行所选项</Button><Button variant="outline" disabled={mutation.frozen || busy || !!current.unconfirmed} onClick={() => void close()}>结束查看并新建批次</Button></div></div>}
  </div></details>;
}
