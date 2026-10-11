"use client";

import { useEffect, useState } from "react";
import { useResource } from "@/hooks/use-resource";
import { date, type User } from "@/lib/api";
import { can } from "@/lib/admin-navigation";
import { Workspace } from "./workspace";
import { AdminPagination } from "./admin-pagination";
import { Input } from "./ui/input";
import { Failure, Loading } from "./common";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";

type AuditRow = { id: number; created_at: string; action: string; entity_type: string; entity_id: string; actor_name: string | null };
type Page = { items: AuditRow[]; total: number; limit: number; offset: number };
export function AdminLogPage() {
  const user = useResource<User>("/me");
  const allowed = can(user.data, "audit.view");
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
  return <Workspace admin title="系统审计日志">
    <div className="min-w-0 space-y-4">
      <h1 className="page-title">系统审计日志</h1>
      {user.error ? <Failure error={user.error} retry={user.refresh} /> : !user.data ? <Loading /> : !allowed ? <p role="alert">无审计查看权限</p> : <>
        <label className="block max-w-xl text-sm">关键词<Input aria-label="日志关键词" placeholder="搜索操作、对象类型、对象 ID 或操作人" value={q} onChange={e => change("q", e.target.value)} /></label>
        {resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <>
          <div className="overflow-x-auto rounded border"><Table><TableHeader><TableRow>{["ID", "时间", "操作人", "操作", "对象类型", "对象 ID"].map(label => <TableHead key={label}>{label}</TableHead>)}</TableRow></TableHeader><TableBody>
            {resource.data.items.map(item => <TableRow key={item.id}><TableCell>{item.id}</TableCell><TableCell className="whitespace-nowrap">{date(item.created_at)}</TableCell><TableCell>{item.actor_name || "系统 / 已删除用户"}</TableCell><TableCell>{item.action}</TableCell><TableCell>{item.entity_type}</TableCell><TableCell>{item.entity_id || "—"}</TableCell></TableRow>)}
            {!resource.data.items.length && <TableRow><TableCell colSpan={6} className="py-8 text-center">暂无匹配的审计日志</TableCell></TableRow>}
          </TableBody></Table></div>
          <AdminPagination total={resource.data.total} page={page} pageSize={limit} onPageChange={n => change("offset", String(n * limit))} onPageSizeChange={n => change("limit", String(n))} />
        </>}
      </>}
    </div>
  </Workspace>;
}
