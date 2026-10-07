"use client";

import { useRef, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { ArrowLeft, ArrowRight, Ban, Pencil, Plus, Save, Search, Trash2 } from "lucide-react";
import { api, money, ApiError } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace, useAdminPermission } from "./workspace";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Textarea } from "./ui/textarea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "./ui/dialog";

export type ManualInvoiceRecord = {
  id: number;
  user_id: number;
  invoice_no: string;
  invoice_kind: "manual" | "automatic";
  status: string;
  version: number;
  amount_cents: number;
  due_at: string | null;
  customer_name?: string;
  email?: string;
  import_review_required: boolean;
  payment_pending: boolean;
  items: { description: string; amount_cents: number }[];
};
type Customer = { id: number; username: string; display_name: string; email: string; status: string };
type CustomerPage = { items: Customer[]; total: number };
type Line = { key: number; description: string; amount: string };
type InvoiceResult = { invoice_id: number; version: number };
const amountLimit = 10000000000;

function cents(value: string): number | null {
  const raw = value.trim();
  if (!/^-?\d{1,9}(\.\d{1,2})?$/.test(raw)) return null;
  const [whole, fraction = ""] = raw.replace(/^-/, "").split(".");
  const magnitude = Number(whole) * 100 + Number(fraction.padEnd(2, "0"));
  return magnitude > amountLimit ? null : raw.startsWith("-") ? -magnitude : magnitude;
}
function localDate(value: string | null): string {
  if (!value) return "";
  const parsed = new Date(value);
  return new Date(parsed.getTime() - parsed.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
}

export function InvoiceCreateLink() {
  const allowed = useAdminPermission("invoices.manage");
  return allowed ? <Button asChild><Link href="/admin/invoices/new"><Plus />创建账单</Link></Button> : null;
}

export function ManualInvoiceTools({ invoice, refresh }: { invoice: ManualInvoiceRecord; refresh: () => void }) {
  const allowed = useAdminPermission("invoices.manage");
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const retry = useRef<{ body: string; key: string } | null>(null);
  if (!allowed || invoice.invoice_kind !== "manual" || invoice.status !== "unpaid") return null;
  async function cancel() {
    const body = JSON.stringify({ expected_version: invoice.version, reason: reason.trim() });
    if (retry.current?.body !== body) retry.current = { body, key: `manual-cancel-${crypto.randomUUID()}` };
    setBusy(true); setError("");
    try {
      await api(`/admin/invoices/${invoice.id}/cancel`, { method: "POST", body, headers: { "Idempotency-Key": retry.current.key } });
      setOpen(false); refresh();
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  return <div className="flex flex-wrap gap-2">
    {invoice.payment_pending || invoice.import_review_required ? <Button variant="outline" size="sm" disabled><Pencil />编辑账单</Button> : <Button variant="outline" size="sm" asChild><Link href={`/admin/invoices/${invoice.id}/edit`}><Pencil />编辑账单</Link></Button>}
    <Button variant="outline" size="sm" disabled={invoice.payment_pending || invoice.import_review_required} onClick={() => setOpen(true)}><Ban />作废账单</Button>
    <Dialog open={open} onOpenChange={value => { if (!busy) setOpen(value); }}><DialogContent showCloseButton={!busy}><DialogHeader><DialogTitle>作废人工账单</DialogTitle><DialogDescription>作废后此账单不可付款，项目和操作记录将保留。</DialogDescription></DialogHeader><p className="font-mono text-xs break-all">{invoice.invoice_no}</p><Label htmlFor="invoice-cancel-reason">作废原因</Label><Textarea id="invoice-cancel-reason" value={reason} maxLength={500} onChange={e => setReason(e.target.value)} disabled={busy} />{error && <p role="alert" className="text-sm text-destructive">{error}</p>}<DialogFooter><Button variant="outline" disabled={busy} onClick={() => setOpen(false)}>返回</Button><Button variant="destructive" disabled={busy || reason.trim().length < 3} onClick={() => void cancel()}><Ban />确认作废</Button></DialogFooter></DialogContent></Dialog>
  </div>;
}

function CustomerPicker({ selected, onSelect, disabled }: { selected: Customer | null; onSelect: (customer: Customer) => void; disabled: boolean }) {
  const [draft, setDraft] = useState("");
  const [query, setQuery] = useState("");
  const [offset, setOffset] = useState(0);
  const { data, error, refresh } = useResource<CustomerPage>(`/admin/invoice-customers?q=${encodeURIComponent(query)}&limit=25&offset=${offset}`, false);
  const choices = data?.items ?? [];
  const options = selected && !choices.some(c => c.id === selected.id) ? [selected, ...choices] : choices;
  function search() { setQuery(draft); setOffset(0); }
  return <div className="space-y-3">
    <div className="flex gap-2"><Input value={draft} maxLength={120} aria-label="搜索账单客户" placeholder="客户账号、名称或邮箱" onChange={e => setDraft(e.target.value)} disabled={disabled} onKeyDown={e => { if (e.key === "Enter") { e.preventDefault(); search(); } }} /><Button type="button" variant="outline" size="icon" title="搜索客户" aria-label="搜索客户" disabled={disabled} onClick={search}><Search /></Button></div>
    {error ? <Failure error={error} retry={refresh} /> : !data ? <Loading /> : <>
      <Select value={selected ? String(selected.id) : ""} disabled={disabled} onValueChange={id => { const choice = options.find(c => c.id === Number(id)); if (choice) onSelect(choice); }}><SelectTrigger className="w-full" aria-label="账单客户"><SelectValue placeholder="选择客户" /></SelectTrigger><SelectContent>{options.map(c => <SelectItem value={String(c.id)} key={c.id}>{c.display_name || c.username} · {c.email}{c.status !== "active" ? " · 已停用" : ""}</SelectItem>)}{options.length === 0 && <SelectItem value="empty" disabled>暂无匹配客户</SelectItem>}</SelectContent></Select>
      <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground"><span>{data.total} 位客户</span><div className="flex gap-2"><Button variant="ghost" type="button" size="icon" title="上一批客户" aria-label="上一批客户" disabled={disabled || offset === 0} onClick={() => setOffset(v => Math.max(0, v - 25))}><ArrowLeft /></Button><Button variant="ghost" type="button" size="icon" title="下一批客户" aria-label="下一批客户" disabled={disabled || offset + 25 >= data.total} onClick={() => setOffset(v => v + 25)}><ArrowRight /></Button></div></div>
    </>}
  </div>;
}

export function ManualInvoiceEditorView({ id }: { id?: number }) {
  return <Workspace title={id ? "编辑人工账单" : "创建人工账单"} admin><ManualInvoiceEditor id={id} /></Workspace>;
}

function ManualInvoiceEditor({ id }: { id?: number }) {
  const allowed = useAdminPermission("invoices.manage");
  const { data, error, refresh } = useResource<ManualInvoiceRecord>(allowed && id ? `/admin/invoices/${id}` : null, false);
  const back = id ? `/admin/invoices/${id}` : "/admin/invoices";
  return <>
    <Button variant="ghost" asChild className="mb-5"><Link href={back}><ArrowLeft />{id ? "返回账单详情" : "返回账单列表"}</Link></Button>
    <div className="admin-page-heading mb-4"><h1 className="page-title">{id ? "编辑人工账单" : "创建人工账单"}</h1></div>
    {!allowed ? <p role="alert" className="text-sm text-muted-foreground">没有维护账单的权限</p> : error ? <Failure error={error} retry={refresh} /> : id && !data ? <Loading /> : data && (data.invoice_kind !== "manual" || data.status !== "unpaid" || data.payment_pending || data.import_review_required) ? <p role="alert" className="text-sm text-muted-foreground">此账单当前不可编辑</p> : <ManualInvoiceForm key={`${id ?? "new"}:${data?.version ?? 0}`} invoice={data ?? undefined} reload={refresh} />}
  </>;
}

function ManualInvoiceForm({ invoice, reload }: { invoice?: ManualInvoiceRecord; reload: () => void }) {
  const router = useRouter();
  const [selected, setSelected] = useState<Customer | null>(null);
  const [due, setDue] = useState(localDate(invoice?.due_at ?? null));
  const [lines, setLines] = useState<Line[]>(invoice ? invoice.items.map((item, index) => ({ key: index + 1, description: item.description, amount: (item.amount_cents / 100).toFixed(2) })) : [{ key: 1, description: "", amount: "" }]);
  const nextKey = useRef(lines.length + 1);
  const retry = useRef<{ body: string; key: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [stale, setStale] = useState(false);
  const parsed = lines.map(line => cents(line.amount));
  const total = parsed.every(n => n !== null) ? parsed.reduce<number>((sum, n) => sum + (n ?? 0), 0) : null;
  const valid = total !== null && total >= 0 && total <= amountLimit && lines.every(line => line.description.trim()) && due && (invoice || selected);
  function change(key: number, field: "description" | "amount", value: string) { setLines(current => current.map(line => line.key === key ? { ...line, [field]: value } : line)); }
  async function save(event: React.FormEvent) {
    event.preventDefault();
    if (!valid || busy) return;
    setBusy(true); setError(""); setStale(false);
    try {
      const body = JSON.stringify({ user_id: invoice?.user_id ?? selected?.id, due_at: new Date(due).toISOString(), expected_version: invoice?.version ?? 0, items: lines.map((line, index) => ({ description: line.description.trim(), amount_cents: parsed[index] })) });
      if (retry.current?.body !== body) retry.current = { body, key: `manual-save-${crypto.randomUUID()}` };
      const result = await api<InvoiceResult>(invoice ? `/admin/invoices/${invoice.id}` : "/admin/invoices", { method: invoice ? "PUT" : "POST", body, headers: { "Idempotency-Key": retry.current.key } });
      router.push(`/admin/invoices/${result.invoice_id}`);
      router.refresh();
    } catch (e) {
      setError((e as Error).message);
      setStale(e instanceof ApiError && (e.code === "stale_invoice" || e.code === "invoice_payment_pending" || e.code === "invoice_not_editable"));
    } finally { setBusy(false); }
  }
  return <form onSubmit={save} className="space-y-5">
    <div className="grid md:grid-cols-2 gap-5 border-b pb-5">
      <div className="space-y-3"><Label>账单客户</Label>{invoice ? <p className="text-sm break-all">{invoice.customer_name} · {invoice.email}</p> : <CustomerPicker selected={selected} onSelect={setSelected} disabled={busy} />}</div>
      <div className="space-y-3"><Label htmlFor="manual-invoice-due">到期时间</Label><Input id="manual-invoice-due" type="datetime-local" value={due} min="2000-01-01T00:00" max="2200-12-31T23:59" required onChange={e => setDue(e.target.value)} disabled={busy} /><p className="text-sm text-muted-foreground">币种 CNY</p></div>
    </div>
    <section><div className="flex justify-between items-center gap-3 mb-4"><h2 className="font-semibold">账单项目</h2><Button type="button" size="sm" variant="outline" disabled={busy || lines.length >= 100} onClick={() => setLines(current => [...current, { key: nextKey.current++, description: "", amount: "" }])}><Plus />添加项目</Button></div>
      <div className="space-y-4">{lines.map((line, index) => <div key={line.key} className="grid sm:grid-cols-[minmax(0,1fr)_10rem_2.25rem] gap-3 border-b pb-4">
        <div><Label className="mb-2" htmlFor={`invoice-description-${line.key}`}>项目 {index + 1}</Label><Textarea id={`invoice-description-${line.key}`} rows={2} maxLength={500} required value={line.description} disabled={busy} onChange={e => change(line.key, "description", e.target.value)} /></div>
        <div><Label className="mb-2" htmlFor={`invoice-amount-${line.key}`}>金额（元）</Label><Input id={`invoice-amount-${line.key}`} type="text" maxLength={14} placeholder="0.00" required value={line.amount} disabled={busy} onChange={e => change(line.key, "amount", e.target.value)} aria-invalid={line.amount !== "" && parsed[index] === null} />{line.amount !== "" && parsed[index] === null && <p className="text-xs text-destructive mt-2">金额最多两位小数，限 ±1 亿元</p>}</div>
        <Button type="button" className="sm:mt-7" variant="ghost" size="icon" title={`删除项目 ${index + 1}`} aria-label={`删除项目 ${index + 1}`} disabled={busy || lines.length === 1} onClick={() => setLines(current => current.filter(item => item.key !== line.key))}><Trash2 /></Button>
      </div>)}</div>
    </section>
    <div className="flex flex-wrap items-center justify-between gap-5 border-t pt-4"><div><p className="text-sm text-muted-foreground">应收总额</p><p className="text-xl font-semibold money mt-1">{total === null ? "-" : money(total)}</p>{total !== null && (total < 0 || total > amountLimit) && <p className="text-sm text-destructive mt-2">总额必须在 0 到 1 亿元之间</p>}</div><div className="flex gap-2"><Button variant="outline" type="button" disabled={busy} asChild><Link href={invoice ? `/admin/invoices/${invoice.id}` : "/admin/invoices"}>取消</Link></Button><Button disabled={busy || !valid} type="submit"><Save />{busy ? "保存中" : "保存账单"}</Button></div></div>
    {error && <div role="alert" className="space-y-3"><p className="text-sm text-destructive">{error}</p>{stale && <Button type="button" variant="outline" disabled={busy} onClick={reload}>重新载入账单</Button>}</div>}
  </form>;
}
