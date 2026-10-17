package httpapi

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

type customerProductGroupInput struct {
	Name            string  `json:"name"`
	ProductIDs      []int64 `json:"product_ids"`
	ExpectedVersion int64   `json:"expected_version"`
}

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
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(in)
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		if replay, replayed, e := loadSettingReplayTx(r, tx, actor, "customer_product_group.create", key, hash); e != nil {
			return txResponse{}, e
		} else if replayed {

			return txResponse{http.StatusOK, replay}, nil
		}
		var id, version int64
		if err = tx.QueryRow(r.Context(), "INSERT INTO customer_product_groups(name) VALUES($1) RETURNING id,version", in.Name).Scan(&id, &version); err != nil {
			return txResponse{}, err
		}
		if err = ensureCustomerProductGroupProducts(r, tx, id, in.ProductIDs); err != nil {
			return txResponse{}, err
		}
		result := map[string]any{"id": id, "version": version}
		if err = store.Audit(r.Context(), tx, actor, "customer_product_group.created", "customer_product_group", id, map[string]any{"name": in.Name, "product_ids": in.ProductIDs}); err != nil {
			return txResponse{}, err
		}
		if err = saveSettingOperationTx(r, tx, actor, "customer_product_group.create", key, hash, result); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusCreated, result}, nil
	})
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
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(struct {
		ID    int64
		Input customerProductGroupInput
	}{id, in})
	operation := "customer_product_group.update:" + strconv.FormatInt(id, 10)
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		if replay, replayed, e := loadSettingReplayTx(r, tx, actor, operation, key, hash); e != nil {
			return txResponse{}, e
		} else if replayed {

			return txResponse{http.StatusOK, replay}, nil
		}
		var current int64
		if err = tx.QueryRow(r.Context(), "SELECT version FROM customer_product_groups WHERE id=$1 FOR UPDATE", id).Scan(&current); err != nil {
			if err == pgx.ErrNoRows {
				return txResponse{}, &txHTTPError{http.StatusNotFound, "not_found", "商品分组不存在"}
			}
			return txResponse{}, err
		}
		if current != in.ExpectedVersion {
			return txResponse{}, &txHTTPError{http.StatusConflict, "version_conflict", "商品分组已变更，请刷新后重试"}
		}
		if _, err = tx.Exec(r.Context(), "UPDATE customer_product_groups SET name=$2,version=version+1,updated_at=now() WHERE id=$1", id, in.Name); err != nil {
			return txResponse{}, err
		}
		if err = replaceCustomerProductGroupProducts(r, tx, id, in.ProductIDs); err != nil {
			return txResponse{}, err
		}
		result := map[string]any{"id": id, "version": current + 1}
		if err = store.Audit(r.Context(), tx, actor, "customer_product_group.updated", "customer_product_group", id, map[string]any{"expected_version": in.ExpectedVersion, "product_ids": in.ProductIDs}); err != nil {
			return txResponse{}, err
		}
		if err = saveSettingOperationTx(r, tx, actor, operation, key, hash, result); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusOK, result}, nil
	})
}

func (s *Server) deleteCustomerProductGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedVersion < 1 {
		writeError(w, http.StatusBadRequest, "expected_version_required", "请提供有效版本")
		return
	}
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(struct {
		ID    int64
		Input any
	}{id, in})
	operation := "customer_product_group.delete:" + strconv.FormatInt(id, 10)
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		if replay, replayed, e := loadSettingReplayTx(r, tx, actor, operation, key, hash); e != nil {
			return txResponse{}, e
		} else if replayed {

			return txResponse{http.StatusOK, replay}, nil
		}
		var current int64
		if err = tx.QueryRow(r.Context(), "SELECT version FROM customer_product_groups WHERE id=$1 FOR UPDATE", id).Scan(&current); err != nil {
			if err == pgx.ErrNoRows {
				return txResponse{}, &txHTTPError{http.StatusNotFound, "not_found", "商品分组不存在"}
			}
			return txResponse{}, err
		}
		if current != in.ExpectedVersion {
			return txResponse{}, &txHTTPError{http.StatusConflict, "version_conflict", "商品分组已变更，请刷新后重试"}
		}
		var deleted int64
		if err = tx.QueryRow(r.Context(), "DELETE FROM customer_product_groups WHERE id=$1 RETURNING id", id).Scan(&deleted); err != nil {
			return txResponse{}, err
		}
		result := map[string]any{"id": deleted, "deleted": true}
		if err = store.Audit(r.Context(), tx, actor, "customer_product_group.deleted", "customer_product_group", id, map[string]any{"expected_version": in.ExpectedVersion}); err != nil {
			return txResponse{}, err
		}
		if err = saveSettingOperationTx(r, tx, actor, operation, key, hash, result); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusOK, result}, nil
	})
}

func ensureCustomerProductGroupProducts(r *http.Request, tx pgx.Tx, groupID int64, productIDs []int64) error {
	if len(productIDs) == 0 {
		return nil
	}
	var count int
	if err := tx.QueryRow(r.Context(), "SELECT count(*) FROM products WHERE id=ANY($1)", productIDs).Scan(&count); err != nil {
		return err
	}
	if count != len(productIDs) {
		return &store.Conflict{Code: "invalid_product", Message: "商品分组包含不存在的商品"}
	}
	for _, productID := range productIDs {
		if _, err := tx.Exec(r.Context(), "INSERT INTO customer_product_group_products(group_id,product_id) VALUES($1,$2)", groupID, productID); err != nil {
			return err
		}
	}
	return nil
}

func replaceCustomerProductGroupProducts(r *http.Request, tx pgx.Tx, groupID int64, productIDs []int64) error {
	if _, err := tx.Exec(r.Context(), "DELETE FROM customer_product_group_products WHERE group_id=$1", groupID); err != nil {
		return err
	}
	return ensureCustomerProductGroupProducts(r, tx, groupID, productIDs)
}

type customerProductDiscountInput struct {
	CustomerGroupID int64  `json:"customer_group_id"`
	ProductGroupID  int64  `json:"product_group_id"`
	DiscountType    string `json:"discount_type"`
	DiscountPercent int    `json:"discount_percent"`
	FixedCents      int64  `json:"fixed_discount_cents"`
	ExpectedVersion int64  `json:"expected_version"`
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
	where := `($1='' OR cg.name ILIKE '%'||$1||'%' OR pg.name ILIKE '%'||$1||'%')`
	s.writeListPage(w, r, `SELECT count(*) FROM customer_product_groups pg CROSS JOIN customer_groups cg WHERE `+where,
		`
SELECT d.id, d.customer_group_id, cg.name AS customer_group_name,
       d.product_group_id, pg.name AS product_group_name,
       COALESCE(d.discount_type,'none') AS discount_type,
       COALESCE(d.discount_percent,0) AS discount_percent,
       COALESCE(d.fixed_discount_cents,0) AS fixed_discount_cents,
       COALESCE(d.version,0) AS version
FROM customer_product_groups pg
CROSS JOIN customer_groups cg
LEFT JOIN customer_product_group_discounts d ON d.product_group_id=pg.id AND d.customer_group_id=cg.id
WHERE `+where+`
ORDER BY pg.id,cg.id`, []any{q}, limit, offset)
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
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(in)
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		operation := "customer_product_discount.save"
		if replay, replayed, e := loadSettingReplayTx(r, tx, actor, operation, key, hash); e != nil {
			return txResponse{}, e
		} else if replayed {

			return txResponse{http.StatusOK, replay}, nil
		}
		var exists int
		if err = tx.QueryRow(r.Context(), "SELECT count(*) FROM customer_groups WHERE id=$1", in.CustomerGroupID).Scan(&exists); err != nil {
			return txResponse{}, err
		}
		if exists == 0 {
			return txResponse{}, &txHTTPError{http.StatusNotFound, "not_found", "客户分组不存在"}
		}
		if err = tx.QueryRow(r.Context(), "SELECT count(*) FROM customer_product_groups WHERE id=$1", in.ProductGroupID).Scan(&exists); err != nil {
			return txResponse{}, err
		}
		if exists == 0 {
			return txResponse{}, &txHTTPError{http.StatusNotFound, "not_found", "商品分组不存在"}
		}
		var id, current int64
		err = tx.QueryRow(r.Context(), "SELECT id,version FROM customer_product_group_discounts WHERE customer_group_id=$1 AND product_group_id=$2 FOR UPDATE", in.CustomerGroupID, in.ProductGroupID).Scan(&id, &current)
		hasExisting := err == nil
		if err != nil && err != pgx.ErrNoRows {
			return txResponse{}, err
		}
		if hasExisting && current != in.ExpectedVersion {
			return txResponse{}, &txHTTPError{http.StatusConflict, "version_conflict", "折扣已变更，请刷新后重试"}
		}
		if !hasExisting && in.ExpectedVersion != 0 {
			return txResponse{}, &txHTTPError{http.StatusConflict, "version_conflict", "折扣已变更，请刷新后重试"}
		}
		var result map[string]any
		if in.DiscountType == "none" {
			if hasExisting {
				if _, err = tx.Exec(r.Context(), "DELETE FROM customer_product_group_discounts WHERE id=$1", id); err != nil {
					return txResponse{}, err
				}
			}
			result = map[string]any{"id": id, "customer_group_id": in.CustomerGroupID, "product_group_id": in.ProductGroupID, "discount_type": "none", "version": 0, "deleted": hasExisting}
		} else if hasExisting {
			next := current + 1
			if _, err = tx.Exec(r.Context(), `UPDATE customer_product_group_discounts SET discount_type=$2,discount_percent=$3,fixed_discount_cents=$4,version=$5,updated_at=now() WHERE id=$1`, id, in.DiscountType, in.DiscountPercent, in.FixedCents, next); err != nil {
				return txResponse{}, err
			}
			result = map[string]any{"id": id, "customer_group_id": in.CustomerGroupID, "product_group_id": in.ProductGroupID, "discount_type": in.DiscountType, "version": next}
		} else {
			if err = tx.QueryRow(r.Context(), `INSERT INTO customer_product_group_discounts(customer_group_id,product_group_id,discount_type,discount_percent,fixed_discount_cents) VALUES($1,$2,$3,$4,$5) RETURNING id,version`, in.CustomerGroupID, in.ProductGroupID, in.DiscountType, in.DiscountPercent, in.FixedCents).Scan(&id, &current); err != nil {
				return txResponse{}, err
			}
			result = map[string]any{"id": id, "customer_group_id": in.CustomerGroupID, "product_group_id": in.ProductGroupID, "discount_type": in.DiscountType, "version": current}
		}
		if err = store.Audit(r.Context(), tx, actor, "customer_product_discount.saved", "customer_product_group_discount", id, map[string]any{"customer_group_id": in.CustomerGroupID, "product_group_id": in.ProductGroupID, "discount_type": in.DiscountType, "expected_version": in.ExpectedVersion}); err != nil {
			return txResponse{}, err
		}
		if err = saveSettingOperationTx(r, tx, actor, operation, key, hash, result); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusOK, result}, nil
	})
}
