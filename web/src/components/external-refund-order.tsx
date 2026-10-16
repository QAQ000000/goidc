"use client";
import { date, type OrderRefundReversal } from "@/lib/api";
import { Status } from "./common";
export type { OrderRefundReversal } from "@/lib/api";
export type OrderRefundContext = {
  eligible: boolean; reason_code: string; reason: string; invoice_id: number; invoice_version: number; order_id: number | null; order_version: number | null;
  services: { id: number; version: number; product_id: number; order_item_id: number; unit_number: number; status: string; provision_mode: string; inventory_released: boolean }[];
  order_reversal: OrderRefundReversal | null;
};
export type ServiceVersion = { id: number; version: number };
const positive = (v: unknown): v is number => Number.isSafeInteger(v) && Number(v) > 0;
export function validOrderVersions(order: unknown, services: unknown): services is ServiceVersion[] {
  return positive(order) && Array.isArray(services) && services.length > 0 && services.every((item, index) => item && Object.keys(item).length === 2 && positive(item.id) && positive(item.version) && (index === 0 || services[index - 1].id < item.id));
}
export function validOrderContext(context: OrderRefundContext | null, invoice: number, order: number, version: number) {
  return !!context && context.invoice_id === invoice && context.order_id === order && context.invoice_version === version && Array.isArray(context.services) && context.services.every(service => service && positive(service.id) && positive(service.version) && positive(service.product_id) && positive(service.order_item_id) && positive(service.unit_number) && typeof service.status === "string" && typeof service.provision_mode === "string" && typeof service.inventory_released === "boolean") && validOrderVersions(context.order_version, context.services.map(({ id, version }) => ({ id, version })));
}
export function validOrderReversal(value: OrderRefundReversal | null | undefined, order: number, status: OrderRefundReversal["status"], count?: number) {
  return !!value && value.order_id === order && value.status === status && positive(value.invoice_version) && positive(value.order_version) && Number.isFinite(Date.parse(value.created_at ?? "")) && positive(value.service_count) && (count === undefined || value.service_count === count) && Number.isSafeInteger(value.stock_units_released) && (status === "applied" ? value.stock_units_released === value.service_count && Number.isFinite(Date.parse(value.applied_at ?? "")) : value.stock_units_released === 0) && (status !== "released" || Number.isFinite(Date.parse(value.released_at ?? "")));
}
export function OrderRefundImpact({ reversal }: { reversal: OrderRefundReversal }) {
  return <div role="status" className="rounded border p-3 space-y-1 text-sm min-w-0 break-words"><p>订单 #{reversal.order_id} · {reversal.status === "pending" ? "外部退款待复核，交付已冻结" : reversal.status === "applied" ? "整单已退款，服务已取消" : "退款已驳回，交付冻结已释放"}</p><p>关联服务 {reversal.service_count} 个 · 已释放库存单位 {reversal.stock_units_released} 个</p><p>{reversal.status === "pending" ? "冻结期间不能审核通过或开通；刷新、关闭弹窗和超时不会解除冻结。" : reversal.status === "released" ? "订单仍须按原流程审核，不会自动审核或开通；库存和原收款保持原事实。" : "保留原待审核历史，已退款终态优先；库存仅释放一次。"} 客户余额不变。</p>{reversal.applied_at && <p>冲正时间：{date(reversal.applied_at)}</p>}{reversal.released_at && <p>释放时间：{date(reversal.released_at)}</p>}</div>;
}
export function OrderRefundSnapshot({ context }: { context: OrderRefundContext }) {
  return <div className="rounded border p-3 space-y-2 text-sm min-w-0 break-words"><p>整单冲正：订单 #{context.order_id} · 订单版本 {context.order_version} · 账单版本 {context.invoice_version}</p><p>申请将冻结整单交付；独立确认后账单和订单标为已退款、以下全部服务取消，按单位释放库存一次。客户余额不增不减；仍须核实付款人真实到账凭据。</p><ul className="space-y-1">{context.services.map(service => <li key={service.id}>服务 #{service.id} · 版本 {service.version} · 商品 #{service.product_id} · 订单项 #{service.order_item_id} / 单位 {service.unit_number} · {service.status} · {service.provision_mode} · 库存{service.inventory_released ? "已释放" : "未释放"}</li>)}</ul><p>只支持服务均未交付且从未有任务的 manual 待审新购订单。退款驳回仅释放交付冻结，不自动审核或开通。</p></div>;
}

// Both list and detail APIs return the persisted order hold. Do not infer it from a local dialog.
export function orderRefundHold(value: unknown): OrderRefundReversal | null {
  if (!value || typeof value !== "object") return null;
  const hold = value as OrderRefundReversal;
  return positive(hold.order_id) && ["pending", "released", "applied"].includes(hold.status) && positive(hold.service_count) ? hold : null;
}
export function orderRefundBlocksDelivery(value: unknown) {
  // A nonempty incomplete projection also requires an explicit refresh before a delivery action.
  return !!value && orderRefundHold(value)?.status !== "released";
}
export function OrderRefundHoldLabel({ value }: { value: unknown }) {
  const hold = orderRefundHold(value);
  if (!hold) return value ? <span className="block text-xs text-destructive">退款冻结事实不完整，请刷新核实</span> : null;
  return <span role="status" className="block text-xs whitespace-normal mt-1">{hold.status === "pending" ? "外部退款待复核 · 交付冻结" : hold.status === "applied" ? "整单已退款 · 禁止交付" : "退款已驳回 · 冻结已释放，仍须原流程审核"}</span>;
}
export function OrderRefundOrderStatus({ row, review = false }: { row: Record<string, unknown>; review?: boolean }) {
  const terminal = row.status === "refunded" || orderRefundHold(row.external_refund_order_hold)?.status === "applied";
  if (review && terminal) return <span className="text-xs text-muted-foreground">原审核：{row.review_status === "pending" ? "待审核" : String(row.review_status ?? "approved")}（历史，退款终态优先）</span>;
  return <><Status value={terminal ? "refunded" : String(review ? row.review_status ?? "approved" : row.status)} />{!review && <OrderRefundHoldLabel value={row.external_refund_order_hold} />}</>;
}
