package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/codex/zjmfgo/internal/payment"
	"github.com/codex/zjmfgo/internal/store"
)

// Explicit acceptance fixture only: one isolated schema, no remote merchant,
// no real payout and no runtime endpoint that can manufacture evidence.
func TestPaymentResolutionBrowserFixture(t *testing.T) {
	if os.Getenv("ZJMFGO_BROWSER_READY_FILE") == "" || os.Getenv("ZJMFGO_BROWSER_FINISH_FILE") == "" {
		t.Skip("opt-in payment resolution browser fixture")
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "isolated gateway only", http.StatusServiceUnavailable)
	}))
	defer gateway.Close()
	t.Setenv("ZJMFGO_BROWSER_EPAY_API_URL", gateway.URL)
	adminBrowserFixture(t, "resolution-super", func(db *store.Store, h http.Handler) {
		f, _ := setupExternalRefundReversalBrowser(t, db, h, gateway.URL)
		accounts := []map[string]any{}
		cookies := map[string]string{}
		for _, actor := range []struct {
			name        string
			permissions []string
			limited     bool
		}{
			{"resolution-closer", []string{"invoices.view", "payments.close"}, false},
			{"resolution-requester", []string{"invoices.view", "payments.close_verify", "payment_anomalies.view", "payment_anomalies.create"}, false},
			{"resolution-reviewer", []string{"invoices.view", "payments.close_review", "payment_anomalies.view", "payment_anomalies.review"}, false},
			{"resolution-reader", []string{"invoices.view", "payment_anomalies.view"}, false},
			{"resolution-denied", []string{"settings.view"}, false},
			{"resolution-limited", []string{"invoices.view", "payments.close_verify", "payments.close_review", "payment_anomalies.view", "payment_anomalies.create", "payment_anomalies.review"}, true},
		} {
			id, cookie := externalRefundActor(t, db, f.h, actor.name, actor.permissions)
			cookies[actor.name] = cookie
			if actor.limited {
				if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1", id); err != nil {
					t.Fatal(err)
				}
			}
			accounts = append(accounts, map[string]any{"id": id, "username": actor.name, "password": "integration-password", "permissions": actor.permissions, "limited": actor.limited})
		}
		closer, closeSession := closeActor(t, f, cookies["resolution-closer"])
		requester, requestSession := closeActor(t, f, cookies["resolution-requester"])
		verificationRows := []map[string]any{}
		for index, scenario := range []string{"lookup_closed", "lookup_closed", "lookup_closed", "lookup_closed", "lookup_unknown", "paid_awaiting_review", "closed_awaiting_review"} {
			key := fmt.Sprintf("resolution-browser-verify-%d", index)
			invoice := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), key+"-invoice")
			w := request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", invoice), map[string]any{"provider": "mock", "expected_version": 1}, f.customer, key+"-intent")
			expect(t, w, 201)
			intent := data(t, w)
			pid := int64(intent["payment_id"].(float64))
			op, err := db.BeginPaymentClose(t.Context(), closer, invoice, pid, 1, key+"-close", closeSession)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "lookup_closed" || scenario == "closed_awaiting_review" {
				g := payment.MockGateway{Secret: []byte("integration-payment-secret")}
				if _, err = g.Close(t.Context(), payment.CloseRequest{OperationID: op.OperationID, ProviderPaymentID: op.ProviderPaymentID, AmountCents: op.AmountCents}); err != nil {
					t.Fatal(err)
				}
			}
			if err = db.FailPaymentClose(t.Context(), op, "unknown"); err != nil {
				t.Fatal(err)
			}
			row := map[string]any{"scenario": scenario, "invoice_id": invoice, "payment_id": pid, "operation_id": op.OperationID, "close_operation_id": op.ID, "close_key": key + "-close", "amount_cents": 3000, "expected_version": 1, "admin_path": fmt.Sprintf("/admin/invoices/%d", invoice), "constructed_by": "real API intent; Store close reservation and unknown receipt; explicit compiled mock Close evidence only for closed scenarios"}
			if scenario == "paid_awaiting_review" || scenario == "closed_awaiting_review" {
				v, err := db.BeginPaymentCloseVerification(t.Context(), requester, invoice, pid, op.ID, 1, key+"-lookup", requestSession)
				if err != nil {
					t.Fatal(err)
				}
				result := payment.CloseStatusResult{OperationID: op.OperationID, ProviderPaymentID: op.ProviderPaymentID, AmountCents: op.AmountCents, Status: "closed", ProviderEvidenceID: "fixture:" + key}
				if scenario == "paid_awaiting_review" {
					result.Status = "succeeded"
					result.ExternalTransactionID = "fixture-paid:" + key
					row["constructed_by"] = "Store explicit trusted paid evidence fixture; no live adapter injection or real payment"
				}
				v, err = db.CompletePaymentCloseVerification(t.Context(), v, result, "verified", requestSession)
				if err != nil {
					t.Fatal(err)
				}
				row["verification_id"] = v.ID
			}
			verificationRows = append(verificationRows, row)
		}
		anomalyRows := []map[string]any{}
		for index, scenario := range []string{"register", "register", "register", "register", "pending", "confirmed", "rejected", "unverified", "pagination"} {
			key := fmt.Sprintf("resolution-browser-anomaly-%d", index)
			invoice := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), key+"-invoice")
			w := request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", invoice), map[string]any{"provider": "mock", "expected_version": 1}, f.customer, key+"-intent")
			expect(t, w, 201)
			intent := data(t, w)
			pid := int64(intent["payment_id"].(float64))
			expect(t, request(f.h, "POST", closePath(invoice, pid), map[string]any{"expected_version": 1}, cookies["resolution-closer"], key+"-close"), 200)
			transaction := "fixture-late:" + key
			if scenario == "unverified" {
				transaction = ""
			}
			for event := 0; event < 2; event++ {
				if _, err := db.ProcessPaymentWebhook(t.Context(), "mock", fmt.Sprintf("%s-event-%d", key, event), intent["provider_payment_id"].(string), invoice, 3000, "succeeded", []byte(`{"fixture":"late-receipt"}`), transaction); err != nil {
					t.Fatal(err)
				}
			}
			var anchor, groupVersion int64
			if err := db.Pool.QueryRow(t.Context(), "SELECT min(id),max(id) FROM payment_close_anomalies WHERE payment_id=$1", pid).Scan(&anchor, &groupVersion); err != nil {
				t.Fatal(err)
			}
			row := map[string]any{"scenario": scenario, "invoice_id": invoice, "payment_id": pid, "anchor_anomaly_id": anchor, "group_version": groupVersion, "external_transaction_id": transaction, "amount_cents": 3000, "event_count": 2, "admin_path": fmt.Sprintf("/admin/invoices/%d", invoice), "constructed_by": "real API intent and mock trusted close; Store late webhook facts; same transaction repeated twice"}
			if scenario == "pending" || scenario == "confirmed" || scenario == "rejected" {
				base, path := anomalyPaths(invoice, anchor)
				w := request(f.h, "POST", path, paymentAnomalyInput(groupVersion, key+"-payout"), cookies["resolution-requester"], key+"-create")
				expect(t, w, 201)
				refund := data(t, w)["refund"].(map[string]any)
				row["refund_id"] = refund["id"]
				if scenario != "pending" {
					action := "confirm"
					if scenario == "rejected" {
						action = "reject"
					}
					w = request(f.h, "POST", fmt.Sprintf("%s/payment-anomaly-refunds/%.0f/%s", base, refund["id"], action), map[string]any{"expected_version": 1, "expected_group_version": groupVersion, "note": "隔离fixture独立复核，没有真实出款"}, cookies["resolution-reviewer"], key+"-review")
					expect(t, w, 201)
				}
			}
			if scenario == "pagination" {
				for group := 0; group < 11; group++ {
					if _, err := db.ProcessPaymentWebhook(t.Context(), "mock", fmt.Sprintf("%s-group-%d", key, group), intent["provider_payment_id"].(string), invoice, 3000, "succeeded", []byte(`{"fixture":"separate-late-transaction"}`), fmt.Sprintf("%s:%d", transaction, group)); err != nil {
						t.Fatal(err)
					}
				}
			}
			anomalyRows = append(anomalyRows, row)
		}
		writePaymentBrowserManifest(t, map[string]any{"fixture": "payment_resolution", "real_external_actions": false, "accounts": accounts, "customer_id": f.uid, "close_verification": verificationRows, "anomaly_refunds": anomalyRows})
	})
}
