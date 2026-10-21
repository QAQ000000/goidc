"use client";

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { ArrowLeft, Calculator, Trash2 } from "lucide-react";
import { api, ApiError, money, type Product, type PromoCode } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { changeProductOption, configurationRuleError, isQuantityOption, optionQuantityBounds, resolveProductOptions } from "@/lib/product-options";
import { Workspace, useAdminPermission } from "./workspace";
import { Loading, Failure, cycles } from "./common";
import { PromoCustomerGroups } from "./promo-customer-groups";
import { PurchaseDiscountDetails, PurchaseDiscountSummary, type PurchasePricingLine } from "./purchase-discount-details";
import { ProductOptionChoice } from "./product-option-choice";
import { PurchaseFieldInputs, purchaseFieldValues } from "./purchase-fields";
import { AdminCustomerCreateBoundary, FixedCreateCustomer, useCreateActivity, useCreateLeaveGuard, assertCreateActor, validCreateResult, rememberCreateIntent, forgetCreateIntent, type AdminCustomerCreateContext } from "./admin-customer-context";
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
type Quote = { quote_hash: string; subtotal_cents: number; discount_cents: number; group_discount_cents?:number; promo_discount_cents?:number; total_cents: number; promo_code: string; review_status: string; generate_invoice: boolean; items: { product_id: number; name: string; billing_cycle: string; quantity: number; unit_first_cents: number; unit_recurring_cents: number; total_cents: number; expected_revision: number; pricing?:PurchasePricingLine }[] };
type Result = { order_id: number; invoice_id: number; order_no: string; email_status: string; invoice_deferred: boolean };
const blank = (): Draft => ({ customer: null, payment: "", promo: "", confirmed: false, generate_invoice: true, send_email: false, notes: "", lines: [] });
const selectClass = "h-10 w-full min-w-0 max-w-full rounded-md border bg-background px-3 text-sm";

function cents(value: string): number | undefined {
  if (!value.trim()) return undefined;
  if (!/^\d+$/.test(value) || !Number.isSafeInteger(Number(value)) || Number(value) > 100000000) throw new Error("内部价格必须为 0 至 100000000 的整数分");
  return Number(value);
}

export function AdminOrderCreateView() { return <Workspace title="新建订单" admin><AdminCustomerCreateBoundary domain="order" fallback="/admin/orders">{context => <OrderForm context={context} />}</AdminCustomerCreateBoundary></Workspace>; }

function OrderForm({ context }: { context: AdminCustomerCreateContext }) {
  const router = useRouter();
  const manage = useAdminPermission("orders.manage");
  const promoView = useAdminPermission("promos.view");
  const promoManage = useAdminPermission("promos.manage");
  const options = useResource<Options>(manage ? "/admin/orders/create-options" : null, false);
  const [customerQuery, setCustomerQuery] = useState("");
  const [customerOffset, setCustomerOffset] = useState(0);
  const customers = useResource<Page<Customer>>(manage && !context.customer ? `/admin/orders/create-options?kind=customers&q=${encodeURIComponent(customerQuery)}&limit=10&offset=${customerOffset}` : null, false);
  const [productQuery, setProductQuery] = useState("");
  const [productOffset, setProductOffset] = useState(0);
  const products = useResource<Page<Product>>(manage ? `/admin/orders/create-options?kind=products&q=${encodeURIComponent(productQuery)}&limit=10&offset=${productOffset}` : null, false);
  const [promoDirectoryOpen, setPromoDirectoryOpen] = useState(false);
  const [createPromoOpen, setCreatePromoOpen] = useState(false);
  const [promoQuery, setPromoQuery] = useState("");
  const [promoStatus, setPromoStatus] = useState("active");
  const [promoOffset, setPromoOffset] = useState(0);
  const promoDirectory = useResource<Page<PromoCode>>(promoView && promoDirectoryOpen ? `/admin/promo-codes/page?q=${encodeURIComponent(promoQuery)}&active=${promoStatus}&order_eligible=true&limit=10&offset=${promoOffset}` : null, false);
  const [catalog, setCatalog] = useState<Record<number, Product>>({});
  const [form, setForm] = useState<Draft>(() => ({ ...blank(), customer: context.customer }));
  const [savedDraft, setSavedDraft] = useState<Draft | null>(null);
  const [draftLoaded, setDraftLoaded] = useState(false);
  const [busy, setBusy] = useState<"quote" | "submit" | "restore" | null>(null);
  const [error, setError] = useState("");
  const [promoConflict, setPromoConflict] = useState(false);
  const [notice, setNotice] = useState("");
  const [quote, setQuote] = useState<{ value: Quote; fingerprint: string } | null>(null);
  const [confirm, setConfirm] = useState(false);
  const [uncertain, setUncertain] = useState(false);
  const submitKey = useRef("");
  const submitted = useRef<{ key: string; body: string } | null>(null);
  const latest = useRef("");
  const leaving = useRef(false);
  const active = useCreateActivity();
  const draftKey = `zjmfgo:admin-order-draft:${context.actorId}:${context.customerId ?? "global"}`;
  const fingerprint = JSON.stringify(form);
  useEffect(() => { latest.current = fingerprint; }, [fingerprint]);
  const dirty = fingerprint !== JSON.stringify({ ...blank(), customer: context.customer });
  const totalQuantity = form.lines.reduce((total, line) => total + line.quantity, 0);
  const currentQuote = quote?.fingerprint === fingerprint ? quote.value : null;
  const frozen = busy !== null || uncertain;
  useCreateLeaveGuard(dirty, frozen, leaving, "订单");

  useEffect(() => {
    if (!draftKey || !manage) return;
    const timer = window.setTimeout(() => {
      try { const raw = sessionStorage.getItem(draftKey); if (raw) { const value = JSON.parse(raw) as Draft; if (Array.isArray(value.lines) && value.lines.length <= 100 && (!context.customerId || value.customer?.id === context.customerId)) setSavedDraft(value); } } catch { /* Optional browser storage. */ }
      setDraftLoaded(true);
    }, 0);
    return () => window.clearTimeout(timer);
  }, [draftKey, manage, context.customerId]);
  useEffect(() => {
    if (!draftKey || !draftLoaded || !manage || !dirty || leaving.current) return;
    // Purchase information can contain passwords. It never enters browser storage.
    const safe = { ...form, lines: form.lines.map(line => ({ ...line, fields: {} })) };
    try { sessionStorage.setItem(draftKey, JSON.stringify(safe)); } catch { /* Optional browser storage. */ }
  }, [draftKey, draftLoaded, manage, dirty, form]);
  function listSuffix() {
    const params = new URLSearchParams(context.query);
    const safe = new URLSearchParams();
    for (const key of ["q", "status", "page", "date_from", "date_to", "payment_status", "limit", "customer_id"]) {
      const value = params.get(key); if (value) safe.set(key, value);
    }
    if (params.get("source") === "supplier") {
      safe.set("source", "supplier");
      const supplier = params.get("supplier_id");
      if (supplier && /^[1-9]\d*$/.test(supplier)) safe.set("supplier_id", supplier);
    }
    return safe.size ? `?${safe.toString()}` : "";
  }
  function clearDraft() { if (draftKey) { try { sessionStorage.removeItem(draftKey); } catch {} } setSavedDraft(null); }
  function change(patch: Partial<Draft>) { if (frozen) return; const next = { ...form, ...patch, ...(context.customer ? { customer: context.customer } : {}) }; latest.current = JSON.stringify(next); setForm(next); setError(""); setPromoConflict(false); setNotice(""); setQuote(null); setConfirm(false); submitKey.current = ""; submitted.current = null; }
  function lineChange(key: string, patch: Partial<Line>) { change({ lines: form.lines.map(line => line.key === key ? { ...line, ...patch } : line) }); }
  function addProduct(product: Product) {
    if (totalQuantity >= 100) { setError("每笔订单最多创建 100 项服务，请先减少已选产品数量。"); return; }
    setCatalog(current => ({ ...current, [product.id]: product }));
    change({ lines: [...form.lines, { key: crypto.randomUUID(), product_id: product.id, billing_cycle: product.pricing?.find(price => price.active)?.billing_cycle ?? product.billing_cycle, quantity: 1, selected: {}, quantities: {}, fields: {}, first: "", renewal: "" }] });
  }
  async function restore() {
    if (!savedDraft || frozen || (context.customerId && savedDraft.customer?.id !== context.customerId)) return;
    setBusy("restore"); context.setLocked(true); setError("");
    try {
      const loaded = await Promise.all([...new Set(savedDraft.lines.map(line => line.product_id))].map(async id => {
        const page = await api<Page<Product>>(`/admin/orders/create-options?kind=products&product_id=${id}`);
        if (!page.items[0]) throw new Error(`商品 #${id} 已不可用，请重新选择`);
        return page.items[0];
      }));
      if (!active.current) return;
      setCatalog(Object.fromEntries(loaded.map(product => [product.id, product])));
      setForm({ ...savedDraft, customer: context.customer ?? savedDraft.customer, lines: savedDraft.lines.map(line => ({ ...line, fields: {} })) }); setQuote(null); clearDraft();
    } catch (e) { if (active.current) setError((e as Error).message); } finally { if (active.current) { setBusy(null); context.setLocked(false); } }
  }
  function payload(draft: Draft = form) {
    if (!draft.customer) throw new Error("请选择客户");
    if (context.customerId && draft.customer.id !== context.customerId) throw new Error("客户身份不一致，请从客户详情重新进入");
    if (!draft.lines.length) throw new Error("请选择至少一个产品/服务");
    if (draft.lines.reduce((total, line) => total + line.quantity, 0) > 100) throw new Error("每笔订单最多创建 100 项服务，请减少产品数量");
    const provider = options.data?.payment_providers.find(item => `${item.name}:${item.instance_id}` === draft.payment);
    if (draft.payment && !provider) throw new Error("支付渠道已不可用，请重新选择");
    return {
      customer_id: draft.customer.id, payment_provider: provider?.name ?? "", channel_instance_id: provider?.instance_id ?? "",
      confirmed: draft.confirmed, generate_invoice: draft.generate_invoice, send_email: draft.send_email, notes: draft.notes, promo_code: draft.promo.trim(),
      items: draft.lines.map(line => {
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
        if (draft.promo.trim() && (first !== undefined || renewal !== undefined)) throw new Error("内部价格覆盖与优惠码不能同时使用");
        return { product_id: product.id, expected_revision: product.revision, billing_cycle: line.billing_cycle, quantity: line.quantity, option_ids: resolved.ids, option_quantities: quantities, purchase_fields: purchaseFieldValues(product.purchase_field_definitions ?? [], line.fields), ...(first !== undefined ? { first_price_override_cents: first } : {}), ...(renewal !== undefined ? { renewal_price_override_cents: renewal } : {}) };
      }),
    };
  }
  async function calculate(draft: Draft = form) {
    if (frozen || !manage) return;
    setBusy("quote"); context.setLocked(true); setError(""); setPromoConflict(false); setNotice(""); setQuote(null); const snapshot = JSON.stringify(draft); latest.current = snapshot;
    try { await assertCreateActor(context, "order"); if (!active.current) return; const result = await api<Quote>("/admin/orders/quote", { method: "POST", body: JSON.stringify(payload(draft)) }); if (active.current && latest.current === snapshot) { setQuote({ value: result, fingerprint: snapshot }); submitKey.current = `admin-order-${crypto.randomUUID()}`; submitted.current = null; } }
    catch (e) { if (active.current) { setError((e as Error).message); setPromoConflict(e instanceof ApiError && e.code === "promo_group_discount_incompatible"); } } finally { if (active.current) { setBusy(null); context.setLocked(false); } }
  }
  function applyPromo(code: string) {
    const next = { ...form, promo: code };
    change({ promo: code });
    setPromoDirectoryOpen(false);
    setCreatePromoOpen(false);
    if (next.customer && next.lines.length) void calculate(next);
    else setNotice("优惠码已带入订单。选择客户和商品后，请重新报价验证。");
  }
  async function submit() {
    if (!currentQuote || busy || !manage) return;
    setBusy("submit"); context.setLocked(true); setError("");
    const wasUncertain = uncertain; let sent = false; let businessFailure = false;
    try {
      await assertCreateActor(context, "order"); if (!active.current) return;
      if (!submitted.current) submitted.current = { key: submitKey.current, body: JSON.stringify({ ...payload(), quote_hash: currentQuote.quote_hash }) };
      rememberCreateIntent(context, "order", submitted.current, JSON.parse(submitted.current.body).items.some((item: { purchase_fields?: Record<string, unknown> }) => Object.keys(item.purchase_fields ?? {}).length > 0));
      sent = true;
      const result = await api<Result>("/admin/orders", { method: "POST", headers: { "Idempotency-Key": submitted.current.key }, body: submitted.current.body }).catch(error => { businessFailure = error instanceof ApiError && [400, 409, 422].includes(error.status) && !error.code.startsWith("idempotency_"); throw error; });
      if (!active.current) return;
      if (!validCreateResult("order", result)) throw new Error("订单响应缺少有效编号，原请求仍需核对");
      await assertCreateActor(context, "order"); if (!active.current) return;
      forgetCreateIntent(context, "order", submitted.current.key);
      leaving.current = true; clearDraft(); router.push(`/admin/orders/${result.order_id}${listSuffix()}`); router.refresh();
    } catch (e) {
      if (!active.current) return;
      setConfirm(false);
      if (!wasUncertain && (!sent || businessFailure)) { setUncertain(false); if (submitted.current) forgetCreateIntent(context, "order", submitted.current.key); submitted.current = null; context.setLocked(false); if (e instanceof ApiError && e.status === 409) { setQuote(null); submitKey.current = ""; } setError(e instanceof ApiError && e.status === 409 ? `${e.message}。当前输入已保留，请重新报价。` : (e as Error).message); }
      else { setUncertain(true); setError("订单提交结果暂时未知。请用同一请求重试，或先查看订单列表确认；不会重复创建订单。"); }
    } finally { if (active.current) setBusy(null); }
  }
  function cancel() { if (frozen || dirty && !window.confirm("订单尚未保存，确认取消并丢弃本次草稿？")) return; leaving.current = true; clearDraft(); router.push(context.customerId || new URLSearchParams(context.query).has("return_to") ? context.returnTo : `${new URLSearchParams(context.query).get("source") === "supplier" ? "/admin/suppliers/orders" : "/admin/orders"}${listSuffix()}`); }

  if (!manage) return <p role="alert" className="py-12 text-center text-destructive">没有新建订单权限。</p>;
  return <div className="min-w-0 space-y-5">
    <div className="flex flex-wrap items-center justify-between gap-3"><h1 className="page-title">添加订单</h1><Button variant="outline" type="button" onClick={cancel} disabled={frozen}><ArrowLeft size={16} />{context.customerId ? "返回客户详情" : "返回订单列表"}</Button></div>
    {savedDraft && <div role="status" className="flex flex-wrap items-center gap-3 rounded border p-3 text-sm"><span>发现未提交的订单草稿，购买资料需重新填写。</span><Button type="button" variant="outline" disabled={frozen} onClick={restore}>恢复草稿</Button><Button type="button" variant="outline" disabled={frozen} onClick={clearDraft}>丢弃草稿</Button></div>}
    {options.error ? <Failure error={options.error} retry={options.refresh} /> : !options.data ? <Loading /> : <form onSubmit={event => { event.preventDefault(); void calculate(); }} className="grid min-w-0 gap-5 xl:grid-cols-[minmax(0,1fr)_320px]">
      <fieldset disabled={frozen} className="min-w-0 space-y-5">
        <section className="min-w-0 rounded border bg-background p-4 space-y-4"><h2 className="font-medium">客户与付款信息</h2>
          {context.customer ? <FixedCreateCustomer customer={context.customer} /> : <><label className="grid min-w-0 gap-2 text-sm">客户搜索<Input aria-label="客户搜索" placeholder="请输入用户姓名或邮箱" value={customerQuery} onChange={event => { setCustomerQuery(event.target.value); setCustomerOffset(0); }} /></label>
          {customers.error ? <Failure error={customers.error} retry={customers.refresh} /> : !customers.data ? <Loading /> : <><label className="grid min-w-0 gap-2 text-sm">客户<select aria-label="客户" className={selectClass} value={form.customer?.id ?? ""} onChange={event => change({ customer: customers.data?.items.find(item => item.id === Number(event.target.value)) ?? form.customer })}><option value="" disabled>请选择客户</option>{form.customer && !customers.data.items.some(item => item.id === form.customer?.id) && <option value={form.customer.id}>{form.customer.name} · {form.customer.email}</option>}{customers.data.items.map(item => <option key={item.id} value={item.id}>{item.name} · {item.email}{item.status !== "active" ? "（停用）" : ""}</option>)}</select></label><SearchPager total={customers.data.total} offset={customerOffset} onChange={setCustomerOffset} label="客户" /></>}
          </>}
          <div className="grid min-w-0 gap-4 sm:grid-cols-2"><label className="grid min-w-0 gap-2 text-sm">支付方式<select aria-label="支付方式" className={selectClass} value={form.payment} onChange={event => change({ payment: event.target.value })}><option value="">由客户付款时选择</option>{options.data.payment_providers.map(item => <option key={`${item.name}:${item.instance_id}`} value={`${item.name}:${item.instance_id}`}>{item.label}{item.simulation ? "（模拟）" : ""}</option>)}</select></label><div className="grid min-w-0 gap-2 text-sm"><span>优惠码</span><div className="flex min-w-0 flex-wrap gap-2"><Input className="min-w-24 flex-1" aria-label="优惠码" maxLength={120} value={form.promo} onChange={event => change({ promo: event.target.value })} placeholder="输入优惠码" />{promoView && <Button type="button" variant="outline" onClick={() => setPromoDirectoryOpen(true)}>选择</Button>}{promoManage && <Button type="button" variant="outline" onClick={() => setCreatePromoOpen(true)}>新增</Button>}</div><p className="text-xs text-muted-foreground">优惠码由后端在报价时按客户、商品和使用规则验证。</p></div></div>
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
      <aside className="min-w-0 rounded border bg-background p-4 space-y-4 self-start xl:sticky xl:top-20"><h2 className="font-medium">订单详情</h2>{currentQuote ? <><div className="divide-y">{currentQuote.items.map((item,index) => <div key={`${item.product_id}-${index}`} className="py-3 text-sm space-y-1"><p className="font-medium break-words">{item.name} × {item.quantity}</p><p className="text-xs text-muted-foreground">{cycles[item.billing_cycle] ?? item.billing_cycle} · 首次基价 {money(item.unit_first_cents)} / 件（券优惠见明细）</p><p className="text-xs text-muted-foreground">续费 {money(item.unit_recurring_cents)} / 件</p><PurchaseDiscountDetails pricing={item.pricing} compact/><p className="text-right tabular-nums">{money(item.total_cents)}</p></div>)}</div><dl className="space-y-3 text-sm"><Amount label="小计" value={currentQuote.subtotal_cents} /><PurchaseDiscountSummary discountCents={currentQuote.discount_cents} groupDiscountCents={currentQuote.group_discount_cents} promoDiscountCents={currentQuote.promo_discount_cents} promoCode={currentQuote.promo_code}/><Amount label="总计" value={currentQuote.total_cents} /></dl><p className="text-xs text-muted-foreground">{currentQuote.review_status === "pending" ? "订单需审核" : "订单已确认"} · {currentQuote.generate_invoice ? "生成待支付账单" : "账单延期生成"}</p></> : <p className="py-6 text-sm text-muted-foreground">{form.lines.length ? "尚未报价，或订单信息已变更。请重新报价后提交。" : "未选择项目"}</p>}
        {notice && <p role="status" className="text-sm text-muted-foreground break-words">{notice}</p>}{error && <p role="alert" className="text-sm text-destructive break-words">{error}</p>}{promoConflict && form.promo && <Button type="button" variant="outline" className="w-full" disabled={frozen} onClick={() => applyPromo("")}>移除优惠码并重新报价</Button>}
        <Button type="submit" className="w-full" disabled={frozen || !form.customer || !form.lines.length}><Calculator size={16} />{busy === "quote" ? "报价中…" : "重新报价"}</Button><Button type="button" className="w-full" disabled={busy !== null || !currentQuote} onClick={() => setConfirm(true)}>{uncertain ? "使用原请求重试" : "提交订单"}</Button><Button type="button" className="w-full" variant="outline" disabled={frozen} onClick={cancel}>取消</Button>
      </aside>
    </form>}
    <PromoDirectoryDialog open={promoDirectoryOpen} onOpenChange={setPromoDirectoryOpen} query={promoQuery} onQueryChange={value => { setPromoQuery(value); setPromoOffset(0); }} status={promoStatus} onStatusChange={value => { setPromoStatus(value); setPromoOffset(0); }} page={promoDirectory.data} error={promoDirectory.error} refresh={promoDirectory.refresh} offset={promoOffset} onOffsetChange={setPromoOffset} onChoose={applyPromo} />
    <PromoCodeCreateDialog key={context.actorId} actorID={context.actorId} open={createPromoOpen} onOpenChange={setCreatePromoOpen} onCreated={applyPromo} />
    <Dialog open={confirm} onOpenChange={open => { if (busy !== "submit") setConfirm(open); }}><DialogContent className="max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>确认提交订单</DialogTitle><DialogDescription>请确认客户、产品配置和最终金额。提交后可在订单详情查看账单与邮件投递结果。</DialogDescription></DialogHeader><div className="text-sm space-y-3"><p className="break-words">客户：{form.customer?.name} · {form.customer?.email}</p><p>商品：{form.lines.length} 个产品项目，共 {totalQuantity} 项服务</p><p className="font-semibold">总计：{currentQuote ? money(currentQuote.total_cents) : "请重新报价"}</p><p>{form.generate_invoice ? "生成账单" : "暂不生成账单"} · {form.send_email ? "请求发送订单邮件" : "不发送订单邮件"}</p></div><DialogFooter><Button variant="outline" type="button" disabled={busy === "submit"} onClick={() => setConfirm(false)}>取消</Button><Button type="button" disabled={busy !== null || !currentQuote} onClick={submit}>{busy === "submit" ? "提交中…" : "确定"}</Button></DialogFooter></DialogContent></Dialog>
  </div>;
}

function promoUnavailableReason(item: PromoCode): string {
  if (!item.active) return "已停用";
  const now = Date.now();
  if (item.starts_at && Date.parse(item.starts_at) > now) return "尚未开始";
  if (item.expires_at && Date.parse(item.expires_at) <= now) return "已过期";
  if (item.max_uses > 0 && item.used_count >= item.max_uses) return "已达到使用上限";
  return "";
}

function promoDiscount(item: PromoCode): string {
  if (item.discount_type === "percent") return `优惠 ${item.value}%`;
  if (item.discount_type === "fixed") return `优惠 ${money(item.value)}`;
  return "免初装费";
}

function PromoDirectoryDialog({ open, onOpenChange, query, onQueryChange, status, onStatusChange, page, error, refresh, offset, onOffsetChange, onChoose }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  query: string;
  onQueryChange: (value: string) => void;
  status: string;
  onStatusChange: (value: string) => void;
  page: Page<PromoCode> | null;
  error: Error | null;
  refresh: () => void;
  offset: number;
  onOffsetChange: (value: number) => void;
  onChoose: (code: string) => void;
}) {
  return <Dialog open={open} onOpenChange={onOpenChange}><DialogContent className="overflow-y-auto sm:max-w-2xl" style={{ maxHeight: "90dvh" }}><DialogHeader><DialogTitle>选择优惠码</DialogTitle><DialogDescription>目录只显示非升级专用优惠。客户、商品和账期限制仍由服务端报价校验。</DialogDescription></DialogHeader>
    <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_9rem]"><label className="grid gap-2 text-sm">搜索优惠码<Input aria-label="搜索优惠码目录" value={query} maxLength={120} onChange={event => onQueryChange(event.target.value)} placeholder="输入代码或备注" /></label><label className="grid gap-2 text-sm">状态<select aria-label="优惠码状态筛选" className={selectClass} value={status} onChange={event => onStatusChange(event.target.value)}><option value="active">已启用</option><option value="inactive">已停用</option><option value="all">全部状态</option></select></label></div>
    {error ? <Failure error={error} retry={refresh} /> : !page ? <Loading /> : <><div className="max-h-[48dvh] space-y-2 overflow-y-auto" aria-live="polite">{page.items.length === 0 ? <p className="rounded border py-10 text-center text-sm text-muted-foreground">没有匹配的优惠码</p> : page.items.map(item => {
      const unavailable = promoUnavailableReason(item);
      return <article key={item.id} className="flex min-w-0 flex-wrap items-start justify-between gap-3 rounded border p-3"><div className="min-w-0 flex-1"><div className="flex flex-wrap items-center gap-x-2 gap-y-1"><span className="font-mono font-semibold break-all">{item.code}</span><span className="text-sm text-primary">{promoDiscount(item)}</span><span className={`text-xs ${unavailable ? "text-muted-foreground" : "text-emerald-700"}`}>{unavailable || "基础状态正常"}</span></div><p className="mt-1 text-xs text-muted-foreground">{item.recurring ? `循环续费${item.recurring_limit > 0 ? ` · ${item.recurring_limit} 次` : " · 不限次数"}` : "首购优惠"}{item.once_per_customer ? " · 每客户一次" : ""}{item.only_new_customer ? " · 仅新客户" : item.only_old_customer ? " · 仅老客户" : ""}</p>{(item.applies_product_ids.length > 0 || item.requires_product_ids.length > 0 || item.cycles.length > 0 || item.notes) && <p className="mt-1 break-words text-xs text-muted-foreground">{item.applies_product_ids.length > 0 ? `指定商品 ID：${item.applies_product_ids.join(", ")}` : "适用于全部商品"}{item.requires_product_ids.length > 0 ? ` · 需拥有商品 ID：${item.requires_product_ids.join(", ")}` : ""}{item.cycles.length > 0 ? ` · 账期：${item.cycles.map(cycle => cycles[cycle] ?? cycle).join("、")}` : ""}{item.notes ? ` · ${item.notes}` : ""}</p>}<p className="mt-1 text-xs text-muted-foreground">{item.applies_customer_group_ids?.length?`指定客户组 ${item.applies_customer_group_ids.map(id=>`#${id}`).join("、")}；在组折后再优惠`:"全部客户；不能与客户组优惠叠加"}</p><p className="mt-1 text-xs text-muted-foreground">已使用 {item.used_count}{item.max_uses > 0 ? ` / ${item.max_uses}` : " / 不限"}</p></div><Button type="button" size="sm" variant="outline" disabled={!!unavailable} onClick={() => onChoose(item.code)}>应用</Button></article>;
    })}</div><div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground"><span>共 {page.total} 个优惠码</span><div className="flex gap-2"><Button type="button" size="sm" variant="outline" disabled={offset === 0} onClick={() => onOffsetChange(Math.max(0, offset - page.limit))}>上一页</Button><Button type="button" size="sm" variant="outline" disabled={offset + page.limit >= page.total} onClick={() => onOffsetChange(offset + page.limit)}>下一页</Button></div></div></>}
    <DialogFooter><Button type="button" variant="outline" onClick={() => onOpenChange(false)}>关闭</Button></DialogFooter>
  </DialogContent></Dialog>;
}

type NewPromoDraft = { appliesCustomerGroupIDs:number[]; code: string; discountType: "percent" | "fixed" | "free"; value: string; maxUses: string; audience: "all" | "new" | "old"; oncePerCustomer: boolean; recurring: boolean; recurringLimit: string; cycles: string[]; notes: string };
const blankPromoDraft = (): NewPromoDraft => ({ appliesCustomerGroupIDs:[], code: "", discountType: "percent", value: "10", maxUses: "0", audience: "all", oncePerCustomer: false, recurring: false, recurringLimit: "0", cycles: [], notes: "" });

type NewPromoAttempt = {key:string;body:string;draft:NewPromoDraft};
function PromoCodeCreateDialog({ actorID, open, onOpenChange, onCreated }: { actorID?:number; open: boolean; onOpenChange: (open: boolean) => void; onCreated: (code: string) => void }) {
  const [draft, setDraft] = useState<NewPromoDraft>(blankPromoDraft);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [pending, setPending] = useState<NewPromoAttempt|null>(null);
  const [rejected, setRejected] = useState(false);
  const attempt = useRef<NewPromoAttempt|null>(null);
  const sending = useRef(false);
  const storageKey = `zjmfgo:order-promo-pending:v1:${actorID??"unavailable"}`;
  useEffect(()=>{
    if(!actorID)return;
    try {
      const raw=sessionStorage.getItem(storageKey);
      if(!raw)return;
      const saved=JSON.parse(raw) as NewPromoAttempt;
      if(typeof saved.key==="string"&&typeof saved.body==="string"&&saved.draft&&typeof saved.draft.code==="string"&&Array.isArray(saved.draft.cycles)) {
        const body=JSON.parse(saved.body);
        if(!body||typeof body!=="object"||typeof body.code!=="string")return;
        attempt.current=saved;
        queueMicrotask(()=>{setPending(saved);setDraft({...blankPromoDraft(),...saved.draft});});
      }
    }catch{/* Never replay an unverified stored request automatically. */}
  },[actorID,storageKey]);
  function clearAttempt(){try{sessionStorage.removeItem(storageKey);}catch{/* A confirmed response remains authoritative if storage becomes unavailable. */}attempt.current=null;setPending(null);setRejected(false);}
  function update(patch: Partial<NewPromoDraft>) { setDraft(current => ({ ...current, ...patch })); setError(""); }
  function changeOpen(next: boolean) { if (busy) return; if (!next&&!pending) { setDraft(blankPromoDraft()); setError(""); } onOpenChange(next); }
  async function create(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (sending.current||rejected||!actorID) return;
    sending.current=true;
    setBusy(true); setError("");
    try {
      const current=attempt.current??{key:crypto.randomUUID(),draft,body:JSON.stringify({ code: draft.code.trim().toUpperCase(), discount_type: draft.discountType, value: draft.discountType === "free" ? 0 : Number(draft.value), max_uses: Number(draft.maxUses), only_new_customer: draft.audience === "new", only_old_customer: draft.audience === "old", once_per_customer: draft.oncePerCustomer, recurring: draft.discountType !== "free" && draft.recurring, recurring_limit: draft.recurring && draft.discountType !== "free" ? Number(draft.recurringLimit) : 0, cycles: draft.cycles, upgrade_only: false, notes: draft.notes.trim(),applies_customer_group_ids:draft.appliesCustomerGroupIDs })};
      sessionStorage.setItem(storageKey,JSON.stringify(current));
      attempt.current=current;
      setPending(current);
      const result = await api<{ id: number; code: string }>("/admin/promo-codes", { method: "POST",headers:{"Idempotency-Key":current.key},body:current.body });
      clearAttempt();
      setDraft(blankPromoDraft());
      onOpenChange(false);
      onCreated(result.code);
    } catch (cause) {
      setError((cause as Error).message);
      if(cause instanceof ApiError&&cause.status>=400&&cause.status<500&&![408,429].includes(cause.status)){
        if(cause.code==="idempotency_conflict")setRejected(true);
        else clearAttempt();
      }
    }
    finally { sending.current=false;setBusy(false); }
  }
  const cycleOptions = [["monthly", "月付"], ["quarterly", "季付"], ["annually", "年付"], ["once", "一次性"]] as const;
  return <Dialog open={open} onOpenChange={changeOpen}><DialogContent className="overflow-y-auto sm:max-w-2xl" style={{ maxHeight: "90dvh" }}><DialogHeader><DialogTitle>新增优惠码</DialogTitle><DialogDescription>创建后会自动选入当前订单。报价仍会检查客户、商品、账期、时间与剩余次数。</DialogDescription></DialogHeader><form className="space-y-4" onSubmit={event => void create(event)}><fieldset disabled={busy||!!pending} className="space-y-4">
    <div className="grid gap-4 sm:grid-cols-2"><label className="grid gap-2 text-sm">优惠码<Input aria-label="新优惠码" required pattern="[A-Za-z0-9](?:[A-Za-z0-9_]|-){2,49}" maxLength={50} autoComplete="off" value={draft.code} onChange={event => update({ code: event.target.value.toUpperCase() })} placeholder="如 NEWUSER10" /></label><label className="grid gap-2 text-sm">优惠类型<select aria-label="新优惠码类型" className={selectClass} value={draft.discountType} onChange={event => { const discountType = event.target.value as NewPromoDraft["discountType"]; update({ discountType, ...(discountType === "free" ? { recurring: false, recurringLimit: "0" } : {}) }); }}><option value="percent">百分比</option><option value="fixed">固定金额（分）</option><option value="free">免初装费</option></select></label>
      <label className="grid gap-2 text-sm">{draft.discountType === "percent" ? "折扣比例（%）" : draft.discountType === "fixed" ? "固定优惠金额（分）" : "优惠值"}<Input aria-label="新优惠码优惠值" type="number" inputMode="numeric" min={draft.discountType === "free" ? 0 : 1} max={draft.discountType === "percent" ? 100 : undefined} required disabled={draft.discountType === "free"} value={draft.value} onChange={event => update({ value: event.target.value })} /></label><label className="grid gap-2 text-sm">最多使用次数<Input aria-label="新优惠码最多使用次数" type="number" min={0} max={1000000000} required value={draft.maxUses} onChange={event => update({ maxUses: event.target.value })} /><span className="text-xs text-muted-foreground">0 表示不限次数</span></label>
      <label className="grid gap-2 text-sm">客户范围<select aria-label="优惠码客户范围" className={selectClass} value={draft.audience} onChange={event => update({ audience: event.target.value as NewPromoDraft["audience"] })}><option value="all">所有客户</option><option value="new">仅新客户</option><option value="old">仅老客户</option></select></label><label className="flex items-center gap-2 self-end pb-2 text-sm"><Checkbox checked={draft.oncePerCustomer} onCheckedChange={value => update({ oncePerCustomer: value === true })} />每位客户限用一次</label>
    </div>
    <PromoCustomerGroups value={draft.appliesCustomerGroupIDs??[]} onChange={ids=>update({appliesCustomerGroupIDs:ids})} disabled={busy||!!pending}/>
    <fieldset className="space-y-2"><legend className="text-sm font-medium">适用账期</legend><p className="text-xs text-muted-foreground">未选择时不限制账期。</p><div className="flex flex-wrap gap-x-5 gap-y-2">{cycleOptions.map(([cycle, label]) => <label key={cycle} className="flex items-center gap-2 text-sm"><Checkbox checked={draft.cycles.includes(cycle)} onCheckedChange={checked => update({ cycles: checked === true ? [...draft.cycles, cycle] : draft.cycles.filter(item => item !== cycle) })} />{label}</label>)}</div></fieldset>
    <div className="grid gap-4 sm:grid-cols-2"><label className="flex items-center gap-2 text-sm"><Checkbox checked={draft.recurring} disabled={draft.discountType === "free"} onCheckedChange={checked => update({ recurring: checked === true })} />用于循环续费</label>{draft.recurring && <label className="grid gap-2 text-sm">每件服务优惠续费次数<Input aria-label="优惠续费次数" type="number" min={0} max={10000} required value={draft.recurringLimit} onChange={event => update({ recurringLimit: event.target.value })} /><span className="text-xs text-muted-foreground">0 表示不限次数</span></label>}</div>
    <label className="grid gap-2 text-sm">内部备注<Textarea aria-label="优惠码内部备注" maxLength={2000} rows={2} value={draft.notes} onChange={event => update({ notes: event.target.value })} /></label>
    </fieldset>{pending&&<p role="status" className="text-sm text-muted-foreground">上次创建结果尚未确认。核对优惠码目录后，重试会保留原幂等键和请求，关闭窗口也不会自动重发。</p>}{rejected&&<Button type="button" variant="outline" disabled={busy} onClick={()=>{clearAttempt();setError("");}}>已核对，放弃原请求并重新编辑</Button>}{error && <p role="alert" className="text-sm text-destructive">{error}</p>}<DialogFooter><Button type="button" variant="outline" disabled={busy} onClick={() => changeOpen(false)}>取消</Button><Button type="submit" disabled={busy || rejected || !actorID || !draft.code.trim()}>{busy ? "创建中…" : pending?"使用原请求重试":"创建并应用"}</Button></DialogFooter>
  </form></DialogContent></Dialog>;
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
