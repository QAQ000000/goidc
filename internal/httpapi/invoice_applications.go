package httpapi

import (
	"errors"
	"io"
	"mime"
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
	documents, err := collectTransactionRows(tx.Query(ctx, `SELECT id,adjustment_id,filename,mime_type,size_bytes,sha256,downloads,created_at FROM invoice_application_documents WHERE application_id=$1 ORDER BY id`, id))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, doc := range documents {
		if docID, ok := doc["id"].(int64); ok {
			prefix := "/api/v1/invoice-applications/"
			if admin {
				prefix = "/api/v1/admin/invoice-applications/"
			}
			doc["download_url"] = prefix + strconv.FormatInt(id, 10) + "/documents/" + strconv.FormatInt(docID, 10) + "/file"
		}
	}
	adjustments, err := collectTransactionRows(tx.Query(ctx, `SELECT id,action,document_number,note,actor_id,created_at FROM invoice_application_adjustments WHERE application_id=$1 ORDER BY id`, id))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	item["documents"], item["adjustments"] = documents, adjustments
	if err = tx.Commit(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, item)
}

func invoiceDocumentID(r *http.Request) (int64, int64, bool) {
	appID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	docID, err2 := strconv.ParseInt(r.PathValue("document_id"), 10, 64)
	return appID, docID, err == nil && err2 == nil && appID > 0 && docID > 0
}

func (s *Server) uploadInvoiceApplicationDocument(w http.ResponseWriter, r *http.Request) {
	appID, ok := pathID(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, (20<<20)+(256<<10))
	if err := r.ParseMultipartForm(32 << 10); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, 413, "invoice_document_too_large", "电子发票文件不能超过 20 MiB")
		} else {
			writeError(w, 400, "invalid_multipart", "文件上传格式无效")
		}
		return
	}
	defer r.MultipartForm.RemoveAll()
	files := r.MultipartForm.File["file"]
	if len(files) != 1 {
		writeError(w, 400, "invalid_invoice_document", "请选择一个电子发票文件")
		return
	}
	header := files[0]
	name := header.Filename
	_, params, err := mime.ParseMediaType(header.Header.Get("Content-Disposition"))
	if err != nil || params["filename"] != name || strings.ContainsAny(name, "/\\\r\n\x00") {
		writeError(w, 400, "invalid_invoice_document", "文件名无效")
		return
	}
	stream, err := header.Open()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	content, err := io.ReadAll(io.LimitReader(stream, (20<<20)+1))
	stream.Close()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(content) > 20<<20 {
		writeError(w, 413, "invoice_document_too_large", "电子发票文件不能超过 20 MiB")
		return
	}
	mt := header.Header.Get("Content-Type")
	if strings.HasSuffix(strings.ToLower(name), ".pdf") && len(content) >= 5 && string(content[:5]) == "%PDF-" {
		mt = "application/pdf"
	}
	if strings.HasSuffix(strings.ToLower(name), ".xml") && strings.HasPrefix(strings.TrimSpace(string(content)), "<") {
		mt = "application/xml"
	}
	if mt == "" {
		mt = "application/octet-stream"
	}
	id, err := s.db.SaveInvoiceApplicationDocument(r.Context(), identityFrom(r).ID, appID, 0, store.InvoiceApplicationDocument{Filename: name, MIME: mt, Content: content})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusCreated, map[string]any{"id": id, "application_id": appID, "provider": "manual", "tax_service": "not_connected"})
}

func (s *Server) adjustInvoiceApplication(w http.ResponseWriter, r *http.Request) {
	appID, ok := pathID(w, r)
	if !ok {
		return
	}
	var in store.InvoiceApplicationAdjustment
	if !decode(w, r, &in) {
		return
	}
	out, err := s.db.AdjustInvoiceApplication(r.Context(), identityFrom(r).ID, appID, in, r.Header.Get("Idempotency-Key"))
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

func (s *Server) invoiceApplicationDocumentFile(w http.ResponseWriter, r *http.Request) {
	appID, docID, ok := invoiceDocumentID(r)
	if !ok {
		writeError(w, 400, "invalid_invoice_document", "电子发票文件编号无效")
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var owner int64
	var name, mt, hash string
	var content []byte
	if err = tx.QueryRow(r.Context(), `SELECT a.user_id,d.filename,d.mime_type,d.sha256,d.content FROM invoice_application_documents d JOIN invoice_applications a ON a.id=d.application_id WHERE d.id=$1 AND d.application_id=$2`, docID, appID).Scan(&owner, &name, &mt, &hash, &content); err != nil {
		s.fail(w, r, err)
		return
	}
	actor := identityFrom(r).ID
	if current, authErr := s.publicDownloadIdentity(r, tx); authErr != nil {
		s.fail(w, r, authErr)
		return
	} else if current != actor {
		writeError(w, http.StatusUnauthorized, "unauthorized", "登录已失效")
		return
	}
	if adminRoute(r) {
		if err = store.LockAdminCustomer(r.Context(), tx, actor, owner); err != nil {
			s.fail(w, r, err)
			return
		}
	} else if owner != actor {
		writeError(w, 404, "not_found", "文件不存在")
		return
	}
	if storedFileTransfersBytes(r, name, mt, hash, content) {
		if _, err = tx.Exec(r.Context(), "UPDATE invoice_application_documents SET downloads=downloads+1 WHERE id=$1", docID); err != nil {
			s.fail(w, r, err)
			return
		}
		if err = store.Audit(r.Context(), tx, actor, "invoice_application.document_downloaded", "invoice_application_document", docID, map[string]any{"application_id": appID, "customer_id": owner, "admin": adminRoute(r)}); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	serveStoredFile(w, r, name, mt, hash, content)
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
