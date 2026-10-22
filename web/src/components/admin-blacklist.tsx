"use client";

import { useState } from "react";
import { api, ApiError } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace, useAdminPermission } from "./workspace";
import { AdminListPage, AdminListRegion } from "./admin-list-page";
import { AdminPagination } from "./admin-pagination";
import { ResourceTable, type ResourceColumn } from "./resource-table";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Badge } from "./ui/badge";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "./ui/dialog";

type Row = { id: number; subject_type: "username" | "ip"; subject: string; operation_type: string; remote_addr: string; reason: string; blocked_until: string | null; active: boolean; version: number; created_at: string };
type Page = { items: Row[]; total: number; limit: number; offset: number };

const key = () => typeof crypto !== "undefined" && "randomUUID" in crypto ? crypto.randomUUID() : `blacklist-${Date.now()}-${Math.random()}`;
const date = (value: string | null) => value ? new Date(value).toLocaleString("zh-CN", { timeZone: "Asia/Shanghai" }) : "永久";
const blank = { subject_type: "username", subject: "", remote_addr: "", reason: "", blocked_until: "" };
const selectClass = "h-9 w-full rounded-md border border-input bg-background px-3 text-sm";

export function AdminBlacklist() {
  const allowed = useAdminPermission("security.view");
  const manage = useAdminPermission("security.manage");
  const [filters, setFilters] = useState({ q: "", active: "true", limit: 50, offset: 0 });
  const resource = useResource<Page>(allowed ? `/admin/blacklist?limit=${filters.limit}&offset=${filters.offset}&q=${encodeURIComponent(filters.q)}&active=${filters.active}` : null, false);
  const [form, setForm] = useState(blank);
  const [adding, setAdding] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [active, setActive] = useState("true");
  async function add(event: React.FormEvent) {
    event.preventDefault();
    if (!manage || busy) return;
    setBusy(true); setError("");
    try {
      await api("/admin/blacklist", { method: "POST", headers: { "Idempotency-Key": key() }, body: JSON.stringify({ ...form, blocked_until: form.blocked_until ? new Date(form.blocked_until).toISOString() : null, expected_version: 0 }) });
      setForm(blank); setAdding(false);
      setFilters(current => ({ ...current, offset: 0 }));
      resource.refresh();
    } catch (e) { setError(e instanceof ApiError && e.status === 409 ? "记录已存在或版本冲突，请刷新后重试。" : e instanceof Error ? e.message : "保存失败"); }
    finally { setBusy(false); }
  }
  async function remove(row: Row) {
    if (!manage || busy || !window.confirm(`确认解除 ${row.subject} 的登录阻断？`)) return;
    setBusy(true); setError("");
    try {
      await api(`/admin/blacklist/${row.id}`, { method: "DELETE", headers: { "Idempotency-Key": key() }, body: JSON.stringify({ expected_version: row.version }) });
      if (resource.data?.items.length === 1 && filters.offset > 0) setFilters(current => ({ ...current, offset: Math.max(0, current.offset - current.limit) }));
      resource.refresh();
    } catch (e) { setError(e instanceof ApiError && e.status === 409 ? "记录已变更，请刷新后重试。" : e instanceof Error ? e.message : "解除失败"); }
    finally { setBusy(false); }
  }
  function search(event: React.FormEvent) { event.preventDefault(); setFilters(current => ({ ...current, q: query, active, offset: 0 })); }
  const status = (row: Row) => <Badge variant={row.active ? "secondary" : "outline"}>{row.active ? "生效中" : "已过期"}</Badge>;
  const action = (row: Row) => manage && <Button type="button" variant="outline" size="sm" aria-label={`解除${row.subject}`} disabled={busy} onClick={() => void remove(row)}>解除</Button>;
  const subject = (row: Row) => <div className="min-w-0 space-y-1"><p className="break-all font-medium">{row.subject}</p><p className="text-xs text-muted-foreground">#{row.id} · {row.subject_type === "ip" ? "IP 地址" : "用户名"}</p>{row.reason && <p className="max-w-md whitespace-pre-wrap break-words text-xs text-muted-foreground">{row.reason}</p>}</div>;
  const columns: ResourceColumn<Row>[] = [
    { id: "subject", header: "阻断对象 / 原因", render: subject },
    { id: "operation", header: "操作类型", render: row => row.operation_type === "login" ? "登录" : row.operation_type },
    { id: "source", header: "来源 IP", render: row => <span className="break-all font-mono text-xs">{row.remote_addr || "—"}</span> },
    { id: "until", header: "解封时间（上海）", render: row => date(row.blocked_until) },
    { id: "status", header: "状态", render: status },
    { id: "actions", header: "操作", className: "text-right", headerClassName: "text-right", render: action },
  ];
  return <Workspace admin title="登录黑名单"><AdminListPage title="登录黑名单" description="查看自动及手动登录阻断；可按用户名或 IP 添加、解除规则，自动阻断阈值与期限遵循安全设置。"
    actions={<>{manage && <Button onClick={() => { setForm(blank); setError(""); setAdding(true); }}>添加阻断</Button>}<Button variant="outline" onClick={resource.refresh}>刷新</Button></>}
    filters={<form onSubmit={search} className="flex flex-wrap items-end gap-3 rounded border border-border bg-card p-4"><label className="grid min-w-0 flex-1 gap-2 text-sm">搜索<Input value={query} onChange={e => setQuery(e.target.value)} placeholder="用户名、IP 或原因" /></label><label className="grid gap-2 text-sm">状态<select className={selectClass} value={active} onChange={e => setActive(e.target.value)}><option value="true">生效中</option><option value="false">已过期</option><option value="">全部</option></select></label><Button type="submit" variant="outline">搜索</Button></form>}
    feedback={error && !adding && <p role="alert" className="text-sm text-destructive">{error}</p>}
    dialogs={<Dialog open={adding} onOpenChange={open => { if (!busy) setAdding(open); }}><DialogContent className="max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>添加阻断</DialogTitle><DialogDescription>规则适用于匹配的登录账号或来源 IP；解封时间留空表示永久。已有相同对象将更新其阻断信息。</DialogDescription></DialogHeader><form onSubmit={add} className="space-y-4"><fieldset disabled={busy || !manage} className="grid gap-4 sm:grid-cols-2"><label className="grid gap-2 text-sm">类型<select className={selectClass} value={form.subject_type} onChange={e => setForm({ ...form, subject_type: e.target.value })}><option value="username">用户名</option><option value="ip">IP 地址</option></select></label><label className="grid gap-2 text-sm">对象<Input required maxLength={254} value={form.subject} onChange={e => setForm({ ...form, subject: e.target.value })} placeholder={form.subject_type === "ip" ? "192.0.2.1" : "管理员用户名"} /></label><label className="grid gap-2 text-sm">来源 IP<Input value={form.remote_addr} onChange={e => setForm({ ...form, remote_addr: e.target.value })} placeholder="可选" /></label><label className="grid gap-2 text-sm">解封时间<Input type="datetime-local" value={form.blocked_until} onChange={e => setForm({ ...form, blocked_until: e.target.value })} /></label><label className="grid gap-2 text-sm sm:col-span-2">原因<Input value={form.reason} maxLength={500} onChange={e => setForm({ ...form, reason: e.target.value })} placeholder="可选" /></label></fieldset>{error && <p role="alert" className="text-sm text-destructive">{error}</p>}<div className="flex justify-end gap-2 border-t pt-4"><Button type="button" variant="outline" disabled={busy} onClick={() => setAdding(false)}>取消</Button><Button type="submit" disabled={busy || !manage || !form.subject.trim()}>{busy ? "保存中…" : "确认添加"}</Button></div></form></DialogContent></Dialog>}>
    <AdminListRegion title="登录黑名单列表" allowed={allowed} resource={resource} retry={resource.refresh}
      pagination={page => <AdminPagination total={page.total} page={Math.floor(page.offset / page.limit)} pageSize={page.limit} onPageChange={index => setFilters(current => ({ ...current, offset: index * page.limit }))} onPageSizeChange={limit => setFilters(current => ({ ...current, limit, offset: 0 }))} />}>
      {page => <ResourceTable items={page.items} rowKey={row => row.id} columns={columns} ariaLabel="登录黑名单列表" className="min-w-0" tableClassName="min-w-[760px]" empty="暂无黑名单记录" renderMobile={row => <article className="space-y-3 p-4">{subject(row)}<div className="flex flex-wrap items-center gap-2">{status(row)}<span className="text-xs text-muted-foreground">{row.operation_type === "login" ? "登录" : row.operation_type}</span></div><dl className="grid gap-2 text-sm"><div><dt className="text-muted-foreground">来源 IP</dt><dd className="break-all font-mono">{row.remote_addr || "—"}</dd></div><div><dt className="text-muted-foreground">解封时间（上海）</dt><dd>{date(row.blocked_until)}</dd></div></dl>{action(row)}</article>} />}
    </AdminListRegion>
  </AdminListPage></Workspace>;
}
