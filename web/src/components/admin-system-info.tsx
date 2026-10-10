"use client";

import { RefreshCw } from "lucide-react";
import { useResource } from "@/hooks/use-resource";
import { Workspace } from "./workspace";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";

type SystemInfo = { status?: string; runtime?: string; go_version: string; go_os?: string; go_arch?: string; goroutines?: number; database?: string; database_version?: string; server_version?: string; database_size?: number; migration_version?: string; build_version?: string; product?: string; backend?: string; frontend?: string; runtime_contract?: string; pool?: { max_connections: number; total_connections: number; idle_connections: number; acquired_connections: number; empty_acquires: number } };

function InfoRow({ label, value }: { label: string; value: unknown }) {
  return <div className="flex flex-wrap justify-between gap-4 border-b py-3 text-sm last:border-b-0"><dt className="text-muted-foreground">{label}</dt><dd className="break-all text-right font-medium">{value === "" || value == null ? "未设置" : String(value)}</dd></div>;
}

export function AdminSystemInfoView({ mode }: { mode: "runtime" | "about" | "database" }) {
  const resource = useResource<SystemInfo>(`/admin/system/${mode}`, false);
  const title = mode === "runtime" ? "运行环境" : mode === "database" ? "数据库状态" : "关于系统";
  return <Workspace title={title} admin><div className="space-y-5"><div className="flex items-start justify-between gap-3"><div><h1 className="page-title">{title}</h1><p className="mt-2 text-sm text-muted-foreground">{mode === "runtime" ? "查看 Go API 与 PostgreSQL 的真实运行信息。" : mode === "database" ? "查看数据库连接和迁移状态。" : "查看当前系统的技术栈和构建契约。"}</p></div><Button type="button" variant="outline" size="icon" title="刷新信息" aria-label="刷新信息" onClick={resource.refresh}><RefreshCw /></Button></div>{resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <dl className="max-w-3xl divide-y border-y">{mode === "runtime" ? <><InfoRow label="后端运行时" value={resource.data.runtime} /><InfoRow label="Go 版本" value={resource.data.go_version} /><InfoRow label="系统 / 架构" value={`${resource.data.go_os} / ${resource.data.go_arch}`} /><InfoRow label="当前协程数" value={resource.data.goroutines} /><InfoRow label="数据库" value={resource.data.database} /><InfoRow label="数据库版本" value={resource.data.database_version} /><InfoRow label="数据库大小（字节）" value={resource.data.database_size} /><InfoRow label="最新迁移" value={resource.data.migration_version} /><InfoRow label="构建版本" value={resource.data.build_version} /></> : mode === "database" ? <><InfoRow label="数据库状态" value={resource.data.status === "ready" ? "正常" : resource.data.status} /><InfoRow label="数据库名称" value={resource.data.database} /><InfoRow label="PostgreSQL 版本" value={resource.data.server_version} /><InfoRow label="数据库大小（字节）" value={resource.data.database_size} /><InfoRow label="最新迁移" value={resource.data.migration_version} /><InfoRow label="连接池上限" value={resource.data.pool?.max_connections} /><InfoRow label="当前连接" value={resource.data.pool?.total_connections} /><InfoRow label="空闲连接" value={resource.data.pool?.idle_connections} /><InfoRow label="占用连接" value={resource.data.pool?.acquired_connections} /><InfoRow label="等待连接次数" value={resource.data.pool?.empty_acquires} /></> : <><InfoRow label="产品" value={resource.data.product} /><InfoRow label="后端" value={resource.data.backend} /><InfoRow label="前端" value={resource.data.frontend} /><InfoRow label="数据库" value={resource.data.database} /><InfoRow label="Go 版本" value={resource.data.go_version} /><InfoRow label="构建版本" value={resource.data.build_version} /><InfoRow label="运行契约" value={resource.data.runtime_contract} /></>}</dl>}</div></Workspace>;
}
