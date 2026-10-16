"use client";

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { api, ApiError, money, type User } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Failure, Loading } from "./common";

type Options = {
  enabled: boolean;
  min_topup_cents: number;
  max_topup_cents: number;
  max_balance_cents: number;
  balance_cents: number;
  reserved_cents: number;
  available_capacity_cents: number | null;
  require_active_order: boolean;
  has_active_order: boolean;
  version: number;
};
type Request = { key: string; body: string };

export function CustomerTopup() {
  const router = useRouter();
  const options = useResource<Options>("/balance/topup-options", false);
  const me = useResource<User>("/me", false);
  const [restoredKey, setRestoredKey] = useState("");
  const storageKey = me.data ? `customer-topup:${me.data.id}` : "";
  const [amount, setAmount] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [unknown, setUnknown] = useState(false);
  const request = useRef<Request | null>(null);
  const lock = useRef(false);
  useEffect(() => {
    request.current = null;
    let active = true;
    void Promise.resolve().then(() => {
    if (!active) return;
    setUnknown(false);
    setRestoredKey("");
    setAmount("");
    setError("");
    if (!storageKey) return;
    try {
      const saved = sessionStorage.getItem(storageKey);
      if (saved) {
        const parsed = JSON.parse(saved) as Request;
        const body = JSON.parse(parsed.body) as { amount_cents: number };
        if (typeof parsed.key === "string" && Number.isSafeInteger(body.amount_cents) && body.amount_cents > 0) {
          request.current = parsed;
          setAmount((body.amount_cents / 100).toFixed(2));
          setUnknown(true);
        }
      }
    } catch { /* A malformed saved request cannot be replayed. */ }
    setRestoredKey(storageKey);
    });
    return () => { active = false; };
  }, [storageKey]);
  const value = options.data;
  const [whole, fraction = ""] = amount.split(".");
  const cents = /^\d{1,12}(\.\d{1,2})?$/.test(amount) ? Number(whole) * 100 + Number(fraction.padEnd(2, "0")) : 0;
  const validation = !value ? "" : !value.enabled ? "当前未开放在线充值" : value.require_active_order && !value.has_active_order ? "需先拥有已付款或已启用的订单才能充值" : !Number.isSafeInteger(cents) || cents <= 0 ? "请输入大于零的金额，最多两位小数" : cents < value.min_topup_cents ? `最低充值 ${money(value.min_topup_cents)}` : value.max_topup_cents > 0 && cents > value.max_topup_cents ? `单笔最多充值 ${money(value.max_topup_cents)}` : value.available_capacity_cents !== null && cents > value.available_capacity_cents ? "充值金额超过当前可用充值额度" : "";

  async function submit() {
    if (lock.current || restoredKey !== storageKey || !storageKey || !value || (!unknown && validation)) return;
    lock.current = true;
    setBusy(true);
    setError("");
    request.current ??= { key: `web-topup-${crypto.randomUUID()}`, body: JSON.stringify({ amount_cents: cents, expected_version: value.version }) };
    try {
      sessionStorage.setItem(storageKey, JSON.stringify(request.current));
      const result = await api<{ invoice_id: number }>("/balance/topups", { method: "POST", body: request.current.body, headers: { "Idempotency-Key": request.current.key } });
      sessionStorage.removeItem(storageKey);
      router.push(`/console/invoices/${result.invoice_id}?from=topup`);
    } catch (e) {
      const uncertain = !(e instanceof ApiError) || e.status >= 500;
      setUnknown(uncertain);
      if (!uncertain) { request.current = null; sessionStorage.removeItem(storageKey); }
      if (e instanceof ApiError && e.status === 409) {
        options.refresh();
        setError(`${e.message}。已重新读取充值规则，请保留当前金额并核对后重试。`);
      } else setError(uncertain ? "创建结果暂未确认，请以原请求重试确认，避免重复创建充值账单。" : (e as Error).message);
    } finally {
      lock.current = false;
      setBusy(false);
    }
  }
  return <section className="data-surface p-4 sm:p-6 mb-6 space-y-4" aria-labelledby="online-topup-title">
    <div className="flex flex-wrap items-center justify-between gap-3"><h2 id="online-topup-title" className="font-semibold">在线充值</h2><Button size="sm" variant="ghost" disabled={busy} onClick={options.refresh}>刷新充值规则</Button></div>
    {options.error ? <Failure error={options.error} retry={options.refresh} /> : !value ? <Loading /> : <>
      <dl className="grid gap-3 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">单笔范围</dt><dd>{money(value.min_topup_cents)} 起 · {value.max_topup_cents > 0 ? `最高 ${money(value.max_topup_cents)}` : "无单笔上限"}</dd></div><div><dt className="text-muted-foreground">可用充值额度</dt><dd>{value.available_capacity_cents === null ? "不限" : money(value.available_capacity_cents)}（已预留 {money(value.reserved_cents)}）</dd></div></dl>
      <form className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_auto]" onSubmit={e => { e.preventDefault(); void submit(); }}>
        <label className="grid gap-2 text-sm">充值金额（元）<Input inputMode="decimal" autoComplete="off" placeholder="请输入充值金额" value={amount} maxLength={15} disabled={busy || unknown} onChange={e => { setAmount(e.target.value); setError(""); request.current = null; }} /></label>
        <Button className="sm:self-end" type="submit" disabled={busy || restoredKey !== storageKey || !storageKey || !!options.error || (!unknown && !!validation)}>{busy ? "正在确认" : unknown ? "以原请求确认创建结果" : "创建充值账单"}</Button>
      </form>
      {validation && !unknown && <p className="text-sm text-muted-foreground">{validation}</p>}
      <p className="text-xs text-muted-foreground">创建账单后选择在线渠道付款，到账后计入账户余额。已接纳的付款须核实原支付结果，请勿重复付款。</p>
    </>}
    {me.error && <Failure error={me.error} retry={me.refresh} />}
    {error && <p role="alert" className="text-sm text-destructive break-words">{error}</p>}
  </section>;
}
