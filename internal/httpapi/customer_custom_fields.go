package httpapi

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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

// Keep configuration reads explicit: schema additions must not expand this API.
type customerCustomFieldDTO struct {
	ID                int64     `json:"id"`
	FieldKey          string    `json:"field_key"`
	Label             string    `json:"label"`
	FieldType         string    `json:"field_type"`
	Description       string    `json:"description"`
	ValidationPattern string    `json:"validation_pattern"`
	Choices           []string  `json:"choices"`
	SortOrder         int       `json:"sort_order"`
	AdminOnly         bool      `json:"admin_only"`
	Required          bool      `json:"required"`
	ShowOnRegister    bool      `json:"show_on_register"`
	Active            bool      `json:"active"`
	Version           int64     `json:"version"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

const customerCustomFieldColumns = `id,field_key,label,field_type,description,validation_pattern,choices,sort_order,admin_only,required,show_on_register,active,version,created_at,updated_at`

func scanCustomerCustomField(row pgx.Row) (customerCustomFieldDTO, error) {
	var out customerCustomFieldDTO
	err := row.Scan(&out.ID, &out.FieldKey, &out.Label, &out.FieldType, &out.Description, &out.ValidationPattern, &out.Choices, &out.SortOrder, &out.AdminOnly, &out.Required, &out.ShowOnRegister, &out.Active, &out.Version, &out.CreatedAt, &out.UpdatedAt)
	if out.Choices == nil {
		out.Choices = []string{}
	}
	return out, err
}
func (s *Server) adminCustomerCustomField(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	out, err := scanCustomerCustomField(s.db.Pool.QueryRow(r.Context(), "SELECT "+customerCustomFieldColumns+" FROM customer_custom_fields WHERE id=$1", id))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, out)
}
func (s *Server) adminCustomerCustomFields(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	query := r.URL.Query()
	for key, values := range query {
		if (key != "q" && key != "field_type" && key != "active" && key != "limit" && key != "offset") || len(values) != 1 {
			writeError(w, 400, "invalid_customer_custom_field_query", "字段查询参数无效")
			return
		}
	}
	limit, offset := pagination(r)
	q := strings.TrimSpace(query.Get("q"))
	if !utf8.ValidString(q) || utf8.RuneCountInString(q) > 100 || strings.ContainsRune(q, 0) {
		writeError(w, 400, "invalid_customer_custom_field_query", "搜索内容无效或超过100字符")
		return
	}
	fieldType := query.Get("field_type")
	if fieldType != "" && fieldType != "text" && fieldType != "textarea" && fieldType != "number" && fieldType != "date" && fieldType != "select" && fieldType != "boolean" {
		writeError(w, 400, "invalid_field_type", "字段类型无效")
		return
	}
	active := query.Get("active")
	if active != "" && active != "true" && active != "false" {
		writeError(w, 400, "invalid_active", "启用状态无效")
		return
	}
	pattern := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
	where := ` FROM customer_custom_fields WHERE (field_key ILIKE $1 OR label ILIKE $1 OR description ILIKE $1) AND ($2='' OR field_type=$2) AND ($3='' OR active=($3='true'))`
	tx, err := s.db.Pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	page, err := listPage(r.Context(), tx, "SELECT count(*)"+where,
		"SELECT "+customerCustomFieldColumns+where+" ORDER BY sort_order,id", []any{pattern, fieldType, active}, limit, offset,
		func(row pgx.CollectableRow) (customerCustomFieldDTO, error) { return scanCustomerCustomField(row) })
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, page)
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
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(in)
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		if _, _, _, err = lockCustomerManager(requestFileContext(r), tx, actor); err != nil {
			return txResponse{}, err
		}
		operation := "customer_custom_field.create"
		if err = lockCustomerMutation(r, tx, actor, operation, key); err != nil {
			return txResponse{}, err
		}
		if replay, replayed, e := loadSettingReplayTx(r, tx, actor, operation, key, hash); e != nil {
			return txResponse{}, e
		} else if replayed {

			return txResponse{http.StatusOK, replay}, nil
		}
		var id int64
		err = tx.QueryRow(r.Context(), `INSERT INTO customer_custom_fields(field_key,label,field_type,description,validation_pattern,choices,sort_order,admin_only,required,show_on_register,active) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`, in.FieldKey, in.Label, in.FieldType, in.Description, in.ValidationPattern, in.Choices, in.SortOrder, in.AdminOnly, in.Required, in.ShowOnRegister, in.Active).Scan(&id)
		if err != nil {
			return txResponse{}, err
		}
		result := map[string]any{"id": id, "version": 1}
		if err = store.Audit(r.Context(), tx, actor, "customer_custom_field.created", "customer_custom_field", id, map[string]any{"field_key": in.FieldKey}); err != nil {
			return txResponse{}, err
		}
		if err = saveSettingOperationTx(r, tx, actor, operation, key, hash, result); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusCreated, result}, nil
	})
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
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(struct {
		ID    int64
		Input customFieldInput
	}{id, in})
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		if _, _, _, err = lockCustomerManager(requestFileContext(r), tx, actor); err != nil {
			return txResponse{}, err
		}
		operation := "customer_custom_field.update:" + strconv.FormatInt(id, 10)
		if err = lockCustomerMutation(r, tx, actor, operation, key); err != nil {
			return txResponse{}, err
		}
		if replay, replayed, e := loadSettingReplayTx(r, tx, actor, operation, key, hash); e != nil {
			return txResponse{}, e
		} else if replayed {

			return txResponse{http.StatusOK, replay}, nil
		}
		var version int64
		var fieldKey string
		if err = tx.QueryRow(r.Context(), "SELECT version,field_key FROM customer_custom_fields WHERE id=$1 FOR UPDATE", id).Scan(&version, &fieldKey); err != nil {
			if err == pgx.ErrNoRows {
				return txResponse{}, &txHTTPError{http.StatusNotFound, "not_found", "客户自定义字段不存在"}
			}
			return txResponse{}, err
		}
		if fieldKey != in.FieldKey {
			return txResponse{}, &txHTTPError{http.StatusConflict, "field_key_immutable", "字段标识创建后不可修改"}
		}
		if version != in.ExpectedVersion {
			return txResponse{}, &txHTTPError{http.StatusConflict, "version_conflict", "客户自定义字段已变更，请刷新后重试"}
		}
		if _, err = store.UpdateVersioned(r.Context(), tx, "customer_custom_fields", `field_key=$1,label=$2,field_type=$3,description=$4,validation_pattern=$5,choices=$6,sort_order=$7,admin_only=$8,required=$9,show_on_register=$10,active=$11`, id, in.ExpectedVersion, in.FieldKey, in.Label, in.FieldType, in.Description, in.ValidationPattern, in.Choices, in.SortOrder, in.AdminOnly, in.Required, in.ShowOnRegister, in.Active); err != nil {
			return txResponse{}, err
		}
		result := map[string]any{"id": id, "version": version + 1}
		if err = store.Audit(r.Context(), tx, actor, "customer_custom_field.updated", "customer_custom_field", id, map[string]any{"expected_version": in.ExpectedVersion}); err != nil {
			return txResponse{}, err
		}
		if err = saveSettingOperationTx(r, tx, actor, operation, key, hash, result); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusOK, result}, nil
	})
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
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(struct {
		ID              int64
		ExpectedVersion int64
	}{id, in.ExpectedVersion})
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		if _, _, _, err = lockCustomerManager(requestFileContext(r), tx, actor); err != nil {
			return txResponse{}, err
		}
		operation := "customer_custom_field.delete:" + strconv.FormatInt(id, 10)
		if err = lockCustomerMutation(r, tx, actor, operation, key); err != nil {
			return txResponse{}, err
		}
		if replay, replayed, e := loadSettingReplayTx(r, tx, actor, operation, key, hash); e != nil {
			return txResponse{}, e
		} else if replayed {

			return txResponse{http.StatusOK, replay}, nil
		}
		var version int64
		if err = tx.QueryRow(r.Context(), "SELECT version FROM customer_custom_fields WHERE id=$1 FOR UPDATE", id).Scan(&version); err != nil {
			if err == pgx.ErrNoRows {
				return txResponse{}, &txHTTPError{http.StatusNotFound, "not_found", "客户自定义字段不存在"}
			}
			return txResponse{}, err
		}
		if version != in.ExpectedVersion {
			return txResponse{}, &txHTTPError{http.StatusConflict, "version_conflict", "客户自定义字段已变更，请刷新后重试"}
		}
		if _, err = tx.Exec(r.Context(), "DELETE FROM customer_custom_fields WHERE id=$1", id); err != nil {
			return txResponse{}, err
		}
		result := map[string]any{"id": id, "deleted": true}
		if err = store.Audit(r.Context(), tx, actor, "customer_custom_field.deleted", "customer_custom_field", id, map[string]any{"expected_version": in.ExpectedVersion}); err != nil {
			return txResponse{}, err
		}
		if err = saveSettingOperationTx(r, tx, actor, operation, key, hash, result); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusOK, result}, nil
	})
}
