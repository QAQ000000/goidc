"use client";

import { useState } from "react";
import Link from "next/link";
import { Search, RefreshCw, Check, Pause, Eye, CreditCard } from "lucide-react";
import { api, date, money } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "./ui/dialog";
import { Table, TableHeader, TableHead, TableBody, TableRow, TableCell } from "./ui/table";
import { Failure, Loading } from "./common";
import { Workspace, PermissionGate } from "./workspace";
import { AdminPagination } from "./admin-pagination";

type AdminAccount = {
  id: number;
  user_id: number;
  username: string;
  email: string;
  account_type: string;
  status: string;
  program_id: number;
  name: string;
  level: number;
  version: number;
  created_at: string;
  updated_at: string;
};

type Attribution = {
  id: number;
  user_id: number;
  username: string;
  account_id: number;
  affiliate_user_id: number;
  affiliate_username: string;
  link_id: number;
  first_clicked_at: string;
  registered_at: string;
  status: string;
};

type Withdrawal = {
  id: number;
  withdrawal_no: string;
  amount_cents: number;
  fee_cents: number;
  payout_cents: number;
  status: string;
  payout_method: string;
  payout_target_masked: string;
  version: number;
  submitted_at: string;
  reviewed_at?: string | null;
  paid_at?: string | null;
  created_at: string;
};

type WithdrawalDetail = Withdrawal & {
  events: {
    id: number;
    event_type: string;
    before_status?: string;
    after_status: string;
    note: string;
    created_at: string;
  }[];
  allocations?: {
    entry_id: number;
    amount_cents: number;
    status: string;
    commission_status: string;
    invoice_id: number;
    invoice_no: string;
    product_id?: number | null;
    coalesce: string;
    event_type: string;
    rule_snapshot: Record<string, unknown>;
    available_at: string;
  }[];
};

const methodLabels: Record<string, string> = {
  bank: "银行卡",
  alipay: "支付宝",
  wechat: "微信",
  other: "其他",
};

const statusLabels: Record<string, { label: string; cls: string }> = {
  submitted: { label: "已提交", cls: "bg-[#fdf6ec] text-[#e6a23c] border-[#faecd8]" },
  reviewing: { label: "审核中", cls: "bg-[#fdf6ec] text-[#e6a23c] border-[#faecd8]" },
  approved: { label: "已通过", cls: "bg-[#f0f9eb] text-[#67c23a] border-[#e1f3d8]" },
  rejected: { label: "已驳回", cls: "bg-[#fef0f0] text-[#f56c6c] border-[#fde2e2]" },
  paying: { label: "打款中", cls: "bg-[#ecf5ff] text-[#409eff] border-[#d9ecff]" },
  paid: { label: "已打款", cls: "bg-[#f0f9eb] text-[#67c23a] border-[#e1f3d8]" },
  failed: { label: "失败", cls: "bg-[#fef0f0] text-[#f56c6c] border-[#fde2e2]" },
  cancelled: { label: "已取消", cls: "bg-[#f4f4f5] text-[#909399] border-[#e9e9eb]" },
};

export function AdminAffiliateView() {
  const accounts = useResource<AdminAccount[]>("/admin/affiliate/accounts?limit=100", false);
  const attributions = useResource<Attribution[]>("/admin/affiliate/attributions?limit=100", false);
  const withdrawals = useResource<Withdrawal[]>("/admin/affiliate/withdrawals?limit=100", false);

  const [tab, setTab] = useState<"accounts" | "withdrawals" | "attributions">("accounts");
  const [keyword, setKeyword] = useState("");
  const [selectedWithdrawal, setSelectedWithdrawal] = useState<Withdrawal | null>(null);
  const [payoutDialogWithdrawal, setPayoutDialogWithdrawal] = useState<Withdrawal | null>(null);

  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const refreshAll = () => {
    accounts.refresh();
    attributions.refresh();
    withdrawals.refresh();
  };

  async function changeAccountStatus(item: AdminAccount, status: string) {
    setBusy(true);
    setError("");
    setMessage("");
    try {
      await api(`/admin/affiliate/accounts/${item.id}/status`, {
        method: "POST",
        headers: { "Idempotency-Key": `affiliate-account-${item.id}-${Date.now()}` },
        body: JSON.stringify({ status, expected_version: item.version }),
      });
      setMessage("推广账户状态已更新");
      refreshAll();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  // Filter accounts
  const filteredAccounts = (accounts.data ?? []).filter((a) => {
    if (!keyword.trim()) return true;
    const q = keyword.trim().toLowerCase();
    return (
      a.username.toLowerCase().includes(q) ||
      a.email.toLowerCase().includes(q) ||
      a.name.toLowerCase().includes(q) ||
      String(a.id).includes(q)
    );
  });

  // Filter withdrawals
  const filteredWithdrawals = (withdrawals.data ?? []).filter((w) => {
    if (!keyword.trim()) return true;
    const q = keyword.trim().toLowerCase();
    return w.withdrawal_no.toLowerCase().includes(q) || String(w.id).includes(q);
  });

  // Filter attributions
  const filteredAttributions = (attributions.data ?? []).filter((at) => {
    if (!keyword.trim()) return true;
    const q = keyword.trim().toLowerCase();
    return (
      at.username.toLowerCase().includes(q) ||
      at.affiliate_username.toLowerCase().includes(q) ||
      String(at.id).includes(q)
    );
  });

  const pendingWithdrawalCount = (withdrawals.data ?? []).filter(
    (w) => w.status === "submitted" || w.status === "reviewing"
  ).length;

  return (
    <Workspace title="推介计划" admin>
      <div className="bg-white rounded border border-[#ebeef5] shadow-sm p-4 sm:p-5 space-y-4 max-w-full overflow-hidden">
        {/* Top guide text matching MoFang #/customer-promotionplan */}
        <div className="text-xs text-[#606266]">
          推介计划允许客户通过推广链接邀请好友，并根据商品规则结算推广佣金。{" "}
          <a
            href="https://bbs.idcsmart.com"
            target="_blank"
            rel="noreferrer"
            className="text-[#409eff] hover:underline ml-1"
          >
            帮助文档
          </a>
        </div>

        {/* Top horizontal tab nav matching MoFang */}
        <div className="border-b border-[#ebeef5]">
          <div className="flex space-x-6 text-sm font-medium">
            <button
              type="button"
              className={`pb-2 px-1 text-xs font-medium border-b-2 transition-colors ${
                tab === "accounts"
                  ? "border-[#409eff] text-[#409eff] font-semibold"
                  : "border-transparent text-[#606266] hover:text-[#409eff]"
              }`}
              onClick={() => setTab("accounts")}
            >
              推介计划 ({accounts.data?.length ?? 0})
            </button>
            <button
              type="button"
              className={`pb-2 px-1 text-xs font-medium border-b-2 transition-colors flex items-center gap-1.5 ${
                tab === "withdrawals"
                  ? "border-[#409eff] text-[#409eff] font-semibold"
                  : "border-transparent text-[#606266] hover:text-[#409eff]"
              }`}
              onClick={() => setTab("withdrawals")}
            >
              <span>提现审核</span>
              {pendingWithdrawalCount > 0 && (
                <span className="px-1.5 py-0.2 bg-[#e6a23c] text-white rounded-full text-[10px] font-semibold">
                  {pendingWithdrawalCount}
                </span>
              )}
            </button>
            <button
              type="button"
              className={`pb-2 px-1 text-xs font-medium border-b-2 transition-colors ${
                tab === "attributions"
                  ? "border-[#409eff] text-[#409eff] font-semibold"
                  : "border-transparent text-[#606266] hover:text-[#409eff]"
              }`}
              onClick={() => setTab("attributions")}
            >
              归因关系 ({attributions.data?.length ?? 0})
            </button>
          </div>
        </div>

        {/* Action & Filter Toolbar */}
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-2">
            <div className="relative">
              <Input
                value={keyword}
                onChange={(e) => setKeyword(e.target.value)}
                placeholder="搜索账号、计划或申请单号"
                className="h-8 w-60 text-xs border-[#dcdfe6] pr-8"
                aria-label="搜索关键字"
              />
              <Search
                size={13}
                className="absolute right-2.5 top-1/2 -translate-y-1/2 text-[#909399] pointer-events-none"
              />
            </div>
            {keyword && (
              <Button
                variant="outline"
                size="sm"
                className="h-8 text-xs border-[#dcdfe6]"
                onClick={() => setKeyword("")}
              >
                清空
              </Button>
            )}
          </div>

          <Button
            variant="outline"
            size="sm"
            className="h-8 text-xs border-[#dcdfe6] text-[#606266]"
            onClick={refreshAll}
          >
            <RefreshCw size={13} className="mr-1" />
            刷新
          </Button>
        </div>

        {/* Global alerts */}
        {message && (
          <div className="p-2.5 bg-[#f0f9eb] border border-[#e1f3d8] text-xs text-[#67c23a] rounded">
            {message}
          </div>
        )}
        {error && (
          <div className="p-2.5 bg-[#fef0f0] border border-[#fde2e2] text-xs text-[#f56c6c] rounded">
            {error}
          </div>
        )}

        {/* Table Content */}
        {tab === "accounts" && (
          <AccountsTable
            data={filteredAccounts}
            error={accounts.error}
            loading={!accounts.data}
            busy={busy}
            onRefresh={accounts.refresh}
            onChangeStatus={changeAccountStatus}
          />
        )}

        {tab === "withdrawals" && (
          <WithdrawalsTable
            data={filteredWithdrawals}
            error={withdrawals.error}
            loading={!withdrawals.data}
            onReview={(item) => setSelectedWithdrawal(item)}
            onPayout={(item) => setPayoutDialogWithdrawal(item)}
          />
        )}

        {tab === "attributions" && (
          <AttributionsTable
            data={filteredAttributions}
            error={attributions.error}
            loading={!attributions.data}
          />
        )}

        {/* Review Dialog */}
        {selectedWithdrawal && (
          <WithdrawalReviewDialog
            item={selectedWithdrawal}
            busy={busy}
            onClose={() => setSelectedWithdrawal(null)}
            onReviewed={() => {
              setSelectedWithdrawal(null);
              refreshAll();
            }}
          />
        )}

        {/* Offline Payout Dialog */}
        {payoutDialogWithdrawal && (
          <OfflinePayoutDialog
            item={payoutDialogWithdrawal}
            busy={busy}
            onClose={() => setPayoutDialogWithdrawal(null)}
            onPaid={() => {
              setPayoutDialogWithdrawal(null);
              refreshAll();
            }}
          />
        )}
      </div>
    </Workspace>
  );
}

function AccountsTable({
  data,
  error,
  loading,
  busy,
  onRefresh,
  onChangeStatus,
}: {
  data: AdminAccount[];
  error: Error | null;
  loading: boolean;
  busy: boolean;
  onRefresh: () => void;
  onChangeStatus: (item: AdminAccount, status: string) => Promise<void>;
}) {
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState(50);
  if (error) return <Failure error={error} retry={onRefresh} />;
  if (loading) return <Loading />;

  const paged = data.slice(page * pageSize, (page + 1) * pageSize);

  return (
    <>
      <div className="overflow-x-auto">
        <Table>
          <TableHeader className="bg-[#f5f7fa]">
            <TableRow className="border-b border-[#ebeef5] hover:bg-[#f5f7fa]">
              <TableHead className="w-16 text-center text-xs font-semibold text-[#909399] py-3">ID</TableHead>
              <TableHead className="text-xs font-semibold text-[#909399] py-3">推广人</TableHead>
              <TableHead className="text-xs font-semibold text-[#909399] py-3">计划名称</TableHead>
              <TableHead className="text-xs font-semibold text-[#909399] py-3">状态</TableHead>
              <TableHead className="text-xs font-semibold text-[#909399] py-3">版本</TableHead>
              <TableHead className="text-xs font-semibold text-[#909399] py-3">开通时间</TableHead>
              <TableHead className="text-right text-xs font-semibold text-[#909399] py-3 pr-4">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {paged.length === 0 ? (
              <TableRow>
                <TableCell colSpan={7} className="h-32 text-center text-xs text-[#909399]">
                  暂无推广账户
                </TableCell>
              </TableRow>
            ) : (
              paged.map((item) => (
                <TableRow
                  key={item.id}
                  className="border-b border-[#ebeef5] hover:bg-[#f5f7fa] text-xs text-[#606266]"
                >
                  <TableCell className="text-center font-mono text-xs text-[#909399]">
                    {item.id}
                  </TableCell>
                  <TableCell>
                    <Link
                      href={`/admin/customers/${item.user_id}`}
                      className="text-[#409eff] hover:underline font-medium"
                    >
                      {item.username}
                    </Link>
                    <div className="text-[11px] text-[#909399]">{item.email}</div>
                  </TableCell>
                  <TableCell>
                    <span className="font-medium text-[#303133]">{item.name}</span>
                    <span className="text-[11px] text-[#909399] ml-1.5">(等级 {item.level})</span>
                  </TableCell>
                  <TableCell>
                    {item.status === "approved" && (
                      <span className="inline-block bg-[#f0f9eb] text-[#67c23a] border border-[#e1f3d8] rounded px-2 py-0.5 text-xs font-medium">
                        已开通
                      </span>
                    )}
                    {item.status === "suspended" && (
                      <span className="inline-block bg-[#fdf6ec] text-[#e6a23c] border border-[#faecd8] rounded px-2 py-0.5 text-xs font-medium">
                        已暂停
                      </span>
                    )}
                    {item.status === "closed" && (
                      <span className="inline-block bg-[#fef0f0] text-[#f56c6c] border border-[#fde2e2] rounded px-2 py-0.5 text-xs font-medium">
                        已关闭
                      </span>
                    )}
                  </TableCell>
                  <TableCell className="font-mono text-xs text-[#909399]">
                    v{item.version}
                  </TableCell>
                  <TableCell className="text-[#909399]">
                    {date(item.created_at)}
                  </TableCell>
                  <TableCell className="text-right pr-4">
                    <PermissionGate permission="affiliates.manage">
                      <div className="flex items-center justify-end gap-1.5">
                        {item.status !== "approved" && (
                          <Button
                            size="sm"
                            variant="outline"
                            className="h-7 text-xs border-[#dcdfe6] text-[#67c23a] hover:border-[#67c23a] hover:bg-[#f0f9eb]"
                            disabled={busy}
                            onClick={() => void onChangeStatus(item, "approved")}
                          >
                            <Check size={12} className="mr-1" />
                            开通
                          </Button>
                        )}
                        {item.status === "approved" && (
                          <Button
                            size="sm"
                            variant="outline"
                            className="h-7 text-xs border-[#dcdfe6] text-[#e6a23c] hover:border-[#e6a23c] hover:bg-[#fdf6ec]"
                            disabled={busy}
                            onClick={() => void onChangeStatus(item, "suspended")}
                          >
                            <Pause size={12} className="mr-1" />
                            暂停
                          </Button>
                        )}
                        {item.status !== "closed" && (
                          <Button
                            size="sm"
                            variant="outline"
                            className="h-7 text-xs border-[#dcdfe6] text-[#f56c6c] hover:border-[#f56c6c] hover:bg-[#fef0f0]"
                            disabled={busy}
                            onClick={() => void onChangeStatus(item, "closed")}
                          >
                            关闭
                          </Button>
                        )}
                      </div>
                    </PermissionGate>
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </div>
      <AdminPagination
        total={data.length}
        page={page}
        pageSize={pageSize}
        onPageChange={setPage}
        onPageSizeChange={setPageSize}
      />
    </>
  );
}

function WithdrawalsTable({
  data,
  error,
  loading,
  onReview,
  onPayout,
}: {
  data: Withdrawal[];
  error: Error | null;
  loading: boolean;
  onReview: (item: Withdrawal) => void;
  onPayout: (item: Withdrawal) => void;
}) {
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState(50);
  if (error) return <Failure error={error} retry={() => {}} />;
  if (loading) return <Loading />;

  const paged = data.slice(page * pageSize, (page + 1) * pageSize);

  return (
    <>
      <div className="overflow-x-auto">
        <Table>
          <TableHeader className="bg-[#f5f7fa]">
            <TableRow className="border-b border-[#ebeef5] hover:bg-[#f5f7fa]">
              <TableHead className="text-xs font-semibold text-[#909399] py-3">申请编号</TableHead>
              <TableHead className="text-xs font-semibold text-[#909399] py-3">申请金额</TableHead>
              <TableHead className="text-xs font-semibold text-[#909399] py-3">手续费</TableHead>
              <TableHead className="text-xs font-semibold text-[#909399] py-3">实付金额</TableHead>
              <TableHead className="text-xs font-semibold text-[#909399] py-3">收款方式</TableHead>
              <TableHead className="text-xs font-semibold text-[#909399] py-3">收款账号</TableHead>
              <TableHead className="text-xs font-semibold text-[#909399] py-3">状态</TableHead>
              <TableHead className="text-xs font-semibold text-[#909399] py-3">提交时间</TableHead>
              <TableHead className="text-right text-xs font-semibold text-[#909399] py-3 pr-4">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {paged.length === 0 ? (
              <TableRow>
                <TableCell colSpan={9} className="h-32 text-center text-xs text-[#909399]">
                  暂无提现申请
                </TableCell>
              </TableRow>
            ) : (
              paged.map((item) => {
                const statusMeta = statusLabels[item.status] ?? {
                  label: item.status,
                  cls: "bg-[#f4f4f5] text-[#909399] border-[#e9e9eb]",
                };
                return (
                  <TableRow
                    key={item.id}
                    className="border-b border-[#ebeef5] hover:bg-[#f5f7fa] text-xs text-[#606266]"
                  >
                    <TableCell className="font-mono text-xs text-[#303133]">
                      {item.withdrawal_no}
                    </TableCell>
                    <TableCell className="font-medium text-[#303133]">
                      {money(item.amount_cents)}
                    </TableCell>
                    <TableCell className="text-[#909399]">
                      {money(item.fee_cents)}
                    </TableCell>
                    <TableCell className="font-semibold text-[#67c23a]">
                      {money(item.payout_cents)}
                    </TableCell>
                    <TableCell>
                      {methodLabels[item.payout_method] ?? item.payout_method}
                    </TableCell>
                    <TableCell className="font-mono text-xs text-[#606266]">
                      {item.payout_target_masked}
                    </TableCell>
                    <TableCell>
                      <span
                        className={`inline-block border rounded px-2 py-0.5 text-xs font-medium ${statusMeta.cls}`}
                      >
                        {statusMeta.label}
                      </span>
                    </TableCell>
                    <TableCell className="text-[#909399]">
                      {date(item.submitted_at)}
                    </TableCell>
                    <TableCell className="text-right pr-4">
                      <div className="flex items-center justify-end gap-1.5">
                        <Button
                          size="sm"
                          variant="outline"
                          className="h-7 text-xs border-[#dcdfe6] text-[#409eff] hover:border-[#409eff] hover:bg-[#ecf5ff]"
                          onClick={() => onReview(item)}
                        >
                          <Eye size={12} className="mr-1" />
                          {item.status === "submitted" || item.status === "reviewing"
                            ? "审核"
                            : "详情"}
                        </Button>
                        {item.status === "approved" && (
                          <PermissionGate permission="affiliates.payout">
                            <Button
                              size="sm"
                              className="h-7 text-xs bg-[#1cb24d] hover:bg-[#169941] text-white px-2"
                              onClick={() => onPayout(item)}
                            >
                              <CreditCard size={12} className="mr-1" />
                              打款
                            </Button>
                          </PermissionGate>
                        )}
                      </div>
                    </TableCell>
                  </TableRow>
                );
              })
            )}
          </TableBody>
        </Table>
      </div>
      <AdminPagination
        total={data.length}
        page={page}
        pageSize={pageSize}
        onPageChange={setPage}
        onPageSizeChange={setPageSize}
      />
    </>
  );
}

function AttributionsTable({
  data,
  error,
  loading,
}: {
  data: Attribution[];
  error: Error | null;
  loading: boolean;
}) {
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState(50);
  if (error) return <Failure error={error} retry={() => {}} />;
  if (loading) return <Loading />;

  const paged = data.slice(page * pageSize, (page + 1) * pageSize);

  return (
    <>
      <div className="overflow-x-auto">
        <Table>
          <TableHeader className="bg-[#f5f7fa]">
            <TableRow className="border-b border-[#ebeef5] hover:bg-[#f5f7fa]">
              <TableHead className="w-16 text-center text-xs font-semibold text-[#909399] py-3">ID</TableHead>
              <TableHead className="text-xs font-semibold text-[#909399] py-3">被归因客户</TableHead>
              <TableHead className="text-xs font-semibold text-[#909399] py-3">推广人</TableHead>
              <TableHead className="text-xs font-semibold text-[#909399] py-3">短码 ID</TableHead>
              <TableHead className="text-xs font-semibold text-[#909399] py-3">首次点击时间</TableHead>
              <TableHead className="text-xs font-semibold text-[#909399] py-3">注册时间</TableHead>
              <TableHead className="text-right text-xs font-semibold text-[#909399] py-3 pr-4">状态</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {paged.length === 0 ? (
              <TableRow>
                <TableCell colSpan={7} className="h-32 text-center text-xs text-[#909399]">
                  暂无归因记录
                </TableCell>
              </TableRow>
            ) : (
              paged.map((item) => (
                <TableRow
                  key={item.id}
                  className="border-b border-[#ebeef5] hover:bg-[#f5f7fa] text-xs text-[#606266]"
                >
                  <TableCell className="text-center font-mono text-xs text-[#909399]">
                    {item.id}
                  </TableCell>
                  <TableCell>
                    <Link
                      href={`/admin/customers/${item.user_id}`}
                      className="text-[#409eff] hover:underline font-medium"
                    >
                      {item.username}
                    </Link>
                    <span className="text-[11px] text-[#909399] ml-1">#{item.user_id}</span>
                  </TableCell>
                  <TableCell>
                    <Link
                      href={`/admin/customers/${item.affiliate_user_id}`}
                      className="text-[#409eff] hover:underline font-medium"
                    >
                      {item.affiliate_username}
                    </Link>
                    <span className="text-[11px] text-[#909399] ml-1">#{item.affiliate_user_id}</span>
                  </TableCell>
                  <TableCell className="font-mono text-xs text-[#909399]">
                    #{item.link_id}
                  </TableCell>
                  <TableCell className="text-[#909399]">
                    {date(item.first_clicked_at)}
                  </TableCell>
                  <TableCell className="text-[#909399]">
                    {date(item.registered_at)}
                  </TableCell>
                  <TableCell className="text-right pr-4">
                    <span className="inline-block bg-[#f0f9eb] text-[#67c23a] border border-[#e1f3d8] rounded px-2 py-0.5 text-xs font-medium">
                      {item.status === "active" ? "有效" : item.status}
                    </span>
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </div>
      <AdminPagination
        total={data.length}
        page={page}
        pageSize={pageSize}
        onPageChange={setPage}
        onPageSizeChange={setPageSize}
      />
    </>
  );
}

function WithdrawalReviewDialog({
  item,
  busy: parentBusy,
  onClose,
  onReviewed,
}: {
  item: Withdrawal;
  busy: boolean;
  onClose: () => void;
  onReviewed: () => void;
}) {
  const detail = useResource<WithdrawalDetail>(`/admin/affiliate/withdrawals/${item.id}`, false);
  const [decision, setDecision] = useState("approved");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function handleReview() {
    if (!detail.data || busy || parentBusy) return;
    setBusy(true);
    setError("");
    try {
      await api(`/admin/affiliate/withdrawals/${item.id}/review`, {
        method: "POST",
        headers: { "Idempotency-Key": `affiliate-review-${item.id}-${Date.now()}` },
        body: JSON.stringify({
          decision,
          note,
          expected_version: detail.data.version,
        }),
      });
      onReviewed();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const isPending = item.status === "submitted" || item.status === "reviewing";

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="sm:max-w-2xl max-h-[85vh] overflow-y-auto bg-white border border-[#ebeef5] rounded shadow-lg p-0 overflow-hidden">
        <DialogHeader className="px-5 py-4 border-b border-[#ebeef5] bg-[#fafafa]">
          <DialogTitle className="text-sm font-semibold text-[#303133]">
            提现申请详情 ({item.withdrawal_no})
          </DialogTitle>
          <DialogDescription className="text-xs text-[#909399]">
            查看申请金额、手续费、冻结佣金明细和审核时间线。
          </DialogDescription>
        </DialogHeader>

        <div className="p-5 space-y-4 text-xs">
          {error && (
            <div className="p-2.5 bg-[#fef0f0] border border-[#fde2e2] text-[#f56c6c] rounded">
              {error}
            </div>
          )}

          {detail.error ? (
            <Failure error={detail.error} retry={detail.refresh} />
          ) : !detail.data ? (
            <Loading />
          ) : (
            <>
              {/* Financial summary card */}
              <div className="grid grid-cols-3 gap-3 p-3 bg-[#fafafa] rounded border border-[#ebeef5]">
                <div>
                  <span className="text-[#909399] block mb-0.5">申请提现</span>
                  <span className="font-semibold text-sm text-[#303133]">
                    {money(detail.data.amount_cents)}
                  </span>
                </div>
                <div>
                  <span className="text-[#909399] block mb-0.5">平台手续费</span>
                  <span className="font-semibold text-sm text-[#909399]">
                    {money(detail.data.fee_cents)}
                  </span>
                </div>
                <div>
                  <span className="text-[#909399] block mb-0.5">预计实付金额</span>
                  <span className="font-semibold text-sm text-[#67c23a]">
                    {money(detail.data.payout_cents)}
                  </span>
                </div>
              </div>

              {/* Payout target card */}
              <div className="p-3 bg-[#fdf6ec] border border-[#faecd8] rounded text-[#303133] space-y-1">
                <span className="font-semibold block text-[#e6a23c]">收款信息</span>
                <div>
                  <span className="text-[#909399]">收款方式：</span>
                  {methodLabels[detail.data.payout_method] ?? detail.data.payout_method}
                </div>
                <div>
                  <span className="text-[#909399]">收款账号：</span>
                  <span className="font-mono">{detail.data.payout_target_masked}</span>
                </div>
              </div>

              {/* Allocations breakdown */}
              <div className="space-y-2">
                <span className="font-semibold text-[#303133] block">关联佣金来源</span>
                <div className="border border-[#ebeef5] rounded divide-y divide-[#ebeef5] max-h-40 overflow-y-auto">
                  {(detail.data.allocations ?? []).length === 0 ? (
                    <div className="p-3 text-center text-[#909399]">无明确拆分条目</div>
                  ) : (
                    detail.data.allocations?.map((alloc) => (
                      <div key={alloc.entry_id} className="p-2.5 flex justify-between items-center">
                        <div>
                          <div className="font-medium text-[#303133]">
                            {alloc.invoice_no} · {alloc.coalesce || "商品服务"}
                          </div>
                          <div className="text-[11px] text-[#909399]">
                            {alloc.event_type} · 可用时间: {date(alloc.available_at)}
                          </div>
                        </div>
                        <span className="font-semibold text-[#67c23a]">
                          {money(alloc.amount_cents)}
                        </span>
                      </div>
                    ))
                  )}
                </div>
              </div>

              {/* Status Timeline */}
              <div className="space-y-2 pt-2 border-t border-[#ebeef5]">
                <span className="font-semibold text-[#303133] block">状态时间线</span>
                <ol className="space-y-2.5 border-l border-[#ebeef5] pl-3 ml-1.5">
                  {detail.data.events.map((ev) => (
                    <li key={ev.id} className="relative">
                      <span className="absolute -left-[17px] top-1 w-2 h-2 rounded-full bg-[#409eff]" />
                      <div className="text-xs font-medium text-[#303133]">
                        {ev.after_status}
                      </div>
                      <div className="text-[11px] text-[#909399]">
                        {date(ev.created_at)} {ev.note ? `· ${ev.note}` : ""}
                      </div>
                    </li>
                  ))}
                </ol>
              </div>

              {/* Review inputs if pending */}
              {isPending && (
                <div className="space-y-3 pt-3 border-t border-[#ebeef5]">
                  <div className="flex items-center gap-3">
                    <span className="font-semibold text-[#303133]">审核决定：</span>
                    <Select value={decision} onValueChange={setDecision}>
                      <SelectTrigger className="h-8 w-36 text-xs border-[#dcdfe6]" aria-label="审核决定">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="approved">通过审核</SelectItem>
                        <SelectItem value="rejected">驳回申请</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>

                  <div className="space-y-1">
                    <label className="font-medium text-[#303133] block">
                      审核备注（驳回时必填）
                    </label>
                    <Textarea
                      rows={2}
                      maxLength={1000}
                      value={note}
                      onChange={(e) => setNote(e.target.value)}
                      placeholder="请填写审核理由或补充说明..."
                      className="text-xs border-[#dcdfe6]"
                    />
                  </div>
                </div>
              )}
            </>
          )}
        </div>

        <div className="px-5 py-3 border-t border-[#ebeef5] bg-[#fafafa] flex items-center justify-end gap-2">
          <Button
            variant="outline"
            size="sm"
            className="h-8 text-xs border-[#dcdfe6]"
            onClick={onClose}
            disabled={busy}
          >
            关闭
          </Button>
          {isPending && (
            <Button
              size="sm"
              className={`h-8 text-xs px-4 text-white ${
                decision === "approved"
                  ? "bg-[#008080] hover:bg-[#006666]"
                  : "bg-[#f56c6c] hover:bg-[#e64242]"
              }`}
              disabled={busy || (decision === "rejected" && !note.trim())}
              onClick={handleReview}
            >
              确认{decision === "approved" ? "通过" : "驳回"}
            </Button>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

function OfflinePayoutDialog({
  item,
  busy: parentBusy,
  onClose,
  onPaid,
}: {
  item: Withdrawal;
  busy: boolean;
  onClose: () => void;
  onPaid: () => void;
}) {
  const [payoutRef, setPayoutRef] = useState("");
  const [payoutChannel, setPayoutChannel] = useState("");
  const [proofHash, setProofHash] = useState("");
  const [payoutNote, setPayoutNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function handlePayout(e: React.FormEvent) {
    e.preventDefault();
    if (busy || parentBusy) return;
    setBusy(true);
    setError("");

    try {
      await api(`/admin/affiliate/withdrawals/${item.id}/payout`, {
        method: "POST",
        headers: { "Idempotency-Key": `affiliate-payout-${item.id}-${Date.now()}` },
        body: JSON.stringify({
          external_ref: payoutRef,
          channel: payoutChannel,
          proof_hash: proofHash,
          note: payoutNote,
          expected_version: item.version,
          confirmed: true,
        }),
      });
      onPaid();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="sm:max-w-md bg-white border border-[#ebeef5] rounded shadow-lg p-0 overflow-hidden">
        <DialogHeader className="px-5 py-4 border-b border-[#ebeef5] bg-[#fafafa]">
          <DialogTitle className="text-sm font-semibold text-[#303133]">
            登记线下打款 ({item.withdrawal_no})
          </DialogTitle>
          <DialogDescription className="text-xs text-[#909399]">
            登记实际转账流水与凭证，打款登记后佣金将标记为已结算。
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={handlePayout} className="p-5 space-y-4 text-xs">
          {error && (
            <div className="p-2.5 bg-[#fef0f0] border border-[#fde2e2] text-[#f56c6c] rounded">
              {error}
            </div>
          )}

          <div className="p-3 bg-[#f0f9eb] border border-[#e1f3d8] rounded flex justify-between items-center">
            <span className="text-[#606266]">打款金额：</span>
            <span className="font-semibold text-sm text-[#67c23a]">
              {money(item.payout_cents)}
            </span>
          </div>

          <div className="space-y-1">
            <label className="font-medium text-[#303133] block">
              <span className="text-red-500 mr-1">*</span>打款流水号
            </label>
            <Input
              required
              value={payoutRef}
              onChange={(e) => setPayoutRef(e.target.value)}
              placeholder="例如：银行流水号、支付宝交易号"
              className="h-8 text-xs border-[#dcdfe6]"
            />
          </div>

          <div className="space-y-1">
            <label className="font-medium text-[#303133] block">
              <span className="text-red-500 mr-1">*</span>渠道快照
            </label>
            <Input
              required
              value={payoutChannel}
              onChange={(e) => setPayoutChannel(e.target.value)}
              placeholder="例如：招商银行对公账户 / 财务主管A"
              className="h-8 text-xs border-[#dcdfe6]"
            />
          </div>

          <div className="space-y-1">
            <label className="font-medium text-[#303133] block">
              <span className="text-red-500 mr-1">*</span>凭证摘要哈希 (SHA-256 64位)
            </label>
            <Input
              required
              maxLength={64}
              value={proofHash}
              onChange={(e) => setProofHash(e.target.value.toLowerCase())}
              placeholder="电子回单或打款凭证的 64 位 SHA-256"
              className="h-8 text-xs font-mono border-[#dcdfe6]"
            />
            {proofHash && proofHash.length !== 64 && (
              <span className="text-[11px] text-[#f56c6c] block">
                长度需精确为 64 位十六进制字符（当前 {proofHash.length} 位）
              </span>
            )}
          </div>

          <div className="space-y-1">
            <label className="font-medium text-[#303133] block">打款备注</label>
            <Textarea
              rows={2}
              maxLength={1000}
              value={payoutNote}
              onChange={(e) => setPayoutNote(e.target.value)}
              placeholder="填写财务备注..."
              className="text-xs border-[#dcdfe6]"
            />
          </div>

          <div className="px-5 py-3 -mx-5 -mb-5 mt-5 border-t border-[#ebeef5] bg-[#fafafa] flex items-center justify-end gap-2">
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="h-8 text-xs border-[#dcdfe6]"
              disabled={busy}
              onClick={onClose}
            >
              取消
            </Button>
            <Button
              type="submit"
              size="sm"
              className="h-8 bg-[#008080] hover:bg-[#006666] text-white text-xs px-4"
              disabled={
                busy ||
                !payoutRef.trim() ||
                !payoutChannel.trim() ||
                proofHash.length !== 64
              }
            >
              确认登记打款
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
