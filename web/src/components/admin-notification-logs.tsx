"use client";

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { useResource } from "@/hooks/use-resource";
import { date } from "@/lib/api";
import { adminNavigationItem, can } from "@/lib/admin-navigation";
import { Workspace, useAdminIdentity } from "./workspace";
import { AdminPagination } from "./admin-pagination";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";

type LogRow = Record<string, unknown> & { id: number; created_at: string };
type LogResult = { items: LogRow[]; total: number; limit: number; offset: number };

const tabs = [
  ["/admin/logs/sms", "短信日志"],
  ["/admin/logs/inbox", "站内信日志"],
  ["/admin/logs/api", "API 日志"],
  ["/admin/logs/email", "邮件日志"],
] as const;

const labels: Record<string, string> = {
  pending: "待处理", sending: "发送中", sent: "成功", expired: "已过期", dead_letter: "失败",
  read: "已读", failed: "失败",
};

function value(row: LogRow, key: string) {
  const v = row[key];
  if (v === null || v === undefined || v === "") return "—";
  return String(v);
}

function status(valueText: string) {
  return labels[valueText] ?? valueText;
}

export function AdminNotificationLogs({ kind }: { kind: "sms" | "inbox" | "api" | "email" }) {
  const user = useAdminIdentity();
  const [params, setParams] = useState<URLSearchParams | null>(null);
  useEffect(() => {
    const restore = () => setParams(new URLSearchParams(window.location.search));
    restore();
    window.addEventListener("popstate", restore);
    return () => window.removeEventListener("popstate", restore);
  }, []);

  const path = `/admin/logs/${kind}`;
  const permission = adminNavigationItem(path)?.permission;
  const allowed = !!permission && can(user, permission);
  const query = params?.toString();
  const logs = useResource<LogResult>(allowed && params ? `${path}?${query}` : null, false);
  const limit = Number(params?.get("limit")) || 25;
  const page = Math.floor((Number(params?.get("offset")) || 0) / limit);
  const title = kind === "sms" ? "短信日志" : kind === "inbox" ? "站内信日志" : kind === "api" ? "API 日志" : "邮件日志";
  const columns = useMemo(() => {
    if (kind === "sms") return [["id", "ID"], ["created_at", "时间"], ["content", "短信内容"], ["username", "用户名"], ["recipient", "电话"], ["status", "是否成功"], ["last_error", "失败原因"], ["remote_ip", "IP"]];
    if (kind === "inbox") return [["created_at", "发送时间"], ["subject", "标题"], ["username", "用户"], ["status", "状态"]];
    if (kind === "api") return [["id", "ID"], ["created_at", "时间"], ["path", "描述"], ["username", "用户名"], ["status_code", "状态码"], ["correlation_id", "关联编号"], ["remote_ip", "IP"]];
    return [["id", "ID"], ["created_at", "时间"], ["subject", "主题"], ["recipient", "收件人"], ["success", "是否成功"], ["last_error", "失败原因"], ["remote_ip", "IP"]];
  }, [kind]);

  function change(key: string, nextValue: string) {
    const next = new URLSearchParams(params ?? undefined);
    if (nextValue) next.set(key, nextValue); else next.delete(key);
    if (key !== "offset") next.delete("offset");
    const suffix = next.toString();
    window.history.replaceState(null, "", `${path}${suffix ? `?${suffix}` : ""}`);
    setParams(next);
  }

  function clear() {
    window.history.replaceState(null, "", path);
    setParams(new URLSearchParams());
  }

  if (!allowed) return <Workspace admin title={title}><p role="alert">没有查看此日志的权限</p></Workspace>;

  return <Workspace admin title={title}>
    <div className="min-w-0 max-w-full space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="page-title">{title}</h1>
        <div className="flex flex-wrap gap-1" aria-label="日志类型">
          {tabs.filter(([href]) => { const item = adminNavigationItem(href); return !!item && can(user, item.permission); }).map(([href, label]) => <Button key={href} asChild size="sm" variant={href === path ? "default" : "outline"}><Link href={href}>{label}</Link></Button>)}
        </div>
      </div>
      <div className="flex flex-wrap items-end gap-3 rounded border bg-card p-4">
        <label className="min-w-[180px] flex-1 text-sm">关键词<Input aria-label="关键词" placeholder="高级搜索" value={params?.get("q") ?? ""} onChange={e => change("q", e.target.value)} /></label>
        {kind !== "api" && <label className="min-w-[140px] text-sm">状态<select aria-label="状态" className="mt-1 h-9 w-full rounded border bg-background px-2 text-sm" value={params?.get("status") ?? ""} onChange={e => change("status", e.target.value)}><option value="">全部状态</option>{(kind === "inbox" ? ["sent", "read", "failed"] : ["pending", "sending", "sent", "expired", "dead_letter"]).map(s => <option key={s} value={s}>{labels[s]}</option>)}</select></label>}
        {kind === "api" && <label className="min-w-[220px] text-sm">关联编号<Input aria-label="关联编号" placeholder="完整错误编号" maxLength={32} value={params?.get("correlation_id") ?? ""} onChange={e => change("correlation_id", e.target.value)} /></label>}
        {kind === "api" && <label className="min-w-[120px] text-sm">方法<select aria-label="方法" className="mt-1 h-9 w-full rounded border bg-background px-2 text-sm" value={params?.get("method") ?? ""} onChange={e => change("method", e.target.value)}><option value="">全部方法</option>{["GET", "POST", "PUT", "PATCH", "DELETE"].map(s => <option key={s}>{s}</option>)}</select></label>}
        {kind === "api" && <label className="w-24 text-sm">状态码<Input aria-label="状态码" inputMode="numeric" value={params?.get("status") ?? ""} onChange={e => change("status", e.target.value)} /></label>}
        {kind === "api" && <label className="min-w-[140px] text-sm">用户名<Input aria-label="用户名" value={params?.get("username") ?? ""} onChange={e => change("username", e.target.value)} /></label>}
        {kind === "email" && <label className="min-w-[160px] flex-1 text-sm">收件人<Input aria-label="收件人" value={params?.get("recipient") ?? ""} onChange={e => change("recipient", e.target.value)} /></label>}
        {kind === "email" && <label className="min-w-[160px] flex-1 text-sm">主题<Input aria-label="主题" value={params?.get("subject") ?? ""} onChange={e => change("subject", e.target.value)} /></label>}
        {kind === "sms" && <label className="min-w-[140px] text-sm">电话<Input aria-label="电话" value={params?.get("phone") ?? ""} onChange={e => change("phone", e.target.value)} /></label>}
        {kind !== "api" && <label className="min-w-[140px] text-sm">用户名<Input aria-label="用户名" value={params?.get("username") ?? ""} onChange={e => change("username", e.target.value)} /></label>}
        <label className="text-sm">开始日期<Input type="date" aria-label="开始日期" value={params?.get("from") ?? ""} onChange={e => change("from", e.target.value)} /></label>
        <label className="text-sm">结束日期<Input type="date" aria-label="结束日期" value={params?.get("to") ?? ""} onChange={e => change("to", e.target.value)} /></label>
        <Button onClick={() => logs.refresh()}>搜索</Button><Button variant="outline" onClick={clear}>清空</Button>
      </div>
      {logs.error ? <Failure error={logs.error} retry={logs.refresh} /> : !logs.data ? <Loading /> : <>
        <div className="min-w-0 overflow-x-auto"><Table className="min-w-[760px]"><TableHeader><TableRow>{columns.map(([, label]) => <TableHead key={label}>{label}</TableHead>)}</TableRow></TableHeader><TableBody>{logs.data.items.map(row => <TableRow key={row.id}>{columns.map(([key]) => <TableCell key={key}>{key === "created_at" ? date(row.created_at) : key === "status" ? status(value(row, key)) : key === "success" ? (row.success ? "是" : "否") : value(row, key)}</TableCell>)}</TableRow>)}</TableBody></Table></div>
        {!logs.data.items.length && <p role="status" className="py-10 text-center text-sm text-muted-foreground">暂无真实日志记录</p>}
        <AdminPagination total={logs.data.total} page={page} pageSize={limit} onPageChange={p => change("offset", String(p * limit))} onPageSizeChange={n => change("limit", String(n))} />
      </>}
    </div>
  </Workspace>;
}
