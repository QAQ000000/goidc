"use client";
import { useState } from "react";
import Link from "next/link";
import { ArrowLeft, Server, ShoppingCart, Check, Search } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api, money, type Cart, type Product, type ProductOption, type ProductPage, type CatalogCategory, type CatalogSection } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace } from "./workspace";
import { PurchaseFieldInputs,purchaseFieldValues } from "./purchase-fields";
import { cycles, Failure, Loading } from "./common";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { changeProductOption, isQuantityOption, optionQuantityBounds, productOptionAmounts, resolveProductOptions } from "@/lib/product-options";
import { ProductOptionChoice } from "./product-option-choice";
export function CatalogView({ initialProduct }: { initialProduct?: Product }) {
  const [query, setQuery] = useState("");
  const [draft, setDraft] = useState("");
  const [section, setSection] = useState("");
  const [category, setCategory] = useState("");
  const [offset, setOffset] = useState(0);
  const sections = useResource<Pick<CatalogSection,"id"|"name"|"description">[]>(initialProduct ? null : "/catalog/sections",false);
  const categories = useResource<Pick<CatalogCategory,"id"|"name"|"section_id"|"description"|"headline"|"tagline">[]>(initialProduct ? null : "/categories",false);
  const { data, error, refresh } = useResource<ProductPage>(
    initialProduct ? null : `/catalog/page?q=${encodeURIComponent(query)}&section_id=${section}&category_id=${category}&limit=12&offset=${offset}`,false,
  );
  const currentProduct = useResource<Product>(initialProduct ? `/catalog/${initialProduct.id}` : null, false);
  const products = initialProduct ? currentProduct.error ? undefined : [currentProduct.data ?? initialProduct] : data?.items;
  const pageError = initialProduct ? currentProduct.error : error;
  const retry = initialProduct ? currentProduct.refresh : refresh;
  const selectedCategory = categories.data?.find(c => String(c.id) === category);
  const [busy, setBusy] = useState<number | null>(null);
  const [notice, setNotice] = useState("");
  const [failed, setFailed] = useState(false);
  const [selectedCycles, setSelectedCycles] = useState<Record<number, string>>({});
  const [selectedOptions, setSelectedOptions] = useState<Record<number, Record<number, number>>>({});
  const [selectedQuantities, setSelectedQuantities] = useState<Record<number, Record<number, number>>>({});
  const [fieldValues,setFieldValues]=useState<Record<number,Record<string,string>>>({});
  function resolvedOptions(product: Product) { return resolveProductOptions(product.option_groups ?? [], selectedOptions[product.id]); }
  function optionIds(product: Product) { return resolvedOptions(product).ids; }
  function quantityFor(product: Product, group: NonNullable<Product["option_groups"]>[number], option: ProductOption) {
    const saved = selectedQuantities[product.id]?.[option.id];
    const minimum = optionQuantityBounds(group, option).min;
    return saved ?? minimum;
  }
  function optionQuantities(product: Product) {
    const result: Record<number, number> = {};
    for (const { group, selected, options } of resolvedOptions(product).branches) {
      const option = options.find((candidate) => candidate.id === selected);
      if (option && isQuantityOption(group)) result[option.id] = quantityFor(product, group, option);
    }
    return result;
  }
  function optionsComplete(product: Product) {
    return resolvedOptions(product).complete && previewPrice(product) !== null;
  }
  function previewPrice(product: Product) {
    const cycle = selectedCycles[product.id] ?? product.billing_cycle;
    const matrix = product.pricing?.find(price => price.billing_cycle === cycle && price.active);
    let price = matrix?.price_cents ?? product.price_cents;
    let setup = matrix?.setup_fee_cents ?? product.setup_fee_cents;
    for (const { group, selected, options } of resolvedOptions(product).branches) {
      const option = options.find(candidate => candidate.id === selected);
      if (!option) continue;
      const amounts = productOptionAmounts(option, cycle, isQuantityOption(group) ? quantityFor(product, group, option) : 1);
      if (!amounts) return null;
      price += amounts.price;
      setup += amounts.setup;
    }
    return Number.isSafeInteger(price) && Number.isSafeInteger(setup) && price >= 0 && setup >= 0 ? { price, setup } : null;
  }
  async function add(product: Product) {
    setBusy(product.id);
    setNotice("");
    setFailed(false);
    try {
      for (const group of product.option_groups ?? []) {
        const option = group.options.find(candidate => optionIds(product).includes(candidate.id));
        if (!option || !isQuantityOption(group)) continue;
        const value = quantityFor(product, group, option);
        const { min, max, step } = optionQuantityBounds(group, option);
        if (!Number.isSafeInteger(value) || value < min || value > max || (value - min) % step !== 0) {
          throw new Error(`${group.name} 数量不符合范围或步长`);
        }
      }
      const cart = await api<Cart>("/cart");
      await api("/cart/items", {
        method: "POST",
        body: JSON.stringify({
          version: cart.version,
          product_id: product.id,
          quantity: 1,
          billing_cycle: selectedCycles[product.id] ?? product.billing_cycle,
          option_ids: optionIds(product),
          option_quantities: optionQuantities(product),
          purchase_fields: purchaseFieldValues(product.purchase_field_definitions??[],fieldValues[product.id]??{}),
        }),
      });
      setNotice(`${product.name} 已加入购物车`);
    } catch (e) {
      setNotice((e as Error).message);
      setFailed(true);
    } finally {
      setBusy(null);
    }
  }
  return (
    <Workspace title={initialProduct?.name ?? "产品目录"}>
      <div className="flex flex-wrap items-center justify-between gap-4 mb-8">
        <div>
          <h1 className="page-title">{initialProduct?.name ?? "产品目录"}</h1>
          <p className="text-muted-foreground mt-2">云计算 / 通用服务</p>
        </div>
        <div className="flex gap-2">{initialProduct && <Button variant="outline" asChild><Link href="/"><ArrowLeft size={16} />返回目录</Link></Button>}<Button variant="outline" asChild>
          <Link href="/cart">
            <ShoppingCart size={16} />
            查看购物车
          </Link>
        </Button></div>
      </div>
      {!initialProduct && (sections.error || categories.error ? <Failure error={sections.error??categories.error!} retry={()=>{sections.refresh();categories.refresh();}} /> : <>
        <nav aria-label="一级商品分组" className="flex flex-wrap gap-2 mb-5"><Button variant={section===""?"default":"outline"} onClick={()=>{setSection("");setCategory("");setOffset(0);}}>全部商品</Button>{sections.data?.map(s=><Button key={s.id} variant={section===String(s.id)?"default":"outline"} onClick={()=>{setSection(String(s.id));setCategory("");setOffset(0);}}>{s.name}</Button>)}</nav>
        {section&&<p className="text-sm text-muted-foreground mb-5 whitespace-pre-wrap break-all">{sections.data?.find(s=>String(s.id)===section)?.description}</p>}
      </>)}
      {!initialProduct && <form
        className="grid sm:grid-cols-[1fr_2fr_auto] gap-3 mb-8"
        onSubmit={(e) => {
          e.preventDefault();
          setQuery(draft);
          setOffset(0);
        }}
      >
        <select aria-label="购买分类" className="h-9 min-w-0 w-full rounded-md border border-input bg-white px-3 text-sm" value={category} onChange={e=>{setCategory(e.target.value);setOffset(0);}}><option value="">全部分类</option>{!section&&<option value="0">未分类</option>}{categories.data?.filter(c=>!section||String(c.section_id)===section).map(c=><option key={c.id} value={c.id}>{c.name}</option>)}</select>
        <Input
          aria-label="搜索产品"
          placeholder="搜索产品名称"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
        />
        <Button variant="outline" size="icon" aria-label="搜索">
          <Search size={16} />
        </Button>
      </form>}
      {!initialProduct&&selectedCategory&&<section className="border rounded-md bg-white p-5 mb-6"><h2 className="text-xl font-semibold break-all">{selectedCategory.headline||selectedCategory.name}</h2>{selectedCategory.tagline&&<p className="text-primary mt-2 break-all">{selectedCategory.tagline}</p>}{selectedCategory.description&&<p className="text-sm text-muted-foreground mt-3 whitespace-pre-wrap break-all">{selectedCategory.description}</p>}</section>}
      {notice && (
        <div
          role={failed ? "alert" : "status"}
          className={`mb-6 p-3 border rounded-md ${failed ? "text-destructive border-red-200 bg-red-50" : "text-primary bg-emerald-50 border-emerald-200"}`}
        >
          {notice}
          {!failed && (
            <Link href="/cart" className="ml-4 underline">
              去结算
            </Link>
          )}
        </div>
      )}
      {pageError ? (
        <Failure error={pageError} retry={retry} />
      ) : !products ? (
        <Loading />
      ) : products.length === 0 ? (
        <p className="py-16 text-center text-muted-foreground">暂无可售产品</p>
      ) : (
        <div className={initialProduct ? "product-grid max-w-xl" : "product-grid"}>
          {products.map((product) => (
            <article key={product.id} className="product-tile">
              <div className="flex justify-between items-center mb-6">
                <span className="flex items-center justify-center w-11 h-11 rounded-md bg-emerald-50 text-primary">
                  <Server size={23} />
                </span>
                <span className="text-xs text-muted-foreground">
                  {product.stock === null ? "不限量" : `库存 ${product.stock}`}
                </span>
              </div>
              {initialProduct ? <h2 className="text-lg font-semibold mb-3 break-all">{product.name}</h2> : <h2 className="text-lg font-semibold mb-3 break-all"><Link href={`/products/${product.id}`} className="hover:text-primary">{product.name}</Link></h2>}
              <p className="text-muted-foreground leading-7 min-h-16 mb-6 whitespace-pre-wrap break-all">
                {product.description}
              </p>
              <div className="border-t pt-5 mb-5">
                {(product.pricing?.filter((price) => price.active).length ?? 0) > 1 && (
                  <Select
                    value={selectedCycles[product.id] ?? product.billing_cycle}
                    onValueChange={(value) => setSelectedCycles((current) => ({ ...current, [product.id]: value }))}
                  >
                    <SelectTrigger aria-label={`${product.name} 付款周期`} className="mb-3">
                      <SelectValue placeholder="选择付款周期" />
                    </SelectTrigger>
                    <SelectContent>
                      {product.pricing?.filter((price) => price.active).map((price) => (
                        <SelectItem key={price.billing_cycle} value={price.billing_cycle}>
                          {cycles[price.billing_cycle] ?? price.billing_cycle} · {money(price.price_cents + price.setup_fee_cents)}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
                {resolvedOptions(product).branches.map(({ group, options: activeOptions, selected }) => {
                  return (
                    <div key={group.id} className="mb-3">
                      <label className="text-xs text-muted-foreground mb-1 block">{group.name}{group.required ? "（必选）" : ""}</label>
                      <ProductOptionChoice group={group} options={activeOptions} selected={selected} label={`${product.name} ${group.name}`}
                        onChange={(id) => setSelectedOptions((current) => ({ ...current, [product.id]: changeProductOption(product.option_groups ?? [], resolveProductOptions(product.option_groups ?? [], current[product.id]).selected, group.id, id) }))}
                        renderLabel={(option) => { const cycle = selectedCycles[product.id] ?? product.billing_cycle; const amounts = productOptionAmounts(option, cycle, isQuantityOption(group) ? quantityFor(product, group, option) : 1); if (!amounts) return `${option.label} · 不可计价`; const delta = amounts.price + amounts.setup; return `${option.label}${delta !== 0 ? ` · ${money(delta)}` : ""}`; }} />
                      {!!selected && isQuantityOption(group) && (() => {
                        const option = activeOptions.find((candidate) => candidate.id === selected);
                        if (!option) return null;
                        const { min, max, step } = optionQuantityBounds(group, option);
                        return <label className="block text-xs text-muted-foreground mt-2">数量<Input type="number" min={min} max={max} step={step} value={quantityFor(product, group, option)} onChange={(event) => setSelectedQuantities((current) => ({ ...current, [product.id]: { ...current[product.id], [option.id]: Number(event.target.value) } }))} /></label>;
                      })()}
                    </div>
                  );
                })}
                <span className="text-2xl font-semibold money">
                {previewPrice(product) ? money(previewPrice(product)!.price) : "价格不可用"}
                </span>
                <span className="text-muted-foreground ml-1">
                  / {cycles[selectedCycles[product.id] ?? product.billing_cycle]}
                </span>
                {(previewPrice(product)?.setup ?? 0) > 0 && (
                  <p className="text-xs mt-2">
                    初装费 {money(previewPrice(product)!.setup)}
                  </p>
                )}
              </div>
              <PurchaseFieldInputs fields={product.purchase_field_definitions??[]} values={fieldValues[product.id]??{}} onChange={values=>setFieldValues({...fieldValues,[product.id]:values})} disabled={busy!==null}/>
              <Button
                className="w-full"
                disabled={busy !== null || product.stock === 0 || !optionsComplete(product)}
                onClick={() => add(product)}
              >
                {busy === product.id ? (
                  <Check size={16} />
                ) : (
                  <ShoppingCart size={16} />
                )}{" "}
                {busy === product.id ? "加入中" : "加入购物车"}
              </Button>
            </article>
          ))}
        </div>
      )}
      {!initialProduct&&data&&!error&&<div className="flex flex-wrap justify-between gap-3 items-center text-sm mt-6"><span>{data.total} 个可售商品</span><div className="flex gap-2"><Button variant="outline" disabled={offset===0} onClick={()=>setOffset(v=>Math.max(0,v-12))}>上一页</Button><Button variant="outline" disabled={offset+12>=data.total} onClick={()=>setOffset(v=>v+12)}>下一页</Button></div></div>}
    </Workspace>
  );
}
