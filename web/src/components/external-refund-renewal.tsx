"use client";
import { date } from "@/lib/api";
export type RenewalEffect = { id: number; service_id: number; billing_cycle: string; before_due: string; after_due: string; period_start: string; before_used: number; after_used: number };
export type RenewalReversal = { status: "pending" | "applied" | "released"; effect_id: number; service_id: number; invoice_version: number; service_version: number; before_due: string; after_due: string; before_used: number; after_used: number; review_before: string; created_at: string; applied_at: string | null; released_at: string | null };
export type RenewalContext = { invoice_id: number; invoice_version: number; eligible: boolean; reason_code: string; reason: string; effect: RenewalEffect | null; service_version: number | null; review_before: string | null; renewal_reversal: RenewalReversal | null };
const positive = (v: unknown) => Number.isSafeInteger(v) && Number(v) > 0;
const count = (v: unknown) => Number.isSafeInteger(v) && Number(v) >= 0;
const timestamp = (v: unknown) => typeof v === "string" && /^\d{4}-\d\d-\d\dT.*(?:Z|[+-]\d\d:\d\d)$/.test(v) && Number.isFinite(Date.parse(v));
export function validRenewalVersions(effect: unknown, service: unknown) { return positive(effect) && positive(service); }
export function validRenewalContext(value: RenewalContext | null, invoice: number, version: number) {
  const e = value?.effect;
  return !!value && value.invoice_id === invoice && value.invoice_version === version && typeof value.eligible === "boolean" && !!e && validRenewalVersions(e.id, value.service_version) && positive(e.service_id) && typeof e.billing_cycle === "string" && !!e.billing_cycle && timestamp(e.before_due) && timestamp(e.after_due) && timestamp(e.period_start) && count(e.before_used) && count(e.after_used) && timestamp(value.review_before);
}
export function validRenewalReversal(value: RenewalReversal | null | undefined, status: RenewalReversal["status"], service?: number) {
  return !!value && value.status === status && positive(value.effect_id) && positive(value.service_id) && (service === undefined || value.service_id === service) && positive(value.invoice_version) && positive(value.service_version) && timestamp(value.before_due) && timestamp(value.after_due) && count(value.before_used) && count(value.after_used) && timestamp(value.review_before) && timestamp(value.created_at) && (status !== "applied" || timestamp(value.applied_at)) && (status !== "released" || timestamp(value.released_at));
}
export function RenewalSnapshot({ context }: { context: RenewalContext }) {
  const effect = context.effect;
  if (!effect) return <p className="text-sm">暂无可验证的本地续期事实，不能登记续费冲正。</p>;
  return <div className="rounded border p-3 space-y-1 text-sm break-words"><p>本地续期 #{effect.id} · 服务 #{effect.service_id} · {effect.billing_cycle} · 服务版本 {context.service_version} · 账单版本 {context.invoice_version}</p><RenewalFacts value={effect} reviewBefore={context.review_before} /><p>服务保留原期限，确认后自动续费将关闭，客户余额不变。申请将冻结服务变更，须由另一员工独立复核；不会调用渠道退款。</p></div>;
}
function RenewalFacts({ value, reviewBefore }: { value: Pick<RenewalEffect, "before_due" | "after_due" | "before_used" | "after_used">; reviewBefore: string | null }) {
  return <><p>原到期：{date(value.before_due)} · 续费后到期：{date(value.after_due)}</p><p>冲正恢复后到期：{date(value.before_due)} · 优惠已用次数：{value.after_used} → {value.before_used}（恢复）</p><p>独立复核截止：{reviewBefore ? date(reviewBefore) : "未提供，请刷新核实"}；跨截止不能确认，仍可驳回释放。</p></>;
}
export function RenewalImpact({ reversal }: { reversal: RenewalReversal }) {
  return <div role="status" className="rounded border p-3 space-y-1 text-sm break-words"><p>续期 #{reversal.effect_id} · 服务 #{reversal.service_id} · {reversal.status === "pending" ? "待独立复核，服务变更已冻结" : reversal.status === "applied" ? "已精确冲正，服务冻结已解除" : "已驳回，服务冻结已释放"}</p><RenewalFacts value={reversal} reviewBefore={reversal.review_before} /><p>{reversal.status === "applied" ? "服务已恢复原期限和优惠次数，自动续费已关闭。" : reversal.status === "released" ? "期限、优惠次数及自动续费未因驳回改变。" : "确认将恢复原期限与优惠次数并关闭自动续费；关闭弹窗或刷新不会解除冻结。"}客户余额不变，原收款保留。</p>{reversal.applied_at && <p>冲正时间：{date(reversal.applied_at)}</p>}{reversal.released_at && <p>释放时间：{date(reversal.released_at)}</p>}</div>;
}
