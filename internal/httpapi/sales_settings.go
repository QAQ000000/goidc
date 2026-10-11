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
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(in)
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if replay, replayed, e := loadSettingReplayTx(r, tx, actor, "sales.settings", key, hash); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		_ = tx.Commit(r.Context())
		write(w, http.StatusOK, replay)
		return
	}
	var current int64
	if err = tx.QueryRow(r.Context(), "SELECT version FROM sales_settings WHERE id=1 FOR UPDATE").Scan(&current); err != nil {
		s.fail(w, r, err)
		return
	}
	if current != in.ExpectedVersion {
		writeError(w, http.StatusConflict, "version_conflict", "销售设置已变更，请重新加载")
		return
	}
	var version int64
	if err = tx.QueryRow(r.Context(), `UPDATE sales_settings SET order_assignment=$1,registration_assignment=$2,version=version+1,updated_at=now()
WHERE id=1 RETURNING version`, in.OrderAssignment, in.RegistrationAssignment).Scan(&version); err != nil {
		s.fail(w, r, err)
		return
	}
	result := map[string]any{"id": 1, "version": version}
	if err = store.Audit(r.Context(), tx, actor, "sales.settings_updated", "sales_settings", 1, map[string]any{
		"order_assignment": in.OrderAssignment, "registration_assignment": in.RegistrationAssignment, "previous_version": current,
	}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = saveSettingOperationTx(r, tx, actor, "sales.settings", key, hash, result); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, result)
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
	var total int64
	if err := s.db.Pool.QueryRow(r.Context(), `SELECT count(*) FROM sales_commission_groups WHERE ($1='' OR name ILIKE '%'||$1||'%') AND ($2='' OR active=($2='true'))`, q, active).Scan(&total); err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), `SELECT id,name,purchase_rate_bps,renewal_rate_bps,upgrade_rate_bps,include_renewal,include_upgrade,active,version,created_at,updated_at
FROM sales_commission_groups WHERE ($1='' OR name ILIKE '%'||$1||'%') AND ($2='' OR active=($2='true')) ORDER BY id LIMIT $3 OFFSET $4`, q, active, limit, offset)
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
	write(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
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
	key, ok := settingMutationKey(w, r)
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
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if replay, replayed, e := loadSettingReplayTx(r, tx, actor, operation, key, hash); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		_ = tx.Commit(r.Context())
		write(w, http.StatusOK, replay)
		return
	}
	var version int64
	if created {
		err = tx.QueryRow(r.Context(), `INSERT INTO sales_commission_groups(name,purchase_rate_bps,renewal_rate_bps,upgrade_rate_bps,include_renewal,include_upgrade,active)
VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id,version`, in.Name, in.PurchaseRateBPS, in.RenewalRateBPS, in.UpgradeRateBPS, in.IncludeRenewal, in.IncludeUpgrade, in.Active).Scan(&id, &version)
	} else {
		var current int64
		if err = tx.QueryRow(r.Context(), "SELECT version FROM sales_commission_groups WHERE id=$1 FOR UPDATE", id).Scan(&current); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "not_found", "提成组不存在")
				return
			}
			s.fail(w, r, err)
			return
		}
		if current != in.ExpectedVersion {
			writeError(w, http.StatusConflict, "version_conflict", "提成组已变更，请重新加载")
			return
		}
		err = tx.QueryRow(r.Context(), `UPDATE sales_commission_groups SET name=$1,purchase_rate_bps=$2,renewal_rate_bps=$3,upgrade_rate_bps=$4,include_renewal=$5,include_upgrade=$6,active=$7,version=version+1,updated_at=now()
WHERE id=$8 RETURNING version`, in.Name, in.PurchaseRateBPS, in.RenewalRateBPS, in.UpgradeRateBPS, in.IncludeRenewal, in.IncludeUpgrade, in.Active, id).Scan(&version)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	result := map[string]any{"id": id, "version": version}
	action := "sales.commission_group.created"
	if !created {
		action = "sales.commission_group.updated"
	}
	if err = store.Audit(r.Context(), tx, actor, action, "sales_commission_group", id, in); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = saveSettingOperationTx(r, tx, actor, operation, key, hash, result); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[created], result)
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
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	operation := "sales.commission_group.delete:" + strconv.FormatInt(id, 10)
	hash := settingMutationHash(struct{ ID, Version int64 }{id, in.ExpectedVersion})
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if replay, replayed, e := loadSettingReplayTx(r, tx, actor, operation, key, hash); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		_ = tx.Commit(r.Context())
		write(w, http.StatusOK, replay)
		return
	}
	var current int64
	if err = tx.QueryRow(r.Context(), "SELECT version FROM sales_commission_groups WHERE id=$1 FOR UPDATE", id).Scan(&current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "提成组不存在")
			return
		}
		s.fail(w, r, err)
		return
	}
	if current != in.ExpectedVersion {
		writeError(w, http.StatusConflict, "version_conflict", "提成组已变更，请重新加载")
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE users SET sales_commission_group_id=NULL WHERE sales_commission_group_id=$1", id); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM sales_commission_groups WHERE id=$1", id); err != nil {
		s.fail(w, r, err)
		return
	}
	result := map[string]any{"id": id, "deleted": true}
	if err = store.Audit(r.Context(), tx, actor, "sales.commission_group.deleted", "sales_commission_group", id, map[string]any{"expected_version": in.ExpectedVersion}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = saveSettingOperationTx(r, tx, actor, operation, key, hash, result); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, result)
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
	var total int64
	if err := s.db.Pool.QueryRow(r.Context(), "SELECT count(*) FROM sales_commission_tiers").Scan(&total); err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), `SELECT id,threshold_cents,extra_rate_bps,active,version,created_at,updated_at
FROM sales_commission_tiers ORDER BY threshold_cents,id LIMIT $1 OFFSET $2`, limit, offset)
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
	write(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
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
	key, ok := settingMutationKey(w, r)
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
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if replay, replayed, e := loadSettingReplayTx(r, tx, actor, operation, key, hash); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		_ = tx.Commit(r.Context())
		write(w, http.StatusOK, replay)
		return
	}
	var version int64
	if created {
		err = tx.QueryRow(r.Context(), "INSERT INTO sales_commission_tiers(threshold_cents,extra_rate_bps,active) VALUES($1,$2,$3) RETURNING id,version", in.ThresholdCents, in.ExtraRateBPS, in.Active).Scan(&id, &version)
	} else {
		var current int64
		if err = tx.QueryRow(r.Context(), "SELECT version FROM sales_commission_tiers WHERE id=$1 FOR UPDATE", id).Scan(&current); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "not_found", "阶梯规则不存在")
				return
			}
			s.fail(w, r, err)
			return
		}
		if current != in.ExpectedVersion {
			writeError(w, http.StatusConflict, "version_conflict", "阶梯规则已变更，请重新加载")
			return
		}
		err = tx.QueryRow(r.Context(), `UPDATE sales_commission_tiers SET threshold_cents=$1,extra_rate_bps=$2,active=$3,version=version+1,updated_at=now() WHERE id=$4 RETURNING version`, in.ThresholdCents, in.ExtraRateBPS, in.Active, id).Scan(&version)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	result := map[string]any{"id": id, "version": version}
	action := "sales.commission_tier.created"
	if !created {
		action = "sales.commission_tier.updated"
	}
	if err = store.Audit(r.Context(), tx, actor, action, "sales_commission_tier", id, in); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = saveSettingOperationTx(r, tx, actor, operation, key, hash, result); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[created], result)
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
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	operation := "sales.commission_tier.delete:" + strconv.FormatInt(id, 10)
	hash := settingMutationHash(struct{ ID, Version int64 }{id, in.ExpectedVersion})
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if replay, replayed, e := loadSettingReplayTx(r, tx, actor, operation, key, hash); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		_ = tx.Commit(r.Context())
		write(w, http.StatusOK, replay)
		return
	}
	var current int64
	if err = tx.QueryRow(r.Context(), "SELECT version FROM sales_commission_tiers WHERE id=$1 FOR UPDATE", id).Scan(&current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "阶梯规则不存在")
			return
		}
		s.fail(w, r, err)
		return
	}
	if current != in.ExpectedVersion {
		writeError(w, http.StatusConflict, "version_conflict", "阶梯规则已变更，请重新加载")
		return
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM sales_commission_tiers WHERE id=$1", id); err != nil {
		s.fail(w, r, err)
		return
	}
	result := map[string]any{"id": id, "deleted": true}
	if err = store.Audit(r.Context(), tx, actor, "sales.commission_tier.deleted", "sales_commission_tier", id, map[string]any{"expected_version": in.ExpectedVersion}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = saveSettingOperationTx(r, tx, actor, operation, key, hash, result); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, result)
}

func (s *Server) salesStaff(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	var total int64
	if err := s.db.Pool.QueryRow(r.Context(), `SELECT count(*) FROM users WHERE role IN ('admin','super_admin') AND ($1='' OR username ILIKE '%'||$1||'%' OR display_name ILIKE '%'||$1||'%' OR email ILIKE '%'||$1||'%')`, q).Scan(&total); err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), `SELECT u.id,u.display_name,u.email,u.username,u.role,u.status,u.sales_enabled,u.customer_scope,u.customer_ownerless,u.sales_commission_group_id,u.admin_version,u.updated_at,COALESCE(g.name,'') AS sales_commission_group_name
FROM users u LEFT JOIN sales_commission_groups g ON g.id=u.sales_commission_group_id
WHERE u.role IN ('admin','super_admin') AND ($1='' OR u.username ILIKE '%'||$1||'%' OR u.display_name ILIKE '%'||$1||'%' OR u.email ILIKE '%'||$1||'%') ORDER BY u.id LIMIT $2 OFFSET $3`, q, limit, offset)
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
	write(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

type salesStaffInput struct {
	SalesEnabled           *bool  `json:"sales_enabled"`
	CustomerScope          string `json:"customer_scope"`
	CustomerOwnerless      *bool  `json:"customer_ownerless"`
	SalesCommissionGroupID *int64 `json:"sales_commission_group_id"`
	ExpectedVersion        int64  `json:"expected_version"`
}

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
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	operation := "sales.staff.update:" + strconv.FormatInt(id, 10)
	hash := settingMutationHash(struct {
		ID int64
		In salesStaffInput
	}{id, in})
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if replay, replayed, e := loadSettingReplayTx(r, tx, actor, operation, key, hash); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		_ = tx.Commit(r.Context())
		write(w, http.StatusOK, replay)
		return
	}
	var current int64
	var role string
	if err = tx.QueryRow(r.Context(), "SELECT admin_version,role FROM users WHERE id=$1 AND role IN ('admin','super_admin') FOR UPDATE", id).Scan(&current, &role); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "销售人员不存在")
			return
		}
		s.fail(w, r, err)
		return
	}
	if current != in.ExpectedVersion {
		writeError(w, http.StatusConflict, "version_conflict", "销售人员资料已变更，请重新加载")
		return
	}
	if in.SalesCommissionGroupID != nil {
		var exists bool
		if err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM sales_commission_groups WHERE id=$1)", *in.SalesCommissionGroupID).Scan(&exists); err != nil {
			s.fail(w, r, err)
			return
		}
		if !exists {
			writeError(w, http.StatusBadRequest, "invalid_sales_staff", "提成组不存在")
			return
		}
	}
	if in.SalesEnabled == nil {
		in.SalesEnabled = boolPtr(false)
	}
	if in.CustomerOwnerless == nil {
		in.CustomerOwnerless = boolPtr(false)
	}
	if in.CustomerScope == "" {
		in.CustomerScope = "owned"
	}
	var version int64
	err = tx.QueryRow(r.Context(), `UPDATE users SET sales_enabled=$1,customer_scope=$2,customer_ownerless=$3,sales_commission_group_id=$4,admin_version=admin_version+1,updated_at=now()
WHERE id=$5 RETURNING admin_version`, *in.SalesEnabled, in.CustomerScope, *in.CustomerOwnerless, in.SalesCommissionGroupID, id).Scan(&version)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	result := map[string]any{"id": id, "version": version}
	if err = store.Audit(r.Context(), tx, actor, "sales.staff_updated", "user", id, map[string]any{"sales_enabled": *in.SalesEnabled, "customer_scope": in.CustomerScope, "customer_ownerless": *in.CustomerOwnerless, "sales_commission_group_id": in.SalesCommissionGroupID, "previous_version": current, "role": role}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = saveSettingOperationTx(r, tx, actor, operation, key, hash, result); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, result)
}
