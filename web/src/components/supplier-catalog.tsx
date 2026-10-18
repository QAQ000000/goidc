"use client";

import { useRef, useState } from "react";
import Link from "next/link";
import { RefreshCw } from "lucide-react";
import { api, money, type SupplierAccount, type SupplierProduct, type SupplierProvider } from "@/lib/api";
import { supplierAccountIssue } from "@/lib/supplier-configuration";
import { useResource } from "@/hooks/use-resource";
import { Workspace, useAdminPermission } from "./workspace";
import { Failure, Loading, cycles } from "./common";
import { ImportProduct, unsupportedLabel } from "./suppliers-admin";
import { AdminPagination } from "./admin-pagination";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Table, TableHeader, TableHead, TableBody, TableRow, TableCell } from "./ui/table";

export function SupplierCatalogView() {
  return <Workspace admin title="供应商商品目录"><Catalog /></Workspace>;
}

function Catalog() {
  const manage = useAdminPermission("products.manage");
  const accounts = useResource<SupplierAccount[]>("/admin/suppliers", false);
  const providers = useResource<SupplierProvider[]>("/admin/supplier-providers", false);
  const [accountID, setAccountID] = useState("");
  const account = accounts.data?.find(item => String(item.id) === accountID);
  const provider = providers.data?.find(item => item.id === account?.provider);
  const unavailable = account ? supplierAccountIssue(account, provider, "catalog") : "";
  const [catalog, setCatalog] = useState<SupplierProduct[] | null>(null);
  const [productID, setProductID] = useState<number | null>(null);
  const [keyword, setKeyword] = useState("");
  const [support, setSupport] = useState("all");
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState(25);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const request = useRef(0);
  const detail = useResource<SupplierProduct>(manage && account && productID && !supplierAccountIssue(account, provider, "product") ? `/admin/suppliers/${account.id}/products/${productID}` : null, false);
  function clear() { request.current++; setBusy(false); setCatalog(null); setProductID(null); setKeyword(""); setSupport("all"); setPage(0); setError(""); }
  async function loadCatalog() {
    if (!manage || !account || busy || unavailable) return;
    const id = ++request.current;
    setBusy(true); setCatalog(null); setProductID(null); setPage(0); setError("");
    try {
      const items = await api<SupplierProduct[]>(`/admin/suppliers/${account.id}/catalog`, { method: "POST" });
      if (id === request.current) setCatalog(items);
    } catch (cause) { if (id === request.current) setError((cause as Error).message); }
    finally { if (id === request.current) setBusy(false); }
  }
  const filtered = (catalog ?? []).filter(item => `${item.name} ${item.id}`.toLowerCase().includes(keyword.trim().toLowerCase()) && (support === "all" || (support === "supported" ? item.supported || item.unsupported_reason === "detail_required" : !item.supported && item.unsupported_reason !== "detail_required")));
  const currentPage = Math.min(page, Math.max(0, Math.ceil(filtered.length / pageSize) - 1));
  const visible = filtered.slice(currentPage * pageSize, (currentPage + 1) * pageSize);
  return <div className="space-y-6"><div className="flex flex-wrap items-center justify-between gap-3"><div><h1 className="page-title">供应商商品目录</h1><p className="mt-2 text-sm text-muted-foreground">选择供应商后手动读取上游目录，再配置并导入本地商品。</p></div><Button asChild variant="outline"><Link href="/admin/suppliers">供应商账户</Link></Button></div>
    {accounts.error || providers.error ? <Failure error={accounts.error ?? providers.error!} retry={()=>{accounts.refresh();providers.refresh();}} /> : !accounts.data || !providers.data ? <Loading /> : accounts.data.length === 0 ? <section className="border-y py-12 text-center text-muted-foreground"><p>暂无供应商账户，暂时无法读取商品目录。</p>{manage && <Button asChild variant="outline" className="mt-4"><Link href="/admin/suppliers">添加供应商</Link></Button>}</section> : <>
      <section className="space-y-3"><div className="flex flex-wrap items-end gap-3"><label className="grid gap-2 text-sm">供应商<select aria-label="选择供应商" className="h-9 w-full rounded-md border bg-background px-3 sm:w-72" value={accountID} onChange={event => { clear(); setAccountID(event.target.value); }}><option value="">请选择供应商</option>{accounts.data.map(item => <option key={item.id} value={item.id}>{item.name}{!item.active ? "（停用）" : !item.credentials_configured ? "（凭据未配置）" : ""}</option>)}</select></label><Button disabled={!manage || !account || busy || !!unavailable} onClick={() => void loadCatalog()}><RefreshCw size={16} />{busy ? "读取中…" : "只读拉取上游目录"}</Button><Button variant="outline" disabled={busy} onClick={() => { clear(); setAccountID(""); accounts.refresh(); providers.refresh(); }}>刷新账户</Button></div>
        {!manage && <p className="text-sm text-muted-foreground">当前账户可查看本地供应商列表。读取上游目录、商品详情和导入需要商品管理权限。</p>}
        {account && <p className="text-xs text-muted-foreground">{account.name} · {account.active ? "已启用" : "已停用"} · {account.credentials_configured ? "凭据已配置" : "凭据未配置"} · 账户版本 {account.version} · 本地商品 {account.product_count} 个。目录读取不会开通服务。</p>}
        {account && unavailable && <p role="status" className="text-sm text-amber-700">{unavailable}</p>}{!account && <p className="text-sm text-muted-foreground">尚未选择供应商。</p>}
      </section>
      {error && <p role="alert" className="break-words text-sm text-destructive">{error}</p>}
      {busy ? <Loading /> : catalog === null ? account && <p className="border-y py-10 text-center text-muted-foreground">尚未读取该供应商的上游目录。</p> : <section className="space-y-4"><div className="flex flex-wrap gap-3"><Input aria-label="搜索上游商品" className="sm:max-w-sm" placeholder="商品名称或上游 ID" value={keyword} onChange={event => { setKeyword(event.target.value); setPage(0); }} /><select aria-label="目录导入状态" className="h-9 rounded-md border bg-background px-3" value={support} onChange={event => { setSupport(event.target.value); setPage(0); }}><option value="all">全部商品</option><option value="supported">可导入或待配置</option><option value="unsupported">不支持导入</option></select>{(keyword || support !== "all") && <Button variant="outline" onClick={() => { setKeyword(""); setSupport("all"); setPage(0); }}>重置筛选</Button>}</div>
        <div className="overflow-x-auto border-y"><Table><TableHeader><TableRow><TableHead>商品 / 上游 ID</TableHead><TableHead>供应商库存</TableHead><TableHead>上游定价</TableHead><TableHead>导入状态</TableHead><TableHead>操作</TableHead></TableRow></TableHeader><TableBody>{visible.map(item => <TableRow key={item.id}><TableCell className="min-w-48"><p className="font-medium break-words">{item.name}</p><p className="mt-1 text-xs text-muted-foreground">#{item.id}</p></TableCell><TableCell>{item.stock ?? "未提供"}</TableCell><TableCell className="min-w-40">{Object.entries(item.prices ?? {}).map(([cycle, price]) => <p key={cycle} className="text-xs">{cycles[cycle] ?? cycle} {money(price.amount_cents)} · 初装费 {money(price.setup_cents)}</p>)}{!Object.keys(item.prices ?? {}).length && <span className="text-xs text-muted-foreground">查看配置后获取</span>}</TableCell><TableCell className="text-xs">{item.supported ? "可配置导入" : unsupportedLabel(item.unsupported_reason)}</TableCell><TableCell><Button variant="outline" size="sm" disabled={!manage || !provider?.capabilities.product || (!item.supported && item.unsupported_reason !== "detail_required")} onClick={() => setProductID(item.id)}>配置导入</Button></TableCell></TableRow>)}{visible.length === 0 && <TableRow><TableCell colSpan={5} className="py-10 text-center text-muted-foreground">{catalog.length === 0 ? "上游目录为空" : "没有符合筛选条件的商品"}</TableCell></TableRow>}</TableBody></Table></div>
        <AdminPagination total={filtered.length} page={currentPage} pageSize={pageSize} onPageChange={setPage} onPageSizeChange={size => { setPageSize(size); setPage(0); }} />
      </section>}
      {manage && account && productID && provider?.capabilities.product && <section className="space-y-3"><div className="flex flex-wrap justify-between gap-3"><h2 className="font-semibold">商品导入配置</h2><Button variant="outline" onClick={() => setProductID(null)}>关闭配置</Button></div>{detail.error ? <Failure error={detail.error} retry={detail.refresh} /> : !detail.data ? <Loading /> : !provider.capabilities.create ? <p role="status" className="text-sm text-amber-700">此供应商未提供开通能力，暂不能导入为销售商品。</p> : <ImportProduct key={`${account.id}:${productID}:${detail.data.revision}`} account={account} product={detail.data} />}</section>}
    </>}
  </div>;
}
