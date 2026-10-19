"use client";
import { useState } from "react";
import Link from "next/link";
import { ArrowRight, Search, Server } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { money, type Product, type ProductPage, type CatalogCategory, type CatalogSection } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace } from "./workspace";
import { cycles, Failure, Loading } from "./common";

function ProductCard({ product }: { product: Product }) {
  const price = product.pricing?.find(item => item.active && item.billing_cycle === product.billing_cycle);
  const amount = price?.price_cents ?? product.price_cents;
  const setup = price?.setup_fee_cents ?? product.setup_fee_cents;
  const configurable = !!product.option_groups?.some(group => group.active);
  return <article className="product-tile flex min-w-0 flex-col">
    <div className="mb-5 flex items-center justify-between gap-3"><span className="flex size-11 items-center justify-center rounded-md bg-emerald-50 text-primary"><Server size={23} /></span><span className="text-xs text-muted-foreground">{product.stock === null ? "库存不限量" : product.stock === 0 ? "暂时售罄" : `库存 ${product.stock}`}</span></div>
    <h2 className="mb-3 text-lg font-semibold break-words"><Link href={`/products/${product.id}`} className="hover:text-primary">{product.name}</Link></h2>
    <p className="mb-6 line-clamp-4 min-h-16 whitespace-pre-wrap break-words text-sm leading-7 text-muted-foreground">{product.description || "查看商品详情，选择适合的服务配置。"}</p>
    <div className="mt-auto border-t pt-5">
      <p className="mb-1 text-xs text-muted-foreground">{configurable ? "基础周期价" : "周期参考价"}</p>
      <div><strong className="money text-2xl font-semibold">{money(amount)}</strong><span className="ml-1 text-sm text-muted-foreground">起 / {cycles[product.billing_cycle] ?? product.billing_cycle}</span></div>
      <p className="mt-2 mb-5 min-h-8 text-xs leading-5 text-muted-foreground">{configurable ? "配置加价与初装费另计，进入详情后选择。" : setup > 0 ? `另收初装费 ${money(setup)} / 件` : "具体购买金额以购物车报价为准。"}</p>
      <Button className="w-full" variant={product.stock === 0 ? "outline" : "default"} asChild><Link href={`/products/${product.id}`}>{product.stock === 0 ? "查看商品详情" : "配置购买"}<ArrowRight size={16} /></Link></Button>
    </div>
  </article>;
}

export function CatalogView() {
  const [query, setQuery] = useState("");
  const [draft, setDraft] = useState("");
  const [section, setSection] = useState("");
  const [category, setCategory] = useState("");
  const [offset, setOffset] = useState(0);
  const sections = useResource<Pick<CatalogSection, "id" | "name" | "description">[]>("/catalog/sections", false);
  const categories = useResource<Pick<CatalogCategory, "id" | "name" | "section_id" | "description" | "headline" | "tagline">[]>("/categories", false);
  const products = useResource<ProductPage>(`/catalog/page?q=${encodeURIComponent(query)}&section_id=${section}&category_id=${category}&limit=12&offset=${offset}`, false);
  const selectedCategory = categories.data?.find(item => String(item.id) === category);
  return <Workspace title="产品目录">
    <div className="mb-8"><h1 className="page-title">产品目录</h1><p className="mt-2 text-muted-foreground">先选择商品，再配置规格与付款周期。</p></div>
    {sections.error || categories.error ? <Failure error={sections.error ?? categories.error!} retry={() => { sections.refresh(); categories.refresh(); }} /> : <>
      <nav aria-label="一级商品分组" className="mb-5 flex flex-wrap gap-2"><Button variant={section === "" ? "default" : "outline"} onClick={() => { setSection(""); setCategory(""); setOffset(0); }}>全部商品</Button>{sections.data?.map(item => <Button key={item.id} variant={section === String(item.id) ? "default" : "outline"} onClick={() => { setSection(String(item.id)); setCategory(""); setOffset(0); }}>{item.name}</Button>)}</nav>
      {section && <p className="mb-5 whitespace-pre-wrap break-words text-sm text-muted-foreground">{sections.data?.find(item => String(item.id) === section)?.description}</p>}
    </>}
    <form className="mb-8 grid gap-3 sm:grid-cols-[1fr_2fr_auto]" onSubmit={event => { event.preventDefault(); setQuery(draft.trim()); setOffset(0); }}>
      <select aria-label="购买分类" className="h-9 min-w-0 w-full rounded-md border border-input bg-white px-3 text-sm" value={category} onChange={event => { setCategory(event.target.value); setOffset(0); }}><option value="">全部分类</option>{!section && <option value="0">未分类</option>}{categories.data?.filter(item => !section || String(item.section_id) === section).map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select>
      <Input aria-label="搜索产品" placeholder="搜索产品名称" value={draft} onChange={event => setDraft(event.target.value)} />
      <Button variant="outline" size="icon" aria-label="搜索"><Search size={16} /></Button>
    </form>
    {selectedCategory && <section className="mb-6 rounded-md border bg-white p-5"><h2 className="text-xl font-semibold break-words">{selectedCategory.headline || selectedCategory.name}</h2>{selectedCategory.tagline && <p className="mt-2 break-words text-primary">{selectedCategory.tagline}</p>}{selectedCategory.description && <p className="mt-3 whitespace-pre-wrap break-words text-sm text-muted-foreground">{selectedCategory.description}</p>}</section>}
    {products.error ? <Failure error={products.error} retry={products.refresh} /> : !products.data ? <Loading /> : !products.data.items.length ? <section className="py-16 text-center"><p className="text-muted-foreground">暂无符合条件的可售产品</p>{(query || section || category || offset > 0) && <Button className="mt-4" variant="outline" onClick={() => { setQuery(""); setDraft(""); setSection(""); setCategory(""); setOffset(0); }}>查看全部商品</Button>}</section> : <div className="product-grid">{products.data.items.map(product => <ProductCard key={product.id} product={product} />)}</div>}
    {products.data && !products.error && <div className="mt-6 flex flex-wrap items-center justify-between gap-3 text-sm"><span>{products.data.total} 个可售商品</span><div className="flex gap-2"><Button variant="outline" disabled={offset === 0} onClick={() => setOffset(value => Math.max(0, value - 12))}>上一页</Button><Button variant="outline" disabled={offset + 12 >= products.data.total} onClick={() => setOffset(value => value + 12)}>下一页</Button></div></div>}
  </Workspace>;
}
