"use client";

import { useEffect, useRef, useState } from "react";
import { api, date, money, type Service, type User } from "@/lib/api";
import { can } from "@/lib/admin-navigation";
import { useAdminIdentity } from "./workspace";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Checkbox } from "./ui/checkbox";
import { serviceAdjustmentAuthority, serviceTermMicros } from "./admin-service-adjustment-mutation";
import { digest, record, whole, reasonText, useServiceCommand, type ServiceCommand } from "./admin-service-command";

const permissions = ["services.view", "customers.view", "services.transfer"];
type TransferQuote = { service_id: number; source_customer_id: number; target_customer_id: number; version: number; owner_generation: number; auto_renew: false; current_due_at: string | null; renewal_pricing_mode: string; renewal_amount_cents: number; quote_hash: string; expires_at: string };
type TransferReceipt = { service_id: number; source_customer_id: number; target_customer_id: number; version: number; owner_generation: number; auto_renew: false; replayed: boolean };
export function validTransferCommand(v: unknown, id: number): v is ServiceCommand {
  if (!record(v) || v.path !== `/admin/services/${id}/transfers` || !record(v.body) || !record(v.context)) return false;
  const b = v.body;
  return Object.keys(b).length === 8 && Object.keys(b).every(k => ["expected_version", "expected_owner_generation", "source_customer_id", "target_customer_id", "credential_handover_confirmed", "reason", "handover_evidence", "quote_hash"].includes(k)) && whole(b.expected_version, 1) && whole(b.expected_owner_generation) && whole(b.source_customer_id, 1) && whole(b.target_customer_id, 1) && b.source_customer_id !== b.target_customer_id && b.credential_handover_confirmed === true && reasonText(b.reason) && reasonText(b.handover_evidence) && digest(b.quote_hash);
}
export function validTransferReceipt(v: unknown, c: ServiceCommand): v is TransferReceipt {
  const b = c.body;
  return record(v) && c.path === `/admin/services/${v.service_id}/transfers` && v.source_customer_id === b.source_customer_id && v.target_customer_id === b.target_customer_id && v.version === Number(b.expected_version) + 1 && v.owner_generation === Number(b.expected_owner_generation) + 1 && v.auto_renew === false && typeof v.replayed === "boolean";
}
export function transferCustomerCandidates(v: unknown) {
  if (!record(v) || !Array.isArray(v.items) || !v.items.every(c => record(c) && whole(c.id, 1) && typeof c.display_name === "string" && typeof c.email === "string" && typeof c.status === "string")) throw new Error("客户候选响应无效。");
  return v.items.filter(c => c.status === "active").map(c => ({ id: c.id as number, name: c.display_name as string, email: c.email as string }));
}
const panel = "min-w-0 space-y-4 rounded border bg-card p-4 sm:p-5";
export function AdminServiceTransfer({ service, refresh }: { service: Service; refresh: () => void }) {
  const actor = useAdminIdentity();
  if (!actor || !permissions.every(p => can(actor, p))) return <section className={panel}><h2 className="font-semibold">服务转移客户</h2><p role="status" className="text-sm text-muted-foreground">需要服务查看、客户查看和独立服务转移权限。</p></section>;
  // Ownership changes are the successful outcome. Keep the same journal mounted.
  return <TransferWorkspace key={`${serviceAdjustmentAuthority(actor)}:${service.id}`} actor={actor} service={service} refresh={refresh}/>;
}
function TransferWorkspace({ actor, service, refresh }: { actor: User; service: Service; refresh: () => void }) {
  const mutation = useServiceCommand(actor, `transfer:${service.id}`, permissions, (v): v is ServiceCommand => validTransferCommand(v, service.id), validTransferReceipt);
  const [target, setTarget] = useState(""), [query, setQuery] = useState(""), [candidates, setCandidates] = useState<{ id: number; name: string; email: string }[]>([]);
  const [reason, setReason] = useState(""), [evidence, setEvidence] = useState(""), [handover, setHandover] = useState(false), [confirmed, setConfirmed] = useState(false);
  const [preview, setPreview] = useState<{ quote: TransferQuote; command: ServiceCommand } | null>(null), [expired, setExpired] = useState(false), [busy, setBusy] = useState(false), [error, setError] = useState("");
  const revision = useRef(0), active = useRef(true), reading = useRef(false);
  useEffect(() => { active.current = true; return () => { active.current = false; }; }, []);
  useEffect(() => { if (!preview) return; const timer = window.setTimeout(() => setExpired(true), Math.max(0, Date.parse(preview.quote.expires_at) - Date.now())); return () => window.clearTimeout(timer); }, [preview]);
  function invalidate() { revision.current++; setPreview(null); setConfirmed(false); setExpired(false); setError(""); }
  async function facts() {
    const v = await api<unknown>(`/admin/services/${service.id}`, { portal: "admin" });
    if (!record(v) || v.id !== service.id || !whole(v.user_id, 1) || !whole(v.version, 1) || !whole(v.owner_generation) || typeof v.status !== "string") throw new Error("服务身份、版本或归属代次无效。");
    return v;
  }
  async function search() {
    if (reading.current || mutation.frozen) return;
    reading.current = true; setBusy(true); setError(""); setCandidates([]);
    try {
      await mutation.identity();
      const v = await api<unknown>(`/admin/customers/page?q=${encodeURIComponent(query.trim())}&limit=10`, { portal: "admin" });
      await mutation.identity();
      const choices = transferCustomerCandidates(v);
      if (active.current) setCandidates(choices);
    } catch (e) { if (active.current) setError((e as Error).message); }
    finally { reading.current = false; if (active.current) setBusy(false); }
  }
  async function quote() {
    if (reading.current || mutation.frozen) return;
    invalidate(); const token = revision.current;
    reading.current = true; setBusy(true);
    try {
      if (!/^[1-9]\d*$/.test(target) || !whole(Number(target), 1) || !handover || !reasonText(reason.trim()) || !reasonText(evidence.trim())) throw new Error("请选择目标客户，完成真实凭据交接，并填写1–500字原因与核查说明。");
      await mutation.identity(); const current = await facts();
      if (current.user_id !== service.user_id || current.owner_generation !== service.owner_generation || !["active", "suspended"].includes(String(current.status))) throw new Error("服务归属或状态已变化，请刷新并核对。");
      const body = { expected_version: current.version, expected_owner_generation: current.owner_generation, source_customer_id: current.user_id, target_customer_id: Number(target), credential_handover_confirmed: true, reason: reason.trim(), handover_evidence: evidence.trim() };
      const q = await api<unknown>(`/admin/services/${service.id}/transfer-quotes`, { method: "POST", portal: "admin", body: JSON.stringify(body) });
      const command = { path: `/admin/services/${service.id}/transfers`, body: { ...body, quote_hash: record(q) ? q.quote_hash : null }, context: {} };
      if (!validTransferCommand(command, service.id) || !record(q) || q.service_id !== service.id || q.source_customer_id !== body.source_customer_id || q.target_customer_id !== body.target_customer_id || q.version !== body.expected_version || q.owner_generation !== body.expected_owner_generation || q.auto_renew !== false || !whole(q.renewal_amount_cents) || !["contract", "fixed"].includes(String(q.renewal_pricing_mode)) || q.current_due_at !== null && serviceTermMicros(q.current_due_at) === null || serviceTermMicros(q.expires_at) === null || Date.parse(String(q.expires_at)) <= Date.now()) throw new Error("转移报价与当前服务不一致或已过期。");
      await mutation.identity();
      if (active.current && token === revision.current) setPreview({ quote: q as TransferQuote, command });
    } catch (e) { if (active.current) setError((e as Error).message); }
    finally { reading.current = false; if (active.current) setBusy(false); }
  }
  async function submit(recover = false) {
    if (!recover && (!preview || !confirmed || expired || Date.parse(preview.quote.expires_at) <= Date.now() || mutation.frozen)) return;
    const receipt = await mutation.run(recover ? undefined : preview!.command);
    if (receipt && active.current) { invalidate(); setHandover(false); refresh(); }
  }
  const locked = mutation.frozen || busy, pending = mutation.pending, receipt = mutation.receipt;
  return <section className={panel} aria-label="服务转移客户">
    <header><h2 className="font-semibold">服务转移客户</h2><p className="mt-1 text-sm text-muted-foreground">当前客户 #{service.user_id} · 服务 #{service.id}。转移不改变价格和期限；自动续费将关闭，旧财务记录留在原客户。</p></header>
    {(error || mutation.error) && <p role="alert" className="break-words text-sm text-destructive">{error || mutation.error}</p>}
    {pending && <div role="status" className="space-y-3 rounded border p-3 text-sm"><p>原请求：客户 #{String(pending.body.source_customer_id)} → #{String(pending.body.target_customer_id)}。{pending.rejected ? "明确拒绝，请读取事实后重新确认。" : "结果待核验，已保留原始内容与请求键。"}</p><Button variant="outline" disabled={mutation.busy || mutation.blocked} onClick={() => void (pending.rejected ? mutation.clear(facts).then(ok => { if (ok) { invalidate(); refresh(); } }) : submit(true))}>{pending.rejected ? "核对当前服务并结束被拒请求" : "恢复原服务转移请求"}</Button></div>}
    {receipt && <div role="status" className="space-y-2 rounded bg-muted/40 p-3 text-sm"><p>历史转移回执：客户 #{receipt.source_customer_id} → #{receipt.target_customer_id} · v{receipt.version} · 归属代次 {receipt.owner_generation}{receipt.replayed ? "（原请求恢复）" : ""}</p><p className="text-muted-foreground">这是原次转移事实；当前归属以上方刷新后的服务为准。真实资源密码、会话撤回和数据交接须由管理员完成。</p></div>}
    <fieldset disabled={locked} className="min-w-0 space-y-4 text-sm">
      <div className="flex min-w-0 flex-wrap gap-2"><Input className="min-w-0 flex-1" aria-label="搜索转入客户" placeholder="客户名称或邮箱" value={query} onChange={e => setQuery(e.target.value)}/><Button variant="outline" onClick={() => void search()}>搜索客户</Button></div>
      {candidates.map(c => <Button key={c.id} variant="outline" className="h-auto max-w-full whitespace-normal break-all text-left" onClick={() => { invalidate(); setTarget(String(c.id)); setCandidates([]); }}>选择 #{c.id} · {c.name} · {c.email}</Button>)}
      <label className="block space-y-2"><span>目标客户 ID</span><Input aria-label="目标客户 ID" inputMode="numeric" value={target} onChange={e => { invalidate(); setTarget(e.target.value); }}/></label>
      <label className="block space-y-2"><span>转移原因</span><Textarea aria-label="转移原因" maxLength={500} value={reason} onChange={e => { invalidate(); setReason(e.target.value); }}/></label>
      <label className="block space-y-2"><span>凭据及数据交接核查说明</span><Textarea aria-label="凭据及数据交接核查说明" maxLength={500} value={evidence} onChange={e => { invalidate(); setEvidence(e.target.value); }}/></label>
      <label className="flex items-start gap-2"><Checkbox aria-label="确认真实凭据及数据已交接" checked={handover} onCheckedChange={v => { invalidate(); setHandover(v === true); }}/><span>已在真实资源侧轮换密码、外部账号及控制台会话，并核对实例、快照和备份数据可交接。</span></label>
      <Button variant="outline" onClick={() => void quote()}>预览服务转移</Button>
    </fieldset>
    {preview && <div className="space-y-3 rounded bg-muted/30 p-3 text-sm"><h3 className="font-semibold">确认转移范围</h3><p>客户 #{preview.quote.source_customer_id} → #{preview.quote.target_customer_id} · 归属代次 {preview.quote.owner_generation} → {preview.quote.owner_generation + 1}</p><p>续费 {money(preview.quote.renewal_amount_cents)} · 期限 {preview.quote.current_due_at ? date(preview.quote.current_due_at) : "未起算"} · 自动续费关闭</p><p className="whitespace-pre-wrap break-words">原因：{String(preview.command.body.reason)}<br/>交接：{String(preview.command.body.handover_evidence)}</p><p>有效至 {date(preview.quote.expires_at)}</p>{expired && <p role="alert" className="text-destructive">预览已过期，请重新核对。</p>}<label className="flex items-start gap-2"><Checkbox aria-label="确认本次服务转移" checked={confirmed} disabled={locked || expired} onCheckedChange={v => setConfirmed(v === true)}/><span>已核对双方客户和交接记录，确认转移此服务。</span></label><Button disabled={locked || expired || !confirmed} onClick={() => void submit()}>确认转移服务</Button></div>}
  </section>;
}
