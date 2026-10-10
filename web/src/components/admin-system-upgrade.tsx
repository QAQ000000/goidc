"use client";

import { RefreshCw } from "lucide-react";
import { useResource } from "@/hooks/use-resource";
import { Button } from "./ui/button";
import { Failure, Loading } from "./common";
import { Workspace } from "./workspace";

type Upgrade = {
  status: "up_to_date" | "pending";
  build_version: string;
  latest_applied: string;
  latest_available: string;
  pending_migrations: string[];
  migration_count: { compiled: number; applied: number; pending: number };
};

export function AdminSystemUpgradeView() {
  const resource = useResource<Upgrade>("/admin/system/upgrade", false);
  return <Workspace title="系统升级检查" admin>
    <div className="data-surface min-w-0 space-y-5 p-4 sm:p-5">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0"><h1 className="page-title">系统升级检查</h1><p className="mt-2 text-sm text-muted-foreground">只读比较当前数据库与编译进 API 的迁移清单。迁移需由部署流程执行。</p></div>
        <Button type="button" variant="outline" size="icon" title="刷新升级状态" aria-label="刷新升级状态" onClick={resource.refresh}><RefreshCw size={16} /></Button>
      </div>
      {resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <>
        <dl className="grid min-w-0 gap-4 border-y py-4 sm:grid-cols-2 xl:grid-cols-3">
          {[['状态', resource.data.status === 'up_to_date' ? '已是最新' : '有待应用迁移'], ['构建版本', resource.data.build_version || '未设置'], ['最新已应用', resource.data.latest_applied || '无'], ['最新可用', resource.data.latest_available], ['迁移总数', resource.data.migration_count.compiled], ['已应用 / 待应用', `${resource.data.migration_count.applied} / ${resource.data.migration_count.pending}`]].map(([label, value]) => <div key={String(label)} className="min-w-0"><dt className="text-xs text-muted-foreground">{label}</dt><dd className="mt-1 break-words text-sm">{value}</dd></div>)}
        </dl>
        {resource.data.pending_migrations.length > 0 ? <div className="space-y-2"><h2 className="text-sm font-medium">待应用迁移</h2><ul className="list-disc space-y-1 pl-5 text-sm text-muted-foreground">{resource.data.pending_migrations.map(item => <li key={item} className="font-mono">{item}</li>)}</ul></div> : <p className="text-sm text-muted-foreground">数据库迁移与当前 API 清单一致。</p>}
      </>}
    </div>
  </Workspace>;
}
