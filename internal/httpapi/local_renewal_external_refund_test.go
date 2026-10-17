package httpapi

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type localRenewalRefundHTTPFixture struct {
	recurringPurchase
	invoice, payment    int64
	callback            url.Values
	requester, reviewer string
	requesterID         int64
	beforeDue           time.Time
	baseline            string
}

func newLocalRenewalRefundHTTPFixture(t *testing.T) localRenewalRefundHTTPFixture {
	t.Helper()
	return setupLocalRenewalRefundHTTPFixture(t, renewalPurchaseFixture(t, "fixed", 500, 2), "", "local-refund")
}
func setupLocalRenewalRefundHTTPFixture(t *testing.T, p recurringPurchase, gateway, prefix string) localRenewalRefundHTTPFixture {
	t.Helper()
	cfg := epayTestConfig()
	if gateway != "" {
		cfg.EpayAPIURL = gateway
	}
	p.h = New(p.db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	f := localRenewalRefundHTTPFixture{recurringPurchase: p}
	permissions := []string{"invoices.view", "services.view", "external_refunds.view", "external_refunds.create", "external_refunds.review", "external_refunds.renewal_reverse"}
	f.requesterID, f.requester = externalRefundActor(t, p.db, p.h, prefix+"-renewal-refund-requester", permissions)
	_, f.reviewer = externalRefundActor(t, p.db, p.h, prefix+"-renewal-refund-reviewer", permissions)
	if err := p.db.Pool.QueryRow(t.Context(), `SELECT next_due_at FROM services WHERE id=$1`, p.sid).Scan(&f.beforeDue); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Pool.Exec(t.Context(), `UPDATE services SET auto_renew=true WHERE id=$1`, p.sid); err != nil {
		t.Fatal(err)
	}
	if err := p.db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_array(to_jsonb(o),to_jsonb(p),u.balance_cents,(SELECT coalesce(jsonb_agg(to_jsonb(c)),'[]') FROM customer_credit_accounts c WHERE c.user_id=u.id),(SELECT coalesce(jsonb_agg(to_jsonb(l) ORDER BY l.id),'[]') FROM balance_ledger l WHERE l.user_id=u.id))::text FROM orders o JOIN products p ON p.id=$2 JOIN users u ON u.id=o.user_id WHERE o.id=$1`, p.orderID, p.pid).Scan(&f.baseline); err != nil {
		t.Fatal(err)
	}
	f.invoice = renewLimited(t, p, prefix+"-local-refund-renew", 734)
	w := request(p.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", f.invoice), map[string]any{"provider": "epay_alipay", "expected_version": 1}, p.customer, prefix+"-local-refund-payment")
	expect(t, w, 201)
	checkout, err := url.Parse(data(t, w)["checkout_url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	f.callback = epayNotification(checkout.Query(), prefix+"-local-refund-trade")
	if w := epayCallback(p.h, "POST", "epay_alipay", f.callback); w.Code != 200 || w.Body.String() != "success" {
		t.Fatalf("settlement %d %s", w.Code, w.Body.String())
	}
	if err = p.db.Pool.QueryRow(t.Context(), `SELECT id FROM payment_transactions WHERE invoice_id=$1`, f.invoice).Scan(&f.payment); err != nil {
		t.Fatal(err)
	}
	return f
}
func localRenewalRefundContextPath(invoice int64) string {
	return fmt.Sprintf("/api/v1/admin/invoices/%d/external-refund-renewal-context", invoice)
}
func (f localRenewalRefundHTTPFixture) context(t *testing.T) map[string]any {
	t.Helper()
	w := request(f.h, "GET", localRenewalRefundContextPath(f.invoice), nil, f.requester, "")
	expect(t, w, 200)
	v := data(t, w)
	for _, key := range []string{"invoice_id", "invoice_version", "eligible", "reason_code", "reason", "effect", "service_version", "review_before"} {
		if _, ok := v[key]; !ok {
			t.Fatalf("context missing %s: %v", key, v)
		}
	}
	if v["invoice_id"] != float64(f.invoice) {
		t.Fatal("context escaped invoice", v)
	}
	if effect, ok := v["effect"].(map[string]any); ok {
		orderExternalRefundWhitelist(t, effect, "id", "service_id", "billing_cycle", "before_due", "after_due", "period_start", "before_used", "after_used")
	}
	return v
}
func (f localRenewalRefundHTTPFixture) input(t *testing.T, key string) map[string]any {
	t.Helper()
	c := f.context(t)
	if c["eligible"] != true {
		t.Fatalf("fixture not eligible: %v", c)
	}
	return map[string]any{"payment_id": f.payment, "amount_cents": 734, "method": "original_route", "provider_refund_id": key, "received_at": time.Now().UTC().Format(time.RFC3339Nano), "evidence_ref": "test://local-renewal/" + key, "evidence_sha256": strings.Repeat("b", 64), "note": "隔离本地续费退款证据，无真实出款", "expected_version": c["invoice_version"], "expected_renewal_effect_id": c["effect"].(map[string]any)["id"], "expected_renewal_service_version": c["service_version"]}
}
func (f localRenewalRefundHTTPFixture) create(t *testing.T, key string) map[string]any {
	t.Helper()
	w := request(f.h, "POST", orderExternalRefundPath(f.invoice), f.input(t, key), f.requester, key)
	expect(t, w, 201)
	return data(t, w)["refund"].(map[string]any)
}
func (f localRenewalRefundHTTPFixture) reviewPath(refund map[string]any, action string) string {
	return fmt.Sprintf("%s/%.0f/%s", orderExternalRefundPath(f.invoice), refund["id"], action)
}
func (f localRenewalRefundHTTPFixture) assertBusiness(t *testing.T, reversed bool) {
	t.Helper()
	var due time.Time
	var used, generation int64
	var automatic bool
	var baseline, status, paymentStatus string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT next_due_at,renewal_discounts_used,auto_renew,renewal_generation FROM services WHERE id=$1`, f.sid).Scan(&due, &used, &automatic, &generation); err != nil {
		t.Fatal(err)
	}
	if reversed {
		if !due.Equal(f.beforeDue) || used != 0 || automatic || generation != 1 {
			t.Fatalf("incorrect inverse due=%s used=%d auto=%v generation=%d", due, used, automatic, generation)
		}
	} else if !due.After(f.beforeDue) || used != 1 || !automatic || generation != 0 {
		t.Fatalf("rejection mutated service: due=%s used=%d auto=%v gen=%d", due, used, automatic, generation)
	}
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_array(to_jsonb(o),to_jsonb(p),u.balance_cents,(SELECT coalesce(jsonb_agg(to_jsonb(c)),'[]') FROM customer_credit_accounts c WHERE c.user_id=u.id),(SELECT coalesce(jsonb_agg(to_jsonb(l) ORDER BY l.id),'[]') FROM balance_ledger l WHERE l.user_id=u.id))::text FROM orders o JOIN products p ON p.id=$2 JOIN users u ON u.id=o.user_id WHERE o.id=$1`, f.orderID, f.pid).Scan(&baseline); err != nil || baseline != f.baseline {
		t.Fatal("external refund changed original order/product/stock/balance/credit/ledger", err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT i.status,p.status FROM invoices i JOIN payment_transactions p ON p.invoice_id=i.id WHERE i.id=$1`, f.invoice).Scan(&status, &paymentStatus); err != nil {
		t.Fatal(err)
	}
	want := "paid"
	if reversed {
		want = "refunded"
	}
	if status != want || paymentStatus != "succeeded" {
		t.Fatalf("invoice/payment %s/%s", status, paymentStatus)
	}

	summary := request(f.h, "GET", fmt.Sprintf("/api/v1/admin/transactions/summary?customer_id=%d", f.uid), nil, f.root, "")
	expect(t, summary, 200)
	cash := data(t, summary)
	outgoing := float64(0)
	if reversed {
		outgoing = 734
	}
	if cash["gateway_income_cents"] != float64(734) || cash["external_refund_cents"] != outgoing || cash["net_gateway_cents"] != 734-outgoing {
		t.Fatal("incorrect external cash accounting", cash)
	}
}

func TestLocalRenewalExternalRefundHTTPRoundTrip(t *testing.T) {
	for _, action := range []string{"confirm", "reject"} {
		t.Run(action, func(t *testing.T) {
			f := newLocalRenewalRefundHTTPFixture(t)
			in := f.input(t, "renewal-http-"+action)
			path := orderExternalRefundPath(f.invoice)
			w := request(f.h, "POST", path, in, f.requester, "renewal-http-create")
			expect(t, w, 201)
			refund := data(t, w)["refund"].(map[string]any)
			expect(t, request(f.h, "POST", path, in, f.requester, "renewal-http-create"), 200)
			changed := orderExternalRefundClone(in)
			changed["note"] = "different payload"
			expect(t, request(f.h, "POST", path, changed, f.requester, "renewal-http-create"), 409)
			review := map[string]any{"expected_version": 1, "note": "independent local renewal verification"}
			orderExternalRefundCode(t, request(f.h, "POST", f.reviewPath(refund, action), review, f.requester, "renewal-self-review"), 403, "external_refund_self_review")
			expect(t, request(f.h, "POST", f.reviewPath(refund, action), map[string]any{"expected_version": 99, "note": "stale version"}, f.reviewer, "renewal-stale-review"), 409)
			expect(t, request(f.h, "POST", f.reviewPath(refund, action), review, f.reviewer, "renewal-http-review"), 201)
			expect(t, request(f.h, "POST", f.reviewPath(refund, action), review, f.reviewer, "renewal-http-review"), 200)
			f.assertBusiness(t, action == "confirm")
			// A repeated signed success must preserve the refund and never extend again.
			if w := epayCallback(f.h, "POST", "epay_alipay", f.callback); w.Code != 200 || w.Body.String() != "success" {
				t.Fatalf("late callback %d %s", w.Code, w.Body.String())
			}
			f.assertBusiness(t, action == "confirm")
			list := request(f.h, "GET", path, nil, f.reviewer, "")
			expect(t, list, 200)
			rows := data(t, list)["items"].([]any)
			if len(rows) != 1 {
				t.Fatal("refund history lost", rows)
			}
			projection := rows[0].(map[string]any)["renewal_reversal"].(map[string]any)
			want := "applied"
			if action == "reject" {
				want = "released"
			}
			if projection["status"] != want {
				t.Fatal("reversal projection", projection)
			}
		})
	}
}

func TestLocalRenewalExternalRefundHTTPAdmission(t *testing.T) {
	f := newLocalRenewalRefundHTTPFixture(t)
	path := localRenewalRefundContextPath(f.invoice)
	expect(t, request(f.h, "GET", path, nil, "", ""), 401)
	expect(t, request(f.h, "GET", path, nil, f.customer, ""), 403)
	_, reader := externalRefundActor(t, f.db, f.h, "renewal-reader", []string{"external_refunds.view"})
	expect(t, request(f.h, "GET", path, nil, reader, ""), 200)
	_, limited := externalRefundActor(t, f.db, f.h, "renewal-limited", []string{"external_refunds.view"})
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE username='renewal-limited'`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "GET", path, nil, limited, ""), 404)
	_, noReverse := externalRefundActor(t, f.db, f.h, "renewal-no-reverse", []string{"external_refunds.view", "external_refunds.create", "external_refunds.review"})
	in := f.input(t, "renewal-admission-evidence")
	expect(t, request(f.h, "POST", orderExternalRefundPath(f.invoice), in, noReverse, "renewal-no-permission"), 403)
	for _, field := range []string{"expected_version", "expected_renewal_effect_id", "expected_renewal_service_version"} {
		bad := orderExternalRefundClone(in)
		bad[field] = float64(999999)
		expect(t, request(f.h, "POST", orderExternalRefundPath(f.invoice), bad, f.requester, "renewal-stale-"+field), 409)
	}
	expect(t, request(f.h, "GET", localRenewalRefundContextPath(99999999), nil, f.requester, ""), 404)
}

func TestLocalRenewalExternalRefundHTTPConcurrentReview(t *testing.T) {
	f := newLocalRenewalRefundHTTPFixture(t)
	refund := f.create(t, "renewal-race-create")
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes <- request(f.h, "POST", f.reviewPath(refund, "confirm"), map[string]any{"expected_version": 1, "note": "concurrent independent verification"}, f.reviewer, fmt.Sprintf("renewal-race-review-%d", i)).Code
		}(i)
	}
	wg.Wait()
	close(codes)
	seen := map[int]int{}
	for code := range codes {
		seen[code]++
	}
	if seen[201] != 1 || seen[409] != 1 {
		t.Fatal("review linearization", seen)
	}
	f.assertBusiness(t, true)
}

func TestLocalRenewalExternalRefundHTTPRevocationAndHold(t *testing.T) {
	f := newLocalRenewalRefundHTTPFixture(t)
	refund := f.create(t, "renewal-hold-create")
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", f.sid), map[string]any{}, f.customer, "renewal-under-hold"), 409)
	expect(t, request(f.h, "PATCH", fmt.Sprintf("/api/v1/services/%d/auto-renew", f.sid), map[string]any{"enabled": false}, f.customer, ""), 409)
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE admin_roles SET permissions=array_remove(permissions,'external_refunds.renewal_reverse') WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)`, f.requesterID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", f.reviewPath(refund, "reject"), map[string]any{"expected_version": 1, "note": "permission removed must deny"}, f.requester, "renewal-revoked-review"), 403)
	expect(t, request(f.h, "POST", f.reviewPath(refund, "reject"), map[string]any{"expected_version": 1, "note": "release persistent hold independently"}, f.reviewer, "renewal-release-hold"), 201)
	f.assertBusiness(t, false)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", f.sid), map[string]any{}, f.customer, "renewal-after-release"), 201)
}

func TestLocalRenewalExternalRefundHTTPAutoRenewNewGeneration(t *testing.T) {
	f := renewalPurchaseFixture(t, "fixed", 500, 2)
	// Both worker generations must target exactly the same original due. An
	// accidentally retained legacy idempotency key would replay a refunded bill.
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE services SET auto_renew=true,next_due_at=now()+interval '1 hour' WHERE id=$1`, f.sid); err != nil {
		t.Fatal(err)
	}
	var originalDue time.Time
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT next_due_at FROM services WHERE id=$1`, f.sid).Scan(&originalDue); err != nil {
		t.Fatal(err)
	}
	count, err := f.db.ProcessAutoRenewals(t.Context(), 20)
	if err != nil || count != 1 {
		t.Fatal("first auto-renew", count, err)
	}
	var invoice int64
	if err = f.db.Pool.QueryRow(t.Context(), `SELECT i.id FROM invoices i JOIN invoice_items ii ON ii.invoice_id=i.id WHERE ii.service_id=$1 AND ii.item_type='renew'`, f.sid).Scan(&invoice); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", invoice), map[string]any{}, f.root, "auto-generation-refund"), 201)
	limitedUsed(t, f, 0)
	expect(t, request(f.h, "PATCH", fmt.Sprintf("/api/v1/services/%d/auto-renew", f.sid), map[string]any{"enabled": true}, f.customer, ""), 200)
	count, err = f.db.ProcessAutoRenewals(t.Context(), 20)
	if err != nil || count != 1 {
		t.Fatal("new generation auto-renew", count, err)
	}
	var paid, refunded, generation int64
	var due time.Time
	if err = f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM invoices i JOIN invoice_items ii ON ii.invoice_id=i.id WHERE ii.service_id=s.id AND ii.item_type='renew' AND i.status='paid'),(SELECT count(*) FROM invoices i JOIN invoice_items ii ON ii.invoice_id=i.id WHERE ii.service_id=s.id AND ii.item_type='renew' AND i.status='refunded'),renewal_generation,next_due_at FROM services s WHERE s.id=$1`, f.sid).Scan(&paid, &refunded, &generation, &due); err != nil || paid != 1 || refunded != 1 || generation != 1 || !due.After(originalDue) {
		t.Fatalf("auto renewed refunded generation paid/refunded/gen=%d/%d/%d due=%s err=%v", paid, refunded, generation, due, err)
	}
	limitedUsed(t, f, 1)
}

func TestLocalRenewalExternalRefundHTTPPendingDependencyAndLIFO(t *testing.T) {
	f := newLocalRenewalRefundHTTPFixture(t)
	second := renewLimited(t, f.recurringPurchase, "renewal-lifo-second", 734)
	c := f.context(t)
	if c["eligible"] != false {
		t.Fatal("unpaid successor admitted", c)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", second), map[string]any{}, f.customer, "renewal-lifo-second-pay"), 201)
	c = f.context(t)
	if c["eligible"] != false {
		t.Fatal("settled successor admitted", c)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", second), map[string]any{}, f.root, "renewal-lifo-second-refund"), 201)
	// Successor is now provably reversed; the original remains refundable with
	// its original before/after due and benefit values, despite version changes.
	c = f.context(t)
	if c["eligible"] != true {
		t.Fatal("reversed successor blocks LIFO", c)
	}
	refund := f.create(t, "renewal-lifo-first-refund")
	expect(t, request(f.h, "POST", f.reviewPath(refund, "confirm"), map[string]any{"expected_version": 1, "note": "independent LIFO inverse"}, f.reviewer, "renewal-lifo-confirm"), 201)
	var due time.Time
	var used int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT next_due_at,renewal_discounts_used FROM services WHERE id=$1`, f.sid).Scan(&due, &used); err != nil || !due.Equal(f.beforeDue) || used != 0 {
		t.Fatal("LIFO inverse mismatch", due, used, err)
	}
}

func TestLocalRenewalExternalRefundHTTPCreateVersusRenewal(t *testing.T) {
	f := newLocalRenewalRefundHTTPFixture(t)
	in := f.input(t, "renewal-create-race-evidence")
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	start := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		codes <- request(f.h, "POST", orderExternalRefundPath(f.invoice), in, f.requester, "renewal-race-apply").Code
	}()
	go func() {
		defer wg.Done()
		<-start
		codes <- request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", f.sid), map[string]any{}, f.customer, "renewal-race-successor").Code
	}()
	close(start)
	wg.Wait()
	close(codes)
	seen := map[int]int{}
	for code := range codes {
		seen[code]++
	}
	if seen[201] != 1 || seen[409] != 1 {
		t.Fatal("refund admission versus successor not serialized", seen)
	}
}

func TestLocalRenewalExternalRefundHTTPAuditFailureRollback(t *testing.T) {
	f := newLocalRenewalRefundHTTPFixture(t)
	refund := f.create(t, "renewal-rollback-create")
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_renewal_review_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='external_refund.confirmed' THEN RAISE EXCEPTION 'isolated audit failure'; END IF; RETURN NEW; END $$;CREATE TRIGGER reject_renewal_review_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_renewal_review_audit()`); err != nil {
		t.Fatal(err)
	}
	w := request(f.h, "POST", f.reviewPath(refund, "confirm"), map[string]any{"expected_version": 1, "note": "independent review with injected audit failure"}, f.reviewer, "renewal-rollback-confirm")
	expect(t, w, 500)
	f.assertBusiness(t, false)
	var status string
	var operations int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT r.status,(SELECT count(*) FROM external_refund_operations WHERE idempotency_key='renewal-rollback-confirm') FROM external_refunds r WHERE id=$1`, int64(refund["id"].(float64))).Scan(&status, &operations); err != nil || status != "pending_review" || operations != 0 {
		t.Fatal("failed review left partial receipt", status, operations, err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `DROP TRIGGER reject_renewal_review_audit ON audit_events;DROP FUNCTION reject_renewal_review_audit()`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", f.reviewPath(refund, "confirm"), map[string]any{"expected_version": 1, "note": "independent review with injected audit failure"}, f.reviewer, "renewal-rollback-confirm"), 201)
	f.assertBusiness(t, true)
}

func TestLocalRenewalExternalRefundHTTPMonthEndExactInverse(t *testing.T) {
	p := renewalPurchaseFixture(t, "fixed", 500, 2)
	// PostgreSQL month arithmetic is not invertible at month ends. This future
	// January boundary proves the refund uses stored before_due, not subtraction.
	due := time.Date(time.Now().UTC().Year()+2, time.January, 31, 12, 34, 56, 0, time.UTC)
	if _, err := p.db.Pool.Exec(t.Context(), `UPDATE services SET next_due_at=$2 WHERE id=$1`, p.sid, due); err != nil {
		t.Fatal(err)
	}
	f := setupLocalRenewalRefundHTTPFixture(t, p, "", "month-end")
	refund := f.create(t, "renewal-month-end-create")
	expect(t, request(f.h, "POST", f.reviewPath(refund, "confirm"), map[string]any{"expected_version": 1, "note": "independent exact month-end inverse"}, f.reviewer, "renewal-month-end-confirm"), 201)
	f.assertBusiness(t, true)
}

func TestLocalRenewalExternalRefundHTTPRevocationAfterActualLockWait(t *testing.T) {
	for _, operation := range []string{"context", "create", "confirm", "replay"} {
		for _, revoke := range []string{"scope", "permission"} {
			t.Run(operation+"_"+revoke, func(t *testing.T) {
				f := newLocalRenewalRefundHTTPFixture(t)
				snapshotter := orderExternalRefundHTTPFixture{db: f.db}
				staffID, staff := externalRefundActor(t, f.db, f.h, "local-renewal-lock-staff", []string{"external_refunds.view", "external_refunds.create", "external_refunds.review", "external_refunds.renewal_reverse"})
				input := f.input(t, "local-renewal-lock-proof")
				path := orderExternalRefundPath(f.invoice)
				method := "POST"
				if operation == "confirm" {
					created := request(f.h, "POST", path, input, f.root, "local-renewal-lock-create")
					expect(t, created, 201)
					path = fmt.Sprintf("%s/%.0f/confirm", path, data(t, created)["refund"].(map[string]any)["id"])
					input = map[string]any{"expected_version": 1, "note": "independent locked review"}
				} else if operation == "replay" {
					expect(t, request(f.h, "POST", path, input, staff, "local-renewal-lock-write"), 201)
				} else if operation == "context" {
					method = "GET"
					path = localRenewalRefundContextPath(f.invoice)
					input = nil
				}
				before := snapshotter.snapshot(t)
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				tx, err := f.db.Pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(context.Background())
				var blocker int
				if err = tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blocker); err != nil {
					t.Fatal(err)
				}
				statement := `UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1`
				expected := 404
				if revoke == "permission" {
					statement = `UPDATE admin_roles SET permissions=array_remove(permissions,'external_refunds.renewal_reverse') WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)`
					expected = 403
					if operation == "context" {
						statement = `UPDATE admin_roles SET permissions=array_remove(permissions,'external_refunds.view') WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)`
					}
				}
				if _, err = tx.Exec(ctx, statement, staffID); err != nil {
					t.Fatal(err)
				}
				result := make(chan *httptest.ResponseRecorder, 1)
				go func() { result <- request(f.h, method, path, input, staff, "local-renewal-lock-write") }()
				for {
					var waiting bool
					if err = f.db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&waiting); err != nil {
						t.Fatal(err)
					}
					if waiting {
						t.Log("observed pg_blocking_pids dependency before revocation committed")
						break
					}
					select {
					case w := <-result:
						t.Fatalf("request did not wait: status=%d", w.Code)
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
					t.Fatal("request stuck after revocation commit")
				}
				orderExternalRefundUnchanged(t, before, snapshotter.snapshot(t))
			})
		}
	}
}
