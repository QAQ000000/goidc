"use client";

import { useState } from "react";
import type { User } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import type { CustomerFieldDefinition } from "./customer-field-inputs";

const filterKeys = ["q", "status", "group_id", "sales_owner_id", "company", "phone", "qq", "certification_status", "level_id"] as const;
export type DirectoryFilters = Record<typeof filterKeys[number], string> & { customFields: Record<string, string | number | boolean> };
export function customerDirectoryActorKey(actor: User | null) {
  return JSON.stringify(actor ? { id: actor.id, role: actor.role, permissions: [...actor.permissions].sort(), scope: actor.customer_scope, ownerless: actor.customer_ownerless, sales: actor.sales_enabled } : null);
}
export const emptyDirectoryFilters = (): DirectoryFilters => ({ q: "", status: "", group_id: "", sales_owner_id: "", company: "", phone: "", qq: "", certification_status: "", level_id: "", customFields: {} });

export function directoryQuery(filters: DirectoryFilters, limit = 25, offset = 0) {
  const query = new URLSearchParams();
  for (const key of filterKeys) {
    const value = filters[key].trim();
    if (value && !((key === "group_id" || key === "sales_owner_id") && value === "all")) query.set(key, value);
  }
  if (Object.keys(filters.customFields).length) query.set("custom_fields", JSON.stringify(filters.customFields));
  query.set("limit", String(limit));
  if (offset > 0) query.set("offset", String(offset));
  return query.toString();
}

export function readDirectoryQuery(source: string) {
  const query = new URLSearchParams(source), filters = emptyDirectoryFilters();
  let error = "";
  for (const key of filterKeys) {
    filters[key] = query.get(key) ?? "";
    if ((key === "group_id" || key === "sales_owner_id") && filters[key] === "all") filters[key] = "";
    if (query.getAll(key).length > 1) error = "列表地址包含重复筛选参数，请重新查询或清空条件。";
  }
  const raw = query.get("custom_fields");
  if (raw !== null) {
    try {
      const value: unknown = JSON.parse(raw);
      if (!value || typeof value !== "object" || Array.isArray(value) || Object.keys(value).length > 20 || new TextEncoder().encode(raw).length > 16384) throw new Error();
      for (const [key, item] of Object.entries(value)) {
        if (!/^[a-z][a-z0-9_]{1,63}$/.test(key) || typeof item === "number" && !Number.isFinite(item) || typeof item !== "string" && typeof item !== "number" && typeof item !== "boolean" || typeof item === "string" && !item.trim()) throw new Error();
        filters.customFields[key] = item;
      }
    } catch { error = "列表地址中的自定义字段条件无效，请重新设置或清空条件。"; }
  }
  const limit = Number(query.get("limit") ?? 25);
  const offset = Number(query.get("offset") ?? (query.has("page") ? Math.max(0, Number(query.get("page")) - 1) * limit : 0));
  if (!Number.isSafeInteger(limit) || limit < 1 || limit > 100 || !Number.isSafeInteger(offset) || offset < 0 || query.getAll("limit").length > 1 || query.getAll("offset").length > 1 || query.getAll("custom_fields").length > 1) error = "列表地址中的分页或筛选参数无效，请清空条件后重试。";
  return { filters, limit: Number.isSafeInteger(limit) && limit >= 1 && limit <= 100 ? limit : 25, offset: Number.isSafeInteger(offset) && offset >= 0 ? offset : 0, error };
}

export const certificationLabels: Record<string, string> = { unverified: "未认证", pending: "待审核", approved: "已认证", rejected: "已驳回" };

export function DirectorySelect({ label, value, onChange, options, disabled, placeholder = "不限", searchable = false, allowEmpty = true }: { label: string; value: string; onChange: (value: string) => void; options: { value: string; label: string }[]; disabled?: boolean; placeholder?: string; searchable?: boolean; allowEmpty?: boolean }) {
  const [search, setSearch] = useState("");
  const filtered = options.filter(option => option.label.toLowerCase().includes(search.trim().toLowerCase()));
  return <div className="min-w-0 space-y-1.5"><span className="block text-sm text-muted-foreground">{label}</span><Select value={value === "" ? "__any__" : `option:${value}`} onValueChange={next => onChange(next === "__any__" ? "" : next.slice(7))} disabled={disabled} onOpenChange={() => setSearch("")}><SelectTrigger aria-label={label} className="h-9 w-full rounded border-border bg-card"><SelectValue placeholder={placeholder} /></SelectTrigger><SelectContent>{searchable && <div className="p-2"><Input aria-label={`搜索${label}选项`} placeholder="搜索选项" value={search} onChange={event => setSearch(event.target.value)} onKeyDown={event => event.stopPropagation()} /></div>}{allowEmpty && <SelectItem value="__any__">{placeholder}</SelectItem>}{value && !options.some(option => option.value === value) && <SelectItem value={`option:${value}`}>编号 {value}（当前候选中不可见）</SelectItem>}{filtered.map(option => <SelectItem key={option.value} value={`option:${option.value}`}>{option.label}</SelectItem>)}{search && !filtered.length && <p className="p-2 text-xs text-muted-foreground">无匹配选项</p>}</SelectContent></Select></div>;
}

export function DirectoryOptionState({ allowed, data, error, retry, label }: { allowed: boolean; data: unknown; error: Error | null; retry: () => void; label: string }) {
  if (!allowed) return <p className="text-xs text-muted-foreground">无{label}目录权限，不加载候选项。</p>;
  if (error) return <p role="alert" className="text-xs text-destructive">{label}候选加载失败。<button type="button" className="ml-1 underline" onClick={retry}>重试</button></p>;
  if (!data) return <p role="status" className="text-xs text-muted-foreground">正在加载{label}候选…</p>;
  return null;
}

export function LevelRuleFilter({ value, onChange, canReadLevels, enabled }: { value: string; onChange: (value: string) => void; canReadLevels: boolean; enabled: boolean }) {
  const [search, setSearch] = useState(""), [offset, setOffset] = useState(0);
  const resource = useResource<{ items: { id: number; name: string }[]; total: number }>(enabled && canReadLevels ? `/admin/customer-levels?active=true&limit=25&offset=${offset}&q=${encodeURIComponent(search)}` : null, false);
  return <div className="space-y-2"><DirectorySelect label="符合等级规则" value={value} onChange={onChange} options={(resource.data?.items ?? []).map(item => ({ value: String(item.id), label: item.name }))} disabled={!canReadLevels || !resource.data} /><p className="text-xs text-muted-foreground">按规则条件匹配，客户可同时符合多个规则。</p>{canReadLevels ? <><Input aria-label="搜索等级规则" placeholder="搜索规则名称" value={search} onChange={event => { setSearch(event.target.value); setOffset(0); }} /><DirectoryOptionState allowed data={resource.data} error={resource.error} retry={resource.refresh} label="等级规则" />{resource.data && resource.data.total > 25 && <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground"><span>候选 {offset + 1}–{Math.min(offset + 25, resource.data.total)} / {resource.data.total}</span><div className="flex gap-1"><Button type="button" size="xs" variant="outline" disabled={offset === 0} onClick={() => setOffset(offset - 25)}>上一组</Button><Button type="button" size="xs" variant="outline" disabled={offset + 25 >= resource.data.total} onClick={() => setOffset(offset + 25)}>下一组</Button></div></div>}</> : <p className="text-xs text-muted-foreground">无等级目录权限；可保留已有规则条件或填写规则编号。</p>}{!canReadLevels && <Input aria-label="等级规则编号" type="number" min={1} step={1} value={value} placeholder="可选：启用的规则编号" onChange={event => onChange(event.target.value)} />}</div>;
}

export function DirectoryCustomFields({ fields, values, onChange }: { fields: CustomerFieldDefinition[]; values: DirectoryFilters["customFields"]; onChange: (next: DirectoryFilters["customFields"]) => void }) {
  function update(key: string, value: string | number | boolean | undefined) {
    const next = { ...values };
    if (value === undefined || typeof value === "string" && !value.trim()) delete next[key]; else next[key] = value;
    onChange(next);
  }
  const unknown = Object.keys(values).filter(key => !fields.some(field => field.field_key === key));
  return <div className="space-y-3"><div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">{fields.map(field => <div key={field.field_key} className="min-w-0 space-y-1.5">{field.field_type === "select" || field.field_type === "boolean" ? <DirectorySelect label={field.label} value={values[field.field_key] === undefined ? "" : String(values[field.field_key])} onChange={value => update(field.field_key, value === "" ? undefined : field.field_type === "boolean" ? value === "true" : value)} options={field.field_type === "boolean" ? [{ value: "true", label: "是" }, { value: "false", label: "否" }] : field.choices.map(choice => ({ value: choice, label: choice }))} /> : <label className="block space-y-1.5 text-sm text-muted-foreground"><span>{field.label}</span><Input type={field.field_type === "number" ? "number" : field.field_type === "date" ? "date" : "text"} step={field.field_type === "number" ? "any" : undefined} value={values[field.field_key] === undefined ? "" : String(values[field.field_key])} maxLength={5000} placeholder={field.field_type === "text" || field.field_type === "textarea" ? "包含文字" : "不限"} onChange={event => update(field.field_key, event.target.value === "" ? undefined : field.field_type === "number" ? Number(event.target.value) : event.target.value)} className="h-9 rounded border-border bg-card" /></label>}</div>)}</div>{unknown.map(key => <div key={key} role="status" className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground"><span>保留条件 {key}：{String(values[key])}（定义未加载或已失效）</span><Button type="button" variant="outline" size="xs" onClick={() => update(key, undefined)}>移除</Button></div>)}</div>;
}
