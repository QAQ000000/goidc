"use client";

import { useState } from "react";
import Link from "next/link";
import { Search, RefreshCw, X, Check, Eye } from "lucide-react";
import { api, date } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "./ui/dialog";
import { Table, TableHeader, TableHead, TableBody, TableRow, TableCell } from "./ui/table";
import { Failure, Loading } from "./common";
import { Workspace, useAdminPermission } from "./workspace";
import { AdminPagination } from "./admin-pagination";

type Certification = {
  id: number;
  user_id: number;
  username: string;
  display_name: string;
  email: string;
  certification_type: "individual" | "company";
  status: "pending" | "approved" | "rejected" | "unverified";
  provider_id: string;
  rejection_reason?: string;
  version: number;
  updated_at?: string;
  submission_id?: number;
};

type AdminDetail = Certification & {
  submitted_at: string;
  payload: Record<string, string>;
};
type CertificationHistory = { id: number; reviewer_id: number; reviewer_name: string; decision: string; note: string; created_at: string };

export function AdminCertificationsView() {
  const manage = useAdminPermission("certifications.manage");
  const [statusFilter, setStatusFilter] = useState("all");
  const [keyword, setKeyword] = useState("");
  const [activeKeyword, setActiveKeyword] = useState("");
  const [typeFilter, setTypeFilter] = useState("all");
  const [providerFilter, setProviderFilter] = useState("all");
  const [page, setPage] = useState(0);
  const pageSize = 25;

  const endpoint = `/admin/certifications/page?q=${encodeURIComponent(activeKeyword.trim())}&limit=${pageSize}&offset=${page * pageSize}${
    statusFilter !== "all" ? `&status=${statusFilter}` : ""
  }${typeFilter !== "all" ? `&certification_type=${typeFilter}` : ""}${providerFilter !== "all" ? `&provider_id=${encodeURIComponent(providerFilter)}` : ""}`;
  const list = useResource<{ items: Certification[]; total: number }>(endpoint, false);
  const lastPage = Math.max(0, Math.ceil((list.data?.total ?? 0) / pageSize) - 1);
  if (list.data && page > lastPage) setPage(lastPage);

  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [detail, setDetail] = useState<AdminDetail | null>(null);
  const [history, setHistory] = useState<CertificationHistory[]>([]);
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState("");

  async function openDetail(id: number) {
    setSelectedId(id);
    setActionError("");
    setNote("");
    try {
      const [data, entries] = await Promise.all([
        api<AdminDetail>(`/admin/certifications/${id}`),
        api<CertificationHistory[]>(`/admin/certifications/${id}/history`),
      ]);
      setDetail(data);
      setHistory(entries);
      if (data.rejection_reason) {
        setNote(data.rejection_reason);
      }
    } catch (e) {
      setActionError((e as Error).message);
    }
  }

  async function review(decision: "approved" | "rejected") {
    if (!detail || busy) return;
    setBusy(true);
    setActionError("");
    try {
      await api(`/admin/certifications/${detail.id}/review`, {
        method: "POST",
        headers: { "Idempotency-Key": `certification-review-${detail.id}-${detail.version}-${crypto.randomUUID()}` },
        body: JSON.stringify({
          decision,
          note,
          expected_version: detail.version,
        }),
      });
      setSelectedId(null);
      setDetail(null);
      setHistory([]);
      setNote("");
      list.refresh();
    } catch (e) {
      setActionError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const filteredData = list.data?.items ?? [];

  return (
    <Workspace title="实名认证" admin>
      <div className="bg-white rounded border border-[#ebeef5] shadow-sm p-4 sm:p-5 space-y-4 max-w-full overflow-hidden">
        {/* Top guide text matching MoFang #/customer-authentication */}
        <div className="text-xs text-[#606266]">
          客户在进行实名认证时提交的信息记录，可在此下载证件及人工审核。{" "}
          <a
            href="https://bbs.idcsmart.com"
            target="_blank"
            rel="noreferrer"
            className="text-[#409eff] hover:underline ml-1"
          >
            帮助文档
          </a>
        </div>

        {/* Action & Filter Toolbar */}
        <div className="flex flex-wrap items-center justify-between gap-3">
          <form
            className="flex items-center gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              setActiveKeyword(keyword);
              setPage(0);
            }}
          >
            <Input
              value={keyword}
              onChange={(e) => setKeyword(e.target.value)}
              placeholder="输入关键字"
              className="h-8 w-56 text-xs border-[#dcdfe6]"
              aria-label="搜索关键字"
            />
            <Button
              type="submit"
              size="sm"
              className="h-8 bg-[#409eff] text-white hover:bg-[#337ecc] text-xs px-3 shadow-none font-normal"
            >
              <Search size={13} className="mr-1" />
              搜索
            </Button>
            {activeKeyword && (
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="h-8 text-xs border-[#dcdfe6]"
                onClick={() => {
                  setKeyword("");
                  setActiveKeyword("");
                  setPage(0);
                }}
              >
                重置
              </Button>
            )}
          </form>

          <div className="flex items-center gap-2">
            <span className="text-xs text-[#606266] whitespace-nowrap">状态筛选：</span>
            <Select
              value={statusFilter}
              onValueChange={(val) => {
                setStatusFilter(val);
                setPage(0);
              }}
            >
              <SelectTrigger className="h-8 w-28 text-xs border-[#dcdfe6]" aria-label="状态筛选">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">全部</SelectItem>
                <SelectItem value="pending">待审核</SelectItem>
                <SelectItem value="approved">已通过</SelectItem>
                <SelectItem value="rejected">已驳回</SelectItem>
              </SelectContent>
            </Select>
            <Select value={typeFilter} onValueChange={(value) => { setTypeFilter(value); setPage(0); }}>
              <SelectTrigger className="h-8 w-28 text-xs border-[#dcdfe6]" aria-label="认证类型筛选"><SelectValue placeholder="认证类型" /></SelectTrigger>
              <SelectContent><SelectItem value="all">全部类型</SelectItem><SelectItem value="individual">个人认证</SelectItem><SelectItem value="company">企业认证</SelectItem></SelectContent>
            </Select>
            <Input value={providerFilter === "all" ? "" : providerFilter} onChange={(event) => { setProviderFilter(event.target.value.trim() || "all"); setPage(0); }} placeholder="认证接口" aria-label="认证接口筛选" className="h-8 w-28 text-xs border-[#dcdfe6]" />
            <Button
              variant="outline"
              size="icon"
              className="h-8 w-8 text-[#606266] border-[#dcdfe6]"
              title="刷新列表"
              aria-label="刷新列表"
              onClick={list.refresh}
            >
              <RefreshCw size={13} />
            </Button>
          </div>
        </div>

        {/* Data Table */}
        {list.error ? (
          <Failure error={list.error} retry={list.refresh} />
        ) : !list.data ? (
          <Loading />
        ) : (
          <>
            <div className="overflow-x-auto">
              <Table>
                <TableHeader className="bg-[#f5f7fa]">
                  <TableRow className="border-b border-[#ebeef5] hover:bg-[#f5f7fa]">
                    <TableHead className="w-16 text-center text-xs font-semibold text-[#909399] py-3">ID</TableHead>
                    <TableHead className="text-xs font-semibold text-[#909399] py-3">姓名</TableHead>
                    <TableHead className="text-xs font-semibold text-[#909399] py-3">实名认证名称</TableHead>
                    <TableHead className="text-xs font-semibold text-[#909399] py-3">证件号码</TableHead>
                    <TableHead className="text-xs font-semibold text-[#909399] py-3">认证方式</TableHead>
                    <TableHead className="text-xs font-semibold text-[#909399] py-3">认证类型</TableHead>
                    <TableHead className="text-xs font-semibold text-[#909399] py-3">状态/原因</TableHead>
                    <TableHead className="text-xs font-semibold text-[#909399] py-3">提交时间</TableHead>
                    <TableHead className="text-right text-xs font-semibold text-[#909399] py-3 pr-4">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {filteredData.length === 0 ? (
                    <TableRow>
                      <TableCell colSpan={9} className="h-32 text-center text-xs text-[#909399]">
                        暂无数据
                      </TableCell>
                    </TableRow>
                  ) : (
                    filteredData.map((row) => (
                      <TableRow
                        key={row.id}
                        className="border-b border-[#ebeef5] hover:bg-[#f5f7fa] text-xs text-[#606266]"
                      >
                        <TableCell className="text-center font-mono text-xs text-[#909399]">
                          {row.id}
                        </TableCell>
                        <TableCell>
                          <Link
                            href={`/admin/customers/${row.user_id}`}
                            className="text-[#409eff] hover:underline font-medium"
                          >
                            {row.display_name || row.username}
                          </Link>
                        </TableCell>
                        <TableCell>
                          {row.display_name || row.username}
                        </TableCell>
                        <TableCell className="font-mono text-xs text-[#606266]">
                          {row.certification_type === "company" ? "企业信用码" : "居民身份证"}
                        </TableCell>
                        <TableCell>
                          <span className="text-[#909399]">人工审核</span>
                        </TableCell>
                        <TableCell>
                          <span className="inline-block bg-[#f4f4f5] text-[#909399] border border-[#e9e9eb] rounded px-1.5 py-0.5 text-[11px]">
                            {row.certification_type === "company" ? "企业认证" : "个人认证"}
                          </span>
                        </TableCell>
                        <TableCell>
                          {row.status === "approved" && (
                            <span className="inline-block bg-[#f0f9eb] text-[#67c23a] border border-[#e1f3d8] rounded px-2 py-0.5 text-xs font-medium">
                              已通过
                            </span>
                          )}
                          {row.status === "pending" && (
                            <span className="inline-block bg-[#fdf6ec] text-[#e6a23c] border border-[#faecd8] rounded px-2 py-0.5 text-xs font-medium">
                              待审核
                            </span>
                          )}
                          {row.status === "rejected" && (
                            <span
                              className="inline-block bg-[#fef0f0] text-[#f56c6c] border border-[#fde2e2] rounded px-2 py-0.5 text-xs font-medium cursor-help"
                              title={row.rejection_reason || "已驳回"}
                            >
                              已驳回
                            </span>
                          )}
                          {row.status === "unverified" && (
                            <span className="inline-block bg-[#f4f4f5] text-[#909399] border border-[#e9e9eb] rounded px-2 py-0.5 text-xs">
                              未认证
                            </span>
                          )}
                        </TableCell>
                        <TableCell className="text-[#909399]">
                          {row.updated_at ? date(row.updated_at) : "-"}
                        </TableCell>
                        <TableCell className="text-right pr-4">
                          <Button
                            size="sm"
                            variant="outline"
                            className="h-7 text-xs border-[#dcdfe6] text-[#409eff] hover:border-[#409eff] hover:bg-[#ecf5ff]"
                            onClick={() => void openDetail(row.id)}
                          >
                            <Eye size={12} className="mr-1" />
                            {row.status === "pending" && manage ? "审核" : "查看资料"}
                          </Button>
                        </TableCell>
                      </TableRow>
                    ))
                  )}
                </TableBody>
              </Table>
            </div>

            <AdminPagination
              total={list.data?.total ?? 0}
              page={page}
              pageSize={pageSize}
              onPageChange={setPage}
            />
          </>
        )}
      </div>

        {/* MoFang Style Review / Detail Dialog */}
        <Dialog
          open={selectedId !== null}
          onOpenChange={(open) => {
            if (!open) {
              setSelectedId(null);
              setDetail(null);
            }
          }}
        >
          <DialogContent className="sm:max-w-xl bg-white border border-[#ebeef5] rounded shadow-lg p-0 overflow-hidden">
            <DialogHeader className="px-5 py-4 border-b border-[#ebeef5] bg-[#fafafa]">
              <DialogTitle className="text-sm font-semibold text-[#303133]">
                {detail?.status === "pending" && manage ? "审核实名认证" : "实名认证资料"}
              </DialogTitle>
              <DialogDescription className="text-xs text-[#909399]">
                客户提交的身份及企业认证资料，敏感信息已加密安全存储。
              </DialogDescription>
            </DialogHeader>

            <div className="p-5 space-y-4 max-h-[70vh] overflow-y-auto text-xs">
              {actionError && (
                <div className="p-2.5 bg-[#fef0f0] border border-[#fde2e2] text-[#f56c6c] rounded">
                  {actionError}
                </div>
              )}

              {!detail ? (
                <Loading />
              ) : (
                <>
                  <div className="grid grid-cols-2 gap-3 p-3 bg-[#fafafa] rounded border border-[#ebeef5]">
                    <div>
                      <span className="text-[#909399] block mb-0.5">认证客户</span>
                      <span className="font-medium text-[#303133]">
                        {detail.display_name || detail.username} (#{detail.user_id})
                      </span>
                    </div>
                    <div>
                      <span className="text-[#909399] block mb-0.5">认证类型</span>
                      <span className="font-medium text-[#303133]">
                        {detail.certification_type === "company" ? "企业认证" : "个人认证"}
                      </span>
                    </div>
                    <div>
                      <span className="text-[#909399] block mb-0.5">当前状态</span>
                      <span>
                        {detail.status === "approved" && <span className="text-[#67c23a] font-medium">已通过</span>}
                        {detail.status === "pending" && <span className="text-[#e6a23c] font-medium">待审核</span>}
                        {detail.status === "rejected" && <span className="text-[#f56c6c] font-medium">已驳回</span>}
                        {detail.status === "unverified" && <span className="text-[#909399]">未认证</span>}
                      </span>
                    </div>
                    <div>
                      <span className="text-[#909399] block mb-0.5">提交时间</span>
                      <span className="text-[#606266]">{date(detail.submitted_at)}</span>
                    </div>
                  </div>

                  {/* Certified payload fields */}
                  <div className="space-y-2">
                    <span className="text-xs font-semibold text-[#303133] block">提交的证件资料</span>
                    <div className="border border-[#ebeef5] rounded divide-y divide-[#ebeef5]">
                      {Object.entries(detail.payload ?? {}).map(([k, v]) => (
                        <div key={k} className="p-2.5 flex justify-between items-center">
                          <span className="text-[#909399] font-medium">
                            {k === "name"
                              ? "真实姓名"
                              : k === "id_number"
                              ? "身份证号码"
                              : k === "company_name"
                              ? "企业名称"
                              : k === "company_registration_number"
                              ? "统一社会信用代码"
                              : k}
                          </span>
                          <span className="font-mono text-[#303133] break-all">{v}</span>
                        </div>
                      ))}
                    </div>
                  </div>

                  {/* Review controls for pending certifications */}
                  {manage && detail.status === "pending" && (
                    <div className="space-y-2 pt-2 border-t border-[#ebeef5]">
                      <label className="text-xs font-semibold text-[#303133] block">
                        审核备注（驳回时必填理由）
                      </label>
                      <Textarea
                        rows={3}
                        maxLength={2000}
                        value={note}
                        onChange={(e) => setNote(e.target.value)}
                        placeholder="请输入审核备注或驳回原因..."
                        className="text-xs border-[#dcdfe6]"
                      />
                    </div>
                  )}

                  {detail.status === "rejected" && detail.rejection_reason && (
                    <div className="p-3 bg-[#fef0f0] border border-[#fde2e2] text-[#f56c6c] rounded">
                      <span className="font-semibold block mb-1">驳回原因：</span>
                      <span>{detail.rejection_reason}</span>
                    </div>
                  )}
                  <div className="space-y-2 pt-2 border-t border-[#ebeef5]">
                    <span className="text-xs font-semibold text-[#303133] block">历史记录</span>
                    {history.length === 0 ? <p className="text-[#909399]">暂无审核记录</p> : <div className="divide-y border border-[#ebeef5] rounded">{history.map(item => <div key={item.id} className="p-2.5 flex flex-wrap items-start justify-between gap-2"><div><span className={item.decision === "approved" ? "text-[#67c23a]" : "text-[#f56c6c]"}>{item.decision === "approved" ? "通过" : "驳回"}</span><span className="ml-2 text-[#606266]">{item.note || "无备注"}</span></div><span className="text-[#909399]">{item.reviewer_name} · {date(item.created_at)}</span></div>)}</div>}
                  </div>
                </>
              )}
            </div>

            <div className="px-5 py-3 border-t border-[#ebeef5] bg-[#fafafa] flex items-center justify-end gap-2">
              <Button
                variant="outline"
                size="sm"
                className="h-8 text-xs border-[#dcdfe6]"
                disabled={busy}
                onClick={() => {
                  setSelectedId(null);
                  setDetail(null);
                  setHistory([]);
                }}
              >
                关闭
              </Button>
              {manage && detail?.status === "pending" && (
                <>
                  <Button
                    size="sm"
                    variant="destructive"
                    className="h-8 text-xs"
                    disabled={busy || !note.trim()}
                    onClick={() => void review("rejected")}
                  >
                    <X size={13} className="mr-1" />
                    驳回
                  </Button>
                  <Button
                    size="sm"
                    className="h-8 bg-[#008080] hover:bg-[#006666] text-white text-xs px-4"
                    disabled={busy}
                    onClick={() => void review("approved")}
                  >
                    <Check size={13} className="mr-1" />
                    通过认证
                  </Button>
                </>
              )}
            </div>
          </DialogContent>
        </Dialog>
    </Workspace>
  );
}
