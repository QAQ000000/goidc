"use client";

import { useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import Link from "next/link";
import {
  ArrowLeft,
  LogIn,
  Save,
  Trash2,
  CheckCircle2,
  Pencil,
  Plus,
  LifeBuoy,
  Clock,
  CheckCircle,
  XCircle,
} from "lucide-react";
import { api, date, money, type Contact, type User } from "@/lib/api";
import { transactionReturnHref } from "@/lib/transaction-return";
import { useResource } from "@/hooks/use-resource";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Checkbox } from "./ui/checkbox";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "./ui/dialog";
import { Table, TableHeader, TableHead, TableBody, TableRow, TableCell } from "./ui/table";
import { Failure, Loading, Status } from "./common";
import { Workspace, PermissionGate, useAdminPermission } from "./workspace";
import { CustomerFieldInputs, customerFieldValues, useCustomerFieldDefinitions, useCustomerMutation, useCustomerDraft, CustomerDraftNotice, CustomerMutationMessage } from "./customer-field-inputs";
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
  sales_version: number;
  group_id: number | null;
  group_name: string | null;
  active_services_count?: number;
  total_services_count?: number;
  income_cents?: number;
  expense_cents?: number;
  company_name?: string | null;
  phone_number?: string | null;
  qq?: string | null;
  gender: string;
  country: string;
  province: string;
  address: string;
  postcode: string;
  source_channel: string;
  payment_method: string;
  language: string;
  admin_note: string;
  receive_marketing: boolean;
  custom_fields: Record<string, unknown>;
  profile_version: number;
};

type CustomerDetailTab =
  | "summary"
  | "profile"
  | "services"
  | "invoices"
  | "transactions"
  | "contacts"
  | "tickets"
  | "orders"
  | "files"
  | "affiliate";

const customerDetailTabLabel: Record<CustomerDetailTab, string> = {
  summary: "客户摘要",
  profile: "个人资料",
  services: "产品/服务",
  invoices: "账单",
  transactions: "交易记录",
  contacts: "联系人",
  tickets: "工单",
  orders: "订单",
  files: "附件",
  affiliate: "推介计划",
};

export function AdminCustomerDetailView({ customerId }: { customerId: number }) {
  const searchParams = useSearchParams();
  const requestedTab = searchParams.get("tab");
  const tab: CustomerDetailTab =
    requestedTab && Object.hasOwn(customerDetailTabLabel, requestedTab)
      ? (requestedTab as CustomerDetailTab)
      : "summary";
  const backParams = new URLSearchParams(searchParams.toString());
  backParams.delete("tab");
  const transactionBack = transactionReturnHref(searchParams.get("return_to"));
  const returnHref = transactionBack ?? `/admin/customers${backParams.toString() ? `?${backParams.toString()}` : ""}`;
  const customer = useResource<AdminCustomer>(`/admin/customers/${customerId}`, false);

  return (
    <Workspace title="客户详情" admin>
      {customer.error ? (
        <Failure error={customer.error} retry={customer.refresh} />
      ) : !customer.data ? (
        <Loading />
      ) : (
        <div className="space-y-4">
          {/* Card Container matching MoFang #/customer-view/abstract */}
          <div className="bg-white rounded border border-[#ebeef5] shadow-sm overflow-hidden">
            {/* Top horizontal tab nav matching MoFang */}
            <div className="border-b border-[#ebeef5] px-4 pt-1 flex items-center justify-between overflow-x-auto">
              <nav className="flex space-x-6 min-w-max" aria-label="客户详情分区">
                {(
                  [
                    "summary",
                    "profile",
                    "services",
                    "invoices",
                    "transactions",
                    "contacts",
                    "tickets",
                    "orders",
                    "files",
                    "affiliate",
                  ] as CustomerDetailTab[]
                ).map((value) => {
                  const params = new URLSearchParams(searchParams.toString());
                  if (value === "summary") params.delete("tab");
                  else params.set("tab", value);
                  const href = `/admin/customers/${customerId}${params.toString() ? `?${params.toString()}` : ""}`;
                  const label = customerDetailTabLabel[value];
                  const isActive = value === tab;
                  return (
                    <Link
                      key={value}
                      href={href}
                      className={`py-3 text-sm font-medium border-b-2 transition-colors whitespace-nowrap ${
                        isActive
                          ? "border-[#409eff] text-[#409eff]"
                          : "border-transparent text-[#606266] hover:text-[#409eff]"
                      }`}
                      aria-current={isActive ? "page" : undefined}
                    >
                      {label}
                    </Link>
                  );
                })}
              </nav>

              <div className="flex items-center gap-2 pl-4 py-2">
                <Button variant="outline" size="sm" asChild className="h-7 text-xs border-[#dcdfe6]">
                  <Link href={returnHref}>
                    <ArrowLeft size={13} className="mr-1" />
                    {transactionBack ? "返回交易流水" : "返回客户列表"}
                  </Link>
                </Button>
              </div>
            </div>

            {/* Tab content */}
            <div className="p-5">
              {tab === "summary" ? (
                <CustomerSummaryView customer={customer.data} refresh={customer.refresh} />
              ) : tab === "profile" ? (
                <CustomerProfile
                  key={`${customerId}:${customer.data.profile_version}`}
                  customer={customer.data}
                  saved={customer.refresh}
                  cancelHref={`/admin/customers/${customerId}${backParams.toString() ? `?${backParams.toString()}` : ""}`}
                />
              ) : tab === "contacts" ? (
                <ContactsPanel adminCustomerId={customerId} />
              ) : tab === "files" ? (
                <CustomerFileManager customerId={customerId} />
              ) : (
                <AdminCustomerRelatedView
                  key={`${customerId}:${tab}`}
                  customerId={customerId}
                  tab={tab}
                />
              )}
            </div>
          </div>
        </div>
      )}
    </Workspace>
  );
}

function CustomerSummaryView({
  customer,
  refresh,
}: {
  customer: AdminCustomer;
  refresh: () => void;
}) {
  const manage = useAdminPermission("customers.manage");
  const [busyStatus, setBusyStatus] = useState(false);
  const [statusError, setStatusError] = useState("");
  const [topupOpen, setTopupOpen] = useState(false);
  const [topupAmount, setTopupAmount] = useState("");
  const [topupBusy, setTopupBusy] = useState(false);
  const [topupMessage, setTopupMessage] = useState("");

  const [registrationDays] = useState(() =>
    Math.max(
      1,
      Math.floor((Date.now() - new Date(customer.created_at).getTime()) / 86400000)
    )
  );

  async function toggleStatus() {
    if (!manage || busyStatus) return;
    setBusyStatus(true);
    setStatusError("");
    const nextStatus = customer.status === "active" ? "disabled" : "active";
    try {
      await api(`/admin/customers/${customer.id}`, {
        method: "PATCH",
        body: JSON.stringify({ status: nextStatus }),
      });
      refresh();
    } catch (e) {
      setStatusError((e as Error).message || "客户状态更新失败");
    } finally {
      setBusyStatus(false);
    }
  }

  async function handleTopup(e: React.FormEvent) {
    e.preventDefault();
    const cents = Math.round(Number(topupAmount) * 100);
    if (!Number.isSafeInteger(cents) || cents < 1) {
      setTopupMessage("金额无效");
      return;
    }
    setTopupBusy(true);
    setTopupMessage("");
    try {
      await api(`/admin/customers/${customer.id}/balance`, {
        method: "POST",
        body: JSON.stringify({ amount_cents: cents, note: "客户详情余额管理" }),
        headers: { "Idempotency-Key": `web-detail-topup-${customer.id}-${crypto.randomUUID()}` },
      });
      setTopupAmount("");
      setTopupOpen(false);
      refresh();
    } catch (err) {
      setTopupMessage((err as Error).message);
    } finally {
      setTopupBusy(false);
    }
  }

  return (
    <div className="grid grid-cols-1 lg:grid-cols-[280px_1fr] gap-6">
      {/* Left Column: Customer Identity Card matching MoFang */}
      <div className="space-y-4 border-r border-[#ebeef5] pr-5">
        <div className="flex items-center gap-2">
          <span className="text-xl font-bold text-[#303133]">{customer.id}</span>
          <span className="inline-flex items-center px-1.5 py-0.5 rounded text-[11px] bg-[#f4f4f5] text-[#909399] border border-[#e9e9eb]">
            未认证
          </span>
          <Link
            href="/admin/certifications"
            title="去认证"
            className="text-[#409eff] hover:opacity-80 ml-auto"
          >
            <Pencil size={13} />
          </Link>
        </div>

        <div className="text-xs text-[#909399] space-y-1">
          <div className="flex justify-between">
            <span>提交人姓名</span>
            <span>-</span>
          </div>
          <div className="flex justify-between">
            <span>身份证号</span>
            <span>-</span>
          </div>
        </div>

        <hr className="border-[#ebeef5]" />

        <div className="text-xs text-[#606266] space-y-2.5">
          <div className="flex justify-between">
            <span className="text-[#909399]">公司</span>
            <span className="text-[#303133]">{customer.company_name || "-"}</span>
          </div>
          <div className="flex justify-between">
            <span className="text-[#909399]">QQ</span>
            <span className="text-[#303133]">{customer.qq || "-"}</span>
          </div>
          <div className="flex justify-between">
            <span className="text-[#909399]">手机</span>
            <span className="text-[#303133]">{customer.phone_number || "-"}</span>
          </div>
          <div className="flex items-center justify-between">
            <span className="text-[#909399]">邮箱</span>
            <span className="text-[#303133] flex items-center gap-1">
              {customer.email}
              <CheckCircle2 size={13} className="text-[#67c23a]" />
            </span>
          </div>
          <div className="flex items-center justify-between">
            <span className="text-[#909399]">状态</span>
            <button
              type="button"
              role="switch"
              aria-checked={customer.status === "active"}
              disabled={!manage || busyStatus}
              onClick={toggleStatus}
              className={`relative inline-flex h-5 w-10 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none ${
                customer.status === "active" ? "bg-[#409eff]" : "bg-gray-300"
              }`}
            >
              <span
                className={`pointer-events-none inline-block h-4 w-4 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out ${
                  customer.status === "active" ? "translate-x-5" : "translate-x-0"
                }`}
              />
            </button>
          </div>
          {statusError && (
            <p role="alert" className="text-xs text-destructive text-right">
              {statusError}
            </p>
          )}
          <div className="flex justify-between">
            <span className="text-[#909399]">分组</span>
            <span className="text-[#303133]">{customer.group_name || "无"}</span>
          </div>
          <div className="flex justify-between">
            <span className="text-[#909399]">销售</span>
            <span className="text-[#303133]">{customer.sales_owner_name || "-"}</span>
          </div>
          <div className="flex justify-between">
            <span className="text-[#909399]">创建时间</span>
            <span className="text-[#303133]">{date(customer.created_at)}</span>
          </div>
          <div className="flex justify-between">
            <span className="text-[#909399]">注册时长</span>
            <span className="text-[#303133]">{registrationDays}天</span>
          </div>
          <div className="flex justify-between">
            <span className="text-[#909399]">推介人信息</span>
            <span className="text-[#303133]">-</span>
          </div>
          <div className="flex justify-between">
            <span className="text-[#909399]">最后登录时间</span>
            <span className="text-[#303133]">-</span>
          </div>
          <div className="flex justify-between">
            <span className="text-[#909399]">最后登录IP</span>
            <span className="text-[#303133] font-mono">-</span>
          </div>
          <div className="flex items-center justify-between">
            <span className="text-[#909399]">管理员备注</span>
            <Link
              href={`/admin/customers/${customer.id}?tab=profile`}
              title="编辑管理员备注"
              className="text-[#409eff] hover:opacity-80"
            >
              <Pencil size={13} />
            </Link>
          </div>
          {customer.admin_note && <p className="text-xs whitespace-pre-wrap break-words text-[#606266]">{customer.admin_note}</p>}
        </div>

        {/* Impersonate button */}
        <div className="pt-2">
          <ImpersonateCustomerButton customerId={customer.id} />
        </div>
      </div>

      {/* Right Column: Statistics & Actions */}
      <div className="space-y-6 min-w-0">
        {/* Top Grid: Support Tickets & Invoices */}
        <div className="grid grid-cols-1 xl:grid-cols-2 gap-5">
          {/* 工单卡片 */}
          <div className="border border-[#ebeef5] rounded p-4 bg-white">
            <h3 className="text-sm font-semibold text-[#303133] mb-4">工单</h3>
            <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
              <div className="flex items-center gap-2.5">
                <div className="w-9 h-9 rounded-full bg-blue-100 text-[#409eff] flex items-center justify-center">
                  <LifeBuoy size={18} />
                </div>
                <div>
                  <div className="text-[11px] text-[#909399]">工单数(单)</div>
                  <div className="text-base font-semibold text-[#409eff]">0</div>
                </div>
              </div>
              <div className="flex items-center gap-2.5">
                <div className="w-9 h-9 rounded-full bg-amber-100 text-[#e6a23c] flex items-center justify-center">
                  <Clock size={18} />
                </div>
                <div>
                  <div className="text-[11px] text-[#909399]">处理中(单)</div>
                  <div className="text-base font-semibold text-[#e6a23c]">0</div>
                </div>
              </div>
              <div className="flex items-center gap-2.5">
                <div className="w-9 h-9 rounded-full bg-green-100 text-[#67c23a] flex items-center justify-center">
                  <CheckCircle size={18} />
                </div>
                <div>
                  <div className="text-[11px] text-[#909399]">已回复(单)</div>
                  <div className="text-base font-semibold text-[#67c23a]">0</div>
                </div>
              </div>
              <div className="flex items-center gap-2.5">
                <div className="w-9 h-9 rounded-full bg-gray-100 text-[#909399] flex items-center justify-center">
                  <XCircle size={18} />
                </div>
                <div>
                  <div className="text-[11px] text-[#909399]">已关闭(单)</div>
                  <div className="text-base font-semibold text-[#909399]">0</div>
                </div>
              </div>
            </div>
          </div>

          {/* 财务账单卡片 */}
          <div className="border border-[#ebeef5] rounded p-4 bg-white space-y-3">
            <div className="flex items-center justify-between">
              <h3 className="text-sm font-semibold text-[#303133]">财务账单</h3>
              <Button
                asChild
                size="sm"
                className="h-7 text-xs bg-[#409eff] hover:bg-[#337ecc] text-white px-2.5"
              >
                <Link href={`/admin/invoices/new?customer_id=${customer.id}`}>
                  <Plus size={12} className="mr-1" />
                  添加账单
                </Link>
              </Button>
            </div>

            {/* 6 metrics matching MoFang */}
            <div className="grid grid-cols-2 sm:grid-cols-3 gap-2.5">
              {/* 总收入 - blue card */}
              <div className="bg-[#409eff] text-white rounded p-2.5 flex flex-col justify-between">
                <span className="text-[11px] text-white/80">总收入</span>
                <div className="mt-1">
                  <div className="text-base font-bold">
                    ¥{((customer.income_cents ?? 0) / 100).toFixed(2)}
                  </div>
                  <div className="text-[10px] text-white/70">0/笔</div>
                </div>
              </div>

              {/* 总支出 */}
              <div className="bg-[#f5f7fa] border border-[#ebeef5] rounded p-2.5 flex flex-col justify-between">
                <span className="text-[11px] text-[#909399]">总支出</span>
                <div className="mt-1">
                  <div className="text-base font-bold text-[#303133]">
                    ¥{((customer.expense_cents ?? 0) / 100).toFixed(2)}
                  </div>
                  <div className="text-[10px] text-[#909399]">0/笔</div>
                </div>
              </div>

              {/* 未支付 */}
              <div className="bg-[#f5f7fa] border border-[#ebeef5] rounded p-2.5 flex flex-col justify-between">
                <span className="text-[11px] text-[#909399]">未支付</span>
                <div className="mt-1">
                  <div className="text-base font-bold text-[#303133]">¥0.00</div>
                  <div className="text-[10px] text-[#909399]">0/笔</div>
                </div>
              </div>

              {/* 即将到期产品 */}
              <div className="bg-[#f5f7fa] border border-[#ebeef5] rounded p-2.5 flex flex-col justify-between">
                <span className="text-[11px] text-[#909399]">即将到期产品</span>
                <div className="mt-1">
                  <div className="text-base font-bold text-[#303133]">¥0.00</div>
                  <div className="text-[10px] text-[#909399]">0/笔</div>
                </div>
              </div>

              {/* 已到期产品 */}
              <div className="bg-[#f5f7fa] border border-[#ebeef5] rounded p-2.5 flex flex-col justify-between">
                <span className="text-[11px] text-[#909399]">已到期产品</span>
                <div className="mt-1">
                  <div className="text-base font-bold text-[#303133]">¥0.00</div>
                  <div className="text-[10px] text-[#909399]">0/笔</div>
                </div>
              </div>

              {/* 余额 */}
              <div className="bg-[#f5f7fa] border border-[#ebeef5] rounded p-2.5 flex flex-col justify-between">
                <div className="flex items-center justify-between">
                  <span className="text-[11px] text-[#909399]">余额</span>
                  <button
                    type="button"
                    onClick={() => setTopupOpen(true)}
                    className="text-[10px] bg-pink-100 hover:bg-pink-200 text-pink-600 px-1.5 py-0.5 rounded font-medium"
                  >
                    管理余额
                  </button>
                </div>
                <div className="mt-1">
                  <div className="text-base font-bold text-[#303133]">
                    ¥{(customer.balance_cents / 100).toFixed(2)}
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>

        {/* Bottom Section: 产品/服务 matching MoFang */}
        <div className="border border-[#ebeef5] rounded p-4 bg-white space-y-4">
          <div className="flex items-center justify-between">
            <h3 className="text-sm font-semibold text-[#303133]">产品/服务</h3>
            <div className="flex items-center gap-2">
              <Button
                asChild
                size="sm"
                variant="outline"
                className="h-7 text-xs border-[#dcdfe6] text-[#409eff]"
              >
                <Link href={`/admin/orders?customer_id=${customer.id}`}>查看订单</Link>
              </Button>
              <Button
                asChild
                size="sm"
                className="h-7 text-xs bg-[#409eff] hover:bg-[#337ecc] text-white"
              >
                <Link href="/admin/orders">添加订单</Link>
              </Button>
            </div>
          </div>

          {/* Service badges matching MoFang */}
          <div className="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-5 lg:grid-cols-9 gap-3 text-center">
            {[
              { title: "独立服务器", current: 0, total: 0 },
              { title: "虚拟主机", current: 0, total: 0 },
              { title: "其他服务", current: 0, total: 0 },
              {
                title: "魔方云",
                current: customer.active_services_count ?? 0,
                total: customer.total_services_count ?? 0,
                highlight: (customer.total_services_count ?? 0) > 0,
              },
              { title: "魔方 DCIM", current: 0, total: 0 },
              { title: "CDN", current: 0, total: 0 },
              { title: "软件产品", current: 0, total: 0 },
              { title: "云服务器", current: 0, total: 0 },
              { title: "ssl证书", current: 0, total: 0 },
            ].map((s) => (
              <div key={s.title} className="flex flex-col items-center">
                <div
                  className={`w-12 h-12 rounded-full border-2 flex items-center justify-center text-xs font-semibold mb-1 ${
                    s.highlight
                      ? "border-[#e6a23c] text-[#e6a23c] bg-amber-50"
                      : "border-[#dcdfe6] text-[#909399] bg-[#f5f7fa]"
                  }`}
                >
                  {s.current} / {s.total}
                </div>
                <span className="text-[11px] text-[#606266] font-medium">{s.title}</span>
                <span className="text-[10px] text-[#909399]">开通</span>
              </div>
            ))}
          </div>

          {/* Progress bar */}
          <div className="pt-2">
            <div className="h-2 bg-[#f5f7fa] rounded-full overflow-hidden border border-[#ebeef5]">
              <div
                className="h-full bg-[#e6a23c] rounded-full transition-all"
                style={{
                  width: `${
                    customer.total_services_count
                      ? Math.min(
                          100,
                          Math.round(
                            ((customer.active_services_count ?? 0) /
                              customer.total_services_count) *
                              100
                          )
                        )
                      : 0
                  }%`,
                }}
              />
            </div>
            <div className="flex justify-between items-center text-xs text-[#909399] mt-1.5">
              <span className="font-medium text-[#303133]">
                {customer.active_services_count ?? 0}/{customer.total_services_count ?? 0}
              </span>
              <span>使用中</span>
            </div>
          </div>
        </div>
      </div>

      {/* Topup Dialog */}
      <Dialog open={topupOpen} onOpenChange={setTopupOpen}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>余额管理 · 充值</DialogTitle>
            <DialogDescription className="text-xs text-[#909399]">
              为客户 #{customer.id} 进行账户余额充值
            </DialogDescription>
          </DialogHeader>
          <form onSubmit={handleTopup} className="space-y-4">
            <div>
              <label className="block text-xs text-[#606266] mb-1">
                充值金额（元）· 当前余额: ¥{(customer.balance_cents / 100).toFixed(2)}
              </label>
              <Input
                type="number"
                min="0.01"
                step="0.01"
                value={topupAmount}
                onChange={(e) => setTopupAmount(e.target.value)}
                placeholder="请输入充值金额"
                required
                className="text-xs h-8 border-[#dcdfe6]"
              />
            </div>
            {topupMessage && (
              <p role="status" className="text-xs text-red-500">
                {topupMessage}
              </p>
            )}
            <div className="flex justify-end gap-2 pt-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setTopupOpen(false)}
                className="text-xs border-[#dcdfe6]"
              >
                取消
              </Button>
              <Button
                disabled={topupBusy || !topupAmount}
                size="sm"
                className="bg-[#409eff] hover:bg-[#337ecc] text-white text-xs"
              >
                {topupBusy ? "处理中..." : "确认充值"}
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function ImpersonateCustomerButton({ customerId }: { customerId: number }) {
  const router = useRouter();
  const manage = useAdminPermission("customers.manage");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");

  async function start() {
    if (
      !manage ||
      busy ||
      !window.confirm("将以该客户身份进入前台，原管理员会话会保留，退出后可恢复。继续吗？")
    )
      return;
    setBusy(true);
    setMessage("");
    try {
      await api(`/admin/customers/${customerId}/impersonate`, { method: "POST", body: "{}" });
      router.push("/console/services");
      router.refresh();
    } catch (error) {
      setMessage((error as Error).message);
      setBusy(false);
    }
  }

  return (
    <div className="w-full">
      <PermissionGate permission="customers.manage">
        <Button
          type="button"
          disabled={busy}
          onClick={() => void start()}
          className="w-full bg-[#409eff] hover:bg-[#337ecc] text-white text-xs h-9 rounded font-normal"
        >
          <LogIn size={14} className="mr-1.5" />
          {busy ? "切换中..." : "以该客户登录"}
        </Button>
      </PermissionGate>
      {message && (
        <span role="alert" className="text-xs text-destructive mt-1 block">
          {message}
        </span>
      )}
    </div>
  );
}

function CustomerProfile({
  customer,
  saved,
  cancelHref,
}: {
  customer: AdminCustomer;
  saved: () => void;
  cancelHref: string;
}) {
  const manage = useAdminPermission("customers.manage");
  const [name, setName] = useState(customer.display_name);
  const [email, setEmail] = useState(customer.email);
  const [status, setStatus] = useState(customer.status);
  const [profile, setProfile] = useState({
    gender: customer.gender, company_name: customer.company_name || "", phone: customer.phone_number || "",
    qq: customer.qq || "", country: customer.country, province: customer.province, address: customer.address,
    postcode: customer.postcode, source_channel: customer.source_channel, payment_method: customer.payment_method,
    language: customer.language, admin_note: customer.admin_note, receive_marketing: customer.receive_marketing,
  });
  const user = useResource<User>("/me", false);
  const definitions = useCustomerFieldDefinitions(true);
  const [customFields, setCustomFields] = useState<Record<string, unknown>>(customer.custom_fields || {});
  const [profileVersion, setProfileVersion] = useState(customer.profile_version);
  const mutation = useCustomerMutation();
  const { busy, uncertain } = mutation;
  const [message, setMessage] = useState("");
  const snapshot = { name, email, status, profile, customFields };
  const [initial] = useState(JSON.stringify(snapshot));
  const dirty = initial !== JSON.stringify(snapshot);
  const draft = useCustomerDraft(manage && user.data ? `customer-profile:${user.data.id}:${customer.id}` : null, snapshot, dirty);
  const router = useRouter();
  function cancel() { if (busy || dirty && !window.confirm("确认取消更改并丢弃草稿？")) return; draft.leave(); router.push(cancelHref); }
  function restoreDraft() {
    if (!draft.draft) return;
    setName(draft.draft.name); setEmail(draft.draft.email); setStatus(draft.draft.status); setProfile(draft.draft.profile); setCustomFields(draft.draft.customFields); draft.clear();
  }
  async function updateVersion() {
    try { const latest = await api<AdminCustomer>(`/admin/customers/${customer.id}`); setProfileVersion(latest.profile_version); definitions.refresh(); setMessage("最新版本已读取，草稿已保留。请核对后重新保存。"); } catch (e) { mutation.setError((e as Error).message); }
  }
  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!manage || busy || !definitions.ready && !uncertain) return;
    setMessage("");
    try {
      const result = await mutation.run(`/admin/customers/${customer.id}`, "PATCH", uncertain ? {} : { display_name: name, email, status, expected_version: profileVersion, profile: { ...profile, custom_fields: customerFieldValues(definitions.fields, customFields), custom_fields_version: definitions.version } });
      if (result) { draft.leave(); setMessage("客户资料已保存"); saved(); }
    } catch (e) { mutation.setError((e as Error).message); }
  }

  const fieldStyle = "block text-xs text-[#606266] min-w-0";
  const textFields = [
    ["company_name", "所在公司"], ["phone", "手机"], ["qq", "QQ"], ["country", "国家"],
    ["province", "省"], ["address", "地址"], ["postcode", "邮编"], ["source_channel", "了解途径"],
  ] as const;

  return (
    <form onSubmit={(event) => void save(event)} className="space-y-6">
      <CustomerDraftNotice available={!!draft.draft} restore={restoreDraft} discard={draft.clear} disabled={mutation.frozen} />
      <fieldset disabled={!manage || mutation.frozen} className="space-y-6">
        <section className="bg-[#f9fafc] p-4 rounded border border-[#ebeef5]">
          <h2 className="text-sm font-semibold text-[#303133] mb-3">个人资料</h2>
          <div className="grid sm:grid-cols-2 xl:grid-cols-4 gap-4">
            {textFields.map(([key, label]) => <label key={key} className={fieldStyle}>{label}
              <Input className="mt-1.5 text-xs h-8 border-[#dcdfe6]" value={profile[key]} onChange={event => setProfile(previous => ({...previous, [key]: event.target.value}))} />
            </label>)}
            <label className={fieldStyle}>性别
              <Select value={profile.gender} onValueChange={value => setProfile(previous => ({...previous, gender: value}))}>
                <SelectTrigger className="mt-1.5 h-8"><SelectValue /></SelectTrigger>
                <SelectContent><SelectItem value="unknown">未知</SelectItem><SelectItem value="male">男</SelectItem><SelectItem value="female">女</SelectItem></SelectContent>
              </Select>
            </label>
            <label className={fieldStyle}>
              姓名
              <Input
                className="mt-1.5 text-xs h-8 border-[#dcdfe6]"
                value={name}
                required
                maxLength={120}
                onChange={(event) => setName(event.target.value)}
              />
            </label>
          </div>
        </section>



        <section className="bg-[#f9fafc] p-4 rounded border border-[#ebeef5]">
          <h2 className="text-sm font-semibold text-[#303133] mb-3">账户信息</h2>
          <div className="grid sm:grid-cols-2 xl:grid-cols-4 gap-4">
            <label className={fieldStyle}>
              账户
              <Input
                className="mt-1.5 text-xs h-8 border-[#dcdfe6] bg-gray-100"
                value={customer.username}
                readOnly
                disabled
              />
            </label>
            <label className={fieldStyle}>
              邮箱
              <Input
                className="mt-1.5 text-xs h-8 border-[#dcdfe6]"
                value={email}
                type="email"
                required
                maxLength={254}
                onChange={(event) => setEmail(event.target.value)}
              />
            </label>
          </div>
        </section>

        <section className="bg-[#f9fafc] p-4 rounded border border-[#ebeef5]">
          <h2 className="text-sm font-semibold text-[#303133] mb-3">其他设置</h2>
          <div className="grid sm:grid-cols-2 xl:grid-cols-4 gap-4">
            <label className={fieldStyle}>默认支付方式
              <Select value={profile.payment_method} onValueChange={value => setProfile(previous => ({...previous, payment_method: value}))}>
                <SelectTrigger className="mt-1.5 h-8"><SelectValue /></SelectTrigger>
                <SelectContent><SelectItem value="bank">银行转账</SelectItem><SelectItem value="alipay">支付宝</SelectItem><SelectItem value="wechat">微信支付</SelectItem></SelectContent>
              </Select>
            </label>
            <label className={fieldStyle}>语言
              <Select value={profile.language} onValueChange={value => setProfile(previous => ({...previous, language: value}))}>
                <SelectTrigger className="mt-1.5 h-8"><SelectValue /></SelectTrigger>
                <SelectContent><SelectItem value="zh_CN">中文简体</SelectItem><SelectItem value="en_US">English</SelectItem></SelectContent>
              </Select>
            </label>
            <label className={fieldStyle}>管理员备注
              <textarea className="mt-1.5 w-full rounded border border-[#dcdfe6] p-2" value={profile.admin_note} maxLength={4000} onChange={event => setProfile(previous => ({...previous, admin_note: event.target.value}))} />
            </label>
            <label className="flex items-center gap-2 text-xs"><Checkbox checked={profile.receive_marketing} onCheckedChange={value => setProfile(previous => ({...previous, receive_marketing: value === true}))} />接收营销信息</label>
            <label className={fieldStyle}>
              状态
              <Select value={status} onValueChange={setStatus} disabled={!manage || busy}>
                <SelectTrigger className="mt-1.5 text-xs h-8 border-[#dcdfe6]" aria-label="客户状态">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="active">正常</SelectItem>
                  <SelectItem value="disabled">已停用</SelectItem>
                </SelectContent>
              </Select>
            </label>
            <label className={fieldStyle}>
              客户分组
              <Input
                className="mt-1.5 text-xs h-8 border-[#dcdfe6] bg-gray-100"
                value={customer.group_name || "未分组"}
                disabled
              />
            </label>
            <label className={fieldStyle}>
              销售
              <Input
                className="mt-1.5 text-xs h-8 border-[#dcdfe6] bg-gray-100"
                value={customer.sales_owner_name || "无归属"}
                disabled
              />
            </label>
          </div>
        </section>
      </fieldset>

        <CustomerFieldInputs definitions={definitions} values={customFields} onChange={setCustomFields} disabled={!manage || mutation.frozen} />
      <CustomerMutationMessage mutation={mutation} />
      {message && <p role="status" className="text-xs text-green-700">{message}</p>}
      {mutation.conflict && <Button type="button" variant="outline" disabled={busy} onClick={() => void updateVersion()}>读取最新版本，保留当前输入</Button>}

      <div className="flex flex-wrap gap-3 pt-2">
        <PermissionGate permission="customers.manage">
          <Button
            disabled={busy || !definitions.ready && !uncertain}
            size="sm"
            className="bg-[#409eff] hover:bg-[#337ecc] text-white text-xs"
          >
            <Save size={13} className="mr-1" />
            {uncertain ? "使用原请求重试" : busy ? "保存中..." : "保存更改"}
          </Button>
        </PermissionGate>
        <Button
          variant="outline"
          size="sm"
          type="button"
          disabled={busy}
          onClick={cancel}
          className="text-xs border-[#dcdfe6]"
        >
          取消更改
        </Button>
      </div>
    </form>
  );
}

function AdminCustomerRelatedView({
  customerId,
  tab,
}: {
  customerId: number;
  tab: Exclude<CustomerDetailTab, "contacts" | "summary" | "profile" | "files">;
}) {
  const [page, setPage] = useState(0);
  const endpoint =
    tab === "transactions"
      ? `/admin/customer-transactions?customer_id=${customerId}&limit=25&offset=${page * 25}`
      : tab === "affiliate"
      ? `/admin/customers/${customerId}/affiliate?limit=25&offset=${page * 25}`
      : `/admin/${tab}?customer_id=${customerId}&limit=25&offset=${page * 25}`;
  const { data, error, refresh } = useResource<Row[]>(endpoint, false);

  if (error) return <Failure error={error} retry={refresh} />;
  if (!data) return <Loading />;
  if (!data.length)
    return (
      <section className="py-12 text-center text-muted-foreground text-xs">
        <p>该客户暂无{customerDetailTabLabel[tab]}</p>
        {page > 0 && (
          <Button
            className="mt-4 text-xs h-7"
            variant="outline"
            onClick={() => setPage((value) => value - 1)}
          >
            上一页
          </Button>
        )}
      </section>
    );

  const columns =
    tab === "services"
      ? ["服务", "域名", "状态", "下次到期"]
      : tab === "orders"
      ? ["订单号", "金额", "状态", "创建时间"]
      : tab === "invoices"
      ? ["账单号", "金额", "状态", "到期时间"]
      : tab === "tickets"
      ? ["工单号", "主题", "状态", "更新时间"]
      : tab === "affiliate"
      ? ["类型", "关联单据", "项目", "金额", "状态", "时间"]
      : ["类型", "关联账单", "渠道/类型", "金额", "状态", "时间"];

  return (
    <div className="overflow-x-auto">
      <Table>
        <TableHeader className="bg-[#f5f7fa]">
          <TableRow className="border-[#ebeef5]">
            {columns.map((column) => (
              <TableHead key={column} className="text-xs text-[#909399] font-medium py-2.5">
                {column}
              </TableHead>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>
          {data.map((row, index) => (
            <TableRow
              key={`${String(row.kind ?? tab)}:${String(row.id ?? index)}`}
              className="border-[#ebeef5] text-xs hover:bg-[#f5f7fa]"
            >
              {tab === "services" && (
                <>
                  <TableCell>
                    <Link
                      className="text-[#409eff] hover:underline"
                      href={`/admin/services/${Number(row.id)}`}
                    >
                      {String(row.name ?? "-")}
                    </Link>
                  </TableCell>
                  <TableCell className="font-mono text-[11px]">{String(row.domain ?? "-")}</TableCell>
                  <TableCell>
                    <Status value={String(row.status ?? "unknown")} />
                  </TableCell>
                  <TableCell>{row.next_due_at ? date(String(row.next_due_at)) : "-"}</TableCell>
                </>
              )}
              {tab === "orders" && (
                <>
                  <TableCell>
                    <Link
                      className="text-[#409eff] hover:underline"
                      href={`/admin/orders/${Number(row.id)}`}
                    >
                      {String(row.order_no ?? "-")}
                    </Link>
                  </TableCell>
                  <TableCell className="money">{money(Number(row.total_cents))}</TableCell>
                  <TableCell>
                    <Status value={String(row.status ?? "unknown")} />
                  </TableCell>
                  <TableCell>{date(String(row.created_at))}</TableCell>
                </>
              )}
              {tab === "invoices" && (
                <>
                  <TableCell>
                    <Link
                      className="text-[#409eff] hover:underline"
                      href={`/admin/invoices/${Number(row.id)}`}
                    >
                      {String(row.invoice_no ?? "-")}
                    </Link>
                  </TableCell>
                  <TableCell className="money">{money(Number(row.amount_cents))}</TableCell>
                  <TableCell>
                    <Status value={String(row.status ?? "unknown")} />
                  </TableCell>
                  <TableCell>{row.due_at ? date(String(row.due_at)) : "-"}</TableCell>
                </>
              )}
              {tab === "tickets" && (
                <>
                  <TableCell>
                    <Link
                      className="text-[#409eff] hover:underline"
                      href={`/admin/tickets/${Number(row.id)}`}
                    >
                      {String(row.ticket_no ?? "-")}
                    </Link>
                  </TableCell>
                  <TableCell className="max-w-sm break-words">{String(row.subject ?? "-")}</TableCell>
                  <TableCell>
                    <Status value={String(row.status ?? "unknown")} />
                  </TableCell>
                  <TableCell>{date(String(row.updated_at))}</TableCell>
                </>
              )}
              {tab === "transactions" && (
                <>
                  <TableCell>{row.kind === "payment" ? "支付" : row.kind === "external_refund" ? "外部退款到账" : "余额流水"}</TableCell>
                  <TableCell>
                    {row.invoice_id ? (
                      <Link
                        className="text-[#409eff] hover:underline"
                        href={`/admin/invoices/${Number(row.invoice_id)}`}
                      >
                        {String(row.invoice_no ?? "-")}
                      </Link>
                    ) : (
                      "-"
                    )}
                  </TableCell>
                  <TableCell>{row.label === "gateway_topup" ? "在线充值入账" : String(row.label ?? "-")}</TableCell>
                  <TableCell className="money">
                    {row.kind === "ledger" || row.kind === "external_refund"
                      ? `${Number(row.delta_cents) > 0 ? "+" : ""}${money(Number(row.delta_cents))}`
                      : money(Number(row.amount_cents))}
                  </TableCell>
                  <TableCell>
                    <Status value={String(row.status ?? "unknown")} />
                  </TableCell>
                  <TableCell>{date(String(row.created_at))}</TableCell>
                </>
              )}
              {tab === "affiliate" && (
                <>
                  <TableCell>{({ affiliate_account: "推广账户", affiliate_attribution: "推荐归因", affiliate_commission: "佣金", affiliate_withdrawal: "提现" } as Record<string, string>)[String(row.kind)] ?? "推广"}</TableCell>
                  <TableCell>{String(row.invoice_no || "-")}</TableCell>
                  <TableCell>{String(row.label ?? "-")}</TableCell>
                  <TableCell className="money">{money(Number(row.amount_cents ?? 0))}</TableCell>
                  <TableCell><Status value={String(row.status ?? "unknown")} /></TableCell>
                  <TableCell>{row.created_at ? date(String(row.created_at)) : "-"}</TableCell>
                </>
              )}
            </TableRow>
          ))}
        </TableBody>
      </Table>
      <div className="flex items-center justify-end gap-3 border-t border-[#ebeef5] p-3">
        <span className="text-xs text-[#909399]">第 {page + 1} 页</span>
        <Button
          variant="outline"
          size="sm"
          disabled={page === 0}
          onClick={() => setPage((value) => value - 1)}
          className="text-xs h-7 border-[#dcdfe6]"
        >
          上一页
        </Button>
        <Button
          variant="outline"
          size="sm"
          disabled={data.length < 25}
          onClick={() => setPage((value) => value + 1)}
          className="text-xs h-7 border-[#dcdfe6]"
        >
          下一页
        </Button>
      </div>
    </div>
  );
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
    setEditing(null);
    setUsername("");
    setEmail("");
    setCompanyName("");
    setPhone("");
    setStatus("inactive");
    setInvoiceEmails(false);
    setSupportEmails(false);
  }

  function edit(contact: Contact) {
    setEditing(contact.id);
    setUsername(contact.username);
    setEmail(contact.email);
    setCompanyName(contact.company_name);
    setPhone(contact.phone_number);
    setStatus(contact.status);
    setInvoiceEmails(contact.invoice_emails);
    setSupportEmails(contact.support_emails);
    setMessage("");
  }

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setMessage("");
    try {
      const body = JSON.stringify({
        username,
        email,
        company_name: companyName,
        phone_number: phone,
        status,
        invoice_emails: invoiceEmails,
        support_emails: supportEmails,
      });
      await api(editing ? `${root}/${editing}` : root, {
        method: editing ? "PATCH" : "POST",
        body,
      });
      reset();
      refresh();
    } catch (e) {
      setMessage((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function remove(id: number) {
    setBusy(true);
    setMessage("");
    try {
      await api(`${root}/${id}`, { method: "DELETE", body: "{}" });
      if (editing === id) reset();
      refresh();
    } catch (e) {
      setMessage((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="space-y-4">
      <div>
        <h2 className="text-sm font-semibold text-[#303133]">联系人管理</h2>
        <p className="text-xs text-[#909399] mt-0.5">
          维护账单和工单通知联系人。联系人邮箱不能与账户重复。
        </p>
      </div>

      <PermissionGate permission="customers.manage" bypass={!adminCustomerId}>
        <form
          className="grid grid-cols-1 sm:grid-cols-2 md:grid-cols-4 gap-3 bg-[#f9fafc] p-4 rounded border border-[#ebeef5]"
          onSubmit={submit}
        >
          <Input
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            placeholder="姓名"
            aria-label="联系人姓名"
            maxLength={120}
            className="text-xs h-8 border-[#dcdfe6]"
          />
          <Input
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            type="email"
            placeholder="邮箱"
            aria-label="联系人邮箱"
            required
            className="text-xs h-8 border-[#dcdfe6]"
          />
          <Input
            value={companyName}
            onChange={(e) => setCompanyName(e.target.value)}
            placeholder="公司"
            aria-label="联系人公司"
            className="text-xs h-8 border-[#dcdfe6]"
          />
          <Input
            value={phone}
            onChange={(e) => setPhone(e.target.value)}
            placeholder="电话"
            aria-label="联系人电话"
            className="text-xs h-8 border-[#dcdfe6]"
          />
          <Select value={status} onValueChange={(v) => setStatus(v as typeof status)}>
            <SelectTrigger className="text-xs h-8 border-[#dcdfe6]" aria-label="联系人状态">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="active">启用</SelectItem>
              <SelectItem value="inactive">未激活</SelectItem>
              <SelectItem value="disabled">停用</SelectItem>
            </SelectContent>
          </Select>
          <label className="flex items-center gap-2 text-xs text-[#606266]">
            <Checkbox
              checked={invoiceEmails}
              onCheckedChange={(v) => setInvoiceEmails(v === true)}
            />
            接收账单通知
          </label>
          <label className="flex items-center gap-2 text-xs text-[#606266]">
            <Checkbox
              checked={supportEmails}
              onCheckedChange={(v) => setSupportEmails(v === true)}
            />
            接收工单通知
          </label>
          <div className="flex items-center gap-2">
            <Button
              disabled={busy || !email.trim()}
              size="sm"
              className="bg-[#409eff] hover:bg-[#337ecc] text-white text-xs h-8"
            >
              {editing ? "保存联系人" : "添加联系人"}
            </Button>
            {editing && (
              <Button type="button" variant="ghost" size="sm" onClick={reset} className="text-xs h-8">
                取消
              </Button>
            )}
          </div>
        </form>
      </PermissionGate>

      {message && <p role="alert" className="text-xs text-destructive">{message}</p>}
      {error ? (
        <p role="alert" className="text-xs text-destructive">{error.message}</p>
      ) : !data ? (
        <Loading />
      ) : (
        <div className="overflow-x-auto border border-[#ebeef5] rounded">
          <Table>
            <TableHeader className="bg-[#f5f7fa]">
              <TableRow className="border-[#ebeef5]">
                <TableHead className="text-xs text-[#909399] font-medium py-2.5">姓名</TableHead>
                <TableHead className="text-xs text-[#909399] font-medium py-2.5">邮箱</TableHead>
                <TableHead className="text-xs text-[#909399] font-medium py-2.5">电话</TableHead>
                <TableHead className="text-xs text-[#909399] font-medium py-2.5">状态</TableHead>
                <TableHead className="text-xs text-[#909399] font-medium py-2.5">通知</TableHead>
                <TableHead className="text-xs text-[#909399] font-medium py-2.5 text-right" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={6} className="h-20 text-center text-xs text-[#909399]">
                    暂无联系人
                  </TableCell>
                </TableRow>
              ) : (
                data.map((contact) => (
                  <TableRow key={contact.id} className="border-[#ebeef5] text-xs hover:bg-[#f5f7fa]">
                    <TableCell>{contact.username || "未命名"}</TableCell>
                    <TableCell>{contact.email}</TableCell>
                    <TableCell>{contact.phone_number || "-"}</TableCell>
                    <TableCell>
                      <Status value={contact.status} />
                    </TableCell>
                    <TableCell className="text-[#909399]">
                      {[contact.invoice_emails && "账单", contact.support_emails && "工单"]
                        .filter(Boolean)
                        .join("、") || "无"}
                    </TableCell>
                    <TableCell className="text-right">
                      <PermissionGate permission="customers.manage" bypass={!adminCustomerId}>
                        <div className="flex items-center justify-end gap-1.5">
                          <Button
                            size="sm"
                            variant="outline"
                            onClick={() => edit(contact)}
                            className="h-7 text-xs border-[#dcdfe6]"
                          >
                            编辑
                          </Button>
                          <Button
                            size="icon-sm"
                            variant="ghost"
                            aria-label="删除联系人"
                            title="删除联系人"
                            disabled={busy}
                            onClick={() => void remove(contact.id)}
                            className="h-7 w-7 text-red-500 hover:text-red-700"
                          >
                            <Trash2 size={13} />
                          </Button>
                        </div>
                      </PermissionGate>
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </div>
      )}
    </section>
  );
}
