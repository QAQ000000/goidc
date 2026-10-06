package httpapi

import (
	"net/http"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
)

func (s *Server) invoiceCustomers(w http.ResponseWriter, r *http.Request) {
	if !identityFrom(r).can("invoices.view") {
		writeError(w, 403, "permission_denied", "没有读取账单的权限")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 120 {
		writeError(w, 400, "invalid_query", "搜索条件过长")
		return
	}
	limit, offset := pagination(r)
	s.transactionPage(w, r, "u.id,u.username,u.display_name,u.email,u.status",
		` FROM users u WHERE u.role='customer' AND admin_customer_allowed($1,u.id)
 AND ($2='' OR u.username ILIKE '%'||$2||'%' OR u.display_name ILIKE '%'||$2||'%' OR u.email ILIKE '%'||$2||'%')`,
		"u.id", []any{identityFrom(r).ID, q}, limit, offset)
}

func (s *Server) saveManualInvoice(w http.ResponseWriter, r *http.Request) {
	var id int64
	if r.Method != "POST" {
		var ok bool
		id, ok = pathID(w, r)
		if !ok {
			return
		}
	}
	var in store.ManualInvoiceInput
	if !decode(w, r, &in) {
		return
	}
	result, err := s.db.SaveManualInvoice(r.Context(), identityFrom(r).ID, id, in, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := 200
	if id == 0 && !result.Replayed {
		status = 201
	}
	write(w, status, result)
}

func (s *Server) cancelManualInvoice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in store.ManualInvoiceCancel
	if !decode(w, r, &in) {
		return
	}
	result, err := s.db.CancelManualInvoice(r.Context(), identityFrom(r).ID, id, in, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, result)
}

func (s *Server) mergeManualInvoices(w http.ResponseWriter, r *http.Request) {
	var in store.ManualInvoiceMergeInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.db.MergeManualInvoices(r.Context(), identityFrom(r).ID, in, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if out.Replayed {
		status = http.StatusOK
	}
	write(w, status, out)
}
