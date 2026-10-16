package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

// Opt-in real TCP HTTP acceptance fixture. All orders are created through cart
// or admin quote/submit and paid by locally signed notifications. The gateway
// rejects outgoing calls; this fixture never sends merchant refunds/resources.
// The existing harness owns a random schema, private ready/manifest files and
// finish-file controlled shutdown/cleanup.
func TestOrderExternalRefundBrowserFixture(t *testing.T) {
	if os.Getenv("ZJMFGO_BROWSER_READY_FILE") == "" || os.Getenv("ZJMFGO_BROWSER_FINISH_FILE") == "" {
		t.Skip("opt-in manual pending-order external refund browser fixture")
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "isolated merchant fixture; no external actions", http.StatusServiceUnavailable)
	}))
	defer gateway.Close()
	t.Setenv("ZJMFGO_BROWSER_EPAY_API_URL", gateway.URL)
	adminBrowserFixture(t, "order-refund-browser-super", func(db *store.Store, h http.Handler) {
		f := setupOrderExternalRefundHTTPFixture(t, db, h, gateway.URL)
		type actor struct {
			name        string
			permissions []string
			limited     bool
		}
		base := []string{"invoices.view", "orders.view", "services.view", "external_refunds.view"}
		permissions := func(extra ...string) []string {
			result := append([]string(nil), base...)
			return append(result, extra...)
		}
		actors := []actor{
			{"order-refund-browser-requester", permissions("external_refunds.create", "external_refunds.review", "external_refunds.order_reverse"), false},
			{"order-refund-browser-reviewer", permissions("external_refunds.review", "external_refunds.order_reverse"), false},
			{"order-refund-browser-reader", permissions(), false},
			{"order-refund-browser-no-reverse", permissions("external_refunds.create", "external_refunds.review"), false},
			{"order-refund-browser-limited", permissions("external_refunds.create", "external_refunds.review", "external_refunds.order_reverse"), true},
		}
		accounts := []map[string]any{}
		cookies := map[string]string{}
		for _, actor := range actors {
			id, cookie := externalRefundActor(t, db, f.h, actor.name, actor.permissions)
			if actor.limited {
				if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1`, id); err != nil {
					t.Fatal(err)
				}
			}
			accounts = append(accounts, map[string]any{"id": id, "username": actor.name, "permissions": actor.permissions, "limited": actor.limited})
			cookies[actor.name] = cookie
		}
		cases := []struct{ scenario, mode, state string }{
			{"confirm", "cart", "empty"}, {"reject", "admin", "empty"}, {"receipt_loss", "cart", "empty"}, {"version_conflict", "admin", "empty"},
			{"pending", "cart", "pending"}, {"applied", "admin", "applied"}, {"released", "cart", "released"}, {"approved_ineligible", "admin", "approved"},
		}
		rows := []map[string]any{}
		for index, scenario := range cases {
			key := fmt.Sprintf("order-refund-browser-%d", index)
			out := f.paid(t, scenario.mode, key)
			input := f.input(t, out, key+"-evidence")
			row := map[string]any{"scenario": scenario.scenario, "creation_path": scenario.mode, "order_id": out.order, "invoice_id": out.invoice, "payment_id": out.payment, "amount_cents": out.amount, "admin_path": fmt.Sprintf("/admin/invoices/%d", out.invoice), "order_path": fmt.Sprintf("/admin/orders/%d", out.order), "expected_version": input["expected_version"], "expected_order_version": input["expected_order_version"], "expected_service_versions": input["expected_service_versions"]}
			if scenario.state == "approved" {
				expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/orders/%d/review", out.order), map[string]any{"decision": "approved", "note": "isolated negative case already approved", "expected_version": orderVersion(t, db, out.order)}, f.root, key+"-approve"), 201)
			} else if scenario.state != "empty" {
				created := request(f.h, "POST", orderExternalRefundPath(out.invoice), input, cookies["order-refund-browser-requester"], key+"-create")
				expect(t, created, 201)
				refund := data(t, created)["refund"].(map[string]any)
				row["refund_id"] = refund["id"]
				if scenario.state != "pending" {
					action := "confirm"
					if scenario.state == "released" {
						action = "reject"
					}
					expect(t, request(f.h, "POST", fmt.Sprintf("%s/%.0f/%s", orderExternalRefundPath(out.invoice), refund["id"], action), map[string]any{"expected_version": 1, "note": "isolated independent order refund review"}, cookies["order-refund-browser-reviewer"], key+"-review"), 201)
				}
			}
			rows = append(rows, row)
		}
		writePaymentBrowserManifest(t, map[string]any{"fixture": "manual_pending_order_external_refund", "real_external_actions": false, "customer_id": f.uid, "customer_username": "order-refund-customer", "balance_cents": 12345, "accounts": accounts, "invoices": rows})
	})
}
