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
 SELECT i.user_id,i.amount_cents,i.paid_at received_at FROM invoices i WHERE (i.status='paid' OR (i.status='refunded' AND EXISTS(SELECT 1 FROM external_refunds er WHERE er.invoice_id=i.id AND er.status='confirmed'))) AND i.invoice_kind<>'topup' AND NOT EXISTS(SELECT 1 FROM credit_invoice_payments c WHERE c.invoice_id=i.id)
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
	items, err := s.db.AdminCustomerDirectory(r.Context(), identityFrom(r).ID, r.URL.Query().Get("q"), limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, items)
}

func (s *Server) adminCustomer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	items, err := s.db.AdminCustomerDetail(r.Context(), identityFrom(r).ID, id)
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
 SELECT 'ledger'::text,l.id,i.id AS invoice_id,i.invoice_no,CASE WHEN l.reference_type='gateway_topup' THEN 'gateway_topup' ELSE l.entry_type END AS label,NULL::bigint,l.delta_cents,'posted'::text,l.balance_after,l.created_at,NULL::timestamptz,l.note
 FROM balance_ledger l LEFT JOIN invoices i ON l.reference_type IN ('invoice','gateway_topup') AND i.id=l.reference_id AND i.user_id=l.user_id
 WHERE l.user_id=$1 AND admin_customer_allowed($2,l.user_id)
) events ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`
	s.list(w, r, query, customerID, identityFrom(r).ID, limit, offset)
}

func (s *Server) adminUpdateCustomer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input store.AdminUpdateCustomerInput
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
	result, err := s.db.AdminUpdateCustomer(requestFileContext(r), identityFrom(r).ID, id, input, r.Header.Get("Idempotency-Key"))
	if accountBusinessFailure(s, w, r, err) {
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

	if !profile.Valid() {
		writeError(w, 400, "invalid_profile", "客户资料格式或长度无效")
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	payloadHash := settingMutationHash(input)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	result, err := s.db.AdminCreateCustomer(requestFileContext(r), identityFrom(r).ID, store.AdminCreateCustomerInput{
		Username: username, Email: email, PasswordHash: string(hash), DisplayName: displayName, Status: status, GroupID: input.GroupID, SalesOwnerID: input.SalesOwnerID, Profile: profile,
	}, key, payloadHash)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	statusCode := http.StatusCreated
	if result["replayed"] == true {
		statusCode = http.StatusOK
	}
	write(w, statusCode, result)
}

func (s *Server) auditEvents(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	items, err := s.db.AdminAuditEvents(r.Context(), limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, items)
}

// adminLogPage lists audit metadata only; arbitrary payloads may contain secrets.
func (s *Server) adminLogPage(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	kind := r.URL.Query().Get("kind")
	if kind != "" && kind != "system" {
		writeError(w, 400, "invalid_log_kind", "请使用对应的专用日志接口")
		return
	}
	page, err := s.db.AdminAuditLogPage(r.Context(), strings.TrimSpace(r.URL.Query().Get("q")), limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, page)
}
