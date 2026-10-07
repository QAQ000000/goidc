"use client";
import { useState } from "react";
import { useSearchParams } from "next/navigation";
import Link from "next/link";
import { ArrowLeft, Save, Trash2 } from "lucide-react";
import { api, date, money, type Contact } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Checkbox } from "./ui/checkbox";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Table, TableHeader, TableHead, TableBody, TableRow, TableCell } from "./ui/table";
import { Failure, Loading, Status } from "./common";
import { Workspace, PermissionGate, useAdminPermission } from "./workspace";
import { CustomerFileManager } from "./customer-files";
type Row = Record<string, unknown>;
type AdminCustomer = {
  id: number;
  username: string;
  email: string;
  display_name: string;
  status: string;
  balance_cents: number;
  created_at: string;
  sales_owner_id: number | null;
  sales_owner_name: string | null;
  group_id: number | null;
  group_name: string | null;
};

export function AdminCustomerDetailView({ customerId }: { customerId: number }) {
  const searchParams = useSearchParams();
  const requestedTab = searchParams.get("tab");
  const tab: CustomerDetailTab = requestedTab && Object.hasOwn(customerDetailTabLabel, requestedTab) ? requestedTab as CustomerDetailTab : "summary";
  const backParams = new URLSearchParams(searchParams.toString());
  backParams.delete("tab");
  const returnHref = `/admin/customers${backParams.toString() ? `?${backParams.toString()}` : ""}`;
  const customer = useResource<AdminCustomer>(`/admin/customers/${customerId}`, false);
  return <Workspace title="客户详情" admin>
    {customer.error ? <Failure error={customer.error} retry={customer.refresh} /> : !customer.data ? <Loading /> : <>
      <header className="flex flex-wrap items-center justify-between gap-3 mb-5"><div><h1 className="text-xl font-semibold break-words">{customer.data.display_name || customer.data.username}</h1><p className="text-sm text-muted-foreground mt-1">客户 #{customer.data.id} · {customer.data.username}</p></div><Button variant="outline" asChild><Link href={returnHref}><ArrowLeft size={16} />返回客户管理</Link></Button></header>
      <section className="data-surface p-5 mb-5">
        <div className="flex flex-wrap items-start justify-between gap-5">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2"><h2 className="text-lg font-semibold break-words">{customer.data.display_name || "未命名客户"}</h2><Status value={customer.data.status} /></div>
            <p className="text-sm text-muted-foreground mt-2 break-all">{customer.data.email} · 注册于 {date(customer.data.created_at)}</p>
          </div>
          <div className="text-left sm:text-right"><p className="text-xs text-muted-foreground">账户余额</p><strong className="money text-2xl block mt-1">{money(customer.data.balance_cents)}</strong></div>
        </div>
        <dl className="grid grid-cols-1 sm:grid-cols-3 gap-4 border-t mt-5 pt-4 text-sm">
          <div><dt className="text-muted-foreground">客户分组</dt><dd className="mt-1">{customer.data.group_name || "未分组"}</dd></div>
          <div><dt className="text-muted-foreground">销售归属</dt><dd className="mt-1">{customer.data.sales_owner_name || "无归属"}</dd></div>
          <div><dt className="text-muted-foreground">账号</dt><dd className="mt-1 break-all">{customer.data.username}</dd></div>
        </dl>
      </section>
      <nav className="flex gap-1 overflow-x-auto border-b mb-5" aria-label="客户详情分区">
        {(["summary", "profile", "services", "orders", "invoices", "transactions", "contacts", "tickets", "files"] as CustomerDetailTab[]).map((value) => {
          const params = new URLSearchParams(searchParams.toString());
          if (value === "summary") params.delete("tab"); else params.set("tab", value);
          const href = `/admin/customers/${customerId}${params.toString() ? `?${params.toString()}` : ""}`;
          const label = customerDetailTabLabel[value];
          return value === tab ? <span key={value} className="border-b-2 border-primary px-4 py-3 text-sm font-medium text-primary whitespace-nowrap" aria-current="page">{label}</span> : <Link key={value} className="px-4 py-3 text-sm text-muted-foreground hover:text-primary whitespace-nowrap" href={href}>{label}</Link>;
        })}
      </nav>
      {tab === "summary" ? <CustomerSummary customer={customer.data} /> : tab === "profile" ? <CustomerProfile key={`${customer.data.id}:${customer.data.display_name}:${customer.data.email}:${customer.data.status}`} customer={customer.data} saved={customer.refresh} cancelHref={`/admin/customers/${customerId}${backParams.toString() ? `?${backParams.toString()}` : ""}`} /> : tab === "contacts" ? <ContactsPanel adminCustomerId={customerId} /> : tab === "files" ? <CustomerFileManager customerId={customerId} /> : <AdminCustomerRelatedView key={`${customerId}:${tab}`} customerId={customerId} tab={tab} />}
    </>}
  </Workspace>;
}

function CustomerSummary({ customer }: { customer: AdminCustomer }) {
  const items = [["客户编号", `#${customer.id}`], ["姓名", customer.display_name || "-"], ["邮箱", customer.email], ["账户", customer.username], ["客户分组", customer.group_name || "未分组"], ["销售", customer.sales_owner_name || "无归属"], ["注册时间", date(customer.created_at)]];
  return <section className="border-y bg-white"><h2 className="border-b px-5 py-4 font-semibold">客户摘要</h2><div className="grid lg:grid-cols-[2fr_1fr] divide-y lg:divide-y-0 lg:divide-x"><dl className="grid sm:grid-cols-2 gap-x-8 gap-y-5 p-5 text-sm">{items.map(([label, value]) => <div key={label} className="min-w-0"><dt className="text-muted-foreground">{label}</dt><dd className="mt-1 break-all">{value}</dd></div>)}</dl><div className="p-5"><h3 className="text-sm font-medium">资金概况</h3><p className="text-sm text-muted-foreground mt-5">账户余额</p><p className="money text-2xl font-semibold mt-2">{money(customer.balance_cents)}</p><p className="text-sm text-muted-foreground mt-5">账户状态</p><div className="mt-2"><Status value={customer.status} /></div></div></div></section>;
}

function CustomerProfile({ customer, saved, cancelHref }: { customer: AdminCustomer; saved: () => void; cancelHref: string }) {
  const manage = useAdminPermission("customers.manage");
  const [name, setName] = useState(customer.display_name);
  const [email, setEmail] = useState(customer.email);
  const [status, setStatus] = useState(customer.status);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [failed, setFailed] = useState(false);
  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!manage || busy) return;
    setBusy(true); setMessage(""); setFailed(false);
    try {
      await api(`/admin/customers/${customer.id}`, { method: "PATCH", body: JSON.stringify({ display_name: name, email, status }) });
      setMessage("客户资料已保存"); saved();
    } catch (error) { setFailed(true); setMessage((error as Error).message); }
    finally { setBusy(false); }
  }
  const fieldStyle = "block text-sm min-w-0";
  return <form onSubmit={event => void save(event)} className="border-y bg-white"><fieldset disabled={!manage || busy}><section className="p-5 border-b"><h2 className="font-semibold mb-5">个人资料</h2><div className="grid sm:grid-cols-2 xl:grid-cols-4 gap-5"><label className={fieldStyle}>姓名<Input className="mt-2" value={name} required maxLength={120} onChange={event => setName(event.target.value)} /></label></div></section><section className="p-5 border-b"><h2 className="font-semibold mb-5">账户信息</h2><div className="grid sm:grid-cols-2 xl:grid-cols-4 gap-5"><label className={fieldStyle}>账户<Input className="mt-2" value={customer.username} readOnly disabled /></label><label className={fieldStyle}>邮箱<Input className="mt-2" value={email} type="email" required maxLength={254} onChange={event => setEmail(event.target.value)} /></label></div></section><section className="p-5"><h2 className="font-semibold mb-5">其他设置</h2><div className="grid sm:grid-cols-2 xl:grid-cols-4 gap-5"><label className={fieldStyle}>状态<Select value={status} onValueChange={setStatus} disabled={!manage || busy}><SelectTrigger className="mt-2" aria-label="客户状态"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="active">启用</SelectItem><SelectItem value="disabled">停用</SelectItem></SelectContent></Select></label><label className={fieldStyle}>客户分组<Input className="mt-2" value={customer.group_name || "未分组"} disabled /></label><label className={fieldStyle}>销售<Input className="mt-2" value={customer.sales_owner_name || "无归属"} disabled /></label></div></section></fieldset>{message && <p role={failed ? "alert" : "status"} className={`px-5 pb-4 text-sm ${failed ? "text-destructive" : "text-emerald-700"}`}>{message}</p>}<div className="border-t p-4 flex flex-wrap gap-3"><PermissionGate permission="customers.manage"><Button disabled={busy}><Save size={16} />{busy ? "保存中..." : "保存更改"}</Button></PermissionGate><Button variant="outline" type="button" disabled={busy} asChild><Link href={cancelHref}>取消更改</Link></Button></div></form>;
}

type CustomerDetailTab = "summary" | "profile" | "contacts" | "services" | "orders" | "invoices" | "tickets" | "transactions" | "files";
const customerDetailTabLabel: Record<CustomerDetailTab, string> = { summary: "摘要", profile: "个人资料", contacts: "联系人", services: "产品/服务", orders: "订单", invoices: "账单", tickets: "工单", transactions: "交易记录", files: "附件" };

function AdminCustomerRelatedView({ customerId, tab }: { customerId: number; tab: Exclude<CustomerDetailTab, "contacts" | "summary" | "profile" | "files"> }) {
  const [page, setPage] = useState(0);
  const endpoint = tab === "transactions" ? `/admin/customer-transactions?customer_id=${customerId}&limit=25&offset=${page * 25}` : `/admin/${tab}?customer_id=${customerId}&limit=25&offset=${page * 25}`;
  const { data, error, refresh } = useResource<Row[]>(endpoint, false);
  if (error) return <Failure error={error} retry={refresh} />;
  if (!data) return <Loading />;
  if (!data.length) return <section className="border-y py-12 text-center text-muted-foreground"><p>该客户暂无{customerDetailTabLabel[tab]}</p>{page > 0 && <Button className="mt-4" variant="outline" onClick={() => setPage(value => value - 1)}>上一页</Button>}</section>;
  const columns = tab === "services" ? ["服务", "域名", "状态", "下次到期"] : tab === "orders" ? ["订单号", "金额", "状态", "创建时间"] : tab === "invoices" ? ["账单号", "金额", "状态", "到期时间"] : tab === "tickets" ? ["工单号", "主题", "状态", "更新时间"] : ["类型", "关联账单", "渠道/类型", "金额", "状态", "时间"];
  return <section className="data-surface overflow-auto"><Table><TableHeader><TableRow>{columns.map(column => <TableHead key={column}>{column}</TableHead>)}</TableRow></TableHeader><TableBody>{data.map((row, index) => <TableRow key={`${String(row.kind ?? tab)}:${String(row.id ?? index)}`}>{tab === "services" && <><TableCell><Link className="text-primary hover:underline" href={`/admin/services/${Number(row.id)}`}>{String(row.name ?? "-")}</Link></TableCell><TableCell className="font-mono text-xs">{String(row.domain ?? "-")}</TableCell><TableCell><Status value={String(row.status ?? "unknown")} /></TableCell><TableCell>{row.next_due_at ? date(String(row.next_due_at)) : "-"}</TableCell></>}{tab === "orders" && <><TableCell><Link className="text-primary hover:underline" href={`/admin/orders/${Number(row.id)}`}>{String(row.order_no ?? "-")}</Link></TableCell><TableCell className="money">{money(Number(row.total_cents))}</TableCell><TableCell><Status value={String(row.status ?? "unknown")} /></TableCell><TableCell>{date(String(row.created_at))}</TableCell></>}{tab === "invoices" && <><TableCell><Link className="text-primary hover:underline" href={`/admin/invoices/${Number(row.id)}`}>{String(row.invoice_no ?? "-")}</Link></TableCell><TableCell className="money">{money(Number(row.amount_cents))}</TableCell><TableCell><Status value={String(row.status ?? "unknown")} /></TableCell><TableCell>{row.due_at ? date(String(row.due_at)) : "-"}</TableCell></>}{tab === "tickets" && <><TableCell><Link className="text-primary hover:underline" href={`/admin/tickets/${Number(row.id)}`}>{String(row.ticket_no ?? "-")}</Link></TableCell><TableCell className="max-w-sm break-words">{String(row.subject ?? "-")}</TableCell><TableCell><Status value={String(row.status ?? "unknown")} /></TableCell><TableCell>{date(String(row.updated_at))}</TableCell></>}{tab === "transactions" && <><TableCell>{String(row.kind) === "payment" ? "支付" : "余额流水"}</TableCell><TableCell>{row.invoice_id ? <Link className="text-primary hover:underline" href={`/admin/invoices/${Number(row.invoice_id)}`}>{String(row.invoice_no ?? "-")}</Link> : "-"}</TableCell><TableCell>{String(row.label ?? "-")}</TableCell><TableCell className="money">{row.kind === "ledger" ? `${Number(row.delta_cents) > 0 ? "+" : ""}${money(Number(row.delta_cents))}` : money(Number(row.amount_cents))}</TableCell><TableCell><Status value={String(row.status ?? "unknown")} /></TableCell><TableCell>{date(String(row.created_at))}</TableCell></>}</TableRow>)}</TableBody></Table><div className="flex items-center justify-end gap-3 border-t p-3"><span className="text-xs text-muted-foreground">第 {page + 1} 页</span><Button variant="outline" size="sm" disabled={page === 0} onClick={() => setPage(value => value - 1)}>上一页</Button><Button variant="outline" size="sm" disabled={data.length < 25} onClick={() => setPage(value => value + 1)}>下一页</Button></div></section>;
}

export function ContactsPanel({ adminCustomerId }: { adminCustomerId?: number }) {
  const root = adminCustomerId ? `/admin/customers/${adminCustomerId}/contacts` : "/contacts";
  const { data, error, refresh } = useResource<Contact[]>(root);
  const [editing, setEditing] = useState<number | null>(null);
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [companyName, setCompanyName] = useState("");
  const [phone, setPhone] = useState("");
  const [status, setStatus] = useState<"active" | "inactive" | "disabled">("inactive");
  const [invoiceEmails, setInvoiceEmails] = useState(false);
  const [supportEmails, setSupportEmails] = useState(false);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  function reset() {
    setEditing(null); setUsername(""); setEmail(""); setCompanyName(""); setPhone(""); setStatus("inactive"); setInvoiceEmails(false); setSupportEmails(false);
  }
  function edit(contact: Contact) {
    setEditing(contact.id); setUsername(contact.username); setEmail(contact.email); setCompanyName(contact.company_name); setPhone(contact.phone_number); setStatus(contact.status); setInvoiceEmails(contact.invoice_emails); setSupportEmails(contact.support_emails); setMessage("");
  }
  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault(); setBusy(true); setMessage("");
    try {
      const body = JSON.stringify({ username, email, company_name: companyName, phone_number: phone, status, invoice_emails: invoiceEmails, support_emails: supportEmails });
      await api(editing ? `${root}/${editing}` : root, { method: editing ? "PATCH" : "POST", body });
      reset(); refresh();
    } catch (e) { setMessage((e as Error).message); } finally { setBusy(false); }
  }
  async function remove(id: number) {
    setBusy(true); setMessage("");
    try { await api(`${root}/${id}`, { method: "DELETE", body: "{}" }); if (editing === id) reset(); refresh(); }
    catch (e) { setMessage((e as Error).message); } finally { setBusy(false); }
  }
  return <section className="data-surface p-6 mb-6">
    <div className="surface-head"><div><h2 className="font-semibold">联系人</h2><p className="text-xs text-muted-foreground mt-1">维护账单和工单通知联系人。联系人邮箱不能与账户重复。</p></div></div>
    <PermissionGate permission="customers.manage" bypass={!adminCustomerId}><form className="grid md:grid-cols-4 gap-3 mt-4" onSubmit={submit}>
      <Input value={username} onChange={(e) => setUsername(e.target.value)} placeholder="姓名" aria-label="联系人姓名" maxLength={120} />
      <Input value={email} onChange={(e) => setEmail(e.target.value)} type="email" placeholder="邮箱" aria-label="联系人邮箱" required />
      <Input value={companyName} onChange={(e) => setCompanyName(e.target.value)} placeholder="公司" aria-label="联系人公司" />
      <Input value={phone} onChange={(e) => setPhone(e.target.value)} placeholder="电话" aria-label="联系人电话" />
      <Select value={status} onValueChange={(v) => setStatus(v as typeof status)}><SelectTrigger aria-label="联系人状态"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="active">启用</SelectItem><SelectItem value="inactive">未激活</SelectItem><SelectItem value="disabled">停用</SelectItem></SelectContent></Select>
      <label className="flex items-center gap-2 text-sm"><Checkbox checked={invoiceEmails} onCheckedChange={(v) => setInvoiceEmails(v === true)} />接收账单通知</label>
      <label className="flex items-center gap-2 text-sm"><Checkbox checked={supportEmails} onCheckedChange={(v) => setSupportEmails(v === true)} />接收工单通知</label>
      <div className="flex items-center gap-2"><Button disabled={busy || !email.trim()}>{editing ? "保存联系人" : "添加联系人"}</Button>{editing && <Button type="button" variant="ghost" onClick={reset}>取消</Button>}</div>
    </form></PermissionGate>
    {message && <p role="alert" className="text-sm text-destructive mt-3">{message}</p>}
    {error ? <p role="alert" className="text-sm text-destructive mt-4">{error.message}</p> : !data ? <Loading /> : <div className="overflow-auto mt-5"><Table><TableHeader><TableRow><TableHead>姓名</TableHead><TableHead>邮箱</TableHead><TableHead>电话</TableHead><TableHead>状态</TableHead><TableHead>通知</TableHead><TableHead /></TableRow></TableHeader><TableBody>{data.length === 0 ? <TableRow><TableCell colSpan={6} className="h-20 text-center text-muted-foreground">暂无联系人</TableCell></TableRow> : data.map((contact) => <TableRow key={contact.id}><TableCell>{contact.username || "未命名"}</TableCell><TableCell>{contact.email}</TableCell><TableCell>{contact.phone_number || "-"}</TableCell><TableCell><Status value={contact.status} /></TableCell><TableCell className="text-xs text-muted-foreground">{[contact.invoice_emails && "账单", contact.support_emails && "工单"].filter(Boolean).join("、") || "无"}</TableCell><TableCell><PermissionGate permission="customers.manage" bypass={!adminCustomerId}><div className="flex items-center gap-2"><Button size="sm" variant="outline" onClick={() => edit(contact)}>编辑</Button><Button size="icon-sm" variant="ghost" aria-label="删除联系人" title="删除联系人" disabled={busy} onClick={() => void remove(contact.id)}><Trash2 size={15} /></Button></div></PermissionGate></TableCell></TableRow>)}</TableBody></Table></div>}
  </section>;
}
