"use client";

import { useEffect, useState } from "react";
import { date } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Workspace, useAdminPermission } from "./workspace";
import { AdminListPage, AdminListRegion } from "./admin-list-page";
import { ResourceTable, type ResourceColumn } from "./resource-table";
import { Button } from "./ui/button";

type AuditRow = { id: number; created_at: string; action: string; actor_id: number | null; actor_name: string | null; entity_type: string; entity_id: string | number | null };
const limit = 25;
const columns: ResourceColumn<AuditRow>[] = [
  { id: "time", header: "时间", render: row => date(row.created_at) },
  { id: "action", header: "操作", render: row => <span className="block max-w-sm whitespace-pre-wrap break-all">{row.action}</span> },
  { id: "actor", header: "操作者", render: row => <div><p className="break-words">{row.actor_name || "系统 / 已删除账号"}</p>{row.actor_id != null && <p className="text-xs text-muted-foreground">#{row.actor_id}</p>}</div> },
  { id: "entity", header: "关联对象", render: row => <div className="max-w-sm space-y-1"><p className="break-all">{row.entity_type || "—"}</p><p className="break-all font-mono text-xs text-muted-foreground">{row.entity_id != null ? `#${row.entity_id}` : "—"}</p></div> },
  { id: "id", header: "日志 ID", render: row => <span className="font-mono text-xs text-muted-foreground">#{row.id}</span> },
];

export function AuditView() {
  const allowed = useAdminPermission("audit.view");
  const [page, setPage] = useState<number | null>(null);
  useEffect(() => {
    const restore = () => {
      const raw = new URLSearchParams(window.location.search).get("page") ?? "0";
      const value = Number(raw);
      setPage(/^\d+$/.test(raw) && Number.isSafeInteger(value) && Number.isSafeInteger(value * limit) ? value : 0);
    };
    restore(); window.addEventListener("popstate", restore);
    return () => window.removeEventListener("popstate", restore);
  }, []);
  const resource = useResource<AuditRow[]>(allowed && page !== null ? `/admin/audit?limit=${limit}&offset=${page * limit}` : null, false);
  function change(next: number) {
    const params = new URLSearchParams();
    if (next > 0) params.set("page", String(next));
    window.history.replaceState(null, "", `/admin/audit${params.size ? `?${params}` : ""}`);
    setPage(next);
  }
  return <Workspace admin title="操作审计"><AdminListPage title="操作审计" description="关键业务变更的只读记录；按记录倒序展示操作、操作者和关联对象。"
    actions={<Button variant="outline" disabled={resource.isLoading} onClick={resource.refresh}>刷新</Button>}>
    <AdminListRegion title="操作审计记录" allowed={allowed} resource={resource} retry={resource.refresh}
      pagination={items => <nav aria-label="审计分页" className="flex flex-wrap items-center justify-between gap-3 text-sm"><p className="text-muted-foreground">第 {(page ?? 0) + 1} 页 · 每页 {limit} 条</p><div className="flex gap-2"><Button variant="outline" disabled={!page} onClick={() => change(Math.max(0, (page ?? 0) - 1))}>上一页</Button><Button variant="outline" disabled={items.length < limit} onClick={() => change((page ?? 0) + 1)}>下一页</Button></div></nav>}>
      {items => <ResourceTable items={items} rowKey={row => row.id} columns={columns} ariaLabel="操作审计记录" className="min-w-0" empty="暂无操作审计记录"
        renderMobile={row => <article className="p-4"><dl className="grid gap-3 text-sm">{columns.map(column => <div key={column.id} className="min-w-0"><dt className="text-xs text-muted-foreground">{column.header}</dt><dd className="break-words">{column.render(row)}</dd></div>)}</dl></article>} />}
    </AdminListRegion>
  </AdminListPage></Workspace>;
}
