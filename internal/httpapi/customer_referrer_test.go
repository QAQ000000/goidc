package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func TestCustomerReferrerQueryEncodingValidation(t *testing.T) {
	for _, raw := range []string{
		"q=\xff", "q=\x00", "q=%00", "q=%ff", "q=%c3%28", "q%00=customer",
		"q=customer&offset=%00", "q=customer&limit=%ff", "q=%", "q=a&q=b",
	} {
		t.Run(fmt.Sprintf("%q", raw), func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.URL.RawQuery = raw
			w := httptest.NewRecorder()
			if _, ok := customerReferrerQuery(w, r, "options"); ok || w.Code != http.StatusBadRequest {
				t.Fatal("malformed query was accepted", w.Code, w.Body.String())
			}
		})
	}
	for _, raw := range []string{"", "q=%E5%AE%A2%E6%88%B7&limit=5&offset=0", "q=100%25_客户"} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.URL.RawQuery = raw
		if _, ok := customerReferrerQuery(httptest.NewRecorder(), r, "options"); !ok {
			t.Fatal("valid Unicode or literal substring query was rejected", raw)
		}
	}
}

type referrerFixture struct {
	affiliateSettlementFixture
	actor, nextID, nextAccount int64
}

func newReferrerFixture(t *testing.T) referrerFixture {
	t.Helper()
	f := referrerFixture{affiliateSettlementFixture: settlementFixture(t)}
	// An ordinary role proves both permissions, scopes and credential checks;
	// the existing super administrator is used only to approve the new account.
	var manager string
	f.actor, manager = user(t, f.db, f.h, "referrer-manager", "admin")
	f.nextID, f.nextAccount = referrerApprovedCustomer(t, f, "referrer-next")
	f.admin = manager
	contentRole(t, f.db, f.actor, "customers.manage", "affiliates.manage", "api_keys.manage")
	return f
}

func referrerApprovedCustomer(t *testing.T, f referrerFixture, name string) (int64, int64) {
	t.Helper()
	id, cookie := user(t, f.db, f.h, name, "customer")
	expect(t, request(f.h, "POST", "/api/v1/affiliate/apply", nil, cookie, name+"-apply"), 201)
	var account int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT id FROM affiliate_accounts WHERE user_id=$1", id).Scan(&account); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/affiliate/accounts/%d/status", account), map[string]any{"status": "approved", "expected_version": 1}, f.admin, name+"-approve"), 200)
	return id, account
}

func (f referrerFixture) endpoint(customer int64, suffix string) string {
	return fmt.Sprintf("/api/v1/admin/customers/%d/referrer%s", customer, suffix)
}
func (f referrerFixture) put(customer int64, target any, version int64, key, bearer string) *httptest.ResponseRecorder {
	return f.call("PUT", f.endpoint(customer, ""), map[string]any{"referrer_customer_id": target, "expected_version": version, "reason": "人工核对推荐关系"}, key, bearer)
}
func (f referrerFixture) call(method, path string, body any, key, bearer string) *httptest.ResponseRecorder {
	cookie := f.admin
	headers := map[string]string{}
	if key != "" {
		headers["Idempotency-Key"] = key
	}
	if bearer != "" {
		cookie = ""
		headers["Authorization"] = "Bearer " + bearer
	}
	return requestFrom(f.h, method, path, body, "192.0.2.89:5678", cookie, headers)
}
func (f referrerFixture) version(t *testing.T, customer int64) int64 {
	t.Helper()
	w := f.call("GET", f.endpoint(customer, ""), nil, "", "")
	expect(t, w, 200)
	if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("referral response may be cached")
	}
	return int64(data(t, w)["referral_version"].(float64))
}
func (f referrerFixture) snapshot(t *testing.T) string {
	t.Helper()
	var out string
	err := f.db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'attributions',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM affiliate_attributions x),
 'versions',(SELECT jsonb_agg(to_jsonb(x) ORDER BY user_id) FROM customer_referral_states x),
 'history',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM customer_referral_changes x),
 'invoices',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM invoices x),
 'births',(SELECT jsonb_agg(to_jsonb(x) ORDER BY invoice_id) FROM invoice_affiliate_attributions x),
 'commissions',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM affiliate_commission_entries x),
 'turnover',(SELECT jsonb_agg(to_jsonb(x) ORDER BY invoice_id) FROM affiliate_turnover_entries x),
 'withdrawals',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM affiliate_withdrawals x),
 'ledger',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM balance_ledger x),
 'audits',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM audit_events x WHERE action='customer.referrer.changed'),
 'receipts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY actor_id,scope,idempotency_key) FROM mutation_operations x WHERE scope LIKE 'admin:customer.referrer:%'))::text`).Scan(&out)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func (f referrerFixture) unchanged(t *testing.T, before string) {
	t.Helper()
	if f.snapshot(t) != before {
		t.Fatal("rejected or replayed referrer operation changed committed referral or financial facts")
	}
}
func (f referrerFixture) unpaid(t *testing.T, n int) int64 {
	t.Helper()
	pid := product(t, f.db, 10)
	version, item := add(t, f.h, f.customer, pid, 1)
	w := request(f.h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{item}}, f.customer, fmt.Sprintf("referrer-order-%d", n))
	expect(t, w, 201)
	return int64(data(t, w)["invoice_id"].(float64))
}
func (f referrerFixture) paid(t *testing.T, invoice int64) {
	t.Helper()
	key := fmt.Sprintf("referrer-pay-%d", invoice)
	path := fmt.Sprintf("/api/v1/invoices/%d/pay", invoice)
	expect(t, request(f.h, "POST", path, map[string]any{}, f.customer, key), 201)
	expect(t, request(f.h, "POST", path, map[string]any{}, f.customer, key), 200)
}
func (f referrerFixture) invoiceAccount(t *testing.T, invoice int64, account *int64, financial bool) {
	t.Helper()
	var got *int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT account_id FROM invoice_affiliate_attributions WHERE invoice_id=$1", invoice).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if (got == nil) != (account == nil) || (got != nil && *got != *account) {
		t.Fatalf("invoice %d birth account=%v expected=%v", invoice, got, account)
	}
	if !financial {
		return
	}
	for _, table := range []string{"affiliate_turnover_entries", "affiliate_commission_entries"} {
		var count, wrong int
		if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*),count(*) FILTER(WHERE account_id IS DISTINCT FROM $2::bigint) FROM "+table+" WHERE invoice_id=$1", invoice, account).Scan(&count, &wrong); err != nil {
			t.Fatal(err)
		}
		if wrong != 0 || (account == nil && count != 0) || (account != nil && count != 1) {
			t.Fatalf("invoice %d table=%s count=%d wrong=%d", invoice, table, count, wrong)
		}
	}
}

func TestCustomerReferrerMutationVersionsReplayAndRollback(t *testing.T) {
	f := newReferrerFixture(t)
	version := f.version(t, f.customerID)
	first := f.put(f.customerID, f.nextID, version, "referrer-set-first", "")
	expect(t, first, 200)
	if got := data(t, first); got["referral_version"] != float64(version+1) || got["changed"] != true || got["replayed"] != false {
		t.Fatal("invalid committed referral", got)
	}
	before := f.snapshot(t)
	replay := f.put(f.customerID, f.nextID, version, "referrer-set-first", "")
	expect(t, replay, 200)
	if data(t, replay)["replayed"] != true || data(t, replay)["referral_version"] != float64(version+1) {
		t.Fatal("durable referral receipt changed", replay.Body.String())
	}
	f.unchanged(t, before)
	expect(t, f.put(f.customerID, nil, version, "referrer-set-first", ""), 409)
	expect(t, f.put(f.customerID, nil, version, "referrer-stale-version", ""), 409)
	f.unchanged(t, before)
	noop := f.put(f.customerID, f.nextID, version+1, "referrer-noop-key", "")
	expect(t, noop, 200)
	if data(t, noop)["changed"] != false || data(t, noop)["referral_version"] != float64(version+1) {
		t.Fatal("same referrer must not advance version")
	}
	expect(t, f.put(f.customerID, nil, version+1, "referrer-clear-key", ""), 200)
	expect(t, f.put(f.customerID, f.ownerID, version+2, "referrer-rebind-key", ""), 200)
	// A historical receipt returns its committed version rather than the latest.
	old := f.put(f.customerID, f.nextID, version, "referrer-set-first", "")
	expect(t, old, 200)
	if data(t, old)["referral_version"] != float64(version+1) || data(t, old)["referrer_customer_id"] != float64(f.nextID) {
		t.Fatal("replay returned current referral instead of original receipt")
	}
	for _, suffix := range []string{"/history?limit=1", "/options?limit=1&q=referrer", "/history?limit=1&offset=10"} {
		w := f.call("GET", f.endpoint(f.customerID, suffix), nil, "", "")
		expect(t, w, 200)
		if data(t, w)["total"] == nil || len(data(t, w)["items"].([]any)) > 1 {
			t.Fatal("invalid referral page", w.Body.String())
		}
	}
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION fail_referrer_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='customer.referrer.changed' THEN RAISE EXCEPTION 'test referrer audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_referrer_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION fail_referrer_audit()`); err != nil {
		t.Fatal(err)
	}
	before = f.snapshot(t)
	expect(t, f.put(f.customerID, f.nextID, version+3, "referrer-audit-rollback", ""), 500)
	f.unchanged(t, before)
}

func TestCustomerReferrerInvoiceBirthAndRefundOwnership(t *testing.T) {
	f := newReferrerFixture(t)
	old := f.unpaid(t, 1)
	version := f.version(t, f.customerID)
	expect(t, f.put(f.customerID, f.nextID, version, "referrer-financial-rebind", ""), 200)
	next := f.unpaid(t, 2)
	expect(t, f.put(f.customerID, nil, version+1, "referrer-financial-clear", ""), 200)
	none := f.unpaid(t, 3)
	expect(t, f.put(f.customerID, f.ownerID, version+2, "referrer-financial-restore", ""), 200)
	rebound := f.unpaid(t, 4)
	for _, invoice := range []int64{old, next, none, rebound} {
		f.paid(t, invoice)
	}
	f.invoiceAccount(t, old, &f.accountID, true)
	f.invoiceAccount(t, next, &f.nextAccount, true)
	f.invoiceAccount(t, none, nil, true)
	f.invoiceAccount(t, rebound, &f.accountID, true)
	// Change ownership again before refund; reversal must still debit the
	// account that earned this old invoice, never the current referrer.
	expect(t, f.put(f.customerID, f.nextID, version+3, "referrer-before-refund", ""), 200)
	_, root := user(t, f.db, f.h, "referrer-refund-root", "super_admin")
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", old), map[string]any{}, root, "referrer-refund-old-invoice"), 201)
	var status string
	var account int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT account_id,status FROM affiliate_commission_entries WHERE invoice_id=$1", old).Scan(&account, &status); err != nil || account != f.accountID || status != "reversed" {
		t.Fatal("refund changed original commission ownership", account, status, err)
	}
	f.invoiceAccount(t, old, &f.accountID, true)
	f.invoiceAccount(t, next, &f.nextAccount, true)
}

func TestCustomerReferrerPermissionsScopeMaskingAndValidation(t *testing.T) {
	f := newReferrerFixture(t)
	version := f.version(t, f.customerID)
	// Record a visible no-op whose historical receipt will later be redacted.
	expect(t, f.put(f.customerID, f.ownerID, version, "referrer-visible-receipt", ""), 200)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", f.actor); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$1 WHERE id IN ($2,$3)", f.actor, f.customerID, f.nextID); err != nil {
		t.Fatal(err)
	}
	current := f.call("GET", f.endpoint(f.customerID, ""), nil, "", "")
	expect(t, current, 200)
	if got := data(t, current); got["attributed"] != true || got["referrer_visible"] != false || got["referrer_customer_id"] != nil || got["referrer_name"] != nil {
		t.Fatal("current hidden referrer identity leaked", got)
	}
	replay := f.put(f.customerID, f.ownerID, version, "referrer-visible-receipt", "")
	expect(t, replay, 200)
	if got := data(t, replay); got["referrer_visible"] != false || got["referrer_customer_id"] != nil || got["referrer_name"] != nil {
		t.Fatal("historical receipt leaked now-hidden referrer", got)
	}
	before := f.snapshot(t)
	expect(t, f.put(f.customerID, f.ownerID, version, "referrer-hidden-new-write", ""), 404)
	f.unchanged(t, before)
	expect(t, f.put(f.customerID, f.nextID, version, "referrer-visible-next", ""), 200)
	history := f.call("GET", f.endpoint(f.customerID, "/history"), nil, "", "")
	expect(t, history, 200)
	entry := data(t, history)["items"].([]any)[0].(map[string]any)
	if entry["old_attributed"] != true || entry["old_referrer_visible"] != false || entry["old_referrer_customer_id"] != nil || entry["old_referrer_name"] != nil || entry["previous_attribution"] != nil {
		t.Fatal("history leaked hidden referrer", entry)
	}
	for _, permission := range []string{"customers.manage", "affiliates.manage"} {
		granted := "customers.manage"
		if permission == "customers.manage" {
			granted = "affiliates.manage"
		}
		contentRole(t, f.db, f.actor, granted)
		before = f.snapshot(t)
		for _, suffix := range []string{"", "/options", "/history"} {
			expect(t, f.call("GET", f.endpoint(f.customerID, suffix), nil, "", ""), 403)
		}
		expect(t, f.put(f.customerID, nil, version+1, "referrer-missing-permission", ""), 403)
		f.unchanged(t, before)
	}
	contentRole(t, f.db, f.actor, "customers.manage", "affiliates.manage")
	before = f.snapshot(t)
	for _, suffix := range []string{"?limit=1", "/options?limit=0", "/options?limit=1&limit=2", "/options?offset=-1", "/history?q=x", "/options?unknown=1", "/options?q=%00", "/options?q=%ff"} {
		expect(t, f.call("GET", f.endpoint(f.customerID, suffix), nil, "", ""), 400)
	}
	for _, target := range []any{0, "1", f.customerID} {
		want := 400
		if target == f.customerID {
			want = 409
		}
		expect(t, f.put(f.customerID, target, version+1, "referrer-invalid-target", ""), want)
	}
	expect(t, f.call("PUT", f.endpoint(f.customerID, ""), map[string]any{"expected_version": version + 1, "reason": "missing target"}, "referrer-missing-target", ""), 400)
	f.unchanged(t, before)
}

func TestCustomerReferrerConcurrentCycleAndCAS(t *testing.T) {
	f := newReferrerFixture(t)
	ownerVersion, nextVersion := f.version(t, f.ownerID), f.version(t, f.nextID)
	gate, err := f.db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback(context.Background())
	if _, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended('affiliate-referral-graph',0))"); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 2)
	go func() { done <- f.put(f.ownerID, f.nextID, ownerVersion, "referrer-cycle-a", "") }()
	go func() { done <- f.put(f.nextID, f.ownerID, nextVersion, "referrer-cycle-b", "") }()
	waitContactBlockedCount(t, f.db, gate.Conn().PgConn().PID(), 2)
	if err = gate.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	statuses := map[int]int{}
	for range 2 {
		statuses[awaitBalanceResponse(t, done).Code]++
	}
	if statuses[200] != 1 || statuses[409] != 1 {
		t.Fatal("concurrent assignments created a cycle or deadlocked", statuses)
	}
	var edges int
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM affiliate_attributions WHERE user_id IN ($1,$2) AND status='active'", f.ownerID, f.nextID).Scan(&edges); err != nil || edges != 1 {
		t.Fatal("cycle loser committed an attribution", edges, err)
	}
	// Two genuine requests observing the same version cannot both win CAS.
	version := f.version(t, f.customerID)
	gate, err = f.db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback(context.Background())
	if _, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended('affiliate-referral-graph',0))"); err != nil {
		t.Fatal(err)
	}
	go func() { done <- f.put(f.customerID, nil, version, "referrer-cas-a", "") }()
	go func() { done <- f.put(f.customerID, nil, version, "referrer-cas-b", "") }()
	waitContactBlockedCount(t, f.db, gate.Conn().PgConn().PID(), 2)
	if err = gate.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	statuses = map[int]int{}
	for range 2 {
		statuses[awaitBalanceResponse(t, done).Code]++
	}
	if statuses[200] != 1 || statuses[409] != 1 {
		t.Fatal("concurrent stale referral CAS both committed", statuses)
	}
}

func TestCustomerReferrerCredentialExpiryAtGraphAuditAndReceiptWait(t *testing.T) {
	for _, credential := range []string{"session", "api_key"} {
		for _, stage := range []string{"credential", "graph", "history", "audit", "receipt"} {
			t.Run(credential+"/"+stage, func(t *testing.T) {
				f := newReferrerFixture(t)
				version := f.version(t, f.customerID)
				secret := ""
				table, column, hash := "sessions", "token_hash", tokenHash(cookieValue(f.admin))
				if credential == "api_key" {
					keyFixture := adminBalanceFixture{db: f.db, h: f.h, actor: f.actor, cookie: f.admin}
					secret = keyFixture.apiKey(t)
					table, column, hash = "api_keys", "secret_hash", tokenHash(secret)
				}
				if _, err := f.db.Pool.Exec(t.Context(), "UPDATE "+table+" SET expires_at=clock_timestamp()+interval '1 second' WHERE "+column+"=$1", hash); err != nil {
					t.Fatal(err)
				}
				before := f.snapshot(t)
				gate, err := f.db.Pool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer gate.Rollback(context.Background())
				const key = "referrer-expiry-stable-key"
				barrier := "referrer-expiry-barrier"
				switch stage {
				case "credential":
					_, err = gate.Exec(t.Context(), "SELECT user_id FROM "+table+" WHERE "+column+"=$1 FOR UPDATE", hash)
				case "graph":
					_, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended('affiliate-referral-graph',0))")
				case "receipt":
					_, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", fmt.Sprintf("mutation:%d:admin:customer.referrer:%d:%s", f.actor, f.customerID, key))
				default:
					_, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", barrier)
					if err == nil {
						target := "customer_referral_changes"
						condition := "true"
						if stage == "audit" {
							target, condition = "audit_events", "NEW.action='customer.referrer.changed'"
						}
						_, err = f.db.Pool.Exec(t.Context(), "CREATE FUNCTION wait_referrer_expiry() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF "+condition+" THEN PERFORM pg_advisory_xact_lock(hashtextextended('"+barrier+"',0)); END IF; RETURN NEW; END $$; CREATE TRIGGER wait_referrer_expiry BEFORE INSERT ON "+target+" FOR EACH ROW EXECUTE FUNCTION wait_referrer_expiry()")
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- f.put(f.customerID, f.nextID, version, key, secret) }()
				waitUploadBlockedBy(t, f.db, gate.Conn().PgConn().PID())
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				for {
					var expired bool
					if err = f.db.Pool.QueryRow(ctx, "SELECT expires_at<=clock_timestamp() FROM "+table+" WHERE "+column+"=$1", hash).Scan(&expired); err != nil {
						t.Fatal(err)
					}
					if expired {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal("referrer credential did not expire")
					case <-time.After(10 * time.Millisecond):
					}
				}
				if err = gate.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				expect(t, awaitBalanceResponse(t, done), 401)
				f.unchanged(t, before)
			})
		}
	}
}

type referralInvoiceResult struct {
	id  int64
	err error
}

func awaitReferralInvoice(t *testing.T, done <-chan referralInvoiceResult) int64 {
	t.Helper()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		return result.id
	case <-time.After(7 * time.Second):
		t.Fatal("invoice referral snapshot stalled after graph lock release")
		return 0
	}
}

func TestCustomerReferrerInvoiceBirthAfterGraphWaitUsesFreshCommittedAttribution(t *testing.T) {
	for _, initiallyAttributed := range []bool{true, false} {
		t.Run(fmt.Sprint(initiallyAttributed), func(t *testing.T) {
			f := newReferrerFixture(t)
			version := f.version(t, f.customerID)
			if !initiallyAttributed {
				expect(t, f.put(f.customerID, nil, version, "referrer-before-wait-clear", ""), 200)
				version++
			}
			var before int64
			if err := f.db.Pool.QueryRow(t.Context(), "INSERT INTO invoices(invoice_no,user_id,amount_cents) VALUES('referral-before-graph-wait',$1,1000) RETURNING id", f.customerID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			gate, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback(context.Background())
			if _, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended('referrer-birth-audit-gate',0))"); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Pool.Exec(t.Context(), `CREATE FUNCTION wait_referrer_birth() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='customer.referrer.changed' THEN PERFORM pg_advisory_xact_lock(hashtextextended('referrer-birth-audit-gate',0)); END IF; RETURN NEW; END $$; CREATE TRIGGER wait_referrer_birth BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION wait_referrer_birth()`); err != nil {
				t.Fatal(err)
			}
			changed := make(chan *httptest.ResponseRecorder, 1)
			go func() { changed <- f.put(f.customerID, f.nextID, version, "referrer-birth-change", "") }()
			waitUploadBlockedBy(t, f.db, gate.Conn().PgConn().PID())
			inserted := make(chan referralInvoiceResult, 1)
			go func() {
				tx, err := f.db.Pool.Begin(t.Context())
				if err != nil {
					inserted <- referralInvoiceResult{err: err}
					return
				}
				defer tx.Rollback(context.Background())
				var isolation string
				err = tx.QueryRow(t.Context(), "SHOW transaction_isolation").Scan(&isolation)
				if err == nil && isolation != "read committed" {
					err = fmt.Errorf("invoice fixture expected READ COMMITTED, got %s", isolation)
				}
				if err == nil {
					err = store.LockFinancialCustomer(t.Context(), tx, f.customerID)
				}
				var invoice int64
				if err == nil {
					err = tx.QueryRow(t.Context(), "INSERT INTO invoices(invoice_no,user_id,amount_cents) VALUES('referral-after-graph-wait',$1,1000) RETURNING id", f.customerID).Scan(&invoice)
				}
				if err == nil {
					err = tx.Commit(t.Context())
				}
				inserted <- referralInvoiceResult{invoice, err}
			}()
			// The invoice INSERT starts its statement snapshot before the change
			// commits, then really waits on the graph held by the API transaction.
			waitContactBlockedCount(t, f.db, gate.Conn().PgConn().PID(), 2)
			if err = gate.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			expect(t, awaitBalanceResponse(t, changed), 200)
			after := awaitReferralInvoice(t, inserted)
			if initiallyAttributed {
				f.invoiceAccount(t, before, &f.accountID, false)
			} else {
				f.invoiceAccount(t, before, nil, false)
			}
			f.invoiceAccount(t, after, &f.nextAccount, false)
		})
	}
}

func TestCustomerReferrerInvoiceSharedBirthLockOrdersLaterAssignment(t *testing.T) {
	f := newReferrerFixture(t)
	version := f.version(t, f.customerID)
	birth, err := f.db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer birth.Rollback(context.Background())
	if err = store.LockFinancialCustomer(t.Context(), birth, f.customerID); err != nil {
		t.Fatal(err)
	}
	var invoice int64
	if err = birth.QueryRow(t.Context(), "INSERT INTO invoices(invoice_no,user_id,amount_cents) VALUES('referral-shared-birth',$1,1000) RETURNING id", f.customerID).Scan(&invoice); err != nil {
		t.Fatal(err)
	}
	changed := make(chan *httptest.ResponseRecorder, 1)
	go func() { changed <- f.put(f.customerID, f.nextID, version, "referrer-after-shared-birth", "") }()
	waitUploadBlockedBy(t, f.db, birth.Conn().PgConn().PID())
	if err = birth.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	expect(t, awaitBalanceResponse(t, changed), 200)
	f.invoiceAccount(t, invoice, &f.accountID, false)
	var after int64
	if err = f.db.Pool.QueryRow(t.Context(), "INSERT INTO invoices(invoice_no,user_id,amount_cents) VALUES('referral-shared-birth-after',$1,1000) RETURNING id", f.customerID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	f.invoiceAccount(t, after, &f.nextAccount, false)
}

func TestCustomerReferrerCandidateScopePrecedesGraphThreePartyLockOrder(t *testing.T) {
	f := newReferrerFixture(t)
	version := f.version(t, f.customerID)
	_, salesAdmin := user(t, f.db, f.h, "referrer-lock-sales-root", "super_admin")
	// T3: a normal invoice writer first owns the candidate customer's finance
	// row. T2 takes that customer's sales scope and waits for its users row.
	// T1 must wait for the candidate scope before acquiring the graph; T3 can
	// therefore finish birth, releasing T2, then T1. The old order deadlocks.
	finance, err := f.db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer finance.Rollback(context.Background())
	if err = store.LockFinancialCustomer(t.Context(), finance, f.nextID); err != nil {
		t.Fatal(err)
	}
	sales := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		sales <- request(f.h, "PUT", fmt.Sprintf("/api/v1/admin/customers/%d/sales-owner", f.nextID), map[string]any{"sales_owner_id": nil, "expected_version": 1}, salesAdmin, "referrer-three-party-sales")
	}()
	waitUploadBlockedBy(t, f.db, finance.Conn().PgConn().PID())
	changed := make(chan *httptest.ResponseRecorder, 1)
	go func() { changed <- f.put(f.customerID, f.nextID, version, "referrer-three-party-change", "") }()
	waitContactBlockedCount(t, f.db, finance.Conn().PgConn().PID(), 2)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var invoice int64
	if err = finance.QueryRow(ctx, "INSERT INTO invoices(invoice_no,user_id,amount_cents) VALUES('referral-three-party-birth',$1,1000) RETURNING id", f.nextID).Scan(&invoice); err != nil {
		t.Fatal("invoice birth was blocked by a graph held behind its sales scope", err)
	}
	if err = finance.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	expect(t, awaitBalanceResponse(t, sales), 200)
	expect(t, awaitBalanceResponse(t, changed), 200)
	f.invoiceAccount(t, invoice, nil, false)
}

func TestCustomerReferrerRevocationAfterAuthorizationWait(t *testing.T) {
	for _, scenario := range []string{"customers_permission", "affiliate_permission", "session", "api_key", "scope", "replay"} {
		for _, method := range []string{"GET", "PUT"} {
			t.Run(scenario+"/"+method, func(t *testing.T) {
				f := newReferrerFixture(t)
				version := f.version(t, f.customerID)
				const key = "referrer-revoked-operation"
				if scenario == "replay" {
					expect(t, f.put(f.customerID, f.nextID, version, key, ""), 200)
				}
				secret := ""
				if scenario == "api_key" {
					secret = (adminBalanceFixture{db: f.db, h: f.h, actor: f.actor, cookie: f.admin}).apiKey(t)
				}
				before := f.snapshot(t)
				gate, err := f.db.Pool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer gate.Rollback(context.Background())
				if _, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(70108190)"); err != nil {
					t.Fatal(err)
				}
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					if method == "GET" {
						done <- f.call("GET", f.endpoint(f.customerID, ""), nil, "", secret)
					} else {
						done <- f.put(f.customerID, f.nextID, version, key, secret)
					}
				}()
				waitUploadBlockedBy(t, f.db, gate.Conn().PgConn().PID())
				status := 403
				switch scenario {
				case "customers_permission", "affiliate_permission", "replay":
					permission := "affiliates.manage"
					if scenario == "customers_permission" {
						permission = "customers.manage"
					}
					_, err = gate.Exec(t.Context(), "UPDATE admin_roles SET permissions=array_remove(permissions,$2) WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", f.actor, permission)
				case "session":
					_, err = gate.Exec(t.Context(), "UPDATE sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE token_hash=$1", tokenHash(cookieValue(f.admin)))
					status = 401
				case "api_key":
					_, err = gate.Exec(t.Context(), "UPDATE api_keys SET revoked_at=clock_timestamp() WHERE secret_hash=$1", tokenHash(secret))
					status = 401
				case "scope":
					_, err = gate.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", f.actor)
					status = 404
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = gate.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				expect(t, awaitBalanceResponse(t, done), status)
				f.unchanged(t, before)
			})
		}
	}
}
