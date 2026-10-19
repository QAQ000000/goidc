"use client";

import { useRef, useState } from "react";
import Link from "next/link";
import { RefreshCw } from "lucide-react";
import { api, type SupplierAccount, type SupplierProduct, type SupplierProvider } from "@/lib/api";
import { supplierAccountIssue } from "@/lib/supplier-configuration";
import { useResource } from "@/hooks/use-resource";
import { Workspace, useAdminPermission } from "./workspace";
import { Failure, Loading } from "./common";
import { ImportProduct } from "./suppliers-admin";
import { SupplierBatchImport } from "./supplier-batch-import";
import { Button } from "./ui/button";

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
  const [batchLocked, setBatchLocked] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const request = useRef(0);
  const detail = useResource<SupplierProduct>(manage && account && productID && !supplierAccountIssue(account, provider, "product") ? `/admin/suppliers/${account.id}/products/${productID}` : null, false);
  function clear() { request.current++; setBusy(false); setCatalog(null); setProductID(null); setError(""); }
  async function loadCatalog() {
    if (!manage || !account || busy || batchLocked || unavailable) return;
    const id = ++request.current;
    setBusy(true); setCatalog(null); setProductID(null); setError("");
    try {
      const items = await api<SupplierProduct[]>(`/admin/suppliers/${account.id}/catalog`, { method: "POST" });
      if (id === request.current) setCatalog(items);
    } catch (cause) { if (id === request.current) setError((cause as Error).message); }
    finally { if (id === request.current) setBusy(false); }
  }
  return <div className="space-y-6"><div className="flex flex-wrap items-center justify-between gap-3"><div><h1 className="page-title">供应商商品目录</h1><p className="mt-2 text-sm text-muted-foreground">选择上游商品，设置售价比例与本地分组，预览后批量导入。</p></div><Button asChild variant="outline"><Link href="/admin/suppliers">供应商账户</Link></Button></div>
    {accounts.error || providers.error ? <Failure error={accounts.error ?? providers.error!} retry={()=>{accounts.refresh();providers.refresh();}} /> : !accounts.data || !providers.data ? <Loading /> : accounts.data.length === 0 ? <section className="border-y py-12 text-center text-muted-foreground"><p>暂无供应商账户，暂时无法读取商品目录。</p>{manage && <Button asChild variant="outline" className="mt-4"><Link href="/admin/suppliers">添加供应商</Link></Button>}</section> : <>
      <section className="space-y-3"><div className="flex flex-wrap items-end gap-3"><label className="grid gap-2 text-sm">供应商<select aria-label="选择供应商" className="h-9 w-full rounded-md border bg-background px-3 sm:w-72" value={accountID} disabled={busy || batchLocked} onChange={event => { if (busy || batchLocked) return; clear(); setAccountID(event.target.value); }}><option value="">请选择供应商</option>{accounts.data.map(item => <option key={item.id} value={item.id}>{item.name}{!item.active ? "（停用）" : !item.credentials_configured ? "（凭据未配置）" : ""}</option>)}</select></label><Button disabled={!manage || !account || busy || batchLocked || !!unavailable} onClick={() => void loadCatalog()}><RefreshCw size={16} />{busy ? "读取中…" : "只读拉取上游目录"}</Button><Button variant="outline" disabled={busy || batchLocked} onClick={() => { if (busy || batchLocked) return; clear(); setAccountID(""); accounts.refresh(); providers.refresh(); }}>刷新账户</Button></div>
        {!manage && <p className="text-sm text-muted-foreground">当前账户可查看本地供应商列表。读取上游目录、商品详情和导入需要商品管理权限。</p>}
        {account && <p className="text-xs text-muted-foreground">{account.name} · {account.active ? "已启用" : "已停用"} · {account.credentials_configured ? "凭据已配置" : "凭据未配置"} · 账户版本 {account.version} · 本地商品 {account.product_count} 个。目录读取不会开通服务。</p>}
        {account && unavailable && <p role="status" className="text-sm text-amber-700">{unavailable}</p>}{!account && <p className="text-sm text-muted-foreground">尚未选择供应商。</p>}
      </section>
      {error && <p role="alert" className="break-words text-sm text-destructive">{error}</p>}
      {busy ? <Loading /> : catalog === null ? account && <p className="border-y py-10 text-center text-muted-foreground">尚未读取该供应商的上游目录。</p> : account && <SupplierBatchImport key={`${account.id}:${account.version}`} account={account} catalog={catalog} unavailable={supplierAccountIssue(account, provider, "product") || (!provider?.capabilities.create ? "此供应商未提供开通能力，暂不能导入为销售商品" : "")} onAdvanced={setProductID} onLockChange={setBatchLocked} onImported={accounts.refresh} />}
      {manage && account && productID && provider?.capabilities.product && !batchLocked && <section className="space-y-3"><div className="flex flex-wrap justify-between gap-3"><h2 className="font-semibold">高级固定规格导入</h2><Button variant="outline" onClick={() => setProductID(null)}>关闭配置</Button></div>{detail.error ? <Failure error={detail.error} retry={detail.refresh} /> : !detail.data ? <Loading /> : !provider.capabilities.create ? <p role="status" className="text-sm text-amber-700">此供应商未提供开通能力，暂不能导入为销售商品。</p> : <ImportProduct key={`${account.id}:${productID}:${detail.data.revision}`} account={account} product={detail.data} />}</section>}
    </>}
  </div>;
}
