package store

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

type ProductOptionInput struct {
	UpgradeEnabled   *bool                        `json:"upgrade_enabled,omitempty"`
	PriceTierChanges map[string][]OptionPriceTier `json:"price_tier_changes,omitempty"`
	ParentOptionID   *int64                       `json:"parent_option_id"`
	Label            string                       `json:"label"`
	Value            string                       `json:"value"`
	PriceDeltaCents  int64                        `json:"price_delta_cents"`
	SetupDeltaCents  int64                        `json:"setup_delta_cents"`
	SortOrder        int                          `json:"sort_order"`
	Active           *bool                        `json:"active"`
	ExpectedRevision *int64                       `json:"expected_revision"`
	QtyMinimum       int64                        `json:"qty_minimum"`
	QtyMaximum       int64                        `json:"qty_maximum"`
}
type ProductOptionGroupInput struct {
	UpgradeEnabled   *bool                `json:"upgrade_enabled,omitempty"`
	ParentGroupID    *int64               `json:"parent_group_id"`
	OptionParents    map[int64]int64      `json:"option_parents"`
	Name             string               `json:"name"`
	Slug             string               `json:"slug"`
	Required         bool                 `json:"required"`
	SortOrder        int                  `json:"sort_order"`
	Active           *bool                `json:"active"`
	ExpectedRevision *int64               `json:"expected_revision"`
	Options          []ProductOptionInput `json:"options"`
	OptionType       int64                `json:"option_type"`
	QtyMinimum       int64                `json:"qty_minimum"`
	QtyMaximum       int64                `json:"qty_maximum"`
	QtyStage         int64                `json:"qty_stage"`
	Unit             string               `json:"unit"`
}

func normalizeOption(in *ProductOptionInput, defaults bool) error {
	if in.ParentOptionID != nil && *in.ParentOptionID < 0 {
		return conflict("invalid_product_options", "父选项编号无效")
	}
	in.Label, in.Value = strings.TrimSpace(in.Label), strings.TrimSpace(in.Value)
	if defaults && in.Active == nil {
		v := true
		in.Active = &v
	}
	if in.Label == "" || in.Value == "" || !contentTextValid(in.Label, 120, false) || !contentTextValid(in.Value, 120, false) || in.Active == nil || in.SortOrder < 0 || in.SortOrder > 1000000 || in.PriceDeltaCents < -100000000 || in.PriceDeltaCents > 100000000 || in.SetupDeltaCents < -100000000 || in.SetupDeltaCents > 100000000 || in.QtyMinimum < 0 || in.QtyMaximum < 0 || (in.QtyMaximum > 0 && in.QtyMaximum < in.QtyMinimum) {
		return conflict("invalid_product_options", "配置选项、金额或排序无效")
	}
	return nil
}
func (s *Store) configurationTx(ctx context.Context, actor, product int64, revision *int64, required bool) (pgx.Tx, error) {
	if required && (revision == nil || *revision < 1) {
		return nil, conflict("version_conflict", "请重新加载商品后操作")
	}
	tx, err := s.CatalogTx(ctx, actor)
	if err != nil {
		return nil, err
	}
	var current int64
	err = tx.QueryRow(ctx, "SELECT revision FROM products WHERE id=$1 FOR UPDATE", product).Scan(&current)
	if err == nil && revision != nil && current != *revision {
		err = conflict("version_conflict", "商品配置已变更，请重新加载")
	}
	if err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}
func configurationCommit(ctx context.Context, tx pgx.Tx, actor, product, object int64, action string) error {
	if _, err := tx.Exec(ctx, "UPDATE products SET revision=revision+1,updated_at=now() WHERE id=$1", product); err != nil {
		return err
	}
	if err := Audit(ctx, tx, actor, action, "product", product, map[string]any{"configuration_id": object}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) SaveProductOptionGroup(ctx context.Context, actor, product, group int64, in ProductOptionGroupInput) (int64, error) {
	if (in.ParentGroupID != nil && (*in.ParentGroupID < 0 || (*in.ParentGroupID > 0 && in.OptionType != 20))) || len(in.OptionParents) > 100 || (group == 0 && len(in.OptionParents) > 0) {
		return 0, conflict("invalid_product_options", "多级父配置组或子选项映射无效")
	}
	in.Name, in.Slug = strings.TrimSpace(in.Name), strings.ToLower(strings.TrimSpace(in.Slug))
	if group == 0 && in.Active == nil {
		v := true
		in.Active = &v
	}
	if in.Name == "" || !contentTextValid(in.Name, 120, false) || !contentSlug.MatchString(in.Slug) || in.Active == nil || in.SortOrder < 0 || in.SortOrder > 1000000 || len(in.Options) > 100 || (group != 0 && len(in.Options) > 0) || in.OptionType < 0 || in.QtyMinimum < 0 || in.QtyMaximum < 0 || (in.QtyMaximum > 0 && in.QtyMaximum < in.QtyMinimum) || in.QtyStage < 0 || !contentTextValid(in.Unit, 32, true) {
		return 0, conflict("invalid_product_options", "配置组名称、标识或排序无效")
	}
	activeCount := 0
	for i := range in.Options {
		if len(in.Options[i].PriceTierChanges) > 0 {
			return 0, conflict("invalid_option_pricing", "请先创建配置组，再为选项配置阶梯价格")
		}
		if err := normalizeOption(&in.Options[i], true); err != nil {
			return 0, err
		}
		if *in.Options[i].Active {
			activeCount++
		}
	}
	tx, err := s.configurationTx(ctx, actor, product, in.ExpectedRevision, group != 0)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if *in.Active {
		var mode string
		if err = tx.QueryRow(ctx, "SELECT provision_mode FROM products WHERE id=$1", product).Scan(&mode); err != nil {
			return 0, err
		}
		if mode == "nokvm" {
			return 0, conflict("nokvm_fixed_resources", "当前 NOKVM 商品使用固定资源规格，暂不支持可配置选项")
		}
	}
	if group == 0 {
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM product_option_groups WHERE product_id=$1", product).Scan(&count); err != nil {
			return 0, err
		}
		if count >= 100 {
			return 0, conflict("configuration_limit", "每个商品最多 100 个配置组")
		}
	} else {
		if err = tx.QueryRow(ctx, "SELECT id FROM product_option_groups WHERE id=$1 AND product_id=$2 FOR UPDATE", group, product).Scan(&group); err != nil {
			return 0, err
		}
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM product_options WHERE group_id=$1 AND active", group).Scan(&activeCount); err != nil {
			return 0, err
		}
	}
	if in.OptionType == 3 && (in.Required || (*in.Active && activeCount != 1)) {
		return 0, conflict("invalid_product_options", "勾选附加项必须可选，启用时须恰好有一个启用选项")
	}
	if *in.Active && in.Required && activeCount == 0 {
		return 0, conflict("required_options_empty", "启用的必选配置组至少需要一个启用选项")
	}
	action := "product.option_group.updated"
	if group == 0 {
		action = "product.options.created"
		err = tx.QueryRow(ctx, `INSERT INTO product_option_groups(product_id,name,slug,required,sort_order,active,option_type,qty_minimum,qty_maximum,qty_stage,unit) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`, product, in.Name, in.Slug, in.Required, in.SortOrder, *in.Active, in.OptionType, in.QtyMinimum, in.QtyMaximum, in.QtyStage, in.Unit).Scan(&group)
		if err == nil {
			for _, o := range in.Options {
				if _, err = tx.Exec(ctx, `INSERT INTO product_options(group_id,label,value,price_delta_cents,setup_delta_cents,sort_order,active,qty_minimum,qty_maximum,parent_option_id,upgrade_enabled) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10::bigint,0),COALESCE($11,true))`, group, o.Label, o.Value, o.PriceDeltaCents, o.SetupDeltaCents, o.SortOrder, *o.Active, o.QtyMinimum, o.QtyMaximum, o.ParentOptionID, o.UpgradeEnabled); err != nil {
					break
				}
			}
		}
	} else {
		_, err = tx.Exec(ctx, `UPDATE product_option_groups SET name=$2,slug=$3,required=$4,sort_order=$5,active=$6,option_type=$7,qty_minimum=$8,qty_maximum=$9,qty_stage=$10,unit=$11,
parent_group_id=CASE WHEN $12::bigint IS NULL THEN parent_group_id ELSE NULLIF($12::bigint,0) END,
hierarchy_native=hierarchy_native OR $12::bigint IS NOT NULL,updated_at=now() WHERE id=$1`, group, in.Name, in.Slug, in.Required, in.SortOrder, *in.Active, in.OptionType, in.QtyMinimum, in.QtyMaximum, in.QtyStage, in.Unit, in.ParentGroupID)
	}
	if err != nil {
		return 0, err
	}
	if in.ParentGroupID != nil && action == "product.options.created" {
		if _, err = tx.Exec(ctx, "UPDATE product_option_groups SET parent_group_id=NULLIF($2::bigint,0),hierarchy_native=true WHERE id=$1", group, *in.ParentGroupID); err != nil {
			return 0, err
		}
	}
	for option, parent := range in.OptionParents {
		if option < 1 || parent < 0 {
			return 0, conflict("invalid_product_options", "父选项映射编号无效")
		}
		tag, updateErr := tx.Exec(ctx, "UPDATE product_options SET parent_option_id=NULLIF($3::bigint,0),hierarchy_native=true,updated_at=now() WHERE id=$1 AND group_id=$2", option, group, parent)
		if updateErr != nil {
			return 0, updateErr
		}
		if tag.RowsAffected() != 1 {
			return 0, conflict("invalid_product_options", "父选项映射不属于当前配置组")
		}
	}
	if _, _, err = loadProductOptionHierarchy(ctx, tx, product); err != nil {
		return 0, err
	}
	if in.UpgradeEnabled != nil {
		if _, err = tx.Exec(ctx, "UPDATE product_option_groups SET upgrade_enabled=$2 WHERE id=$1", group, *in.UpgradeEnabled); err != nil {
			return 0, err
		}
	}
	return group, configurationCommit(ctx, tx, actor, product, group, action)
}
func (s *Store) SaveProductOption(ctx context.Context, actor, product, group, option int64, in ProductOptionInput) (int64, error) {
	if err := normalizeOption(&in, option == 0); err != nil {
		return 0, err
	}
	tierChanges, err := normalizeOptionPriceTierChanges(in.PriceTierChanges)
	if err != nil {
		return 0, err
	}
	tx, err := s.configurationTx(ctx, actor, product, in.ExpectedRevision, true)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var required, active bool
	var optionType int64
	if err = tx.QueryRow(ctx, "SELECT required,active,option_type FROM product_option_groups WHERE id=$1 AND product_id=$2 FOR UPDATE", group, product).Scan(&required, &active, &optionType); err != nil {
		return 0, err
	}
	if len(tierChanges) > 0 && !isQuantityOptionType(optionType) {
		return 0, conflict("invalid_option_pricing", "只有数量型配置支持阶梯价格")
	}
	var count, activeOthers int
	if err = tx.QueryRow(ctx, "SELECT count(*),count(*) FILTER(WHERE active AND id<>$2) FROM product_options WHERE group_id=$1", group, option).Scan(&count, &activeOthers); err != nil {
		return 0, err
	}
	if option == 0 && count >= 100 {
		return 0, conflict("configuration_limit", "每个配置组最多 100 个选项")
	}
	if option != 0 {
		if err = tx.QueryRow(ctx, "SELECT id FROM product_options WHERE id=$1 AND group_id=$2 FOR UPDATE", option, group).Scan(&option); err != nil {
			return 0, err
		}
	}
	if optionType == 3 {
		enabledAfter := activeOthers
		if *in.Active {
			enabledAfter++
		}
		if enabledAfter > 1 || (active && enabledAfter != 1) || required {
			return 0, conflict("invalid_product_options", "勾选附加项必须可选，启用时须恰好有一个启用选项")
		}
	}
	if required && active && !*in.Active && activeOthers == 0 {
		return 0, conflict("required_options_empty", "请先停用配置组或启用其他选项")
	}
	action := "product.option.updated"
	if option == 0 {
		action = "product.option.created"
		err = tx.QueryRow(ctx, `INSERT INTO product_options(group_id,label,value,price_delta_cents,setup_delta_cents,sort_order,active,qty_minimum,qty_maximum) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`, group, in.Label, in.Value, in.PriceDeltaCents, in.SetupDeltaCents, in.SortOrder, *in.Active, in.QtyMinimum, in.QtyMaximum).Scan(&option)
	} else {
		_, err = tx.Exec(ctx, `UPDATE product_options SET label=$2,value=$3,price_delta_cents=$4,setup_delta_cents=$5,sort_order=$6,active=$7,qty_minimum=$8,qty_maximum=$9,updated_at=now() WHERE id=$1`, option, in.Label, in.Value, in.PriceDeltaCents, in.SetupDeltaCents, in.SortOrder, *in.Active, in.QtyMinimum, in.QtyMaximum)
	}
	if err != nil {
		return 0, err
	}
	if in.ParentOptionID != nil {
		if _, err = tx.Exec(ctx, "UPDATE product_options SET parent_option_id=NULLIF($2::bigint,0),hierarchy_native=true WHERE id=$1", option, *in.ParentOptionID); err != nil {
			return 0, err
		}
	}
	if _, _, err = loadProductOptionHierarchy(ctx, tx, product); err != nil {
		return 0, err
	}
	if err = replaceOptionPriceTierChanges(ctx, tx, option, tierChanges); err != nil {
		return 0, err
	}
	if in.UpgradeEnabled != nil {
		if _, err = tx.Exec(ctx, "UPDATE product_options SET upgrade_enabled=$2 WHERE id=$1", option, *in.UpgradeEnabled); err != nil {
			return 0, err
		}
	}
	return option, configurationCommit(ctx, tx, actor, product, option, action)
}
func (s *Store) DeleteProductConfiguration(ctx context.Context, actor, product, group, option int64, revision *int64) error {
	tx, err := s.configurationTx(ctx, actor, product, revision, true)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var required, active bool
	var optionType int64
	if err = tx.QueryRow(ctx, "SELECT required,active,option_type FROM product_option_groups WHERE id=$1 AND product_id=$2 FOR UPDATE", group, product).Scan(&required, &active, &optionType); err != nil {
		return err
	}
	if option != 0 {
		if err = tx.QueryRow(ctx, "SELECT id FROM product_options WHERE id=$1 AND group_id=$2 FOR UPDATE", option, group).Scan(&option); err != nil {
			return err
		}
	}
	groups, options, err := loadProductOptionHierarchy(ctx, tx, product)
	if err != nil {
		return err
	}
	if option == 0 {
		for _, child := range groups {
			if child.parentGroupID == group {
				return conflict("configuration_in_use", "配置组仍被下级配置引用，请先调整父子关系")
			}
		}
	} else {
		for _, child := range options {
			if child.parentOptionID == option {
				return conflict("configuration_in_use", "选项仍被下级选项引用，请先调整父子关系")
			}
		}
	}
	var used bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product_options po WHERE po.group_id=$1 AND ($2::bigint=0 OR po.id=$2) AND
 (EXISTS(SELECT 1 FROM cart_items ci WHERE ci.option_ids @> jsonb_build_array(po.id)) OR EXISTS(SELECT 1 FROM order_items oi WHERE oi.config_options @> jsonb_build_array(po.id)) OR EXISTS(SELECT 1 FROM service_configuration_changes c WHERE c.option_ids @> jsonb_build_array(po.id) AND c.status<>'quoted')))`, group, option).Scan(&used); err != nil {
		return err
	}
	if used {
		return conflict("configuration_in_use", "配置已被购物车或订单引用，请停用而不是删除")
	}
	action := "product.option_group.deleted"
	object := group
	if option == 0 {
		_, err = tx.Exec(ctx, "DELETE FROM product_option_groups WHERE id=$1", group)
	} else {
		var others int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM product_options WHERE group_id=$1 AND active AND id<>$2", group, option).Scan(&others); err != nil {
			return err
		}
		if required && active && others == 0 {
			return conflict("required_options_empty", "请先停用配置组或启用其他选项")
		}
		if optionType == 3 && active && others != 1 {
			return conflict("invalid_product_options", "请先停用勾选附加项配置组")
		}
		action = "product.option.deleted"
		object = option
		_, err = tx.Exec(ctx, "DELETE FROM product_options WHERE id=$1", option)
	}
	if err != nil {
		return err
	}
	return configurationCommit(ctx, tx, actor, product, object, action)
}
