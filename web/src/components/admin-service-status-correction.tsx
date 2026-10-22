"use client";

import { useEffect, useRef, useState } from "react";
import { date, type Service, type User } from "@/lib/api";
import { can } from "@/lib/admin-navigation";
import { useAdminIdentity } from "./workspace";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Checkbox } from "./ui/checkbox";
import { serviceAdjustmentAuthority } from "./admin-service-adjustment-mutation";
import { latestLifecycleJob, statusSourceAction, useServiceStatusCorrection, type StatusFacts, type StatusRequest } from "./admin-service-status-mutation";

const panel = "min-w-0 space-y-4 rounded border bg-card p-4 sm:p-5";
const statuses: Record<string, string> = { active: "运行中", suspended: "已暂停", terminated: "已终止", cancelled: "已取消" };
const actions: Record<string, string> = { create: "开通", suspend: "暂停", resume: "恢复", terminate: "终止" };
export function AdminServiceStatusCorrection({ service, refresh }: { service: Service; refresh: () => void }) {
  const actor = useAdminIdentity();
  if (!actor || !["services.view", "services.status.correct"].every(p => can(actor, p))) return <section className={panel}><h2 className="font-semibold">受限本地状态纠错</h2><p role="status" className="text-sm text-muted-foreground">需要服务查看和独立状态纠错权限；服务管理权限不包含此操作。</p></section>;
  const owner = service.owner_generation;
  if (![service.id, service.user_id, service.version].every(v => Number.isSafeInteger(v) && v > 0) || typeof owner !== "number" || !Number.isSafeInteger(owner) || owner < 0) return <p role="alert" className="text-destructive">服务身份或归属代次缺失，请重新读取服务。</p>;
  // Keep recovery mounted even if a later lifecycle operation changes status/mode.
  return <StatusWorkspace key={`${serviceAdjustmentAuthority(actor)}:${service.id}:${service.user_id}:${owner}`} actor={actor} service={service} owner={owner} refresh={refresh}/>;
}
function StatusWorkspace({ actor, service, owner, refresh }: { actor: User; service: Service; owner: number; refresh: () => void }) {
  const mutation = useServiceStatusCorrection(actor, service, owner);
  const [baseline, setBaseline] = useState<StatusFacts | null>(null), [source, setSource] = useState("");
  const [reason, setReason] = useState(""), [evidence, setEvidence] = useState(""), [request, setRequest] = useState<StatusRequest | null>(null);
  const [confirmed, setConfirmed] = useState(false), [expiredHash, setExpiredHash] = useState(""), [error, setError] = useState("");
  const revision = useRef(0);
  useEffect(() => {
    if (!request) return;
    const timer = window.setTimeout(() => setExpiredHash(request.quote.quote_hash), Math.max(0, Date.parse(request.quote.expires_at) - Date.now()));
    return () => window.clearTimeout(timer);
  }, [request]);
  const expired = !!request && expiredHash === request.quote.quote_hash;
  function invalidate() { revision.current++; setRequest(null); setConfirmed(false); setError(""); }
  function selectFacts(value: StatusFacts) {
    invalidate(); setBaseline(value); const job = latestLifecycleJob(value.jobs), target = value.management.status === "active" ? "suspended" : "active";
    setSource(job?.status === "succeeded" && job.action === statusSourceAction(target) && job.completed_at ? String(job.id) : "");
  }
  async function read() { invalidate(); setBaseline(null); const value = await mutation.read(); if (value) selectFacts(value); }
  async function preview() {
    invalidate(); if (!baseline) return;
    if (!/^[1-9]\d*$/.test(source) || !Number.isSafeInteger(Number(source))) { setError("请输入已核对的来源任务 ID。"); return; }
    const token = revision.current, value = await mutation.preview(baseline, Number(source), reason, evidence); if (token === revision.current) setRequest(value);
  }
  async function submit() {
    if (!request || !confirmed || expired || mutation.frozen) return;
    const result = await mutation.run(request); if (result) { selectFacts(result.facts); refresh(); }
  }
  async function recover() { const result = await mutation.run(); if (result) { selectFacts(result.facts); refresh(); } }
  async function finishRejected() { const value = await mutation.finishRejected(); if (value) { selectFacts(value); refresh(); } }
  const current = mutation.facts, pending = mutation.pending, receipt = mutation.receipt;
  const eligible = baseline && ["manual", "simulation"].includes(baseline.mode) && ["active", "suspended"].includes(baseline.management.status);
  const latest = baseline ? latestLifecycleJob(baseline.jobs) : null;
  return <section className={panel} aria-label="受限本地状态纠错">
    <header className="space-y-1"><h2 className="text-base font-semibold">受限本地状态纠错</h2><p className="text-sm text-muted-foreground">客户 #{service.user_id} · 服务 #{service.id}。仅根据已完成的人工/模拟暂停或恢复任务修正本地记录，不执行资源动作。</p></header>
    {(error || mutation.error) && <p role="alert" className="break-words text-sm text-destructive">{error || mutation.error}</p>}
    {pending && <div role="status" className="space-y-3 rounded border p-3 text-sm"><p>原状态纠错：{statuses[pending.quote.current_status]} → {statuses[pending.body.target_status]} · 来源任务 #{pending.body.source_job_id}</p><p className="text-muted-foreground">{pending.phase === "rejected" ? "原请求明确拒绝，重新预览前先核对当前事实。" : "结果待核验，已保存原请求与请求键。仅可原样恢复，勿清除浏览器资料后重复修改。"}</p><Button variant="outline" disabled={mutation.busy || mutation.blocked} onClick={() => void (pending.phase === "rejected" ? finishRejected() : recover())}>{pending.phase === "rejected" ? "核对事实并结束被拒纠错" : "恢复原状态纠错请求"}</Button></div>}
    {receipt && <div role="status" className="space-y-2 rounded bg-muted/40 p-3 text-sm"><p>{receipt.replayed ? "原状态纠错历史回执已恢复" : "状态纠错已核验"}：{statuses[receipt.before_status]} → {statuses[receipt.status]} · v{receipt.service_version} · 任务 #{receipt.source_job_id}</p><p className="text-muted-foreground">回执记录原次修改结果；以下当前事实为另行读取。不派发外部操作，不重复发送生命周期通知。</p></div>}
    {current && <p className="text-sm text-muted-foreground">当前状态：{statuses[current.management.status] ?? current.management.status} · v{current.management.service_version} · {current.mode === "manual" ? "人工交付" : current.mode === "simulation" ? "模拟交付" : current.mode} · 当前任务 {current.jobs.length} 条（最多显示最近50条）</p>}
    <Button variant="outline" disabled={mutation.frozen} onClick={() => void read()}>读取当前状态与任务</Button>
    {baseline && !eligible && <p role="status" className="text-sm text-muted-foreground">当前模式或状态不支持新纠错。仅人工/模拟服务可在运行与暂停之间校正；供应商和NOKVM须走原生命周期流程，不允许终态复活。</p>}
    {eligible && <div className="space-y-4 text-sm">
      <div className="space-y-2 rounded bg-muted/30 p-3"><p>本地记录：{statuses[baseline.management.status]} → {statuses[baseline.management.status === "active" ? "suspended" : "active"]}</p><p className="break-words text-muted-foreground">{latest ? `最近可见生命周期尝试：#${latest.id} · ${actions[latest.action] ?? latest.action} · ${latest.status}${latest.completed_at ? ` · ${date(latest.completed_at)}` : ""}` : "最近50条任务中未找到生命周期尝试，请从任务记录核对来源。"}</p><p className="text-muted-foreground">来源必须是最新且成功完成的对应暂停/恢复任务。更新的失败或取消尝试也会阻断旧成功任务，原完成审计由服务器核验；仅填写说明不能代替完成证据。</p></div>
      <fieldset disabled={mutation.frozen} className="grid min-w-0 gap-4 md:grid-cols-2">
        <label className="min-w-0 space-y-2 md:col-span-2"><span>纠错来源任务 ID</span><Input aria-label="纠错来源任务 ID" inputMode="numeric" value={source} onChange={e => { invalidate(); setSource(e.target.value); }}/></label>
        <label className="min-w-0 space-y-2"><span>状态纠错原因</span><Textarea aria-label="状态纠错原因" value={reason} maxLength={500} onChange={e => { invalidate(); setReason(e.target.value); }}/></label>
        <label className="min-w-0 space-y-2"><span>完成凭证及核对说明</span><Textarea aria-label="完成凭证及核对说明" value={evidence} maxLength={2000} placeholder="原任务完成记录、工单编号等非秘密依据" onChange={e => { invalidate(); setEvidence(e.target.value); }}/></label>
      </fieldset>
      <Button variant="outline" disabled={mutation.frozen} onClick={() => void preview()}>预览状态纠错</Button>
    </div>}
    {request && <div className="space-y-3 rounded bg-muted/30 p-3 text-sm"><h3 className="font-semibold">核对本地状态变化</h3><p>服务 #{request.quote.service_id} · 客户 #{request.quote.customer_id} · v{request.quote.service_version}</p><p>{statuses[request.quote.current_status]} → {statuses[request.quote.target_status]} · {request.quote.provision_mode === "manual" ? "人工交付" : "模拟交付"}</p><p>来源 #{request.quote.source_job_id}（{actions[request.quote.source_action]}）· 完成 {date(request.quote.source_completed_at)}</p><p className="whitespace-pre-wrap break-words">原因：{request.body.reason}</p><p className="whitespace-pre-wrap break-words">凭据：{request.body.evidence}</p><p className="text-muted-foreground">有效至 {date(request.quote.expires_at)}。仅修正本地状态和版本，不开关机、不改价格期限或资金、不创建任务或重复发送通知。</p>{expired && <p role="alert" className="text-destructive">状态纠错预览已过期，请重新预览。</p>}<label className="flex items-start gap-2"><Checkbox aria-label="确认仅修正本地状态" checked={confirmed} disabled={mutation.frozen || expired} onCheckedChange={v => setConfirmed(v === true)}/><span>已核对原任务完成事实，确认仅修正此服务的本地记录。</span></label><Button disabled={mutation.frozen || !confirmed || expired} onClick={() => void submit()}>确认执行状态纠错</Button></div>}
  </section>;
}
