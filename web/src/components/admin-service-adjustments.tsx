"use client";

import { useEffect, useState } from "react";
import { RefreshCw } from "lucide-react";
import { date, type Service, type User } from "@/lib/api";
import { useAdminIdentity, useAdminPermission } from "./workspace";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Checkbox } from "./ui/checkbox";
import { adjustmentPermission, serviceAdjustmentAuthority, serviceTermMicros, useServiceAdjustmentManagement, useServiceAdjustmentMutation, validServiceManagement, type AdjustmentKind, type AdjustmentQuote, type AdjustmentRequest, type ServiceManagement } from "./admin-service-adjustment-mutation";

const can = (actor: User, permission: string) => actor.role === "super_admin" || actor.role === "admin" && actor.permissions.includes(permission);
const money = (amount: number) => { const value = BigInt(amount); return `¥${value / BigInt(100)}.${String(value % BigInt(100)).padStart(2, "0")}`; };
const panel = "min-w-0 space-y-4 rounded border bg-card p-4 sm:p-5";
const selectClass = "h-10 w-full rounded border bg-background px-3 text-sm";
export function serviceAdjustmentCents(value: string) {
  if (!/^\d+(?:\.\d{1,2})?$/.test(value)) throw new Error("固定应付须为非负金额，最多两位小数。");
  const [whole, fraction = ""] = value.split("."), result = BigInt(whole) * BigInt(100) + BigInt(fraction.padEnd(2, "0"));
  if (result > BigInt(Number.MAX_SAFE_INTEGER)) throw new Error("金额超出精确整数分范围。");
  return Number(result);
}
const amountText = (cents: number) => `${Math.floor(cents / 100)}.${String(cents % 100).padStart(2, "0")}`;
const kindLabel = (kind: AdjustmentKind) => kind === "pricing" ? "长期续费价" : kind === "correction" ? "客户期限纠错" : "免费期限补偿";

export function AdminServiceAdjustments({ service, refresh }: { service: Service; refresh: () => void }) {
  const actor = useAdminIdentity(), view = useAdminPermission("services.view");
  if (!actor || !view) return <section className={panel}><h2 className="font-semibold">价格与客户期限</h2><p role="status" className="text-sm text-muted-foreground">无服务查看权限。</p></section>;
  const ownerGeneration = service.owner_generation;
  if (![service.id, service.user_id, service.version].every(value => Number.isSafeInteger(value) && value > 0) || typeof ownerGeneration !== "number" || !Number.isSafeInteger(ownerGeneration) || ownerGeneration < 0) return <section className={panel}><h2 className="font-semibold">价格与客户期限</h2><p role="alert" className="text-sm text-destructive">服务身份、版本或归属代次缺失，已阻止读取和调整，请重新读取服务。</p></section>;
  return <ServiceAdjustmentWorkspace key={`${serviceAdjustmentAuthority(actor)}:${service.id}:${service.user_id}:${ownerGeneration}`} actor={actor} service={service} ownerGeneration={ownerGeneration} refresh={refresh} />;
}
function ServiceAdjustmentWorkspace({ actor, service, ownerGeneration, refresh }: { actor: User; service: Service; ownerGeneration: number; refresh: () => void }) {
  const management = useServiceAdjustmentManagement(actor, service.id, service.user_id, ownerGeneration, service.version);
  const mutation = useServiceAdjustmentMutation(actor, service.id, service.user_id, ownerGeneration);
  const [revision, setRevision] = useState(0), [success, setSuccess] = useState("");
  const [restored, setRestored] = useState<{ latest: ServiceManagement; original: AdjustmentRequest } | null>(null);
  const current = validServiceManagement(management.data) && management.data.service_id === service.id && management.data.customer_id === service.user_id && management.data.owner_generation === ownerGeneration && management.data.service_version === service.version ? management.data : null;
  function saved() { setSuccess("原请求回执已确认；正在重新读取当前管理状态与服务。回执不代替当前价格、权益或上游状态。"); management.refresh(); refresh(); setRestored(null); setRevision(value => value + 1); }
  function reload() { setRestored(null); management.refresh(); refresh(); }
  function retry() { void mutation.run().then(result => { if (result) saved(); }); }
  const editFacts = restored?.latest ?? current;
  return <div className="min-w-0 space-y-5">
    <section className={panel}><div className="flex flex-wrap items-center justify-between gap-3"><h2 className="font-semibold">当前价格与客户期限</h2><Button type="button" variant="outline" disabled={management.isLoading} onClick={reload}><RefreshCw size={15} />刷新管理状态</Button></div>
      {management.error ? <ReadError error={management.error.message} retry={reload} /> : !management.data || management.isLoading ? <p role="status">正在读取服务管理状态…</p> : !current ? <ReadError error="管理响应与当前服务或客户不一致，请重新读取。" retry={reload} /> : <ManagementFacts facts={current} />}
      <p className="text-sm leading-6 text-muted-foreground">长期固定价为最终周期应付，不再叠加原优惠。恢复合同模式沿用原合同及剩余优惠，不按客户当前分组或商品现价重算。</p>
      <p className="text-xs leading-5 text-muted-foreground">离开本页会丢弃尚未提交的预览；已发送且结果待核验的原请求会保留，返回后请用原请求核验。</p>
    </section>
    {success && <p role="status" className="text-sm text-primary">{success}</p>}{mutation.error && <p role="alert" className="break-words text-sm text-destructive">{mutation.error}</p>}
    {mutation.pending && <section className={panel}><h2 className="font-semibold">{mutation.pending.phase === "rejected" ? "原调整请求已拒绝" : "原调整结果待核验"}</h2><p className="text-sm">{kindLabel(mutation.pending.kind)} · 服务 #{service.id} · 原客户 #{mutation.pending.baseline.customer_id} · 归属代次 {mutation.pending.baseline.owner_generation}</p><p className="break-all text-xs text-muted-foreground">请求键 {mutation.pending.key} · 提交版本 {mutation.pending.body.expected_version as number}</p>
      <p className="whitespace-pre-wrap break-words text-sm">原原因：{String(mutation.pending.body.reason)}</p>
      {mutation.pending.phase === "uncertain" ? <Button type="button" variant="outline" disabled={mutation.busy || !can(actor, adjustmentPermission(mutation.pending.kind))} onClick={retry}>使用原调整请求核验</Button> : <>
        <Button type="button" variant="outline" disabled={mutation.busy} onClick={() => void mutation.review()}>读取当前管理事实</Button>
        {mutation.reviewed && <><ManagementFacts facts={mutation.reviewed} /><Button type="button" variant="outline" disabled={mutation.busy} onClick={() => {
          if (window.confirm("已核对原请求被拒绝与当前事实，结束旧请求并重新预览？不会自动提交。")) void mutation.endRejected().then(result => { if (result) { setRestored(result); setRevision(value => value + 1); management.refresh(); refresh(); } });
        }}>已核对，结束旧请求并重新预览</Button></>}
      </>}
    </section>}
    {editFacts && !management.error && !management.isLoading && <AdjustmentForms key={`${revision}:${editFacts.customer_id}:${editFacts.owner_generation}:${editFacts.service_version}`} actor={actor} facts={editFacts} mutation={mutation} restored={restored?.original} onSaved={saved} />}
  </div>;
}
function ManagementFacts({ facts }: { facts: ServiceManagement }) {
  const fields: [string, string][] = [
    ["客户 / 归属代次", `#${facts.customer_id} · ${facts.owner_generation}`], ["服务版本 / 账期", `${facts.service_version} · ${facts.billing_cycle}`], ["计价模式", facts.pricing_mode === "fixed" ? "长期固定最终应付" : "原合同价"], ["当前周期应付 CNY", money(facts.amount_cents)],
    ["原合同周期基础价", money(facts.contract_base_cents)], ["原合同本次减免", money(facts.contract_discount_cents)], ["原合同预计应付", money(facts.contract_amount_cents)], ["长期固定金额", facts.fixed_amount_cents === null ? "未设置" : money(facts.fixed_amount_cents)],
    ["客户权益到期", facts.next_due_at ? date(facts.next_due_at) : "未起算"], ["客户期限模式", facts.term_mode === "managed" ? "人工管理 managed" : "跟随模式 follow_supplier"], ["上游实际到期观测", facts.supplier_next_due_at ? date(facts.supplier_next_due_at) : "尚未观测"], ["上游观测时间", facts.supplier_observed_at ? date(facts.supplier_observed_at) : "尚未观测"],
  ];
  return <dl className="grid min-w-0 gap-4 text-sm sm:grid-cols-2 xl:grid-cols-3">{fields.map(([label, value]) => <div key={label} className="min-w-0"><dt className="text-muted-foreground">{label}</dt><dd className="mt-1 break-words font-medium tabular-nums">{value}</dd></div>)}</dl>;
}
function ReadError({ error, retry }: { error: string; retry: () => void }) { return <div role="alert" className="space-y-2 text-sm"><p className="text-destructive">{error}</p><Button type="button" variant="outline" onClick={retry}>重新读取本区域</Button></div>; }
function AdjustmentForms({ actor, facts, mutation, restored, onSaved }: { actor: User; facts: ServiceManagement; mutation: ReturnType<typeof useServiceAdjustmentMutation>; restored?: AdjustmentRequest; onSaved: () => void }) {
  return <div className="grid min-w-0 items-start gap-5 xl:grid-cols-2">
    <section className={panel}><h2 className="font-semibold">长期续费价</h2><p className="text-sm text-muted-foreground">只改变未来续费定价，不修改已有账单或历史金额。固定价期间不能升级商品、配置或账期，须先恢复合同模式。</p>{can(actor, "services.pricing") ? <PricingForm facts={facts} mutation={mutation} restored={restored?.kind === "pricing" ? restored : undefined} onSaved={onSaved} /> : <p role="status" className="text-sm text-muted-foreground">无长期改价权限，需要 services.pricing。</p>}</section>
    <section className={panel}><h2 className="font-semibold">客户期限纠错与免费补偿</h2><p className="text-sm text-muted-foreground">调整后进入 managed。只改变客户权益；不建单、不收款、不修改实际上游期限，也不自动恢复已暂停服务。</p>{can(actor, "services.term.correct") || can(actor, "services.term.compensate") ? <TermForm actor={actor} facts={facts} mutation={mutation} restored={restored?.kind !== "pricing" ? restored : undefined} onSaved={onSaved} /> : <p role="status" className="text-sm text-muted-foreground">无期限纠错或免费补偿权限。</p>}</section>
  </div>;
}
type SharedForm = { facts: ServiceManagement; mutation: ReturnType<typeof useServiceAdjustmentMutation>; restored?: AdjustmentRequest; onSaved: () => void };
function PricingForm({ facts, mutation, restored, onSaved }: SharedForm) {
  const [mode, setMode] = useState(restored ? String(restored.body.mode) : facts.pricing_mode), [amount, setAmount] = useState(amountText(restored?.body.fixed_amount_cents as number ?? facts.fixed_amount_cents ?? facts.amount_cents));
  const [reason, setReason] = useState(restored ? String(restored.body.reason) : ""), [quote, setQuote] = useState<AdjustmentQuote | null>(null), [input, setInput] = useState<Record<string, unknown> | null>(null), [zero, setZero] = useState(false), [error, setError] = useState("");
  const expired = usePreviewExpiry(quote);
  function change() { setQuote(null); setInput(null); setZero(false); setError(""); }
  async function preview(event: React.FormEvent) {
    event.preventDefault(); if (mutation.frozen) return;
    try {
      const normalized = reason.trim(); if (!normalized || Array.from(normalized).length > 240 || normalized.includes("\0")) throw new Error("请填写1至240字改价原因。");
      const body = { expected_version: facts.service_version, mode, ...(mode === "fixed" ? { fixed_amount_cents: serviceAdjustmentCents(amount) } : {}) };
      const result = await mutation.preview("pricing", facts, body); setQuote(result); setInput(result ? { ...body, quote_hash: result.quote_hash, reason: normalized } : null); setZero(false); setError("");
    } catch (error) { setError((error as Error).message); }
  }
  return <form onSubmit={event => void preview(event)} className="space-y-4"><fieldset disabled={mutation.frozen} className="space-y-4">
    <label className="block space-y-2 text-sm"><span>目标定价模式</span><select aria-label="目标定价模式" className={selectClass} value={mode} onChange={event => { change(); setMode(event.target.value); }}><option value="fixed">长期固定最终应付</option><option value="contract">恢复原合同模式</option></select></label>
    {mode === "fixed" && <label className="block space-y-2 text-sm"><span>固定周期应付 CNY</span><Input aria-label="固定周期应付" inputMode="decimal" value={amount} onChange={event => { change(); setAmount(event.target.value); }} /></label>}
    <label className="block space-y-2 text-sm"><span>改价原因</span><Textarea aria-label="长期改价原因" value={reason} onChange={event => { change(); setReason(event.target.value); }} /></label><Button type="submit">预览长期定价</Button>
  </fieldset>{error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    {quote && input && <section className="space-y-3 rounded border p-4 text-sm"><h3 className="font-semibold">确认长期定价变化</h3><p>客户 #{quote.customer_id} · 服务 #{quote.service_id} · 原服务版本 {quote.service_version}</p><p>当前：{money(facts.amount_cents)} · {facts.pricing_mode === "fixed" ? "固定" : "合同"} → 目标：{money(quote.amount_cents!)} · {quote.mode === "fixed" ? "固定最终应付" : "原合同"}</p><p>保留原合同：基础 {money(quote.contract_base_cents!)} − 减免 {money(quote.contract_discount_cents!)} ＝ {money(quote.contract_amount_cents!)}。</p><p>预览到期：{date(quote.expires_at)}；摘要不自动刷新。</p>
      {quote.amount_cents === 0 && <label className="flex items-start gap-2"><Checkbox checked={zero} disabled={mutation.frozen} onCheckedChange={value => setZero(value === true)} /><span>我确认未来续费最终应付为0元。</span></label>}
      {expired && <p role="alert" className="text-destructive">预览已过期，请重新预览并人工确认。</p>}
      <Button type="button" disabled={mutation.frozen || expired || quote.amount_cents === 0 && !zero} onClick={() => void mutation.run({ kind: "pricing", baseline: facts, body: input, quote }).then(result => { if (result) onSaved(); })}>确认修改长期定价</Button>
    </section>}
  </form>;
}
function TermForm({ actor, facts, mutation, restored, onSaved }: SharedForm & { actor: User }) {
  const [kind, setKind] = useState<"correction" | "compensation">(restored?.kind === "correction" || restored?.kind === "compensation" ? restored.kind : can(actor, "services.term.correct") ? "correction" : "compensation");
  const [due, setDue] = useState(restored ? String(restored.body.next_due_at) : facts.next_due_at ?? ""), [reason, setReason] = useState(restored ? String(restored.body.reason) : "");
  const [quote, setQuote] = useState<AdjustmentQuote | null>(null), [input, setInput] = useState<Record<string, unknown> | null>(null), [confirmed, setConfirmed] = useState(false), [error, setError] = useState("");
  const expired = usePreviewExpiry(quote);
  function change() { setQuote(null); setInput(null); setConfirmed(false); setError(""); }
  async function preview(event: React.FormEvent) {
    event.preventDefault(); if (mutation.frozen || !can(actor, adjustmentPermission(kind))) return;
    try {
      const normalized = reason.trim(), next = serviceTermMicros(due.trim()), before = serviceTermMicros(facts.next_due_at);
      if (!normalized || Array.from(normalized).length > 500 || normalized.includes("\0")) throw new Error("请填写1至500字调整原因。");
      if (next === null) throw new Error("请填写2000至9999年的RFC3339时间，含时区且最多6位小数。");
      if (before === null || next === before || kind === "compensation" && next <= before) throw new Error("纠错期限必须不同于原期限，免费补偿必须严格延长。");
      const body = { expected_version: facts.service_version, kind, next_due_at: due.trim(), reason: normalized };
      const result = await mutation.preview(kind, facts, body); setQuote(result); setInput(result ? { ...body, quote_hash: result.quote_hash } : null); setConfirmed(false); setError("");
    } catch (error) { setError((error as Error).message); }
  }
  return <form onSubmit={event => void preview(event)} className="space-y-4"><fieldset disabled={mutation.frozen} className="space-y-4">
    <label className="block space-y-2 text-sm"><span>调整类型</span><select aria-label="客户期限调整类型" className={selectClass} value={kind} onChange={event => { change(); setKind(event.target.value as typeof kind); }}>{can(actor, "services.term.correct") && <option value="correction">期限纠错（可提前或延后）</option>}{can(actor, "services.term.compensate") && <option value="compensation">免费补偿（仅延后）</option>}</select></label>
    <label className="block space-y-2 text-sm"><span>目标客户期限（RFC3339，含时区）</span><Input aria-label="目标客户期限" value={due} onChange={event => { change(); setDue(event.target.value); }} placeholder="2026-12-01T08:00:00+08:00" /></label><p className="text-xs text-muted-foreground">例如 +08:00 为上海时区；支持到微秒，不按浏览器本地时区猜测。</p>
    <label className="block space-y-2 text-sm"><span>调整原因（原因绑定预览）</span><Textarea aria-label="客户期限调整原因" value={reason} onChange={event => { change(); setReason(event.target.value); }} /></label><Button type="submit">预览客户期限</Button>
  </fieldset>{error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    {quote && input && <section className="space-y-3 rounded border p-4 text-sm"><h3 className="font-semibold">确认{kindLabel(kind)}</h3><p>客户 #{quote.customer_id} · 服务 #{quote.service_id} · 版本 {quote.service_version}</p><p className="break-all">原客户期限：{quote.current_due_at ?? "未起算"}<br />目标客户期限：{quote.next_due_at}</p><p>上游到期观测：{quote.supplier_next_due_at ? date(quote.supplier_next_due_at) : "尚未观测"}；上游期限不修改。</p><p>预览到期：{date(quote.expires_at)}。成功后进入人工管理，不自动恢复暂停，不产生收入或收款。</p><label className="flex items-start gap-2"><Checkbox checked={confirmed} disabled={mutation.frozen} onCheckedChange={value => setConfirmed(value === true)} /><span>我已核对客户期限变化，确认不代表上游续费或恢复成功。</span></label>
      {expired && <p role="alert" className="text-destructive">预览已过期，请重新预览并人工确认。</p>}<Button type="button" disabled={mutation.frozen || expired || !confirmed} onClick={() => void mutation.run({ kind, baseline: facts, body: input, quote }).then(result => { if (result) onSaved(); })}>确认调整客户期限</Button>
    </section>}
  </form>;
}
function usePreviewExpiry(quote: AdjustmentQuote | null) {
  const [expiredQuote, setExpiredQuote] = useState<string | null>(null);
  useEffect(() => {
    if (!quote) return;
    const remaining = Date.parse(quote.expires_at) - Date.now();
    const timer = window.setTimeout(() => setExpiredQuote(quote.quote_hash), Math.max(0, Math.min(remaining, 2147483647)));
    return () => window.clearTimeout(timer);
  }, [quote]);
  return !!quote && expiredQuote === quote.quote_hash;
}
