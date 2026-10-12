"use client";

import Link from "next/link";
import { useSearchParams, useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { api, ApiError, date, download } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { useAdminListNavigation } from "@/hooks/use-admin-list-navigation";
import { Workspace, useAdminIdentity, useAdminPermission } from "./workspace";
import { AdminPagination } from "./admin-pagination";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Dialog, DialogContent, DialogTitle, DialogDescription } from "./ui/dialog";

type Page<T> = { items: T[]; total: number; limit: number; offset: number };
type Template = { id: number; name: string; content: string; enabled: boolean; version: number; created_at: string };
type Contract = { id: number; customer_id: number; service_id: number; customer_name: string; customer_email: string; product_name: string; service_name: string; service_status: string; domain: string; paid_at: string; status: string; version: number; content: string; signer_name: string; review_note: string; carrier: string; tracking_number: string; recipient: string; address: string; phone: string; history?: { action: string; note: string; created_at: string; actor_name?: string }[] };
type Service = { id: number; customer_name: string; product_name: string; name: string; domain: string; status: string };
type Pending = { path: string; method: string; body: string; key: string };
const states = ["all", "draft", "pending", "approved", "rejected", "shipped", "void"] as const;
const actionLabels: Record<string, string> = { review: "审核", approve: "通过", reject: "驳回", submit: "提交审核", ship: "邮寄", shipping: "邮寄", void: "作废", create: "生成" };
const labels: Record<string, string> = { all: "全部", draft: "待签订", pending: "待审核", approved: "已通过", rejected: "已驳回", shipped: "已邮寄", void: "已作废", active: "运行中", suspended: "已暂停" };
const extras = { service_status: "", from: "", to: "", limit: "25" };
const selectClass = "h-9 rounded-md border bg-background px-3 min-w-0 text-sm";
const time = (value: string) => value ? date(value) : "—";

// A stored request is immutable until the server gives a definite response.
function useContractWrite(identity: number | undefined, allowed: boolean, done: () => void, scope = "contracts") {
  const storage = identity ? `zjmfgo:contract-write:${scope}:${identity}` : null;
  const [pending, setPending] = useState<Pending | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!storage || !allowed) { queueMicrotask(() => setPending(null)); return; }
    try { const raw = sessionStorage.getItem(storage); if (raw) { const request = JSON.parse(raw) as Pending; queueMicrotask(() => setPending(request)); } } catch { /* unavailable storage */ }
  }, [storage, allowed]);
  async function run(request: Pending) {
    if (!allowed || busy) return;
    setBusy(true); setError(""); setPending(request);
    try { if (storage) sessionStorage.setItem(storage, JSON.stringify(request)); } catch { /* retain in memory */ }
    try {
      await api(request.path, { method: request.method, body: request.body, headers: { "Idempotency-Key": request.key } });
      setPending(null); try { if (storage) sessionStorage.removeItem(storage); } catch { /* server result remains definite */ } done();
    } catch (e) {
      const definite = e instanceof ApiError && e.status < 500;
      if (definite) { setPending(null); try { if (storage) sessionStorage.removeItem(storage); } catch { /* server result remains definite */ } }
      setError(e instanceof ApiError && e.status === 409 ? "记录版本已变化，请重新加载详情后再操作。" : `${(e as Error).message}${definite ? "" : "。结果尚未确认，请使用原请求重试。"}`);
    } finally { setBusy(false); }
  }
  return { pending, busy, error, allowed, write: (path: string, method: string, body: unknown) => { if (!pending) void run({ path, method, body: JSON.stringify(body), key: crypto.randomUUID() }); }, retry: () => pending && void run(pending) };
}
function WriteNotice({ write }: { write: ReturnType<typeof useContractWrite> }) {
  return <>{write.error && <p role="alert" className="text-sm text-destructive">{write.error}</p>}{write.pending && <div className="rounded border p-3 text-sm space-y-2"><p className="break-all">未确认操作：{write.pending.method} {write.pending.path}。有尚未确认的请求，表单已锁定。刷新后可恢复原请求；请勿重复创建。</p><Button type="button" disabled={write.busy || !write.allowed} onClick={write.retry}>使用原请求重试</Button></div>}</>;
}
function useLeaveGuard(dirty: boolean) {
  useEffect(() => {
    if (!dirty) return;
    const unload = (e: BeforeUnloadEvent) => { e.preventDefault(); };
    const click = (e: MouseEvent) => { if ((e.target as HTMLElement).closest("a[href]") && !window.confirm("尚有未保存的合同操作，确认离开？")) e.preventDefault(); };
    window.addEventListener("beforeunload", unload); document.addEventListener("click", click, true);
    return () => { window.removeEventListener("beforeunload", unload); document.removeEventListener("click", click, true); };
  }, [dirty]);
}

export function ContractsView({ admin = false, identity }: { admin?: boolean; identity: number }) {
  const permission = useAdminPermission("contracts.manage");
  const manage = !admin || permission;
  const nav = useAdminListNavigation(true, states, "all", extras);
  const size = [25, 50, 100].includes(Number(nav.extra.limit)) ? Number(nav.extra.limit) : 25;
  const prefix = admin ? "/admin/contracts" : "/contracts";
  const params = new URLSearchParams({ limit: String(size), offset: String(nav.page * size) });
  if (nav.query) params.set("q", nav.query);
  if (nav.status !== "all") params.set("status", nav.status);
  for (const k of ["service_status", "from", "to"]) if (nav.extra[k]) params.set(k, nav.extra[k]);
  const resource = useResource<Page<Contract>>(nav.ready ? `${prefix}?${params}` : null, false);
  const [selected, setSelected] = useState<Contract[]>([]);
  const [detailID, setDetailID] = useState<number | null>(null);
  const detail = useResource<Contract>(detailID ? `${prefix}/${detailID}` : null, false);
  const [mode, setMode] = useState<"generate" | "review" | "shipping" | "submit" | "void" | null>(null);
  const [form, setForm] = useState<Record<string, string>>({});
  const [accepted, setAccepted] = useState(false);
  const [downloadError, setDownloadError] = useState("");
  const [servicePage, setServicePage] = useState(0);
  const [templatePage, setTemplatePage] = useState(0);
  const services = useResource<Page<Service>>(mode === "generate" ? `${prefix}/eligible-services?limit=25&offset=${servicePage * 25}` : null, false);
  const templates = useResource<Page<Template>>(mode === "generate" ? `${admin ? "/admin/contracts/available-templates" : "/contract-templates"}?limit=25&offset=${templatePage * 25}` : null, false);
  const write = useContractWrite(identity, manage, () => { setMode(null); setForm({}); setSelected([]); resource.refresh(); detail.refresh(); setDetailID(null); }, admin ? "admin-contracts" : "customer-contracts");
  const [contractDraftReady, setContractDraftReady] = useState(false);
  const draftKey = `zjmfgo:contract-draft:${admin ? "admin" : "customer"}:${identity}`;
  useEffect(() => {
    if (!manage) return;
    try {
      const raw = sessionStorage.getItem(draftKey);
      if (raw) {
        const draft = JSON.parse(raw) as { mode: typeof mode; form: Record<string, string>; detailID: number | null };
        if (["generate", "review", "shipping", "submit"].includes(draft.mode || "") && window.confirm("发现该账户未提交的合同草稿，恢复输入？")) queueMicrotask(() => { setMode(draft.mode); setForm(draft.form); setDetailID(draft.detailID); });
      }
    } catch { /* storage unavailable */ }
    queueMicrotask(() => setContractDraftReady(true));
  }, [draftKey, manage]);
  useEffect(() => {
    if (!manage || !contractDraftReady) return;
    try { if (mode && mode !== "void") sessionStorage.setItem(draftKey, JSON.stringify({ mode, form, detailID }));
    else sessionStorage.removeItem(draftKey); } catch { /* storage unavailable */ }
  }, [draftKey, manage, contractDraftReady, mode, form, detailID]);

  const dirty = !!mode && Object.values(form).some(Boolean);
  useLeaveGuard(dirty || !!write.pending);
  const locked = write.busy || !!write.pending || !manage;
  function close() { if (write.busy || write.pending) return; if (dirty && !window.confirm("确认关闭？尚未确认的请求仍会保留，其他未保存输入将丢弃。")) return; setMode(null); setForm({}); setDetailID(null); }
  function open(next: NonNullable<typeof mode>, row?: Contract) { if (write.pending) return; if (row) setDetailID(row.id); setForm(next === "shipping" && row ? { carrier: row.carrier ?? "", tracking_number: row.tracking_number ?? "", recipient: row.recipient ?? "", address: row.address ?? "", phone: row.phone ?? "" } : {}); setAccepted(false); setMode(next); }
  function submit(e: React.FormEvent) {
    e.preventDefault(); if (locked) return;
    const row = detail.data;
    if (mode === "generate") write.write(prefix, "POST", { service_id: Number(form.service_id), template_id: Number(form.template_id) });
    else if (mode === "void") write.write(`${prefix}/bulk-void`, "POST", { items: selected.map(r => ({ id: r.id, expected_version: r.version })), reason: form.reason });
    else if (row && mode === "review") write.write(`${prefix}/${row.id}/review`, "POST", { decision: form.decision || "approve", note: form.note || "", expected_version: row.version });
    else if (row && mode === "shipping") write.write(`${prefix}/${row.id}/shipping`, "PUT", { ...form, expected_version: row.version });
    else if (row && mode === "submit") write.write(`${prefix}/${row.id}/submit`, "POST", { signer_name: form.signer_name, accepted, expected_version: row.version });
  }
  async function documentDownload(row: Contract) {
    setDownloadError("");
    try { const blob = await download(`${prefix}/${row.id}/document`); const url = URL.createObjectURL(blob); const a = document.createElement("a"); a.href = url; a.download = `contract-${row.id}.html`; a.click(); URL.revokeObjectURL(url); } catch (e) { setDownloadError((e as Error).message); }
  }
  function field(key: string, title: string, required = true) { return <label className="grid gap-2 text-sm" key={key}>{title}<Input required={required} maxLength={key === "address" ? 500 : 200} value={form[key] || ""} onChange={e => setForm({ ...form, [key]: e.target.value })} /></label>; }
  return <Workspace title={admin ? "合同列表" : "我的合同"} admin={admin}><div className="space-y-5 min-w-0"><div className="flex flex-wrap justify-between gap-3"><h1 className="page-title">{admin ? "合同列表" : "我的合同"}</h1><div className="flex gap-2"><Button variant="outline" onClick={resource.refresh}>刷新</Button>{manage && <Button disabled={!!write.pending} onClick={() => open("generate")}>{admin ? "生成合同" : "申请合同"}</Button>}</div></div><WriteNotice write={write} />{downloadError && <p role="alert" className="text-destructive">{downloadError}</p>}<form className="flex flex-wrap gap-3 items-end" onSubmit={e => { e.preventDefault(); nav.setQuery(nav.draft.trim()); nav.setPage(0); setSelected([]); }}><label className="grid gap-1 text-sm">关键词<Input value={nav.draft} onChange={e => nav.setDraft(e.target.value)} placeholder="合同ID、客户、产品" /></label><label className="grid gap-1 text-sm">合同状态<select className={selectClass} value={nav.status} onChange={e => { nav.setStatus(e.target.value); nav.setPage(0); setSelected([]); }}>{states.map(s => <option key={s} value={s}>{labels[s]}</option>)}</select></label>{admin && <label className="grid gap-1 text-sm">产品状态<select className={selectClass} value={nav.extra.service_status} onChange={e => { nav.setExtra({ ...nav.extra, service_status: e.target.value }); nav.setPage(0); setSelected([]); }}><option value="">全部</option><option value="active">运行中</option><option value="suspended">已暂停</option><option value="pending">待处理</option><option value="terminated">已终止</option><option value="cancelled">已取消</option></select></label>}{["from", "to"].map(k => <label key={k} className="grid gap-1 text-sm">{k === "from" ? "付款开始日期" : "付款结束日期"}<Input type="date" value={nav.extra[k]} onChange={e => { nav.setExtra({ ...nav.extra, [k]: e.target.value }); nav.setPage(0); setSelected([]); }} /></label>)}<Button type="submit">查询</Button></form>{resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <><div className="overflow-x-auto border-y max-w-full"><table className="w-full min-w-[850px] text-sm"><thead><tr className="text-left border-b">{["ID", "客户", "产品信息", "付款时间", "产品状态", "合同状态", "操作"].map(s => <th className="p-3" key={s}>{s}</th>)}</tr></thead><tbody>{resource.data.items.map(row => <tr className="border-b" key={row.id}><td className="p-3 whitespace-nowrap">{admin && manage && <input aria-label={`选择合同 ${row.id}`} type="checkbox" className="mr-2" disabled={locked || row.status === "void"} checked={selected.some(r => r.id === row.id)} onChange={e => setSelected(e.target.checked ? [...selected, row] : selected.filter(r => r.id !== row.id))} />}#{row.id}</td><td className="p-3">{admin ? <Link className="text-primary hover:underline" href={`/admin/customers/${row.customer_id}`}>{row.customer_name}</Link> : row.customer_name}<div className="text-xs text-muted-foreground">{row.customer_email}</div></td><td className="p-3">{admin ? <Link className="text-primary hover:underline" href={`/admin/services/${row.service_id}`}>{row.product_name}</Link> : row.product_name}<div className="text-xs text-muted-foreground">{row.service_name} {row.domain}</div></td><td className="p-3 whitespace-nowrap">{time(row.paid_at)}</td><td className="p-3">{labels[row.service_status] || row.service_status || "—"}</td><td className="p-3">{labels[row.status] || row.status}</td><td className="p-3"><div className="flex flex-wrap gap-2"><Button variant="ghost" size="sm" disabled={write.busy || !!write.pending} onClick={() => { setDetailID(row.id); setMode(null); }}>详情</Button>{admin && manage && row.status === "pending" && <Button variant="ghost" size="sm" disabled={locked} onClick={() => open("review", row)}>审核</Button>}{admin && manage && ["approved", "shipped"].includes(row.status) && <Button variant="ghost" size="sm" disabled={locked} onClick={() => open("shipping", row)}>邮寄管理</Button>}{!admin && ["draft", "rejected"].includes(row.status) && <Button size="sm" disabled={locked} onClick={() => open("submit", row)}>确认并提交</Button>}</div></td></tr>)}{!resource.data.items.length && <tr><td colSpan={7} className="p-12 text-center text-muted-foreground">暂无符合条件的合同</td></tr>}</tbody></table></div><AdminPagination total={resource.data.total} page={nav.page} pageSize={size} onPageChange={p => { nav.setPage(p); setSelected([]); }} onPageSizeChange={s => { nav.setExtra({ ...nav.extra, limit: String(s) }); nav.setPage(0); setSelected([]); }} />{admin && manage && <div className="flex flex-wrap gap-3 items-center text-sm"><span>已选中 {selected.length} 个合同</span><Button variant="destructive" disabled={!selected.length || locked} onClick={() => open("void")}>作废合同</Button></div>}</>}
    <Dialog open={!!mode || !!detailID} onOpenChange={value => { if (!value) close(); }}><DialogContent className="sm:max-w-2xl max-h-[85dvh] overflow-y-auto"><DialogTitle>{mode === "generate" ? "生成合同" : mode === "review" ? "审核合同" : mode === "shipping" ? "邮寄管理" : mode === "submit" ? "确认合同内容并提交审核" : mode === "void" ? "确认作废合同" : "合同详情"}</DialogTitle><DialogDescription>{mode === "submit" ? "姓名确认用于提交审核，不是具有实名验证的法律电子签章。" : mode === "void" ? `将作废选中的 ${selected.length} 份合同，此操作不可撤销。` : "合同正文为纯文本；下载后可使用浏览器打印。"}</DialogDescription><WriteNotice write={write} />{!mode && detailID ? detail.error ? <Failure error={detail.error} retry={detail.refresh} /> : !detail.data ? <Loading /> : <div className="space-y-4"><p className="text-sm">#{detail.data.id} · {labels[detail.data.status]} · 版本 {detail.data.version}</p><p className="text-sm">{detail.data.customer_name} · {detail.data.product_name} · {detail.data.service_name}</p><pre className="whitespace-pre-wrap break-words text-sm rounded border p-4 font-sans">{detail.data.content}</pre>{detail.data.signer_name && <p>确认姓名：{detail.data.signer_name}</p>}{detail.data.review_note && <p>审核备注：{detail.data.review_note}</p>}{detail.data.carrier && <p className="break-words">邮寄：{detail.data.carrier} {detail.data.tracking_number} · {detail.data.recipient} · {detail.data.phone} · {detail.data.address}</p>}<h3 className="font-semibold">变更历史</h3>{detail.data.history?.length ? <ul className="space-y-2 text-sm">{detail.data.history.map((h, i) => <li key={i}>{time(h.created_at)} · {actionLabels[h.action] || "状态变更"} · {h.actor_name} {h.note}</li>)}</ul> : <p className="text-sm text-muted-foreground">暂无变更历史</p>}<Button variant="outline" onClick={() => void documentDownload(detail.data!)}>下载合同（可打印）</Button></div> : <form onSubmit={submit} className="space-y-4"><fieldset disabled={locked} className="space-y-4">{mode === "generate" ? <>{services.error ? <Failure error={services.error} retry={services.refresh} /> : !services.data ? <Loading /> : <><label className="grid gap-2 text-sm">已付款产品服务<select required className={selectClass} value={form.service_id || ""} onChange={e => setForm({ ...form, service_id: e.target.value })}><option value="">请选择服务</option>{services.data.items.map(s => <option key={s.id} value={s.id}>#{s.id} {s.customer_name} {s.product_name} {s.name} {s.domain}</option>)}</select></label>{!services.data.items.length && <p className="text-sm text-muted-foreground">暂无可申请合同的已付款服务</p>}<AdminPagination total={services.data.total} page={servicePage} pageSize={25} onPageChange={p => { setServicePage(p); setForm({ ...form, service_id: "" }); }} /></>}{templates.error ? <Failure error={templates.error} retry={templates.refresh} /> : !templates.data ? <Loading /> : <><label className="grid gap-2 text-sm">合同模板<select required className={selectClass} value={form.template_id || ""} onChange={e => setForm({ ...form, template_id: e.target.value })}><option value="">请选择启用的模板</option>{templates.data.items.filter(t => admin || t.enabled).map(t => <option key={t.id} value={t.id}>{t.name}</option>)}</select></label><AdminPagination total={templates.data.total} page={templatePage} pageSize={25} onPageChange={p => { setTemplatePage(p); setForm({ ...form, template_id: "" }); }} /></>}</> : mode === "void" ? field("reason", "作废原因") : detail.error ? <Failure error={detail.error} retry={detail.refresh} /> : !detail.data ? <Loading /> : mode === "shipping" ? [ ["carrier", "快递公司"], ["tracking_number", "快递单号"], ["recipient", "收件人"], ["address", "收件地址"], ["phone", "联系电话"] ].map(([k, title]) => field(k, title)) : mode === "review" ? <><label className="grid gap-2 text-sm">审核结果<select className={selectClass} value={form.decision || "approve"} onChange={e => setForm({ ...form, decision: e.target.value })}><option value="approve">通过</option><option value="reject">驳回</option></select></label>{field("note", "审核备注", form.decision === "reject")}</> : mode === "submit" ? <><pre className="whitespace-pre-wrap break-words text-sm rounded border p-4 font-sans">{detail.data.content}</pre>{field("signer_name", "确认姓名")}<label className="flex gap-2 text-sm"><input type="checkbox" required checked={accepted} onChange={e => setAccepted(e.target.checked)} />我已阅读并确认合同内容，提交审核</label></> : null}</fieldset><div className="flex gap-2 pt-3 border-t"><Button type="button" variant="outline" disabled={write.busy || !!write.pending} onClick={close}>取消</Button><Button type="submit" disabled={locked || (mode !== "generate" && mode !== "void" && !detail.data) || (mode === "generate" && (!form.service_id || !form.template_id)) || (mode === "submit" && !accepted)}>{write.busy ? "处理中…" : mode === "void" ? "确认作废" : "确定"}</Button></div></form>}</DialogContent></Dialog></div></Workspace>;
}

export function ContractTemplates() {
  const user = useAdminIdentity(); const manage = useAdminPermission("settings.manage");
  const params = useSearchParams(); const router = useRouter();
  const readPage = () => { const value = new URLSearchParams(window.location.search).get("template_page") || "0"; return /^\d{1,6}$/.test(value) ? Number(value) : 0; };
  const [page, setPage] = useState(() => { const value = params.get("template_page") || "0"; return /^\d{1,6}$/.test(value) ? Number(value) : 0; });
  useEffect(() => { const apply = () => setPage(readPage()); window.addEventListener("popstate", apply); return () => window.removeEventListener("popstate", apply); }, []);
  function changePage(next: number) { setPage(next); const p = new URLSearchParams(window.location.search); if (next) p.set("template_page", String(next)); else p.delete("template_page"); router.replace(`/admin/contract-settings${p.size ? `?${p}` : ""}`); }
  const resource = useResource<Page<Template>>(`/admin/contract-templates?limit=25&offset=${page * 25}`, false);
  const [form, setForm] = useState<Template | null>(null);
  const [dirty, setDirty] = useState(false);
  const write = useContractWrite(user?.id, manage, () => { setForm(null); setDirty(false); resource.refresh(); }, "contract-templates");
  const templateDraftKey = `zjmfgo:template-draft:${user?.id || "none"}`;
  const [draftReady, setDraftReady] = useState(false);
  useEffect(() => {
    if (!manage || !user?.id) return;
    let recovered: Template | null = null;
    try { const raw = sessionStorage.getItem(templateDraftKey); if (raw && window.confirm("恢复该账户未保存的合同模板草稿？")) recovered = JSON.parse(raw) as Template; } catch { /* storage unavailable */ }
    queueMicrotask(() => { if (recovered) { setForm(recovered); setDirty(true); } setDraftReady(true); });
  }, [manage, user?.id, templateDraftKey]);
  useEffect(() => {
    if (!manage || !draftReady) return;
    try { if (form && dirty) sessionStorage.setItem(templateDraftKey, JSON.stringify(form)); else sessionStorage.removeItem(templateDraftKey); } catch { /* storage unavailable */ }
  }, [manage, draftReady, form, dirty, templateDraftKey]);

  useLeaveGuard(dirty || !!write.pending);
  function close() { if (write.busy || write.pending || (dirty && !window.confirm("放弃尚未保存的模板输入？未确认请求仍会保留。"))) return; setForm(null); setDirty(false); }
  return <div className="space-y-4"><div className="flex flex-wrap items-center justify-between gap-3"><h2 className="font-semibold">合同模板管理</h2>{manage && <Button disabled={!!write.pending} onClick={() => { setDirty(false); setForm({ id: 0, name: "", content: "", enabled: true, version: 0, created_at: "" }); }}>创建模板</Button>}</div><WriteNotice write={write} />{resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <><div className="overflow-x-auto"><table className="w-full min-w-[550px] text-sm"><thead><tr className="text-left border-b">{["ID", "模板名称", "状态", "版本", "创建时间", "操作"].map(t => <th key={t} className="p-3">{t}</th>)}</tr></thead><tbody>{resource.data.items.map(t => <tr key={t.id} className="border-b"><td className="p-3">{t.id}</td><td className="p-3">{t.name}</td><td className="p-3">{t.enabled ? "启用" : "停用"}</td><td className="p-3">{t.version}</td><td className="p-3">{time(t.created_at)}</td><td className="p-3"><Button size="sm" variant="ghost" disabled={!!write.pending} onClick={() => { setForm(t); setDirty(false); }}>{manage ? "编辑" : "查看"}</Button></td></tr>)}{!resource.data.items.length && <tr><td colSpan={6} className="p-10 text-center text-muted-foreground">暂无合同模板</td></tr>}</tbody></table></div><AdminPagination total={resource.data.total} page={page} pageSize={25} onPageChange={changePage} /></>}<Dialog open={!!form} onOpenChange={v => { if (!v) close(); }}><DialogContent className="sm:max-w-2xl max-h-[85dvh] overflow-y-auto"><DialogTitle>{form?.id ? "编辑合同模板" : "创建合同模板"}</DialogTitle><DialogDescription>支持变量：{["customer_name", "customer_email", "product_name", "service_name", "domain", "paid_at"].map(k => `{{${k}}}`).join("、")}。正文按纯文本保存。</DialogDescription><WriteNotice write={write} />{form && <form className="space-y-4" onSubmit={e => { e.preventDefault(); write.write(`/admin/contract-templates${form.id ? `/${form.id}` : ""}`, form.id ? "PUT" : "POST", { name: form.name, content: form.content, enabled: form.enabled, ...(form.id ? { expected_version: form.version } : {}) }); }}><fieldset disabled={!manage || write.busy || !!write.pending} className="space-y-4"><label className="grid gap-2 text-sm">模板名称<Input required maxLength={200} value={form.name} onChange={e => { setDirty(true); setForm({ ...form, name: e.target.value }); }} /></label><label className="grid gap-2 text-sm">正文<textarea required className="border rounded-md p-3 min-h-64 w-full" maxLength={100000} value={form.content} onChange={e => { setDirty(true); setForm({ ...form, content: e.target.value }); }} /></label><label className="flex gap-2 text-sm"><input type="checkbox" checked={form.enabled} onChange={e => { setDirty(true); setForm({ ...form, enabled: e.target.checked }); }} />启用模板</label></fieldset><div className="flex gap-2 border-t pt-3"><Button variant="outline" type="button" onClick={close}>取消</Button>{manage && <Button disabled={write.busy || !!write.pending || !dirty || !form.name.trim() || !form.content.trim()}>保存模板</Button>}</div></form>}</DialogContent></Dialog></div>;
}

export function ContractSystemSettings({ data, refresh }: { data: { enabled: boolean; version: number }; refresh: () => void }) {
  const user = useAdminIdentity(); const manage = useAdminPermission("settings.manage");
  const [enabled, setEnabled] = useState(data.enabled);
  const [draftReady, setDraftReady] = useState(false);
  const [saved, setSaved] = useState(false);
  const dirty = !saved && enabled !== data.enabled;
  const draftKey = `zjmfgo:contract-settings-draft:${user?.id || "none"}`;
  useEffect(() => {
    if (!manage || !user?.id) return;
    let recovered: boolean | null = null;
    try { const raw = sessionStorage.getItem(draftKey); if (raw && window.confirm("恢复该账户尚未保存的合同开关设置？")) { const draft = JSON.parse(raw) as { enabled: boolean }; if (typeof draft.enabled === "boolean") recovered = draft.enabled; } } catch { /* storage unavailable */ }
    queueMicrotask(() => { if (recovered !== null) setEnabled(recovered); setDraftReady(true); });
  }, [manage, user?.id, draftKey]);
  useEffect(() => {
    if (!manage || !draftReady) return;
    try { if (dirty) sessionStorage.setItem(draftKey, JSON.stringify({ enabled })); else sessionStorage.removeItem(draftKey); } catch { /* storage unavailable */ }
  }, [manage, draftReady, dirty, enabled, draftKey]);
  const write = useContractWrite(user?.id, manage, () => { setSaved(true); try { sessionStorage.removeItem(draftKey); } catch { /* storage unavailable */ } refresh(); }, "contract-settings");
  useLeaveGuard(dirty || !!write.pending);
  return <form className="space-y-5 max-w-3xl" onSubmit={e => { e.preventDefault(); write.write("/admin/contract-settings", "PUT", { enabled, expected_version: data.version }); }}><WriteNotice write={write} /><label className="flex gap-3 text-sm"><input type="checkbox" checked={enabled} disabled={!manage || write.busy || !!write.pending} onChange={e => { setSaved(false); setEnabled(e.target.checked); }} />启用合同功能</label><div className="flex flex-wrap gap-3 border-t pt-5"><span className="text-xs text-muted-foreground">版本 {data.version}</span><Button type="button" variant="outline" disabled={!dirty || write.busy || !!write.pending} onClick={() => { if (window.confirm("确认放弃更改？")) setEnabled(data.enabled); }}>取消更改</Button>{manage && <Button disabled={!dirty || write.busy || !!write.pending}>保存更改</Button>}</div></form>;
}
