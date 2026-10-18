"use client";

import { useEffect, useState } from "react";
import { Receipt, Wallet, BadgePercent } from "lucide-react";
import { api, money } from "@/lib/api";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { ReportPanel } from "./report-shared";
import { ResourcePagination, ResourceTable } from "./resource-table";
import { Stat } from "./resource-page";

type Filters = { owner: string; preset: string; from: string; to: string; q: string; customer: string; product: string; invoice: string; kind: string; condition: string; offset: number };
type Rule = { group_id: number | null; group_name: string | null; version: number | null; rate_bps: number | null; tier_id: number | null; tier_version: number | null; extra_rate_bps: number };
type Line = { line_key: string; product_id: number; product_name: string; kind: string; amount_cents: number; estimated_commission_cents: number | null; confirmed_original_cents: number | null; confirmed_commission_cents: number | null; rules: Rule[]; first_received_at: string; received_at: string; credit: boolean };
type Invoice = { invoice_id: number; invoice_no: string; customer_id: number; customer_name: string; sales_owner_id: number; sales_owner_name: string; paid_at: string; received_at: string; amount_cents: number; estimated_commission_cents: number | null; unconfigured_lines: number; confirmed_invoice_original_cents: number | null; confirmed_original_cents: number | null; confirmed_commission_cents: number | null; confirmation_status: string; confirmed_sales_owner_id: number | null; items: Line[] };
type Statistic = { start_at: string; end_at: string; amount_cents: number; invoice_count: number; estimated_commission_cents: number; unconfigured_invoices: number; confirmed_commission_cents: number };
type Trend = { date: string; amount_cents: number; estimated_commission_cents: number };
type SalesPage = { items: Invoice[]; total: number; limit: number; offset: number; from: string; to: string; computed_at: string; statistics: Record<string, Statistic>; trend: Trend[]; trend_unit: "day" | "month" };
type Staff = { id: number; name: string };
const defaults: Filters = { owner: "", preset: "", from: "", to: "", q: "", customer: "", product: "", invoice: "", kind: "", condition: "", offset: 0 };
const keys: (keyof Omit<Filters, "offset">)[] = ["owner", "preset", "from", "to", "q", "customer", "product", "invoice", "kind", "condition"];
const readFilters = (): Filters => {
  const query = new URLSearchParams(typeof window === "undefined" ? "" : window.location.search);
  const next = { ...defaults };
  for (const key of keys) next[key] = query.get(`sales_${key}`) ?? "";
  next.offset = Math.max(0, Number(query.get("sales_offset")) || 0);
  return next;
};
const dateTime = (value: string) => new Date(value).toLocaleString("zh-CN", { timeZone: "Asia/Shanghai", hour12: false });
const kindLabel = (kind: string) => ({ purchase: "新购 / 初装", renew: "续费", upgrade: "升级", other: "其他 / 未归属" })[kind] ?? kind;
const confirmationLabel = (status: string) => ({ confirmed: "已确认", reversed: "已冲正", unconfirmed: "未确认" })[status] ?? status;
const estimate = (amount: number | null, incomplete = false) => amount === null ? "未配置" : `${money(amount)}${incomplete ? "（部分未配置）" : ""}`;

function SalesTrend({ points, unit }: { points: Trend[]; unit: string }) {
  const maximum = Math.max(1, ...points.map(point => point.amount_cents));
  const coordinate = (index: number, value: number) => `${12 + index / Math.max(1, points.length - 1) * 576},${112 - value / maximum * 100}`;
  return <div className="rounded-md border p-3 my-5 min-w-0">
    <h3 className="font-medium text-sm">到账趋势 · 按{unit === "month" ? "月" : "日"} · 上海时区</h3>
    <svg role="img" aria-label={`期间到账趋势，共 ${points.length} 个${unit === "month" ? "月" : "日期"}`} viewBox="0 0 600 132" className="w-full max-h-52 mt-3"><line x1="12" y1="112" x2="588" y2="112" stroke="currentColor" opacity=".2" /><polyline points={points.map((point, index) => coordinate(index, point.amount_cents)).join(" ")} fill="none" stroke="var(--primary)" strokeWidth="2" />{points.length === 1 && <circle cx="12" cy={112 - points[0].amount_cents / maximum * 100} r="3" fill="var(--primary)" />}</svg>
    <div className="flex justify-between text-xs text-muted-foreground"><span>{points[0]?.date}</span><span>最高 {money(maximum === 1 && !points.some(point => point.amount_cents) ? 0 : maximum)}</span><span>{points.at(-1)?.date}</span></div>
    <details className="mt-3 text-sm"><summary className="cursor-pointer">查看趋势明细</summary><div className="max-h-72 overflow-auto mt-2"><ResourceTable items={points} rowKey={point => point.date} columns={[{ header: "日期", render: point => point.date }, { header: "到账金额", render: point => money(point.amount_cents) }, { header: "已配置项目预计提成", render: point => money(point.estimated_commission_cents) }]} /></div></details>
  </div>;
}

export function SalesReportDetails({ year, month, exportDisabled = false }: { year: number; month: number; exportDisabled?: boolean }) {
  const [filters, setFilters] = useState<Filters>(readFilters);
  const [draft, setDraft] = useState<Filters>(readFilters);
  const [staff, setStaff] = useState<Staff[]>([]);
  const [staffError, setStaffError] = useState("");
  const [staffLoading, setStaffLoading] = useState(true);
  const [staffAttempt, setStaffAttempt] = useState(0);
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    const restore = () => { const next = readFilters(); setFilters(next); setDraft(next); };
    window.addEventListener("popstate", restore);
    return () => window.removeEventListener("popstate", restore);
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    api<{ staff: Staff[] }>("/admin/reports/sales/options", { signal: controller.signal }).then(data => {
      if (!controller.signal.aborted) { setStaff(data.staff); setStaffError(""); setStaffLoading(false); }
    }).catch(error => { if (!controller.signal.aborted) { setStaffError(error instanceof Error ? error.message : "销售选项加载失败"); setStaffLoading(false); } });
    return () => controller.abort();
  }, [staffAttempt]);
  function apply(next: Filters) {
    const query = new URLSearchParams(window.location.search);
    for (const key of keys) { if (next[key]) query.set(`sales_${key}`, next[key]); else query.delete(`sales_${key}`); }
    if (next.offset) query.set("sales_offset", String(next.offset)); else query.delete("sales_offset");
    window.history.pushState(null, "", `${window.location.pathname}?${query}`);
    setFilters(next); setDraft(next); setRevision(value => value + 1);
  }
  const params = new URLSearchParams({ year: String(year), month: String(month), limit: "25", offset: String(filters.offset) });
  const queryNames: Record<keyof Omit<Filters, "offset">, string> = { owner: "sales_owner_id", preset: "preset", from: "from", to: "to", q: "q", customer: "customer", product: "product", invoice: "invoice", kind: "kind", condition: "condition" };
  for (const key of keys) if (filters[key]) params.set(queryNames[key], filters[key]);
  const path = `/admin/reports/sales/details?${params}`;
  const changed = exportDisabled || keys.some(key => draft[key] !== filters[key]);
  const valid = (!draft.from && !draft.to || !!draft.from && !!draft.to && draft.from <= draft.to) && (draft.owner === "" || /^\d+$/.test(draft.owner));
  function field(key: keyof Omit<Filters, "offset">, value: string) { setDraft(current => ({ ...current, [key]: value })); }
  const selectClass = "h-9 w-full rounded-md border bg-background px-3";
  return <div className="space-y-4 min-w-0">
    <form className="rounded-md border p-4 space-y-4" onSubmit={event => { event.preventDefault(); if (valid) apply({ ...draft, offset: 0 }); }}>
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <label className="text-sm space-y-1">销售人员<select aria-label="销售人员" className={selectClass} value={draft.owner} onChange={event => field("owner", event.target.value)}><option value="">全部授权范围销售</option>{staff.map(option => <option key={option.id} value={option.id}>{option.name}（{option.id}）</option>)}{draft.owner && !staff.some(option => String(option.id) === draft.owner) && <option value={draft.owner}>已选编号 {draft.owner}（当前范围内无可见客户）</option>}</select></label>
        <label className="text-sm space-y-1">时间范围<select aria-label="业绩时间范围" className={selectClass} value={draft.preset} onChange={event => setDraft(current => ({ ...current, preset: event.target.value, from: "", to: "" }))}><option value="">上方所选月份 / 自定日期</option>{Object.entries({ today: "今日", week: "本周至今日", month: "本月", last_month: "上月", three_months: "近三个月", all: "全部历史" }).map(([value, label]) => <option value={value} key={value}>{label}</option>)}</select></label>
        <label className="text-sm space-y-1">开始日期<Input aria-label="业绩开始日期" type="date" value={draft.from} onChange={event => setDraft(current => ({ ...current, from: event.target.value, preset: "" }))} /></label>
        <label className="text-sm space-y-1">结束日期（含当天）<Input aria-label="业绩结束日期" type="date" value={draft.to} onChange={event => setDraft(current => ({ ...current, to: event.target.value, preset: "" }))} /></label>
      </div>
      {staffLoading && <p className="text-xs text-muted-foreground" role="status">正在加载授权范围内的销售选项…</p>}
      {staffError && <div className="flex flex-wrap items-center gap-2"><p className="text-sm text-destructive" role="alert">销售选项加载失败：{staffError}</p><Button variant="outline" type="button" onClick={() => { setStaffLoading(true); setStaffError(""); setStaffAttempt(value => value + 1); }}>重试销售选项</Button></div>}
      <label className="block text-sm space-y-1">关键词<Input aria-label="业绩关键词" placeholder="客户、产品或账单编号" maxLength={120} value={draft.q} onChange={event => field("q", event.target.value)} /></label>
      <details><summary className="cursor-pointer text-sm font-medium">高级筛选</summary><div className="grid gap-3 mt-3 sm:grid-cols-2 xl:grid-cols-3">
        <label className="text-sm space-y-1">客户<Input aria-label="业绩客户" maxLength={120} value={draft.customer} onChange={event => field("customer", event.target.value)} /></label>
        <label className="text-sm space-y-1">产品<Input aria-label="业绩产品" maxLength={120} value={draft.product} onChange={event => field("product", event.target.value)} /></label>
        <label className="text-sm space-y-1">账单编号<Input aria-label="业绩账单编号" maxLength={120} value={draft.invoice} onChange={event => field("invoice", event.target.value)} /></label>
        <label className="text-sm space-y-1">业务类型<select aria-label="业绩业务类型" className={selectClass} value={draft.kind} onChange={event => field("kind", event.target.value)}><option value="">全部类型</option>{["purchase", "renew", "upgrade", "other"].map(kind => <option key={kind} value={kind}>{kindLabel(kind)}</option>)}</select></label>
        <label className="text-sm space-y-1">条件<select aria-label="业绩提成条件" className={selectClass} value={draft.condition} onChange={event => field("condition", event.target.value)}><option value="">全部条件</option>{Object.entries({ configured: "已配置预计规则", unconfigured: "预计规则未配置", confirmed: "历史已确认", reversed: "历史已冲正", unconfirmed: "历史未确认", credit: "信用实际还款" }).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
      </div></details>
      {!valid && <p className="text-sm text-destructive" role="alert">请填写完整且有效的起止日期。</p>}
      <div className="flex flex-wrap gap-2"><Button type="submit" disabled={!valid}>查询业绩</Button><Button type="button" variant="outline" onClick={() => apply({ ...defaults })}>重置筛选</Button></div>
    </form>
    <ReportPanel<SalesPage> key={`${path}:${revision}`} path={path} filename={`sales-${year}-${month}-page-${filters.offset / 25 + 1}.csv`} title="销售业绩与提成" description="按账单分页，展开当前筛选内的业务子项。金额为折后收入，信用按真实到账。CSV 仅导出当前页的展开子项，金额单位为整数分。" exportDisabled={changed}>
      {data => <>
        <p className="text-sm text-muted-foreground mb-4">期间：{data.from} 至 {data.to}；计算时间：{dateTime(data.computed_at)}。当前销售归属用于预计；已确认提成保留历史人员和规则快照。</p>
        <div className="stat-grid mb-4"><Stat icon={Wallet} label="筛选结果到账" value={money(data.statistics.selected.amount_cents)} /><Stat icon={Receipt} label="去重账单数" value={data.statistics.selected.invoice_count} /><Stat icon={BadgePercent} label="已配置项目预计提成" value={money(data.statistics.selected.estimated_commission_cents)} /><Stat icon={BadgePercent} label="所选账单历史有效确认" value={money(data.statistics.selected.confirmed_commission_cents)} /></div>
        {!!data.statistics.selected.unconfigured_invoices && <p role="status" className="text-sm mb-4">{data.statistics.selected.unconfigured_invoices} 张账单含未配置项目，预计金额只包含已配置部分。</p>}
        <ResourceTable items={[{ name: "今日", ...data.statistics.today }, { name: "本周至今日", ...data.statistics.week }, { name: "本月", ...data.statistics.month }, { name: "上月", ...data.statistics.last_month }, { name: "前一等长期间", ...data.statistics.comparison }]} rowKey={item => item.name} columns={[{ header: "对比范围", render: item => item.name }, { header: "到账", render: item => money(item.amount_cents) }, { header: "账单数", render: item => item.invoice_count }, { header: "已配置项目预计", render: item => money(item.estimated_commission_cents) }]} />
        <p className="text-xs text-muted-foreground mt-2">对比应用当前人员及高级筛选；前一期间与查询期间等长。预计阶梯按当前可见客户的新购月到账计算，不包含无权查看客户的营业额。</p>
        <SalesTrend points={data.trend} unit={data.trend_unit} />
        {!data.items.length && <p className="py-10 text-center text-muted-foreground">当前条件下暂无可见业绩账单</p>}
        <div className="space-y-3">{data.items.map(invoice => <details key={invoice.invoice_id} className="border rounded-md p-3">
          <summary className="cursor-pointer text-sm"><span className="font-semibold break-all">{invoice.invoice_no}</span><span className="ml-3">{invoice.customer_name} · {invoice.sales_owner_name}</span><span className="block mt-1">到账 {money(invoice.amount_cents)} · 预计 {estimate(invoice.estimated_commission_cents, invoice.unconfigured_lines > 0)} · 历史有效确认 {invoice.confirmed_commission_cents === null ? "未确认" : money(invoice.confirmed_commission_cents)} · {confirmationLabel(invoice.confirmation_status)}</span></summary>
          <p className="text-xs text-muted-foreground my-3">支付时间 {dateTime(invoice.paid_at)}；本次筛选最后到账 {dateTime(invoice.received_at)}。{invoice.confirmed_sales_owner_id !== null && <>历史确认销售编号 {invoice.confirmed_sales_owner_id}；整单原确认 {money(invoice.confirmed_invoice_original_cents ?? 0)}；筛选子项原确认 {money(invoice.confirmed_original_cents ?? 0)}。</>}</p>
          <ResourceTable items={invoice.items} rowKey={line => line.line_key} columns={[
            { header: "产品 / 项目", render: line => <span className="whitespace-normal">{line.product_name}{!line.product_id && "（未归属产品）"}</span> }, { header: "类型", render: line => kindLabel(line.kind) }, { header: "到账金额", render: line => money(line.amount_cents) }, { header: "预计提成", render: line => estimate(line.estimated_commission_cents) }, { header: "历史有效确认", render: line => line.confirmed_commission_cents === null ? "未确认" : money(line.confirmed_commission_cents) },
            { header: "当前预计规则", render: line => <div className="space-y-1">{line.rules.map((rule, index) => <p key={index} className="text-xs whitespace-normal">{rule.rate_bps === null ? "未配置" : `${rule.group_name} v${rule.version} · ${rule.rate_bps / 100}%${rule.tier_id ? ` · 阶梯 v${rule.tier_version} +${rule.extra_rate_bps / 100}%` : ""}`}</p>)}</div> }, { header: "到账期间", render: line => <span className="text-xs">{dateTime(line.first_received_at)}{line.first_received_at !== line.received_at && <> 至 {dateTime(line.received_at)}</>}{line.credit && " · 信用还款"}</span> },
          ]} />
        </details>)}</div>
        <ResourcePagination total={data.total} limit={data.limit} offset={data.offset} unit="账单" onOffsetChange={offset => apply({ ...filters, offset })} />
        <p className="mt-4 text-xs text-muted-foreground">预计与已确认分别展示，不相加、不代表已发放。历史确认按完整账单子项比例分摊后筛选，不因产品筛选重算。该金额是所选账单的历史确认快照，不是期间新确认额；跨月信用账单应以全年查询去重，不累加各月确认值。退款或取消的有效确认显示零，原确认保留。此页不创建结算或发放。</p>
      </>}
    </ReportPanel>
  </div>;
}
