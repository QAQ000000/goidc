"use client";
import Link from "next/link";
import { RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { ApiError } from "@/lib/api";
export function Loading() {
  return (
    <div aria-label="加载中" role="status" className="space-y-4 py-5">
      <Skeleton className="h-12 w-full" />
      <Skeleton className="h-40 w-full" />
      <Skeleton className="h-12 w-2/3" />
    </div>
  );
}
export function Failure({ error, retry }: { error: Error; retry: () => void }) {
  return (
    <div role="alert" className="py-12 text-center">
      <p className="mb-4 text-destructive">{error.message}</p>
      {error instanceof ApiError && error.status === 401 ? (
        <Button asChild>
          <Link href="/login">登录账户</Link>
        </Button>
      ) : (
        <Button variant="outline" onClick={retry}>
          <RefreshCw size={16} />
          重新加载
        </Button>
      )}
    </div>
  );
}
const names: Record<string, string> = {
  pending: "待处理",
  available: "可提现",
  reserved: "提现中",
  reversed: "已冲正",
  clawback: "待追缴",
  expired: "本地已过期",
  paid: "已支付",
  unpaid: "待支付",
  active: "已启用",
  cancelled: "已取消",
  refunded: "已退款",
  open: "处理中",
  closed: "已关闭",
  resolved: "已解决",
  disabled: "已停用",
  revoked: "已撤销",
  normal: "普通",
  high: "高",
  low: "低",
  urgent: "紧急",
  pending_payment: "待支付",
  queued: "排队中",
  provisioning: "开通中",
  waiting_manual: "待人工确认",
  waiting_verification: "待核验",
  completed: "已完成",
  suspended: "已暂停",
  terminated: "已终止",
  failed: "失败",
  running: "执行中",
  succeeded: "已完成",
  command_accepted: "已受理",
  credentials_confirmed: "已确认",
  approved: "已通过",
  submitted: "已提交",
  reviewing: "审核中",
  paying: "打款中",
  declined: "已驳回",
  rejected: "已驳回",
  withdrawn: "已撤回",
  recorded: "已记录",
  verified: "已核验",
  requested: "已申请",
};
export function Status({ value }: { value: string }) {
  return (
    <Badge
      variant="outline"
      className={
        value === "paid" || value === "active" || value === "resolved" || value === "available"
          ? "text-emerald-700 bg-emerald-50 border-emerald-200"
          : value === "pending" || value === "unpaid" || value === "urgent" || value === "queued" || value === "provisioning" || value === "reserved" || value === "clawback"
            ? "text-amber-700 bg-amber-50 border-amber-200"
            : value === "failed"
              ? "text-destructive bg-red-50 border-red-200"
            : "text-zinc-600 bg-zinc-50"
      }
    >
      {names[value] ?? value}
    </Badge>
  );
}
export const cycles: Record<string, string> = {
  monthly: "月",
  quarterly: "季",
  annually: "年",
  once: "次",
};
