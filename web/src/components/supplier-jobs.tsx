"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { RefreshCw, RotateCcw } from "lucide-react";
import { api, ApiError, date } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { usePendingSetting } from "@/hooks/use-pending-setting";
import { AdminPagination } from "./admin-pagination";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";
import { Workspace, useAdminIdentity, useAdminPermission } from "./workspace";

type Job = { id: number; service_id: number; customer_id: number; customer: string; product: string; supplier_id: number; supplier: string; action: string; description: string; source: string; status: string; attempts: number; created_at: string; updated_at: string; can_retry: boolean; verification_required: boolean };
type Page = { items: Job[]; total: number; limit: number; offset: number };
const actions: Record<string, string> = { create: "开通", suspend: "暂停", resume: "恢复", terminate: "终止", sync: "同步", renew: "续费", upgrade: "升级", reinstall: "重装", reset_password: "重置密码", reboot: "重启", power_on: "开机", power_off: "关机", force_off: "强制关机", force_reboot: "强制重启", rescue_system: "进入救援", exit_rescue_system: "退出救援", snapshot_create: "创建快照", snapshot_delete: "删除快照", snapshot_restore: "恢复快照", backup_create: "创建备份", backup_delete: "删除备份", backup_restore: "恢复备份", nat_acl_create: "创建 NAT 规则", nat_acl_delete: "删除 NAT 规则", nat_web_create: "创建 Web 映射", nat_web_delete: "删除 Web 映射", cdrom_mount: "挂载光驱", cdrom_unmount: "卸载光驱" };
const statuses: Record<string, string> = { pending: "待处理", running: "运行中", waiting_manual: "等待人工", waiting_verification: "等待核验", succeeded: "已完成", failed: "失败", cancelled: "已取消" };

export function SupplierJobsView({ mode }: { mode: "jobs" | "tasks" }) {
  const [params, setParams] = useState<URLSearchParams | null>(null);
  const [busy, setBusy] = useState<number | null>(null);
  const [message, setMessage] = useState("");
  const canRetry = useAdminPermission("services.manage");
  const actor = useAdminIdentity();
  const { pending, unknown, setUnknown, ready, remember, resolve } = usePendingSetting("supplier-job-retry", actor?.id ?? null);
  useEffect(() => {
    const restore = () => setParams(new URLSearchParams(window.location.search));
    restore();
    window.addEventListener("popstate", restore);
    return () => window.removeEventListener("popstate", restore);
  }, []);
  const resource = useResource<Page>(params ? `/admin/supplier-${mode}?${params.toString()}` : null, false);
  function change(key: string, value: string) {
    const next = new URLSearchParams(params ?? undefined);
    if (value) next.set(key, value); else next.delete(key);
    if (key !== "offset") next.delete("offset");
    window.history.replaceState(null, "", `${window.location.pathname}${next.size ? `?${next}` : ""}`);
    setParams(next);
  }
  async function retry(job?: Job) {
    if (!ready || busy !== null || (!unknown && (!job?.can_retry || !window.confirm(`重新排队任务 #${job.id}？任务将在后台执行。`)))) return;
    const operation = unknown ? pending.current : job ? { path: `/admin/supplier-jobs/${job.id}/retry`, body: JSON.stringify({ expected_attempts: job.attempts, expected_updated_at: job.updated_at }), key: crypto.randomUUID() } : null;
    if (!operation?.path) return;
    remember(operation);
    setBusy(job?.id ?? -1); setMessage("");
    try {
      await api(operation.path, { method: "POST", headers: { "Idempotency-Key": operation.key }, body: operation.body });
      resolve(); setMessage("任务已重新排队");
    } catch (error) {
      if (!(error instanceof ApiError) || error.status >= 500) { setUnknown(true); setMessage("重试结果暂无法确认，请只重发原请求。"); }
      else { resolve(); setMessage((error as Error).message); }
    }
    finally { setBusy(null); resource.refresh(); }
  }
  const limit = Number(params?.get("limit")) || 25;
  const page = Math.floor((Number(params?.get("offset")) || 0) / limit);
  const title = mode === "jobs" ? "供应商任务队列" : "供应商交付任务";
  return <Workspace admin title={title}><div className="space-y-5">
    <div className="flex items-center justify-between gap-3"><h1 className="page-title">{title}</h1><Button variant="outline" size="icon" title="刷新任务" aria-label="刷新任务" onClick={resource.refresh}><RefreshCw /></Button></div>
    {unknown && <div className="flex items-center gap-3 border-y py-3 text-sm"><span>上次重试结果暂无法确认</span><Button type="button" variant="outline" disabled={!ready || busy !== null} onClick={() => void retry()}><RotateCcw />重试原请求</Button></div>}
    <div className="grid gap-3 border-y py-4 sm:grid-cols-2 lg:grid-cols-4">
      <Input aria-label="关键词" placeholder="客户、产品、供应商或动作" value={params?.get("q") ?? ""} onChange={event => change("q", event.target.value)} />
      <Input aria-label="客户编号" type="number" min={1} placeholder="客户编号" value={params?.get("customer_id") ?? ""} onChange={event => change("customer_id", event.target.value)} />
      <Input aria-label="供应商编号" type="number" min={1} placeholder="供应商编号" value={params?.get("supplier_id") ?? ""} onChange={event => change("supplier_id", event.target.value)} />
      <select className="rounded border p-2 text-sm" aria-label="动作" value={params?.get("action") ?? ""} onChange={event => change("action", event.target.value)}><option value="">全部动作</option>{Object.entries(actions).map(([key, label]) => <option key={key} value={key}>{label}</option>)}</select>
      <select className="rounded border p-2 text-sm" aria-label="状态" value={params?.get("status") ?? ""} onChange={event => change("status", event.target.value)}><option value="">全部状态</option>{Object.entries(statuses).map(([key, label]) => <option key={key} value={key}>{label}</option>)}</select>
      <label className="text-sm">开始日期<Input type="date" value={params?.get("from") ?? ""} onChange={event => change("from", event.target.value)} /></label>
      <label className="text-sm">结束日期<Input type="date" value={params?.get("to") ?? ""} onChange={event => change("to", event.target.value)} /></label>
      <Button type="button" variant="outline" onClick={() => { window.history.replaceState(null, "", window.location.pathname); setParams(new URLSearchParams()); }}>清空筛选</Button>
    </div>
    {message && <p role="status" className="text-sm">{message}</p>}
    {resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <><div className="overflow-x-auto border-y"><Table><TableHeader><TableRow>{["客户", "产品", "动作", "描述", "来源", "状态", "最后操作时间", "操作"].map(label => <TableHead key={label}>{label}</TableHead>)}</TableRow></TableHeader><TableBody>{resource.data.items.map(job => <TableRow key={job.id}>
      <TableCell>{job.customer}<span className="block text-xs text-muted-foreground">#{job.customer_id}</span></TableCell>
      <TableCell>{job.product}</TableCell><TableCell>{actions[job.action] ?? job.action}</TableCell><TableCell>{job.description} #{job.id}</TableCell>
      <TableCell>{job.supplier}<span className="block text-xs text-muted-foreground">供应商交付</span></TableCell>
      <TableCell>{statuses[job.status] ?? job.status}</TableCell><TableCell>{date(job.updated_at)}</TableCell>
      <TableCell><div className="flex items-center gap-3 whitespace-nowrap"><Link className="text-primary underline" href={`/admin/services/${job.service_id}`}>{job.status === "waiting_manual" ? "前往处理" : job.verification_required ? "前往核验" : "查看服务"}</Link>{job.can_retry && canRetry && <Button type="button" variant="outline" size="sm" disabled={!ready || busy !== null || unknown} onClick={() => void retry(job)}><RotateCcw />重试</Button>}</div></TableCell>
    </TableRow>)}</TableBody></Table></div>{!resource.data.items.length && <p role="status" className="py-8 text-center text-sm text-muted-foreground">暂无符合条件的任务</p>}
      <AdminPagination total={resource.data.total} page={page} pageSize={limit} onPageChange={value => change("offset", String(value * limit))} onPageSizeChange={value => change("limit", String(value))} /></>}
  </div></Workspace>;
}
