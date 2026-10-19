import type { Product, ProductOption, ProductOptionGroup, PurchaseField } from "./api";
import { configurationRuleError, isQuantityOption, optionQuantityBounds, productOptionAmounts, resolveProductOptions } from "./product-options";

export function purchaseCycles(product: Product) {
  if (product.pricing?.length) return product.pricing.filter(price => price.active);
  return [{ billing_cycle: product.billing_cycle, price_cents: product.price_cents, setup_fee_cents: product.setup_fee_cents, active: true }];
}

export function defaultPurchaseCycle(product: Product) {
  const prices = purchaseCycles(product);
  return prices.find(price => price.billing_cycle === product.billing_cycle)?.billing_cycle ?? prices[0]?.billing_cycle ?? product.billing_cycle;
}

export function purchaseOptionQuantity(group: ProductOptionGroup, option: ProductOption, quantities: Record<number, number>) {
  return quantities[option.id] ?? optionQuantityBounds(group, option).min;
}

// This is a public catalog reference, before customer-group and coupon discounts.
// Cart quotes and checkout continue to own the authoritative amount.
export function purchaseConfiguration(product: Product, cycle: string, choices: Record<number, number>, quantities: Record<number, number>, quantity: number) {
  const resolved = resolveProductOptions(product.option_groups ?? [], choices);
  const base = purchaseCycles(product).find(price => price.billing_cycle === cycle);
  const optionQuantities: Record<number, number> = {};
  let error: string | null = !base ? "所选账期暂不可用，请重新选择。" : !resolved.complete ? "请完成所有必选配置。" : null;
  let recurring = base?.price_cents ?? 0;
  let setup = base?.setup_fee_cents ?? 0;
  const selections = resolved.branches.flatMap(({ group, options, selected }) => {
    const option = options.find(candidate => candidate.id === selected);
    if (!option) return [];
    const units = isQuantityOption(group) ? purchaseOptionQuantity(group, option, quantities) : 1;
    if (isQuantityOption(group)) {
      const { min, max, step } = optionQuantityBounds(group, option);
      if (!Number.isSafeInteger(units) || units < min || units > max || (units - min) % step !== 0) error ??= `${group.name} 数量不符合范围或步长。`;
      optionQuantities[option.id] = units;
    }
    const amount = productOptionAmounts(option, cycle, units);
    if (!amount) error ??= `${group.name} 在当前账期与数量下暂无可用价格。`;
    recurring += amount?.price ?? 0;
    setup += amount?.setup ?? 0;
    return [{ group, option, quantity: units }];
  });
  error ??= configurationRuleError(product.configuration_rules, product.option_groups ?? [], resolved.ids, optionQuantities);
  if (!Number.isSafeInteger(quantity) || quantity < 1 || quantity > 100) error ??= "购买数量必须是 1–100 的整数。";
  if (product.stock !== null && quantity > product.stock) error ??= "购买数量超过当前可售库存。";
  if (product.stock === 0) error ??= "商品暂时售罄。";
  if (product.active === false || product.category_visible === false) error ??= "商品暂不可购买。";
  const recurringTotal = recurring * quantity;
  const setupTotal = setup * quantity;
  const total = recurringTotal + setupTotal;
  if (![recurring, setup, recurringTotal, setupTotal, total].every(value => Number.isSafeInteger(value) && value >= 0)) error ??= "所选配置价格暂不可用，请调整配置或联系管理员。";
  return { resolved, optionIds: resolved.ids, optionQuantities, selections, recurring, setup, recurringTotal, setupTotal, total, error };
}

export function purchaseFieldsError(fields: PurchaseField[], values: Record<string, string>) {
  for (const field of fields.filter(item => item.active && item.scope !== "admin")) {
    const value = (values[field.key] ?? "").trim();
    if (field.required && (!value || field.type === "checkbox" && value !== "true")) return `请填写${field.label}${field.type === "checkbox" ? "并确认勾选" : ""}。`;
    if (!value) continue;
    if ([...value].length > field.max_length) return `${field.label}不能超过 ${field.max_length} 个字符。`;
    if (field.type === "select" && !field.choices.includes(value)) return `${field.label}的选择已不可用，请重新选择。`;
    if (field.type === "checkbox" && value !== "true") return `${field.label}的值无效。`;
    if (field.encrypted && (value === "********" || value.startsWith("enc:"))) return `请填写${field.label}的新值。`;
    if (field.type === "url") {
      try {
        const url = new URL(value);
        if (!["http:", "https:"].includes(url.protocol) || url.username || url.password) return `${field.label}需要不含账户信息的 HTTP/HTTPS 地址。`;
      } catch { return `${field.label}需要完整的 HTTP/HTTPS 地址。`; }
    }
  }
  return null;
}
