package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func settingMutationKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) < 8 || len(key) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "请提供长度为 8 到 128 的幂等键")
		return "", false
	}
	return key, true
}

func settingMutationHash(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// currencies returns the old admin table contract with a stable page shape.
func (s *Server) currencies(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	active := strings.TrimSpace(r.URL.Query().Get("active"))
	where := `($1='' OR code ILIKE '%'||$1||'%' OR name ILIKE '%'||$1||'%') AND ($2='' OR active=($2='true'))`
	rows, err := s.db.Pool.Query(r.Context(), `SELECT id,code,name,prefix,suffix,format,exchange_rate,active,is_default,version,created_at,updated_at FROM currencies WHERE `+where+` ORDER BY is_default DESC,id LIMIT $3 OFFSET $4`, q, active, limit, offset)
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
	var total int64
	var defaultID int64
	if err = s.db.Pool.QueryRow(r.Context(), `SELECT count(*) FROM currencies WHERE `+where, q, active).Scan(&total); err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.db.Pool.QueryRow(r.Context(), `SELECT id FROM currencies WHERE is_default=true`).Scan(&defaultID)
	write(w, 200, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset, "default_currency_id": defaultID})
}

type currencyInput struct {
	Code            string  `json:"code"`
	Name            string  `json:"name"`
	Prefix          string  `json:"prefix"`
	Suffix          string  `json:"suffix"`
	Format          string  `json:"format"`
	ExchangeRate    float64 `json:"exchange_rate"`
	Active          *bool   `json:"active"`
	ExpectedVersion int64   `json:"expected_version"`
}

func validateCurrency(in *currencyInput) error {
	in.Code = strings.ToUpper(strings.TrimSpace(in.Code))
	in.Name = strings.TrimSpace(in.Name)
	in.Prefix = strings.TrimSpace(in.Prefix)
	in.Suffix = strings.TrimSpace(in.Suffix)
	in.Format = strings.TrimSpace(in.Format)
	if len(in.Code) != 3 || in.Name == "" || in.ExchangeRate <= 0 || in.ExchangeRate > 1e9 || !map[string]bool{"prefix_amount": true, "amount_suffix": true, "code_amount": true}[in.Format] {
		return &store.Conflict{Code: "invalid_currency", Message: "货币代码、名称、格式或汇率无效"}
	}
	return nil
}

func (s *Server) saveCurrency(w http.ResponseWriter, r *http.Request) {
	var in currencyInput
	if !decode(w, r, &in) {
		return
	}
	if err := validateCurrency(&in); err != nil {
		s.fail(w, r, err)
		return
	}
	created := r.Method == http.MethodPost
	mutationID := int64(0)
	if !created {
		var ok bool
		mutationID, ok = pathID(w, r)
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
		ID    int64
		Input currencyInput
	}{mutationID, in})
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if replay, replayed, e := loadSettingReplayTx(r, tx, actor, "currency.save", key, hash); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		_ = tx.Commit(r.Context())
		write(w, 200, replay)
		return
	}
	var id, version int64
	if created {
		if in.Active == nil {
			in.Active = boolPtr(true)
		}
		err = tx.QueryRow(r.Context(), `INSERT INTO currencies(code,name,prefix,suffix,format,exchange_rate,active) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id,version`, in.Code, in.Name, in.Prefix, in.Suffix, in.Format, in.ExchangeRate, *in.Active).Scan(&id, &version)
	} else {
		id = mutationID
		if in.ExpectedVersion < 1 {
			writeError(w, 400, "invalid_currency", "请提供有效版本")
			return
		}
		err = tx.QueryRow(r.Context(), `UPDATE currencies SET code=$1,name=$2,prefix=$3,suffix=$4,format=$5,exchange_rate=$6,active=coalesce($7,active),version=version+1,updated_at=now() WHERE id=$8 AND version=$9 RETURNING id,version`, in.Code, in.Name, in.Prefix, in.Suffix, in.Format, in.ExchangeRate, in.Active, id, in.ExpectedVersion).Scan(&id, &version)
	}
	if err != nil {
		if !created && errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "version_conflict", "货币已变更，请重新加载")
			return
		}
		s.fail(w, r, err)
		return
	}
	result := map[string]any{"id": id, "version": version}
	if err = store.Audit(r.Context(), tx, actor, "currency.settings_updated", "currencies", id, in); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = saveSettingOperationTx(r, tx, actor, "currency.save", key, hash, result); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, map[bool]int{true: 201, false: 200}[created], result)
}

func (s *Server) setCurrencyDefault(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, 400, "invalid_currency", "请提供有效版本")
		return
	}
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(struct{ ID, Version int64 }{id, in.ExpectedVersion})
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if replay, replayed, e := loadSettingReplayTx(r, tx, actor, "currency.default", key, hash); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		_ = tx.Commit(r.Context())
		write(w, 200, replay)
		return
	}
	var version int64
	if err = tx.QueryRow(r.Context(), `SELECT version FROM currencies WHERE id=$1 AND active=true FOR UPDATE`, id).Scan(&version); err != nil {
		s.fail(w, r, err)
		return
	}
	if version != in.ExpectedVersion {
		writeError(w, 409, "version_conflict", "货币已变更，请重新加载")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE currencies SET is_default=false,version=version+1,updated_at=now() WHERE is_default=true`); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE currencies SET is_default=true,version=version+1,updated_at=now() WHERE id=$1`, id); err != nil {
		s.fail(w, r, err)
		return
	}
	result := map[string]any{"id": id, "version": version + 1}
	if err = store.Audit(r.Context(), tx, actor, "currency.default_updated", "currencies", id, in); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = saveSettingOperationTx(r, tx, actor, "currency.default", key, hash, result); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, result)
}

func (s *Server) deleteCurrency(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, 400, "invalid_currency", "请提供有效版本")
		return
	}
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(struct{ ID, Version int64 }{id, in.ExpectedVersion})
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if replay, replayed, e := loadSettingReplayTx(r, tx, actor, "currency.delete", key, hash); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		_ = tx.Commit(r.Context())
		write(w, 200, replay)
		return
	}
	var version int64
	var def bool
	if err = tx.QueryRow(r.Context(), `SELECT version,is_default FROM currencies WHERE id=$1 FOR UPDATE`, id).Scan(&version, &def); err != nil {
		s.fail(w, r, err)
		return
	}
	if version != in.ExpectedVersion {
		writeError(w, 409, "version_conflict", "货币已变更，请重新加载")
		return
	}
	if def {
		writeError(w, 409, "default_currency_required", "默认货币不可删除")
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM currencies WHERE id=$1`, id); err != nil {
		s.fail(w, r, err)
		return
	}
	result := map[string]any{"deleted": true, "id": id}
	if err = store.Audit(r.Context(), tx, actor, "currency.deleted", "currencies", id, in); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = saveSettingOperationTx(r, tx, actor, "currency.delete", key, hash, result); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, result)
}

func boolPtr(v bool) *bool { return &v }

// Settings records use the same transaction and version contract as site and
// payment settings while keeping their APIs grouped by the legacy pages.
func (s *Server) financeSettings(w http.ResponseWriter, r *http.Request) {
	var min, max, balance int64
	var enabled, require bool
	var version int64
	var updated any
	err := s.db.Pool.QueryRow(r.Context(), `SELECT topup_enabled,min_topup_cents,max_topup_cents,max_balance_cents,require_active_order,version,updated_at FROM finance_settings WHERE id=1`).Scan(&enabled, &min, &max, &balance, &require, &version, &updated)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"topup_enabled": enabled, "min_topup_cents": min, "max_topup_cents": max, "max_balance_cents": balance, "require_active_order": require, "version": version, "updated_at": updated})
}

func (s *Server) updateFinanceSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TopupEnabled       *bool `json:"topup_enabled"`
		MinTopupCents      int64 `json:"min_topup_cents"`
		MaxTopupCents      int64 `json:"max_topup_cents"`
		MaxBalanceCents    int64 `json:"max_balance_cents"`
		RequireActiveOrder *bool `json:"require_active_order"`
		ExpectedVersion    int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.TopupEnabled == nil || in.RequireActiveOrder == nil || in.MinTopupCents < 0 || in.MaxTopupCents < in.MinTopupCents || in.MaxBalanceCents < 0 || in.ExpectedVersion < 1 {
		writeError(w, 400, "invalid_finance_settings", "充值财务设置无效")
		return
	}
	s.updateSingletonSettings(w, r, "finance.settings_updated", "finance_settings", in.ExpectedVersion, map[string]any{"topup_enabled": *in.TopupEnabled, "min_topup_cents": in.MinTopupCents, "max_topup_cents": in.MaxTopupCents, "max_balance_cents": in.MaxBalanceCents, "require_active_order": *in.RequireActiveOrder}, `UPDATE finance_settings SET topup_enabled=$1,min_topup_cents=$2,max_topup_cents=$3,max_balance_cents=$4,require_active_order=$5,version=version+1,updated_at=now() WHERE id=1 AND version=$6 RETURNING version`, *in.TopupEnabled, in.MinTopupCents, in.MaxTopupCents, in.MaxBalanceCents, *in.RequireActiveOrder, in.ExpectedVersion)
}

func (s *Server) invoiceSettings(w http.ResponseWriter, r *http.Request) {
	var enabled bool
	var bps, version int64
	var updated any
	if err := s.db.Pool.QueryRow(r.Context(), `SELECT enabled,rate_bps,version,updated_at FROM invoice_settings WHERE id=1`).Scan(&enabled, &bps, &version, &updated); err != nil {
		s.fail(w, r, err)
		return
	}
	rows, e := s.db.Pool.Query(r.Context(), `SELECT id,name,price_cents,active,version,created_at,updated_at FROM invoice_shipping_methods ORDER BY id`)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	methods, e := pgx.CollectRows(rows, pgx.RowToMap)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	write(w, 200, map[string]any{"enabled": enabled, "rate_percent": float64(bps) / 100, "version": version, "updated_at": updated, "shipping_methods": methods})
}

func (s *Server) updateInvoiceSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled         *bool   `json:"enabled"`
		RatePercent     float64 `json:"rate_percent"`
		ExpectedVersion int64   `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Enabled == nil || in.RatePercent < 0 || in.RatePercent > 100 || in.ExpectedVersion < 1 {
		writeError(w, 400, "invalid_invoice_settings", "发票设置无效")
		return
	}
	bps := int64(math.Round(in.RatePercent * 100))
	s.updateSingletonSettings(w, r, "invoice.settings_updated", "invoice_settings", in.ExpectedVersion, map[string]any{"enabled": *in.Enabled, "rate_percent": in.RatePercent}, `UPDATE invoice_settings SET enabled=$1,rate_bps=$2,version=version+1,updated_at=now() WHERE id=1 AND version=$3 RETURNING version`, *in.Enabled, bps, in.ExpectedVersion)
}

type shippingMethodInput struct {
	Name            string `json:"name"`
	PriceCents      int64  `json:"price_cents"`
	Active          *bool  `json:"active"`
	ExpectedVersion int64  `json:"expected_version"`
}

func (s *Server) saveInvoiceShippingMethod(w http.ResponseWriter, r *http.Request) {
	var in shippingMethodInput
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 120 || in.PriceCents < 0 {
		writeError(w, 400, "invalid_shipping_method", "快递名称或价格无效")
		return
	}
	created := r.Method == http.MethodPost
	id := int64(0)
	if !created {
		var ok bool
		id, ok = pathID(w, r)
		if !ok {
			return
		}
		if in.ExpectedVersion < 1 {
			writeError(w, 400, "invalid_shipping_method", "请提供有效版本")
			return
		}
	}
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(struct {
		ID    int64
		Input shippingMethodInput
	}{id, in})
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if replay, replayed, e := loadSettingReplayTx(r, tx, actor, "invoice.shipping.save", key, hash); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		_ = tx.Commit(r.Context())
		write(w, 200, replay)
		return
	}
	var version int64
	if created {
		if in.Active == nil {
			in.Active = boolPtr(true)
		}
		err = tx.QueryRow(r.Context(), `INSERT INTO invoice_shipping_methods(name,price_cents,active) VALUES($1,$2,$3) RETURNING id,version`, in.Name, in.PriceCents, *in.Active).Scan(&id, &version)
	} else {
		err = tx.QueryRow(r.Context(), `UPDATE invoice_shipping_methods SET name=$1,price_cents=$2,active=coalesce($3,active),version=version+1,updated_at=now() WHERE id=$4 AND version=$5 RETURNING id,version`, in.Name, in.PriceCents, in.Active, id, in.ExpectedVersion).Scan(&id, &version)
	}
	if err != nil {
		if !created && errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 409, "version_conflict", "快递方式已变更，请重新加载")
			return
		}
		s.fail(w, r, err)
		return
	}
	result := map[string]any{"id": id, "version": version}
	if err = store.Audit(r.Context(), tx, actor, "invoice.shipping_updated", "invoice_shipping_methods", id, in); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = saveSettingOperationTx(r, tx, actor, "invoice.shipping.save", key, hash, result); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, map[bool]int{true: 201, false: 200}[created], result)
}

func (s *Server) deleteInvoiceShippingMethod(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, 400, "invalid_shipping_method", "请提供有效版本")
		return
	}
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(struct {
		ID      int64
		Version int64
	}{id, in.ExpectedVersion})
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if replay, replayed, e := loadSettingReplayTx(r, tx, actor, "invoice.shipping.delete", key, hash); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		_ = tx.Commit(r.Context())
		write(w, 200, replay)
		return
	}
	var version int64
	if err = tx.QueryRow(r.Context(), `SELECT version FROM invoice_shipping_methods WHERE id=$1 FOR UPDATE`, id).Scan(&version); err != nil {
		s.fail(w, r, err)
		return
	}
	if version != in.ExpectedVersion {
		writeError(w, 409, "version_conflict", "快递方式已变更，请重新加载")
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM invoice_shipping_methods WHERE id=$1`, id); err != nil {
		s.fail(w, r, err)
		return
	}
	result := map[string]any{"id": id, "deleted": true}
	if err = store.Audit(r.Context(), tx, actor, "invoice.shipping_deleted", "invoice_shipping_methods", id, in); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = saveSettingOperationTx(r, tx, actor, "invoice.shipping.delete", key, hash, result); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, result)
}

func (s *Server) contractSettings(w http.ResponseWriter, r *http.Request) {
	var enabled bool
	var version int64
	var updated any
	if err := s.db.Pool.QueryRow(r.Context(), `SELECT enabled,version,updated_at FROM contract_settings WHERE id=1`).Scan(&enabled, &version, &updated); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"enabled": enabled, "version": version, "updated_at": updated})
}
func (s *Server) updateContractSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled         *bool `json:"enabled"`
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Enabled == nil || in.ExpectedVersion < 1 {
		writeError(w, 400, "invalid_contract_settings", "合同设置无效")
		return
	}
	s.updateSingletonSettings(w, r, "contract.settings_updated", "contract_settings", in.ExpectedVersion, map[string]any{"enabled": *in.Enabled}, `UPDATE contract_settings SET enabled=$1,version=version+1,updated_at=now() WHERE id=1 AND version=$2 RETURNING version`, *in.Enabled, in.ExpectedVersion)
}

func (s *Server) updateSingletonSettings(w http.ResponseWriter, r *http.Request, action, entity string, expected int64, audit map[string]any, query string, args ...any) {
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(audit)
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if replay, replayed, e := loadSettingReplayTx(r, tx, actor, entity, key, hash); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		_ = tx.Commit(r.Context())
		write(w, 200, replay)
		return
	}
	var version int64
	if err = tx.QueryRow(r.Context(), `SELECT version FROM `+entity+` WHERE id=1 FOR UPDATE`).Scan(&version); err != nil {
		s.fail(w, r, err)
		return
	}
	if version != expected {
		writeError(w, 409, "version_conflict", "设置已变更，请重新加载")
		return
	}
	var next int64
	if err = tx.QueryRow(r.Context(), query, args...).Scan(&next); err != nil {
		s.fail(w, r, err)
		return
	}
	result := map[string]any{"version": next}
	if err = store.Audit(r.Context(), tx, actor, action, entity, 1, audit); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = saveSettingOperationTx(r, tx, actor, entity, key, hash, result); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, result)
}

// Helpers keep context handling explicit at call sites while allowing replay
// lookup to run under the same row lock as the mutation.
func loadSettingReplayTx(r *http.Request, tx pgx.Tx, actor int64, operation, key, hash string) (map[string]any, bool, error) {
	var saved string
	var raw []byte
	err := tx.QueryRow(r.Context(), `SELECT request_hash,result FROM admin_setting_operations WHERE actor_id=$1 AND operation=$2 AND idempotency_key=$3 FOR UPDATE`, actor, operation, key).Scan(&saved, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if saved != hash {
		return nil, false, &store.Conflict{Code: "idempotency_conflict", Message: "相同幂等键对应不同的设置操作"}
	}
	var out map[string]any
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, false, err
	}
	out["replayed"] = true
	return out, true, nil
}
func saveSettingOperationTx(r *http.Request, tx pgx.Tx, actor int64, operation, key, hash string, result map[string]any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO admin_setting_operations(actor_id,operation,idempotency_key,request_hash,result) VALUES($1,$2,$3,$4,$5)`, actor, operation, key, hash, raw)
	return err
}
