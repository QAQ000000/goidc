package store

import "strings"

const productColumns = `products.id,products.category_id,products.name,products.slug,products.description,products.product_type,products.billing_cycle,products.price_cents,products.setup_fee_cents,products.stock,products.active,products.provision_mode,products.approval_required,products.require_verified_phone,products.require_certification,products.metadata,products.revision,products.sort_order,(SELECT name FROM product_categories WHERE id=products.category_id) AS category_name,(SELECT section_id FROM product_categories WHERE id=products.category_id) AS section_id,catalog_category_visible(products.category_id) AS category_visible,
COALESCE((SELECT jsonb_agg(jsonb_build_object('id',pp.id,'billing_cycle',pp.billing_cycle,'price_cents',pp.price_cents,'setup_fee_cents',pp.setup_fee_cents,'active',pp.active) ORDER BY pp.id)
FROM product_prices pp WHERE pp.product_id=products.id), '[]'::jsonb) AS pricing,
COALESCE((SELECT jsonb_agg(jsonb_build_object('id',pog.id,'name',pog.name,'slug',pog.slug,'required',pog.required,'active',pog.active,'sort_order',pog.sort_order,'option_type',pog.option_type,'qty_minimum',pog.qty_minimum,'qty_maximum',pog.qty_maximum,'qty_stage',pog.qty_stage,'unit',pog.unit,'upgrade_enabled',pog.upgrade_enabled,'options',
  COALESCE((SELECT jsonb_agg(jsonb_build_object('id',po.id,'label',po.label,'value',po.value,'price_delta_cents',po.price_delta_cents,'setup_delta_cents',po.setup_delta_cents,'active',po.active,'hidden',po.hidden,'qty_minimum',po.qty_minimum,'qty_maximum',po.qty_maximum,'upgrade_enabled',po.upgrade_enabled,'sort_order',po.sort_order,'pricing',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',pop.id,'billing_cycle',pop.billing_cycle,'price_cents',pop.price_cents,'setup_fee_cents',pop.setup_fee_cents,'active',pop.active) ORDER BY pop.id) FROM product_option_prices pop WHERE pop.option_id=po.id), '[]'::jsonb),'price_tiers',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',pot.id,'billing_cycle',pot.billing_cycle,'pricing_mode',pot.pricing_mode,'qty_minimum',pot.qty_minimum,'qty_maximum',pot.qty_maximum,'price_cents',pot.price_cents,'setup_fee_cents',pot.setup_fee_cents,'active',pot.active) ORDER BY pot.billing_cycle,pot.qty_minimum,pot.id) FROM product_option_price_tiers pot WHERE pot.option_id=po.id), '[]'::jsonb)) ORDER BY po.sort_order,po.id)
  FROM product_options po WHERE po.group_id=pog.id /*value_visibility*/), '[]'::jsonb)) ORDER BY pog.sort_order,pog.id)
FROM product_option_groups pog WHERE pog.product_id=products.id /*group_visibility*/), '[]'::jsonb) AS option_groups`

// CatalogProductProjection keeps public and administrative catalog fields aligned.
// Administrative notes never enter the public purchase or quote projection.
func CatalogProductProjection(admin bool) string {
	groups, values := "", ""
	if !admin {
		groups = "AND pog.active"
		values = "AND po.active"
	}
	projection := strings.ReplaceAll(strings.ReplaceAll(productColumns, "/*group_visibility*/", groups), "/*value_visibility*/", values)
	if admin {
		projection = "CASE WHEN products.provision_mode='nokvm' THEN products.delivery_config ELSE NULL END AS delivery_config," + projection
		projection = strings.ReplaceAll(projection, "'option_type',pog.option_type", "'notes',pog.notes,'option_type',pog.option_type")
	}
	// Resolve old identities inside the same product and source. Invalid metadata
	// remains readable by admins; the shared purchase validator rejects it.
	groupParent := `CASE WHEN pog.hierarchy_native THEN COALESCE(pog.parent_group_id,0) WHEN pog.option_type=20 THEN COALESCE((SELECT parent.id FROM product_option_groups parent WHERE parent.product_id=pog.product_id AND parent.legacy_source=pog.legacy_source AND parent.legacy_id=CASE WHEN pg_input_is_valid(pog.legacy_metadata->>'linkage_pid','bigint') THEN (pog.legacy_metadata->>'linkage_pid')::bigint END),0) ELSE 0 END`
	optionParent := `CASE WHEN po.hierarchy_native THEN COALESCE(po.parent_option_id,0) WHEN pog.option_type=20 THEN COALESCE((SELECT parent.id FROM product_options parent JOIN product_option_groups pg ON pg.id=parent.group_id WHERE pg.product_id=pog.product_id AND parent.legacy_source=po.legacy_source AND parent.legacy_id=CASE WHEN pg_input_is_valid(po.legacy_metadata->>'linkage_pid','bigint') THEN (po.legacy_metadata->>'linkage_pid')::bigint END),0) ELSE 0 END`
	projection = strings.ReplaceAll(projection, "'option_type',pog.option_type", "'parent_group_id',"+groupParent+",'option_type',pog.option_type")
	projection = strings.ReplaceAll(projection, "'label',po.label", "'parent_option_id',"+optionParent+",'label',po.label")
	imported := `EXISTS(SELECT 1 FROM supplier_catalog_imports ci JOIN supplier_product_bindings b ON b.id=ci.binding_id WHERE b.product_id=products.id)`
	zeroQuantity := `(` + imported + ` AND pog.option_type=4 AND pog.qty_minimum=0)`
	projection = strings.ReplaceAll(projection, "'option_type',pog.option_type", "'allow_zero_quantity',"+zeroQuantity+",'option_type',pog.option_type")
	projection = strings.ReplaceAll(projection, "'label',po.label", "'allow_zero_quantity',"+zeroQuantity+" AND po.qty_minimum=0,'label',po.label")
	projection = `CASE WHEN ` + imported + ` THEN 'configurable' ELSE '' END AS supplier_catalog_mode,` + projection
	fieldVisibility := ""
	if !admin {
		fieldVisibility = " AND f.active"
	}
	if !admin {
		fieldVisibility += " AND f.field_scope <> 'admin'"
	}
	fields := `coalesce((SELECT jsonb_agg(jsonb_build_object('key',f.field_key,'label',f.label,'type',f.field_type,'description',f.description,'required',f.required,'active',f.active,'show_invoice',f.show_invoice,'max_length',f.max_length,'pattern',f.validation_pattern,'choices',f.choices,'sort_order',f.sort_order,'scope',f.field_scope,'encrypted',f.encrypted,'customer_profile_key',CASE WHEN f.field_scope='customer' THEN f.customer_profile_key ELSE '' END) ORDER BY f.sort_order,f.field_key) FROM product_purchase_fields f WHERE f.product_id=products.id` + fieldVisibility + `),'[]'::jsonb) AS purchase_field_definitions,`
	ruleVisibility := ""
	if !admin {
		ruleVisibility = " AND r.enabled"
	}
	rules := `coalesce((SELECT jsonb_agg(jsonb_build_object('id',r.id,'name',r.name,'enabled',r.enabled,'conditions',coalesce((SELECT jsonb_agg(jsonb_build_object('group_id',p.group_id,'option_id',p.option_id,'operator',p.operator,'qty_minimum',p.qty_minimum,'qty_maximum',p.qty_maximum) ORDER BY p.sort_order) FROM native_configuration_predicates p WHERE p.rule_id=r.id AND p.phase='condition'),'[]'::jsonb),'results',coalesce((SELECT jsonb_agg(jsonb_build_object('group_id',p.group_id,'option_id',p.option_id,'operator',p.operator,'qty_minimum',p.qty_minimum,'qty_maximum',p.qty_maximum) ORDER BY p.sort_order) FROM native_configuration_predicates p WHERE p.rule_id=r.id AND p.phase='result'),'[]'::jsonb)) ORDER BY r.id) FROM native_configuration_rules r WHERE r.product_id=products.id` + ruleVisibility + `),'[]'::jsonb) AS configuration_rules,`
	return rules + fields + `products.config_upgrade_enabled,products.config_downgrade_enabled,products.service_group_id,(SELECT name FROM service_navigation_groups WHERE id=products.service_group_id) AS service_group_name,` + projection
}
