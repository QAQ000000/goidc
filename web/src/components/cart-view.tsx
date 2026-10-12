"use client";
import { useState, useEffect, useRef } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { Trash2, ArrowRight, ShoppingCart, Pencil } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import {
  Table,
  TableHeader,
  TableHead,
  TableBody,
  TableRow,
  TableCell,
} from "@/components/ui/table";
import { api, ApiError, money, type Cart, type CartItem, type Product } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace } from "./workspace";
import { PurchaseFieldInputs,PurchaseFieldDetails,purchaseFieldValues } from "./purchase-fields";
import { cycles, Loading, Failure } from "./common";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { changeProductOption, configurationRuleError, isQuantityOption, optionQuantityBounds, productOptionAmounts, resolveProductOptions } from "@/lib/product-options";
import { ProductOptionChoice } from "./product-option-choice";
type Selection = { version: number; ids: number[] };
type OptionQuantityMap = Record<number, number>;
export function CartView() {
  const resource = useResource<Cart>("/cart");
  const { data, error, refresh } = resource;
  const router = useRouter();
  const [selection, setSelection] = useState<Selection>({
    version: 0,
    ids: [],
  });
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const [promoCode, setPromoCode] = useState("");
  const retry = useRef<{ key: string; body: string } | null>(null);
  const selected = selection.version === data?.version ? selection.ids : [];
  useEffect(() => {
    function clear() {
      setSelection({ version: 0, ids: [] });
      refresh();
    }
    window.addEventListener("focus", clear);
    return () => window.removeEventListener("focus", clear);
  }, [refresh]);
  function toggle(id: number, checked: boolean) {
    if (!data) return;
    setSelection({
      version: data.version,
      ids: checked
        ? [...selected.filter((v) => v !== id), id]
        : selected.filter((v) => v !== id),
    });
    retry.current = null;
  }
  async function mutate(id: number, method: string, quantity?: number, extra: Record<string, unknown> = {}): Promise<boolean> {
    if (!data) return false;
    setBusy(true);
    setSelection({ version: 0, ids: [] });
    retry.current = null;
    setNotice("");
    try {
      await api(`/cart/items/${id}`, {
        method,
        body: JSON.stringify({
          version: data.version,
          ...(quantity ? { quantity } : {}),
          ...extra,
        }),
      });
      refresh();
      return true;
    } catch (e) {
      setNotice((e as Error).message);
      refresh();
      return false;
    } finally {
      setBusy(false);
    }
  }
  async function checkout() {
    if (!data || selected.length === 0) return;
    setBusy(true);
    setNotice("");
    const body = JSON.stringify({
      cart_version: data.version,
      item_ids: [...selected].sort((a, b) => a - b),
    });
    if (!retry.current || retry.current.body !== body)
      retry.current = { key: crypto.randomUUID(), body };
    try {
      const order = await api<{ order_id: number }>("/orders", {
        method: "POST",
        headers: { "Idempotency-Key": retry.current.key },
        body,
      });
      retry.current = null;
      setSelection({ version: 0, ids: [] });
      router.push(`/console/orders/${order.order_id}`);
    } catch (e) {
      setNotice((e as Error).message);
      if (e instanceof ApiError) {
        retry.current = null;
        setSelection({ version: 0, ids: [] });
        refresh();
      }
    } finally {
      setBusy(false);
    }
  }
  async function applyPromo(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!data || !promoCode.trim()) return;
    setBusy(true); setNotice(""); setSelection({ version: 0, ids: [] });
    try { await api("/cart/promo", { method: "POST", body: JSON.stringify({ version: data.version, code: promoCode }) }); setPromoCode(""); refresh(); }
    catch (e) { setNotice((e as Error).message); refresh(); }
    finally { setBusy(false); }
  }
  async function removePromo() {
    if (!data) return;
    setBusy(true); setNotice(""); setSelection({ version: 0, ids: [] });
    try { await api("/cart/promo", { method: "DELETE", body: JSON.stringify({ version: data.version }) }); refresh(); }
    catch (e) { setNotice((e as Error).message); refresh(); }
    finally { setBusy(false); }
  }
  const total =
    data?.items
      .filter((item) => selected.includes(item.id))
      .reduce((sum, item) => sum + item.total_cents, 0) ?? 0;
  const selectedAll = !!data && selected.length === data.items.length;
  const shownDiscount = selectedAll ? (data?.promo?.discount_cents ?? 0) : 0;
  return (
    <Workspace title="购物车">
      <div className="flex justify-between items-center gap-4 mb-8">
        <h1 className="page-title">购物车</h1>
        <Button variant="outline" asChild>
          <Link href="/">继续选购</Link>
        </Button>
      </div>
      {notice && (
        <p role="alert" className="text-destructive mb-4">
          {notice}
        </p>
      )}
      {error ? (
        <><Failure error={error} retry={refresh} /><div className="flex flex-wrap gap-4 text-sm mt-3"><Link href="/console/account" className="text-primary underline">验证绑定手机</Link><Link href="/console/certification" className="text-primary underline">实名认证</Link></div></>
      ) : !data ? (
        <Loading />
      ) : data.items.length === 0 ? (
        <div className="py-20 text-center">
          <ShoppingCart
            className="mx-auto text-muted-foreground mb-5"
            size={40}
          />
          <p className="text-muted-foreground mb-6">购物车为空</p>
          <Button asChild>
            <Link href="/">选购产品</Link>
          </Button>
        </div>
      ) : (
        <>
          <div className="data-surface">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-12" />
                  <TableHead>产品</TableHead>
                  <TableHead>单价</TableHead>
                  <TableHead>数量</TableHead>
                  <TableHead>小计</TableHead>
                  <TableHead className="w-12" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.items.map((item) => (
                  <TableRow key={item.id}>
                    <TableCell>
                      <Checkbox
                        aria-label={`选择 ${item.name}`}
                        checked={selected.includes(item.id)}
                        disabled={busy || !item.current || !item.active}
                        onCheckedChange={(v) => toggle(item.id, v === true)}
                      />
                    </TableCell>
                    <TableCell>
                      <span className="font-medium">{item.name}</span>
                      <div className="text-xs text-muted-foreground mt-1">
                        {cycles[item.billing_cycle]}付
                      </div>
                      {(!item.current || !item.active) && (
                        <div className="text-xs text-destructive mt-1">
                          产品已变更，请重新选购
                        </div>
                      )}
                      {item.admission_error&&<div role="alert" className="text-xs text-destructive mt-2">{item.admission_error}<div className="flex gap-3 mt-1"><Link className="underline" href="/console/account">验证绑定手机</Link><Link className="underline" href="/console/certification">实名认证</Link></div></div>}{item.config_snapshot?.map(option=><div key={option.id} className="text-xs text-muted-foreground mt-1 break-all">{option.group_name}：{option.label}{(option.quantity??1)>1?` × ${option.quantity}`:""}</div>)}
                      {!item.options_valid&&<div className="text-xs text-destructive mt-1">配置已不可用，请修改配置或移除</div>}
                      <PurchaseFieldDetails fields={item.purchase_field_snapshot}/>
                      {item.fields_error&&<p className="text-xs text-destructive mt-1">{item.fields_error}，请修改购买信息</p>}
                    </TableCell>
                    <TableCell className="money">
                      {money(item.price_cents + item.setup_fee_cents)}
                    </TableCell>
                    <TableCell>
                      <Input
                        type="number"
                        min={1}
                        max={100}
                        className="w-20"
                        aria-label={`${item.name} 数量`}
                        defaultValue={item.quantity}
                        key={`${item.id}-${data.version}`}
                        disabled={busy || !item.current || !item.active}
                        onBlur={(e) => {
                          const qty = Number(e.target.value);
                          if (
                            qty !== item.quantity &&
                            Number.isInteger(qty) &&
                            qty >= 1 &&
                            qty <= 100
                          )
                            void mutate(item.id, "PATCH", qty);
                          else e.target.value = String(item.quantity);
                        }}
                      />
                    </TableCell>
                    <TableCell className="money font-medium">
                      {money(item.total_cents)}
                    </TableCell>
                    <TableCell>
                      <CartItemEditor
                        key={`${item.id}-${data.version}`}
                        item={item}
                        disabled={busy}
                        onSave={async (extra) => { if (!(await mutate(item.id, "PATCH", item.quantity, extra))) throw new Error("保存失败，请检查购物车提示"); }}
                      />
                      <Button
                        size="icon"
                        variant="ghost"
                        aria-label={`删除 ${item.name}`}
                        title="删除"
                        disabled={busy}
                        onClick={() => mutate(item.id, "DELETE")}
                      >
                        <Trash2 size={16} />
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
          <section className="data-surface p-5 mt-5">
            <div className="flex flex-wrap items-end gap-3">
              <form className="flex flex-wrap items-end gap-2" onSubmit={applyPromo}>
                <div><label className="text-xs text-muted-foreground block mb-1" htmlFor="cart-promo">优惠码</label><Input id="cart-promo" value={promoCode} onChange={(event) => setPromoCode(event.target.value.toUpperCase())} placeholder="输入优惠码" maxLength={50} disabled={busy} /></div>
                <Button variant="outline" disabled={busy || !promoCode.trim()}>应用</Button>
              </form>
              {data.promo && <div className="flex items-center gap-3 text-sm"><span className="font-mono">{data.promo.code}</span>{data.promo.error ? <span className="text-destructive">{data.promo.error}</span> : <span className="text-emerald-700">已优惠 {money(data.promo.discount_cents)}</span>}<Button type="button" variant="ghost" size="sm" disabled={busy} onClick={() => void removePromo()}>移除</Button></div>}
              {data.promo?.recurring && !data.promo.error && <p className="text-xs text-muted-foreground">符合范围的周期服务将保留续费优惠，{data.promo.recurring_limit?`每件可优惠续费 ${data.promo.recurring_limit} 次`:"不限续费次数"}；首次购买不占续费次数，一次性商品只享受本次优惠。固定金额按每件服务每期减免，最多减至零元。</p>}
            </div>
            {!selectedAll && data.promo && !data.promo.error && <p className="text-xs text-muted-foreground mt-3">优惠金额会在只结算部分商品时按所选项目重新计算。</p>}
          </section>
          <div className="flex flex-wrap justify-end items-center gap-6 py-6 border-b">
            <div>
              <div className="text-xs text-muted-foreground mb-1">
                已选 {selected.length} 项
              </div>
              {shownDiscount > 0 && <div className="text-xs text-emerald-700">优惠 -{money(shownDiscount)}</div>}
              <strong className="money text-2xl">{money(total - shownDiscount)}</strong>
            </div>
            <Button disabled={busy || selected.length === 0 || data.items.some(item=>selected.includes(item.id)&&!!item.admission_error)} onClick={checkout}>
              {busy ? "提交中" : "确认订单"}
              <ArrowRight size={16} />
            </Button>
          </div>
        </>
      )}
    </Workspace>
  );
}

function CartItemEditor({
  item,
  disabled,
  onSave,
}: {
  item: CartItem;
  disabled: boolean;
  onSave: (extra: { billing_cycle: string; option_ids: number[]; option_quantities: OptionQuantityMap;purchase_fields:Record<string,string> }) => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  const {data:product,error:productError,refresh:refreshProduct}=useResource<Product>(open?`/catalog/${item.product_id}`:null,false);
  const [cycle, setCycle] = useState(item.billing_cycle);
  const [options, setOptions] = useState<Record<number, number>>({});
  const [quantities, setQuantities] = useState<OptionQuantityMap>({});
  const [fields,setFields]=useState<Record<string,string>>({...item.purchase_fields});
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const activeGroups = product?.option_groups ?? [];
  const originalOptions = Object.fromEntries(activeGroups.map((group) => [group.id, group.options.find((option) => item.option_ids.includes(option.id))?.id ?? 0]));
  const resolved = resolveProductOptions(activeGroups, { ...originalOptions, ...options });
  const optionIDs = resolved.ids;
  const complete = resolved.complete;
  const ruleError=complete?configurationRuleError(product?.configuration_rules,activeGroups,optionIDs,Object.fromEntries(resolved.branches.flatMap(({group,options,selected})=>{const option=options.find(candidate=>candidate.id===selected);return option&&isQuantityOption(group)?[[option.id,quantities[option.id]??optionQuantityBounds(group,option).min]]:[];}))):null;
  function quantityFor(group: NonNullable<Product["option_groups"]>[number], option: NonNullable<Product["option_groups"]>[number]["options"][number]) { return quantities[option.id] ?? optionQuantityBounds(group, option).min; }
  const pricesValid = resolved.branches.every(({ group, options, selected }) => {
    const option = options.find(candidate => candidate.id === selected);
    return !option || productOptionAmounts(option, cycle, isQuantityOption(group) ? quantityFor(group, option) : 1) !== null;
  });
  function changeOpen(next: boolean) {
    if (next) {
      setCycle(item.billing_cycle);
      setOptions({});
      setQuantities({ ...item.option_quantities });
      setMessage("");
      setFields({...item.purchase_fields});
    }
    setOpen(next);
  }
  async function save() {
    if (!complete || !pricesValid || ruleError) {
      setMessage(ruleError??(pricesValid ? "请完成必选配置" : "所选数量暂无可用价格"));
      return;
    }
    setBusy(true);
    setMessage("");
    try {
      const selectedQuantities: OptionQuantityMap = {};
      for (const group of activeGroups) {
        const option = group.options.find(candidate => optionIDs.includes(candidate.id));
        if (!option || !isQuantityOption(group)) continue;
        const value = quantityFor(group, option);
        const { min, max, step } = optionQuantityBounds(group, option);
        if (!Number.isSafeInteger(value) || value < min || value > max || (value - min) % step !== 0) {
          throw new Error(`${group.name} 数量不符合范围或步长`);
        }
        selectedQuantities[option.id] = value;
      }
      await onSave({ billing_cycle: cycle, option_ids: optionIDs, option_quantities: selectedQuantities,purchase_fields:purchaseFieldValues(product?.purchase_field_definitions??[],fields) });
      setOpen(false);
    } catch (error) {
      setMessage((error as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Dialog open={open} onOpenChange={changeOpen}>
      <DialogTrigger asChild>
        <Button size="icon" variant="ghost" aria-label={`修改 ${item.name} 配置`} title="修改配置" disabled={disabled}>
          <Pencil size={15} />
        </Button>
      </DialogTrigger>
      <DialogContent className="max-h-[90dvh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>修改 {item.name}</DialogTitle>
          <DialogDescription>修改后的价格会在购物车中重新计算。</DialogDescription>
        </DialogHeader>
        {productError?<Failure error={productError} retry={refreshProduct}/>:!product?<Loading/>:<div className="space-y-4">
          {(product.pricing?.filter((price) => price.active).length ?? 0) > 1 && (
            <label className="block text-sm"><span className="block text-muted-foreground mb-2">付款周期</span><Select value={cycle} onValueChange={setCycle}><SelectTrigger><SelectValue /></SelectTrigger><SelectContent>{product.pricing?.filter((price) => price.active).map((price) => <SelectItem key={price.billing_cycle} value={price.billing_cycle}>{cycles[price.billing_cycle] ?? price.billing_cycle}</SelectItem>)}</SelectContent></Select></label>
          )}
          {resolved.branches.map(({ group, options: availableOptions, selected: selectedID }) => {
            const selected = availableOptions.find((option) => option.id === selectedID);
            const quantityType = isQuantityOption(group);
            return <div className="block text-sm" key={group.id}><span className="block text-muted-foreground mb-2">{group.name}{group.required ? "（必选）" : ""}</span><ProductOptionChoice group={group} options={availableOptions} selected={selectedID} label={`购物车 ${group.name}`} onChange={id => setOptions(changeProductOption(activeGroups, resolved.selected, group.id, id))} renderLabel={option => { const amount = productOptionAmounts(option, cycle, quantityType ? quantityFor(group, option) : 1); const delta = amount ? amount.price + amount.setup : null; return `${option.label}${delta === null ? " · 暂无可用价格" : delta !== 0 ? ` · ${money(delta)}` : ""}`; }} />{selected && quantityType && <label className="block text-xs text-muted-foreground mt-2">数量<Input type="number" aria-label={`购物车 ${group.name} 数量`} {...optionQuantityBounds(group, selected)} value={quantityFor(group, selected)} onChange={(event) => setQuantities((current) => ({ ...current, [selected.id]: Number(event.target.value) }))} /></label>}</div>;
          })}
          <PurchaseFieldInputs fields={product.purchase_field_definitions??[]} values={fields} onChange={setFields} disabled={busy}/>
          {(message||ruleError) && <p role="alert" className="text-sm text-destructive">{message||ruleError}</p>}
        </div>}
        <DialogFooter><Button variant="outline" onClick={() => setOpen(false)} disabled={busy}>取消</Button><Button onClick={() => void save()} disabled={busy || !product || !!productError || !complete || !pricesValid || !!ruleError}>{busy ? "保存中" : "保存配置"}</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
