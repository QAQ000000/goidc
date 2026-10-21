"use client";

import type { ReactNode } from "react";
import Link from "next/link";
import { RefreshCw } from "lucide-react";
import { ApiError } from "@/lib/api";
import { Button } from "./ui/button";
import { Skeleton } from "./ui/skeleton";
import { AdminPageContainer } from "./admin-page-container";

// These slots arrange a page; the domain retains its queries and write protocol.
export function AdminListPage({ title, description, actions, filters, feedback, children, dialogs }: {
  title: string;
  description?: string;
  actions?: ReactNode;
  filters?: ReactNode;
  feedback?: ReactNode;
  children: ReactNode;
  dialogs?: ReactNode;
}) {
  return <AdminPageContainer pageTitle={title} pageDescription={description} pageHeaderAction={actions}>
    {filters}{feedback}{children}{dialogs}
  </AdminPageContainer>;
}

export function AdminListRegion<T>({ title, summary, allowed = true, invalid, resource, retry, children, pagination }: {
  title: string;
  summary?: ReactNode;
  allowed?: boolean;
  invalid?: ReactNode;
  resource: { data: T | null; error: Error | null; isLoading: boolean };
  retry: () => void;
  children: (data: T) => ReactNode;
  pagination?: (data: T) => ReactNode;
}) {
  const { data, error, isLoading } = resource;
  const hasInvalid = invalid !== undefined && invalid !== null && invalid !== false;
  const denied = !allowed || error instanceof ApiError && error.status === 403;
  const unauthorized = error instanceof ApiError && error.status === 401;
  const usable = !denied && !hasInvalid && !error && data !== null;
  const loading = !denied && !hasInvalid && isLoading;
  return <section aria-label={title} aria-busy={loading} className="min-w-0 overflow-hidden rounded border border-border bg-card text-card-foreground">
    <header className="flex flex-wrap items-center justify-between gap-2 border-b border-border px-4 py-3 text-sm">
      <h2 className="font-medium">{title}</h2>
      <div className="flex flex-wrap items-center gap-2 text-muted-foreground">
        {usable && summary}
        {loading && data !== null && !error && <span role="status" className="text-xs">正在刷新…</span>}
      </div>
    </header>
    {denied ? <p role="status" className="p-5 text-sm text-muted-foreground">没有查看{title}的权限，请联系管理员核对授权。</p>
      : unauthorized ? <div role="alert" className="space-y-3 p-5 text-sm">
        <p>管理员登录已失效，请重新登录。</p>
        <Button asChild variant="outline"><Link href="/admin-login">管理员登录</Link></Button>
      </div>
      : hasInvalid ? <div role="alert" className="space-y-3 p-5 text-sm">
        <div className="text-destructive">{invalid}</div>
        <Button type="button" variant="outline" disabled={isLoading} onClick={retry}><RefreshCw aria-hidden="true" size={16} />{isLoading ? "正在重试…" : "重新加载"}</Button>
      </div>
      : error ? <div role="alert" className="space-y-3 p-5 text-sm">
        <p className="break-words text-destructive">{error.message}</p>
        {error instanceof ApiError && error.correlationId && error.status < 500 && <p className="break-all text-xs text-muted-foreground">错误编号：{error.correlationId}</p>}
        <Button type="button" variant="outline" disabled={isLoading} onClick={retry}><RefreshCw aria-hidden="true" size={16} />{isLoading ? "正在重试…" : "重新加载"}</Button>
      </div>
      : data === null ? <div role="status" aria-label={`正在加载${title}`} className="space-y-3 p-5"><span className="sr-only">正在加载{title}…</span>{[0, 1, 2].map(row => <Skeleton key={row} className="h-11 w-full rounded" />)}</div>
      : <>{children(data)}{pagination && <div className="border-t border-border px-4 py-3">{pagination(data)}</div>}</>}
  </section>;
}
