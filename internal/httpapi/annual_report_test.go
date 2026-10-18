package httpapi

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/secure"
)

func TestAnnualCashSeparatesReceiptsConsumptionAndRefundMonth(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "annual-root", "super_admin")
	uid, customer := user(t, db, h, "annual-customer", "customer")
	foreign, _ := user(t, db, h, "annual-foreign", "customer")
	staffID, staff := user(t, db, h, "annual-scoped", "admin")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID)
	exec("UPDATE users SET sales_owner_id=$1 WHERE id=$2", staffID, uid)
	_ = customer
	invoice := manualInvoiceCreate(t, h, root, manualInvoiceInput(uid), "annual-real-refund-invoice")
	reference := "annual-real-refund-payment"
	sealed := annualReportCredentials(t, db.ProvisionEncryptionKey, reference, "merchant-main")
	var paymentID int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status,credential_snapshot) VALUES('epay_alipay',$1,$2,$3,3000,'pending',$4) RETURNING id`, reference, invoice, uid, sealed).Scan(&paymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ProcessPaymentWebhook(t.Context(), "epay_alipay", "annual-real-refund-event", reference, invoice, 3000, "succeeded", []byte(`{"paid":true}`), "annual-real-original"); err != nil {
		t.Fatal(err)
	}
	in := map[string]any{"payment_id": paymentID, "amount_cents": 3000, "method": "original_route", "provider_refund_id": "annual-real-refund", "received_at": "2024-02-01T00:00:00+08:00", "evidence_ref": "test://annual-real-refund", "evidence_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "note": "isolated real-channel fixture", "expected_version": 2}
	exec("UPDATE payment_transactions SET created_at='2023-01-15T00:00:00+08',completed_at='2023-01-15T00:00:00+08' WHERE invoice_id=$1", invoice)
	exec("UPDATE invoices SET paid_at='2023-01-15T00:00:00+08' WHERE id=$1", invoice)
	var version int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT version FROM invoices WHERE id=$1", invoice).Scan(&version); err != nil {
		t.Fatal(err)
	}
	in["expected_version"] = version
	in["received_at"] = "2024-02-01T00:00:00+08:00"
	w := request(h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/external-refunds", invoice), in, root, "annual-refund-create")
	expect(t, w, 201)
	rid := int64(data(t, w)["refund"].(map[string]any)["id"].(float64))
	_, reviewer := externalRefundActor(t, db, h, "annual-reviewer", []string{"external_refunds.view", "external_refunds.review"})
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/external-refunds/%d/confirm", invoice, rid), map[string]any{"expected_version": 1, "note": "confirmed later"}, reviewer, "annual-refund-confirm"), 201)
	for _, v := range []struct {
		number, kind string
		user, amount int64
		gateway      bool
	}{{"annual-topup", "topup", uid, 1000, true}, {"annual-consumption", "automatic", uid, 200, false}, {"annual-foreign-cash", "automatic", foreign, 999, true}} {
		var id int64
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,invoice_kind,status,amount_cents,paid_at) VALUES($1,$2,$3,'paid',$4,'2024-01-15T00:00:00+08') RETURNING id`, v.number, v.user, v.kind, v.amount).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if v.gateway {
			exec(`INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status,completed_at,credential_snapshot) VALUES('epay_alipay',$1,$2,$3,$4,'succeeded','2024-01-15T00:00:00+08',$5)`, v.number, id, v.user, v.amount, annualReportCredentials(t, db.ProvisionEncryptionKey, v.number, "merchant-main"))
		}
		if v.kind == "topup" {
			exec("INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after,reference_type,reference_id) VALUES($1,'topup',1000,1000,'gateway_topup',$2)", uid, id)
		} else if !v.gateway {
			exec("INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after,reference_type,reference_id) VALUES($1,'payment',-200,800,'invoice',$2)", uid, id)
		}
	}
	var credit int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,status,amount_cents,paid_at) VALUES('annual-credit',$1,'paid',1000,'2024-01-15T00:00:00+08') RETURNING id`, uid).Scan(&credit); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO credit_invoice_payments(invoice_id,user_id,amount_cents,remaining_cents,expected_version,idempotency_key) VALUES($1,$2,1000,600,1,'annual-credit-key')`, credit, uid)
	exec(`INSERT INTO credit_repayments(invoice_id,user_id,actor_id,amount_cents,receipt,received_at,idempotency_key,expected_version,version_after,remaining_after) VALUES($1,$2,$3,400,'annual-credit-receipt','2024-01-15T00:00:00+08','annual-credit-repay',1,2,600)`, credit, uid, staffID)
	for _, v := range []struct {
		date   string
		amount int64
	}{{"2024-01-01", 50}, {"2023-01-01", 20}} {
		expect(t, request(h, "POST", "/api/v1/admin/reports/expenses", map[string]any{"amount_cents": v.amount, "occurred_on": v.date, "category": "hosting", "source": "bank", "evidence_ref": "test://annual/" + v.date, "note": "annual"}, root, "annual-cost-"+v.date), 201)
	}
	get := func(cookie string) map[string]any {
		t.Helper()
		w := request(h, "GET", "/api/v1/admin/reports/annual-cash?year=2024", nil, cookie, "")
		expect(t, w, 200)
		return data(t, w)
	}
	result := get(root)
	total := result["total"].(map[string]any)
	for key, want := range map[string]float64{"received_cents": 2399, "gateway_received_cents": 1999, "credit_received_cents": 400, "refund_cents": 3000, "manual_expense_cents": 50, "outgoing_cents": 3050, "net_cents": -651, "business_consumption_cents": 1599} {
		if total[key] != want {
			t.Fatalf("cash %s=%v want %v: %#v", key, total[key], want, total)
		}
	}
	months := result["months"].([]any)
	if len(months) != 12 || months[0].(map[string]any)["refund_cents"] != float64(0) || months[1].(map[string]any)["refund_cents"] != float64(3000) {
		t.Fatalf("refund must use received_at month: %#v", months)
	}
	previous := result["previous_year"].(map[string]any)
	if previous["year"] != float64(2023) || len(previous["months"].([]any)) != 12 || previous["total"].(map[string]any)["received_cents"] != float64(3000) || previous["total"].(map[string]any)["net_cents"] != float64(2980) {
		t.Fatalf("previous year: %#v", previous)
	}
	if result["comparison"].(map[string]any)["net_cents"] != float64(-3631) {
		t.Fatalf("year comparison: %#v", result["comparison"])
	}
	result = get(staff)
	total = result["total"].(map[string]any)
	if result["expenses_available"] != false || total["received_cents"] != float64(1400) || total["business_consumption_cents"] != float64(600) || total["manual_expense_cents"] != nil || total["outgoing_cents"] != nil || total["net_cents"] != nil {
		t.Fatalf("scoped report leaked global expenses/foreign cash: %#v", result)
	}
	w = request(h, "GET", "/api/v1/admin/reports/annual-cash?year=2024&format=csv", nil, staff, "")
	expect(t, w, 200)
	records := reportCSVRecords(t, w.Body.Bytes())
	if len(records) != 14 || records[1][3] != "无权限" || records[13][1] != "1400" {
		t.Fatalf("scoped annual CSV: %#v", records)
	}
	expect(t, request(h, "GET", "/api/v1/admin/reports/annual-cash?year=2024&month=2", nil, root, ""), 400)
	expect(t, request(h, "GET", "/api/v1/admin/reports/annual-cash?year=2024", nil, customer, ""), 403)
}

func TestAnnualCashIncludesConfirmedAnomalyRefundOnce(t *testing.T) {
	f, invoice, pid, reference, anchor := paymentAnomalyFixture(t, "annual-anomaly-tx")
	base, create := anomalyPaths(invoice, anchor)
	_, reviewer := anomalyReviewer(t, f)
	in := paymentAnomalyInput(anchor, "annual-anomaly-refund")
	w := request(f.h, "POST", create, in, f.root, "annual-anomaly-create")
	expect(t, w, 201)
	rid := int64(data(t, w)["refund"].(map[string]any)["id"].(float64))
	expect(t, request(f.h, "POST", fmt.Sprintf("%s/payment-anomaly-refunds/%d/confirm", base, rid), map[string]any{"expected_version": 1, "expected_group_version": anchor, "note": "review"}, reviewer, "annual-anomaly-confirm"), 201)
	if _, err := f.db.ProcessPaymentWebhook(t.Context(), "mock", "annual-anomaly-duplicate-event", reference, invoice, 3000, "succeeded", []byte(`{"late":2}`), "annual-anomaly-tx"); err != nil {
		t.Fatal(err)
	}
	year := time.Now().In(time.FixedZone("Shanghai", 8*3600)).Year()
	w = request(f.h, "GET", fmt.Sprintf("/api/v1/admin/reports/annual-cash?year=%d", year), nil, f.root, "")
	expect(t, w, 200)
	total := data(t, w)["total"].(map[string]any)
	if total["anomaly_received_cents"] != float64(0) || total["refund_cents"] != float64(0) || total["simulated_received_cents"] != float64(3000) || total["simulated_refund_cents"] != float64(3000) || total["received_cents"] != float64(0) || total["net_cents"] != float64(0) {
		t.Fatalf("anomaly duplicated/omitted (payment %d): %#v", pid, total)
	}
}

func annualReportCredentials(t *testing.T, key []byte, reference, pid string) []byte {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"APIURL": "https://payments.example.test", "PID": pid, "Key": "isolated-test-secret"})
	sealed, err := secure.Seal(key, []byte("zjmfgo:payment-credentials:v1:"+reference), raw)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func TestAnnualCashLateReceiptMerchantScopesAndUnverifiedClaims(t *testing.T) {
	f, invoice, pid, reference := closeFixture(t, "epay_alipay")
	actor, hash := closeActor(t, f, f.root)
	op, err := f.db.BeginPaymentClose(t.Context(), actor, invoice, pid, 1, "annual-merchant-close", hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.CompletePaymentClose(t.Context(), op, closeVerified(op), hash); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.ProcessPaymentWebhook(t.Context(), "epay_alipay", "annual-merchant-late", reference, invoice, 3000, "succeeded", []byte(`{"late":1}`), "annual-shared-number"); err != nil {
		t.Fatal(err)
	}
	var sealed []byte
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT credential_snapshot FROM payment_transactions WHERE id=$1", pid).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	scope, err := f.db.ReportPaymentMerchantScope("epay_alipay", reference, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Pool.Exec(t.Context(), `INSERT INTO payment_close_anomaly_merchant_bindings(payment_id,credential_sha256,merchant_scope,provider,provider_payment_id,channel_instance_id) SELECT id,encode(sha256(credential_snapshot),'hex'),$2,provider,provider_payment_id,coalesce(channel_instance_id,'') FROM payment_transactions WHERE id=$1`, pid, scope); err != nil {
		t.Fatal(err)
	}
	var otherInvoice, normal int64
	if err = f.db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,status,amount_cents,paid_at) VALUES('annual-merchant-other',$1,'paid',100,clock_timestamp()) RETURNING id`, f.uid).Scan(&otherInvoice); err != nil {
		t.Fatal(err)
	}
	otherRef := "annual-merchant-other"
	if err = f.db.Pool.QueryRow(t.Context(), `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,status,amount_cents,external_transaction_id,completed_at,credential_snapshot) VALUES('epay_alipay',$1,$2,$3,'succeeded',100,'annual-shared-number',clock_timestamp(),$4) RETURNING id`, otherRef, otherInvoice, f.uid, annualReportCredentials(t, f.db.ProvisionEncryptionKey, otherRef, "different-merchant")).Scan(&normal); err != nil {
		t.Fatal(err)
	}
	year := time.Now().In(time.FixedZone("Shanghai", 8*3600)).Year()
	get := func() map[string]any {
		t.Helper()
		w := request(f.h, "GET", fmt.Sprintf("/api/v1/admin/reports/annual-cash?year=%d", year), nil, f.root, "")
		expect(t, w, 200)
		return data(t, w)["total"].(map[string]any)
	}
	total := get()
	if total["received_cents"] != float64(3100) || total["anomaly_received_cents"] != float64(3000) {
		t.Fatalf("different merchant suppressed independent receipt: %#v", total)
	}
	// Rebuild the isolated successful fixture with the original merchant's
	// snapshot, encrypted for the new payment reference (rotation-safe scope).
	if _, err = f.db.Pool.Exec(t.Context(), "DELETE FROM payment_transactions WHERE id=$1", normal); err != nil {
		t.Fatal(err)
	}
	raw, err := secure.Open(f.db.ProvisionEncryptionKey, []byte("zjmfgo:payment-credentials:v1:"+reference), sealed)
	if err != nil {
		t.Fatal(err)
	}
	sameSealed, err := secure.Seal(f.db.ProvisionEncryptionKey, []byte("zjmfgo:payment-credentials:v1:"+otherRef), raw)
	if err != nil {
		t.Fatal(err)
	}
	insert := func(credential []byte) {
		t.Helper()
		if err = f.db.Pool.QueryRow(t.Context(), `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,status,amount_cents,external_transaction_id,completed_at,credential_snapshot) VALUES('epay_alipay',$1,$2,$3,'succeeded',100,'annual-shared-number',clock_timestamp(),$4) RETURNING id`, otherRef, otherInvoice, f.uid, credential).Scan(&normal); err != nil {
			t.Fatal(err)
		}
	}
	insert(sameSealed)
	total = get()
	if total["received_cents"] != float64(100) || total["anomaly_received_cents"] != float64(0) || total["unverified_anomaly_cents"] != float64(0) {
		t.Fatalf("same merchant duplicate counted: %#v", total)
	}
	if _, err = f.db.Pool.Exec(t.Context(), "DELETE FROM payment_transactions WHERE id=$1", normal); err != nil {
		t.Fatal(err)
	}
	insert([]byte("unavailable"))
	total = get()
	if total["received_cents"] != float64(100) || total["anomaly_received_cents"] != float64(0) || total["unverified_anomaly_cents"] != float64(3000) {
		t.Fatalf("unknown merchant must remain unverified: %#v", total)
	}
}
