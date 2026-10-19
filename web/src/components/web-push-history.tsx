"use client";

import { useState } from "react";
import { RefreshCw } from "lucide-react";
import { date } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import type { WebPushHistoryPage } from "@/lib/web-push";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";

export function WebPushHistory() {
  const [offset, setOffset] = useState(0);
  const resource = useResource<WebPushHistoryPage>(`/me/notification-web-push/history?limit=20&offset=${offset}`, false);
  const data = resource.data;
  return <section className="mt-6 space-y-3 border-t pt-5" aria-label="浏览器通知记录">
    <div className="flex flex-wrap items-center justify-between gap-3"><h3 className="text-sm font-medium">浏览器通知记录</h3><Button type="button" size="sm" variant="outline" onClick={resource.refresh}><RefreshCw className="size-4" />刷新通知记录</Button></div>
    <p className="text-sm text-muted-foreground">这里是已尝试推送的业务事件，同一事件在多个浏览器推送时合并显示。平台接受不代表已显示或已读；结果未知的尝试也可能出现在记录中。</p>
    {resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !data ? <Loading /> : !data.items.length ? <p className="py-3 text-sm text-muted-foreground">暂无浏览器通知记录。</p> : <div className="divide-y">{data.items.map(item => <article key={item.id} className="space-y-2 py-3">
      <div className="flex flex-wrap items-start justify-between gap-2"><h4 className="min-w-0 break-words text-sm font-medium">{item.name || item.event_kind}</h4><time dateTime={item.created_at} className="text-xs text-muted-foreground">{date(item.created_at)}</time></div>
      <dl className="space-y-1 text-sm">{item.variables.filter(variable => variable.value.trim() !== "").map(variable => <div key={variable.key} className="grid min-w-0 gap-1 sm:grid-cols-[minmax(5rem,auto)_1fr]"><dt className="break-words text-muted-foreground">{variable.label || variable.key}</dt><dd className="min-w-0 whitespace-pre-wrap break-words">{variable.value}</dd></div>)}</dl>
    </article>)}</div>}
    <div className="flex flex-wrap items-center justify-between gap-3"><Button type="button" size="sm" variant="outline" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - 20))}>上一页通知记录</Button><span className="text-xs text-muted-foreground">第 {offset / 20 + 1} 页 · 每页 20 条</span><Button type="button" size="sm" variant="outline" disabled={!!resource.error || !data?.has_more} onClick={() => setOffset(offset + 20)}>下一页通知记录</Button></div>
  </section>;
}
