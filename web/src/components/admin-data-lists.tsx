"use client";
import { useEffect, useState } from "react";
import Link from "next/link";
import { Plus, Search, ArrowUpRight, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "@/components/ui/dialog";
import { Checkbox } from "@/components/ui/checkbox";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { ResourceTable } from "./resource-table";
import { Table, TableHeader, TableHead, TableBody, TableRow, TableCell } from "@/components/ui/table";
import { api, date, type CustomerGroup, type TicketDepartment, type User } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Failure, Loading, Status } from "./common";
import { Workspace, PermissionGate, useAdminPermission } from "./workspace";
import { PageHeading, SearchForm } from "./resource-page";

export function CustomerGroupsView() {
  const { data, error, refresh } = useResource<CustomerGroup[]>("/admin/customer-groups?options=true");
  const [name, setName] = useState("");
  const [color, setColor] = useState("#64748b");
  const [discount, setDiscount] = useState("0");
  const [separateInvoices, setSeparateInvoices] = useState(false);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  async function create(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault(); setBusy(true); setMessage("");
    try { await api("/admin/customer-groups", { method: "POST", headers: { "Idempotency-Key": `customer-group-create-legacy-${crypto.randomUUID()}` }, body: JSON.stringify({ name, color, discount_percent: Number(discount), separate_invoices: separateInvoices }) }); setName(""); setDiscount("0"); setSeparateInvoices(false); refresh(); }
    catch (e) { setMessage((e as Error).message); } finally { setBusy(false); }
  }
  async function remove(id: number) {
    setBusy(true); setMessage("");
    try { const group = data?.find(item => item.id === id); if (!group) throw new Error("分组已刷新，请重试"); await api(`/admin/customer-groups/${id}`, { method: "DELETE", headers: { "Idempotency-Key": `customer-group-delete-legacy-${id}-${group.version}-${crypto.randomUUID()}` }, body: JSON.stringify({ expected_version: group.version }) }); refresh(); }
    catch (e) { setMessage((e as Error).message); } finally { setBusy(false); }
  }
  return <Workspace title="客户分组" admin><PageHeading title="客户分组" description="设置客户折扣和账单属性"><Button variant="outline" asChild><Link href="/admin/customers"><ArrowUpRight size={16} />返回客户管理</Link></Button></PageHeading><PermissionGate permission="customer_groups.manage"><section className="data-surface p-6 mb-6"><form className="grid md:grid-cols-[1fr_5rem_7rem_auto_auto] gap-3 items-center" onSubmit={create}><Input value={name} onChange={(e) => setName(e.target.value)} placeholder="分组名称" aria-label="分组名称" required maxLength={120} /><Input type="color" value={color} onChange={(e) => setColor(e.target.value)} aria-label="分组颜色" className="p-1 h-9" /><Input type="number" min="0" max="100" value={discount} onChange={(e) => setDiscount(e.target.value)} placeholder="折扣 %" aria-label="折扣百分比" /><label className="flex items-center gap-2 text-sm whitespace-nowrap"><Checkbox checked={separateInvoices} onCheckedChange={(v) => setSeparateInvoices(v === true)} />拆分账单</label><Button disabled={busy || !name.trim()}><Plus size={16} />新增分组</Button></form>{message && <p role="alert" className="text-sm text-destructive mt-3">{message}</p>}</section></PermissionGate>{error ? <Failure error={error} retry={refresh} /> : !data ? <Loading /> : <div className="data-surface overflow-auto"><Table><TableHeader><TableRow><TableHead>名称</TableHead><TableHead>折扣</TableHead><TableHead>账单</TableHead><TableHead>客户数</TableHead><TableHead /></TableRow></TableHeader><TableBody>{data.length === 0 ? <TableRow><TableCell colSpan={5} className="h-28 text-center text-muted-foreground">暂无分组</TableCell></TableRow> : data.map((group) => <TableRow key={group.id}><TableCell><span className="inline-block w-3 h-3 rounded-sm mr-2" style={{ backgroundColor: group.color }} />{group.name}</TableCell><TableCell>{group.discount_percent}%</TableCell><TableCell>{group.separate_invoices ? "拆分" : "合并"}</TableCell><TableCell>{group.member_count}</TableCell><TableCell><PermissionGate permission="customer_groups.manage"><Button size="icon-sm" variant="ghost" aria-label={`删除${group.name}`} title="删除分组" disabled={busy || group.member_count > 0} onClick={() => void remove(group.id)}><Trash2 size={15} /></Button></PermissionGate></TableCell></TableRow>)}</TableBody></Table></div>}</Workspace>;
}

export type Row = Record<string, unknown>;

function readAdminListState(kind: string) {
  const params = new URLSearchParams(window.location.search);
  const page = Number.parseInt(params.get("page") || "0", 10);
  return {
    query: params.get("q") || "",
    page: Number.isSafeInteger(page) && page >= 0 ? page : 0,
    department: kind === "tickets" ? params.get("department") || "all" : "all",
  };
}

function serializeAdminListState(kind: string, query: string, page: number, department: string) {
  const params = new URLSearchParams();
  if (query) params.set("q", query);
  if (page) params.set("page", String(page));
  if (kind === "tickets" && department !== "all") params.set("department", department);
  return params.toString();
}

export function AdminListView({
  kind,
  title,
  description,
  endpoint,
  columns,
  admin=true,
  actions,
}: {
  kind: "customers" | "orders" | "invoices" | "tickets" | "audit" | "cancellations";
  title: string;
  description: string;
  endpoint: string;
  admin?: boolean;
  actions?: React.ReactNode;
  columns: {
    key: string;
    label: string;
    render?: (row: Row, refresh: () => void, returnQuery?: string) => React.ReactNode;
  }[];
}) {
  const [query, setQuery] = useState("");
  const [draft, setDraft] = useState("");
  const [page,setPage] = useState(0);
  const [department, setDepartment] = useState("all");
  const [urlReady, setUrlReady] = useState(false);
  const [searchOpen, setSearchOpen] = useState(false);
  const returnQuery = urlReady ? serializeAdminListState(kind, query, page, department) : "";
  const directory = useResource<TicketDepartment[]>(kind === "tickets" && admin ? "/admin/ticket-departments" : null);
  const currentUser = useResource<User>(kind === "tickets" && admin ? "/me" : null);
  const { data, error, refresh } = useResource<Row[]>(
    `${endpoint}?q=${encodeURIComponent(query)}&limit=25&offset=${page*25}${kind === "tickets" ? `&department_id=${department}` : ""}`,
  );
  useEffect(() => {
    const apply = () => {
      const state = readAdminListState(kind);
      setQuery(state.query);
      setDraft(state.query);
      setPage(state.page);
      setDepartment(state.department);
      setSearchOpen(Boolean(state.query) || state.department !== "all");
      setUrlReady(true);
    };
    apply();
    window.addEventListener("popstate", apply);
    return () => window.removeEventListener("popstate", apply);
  }, [kind]);
  useEffect(() => {
    if (!urlReady) return;
    const suffix = serializeAdminListState(kind, query, page, department);
    window.history.replaceState(null, "", `${window.location.pathname}${suffix ? `?${suffix}` : ""}`);
  }, [department, kind, page, query, urlReady]);
  return (
    <Workspace title={title} admin={admin}>
      <PageHeading title={title} description={description}>{actions}</PageHeading>
      {(kind === "customers" || kind === "tickets") && (
        <div className="admin-list-search mb-4 border-b">
          <button type="button" className="flex h-10 items-center gap-2 text-sm text-primary" aria-expanded={searchOpen} aria-controls={`admin-search-${kind}`} onClick={() => setSearchOpen(value => !value)}><Search size={15} />高级搜索<span className="text-xs text-muted-foreground">{searchOpen ? "收起" : "展开"}</span></button>
          <div id={`admin-search-${kind}`} hidden={!searchOpen} className="pb-1"><SearchForm
          value={draft}
          setValue={setDraft}
          submit={() => {setQuery(draft);setPage(0)}}
          placeholder={kind === "customers" ? "搜索客户姓名或邮箱" : "搜索工单标题或编号"}
        />
        {kind === "tickets" && admin && <div className="mb-4 max-w-xs"><Select value={department} onValueChange={value => { setDepartment(value); setPage(0); }}><SelectTrigger aria-label="筛选工单部门" className="w-full"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="all">全部授权部门</SelectItem>{currentUser.data?.ticket_unassigned && <SelectItem value="0">未指定部门</SelectItem>}{directory.data?.map(item => <SelectItem key={item.id} value={String(item.id)}>{item.name}{item.active === false ? "（已停用）" : ""}</SelectItem>)}</SelectContent></Select>{directory.error && <Failure error={directory.error} retry={directory.refresh} />}</div>}
          </div>
        </div>
      )}
      {error ? (
        <Failure error={error} retry={refresh} />
      ) : !data ? (
        <Loading />
      ) : (
        <div className="admin-list-surface data-surface overflow-auto">
          <ResourceTable
            items={data}
            headers={columns.map((column) => column.label)}
            rowKey={(row, index) => String(row.id ?? index)}
            className=""
            renderRow={(row) => (
              <>
                {columns.map((column) => (
                  <TableCell key={column.key}>
                    {column.render
                      ? column.render(row, refresh, returnQuery)
                      : String(row[column.key] ?? "-")}
                  </TableCell>
                ))}
              </>
            )}
          />
          <div className="flex items-center justify-end gap-4 border-t p-3"><span className="text-xs text-muted-foreground">第 {page+1} 页</span><Button variant="outline" size="sm" disabled={page===0} onClick={()=>setPage(v=>v-1)}>上一页</Button><Button variant="outline" size="sm" disabled={data.length<25} onClick={()=>setPage(v=>v+1)}>下一页</Button></div>
        </div>
      )}
    </Workspace>
  );
}

export function TicketsView({ admin = false }: { admin?: boolean }) {
  return (
    <AdminListView
      kind="tickets"
      admin={admin}
      actions={!admin&&<Button asChild><Link href="/console/tickets/new"><Plus size={16}/>提交工单</Link></Button>}
      title={admin ? "支持工单" : "我的工单"}
      description={admin ? "客户问题与服务请求" : "提交和跟踪支持请求"}
      endpoint={admin ? "/admin/tickets" : "/tickets"}
      columns={[
        { key: "id", label: "ID" },
        { key: "subject", label: "工单标题", render: (r, _refresh, returnQuery) => <div className="min-w-48 max-w-96"><Link className="block truncate text-primary hover:underline" title={String(r.subject)} href={`${admin ? "/admin" : "/console"}/tickets/${r.id}${returnQuery ? `?${returnQuery}` : ""}`}>{String(r.subject)}</Link><span className="mt-1 block font-mono text-xs text-muted-foreground">{String(r.ticket_no ?? `#${r.id}`)}</span></div> },
        ...(admin ? [
          { key: "customer_name", label: "提交人", render: (r: Row) => <div><span className="block">{String(r.customer_name ?? "-")}</span><span className="text-xs text-muted-foreground">#{String(r.user_id ?? "-")}</span></div> },
          { key: "department_name", label: "部门", render: (r: Row) => String(r.department_name ?? "未指定") },
          { key: "assignee_name", label: "处理人", render: (r: Row) => String(r.assignee_name ?? "未分派") },
        ] : []),
        {
          key: "priority",
          label: "优先级",
          render: (r) => <Status value={String(r.priority)} />,
        },
        {
          key: "created_at",
          label: "提交时间",
          render: (r) => date(String(r.created_at)),
        },
        {
          key: "updated_at",
          label: "最近更新",
          render: (r) => date(String(r.updated_at)),
        },
        {
          key: "status",
          label: "状态",
          render: (r) => <Status value={String(r.status)} />,
        },
      ]}
    />
  );
}

export { AuditView } from "./admin-audit";

export function CancellationsView() {
  return <AdminListView kind="cancellations" title="取消申请" description="审核客户提交的服务取消申请" endpoint="/admin/cancellations" columns={[{ key: "id", label: "ID" }, { key: "service_name", label: "产品 / 服务", render: (r) => <PermissionGate permission="services.view"><Link className="text-primary hover:underline" href={`/admin/services/${r.service_id}`}>{String(r.service_name)}<span className="mt-1 block text-xs text-muted-foreground">服务 #{String(r.service_id)}</span></Link></PermissionGate> }, { key: "customer_name", label: "客户", render: (r) => <span>{String(r.customer_name)}<span className="mt-1 block text-xs text-muted-foreground">#{String(r.user_id)}</span></span> }, { key: "type", label: "取消时间", render: (r) => String(r.type) === "immediate" ? "立即取消" : "账期结束" }, { key: "reason", label: "申请原因", render: (r) => <span className="block min-w-48 max-w-80 whitespace-pre-wrap break-words">{String(r.reason)}</span> }, { key: "created_at", label: "申请时间", render: (r) => date(String(r.created_at)) }, { key: "status", label: "状态", render: (r) => <Status value={String(r.status)} /> }, { key: "actions", label: "操作", render: (r, refresh) => <CancellationDecision row={r} refresh={refresh} /> }]} />;
}

function CancellationDecision({ row, refresh }: { row: Row; refresh: () => void }) {
  const allowed = useAdminPermission("services.manage");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [decision, setDecision] = useState<"approved" | "declined" | null>(null);
  const status = String(row.status);
  async function decide(decision: "approved" | "declined") {
    setBusy(true); setMessage("");
    try { await api(`/admin/cancellations/${Number(row.id)}/decision`, { method: "POST", body: JSON.stringify({ expected_version: Number(row.version), decision, note: decision === "approved" ? "审核通过" : "审核驳回" }) }); setMessage("已处理"); setDecision(null); refresh(); }
    catch (e) { setMessage((e as Error).message); } finally { setBusy(false); }
  }
  if (!allowed || status !== "pending") return <span className="text-xs text-muted-foreground">{message || "-"}</span>;
  return <div className="flex items-center gap-2"><Button size="sm" disabled={busy} onClick={() => setDecision("approved")}>通过</Button><Button size="sm" variant="outline" disabled={busy} onClick={() => setDecision("declined")}>驳回</Button>{message && <span className="text-xs text-muted-foreground">{message}</span>}<Dialog open={decision !== null} onOpenChange={open => { if (!open && !busy) setDecision(null); }}><DialogContent showCloseButton={!busy}><DialogHeader><DialogTitle>{decision === "approved" ? "通过取消申请" : "驳回取消申请"}</DialogTitle><DialogDescription>{decision === "approved" ? (row.type === "immediate" ? "通过后将按立即取消处理，服务可能中断。" : "通过后将在当前账期结束时取消服务。") : "驳回后此申请结束，服务继续保持原状态。"}</DialogDescription></DialogHeader><p className="text-sm whitespace-pre-wrap break-words">{String(row.reason)}</p>{message && <p role="alert" className="text-sm text-destructive">{message}</p>}<DialogFooter><Button variant="outline" disabled={busy} onClick={() => setDecision(null)}>返回</Button><Button variant={decision === "approved" ? "destructive" : "default"} disabled={busy} onClick={() => decision && void decide(decision)}>确认处理</Button></DialogFooter></DialogContent></Dialog></div>;
}
