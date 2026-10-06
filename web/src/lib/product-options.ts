import type { ProductOption, ProductOptionGroup } from "./api";

type OptionBranch = { group: ProductOptionGroup; options: ProductOption[]; selected: number };

export function resolveProductOptions(groups: ProductOptionGroup[], choices: Record<number, number> = {}) {
  const byID = new Map(groups.map((group) => [group.id, group]));
  const resolved = new Map<number, OptionBranch | null>();
  const visiting = new Set<number>();
  let valid = true;
  function visit(group: ProductOptionGroup): OptionBranch | null {
    if (resolved.has(group.id)) return resolved.get(group.id) ?? null;
    if (visiting.has(group.id)) { valid = false; return null; }
    if (!group.active) { resolved.set(group.id, null); return null; }
    visiting.add(group.id);
    let parentOption = 0;
    if (group.parent_group_id) {
      const parent = byID.get(group.parent_group_id);
      const parentBranch = parent ? visit(parent) : null;
      if (!parentBranch?.selected) {
        visiting.delete(group.id);
        resolved.set(group.id, null);
        return null;
      }
      parentOption = parentBranch.selected;
    }
    const options = group.options.filter((option) => option.active && (option.parent_option_id ?? 0) === parentOption);
    // No child choices means this particular branch has reached its last level.
    if (group.parent_group_id && options.length === 0) {
      visiting.delete(group.id);
      resolved.set(group.id, null);
      return null;
    }
    const requested = choices[group.id];
    const selected = options.some((option) => option.id === requested) ? requested : (group.required && group.option_type !== 3 ? options[0]?.id ?? 0 : 0);
    const branch = { group, options, selected };
    visiting.delete(group.id);
    resolved.set(group.id, branch);
    return branch;
  }
  for (const group of groups) visit(group);
  const branches = [...resolved.values()].filter((branch): branch is OptionBranch => branch !== null);
  const selected = Object.fromEntries(branches.map((branch) => [branch.group.id, branch.selected]));
  return {
    branches, selected,
    ids: branches.flatMap((branch) => branch.selected ? [branch.selected] : []),
    complete: valid && branches.every((branch) => (branch.group.option_type !== 3 || (!branch.group.required && branch.options.length === 1)) && (!branch.group.required || branch.selected > 0)),
  };
}

export function optionGroupDescendants(groups: ProductOptionGroup[], groupID: number) {
  const descendants = new Set([groupID]);
  // Clear the entire descendant path, including choices that are currently hidden.
  for (let pass = 0; pass < groups.length; pass++) {
    let changed = false;
    for (const group of groups) {
      if (group.parent_group_id && descendants.has(group.parent_group_id) && !descendants.has(group.id)) {
        descendants.add(group.id);
        changed = true;
      }
    }
    if (!changed) break;
  }
  return descendants;
}

export function changeProductOption(groups: ProductOptionGroup[], selected: Record<number, number>, groupID: number, optionID: number) {
  const next = { ...selected, [groupID]: optionID };
  for (const id of optionGroupDescendants(groups, groupID)) {
    if (id !== groupID) next[id] = 0;
  }
  return next;
}

export function isQuantityOption(group: ProductOptionGroup) {
  return [4, 7, 9, 11, 14, 15, 16, 17, 18, 19].includes(group.option_type ?? 0);
}

export function optionQuantityBounds(group: ProductOptionGroup, option: ProductOption) {
  const origin = option.qty_minimum || group.qty_minimum || 0;
  const step = group.qty_stage || 1;
  const min = origin > 0 ? origin : step;
  const max = Math.min(option.qty_maximum || group.qty_maximum || 1000000, 1000000);
  return { min, max, step };
}

export function productOptionAmounts(option: ProductOption, cycle: string, quantity = 1) {
  if (!Number.isSafeInteger(quantity) || quantity < 1 || quantity > 1000000) return null;
  const tiers = (option.price_tiers ?? []).filter(tier => tier.billing_cycle === cycle && tier.active !== false).sort((a, b) => a.qty_minimum - b.qty_minimum);
  if (tiers.length === 0) {
    const matrix = option.pricing?.find(price => price.billing_cycle === cycle && price.active !== false);
    const price = (matrix?.price_cents ?? option.price_delta_cents) * quantity;
    const setup = (matrix?.setup_fee_cents ?? option.setup_delta_cents) * quantity;
    return Number.isSafeInteger(price) && Number.isSafeInteger(setup) ? { price, setup } : null;
  }
  let previousMaximum = 0;
  for (const tier of tiers) {
    if (!Number.isSafeInteger(tier.qty_minimum) || !Number.isSafeInteger(tier.qty_maximum) || tier.qty_minimum <= previousMaximum || tier.qty_maximum < tier.qty_minimum || tier.price_cents < 0 || tier.setup_fee_cents < 0) return null;
    previousMaximum = tier.qty_maximum;
  }
  const selected = tiers.find(tier => quantity >= tier.qty_minimum && quantity <= tier.qty_maximum);
  if (!selected) return null;
  // Match Go's inclusive progressive intervals; setup applies once at this tier.
  const previous = tiers.filter(tier => tier.qty_minimum < selected.qty_minimum).reduce((sum, tier) => sum + (tier.qty_maximum - tier.qty_minimum + 1) * tier.price_cents, 0);
  const price = previous + (quantity - selected.qty_minimum + 1) * selected.price_cents;
  const setup = selected.setup_fee_cents;
  return Number.isSafeInteger(price) && Number.isSafeInteger(setup) ? { price, setup } : null;
}
