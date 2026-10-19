package httpapi

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Auth sees the last committed role while a revocation transaction is open.
// A mutation must then lock/read that role inside its own transaction, waiting
// for the revocation and rejecting before it changes business state or replays.
func TestAdminOrderMutationsRejectPermissionRevokedAfterHTTPAuth(t *testing.T) {
	for _, action := range []string{"notes", "cancel", "delete", "review", "invoice", "create", "create_replay", "quote"} {
		t.Run(action, func(t *testing.T) {
			f := newMutationOrderFixture(t)
			method := "POST"
			path := ""
			var body any
			switch action {
			case "create", "create_replay", "quote":
				in := adminOrderQuoted(t, f, adminOrderDraft(t, f), f.staff)
				body = in
				path = "/api/v1/admin/orders"
				if action == "quote" {
					path += "/quote"
				}
				if action == "create_replay" {
					expect(t, request(f.h, "POST", path, in, f.staff, "revoke-after-auth-key"), 201)
				}
			case "invoice":
				in := adminOrderDraft(t, f)
				generateInvoice := false
				in.GenerateInvoice = &generateInvoice
				in = adminOrderQuoted(t, f, in, f.root)
				w := request(f.h, "POST", "/api/v1/admin/orders", in, f.root, "revoke-deferred-fixture")
				expect(t, w, 201)
				d := data(t, w)
				path = fmt.Sprintf("/api/v1/admin/orders/%d/invoice", int64(d["order_id"].(float64)))
				body = map[string]any{"expected_version": d["version"]}
			default:
				var id int64
				if action == "review" {
					id, _ = f.paidReviewOrder(t, "revoke-review-fixture")
				} else {
					id, _ = f.order(t, "revoke-order-fixture", 1)
				}
				path = fmt.Sprintf("/api/v1/admin/orders/%d", id)
				payload := map[string]any{"expected_version": orderVersion(t, f.db, id)}
				switch action {
				case "notes":
					path += "/notes"
					payload["notes"] = "must not save after revoke"
				case "cancel":
					path += "/cancel"
				case "review":
					path += "/review"
					payload["decision"] = "approved"
				case "delete":
					method = "DELETE"
					payload["reason"] = "must not delete after revoke"
				}
				body = payload
			}
			var beforeOrders, beforeInvoices, beforeReceipts, beforeAudits int
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM orders),(SELECT count(*) FROM invoices),(SELECT count(*) FROM mutation_operations WHERE scope LIKE 'order:%' OR scope='admin:order.create'),(SELECT count(*) FROM audit_events)`).Scan(&beforeOrders, &beforeInvoices, &beforeReceipts, &beforeAudits); err != nil {
				t.Fatal(err)
			}
			tx, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(t.Context())
			var blocker int
			if err = tx.QueryRow(t.Context(), "SELECT pg_backend_pid()").Scan(&blocker); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['orders.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", f.staffID); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- request(f.h, method, path, body, f.staff, "revoke-after-auth-key") }()
			blocked := false
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if err = f.db.Pool.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM pg_stat_activity a WHERE $1=ANY(pg_blocking_pids(a.pid)) AND a.query LIKE 'SELECT active AND%orders.manage%')", blocker).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				select {
				case response := <-done:
					t.Fatalf("mutation did not await transaction authorization: status=%d body=%s", response.Code, response.Body.String())
				default:
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !blocked {
				tx.Rollback(t.Context())
				<-done
				t.Fatal("mutation never awaited role permission lock")
			}
			if err = tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			response := <-done
			expect(t, response, 403)
			if !strings.Contains(response.Body.String(), "permission_denied") {
				t.Fatal("wrong rejection", response.Body.String())
			}
			var orders, invoices, receipts, audits int
			if err = f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM orders),(SELECT count(*) FROM invoices),(SELECT count(*) FROM mutation_operations WHERE scope LIKE 'order:%' OR scope='admin:order.create'),(SELECT count(*) FROM audit_events)`).Scan(&orders, &invoices, &receipts, &audits); err != nil {
				t.Fatal(err)
			}
			if orders != beforeOrders || invoices != beforeInvoices || receipts != beforeReceipts || audits != beforeAudits {
				t.Fatal("revoked write left business state", orders, invoices, receipts, audits)
			}
		})
	}
}
