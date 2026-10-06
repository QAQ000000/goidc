"use client";

import { Plus, Trash2 } from "lucide-react";
import type { ProductOptionPriceTier } from "@/lib/api";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";

export const tierCycles: Record<string, string> = {
  hourly: "小时", daily: "日", monthly: "月", quarterly: "季", semiannually: "半年",
  annually: "年", biennially: "两年", triennially: "三年", once: "一次性",
};
type TierDraft = { minimum: string; maximum: string; price: string; setup: string };
export type TierDrafts = Record<string, TierDraft[]>;

export function makeTierDrafts(tiers: ProductOptionPriceTier[] = []): TierDrafts {
  return Object.fromEntries(Object.keys(tierCycles).map(cycle => [cycle, tiers.filter(tier => tier.billing_cycle === cycle).map(tier => ({
    minimum: String(tier.qty_minimum), maximum: String(tier.qty_maximum),
    price: (tier.price_cents / 100).toFixed(2), setup: (tier.setup_fee_cents / 100).toFixed(2),
  }))]));
}

function quantity(input: string) {
  if (!/^\d{1,7}$/.test(input) || Number(input) < 1 || Number(input) > 1000000) {
    throw new Error("数量必须是 1 至 1000000 的整数");
  }
  return Number(input);
}
function amount(input: string) {
  const match = input.match(/^(\d{1,7})(?:\.(\d{1,2}))?$/);
  if (!match) throw new Error("金额必须为非负数，最多两位小数");
  const cents = Number(match[1]) * 100 + Number((match[2] ?? "").padEnd(2, "0"));
  if (cents > 100000000) throw new Error("金额不能超过 100 万元");
  return cents;
}

export function parseTierChanges(drafts: TierDrafts, changedCycles: string[]) {
  return Object.fromEntries(changedCycles.map(cycle => [cycle, drafts[cycle].map((tier, index) => {
    try {
      return { qty_minimum: quantity(tier.minimum), qty_maximum: quantity(tier.maximum), price_cents: amount(tier.price), setup_fee_cents: amount(tier.setup) };
    } catch (error) {
      throw new Error(`${tierCycles[cycle]}账期，第 ${index + 1} 段：${(error as Error).message}`);
    }
  })]));
}

export function QuantityTierEditor({ drafts, cycle, changedCycles, manage, busy, onChange, onCycleChange }: {
  drafts: TierDrafts; cycle: string; changedCycles: string[]; manage: boolean; busy: boolean;
  onChange: (drafts: TierDrafts) => void; onCycleChange: (cycle: string) => void;
}) {
  const tiers = drafts[cycle];
  function update(index: number, patch: Partial<TierDraft>) {
    onChange({ ...drafts, [cycle]: tiers.map((tier, i) => i === index ? { ...tier, ...patch } : tier) });
  }
  function add() {
    const minimum = tiers.length ? Math.min(1000000, Math.max(...tiers.map(tier => Number(tier.maximum) || 0)) + 1) : 1;
    onChange({ ...drafts, [cycle]: [...tiers, { minimum: String(minimum), maximum: String(minimum), price: "0.00", setup: "0.00" }] });
  }
  return <section className="border-t pt-5 space-y-4">
    <div className="flex flex-wrap items-end justify-between gap-3">
      <div><h3 className="font-medium">数量阶梯价格</h3><p className="text-xs text-muted-foreground mt-1">区间不可重叠；未覆盖的数量不可购买。</p></div>
      {manage && <Button type="button" variant="outline" size="sm" disabled={busy || tiers.length >= 100} onClick={add}><Plus />新增区间</Button>}
    </div>
    <label className="grid gap-2 text-sm">账期
      <Select value={cycle} disabled={busy} onValueChange={onCycleChange}>
        <SelectTrigger aria-label="阶梯价格账期" className="w-full"><SelectValue /></SelectTrigger>
        <SelectContent>{Object.entries(tierCycles).map(([value, label]) => <SelectItem key={value} value={value}>{label}{changedCycles.includes(value) ? "（未保存）" : ""}</SelectItem>)}</SelectContent>
      </Select>
    </label>
    {tiers.length === 0 && <p className="text-sm text-muted-foreground py-3">此账期暂无阶梯。</p>}
    <fieldset disabled={!manage || busy} className="space-y-4">
      {tiers.map((tier, index) => <div key={index} className="grid grid-cols-2 gap-3 items-end">
        <label className="grid gap-2 text-xs">起始数量<Input aria-label={`阶梯 ${index + 1} 起始数量`} type="number" min={1} max={1000000} step={1} value={tier.minimum} onChange={event => update(index, { minimum: event.target.value })} /></label>
        <label className="grid gap-2 text-xs">结束数量<Input aria-label={`阶梯 ${index + 1} 结束数量`} type="number" min={1} max={1000000} step={1} value={tier.maximum} onChange={event => update(index, { maximum: event.target.value })} /></label>
        <label className="grid gap-2 text-xs">单价（元）<Input aria-label={`阶梯 ${index + 1} 单价`} inputMode="decimal" value={tier.price} onChange={event => update(index, { price: event.target.value })} /></label>
        <label className="grid gap-2 text-xs">初装费（元）<Input aria-label={`阶梯 ${index + 1} 初装费`} inputMode="decimal" value={tier.setup} onChange={event => update(index, { setup: event.target.value })} /></label>
        {manage && <div className="col-span-2 flex justify-end"><Button type="button" variant="ghost" size="icon" title={`删除阶梯 ${index + 1}`} aria-label={`删除阶梯 ${index + 1}`} onClick={() => onChange({ ...drafts, [cycle]: tiers.filter((_, i) => i !== index) })}><Trash2 /></Button></div>}
      </div>)}
    </fieldset>
    {changedCycles.length > 0 && <p role="status" className="text-xs text-muted-foreground">待保存账期：{changedCycles.map(value => tierCycles[value]).join("、")}</p>}
  </section>;
}
