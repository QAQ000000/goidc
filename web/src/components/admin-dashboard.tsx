"use client";

import { useState } from "react";
import Link from "next/link";
import {
  ClipboardCheck,
  FileText,
  MoreHorizontal,
  Settings,
  UserCheck,
  WalletCards,
  RefreshCw,
} from "lucide-react";
import { useResource } from "@/hooks/use-resource";
import { money, type User } from "@/lib/api";
import { can as hasAdminPermission } from "@/lib/admin-navigation";
import { Failure } from "./common";
import { Workspace } from "./workspace";

type DashboardSummary = {
  customers: number;
  active_products: number;
  pending_orders: number;
  unpaid_invoices: number;
  open_tickets: number;
  unpaid_cents: number;
  total_orders: number;
  today_paid_orders: number;
  month_paid_orders: number;
  seven_days_paid_orders: number;
  seven_days_pending_orders: number;
  today_income_cents: number;
  month_income_cents: number;
  annual_income_cents: number;
  total_income_cents: number;
  pending_certifications: number;
  pending_withdrawals: number;
  today_new_clients: number;
  expiring_services: ServiceItem[];
};

type DailyReport = {
  year: number;
  month: number;
  days: {
    date: string;
    new_clients: number;
    new_orders: number;
    paid_orders: number;
    new_tickets: number;
    ticket_replies: number;
    cancellation_requests: number;
  }[];
};

type AuditEvent = {
  id: number;
  action: string;
  actor_id: number | null;
  actor_name: string | null;
  entity_type: string;
  entity_id: string;
  created_at: string;
  payload?: Record<string, unknown>;
};

type TicketItem = {
  id: number;
  ticket_no: string;
  legacy_ticket_no?: string;
  subject: string;
  status: string;
  created_at: string;
  user_id: number;
};

type ServiceItem = {
  id: number;
  domain?: string;
  product_name?: string;
  status: string;
  next_due_at?: string;
  name?: string;
  customer_name?: string;
};

const DEFAULT_CARDS: Record<string, boolean> = {
  ordersOverview: true,
  todos: true,
  incomeOverview: true,
  tradeStats: true,
  systemLogs: true,
  onlineAdmins: true,
  expiringServices: true,
  productRankings: true,
  customerOverview: true,
  staffRankings: true,
  pendingTickets: true,
};

export function AdminDashboard() {
  return (
    <Workspace title="工作台" admin>
      <DashboardContent />
    </Workspace>
  );
}

function DashboardContent() {
  const now = new Date(new Date().toLocaleString("en-US", { timeZone: "Asia/Shanghai" }));
  const year = now.getFullYear();
  const month = now.getMonth() + 1;

  const user = useResource<User>("/me");
  const summary = useResource<DashboardSummary>("/admin/dashboard");
  const can = (permission: string) => hasAdminPermission(user.data, permission);
  const daily = useResource<DailyReport>(can("reports.view") ? `/admin/reports/new-clients?year=${year}&month=${month}` : null, false);
  const audit = useResource<AuditEvent[]>(can("audit.view") ? "/admin/audit" : null, false);
  const tickets = useResource<TicketItem[]>(can("tickets.view") ? "/admin/tickets?status=open" : null, false);

  const [cardsVisible, setCardsVisible] = useState<Record<string, boolean>>(DEFAULT_CARDS);
  const [showConfigPopover, setShowConfigPopover] = useState(false);

  const refreshAll = () => {
    summary.refresh();
    daily.refresh();
    audit.refresh();
    tickets.refresh();
  };

  const weekdays = ["星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"];
  const formattedDate = `${year}年${month}月${now.getDate()}日，${weekdays[now.getDay()]}`;
  const displayName = user.data?.display_name || user.data?.username || "";

  // Calculate order counts
  const todayPaidOrders = summary.data?.today_paid_orders;
  const monthPaidOrders = summary.data?.month_paid_orders;

  // Past 7 days paid

  const sevenDaysPaidOrders = summary.data?.seven_days_paid_orders;
  const sevenDaysPendingOrders = summary.data?.seven_days_pending_orders;
  const totalOrders = summary.data?.total_orders;

  // Income summary
  const currentMonthIncome = summary.data?.month_income_cents ?? 0;
  const totalIncomeCents = summary.data?.total_income_cents ?? 0;

  // Real name certs pending count
  const certsPendingCount = can("certifications.view") ? summary.data?.pending_certifications : "无权限";
  const withdrawalsPendingCount = can("affiliates.view") ? summary.data?.pending_withdrawals : "无权限";

  // Open tickets
  const openTicketsList = (tickets.data ?? [])
    .filter(t => t.status === "open" || t.status === "pending")
    .slice(0, 5);

  // Expiring services in next 30 days
  const expiringServicesList = summary.data?.expiring_services ?? [];

  // Today new customers
  const todayNewClients = summary.data?.today_new_clients;

  if (summary.error) {
    return <Failure error={summary.error} retry={summary.refresh} />;
  }
  if (user.error) return <Failure error={user.error} retry={user.refresh} />;
  if (!summary.data || !user.data) return <p role="status">正在加载工作台…</p>;

  return (
    <div className="w-full space-y-4">
      {/* Top Greeting Bar */}
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between text-xs text-[#666666] py-2 px-1">
        <div>
          您好，{displayName}！今日，{formattedDate}
        </div>
        <div className="relative flex items-center gap-2 mt-1 sm:mt-0">
          <button
            type="button"
            className="p-1 hover:text-primary transition-colors"
            title="个性化卡片设置"
            aria-label="个性化卡片设置"
            onClick={() => setShowConfigPopover(prev => !prev)}
          >
            <Settings size={15} />
          </button>
          <button
            type="button"
            className="p-1 hover:text-primary transition-colors ml-1"
            title="刷新数据"
            aria-label="刷新数据"
            onClick={refreshAll}
          >
            <RefreshCw size={14} />
          </button>

          {/* Card visibility popover */}
          {showConfigPopover && (
            <div className="absolute right-0 top-7 z-50 w-44 rounded border border-border bg-white p-3 shadow-lg text-xs space-y-2">
              <button
                type="button"
                className="w-full rounded bg-[#ebf1ff] py-1 text-center font-medium text-[#2e53e9] hover:bg-[#dce6ff]"
                onClick={() => setCardsVisible(DEFAULT_CARDS)}
              >
                恢复默认
              </button>
              <div className="space-y-1.5 max-h-56 overflow-y-auto pt-1">
                {[
                  { key: "ordersOverview", label: "订单概览" },
                  { key: "todos", label: "代办事项" },
                  { key: "incomeOverview", label: "收入概览" },
                  { key: "tradeStats", label: "交易统计" },
                  { key: "systemLogs", label: "系统日志" },
                  { key: "onlineAdmins", label: "在线管理员" },
                  { key: "expiringServices", label: "即将到期" },
                  { key: "productRankings", label: "本月销量排行" },
                  { key: "customerOverview", label: "客户概况" },
                  { key: "staffRankings", label: "本月员工销量排行" },
                  { key: "pendingTickets", label: "待处理工单" },
                ].map(item => (
                  <label key={item.key} className="flex items-center gap-2 cursor-pointer hover:bg-muted/50 p-0.5 rounded">
                    <input
                      type="checkbox"
                      checked={cardsVisible[item.key] ?? true}
                      onChange={e => setCardsVisible(prev => ({ ...prev, [item.key]: e.target.checked }))}
                      className="rounded border-gray-300 text-primary focus:ring-primary"
                    />
                    <span>{item.label}</span>
                  </label>
                ))}
              </div>
            </div>
          )}
        </div>
      </div>

      {/* Row 1: 3 cards */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-5">
        {/* Card 1: 订单概览 */}
        {cardsVisible.ordersOverview && (
          <section className="bg-white rounded border border-[#ebeef5] shadow-[0_2px_12px_0_rgba(0,0,0,0.08)] p-5 min-h-[206px] flex flex-col justify-between">
            <div className="flex items-center justify-between mb-4">
              <span className="text-sm font-semibold text-black">订单概览</span>
              <MoreHorizontal size={16} className="text-[#999999] cursor-pointer" />
            </div>
            <div className="grid grid-cols-2 sm:grid-cols-4 gap-2 text-center sm:text-left">
              <div>
                <div className="text-xs text-[#999999]">今日已付订单</div>
                <div className="text-2xl text-[#316eea] font-normal mt-2.5 tabular-nums">{todayPaidOrders}</div>
                <div className="text-xs text-[#666666] mt-2.5">总订单: {totalOrders}笔</div>
              </div>
              <div>
                <div className="text-xs text-[#999999]">本月已付订单</div>
                <div className="text-2xl text-[#316eea] font-normal mt-2.5 tabular-nums">{monthPaidOrders}</div>
                <div className="text-xs text-[#666666] mt-2.5">总订单: {totalOrders}笔</div>
              </div>
              <div>
                <div className="text-xs text-[#999999]">近7日已付订单</div>
                <div className="text-2xl text-[#316eea] font-normal mt-2.5 tabular-nums">{sevenDaysPaidOrders}</div>
                <div className="text-xs text-[#666666] mt-2.5">总订单: {totalOrders}笔</div>
              </div>
              <div>
                <div className="text-xs text-[#999999]">近7日待付订单</div>
                <div className="text-2xl text-[#316eea] font-normal mt-2.5 tabular-nums">{sevenDaysPendingOrders}</div>
                <div className="text-xs text-[#666666] mt-2.5">总订单: {totalOrders}笔</div>
              </div>
            </div>
          </section>
        )}

        {/* Card 2: 代办事项 */}
        {cardsVisible.todos && (
          <section className="bg-white rounded border border-[#ebeef5] shadow-[0_2px_12px_0_rgba(0,0,0,0.08)] p-5 min-h-[206px]">
            <div className="flex items-center justify-between mb-4">
              <span className="text-sm font-semibold text-black">代办事项</span>
              <MoreHorizontal size={16} className="text-[#999999] cursor-pointer" />
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <Link href="/admin/tickets" className="bg-[#f9f9f9] rounded p-3 flex items-center hover:bg-slate-100 transition-colors">
                <div className="h-11 w-11 rounded-full bg-[#0089f0] text-white flex items-center justify-center shrink-0">
                  <FileText size={20} />
                </div>
                <div className="ml-4 min-w-0">
                  <div className="text-2xl text-[#0000ee] font-normal leading-tight tabular-nums">{can("tickets.view") ? summary.data?.open_tickets : "无权限"}</div>
                  <div className="text-xs text-[#999999] mt-0.5">待处理工单</div>
                </div>
              </Link>

              <Link href="/admin/orders?status=pending" className="bg-[#f9f9f9] rounded p-3 flex items-center hover:bg-slate-100 transition-colors">
                <div className="h-11 w-11 rounded-full bg-[#64b524] text-white flex items-center justify-center shrink-0">
                  <ClipboardCheck size={20} />
                </div>
                <div className="ml-4 min-w-0">
                  <div className="text-2xl text-[#0000ee] font-normal leading-tight tabular-nums">{summary.data?.pending_orders ?? 0}</div>
                  <div className="text-xs text-[#999999] mt-0.5">待核验订单</div>
                </div>
              </Link>

              <Link href="/admin/certifications" className="bg-[#f9f9f9] rounded p-3 flex items-center hover:bg-slate-100 transition-colors">
                <div className="h-11 w-11 rounded-full bg-[#f38900] text-white flex items-center justify-center shrink-0">
                  <UserCheck size={20} />
                </div>
                <div className="ml-4 min-w-0">
                  <div className="text-2xl text-[#0000ee] font-normal leading-tight tabular-nums">{certsPendingCount}</div>
                  <div className="text-xs text-[#999999] mt-0.5">实名认证</div>
                </div>
              </Link>

              <Link href="/admin/affiliate" className="bg-[#f9f9f9] rounded p-3 flex items-center hover:bg-slate-100 transition-colors">
                <div className="h-11 w-11 rounded-full bg-[#40c9c8] text-white flex items-center justify-center shrink-0">
                  <WalletCards size={20} />
                </div>
                <div className="ml-4 min-w-0">
                  <div className="text-2xl text-[#0000ee] font-normal leading-tight tabular-nums">{withdrawalsPendingCount}</div>
                  <div className="text-xs text-[#999999] mt-0.5">提现申请</div>
                </div>
              </Link>
            </div>
          </section>
        )}

        {/* Card 3: 收入概览 */}
        {cardsVisible.incomeOverview && (
          <section className="bg-white rounded border border-[#ebeef5] shadow-[0_2px_12px_0_rgba(0,0,0,0.08)] p-5 min-h-[206px]">
            <div className="flex items-center justify-between mb-4">
              <span className="text-sm font-semibold text-black">收入概览</span>
              <MoreHorizontal size={16} className="text-[#999999] cursor-pointer" />
            </div>
            <div className="grid grid-cols-2 gap-y-4 gap-x-2">
              <div>
                <div className="text-2xl text-[#f38900] font-normal mb-1 tabular-nums">{money(summary.data.today_income_cents)}</div>
                <div className="text-xs text-[#999999]">今日收入</div>
              </div>
              <div>
                <div className="text-2xl text-[#0089f0] font-normal mb-1 tabular-nums">{money(currentMonthIncome)}</div>
                <div className="text-xs text-[#999999]">本月收入</div>
              </div>
              <div>
                <div className="text-2xl text-[#333333] font-normal mb-1 tabular-nums">{money(summary.data.annual_income_cents)}</div>
                <div className="text-xs text-[#999999]">今年收入</div>
              </div>
              <div>
                <div className="text-2xl text-[#333333] font-normal mb-1 tabular-nums">{money(totalIncomeCents)}</div>
                <div className="text-xs text-[#999999]">总收入</div>
              </div>
            </div>
          </section>
        )}
      </div>

      {/* Row 2: 交易统计 (2 cols) & 系统日志 (1 col) */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-5">
        {/* Card 4: 交易统计 */}
        {cardsVisible.tradeStats && (
          <section className="lg:col-span-2 bg-white rounded border border-[#ebeef5] shadow-[0_2px_12px_0_rgba(0,0,0,0.08)] p-5 min-h-[206px]">
            <div className="flex items-center justify-between mb-4">
              <span className="text-sm font-semibold text-black">交易统计</span>
              <MoreHorizontal size={16} className="text-[#999999] cursor-pointer" />
            </div>

            {/* Legend */}
            <div className="flex items-center justify-center gap-6 text-xs text-[#666666] mb-3">
              <div className="flex items-center gap-1.5">
                <span className="h-2.5 w-6 rounded-sm bg-[#2e53e9]" />
                <span>已付订单(笔)</span>
              </div>
            </div>

            {/* Interactive SVG Chart */}
            {!can("reports.view") ? <p>无报表查看权限</p> : daily.error ? <Failure error={daily.error} retry={daily.refresh} /> : !daily.data ? <p>正在加载…</p> : <TradeStatsChart daily={daily.data} month={month} />}
          </section>
        )}

        {/* Card 5: 系统日志 */}
        {cardsVisible.systemLogs && (
          <section className="bg-white rounded border border-[#ebeef5] shadow-[0_2px_12px_0_rgba(0,0,0,0.08)] p-5 min-h-[206px]">
            <div className="flex items-center justify-between mb-4">
              <Link href="/admin/audit" className="text-sm font-semibold text-[#333333] hover:text-[#2e53e9] transition-colors">
                系统日志
              </Link>
              <MoreHorizontal size={16} className="text-[#999999] cursor-pointer" />
            </div>

            <div className="space-y-4">
              {!can("audit.view") ? <p>无审计查看权限</p> : audit.error ? <Failure error={audit.error} retry={audit.refresh} /> : !audit.data ? <p>正在加载…</p> : audit.data.length > 0 ? (
                audit.data.slice(0, 5).map(item => (
                  <div key={item.id} className="text-xs border-b border-gray-50 pb-2.5 last:border-none last:pb-0">
                    <div className="text-[#333333] font-normal leading-relaxed break-words">
                      {formatAuditAction(item)}
                    </div>
                    <div className="flex items-center justify-between text-[#999999] mt-1.5">
                      <span>{item.actor_name || "系统"}</span>
                      <span>{formatDateTime(item.created_at)}</span>
                    </div>
                  </div>
                ))
              ) : (
                <div className="text-center text-xs text-[#999999] py-10">暂无系统日志</div>
              )}
            </div>
          </section>
        )}
      </div>

      {/* Row 3: 在线管理员 (1 col) & 即将到期 (1 col) & 本月销量排行 (1 col) */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-5">
        {/* Card 6: 在线管理员 */}
        {cardsVisible.onlineAdmins && (
          <section className="bg-white rounded border border-[#ebeef5] shadow-[0_2px_12px_0_rgba(0,0,0,0.08)] p-5 min-h-[206px]">
            <div className="flex items-center justify-between mb-3">
              <span className="text-sm font-semibold text-black">当前管理员</span>
              <MoreHorizontal size={16} className="text-[#999999] cursor-pointer" />
            </div>
            <div className="text-xs text-[#666666] mb-4">当前管理员</div>
            <div className="flex items-center gap-3">
              <div className="h-10 w-10 rounded-full bg-[#ebf1ff] text-[#2e53e9] font-semibold text-base flex items-center justify-center shrink-0">
                {Array.from(displayName)[0] || "A"}
              </div>
              <div className="min-w-0">
                <div className="text-sm text-[#333333] font-normal">{displayName}</div>
              </div>
            </div>
          </section>
        )}

        {/* Card 7: 即将到期 */}
        {cardsVisible.expiringServices && (
          <section className="bg-white rounded border border-[#ebeef5] shadow-[0_2px_12px_0_rgba(0,0,0,0.08)] p-5 min-h-[206px]">
            <div className="flex items-center justify-between mb-3">
              <span className="text-sm font-semibold text-black">即将到期</span>
              <MoreHorizontal size={16} className="text-[#999999] cursor-pointer" />
            </div>
            {!can("services.view") ? <p>无服务查看权限</p> : expiringServicesList.length > 0 ? (
              <div className="space-y-3">
                {expiringServicesList.map(s => (
                  <div key={s.id} className="flex items-center justify-between text-xs py-1 border-b border-gray-50 last:border-none">
                    <span className="text-[#333333] truncate max-w-[180px]">{s.domain || s.name || `服务 #${s.id}`}</span>
                    <span className="text-[#999999] shrink-0">{s.next_due_at ? new Date(s.next_due_at).toLocaleDateString("zh-CN", {timeZone:"Asia/Shanghai"}) : ""}</span>
                  </div>
                ))}
              </div>
            ) : (
              <div className="h-32 flex items-center justify-center text-xs text-[#999999]">
                暂无即将到期业务
              </div>
            )}
          </section>
        )}

        {/* Card 8: 本月销量排行 */}
        {cardsVisible.productRankings && (
          <section className="bg-white rounded border border-[#ebeef5] shadow-[0_2px_12px_0_rgba(0,0,0,0.08)] p-5 min-h-[206px] flex flex-col justify-between">
            <div className="flex items-center justify-between mb-3">
              <span className="text-sm font-semibold text-black">本月销量排行</span>
              <MoreHorizontal size={16} className="text-[#999999] cursor-pointer" />
            </div>
            <div className="flex flex-col items-center justify-center py-6">
              <span className="text-xs text-[#999999]">销量排行暂未提供</span>
            </div>
          </section>
        )}
      </div>

      {/* Row 4: 客户概况 (1 col) & 本月员工销量排行 (1 col) & 待处理工单 (1 col) */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-5">
        {/* Card 9: 客户概况 */}
        {cardsVisible.customerOverview && (
          <section className="bg-white rounded border border-[#ebeef5] shadow-[0_2px_12px_0_rgba(0,0,0,0.08)] p-5 min-h-[206px]">
            <div className="flex items-center justify-between mb-4">
              <span className="text-sm font-semibold text-black">客户概况</span>
              <MoreHorizontal size={16} className="text-[#999999] cursor-pointer" />
            </div>
            <div className="flex items-center justify-between text-xs text-[#666666] pt-1">
              <div>
                <span className="text-sm font-medium text-[#316eea] mr-1">{todayNewClients}</span>今日新增
              </div>
              <div>
                <span className="text-sm font-medium text-[#f38900] mr-1">未提供</span>当前在线数量
              </div>
              <div>
                <span className="text-sm font-medium text-[#333333] mr-1">{summary.data?.customers ?? 0}</span>系统总计数量
              </div>
            </div>
            <div className="text-center text-xs text-[#999999] mt-16 pb-4">
              暂无数据
            </div>
          </section>
        )}

        {/* Card 10: 本月员工销量排行 */}
        {cardsVisible.staffRankings && (
          <section className="bg-white rounded border border-[#ebeef5] shadow-[0_2px_12px_0_rgba(0,0,0,0.08)] p-5 min-h-[206px] flex flex-col justify-between">
            <div className="flex items-center justify-between mb-3">
              <span className="text-sm font-semibold text-black">本月员工销量排行</span>
              <MoreHorizontal size={16} className="text-[#999999] cursor-pointer" />
            </div>
            <div className="flex flex-col items-center justify-center py-6">
              <span className="text-xs text-[#999999]">员工销量排行暂未提供</span>
            </div>
          </section>
        )}

        {/* Card 11: 待处理工单 */}
        {cardsVisible.pendingTickets && (
          <section className="bg-white rounded border border-[#ebeef5] shadow-[0_2px_12px_0_rgba(0,0,0,0.08)] p-5 min-h-[206px]">
            <div className="flex items-center justify-between mb-4">
              <span className="text-sm font-semibold text-black">待处理工单</span>
              <MoreHorizontal size={16} className="text-[#999999] cursor-pointer" />
            </div>
            <div className="space-y-3">
              {!can("tickets.view") ? <p>无工单查看权限</p> : tickets.error ? <Failure error={tickets.error} retry={tickets.refresh} /> : !tickets.data ? <p>正在加载…</p> : openTicketsList.length > 0 ? (
                openTicketsList.map(t => (
                  <div key={t.id} className="flex items-center justify-between text-xs">
                    <div className="flex items-center gap-2 min-w-0 pr-2">
                      <span className="shrink-0 rounded bg-[rgba(24,129,235,0.1)] px-1.5 py-0.5 text-[10px] text-[#1881eb]">
                        开启中
                      </span>
                      <Link href="/admin/tickets" className="truncate text-[#333333] hover:underline hover:text-[#2e53e9]">
                        #{t.ticket_no.slice(0, 8)} - {t.subject}
                      </Link>
                    </div>
                    <span className="shrink-0 text-[#999999]">{formatMonthDay(t.created_at)}</span>
                  </div>
                ))
              ) : (
                <div className="text-center text-xs text-[#999999] py-12">暂无待处理工单</div>
              )}
            </div>
          </section>
        )}
      </div>

      {/* Footer */}
      <div className="text-center text-xs text-[#999999] py-6">
        重庆顺戴网络科技有限公司 智简魔方财务系统
      </div>
    </div>
  );
}

// Interactive SVG chart for 交易统计
function TradeStatsChart({ daily, month }: { daily: DailyReport | null | undefined; month: number }) {
  const days = daily?.days ?? [];
  const count = Math.max(days.length, 2);
  const maxOrder = Math.max(1, ...days.map(d => Math.max(d.new_orders || 0, d.paid_orders || 0)));

  // SVG dimensions
  const width = 860;
  const height = 240;
  const padLeft = 45;
  const padRight = 45;
  const padBottom = 35;
  const padTop = 20;

  const chartW = width - padLeft - padRight;
  const chartH = height - padTop - padBottom;

  const points = days.map((d, index) => {
    const x = padLeft + (index / (count - 1)) * chartW;
    const yVal = d.paid_orders || 0;
    const y = padTop + chartH - (yVal / maxOrder) * chartH;
    return { x, y, val: yVal, date: d.date, dayNum: index + 1 };
  });

  const pathD = points.length > 0
    ? `M ${points.map(p => `${p.x.toFixed(1)} ${p.y.toFixed(1)}`).join(" L ")}`
    : `M ${padLeft} ${padTop + chartH} L ${padLeft + chartW} ${padTop + chartH}`;

  return (
    <div className="w-full overflow-x-auto">
      <div className="min-w-[600px] h-[260px]">
        <svg viewBox={`0 0 ${width} ${height}`} className="w-full h-full text-xs">
          {/* Grid lines */}
          {[0, 0.25, 0.5, 0.75, 1].map((ratio, i) => {
            const y = padTop + chartH * (1 - ratio);
            const valLeft = (ratio * maxOrder).toFixed(0);
            const valRight = (ratio * maxOrder).toFixed(0);
            return (
              <g key={i}>
                <line x1={padLeft} y1={y} x2={width - padRight} y2={y} stroke="#f0f2f5" strokeWidth="1" />
                <text x={padLeft - 8} y={y + 4} textAnchor="end" fill="#999999" fontSize="10">
                  {valLeft}
                </text>
                <text x={width - padRight + 8} y={y + 4} textAnchor="start" fill="#999999" fontSize="10">
                  {valRight}
                </text>
              </g>
            );
          })}

          {/* Axis Labels */}
          <text x={padLeft - 8} y={padTop - 6} textAnchor="end" fill="#666666" fontSize="11">笔</text>
          <text x={width - padRight + 8} y={padTop - 6} textAnchor="start" fill="#666666" fontSize="11">笔</text>

          {/* X Axis bottom baseline */}
          <line x1={padLeft} y1={padTop + chartH} x2={width - padRight} y2={padTop + chartH} stroke="#dcdfe6" strokeWidth="1" />

          {/* Month Label */}
          <text x={padLeft - 10} y={height - 10} textAnchor="end" fill="#333333" fontSize="11" fontWeight="500">
            {month}月
          </text>

          {/* Days on X Axis */}
          {Array.from({ length: days.length }, (_, i) => {
            const dayNum = i + 1;
            const x = padLeft + (i / (count - 1)) * chartW;
            const isLabel = dayNum === 1 || dayNum % 3 === 0 || dayNum === days.length;
            return (
              <g key={i}>
                <line x1={x} y1={padTop + chartH} x2={x} y2={padTop + chartH + 4} stroke="#dcdfe6" strokeWidth="1" />
                {isLabel && (
                  <text x={x} y={height - 10} textAnchor="middle" fill="#999999" fontSize="10">
                    {String(dayNum).padStart(2, "0")}
                  </text>
                )}
              </g>
            );
          })}

          {/* Paid Orders Line (Blue) */}
          <path d={pathD} fill="none" stroke="#316eea" strokeWidth="2" />
          {points.map((p, i) => (
            <circle key={i} cx={p.x} cy={p.y} r={p.val > 0 ? "3.5" : "1"} fill="#316eea" stroke="#ffffff" strokeWidth="1">
              <title>{`${p.date}: ${p.val}笔已付`}</title>
            </circle>
          ))}
        </svg>
      </div>
    </div>
  );
}

function formatAuditAction(item: AuditEvent): string {
  const action = item.action || "";
  if (action === "supplier.delivery.succeeded") return `模块命令:交付成功#Host ID:${item.entity_id}`;
  if (action === "supplier.delivery.failed") return `模块命令:交付失败#Host ID:${item.entity_id}`;
  if (action.includes("customer.impersonated")) return `以该客户登录 - User ID:${item.entity_id} - 成功`;
  if (action.includes("product")) return `${action} - Product ID:${item.entity_id}`;
  if (action.includes("service")) return `主机配置修改 Host ID:${item.entity_id}`;
  if (action.includes("order")) return `订单变更 Order ID:${item.entity_id}`;
  if (action.includes("invoice")) return `账单变更 Invoice ID:${item.entity_id}`;
  return `${action} #${item.entity_type}:${item.entity_id}`;
}

function formatDateTime(iso: string): string {
  if (!iso) return "";
  try {
    const d = new Date(iso);
    const y = d.getFullYear();
    const m = String(d.getMonth() + 1).padStart(2, "0");
    const day = String(d.getDate()).padStart(2, "0");
    const h = String(d.getHours()).padStart(2, "0");
    const min = String(d.getMinutes()).padStart(2, "0");
    return `${y}-${m}-${day} ${h}:${min}`;
  } catch {
    return iso.slice(0, 16).replace("T", " ");
  }
}

function formatMonthDay(iso: string): string {
  if (!iso) return "";
  try {
    const d = new Date(iso);
    const m = String(d.getMonth() + 1).padStart(2, "0");
    const day = String(d.getDate()).padStart(2, "0");
    return `${m}月${day}日`;
  } catch {
    return "";
  }
}
