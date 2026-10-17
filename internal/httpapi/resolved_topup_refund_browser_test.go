package httpapi

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

// Opt-in acceptance data only. Payments and failed ledger writes are isolated
// protocol fixtures; this server never sends an external refund or real email.
func TestResolvedTopupRefundBrowserFixture(t *testing.T) {
	if os.Getenv("ZJMFGO_BROWSER_READY_FILE") == "" || os.Getenv("ZJMFGO_BROWSER_FINISH_FILE") == "" {
		t.Skip("opt-in resolved top-up refund browser fixture")
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "isolated gateway only", http.StatusServiceUnavailable)
	}))
	defer gateway.Close()
	t.Setenv("ZJMFGO_BROWSER_EPAY_API_URL", gateway.URL)
	adminBrowserFixture(t, "resolved-refund-super", func(db *store.Store, h http.Handler) {
		f := setupConfigurationUpgradeFixture(t, db, h, false)
		cfg := epayTestConfig()
		cfg.EpayAPIURL = gateway.URL
		f.h = New(db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
		if _, err := db.Pool.Exec(t.Context(), `UPDATE finance_settings SET topup_enabled=true,min_topup_cents=100,max_topup_cents=10000,max_balance_cents=0,require_active_order=false`); err != nil {
			t.Fatal(err)
		}
		accounts := []map[string]any{}
		for _, actor := range []struct {
			name        string
			permissions []string
			limited     bool
		}{
			{"resolved-refund-requester", []string{"invoices.view", "external_refunds.view", "external_refunds.create"}, false},
			{"resolved-refund-reviewer", []string{"invoices.view", "external_refunds.view", "external_refunds.review"}, false},
			{"resolved-refund-reader", []string{"invoices.view", "external_refunds.view"}, false},
			{"resolved-refund-limited", []string{"invoices.view", "external_refunds.view", "external_refunds.create", "external_refunds.review"}, true},
		} {
			id, _ := externalRefundActor(t, db, f.h, actor.name, actor.permissions)
			if actor.limited {
				if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1", id); err != nil {
					t.Fatal(err)
				}
			}
			accounts = append(accounts, map[string]any{"id": id, "username": actor.name, "permissions": actor.permissions, "limited": actor.limited})
		}
		rows := []map[string]any{}
		for index, scenario := range []string{"confirm", "reject", "unknown_receipt", "version_conflict", "insufficient_balance", "unresolved_liability"} {
			key := fmt.Sprintf("resolved-refund-browser-%d", index)
			invoice, _ := topupLiabilityPendingHTTP(t, f, key)
			if scenario != "unresolved_liability" {
				expect(t, request(f.h, "POST", topupLiabilityPath(invoice)+"/resolve", map[string]any{"expected_version": topupLiabilityVersion(t, f, invoice), "note": "隔离fixture核对原收款后补入余额，无真实付款"}, f.root, key+"-resolve"), 201)
			}
			var payment int64
			if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM payment_transactions WHERE invoice_id=$1", invoice).Scan(&payment); err != nil {
				t.Fatal(err)
			}
			rows = append(rows, map[string]any{"scenario": scenario, "invoice_id": invoice, "payment_id": payment, "amount_cents": 1234, "expected_version": topupLiabilityVersion(t, f, invoice), "admin_path": fmt.Sprintf("/admin/invoices/%d", invoice)})
		}
		writePaymentBrowserManifest(t, map[string]any{"fixture": "resolved_topup_refund", "real_external_actions": false, "customer_id": f.uid, "accounts": accounts, "invoices": rows})
	})
}
