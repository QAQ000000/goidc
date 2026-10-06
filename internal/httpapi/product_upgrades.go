package httpapi

import (
	"net/http"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func (s *Server) productUpgradeTargets(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if r.Method == "PUT" {
		var in store.ProductUpgradeTargetsInput
		if !decode(w, r, &in) {
			return
		}
		if err := s.db.SaveProductUpgradeTargets(r.Context(), identityFrom(r).ID, id, in); err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, 200, map[string]int64{"product_id": id})
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), `SELECT p.revision,
 coalesce((SELECT jsonb_agg(jsonb_build_object('product_id',t.target_product_id,'allow_downgrade',t.allow_downgrade) ORDER BY t.target_product_id) FROM product_upgrade_targets t WHERE t.product_id=p.id),'[]'::jsonb) AS targets,
 coalesce((SELECT jsonb_agg(jsonb_build_object('id',c.id,'name',c.name,'active',c.active,'compatible',c.product_type=p.product_type AND c.provision_mode=p.provision_mode) ORDER BY c.sort_order,c.id) FROM products c WHERE c.id<>p.id AND ((c.product_type=p.product_type AND c.provision_mode=p.provision_mode) OR EXISTS(SELECT 1 FROM product_upgrade_targets t WHERE t.product_id=p.id AND t.target_product_id=c.id))),'[]'::jsonb) AS candidates
 FROM products p WHERE p.id=$1`, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	result, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, result)
}

func (s *Server) productUpgradeOptions(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var source, revision int64
	var cycle string
	var kind, mode string
	if err := s.db.Pool.QueryRow(r.Context(), `SELECT s.product_id,p.revision,s.billing_cycle,p.product_type,s.provision_mode FROM services s JOIN products p ON p.id=s.product_id WHERE s.id=$1 AND s.user_id=$2 AND NOT s.import_review_required`, id, identityFrom(r).ID).Scan(&source, &revision, &cycle, &kind, &mode); err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), "SELECT "+productProjection(false)+`,t.allow_downgrade AS upgrade_allow_downgrade FROM products JOIN product_upgrade_targets t ON t.target_product_id=products.id
 WHERE t.product_id=$1 AND products.active AND catalog_category_visible(products.category_id)
 AND products.product_type=$3 AND products.provision_mode=$4 AND (NOT products.approval_required OR $4='manual')
 AND EXISTS(SELECT 1 FROM product_prices pp WHERE pp.product_id=products.id AND pp.billing_cycle=$2 AND pp.active) ORDER BY products.sort_order,products.id`, source, cycle, kind, mode)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	products, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if products == nil {
		products = []map[string]any{}
	}
	write(w, 200, map[string]any{"source_revision": revision, "products": products})
}

func (s *Server) quoteProductChange(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if !s.authLimit(w, r, "product-change-quote", 60) {
		return
	}
	var in store.ProductChangeInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.db.QuoteProductChange(r.Context(), identityFrom(r).ID, id, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 201, out)
}
