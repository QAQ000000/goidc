package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/legacy"
	"github.com/codex/zjmfgo/internal/store"
)

func archiveReviewFixture(t *testing.T, db *store.Store, entity string, id int64, record legacy.Record) {
	t.Helper()
	raw, _ := json.Marshal(record)
	sum := sha256.Sum256(raw)
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO legacy_source_records(source,entity,legacy_id,run_id,payload,checksum)
VALUES('review-test',$1,$2,(SELECT max(id) FROM legacy_import_runs),$3,$4)
ON CONFLICT(source,entity,legacy_id) DO UPDATE SET payload=excluded.payload,checksum=excluded.checksum`, entity, id, raw, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
}

func reviewFixture(t *testing.T, db *store.Store, h http.Handler) (uid, iid, sid, pid int64, customer string) {
	t.Helper()
	uid, customer = user(t, db, h, "review-customer", "customer")
	pid = product(t, db, 5)
	v, item := add(t, h, customer, pid, 1)
	w := request(h, "POST", "/api/v1/orders", map[string]any{"cart_version": v, "item_ids": []int64{item}}, customer, "review-fixture-order")
	expect(t, w, 201)
	d := data(t, w)
	oid := int64(d["order_id"].(float64))
	iid = int64(d["invoice_id"].(float64))
	var oi int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT id,order_item_id FROM services WHERE order_id=$1", oid).Scan(&sid, &oi); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		table        string
		id, legacyID int64
	}{{"users", uid, 10}, {"products", pid, 167}, {"orders", oid, 20}, {"invoices", iid, 30}, {"services", sid, 40}, {"order_items", oi, 40}} {
		if _, err := db.Pool.Exec(t.Context(), "UPDATE "+entry.table+" SET legacy_source='review-test',legacy_id=$2 WHERE id=$1", entry.id, entry.legacyID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), "INSERT INTO legacy_id_map(source,entity,legacy_id,target_id) VALUES('review-test',$1,$2,$3)", entry.table, entry.legacyID, entry.id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE invoices SET import_review_required=true WHERE id=$1", iid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET import_review_required=true,provision_mode='manual' WHERE id=$1", sid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "INSERT INTO legacy_import_runs(source,mode,status) VALUES('review-test','apply','succeeded')"); err != nil {
		t.Fatal(err)
	}
	var ii int64
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,legacy_source,legacy_id) VALUES($1,'旧服务','host',1333,'review-test',50) RETURNING id", iid).Scan(&ii); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "INSERT INTO legacy_id_map(source,entity,legacy_id,target_id) VALUES('review-test','invoice_items',50,$1)", ii); err != nil {
		t.Fatal(err)
	}
	archiveReviewFixture(t, db, "services", 40, legacy.Record{"id": "40", "uid": "10", "orderid": "20", "productid": "167", "domainstatus": "Pending", "billingcycle": "monthly", "amount": "12.34", "firstpaymentamount": "13.33", "nextduedate": "1700000000"})
	archiveReviewFixture(t, db, "invoices", 30, legacy.Record{"id": "30", "uid": "10", "status": "Unpaid", "total": "13.33", "subtotal": "13.33", "credit": "0.00", "tax": "0.00", "tax2": "0.00", "delete_time": "0", "is_delete": "0"})
	archiveReviewFixture(t, db, "invoice_items", 50, legacy.Record{"id": "50", "uid": "10", "invoice_id": "30", "type": "host", "rel_id": "40", "amount": "13.33", "delete_time": "0"})
	return
}

func TestLegacyReviewPermissionsArchiveAndVersions(t *testing.T) {
	db, h := testServer(t)
	_, iid, _, _, customer := reviewFixture(t, db, h)
	_, admin := user(t, db, h, "review-admin", "admin")
	_, super := user(t, db, h, "review-super", "super_admin")
	var sid int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM services").Scan(&sid); err != nil {
		t.Fatal(err)
	}
	serviceDetail := request(h, "GET", fmt.Sprintf("/api/v1/admin/migrations/reviews/services/%d", sid), nil, super, "")
	expect(t, serviceDetail, 200)
	if data(t, serviceDetail)["current"].(map[string]any)["amount_cents"] != float64(1234) {
		t.Fatal("missing recurring price displayed as zero")
	}
	path := fmt.Sprintf("/api/v1/admin/migrations/reviews/invoices/%d", iid)
	body := map[string]any{"version": 1, "decision": "archive", "evidence": "财务核对后保留历史账单"}
	expect(t, request(h, "GET", "/api/v1/admin/migrations/reviews", nil, "", ""), 401)
	expect(t, request(h, "GET", "/api/v1/admin/migrations/reviews", nil, customer, ""), 403)
	expect(t, request(h, "GET", "/api/v1/admin/migrations/runs", nil, admin, ""), 200)
	expect(t, request(h, "POST", path, body, admin, "review-archive-1"), 403)
	expect(t, requestBearer(h, "POST", path, body, "zjmfgo_"+strings.Repeat("a", 64)), 403)
	detail := request(h, "GET", path, nil, super, "")
	expect(t, detail, 200)
	if len(data(t, detail)["release_blockers"].([]any)) == 0 {
		t.Fatal("unreviewed service not identified")
	}
	w := request(h, "POST", path, body, super, "review-archive-1")
	expect(t, w, 201)
	if data(t, w)["version"] != float64(2) || data(t, w)["operation_blocked"] != true {
		t.Fatal("archive changed operational hold")
	}
	expect(t, request(h, "POST", path, body, super, "review-archive-1"), 200)
	expect(t, request(h, "POST", path, body, super, "review-archive-2"), 409)
	body["evidence"] = "不同的凭据"
	expect(t, request(h, "POST", path, body, super, "review-archive-1"), 409)
	listed := request(h, "GET", "/api/v1/admin/migrations/reviews?entity=invoices&state=archive", nil, admin, "")
	expect(t, listed, 200)
	if len(listData(t, listed)) != 1 {
		t.Fatal("archive filter wrong")
	}
	var n int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM legacy_review_decisions").Scan(&n); err != nil || n != 1 {
		t.Fatal("review retry duplicated decision")
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action='legacy.review.archive'").Scan(&n); err != nil || n != 1 {
		t.Fatal("missing or duplicate review audit")
	}
	_, err := db.PayInvoice(t.Context(), int64(data(t, detail)["current"].(map[string]any)["user_id"].(float64)), iid, "archive-pay-test")
	if err == nil || !strings.Contains(err.Error(), "历史账单") {
		t.Fatal("archive allowed payment")
	}
}

func TestLegacyReviewReleasesVerifiedPendingCommerce(t *testing.T) {
	db, h := testServer(t)
	uid, iid, sid, _, customer := reviewFixture(t, db, h)
	_, super := user(t, db, h, "release-super", "super_admin")
	servicePath := fmt.Sprintf("/api/v1/admin/migrations/reviews/services/%d", sid)
	invoicePath := fmt.Sprintf("/api/v1/admin/migrations/reviews/invoices/%d", iid)
	body := map[string]any{"version": 1, "decision": "release", "evidence": "核对客户、账期、首次应收和配置", "state": "pending_payment", "recurring_price_cents": 1234}
	expect(t, request(h, "POST", invoicePath, map[string]any{"version": 1, "decision": "release", "evidence": "应收核对一致"}, super, "invoice-before-service"), 409)
	expect(t, request(h, "POST", servicePath, body, super, "release-service-1"), 201)
	detail := request(h, "GET", invoicePath, nil, super, "")
	expect(t, detail, 200)
	if len(data(t, detail)["release_blockers"].([]any)) != 0 {
		t.Fatal("verified invoice still blocked")
	}
	expect(t, request(h, "POST", invoicePath, map[string]any{"version": 1, "decision": "release", "evidence": "核对来源订单与全部账单项目"}, super, "release-invoice-1"), 201)
	var n int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs").Scan(&n); err != nil || n != 0 {
		t.Fatal("review created provision jobs")
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET balance_cents=5000 WHERE id=$1", uid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", iid), map[string]any{}, customer, "released-invoice-pay"), 201)
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs").Scan(&n); err != nil || n != 1 {
		t.Fatal("payment of verified invoice did not queue service")
	}
}

func TestLegacyServiceRestorePreservesTermsAndDueDate(t *testing.T) {
	db, h := testServer(t)
	uid, _, sid, pid, _ := reviewFixture(t, db, h)
	_, super := user(t, db, h, "restore-super", "super_admin")
	due := time.Unix(1700000000, 0).UTC()
	if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET status='provisioning',next_due_at=$2 WHERE id=$1", sid, due); err != nil {
		t.Fatal(err)
	}
	archiveReviewFixture(t, db, "services", 40, legacy.Record{"id": "40", "uid": "10", "orderid": "20", "productid": "167", "domainstatus": "Active", "billingcycle": "monthly", "amount": "12.34", "firstpaymentamount": "13.33", "nextduedate": "1700000000"})
	path := fmt.Sprintf("/api/v1/admin/migrations/reviews/services/%d", sid)
	body := map[string]any{"version": 1, "decision": "release", "evidence": "已在供应商控制台核对现有资源和到期日", "state": "active", "upstream_id": "legacy-host-40", "recurring_price_cents": 1234}
	expect(t, request(h, "POST", path, body, super, "invalid-resource-proof"), 409)
	body["upstream_id"] = "verified-server-420"
	expect(t, request(h, "POST", path, body, super, "restore-existing-resource"), 201)
	var currentDue time.Time
	var resource, state string
	var auto bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT status,upstream_id,next_due_at,auto_renew FROM services WHERE id=$1", sid).Scan(&state, &resource, &currentDue, &auto); err != nil || !currentDue.Equal(due) || state != "active" || resource != "verified-server-420" || auto {
		t.Fatal("restoration changed historical due date or resource terms")
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE products SET price_cents=9999 WHERE id=$1", pid); err != nil {
		t.Fatal(err)
	}
	renewal, err := db.CreateRenewalInvoice(t.Context(), uid, sid, "verified-service-renewal")
	if err != nil || renewal.AmountCents != 1234 {
		t.Fatalf("restored service repriced from catalog: %v/%v", renewal, err)
	}
}

func TestLegacyInvoiceMismatchAndPaidArchiveOnly(t *testing.T) {
	db, h := testServer(t)
	_, iid, _, _, _ := reviewFixture(t, db, h)
	_, super := user(t, db, h, "paid-super", "super_admin")
	path := fmt.Sprintf("/api/v1/admin/migrations/reviews/invoices/%d", iid)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE invoices SET amount_cents=9999 WHERE id=$1", iid); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"version": 1, "decision": "release", "evidence": "尝试不一致的金额"}
	w := request(h, "POST", path, body, super, "release-invalid-money")
	expect(t, w, 409)
	if !strings.Contains(w.Body.String(), "legacy_invoice_amount") {
		t.Fatal("wrong mismatch failure")
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE invoices SET amount_cents=1333,status='paid' WHERE id=$1", iid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", path, body, super, "release-historical-paid"), 409)
	body["decision"] = "archive"
	expect(t, request(h, "POST", path, body, super, "archive-historical-paid"), 201)
	var held bool
	var state string
	if err := db.Pool.QueryRow(t.Context(), "SELECT import_review_required,status FROM invoices WHERE id=$1", iid).Scan(&held, &state); err != nil || !held || state != "paid" {
		t.Fatal("archive rewrote historical payment status")
	}
}

func TestLegacyReviewConcurrentVersionAndArchiveIntegrity(t *testing.T) {
	db, h := testServer(t)
	_, iid, _, _, _ := reviewFixture(t, db, h)
	_, super := user(t, db, h, "concurrent-review-super", "super_admin")
	path := fmt.Sprintf("/api/v1/admin/migrations/reviews/invoices/%d", iid)
	body := map[string]any{"version": 1, "decision": "archive", "evidence": "核对账单历史记录"}
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- request(h, "POST", path, body, super, fmt.Sprintf("concurrent-review-%d", i)).Code
		}()
	}
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for result := range results {
		counts[result]++
	}
	if counts[201] != 1 || counts[409] != 1 {
		t.Fatalf("version race: %v", counts)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE legacy_source_records SET payload=jsonb_set(payload,'{total}','\"100.00\"') WHERE entity='invoices'"); err != nil {
		t.Fatal(err)
	}
	body["version"] = 2
	w := request(h, "POST", path, body, super, "tampered-archive-test")
	expect(t, w, 409)
	if !strings.Contains(w.Body.String(), "legacy_archive_changed") {
		t.Fatal("tampered archive accepted")
	}
}
