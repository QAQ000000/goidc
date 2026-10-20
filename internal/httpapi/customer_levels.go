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

func (s *Server) adminCustomerLevels(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	active := strings.TrimSpace(r.URL.Query().Get("active"))
	if active != "" && active != "true" && active != "false" {
		writeError(w, http.StatusBadRequest, "invalid_active", "启用状态无效")
		return
	}
	page, err := s.db.AdminCustomerLevels(r.Context(), identityFrom(r).ID, q, active, limit, offset)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, page)
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
	key, ok := mutationKey(w, r, "", true)
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
	key, ok := mutationKey(w, r, "", true)
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
		_, err = store.UpdateVersioned(r.Context(), tx, "customer_levels", `name=$1,min_income_cents=$2,min_purchase_count=$3,min_login_count=$4,min_recent_login_count=$5,min_renewal_count=$6,min_recent_renewal_count=$7,sort_order=$8,active=$9`, id, in.ExpectedVersion, in.Name, in.MinIncomeCents, in.MinPurchaseCount, in.MinLoginCount, in.MinRecentLoginCount, in.MinRenewalCount, in.MinRecentRenewalCount, in.SortOrder, in.Active)
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
	key, ok := mutationKey(w, r, "", true)
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
