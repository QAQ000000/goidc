"use client";
import { AdminCustomerDetailView, ContactsPanel } from "./admin-customer-detail";
import { AccountVerification } from "./account-verification";
import { AdminCustomersView } from "./admin-customers";
export { AdminCustomerDetailView } from "./admin-customer-detail";
import { NOKVMServiceDelivery, serviceActionLabels, isPowerAction } from "./nokvm-delivery";
import { SupplierServiceDelivery } from "./supplier-delivery";
import { AdminServiceDetail } from "./admin-service-detail";
import { NOKVMMonitoringPanel } from "./nokvm-monitoring";
import { NOKVMStoragePanel } from "./nokvm-storage";
import { NOKVMNATPanel } from "./nokvm-nat";
import { NOKVMCDROMPanel } from "./nokvm-cdrom";
import { RenewalBenefitDetails } from "./renewal-benefit";
import { useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import { ServiceDownloads } from "./service-downloads";
import { ServiceConfigurationChanges } from "./configuration-upgrades";
import { ServiceActivityPanel } from "./service-activity";
import { PurchaseFieldDetails } from "./purchase-fields";
import type { PurchaseFieldSnapshot } from "@/lib/api";
import Link from "next/link";
import {
  Plus,
  Search,
  ArrowUpRight,
  Package,
  Users,
  Receipt,
  ClipboardList,
  Server,
  Pause,
  Play,
  Trash2,
  Ban,
  RotateCcw,
  CreditCard,
  KeyRound,
  Copy,
  Tag,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "@/components/ui/dialog";
import { Textarea } from "@/components/ui/textarea";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableHeader,
  TableHead,
  TableBody,
  TableRow,
  TableCell,
} from "@/components/ui/table";
import { api, date, money, type ApiKey, type CustomerGroup, type PromoCode, type Service, type TicketDepartment, type User } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Failure, Loading, Status } from "./common";
import { Workspace, PermissionGate, PermissionFields, useAdminPermission } from "./workspace";
import { MfaPanel } from "./mfa-panel";
import { ServiceNavigationSettings } from "./service-groups";
import { useServiceNavigation } from "@/hooks/use-service-navigation";
import type { ServicePage } from "@/lib/api";
import { catalogSelectStyle } from "./catalog-admin";
import { AdminDashboard } from "./admin-dashboard";
import { AdminStatusTabs } from "./admin-status-tabs";
import { useAdminListNavigation, useAdminListReturnHref } from "@/hooks/use-admin-list-navigation";
import { AdminPagination } from "./admin-pagination";

const adminOrderStates = ["review_pending", "pending", "paid", "active", "cancelled", "refunded"] as const;
const adminRenewalStates = ["pending", "paid", "cancelled", "refunded"] as const;
const orderListDefaults = { limit: "25", customer_id: "", date_from: "", date_to: "", payment_status: "" } as const;
const supplierListDefaults = {...orderListDefaults,supplier_id:"",source:"supplier"} as const;
const renewalListDefaults = { kind: "renew", ...orderListDefaults } as const;
const adminServiceStates = ["pending_payment", "queued", "provisioning", "active", "suspended", "failed", "cancelled", "terminated", "import_review_required"] as const;
type OrderCommissionPreview = { order_id: number; sales_owner_name: string; invoice_status: string; basis_cents: number; purchase_rate_bps: number; tier_extra_rate_bps: number; monthly_turnover_cents: number; commission_cents: number; status: string };
const commissionStatusLabels: Record<string, string> = { calculated: "按当前规则估算", not_invoiced: "未出账单", not_paid: "未付款", unassigned: "未分配销售", sales_inactive: "销售已停用", commission_group_unavailable: "无有效提成组" };

export function DashboardView() {
  return <AdminDashboard />;
}

type IncomeReport = { year: number; months: { month: string; income_cents: number; invoice_count: number }[]; total: { income_cents: number; invoice_count: number } };
type DailyReport = { year: number; month: number; days: { date: string; new_clients: number; new_orders: number; paid_orders: number; new_tickets: number; ticket_replies: number; cancellation_requests: number }[] };
type ProductIncomeReport = { year: number; month: number; products: { product_id: number; name: string; new_order_amount_cents: number; new_order_count: number; renewal_amount_cents: number; renewal_order_count: number; total_amount_cents: number }[] };
type CustomerRanking = { user_id: number; display_name: string; income_cents: number; expense_cents: number; net_cents: number }[];
type SalesReport = { year: number; items: { sales_owner_id: number; sales_owner_name: string; order_count: number; income_cents: number }[] };
type TicketReport = { year: number; month: number; items: { date: string; ticket_count: number; open_count: number }[] };

export function ReportsView() {
  const [reportTab, setReportTab] = useState("income");
  const now = new Date(new Date().toLocaleString("en-US", { timeZone: "Asia/Shanghai" }));
  const [year, setYear] = useState(String(now.getFullYear()));
  const [month, setMonth] = useState(String(now.getMonth() + 1));
  const [period, setPeriod] = useState({ year: now.getFullYear(), month: now.getMonth() + 1 });
  const [reportReady, setReportReady] = useState(false);
  useEffect(() => {
    const restore = () => {
      const params = new URLSearchParams(window.location.search);
      const current = new Date(new Date().toLocaleString("en-US", { timeZone: "Asia/Shanghai" }));
      const requestedYear = Number(params.get("year") ?? current.getFullYear());
      const requestedMonth = Number(params.get("month") ?? current.getMonth() + 1);
      const restoredYear = Number.isInteger(requestedYear) && requestedYear >= 2000 && requestedYear <= current.getFullYear() + 1 ? requestedYear : current.getFullYear();
      const restoredMonth = Number.isInteger(requestedMonth) && requestedMonth >= 1 && requestedMonth <= 12 ? requestedMonth : current.getMonth() + 1;
      const requestedTab = params.get("tab") ?? "income";
      const tab = ({ annual: "income", "new-clients": "daily", ranking: "customers" } as Record<string, string>)[requestedTab] ?? requestedTab;
      setReportTab(["income", "customers", "products", "daily", "sales", "tickets"].includes(tab) ? tab : "income");
      setYear(String(restoredYear));
      setMonth(String(restoredMonth));
      setPeriod({ year: restoredYear, month: restoredMonth });
      setReportReady(true);
    };
    restore();
    window.addEventListener("popstate", restore);
    return () => window.removeEventListener("popstate", restore);
  }, []);
  useEffect(() => {
    if (!reportReady) return;
    const params = new URLSearchParams(window.location.search);
    params.set("tab", reportTab);
    params.set("year", String(period.year));
    params.set("month", String(period.month));
    window.history.replaceState(null, "", `${window.location.pathname}?${params}${window.location.hash}`);
  }, [reportReady, reportTab, period]);
  const income = useResource<IncomeReport>(`/admin/reports/income?year=${period.year}`, false);
  const daily = useResource<DailyReport>(`/admin/reports/new-clients?year=${period.year}&month=${period.month}`, false);
  const products = useResource<ProductIncomeReport>(`/admin/reports/product-income?year=${period.year}&month=${period.month}`, false);
  const ranking = useResource<CustomerRanking>("/admin/reports/customer-ranking", false);
  const sales = useResource<SalesReport>(`/admin/reports/sales?year=${period.year}`, false);
  const ticketStats = useResource<TicketReport>(`/admin/reports/tickets?year=${period.year}&month=${period.month}`, false);
  const error = income.error ?? daily.error ?? products.error ?? ranking.error ?? sales.error ?? ticketStats.error;
  const refresh = () => { income.refresh(); daily.refresh(); products.refresh(); ranking.refresh(); sales.refresh(); ticketStats.refresh(); };
  return (
    <Workspace title="统计报表" admin>
      <PageHeading title="统计报表" description="收入、客户、产品和服务运营数据">
        <div className="flex items-center gap-2">
          <Input className="w-24" type="number" min={2000} max={now.getFullYear() + 1} value={year} onChange={(event) => setYear(event.target.value)} aria-label="统计年份" />
          <Select value={month} onValueChange={setMonth}><SelectTrigger className="w-28" aria-label="统计月份"><SelectValue /></SelectTrigger><SelectContent>{Array.from({ length: 12 }, (_, index) => <SelectItem key={index + 1} value={String(index + 1)}>{index + 1} 月</SelectItem>)}</SelectContent></Select>
          <Button disabled={!Number.isInteger(Number(year)) || Number(year) < 2000 || Number(year) > now.getFullYear() + 1} onClick={() => setPeriod({ year: Number(year), month: Number(month) })}><Search size={15} />查询</Button>
        </div>
      </PageHeading>
      <div className="mb-4 flex overflow-x-auto border-b" role="group" aria-label="报表分类">{Object.entries({ income: "收入", customers: "客户", products: "产品", daily: "每日运营", sales: "我的业绩", tickets: "工单统计" }).map(([key, label]) => <button key={key} type="button" aria-pressed={reportTab === key} className={`shrink-0 border-b-2 px-5 py-3 text-sm ${reportTab === key ? "border-primary text-primary font-medium" : "border-transparent text-muted-foreground"}`} onClick={() => setReportTab(key)}>{label}</button>)}</div>
      {error ? <Failure error={error} retry={refresh} /> : !income.data || !daily.data || !products.data || !ranking.data || !sales.data || !ticketStats.data ? <Loading /> : (
        <>
          <div className="stat-grid mb-6"><Stat icon={Receipt} label={`${period.year} 年收入`} value={money(income.data.total.income_cents)} /><Stat icon={ClipboardList} label="已支付账单" value={income.data.total.invoice_count} /><Stat icon={Users} label={`${period.month} 月新客户`} value={daily.data.days.reduce((total, item) => total + item.new_clients, 0)} /><Stat icon={Package} label={`${period.month} 月产品收入`} value={money(products.data.products.reduce((total, item) => total + item.total_amount_cents, 0))} /></div>
          <div className="grid gap-5">
            <section hidden={reportTab !== "income"} className="data-surface p-4"><div className="surface-head"><div><h2 className="font-semibold">月度收入</h2><p className="text-xs text-muted-foreground mt-1">按支付时间统计</p></div></div><div className="overflow-auto mt-4"><Table><TableHeader><TableRow><TableHead>月份</TableHead><TableHead>收入</TableHead><TableHead>账单数</TableHead></TableRow></TableHeader><TableBody>{income.data.months.length === 0 ? <TableRow><TableCell colSpan={3} className="h-20 text-center text-muted-foreground">暂无收入</TableCell></TableRow> : income.data.months.map((item) => <TableRow key={item.month}><TableCell>{item.month}</TableCell><TableCell className="money">{money(item.income_cents)}</TableCell><TableCell>{item.invoice_count}</TableCell></TableRow>)}</TableBody></Table></div></section>
            <section hidden={reportTab !== "customers"} className="data-surface p-4"><div className="surface-head"><div><h2 className="font-semibold">客户收入排名</h2><p className="text-xs text-muted-foreground mt-1">按净流水排序，展示前十名</p></div></div><div className="overflow-auto mt-4"><Table><TableHeader><TableRow><TableHead>客户</TableHead><TableHead>收入</TableHead><TableHead>支出</TableHead><TableHead>净额</TableHead></TableRow></TableHeader><TableBody>{ranking.data.length === 0 ? <TableRow><TableCell colSpan={4} className="h-20 text-center text-muted-foreground">暂无流水</TableCell></TableRow> : ranking.data.map((item) => <TableRow key={item.user_id}><TableCell>{item.display_name}</TableCell><TableCell className="money">{money(item.income_cents)}</TableCell><TableCell className="money">{money(item.expense_cents)}</TableCell><TableCell className="money">{money(item.net_cents)}</TableCell></TableRow>)}</TableBody></Table></div></section>
            <section hidden={reportTab !== "products"} className="data-surface p-4 xl:col-span-2"><div className="surface-head"><div><h2 className="font-semibold">产品收入</h2><p className="text-xs text-muted-foreground mt-1">{period.year} 年 {period.month} 月已支付新购和续费</p></div></div><div className="overflow-auto mt-4"><Table><TableHeader><TableRow><TableHead>产品</TableHead><TableHead>新购收入</TableHead><TableHead>新购订单</TableHead><TableHead>续费收入</TableHead><TableHead>续费订单</TableHead><TableHead>合计</TableHead></TableRow></TableHeader><TableBody>{products.data.products.length === 0 ? <TableRow><TableCell colSpan={6} className="h-20 text-center text-muted-foreground">暂无产品收入</TableCell></TableRow> : products.data.products.map((item) => <TableRow key={item.product_id}><TableCell>{item.name}</TableCell><TableCell className="money">{money(item.new_order_amount_cents)}</TableCell><TableCell>{item.new_order_count}</TableCell><TableCell className="money">{money(item.renewal_amount_cents)}</TableCell><TableCell>{item.renewal_order_count}</TableCell><TableCell className="money font-medium">{money(item.total_amount_cents)}</TableCell></TableRow>)}</TableBody></Table></div></section>
            <section hidden={reportTab !== "daily"} className="data-surface p-4 xl:col-span-2"><div className="surface-head"><div><h2 className="font-semibold">每日运营概览</h2><p className="text-xs text-muted-foreground mt-1">新客户、订单、工单和取消申请</p></div></div><div className="overflow-auto mt-4"><Table><TableHeader><TableRow><TableHead>日期</TableHead><TableHead>新客户</TableHead><TableHead>新订单</TableHead><TableHead>已支付订单</TableHead><TableHead>新工单</TableHead><TableHead>工单回复</TableHead><TableHead>取消申请</TableHead></TableRow></TableHeader><TableBody>{daily.data.days.length === 0 ? <TableRow><TableCell colSpan={7} className="h-20 text-center text-muted-foreground">暂无每日运营数据</TableCell></TableRow> : daily.data.days.map((item) => <TableRow key={item.date}><TableCell>{item.date}</TableCell><TableCell>{item.new_clients}</TableCell><TableCell>{item.new_orders}</TableCell><TableCell>{item.paid_orders}</TableCell><TableCell>{item.new_tickets}</TableCell><TableCell>{item.ticket_replies}</TableCell><TableCell>{item.cancellation_requests}</TableCell></TableRow>)}</TableBody></Table></div></section>
            <section hidden={reportTab !== "sales"} className="data-surface p-4"><div className="surface-head"><div><h2 className="font-semibold">我的业绩</h2><p className="text-xs text-muted-foreground mt-1">{period.year} 年按销售归属统计新订单和期间已支付账单收入</p></div></div><div className="overflow-auto mt-4"><Table><TableHeader><TableRow><TableHead>销售</TableHead><TableHead>订单数</TableHead><TableHead>收入</TableHead></TableRow></TableHeader><TableBody>{sales.data.items.length === 0 ? <TableRow><TableCell colSpan={3} className="h-20 text-center text-muted-foreground">暂无业绩数据</TableCell></TableRow> : sales.data.items.map(item => <TableRow key={item.sales_owner_id}><TableCell>{item.sales_owner_name}</TableCell><TableCell>{item.order_count}</TableCell><TableCell className="money">{money(item.income_cents)}</TableCell></TableRow>)}</TableBody></Table></div></section>
            <section hidden={reportTab !== "tickets"} className="data-surface p-4 xl:col-span-2"><div className="surface-head"><div><h2 className="font-semibold">工单统计</h2><p className="text-xs text-muted-foreground mt-1">{period.year} 年 {period.month} 月按上海时区统计工单和当前待处理数量</p></div></div><div className="overflow-auto mt-4"><Table><TableHeader><TableRow><TableHead>日期</TableHead><TableHead>工单数</TableHead><TableHead>待处理</TableHead></TableRow></TableHeader><TableBody>{ticketStats.data.items.length === 0 ? <TableRow><TableCell colSpan={3} className="h-20 text-center text-muted-foreground">暂无工单数据</TableCell></TableRow> : ticketStats.data.items.map(item => <TableRow key={item.date}><TableCell>{item.date}</TableCell><TableCell>{item.ticket_count}</TableCell><TableCell>{item.open_count}</TableCell></TableRow>)}</TableBody></Table></div></section>
          </div>
        </>
      )}
    </Workspace>
  );
}
function Stat({
  icon: Icon,
  label,
  value,
}: {
  icon: typeof Users;
  label: string;
  value: number | string;
}) {
  return (
    <div className="stat">
      <div className="flex gap-2 items-center text-muted-foreground">
        <Icon size={16} />
        {label}
      </div>
      <strong>{typeof value === "number" ? value.toLocaleString("zh-CN") : value}</strong>
    </div>
  );
}


export function CustomersView() {
  return <AdminCustomersView />;
}

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

export function PromoCodesView() {
  const [promoStatus, setPromoStatus] = useState("all");
  const { data, error, refresh } = useResource<PromoCode[]>("/admin/promo-codes");
  const [code, setCode] = useState("");
  const [discountType, setDiscountType] = useState<"percent" | "fixed" | "free">("percent");
  const [value, setValue] = useState("10");
  const [maxUses, setMaxUses] = useState("0");
  const [oncePerCustomer, setOncePerCustomer] = useState(false);
  const [onlyNewCustomer, setOnlyNewCustomer] = useState(false);
  const [recurring, setRecurring] = useState(false);
  const [recurringLimit,setRecurringLimit]=useState("0");
  const [upgradeOnly,setUpgradeOnly]=useState(false);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const visiblePromos = data?.filter(item => promoStatus === "all" || (promoStatus === "active" ? item.active : !item.active)) ?? [];
  async function create(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault(); setBusy(true); setMessage("");
    try { await api("/admin/promo-codes", { method: "POST", body: JSON.stringify({ code, discount_type: discountType, value: discountType === "free" ? 0 : Number(value), max_uses: Number(maxUses), once_per_customer: oncePerCustomer, only_new_customer: onlyNewCustomer, recurring: discountType !== "free" && recurring, recurring_limit: recurring ? Number(recurringLimit) : 0, upgrade_only: upgradeOnly }) }); setCode(""); setValue("10"); setMaxUses("0"); setRecurring(false); setRecurringLimit("0"); setUpgradeOnly(false); refresh(); }
    catch (e) { setMessage((e as Error).message); } finally { setBusy(false); }
  }
  async function toggle(item: PromoCode) {
    setBusy(true); setMessage("");
    try { await api(`/admin/promo-codes/${item.id}`, { method: "PATCH", body: JSON.stringify({ active: !item.active }) }); refresh(); }
    catch (e) { setMessage((e as Error).message); } finally { setBusy(false); }
  }
  return <Workspace title="优惠码" admin><PageHeading title="优惠码" description="配置购买折扣、循环续费和升级优惠"><p className="text-xs text-muted-foreground max-w-sm">升级专用优惠只在商品升级差额上生效，不影响首购、续费或降级返还。</p></PageHeading><PermissionGate permission="promos.manage"><section className="data-surface p-4 mb-5"><h2 className="mb-4 text-sm font-semibold">新增优惠码</h2><form className="grid md:grid-cols-[1fr_8rem_7rem_7rem_auto] gap-3 items-center" onSubmit={create}><label className="grid gap-2 text-sm">优惠码<Input value={code} onChange={(e) => setCode(e.target.value.toUpperCase())} placeholder="优惠码" aria-label="优惠码" maxLength={50} required /></label><label className="grid gap-2 text-sm">优惠类型<Select value={discountType} onValueChange={(v) => { setDiscountType(v as typeof discountType); setRecurring(false); setRecurringLimit("0"); }}><SelectTrigger aria-label="优惠类型"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="percent">百分比</SelectItem><SelectItem value="fixed">固定金额（分）</SelectItem><SelectItem value="free">免初装费</SelectItem></SelectContent></Select></label><label className="grid gap-2 text-sm">优惠值<Input type="number" min={discountType === "free" ? 0 : 1} max={discountType === "percent" ? 100 : undefined} value={value} onChange={(e) => setValue(e.target.value)} aria-label="优惠值" disabled={discountType === "free"} /></label><label className="grid gap-2 text-sm">最多使用次数<Input type="number" min="0" value={maxUses} onChange={(e) => setMaxUses(e.target.value)} aria-label="最大发码次数" placeholder="不限次数" /></label><Button className="md:self-end" disabled={busy || !code.trim()}><Plus size={16} />新增</Button><label className="flex items-center gap-2 text-sm"><Checkbox checked={oncePerCustomer} onCheckedChange={(v) => setOncePerCustomer(v === true)} />每客户一次</label><label className="flex items-center gap-2 text-sm"><Checkbox checked={onlyNewCustomer} onCheckedChange={(v) => setOnlyNewCustomer(v === true)} />仅新客户</label><label className="flex items-center gap-2 text-sm md:col-span-3"><Checkbox checked={recurring} disabled={discountType === "free" || upgradeOnly || busy} onCheckedChange={(v) => setRecurring(v === true)} />循环续费优惠</label>{recurring&&<label className="grid gap-2 text-sm md:col-span-3">每件服务优惠续费次数（0为不限）<Input type="number" min={0} max={10000} required disabled={busy} value={recurringLimit} onChange={e=>setRecurringLimit(e.target.value)} aria-label="优惠续费次数"/></label>}<label className="flex items-center gap-2 text-sm md:col-span-3"><Checkbox checked={upgradeOnly} disabled={recurring || discountType === "free" || busy} onCheckedChange={(v) => { setUpgradeOnly(v === true); setRecurring(false); }} />仅用于商品升级</label></form>{message && <p role="alert" className="text-sm text-destructive mt-3">{message}</p>}</section></PermissionGate><AdminStatusTabs label="本页优惠码状态" value={promoStatus} states={{active:"已启用",inactive:"已停用"}} onChange={setPromoStatus}/>{error ? <Failure error={error} retry={refresh} /> : !data ? <Loading /> : <div className="admin-list-surface data-surface overflow-auto"><Table><TableHeader><TableRow><TableHead>ID</TableHead><TableHead>代码</TableHead><TableHead>类型</TableHead><TableHead>值</TableHead><TableHead>发码次数</TableHead><TableHead>状态</TableHead><TableHead>操作</TableHead></TableRow></TableHeader><TableBody>{visiblePromos.length === 0 ? <TableRow><TableCell colSpan={7} className="h-28 text-center text-muted-foreground">暂无优惠码</TableCell></TableRow> : visiblePromos.map((item) => <TableRow key={item.id}><TableCell>{item.id}</TableCell><TableCell className="font-mono"><Tag size={14} className="inline mr-2" />{item.code}{item.recurring && <span className="block text-xs text-muted-foreground mt-1 font-sans">循环续费 · {item.recurring_limit>0?`${item.recurring_limit} 次 / 件`:"不限次数"}</span>}{item.upgrade_only && <span className="block text-xs text-muted-foreground mt-1 font-sans">仅升级</span>}</TableCell><TableCell>{item.discount_type === "percent" ? "百分比" : item.discount_type === "fixed" ? "固定金额" : "免初装费"}</TableCell><TableCell>{item.discount_type === "percent" ? `${item.value}%` : item.discount_type === "fixed" ? money(item.value) : "初装费"}</TableCell><TableCell>{item.used_count}{item.max_uses > 0 ? ` / ${item.max_uses}` : " / 不限"}</TableCell><TableCell><Status value={item.active ? "active" : "disabled"} /></TableCell><TableCell><PermissionGate permission="promos.manage"><Button size="sm" variant="outline" disabled={busy} onClick={() => void toggle(item)}>{item.active ? "停用" : "启用"}</Button></PermissionGate></TableCell></TableRow>)}</TableBody></Table></div>}</Workspace>;
}

type Row = Record<string, unknown>;
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
          <Table>
            <TableHeader>
              <TableRow>
                {columns.map((c) => (
                  <TableHead key={c.key}>{c.label}</TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.length===0&&<TableRow><TableCell colSpan={columns.length} className="h-32 text-center text-muted-foreground">暂无记录</TableCell></TableRow>}
              {data.map((row, i) => (
                <TableRow key={String(row.id ?? i)}>
                  {columns.map((c) => (
                    <TableCell key={c.key}>
                      {c.render ? c.render(row, refresh, returnQuery) : String(row[c.key] ?? "-")}
                    </TableCell>
                  ))}
                </TableRow>
              ))}
            </TableBody>
          </Table>
          <div className="flex items-center justify-end gap-4 border-t p-3"><span className="text-xs text-muted-foreground">第 {page+1} 页</span><Button variant="outline" size="sm" disabled={page===0} onClick={()=>setPage(v=>v-1)}>上一页</Button><Button variant="outline" size="sm" disabled={data.length<25} onClick={()=>setPage(v=>v+1)}>下一页</Button></div>
        </div>
      )}
    </Workspace>
  );
}
function PageHeading({
  title,
  description,
  children,
}: {
  title: string;
  description: string;
  children?: React.ReactNode;
}) {
  return (
    <div className="admin-page-heading flex flex-wrap justify-between items-start gap-4 mb-8">
      <div className="min-w-0">
        <h1 className="page-title break-all">{title}</h1>
        <p className="text-muted-foreground mt-2">{description}</p>
      </div>
      {children}
    </div>
  );
}
function SearchForm({
  value,
  setValue,
  submit,
  placeholder,
}: {
  value: string;
  setValue: (s: string) => void;
  submit: () => void;
  placeholder: string;
}) {
  return (
    <form
      className="flex gap-2 mb-6 max-w-md"
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <Input
        value={value}
        onChange={(e) => setValue(e.target.value)}
        placeholder={placeholder}
        aria-label={placeholder}
      />
      <Button variant="outline" size="icon" aria-label="搜索">
        <Search size={16} />
      </Button>
    </form>
  );
}

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
  function toggle(id: number, checked: boolean) { setSelected(current => checked ? [...new Set([...current,id])] : current.filter(value => value !== id)); }
  async function bulkAction(action: "cancel" | "review" | "delete") {
    if (!visibleSelected.length) return;
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
              {data.map((row, i) => { const id = Number(row.id); const commission = commissionByOrder[id]; return <TableRow key={String(row.id ?? i)}><TableCell><PermissionGate permission="orders.manage"><Checkbox checked={selected.includes(id)} onCheckedChange={(checked) => toggle(id, checked === true)} aria-label={`选择订单 ${String(row.order_no ?? id)}`} /></PermissionGate></TableCell><TableCell className="font-mono text-xs"><Link className="text-primary hover:underline" href={`/admin/orders/${id}${suffix}`}>{String(row.order_no ?? "-")}</Link></TableCell><TableCell>{String(row.customer_name ?? "-")}</TableCell><TableCell><div className="max-w-64 whitespace-normal break-words">{String(row.product_names || "-")}<span className="block text-xs text-muted-foreground">{String(row.service_domains || "-")}</span></div></TableCell><TableCell className="font-mono text-xs">{String(row.service_ips || "-")}</TableCell><TableCell><span className="money">{money(Number(row.total_cents))}</span><span className="block text-xs text-muted-foreground">{row.invoice_id == null ? "未生成账单" : <Status value={String(row.invoice_status)} />} · {String(row.payment_method || "-")}</span></TableCell><TableCell>{date(String(row.created_at))}</TableCell><TableCell><Status value={String(row.status)} /></TableCell><TableCell><Status value={String(row.review_status ?? "approved")} /></TableCell><TableCell className="max-w-56 whitespace-normal break-words">{String(row.customer_note || "-")}</TableCell><TableCell className="max-w-56 whitespace-normal break-words">{String(row.notes || "-")}</TableCell><TableCell><span>{String(row.sales_owner_name || "-")}</span>{canViewSales && <span className="mt-1 block text-xs text-muted-foreground">{commissionLoading ? "计算中…" : commission ? <><strong className="money">{money(Number(commission.commission_cents))}</strong><span className="ml-1">{commissionStatusLabels[commission.status] ?? commission.status}</span></> : commissionError ? "预估读取失败" : "—"}</span>}</TableCell>{supplier&&<TableCell>{(Array.isArray(row.supplier_resources)?row.supplier_resources:[]).map((resource:Record<string,unknown>)=><p key={String(resource.service_id)} className="text-xs">{String(resource.supplier_name||`供应商 #${resource.supplier_id}`)} · 服务 #{String(resource.service_id)} · {resource.cost_cents==null?"成本未记录":`${money(Number(resource.cost_cents))} CNY`}</p>)}</TableCell>}<TableCell><div className="flex flex-wrap items-center gap-2"><OrderReviewAction row={row} refresh={refresh} /><OrderCancelAction row={row} admin refresh={refresh} /><OrderDeleteAction row={row} refresh={refresh} /></div></TableCell></TableRow>; })}
            </TableBody>
          </Table>
          <div className="flex flex-wrap items-center justify-between gap-3 border-t p-3"><div className="flex flex-wrap gap-2"><PermissionGate permission="orders.manage"><Button variant="destructive" size="sm" disabled={busy || visibleSelected.length === 0} onClick={() => void bulkAction("cancel")}><Ban size={15} />批量取消 {visibleSelected.length ? `(${visibleSelected.length})` : ""}</Button><Button size="sm" disabled={busy || !visibleSelected.length} onClick={() => void bulkAction("review")}>核验通过</Button><Button variant="destructive" size="sm" disabled={busy || !visibleSelected.length} onClick={() => void bulkAction("delete")}>删除订单</Button></PermissionGate></div><div className="min-w-0 basis-72 flex-1"><AdminPagination total={result?.total ?? 0} page={page} pageSize={pageSize} onPageChange={setPage} onPageSizeChange={size => { setExtra({ ...extra, limit: String(size) }); setPage(0); }} /></div></div>
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
  if (!allowed || String(row.review_status) !== "pending" || String(row.invoice_status) !== "paid") return null;
  async function decide(decision: "approved" | "rejected") {
    if (!window.confirm(decision === "approved" ? "确认审核通过此订单并继续交付？" : "确认驳回此订单？")) return;
    setBusy(true); setMessage("");
    try {
      await api(`/admin/orders/${Number(row.id)}/review`, { method: "POST", body: JSON.stringify({ expected_version: Number(row.version), decision, note: decision === "approved" ? "管理员审核通过" : "管理员审核驳回" }), headers: { "Idempotency-Key": `web-order-review-${row.id}-${decision}-${crypto.randomUUID()}` } });
      setMessage(decision === "approved" ? "已通过" : "已驳回"); refresh();
    } catch (e) { setMessage((e as Error).message); } finally { setBusy(false); }
  }
  return <div className="flex flex-wrap items-center gap-2"><Button size="sm" disabled={busy} onClick={() => void decide("approved")}>通过</Button><Button size="sm" variant="outline" disabled={busy} onClick={() => void decide("rejected")}>驳回</Button>{message && <span className="text-xs text-muted-foreground whitespace-nowrap">{message}</span>}</div>;
}

function OrderDeleteAction({row,refresh}:{row:Row;refresh:()=>void}) {
 const allowed = useAdminPermission("orders.manage");
 const [busy,setBusy] = useState(false); const [message,setMessage] = useState("");
 if (!allowed || !["pending","cancelled","refunded"].includes(String(row.status))) return null;
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
 return <div className="text-xs text-muted-foreground mt-2 space-y-1">{snapshot.map(option=><p className="whitespace-normal break-all" key={String(option.id)}>{String(option.group_name)}：{String(option.label)}（{String(option.value)}）{Number(option.quantity??1)>1?` × ${option.quantity}`:""} · 周期增减 {money(Number(option.price_delta_cents))} · 初装增减 {money(Number(option.setup_delta_cents))}</p>)}{snapshot.length===0&&Array.isArray(item.config_options)&&item.config_options.length>0&&<p>历史配置编号：{item.config_options.join("、")}；未保留名称快照。</p>}</div>;
}
export function OrderDetailView({ id }: { id: number }) {
  const supplierContext=useSearchParams().get("source")==="supplier";
 const backHref = useAdminListReturnHref(supplierContext?"/admin/suppliers/orders":"/admin/orders", adminOrderStates, "all", supplierContext?supplierListDefaults:orderListDefaults);
  const { data, error, refresh } = useResource<Row>(`/admin/orders/${id}`);
  const [note, setNote] = useState<string | null>(null);
  const [noteVersion,setNoteVersion] = useState<number | null>(null);
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState("");
  async function saveNotes(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSaving(true); setMessage("");
    try {
      await api(`/admin/orders/${id}/notes`, { method: "POST", body: JSON.stringify({ expected_version: noteVersion ?? Number(data?.version), notes: note ?? String(data?.notes ?? "") }), headers: {"Idempotency-Key":`web-order-notes-${id}-${crypto.randomUUID()}`} });
      setMessage("备注已保存"); setNote(null); setNoteVersion(null); refresh();
    } catch (e) { setMessage((e as Error).message); } finally { setSaving(false); }
  }
  return <Workspace title="订单详情" admin><Button variant="ghost" className="mb-4" asChild><Link href={backHref}><ArrowUpRight size={16} />返回订单列表</Link></Button>{error ? <Failure error={error} retry={refresh} /> : !data ? <Loading /> : <><PageHeading title={String(data.order_no)} description={`客户：${String(data.display_name)} · 创建于 ${date(String(data.created_at))}`}><div className="flex flex-wrap items-center gap-2"><Status value={String(data.status)} /><Status value={String(data.review_status ?? "approved")} /><OrderReviewAction row={data} refresh={refresh} /><OrderCancelAction row={data} admin refresh={refresh} /><Button variant="outline" size="sm" onClick={refresh}>刷新订单</Button><OrderDeleteAction row={data} refresh={() => window.location.assign(backHref)} /></div></PageHeading><div className="grid md:grid-cols-3 gap-3 mb-5"><section className="data-surface p-4"><p className="text-xs text-muted-foreground">订单金额</p><p className="money text-xl font-semibold mt-2">{money(Number(data.total_cents))}</p></section><section className="data-surface p-4"><p className="text-xs text-muted-foreground">账单</p><p className="mt-2">{data.invoice_id == null ? "尚未生成账单" : <>{String(data.invoice_no)} · <Status value={String(data.invoice_status)} /></>}</p></section><section className="data-surface p-4"><p className="text-xs text-muted-foreground">客户邮箱</p><p className="mt-2 break-all">{String(data.email)}</p></section></div><div className="mb-4 flex flex-wrap gap-2"><DeferredOrderInvoice row={data} refresh={refresh}/><PermissionGate permission="customers.view"><Button variant="outline" size="sm" asChild><Link href={`/admin/customers/${data.user_id}`}>客户摘要</Link></Button></PermissionGate>{data.invoice_id != null && <PermissionGate permission="invoices.view"><Button variant="outline" size="sm" asChild><Link href={`/admin/invoices/${data.invoice_id}`}>关联账单</Link></Button></PermissionGate>}</div><div className="grid xl:grid-cols-2 gap-4"><section className="data-surface p-6"><h2 className="font-semibold">订单项目</h2><div className="overflow-auto mt-4"><Table><TableHeader><TableRow><TableHead>产品</TableHead><TableHead>账期</TableHead><TableHead>数量</TableHead><TableHead>金额</TableHead></TableRow></TableHeader><TableBody>{(data.items as Row[]).map((item) => <TableRow key={String(item.id)}><TableCell>{String(item.product_name)}<OrderConfigurationSnapshot item={item}/>{item.first_price_override_cents != null && <p className="text-xs text-muted-foreground">首次内部价格：{money(Number(item.first_price_override_cents))}</p>}{item.renewal_price_override_cents != null && <p className="text-xs text-muted-foreground">续费内部价格：{money(Number(item.renewal_price_override_cents))}</p>}<RenewalBenefitDetails terms={{renewal_discount_percent:Number(item.renewal_discount_percent),renewal_fixed_discount_cents:Number(item.renewal_fixed_discount_cents),renewal_discount_limit:Number(item.renewal_discount_limit),renewal_promo_code:String(item.renewal_promo_code??"")}}/><PurchaseFieldDetails fields={Array.isArray(item.purchase_fields)?item.purchase_fields as PurchaseFieldSnapshot[]:[]}/></TableCell><TableCell>{String(item.billing_cycle)}</TableCell><TableCell>{String(item.quantity)}</TableCell><TableCell className="money">{money(Number(item.total_cents))}</TableCell></TableRow>)}</TableBody></Table></div></section><section className="data-surface p-6"><h2 className="font-semibold">服务实例</h2><div className="overflow-auto mt-4"><Table><TableHeader><TableRow><TableHead>服务</TableHead><TableHead>域名</TableHead><TableHead>状态</TableHead></TableRow></TableHeader><TableBody>{(data.services as Row[]).map((service) => <TableRow key={String(service.id)}><TableCell><PermissionGate permission="services.view"><Link className="text-primary hover:underline" href={`/admin/services/${service.id}`}>{String(service.name)}</Link></PermissionGate></TableCell><TableCell className="font-mono text-xs">{String(service.domain)}</TableCell><TableCell><Status value={String(service.status)} /></TableCell></TableRow>)}</TableBody></Table></div></section></div><section className="data-surface p-4 mt-5"><h2 className="font-semibold">建单信息</h2><dl className="grid sm:grid-cols-2 gap-3 mt-3 text-sm"><div><dt className="text-muted-foreground">付款偏好</dt><dd>{String(data.payment_provider || "由客户付款时选择")}</dd></div><div><dt className="text-muted-foreground">订单通知</dt><dd>{({not_requested:"未请求发送",queued:"已加入发送队列",skipped_unverified:"未发送：客户邮箱尚未验证",skipped_channel:"未发送：邮件渠道未启用",skipped_template:"未发送：通知模板未启用"} as Record<string,string>)[String(data.email_status)] ?? String(data.email_status || "-")}</dd></div></dl></section><section className="data-surface p-6 mt-5"><h2 className="font-semibold">管理员备注</h2><PermissionFields permission="orders.manage"><form className="mt-4 space-y-3" onSubmit={(event) => void saveNotes(event)}><Textarea value={note ?? String(data.notes ?? "")} onChange={(event) => {setNote(event.target.value);if(noteVersion === null)setNoteVersion(Number(data.version));}} rows={5} maxLength={5000} aria-label="订单备注" /><div className="flex items-center gap-3"><Button disabled={saving}>{saving ? "保存中" : "保存备注"}</Button><Button type="button" variant="outline" disabled={saving} onClick={() => {setNote(null);setNoteVersion(null);setMessage("");refresh();}}>取消修改</Button>{message && <span className="text-sm text-muted-foreground" role="status">{message}</span>}</div></form></PermissionFields></section></> }</Workspace>;
}

export function ServicesView({ admin = false, groupId }: { admin?: boolean; groupId?: number }) {
  return <Workspace title={admin ? "产品服务" : "我的服务"} admin={admin}><ServiceList admin={admin} groupId={groupId}/></Workspace>;
}
function ServiceList({admin,groupId}:{admin:boolean;groupId?:number}){
  const { query, setQuery, draft, setDraft, status: state, setStatus: setState, page, setPage, ready, suffix } = useAdminListNavigation(admin, adminServiceStates, "");
  const offset = page * 20;
  const setOffset = (value: number | ((current: number) => number)) => setPage(current => Math.floor((typeof value === "function" ? value(current * 20) : value) / 20));
  const groups=useServiceNavigation(!admin);
  const {data,error,refresh}=useResource<ServicePage>(ready ? `${admin?"/admin":""}/services/page?group_id=${groupId??""}&status=${state}&q=${encodeURIComponent(query)}&limit=20&offset=${offset}` : null,false);
  useEffect(()=>{
    if(!data?.items.some(s=>s.status==="queued"||s.status==="provisioning"))return;
    const timer=window.setInterval(refresh,2000);return ()=>window.clearInterval(timer);
  },[data,refresh]);
  const group=groups.data?.find(g=>g.id===groupId);
  const missingGroup=groupId!==undefined&&groupId!==0&&!!groups.data&&!group;
  return <>
    <PageHeading title={admin?"产品服务":groupId===undefined?"我的服务":groupId===0?"未分组服务":group?.name??"服务分组"} description={admin?"业务列表":"查看并管理已购买的服务"}>{!admin&&<ServiceNavigationSettings/>}</PageHeading>
    {admin&&<nav aria-label="业务状态" className="flex overflow-x-auto border-b mb-5">{[{value:"",label:"全部"},{value:"active",label:"已激活"},{value:"queued",label:"待开通"},{value:"suspended",label:"已暂停"},{value:"terminated",label:"已终止"},{value:"failed",label:"失败"}].map(tab=><button key={tab.value} type="button" aria-current={state===tab.value?"page":undefined} className={`shrink-0 border-b-2 px-5 py-3 text-sm whitespace-nowrap ${state===tab.value?"border-primary text-primary font-medium":"border-transparent text-muted-foreground hover:text-primary"}`} onClick={()=>{setState(tab.value);setOffset(0);}}>{tab.label}</button>)}</nav>}
    {!admin&&<div className="flex flex-wrap items-center gap-3 mb-5"><label className="text-sm flex gap-2 items-center min-w-0">查看分组<select aria-label="查看服务分组" className={`${catalogSelectStyle} sm:max-w-xs`} value={groupId??""} onChange={e=>{window.location.assign(e.target.value?`/console/services/group/${e.target.value}`:"/console/services");}}><option value="">全部服务</option><option value="0">未分组服务</option>{groups.data?.map(g=><option key={g.id} value={g.id}>{g.name}{!g.visible&&"（导航隐藏）"}</option>)}{missingGroup&&<option value={groupId}>已删除的分组</option>}</select></label>{groupId!==undefined&&<Button variant="link" asChild><Link href="/console/services">查看全部服务</Link></Button>}</div>}
    {missingGroup&&<p role="status" className="text-sm text-muted-foreground mb-5">该服务分组已删除，请从全部服务或其他分组继续查看。</p>}
    {!admin&&groups.error&&<Failure error={groups.error} retry={groups.refresh}/>}
    <form className="grid sm:grid-cols-[12rem_1fr_auto] gap-3 mb-6" onSubmit={e=>{e.preventDefault();setQuery(draft);setOffset(0);}}><select aria-label="服务状态筛选" className={catalogSelectStyle} value={state} onChange={e=>{setState(e.target.value);setOffset(0);}}><option value="">全部状态</option>{Object.entries({pending_payment:"待付款",queued:"开通队列",provisioning:"开通中",active:"运行中",suspended:"已暂停",failed:"失败",cancelled:"已取消",terminated:"已终止",import_review_required:"迁移待复核"}).map(([value,label])=><option value={value} key={value}>{label}</option>)}</select><Input aria-label="搜索服务" value={draft} onChange={e=>setDraft(e.target.value)} placeholder="搜索服务名称或标识"/><Button variant="outline"><Search size={16}/>搜索</Button></form>
    {error?<Failure error={error} retry={refresh}/>:!data?<Loading/>:<><div className="admin-list-surface data-surface overflow-auto"><Table><TableHeader><TableRow><TableHead>服务</TableHead>{admin&&<TableHead>客户</TableHead>}<TableHead>主机名</TableHead>{admin&&<><TableHead>IP 地址</TableHead><TableHead>付款周期</TableHead><TableHead>周期金额</TableHead></>}<TableHead>到期时间</TableHead><TableHead>状态</TableHead><TableHead>操作</TableHead></TableRow></TableHeader><TableBody>{data.items.length===0?<TableRow><TableCell colSpan={admin?9:5} className="h-32 text-center text-muted-foreground">暂无服务</TableCell></TableRow>:data.items.map(service=><TableRow key={service.id}><TableCell><Link className="text-primary hover:underline" href={`${admin?"/admin":"/console"}/services/${service.id}${suffix}`}><div className="font-medium flex items-center gap-2"><Server size={15}/>{service.name}</div><div className="text-xs text-muted-foreground mt-1">#{service.id} · {service.service_group_name??"未分组"}</div></Link></TableCell>{admin&&<TableCell>{service.customer_name??"-"}</TableCell>}<TableCell className="font-mono text-xs">{service.domain}</TableCell>{admin&&<><TableCell className="font-mono text-xs">{service.resource_info?.dedicated_ip || service.resource_info?.ips?.[0] || "-"}</TableCell><TableCell>{({monthly:"月付",quarterly:"季付",annually:"年付",once:"一次性"} as Record<string,string>)[service.billing_cycle] || service.billing_cycle}</TableCell><TableCell className="money">{money(service.recurring_price_cents)}</TableCell></>}<TableCell>{serviceDueLabel(service)}</TableCell><TableCell><Status value={service.status}/></TableCell><TableCell><ServiceActions service={service} admin={admin} refresh={refresh}/></TableCell></TableRow>)}</TableBody></Table></div><div className="flex justify-between items-center flex-wrap gap-3 mt-5 text-sm"><span>{data.total} 个服务</span><div className="flex gap-2"><Button variant="outline" disabled={offset===0} onClick={()=>setOffset(v=>Math.max(0,v-20))}>上一页</Button><Button variant="outline" disabled={offset+20>=data.total} onClick={()=>setOffset(v=>v+20)}>下一页</Button></div></div></>}
  </>;
}

function serviceDueLabel(service: Service) {
  if (service.next_due_at) return date(service.next_due_at);
  return service.billing_cycle === "once" ? "一次性服务" : "尚未起算";
}

export function ServiceDetailView({ id, admin = false }: { id: number; admin?: boolean }) {
  const { data, error, refresh } = useResource<Service>(`${admin ? "/admin" : ""}/services/${id}`);
  useEffect(() => {
    if (!data?.jobs?.some((job) => job.status === "pending" || job.status === "running")) return;
    const timer = window.setInterval(refresh, 2000);
    return () => window.clearInterval(timer);
  }, [data, refresh]);
  if (admin) return <Workspace title="服务详情" admin>{error?<Failure error={error} retry={refresh}/>:!data?<Loading/>:<AdminServiceDetail service={data} refresh={refresh} manualDelivery={data.provision_mode==="manual"&&data.jobs?.some(job=>job.status==="waiting_manual")?<PermissionGate permission="services.manage"><ManualDelivery service={data} refresh={refresh}/></PermissionGate>:null} resourcePanels={data.provision_mode==="nokvm"&&["active","suspended"].includes(data.status)?<><NOKVMMonitoringPanel service={data} admin/><NOKVMStoragePanel service={data} admin refresh={refresh}/><NOKVMNATPanel service={data} admin refresh={refresh}/><NOKVMCDROMPanel service={data} admin refresh={refresh}/></>:null}/>}</Workspace>;
  return (
    <Workspace title="服务详情" admin={admin}>
      <Button variant="ghost" className="mb-5" asChild><Link href={admin ? "/admin/services" : "/console/services"}><ArrowUpRight size={16} />返回服务列表</Link></Button>
      {error ? <Failure error={error} retry={refresh} /> : !data ? <Loading /> : (
        <>
          <PageHeading title={data.name} description={`服务 #${data.id} · ${data.domain}`}>
            <Status value={data.status} />
            {["active", "suspended"].includes(data.status) && <Button variant="outline" asChild><Link href={`/console/services/${id}/traffic`}>购买流量包</Link></Button>}
          </PageHeading>
          <div className="grid md:grid-cols-2 gap-5 mb-8">
            <section className="data-surface p-6"><p className="text-xs text-muted-foreground">上游标识</p><p className="font-mono mt-2 break-all">{data.upstream_id || "尚未分配"}</p><p className="text-xs text-muted-foreground mt-5">登录用户名</p><p className="mt-2">{data.username || "尚未分配"}</p></section>
            <section className="data-surface p-6"><p className="text-xs text-muted-foreground">到期时间</p><p className="mt-2">{serviceDueLabel(data)}</p><p className="text-xs text-muted-foreground mt-5">最近错误</p><p className="mt-2 text-destructive break-words">{data.last_error || "无"}</p></section>
          </div>
          <section className="data-surface overflow-auto"><div className="surface-head"><div><h2 className="font-semibold">开通任务</h2><p className="text-xs text-muted-foreground mt-1">任务状态与重试记录</p></div></div><Table><TableHeader><TableRow><TableHead>任务</TableHead><TableHead>动作</TableHead><TableHead>状态</TableHead><TableHead>尝试次数</TableHead><TableHead>错误</TableHead><TableHead>时间</TableHead></TableRow></TableHeader><TableBody>{!data.jobs?.length ? <TableRow><TableCell colSpan={6} className="h-24 text-center text-muted-foreground">暂无任务</TableCell></TableRow> : data.jobs.map((job) => <TableRow key={job.id}><TableCell>#{job.id}</TableCell><TableCell>{serviceActionLabels[job.action]??job.action}</TableCell><TableCell><Status value={job.status==="succeeded"?(isPowerAction(job.action)?"command_accepted":job.action==="reset_password"?"credentials_confirmed":job.status):job.status} /></TableCell><TableCell>{job.attempts}</TableCell><TableCell className="text-destructive max-w-80 whitespace-normal">{job.last_error || "-"}</TableCell><TableCell>{date(job.completed_at || job.created_at)}</TableCell></TableRow>)}</TableBody></Table></section>
          <ServiceActivityPanel serviceID={data.id} admin={admin} />
          {admin && data.provision_mode === "manual" && data.jobs?.some((job) => job.status === "waiting_manual") && <PermissionGate permission="services.manage"><ManualDelivery service={data} refresh={refresh} /></PermissionGate>}
          {data.provision_mode === "nokvm" && <NOKVMServiceDelivery key={`${data.id}:${data.version}`} service={data} admin={admin} refresh={refresh} />}
          {data.provision_mode === "supplier" && <SupplierServiceDelivery key={`${data.id}:${data.version}`} service={data} admin={admin} refresh={refresh} />}
          {data.provision_mode === "nokvm" && ["active","suspended"].includes(data.status) && <NOKVMMonitoringPanel key={data.id} service={data} admin={admin}/>}
          {data.provision_mode === "nokvm" && ["active","suspended"].includes(data.status) && <NOKVMStoragePanel key={`${data.id}:${data.jobs?.map(j=>j.status).join("-")}`} service={data} admin={admin} refresh={refresh}/>}
          {data.provision_mode === "nokvm" && ["active","suspended"].includes(data.status) && <NOKVMNATPanel key={`${data.id}:${data.jobs?.map(j=>j.status).join("-")}`} service={data} admin={admin} refresh={refresh}/>}
          {data.provision_mode === "nokvm" && ["active","suspended"].includes(data.status) && <NOKVMCDROMPanel key={`cdrom:${data.id}:${data.jobs?.map(j=>j.status).join("-")}`} service={data} admin={admin} refresh={refresh}/>}
          <ServiceConfigurationChanges service={data} admin={admin} refresh={refresh} />
          {!!data.purchase_fields?.length && <section className="data-surface p-6 my-5"><h2 className="font-semibold mb-4">购买信息</h2><PurchaseFieldDetails fields={data.purchase_fields}/></section>}
          {!admin && <ServiceDownloads key={data.id} serviceID={data.id} />}
          {!admin && (data.provision_mode !== "supplier" || data.cancellation) && <CancellationPanel service={data} refresh={refresh} />}
          {!admin && data.provision_mode !== "supplier" && <RenewalPanel service={data} />}
          {!admin && data.provision_mode !== "supplier" && <AutoRenewPanel service={data} refresh={refresh} />}
        </>
      )}
    </Workspace>
  );
}

function CancellationPanel({ service, refresh }: { service: Service; refresh: () => void }) {
  const request = service.cancellation;
  const [type, setType] = useState<"immediate" | "end_of_billing">("end_of_billing");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true); setError("");
    try {
      await api(`/services/${service.id}/cancellation`, { method: "POST", body: JSON.stringify({ type, reason }), headers: { "Idempotency-Key": `web-cancellation-${service.id}-${crypto.randomUUID()}` } });
      setReason(""); refresh();
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  async function withdraw() {
    setBusy(true); setError("");
    try { await api(`/services/${service.id}/cancellation`, { method: "DELETE" }); refresh(); }
    catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  if (request && request.status !== "withdrawn") {
    return <section className="data-surface p-6 mt-5"><div className="surface-head"><div><h2 className="font-semibold">取消申请</h2><p className="text-xs text-muted-foreground mt-1">{request.type === "immediate" ? "立即取消" : "当前账期结束时取消"}</p></div><Status value={request.status} /></div><p className="text-sm mt-4 whitespace-pre-wrap">{request.reason}</p>{request.resolution_note && <p className="text-xs text-muted-foreground mt-3">处理备注：{request.resolution_note}</p>}{request.status === "pending" && <Button className="mt-4" variant="outline" disabled={busy} onClick={withdraw}>撤回申请</Button>}{error && <p className="text-sm text-destructive mt-3">{error}</p>}</section>;
  }
  if (service.status !== "active" && service.status !== "suspended") return null;
  return <section className="data-surface p-6 mt-5"><div className="surface-head"><div><h2 className="font-semibold">申请取消服务</h2><p className="text-xs text-muted-foreground mt-1">提交后由管理员审核，服务不会立即中断。</p></div></div><form className="space-y-4 mt-4" onSubmit={submit}><div className="grid sm:grid-cols-2 gap-3"><label className="text-sm"><span className="block text-muted-foreground mb-2">取消时间</span><Select value={type} onValueChange={(value) => setType(value as typeof type)}><SelectTrigger><SelectValue /></SelectTrigger><SelectContent><SelectItem value="end_of_billing">账期结束时取消</SelectItem><SelectItem value="immediate">立即取消</SelectItem></SelectContent></Select></label><label className="text-sm"><span className="block text-muted-foreground mb-2">申请原因</span><Input value={reason} onChange={(event) => setReason(event.target.value)} minLength={3} maxLength={2000} placeholder="请说明取消原因" required /></label></div><div className="flex items-center gap-3"><Button variant="destructive" disabled={busy || reason.trim().length < 3}>提交取消申请</Button>{error && <span className="text-sm text-destructive">{error}</span>}</div></form></section>;
}

function RenewalPanel({ service }: { service: Service }) {
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [invoiceID, setInvoiceID] = useState<number | null>(null);
  if ((service.status !== "active" && service.status !== "suspended") || service.billing_cycle === "once") return null;
  async function renew() {
    setBusy(true); setMessage("");
    try {
      const result = await api<{ invoice_id: number }>(`/services/${service.id}/renew`, { method: "POST", body: "{}", headers: { "Idempotency-Key": `web-renew-${service.id}-${crypto.randomUUID()}` } });
      setMessage(`已生成账单 #${result.invoice_id}`);
      setInvoiceID(result.invoice_id);
    } catch (e) { setMessage((e as Error).message); } finally { setBusy(false); }
  }
  return <section className="data-surface p-6 mt-5"><div className="surface-head"><div><h2 className="font-semibold">服务续费</h2><p className="text-xs text-muted-foreground mt-1">当前到期时间：{service.next_due_at ? date(service.next_due_at) : "未设置"}</p></div></div><div className="flex flex-wrap items-center gap-3 mt-4"><Button variant="outline" disabled={busy} onClick={renew}><RotateCcw size={15} />{busy ? "生成中" : "生成续费账单"}</Button><Link className="text-sm text-primary underline underline-offset-4" href={invoiceID ? `/console/invoices/${invoiceID}` : "/console/invoices"}>查看账单</Link>{message && <span className="text-xs text-muted-foreground">{message}</span>}</div></section>;
}

function AutoRenewPanel({ service, refresh }: { service: Service; refresh: () => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  if ((service.status !== "active" && service.status !== "suspended") || service.billing_cycle === "once") return null;
  async function change(enabled: boolean) {
    setBusy(true); setError("");
    try {
      await api(`/services/${service.id}/auto-renew`, { method: "PATCH", body: JSON.stringify({ enabled }) });
      refresh();
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  return <section className="data-surface p-6 mt-5"><div className="surface-head"><div><h2 className="font-semibold">自动续费</h2><p className="text-xs text-muted-foreground mt-1">到期前 24 小时内尝试使用账户余额续费</p></div></div><label className="flex items-center gap-3 mt-4 text-sm"><Checkbox checked={service.auto_renew} disabled={busy} onCheckedChange={(value) => void change(value === true)} />启用自动续费</label>{service.auto_renew_error && <p className="text-xs text-destructive mt-3">最近一次尝试：{service.auto_renew_error}</p>}{error && <p className="text-xs text-destructive mt-3">{error}</p>}</section>;
}

function ManualDelivery({ service, refresh }: { service: Service; refresh: () => void }) {
  const job = service.jobs?.find((item) => item.status === "waiting_manual");
  const [upstreamId, setUpstreamId] = useState("");
  const [username, setUsername] = useState("");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  if (!job) return null;
  const currentJob = job;
  async function complete() {
    setBusy(true); setError("");
    try {
      await api(`/admin/services/${service.id}/delivery`, { method: "POST", body: JSON.stringify({ job_id: currentJob.id, upstream_id: upstreamId, username, note }) });
      refresh();
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  return <section className="data-surface p-6 mt-5"><div className="surface-head"><div><h2 className="font-semibold">人工交付确认</h2><p className="text-xs text-muted-foreground mt-1">任务 #{currentJob.id} 已等待管理员登记实际资源</p></div></div><div className="grid md:grid-cols-3 gap-3"><Input value={upstreamId} onChange={(e) => setUpstreamId(e.target.value)} placeholder="实际资源标识" aria-label="实际资源标识" /><Input value={username} onChange={(e) => setUsername(e.target.value)} placeholder="登录用户名（可选）" aria-label="登录用户名" /><Input value={note} onChange={(e) => setNote(e.target.value)} placeholder="交付凭据或核验记录" aria-label="交付凭据或核验记录" /></div><div className="flex items-center gap-3 mt-4"><Button disabled={busy || note.trim().length < 3 || (currentJob.action === "create" && !upstreamId.trim())} onClick={complete}><Play size={14} />{busy ? "提交中" : "确认交付"}</Button>{error && <span className="text-xs text-destructive">{error}</span>}</div></section>;
}

function ServiceActions({ service, admin, refresh }: { service: Service; admin: boolean; refresh: () => void }) {
  const allowed = useAdminPermission("services.manage");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const actions: { action: "suspend" | "resume" | "terminate"; label: string; icon: typeof Pause; destructive?: boolean }[] =
    service.status === "active" ? [{ action: "suspend", label: "暂停", icon: Pause }, { action: "terminate", label: "终止", icon: Trash2, destructive: true }] :
    service.status === "suspended" ? [{ action: "resume", label: "恢复", icon: Play }, { action: "terminate", label: "终止", icon: Trash2, destructive: true }] : [];
  async function run(action: "suspend" | "resume" | "terminate") {
    setBusy(true);
    setError("");
    try {
      await api(`${admin ? "/admin" : ""}/services/${service.id}/actions`, {
        method: "POST",
        body: JSON.stringify({ action }),
        headers: { "Idempotency-Key": `web-service-${action}-${service.id}-${crypto.randomUUID()}` },
      });
      refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  if (service.provision_mode==="supplier" || (admin && !allowed) || (!actions.length && !error)) return <span className="text-xs text-muted-foreground">-</span>;
  return <div className="flex flex-wrap items-center gap-2">{actions.map(({ action, label, icon: Icon, destructive }) => <Button key={action} size="sm" variant={destructive ? "destructive" : "outline"} disabled={busy} onClick={() => run(action)}><Icon size={14} />{label}</Button>)}{error && <span className="text-xs text-destructive max-w-40 whitespace-normal">{error}</span>}</div>;
}

export function InvoicesView({ admin = false }: { admin?: boolean }) {
  return (
    <AdminListView
      kind="invoices"
      admin={admin}
      title={admin ? "财务账单" : "我的账单"}
      description={admin ? "账单状态与应收金额" : "查看应付账单"}
      endpoint={admin ? "/admin/invoices" : "/invoices"}
      columns={[
        { key: "invoice_no", label: "账单号" },
        { key: "order_id", label: "订单" },
        {
          key: "amount_cents",
          label: "金额",
          render: (r) => (
            <span className="money">{money(Number(r.amount_cents))}</span>
          ),
        },
        {
          key: "due_at",
          label: "到期时间",
          render: (r) => date(String(r.due_at)),
        },
        {
          key: "status",
          label: "状态",
          render: (r) => <Status value={String(r.status)} />,
        },
        {
          key: "actions",
          label: "操作",
          render: (r) => <InvoiceAction row={r} admin={admin} />,
        },
      ]}
    />
  );
}

function InvoiceAction({ row, admin }: { row: Row; admin: boolean }) {
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState(String(row.status));
  const [error, setError] = useState("");
  const [checkout, setCheckout] = useState("");
  async function change(action: "pay" | "refund") {
    setBusy(true);
    setError("");
    try {
      const id = Number(row.id);
      const endpoint = admin ? `/admin/invoices/${id}/refund` : `/invoices/${id}/pay`;
      await api(endpoint, {
        method: "POST",
        body: "{}",
        headers: { "Idempotency-Key": `web-${action}-${id}-${crypto.randomUUID()}` },
      });
      setStatus(action === "pay" ? "paid" : "refunded");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function createExternalPayment() {
    setBusy(true);
    setError("");
    try {
      const id = Number(row.id);
      const result = await api<{ checkout_url: string; provider_payment_id: string }>(`/invoices/${id}/payments`, {
        method: "POST",
        body: JSON.stringify({ provider: "mock" }),
        headers: { "Idempotency-Key": `web-gateway-${id}-${crypto.randomUUID()}` },
      });
      setCheckout(result.checkout_url);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  if (admin) {
    if (status !== "paid") return null;
    return (
      <PermissionGate permission="invoices.refund"><div className="flex items-center gap-2">
        <Button size="sm" variant="outline" disabled={busy} onClick={() => change("refund")}>
          退款
        </Button>
        {error && <span className="text-xs text-destructive max-w-36 whitespace-normal">{error}</span>}
      </div></PermissionGate>
    );
  }
  if (status !== "unpaid") return null;
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Button size="sm" disabled={busy} onClick={() => change("pay")}>
        {busy ? "处理中" : "余额支付"}
      </Button>
      <Button size="sm" variant="outline" disabled={busy} onClick={createExternalPayment}>
        <CreditCard size={14} />在线支付
      </Button>
      {checkout && <span className="text-xs text-muted-foreground">支付单已创建：{checkout}</span>}
      {error && <span className="text-xs text-destructive max-w-36 whitespace-normal">{error}</span>}
    </div>
  );
}

type Account = {
  phone?: string;
  phone_verified?: boolean;
  email_verified?: boolean;
  balance_cents: number;
  ledger: {
    id: number;
    entry_type: string;
    delta_cents: number;
    balance_after: number;
    note: string;
    created_at: string;
  }[];
};

export function AccountView() {
  const { data, error, refresh } = useResource<Account>("/account");
  const me = useResource<User>("/me");
  return (
    <Workspace title="账户余额">
      <PageHeading title="账户余额" description="余额流水与可用金额" />
      {error ? <Failure error={error} retry={refresh} /> : !data ? <Loading /> : (
        <>
          <section className="data-surface p-6 mb-6">
            <p className="text-sm text-muted-foreground">可用余额</p>
            <strong className="text-3xl money block mt-2">{money(data.balance_cents)}</strong>
          </section>
          <PasswordForm />
          {me.error ? <Failure error={me.error} retry={me.refresh} /> : !me.data ? <Loading /> : <AccountVerification email={me.data.email} phone={data.phone} phoneVerified={data.phone_verified} emailVerified={data.email_verified} onVerified={refresh} />}
          <MfaPanel />
          <ApiKeysPanel />
          <ContactsPanel />
          <div className="data-surface overflow-auto">
            <Table>
              <TableHeader><TableRow><TableHead>时间</TableHead><TableHead>类型</TableHead><TableHead>变动</TableHead><TableHead>余额</TableHead><TableHead>备注</TableHead></TableRow></TableHeader>
              <TableBody>
                {data.ledger.length === 0 ? <TableRow><TableCell colSpan={5} className="h-28 text-center text-muted-foreground">暂无流水</TableCell></TableRow> : data.ledger.map((entry) => (
                  <TableRow key={entry.id}>
                    <TableCell>{date(entry.created_at)}</TableCell>
                    <TableCell>{entry.entry_type === "payment" ? "账单支付" : entry.entry_type === "refund" ? "退款" : entry.entry_type === "topup" ? "管理员充值" : "余额调整"}</TableCell>
                    <TableCell className={entry.delta_cents < 0 ? "text-destructive money" : "text-emerald-700 money"}>{entry.delta_cents > 0 ? "+" : ""}{money(entry.delta_cents)}</TableCell>
                    <TableCell className="money">{money(entry.balance_after)}</TableCell>
                    <TableCell>{entry.note || "-"}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        </>
      )}
    </Workspace>
  );
}

export function AdminCustomerContactsView({ customerId }: { customerId: number }) {
  return <AdminCustomerDetailView customerId={customerId} />;
}

function ApiKeysPanel() {
  const { data, error, refresh } = useResource<ApiKey[]>("/api-keys");
  const [name, setName] = useState("");
  const [expiresAt, setExpiresAt] = useState("");
  const [secret, setSecret] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  async function create(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setMessage("");
    try {
      const result = await api<{ secret: string }>("/api-keys", { method: "POST", body: JSON.stringify({ name, expires_at: expiresAt ? `${expiresAt}T23:59:59Z` : "" }) });
      setName("");
      setExpiresAt("");
      setSecret(result.secret);
      refresh();
    } catch (e) {
      setMessage((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function revoke(id: number) {
    setBusy(true);
    setMessage("");
    try {
      await api(`/api-keys/${id}`, { method: "DELETE", body: "{}" });
      refresh();
    } catch (e) {
      setMessage((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function copySecret() {
    if (!secret) return;
    await navigator.clipboard.writeText(secret);
    setMessage("密钥已复制，请立即保存");
  }
  return <section className="data-surface p-6 mb-6"><div className="surface-head"><div><h2 className="font-semibold flex items-center gap-2"><KeyRound size={17} />API 密钥</h2><p className="text-xs text-muted-foreground mt-1">用于自动化调用 API，创建后完整密钥只显示一次。</p></div></div><form className="grid md:grid-cols-[1fr_12rem_auto] gap-3 mt-4" onSubmit={create}><Input value={name} onChange={(event) => setName(event.target.value)} placeholder="密钥名称" aria-label="密钥名称" maxLength={120} required /><Input type="date" value={expiresAt} onChange={(event) => setExpiresAt(event.target.value)} aria-label="过期日期" /><Button disabled={busy || name.trim() === ""}>创建密钥</Button></form>{secret && <div className="mt-4 border border-amber-200 bg-amber-50 p-3 text-sm"><p className="font-medium">请复制并安全保存此密钥</p><div className="flex items-center gap-2 mt-2"><code className="break-all flex-1">{secret}</code><Button type="button" size="icon" variant="outline" aria-label="复制 API 密钥" title="复制" onClick={() => void copySecret()}><Copy size={15} /></Button></div></div>}{message && <p role="alert" className="text-sm text-destructive mt-3">{message}</p>}{error ? <p role="alert" className="text-sm text-destructive mt-4">{error.message}</p> : !data ? <Loading /> : <div className="overflow-auto mt-5"><Table><TableHeader><TableRow><TableHead>名称</TableHead><TableHead>前缀</TableHead><TableHead>最近使用</TableHead><TableHead>过期时间</TableHead><TableHead>状态</TableHead><TableHead /></TableRow></TableHeader><TableBody>{data.length === 0 ? <TableRow><TableCell colSpan={6} className="h-20 text-center text-muted-foreground">暂无密钥</TableCell></TableRow> : data.map((key) => <TableRow key={key.id}><TableCell>{key.name}</TableCell><TableCell className="font-mono text-xs">{key.prefix}</TableCell><TableCell>{key.last_used_at ? date(key.last_used_at) : "未使用"}</TableCell><TableCell>{key.expires_at ? date(key.expires_at) : "永不过期"}</TableCell><TableCell><Status value={key.revoked_at ? "revoked" : "active"} /></TableCell><TableCell>{!key.revoked_at && <Button size="sm" variant="destructive" disabled={busy} onClick={() => void revoke(key.id)}>撤销</Button>}</TableCell></TableRow>)}</TableBody></Table></div>}</section>;
}

function PasswordForm() {
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (newPassword.length < 8 || newPassword.length > 72) { setMessage("新密码需为 8-72 位"); return; }
    setBusy(true); setMessage("");
    try {
      await api("/account/password", { method: "PUT", body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }) });
      setCurrentPassword(""); setNewPassword(""); setMessage("密码已更新");
    } catch (e) { setMessage((e as Error).message); } finally { setBusy(false); }
  }
  return <section className="data-surface p-6 mb-6"><div className="surface-head"><div><h2 className="font-semibold">修改密码</h2><p className="text-xs text-muted-foreground mt-1">修改后其他登录设备会被退出。</p></div></div><form className="grid md:grid-cols-3 gap-3 mt-4" onSubmit={submit}><Input type="password" autoComplete="current-password" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} placeholder="当前密码" aria-label="当前密码" required /><Input type="password" autoComplete="new-password" value={newPassword} onChange={(event) => setNewPassword(event.target.value)} placeholder="新密码（8-72 位）" aria-label="新密码" minLength={8} maxLength={72} required /><div className="flex items-center gap-3"><Button disabled={busy}>更新密码</Button>{message && <span className="text-xs text-muted-foreground">{message}</span>}</div></form></section>;
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
export function AuditView() {
  return (
    <AdminListView
      kind="audit"
      title="系统日志"
      description="关键业务变更的不可变记录"
      endpoint="/admin/audit"
      columns={[
        {
          key: "created_at",
          label: "时间",
          render: (r) => date(String(r.created_at)),
        },
        { key: "action", label: "描述", render: (r) => <span className="block min-w-48 max-w-96 break-words text-sm">{String(r.action ?? "-")}</span> },
        { key: "actor_name", label: "操作者", render: (r) => <div><span className="block">{String(r.actor_name ?? "系统")}</span>{r.actor_id != null && <span className="text-xs text-muted-foreground">#{String(r.actor_id)}</span>}</div> },
        { key: "entity_type", label: "关联对象", render: (r) => <span className="text-sm">{String(r.entity_type ?? "-")}<span className="ml-2 font-mono text-xs text-muted-foreground">#{String(r.entity_id ?? "-")}</span></span> },
        { key: "id", label: "日志 ID", render: (r) => <span className="font-mono text-xs text-muted-foreground">{String(r.id)}</span> },
      ]}
    />
  );
}

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
