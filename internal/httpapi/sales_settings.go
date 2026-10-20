package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

var salesAssignmentModes = map[string]bool{
	"none": true, "customer_choice": true, "random": true, "round_robin": true,
}

type salesSettingsInput struct {
	OrderAssignment        string `json:"order_assignment"`
	RegistrationAssignment string `json:"registration_assignment"`
	ExpectedVersion        int64  `json:"expected_version"`
}

func validateSalesSettings(in *salesSettingsInput) error {
	in.OrderAssignment = strings.TrimSpace(in.OrderAssignment)
	in.RegistrationAssignment = strings.TrimSpace(in.RegistrationAssignment)
	if !salesAssignmentModes[in.OrderAssignment] || !salesAssignmentModes[in.RegistrationAssignment] || in.ExpectedVersion < 1 {
		return &store.Conflict{Code: "invalid_sales_settings", Message: "销售分配设置无效"}
	}
	return nil
}

func (s *Server) salesSettings(w http.ResponseWriter, r *http.Request) {
	var id, version int64
	var orderAssignment, registrationAssignment string
	var updatedAt any
	if err := s.db.Pool.QueryRow(r.Context(), `SELECT id,order_assignment,registration_assignment,version,updated_at
FROM sales_settings WHERE id=1`).Scan(&id, &orderAssignment, &registrationAssignment, &version, &updatedAt); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{
		"id": id, "order_assignment": orderAssignment, "registration_assignment": registrationAssignment,
		"version": version, "updated_at": updatedAt,
	})
}

func (s *Server) updateSalesSettings(w http.ResponseWriter, r *http.Request) {
	var in salesSettingsInput
	if !decode(w, r, &in) {
		return
	}
	if err := validateSalesSettings(&in); err != nil {
		s.fail(w, r, err)
		return
	}
	s.updateSingletonSettings(w, r, settingsUpdate{
		action: "sales.settings_updated", entity: "sales_settings", operation: "sales.settings", request: in,
		apply: func(tx pgx.Tx) (any, any, error) {
			next, err := updateSettingsVersion(r, tx, "sales_settings", in.ExpectedVersion, "销售设置已变更，请重新加载",
				`UPDATE sales_settings SET order_assignment=$1,registration_assignment=$2,version=version+1,updated_at=now() WHERE id=1 RETURNING version`, in.OrderAssignment, in.RegistrationAssignment)
			return map[string]any{"id": 1, "version": next}, map[string]any{
				"order_assignment": in.OrderAssignment, "registration_assignment": in.RegistrationAssignment, "previous_version": in.ExpectedVersion,
			}, err
		},
	})
}

type salesCommissionGroupInput struct {
	Name            string `json:"name"`
	PurchaseRateBPS int    `json:"purchase_rate_bps"`
	RenewalRateBPS  int    `json:"renewal_rate_bps"`
	UpgradeRateBPS  int    `json:"upgrade_rate_bps"`
	IncludeRenewal  bool   `json:"include_renewal"`
	IncludeUpgrade  bool   `json:"include_upgrade"`
	Active          bool   `json:"active"`
	ExpectedVersion int64  `json:"expected_version"`
}

func validateSalesCommissionGroup(in *salesCommissionGroupInput, create bool) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 120 || in.PurchaseRateBPS < 0 || in.PurchaseRateBPS > 10000 || in.RenewalRateBPS < 0 || in.RenewalRateBPS > 10000 || in.UpgradeRateBPS < 0 || in.UpgradeRateBPS > 10000 {
		return &store.Conflict{Code: "invalid_sales_commission_group", Message: "提成组名称或比例无效，比例应为 0 到 10000 基点"}
	}
	if create && in.ExpectedVersion != 0 || !create && in.ExpectedVersion < 1 {
		return &store.Conflict{Code: "invalid_sales_commission_group", Message: "提成组版本无效"}
	}
	return nil
}

func (s *Server) salesCommissionGroups(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	active := strings.TrimSpace(r.URL.Query().Get("active"))
	if active != "" && active != "true" && active != "false" {
		writeError(w, http.StatusBadRequest, "invalid_active", "启用状态无效")
		return
	}
	s.writeListPage(w, r, `SELECT count(*) FROM sales_commission_groups WHERE ($1='' OR name ILIKE '%'||$1||'%') AND ($2='' OR active=($2='true'))`,
		`SELECT id,name,purchase_rate_bps,renewal_rate_bps,upgrade_rate_bps,include_renewal,include_upgrade,active,version,created_at,updated_at
FROM sales_commission_groups WHERE ($1='' OR name ILIKE '%'||$1||'%') AND ($2='' OR active=($2='true')) ORDER BY id`, []any{q, active}, limit, offset)
}

func (s *Server) saveSalesCommissionGroup(w http.ResponseWriter, r *http.Request) {
	var in salesCommissionGroupInput
	if !decode(w, r, &in) {
		return
	}
	created := r.Method == http.MethodPost
	if err := validateSalesCommissionGroup(&in, created); err != nil {
		s.fail(w, r, err)
		return
	}
	id := int64(0)
	if !created {
		var ok bool
		id, ok = pathID(w, r)
		if !ok {
			return
		}
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(struct {
		ID int64
		In salesCommissionGroupInput
	}{id, in})
	operation := "sales.commission_group.create"
	if !created {
		operation = "sales.commission_group.update:" + strconv.FormatInt(id, 10)
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		if replay, replayed, e := loadSettingReplayTx(r, tx, actor, operation, key, hash); e != nil {
			return txResponse{}, e
		} else if replayed {

			return txResponse{http.StatusOK, replay}, nil
		}
		var version int64
		if created {
			err = tx.QueryRow(r.Context(), `INSERT INTO sales_commission_groups(name,purchase_rate_bps,renewal_rate_bps,upgrade_rate_bps,include_renewal,include_upgrade,active)
VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id,version`, in.Name, in.PurchaseRateBPS, in.RenewalRateBPS, in.UpgradeRateBPS, in.IncludeRenewal, in.IncludeUpgrade, in.Active).Scan(&id, &version)
		} else {
			var current int64
			if err = tx.QueryRow(r.Context(), "SELECT version FROM sales_commission_groups WHERE id=$1 FOR UPDATE", id).Scan(&current); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return txResponse{}, &txHTTPError{http.StatusNotFound, "not_found", "提成组不存在"}
				}
				return txResponse{}, err
			}
			if current != in.ExpectedVersion {
				return txResponse{}, &txHTTPError{http.StatusConflict, "version_conflict", "提成组已变更，请重新加载"}
			}
			err = tx.QueryRow(r.Context(), `UPDATE sales_commission_groups SET name=$1,purchase_rate_bps=$2,renewal_rate_bps=$3,upgrade_rate_bps=$4,include_renewal=$5,include_upgrade=$6,active=$7,version=version+1,updated_at=now()
WHERE id=$8 RETURNING version`, in.Name, in.PurchaseRateBPS, in.RenewalRateBPS, in.UpgradeRateBPS, in.IncludeRenewal, in.IncludeUpgrade, in.Active, id).Scan(&version)
		}
		if err != nil {
			return txResponse{}, err
		}
		result := map[string]any{"id": id, "version": version}
		action := "sales.commission_group.created"
		if !created {
			action = "sales.commission_group.updated"
		}
		if err = store.Audit(r.Context(), tx, actor, action, "sales_commission_group", id, in); err != nil {
			return txResponse{}, err
		}
		if err = saveSettingOperationTx(r, tx, actor, operation, key, hash, result); err != nil {
			return txResponse{}, err
		}

		return txResponse{map[bool]int{true: http.StatusCreated, false: http.StatusOK}[created], result}, nil
	})
}

func (s *Server) deleteSalesCommissionGroup(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, http.StatusBadRequest, "invalid_sales_commission_group", "提成组版本无效")
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	operation := "sales.commission_group.delete:" + strconv.FormatInt(id, 10)
	hash := settingMutationHash(struct{ ID, Version int64 }{id, in.ExpectedVersion})
	result, err := s.db.DeleteSalesCommissionGroup(r.Context(), actor, id, in.ExpectedVersion, operation, key, hash)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, 200, result)
}

type salesCommissionTierInput struct {
	ThresholdCents  int64 `json:"threshold_cents"`
	ExtraRateBPS    int   `json:"extra_rate_bps"`
	Active          bool  `json:"active"`
	ExpectedVersion int64 `json:"expected_version"`
}

func validateSalesCommissionTier(in *salesCommissionTierInput, create bool) error {
	if in.ThresholdCents < 0 || in.ExtraRateBPS < 0 || in.ExtraRateBPS > 10000 || (create && in.ExpectedVersion != 0) || (!create && in.ExpectedVersion < 1) {
		return &store.Conflict{Code: "invalid_sales_commission_tier", Message: "阶梯阈值或额外提成比例无效"}
	}
	return nil
}

func (s *Server) salesCommissionTiers(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	s.writeListPage(w, r, "SELECT count(*) FROM sales_commission_tiers",
		`SELECT id,threshold_cents,extra_rate_bps,active,version,created_at,updated_at
FROM sales_commission_tiers ORDER BY threshold_cents,id`, nil, limit, offset)
}

func (s *Server) saveSalesCommissionTier(w http.ResponseWriter, r *http.Request) {
	var in salesCommissionTierInput
	if !decode(w, r, &in) {
		return
	}
	created := r.Method == http.MethodPost
	if err := validateSalesCommissionTier(&in, created); err != nil {
		s.fail(w, r, err)
		return
	}
	id := int64(0)
	if !created {
		var ok bool
		id, ok = pathID(w, r)
		if !ok {
			return
		}
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(struct {
		ID int64
		In salesCommissionTierInput
	}{id, in})
	operation := "sales.commission_tier.create"
	if !created {
		operation = "sales.commission_tier.update:" + strconv.FormatInt(id, 10)
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		if replay, replayed, e := loadSettingReplayTx(r, tx, actor, operation, key, hash); e != nil {
			return txResponse{}, e
		} else if replayed {

			return txResponse{http.StatusOK, replay}, nil
		}
		var version int64
		if created {
			err = tx.QueryRow(r.Context(), "INSERT INTO sales_commission_tiers(threshold_cents,extra_rate_bps,active) VALUES($1,$2,$3) RETURNING id,version", in.ThresholdCents, in.ExtraRateBPS, in.Active).Scan(&id, &version)
		} else {
			var current int64
			if err = tx.QueryRow(r.Context(), "SELECT version FROM sales_commission_tiers WHERE id=$1 FOR UPDATE", id).Scan(&current); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return txResponse{}, &txHTTPError{http.StatusNotFound, "not_found", "阶梯规则不存在"}
				}
				return txResponse{}, err
			}
			if current != in.ExpectedVersion {
				return txResponse{}, &txHTTPError{http.StatusConflict, "version_conflict", "阶梯规则已变更，请重新加载"}
			}
			err = tx.QueryRow(r.Context(), `UPDATE sales_commission_tiers SET threshold_cents=$1,extra_rate_bps=$2,active=$3,version=version+1,updated_at=now() WHERE id=$4 RETURNING version`, in.ThresholdCents, in.ExtraRateBPS, in.Active, id).Scan(&version)
		}
		if err != nil {
			return txResponse{}, err
		}
		result := map[string]any{"id": id, "version": version}
		action := "sales.commission_tier.created"
		if !created {
			action = "sales.commission_tier.updated"
		}
		if err = store.Audit(r.Context(), tx, actor, action, "sales_commission_tier", id, in); err != nil {
			return txResponse{}, err
		}
		if err = saveSettingOperationTx(r, tx, actor, operation, key, hash, result); err != nil {
			return txResponse{}, err
		}

		return txResponse{map[bool]int{true: http.StatusCreated, false: http.StatusOK}[created], result}, nil
	})
}

func (s *Server) deleteSalesCommissionTier(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, http.StatusBadRequest, "invalid_sales_commission_tier", "阶梯规则版本无效")
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	operation := "sales.commission_tier.delete:" + strconv.FormatInt(id, 10)
	hash := settingMutationHash(struct{ ID, Version int64 }{id, in.ExpectedVersion})
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		if replay, replayed, e := loadSettingReplayTx(r, tx, actor, operation, key, hash); e != nil {
			return txResponse{}, e
		} else if replayed {

			return txResponse{http.StatusOK, replay}, nil
		}
		var current int64
		if err = tx.QueryRow(r.Context(), "SELECT version FROM sales_commission_tiers WHERE id=$1 FOR UPDATE", id).Scan(&current); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return txResponse{}, &txHTTPError{http.StatusNotFound, "not_found", "阶梯规则不存在"}
			}
			return txResponse{}, err
		}
		if current != in.ExpectedVersion {
			return txResponse{}, &txHTTPError{http.StatusConflict, "version_conflict", "阶梯规则已变更，请重新加载"}
		}
		if _, err = tx.Exec(r.Context(), "DELETE FROM sales_commission_tiers WHERE id=$1", id); err != nil {
			return txResponse{}, err
		}
		result := map[string]any{"id": id, "deleted": true}
		if err = store.Audit(r.Context(), tx, actor, "sales.commission_tier.deleted", "sales_commission_tier", id, map[string]any{"expected_version": in.ExpectedVersion}); err != nil {
			return txResponse{}, err
		}
		if err = saveSettingOperationTx(r, tx, actor, operation, key, hash, result); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusOK, result}, nil
	})
}

func (s *Server) salesStaff(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	result, err := s.db.SalesStaffPage(r.Context(), q, limit, offset)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, 200, result)
}

type salesStaffInput = store.SalesStaffInput

func (s *Server) updateSalesStaff(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in salesStaffInput
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedVersion < 1 || (in.CustomerScope != "" && in.CustomerScope != "all" && in.CustomerScope != "owned" && in.CustomerScope != "assigned") {
		writeError(w, http.StatusBadRequest, "invalid_sales_staff", "销售人员设置无效")
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	operation := "sales.staff.update:" + strconv.FormatInt(id, 10)
	hash := settingMutationHash(struct {
		ID int64
		In salesStaffInput
	}{id, in})
	result, err := s.db.UpdateSalesStaff(r.Context(), actor, id, in, operation, key, hash)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, 200, result)
}
