package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"
)

func manualCollectionGrants(t *testing.T, f adminRenewalFixture) {
	t.Helper()
	contentRole(t, f.db, f.actor, "services.view", "invoices.view", "invoices.collect_manual", "services.renew", "services.renew.pay_balance")
}

func manualCollectionFixture(t *testing.T, manual bool) (adminRenewalFixture, map[string]any) {
	t.Helper()
	f := managementFixture(t, manual)
	invoice := f.createRenewal(t, "manual-collection-renewal")
	manualCollectionGrants(t, f)
	return f, invoice
}

func manualCollectionPath(f adminRenewalFixture, invoice map[string]any) string {
	return fmt.Sprintf("%s/renewals/%.0f", adminRenewalPath(f.sid), invoice["invoice_id"])
}

func manualCollectionInput(t *testing.T, f adminRenewalFixture, invoice map[string]any, reference string) map[string]any {
	t.Helper()
	var received time.Time
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT greatest(created_at,clock_timestamp()-interval '1 microsecond') FROM invoices WHERE id=$1", int64(invoice["invoice_id"].(float64))).Scan(&received); err != nil {
		t.Fatal(err)
	}
	return map[string]any{"expected_version": invoice["version"], "expected_service_version": f.version(t), "amount_cents": invoice["amount_cents"],
		"received_at": received.UTC().Format(time.RFC3339Nano), "method": "bank_transfer", "receiving_account": "隔离测试收款账户", "transaction_no": reference,
		"evidence_ref": "test://manual-collection/" + reference, "evidence_sha256": strings.Repeat("a", 64), "reason": "已核实线下全额到账，登记续费收款"}
}

func manualCollectionFacts(t *testing.T, f adminRenewalFixture) string {
	t.Helper()
	var facts string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_array(
 (SELECT coalesce(jsonb_agg(to_jsonb(c) ORDER BY id),'[]') FROM manual_collections c),
 (SELECT coalesce(jsonb_agg(to_jsonb(p) ORDER BY id),'[]') FROM payment_transactions p),
 (SELECT coalesce(jsonb_agg(to_jsonb(e) ORDER BY id),'[]') FROM payment_webhook_events e),
 (SELECT coalesce(jsonb_agg(to_jsonb(ii) ORDER BY id),'[]') FROM invoice_items ii))::text`).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	return managementFacts(t, f) + facts
}

func manualCollectionUnchanged(t *testing.T, f adminRenewalFixture, before string) {
	t.Helper()
	if after := manualCollectionFacts(t, f); after != before {
		t.Fatal("preview, replay or rejected manual collection changed durable facts", before, after)
	}
}

func manualCollectionQuote(t *testing.T, f adminRenewalFixture, invoice map[string]any, input map[string]any) map[string]any {
	t.Helper()
	before := manualCollectionFacts(t, f)
	w := request(f.h, "POST", manualCollectionPath(f, invoice)+"/manual-collection-quotes", input, f.staff, "")
	expect(t, w, 200)
	q := data(t, w)
	if q["quote_hash"] == nil || q["quote_hash"] == "" || q["expires_at"] == nil || q["invoice_id"] != invoice["invoice_id"] || q["service_id"] != float64(f.sid) {
		t.Fatal("manual collection preview omitted its confirmation boundary", w.Body.String())
	}
	manualCollectionUnchanged(t, f, before)
	body := make(map[string]any, len(input)+1)
	for field, value := range input {
		body[field] = value
	}
	body["quote_hash"] = q["quote_hash"]
	return body
}

func manualCollectionApply(t *testing.T, f adminRenewalFixture, invoice, body map[string]any, key string) map[string]any {
	t.Helper()
	w := request(f.h, "POST", manualCollectionPath(f, invoice)+"/manual-collections", body, f.staff, key)
	expect(t, w, 201)
	return data(t, w)
}

func TestAdminServiceManualCollectionSettlementAndPreservedFacts(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprintf("manual=%t", manual), func(t *testing.T) {
			f, invoice := manualCollectionFixture(t, manual)
			body := manualCollectionQuote(t, f, invoice, manualCollectionInput(t, f, invoice, "manual-positive"))
			before := f.snapshot(t)
			out := manualCollectionApply(t, f, invoice, body, "manual-positive-collection")
			if out["status"] != "paid" || out["renewal_status"] != "completed" || out["amount_cents"] != invoice["amount_cents"] || out["service_id"] != float64(f.sid) || out["customer_id"] != float64(f.uid) || out["collection_id"] == nil || out["payment_id"] == nil {
				t.Fatal("manual receipt did not produce an explicit attributed settlement", out)
			}
			after := f.snapshot(t)
			if after.balance != before.balance || after.ledger != before.ledger || after.contractPrice != before.contractPrice || after.discountsUsed != before.discountsUsed || after.effects != before.effects+1 || !after.due.Equal(before.due.AddDate(0, 1, 0)) || after.jobs != before.jobs || after.receipts != before.receipts+1 {
				t.Fatal("offline receipt moved wallet money, changed the contract or duplicated entitlement", before, after)
			}
			var provider, state string
			var paidAt, completedAt time.Time
			var collections, webhooks, succeeded int64
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT p.provider,p.status,p.completed_at,i.paid_at,
 (SELECT count(*) FROM manual_collections),(SELECT count(*) FROM payment_webhook_events),
 (SELECT count(*) FROM payment_transactions WHERE invoice_id=i.id AND status='succeeded')
 FROM payment_transactions p JOIN invoices i ON i.id=p.invoice_id WHERE p.id=$1`, int64(out["payment_id"].(float64))).Scan(&provider, &state, &completedAt, &paidAt, &collections, &webhooks, &succeeded); err != nil || provider != "manual_offline" || state != "succeeded" || collections != 1 || webhooks != 0 || succeeded != 1 || !paidAt.Equal(completedAt) {
				t.Fatal("offline payment was not a genuine immutable receipt with actual receipt time", provider, state, collections, webhooks, succeeded, err)
			}
			received, err := time.Parse(time.RFC3339Nano, body["received_at"].(string))
			if err != nil || !paidAt.Equal(received) {
				t.Fatal("payment dates do not preserve actual receipt time", paidAt, received, err)
			}
			facts := manualCollectionFacts(t, f)
			w := request(f.h, "POST", manualCollectionPath(f, invoice)+"/manual-collections", body, f.staff, "manual-positive-collection")
			expect(t, w, 200)
			if data(t, w)["replayed"] != true || data(t, w)["payment_id"] != out["payment_id"] {
				t.Fatal("receipt replay did not recover the original payment", w.Body.String())
			}
			manualCollectionUnchanged(t, f, facts)
		})
	}
	t.Run("supplier_collection_does_not_claim_upstream_renewal", func(t *testing.T) {
		supplier := supplierSetup(t)
		supplier.publish(t, supplier.pid, "manual-collection-supplier")
		sid, _ := supplier.purchase(t, supplier.pid, "manual-collection-supplier-purchase")
		if err := supplier.db.ProcessProvisionJob(t.Context(), *supplier.claim(t)); err != nil {
			t.Fatal(err)
		}
		actor, staff := user(t, supplier.db, supplier.h, "manual-supplier-staff", "admin")
		f := adminRenewalFixture{upgradeFixture: upgradeFixture{db: supplier.db, h: supplier.h, root: supplier.root, customer: supplier.customer, uid: supplier.uid, sid: sid}, actor: actor, staff: staff}
		contentRole(t, f.db, actor, "services.view", "invoices.view", "services.renew")
		invoice := f.createRenewal(t, "manual-supplier-renewal")
		manualCollectionGrants(t, f)
		var before time.Time
		if err := f.db.Pool.QueryRow(t.Context(), "SELECT next_due_at FROM services WHERE id=$1", sid).Scan(&before); err != nil {
			t.Fatal(err)
		}
		body := manualCollectionQuote(t, f, invoice, manualCollectionInput(t, f, invoice, "manual-supplier-receipt"))
		out := manualCollectionApply(t, f, invoice, body, "manual-supplier-collection")
		var due time.Time
		var pending int64
		if err := f.db.Pool.QueryRow(t.Context(), "SELECT next_due_at,(SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='renew' AND status='pending') FROM services WHERE id=$1", sid).Scan(&due, &pending); err != nil || !due.Equal(before) || pending != 1 || out["renewal_status"] != "pending" || supplier.renews.Load() != 0 || supplier.renewalPays.Load() != 0 {
			t.Fatal("local collection fabricated supplier completion", due, before, pending, out, err)
		}
	})
}

func TestAdminServiceManualCollectionStrictInputsPermissionsAndScope(t *testing.T) {
	f, invoice := manualCollectionFixture(t, false)
	in := manualCollectionInput(t, f, invoice, "manual-strict")
	body := manualCollectionQuote(t, f, invoice, in)
	path := manualCollectionPath(f, invoice) + "/manual-collections"
	before := manualCollectionFacts(t, f)
	for _, cookie := range []string{"", f.customer, f.other} {
		expect(t, request(f.h, "POST", path, body, cookie, "manual-wrong-audience"), 401)
	}
	expect(t, request(f.h, "POST", path, body, f.staff, ""), 400)
	for _, field := range []string{"expected_version", "expected_service_version", "amount_cents", "received_at", "method", "receiving_account", "transaction_no", "evidence_ref", "evidence_sha256", "reason", "quote_hash"} {
		value := body[field]
		delete(body, field)
		expect(t, request(f.h, "POST", path, body, f.staff, "manual-missing-field"), 400)
		body[field] = value
	}
	for _, change := range []struct {
		field string
		value any
	}{{"amount_cents", 0}, {"amount_cents", -1}, {"method", "epay"}, {"method", "balance"}, {"evidence_sha256", "invalid"}, {"received_at", "not-a-date"}, {"receiving_account", strings.Repeat("中", 121)}, {"transaction_no", strings.Repeat("x", 161)}, {"reason", strings.Repeat("中", 501)}} {
		value := body[change.field]
		body[change.field] = change.value
		expect(t, request(f.h, "POST", path, body, f.staff, "manual-invalid-field"), 400)
		body[change.field] = value
	}
	body["user_id"] = f.uid
	expect(t, request(f.h, "POST", path, body, f.staff, "manual-unknown-field"), 400)
	delete(body, "user_id")
	for _, grants := range [][]string{{"services.view", "invoices.view", "invoices.manage", "services.manage"}, {"invoices.collect_manual", "invoices.view"}, {"invoices.collect_manual", "services.view"}} {
		contentRole(t, f.db, f.actor, grants...)
		expect(t, request(f.h, "POST", path, body, f.staff, "manual-grant-missing"), 403)
		expect(t, request(f.h, "POST", manualCollectionPath(f, invoice)+"/manual-collection-quotes", in, f.staff, ""), 403)
	}
	manualCollectionGrants(t, f)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", f.actor); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path, body, f.staff, "manual-scope-missing"), 404)
	manualCollectionUnchanged(t, f, before)
}

func TestAdminServiceManualCollectionReceiptIdentityAndConcurrentCAS(t *testing.T) {
	for _, sameKey := range []bool{true, false} {
		t.Run(fmt.Sprintf("same_key=%t", sameKey), func(t *testing.T) {
			f, invoice := manualCollectionFixture(t, false)
			body := manualCollectionQuote(t, f, invoice, manualCollectionInput(t, f, invoice, "manual-concurrent"))
			before := f.snapshot(t)
			start, done := make(chan struct{}), make(chan *httptest.ResponseRecorder, 2)
			for n := range 2 {
				key := "manual-concurrent-collection"
				if !sameKey {
					key = fmt.Sprintf("manual-concurrent-%d", n)
				}
				go func(key string) {
					<-start
					done <- request(f.h, "POST", manualCollectionPath(f, invoice)+"/manual-collections", body, f.staff, key)
				}(key)
			}
			close(start)
			created, replayed, conflicts := 0, 0, 0
			for range 2 {
				w := awaitBalanceResponse(t, done)
				switch w.Code {
				case 201:
					created++
				case 200:
					replayed++
				case 409:
					conflicts++
				default:
					t.Fatal("concurrent collection failed", w.Code, w.Body.String())
				}
			}
			if created != 1 || (sameKey && replayed != 1) || (!sameKey && conflicts != 1) || f.snapshot(t).effects != before.effects+1 {
				t.Fatal("one physical receipt did not settle exactly once", created, replayed, conflicts)
			}
		})
	}
	t.Run("raw_receipt_is_not_reusable_under_another_alias_or_actor", func(t *testing.T) {
		f, invoice := manualCollectionFixture(t, false)
		body := manualCollectionQuote(t, f, invoice, manualCollectionInput(t, f, invoice, "manual-unique-physical-receipt"))
		manualCollectionApply(t, f, invoice, body, "manual-first-physical-receipt")
		next := f.createRenewal(t, "manual-second-renewal")
		in := manualCollectionInput(t, f, next, "manual-unique-physical-receipt")
		in["method"], in["receiving_account"] = "other_offline", "另一个账户别名"
		before := manualCollectionFacts(t, f)
		w := request(f.h, "POST", manualCollectionPath(f, next)+"/manual-collection-quotes", in, f.root, "")
		if w.Code == 200 {
			in["quote_hash"] = data(t, w)["quote_hash"]
			expect(t, request(f.h, "POST", manualCollectionPath(f, next)+"/manual-collections", in, f.root, "manual-other-actor-receipt"), 409)
		} else {
			expect(t, w, 409)
		}
		manualCollectionUnchanged(t, f, before)
		body["reason"] = "另一笔不同的确认意图"
		expect(t, request(f.h, "POST", manualCollectionPath(f, invoice)+"/manual-collections", body, f.staff, "manual-first-physical-receipt"), 409)
		manualCollectionUnchanged(t, f, before)
	})
	t.Run("confirmation_binds_input_invoice_and_service", func(t *testing.T) {
		f, invoice := manualCollectionFixture(t, false)
		body := manualCollectionQuote(t, f, invoice, manualCollectionInput(t, f, invoice, "manual-bound-receipt"))
		before := manualCollectionFacts(t, f)
		body["evidence_ref"] = "test://another-evidence"
		expect(t, request(f.h, "POST", manualCollectionPath(f, invoice)+"/manual-collections", body, f.staff, "manual-changed-proof"), 409)
		manualCollectionUnchanged(t, f, before)
		body = manualCollectionQuote(t, f, invoice, manualCollectionInput(t, f, invoice, "manual-bound-receipt"))
		if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET version=version+1 WHERE id=$1", f.sid); err != nil {
			t.Fatal(err)
		}
		before = manualCollectionFacts(t, f)
		expect(t, request(f.h, "POST", manualCollectionPath(f, invoice)+"/manual-collections", body, f.staff, "manual-stale-service"), 409)
		manualCollectionUnchanged(t, f, before)
	})
}

func TestAdminServiceManualCollectionBusinessFences(t *testing.T) {
	for _, scenario := range []string{"online_pending", "invoice_paid", "configuration_pending", "customer_disabled", "service_terminated", "stale_invoice", "stale_amount", "received_before_invoice", "received_after_now"} {
		t.Run(scenario, func(t *testing.T) {
			f, invoice := manualCollectionFixture(t, false)
			input := manualCollectionInput(t, f, invoice, "manual-fence-"+scenario)
			var body map[string]any
			if scenario != "received_before_invoice" && scenario != "received_after_now" {
				body = manualCollectionQuote(t, f, invoice, input)
			}
			var err error
			switch scenario {
			case "online_pending":
				_, err = f.db.Pool.Exec(t.Context(), `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status) SELECT 'epay_alipay',$1,id,user_id,amount_cents,'pending' FROM invoices WHERE id=$2`, "manual-fence-online", int64(invoice["invoice_id"].(float64)))
			case "invoice_paid":
				expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", int64(invoice["invoice_id"].(float64))), map[string]any{}, f.customer, "manual-fence-balance-payment"), 201)
			case "configuration_pending":
				expect(t, request(f.h, "POST", adminRenewalPath(f.sid)+"/actions", map[string]any{"action": "suspend"}, f.root, "manual-fence-action"), 202)
			case "customer_disabled":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE users SET status='disabled' WHERE id=$1", f.uid)
			case "service_terminated":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE services SET status='terminated',version=version+1 WHERE id=$1", f.sid)
			case "stale_invoice":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE invoices SET version=version+1 WHERE id=$1", int64(invoice["invoice_id"].(float64)))
			case "stale_amount":
				input["amount_cents"] = input["amount_cents"].(float64) + 1
			case "received_before_invoice":
				var created time.Time
				if err = f.db.Pool.QueryRow(t.Context(), "SELECT created_at FROM invoices WHERE id=$1", int64(invoice["invoice_id"].(float64))).Scan(&created); err == nil {
					input["received_at"] = created.Add(-time.Second).UTC().Format(time.RFC3339Nano)
				}
			case "received_after_now":
				input["received_at"] = time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339Nano)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := manualCollectionFacts(t, f)
			path := manualCollectionPath(f, invoice) + "/manual-collection-quotes"
			if scenario == "stale_amount" || scenario == "received_before_invoice" || scenario == "received_after_now" {
				w := request(f.h, "POST", path, input, f.staff, "")
				if w.Code != 400 && w.Code != 409 {
					t.Fatal("invalid receipt boundary was accepted unexpectedly", w.Code, w.Body.String())
				}
			} else {
				w := request(f.h, "POST", path, input, f.staff, "")
				if w.Code != 404 && w.Code != 409 {
					t.Fatal("business fence returned unexpected status", w.Code, w.Body.String())
				}
				if body != nil {
					expect(t, request(f.h, "POST", manualCollectionPath(f, invoice)+"/manual-collections", body, f.staff, "manual-fence-apply"), 409)
				}
			}
			manualCollectionUnchanged(t, f, before)
		})
	}
}

func TestAdminServiceManualCollectionPaymentRaces(t *testing.T) {
	t.Run("customer_balance_payment_wins", func(t *testing.T) {
		f, invoice := manualCollectionFixture(t, false)
		body := manualCollectionQuote(t, f, invoice, manualCollectionInput(t, f, invoice, "manual-balance-race"))
		expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", int64(invoice["invoice_id"].(float64))), map[string]any{}, f.customer, "manual-balance-race-pay"), 201)
		before := manualCollectionFacts(t, f)
		w := request(f.h, "POST", manualCollectionPath(f, invoice)+"/manual-collections", body, f.staff, "manual-balance-race-collection")
		if w.Code != 409 {
			t.Fatal("manual collection bypassed an already settled balance payment", w.Code, w.Body.String())
		}
		manualCollectionUnchanged(t, f, before)
	})
	t.Run("unknown_close_and_anomaly_block", func(t *testing.T) {
		f, invoice := manualCollectionFixture(t, false)
		body := manualCollectionQuote(t, f, invoice, manualCollectionInput(t, f, invoice, "manual-close-race"))
		if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status) SELECT 'epay_alipay','manual-close-payment',id,user_id,amount_cents,'pending' FROM invoices WHERE id=$1`, int64(invoice["invoice_id"].(float64))); err != nil {
			t.Fatal(err)
		}
		before := manualCollectionFacts(t, f)
		w := request(f.h, "POST", manualCollectionPath(f, invoice)+"/manual-collections", body, f.staff, "manual-close-race-collection")
		if w.Code != 409 {
			t.Fatal("manual collection bypassed pending online payment", w.Code, w.Body.String())
		}
		manualCollectionUnchanged(t, f, before)
	})
}

func TestAdminServiceManualCollectionConcurrentTrustedCallback(t *testing.T) {
	f, invoice := manualCollectionFixture(t, false)
	body := manualCollectionQuote(t, f, invoice, manualCollectionInput(t, f, invoice, "manual-concurrent-callback"))
	invoiceID := int64(invoice["invoice_id"].(float64))
	w := request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", invoiceID), map[string]any{"provider": "mock", "expected_version": invoice["version"]}, f.customer, "manual-concurrent-online-intent")
	expect(t, w, 201)
	intent := data(t, w)
	gate, err := f.db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback(context.Background())
	if _, err = gate.Exec(t.Context(), "SELECT id FROM invoices WHERE id=$1 FOR UPDATE", invoiceID); err != nil {
		t.Fatal(err)
	}
	before := f.snapshot(t)
	manualDone := make(chan *httptest.ResponseRecorder, 1)
	callbackDone := make(chan error, 1)
	go func() {
		manualDone <- request(f.h, "POST", manualCollectionPath(f, invoice)+"/manual-collections", body, f.staff, "manual-concurrent-callback-collection")
	}()
	go func() {
		// Exercise the trusted settlement core with a local mock receipt; this
		// does not claim to validate a live gateway or its notification signature.
		_, callbackErr := f.db.ProcessPaymentWebhook(t.Context(), "mock", "manual-concurrent-callback-event", intent["provider_payment_id"].(string), invoiceID, int64(invoice["amount_cents"].(float64)), "succeeded", []byte(`{"fixture":"concurrent-online-receipt"}`), "mock:manual-concurrent-callback")
		callbackDone <- callbackErr
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
	defer cancel()
	for {
		var waiting int
		// Both writers serialize financial customer before invoice. The second
		// writer can therefore wait behind the first writer's customer lock,
		// rather than directly behind the fixture's invoice lock.
		if err = f.db.Pool.QueryRow(ctx, `WITH RECURSIVE waiters(pid) AS (
 SELECT a.pid FROM pg_stat_activity a WHERE datname=current_database() AND $1::integer=ANY(pg_blocking_pids(a.pid))
 UNION
 SELECT a.pid FROM pg_stat_activity a JOIN waiters w ON w.pid=ANY(pg_blocking_pids(a.pid)) WHERE datname=current_database()
) SELECT count(*) FROM waiters`, gate.Conn().PgConn().PID()).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("manual collection and trusted callback did not overlap behind the locked invoice")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err = gate.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	expect(t, awaitBalanceResponse(t, manualDone), 409)
	select {
	case err = <-callbackDone:
		if err != nil {
			t.Fatal("trusted online receipt lost the payment race", err)
		}
	case <-ctx.Done():
		t.Fatal("trusted online receipt did not finish after the invoice lock was released")
	}
	after := f.snapshot(t)
	if after.balance != before.balance || after.ledger != before.ledger || after.effects != before.effects+1 || !after.due.Equal(before.due.AddDate(0, 1, 0)) || after.receipts != before.receipts {
		t.Fatal("concurrent manual/online collection moved wallet money or settled entitlement twice", before, after)
	}
	var status string
	var succeeded, collections, callbacks int
	if err = f.db.Pool.QueryRow(t.Context(), `SELECT i.status,
 (SELECT count(*) FROM payment_transactions p WHERE p.invoice_id=i.id AND p.status='succeeded'),
 (SELECT count(*) FROM manual_collections c WHERE c.invoice_id=i.id),
 (SELECT count(*) FROM payment_webhook_events WHERE event_id='manual-concurrent-callback-event')
 FROM invoices i WHERE i.id=$1`, invoiceID).Scan(&status, &succeeded, &collections, &callbacks); err != nil || status != "paid" || succeeded != 1 || collections != 0 || callbacks != 1 {
		t.Fatal("manual/online race did not preserve exactly one actual receipt", status, succeeded, collections, callbacks, err)
	}
	facts := manualCollectionFacts(t, f)
	if _, err = f.db.ProcessPaymentWebhook(t.Context(), "mock", "manual-concurrent-callback-event", intent["provider_payment_id"].(string), invoiceID, int64(invoice["amount_cents"].(float64)), "succeeded", []byte(`{"fixture":"concurrent-online-receipt"}`), "mock:manual-concurrent-callback"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", manualCollectionPath(f, invoice)+"/manual-collections", body, f.staff, "manual-concurrent-callback-collection"), 409)
	manualCollectionUnchanged(t, f, facts)
}

func TestAdminServiceManualCollectionRollbackAndUnknownCommit(t *testing.T) {
	f, invoice := manualCollectionFixture(t, false)
	body := manualCollectionQuote(t, f, invoice, manualCollectionInput(t, f, invoice, "manual-rollback"))
	path := manualCollectionPath(f, invoice) + "/manual-collections"
	before := manualCollectionFacts(t, f)
	for _, table := range []string{"audit_events", "mutation_operations"} {
		if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_manual_collection_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'manual collection durability failure'; END $$;
 CREATE TRIGGER reject_manual_collection_write BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_manual_collection_write()`); err != nil {
			t.Fatal(err)
		}
		expect(t, request(f.h, "POST", path, body, f.staff, "manual-rollback"), 500)
		manualCollectionUnchanged(t, f, before)
		if _, err := f.db.Pool.Exec(t.Context(), "DROP TRIGGER reject_manual_collection_write ON "+table+"; DROP FUNCTION reject_manual_collection_write()"); err != nil {
			t.Fatal(err)
		}
	}
	var commits atomic.Int32
	cfg := f.db.Pool.Config().Copy()
	cfg.ConnConfig.BuildFrontend = func(reader io.Reader, writer io.Writer) *pgproto3.Frontend {
		return pgproto3.NewFrontend(&uploadCommitAckReader{reader: reader, count: &commits, dropAt: 1}, writer)
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	w := request(renewalBatchHTTPWithPool(f.db, pool), "POST", path, body, f.staff, "manual-lost-commit")
	if commits.Load() < 1 || (w.Code != 200 && w.Code != 201 && w.Code != 500 && w.Code != 503) {
		t.Fatal("manual collection did not exercise COMMIT acknowledgement loss", commits.Load(), w.Code, w.Body.String())
	}
	facts := manualCollectionFacts(t, f)
	w = request(f.h, "POST", path, body, f.staff, "manual-lost-commit")
	expect(t, w, 200)
	if data(t, w)["replayed"] != true {
		t.Fatal("manual collection lost receipt after commit acknowledgement loss", w.Body.String())
	}
	manualCollectionUnchanged(t, f, facts)
}

func TestAdminServiceManualCollectionOriginalCredentialAndAuthorityWaits(t *testing.T) {
	for _, credential := range []string{"session", "api_key"} {
		t.Run(credential, func(t *testing.T) {
			f, invoice := manualCollectionFixture(t, false)
			body := manualCollectionQuote(t, f, invoice, manualCollectionInput(t, f, invoice, "manual-credential-wait"))
			secret, table, column, hash := adminRenewalTestCredential(t, f, credential)
			gate, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback(t.Context())
			var blocker uint32
			if err = gate.QueryRow(t.Context(), "SELECT pg_backend_pid() FROM invoices WHERE id=$1 FOR UPDATE", int64(invoice["invoice_id"].(float64))).Scan(&blocker); err != nil {
				t.Fatal(err)
			}
			before := manualCollectionFacts(t, f)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- managementCredentialRequest(f, "POST", manualCollectionPath(f, invoice)+"/manual-collections", body, "manual-credential-wait", secret)
			}()
			waitUploadBlockedBy(t, f.db, blocker)
			if _, err = f.db.Pool.Exec(t.Context(), "UPDATE "+table+" SET expires_at=clock_timestamp()-interval '1 second' WHERE "+column+"=$1", hash); err != nil {
				t.Fatal(err)
			}
			if err = gate.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			expect(t, awaitBalanceResponse(t, done), 401)
			manualCollectionUnchanged(t, f, before)
		})
	}
}

func TestAdminServiceManualCollectionRawSourceWaitRechecksCredential(t *testing.T) {
	for _, credential := range []string{"session", "api_key"} {
		for _, naturalExpiry := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/natural_expiry=%t", credential, naturalExpiry), func(t *testing.T) {
				f, invoice := manualCollectionFixture(t, false)
				body := manualCollectionQuote(t, f, invoice, manualCollectionInput(t, f, invoice, "manual-raw-source-wait"))
				secret, table, column, hash := adminRenewalTestCredential(t, f, credential)
				gate, err := f.db.Pool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer gate.Rollback(context.Background())
				if _, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended('refund-original:'||$1,0))", body["transaction_no"]); err != nil {
					t.Fatal(err)
				}
				if naturalExpiry {
					if _, err = f.db.Pool.Exec(t.Context(), "UPDATE "+table+" SET expires_at=clock_timestamp()+interval '1200 milliseconds' WHERE "+column+"=$1", hash); err != nil {
						t.Fatal(err)
					}
				}
				before := manualCollectionFacts(t, f)
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					done <- managementCredentialRequest(f, "POST", manualCollectionPath(f, invoice)+"/manual-collections", body, "manual-raw-source-credential", secret)
				}()
				waitUploadBlockedBy(t, f.db, gate.Conn().PgConn().PID())
				if naturalExpiry {
					waitAdminRenewalCredentialExpiry(t, f.db, table, column, hash)
				} else {
					// This write must finish while the business-source lock remains
					// held. Taking the request credential early would block revocation.
					revokeCtx, revokeCancel := context.WithTimeout(t.Context(), 3*time.Second)
					if credential == "session" {
						_, err = f.db.Pool.Exec(revokeCtx, "DELETE FROM sessions WHERE token_hash=$1", hash)
					} else {
						_, err = f.db.Pool.Exec(revokeCtx, "UPDATE api_keys SET revoked_at=clock_timestamp() WHERE secret_hash=$1", hash)
					}
					revokeCancel()
					if err != nil {
						t.Fatal("raw-source wait locked the original credential before business waits ended", err)
					}
				}
				if err = gate.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				expect(t, awaitBalanceResponse(t, done), 401)
				manualCollectionUnchanged(t, f, before)
			})
		}
	}
}

func TestAdminServiceManualCollectionReservedProviderAndImmutableFacts(t *testing.T) {
	f, invoice := manualCollectionFixture(t, false)
	for _, provider := range []string{"manual_offline", "manual", "bank_transfer"} {
		expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", int64(invoice["invoice_id"].(float64))), map[string]any{"provider": provider, "expected_version": invoice["version"]}, f.customer, "manual-direct-provider-"+provider), 400)
	}
	if _, err := f.db.ProcessPaymentWebhook(t.Context(), "manual_offline", "manual-direct-webhook", "manual-direct-payment", int64(invoice["invoice_id"].(float64)), int64(invoice["amount_cents"].(float64)), "succeeded", []byte(`{"manual":true}`)); err == nil {
		t.Fatal("manual provider callback bypassed the collection command")
	}
	if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status) SELECT 'manual_offline','manual-fact-forge',id,user_id,amount_cents,'succeeded' FROM invoices WHERE id=$1`, int64(invoice["invoice_id"].(float64))); err == nil {
		t.Fatal("direct succeeded manual payment fact bypassed immutable collection writer")
	}
}

func TestAdminServiceManualCollectionCashReportsAndRefundRoundTrip(t *testing.T) {
	f, invoice := manualCollectionFixture(t, false)
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	var now time.Time
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT clock_timestamp()").Scan(&now); err != nil {
		t.Fatal(err)
	}
	now = now.In(shanghai)
	// Separate the real cash dates from this month's administrator confirmation.
	// The fixture's bill predates the receipt; no production fact is rewritten.
	received := time.Date(now.Year(), now.Month()-2, 15, 12, 0, 0, 0, shanghai)
	refundReceived := received.AddDate(0, 1, 0)
	var fixtureInvoiceVersion int64
	if err = f.db.Pool.QueryRow(t.Context(), "UPDATE invoices SET created_at=$2 WHERE id=$1 RETURNING version", int64(invoice["invoice_id"].(float64)), received.Add(-time.Hour)).Scan(&fixtureInvoiceVersion); err != nil {
		t.Fatal(err)
	}
	invoice["version"] = float64(fixtureInvoiceVersion)
	input := manualCollectionInput(t, f, invoice, "manual-report-receipt")
	input["received_at"] = received.UTC().Format(time.RFC3339Nano)
	body := manualCollectionQuote(t, f, invoice, input)
	before := f.snapshot(t)
	out := manualCollectionApply(t, f, invoice, body, "manual-report-collection")
	get := func(path string) map[string]any {
		t.Helper()
		w := request(f.h, "GET", path, nil, f.root, "")
		expect(t, w, 200)
		return data(t, w)
	}
	immutableFacts := func() string {
		t.Helper()
		var facts string
		if err := f.db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_array(to_jsonb(c),to_jsonb(p))::text
 FROM manual_collections c JOIN payment_transactions p ON p.id=c.payment_id WHERE c.id=$1`, int64(out["collection_id"].(float64))).Scan(&facts); err != nil {
			t.Fatal(err)
		}
		return facts
	}
	collectedFacts := immutableFacts()
	summaryPath := fmt.Sprintf("/api/v1/admin/transactions/summary?customer_id=%d", f.uid)
	summary := get(summaryPath)
	if summary["manual_income_cents"] != invoice["amount_cents"] || summary["gateway_income_cents"] != float64(0) || summary["cash_income_cents"] != invoice["amount_cents"] {
		t.Fatal("manual receipt was misclassified in transaction cash summary", summary)
	}
	page := get(fmt.Sprintf("/api/v1/admin/transactions/page?customer_id=%d&channel=manual_offline&limit=100", f.uid))
	items := page["items"].([]any)
	if page["total"] != float64(1) || len(items) != 1 {
		t.Fatal("manual receipt missing or duplicated in transaction page", page)
	}
	entry := items[0].(map[string]any)
	if entry["source"] != "manual_receipt" || entry["channel"] != "manual_offline" || entry["transaction_no"] != input["transaction_no"] || entry["invoice_id"] != invoice["invoice_id"] || entry["id"] != out["payment_id"] {
		t.Fatal("manual transaction page lost its genuine receipt source", entry)
	}
	report := get(fmt.Sprintf("/api/v1/admin/reports/annual-cash?year=%d", received.Year()))
	total := report["total"].(map[string]any)
	if total["manual_received_cents"] != invoice["amount_cents"] || total["gateway_received_cents"] != float64(0) {
		t.Fatal("manual receipt was misclassified in annual cash report", report)
	}
	refundPath := fmt.Sprintf("/api/v1/admin/invoices/%d/refund", int64(invoice["invoice_id"].(float64)))
	w := request(f.h, "POST", refundPath, map[string]any{}, f.root, "manual-report-wallet-refund")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "external_refund_requires_gateway") {
		t.Fatal("manual external receipt was incorrectly refunded to wallet", w.Code, w.Body.String())
	}
	requesterID, requester := externalRefundActor(t, f.db, f.h, "manual-collection-refund-requester", []string{"invoices.view", "external_refunds.view", "external_refunds.create", "external_refunds.renewal_reverse"})
	_ = requesterID
	_, reviewer := externalRefundActor(t, f.db, f.h, "manual-collection-refund-reviewer", []string{"invoices.view", "external_refunds.view", "external_refunds.review", "external_refunds.renewal_reverse"})
	var effectID, serviceVersion, invoiceVersion int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT e.id,s.version,i.version FROM service_renewal_effects e JOIN services s ON s.id=e.service_id JOIN invoices i ON i.id=e.invoice_id WHERE e.invoice_id=$1`, int64(invoice["invoice_id"].(float64))).Scan(&effectID, &serviceVersion, &invoiceVersion); err != nil {
		t.Fatal("manual collection did not create the local renewal effect", err)
	}
	refundInput := map[string]any{"payment_id": out["payment_id"], "amount_cents": invoice["amount_cents"], "method": "original_route", "provider_refund_id": "manual-report-external-refund", "received_at": refundReceived.UTC().Format(time.RFC3339Nano), "evidence_ref": "test://manual-collection/refund", "evidence_sha256": strings.Repeat("b", 64), "note": "已核验人工收款原路退款", "expected_version": invoiceVersion, "expected_renewal_effect_id": effectID, "expected_renewal_service_version": serviceVersion}
	externalRefundPath := fmt.Sprintf("/api/v1/admin/invoices/%d/external-refunds", int64(invoice["invoice_id"].(float64)))
	created := request(f.h, "POST", externalRefundPath, refundInput, requester, "manual-report-external-refund")
	expect(t, created, 201)
	refund := data(t, created)["refund"].(map[string]any)
	confirmPath := fmt.Sprintf("%s/%.0f/confirm", externalRefundPath, refund["id"])
	expect(t, request(f.h, "POST", confirmPath, map[string]any{"expected_version": 1, "note": "人工收款退款复核通过"}, reviewer, "manual-report-external-confirm"), 201)
	var status string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM invoices WHERE id=$1", int64(invoice["invoice_id"].(float64))).Scan(&status); err != nil || status != "refunded" {
		t.Fatal("manual external refund did not complete the existing reversal path", status, err)
	}
	after := f.snapshot(t)
	if after.balance != before.balance || after.ledger != before.ledger || after.contractPrice != before.contractPrice || after.discountsUsed != before.discountsUsed || !after.due.Equal(before.due) {
		t.Fatal("manual external refund did not exactly reverse entitlement while preserving wallet and contract", before, after)
	}
	if facts := immutableFacts(); facts != collectedFacts {
		t.Fatal("manual refund mutated or removed original collection/payment facts", collectedFacts, facts)
	}
	summary = get(summaryPath)
	for key, want := range map[string]any{
		"gateway_income_cents": float64(0), "net_gateway_cents": float64(0),
		"manual_income_cents": invoice["amount_cents"], "manual_refund_cents": invoice["amount_cents"],
		"net_manual_cents": float64(0), "net_cash_cents": float64(0),
		"external_refund_cents": invoice["amount_cents"], "cash_income_cents": invoice["amount_cents"],
	} {
		if summary[key] != want {
			t.Fatal("manual refund contaminated gateway totals or erased the original receipt", key, want, summary)
		}
	}
	years := []int{received.Year()}
	if refundReceived.Year() != received.Year() {
		years = append(years, refundReceived.Year())
	}
	amount := invoice["amount_cents"].(float64)
	for _, year := range years {
		report = get(fmt.Sprintf("/api/v1/admin/reports/annual-cash?year=%d", year))
		total = report["total"].(map[string]any)
		var income, refunded float64
		if year == received.Year() {
			income = amount
		}
		if year == refundReceived.Year() {
			refunded = amount
		}
		for key, want := range map[string]float64{"manual_received_cents": income, "received_cents": income, "gateway_received_cents": 0, "refund_cents": refunded, "outgoing_cents": refunded, "net_cents": income - refunded} {
			if total[key] != want {
				t.Fatal("annual manual cash/refund totals use the wrong year or source", year, key, want, total)
			}
		}
		for _, raw := range report["months"].([]any) {
			month := raw.(map[string]any)
			var monthIncome, monthRefund float64
			if month["month"] == received.Format("2006-01") {
				monthIncome = amount
			}
			if month["month"] == refundReceived.Format("2006-01") {
				monthRefund = amount
			}
			if month["manual_received_cents"] != monthIncome || month["refund_cents"] != monthRefund || month["outgoing_cents"] != monthRefund || month["net_cents"] != monthIncome-monthRefund {
				t.Fatal("cash report buckets must use receipt/refund dates rather than confirmation date", month, received, refundReceived, now)
			}
		}
	}
}
