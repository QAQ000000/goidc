"use client";

import { useEffect, useState } from "react";
import { useResource } from "@/hooks/use-resource";
import { date } from "@/lib/api";
import { Workspace } from "./workspace";
import { AdminListPage, AdminListRegion } from "./admin-list-page";
import { AdminPagination } from "./admin-pagination";
import { ResourceTable, type ResourceColumn } from "./resource-table";
import { Button } from "./ui/button";
import { Badge } from "./ui/badge";
import { Input } from "./ui/input";

const states: Record<string, string> = { running: "运行中 / 结束未确认", succeeded: "成功", failed: "失败", interrupted: "已中断" };
type Run = { id: number; task_key: string; started_at: string; finished_at: string | null; duration_ms: number | null; status: string; affected_count: number | null; error_code: string };
type Result = { items: Run[]; total: number; tasks: { key: string; name: string }[] };
type TrendRow = { day: string; total: number; succeeded: number; failed: number; running: number; interrupted: number };
type Trend = { items: TrendRow[] };
const selectClass = "h-9 w-full rounded-md border border-input bg-background px-3 text-sm";
const status = (row: Run) => <Badge variant={row.status === "failed" ? "destructive" : "secondary"}>{states[row.status] ?? row.status}</Badge>;
const trendColumns: ResourceColumn<TrendRow>[] = [
  { id: "day", header: "日期", render: row => row.day },
  { id: "total", header: "总数", render: row => row.total },
  { id: "succeeded", header: "成功", render: row => row.succeeded },
  { id: "failed", header: "失败", render: row => row.failed },
  { id: "running", header: "运行中", render: row => row.running },
  { id: "interrupted", header: "中断", render: row => row.interrupted },
];

export function AdminSchedulerLogs() {
  const [params, setParams] = useState<URLSearchParams | null>(null);
  useEffect(() => {
    const restore = () => setParams(new URLSearchParams(window.location.search));
    restore(); window.addEventListener("popstate", restore);
    return () => window.removeEventListener("popstate", restore);
  }, []);
  const query = params?.toString();
  const logs = useResource<Result>(params ? `/admin/logs/jobs?${query}` : null, false);
  const trend = useResource<Trend>(params ? `/admin/logs/jobs/trend?${query}` : null, false);
  const tasks = logs.error ? [] : logs.data?.tasks ?? [];
  const taskNames = new Map(tasks.map(task => [task.key, task.name]));
  const selectedTask = params?.get("task") ?? "";
  const limit = Number(params?.get("limit")) || 25;
  const page = Math.floor((Number(params?.get("offset")) || 0) / limit);
  function change(key: string, value: string) {
    const next = new URLSearchParams(params ?? undefined);
    if (value) next.set(key, value); else next.delete(key);
    if (key !== "offset") next.delete("offset");
    window.history.replaceState(null, "", `${window.location.pathname}?${next}`); setParams(next);
  }
  const columns: ResourceColumn<Run>[] = [
    { id: "task", header: "任务", render: row => <div className="min-w-0 space-y-1"><p className="break-words font-medium">{taskNames.get(row.task_key) ?? row.task_key}</p><p className="break-all text-xs text-muted-foreground">#{row.id} · {row.task_key}</p></div> },
    { id: "started", header: "开始时间", render: row => date(row.started_at) },
    { id: "finished", header: "结束时间", render: row => row.finished_at ? date(row.finished_at) : "—" },
    { id: "duration", header: "耗时（毫秒）", render: row => row.duration_ms ?? "—" },
    { id: "status", header: "状态", render: status },
    { id: "affected", header: "处理数", render: row => row.affected_count ?? "—" },
    { id: "error", header: "安全错误码", render: row => <span className="break-all font-mono text-xs">{row.error_code || "—"}</span> },
  ];
  return <Workspace admin title="定时任务日志"><AdminListPage title="定时任务日志" description="只供超级管理员查看；仅记录启用后的真实调度，不回填历史。调度成功表示本轮处理或入队完成，不代表通知送达或供应商外部操作成功。运行中记录可能由进程异常退出留下，不能据此自动重试。"
    actions={<Button variant="outline" onClick={() => { logs.refresh(); trend.refresh(); }}>刷新</Button>}
    filters={<div className="flex flex-wrap items-end gap-3 rounded border border-border bg-card p-4"><label className="grid min-w-0 gap-2 text-sm">任务类别<select aria-label="任务类别" className={selectClass} value={selectedTask} onChange={e => change("task", e.target.value)}><option value="">全部任务</option>{selectedTask && !taskNames.has(selectedTask) && <option value={selectedTask}>当前任务：{selectedTask}</option>}{tasks.map(task => <option key={task.key} value={task.key}>{task.name}</option>)}</select></label><label className="grid gap-2 text-sm">运行状态<select aria-label="运行状态" className={selectClass} value={params?.get("status") ?? ""} onChange={e => change("status", e.target.value)}><option value="">全部状态</option>{Object.entries(states).map(([key, name]) => <option key={key} value={key}>{name}</option>)}</select></label>{["from", "to"].map(key => <label className="grid gap-2 text-sm" key={key}>{key === "from" ? "开始日期（上海）" : "结束日期（上海）"}<Input type="date" value={params?.get(key) ?? ""} onChange={e => change(key, e.target.value)} /></label>)}<Button variant="outline" onClick={() => { window.history.replaceState(null, "", window.location.pathname); setParams(new URLSearchParams()); }}>清空筛选</Button></div>}>
    <AdminListRegion title="调度执行记录" resource={logs} retry={logs.refresh}
      pagination={data => <AdminPagination total={data.total} page={page} pageSize={limit} onPageChange={index => change("offset", String(index * limit))} onPageSizeChange={size => change("limit", String(size))} />}>
      {data => <ResourceTable items={data.items} rowKey={row => row.id} columns={columns} ariaLabel="调度执行记录" className="min-w-0" tableClassName="min-w-[900px]" empty="暂无真实调度记录" renderMobile={row => <article className="space-y-3 p-4"><div className="space-y-1"><p className="break-words font-medium">{taskNames.get(row.task_key) ?? row.task_key}</p><p className="break-all text-xs text-muted-foreground">#{row.id} · {row.task_key}</p></div>{status(row)}<dl className="grid gap-2 text-sm">{[["开始时间", date(row.started_at)], ["结束时间", row.finished_at ? date(row.finished_at) : "—"], ["耗时（毫秒）", row.duration_ms ?? "—"], ["处理数", row.affected_count ?? "—"], ["安全错误码", row.error_code || "—"]].map(([label, value]) => <div key={label}><dt className="text-muted-foreground">{label}</dt><dd className="break-all">{value}</dd></div>)}</dl></article>} />}
    </AdminListRegion>
    <AdminListRegion title="近31天执行趋势（上海时间）" resource={trend} retry={trend.refresh}>
      {data => <ResourceTable items={data.items} rowKey={row => row.day} columns={trendColumns} ariaLabel="调度执行趋势" className="min-w-0" tableClassName="min-w-[600px]" empty="暂无趋势数据" renderMobile={row => <article className="space-y-3 p-4"><p className="font-medium">{row.day}</p><dl className="grid grid-cols-2 gap-3 text-sm">{[["总数", row.total], ["成功", row.succeeded], ["失败", row.failed], ["运行中", row.running], ["中断", row.interrupted]].map(([label, value]) => <div key={label}><dt className="text-muted-foreground">{label}</dt><dd className="tabular-nums">{value}</dd></div>)}</dl></article>} />}
    </AdminListRegion>
  </AdminListPage></Workspace>;
}
