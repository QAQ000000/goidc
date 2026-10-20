package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func externalRefundPaidInvoice(t *testing.T, db *store.Store, h http.Handler, root, customer string, uid int64, key string) (int64, map[string]any) {
	t.Helper()
	invoice := manualInvoiceCreate(t, h, root, manualInvoiceInput(uid), key+"-invoice")
	path := fmt.Sprintf("/api/v1/invoices/%d", invoice)
	w := request(h, "POST", path+"/payments", map[string]any{"provider": "mock", "expected_version": 1}, customer, key+"-payment")
	expect(t, w, 201)
	payment := data(t, w)
	if _, err := db.ProcessPaymentWebhook(t.Context(), "mock", key+"-event", payment["provider_payment_id"].(string), invoice, 3000, "succeeded", []byte(`{"status":"succeeded"}`)); err != nil {
		t.Fatal(err)
	}
	var pid int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM payment_transactions WHERE invoice_id=$1", invoice).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	return invoice, map[string]any{"payment_id": pid, "amount_cents": 3000, "method": "original_route", "provider_refund_id": key + "-refund", "received_at": time.Now().UTC().Format(time.RFC3339Nano), "evidence_ref": "test://receipt/" + key, "evidence_sha256": strings.Repeat("a", 64), "note": "隔离退款到账凭证", "expected_version": 2}
}

func externalRefundActor(t *testing.T, db *store.Store, h http.Handler, name string, permissions []string) (int64, string) {
	t.Helper()
	id, cookie := user(t, db, h, name, "admin")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=$2 WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", id, permissions); err != nil {
		t.Fatal(err)
	}
	return id, cookie
}

func TestExternalRefundHTTPPermissionsLifecycleAndReplay(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	invoice, in := externalRefundPaidInvoice(t, f.db, f.h, f.root, f.customer, f.uid, "external-lifecycle")
	path := fmt.Sprintf("/api/v1/admin/invoices/%d/external-refunds", invoice)
	requesterID, requester := externalRefundActor(t, f.db, f.h, "refund-requester", []string{"external_refunds.view", "external_refunds.create"})
	_, reviewer := externalRefundActor(t, f.db, f.h, "refund-reviewer", []string{"external_refunds.view", "external_refunds.review"})
	_, reader := externalRefundActor(t, f.db, f.h, "refund-reader", []string{"external_refunds.view"})
	expect(t, request(f.h, "POST", path, in, reader, "refund-denied"), 403)
	expect(t, request(f.h, "POST", path, in, f.customer, "refund-customer"), 401)
	expect(t, requestBearer(f.h, "POST", path, in, "invalid-api-key"), 403)
	page := data(t, request(f.h, "GET", path+"?limit=1&offset=0", nil, reader, ""))
	if page["total"] != float64(0) || len(page["items"].([]any)) != 0 {
		t.Fatal("not a real empty page", page)
	}
	expect(t, request(f.h, "POST", path, in, requester, ""), 409)
	in["expected_version"] = 1
	expect(t, request(f.h, "POST", path, in, requester, "refund-stale"), 409)
	in["expected_version"] = 2
	w := request(f.h, "POST", path, in, requester, "refund-create")
	expect(t, w, 201)
	refund := data(t, w)["refund"].(map[string]any)

	incomeBefore := data(t, request(f.h, "GET", "/api/v1/admin/reports/income", nil, f.root, ""))
	summaryPath := fmt.Sprintf("/api/v1/admin/transactions/summary?customer_id=%d", f.uid)
	summaryBefore := data(t, request(f.h, "GET", summaryPath, nil, f.root, ""))
	if summaryBefore["gateway_income_cents"] != float64(3000) || summaryBefore["external_refund_cents"] != float64(0) || summaryBefore["net_gateway_cents"] != float64(3000) {
		t.Fatal("pending refund changed cash summary", summaryBefore)
	}
	review := fmt.Sprintf("%s/%.0f/confirm", path, refund["id"])
	expect(t, request(f.h, "POST", path, in, requester, "refund-create"), 200)
	in["note"] = "different payload"
	expect(t, request(f.h, "POST", path, in, requester, "refund-create"), 409)
	in["note"] = "隔离退款到账凭证"
	expect(t, request(f.h, "POST", review, map[string]any{"expected_version": 1, "note": "review"}, reader, "reader-confirm"), 403)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=array_append(permissions,'external_refunds.review') WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", requesterID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", review, map[string]any{"expected_version": 1, "note": "self"}, requester, "self-confirm"), 403)
	expect(t, request(f.h, "POST", review, map[string]any{"expected_version": 99, "note": "stale"}, reviewer, "stale-confirm"), 409)
	expect(t, request(f.h, "POST", review, map[string]any{"expected_version": 1, "note": "独立核对到账"}, reviewer, "confirm-refund"), 201)
	expect(t, request(f.h, "POST", review, map[string]any{"expected_version": 1, "note": "独立核对到账"}, reviewer, "confirm-refund"), 200)

	summaryAfter := data(t, request(f.h, "GET", summaryPath, nil, f.root, ""))
	if summaryAfter["gateway_income_cents"] != float64(3000) || summaryAfter["external_refund_cents"] != float64(3000) || summaryAfter["net_gateway_cents"] != float64(0) {
		t.Fatal("confirmed refund cash summary", summaryAfter)
	}
	incomeAfter := data(t, request(f.h, "GET", "/api/v1/admin/reports/income", nil, f.root, ""))
	if fmt.Sprint(incomeBefore["total"]) != fmt.Sprint(incomeAfter["total"]) {
		t.Fatal("refund erased original gross income", incomeBefore, incomeAfter)
	}
	transactions := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/admin/transactions/page?customer_id=%d", f.uid), nil, f.root, ""))
	payments, refunds := 0, 0
	for _, item := range transactions["items"].([]any) {
		row := item.(map[string]any)
		if row["source"] == "payment" {
			payments++
		}
		if row["source"] == "external_refund" {
			refunds++
		}
	}
	if payments != 1 || refunds != 1 {
		t.Fatal("cash source traceability", transactions)
	}

	customerTransactions := request(f.h, "GET", fmt.Sprintf("/api/v1/admin/customer-transactions?customer_id=%d", f.uid), nil, f.root, "")
	expect(t, customerTransactions, 200)
	var customerRows struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(customerTransactions.Body.Bytes(), &customerRows); err != nil {
		t.Fatal(err)
	}
	customerRefunds := 0
	for _, row := range customerRows.Data {
		if row["kind"] == "external_refund" {
			customerRefunds++
			if row["delta_cents"] != float64(-3000) || row["invoice_id"] != float64(invoice) {
				t.Fatal(row)
			}
		}
	}
	if customerRefunds != 1 {
		t.Fatal("customer refund trace missing", customerRows)
	}
	var status, pstatus string
	var ledger int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT i.status,p.status,(SELECT count(*) FROM balance_ledger WHERE reference_type='invoice' AND reference_id=i.id) FROM invoices i JOIN payment_transactions p ON p.invoice_id=i.id WHERE i.id=$1", invoice).Scan(&status, &pstatus, &ledger); err != nil || status != "refunded" || pstatus != "succeeded" || ledger != 0 {
		t.Fatal("external refund changed original receipt/balance", status, pstatus, ledger, err)
	}
	var providerPaymentID string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT provider_payment_id FROM payment_transactions WHERE invoice_id=$1", invoice).Scan(&providerPaymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ProcessPaymentWebhook(t.Context(), "mock", "external-lifecycle-event", providerPaymentID, invoice, 3000, "succeeded", []byte(`{"status":"succeeded"}`)); err != nil {
		t.Fatal("original success replay failed after refund", err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM invoices WHERE id=$1", invoice).Scan(&status); err != nil || status != "refunded" {
		t.Fatal("notification replay reopened invoice", status, err)
	}
	// Replays must still recheck current grants, scope and session validity.
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions='{}' WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", requesterID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path, in, requester, "refund-create"), 403)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE sessions SET expires_at=now()-interval '1 second' WHERE user_id=$1", requesterID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path, in, requester, "refund-create"), 401)
}

func TestExternalRefundHTTPConcurrentCreateAndConfirm(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	invoice, in := externalRefundPaidInvoice(t, f.db, f.h, f.root, f.customer, f.uid, "external-concurrency")
	path := fmt.Sprintf("/api/v1/admin/invoices/%d/external-refunds", invoice)
	_, reviewer := externalRefundActor(t, f.db, f.h, "concurrent-reviewer", []string{"external_refunds.review"})
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes <- request(f.h, "POST", path, in, f.root, fmt.Sprintf("create-race-%d", i)).Code
		}(i)
	}
	wg.Wait()
	close(codes)
	created, conflict := 0, 0
	for code := range codes {
		if code == 201 {
			created++
		} else if code == 409 {
			conflict++
		} else {
			t.Fatalf("unexpected create status %d", code)
		}
	}
	if created != 1 || conflict != 1 {
		t.Fatal("duplicate refund accepted", created, conflict)
	}
	page := data(t, request(f.h, "GET", path, nil, f.root, ""))
	rows := page["items"].([]any)
	if page["total"] != float64(1) || len(rows) != 1 {
		t.Fatal(page)
	}
	refund := rows[0].(map[string]any)
	review := fmt.Sprintf("%s/%.0f/confirm", path, refund["id"])
	codes = make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes <- request(f.h, "POST", review, map[string]any{"expected_version": 1, "note": "concurrent review"}, reviewer, fmt.Sprintf("confirm-race-%d", i)).Code
		}(i)
	}
	wg.Wait()
	close(codes)
	created, conflict = 0, 0
	for code := range codes {
		if code == 201 {
			created++
		} else if code == 409 {
			conflict++
		} else {
			t.Fatalf("unexpected confirm status %d", code)
		}
	}
	if created != 1 || conflict != 1 {
		t.Fatal("duplicate confirmation accepted", created, conflict)
	}
}

func TestExternalRefundHTTPRejectScopeAndFailureAtomicity(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	invoice, in := externalRefundPaidInvoice(t, f.db, f.h, f.root, f.customer, f.uid, "external-reject")
	path := fmt.Sprintf("/api/v1/admin/invoices/%d/external-refunds", invoice)
	actorID, actor := externalRefundActor(t, f.db, f.h, "scope-refund", []string{"external_refunds.view", "external_refunds.create", "external_refunds.review"})
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned' WHERE id=$1", actorID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "GET", path, nil, actor, ""), 404)
	expect(t, request(f.h, "POST", path, in, actor, "out-of-scope"), 404)
	// A transaction failure after creation must not leave a pending record.
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_external_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE '%external_refund%' THEN RAISE EXCEPTION 'isolated audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_external_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_external_audit()`); err != nil {
		t.Fatal(err)
	}
	failed := request(f.h, "POST", path, in, f.root, "audit-failure")
	if failed.Code < 400 {
		t.Fatal("audit failure accepted", failed.Body.String())
	}
	page := data(t, request(f.h, "GET", path, nil, f.root, ""))
	if page["total"] != float64(0) {
		t.Fatal("failed transaction left partial record", page)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "DROP TRIGGER reject_external_audit ON audit_events"); err != nil {
		t.Fatal(err)
	}
	w := request(f.h, "POST", path, in, f.root, "audit-failure")
	expect(t, w, 201)
	refund := data(t, w)["refund"].(map[string]any)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", f.uid, actorID); err != nil {
		t.Fatal(err)
	}
	review := fmt.Sprintf("%s/%.0f/reject", path, refund["id"])
	expect(t, request(f.h, "POST", review, map[string]any{"expected_version": 1, "note": "凭证需补正"}, actor, "reject-refund"), 201)
	expect(t, request(f.h, "POST", review, map[string]any{"expected_version": 1, "note": "凭证需补正"}, actor, "reject-refund"), 200)
	var status string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM invoices WHERE id=$1", invoice).Scan(&status); err != nil || status != "paid" {
		t.Fatal("rejection changed invoice", status, err)
	}
}
