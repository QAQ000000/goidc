"use client";
import { useCallback, useEffect, useState } from "react";
import { api, date, money } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { useDurableAdminMutation, type DurableRequest } from "@/hooks/use-durable-admin-mutation";
import { useAdminIdentity, useAdminPermission } from "./workspace";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "./ui/dialog";
import { DurableRecovery, ExceptionPager, exactFields, positiveInteger, useExceptionQuery, validNote } from "./payment-exception-common";
import type { AnomalyGroup, AnomalyReceipt, AnomalyRefund, ExceptionPage } from "./payment-exception-types";

const labels = { pending_review: "待其他员工独立复核", confirmed: "已确认外部退回", rejected: "已驳回退回登记" };
const methods: Record<string, string> = { original_route: "原路退回", bank_transfer: "人工银行转账", other: "其他人工退付" };
const refusals = new Set(["invalid_payment_anomaly_refund", "invalid_payment_anomaly_review", "payment_anomaly_group_changed", "idempotency_conflict", "payment_anomaly_identity_unverified", "payment_anomaly_transaction_occupied", "payment_anomaly_refund_exists", "version_conflict", "payment_anomaly_refund_final", "external_refund_duplicate"]);
const blank = () => ({ method: "original_route", provider_refund_id: "", received_at: "", evidence_ref: "", evidence_sha256: "", note: "" });
export function PaymentAnomalyGroups({ invoiceId, refresh, revision }: { invoiceId: number; refresh: () => void; revision: number }) {
  const actor = useAdminIdentity();
  const create = useAdminPermission("payment_anomalies.create");
  const review = useAdminPermission("payment_anomalies.review");
  return <GroupsPanel key={`${actor?.id ?? 0}:${create}:${review}:${invoiceId}`} actor={actor?.id ?? 0} create={create} review={review} invoiceId={invoiceId} refresh={refresh} revision={revision} />;
}
function GroupsPanel({ actor, create, review, invoiceId, refresh, revision }: { actor: number; create: boolean; review: boolean; invoiceId: number; refresh: () => void; revision: number }) {
  const { params, offset: getOffset, update } = useExceptionQuery();
  const offset = getOffset("anomaly_offset");
  const base = `/admin/invoices/${invoiceId}`;
  const groupsPath = `${base}/payment-anomaly-groups`;
  const resource = useResource<ExceptionPage<AnomalyGroup>>(`${groupsPath}?limit=10&offset=${offset}`, false);
  const refreshResource = resource.refresh;
  useEffect(() => { refreshResource(); }, [revision, refreshResource]);
  const [draft, setDraft] = useState<{ kind: "create" | "confirm" | "reject"; group: AnomalyGroup; refund?: AnomalyRefund } | null>(null);
  const [fields, setFields] = useState(blank);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const validate = useCallback((request: DurableRequest) => {
    try {
      const body = JSON.parse(request.body) as Record<string, unknown>;
      if (!body || !positiveInteger(request.target) || !positiveInteger(body.expected_group_version) || !validNote(body.note)) return false;
      if (request.kind === "create") return request.path === `${groupsPath}/${request.target}/refunds` && exactFields(body, ["method", "provider_refund_id", "received_at", "evidence_ref", "evidence_sha256", "note", "expected_group_version"]) && typeof body.method === "string" && Object.hasOwn(methods, body.method) && typeof body.provider_refund_id === "string" && !!body.provider_refund_id.trim() && typeof body.evidence_ref === "string" && !!body.evidence_ref.trim() && typeof body.evidence_sha256 === "string" && /^[a-f0-9]{64}$/.test(body.evidence_sha256) && typeof body.received_at === "string" && Number.isFinite(Date.parse(body.received_at));
      return ["confirm", "reject"].includes(request.kind) && request.path === `${base}/payment-anomaly-refunds/${request.target}/${request.kind}` && exactFields(body, ["expected_version", "expected_group_version", "note"]) && positiveInteger(body.expected_version);
    } catch { return false; }
  }, [base, groupsPath]);
  const mutation = useDurableAdminMutation<AnomalyReceipt>({
    actor, scope: `anomaly-refund:${invoiceId}`, readPermission: "payment_anomalies.view", validate,
    permission: request => request.kind === "create" ? "payment_anomalies.create" : "payment_anomalies.review",
    rejectionCodes: refusals,
    receipt: (receipt, request) => {
      const refund = receipt?.refund, group = receipt?.group;
      const body = JSON.parse(request.body) as ReturnType<typeof blank> & { expected_version?: number; expected_group_version: number };
      if (!refund || !group || refund.invoice_id !== invoiceId || group.invoice_id !== invoiceId || !positiveInteger(refund.id) || !positiveInteger(refund.payment_id) || !positiveInteger(refund.anchor_anomaly_id) || refund.anchor_anomaly_id !== group.anchor_anomaly_id || refund.payment_id !== group.payment_id || !positiveInteger(refund.amount_cents) || refund.amount_cents !== group.amount_cents || refund.external_transaction_id !== group.external_transaction_id || typeof receipt.invoice_has_unresolved !== "boolean" || !positiveInteger(group.group_version) || group.group_version < body.expected_group_version) return false;
      if (request.kind === "create") return group.merchant_verified && group.anchor_anomaly_id === request.target && refund.requested_by === actor && refund.status === "pending_review" && refund.version === 1 && refund.expected_group_version === body.expected_group_version && refund.method === body.method && refund.provider_refund_id === body.provider_refund_id && Date.parse(refund.received_at) === Date.parse(body.received_at) && refund.evidence_ref === body.evidence_ref && refund.evidence_sha256 === body.evidence_sha256 && refund.note === body.note;
      return refund.id === request.target && refund.reviewed_by === actor && refund.review_note === body.note && refund.status === (request.kind === "confirm" ? "confirmed" : "rejected") && refund.version === body.expected_version! + 1 && refund.reviewed_group_version === body.expected_group_version && (request.kind !== "confirm" || (group.merchant_verified && group.unresolved === false));
    },
    readFacts: async request => {
      let found: AnomalyGroup | undefined;
      for (let skip = 0; ; skip += 100) {
        const current = await api<ExceptionPage<AnomalyGroup>>(`${groupsPath}?limit=100&offset=${skip}`);
        if (!Array.isArray(current.items)) throw new Error("责任组数据不完整。");
        found = current.items.find(group => request.kind === "create" ? group.anchor_anomaly_id === request.target : group.refunds.some(refund => refund.id === request.target));
        if (found || skip + current.items.length >= current.total || current.items.length === 0) break;
      }
      if (!found || found.invoice_id !== invoiceId) throw new Error("未读到原责任组及申请，不能结束原请求。");
      resource.refresh(); refresh();
      return `责任组 #${found.anchor_anomaly_id}，最新组版本 ${found.group_version}；${found.unresolved ? "仍有待解决责任" : "该组已解决"}。原载荷版本未改变。新事件导致冲突时，结束被拒绝请求后重新核对原待审申请，无须驳回重建。`;
    },
    onSuccess: (receipt, request) => {
      setDraft(null); setFields(blank()); setError("");
      setNotice(request.kind === "create" ? "退回证据登记已保存，等待其他员工独立核实；尚未解除责任保护，本操作未执行出款。" : request.kind === "reject" ? "退回证据登记已驳回，责任及原历史仍保留。" : `此责任组退回事实已独立确认。${receipt.invoice_has_unresolved ? "账单仍有其他未解决责任，保护继续保留。" : "本次回执未发现其他未解决迟到收款组；关单未知等其他保护仍以最新账单为准。"}未改变原账单、正常支付、余额或服务。`);
      resource.refresh(); refresh();
    },
  });
  const blocked = mutation.busy || !!mutation.pending || !mutation.ready || !!mutation.storageError;
  function start(kind: "create" | "confirm" | "reject", group: AnomalyGroup, refund?: AnomalyRefund) {
    setDraft({ kind, group, refund }); setFields(blank()); setError(""); update({ exception_group: group.anchor_anomaly_id });
  }
  function cancel() {
    if (mutation.busy) return;
    const dirty = Object.entries(fields).some(([key, value]) => key === "method" ? value !== "original_route" : !!value);
    if (!mutation.pending && dirty && !window.confirm("放弃尚未提交的退回证据或复核说明？")) return;
    setDraft(null); setFields(blank()); setError("");
  }
  function submit() {
    if (!draft || blocked) return;
    const received = new Date(fields.received_at);
    if (!validNote(fields.note) || (draft.kind === "create" && (!fields.provider_refund_id.trim() || !fields.evidence_ref.trim() || !/^[a-fA-F0-9]{64}$/.test(fields.evidence_sha256) || !Number.isFinite(received.getTime()) || received.getTime() > Date.now()))) { setError("请填写说明、有效实际到账时间、退款流水、受控凭证引用和 64 位 SHA-256。到账时间不可晚于现在。"); return; }
    if (!window.confirm(draft.kind === "create" ? `确认登记该笔异常责任资金已在外部退回 ${money(draft.group.amount_cents)} 的证据？本系统不执行退款，需其他员工独立复核。` : draft.kind === "confirm" ? "确认已核实原交易、实际退回及凭证？仅解决匹配责任组，不改变正常销售或余额，不执行转账。" : "确认驳回此退回证据登记？责任继续保留。")) return;
    void mutation.execute({ path: draft.kind === "create" ? `${groupsPath}/${draft.group.anchor_anomaly_id}/refunds` : `${base}/payment-anomaly-refunds/${draft.refund!.id}/${draft.kind}`, body: draft.kind === "create" ? { method: fields.method, provider_refund_id: fields.provider_refund_id.trim(), received_at: received.toISOString(), evidence_ref: fields.evidence_ref.trim(), evidence_sha256: fields.evidence_sha256.toLowerCase(), note: fields.note.trim(), expected_group_version: draft.group.group_version } : { expected_version: draft.refund!.version, expected_group_version: draft.group.group_version, note: fields.note.trim() }, kind: draft.kind, target: draft.kind === "create" ? draft.group.anchor_anomaly_id : draft.refund!.id });
  }
  return <section aria-labelledby="anomaly-groups-title" className="space-y-3 min-w-0"><h3 id="anomaly-groups-title" className="font-semibold">迟到收款责任组与退回证据</h3><p className="text-sm text-muted-foreground">同一原支付、原商户及精确渠道流水按责任组核验，重复通知不等于重复收款。这里只登记已在外部发生的退回，不执行出款，不增加或扣减客户余额，不改变原账单、正常支付或服务。</p><DurableRecovery mutation={mutation} />{notice && <p role="status" className="text-sm">{notice}</p>}<Button size="sm" variant="outline" disabled={mutation.busy} onClick={() => { resource.refresh(); refresh(); }}>读取已保存责任组事实</Button>{resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <>
    {!resource.data.items.length && <p className="text-sm text-muted-foreground">暂无迟到收款责任组。</p>}
    {resource.data.items.map(group => <article id={`payment-anomaly-${group.anchor_anomaly_id}`} key={group.anchor_anomaly_id} className={`rounded border p-3 space-y-3 text-sm min-w-0 break-words ${params.get("exception_group") === String(group.anchor_anomaly_id) ? "border-primary" : ""}`}><div className="flex flex-wrap justify-between gap-2"><strong>责任组 #{group.anchor_anomaly_id} · {money(group.amount_cents)}</strong><span>{!group.merchant_verified ? "原交易身份不可核验" : group.unresolved ? "待解决" : "已确认退回"}</span></div><p>原支付 #{group.payment_id} · {group.provider} · 组版本 {group.group_version}</p><p className="break-all">精确原渠道流水：{group.external_transaction_id || "缺失，不能人工补填解锁"}</p><p className="break-all">原商户订单：{group.provider_payment_id}</p><p>通知事件 {group.event_count} 条 · 首次 {date(group.first_seen)} · 最近 {date(group.last_seen)}</p>{!group.merchant_verified && <p className="text-destructive">未取得完整可信原交易关联，禁止登记或确认退回；没有手填原交易号、更改金额或人工解除保护入口。</p>}{create && group.can_create && <Button size="sm" disabled={blocked} onClick={() => start("create", group)}>登记异常资金退回证据</Button>}
    {!group.refunds.length && <p className="text-muted-foreground">暂无退回证据登记。</p>}{group.refunds.map(refund => <div key={refund.id} className="rounded border p-3 space-y-2 min-w-0"><p className="font-medium">退回登记 #{refund.id} · {labels[refund.status]} · 版本 {refund.version}</p><p>路径：{methods[refund.method] ?? refund.method} · {money(refund.amount_cents)}</p><p className="break-all">外部退回流水：{refund.provider_refund_id}</p><p>实际到账：{date(refund.received_at)}</p><p className="break-all">受控凭证：{refund.evidence_ref}</p><p className="break-all text-xs">SHA-256：{refund.evidence_sha256}</p><p>登记说明：{refund.note}</p><p>申请员工 #{refund.requested_by} · {date(refund.created_at)} · 申请组版本 {refund.expected_group_version}</p>{refund.reviewed_by && <p>复核员工 #{refund.reviewed_by} · 复核组版本 {refund.reviewed_group_version} · {refund.review_note}</p>}{refund.status === "pending_review" && refund.requested_by === actor && <p className="text-muted-foreground">申请人不得自行确认或驳回，请其他有权限员工独立复核。</p>}{review && refund.can_review && <div className="flex flex-wrap gap-2"><Button size="sm" disabled={blocked} onClick={() => start("confirm", group, refund)}>独立确认已外退</Button><Button size="sm" variant="outline" disabled={blocked} onClick={() => start("reject", group, refund)}>驳回退回登记</Button></div>}</div>)}</article>)}
    <ExceptionPager total={resource.data.total} offset={offset} count={resource.data.items.length} busy={mutation.busy} change={value => update({ anomaly_offset: value })} />
  </>}
  <Dialog open={!!draft} onOpenChange={value => { if (!value) cancel(); }}><DialogContent showCloseButton={!mutation.busy} className="min-w-0 max-h-[85dvh] overflow-y-auto"><DialogHeader><DialogTitle>{draft?.kind === "create" ? "登记异常资金退回证据" : draft?.kind === "confirm" ? "独立确认异常资金已外退" : "驳回异常退回登记"}</DialogTitle><DialogDescription>仅记录已在外部发生的退回事实，本站不执行退款。原客户、原交易和金额由服务端固定；凭证仍需独立人工核实，不宣称自动验证真实到账。</DialogDescription></DialogHeader><div className="text-sm space-y-2 break-all"><p>责任组 #{draft?.group.anchor_anomaly_id} · 原支付 #{draft?.group.payment_id} · {money(draft?.group.amount_cents ?? 0)}</p><p>组版本 {draft?.group.group_version}{draft?.refund ? ` · 申请 #${draft.refund.id} · 申请版本 ${draft.refund.version}` : ""}</p><p>原交易：{draft?.group.external_transaction_id}</p>{draft?.refund && <><p>外部退回流水：{draft.refund.provider_refund_id}</p><p>实际到账：{date(draft.refund.received_at)}</p><p>受控凭证：{draft.refund.evidence_ref}</p><p className="text-xs">SHA-256：{draft.refund.evidence_sha256}</p></>}</div>
    <fieldset disabled={blocked} className="space-y-3 min-w-0">{draft?.kind === "create" && <><label className="grid gap-2 text-sm">退回路径<select aria-label="异常资金退回路径" className="w-full rounded border bg-background p-2" value={fields.method} onChange={event => setFields(value => ({ ...value, method: event.target.value }))}>{Object.entries(methods).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>{([["provider_refund_id", "外部退回流水", "text", 200], ["received_at", "实际退回到账时间", "datetime-local", 100], ["evidence_ref", "受控凭证引用", "text", 500], ["evidence_sha256", "凭证 SHA-256", "text", 64]] as const).map(([key, label, type, max]) => <label key={key} className="grid gap-2 text-sm min-w-0">{label}<Input aria-label={label} className="min-w-0" type={type} maxLength={max} value={fields[key]} onChange={event => setFields(value => ({ ...value, [key]: event.target.value }))} /></label>)}</>}<label className="grid gap-2 text-sm">{draft?.kind === "create" ? "退回登记说明" : "独立复核说明"}<Textarea aria-label={draft?.kind === "create" ? "退回登记说明" : "异常资金独立复核说明"} maxLength={1000} value={fields.note} onChange={event => setFields(value => ({ ...value, note: event.target.value }))} /></label></fieldset>
    {(error || mutation.error) && <p role="alert" className="text-sm text-destructive break-words">{error || mutation.error}</p>}<DialogFooter className="flex-wrap"><Button variant="outline" disabled={mutation.busy} onClick={cancel}>{mutation.pending ? "关闭，保留原请求" : "取消未提交草稿"}</Button><Button disabled={blocked || !validNote(fields.note)} onClick={submit}>{mutation.busy ? "正在提交" : draft?.kind === "create" ? "核对并保存退回证据" : draft?.kind === "confirm" ? "核实并独立确认" : "确认驳回"}</Button></DialogFooter></DialogContent></Dialog>
  </section>;
}
