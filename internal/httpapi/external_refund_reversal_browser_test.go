package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

// All gateway traffic is a local protocol fixture.  This never sends a refund,
// notification, or real resource command to an external system.
func TestExternalRefundReversalBrowserFixture(t *testing.T) {
	if os.Getenv("ZJMFGO_BROWSER_READY_FILE") == "" {
		t.Skip("opt-in browser fixture")
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "isolated gateway only", 503) }))
	defer gateway.Close()
	t.Setenv("ZJMFGO_BROWSER_EPAY_API_URL", gateway.URL)
	adminBrowserFixture(t, "reversal-super", func(db *store.Store, h http.Handler) {
		f := setupConfigurationUpgradeFixture(t, db, h, false)
		cfg := epayTestConfig()
		cfg.EpayAPIURL = gateway.URL
		f.h = New(db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
		if _, err := db.Pool.Exec(t.Context(), `UPDATE finance_settings SET topup_enabled=true,min_topup_cents=100,max_topup_cents=10000,max_balance_cents=0,require_active_order=false`); err != nil {
			t.Fatal(err)
		}
		requesterID, requester := externalRefundActor(t, db, f.h, "reversal-requester", []string{"invoices.view", "external_refunds.view", "external_refunds.create"})
		reviewerID, reviewer := externalRefundActor(t, db, f.h, "reversal-reviewer", []string{"invoices.view", "external_refunds.view", "external_refunds.review"})
		readerID, _ := externalRefundActor(t, db, f.h, "reversal-reader", []string{"invoices.view", "external_refunds.view"})
		limitedID, _ := externalRefundActor(t, db, f.h, "reversal-limited", []string{"invoices.view", "external_refunds.view", "external_refunds.create", "external_refunds.review"})
		if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1`, limitedID); err != nil {
			t.Fatal(err)
		}
		externalRefundActor(t, db, f.h, "reversal-denied", []string{"invoices.view"})
		rows := []map[string]any{}
		for i := 0; i < 7; i++ {
			key := fmt.Sprintf("reversal-browser-%d", i)
			invoice := onlineTopupCreate(t, f, key)
			checkout := epayIntent(t, f, invoice, "epay_alipay", key+"-intent")
			expect(t, epayCallback(f.h, "POST", "epay_alipay", epayNotification(checkout, key+"-trade")), 200)
			var payment int64
			if err := db.Pool.QueryRow(t.Context(), `SELECT id FROM payment_transactions WHERE invoice_id=$1`, invoice).Scan(&payment); err != nil {
				t.Fatal(err)
			}
			row := map[string]any{"invoice_id": invoice, "payment_id": payment, "amount_cents": 1234, "scenario": "empty", "expected_version": 2}
			if i >= 4 {
				path := fmt.Sprintf("/api/v1/admin/invoices/%d/external-refunds", invoice)
				body := map[string]any{"payment_id": payment, "amount_cents": 1234, "method": "bank_transfer", "provider_refund_id": key + "-refund", "received_at": time.Now().UTC().Format(time.RFC3339Nano), "evidence_ref": "test://" + key, "evidence_sha256": strings.Repeat("a", 64), "note": "本地隔离fixture到账证据；无真实出款", "expected_version": 2}
				w := request(f.h, "POST", path, body, requester, key+"-create")
				expect(t, w, 201)
				refund := data(t, w)["refund"].(map[string]any)
				row["refund_id"] = refund["id"]
				row["scenario"] = "pending"
				if i > 4 {
					action := "confirm"
					row["scenario"] = "confirmed"
					if i == 6 {
						action = "reject"
						row["scenario"] = "rejected"
					}
					expect(t, request(f.h, "POST", fmt.Sprintf("%s/%.0f/%s", path, refund["id"], action), map[string]any{"expected_version": 1, "note": "fixture独立复核"}, reviewer, key+"-review"), 201)
				}
			}
			rows = append(rows, row)
		}
		pendingLiability, _ := topupLiabilityPendingHTTP(t, f, "reversal-browser-liability")
		manifest, err := json.Marshal(map[string]any{"customer_id": f.uid, "customer_username": "upgrade-customer", "requester_id": requesterID, "reviewer_id": reviewerID, "reader_id": readerID, "limited_id": limitedID, "invoices": rows, "pending_liability_invoice_id": pendingLiability})
		if err != nil {
			t.Fatal(err)
		}
		path := os.Getenv("ZJMFGO_BROWSER_READY_FILE") + ".fixture.json"
		if err = os.WriteFile(path, manifest, 0600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(path) })
	})
}
