package httpapi

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

var customFieldKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

type customFieldInput struct {
	FieldKey          string   `json:"field_key"`
	Label             string   `json:"label"`
	FieldType         string   `json:"field_type"`
	Description       string   `json:"description"`
	ValidationPattern string   `json:"validation_pattern"`
	Choices           []string `json:"choices"`
	SortOrder         int      `json:"sort_order"`
	AdminOnly         bool     `json:"admin_only"`
	Required          bool     `json:"required"`
	ShowOnRegister    bool     `json:"show_on_register"`
	Active            bool     `json:"active"`
	ExpectedVersion   int64    `json:"expected_version"`
}

func normalizeCustomField(in *customFieldInput, create bool) error {
	in.FieldKey = strings.TrimSpace(in.FieldKey)
	in.Label = strings.TrimSpace(in.Label)
	in.FieldType = strings.TrimSpace(in.FieldType)
	in.Description = strings.TrimSpace(in.Description)
	in.ValidationPattern = strings.TrimSpace(in.ValidationPattern)
	if !customFieldKeyPattern.MatchString(in.FieldKey) || in.Label == "" || len([]rune(in.Label)) > 120 {
		return errInvalidCustomField
	}
	switch in.FieldType {
	case "text", "textarea", "number", "date", "select", "boolean":
	default:
		return errInvalidCustomField
	}
	if len([]rune(in.Description)) > 500 || len(in.ValidationPattern) > 500 || in.SortOrder < -100000 || in.SortOrder > 100000 {
		return errInvalidCustomField
	}
	if in.ValidationPattern != "" {
		if _, err := regexp.Compile(in.ValidationPattern); err != nil {
			return errInvalidCustomField
		}
	}
	clean := make([]string, 0, len(in.Choices))
	seen := map[string]struct{}{}
	for _, choice := range in.Choices {
		choice = strings.TrimSpace(choice)
		if choice == "" || len([]rune(choice)) > 120 {
			return errInvalidCustomField
		}
		if _, ok := seen[choice]; ok {
			return errInvalidCustomField
		}
		seen[choice] = struct{}{}
		clean = append(clean, choice)
	}
	in.Choices = clean
	if in.FieldType == "select" && len(in.Choices) == 0 || in.FieldType != "select" && len(in.Choices) != 0 {
		return errInvalidCustomField
	}
	if create && in.ExpectedVersion != 0 || !create && in.ExpectedVersion < 1 {
		return errInvalidCustomField
	}
	return nil
}

type customFieldInputError struct{}

func (*customFieldInputError) Error() string { return "客户自定义字段无效" }

var errInvalidCustomField = &customFieldInputError{}

func (s *Server) customerCustomFieldFailure(w http.ResponseWriter, r *http.Request, err error) {
	if err == errInvalidCustomField {
		writeError(w, http.StatusBadRequest, "invalid_customer_custom_field", err.Error())
		return
	}
	s.fail(w, r, err)
}

func (s *Server) adminCustomerCustomFields(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	fieldType := strings.TrimSpace(r.URL.Query().Get("field_type"))
	if fieldType != "" && fieldType != "text" && fieldType != "textarea" && fieldType != "number" && fieldType != "date" && fieldType != "select" && fieldType != "boolean" {
		writeError(w, http.StatusBadRequest, "invalid_field_type", "字段类型无效")
		return
	}
	active := r.URL.Query().Get("active")
	if active != "" && active != "true" && active != "false" {
		writeError(w, http.StatusBadRequest, "invalid_active", "启用状态无效")
		return
	}
	where := ` FROM customer_custom_fields WHERE ($1='' OR field_key ILIKE '%'||$1||'%' OR label ILIKE '%'||$1||'%' OR description ILIKE '%'||$1||'%') AND ($2='' OR field_type=$2) AND ($3='' OR active=($3='true'))`
	var total int64
	if err := s.db.Pool.QueryRow(r.Context(), "SELECT count(*)"+where, q, fieldType, active).Scan(&total); err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), `SELECT id,field_key,label,field_type,description,validation_pattern,choices,sort_order,admin_only,required,show_on_register,active,version,created_at,updated_at`+where+` ORDER BY sort_order,id LIMIT $4 OFFSET $5`, q, fieldType, active, limit, offset)
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

func (s *Server) createCustomerCustomField(w http.ResponseWriter, r *http.Request) {
	var in customFieldInput
	if !decode(w, r, &in) {
		return
	}
	if err := normalizeCustomField(&in, true); err != nil {
		s.customerCustomFieldFailure(w, r, err)
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
	if _, _, _, err = lockCustomerManager(requestFileContext(r), tx, actor); err != nil {
		s.fail(w, r, err)
		return
	}
	operation := "customer_custom_field.create"
	if err = lockCustomerMutation(r, tx, actor, operation, key); err != nil {
		s.fail(w, r, err)
		return
	}
	if replay, replayed, e := loadSettingReplayTx(r, tx, actor, operation, key, hash); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		_ = tx.Commit(r.Context())
		write(w, http.StatusOK, replay)
		return
	}
	var id int64
	err = tx.QueryRow(r.Context(), `INSERT INTO customer_custom_fields(field_key,label,field_type,description,validation_pattern,choices,sort_order,admin_only,required,show_on_register,active) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`, in.FieldKey, in.Label, in.FieldType, in.Description, in.ValidationPattern, in.Choices, in.SortOrder, in.AdminOnly, in.Required, in.ShowOnRegister, in.Active).Scan(&id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	result := map[string]any{"id": id, "version": 1}
	if err = store.Audit(r.Context(), tx, actor, "customer_custom_field.created", "customer_custom_field", id, map[string]any{"field_key": in.FieldKey}); err != nil {
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
	write(w, http.StatusCreated, result)
}

func (s *Server) updateCustomerCustomField(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in customFieldInput
	if !decode(w, r, &in) {
		return
	}
	if err := normalizeCustomField(&in, false); err != nil {
		s.customerCustomFieldFailure(w, r, err)
		return
	}
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(struct {
		ID    int64
		Input customFieldInput
	}{id, in})
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, _, _, err = lockCustomerManager(requestFileContext(r), tx, actor); err != nil {
		s.fail(w, r, err)
		return
	}
	operation := "customer_custom_field.update:" + strconv.FormatInt(id, 10)
	if err = lockCustomerMutation(r, tx, actor, operation, key); err != nil {
		s.fail(w, r, err)
		return
	}
	if replay, replayed, e := loadSettingReplayTx(r, tx, actor, operation, key, hash); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		_ = tx.Commit(r.Context())
		write(w, http.StatusOK, replay)
		return
	}
	var version int64
	if err = tx.QueryRow(r.Context(), "SELECT version FROM customer_custom_fields WHERE id=$1 FOR UPDATE", id).Scan(&version); err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "not_found", "客户自定义字段不存在")
			return
		}
		s.fail(w, r, err)
		return
	}
	if version != in.ExpectedVersion {
		writeError(w, http.StatusConflict, "version_conflict", "客户自定义字段已变更，请刷新后重试")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE customer_custom_fields SET field_key=$2,label=$3,field_type=$4,description=$5,validation_pattern=$6,choices=$7,sort_order=$8,admin_only=$9,required=$10,show_on_register=$11,active=$12,version=version+1,updated_at=now() WHERE id=$1`, id, in.FieldKey, in.Label, in.FieldType, in.Description, in.ValidationPattern, in.Choices, in.SortOrder, in.AdminOnly, in.Required, in.ShowOnRegister, in.Active); err != nil {
		s.fail(w, r, err)
		return
	}
	result := map[string]any{"id": id, "version": version + 1}
	if err = store.Audit(r.Context(), tx, actor, "customer_custom_field.updated", "customer_custom_field", id, map[string]any{"expected_version": in.ExpectedVersion}); err != nil {
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

func (s *Server) deleteCustomerCustomField(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, http.StatusBadRequest, "invalid_customer_custom_field", "版本无效")
		return
	}
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(struct {
		ID              int64
		ExpectedVersion int64
	}{id, in.ExpectedVersion})
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, _, _, err = lockCustomerManager(requestFileContext(r), tx, actor); err != nil {
		s.fail(w, r, err)
		return
	}
	operation := "customer_custom_field.delete:" + strconv.FormatInt(id, 10)
	if err = lockCustomerMutation(r, tx, actor, operation, key); err != nil {
		s.fail(w, r, err)
		return
	}
	if replay, replayed, e := loadSettingReplayTx(r, tx, actor, operation, key, hash); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		_ = tx.Commit(r.Context())
		write(w, http.StatusOK, replay)
		return
	}
	var version int64
	if err = tx.QueryRow(r.Context(), "SELECT version FROM customer_custom_fields WHERE id=$1 FOR UPDATE", id).Scan(&version); err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "not_found", "客户自定义字段不存在")
			return
		}
		s.fail(w, r, err)
		return
	}
	if version != in.ExpectedVersion {
		writeError(w, http.StatusConflict, "version_conflict", "客户自定义字段已变更，请刷新后重试")
		return
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM customer_custom_fields WHERE id=$1", id); err != nil {
		s.fail(w, r, err)
		return
	}
	result := map[string]any{"id": id, "deleted": true}
	if err = store.Audit(r.Context(), tx, actor, "customer_custom_field.deleted", "customer_custom_field", id, map[string]any{"expected_version": in.ExpectedVersion}); err != nil {
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
