package store

import "context"

type legacyOptionParent struct {
	groupID, parentOptionID int64
	active                  bool
}

func loadProductOptionHierarchy(ctx context.Context, q sqlQueryer, productID int64) (map[int64]legacyGroupHierarchy, map[int64]legacyOptionParent, error) {
	rows, err := q.Query(ctx, `SELECT id,required,active,option_type,legacy_source,legacy_id,legacy_metadata,COALESCE(parent_group_id,0),hierarchy_native FROM product_option_groups WHERE product_id=$1`, productID)
	if err != nil {
		return nil, nil, err
	}
	groups := map[int64]legacyGroupHierarchy{}
	identities := map[legacyRuleID]int64{}
	for rows.Next() {
		var id, optionType, parentID int64
		var active, required, native bool
		var source string
		var legacyID *int64
		var metadata []byte
		if err = rows.Scan(&id, &required, &active, &optionType, &source, &legacyID, &metadata, &parentID, &native); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if native || optionType != 20 {
			metadata = nil
		}
		group, parseErr := parseLegacyGroupHierarchy(source, legacyID, metadata)
		if parseErr != nil {
			rows.Close()
			return nil, nil, parseErr
		}
		group.active, group.required, group.native, group.optionType = active, required, native, optionType
		if native {
			group.parentGroupID = parentID
		}
		groups[id] = group
		if group.legacyID > 0 {
			identities[legacyRuleID{group.source, group.legacyID}] = id
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()
	for id, group := range groups {
		if !group.native && group.parentID > 0 {
			parent, ok := identities[legacyRuleID{group.source, group.parentID}]
			if !ok {
				return nil, nil, conflict("unsupported_product_configuration", "商品配置层级引用了不存在的父级")
			}
			group.parentGroupID = parent
			groups[id] = group
		}
		if group.parentGroupID != 0 {
			parent, ok := groups[group.parentGroupID]
			if !ok || group.optionType != 20 || parent.optionType != 20 {
				return nil, nil, conflict("unsupported_product_configuration", "多级父配置组必须属于同一商品并使用多级类型")
			}
		}
	}
	if err = validateLegacyGroupGraph(groups); err != nil {
		return nil, nil, err
	}
	options, err := loadLegacyOptionParents(ctx, q, productID, groups)
	return groups, options, err
}

func validateLegacyGroupGraph(groups map[int64]legacyGroupHierarchy) error {
	states := map[int64]int{}
	var visit func(int64) error
	visit = func(id int64) error {
		if states[id] == 1 {
			return conflict("unsupported_product_configuration", "商品配置层级存在循环引用")
		}
		if states[id] == 2 {
			return nil
		}
		states[id] = 1
		if parent := groups[id].parentGroupID; parent != 0 {
			if _, ok := groups[parent]; !ok {
				return conflict("unsupported_product_configuration", "商品配置层级缺少父级")
			}
			if err := visit(parent); err != nil {
				return err
			}
		}
		states[id] = 2
		return nil
	}
	for id := range groups {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func loadLegacyOptionParents(ctx context.Context, q sqlQueryer, productID int64, groups map[int64]legacyGroupHierarchy) (map[int64]legacyOptionParent, error) {
	rows, err := q.Query(ctx, `SELECT po.id,po.group_id,po.active,po.legacy_source,po.legacy_id,po.legacy_metadata,COALESCE(po.parent_option_id,0),po.hierarchy_native
FROM product_options po JOIN product_option_groups pog ON pog.id=po.group_id WHERE pog.product_id=$1`, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	options := map[int64]legacyOptionParent{}
	references := map[int64]legacyGroupHierarchy{}
	identities := map[legacyRuleID]int64{}
	for rows.Next() {
		var id int64
		var option legacyOptionParent
		var source string
		var legacyID *int64
		var metadata []byte
		var native bool
		if err = rows.Scan(&id, &option.groupID, &option.active, &source, &legacyID, &metadata, &option.parentOptionID, &native); err != nil {
			return nil, err
		}
		group := groups[option.groupID]
		option.active = option.active && group.active
		options[id] = option
		if group.optionType != 20 {
			continue
		}
		if legacyID != nil {
			identities[legacyRuleID{source, *legacyID}] = id
		}
		if native {
			continue
		}
		ref, parseErr := parseLegacyGroupHierarchy(source, legacyID, metadata)
		if parseErr != nil {
			return nil, parseErr
		}
		if group.parentGroupID != 0 && ref.parentID == 0 {
			return nil, conflict("unsupported_product_configuration", "下级配置选项缺少父选项")
		}
		references[id] = ref
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for id, ref := range references {
		if ref.parentID == 0 {
			continue
		}
		option := options[id]
		parent, ok := identities[legacyRuleID{ref.source, ref.parentID}]
		group := groups[option.groupID]
		if !ok || group.parentGroupID == 0 || options[parent].groupID != group.parentGroupID || ref.source != group.source {
			return nil, conflict("unsupported_product_configuration", "商品配置选项的父级归属无效")
		}
		option.parentOptionID = parent
		options[id] = option
	}
	for _, option := range options {
		group := groups[option.groupID]
		if option.parentOptionID == 0 {
			if group.parentGroupID != 0 {
				return nil, conflict("unsupported_product_configuration", "下级配置选项缺少父选项")
			}
			continue
		}
		parent, ok := options[option.parentOptionID]
		if !ok || group.optionType != 20 || group.parentGroupID == 0 || parent.groupID != group.parentGroupID {
			return nil, conflict("unsupported_product_configuration", "父选项必须属于当前配置组的父组")
		}
	}
	// Option edges follow the already validated group graph, so they cannot cycle.
	return options, nil
}

func legacyOptionEligible(id int64, selected []int64, options map[int64]legacyOptionParent) bool {
	option, ok := options[id]
	if !ok || !option.active {
		return false
	}
	if option.parentOptionID == 0 {
		return true
	}
	return containsInt64(selected, option.parentOptionID) && legacyOptionEligible(option.parentOptionID, selected, options)
}

func legacyBranchAvailable(groupID int64, selected []int64, options map[int64]legacyOptionParent) bool {
	for id, option := range options {
		if option.groupID == groupID && legacyOptionEligible(id, selected, options) {
			return true
		}
	}
	return false
}
