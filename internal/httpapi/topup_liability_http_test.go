package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func topupLiabilityPendingHTTP(t *testing.T, f upgradeFixture, key string) (int64, url.Values) {
	t.Helper()
	invoice := onlineTopupCreate(t, f, key)
	checkout := epayIntent(t, f, invoice, "epay_alipay", key+"-intent")
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE OR REPLACE FUNCTION reject_liability_ledger() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.reference_type='gateway_topup' THEN RAISE EXCEPTION 'isolated topup ledger failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_liability BEFORE INSERT ON balance_ledger FOR EACH ROW EXECUTE FUNCTION reject_liability_ledger()`); err != nil {
		t.Fatal(err)
	}
	callback := epayNotification(checkout, key+"-trade")
	expect(t, epayCallback(f.h, "POST", "epay_alipay", callback), 200)
	if _, err := f.db.Pool.Exec(t.Context(), "DROP TRIGGER reject_liability ON balance_ledger"); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM balance_topups WHERE invoice_id=$1", invoice).Scan(&status); err != nil || status != "pending_liability" {
		t.Fatal("did not preserve collected liability", status, err)
	}
	return invoice, callback
}
func topupLiabilityPath(id int64) string {
	return fmt.Sprintf("/api/v1/admin/invoices/%d/topup-liability", id)
}
func topupLiabilityVersion(t *testing.T, f upgradeFixture, id int64) int64 {
	t.Helper()
	var v int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT version FROM invoices WHERE id=$1", id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTopupLiabilityHTTPResolveReplayPrivacyAndCash(t *testing.T) {
	f := onlineTopupFixture(t)
	id, callback := topupLiabilityPendingHTTP(t, f, "liability-http-main")
	path := topupLiabilityPath(id)
	v := topupLiabilityVersion(t, f, id)
	w := request(f.h, "GET", path, nil, f.root, "")
	expect(t, w, 200)
	if w.Header().Get("Cache-Control") != "no-store" || data(t, w)["resolution"] != nil {
		t.Fatal("unexpected liability read", w.Body.String())
	}
	var settlement []byte
	var before int64
	var paid time.Time
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT to_jsonb(s) FROM balance_topup_settlements s WHERE invoice_id=$1", id).Scan(&settlement); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT balance_cents FROM users WHERE id=$1", f.uid).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT paid_at FROM invoices WHERE id=$1", id).Scan(&paid); err != nil {
		t.Fatal(err)
	}
	income := func() []byte {
		w := request(f.h, "GET", fmt.Sprintf("/api/v1/admin/reports/income?year=%d", time.Now().Year()), nil, f.root, "")
		expect(t, w, 200)
		out, _ := json.Marshal(data(t, w))
		return out
	}
	cash := income()
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE finance_settings SET topup_enabled=false,max_balance_cents=1,require_active_order=true;UPDATE users SET status='disabled' WHERE role='customer'"); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"expected_version": v, "note": "核实原收款并补余额"}
	expect(t, request(f.h, "POST", path+"/resolve", body, f.root, "liability-main-resolve"), 201)
	expect(t, request(f.h, "POST", path+"/resolve", body, f.root, "liability-main-resolve"), 200)
	expect(t, request(f.h, "POST", path+"/resolve", body, f.root, "liability-other-key"), 409)
	expect(t, request(f.h, "POST", path+"/resolve", map[string]any{"expected_version": v, "note": "不同说明"}, f.root, "liability-main-resolve"), 409)
	expect(t, epayCallback(f.h, "POST", "epay_alipay", callback), 200)
	var balance int64
	var original []byte
	var afterPaid time.Time
	var state, customerState string
	var ledger, resolutions int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT u.balance_cents,u.status,b.status,i.paid_at,(SELECT count(*) FROM balance_ledger WHERE reference_type='gateway_topup' AND reference_id=$1),(SELECT count(*) FROM balance_topup_liability_resolutions WHERE invoice_id=$1) FROM users u JOIN balance_topups b ON b.user_id=u.id JOIN invoices i ON i.id=b.invoice_id WHERE i.id=$1`, id).Scan(&balance, &customerState, &state, &afterPaid, &ledger, &resolutions); err != nil {
		t.Fatal(err)
	}
	if balance != before+1234 || state != "credited" || customerState != "disabled" || ledger != 1 || resolutions != 1 || !afterPaid.Equal(paid) {
		t.Fatal("nonunique or altered liability", balance, state, customerState, ledger, resolutions)
	}
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT to_jsonb(s) FROM balance_topup_settlements s WHERE invoice_id=$1", id).Scan(&original); err != nil || !bytes.Equal(original, settlement) {
		t.Fatal("original failure mutated", err)
	}
	var audits int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action='balance.topup.liability_resolved' AND entity_id=$1::bigint::text", id).Scan(&audits); err != nil || audits != 1 {
		t.Fatal("resolution audit must be unique", audits, err)
	}
	if !bytes.Equal(income(), cash) {
		t.Fatal("liability resolution recounted cash")
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET status='active' WHERE id=$1", f.uid); err != nil {
		t.Fatal(err)
	}
	detail := request(f.h, "GET", fmt.Sprintf("/api/v1/invoices/%d", id), nil, f.customer, "")
	expect(t, detail, 200)
	var public map[string]any
	if err := json.Unmarshal(detail.Body.Bytes(), &public); err != nil {
		t.Fatal(err)
	}
	raw := detail.Body.String()
	for _, secret := range []string{"核实原收款并补余额", "liability-main-resolve", "request_hash", "actor_id"} {
		if bytes.Contains([]byte(raw), []byte(secret)) {
			t.Fatal("customer resolution leak", secret)
		}
	}
}

func TestTopupLiabilityHTTPPermissionScopeInputsAndReplayRevocation(t *testing.T) {
	f := onlineTopupFixture(t)
	id, _ := topupLiabilityPendingHTTP(t, f, "liability-auth-main")
	path := topupLiabilityPath(id)
	body := map[string]any{"expected_version": topupLiabilityVersion(t, f, id), "note": "财务核实"}
	staffID, staff := user(t, f.db, f.h, "liability-finance", "admin")
	contentRole(t, f.db, staffID, "invoices.view")
	expect(t, request(f.h, "GET", path, nil, "", ""), 401)
	expect(t, request(f.h, "GET", path, nil, f.customer, ""), 403)
	expect(t, request(f.h, "GET", path, nil, staff, ""), 200)
	expect(t, request(f.h, "POST", path+"/resolve", body, staff, "liability-auth-resolve"), 403)
	contentRole(t, f.db, staffID, "invoices.view", "balance.topup.resolve")
	key := request(f.h, "POST", "/api/v1/api-keys", map[string]any{"name": "liability-browser-only"}, staff, "")
	expect(t, key, 201)
	secret := data(t, key)["secret"].(string)
	expect(t, requestBearer(f.h, "POST", path+"/resolve", body, secret), 403)
	expect(t, request(f.h, "POST", path+"/resolve", map[string]any{"expected_version": body["expected_version"], "note": ""}, staff, "liability-empty-note"), 409)
	expect(t, request(f.h, "POST", path+"/resolve", map[string]any{"expected_version": body["expected_version"], "note": "x", "amount_cents": 1}, staff, "liability-extra-field"), 400)
	expect(t, request(f.h, "POST", path+"/resolve", body, staff, ""), 409)
	expect(t, request(f.h, "POST", path+"/resolve", map[string]any{"expected_version": 9999, "note": "stale"}, staff, "liability-stale"), 409)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "GET", path, nil, staff, ""), 404)
	expect(t, request(f.h, "POST", path+"/resolve", body, staff, "liability-auth-resolve"), 404)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='all' WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path+"/resolve", body, staff, "liability-auth-resolve"), 201)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path+"/resolve", body, staff, "liability-auth-resolve"), 404)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='all' WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	contentRole(t, f.db, staffID, "invoices.view")
	expect(t, request(f.h, "POST", path+"/resolve", body, staff, "liability-auth-resolve"), 403)
	contentRole(t, f.db, staffID, "invoices.view", "balance.topup.resolve")
	if _, err := f.db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE user_id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path+"/resolve", body, staff, "liability-auth-resolve"), 401)
}

func TestTopupLiabilityBrowserFixture(t *testing.T) {
	if os.Getenv("ZJMFGO_BROWSER_READY_FILE") == "" {
		t.Skip("opt-in browser fixture")
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "isolated unknown gateway", 503) }))
	defer gateway.Close()
	t.Setenv("ZJMFGO_BROWSER_EPAY_API_URL", gateway.URL)
	adminBrowserFixture(t, "liability-super", func(db *store.Store, h http.Handler) {
		f := setupConfigurationUpgradeFixture(t, db, h, false)
		cfg := epayTestConfig()
		cfg.EpayAPIURL = gateway.URL
		f.h = New(db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
		if _, err := db.Pool.Exec(t.Context(), "UPDATE finance_settings SET topup_enabled=true,min_topup_cents=100,max_topup_cents=10000,max_balance_cents=0,require_active_order=false"); err != nil {
			t.Fatal(err)
		}
		finance, _ := user(t, db, f.h, "liability-finance", "admin")
		contentRole(t, db, finance, "invoices.view", "balance.topup.resolve", "customers.view")
		reader, _ := user(t, db, f.h, "liability-reader", "admin")
		contentRole(t, db, reader, "invoices.view", "customers.view")
		limited, _ := user(t, db, f.h, "liability-limited", "admin")
		contentRole(t, db, limited, "invoices.view", "balance.topup.resolve")
		if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1", limited); err != nil {
			t.Fatal(err)
		}
		other, _ := user(t, db, f.h, "liability-other-customer", "customer")
		ids := []int64{}
		for i := 0; i < 5; i++ {
			id, _ := topupLiabilityPendingHTTP(t, f, fmt.Sprintf("fixture-liability-%d", i))
			ids = append(ids, id)
		}
		resolved := ids[4]
		expect(t, request(f.h, "POST", topupLiabilityPath(resolved)+"/resolve", map[string]any{"expected_version": topupLiabilityVersion(t, f, resolved), "note": "隔离fixture已补账"}, f.root, "fixture-resolved-liability"), 201)
		manifest, err := json.Marshal(map[string]any{"customer_id": f.uid, "other_customer_id": other, "finance_id": finance, "reader_id": reader, "limited_id": limited, "pending_invoice_ids": ids[:4], "resolved_invoice_id": resolved})
		if err != nil {
			t.Fatal(err)
		}
		p := os.Getenv("ZJMFGO_BROWSER_READY_FILE") + ".fixture.json"
		if err = os.WriteFile(p, manifest, 0600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(p) })
	})
}
