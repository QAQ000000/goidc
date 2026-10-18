"use client";
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { Download } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api, download } from "@/lib/api";
import { Failure, Loading } from "./common";

export function ReportExport({ path, filename, disabled }: { path: string; filename: string; disabled: boolean }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const active = useRef(false);
  const pending = useRef(false);
  useLayoutEffect(() => {
    active.current = true;
    return () => { active.current = false; };
  }, []);
  async function exportCSV() {
    if (disabled || pending.current) return;
    pending.current = true;
    setBusy(true);
    setError("");
    try {
      const blob = await download(`${path}${path.includes("?") ? "&" : "?"}format=csv`);
      // A category, period or filter change invalidates this download.
      if (!active.current) return;
      if (!blob.type.toLowerCase().includes("text/csv")) throw new Error("服务器未返回 CSV 文件，请重新加载后重试");
      const href = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = href;
      link.download = filename;
      document.body.appendChild(link);
      link.click();
      link.remove();
      window.setTimeout(() => URL.revokeObjectURL(href), 1000);
    } catch (cause) {
      if (active.current) setError(cause instanceof Error ? cause.message : "导出失败，请重试");
    } finally {
      pending.current = false;
      if (active.current) setBusy(false);
    }
  }
  return <div className="flex flex-col items-end gap-2">
    <Button variant="outline" disabled={disabled || busy} onClick={() => void exportCSV()}><Download size={15} />{busy ? "导出中…" : error ? "重试导出 CSV" : "导出当前报表 CSV"}</Button>
    {error && <p role="alert" className="max-w-sm text-sm text-destructive">导出失败：{error}</p>}
  </div>;
}

export function ReportPanel<T>({ path, filename, title, description, exportDisabled, children }: {
  path: string; filename: string; title: string; description: string; exportDisabled: boolean; children: (data: T) => ReactNode;
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
  const disabled = exportDisabled || !data || !!error;
  return <section aria-label={title} className="data-surface p-4">
    <div className="flex flex-wrap justify-between items-start gap-3 mb-5">
      <div><h2 className="font-semibold">{title}</h2><p className="text-xs text-muted-foreground mt-1">{description}</p></div>
      <ReportExport key={`${path}:${disabled}`} path={path} filename={filename} disabled={disabled} />
    </div>
    {exportDisabled && <p className="mb-4 text-sm text-muted-foreground">筛选条件已更改，请先查询后导出。</p>}
    {error ? <Failure error={error} retry={retry} /> : !data ? <Loading /> : children(data)}
  </section>;
}
