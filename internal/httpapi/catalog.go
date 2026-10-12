package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

const productColumns = `products.id,products.category_id,products.name,products.slug,products.description,products.product_type,products.billing_cycle,products.price_cents,products.setup_fee_cents,products.stock,products.active,products.provision_mode,products.approval_required,products.require_verified_phone,products.require_certification,products.metadata,products.revision,products.sort_order,(SELECT name FROM product_categories WHERE id=products.category_id) AS category_name,(SELECT section_id FROM product_categories WHERE id=products.category_id) AS section_id,catalog_category_visible(products.category_id) AS category_visible,
COALESCE((SELECT jsonb_agg(jsonb_build_object('id',pp.id,'billing_cycle',pp.billing_cycle,'price_cents',pp.price_cents,'setup_fee_cents',pp.setup_fee_cents,'active',pp.active) ORDER BY pp.id)
FROM product_prices pp WHERE pp.product_id=products.id), '[]'::jsonb) AS pricing,
COALESCE((SELECT jsonb_agg(jsonb_build_object('id',pog.id,'name',pog.name,'slug',pog.slug,'required',pog.required,'active',pog.active,'sort_order',pog.sort_order,'option_type',pog.option_type,'qty_minimum',pog.qty_minimum,'qty_maximum',pog.qty_maximum,'qty_stage',pog.qty_stage,'unit',pog.unit,'upgrade_enabled',pog.upgrade_enabled,'options',
  COALESCE((SELECT jsonb_agg(jsonb_build_object('id',po.id,'label',po.label,'value',po.value,'price_delta_cents',po.price_delta_cents,'setup_delta_cents',po.setup_delta_cents,'active',po.active,'hidden',po.hidden,'qty_minimum',po.qty_minimum,'qty_maximum',po.qty_maximum,'upgrade_enabled',po.upgrade_enabled,'sort_order',po.sort_order,'pricing',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',pop.id,'billing_cycle',pop.billing_cycle,'price_cents',pop.price_cents,'setup_fee_cents',pop.setup_fee_cents,'active',pop.active) ORDER BY pop.id) FROM product_option_prices pop WHERE pop.option_id=po.id), '[]'::jsonb),'price_tiers',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',pot.id,'billing_cycle',pot.billing_cycle,'qty_minimum',pot.qty_minimum,'qty_maximum',pot.qty_maximum,'price_cents',pot.price_cents,'setup_fee_cents',pot.setup_fee_cents,'active',pot.active) ORDER BY pot.billing_cycle,pot.qty_minimum,pot.id) FROM product_option_price_tiers pot WHERE pot.option_id=po.id), '[]'::jsonb)) ORDER BY po.sort_order,po.id)
  FROM product_options po WHERE po.group_id=pog.id /*value_visibility*/), '[]'::jsonb)) ORDER BY pog.sort_order,pog.id)
FROM product_option_groups pog WHERE pog.product_id=products.id /*group_visibility*/), '[]'::jsonb) AS option_groups`

func productProjection(admin bool) string {
	groups, values := "", ""
	if !admin {
		groups = "AND pog.active"
		values = "AND po.active"
	}
	projection := strings.ReplaceAll(strings.ReplaceAll(productColumns, "/*group_visibility*/", groups), "/*value_visibility*/", values)
	if admin {
		projection = "CASE WHEN products.provision_mode='nokvm' THEN products.delivery_config ELSE NULL END AS delivery_config," + projection
	}
	// Resolve old identities inside the same product and source. Invalid metadata
	// remains readable by admins; the shared purchase validator rejects it.
	groupParent := `CASE WHEN pog.hierarchy_native THEN COALESCE(pog.parent_group_id,0) WHEN pog.option_type=20 THEN COALESCE((SELECT parent.id FROM product_option_groups parent WHERE parent.product_id=pog.product_id AND parent.legacy_source=pog.legacy_source AND parent.legacy_id=CASE WHEN pg_input_is_valid(pog.legacy_metadata->>'linkage_pid','bigint') THEN (pog.legacy_metadata->>'linkage_pid')::bigint END),0) ELSE 0 END`
	optionParent := `CASE WHEN po.hierarchy_native THEN COALESCE(po.parent_option_id,0) WHEN pog.option_type=20 THEN COALESCE((SELECT parent.id FROM product_options parent JOIN product_option_groups pg ON pg.id=parent.group_id WHERE pg.product_id=pog.product_id AND parent.legacy_source=po.legacy_source AND parent.legacy_id=CASE WHEN pg_input_is_valid(po.legacy_metadata->>'linkage_pid','bigint') THEN (po.legacy_metadata->>'linkage_pid')::bigint END),0) ELSE 0 END`
	projection = strings.ReplaceAll(projection, "'option_type',pog.option_type", "'parent_group_id',"+groupParent+",'option_type',pog.option_type")
	projection = strings.ReplaceAll(projection, "'label',po.label", "'parent_option_id',"+optionParent+",'label',po.label")
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

func (s *Server) catalog(w http.ResponseWriter, r *http.Request) { s.catalogList(w, r, false, false) }
func (s *Server) catalogPage(w http.ResponseWriter, r *http.Request) {
	s.catalogList(w, r, false, true)
}
func (s *Server) adminProducts(w http.ResponseWriter, r *http.Request) {
	s.catalogList(w, r, true, false)
}
func (s *Server) adminProductsPage(w http.ResponseWriter, r *http.Request) {
	s.catalogList(w, r, true, true)
}
func (s *Server) catalogList(w http.ResponseWriter, r *http.Request, admin, paged bool) {
	category, ok := categoryFilter(w, r)
	if !ok {
		return
	}
	section, ok := catalogSectionFilter(w, r)
	if !ok {
		return
	}
	state := r.URL.Query().Get("active")
	if admin && state != "" && state != "true" && state != "false" {
		writeError(w, 400, "invalid_status", "商品状态无效")
		return
	}
	limit, offset := pagination(r)
	where := ` FROM products WHERE (name ILIKE $1 OR slug ILIKE $1 OR description ILIKE $1) AND ($2::bigint IS NULL OR coalesce(category_id,0)=$2) AND ($3::bigint IS NULL OR EXISTS(SELECT 1 FROM product_categories c WHERE c.id=category_id AND c.section_id=$3))`
	if !admin {
		where += " AND active AND catalog_category_visible(category_id)"
	} else {
		where += " AND ($4='' OR active=($4='true'))"
	}
	args := []any{"%" + strings.TrimSpace(r.URL.Query().Get("q")) + "%", category, section}
	if admin {
		args = append(args, state)
	}
	var total int64
	if paged {
		if err := s.db.Pool.QueryRow(r.Context(), "SELECT count(*)"+where, args...).Scan(&total); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	n := len(args)
	args = append(args, limit, offset)
	rows, err := s.db.Pool.Query(r.Context(), "SELECT "+productProjection(admin)+where+fmt.Sprintf(" ORDER BY sort_order,id LIMIT $%d OFFSET $%d", n+1, n+2), args...)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if items == nil {
		items = []map[string]any{}
	}
	if paged {
		write(w, 200, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
	} else {
		write(w, 200, items)
	}
}
func (s *Server) product(w http.ResponseWriter, r *http.Request)      { s.productDetail(w, r, false) }
func (s *Server) adminProduct(w http.ResponseWriter, r *http.Request) { s.productDetail(w, r, true) }
func (s *Server) productDetail(w http.ResponseWriter, r *http.Request, admin bool) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	query := "SELECT " + productProjection(admin) + " FROM products WHERE id=$1"
	if !admin {
		query += " AND active AND catalog_category_visible(category_id)"
	}
	rows, err := s.db.Pool.Query(r.Context(), query, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	item, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, item)
}
func (s *Server) categories(w http.ResponseWriter, r *http.Request) {
	s.list(w, r, `SELECT id,name,slug,section_id,description,headline,tagline,sort_order FROM product_categories WHERE catalog_category_visible(id) ORDER BY sort_order,id`)
}
func (s *Server) catalogSections(w http.ResponseWriter, r *http.Request) {
	s.list(w, r, "SELECT id,name,slug,description,sort_order FROM product_sections WHERE active ORDER BY sort_order,id")
}
func validSlug(slug string) bool {
	if len(slug) == 0 || len(slug) > 120 {
		return false
	}
	for _, c := range slug {
		if c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}
func (s *Server) createProduct(w http.ResponseWriter, r *http.Request) {
	var id int64
	if r.Method == "PATCH" {
		var ok bool
		id, ok = pathID(w, r)
		if !ok {
			return
		}
	}
	var input store.ProductInput
	if !decodeLimit(w, r, &input, 256<<10) {
		return
	}
	saved, err := s.db.SaveProduct(r.Context(), identityFrom(r).ID, id, input)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := 200
	if id == 0 {
		status = 201
	}
	write(w, status, map[string]int64{"id": saved})
}

func (s *Server) duplicateProduct(w http.ResponseWriter, r *http.Request) {
	source, ok := pathID(w, r)
	if !ok {
		return
	}
	var in store.ProductDuplicateInput
	if !decodeLimit(w, r, &in, 64<<10) {
		return
	}
	in.SourceProductID = source
	id, err := s.db.DuplicateProduct(r.Context(), identityFrom(r).ID, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusCreated, map[string]int64{"id": id, "source_product_id": source})
}

func (s *Server) upsertProductPricing(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		BillingCycle     string `json:"billing_cycle"`
		PriceCents       int64  `json:"price_cents"`
		SetupFeeCents    int64  `json:"setup_fee_cents"`
		Active           *bool  `json:"active"`
		Default          bool   `json:"default"`
		ExpectedRevision *int64 `json:"expected_revision"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.BillingCycle != "monthly" && input.BillingCycle != "quarterly" && input.BillingCycle != "annually" && input.BillingCycle != "once" {
		writeError(w, 400, "invalid_cycle", "账期无效")
		return
	}
	if input.PriceCents < 0 || input.PriceCents > 100000000 || input.SetupFeeCents < 0 || input.SetupFeeCents > 100000000 {
		writeError(w, 400, "invalid_pricing", "价格或初装费无效")
		return
	}
	active := true
	if input.Active != nil {
		active = *input.Active
	}
	if input.Default && !active {
		writeError(w, 400, "invalid_pricing", "默认账期不能停用")
		return
	}
	tx, err := s.db.CatalogTx(r.Context(), identityFrom(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var productID, revision int64
	var defaultCycle string
	if err = tx.QueryRow(r.Context(), "SELECT id,revision,billing_cycle FROM products WHERE id=$1 FOR UPDATE", id).Scan(&productID, &revision, &defaultCycle); err != nil {
		s.fail(w, r, err)
		return
	}
	if input.ExpectedRevision != nil && *input.ExpectedRevision != revision {
		writeError(w, 409, "version_conflict", "商品已变更，请重新加载")
		return
	}
	if !active && defaultCycle == input.BillingCycle {
		writeError(w, 400, "invalid_pricing", "默认账期不能停用，请先切换默认账期")
		return
	}
	if active {
		if err = store.RequireSupplierProductCycle(r.Context(), tx, id, input.BillingCycle); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	var priceID int64
	err = tx.QueryRow(r.Context(), `INSERT INTO product_prices(product_id,billing_cycle,price_cents,setup_fee_cents,active)
VALUES($1,$2,$3,$4,$5)
ON CONFLICT(product_id,billing_cycle) DO UPDATE SET price_cents=EXCLUDED.price_cents,setup_fee_cents=EXCLUDED.setup_fee_cents,active=EXCLUDED.active,updated_at=now()
RETURNING id`, id, input.BillingCycle, input.PriceCents, input.SetupFeeCents, active).Scan(&priceID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if input.Default || input.BillingCycle == defaultCycle {
		if _, err = tx.Exec(r.Context(), "UPDATE products SET billing_cycle=$2,price_cents=$3,setup_fee_cents=$4,revision=revision+1,updated_at=now() WHERE id=$1", id, input.BillingCycle, input.PriceCents, input.SetupFeeCents); err != nil {
			s.fail(w, r, err)
			return
		}
	} else if _, err = tx.Exec(r.Context(), "UPDATE products SET revision=revision+1,updated_at=now() WHERE id=$1", id); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "product.pricing.updated", "product", id, input); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"id": priceID, "product_id": id, "billing_cycle": input.BillingCycle, "price_cents": input.PriceCents, "setup_fee_cents": input.SetupFeeCents, "active": active, "default": input.Default || input.BillingCycle == defaultCycle})
}
