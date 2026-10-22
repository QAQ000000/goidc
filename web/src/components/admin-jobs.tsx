"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { RefreshCw } from "lucide-react";
import { useResource } from "@/hooks/use-resource";
import { date } from "@/lib/api";
import { WorkerRuntimePanel } from "./worker-runtime";
import { AdminPagination } from "./admin-pagination";
import { AdminListPage, AdminListRegion } from "./admin-list-page";
import { ResourceTable, type ResourceColumn } from "./resource-table";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Workspace, PermissionGate, useAdminPermission } from "./workspace";

type Item = {source: string; id: number; service_id: number | null; customer_id: number | null; customer: string; product: string; action: string; description: string; status: string; updated_at: string; has_error: boolean};
type Result = {items: Item[]; total: number; limit: number; offset: number};
type SummaryItem = {source: string; total: number; pending: number; running: number; waiting: number; failed: number};
type Summary = {items: SummaryItem[]};

const sources: Record<string, string> = {provision: "开通任务", email: "邮件队列", sms: "短信队列"};
const statuses: Record<string, string> = {pending: "待处理", queued: "排队中", running: "运行中", sending: "投递中", waiting_manual: "等待人工处理", waiting_verification: "等待核验", succeeded: "成功", sent: "已发送", failed: "失败", dead_letter: "死信", cancelled: "已取消", expired: "已过期"};
const actions: Record<string, string> = {create: "开通", suspend: "暂停", resume: "恢复", terminate: "终止", sync: "同步", upgrade: "升级", reinstall: "重装", reset_password: "重置密码", reboot: "重启", power_on: "开机", power_off: "关机", operation: "服务操作", notification: "通知投递"};
const statusTones: Record<string, string> = {
  failed: "border-destructive/30 bg-destructive/10 text-destructive",
  dead_letter: "border-destructive/30 bg-destructive/10 text-destructive",
};
const selectClass = "h-11 w-full min-w-0 rounded-md border border-input bg-card px-3 text-sm text-foreground shadow-xs outline-none transition-[color,box-shadow] focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 md:h-9";

function JobStatus({status}:{status: string}) {
  return <Badge variant="outline" className={statusTones[status] ?? "border-border bg-muted text-foreground"}>{statuses[status] ?? "未知状态"}</Badge>;
}

function ServiceAction({item, canViewServices}:{item: Item; canViewServices: boolean}) {
  if (canViewServices && item.source === "provision" && item.service_id != null) {
    return <Link className="break-words text-primary underline underline-offset-4" href={`/admin/services/${item.service_id}`}>查看服务</Link>;
  }
  return <span className="text-xs text-muted-foreground">暂不支持统一重试</span>;
}

function JobFilters({params, onChange, onReset}:{params: URLSearchParams | null; onChange: (key: string, value: string) => void; onReset: () => void}) {
  return <section aria-label="任务筛选" className="rounded border border-border bg-card p-4">
    <div className="grid min-w-0 gap-3 sm:grid-cols-2 xl:grid-cols-4">
      <label className="grid min-w-0 gap-1.5 text-sm sm:col-span-2">
        <span>关键词</span>
        <Input className="h-11 md:h-9" aria-label="关键词" placeholder="客户、产品或动作关键词" value={params?.get("q") ?? ""} onChange={e => onChange("q", e.target.value)} />
      </label>
      <label className="grid min-w-0 gap-1.5 text-sm">
        <span>任务来源</span>
        <select className={selectClass} aria-label="任务来源" value={params?.get("source") ?? ""} onChange={e => onChange("source", e.target.value)}>
          <option value="">全部来源</option>
          {Object.entries(sources).map(([key, label]) => <option key={key} value={key}>{label}</option>)}
        </select>
      </label>
      <label className="grid min-w-0 gap-1.5 text-sm">
        <span>任务状态</span>
        <select className={selectClass} aria-label="任务状态" value={params?.get("status") ?? ""} onChange={e => onChange("status", e.target.value)}>
          <option value="">全部状态</option>
          {Object.entries(statuses).map(([key, label]) => <option key={key} value={key}>{label}</option>)}
        </select>
      </label>
      <label className="grid min-w-0 gap-1.5 text-sm">
        <span>操作开始日期</span>
        <Input className="h-11 md:h-9" type="date" aria-label="操作开始日期" value={params?.get("from") ?? ""} onChange={e => onChange("from", e.target.value)} />
      </label>
      <label className="grid min-w-0 gap-1.5 text-sm">
        <span>操作结束日期</span>
        <Input className="h-11 md:h-9" type="date" aria-label="操作结束日期" value={params?.get("to") ?? ""} onChange={e => onChange("to", e.target.value)} />
      </label>
    </div>
    <div className="mt-3 flex justify-end">
      <Button type="button" variant="outline" onClick={onReset}>重置</Button>
    </div>
  </section>;
}

function JobMobileCard({item, canViewServices}:{item: Item; canViewServices: boolean}) {
  return <article className="min-w-0 space-y-3 p-4">
    <div className="flex min-w-0 flex-wrap items-start justify-between gap-2">
      <div className="min-w-0 space-y-1">
        <p className="break-words font-medium">{item.customer || "—"}</p>
        <p className="break-words text-xs text-muted-foreground">{item.product || "—"}</p>
      </div>
      <JobStatus status={item.status} />
    </div>
    <dl className="grid min-w-0 gap-2 text-sm">
      <div className="min-w-0">
        <dt className="text-xs text-muted-foreground">来源 / 动作</dt>
        <dd className="break-words">{sources[item.source] ?? "未知来源"} / {actions[item.action] ?? "业务任务"}</dd>
      </div>
      <div className="min-w-0">
        <dt className="text-xs text-muted-foreground">描述</dt>
        <dd className="whitespace-pre-wrap break-words">{item.description || "—"}</dd>
      </div>
      <div className="min-w-0">
        <dt className="text-xs text-muted-foreground">最后操作时间</dt>
        <dd className="break-words">{date(item.updated_at)}</dd>
      </div>
    </dl>
    {item.has_error && <p className="break-words text-xs text-destructive">存在错误，请在业务页核验</p>}
    <div className="min-w-0"><ServiceAction item={item} canViewServices={canViewServices} /></div>
  </article>;
}

function SummaryMobileCard({item}:{item: SummaryItem}) {
  const counts = [["总数", item.total], ["待处理", item.pending], ["运行中", item.running], ["等待人工 / 核验", item.waiting], ["失败 / 死信", item.failed]] as const;
  return <article className="min-w-0 space-y-3 p-4">
    <p className="break-words font-medium">{sources[item.source] ?? "未知来源"}</p>
    <dl className="grid min-w-0 grid-cols-2 gap-2 text-sm">
      {counts.map(([label, value]) => <div key={label} className="min-w-0 rounded border border-border bg-muted/30 px-2 py-1.5">
        <dt className="text-xs text-muted-foreground">{label}</dt>
        <dd className="tabular-nums">{value}</dd>
      </div>)}
    </dl>
  </article>;
}

function jobColumns(canViewServices: boolean): ResourceColumn<Item>[] {
  return [
    {id: "customer", header: "客户", headerClassName: "w-44", className: "whitespace-normal break-words", render: item => item.customer || "—"},
    {id: "product", header: "产品", headerClassName: "w-44", className: "whitespace-normal break-words", render: item => item.product || "—"},
    {id: "action", header: "动作", headerClassName: "w-20", className: "whitespace-normal", render: item => actions[item.action] ?? "业务任务"},
    {
      id: "description",
      header: "描述",
      headerClassName: "w-48",
      className: "max-w-sm whitespace-normal break-words",
      render: item => <div className="min-w-0 space-y-1">
        <p className="whitespace-pre-wrap break-words">{item.description || "—"}</p>
        {item.has_error && <p className="text-xs text-destructive">存在错误，请在业务页核验</p>}
      </div>,
    },
    {id: "source", header: "来源", headerClassName: "w-24", className: "whitespace-normal", render: item => sources[item.source] ?? "未知来源"},
    {id: "status", header: "状态", headerClassName: "w-32", className: "whitespace-normal", render: item => <JobStatus status={item.status} />},
    {id: "updated", header: "最后操作时间", headerClassName: "w-36", className: "whitespace-normal", render: item => date(item.updated_at)},
    {id: "operation", header: "操作", headerClassName: "w-36", className: "whitespace-normal", render: item => <ServiceAction item={item} canViewServices={canViewServices} />},
  ];
}

const summaryColumns: ResourceColumn<SummaryItem>[] = [
  {id: "source", header: "来源", render: item => sources[item.source] ?? "未知来源"},
  {id: "total", header: "总数", render: item => item.total},
  {id: "pending", header: "待处理", render: item => item.pending},
  {id: "running", header: "运行中", render: item => item.running},
  {id: "waiting", header: "等待人工 / 核验", render: item => item.waiting},
  {id: "failed", header: "失败 / 死信", render: item => item.failed},
];

function QueuePagination({result, onNavigate}:{result: Result; onNavigate: (key: string, value: string) => void}) {
  const limit = Number.isFinite(result.limit) && result.limit > 0 ? result.limit : 25;
  const offset = Number.isFinite(result.offset) && result.offset >= 0 ? result.offset : 0;
  return <AdminPagination
    total={result.total}
    page={Math.floor(offset / limit)}
    pageSize={limit}
    onPageChange={page => onNavigate("offset", String(page * limit))}
    onPageSizeChange={size => onNavigate("limit", String(size))}
  />;
}

export function AdminJobsView({queue = false}:{queue?: boolean}) {
  const allowed = useAdminPermission("jobs.view");
  const canViewServices = useAdminPermission("services.view");
  const [params, setParams] = useState<URLSearchParams | null>(null);

  useEffect(() => {
    const restore = () => setParams(new URLSearchParams(window.location.search));
    restore();
    window.addEventListener("popstate", restore);
    return () => window.removeEventListener("popstate", restore);
  }, []);

  const query = params?.toString() ?? "";
  const resource = useResource<Result | Summary>(
    !allowed ? null : queue ? (params ? `/admin/jobs/queue?${query}` : null) : "/admin/jobs",
    false,
  );

  function change(key: string, value: string) {
    const next = new URLSearchParams(params ?? undefined);
    if (value) next.set(key, value);
    else next.delete(key);
    if (key !== "offset") next.delete("offset");
    const search = next.toString();
    window.history.replaceState(null, "", `${window.location.pathname}${search ? `?${search}` : ""}`);
    setParams(next);
  }

  function reset() {
    window.history.replaceState(null, "", window.location.pathname);
    setParams(new URLSearchParams());
  }

  return <Workspace admin title={queue ? "任务队列" : "任务状态概览"}>
    <AdminListPage
      title={queue ? "任务队列" : "任务状态概览"}
      description={queue
        ? "统一查看服务操作、邮件与短信队列；仅显示当前账号有权查看的任务。发送状态只表示已发送，不代表最终送达；失败任务请进入对应业务页核验。"
        : "仅显示当前账号有权查看的服务及通知任务；本页为业务任务队列汇总，不代表 Worker 健康状态或定时调度历史。失败任务请进入对应业务页核验，避免重复执行外部动作。"}
      actions={<>
        <Button variant="outline" asChild><Link href={queue ? "/admin/jobs" : "/admin/jobs/queue"}>{queue ? "任务概览" : "任务队列"}</Link></Button>
        {!queue && <PermissionGate permission="@super"><Button variant="outline" asChild><Link href="/admin/logs/jobs">调度日志</Link></Button></PermissionGate>}
        <Button type="button" variant="outline" size="icon" aria-label="刷新任务" onClick={resource.refresh}><RefreshCw aria-hidden="true" /></Button>
      </>}
      filters={queue ? <JobFilters params={params} onChange={change} onReset={reset} /> : undefined}
    >
      {queue ? <AdminListRegion
        title="统一任务队列"
        allowed={allowed}
        resource={resource}
        retry={resource.refresh}
        pagination={page => <QueuePagination result={page as Result} onNavigate={change} />}
      >
        {page => {
          const result = page as Result;
          return <ResourceTable
            items={result.items}
            rowKey={item => `${item.source}-${item.id}`}
            columns={jobColumns(canViewServices)}
            ariaLabel="统一任务队列"
            className="min-w-0"
            tableClassName="min-w-[1120px] table-fixed"
            empty="暂无符合条件的任务"
            renderMobile={item => <JobMobileCard item={item} canViewServices={canViewServices} />}
          />;
        }}
      </AdminListRegion> : <AdminListRegion
        title="任务来源汇总"
        allowed={allowed}
        resource={resource}
        retry={resource.refresh}
      >
        {page => {
          const summary = page as Summary;
          return <ResourceTable
            items={summary.items}
            rowKey={item => item.source}
            columns={summaryColumns}
            ariaLabel="任务来源汇总"
            className="min-w-0"
            empty="暂无可见任务"
            renderMobile={item => <SummaryMobileCard item={item} />}
          />;
        }}
      </AdminListRegion>}
      {!queue && <PermissionGate permission="@super"><WorkerRuntimePanel /></PermissionGate>}
    </AdminListPage>
  </Workspace>;
}
