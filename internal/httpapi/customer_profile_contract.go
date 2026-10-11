package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

// Definition consumption is read-only, and does not grant access to CRUD.
func (s *Server) customerFieldDefinitions(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock_shared(70108190)"); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.LockFileCredential(requestFileContext(r), tx, identityFrom(r).ID); err != nil {
		s.fail(w, r, err)
		return
	}
	var allowed bool
	if err = tx.QueryRow(r.Context(), `SELECT u.status='active' AND (u.role='super_admin' OR (u.role='admin' AND a.active AND ('customers.view'=ANY(a.permissions) OR 'customers.manage'=ANY(a.permissions)))) FROM users u LEFT JOIN admin_roles a ON a.id=u.admin_role_id WHERE u.id=$1`, identityFrom(r).ID).Scan(&allowed); err != nil {
		s.fail(w, r, err)
		return
	}
	if !allowed {
		writeError(w, 403, "permission_denied", "没有读取客户字段的权限")
		return
	}
	var version, total int64
	if err = tx.QueryRow(r.Context(), "SELECT version FROM customer_field_catalogue WHERE id=1 FOR SHARE").Scan(&version); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.QueryRow(r.Context(), "SELECT count(*) FROM customer_custom_fields WHERE active").Scan(&total); err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT id,field_key,label,field_type,description,validation_pattern,choices,sort_order,admin_only,required,show_on_register,active,version FROM customer_custom_fields WHERE active ORDER BY sort_order,id LIMIT $1 OFFSET $2`, limit, offset)
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
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset, "definitions_version": version})
}

// An advisory lock also serializes the first request, when no receipt exists yet.
func lockCustomerMutation(r *http.Request, tx pgx.Tx, actor int64, operation, key string) error {
	_, err := tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", fmt.Sprintf("customer-mutation:%d:%s:%s", actor, operation, key))
	return err
}

func currentCustomerProfile(r *http.Request, tx pgx.Tx, id int64) (customerProfileInput, error) {
	var p customerProfileInput
	var raw []byte
	// Old or independently-created accounts can lack a profile row.
	if _, err := tx.Exec(r.Context(), "INSERT INTO customer_profiles(customer_id,company_name,phone) SELECT $1,COALESCE((SELECT company_name FROM customer_contacts WHERE customer_id=$1 AND company_name<>'' ORDER BY id LIMIT 1),''),COALESCE((SELECT phone_number FROM customer_contacts WHERE customer_id=$1 AND phone_number<>'' ORDER BY id LIMIT 1),'') ON CONFLICT DO NOTHING", id); err != nil {
		return p, err
	}
	err := tx.QueryRow(r.Context(), `SELECT gender,company_name,phone,qq,country,province,address,postcode,source_channel,payment_method,language,admin_note,receive_marketing,custom_fields,version FROM customer_profiles WHERE customer_id=$1 FOR UPDATE`, id).Scan(&p.Gender, &p.CompanyName, &p.Phone, &p.QQ, &p.Country, &p.Province, &p.Address, &p.Postcode, &p.SourceChannel, &p.PaymentMethod, &p.Language, &p.AdminNote, &p.ReceiveMarketing, &raw, &p.ExpectedVersion)
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(raw, &p.CustomFields)
	return p, err
}
