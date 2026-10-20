package httpapi

import (
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

var customFieldKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

type customFieldInput = store.CustomerCustomFieldInput

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
	result, err := s.db.CreateCustomerCustomField(requestFileContext(r), identityFrom(r).ID, in, key)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	status := http.StatusOK
	if result["replayed"] != true {
		status = http.StatusCreated
	}
	write(w, status, result)
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
	result, err := s.db.UpdateCustomerCustomField(requestFileContext(r), identityFrom(r).ID, id, in, key)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	status := http.StatusOK
	write(w, status, result)
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
	result, err := s.db.DeleteCustomerCustomField(requestFileContext(r), identityFrom(r).ID, id, in.ExpectedVersion, key)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	status := http.StatusOK
	write(w, status, result)
}
