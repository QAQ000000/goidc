package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func issuedApplication(t *testing.T, f upgradeFixture) int64 {
	t.Helper()
	profile, address, invoice := applicationInputs(t, f)
	out := request(f.h, "POST", "/api/v1/invoice-applications", store.InvoiceApplicationInput{ProfileID: profile, AddressID: address, InvoiceIDs: []int64{invoice}}, f.customer, "security-file-create")
	expect(t, out, 201)
	id := int64(data(t, out)["application_id"].(float64))
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoice-applications/%d/review", id), store.InvoiceApplicationReview{Status: "issued", DocumentNumber: "ISOLATED-INV", ExpectedVersion: 1}, f.root, "security-file-issue"), 201)
	return id
}

func waitQueryLock(t *testing.T, db *store.Store, query string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
	defer cancel()
	for {
		var waiting bool
		if err := db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query=$1)", query).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("request did not reach expected lock", query)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestInvoiceDocumentWaitingRevocationAndReplacement(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	id := issuedApplication(t, f)
	path := fmt.Sprintf("/api/v1/admin/invoice-applications/%d/document", id)
	pdf := []byte("%PDF-1.7\nisolated-file\n")
	// A revoked session while waiting for the parent row must not upload.
	tx, err := f.db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(t.Context(), "SELECT id FROM invoice_applications WHERE id=$1 FOR UPDATE", id); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- multipartRequest(f.h, path, f.root, "invoice.pdf", pdf) }()
	waitQueryLock(t, f.db, "SELECT status FROM invoice_applications WHERE id=$1 FOR UPDATE")
	if _, err = f.db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(cookieValue(f.root))); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-done:
		expect(t, out, 401)
	case <-time.After(7 * time.Second):
		t.Fatal("waiting upload stalled")
	}
	var count int
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM invoice_application_documents").Scan(&count); err != nil || count != 0 {
		t.Fatal("revoked upload wrote file", err)
	}
	var username string
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT username FROM users WHERE role='super_admin' ORDER BY id LIMIT 1").Scan(&username); err != nil {
		t.Fatal(err)
	}
	root := request(f.h, "POST", "/api/v1/auth/login", map[string]string{"username": username, "password": "integration-password"}, "", "")
	expect(t, root, 200)
	cookie := root.Result().Cookies()[0]
	admin := cookie.Name + "=" + cookie.Value
	uploaded := multipartRequest(f.h, path, admin, "invoice.pdf", pdf)
	expect(t, uploaded, 201)
	docID := int64(data(t, uploaded)["id"].(float64))
	expect(t, multipartRequest(f.h, path, admin, "replacement.pdf", []byte("%PDF-1.7\nreplacement")), 409)
	for _, invalidXML := range []string{"only text", "<!-- no root -->", "<a/><b/>", "<a/>trailing"} {
		expect(t, multipartRequest(f.h, path, admin, "invoice.xml", []byte(invalidXML)), 400)
	}
	var hash string
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT sha256 FROM invoice_application_documents WHERE id=$1", docID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	part, _ := writer.CreateFormFile("file", "replacement.xml")
	part.Write([]byte("<invoice><number>isolated</number></invoice>"))
	writer.WriteField("expected_sha256", hash)
	writer.Close()
	r := httptest.NewRequest("POST", path, &payload)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	r.Header.Set("Cookie", admin)
	r.Header.Set("Origin", "http://localhost:3088")
	r.Header.Set("X-Requested-With", "zjmfgo")
	response := httptest.NewRecorder()
	f.h.ServeHTTP(response, r)
	expect(t, response, 201)
	// Download waits on the file before checking the current session.
	tx, err = f.db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(t.Context(), "SELECT id FROM invoice_application_documents WHERE id=$1 FOR UPDATE", docID); err != nil {
		t.Fatal(err)
	}
	download := fmt.Sprintf("/api/v1/invoice-applications/%d/documents/%d/file", id, docID)
	go func() { done <- request(f.h, "GET", download, nil, f.customer, "") }()
	waitQueryLock(t, f.db, "SELECT filename,mime_type,sha256,content,storage_object_id FROM invoice_application_documents WHERE id=$1 AND application_id=$2 FOR UPDATE")
	if _, err = f.db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(cookieValue(f.customer))); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-done:
		expect(t, out, 401)
		if strings.Contains(out.Body.String(), "<invoice>") {
			t.Fatal("revoked download leaked bytes")
		}
	case <-time.After(7 * time.Second):
		t.Fatal("download stalled")
	}
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT downloads FROM invoice_application_documents WHERE id=$1", docID).Scan(&count); err != nil || count != 0 {
		t.Fatal("denied download counted", err)
	}
}

func TestInvoiceFileWaitingManagePermissionRevocation(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	id := issuedApplication(t, f)
	staffID, staff := user(t, f.db, f.h, "file-waiting-staff", "admin")
	tx, err := f.db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(t.Context(), "SELECT id FROM invoice_applications WHERE id=$1 FOR UPDATE", id); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- multipartRequest(f.h, fmt.Sprintf("/api/v1/admin/invoice-applications/%d/document", id), staff, "file.pdf", []byte("%PDF-1.7\nprivate"))
	}()
	waitQueryLock(t, f.db, "SELECT status FROM invoice_applications WHERE id=$1 FOR UPDATE")
	// Direct SQL simulates a change during the wait; normal role management also takes 70108190.
	if _, err = tx.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['invoice_applications.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-done:
		expect(t, out, 403)
	case <-time.After(7 * time.Second):
		t.Fatal("permission wait stalled")
	}
	var count int
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM invoice_application_documents").Scan(&count); err != nil || count != 0 {
		t.Fatal("revoked grant uploaded file", err)
	}
}
