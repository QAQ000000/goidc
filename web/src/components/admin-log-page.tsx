"use client";

import { useEffect, useState } from "react";
import { useResource } from "@/hooks/use-resource";
import { date } from "@/lib/api";
import { RefreshCw } from "lucide-react";
import { Workspace, useAdminPermission } from "./workspace";
import { AdminPagination } from "./admin-pagination";
import { Input } from "./ui/input";
import { AdminListPage, AdminListRegion } from "./admin-list-page";
import { ResourceTable, type ResourceColumn } from "./resource-table";
import { Button } from "./ui/button";

type AuditRow = { id: number; created_at: string; action: string; entity_type: string; entity_id: string; actor_name: string | null };
type Page = { items: AuditRow[]; total: number; limit: number; offset: number };
export function AdminLogPage() {
  const allowed = useAdminPermission("audit.view");
  const [params, setParams] = useState<URLSearchParams | null>(null);
  useEffect(() => {
    const restore = () => setParams(new URLSearchParams(window.location.search));
    restore();
    window.addEventListener("popstate", restore);
    return () => window.removeEventListener("popstate", restore);
  }, []);
  const rawLimit = Number(params?.get("limit"));
  const limit = [25, 50, 100].includes(rawLimit) ? rawLimit : 25;
  const rawOffset = Number(params?.get("offset"));
  const offset = Number.isSafeInteger(rawOffset) && rawOffset >= 0 ? rawOffset : 0;
  const page = Math.floor(offset / limit);
  const q = params?.get("q") ?? "";
  const query = new URLSearchParams({ kind: "system", q, limit: String(limit), offset: String(page * limit) });
  const resource = useResource<Page>(allowed && params ? `/admin/logs?${query}` : null, false);
  function change(key: string, value: string) {
    const next = new URLSearchParams(params ?? undefined);
    if (value) next.set(key, value); else next.delete(key);
    if (key !== "offset") next.delete("offset");
    window.history.replaceState(null, "", `/admin/logs/system${next.size ? `?${next}` : ""}`);
    setParams(next);
  }
  const columns: ResourceColumn<AuditRow>[] = [
    { id: "id", header: "ID", render: row => `#${row.id}` },
    { id: "time", header: "时间", render: row => date(row.created_at) },
    { id: "actor", header: "操作人", render: row => row.actor_name || "系统 / 已删除用户" },
    { id: "action", header: "操作", render: row => row.action, className: "max-w-80 whitespace-normal break-all" },
    { id: "entity", header: "对象类型", render: row => row.entity_type },
    { id: "entity_id", header: "对象 ID", render: row => row.entity_id || "—", className: "max-w-64 whitespace-normal break-all" },
  ];
  return <Workspace admin title="系统审计日志"><AdminListPage title="系统审计日志" description="查看系统审计元数据，操作记录只读。"
    actions={<Button variant="outline" disabled={resource.isLoading} onClick={resource.refresh}><RefreshCw />刷新</Button>}
    filters={allowed && <div className="rounded border border-border bg-card p-4"><label className="block max-w-xl text-sm">关键词<Input aria-label="日志关键词" placeholder="搜索操作、对象类型、对象 ID 或操作人" value={q} onChange={e => change("q", e.target.value)} /></label></div>}>
    <AdminListRegion title="系统审计日志列表" allowed={allowed} resource={resource} retry={resource.refresh} summary={resource.data && `共 ${resource.data.total} 条`}
      pagination={data => <AdminPagination total={data.total} page={page} pageSize={limit} onPageChange={n => change("offset", String(n * limit))} onPageSizeChange={n => change("limit", String(n))} />}>
      {data => <ResourceTable items={data.items} rowKey={row => row.id} columns={columns} ariaLabel="系统审计日志列表" className="min-w-0" empty="暂无匹配的审计日志"
        renderMobile={row => <article className="p-4"><dl className="grid gap-3 text-sm">{columns.map(column => <div key={column.id} className="min-w-0"><dt className="text-xs text-muted-foreground">{column.header}</dt><dd className="break-all">{column.render(row)}</dd></div>)}</dl></article>} />}
    </AdminListRegion>
  </AdminListPage></Workspace>;
}
