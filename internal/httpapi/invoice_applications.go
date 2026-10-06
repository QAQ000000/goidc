package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func (s *Server) invoiceProfiles(w http.ResponseWriter, r *http.Request) {
	uid := identityFrom(r).ID
	if r.Method == http.MethodGet {
		items, err := s.db.ListInvoiceProfiles(r.Context(), uid)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, http.StatusOK, items)
		return
	}
	id := int64(0)
	if r.Method != http.MethodPost {
		var ok bool
		id, ok = pathID(w, r)
		if !ok {
			return
		}
	}
	if r.Method == http.MethodDelete {
		var in struct {
			ExpectedVersion int64 `json:"expected_version"`
		}
		if !decode(w, r, &in) {
			return
		}
		if err := s.db.DeleteInvoiceProfile(r.Context(), uid, id, in.ExpectedVersion); err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, http.StatusOK, map[string]any{"deleted": true})
		return
	}
	var in store.InvoiceProfileInput
	if !decode(w, r, &in) {
		return
	}
	profileID, err := s.db.SaveInvoiceProfile(r.Context(), uid, id, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if id == 0 {
		status = http.StatusCreated
	}
	write(w, status, map[string]any{"id": profileID})
}

func (s *Server) invoiceAddresses(w http.ResponseWriter, r *http.Request) {
	uid := identityFrom(r).ID
	if r.Method == http.MethodGet {
		items, err := s.db.ListInvoiceAddresses(r.Context(), uid)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, http.StatusOK, items)
		return
	}
	id := int64(0)
	if r.Method != http.MethodPost {
		var ok bool
		id, ok = pathID(w, r)
		if !ok {
			return
		}
	}
	if r.Method == http.MethodDelete {
		var in struct {
			ExpectedVersion int64 `json:"expected_version"`
		}
		if !decode(w, r, &in) {
			return
		}
		if err := s.db.DeleteInvoiceAddress(r.Context(), uid, id, in.ExpectedVersion); err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, http.StatusOK, map[string]any{"deleted": true})
		return
	}
	var in store.InvoiceAddressInput
	if !decode(w, r, &in) {
		return
	}
	addressID, err := s.db.SaveInvoiceAddress(r.Context(), uid, id, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if id == 0 {
		status = http.StatusCreated
	}
	write(w, status, map[string]any{"id": addressID})
}

func (s *Server) invoiceableInvoices(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	s.transactionPage(w, r, "i.id,i.invoice_no,i.amount_cents,i.paid_at,i.invoice_kind", ` FROM invoices i WHERE i.user_id=$1 AND i.status='paid' AND i.amount_cents>0 AND NOT i.import_review_required AND i.legacy_source='' AND NOT EXISTS(SELECT 1 FROM invoice_application_items x JOIN invoice_applications a ON a.id=x.application_id WHERE x.source_invoice_id=i.id AND a.status IN ('pending','issued'))`, "i.id", []any{identityFrom(r).ID}, limit, offset)
}

func (s *Server) invoiceApplicationDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	actor := identityFrom(r).ID
	admin := adminRoute(r)
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(ctx)
	if admin {
		var customerID int64
		if err = tx.QueryRow(ctx, "SELECT user_id FROM invoice_applications WHERE id=$1", id).Scan(&customerID); err != nil {
			s.fail(w, r, err)
			return
		}
		if err = store.LockAdminCustomer(ctx, tx, actor, customerID); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	projection := `a.id,a.user_id,a.status,a.amount_cents,a.profile_snapshot,a.address_snapshot,a.document_number,a.carrier,a.tracking_number,a.review_note,a.version,a.created_at,a.reviewed_at`
	if admin {
		projection += ",u.display_name AS customer_name,u.email AS customer_email"
	}
	rows, err := tx.Query(ctx, "SELECT "+projection+` FROM invoice_applications a JOIN users u ON u.id=a.user_id WHERE a.id=$1 AND ($2::boolean OR a.user_id=$3)`, id, admin, actor)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	item, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := collectTransactionRows(tx.Query(ctx, `SELECT source_invoice_id,invoice_no,description,amount_cents FROM invoice_application_items WHERE application_id=$1 ORDER BY id`, id))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	item["items"] = items
	if err = tx.Commit(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, item)
}

func (s *Server) invoiceApplications(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	admin := adminRoute(r)
	args := []any{admin, identityFrom(r).ID}
	where := "(($1::boolean AND admin_customer_allowed($2,a.user_id)) OR (NOT $1::boolean AND a.user_id=$2))"
	if status := strings.TrimSpace(r.URL.Query().Get("status")); status != "" {
		if status != "pending" && status != "rejected" && status != "issued" {
			writeError(w, 400, "invalid_status", "发票申请状态无效")
			return
		}
		args = append(args, status)
		where += " AND a.status=$" + strconv.Itoa(len(args))
	}
	projection := "a.id,a.user_id,a.status,a.amount_cents,a.review_note,a.document_number,a.carrier,a.tracking_number,a.reviewer_id,a.reviewed_at,a.version,a.created_at,a.updated_at,a.profile_snapshot->>'profile_type' AS profile_type,a.profile_snapshot->>'title' AS title,a.address_snapshot->>'recipient' AS recipient,a.address_snapshot->>'address' AS address"
	if admin {
		projection += ",u.display_name AS customer_name,u.email AS customer_email"
	}
	from := " FROM invoice_applications a JOIN users u ON u.id=a.user_id WHERE " + where
	s.transactionPage(w, r, projection, from, "a.id", args, limit, offset)
}

func (s *Server) createInvoiceApplication(w http.ResponseWriter, r *http.Request) {
	var in store.InvoiceApplicationInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.db.CreateInvoiceApplication(r.Context(), identityFrom(r).ID, in, r.Header.Get("Idempotency-Key"))
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

func (s *Server) reviewInvoiceApplication(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in store.InvoiceApplicationReview
	if !decode(w, r, &in) {
		return
	}
	out, err := s.db.ReviewInvoiceApplication(r.Context(), identityFrom(r).ID, id, in, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if !out.Replayed {
		status = http.StatusCreated
	}
	write(w, status, out)
}
