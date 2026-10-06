"use client";

import { useId } from "react";
import type { ProductOption, ProductOptionGroup } from "@/lib/api";
import { Checkbox } from "./ui/checkbox";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";

export function ProductOptionChoice({ group, options, selected, onChange, label, disabled, optionDisabled, renderLabel }: {
  group: ProductOptionGroup;
  options: ProductOption[];
  selected: number;
  onChange: (id: number) => void;
  label: string;
  disabled?: boolean;
  optionDisabled?: (option: ProductOption) => boolean;
  renderLabel?: (option: ProductOption) => string;
}) {
  const radioName = useId();
  const name = (option: ProductOption) => renderLabel?.(option) ?? option.label;
  if (group.option_type === 3) {
    if (group.required || options.length !== 1) {
      return <p role="alert" className="text-sm text-destructive">勾选附加项配置无效，请联系管理员</p>;
    }
    const option = options[0];
    return <label className="flex items-center gap-2 text-sm break-words"><Checkbox aria-label={label} checked={selected === option.id} disabled={disabled || optionDisabled?.(option)} onCheckedChange={checked => onChange(checked === true ? option.id : 0)} /><span>{name(option)}</span></label>;
  }
  if (group.option_type === 2) {
    return <div role="radiogroup" aria-label={label} className="space-y-2">
      {!group.required && <label className="flex items-center gap-2 text-sm"><input type="radio" name={radioName} checked={!selected} disabled={disabled} onChange={() => onChange(0)} className="accent-primary" />不选择</label>}
      {options.map(option => <label key={option.id} className="flex items-center gap-2 text-sm break-words"><input type="radio" name={radioName} checked={selected === option.id} disabled={disabled || optionDisabled?.(option)} onChange={() => onChange(option.id)} className="accent-primary" />{name(option)}</label>)}
    </div>;
  }
  return <Select value={selected ? String(selected) : "none"} disabled={disabled} onValueChange={value => onChange(value === "none" ? 0 : Number(value))}>
    <SelectTrigger aria-label={label} className="w-full"><SelectValue placeholder={`选择${group.name}`} /></SelectTrigger>
    <SelectContent>
      {!group.required && <SelectItem value="none">不选择</SelectItem>}
      {options.map(option => <SelectItem key={option.id} value={String(option.id)} disabled={optionDisabled?.(option)}>{name(option)}</SelectItem>)}
    </SelectContent>
  </Select>;
}
