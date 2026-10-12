package store

import (
	"context"
	"strings"
)

type ProductDuplicateInput struct {
	SourceProductID int64  `json:"source_product_id"`
	Name            string `json:"name"`
	Slug            string `json:"slug"`
}

// DuplicateProduct copies catalog-owned state only. Orders, services, carts,
// legacy identities, and audit history always remain attached to the source.
func (s *Store) DuplicateProduct(ctx context.Context, actor int64, in ProductDuplicateInput) (int64, error) {
	in.Name, in.Slug = strings.TrimSpace(in.Name), strings.ToLower(strings.TrimSpace(in.Slug))
	if in.SourceProductID < 1 || in.Name == "" || !contentTextValid(in.Name, 180, false) || !productSlugPattern.MatchString(in.Slug) {
		return 0, conflict("invalid_product_duplicate", "源商品、名称或标识无效")
	}
	tx, err := s.CatalogTx(ctx, actor)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var sourceID int64
	err = tx.QueryRow(ctx, `SELECT id FROM products WHERE id=$1 FOR SHARE`, in.SourceProductID).Scan(&sourceID)
	if err != nil {
		return 0, err
	}
	var supplierMode bool
	if err = tx.QueryRow(ctx, "SELECT provision_mode='supplier' FROM products WHERE id=$1", sourceID).Scan(&supplierMode); err != nil {
		return 0, err
	}
	if supplierMode {
		return 0, conflict("supplier_import_required", "请从供应商目录重新导入商品，交付绑定不能复制")
	}
	var hasRules bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM legacy_product_config_option_links l JOIN product_option_groups g ON g.id=l.config_option_group_id WHERE g.product_id=$1)`, sourceID).Scan(&hasRules); err != nil {
		return 0, err
	}
	if hasRules {
		return 0, conflict("unsupported_product_duplicate", "商品包含高级联动规则，暂不支持复制")
	}
	sourceGroups, sourceOptions, err := loadProductOptionHierarchy(ctx, tx, sourceID)
	if err != nil {
		return 0, err
	}
	var productID int64
	err = tx.QueryRow(ctx, `
INSERT INTO products(category_id,name,slug,description,product_type,billing_cycle,price_cents,setup_fee_cents,stock,active,metadata,revision,sort_order,provision_mode,approval_required,require_verified_phone,require_certification,service_group_id,config_upgrade_enabled,config_downgrade_enabled,delivery_config)
SELECT category_id,$2,$3,description,product_type,billing_cycle,price_cents,setup_fee_cents,stock,active,metadata,1,sort_order,provision_mode,approval_required,require_verified_phone,require_certification,service_group_id,config_upgrade_enabled,config_downgrade_enabled,delivery_config
FROM products WHERE id=$1
RETURNING id`, sourceID, in.Name, in.Slug).Scan(&productID)
	if err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO product_upgrade_targets(product_id,target_product_id,allow_downgrade)
 SELECT $2,target_product_id,allow_downgrade FROM product_upgrade_targets WHERE product_id=$1`, sourceID, productID); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO product_purchase_fields(product_id,field_key,label,field_type,description,required,active,show_invoice,max_length,validation_pattern,choices,sort_order,field_scope,encrypted,customer_profile_key) SELECT $2,field_key,label,field_type,description,required,active,show_invoice,max_length,validation_pattern,choices,sort_order,field_scope,encrypted,customer_profile_key FROM product_purchase_fields WHERE product_id=$1`, sourceID, productID); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO product_prices(product_id,billing_cycle,price_cents,setup_fee_cents,active)
		SELECT $2,billing_cycle,price_cents,setup_fee_cents,active
		FROM product_prices WHERE product_id=$1
		ON CONFLICT(product_id,billing_cycle) DO UPDATE SET price_cents=EXCLUDED.price_cents,setup_fee_cents=EXCLUDED.setup_fee_cents,active=EXCLUDED.active,updated_at=now()`, sourceID, productID); err != nil {
		return 0, err
	}

	groups, err := tx.Query(ctx, `SELECT id,name,slug,required,sort_order,active,option_type,qty_minimum,qty_maximum,qty_stage,unit,notes,upgrade_enabled,legacy_metadata
FROM product_option_groups WHERE product_id=$1 ORDER BY sort_order,id`, sourceID)
	if err != nil {
		return 0, err
	}
	groupMap := map[int64]int64{}
	type groupCopy struct {
		oldID                     int64
		name, slug, unit, notes   string
		required, active, upgrade bool
		sortOrder, optionType     int
		qtyMin, qtyMax, qtyStage  int64
		metadata                  []byte
	}
	groupCopies := []groupCopy{}
	for groups.Next() {
		var copy groupCopy
		if err = groups.Scan(&copy.oldID, &copy.name, &copy.slug, &copy.required, &copy.sortOrder, &copy.active, &copy.optionType, &copy.qtyMin, &copy.qtyMax, &copy.qtyStage, &copy.unit, &copy.notes, &copy.upgrade, &copy.metadata); err != nil {
			groups.Close()
			return 0, err
		}
		groupCopies = append(groupCopies, copy)
	}
	if err = groups.Err(); err != nil {
		groups.Close()
		return 0, err
	}
	groups.Close()
	for _, copy := range groupCopies {
		var newID int64
		err = tx.QueryRow(ctx, `INSERT INTO product_option_groups(product_id,name,slug,required,sort_order,active,option_type,qty_minimum,qty_maximum,qty_stage,unit,notes,upgrade_enabled,legacy_metadata,legacy_source,legacy_id)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'',NULL) RETURNING id`, productID, copy.name, copy.slug, copy.required, copy.sortOrder, copy.active, copy.optionType, copy.qtyMin, copy.qtyMax, copy.qtyStage, copy.unit, copy.notes, copy.upgrade, copy.metadata).Scan(&newID)
		if err != nil {
			return 0, err
		}
		groupMap[copy.oldID] = newID
	}

	options, err := tx.Query(ctx, `SELECT po.id,po.group_id,po.label,po.value,po.price_delta_cents,po.setup_delta_cents,po.sort_order,po.active,po.qty_minimum,po.qty_maximum,po.hidden,po.upgrade_enabled,po.legacy_metadata
FROM product_options po JOIN product_option_groups pog ON pog.id=po.group_id WHERE pog.product_id=$1 ORDER BY po.group_id,po.sort_order,po.id`, sourceID)
	if err != nil {
		return 0, err
	}
	optionMap := map[int64]int64{}
	type optionCopy struct {
		oldID, oldGroup                        int64
		label, value                           string
		priceDelta, setupDelta, qtyMin, qtyMax int64
		sortOrder                              int
		active, hidden, upgrade                bool
		metadata                               []byte
	}
	optionCopies := []optionCopy{}
	for options.Next() {
		var copy optionCopy
		if err = options.Scan(&copy.oldID, &copy.oldGroup, &copy.label, &copy.value, &copy.priceDelta, &copy.setupDelta, &copy.sortOrder, &copy.active, &copy.qtyMin, &copy.qtyMax, &copy.hidden, &copy.upgrade, &copy.metadata); err != nil {
			options.Close()
			return 0, err
		}
		optionCopies = append(optionCopies, copy)
	}
	if err = options.Err(); err != nil {
		options.Close()
		return 0, err
	}
	options.Close()
	for _, copy := range optionCopies {
		newGroup, ok := groupMap[copy.oldGroup]
		if !ok {
			return 0, conflict("invalid_product_duplicate", "源商品配置组关系无效")
		}
		var newID int64
		err = tx.QueryRow(ctx, `INSERT INTO product_options(group_id,label,value,price_delta_cents,setup_delta_cents,sort_order,active,qty_minimum,qty_maximum,hidden,upgrade_enabled,legacy_metadata,legacy_source,legacy_id)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'',NULL) RETURNING id`, newGroup, copy.label, copy.value, copy.priceDelta, copy.setupDelta, copy.sortOrder, copy.active, copy.qtyMin, copy.qtyMax, copy.hidden, copy.upgrade, copy.metadata).Scan(&newID)
		if err != nil {
			return 0, err
		}
		optionMap[copy.oldID] = newID
	}
	for oldID, newID := range groupMap {
		if parent := sourceGroups[oldID].parentGroupID; parent != 0 {
			if _, err = tx.Exec(ctx, "UPDATE product_option_groups SET parent_group_id=$2 WHERE id=$1", newID, groupMap[parent]); err != nil {
				return 0, err
			}
		}
	}
	for oldID, newID := range optionMap {
		if parent := sourceOptions[oldID].parentOptionID; parent != 0 {
			if _, err = tx.Exec(ctx, "UPDATE product_options SET parent_option_id=$2 WHERE id=$1", newID, optionMap[parent]); err != nil {
				return 0, err
			}
		}
	}
	if err = copyNativeConfigurationRules(ctx, tx, sourceID, productID, groupMap, optionMap); err != nil {
		return 0, err
	}
	if _, _, err = loadProductOptionHierarchy(ctx, tx, productID); err != nil {
		return 0, err
	}

	for oldOption, newOption := range optionMap {
		if _, err = tx.Exec(ctx, `INSERT INTO product_option_prices(option_id,billing_cycle,price_cents,setup_fee_cents,active,legacy_source,legacy_id)
SELECT $2,billing_cycle,price_cents,setup_fee_cents,active,'',NULL FROM product_option_prices WHERE option_id=$1`, oldOption, newOption); err != nil {
			return 0, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO product_option_price_tiers(option_id,billing_cycle,qty_minimum,qty_maximum,price_cents,setup_fee_cents,active,legacy_source,legacy_id)
SELECT $2,billing_cycle,qty_minimum,qty_maximum,price_cents,setup_fee_cents,active,'',NULL FROM product_option_price_tiers WHERE option_id=$1`, oldOption, newOption); err != nil {
			return 0, err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO download_products(download_id,product_id)
SELECT download_id,$2 FROM download_products WHERE product_id=$1 ON CONFLICT DO NOTHING`, sourceID, productID); err != nil {
		return 0, err
	}
	if err = Audit(ctx, tx, actor, "product.duplicated", "product", productID, map[string]any{"source_product_id": sourceID, "name": in.Name, "slug": in.Slug}); err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return productID, nil
}
