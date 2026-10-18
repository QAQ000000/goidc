"use client";
import { useState } from "react";
import { api, ApiError, type TicketRating } from "@/lib/api";
import { usePendingSetting } from "@/hooks/use-pending-setting";
import { Button } from "./ui/button";

export function TicketMessageRating({ ticketID, replyID, rating, actorID, refresh }: { ticketID: number; replyID?: number; rating?: TicketRating; actorID: number | null; refresh: () => void }) {
  const target = replyID ? "reply" : "initial";
  const pending = usePendingSetting(`ticket-rating:${ticketID}:${replyID ?? "initial"}`, actorID);
  const [stars, setStars] = useState(5);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  let displayedStars = stars;
  if (pending.unknown && pending.pending.current) {
    try {
      const original = JSON.parse(pending.pending.current.body);
      if (Number.isInteger(original.stars) && original.stars >= 1 && original.stars <= 5) displayedStars = original.stars;
    } catch { /* Keep the original request available even if stored display metadata is invalid. */ }
  }
  if (!rating) return null;
  if (rating.stars !== null) return <p className="mt-4 text-sm text-muted-foreground">客户评分：{rating.stars} 星{rating.provenance === "legacy" ? "（历史评分）" : ""}</p>;
  if (!rating.can_rate && !pending.unknown) return null;
  async function submit() {
    if (busy || !pending.ready) return;
    setBusy(true); setError("");
    if (!pending.pending.current) pending.remember({ key: crypto.randomUUID(), body: JSON.stringify({ target, reply_id: replyID ?? 0, stars }) });
    const original = pending.pending.current!;
    try {
      await api(`/tickets/${ticketID}/ratings`, { method: "POST", headers: { "Idempotency-Key": original.key }, body: original.body });
      pending.resolve(); refresh();
    } catch (cause) {
      const unknown = !(cause instanceof ApiError) || cause.status >= 500;
      pending.setUnknown(unknown);
      if (!unknown) pending.resolve();
      setError(unknown ? "评分结果未知，请重试原评分以确认，原星级和请求标识已保留。" : (cause as Error).message);
      refresh();
    } finally { setBusy(false); }
  }
  return <div className="mt-4 border-t pt-3">
    <p className="text-xs text-muted-foreground mb-2">为此公开管理员内容评分（仅一次，提交后不可修改）</p>
    <div className="flex flex-wrap items-center gap-3"><label className="text-sm">星级 <select aria-label={replyID ? `回复 ${replyID} 评分星级` : "初始内容评分星级"} className="rounded-md border bg-background p-2" disabled={busy || pending.unknown} value={displayedStars} onChange={event => setStars(Number(event.target.value))}>{[1,2,3,4,5].map(star => <option key={star} value={star}>{star} 星</option>)}</select></label><Button size="sm" variant="outline" disabled={busy || !pending.ready} onClick={() => void submit()}>{busy ? "提交中…" : pending.unknown ? "重试原评分" : "提交评分"}</Button></div>
    {error && <p role="alert" className="mt-2 text-sm text-destructive">{error}</p>}
    {pending.unknown && !error && <p role="status" className="mt-2 text-sm">已恢复待确认评分。重试使用原星级和请求标识。</p>}
  </div>;
}
