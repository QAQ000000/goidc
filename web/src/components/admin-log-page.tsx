"use client";
import { useState } from "react";
import { api } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { AdminPagination } from "./admin-pagination";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Failure, Loading } from "./common";

type Page = { items: Record<string, unknown>[]; total: number; limit: number; offset: number };
const kinds = ["system", "api", "login", "cron", "message", "sms", "email"] as const;
export function AdminLogPage({ kind }: { kind: string }) {
  const selected = kinds.includes(kind as never) ? kind : "system";
  const [page, setPage] = useState(0); const [limit, setLimit] = useState(25); const [q, setQ] = useState("");
  const resource = useResource<Page>(`/admin/logs?kind=${selected}&q=${encodeURIComponent(q)}&limit=${limit}&offset=${page * limit}`, false);
  const [busy, setBusy] = useState(false); const [message, setMessage] = useState("");
  async function cleanup() {
    const before = window.prompt("请输入清理截止时间（ISO 时间）"); if (!before || !window.confirm(`确认删除 ${before} 之前的${selected}日志？此操作不可撤销。`)) return;
    setBusy(true); setMessage(""); try { const result = await api<{ deleted: number }>("/admin/logs/cleanup", { method: "POST", headers: { "Idempotency-Key": `admin-log-cleanup-${selected}-${crypto.randomUUID()}` }, body: JSON.stringify({ log_type: selected === "system" ? "audit" : selected === "cron" ? "scheduler" : selected, before, acknowledged: true }) }); setMessage(`已清理 ${result.deleted} 条`); resource.refresh(); } catch (e) { setMessage((e as Error).message); } finally { setBusy(false); }
  }
  return <section className="space-y-4"><div className="flex flex-wrap gap-3"><Input aria-label="日志关键词" placeholder="搜索日志" value={q} onChange={e => { setQ(e.target.value); setPage(0); }} /><Button variant="outline" disabled={busy} onClick={() => void cleanup()}>清理日志</Button></div>{message && <p role="status" className="text-sm">{message}</p>}{resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <><div className="overflow-x-auto border"><table className="w-full text-sm"><thead><tr className="border-b text-left"><th className="p-2">ID</th><th className="p-2">时间</th><th className="p-2">类型</th><th className="p-2">详情</th></tr></thead><tbody>{resource.data.items.map(item => <tr className="border-b" key={String(item.id)}><td className="p-2">{String(item.id)}</td><td className="p-2">{String(item.created_at ?? "")}</td><td className="p-2">{String(item.action ?? item.kind ?? "")}</td><td className="max-w-xl whitespace-pre-wrap break-all p-2">{JSON.stringify(item)}</td></tr>)}{!resource.data.items.length && <tr><td colSpan={4} className="p-8 text-center">暂无数据</td></tr>}</tbody></table></div><AdminPagination total={resource.data.total} page={page} pageSize={limit} onPageChange={setPage} onPageSizeChange={n => { setLimit(n); setPage(0); }} /></>}</section>;
}
