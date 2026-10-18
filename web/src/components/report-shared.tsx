"use client";
import { useEffect, useState, type ReactNode } from "react";
import { api } from "@/lib/api";
import { Failure, Loading } from "./common";

export function ReportPanel<T>({ path, title, description, filtersChanged, children }: {
  path: string; title: string; description: string; filtersChanged?: boolean; children: (data: T) => ReactNode;
}) {
  const [state, setState] = useState<{ path: string; data: T | null; error: Error | null }>({ path, data: null, error: null });
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    api<T>(path, { signal: controller.signal }).then(data => {
      if (!controller.signal.aborted) setState({ path, data, error: null });
    }).catch(cause => {
      if (!controller.signal.aborted) setState({ path, data: null, error: cause instanceof Error ? cause : new Error("报表加载失败") });
    });
    return () => controller.abort();
  }, [path, attempt]);
  const retry = () => { setState({ path, data: null, error: null }); setAttempt(value => value + 1); };
  const data = state.path === path ? state.data : null;
  const error = state.path === path ? state.error : null;
  return <section aria-label={title} className="data-surface p-4">
    <div className="mb-5">
      <h2 className="font-semibold">{title}</h2><p className="text-xs text-muted-foreground mt-1">{description}</p>
    </div>
    {filtersChanged && <p className="mb-4 text-sm text-muted-foreground">筛选条件已更改，请查询。</p>}
    {error ? <Failure error={error} retry={retry} /> : !data ? <Loading /> : children(data)}
  </section>;
}
