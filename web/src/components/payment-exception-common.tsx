"use client";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { Button } from "./ui/button";
import type { DurableRequest } from "@/hooks/use-durable-admin-mutation";

export function useExceptionQuery() {
  const params = useSearchParams();
  const pathname = usePathname();
  const router = useRouter();
  function offset(key: string) {
    const raw = params.get(key) ?? "0";
    const number = /^\d+$/.test(raw) ? Number(raw) : 0;
    return Number.isSafeInteger(number) && number <= 2147483640 ? Math.floor(number / 10) * 10 : 0;
  }
  function update(values: Record<string, string | number | null>) {
    const next = new URLSearchParams(params.toString());
    Object.entries(values).forEach(([key, value]) => { if (value === null || value === 0 || value === "") next.delete(key); else next.set(key, String(value)); });
    router.replace(`${pathname}${next.size ? `?${next}` : ""}`, { scroll: false });
  }
  return { params, offset, update };
}
export function ExceptionPager({ total, offset, count, busy, change }: { total: number; offset: number; count: number; busy?: boolean; change: (offset: number) => void }) {
  return <div className="flex flex-wrap items-center justify-between gap-3 text-sm"><span>共 {total} 条 · 第 {Math.floor(offset / 10) + 1} 页</span><div className="flex flex-wrap gap-2"><Button size="sm" variant="outline" disabled={busy || offset === 0} onClick={() => change(Math.max(0, offset - 10))}>上一页</Button><Button size="sm" variant="outline" disabled={busy || offset + count >= total} onClick={() => change(offset + 10)}>下一页</Button></div></div>;
}
export function DurableRecovery({ mutation }: { mutation: { pending: DurableRequest | null; ready: boolean; busy: boolean; storageError: string; error: string; reviewed: boolean; latestFacts: string; execute: () => Promise<void>; readLatest: () => Promise<void>; endRejected: () => Promise<void> } }) {
  return <div className="space-y-2 text-sm min-w-0 break-words">
    {!mutation.ready && !mutation.storageError && <p role="status">正在检查本员工原请求，核实前不可新操作。</p>}
    {mutation.storageError && <p role="alert" className="text-destructive">{mutation.storageError}</p>}
    {mutation.pending && <div role="status" className="rounded border p-3 space-y-2"><p>原请求仍需核实。原版本、完整载荷和键均已保留；刷新不会自动提交，关闭弹窗不会清理原请求。</p><div className="flex flex-wrap gap-2"><Button size="sm" disabled={mutation.busy || !!mutation.storageError} onClick={() => void mutation.execute()}>以原请求核实结果</Button><Button size="sm" variant="outline" disabled={mutation.busy} onClick={() => void mutation.readLatest()}>读取最新案件事实</Button>{mutation.pending.rejected && mutation.reviewed && <Button size="sm" variant="outline" disabled={mutation.busy} onClick={() => void mutation.endRejected()}>已核对，结束被拒绝请求</Button>}</div>{mutation.latestFacts && <p>{mutation.latestFacts}</p>}{mutation.pending.rejected && !mutation.reviewed && <p>已收到确定拒绝，请先显式读取最新事实；不会自动换键、版本或重建申请。</p>}</div>}
    {mutation.error && <p role="alert" className="text-destructive">{mutation.error}</p>}
  </div>;
}
export const positiveInteger = (value: unknown): value is number => Number.isSafeInteger(value) && Number(value) > 0;
export function validNote(value: unknown) { return typeof value === "string" && value.trim().length > 0 && Array.from(value).length <= 1000; }
export function exactFields(body: Record<string, unknown>, fields: string[]) { return Object.keys(body).length === fields.length && fields.every(field => Object.hasOwn(body, field)); }
