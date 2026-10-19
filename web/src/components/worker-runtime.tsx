"use client";

import { useResource } from "@/hooks/use-resource";
import { date } from "@/lib/api";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";

type Lane = { instance_id: string; name: string; state: string; status: string; progress_at: string; completed_passes: number; stall_after_seconds: number };
type Queue = { name: string; runnable_pending: number; future_pending: number; oldest_waiting_seconds: number; running: number; expired_leases: number; recent_failures: number; recent_unknown: number; recent_dead_letters: number };
type Runtime = { status: "ok" | "degraded"; observed_at: string; recent_window_seconds: number; missing_lanes: string[]; lanes: Lane[]; queues: Queue[] };
const names: Record<string, string> = { provision: "服务 / 邮件 / 短信", payment: "支付查单", file_cleanup: "文件回收", notification_schedule: "通知与逾期调度", auto_renew: "自动续费", notification_center: "统一通知", certification: "实名认证", email: "邮件", sms: "短信", payment_checks: "支付查单记录" };
const states: Record<string, string> = { starting: "启动中", idle: "轮询正常", running: "执行中", stale: "轮询中断", stalled: "执行无进展", stopped: "已停止" };

export function WorkerRuntimePanel() {
  const resource = useResource<Runtime>("/admin/runtime", false);
  return <section className="space-y-4 border-t pt-5" aria-label="后台执行状态">
    <div className="flex items-center justify-between gap-3"><h2 className="text-lg font-semibold">后台执行状态</h2><Button variant="outline" onClick={resource.refresh}>刷新执行状态</Button></div>
    <p className="text-sm text-muted-foreground">按执行循环的实际进展判断。此处只读，不触发重试；正常运行不代表每项业务已成功。</p>
    {resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <>
      <p role="status" className={resource.data.status === "ok" ? "text-sm" : "text-sm text-destructive"}>{resource.data.status === "ok" ? "执行循环正常" : "存在需检查的运行状态"} · 采样时间 {date(resource.data.observed_at)}</p>
      {resource.data.missing_lanes.length > 0 && <p className="text-sm text-destructive">缺少有效进展：{resource.data.missing_lanes.map(name => names[name] ?? name).join("、")}</p>}
      <div className="overflow-x-auto"><Table><TableHeader><TableRow>{["队列", "可执行待办", "尚未到期", "最久等待", "运行中", "过期租约", `最近 ${resource.data.recent_window_seconds / 60} 分钟失败 / 未知 / 死信`].map(name => <TableHead key={name}>{name}</TableHead>)}</TableRow></TableHeader><TableBody>{resource.data.queues.map(queue => <TableRow key={queue.name}><TableCell>{queue.name === "provision" ? "服务操作" : names[queue.name] ?? queue.name}</TableCell><TableCell>{queue.runnable_pending}</TableCell><TableCell>{queue.future_pending}</TableCell><TableCell>{queue.oldest_waiting_seconds} 秒</TableCell><TableCell>{queue.running}</TableCell><TableCell>{queue.expired_leases}</TableCell><TableCell>{queue.recent_failures} / {queue.recent_unknown} / {queue.recent_dead_letters}</TableCell></TableRow>)}</TableBody></Table></div>
      <details><summary className="cursor-pointer text-sm">查看各进程的执行进展（{resource.data.lanes.length}）</summary><div className="mt-3 max-h-96 overflow-auto"><Table><TableHeader><TableRow>{["进程", "执行循环", "状态", "最后进展", "完成轮次", "无进展告警阈值"].map(name => <TableHead key={name}>{name}</TableHead>)}</TableRow></TableHeader><TableBody>{resource.data.lanes.map(lane => <TableRow key={`${lane.instance_id}-${lane.name}`}><TableCell title={lane.instance_id}>{lane.instance_id.slice(0, 8)}</TableCell><TableCell>{names[lane.name] ?? lane.name}</TableCell><TableCell>{states[lane.status] ?? lane.status}</TableCell><TableCell>{date(lane.progress_at)}</TableCell><TableCell>{lane.completed_passes}</TableCell><TableCell>{lane.stall_after_seconds} 秒</TableCell></TableRow>)}</TableBody></Table></div></details>
    </>}
  </section>;
}
