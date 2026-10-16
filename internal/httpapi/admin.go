package httpapi

import (
	"encoding/json"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	var customers, products, orders, invoices, tickets, unpaid int64
	err := s.db.Pool.QueryRow(r.Context(), `SELECT
 (SELECT count(*) FROM users WHERE role='customer' AND admin_customer_allowed($1,id)),(SELECT count(*) FROM products WHERE active),(SELECT count(*) FROM orders WHERE status='pending' AND admin_customer_allowed($1,user_id)),
 (SELECT count(*) FROM invoices WHERE status='unpaid' AND admin_customer_allowed($1,user_id)),(SELECT count(*) FROM tickets WHERE status IN ('open','pending') AND merged_ticket_id IS NULL AND $2::boolean AND admin_ticket_department_allowed($1,department_id)),
 (SELECT coalesce(sum(amount_cents),0) FROM invoices WHERE status='unpaid' AND admin_customer_allowed($1,user_id))`, identityFrom(r).ID, identityFrom(r).can("tickets.view")).Scan(&customers, &products, &orders, &invoices, &tickets, &unpaid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	result := map[string]any{"customers": customers, "active_products": products, "pending_orders": orders, "unpaid_invoices": invoices, "open_tickets": tickets, "unpaid_cents": unpaid}
	now := time.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60))
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	year := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, now.Location())
	rows, err := s.db.Pool.Query(r.Context(), `WITH cash AS (
 SELECT i.user_id,i.amount_cents,i.paid_at received_at FROM invoices i WHERE i.status='paid' AND NOT EXISTS(SELECT 1 FROM credit_invoice_payments c WHERE c.invoice_id=i.id)
 UNION ALL SELECT r.user_id,r.amount_cents,r.received_at FROM credit_repayments r
 ) SELECT
 count(*)::bigint AS total_orders,
 count(*) FILTER (WHERE status IN ('paid','active') AND created_at >= $2)::bigint AS today_paid_orders,
 count(*) FILTER (WHERE status IN ('paid','active') AND created_at >= $3)::bigint AS month_paid_orders,
 count(*) FILTER (WHERE status IN ('paid','active') AND created_at >= $4)::bigint AS seven_days_paid_orders,
 count(*) FILTER (WHERE status='pending' AND created_at >= $4)::bigint AS seven_days_pending_orders,
 (SELECT coalesce(sum(amount_cents),0)::bigint FROM cash WHERE received_at >= $2 AND received_at <= $9 AND admin_customer_allowed($1,user_id)) AS today_income_cents,
 (SELECT coalesce(sum(amount_cents),0)::bigint FROM cash WHERE received_at >= $3 AND received_at <= $9 AND admin_customer_allowed($1,user_id)) AS month_income_cents,
 (SELECT coalesce(sum(amount_cents),0)::bigint FROM cash WHERE received_at >= $5 AND received_at <= $9 AND admin_customer_allowed($1,user_id)) AS annual_income_cents,
 (SELECT coalesce(sum(amount_cents),0)::bigint FROM cash WHERE received_at <= $9 AND admin_customer_allowed($1,user_id)) AS total_income_cents,
 (SELECT count(*) FROM certification_profiles WHERE status='pending' AND $6::boolean AND admin_customer_allowed($1,user_id)) AS pending_certifications,
 (SELECT count(*) FROM affiliate_withdrawals w JOIN affiliate_accounts a ON a.id=w.account_id WHERE w.status IN ('submitted','reviewing') AND $7::boolean AND admin_customer_allowed($1,a.user_id)) AS pending_withdrawals,
 (SELECT count(*) FROM users WHERE role='customer' AND created_at >= $2 AND created_at <= $9 AND admin_customer_allowed($1,id)) AS today_new_clients
 FROM orders WHERE admin_customer_allowed($1,user_id) AND created_at <= $9 AND $8::boolean`, identityFrom(r).ID, today, month, today.AddDate(0, 0, -6), year, identityFrom(r).can("certifications.view"), identityFrom(r).can("affiliates.view"), true, now)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	aggregate, err := pgx.CollectOneRow(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for key, value := range aggregate {
		result[key] = value
	}
	rows, err = s.db.Pool.Query(r.Context(), `SELECT id,name,domain,next_due_at FROM services WHERE status='active' AND next_due_at >= $2 AND next_due_at < $3 AND $4::boolean AND admin_customer_allowed($1,user_id) ORDER BY next_due_at,id LIMIT 5`, identityFrom(r).ID, now, now.AddDate(0, 0, 30), identityFrom(r).can("services.view"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	expiring, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if expiring == nil {
		expiring = []map[string]any{}
	}
	result["expiring_services"] = expiring
	write(w, 200, result)
}
func (s *Server) customers(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	s.list(w, r, customerListSelect+`
 WHERE u.role='customer' AND admin_customer_allowed($4,u.id) AND (u.display_name ILIKE $1 OR u.email ILIKE $1 OR u.username ILIKE $1)
 ORDER BY u.id DESC LIMIT $2 OFFSET $3`, "%"+r.URL.Query().Get("q")+"%", limit, offset, identityFrom(r).ID)
}

const customerListSelect = `SELECT u.id,u.username,u.email,u.display_name,u.status,u.balance_cents,u.created_at,
 u.sales_owner_id,owner.display_name AS sales_owner_name,u.sales_version,g.id AS group_id,g.name AS group_name,
 (SELECT count(*) FROM services s WHERE s.user_id=u.id AND s.status='active')::bigint AS active_services_count,
 (SELECT count(*) FROM services s WHERE s.user_id=u.id)::bigint AS total_services_count,
 (SELECT coalesce(sum(CASE WHEN l.delta_cents > 0 THEN l.delta_cents ELSE 0 END), 0) FROM balance_ledger l WHERE l.user_id=u.id)::bigint AS income_cents,
 (SELECT coalesce(sum(CASE WHEN l.delta_cents < 0 THEN -l.delta_cents ELSE 0 END), 0) FROM balance_ledger l WHERE l.user_id=u.id)::bigint AS expense_cents,
 coalesce(p.company_name,(SELECT c.company_name FROM customer_contacts c WHERE c.customer_id=u.id AND c.company_name != '' ORDER BY c.id ASC LIMIT 1)) AS company_name,
 coalesce(p.phone,(SELECT c.phone_number FROM customer_contacts c WHERE c.customer_id=u.id AND c.phone_number != '' ORDER BY c.id ASC LIMIT 1)) AS phone_number
 FROM users u
 LEFT JOIN users owner ON owner.id=u.sales_owner_id
 LEFT JOIN customer_group_members gm ON gm.customer_id=u.id
 LEFT JOIN customer_groups g ON g.id=gm.group_id
 LEFT JOIN customer_profiles p ON p.customer_id=u.id`

func (s *Server) adminCustomer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), `SELECT u.id,u.username,u.email,u.display_name,u.status,u.balance_cents,u.created_at,
 u.sales_owner_id,owner.display_name AS sales_owner_name,u.sales_version,g.id AS group_id,g.name AS group_name,
 (SELECT count(*) FROM services s WHERE s.user_id=u.id AND s.status='active')::bigint AS active_services_count,
 (SELECT count(*) FROM services s WHERE s.user_id=u.id)::bigint AS total_services_count,
 (SELECT coalesce(sum(CASE WHEN l.delta_cents > 0 THEN l.delta_cents ELSE 0 END), 0) FROM balance_ledger l WHERE l.user_id=u.id)::bigint AS income_cents,
 (SELECT coalesce(sum(CASE WHEN l.delta_cents < 0 THEN -l.delta_cents ELSE 0 END), 0) FROM balance_ledger l WHERE l.user_id=u.id)::bigint AS expense_cents,
 COALESCE(p.company_name,(SELECT c.company_name FROM customer_contacts c WHERE c.customer_id=u.id AND c.company_name != '' ORDER BY c.id ASC LIMIT 1)) AS company_name,
 COALESCE(p.phone,(SELECT c.phone_number FROM customer_contacts c WHERE c.customer_id=u.id AND c.phone_number != '' ORDER BY c.id ASC LIMIT 1)) AS phone_number,
 COALESCE(p.qq,'') AS qq,COALESCE(p.gender,'unknown') AS gender,
 COALESCE(p.country,'') AS country,COALESCE(p.province,'') AS province,COALESCE(p.address,'') AS address,
 COALESCE(p.postcode,'') AS postcode,COALESCE(p.source_channel,'') AS source_channel,
 COALESCE(p.payment_method,'bank') AS payment_method,COALESCE(p.language,'zh_CN') AS language,
 COALESCE(p.admin_note,'') AS admin_note,COALESCE(p.receive_marketing,true) AS receive_marketing,COALESCE(p.custom_fields,'{}'::jsonb) AS custom_fields,COALESCE(p.version,1) AS profile_version
 FROM users u
 LEFT JOIN users owner ON owner.id=u.sales_owner_id
 LEFT JOIN customer_group_members gm ON gm.customer_id=u.id
 LEFT JOIN customer_groups g ON g.id=gm.group_id
 LEFT JOIN customer_profiles p ON p.customer_id=u.id
 WHERE u.role='customer' AND u.id=$1 AND admin_customer_allowed($2,u.id)`, id, identityFrom(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(items) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "客户不存在")
		return
	}
	write(w, http.StatusOK, items[0])
}

func (s *Server) adminCustomerTransactions(w http.ResponseWriter, r *http.Request) {
	customerID, err := strconv.ParseInt(r.URL.Query().Get("customer_id"), 10, 64)
	if err != nil || customerID < 1 {
		writeError(w, http.StatusBadRequest, "invalid_customer", "客户筛选无效")
		return
	}
	limit, offset := pagination(r)
	query := `SELECT kind,id,invoice_id,invoice_no,label,amount_cents,delta_cents,status,balance_after,created_at,completed_at,note
FROM (
 SELECT 'payment'::text AS kind,pt.id,i.id AS invoice_id,i.invoice_no,pt.provider AS label,pt.amount_cents,NULL::bigint AS delta_cents,pt.status,NULL::bigint AS balance_after,pt.created_at,pt.completed_at,''::text AS note
 FROM payment_transactions pt JOIN invoices i ON i.id=pt.invoice_id AND i.user_id=pt.user_id
 WHERE i.user_id=$1 AND admin_customer_allowed($2,i.user_id)
 UNION ALL
 SELECT 'external_refund',r.id,i.id,i.invoice_no,r.provider,r.amount_cents,-r.amount_cents,r.status,NULL::bigint,r.created_at,r.confirmed_at,'外部退款到账'
 FROM external_refunds r JOIN invoices i ON i.id=r.invoice_id AND i.user_id=r.user_id
 WHERE r.status='confirmed' AND r.user_id=$1 AND admin_customer_allowed($2,r.user_id)
 UNION ALL
 SELECT 'ledger'::text,l.id,i.id AS invoice_id,i.invoice_no,l.entry_type AS label,NULL::bigint,l.delta_cents,'posted'::text,l.balance_after,l.created_at,NULL::timestamptz,l.note
 FROM balance_ledger l LEFT JOIN invoices i ON l.reference_type='invoice' AND i.id=l.reference_id AND i.user_id=l.user_id
 WHERE l.user_id=$1 AND admin_customer_allowed($2,l.user_id)
) events ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`
	s.list(w, r, query, customerID, identityFrom(r).ID, limit, offset)
}

func (s *Server) adminUpdateCustomer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		DisplayName     *string                    `json:"display_name"`
		Email           *string                    `json:"email"`
		Status          *string                    `json:"status"`
		Profile         map[string]json.RawMessage `json:"profile"`
		ExpectedVersion int64                      `json:"expected_version"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.DisplayName == nil && input.Email == nil && input.Status == nil && input.Profile == nil {
		writeError(w, http.StatusBadRequest, "invalid_customer", "至少提供一个要修改的字段")
		return
	}
	if input.DisplayName != nil {
		*input.DisplayName = strings.TrimSpace(*input.DisplayName)
		if *input.DisplayName == "" || len(*input.DisplayName) > 120 {
			writeError(w, http.StatusBadRequest, "invalid_customer", "姓名长度无效")
			return
		}
	}
	if input.Email != nil {
		*input.Email = strings.ToLower(strings.TrimSpace(*input.Email))
		parsed, err := mail.ParseAddress(*input.Email)
		if err != nil || parsed.Address != *input.Email || len(*input.Email) > 254 {
			writeError(w, http.StatusBadRequest, "invalid_email", "邮箱格式无效")
			return
		}
	}
	if input.Status != nil && *input.Status != "active" && *input.Status != "disabled" {
		writeError(w, http.StatusBadRequest, "invalid_customer", "客户状态无效")
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, _, _, err = lockCustomerManager(requestFileContext(r), tx, identityFrom(r).ID); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.LockAdminCustomer(r.Context(), tx, identityFrom(r).ID, id); err != nil {
		s.fail(w, r, err)
		return
	}
	// Resolve the customer scope before validating concurrency or idempotency
	// input, so a caller cannot use malformed/missing fields to probe records.
	if value, exists := input.Profile["expected_version"]; exists {
		var nested int64
		if err := json.Unmarshal(value, &nested); err != nil || (input.ExpectedVersion > 0 && input.ExpectedVersion != nested) {
			writeError(w, 400, "invalid_profile", "资料版本无效")
			return
		}
		input.ExpectedVersion = nested
	}
	if input.ExpectedVersion < 1 {
		writeError(w, 409, "customer_version_required", "请提交当前客户资料版本")
		return
	}
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	payloadHash := settingMutationHash(struct {
		ID    int64
		Input any
	}{id, input})
	operation := "customer.update:" + strconv.FormatInt(id, 10)
	if err = lockCustomerMutation(r, tx, identityFrom(r).ID, operation, key); err != nil {
		s.fail(w, r, err)
		return
	}
	if replay, replayed, e := loadSettingReplayTx(r, tx, identityFrom(r).ID, operation, key, payloadHash); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		if e = tx.Commit(r.Context()); e != nil {
			s.fail(w, r, e)
			return
		}
		write(w, 200, replay)
		return
	}
	var currentStatus string
	if err = tx.QueryRow(r.Context(), "SELECT status FROM users WHERE id=$1 AND role='customer' FOR UPDATE", id).Scan(&currentStatus); err != nil {
		s.fail(w, r, err)
		return
	}
	profile, err := currentCustomerProfile(r, tx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if profile.ExpectedVersion != input.ExpectedVersion {
		writeError(w, 409, "stale_customer_profile", "客户资料已更新，请刷新后重试")
		return
	}
	if input.Profile != nil {
		if value, exists := input.Profile["custom_fields"]; exists && strings.TrimSpace(string(value)) == "null" {
			writeError(w, 400, "invalid_custom_field", "客户字段不能为null")
			return
		}
		// Merge only explicitly submitted profile properties, retaining existing data.
		raw, _ := json.Marshal(profile)
		var merged map[string]json.RawMessage
		_ = json.Unmarshal(raw, &merged)
		for k, v := range input.Profile {
			if strings.TrimSpace(string(v)) == "null" {
				writeError(w, 400, "invalid_profile", "客户资料字段不能为null")
				return
			}
			if _, known := merged[k]; !known {
				writeError(w, 400, "invalid_profile", "未知客户资料字段")
				return
			}
			merged[k] = v
		}
		raw, _ = json.Marshal(merged)
		if err = json.Unmarshal(raw, &profile); err != nil || !profile.valid() {
			writeError(w, 400, "invalid_profile", "客户资料格式或长度无效")
			return
		}
		// An omitted custom_fields means preserve, but still validate required values.
		if _, present := input.Profile["custom_fields"]; !present {
			profile.CustomFields = nil
		}
	}
	if _, err = tx.Exec(r.Context(), `UPDATE users SET display_name=COALESCE($2,display_name),email=COALESCE($3,email),status=COALESCE($4,status),updated_at=now() WHERE id=$1`, id, input.DisplayName, input.Email, input.Status); err != nil {
		s.fail(w, r, err)
		return
	}
	if input.Status != nil && *input.Status == "disabled" {
		if _, err = tx.Exec(r.Context(), "DELETE FROM sessions WHERE user_id=$1", id); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if input.Profile != nil {
		if err = saveCustomerProfile(r.Context(), tx, id, profile); err != nil {
			if customerProfileFailure(w, r, err) {
				return
			}
			s.fail(w, r, err)
			return
		}
	}
	if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "customer.updated", "user", id, map[string]any{"display_name": input.DisplayName, "email": input.Email, "status": input.Status, "previous_status": currentStatus, "profile_updated": input.Profile != nil}); err != nil {
		s.fail(w, r, err)
		return
	}
	if input.Profile == nil {
		if _, err = tx.Exec(r.Context(), "UPDATE customer_profiles SET version=version+1,updated_at=now() WHERE customer_id=$1", id); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	result := map[string]any{"user_id": id, "profile_version": input.ExpectedVersion + 1, "replayed": false}
	if err = saveSettingOperationTx(r, tx, identityFrom(r).ID, operation, key, payloadHash, result); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, result)
}

func (s *Server) adminCreateCustomer(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username            string          `json:"username"`
		Email               string          `json:"email"`
		Password            string          `json:"password"`
		DisplayName         string          `json:"display_name"`
		Status              *string         `json:"status"`
		GroupID             *int64          `json:"group_id"`
		SalesOwnerID        *int64          `json:"sales_owner_id"`
		Gender              string          `json:"gender"`
		CompanyName         string          `json:"company_name"`
		Phone               string          `json:"phone"`
		QQ                  string          `json:"qq"`
		Country             string          `json:"country"`
		Province            string          `json:"province"`
		Address             string          `json:"address"`
		Postcode            string          `json:"postcode"`
		SourceChannel       string          `json:"source_channel"`
		PaymentMethod       string          `json:"payment_method"`
		Language            string          `json:"language"`
		AdminNote           string          `json:"admin_note"`
		ReceiveMarketing    *bool           `json:"receive_marketing"`
		CustomFieldValues   json.RawMessage `json:"custom_fields"`
		CustomFieldsVersion int64           `json:"custom_fields_version"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.CustomFieldValues != nil {
		if strings.TrimSpace(string(input.CustomFieldValues)) == "null" {
			writeError(w, 400, "invalid_custom_field", "客户字段必须为对象")
			return
		}
	}
	profile := customerProfileInput{
		Gender: input.Gender, CompanyName: input.CompanyName, Phone: input.Phone, QQ: input.QQ,
		Country: input.Country, Province: input.Province, Address: input.Address, Postcode: input.Postcode,
		SourceChannel: input.SourceChannel, PaymentMethod: input.PaymentMethod, Language: input.Language,
		AdminNote: input.AdminNote, ReceiveMarketing: input.ReceiveMarketing, CustomFieldsVersion: input.CustomFieldsVersion,
	}
	if input.CustomFieldValues != nil {
		if err := json.Unmarshal(input.CustomFieldValues, &profile.CustomFields); err != nil || profile.CustomFields == nil {
			writeError(w, 400, "invalid_custom_field", "客户字段必须为对象")
			return
		}
	}
	email := strings.ToLower(strings.TrimSpace(input.Email))
	if email == "" || len(email) > 254 {
		writeError(w, http.StatusBadRequest, "invalid_email", "邮箱格式无效")
		return
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		writeError(w, http.StatusBadRequest, "invalid_email", "邮箱格式无效")
		return
	}
	username := strings.ToLower(strings.TrimSpace(input.Username))
	if username == "" {
		username = strings.Split(email, "@")[0]
	}
	if len(username) < 2 || len(username) > 64 {
		writeError(w, http.StatusBadRequest, "invalid_username", "用户名长度需在2-64位之间")
		return
	}
	displayName := strings.TrimSpace(input.DisplayName)
	if displayName == "" {
		displayName = username
	}
	if len(displayName) > 120 {
		writeError(w, http.StatusBadRequest, "invalid_customer", "姓名长度无效")
		return
	}
	password := input.Password
	if len(password) < 8 || len(password) > 72 {
		writeError(w, http.StatusBadRequest, "invalid_password", "密码长度需在8-72字节之间")
		return
	}
	status := "active"
	if input.Status != nil && *input.Status != "" {
		if *input.Status != "active" && *input.Status != "disabled" {
			writeError(w, http.StatusBadRequest, "invalid_status", "客户状态无效")
			return
		}
		status = *input.Status
	}

	if !profile.valid() {
		writeError(w, 400, "invalid_profile", "客户资料格式或长度无效")
		return
	}
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	payloadHash := settingMutationHash(input)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())

	super, groupAllowed, salesEnabled, err := lockCustomerManager(requestFileContext(r), tx, identityFrom(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if (input.GroupID != nil && !groupAllowed) || (input.SalesOwnerID != nil && !super) {
		writeError(w, 403, "permission_denied", "没有设置客户分组或销售归属的权限")
		return
	}
	if (input.GroupID != nil && *input.GroupID < 1) || (input.SalesOwnerID != nil && *input.SalesOwnerID < 1) {
		writeError(w, 400, "invalid_assignment", "客户分组或销售归属无效")
		return
	}
	if err = lockCustomerMutation(r, tx, identityFrom(r).ID, "customer.create", key); err != nil {
		s.fail(w, r, err)
		return
	}
	if replay, replayed, e := loadSettingReplayTx(r, tx, identityFrom(r).ID, "customer.create", key, payloadHash); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		customerID := int64(replay["id"].(float64))
		if e = store.LockAdminCustomer(r.Context(), tx, identityFrom(r).ID, customerID); e != nil {
			s.fail(w, r, e)
			return
		}
		if e = tx.Commit(r.Context()); e != nil {
			s.fail(w, r, e)
			return
		}
		write(w, 200, replay)
		return
	}
	if input.GroupID != nil {
		var groupID int64
		if err = tx.QueryRow(r.Context(), "SELECT id FROM customer_groups WHERE id=$1 FOR SHARE", *input.GroupID).Scan(&groupID); err != nil {
			if err == pgx.ErrNoRows {
				writeError(w, 400, "invalid_group", "客户分组不存在")
			} else {
				s.fail(w, r, err)
			}
			return
		}
	}
	var conflict bool
	if err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM users WHERE lower(username)=$1 OR lower(email)=$2)", username, email).Scan(&conflict); err != nil {
		s.fail(w, r, err)
		return
	}
	if conflict {
		writeError(w, http.StatusConflict, "user_conflict", "用户名或邮箱已被使用")
		return
	}

	var salesOwnerID *int64
	if input.SalesOwnerID != nil && *input.SalesOwnerID > 0 {
		var ownerExists bool
		if err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND role='admin' AND status='active' AND sales_enabled)", *input.SalesOwnerID).Scan(&ownerExists); err != nil {
			s.fail(w, r, err)
			return
		}
		if !ownerExists {
			writeError(w, 400, "invalid_sales_owner", "销售负责人不存在或已停用")
			return
		}
		salesOwnerID = input.SalesOwnerID
	} else if !super && salesEnabled {
		actor := identityFrom(r).ID
		salesOwnerID = &actor
	}
	var scopeAllowed bool
	if err = tx.QueryRow(r.Context(), "SELECT admin_customer_owner_allowed($1,$2)", identityFrom(r).ID, salesOwnerID).Scan(&scopeAllowed); err != nil {
		s.fail(w, r, err)
		return
	}
	if !scopeAllowed {
		writeError(w, 403, "permission_denied", "当前客户范围不允许创建无归属客户，请联系管理员开启销售身份")
		return
	}

	var id int64
	if err = tx.QueryRow(r.Context(),
		"INSERT INTO users(username,email,password_hash,display_name,role,status,sales_owner_id,email_verified) VALUES($1,$2,$3,$4,'customer',$5,$6,true) RETURNING id",
		username, email, string(hash), displayName, status, salesOwnerID).Scan(&id); err != nil {
		s.fail(w, r, err)
		return
	}

	if _, err = tx.Exec(r.Context(), "INSERT INTO carts(user_id) VALUES($1)", id); err != nil {
		s.fail(w, r, err)
		return
	}

	if input.GroupID != nil && *input.GroupID > 0 {
		if _, err = tx.Exec(r.Context(), "INSERT INTO customer_group_members(customer_id, group_id) VALUES($1, $2) ON CONFLICT DO NOTHING", id, *input.GroupID); err != nil {
			s.fail(w, r, err)
			return
		}
	}

	if err = saveCustomerProfile(r.Context(), tx, id, profile); err != nil {
		if customerProfileFailure(w, r, err) {
			return
		}
		s.fail(w, r, err)
		return
	}
	companyName := strings.TrimSpace(profile.CompanyName)
	phone := strings.TrimSpace(profile.Phone)
	if companyName != "" || phone != "" {
		if _, err = tx.Exec(r.Context(),
			"INSERT INTO customer_contacts(customer_id, username, email, company_name, phone_number, status) VALUES($1, $2, $3, $4, $5, 'active') ON CONFLICT DO NOTHING",
			id, displayName, email, companyName, phone); err != nil {
			s.fail(w, r, err)
			return
		}
	}

	if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "customer.created", "user", id, map[string]any{
		"username":     username,
		"email":        email,
		"display_name": displayName,
		"status":       status,
	}); err != nil {
		s.fail(w, r, err)
		return
	}

	result := map[string]any{"id": id, "username": username, "email": email, "display_name": displayName, "status": status, "profile_version": 1, "replayed": false}
	if err = saveSettingOperationTx(r, tx, identityFrom(r).ID, "customer.create", key, payloadHash, result); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusCreated, result)
}

func (s *Server) auditEvents(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	s.list(w, r, "SELECT a.id,a.actor_id,u.display_name AS actor_name,a.action,a.entity_type,a.entity_id,a.payload,a.created_at FROM audit_events a LEFT JOIN users u ON u.id=a.actor_id ORDER BY a.id DESC LIMIT $1 OFFSET $2", limit, offset)
}

// adminLogPage lists audit metadata only; arbitrary payloads may contain secrets.
func (s *Server) adminLogPage(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	kind := r.URL.Query().Get("kind")
	if kind != "" && kind != "system" {
		writeError(w, 400, "invalid_log_kind", "请使用对应的专用日志接口")
		return
	}
	args := []any{}
	where := "TRUE"
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		args = append(args, "%"+q+"%")
		where = "(a.action ILIKE $1 OR a.entity_type ILIKE $1 OR a.entity_id::text ILIKE $1 OR u.display_name ILIKE $1)"
	}
	table := "audit_events a LEFT JOIN users u ON u.id=a.actor_id"
	var total int
	if err := s.db.Pool.QueryRow(r.Context(), "SELECT count(*) FROM "+table+" WHERE "+where, args...).Scan(&total); err != nil {
		s.fail(w, r, err)
		return
	}
	args = append(args, limit, offset)
	rows, err := s.db.Pool.Query(r.Context(), "SELECT a.id,a.created_at,a.action,a.entity_type,a.entity_id,a.actor_id,u.display_name AS actor_name FROM "+table+" WHERE "+where+" ORDER BY a.id DESC LIMIT $"+strconv.Itoa(len(args)-1)+" OFFSET $"+strconv.Itoa(len(args)), args...)
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
	write(w, 200, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}
