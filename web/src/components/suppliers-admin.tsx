"use client";

import { useRef, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { ArrowLeft, Download, Pencil, Plus, RefreshCw, Save } from "lucide-react";
import { api, money, type CatalogCategory, type SupplierAccount, type SupplierPrice, type SupplierPriceTier, type SupplierProduct, type SupplierProvider } from "@/lib/api";
import { channelConfigurationErrors, channelSchemaVersion, channelSecureConfigured, initialChannelConfig, isSupportedChannelField } from "@/lib/channel-configuration";
import { supplierAccountIssue, supplierConfigurationInstance, supplierConfigurationIssue } from "@/lib/supplier-configuration";
import { useResource } from "@/hooks/use-resource";
import { Workspace, useAdminPermission } from "./workspace";
import { Failure, Loading, cycles } from "./common";
import { ChannelConfigurationIssues, ChannelFieldEditor } from "./channel-instances";
import { catalogSelectStyle } from "./catalog-admin";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Checkbox } from "./ui/checkbox";
import { Table, TableHeader, TableHead, TableBody, TableRow, TableCell } from "./ui/table";

function cents(value: string) {
  if (!/^\d{1,7}(?:\.\d{1,2})?$/.test(value)) throw new Error("金额最多保留两位小数且不能超过 100 万元");
  const result = Math.round(Number(value) * 100);
  if (result > 100000000) throw new Error("金额不能超过 100 万元");
  return result;
}
function amount(value: number) { return (value / 100).toFixed(2); }
function initialSupplierOptions(product: SupplierProduct) {
  return Object.fromEntries((product.options ?? []).filter(option => option.type === "quantity").map(option => [String(option.id), String(option.choices?.[0]?.min_qty ?? 0)]));
}
type SupplierCostParts = { amount: bigint; setup: bigint };
function costParts(price: SupplierPrice | undefined, raw: boolean): SupplierCostParts | null {
  const amount = raw ? price?.raw_amount_cents : price?.amount_cents;
  const setup = raw ? price?.raw_setup_cents : price?.setup_cents;
  if (amount === undefined || setup === undefined || !Number.isSafeInteger(amount) || !Number.isSafeInteger(setup) || amount < 0 || setup < 0) return null;
  return { amount: BigInt(amount), setup: BigInt(setup) };
}
function graduatedCost(tiers: SupplierPriceTier[] | undefined, quantity: number, raw: boolean, volume = false): SupplierCostParts | null {
  if (quantity === 0) return { amount: BigInt(0), setup: BigInt(0) };
  let last = 0, total = BigInt(0);
  for (const tier of tiers ?? []) {
    const price = costParts(tier, raw);
    if (!price || ![tier.min_qty, tier.max_qty].every(Number.isSafeInteger) || tier.min_qty !== last + 1 || tier.max_qty < tier.min_qty) return null;
    last = tier.max_qty;
    if (quantity < tier.min_qty) continue;
    total += BigInt(Math.min(quantity, tier.max_qty) - tier.min_qty + 1) * price.amount;
    if (quantity <= tier.max_qty) return { amount: volume ? BigInt(quantity) * price.amount : total, setup: price.setup };
  }
  return null;
}
function upstreamCost(product: SupplierProduct, cycle: string, options: Record<string, string>) {
  const multiplier = product.price_multiplier ?? 0;
  if (!Number.isSafeInteger(multiplier) || multiplier < 0) return NaN;
  const raw = multiplier > 0;
  const total = costParts(product.prices?.[cycle], raw);
  if (!total) return NaN;
  for (const option of product.options ?? []) {
    const value = options[String(option.id)];
    if (!value) continue;
    const choice = option.type === "quantity" ? option.choices?.[0] : option.choices?.find(item => String(item.id) === value);
    if (!choice) return NaN;
    let price: SupplierCostParts | null;
    if (option.type === "quantity") {
      const quantity = /^\d+$/.test(value) ? Number(value) : NaN;
      if (!Number.isSafeInteger(quantity)) return NaN;
      if (option.quantity_mode && !["linear", "graduated", "volume"].includes(option.quantity_mode)) return NaN;
      const unit = costParts(choice.prices?.[cycle], raw);
      if (!unit) return NaN;
      if (quantity === 0) continue;
      if (option.quantity_mode === "graduated" || option.quantity_mode === "volume") {
        price = graduatedCost(choice.price_tiers?.[cycle], quantity, raw, option.quantity_mode === "volume");
      } else {
        price = { amount: unit.amount * BigInt(quantity), setup: unit.setup };
      }
    } else {
      price = costParts(choice.prices?.[cycle], raw);
    }
    if (!price) return NaN;
    total.amount += price.amount;
    total.setup += price.setup;
  }
  // The limit covers upstream DECIMAL rounding on the aggregate recurring
  // amount and setup amount independently. The actual debit uses its invoice.
  const result = raw ? (total.amount * BigInt(multiplier) + BigInt(9999)) / BigInt(10000) + (total.setup * BigInt(multiplier) + BigInt(9999)) / BigInt(10000) : total.amount + total.setup;
  return result <= BigInt(Number.MAX_SAFE_INTEGER) ? Number(result) : NaN;
}
function quantityPriceHint(option: NonNullable<SupplierProduct["options"]>[number], cycle: string) {
  const choice = option.choices?.[0];
  if (option.quantity_mode === "graduated" || option.quantity_mode === "volume") return `${option.quantity_mode === "volume" ? "区间单价（全部数量按命中区间单价计算）" : "累进阶梯"}：${(choice?.price_tiers?.[cycle] ?? []).map(tier => `${tier.min_qty}–${tier.max_qty} 每单位 ${money(tier.amount_cents)}，初装费 ${money(tier.setup_cents)}`).join("；")}。初装费取命中区间并收取一次，数量为 0 不收费。`;
  return `${choice?.name ?? ""} · 每单位 ${money(choice?.prices?.[cycle]?.amount_cents ?? 0)}，初装费 ${money(choice?.prices?.[cycle]?.setup_cents ?? 0)}（数量大于 0 时收取一次）`;
}
export function unsupportedLabel(reason?: string) {
  return ({ detail_required: "需要查看商品配置", unsupported_currency: "仅支持人民币", unsupported_cycle: "没有可导入的付费账期", option_linkage_unsupported: "规格包含尚不支持的多级联动", advanced_configuration: "暂不支持高级配置联动", quantity_tiers_invalid: "数量计价区间不连续或范围无效", quantity_volume_pricing_unsupported: "暂不支持此数量区间计价方式", unsupported_option_type: "暂不支持此规格类型", unsupported_advanced: "暂不支持高级配置联动", unsupported_advanced_config: "暂不支持高级配置联动", unsupported_field: "暂不支持此购买字段", invalid_quantity_bounds: "上游数量范围无法映射，请调整上游数量上限", out_of_stock: "上游库存不足" } as Record<string, string>)[reason ?? ""] ?? (reason ? `当前商品暂不支持导入：${reason}` : "当前商品配置暂不支持导入");
}

export function SuppliersView({ returnHref }: { returnHref?: string }) {
  return <Workspace title="供应商" admin>{returnHref && <Button type="button" variant="outline" size="sm" className="mb-5" asChild><Link href={returnHref}>返回应用列表</Link></Button>}<SuppliersManager /></Workspace>;
}

function SuppliersManager() {
  const manage = useAdminPermission("products.manage");
  const providers = useResource<SupplierProvider[]>("/admin/supplier-providers", false);
  const accounts = useResource<SupplierAccount[]>("/admin/suppliers", false);
  const [editor, setEditor] = useState<{ account?: SupplierAccount } | null>(null);
  const [selected, setSelected] = useState<SupplierAccount | null>(null);
  const [catalog, setCatalog] = useState<SupplierProduct[] | null>(null);
  const [catalogBusy, setCatalogBusy] = useState(false);
  const [productID, setProductID] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const catalogRequest = useRef(0);
  const mutation = useRef(false);
  const [catalogSearch, setCatalogSearch] = useState("");
  const [supportFilter, setSupportFilter] = useState("all");
  const selectedProvider = providers.data?.find(item => item.id === selected?.provider);
  const detail = useResource<SupplierProduct>(manage && selected && productID && !supplierAccountIssue(selected, selectedProvider, "product") ? `/admin/suppliers/${selected.id}/products/${productID}` : null, false);

  function clearCatalog() { catalogRequest.current++; setCatalogBusy(false); setSelected(null); setCatalog(null); setProductID(null); }
  async function disableAccount(account: SupplierAccount) {
    if (!manage || mutation.current || !account.active || account.blocked_job_id || account.running_job_id) return;
    mutation.current = true; setBusy(true); setError(""); setNotice("");
    try {
      await api(`/admin/suppliers/${account.id}`, { method: "PUT", body: JSON.stringify({ disable_only: true, active: false, expected_version: account.version }) });
      accounts.refresh(); clearCatalog(); setNotice("供应商已停用，原账户和凭据已保留");
    } catch (cause) { setError((cause as Error).message); }
    finally { mutation.current = false; setBusy(false); }
  }
  async function loadCatalog(account: SupplierAccount) {
    const provider = providers.data?.find(item => item.id === account.provider);
    if (!manage || busy || catalogBusy || supplierAccountIssue(account, provider, "catalog")) return;
    const request = ++catalogRequest.current;
    setSelected(account); setCatalog(null); setProductID(null); setCatalogSearch(""); setSupportFilter("all"); setCatalogBusy(true); setError(""); setNotice("");
    try { const products = await api<SupplierProduct[]>(`/admin/suppliers/${account.id}/catalog`, { method: "POST" }); if (request === catalogRequest.current) { setCatalog(products); setNotice("上游目录已读取"); } }
    catch (cause) { if (request === catalogRequest.current) setError((cause as Error).message); }
    finally { if (request === catalogRequest.current) setCatalogBusy(false); }
  }
  const visibleCatalog = catalog?.filter(product => `${product.name} ${product.id}`.toLowerCase().includes(catalogSearch.toLowerCase()) && (supportFilter === "all" || (supportFilter === "supported" ? product.supported || product.unsupported_reason === "detail_required" : !product.supported && product.unsupported_reason !== "detail_required"))) ?? [];
  const failure = providers.error ?? accounts.error;
  return <div className="space-y-7">
    <div className="flex flex-wrap items-end justify-between gap-3"><div><h1 className="page-title">供应商</h1><p className="text-sm text-muted-foreground mt-2">管理上游账户与本地商品导入</p></div>{manage && providers.data && <div className="flex flex-wrap gap-3"><Button asChild variant="outline"><Link href="/admin/suppliers/catalog"><Download />批量导入商品</Link></Button><Button onClick={() => { setEditor({}); setError(""); setNotice(""); }} disabled={busy || providers.data.length === 0}><Plus />新增供应商</Button></div>}</div>
    {notice && <p role="status" className="text-sm text-emerald-700">{notice}</p>}
    {error && <p role="alert" className="text-sm text-destructive break-all">{error}</p>}
    {failure ? <Failure error={failure} retry={() => { providers.refresh(); accounts.refresh(); }} /> : !providers.data || !accounts.data ? <Loading /> : <>
      {providers.data.length === 0 && <p className="border-y py-8 text-muted-foreground">暂无可用供应商类型</p>}
      <div className="overflow-x-auto border-y"><Table><TableHeader><TableRow><TableHead>供应商</TableHead><TableHead>接口 / 账户</TableHead><TableHead>状态</TableHead><TableHead>本地商品</TableHead><TableHead>版本 / 风险</TableHead><TableHead>操作</TableHead></TableRow></TableHeader><TableBody>{accounts.data.map(account => {
        const provider = providers.data?.find(item => item.id === account.provider);
        const issue = supplierConfigurationIssue(provider, account);
        const unavailable = supplierAccountIssue(account, provider, "catalog");
        const blocked = account.blocked_job_id ?? account.running_job_id;
        return <TableRow key={account.id}><TableCell className="min-w-40"><p className="font-medium break-words">{account.name}</p><p className="text-xs text-muted-foreground mt-1">{provider?.name ?? account.provider}</p>{issue && <p className="mt-1 text-xs text-amber-700">{issue}</p>}</TableCell><TableCell className="max-w-xs break-all"><p>{account.base_url || "使用模块配置"}</p><p className="text-xs text-muted-foreground mt-1">{account.username || account.provider}</p></TableCell><TableCell className="whitespace-nowrap">{account.active ? "已启用" : "已停用"}<p className="text-xs text-muted-foreground mt-1">{account.configuration_status === "valid" ? "配置有效" : account.configuration_status === "invalid" ? "配置待修正" : "请核对配置状态"}</p>{unavailable && !issue && <p className="text-xs text-amber-700 mt-1 whitespace-normal">{unavailable}</p>}</TableCell><TableCell>{account.product_count}</TableCell><TableCell>{account.version}<p className="text-xs text-muted-foreground mt-1">配置版本 {account.schema_version ?? 1}</p>{blocked && <p className="text-xs text-amber-700 mt-1">待核对任务 #{blocked}</p>}</TableCell><TableCell>{manage && <div className="flex flex-wrap gap-2"><Button variant="outline" size="icon-sm" title="编辑供应商" aria-label={`编辑 ${account.name}`} disabled={busy || !!issue || !!blocked} onClick={() => { setEditor({ account }); setError(""); setNotice(""); }}><Pencil size={14} /></Button>{account.active && <Button variant="outline" size="sm" disabled={busy || !!blocked} onClick={() => void disableAccount(account)}>停用</Button>}<Button variant="outline" size="sm" disabled={busy || catalogBusy || !!unavailable} onClick={() => void loadCatalog(account)}><RefreshCw size={14} />高级固定规格导入</Button></div>}</TableCell></TableRow>;
      })}{accounts.data.length === 0 && <TableRow><TableCell colSpan={6} className="py-10 text-center text-muted-foreground">暂无供应商</TableCell></TableRow>}</TableBody></Table></div>
    </>}
    {editor && providers.data && <SupplierAccountEditor key={editor.account?.id ?? "new"} providers={providers.data} account={editor.account} onCancel={() => setEditor(null)} onDone={() => { setEditor(null); accounts.refresh(); clearCatalog(); setNotice("供应商已保存"); }} />}
    {selected && <section className="border-t pt-6"><div className="flex flex-wrap justify-between gap-3 items-center mb-5"><div><h2 className="text-lg font-semibold">{selected.name} · 上游目录</h2><p className="text-sm text-muted-foreground mt-1">固定规格导入仅适合单一账期和预设配置；多账期与客户可选规格请使用批量导入。</p></div><Button variant="ghost" onClick={() => { clearCatalog(); setError(""); setNotice(""); }}><ArrowLeft />返回供应商</Button></div>{catalogBusy ? <Loading /> : catalog && <div className="divide-y border-y"><div className="flex flex-wrap gap-3 pb-4"><Input aria-label="搜索上游商品" className="sm:max-w-sm" placeholder="商品名称或上游 ID" value={catalogSearch} onChange={event => setCatalogSearch(event.target.value)} /><select aria-label="目录导入状态" className={`${catalogSelectStyle} sm:max-w-xs`} value={supportFilter} onChange={event => setSupportFilter(event.target.value)}><option value="all">全部商品</option><option value="supported">可导入或待配置</option><option value="unsupported">不支持导入</option></select></div><div className="overflow-x-auto"><Table><TableHeader><TableRow><TableHead>商品 / 上游 ID</TableHead><TableHead>库存</TableHead><TableHead>账期价格</TableHead><TableHead>状态</TableHead><TableHead>操作</TableHead></TableRow></TableHeader><TableBody>{visibleCatalog.map(product => <TableRow key={product.id}><TableCell className="min-w-48"><h3 className="font-medium break-words">{product.name}</h3><p className="text-xs text-muted-foreground mt-1">#{product.id}</p></TableCell><TableCell>{product.stock ?? "未提供"}</TableCell><TableCell className="min-w-40 text-xs">{Object.entries(product.prices ?? {}).map(([cycle, price]) => <p key={cycle}>{cycles[cycle] ?? cycle} {money(price.amount_cents)}</p>)}</TableCell><TableCell className="text-xs">{product.supported ? "可导入" : unsupportedLabel(product.unsupported_reason)}</TableCell><TableCell><Button size="sm" variant="outline" disabled={!selectedProvider?.capabilities.product || (!product.supported && product.unsupported_reason !== "detail_required")} onClick={() => setProductID(product.id)}>{product.unsupported_reason === "detail_required" ? "查看配置" : "配置导入"}</Button></TableCell></TableRow>)}{visibleCatalog.length === 0 && <TableRow><TableCell colSpan={5} className="py-10 text-center text-muted-foreground">{catalog.length === 0 ? "上游目录为空" : "没有符合筛选条件的商品"}</TableCell></TableRow>}</TableBody></Table></div></div>}{productID && (detail.error ? <Failure error={detail.error} retry={detail.refresh} /> : !detail.data ? <Loading /> : !selectedProvider?.capabilities.create ? <p role="status" className="text-sm text-amber-700">此供应商未提供开通能力，暂不能导入为销售商品。</p> : <ImportProduct key={`${selected.id}:${productID}:${detail.data.revision}`} account={selected} product={detail.data} />)}</section>}
  </div>;
}

export function SupplierAccountEditor({ providers, account, onDone, onCancel }: { providers: SupplierProvider[]; account?: SupplierAccount; onDone: () => void; onCancel: () => void }) {
  const manage = useAdminPermission("products.manage");
  const [providerID, setProviderID] = useState(account?.provider ?? providers[0]?.id ?? "");
  const provider = providers.find(item => item.id === providerID);
  const descriptor = provider?.descriptor;
  const [name, setName] = useState(account?.name ?? "");
  const [active, setActive] = useState(account?.active ?? true);
  const [config, setConfig] = useState<Record<string, unknown>>(() => account ? { ...account.config } : descriptor ? initialChannelConfig(descriptor) : {});
  const [secureConfig, setSecureConfig] = useState<Record<string, string>>({});
  const [clearSecureFields, setClearSecureFields] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const inFlight = useRef(false);
  const instance = account ? supplierConfigurationInstance(account) : undefined;
  const issue = supplierConfigurationIssue(provider, account);
  const unsupported = (descriptor?.fields ?? []).some(field => !isSupportedChannelField(field));
  const blocked = account?.blocked_job_id ?? account?.running_job_id;
  const lockedIssues = (account?.configuration_errors ?? []).filter(item => !(descriptor?.fields ?? []).some(field => field.key === item.field && field.secret) || item.code === "secret_in_config");
  function selectProvider(value: string) {
    if (account || busy) return;
    setProviderID(value); const next = providers.find(item => item.id === value)?.descriptor;
    setConfig(next ? initialChannelConfig(next) : {}); setSecureConfig({}); setClearSecureFields([]); setError("");
  }
  async function save(event: React.FormEvent) {
    event.preventDefault();
    if (!manage || inFlight.current || issue || blocked || unsupported || lockedIssues.length || !descriptor) return;
    const errors = channelConfigurationErrors(descriptor, config, active, secureConfig, clearSecureFields, instance);
    if (errors.length) { setError(errors.join(" ")); return; }
    inFlight.current = true; setBusy(true); setError("");
    try {
      const body = {
        name: name.trim(), provider: providerID, active, config,
        schema_version: channelSchemaVersion(descriptor), secure_config: Object.fromEntries(Object.entries(secureConfig).filter(([, value]) => value !== "")), clear_secure_fields: clearSecureFields,
        ...(account ? { expected_version: account.version, expected_schema_version: account.schema_version ?? 1 } : {}),
      };
      await api(`/admin/suppliers${account ? `/${account.id}` : ""}`, { method: account ? "PUT" : "POST", body: JSON.stringify(body) });
      setSecureConfig({}); onDone();
    } catch (cause) { setError((cause as Error).message); }
    finally { inFlight.current = false; setBusy(false); }
  }
  return <section className="border-t pt-5 max-w-4xl space-y-4"><div className="flex items-center justify-between gap-3"><h2 className="text-lg font-semibold">{account ? "编辑供应商" : "新增供应商"}</h2><Button type="button" variant="ghost" disabled={busy} onClick={onCancel}>取消</Button></div>
    {issue && <p role="alert" className="text-sm text-amber-700">{issue}；原账户可在列表中停用。</p>}
    {lockedIssues.length > 0 && <p role="status" className="text-sm text-amber-700">原账户的固定配置无效，无法在此改写。请先停用该账户，再新增供应商；原商品和服务的绑定会保留。</p>}
    {account && descriptor && !lockedIssues.length && <ChannelConfigurationIssues descriptor={descriptor} instance={instance} />}
    <form onSubmit={save} className="space-y-4"><fieldset disabled={busy || !manage || !!issue || !!blocked} className="grid sm:grid-cols-2 gap-4"><label className="grid gap-2 text-sm">名称<Input aria-label="供应商名称" required maxLength={120} value={name} onChange={event => setName(event.target.value)} /></label><label className="grid gap-2 text-sm">类型<select aria-label="供应商类型" className={catalogSelectStyle} required value={providerID} disabled={!!account} onChange={event => selectProvider(event.target.value)}>{providers.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
      <label className="flex items-center gap-2 text-sm sm:col-span-2"><Checkbox aria-label="启用供应商" checked={active} onCheckedChange={value => setActive(value === true)} />启用供应商</label>
      {account && <p className="text-xs text-muted-foreground sm:col-span-2">普通配置和账户身份已固定；更换上游配置请新增供应商。密钥留空保留原值，可单独轮换或清除。</p>}
      {(descriptor?.fields ?? []).map(field => !field.secret || !isSupportedChannelField(field) ? <ChannelFieldEditor key={field.key} field={field} value={config[field.key]} disabled={!!account} onChange={value => { if (!account) setConfig(current => ({ ...current, [field.key]: value })); }} /> : <div key={field.key} className="grid gap-2 text-sm sm:col-span-2"><label htmlFor={`supplier-secret-${field.key}`}>{field.label}{field.required && " *"}</label>{field.type === "textarea" ? <Textarea id={`supplier-secret-${field.key}`} aria-label={field.label} value={secureConfig[field.key] ?? ""} disabled={clearSecureFields.includes(field.key)} onChange={event => setSecureConfig(current => ({ ...current, [field.key]: event.target.value }))} placeholder={channelSecureConfigured(descriptor!, instance, field.key) ? "已配置，留空保留" : "请输入凭据"} autoComplete="off" rows={4} /> : <Input id={`supplier-secret-${field.key}`} aria-label={field.label} type="password" value={secureConfig[field.key] ?? ""} disabled={clearSecureFields.includes(field.key)} onChange={event => setSecureConfig(current => ({ ...current, [field.key]: event.target.value }))} placeholder={channelSecureConfigured(descriptor!, instance, field.key) ? "已配置，留空保留" : "请输入凭据"} autoComplete="new-password" />}<label className="flex items-center gap-2 text-xs text-muted-foreground"><Checkbox aria-label={`清除${field.label}`} checked={clearSecureFields.includes(field.key)} onCheckedChange={value => { setClearSecureFields(current => value === true ? [...new Set([...current, field.key])] : current.filter(key => key !== field.key)); if (value === true) { setSecureConfig(current => ({ ...current, [field.key]: "" })); if (field.required) setActive(false); } }} />清除{field.label}{channelSecureConfigured(descriptor!, instance, field.key) ? "（已配置）" : "（尚未配置）"}</label></div>)}
    </fieldset>
      {blocked && <p role="status" className="text-sm text-amber-700">供应商任务 #{blocked} 待核对，暂不能修改配置。</p>}
      {error && <p role="alert" className="text-sm text-destructive break-words">{error}</p>}
      <div className="border-t pt-4 flex gap-3">{manage && <Button disabled={busy || !!issue || !!blocked || unsupported || lockedIssues.length > 0}><Save />{busy ? "保存中" : "保存更改"}</Button>}<Button type="button" variant="outline" disabled={busy} onClick={onCancel}>取消更改</Button></div>
    </form>
  </section>;
}

export function ImportProduct({account,product}:{account:SupplierAccount;product:SupplierProduct}) {
  const router=useRouter(); const manage=useAdminPermission("products.manage");
  const categories=useResource<CatalogCategory[]>("/admin/catalog-groups/category",false);
  const availableCycles=Object.keys(product.prices??{}).filter(c=>Object.hasOwn(cycles,c));
  const [cycle,setCycle]=useState(availableCycles[0]??"");
  const [name,setName]=useState(product.name);
  const [slug,setSlug]=useState("");
  const [categoryID,setCategoryID]=useState("");
  const [price,setPrice]=useState(amount(product.prices?.[availableCycles[0]]?.amount_cents??0));
  const [setup,setSetup]=useState(amount(product.prices?.[availableCycles[0]]?.setup_cents??0));
  const [maxCostOverride,setMaxCostOverride]=useState<string|null>(null);
  const [stock,setStock]=useState(product.stock==null?"":String(product.stock));
  const [options,setOptions]=useState<Record<string,string>>(()=>initialSupplierOptions(product));
  const [fields,setFields]=useState<Record<string,string>>({});
  const estimatedCost=upstreamCost(product,cycle,options);
  const invalidCost=!Number.isSafeInteger(estimatedCost)||estimatedCost<0;
  const maxCost=maxCostOverride??(invalidCost?"":amount(estimatedCost));
  const [busy,setBusy]=useState(false);const [error,setError]=useState("");
  const inFlight=useRef(false);
  const unsupported=invalidCost||(product.options??[]).some(option=>!["select","quantity"].includes(option.type))||(product.fields??[]).some(field=>!["text","dropdown","textarea","password"].includes(field.type));
  async function submit(event:React.FormEvent) {
    event.preventDefault(); if (!manage||inFlight.current||!product.supported||!product.revision) return;
    for (const option of product.options??[]) {
      if (option.type!=="quantity") continue;
      const value=options[String(option.id)]??"";
      if (!value&&!option.required) continue;
      const quantity=Number(value);
      const choice=option.choices?.[0];
      if (!/^\d+$/.test(value)||!Number.isSafeInteger(quantity)||option.choices?.length!==1||!choice||quantity<(choice.min_qty??0)||quantity>(choice.max_qty??0)) {setError(`${option.name}数量必须为 ${choice?.min_qty??0} 至 ${choice?.max_qty??0} 之间的整数`);return;}
    }
    if (unsupported) return;
    inFlight.current=true;setBusy(true);setError("");
    try {
      if (cents(maxCost)<estimatedCost) throw new Error("上游最高成本低于当前规格和初装费合计");
      const result=await api<{id:number}>(`/admin/suppliers/${account.id}/import`,{method:"POST",body:JSON.stringify({upstream_product_id:product.id,expected_upstream_revision:product.revision,expected_supplier_version:account.version,name:name.trim(),slug:slug.trim(),category_id:categoryID?Number(categoryID):null,billing_cycle:cycle,price_cents:cents(price),setup_fee_cents:cents(setup),maximum_cost_cents:cents(maxCost),stock:stock===""?null:Number(stock),active:false,config_options:options,custom_fields:fields})});
      router.push(`/admin/products/${result.id}`);
    } catch (e) {setError((e as Error).message);} finally {inFlight.current=false;setBusy(false);}
  }
  return <section className="border-t pt-6 mt-6 max-w-5xl space-y-5"><div><h3 className="text-lg font-semibold">导入 {product.name}</h3><p className="text-sm text-muted-foreground mt-1">选择固定的上游配置。客户使用本地购物车；导入后商品默认下架，可在商品编辑页上架。</p></div>{product.supported && !product.revision && <p role="alert" className="text-sm text-amber-700">上游未提供可核对的商品版本，暂不能导入。请重新读取配置或联系供应商。</p>}{!product.supported?<p className="text-sm text-destructive">{unsupportedLabel(product.unsupported_reason)}</p>:<form onSubmit={submit} className="space-y-5"><fieldset disabled={!manage||busy} className="space-y-5"><h4 className="font-medium text-sm border-b pb-3">本地商品</h4><div className="grid sm:grid-cols-2 xl:grid-cols-4 gap-4"><label className="grid gap-2 text-sm">本地商品名称<Input required maxLength={180} value={name} onChange={e=>setName(e.target.value)}/></label><label className="grid gap-2 text-sm">公开标识<Input required maxLength={120} pattern="[a-z0-9\-]+" placeholder="例如 cloud-starter" value={slug} onChange={e=>setSlug(e.target.value)}/></label><label className="grid gap-2 text-sm">分类<select className={catalogSelectStyle} value={categoryID} onChange={e=>setCategoryID(e.target.value)}><option value="">未分类</option>{categories.data?.map(c=><option key={c.id} value={c.id}>{c.section_name?`${c.section_name} / `:""}{c.name}</option>)}</select></label><label className="grid gap-2 text-sm">账期<select className={catalogSelectStyle} required value={cycle} onChange={e=>setCycle(e.target.value)}>{availableCycles.map(c=><option key={c} value={c}>{cycles[c]??c}</option>)}</select></label></div>
    {unsupported&&<p role="alert" className="text-sm text-destructive">{invalidCost?"上游价格、原始计价数据或阶梯区间无效，暂不能导入。":"当前商品包含界面不支持的规格或字段类型，暂不能导入。"}</p>}
    {(product.options??[]).length>0 && <div className="border-t pt-4 space-y-4"><h4 className="font-medium">上游固定配置</h4>{(product.options??[]).map(option=><label key={option.id} className="grid gap-2 text-sm">{option.name}{option.required&&" *"}{option.type==="quantity"?<><Input aria-label={`${option.name}数量`} type="number" step={1} min={option.choices?.[0]?.min_qty??0} max={option.choices?.[0]?.max_qty??0} required={option.required} value={options[String(option.id)]??""} onChange={event=>setOptions({...options,[option.id]:event.target.value})}/><span className="text-xs text-muted-foreground">{quantityPriceHint(option,cycle)}</span></>:option.type==="select"?<select aria-label={option.name} className={catalogSelectStyle} required={option.required} value={options[String(option.id)]??""} onChange={e=>setOptions({...options,[option.id]:e.target.value})}><option value="">请选择</option>{(option.choices??[]).map(choice=><option key={choice.id} value={String(choice.id)}>{choice.name}{choice.prices?.[cycle]?` · ${money(choice.prices[cycle].amount_cents+choice.prices[cycle].setup_cents)}`:""}</option>)}</select>:<span className="text-destructive">不支持的规格类型：{option.type}</span>}</label>)}</div>}
    {(product.fields??[]).length>0 && <div className="border-t pt-4 space-y-4"><h4 className="font-medium">上游自定义字段</h4>{(product.fields??[]).map(field=><label key={field.id} className="grid gap-2 text-sm">{field.name}{field.required&&" *"}{field.description&&<span className="text-xs text-muted-foreground">{field.description}</span>}{(field.choices??[]).length>0?<select className={catalogSelectStyle} required={field.required} value={fields[String(field.id)]??""} onChange={e=>setFields({...fields,[field.id]:e.target.value})}><option value="">请选择</option>{(field.choices??[]).map(choice=><option key={choice} value={choice}>{choice}</option>)}</select>:field.type==="textarea"?<Textarea required={field.required} value={fields[String(field.id)]??""} onChange={e=>setFields({...fields,[field.id]:e.target.value})}/>:<Input required={field.required} type={field.type==="password"?"password":"text"} autoComplete="off" value={fields[String(field.id)]??""} onChange={e=>setFields({...fields,[field.id]:e.target.value})}/>}</label>)}</div>}
    <h4 className="font-medium text-sm border-b pb-3">售价与成本控制</h4><div className="grid sm:grid-cols-2 xl:grid-cols-4 gap-4"><label className="grid gap-2 text-sm">本地售价（元）<Input required inputMode="decimal" value={price} onChange={e=>setPrice(e.target.value)}/></label><label className="grid gap-2 text-sm">初装费（元）<Input required inputMode="decimal" value={setup} onChange={e=>setSetup(e.target.value)}/></label><label className="grid gap-2 text-sm">上游最高成本（元）<Input required inputMode="decimal" value={maxCost} onChange={e=>setMaxCostOverride(e.target.value)}/></label><label className="grid gap-2 text-sm">本地库存<Input type="number" min={0} max={2147483647} placeholder="留空不限量" value={stock} onChange={e=>setStock(e.target.value)}/></label></div><p className="text-xs text-muted-foreground">售价由本地独立设定。当前上游规格及初装费{product.price_multiplier?"预估上限":"合计"} {invalidCost?"无法估算":money(estimatedCost)}；最高成本不能低于此金额。</p>{!!product.price_multiplier&&<p className="text-xs text-muted-foreground">上游代理价按周期费用与初装费分别汇总并向上取整到分，以上金额为保守预估上限；实际支出以上游账单为准。</p>}{categories.error&&<Failure error={categories.error} retry={categories.refresh}/>}<div className="border-t pt-4 flex flex-wrap gap-3"><Button disabled={busy||!cycle||!categories.data||!product.revision||unsupported}><Download/>{busy?"导入中":"导入为下架商品"}</Button><Button type="button" variant="outline" disabled={busy} onClick={()=>{setCycle(availableCycles[0]??"");setName(product.name);setSlug("");setCategoryID("");setPrice(amount(product.prices?.[availableCycles[0]]?.amount_cents??0));setSetup(amount(product.prices?.[availableCycles[0]]?.setup_cents??0));setMaxCostOverride(null);setStock(product.stock==null?"":String(product.stock));setOptions(initialSupplierOptions(product));setFields({});setError("");}}>取消更改</Button></div></fieldset>{error&&<p role="alert" className="text-sm text-destructive break-all">{error}</p>}</form>}</section>;
}
