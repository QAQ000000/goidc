"use client";

import { useState } from "react";
import { Archive, CheckCircle2, ChevronLeft, ChevronRight, ClipboardCheck, Eye, RefreshCw } from "lucide-react";
import { api, date, money, type User } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace } from "@/components/workspace";
import { Failure, Loading, Status } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Label } from "@/components/ui/label";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

type Entity = "invoices" | "services";
type ReviewRow = { entity: Entity; id: number; source: string; legacy_id: number; name: string; customer_name: string; status: string; amount_cents: number; import_review_required: boolean; import_review_version: number; review_state: "pending" | "archive" | "release" };
type Run = { id: number; source: string; status: string; started_at: string; completed_at: string | null; issue_count: number; counts: Record<string, number> };
type Detail = {
  entity: Entity; target_id: number;
  current: { source: string; legacy_id: number; version: number; review_required: boolean; state: string; amount_cents: number; billing_cycle: string; upstream_id: string; user_id: number; order_id: number };
  source_record: Record<string, string>; source_checksum: string; release_blockers: string[];
  decisions: { id: number; version: number; decision: string; evidence: string; actor_id: number; created_at: string }[];
  issues: { entity: string; legacy_id: number; code: string; detail: Record<string, unknown> }[];
};

const reviewNames: Record<string, string> = { pending: "待复核", archive: "已归档", release: "已恢复业务" };
const issueNames: Record<string, string> = {
  historical_invoice_requires_review: "历史账单待核验", standalone_invoice: "账单无订单关联", invoice_item_total_mismatch: "账单项目合计不一致",
  order_invoice_amount_mismatch: "订单与账单金额不一致", missing_invoice: "原账单记录缺失", missing_host_record: "原主机记录缺失",
  active_host_without_paid_invoice: "旧服务已启用但账单未付", upstream_resource_requires_verification: "上游资源待核验", order_owner_mismatch: "订单与服务客户不一致",
};

function exactCents(value: string): number | null {
  if (!/^\d+(?:\.\d{1,2})?$/.test(value)) return null;
  const [whole, fraction = ""] = value.split(".");
  const cents = Number(whole + fraction.padEnd(2, "0"));
  return Number.isSafeInteger(cents) ? cents : null;
}

export function LegacyReviewsView() {
  const [entity, setEntity] = useState("all");
  const [state, setState] = useState("pending");
  const [source, setSource] = useState("");
  const [offset, setOffset] = useState(0);
  const [selected, setSelected] = useState<ReviewRow | null>(null);
  const me = useResource<User>("/me");
  const query = new URLSearchParams({ limit: "25", offset: String(offset), source });
  if (entity !== "all") query.set("entity", entity);
  if (state !== "all") query.set("state", state);
  const reviews = useResource<ReviewRow[]>("/admin/migrations/reviews?" + query.toString());
  const runs = useResource<Run[]>("/admin/migrations/runs?limit=10");
  const refresh = () => { reviews.refresh(); runs.refresh(); };
  return <Workspace title="迁移复核" admin>
    <div className="flex items-center justify-between gap-4 mb-6"><h1 className="page-title">迁移复核</h1><Button variant="outline" size="icon" title="刷新复核记录" aria-label="刷新复核记录" onClick={refresh}><RefreshCw size={17} /></Button></div>
    <section className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <Select value={entity} onValueChange={(value) => { setEntity(value); setOffset(0); }}><SelectTrigger className="w-36" aria-label="复核类型"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="all">全部类型</SelectItem><SelectItem value="invoices">历史账单</SelectItem><SelectItem value="services">历史服务</SelectItem></SelectContent></Select>
        <Select value={state} onValueChange={(value) => { setState(value); setOffset(0); }}><SelectTrigger className="w-36" aria-label="复核状态"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="all">全部状态</SelectItem><SelectItem value="pending">待复核</SelectItem><SelectItem value="archive">已归档</SelectItem><SelectItem value="release">已恢复业务</SelectItem></SelectContent></Select>
        <Input className="w-full sm:w-64" aria-label="来源筛选" placeholder="来源筛选" value={source} onChange={(event) => { setSource(event.target.value); setOffset(0); }} />
      </div>
      {reviews.error ? <Failure error={reviews.error} retry={reviews.refresh} /> : !reviews.data ? <Loading /> : <>
        <div className="overflow-auto border-y bg-white"><Table><TableHeader><TableRow><TableHead>记录</TableHead><TableHead>客户</TableHead><TableHead>来源 / 旧 ID</TableHead><TableHead>业务状态</TableHead><TableHead>操作限制</TableHead><TableHead>复核状态</TableHead><TableHead /></TableRow></TableHeader><TableBody>
          {reviews.data.length === 0 ? <TableRow><TableCell colSpan={7} className="h-32 text-center text-muted-foreground">暂无符合条件的迁移记录</TableCell></TableRow> : reviews.data.map((row) => <TableRow key={row.entity + row.id}><TableCell><span className="block font-medium max-w-52 truncate">{row.name}</span><span className="text-xs text-muted-foreground">{row.entity === "invoices" ? "账单" : "服务"} #{row.id}</span></TableCell><TableCell>{row.customer_name}</TableCell><TableCell><span className="block max-w-52 truncate" title={row.source}>{row.source}</span><span className="text-xs text-muted-foreground">{row.legacy_id}</span></TableCell><TableCell><Status value={row.status} /></TableCell><TableCell>{row.import_review_required ? "保留" : "已解除"}</TableCell><TableCell>{reviewNames[row.review_state]}</TableCell><TableCell><Button variant="ghost" size="icon-sm" title="查看复核详情" aria-label={`查看${row.name}复核详情`} onClick={() => setSelected(row)}><Eye size={16} /></Button></TableCell></TableRow>)}
        </TableBody></Table></div>
        <div className="flex items-center justify-end gap-3"><span className="text-sm text-muted-foreground">第 {Math.floor(offset / 25) + 1} 页</span><Button variant="outline" size="icon-sm" disabled={offset === 0} title="上一页" aria-label="上一页" onClick={() => setOffset((value) => value - 25)}><ChevronLeft size={16} /></Button><Button variant="outline" size="icon-sm" disabled={reviews.data.length < 25} title="下一页" aria-label="下一页" onClick={() => setOffset((value) => value + 25)}><ChevronRight size={16} /></Button></div>
      </>}
    </section>
    <section className="mt-10 border-t pt-6"><h2 className="font-semibold mb-4">最近导入批次</h2>{runs.error ? <Failure error={runs.error} retry={runs.refresh} /> : !runs.data ? <Loading /> : <div className="overflow-auto"><Table><TableHeader><TableRow><TableHead>批次</TableHead><TableHead>来源</TableHead><TableHead>状态</TableHead><TableHead>异常数</TableHead><TableHead>开始时间</TableHead><TableHead>结束时间</TableHead></TableRow></TableHeader><TableBody>{runs.data.length === 0 ? <TableRow><TableCell colSpan={6} className="h-24 text-center text-muted-foreground">暂无导入批次</TableCell></TableRow> : runs.data.map((run) => <TableRow key={run.id}><TableCell>#{run.id}</TableCell><TableCell>{run.source}</TableCell><TableCell><Status value={run.status} /></TableCell><TableCell>{run.issue_count}</TableCell><TableCell className="whitespace-nowrap">{date(run.started_at)}</TableCell><TableCell className="whitespace-nowrap">{run.completed_at ? date(run.completed_at) : "-"}</TableCell></TableRow>)}</TableBody></Table></div>}</section>
    {selected && <ReviewDialog key={selected.entity + selected.id} row={selected} canReview={me.data?.role === "super_admin"} close={() => setSelected(null)} onSaved={refresh} />}
  </Workspace>;
}

function ReviewDialog({ row, canReview, close, onSaved }: { row: ReviewRow; canReview: boolean; close: () => void; onSaved: () => void }) {
  const detail = useResource<Detail>(`/admin/migrations/reviews/${row.entity}/${row.id}`);
  return <Dialog open onOpenChange={(open) => { if (!open) close(); }}><DialogContent aria-describedby={undefined} className="sm:max-w-3xl max-h-[90dvh] overflow-y-auto grid-cols-[minmax(0,1fr)]"><DialogHeader className="min-w-0"><DialogTitle className="pr-5">{row.entity === "invoices" ? "账单" : "服务"} #{row.id} · 迁移复核</DialogTitle><p className="text-sm text-muted-foreground break-all text-left">{row.name}</p></DialogHeader>{detail.error ? <Failure error={detail.error} retry={detail.refresh} /> : !detail.data ? <Loading /> : <ReviewDetail key={detail.data.current.version} detail={detail.data} canReview={canReview} onSaved={() => { detail.refresh(); onSaved(); }} />}</DialogContent></Dialog>;
}

function ReviewDetail({ detail, canReview, onSaved }: { detail: Detail; canReview: boolean; onSaved: () => void }) {
  const [decision, setDecision] = useState("archive");
  const [evidence, setEvidence] = useState("");
  const [resource, setResource] = useState("");
  const [amount, setAmount] = useState(detail.source_record.amount ?? "0.00");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [key] = useState(() => crypto.randomUUID());
  const { current, source_record: source } = detail;
  const isService = detail.entity === "services";
  const nextState = ({ pending: "pending_payment", active: "active", suspended: "suspended" } as Record<string, string>)[source.domainstatus?.toLowerCase()] ?? "";
  const releaseAllowed = current.review_required && detail.release_blockers.length === 0;
  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault(); setBusy(true); setError("");
    const input: Record<string, unknown> = { version: current.version, decision, evidence };
    if (decision === "release" && isService) { input.state = nextState; input.upstream_id = resource; input.recurring_price_cents = exactCents(amount); }
    try { await api(`/admin/migrations/reviews/${detail.entity}/${detail.target_id}`, { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(input) }); onSaved(); }
    catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  const fields = isService ? [["旧状态", source.domainstatus], ["旧账期", source.billingcycle], ["旧周期金额", source.amount], ["旧首次支付金额", source.firstpaymentamount], ["旧资源标识", source.dcimid || "-"], ["旧到期时间戳", source.nextduedate || "-"]] : [["旧状态", source.status || "账单记录缺失"], ["旧总额", source.total || "-"], ["旧小计", source.subtotal || "-"], ["旧抵扣", source.credit || "-"], ["旧税费", source.tax || "-"], ["旧附加税", source.tax2 || "-"]];
  return <div className="space-y-6 min-w-0">
    <dl className="grid grid-cols-2 sm:grid-cols-3 gap-x-5 gap-y-3 text-sm"><Info name="来源" value={current.source} /><Info name="旧 ID" value={String(current.legacy_id)} /><Info name="复核版本" value={String(current.version)} /><Info name="当前状态" value={<Status value={current.state} />} /><Info name="客户 / 订单" value={`#${current.user_id} / ${current.order_id ? "#" + current.order_id : "-"}`} /><Info name={isService ? "当前周期金额" : "当前应收"} value={money(current.amount_cents)} /></dl>
    <section className="border-t pt-4"><h3 className="font-medium mb-3">旧数据记录</h3><dl className="grid grid-cols-2 sm:grid-cols-3 gap-3 text-sm">{fields.map(([name, value]) => <Info key={name} name={name} value={value} />)}</dl><p className="text-xs text-muted-foreground mt-4 break-all" title={detail.source_checksum}>归档校验：{detail.source_checksum}</p></section>
    {detail.issues.length > 0 && <section className="border-t pt-4"><h3 className="font-medium mb-3">异常记录</h3><ul className="space-y-2 text-sm">{detail.issues.map((issue, index) => <li key={index} className="break-words"><span className="text-amber-800">{issueNames[issue.code] ?? issue.code}</span><span className="text-muted-foreground ml-2">旧 ID {issue.legacy_id}</span>{Object.keys(issue.detail).length > 0 && <span className="block text-xs text-muted-foreground mt-1 break-all">{JSON.stringify(issue.detail)}</span>}</li>)}</ul></section>}
    {current.review_required && detail.release_blockers.length > 0 && <section className="border-t pt-4"><h3 className="font-medium mb-2">恢复业务未满足项</h3><ul className="text-sm text-destructive space-y-1">{detail.release_blockers.map((blocker) => <li key={blocker}>{blocker}</li>)}</ul></section>}
    {canReview && current.review_required && <form onSubmit={(event) => void submit(event)} className="border-t pt-4 space-y-4"><div className="flex flex-wrap gap-2" role="radiogroup" aria-label="复核决定"><Button type="button" role="radio" aria-checked={decision === "archive"} variant={decision === "archive" ? "default" : "outline"} onClick={() => setDecision("archive")} disabled={busy}><Archive size={16} />确认归档</Button><Button type="button" role="radio" aria-checked={decision === "release"} variant={decision === "release" ? "default" : "outline"} onClick={() => setDecision("release")} disabled={!releaseAllowed || busy}><CheckCircle2 size={16} />恢复业务</Button></div><p className="text-sm text-muted-foreground">操作限制：{decision === "archive" ? "保留" : "解除"}</p>
      {decision === "release" && isService && <div className="grid sm:grid-cols-2 gap-4"><div><Label>核验状态</Label><div className="mt-2"><Status value={nextState} /></div></div><div><Label htmlFor="review-amount">周期金额（元）</Label><Input id="review-amount" inputMode="decimal" value={amount} onChange={(event) => setAmount(event.target.value)} className="mt-2" required /></div>{nextState !== "pending_payment" && <div className="sm:col-span-2"><Label htmlFor="review-resource">实际资源标识</Label><Input id="review-resource" value={resource} onChange={(event) => setResource(event.target.value)} maxLength={256} className="mt-2" required /></div>}</div>}
      <div><Label htmlFor="review-evidence">核验凭据与处理依据</Label><Textarea id="review-evidence" value={evidence} onChange={(event) => setEvidence(event.target.value)} maxLength={2000} minLength={3} rows={4} className="mt-2" required /></div>{error && <p role="alert" className="text-sm text-destructive">{error}</p>}<Button disabled={busy || evidence.trim().length < 3 || (decision === "release" && isService && exactCents(amount) === null)}><ClipboardCheck size={16} />{busy ? "提交中" : "提交复核"}</Button>
    </form>}
    {detail.decisions.length > 0 && <section className="border-t pt-4"><h3 className="font-medium mb-3">复核记录</h3><div className="space-y-4">{detail.decisions.map((item) => <div key={item.id} className="border-l-2 pl-3"><div className="flex flex-wrap gap-x-3 gap-y-1 text-sm"><span className="font-medium">{reviewNames[item.decision]}</span><span className="text-muted-foreground">版本 {item.version} · 管理员 #{item.actor_id}</span><span className="text-xs text-muted-foreground">{date(item.created_at)}</span></div><p className="text-sm mt-2 whitespace-pre-wrap break-words">{item.evidence}</p></div>)}</div></section>}
  </div>;
}

function Info({ name, value }: { name: string; value: React.ReactNode }) { return <div className="min-w-0"><dt className="text-muted-foreground text-xs mb-1">{name}</dt><dd className="break-words">{value || "-"}</dd></div>; }
