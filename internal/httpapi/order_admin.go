package httpapi

import (
	"net/http"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func (s *Server) adminOrderDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if err = store.LockAdminResourceCustomer(r.Context(), tx, identityFrom(r).ID, "order", id); err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT o.id,o.order_no,o.user_id,u.username,u.email,u.display_name,
o.status,o.review_status,o.notes,o.total_cents,o.currency,o.created_at,o.updated_at,
i.id AS invoice_id,i.invoice_no,i.status AS invoice_status,i.amount_cents,i.paid_at,i.due_at
FROM orders o JOIN users u ON u.id=o.user_id JOIN invoices i ON i.order_id=o.id WHERE o.id=$1`, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	order, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	itemsRows, err := tx.Query(r.Context(), `SELECT id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents,config_options,option_quantities,config_snapshot,`+maskedPurchaseFields+` AS purchase_fields,renewal_discount_percent,renewal_promo_code,renewal_fixed_discount_cents,renewal_discount_limit
FROM order_items WHERE order_id=$1 ORDER BY id`, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := pgx.CollectRows(itemsRows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	servicesRows, err := tx.Query(r.Context(), `SELECT id,order_item_id,product_id,name,domain,billing_cycle,status,next_due_at,username,upstream_id,last_error,provision_mode,unit_number,version,created_at,updated_at
FROM services WHERE order_id=$1 ORDER BY id`, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	services, err := pgx.CollectRows(servicesRows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if items == nil {
		items = []map[string]any{}
	}
	if services == nil {
		services = []map[string]any{}
	}
	order["items"], order["services"] = items, services
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, order)
}

func (s *Server) reviewOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Decision string `json:"decision"`
		Note     string `json:"note"`
	}
	if !decode(w, r, &input) {
		return
	}
	result, err := s.db.ReviewOrder(r.Context(), identityFrom(r).ID, id, input.Decision, input.Note, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if !result.Replayed {
		status = http.StatusCreated
	}
	write(w, status, result)
}

func (s *Server) updateOrderNotes(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Notes string `json:"notes"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Notes = strings.TrimSpace(input.Notes)
	if len(input.Notes) > 5000 {
		writeError(w, http.StatusBadRequest, "invalid_order_notes", "订单备注过长")
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if err = store.LockAdminResourceCustomer(r.Context(), tx, identityFrom(r).ID, "order", id); err != nil {
		s.fail(w, r, err)
		return
	}
	var previous string
	if err = tx.QueryRow(r.Context(), "SELECT notes FROM orders WHERE id=$1 FOR UPDATE", id).Scan(&previous); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE orders SET notes=$2,updated_at=now() WHERE id=$1", id, input.Notes); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "order.notes.updated", "order", id, map[string]any{"previous": previous, "notes": input.Notes}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"order_id": id, "notes": input.Notes})
}
