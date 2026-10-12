package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

type GlobalOptionPrice struct {
	BillingCycle  string `json:"billing_cycle"`
	PriceCents    int64  `json:"price_cents"`
	SetupFeeCents int64  `json:"setup_fee_cents"`
	Active        bool   `json:"active"`
}
type GlobalOption struct {
	ProductOptionInput
	Prices []GlobalOptionPrice `json:"prices"`
}
type GlobalDefinition struct {
	ProductOptionGroupInput
	Items []GlobalOption `json:"items"`
}
type GlobalConfigurationInput struct {
	Name              string             `json:"name"`
	Description       string             `json:"description"`
	Definitions       []GlobalDefinition `json:"definitions"`
	ExpectedVersion   int64              `json:"expected_version"`
	SourceID          int64              `json:"source_id"`
	ProductIDs        []int64            `json:"product_ids"`
	ExpectedRevisions map[int64]int64    `json:"expected_revisions"`
	AcknowledgeDetach bool               `json:"acknowledge_detach"`
}

func ValidateGlobalConfiguration(in *GlobalConfigurationInput) error {
	if in.Definitions == nil {
		in.Definitions = []GlobalDefinition{}
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if !contentTextValid(in.Name, 120, false) || in.Name == "" || !contentTextValid(in.Description, 2000, true) || len(in.Definitions) > 100 {
		return conflict("invalid_global_configuration", "组名称、描述或配置数量无效")
	}
	slugs := map[string]bool{}
	for i := range in.Definitions {
		g := &in.Definitions[i]
		g.Name = strings.TrimSpace(g.Name)
		g.Slug = strings.ToLower(strings.TrimSpace(g.Slug))
		if g.Active == nil {
			v := true
			g.Active = &v
		}
		// Hierarchy identifiers are product-local. Cross-product hierarchy is deliberately rejected.
		if g.Name == "" || !contentTextValid(g.Name, 120, false) || !contentSlug.MatchString(g.Slug) || slugs[g.Slug] || g.ParentGroupID != nil || len(g.OptionParents) > 0 || len(g.Options) > 0 || g.OptionType == 20 || g.OptionType < 0 || g.OptionType > 19 || g.SortOrder < 0 || g.SortOrder > 1000000 || g.QtyMinimum < 0 || g.QtyMaximum < 0 || (g.QtyMaximum > 0 && g.QtyMaximum < g.QtyMinimum) || g.QtyStage < 0 || !contentTextValid(g.Unit, 32, true) || len(g.Items) > 100 {
			return conflict("invalid_global_configuration", "配置标识重复、参数无效或包含商品专属层级；全局组支持独立选择/数量配置")
		}
		slugs[g.Slug] = true
		values := map[string]bool{}
		active := 0
		for j := range g.Items {
			o := &g.Items[j]
			if o.ParentOptionID != nil {
				return conflict("invalid_global_configuration", "全局选项不支持商品专属父选项")
			}
			if err := normalizeOption(&o.ProductOptionInput, true); err != nil {
				return err
			}
			if values[o.Value] {
				return conflict("invalid_global_configuration", "选项标识重复")
			}
			values[o.Value] = true
			if *o.Active {
				active++
			}
			tiers, err := normalizeOptionPriceTierChanges(o.PriceTierChanges)
			if err != nil {
				return err
			}
			o.PriceTierChanges = tiers
			if len(tiers) > 0 && !isQuantityOptionType(g.OptionType) {
				return conflict("invalid_option_pricing", "只有数量配置支持阶梯价")
			}
			cycles := map[string]bool{}
			for _, p := range o.Prices {
				if !validOptionPricingCycle(p.BillingCycle) || cycles[p.BillingCycle] || p.PriceCents < 0 || p.PriceCents > 100000000 || p.SetupFeeCents < 0 || p.SetupFeeCents > 100000000 {
					return conflict("invalid_option_pricing", "周期价格无效或重复")
				}
				cycles[p.BillingCycle] = true
			}
		}
		if g.OptionType == 3 && (g.Required || (*g.Active && active != 1)) {
			return conflict("invalid_global_configuration", "勾选配置必须可选且有一个启用选项")
		}
		if g.Required && *g.Active && active == 0 {
			return conflict("invalid_global_configuration", "启用的必选配置必须有启用选项")
		}
	}
	return nil
}

// Caller owns the CatalogTx, permission check, idempotency record and commit.
func MutateGlobalConfiguration(ctx context.Context, tx pgx.Tx, actor, id int64, operation string, in GlobalConfigurationInput) (map[string]any, error) {
	var version int64
	var previous []byte
	if id != 0 {
		if err := tx.QueryRow(ctx, "SELECT version,definitions FROM global_configuration_groups WHERE id=$1 FOR UPDATE", id).Scan(&version, &previous); err != nil {
			return nil, err
		}
		if version != in.ExpectedVersion {
			return nil, conflict("version_conflict", "全局组已变更，请重新加载")
		}
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('zjmfgo.global_configuration_write','on',true)"); err != nil {
		return nil, err
	}
	switch operation {
	case "save", "copy":
		if operation == "copy" {
			if in.SourceID < 1 {
				return nil, conflict("invalid_global_configuration", "请选择源组")
			}
			var raw []byte
			if err := tx.QueryRow(ctx, "SELECT definitions FROM global_configuration_groups WHERE id=$1 AND version=$2 FOR SHARE", in.SourceID, in.ExpectedVersion).Scan(&raw); err != nil {
				return nil, conflict("version_conflict", "源组已变更，请重新加载")
			}
			if err := json.Unmarshal(raw, &in.Definitions); err != nil {
				return nil, err
			}
		}
		if err := ValidateGlobalConfiguration(&in); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(in.Definitions)
		if err != nil {
			return nil, err
		}
		if id == 0 {
			err = tx.QueryRow(ctx, "INSERT INTO global_configuration_groups(name,description,definitions) VALUES($1,$2,$3) RETURNING id,version", in.Name, in.Description, raw).Scan(&id, &version)
		} else {
			err = tx.QueryRow(ctx, "UPDATE global_configuration_groups SET name=$2,description=$3,definitions=$4,version=version+1,updated_at=now() WHERE id=$1 RETURNING version", id, in.Name, in.Description, raw).Scan(&version)
		}
		if err != nil {
			return nil, err
		}
		rows, err := tx.Query(ctx, "SELECT product_id FROM global_configuration_assignments WHERE global_group_id=$1 ORDER BY product_id", id)
		if err != nil {
			return nil, err
		}
		products, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return nil, err
		}
		for _, p := range products {
			if err = projectGlobalConfiguration(ctx, tx, id, p, in.Definitions); err != nil {
				return nil, err
			}
		}
	case "assign":
		if len(in.ProductIDs) > 100 {
			return nil, conflict("invalid_global_configuration", "一次最多分配100个商品")
		}
		desired := map[int64]bool{}
		for _, p := range in.ProductIDs {
			if p < 1 || desired[p] {
				return nil, conflict("invalid_global_configuration", "商品编号无效或重复")
			}
			desired[p] = true
		}
		rows, err := tx.Query(ctx, "SELECT product_id FROM global_configuration_assignments WHERE global_group_id=$1 ORDER BY product_id", id)
		if err != nil {
			return nil, err
		}
		old, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return nil, err
		}
		current := map[int64]bool{}
		for _, p := range old {
			current[p] = true
		}
		// CatalogTx serializes catalog writers; product locks are ascending.
		rows, err = tx.Query(ctx, "SELECT id,revision,provision_mode FROM products WHERE id=ANY($1) OR id=ANY($2) ORDER BY id FOR UPDATE", in.ProductIDs, old)
		if err != nil {
			return nil, err
		}
		type productState struct {
			ID, Revision int64
			Mode         string
		}
		products, err := pgx.CollectRows(rows, pgx.RowToStructByPos[productState])
		if err != nil {
			return nil, err
		}
		found := map[int64]bool{}
		for _, p := range products {
			found[p.ID] = true
			if desired[p.ID] == current[p.ID] {
				continue
			}
			if in.ExpectedRevisions[p.ID] != p.Revision {
				return nil, conflict("version_conflict", "商品已变更，请重新加载商品版本")
			}
			if desired[p.ID] && (p.Mode == "supplier" || p.Mode == "nokvm") {
				return nil, conflict("supplier_fixed_configuration", "上游商品不能分配本地全局配置")
			}
		}
		for p := range desired {
			if !found[p] {
				return nil, conflict("invalid_global_configuration", "商品不存在")
			}
		}
		var defs []GlobalDefinition
		if err = json.Unmarshal(previous, &defs); err != nil {
			return nil, err
		}
		for _, p := range products {
			if desired[p.ID] == current[p.ID] {
				continue
			}
			if desired[p.ID] {
				if _, err = tx.Exec(ctx, "INSERT INTO global_configuration_assignments(global_group_id,product_id) VALUES($1,$2)", id, p.ID); err != nil {
					return nil, err
				}
				if err = projectGlobalConfiguration(ctx, tx, id, p.ID, defs); err != nil {
					return nil, err
				}
			} else {
				if !in.AcknowledgeDetach {
					return nil, conflict("detach_acknowledgement_required", "取消分配将保留为商品独立配置，仍参与计价；请明确确认")
				}
				if _, err = tx.Exec(ctx, "UPDATE product_option_groups SET global_group_id=NULL WHERE global_group_id=$1 AND product_id=$2", id, p.ID); err != nil {
					return nil, err
				}
				if _, err = tx.Exec(ctx, "DELETE FROM global_configuration_assignments WHERE global_group_id=$1 AND product_id=$2", id, p.ID); err != nil {
					return nil, err
				}
				if _, err = tx.Exec(ctx, "UPDATE products SET revision=revision+1,updated_at=now() WHERE id=$1", p.ID); err != nil {
					return nil, err
				}
			}
		}
		if err = tx.QueryRow(ctx, "UPDATE global_configuration_groups SET version=version+1,updated_at=now() WHERE id=$1 RETURNING version", id).Scan(&version); err != nil {
			return nil, err
		}
	case "delete":
		var used bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM global_configuration_assignments WHERE global_group_id=$1)", id).Scan(&used); err != nil {
			return nil, err
		}
		if used {
			return nil, conflict("global_configuration_in_use", "先取消所有分配，才能删除全局组")
		}
		if _, err := tx.Exec(ctx, "DELETE FROM global_configuration_groups WHERE id=$1", id); err != nil {
			return nil, err
		}
	default:
		return nil, conflict("invalid_global_configuration", "操作无效")
	}
	if err := Audit(ctx, tx, actor, "global_configuration."+operation, "global_configuration_group", id, map[string]any{"version": version, "input": in}); err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "version": version, "deleted": operation == "delete"}, nil
}

func projectGlobalConfiguration(ctx context.Context, tx pgx.Tx, global, product int64, defs []GlobalDefinition) error {
	var mode string
	if err := tx.QueryRow(ctx, "SELECT provision_mode FROM products WHERE id=$1 FOR UPDATE", product).Scan(&mode); err != nil {
		return err
	}
	for _, g := range defs {
		if *g.Active && (mode == "nokvm" || mode == "supplier") {
			return conflict("fixed_configuration", "固定规格交付商品不允许启用全局配置")
		}
	}
	var count int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM product_option_groups WHERE product_id=$1 AND global_group_id IS DISTINCT FROM $2", product, global).Scan(&count); err != nil {
		return err
	}
	if count+len(defs) > 100 {
		return conflict("configuration_limit", "每个商品最多100个配置组")
	}
	// Removal would invalidate service selections and configuration rules. Keep IDs stable;
	// retire items using active=false, or detach the product before structural removal.
	rows, err := tx.Query(ctx, "SELECT slug FROM product_option_groups WHERE global_group_id=$1 AND product_id=$2", global, product)
	if err != nil {
		return err
	}
	existing, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	desired := map[string]bool{}
	for _, g := range defs {
		desired[g.Slug] = true
	}
	for _, slug := range existing {
		if !desired[slug] {
			return conflict("global_configuration_in_use", "已分配配置不能删除或改标识；请停用或先取消分配")
		}
	}
	for _, g := range defs {
		var group int64
		var owner *int64
		err = tx.QueryRow(ctx, "SELECT id,global_group_id FROM product_option_groups WHERE product_id=$1 AND slug=$2 FOR UPDATE", product, g.Slug).Scan(&group, &owner)
		if err == pgx.ErrNoRows {
			err = tx.QueryRow(ctx, `INSERT INTO product_option_groups(product_id,name,slug,required,sort_order,active,option_type,qty_minimum,qty_maximum,qty_stage,unit,global_group_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`, product, g.Name, g.Slug, g.Required, g.SortOrder, *g.Active, g.OptionType, g.QtyMinimum, g.QtyMaximum, g.QtyStage, g.Unit, global).Scan(&group)
		} else if err == nil {
			if owner == nil || *owner != global {
				return conflict("global_configuration_slug_conflict", fmt.Sprintf("商品 %d 已存在独立或其它全局组配置 %s", product, g.Slug))
			}
			var oldType int64
			if err = tx.QueryRow(ctx, "SELECT option_type FROM product_option_groups WHERE id=$1", group).Scan(&oldType); err != nil {
				return err
			}
			if oldType != g.OptionType {
				return conflict("global_configuration_in_use", "分配后不能更改配置类型")
			}
			_, err = tx.Exec(ctx, `UPDATE product_option_groups SET name=$2,required=$3,sort_order=$4,active=$5,qty_minimum=$6,qty_maximum=$7,qty_stage=$8,unit=$9,updated_at=now() WHERE id=$1`, group, g.Name, g.Required, g.SortOrder, *g.Active, g.QtyMinimum, g.QtyMaximum, g.QtyStage, g.Unit)
		}
		if err != nil {
			return err
		}
		rows, err = tx.Query(ctx, "SELECT value FROM product_options WHERE group_id=$1", group)
		if err != nil {
			return err
		}
		old, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		values := map[string]bool{}
		for _, o := range g.Items {
			values[o.Value] = true
		}
		for _, value := range old {
			if !values[value] {
				return conflict("global_configuration_in_use", "已分配选项不能删除或改标识；请停用或先取消分配")
			}
		}
		for _, o := range g.Items {
			upgrade := true
			if o.UpgradeEnabled != nil {
				upgrade = *o.UpgradeEnabled
			}
			var option int64
			err = tx.QueryRow(ctx, `INSERT INTO product_options(group_id,label,value,price_delta_cents,setup_delta_cents,sort_order,active,qty_minimum,qty_maximum,upgrade_enabled) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(group_id,value) DO UPDATE SET label=EXCLUDED.label,price_delta_cents=EXCLUDED.price_delta_cents,setup_delta_cents=EXCLUDED.setup_delta_cents,sort_order=EXCLUDED.sort_order,active=EXCLUDED.active,qty_minimum=EXCLUDED.qty_minimum,qty_maximum=EXCLUDED.qty_maximum,upgrade_enabled=EXCLUDED.upgrade_enabled,updated_at=now() RETURNING id`, group, o.Label, o.Value, o.PriceDeltaCents, o.SetupDeltaCents, o.SortOrder, *o.Active, o.QtyMinimum, o.QtyMaximum, upgrade).Scan(&option)
			if err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, "DELETE FROM product_option_prices WHERE option_id=$1", option); err != nil {
				return err
			}
			for _, p := range o.Prices {
				if _, err = tx.Exec(ctx, `INSERT INTO product_option_prices(option_id,billing_cycle,price_cents,setup_fee_cents,active) VALUES($1,$2,$3,$4,$5)`, option, p.BillingCycle, p.PriceCents, p.SetupFeeCents, p.Active); err != nil {
					return err
				}
			}
			if _, err = tx.Exec(ctx, "DELETE FROM product_option_price_tiers WHERE option_id=$1", option); err != nil {
				return err
			}
			if err = replaceOptionPriceTierChanges(ctx, tx, option, o.PriceTierChanges); err != nil {
				return err
			}
		}
		if g.UpgradeEnabled != nil {
			if _, err = tx.Exec(ctx, "UPDATE product_option_groups SET upgrade_enabled=$2 WHERE id=$1", group, *g.UpgradeEnabled); err != nil {
				return err
			}
		}
	}
	if _, _, err = loadProductOptionHierarchy(ctx, tx, product); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE products SET revision=revision+1,updated_at=now() WHERE id=$1", product)
	return err
}
