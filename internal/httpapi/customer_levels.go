package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

type customerLevelInput struct {
	Name                  string `json:"name"`
	MinIncomeCents        int64  `json:"min_income_cents"`
	MinPurchaseCount      int    `json:"min_purchase_count"`
	MinLoginCount         int    `json:"min_login_count"`
	MinRecentLoginCount   int    `json:"min_recent_login_count"`
	MinRenewalCount       int    `json:"min_renewal_count"`
	MinRecentRenewalCount int    `json:"min_recent_renewal_count"`
	SortOrder             int    `json:"sort_order"`
	Active                bool   `json:"active"`
	ExpectedVersion       int64  `json:"expected_version"`
}

func validateCustomerLevel(in *customerLevelInput, create bool) bool {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 120 || in.MinIncomeCents < 0 || in.MinPurchaseCount < 0 || in.MinLoginCount < 0 || in.MinRecentLoginCount < 0 || in.MinRenewalCount < 0 || in.MinRecentRenewalCount < 0 || in.SortOrder < -100000 || in.SortOrder > 100000 {
		return false
	}
	if create {
		return in.ExpectedVersion == 0
	}
	return in.ExpectedVersion > 0
}

func customerLevelProjection() string {
	return `cl.id,cl.name,cl.min_income_cents,cl.min_purchase_count,cl.min_login_count,cl.min_recent_login_count,
cl.min_renewal_count,cl.min_recent_renewal_count,cl.sort_order,cl.active,cl.version,cl.created_at,cl.updated_at,
(SELECT count(*) FROM users u WHERE u.role='customer' AND u.status='active' AND admin_customer_allowed($1,u.id)
 AND (SELECT coalesce(sum(i.amount_cents),0) FROM invoices i WHERE i.user_id=u.id AND i.status='paid') >= cl.min_income_cents
 AND (SELECT coalesce(sum(oi.quantity),0) FROM order_items oi JOIN orders o ON o.id=oi.order_id WHERE o.user_id=u.id AND o.status IN ('paid','active')) >= cl.min_purchase_count
 AND (SELECT count(*) FROM audit_events a WHERE a.actor_id=u.id AND a.action='auth.login_succeeded') >= cl.min_login_count
 AND (SELECT count(*) FROM audit_events a WHERE a.actor_id=u.id AND a.action='auth.login_succeeded' AND a.created_at >= now()-interval '30 days') >= cl.min_recent_login_count
 AND (SELECT count(*) FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id WHERE i.user_id=u.id AND i.status='paid' AND ii.item_type='renew') >= cl.min_renewal_count
 AND (SELECT count(*) FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id WHERE i.user_id=u.id AND i.status='paid' AND ii.item_type='renew' AND i.paid_at >= now()-interval '30 days') >= cl.min_recent_renewal_count) AS matching_customer_count`
}

func (s *Server) adminCustomerLevels(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	active := strings.TrimSpace(r.URL.Query().Get("active"))
	if active != "" && active != "true" && active != "false" {
		writeError(w, http.StatusBadRequest, "invalid_active", "启用状态无效")
		return
	}
	where := ` FROM customer_levels cl WHERE ($2='' OR cl.name ILIKE '%'||$2||'%') AND ($3='' OR cl.active=($3='true'))`
	var total int64
	if err := s.db.Pool.QueryRow(r.Context(), "SELECT count(*) FROM customer_levels cl WHERE ($1='' OR cl.name ILIKE '%'||$1||'%') AND ($2='' OR cl.active=($2='true'))", q, active).Scan(&total); err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), "SELECT "+customerLevelProjection()+where+" ORDER BY cl.sort_order,cl.id LIMIT $4 OFFSET $5", identityFrom(r).ID, q, active, limit, offset)
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
	write(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset, "recent_window_days": 30})
}

func (s *Server) createCustomerLevel(w http.ResponseWriter, r *http.Request) {
	var in customerLevelInput
	if !decode(w, r, &in) {
		return
	}
	if !validateCustomerLevel(&in, true) {
		writeError(w, http.StatusBadRequest, "invalid_customer_level", "客户等级规则无效")
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
		if replay, replayed, e := loadSettingReplayTx(r, tx, actor, "customer_level.create", key, hash); e != nil {
			return txResponse{}, e
		} else if replayed {

			return txResponse{http.StatusOK, replay}, nil
		}
		var id int64
		err = tx.QueryRow(r.Context(), `INSERT INTO customer_levels(name,min_income_cents,min_purchase_count,min_login_count,min_recent_login_count,min_renewal_count,min_recent_renewal_count,sort_order,active) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`, in.Name, in.MinIncomeCents, in.MinPurchaseCount, in.MinLoginCount, in.MinRecentLoginCount, in.MinRenewalCount, in.MinRecentRenewalCount, in.SortOrder, in.Active).Scan(&id)
		if err != nil {
			return txResponse{}, err
		}
		result := map[string]any{"id": id, "version": int64(1)}
		if err = store.Audit(r.Context(), tx, actor, "customer_level.created", "customer_level", id, map[string]any{"name": in.Name}); err != nil {
			return txResponse{}, err
		}
		if err = saveSettingOperationTx(r, tx, actor, "customer_level.create", key, hash, result); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusCreated, result}, nil
	})
}

func (s *Server) updateCustomerLevel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in customerLevelInput
	if !decode(w, r, &in) {
		return
	}
	if !validateCustomerLevel(&in, false) {
		writeError(w, http.StatusBadRequest, "invalid_customer_level", "客户等级规则或版本无效")
		return
	}
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(struct {
		ID    int64
		Input customerLevelInput
	}{id, in})
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		operation := "customer_level.update:" + strconv.FormatInt(id, 10)
		if replay, replayed, e := loadSettingReplayTx(r, tx, actor, operation, key, hash); e != nil {
			return txResponse{}, e
		} else if replayed {

			return txResponse{http.StatusOK, replay}, nil
		}
		var version int64
		if err = tx.QueryRow(r.Context(), "SELECT version FROM customer_levels WHERE id=$1 FOR UPDATE", id).Scan(&version); err != nil {
			if err == pgx.ErrNoRows {
				return txResponse{}, &txHTTPError{http.StatusNotFound, "not_found", "客户等级不存在"}
			}
			return txResponse{}, err
		}
		if version != in.ExpectedVersion {
			return txResponse{}, &txHTTPError{http.StatusConflict, "version_conflict", "客户等级已变更，请刷新后重试"}
		}
		_, err = tx.Exec(r.Context(), `UPDATE customer_levels SET name=$2,min_income_cents=$3,min_purchase_count=$4,min_login_count=$5,min_recent_login_count=$6,min_renewal_count=$7,min_recent_renewal_count=$8,sort_order=$9,active=$10,version=version+1,updated_at=now() WHERE id=$1`, id, in.Name, in.MinIncomeCents, in.MinPurchaseCount, in.MinLoginCount, in.MinRecentLoginCount, in.MinRenewalCount, in.MinRecentRenewalCount, in.SortOrder, in.Active)
		if err != nil {
			return txResponse{}, err
		}
		result := map[string]any{"id": id, "version": version + 1}
		if err = store.Audit(r.Context(), tx, actor, "customer_level.updated", "customer_level", id, map[string]any{"expected_version": in.ExpectedVersion}); err != nil {
			return txResponse{}, err
		}
		if err = saveSettingOperationTx(r, tx, actor, operation, key, hash, result); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusOK, result}, nil
	})
}

func (s *Server) deleteCustomerLevel(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, http.StatusBadRequest, "invalid_customer_level", "版本无效")
		return
	}
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(struct{ ID, Version int64 }{id, in.ExpectedVersion})
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		operation := "customer_level.delete:" + strconv.FormatInt(id, 10)
		if replay, replayed, e := loadSettingReplayTx(r, tx, actor, operation, key, hash); e != nil {
			return txResponse{}, e
		} else if replayed {

			return txResponse{http.StatusOK, replay}, nil
		}
		var version int64
		if err = tx.QueryRow(r.Context(), "SELECT version FROM customer_levels WHERE id=$1 FOR UPDATE", id).Scan(&version); err != nil {
			if err == pgx.ErrNoRows {
				return txResponse{}, &txHTTPError{http.StatusNotFound, "not_found", "客户等级不存在"}
			}
			return txResponse{}, err
		}
		if version != in.ExpectedVersion {
			return txResponse{}, &txHTTPError{http.StatusConflict, "version_conflict", "客户等级已变更，请刷新后重试"}
		}
		if _, err = tx.Exec(r.Context(), "DELETE FROM customer_levels WHERE id=$1", id); err != nil {
			return txResponse{}, err
		}
		result := map[string]any{"id": id, "deleted": true}
		if err = store.Audit(r.Context(), tx, actor, "customer_level.deleted", "customer_level", id, map[string]any{"expected_version": in.ExpectedVersion}); err != nil {
			return txResponse{}, err
		}
		if err = saveSettingOperationTx(r, tx, actor, operation, key, hash, result); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusOK, result}, nil
	})
}
