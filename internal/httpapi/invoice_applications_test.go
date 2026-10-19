package httpapi

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func applicationInputs(t *testing.T, f upgradeFixture) (int64, int64, int64) {
	t.Helper()
	profile := request(f.h, "POST", "/api/v1/invoice-profiles", store.InvoiceProfileInput{ProfileType: "company", Title: "测试企业", TaxNumber: "91310000TEST"}, f.customer, "")
	expect(t, profile, 201)
	pid := int64(data(t, profile)["id"].(float64))
	addr := request(f.h, "POST", "/api/v1/invoice-addresses", store.InvoiceAddressInput{Recipient: "测试客户", Phone: "13800000000", Address: "测试路 1 号", IsDefault: true}, f.customer, "")
	expect(t, addr, 201)
	aid := int64(data(t, addr)["id"].(float64))
	iid := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "application-manual-create")
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", iid), map[string]any{"expected_version": 1}, f.customer, "application-manual-pay"), 201)
	return pid, aid, iid
}

func TestInvoiceApplicationsBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "application-super", func(db *store.Store, h http.Handler) {
		f := setupConfigurationUpgradeFixture(t, db, h, false)
		applicationInputs(t, f)
	})
}

func TestInvoiceApplicationLifecycle(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	pid, aid, iid := applicationInputs(t, f)
	in := store.InvoiceApplicationInput{ProfileID: pid, AddressID: aid, InvoiceIDs: []int64{iid}}
	w := request(f.h, "POST", "/api/v1/invoice-applications", in, f.customer, "application-create-1")
	expect(t, w, 201)
	appID := int64(data(t, w)["application_id"].(float64))
	path := fmt.Sprintf("/api/v1/invoice-applications/%d", appID)
	expect(t, request(f.h, "POST", "/api/v1/invoice-applications", in, f.customer, "application-create-1"), 200)
	expect(t, request(f.h, "POST", "/api/v1/invoice-applications", in, f.customer, "application-create-2"), 409)
	expect(t, request(f.h, "GET", path, nil, f.other, ""), 404)
	expect(t, request(f.h, "PUT", fmt.Sprintf("/api/v1/invoice-profiles/%d", pid), store.InvoiceProfileInput{ProfileType: "individual", Title: "修改后的姓名", ExpectedVersion: 1}, f.customer, ""), 200)
	expect(t, request(f.h, "PUT", fmt.Sprintf("/api/v1/invoice-addresses/%d", aid), store.InvoiceAddressInput{Recipient: "修改收件人", Phone: "13900000000", Address: "修改路 2 号", ExpectedVersion: 1}, f.customer, ""), 200)
	detail := data(t, request(f.h, "GET", path, nil, f.customer, ""))
	if detail["amount_cents"] != float64(3000) || detail["profile_snapshot"].(map[string]any)["title"] != "测试企业" || detail["address_snapshot"].(map[string]any)["recipient"] != "测试客户" {
		t.Fatal("application snapshot changed", detail)
	}
	expect(t, request(f.h, "DELETE", fmt.Sprintf("/api/v1/invoice-profiles/%d", pid), map[string]any{"expected_version": 2}, f.customer, ""), 409)
	refundPath := fmt.Sprintf("/api/v1/admin/invoices/%d/refund", iid)
	expect(t, request(f.h, "POST", refundPath, map[string]any{}, f.root, "application-pending-refund"), 409)
	reviewPath := fmt.Sprintf("/api/v1/admin/invoice-applications/%d/review", appID)
	review := store.InvoiceApplicationReview{Status: "rejected", ReviewNote: "资料不完整", ExpectedVersion: 1}
	expect(t, request(f.h, "POST", reviewPath, review, f.customer, "application-denied-review"), 401)
	expect(t, request(f.h, "POST", reviewPath, review, f.root, "application-review-1"), 201)
	expect(t, request(f.h, "POST", reviewPath, review, f.root, "application-review-1"), 200)
	expect(t, request(f.h, "POST", reviewPath, review, f.root, "application-stale-review"), 409)
	w = request(f.h, "POST", "/api/v1/invoice-applications", in, f.customer, "application-create-3")
	expect(t, w, 201)
	nextID := int64(data(t, w)["application_id"].(float64))
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoice-applications/%d/review", nextID), review, f.root, "application-review-1"), 409)
	issue := store.InvoiceApplicationReview{Status: "issued", DocumentNumber: "TEST-20261006", Carrier: "测试快递", TrackingNumber: "TEST-TRACK", ExpectedVersion: 1}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoice-applications/%d/review", nextID), issue, f.root, "application-issue-1"), 201)
	expect(t, request(f.h, "POST", refundPath, map[string]any{}, f.root, "application-issued-refund"), 409)
}

func TestInvoiceElectronicFileAndAdjustmentControls(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	pid, aid, iid := applicationInputs(t, f)
	in := store.InvoiceApplicationInput{ProfileID: pid, AddressID: aid, InvoiceIDs: []int64{iid}}
	created := request(f.h, "POST", "/api/v1/invoice-applications", in, f.customer, "efile-create-1")
	expect(t, created, 201)
	appID := int64(data(t, created)["application_id"].(float64))
	review := request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoice-applications/%d/review", appID), store.InvoiceApplicationReview{Status: "issued", DocumentNumber: "INV-EFILE-1", ExpectedVersion: 1}, f.root, "efile-review-1")
	expect(t, review, 201)
	path := fmt.Sprintf("/api/v1/admin/invoice-applications/%d/document", appID)
	pdf := []byte("%PDF-1.7\ninvoice\n")
	expect(t, multipartRequest(f.h, path, f.root, "invoice.pdf", pdf), 201)
	badXML := []byte("<?xml version=\"1.0\"?><!DOCTYPE invoice [<!ENTITY xxe SYSTEM \"file:///etc/passwd\">]><invoice>&xxe;</invoice>")
	expect(t, multipartRequest(f.h, path, f.root, "invoice.xml", badXML), 400)
	detail := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/invoice-applications/%d", appID), nil, f.customer, ""))
	docs := detail["documents"].([]any)
	if len(docs) != 1 || docs[0].(map[string]any)["sha256"] != fmt.Sprintf("%x", sha256.Sum256(pdf)) {
		t.Fatalf("unexpected electronic file projection: %#v", detail["documents"])
	}
	docID := int64(docs[0].(map[string]any)["id"].(float64))
	downloaded := request(f.h, "GET", fmt.Sprintf("/api/v1/invoice-applications/%d/documents/%d/file", appID, docID), nil, f.customer, "")
	expect(t, downloaded, 200)
	if downloaded.Body.String() != string(pdf) || downloaded.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("electronic file download policy/content mismatch: %d %q %q", downloaded.Code, downloaded.Body.String(), downloaded.Header().Get("Cache-Control"))
	}
	expect(t, request(f.h, "GET", fmt.Sprintf("/api/v1/invoice-applications/%d/documents/%d/file", appID, docID), nil, f.other, ""), 404)
	adjustPath := fmt.Sprintf("/api/v1/admin/invoice-applications/%d/adjust", appID)
	adjust := store.InvoiceApplicationAdjustment{Action: "red", DocumentNumber: "RED-EFILE-1", Note: "客户要求红冲", ExpectedVersion: 2}
	first := request(f.h, "POST", adjustPath, adjust, f.root, "efile-adjust-1")
	expect(t, first, 201)
	second := request(f.h, "POST", adjustPath, adjust, f.root, "efile-adjust-1")
	expect(t, second, 200)
	if !data(t, second)["replayed"].(bool) {
		t.Fatal("adjustment replay was not marked")
	}
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION invoice_efile_reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='invoice_application.adjusted' THEN RAISE EXCEPTION 'efile audit rejected'; END IF; RETURN NEW; END $$; CREATE TRIGGER invoice_efile_reject_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION invoice_efile_reject_audit()`); err != nil {
		t.Fatal(err)
	}
	failed := request(f.h, "POST", adjustPath, store.InvoiceApplicationAdjustment{Action: "void", Note: "人工作废", ExpectedVersion: 3}, f.root, "efile-adjust-failed")
	expect(t, failed, 500)
	var count int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM invoice_application_adjustments WHERE application_id=$1", appID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit failure left adjustment: %d %v", count, err)
	}
}

func TestInvoiceApplicationConcurrentAndOwnership(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	pid, aid, iid := applicationInputs(t, f)
	in := store.InvoiceApplicationInput{ProfileID: pid, AddressID: aid, InvoiceIDs: []int64{iid}}
	expect(t, request(f.h, "POST", "/api/v1/invoice-applications", in, f.other, "application-other-create"), 404)
	expect(t, request(f.h, "PUT", fmt.Sprintf("/api/v1/invoice-profiles/%d", pid), store.InvoiceProfileInput{ProfileType: "individual", Title: "越权", ExpectedVersion: 1}, f.other, ""), 404)
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			results <- request(f.h, "POST", "/api/v1/invoice-applications", in, f.customer, fmt.Sprintf("application-race-%d", n)).Code
		}(n)
	}
	wg.Wait()
	close(results)
	created, conflict := 0, 0
	for code := range results {
		if code == 201 {
			created++
		}
		if code == 409 {
			conflict++
		}
	}
	if created != 1 || conflict != 1 {
		t.Fatal("duplicate application race", created, conflict)
	}
	var count int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM invoice_applications").Scan(&count); err != nil || count != 1 {
		t.Fatal("application race count", count, err)
	}
}

func TestInvoiceApplicationPermissionsAndScopeReplay(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	pid, aid, iid := applicationInputs(t, f)
	w := request(f.h, "POST", "/api/v1/invoice-applications", store.InvoiceApplicationInput{ProfileID: pid, AddressID: aid, InvoiceIDs: []int64{iid}}, f.customer, "application-scope-create")
	expect(t, w, 201)
	id := int64(data(t, w)["application_id"].(float64))
	path := fmt.Sprintf("/api/v1/admin/invoice-applications/%d", id)
	staffID, staff := user(t, f.db, f.h, "application-staff", "admin")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['invoices.view','invoices.manage'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "GET", path, nil, staff, ""), 403)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['invoice_applications.view','invoice_applications.manage'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1);", staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='owned' WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	review := store.InvoiceApplicationReview{Status: "rejected", ReviewNote: "补充资料", ExpectedVersion: 1}
	expect(t, request(f.h, "GET", path, nil, staff, ""), 404)
	expect(t, request(f.h, "POST", path+"/review", review, staff, "application-scope-review"), 404)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", f.uid, staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "GET", path, nil, staff, ""), 200)
	expect(t, request(f.h, "POST", path+"/review", review, staff, "application-scope-review"), 201)
	expect(t, request(f.h, "POST", path+"/review", review, staff, "application-scope-review"), 200)
	salesAssignment(t, f.h, f.root, f.uid, nil, 1)
	expect(t, request(f.h, "POST", path+"/review", review, staff, "application-scope-review"), 404)
	page := data(t, request(f.h, "GET", "/api/v1/admin/invoice-applications", nil, staff, ""))
	if page["total"] != float64(0) {
		t.Fatal("application list leaked transferred customer", page)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['invoice_applications.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path+"/review", review, staff, "application-scope-review"), 403)
}

func TestInvoiceApplicationAuditRollbackAndDefaultAddress(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	pid, aid, iid := applicationInputs(t, f)
	in := store.InvoiceApplicationInput{ProfileID: pid, AddressID: aid, InvoiceIDs: []int64{iid}}
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_application_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'invoice_application.%' THEN RAISE EXCEPTION 'application audit rejected'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_application_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_application_audit()`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", "/api/v1/invoice-applications", in, f.customer, "application-audit-create"), 500)
	var count int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM invoice_applications").Scan(&count); err != nil || count != 0 {
		t.Fatal("audit failure left application", count, err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE audit_events DISABLE TRIGGER reject_application_audit"); err != nil {
		t.Fatal(err)
	}
	w := request(f.h, "POST", "/api/v1/invoice-applications", in, f.customer, "application-audit-create")
	expect(t, w, 201)
	id := int64(data(t, w)["application_id"].(float64))
	if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE audit_events ENABLE TRIGGER reject_application_audit"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoice-applications/%d/review", id), store.InvoiceApplicationReview{Status: "issued", DocumentNumber: "TEST-AUDIT", ExpectedVersion: 1}, f.root, "application-audit-review"), 500)
	detail := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/invoice-applications/%d", id), nil, f.customer, ""))
	if detail["status"] != "pending" || detail["version"] != float64(1) {
		t.Fatal("audit failure changed application", detail)
	}
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			results <- request(f.h, "POST", "/api/v1/invoice-addresses", store.InvoiceAddressInput{Recipient: fmt.Sprintf("收件人%d", n), Phone: "13800000000", Address: "并发默认地址", IsDefault: true}, f.customer, "").Code
		}(n)
	}
	wg.Wait()
	close(results)
	for code := range results {
		if code != 201 {
			t.Fatal("default address creation", code)
		}
	}
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM invoice_addresses WHERE user_id=$1 AND is_default", f.uid).Scan(&count); err != nil || count != 1 {
		t.Fatal("default address uniqueness", count, err)
	}
	expect(t, request(f.h, "PUT", fmt.Sprintf("/api/v1/invoice-addresses/%d", aid), store.InvoiceAddressInput{Recipient: "旧地址", Phone: "13800000000", Address: "旧地址", IsDefault: true, ExpectedVersion: 1}, f.customer, ""), 409)
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM invoice_addresses WHERE user_id=$1 AND is_default", f.uid).Scan(&count); err != nil || count != 1 {
		t.Fatal("stale edit changed default", count, err)
	}
}
