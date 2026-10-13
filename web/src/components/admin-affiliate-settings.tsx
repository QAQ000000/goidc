"use client";

import { useEffect, useState } from "react";
import { RefreshCw, Save } from "lucide-react";
import { ApiError, api, date } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { usePendingSetting } from "@/hooks/use-pending-setting";
import { Workspace, useAdminIdentity, useAdminPermission } from "./workspace";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Input } from "./ui/input";

type Rule = { rate_bps: number; amount_cents: number; max_cents: number | null };
type Policy = { id: number; name: string; enabled: boolean; attribution_days: number; hold_days: number; min_withdraw_cents: number; fee_cents: number; default_rules: Record<string, Rule>; version: number; updated_at: string };
const events = [["first_purchase", "首次订购"], ["reorder", "二次订购"], ["renewal", "续费"], ["upgrade", "升降级"]] as const;
type Event = typeof events[number][0];
type RuleForm = { mode: "disabled" | "rate" | "fixed"; value: string; max: string };
type Form = { name: string; enabled: boolean; attribution_days: string; hold_days: string; min_withdraw: string; fee: string; rules: Record<Event, RuleForm> };
type Draft = { base_version: number; form: Form };
function toForm(data: Policy): Form {
  return { name: data.name, enabled: data.enabled, attribution_days: String(data.attribution_days), hold_days: String(data.hold_days), min_withdraw: (data.min_withdraw_cents / 100).toFixed(2), fee: (data.fee_cents / 100).toFixed(2), rules: Object.fromEntries(events.map(([event]) => { const rule = data.default_rules[event]; return [event, { mode: rule?.rate_bps ? "rate" : rule?.amount_cents ? "fixed" : "disabled", value: ((rule?.rate_bps || rule?.amount_cents || 0) / 100).toFixed(2), max: rule?.max_cents == null ? "" : (rule.max_cents / 100).toFixed(2) }]; })) as Form["rules"] };
}
function validForm(value: unknown): value is Form {
  if (!value || typeof value !== "object") return false;
  const v = value as Form;
  return typeof v.name === "string" && typeof v.enabled === "boolean" && [v.attribution_days, v.hold_days, v.min_withdraw, v.fee].every(field => typeof field === "string") && !!v.rules && events.every(([event]) => { const rule = v.rules[event]; return !!rule && ["disabled", "rate", "fixed"].includes(rule.mode) && typeof rule.value === "string" && typeof rule.max === "string"; });
}
function hundredths(value: string, label: string, maximum = 100000000): number {
  if (!/^\d{1,7}(?:\.\d{1,2})?$/.test(value)) throw new Error(`${label}必须是非负数，最多保留两位小数`);
  const result = Math.round(Number(value) * 100);
  if (result > maximum) throw new Error(`${label}超过允许范围`);
  return result;
}
function requestBody(form: Form, version: number) {
  if (!form.name.trim()) throw new Error("请输入推介计划名称");
  if (!/^\d+$/.test(form.attribution_days) || Number(form.attribution_days) < 1 || Number(form.attribution_days) > 365) throw new Error("Cookie 有效期必须为 1 至 365 天的整数");
  if (!/^\d+$/.test(form.hold_days) || Number(form.hold_days) > 365) throw new Error("佣金冻结期必须为 0 至 365 天的整数");
  const minimum = hundredths(form.min_withdraw, "最低提现金额");
  const fee = hundredths(form.fee, "提现手续费");
  if (minimum < 1 || fee >= minimum) throw new Error("最低提现金额需大于零，手续费需低于最低提现金额");
  const rules = Object.fromEntries(events.map(([event, label]) => {
    const rule = form.rules[event];
    const value = rule.mode === "disabled" ? 0 : hundredths(rule.value, `${label}返佣`, rule.mode === "rate" ? 10000 : 100000000);
    const maximum = rule.mode === "disabled" || !rule.max ? null : hundredths(rule.max, `${label}佣金上限`);
    if (rule.mode !== "disabled" && value === 0) throw new Error(`${label}返佣必须大于零`);
    if (maximum === 0) throw new Error(`${label}佣金上限必须大于零，或留空不限`);
    return [event, { rate_bps: rule.mode === "rate" ? value : 0, amount_cents: rule.mode === "fixed" ? value : 0, max_cents: maximum }];
  }));
  return JSON.stringify({ name: form.name.trim(), enabled: form.enabled, attribution_days: Number(form.attribution_days), hold_days: Number(form.hold_days), min_withdraw_cents: minimum, fee_cents: fee, default_rules: rules, expected_version: version });
}

export function AdminAffiliateSettingsView() {
  const resource = useResource<Policy>("/admin/affiliate/policy", false);
  const [notice, setNotice] = useState("");
  return <Workspace title="推介设置" admin><div className="space-y-6"><div><h1 className="page-title">推介设置</h1><p className="mt-2 text-sm text-muted-foreground">设置默认推广计划、归因周期、返佣规则与提现门槛。</p></div>{notice && <p role="status" className="text-sm text-muted-foreground">{notice}</p>}{resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <PolicyForm key={resource.data.version} data={resource.data} refresh={resource.refresh} notify={setNotice} />}</div></Workspace>;
}

function PolicyForm({ data, refresh, notify }: { data: Policy; refresh: () => void; notify: (message: string) => void }) {
  const actor = useAdminIdentity();
  const manage = useAdminPermission("affiliates.manage");
  const storageKey = actor ? `zjmfgo:affiliate-policy-draft:${actor.id}` : null;
  const pending = usePendingSetting("affiliate-policy", actor?.id ?? null);
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
  function cancel() { if (!window.confirm("确认取消更改并丢弃推介设置草稿？")) return; setForm(toForm(data)); clearDraft(); setError(""); setMessage(""); setSaved(false); }
  function recover() { if (!draft || (draft.base_version !== data.version && !window.confirm("草稿版本与当前设置不同，确认恢复并逐项核对？"))) return; update(draft.form); clearDraft(); setError(""); }
  async function save(event: React.FormEvent) {
    event.preventDefault(); if (!manage || !loaded || busy || (!dirty && !unknown)) return;
    let body: string;
    try { body = unknown && pending.pending.current ? pending.pending.current.body : requestBody(form, data.version); } catch (cause) { setError((cause as Error).message); return; }
    if (!unknown && data.enabled && !form.enabled && !window.confirm("关闭后将停止新的推广申请、点击归因、计佣与提现申请；已申请提现继续审核打款。确认关闭推介计划？")) return;
    const operation = unknown && pending.pending.current ? pending.pending.current : { body, key: `affiliate-policy-${typeof crypto !== "undefined" && "randomUUID" in crypto ? crypto.randomUUID() : `${Date.now()}-${Math.random()}`}`, path: "/admin/affiliate/policy" };
    pending.remember(operation); setBusy(true); setError(""); setMessage("");
    try {
      const result = await api<Policy>("/admin/affiliate/policy", { method: "PUT", headers: { "Idempotency-Key": operation.key }, body: operation.body });
      pending.resolve(); clearDraft(); setForm(toForm(result)); setSaved(true); notify(`推介设置已保存（版本 ${result.version}）`); refresh();
    } catch (cause) {
      if (!(cause instanceof ApiError) || cause.status >= 500) { pending.setUnknown(true); setMessage("保存结果暂无法确认，请仅重试原请求，勿刷新或离开页面。"); }
      else { pending.resolve(); setError(cause.message); if (cause.code === "version_conflict") { if (storageKey) { try { sessionStorage.setItem(storageKey, JSON.stringify({ base_version: data.version, form } satisfies Draft)); } catch {} } notify("设置版本已被其他管理员更新，草稿已保留。请重新加载并对照最新值后恢复。"); refresh(); } }
    } finally { setBusy(false); }
  }
  return <form onSubmit={save} className="max-w-5xl space-y-6">
    {draft && !unknown && <div className="flex flex-wrap items-center justify-between gap-3 rounded-md border bg-muted/30 p-4"><p className="text-sm">发现未保存的会话草稿{draft.base_version !== data.version ? "，当前服务器版本已更新，请核对后恢复。" : "。"}</p><div className="flex gap-2"><Button type="button" variant="outline" disabled={busy || !loaded} onClick={recover}>恢复草稿</Button><Button type="button" variant="ghost" disabled={busy || !loaded} onClick={clearDraft}>丢弃草稿</Button></div></div>}
    <fieldset disabled={!manage || !loaded || busy || unknown} className="space-y-6">
      <section className="space-y-4"><h2 className="border-b pb-3 font-semibold">计划与归因</h2><label className="flex items-center gap-3 text-sm"><Checkbox checked={form.enabled} onCheckedChange={checked => update({ ...form, enabled: checked === true })} />启用推介计划</label><p className="text-xs text-muted-foreground">停用后停止新推广申请、点击归因、计佣及提现申请；已入账佣金继续保留，已申请提现可继续审核打款。</p><div className="grid gap-4 sm:grid-cols-3"><label className="grid gap-2 text-sm">计划名称<Input required maxLength={120} value={form.name} onChange={event => update({ ...form, name: event.target.value })} /></label><label className="grid gap-2 text-sm">推荐链接 Cookie 有效期（天）<Input required type="number" min={1} max={365} step={1} value={form.attribution_days} onChange={event => update({ ...form, attribution_days: event.target.value })} /></label><label className="grid gap-2 text-sm">佣金冻结期（天）<Input required type="number" min={0} max={365} step={1} value={form.hold_days} onChange={event => update({ ...form, hold_days: event.target.value })} /></label></div></section>
      <section className="space-y-4"><h2 className="border-b pb-3 font-semibold">默认返佣规则</h2><p className="text-xs text-muted-foreground">比例按实际付款净额计算，最多 100%。商品专属规则继续优先；保存生成新版本，已入账佣金不重新计算。</p>{events.map(([event, label]) => { const rule = form.rules[event]; const change = (next: RuleForm) => update({ ...form, rules: { ...form.rules, [event]: next } }); return <div key={event} className="grid items-end gap-4 border-b pb-4 sm:grid-cols-3"><label className="grid gap-2 text-sm">{label}返佣类型<select className="h-9 rounded-md border bg-background px-3" value={rule.mode} onChange={e => change({ ...rule, mode: e.target.value as RuleForm["mode"], value: "0.00" })}><option value="disabled">关闭默认返佣</option><option value="rate">比例</option><option value="fixed">固定金额</option></select></label>{rule.mode !== "disabled" && <><label className="grid gap-2 text-sm">{label}{rule.mode === "rate" ? "返佣比例（%）" : "返佣金额（元）"}<Input required inputMode="decimal" value={rule.value} onChange={e => change({ ...rule, value: e.target.value })} /></label><label className="grid gap-2 text-sm">{label}佣金上限（元）<Input inputMode="decimal" placeholder="留空不限" value={rule.max} onChange={e => change({ ...rule, max: e.target.value })} /></label></>}</div>; })}</section>
      <section className="space-y-4"><h2 className="border-b pb-3 font-semibold">提现门槛</h2><div className="grid gap-4 sm:grid-cols-2"><label className="grid gap-2 text-sm">最低提现金额（元）<Input required inputMode="decimal" value={form.min_withdraw} onChange={event => update({ ...form, min_withdraw: event.target.value })} /></label><label className="grid gap-2 text-sm">每次提现手续费（元）<Input required inputMode="decimal" value={form.fee} onChange={event => update({ ...form, fee: event.target.value })} /></label></div></section>
    </fieldset>
    {(error || message) && <p role={error ? "alert" : "status"} className={`break-words text-sm ${error ? "text-destructive" : "text-muted-foreground"}`}>{error || message}</p>}
    <div className="flex flex-wrap items-center justify-between gap-3 border-t pt-4"><p className="text-xs text-muted-foreground">版本 {data.version} · 最后更新 {date(data.updated_at)}{saved && " · 已保存"}</p><div className="flex flex-wrap gap-2"><Button type="button" variant="outline" disabled={!loaded || busy || unknown} onClick={() => { if (!dirty || window.confirm("重新加载服务器设置？当前修改会保留为会话草稿。")) refresh(); }}><RefreshCw size={16} />重新加载</Button><Button type="button" variant="outline" disabled={!loaded || busy || unknown || !dirty} onClick={cancel}>取消更改</Button><Button disabled={!manage || !loaded || busy || (!dirty && !unknown)}><Save size={16} />{busy ? "保存中…" : unknown ? "重试原请求" : "保存更改"}</Button></div></div>
  </form>;
}
