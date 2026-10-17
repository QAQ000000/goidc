"use client";
import { useCallback, useEffect, useState } from "react";
import { api, date, money } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { useDurableAdminMutation, type DurableRequest } from "@/hooks/use-durable-admin-mutation";
import { useAdminIdentity, useAdminPermission } from "./workspace";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Textarea } from "./ui/textarea";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "./ui/dialog";
import { DurableRecovery, ExceptionPager, exactFields, positiveInteger, useExceptionQuery, validNote } from "./payment-exception-common";
import type { ReadFactsResult } from "@/hooks/use-durable-admin-mutation";
import type { PaymentRecord } from "./online-payment";
import type { CloseResolution, CloseVerification, CloseVerificationPage, CloseVerificationReceipt } from "./payment-exception-types";

const decisions: Record<string, string> = { applied_closed: "已独立确认最终关闭", applied_paid: "已独立确认可信收款并结算", already_paid: "已存在可信收款，保留原结算", superseded_paid: "已由可信收款处理，未关闭支付", rejected: "核验申请已驳回" };
const results: Record<string, string> = { closed: "渠道证明最终关闭", succeeded: "渠道证明已收款", unknown: "可信结果仍未知，继续冻结", unsupported: "原渠道不支持关闭状态核验", error: "本次未取得可信结果，继续冻结" };
const refusals = new Set(["stale_invoice", "idempotency_conflict", "close_verification_expired", "close_verification_ineligible", "close_verification_in_progress", "close_verification_self_review", "close_verification_reviewed", "close_verification_unverified", "close_verification_resolved", "invalid_close_verification_review"]);
const validReviewNote = (value: unknown) => validNote(value) && Array.from((value as string).trim()).length >= 3;
async function sha256Hex(value: string) {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(value));
  return Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, "0")).join("");
}
export function validCloseResolution(resolution: CloseResolution | null | undefined, paymentID: number, operation: string, status: string, version: number) {
  // paymentID is checked on the parent receipt; the immutable original remote
  // operation and decision linkage must also match before archiving a request.
  return positiveInteger(paymentID) && !!resolution && positiveInteger(resolution.verification_id) && positiveInteger(resolution.decision_id) && positiveInteger(resolution.close_operation_id) && resolution.operation_id === operation && resolution.version === version && ((status === "closed" && resolution.payment_status === "closed" && resolution.status === "applied_closed") || (status === "succeeded" && resolution.payment_status === "succeeded" && ["applied_paid", "already_paid", "superseded_paid"].includes(resolution.status) && typeof resolution.external_transaction_id === "string" && resolution.external_transaction_id.length > 0));
}
export function PaymentCloseVerifications({ invoiceId, payments, refresh, revision }: { invoiceId: number; payments: PaymentRecord[]; refresh: () => void; revision: number }) {
  const actor = useAdminIdentity();
  const verify = useAdminPermission("payments.close_verify");
  const review = useAdminPermission("payments.close_review");
  const { params, update } = useExceptionQuery();
  const raw = Number(params.get("exception_payment"));
  const selected = payments.find(payment => payment.id === raw) ?? payments.find(payment => payment.close_status || payment.status === "closed") ?? payments[0];
  return <section aria-labelledby="close-verification-title" className="space-y-3 min-w-0"><h3 id="close-verification-title" className="font-semibold">关闭状态核验</h3><p className="text-sm text-muted-foreground">读取原渠道对原关单操作的可信状态，不会再次发送关单。查询结果先保存，其他员工独立确认后才可能解除保护；本地过期、未付款或人工勾选均不能证明关闭。</p>{!selected ? <p className="text-sm text-muted-foreground">暂无在线支付记录。</p> : <><label className="grid gap-2 text-sm min-w-0">原支付<select aria-label="选择核验原支付" className="w-full max-w-full min-w-0 rounded border bg-background p-2" value={selected.id} onChange={event => update({ exception_payment: Number(event.target.value), verification_offset: null })}>{payments.map(payment => <option key={payment.id} value={payment.id}>#{payment.id} · {payment.provider} · {money(payment.amount_cents)}</option>)}</select></label><VerificationPanel key={`${actor?.id ?? 0}:${verify}:${review}:${invoiceId}:${selected.id}`} actor={actor?.id ?? 0} verify={verify} review={review} invoiceId={invoiceId} payment={selected} refresh={refresh} revision={revision} /></>}</section>;
}
function VerificationPanel({ actor, verify, review, invoiceId, payment, refresh, revision }: { actor: number; verify: boolean; review: boolean; invoiceId: number; payment: PaymentRecord; refresh: () => void; revision: number }) {
  const { offset: getOffset, update } = useExceptionQuery();
  const offset = getOffset("verification_offset");
  const path = `/admin/invoices/${invoiceId}/payments/${payment.id}/close-verifications`;
  const resource = useResource<CloseVerificationPage>(actor ? `${path}?limit=10&offset=${offset}` : null, false);
  const refreshResource = resource.refresh;
  useEffect(() => { refreshResource(); }, [revision, refreshResource]);
  const [notice, setNotice] = useState("");
  const [draft, setDraft] = useState<{ kind: "lookup" | "confirm" | "reject"; target: number; version: number; record?: CloseVerification } | null>(null);
  const [note, setNote] = useState("");
  const validate = useCallback((request: DurableRequest) => {
    try {
      const body = JSON.parse(request.body) as Record<string, unknown>;
      if (!body || !positiveInteger(body.expected_version) || !positiveInteger(request.target)) return false;
      if (request.kind === "lookup") return request.path === path && exactFields(body, ["close_operation_id", "expected_version"]) && body.close_operation_id === request.target;
      return ["confirm", "reject"].includes(request.kind) && request.path === `${path}/${request.target}/${request.kind}` && exactFields(body, ["expected_version", "note"]) && validReviewNote(body.note);
    } catch { return false; }
  }, [path]);
  const mutation = useDurableAdminMutation<CloseVerificationReceipt>({
    actor, scope: `close-verification:${invoiceId}:${payment.id}`, validate,
    permission: request => request.kind === "lookup" ? "payments.close_verify" : "payments.close_review",
    rejectionCodes: refusals,
    receipt: (receipt, request) => {
      const record = receipt?.verification;
      const body = JSON.parse(request.body) as { expected_version: number; note?: string };
      if (!record || record.invoice_id !== invoiceId || record.payment_id !== payment.id || record.amount_cents !== payment.amount_cents || !positiveInteger(record.id) || !positiveInteger(record.close_operation_id) || !record.operation_id || !positiveInteger(record.version) || record.version < body.expected_version) return false;
      if (request.kind === "lookup") return record.requested_by === actor && record.close_operation_id === request.target && record.expected_version === body.expected_version && ["closed", "succeeded", "unknown", "unsupported", "error"].includes(record.result_status) && record.status !== "checking";
      if (record.id !== request.target || record.reviewed_by !== actor || record.review_note !== body.note) return false;
      if (request.kind === "reject") return record.review_status === "rejected";
      return !!receipt.resolution && receipt.resolution.verification_id === record.id && receipt.resolution.close_operation_id === record.close_operation_id && validCloseResolution(receipt.resolution, record.payment_id, record.operation_id, record.payment_status, record.version) && record.review_status === receipt.resolution.status;
    },
    readFacts: async (request: DurableRequest) => {
      let current: CloseVerificationPage | null = null;
      let record: CloseVerification | undefined;
      const requestKeyHash = request.kind === "lookup" ? await sha256Hex(request.key) : "";
      for (let skip = 0; ; skip += 100) {
        current = await api<CloseVerificationPage>(`${path}?limit=100&offset=${skip}`);
        if (current.invoice_id !== invoiceId || current.payment_id !== payment.id || !Array.isArray(current.items)) throw new Error("核验案件关联不完整。");
        // One Close can have many lookups. Keep paging until the original
        // employee's exact key is found; a newer lookup cannot stand in for it.
        record = current.items.find(row => request.kind === "lookup" ? row.close_operation_id === request.target && row.requested_by === actor && row.request_key_sha256 === requestKeyHash : row.id === request.target);
        if (record || skip + current.items.length >= current.total || current.items.length === 0) break;
      }
      if (request.kind !== "lookup" && !record) throw new Error("未读到原核验记录，不能结束原请求。");
      let expiredLookup = false;
      if (request.kind === "lookup") {
        const body = JSON.parse(request.body) as { close_operation_id: number; expected_version: number };
        const operation = current?.operations.find(item => item.id === request.target);
        expiredLookup = !!record && !!operation && request.rejected && record.requested_by === actor && record.invoice_id === invoiceId && record.payment_id === payment.id && record.close_operation_id === request.target && record.operation_id === operation.operation_id && record.amount_cents === payment.amount_cents && record.expected_version === body.expected_version && record.request_key_sha256 === requestKeyHash && record.status === "expired" && record.result_status === "" && Number.isFinite(Date.parse(record.lease_until)) && Date.parse(record.lease_until) <= Date.now();
      }
      resource.refresh(); refresh();
      const text = `最新账单版本 ${current!.version}${record ? `；核验 #${record.id}：${decisions[record.review_status] ?? results[record.result_status] ?? (record.status === "expired" ? "只读查询租约已过期，未取得可信结果" : "核验在途")}` : "；原关单操作尚无已保存核验"}。原请求版本及载荷未改变。`;
      const result: ReadFactsResult = request.kind === "lookup" ? { text, canEndRejected: expiredLookup } : text;
      return result;
    },
    onSuccess: (receipt, request) => {
      setDraft(null); setNote("");
      setNotice(request.kind === "lookup" ? ["unknown", "unsupported", "error"].includes(receipt.verification.result_status) ? "本次只读查询记录已保存，请求结果已确认；业务仍未解除冻结。" : "可信结果已保存，等待符合独立条件的其他员工复核，尚未因此解锁。" : request.kind === "reject" ? "核验申请已驳回，原业务保护继续保留。" : "独立确认回执已核实。是否可付款或取消，以账单最新有效状态及其他未解决责任为准。");
      resource.refresh(); refresh();
    },
  });
  const data = resource.data;
  const blocked = mutation.busy || !!mutation.pending || !mutation.ready || !!mutation.storageError;
  function cancel() {
    if (mutation.busy) return;
    if (!mutation.pending && note && !window.confirm("放弃尚未提交的复核说明？")) return;
    setDraft(null); setNote("");
  }
  function submit() {
    if (!draft || blocked || (draft.kind !== "lookup" && !validReviewNote(note))) return;
    if (!window.confirm(draft.kind === "lookup" ? "确认向原渠道只读核验原操作？不会再次发送关单，未知结果仍冻结。" : draft.kind === "confirm" ? "确认已独立核实可信渠道证据？服务端将原子应用可信结果；不会采纳手工指定状态。" : "确认驳回此核验申请？原保护继续保留。")) return;
    void mutation.execute({ path: draft.kind === "lookup" ? path : `${path}/${draft.target}/${draft.kind}`, body: draft.kind === "lookup" ? { close_operation_id: draft.target, expected_version: draft.version } : { expected_version: draft.version, note: note.trim() }, kind: draft.kind, target: draft.target });
  }
  return <div className="space-y-3 min-w-0"><DurableRecovery mutation={mutation} />{notice && <p role="status" className="text-sm">{notice}</p>}<Button size="sm" variant="outline" disabled={mutation.busy} onClick={() => { resource.refresh(); refresh(); }}>读取已保存核验事实</Button>{resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !data ? <Loading /> : <>
    {!data.lookup_supported && <p className="text-sm rounded border p-3">原渠道不支持可信关闭状态核验。不会改用当前商户凭据、本地未付状态或人工证据解除冻结。</p>}
    {data.effective_resolution && <p role="status" className="text-sm rounded border p-3">有效处理：{decisions[data.effective_resolution.status]} · 核验 #{data.effective_resolution.verification_id} · 当前版本 {data.version}。原关单历史完整保留，其他未解决责任仍可能继续冻结。</p>}
    {data.operations.length === 0 && <p className="text-sm text-muted-foreground">暂无可关联的原关单操作。</p>}
    {data.operations.map(operation => <article key={operation.id} className="rounded border p-3 space-y-2 text-sm min-w-0 break-words"><p>原关单记录 #{operation.id} · 历史状态 {operation.status}</p><p className="break-all">原操作号：{operation.operation_id}</p>{verify && data.lookup_supported && data.can_request && operation.can_verify && <Button size="sm" disabled={blocked} onClick={() => { setDraft({ kind: "lookup", target: operation.id, version: data.version }); setNote(""); }}>向原渠道核验关闭状态</Button>}</article>)}
    {!data.items.length && <p className="text-sm text-muted-foreground">暂无已保存关闭状态核验。</p>}
    {data.items.map(record => <article key={record.id} className="rounded border p-3 space-y-2 text-sm min-w-0 break-words"><p className="font-medium">核验 #{record.id} · {decisions[record.review_status] ?? results[record.result_status] ?? (record.status === "expired" ? "查询租约已过期，未取得可信结果" : "只读查询在途")}</p><p>原关单记录 #{record.close_operation_id} · 原执行员工 #{record.original_actor_id}</p><p>申请员工 #{record.requested_by} · {date(record.requested_at)} · 当前版本 {record.version}</p>{record.external_transaction_id && <p className="break-all">可信渠道流水：{record.external_transaction_id}</p>}{record.provider_evidence_id && <p className="break-all">渠道证据号：{record.provider_evidence_id}</p>}{record.result_code && <p>结果码：{record.result_code}</p>}{record.reviewed_by && <p>独立复核员工 #{record.reviewed_by} · {date(record.reviewed_at ?? "")} · {record.review_note}</p>}{review && (record.can_confirm || record.can_reject) && <div className="flex flex-wrap gap-2">{record.can_confirm && <Button size="sm" disabled={blocked} onClick={() => { setDraft({ kind: "confirm", target: record.id, version: data.version, record }); setNote(""); }}>独立确认可信结果</Button>}{record.can_reject && <Button size="sm" variant="outline" disabled={blocked} onClick={() => { setDraft({ kind: "reject", target: record.id, version: data.version, record }); setNote(""); }}>驳回核验申请</Button>}</div>}{!record.review_status && (record.requested_by === actor || record.original_actor_id === actor) && <p className="text-muted-foreground">核验发起人和原关单员工均不得独立确认本人关联操作。</p>}</article>)}
    <ExceptionPager total={data.total} offset={offset} count={data.items.length} busy={mutation.busy} change={value => update({ verification_offset: value })} />
  </>}
  <Dialog open={!!draft} onOpenChange={value => { if (!value) cancel(); }}><DialogContent showCloseButton={!mutation.busy} className="min-w-0 max-h-[85dvh] overflow-y-auto"><DialogHeader><DialogTitle>{draft?.kind === "lookup" ? "向原渠道核验关闭状态" : draft?.kind === "confirm" ? "独立确认可信核验结果" : "驳回核验申请"}</DialogTitle><DialogDescription>仅以原渠道可信证据处理；没有手工指定已关闭或已付款的选项。原状态未知不会因读取、勾选或本地过期而解除保护。</DialogDescription></DialogHeader><p className="text-sm">原支付 #{payment.id} · {money(payment.amount_cents)} · 本次账单版本 {draft?.version}</p>{draft?.record && <div className="text-sm space-y-2 break-all"><p>核验 #{draft.record.id} · 原关单 #{draft.record.close_operation_id}</p><p>{results[draft.record.result_status] ?? "未取得可信终态"}</p><p>原操作号：{draft.record.operation_id}</p>{draft.record.external_transaction_id && <p>可信流水：{draft.record.external_transaction_id}</p>}</div>}{draft?.kind !== "lookup" && <label className="grid gap-2 text-sm">独立复核说明（至少 3 字）<Textarea aria-label="独立复核说明" maxLength={1000} value={note} disabled={blocked} onChange={event => setNote(event.target.value)} /></label>}{mutation.error && <p role="alert" className="text-sm text-destructive">{mutation.error}</p>}<DialogFooter className="flex-wrap"><Button variant="outline" disabled={mutation.busy} onClick={cancel}>{mutation.pending ? "关闭，保留原请求" : "取消未提交操作"}</Button><Button disabled={blocked || (draft?.kind !== "lookup" && !validReviewNote(note))} onClick={submit}>{mutation.busy ? "正在提交" : draft?.kind === "lookup" ? "确认只读核验" : draft?.kind === "confirm" ? "确认应用可信结果" : "确认驳回"}</Button></DialogFooter></DialogContent></Dialog>
  </div>;
}
