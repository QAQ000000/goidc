package httpapi

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func onlineTopupFixture(t *testing.T) upgradeFixture {
	t.Helper()
	f := configurationUpgradeFixture(t, false)
	// A loopback-only protocol endpoint: no merchant or external network is used.
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "isolated gateway: result unknown", 503) }))
	t.Cleanup(gateway.Close)
	cfg := epayTestConfig()
	cfg.EpayAPIURL = gateway.URL
	f.h = New(f.db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE finance_settings SET topup_enabled=true,min_topup_cents=100,max_topup_cents=10000,max_balance_cents=0,require_active_order=false WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	return f
}
func onlineTopupInput(t *testing.T, f upgradeFixture, amount int64) map[string]any {
	t.Helper()
	w := request(f.h, "GET", "/api/v1/balance/topup-options", nil, f.customer, "")
	expect(t, w, 200)
	return map[string]any{"amount_cents": amount, "expected_version": data(t, w)["version"]}
}
func onlineTopupCreate(t *testing.T, f upgradeFixture, key string) int64 {
	t.Helper()
	w := request(f.h, "POST", "/api/v1/balance/topups", onlineTopupInput(t, f, 1234), f.customer, key)
	expect(t, w, 201)
	return int64(data(t, w)["invoice_id"].(float64))
}
func TestOnlineTopupHTTPAuthReplayAndCancel(t *testing.T) {
	f := onlineTopupFixture(t)
	path := "/api/v1/balance/topups"
	in := onlineTopupInput(t, f, 1234)
	keyResponse := request(f.h, "POST", "/api/v1/api-keys", map[string]any{"name": "topup-auth"}, f.customer, "")
	expect(t, keyResponse, 201)
	secret := data(t, keyResponse)["secret"].(string)
	expect(t, requestBearer(f.h, "GET", "/api/v1/balance/topup-options", nil, secret), 403)
	expect(t, requestBearer(f.h, "POST", path, in, secret), 403)
	expect(t, requestBearer(f.h, "GET", "/api/v1/balance/topup-options", nil, "invalid-api-key"), 401)
	expect(t, request(f.h, "GET", "/api/v1/balance/topup-options", nil, f.root, ""), 403)
	expect(t, request(f.h, "POST", path, in, f.customer, ""), 409)
	expect(t, request(f.h, "POST", path, map[string]any{"amount_cents": 12.34, "expected_version": in["expected_version"]}, f.customer, "fractional"), 400)
	expect(t, request(f.h, "POST", path, map[string]any{"amount_cents": 1234, "expected_version": 999999}, f.customer, "topup-stale"), 409)
	w := request(f.h, "POST", path, in, f.customer, "topup-same")
	expect(t, w, 201)
	iid := int64(data(t, w)["invoice_id"].(float64))
	expect(t, request(f.h, "POST", path, in, f.customer, "topup-same"), 200)
	in["amount_cents"] = 1235
	expect(t, request(f.h, "POST", path, in, f.customer, "topup-same"), 409)
	invoice := fmt.Sprintf("/api/v1/invoices/%d", iid)
	expect(t, requestBearer(f.h, "POST", invoice+"/payments", map[string]any{"provider": "epay_alipay", "expected_version": 1}, secret), 403)
	expect(t, requestBearer(f.h, "POST", fmt.Sprintf("%s/%d/cancel", path, iid), map[string]any{"expected_version": 1, "note": "key forbidden"}, secret), 403)
	cancel := fmt.Sprintf("%s/%d/cancel", path, iid)
	expect(t, request(f.h, "GET", invoice, nil, f.other, ""), 404)
	expect(t, request(f.h, "POST", cancel, map[string]any{"expected_version": 1, "note": "topup-other"}, f.other, "topup-other"), 404)
	expect(t, request(f.h, "POST", invoice+"/pay", map[string]any{"expected_version": 1}, f.customer, "topup-balance"), 409)
	expect(t, request(f.h, "POST", invoice+"/pay-credit", map[string]any{"expected_version": 1}, f.customer, "topup-credit"), 409)
	expect(t, request(f.h, "POST", invoice+"/payments", map[string]any{"provider": "mock", "expected_version": 1}, f.customer, "topup-mock"), 409)
	expect(t, request(f.h, "POST", cancel, map[string]any{"expected_version": 99, "note": "topup-stale"}, f.customer, "cancel-stale"), 409)
	var balanceBefore int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT balance_cents FROM users WHERE id=$1", f.uid).Scan(&balanceBefore); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", cancel, map[string]any{"expected_version": 1, "note": "customer cancelled"}, f.customer, "topup-cancel"), 200)
	expect(t, request(f.h, "POST", cancel, map[string]any{"expected_version": 1, "note": "customer cancelled"}, f.customer, "topup-cancel"), 200)
	expect(t, request(f.h, "POST", cancel, map[string]any{"expected_version": 1, "note": "different note"}, f.customer, "topup-cancel"), 409)
	var balanceAfter, ledgerCount int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT balance_cents FROM users WHERE id=$1", f.uid).Scan(&balanceAfter); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM balance_ledger WHERE reference_type='gateway_topup' AND reference_id=$1", iid).Scan(&ledgerCount); err != nil {
		t.Fatal(err)
	}
	if balanceAfter != balanceBefore || ledgerCount != 0 {
		t.Fatal("cancel credited balance", balanceBefore, balanceAfter, ledgerCount)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE user_id=$1", f.uid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path, map[string]any{"amount_cents": 1234, "expected_version": in["expected_version"]}, f.customer, "topup-same"), 401)
	expect(t, request(f.h, "POST", cancel, map[string]any{"expected_version": 1, "note": "customer cancelled"}, f.customer, "topup-cancel"), 401)

}
func TestOnlineTopupHTTPLateSignedSuccessReplayAndTradeConflict(t *testing.T) {
	f := onlineTopupFixture(t)
	iid := onlineTopupCreate(t, f, "topup-late")
	var before int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT balance_cents FROM users WHERE id=$1", f.uid).Scan(&before); err != nil {
		t.Fatal(err)
	}
	checkout := epayIntent(t, f, iid, "epay_alipay", "late-intent")
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/balance/topups/%d/cancel", iid), map[string]any{"expected_version": 1, "note": "unknown payment"}, f.customer, "cancel-accepted"), 409)
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE finance_settings SET topup_enabled=false,min_topup_cents=9999,max_topup_cents=10000,require_active_order=true,version=version+1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	forged := epayNotification(checkout, "late-real-format")
	forged.Set("money", "12.35")
	expect(t, epayCallback(f.h, "POST", "epay_alipay", forged), 401)
	for _, event := range []string{"late-event-one", "late-event-two", "late-event-one"} {
		w := epayCallback(f.h, "POST", "epay_alipay", epayNotification(checkout, event))
		if event == "late-event-two" {
			expect(t, w, 409)
			continue
		}
		expect(t, w, 200)
		if w.Body.String() != "success" {
			t.Fatal(w.Body.String())
		}
	}
	var after, credits int64
	var state string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT balance_cents FROM users WHERE id=$1", f.uid).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM balance_topups WHERE invoice_id=$1", iid).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM balance_ledger WHERE user_id=$1 AND reference_type='gateway_topup'", f.uid).Scan(&credits); err != nil {
		t.Fatal(err)
	}
	if after != before+1234 || credits != 1 || state != "credited" {
		t.Fatalf("balance %d -> %d credits=%d state=%s", before, after, credits, state)
	}
}
func TestOnlineTopupHTTPSettingsEligibility(t *testing.T) {
	f := onlineTopupFixture(t)
	for _, tc := range []struct {
		name, sql string
		amount    int64
	}{
		{"disabled", `UPDATE finance_settings SET topup_enabled=false WHERE id=1`, 1234},
		{"minimum", `UPDATE finance_settings SET topup_enabled=true WHERE id=1`, 99},
		{"maximum", `UPDATE finance_settings SET topup_enabled=true WHERE id=1`, 10001},
		{"capacity", `UPDATE finance_settings SET max_balance_cents=1 WHERE id=1`, 1234},
		{"order", `UPDATE finance_settings SET max_balance_cents=0,require_active_order=true WHERE id=1; UPDATE orders SET status='cancelled'`, 1234},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.db.Pool.Exec(t.Context(), tc.sql); err != nil {
				t.Fatal(err)
			}
			expect(t, request(f.h, "POST", "/api/v1/balance/topups", onlineTopupInput(t, f, tc.amount), f.customer, "eligibility-"+tc.name), 409)
		})
	}
}

func TestOnlineTopupHTTPChangedAdmissionVersion(t *testing.T) {
	f := onlineTopupFixture(t)
	iid := onlineTopupCreate(t, f, "version-before-admission")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE finance_settings SET version=version+1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", iid), map[string]any{"provider": "epay_alipay", "expected_version": 1}, f.customer, "stale-admission-intent"), 409)
	var n int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM payment_transactions WHERE invoice_id=$1", iid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("stale admission left payment transaction", n)
	}
}
