"use client";
import { OrderRefundImpact, OrderRefundOrderStatus, orderRefundHold, orderRefundBlocksDelivery } from "./external-refund-order";
import { RenewalBenefitDetails } from "./renewal-benefit";
import { useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import { PurchaseFieldDetails } from "./purchase-fields";
import type { PurchaseFieldSnapshot } from "@/lib/api";
import Link from "next/link";
import { Plus, ArrowUpRight, Trash2, Ban } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Checkbox } from "@/components/ui/checkbox";
import { Table, TableHeader, TableHead, TableBody, TableRow, TableCell } from "@/components/ui/table";
import { api, date, money } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Failure, Loading, Status } from "./common";
import { Workspace, PermissionGate, PermissionFields, useAdminPermission } from "./workspace";
import { catalogSelectStyle } from "./catalog-admin";
import { AdminStatusTabs } from "./admin-status-tabs";
import { useAdminListNavigation, useAdminListReturnHref } from "@/hooks/use-admin-list-navigation";
import { AdminPagination } from "./admin-pagination";
import { PageHeading, SearchForm } from "./resource-page";
import { Row, AdminListView } from "./admin-data-lists";

const adminOrderStates = ["review_pending", "pending", "paid", "active", "cancelled", "refunded"] as const;

const adminRenewalStates = ["pending", "paid", "cancelled", "refunded"] as const;

const orderListDefaults = { limit: "25", customer_id: "", date_from: "", date_to: "", payment_status: "" } as const;

const supplierListDefaults = {...orderListDefaults,supplier_id:"",source:"supplier"} as const;

const renewalListDefaults = { kind: "renew", ...orderListDefaults } as const;

type OrderCommissionPreview = { order_id: number; sales_owner_name: string; invoice_status: string; basis_cents: number; purchase_rate_bps: number; tier_extra_rate_bps: number; monthly_turnover_cents: number; commission_cents: number; status: string };

const commissionStatusLabels: Record<string, string> = { calculated: "按当前规则估算", not_invoiced: "未出账单", not_paid: "未付款", unassigned: "未分配销售", sales_inactive: "销售已停用", commission_group_unavailable: "无有效提成组" };

export function OrdersView({ admin = false }: { admin?: boolean }) {
  const searchParams = useSearchParams();
  if (admin) return searchParams.get("kind") === "renew" ? <AdminRenewalOrdersView /> : <AdminOrdersView />;
  const path = admin ? "/admin/orders" : "/orders";
  return (
    <AdminListView
      kind="orders"
      admin={admin}
      title={admin ? "订单管理" : "我的订单"}
      description={admin ? "订单状态与客户交易" : "查看订单和账单状态"}
      endpoint={path}
      columns={[
        { key: "order_no", label: "订单号" },
        {
          key: "customer_name",
          label: "客户",
        },
        {
          key: "total_cents",
          label: "金额",
          render: (r) => (
            <span className="money">{money(Number(r.total_cents))}</span>
          ),
        },
        {
          key: "created_at",
          label: "创建时间",
          render: (r) => date(String(r.created_at)),
        },
        {
          key: "status",
          label: "状态",
          render: (r) => <Status value={String(r.status)} />,
        },
        { key: "actions", label: "操作", render: (r) => <OrderCancelAction row={r} admin={admin} /> },
      ]}
    />
  );
}

function AdminRenewalOrdersView() {
  const { query, setQuery, draft, setDraft, status, setStatus, page, setPage, ready, suffix, extra, setExtra } = useAdminListNavigation(true, adminRenewalStates, "all", renewalListDefaults);
  const pageSize = [25, 50, 100].includes(Number(extra.limit)) ? Number(extra.limit) : 25;
  const params = new URLSearchParams({ q: query, limit: String(pageSize), offset: String(page * pageSize) });
  if (/^[1-9]\d*$/.test(extra.customer_id ?? "")) params.set("customer_id", extra.customer_id);
  if (status !== "all") params.set("status", status);
  const resource = useResource<{ items: Row[]; total: number; limit: number; offset: number }>(ready ? `/admin/renewal-orders?${params.toString()}` : null, false);
  const data = resource.data;
  return (
    <Workspace title="续费订单" admin>
      <PageHeading title="续费订单" description="查看客户服务续费账单和付款状态"><Button variant="outline" asChild><Link href="/admin/orders">产品订单</Link></Button></PageHeading>
      <AdminStatusTabs label="续费订单状态" value={status} states={{ review_pending: "待核验", pending: "待支付", paid: "已支付", cancelled: "已取消", refunded: "已退款" }} onChange={value => { setStatus(value); setPage(0); }} />
      <div className="flex flex-wrap items-center gap-3 mb-6">
        <SearchForm value={draft} setValue={setDraft} submit={() => { setQuery(draft); setPage(0); }} placeholder="账单号、客户、产品或邮箱" />
      </div>
      {resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !data ? <Loading /> : (
        <div className="admin-list-surface data-surface overflow-auto">
          <Table>
            <TableHeader><TableRow><TableHead>账单号</TableHead><TableHead>客户</TableHead><TableHead>产品</TableHead><TableHead>IP/服务域名</TableHead><TableHead>下单时间</TableHead><TableHead>金额</TableHead><TableHead>付款方式</TableHead><TableHead>账单状态</TableHead></TableRow></TableHeader>
            <TableBody>
              {data.items.length === 0 && <TableRow><TableCell colSpan={8} className="h-32 text-center text-muted-foreground">暂无续费订单</TableCell></TableRow>}
              {data.items.map((row, index) => {
                const id = Number(row.id ?? index);
                const invoiceID = Number(row.invoice_id);
                const serviceDomain = String(row.service_domain ?? "-");
                return <TableRow key={String(row.id ?? index)}>
                  <TableCell className="font-mono text-xs">{invoiceID > 0 ? <Link className="text-primary hover:underline" href={`/admin/invoices/${invoiceID}${suffix}`}>{String(row.order_no ?? id)}</Link> : String(row.order_no ?? id)}</TableCell>
                  <TableCell><span className="font-medium">{String(row.customer_name ?? "-")}</span></TableCell>
                  <TableCell><div className="max-w-80 whitespace-normal break-words">{String(row.product_name ?? "-")}</div></TableCell>
                  <TableCell className="font-mono text-xs"><div className="max-w-48 whitespace-normal break-all">{serviceDomain}</div></TableCell>
                  <TableCell>{date(String(row.created_at))}</TableCell>
                  <TableCell><span className="money">{money(Number(row.total_cents ?? 0))}</span></TableCell>
                  <TableCell>{String(row.payment_method || row.provider || "-")}</TableCell>
                  <TableCell><Status value={String(row.status ?? "pending")} /></TableCell>
                </TableRow>;
              })}
            </TableBody>
          </Table>
          <AdminPagination total={Number(data.total ?? 0)} page={Math.floor(Number(data.offset ?? 0) / Math.max(1, Number(data.limit ?? 25)))} pageSize={Number(data.limit ?? 25)} onPageChange={setPage} onPageSizeChange={size => { setExtra({ ...extra, limit: String(size) }); setPage(0); }} />
        </div>
      )}
    </Workspace>
  );
}

export function SupplierOrdersView() {return <AdminOrdersView supplier />;}

function AdminOrdersView({supplier=false}:{supplier?:boolean}={}) {
  const { query, setQuery, draft, setDraft, status, setStatus, page, setPage, ready, suffix, extra, setExtra } = useAdminListNavigation(true, adminOrderStates, "all", supplier ? supplierListDefaults : orderListDefaults);
  const canViewSales = useAdminPermission("sales.view");
  const [selected, setSelected] = useState<number[]>([]);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [commissionRequest, setCommissionRequest] = useState<{ key: string; items: Record<number, OrderCommissionPreview>; error: string } | null>(null);
  const [advanced,setAdvanced] = useState(false);
  const pageSize = [25, 50, 100].includes(Number(extra.limit)) ? Number(extra.limit) : 25;
  const params = new URLSearchParams({ q: query, limit: String(pageSize), offset: String(page * pageSize) });
  if (/^[1-9]\d*$/.test(extra.customer_id ?? "")) params.set("customer_id", extra.customer_id);
  if (status !== "all") params.set("status", status);
  for(const key of ["date_from","date_to","payment_status","supplier_id"]) if(extra[key])params.set(key,extra[key]);
  const { data: result, error, refresh } = useResource<{ items: Row[]; total: number; limit: number; offset: number }>(ready ? `${supplier ? "/admin/supplier-orders" : "/admin/orders/page"}?${params.toString()}` : null, false);
  const data = result?.items;
  const orderIDsKey = (data ?? []).map(row => Number(row.id)).join(",");
  useEffect(() => {
    let current = true;
    if (!canViewSales || !orderIDsKey) return () => { current = false; };
    const orderIDs = orderIDsKey.split(",").map(Number);
    void api<{ items: OrderCommissionPreview[]; total_commission_cents: number }>("/admin/orders/commission", { method: "POST", body: JSON.stringify({ order_ids: orderIDs }) })
      .then(result => {
        if (!current) return;
        setCommissionRequest({ key: orderIDsKey, items: Object.fromEntries(result.items.map(item => [item.order_id, item])), error: "" });
      })
      .catch(error => { if (current) setCommissionRequest({ key: orderIDsKey, items: {}, error: error instanceof Error ? error.message : "提成预估读取失败" }); });
    return () => { current = false; };
  }, [canViewSales, orderIDsKey]);
  const commissionMatches = commissionRequest?.key === orderIDsKey;
  const commissionByOrder = commissionMatches ? commissionRequest.items : {};
  const commissionLoading = canViewSales && Boolean(orderIDsKey) && !commissionMatches;
  const commissionError = commissionMatches ? commissionRequest.error : "";
  const commissionTotal = Object.values(commissionByOrder).reduce((sum, item) => sum + Number(item.commission_cents || 0), 0);
  const selectable = (data ?? []).map((row) => Number(row.id));
  const visibleSelected = selected.filter(id => selectable.includes(id));
  const allSelected = selectable.length > 0 && selectable.every(id => selected.includes(id));
  const selectedMutationBlocked = (data ?? []).some(row => visibleSelected.includes(Number(row.id)) && orderRefundBlocksDelivery(row.external_refund_order_hold));
  const selectedReviewBlocked = (data ?? []).some(row => visibleSelected.includes(Number(row.id)) && (row.status === "refunded" || orderRefundBlocksDelivery(row.external_refund_order_hold)));
  function toggle(id: number, checked: boolean) { setSelected(current => checked ? [...new Set([...current,id])] : current.filter(value => value !== id)); }
  async function bulkAction(action: "cancel" | "review" | "delete") {
    if (!visibleSelected.length) return;
    if (action === "review" && selectedReviewBlocked) { setMessage("所选订单包含外部退款冻结或已退款订单，不能审核通过。请先核实退款事实。"); return; }
    if (action !== "review" && selectedMutationBlocked) { setMessage("所选订单包含退款冻结、已冲正或事实不完整的订单，禁止取消或删除；请保留原历史。"); return; }
    let reason = "";
    if (action === "delete") { reason = window.prompt("填写删除原因（至少3字）。待付款订单会取消并释放库存，账单与审计记录保留。")?.trim() ?? ""; if (reason.length < 3) return; }
    if (!window.confirm(`确认${action === "cancel" ? "取消" : action === "review" ? "核验通过" : "删除"}所选 ${visibleSelected.length} 个订单？不可处理的订单会逐项返回原因。`)) return;
    setBusy(true); setMessage("");
    try {
      const versions = Object.fromEntries((data ?? []).filter(row => visibleSelected.includes(Number(row.id))).map(row => [String(row.id),Number(row.version)]));
      const result = await api<{ succeeded: number; failed: number; items: { order_id: number; error?: string }[] }>(`/admin/orders/bulk-${action}`, { method: "POST", body: JSON.stringify({ order_ids: visibleSelected, expected_versions: versions, ...(action === "review" ? {decision:"approved",note:"管理员批量核验通过"} : action === "delete" ? {reason} : {}) }), headers: { "Idempotency-Key": `web-order-bulk-${action}-${crypto.randomUUID()}` } });
      setMessage(`成功 ${result.succeeded} 个，失败 ${result.failed} 个${result.failed ? "：" + result.items.filter(item => item.error).map(item => `#${item.order_id} ${item.error}`).join("；") : ""}`);
      setSelected([]); refresh();
    } catch (e) { setMessage((e as Error).message); }
    finally { setBusy(false); }
  }

  return (
    <Workspace title={supplier ? "供应商订单" : "订单管理"} admin>
      <PageHeading title={supplier ? "供应商订单" : "订单管理"} description={supplier ? "按交付时供应商归属查询订单；金额为客户完整订单金额" : "查询订单、核验交付与管理订单历史"}><div className="flex flex-wrap gap-2"><PermissionGate permission="orders.manage"><Button asChild><Link href={`/admin/orders/new${suffix}`}><Plus size={16} />添加新订单</Link></Button></PermissionGate><Button variant="outline" asChild><Link href={supplier ? "/admin/suppliers/renewals" : "/admin/orders?kind=renew"}>续费订单</Link></Button>{!supplier && canViewSales && <Button variant="outline" asChild><Link href={`/admin/sales-settlements${visibleSelected.length ? `?order_ids=${visibleSelected.join(",")}` : ""}`}>销售结算{visibleSelected.length ? ` (${visibleSelected.length})` : ""}</Link></Button>}</div></PageHeading>
      <AdminStatusTabs label="订单状态" value={status} states={{ review_pending: "待核验", pending: "待支付", paid: "已支付", active: "已启用", cancelled: "已取消", refunded: "已退款" }} onChange={value => { setStatus(value); setPage(0); }} />
      <div className="flex flex-wrap items-center gap-3 mb-6">
        <SearchForm value={draft} setValue={setDraft} submit={() => { setQuery(draft); setPage(0); }} placeholder="订单号、客户名称或邮箱" />
        <Button variant="outline" onClick={() => setAdvanced(value => !value)} aria-expanded={advanced}>高级搜索</Button>{message && <span className="text-sm text-muted-foreground break-words" role="status">{message}</span>}{canViewSales && data?.length && !commissionLoading && !commissionError && commissionMatches && <span className="text-sm text-muted-foreground">当前页提成预估合计：<strong className="money">{money(commissionTotal)}</strong></span>}{commissionError && canViewSales && <span className="text-sm text-destructive" role="status">提成预估暂不可用：{commissionError}</span>}
      </div>
      {advanced&&<section className="data-surface p-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-4 mb-5">{supplier&&<label className="text-sm">供应商ID<Input type="number" min={1} aria-label="筛选供应商ID" value={extra.supplier_id} onChange={e=>{setExtra({...extra,supplier_id:e.target.value});setPage(0);}}/></label>}<label className="text-sm min-w-0">客户ID<Input aria-label="筛选客户ID" type="number" min={1} value={extra.customer_id} onChange={e=>{setExtra({...extra,customer_id:e.target.value});setPage(0);}}/></label><label className="text-sm min-w-0">开始日期<Input aria-label="订单开始日期" type="date" value={extra.date_from} onChange={e=>{setExtra({...extra,date_from:e.target.value});setPage(0);}}/></label><label className="text-sm min-w-0">结束日期<Input aria-label="订单结束日期" type="date" value={extra.date_to} onChange={e=>{setExtra({...extra,date_to:e.target.value});setPage(0);}}/></label><label className="text-sm min-w-0">付款状态<select aria-label="订单付款状态" className={catalogSelectStyle} value={extra.payment_status} onChange={e=>{setExtra({...extra,payment_status:e.target.value});setPage(0);}}><option value="">全部</option><option value="deferred">未生成账单</option><option value="unpaid">待付款</option><option value="paid">已支付</option><option value="cancelled">已取消</option><option value="refunded">已退款</option></select></label><Button variant="outline" onClick={()=>{setExtra(supplier?{...supplierListDefaults}:{...orderListDefaults});setPage(0);}}>清除高级筛选</Button></section>}
      {error ? <Failure error={error} retry={refresh} /> : !data ? <Loading /> : (
        <div className="admin-list-surface data-surface overflow-auto">
          <Table>
            <TableHeader><TableRow><TableHead className="w-12"><PermissionGate permission="orders.manage"><Checkbox checked={allSelected} onCheckedChange={(checked) => setSelected(checked ? selectable : [])} aria-label="选择当前页订单" /></PermissionGate></TableHead><TableHead>订单号</TableHead><TableHead>客户</TableHead><TableHead>产品 / 服务域名</TableHead><TableHead>IP 地址</TableHead><TableHead>金额 / 付款</TableHead><TableHead>创建时间</TableHead><TableHead>订单状态</TableHead><TableHead>审核</TableHead><TableHead>客户资料备注</TableHead><TableHead>订单备注</TableHead><TableHead>{canViewSales ? "提成 / 销售" : "客户销售"}</TableHead>{supplier&&<TableHead>供应商 / 成本快照</TableHead>}<TableHead>操作</TableHead></TableRow></TableHeader>
            <TableBody>
              {data.length === 0 && <TableRow><TableCell colSpan={supplier?14:13} className="h-32 text-center text-muted-foreground">暂无记录</TableCell></TableRow>}
              {data.map((row, i) => { const id = Number(row.id); const commission = commissionByOrder[id]; return <TableRow key={String(row.id ?? i)}><TableCell><PermissionGate permission="orders.manage"><Checkbox checked={selected.includes(id)} onCheckedChange={(checked) => toggle(id, checked === true)} aria-label={`选择订单 ${String(row.order_no ?? id)}`} /></PermissionGate></TableCell><TableCell className="font-mono text-xs"><Link className="text-primary hover:underline" href={`/admin/orders/${id}${suffix}`}>{String(row.order_no ?? "-")}</Link></TableCell><TableCell>{String(row.customer_name ?? "-")}</TableCell><TableCell><div className="max-w-64 whitespace-normal break-words">{String(row.product_names || "-")}<span className="block text-xs text-muted-foreground">{String(row.service_domains || "-")}</span></div></TableCell><TableCell className="font-mono text-xs">{String(row.service_ips || "-")}</TableCell><TableCell><span className="money">{money(Number(row.total_cents))}</span><span className="block text-xs text-muted-foreground">{row.invoice_id == null ? "未生成账单" : <Status value={String(row.invoice_status)} />} · {String(row.payment_method || "-")}</span></TableCell><TableCell>{date(String(row.created_at))}</TableCell><TableCell><OrderRefundOrderStatus row={row} /></TableCell><TableCell><OrderRefundOrderStatus row={row} review /></TableCell><TableCell className="max-w-56 whitespace-normal break-words">{String(row.customer_note || "-")}</TableCell><TableCell className="max-w-56 whitespace-normal break-words">{String(row.notes || "-")}</TableCell><TableCell><span>{String(row.sales_owner_name || "-")}</span>{canViewSales && <span className="mt-1 block text-xs text-muted-foreground">{commissionLoading ? "计算中…" : commission ? <><strong className="money">{money(Number(commission.commission_cents))}</strong><span className="ml-1">{commissionStatusLabels[commission.status] ?? commission.status}</span></> : commissionError ? "预估读取失败" : "—"}</span>}</TableCell>{supplier&&<TableCell>{(Array.isArray(row.supplier_resources)?row.supplier_resources:[]).map((resource:Record<string,unknown>)=><p key={String(resource.service_id)} className="text-xs">{String(resource.supplier_name||`供应商 #${resource.supplier_id}`)} · 服务 #{String(resource.service_id)} · {resource.cost_cents==null?"成本未记录":`${money(Number(resource.cost_cents))} CNY`}</p>)}</TableCell>}<TableCell><div className="flex flex-wrap items-center gap-2"><OrderReviewAction row={row} refresh={refresh} /><OrderCancelAction row={row} admin refresh={refresh} /><OrderDeleteAction row={row} refresh={refresh} /></div></TableCell></TableRow>; })}
            </TableBody>
          </Table>
          <div className="flex flex-wrap items-center justify-between gap-3 border-t p-3"><div className="flex flex-wrap gap-2"><PermissionGate permission="orders.manage"><Button variant="destructive" size="sm" disabled={busy || visibleSelected.length === 0 || selectedMutationBlocked} title={selectedMutationBlocked ? "退款冻结或冲正订单禁止取消" : undefined} onClick={() => void bulkAction("cancel")}><Ban size={15} />批量取消 {visibleSelected.length ? `(${visibleSelected.length})` : ""}</Button><Button size="sm" disabled={busy || !visibleSelected.length || selectedReviewBlocked} title={selectedReviewBlocked ? "所选订单包含外部退款冻结或已退款订单" : undefined} onClick={() => void bulkAction("review")}>核验通过</Button><Button variant="destructive" size="sm" disabled={busy || !visibleSelected.length || selectedMutationBlocked} title={selectedMutationBlocked ? "退款冻结或冲正订单禁止删除" : undefined} onClick={() => void bulkAction("delete")}>删除订单</Button></PermissionGate></div><div className="min-w-0 basis-72 flex-1"><AdminPagination total={result?.total ?? 0} page={page} pageSize={pageSize} onPageChange={setPage} onPageSizeChange={size => { setExtra({ ...extra, limit: String(size) }); setPage(0); }} /></div></div>
        </div>
      )}
    </Workspace>
  );
}

function OrderCancelAction({ row, admin, refresh }: { row: Row; admin: boolean; refresh?: () => void }) {
  const allowed = useAdminPermission("orders.manage");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  if ((admin && !allowed) || String(row.status) !== "pending") return <span className="text-xs text-muted-foreground">-</span>;
  async function cancel() {
    if (!window.confirm("确认取消此订单？取消后会释放预留库存。")) return;
    setBusy(true); setMessage("");
    try {
      await api(`${admin ? "/admin" : ""}/orders/${Number(row.id)}/cancel`, { method: "POST", body: JSON.stringify(admin ? {expected_version: Number(row.version)} : {}), headers: { "Idempotency-Key": `web-order-cancel-${row.id}-${crypto.randomUUID()}` } });
      setMessage("已取消"); refresh?.();
    } catch (e) { setMessage((e as Error).message); } finally { setBusy(false); }
  }
  return <div className="flex items-center gap-2"><Button size="sm" variant="destructive" disabled={busy} onClick={cancel}><Ban size={14} />取消订单</Button>{message && <span className="text-xs text-muted-foreground whitespace-nowrap">{message}</span>}</div>;
}

function OrderReviewAction({ row, refresh }: { row: Row; refresh: () => void }) {
  const allowed = useAdminPermission("orders.manage");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  if (!allowed || String(row.review_status) !== "pending" || String(row.invoice_status) !== "paid" || row.status === "refunded") return null;
  const refundBlocked = orderRefundBlocksDelivery(row.external_refund_order_hold);
  async function decide(decision: "approved" | "rejected") {
    if (refundBlocked) return;
    if (!window.confirm(decision === "approved" ? "确认审核通过此订单并继续交付？" : "确认驳回此订单？")) return;
    setBusy(true); setMessage("");
    try {
      await api(`/admin/orders/${Number(row.id)}/review`, { method: "POST", body: JSON.stringify({ expected_version: Number(row.version), decision, note: decision === "approved" ? "管理员审核通过" : "管理员审核驳回" }), headers: { "Idempotency-Key": `web-order-review-${row.id}-${decision}-${crypto.randomUUID()}` } });
      setMessage(decision === "approved" ? "已通过" : "已驳回"); refresh();
    } catch (e) { setMessage((e as Error).message); } finally { setBusy(false); }
  }
  return <div className="flex flex-wrap items-center gap-2"><Button size="sm" disabled={busy || refundBlocked} onClick={() => void decide("approved")}>通过</Button><Button size="sm" variant="outline" disabled={busy || refundBlocked} onClick={() => void decide("rejected")}>驳回</Button>{refundBlocked && <span className="text-xs whitespace-normal">外部退款冻结中，请先完成独立复核；不能审核或交付。</span>}{message && <span className="text-xs text-muted-foreground whitespace-nowrap">{message}</span>}</div>;
}

function OrderDeleteAction({row,refresh}:{row:Row;refresh:()=>void}) {
 const allowed = useAdminPermission("orders.manage");
 const [busy,setBusy] = useState(false); const [message,setMessage] = useState("");
 if (!allowed) return null;
 if (orderRefundBlocksDelivery(row.external_refund_order_hold)) return <div className="max-w-64 space-y-1"><Button size="sm" variant="destructive" disabled><Trash2 size={14}/>删除</Button><p className="text-xs whitespace-normal">外部退款冻结、已冲正或事实不完整，禁止删除；原历史须保留。</p></div>;
 if (!["pending","cancelled","refunded"].includes(String(row.status))) return null;
 async function remove() {
  const reason = window.prompt("填写删除原因（3至500字）。待付款订单会取消，财务与资源历史保留。")?.trim();
  if (!reason || reason.length < 3 || !window.confirm("确认删除此订单？订单将从常规列表移除。")) return;
  setBusy(true);setMessage("");
  try { await api(`/admin/orders/${row.id}`, {method:"DELETE",body:JSON.stringify({expected_version:Number(row.version),reason}),headers:{"Idempotency-Key":`web-order-delete-${row.id}-${crypto.randomUUID()}`}});refresh(); }
  catch(e){setMessage((e as Error).message);} finally{setBusy(false);}
 }
 return <div className="flex flex-wrap gap-2"><Button size="sm" variant="destructive" disabled={busy} onClick={() => void remove()}><Trash2 size={14}/>删除</Button>{message&&<span role="status" className="text-xs break-words">{message}</span>}</div>;
}

function DeferredOrderInvoice({row,refresh}:{row:Row;refresh:()=>void}) {
 const allowed = useAdminPermission("orders.manage");const [busy,setBusy]=useState(false);const [message,setMessage]=useState("");
 if (!allowed || row.invoice_id != null || row.status !== "pending") return null;
 async function generate(){if(!window.confirm("确认按订单已保存的金额生成待付款账单？"))return;setBusy(true);try{await api(`/admin/orders/${row.id}/invoice`,{method:"POST",body:JSON.stringify({expected_version:Number(row.version)}),headers:{"Idempotency-Key":`web-order-invoice-${row.id}-${crypto.randomUUID()}`}});refresh();}catch(e){setMessage((e as Error).message);}finally{setBusy(false);}}
 return <div className="flex flex-wrap gap-2"><Button disabled={busy} onClick={() => void generate()}>生成账单</Button>{message&&<span role="status" className="text-xs break-words">{message}</span>}</div>;
}

function OrderConfigurationSnapshot({item}:{item:Row}){
 const snapshot=Array.isArray(item.config_snapshot)?item.config_snapshot as Row[]:[];
 return <div className="text-xs text-muted-foreground mt-2 space-y-1">{snapshot.map(option=><p className="whitespace-normal break-all" key={String(option.id)}>{String(option.group_name)}：{String(option.label)}（{String(option.value)}）{Number(option.quantity??1)!==1?` × ${option.quantity}`:""} · 周期增减 {money(Number(option.price_delta_cents))} · 初装增减 {money(Number(option.setup_delta_cents))}</p>)}{snapshot.length===0&&Array.isArray(item.config_options)&&item.config_options.length>0&&<p>历史配置编号：{item.config_options.join("、")}；未保留名称快照。</p>}</div>;
}

export function OrderDetailView({ id }: { id: number }) {
  const supplierContext=useSearchParams().get("source")==="supplier";
 const backHref = useAdminListReturnHref(supplierContext?"/admin/suppliers/orders":"/admin/orders", adminOrderStates, "all", supplierContext?supplierListDefaults:orderListDefaults);
  const { data, error, refresh } = useResource<Row>(`/admin/orders/${id}`);
  const [note, setNote] = useState<string | null>(null);
  const [noteVersion,setNoteVersion] = useState<number | null>(null);
  const refundMutationBlocked = orderRefundBlocksDelivery(data?.external_refund_order_hold);
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState("");
  async function saveNotes(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (refundMutationBlocked) { setMessage("外部退款冻结、已冲正或事实不完整，禁止修改订单备注。"); return; }
    setSaving(true); setMessage("");
    try {
      await api(`/admin/orders/${id}/notes`, { method: "POST", body: JSON.stringify({ expected_version: noteVersion ?? Number(data?.version), notes: note ?? String(data?.notes ?? "") }), headers: {"Idempotency-Key":`web-order-notes-${id}-${crypto.randomUUID()}`} });
      setMessage("备注已保存"); setNote(null); setNoteVersion(null); refresh();
    } catch (e) { setMessage((e as Error).message); } finally { setSaving(false); }
  }
  return <Workspace title="订单详情" admin><Button variant="ghost" className="mb-4" asChild><Link href={backHref}><ArrowUpRight size={16} />返回订单列表</Link></Button>{error ? <Failure error={error} retry={refresh} /> : !data ? <Loading /> : <><PageHeading title={String(data.order_no)} description={`客户：${String(data.display_name)} · 创建于 ${date(String(data.created_at))}`}><div className="flex flex-wrap items-center gap-2"><OrderRefundOrderStatus row={data} /><OrderRefundOrderStatus row={data} review /><OrderReviewAction row={data} refresh={refresh} /><OrderCancelAction row={data} admin refresh={refresh} /><Button variant="outline" size="sm" onClick={refresh}>刷新订单</Button><OrderDeleteAction row={data} refresh={() => window.location.assign(backHref)} /></div></PageHeading>{orderRefundHold(data.external_refund_order_hold) && <div className="mb-5"><OrderRefundImpact reversal={orderRefundHold(data.external_refund_order_hold)!} /></div>}<div className="grid md:grid-cols-3 gap-3 mb-5"><section className="data-surface p-4"><p className="text-xs text-muted-foreground">订单金额</p><p className="money text-xl font-semibold mt-2">{money(Number(data.total_cents))}</p></section><section className="data-surface p-4"><p className="text-xs text-muted-foreground">账单</p><p className="mt-2">{data.invoice_id == null ? "尚未生成账单" : <>{String(data.invoice_no)} · <Status value={String(data.invoice_status)} /></>}</p></section><section className="data-surface p-4"><p className="text-xs text-muted-foreground">客户邮箱</p><p className="mt-2 break-all">{String(data.email)}</p></section></div><div className="mb-4 flex flex-wrap gap-2"><DeferredOrderInvoice row={data} refresh={refresh}/><PermissionGate permission="customers.view"><Button variant="outline" size="sm" asChild><Link href={`/admin/customers/${data.user_id}`}>客户摘要</Link></Button></PermissionGate>{data.invoice_id != null && <PermissionGate permission="invoices.view"><Button variant="outline" size="sm" asChild><Link href={`/admin/invoices/${data.invoice_id}`}>关联账单</Link></Button></PermissionGate>}</div><div className="grid xl:grid-cols-2 gap-4"><section className="data-surface p-6"><h2 className="font-semibold">订单项目</h2><div className="overflow-auto mt-4"><Table><TableHeader><TableRow><TableHead>产品</TableHead><TableHead>账期</TableHead><TableHead>数量</TableHead><TableHead>金额</TableHead></TableRow></TableHeader><TableBody>{(data.items as Row[]).map((item) => <TableRow key={String(item.id)}><TableCell>{String(item.product_name)}<OrderConfigurationSnapshot item={item}/>{item.first_price_override_cents != null && <p className="text-xs text-muted-foreground">首次内部价格：{money(Number(item.first_price_override_cents))}</p>}{item.renewal_price_override_cents != null && <p className="text-xs text-muted-foreground">续费内部价格：{money(Number(item.renewal_price_override_cents))}</p>}<RenewalBenefitDetails terms={{renewal_discount_percent:Number(item.renewal_discount_percent),renewal_fixed_discount_cents:Number(item.renewal_fixed_discount_cents),renewal_discount_limit:Number(item.renewal_discount_limit),renewal_promo_code:String(item.renewal_promo_code??"")}}/><PurchaseFieldDetails fields={Array.isArray(item.purchase_fields)?item.purchase_fields as PurchaseFieldSnapshot[]:[]}/></TableCell><TableCell>{String(item.billing_cycle)}</TableCell><TableCell>{String(item.quantity)}</TableCell><TableCell className="money">{money(Number(item.total_cents))}</TableCell></TableRow>)}</TableBody></Table></div></section><section className="data-surface p-6"><h2 className="font-semibold">服务实例</h2><div className="overflow-auto mt-4"><Table><TableHeader><TableRow><TableHead>服务</TableHead><TableHead>域名</TableHead><TableHead>状态</TableHead></TableRow></TableHeader><TableBody>{(data.services as Row[]).map((service) => <TableRow key={String(service.id)}><TableCell><PermissionGate permission="services.view"><Link className="text-primary hover:underline" href={`/admin/services/${service.id}`}>{String(service.name)}</Link></PermissionGate></TableCell><TableCell className="font-mono text-xs">{String(service.domain)}</TableCell><TableCell><Status value={String(service.status)} /></TableCell></TableRow>)}</TableBody></Table></div></section></div><section className="data-surface p-4 mt-5"><h2 className="font-semibold">建单信息</h2><dl className="grid sm:grid-cols-2 gap-3 mt-3 text-sm"><div><dt className="text-muted-foreground">付款偏好</dt><dd>{String(data.payment_provider || "由客户付款时选择")}</dd></div><div><dt className="text-muted-foreground">订单通知</dt><dd>{({not_requested:"未请求发送",queued:"已加入发送队列",skipped_unverified:"未发送：客户邮箱尚未验证",skipped_channel:"未发送：邮件渠道未启用",skipped_template:"未发送：通知模板未启用"} as Record<string,string>)[String(data.email_status)] ?? String(data.email_status || "-")}</dd></div></dl></section><section className="data-surface p-6 mt-5"><h2 className="font-semibold">管理员备注</h2><PermissionFields permission="orders.manage"><form className="mt-4 space-y-3" onSubmit={(event) => void saveNotes(event)}><Textarea disabled={saving || refundMutationBlocked} value={note ?? String(data.notes ?? "")} onChange={(event) => {setNote(event.target.value);if(noteVersion === null)setNoteVersion(Number(data.version));}} rows={5} maxLength={5000} aria-label="订单备注" /><div className="flex flex-wrap items-center gap-3"><Button disabled={saving || refundMutationBlocked}>{saving ? "保存中" : "保存备注"}</Button>{refundMutationBlocked && <p role="status" className="text-xs break-words">外部退款冻结、已冲正或事实不完整，禁止修改备注；请保留原历史。</p>}<Button type="button" variant="outline" disabled={saving} onClick={() => {setNote(null);setNoteVersion(null);setMessage("");refresh();}}>取消修改</Button>{message && <span className="text-sm text-muted-foreground" role="status">{message}</span>}</div></form></PermissionFields></section></> }</Workspace>;
}
