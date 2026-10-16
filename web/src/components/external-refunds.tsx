"use client";

import { useEffect, useRef, useState } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { api, ApiError, money, date, type User } from "@/lib/api";
import { can } from "@/lib/admin-navigation";
import type { TopupState } from "./topup-invoice-actions";
import { useResource } from "@/hooks/use-resource";
import { useAdminIdentity, useAdminPermission } from "./workspace";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Textarea } from "./ui/textarea";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "./ui/dialog";
import type { ManualInvoiceRecord } from "./manual-invoices";
import type { PaymentRecord } from "./online-payment";

import { OrderRefundImpact, OrderRefundSnapshot, validOrderContext, validOrderVersions, validOrderReversal, type OrderRefundContext, type OrderRefundReversal } from "./external-refund-order";

type Refund = {
  id: number;
  invoice_id: number;
  payment_id: number;
  user_id: number;
  amount_cents: number;
  status: "pending_review" | "confirmed" | "rejected";
  version: number;
  method: string;
  original_transaction_id: string;
  provider: string;
  channel_instance_id: string;
  provider_payment_id: string;
  provider_refund_id: string;
  received_at: string;
  evidence_ref: string;
  evidence_sha256: string;
  note: string;
  requested_by: number;
  reviewed_by: number | null;
  review_note: string;
  created_at: string;
  updated_at: string;
  order_reversal?: OrderRefundReversal | null;
  reversal?: { status: "pending" | "released" | "applied"; amount_cents: number; ledger_id: number | null; applied_at: string | null; released_at: string | null } | null;
};
type Page = { items: Refund[]; total: number; limit: number; offset: number };
type Invoice = Omit<ManualInvoiceRecord, "items"> & {
  external_payment_paid: boolean;
  order_id: number | null;
  credit_payment?: unknown;
  topup?: TopupState;
  items: {
    description: string;
    amount_cents: number;
    item_type: string;
    service_id: number | null;
  }[];
  payments: PaymentRecord[];
};
const statuses = {
  pending_review: "待独立复核",
  confirmed: "已确认到账",
  rejected: "已驳回",
};
const methods: Record<string, string> = {
  original_route: "原路退回",
  bank_transfer: "人工银行转账",
  other: "其他人工退付",
};
const blank = () => ({
  payment_id: "",
  method: "original_route",
  provider_refund_id: "",
  received_at: "",
  evidence_ref: "",
  evidence_sha256: "",
  note: "",
});

type Pending = { path: string; body: string; key: string; kind: "create" | "confirm" | "reject"; recordId: number | null; rejected: boolean };

export function ExternalRefunds(props: { invoice: Invoice; refreshInvoice: () => void }) {
  const actor = useAdminIdentity();
  const view = useAdminPermission("external_refunds.view");
  const create = useAdminPermission("external_refunds.create");
  const review = useAdminPermission("external_refunds.review");
  const orderReverse = useAdminPermission("external_refunds.order_reverse");
  return <RefundPanel key={`${actor?.id ?? 0}:${view}:${create}:${review}:${orderReverse}:${props.invoice.id}`} {...props} />;
}

function RefundPanel({
  invoice,
  refreshInvoice,
}: {
  invoice: Invoice;
  refreshInvoice: () => void;
}) {
  const view = useAdminPermission("external_refunds.view");
  const create = useAdminPermission("external_refunds.create");
  const review = useAdminPermission("external_refunds.review");
  const orderReverse = useAdminPermission("external_refunds.order_reverse");
  const orderInvoice = invoice.order_id != null;
  const user = useAdminIdentity();
  const searchParams = useSearchParams();
  const pathname = usePathname();
  const router = useRouter();
  const requestedOffset = searchParams.get("refund_offset") ?? "0";
  const parsedOffset = /^\d+$/.test(requestedOffset)
    ? Number(requestedOffset)
    : 0;
  const offset =
    Number.isSafeInteger(parsedOffset) && parsedOffset <= 2147483640
      ? Math.floor(parsedOffset / 10) * 10
      : 0;
  function onPageChange(nextOffset: number) {
    const params = new URLSearchParams(searchParams.toString());
    if (nextOffset > 0) params.set("refund_offset", String(nextOffset));
    else params.delete("refund_offset");
    const query = params.toString();
    router.replace(query ? `${pathname}?${query}` : pathname, {
      scroll: false,
    });
  }
  const path = `/admin/invoices/${invoice.id}/external-refunds`;
  const list = useResource<Page>(
    view ? `${path}?limit=10&offset=${offset}` : null,
    false,
  );
  const orderContext = useResource<OrderRefundContext>(view && orderInvoice ? `/admin/invoices/${invoice.id}/external-refund-order-context` : null, false);
  const refreshOrderContext = orderContext.refresh;
  useEffect(() => { refreshOrderContext(); }, [invoice.version, refreshOrderContext]);
  const [orderDraft, setOrderDraft] = useState<OrderRefundContext | null>(null);
  const [draft, setDraft] = useState(blank);
  const [open, setOpen] = useState(false);
  const [action, setAction] = useState<{
    record: Refund;
    kind: "confirm" | "reject";
  } | null>(null);
  const [note, setNote] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [pending, setPending] = useState<Pending | null>(null);
  const [storageReady, setStorageReady] = useState(false);
  const [storageError, setStorageError] = useState("");
  const [reviewed, setReviewed] = useState(false);
  const [latestFacts, setLatestFacts] = useState("");
  const [identityFailed, setIdentityFailed] = useState(false);
  const running = useRef(false);
  const mounted = useRef(true);
  const storage = `zjmfgo:external-refund:${user?.id ?? 0}:${invoice.id}`;
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  useEffect(() => {
    queueMicrotask(() => {
      if (!mounted.current) return;
      try {
        const raw = sessionStorage.getItem(storage);
        if (raw) {
          const saved = JSON.parse(raw) as Pending;
          const body = JSON.parse(saved.body) as Record<string, unknown>;
          const expectedPath = saved.kind === "create" ? path : `${path}/${saved.recordId}/${saved.kind}`;
          const allowed = saved.kind === "create" ? ["payment_id", "amount_cents", "method", "provider_refund_id", "received_at", "evidence_ref", "evidence_sha256", "note", "expected_version", "expected_order_version", "expected_service_versions"] : ["note", "expected_version"];
          if (!["create", "confirm", "reject"].includes(saved.kind) || saved.path !== expectedPath || typeof saved.key !== "string" || saved.key.length < 8 || saved.key.length > 128 || typeof saved.rejected !== "boolean" || (saved.kind !== "create" && (!Number.isSafeInteger(saved.recordId) || (saved.recordId ?? 0) < 1)) || (saved.kind === "create" && saved.recordId !== null) || !Number.isSafeInteger(body.expected_version) || Number(body.expected_version) < 1 || typeof body.note !== "string" || !body.note.trim() || Array.from(body.note).length > 1000 || Object.keys(body).some(key => !allowed.includes(key))) throw new Error();
          if (saved.kind === "create" && (!Number.isSafeInteger(body.payment_id) || Number(body.payment_id) < 1 || !Number.isSafeInteger(body.amount_cents) || Number(body.amount_cents) < 1 || typeof body.method !== "string" || !["original_route", "bank_transfer", "other"].includes(body.method) || typeof body.provider_refund_id !== "string" || !body.provider_refund_id.trim() || typeof body.received_at !== "string" || !Number.isFinite(Date.parse(body.received_at)) || typeof body.evidence_ref !== "string" || !body.evidence_ref.trim() || typeof body.evidence_sha256 !== "string" || !/^[a-f0-9]{64}$/.test(body.evidence_sha256))) throw new Error();
          if (saved.kind === "create" && (orderInvoice ? !validOrderVersions(body.expected_order_version, body.expected_service_versions) : Object.hasOwn(body, "expected_order_version") || Object.hasOwn(body, "expected_service_versions"))) throw new Error();
          setPending(saved); setConflict(saved.rejected);
        }
        setStorageReady(true);
      } catch { setStorageError("原退款请求恢复资料无法读取，已暂停新操作。请联系支持核实，勿删除恢复资料后重新登记。"); }
    });
  }, [storage, path, orderInvoice]);
  useEffect(() => {
    if (!pending && !busy) return;
    const leave = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    window.addEventListener("beforeunload", leave);
    return () => window.removeEventListener("beforeunload", leave);
  }, [pending, busy]);
  const successfulPayments = invoice.payments.filter(
    (p) => p.status === "succeeded",
  );
  const payments = successfulPayments.filter(
    (p) => p.amount_cents === invoice.amount_cents,
  );
  const topup = invoice.invoice_kind === "topup";
  const ordinaryEligible =
    (topup ? invoice.topup?.status === "credited" && (!invoice.topup.resolution || (invoice.topup.resolution.amount_cents === invoice.amount_cents && invoice.topup.resolution.ledger_id > 0)) : invoice.invoice_kind === "manual") &&
    invoice.status === "paid" &&
    invoice.external_payment_paid &&
    !invoice.order_id &&
    !invoice.credit_payment &&
    !invoice.import_review_required &&
    invoice.items.length > 0 &&
    invoice.items.every((i) => i.item_type === (topup ? "topup" : "manual") && !i.service_id) &&
    successfulPayments.length === 1 &&
    payments.length === 1;
  const orderEligible = orderInvoice && orderReverse && validOrderContext(orderContext.data, invoice.id, invoice.order_id!, invoice.version) && orderContext.data?.eligible === true && !orderContext.error;
  const eligible = orderInvoice ? orderEligible : ordinaryEligible;
  const activeRefund = list.data?.items.some((r) => r.status !== "rejected");
  const update = (key: keyof ReturnType<typeof blank>, value: string) => {
    setDraft((d) => ({ ...d, [key]: value }));
    setError("");
  };
  async function liveIdentity(permission: string, requireOrderReverse = false) {
    const live = await api<User>("/me");
    if (live.id !== user?.id || !can(live, "external_refunds.view") || !can(live, permission) || (requireOrderReverse && !can(live, "external_refunds.order_reverse"))) {
      setIdentityFailed(true);
      throw new Error("员工身份或退款权限已变化，请使用原账户核实；原请求保留。");
    }
    setIdentityFailed(false);
    return live;
  }
  async function readLatest() {
    if (running.current) return;
    running.current = true; setBusy(true); setError(""); setReviewed(false);
    try {
      await liveIdentity("external_refunds.view");
      const current = await api<Invoice>(`/admin/invoices/${invoice.id}`);
      const context = orderInvoice ? await api<OrderRefundContext>(`/admin/invoices/${invoice.id}/external-refund-order-context`) : null;
      if (context && (context.invoice_id !== invoice.id || context.order_id !== invoice.order_id)) throw new Error("订单退款案件关联不完整，原请求保留。");
      let found: Refund | undefined;
      let pageOffset = 0;
      do {
        const page = await api<Page>(`${path}?limit=100&offset=${pageOffset}`);
        found = page.items.find(record => record.id === pending?.recordId);
        if (!pending?.recordId || found || pageOffset + page.items.length >= page.total || page.items.length === 0) break;
        pageOffset += page.items.length;
      } while (true);
      await liveIdentity("external_refunds.view");
      if (!mounted.current) return;
      if (pending?.recordId && !found) throw new Error("未读到原退款记录，不能结束原请求，请核对权限或联系支持。");
      setLatestFacts(`最新账单：${current.status}，版本 ${current.version}${found ? `；原退款 #${found.id}：${statuses[found.status]}，版本 ${found.version}` : ""}${context ? `；订单 #${context.order_id}，版本 ${context.order_version}，交付冻结 ${context.order_reversal?.status ?? "无活动记录"}，服务版本：${context.services.map(item => `#${item.id}/v${item.version}`).join("、")}` : ""}。原请求版本与载荷未改变。`);
      setReviewed(true); list.refresh(); orderContext.refresh(); refreshInvoice();
    } catch (failure) { if (mounted.current) setError((failure as Error).message); }
    finally { running.current = false; if (mounted.current) setBusy(false); }
  }
  async function write(url: string, body: object, kind: Pending["kind"], recordId: number | null, original?: Pending) {
    if (running.current || !storageReady || storageError || (!original && pending)) return;
    const request: Pending = original ? { ...original, rejected: false } : { path: url, body: JSON.stringify(body), key: `external-refund-${crypto.randomUUID()}`, kind, recordId, rejected: false };
    running.current = true; setBusy(true); setError(""); setNotice("");
    let dispatched = false;
    let responseReceived = false;
    try {
      await liveIdentity(request.kind === "create" ? "external_refunds.create" : "external_refunds.review", orderInvoice || Object.hasOwn(JSON.parse(request.body), "expected_order_version"));
      if (!mounted.current) return;
      try { sessionStorage.setItem(storage, JSON.stringify(request)); }
      catch { throw new Error("浏览器无法保存原退款请求，已停止发送。请检查浏览器存储。"); }
      setPending(request); setConflict(false); setReviewed(false); setLatestFacts(""); dispatched = true;
      const result = await api<{ refund: Refund; replayed: boolean }>(request.path, { method: "POST", body: request.body, headers: { "Idempotency-Key": request.key } });
      responseReceived = true;
      if (orderInvoice) {
        const originalBody = JSON.parse(request.body) as { expected_version: number; expected_order_version?: number; expected_service_versions?: unknown[] };
        if (!validOrderReversal(result?.refund?.order_reversal, invoice.order_id!, request.kind === "create" ? "pending" : request.kind === "confirm" ? "applied" : "released", request.kind === "create" ? originalBody.expected_service_versions?.length : undefined) || (request.kind === "create" && (result.refund.order_reversal?.invoice_version !== originalBody.expected_version || result.refund.order_reversal?.order_version !== originalBody.expected_order_version))) throw new Error("订单冲正回执关联或交付/库存事实不完整，请保留原请求核实。");
      }
      if (topup && (!result?.refund?.reversal || result.refund.reversal.status !== (request.kind === "create" ? "pending" : request.kind === "confirm" ? "applied" : "released") || result.refund.reversal.amount_cents !== invoice.amount_cents || (request.kind === "confirm" && (!Number.isSafeInteger(result.refund.reversal.ledger_id) || (result.refund.reversal.ledger_id ?? 0) < 1)))) throw new Error("充值冲正回执不完整，请保留原请求核实冻结及扣减事实。");
      if (!result?.refund || result.refund.invoice_id !== invoice.id || !Number.isSafeInteger(result.refund.id) || result.refund.id < 1 || (request.recordId && result.refund.id !== request.recordId) || result.refund.status !== (request.kind === "create" ? "pending_review" : request.kind === "confirm" ? "confirmed" : "rejected")) throw new Error("退款回执不完整，请保留原请求核实结果。");
      await liveIdentity(request.kind === "create" ? "external_refunds.create" : "external_refunds.review", orderInvoice || Object.hasOwn(JSON.parse(request.body), "expected_order_version"));
      if (!mounted.current) return;
      try { sessionStorage.removeItem(storage); }
      catch { throw new Error("回执已确认，但原请求恢复资料无法清理；请继续以原请求核实，勿重新登记。"); }
      setPending(null); setOpen(false); setAction(null); setDraft(blank()); setNote(""); setConflict(false); setReviewed(false); setLatestFacts("");
      setNotice(orderInvoice ? request.kind === "create" ? "退款登记已保存，整单交付已冻结，等待其他有订单冲正权限的员工独立复核。客户余额不变。" : request.kind === "confirm" ? "真实到账已独立确认，整单已退款、服务已取消，库存按单位释放一次；客户余额不变，原收款保留。" : "退款登记已驳回，交付冻结已释放；订单仍待原流程审核，不会自动开通，余额和库存未变。" : result.replayed ? "原退款请求回执已确认，未重复变更；请以最新退款及余额事实为准。" : topup ? request.kind === "create" ? "退款登记已保存，对应余额已冻结，等待其他员工独立复核。" : request.kind === "confirm" ? "退款到账已确认，冻结余额已扣减，原收款和入账流水保留。" : "退款登记已驳回，对应余额冻结已释放。" : "退款记录已保存。原收款记录保留，未增加客户余额。");
      list.refresh(); orderContext.refresh(); refreshInvoice();
    } catch (failure) {
      if (!mounted.current) return;
      if (dispatched && !responseReceived && failure instanceof ApiError && [400, 404, 409, 422].includes(failure.status)) {
        const rejected = { ...request, rejected: true };
        try { sessionStorage.setItem(storage, JSON.stringify(rejected)); setPending(rejected); }
        catch { setStorageError("原请求拒绝状态无法保存，已暂停新操作；原键与载荷仍须保留核实。"); }
        setConflict(true); setReviewed(false);
        setError(`${failure.message}。原键、原版本及原载荷已保留；请显式读取最新事实，系统不会自动换版本提交。`);
      } else setError(`${(failure as Error).message}${dispatched ? " 结果尚未确认，请保留并核实原请求，勿重新登记。" : " 未发送退款请求。"}`);
    } finally { running.current = false; if (mounted.current) setBusy(false); }
  }
  async function endRejected() {
    if (!pending?.rejected || !reviewed || running.current || !window.confirm("已核对最新事实，确认结束被拒绝的原请求？此操作不提交资金变更。之后须重新填写并确认，才会创建新请求。")) return;
    running.current = true; setBusy(true); setError("");
    try {
      await liveIdentity(pending.kind === "create" ? "external_refunds.create" : "external_refunds.review", orderInvoice || Object.hasOwn(JSON.parse(pending.body), "expected_order_version"));
      if (!mounted.current) return;
      sessionStorage.removeItem(storage); setPending(null); setConflict(false); setReviewed(false); setOpen(false); setAction(null); setDraft(blank()); setNote(""); setLatestFacts("");
    } catch (failure) { if (mounted.current) setError((failure as Error).message); }
    finally { running.current = false; if (mounted.current) setBusy(false); }
  }
  function closeDraft(kind: "create" | "review") {
    if (busy) return;
    const dirty = kind === "create" ? Object.entries(draft).some(([key, value]) => key === "method" ? value !== "original_route" : value !== "") : !!note;
    if (!pending && dirty && !window.confirm("放弃尚未提交的退款草稿？")) return;
    if (kind === "create") { setOpen(false); if (!pending) setDraft(blank()); }
    else { setAction(null); if (!pending) setNote(""); }
  }
  function submit(now: number) {
    if (orderInvoice && (!orderReverse || !orderDraft?.eligible || !validOrderContext(orderDraft, invoice.id, invoice.order_id!, invoice.version))) { setError("原订单资格或版本上下文不完整，请取消草稿后显式刷新核对。不会自动换版本提交。"); return; }
    const received = new Date(draft.received_at);
    if (
      !payments.some((p) => p.id === Number(draft.payment_id)) ||
      !draft.provider_refund_id.trim() ||
      !draft.evidence_ref.trim() ||
      !/^[a-fA-F0-9]{64}$/.test(draft.evidence_sha256) ||
      !draft.note.trim() ||
      !Number.isFinite(received.getTime()) ||
      received.getTime() > now
    ) {
      setError(
        "请选择原支付，填写退款流水、有效到账时间（不可晚于现在）、证据引用、64 位 SHA-256 和说明。",
      );
      return;
    }
    if (
      !window.confirm(
        `确认登记整单外部退款 ${money(invoice.amount_cents)}？此操作不执行退款，独立复核后才确认出款事实。${orderInvoice ? "申请会持久冻结整单交付；确认后取消所有服务并释放库存，余额不变。驳回仅释放冻结，不自动审核或开通。" : topup ? "申请会冻结客户可用余额，确认后扣减，驳回后释放。" : ""}`,
      )
    )
      return;
    void write(path, {
      ...draft,
      payment_id: Number(draft.payment_id),
      amount_cents: invoice.amount_cents,
      provider_refund_id: draft.provider_refund_id.trim(),
      evidence_ref: draft.evidence_ref.trim(),
      note: draft.note.trim(),
      evidence_sha256: draft.evidence_sha256.toLowerCase(),
      received_at: received.toISOString(),
      expected_version: orderInvoice ? orderDraft!.invoice_version : invoice.version,
      ...(orderInvoice ? { expected_order_version: orderDraft!.order_version, expected_service_versions: orderDraft!.services.map(({ id, version }) => ({ id, version })) } : {}),
    }, "create", null);
  }
  function submitReview() {
    if (!action || !note.trim()) {
      setError("请填写复核说明。");
      return;
    }
    const record = action.record;
    if (orderInvoice && (!orderReverse || !validOrderReversal(record.order_reversal, invoice.order_id!, "pending"))) { setError("缺少订单冲正权限或原退款冻结事实不完整，未发送请求。"); return; }
    const latest = list.data?.items.find((r) => r.id === record.id);
    if (!latest || latest.version !== record.version || latest.status !== record.status || record.status !== "pending_review") {
      setError("该记录已被处理，请关闭后查看最新结果。");
      return;
    }
    if (
      !window.confirm(
        action.kind === "confirm"
          ? `确认已核实付款人真实到账 ${money(record.amount_cents)}，并完成独立复核？确认将记录出款并更新账单。${record.order_reversal ? "整单及全部服务将冲正，库存仅释放一次，余额不变；不得再次向客户转账。" : record.reversal ? "同时扣减已冻结的客户余额，不可重复退付。" : ""}`
          : `确认驳回该退款登记？${record.order_reversal ? "仅释放交付冻结，订单仍须按原流程审核，不会自动开通；余额和库存不变。" : record.reversal ? "将释放本申请冻结的客户余额。" : "驳回不会执行任何资金操作。"}`,
      )
    )
      return;
    void write(`${path}/${record.id}/${action.kind}`, {
      expected_version: record.version,
      note: note.trim(),
    }, action.kind, record.id);
  }
  if (!view) return null;
  return (
    <section className="rounded-xl border bg-card p-4 sm:p-6 space-y-4 min-w-0">
      <div className="flex flex-wrap gap-3 items-center justify-between">
        <h2 className="font-semibold">外部退款登记与复核</h2>
        <div className="flex flex-wrap gap-2">
          <Button
            size="sm"
            variant="outline"
            disabled={busy}
            onClick={() => {
              list.refresh();
              orderContext.refresh();
              refreshInvoice();
            }}
          >
            刷新
          </Button>
          {create && (
            <Button
              size="sm"
              disabled={
                busy || !storageReady || !!storageError || !!pending || identityFailed || !eligible || !list.data || !!list.error || activeRefund
              }
              onClick={() => {
                setOrderDraft(orderInvoice ? orderContext.data : null);
                setOpen(true);
                setError("");
                setConflict(false);
              }}
            >
              登记外部退款
            </Button>
          )}
        </div>
      </div>
      <p className="text-sm text-muted-foreground">
        这里只登记已在外部发生的退款，不执行退款、不增加客户余额。确认须核实真实到账并由其他员工独立复核；原收款记录永久保留。支持无业务关联的普通人工账单、实际已入账（含有完整补账事实）的充值，以及符合服务端资格的待审 manual 未交付新购订单整单退款。最终资格由服务端重验。
      </p>
      {orderInvoice && <div className="space-y-3 min-w-0">{!orderReverse && <p className="text-sm">当前缺少专用订单冲正权限，不能登记、确认或驳回订单退款。</p>}{orderContext.error ? <Failure error={orderContext.error} retry={orderContext.refresh} /> : !orderContext.data ? <Loading /> : <><p className="text-sm">{orderContext.data.eligible ? "服务端资格：当前可登记未交付订单整单退款。" : `服务端资格：${orderContext.data.reason || "当前不满足订单冲正条件"}`}</p>{orderContext.data.order_reversal && <OrderRefundImpact reversal={orderContext.data.order_reversal} />}</>}</div>}
      {topup && <p className="text-sm rounded border p-3">充值退款申请会冻结对应可用余额；独立确认后扣减，驳回后释放。可用余额不足时服务端拒绝申请，不允许透支或重复冲正。</p>}
      {!storageReady && !storageError && <p role="status" className="text-sm">正在检查本员工的原退款请求，完成前不可新操作。</p>}
      {storageError && <p role="alert" className="text-sm text-destructive">{storageError}</p>}
      {pending && <div role="status" className="rounded border p-3 space-y-2 text-sm"><p>存在尚需核实的原退款{pending.kind === "create" ? "登记" : pending.kind === "confirm" ? "确认" : "驳回"}请求。刷新后不会自动提交；原版本、载荷和幂等键保持不变。</p><div className="flex flex-wrap gap-2"><Button size="sm" disabled={busy || !!storageError} onClick={() => void write(pending.path, {}, pending.kind, pending.recordId, pending)}>以原请求核实退款结果</Button><Button size="sm" variant="outline" disabled={busy} onClick={() => void readLatest()}>读取最新退款事实</Button>{pending.rejected && reviewed && <Button size="sm" variant="outline" disabled={busy} onClick={() => void endRejected()}>已核对，结束被拒绝请求</Button>}</div>{latestFacts && <p>{latestFacts}</p>}{conflict && !reviewed && <p>原请求已被拒绝，请先读取最新事实；不会自动换键或换版本。</p>}</div>}
      {!open && !action && error && <p role="alert" className="text-sm text-destructive break-words">{error}</p>}
      {!eligible && (
        <p className="text-sm">
          {orderInvoice ? "订单退款仅依据服务端返回的整单资格、完整服务集合及版本。资格未确认、交付已开始或缺少订单冲正权限时不可登记。" : "此账单当前不满足登记条件：须为已付普通人工账单或实际已入账充值，匹配唯一全额成功外部收款，且无订单、资源或信用等关联。补账充值须保留完整补账事实与入账流水；未补入余额暂不支持。"}
        </p>
      )}
      {notice && (
        <p role="status" className="text-sm">
          {notice}
        </p>
      )}
      {list.error ? (
        <Failure error={list.error} retry={list.refresh} />
      ) : !list.data ? (
        <Loading />
      ) : (
        <>
          {list.data.items.length === 0 ? (
            <p className="text-sm text-muted-foreground">暂无外部退款记录。</p>
          ) : (
            <div className="space-y-3">
              {list.data.items.map((r) => (
                <article
                  key={r.id}
                  className="rounded-lg border p-3 space-y-2 text-sm break-all min-w-0"
                >
                  <div className="flex flex-wrap gap-2 justify-between">
                    <strong>
                      #{r.id} · {money(r.amount_cents)}
                    </strong>
                    <span>{statuses[r.status]}</span>
                  </div>
                  <p>
                    原支付 #{r.payment_id} · 资金路径：
                    {methods[r.method] ?? r.method}
                  </p>
                  <p>
                    原渠道：{r.provider} · 渠道实例：
                    {r.channel_instance_id || "未记录"}
                  </p>
                  <p>原商户订单：{r.provider_payment_id || "未记录"}</p>
                  <p>
                    原渠道流水：
                    {r.original_transaction_id || "未记录，请先核对原收款"}
                  </p>
                  <p>退款流水：{r.provider_refund_id}</p>
                  <p>
                    实际到账：{new Date(r.received_at).toLocaleString("zh-CN")}
                  </p>
                  <p>证据引用：{r.evidence_ref}</p>
                  <p className="text-xs">SHA-256：{r.evidence_sha256}</p>
                  <p>登记说明：{r.note}</p>
                  <p>
                    申请人 #{r.requested_by} · 复核人{" "}
                    {r.reviewed_by ? `#${r.reviewed_by}` : "待复核"} · 版本{" "}
                    {r.version}
                  </p>
                  {r.review_note && <p>复核说明：{r.review_note}</p>}
                  {r.order_reversal && <OrderRefundImpact reversal={r.order_reversal} />}
                  {r.reversal && <div className="rounded border p-3 space-y-1"><p>充值余额冲正：{r.reversal.status === "pending" ? "已冻结，待复核" : r.reversal.status === "applied" ? "已扣减" : "已释放"} · {money(r.reversal.amount_cents)}</p>{r.reversal.ledger_id && <p>冲正流水 #{r.reversal.ledger_id}</p>}{r.reversal.applied_at && <p>扣减时间：{date(r.reversal.applied_at)}</p>}{r.reversal.released_at && <p>释放时间：{date(r.reversal.released_at)}</p>}</div>}
                  {review &&
                    r.status === "pending_review" &&
                    (r.requested_by === user?.id ? (
                      <p className="text-muted-foreground">
                        申请人不得自行复核，请由其他有权限员工处理。
                      </p>
                    ) : (
                      <div className="flex flex-wrap gap-2">
                        {(["confirm", "reject"] as const).map((kind) => (
                          <Button
                            key={kind}
                            size="sm"
                            variant={
                              kind === "confirm" ? "destructive" : "outline"
                            }
                            disabled={busy || !!pending || !storageReady || !!storageError || identityFailed || (orderInvoice && (!orderReverse || !validOrderReversal(r.order_reversal, invoice.order_id!, "pending")))}
                            onClick={() => {
                              setAction({ record: r, kind });
                              setNote("");
                              setError("");
                              setConflict(false);
                            }}
                          >
                            {kind === "confirm" ? "确认真实到账" : "驳回登记"}
                          </Button>
                        ))}
                      </div>
                    ))}
                </article>
              ))}
            </div>
          )}
          <div className="flex flex-wrap gap-3 items-center justify-between text-sm">
            <span>
              共 {list.data.total} 条 · 第 {Math.floor(offset / 10) + 1} 页
            </span>
            <div className="flex gap-2">
              <Button
                size="sm"
                variant="outline"
                disabled={offset === 0 || busy}
                onClick={() => onPageChange(Math.max(0, offset - 10))}
              >
                上一页
              </Button>
              <Button
                size="sm"
                variant="outline"
                disabled={
                  offset + list.data.items.length >= list.data.total || busy
                }
                onClick={() => onPageChange(offset + 10)}
              >
                下一页
              </Button>
            </div>
          </div>
        </>
      )}
      <Dialog
        open={open}
        onOpenChange={(value) => {
          if (!value) closeDraft("create");
        }}
      >
        <DialogContent
          className="max-h-[90dvh] overflow-y-auto min-w-0"
          showCloseButton={!busy}
        >
          <DialogHeader>
            <DialogTitle className="pr-12">
              登记外部退款 · {money(invoice.amount_cents)}
            </DialogTitle>
            <DialogDescription>
              仅登记已经发生的整单退付。请勿填写密钥或完整个人敏感资料。取消不会提交。{topup && "申请冻结对应可用余额，确认扣减，驳回释放。"}
            </DialogDescription>
          </DialogHeader>
          {orderInvoice && orderDraft && <OrderRefundSnapshot context={orderDraft} />}
          <fieldset disabled={busy || !!pending} className="space-y-3 min-w-0">
            <div className="space-y-1">
              <Label htmlFor="refund-payment">原外部支付</Label>
              <select
                id="refund-payment"
                className="w-full min-w-0 rounded-md border p-2 text-sm"
                value={draft.payment_id}
                onChange={(e) => update("payment_id", e.target.value)}
              >
                <option value="">请选择成功收款</option>
                {payments.map((p) => (
                  <option key={p.id} value={p.id}>
                    #{p.id} · {p.provider} · {money(p.amount_cents)} ·{" "}
                    {p.external_transaction_id || p.provider_payment_id}
                  </option>
                ))}
              </select>
            </div>
            <div className="space-y-1">
              <Label htmlFor="refund-method">资金路径</Label>
              <select
                id="refund-method"
                className="w-full rounded-md border p-2 text-sm"
                value={draft.method}
                onChange={(e) => update("method", e.target.value)}
              >
                {Object.entries(methods).map(([value, label]) => (
                  <option key={value} value={value}>
                    {label}
                  </option>
                ))}
              </select>
            </div>
            {(
              [
                ["provider_refund_id", "外部退款流水", "text", 200],
                ["received_at", "实际到账时间", "datetime-local", 100],
                ["evidence_ref", "证据引用（受控凭证编号或位置）", "text", 500],
                [
                  "evidence_sha256",
                  "证据 SHA-256（64 位十六进制）",
                  "text",
                  64,
                ],
              ] as const
            ).map(([key, label, type, max]) => (
              <div className="space-y-1" key={key}>
                <Label htmlFor={`refund-${key}`}>{label}</Label>
                <Input
                  id={`refund-${key}`}
                  type={type}
                  step={type === "datetime-local" ? 1 : undefined}
                  aria-describedby={key === "received_at" ? "refund-received-help" : undefined}
                  maxLength={max}
                  value={draft[key]}
                  onChange={(e) => update(key, e.target.value)}
                />
                {key === "received_at" && <p id="refund-received-help" className="text-xs text-muted-foreground break-words">请按真实到账凭证填写，可精确到秒；不得早于原支付完成时间或晚于现在。系统不会自动补入到账时间。{payments.find(payment => payment.id === Number(draft.payment_id))?.completed_at && <>原支付完成：{new Date(payments.find(payment => payment.id === Number(draft.payment_id))!.completed_at!).toLocaleString("zh-CN")}。</>}</p>}
              </div>
            ))}
            <Label htmlFor="refund-note">登记说明</Label>
            <Textarea
              id="refund-note"
              maxLength={1000}
              value={draft.note}
              onChange={(e) => update("note", e.target.value)}
            />
          </fieldset>
          {error && (
            <p role="alert" className="text-sm text-destructive break-words">
              {error}
            </p>
          )}
          {conflict && (
            <p className="text-sm">
              原请求版本未改变。请在账单面板显式读取最新事实后处理被拒绝请求。
            </p>
          )}
          <DialogFooter>
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => closeDraft("create")}
            >
              {pending ? "关闭，保留原请求" : "取消未提交草稿"}
            </Button>
            <Button
              disabled={
                busy || !!pending || !storageReady || !!storageError || identityFailed || !eligible || !create || !list.data || !!list.error
              }
              onClick={() => submit(Date.now())}
            >
              {busy ? "提交中…" : "核对并登记"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!action}
        onOpenChange={(value) => {
          if (!value) closeDraft("review");
        }}
      >
        <DialogContent
          className="max-h-[90dvh] overflow-y-auto"
          showCloseButton={!busy}
        >
          <DialogHeader>
            <DialogTitle className="pr-12">
              {action?.kind === "confirm"
                ? "独立复核：确认真实到账"
                : "驳回外部退款登记"}
            </DialogTitle>
            <DialogDescription>
              核对原支付、退款流水、到账时间与证据。商户受理不代表付款人已到账；申请人不得自审。{action?.record.reversal && (action.kind === "confirm" ? "确认将扣减被冻结余额，请勿再次向客户转账。" : "驳回将释放本申请冻结的余额。")}
            </DialogDescription>
          </DialogHeader>
          {action?.record.order_reversal && <><OrderRefundImpact reversal={action.record.order_reversal} />{orderContext.data && <><p className="text-xs text-muted-foreground">以下为当前服务端上下文；复核提交仍使用本退款原版本，由服务端核验申请快照。</p><OrderRefundSnapshot context={orderContext.data} /></>}</>}
          <div className="space-y-2 text-sm break-all">
            <p>
              记录 #{action?.record.id} ·{" "}
              {money(action?.record.amount_cents ?? 0)}
            </p>
            <p>
              资金路径：
              {action
                ? (methods[action.record.method] ?? action.record.method)
                : ""}
            </p>
            <p>
              原渠道：{action?.record.provider} · 实例：
              {action?.record.channel_instance_id || "未记录"}
            </p>
            <p>原商户订单：{action?.record.provider_payment_id || "未记录"}</p>
            <p>
              原渠道流水：
              {action?.record.original_transaction_id ||
                "未记录，请先核对原收款"}
            </p>
            <p>退款流水：{action?.record.provider_refund_id}</p>
            <p>
              到账时间：
              {action
                ? new Date(action.record.received_at).toLocaleString("zh-CN")
                : ""}
            </p>
            <p>证据引用：{action?.record.evidence_ref}</p>
            <p className="text-xs">SHA-256：{action?.record.evidence_sha256}</p>
          </div>
          <Label htmlFor="refund-review-note">复核说明（必填）</Label>
          <Textarea
            id="refund-review-note"
            disabled={busy || !!pending}
            maxLength={1000}
            value={note}
            onChange={(e) => setNote(e.target.value)}
          />
          {error && (
            <p role="alert" className="text-sm text-destructive break-words">
              {error}
            </p>
          )}
          {conflict && (
            <p className="text-sm">草稿已保留，请核对刷新后的记录再提交。</p>
          )}
          <DialogFooter>
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => closeDraft("review")}
            >
              {pending ? "关闭，保留原请求" : "取消未提交草稿"}
            </Button>
            <Button
              variant="destructive"
              disabled={
                busy || !!pending || !storageReady || !!storageError || identityFailed ||
                !note.trim() ||
                !review ||
                (!!action?.record.order_reversal && !orderReverse) ||
                action?.record.requested_by === user?.id ||
                !!list.error ||
                !list.data
              }
              onClick={() => submitReview()}
            >
              {busy
                ? "提交中…"
                : action?.kind === "confirm"
                  ? "核实并确认"
                  : "确认驳回"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  );
}
