"use client";

import { useState } from "react";
import { RefreshCw, UserRoundCog } from "lucide-react";
import { date } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { useAdminIdentity, useAdminPermission } from "./workspace";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "./ui/dialog";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";
import { useCustomerReferrerMutation, validCustomerReferrer, type CustomerReferrer, type ReferrerInput } from "./admin-customer-referrer-mutation";

type Candidate = { id: number; username: string; display_name: string; email: string };
type ReferrerHistory = {
  id: number; customer_id: number; actor_id: number; reason: string; version_before: number; version_after: number; created_at: string;
  old_referrer_customer_id: number | null; old_referrer_name: string | null; old_referrer_visible: boolean; old_attributed: boolean;
  new_referrer_customer_id: number | null; new_referrer_name: string | null; new_referrer_visible: boolean; new_attributed: boolean;
};
type Page<T> = { items: T[]; total: number; limit: number; offset: number };
type Choice = { id: number; name: string } | null | undefined;
const notice = "仅影响变更后新建的账单；既有未付或已付账单、佣金、提现和退款归属均不改变。";
const panelClass = "min-w-0 space-y-4 rounded border border-border bg-card p-4 sm:p-5";

export function CustomerReferrerPanel({ customerId, onChanged }: { customerId: number; onChanged: () => void }) {
  const actor = useAdminIdentity();
  const manageCustomers = useAdminPermission("customers.manage");
  const manageAffiliates = useAdminPermission("affiliates.manage");
  if (!actor || !manageCustomers || !manageAffiliates) return <section className={panelClass}><h2 className="text-base font-semibold">推荐人关系与调整历史</h2><p role="status" className="text-sm text-muted-foreground">无权限查看或修改推荐人，需要客户管理及推广管理权限。</p></section>;
  const authority = JSON.stringify([actor.id, actor.role, [...actor.permissions].sort(), actor.customer_scope, actor.customer_ownerless, actor.sales_enabled]);
  return <CustomerReferrerWorkspace key={`${authority}:${customerId}`} actorID={actor.id} customerId={customerId} onChanged={onChanged} />;
}

function CustomerReferrerWorkspace({ actorID, customerId, onChanged }: { actorID: number; customerId: number; onChanged: () => void }) {
  const root = `/admin/customers/${customerId}/referrer`;
  const relation = useResource<CustomerReferrer>(root, false);
  const mutation = useCustomerReferrerMutation(actorID, customerId, true);
  const [historyPage, setHistoryPage] = useState(0);
  const history = useResource<Page<ReferrerHistory>>(`${root}/history?limit=10&offset=${historyPage * 10}`, false);
  const [editing, setEditing] = useState(false);
  const [baseline, setBaseline] = useState<number | null>(null);
  const [choice, setChoice] = useState<Choice>(undefined);
  const [reason, setReason] = useState("");
  const [confirmedScope, setConfirmedScope] = useState(false);
  const [preview, setPreview] = useState<ReferrerInput | null>(null);
  const [error, setError] = useState("");
  const [success, setSuccess] = useState("");
  const current = validCustomerReferrer(relation.data, customerId) ? relation.data : null;
  const currentError = relation.error?.message || (relation.data && !current ? "当前推荐关系响应无效，请重新读取。" : "");

  function beginEdit() {
    if (!current || mutation.frozen) return;
    setBaseline(current.referral_version); setEditing(true); setChoice(undefined); setReason(""); setConfirmedScope(false); setSuccess(""); setError("");
  }
  function prepare(event: React.FormEvent) {
    event.preventDefault();
    if (mutation.frozen || !baseline) return;
    if (choice === undefined) { setError("请选择新的推荐人，或明确选择清除推荐人。"); return; }
    const normalized = reason.trim();
    if (!normalized || Array.from(normalized).length > 500 || normalized.includes("\0")) { setError("请填写1至500字的调整原因。"); return; }
    if (!confirmedScope) { setError("请先确认仅对变更后新建账单生效。"); return; }
    setError(""); setPreview({ referrer_customer_id: choice?.id ?? null, expected_version: baseline, reason: normalized });
  }
  async function submit(payload?: ReferrerInput) {
    const result = await mutation.run(payload);
    setPreview(null);
    if (!result) return;
    setEditing(false); setChoice(undefined); setReason(""); setBaseline(null); setConfirmedScope(false);
    setSuccess(result.replayed ? "已核验原请求结果；当前关系以重新读取结果为准。" : result.changed ? "推荐人关系已调整。" : "已确认，本次目标与原关系一致，关系版本未改变。");
    relation.refresh(); history.refresh(); onChanged();
  }
  async function reopen() {
    const pending = mutation.pending;
    const latest = await mutation.reopenReviewed();
    if (!latest || !pending) return;
    setBaseline(latest.referral_version); setChoice(pending.referrer_customer_id === null ? null : { id: pending.referrer_customer_id, name: `客户 #${pending.referrer_customer_id}（请重新核对候选资格）` });
    setReason(pending.reason); setConfirmedScope(false); setEditing(true); setError(""); setSuccess(""); relation.refresh();
  }
  return <div className="min-w-0 space-y-5">
    <section className={panelClass}>
      <div className="flex flex-wrap items-center justify-between gap-3"><h2 className="flex items-center gap-2 text-base font-semibold"><UserRoundCog size={18} />推荐人关系</h2><Button type="button" variant="outline" size="sm" disabled={relation.isLoading} onClick={relation.refresh}><RefreshCw size={14} />刷新当前关系</Button></div>
      {currentError ? <ReadError error={currentError} retry={relation.refresh} /> : !current ? <p role="status" className="text-sm text-muted-foreground">正在读取当前推荐人…</p> : <div className="flex flex-wrap items-center justify-between gap-3"><div><p className="break-words text-sm font-medium">{referrerLabel(current.attributed, current.referrer_visible, current.referrer_customer_id, current.referrer_name)}</p><p className="mt-1 text-xs text-muted-foreground">推荐关系版本 {current.referral_version}</p></div>{!editing && !mutation.pending && <Button type="button" variant="outline" disabled={mutation.frozen || relation.isLoading} onClick={beginEdit}>调整推荐人</Button>}</div>}
      <p className="text-sm leading-6 text-muted-foreground">{notice}</p>
      {!mutation.ready && !mutation.error && <p role="status" className="text-sm text-muted-foreground">正在核对是否存在待确认请求…</p>}
      {success && <p role="status" className="text-sm text-emerald-700">{success}</p>}
      {mutation.error && <p role="alert" className="break-words text-sm text-destructive">{mutation.error}</p>}
      {mutation.pending && <div className="space-y-3 rounded border border-border bg-muted/30 p-4 text-sm">
        <p className="font-medium">{mutation.rejected ? "原请求已被明确拒绝，重新编辑前须核对当前关系。" : "原请求结果待核验，已冻结目标、原因、版本与请求键。"}</p>
        <dl className="grid min-w-0 gap-2 sm:grid-cols-[90px_minmax(0,1fr)]"><dt className="text-muted-foreground">原目标</dt><dd>{mutation.pending.referrer_customer_id === null ? "明确清除推荐人" : `客户 #${mutation.pending.referrer_customer_id}`}</dd><dt className="text-muted-foreground">提交版本</dt><dd>{mutation.pending.expected_version}</dd><dt className="text-muted-foreground">原原因</dt><dd className="whitespace-pre-wrap break-words">{mutation.pending.reason}</dd></dl>
        {mutation.rejected ? <><Button type="button" variant="outline" disabled={mutation.busy} onClick={() => void mutation.reviewCurrent()}>读取当前关系以核对</Button>{mutation.reviewed && <div className="space-y-3 border-t border-border pt-3"><p>当前：{referrerLabel(mutation.reviewed.attributed, mutation.reviewed.referrer_visible, mutation.reviewed.referrer_customer_id, mutation.reviewed.referrer_name)} · 版本 {mutation.reviewed.referral_version}</p><Button type="button" variant="outline" disabled={mutation.busy} onClick={reopen}>已核对，保留输入并使用当前版本重新编辑</Button></div>}</> : <Button type="button" variant="outline" disabled={mutation.busy || !mutation.ready} onClick={() => void submit()}>{mutation.busy ? "正在核验…" : "使用原推荐人请求重试"}</Button>}
      </div>}
      {editing && !mutation.pending && <form onSubmit={prepare} className="space-y-4 border-t border-border pt-4">
        <fieldset disabled={mutation.frozen} className="min-w-0 space-y-4"><legend className="mb-3 text-sm font-medium">调整推荐人 · 提交版本 {baseline}</legend>
          <ReferrerCandidates root={root} radioName={`referrer-choice-${customerId}`} choice={choice} onChoose={setChoice} />
          <label className="flex min-h-11 cursor-pointer items-center gap-2 text-sm"><input type="radio" name={`referrer-choice-${customerId}`} checked={choice === null} onChange={() => setChoice(null)} />明确清除当前推荐人</label>
          <label className="block space-y-2 text-sm"><span>调整原因 <span className="text-destructive">*</span></span><Textarea aria-label="推荐人调整原因" value={reason} onChange={event => setReason(event.target.value)} rows={3} maxLength={1000} placeholder="说明本次人工核对与调整原因" className="rounded border-border bg-card" /></label>
          <label className="flex cursor-pointer items-start gap-2 text-sm leading-6"><Checkbox className="mt-1" checked={confirmedScope} onCheckedChange={value => setConfirmedScope(value === true)} /><span>我已核对：{notice}</span></label>
        </fieldset>
        {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
        <div className="flex flex-wrap justify-end gap-2"><Button type="button" variant="outline" disabled={mutation.frozen} onClick={() => { setEditing(false); setChoice(undefined); setReason(""); setError(""); }}>取消调整</Button><Button type="submit" disabled={mutation.frozen || preview !== null}>核对并确认调整</Button></div>
      </form>}
    </section>
    <section className={panelClass}><div className="flex flex-wrap items-center justify-between gap-3"><h2 className="text-base font-semibold">推荐人调整历史</h2><Button type="button" variant="outline" size="sm" disabled={history.isLoading} onClick={history.refresh}><RefreshCw size={14} />刷新历史</Button></div>
      {history.error ? <ReadError error={history.error.message} retry={history.refresh} /> : !history.data ? <p role="status" className="text-sm text-muted-foreground">正在读取调整历史…</p> : <>
        {!history.data.items.length ? <p className="py-6 text-sm text-muted-foreground">暂无推荐人调整记录。</p> : <div className="min-w-0 overflow-x-auto"><Table className="min-w-[680px]"><TableHeader><TableRow><TableHead>时间 / 版本</TableHead><TableHead>原推荐人</TableHead><TableHead>新推荐人</TableHead><TableHead>调整原因</TableHead><TableHead>操作者</TableHead></TableRow></TableHeader><TableBody>{history.data.items.map(item => <TableRow key={item.id}><TableCell className="text-xs"><span className="whitespace-nowrap">{date(item.created_at)}</span><p className="mt-1 tabular-nums text-muted-foreground">{item.version_before} → {item.version_after}</p></TableCell><TableCell className="max-w-40 whitespace-normal break-words">{referrerLabel(item.old_attributed, item.old_referrer_visible, item.old_referrer_customer_id, item.old_referrer_name)}</TableCell><TableCell className="max-w-40 whitespace-normal break-words">{referrerLabel(item.new_attributed, item.new_referrer_visible, item.new_referrer_customer_id, item.new_referrer_name)}</TableCell><TableCell className="max-w-64 whitespace-pre-wrap break-words">{item.reason}</TableCell><TableCell className="whitespace-nowrap">管理员 #{item.actor_id}</TableCell></TableRow>)}</TableBody></Table></div>}
        <ReferrerPager page={historyPage} limit={10} total={history.data.total} busy={history.isLoading} onChange={setHistoryPage} label="推荐人调整历史" />
      </>}
    </section>
    <Dialog open={preview !== null} onOpenChange={open => { if (!open && !mutation.busy) setPreview(null); }}><DialogContent className="max-w-lg rounded-lg"><DialogHeader><DialogTitle>确认调整推荐人</DialogTitle><DialogDescription>{notice}</DialogDescription></DialogHeader><dl className="grid min-w-0 grid-cols-[80px_minmax(0,1fr)] gap-3 text-sm"><dt className="text-muted-foreground">客户</dt><dd>#{customerId}</dd><dt className="text-muted-foreground">新关系</dt><dd className="break-words">{preview?.referrer_customer_id === null ? "清除推荐人" : `${choice?.name || "推荐客户"} #${preview?.referrer_customer_id}`}</dd><dt className="text-muted-foreground">关系版本</dt><dd>{preview?.expected_version}</dd><dt className="text-muted-foreground">原因</dt><dd className="whitespace-pre-wrap break-words">{preview?.reason}</dd></dl><div className="flex justify-end gap-2 border-t border-border pt-4"><Button type="button" variant="outline" disabled={mutation.busy} onClick={() => setPreview(null)}>返回修改</Button><Button type="button" disabled={mutation.busy || !preview} onClick={() => { if (preview) void submit(preview); }}>{mutation.busy ? "正在提交…" : "确认调整推荐人"}</Button></div></DialogContent></Dialog>
  </div>;
}

function ReferrerCandidates({ root, radioName, choice, onChoose }: { root: string; radioName: string; choice: Choice; onChoose: (value: Choice) => void }) {
  const [draft, setDraft] = useState("");
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(0);
  const [error, setError] = useState("");
  const resource = useResource<Page<Candidate>>(`${root}/options?${new URLSearchParams({ q: query, limit: "10", offset: String(page * 10) })}`, false);
  function search() {
    const value = draft.trim();
    if (Array.from(value).length > 120 || value.includes("\0")) { setError("搜索条件不能超过120字。"); return; }
    setError(""); setQuery(value); setPage(0); if (value === query && page === 0) resource.refresh();
  }
  return <div className="min-w-0 space-y-3"><p className="text-sm text-muted-foreground">候选仅包含当前客户范围内已启用、推广审核通过且计划启用的客户；不能选择本人或循环关系。</p>
    <div className="flex flex-wrap items-end gap-2"><label className="min-w-0 flex-1 space-y-2 text-sm"><span>搜索推荐客户</span><Input aria-label="搜索推荐客户" value={draft} maxLength={240} onChange={event => setDraft(event.target.value)} onKeyDown={event => { if (event.key === "Enter") { event.preventDefault(); search(); } }} placeholder="编号、姓名、用户名或邮箱" className="rounded border-border bg-card" /></label><Button type="button" variant="outline" onClick={search}>查询候选</Button></div>
    {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    {choice && <p role="status" className="break-words rounded border border-border bg-muted/30 p-3 text-sm">已选择：{choice.name} · #{choice.id}</p>}
    {resource.error ? <ReadError error={resource.error.message} retry={resource.refresh} /> : !resource.data ? <p role="status" className="text-sm text-muted-foreground">正在加载候选客户…</p> : <div className="space-y-3"><div className="divide-y divide-border rounded border border-border">{resource.data.items.length ? resource.data.items.map(item => <label key={item.id} className="flex min-h-14 cursor-pointer items-center gap-3 p-3 text-sm"><input type="radio" name={radioName} checked={choice?.id === item.id} onChange={() => onChoose({ id: item.id, name: item.display_name || item.username })} /><span className="min-w-0"><span className="block break-words font-medium">{item.display_name || item.username} · #{item.id}</span><span className="mt-1 block break-all text-xs text-muted-foreground">{item.username} · {item.email}</span></span></label>) : <p className="p-4 text-sm text-muted-foreground">{query ? "无匹配的合格候选客户。" : "暂无可选的推荐客户。"}</p>}</div><ReferrerPager page={page} limit={10} total={resource.data.total} busy={resource.isLoading} onChange={setPage} label="推荐客户候选" /></div>}
  </div>;
}
function referrerLabel(attributed: boolean, visible: boolean, id: number | null, name: string | null) {
  if (!attributed) return "无推荐人";
  if (!visible) return "已有推荐人；身份不在当前客户范围，已隐藏";
  return `${name || "推荐客户"}${id ? ` · #${id}` : ""}`;
}
function ReadError({ error, retry }: { error: string; retry: () => void }) {
  return <div role="alert" className="space-y-2 text-sm"><p className="break-words text-destructive">{error}</p><Button type="button" variant="outline" size="sm" onClick={retry}>重试本区域</Button></div>;
}
function ReferrerPager({ page, limit, total, busy, onChange, label }: { page: number; limit: number; total: number; busy: boolean; onChange: (page: number) => void; label: string }) {
  const pages = Math.max(1, Math.ceil(total / limit));
  return <nav aria-label={`${label}分页`} className="flex flex-wrap items-center justify-between gap-3 text-xs text-muted-foreground"><span>共 {total} 条 · 第 {page + 1} / {pages} 页</span><div className="flex gap-2"><Button type="button" variant="outline" size="sm" disabled={busy || page === 0} onClick={() => onChange(page - 1)}>上一页</Button><Button type="button" variant="outline" size="sm" disabled={busy || page + 1 >= pages} onClick={() => onChange(page + 1)}>下一页</Button></div></nav>;
}
