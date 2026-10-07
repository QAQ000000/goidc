package httpapi

import (
	"net/http"
	"net/mail"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
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
	write(w, 200, map[string]int64{"customers": customers, "active_products": products, "pending_orders": orders, "unpaid_invoices": invoices, "open_tickets": tickets, "unpaid_cents": unpaid})
}
func (s *Server) customers(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	s.list(w, r, "SELECT u.id,u.username,u.email,u.display_name,u.status,u.balance_cents,u.created_at,u.sales_owner_id,owner.display_name AS sales_owner_name,u.sales_version,g.id AS group_id,g.name AS group_name FROM users u LEFT JOIN users owner ON owner.id=u.sales_owner_id LEFT JOIN customer_group_members gm ON gm.customer_id=u.id LEFT JOIN customer_groups g ON g.id=gm.group_id WHERE u.role='customer' AND admin_customer_allowed($4,u.id) AND (u.display_name ILIKE $1 OR u.email ILIKE $1) ORDER BY u.id DESC LIMIT $2 OFFSET $3", "%"+r.URL.Query().Get("q")+"%", limit, offset, identityFrom(r).ID)
}

func (s *Server) adminCustomer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), `SELECT u.id,u.username,u.email,u.display_name,u.status,u.balance_cents,u.created_at,u.sales_owner_id,owner.display_name AS sales_owner_name,u.sales_version,g.id AS group_id,g.name AS group_name FROM users u LEFT JOIN users owner ON owner.id=u.sales_owner_id LEFT JOIN customer_group_members gm ON gm.customer_id=u.id LEFT JOIN customer_groups g ON g.id=gm.group_id WHERE u.role='customer' AND u.id=$1 AND admin_customer_allowed($2,u.id)`, id, identityFrom(r).ID)
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
		DisplayName *string `json:"display_name"`
		Email       *string `json:"email"`
		Status      *string `json:"status"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.DisplayName == nil && input.Email == nil && input.Status == nil {
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
	if err = store.LockAdminCustomer(r.Context(), tx, identityFrom(r).ID, id); err != nil {
		s.fail(w, r, err)
		return
	}
	var currentStatus string
	if err = tx.QueryRow(r.Context(), "SELECT status FROM users WHERE id=$1 AND role='customer' FOR UPDATE", id).Scan(&currentStatus); err != nil {
		s.fail(w, r, err)
		return
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
	if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "customer.updated", "user", id, map[string]any{"display_name": input.DisplayName, "email": input.Email, "status": input.Status, "previous_status": currentStatus}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"user_id": id, "status": input.Status})
}

func (s *Server) auditEvents(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	s.list(w, r, "SELECT a.id,a.actor_id,u.display_name AS actor_name,a.action,a.entity_type,a.entity_id,a.payload,a.created_at FROM audit_events a LEFT JOIN users u ON u.id=a.actor_id ORDER BY a.id DESC LIMIT $1 OFFSET $2", limit, offset)
}
