package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func customerListFilterID(w http.ResponseWriter, r *http.Request, key string) (*int64, bool) {
	raw := r.URL.Query().Get(key)
	if raw == "" || raw == "all" {
		return nil, true
	}
	if raw == "none" {
		value := int64(0)
		return &value, true
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 1 {
		writeError(w, 400, "invalid_filter", "筛选条件无效")
		return nil, false
	}
	return &value, true
}

func customerDirectoryCustomFilters(raw string) (map[string]any, error) {
	if len(raw) > 16384 || !utf8.ValidString(raw) {
		return nil, io.ErrUnexpectedEOF
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return nil, io.ErrUnexpectedEOF
	}
	values := map[string]any{}
	for dec.More() {
		token, err = dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok || !customFieldKeyPattern.MatchString(key) || len(values) >= 20 {
			return nil, io.ErrUnexpectedEOF
		}
		if _, exists := values[key]; exists {
			return nil, io.ErrUnexpectedEOF
		}
		var value any
		if err = dec.Decode(&value); err != nil {
			return nil, err
		}
		switch v := value.(type) {
		case string:
			if !utf8.ValidString(v) || strings.TrimSpace(v) == "" || strings.ContainsRune(v, 0) || utf8.RuneCountInString(v) > 5000 {
				return nil, io.ErrUnexpectedEOF
			}
		case bool, float64:
		default:
			return nil, io.ErrUnexpectedEOF
		}
		values[key] = value
	}
	token, err = dec.Token()
	if err != nil || token != json.Delim('}') {
		return nil, io.ErrUnexpectedEOF
	}
	if _, err = dec.Token(); err != io.EOF {
		return nil, io.ErrUnexpectedEOF
	}
	return values, nil
}

func customerDirectoryInput(w http.ResponseWriter, r *http.Request) (store.AdminCustomerDirectoryFilter, bool) {
	var in store.AdminCustomerDirectoryFilter
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, 400, "invalid_customer_filter", "客户高级筛选条件无效")
		return in, false
	}
	allowed := map[string]bool{"q": true, "group_id": true, "sales_owner_id": true, "status": true, "limit": true, "offset": true, "company": true, "phone": true, "qq": true, "certification_status": true, "level_id": true, "custom_fields": true}
	for name, entries := range values {
		if !allowed[name] || len(entries) != 1 {
			writeError(w, 400, "invalid_customer_filter", "客户高级筛选条件无效")
			return in, false
		}
	}
	var ok bool
	in.GroupID, ok = customerListFilterID(w, r, "group_id")
	if !ok {
		return in, false
	}
	in.SalesOwnerID, ok = customerListFilterID(w, r, "sales_owner_id")
	if !ok {
		return in, false
	}
	in.Status = values.Get("status")
	if in.Status != "" && in.Status != "active" && in.Status != "disabled" {
		writeError(w, 400, "invalid_status", "客户状态无效")
		return in, false
	}
	in.Q = strings.TrimSpace(values.Get("q"))
	in.Company = strings.TrimSpace(values.Get("company"))
	in.Phone = strings.TrimSpace(values.Get("phone"))
	in.QQ = strings.TrimSpace(values.Get("qq"))
	for _, field := range []struct {
		name, value string
		max         int
	}{{"q", in.Q, 120}, {"company", in.Company, 200}, {"phone", in.Phone, 80}, {"qq", in.QQ, 40}} {
		_, present := values[field.name]
		if !utf8.ValidString(field.value) || utf8.RuneCountInString(field.value) > field.max || strings.ContainsRune(field.value, 0) || (present && field.name != "q" && field.value == "") {
			writeError(w, 400, "invalid_customer_filter", "客户高级筛选条件无效")
			return in, false
		}
	}
	if entries, present := values["certification_status"]; present {
		in.CertificationStatus = entries[0]
		if in.CertificationStatus != "unverified" && in.CertificationStatus != "pending" && in.CertificationStatus != "approved" && in.CertificationStatus != "rejected" {
			writeError(w, 400, "invalid_customer_filter", "客户高级筛选条件无效")
			return in, false
		}
	}
	if entries, present := values["level_id"]; present {
		raw := entries[0]
		id, e := strconv.ParseInt(raw, 10, 64)
		if e != nil || id < 1 || strings.Trim(raw, "0123456789") != "" {
			writeError(w, 400, "invalid_customer_filter", "客户高级筛选条件无效")
			return in, false
		}
		in.LevelID = &id
	}
	if entries, present := values["custom_fields"]; present {
		in.CustomFields, err = customerDirectoryCustomFilters(entries[0])
		if err != nil {
			writeError(w, 400, "invalid_customer_filter", "客户自定义字段筛选无效")
			return in, false
		}
	}
	return in, true
}

func (s *Server) customersPage(w http.ResponseWriter, r *http.Request) {
	in, ok := customerDirectoryInput(w, r)
	if !ok {
		return
	}
	limit, offset := pagination(r)
	page, err := s.db.AdminCustomerDirectoryPage(r.Context(), identityFrom(r).ID, in, limit, offset)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, 200, page)
}

func (s *Server) adminCertificationsPage(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && status != "unverified" && status != "pending" && status != "approved" && status != "rejected" {
		writeError(w, 400, "invalid_certification_status", "认证状态无效")
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("certification_type"))
	if kind != "" && kind != "individual" && kind != "company" {
		writeError(w, 400, "invalid_certification_type", "认证类型无效")
		return
	}
	provider := strings.TrimSpace(r.URL.Query().Get("provider_id"))
	if len(provider) > 80 {
		writeError(w, 400, "invalid_certification_provider", "认证接口无效")
		return
	}
	limit, offset := pagination(r)
	query := `SELECT p.id,p.user_id,u.username,u.display_name,u.email,p.certification_type,p.status,p.provider_id,p.rejection_reason,p.version,p.updated_at,
 (SELECT max(id) FROM certification_submissions x WHERE x.profile_id=p.id) AS submission_id
 FROM certification_profiles p JOIN users u ON u.id=p.user_id
 WHERE ($1='' OR p.status=$1) AND ($2='' OR p.certification_type=$2) AND ($3='' OR p.provider_id=$3)
 AND (u.username ILIKE $4 OR u.display_name ILIKE $4 OR u.email ILIKE $4 OR p.id::text ILIKE $4)
 AND admin_customer_allowed($5,u.id)`
	s.adminListPage(w, r, query, "updated_at DESC,id DESC", limit, offset, status, kind, provider, "%"+strings.TrimSpace(r.URL.Query().Get("q"))+"%", identityFrom(r).ID)
}

// One statement keeps the total and page on the same database snapshot.
func (s *Server) adminListPage(w http.ResponseWriter, r *http.Request, query, order string, limit, offset int, args ...any) {
	limitArg, offsetArg := strconv.Itoa(len(args)+1), strconv.Itoa(len(args)+2)
	rows, err := s.db.Pool.Query(r.Context(), `WITH filtered AS (`+query+`) SELECT (SELECT count(*) FROM filtered) AS total,
 coalesce((SELECT jsonb_agg(row_to_json(page)) FROM (SELECT * FROM filtered ORDER BY `+order+` LIMIT $`+limitArg+` OFFSET $`+offsetArg+`) page),'[]'::jsonb) AS items`, append(args, limit, offset)...)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	result, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	result["limit"], result["offset"] = limit, offset
	write(w, 200, result)
}
