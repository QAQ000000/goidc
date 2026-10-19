"use client";
import { useRef, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { Check, ShoppingCart } from "lucide-react";
import { api, ApiError, money, type Cart, type Product, type User } from "@/lib/api";
import { changeProductOption, isQuantityOption, optionGroupDescendants, optionQuantityBounds, productOptionAmounts } from "@/lib/product-options";
import { defaultPurchaseCycle, purchaseConfiguration, purchaseCycles, purchaseFieldsError, purchaseOptionQuantity } from "@/lib/product-purchase";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { ProductOptionChoice } from "./product-option-choice";
import { PurchaseFieldInputs, purchaseFieldValues } from "./purchase-fields";
import { cycles } from "./common";

export function ProductPurchaseForm({ product }: { product: Product }) {
  const router = useRouter();
  const [cycle, setCycle] = useState(() => defaultPurchaseCycle(product));
  const [quantity, setQuantity] = useState(1);
  const [options, setOptions] = useState<Record<number, number>>({});
  const [quantities, setQuantities] = useState<Record<number, number>>({});
  const [fields, setFields] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const submitting = useRef(false);
  const [notice, setNotice] = useState("");
  const [noticeKind, setNoticeKind] = useState<"error" | "login" | "success">("error");
  const prices = purchaseCycles(product);
  const configuration = purchaseConfiguration(product, cycle, options, quantities, quantity);
  const groups = product.option_groups ?? [];
  const fieldError = purchaseFieldsError(product.purchase_field_definitions ?? [], fields);
  const unavailable = product.active === false || product.category_visible === false || product.stock === 0;
  const blocked = busy || unavailable || !!configuration.error;
  function choose(groupID: number, optionID: number) {
    setOptions(changeProductOption(groups, configuration.resolved.selected, groupID, optionID));
    const descendants = optionGroupDescendants(groups, groupID);
    setQuantities(current => Object.fromEntries(Object.entries(current).filter(([id]) => !groups.some(group => descendants.has(group.id) && group.options.some(option => option.id === Number(id))))));
  }
  async function add(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (submitting.current) return;
    setNoticeKind("error"); setNotice("");
    if (configuration.error || fieldError) { setNotice(configuration.error ?? fieldError ?? "购买配置无效。"); return; }
    submitting.current = true;
    setBusy(true);
    let added = false;
    try {
      // A second tab can replace the session after this public page rendered.
      // Verify the current actor before reading or mutating customer commerce.
      const actor = await api<User>("/me", { portal: "customer" });
      if (actor.role !== "customer") throw new Error("客户登录状态无效，请重新登录后购买。");
      const cart = await api<Cart>("/cart", { portal: "customer" });
      await api("/cart/items", {
        portal: "customer",
        method: "POST",
        body: JSON.stringify({
          version: cart.version,
          product_id: product.id,
          quantity,
          billing_cycle: cycle,
          option_ids: configuration.optionIds,
          option_quantities: configuration.optionQuantities,
          purchase_fields: purchaseFieldValues(product.purchase_field_definitions ?? [], fields),
        }),
      });
      setNoticeKind("success");
      setNotice(`${product.name} × ${quantity} 已加入购物车。`);
      // Purchase fields can include secrets. Do not retain a submitted value in
      // a success screen, query string, draft store or cross-page navigation.
      setFields({});
      added = true;
      router.push("/cart");
    } catch (failure) {
      if (failure instanceof ApiError && failure.status === 401) {
        setNoticeKind("login"); setNotice("请先登录客户账号，当前配置仍保留在本页。");
      } else {
        setNotice((failure as Error).message);
      }
    } finally {
      if (!added) { submitting.current = false; setBusy(false); }
    }
  }
  return <form onSubmit={add} className="grid min-w-0 items-start gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(280px,340px)]">
    <div className="min-w-0 space-y-6">
      <fieldset disabled={busy || unavailable} className="min-w-0 space-y-6">
        <section className="data-surface min-w-0 p-5 sm:p-6">
          <h2 className="mb-5 text-lg font-semibold">付款周期与购买数量</h2>
          <div className="grid min-w-0 gap-5 sm:grid-cols-2">
            <div className="min-w-0 space-y-2"><label className="text-sm font-medium" htmlFor={`product-cycle-${product.id}`}>付款周期</label><Select value={cycle} disabled={busy || unavailable || !prices.length} onValueChange={setCycle}><SelectTrigger id={`product-cycle-${product.id}`} aria-label="付款周期" className="w-full"><SelectValue placeholder="选择付款周期" /></SelectTrigger><SelectContent>{prices.map(price => <SelectItem key={price.billing_cycle} value={price.billing_cycle}>{cycles[price.billing_cycle] ?? price.billing_cycle}</SelectItem>)}</SelectContent></Select><p className="text-xs leading-5 text-muted-foreground">按所选周期计费，初装费只在首次购买收取。</p></div>
            <label className="min-w-0 space-y-2 text-sm"><span className="font-medium">购买数量</span><Input aria-label="购买数量" type="number" min={1} max={100} step={1} required value={Number.isNaN(quantity) ? "" : quantity} onChange={event => setQuantity(event.target.value === "" ? NaN : Number(event.target.value))} /><span className="block text-xs leading-5 text-muted-foreground">1–100 件，每件使用相同规格与购买信息。{product.stock !== null ? `当前库存 ${product.stock} 件。` : "库存不限量。"}</span></label>
          </div>
        </section>
        <section className="data-surface min-w-0 p-5 sm:p-6">
          <h2 className="text-lg font-semibold">服务规格</h2>
          {!configuration.resolved.branches.length ? <p className="mt-3 text-sm text-muted-foreground">此商品使用固定规格，无需选择配置项。</p> : <div className="mt-5 grid min-w-0 gap-6 xl:grid-cols-2">{configuration.resolved.branches.map(({ group, options: available, selected }) => {
            const option = available.find(item => item.id === selected);
            const quantityType = isQuantityOption(group);
            return <div className="min-w-0 space-y-3" key={group.id}>
              <h3 className="text-sm font-medium break-words">{group.name}{group.required && <span className="ml-1 text-destructive">*</span>}</h3>
              <ProductOptionChoice group={group} options={available} selected={selected} label={`${product.name} ${group.name}`} disabled={busy || unavailable} onChange={id => choose(group.id, id)} renderLabel={item => {
                const amount = productOptionAmounts(item, cycle, quantityType ? purchaseOptionQuantity(group, item, quantities) : 1);
                return `${item.label}${!amount ? " · 暂无可用价格" : amount.price !== 0 || amount.setup !== 0 ? ` · 周期 ${money(amount.price)}${amount.setup !== 0 ? ` / 初装 ${money(amount.setup)}` : ""}` : ""}`;
              }} />
              {quantityType && option && <label className="block min-w-0 space-y-2 text-sm"><span>配置数量{group.unit ? `（${group.unit}）` : ""}</span><Input type="number" aria-label={`${group.name} 数量`} {...optionQuantityBounds(group, option)} required value={Number.isNaN(purchaseOptionQuantity(group, option, quantities)) ? "" : purchaseOptionQuantity(group, option, quantities)} onChange={event => setQuantities(current => ({ ...current, [option.id]: event.target.value === "" ? NaN : Number(event.target.value) }))} /><span className="block text-xs leading-5 text-muted-foreground">{optionQuantityBounds(group, option).min}–{optionQuantityBounds(group, option).max}{group.unit ? ` ${group.unit}` : ""}，步长 {optionQuantityBounds(group, option).step}{option.price_tiers?.some(tier => tier.billing_cycle === cycle && tier.active !== false) ? "；按当前数量对应的阶梯计价。" : "。"}</span></label>}
            </div>;
          })}</div>}
        </section>
        {!!product.purchase_field_definitions?.some(field => field.active && field.scope !== "admin") && <section className="data-surface min-w-0 p-5 sm:p-6"><PurchaseFieldInputs fields={product.purchase_field_definitions} values={fields} onChange={setFields} disabled={busy || unavailable} /><p className="text-xs leading-5 text-muted-foreground">资料仅保留在当前页面，提交后由服务端按字段规则校验并保存。敏感字段不会展示在订单摘要中。</p></section>}
      </fieldset>
    </div>
    <aside aria-label="所选配置与价格摘要" className="data-surface min-w-0 space-y-5 p-5 sm:p-6 lg:sticky lg:top-6">
      <div><h2 className="text-lg font-semibold">配置摘要</h2><p className="mt-2 break-words text-sm text-muted-foreground">{product.name} · {cycles[cycle] ?? cycle} · {Number.isSafeInteger(quantity) && quantity > 0 ? `${quantity} 件` : "请填写数量"}</p></div>
      <dl className="space-y-3 text-sm">{configuration.selections.length ? configuration.selections.map(({ group, option, quantity: units }) => <div key={group.id} className="grid grid-cols-[minmax(0,1fr)_minmax(0,1.5fr)] gap-3"><dt className="break-words text-muted-foreground">{group.name}</dt><dd className="break-words text-right">{option.label}{isQuantityOption(group) ? ` × ${units}${group.unit ? ` ${group.unit}` : ""}` : ""}</dd></div>) : <div><dt className="sr-only">规格</dt><dd className="text-muted-foreground">固定规格</dd></div>}</dl>
      {configuration.error ? <p role="alert" className="border-y py-4 text-sm leading-6 text-destructive">{configuration.error}</p> : <div className="space-y-3 border-y py-4 text-sm">
        <div className="flex justify-between gap-3"><span className="text-muted-foreground">周期费{quantity > 1 ? `（${quantity} 件）` : ""}</span><span className="money">{money(configuration.recurringTotal)}</span></div>
        <div className="flex justify-between gap-3"><span className="text-muted-foreground">初装费{quantity > 1 ? `（${quantity} 件）` : ""}</span><span className="money">{money(configuration.setupTotal)}</span></div>
        <div><span className="block text-xs text-muted-foreground">首次购买总参考价</span><strong className="money mt-2 block text-3xl font-semibold">{money(configuration.total)}</strong></div>
      </div>}
      <p className="text-xs leading-6 text-muted-foreground">参考价包含所选规格、数量与初装费。客户分组折扣、优惠码及最终应付金额以购物车报价为准。</p>
      {product.approval_required && <p className="text-xs leading-5 text-muted-foreground">此商品付款后需经管理员审核，再安排开通。</p>}
      {product.require_verified_phone && <p className="text-xs leading-5 text-muted-foreground">购买前需要验证绑定手机。<Link href="/console/account" className="ml-1 text-primary underline">前往验证</Link></p>}
      {product.require_certification && <p className="text-xs leading-5 text-muted-foreground">购买前需要完成实名认证。<Link href="/console/certification" className="ml-1 text-primary underline">前往认证</Link></p>}
      <Button type="submit" className="w-full" disabled={blocked}>{busy ? <Check size={16} /> : <ShoppingCart size={16} />}{busy ? "加入中…" : unavailable ? "暂不可购买" : "加入购物车"}</Button>
      {notice && <div role={noticeKind === "success" ? "status" : "alert"} className={`space-y-3 break-words text-sm leading-6 ${noticeKind === "success" ? "text-primary" : "text-destructive"}`}><p>{notice}</p>{noticeKind === "login" && <Link href="/login" className="underline">登录客户账号</Link>}{noticeKind === "success" && <Button className="w-full" variant="outline" asChild><Link href="/cart">查看购物车并结算</Link></Button>}</div>}
      {noticeKind !== "success" && <Link href="/cart" className="block text-center text-sm text-primary underline">查看购物车</Link>}
    </aside>
  </form>;
}
