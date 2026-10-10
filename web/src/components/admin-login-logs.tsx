"use client";

import { useEffect, useState } from "react";
import { useResource } from "@/hooks/use-resource";
import { api, date } from "@/lib/api";
import { Workspace } from "./workspace";
import { AdminPagination } from "./admin-pagination";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";

type LoginRow = { id: number; login_time: string; last_access: string; logout_time: string | null; username: string; display_name: string; ip: string };
type LoginResult = { items: LoginRow[]; total: number; limit: number; offset: number };

export function AdminLoginLogs() {
  const [params, setParams] = useState<URLSearchParams | null>(null);
  useEffect(() => {
    const restore = () => setParams(new URLSearchParams(window.location.search));
    restore(); window.addEventListener("popstate", restore);
    return () => window.removeEventListener("popstate", restore);
  }, []);
  const query = params?.toString();
  const resource = useResource<LoginResult>(params ? `/admin/logs/admin-login?${query}` : null, false);
  const limit = Number(params?.get("limit")) || 25;
  const page = Math.floor((Number(params?.get("offset")) || 0) / limit);
  function change(key: string, value: string) {
    const next = new URLSearchParams(params ?? undefined);
    if (value) next.set(key, value); else next.delete(key);
    if (key !== "offset") next.delete("offset");
    const suffix = next.toString(); window.history.replaceState(null, "", `/admin/logs/admin-login${suffix ? `?${suffix}` : ""}`); setParams(next);
  }
  return <Workspace admin title="管理员登录日志"><div className="min-w-0 max-w-full space-y-5">
    <div className="flex flex-wrap items-center justify-between gap-3"><h1 className="page-title">管理员登录日志</h1><Button variant="outline" onClick={() => resource.refresh()}>刷新</Button></div>
    <div className="flex flex-wrap items-end gap-3 rounded border bg-card p-4">
      <label className="min-w-[220px] flex-1 text-sm">用户名<Input aria-label="用户名" placeholder="请输入用户名" value={params?.get("username") ?? ""} onChange={e => change("username", e.target.value)} /></label>
      <label className="min-w-[180px] text-sm">IP地址<Input aria-label="IP地址" placeholder="请输入 IP" value={params?.get("ip") ?? ""} onChange={e => change("ip", e.target.value)} /></label>
      <label className="text-sm">开始日期<Input type="date" aria-label="开始日期" value={params?.get("from") ?? ""} onChange={e => change("from", e.target.value)} /></label>
      <label className="text-sm">结束日期<Input type="date" aria-label="结束日期" value={params?.get("to") ?? ""} onChange={e => change("to", e.target.value)} /></label>
      <Button variant="outline" onClick={() => { window.history.replaceState(null, "", "/admin/logs/admin-login"); setParams(new URLSearchParams()); }}>清空</Button>
    </div>
    {resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <>
      <div className="min-w-0 overflow-x-auto"><Table className="min-w-[760px]"><TableHeader><TableRow>{["ID", "登录时间", "最后访问", "注销时间", "用户名", "IP地址"].map(x => <TableHead key={x}>{x}</TableHead>)}</TableRow></TableHeader><TableBody>{resource.data.items.map(row => <TableRow key={row.id}><TableCell>{row.id}</TableCell><TableCell>{date(row.login_time)}</TableCell><TableCell>{date(row.last_access)}</TableCell><TableCell>{row.logout_time ? date(row.logout_time) : "—"}</TableCell><TableCell>{row.display_name || row.username}</TableCell><TableCell>{row.ip || "—"}</TableCell></TableRow>)}</TableBody></Table></div>
      {!resource.data.items.length && <p role="status" className="py-10 text-center text-sm text-muted-foreground">暂无真实登录记录</p>}
      <AdminPagination total={resource.data.total} page={page} pageSize={limit} onPageChange={p => change("offset", String(p * limit))} onPageSizeChange={n => change("limit", String(n))} />
    </>}
  </div></Workspace>;
}

export function LogCleanupView() {
  const [kind, setKind] = useState("audit");
  const [before, setBefore] = useState("");
  const [ack, setAck] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  async function submit() {
    if (!before || !ack || busy) return;
    if (!window.confirm("清理后无法恢复，确认继续吗？")) return;
    setBusy(true); setError(""); setMessage("");
    try {
      const result = await api<{ deleted: number }>("/admin/logs/cleanup", { method: "POST", headers: { "Idempotency-Key": crypto.randomUUID() }, body: JSON.stringify({ log_type: kind, before: new Date(before).toISOString(), acknowledged: true }) });
      setMessage(`已清理 ${result.deleted} 条日志`); setAck(false);
    } catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  }
  return <Workspace admin title="日志清理"><div className="max-w-2xl space-y-5"><h1 className="page-title">日志清理</h1><p className="text-sm text-muted-foreground">仅删除截止时间以前已完成的历史日志，进行中的投递和任务会保留。</p>
    <div className="space-y-4 rounded border bg-card p-5"><label className="block text-sm">选择日志<select className="mt-1 h-9 w-full rounded border bg-background px-2" value={kind} onChange={e => setKind(e.target.value)}><option value="audit">系统审计日志</option><option value="email">邮件日志</option><option value="sms">短信日志</option><option value="scheduler">定时任务日志</option></select></label>
      <label className="block text-sm">删除此时间以前的记录<Input type="datetime-local" value={before} onChange={e => setBefore(e.target.value)} /></label>
      <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={ack} onChange={e => setAck(e.target.checked)} />我确认该操作不可恢复</label>
      <Button disabled={busy || !before || !ack} onClick={submit}>{busy ? "提交中..." : "提交清理"}</Button>
      {message && <p role="status" className="text-sm text-emerald-700">{message}</p>}{error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    </div></div></Workspace>;
}
