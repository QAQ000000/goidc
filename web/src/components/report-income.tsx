"use client";
import { useEffect, useId, useState } from "react";
import { ArrowDownLeft, ArrowUpRight, Wallet, Receipt } from "lucide-react";
import { api, ApiError, money } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { useAdminIdentity } from "./workspace";
import { Failure, Loading } from "./common";
import { Stat } from "./resource-page";
import { ResourcePagination, ResourceTable } from "./resource-table";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "./ui/dialog";

type CashMonth = {
  month?: string; received_cents: number; gateway_received_cents: number; credit_received_cents: number;
  anomaly_received_cents: number; unverified_anomaly_cents: number; refund_cents: number;
  manual_expense_cents: number | null; outgoing_cents: number | null; net_cents: number | null; business_consumption_cents: number;
  simulated_received_cents: number; simulated_refund_cents: number;
};
type AnnualCash = {
  year: number; currency: string; expenses_available: boolean; expenses_manageable: boolean; months: CashMonth[]; total: CashMonth;
  previous_year: { year: number; months: CashMonth[]; total: CashMonth }; comparison: CashMonth;
};
type Expense = {
  id: number; amount_cents: number; occurred_on: string; category: string; source: string; evidence_ref: string; note: string;
  recorded_by_name: string; created_at: string; status: "active" | "voided"; version: number; voided_by_name?: string; void_note?: string;
};
type ExpensePage = { items: Expense[]; total: number; limit: number; offset: number; manageable: boolean };
type ExpenseDraft = { amount: string; occurred_on: string; category: string; source: string; evidence_ref: string; note: string };
type Pending = { path: string; body: string; key: string; kind: "create" | "void" };
const showMoney = (value: number | null) => value === null ? "无权限" : money(value);
function pendingExpenseLabel(request: Pending) {
  try {
    const body = JSON.parse(request.body) as Partial<Expense> & { note?: string };
    if (request.kind === "create" && Number.isSafeInteger(body.amount_cents)) return `登记 ${money(body.amount_cents!)} · ${body.occurred_on ?? ""} · ${body.source ?? ""} / ${body.evidence_ref ?? ""}`;
    if (request.kind === "void") return `撤销费用 #${request.path.split("/").at(-2)} · ${body.note ?? ""}`;
  } catch { /* A malformed stored request still requires a server-confirmed result. */ }
  return "原请求内容无法预览，请核对台账后使用原请求确认结果。";
}

type CashSeries = { label: string; values: (number | null)[]; color: string; dashed?: boolean };
function CashTrendChart({ title, series }: { title: string; series: CashSeries[] }) {
  const titleId = useId();
  const visible = series.filter(line => line.values.some(value => value !== null));
  const values = visible.flatMap(line => line.values.filter((value): value is number => value !== null));
  const minimum = Math.min(0, ...values);
  const maximum = Math.max(0, ...values);
  const range = maximum - minimum || 1;
  const x = (index: number) => 24 + index / 11 * 592;
  const y = (value: number) => 148 - (value - minimum) / range * 124;
  return <figure className="min-w-0 rounded-md border p-3 space-y-3">
    <figcaption id={titleId} className="text-sm font-medium">{title}</figcaption>
    <div className="flex flex-wrap gap-x-4 gap-y-2 text-xs">{visible.map(line => <span key={line.label} className="inline-flex items-center gap-2"><svg aria-hidden="true" width="22" height="10"><line x1="0" y1="5" x2="22" y2="5" stroke={line.color} strokeWidth="3" strokeDasharray={line.dashed ? "5 3" : undefined}/></svg>{line.label}</span>)}</div>
    <svg role="img" aria-labelledby={titleId} viewBox="0 0 640 184" className="w-full max-h-64">
      <desc>按上海时区展示一月至十二月，金额单位为人民币。各月精确金额可在下方表格查看。</desc>
      <line x1="24" y1={y(0)} x2="616" y2={y(0)} stroke="currentColor" opacity=".3"/>
      {visible.map(line => {
        // Null values are permission boundaries, so never connect across them.
        const segments: string[][] = [[]];
        line.values.forEach((value, index) => { if (value === null) { if (segments.at(-1)!.length) segments.push([]); } else segments.at(-1)!.push(`${x(index)},${y(value)}`); });
        return <g key={line.label}>{segments.filter(segment => segment.length).map((segment, index) => <polyline key={index} points={segment.join(" ")} fill="none" stroke={line.color} strokeWidth="2.5" strokeDasharray={line.dashed ? "7 5" : undefined}/>)}{line.values.map((value, index) => value === null ? null : <circle key={index} cx={x(index)} cy={y(value)} r="3" fill={line.color}><title>{index + 1}月 · {line.label}：{money(value)}</title></circle>)}</g>;
      })}
      {[0, 2, 5, 8, 11].map(index => <text key={index} x={x(index)} y="176" textAnchor="middle" fill="currentColor" fontSize="18">{index + 1}月</text>)}
    </svg>
    <p className="text-xs text-muted-foreground">纵轴范围 {money(minimum)} 至 {money(maximum)}；精确数值见下方月度明细。</p>
  </figure>;
}

export function AnnualIncomeReport({ year, filtersChanged = false }: { year: number; filtersChanged?: boolean }) {
  const path = `/admin/reports/annual-cash?year=${year}`;
  const resource = useResource<AnnualCash>(path, false);
  const result = resource.data;
  return <section aria-label="年度现金收支" className="data-surface p-4 space-y-5">
    <div><h2 className="font-semibold">{year} 年现金收支</h2><p className="mt-1 text-xs text-muted-foreground">现金收入包含网关到账、信用还款和已核验迟到到账；退款按实际凭证日期统计。余额消费单列为业务消费。</p></div>
    {filtersChanged && <p className="text-sm text-muted-foreground">筛选条件已更改，请查询。</p>}
    {resource.error ? <Failure error={resource.error} retry={resource.refresh}/> : !result ? <Loading/> : <>
      <div className="stat-grid"><Stat icon={ArrowDownLeft} label="实际收款" value={money(result.total.received_cents)}/><Stat icon={ArrowUpRight} label="总支出（退款与人工费用）" value={showMoney(result.total.outgoing_cents)}/><Stat icon={Wallet} label="现金净额" value={showMoney(result.total.net_cents)}/><Stat icon={Receipt} label="业务消费（含余额消费）" value={money(result.total.business_consumption_cents)}/></div>
      {!result.expenses_available && <p role="status" className="text-sm text-muted-foreground">当前客户范围不允许查看全局费用，因此人工费用、总支出和现金净额不可见。</p>}
      {result.total.unverified_anomaly_cents > 0 && <p role="status" className="text-sm">待核验迟到收款 {money(result.total.unverified_anomaly_cents)}，未计入实际收款。</p>}
      {(result.total.simulated_received_cents > 0 || result.total.simulated_refund_cents > 0) && <p role="status" className="text-sm">模拟渠道收款 {money(result.total.simulated_received_cents)}，模拟退款 {money(result.total.simulated_refund_cents)}，均未计入实际现金收支和净额。</p>}
      <div className="grid gap-4 xl:grid-cols-2">
        <CashTrendChart title={`${year} 年月度现金收支趋势`} series={[
          {label:"实际收款",values:result.months.map(row=>row.received_cents),color:"var(--primary)"},
          {label:"总支出",values:result.months.map(row=>row.outgoing_cents),color:"#b45309",dashed:true},
          {label:"现金净额",values:result.months.map(row=>row.net_cents),color:"#0d9488"},
        ]}/>
        <CashTrendChart title={`${year} 与 ${result.previous_year.year} 年同期比较`} series={[
          {label:`${year} 收款`,values:result.months.map(row=>row.received_cents),color:"var(--primary)"},
          {label:`${result.previous_year.year} 收款`,values:result.previous_year.months.map(row=>row.received_cents),color:"var(--primary)",dashed:true},
          {label:`${year} 净额`,values:result.months.map(row=>row.net_cents),color:"#0d9488"},
          {label:`${result.previous_year.year} 净额`,values:result.previous_year.months.map(row=>row.net_cents),color:"#0d9488",dashed:true},
        ]}/>
      </div>
      <ResourceTable items={[...result.months, { ...result.total, month: "全年总计" }]} rowKey={row => row.month!} columns={[
        {header:"月份",render:row=>row.month}, {header:"实际收款",render:row=>money(row.received_cents)},
        {header:"网关到账",render:row=>money(row.gateway_received_cents)}, {header:"信用还款",render:row=>money(row.credit_received_cents)},
        {header:"核验迟到到账",render:row=>money(row.anomaly_received_cents)}, {header:"待核验",render:row=>money(row.unverified_anomaly_cents)},
        {header:"退款",render:row=>money(row.refund_cents)}, {header:"人工费用",render:row=>showMoney(row.manual_expense_cents)},
        {header:"总支出",render:row=>showMoney(row.outgoing_cents)}, {header:"现金净额",render:row=>showMoney(row.net_cents)},
        {header:"业务消费",render:row=>money(row.business_consumption_cents)},
        {header:"模拟收款",render:row=>money(row.simulated_received_cents)}, {header:"模拟退款",render:row=>money(row.simulated_refund_cents)},
      ]}/>
      <details><summary className="cursor-pointer font-medium">与 {result.previous_year.year} 年比较</summary><ResourceTable className="mt-4 overflow-auto" items={result.months.map((row,index)=>({row,previous:result.previous_year.months[index]}))} rowKey={item=>item.row.month!} columns={[
        {header:"月份",render:item=>item.row.month?.slice(-2)}, {header:`${year} 收款`,render:item=>money(item.row.received_cents)},
        {header:`${result.previous_year.year} 收款`,render:item=>money(item.previous.received_cents)}, {header:"收款差额",render:item=>money(item.row.received_cents-item.previous.received_cents)},
        {header:`${year} 净额`,render:item=>showMoney(item.row.net_cents)}, {header:`${result.previous_year.year} 净额`,render:item=>showMoney(item.previous.net_cents)},
      ]}/><p className="mt-3 text-sm">年度收款差额 {money(result.comparison.received_cents)}；年度净额差额 {showMoney(result.comparison.net_cents)}。</p></details>
      {result.expenses_available && <ExpenseLedger key={year} year={year} manageable={result.expenses_manageable} onChange={resource.refresh}/>}
    </>}
  </section>;
}

function ExpenseLedger({ year, manageable, onChange }: { year: number; manageable: boolean; onChange: () => void }) {
  const [offset,setOffset] = useState(0);
  const resource = useResource<ExpensePage>(`/admin/reports/expenses?year=${year}&limit=20&offset=${offset}`,false);
  const actor = useAdminIdentity();
  const storageKey = actor ? `goidc:pending-report-expense:${actor.id}` : null;
  const [readyKey,setReadyKey] = useState<string|null>(null);
  const [pending,setPending] = useState<Pending|null>(null);
  const [draft,setDraft] = useState<ExpenseDraft|null>(null);
  const [voiding,setVoiding] = useState<Expense|null>(null);
  const [voidNote,setVoidNote] = useState("");
  const [busy,setBusy] = useState(false);
  const [error,setError] = useState("");
  const [revoked,setRevoked] = useState(false);
  const allowed = manageable && resource.data?.manageable === true && !resource.error && !revoked;
  useEffect(()=>{
    let restored:Pending|null=null;
    try { const raw=storageKey?sessionStorage.getItem(storageKey):null; if(raw){const v=JSON.parse(raw) as Pending;if(typeof v.key==="string" && typeof v.body==="string" && /^\/admin\/reports\/expenses(?:\/\d+\/void)?$/.test(v.path) && (v.kind==="create"||v.kind==="void"))restored=v;} } catch { /* A pending request remains available in this tab if storage is disabled. */ }
    let active=true;queueMicrotask(()=>{if(active){setPending(restored);setReadyKey(storageKey);}});return()=>{active=false;};
  },[storageKey]);
  useEffect(()=>{if(!pending)return;const warn=(event:BeforeUnloadEvent)=>{event.preventDefault();event.returnValue="";};window.addEventListener("beforeunload",warn);return()=>window.removeEventListener("beforeunload",warn);},[pending]);
  function remember(value:Pending|null){setPending(value);if(storageKey){try{if(value)sessionStorage.setItem(storageKey,JSON.stringify(value));else sessionStorage.removeItem(storageKey);}catch{/* Exact request is retained in memory. */}}}
  async function submit(request:Pending){
    if(busy||!allowed||!storageKey||storageKey!==readyKey)return;
    setBusy(true);setError("");remember(request);
    try{await api(request.path,{method:"POST",headers:{"Idempotency-Key":request.key},body:request.body});remember(null);setDraft(null);setVoiding(null);resource.refresh();onChange();}
    catch(cause){setError(cause instanceof Error?cause.message:"保存失败，请重试");if(cause instanceof ApiError && cause.status<500){if(cause.status===401||cause.status===403){setRevoked(true);}else{remember(null);}if(cause.status===409){resource.refresh();onChange();setVoiding(null);}}}
    finally{setBusy(false);}
  }
  function create(){
    if(!draft)return;
    const raw=draft.amount.trim();const [whole,fraction=""]=raw.split(".");const amount=Number(whole)*100+Number(fraction.padEnd(2,"0"));
    if(!/^\d+(\.\d{1,2})?$/.test(raw)||!Number.isSafeInteger(amount)||amount<1||amount>100000000000){setError("金额须大于零，最多两位小数，且不超过十亿元。");return;}
    void submit({path:"/admin/reports/expenses",body:JSON.stringify({...draft,amount:undefined,amount_cents:amount}),key:`expense-${crypto.randomUUID()}`,kind:"create"});
  }
  const close=()=>{if(!busy&&!pending){setDraft(null);setVoiding(null);setError("");}};
  return <section aria-label="人工费用台账" className="space-y-4 border-t pt-5">
    <div className="flex flex-wrap items-center justify-between gap-3"><div><h3 className="font-semibold">人工费用台账</h3><p className="mt-1 text-xs text-muted-foreground">独立登记实际经营费用。同一来源与凭据仅能登记一次；撤销保留原始事实并重述原发生月份。</p></div><div className="flex gap-2"><Button variant="outline" disabled={busy} onClick={()=>{setRevoked(false);resource.refresh();onChange();}}>刷新台账</Button><Button disabled={!allowed||busy||!!pending||!readyKey} onClick={()=>{setError("");setDraft({amount:"",occurred_on:`${year}-01-01`,category:"",source:"",evidence_ref:"",note:""});}}>登记费用</Button></div></div>
    {pending && !busy && <div role="status" className="space-y-2 text-sm"><p>上次费用请求的结果尚未确认。请使用原凭据重试确认，期间不能登记另一笔费用。</p><p className="break-all">原请求：{pendingExpenseLabel(pending)}</p><Button variant="outline" disabled={!allowed||busy} onClick={()=>void submit(pending)}>重试确认结果</Button></div>}
    {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    {revoked && <p role="alert" className="text-sm">会话或费用管理权限已失效，请恢复登录或权限后刷新台账。</p>}
    {resource.error?<Failure error={resource.error} retry={resource.refresh}/>:!resource.data?<Loading/>:<><ResourceTable items={resource.data.items} rowKey={row=>row.id} empty="本年度暂无人工费用" columns={[
      {header:"发生日期",render:row=>row.occurred_on}, {header:"金额",render:row=>money(row.amount_cents)}, {header:"类别",render:row=>row.category},
      {header:"来源",render:row=>row.source}, {header:"凭据",render:row=><span className="break-all">{row.evidence_ref}</span>}, {header:"说明",render:row=>row.note||"—"},
      {header:"登记人",render:row=>row.recorded_by_name}, {header:"状态",render:row=>row.status==="active"?"有效":`已撤销 · ${row.voided_by_name||""} · ${row.void_note||""}`},
      {header:"操作",render:row=>row.status==="active"&&<Button variant="outline" size="sm" disabled={!allowed||busy||!!pending} onClick={()=>{setError("");setVoiding(row);setVoidNote("");}}>撤销</Button>},
    ]}/><ResourcePagination total={resource.data.total} offset={offset} limit={20} onOffsetChange={setOffset} unit="费用记录"/></>}
    <Dialog open={!!draft||!!voiding} onOpenChange={open=>{if(!open)close();}}><DialogContent><DialogHeader><DialogTitle>{draft?"登记经营费用":"撤销费用记录"}</DialogTitle><DialogDescription>{draft?"金额、实际发生日期、来源与凭据均须可核验。":"撤销只修正错误登记，原记录与操作审计将保留。"}</DialogDescription></DialogHeader>
      <form className="space-y-4" onSubmit={event=>{event.preventDefault();if(pending){void submit(pending);}else if(draft){create();}else if(voiding&&voidNote.trim()){void submit({path:`/admin/reports/expenses/${voiding.id}/void`,body:JSON.stringify({expected_version:voiding.version,note:voidNote}),key:`expense-void-${crypto.randomUUID()}`,kind:"void"});}}}>
        <fieldset disabled={busy||!!pending||!allowed} className="space-y-3">{draft?<>
          <label className="grid gap-1 text-sm">费用金额（元）<Input required inputMode="decimal" value={draft.amount} onChange={e=>setDraft({...draft,amount:e.target.value})}/></label>
          <label className="grid gap-1 text-sm">发生日期<Input required type="date" min="2000-01-01" value={draft.occurred_on} onChange={e=>setDraft({...draft,occurred_on:e.target.value})}/></label>
          <label className="grid gap-1 text-sm">类别<Input required maxLength={80} value={draft.category} onChange={e=>setDraft({...draft,category:e.target.value})}/></label>
          <label className="grid gap-1 text-sm">来源<Input required maxLength={120} value={draft.source} onChange={e=>setDraft({...draft,source:e.target.value})}/></label>
          <label className="grid gap-1 text-sm">凭据标识<Input required maxLength={500} value={draft.evidence_ref} onChange={e=>setDraft({...draft,evidence_ref:e.target.value})}/></label>
          <label className="grid gap-1 text-sm">说明<Textarea maxLength={1000} value={draft.note} onChange={e=>setDraft({...draft,note:e.target.value})}/></label>
        </>:<><p className="text-sm">费用 #{voiding?.id} · {voiding&&money(voiding.amount_cents)} · {voiding?.occurred_on}</p><label className="grid gap-1 text-sm">撤销原因<Textarea required maxLength={1000} value={voidNote} onChange={e=>setVoidNote(e.target.value)}/></label></>}</fieldset>
        {error&&<p role="alert" className="text-sm text-destructive">{error}</p>}{pending&&<p className="text-sm">请求结果未确认，原请求与幂等键已保留。</p>}
        <DialogFooter><Button type="button" variant="outline" disabled={busy||!!pending} onClick={close}>取消</Button><Button type="submit" disabled={busy||!allowed}>{busy?"保存中…":pending?"重试确认结果":draft?"确认登记":"确认撤销"}</Button></DialogFooter>
      </form>
    </DialogContent></Dialog>
  </section>;
}
