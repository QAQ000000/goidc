"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import { Check, Download, Search } from "lucide-react";
import { ApiError, api, money, type CatalogCategory, type SupplierAccount, type SupplierBatchImportInput, type SupplierBatchImportPreview, type SupplierBatchImportResult, type SupplierProduct } from "@/lib/api";
import { usePendingSetting } from "@/hooks/use-pending-setting";
import { useResource } from "@/hooks/use-resource";
import { useAdminIdentity, useAdminPermission } from "./workspace";
import { AdminPagination } from "./admin-pagination";
import { Failure, Loading, cycles } from "./common";
import { unsupportedLabel } from "./suppliers-admin";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Input } from "./ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";

const maximumBatch = 50;
const selectStyle = "h-9 w-full rounded-md border bg-background px-3 text-sm";
const selectable = (product: SupplierProduct) => product.supported || product.unsupported_reason === "detail_required";
const groupKey = (product: SupplierProduct) => `${product.group_id ?? 0}:${product.group_name ?? ""}`;

export function supplierMultiplierBPS(value: string) {
  const match = value.trim().match(/^(\d{1,5})(?:\.(\d{1,2}))?$/);
  if (!match) throw new Error("售价比例请输入 0.01 至 10000 之间的百分比，最多两位小数");
  const result = Number(match[1]) * 100 + Number((match[2] ?? "").padEnd(2, "0"));
  if (result < 1 || result > 1000000) throw new Error("售价比例必须在 0.01% 至 10000% 之间");
  return result;
}

function matchesImportResult(response: unknown, originalBody: string): response is SupplierBatchImportResult {
  const record = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null && !Array.isArray(value);
  const positiveID = (value: unknown): value is number => typeof value === "number" && Number.isSafeInteger(value) && value > 0;
  let original: unknown;
  try { original = JSON.parse(originalBody); } catch { return false; }
  if (!record(original) || !Array.isArray(original.items) || !original.items.length || !record(response) || !Array.isArray(response.items) || response.items.length !== original.items.length) return false;
  const expected = new Set<number>();
  for (const item of original.items) {
    if (!record(item) || !positiveID(item.upstream_product_id) || expected.has(item.upstream_product_id)) return false;
    expected.add(item.upstream_product_id);
  }
  const returned = new Set<number>();
  const productIDs = new Set<number>();
  for (const item of response.items) {
    if (!record(item) || !positiveID(item.upstream_product_id) || !expected.has(item.upstream_product_id) || returned.has(item.upstream_product_id) || !positiveID(item.product_id) || productIDs.has(item.product_id) || typeof item.name !== "string" || !item.name.trim() || typeof item.slug !== "string" || !item.slug.trim() || item.status !== "created" && item.status !== "existing") return false;
    returned.add(item.upstream_product_id);
    productIDs.add(item.product_id);
  }
  return true;
}

function PriceLines({ prices }: { prices?: SupplierProduct["prices"] }) {
  const entries = Object.entries(prices ?? {});
  return entries.length ? <>{entries.map(([cycle, price]) => <p key={cycle} className="text-xs leading-6">{cycles[cycle] ?? cycle} {money(price.amount_cents)} · 初装费 {money(price.setup_cents)}</p>)}</> : <span className="text-xs text-muted-foreground">预览时读取完整价格</span>;
}

export function SupplierBatchImport({ account, catalog, unavailable, onAdvanced, onLockChange, onImported }: {
  account: SupplierAccount;
  catalog: SupplierProduct[];
  unavailable?: string;
  onAdvanced?: (productID: number) => void;
  onLockChange?: (locked: boolean) => void;
  onImported?: () => void;
}) {
  const actor = useAdminIdentity();
  const manage = useAdminPermission("products.manage");
  const pending = usePendingSetting("supplier-batch-import", actor?.id ?? null);
  const categories = useResource<CatalogCategory[]>("/admin/catalog-groups/category", false);
  const [selected, setSelected] = useState<number[]>([]);
  const [keyword, setKeyword] = useState("");
  const [group, setGroup] = useState("");
  const [support, setSupport] = useState("all");
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState(25);
  const [percentage, setPercentage] = useState("120");
  const [categoryID, setCategoryID] = useState("");
  const [active, setActive] = useState(true);
  const [preview, setPreview] = useState<{ identity: string; response: SupplierBatchImportPreview; input: SupplierBatchImportInput } | null>(null);
  const [result, setResult] = useState<SupplierBatchImportResult | null>(null);
  const [busy, setBusy] = useState<"preview" | "import" | null>(null);
  const [error, setError] = useState("");
  const [forbidden, setForbidden] = useState(false);
  const inFlight = useRef(false);
  const generation = useRef(0);
  const identity = `${account.id}:${account.version}`;
  const currentIdentity = useRef(identity);
  const locked = busy !== null || pending.unknown;
  const canWrite = manage && !forbidden && !unavailable && account.active;

  useEffect(() => { currentIdentity.current = identity; }, [identity]);
  useEffect(() => {
    onLockChange?.(locked);
    return () => onLockChange?.(false);
  }, [locked, onLockChange]);
  useEffect(() => () => { generation.current++; }, [identity]);

  const filtered = catalog.filter(product => `${product.name} ${product.id} ${product.group_name ?? ""}`.toLowerCase().includes(keyword.trim().toLowerCase()) && (!group || groupKey(product) === group) && (support === "all" || (support === "supported" ? selectable(product) : !selectable(product))));
  const eligibleFiltered = filtered.filter(selectable);
  const currentPage = Math.min(page, Math.max(0, Math.ceil(filtered.length / pageSize) - 1));
  const visible = filtered.slice(currentPage * pageSize, (currentPage + 1) * pageSize);
  const selectedSet = new Set(selected);
  const groups = Array.from(new Map(catalog.map(product => [groupKey(product), product.group_name || "未分组"])).entries());
  const allFilteredSelected = eligibleFiltered.length > 0 && eligibleFiltered.every(product => selectedSet.has(product.id));
  const hasUnimportable = preview?.response.items.some(item => !item.importable || !item.product.revision) ?? false;
  const originalPath = pending.pending.current?.path;
  const pendingAccountID = originalPath?.match(/^\/admin\/suppliers\/(\d+)\/import-batch$/)?.[1];
  const currentPreview = preview?.identity === identity ? preview : null;

  function invalidate() { generation.current++; setPreview(null); setError(""); setResult(null); }
  function toggle(id: number, checked: boolean) {
    if (!canWrite || locked) return;
    if (checked && !selectedSet.has(id) && selected.length >= maximumBatch) { setError(`单批最多导入 ${maximumBatch} 个商品，请分批操作`); return; }
    invalidate(); setSelected(current => checked ? [...new Set([...current, id])] : current.filter(value => value !== id));
  }
  function toggleFiltered() {
    if (!canWrite || locked) return;
    const ids = new Set(eligibleFiltered.map(product => product.id));
    const next = allFilteredSelected ? selected.filter(id => !ids.has(id)) : [...new Set([...selected, ...ids])];
    if (next.length > maximumBatch) { setError(`当前筛选与已选商品合计 ${next.length} 个，单批最多 ${maximumBatch} 个，请缩小筛选范围`); return; }
    invalidate(); setSelected(next);
  }
  async function loadPreview(event: React.FormEvent) {
    event.preventDefault();
    if (!canWrite || inFlight.current || pending.unknown || !pending.ready || !selected.length || !categories.data) return;
    let input: SupplierBatchImportInput;
    try {
      if (selected.length > maximumBatch || selected.some(id => !catalog.some(product => product.id === id && selectable(product)))) throw new Error("已选商品已变化，请重新选择上游商品");
      if (categoryID && !categories.data.some(category => category.id === Number(categoryID))) throw new Error("本地商品分组已变化，请重新选择");
      input = { expected_supplier_version: account.version, category_id: categoryID ? Number(categoryID) : null, price_multiplier_bps: supplierMultiplierBPS(percentage), active, items: selected.map(id => ({ upstream_product_id: id })) };
    } catch (cause) { setError((cause as Error).message); return; }
    const request = ++generation.current;
    const source = identity;
    inFlight.current = true; setBusy("preview"); setError(""); setPreview(null); setResult(null);
    try {
      const response = await api<SupplierBatchImportPreview>(`/admin/suppliers/${account.id}/import-preview`, { method: "POST", body: JSON.stringify(input) });
      if (request !== generation.current || source !== currentIdentity.current) return;
      if (response.expected_supplier_version !== input.expected_supplier_version || response.price_multiplier_bps !== input.price_multiplier_bps || response.category_id !== input.category_id || response.active !== input.active || response.items.length !== input.items.length || response.items.some((item, index) => item.product.id !== input.items[index].upstream_product_id)) throw new Error("预览响应与当前选择不一致，请重新读取目录后预览");
      setPreview({ identity: source, response, input });
    } catch (cause) {
      if (request === generation.current && source === currentIdentity.current) { if (cause instanceof ApiError && cause.status === 403) setForbidden(true); setError((cause as Error).message); }
    } finally { if (source === currentIdentity.current) { inFlight.current = false; setBusy(null); } }
  }
  async function confirm() {
    if (!manage || forbidden || inFlight.current || !pending.ready) return;
    let operation = pending.pending.current;
    if (!pending.unknown) {
      if (!canWrite || !currentPreview || hasUnimportable) return;
      const body: SupplierBatchImportInput = { ...currentPreview.input, items: currentPreview.input.items.map((item, index) => ({ ...item, expected_upstream_revision: currentPreview.response.items[index].product.revision })) };
      operation = { path: `/admin/suppliers/${account.id}/import-batch`, key: `supplier-import-${crypto.randomUUID()}`, body: JSON.stringify(body) };
      pending.remember(operation);
    }
    if (!operation?.path || !/^\/admin\/suppliers\/\d+\/import-batch$/.test(operation.path)) { setError("找不到可恢复的原导入请求，请重新进入供应商目录核对"); return; }
    const source = identity;
    inFlight.current = true; setBusy("import"); setError("");
    try {
      const response = await api<SupplierBatchImportResult>(operation.path, { method: "POST", headers: { "Idempotency-Key": operation.key }, body: operation.body });
      if (!matchesImportResult(response, operation.body)) throw new Error("导入响应不完整，需重试原请求核对结果");
      pending.resolve();
      if (source === currentIdentity.current) { setResult(response); setPreview(null); setSelected([]); onImported?.(); }
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 403) setForbidden(true);
      if (pending.unknown || !(cause instanceof ApiError) || cause.status >= 500 || cause.status === 408) {
        pending.setUnknown(true); setError("导入结果尚未确认。已保留原请求，请重试原请求确认结果。");
      } else {
        pending.resolve(); setPreview(null); setError(`${cause.message}。请重新预览后确认。`);
      }
    } finally { inFlight.current = false; if (source === currentIdentity.current) setBusy(null); }
  }

  return <section className="space-y-5" aria-label="批量导入供应商商品">
    <div><h2 className="text-lg font-semibold">选择上游商品</h2><p className="mt-1 text-sm text-muted-foreground">支持跨页选择。设置统一售价比例与本地分组后预览，一次导入商品的全部可用账期和支持的客户可选规格。</p></div>
    {unavailable && <p role="status" className="text-sm text-amber-700">{unavailable}</p>}
    {!manage && <p role="status" className="text-sm text-muted-foreground">当前账户没有商品管理权限，无法预览或导入。</p>}
    {forbidden && <p role="status" className="text-sm text-amber-700">商品管理权限已变化，请刷新账户权限后继续。</p>}
    {pending.unknown && <div role="status" className="space-y-3 border-y border-amber-200 bg-amber-50 p-4 text-sm"><p>上次批量导入结果尚未确认{pendingAccountID ? `（供应商 #${pendingAccountID}）` : ""}。已保留原商品、比例、分组和请求标识，确认前不能修改内容或开始另一批导入。</p><Button type="button" variant="outline" disabled={!manage || forbidden || busy !== null || !pending.ready} onClick={() => void confirm()}>重试原导入请求</Button></div>}
    <fieldset disabled={locked || !canWrite} className="min-w-0 w-full space-y-4">
      <div className="grid gap-3 sm:grid-cols-[minmax(12rem,1fr)_minmax(10rem,14rem)_minmax(10rem,14rem)]"><label className="grid min-w-0 gap-2 text-sm">搜索上游商品<Input aria-label="搜索上游商品" placeholder="商品名称、上游 ID 或分组" value={keyword} onChange={event => { setKeyword(event.target.value); setPage(0); }} /></label><label className="grid min-w-0 gap-2 text-sm">上游商品分组<select aria-label="上游商品分组" className={selectStyle} value={group} onChange={event => { setGroup(event.target.value); setPage(0); }}><option value="">全部分组</option>{groups.map(([key, name]) => <option key={key} value={key}>{name}</option>)}</select></label><label className="grid min-w-0 gap-2 text-sm">导入状态<select aria-label="目录导入状态" className={selectStyle} value={support} onChange={event => { setSupport(event.target.value); setPage(0); }}><option value="all">全部商品</option><option value="supported">可导入或待预览</option><option value="unsupported">不支持导入</option></select></label></div>
      <div className="flex flex-wrap items-center gap-3 text-sm"><Button type="button" variant="outline" disabled={!eligibleFiltered.length || !canWrite || locked} onClick={toggleFiltered}>{allFilteredSelected ? "取消选择当前筛选" : "全选当前筛选"}</Button><span role="status">已选 {selected.length} 个 / 单批最多 {maximumBatch} 个</span>{selected.length > 0 && <Button type="button" variant="ghost" onClick={() => { invalidate(); setSelected([]); }}>清空选择</Button>}{(keyword || group || support !== "all") && <Button type="button" variant="ghost" onClick={() => { setKeyword(""); setGroup(""); setSupport("all"); setPage(0); }}>重置筛选</Button>}</div>
      <div className="overflow-x-auto border-y"><Table><TableHeader><TableRow><TableHead className="w-12">选择</TableHead><TableHead>商品 / 上游 ID</TableHead><TableHead>上游分组</TableHead><TableHead>库存</TableHead><TableHead>上游基础价格</TableHead><TableHead>状态 / 操作</TableHead></TableRow></TableHeader><TableBody>{visible.map(product => <TableRow key={product.id}><TableCell><Checkbox aria-label={`选择上游商品 ${product.name} #${product.id}`} disabled={!selectable(product) || !canWrite || locked} checked={selectedSet.has(product.id)} onCheckedChange={checked => toggle(product.id, checked === true)} /></TableCell><TableCell className="min-w-44"><p className="font-medium break-words">{product.name}</p><p className="mt-1 text-xs text-muted-foreground">#{product.id}{product.type ? ` · ${product.type}` : ""}</p></TableCell><TableCell className="text-xs">{product.group_name || "未分组"}</TableCell><TableCell>{product.stock ?? "未提供"}</TableCell><TableCell className="min-w-52"><PriceLines prices={product.prices} /></TableCell><TableCell className="min-w-40 text-xs"><p>{product.supported ? "可预览导入" : product.unsupported_reason === "detail_required" ? "预览后核对支持范围" : unsupportedLabel(product.unsupported_reason)}</p>{onAdvanced && selectable(product) && <Button type="button" size="sm" variant="link" className="mt-1 h-auto p-0 text-xs" disabled={locked || !canWrite} onClick={() => onAdvanced(product.id)}>高级固定规格导入</Button>}</TableCell></TableRow>)}{visible.length === 0 && <TableRow><TableCell colSpan={6} className="py-10 text-center text-muted-foreground">{catalog.length ? "没有符合筛选条件的商品" : "上游目录为空"}</TableCell></TableRow>}</TableBody></Table></div>
      <AdminPagination total={filtered.length} page={currentPage} pageSize={pageSize} onPageChange={setPage} onPageSizeChange={value => { setPageSize(value); setPage(0); }} />
    </fieldset>
    <form onSubmit={loadPreview} className="space-y-4 border-t pt-5"><fieldset disabled={locked || !canWrite} className="grid gap-4 sm:grid-cols-2"><label className="grid min-w-0 gap-2 text-sm">售价比例（%）<Input aria-label="售价比例" inputMode="decimal" required value={percentage} onChange={event => { invalidate(); setPercentage(event.target.value); }} /><span className="text-xs text-muted-foreground">100% 与上游同价，120% 为上游价格 × 1.2；商品、规格与初装费分别按此比例四舍五入到分。</span></label><label className="grid min-w-0 gap-2 text-sm">本地商品分组<select aria-label="本地商品分组" className={selectStyle} value={categoryID} onChange={event => { invalidate(); setCategoryID(event.target.value); }}><option value="">未分类</option>{categories.data?.map(category => <option key={category.id} value={category.id}>{category.section_name ? `${category.section_name} / ` : ""}{category.name}{!category.active || !category.section_active ? "（隐藏）" : ""}</option>)}</select><span className="text-xs text-muted-foreground">选择本地二级商品分组；分组隐藏时客户仍无法新购。</span></label><label className="flex items-center gap-3 text-sm sm:col-span-2"><Checkbox aria-label="导入后直接上架" checked={active} onCheckedChange={checked => { invalidate(); setActive(checked === true); }} />导入后直接上架</label></fieldset>
      {categories.error ? <Failure error={categories.error} retry={categories.refresh} /> : !categories.data ? <Loading /> : null}
      <div className="flex flex-wrap items-center gap-3"><Button disabled={!canWrite || locked || !selected.length || !categories.data || !pending.ready}><Search size={16} />{busy === "preview" ? "正在预览…" : `预览 ${selected.length} 个商品`}</Button><p className="text-xs text-muted-foreground">预览只读取上游配置，不会购买或开通服务。单批全部成功后统一保存。</p></div>
    </form>
    {currentPreview && <section className="space-y-4 border-t pt-5" aria-label="批量导入预览"><div><h2 className="text-lg font-semibold">导入预览</h2><p className="mt-1 text-sm text-muted-foreground">售价比例 {(currentPreview.response.price_multiplier_bps / 100).toFixed(2)}% · {currentPreview.response.active ? "确认后直接上架" : "确认后保持下架"}。名称与公开标识自动生成；复杂联动或不支持的配置会列出原因。</p></div><div className="overflow-x-auto border-y"><Table><TableHeader><TableRow><TableHead>商品 / 公开标识</TableHead><TableHead>本地基础售价</TableHead><TableHead>客户可选规格</TableHead><TableHead>核对结果</TableHead></TableRow></TableHeader><TableBody>{currentPreview.response.items.map(item => <TableRow key={item.product.id}><TableCell className="min-w-48"><p className="font-medium">{item.product.name}</p><p className="mt-1 text-xs text-muted-foreground break-all">#{item.product.id} · {item.slug}</p></TableCell><TableCell className="min-w-52"><PriceLines prices={item.local_prices} /></TableCell><TableCell className="text-xs">{item.product.options?.length ?? 0} 组规格 · {item.product.fields?.length ?? 0} 个购买字段<p className="mt-1 text-muted-foreground">订单按客户所选配置计算，配置增价另计。</p></TableCell><TableCell className="min-w-44 text-xs">{item.importable && item.product.revision ? <span className="text-emerald-700">可导入</span> : <span className="text-destructive">{item.reason ? unsupportedLabel(item.reason) : "上游未提供有效版本，暂不能导入"}</span>}</TableCell></TableRow>)}</TableBody></Table></div>{hasUnimportable && <p role="alert" className="text-sm text-destructive">部分商品无法导入，当前批次尚未创建任何本地商品。请取消对应商品或调整上游配置后重新预览。</p>}<div className="flex flex-wrap gap-3"><Button type="button" disabled={!canWrite || locked || hasUnimportable || !pending.ready} onClick={() => void confirm()}><Download size={16} />{busy === "import" ? "导入中…" : `确认导入 ${currentPreview.response.items.length} 个商品`}</Button><Button type="button" variant="outline" disabled={locked} onClick={() => setPreview(null)}>返回修改</Button></div></section>}
    {error && <p role="alert" className="break-words text-sm text-destructive">{error}</p>}
    {result && <section role="status" className="space-y-3 border-y border-emerald-200 py-5"><h2 className="flex items-center gap-2 font-semibold text-emerald-700"><Check size={18} />已确认 {result.items.length} 个商品</h2><div className="divide-y">{result.items.map(item => <p key={item.product_id} className="flex flex-wrap gap-2 py-2 text-sm"><Link className="text-primary underline" href={`/admin/products/${item.product_id}`}>{item.name} · #{item.product_id}</Link><span className="text-muted-foreground">{item.status === "existing" ? "此前已导入，本次未重复创建" : "导入成功"}</span></p>)}</div><Button asChild type="button" variant="outline"><Link href="/admin/products">查看本地商品</Link></Button></section>}
  </section>;
}
