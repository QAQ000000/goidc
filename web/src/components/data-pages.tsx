"use client";
import { NOKVMServiceDelivery, serviceActionLabels, isPowerAction } from "./nokvm-delivery";
import { SupplierServiceDelivery } from "./supplier-delivery";
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
  MessageSquare,
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
  ContactRound,
  Tag,
  UserRoundCog,
  Save,
  Pencil,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "@/components/ui/dialog";
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
import { api, date, money, type ApiKey, type Contact, type CustomerGroup, type PromoCode, type Service, type TicketDepartment, type User } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Failure, Loading, Status } from "./common";
import { Workspace, PermissionGate, PermissionFields, useAdminPermission } from "./workspace";
import { MfaPanel } from "./mfa-panel";
import { ServiceNavigationSettings } from "./service-groups";
import { useServiceNavigation } from "@/hooks/use-service-navigation";
import type { ServicePage } from "@/lib/api";
import { catalogSelectStyle } from "./catalog-admin";

type Dashboard = {
  customers: number;
  active_products: number;
  pending_orders: number;
  unpaid_invoices: number;
  open_tickets: number;
  unpaid_cents: number;
};
export function DashboardView() {
  const { data, error, refresh } = useResource<Dashboard>("/admin/dashboard");
  return (
    <Workspace title="工作台" admin>
      <div className="flex justify-between items-start mb-8">
        <div>
          <h1 className="page-title">工作台</h1>
          <p className="text-muted-foreground mt-2">业务概况与待处理事项</p>
        </div>
        <Button variant="outline" asChild>
          <Link href="/">
            <ArrowUpRight size={16} />
            客户中心
          </Link>
        </Button>
      </div>
      {error ? (
        <Failure error={error} retry={refresh} />
      ) : !data ? (
        <Loading />
      ) : (
        <>
          <div className="stat-grid">
            <Stat icon={Users} label="客户" value={data.customers} />
            <Stat
              icon={Package}
              label="在售产品"
              value={data.active_products}
            />
            <Stat
              icon={ClipboardList}
              label="待处理订单"
              value={data.pending_orders}
            />
            <Stat
              icon={Receipt}
              label="待收账单"
              value={data.unpaid_invoices}
            />
          </div>
          <div className="grid md:grid-cols-2 gap-5">
            <section className="data-surface p-6">
              <div className="flex items-center gap-3 mb-5">
                <span className="w-9 h-9 grid place-items-center rounded-md bg-amber-50 text-amber-700">
                  <Receipt size={17} />
                </span>
                <div>
                  <h2 className="font-semibold">财务待办</h2>
                  <p className="text-xs text-muted-foreground mt-1">
                    未支付账单总额
                  </p>
                </div>
              </div>
              <strong className="text-3xl money">
                {money(data.unpaid_cents)}
              </strong>
              <Link
                className="block text-sm text-primary mt-5"
                href="/admin/invoices"
              >
                查看账单 <ArrowUpRight className="inline" size={14} />
              </Link>
            </section>
            <section className="data-surface p-6">
              <div className="flex items-center gap-3 mb-5">
                <span className="w-9 h-9 grid place-items-center rounded-md bg-blue-50 text-blue-700">
                  <MessageSquare size={17} />
                </span>
                <div>
                  <h2 className="font-semibold">支持工单</h2>
                  <p className="text-xs text-muted-foreground mt-1">
                    开放及等待回复
                  </p>
                </div>
              </div>
              <strong className="text-3xl">{data.open_tickets}</strong>
              <Link
                className="block text-sm text-primary mt-5"
                href="/admin/tickets"
              >
                进入工单 <ArrowUpRight className="inline" size={14} />
              </Link>
            </section>
          </div>
        </>
      )}
    </Workspace>
  );
}

type IncomeReport = { year: number; months: { month: string; income_cents: number; invoice_count: number }[]; total: { income_cents: number; invoice_count: number } };
type DailyReport = { year: number; month: number; days: { date: string; new_clients: number; new_orders: number; paid_orders: number; new_tickets: number; ticket_replies: number; cancellation_requests: number }[] };
type ProductIncomeReport = { year: number; month: number; products: { product_id: number; name: string; new_order_amount_cents: number; new_order_count: number; renewal_amount_cents: number; renewal_order_count: number; total_amount_cents: number }[] };
type CustomerRanking = { user_id: number; display_name: string; income_cents: number; expense_cents: number; net_cents: number }[];

export function ReportsView() {
  const now = new Date();
  const [year, setYear] = useState(String(now.getFullYear()));
  const [month, setMonth] = useState(String(now.getMonth() + 1));
  const [period, setPeriod] = useState({ year: now.getFullYear(), month: now.getMonth() + 1 });
  const income = useResource<IncomeReport>(`/admin/reports/income?year=${period.year}`);
  const daily = useResource<DailyReport>(`/admin/reports/new-clients?year=${period.year}&month=${period.month}`);
  const products = useResource<ProductIncomeReport>(`/admin/reports/product-income?year=${period.year}&month=${period.month}`);
  const ranking = useResource<CustomerRanking>("/admin/reports/customer-ranking");
  const error = income.error ?? daily.error ?? products.error ?? ranking.error;
  const refresh = () => { income.refresh(); daily.refresh(); products.refresh(); ranking.refresh(); };
  return (
    <Workspace title="统计报表" admin>
      <PageHeading title="统计报表" description="收入、客户、产品和服务运营数据">
        <div className="flex items-center gap-2">
          <Input className="w-24" type="number" min={2000} max={now.getFullYear() + 1} value={year} onChange={(event) => setYear(event.target.value)} aria-label="统计年份" />
          <Select value={month} onValueChange={setMonth}><SelectTrigger className="w-28" aria-label="统计月份"><SelectValue /></SelectTrigger><SelectContent>{Array.from({ length: 12 }, (_, index) => <SelectItem key={index + 1} value={String(index + 1)}>{index + 1} 月</SelectItem>)}</SelectContent></Select>
          <Button onClick={() => setPeriod({ year: Number(year), month: Number(month) })}>查询</Button>
        </div>
      </PageHeading>
      {error ? <Failure error={error} retry={refresh} /> : !income.data || !daily.data || !products.data || !ranking.data ? <Loading /> : (
        <>
          <div className="stat-grid mb-6"><Stat icon={Receipt} label={`${period.year} 年收入`} value={money(income.data.total.income_cents)} /><Stat icon={ClipboardList} label="已支付账单" value={income.data.total.invoice_count} /><Stat icon={Users} label={`${period.month} 月新客户`} value={daily.data.days.reduce((total, item) => total + item.new_clients, 0)} /><Stat icon={Package} label={`${period.month} 月产品收入`} value={money(products.data.products.reduce((total, item) => total + item.total_amount_cents, 0))} /></div>
          <div className="grid xl:grid-cols-2 gap-5">
            <section className="data-surface p-6"><div className="surface-head"><div><h2 className="font-semibold">月度收入</h2><p className="text-xs text-muted-foreground mt-1">按支付时间统计</p></div></div><div className="overflow-auto mt-4"><Table><TableHeader><TableRow><TableHead>月份</TableHead><TableHead>收入</TableHead><TableHead>账单数</TableHead></TableRow></TableHeader><TableBody>{income.data.months.length === 0 ? <TableRow><TableCell colSpan={3} className="h-20 text-center text-muted-foreground">暂无收入</TableCell></TableRow> : income.data.months.map((item) => <TableRow key={item.month}><TableCell>{item.month}</TableCell><TableCell className="money">{money(item.income_cents)}</TableCell><TableCell>{item.invoice_count}</TableCell></TableRow>)}</TableBody></Table></div></section>
            <section className="data-surface p-6"><div className="surface-head"><div><h2 className="font-semibold">客户收入排名</h2><p className="text-xs text-muted-foreground mt-1">按净流水排序，展示前十名</p></div></div><div className="overflow-auto mt-4"><Table><TableHeader><TableRow><TableHead>客户</TableHead><TableHead>收入</TableHead><TableHead>支出</TableHead><TableHead>净额</TableHead></TableRow></TableHeader><TableBody>{ranking.data.length === 0 ? <TableRow><TableCell colSpan={4} className="h-20 text-center text-muted-foreground">暂无流水</TableCell></TableRow> : ranking.data.map((item) => <TableRow key={item.user_id}><TableCell>{item.display_name}</TableCell><TableCell className="money">{money(item.income_cents)}</TableCell><TableCell className="money">{money(item.expense_cents)}</TableCell><TableCell className="money">{money(item.net_cents)}</TableCell></TableRow>)}</TableBody></Table></div></section>
            <section className="data-surface p-6 xl:col-span-2"><div className="surface-head"><div><h2 className="font-semibold">产品收入</h2><p className="text-xs text-muted-foreground mt-1">{period.year} 年 {period.month} 月已支付新购和续费</p></div></div><div className="overflow-auto mt-4"><Table><TableHeader><TableRow><TableHead>产品</TableHead><TableHead>新购收入</TableHead><TableHead>新购订单</TableHead><TableHead>续费收入</TableHead><TableHead>续费订单</TableHead><TableHead>合计</TableHead></TableRow></TableHeader><TableBody>{products.data.products.length === 0 ? <TableRow><TableCell colSpan={6} className="h-20 text-center text-muted-foreground">暂无产品收入</TableCell></TableRow> : products.data.products.map((item) => <TableRow key={item.product_id}><TableCell>{item.name}</TableCell><TableCell className="money">{money(item.new_order_amount_cents)}</TableCell><TableCell>{item.new_order_count}</TableCell><TableCell className="money">{money(item.renewal_amount_cents)}</TableCell><TableCell>{item.renewal_order_count}</TableCell><TableCell className="money font-medium">{money(item.total_amount_cents)}</TableCell></TableRow>)}</TableBody></Table></div></section>
            <section className="data-surface p-6 xl:col-span-2"><div className="surface-head"><div><h2 className="font-semibold">每日运营概览</h2><p className="text-xs text-muted-foreground mt-1">新客户、订单、工单和取消申请</p></div></div><div className="overflow-auto mt-4"><Table><TableHeader><TableRow><TableHead>日期</TableHead><TableHead>新客户</TableHead><TableHead>新订单</TableHead><TableHead>已支付订单</TableHead><TableHead>新工单</TableHead><TableHead>工单回复</TableHead><TableHead>取消申请</TableHead></TableRow></TableHeader><TableBody>{daily.data.days.map((item) => <TableRow key={item.date}><TableCell>{item.date}</TableCell><TableCell>{item.new_clients}</TableCell><TableCell>{item.new_orders}</TableCell><TableCell>{item.paid_orders}</TableCell><TableCell>{item.new_tickets}</TableCell><TableCell>{item.ticket_replies}</TableCell><TableCell>{item.cancellation_requests}</TableCell></TableRow>)}</TableBody></Table></div></section>
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
  return (
    <AdminListView
      kind="customers"
      title="客户管理"
      description="客户账号与余额"
      endpoint="/admin/customers"
      actions={<Button variant="outline" asChild><Link href="/admin/customer-groups"><Users size={16} />管理客户分组</Link></Button>}
      columns={[
        { key: "username", label: "账号" },
        { key: "display_name", label: "姓名" },
        { key: "email", label: "邮箱" },
        { key: "group_name", label: "客户分组", render: (r) => String(r.group_name ?? "未分组") },
        { key: "sales_owner_name", label: "销售归属", render: (r) => String(r.sales_owner_name ?? "无归属") },
        {
          key: "balance_cents",
          label: "余额",
          render: (r) => (
            <span className="money">{money(Number(r.balance_cents))}</span>
          ),
        },
        { key: "status", label: "状态" },
        { key: "actions", label: "操作", render: (r, refresh, returnQuery) => <CustomerActions row={r} refresh={refresh} returnQuery={returnQuery ? `?${returnQuery}` : ""} /> },
      ]}
    />
  );
}

export function CustomerGroupsView() {
  const { data, error, refresh } = useResource<CustomerGroup[]>("/admin/customer-groups");
  const [name, setName] = useState("");
  const [color, setColor] = useState("#64748b");
  const [discount, setDiscount] = useState("0");
  const [separateInvoices, setSeparateInvoices] = useState(false);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  async function create(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault(); setBusy(true); setMessage("");
    try { await api("/admin/customer-groups", { method: "POST", body: JSON.stringify({ name, color, discount_percent: Number(discount), separate_invoices: separateInvoices }) }); setName(""); setDiscount("0"); setSeparateInvoices(false); refresh(); }
    catch (e) { setMessage((e as Error).message); } finally { setBusy(false); }
  }
  async function remove(id: number) {
    setBusy(true); setMessage("");
    try { await api(`/admin/customer-groups/${id}`, { method: "DELETE", body: "{}" }); refresh(); }
    catch (e) { setMessage((e as Error).message); } finally { setBusy(false); }
  }
  return <Workspace title="客户分组" admin><PageHeading title="客户分组" description="设置客户折扣和账单属性"><Button variant="outline" asChild><Link href="/admin/customers"><ArrowUpRight size={16} />返回客户管理</Link></Button></PageHeading><PermissionGate permission="customer_groups.manage"><section className="data-surface p-6 mb-6"><form className="grid md:grid-cols-[1fr_5rem_7rem_auto_auto] gap-3 items-center" onSubmit={create}><Input value={name} onChange={(e) => setName(e.target.value)} placeholder="分组名称" aria-label="分组名称" required maxLength={120} /><Input type="color" value={color} onChange={(e) => setColor(e.target.value)} aria-label="分组颜色" className="p-1 h-9" /><Input type="number" min="0" max="100" value={discount} onChange={(e) => setDiscount(e.target.value)} placeholder="折扣 %" aria-label="折扣百分比" /><label className="flex items-center gap-2 text-sm whitespace-nowrap"><Checkbox checked={separateInvoices} onCheckedChange={(v) => setSeparateInvoices(v === true)} />拆分账单</label><Button disabled={busy || !name.trim()}><Plus size={16} />新增分组</Button></form>{message && <p role="alert" className="text-sm text-destructive mt-3">{message}</p>}</section></PermissionGate>{error ? <Failure error={error} retry={refresh} /> : !data ? <Loading /> : <div className="data-surface overflow-auto"><Table><TableHeader><TableRow><TableHead>名称</TableHead><TableHead>折扣</TableHead><TableHead>账单</TableHead><TableHead>客户数</TableHead><TableHead /></TableRow></TableHeader><TableBody>{data.length === 0 ? <TableRow><TableCell colSpan={5} className="h-28 text-center text-muted-foreground">暂无分组</TableCell></TableRow> : data.map((group) => <TableRow key={group.id}><TableCell><span className="inline-block w-3 h-3 rounded-sm mr-2" style={{ backgroundColor: group.color }} />{group.name}</TableCell><TableCell>{group.discount_percent}%</TableCell><TableCell>{group.separate_invoices ? "拆分" : "合并"}</TableCell><TableCell>{group.member_count}</TableCell><TableCell><PermissionGate permission="customer_groups.manage"><Button size="icon-sm" variant="ghost" aria-label={`删除${group.name}`} title="删除分组" disabled={busy || group.member_count > 0} onClick={() => void remove(group.id)}><Trash2 size={15} /></Button></PermissionGate></TableCell></TableRow>)}</TableBody></Table></div>}</Workspace>;
}

export function PromoCodesView() {
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
  return <Workspace title="优惠码" admin><PageHeading title="优惠码" description="配置购买折扣、循环续费和升级优惠"><p className="text-xs text-muted-foreground max-w-sm">升级专用优惠只在商品升级差额上生效，不影响首购、续费或降级返还。</p></PageHeading><PermissionGate permission="promos.manage"><section className="data-surface p-6 mb-6"><form className="grid md:grid-cols-[1fr_8rem_7rem_7rem_auto] gap-3 items-center" onSubmit={create}><Input value={code} onChange={(e) => setCode(e.target.value.toUpperCase())} placeholder="优惠码" aria-label="优惠码" maxLength={50} required /><Select value={discountType} onValueChange={(v) => { setDiscountType(v as typeof discountType); setRecurring(false); setRecurringLimit("0"); }}><SelectTrigger aria-label="优惠类型"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="percent">百分比</SelectItem><SelectItem value="fixed">固定金额（分）</SelectItem><SelectItem value="free">免初装费</SelectItem></SelectContent></Select><Input type="number" min={discountType === "free" ? 0 : 1} max={discountType === "percent" ? 100 : undefined} value={value} onChange={(e) => setValue(e.target.value)} aria-label="优惠值" disabled={discountType === "free"} /><Input type="number" min="0" value={maxUses} onChange={(e) => setMaxUses(e.target.value)} aria-label="最大发码次数" placeholder="不限次数" /><Button disabled={busy || !code.trim()}><Plus size={16} />新增</Button><label className="flex items-center gap-2 text-sm"><Checkbox checked={oncePerCustomer} onCheckedChange={(v) => setOncePerCustomer(v === true)} />每客户一次</label><label className="flex items-center gap-2 text-sm"><Checkbox checked={onlyNewCustomer} onCheckedChange={(v) => setOnlyNewCustomer(v === true)} />仅新客户</label><label className="flex items-center gap-2 text-sm md:col-span-3"><Checkbox checked={recurring} disabled={discountType === "free" || upgradeOnly || busy} onCheckedChange={(v) => setRecurring(v === true)} />循环续费优惠</label>{recurring&&<label className="grid gap-2 text-sm md:col-span-3">每件服务优惠续费次数（0为不限）<Input type="number" min={0} max={10000} required disabled={busy} value={recurringLimit} onChange={e=>setRecurringLimit(e.target.value)} aria-label="优惠续费次数"/></label>}<label className="flex items-center gap-2 text-sm md:col-span-3"><Checkbox checked={upgradeOnly} disabled={recurring || discountType === "free" || busy} onCheckedChange={(v) => { setUpgradeOnly(v === true); setRecurring(false); }} />仅用于商品升级</label></form>{message && <p role="alert" className="text-sm text-destructive mt-3">{message}</p>}</section></PermissionGate>{error ? <Failure error={error} retry={refresh} /> : !data ? <Loading /> : <div className="data-surface overflow-auto"><Table><TableHeader><TableRow><TableHead>代码</TableHead><TableHead>类型</TableHead><TableHead>值</TableHead><TableHead>发码次数</TableHead><TableHead>状态</TableHead><TableHead /></TableRow></TableHeader><TableBody>{data.length === 0 ? <TableRow><TableCell colSpan={6} className="h-28 text-center text-muted-foreground">暂无优惠码</TableCell></TableRow> : data.map((item) => <TableRow key={item.id}><TableCell className="font-mono"><Tag size={14} className="inline mr-2" />{item.code}{item.recurring && <span className="block text-xs text-muted-foreground mt-1 font-sans">循环续费 · {item.recurring_limit>0?`${item.recurring_limit} 次 / 件`:"不限次数"}</span>}{item.upgrade_only && <span className="block text-xs text-muted-foreground mt-1 font-sans">仅升级</span>}</TableCell><TableCell>{item.discount_type === "percent" ? "百分比" : item.discount_type === "fixed" ? "固定金额" : "免初装费"}</TableCell><TableCell>{item.discount_type === "percent" ? `${item.value}%` : item.discount_type === "fixed" ? money(item.value) : "初装费"}</TableCell><TableCell>{item.used_count}{item.max_uses > 0 ? ` / ${item.max_uses}` : " / 不限"}</TableCell><TableCell><Status value={item.active ? "active" : "disabled"} /></TableCell><TableCell><PermissionGate permission="promos.manage"><Button size="sm" variant="outline" disabled={busy} onClick={() => void toggle(item)}>{item.active ? "停用" : "启用"}</Button></PermissionGate></TableCell></TableRow>)}</TableBody></Table></div>}</Workspace>;
}

function CustomerBalanceAction({ row, refresh, onBusyChange }: { row: Row; refresh: () => void; onBusyChange: (busy: boolean) => void }) {
  const [amount, setAmount] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const cents = Math.round(Number(amount) * 100);
    if (!Number.isSafeInteger(cents) || cents < 1) {
      setMessage("金额无效");
      return;
    }
    setBusy(true);
    onBusyChange(true);
    setMessage("");
    try {
      await api(`/admin/customers/${Number(row.id)}/balance`, {
        method: "POST",
        body: JSON.stringify({ amount_cents: cents, note: "管理台充值" }),
        headers: { "Idempotency-Key": `web-topup-${row.id}-${crypto.randomUUID()}` },
      });
      setAmount("");
      setMessage("已充值");
      refresh();
    } catch (e) {
      setMessage((e as Error).message);
    } finally {
      setBusy(false);
      onBusyChange(false);
    }
  }
  return (
    <form className="flex flex-wrap items-center gap-2" onSubmit={submit}>
      <Input
        className="flex-1 min-w-0"
        type="number"
        min="0.01"
        step="0.01"
        value={amount}
        onChange={(event) => setAmount(event.target.value)}
        placeholder="金额"
        aria-label="充值金额（元）"
        disabled={busy}
      />
      <Button size="sm" variant="outline" disabled={busy}>
        充值
      </Button>
      {message && <p role="status" className="w-full text-sm break-words">{message}</p>}
    </form>
  );
}

function CustomerActionDialog({ row, title, icon: Icon, children }: { row: Row; title: string; icon: typeof Users; children: (onBusyChange: (busy: boolean) => void) => React.ReactNode }) {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  return <><Button size="icon-sm" variant="outline" title={title} aria-label={`${title} ${row.username}`} onClick={() => setOpen(true)}><Icon /></Button><Dialog open={open} onOpenChange={value => { if (!busy) setOpen(value); }}><DialogContent className="grid-cols-[minmax(0,1fr)]"><DialogHeader><DialogTitle>{title}</DialogTitle><DialogDescription className="break-all">{String(row.display_name)}</DialogDescription></DialogHeader>{open && children(setBusy)}</DialogContent></Dialog></>;
}

function CustomerActions({ row, refresh, returnQuery }: { row: Row; refresh: () => void; returnQuery: string }) {
  const detailHref = `/admin/customers/${Number(row.id)}${returnQuery}`;
  return (
    <div className="flex items-center gap-2">
      <Button size="sm" variant="outline" asChild><Link href={detailHref}><ContactRound size={14} />联系人</Link></Button>
      <PermissionGate permission="customer_files.view"><Button size="sm" variant="outline" asChild><Link href={`/admin/customers/${Number(row.id)}/files`}>客户文件</Link></Button></PermissionGate>
      <PermissionGate permission="@super"><CustomerSalesAction row={row} refresh={refresh} /></PermissionGate>
      <PermissionGate permission="customers.manage"><CustomerActionDialog row={row} title="编辑客户" icon={Pencil}>{onBusyChange => <CustomerProfileAction row={row} refresh={refresh} onBusyChange={onBusyChange} />}</CustomerActionDialog><CustomerActionDialog row={row} title="重置密码" icon={KeyRound}>{onBusyChange => <CustomerPasswordAction row={row} onBusyChange={onBusyChange} />}</CustomerActionDialog></PermissionGate>
      <PermissionGate permission="balance.manage"><CustomerActionDialog row={row} title="余额充值" icon={CreditCard}>{onBusyChange => <CustomerBalanceAction row={row} refresh={refresh} onBusyChange={onBusyChange} />}</CustomerActionDialog></PermissionGate>
    </div>
  );
}

type SalesOwner = { id: number; username: string; display_name: string };
function CustomerSalesAction({ row, refresh }: { row: Row; refresh: () => void }) {
  const [open, setOpen] = useState(false);
  const [owner, setOwner] = useState("none");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const directory = useResource<SalesOwner[]>(open ? "/admin/sales-owners" : null, false);
  const currentOwner = row.sales_owner_id == null ? "none" : String(row.sales_owner_id);
  const historicalOwner = currentOwner !== "none" && directory.data && !directory.data.some(item => String(item.id) === currentOwner);
  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault(); if (busy || owner === currentOwner) return;
    setBusy(true); setError("");
    try {
      await api(`/admin/customers/${Number(row.id)}/sales-owner`, { method: "PUT", body: JSON.stringify({ sales_owner_id: owner === "none" ? null : Number(owner), expected_version: Number(row.sales_version) }) });
      setOpen(false); refresh();
    } catch (e) { setError((e as Error).message); refresh(); }
    finally { setBusy(false); }
  }
  return <>
    <Button size="icon-sm" variant="outline" title="调整销售归属" aria-label={`调整 ${row.display_name} 的销售归属`} onClick={() => { setOwner(currentOwner); setError(""); setOpen(true); }}><UserRoundCog /></Button>
    <Dialog open={open} onOpenChange={value => { if (!busy) setOpen(value); }}><DialogContent className="grid-cols-[minmax(0,1fr)]"><DialogHeader><DialogTitle>销售归属</DialogTitle><DialogDescription className="break-all">{String(row.display_name)}</DialogDescription></DialogHeader>
      {directory.error ? <Failure error={directory.error} retry={directory.refresh} /> : !directory.data ? <Loading /> : <form onSubmit={save} className="space-y-4"><Select value={owner} onValueChange={setOwner} disabled={busy}><SelectTrigger className="w-full" aria-label="销售管理员"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="none">无归属</SelectItem>{historicalOwner && <SelectItem value={currentOwner} disabled>{String(row.sales_owner_name)}（不可新分配）</SelectItem>}{directory.data.map(item => <SelectItem key={item.id} value={String(item.id)}>{item.display_name} · {item.username}</SelectItem>)}</SelectContent></Select>
        {error && <p role="alert" className="text-destructive text-sm">{error}</p>}<div className="flex justify-end"><Button disabled={busy || owner === currentOwner}><Save />{busy ? "保存中" : "保存归属"}</Button></div>
      </form>}
    </DialogContent></Dialog>
  </>;
}

function CustomerProfileAction({ row, refresh, onBusyChange }: { row: Row; refresh: () => void; onBusyChange: (busy: boolean) => void }) {
  const canAssignGroup = useAdminPermission("customer_groups.manage");
  const [displayName, setDisplayName] = useState(String(row.display_name ?? ""));
  const [email, setEmail] = useState(String(row.email ?? ""));
  const [status, setStatus] = useState(String(row.status ?? "active"));
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const { data: groups } = useResource<CustomerGroup[]>("/admin/customer-groups");
  const [groupID, setGroupID] = useState(String(row.group_id ?? "none"));
  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    onBusyChange(true);
    setMessage("");
    try {
      await api(`/admin/customers/${Number(row.id)}`, { method: "PATCH", body: JSON.stringify({ display_name: displayName, email, status }) });
      if (canAssignGroup) await api(`/admin/customers/${Number(row.id)}/group`, { method: "PUT", body: JSON.stringify({ group_id: groupID === "none" ? null : Number(groupID) }) });
      setMessage("已保存");
      refresh();
    } catch (e) {
      setMessage((e as Error).message);
    } finally {
      setBusy(false);
      onBusyChange(false);
    }
  }
  return <form className="space-y-4 min-w-0" onSubmit={submit}><fieldset disabled={busy} className="space-y-4 min-w-0">
    <label className="grid gap-2 text-sm">姓名<Input value={displayName} onChange={(event) => setDisplayName(event.target.value)} aria-label="客户姓名" maxLength={120} required /></label>
    <label className="grid gap-2 text-sm">邮箱<Input type="email" value={email} onChange={(event) => setEmail(event.target.value)} aria-label="客户邮箱" maxLength={254} required /></label>
    <Select value={status} onValueChange={setStatus}><SelectTrigger className="w-full" aria-label="客户状态"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="active">启用</SelectItem><SelectItem value="disabled">停用</SelectItem></SelectContent></Select>
    {canAssignGroup && <Select value={groupID} onValueChange={setGroupID}><SelectTrigger className="w-full" aria-label="客户分组"><SelectValue placeholder="客户分组" /></SelectTrigger><SelectContent><SelectItem value="none">未分组</SelectItem>{groups?.map((group) => <SelectItem key={group.id} value={String(group.id)}>{group.name}</SelectItem>)}</SelectContent></Select>}
    <div className="flex justify-end"><Button disabled={busy}><Save />保存资料</Button></div>
    {message && <p role="status" className="text-sm break-words">{message}</p>}
  </fieldset>
  </form>;
}

function CustomerPasswordAction({ row, onBusyChange }: { row: Row; onBusyChange: (busy: boolean) => void }) {
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (password.length < 8 || password.length > 72) {
      setMessage("密码需为 8-72 位");
      return;
    }
    setBusy(true);
    onBusyChange(true);
    setMessage("");
    try {
      await api(`/admin/customers/${Number(row.id)}/password`, {
        method: "POST",
        body: JSON.stringify({ password }),
      });
      setPassword("");
      setMessage("已重置");
    } catch (e) {
      setMessage((e as Error).message);
    } finally {
      setBusy(false);
      onBusyChange(false);
    }
  }
  return (
    <form className="flex flex-wrap items-center gap-2" onSubmit={submit}>
      <Input
        className="flex-1 min-w-0"
        type="password"
        minLength={8}
        maxLength={72}
        value={password}
        onChange={(event) => setPassword(event.target.value)}
        placeholder="新密码"
        aria-label="客户新密码"
        disabled={busy}
      />
      <Button size="sm" variant="outline" disabled={busy}>重置密码</Button>
      {message && <p role="status" className="w-full text-sm break-words">{message}</p>}
    </form>
  );
}

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
        <SearchForm
          value={draft}
          setValue={setDraft}
          submit={() => {setQuery(draft);setPage(0)}}
          placeholder="搜索记录"
        />
      )}
      {kind === "tickets" && admin && <div className="mb-5 max-w-xs"><Select value={department} onValueChange={value => { setDepartment(value); setPage(0); }}><SelectTrigger aria-label="筛选工单部门" className="w-full"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="all">全部授权部门</SelectItem>{currentUser.data?.ticket_unassigned && <SelectItem value="0">未指定部门</SelectItem>}{directory.data?.map(item => <SelectItem key={item.id} value={String(item.id)}>{item.name}{item.active === false ? "（已停用）" : ""}</SelectItem>)}</SelectContent></Select>{directory.error && <Failure error={directory.error} retry={directory.refresh} />}</div>}
      {error ? (
        <Failure error={error} retry={refresh} />
      ) : !data ? (
        <Loading />
      ) : (
        <div className="data-surface overflow-auto">
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
    <div className="flex flex-wrap justify-between items-start gap-4 mb-8">
      <div>
        <h1 className="page-title">{title}</h1>
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
  if (admin) return <AdminOrdersView />;
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

function AdminOrdersView() {
  const [query, setQuery] = useState("");
  const [draft, setDraft] = useState("");
  const [status, setStatus] = useState("all");
  const [page, setPage] = useState(0);
  const [selected, setSelected] = useState<number[]>([]);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const params = new URLSearchParams({ q: query, limit: "25", offset: String(page * 25) });
  if (status !== "all") params.set("status", status);
  const { data, error, refresh } = useResource<Row[]>(`/admin/orders?${params.toString()}`);
  const cancellable = (data ?? []).filter((row) => String(row.status) === "pending").map((row) => Number(row.id));
  const visibleSelected = selected.filter((id) => cancellable.includes(id));
  const allSelected = cancellable.length > 0 && cancellable.every((id) => selected.includes(id));

  function toggle(id: number, checked: boolean) {
    setSelected((current) => checked ? [...new Set([...current, id])] : current.filter((value) => value !== id));
  }
  async function bulkCancel() {
    if (visibleSelected.length === 0) return;
    setBusy(true); setMessage("");
    try {
      const result = await api<{ succeeded: number; failed: number }>("/admin/orders/bulk-cancel", {
        method: "POST",
        body: JSON.stringify({ order_ids: visibleSelected }),
        headers: { "Idempotency-Key": `web-order-bulk-cancel-${crypto.randomUUID()}` },
      });
      setMessage(`已处理 ${result.succeeded} 个订单${result.failed ? `，${result.failed} 个未取消` : ""}`);
      setSelected([]); refresh();
    } catch (e) { setMessage((e as Error).message); }
    finally { setBusy(false); }
  }
  return (
    <Workspace title="订单管理" admin>
      <PageHeading title="订单管理" description="筛选订单并批量处理待支付订单">
        <PermissionGate permission="orders.manage"><Button variant="destructive" disabled={busy || visibleSelected.length === 0} onClick={() => void bulkCancel()}><Ban size={15} />批量取消 {visibleSelected.length ? `(${visibleSelected.length})` : ""}</Button></PermissionGate>
      </PageHeading>
      <div className="flex flex-wrap items-center gap-3 mb-6">
        <SearchForm value={draft} setValue={setDraft} submit={() => { setQuery(draft); setPage(0); }} placeholder="订单号、客户名称或邮箱" />
        <Select value={status} onValueChange={(value) => { setStatus(value); setPage(0); }}>
          <SelectTrigger className="w-36" aria-label="订单状态"><SelectValue placeholder="订单状态" /></SelectTrigger>
          <SelectContent><SelectItem value="all">全部状态</SelectItem><SelectItem value="pending">待支付</SelectItem><SelectItem value="paid">已支付</SelectItem><SelectItem value="active">已启用</SelectItem><SelectItem value="cancelled">已取消</SelectItem><SelectItem value="refunded">已退款</SelectItem></SelectContent>
        </Select>
        {message && <span className="text-sm text-muted-foreground" role="status">{message}</span>}
      </div>
      {error ? <Failure error={error} retry={refresh} /> : !data ? <Loading /> : (
        <div className="data-surface overflow-auto">
          <Table>
            <TableHeader><TableRow><TableHead className="w-12"><PermissionGate permission="orders.manage"><Checkbox checked={allSelected} onCheckedChange={(checked) => setSelected(checked ? cancellable : [])} aria-label="选择待支付订单" /></PermissionGate></TableHead><TableHead>订单号</TableHead><TableHead>客户</TableHead><TableHead>金额</TableHead><TableHead>创建时间</TableHead><TableHead>订单状态</TableHead><TableHead>审核</TableHead><TableHead>操作</TableHead></TableRow></TableHeader>
            <TableBody>
              {data.length === 0 && <TableRow><TableCell colSpan={8} className="h-32 text-center text-muted-foreground">暂无记录</TableCell></TableRow>}
              {data.map((row, i) => { const id = Number(row.id); const pending = String(row.status) === "pending"; return <TableRow key={String(row.id ?? i)}><TableCell><PermissionGate permission="orders.manage"><Checkbox checked={selected.includes(id)} disabled={!pending} onCheckedChange={(checked) => toggle(id, checked === true)} aria-label={`选择订单 ${String(row.order_no ?? id)}`} /></PermissionGate></TableCell><TableCell className="font-mono text-xs"><Link className="text-primary hover:underline" href={`/admin/orders/${id}`}>{String(row.order_no ?? "-")}</Link></TableCell><TableCell>{String(row.customer_name ?? "-")}</TableCell><TableCell><span className="money">{money(Number(row.total_cents))}</span></TableCell><TableCell>{date(String(row.created_at))}</TableCell><TableCell><Status value={String(row.status)} /></TableCell><TableCell><Status value={String(row.review_status ?? "approved")} /></TableCell><TableCell><div className="flex flex-wrap items-center gap-2"><OrderReviewAction row={row} refresh={refresh} /><OrderCancelAction row={row} admin /></div></TableCell></TableRow>; })}
            </TableBody>
          </Table>
          <div className="flex items-center justify-end gap-4 border-t p-3"><span className="text-xs text-muted-foreground">第 {page + 1} 页</span><Button variant="outline" size="sm" disabled={page === 0} onClick={() => setPage((v) => v - 1)}>上一页</Button><Button variant="outline" size="sm" disabled={data.length < 25} onClick={() => setPage((v) => v + 1)}>下一页</Button></div>
        </div>
      )}
    </Workspace>
  );
}

function OrderCancelAction({ row, admin }: { row: Row; admin: boolean }) {
  const allowed = useAdminPermission("orders.manage");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  if ((admin && !allowed) || String(row.status) !== "pending") return <span className="text-xs text-muted-foreground">-</span>;
  async function cancel() {
    setBusy(true); setMessage("");
    try {
      await api(`${admin ? "/admin" : ""}/orders/${Number(row.id)}/cancel`, { method: "POST", body: "{}", headers: { "Idempotency-Key": `web-order-cancel-${row.id}-${crypto.randomUUID()}` } });
      setMessage("已取消");
    } catch (e) { setMessage((e as Error).message); } finally { setBusy(false); }
  }
  return <div className="flex items-center gap-2"><Button size="sm" variant="destructive" disabled={busy} onClick={cancel}><Ban size={14} />取消订单</Button>{message && <span className="text-xs text-muted-foreground whitespace-nowrap">{message}</span>}</div>;
}

function OrderReviewAction({ row, refresh }: { row: Row; refresh: () => void }) {
  const allowed = useAdminPermission("orders.manage");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  if (!allowed || String(row.review_status) !== "pending") return null;
  async function decide(decision: "approved" | "rejected") {
    setBusy(true); setMessage("");
    try {
      await api(`/admin/orders/${Number(row.id)}/review`, { method: "POST", body: JSON.stringify({ decision, note: decision === "approved" ? "管理员审核通过" : "管理员审核驳回" }), headers: { "Idempotency-Key": `web-order-review-${row.id}-${decision}-${crypto.randomUUID()}` } });
      setMessage(decision === "approved" ? "已通过" : "已驳回"); refresh();
    } catch (e) { setMessage((e as Error).message); } finally { setBusy(false); }
  }
  return <div className="flex flex-wrap items-center gap-2"><Button size="sm" disabled={busy} onClick={() => void decide("approved")}>通过</Button><Button size="sm" variant="outline" disabled={busy} onClick={() => void decide("rejected")}>驳回</Button>{message && <span className="text-xs text-muted-foreground whitespace-nowrap">{message}</span>}</div>;
}

function OrderConfigurationSnapshot({item}:{item:Row}){
 const snapshot=Array.isArray(item.config_snapshot)?item.config_snapshot as Row[]:[];
 return <div className="text-xs text-muted-foreground mt-2 space-y-1">{snapshot.map(option=><p className="whitespace-normal break-all" key={String(option.id)}>{String(option.group_name)}：{String(option.label)}（{String(option.value)}）{Number(option.quantity??1)>1?` × ${option.quantity}`:""} · 周期增减 {money(Number(option.price_delta_cents))} · 初装增减 {money(Number(option.setup_delta_cents))}</p>)}{snapshot.length===0&&Array.isArray(item.config_options)&&item.config_options.length>0&&<p>历史配置编号：{item.config_options.join("、")}；未保留名称快照。</p>}</div>;
}
export function OrderDetailView({ id }: { id: number }) {
  const { data, error, refresh } = useResource<Row>(`/admin/orders/${id}`);
  const [note, setNote] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState("");
  async function saveNotes(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSaving(true); setMessage("");
    try {
      await api(`/admin/orders/${id}/notes`, { method: "POST", body: JSON.stringify({ notes: note ?? String(data?.notes ?? "") }) });
      setMessage("备注已保存"); refresh();
    } catch (e) { setMessage((e as Error).message); } finally { setSaving(false); }
  }
  return <Workspace title="订单详情" admin><Button variant="ghost" className="mb-5" asChild><Link href="/admin/orders"><ArrowUpRight size={16} />返回订单列表</Link></Button>{error ? <Failure error={error} retry={refresh} /> : !data ? <Loading /> : <><PageHeading title={String(data.order_no)} description={`客户：${String(data.display_name)} · 创建于 ${date(String(data.created_at))}`}><div className="flex items-center gap-2"><Status value={String(data.status)} /><Status value={String(data.review_status ?? "approved")} /><OrderReviewAction row={data} refresh={refresh} /></div></PageHeading><div className="grid md:grid-cols-3 gap-5 mb-6"><section className="data-surface p-5"><p className="text-xs text-muted-foreground">订单金额</p><p className="money text-xl font-semibold mt-2">{money(Number(data.total_cents))}</p></section><section className="data-surface p-5"><p className="text-xs text-muted-foreground">账单</p><p className="mt-2">{String(data.invoice_no)} · <Status value={String(data.invoice_status)} /></p></section><section className="data-surface p-5"><p className="text-xs text-muted-foreground">客户邮箱</p><p className="mt-2 break-all">{String(data.email)}</p></section></div><div className="grid xl:grid-cols-2 gap-5"><section className="data-surface p-6"><h2 className="font-semibold">订单项目</h2><div className="overflow-auto mt-4"><Table><TableHeader><TableRow><TableHead>产品</TableHead><TableHead>账期</TableHead><TableHead>数量</TableHead><TableHead>金额</TableHead></TableRow></TableHeader><TableBody>{(data.items as Row[]).map((item) => <TableRow key={String(item.id)}><TableCell>{String(item.product_name)}<OrderConfigurationSnapshot item={item}/><RenewalBenefitDetails terms={{renewal_discount_percent:Number(item.renewal_discount_percent),renewal_fixed_discount_cents:Number(item.renewal_fixed_discount_cents),renewal_discount_limit:Number(item.renewal_discount_limit),renewal_promo_code:String(item.renewal_promo_code??"")}}/><PurchaseFieldDetails fields={Array.isArray(item.purchase_fields)?item.purchase_fields as PurchaseFieldSnapshot[]:[]}/></TableCell><TableCell>{String(item.billing_cycle)}</TableCell><TableCell>{String(item.quantity)}</TableCell><TableCell className="money">{money(Number(item.total_cents))}</TableCell></TableRow>)}</TableBody></Table></div></section><section className="data-surface p-6"><h2 className="font-semibold">服务实例</h2><div className="overflow-auto mt-4"><Table><TableHeader><TableRow><TableHead>服务</TableHead><TableHead>域名</TableHead><TableHead>状态</TableHead></TableRow></TableHeader><TableBody>{(data.services as Row[]).map((service) => <TableRow key={String(service.id)}><TableCell>{String(service.name)}</TableCell><TableCell className="font-mono text-xs">{String(service.domain)}</TableCell><TableCell><Status value={String(service.status)} /></TableCell></TableRow>)}</TableBody></Table></div></section></div><section className="data-surface p-6 mt-5"><h2 className="font-semibold">管理员备注</h2><PermissionFields permission="orders.manage"><form className="mt-4 space-y-3" onSubmit={(event) => void saveNotes(event)}><Textarea value={note ?? String(data.notes ?? "")} onChange={(event) => setNote(event.target.value)} rows={5} maxLength={5000} aria-label="订单备注" /><div className="flex items-center gap-3"><Button disabled={saving}>{saving ? "保存中" : "保存备注"}</Button>{message && <span className="text-sm text-muted-foreground" role="status">{message}</span>}</div></form></PermissionFields></section></> }</Workspace>;
}

export function ServicesView({ admin = false, groupId }: { admin?: boolean; groupId?: number }) {
  return <Workspace title={admin ? "服务主机" : "我的服务"} admin={admin}><ServiceList admin={admin} groupId={groupId}/></Workspace>;
}
function ServiceList({admin,groupId}:{admin:boolean;groupId?:number}){
  const [query,setQuery]=useState("");const [draft,setDraft]=useState("");const [state,setState]=useState("");const [offset,setOffset]=useState(0);
  const groups=useServiceNavigation(!admin);
  const {data,error,refresh}=useResource<ServicePage>(`${admin?"/admin":""}/services/page?group_id=${groupId??""}&status=${state}&q=${encodeURIComponent(query)}&limit=20&offset=${offset}`,false);
  useEffect(()=>{
    if(!data?.items.some(s=>s.status==="queued"||s.status==="provisioning"))return;
    const timer=window.setInterval(refresh,2000);return ()=>window.clearInterval(timer);
  },[data,refresh]);
  const group=groups.data?.find(g=>g.id===groupId);
  const missingGroup=groupId!==undefined&&groupId!==0&&!!groups.data&&!group;
  return <>
    <PageHeading title={admin?"服务主机":groupId===undefined?"我的服务":groupId===0?"未分组服务":group?.name??"服务分组"} description={admin?"服务生命周期与开通队列":"查看并管理已购买的服务"}>{!admin&&<ServiceNavigationSettings/>}</PageHeading>
    {!admin&&<div className="flex flex-wrap items-center gap-3 mb-5"><label className="text-sm flex gap-2 items-center min-w-0">查看分组<select aria-label="查看服务分组" className={`${catalogSelectStyle} sm:max-w-xs`} value={groupId??""} onChange={e=>{window.location.assign(e.target.value?`/console/services/group/${e.target.value}`:"/console/services");}}><option value="">全部服务</option><option value="0">未分组服务</option>{groups.data?.map(g=><option key={g.id} value={g.id}>{g.name}{!g.visible&&"（导航隐藏）"}</option>)}{missingGroup&&<option value={groupId}>已删除的分组</option>}</select></label>{groupId!==undefined&&<Button variant="link" asChild><Link href="/console/services">查看全部服务</Link></Button>}</div>}
    {missingGroup&&<p role="status" className="text-sm text-muted-foreground mb-5">该服务分组已删除，请从全部服务或其他分组继续查看。</p>}
    {!admin&&groups.error&&<Failure error={groups.error} retry={groups.refresh}/>}
    <form className="grid sm:grid-cols-[12rem_1fr_auto] gap-3 mb-6" onSubmit={e=>{e.preventDefault();setQuery(draft);setOffset(0);}}><select aria-label="服务状态筛选" className={catalogSelectStyle} value={state} onChange={e=>{setState(e.target.value);setOffset(0);}}><option value="">全部状态</option>{Object.entries({pending_payment:"待付款",queued:"开通队列",provisioning:"开通中",active:"运行中",suspended:"已暂停",failed:"失败",cancelled:"已取消",terminated:"已终止",import_review_required:"迁移待复核"}).map(([value,label])=><option value={value} key={value}>{label}</option>)}</select><Input aria-label="搜索服务" value={draft} onChange={e=>setDraft(e.target.value)} placeholder="搜索服务名称或标识"/><Button variant="outline"><Search size={16}/>搜索</Button></form>
    {error?<Failure error={error} retry={refresh}/>:!data?<Loading/>:<><div className="data-surface overflow-auto"><Table><TableHeader><TableRow><TableHead>服务</TableHead>{admin&&<TableHead>客户</TableHead>}<TableHead>标识</TableHead><TableHead>到期时间</TableHead><TableHead>状态</TableHead><TableHead>操作</TableHead></TableRow></TableHeader><TableBody>{data.items.length===0?<TableRow><TableCell colSpan={admin?6:5} className="h-32 text-center text-muted-foreground">暂无服务</TableCell></TableRow>:data.items.map(service=><TableRow key={service.id}><TableCell><Link className="text-primary hover:underline" href={`${admin?"/admin":"/console"}/services/${service.id}`}><div className="font-medium flex items-center gap-2"><Server size={15}/>{service.name}</div><div className="text-xs text-muted-foreground mt-1">#{service.id} · {service.service_group_name??"未分组"}</div></Link></TableCell>{admin&&<TableCell>{service.customer_name??"-"}</TableCell>}<TableCell className="font-mono text-xs">{service.domain}</TableCell><TableCell>{serviceDueLabel(service)}</TableCell><TableCell><Status value={service.status}/></TableCell><TableCell><ServiceActions service={service} admin={admin} refresh={refresh}/></TableCell></TableRow>)}</TableBody></Table></div><div className="flex justify-between items-center flex-wrap gap-3 mt-5 text-sm"><span>{data.total} 个服务</span><div className="flex gap-2"><Button variant="outline" disabled={offset===0} onClick={()=>setOffset(v=>Math.max(0,v-20))}>上一页</Button><Button variant="outline" disabled={offset+20>=data.total} onClick={()=>setOffset(v=>v+20)}>下一页</Button></div></div></>}
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
  return (
    <Workspace title="服务详情" admin={admin}>
      <Button variant="ghost" className="mb-5" asChild><Link href={admin ? "/admin/services" : "/console/services"}><ArrowUpRight size={16} />返回服务列表</Link></Button>
      {error ? <Failure error={error} retry={refresh} /> : !data ? <Loading /> : (
        <>
          <PageHeading title={data.name} description={`服务 #${data.id} · ${data.domain}`}>
            <Status value={data.status} />
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

export function AdminCustomerDetailView({ customerId }: { customerId: number }) {
  const searchParams = useSearchParams();
  const returnHref = `/admin/customers${searchParams.toString() ? `?${searchParams.toString()}` : ""}`;
  const customer = useResource<AdminCustomer>(`/admin/customers/${customerId}`);
  return <Workspace title="客户详情" admin>
    {customer.error ? <Failure error={customer.error} retry={customer.refresh} /> : !customer.data ? <Loading /> : <>
      <PageHeading title={customer.data.display_name || customer.data.username} description={`客户 #${customer.data.id} · ${customer.data.username}`}>
        <Button variant="outline" asChild><Link href={returnHref}><ArrowUpRight size={16} />返回客户管理</Link></Button>
      </PageHeading>
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
        <span className="border-b-2 border-primary px-4 py-3 text-sm font-medium text-primary whitespace-nowrap" aria-current="page">联系人</span>
        <Link className="px-4 py-3 text-sm text-muted-foreground hover:text-primary whitespace-nowrap" href={`/admin/customers/${customerId}/files`}>客户文件</Link>
      </nav>
      <ContactsPanel adminCustomerId={customerId} />
    </>}
  </Workspace>;
}

function ContactsPanel({ adminCustomerId }: { adminCustomerId?: number }) {
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
        { key: "ticket_no", label: "工单号" },
        { key: "subject", label: "主题",render:r=><Link className="text-primary underline underline-offset-4" href={`${admin?"/admin":"/console"}/tickets/${r.id}`}>{String(r.subject)}</Link> },
        ...(admin ? [
          { key: "department_name", label: "部门", render: (r: Row) => String(r.department_name ?? "未指定") },
          { key: "assignee_name", label: "处理人", render: (r: Row) => String(r.assignee_name ?? "未分派") },
        ] : []),
        {
          key: "priority",
          label: "优先级",
          render: (r) => <Status value={String(r.priority)} />,
        },
        {
          key: "updated_at",
          label: "更新时间",
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
      title="操作审计"
      description="关键业务变更的不可变记录"
      endpoint="/admin/audit"
      columns={[
        {
          key: "created_at",
          label: "时间",
          render: (r) => date(String(r.created_at)),
        },
        { key: "actor_name", label: "操作者" },
        { key: "action", label: "动作" },
        { key: "entity_type", label: "对象" },
        { key: "entity_id", label: "编号" },
      ]}
    />
  );
}

export function CancellationsView() {
  return <AdminListView kind="cancellations" title="取消申请" description="审核客户提交的服务取消申请" endpoint="/admin/cancellations" columns={[{ key: "service_name", label: "服务" }, { key: "customer_name", label: "客户" }, { key: "type", label: "类型", render: (r) => String(r.type) === "immediate" ? "立即取消" : "账期结束" }, { key: "reason", label: "原因" }, { key: "status", label: "状态", render: (r) => <Status value={String(r.status)} /> }, { key: "actions", label: "操作", render: (r) => <CancellationDecision row={r} /> }]} />;
}

function CancellationDecision({ row }: { row: Row }) {
  const allowed = useAdminPermission("services.manage");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const status = String(row.status);
  async function decide(decision: "approved" | "declined") {
    setBusy(true); setMessage("");
    try { await api(`/admin/cancellations/${Number(row.id)}/decision`, { method: "POST", body: JSON.stringify({ decision, note: decision === "approved" ? "审核通过" : "审核驳回" }) }); setMessage("已处理"); }
    catch (e) { setMessage((e as Error).message); } finally { setBusy(false); }
  }
  if (!allowed || status !== "pending") return <span className="text-xs text-muted-foreground">{message || "-"}</span>;
  return <div className="flex items-center gap-2"><Button size="sm" disabled={busy} onClick={() => decide("approved")}>通过</Button><Button size="sm" variant="outline" disabled={busy} onClick={() => decide("declined")}>驳回</Button>{message && <span className="text-xs text-muted-foreground">{message}</span>}</div>;
}
