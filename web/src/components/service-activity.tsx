"use client";

import { ChevronLeft, ChevronRight, RefreshCw } from "lucide-react";
import { useState } from "react";
import { date } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Failure, Loading, Status } from "./common";
import { serviceActionLabels } from "./nokvm-delivery";

type ActivityItem = {
  id: string;
  source: "audit" | "job";
  audit_id: number | null;
  job_id: number | null;
  action: string;
  category: string;
  status: string;
  actor_kind: "you" | "admin" | "system";
  actor_display_name: string | null;
  occurred_at: string;
  attempts: number | null;
  metadata: { job_id?: number; audit_id?: number; action?: string };
};

type ActivityResponse = {
  service_id: number;
  total: number;
  limit: number;
  offset: number;
  items: ActivityItem[];
};

const activityActions = [
  ["create", "开通资源"], ["suspend", "暂停服务"], ["resume", "恢复服务"], ["terminate", "终止资源"], ["sync", "刷新资源"],
  ["upgrade", "变更配置"], ["reset_password", "重置系统密码"], ["reinstall", "重装系统"], ["power_on", "开机"], ["power_off", "关机"],
  ["reboot", "重启"], ["force_off", "强制关机"], ["force_reboot", "强制重启"], ["rescue_system", "切换救援系统"], ["exit_rescue_system", "退出救援系统"], ["snapshot_create", "创建快照"], ["snapshot_delete", "删除快照"],
  ["snapshot_restore", "恢复快照"], ["backup_create", "创建备份"], ["backup_delete", "删除备份"], ["backup_restore", "恢复备份"], ["nat_acl_create", "创建端口转发"],
  ["nat_acl_delete", "删除端口转发"], ["nat_web_create", "创建 NAT 建站"], ["nat_web_delete", "删除 NAT 建站"], ["cdrom_mount", "挂载光驱"], ["cdrom_unmount", "卸载光驱"],
  ["service.manual.completed", "人工交付完成"], ["service.nokvm.verified", "资源核验结果"], ["service.nokvm.completed", "资源操作完成"],
  ["supplier.lifecycle.manual_requested", "上游人工操作已登记"], ["supplier.lifecycle.manual_verified", "上游人工操作已核验"], ["supplier.lifecycle.manual_cancelled", "上游人工操作已取消"],
  ["service.nokvm.pending", "资源任务待处理"], ["service.nokvm.running", "资源任务执行中"], ["service.nokvm.failed", "资源任务失败"], ["service.nokvm.waiting_verification", "资源任务待核验"],
  ["service.renewal.created", "创建续费账单"], ["service.auto_renew.updated", "更新自动续费"], ["service.configuration.submitted", "提交配置变更"],
  ["service.configuration.cancelled", "取消配置变更"], ["service.configuration.retried", "重试配置变更"], ["service.billing_cycle.completed", "账期变更完成"],
  ["service.simulation.completed", "模拟交付完成"], ["service.cancellation.requested", "提交取消申请"], ["service.cancellation.withdrawn", "撤回取消申请"],
  ["service.cancellation.approved", "批准取消申请"], ["service.cancellation.rejected", "驳回取消申请"],
] as const;

const categories = [["lifecycle", "生命周期"], ["operation", "资源操作"], ["configuration", "配置变更"], ["billing", "账务与续费"], ["cancellation", "取消申请"]] as const;
const statuses = [["queued", "排队中"], ["pending", "待处理"], ["running", "执行中"], ["waiting_manual", "待人工确认"], ["waiting_verification", "待核验"], ["succeeded", "已完成"], ["completed", "已完成"], ["verified", "已核验"], ["failed", "失败"], ["cancelled", "已取消"], ["recorded", "已记录"], ["requested", "已申请"], ["withdrawn", "已撤回"], ["approved", "已通过"], ["rejected", "已驳回"]] as const;

const actorLabels: Record<ActivityItem["actor_kind"], string> = { you: "你", admin: "管理员", system: "系统" };
const categoryLabels = Object.fromEntries(categories) as Record<string, string>;
const actionLabels = Object.fromEntries(activityActions) as Record<string, string>;

export function ServiceActivityPanel({ serviceID, admin = false }: { serviceID: number; admin?: boolean }) {
  const [action, setAction] = useState("");
  const [category, setCategory] = useState("");
  const [status, setStatus] = useState("");
  const [offset, setOffset] = useState(0);
  const query = new URLSearchParams({ limit: "10", offset: String(offset) });
  if (action) query.set("action", action);
  if (category) query.set("category", category);
  if (status) query.set("status", status);
  const { data, error, refresh } = useResource<ActivityResponse>(`${admin ? "/admin" : ""}/services/${serviceID}/activity?${query.toString()}`, false);
  const total = data?.total ?? 0;
  const hasPrevious = offset > 0;
  const hasNext = !!data && offset + data.items.length < total;
  function changeFilter(setter: (value: string) => void, value: string) {
    setter(value === "all" ? "" : value);
    setOffset(0);
  }
  return (
    <section className="data-surface mt-5 overflow-hidden">
      <div className="surface-head flex-wrap gap-4">
        <div>
          <h2 className="font-semibold">操作历史</h2>
          <p className="text-xs text-muted-foreground mt-1">查看任务进度、核验结果及续费记录</p>
        </div>
        <Button variant="ghost" size="icon-sm" onClick={refresh} aria-label="刷新操作历史" title="刷新操作历史"><RefreshCw size={16} /></Button>
      </div>
      <div className="grid gap-3 border-y bg-muted/20 p-4 sm:grid-cols-3">
        <Select value={action || "all"} onValueChange={(value) => changeFilter(setAction, value)}><SelectTrigger aria-label="按动作筛选" className="w-full"><SelectValue placeholder="全部动作" /></SelectTrigger><SelectContent><SelectItem value="all">全部动作</SelectItem>{activityActions.map(([value, label]) => <SelectItem key={value} value={value}>{label}</SelectItem>)}</SelectContent></Select>
        <Select value={category || "all"} onValueChange={(value) => changeFilter(setCategory, value)}><SelectTrigger aria-label="按分类筛选" className="w-full"><SelectValue placeholder="全部分类" /></SelectTrigger><SelectContent><SelectItem value="all">全部分类</SelectItem>{categories.map(([value, label]) => <SelectItem key={value} value={value}>{label}</SelectItem>)}</SelectContent></Select>
        <Select value={status || "all"} onValueChange={(value) => changeFilter(setStatus, value)}><SelectTrigger aria-label="按状态筛选" className="w-full"><SelectValue placeholder="全部状态" /></SelectTrigger><SelectContent><SelectItem value="all">全部状态</SelectItem>{statuses.map(([value, label]) => <SelectItem key={value} value={value}>{label}</SelectItem>)}</SelectContent></Select>
      </div>
      {error ? <div className="p-4"><Failure error={error} retry={refresh} /></div> : !data ? <div className="p-4"><Loading /></div> : data.items.length === 0 ? <p className="p-8 text-center text-sm text-muted-foreground">暂无符合条件的操作记录</p> : (
        <div className="divide-y">
          {data.items.map((item) => <article key={item.id} className="grid gap-2 p-4 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-start">
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2"><h3 className="font-medium break-words">{actionLabels[item.action] ?? serviceActionLabels[item.action] ?? item.action}</h3><Status value={item.status} /><span className="text-xs text-muted-foreground">{categoryLabels[item.category] ?? item.category}</span></div>
              <p className="mt-2 text-xs text-muted-foreground">{actorLabels[item.actor_kind]}{item.actor_display_name ? ` · ${item.actor_display_name}` : ""}{item.job_id ? ` · 任务 #${item.job_id}` : ""}{typeof item.attempts === "number" ? ` · 尝试 ${item.attempts} 次` : ""}</p>
            </div>
            <time className="text-xs text-muted-foreground sm:text-right" dateTime={item.occurred_at}>{date(item.occurred_at)}</time>
          </article>)}
        </div>
      )}
      {data && total > 0 && <div className="flex items-center justify-between border-t p-3 text-xs text-muted-foreground"><span>{offset + 1}-{Math.min(offset + data.items.length, total)} / {total}</span><div className="flex gap-2"><Button variant="outline" size="icon-sm" disabled={!hasPrevious} onClick={() => setOffset(Math.max(0, offset - 10))} aria-label="上一页" title="上一页"><ChevronLeft size={15} /></Button><Button variant="outline" size="icon-sm" disabled={!hasNext} onClick={() => setOffset(offset + 10)} aria-label="下一页" title="下一页"><ChevronRight size={15} /></Button></div></div>}
    </section>
  );
}
