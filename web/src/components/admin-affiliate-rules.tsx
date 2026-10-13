"use client";
import { useEffect, useState } from "react";
import { RefreshCw, Save } from "lucide-react";
import { ApiError, api } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { usePendingSetting } from "@/hooks/use-pending-setting";
import { useAdminIdentity, useAdminPermission } from "./workspace";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
const events = [["first_purchase", "首次订购"], ["reorder", "重购"], ["renewal", "续费"], ["upgrade", "升级 / 降级"]];
type Mode = "inherit" | "disabled" | "rate" | "fixed";
type Rule = { product_id?: number; event_type?: string; mode?: Mode; lower_cents?: number; upper_cents?: number | null; rate_bps: number; amount_cents: number; max_cents?: number | null };
type Rules = { version: number; product_rules: Rule[]; tier_rules: Rule[] };
type Row = { product: string; event: string; mode: Mode; value: string; max: string; lower: string; upper: string };
type Form = { products: Row[]; tiers: Row[] };
type Draft = { base_version: number; form: Form };
type Schema = { products: { id: number; name: string; slug: string }[] };
const blank = (): Row => ({ product: "", event: "first_purchase", mode: "inherit", value: "0.00", max: "", lower: "0.00", upper: "" });
function toForm(data: Rules): Form {
 const row = (r: Rule): Row => ({ ...blank(), product: r.product_id ? String(r.product_id) : "", event: r.event_type || "first_purchase", mode: r.mode || (r.rate_bps ? "rate" : r.amount_cents ? "fixed" : "disabled"), value: ((r.rate_bps || r.amount_cents) / 100).toFixed(2), max: r.max_cents == null ? "" : (r.max_cents / 100).toFixed(2), lower: ((r.lower_cents || 0) / 100).toFixed(2), upper: r.upper_cents == null ? "" : (r.upper_cents / 100).toFixed(2) });
 return { products: data.product_rules.map(row), tiers: data.tier_rules.map(row) };
}
function validForm(v: unknown): v is Form {
 if (!v || typeof v !== "object") return false;
 const f = v as Form;
 return [f.products, f.tiers].every(rows => Array.isArray(rows) && rows.every(r => r && ["product", "event", "value", "max", "lower", "upper"].every(k => typeof r[k as keyof Row] === "string") && ["inherit", "disabled", "rate", "fixed"].includes(r.mode)));
}
function cents(v: string, label: string, max = 100000000) {
 if (!/^\d{1,7}(?:\.\d{1,2})?$/.test(v)) throw new Error(`${label}需为非负数，最多两位小数`);
 const n = Math.round(Number(v) * 100); if (n > max) throw new Error(`${label}超过允许范围`); return n;
}
function requestBody(form: Form, version: number, schema: Schema) {
 if (form.products.length > 2000 || form.tiers.length > 200) throw new Error("最多 2000 条商品规则、200 个阶梯");
 const seen = new Set<string>();
 const reward = (r: Row) => { const active = r.mode === "rate" || r.mode === "fixed"; const value = active ? cents(r.value, "返佣", r.mode === "rate" ? 10000 : 100000000) : 0; if (active && !value) throw new Error("返佣需大于零"); return { rate_bps: r.mode === "rate" ? value : 0, amount_cents: r.mode === "fixed" ? value : 0 }; };
 const product_rules = form.products.map(r => {
  if (!schema.products.some(p => String(p.id) === r.product)) throw new Error("请选择有效商品");
  if (!events.some(([e]) => e === r.event)) throw new Error("请选择有效事件");
  const key = `${r.product}:${r.event}`; if (seen.has(key)) throw new Error("同一商品与事件不可重复"); seen.add(key);
  const max = (r.mode === "rate" || r.mode === "fixed") && r.max ? cents(r.max, "佣金上限") : null; if (max === 0) throw new Error("佣金上限需大于零或留空");
  return { product_id: Number(r.product), event_type: r.event, mode: r.mode, ...reward(r), max_cents: max };
 });
 const tier_rules = form.tiers.map(r => { const lower = cents(r.lower, "阶梯下限"), upper = r.upper ? cents(r.upper, "阶梯上限") : null; if (upper !== null && upper <= lower) throw new Error("阶梯上限需高于下限"); if (r.mode === "inherit") throw new Error("阶梯不可使用继承模式"); return { lower_cents: lower, upper_cents: upper, ...reward(r) }; }).sort((a,b) => a.lower_cents - b.lower_cents);
 for (let i=1; i<tier_rules.length; i++) if (tier_rules[i-1].upper_cents === null || tier_rules[i].lower_cents < tier_rules[i-1].upper_cents!) throw new Error("阶梯区间不可重叠，无限上限只能位于最后一档");
 return JSON.stringify({ expected_version: version, product_rules, tier_rules });
}
export function AffiliateRulesPanel({ onSaved }: { onSaved: () => void }) {
 const view = useAdminPermission("affiliates.view");
 const resource = useResource<Rules>(view ? "/admin/affiliate/rules" : null, false);
 const schema = useResource<Schema>(view ? "/admin/affiliate/rules/schema" : null, false);
 const [notice, setNotice] = useState("");
 if (!view) return <p role="alert">无权读取推介规则。</p>;
 return <div className="space-y-4">{notice && <p role="status">{notice}</p>}{resource.error || schema.error ? <Failure error={resource.error || schema.error!} retry={() => {resource.refresh();schema.refresh();}} /> : !resource.data || !schema.data ? <Loading /> : <RulesForm key={resource.data.version} data={resource.data} schema={schema.data} refresh={resource.refresh} notify={message => {setNotice(message); if (message.includes("已保存")) onSaved();}} />}</div>;
}
function RulesForm({ data, refresh, notify, schema }: { data: Rules; refresh: () => void; notify: (message: string) => void; schema: Schema }) {
  const actor = useAdminIdentity();
  const manage = useAdminPermission("affiliates.manage");
  const storageKey = actor ? `zjmfgo:affiliate-rules-draft:${actor.id}` : null;
  const pending = usePendingSetting("affiliate-rules", actor?.id ?? null);
  const [form, setForm] = useState<Form>(() => toForm(data));
  const [draft, setDraft] = useState<Draft | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [saved, setSaved] = useState(false);
  const dirty = JSON.stringify(form) !== JSON.stringify(toForm(data));
  const unknown = pending.unknown;
  useEffect(() => {
    if (!pending.ready) return;
    const timer = window.setTimeout(() => {
      if (pending.unknown && pending.pending.current) {
        try {
          const request = JSON.parse(pending.pending.current.body);
          const restored = toForm({ ...data, ...request });
          if (validForm(restored)) setForm(restored);
        } catch { /* Keep the immutable request available for retry. */ }
        setMessage("上次保存结果暂无法确认，请重试原请求后继续编辑。");
      } else if (storageKey && manage) {
        try { const raw = sessionStorage.getItem(storageKey); const value = raw ? JSON.parse(raw) as Draft : null; if (value && Number.isInteger(value.base_version) && validForm(value.form)) setDraft(value); } catch { /* Optional draft storage. */ }
      }
      setLoaded(true);
    }, 0);
    return () => window.clearTimeout(timer);
  }, [pending.ready, pending.unknown, pending.pending, storageKey, manage, data]);
  useEffect(() => {
    if (!loaded || !storageKey || !manage || unknown || saved || !dirty) return;
    try { sessionStorage.setItem(storageKey, JSON.stringify({ base_version: data.version, form } satisfies Draft)); } catch { /* Optional draft storage. */ }
  }, [loaded, storageKey, manage, unknown, saved, dirty, data.version, form]);
  useEffect(() => {
    if (!loaded || (!dirty && !unknown)) return;
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    const click = (event: MouseEvent) => { const anchor = event.target instanceof Element ? event.target.closest("a[href]") : null; if (anchor && !event.ctrlKey && !event.metaKey && !event.shiftKey && (unknown || !window.confirm("修改尚未保存，确认离开？会话草稿会保留。"))) { event.preventDefault(); event.stopPropagation(); } };
    window.addEventListener("beforeunload", warn); document.addEventListener("click", click, true);
    return () => { window.removeEventListener("beforeunload", warn); document.removeEventListener("click", click, true); };
  }, [loaded, dirty, unknown]);
  function clearDraft() { if (storageKey) { try { sessionStorage.removeItem(storageKey); } catch {} } setDraft(null); }
  function update(next: Form) { notify(""); setForm(next); setSaved(false); setMessage(""); }
  function cancel() { if (!window.confirm("确认取消更改并丢弃商品与阶梯规则草稿？")) return; setForm(toForm(data)); clearDraft(); setError(""); setMessage(""); setSaved(false); }
  function recover() { if (!draft || (draft.base_version !== data.version && !window.confirm("草稿版本与当前设置不同，确认恢复并逐项核对？"))) return; update(draft.form); clearDraft(); setError(""); }
  async function save(event: React.FormEvent) {
    event.preventDefault(); if (!manage || !loaded || busy || (!dirty && !unknown)) return;
    let body: string;
    try { body = unknown && pending.pending.current ? pending.pending.current.body : requestBody(form, data.version, schema); } catch (cause) { setError((cause as Error).message); return; }
    if (!unknown && !window.confirm("确认保存全部规则？历史佣金不会重算。")) return;
    const operation = unknown && pending.pending.current ? pending.pending.current : { body, key: `affiliate-rules-${typeof crypto !== "undefined" && "randomUUID" in crypto ? crypto.randomUUID() : `${Date.now()}-${Math.random()}`}`, path: "/admin/affiliate/rules" };
    pending.remember(operation); setBusy(true); setError(""); setMessage("");
    try {
      const result = await api<Rules>("/admin/affiliate/rules", { method: "PUT", headers: { "Idempotency-Key": operation.key }, body: operation.body });
      pending.resolve(); clearDraft(); setForm(toForm(result)); setSaved(true); notify(`商品与阶梯规则已保存（版本 ${result.version}）`); refresh();
    } catch (cause) {
      if (!(cause instanceof ApiError) || cause.status >= 500) { pending.setUnknown(true); setMessage("保存结果暂无法确认，请仅重试原请求，勿刷新或离开页面。"); }
      else { pending.resolve(); setError(cause.message); if (cause.code === "version_conflict") { if (storageKey) { try { sessionStorage.setItem(storageKey, JSON.stringify({ base_version: data.version, form } satisfies Draft)); } catch {} } notify("设置版本已被其他管理员更新，草稿已保留。请重新加载并对照最新值后恢复。"); refresh(); } }
    } finally { setBusy(false); }
  }
  const change = (kind: keyof Form, i: number, patch: Partial<Row>) => update({ ...form, [kind]: form[kind].map((r,n) => n === i ? {...r,...patch} : r) });
  const selectClass = "h-9 w-full min-w-0 rounded-md border bg-background px-2";
  return <form onSubmit={save} className="max-w-5xl space-y-6">
    <p className="text-sm text-muted-foreground">商品专属规则优先；继承时按推广人上海自然月营业额（含当前到账净额）匹配阶梯，未匹配使用默认规则。阶梯含下限、不含上限，留空上限表示不限，返佣保留默认事件的佣金上限；商品专属使用自身上限。历史佣金不重算。</p>
    {draft && !unknown && <div className="flex flex-wrap gap-3 rounded-md border p-4"><p className="text-sm">发现会话草稿{draft.base_version !== data.version && "，服务器版本已更新，请核对"}。</p><Button type="button" variant="outline" disabled={busy || !loaded || !manage} onClick={recover}>恢复草稿</Button><Button type="button" variant="ghost" disabled={busy || !loaded || !manage} onClick={clearDraft}>丢弃草稿</Button></div>}
    <fieldset disabled={!manage || !loaded || busy || unknown} className="min-w-0 space-y-6">
      {(["products", "tiers"] as const).map(kind => <section key={kind} className="space-y-4"><div className="flex flex-wrap items-center justify-between gap-3"><h2 className="font-semibold">{kind === "products" ? "商品专属规则" : "月营业额阶梯"}</h2><Button type="button" variant="outline" disabled={form[kind].length >= (kind === "products" ? 2000 : 200) || (kind === "products" && !schema.products.length)} onClick={() => update({...form, [kind]: [...form[kind], {...blank(), mode: kind === "products" ? "inherit" : "rate"}]})}>{kind === "products" ? "添加商品规则" : "添加阶梯"}</Button></div>
      {kind === "products" && !schema.products.length && <p className="text-sm text-muted-foreground">暂无可选商品，请先创建商品。</p>}
      {!form[kind].length && <p className="rounded-md border border-dashed p-6 text-sm text-muted-foreground">{kind === "products" ? "暂无商品专属规则，商品使用阶梯或默认规则。" : "暂无阶梯，继承的商品使用默认规则。"}</p>}
      {form[kind].map((r,i) => <div key={i} className="grid min-w-0 gap-4 rounded-md border p-4 sm:grid-cols-2 lg:grid-cols-3">
       {kind === "products" ? <><label className="grid min-w-0 gap-2 text-sm">规则 {i+1} · 商品<select required className={selectClass} value={r.product} onChange={e => change(kind,i,{product:e.target.value})}><option value="">请选择商品</option>{r.product && !schema.products.some(p=>String(p.id)===r.product) && <option value={r.product}>商品 #{r.product}（已不可选）</option>}{schema.products.map(p=><option key={p.id} value={p.id}>{p.name} · #{p.id}</option>)}</select></label><label className="grid gap-2 text-sm">事件<select className={selectClass} value={r.event} onChange={e=>change(kind,i,{event:e.target.value})}>{events.map(([v,label])=><option key={v} value={v}>{label}</option>)}</select></label></> : <><label className="grid gap-2 text-sm">阶梯 {i+1} · 下限（元，含）<Input required inputMode="decimal" value={r.lower} onChange={e=>change(kind,i,{lower:e.target.value})}/></label><label className="grid gap-2 text-sm">上限（元，不含）<Input inputMode="decimal" placeholder="留空不限" value={r.upper} onChange={e=>change(kind,i,{upper:e.target.value})}/></label></>}
       <label className="grid gap-2 text-sm">返佣类型<select className={selectClass} value={r.mode} onChange={e=>change(kind,i,{mode:e.target.value as Mode,value:"0.00",max:""})}>{kind === "products" && <option value="inherit">继承阶梯 / 默认</option>}<option value="disabled">{kind === "products" ? "禁用返佣" : "零佣金"}</option><option value="rate">比例</option><option value="fixed">固定金额</option></select></label>
       {(r.mode === "rate" || r.mode === "fixed") && <><label className="grid gap-2 text-sm">{r.mode === "rate" ? "返佣比例（%）" : "固定佣金（元）"}<Input required inputMode="decimal" value={r.value} onChange={e=>change(kind,i,{value:e.target.value})}/></label>{kind === "products" && <label className="grid gap-2 text-sm">佣金上限（元）<Input inputMode="decimal" placeholder="留空不限" value={r.max} onChange={e=>change(kind,i,{max:e.target.value})}/></label>}</>}
       <Button type="button" variant="outline" className="self-end" onClick={()=>{if(window.confirm(`确认删除${kind === "products" ? "商品规则" : "阶梯"} ${i+1}？保存后生效。`)) update({...form,[kind]:form[kind].filter((_,n)=>n!==i)});}}>删除{kind === "products" ? "商品规则" : "阶梯"} {i+1}</Button>
      </div>)}</section>)}
    </fieldset>
    {error && <p role="alert" className="break-words text-sm text-destructive">{error}</p>}{message && <p role="status" className="break-words text-sm">{message}</p>}
    <div className="flex flex-wrap items-center justify-between gap-3 border-t pt-4"><p className="text-xs text-muted-foreground">版本 {data.version}{saved && " · 已保存"}{!manage && " · 只读权限"}</p><div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={!loaded || busy || unknown} onClick={()=>{if(!dirty || window.confirm("重新加载服务器规则？当前更改保留为会话草稿。")) refresh();}}><RefreshCw size={16}/>重新加载</Button><Button type="button" variant="outline" disabled={!manage || !loaded || busy || unknown || !dirty} onClick={cancel}>取消更改</Button><Button disabled={!manage || !loaded || busy || (!dirty && !unknown)}><Save size={16}/>{busy ? "保存中…" : unknown ? "重试原请求" : "保存全部规则"}</Button></div></div>
  </form>;
}
