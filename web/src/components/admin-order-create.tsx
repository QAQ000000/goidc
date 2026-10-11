"use client";

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { ArrowLeft, Calculator, Trash2 } from "lucide-react";
import { api, ApiError, money, type Product, type User } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { changeProductOption, configurationRuleError, isQuantityOption, optionQuantityBounds, resolveProductOptions } from "@/lib/product-options";
import { Workspace, useAdminPermission } from "./workspace";
import { Loading, Failure, cycles } from "./common";
import { ProductOptionChoice } from "./product-option-choice";
import { PurchaseFieldInputs, purchaseFieldValues } from "./purchase-fields";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Checkbox } from "./ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "./ui/dialog";

type Customer = { id: number; name: string; email: string; status: string };
type Page<T> = { items: T[]; total: number; limit: number; offset: number };
type Provider = { name: string; label: string; instance_id: string; simulation: boolean };
type Options = { payment_providers: Provider[]; mail_enabled: boolean };
type Line = { key: string; product_id: number; billing_cycle: string; quantity: number; selected: Record<number, number>; quantities: Record<number, number>; fields: Record<string, string>; first: string; renewal: string };
type Draft = { customer: Customer | null; payment: string; promo: string; confirmed: boolean; generate_invoice: boolean; send_email: boolean; notes: string; lines: Line[] };
type Quote = { quote_hash: string; subtotal_cents: number; discount_cents: number; total_cents: number; promo_code: string; review_status: string; generate_invoice: boolean; items: { product_id: number; name: string; billing_cycle: string; quantity: number; unit_first_cents: number; unit_recurring_cents: number; total_cents: number; expected_revision: number }[] };
type Result = { order_id: number; invoice_id: number; order_no: string; email_status: string; invoice_deferred: boolean };
const blank = (): Draft => ({ customer: null, payment: "", promo: "", confirmed: false, generate_invoice: true, send_email: false, notes: "", lines: [] });
const selectClass = "h-10 w-full min-w-0 max-w-full rounded-md border bg-background px-3 text-sm";

function cents(value: string): number | undefined {
  if (!value.trim()) return undefined;
  if (!/^\d+$/.test(value) || !Number.isSafeInteger(Number(value)) || Number(value) > 100000000) throw new Error("内部价格必须为 0 至 100000000 的整数分");
  return Number(value);
}

export function AdminOrderCreateView() { return <Workspace title="新建订单" admin><OrderForm /></Workspace>; }

function OrderForm() {
  const router = useRouter();
  const manage = useAdminPermission("orders.manage");
  const user = useResource<User>("/me", false);
  const options = useResource<Options>(manage ? "/admin/orders/create-options" : null, false);
  const [customerQuery, setCustomerQuery] = useState("");
  const [customerOffset, setCustomerOffset] = useState(0);
  const customers = useResource<Page<Customer>>(manage ? `/admin/orders/create-options?kind=customers&q=${encodeURIComponent(customerQuery)}&limit=10&offset=${customerOffset}` : null, false);
  const [productQuery, setProductQuery] = useState("");
  const [productOffset, setProductOffset] = useState(0);
  const products = useResource<Page<Product>>(manage ? `/admin/orders/create-options?kind=products&q=${encodeURIComponent(productQuery)}&limit=10&offset=${productOffset}` : null, false);
  const [catalog, setCatalog] = useState<Record<number, Product>>({});
  const [form, setForm] = useState<Draft>(blank);
  const [savedDraft, setSavedDraft] = useState<Draft | null>(null);
  const [draftLoaded, setDraftLoaded] = useState(false);
  const [busy, setBusy] = useState<"quote" | "submit" | "restore" | null>(null);
  const [error, setError] = useState("");
  const [quote, setQuote] = useState<{ value: Quote; fingerprint: string } | null>(null);
  const [confirm, setConfirm] = useState(false);
  const [uncertain, setUncertain] = useState(false);
  const submitKey = useRef("");
  const latest = useRef("");
  const leaving = useRef(false);
  const draftKey = user.data ? `zjmfgo:admin-order-draft:${user.data.id}` : null;
  const fingerprint = JSON.stringify(form);
  useEffect(() => { latest.current = fingerprint; }, [fingerprint]);
  const dirty = fingerprint !== JSON.stringify(blank());
  const totalQuantity = form.lines.reduce((total, line) => total + line.quantity, 0);
  const currentQuote = quote?.fingerprint === fingerprint ? quote.value : null;
  const frozen = busy !== null || uncertain;

  useEffect(() => {
    if (!draftKey || !manage) return;
    const timer = window.setTimeout(() => {
      try { const raw = sessionStorage.getItem(draftKey); if (raw) { const value = JSON.parse(raw) as Draft; if (Array.isArray(value.lines) && value.lines.length <= 100) setSavedDraft(value); } } catch { /* Optional browser storage. */ }
      setDraftLoaded(true);
    }, 0);
    return () => window.clearTimeout(timer);
  }, [draftKey, manage]);
  useEffect(() => {
    if (!draftKey || !draftLoaded || !manage || !dirty || leaving.current) return;
    // Purchase information can contain passwords. It never enters browser storage.
    const safe = { ...form, lines: form.lines.map(line => ({ ...line, fields: {} })) };
    try { sessionStorage.setItem(draftKey, JSON.stringify(safe)); } catch { /* Optional browser storage. */ }
  }, [draftKey, draftLoaded, manage, dirty, form]);
  useEffect(() => {
    if (!dirty) return;
    const unload = (event: BeforeUnloadEvent) => { if (!leaving.current) { event.preventDefault(); event.returnValue = ""; } };
    const click = (event: MouseEvent) => {
      const anchor = event.target instanceof Element ? event.target.closest("a[href]") : null;
      if (anchor && !leaving.current && !event.ctrlKey && !event.metaKey && !window.confirm("订单尚未提交，确认离开？非敏感草稿会保留。")) { event.preventDefault(); event.stopPropagation(); }
    };
    window.addEventListener("beforeunload", unload); document.addEventListener("click", click, true);
    return () => { window.removeEventListener("beforeunload", unload); document.removeEventListener("click", click, true); };
  }, [dirty]);

  function listSuffix() {
    const params = new URLSearchParams(window.location.search);
    const safe = new URLSearchParams();
    for (const key of ["q", "status", "page", "date_from", "date_to", "payment_status", "limit", "customer_id"]) {
      const value = params.get(key); if (value) safe.set(key, value);
    }
    return safe.size ? `?${safe.toString()}` : "";
  }
  function clearDraft() { if (draftKey) { try { sessionStorage.removeItem(draftKey); } catch {} } setSavedDraft(null); }
  function change(patch: Partial<Draft>) { setForm(current => ({ ...current, ...patch })); setError(""); setQuote(null); setConfirm(false); submitKey.current = ""; }
  function lineChange(key: string, patch: Partial<Line>) { change({ lines: form.lines.map(line => line.key === key ? { ...line, ...patch } : line) }); }
  function addProduct(product: Product) {
    if (totalQuantity >= 100) { setError("每笔订单最多创建 100 项服务，请先减少已选产品数量。"); return; }
    setCatalog(current => ({ ...current, [product.id]: product }));
    change({ lines: [...form.lines, { key: crypto.randomUUID(), product_id: product.id, billing_cycle: product.pricing?.find(price => price.active)?.billing_cycle ?? product.billing_cycle, quantity: 1, selected: {}, quantities: {}, fields: {}, first: "", renewal: "" }] });
  }
  async function restore() {
    if (!savedDraft || busy) return;
    setBusy("restore"); setError("");
    try {
      const loaded = await Promise.all([...new Set(savedDraft.lines.map(line => line.product_id))].map(async id => {
        const page = await api<Page<Product>>(`/admin/orders/create-options?kind=products&product_id=${id}`);
        if (!page.items[0]) throw new Error(`商品 #${id} 已不可用，请重新选择`);
        return page.items[0];
      }));
      setCatalog(Object.fromEntries(loaded.map(product => [product.id, product])));
      setForm({ ...savedDraft, lines: savedDraft.lines.map(line => ({ ...line, fields: {} })) }); setQuote(null); clearDraft();
    } catch (e) { setError((e as Error).message); } finally { setBusy(null); }
  }
  function payload() {
    if (!form.customer) throw new Error("请选择客户");
    if (!form.lines.length) throw new Error("请选择至少一个产品/服务");
    if (totalQuantity > 100) throw new Error("每笔订单最多创建 100 项服务，请减少产品数量");
    const provider = options.data?.payment_providers.find(item => `${item.name}:${item.instance_id}` === form.payment);
    if (form.payment && !provider) throw new Error("支付渠道已不可用，请重新选择");
    return {
      customer_id: form.customer.id, payment_provider: provider?.name ?? "", channel_instance_id: provider?.instance_id ?? "",
      confirmed: form.confirmed, generate_invoice: form.generate_invoice, send_email: form.send_email, notes: form.notes, promo_code: form.promo.trim(),
      items: form.lines.map(line => {
        const product = catalog[line.product_id]; if (!product) throw new Error("商品信息尚未加载");
        if (!Number.isSafeInteger(line.quantity) || line.quantity < 1 || line.quantity > 100) throw new Error(`${product.name} 数量需为 1 至 100`);
        const groups = product.option_groups ?? []; const resolved = resolveProductOptions(groups, line.selected);
        if (!resolved.complete) throw new Error(`${product.name} 请完成必选配置`);
        const quantities = Object.fromEntries(resolved.branches.flatMap(({ group, options, selected }) => {
          const option = options.find(item => item.id === selected); if (!option || !isQuantityOption(group)) return [];
          const bounds = optionQuantityBounds(group, option); const value = line.quantities[option.id] ?? bounds.min;
          if (!Number.isSafeInteger(value) || value < bounds.min || value > bounds.max || (value - bounds.min) % bounds.step !== 0) throw new Error(`${group.name} 数量不符合范围或步长`);
          return [[option.id, value]];
        }));
        const ruleError = configurationRuleError(product.configuration_rules, groups, resolved.ids, quantities); if (ruleError) throw new Error(ruleError);
        const first = cents(line.first); const renewal = cents(line.renewal);
        if (form.promo.trim() && (first !== undefined || renewal !== undefined)) throw new Error("内部价格覆盖与优惠码不能同时使用");
        return { product_id: product.id, expected_revision: product.revision, billing_cycle: line.billing_cycle, quantity: line.quantity, option_ids: resolved.ids, option_quantities: quantities, purchase_fields: purchaseFieldValues(product.purchase_field_definitions ?? [], line.fields), ...(first !== undefined ? { first_price_override_cents: first } : {}), ...(renewal !== undefined ? { renewal_price_override_cents: renewal } : {}) };
      }),
    };
  }
  async function calculate() {
    if (frozen || !manage) return;
    setBusy("quote"); setError(""); setQuote(null); const snapshot = fingerprint;
    try { const result = await api<Quote>("/admin/orders/quote", { method: "POST", body: JSON.stringify(payload()) }); if (latest.current === snapshot) { setQuote({ value: result, fingerprint: snapshot }); submitKey.current = `admin-order-${crypto.randomUUID()}`; } }
    catch (e) { setError((e as Error).message); } finally { setBusy(null); }
  }
  async function submit() {
    if (!currentQuote || busy || !manage) return;
    setBusy("submit"); setError("");
    try {
      const result = await api<Result>("/admin/orders", { method: "POST", headers: { "Idempotency-Key": submitKey.current }, body: JSON.stringify({ ...payload(), quote_hash: currentQuote.quote_hash }) });
      leaving.current = true; clearDraft(); router.push(`/admin/orders/${result.order_id}${listSuffix()}`); router.refresh();
    } catch (e) {
      setConfirm(false);
      if (e instanceof ApiError && e.status < 500) { setUncertain(false); if (e.status === 409) { setQuote(null); submitKey.current = ""; } setError(e.status === 409 ? `${e.message}。当前输入已保留，请重新报价。` : e.message); }
      else { setUncertain(true); setError("订单提交结果暂时未知。请用同一请求重试，或先查看订单列表确认；不会重复创建订单。"); }
    } finally { setBusy(null); }
  }
  function cancel() { if (busy || dirty && !window.confirm("确认取消本次新建订单并丢弃草稿？")) return; leaving.current = true; clearDraft(); router.push(`/admin/orders${listSuffix()}`); }

  if (!manage) return <p role="alert" className="py-12 text-center text-destructive">没有新建订单权限。</p>;
  return <div className="min-w-0 space-y-5">
    <div className="flex flex-wrap items-center justify-between gap-3"><h1 className="page-title">添加订单</h1><Button variant="outline" type="button" onClick={cancel} disabled={busy !== null}><ArrowLeft size={16} />返回订单列表</Button></div>
    {savedDraft && <div role="status" className="flex flex-wrap items-center gap-3 rounded border p-3 text-sm"><span>发现未提交的订单草稿，购买资料需重新填写。</span><Button type="button" variant="outline" disabled={frozen} onClick={restore}>恢复草稿</Button><Button type="button" variant="outline" disabled={frozen} onClick={clearDraft}>丢弃草稿</Button></div>}
    {options.error ? <Failure error={options.error} retry={options.refresh} /> : !options.data ? <Loading /> : <form onSubmit={event => { event.preventDefault(); void calculate(); }} className="grid min-w-0 gap-5 xl:grid-cols-[minmax(0,1fr)_320px]">
      <fieldset disabled={frozen} className="min-w-0 space-y-5">
        <section className="min-w-0 rounded border bg-background p-4 space-y-4"><h2 className="font-medium">客户与付款信息</h2>
          <label className="grid min-w-0 gap-2 text-sm">客户搜索<Input aria-label="客户搜索" placeholder="请输入用户姓名或邮箱" value={customerQuery} onChange={event => { setCustomerQuery(event.target.value); setCustomerOffset(0); }} /></label>
          {customers.error ? <Failure error={customers.error} retry={customers.refresh} /> : !customers.data ? <Loading /> : <><label className="grid min-w-0 gap-2 text-sm">客户<select aria-label="客户" className={selectClass} value={form.customer?.id ?? ""} onChange={event => change({ customer: customers.data?.items.find(item => item.id === Number(event.target.value)) ?? form.customer })}><option value="" disabled>请选择客户</option>{form.customer && !customers.data.items.some(item => item.id === form.customer?.id) && <option value={form.customer.id}>{form.customer.name} · {form.customer.email}</option>}{customers.data.items.map(item => <option key={item.id} value={item.id}>{item.name} · {item.email}{item.status !== "active" ? "（停用）" : ""}</option>)}</select></label><SearchPager total={customers.data.total} offset={customerOffset} onChange={setCustomerOffset} label="客户" /></>}
          <div className="grid min-w-0 gap-4 sm:grid-cols-2"><label className="grid min-w-0 gap-2 text-sm">支付方式<select aria-label="支付方式" className={selectClass} value={form.payment} onChange={event => change({ payment: event.target.value })}><option value="">由客户付款时选择</option>{options.data.payment_providers.map(item => <option key={`${item.name}:${item.instance_id}`} value={`${item.name}:${item.instance_id}`}>{item.label}{item.simulation ? "（模拟）" : ""}</option>)}</select></label><label className="grid min-w-0 gap-2 text-sm">优惠码<Input aria-label="优惠码" maxLength={120} value={form.promo} onChange={event => change({ promo: event.target.value })} placeholder="输入后重新报价验证" /></label></div>
          <div className="flex flex-wrap gap-5">{([['confirmed','订单确认'],['generate_invoice','生成账单'],['send_email','发送邮件']] as const).map(([key,label]) => <label key={key} className="flex items-center gap-2 text-sm"><Checkbox checked={form[key]} disabled={key === "send_email" && !options.data?.mail_enabled} onCheckedChange={value => change({ [key]: value === true })} />{label}</label>)}</div><p className="text-xs text-muted-foreground">订单确认仅决定审核状态。付款成功后才进入开通流程。{!options.data.mail_enabled && " 邮件渠道未启用。"}</p>
          <label className="grid gap-2 text-sm">内部备注<Textarea aria-label="内部备注" value={form.notes} maxLength={5000} onChange={event => change({ notes: event.target.value })} /></label>
        </section>
        <section className="min-w-0 rounded border bg-background p-4 space-y-4"><h2 className="font-medium">产品/服务</h2><label className="grid gap-2 text-sm">搜索产品<Input aria-label="搜索产品" placeholder="产品名称" value={productQuery} onChange={event => { setProductQuery(event.target.value); setProductOffset(0); }} /></label>
          {products.error ? <Failure error={products.error} retry={products.refresh} /> : !products.data ? <Loading /> : <><label className="grid min-w-0 gap-2 text-sm">添加其他产品<select aria-label="添加其他产品" className={selectClass} value="" onChange={event => { const product = products.data?.items.find(item => item.id === Number(event.target.value)); if (product) addProduct(product); }} disabled={totalQuantity >= 100}><option value="">请选择产品/服务</option>{products.data.items.map(product => <option key={product.id} value={product.id}>{product.name}</option>)}</select></label><SearchPager total={products.data.total} offset={productOffset} onChange={setProductOffset} label="商品" /></>}
          <p role="status" className="text-xs text-muted-foreground">已选 {form.lines.length} 个产品项目，共 {totalQuantity} 项服务；每笔订单最多 100 项服务。</p>
          {!form.lines.length && <p className="py-8 text-center text-sm text-muted-foreground">未选择项目</p>}
          {form.lines.map((line,index) => <OrderLine key={line.key} index={index} line={line} product={catalog[line.product_id]} change={patch => lineChange(line.key, patch)} remove={() => change({ lines: form.lines.filter(item => item.key !== line.key) })} />)}
        </section>
      </fieldset>
      <aside className="min-w-0 rounded border bg-background p-4 space-y-4 self-start xl:sticky xl:top-20"><h2 className="font-medium">订单详情</h2>{currentQuote ? <><div className="divide-y">{currentQuote.items.map((item,index) => <div key={`${item.product_id}-${index}`} className="py-3 text-sm space-y-1"><p className="font-medium break-words">{item.name} × {item.quantity}</p><p className="text-xs text-muted-foreground">{cycles[item.billing_cycle] ?? item.billing_cycle} · 首次 {money(item.unit_first_cents)} / 件</p><p className="text-xs text-muted-foreground">续费 {money(item.unit_recurring_cents)} / 件</p><p className="text-right tabular-nums">{money(item.total_cents)}</p></div>)}</div><dl className="space-y-3 text-sm"><Amount label="小计" value={currentQuote.subtotal_cents} /><Amount label="优惠" value={currentQuote.discount_cents} /><Amount label="总计" value={currentQuote.total_cents} /></dl><p className="text-xs text-muted-foreground">{currentQuote.review_status === "pending" ? "订单需审核" : "订单已确认"} · {currentQuote.generate_invoice ? "生成待支付账单" : "账单延期生成"}</p></> : <p className="py-6 text-sm text-muted-foreground">{form.lines.length ? "尚未报价，或订单信息已变更。请重新报价后提交。" : "未选择项目"}</p>}
        {error && <p role="alert" className="text-sm text-destructive break-words">{error}</p>}
        <Button type="submit" className="w-full" disabled={frozen || !form.customer || !form.lines.length}><Calculator size={16} />{busy === "quote" ? "报价中…" : "重新报价"}</Button><Button type="button" className="w-full" disabled={busy !== null || !currentQuote} onClick={() => setConfirm(true)}>{uncertain ? "使用原请求重试" : "提交订单"}</Button><Button type="button" className="w-full" variant="outline" disabled={busy !== null} onClick={cancel}>取消</Button>
      </aside>
    </form>}
    <Dialog open={confirm} onOpenChange={open => { if (busy !== "submit") setConfirm(open); }}><DialogContent className="max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>确认提交订单</DialogTitle><DialogDescription>请确认客户、产品配置和最终金额。提交后可在订单详情查看账单与邮件投递结果。</DialogDescription></DialogHeader><div className="text-sm space-y-3"><p className="break-words">客户：{form.customer?.name} · {form.customer?.email}</p><p>商品：{form.lines.length} 个产品项目，共 {totalQuantity} 项服务</p><p className="font-semibold">总计：{currentQuote ? money(currentQuote.total_cents) : "请重新报价"}</p><p>{form.generate_invoice ? "生成账单" : "暂不生成账单"} · {form.send_email ? "请求发送订单邮件" : "不发送订单邮件"}</p></div><DialogFooter><Button variant="outline" type="button" disabled={busy === "submit"} onClick={() => setConfirm(false)}>取消</Button><Button type="button" disabled={busy !== null || !currentQuote} onClick={submit}>{busy === "submit" ? "提交中…" : "确定"}</Button></DialogFooter></DialogContent></Dialog>
  </div>;
}

function SearchPager({ total, offset, onChange, label }: { total: number; offset: number; onChange: (offset: number) => void; label: string }) {
  return <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground"><span>共 {total} 个{label}</span><div className="flex gap-2"><Button type="button" size="sm" variant="outline" disabled={offset === 0} onClick={() => onChange(Math.max(0,offset-10))}>上一页</Button><Button type="button" size="sm" variant="outline" disabled={offset+10>=total} onClick={() => onChange(offset+10)}>下一页</Button></div></div>;
}
function Amount({ label, value }: { label: string; value: number }) { return <div className="flex justify-between gap-3"><dt>{label}</dt><dd className="font-medium tabular-nums">{money(value)}</dd></div>; }
function OrderLine({ line, product, index, change, remove }: { line: Line; product?: Product; index: number; change: (patch: Partial<Line>) => void; remove: () => void }) {
  if (!product) return <p role="alert" className="text-destructive">商品 #{line.product_id} 未加载</p>;
  const groups=product.option_groups??[]; const resolved=resolveProductOptions(groups,line.selected);
  return <article className="min-w-0 border-t pt-5 space-y-4"><div className="flex items-start justify-between gap-3"><h3 className="font-medium break-words">{index+1}. {product.name}</h3><Button type="button" size="icon" variant="ghost" aria-label={`移除产品 ${index+1}`} onClick={remove}><Trash2 size={16} /></Button></div><div className="grid min-w-0 gap-4 sm:grid-cols-2"><label className="grid min-w-0 gap-2 text-sm">付款周期<select aria-label={`产品 ${index+1} 付款周期`} className={selectClass} value={line.billing_cycle} onChange={event => change({ billing_cycle:event.target.value,...(event.target.value==='once'?{renewal:''}:{}) })}>{(product.pricing??[]).filter(price=>price.active).map(price=><option key={price.billing_cycle} value={price.billing_cycle}>{cycles[price.billing_cycle]??price.billing_cycle}</option>)}</select></label><label className="grid gap-2 text-sm">数量<Input aria-label={`产品 ${index+1} 数量`} type="number" min={1} max={100} step={1} value={line.quantity} onChange={event=>change({quantity:Number(event.target.value)})} /></label><label className="grid gap-2 text-sm">内部价格（首次，分）<Input aria-label={`产品 ${index+1} 首次金额`} inputMode="numeric" value={line.first} onChange={event=>change({first:event.target.value})} placeholder="留空使用默认价格" /></label><label className="grid gap-2 text-sm">内部价格（续费，分）<Input aria-label={`产品 ${index+1} 续费金额`} inputMode="numeric" disabled={line.billing_cycle==='once'} value={line.renewal} onChange={event=>change({renewal:event.target.value})} placeholder="留空使用默认续费价格" /></label></div><p className="text-xs text-muted-foreground">首次覆盖包含单件商品、配置和初装费，不改变默认续费价格；一次性商品无续费。商品版本 {product.revision}{product.stock!==null?` · 库存 ${product.stock}`:''}</p><div className="grid min-w-0 gap-4 sm:grid-cols-2">{resolved.branches.map(({group,options,selected})=><div key={group.id} className="min-w-0 space-y-2"><p className="text-sm">{group.name}{group.required?'（必选）':''}</p><ProductOptionChoice group={group} options={options} selected={selected} label={`产品 ${index+1} ${group.name}`} onChange={id=>change({selected:changeProductOption(groups,resolved.selected,group.id,id)})} />{selected>0&&isQuantityOption(group)&&<label className="grid gap-2 text-xs">配置数量<Input type="number" aria-label={`产品 ${index+1} ${group.name} 数量`} {...optionQuantityBounds(group,options.find(option=>option.id===selected)!)} value={line.quantities[selected]??optionQuantityBounds(group,options.find(option=>option.id===selected)!).min} onChange={event=>change({quantities:{...line.quantities,[selected]:Number(event.target.value)}})} /></label>}</div>)}</div><PurchaseFieldInputs fields={product.purchase_field_definitions??[]} values={line.fields} onChange={fields=>change({fields})} /></article>;
}
