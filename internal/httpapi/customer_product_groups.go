package httpapi

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
)

type customerProductGroupInput = store.CustomerProductGroupInput
type customerProductDiscountInput = store.CustomerProductDiscountInput

func normalizeCustomerProductGroup(in *customerProductGroupInput, create bool) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 120 || (create && in.ExpectedVersion != 0) || (!create && in.ExpectedVersion < 1) {
		return &store.Conflict{Code: "invalid_customer_product_group", Message: "商品分组名称或版本无效"}
	}
	seen := make(map[int64]struct{}, len(in.ProductIDs))
	ids := make([]int64, 0, len(in.ProductIDs))
	for _, id := range in.ProductIDs {
		if id < 1 {
			return &store.Conflict{Code: "invalid_customer_product_group", Message: "商品编号无效"}
		}
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	in.ProductIDs = ids
	return nil
}

func (s *Server) adminCustomerProductGroups(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit, offset := pagination(r)
	where := `($1='' OR g.name ILIKE '%'||$1||'%')`
	s.writeListPage(w, r, `SELECT count(*) FROM customer_product_groups g WHERE `+where,
		`
SELECT g.id,g.name,g.version,g.created_at,g.updated_at,
       count(gp.product_id)::int AS product_count,
       COALESCE(array_agg(gp.product_id ORDER BY gp.product_id) FILTER (WHERE gp.product_id IS NOT NULL), '{}') AS product_ids,
       COALESCE(jsonb_agg(jsonb_build_object('id',p.id,'name',p.name) ORDER BY p.id) FILTER (WHERE p.id IS NOT NULL), '[]'::jsonb) AS products
FROM customer_product_groups g
LEFT JOIN customer_product_group_products gp ON gp.group_id=g.id
LEFT JOIN products p ON p.id=gp.product_id
WHERE `+where+`
GROUP BY g.id
ORDER BY g.id DESC`, []any{q}, limit, offset)
}

func (s *Server) createCustomerProductGroup(w http.ResponseWriter, r *http.Request) {
	var in customerProductGroupInput
	if !decode(w, r, &in) {
		return
	}
	if err := normalizeCustomerProductGroup(&in, true); err != nil {
		s.fail(w, r, err)
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	result, replayed, err := s.db.CreateCustomerProductGroup(requestFileContext(r), identityFrom(r).ID, key, in)
	if err != nil {
		s.failCustomerPricing(w, r, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	write(w, status, result)
}

func (s *Server) updateCustomerProductGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in customerProductGroupInput
	if !decode(w, r, &in) {
		return
	}
	if err := normalizeCustomerProductGroup(&in, false); err != nil {
		s.fail(w, r, err)
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	result, err := s.db.UpdateCustomerProductGroup(requestFileContext(r), identityFrom(r).ID, id, key, in)
	if err != nil {
		s.failCustomerPricing(w, r, err)
		return
	}
	write(w, http.StatusOK, result)
}

func (s *Server) deleteCustomerProductGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in store.CustomerProductGroupDeleteInput
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedVersion < 1 {
		writeError(w, http.StatusBadRequest, "expected_version_required", "请提供有效版本")
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	result, err := s.db.DeleteCustomerProductGroup(requestFileContext(r), identityFrom(r).ID, id, key, in)
	if err != nil {
		s.failCustomerPricing(w, r, err)
		return
	}
	write(w, http.StatusOK, result)
}

func validateCustomerProductDiscount(in *customerProductDiscountInput) error {
	if in.CustomerGroupID < 1 || in.ProductGroupID < 1 || (in.ExpectedVersion < 0) {
		return &store.Conflict{Code: "invalid_customer_product_discount", Message: "折扣关联或版本无效"}
	}
	switch in.DiscountType {
	case "percent":
		if in.DiscountPercent < 1 || in.DiscountPercent > 100 || in.FixedCents != 0 {
			return &store.Conflict{Code: "invalid_customer_product_discount", Message: "折扣百分比无效"}
		}
	case "fixed":
		if in.FixedCents < 1 || in.FixedCents > 100000000 || in.DiscountPercent != 0 {
			return &store.Conflict{Code: "invalid_customer_product_discount", Message: "固定折扣金额无效"}
		}
	case "none":
		if in.DiscountPercent != 0 || in.FixedCents != 0 {
			return &store.Conflict{Code: "invalid_customer_product_discount", Message: "清除折扣参数无效"}
		}
	default:
		return &store.Conflict{Code: "invalid_customer_product_discount", Message: "折扣类型无效"}
	}
	return nil
}

func (s *Server) adminCustomerProductDiscounts(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit, offset := pagination(r)
	page, err := s.db.AdminCustomerProductDiscountsPage(r.Context(), q, limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, page)
}

func (s *Server) saveCustomerProductDiscount(w http.ResponseWriter, r *http.Request) {
	var in customerProductDiscountInput
	if !decode(w, r, &in) {
		return
	}
	if err := validateCustomerProductDiscount(&in); err != nil {
		s.fail(w, r, err)
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	result, err := s.db.SaveCustomerProductDiscount(requestFileContext(r), identityFrom(r).ID, key, in)
	if err != nil {
		s.failCustomerPricing(w, r, err)
		return
	}
	write(w, http.StatusOK, result)
}

func (s *Server) failCustomerPricing(w http.ResponseWriter, r *http.Request, err error) {
	var missing *store.CustomerPricingResourceNotFound
	if errors.As(err, &missing) {
		writeError(w, http.StatusNotFound, "not_found", missing.Error())
		return
	}
	s.fail(w, r, err)
}
