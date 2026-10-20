package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func productProjection(admin bool) string { return store.CatalogProductProjection(admin) }

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
	selectSQL := "SELECT " + productProjection(admin) + where + " ORDER BY sort_order,id"
	if paged {
		s.writeListPage(w, r, "SELECT count(*)"+where, selectSQL, args, limit, offset)
		return
	}
	n := len(args)
	s.list(w, r, selectSQL+fmt.Sprintf(" LIMIT $%d OFFSET $%d", n+1, n+2), append(args, limit, offset)...)
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
	saved, err := s.db.SaveProduct(requestFileContext(r), identityFrom(r).ID, id, input, r.Header.Get("Idempotency-Key"))
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
	id, err := s.db.DuplicateProduct(requestFileContext(r), identityFrom(r).ID, in, r.Header.Get("Idempotency-Key"))
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
	var input store.ProductPricingInput
	if !decode(w, r, &input) {
		return
	}
	out, err := s.db.SaveProductPricing(requestFileContext(r), identityFrom(r).ID, id, input)
	if err != nil {
		var business *store.AccountBusinessError
		if errors.As(err, &business) {
			writeError(w, business.Status, business.Code, business.Message)
			return
		}
		s.fail(w, r, err)
		return
	}
	write(w, 200, out)
}
