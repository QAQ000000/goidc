"use client";
import Link from "next/link";
import { ArrowLeft, RefreshCw } from "lucide-react";
import { ApiError, type Product } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Button } from "./ui/button";
import { Workspace } from "./workspace";
import { Failure, Loading } from "./common";
import { ProductPurchaseForm } from "./product-purchase-form";

export function ProductPurchaseView({ initialProduct }: { initialProduct: Product }) {
  const resource = useResource<Product>(`/catalog/${initialProduct.id}`, false);
  const product = resource.data ?? initialProduct;
  const missing = resource.error instanceof ApiError && resource.error.status === 404;
  return <Workspace title={product.name}>
    <Link href="/" className="mb-6 inline-flex items-center gap-2 text-sm text-primary"><ArrowLeft size={16} />返回产品目录</Link>
    <div className="mb-8 flex min-w-0 flex-wrap items-start justify-between gap-4"><div className="min-w-0"><p className="mb-2 text-xs text-muted-foreground">{product.category_name || "云服务"} · 商品详情</p><h1 className="page-title break-words">{product.name}</h1><p className="mt-3 max-w-3xl whitespace-pre-wrap break-words text-sm leading-7 text-muted-foreground">{product.description || "选择付款周期和服务规格，确认后加入购物车。"}</p></div><Button type="button" variant="outline" size="sm" onClick={resource.refresh}><RefreshCw size={15} />刷新商品</Button></div>
    {missing ? <section className="data-surface p-8 text-center" role="alert"><h2 className="font-semibold">商品已下架或暂不可见</h2><p className="mt-3 text-sm text-muted-foreground">请返回目录选择其他商品，或稍后重新检查。</p><Button type="button" className="mt-4" variant="outline" onClick={resource.refresh}>重新检查</Button></section> : resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !product ? <Loading /> : <ProductPurchaseForm key={`${product.id}:${product.revision}`} product={product} />}
  </Workspace>;
}
