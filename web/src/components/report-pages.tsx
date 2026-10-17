"use client";
import { ResourceTable } from "./resource-table";
import { useEffect, useState } from "react";
import { Search, Package, Users, Receipt, ClipboardList } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

import { money } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Failure, Loading } from "./common";
import { Workspace } from "./workspace";
import { Stat, PageHeading } from "./resource-page";

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
            <section hidden={reportTab !== "income"} className="data-surface p-4"><div className="surface-head"><div><h2 className="font-semibold">月度收入</h2><p className="text-xs text-muted-foreground mt-1">按支付时间统计</p></div></div><div className="overflow-auto mt-4"><ResourceTable
              items={income.data.months}
              rowKey={(item) => item.month}
              className=""
              empty={"暂无收入"}
              columns={[
                { header: "月份", render: (item) => item.month },
                { header: "收入", render: (item) => money(item.income_cents), className: "money" },
                { header: "账单数", render: (item) => item.invoice_count },
              ]}
            /></div></section>
            <section hidden={reportTab !== "customers"} className="data-surface p-4"><div className="surface-head"><div><h2 className="font-semibold">客户收入排名</h2><p className="text-xs text-muted-foreground mt-1">按净流水排序，展示前十名</p></div></div><div className="overflow-auto mt-4"><ResourceTable
              items={ranking.data}
              rowKey={(item) => item.user_id}
              className=""
              empty={"暂无流水"}
              columns={[
                { header: "客户", render: (item) => item.display_name },
                { header: "收入", render: (item) => money(item.income_cents), className: "money" },
                { header: "支出", render: (item) => money(item.expense_cents), className: "money" },
                { header: "净额", render: (item) => money(item.net_cents), className: "money" },
              ]}
            /></div></section>
            <section hidden={reportTab !== "products"} className="data-surface p-4 xl:col-span-2"><div className="surface-head"><div><h2 className="font-semibold">产品收入</h2><p className="text-xs text-muted-foreground mt-1">{period.year} 年 {period.month} 月已支付新购和续费</p></div></div><div className="overflow-auto mt-4"><ResourceTable
              items={products.data.products}
              rowKey={(item) => item.product_id}
              className=""
              empty={"暂无产品收入"}
              columns={[
                { header: "产品", render: (item) => item.name },
                {
                  header: "新购收入",
                  render: (item) => money(item.new_order_amount_cents),
                  className: "money",
                },
                { header: "新购订单", render: (item) => item.new_order_count },
                { header: "续费收入", render: (item) => money(item.renewal_amount_cents), className: "money" },
                { header: "续费订单", render: (item) => item.renewal_order_count },
                {
                  header: "合计",
                  render: (item) => money(item.total_amount_cents),
                  className: "money font-medium",
                },
              ]}
            /></div></section>
            <section hidden={reportTab !== "daily"} className="data-surface p-4 xl:col-span-2"><div className="surface-head"><div><h2 className="font-semibold">每日运营概览</h2><p className="text-xs text-muted-foreground mt-1">新客户、订单、工单和取消申请</p></div></div><div className="overflow-auto mt-4"><ResourceTable
              items={daily.data.days}
              rowKey={(item) => item.date}
              className=""
              empty={"暂无每日运营数据"}
              columns={[
                { header: "日期", render: (item) => item.date },
                { header: "新客户", render: (item) => item.new_clients },
                { header: "新订单", render: (item) => item.new_orders },
                { header: "已支付订单", render: (item) => item.paid_orders },
                { header: "新工单", render: (item) => item.new_tickets },
                { header: "工单回复", render: (item) => item.ticket_replies },
                { header: "取消申请", render: (item) => item.cancellation_requests },
              ]}
            /></div></section>
            <section hidden={reportTab !== "sales"} className="data-surface p-4"><div className="surface-head"><div><h2 className="font-semibold">我的业绩</h2><p className="text-xs text-muted-foreground mt-1">{period.year} 年按销售归属统计新订单和期间已支付账单收入</p></div></div><div className="overflow-auto mt-4"><ResourceTable
              items={sales.data.items}
              rowKey={(item) => item.sales_owner_id}
              className=""
              empty={"暂无业绩数据"}
              columns={[
                { header: "销售", render: (item) => item.sales_owner_name },
                { header: "订单数", render: (item) => item.order_count },
                { header: "收入", render: (item) => money(item.income_cents), className: "money" },
              ]}
            /></div></section>
            <section hidden={reportTab !== "tickets"} className="data-surface p-4 xl:col-span-2"><div className="surface-head"><div><h2 className="font-semibold">工单统计</h2><p className="text-xs text-muted-foreground mt-1">{period.year} 年 {period.month} 月按上海时区统计工单和当前待处理数量</p></div></div><div className="overflow-auto mt-4"><ResourceTable
              items={ticketStats.data.items}
              rowKey={(item) => item.date}
              className=""
              empty={"暂无工单数据"}
              columns={[
                { header: "日期", render: (item) => item.date },
                { header: "工单数", render: (item) => item.ticket_count },
                { header: "待处理", render: (item) => item.open_count },
              ]}
            /></div></section>
          </div>
        </>
      )}
    </Workspace>
  );
}
