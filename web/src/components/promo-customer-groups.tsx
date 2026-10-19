"use client";

import { useState } from "react";
import { useResource } from "@/hooks/use-resource";
import { Loading, Failure } from "./common";
import { Button } from "./ui/button";
import { Input } from "./ui/input";

type GroupPage = { items: { id: number; name: string }[]; total: number; limit: number; offset: number };

export function PromoCustomerGroups({ value, onChange, disabled = false }: { value: number[]; onChange: (ids: number[]) => void; disabled?: boolean }) {
  const [query, setQuery] = useState("");
  const [offset, setOffset] = useState(0);
  const groups = useResource<GroupPage>(`/admin/promo-codes/customer-groups?q=${encodeURIComponent(query)}&limit=10&offset=${offset}`, false);
  function choose(id: number, checked: boolean) {
    onChange(checked ? [...new Set([...value, id])].sort((a, b) => a - b) : value.filter(item => item !== id));
  }
  return <fieldset disabled={disabled} className="min-w-0 space-y-3 rounded border p-3 sm:col-span-2">
    <legend className="px-1 text-sm font-medium">适用客户组</legend>
    <p className="text-xs text-muted-foreground">未选择时面向全部客户，但不能与客户组优惠叠加。选择客户组后，仅这些组的客户可用，并在客户组折后金额上再计算优惠。最多选择 100 个组。</p>
    <label className="grid gap-2 text-sm">搜索适用客户组<Input aria-label="搜索适用客户组" maxLength={120} value={query} onChange={event => { setQuery(event.target.value); setOffset(0); }} /></label>
    <p role="status" className="text-xs text-muted-foreground">{value.length ? `已选 ${value.length} 个客户组` : "全部客户；不叠加客户组优惠"}</p>
    {value.length > 0 && <div className="flex flex-wrap gap-2">{value.map(id => <Button key={id} type="button" size="sm" variant="outline" aria-label={`移除适用客户组 ${id}`} onClick={() => choose(id, false)}>#{id} ×</Button>)}<Button type="button" size="sm" variant="ghost" onClick={() => onChange([])}>清空客户组限制</Button></div>}
    {groups.error ? <Failure error={groups.error} retry={groups.refresh} /> : !groups.data ? <Loading /> : <>
      <div className="max-h-48 space-y-2 overflow-y-auto">{groups.data.items.length ? groups.data.items.map(group => <label key={group.id} className="flex items-center gap-2 text-sm"><input type="checkbox" aria-label={`适用客户组 ${group.name}`} checked={value.includes(group.id)} disabled={!value.includes(group.id) && value.length >= 100} onChange={event => choose(group.id, event.target.checked)} /><span className="break-words">{group.name} <span className="text-xs text-muted-foreground">#{group.id}</span></span></label>) : <p className="py-3 text-xs text-muted-foreground">没有匹配的客户组</p>}</div>
      <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground"><span>共 {groups.data.total} 个客户组</span><div className="flex gap-2"><Button type="button" size="sm" variant="outline" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - 10))}>上一页客户组</Button><Button type="button" size="sm" variant="outline" disabled={offset + groups.data.limit >= groups.data.total} onClick={() => setOffset(offset + 10)}>下一页客户组</Button></div></div>
    </>}
  </fieldset>;
}
