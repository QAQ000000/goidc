"use client";
import { useEffect, useState } from "react";
import { Search, Users, Receipt, ClipboardList } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { money } from "@/lib/api";
import { Loading } from "./common";
import { ResourceTable } from "./resource-table";
import { Workspace, useAdminPermission } from "./workspace";
import { Stat, PageHeading } from "./resource-page";
import { ReportPanel } from "./report-shared";
import { ProductIncomeGroups } from "./report-products";
import { AnnualIncomeReport } from "./report-income";
import { SalesReportDetails } from "./report-sales";
import { TicketHandlerReport } from "./report-tickets";

type IncomeReport = { year: number; months: { month: string; income_cents: number; invoice_count: number }[]; total: { income_cents: number; invoice_count: number } };
type DailyReport = { year: number; month: number; ticket_metrics_available: boolean; days: { date: string; new_clients: number; new_orders: number; paid_orders: number; new_tickets: number | null; ticket_replies: number | null; cancellation_requests: number }[] };
type CustomerRanking = { user_id: number; display_name: string; income_cents: number; expense_cents: number; net_cents: number }[];
type TicketReport = { year: number; month: number; items: { date: string; ticket_count: number; open_count: number }[] };
type Period = { year: number; month: number };
const tabs = { income: "收入", customers: "客户", products: "产品", daily: "每日运营", sales: "我的业绩", tickets: "工单统计" };
type ReportTab = keyof typeof tabs;

function ActiveReport({ tab, period, exportDisabled, ticketPermission }: { tab: ReportTab; period: Period; exportDisabled: boolean; ticketPermission: boolean }) {
  const annual = `year=${period.year}`;
  const monthly = `${annual}&month=${period.month}`;
  const filename = `report-${tab}-${tab === "customers" ? "all-history-top10" : tab === "income" || tab === "sales" ? period.year : `${period.year}-${String(period.month).padStart(2, "0")}`}.csv`;
  const shared = { filename, exportDisabled };
  switch (tab) {
    case "income": return <IncomeReports period={period} exportDisabled={exportDisabled}/>;
    case "customers": return <ReportPanel<CustomerRanking> {...shared} path="/admin/reports/customer-ranking" title="客户收入排名 · 全历史" description="全历史余额流水，按净额排序展示前十名；不受年份、月份筛选影响，导出范围与此表一致">
      {data => <ResourceTable items={data} rowKey={item => item.user_id} empty="暂无流水" columns={[
        { header: "客户", render: item => item.display_name },
        { header: "收入", render: item => money(item.income_cents), className: "money" },
        { header: "支出", render: item => money(item.expense_cents), className: "money" },
        { header: "净额", render: item => money(item.net_cents), className: "money" },
      ]} />}
    </ReportPanel>;
    case "products": return <ProductIncomeGroups year={period.year} month={period.month} exportDisabled={exportDisabled}/>;
    case "daily": return <ReportPanel<DailyReport> {...shared} path={`/admin/reports/new-clients?${monthly}`} title="每日运营概览" description={`${period.year} 年 ${period.month} 月，按上海时区统计新客户、订单、工单和取消申请`}>
      {data => <>
        <div className="stat-grid mb-6"><Stat icon={Users} label={`${period.month} 月新客户`} value={data.days.reduce((total, item) => total + item.new_clients, 0)} /></div>
        {(!ticketPermission || !data.ticket_metrics_available) && <p className="mb-4 text-sm text-muted-foreground">无工单查看权限：新工单和工单回复指标不可见，其余经营数据正常展示。</p>}
        <ResourceTable items={data.days} rowKey={item => item.date} empty="暂无每日运营数据" columns={[
          { header: "日期", render: item => item.date },
          { header: "新客户", render: item => item.new_clients },
          { header: "新订单", render: item => item.new_orders },
          { header: "已支付订单", render: item => item.paid_orders },
          { header: "新工单", render: item => ticketPermission && data.ticket_metrics_available && item.new_tickets !== null ? item.new_tickets : "无权限" },
          { header: "工单回复", render: item => ticketPermission && data.ticket_metrics_available && item.ticket_replies !== null ? item.ticket_replies : "无权限" },
          { header: "取消申请", render: item => item.cancellation_requests },
        ]} />
      </>}
    </ReportPanel>;
    case "sales": return <SalesReportDetails year={period.year} month={period.month} exportDisabled={exportDisabled}/>;
    case "tickets": return <TicketReports period={period} exportDisabled={exportDisabled} ticketPermission={ticketPermission}/>;
  }
}

function useReportView(name: string) {
  const read = () => new URLSearchParams(typeof window === "undefined" ? "" : window.location.search).get(name) === "legacy";
  const [legacy, setLegacy] = useState(read);
  useEffect(() => { const restore = () => setLegacy(new URLSearchParams(window.location.search).get(name) === "legacy"); window.addEventListener("popstate", restore); return () => window.removeEventListener("popstate", restore); }, [name]);
  function change(value: boolean) {
    const params = new URLSearchParams(window.location.search);
    if (value) params.set(name, "legacy"); else params.delete(name);
    window.history.pushState(null, "", `${window.location.pathname}?${params}`);
    setLegacy(value);
  }
  return [legacy, change] as const;
}

function IncomeReports({period,exportDisabled}:{period:Period;exportDisabled:boolean}) {
  const [legacy,change] = useReportView("income_view");
  const annual = `year=${period.year}`;
  const shared = {filename:`business-income-${period.year}.csv`, exportDisabled};
  return <><div className="mb-4 flex flex-wrap gap-2"><Button variant={legacy?"outline":"default"} onClick={()=>change(false)}>年度现金收支</Button><Button variant={legacy?"default":"outline"} onClick={()=>change(true)}>业务消费与账单</Button></div>{legacy ? <ReportPanel<IncomeReport> {...shared} path={`/admin/reports/income?${annual}`} title="业务消费与账单" description={`${period.year} 年业务消费（含余额消费、信用实际还款，排除充值）；年度账单数按全年去重`}>
      {data => <>
        <div className="stat-grid mb-6"><Stat icon={Receipt} label={`${period.year} 年业务消费`} value={money(data.total.income_cents)} /><Stat icon={ClipboardList} label="全年去重账单数" value={data.total.invoice_count} /></div>
        <ResourceTable items={data.months} rowKey={item => item.month} empty="暂无收入" columns={[
          { header: "月份", render: item => item.month },
          { header: "收入", render: item => money(item.income_cents), className: "money" },
          { header: "账单数", render: item => item.invoice_count },
        ]} />
      </>}
    </ReportPanel> : <AnnualIncomeReport year={period.year} exportDisabled={exportDisabled}/>}</>;
}

function TicketReports({period,exportDisabled,ticketPermission}:{period:Period;exportDisabled:boolean;ticketPermission:boolean}) {
  const [legacy,change] = useReportView("ticket_view");
  const monthly = `year=${period.year}&month=${period.month}`;
  const shared = {filename:`ticket-days-${period.year}-${period.month}.csv`,exportDisabled};
  if(!ticketPermission) return <section className="data-surface p-6" aria-label="工单统计"><h2 className="font-semibold">工单统计</h2><p role="status" className="mt-3 text-muted-foreground">没有查看工单统计的权限，需要工单查看权限。</p></section>;
  return <><div className="mb-4 flex flex-wrap gap-2"><Button variant={legacy?"outline":"default"} onClick={()=>change(false)}>处理人与评分</Button><Button variant={legacy?"default":"outline"} onClick={()=>change(true)}>每日创建与待处理</Button></div>{legacy ? <ReportPanel<TicketReport> {...shared} path={`/admin/reports/tickets?${monthly}`} title="工单统计" description={`${period.year} 年 ${period.month} 月，按上海时区统计创建量（保留合并来源）和这些工单中当前未合并的待处理量`}>
      {data => <ResourceTable items={data.items} rowKey={item => item.date} empty="暂无工单数据" columns={[
        { header: "日期", render: item => item.date },
        { header: "创建工单数", render: item => item.ticket_count },
        { header: "当前待处理", render: item => item.open_count },
      ]} />}
    </ReportPanel> : <TicketHandlerReport year={period.year} month={period.month} exportDisabled={exportDisabled}/>}</>;
}

export function ReportsView() {
  const [reportTab, setReportTab] = useState<ReportTab>("income");
  const now = new Date(new Date().toLocaleString("en-US", { timeZone: "Asia/Shanghai" }));
  const [year, setYear] = useState(String(now.getFullYear()));
  const [month, setMonth] = useState(String(now.getMonth() + 1));
  const [period, setPeriod] = useState<Period>({ year: now.getFullYear(), month: now.getMonth() + 1 });
  const [reportReady, setReportReady] = useState(false);
  const [queryRevision, setQueryRevision] = useState(0);
  const ticketPermission = useAdminPermission("tickets.view");
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
      setReportTab(Object.hasOwn(tabs, tab) ? tab as ReportTab : "income");
      setYear(String(restoredYear));
      setMonth(String(restoredMonth));
      setPeriod({ year: restoredYear, month: restoredMonth });
      setQueryRevision(value => value + 1);
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
  const monthly = ["products", "daily", "tickets", "sales"].includes(reportTab);
  const validYear = Number.isInteger(Number(year)) && Number(year) >= 2000 && Number(year) <= now.getFullYear() + 1;
  const changed = reportTab !== "customers" && (Number(year) !== period.year || monthly && Number(month) !== period.month);
  const reportKey = `${reportTab}:${reportTab === "customers" ? "all-history" : monthly ? `${period.year}-${period.month}` : period.year}:${ticketPermission}:${queryRevision}`;
  return <Workspace title="统计报表" admin>
    <PageHeading title="统计报表" description="收入、客户、产品和服务运营数据">
      {reportTab === "customers" ? <p className="text-sm text-muted-foreground">统计范围：全历史余额流水 · 前十名</p> : <form className="flex flex-wrap items-center gap-2" onSubmit={event => { event.preventDefault(); if (validYear) { const params = new URLSearchParams(window.location.search); params.set("year",year); params.set("month",month); params.delete("product_offset"); params.delete("sales_offset"); params.delete("ticket_offset"); window.history.pushState(null,"",`${window.location.pathname}?${params}`); setPeriod({ year: Number(year), month: Number(month) }); setQueryRevision(value => value + 1); } }}>
        <Input className="w-24" type="number" min={2000} max={now.getFullYear() + 1} value={year} onChange={event => setYear(event.target.value)} aria-label="统计年份" />
        {monthly && <Select value={month} onValueChange={setMonth}><SelectTrigger className="w-28" aria-label="统计月份"><SelectValue /></SelectTrigger><SelectContent>{Array.from({ length: 12 }, (_, index) => <SelectItem key={index + 1} value={String(index + 1)}>{index + 1} 月</SelectItem>)}</SelectContent></Select>}
        <Button type="submit" disabled={!validYear || !reportReady}><Search size={15} />查询</Button>
      </form>}
    </PageHeading>
    <div className="mb-4 flex overflow-x-auto border-b" role="group" aria-label="报表分类">{Object.entries(tabs).map(([key, label]) => <button key={key} type="button" aria-pressed={reportTab === key} className={`shrink-0 border-b-2 px-5 py-3 text-sm ${reportTab === key ? "border-primary text-primary font-medium" : "border-transparent text-muted-foreground"}`} onClick={() => { const params = new URLSearchParams(window.location.search); params.set("tab",key); window.history.pushState(null,"",`${window.location.pathname}?${params}`); setReportTab(key as ReportTab); }}>{label}{key === "tickets" && !ticketPermission ? "（无权限）" : ""}</button>)}</div>
    {!reportReady ? <Loading /> : <ActiveReport key={reportKey} tab={reportTab} period={period} exportDisabled={changed} ticketPermission={ticketPermission} />}
  </Workspace>;
}
