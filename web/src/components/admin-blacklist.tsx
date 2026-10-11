"use client";

import { useState } from "react";
import { api, ApiError } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace, useAdminPermission } from "./workspace";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Input } from "./ui/input";

type Row = { id: number; subject_type: "username" | "ip"; subject: string; operation_type: string; remote_addr: string; reason: string; blocked_until: string | null; active: boolean; version: number; created_at: string };
type Page = { items: Row[]; total: number; limit: number; offset: number };

const key = () => typeof crypto !== "undefined" && "randomUUID" in crypto ? crypto.randomUUID() : `blacklist-${Date.now()}-${Math.random()}`;
const date = (value: string | null) => value ? new Date(value).toLocaleString("zh-CN", { timeZone: "Asia/Shanghai" }) : "永久";

export function AdminBlacklist() {
  const [filters, setFilters] = useState({ q: "", active: "true" });
  const resource = useResource<Page>(`/admin/blacklist?limit=50&offset=0&q=${encodeURIComponent(filters.q)}&active=${filters.active}`, false);
  if (resource.error) return <Workspace admin title="登录黑名单"><Failure error={resource.error} retry={resource.refresh} /></Workspace>;
  if (!resource.data) return <Workspace admin title="登录黑名单"><Loading /></Workspace>;
  return <Workspace admin title="登录黑名单"><BlacklistPanel resource={resource} filters={filters} setFilters={setFilters} /></Workspace>;
}

function BlacklistPanel({ resource, filters, setFilters }: { resource: ReturnType<typeof useResource<Page>>; filters: { q: string; active: string }; setFilters: (value: { q: string; active: string }) => void }) {
  const manage = useAdminPermission("security.manage");
  const [form, setForm] = useState({ subject_type: "username", subject: "", remote_addr: "", reason: "", blocked_until: "" });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [active, setActive] = useState("true");
  function refreshFiltered(nextQuery = query, nextActive = active) { setFilters({ q: nextQuery, active: nextActive }); }
  async function add() {
    if (!manage || busy) return;
    setBusy(true); setError("");
    try {
      await api("/admin/blacklist", { method: "POST", headers: { "Idempotency-Key": key() }, body: JSON.stringify({ ...form, blocked_until: form.blocked_until ? new Date(form.blocked_until).toISOString() : null, expected_version: 0 }) });
      setForm({ subject_type: "username", subject: "", remote_addr: "", reason: "", blocked_until: "" });
      refreshFiltered();
      resource.refresh();
    } catch (e) { setError(e instanceof ApiError && e.status === 409 ? "记录已存在或版本冲突，请刷新后重试。" : e instanceof Error ? e.message : "保存失败"); }
    finally { setBusy(false); }
  }
  async function remove(row: Row) {
    if (!manage || busy || !window.confirm(`确认解除 ${row.subject} 的登录阻断？`)) return;
    setBusy(true); setError("");
    try { await api(`/admin/blacklist/${row.id}`, { method: "DELETE", headers: { "Idempotency-Key": key() }, body: JSON.stringify({ expected_version: row.version }) }); resource.refresh(); }
    catch (e) { setError(e instanceof ApiError && e.status === 409 ? "记录已变更，请刷新后重试。" : e instanceof Error ? e.message : "解除失败"); }
    finally { setBusy(false); }
  }
  function search(event: React.FormEvent) { event.preventDefault(); void refreshFiltered(query, active); }
  return <div className="space-y-5"><div><h1 className="page-title">登录黑名单</h1><p className="mt-2 text-sm text-muted-foreground">连续三次管理员登录失败会自动阻断 3 小时，也可以手动添加或解除用户名/IP 阻断。</p></div>
    {manage && <section className="data-surface p-4 space-y-4"><h2 className="font-semibold">添加阻断</h2><div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-5"><label className="grid gap-1 text-sm">类型<select className="h-9 rounded-md border bg-background px-3" value={form.subject_type} onChange={e => setForm({ ...form, subject_type: e.target.value as "username" | "ip" })}><option value="username">用户名</option><option value="ip">IP 地址</option></select></label><label className="grid gap-1 text-sm">对象<Input value={form.subject} onChange={e => setForm({ ...form, subject: e.target.value })} placeholder={form.subject_type === "ip" ? "192.0.2.1" : "管理员用户名"} /></label><label className="grid gap-1 text-sm">来源 IP<Input value={form.remote_addr} onChange={e => setForm({ ...form, remote_addr: e.target.value })} placeholder="可选" /></label><label className="grid gap-1 text-sm">解封时间<Input type="datetime-local" value={form.blocked_until} onChange={e => setForm({ ...form, blocked_until: e.target.value })} /></label><label className="grid gap-1 text-sm">原因<Input value={form.reason} maxLength={500} onChange={e => setForm({ ...form, reason: e.target.value })} placeholder="可选" /></label></div><Button type="button" disabled={busy || !form.subject.trim()} onClick={() => void add()}>添加阻断</Button></section>}
    <form onSubmit={search} className="flex flex-wrap items-end gap-3"><label className="grid gap-1 text-sm">搜索<Input value={query} onChange={e => setQuery(e.target.value)} placeholder="用户名、IP 或原因" /></label><label className="grid gap-1 text-sm">状态<select className="h-9 rounded-md border bg-background px-3" value={active} onChange={e => setActive(e.target.value)}><option value="true">生效中</option><option value="false">已过期</option><option value="">全部</option></select></label><Button variant="outline">搜索</Button></form>
    {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    {resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <div className="data-surface overflow-x-auto"><table className="w-full min-w-[760px] text-sm"><thead><tr className="border-b text-left text-muted-foreground"><th className="px-3 py-3">ID</th><th>用户名/IP</th><th>操作类型</th><th>来源 IP</th><th>阻断时间</th><th>状态</th><th className="text-right px-3">操作</th></tr></thead><tbody>{resource.data.items.length === 0 ? <tr><td colSpan={7} className="py-12 text-center text-muted-foreground">暂无黑名单记录</td></tr> : resource.data.items.map(row => <tr key={row.id} className="border-t"><td className="px-3 py-3">{row.id}</td><td><span className="font-medium">{row.subject}</span><span className="ml-2 text-xs text-muted-foreground">{row.subject_type === "ip" ? "IP" : "用户名"}</span>{row.reason && <span className="block text-xs text-muted-foreground">{row.reason}</span>}</td><td>{row.operation_type}</td><td className="font-mono text-xs">{row.remote_addr || "-"}</td><td>{date(row.blocked_until)}</td><td>{row.active ? "生效中" : "已过期"}</td><td className="px-3 text-right">{manage && <Button type="button" variant="ghost" size="sm" disabled={busy} onClick={() => void remove(row)}>解除</Button>}</td></tr>)}</tbody></table><div className="border-t p-3 text-sm text-muted-foreground">共 {resource.data.total} 条</div></div>}
  </div>;
}
