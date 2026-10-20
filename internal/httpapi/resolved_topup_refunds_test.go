package httpapi

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExternalRefundResolvedTopupHTTPRevocationWhileLocked(t *testing.T) {
	for _, scenario := range []string{"read", "create", "confirm", "replay"} {
		for _, revoke := range []string{"scope", "permission"} {
			t.Run(scenario+"_"+revoke, func(t *testing.T) {
				f := onlineTopupFixture(t)
				invoice, _ := topupLiabilityPendingHTTP(t, f, "scope-refund-"+scenario)
				expect(t, request(f.h, "POST", topupLiabilityPath(invoice)+"/resolve", map[string]any{"expected_version": topupLiabilityVersion(t, f, invoice), "note": "isolated resolved credit"}, f.root, "scope-refund-credit"), 201)
				staffID, staff := externalRefundActor(t, f.db, f.h, "scope-refund-staff", []string{"external_refunds.view", "external_refunds.create", "external_refunds.review"})
				var payment int64
				if err := f.db.Pool.QueryRow(t.Context(), `SELECT id FROM payment_transactions WHERE invoice_id=$1`, invoice).Scan(&payment); err != nil {
					t.Fatal(err)
				}
				body := map[string]any{"payment_id": payment, "amount_cents": 1234, "method": "original_route", "provider_refund_id": "scope-refund-proof", "received_at": time.Now().UTC().Format(time.RFC3339Nano), "evidence_ref": "test://scope-refund", "evidence_sha256": strings.Repeat("a", 64), "note": "isolated external receipt", "expected_version": topupLiabilityVersion(t, f, invoice)}
				path := fmt.Sprintf("/api/v1/admin/invoices/%d/external-refunds", invoice)
				if scenario == "confirm" {
					created := request(f.h, "POST", path, body, f.root, "scope-refund-request")
					expect(t, created, 201)
					path = fmt.Sprintf("%s/%.0f/confirm", path, data(t, created)["refund"].(map[string]any)["id"])
					body = map[string]any{"expected_version": 1, "note": "independent receipt verification"}
				}
				if scenario == "replay" {
					expect(t, request(f.h, "POST", path, body, staff, "scope-refund-write"), 201)
				}
				snapshot := func() string {
					var value string
					if err := f.db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_array((SELECT balance_cents FROM users WHERE id=$1),(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM external_refunds r WHERE invoice_id=$2),(SELECT jsonb_agg(to_jsonb(v) ORDER BY refund_id) FROM external_refund_reversal_reservations v WHERE invoice_id=$2),(SELECT count(*) FROM balance_ledger WHERE user_id=$1),(SELECT count(*) FROM mutation_operations WHERE scope='external-refund' AND (result->'refund'->>'invoice_id')::bigint=$2),(SELECT count(*) FROM audit_events))::text`, f.uid, invoice).Scan(&value); err != nil {
						t.Fatal(err)
					}
					return value
				}
				before := snapshot()
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				tx, err := f.db.Pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(context.Background())
				var blocker int
				if err = tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&blocker); err != nil {
					t.Fatal(err)
				}
				statement := `UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1`
				expected := 404
				if revoke == "permission" {
					statement = `UPDATE admin_roles SET active=false WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)`
					expected = 403
				}
				if _, err = tx.Exec(ctx, statement, staffID); err != nil {
					t.Fatal(err)
				}
				result := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					if scenario == "read" {
						result <- request(f.h, "GET", path, nil, staff, "")
					} else {
						result <- request(f.h, "POST", path, body, staff, "scope-refund-write")
					}
				}()
				for {
					var waiting bool
					if err = f.db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&waiting); err != nil {
						t.Fatal(err)
					}
					if waiting {
						break
					}
					select {
					case w := <-result:
						t.Fatalf("request did not wait: %d %s", w.Code, w.Body.String())
					case <-ctx.Done():
						t.Fatal("no actual lock wait")
					case <-time.After(10 * time.Millisecond):
					}
				}
				if err = tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				select {
				case w := <-result:
					expect(t, w, expected)
				case <-ctx.Done():
					t.Fatal("request stuck after revocation")
				}
				if after := snapshot(); after != before {
					t.Fatalf("denied request mutated funds: %s/%s", before, after)
				}
			})
		}
	}
}
