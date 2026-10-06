package store

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

type sqlQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type OptionSelection struct {
	IDs             []int64
	Quantities      map[int64]int64
	PriceDeltaCents int64
	SetupDeltaCents int64
	Snapshot        []OptionSnapshot
}

type OptionSnapshot struct {
	ID              int64  `json:"id"`
	GroupID         int64  `json:"group_id"`
	GroupName       string `json:"group_name"`
	GroupSlug       string `json:"group_slug"`
	Label           string `json:"label"`
	Value           string `json:"value"`
	PriceDeltaCents int64  `json:"price_delta_cents"`
	SetupDeltaCents int64  `json:"setup_delta_cents"`
	Quantity        int64  `json:"quantity,omitempty"`
}

func OptionAmounts(price, setup int64, selection OptionSelection) (int64, int64, error) {
	if (selection.PriceDeltaCents > 0 && price > maxInt64-selection.PriceDeltaCents) || (selection.SetupDeltaCents > 0 && setup > maxInt64-selection.SetupDeltaCents) {
		return 0, 0, conflict("invalid_product_options", "配置后金额超出范围")
	}
	price += selection.PriceDeltaCents
	setup += selection.SetupDeltaCents
	if price < 0 || setup < 0 || price > maxInt64-setup {
		return 0, 0, conflict("invalid_product_options", "配置后的周期价和初装费不能为负数")
	}
	return price, setup, nil
}

const maxInt64 = int64(1<<63 - 1)
const minInt64 = -maxInt64 - 1

// ValidateProductOptions is shared by cart writes and checkout so prices cannot
// be changed by sending option IDs from another product or inactive groups.
func ValidateProductOptions(ctx context.Context, q sqlQueryer, productID int64, ids []int64) (OptionSelection, error) {
	return ValidateProductOptionsForCycleWithQuantities(ctx, q, productID, "", ids, nil)
}

// ValidateProductOptionsForCycle uses the option's price matrix when one is
// available for the selected billing cycle, retaining the local delta as a
// fallback for options created by the current admin UI.
func ValidateProductOptionsForCycle(ctx context.Context, q sqlQueryer, productID int64, billingCycle string, ids []int64) (OptionSelection, error) {
	return ValidateProductOptionsForCycleWithQuantities(ctx, q, productID, billingCycle, ids, nil)
}

// ValidateProductOptionsForCycleWithQuantities validates the selected option
// IDs and optional quantities. The quantity map is keyed by option ID; an
// omitted quantity keeps legacy fixed-price behavior at one unit.
func ValidateProductOptionsForCycleWithQuantities(ctx context.Context, q sqlQueryer, productID int64, billingCycle string, ids []int64, quantities map[int64]int64) (OptionSelection, error) {
	selection := OptionSelection{IDs: []int64{}, Quantities: map[int64]int64{}, Snapshot: []OptionSnapshot{}}
	if productID < 1 || len(ids) > 100 {
		return selection, conflict("invalid_product_options", "产品配置项无效")
	}
	ids = append([]int64(nil), ids...)
	if ids == nil {
		ids = []int64{}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for i, id := range ids {
		if id < 1 || (i > 0 && ids[i-1] == id) {
			return selection, conflict("invalid_product_options", "产品配置项重复或无效")
		}
	}
	hierarchies, optionParents, err := loadProductOptionHierarchy(ctx, q, productID)
	if err != nil {
		return selection, err
	}
	for groupID, group := range hierarchies {
		if !group.active || group.optionType != 3 {
			continue
		}
		count := 0
		for _, option := range optionParents {
			if option.groupID == groupID && option.active {
				count++
			}
		}
		if group.required || count != 1 {
			return selection, conflict("unsupported_product_configuration", "勾选附加项必须可选且恰好有一个启用选项")
		}
	}
	requiredGroups := map[int64]bool{}
	for id, group := range hierarchies {
		if group.required && group.active {
			requiredGroups[id] = true
		}
	}
	var rows pgx.Rows
	selectedGroups := map[int64]bool{}
	if len(ids) > 0 {
		rows, err = q.Query(ctx, `SELECT po.id,pog.id,pog.option_type,pog.qty_minimum,pog.qty_maximum,pog.qty_stage,po.qty_minimum,po.qty_maximum,COALESCE(pop.price_cents,po.price_delta_cents),COALESCE(pop.setup_fee_cents,po.setup_delta_cents),COALESCE((SELECT jsonb_agg(jsonb_build_object('qty_minimum',t.qty_minimum,'qty_maximum',t.qty_maximum,'price_cents',t.price_cents,'setup_fee_cents',t.setup_fee_cents) ORDER BY t.qty_minimum,t.id) FROM product_option_price_tiers t WHERE t.option_id=po.id AND t.billing_cycle=$2 AND t.active), '[]'::jsonb),pog.name,pog.slug,po.label,po.value
FROM product_options po JOIN product_option_groups pog ON pog.id=po.group_id
LEFT JOIN product_option_prices pop ON pop.option_id=po.id AND pop.billing_cycle=$2 AND pop.active
WHERE pog.product_id=$1 AND pog.active AND po.active AND po.id=ANY($3) ORDER BY pog.sort_order,pog.id,po.sort_order,po.id`, productID, billingCycle, ids)
		if err != nil {
			return selection, err
		}
		count := 0
		for rows.Next() {
			var id, groupID, optionType, groupMin, groupMax, groupStage, optionMin, optionMax, priceDelta, setupDelta int64
			var tierJSON []byte
			var snapshot OptionSnapshot
			if err = rows.Scan(&id, &groupID, &optionType, &groupMin, &groupMax, &groupStage, &optionMin, &optionMax, &priceDelta, &setupDelta, &tierJSON, &snapshot.GroupName, &snapshot.GroupSlug, &snapshot.Label, &snapshot.Value); err != nil {
				rows.Close()
				return selection, err
			}
			if selectedGroups[groupID] {
				rows.Close()
				return selection, conflict("invalid_product_options", "每个配置组只能选择一项")
			}
			selectedGroups[groupID] = true
			quantity := int64(1)
			if quantityValue, ok := quantities[id]; ok {
				quantity = quantityValue
			}
			if isQuantityOptionType(optionType) {
				if _, ok := quantities[id]; !ok {
					rows.Close()
					return selection, conflict("invalid_product_option_quantity", "数量型配置必须提供数量")
				}
				min, max := optionMin, optionMax
				if min == 0 {
					min = groupMin
				}
				if max == 0 {
					max = groupMax
				}
				if quantity < min || (max > 0 && quantity > max) || quantity < 1 {
					rows.Close()
					return selection, conflict("invalid_product_option_quantity", "配置数量超出允许范围")
				}
				if groupStage > 0 && (quantity-min)%groupStage != 0 {
					rows.Close()
					return selection, conflict("invalid_product_option_quantity", "配置数量不符合步长")
				}
			} else if quantity != 1 {
				rows.Close()
				return selection, conflict("invalid_product_option_quantity", "固定价配置数量必须为一")
			}
			if quantity < 1 || quantity > 1000000 {
				rows.Close()
				return selection, conflict("invalid_product_option_quantity", "配置数量无效")
			}
			var tiers []OptionPriceTier
			if err = json.Unmarshal(tierJSON, &tiers); err != nil {
				rows.Close()
				return selection, conflict("unsupported_product_configuration", "配置阶梯价格数据无效")
			}
			weightedPrice, weightedSetup, ok := PriceForQuantity(tiers, quantity, priceDelta, setupDelta)
			overflow := !ok
			if len(tiers) > 0 && overflow {
				rows.Close()
				return selection, conflict("invalid_product_option_quantity", "配置数量没有匹配的阶梯价格")
			}
			if overflow || (weightedPrice > 0 && selection.PriceDeltaCents > maxInt64-weightedPrice) || (weightedPrice < 0 && selection.PriceDeltaCents < minInt64-weightedPrice) ||
				(weightedSetup > 0 && selection.SetupDeltaCents > maxInt64-weightedSetup) || (weightedSetup < 0 && selection.SetupDeltaCents < minInt64-weightedSetup) {
				rows.Close()
				return selection, conflict("invalid_product_options", "配置项金额超出范围")
			}
			selection.PriceDeltaCents += weightedPrice
			selection.SetupDeltaCents += weightedSetup
			selection.Quantities[id] = quantity
			snapshot.ID, snapshot.GroupID, snapshot.PriceDeltaCents, snapshot.SetupDeltaCents, snapshot.Quantity = id, groupID, weightedPrice, weightedSetup, quantity
			selection.Snapshot = append(selection.Snapshot, snapshot)
			count++
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return selection, err
		}
		rows.Close()
		if count != len(ids) {
			return selection, conflict("invalid_product_options", "配置项不存在或已停用")
		}
	}
	for id := range quantities {
		if !containsInt64(ids, id) {
			return selection, conflict("invalid_product_option_quantity", "数量不能用于未选择的配置项")
		}
	}
	for groupID := range selectedGroups {
		eligible, hierarchyErr := legacyGroupEligible(groupID, selectedGroups, hierarchies, map[int64]bool{})
		if hierarchyErr != nil {
			return selection, hierarchyErr
		}
		if !eligible {
			return selection, conflict("configuration_dependency", "当前配置项依赖尚未选择的上级配置")
		}
	}
	for _, optionID := range ids {
		if !legacyOptionEligible(optionID, ids, optionParents) {
			return selection, conflict("configuration_dependency", "当前配置项依赖尚未选择的上级选项")
		}
	}
	for groupID := range requiredGroups {
		eligible, hierarchyErr := legacyGroupEligible(groupID, selectedGroups, hierarchies, map[int64]bool{})
		if hierarchyErr != nil {
			return selection, hierarchyErr
		}
		if eligible && !selectedGroups[groupID] {
			// A branch ends when it has no active sub-options for its parent choice.
			if hierarchies[groupID].parentGroupID != 0 && !legacyBranchAvailable(groupID, ids, optionParents) {
				continue
			}
			return selection, conflict("required_product_options", "请完成必选配置项")
		}
	}
	selection.IDs = ids
	if err = validateLegacyOptionLinks(ctx, q, productID, selection); err != nil {
		return selection, err
	}
	return selection, nil
}

func isQuantityOptionType(optionType int64) bool {
	switch optionType {
	case 4, 7, 9, 11, 14, 15, 16, 17, 18, 19:
		return true
	default:
		return false
	}
}

func multiplyOptionAmount(price, setup, quantity int64) (int64, int64, bool) {
	if quantity > 0 && ((price > 0 && price > maxInt64/quantity) || (price < 0 && price < minInt64/quantity) || (setup > 0 && setup > maxInt64/quantity) || (setup < 0 && setup < minInt64/quantity)) {
		return 0, 0, true
	}
	return price * quantity, setup * quantity, false
}

type legacyOptionLink struct {
	id, groupID, relationID, legacyID int64
	relation, kind, subIDs, source    string
	ranges                            map[int64]legacySubRange
}

type legacyRuleID struct {
	source string
	id     int64
}

type legacyGroupSelection struct {
	active, quantityType, selected bool
	quantity                       int64
	options                        map[legacyRuleID]bool
}

type legacyGroupHierarchy struct {
	active        bool
	required      bool
	native        bool
	optionType    int64
	source        string
	legacyID      int64
	parentID      int64
	parentGroupID int64
}

func parseLegacyGroupHierarchy(source string, legacyID *int64, metadata []byte) (legacyGroupHierarchy, error) {
	hierarchy := legacyGroupHierarchy{source: strings.TrimSpace(source)}
	if legacyID != nil {
		hierarchy.legacyID = *legacyID
	}
	if len(metadata) == 0 || string(metadata) == "{}" || hierarchy.legacyID == 0 {
		return hierarchy, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &fields); err != nil {
		return hierarchy, conflict("unsupported_product_configuration", "商品配置层级元数据无效")
	}
	parent, present, err := legacyMetadataInt(fields, "linkage_pid")
	if err != nil {
		return hierarchy, err
	}
	if present && parent > 0 {
		hierarchy.parentID = parent
	}
	return hierarchy, nil
}

func legacyMetadataInt(fields map[string]json.RawMessage, key string) (int64, bool, error) {
	raw, ok := fields[key]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return 0, false, nil
	}
	value, err := legacyRangeValue(raw)
	if err != nil {
		return 0, false, conflict("unsupported_product_configuration", "商品配置层级引用必须为整数")
	}
	if value < 0 {
		return 0, false, conflict("unsupported_product_configuration", "商品配置层级引用不能为负数")
	}
	return value, true, nil
}

func legacyGroupEligible(groupID int64, selected map[int64]bool, hierarchies map[int64]legacyGroupHierarchy, visiting map[int64]bool) (bool, error) {
	hierarchy, ok := hierarchies[groupID]
	if !ok || hierarchy.parentGroupID == 0 {
		return true, nil
	}
	if visiting[groupID] {
		return false, conflict("unsupported_product_configuration", "商品配置层级存在循环引用")
	}
	visiting[groupID] = true
	defer delete(visiting, groupID)
	if !selected[hierarchy.parentGroupID] || !hierarchies[hierarchy.parentGroupID].active {
		return false, nil
	}
	return legacyGroupEligible(hierarchy.parentGroupID, selected, hierarchies, visiting)
}

type legacySubRange struct {
	min, max int64
	bounded  bool
}

// validateLegacyOptionLinks evaluates the old condition/result relation format
// after the normal product/option and quantity checks. Every triggered root
// condition must satisfy all of its results, including quantity ranges and
// chained group rules.
func validateLegacyOptionLinks(ctx context.Context, q sqlQueryer, productID int64, selected OptionSelection) error {
	rows, err := q.Query(ctx, `SELECT l.id,l.config_option_group_id,l.relation_legacy_id,l.legacy_id,l.relation,l.link_type,l.sub_ids,l.legacy_source
FROM legacy_product_config_option_links l
JOIN product_option_groups pog ON pog.id=l.config_option_group_id
WHERE pog.product_id=$1 ORDER BY l.id`, productID)
	if err != nil {
		return err
	}
	defer rows.Close()
	links := []legacyOptionLink{}
	for rows.Next() {
		var link legacyOptionLink
		if err = rows.Scan(&link.id, &link.groupID, &link.relationID, &link.legacyID, &link.relation, &link.kind, &link.subIDs, &link.source); err != nil {
			return err
		}
		link.kind = strings.ToLower(strings.TrimSpace(link.kind))
		link.relation = strings.ToLower(strings.TrimSpace(link.relation))
		if link.kind != "condition" && link.kind != "result" {
			return conflict("unsupported_product_configuration", "商品包含无法识别的配置联动类型")
		}
		if link.relation != "seq" && link.relation != "sneq" {
			return conflict("unsupported_product_configuration", "商品包含无法识别的配置联动条件")
		}
		if link.ranges, err = parseLegacySubIDs(link.subIDs); err != nil {
			return err
		}
		links = append(links, link)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	rows.Close()
	if len(links) == 0 {
		return nil
	}

	groups := map[int64]*legacyGroupSelection{}
	rows, err = q.Query(ctx, "SELECT id,active,option_type FROM product_option_groups WHERE product_id=$1", productID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, optionType int64
		group := &legacyGroupSelection{options: map[legacyRuleID]bool{}}
		if err = rows.Scan(&id, &group.active, &optionType); err != nil {
			rows.Close()
			return err
		}
		group.quantityType = isQuantityOptionType(optionType)
		groups[id] = group
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	rows, err = q.Query(ctx, `SELECT po.id,po.group_id,po.legacy_id,po.legacy_source
FROM product_options po JOIN product_option_groups pog ON pog.id=po.group_id
WHERE pog.product_id=$1`, productID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var optionID, groupID int64
		var legacyID *int64
		var source string
		if err = rows.Scan(&optionID, &groupID, &legacyID, &source); err != nil {
			rows.Close()
			return err
		}
		group := groups[groupID]
		chosen := containsInt64(selected.IDs, optionID)
		if chosen {
			group.selected = true
			group.quantity = selected.Quantities[optionID]
		}
		if legacyID != nil {
			group.options[legacyRuleID{source, *legacyID}] = chosen
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	rows, err = q.Query(ctx, `SELECT a.option_id,po.group_id,a.legacy_id,a.legacy_source
FROM legacy_product_config_option_aliases a JOIN product_options po ON po.id=a.option_id JOIN product_option_groups pog ON pog.id=po.group_id
WHERE pog.product_id=$1`, productID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var optionID, groupID int64
		var legacyID int64
		var source string
		if err = rows.Scan(&optionID, &groupID, &legacyID, &source); err != nil {
			rows.Close()
			return err
		}
		if group := groups[groupID]; group != nil {
			group.options[legacyRuleID{source, legacyID}] = containsInt64(selected.IDs, optionID)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	conditions := map[legacyRuleID]legacyOptionLink{}
	results := map[legacyRuleID][]legacyOptionLink{}
	for _, link := range links {
		if err = validateLegacyLinkReferences(link, groups[link.groupID]); err != nil {
			return err
		}
		if link.kind == "condition" && link.relationID == 0 {
			conditions[legacyRuleID{link.source, link.legacyID}] = link
		} else if link.kind == "result" && link.relationID > 0 {
			key := legacyRuleID{link.source, link.relationID}
			results[key] = append(results[key], link)
		} else {
			return conflict("unsupported_product_configuration", "商品联动的条件或结果引用无效")
		}
	}
	for key := range results {
		if _, exists := conditions[key]; !exists {
			return conflict("unsupported_product_configuration", "商品联动缺少同来源的条件")
		}
	}
	for _, condition := range links {
		if condition.kind != "condition" || !groups[condition.groupID].active {
			continue
		}
		if !legacyLinkMatches(condition, groups[condition.groupID]) {
			continue
		}
		for _, result := range results[legacyRuleID{condition.source, condition.legacyID}] {
			if !legacyLinkMatches(result, groups[result.groupID]) {
				return conflict("configuration_dependency", "当前配置组合不满足商品联动条件")
			}
		}
	}
	return nil
}

func validateLegacyLinkReferences(link legacyOptionLink, group *legacyGroupSelection) error {
	if group == nil || len(link.ranges) == 0 {
		return conflict("unsupported_product_configuration", "商品联动缺少配置组或旧子项")
	}
	for legacyID, item := range link.ranges {
		if _, exists := group.options[legacyRuleID{link.source, legacyID}]; !exists {
			return conflict("unsupported_product_configuration", "商品联动的旧子项归属无效")
		}
		if item.bounded && !group.quantityType {
			return conflict("unsupported_product_configuration", "固定价配置不能使用数量区间联动")
		}
	}
	return nil
}

func legacyLinkMatches(link legacyOptionLink, group *legacyGroupSelection) bool {
	matched := false
	for legacyID, item := range link.ranges {
		// Quantity predicates use the group value; tier IDs only identify the
		// archived ranges. Multiple entries form the union of those ranges.
		if group.quantityType {
			matched = group.selected && group.quantity >= item.min && group.quantity <= item.max
		} else {
			matched = group.options[legacyRuleID{link.source, legacyID}]
		}
		if matched {
			break
		}
	}
	if link.relation == "sneq" {
		return !matched
	}
	return matched
}

func parseLegacySubIDs(raw string) (map[int64]legacySubRange, error) {
	result := map[int64]legacySubRange{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return result, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, conflict("unsupported_product_configuration", "商品联动的旧子项数据无效")
	}
	for key, value := range values {
		legacyID, err := strconv.ParseInt(key, 10, 64)
		if err != nil || legacyID < 1 || strconv.FormatInt(legacyID, 10) != key {
			return nil, conflict("unsupported_product_configuration", "商品联动引用了无效旧子项")
		}
		item := legacySubRange{}
		if len(value) > 0 && string(value) != "null" {
			var fields map[string]json.RawMessage
			if err = json.Unmarshal(value, &fields); err != nil {
				return nil, conflict("unsupported_product_configuration", "商品联动的旧数量数据无效")
			}
			if item.min, err = legacyRangeValue(fields["qty_minimum"]); err != nil {
				return nil, err
			}
			if item.max, err = legacyRangeValue(fields["qty_maximum"]); err != nil {
				return nil, err
			}
			if item.min < 0 || item.max < item.min {
				return nil, conflict("unsupported_product_configuration", "商品联动的旧数量区间无效")
			}
			item.bounded = item.min != 0 || item.max != 0
		}
		result[legacyID] = item
	}
	return result, nil
}

func legacyRangeValue(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 {
		return 0, nil
	}
	var number json.Number
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		if err = json.Unmarshal(raw, &number); err != nil {
			return 0, conflict("unsupported_product_configuration", "商品联动的数量必须为整数")
		}
		value = number.String()
	}
	n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, conflict("unsupported_product_configuration", "商品联动的数量必须为整数")
	}
	return n, nil
}
