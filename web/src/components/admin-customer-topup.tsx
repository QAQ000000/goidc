"use client";

import { useCallback, useState } from "react";
import Link from "next/link";
import { Plus, RefreshCw } from "lucide-react";
import { api, money } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { useDurableAdminMutation, type DurableRequest } from "@/hooks/use-durable-admin-mutation";
import { useAdminIdentity, useAdminPermission } from "./workspace";
import { Loading } from "./common";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "./ui/dialog";

type TopupOptions = {
  enabled: boolean;
  min_topup_cents: number;
  max_topup_cents: number;
  max_balance_cents: number;
  balance_cents: number;
  reserved_cents: number;
  available_capacity_cents: number | null;
  require_active_order: boolean;
  has_active_order: boolean;
  version: number;
};
type TopupBody = { amount_cents: number; expected_version: number; note: string };
export type CustomerTopupReceipt = {
  invoice_id: number;
  invoice_no: string;
  amount_cents: number;
  status: "unpaid";
  topup_status: "created";
  version: number;
  settings_version: number;
  user_id: number;
  replayed: boolean;
};
type Props = { customerId: number; customerName: string; onCreated?: (receipt: CustomerTopupReceipt) => void };
const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);
const positive = (value: unknown): value is number => typeof value === "number" && Number.isSafeInteger(value) && value > 0;
const nonnegative = (value: unknown): value is number => typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
const rejectedCodes = new Set(["invalid_topup", "version_conflict", "topup_disabled", "topup_below_minimum", "topup_above_maximum", "topup_active_order_required", "topup_balance_limit", "customer_inactive"]);

function topupBody(value: unknown): value is TopupBody {
  return object(value) && Object.keys(value).length === 3 && positive(value.amount_cents) && value.amount_cents <= 10_000_000_000 && positive(value.expected_version) && typeof value.note === "string" && value.note.trim() === value.note && !!value.note && Array.from(value.note).length <= 240 && !value.note.includes("\0");
}
function parseOptions(value: unknown): TopupOptions {
  if (!object(value) || typeof value.enabled !== "boolean" || typeof value.require_active_order !== "boolean" || typeof value.has_active_order !== "boolean" || !positive(value.version) || !nonnegative(value.min_topup_cents) || !positive(value.max_topup_cents) || value.max_topup_cents < value.min_topup_cents || !nonnegative(value.max_balance_cents) || typeof value.balance_cents !== "number" || !Number.isSafeInteger(value.balance_cents) || !nonnegative(value.reserved_cents) || value.available_capacity_cents !== null && !nonnegative(value.available_capacity_cents)) throw new Error("充值选项响应不完整，请重新读取后核对。");
  return value as TopupOptions;
}
function amountInCents(raw: string) {
  const text = raw.trim();
  if (!/^\d{1,9}(?:\.\d{1,2})?$/.test(text)) throw new Error("金额需为正数，最多两位小数，不支持科学计数法。");
  const [whole, fraction = ""] = text.split(".");
  const cents = Number(whole) * 100 + Number(fraction.padEnd(2, "0"));
  if (!positive(cents) || cents > 10_000_000_000) throw new Error("充值金额超出允许范围。");
  return cents;
}
function prepareTopup(amount: string, note: string, options: TopupOptions): TopupBody {
  const body = { amount_cents: amountInCents(amount), expected_version: options.version, note: note.trim() };
  if (!topupBody(body)) throw new Error("请填写1–240字的代建原因。");
  if (!options.enabled) throw new Error("充值功能已关闭。");
  if (options.require_active_order && !options.has_active_order) throw new Error("客户须先有已支付或已激活订单。");
  if (body.amount_cents < options.min_topup_cents) throw new Error(`金额不得低于 ${money(options.min_topup_cents)}。`);
  if (body.amount_cents > options.max_topup_cents) throw new Error(`金额不得超过 ${money(options.max_topup_cents)}。`);
  if (options.available_capacity_cents !== null && body.amount_cents > options.available_capacity_cents) throw new Error(`金额超过当前可用充值容量 ${money(options.available_capacity_cents)}。`);
  return body;
}
function validReceipt(result: CustomerTopupReceipt, request: DurableRequest) {
  const body: unknown = JSON.parse(request.body);
  return topupBody(body) && object(result) && positive(result.invoice_id) && typeof result.invoice_no === "string" && !!result.invoice_no.trim() && result.user_id === request.target && result.amount_cents === body.amount_cents && result.status === "unpaid" && result.topup_status === "created" && result.version === 1 && result.settings_version === body.expected_version && typeof result.replayed === "boolean";
}

export function CustomerTopupButton(props: Props) {
  const actor = useAdminIdentity();
  const view = useAdminPermission("invoices.view");
  const manage = useAdminPermission("invoices.manage");
  if (!actor || !view || !manage || !positive(props.customerId)) return null;
  const identity = JSON.stringify([actor.id, actor.role, actor.permissions, actor.customer_scope, actor.customer_ownerless, props.customerId]);
  return <CustomerTopupForActor key={identity} {...props} actorId={actor.id} />;
}

function CustomerTopupForActor({ actorId, customerId, customerName, onCreated }: Props & { actorId: number }) {
  const view = useAdminPermission("invoices.view");
  const manage = useAdminPermission("invoices.manage");
  const allowed = view && manage;
  const [open, setOpen] = useState(false);
  const [amount, setAmount] = useState("");
  const [note, setNote] = useState("");
  const [preview, setPreview] = useState<TopupBody | null>(null);
  const [error, setError] = useState("");
  const [created, setCreated] = useState<CustomerTopupReceipt | null>(null);
  const path = `/admin/customers/${customerId}/topups`;
  const optionsPath = `/admin/customers/${customerId}/topup-options`;
  const options = useResource<unknown>(open && allowed ? optionsPath : null, false);
  const validate = useCallback((request: DurableRequest) => {
    if (request.kind !== "customer_topup_create" || request.target !== customerId || request.path !== `/admin/customers/${customerId}/topups` || typeof request.key !== "string" || request.key.trim() !== request.key || /[\u0000-\u001f\u007f]/.test(request.key)) return false;
    try { return topupBody(JSON.parse(request.body)); } catch { return false; }
  }, [customerId]);
  const mutation = useDurableAdminMutation<CustomerTopupReceipt>({
    actor: actorId,
    scope: `customer-topup:${customerId}`,
    validate,
    permission: () => "invoices.manage",
    readPermission: "invoices.manage",
    rejectionCodes: rejectedCodes,
    receipt: validReceipt,
    readFacts: async () => {
      const latest = parseOptions(await api<unknown>(optionsPath));
      options.refresh();
      return { text: `当前设置版本 ${latest.version}；充值${latest.enabled ? "已启用" : "已关闭"}；单笔 ${money(latest.min_topup_cents)}–${money(latest.max_topup_cents)}；可用容量${latest.available_capacity_cents === null ? "不限额" : money(latest.available_capacity_cents)}；${latest.require_active_order && !latest.has_active_order ? "客户尚无符合条件的有效订单" : "订单条件符合"}。原请求未作修改。`, canEndRejected: true };
    },
    onSuccess: receipt => {
      setCreated(receipt); setOpen(false); setAmount(""); setNote(""); setPreview(null); setError(""); options.refresh();
      try { onCreated?.(receipt); } catch { setError("账单创建已确认，相关区域刷新失败，请手动刷新。"); }
    },
  });
  let facts: TopupOptions | null = null;
  let optionsError = "";
  if (options.data) { try { facts = parseOptions(options.data); } catch (failure) { optionsError = (failure as Error).message; } }
  const blocked = !allowed || !mutation.ready || !!mutation.storageError || mutation.busy || !!mutation.pending;
  const original = mutation.pending ? JSON.parse(mutation.pending.body) as TopupBody : null;
  function previewRequest(event: React.FormEvent) {
    event.preventDefault();
    if (blocked || options.isLoading || !facts) return;
    try { setPreview(prepareTopup(amount, note, facts)); setCreated(null); setError(""); } catch (failure) { setError((failure as Error).message); }
  }
  async function confirm() {
    if (!allowed || blocked || !preview) return;
    await mutation.execute({ path, body: preview, kind: "customer_topup_create", target: customerId });
  }
  async function finishRejected() {
    if (!allowed) return;
    await mutation.endRejected(); setPreview(null); options.refresh();
  }
  return <div className="min-w-0 space-y-2">
    <Button type="button" variant="outline" onClick={() => { if (allowed) setOpen(true); }}><Plus size={14} />代建未付充值单</Button>
    {mutation.pending && !open && <p role="status" className="text-xs text-muted-foreground">存在待核验的代建请求，请打开恢复。</p>}
    {created && <p role="status" className="break-words text-sm">充值账单创建已确认：<Link className="text-primary underline" href={`/admin/invoices/${created.invoice_id}`}>{created.invoice_no}</Link>。创建回执不代表当前支付状态，请打开账单核对。</p>}
    <Dialog open={open} onOpenChange={value => { if (!mutation.busy) setOpen(value); }}>
      <DialogContent className="max-h-[90dvh] max-w-lg overflow-y-auto">
        <DialogHeader><DialogTitle>代建未付充值账单</DialogTitle><DialogDescription>客户 #{customerId} · {customerName}。仅创建未付账单，不会直接增加钱包余额；客户后续付款成功后才按正常流程入账。</DialogDescription></DialogHeader>
        {!mutation.ready && !mutation.storageError && <p role="status" className="text-sm">正在检查当前管理员的原请求恢复资料…</p>}
        {mutation.storageError && <p role="alert" className="text-sm text-destructive">{mutation.storageError}</p>}
        {mutation.error && <p role="alert" className="break-words text-sm text-destructive">{mutation.error}</p>}
        {error && <p role="alert" className="break-words text-sm text-destructive">{error}</p>}
        {mutation.pending && original && <section className="space-y-3 rounded border p-4 text-sm" aria-label="原请求恢复">
          <p className="font-medium">{mutation.pending.rejected ? "原请求已被明确拒绝" : "原请求结果待核验"}</p>
          <p>金额 {money(original.amount_cents)} · 原设置版本 {original.expected_version}</p><p className="break-words">原因：{original.note}</p>
          <p className="text-muted-foreground">原载荷和键已保留。刷新、关闭弹窗不会重新创建；账单可能已生成，不能改金额换键再提交。</p>
          <div className="flex flex-wrap gap-2"><Button type="button" disabled={!allowed || mutation.busy || mutation.pending.rejected || !!mutation.storageError} onClick={() => { if (allowed) void mutation.execute(); }}>使用原请求核验</Button><Button type="button" variant="outline" disabled={!allowed || mutation.busy} onClick={() => { if (allowed) void mutation.readLatest(); }}>读取最新设置与资格</Button>{mutation.pending.rejected && mutation.reviewed && <Button type="button" variant="outline" disabled={!allowed || mutation.busy} onClick={() => void finishRejected()}>已核对，结束被拒绝请求</Button>}</div>
          {mutation.latestFacts && <p className="break-words text-muted-foreground">{mutation.latestFacts}</p>}
          {mutation.pending.rejected && <p className="text-muted-foreground">先读取并核对最新设置，再明确结束拒绝的原请求；下一次操作仍需重新预览与确认。</p>}
        </section>}
        {!mutation.pending && <>
          {options.error || optionsError ? <section role="alert" className="space-y-2 text-sm"><p className="text-destructive">{options.error?.message || optionsError}</p><Button variant="outline" onClick={options.refresh}><RefreshCw size={14} />重新读取充值选项</Button></section> : options.isLoading || !facts ? <Loading /> : <>
            <dl className="grid grid-cols-2 gap-3 rounded border bg-muted/20 p-3 text-sm"><div><dt className="text-muted-foreground">当前余额</dt><dd className="tabular-nums">{money(facts.balance_cents)}</dd></div><div><dt className="text-muted-foreground">已占用待到账容量</dt><dd className="tabular-nums">{money(facts.reserved_cents)}</dd></div><div><dt className="text-muted-foreground">单笔范围</dt><dd className="tabular-nums">{money(facts.min_topup_cents)}–{money(facts.max_topup_cents)}</dd></div><div><dt className="text-muted-foreground">可用充值容量</dt><dd>{facts.available_capacity_cents === null ? "不限额" : money(facts.available_capacity_cents)}</dd></div></dl>
            {!facts.enabled && <p role="status" className="text-sm text-destructive">充值功能已关闭，不能新建充值单。</p>}
            {facts.require_active_order && !facts.has_active_order && <p role="status" className="text-sm text-destructive">客户需先有已支付或已激活订单。</p>}
            {!preview ? <form className="space-y-4" onSubmit={previewRequest}><fieldset disabled={blocked || !facts.enabled || facts.require_active_order && !facts.has_active_order} className="space-y-4"><label className="block text-sm">充值金额（元）<Input className="mt-1" value={amount} inputMode="decimal" maxLength={12} required onChange={event => setAmount(event.target.value)} placeholder="例如 100.00" /></label><label className="block text-sm">代建原因<Textarea className="mt-1" value={note} required maxLength={480} onChange={event => setNote(event.target.value)} placeholder="说明客户诉求或对应工单；最多240字" /></label><Button type="submit">核对创建内容</Button></fieldset></form> : <section className="space-y-4 rounded border p-4 text-sm" aria-label="创建确认"><h3 className="font-medium">确认创建内容</h3><p>固定客户：#{customerId} · {customerName}</p><p className="tabular-nums">充值金额：{money(preview.amount_cents)}</p><p className="break-words">原因：{preview.note}</p><p className="text-muted-foreground">使用充值设置版本 {preview.expected_version}；只建未付账单，不直接入钱包。</p><div className="flex flex-wrap gap-2"><Button type="button" disabled={blocked} onClick={() => void confirm()}>{mutation.busy ? "创建中…" : "确认创建未付账单"}</Button><Button type="button" variant="outline" disabled={blocked} onClick={() => setPreview(null)}>返回修改</Button></div></section>}
          </>}
        </>}
        <div className="flex justify-end"><Button type="button" variant="outline" disabled={mutation.busy} onClick={() => setOpen(false)}>关闭</Button></div>
      </DialogContent>
    </Dialog>
  </div>;
}
