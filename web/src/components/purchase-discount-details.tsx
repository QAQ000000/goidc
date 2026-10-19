import { money } from "@/lib/api";

export type PurchaseGroupSnapshot = {
  source: "customer_group" | "product_group" | "none" | "override";
  customer_group_id?: number;
  customer_group_name?: string;
  customer_group_version?: number;
  product_group_id?: number;
  product_group_name?: string;
  product_group_rule_id?: number;
  product_group_version?: number;
  discount_type?: "percent" | "fixed" | "none";
  discount_percent?: number;
  fixed_discount_cents?: number;
};
export type PurchasePricingLine = {
  item_id: number;
  product_id: number;
  quantity: number;
  gross_cents: number;
  group_discount_cents: number;
  promo_discount_cents: number;
  net_cents: number;
  recurring_cents: number;
  group_snapshot: PurchaseGroupSnapshot;
};

export function PurchaseDiscountDetails({ pricing, compact = false }: { pricing?: PurchasePricingLine | null; compact?: boolean }) {
  if (!pricing) return null;
  const snapshot = pricing.group_snapshot;
  const group = snapshot.source === "product_group"
    ? `${snapshot.customer_group_name || "客户组"} · ${snapshot.product_group_name || "商品分组"}`
    : snapshot.customer_group_name || "客户组默认规则";
  return <div className="mt-2 space-y-1 text-xs text-muted-foreground whitespace-normal" aria-label="购买优惠明细">
    {snapshot.source === "override" ? <p>管理员内部价格，不叠加分组减免</p> : (snapshot.source === "customer_group" || snapshot.source === "product_group") && <p className="break-words">{group}{snapshot.discount_type === "fixed" ? ` · 每件减免 ${money(snapshot.fixed_discount_cents ?? 0)}` : ` · 减免 ${snapshot.discount_percent ?? 0}%`}</p>}
    <p>分组减免 -{money(pricing.group_discount_cents)}{pricing.promo_discount_cents > 0 ? ` · 优惠码减免 -${money(pricing.promo_discount_cents)}` : ""}</p>
    {!compact && <><p>原价 {money(pricing.gross_cents)} · 本次应付 {money(pricing.net_cents)}</p><p>每件服务续费基价 {money(pricing.recurring_cents)}，续费优惠另按已保存条款计算</p></>}
  </div>;
}

export function PurchaseDiscountSummary({ discountCents, groupDiscountCents, promoDiscountCents, promoCode }: { discountCents: number; groupDiscountCents?: number | null; promoDiscountCents?: number | null; promoCode?: string }) {
  if (groupDiscountCents == null || promoDiscountCents == null) {
    return discountCents > 0 ? <p className="text-sm text-muted-foreground mt-2">优惠 {money(discountCents)}{promoCode ? ` · ${promoCode}` : ""}</p> : null;
  }
  return <div className="text-sm text-muted-foreground mt-2 space-y-1" aria-label="订单优惠汇总">
    <p>分组减免 -{money(groupDiscountCents)}</p>
    <p>优惠码减免 -{money(promoDiscountCents)}{promoCode ? ` · ${promoCode}` : ""}</p>
    {discountCents > 0 && <p>优惠合计 {money(discountCents)}</p>}
  </div>;
}
