"use client";

import { RefreshCw, Search } from "lucide-react";
import { useResource } from "@/hooks/use-resource";
import { useAdminListNavigation } from "@/hooks/use-admin-list-navigation";
import { Workspace } from "./workspace";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";
import { AdminPagination } from "./admin-pagination";

type DatabasePage = {
  status: string; database: string; server_version: string; database_size: number; migration_version: string;
  pool: { max_connections: number; total_connections: number; idle_connections: number; acquired_connections: number; empty_acquires: number };
  items: { name: string; estimated_rows: number; size_bytes: number }[]; total: number; limit: number; offset: number;
};
const states = ["all"] as const;
const defaults = { limit: "25" };
function bytes(value: number) {
  return value < 1024 ? `${value} B` : value < 1024 * 1024 ? `${(value / 1024).toFixed(1)} KiB` : `${(value / 1024 / 1024).toFixed(1)} MiB`;
}

export function AdminDatabaseStatusView() {
  const nav = useAdminListNavigation(true, states, "all", defaults);
  const limit = [25, 50, 100].includes(Number(nav.extra.limit)) ? Number(nav.extra.limit) : 25;
  const resource = useResource<DatabasePage>(nav.ready ? `/admin/system/database?q=${encodeURIComponent(nav.query)}&limit=${limit}&offset=${nav.page * limit}` : null, false);
  const lastPage = Math.max(0, Math.ceil((resource.data?.total ?? 0) / limit) - 1);
  if (resource.data && nav.page > lastPage) nav.setPage(lastPage);
  return <Workspace title="数据库状态" admin>
    <div className="data-surface min-w-0 space-y-5 p-4 sm:p-5">
      <div className="flex items-start justify-between gap-3"><div className="min-w-0"><h1 className="page-title">数据库状态</h1><p className="mt-2 text-sm text-muted-foreground">PostgreSQL 运行信息和应用数据表。表行数为统计估算值，表大小包含索引。</p></div><Button type="button" variant="outline" size="icon" className="shrink-0" title="刷新数据库状态" aria-label="刷新数据库状态" onClick={resource.refresh}><RefreshCw /></Button></div>
      <form className="flex flex-wrap gap-2" onSubmit={event => { event.preventDefault(); nav.setQuery(nav.draft.trim()); nav.setPage(0); }}><Input className="min-w-0 flex-1 sm:max-w-xs" aria-label="搜索数据表" placeholder="表名称" maxLength={120} value={nav.draft} onChange={event => nav.setDraft(event.target.value)} /><Button type="submit"><Search size={16} />搜索</Button>{nav.query && <Button type="button" variant="outline" onClick={() => { nav.setQuery(""); nav.setDraft(""); nav.setPage(0); }}>重置</Button>}</form>
      {resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <>
        <dl className="grid min-w-0 gap-4 border-y py-4 sm:grid-cols-2 xl:grid-cols-3">
          {[["连接状态", resource.data.status === "ready" ? "正常" : resource.data.status], ["数据库", resource.data.database], ["PostgreSQL 版本", resource.data.server_version], ["数据库大小", bytes(resource.data.database_size)], ["最新迁移", resource.data.migration_version], ["连接池（占用 / 总数 / 上限）", `${resource.data.pool.acquired_connections} / ${resource.data.pool.total_connections} / ${resource.data.pool.max_connections}`]].map(([label, value]) => <div key={label} className="min-w-0"><dt className="text-xs text-muted-foreground">{label}</dt><dd className="mt-1 break-words text-sm">{value || "未设置"}</dd></div>)}
        </dl>
        <div className="overflow-x-auto"><Table><TableHeader><TableRow><TableHead>表名称</TableHead><TableHead className="text-right">表行数（估算）</TableHead><TableHead className="text-right">表大小</TableHead></TableRow></TableHeader><TableBody>{resource.data.items.map(table => <TableRow key={table.name}><TableCell className="max-w-72 whitespace-normal break-all font-mono text-xs">{table.name}</TableCell><TableCell className="text-right">{table.estimated_rows.toLocaleString("zh-CN")}</TableCell><TableCell className="text-right">{bytes(table.size_bytes)}</TableCell></TableRow>)}{!resource.data.items.length && <TableRow><TableCell colSpan={3} className="py-12 text-center text-muted-foreground">暂无符合条件的数据表</TableCell></TableRow>}</TableBody></Table></div>
        <AdminPagination total={resource.data.total} page={nav.page} pageSize={limit} onPageChange={nav.setPage} onPageSizeChange={value => { nav.setExtra({ limit: String(value) }); nav.setPage(0); }} />
      </>}
    </div>
  </Workspace>;
}
